// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_json.c (BSD-3-Clause, © CESNET).

package data

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyjson"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// jsonParser is struct lyd_json_ctx: the parser context, the lexer and the LYD_PARSE_STRICT and
// LYD_PARSE_OPAQ options, which the attribute parsing switches for a while.
type jsonParser struct {
	lc           *lydCtx
	lx           *lyjson.Lexer
	strict, opaq bool
	// opqFirst is the first opaque child of a parent (nil: the top level) with a name and module
	// name, for parseAttribute. JSON opaque nodes are only ever appended, so the first one stays.
	opqFirst map[opqKey]*Node
}

type opqKey struct {
	parent    *Node
	name, mod string
}

// insertOpaq links the new opaque node n under parent (lyd_parser_node_insert) and indexes it.
func (p *jsonParser) insertOpaq(parent, n *Node) {
	p.lc.nodeInsert(parent, nil, n)
	k := opqKey{parent, n.opaq.Name, n.opaq.ModuleNS}
	if _, ok := p.opqFirst[k]; !ok {
		p.opqFirst[k] = n
	}
}

// parseJSON is lyd_parse_json for datastore data (LYD_INTOPT_WITH_SIBLINGS, no parent, no bare
// value): every top-level member, then the metadata that still wait for their node. Operations and
// RESTCONF envelopes are design 07 M4; anydata/anyxml instances fail with yang.ErrUnsupported
// (deviations.md U-0043).
func parseJSON(lc *lydCtx, in []byte) error {
	lx, err := lyjson.New(in)
	if err != nil {
		_ = lc.lexErr(err)
		return errLoggedFatal // no parser context: lyd_parse stops
	}
	lc.log.pushInput(func() int { return int(lx.Line()) }) //nolint:gosec // line numbers fit
	defer lc.log.popInput()
	p := &jsonParser{lc: lc, lx: lx, strict: lc.opts.Unknown == Reject, opaq: lc.opts.Unknown == Opaque,
		opqFirst: map[opqKey]*Node{}}
	if st := lx.Status(); st != lyjson.TokenObject {
		_ = lc.log.val(nil, "", ly.SyntaxJSON, "Expected top-level JSON object or correct bare value, but %s found.", st)
		return errLoggedFatal
	}
	var rc error
	for {
		if r := p.subtree(nil); r != nil {
			if rc = r; lc.fatal(r) {
				return rc
			}
		}
		if lx.Status() != lyjson.TokenObjectNext {
			break
		}
	}
	if r := p.metadataFinish(nil); r != nil {
		rc = r
	}
	return rc
}

// next is lyjson_ctx_next with its error logged (lexErr).
func (p *jsonParser) next(status *lyjson.Token) error {
	t, err := p.lx.Next()
	if err != nil {
		return p.lc.lexErr(err)
	}
	if status != nil {
		*status = t
	}
	return nil
}

// parseName is lydjson_parse_name: [@][prefix:]name. hasPrefix tells "p:" from no colon at all.
func parseName(v string) (name, prefix string, hasPrefix, isMeta bool) {
	if i := strings.IndexByte(v, ':'); i >= 0 {
		prefix, name, hasPrefix = v[:i], v[i+1:], true
		if strings.HasPrefix(prefix, "@") {
			prefix, isMeta = prefix[1:], true
		}
		return name, prefix, hasPrefix, isMeta
	}
	if strings.HasPrefix(v, "@") {
		return v[1:], "", false, true
	}
	return v, "", false, false
}

// nodePrefix is lydjson_get_node_prefix: local, else the module name of n or its nearest ancestor
// that has one ("" when none has).
func nodePrefix(n *Node, local string) string {
	if local != "" {
		return local
	}
	for ; n != nil; n = n.parent {
		switch {
		case n.schema != nil:
			return n.schema.Module.Name
		case n.opaq.ModuleNS != "":
			return n.opaq.ModuleNS
		case n.opaq.Prefix != "":
			return n.opaq.Prefix
		}
	}
	return ""
}

// dataSkip is lydjson_data_skip: past the current item, onto its last token.
func (p *jsonParser) dataSkip() error {
	depth := p.lx.Depth()
	var cur lyjson.Token
	switch p.lx.Status() {
	case lyjson.TokenObject, lyjson.TokenArray:
		closed := lyjson.TokenObjectClosed
		if p.lx.Status() == lyjson.TokenArray {
			closed = lyjson.TokenArrayClosed
		}
		depth++
		for {
			if err := p.next(&cur); err != nil {
				return err
			}
			if cur == closed && depth == p.lx.Depth() {
				return nil
			}
		}
	case lyjson.TokenObjectName:
		if err := p.next(&cur); err != nil {
			return err
		}
		if cur == lyjson.TokenObject || cur == lyjson.TokenArray {
			return p.dataSkip()
		}
		return nil
	}
	return p.next(nil)
}

