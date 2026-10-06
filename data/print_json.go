// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/printer_json.c (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// jsonPrinter is jsonpr_ctx. The sibling lists are passed with the node index where libyang uses
// lyd_node.next / prev.
type jsonPrinter struct {
	*printer
	levelPrinted int     // level where some data were already printed (LEVEL_PRINTED)
	open         []*Node // open arrays, by their first printed node
	parent       *Node   // parent of the node being printed
	firstLL      *Node   // first printed leaf-list instance with metadata to print after the array
	firstLLSibs  []*Node
	firstLLIdx   int
}

// printJSON is json_print_data over the top-level siblings.
func printJSON(p *printer, top []*Node) error {
	if len(top) == 0 {
		p.printf("{}%s", p.nl())
		return nil
	}
	j := &jsonPrinter{printer: p}
	p.level = 1
	p.printf("{%s", p.nl())
	for i := range top {
		if err := j.node(top, i); err != nil {
			return err
		}
	}
	p.printf("%s}%s", p.nl(), p.nl())
	return nil
}

// levelDone is LEVEL_PRINTED.
func (j *jsonPrinter) levelDone() { j.levelPrinted = j.level }

// comma is PRINT_COMMA.
func (j *jsonPrinter) comma() {
	if j.levelPrinted >= j.level {
		j.printf(",%s", j.nl())
	}
}

// matching is matching_node: same schema node, for opaque nodes the same name and prefix.
func matching(a, b *Node) bool {
	switch {
	case a == nil || b == nil:
		return false
	case a.schema != b.schema:
		return false
	case a.schema == nil:
		return a.opaq.Name == b.opaq.Name && a.opaq.Prefix == b.opaq.Prefix
	}
	return true
}

func (j *jsonPrinter) isOpenArray(n *Node) bool {
	return len(j.open) > 0 && matching(n, j.open[len(j.open)-1])
}

func (j *jsonPrinter) arrayOpen(n *Node) {
	j.printf("[%s", j.nl())
	j.open = append(j.open, n)
	j.level++
}

func (j *jsonPrinter) arrayClose() {
	j.level--
	j.open = j.open[:len(j.open)-1]
	j.printf("%s%s]", j.nl(), j.indent())
}

// nodeModule is node_prefix: the module name of a node, "" if an opaque node has none the set knows.
func (j *jsonPrinter) nodeModule(n *Node) string {
	if n.schema != nil {
		return n.schema.Module.Name
	}
	if n.opaq.XML {
		if n.opaq.ModuleNS != "" {
			if m := j.set.ByNamespace(n.opaq.ModuleNS); m != nil {
				return m.Name
			}
		}
		return ""
	}
	return n.opaq.ModuleNS
}

// nsDiffers is json_nscmp for a printed schema node (snode) under parent: true when the member
// needs its module name.
func (j *jsonPrinter) nsDiffers(snode *schema.Node, parent *Node) bool {
	if snode == nil || parent == nil {
		return true
	}
	if parent.schema != nil {
		return snode.Module != parent.schema.Module
	}
	pm := j.nodeModule(parent)
	return pm == "" || pm != snode.Module.Name
}

// member is json_print_member: the member name of n (attr: the metadata '@').
func (j *jsonPrinter) member(n *Node, attr bool) {
	j.comma()
	at := ""
	if attr {
		at = "@"
	}
	sep := ""
	if j.format() {
		sep = " "
	}
	if j.nsDiffers(n.schema, j.parent) {
		j.printf("%s\"%s%s:%s\":%s", j.indent(), at, j.nodeModule(n), n.Name(), sep)
	} else {
		j.printf("%s\"%s%s\":%s", j.indent(), at, n.Name(), sep)
	}
}

// memberOpaq is json_print_member2 for an opaque node name (name nil: the empty "@" member).
func (j *jsonPrinter) memberOpaq(parent, n *Node, attr bool) {
	j.comma()
	at := ""
	if attr {
		at = "@"
	}
	sep := ""
	if j.format() {
		sep = " "
	}
	mod, name := "", ""
	if n != nil {
		mod, name = j.opaqModule(n), n.opaq.Name
	}
	pmod := ""
	if parent != nil {
		pmod = j.nodeModule(parent)
	}
	if mod != "" && (parent == nil || pmod == "" || pmod != mod) {
		j.printf("%s\"%s%s:%s\":%s", j.indent(), at, mod, name, sep)
	} else {
		j.printf("%s\"%s%s\":%s", j.indent(), at, name, sep)
	}
}

