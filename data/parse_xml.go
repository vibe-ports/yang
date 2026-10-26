// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_xml.c (BSD-3-Clause, © CESNET).

package data

import (
	"math"

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
	// The namespaces in scope by prefix, innermost last, kept in step with the lexer's stack
	// (syncNS): lyxml_ns_get without scanning the stack.
	nsPfx []string            // the prefixes of the lexer's stack, in order
	nsMap map[string][]string // prefix -> URIs, innermost last
}

// newXMLParser is the parser context of lyd_parse_xml and lyd_parse_xml_netconf over in
// (lyxml_ctx_new); pop ends its line numbers in the log.
func newXMLParser(lc *lydCtx, in []byte) (p *xmlParser, pop func(), err error) {
	x, err := lyxml.New(in)
	if err != nil {
		return nil, nil, lc.lexErr(err) // LYVE_SYNTAX or a limit: the parse stops
	}
	lc.log.pushInput(func() int { return int(x.Line()) }) //nolint:gosec // line numbers fit
	p = &xmlParser{lc: lc, x: x, strict: lc.opts.Unknown == Reject, opaq: lc.opts.Unknown == Opaque,
		nsMap: map[string][]string{}}
	p.syncNS()
	return p, lc.log.popInput, nil
}

// parseXML is lyd_parse_xml: every top-level element (only the first one under
// LYD_INTOPT_NO_SIBLINGS) as children of lc.parent (the top level when nil), and the operation
// checks of an operation parse, an rpc-or-action parse first opening a YANG "action" element.
// The NETCONF envelopes are parseXMLNetconf's.
func parseXML(lc *lydCtx, in []byte) error {
	p, pop, err := newXMLParser(lc, in)
	if err != nil {
		return err
	}
	defer pop()
	x := p.x
	if err := lc.findOperation(); err != nil {
		return errLoggedFatal
	}
	closeElem := false
	if lc.op.rpc && lc.op.action {
		// can be either: try to parse "action" (its envelope node is freed again); libyang clears
		// LYD_INTOPT_RPC only in its local copy of the options, which the checks of an rpc inside
		// the element never read
		if _, r := p.envelope("action", nsYang); r == nil {
			closeElem = true
		}
	}
	var rc error
	parsedData := false
	for x.Status == lyxml.Element {
		if r := p.subtree(lc.parent); r != nil {
			if rc = r; lc.fatal(r) {
				return rc
			}
		}
		parsedData = true
		if lc.op.noSiblings {
			break
		}
	}
	if closeElem {
		if x.Status != lyxml.ElemClose {
			_ = lc.log.val(nil, "", ly.Syntax, "Unexpected child element \"%s\".", x.Name)
			return errLoggedFatal
		}
		if err := p.next(); err != nil {
			return err
		}
	}
	if lc.op.noSiblings && x.Status == lyxml.Element {
		r := lc.log.val(nil, "", ly.Syntax, "Unexpected sibling node.")
		if rc = r; lc.fatal(r) {
			return rc
		}
	}
	if lc.op.any() && lc.opNode == nil {
		r := lc.log.val(nil, "", ly.Data, "Missing the operation node.")
		if rc = r; lc.fatal(r) {
			return rc
		}
	}
	if !parsedData {
		lc.opNode = nil // no data nodes were parsed
	}
	return rc
}

// next is lyxml_ctx_next with its error logged (lexErr).
func (p *xmlParser) next() error {
	err := p.x.Next()
	p.syncNS()
	if err != nil {
		return p.lc.lexErr(err)
	}
	return nil
}

// syncNS brings nsMap in step with the lexer's namespace stack. One lexer step opens or closes
// one element, so the stack only grew (declarations of the opened element) or only shrank since
// the last sync; a restored backup only shrinks it (the checks never leave their element).
func (p *xmlParser) syncNS() {
	ns := p.x.NS()
	for len(p.nsPfx) > len(ns) {
		k := p.nsPfx[len(p.nsPfx)-1]
		p.nsPfx = p.nsPfx[:len(p.nsPfx)-1]
		if u := p.nsMap[k]; len(u) > 1 {
			p.nsMap[k] = u[:len(u)-1]
		} else {
			delete(p.nsMap, k)
		}
		p.lc.tree.work.Add(1)
	}
	for _, d := range ns[len(p.nsPfx):] {
		p.nsPfx = append(p.nsPfx, d.Prefix)
		p.nsMap[d.Prefix] = append(p.nsMap[d.Prefix], d.URI)
		p.lc.tree.work.Add(1)
	}
}