// getSnode is lydjson_get_snode: the schema node of member [prefix:]name under parent, nil when
// there is none and that is not an error (skip or opaque).
func (p *jsonParser) getSnode(isAttr bool, prefix, name string, parent *Node) (*schema.Node, error) {
	lc := p.lc
	var sparent *schema.Node
	if parent != nil && parent.schema != nil && !isAny(parent.schema) {
		sparent = parent.schema
	}
	var mod *schema.Module
	if prefix != "" {
		mod = lc.tree.set.Implemented(prefix)
	} else if sparent != nil {
		mod = sparent.Module
	}
	if mod != nil {
		if sn := schema.FindChild(sparent, mod.Top, mod, name, 0); sn != nil {
			return sn, lc.checkSchema(sn)
		}
		if err := extData(sparent, mod, name); err != nil {
			return nil, err
		}
	}
	switch {
	case prefix != "":
	case parent != nil:
		mod = nil
		if parent.schema != nil {
			mod = parent.schema.Module
		}
	default:
		if isAttr {
			name = "@" + name
		}
		return nil, lc.log.val(nil, "", ly.SyntaxJSON, "Top-level JSON object member \"%s\" must be namespace-qualified.", name)
	}
	if !p.strict {
		return nil, nil
	}
	switch {
	case mod == nil:
		return nil, lc.log.val(parent, "", ly.Reference, "No module named \"%s\" in the context.", prefix)
	case sparent != nil:
		return nil, lc.log.val(parent, "", ly.Reference, "Node \"%s\" not found as a child of \"%s\" node.", name, sparent.Name)
	}
	return nil, lc.log.val(parent, "", ly.Reference, "Node \"%s\" not found in the \"%s\" module.", name, mod.Name)
}

// valueTypeHint is lydjson_value_type_hint: the value hints of the current token; [null] is read
// up to its end. A wrong token is LY_EINVAL after its LOGVAL at sn.
func (p *jsonParser) valueTypeHint(sn *schema.Node, status *lyjson.Token) (types.Hints, error) {
	lc := p.lc
	fail := func(format string, a ...any) error {
		if sn != nil {
			lc.log.locSet(sn)
			defer lc.log.locBack(1)
		}
		_ = lc.log.val(nil, "", ly.SyntaxJSON, format, a...)
		return fatalRC("LY_EINVAL")
	}
	var h types.Hints
	switch *status {
	case lyjson.TokenArray: // only [null]
		if err := p.next(status); err != nil {
			return 0, err
		}
		if *status != lyjson.TokenNull {
			return 0, fail("Expected JSON name/value or special name/[null], but input data contains name/[%s].", *status)
		}
		if err := p.next(nil); err != nil {
			return 0, err
		}
		if p.lx.Status() != lyjson.TokenArrayClosed {
			// libyang names the stale status, null
			return 0, fail("Expected array end, but input data contains %s.", *status)
		}
		h = types.HintEmpty
	case lyjson.TokenString:
		h = types.HintString | types.HintNum64
	case lyjson.TokenNumber:
		h = types.HintDecNum
	case lyjson.TokenFalse, lyjson.TokenTrue:
		h = types.HintBoolean
	case lyjson.TokenNull:
	default:
		return 0, fail("Unexpected input data %s.", *status)
	}
	if lc.opts.jsonStringDatatypes {
		h |= types.HintStringDatatypes
	}
	return h, nil
}

// valueValid is ly_value_validate without a context: whether the current value stores as sn's
// type, nothing logged.
func (p *jsonParser) valueValid(sn *schema.Node, h types.Hints) bool {
	_, d := types.Store(sn.Type, p.lx.Value(), types.FormatJSON, h, types.ModuleNames{Set: p.lc.tree.set}, sn)
	return d == nil
}

