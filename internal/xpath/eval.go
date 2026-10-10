// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (BSD-3-Clause, © CESNET).

package xpath

import (
	"context"
	"errors"
	"slices"
	"strings"
)

type itemType uint8

const (
	itElem itemType = iota // LYXP_NODE_ELEM
	itText                 // LYXP_NODE_TEXT: the text of a leaf / leaf-list (same Node)
	itRoot                 // LYXP_NODE_ROOT / LYXP_NODE_ROOT_CONFIG
	itMeta                 // LYXP_NODE_META: metadata m of the element n
)

type item struct {
	n Node // nil for itRoot
	t itemType
	m int // itMeta: index in n's Meta()
	// pos is lyxp_set_node.pos: the element whose document position libyang stored for the item
	// (nil: none yet, set_assign_pos takes the item's own). It goes stale in attr.
	pos Node
}

// meta is the metadata of an itMeta item.
func (it item) meta() Meta { return metaOf(it.n)[it.m] }

func metaOf(n Node) []Meta {
	if m, ok := n.(MetaNode); ok {
		return m.Meta()
	}
	return nil
}

type valType uint8

const (
	vNodes valType = iota
	vBool
	vNum
	vStr
)

// value is struct lyxp_set.
type value struct {
	t         valType
	nodes     []item
	b         bool
	f         ld
	s         string
	pos, size int // context position and size, set inside predicates
	// nonChild is lyxp_set.non_child_axis: a step on a non-child axis matched nodes of this set or
	// of a set it was derived from (set_init copies it, only moveto_root clears it), so moveto
	// sorts its result even on the child and self axes.
	nonChild bool
}

var typeNames = [...]string{"node set", "boolean", "number", "string"} // print_set_type

func boolV(b bool) value  { return value{t: vBool, b: b} }
func numV(f ld) value     { return value{t: vNum, f: f} }
func intV(i int) value    { return numV(ldInt(int64(i))) }
func strV(s string) value { return value{t: vStr, s: s} }

// nodesV builds a node set; like set_insert_node, a non-empty one has context position and size 1.
func nodesV(n []item) value {
	if len(n) == 0 {
		return value{t: vNodes}
	}
	return value{t: vNodes, nodes: n, pos: 1, size: 1}
}

type evaluator struct {
	e       *Expr
	ns      NamespaceCtx
	ec      *EvalContext
	ctx     context.Context
	cur     item             // current()
	op      SchemaNode       // set->context_op: the RPC/action/notification of current()
	steps   int              // remaining budget
	budget  int              // initial budget
	err     error            // sticky budget / cancellation error
	sib     map[Node]sibList // per parent (nil: top level), its children; built lazily
	keys    map[Node][]int   // sibling indexes from the top level down to the node
	nums    map[string]ld    // parsed long number texts
	parses  int              // long number texts actually parsed (tests)
	vars    int              // variable references being evaluated, nested
	varASTs map[int]varAST   // parsed variable values, by index in ec.Vars
	intErrs int              // Result.InternalErrors
}

func newEvaluator(e *Expr, ec *EvalContext) *evaluator {
	ev := &evaluator{e: e, ns: e.ns, ec: ec, ctx: ec.Ctx, steps: ec.MaxSteps}
	if ev.ctx == nil {
		ev.ctx = context.Background()
	}
	if ev.steps <= 0 {
		ev.steps = DefaultMaxSteps
	}
	ev.budget = ev.steps
	switch {
	case ec.Current != nil:
		ev.cur = item{n: ec.Current, t: itElem}
	case ec.Node != nil:
		ev.cur = item{n: ec.Node, t: itElem}
	default:
		ev.cur = item{t: itRoot}
	}
	for n := ev.cur.n; n != nil && ev.op == nil; n = n.Parent() {
		if sn := n.Schema(); sn != nil && isOp(sn) {
			ev.op = sn
		}
	}
	return ev
}

func isOp(sn SchemaNode) bool {
	return sn.Kind() == KindRPC || sn.Kind() == KindAction || sn.Kind() == KindNotif
}

// start is the initial set of lyxp_eval: the context node or the root.
func (ev *evaluator) start() value {
	if ev.ec.Node == nil {
		return nodesV([]item{{t: itRoot}})
	}
	return nodesV([]item{{n: ev.ec.Node, t: itElem}})
}

// tick charges one step of the budget and polls cancellation; the error sticks.
func (ev *evaluator) tick() error {
	if ev.err != nil {
		return ev.err
	}
	if ev.steps--; ev.steps < 0 {
		ev.err = ErrBudget
	} else if ev.steps&1023 == 0 {
		ev.err = ev.ctx.Err()
	}
	return ev.err
}

// eval is eval_expr_select over the AST; ctx is the context set (never modified).
func (ev *evaluator) eval(a ast, ctx value) (value, error) {
	if err := ev.tick(); err != nil {
		return value{}, err
	}
	v, err := ev.eval1(a, ctx)
	if err == nil {
		err = ev.err // set by work that cannot return errors (casts, sorting)
	}
	return v, err
}

// eval1 keeps lyxp_set.non_child_axis as libyang does: literals, numbers and function results are
// written into the context set (its flag), a unary minus into its operand's set, a chain into its
// first operand's set (a union of an empty first operand takes the second's).
func (ev *evaluator) eval1(a ast, ctx value) (value, error) {
	switch a := a.(type) {
	case chainExpr:
		return ev.chain(a, ctx)
	case negExpr:
		v, err := ev.eval(a.x, ctx)
		r := numV(ldNeg(ev.toNum(v)))
		r.nonChild = v.nonChild
		return r, err
	case litExpr:
		r := strV(string(a))
		r.nonChild = ctx.nonChild
		return r, nil
	case numExpr:
		r := numV(a.v)
		r.nonChild = ctx.nonChild
		return r, nil
	case varExpr:
		root, err := ev.variable(string(a))
		if err != nil {
			return value{}, err
		}
		ev.vars++
		defer func() { ev.vars-- }()
		return ev.eval(root, ctx)
	case callExpr:
		args := make([]value, len(a.args))
		for i, x := range a.args {
			var err error
			if args[i], err = ev.eval(x, ctx); err != nil {
				return value{}, err
			}
		}
		r, err := funcImpls[a.name](ev, args, ctx)
		r.nonChild = ctx.nonChild
		return r, err
	case pathExpr:
		return ev.path(a, ctx)
	}
	panic("xpath: unknown AST node")
}

