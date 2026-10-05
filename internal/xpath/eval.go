// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (BSD-3-Clause, © CESNET).

package xpath

import (
	"context"
	"slices"
	"strings"
)

type itemType uint8

const (
	itElem itemType = iota // LYXP_NODE_ELEM
	itText                 // LYXP_NODE_TEXT: the text of a leaf / leaf-list (same Node)
	itRoot                 // LYXP_NODE_ROOT / LYXP_NODE_ROOT_CONFIG
)

type item struct {
	n Node // nil for itRoot
	t itemType
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
	e     *Expr
	ns    NamespaceCtx
	ec    *EvalContext
	ctx   context.Context
	cur   item       // current()
	op    SchemaNode // set->context_op: the RPC/action/notification of current()
	steps int
	err   error                 // sticky budget / cancellation error
	sib   map[Node]map[Node]int // per parent (nil: top level), index of each child; built lazily
	keys  map[Node][]int        // sibling indexes from the top level down to the node
}

func newEvaluator(e *Expr, ec *EvalContext) *evaluator {
	ev := &evaluator{e: e, ns: e.ns, ec: ec, ctx: ec.Ctx, steps: ec.MaxSteps}
	if ev.ctx == nil {
		ev.ctx = context.Background()
	}
	if ev.steps <= 0 {
		ev.steps = DefaultMaxSteps
	}
	switch {
	case ec.Current != nil:
		ev.cur = item{ec.Current, itElem}
	case ec.Node != nil:
		ev.cur = item{ec.Node, itElem}
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
	return nodesV([]item{{ev.ec.Node, itElem}})
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

func (ev *evaluator) eval1(a ast, ctx value) (value, error) {
	switch a := a.(type) {
	case chainExpr:
		return ev.chain(a, ctx)
	case negExpr:
		v, err := ev.eval(a.x, ctx)
		return numV(ldNeg(ev.toNum(v))), err
	case litExpr:
		return strV(string(a)), nil
	case numExpr:
		return numV(a.v), nil
	case varExpr:
		return value{}, &Error{Err: "LY_ENOTFOUND", Msg: "Variable \"" + string(a) + "\" not defined."}
	case callExpr:
		args := make([]value, len(a.args))
		for i, x := range a.args {
			var err error
			if args[i], err = ev.eval(x, ctx); err != nil {
				return value{}, err
			}
		}
		return funcImpls[a.name](ev, args, ctx)
	case pathExpr:
		return ev.path(a, ctx)
	}
	panic("xpath: unknown AST node")
}

// chain is eval_or_expr … eval_union_expr: every operand is evaluated against
// the original context, left to right, without recursion per operator.
func (ev *evaluator) chain(a chainExpr, ctx value) (value, error) {
	acc, err := ev.eval(a.args[0], ctx)
	if err != nil {
		return value{}, err
	}
	logic := a.ops[0] == "or" || a.ops[0] == "and"
	if logic {
		acc = boolV(ev.toBool(acc))
	}
	var union []item
	for i, op := range a.ops {
		if logic && acc.b == (op == "or") { // lazy evaluation
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
			if union == nil {
				union = slices.Clone(acc.nodes)
			}
			union = append(union, r.nodes...)
			acc = value{t: vNodes, nodes: union} // sorted once below
		case "=", "!=", "<", "<=", ">", ">=":
			acc = boolV(ev.compare(acc, r, op))
		default:
			acc = numV(ldOp(op, ev.toNum(acc), ev.toNum(r)))
		}
	}
	if union != nil {
		acc = nodesV(ev.sortUnique(union))
	}
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
	var err error
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
			set = nodesV(nil) // U-0002: Node has no metadata, so attributes never match
		case s.allDesc && s.axis == "child":
			set, err = ev.allDescChild(set, nt)
		default:
			if sn := ev.schemaTarget(set, nt, s); sn != nil {
				set, err = ev.hashChild(set, sn, nt.name)
			} else {
				set, err = ev.moveto(set, s.axis, nt)
			}
		}
		if err != nil {
			return value{}, err
		}
	}
	return ev.predicates(set, s.preds, s.axis)
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
		nt.mod = ev.ns.Default()
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
func (ev *evaluator) schemaTarget(set value, nt nameTest, s step) SchemaNode {
	if nt.name == "" || s.axis != "child" {
		return nil
	}
	var found, foundParent SchemaNode
	for _, it := range set.nodes {
		var cand SchemaNode
		if it.t == itRoot {
			if ev.ec.Schema == nil {
				return nil // cannot search: match by name
			}
			tops := ev.ec.Schema.TopLevel(nt.mod, nt.name)
			if len(tops) > 1 {
				return nil // matches in several modules
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
				return nil // found at different levels
			}
			found = cand
		}
	}
	if found != nil && (found.Kind() == KindList || found.Kind() == KindLeafList) && !ev.hashPredicates(set, found, s.preds) {
		return nil
	}
	return found
}

// hashPredicates is eval_name_test_try_compile_predicates: whether the step
// starts with "[key = value]" for every list key in order (leaf-list:
// "[. = value]") whose values do not depend on the instance.
func (ev *evaluator) hashPredicates(set value, sn SchemaNode, preds []ast) bool {
	keys := sn.Keys()
	if sn.Kind() == KindLeafList {
		keys = []string{"."}
	}
	if len(keys) == 0 || len(preds) < len(keys) {
		return false
	}
	base := []SchemaNode{nil} // schema path of the predicate context: ... / sn
	if it := set.nodes[0]; it.t != itRoot {
		if base = schemaChain(it.n); base == nil {
			return false
		}
	}
	base = append(base, sn)
	for i, k := range keys {
		c, ok := preds[i].(chainExpr)
		if !ok || c.ops[0] != "=" {
			return false
		}
		p, ok := c.args[0].(pathExpr)
		if !ok || p.abs || p.prim != nil || len(p.steps) != 1 {
			return false
		}
		st := p.steps[0]
		if st.allDesc || st.explicit || len(st.preds) > 0 {
			return false // '[' NameTest '=' only
		}
		if k == "." {
			if st.test != tDot {
				return false
			}
		} else {
			// eval_name_test_try_compile_predicate_key: the key's module and name
			nt, err := ev.resolveName(st.name)
			if nt.mod == "" {
				nt.mod = sn.Module() // JSON: the list's module
			}
			if st.test != tNameTest || err != nil || nt.name != k || nt.mod != sn.Module() {
				return false
			}
		}
		val := ast(chainExpr{ops: c.ops[1:], args: c.args[1:]})
		if len(c.ops) == 1 {
			val = c.args[1]
		}
		if !ev.atomsOK(val, base) {
			return false
		}
	}
	return true
}

// schemaChain is the schema path of a data node: nil (the root), then its
// ancestors' schema nodes down to its own; nil for an opaque node.
func schemaChain(n Node) []SchemaNode {
	var c []SchemaNode
	for ; n != nil; n = n.Parent() {
		sn := n.Schema()
		if sn == nil {
			return nil
		}
		c = append(c, sn)
	}
	c = append(c, nil)
	slices.Reverse(c)
	return c
}

// atomsOK is the dependency check of eval_name_test_try_compile_predicate_append
// on a key value: no top-level or/and, and none of its paths (walked over the
// schema like lyxp_atomize) reaches a list or leaf-list other than current()'s
// node, nor a child of the looked-up node (the last element of base).
func (ev *evaluator) atomsOK(a ast, base []SchemaNode) bool {
	switch a := a.(type) {
	case litExpr, numExpr:
		return true
	case negExpr:
		return ev.atomsOK(a.x, base)
	case chainExpr:
		for i, op := range a.ops {
			if op == "or" || op == "and" || !ev.atomsOK(a.args[i], base) {
				return false
			}
		}
		return ev.atomsOK(a.args[len(a.args)-1], base)
	case callExpr:
		for _, x := range a.args {
			if !ev.atomsOK(x, base) {
				return false
			}
		}
		return true
	case pathExpr:
		var stack []SchemaNode
		switch c, ok := a.prim.(callExpr); {
		case a.abs:
			stack = []SchemaNode{nil}
		case a.prim == nil:
			stack = slices.Clone(base)
		case ok && c.name == "current" && len(a.preds) == 0:
			stack = []SchemaNode{nil}
			if ev.cur.t == itElem {
				stack = schemaChain(ev.cur.n)
			}
		default:
			return false // ponytail: other primary expressions are not analysed (D-0013)
		}
		return stack != nil && ev.walkAtoms(stack, a.steps, base[len(base)-1])
	}
	return false
}

func (ev *evaluator) walkAtoms(stack []SchemaNode, steps []step, target SchemaNode) bool {
	var cur SchemaNode
	if ev.cur.t == itElem {
		cur = ev.cur.n.Schema()
	}
	multi := func(sn SchemaNode) bool {
		return sn != nil && sn != cur && (sn.Kind() == KindList || sn.Kind() == KindLeafList)
	}
	for _, s := range steps {
		if s.allDesc || s.explicit || len(s.preds) > 0 {
			return false // ponytail: only plain child, '.' and '..' steps are analysed (D-0013)
		}
		top := stack[len(stack)-1]
		switch s.test {
		case tDot:
			if multi(top) {
				return false
			}
		case tDDot:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			if multi(stack[len(stack)-1]) {
				return false
			}
		case tNameTest:
			nt, err := ev.resolveName(s.name)
			if err != nil || nt.name == "" {
				return false
			}
			var next []SchemaNode
			switch {
			case top != nil:
				if top == target {
					return false // a child of the looked-up node: depends on the instance
				}
				mod := nt.mod
				if mod == "" {
					mod = top.Module()
				}
				if c := top.Child(mod, nt.name); c != nil {
					next = []SchemaNode{c}
				}
			case ev.ec.Schema != nil:
				next = ev.ec.Schema.TopLevel(nt.mod, nt.name)
			}
			for _, n := range next {
				if multi(n) {
					return false
				}
			}
			if len(next) != 1 {
				return true // no such schema node: libyang stops atomizing the path
			}
			stack = append(stack, next[0])
		default:
			return false // node(), text()
		}
	}
	return true
}

// hashChild is moveto_node_hash_child: the instances of sn under each context
// node (an opaque node of that name if there is none).
func (ev *evaluator) hashChild(set value, sn SchemaNode, name string) (value, error) {
	if ev.ec.Root == RootConfig && !sn.Config() || ev.op != nil && isOp(sn) && sn != ev.op {
		return nodesV(nil), nil
	}
	var out []item
	for _, it := range set.nodes {
		var kids []Node
		switch it.t {
		case itRoot:
			kids = ev.ec.Tree
		case itElem:
			kids = it.n.Children()
		}
		var hit []Node
		for _, c := range kids {
			if err := ev.tick(); err != nil {
				return value{}, err
			}
			if c.Schema() == sn {
				hit = append(hit, c)
			}
		}
		if hit == nil {
			for _, c := range kids {
				if c.Schema() == nil && c.Name() == name {
					hit = []Node{c}
					break
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
			out = append(out, item{c, itElem})
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
	sn := it.n.Schema()
	if sn == nil || (nt.mod != "" && it.n.Module() != nt.mod) {
		return false, false, nil
	}
	if ev.ec.Root == RootConfig && !sn.Config() || ev.op != nil && isOp(sn) && sn != ev.op {
		return false, true, nil
	}
	if nt.name != "" && it.n.Name() != nt.name {
		return false, false, nil
	}
	isCur := ev.cur.t == itElem && it.n == ev.cur.n
	switch it.n.When() {
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
	for _, it := range set.nodes {
		err := ev.axis(it, axis, func(x item) error {
			if err := ev.tick(); err != nil {
				return err
			}
			ok, _, err := ev.check(x, nt)
			if ok {
				out = append(out, x)
			}
			return err
		})
		if err != nil {
			return value{}, err
		}
	}
	if axis != "child" && axis != "self" { // libyang sorts only after the other axes
		out = ev.sortUnique(out)
	}
	return nodesV(out), nil
}

// allDescChild is moveto_node_alldesc_child ('//' NameTest).
func (ev *evaluator) allDescChild(set value, nt nameTest) (value, error) {
	starts := len(set.nodes)
	set, err := ev.moveto(set, "child", nameTest{any: true})
	if err != nil {
		return value{}, err
	}
	var out []item
	var dfs func(n Node) error
	dfs = func(n Node) error {
		if err := ev.tick(); err != nil {
			return err
		}
		ok, skip, err := ev.check(item{n, itElem}, nt)
		if err != nil || skip {
			return err
		}
		if ok {
			out = append(out, item{n, itElem})
		}
		// libyang descends into when-false nodes here: their descendants match
		for _, c := range n.Children() {
			if err := dfs(c); err != nil {
				return err
			}
		}
		return nil
	}
	for _, it := range set.nodes {
		if err := dfs(it.n); err != nil {
			return value{}, err
		}
	}
	if starts > 1 { // several start nodes may nest
		out = ev.sortUnique(out)
	}
	return nodesV(out), nil
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
			out = append(out, item{it.n, itText})
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
	sib := ev.siblings(n)
	p := n.Parent()
	m, ok := ev.sib[p]
	if !ok {
		if ev.sib == nil {
			ev.sib = map[Node]map[Node]int{}
		}
		m = make(map[Node]int, len(sib))
		for i, s := range sib {
			if ev.tick() != nil {
				break
			}
			m[s] = i
		}
		ev.sib[p] = m
	}
	i, ok := m[n]
	if !ok {
		i = -1 // not under Tree
	}
	return sib, i
}

// axis is moveto_axis_node_next: every node on axis from it, in any order
// (callers sort).
func (ev *evaluator) axis(it item, axis string, yield func(item) error) error {
	var subtree func(n Node) error // n and its descendants
	subtree = func(n Node) error {
		if err := yield(item{n, itElem}); err != nil {
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
		case x.t == itText:
			return item{x.n, itElem}, true
		case x.t == itElem && x.n.Parent() != nil:
			return item{x.n.Parent(), itElem}, true
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
				err = yield(item{c, itElem})
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
				if err := yield(item{s, itElem}); err != nil {
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
				if err := yield(item{x.Parent(), itElem}); err != nil {
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
				set = nodesV(nil)
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
			r, err := ev.eval(pr, value{t: vNodes, nodes: []item{it}, pos: pos, size: len(set.nodes)})
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
		set = value{t: vNodes, nodes: keep, pos: set.pos, size: set.size}
	}
	return set, nil
}

// key is the document-order key of an item: its sibling indexes from the top
// level (root: empty, so first; text: right after its element).
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
	if it.t == itText {
		return append(slices.Clip(k), -1)
	}
	return k
}

// sortUnique is set_sort + duplicate removal.
func (ev *evaluator) sortUnique(items []item) []item {
	if len(items) < 2 {
		return items
	}
	keys := make(map[item][]int, len(items))
	for _, it := range items {
		if ev.tick() != nil {
			return items
		}
		keys[it] = ev.key(it)
	}
	slices.SortStableFunc(items, func(a, b item) int { return slices.Compare(keys[a], keys[b]) })
	return slices.Compact(items)
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
	return cStrtod(ev.toString(v))
}

func (ev *evaluator) toString(v value) string {
	switch v.t {
	case vStr:
		return v.s
	case vNum:
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

// stringValue is cast_string_elem: libyang's own string-value, an indented dump
// of the subtree's term values.
func (ev *evaluator) stringValue(it item) string {
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
			return (numCmp(ev.toNum(a), ev.toNum(b)) == 0) == (op == "=")
		}
		return (a.s == b.s) == (op == "=")
	}
	c := numCmp(ev.toNum(a), ev.toNum(b))
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
	// set_comp_canonize; libyang canonizes the other operand in place
	if other.t == vStr && it.t == itElem && isTerm(it.n) {
		if c, ok := it.n.Schema().Canonical(other.s); ok {
			other.s = c
		}
	}
	if switched {
		return ev.compare(*other, tmp, op)
	}
	return ev.compare(tmp, *other, op)
}
