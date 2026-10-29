// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (the data and schema accessors the evaluator reads:
// set_comp_canonize, lysc_has_when / LYD_WHEN_* in moveto, lyxp_get_root_type) and
// src/tree_data_common.c (lyd_node_module) (BSD-3-Clause, © CESNET).

package data

import (
	"bytes"
	"context"
	"errors"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

// xn adapts a data node to xpath.Node. It is a comparable value: two xn of one *Node are ==.
type xn struct {
	n   *Node
	set *schema.Set
}

func wrap(set *schema.Set, n *Node) xpath.Node {
	if n == nil {
		return nil
	}
	return xn{n, xpathNodeSet(set, n)}
}

// xpathNodeSet is LYD_CTX(n) (ctxOf: a schema-bound node's compiling context, an opaque
// node's own, a payload root's source context inherited by its descendants), with the
// fallback for ownerless hand-built schemas.
func xpathNodeSet(fallback *schema.Set, n *Node) *schema.Set {
	if set := ctxOf(n); set != nil {
		return set
	}
	return fallback
}

func (x xn) Parent() xpath.Node { return wrap(x.set, x.n.parent) }

// Ordinary child and descendant axes and the predicate's first-instance lookup
// enter any payloads. The hash lookup below reads only lyd_child's sibling list.
func (x xn) Children() []xpath.Node {
	if x.n.flags&flagDead != 0 {
		return nil // deleted by the running when pass: its subtree is gone for XPath too
	}
	sib := x.childSiblings()
	out := make([]xpath.Node, 0, sib.len())
	for c := range sib.all() {
		if c.flags&flagDead == 0 {
			out = append(out, wrap(x.set, c))
		}
	}
	return out
}

func (x xn) childSiblings() *siblings {
	if x.n.any != nil {
		return &x.n.any.children
	}
	return &x.n.kids
}

// LookupChild serves schema-only lookup and callers without a prepared probe.
// The evaluator uses LookupPrepared for typed predicates, preparing once per step.
func (x xn) LookupChild(sn xpath.SchemaNode, vals []string) ([]xpath.Node, bool) {
	s, ok := sn.(xs)
	if !ok {
		return nil, false
	}
	if vals != nil {
		if len(vals) != len(s.s.Keys) && (s.s.Kind != schema.LeafList || len(vals) != 1) {
			return nil, false
		}
		probe, err := s.PrepareInstance(vals, nil)
		if err != nil {
			return nil, false
		}
		nodes, found, _ := x.LookupPrepared(probe)
		return nodes, found
	}
	probe := &xprobe{node: newInner(s.s), set: s.set}
	probe.key, _ = hashOf(probe.node)
	nodes, found, _ := x.LookupPrepared(probe)
	return nodes, found
}

// LookupPrepared follows native registration order for keyed lists, including
// duplicate exact buckets. Only native duplicate-instance kinds need the charged
// document-order fallback (tree_data.c:3213-3235). The fixed-size discriminator
// avoids hashing predicate bytes again for each parent; equality verifies collisions.
func (x xn) LookupPrepared(prepared xpath.InstanceProbe) ([]xpath.Node, bool, error) {
	// moveto_node_hash_child passes lyd_child(x), which excludes any payloads.
	// A handled miss also prevents the evaluator's Children() fallback scan.
	if x.n.any != nil {
		return nil, true, nil
	}
	p, ok := prepared.(*xprobe)
	sib := &x.n.kids
	if !ok || sib.ht == nil || sib.mixedContexts() || x.n.flags&flagDead != 0 {
		return nil, false, nil
	}
	if p.set != x.set {
		// A name test compiled from one context can be applied to another
		// context's node in a union. Translate it before probing the hash: the
		// table's records match by lyd_compare_schema_equal with all the parents
		// (lyd_hash_table_schema_val_equal, lyd_hash_table_val_equal).
		mapped, err := findSchemaCtx(p.node.schema, x.set, x.n)
		if err != nil || mapped == nil || !compareSchemaEqual(mapped, p.node.schema, true) {
			// LY_ENOTFOUND, then lyd_find_sibling_opaq_next. Not a decline: the
			// evaluator's scan would match the unequal node by module and name.
			for _, c := range sib.opaqTail().All() {
				if err := p.work(1); err != nil {
					return nil, false, err
				}
				sib.visit()
				if c.flags&flagDead == 0 && c.Name() == p.node.schema.Name {
					return []xpath.Node{wrap(x.set, c)}, true, nil
				}
			}
			return nil, true, nil
		}
		if p = p.toCtx(x.set); p == nil {
			return nil, false, nil // the instance does not store there: the charged scan
		}
	}
	c := sib.ht[p.key]
	if c != nil && c.count > 1 && isDupInstList(p.node.schema) {
		return nil, false, nil
	}
	for n := range sib.bucketNodes(p.key) {
		if err := p.work(1); err != nil {
			return nil, false, err
		}
		if n.flags&flagDead != 0 {
			continue
		}
		match, err := p.Match(xn{n, x.set})
		if err != nil {
			return nil, false, err
		}
		if match {
			return []xpath.Node{wrap(x.set, n)}, true, nil
		}
	}
	return nil, sib.OpaqueLen() == 0, nil
}

// HashKeyValuesEqual applies lyd_hash's LYB value gate when XPath's keyed-list
// optimization falls back to a charged mixed-context child scan.
func (x xn) HashKeyValuesEqual(sn xpath.SchemaNode, vals []string) bool {
	if len(vals) == 0 {
		return true
	}
	s, ok := sn.(xs)
	if !ok || x.n.schema == nil || x.n.schema.Kind != schema.List ||
		len(s.s.Keys) < len(vals) || x.n.kids.SchemaLen() < len(vals) {
		return false
	}
	prefix := x.n.kids.schemaSpan().cursor()
	for i, text := range vals {
		key := s.s.Keys[i]
		value, diag := types.StoreOnly(key.Type, text, types.FormatCanon, types.HintData,
			types.ModuleNames{Set: s.set}, key)
		if diag != nil || !hashValueEqual(prefix.next().value, value) {
			return false
		}
	}
	return true
}

// FirstChild is lyd_find_sibling_schema over lyd_child_any(x): the first live instance of sn.
// It uses the index within one context and matches schema paths across contexts; a node outside
// a tree or a dead instance declines to the evaluator's charged scan.
func (x xn) FirstChild(sn xpath.SchemaNode) (xpath.Node, bool) {
	s, ok := sn.(xs)
	t := treeOf(x.n)
	if !ok || t == nil || x.n.flags&flagDead != 0 {
		return nil, false
	}
	sib := x.childSiblings()
	if sib.mixedContexts() {
		return nil, false // siblings of several contexts: the evaluator's scan with SchemaLookupMatch
	}
	if first := sib.first(); first != nil && xpathNodeSet(x.set, first) != s.set {
		// Siblings of another context (tree_data_common.c:1101-1135). Without a children hash
		// table (always under anydata/anyxml) decline to the evaluator's charged walk, which
		// compares the module and node name only and stops at the first opaque node. With one,
		// the record's lyd_compare_schema_equal compares kinds, names and modules up all the
		// parents: map sn into this context and look the mapped node up.
		if sib.ht == nil || x.n.any != nil {
			return nil, false
		}
		mapped, err := findSchemaCtx(s.s, x.set, x.n)
		if err != nil || mapped == nil || !compareSchemaEqual(mapped, s.s, true) {
			return nil, true
		}
		s.s = mapped
	}
	c := t.schemaLookup(sib, s.s) // an operation's children: the instance index, no scan
	if c == nil {
		return nil, true
	}
	if c.flags&flagDead == 0 {
		return wrap(x.set, c), true
	}
	return nil, false
}

// SchemaLookupMatch uses lyd_find_sibling_schema's table or linear callback
// when XPath has to scan a sibling set under its charged work budget.
func (x xn) SchemaLookupMatch(sn xpath.SchemaNode) bool {
	s, ok := sn.(xs)
	if !ok || x.n.schema == nil {
		return false
	}
	sib := x.n.siblingsOf()
	return schemaLookupMatch(x.n.schema, s.s, sib != nil && sib.ht != nil)
}

// OpaqueSchema is lyd_node_schema of an opaque node, for the name tests of the evaluator.
func (x xn) OpaqueSchema() (xpath.SchemaNode, bool) {
	sn := nodeSchema(x.set, x.n)
	if sn == nil {
		return nil, false
	}
	return xs{sn, x.set}, hasWhen(sn)
}

func (x xn) Name() string { return x.n.Name() }

func (x xn) Module() string {
	if m := nodeModule(x.set, x.n); m != nil {
		return m.Name
	}
	return ""
}

func (x xn) Schema() xpath.SchemaNode { return wrapSchema(x.set, x.n.schema) }

func (x xn) Value() xpath.Value {
	if x.n.opaq != nil {
		return opqVal(x.n.opaq.Value) // lyd_get_value of an opaque node: its text
	}
	if isAny(x.n.schema) {
		return xpathAnyValue{x.n}
	}
	if !x.n.isTerm() {
		return nil
	}
	return xval{x.n.value, x.n.schema}
}

// xpathAnyValue implements cast_string_recursive: stored text wins over children.
// Serialization failures are returned to the evaluator rather than hidden by Value().
type xpathAnyValue struct{ n *Node }

// xpathPrintNotice is a completed printer call whose context log is non-empty. The XPath
// evaluator forwards each item to its emitting context without changing the cast's return status.
type xpathPrintNotice struct {
	diags []printDiagnostic
	err   *ValidationError
}

func (e *xpathPrintNotice) Error() string                            { return e.err.Error() }
func (e *xpathPrintNotice) Unwrap() error                            { return e.err }
func (*xpathPrintNotice) XPathDiagnosticOnly()                       {}
func (e *xpathPrintNotice) xpathPrintDiagnostics() []printDiagnostic { return e.diags }

// xpathPrintFailure preserves the context of every diagnostic from a printer that failed.
// Unlike xpathPrintNotice it remains a fatal XPath evaluation error.
type xpathPrintFailure struct {
	diags []printDiagnostic
	err   error
}

func (e *xpathPrintFailure) Error() string                            { return e.err.Error() }
func (e *xpathPrintFailure) Unwrap() error                            { return e.err }
func (e *xpathPrintFailure) xpathPrintDiagnostics() []printDiagnostic { return e.diags }

// xpathPrintResourceFailure preserves the context of a native printer resource failure.
type xpathPrintResourceFailure struct {
	diags []printDiagnostic
	err   error
}

func (e *xpathPrintResourceFailure) Error() string                            { return e.err.Error() }
func (e *xpathPrintResourceFailure) Unwrap() error                            { return e.err }
func (e *xpathPrintResourceFailure) xpathPrintDiagnostics() []printDiagnostic { return e.diags }

func (v xpathAnyValue) String() string {
	s, _ := v.StringWithBudget(context.Background(), nil, nil)
	return s
}

func (v xpathAnyValue) StringWithBudget(ctx context.Context, step, byteStep func() error) (string, error) {
	if err := poll(ctx); err != nil {
		return "", err
	}
	if v.n.any != nil && v.n.any.text != nil {
		if err := chargeXPathBytes(len(*v.n.any.text), byteStep); err != nil {
			return "", err
		}
		return *v.n.any.text, nil
	}
	if v.n.any == nil || v.n.any.children.len() == 0 {
		return "", nil
	}
	// cast_string_recursive calls lyd_print_all on the any node's child forest.
	// lyd_print_all climbs to the top-level forest and prints every sibling.
	root := v.n
	for root.parent != nil {
		if step != nil {
			if err := step(); err != nil {
				return "", err
			}
		}
		root = root.parent
	}
	sib := root.siblingsOf()
	var nodes []*Node
	if sib == nil {
		if step != nil {
			if err := step(); err != nil {
				return "", err
			}
		}
		nodes = []*Node{root} // detached top-level node is its whole forest
	} else {
		for n := range sib.all() {
			if step != nil {
				if err := step(); err != nil {
					return "", err
				}
			}
			nodes = append(nodes, n)
		}
	}
	var b bytes.Buffer
	diags, err := printDataResult(&b, ctxOf(root), printConfig{step: step, byteStep: byteStep}, nodes, 0, true, printXML)
	if err != nil {
		if errors.Is(err, yang.ErrBudget) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", &xpathPrintResourceFailure{diags: diags, err: err}
		}
		return "", &xpathPrintFailure{diags: diags, err: err}
	}
	if len(diags) != 0 {
		flat := make([]yang.Diagnostic, len(diags))
		for i := range diags {
			flat[i] = diags[i].diag
		}
		return b.String(), &xpathPrintNotice{diags: diags,
			err: &ValidationError{rc: flat[0].Err, Diags: flat}}
	}
	return b.String(), nil
}

