// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_xml.c (BSD-3-Clause, © CESNET).

package data

import (
	"errors"
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
	// The namespaces in scope by prefix, innermost last, kept in step with the lexer's stack
	// (syncNS): lyxml_ns_get without scanning the stack.
	nsPfx []string            // the prefixes of the lexer's stack, in order
	nsMap map[string][]string // prefix -> URIs, innermost last
}

// parseXML is lyd_parse_xml: every top-level element (only the first one under
// LYD_INTOPT_NO_SIBLINGS) as children of lc.parent (the top level when nil), and the operation
// checks of an operation parse, an rpc-or-action parse first opening a YANG "action" element.
// NETCONF envelopes are not ported; anydata/anyxml instances fail with yang.ErrUnsupported
// (deviations.md U-0043).
func parseXML(lc *lydCtx, in []byte) error {
	x, err := lyxml.New(in)
	if err != nil {
		return lc.lexErr(err) // LYVE_SYNTAX or a limit: lyd_parse stops
	}
	lc.log.pushInput(func() int { return int(x.Line()) }) //nolint:gosec // line numbers fit
	defer lc.log.popInput()
	p := &xmlParser{lc: lc, x: x, strict: lc.opts.Unknown == Reject, opaq: lc.opts.Unknown == Opaque,
		nsMap: map[string][]string{}}
	p.syncNS()
	if err := lc.findOperation(); err != nil {
		return errLoggedFatal
	}
	closeElem := false
	if lc.op.rpc && lc.op.action {
		// can be either: try to parse "action"; libyang clears LYD_INTOPT_RPC only in its local
		// copy of the options, which the checks of an rpc inside the element never read
		if r := p.envelope("action", "urn:ietf:params:xml:ns:yang:1"); r == nil {
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

// errNotEnvelope is lydxml_envelope's LY_ENOT: the current element is not the envelope.
var errNotEnvelope = errors.New("data: not the envelope element")

// envelope is lydxml_envelope for an envelope element without a value: when the current element
// is name in namespace uri, it is opened and its attributes read (the opaque envelope node
// libyang creates is freed by its only caller here, so none is made). errNotEnvelope when it is
// another element.
func (p *xmlParser) envelope(name, uri string) error {
	x := p.x
	if x.Status != lyxml.Element || x.Name != name {
		return errNotEnvelope
	}
	ns, ok := p.getNS(x.Prefix)
	if !ok {
		return p.namespaceErr(nil, x.Prefix, "")
	} else if ns != uri {
		return errNotEnvelope
	}
	if err := p.next(); err != nil {
		return err
	}
	if x.Status == lyxml.Attribute {
		if _, err := p.attrs(nil); err != nil {
			return err
		}
	}
	if !x.WSOnly {
		return p.lc.log.val(nil, "", ly.Syntax, "Unexpected value \"%s\" in the \"%s\" element.", x.Value, name)
	}
	return p.next()
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

// prefixes is the prefix context of value at the current position (ly_store_prefix_data with
// LY_VALUE_XML): the default namespace and the in-scope namespaces of the prefixes the value
// uses. A snapshot, because a stored union value or an opaque node keeps it.
func (p *xmlParser) prefixes(value string) types.PrefixCtx {
	p.lc.tree.work.Add(1)
	ns := map[string]string{}
	var order []string
	if u, ok := p.getNS(""); ok {
		ns[""], order = u, append(order, "")
	}
	for i := 0; i < len(value); {
		// ly_value_prefix_next: an XML name followed by ':'
		if !isNameStartByte(value[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(value) && isNameByte(value[j]) {
			j++
		}
		if j < len(value) && value[j] == ':' {
			if _, done := ns[value[i:j]]; !done {
				if u, ok := p.getNS(value[i:j]); ok {
					ns[value[i:j]], order = u, append(order, value[i:j])
				}
			}
			j++
		}
		i = j
	}
	p.lc.tree.work.Add(int64(len(ns))) // the namespace entries the snapshot copies
	return types.XMLNamespaces{Set: p.lc.tree.set, NS: ns, Order: order}
}

// isNameStartByte and isNameByte approximate is_xmlqnamestartchar/is_xmlqnamechar per byte (any
// non-ASCII byte counts as a name character): a superset of the prefixes libyang collects, which
// resolves the same way.
func isNameStartByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_' || b >= 0x80
}

func isNameByte(b byte) bool {
	return isNameStartByte(b) || b >= '0' && b <= '9' || b == '-' || b == '.'
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
		if err := lc.createMeta(nil, &metas, mod, name, x.Value, types.FormatXML, p.prefixes(x.Value), types.HintData, sparent, lnode); err != nil {
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
		out = append(out, attr{Name: name, Prefix: prefix, ModuleNS: uri, Value: x.Value, Format: types.FormatXML,
			Prefixes: p.prefixes(x.Value), Hints: types.HintData})
		if err := p.next(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// valueValid is ly_value_validate without a context: whether the current value stores as sn's
// type, nothing logged.
func (p *xmlParser) valueValid(sn *schema.Node) bool {
	_, d := types.Store(sn.Type, p.x.Value, types.FormatXML, types.HintData, p.prefixes(p.x.Value), sn)
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
	defer func() { x.Restore(b); p.syncNS() }()
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
	if !nsOK {
		return nil, p.namespaceErr(parent, prefix, "")
	}
	if !p.strict {
		return nil, nil
	}
	mod := set.ByNamespace(ns)
	switch {
	case mod == nil:
		return nil, lc.log.val(parent, "", ly.Reference, "No module with namespace \"%s\" in the context.", ns)
	case sparent != nil:
		return nil, lc.log.val(parent, "", ly.Reference, "Node \"%s\" not found as a child of \"%s\" node.", name, sparent.Name)
	}
	return nil, lc.log.val(parent, "", ly.Reference, "Node \"%s\" not found in the \"%s\" module.", name, mod.Name)
}

// subtreeOpaq is lydxml_subtree_opaq: an opaque node from the current element, its children
// and its text; on any error the node is freed again.
func (p *xmlParser) subtreeOpaq(prefix, name string, parent *Node) (node *Node, rc error) {
	lc, x := p.lc, p.x
	value, wsOnly, pc := x.Value, x.WSOnly, p.prefixes(x.Value)
	ns, _ := p.getNS(prefix)
	h, anchor := hintsOpaq(name, value, lc.tree.childrenOf(parent), ns)
	node, err := lc.createOpaq(opaque{Name: name, Prefix: prefix, ModuleNS: ns, Format: types.FormatXML,
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
	node, rc := lc.createTerm(sn, parent, x.Value, types.FormatXML, p.prefixes(x.Value), types.HintData)
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
	if rc = lc.closeInner(node, rc); rc == nil {
		lc.opParsed(node)
	}
	return node, rc
}

// freeFailed is the cleanup condition of lydxml_subtree_opaq, _term and _inner after their error
// r: an opaque node always, a schema node unless the error is a validation error that
// LYD_VALIDATE_MULTI_ERROR goes past, a list instance also when it lacks keys. Errors after the
// node was parsed (moving past its closing tag) free nothing: the node stays linked.
func (p *xmlParser) freeFailed(sn *schema.Node, node *Node, r error) bool {
	if sn == nil || !p.lc.isEValid(r) || !p.lc.opts.Validate.MultiError {
		return true
	}
	_, hashed := hashOf(node)
	return sn.Kind == schema.List && !hashed
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
		return fmt.Errorf("%w: %s %q instance (anydata/anyxml data, deviations.md U-0043)",
			yang.ErrUnsupported, nodetypeStr(sn.Kind), sn.Name)
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
