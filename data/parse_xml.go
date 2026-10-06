// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_xml.c (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"
	"math"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyxml"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// xmlParser is struct lyd_xml_ctx: the parser context and the lexer.
type xmlParser struct {
	lc           *lydCtx
	x            *lyxml.Ctx
	strict, opaq bool // LYD_PARSE_STRICT, LYD_PARSE_OPAQ
}

// parseXML is lyd_parse_xml for datastore data (LYD_INTOPT_WITH_SIBLINGS, no parent): every
// top-level element. Operations and NETCONF envelopes are design 07 M4; anydata/anyxml instances
// fail with yang.ErrUnsupported (deviations.md U-0043).
func parseXML(lc *lydCtx, in []byte) error {
	x, err := lyxml.New(in)
	if err != nil {
		return lc.lexErr(err) // LYVE_SYNTAX or a limit: lyd_parse stops
	}
	lc.log.pushInput(func() int { return int(x.Line()) }) //nolint:gosec // line numbers fit
	defer lc.log.popInput()
	p := &xmlParser{lc: lc, x: x, strict: lc.opts.Unknown == Reject, opaq: lc.opts.Unknown == Opaque}
	var rc error
	for x.Status == lyxml.Element {
		if r := p.subtree(nil); r != nil {
			if rc = r; lc.fatal(r) {
				return rc
			}
		}
	}
	return rc
}

// next is lyxml_ctx_next with its error logged (lexErr).
func (p *xmlParser) next() error {
	if err := p.x.Next(); err != nil {
		return p.lc.lexErr(err)
	}
	return nil
}

// prefixes is the prefix context of a value at the current position (ly_store_prefix_data with
// LY_VALUE_XML): the namespaces in scope, innermost first. A snapshot, because a stored union
// value or an opaque node keeps it.
func (p *xmlParser) prefixes() types.PrefixCtx {
	ns := map[string]string{}
	for _, d := range p.x.NS() {
		ns[d.Prefix] = d.URI // innermost last: it wins
	}
	return types.XMLNamespaces{Set: p.lc.tree.set, NS: ns}
}

// namespaceErr is lydxml_log_namespace_err.
func (p *xmlParser) namespaceErr(lnode *Node, prefix, attrName string) error {
	switch {
	case prefix != "" && attrName != "":
		return p.lc.log.val(lnode, "", ly.Reference, "Unknown XML prefix \"%s\" at attribute \"%s\".", prefix, attrName)
	case prefix != "":
		return p.lc.log.val(lnode, "", ly.Reference, "Unknown XML prefix \"%s\".", prefix)
	}
	return p.lc.log.val(lnode, "", ly.Reference, "Missing XML namespace.")
}

// skipAttr moves past an attribute and its value.
func (p *xmlParser) skipAttr() error {
	if err := p.next(); err != nil {
		return err
	}
	return p.next()
}