// checkList is lydjson_check_list: whether the object holds every key of list with a valid value
// (errNot or any error: no). It moves the lexer; the caller restores it.
func (p *jsonParser) checkList(list *schema.Node) error {
	keys := append([]*schema.Node(nil), list.Keys...)
	if len(keys) == 0 {
		return nil
	}
	status := p.lx.Status()
	if status == lyjson.TokenObject {
		for {
			if err := p.next(&status); err != nil {
				return err
			}
			if status != lyjson.TokenObjectName {
				break
			}
			name, _, hasPrefix, isAttr := parseName(p.lx.Value())
			if !isAttr && !hasPrefix {
				i := 0
				for i < len(keys) && keys[i].Name != name {
					i++
				}
				if err := p.next(&status); err != nil {
					return err
				}
				if i < len(keys) {
					if status < lyjson.TokenNumber || status > lyjson.TokenNull {
						return errNot // not a terminal
					}
					h, err := p.valueTypeHint(keys[i], &status)
					if err != nil {
						return err
					}
					if !p.valueValid(keys[i], h) {
						return errNot
					}
					keys[i] = keys[len(keys)-1] // ly_set_rm_index
					keys = keys[:len(keys)-1]
				}
				if err := p.next(&status); err != nil {
					return err
				}
			} else {
				if err := p.dataSkip(); err != nil {
					return err
				}
				if err := p.next(&status); err != nil {
					return err
				}
			}
			if len(keys) == 0 || status != lyjson.TokenObjectNext {
				break
			}
		}
	}
	if len(keys) > 0 {
		return errNot
	}
	return nil
}

// checkOpaq is lydjson_data_check_opaq: with LYD_PARSE_OPAQ, errNot when the input cannot be an
// instance of sn (parse it as opaque); otherwise the value hints of a term. The lexer is restored
// (lyjson_ctx_backup/restore, single use).
func (p *jsonParser) checkOpaq(sn *schema.Node) (h types.Hints, err error) {
	lc := p.lc
	p.lx.Backup()
	defer p.lx.Restore()
	status := p.lx.Status()
	switch {
	case p.opaq:
		switch sn.Kind {
		case schema.Leaf, schema.LeafList:
			// ly_temp_log_options(0): nothing logged here is kept
			nd, hit := len(lc.log.diags), lc.limitHit
			if h, err = p.valueTypeHint(sn, &status); err != nil || !p.valueValid(sn, h) {
				err = errNot
			}
			lc.log.diags, lc.limitHit = lc.log.diags[:nd], hit
		case schema.List:
			if p.checkList(sn) != nil {
				err = errNot
			}
		case schema.Container, schema.RPC, schema.Action, schema.Notification:
			if status != lyjson.TokenObject {
				err = errNot
			}
		}
	case sn.Kind == schema.Leaf || sn.Kind == schema.LeafList:
		h, err = p.valueTypeHint(sn, &status)
	}
	return h, err
}

// metadataFinish is lydjson_metadata_finish: the opaque "@name" nodes among the children of
// parent (the top level when nil) become metadata of the instance they precede in the input; the
// n-th "@name" in a run of them goes to the n-th instance in sibling order (system-ordered
// instances are already sorted, design 07 §4). libyang scans all siblings per "@name"; the
// instances are looked up instead (the run of a schema node by binary search, opaque nodes by
// name), so the work is linear.
func (p *jsonParser) metadataFinish(parent *Node) error {
	lc := p.lc
	sib := lc.tree.childrenOf(parent)
	var ats []*Node
	byName := map[string][]*Node{} // opaque siblings by name, in order
	for _, n := range sib.opq {
		lc.tree.work.Add(1)
		byName[n.opaq.Name] = append(byName[n.opaq.Name], n)
		if strings.HasPrefix(n.opaq.Name, "@") {
			ats = append(ats, n)
		}
	}
	if len(ats) == 0 {
		return nil
	}
	type run struct{ at, n int } // the instances of a schema node: sib.list[at:at+n]
	runs := map[*schema.Node]run{}
	firstList := map[string]int{} // index of the first list-hinted opaque node of a name, -1: none
	var done []*Node
	// libyang frees each one when it is attached; none of them is a target of another (their
	// names start with '@'), except for a "@@name" member
	defer func() { _ = lc.tree.unlinkAll(done) }()
	var rc error
	prev, instance := "", 0
	for _, at := range ats {
		lc.tree.work.Add(1)
		if prev != at.opaq.Name {
			prev, instance = at.opaq.Name, 1
		} else {
			instance++
		}
		// libyang resolves the name again at every schema sibling; it succeeded the first time or
		// found nothing, except for the top-level "@", whose error it logs once per schema sibling
		// (D-0060: once here)
		var sn *schema.Node
		if len(sib.list) > 0 {
			name, prefix, _, isAttr := parseName(at.opaq.Name)
			sn, _ = p.getSnode(isAttr, prefix, name, parent)
		}
		r, ok := runs[sn]
		if !ok && sn != nil {
			if r.at = lc.tree.schemaIndex(sib, sn); r.at >= 0 {
				for r.n = 0; r.at+r.n < len(sib.list) && sib.list[r.at+r.n].schema == sn; r.n++ {
					lc.tree.work.Add(1)
				}
			}
			runs[sn] = r
		}
		var n *Node
		if instance <= r.n {
			n = sib.list[r.at+instance-1]
		} else {
			// then the opaque siblings of the name; a list-hinted one before the instance fails
			m, same := instance-r.n, byName[at.opaq.Name[1:]]
			fl, ok := firstList[at.opaq.Name[1:]]
			if !ok {
				fl = slices.IndexFunc(same, func(o *Node) bool { return o.opaq.Hints&hintList != 0 })
				firstList[at.opaq.Name[1:]] = fl
			}
			if fl >= 0 && fl < m {
				return lc.log.val(at, "", ly.Syntax, "Metadata container references a sibling list node %s.", same[fl].opaq.Name)
			}
			if m <= len(same) {
				n = same[m-1]
			}
		}
		switch {
		case n == nil:
			if instance > 1 {
				rc = lc.log.val(at, "", ly.Reference, "Missing JSON data instance #%d to be coupled with %s metadata.",
					instance, at.opaq.Name)
			} else {
				rc = lc.log.val(at, "", ly.Reference, "Missing JSON data instance to be coupled with %s metadata.", at.opaq.Name)
			}
			continue
		case n.schema == nil:
			for m := range at.kids.all() {
				a := attr{Name: m.Name(), Value: valueText(m), Format: types.FormatJSON}
				if m.schema != nil {
					a.ModuleNS = m.schema.Module.Name
				} else {
					a.Prefix, a.ModuleNS, a.Hints = m.opaq.Prefix, m.opaq.ModuleNS, m.opaq.Hints
				}
				createAttr(n, a)
			}
		default:
			if err := p.attachMeta(at, n); err != nil {
				return err
			}
			lc.setDataFlags(n, &n.meta)
		}
		done = append(done, at)
	}
	return rc
}