// opaqModule is the module name of an opaque node's name (JSON: as given, XML: by namespace).
func (j *jsonPrinter) opaqModule(n *Node) string { return j.nodeModule(n) }

// str is json_print_string.
func (j *jsonPrinter) str(s string) {
	j.buf.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			j.buf.WriteString(`\"`)
		case c == '\\':
			j.buf.WriteString(`\\`)
		case c == '\r':
			j.buf.WriteString(`\r`)
		case c == '\t':
			j.buf.WriteString(`\t`)
		case c < 0x20 || c == 0x7f: // iscntrl
			j.printf(`\u%04X`, c)
		default:
			j.buf.WriteByte(c)
		}
	}
	j.buf.WriteByte('"')
}

// value is json_print_value: the stored value in JSON form, by the base type of the stored (union:
// selected) type; local is the module of the node (its prefix is left out of identities).
func (j *jsonPrinter) value(v types.Value, local *schema.Module) error {
	text, err := types.Print(v, types.FormatJSON, &types.PrintCtx{Local: local})
	if err != nil {
		return err
	}
	switch leafBase(v) {
	case schema.Binary, schema.String, schema.Bits, schema.Enumeration, schema.InstanceID, schema.Int64,
		schema.Uint64, schema.Dec64, schema.IdentityRef:
		j.str(text)
	case schema.Int8, schema.Int16, schema.Int32, schema.Uint8, schema.Uint16, schema.Uint32, schema.Bool:
		if text == "" {
			text = "null"
		}
		j.buf.WriteString(text)
	case schema.Empty:
		j.buf.WriteString("[null]")
	default:
		return fmt.Errorf("data: no JSON form for a %s value", leafBase(v))
	}
	return nil
}

// metadata is json_print_metadata (without the with-defaults tag).
func (j *jsonPrinter) metadata(n *Node) error {
	sep := ""
	if j.format() {
		sep = " "
	}
	for _, m := range n.meta {
		j.comma()
		j.printf("%s\"%s:%s\":%s", j.indent(), m.mod.Name, m.name, sep)
		if err := j.value(m.value, nil); err != nil {
			return err
		}
		j.levelDone()
	}
	return nil
}

// attributes is json_print_attributes: the metadata of a schema node as a "@" object (inner nodes,
// inside their object) or as an "@name" sibling member (terms).
func (j *jsonPrinter) attributes(n *Node, inner bool) error {
	if n.schema == nil || !hasPrintableMeta(n) {
		return nil
	}
	if inner {
		j.memberOpaq(n.parent, nil, true)
	} else {
		j.member(n, true)
	}
	j.printf("{%s", j.nl())
	j.level++
	err := j.metadata(n)
	j.level--
	j.printf("%s%s}", j.nl(), j.indent())
	j.levelDone()
	return err
}

func (j *jsonPrinter) leaf(n *Node) error {
	j.member(n, false)
	if err := j.value(n.value, n.schema.Module); err != nil {
		return err
	}
	j.levelDone()
	return j.attributes(n, false) // as a sibling
}

// inner is json_print_inner: the object of a container, list instance or opaque inner node.
func (j *jsonPrinter) inner(n *Node) error {
	children := kids(n)
	hasContent := len(n.meta) > 0 || len(children) > 0
	isList := (n.schema != nil && n.schema.Kind == schema.List)
	comma := ""
	if j.isOpenArray(n) && j.levelPrinted >= j.level {
		comma = ","
		if isList {
			comma = "," + j.nl()
		}
	}
	nlc := ""
	if hasContent {
		nlc = j.nl()
	}
	if isList {
		j.printf("%s%s{%s", comma, j.indent(), nlc)
	} else {
		j.printf("%s{%s", comma, nlc)
	}
	j.level++
	if err := j.attributes(n, true); err != nil {
		return err
	}
	prev := j.parent
	j.parent = n
	for k := range children {
		if err := j.node(children, k); err != nil {
			return err
		}
	}
	j.parent = prev
	j.level--
	if hasContent {
		j.printf("%s%s}", j.nl(), j.indent())
	} else {
		j.printf("}")
	}
	j.levelDone()
	return nil
}