func chargeXPathBytes(n int, step func() error) error {
	if step != nil {
		for i := 0; i < (n+63)/64; i++ {
			if err := step(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (xpathAnyValue) Identity() (xpath.Ident, bool) { return xpath.Ident{}, false }
func (xpathAnyValue) Enum() (int, bool)             { return 0, false }
func (xpathAnyValue) Bits() ([]string, bool)        { return nil, false }

// Meta is the node's metadata (lyd_node.meta) for the attribute axis and lang().
func (x xn) Meta() []xpath.Meta {
	if len(x.n.meta) == 0 {
		return nil
	}
	out := make([]xpath.Meta, len(x.n.meta))
	for i, m := range x.n.meta {
		out[i] = xpath.Meta{Module: m.mod.Name, Namespace: m.mod.Namespace, Name: m.name, Value: xval{m.value, nil}}
	}
	return out
}

// opqVal is the value of an opaque node: text only, no typed value behind it.
type opqVal string

func (v opqVal) String() string              { return string(v) }
func (opqVal) Identity() (xpath.Ident, bool) { return xpath.Ident{}, false }
func (opqVal) Enum() (int, bool)             { return 0, false }
func (opqVal) Bits() ([]string, bool)        { return nil, false }

// When is the when state the evaluator reads (xpath.c moveto checks): false when flagged
// WhenFalse; unresolved when the node or a choice/case ancestor has a when and the node was not
// evaluated true yet; else true.
func (x xn) When() xpath.WhenState {
	switch {
	case x.n.flags&(FlagWhenFalse|flagDead) != 0: // a node deleted by the running when pass too
		return xpath.WhenFalse
	case x.n.schema != nil && hasWhen(x.n.schema) && x.n.flags&FlagWhenTrue == 0:
		return xpath.WhenUnresolved
	}
	return xpath.WhenTrue
}

// xval adapts a stored value to xpath.Value; s is the term node's schema node, nil for metadata.
type xval struct {
	v types.Value
	s *schema.Node
}

func (v xval) String() string { return v.v.Canonical() }

// selected is the stored value of the selected union member, or the value itself.
func (v xval) selected() types.Value {
	if u := v.v.Union(); u != nil {
		m, _ := u.Member()
		return m
	}
	return v.v
}

func (v xval) Identity() (xpath.Ident, bool) {
	s := v.selected()
	if s.Type() == nil || s.Type().Base != schema.IdentityRef || s.Ident() == nil {
		return xpath.Ident{}, false
	}
	m := s.Ident().Module
	return xpath.Ident{Module: m.Name, Name: s.Ident().Name, Revision: m.Revision, Exact: true}, true
}

// Enum: libyang checks the leaf's schema type, so an enum union member does not count.
func (v xval) Enum() (int, bool) {
	if v.s == nil || v.s.Type == nil || v.s.Type.Base != schema.Enumeration || v.v.Enum() == nil {
		return 0, false
	}
	return int(v.v.Enum().Value), true
}

func (v xval) Bits() ([]string, bool) {
	s := v.selected()
	if s.Type() == nil || s.Type().Base != schema.Bits {
		return nil, false
	}
	var names []string
	for _, b := range s.Bits() {
		names = append(names, b.Name)
	}
	return names, true
}

// xs adapts a compiled schema node to xpath.SchemaNode (a comparable value).
type xs struct {
	s   *schema.Node
	set *schema.Set
}

func wrapSchema(set *schema.Set, s *schema.Node) xpath.SchemaNode {
	if s == nil {
		return nil
	}
	return xs{s, set}
}

// xpathProbeError retains a type plugin's complete diagnostic chain until the
// query/validation logger can place it at the probe key's schema location.
type xpathProbeError struct {
	schema *schema.Node
	diag   *types.Diag
}

func (e *xpathProbeError) Error() string { return e.diag.Msg }

// xprobe is lyd_create_list's fresh typed instance and its secondary index key.
// It is private to one evaluation: concurrent reads never mutate the real tree.
type xprobe struct {
	node   *Node
	set    *schema.Set // the context the instance was created in
	key    idxKey
	charge func(int64) error
	bytes  int64
}

// PrepareInstance follows xpath.c:6343-6345 before the parent loop. Re-storing
// canonical predicates is observable: a second store may fail or choose another
// union member. Never turn that error into a successful empty result.
func (x xs) PrepareInstance(vals []string, charge func(int64) error) (xpath.InstanceProbe, error) {
	p := &xprobe{node: newInner(x.s), set: x.set, charge: charge}
	keys := x.s.Keys
	if x.s.Kind == schema.LeafList {
		keys = []*schema.Node{x.s}
	}
	// lyd_find_sibling_schema builds a schema-only lookup. Keep the probe so a
	// cross-context hash miss can charge lyd_find_sibling_opaq_next, but do not
	// try to materialize absent list keys or a leaf-list value.
	if vals == nil {
		keys = nil
	}
	for i, key := range keys {
		if err := p.work(1 + int64(len(vals[i]))); err != nil {
			return nil, err
		}
		v, d := types.StoreOnly(key.Type, vals[i], types.FormatJSON, types.HintData,
			types.ModuleNames{Set: x.set}, key)
		if d != nil {
			return nil, &xpathProbeError{schema: key, diag: d}
		}
		if err := p.work(int64(types.HashLYBWorkBound(v))); err != nil { //nolint:gosec // bounded by the LYB representation size
			return nil, err
		}
		p.bytes += 1 + int64(len(v.Canonical()))
		if x.s.Kind == schema.LeafList {
			p.node = newTerm(key, v)
		} else {
			child := newTerm(key, v)
			child.parent = p.node
			if err := p.node.kids.appendNode(child); err != nil {
				return nil, err
			}
		}
	}
	if x.s.Kind == schema.List {
		resetListKey(nil, p.node)
	}
	var work workCounter
	p.key, _ = hashOfWork(p.node, &work)
	if work.hashErr != nil {
		return nil, work.hashErr
	}
	if err := p.work(work.Load() + int64(work.hashUsed)); err != nil { //nolint:gosec // capped by defaultMaxHashWork
		return nil, err
	}
	return p, nil
}

// toCtx is lyd_find_sibling_first's lyd_dup_single_to_ctx of the instance into the
// siblings' context (stored hashes as a duplicate keeps them); nil when it fails there.
func (p *xprobe) toCtx(set *schema.Set) *xprobe {
	src := *p.node
	src.payloadSet = p.set // the probe is in no tree: name its context for the copy
	d, err := newTree(set).dupR(&src, nil, false, insertDefault, dupRecursive)
	if err != nil {
		return nil
	}
	q := &xprobe{node: d, set: set, charge: p.charge, bytes: p.bytes}
	var work workCounter
	q.key, _ = hashOfWork(d, &work)
	if work.hashErr != nil {
		return nil
	}
	return q
}

func (p *xprobe) work(n int64) error {
	if p.charge == nil {
		return nil
	}
	return p.charge(n)
}

func (p *xprobe) Match(node xpath.Node) (bool, error) {
	n, ok := node.(xn)
	if !ok || n.n.schema != p.node.schema || n.n.hash != p.node.hash {
		return false, nil
	}
	if err := p.work(1 + p.bytes); err != nil {
		return false, err
	}
	// Shared with Find/NewPath: first key-count actual children, including
	// each child's schema and stored hash (tree_data.c:1769-1782).
	var work workCounter
	return htValEqual(&Tree{}, &work, n.n, p.node)
}

var xkinds = map[schema.Kind]xpath.Kind{
	schema.Container: xpath.KindContainer, schema.List: xpath.KindList, schema.Leaf: xpath.KindLeaf,
	schema.LeafList: xpath.KindLeafList, schema.AnyData: xpath.KindAnydata, schema.AnyXML: xpath.KindAnyxml,
	schema.RPC: xpath.KindRPC, schema.Action: xpath.KindAction, schema.Notification: xpath.KindNotif,
	schema.Choice: xpath.KindChoice, schema.Case: xpath.KindCase, schema.Input: xpath.KindInput,
	schema.Output: xpath.KindOutput,
}

func (x xs) Kind() xpath.Kind         { return xkinds[x.s.Kind] }
func (x xs) Name() string             { return x.s.Name }
func (x xs) Module() string           { return x.s.Module.Name }
func (x xs) Config() bool             { return !configR(x.s) }
func (x xs) Namespace() string        { return x.s.Module.Namespace }
func (x xs) Path() string             { return x.s.LogPath() }
func (x xs) Parent() xpath.SchemaNode { return wrapSchema(x.set, x.s.Parent) }

// SameContext and Implements make xs a context-aware schema node for the evaluator.
func (x xs) SameContext(o xpath.SchemaNode) bool {
	y, ok := o.(xs)
	return ok && y.set == x.set
}

func (x xs) Implements(module string) bool { return x.set == nil || x.set.Implemented(module) != nil }

func (x xs) Keys() []string {
	var k []string
	for _, n := range x.s.Keys {
		k = append(k, n.Name)
	}
	return k
}

func (x xs) wrapAll(l []*schema.Node) []xpath.SchemaNode {
	out := make([]xpath.SchemaNode, len(l))
	for i, n := range l {
		out[i] = xs{n, x.set}
	}
	return out
}

func (x xs) Children() []xpath.SchemaNode      { return x.wrapAll(x.s.Children) }
func (x xs) Actions() []xpath.SchemaNode       { return x.wrapAll(x.s.Actions) }
func (x xs) Notifications() []xpath.SchemaNode { return x.wrapAll(x.s.Notifs) }

// Child is lys_find_child through choice and case; an operation's child from its input or its
// output, nil when both have one (eval_name_test_with_predicate_get_scnode: a name in both may be
// either, so the evaluator scans by name instead of looking the instance up).
func (x xs) Child(module, name string) xpath.SchemaNode {
	mod := x.set.Implemented(module)
	if mod == nil {
		return nil
	}
	c := schema.FindChild(x.s, nil, mod, name, 0)
	if x.s.Kind == schema.RPC || x.s.Kind == schema.Action {
		out := schema.FindChild(x.s, nil, mod, name, schema.GetNextOutput)
		switch {
		case c != nil && out != nil:
			// nil as libyang's conflict: eval_name_test_with_predicate_get_scnode (xpath.c
			// L8246-8258) sets scnode NULL and leaves *found untouched, it does not return
			// LY_ENOT, so eval_name_test_with_predicate (L8384-8399) goes on with the next
			// context node and a schema node found there decides the step: (/r3 | /r3/c)/v
			// returns only c/v (oracle: ops/xpath-json-reply-shared-name-union,
			// TestXPathOracleOps). Only when no context node gives one is the step matched
			// by name.
			return nil
		case out != nil:
			c = out
		}
	}
	if c == nil {
		return nil
	}
	return xs{c, x.set}
}

// Canonical is set_comp_canonize with the schema node's type: a compared string, boolean or
// enumeration based value needs nothing, whatever its type plugin (libyang skips every "ly2"
// plugin, which all built-in ones are, of these base types: xpath1.0, ipv4-address, …); a hashed
// key value (pc an xpath.KeyValue) is stored with its type, plugin normalization included. An
// invalid value is left as it is. The value's prefixes are the
// expression's: module names in a JSON expression (a query, pc.Default() ""), the module's own and
// import prefixes in a schema one (must, when, leafref path: LY_VALUE_SCHEMA_RESOLVED, an
// unprefixed name in the expression's module).
func (x xs) Canonical(lex string, pc xpath.NamespaceCtx) (string, bool) {
	t := x.s.Type
	if t == nil {
		return "", false
	}
	// set_comp_canonize checks the stored value's realtype (a leafref's target type), not the
	// declared one (src/xpath.c: "type = ((struct lyd_node_term *)xp_node->node)->value.realtype")
	rt := t
	if t.Base == schema.Leafref && t.Realtype != nil {
		rt = t.Realtype
	}
	if _, key := pc.(xpath.KeyValue); !key && (rt.Base == schema.String || rt.Base == schema.Bool || rt.Base == schema.Enumeration) {
		return "", false
	}
	f, p := valueFormat(x.set, pc)
	v, d := types.Store(t, lex, f, types.HintData, p, x.s)
	if d != nil {
		return "", false
	}
	return v.Canonical(), true
}

// valueFormat is the format and prefixes of a value in an expression with prefix context pc
// (set->format, set->prefix_data): JSON module names, XML namespaces (LY_VALUE_XML, an unprefixed
// value in the default namespace), or a module's prefixes (schema formats).
func valueFormat(set *schema.Set, pc xpath.NamespaceCtx) (types.Format, types.PrefixCtx) {
	if _, xml := pc.(xpath.PrefixedOnly); xml {
		return types.FormatXML, nsPrefixes{set, pc}
	}
	if _, schemaText := pc.(schemaNS); schemaText { // LY_VALUE_SCHEMA: with the status checks
		return types.FormatSchema, nsPrefixes{set, pc}
	}
	if pc != nil && pc.Default() != "" {
		return types.FormatSchemaResolved, nsPrefixes{set, pc}
	}
	return types.FormatJSON, types.ModuleNames{Set: set}
}

func (x xs) CheckValue(lex string, pc xpath.NamespaceCtx) (string, bool) {
	f := types.FormatJSON
	if _, xml := pc.(xpath.PrefixedOnly); xml {
		f = types.FormatXML
	}
	if _, d := types.Store(x.s.Type, lex, f, types.HintData, nsPrefixes{x.set, pc}, x.s); d != nil {
		return d.Msg, false
	}
	return "", true
}

// LeafrefTarget is the target of a leaf whose own type is a leafref. A snapshot drops the
// compiled path because it names the compiler's nodes, so resolve it lazily against the copy.
func (x xs) LeafrefTarget() xpath.SchemaNode {
	t := x.s.Type
	if t == nil || t.Base != schema.Leafref {
		return nil
	}
	if p, ok := t.PathCompiled.(types.Path); ok && len(p) > 0 {
		return xs{p[len(p)-1].Node, x.set}
	}
	return wrapSchema(x.set, snap.LeafrefTarget(x.s, t))
}

func (x xs) Type() xpath.SchemaType {
	if x.s.Type == nil {
		return nil
	}
	return xt{x.s.Type}
}

// xt adapts a compiled type to xpath.SchemaType.
type xt struct{ t *schema.Type }

var xbases = map[schema.BaseType]xpath.BaseType{
	schema.Binary: xpath.TypeBinary, schema.Uint8: xpath.TypeUint8, schema.Uint16: xpath.TypeUint16,
	schema.Uint32: xpath.TypeUint32, schema.Uint64: xpath.TypeUint64, schema.String: xpath.TypeString,
	schema.Bits: xpath.TypeBits, schema.Bool: xpath.TypeBool, schema.Dec64: xpath.TypeDec64,
	schema.Empty: xpath.TypeEmpty, schema.Enumeration: xpath.TypeEnum, schema.IdentityRef: xpath.TypeIdent,
	schema.InstanceID: xpath.TypeInst, schema.Leafref: xpath.TypeLeafref, schema.Union: xpath.TypeUnion,
	schema.Int8: xpath.TypeInt8, schema.Int16: xpath.TypeInt16, schema.Int32: xpath.TypeInt32,
	schema.Int64: xpath.TypeInt64,
}

func (x xt) Base() xpath.BaseType { return xbases[x.t.Base] }

func (x xt) Union() []xpath.SchemaType {
	var out []xpath.SchemaType
	for _, m := range x.t.Union {
		out = append(out, xt{m})
	}
	return out
}

func (x xt) Realtype() xpath.SchemaType {
	if x.t.Realtype == nil {
		return nil
	}
	return xt{x.t.Realtype}
}

// valuePrefixes is implemented by the namespace contexts whose value prefixes resolve differently
// from node names: ly_resolve_prefix gives a value any module (an import-only one, the exact
// imported revision), where a node test needs an implemented one (moveto_resolve_module).
type valuePrefixes interface {
	valueModule(prefix string) *schema.Module
}

// nsPrefixes turns an expression's xpath.NamespaceCtx into the types.PrefixCtx of a stored value.
type nsPrefixes struct {
	set *schema.Set
	ns  xpath.NamespaceCtx
}

func (p nsPrefixes) Resolve(prefix string) *schema.Module {
	if p.ns == nil {
		return nil
	}
	if v, ok := p.ns.(valuePrefixes); ok { // the query formats: import-only modules count for values
		return v.valueModule(prefix)
	}
	name, ok := p.ns.Resolve(prefix) // "": the expression's own module where it has one
	if prefix == "" && !ok {
		name, ok = p.ns.Default(), true
	}
	if !ok {
		return nil
	}
	return p.set.Module(name, "")
}

// info adapts the schema set to xpath.SchemaInfo.
type info struct{ set *schema.Set }

func (i info) ident(id xpath.Ident) *schema.Identity {
	if id.Exact {
		for m := range i.set.All(id.Module) {
			if m.Revision == id.Revision {
				return m.Identity(id.Name)
			}
		}
		return nil
	}
	for m := range i.set.All(id.Module) {
		if d := m.Identity(id.Name); d != nil {
			return d
		}
	}
	return nil
}

func (i info) HasIdentity(id xpath.Ident) bool { return i.ident(id) != nil }

func (i info) IsDerived(base, id xpath.Ident) bool {
	b, d := i.ident(base), i.ident(id)
	return b != nil && d != nil && types.IsDerived(b, d)
}

// Contains reports whether sn belongs to this context (lysc_node.module->ctx == set->ctx).
func (i info) Contains(sn xpath.SchemaNode) bool {
	x, ok := sn.(xs)
	return !ok || x.set == i.set
}

func (i info) TopLevel(module, name string) []xpath.SchemaNode {
	var out []xpath.SchemaNode
	for _, m := range i.set.Modules {
		if !m.Implemented || module != "" && m.Name != module {
			continue
		}
		if n := schema.FindChild(nil, m.Top, m, name, 0); n != nil {
			out = append(out, xs{n, i.set})
		}
	}
	return out
}

func (i info) Modules() []string {
	var out []string
	for _, m := range i.set.Modules {
		if m.Implemented {
			out = append(out, m.Name)
		}
	}
	return out
}

func (i info) ModuleNodes(module string) (data, rpcs, notifs []xpath.SchemaNode) {
	m := i.set.Implemented(module)
	if m == nil {
		return nil, nil, nil
	}
	for _, n := range m.Top {
		switch n.Kind {
		case schema.RPC:
			rpcs = append(rpcs, xs{n, i.set})
		case schema.Notification:
			notifs = append(notifs, xs{n, i.set})
		default:
			data = append(data, xs{n, i.set})
		}
	}
	return data, rpcs, notifs
}

// rootType is lyxp_get_root_type for a data evaluation with LYXP_SCHEMA: inside an operation
// everything, else config-only when the context node is config (or the document root).
func rootType(ctx *Node) xpath.RootKind {
	if ctx == nil || ctx.schema == nil {
		return xpath.RootConfig
	}
	for op := ctx.schema; op != nil; op = op.Parent {
		switch op.Kind {
		case schema.RPC, schema.Action, schema.Notification:
			return xpath.RootAll
		}
	}
	if ctx.schema.Config {
		return xpath.RootConfig
	}
	return xpath.RootAll
}