// attachMeta turns the members of the opaque metadata container at into metadata of n.
func (p *jsonParser) attachMeta(at, n *Node) error {
	lc := p.lc
	for m := range at.kids.all() {
		var mod *schema.Module
		var mprefix string
		var h types.Hints
		switch {
		case m.schema != nil:
			mod = m.schema.Module
		case m.opaq.Prefix != "":
			mprefix, h = m.opaq.Prefix, m.opaq.Hints
			mod = lc.tree.set.Implemented(mprefix)
		default:
			// libyang dereferences the missing schema node here and crashes (D-0059): treated as
			// metadata without a module
			h = m.opaq.Hints
		}
		switch {
		case mod != nil:
			err := lc.createMeta(n, &n.meta, mod, m.Name(), valueText(m), types.FormatJSON,
				types.ModuleNames{Set: lc.tree.set}, h, n.schema, n)
			if err != nil {
				return err
			}
		case p.strict && mprefix != "":
			return lc.log.val(at, "", ly.Reference, "Unknown (or not implemented) YANG module \"%s\" of metadata \"%s:%s\".",
				mprefix, mprefix, m.Name())
		case p.strict:
			return lc.log.val(at, "", ly.Reference, "Missing YANG module of metadata \"%s\".", m.Name())
		}
	}
	return nil
}

// valueText is lyd_get_value: the canonical value of a term, the value of an opaque node, "" else.
func valueText(n *Node) string {
	switch {
	case n.schema == nil:
		return n.opaq.Value
	case n.isTerm():
		return n.value.Canonical()
	}
	return ""
}

// sibAt is the i-th sibling in libyang's order (schema nodes, then opaque nodes), nil past the end.
func sibAt(sib *siblings, i int) *Node {
	switch {
	case i < len(sib.list):
		return sib.list[i]
	case i-len(sib.list) < len(sib.opq):
		return sib.opq[i-len(sib.list)]
	}
	return nil
}

