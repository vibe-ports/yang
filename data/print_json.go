// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/printer_json.c (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"
	"slices"

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
	root         *Node   // the top node being printed (pctx->root)
	firstLL      *Node   // first printed leaf-list instance with metadata to print after the array
	firstLLSibs  []*Node
	firstLLIdx   int
}

// printJSON is json_print_data from sibs[from]: with siblings (LYD_PRINT_SIBLINGS) the following
// siblings too, else only that subtree (lyd_print_tree).
func printJSON(p *printer, sibs []*Node, from int) error {
	if from >= len(sibs) {
		p.printf("{}%s", p.nl())
		return nil
	}
	j := &jsonPrinter{printer: p}
	p.level = 1
	p.printf("{%s", p.nl())
	for i := from; i < len(sibs); i++ {
		j.root = sibs[i]
		if err := j.node(sibs, i); err != nil {
			return err
		}
		if !p.siblings {
			break
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
	if n.opaq.Format == types.FormatXML {
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
	j.memberOf(n.schema, j.nodeModule(n), n.Name(), attr)
}

// memberOf is json_print_member of the schema node sn (nil: an opaque node of module mod) or of
// a schema node without an instance.
func (j *jsonPrinter) memberOf(sn *schema.Node, mod, name string, attr bool) {
	j.comma()
	at := ""
	if attr {
		at = "@"
	}
	sep := ""
	if j.format() {
		sep = " "
	}
	if j.nsDiffers(sn, j.parent) {
		j.printf("%s\"%s%s:%s\":%s", j.indent(), at, mod, name, sep)
	} else {
		j.printf("%s\"%s%s\":%s", j.indent(), at, name, sep)
	}
}

// member2 is json_print_member2 of an opaque name (an opaque node or an attribute: moduleNS is
// its XML namespace or JSON module name per format), the module name printed unless parent's is
// the same; an empty name is the empty "@" member.
func (j *jsonPrinter) member2(parent *Node, format types.Format, moduleNS, name string, attr bool) {
	j.comma()
	at := ""
	if attr {
		at = "@"
	}
	sep := ""
	if j.format() {
		sep = " "
	}
	mod := moduleNS
	if format == types.FormatXML {
		mod = ""
		if moduleNS != "" {
			if m := j.set.ByNamespace(moduleNS); m != nil {
				mod = m.Name
			}
		}
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

// memberOpaq is json_print_member2 of the opaque node n.
func (j *jsonPrinter) memberOpaq(parent, n *Node, attr bool) {
	j.member2(parent, n.opaq.Format, n.opaq.ModuleNS, n.opaq.Name, attr)
}

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

// metadata is json_print_metadata; wd is the with-defaults module when the default attribute is
// to be printed first.
func (j *jsonPrinter) metadata(n *Node, wd *schema.Module) error {
	sep := ""
	if j.format() {
		sep = " "
	}
	if wd != nil {
		j.printf("%s\"%s:default\":%strue", j.indent(), wd.Name, sep)
		j.levelDone()
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
	var wd *schema.Module
	if n.schema != nil && n.schema.Kind != schema.Container && j.opts.tagged(n) {
		wd = j.set.Implemented(wdModule) // printed only if the context has the module
	}
	var err error
	switch {
	case n.schema != nil && (wd != nil || hasPrintableMeta(n)):
		if inner {
			j.member2(n.parent, types.FormatJSON, "", "", true)
		} else {
			j.member(n, true)
		}
		j.printf("{%s", j.nl())
		j.level++
		err = j.metadata(n, wd)
	case n.schema == nil && len(n.opaq.Attrs) > 0:
		if inner {
			j.member2(n.parent, types.FormatJSON, "", "", true)
		} else {
			j.memberOpaq(n.parent, n, true)
		}
		j.printf("{%s", j.nl())
		j.level++
		j.attribute(n)
	default:
		return nil
	}
	j.level--
	j.printf("%s%s}", j.nl(), j.indent())
	j.levelDone()
	return err
}

// attribute is json_print_attribute: the attributes of the opaque node n, as members whose
// value is printed by its hints.
func (j *jsonPrinter) attribute(n *Node) {
	for _, a := range n.opaq.Attrs {
		j.member2(n, a.Format, a.ModuleNS, a.Name, false)
		switch h := a.Hints; {
		case h&(types.HintString|types.HintOctNum|types.HintHexNum|types.HintNum64) != 0:
			j.str(a.Value)
		case h&(types.HintBoolean|types.HintDecNum) != 0:
			if a.Value == "" {
				j.buf.WriteString("null")
			} else {
				j.buf.WriteString(a.Value)
			}
		case h&types.HintEmpty != 0:
			j.buf.WriteString("[null]")
		default:
			j.str(a.Value) // no hints: a string
		}
		j.levelDone()
	}
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
	printable := false // json_print_inner: a child that will be printed
	for _, c := range children {
		if j.opts.shouldPrint(c) {
			printable = true
			break
		}
	}
	var schemaKids []*schema.Node // lysc_node_child
	if n.schema != nil {
		schemaKids = n.schema.Children
	}
	hasContent := len(n.meta) > 0 || printable || (j.opts.EmptyLeafList && j.nextEmpty(schemaKids, children) != nil)
	isList := (n.schema != nil && n.schema.Kind == schema.List) ||
		(n.schema == nil && n.opaq.Hints != types.HintData && n.opaq.Hints&hintList != 0)
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
	if !printable && j.opts.EmptyLeafList {
		for rest := j.nextEmpty(schemaKids, nil); rest != nil; rest = j.nextEmpty(rest[1:], nil) {
			j.leafListEmpty(rest[0])
			j.levelDone()
		}
	}
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
	if n == j.root && !j.siblings {
		return true // the only printed instance
	}
	return i+1 >= len(sibs) || sibs[i+1].schema != n.schema
}

// leafListEmpty is json_print_leaf_list_empty: an empty array of the list or leaf-list sn.
func (j *jsonPrinter) leafListEmpty(sn *schema.Node) {
	j.memberOf(sn, sn.Module.Name, sn.Name, false)
	j.buf.WriteString("[") // json_print_array_open with LY_PRINT_SHRINK: no empty line
	j.open = append(j.open, nil)
	j.level++
	j.arrayClose()
}

// nextEmpty is json_print_next_empty_leaf_list: the rest of the schema siblings sl from the first
// list or leaf-list, unless an instance in sibs that will be printed comes first; nil if none.
func (j *jsonPrinter) nextEmpty(sl []*schema.Node, sibs []*Node) []*schema.Node {
	for k, sn := range sl {
		// lyd_find_sibling_schema: the first instance
		if i := slices.IndexFunc(sibs, func(n *Node) bool { return n.schema == sn }); i >= 0 && j.opts.shouldPrint(sibs[i]) {
			return nil
		}
		if sn.Kind == schema.LeafList || sn.Kind == schema.List {
			return sl[k:]
		}
	}
	return nil
}

// schemaAfter is the lysc_node.next chain of sn: its following schema siblings. The children of
// all the cases of a choice are linked as one list in libyang.
func schemaAfter(sn *schema.Node) []*schema.Node {
	var l []*schema.Node
	switch p := sn.Parent; {
	case p == nil:
		l = sn.Module.Top
	case p.Kind == schema.Case && p.Parent != nil:
		for _, c := range p.Parent.Children {
			l = append(l, c.Children...)
		}
	default:
		l = p.Children
	}
	if i := slices.Index(l, sn); i >= 0 {
		return l[i+1:]
	}
	return nil
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
		if j.firstLL == nil {
			var wd *schema.Module
			if j.opts.tagged(n) {
				wd = j.set.Implemented(wdModule)
			}
			if wd != nil || hasPrintableMeta(n) {
				j.firstLL, j.firstLLSibs, j.firstLLIdx = n, sibs, i
			}
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
	var wdMod *schema.Module
	if j.opts.WithDefaults == WDAllTagged || j.opts.WithDefaults == WDImplicitTagged {
		wdMod = j.set.Implemented(wdModule)
	}
	// libyang decides on the attributes of an opaque leaf-list by its first instance
	var opaq *opaque
	if first := sibs[k]; first.schema != nil {
		j.member(first, true)
	} else {
		opaq = first.opaq
		j.memberOpaq(first.parent, first, true)
	}
	j.printf("[%s", j.nl())
	j.level++
	for ; k < len(sibs); k++ {
		it := sibs[k]
		j.comma()
		var wd *schema.Module
		if it.schema != nil && j.opts.tagged(it) {
			wd = wdMod
		}
		if (it.schema != nil && (hasPrintableMeta(it) || wd != nil)) || (opaq != nil && len(opaq.Attrs) > 0) {
			nl := "{"
			if j.format() {
				nl = "{\n"
			}
			j.printf("%s%s", j.indent(), nl)
			j.level++
			if it.schema != nil {
				if err := j.metadata(it, wd); err != nil {
					return err
				}
			} else {
				j.attribute(it)
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

// opaq is json_print_opaq: by the node hints, an instance of an array (list or leaf-list), an
// object (children, a list or a container) or a value printed by its value hints, with its
// attributes (those of leaf-list instances after the array).
func (j *jsonPrinter) opaq(sibs []*Node, i int) error {
	n := sibs[i]
	o := n.opaq
	h := o.Hints
	if h == types.HintData {
		h = 0 // useless and confusing hints
	}
	inArray := h&(hintList|hintLeafList) != 0
	first := !inArray || i == 0 || !matching(sibs[i-1], n)
	last := !inArray || i+1 >= len(sibs) || !matching(n, sibs[i+1])
	switch {
	case first:
		j.memberOpaq(j.parent, n, false)
		if inArray {
			j.arrayOpen(n)
		}
		if h&hintLeafList != 0 {
			j.printf("%s", j.indent())
		}
	case h&hintLeafList != 0:
		j.printf(",%s%s", j.nl(), j.indent())
	}
	if len(kids(n)) > 0 || h&(hintList|hintContainer) != 0 {
		if err := j.inner(n); err != nil {
			return err
		}
		j.levelDone()
	} else {
		switch {
		case h&types.HintEmpty != 0:
			j.buf.WriteString("[null]")
		case h&(types.HintBoolean|types.HintDecNum) != 0 && h&types.HintNum64 == 0:
			j.buf.WriteString(o.Value)
		default:
			j.str(o.Value) // a string or a large number
		}
		j.levelDone()
		switch {
		case h&hintLeafList == 0:
			if err := j.attributes(n, false); err != nil {
				return err
			}
		case j.firstLL == nil && len(o.Attrs) > 0:
			j.firstLL, j.firstLLSibs, j.firstLLIdx = n, sibs, i // printed after the array
		}
	}
	if last && inArray {
		j.arrayClose()
		j.levelDone()
	}
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
		err = j.opaq(sibs, i)
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
	var next *Node
	if i+1 < len(sibs) {
		next = sibs[i+1]
	}
	if n.schema != nil && j.opts.EmptyLeafList && (next == nil || next.schema != n.schema) {
		// after the last instance: the empty (leaf-)lists that follow it in the schema
		for rest := j.nextEmpty(schemaAfter(n.schema), sibs); rest != nil; rest = j.nextEmpty(rest[1:], sibs) {
			j.leafListEmpty(rest[0])
		}
	}
	j.levelDone()
	if j.firstLL != nil && !matching(next, j.firstLL) {
		if err := j.metaLeafList(); err != nil {
			return err
		}
		j.firstLL = nil
	}
	return nil
}