// metadata is lydxml_metadata: the attributes of the current element as metadata of a node of
// sparent, logged at lnode (the parent data node). LY_ENOTFOUND (errLoggedFatal) for an unknown
// prefix, or an unknown module under LYD_PARSE_STRICT.
func (p *xmlParser) metadata(sparent *schema.Node, lnode *Node) (metas []*meta, rc error) {
	lc, x := p.lc, p.x
	defer func() {
		if rc != nil {
			metas = nil // lyd_free_meta_siblings
		}
	}()
	// LOGVAL at the schema node, then LY_ENOTFOUND
	notFound := func(log func() error) error {
		lc.log.locSet(sparent)
		defer lc.log.locBack(1)
		_ = log()
		return errLoggedFatal
	}
	// NETCONF filter attributes need no prefix (an ancient module, or the extension marks them)
	filterAttrs := sparent.Module.Name == "notifications"
	for _, e := range sparent.Exts {
		if e.Def != nil && e.Def.Name == "ietf-netconf" && e.Name == "get-filter-element-attributes" {
			filterAttrs = true
		}
	}
	for x.Status == lyxml.Attribute {
		var mod *schema.Module
		switch {
		case x.Prefix == "" && filterAttrs && (x.Name == "type" || x.Name == "select"):
			if mod = lc.tree.set.Implemented("ietf-netconf"); mod == nil {
				return nil, notFound(func() error {
					return lc.log.val(lnode, "", ly.Reference,
						"Missing (or not implemented) YANG module \"ietf-netconf\" for special filter attributes.")
				})
			}
		case x.Prefix == "":
			if p.strict {
				lc.log.locSet(sparent)
				rc = lc.log.val(lnode, "", ly.Reference, "Missing mandatory prefix for XML metadata \"%s\".", x.Name)
				lc.log.locBack(1)
				if lc.fatal(rc) {
					return nil, rc
				}
			}
			if err := p.skipAttr(); err != nil {
				return nil, err
			}
			continue
		default:
			ns, ok := x.GetNS(x.Prefix)
			if !ok {
				return nil, notFound(func() error { return p.namespaceErr(lnode, x.Prefix, x.Name) })
			}
			if mod = lc.tree.set.ByNamespace(ns.URI); mod == nil {
				if p.strict {
					return nil, notFound(func() error {
						return lc.log.val(lnode, "", ly.Reference,
							"Unknown (or not implemented) YANG module with namespace \"%s\" for metadata \"%s:%s\".", ns.URI, x.Prefix, x.Name)
					})
				}
				if err := p.skipAttr(); err != nil {
					return nil, err
				}
				continue
			}
		}
		name := x.Name
		if err := p.next(); err != nil { // the value
			return nil, err
		}
		if err := lc.createMeta(nil, &metas, mod, name, x.Value, types.FormatXML, p.prefixes(), types.HintData, sparent, lnode); err != nil {
			return nil, err
		}
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	return metas, rc
}

// attrs is lydxml_attrs: the attributes of the current element as generic attributes of an
// opaque node, logged at lnode.
func (p *xmlParser) attrs(lnode *Node) ([]attr, error) {
	x := p.x
	var out []attr
	for x.Status == lyxml.Attribute {
		prefix, name := x.Prefix, x.Name
		if err := p.next(); err != nil { // the value
			return nil, err
		}
		if prefix == "xml" { // the special "xml" prefix stays in the name
			prefix, name = "", "xml:"+name
		}
		var uri string
		if prefix != "" {
			ns, ok := x.GetNS(prefix)
			if !ok {
				return nil, p.namespaceErr(lnode, prefix, name)
			}
			uri = ns.URI
		}
		out = append(out, attr{Name: name, Prefix: prefix, ModuleNS: uri, Value: x.Value, Format: types.FormatXML,
			Prefixes: p.prefixes(), Hints: types.HintData})
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// valueValid is ly_value_validate without a context: whether the current value stores as sn's
// type, nothing logged.
func (p *xmlParser) valueValid(sn *schema.Node) bool {
	_, d := types.Store(sn.Type, p.x.Value, types.FormatXML, types.HintData, p.prefixes(), sn)
	return d == nil
}

// checkList is lydxml_check_list: whether the list instance at the lexer has every key with a
// valid value (errNot: no). Keys are matched by name only, as libyang does.
func (p *xmlParser) checkList(list *schema.Node) error {
	x := p.x
	keys := append([]*schema.Node(nil), list.Keys...)
	parents := x.Depth()
	for x.Status == lyxml.Element {
		i := 0
		for i < len(keys) && keys[i].Name != x.Name {
			i++
		}
		if err := p.next(); err != nil {
			return err
		}
		for x.Status == lyxml.Attribute {
			if err := p.skipAttr(); err != nil {
				return err
			}
		}
		if i < len(keys) && p.valueValid(keys[i]) {
			keys[i] = keys[len(keys)-1] // ly_set_rm_index
			keys = keys[:len(keys)-1]
		}
		if err := p.next(); err != nil {
			return err
		}
		for x.Status == lyxml.Element { // skip any children, recursively
			for parents < x.Depth() {
				if err := p.next(); err != nil {
					return err
				}
			}
			if err := p.next(); err != nil {
				return err
			}
		}
		// on the element's close; do not read the list's own close, it would drop its namespaces
		st, err := x.Peek()
		if err != nil {
			return p.lc.lexErr(err)
		}
		if st != lyxml.ElemClose {
			if err := p.next(); err != nil {
				return err
			}
		}
	}
	if len(keys) > 0 {
		return errNot
	}
	return nil
}

// dataSkip is lydxml_data_skip: past the current element with all its descendants.
func (p *xmlParser) dataSkip() error {
	x := p.x
	parents := x.Depth()
	for x.Status != lyxml.ElemContent {
		if err := p.next(); err != nil {
			return err
		}
	}
	if err := p.next(); err != nil {
		return err
	}
	for parents <= x.Depth() {
		if err := p.next(); err != nil {
			return err
		}
	}
	return p.next() // the element's close
}

// checkOpaq is lydxml_data_check_opaq: with LYD_PARSE_OPAQ, nil when the element cannot be an
// instance of sn (it is parsed as opaque). The lexer is restored (single-use backup).
func (p *xmlParser) checkOpaq(sn *schema.Node) (*schema.Node, error) {
	x := p.x
	switch {
	case !p.opaq:
		return sn, nil
	case sn.Kind == schema.Leaf || sn.Kind == schema.LeafList:
	case sn.Kind == schema.Container || sn.Kind == schema.List || sn.Kind == schema.RPC ||
		sn.Kind == schema.Action || sn.Kind == schema.Notification:
	default:
		return sn, nil
	}
	b := x.Backup()
	defer x.Restore(b)
	for x.Status == lyxml.Attribute {
		if err := p.skipAttr(); err != nil {
			return sn, err
		}
	}
	switch sn.Kind {
	case schema.Leaf, schema.LeafList:
		if !p.valueValid(sn) {
			return nil, nil
		}
	case schema.List:
		if err := p.next(); err != nil { // the content
			return sn, err
		}
		if p.checkList(sn) != nil {
			return nil, nil
		}
	default:
		if !x.WSOnly { // a value: not an inner node
			return nil, nil
		}
	}
	return sn, nil
}

// strtollLen is how many bytes of s C strtoll(s, &end, 10) consumes: white space, a sign and
// digits; 0 without digits.
func strtollLen(s string) (n int, v int64) {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] >= '\t' && s[i] <= '\r') {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	var u uint64
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		if u <= math.MaxInt64 {
			u = u*10 + uint64(s[i]-'0')
		}
		i++
	}
	if i == start {
		return 0, 0
	}
	switch {
	case neg && u > math.MaxInt64:
		v = math.MinInt64
	case neg:
		v = -int64(u)
	case u > math.MaxInt64:
		v = math.MaxInt64
	default:
		v = int64(u)
	}
	return i, v
}

// isPrefix reports whether s is a prefix of word (C strncmp(s, word, strlen(s)) == 0).
func isPrefix(s, word string) bool { return len(s) <= len(word) && word[:len(s)] == s }

// hintsOpaq is lydxml_get_hints_opaq: value hints guessed from the text, and the last opaque
// sibling with the same name and namespace, which becomes a list (no value) or leaf-list
// instance and the anchor to insert after.
func hintsOpaq(name, value string, sibs *siblings, ns string) (h types.Hints, anchor *Node) {
	switch {
	case value == "":
		h = types.HintEmpty | types.HintString // no value, or an empty string
	case isPrefix(value, "true") || isPrefix(value, "false"): // strncmp(value, "true", len)
		h = types.HintBoolean
	default:
		if n, num := strtollLen(value); n == len(value) {
			h = types.HintDecNum
			if num < math.MinInt32 || num > math.MaxUint32 {
				h |= types.HintNum64
			}
		} else {
			h = types.HintString
		}
	}
	if sibs == nil {
		return h, nil
	}
	for i := len(sibs.opq) - 1; i >= 0; i-- {
		o := sibs.opq[i]
		if o.opaq.Name == name && o.opaq.ModuleNS == ns {
			if o.opaq.Value != "" {
				o.opaq.Hints |= hintLeafList
				h |= hintLeafList
			} else {
				o.opaq.Hints |= hintList
				h |= hintList
			}
			return h, o
		}
	}
	return h, nil
}

// getSnode is lydxml_subtree_get_snode: the schema node of element prefix:name under parent; nil
// when there is none or (LYD_PARSE_OPAQ) the element does not fit it, and that is not an error.
func (p *xmlParser) getSnode(parent *Node, prefix, name string) (*schema.Node, error) {
	lc, set := p.lc, p.lc.tree.set
	var sparent *schema.Node
	if parent != nil && parent.schema != nil && !isAny(parent.schema) {
		sparent = parent.schema
	}
	ns, nsOK := p.x.GetNS(prefix)
	if nsOK {
		if mod := set.ByNamespace(ns.URI); mod != nil {
			if sn := schema.FindChild(sparent, mod.Top, mod, name, 0); sn != nil {
				if err := lc.checkSchema(sn); err != nil {
					return nil, err
				}
				return p.checkOpaq(sn)
			}
		}
	}
	if !nsOK {
		return nil, p.namespaceErr(parent, prefix, "")
	}
	if !p.strict {
		return nil, nil
	}
	mod := set.ByNamespace(ns.URI)
	switch {
	case mod == nil:
		return nil, lc.log.val(parent, "", ly.Reference, "No module with namespace \"%s\" in the context.", ns.URI)
	case sparent != nil:
		return nil, lc.log.val(parent, "", ly.Reference, "Node \"%s\" not found as a child of \"%s\" node.", name, sparent.Name)
	}
	return nil, lc.log.val(parent, "", ly.Reference, "Node \"%s\" not found in the \"%s\" module.", name, mod.Name)
}

// subtreeOpaq is lydxml_subtree_opaq: an opaque node from the current element, its children
// and its text; on any error the node is freed again.
func (p *xmlParser) subtreeOpaq(prefix, name string, parent *Node) (node *Node, rc error) {
	lc, x := p.lc, p.x
	value, wsOnly, pc := x.Value, x.WSOnly, p.prefixes()
	ns, _ := x.GetNS(prefix)
	h, anchor := hintsOpaq(name, value, lc.tree.childrenOf(parent), ns.URI)
	node, err := lc.createOpaq(opaque{Name: name, Prefix: prefix, ModuleNS: ns.URI, Format: types.FormatXML,
		Prefixes: pc, Hints: h})
	if err != nil {
		return nil, err
	}
	defer func() {
		if rc != nil {
			lc.nodeFree(node)
			node = nil
		}
	}()
	lc.nodeInsert(parent, anchor, node)
	if err := p.next(); err != nil {
		return node, err
	}
	for x.Status == lyxml.Element {
		if err := p.subtree(node); err != nil {
			return node, err
		}
	}
	switch {
	case node.kids.len() > 0:
		if !wsOnly {
			return node, lc.log.val(node, "", ly.SyntaxXML, "Mixed XML content node \"%s\" found, not supported.", name)
		}
	default:
		node.opaq.Value = value
	}
	return node, nil
}

// nextAnchor is lyd_insert_get_next_anchor for the linked node n: the first sibling after the
// instances of n's schema node.
func nextAnchor(n *Node) *Node {
	sib := n.siblingsOf()
	for i := sib.indexOf(sib.list, n) + 1; i < len(sib.list); i++ {
		if sib.list[i].schema != n.schema {
			return sib.list[i]
		}
	}
	if len(sib.opq) > 0 {
		return sib.opq[0]
	}
	return nil
}

// subtreeTerm is lydxml_subtree_term: a leaf or leaf-list instance from the element text, the
// key position check (an error under LYD_PARSE_STRICT, else a warning; design 07 §1.3), and no
// child elements.
func (p *xmlParser) subtreeTerm(sn *schema.Node, parent *Node) (*Node, error) {
	lc, x := p.lc, p.x
	node, rc := lc.createTerm(sn, parent, x.Value, types.FormatXML, p.prefixes(), types.HintData)
	if rc != nil && lc.fatal(rc) {
		return nil, rc
	}
	lc.nodeInsert(parent, nil, node)
	if node != nil && parent != nil && sn.IsKey() {
		if a := nextAnchor(node); a != nil && a.schema != nil && a.schema.IsKey() {
			if p.strict {
				r := lc.log.val(node, "", ly.Data, "Invalid position of the key \"%s\" in a list.", sn.Name)
				if rc = r; lc.fatal(r) {
					return node, rc
				}
			} else {
				lc.log.warn("Invalid position of the key \"%s\" in a list.", sn.Name)
			}
		}
	}
	if err := p.next(); err != nil {
		return node, err
	}
	if x.Status == lyxml.Element {
		r := lc.log.val(node, "", ly.Syntax, "Child element \"%s\" inside a terminal node \"%s\" found.", x.Name, sn.Name)
		if rc = r; lc.fatal(r) {
			return node, rc
		}
	}
	return node, rc
}

// subtreeInner is lydxml_subtree_inner: a container or list instance, its children, the key check
// and the new-node validation (closeInner).
func (p *xmlParser) subtreeInner(sn *schema.Node, parent *Node) (*Node, error) {
	lc, x := p.lc, p.x
	var rc error
	if !x.WSOnly {
		r := lc.log.val(parent, "", ly.Syntax, "Text value \"%s\" inside an inner node \"%s\" found.", x.Value, sn.Name)
		if rc = r; lc.fatal(r) {
			return nil, rc
		}
	}
	node, err := lc.createInner(sn)
	if err != nil {
		return nil, err
	}
	lc.nodeInsert(parent, nil, node)
	if err := p.next(); err != nil {
		return node, err
	}
	// libyang frees the node after an error that stops the parse; the tree is dropped then anyway
	for x.Status == lyxml.Element {
		if r := p.subtree(node); r != nil {
			if rc = r; lc.fatal(r) {
				return node, rc
			}
		}
		lc.nodeInsert(parent, nil, node) // a list that had its keys missing
	}
	return node, lc.closeInner(node, rc)
}

// subtree is lydxml_subtree_r: the current element and its descendants as data nodes under parent
// (the top level when nil).
func (p *xmlParser) subtree(parent *Node) error {
	lc, x := p.lc, p.x
	prefix, name := x.Prefix, x.Name
	if err := p.next(); err != nil {
		return err
	}
	sn, r := p.getSnode(parent, prefix, name)
	switch {
	case r != nil:
		rc := r
		if lc.isEValid(r) && lc.opts.Validate.MultiError {
			if r := p.dataSkip(); r != nil { // skip the invalid data
				rc = r
			}
		}
		return rc
	case sn == nil && !p.opaq:
		return p.dataSkip() // an unknown node, skipped
	}
	var rc error
	var metas []*meta
	var attrs []attr
	if x.Status == lyxml.Attribute {
		if sn != nil {
			metas, r = p.metadata(sn, parent)
		} else {
			attrs, r = p.attrs(parent)
		}
		if r != nil {
			if rc = r; lc.fatal(r) {
				return rc
			}
		}
	}
	var node *Node
	switch {
	case sn == nil:
		node, r = p.subtreeOpaq(prefix, name, parent)
	case sn.Kind == schema.Leaf || sn.Kind == schema.LeafList:
		node, r = p.subtreeTerm(sn, parent)
	case isAny(sn):
		return fmt.Errorf("%w: %s %q instance (anydata/anyxml data, deviations.md U-0043)",
			yang.ErrUnsupported, nodetypeStr(sn.Kind), sn.Name)
	default:
		node, r = p.subtreeInner(sn, parent)
	}
	if r != nil {
		if rc = r; lc.fatal(r) {
			return rc
		}
	}
	if node != nil && sn != nil {
		lc.setDataFlags(node, &metas)
	}
	if err := p.next(); err != nil { // past the element's close
		return err
	}
	switch {
	case node == nil:
	case sn != nil:
		node.meta = append(node.meta, metas...) // lyd_insert_meta without clearing the default flags
	default:
		for _, a := range attrs {
			createAttr(node, a) // lyd_insert_attr
		}
	}
	return rc
}