// metaAttr is lydjson_meta_attr: the metadata object of node (an array of them for a leaf-list,
// one per instance from node on), as metadata of a schema node or attributes of an opaque one.
// idx is the position of a leaf-list node among its siblings (sibAt): libyang's node->next.
func (p *jsonParser) metaAttr(node *Node, idx int) (rc error) {
	lc := p.lc
	nodetype := schema.Container
	if node.schema != nil {
		nodetype = node.schema.Kind
	}
	prev, instance := node, 0
	var status lyjson.Token
	var expected string
	inParent := false
	defer func() {
		if lc.isEValid(rc) && lc.opts.Validate.MultiError {
			if r := p.dataSkip(); r != nil { // try to skip the invalid data
				rc = r
			}
		}
	}()
	reprErr := func() error {
		name := prev.Name()
		if node != nil {
			name = node.Name()
		}
		in := "name"
		if inParent {
			in = ""
		}
		return lc.log.val(prev, "", ly.SyntaxJSON,
			"The attribute(s) of %s \"%s\" is expected to be represented as JSON %s, but input data contains @%s/%s.",
			nodetypeStr(nodetype), name, expected, status, in)
	}
	if err := p.next(&status); err != nil { // the second item of the name/X pair
		return err
	}
	switch nodetype {
	case schema.LeafList:
		expected = "@name/array of objects/nulls"
		if status != lyjson.TokenArray {
			return reprErr()
		}
	case schema.Leaf, schema.AnyXML:
		expected = "@name/object"
		if status != lyjson.TokenObject {
			return reprErr()
		}
	case schema.Container, schema.List, schema.AnyData, schema.Notification, schema.Action, schema.RPC:
		inParent, expected = true, "@/object"
		if status != lyjson.TokenObject {
			return reprErr()
		}
	default:
		return lc.log.logErr("LY_EINT", "Internal error (%s:%d).", "parser_json.c", 814)
	}
	for {
		if nodetype == schema.LeafList { // next_entry
			if status == lyjson.TokenArrayClosed {
				return nil // no more metadata
			}
			if err := p.next(&status); err != nil { // into the array / next item
				return err
			}
			instance++
			if status != lyjson.TokenObject && status != lyjson.TokenNull {
				return reprErr()
			}
			if node == nil || node.schema != prev.schema {
				lc.log.locSet(prev.schema)
				defer lc.log.locBack(1)
				return lc.log.val(nil, "", ly.Reference, "Missing JSON data instance #%d of %s:%s to be coupled with metadata.",
					instance, prev.schema.Module.Name, prev.schema.Name)
			}
			if status == lyjson.TokenNull {
				idx++
				prev, node = node, sibAt(node.siblingsOf(), idx)
				if err := p.next(&status); err != nil {
					return err
				}
				continue
			}
		}
		// every member of one metadata object
		for {
			if err := p.next(&status); err != nil {
				return err
			}
			if status != lyjson.TokenObjectName {
				return reprErr()
			}
			raw := p.lx.Value()
			name, prefix, _, isAttr := parseName(raw)
			switch {
			case name == "":
				// libyang prints "%.10s" of the empty name, which reads on past the member name
				return lc.log.val(prev, "", ly.SyntaxJSON, "Metadata in JSON found with an empty name, followed by: %s",
					p.lx.AfterString(10))
			case prefix == "":
				return lc.log.val(prev, "", ly.SyntaxJSON, "Metadata in JSON must be namespace-qualified, missing prefix for \"%s\".", raw)
			case isAttr:
				return lc.log.val(prev, "", ly.SyntaxJSON, "Invalid format of the Metadata identifier in JSON, unexpected '@' in \"%s\"", raw)
			}
			mod := lc.tree.set.Implemented(prefix)
			if mod == nil {
				if p.strict {
					return lc.log.val(prev, "", ly.Reference, "Prefix \"%s\" of the metadata \"%s\" does not match any module in the context.",
						prefix, name)
				}
				if node.schema != nil {
					// skip the member; libyang then leaves the object loop on the skipped value, so
					// this always ends in the representation error below
					if err := p.dataSkip(); err != nil {
						return err
					}
					status = p.lx.Status()
					if status != lyjson.TokenObjectNext {
						break
					}
					continue
				}
			}
			if err := p.next(&status); err != nil { // the value
				return err
			}
			h, err := p.valueTypeHint(node.schema, &status)
			if err != nil {
				return err
			}
			if node.schema != nil {
				err := lc.createMeta(node, &node.meta, mod, name, p.lx.Value(), types.FormatJSON,
					types.ModuleNames{Set: lc.tree.set}, h, node.schema, node)
				if err != nil {
					return err
				}
				lc.setDataFlags(node, &node.meta)
			} else {
				createAttr(node, attr{Name: name, Prefix: prefix, ModuleNS: nodePrefix(node, prefix), Value: p.lx.Value(),
					Format: types.FormatJSON, Hints: h})
			}
			if err := p.next(&status); err != nil { // the next member
				return err
			}
			if status != lyjson.TokenObjectNext {
				break
			}
		}
		if status != lyjson.TokenObjectClosed {
			return reprErr()
		}
		if nodetype != schema.LeafList {
			return nil
		}
		// the metadata object of the next leaf-list instance
		idx++
		prev, node = node, sibAt(node.siblingsOf(), idx)
		if err := p.next(&status); err != nil {
			return err
		}
	}
}