// variable is the lookup and parse of eval_variable_reference: the value of the variable,
// parsed. libyang recurses through a variable whose value refers back to it until the stack
// overflows; the port stops at maxDepth nested references (D-0063).
func (ev *evaluator) variable(name string) (ast, error) {
	i, ok := FindVar(ev.ec.Vars, name)
	if !ok {
		return nil, &Error{Err: "LY_ENOTFOUND", Msg: "Variable \"" + name + "\" not defined."}
	}
	if ev.vars >= maxDepth {
		return nil, xpErr("The maximum nesting of expressions has been exceeded.")
	}
	if ev.varASTs == nil {
		ev.varASTs = map[int]varAST{}
	}
	c, ok := ev.varASTs[i]
	if !ok {
		c.root, _, c.err = parse(ev.ec.Vars[i].Value)
		ev.varASTs[i] = c
	}
	return c.root, c.err
}

// varAST is a variable's parsed value, or why it does not parse.
type varAST struct {
	root ast
	err  error
}

// skip is evaluation with LYXP_SKIP_EXPR, which libyang runs over the operands lazy and/or do
// not need and over the predicates of an empty node set: nothing is evaluated, but every
// variable reference is still looked up and its value parsed and skipped in turn, so an
// undefined variable fails even there.
func (ev *evaluator) skip(a ast) error {
	if err := ev.tick(); err != nil {
		return err
	}
	switch a := a.(type) {
	case chainExpr:
		for _, x := range a.args {
			if err := ev.skip(x); err != nil {
				return err
			}
		}
	case negExpr:
		return ev.skip(a.x)
	case varExpr:
		root, err := ev.variable(string(a))
		if err != nil {
			return err
		}
		ev.vars++
		defer func() { ev.vars-- }()
		return ev.skip(root)
	case callExpr:
		for _, x := range a.args {
			if err := ev.skip(x); err != nil {
				return err
			}
		}
	case pathExpr:
		if a.prim != nil {
			if err := ev.skip(a.prim); err != nil {
				return err
			}
		}
		for _, x := range a.preds {
			if err := ev.skip(x); err != nil {
				return err
			}
		}
		for _, s := range a.steps {
			for _, x := range s.preds {
				if err := ev.skip(x); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// chain is eval_or_expr … eval_union_expr: every operand is evaluated against
// the original context, left to right, without recursion per operator.
func (ev *evaluator) chain(a chainExpr, ctx value) (value, error) {
	acc, err := ev.eval(a.args[0], ctx)
	if err != nil {
		return value{}, err
	}
	logic := a.ops[0] == "or" || a.ops[0] == "and"
	nonChild := acc.nonChild // the first operand's set holds the result
	if logic {
		acc = boolV(ev.toBool(acc))
	}
	for i, op := range a.ops {
		if logic && acc.b == (op == "or") { // lazy evaluation
			for _, rest := range a.args[i+1:] {
				if err := ev.skip(rest); err != nil {
					return value{}, err
				}
			}
			break
		}
		r, err := ev.eval(a.args[i+1], ctx)
		if err != nil {
			return value{}, err
		}
		switch op {
		case "or", "and":
			acc = boolV(ev.toBool(r))
		case "|": // moveto_union
			if acc.t != vNodes || r.t != vNodes {
				return value{}, xpErr("Cannot apply XPath operation union on %s and %s.", typeNames[acc.t], typeNames[r.t])
			}
			if len(acc.nodes) == 0 && len(r.nodes) > 0 {
				nonChild = r.nonChild // set2 copied into the empty set1, flag included
			}
			acc = nodesV(ev.union(acc.nodes, r.nodes))
		case "=", "!=", "<", "<=", ">", ">=":
			acc = boolV(ev.compare(acc, r, op))
		default:
			acc = numV(ldOp(op, ev.toNum(acc), ev.toNum(r)))
		}
	}
	acc.nonChild = nonChild
	return acc, nil
}

// path is eval_path_expr / eval_absolute_location_path / eval_relative_location_path.
func (ev *evaluator) path(a pathExpr, ctx value) (value, error) {
	set := ctx
	var err error
	switch {
	case a.abs: // moveto_root
		set = nodesV([]item{{t: itRoot}})
	case a.prim != nil:
		if set, err = ev.eval(a.prim, ctx); err != nil {
			return value{}, err
		}
		if set, err = ev.predicates(set, a.preds, "child"); err != nil {
			return value{}, err
		}
	}
	for _, s := range a.steps {
		if set, err = ev.step(set, s); err != nil {
			return value{}, err
		}
	}
	return set, nil
}

func (ev *evaluator) step(set value, s step) (value, error) {
	v, err := ev.step1(set, s)
	v.nonChild = v.nonChild || set.nonChild
	return v, err
}

func (ev *evaluator) step1(set value, s step) (value, error) {
	var err error
	preds := s.preds // what hashChild's lookup consumed is not evaluated again
	switch s.test {
	case tDot, tDDot:
		if set.t != vNodes {
			return value{}, xpErr("Cannot apply XPath operation path operator on %s.", typeNames[set.t])
		}
		if s.allDesc {
			if set, err = ev.moveto(set, "descendant-or-self", nameTest{any: true}); err != nil {
				return value{}, err
			}
		}
		axis := "self"
		if s.test == tDDot {
			axis = "parent"
		}
		return ev.moveto(set, axis, nameTest{any: true})
	case tNodeType: // libyang ignores a preceding '//' here
		if s.name == "node" { // xpath_pi_node
			if set.t != vNodes {
				set = nodesV(nil)
			} else if set, err = ev.moveto(set, s.axis, nameTest{any: true}); err != nil {
				return value{}, err
			}
		} else if set, err = ev.text(set, s.axis); err != nil { // text(); libyang evaluates comment() as text()
			return value{}, err
		}
	default: // eval_name_test_with_predicate
		nt, err := ev.resolveName(s.name)
		if err != nil {
			return value{}, err
		}
		if s.allDesc && s.axis != "child" && s.axis != "attribute" { // '//' = '/descendant-or-self::node()/'
			if set.t != vNodes {
				set = nodesV(nil)
			} else if set, err = ev.moveto(set, "descendant-or-self", nameTest{any: true}); err != nil {
				return value{}, err
			}
		}
		switch {
		case set.t != vNodes:
			return value{}, xpErr("Cannot apply XPath operation path operator on %s.", typeNames[set.t])
		case s.axis == "attribute":
			set, err = ev.attr(set, nt, s.allDesc)
		case s.allDesc && s.axis == "child":
			set, err = ev.allDescChild(set, nt)
		default:
			if sn, vals, ok := ev.schemaTarget(set, nt, s); ok {
				set, err = ev.hashChild(set, sn, nt.name, vals)
				preds = s.preds[len(vals):] // the hashed key predicates are not evaluated
			} else {
				set, err = ev.moveto(set, s.axis, nt)
			}
		}
		if err != nil {
			return value{}, err
		}
	}
	return ev.predicates(set, preds, s.axis)
}

// attr is moveto_attr ('@' NameTest) and, for allDesc, moveto_attr_alldesc ('//@' NameTest:
// the context nodes and all their descendants): every element is replaced by its metadata that
// match nt, in metadata order, and every other item is removed.
//
// libyang replaces in place, and after the first match its index already points past it, so the
// second match is inserted one place too far, after the next item, which is then never looked at:
// it stays in the set as it is (an element is not replaced by its metadata), and the last match is
// removed as a non-element when the loop goes on there. After the last item the insert index is
// past the end: libyang logs an internal error (Result.InternalErrors) and appends. The result is
// built here in one pass instead of by inserts and removals.
func (ev *evaluator) attr(set value, nt nameTest, allDesc bool) (value, error) {
	nodes := set.nodes
	if allDesc {
		desc, err := ev.allDescChild(set, nameTest{})
		if err != nil {
			return value{}, err
		}
		nodes = ev.union(nodes, desc.nodes)
	}
	var out []item
	for p := 0; p < len(nodes); p++ {
		if err := ev.tick(); err != nil {
			return value{}, err
		}
		if nodes[p].t != itElem {
			continue
		}
		var ms []item
		for j, m := range metaOf(nodes[p].n) {
			if err := ev.tick(); err != nil {
				return value{}, err
			}
			if (nt.mod == "" || m.Module == nt.mod) && (nt.name == "" || m.Name == nt.name) {
				ms = append(ms, item{n: nodes[p].n, t: itMeta, m: j, pos: nodes[p].pos})
			}
		}
		switch {
		case len(ms) <= 1:
			out = append(out, ms...)
		case p+1 < len(nodes):
			for i := range ms[1:] { // inserted with the pos of the item after the element
				ms[1+i].pos = nodes[p+1].pos
			}
			out = append(append(out, ms[0], nodes[p+1]), ms[1:len(ms)-1]...)
			p++
		default: // the pos libyang reads past the end is taken as the element's own
			ev.intErrs += len(ms) - 1
			out = append(out, ms...)
		}
	}
	return nodesV(out), nil
}

// nameTest selects nodes; any matches every node incl. the root.
type nameTest struct {
	any       bool
	mod, name string // "" = any module / any name
}

// resolveName is moveto_resolve_module.
func (ev *evaluator) resolveName(qname string) (nameTest, error) {
	if qname == "*" {
		return nameTest{any: true}, nil
	}
	var nt nameTest
	if pref, local, ok := strings.Cut(qname, ":"); ok {
		mod, ok := ev.ns.Resolve(pref)
		if !ok {
			return nt, xpErr("Unknown/non-implemented module \"%s\".", pref)
		}
		nt.mod, qname = mod, local
	} else {
		var err error
		if nt.mod, err = unprefixed(ev.ns, qname); err != nil {
			return nt, err
		}
	}
	if qname != "*" {
		nt.name = qname
	}
	return nt, nil
}

// schemaTarget is eval_name_test_with_predicate_get_scnode: the one schema
// node a child NameTest denotes, which libyang then looks up by hash
// (restricting an unprefixed JSON name to the context node's module); nil
// when libyang falls back to matching by name in every module.
func (ev *evaluator) schemaTarget(set value, nt nameTest, s step) (sn SchemaNode, vals []string, ok bool) {
	if nt.name == "" || s.axis != "child" {
		return nil, nil, false
	}
	var found, foundParent SchemaNode
	for _, it := range set.nodes {
		var cand SchemaNode
		if it.t == itRoot {
			if ev.ec.Schema == nil {
				return nil, nil, false // cannot search: match by name
			}
			tops := ev.ec.Schema.TopLevel(nt.mod, nt.name)
			if len(tops) > 1 {
				return nil, nil, false // matches in several modules
			}
			if len(tops) == 1 {
				cand = tops[0]
			}
		} else {
			sn := it.n.Schema()
			if sn == nil || found != nil && foundParent == sn {
				continue // opaque, or this parent was searched already
			}
			mod := nt.mod
			if mod == "" {
				mod = sn.Module() // JSON: inherit the module of the context node
			}
			cand = sn.Child(mod, nt.name)
			if found == nil {
				foundParent = sn
			}
		}
		if cand != nil && ev.ec.Root == RootConfig && !cand.Config() {
			cand = nil
		}
		if cand != nil {
			if found != nil {
				return nil, nil, false // found at different levels
			}
			found = cand
		}
	}
	if found == nil {
		return nil, nil, false
	}
	if found.Kind() == KindList || found.Kind() == KindLeafList {
		if vals, ok = ev.hashPredicates(found, s.preds, set); !ok {
			return nil, nil, false
		}
	}
	return found, vals, true
}

// hashPredicates is eval_name_test_try_compile_predicates: the key values of a list step that
// starts with "[key = value]" for every key in order, each value independent of the instance.
// Like libyang, it gives up (ok=false: the step matches by name and evaluates its predicates)
// for a leaf-list (ly_path_compile_predicate rejects the "[ll='v']" predicate it builds), when
// the first context node has no instance of the list (lyd_find_sibling_schema), when a value
// does not evaluate (a variable: libyang evaluates without them) or does not fit the key's type,
// and when it contains both quote characters. A value is only the first operand of an '='
// chain (the copied tokens lose the chain): [k = 'a' != 'zz'] looks up k = 'a'.
func (ev *evaluator) hashPredicates(sn SchemaNode, preds []ast, set value) (vals []string, ok bool) {
	keys := sn.Keys()
	if sn.Kind() != KindList || len(keys) == 0 || len(preds) < len(keys) {
		return nil, false
	}
	var inst Node // eval_name_test_try_compile_predicate_append: any instance, the first
	for i, k := range keys {
		c, ok := preds[i].(chainExpr)
		if !ok || c.ops[0] != "=" {
			return nil, false
		}
		p, ok := c.args[0].(pathExpr)
		if !ok || p.abs || p.prim != nil || len(p.steps) != 1 {
			return nil, false
		}
		st := p.steps[0]
		if st.allDesc || st.explicit || len(st.preds) > 0 || st.test != tNameTest {
			return nil, false // '[' NameTest '=' only
		}
		// eval_name_test_try_compile_predicate_key: the key's module and name
		nt, err := ev.resolveName(st.name)
		if nt.mod == "" {
			nt.mod = sn.Module() // JSON: the list's module
		}
		if err != nil || nt.name != k || nt.mod != sn.Module() {
			return nil, false
		}
		if slices.ContainsFunc(c.args[1:], hasLogOp) || !ev.atomsOK(c.args[1], sn) {
			return nil, false
		}
		if inst == nil {
			if inst = ev.firstInstance(set, sn); inst == nil {
				return nil, false
			}
		}
		if hasVar(c.args[1]) {
			return nil, false
		}
		v, err := ev.eval(c.args[1], nodesV([]item{{n: inst, t: itElem}}))
		if err != nil {
			return nil, false // a budget error stays in ev.err
		}
		str := ev.toString(v)
		if strings.Contains(str, "'") && strings.Contains(str, `"`) {
			return nil, false // the predicate it builds does not parse
		}
		ks := sn.Child(sn.Module(), k)
		if ks == nil {
			return nil, false
		}
		if _, ok := ks.CheckValue(str, jsonPrefixes{}); !ok {
			return nil, false // ly_path_compile_predicate stores the value by the key's type
		}
		if cv, ok := ks.Canonical(str, jsonPrefixes{}); ok { // ly_path_compile_predicate: LY_VALUE_JSON, no prefix data
			str = cv
		}
		vals = append(vals, str)
	}
	return vals, true
}

// firstInstance is lyd_find_sibling_schema over the children of the first context node (the
// document root: the tree): its first instance of sn, nil if there is none.
func (ev *evaluator) firstInstance(set value, sn SchemaNode) Node {
	if len(set.nodes) == 0 {
		return nil
	}
	var kids []Node
	switch it := set.nodes[0]; it.t {
	case itRoot:
		kids = ev.ec.Tree
	case itElem:
		if f, ok := it.n.(FirstLookup); ok {
			if err := ev.tick(); err != nil {
				return nil
			}
			if n, ok := f.FirstChild(sn); ok {
				return n
			}
		}
		kids = it.n.Children()
	}
	for _, c := range kids {
		if ev.tick() != nil {
			return nil
		}
		if c.Schema() == sn {
			return c
		}
	}
	return nil
}

// hasVar reports a variable reference in a.
func hasVar(a ast) bool {
	switch a := a.(type) {
	case varExpr:
		return true
	case chainExpr:
		return slices.ContainsFunc(a.args, hasVar)
	case negExpr:
		return hasVar(a.x)
	case callExpr:
		return slices.ContainsFunc(a.args, hasVar)
	case pathExpr:
		if a.prim != nil && hasVar(a.prim) || slices.ContainsFunc(a.preds, hasVar) {
			return true
		}
		for _, st := range a.steps {
			if slices.ContainsFunc(st.preds, hasVar) {
				return true
			}
		}
	}
	return false
}

// jsonPrefixes is LY_VALUE_JSON without prefix data, the format ly_path_compile_predicate stores
// a hashed key value in: prefixes are module names, unprefixed names the context node's module.
type jsonPrefixes struct{}

func (jsonPrefixes) Resolve(p string) (string, bool) { return p, true }
func (jsonPrefixes) Prefix(m string) string          { return m }
func (jsonPrefixes) Default() string                 { return "" }
func (jsonPrefixes) KeyValue()                       {}

// hasLogOp reports an 'or'/'and' token outside nested brackets, which makes
// eval_name_test_try_compile_predicates give up on a value.
func hasLogOp(a ast) bool {
	switch a := a.(type) {
	case chainExpr:
		return a.ops[0] == "or" || a.ops[0] == "and" || slices.ContainsFunc(a.args, hasLogOp)
	case negExpr:
		return hasLogOp(a.x)
	case callExpr:
		return slices.ContainsFunc(a.args, hasLogOp)
	case pathExpr:
		return a.prim != nil && hasLogOp(a.prim)
	}
	return false
}

// atomsOK is the dependency check of eval_name_test_try_compile_predicate_append:
// the key value val, atomized with the list sn as its context node (libyang
// atomizes the value's tokens, whose copied repeat info covers only the first
// operand of an '=' chain), reaches no child of sn, no descendant of sn on a
// descendant axis and no list/leaf-list other than current()'s schema node.
func (ev *evaluator) atomsOK(val ast, sn SchemaNode) bool {
	if ev.tick() != nil {
		return false
	}
	var cur SchemaNode
	if ev.cur.t == itElem {
		cur = ev.cur.n.Schema()
	}
	a := newAtomizer(ev.ns, ev.ec.Schema, cur, sn, nRoot, false, nil, ev.steps)
	set, err := a.run(ev.e.src, val)
	ev.steps = a.steps
	if errors.Is(err, ErrBudget) {
		ev.err = err
	}
	if err != nil {
		return false
	}
	for _, x := range set.n {
		if x.t != nElem || x.use < AtomNode {
			continue // the root and the context node
		}
		if x.axis == "child" && x.n.Parent() == sn {
			return false // a child of the list: certain dependency
		}
		if x.axis == "descendant" || x.axis == "descendant-or-self" {
			for p := x.n.Parent(); p != nil; p = p.Parent() {
				if p == sn {
					return false // probable dependency
				}
			}
		}
		if (x.n.Kind() == KindList || x.n.Kind() == KindLeafList) && x.n != cur {
			return false
		}
	}
	return true
}

// hashChild is moveto_node_hash_child: under each context node, the first instance of sn whose
// keys are vals (hashPredicates; any instance for a node that is not a list), or else the first
// opaque child of that name, which libyang returns without evaluating anything on it. A context
// node that is a ChildLookup (the document root: EvalContext.TreeLookup) is asked first; one
// that declines, or is none, has its children scanned.
func (ev *evaluator) hashChild(set value, sn SchemaNode, name string, vals []string) (value, error) {
	if ev.ec.Root == RootConfig && !sn.Config() || ev.op != nil && isOp(sn) && sn != ev.op {
		return nodesV(nil), nil
	}
	var keys []SchemaNode
	for _, k := range sn.Keys()[:len(vals)] {
		keys = append(keys, sn.Child(sn.Module(), k))
	}
	matches := func(c Node) bool { // lyd_find_sibling_first / lyd_find_sibling_val
		if c.Schema() != sn {
			return false
		}
		kids := c.Children()
		for i, ks := range keys {
			j := slices.IndexFunc(kids, func(k Node) bool { return k.Schema() == ks })
			if j < 0 || valueOf(kids[j]).String() != vals[i] {
				return false
			}
		}
		return true
	}
	var out []item
	for _, it := range set.nodes {
		var lookup ChildLookup
		switch it.t {
		case itRoot:
			lookup = ev.ec.TreeLookup
		case itElem:
			lookup, _ = it.n.(ChildLookup)
		}
		var hit []Node
		looked := false
		if lookup != nil {
			if err := ev.tick(); err != nil {
				return value{}, err
			}
			hit, looked = lookup.LookupChild(sn, vals)
			if len(hit) > 1 {
				hit = hit[:1]
			}
		}
		if !looked { // scan, one step per child
			var kids []Node
			switch it.t {
			case itRoot:
				kids = ev.ec.Tree
			case itElem:
				kids = it.n.Children()
			}
			for _, c := range kids {
				if err := ev.tick(); err != nil {
					return value{}, err
				}
				if matches(c) {
					hit = []Node{c}
					break
				}
			}
			if hit == nil { // lyd_find_sibling_opaq_next
				for _, c := range kids {
					if c.Schema() == nil && c.Name() == name {
						hit = []Node{c}
						break
					}
				}
			}
		}
		for _, c := range hit {
			switch c.When() {
			case WhenUnresolved:
				if !ev.ec.IgnoreWhen {
					return value{}, ErrIncomplete
				}
			case WhenFalse:
				continue // no exception for current() here, as libyang
			}
			out = append(out, item{n: c, t: itElem})
		}
	}
	return nodesV(out), nil
}

// check is moveto_node_check: match, or skip (LY_EINVAL: config-false under
// the config root, or an operation other than current()'s).
func (ev *evaluator) check(it item, nt nameTest) (match, skip bool, err error) {
	if it.t == itRoot {
		return nt.any, false, nil
	}
	if it.t != itElem {
		return false, false, nil
	}
	// lyd_node_schema: an opaque node is checked as an instance of the schema node its name
	// has, but has no when flags (an unresolved when when its schema node has one)
	sn, mod, opqWhen := it.n.Schema(), it.n.Module(), false
	if sn == nil {
		if o, ok := it.n.(OpaqueSchema); ok {
			sn, opqWhen = o.OpaqueSchema()
		}
		if sn == nil {
			return false, false, nil
		}
		mod = sn.Module()
	}
	if nt.mod != "" && mod != nt.mod {
		return false, false, nil
	}
	if ev.ec.Root == RootConfig && !sn.Config() || ev.op != nil && isOp(sn) && sn != ev.op {
		return false, true, nil
	}
	if nt.name != "" && it.n.Name() != nt.name {
		return false, false, nil
	}
	isCur := ev.cur.t == itElem && it.n == ev.cur.n
	w := it.n.When()
	if opqWhen {
		w = WhenUnresolved
	}
	switch w {
	case WhenUnresolved:
		if !ev.ec.IgnoreWhen && !isCur {
			return false, false, ErrIncomplete
		}
	case WhenFalse:
		return isCur, false, nil // a when-false node does not exist for XPath
	}
	return true, false, nil
}

// moveto is moveto_node (and xpath_pi_node for a nameTest{any: true}).
func (ev *evaluator) moveto(set value, axis string, nt nameTest) (value, error) {
	if set.t != vNodes {
		return value{}, xpErr("Cannot apply XPath operation path operator on %s.", typeNames[set.t])
	}
	var out []item
	type id struct {
		n Node
		t itemType
	}
	var seen map[id]bool // set_dup_node_check: only the non-child axes can repeat a node
	if axis != "child" && axis != "self" {
		seen = map[id]bool{}
	}
	for _, it := range set.nodes {
		err := ev.axis(it, axis, func(x item) error {
			if err := ev.tick(); err != nil {
				return err
			}
			ok, _, err := ev.check(x, nt)
			if ok && (seen == nil || !seen[id{x.n, x.t}]) {
				if seen != nil {
					seen[id{x.n, x.t}] = true
				}
				out = append(out, x)
			}
			return err
		})
		if err != nil {
			return value{}, err
		}
	}
	nonChild := set.nonChild || axis != "child" && axis != "self" && len(out) > 0
	if nonChild { // libyang sorts after the other axes and on any set that saw one (set_sort)
		out = ev.sortSet(out)
	}
	v := nodesV(out)
	v.nonChild = nonChild
	return v, nil
}

// allDescChild is moveto_node_alldesc_child ('//' NameTest).
func (ev *evaluator) allDescChild(set value, nt nameTest) (value, error) {
	set, err := ev.moveto(set, "child", nameTest{any: true})
	if err != nil {
		return value{}, err
	}
	// set_dup_node_check: a matching node that is also another item of the set is not descended
	// into (that item's own walk covers it), but it is added each time; libyang neither removes
	// these duplicates nor sorts (its set_sort is an assert, compiled out)
	count := make(map[Node]int, len(set.nodes))
	for _, it := range set.nodes {
		count[it.n]++
	}
	var out []item
	var dfs func(n, start Node) error
	dfs = func(n, start Node) error {
		if err := ev.tick(); err != nil {
			return err
		}
		ok, skip, err := ev.check(item{n: n, t: itElem}, nt)
		if err != nil || skip {
			return err
		}
		if ok {
			out = append(out, item{n: n, t: itElem})
			if c := count[n]; n == start && c > 1 || n != start && c > 0 {
				return nil
			}
		}
		// libyang descends into when-false nodes here: their descendants match
		for _, c := range n.Children() {
			if err := dfs(c, start); err != nil {
				return err
			}
		}
		return nil
	}
	for _, it := range set.nodes {
		if err := dfs(it.n, it.n); err != nil {
			return value{}, err
		}
	}
	v := nodesV(out)
	v.nonChild = set.nonChild
	return v, nil
}

// text is xpath_pi_text.
func (ev *evaluator) text(set value, axis string) (value, error) {
	if set.t != vNodes {
		return value{}, xpErr("Invalid context type %s in text().", typeNames[set.t])
	}
	if axis != "child" {
		return nodesV(nil), nil
	}
	var out []item
	for _, it := range set.nodes {
		if it.t == itElem && (it.n.Schema() == nil || isTerm(it.n)) {
			out = append(out, item{n: it.n, t: itText})
		}
	}
	return nodesV(out), nil
}

func isTerm(n Node) bool {
	sn := n.Schema()
	return sn != nil && (sn.Kind() == KindLeaf || sn.Kind() == KindLeafList)
}

func (ev *evaluator) siblings(n Node) []Node {
	if p := n.Parent(); p != nil {
		return p.Children()
	}
	return ev.ec.Tree
}

// index returns n's siblings and its index among them. Each sibling list is
// numbered once per evaluation, and only when reached (set_assign_pos without
// walking the whole tree).
func (ev *evaluator) index(n Node) ([]Node, int) {
	p := n.Parent()
	sl, ok := ev.sib[p]
	if !ok { // the sibling list is read (Children() allocates it) and numbered once per parent
		if ev.sib == nil {
			ev.sib = map[Node]sibList{}
		}
		sl.nodes = ev.siblings(n)
		sl.idx = make(map[Node]int, len(sl.nodes))
		for i, s := range sl.nodes {
			if ev.tick() != nil {
				break
			}
			sl.idx[s] = i
		}
		ev.sib[p] = sl
	}
	i, ok := sl.idx[n]
	if !ok {
		i = -1 // not under Tree
	}
	return sl.nodes, i
}

// sibList is one parent's children and the index of each, cached by index.
type sibList struct {
	nodes []Node
	idx   map[Node]int
}

// axis is moveto_axis_node_next: every node on axis from it, in any order
// (callers sort).
func (ev *evaluator) axis(it item, axis string, yield func(item) error) error {
	var subtree func(n Node) error // n and its descendants
	subtree = func(n Node) error {
		if err := yield(item{n: n, t: itElem}); err != nil {
			return err
		}
		for _, c := range n.Children() {
			if err := subtree(c); err != nil {
				return err
			}
		}
		return nil
	}
	parent := func(x item) (item, bool) {
		switch {
		case x.t == itText || x.t == itMeta:
			return item{n: x.n, t: itElem}, true
		case x.t == itElem && x.n.Parent() != nil:
			return item{n: x.n.Parent(), t: itElem}, true
		case x.t == itElem:
			return item{t: itRoot}, true
		}
		return item{}, false
	}
	switch axis {
	case "self":
		return yield(it)
	case "parent":
		if p, ok := parent(it); ok {
			return yield(p)
		}
	case "ancestor-or-self", "ancestor":
		if axis == "ancestor-or-self" {
			if err := yield(it); err != nil {
				return err
			}
		}
		for p, ok := parent(it); ok; p, ok = parent(p) {
			if err := yield(p); err != nil {
				return err
			}
		}
	case "child", "descendant", "descendant-or-self":
		if axis == "descendant-or-self" {
			if err := yield(it); err != nil {
				return err
			}
		}
		var kids []Node
		switch it.t {
		case itRoot:
			kids = ev.ec.Tree
		case itElem:
			kids = it.n.Children()
		}
		for _, c := range kids {
			var err error
			if axis == "child" {
				err = yield(item{n: c, t: itElem})
			} else {
				err = subtree(c)
			}
			if err != nil {
				return err
			}
		}
	case "following-sibling", "preceding-sibling":
		if it.t != itElem {
			return nil
		}
		sib, i := ev.index(it.n)
		for j, s := range sib {
			if j != i && (j > i) == (axis == "following-sibling") {
				if err := yield(item{n: s, t: itElem}); err != nil {
					return err
				}
			}
		}
	case "following", "preceding":
		if it.t != itElem {
			return nil
		}
		fwd := axis == "following"
		if sib, i := ev.index(it.n); fwd && i+1 >= len(sib) || !fwd && i < 1 {
			return nil // libyang: without a sibling in that direction the axis is empty
		}
		for x := it.n; x != nil; x = x.Parent() {
			sib, i := ev.index(x)
			for j, s := range sib {
				if j != i && (j > i) == fwd {
					if err := subtree(s); err != nil {
						return err
					}
				}
			}
			if !fwd && x.Parent() != nil {
				// libyang: preceding also returns the ancestors (moveto_axis_node_next_dfs_backward)
				if err := yield(item{n: x.Parent(), t: itElem}); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// predicates is eval_predicate for each predicate in turn.
func (ev *evaluator) predicates(set value, preds []ast, axis string) (value, error) {
	for _, pr := range preds {
		if set.t != vNodes {
			r, err := ev.eval(pr, set)
			if err != nil {
				return value{}, err
			}
			if !ev.toBool(r) {
				set = value{t: vNodes, nonChild: set.nonChild}
			}
			continue
		}
		if len(set.nodes) == 0 { // only_parse
			if err := ev.skip(pr); err != nil {
				return value{}, err
			}
			continue
		}
		reverse := axis == "ancestor" || axis == "ancestor-or-self" || axis == "preceding" || axis == "preceding-sibling"
		var keep []item
		for i, it := range set.nodes {
			pos := i + 1
			if reverse {
				pos = len(set.nodes) - i
			}
			r, err := ev.eval(pr, value{t: vNodes, nodes: []item{it}, pos: pos, size: len(set.nodes), nonChild: set.nonChild})
			if err != nil {
				return value{}, err
			}
			if r.t == vNum { // proximity position; libyang truncates (1.5 selects position 1)
				r = boolV(ctrunc(r.f) == int64(pos))
			}
			if ev.toBool(r) {
				keep = append(keep, it)
			}
		}
		set = value{t: vNodes, nodes: keep, pos: set.pos, size: set.size, nonChild: set.nonChild}
	}
	return set, nil
}

// key is the document-order key of an item: its sibling indexes from the top
// level (root: empty, so first; metadata, then text: right after its element).
func (ev *evaluator) key(it item) []int {
	if it.t == itRoot {
		return nil
	}
	k, ok := ev.keys[it.n]
	if !ok {
		for n := it.n; n != nil && ev.tick() == nil; n = n.Parent() {
			_, i := ev.index(n)
			k = append(k, i)
		}
		slices.Reverse(k)
		if ev.keys == nil {
			ev.keys = map[Node][]int{}
		}
		ev.keys[it.n] = k
	}
	switch it.t {
	case itText:
		return append(slices.Clip(k), -1)
	case itMeta: // after its element, before its text (set_sort_compare, get_meta_pos)
		return append(slices.Clip(k), -2, it.m)
	}
	return k
}

// union is moveto_union of two node sets: an empty set2 leaves set1 as it is, an empty set1 takes
// set2, and only two non-empty sets are merged (set_sorted_merge, which stores positions).
func (ev *evaluator) union(set1, set2 []item) []item {
	switch {
	case len(set2) == 0:
		return set1
	case len(set1) == 0:
		return set2
	}
	return ev.sortedMerge(set1, set2)
}

// sortedMerge is set_sorted_merge (moveto_union of two non-empty node sets): src merged into trg
// assuming both are in document order, which libyang does not check (its set_sort calls are in
// asserts, compiled out): runs of src items that go before the current trg item are inserted
// there, equal items are kept once. An unsorted set (a multi-match attribute step, see attr)
// stays as unsorted as libyang leaves it.
func (ev *evaluator) sortedMerge(trg, src []item) []item {
	out, src := slices.Clone(trg), slices.Clone(src)
	ev.assignPos(out)
	ev.assignPos(src)
	cmp := ev.sortCompare
	i, j, count, dup := 0, 0, 0, 0
	flush := func() { // copy_nodes: the block src[i-count:i] replaces the dup trg items before j
		ev.charge(len(out) / 64) // the memmove of the rest of trg
		out = slices.Concat(out[:j-dup], src[i-count:i], out[j:])
		j += count - dup
		count, dup = 0, 0
	}
	for i < len(src) && j < len(out) {
		if ev.tick() != nil {
			return out
		}
		switch c := cmp(src[i], out[j]); {
		case c == 0 && count == 0:
			i, j = i+1, j+1
		case c == 0:
			count, dup, i, j = count+1, dup+1, i+1, j+1
		case c < 0:
			count, i = count+1, i+1
		case count > 0:
			flush()
		default:
			j++
		}
	}
	if i < len(src) || count > 0 {
		count += len(src) - i
		i = len(src)
		flush()
	}
	return out
}

// sortSet is set_sort: positions assigned, then document order. Duplicates stay (libyang only
// asserts there are none).
func (ev *evaluator) sortSet(items []item) []item {
	if len(items) < 2 {
		return items
	}
	ev.assignPos(items)
	slices.SortStableFunc(items, ev.sortCompare)
	return items
}

// assignPos is set_assign_pos: every item without a stored position gets its own (roots keep none).
func (ev *evaluator) assignPos(items []item) {
	for i := range items {
		if items[i].pos == nil && items[i].t != itRoot {
			items[i].pos = items[i].n
		}
	}
}

// sortCompare is set_sort_compare: by the stored position (its element's document order), then
// at an equal position an element before its text, metadata by get_meta_pos, an element before
// anything else and text or metadata after an element. A stale position (attr) puts metadata at
// another element's position, where these rules compare them with that element's items.
func (ev *evaluator) sortCompare(a, b item) int {
	pk := func(it item) []int {
		if it.t == itRoot {
			return nil
		}
		return ev.key(item{n: it.pos, t: itElem})
	}
	if c := slices.Compare(pk(a), pk(b)); c != 0 {
		return c
	}
	same := a.n == b.n && (a.t != itMeta || b.t != itMeta || a.m == b.m)
	switch {
	case same && a.t != b.t && a.t != itMeta && b.t != itMeta: // element and its text
		if a.t == itElem {
			return -1
		}
		return 1
	case same && a.t == b.t:
		return 0
	case a.t == itElem:
		return -1
	case a.t == itText && (b.t == itElem || b.t == itMeta), a.t == itMeta && b.t == itElem,
		a.t == itMeta && b.t == itMeta && a.m > b.m:
		return 1
	}
	return -1
}

// ---- casts (lyxp_set_cast) ----

func (ev *evaluator) toBool(v value) bool {
	switch v.t {
	case vBool:
		return v.b
	case vNum:
		return !v.f.isZero() && !v.f.isNaN()
	case vStr:
		return v.s != ""
	}
	return len(v.nodes) > 0
}

func (ev *evaluator) toNum(v value) ld {
	switch v.t {
	case vNum:
		return v.f
	case vBool:
		if v.b {
			return ldInt(1)
		}
		return ld{}
	}
	str := ev.toString(v)
	if len(str) <= 64 {
		return cStrtod(str)
	}
	// long texts: big-number conversion, charged by length and memoized
	// (a constant argument or a repeated value is parsed once per evaluation)
	if x, ok := ev.nums[str]; ok {
		return x
	}
	ev.charge(len(str) / 4)
	ev.parses++
	x := cStrtod(str)
	if ev.nums == nil {
		ev.nums = map[string]ld{}
	}
	if len(ev.nums) < 1024 {
		ev.nums[str] = x
	}
	return x
}

// numCmp charges the %Lf renderings moveto_num_cmp compares.
func (ev *evaluator) numCmp(a, b ld) int {
	ev.charge((a.digits() + b.digits()) / 64)
	return numCmp(a, b)
}

// charge takes n steps from the budget at once (work proportional to digits).
func (ev *evaluator) charge(n int) {
	if n <= 0 || ev.err != nil {
		return
	}
	if ev.steps -= n; ev.steps < 0 {
		ev.err = ErrBudget
	}
}

func (ev *evaluator) toString(v value) string {
	switch v.t {
	case vStr:
		return v.s
	case vNum:
		ev.charge(v.f.digits() / 64)
		return numToString(v.f)
	case vBool:
		if v.b {
			return "true"
		}
		return "false"
	}
	if len(v.nodes) == 0 {
		return ""
	}
	return ev.stringValue(v.nodes[0])
}

// stringValue is cast_node_set_to_string of one item: a metadata value, else cast_string_elem,
// libyang's own string-value, an indented dump of the subtree's term values.
func (ev *evaluator) stringValue(it item) string {
	if it.t == itMeta {
		return orNoValue(it.meta().Value).String()
	}
	var b strings.Builder
	var rec func(n Node, indent int)
	rec = func(n Node, indent int) {
		if ev.tick() != nil {
			return
		}
		sn := n.Schema()
		if ev.ec.Root == RootConfig && sn != nil && !sn.Config() {
			return
		}
		kind := KindLeaf
		switch {
		case sn != nil:
			kind = sn.Kind()
		case len(n.Children()) > 0:
			kind = KindContainer
		}
		switch kind {
		case KindLeaf, KindLeafList:
			b.WriteString(strings.Repeat("  ", indent))
			empty := b.Len() == 0
			b.WriteString(valueOf(n).String())
			if !empty {
				b.WriteByte('\n')
			}
		case KindAnydata, KindAnyxml:
			for line := range strings.SplitSeq(valueOf(n).String(), "\n") {
				if line != "" { // strtok skips empty lines (D-0012: libyang crashes on empty content)
					b.WriteString(strings.Repeat("  ", indent) + line + "\n")
				}
			}
		default: // containers, lists, operations (D-0012: libyang fails internally on an action)
			b.WriteByte('\n')
			for _, c := range n.Children() {
				rec(c, indent+1)
			}
		}
	}
	if it.t == itRoot {
		b.WriteByte('\n')
		for _, c := range ev.ec.Tree {
			rec(c, 1)
		}
		b.WriteByte('\n')
	} else {
		rec(it.n, 0)
	}
	return b.String()
}

// ---- comparison (moveto_op_comp) ----

func (ev *evaluator) compare(a, b value, op string) bool {
	if a.t == vNodes {
		for i := range a.nodes {
			if ev.compareItem(a.nodes[i], &b, op, false) {
				return true
			}
		}
		return false
	}
	if b.t == vNodes {
		for i := range b.nodes {
			if ev.compareItem(b.nodes[i], &a, op, true) {
				return true
			}
		}
		return false
	}
	if op == "=" || op == "!=" {
		switch {
		case a.t == vBool || b.t == vBool:
			return ev.toBool(a) == ev.toBool(b) == (op == "=")
		case a.t == vNum || b.t == vNum:
			return (ev.numCmp(ev.toNum(a), ev.toNum(b)) == 0) == (op == "=")
		}
		return (a.s == b.s) == (op == "=")
	}
	c := ev.numCmp(ev.toNum(a), ev.toNum(b))
	switch op {
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	case ">":
		return c > 0
	}
	return c >= 0
}

// compareItem is moveto_op_comp_item: one node cast to the other operand's type.
func (ev *evaluator) compareItem(it item, other *value, op string, switched bool) bool {
	one := nodesV([]item{it})
	var tmp value
	switch other.t {
	case vNum:
		tmp = numV(ev.toNum(one))
	case vBool:
		tmp = boolV(true)
	default:
		tmp = strV(ev.toString(one))
	}
	// set_comp_canonize; libyang canonizes the other operand in place. For metadata it validates
	// with the metadata's next pointer as the schema node (a lyd_meta read as a lyd_node): an
	// error for the last metadata, undefined otherwise, so metadata are never canonized here.
	if other.t == vStr && it.t == itElem && isTerm(it.n) {
		if c, ok := it.n.Schema().Canonical(other.s, ev.ns); ok {
			other.s = c
		}
	}
	if switched {
		return ev.compare(*other, tmp, op)
	}
	return ev.compare(tmp, *other, op)
}