// getNS is lyxml_ns_get: the URI of prefix ("" the default namespace) in scope.
func (p *xmlParser) getNS(prefix string) (string, bool) {
	u := p.nsMap[prefix]
	if len(u) == 0 {
		return "", false
	}
	return u[len(u)-1], true
}

// Resolve supplies the current namespace context to typed stores, without scanning
// their text (parser_xml.c:186,445,770). Plugins that retain prefix data take their
// own snapshot before the lexer advances.
func (p *xmlParser) Resolve(prefix string) *schema.Module {
	u, ok := p.getNS(prefix)
	if !ok {
		return nil
	}
	return (types.XMLNamespaces{Set: p.lc.tree.set, NS: map[string]string{prefix: u}}).Resolve(prefix)
}

// Namespace implements types.XMLScope.
func (p *xmlParser) Namespace(prefix string) (string, bool) { return p.getNS(prefix) }

// Context implements types.XMLScope.
func (p *xmlParser) Context() *schema.Set { return p.lc.tree.set }

// prefixes is the prefix context of value at the current position (ly_store_prefix_data with
// LY_VALUE_XML): the default namespace and the in-scope namespaces of the prefixes the value
// uses. A snapshot, because a stored union value or an opaque node keeps it.
// A scanner failure is LY_EINVAL: ly_store_prefix_data propagates ly_value_prefix_next
// (tree_data_common.c:1404-1407), and lydxml_subtree_opaq propagates it unchanged
// (parser_xml.c:680-682).
func (p *xmlParser) prefixes(value string) (types.PrefixCtx, error) {
	p.lc.tree.work.Add(1)
	ns := map[string]string{}
	var order []string
	if u, ok := p.getNS(""); ok {
		ns[""], order = u, append(order, "")
	}
	for i := 0; i >= 0; {
		n, isPrefix, next, ok := lyxml.ValuePrefixNext(value, i)
		if !ok {
			return nil, fatalRC("LY_EINVAL")
		}
		if isPrefix {
			prefix := value[i : i+n]
			if _, done := ns[prefix]; !done {
				if u, ok := p.getNS(prefix); ok {
					ns[prefix], order = u, append(order, prefix)
				}
			}
		}
		i = next
	}
	p.lc.tree.work.Add(int64(len(ns))) // the namespace entries the snapshot copies
	return types.XMLNamespaces{Set: p.lc.tree.set, NS: ns, Order: order}, nil
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
		return fatalRC("LY_ENOTFOUND")
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
			ns, ok := p.getNS(x.Prefix)
			if !ok {
				return nil, notFound(func() error { return p.namespaceErr(lnode, x.Prefix, x.Name) })
			}
			if mod = lc.tree.set.ByNamespace(ns); mod == nil {
				if p.strict {
					return nil, notFound(func() error {
						return lc.log.val(lnode, "", ly.Reference,
							"Unknown (or not implemented) YANG module with namespace \"%s\" for metadata \"%s:%s\".", ns, x.Prefix, x.Name)
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
		if err := lc.createMeta(nil, &metas, mod, name, x.Value, types.FormatXML, p, types.HintData, sparent, lnode); err != nil {
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
			u, ok := p.getNS(prefix)
			if !ok {
				return nil, p.namespaceErr(lnode, prefix, name)
			}
			uri = u
		}
		pc, err := p.prefixes(x.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, attr{Name: name, Prefix: prefix, ModuleNS: uri, Value: x.Value, Format: types.FormatXML,
			Prefixes: pc, Hints: types.HintData})
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// valueValid is ly_value_validate without a context: whether the current value stores as sn's
// type, nothing logged.
func (p *xmlParser) valueValid(sn *schema.Node) (bool, error) {
	_, d := types.Store(sn.Type, p.x.Value, types.FormatXML, types.HintData, p, sn)
	return d == nil, nil
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
		if i < len(keys) {
			valid, err := p.valueValid(keys[i])
			if err != nil {
				return err
			}
			if valid {
				keys[i] = keys[len(keys)-1] // ly_set_rm_index
				keys = keys[:len(keys)-1]
			}
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
	defer func() { x.Restore(b); p.syncNS() }()
	for x.Status == lyxml.Attribute {
		if err := p.skipAttr(); err != nil {
			return sn, err
		}
	}
	switch sn.Kind {
	case schema.Leaf, schema.LeafList:
		valid, err := p.valueValid(sn)
		if err != nil {
			return sn, err
		}
		if !valid {
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
	if o := sibs.opaqueLast(name, ns); o != nil {
		if o.opaq.Value != "" {
			o.opaq.Hints |= hintLeafList
			h |= hintLeafList
		} else {
			o.opaq.Hints |= hintList
			h |= hintList
		}
		return h, o
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
	ns, nsOK := p.getNS(prefix)
	if nsOK {
		if mod := set.ByNamespace(ns); mod != nil {
			if sn := schema.FindChild(sparent, mod.Top, mod, name, lc.getnextOpts()); sn != nil {
				if err := lc.checkSchema(sn); err != nil {
					return nil, err
				}
				return p.checkOpaq(sn)
			}
			if err := extData(sparent, mod, name); err != nil {
				return nil, err
			}
		}
	}
	if !nsOK && !lc.opts.anyContent {
		return nil, p.namespaceErr(parent, prefix, "")
	}
	if !p.strict {
		return nil, nil
	}
	mod := set.ByNamespace(ns)
	switch {
	case mod == nil && nsOK:
		return nil, lc.log.val(parent, "", ly.Reference, "No module with namespace \"%s\" in the context.", ns)
	case mod == nil && prefix != "":
		return nil, lc.log.val(parent, "", ly.Reference, "No module with namespace \"%s\" in the context.", prefix)
	case mod == nil:
		return nil, lc.log.val(parent, "", ly.Reference, "No default namespace in the context.")
	case sparent != nil:
		return nil, lc.log.val(parent, "", ly.Reference, "Node \"%s\" not found as a child of \"%s\" node.", name, sparent.Name)
	}
	return nil, lc.log.val(parent, "", ly.Reference, "Node \"%s\" not found in the \"%s\" module.", name, mod.Name)
}

// subtreeOpaq is lydxml_subtree_opaq: an opaque node from the current element, its children
// and its text; on any error the node is freed again.
func (p *xmlParser) subtreeOpaq(prefix, name string, parent *Node) (node *Node, rc error) {
	lc, x := p.lc, p.x
	value, wsOnly := x.Value, x.WSOnly
	pc, err := p.prefixes(value)
	if err != nil {
		return nil, err
	}
	ns, _ := p.getNS(prefix)
	h, anchor := hintsOpaq(name, value, lc.tree.childrenOf(parent), ns)
	node, err = lc.createOpaq(opaque{Name: name, Prefix: prefix, ModuleNS: ns, Format: types.FormatXML,
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
	if err := lc.nodeInsert(parent, anchor, node); err != nil {
		return node, err
	}
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

// nextAnchor is lyd_insert_get_next_anchor for the linked node n. For monotone ordinary
// siblings, this is the first sibling after the instances of n's schema node.
func (t *Tree) nextAnchor(n *Node) *Node {
	sib := n.siblingsOf()
	if sib == nil {
		return nil
	}
	if sib.schemaUnordered || opChild(n.schema) {
		var at int
		if opChild(n.schema) {
			at = t.opAnchor(sib, n)
		} else {
			at = t.unorderedAnchor(sib, n)
		}
		if at < sib.SchemaLen() {
			return sib.At(at)
		}
		if sib.OpaqueLen() > 0 {
			return sib.opaqueAt(0)
		}
		return nil
	}
	for i := sib.indexOf(sib.schemaSpan(), n) + 1; i < sib.SchemaLen(); i++ {
		if sib.At(i).schema != n.schema {
			return sib.At(i)
		}
	}
	if sib.OpaqueLen() > 0 {
		return sib.opaqueAt(0)
	}
	return nil
}

// subtreeTerm is lydxml_subtree_term: a leaf or leaf-list instance from the element text, the
// key position check (an error under LYD_PARSE_STRICT, else a warning; design 07 §1.3), and no
// child elements.
func (p *xmlParser) subtreeTerm(sn *schema.Node, parent *Node) (*Node, error) {
	lc, x := p.lc, p.x
	node, rc := lc.createTerm(sn, parent, x.Value, types.FormatXML, p, types.HintData)
	if rc != nil && lc.fatal(rc) {
		return nil, rc
	}
	if err := lc.nodeInsert(parent, nil, node); err != nil {
		return node, err
	}
	if node != nil && parent != nil && sn.IsKey() {
		if a := lc.tree.nextAnchor(node); a != nil && a.schema != nil && a.schema.IsKey() {
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
	if err := lc.nodeInsert(parent, nil, node); err != nil {
		return node, err
	}
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
		if err := lc.nodeInsert(parent, nil, node); err != nil { // a list that had its keys missing
			return node, err
		}
	}
	if rc = lc.closeInner(node, rc); rc == nil {
		lc.opParsed(node)
	}
	return node, rc
}

// subtreeAny is lydxml_subtree_any. The first text token decides the representation:
// whitespace (including an empty element) starts a data tree, while anyxml may retain
// non-whitespace text (an element after that text is not rejected here, as in libyang; only an
// opaque payload node checks mixed content). Payload elements use the same lexer and namespace scope as the
// enclosing document; they are owned by the any node, outside ordinary traversal.
func (p *xmlParser) subtreeAny(sn *schema.Node, parent *Node) (*Node, error) {
	lc, x := p.lc, p.x
	var value *string
	if !x.WSOnly {
		if sn.Kind == schema.AnyData {
			text := x.Value
			if len(text) > 20 {
				text = text[:20]
			}
			return nil, lc.log.val(parent, "", ly.Syntax,
				"Text value \"%s\" inside an anydata node \"%s\" found.", text, sn.Name)
		}
		value = &x.Value
	}
	if err := lc.countNode(); err != nil {
		return nil, err
	}
	node, _, err := newAnyWork(lc.ctx, lc.opts.Budget).createAny(lc.tree, sn, nil, value, 0, true, false)
	if err != nil {
		return nil, err
	}
	node.flags = lc.newFlags()
	if err := lc.nodeInsert(parent, nil, node); err != nil {
		return node, err
	}
	if err := p.next(); err != nil {
		return node, err
	}
	if value == nil {
		strict, opaq, anyContent := p.strict, p.opaq, lc.opts.anyContent
		defer func() { p.strict, p.opaq, lc.opts.anyContent = strict, opaq, anyContent }()
		p.strict = lc.opts.AnyDataStrict
		if !p.strict {
			p.opaq = true
		}
		lc.opts.anyContent = true
		var rc error
		for x.Status == lyxml.Element {
			if r := p.subtree(node); r != nil {
				if rc = r; lc.fatal(r) {
					return node, rc
				}
			}
		}
		return node, rc
	}
	return node, nil
}

// freeFailed is the cleanup condition of lydxml_subtree_opaq, _term and _inner after their error
// r: an opaque node always, a schema node unless the error is a validation error that
// LYD_VALIDATE_MULTI_ERROR goes past, a list instance also when it lacks keys. Errors after the
// node was parsed (moving past its closing tag) free nothing: the node stays linked.
func (p *xmlParser) freeFailed(sn *schema.Node, node *Node, r error) bool {
	if sn == nil || !p.lc.isEValid(r) || !p.lc.opts.Validate.MultiError {
		return true
	}
	return sn.Kind == schema.List && !node.lkeyOK && !sn.Keyless()
}

// subtree is lydxml_subtree_r: the current element and its descendants as data nodes under parent
// (the top level when nil).
func (p *xmlParser) subtree(parent *Node) error {
	lc, x := p.lc, p.x
	var node *Node
	prefix, name := x.Prefix, x.Name
	if err := p.next(); err != nil {
		return err
	}
	if lc.op.eventTime && parent == nil && name == "eventTime" && prefix == "" {
		return p.eventTimeXML()
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
	switch {
	case sn == nil:
		node, r = p.subtreeOpaq(prefix, name, parent)
	case sn.Kind == schema.Leaf || sn.Kind == schema.LeafList:
		node, r = p.subtreeTerm(sn, parent)
	case isAny(sn):
		node, r = p.subtreeAny(sn, parent)
	default:
		node, r = p.subtreeInner(sn, parent)
	}
	if r != nil && node != nil && p.freeFailed(sn, node, r) {
		lc.nodeFree(node)
		node = nil
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
		node.touch()
		node.meta = append(node.meta, metas...) // lyd_insert_meta without clearing the default flags
	default:
		for _, a := range attrs {
			createAttr(node, a) // lyd_insert_attr
		}
	}
	if node != nil && parent == lc.parent && lc.op.any() {
		lc.parsed = append(lc.parsed, node) // lyd_parse_op's parsed set
	}
	return rc
}