// createOpaqJSON is lydjson_create_opaq: an opaque node named [prefix:]name with the current
// value (none for an object) and its module name inherited from parent unless prefixed.
func (p *jsonParser) createOpaqJSON(name, prefix string, parent *Node, statusInner *lyjson.Token) (*Node, error) {
	var value string
	var h types.Hints
	if *statusInner != lyjson.TokenObject {
		value = p.lx.Value()
		var err error
		if h, err = p.valueTypeHint(nil, statusInner); err != nil {
			return nil, err
		}
	}
	return p.lc.createOpaq(opaque{Name: name, Prefix: prefix, ModuleNS: nodePrefix(parent, prefix), Value: value,
		Format: types.FormatJSON, Prefixes: types.ModuleNames{Set: p.lc.tree.set}, Hints: h})
}

// parseOpaq is lydjson_parse_opaq: an opaque node from the current value, every instance of an
// array of them (the last one returned), with its children and their metadata. statusP and
// statusInner may be the same variable.
func (p *jsonParser) parseOpaq(name, prefix string, parent *Node, statusP, statusInner *lyjson.Token) (*Node, error) {
	lc := p.lc
	node, err := p.createOpaqJSON(name, prefix, parent, statusInner)
	if err != nil {
		return nil, err
	}
	p.insertOpaq(parent, node)
	children := func(status *lyjson.Token) error {
		for {
			if err := p.subtree(node); err != nil {
				return err
			}
			if *status = p.lx.Status(); *status != lyjson.TokenObjectNext {
				return nil
			}
		}
	}
	switch {
	case *statusP == lyjson.TokenArray && *statusInner == lyjson.TokenNull:
		node.opaq.Hints |= types.HintEmpty // special array null value, the only item
		if err := p.next(statusInner); err != nil {
			return node, err
		}
		if *statusInner != lyjson.TokenArrayClosed {
			return node, lc.log.val(node, "", ly.Syntax, "Array \"null\" member with another member.")
		}
	case *statusP == lyjson.TokenArray:
		for {
			if *statusInner == lyjson.TokenObject {
				node.opaq.Hints |= hintList
				if err := children(statusInner); err != nil {
					return node, err
				}
			} else {
				node.opaq.Hints |= hintLeafList
			}
			if err := p.next(statusInner); err != nil {
				return node, err
			}
			if *statusInner == lyjson.TokenArrayClosed {
				break
			}
			// ARRAY_NEXT: the next instance
			if err := p.next(statusInner); err != nil {
				return node, err
			}
			if node, err = p.createOpaqJSON(name, prefix, parent, statusInner); err != nil {
				return nil, err
			}
			p.insertOpaq(parent, node)
		}
	case *statusP == lyjson.TokenObject:
		node.opaq.Hints |= hintContainer
		if err := children(statusP); err != nil {
			return node, err
		}
	}
	return node, p.metadataFinish(node)
}

// ctxNextParseOpaq is lydjson_ctx_next_parse_opaq: onto the value of the name/value pair, then
// parseOpaq.
func (p *jsonParser) ctxNextParseOpaq(name, prefix string, parent *Node, status *lyjson.Token) (*Node, error) {
	if err := p.next(status); err != nil {
		return nil, err
	}
	inner := *status
	if *status == lyjson.TokenArray {
		if err := p.next(&inner); err != nil { // into the array
			return nil, err
		}
	}
	return p.parseOpaq(name, prefix, parent, status, &inner)
}

// parseAttribute is lydjson_parse_attribute: the "@name" member of attrNode, else of the sibling
// it names; a member whose node is not parsed yet (or unknown) is kept as an opaque "@name" node
// for metadataFinish. libyang scans the siblings for the first match; schema nodes come first, so
// the first instance of the schema node (binary search), else the first opaque node of the name
// and module (opqFirst) is that match.
func (p *jsonParser) parseAttribute(attrNode *Node, sn *schema.Node, name, prefix string, hasPrefix bool,
	parent *Node, status *lyjson.Token) (*Node, error) {
	lc := p.lc
	idx := -1
	if attrNode == nil {
		lc.tree.work.Add(1)
		sib := lc.tree.childrenOf(parent)
		mod := ""
		if sn == nil {
			// any node of that name and module (libyang compares them only when both are set)
			if mod = prefix; !hasPrefix {
				mod = nodePrefix(parent, "")
			}
			if m := lc.tree.set.Implemented(mod); m != nil && mod != "" {
				var sparent *schema.Node
				if parent != nil {
					sparent = parent.schema
				}
				sn = schema.FindChild(sparent, m.Top, m, name, 0)
			}
		} else {
			mod, name = sn.Module.Name, sn.Name
		}
		if sn != nil {
			if idx = lc.tree.schemaIndex(sib, sn); idx >= 0 {
				attrNode = sib.list[idx]
			}
		}
		if attrNode == nil && mod != "" {
			attrNode = p.opqFirst[opqKey{parent, name, mod}]
		}
	}
	if attrNode != nil {
		return nil, p.metaAttr(attrNode, idx)
	}
	// parse it as an opaque node named "@[prefix:]name", resolved later
	strict, opaq := p.strict, p.opaq
	p.strict, p.opaq = false, true
	defer func() { p.strict, p.opaq = strict, opaq }()
	opaqName := "@" + name
	if hasPrefix {
		opaqName = "@" + prefix + ":" + name
	}
	return p.ctxNextParseOpaq(opaqName, "", parent, status)
}