func (j *jsonPrinter) container(n *Node) error {
	j.member(n, false)
	return j.inner(n)
}

// isLastInst is json_print_array_is_last_inst.
func (j *jsonPrinter) isLastInst(n *Node, sibs []*Node, i int) bool {
	if !j.isOpenArray(n) {
		return false
	}
	return i+1 >= len(sibs) || sibs[i+1].schema != n.schema
}

// leafList is json_print_leaf_list: one instance of a list or leaf-list.
func (j *jsonPrinter) leafList(n *Node, sibs []*Node, i int) error {
	if !j.isOpenArray(n) {
		j.member(n, false)
		j.arrayOpen(n)
		if n.schema.Kind == schema.LeafList {
			j.printf("%s", j.indent())
		}
	} else if n.schema.Kind == schema.LeafList {
		j.printf(",%s%s", j.nl(), j.indent())
	}
	if n.schema.Kind == schema.List {
		if err := j.inner(n); err != nil {
			return err
		}
	} else {
		if err := j.value(n.value, n.schema.Module); err != nil {
			return err
		}
		if j.firstLL == nil && hasPrintableMeta(n) {
			j.firstLL, j.firstLLSibs, j.firstLLIdx = n, sibs, i
		}
	}
	if j.isLastInst(n, sibs, i) {
		j.arrayClose()
	}
	return nil
}

// metaLeafList is json_print_meta_attr_leaflist: the "@name" array of a leaf-list's metadata, one
// entry per instance (null without metadata), printed after the array.
func (j *jsonPrinter) metaLeafList() error {
	sibs, k := j.firstLLSibs, j.firstLLIdx
	for k > 0 && matching(sibs[k-1], sibs[k]) {
		k--
	}
	j.member(sibs[k], true)
	j.printf("[%s", j.nl())
	j.level++
	for ; k < len(sibs); k++ {
		it := sibs[k]
		j.comma()
		if it.schema != nil && hasPrintableMeta(it) {
			nl := "{"
			if j.format() {
				nl = "{\n"
			}
			j.printf("%s%s", j.indent(), nl)
			j.level++
			if err := j.metadata(it); err != nil {
				return err
			}
			j.level--
			j.printf("%s%s}", j.nl(), j.indent())
		} else {
			j.printf("%snull", j.indent())
		}
		j.levelDone()
		if k+1 >= len(sibs) || !matching(it, sibs[k+1]) {
			break
		}
	}
	j.level--
	j.printf("%s%s]", j.nl(), j.indent())
	j.levelDone()
	return nil
}

// opaq is json_print_opaq for an opaque node without hints and attributes: an object when it has
// children, else its text as a string.
func (j *jsonPrinter) opaq(n *Node) error {
	j.memberOpaq(j.parent, n, false)
	if len(kids(n)) > 0 {
		if err := j.inner(n); err != nil {
			return err
		}
		j.levelDone()
		return nil
	}
	j.str(n.opaq.Value)
	j.levelDone()
	return nil
}

// node is json_print_node.
func (j *jsonPrinter) node(sibs []*Node, i int) error {
	n := sibs[i]
	if !j.opts.shouldPrint(n) {
		if j.isLastInst(n, sibs, i) {
			j.arrayClose()
		}
		return nil
	}
	var err error
	switch {
	case n.schema == nil:
		err = j.opaq(n)
	case n.schema.Kind == schema.Container || n.schema.Kind == schema.RPC || n.schema.Kind == schema.Action ||
		n.schema.Kind == schema.Notification:
		err = j.container(n)
	case n.schema.Kind == schema.Leaf:
		err = j.leaf(n)
	case n.schema.Kind == schema.LeafList || n.schema.Kind == schema.List:
		err = j.leafList(n, sibs, i)
	default:
		err = fmt.Errorf("%w: anydata or anyxml node %q", ErrUnsupported, n.Name())
	}
	if err != nil {
		return err
	}
	j.levelDone()
	var next *Node
	if i+1 < len(sibs) {
		next = sibs[i+1]
	}
	if j.firstLL != nil && !matching(next, j.firstLL) {
		if err := j.metaLeafList(); err != nil {
			return err
		}
		j.firstLL = nil
	}
	return nil
}