// parseInstanceInner is lydjson_parse_instance_inner: a container or list instance, its children,
// their metadata, the key check and the new-node validation (closeInner).
func (p *jsonParser) parseInstanceInner(sn *schema.Node, parent *Node, status *lyjson.Token) (*Node, error) {
	lc := p.lc
	if *status != lyjson.TokenObject {
		return nil, errNot
	}
	node, err := lc.createInner(sn)
	if err != nil {
		return nil, err
	}
	lc.nodeInsert(parent, nil, node)
	// libyang frees the node after an error that stops the parse; the tree is dropped then anyway
	var rc error
	for {
		if r := p.subtree(node); r != nil {
			if rc = r; lc.fatal(r) {
				return node, rc
			}
		}
		lc.nodeInsert(parent, nil, node) // a list that had its keys missing
		if *status = p.lx.Status(); *status != lyjson.TokenObjectNext {
			break
		}
	}
	if r := p.metadataFinish(node); r != nil {
		return node, r
	}
	return node, lc.closeInner(node, rc)
}

// parseInstanceTerm is lydjson_parse_instance_term: a leaf or leaf-list instance from the current
// value ([null] is read up to its end even when the value was rejected).
func (p *jsonParser) parseInstanceTerm(sn *schema.Node, h types.Hints, parent *Node, status *lyjson.Token) (*Node, error) {
	lc := p.lc
	switch *status {
	case lyjson.TokenArray, lyjson.TokenNumber, lyjson.TokenString, lyjson.TokenFalse, lyjson.TokenTrue, lyjson.TokenNull:
	default:
		return nil, errNot
	}
	node, rc := lc.createTerm(sn, parent, p.lx.Value(), types.FormatJSON, types.ModuleNames{Set: lc.tree.set}, h)
	if rc != nil && lc.fatal(rc) {
		return nil, rc
	}
	lc.nodeInsert(parent, nil, node)
	if *status == lyjson.TokenArray { // [null]: two more moves
		for range 2 {
			if err := p.next(status); err != nil {
				return node, err
			}
		}
	}
	return node, rc
}

// parseInstance is lydjson_parse_instance: one instance of sn, or an opaque node when the input
// does not fit it and LYD_PARSE_OPAQ is set.
func (p *jsonParser) parseInstance(parent *Node, sn *schema.Node, name, prefix string, status *lyjson.Token) (*Node, error) {
	lc := p.lc
	h, err := p.checkOpaq(sn)
	switch {
	case err == nil:
		var node *Node
		var rc error
		switch {
		case lc.opts.jsonNull && *status == lyjson.TokenNull:
			return nil, nil // LYD_PARSE_JSON_NULL: nothing for a JSON null
		case sn.Kind == schema.Leaf || sn.Kind == schema.LeafList:
			node, rc = p.parseInstanceTerm(sn, h, parent, status)
		case isAny(sn):
			return nil, fmt.Errorf("%w: %s %q instance (anydata/anyxml data, deviations.md U-0043)",
				yang.ErrUnsupported, nodetypeStr(sn.Kind), sn.Name)
		default:
			node, rc = p.parseInstanceInner(sn, parent, status)
		}
		if rc != nil && lc.fatal(rc) || node == nil {
			return node, rc
		}
		lc.setDataFlags(node, &node.meta)
		return node, rc
	case errors.Is(err, errNot):
		node, err := p.parseOpaq(name, prefix, parent, status, status)
		if err != nil {
			return node, err
		}
		switch sn.Kind {
		case schema.List:
			node.opaq.Hints |= hintList
		case schema.LeafList:
			node.opaq.Hints |= hintLeafList
		}
		return node, nil
	}
	return nil, err
}

// subtree is lydjson_subtree_r: one member of the object the lexer is in (all instances of a
// list or leaf-list), as data nodes under parent (the top level when nil).
func (p *jsonParser) subtree(parent *Node) error {
	lc := p.lc
	status := p.lx.Status()
	if err := p.next(&status); err != nil {
		return err
	}
	if status == lyjson.TokenObjectClosed {
		return nil // empty object
	}
	name, prefix, hasPrefix, isMeta := parseName(p.lx.Value())
	var sn *schema.Node
	var rc error
	if !isMeta || name != "" || prefix != "" {
		var err error
		sn, err = p.getSnode(isMeta, prefix, name, parent)
		switch {
		case err != nil && lc.isEValid(err) && lc.opts.Validate.MultiError:
			rc = err
			if r := p.dataSkip(); r != nil { // skip the invalid data
				rc = r
			}
			if r := p.next(&status); r != nil {
				rc = r
			}
			return rc
		case err != nil:
			return err
		}
	}
	var expected string
	switch {
	case isMeta:
		var attrNode *Node
		switch {
		case name == "" && prefix == "" && parent == nil:
			r := lc.log.val(nil, "", ly.SyntaxJSON,
				"Invalid metadata format - \"@\" can be used only inside anydata, container or list entries.")
			if rc = r; lc.fatal(r) {
				return rc
			}
		case name == "" && prefix == "":
			attrNode, sn = parent, parent.schema // the parent's own metadata
		}
		if _, err := p.parseAttribute(attrNode, sn, name, prefix, hasPrefix, parent, &status); err != nil {
			return err
		}
	case sn == nil && !p.opaq:
		if err := p.dataSkip(); err != nil {
			return err
		}
	case sn == nil:
		if name == "" {
			r := lc.log.val(parent, "", ly.SyntaxJSON, "JSON object member name cannot be a zero-length string.")
			if rc = r; lc.fatal(r) {
				return rc
			}
		}
		if _, err := p.ctxNextParseOpaq(name, prefix, parent, &status); err != nil {
			return err
		}
	default:
		if err := p.next(&status); err != nil { // the value
			return err
		}
		switch sn.Kind {
		case schema.LeafList:
			expected = "name/array of values"
		case schema.List:
			expected = "name/array of objects"
		case schema.Leaf:
			expected = "name/value"
			if status == lyjson.TokenArray {
				expected = "name/[null]"
			}
		case schema.Container, schema.Notification, schema.Action, schema.RPC, schema.AnyData:
			expected = "name/object"
		case schema.AnyXML:
			expected = "name/value"
			if status == lyjson.TokenArray {
				expected = "name/array"
			}
		}
		instance := func() (repr bool, stop error) {
			_, r := p.parseInstance(parent, sn, name, prefix, &status)
			switch {
			case errors.Is(r, errNot):
				return true, nil
			case r != nil:
				if rc = r; lc.fatal(r) {
					return false, rc
				}
			}
			return false, nil
		}
		repr := false
		switch {
		case (sn.Kind == schema.LeafList || sn.Kind == schema.List) && status == lyjson.TokenArray:
			for {
				if err := p.next(&status); err != nil { // into the array / the next value
					return err
				}
				if status == lyjson.TokenArrayClosed {
					break // empty array
				}
				var err error
				if repr, err = instance(); err != nil {
					return err
				}
				if repr {
					break
				}
				if err := p.next(&status); err != nil { // after the item
					return err
				}
				if status != lyjson.TokenArrayNext {
					break
				}
			}
		case (sn.Kind == schema.LeafList || sn.Kind == schema.List) && !p.opaq:
			repr = true
		default:
			var err error
			if repr, err = instance(); err != nil {
				return err
			}
		}
		if repr {
			rc = lc.log.val(parent, "", ly.SyntaxJSON, "Expecting JSON %s but %s \"%s\" is represented in input data as name/%s.",
				expected, nodetypeStr(sn.Kind), sn.Name, status)
			if lc.opts.Validate.MultiError {
				if r := p.dataSkip(); r != nil { // try to skip the invalid data
					rc = r
				}
			}
			return rc
		}
	}
	if err := p.next(&status); err != nil { // after the item(s)
		return err
	}
	return rc
}

// extData is the extension-instance branch of lys_find_child_node (lys_find_child_node_ext with
// the plugins' snode callbacks): a node of a yang-data or structure tree is found there, and its
// data is not parsed (deviations.md U-0060).
func extData(sparent *schema.Node, mod *schema.Module, name string) error {
	if sn, e := schema.FindExtNode(sparent, mod, mod, name, false); sn != nil {
		return fmt.Errorf("%w: data of extension instance %s:%s %q (deviations.md U-0060)", yang.ErrUnsupported,
			e.Def.Name, e.Name, e.Argument)
	}
	return nil
}
