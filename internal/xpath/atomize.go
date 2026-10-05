// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (BSD-3-Clause, © CESNET).

package xpath

import (
	"fmt"
	"slices"
)

// AtomUse is lyxp_set_scnode.in_ctx (xpath.h): how an atom was used.
// Values from AtomPredCtx up are one level per nested predicate.
type AtomUse int32

// Atom uses (LYXP_SET_SCNODE_*).
const (
	AtomStart     AtomUse = -2 // context node, not traversed, still in context
	AtomStartUsed AtomUse = -1 // context node, not traversed except for the start
	AtomNode      AtomUse = 0  // traversed
	AtomVal       AtomUse = 1  // traversed and its value used
	AtomCtx       AtomUse = 2  // in context
	AtomNewCtx    AtomUse = 3  // in context, just added (internal to one move)
	AtomPredCtx   AtomUse = 4  // in context of an enclosing predicate
)

// Atom is a schema node an expression can reach (lyxp_set_scnode).
type Atom struct {
	Node SchemaNode // nil for the document root
	Use  AtomUse
}

// AtomizeContext is the context of lyxp_atomize.
type AtomizeContext struct {
	Node        SchemaNode // context node and current(); nil = the root
	SchemaRules bool       // LYXP_SCNODE_SCHEMA: when/must access rules (config root for a config node)
	Output      bool       // LYXP_SCNODE_OUTPUT: RPC/action output instead of input
	Schema      SchemaInfo
	Warn        func(msg string) // LOGWRN; nil drops warnings
	MaxSteps    int              // 0 = DefaultMaxSteps
}

// Atomize walks e over the schema (lyxp_atomize) and returns every schema
// node it reaches, in libyang's set order, the context node first.
func (e *Expr) Atomize(ac AtomizeContext) ([]Atom, error) {
	a := newAtomizer(e.ns, ac.Schema, ac.Node, ac.Node, rootType(ac.Node, ac.SchemaRules), ac.Output, ac.Warn, ac.MaxSteps)
	set, err := a.run(e.src, e.root)
	if err != nil {
		return nil, err
	}
	out := make([]Atom, len(set.n))
	for i, x := range set.n {
		out[i] = Atom{x.n, x.use}
	}
	return out, nil
}

// rootType is the schema branch of lyxp_get_root_type.
func rootType(ctx SchemaNode, rules bool) ntype {
	op := ctx
	for op != nil && !isOp(op) {
		op = op.Parent()
	}
	if op == nil && rules && (ctx == nil || ctx.Config()) {
		return nRootConfig // only config data, "because we said so" (libyang)
	}
	return nRoot
}

type ntype uint8 // lyxp_node_type of a schema set item

const (
	nElem ntype = iota + 1
	nRoot
	nRootConfig
)

type scnode struct {
	n    SchemaNode
	t    ntype
	use  AtomUse
	axis string
}

type scset struct{ n []scnode } // LYXP_SET_SCNODE_SET

type atomizer struct {
	ns     NamespaceCtx
	info   SchemaInfo
	cur    SchemaNode // cur_scnode
	ctx    SchemaNode // ctx_scnode
	op     SchemaNode // context_op
	root   ntype
	output bool
	warn   func(string)
	src    string
	steps  int
	err    error
	pos    map[SchemaNode]int // index of a node in its sibling list
	mods   []string
}

func newAtomizer(ns NamespaceCtx, info SchemaInfo, cur, ctx SchemaNode, root ntype, output bool, warn func(string), steps int) *atomizer {
	a := &atomizer{ns: ns, info: info, cur: cur, ctx: ctx, root: root, output: output, warn: warn, steps: steps}
	if a.steps <= 0 {
		a.steps = DefaultMaxSteps
	}
	a.op = cur
	for a.op != nil && !isOp(a.op) {
		a.op = a.op.Parent()
	}
	if info != nil {
		a.mods = info.Modules()
	}
	return a
}

func (a *atomizer) run(src string, root ast) (*scset, error) {
	a.src = src
	set := &scset{}
	t := nElem
	if a.ctx == nil {
		t = a.root
	}
	set.n = []scnode{{a.ctx, t, AtomStart, "self"}}
	if err := a.eval(root, set); err != nil {
		return nil, err
	}
	return set, a.err
}

func (a *atomizer) tick() error {
	if a.err == nil {
		if a.steps--; a.steps < 0 {
			a.err = ErrBudget
		}
	}
	return a.err
}

// ---- set operations ----

// clearCtx is set_scnode_clear_ctx.
func (s *scset) clearCtx(use AtomUse) {
	for i := range s.n {
		switch s.n[i].use {
		case AtomCtx:
			s.n[i].use = use
		case AtomStart:
			s.n[i].use = AtomStartUsed
		}
	}
}

// contains is lyxp_set_scnode_contains.
func (s *scset) contains(n SchemaNode, t ntype, skip int) (int, bool) {
	for i, x := range s.n {
		if i != skip && x.n == n && x.t == t {
			return i, true
		}
	}
	return 0, false
}

// insert is lyxp_set_scnode_insert_node.
func (s *scset) insert(n SchemaNode, t ntype, axis string) int {
	if i, ok := s.contains(n, t, -1); ok {
		s.n[i].use = AtomCtx // libyang: a different axis is thrown away
		return i
	}
	s.n = append(s.n, scnode{n, t, AtomCtx, axis})
	return len(s.n) - 1
}

// merge is lyxp_set_scnode_merge (duplicates by schema node only).
func (s *scset) merge(s2 *scset) {
	if len(s2.n) == 0 {
		return
	}
	if len(s.n) == 0 {
		s.n = slices.Clone(s2.n)
		return
	}
	orig := len(s.n)
	for _, y := range s2.n {
		j := slices.IndexFunc(s.n[:orig], func(x scnode) bool { return x.n == y.n })
		switch {
		case j < 0:
			s.n = append(s.n, y)
		case s.n[j].use == AtomStartUsed, s.n[j].use == AtomNode && y.use == AtomVal:
			s.n[j].use = y.use
		}
	}
}

// clone is set_fill_set.
func (s *scset) clone() *scset { return &scset{slices.Clone(s.n)} }

// copyCtx is set_copy: only the nodes in context or at the start.
func (s *scset) copyCtx() *scset {
	c := &scset{}
	for _, x := range s.n {
		if x.use == AtomCtx || x.use == AtomStart {
			c.n[c.insert(x.n, x.t, x.axis)].use = x.use
		}
	}
	return c
}

// newInCtx is set_scnode_new_in_ctx.
func (s *scset) newInCtx() AtomUse {
	u := AtomPredCtx
	for _, x := range s.n {
		u = max(u, x.use+1)
	}
	for i := range s.n {
		if s.n[i].use == AtomCtx {
			s.n[i].use = u
		}
	}
	return u
}

func (s *scset) hasCtx() bool {
	return slices.ContainsFunc(s.n, func(x scnode) bool { return x.use == AtomCtx })
}

// ---- expressions (eval_* in schema mode) ----

func (a *atomizer) eval(x ast, s *scset) error {
	if err := a.tick(); err != nil {
		return err
	}
	switch x := x.(type) {
	case chainExpr:
		return a.chain(x, s)
	case negExpr: // eval_unary_expr; warn_operands is C2b
		return a.eval(x.x, s)
	case litExpr, numExpr:
		s.clearCtx(AtomVal)
	case varExpr:
		return &Error{Err: "LY_ENOTFOUND", Msg: "Variable \"" + string(x) + "\" not defined."}
	case callExpr:
		return a.call(x, s)
	case pathExpr:
		return a.path(x, s)
	}
	return nil
}

// chain is eval_or_expr … eval_union_expr (LYXP_SCNODE_ALL branches).
func (a *atomizer) chain(x chainExpr, s *scset) error {
	orig := s.clone()
	if err := a.eval(x.args[0], s); err != nil {
		return err
	}
	logic := x.ops[0] == "or" || x.ops[0] == "and"
	if logic {
		s.clearCtx(AtomNode)
	}
	for i, op := range x.ops {
		s2 := orig.clone()
		if err := a.eval(x.args[i+1], s2); err != nil {
			return err
		}
		switch {
		case logic:
			s2.clearCtx(AtomNode)
			s.merge(s2)
		case op == "|":
			s.merge(s2)
		default: // warn_operands / warn_equality_value are C2b
			s.merge(s2)
			s.clearCtx(AtomVal)
		}
	}
	return nil
}

// nodeFuncs leave their context as plain atoms; the others use the value.
var nodeFuncs = map[string]bool{"boolean": true, "count": true, "false": true, "last": true,
	"local-name": true, "name": true, "not": true, "position": true, "true": true}

// call is eval_function_call; per-function argument warnings are C2b.
func (a *atomizer) call(x callExpr, s *scset) error {
	args := make([]*scset, len(x.args))
	for i, arg := range x.args {
		args[i] = s.copyCtx()
		if err := a.eval(arg, args[i]); err != nil {
			return err
		}
	}
	switch {
	case x.name == "current": // xpath_current
		s.clearCtx(AtomNode)
		if a.cur != nil {
			s.insert(a.cur, nElem, "self")
		} else {
			s.insert(nil, a.root, "self")
		}
	case x.name == "deref": // xpath_deref
		sn := lastCtx(args[0])
		s.clearCtx(AtomVal)
		if sn != nil && (sn.Kind() == KindLeaf || sn.Kind() == KindLeafList) {
			if t := sn.LeafrefTarget(); t != nil {
				s.insert(t, nElem, "self")
			}
		}
	case nodeFuncs[x.name]:
		s.clearCtx(AtomNode)
	default:
		s.clearCtx(AtomVal)
	}
	for _, arg := range args {
		arg.clearCtx(AtomNode)
		s.merge(arg)
	}
	return nil
}

// lastCtx is warn_get_scnode_in_ctx: the last-added node in context.
func lastCtx(s *scset) SchemaNode {
	for i := len(s.n) - 1; i >= 0; i-- {
		if s.n[i].use == AtomCtx {
			return s.n[i].n
		}
	}
	return nil
}

// path is eval_path_expr / eval_absolute_location_path / eval_relative_location_path.
func (a *atomizer) path(x pathExpr, s *scset) error {
	switch {
	case x.abs: // moveto_root
		s.clearCtx(AtomNode)
		s.insert(nil, a.root, "self")
	case x.prim != nil:
		if err := a.eval(x.prim, s); err != nil {
			return err
		}
		if err := a.predicates(s, x.preds); err != nil {
			return err
		}
	}
	for _, st := range x.steps {
		found, err := a.step(st, s)
		if err != nil || !found {
			return err // not found: the rest of the path is skipped
		}
	}
	return nil
}

func (a *atomizer) step(st step, s *scset) (bool, error) {
	all := nameTest{any: true}
	switch st.test {
	case tDot, tDDot:
		if st.allDesc {
			if err := a.moveto(s, "descendant-or-self", all); err != nil {
				return false, err
			}
		}
		axis := "self"
		if st.test == tDDot {
			axis = "parent"
		}
		return true, a.moveto(s, axis, all)
	case tNodeType: // eval_node_type_with_predicate ignores '//'
		if st.name == "node" {
			if err := a.moveto(s, st.axis, all); err != nil {
				return false, err
			}
		} else {
			s.clearCtx(AtomVal) // xpath_pi_text
		}
		return true, a.predicates(s, st.preds)
	}
	// eval_name_test_with_predicate
	nt, err := a.resolve(st.name)
	if err != nil {
		return false, err
	}
	if st.axis == "attribute" {
		s.clearCtx(AtomNode)
		return true, a.predicates(s, st.preds)
	}
	var parent *scnode // the only node in context, for the warning
	for i := range s.n {
		if s.n[i].use == AtomCtx {
			if parent != nil {
				parent = nil
				break
			}
			parent = &s.n[i]
		}
	}
	var pp string
	if parent != nil {
		switch parent.t {
		case nElem:
			pp = parent.n.Path()
		case nRoot:
			pp = "<root>"
		case nRootConfig:
			pp = "<config-root>"
		}
	}
	if st.allDesc && st.axis == "child" {
		err = a.alldescChild(s, nt)
	} else {
		if st.allDesc {
			err = a.moveto(s, "descendant-or-self", all)
		}
		if err == nil {
			err = a.moveto(s, st.axis, nt)
		}
	}
	if err != nil {
		return false, err
	}
	if !slices.ContainsFunc(s.n, func(x scnode) bool { return x.use > AtomNode }) {
		a.notFound(st, nt, pp)
		return false, nil // predicates and the rest of the path are skipped
	}
	return true, a.predicates(s, st.preds)
}

// resolve is moveto_resolve_module for a NameTest; "*" and "p:*" match any name.
func (a *atomizer) resolve(qname string) (nameTest, error) {
	if qname == "*" {
		return nameTest{}, nil
	}
	ev := evaluator{ns: a.ns}
	nt, err := ev.resolveName(qname)
	nt.any = false
	return nt, err
}

// notFound is eval_name_test_scnode_no_match_msg.
func (a *atomizer) notFound(st step, nt nameTest, parentPath string) {
	if a.warn == nil {
		return
	}
	// libyang: for '*' the name is NULL and the expression length garbage,
	// which prints the whole expression (D-0014)
	expr := a.src
	if nt.name != "" {
		expr = a.src[:st.end]
	}
	cur := "(null)"
	if a.cur != nil {
		cur = a.cur.Path()
	}
	if parentPath != "" {
		a.warn(fmt.Sprintf("Schema node \"%s\" for parent \"%s\" not found; in expr \"%s\" with context node \"%s\".", nt.name, parentPath, expr, cur))
	} else {
		a.warn(fmt.Sprintf("Schema node \"%s\" not found; in expr \"%s\" with context node \"%s\".", nt.name, expr, cur))
	}
}

// predicates is eval_predicate (schema branch) for each predicate.
func (a *atomizer) predicates(s *scset, preds []ast) error {
	for _, pr := range preds {
		if !s.hasCtx() {
			continue // only parsed
		}
		pc := s.newInCtx()
		for i := 0; i < len(s.n); i++ {
			if s.n[i].use != pc {
				continue
			}
			s.n[i].use = AtomCtx
			if err := a.eval(pr, s); err != nil {
				return err
			}
			s.n[i].use = pc
		}
		for i := range s.n {
			switch s.n[i].use {
			case AtomCtx:
				s.n[i].use = AtomNode
			case pc:
				s.n[i].use = AtomCtx
			}
		}
	}
	return nil
}

// ---- moving over the schema ----

// check is moveto_scnode_check: 0 match, 1 LY_ENOT, 2 LY_EINVAL (skip the subtree).
func (a *atomizer) check(n SchemaNode, nt nameTest) int {
	switch {
	case n == nil:
		if nt.name != "" || nt.mod != "" {
			return 1
		}
		return 0
	case nt.mod != "" && n.Module() != nt.mod:
		return 1
	case a.root == nRootConfig && !n.Config(), a.op != nil && isOp(n) && n != a.op:
		return 2
	case nt.name != "" && n.Name() != nt.name:
		return 1
	}
	return 0
}

// inCtx is moveto_axis_scnode_next_in_ctx.
func inCtx(use *AtomUse, axis string) bool {
	switch {
	case axis == "self" && (*use == AtomStart || *use == AtomCtx):
		*use = AtomCtx
	case axis != "self" && *use == AtomStart:
		*use = AtomStartUsed
	case axis != "self" && *use == AtomCtx:
		*use = AtomNode
	default:
		return false
	}
	return true
}

// moveto is moveto_scnode (and xpath_pi_node for nameTest{}). Nodes of
// extension instances (lys_find_child_node_ext) are not searched: their
// plugins are unsupported (design 06 §2.17).
func (a *atomizer) moveto(s *scset, axis string, nt nameTest) error {
	orig := len(s.n)
	temp := false
	for i := 0; i < orig; i++ {
		if !inCtx(&s.n[i].use, axis) {
			continue
		}
		it := axisIter{}
		for a.axisNext(&it, s.n[i].n, s.n[i].t, axis) {
			if err := a.tick(); err != nil {
				return err
			}
			if a.check(it.n, nt) != 0 {
				continue
			}
			if idx := s.insert(it.n, it.t, axis); idx < orig && idx > i {
				s.n[idx].use = AtomNewCtx
				temp = true
			}
		}
	}
	if temp {
		for i := range orig {
			if s.n[i].use == AtomNewCtx {
				s.n[i].use = AtomCtx
			}
		}
	}
	return a.err
}

// alldescChild is moveto_scnode_alldesc_child.
func (a *atomizer) alldescChild(s *scset, nt nameTest) error {
	orig := len(s.n)
	for i := 0; i < orig; i++ {
		switch s.n[i].use {
		case AtomCtx:
			s.n[i].use = AtomNode
		case AtomStart:
			s.n[i].use = AtomStartUsed
		default:
			continue
		}
		var err error
		if s.n[i].t == nElem {
			err = a.dfs(s, s.n[i].n, i, nt)
		} else {
			for _, m := range a.mods {
				for r := a.getnext(nil, nil, m, false); r != nil && err == nil; r = a.getnext(r, nil, m, false) {
					err = a.dfs(s, r, i, nt)
				}
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// dfs is moveto_scnode_dfs: start's descendants over the raw tree.
func (a *atomizer) dfs(s *scset, start SchemaNode, startIdx int, nt nameTest) error {
	for elem, next := start, start; elem != nil; elem = next {
		if err := a.tick(); err != nil {
			return err
		}
		skip := false
		if k := elem.Kind(); elem != start && k != KindChoice && k != KindCase {
			switch a.check(elem, nt) {
			case 0:
				if idx, ok := s.contains(elem, nElem, startIdx); ok {
					s.n[idx].use = AtomCtx
					skip = idx > startIdx // processed later in the set
				} else {
					s.insert(elem, nElem, "descendant")
				}
			case 2:
				skip = true
			}
		}
		next = nil
		if !skip {
			next = child(elem)
			if next != nil && (next.Kind() == KindInput && a.output || next.Kind() == KindOutput && !a.output) {
				next = a.next(next)
			}
		}
		if next == nil {
			if elem == start {
				break
			}
			next = a.next(elem)
		}
		for next == nil {
			if elem.Parent() == start {
				break
			}
			elem = elem.Parent()
			next = a.next(elem)
		}
	}
	return nil
}

// axisIter is the iterator state of moveto_axis_scnode_next.
type axisIter struct {
	n      SchemaNode
	t      ntype // 0 before the first node
	mod    string
	modIdx int
}

// axisNext is moveto_axis_scnode_next (+ _next_first): it advances it to
// the next node on axis from the context node (sn, t); false at the end.
func (a *atomizer) axisNext(it *axisIter, sn SchemaNode, t ntype, axis string) bool {
	if it.t == 0 {
		return a.axisFirst(it, sn, t, axis)
	}
	var next SchemaNode
	nt := ntype(0)
	set := func(n SchemaNode) bool {
		if n == nil {
			return false
		}
		next, nt = n, nElem
		return true
	}
	dfs, top := false, false
	var stop SchemaNode
	switch axis {
	case "ancestor-or-self", "ancestor":
		if axis == "ancestor-or-self" && it.n == sn && it.t == t {
			*it = axisIter{} // self was returned, now the ancestors
			return a.axisFirst(it, sn, t, "ancestor")
		}
		if it.t == nElem && !set(dataParent(it.n)) {
			nt = a.root
		}
	case "descendant-or-self":
		if it.n == sn && it.t == t {
			*it = axisIter{}
			return a.axisFirst(it, sn, t, "descendant")
		}
		dfs, stop = true, sn
	case "descendant":
		dfs, stop = true, sn
	case "preceding":
		stop = a.precedingSibling(sn)
		dfs = it.n != stop // done at the previous sibling
	case "following":
		dfs = true
	case "child", "following-sibling":
		top = true
	case "preceding-sibling":
		if n := a.getnext(it.n, dataParent(it.n), it.n.Module(), a.output); n != sn {
			set(n)
		}
	}
	if dfs && !set(a.dfsForward(it.n, stop)) {
		top = true // the next top-level node, like a child
	}
	if top {
		a.nextTop(it, sn, axis, set)
	}
	it.n, it.t = next, nt
	return nt != 0
}

// nextTop is the child / following-sibling part of moveto_axis_scnode_next.
func (a *atomizer) nextTop(it *axisIter, sn SchemaNode, axis string, set func(SchemaNode) bool) {
	if it.mod == "" { // nodes of a single module
		if set(a.getnext(it.n, dataParent(it.n), it.n.Module(), a.output)) {
			return
		}
		if axis != "child" && dataParent(sn) == nil {
			for dataParent(it.n) != nil {
				it.n = dataParent(it.n)
			}
			if set(a.getnext(it.n, nil, it.n.Module(), a.output)) {
				return
			}
		}
	}
	for it.mod != "" { // the root's children, module after module
		if set(a.getnext(it.n, nil, it.mod, a.output)) {
			return
		}
		it.mod = ""
		if it.modIdx < len(a.mods) {
			it.mod = a.mods[it.modIdx]
			it.modIdx++
		}
		it.n = nil
	}
}

// axisFirst is moveto_axis_scnode_next_first.
func (a *atomizer) axisFirst(it *axisIter, sn SchemaNode, t ntype, axis string) bool {
	var next SchemaNode
	nt := ntype(0)
	switch axis {
	case "ancestor-or-self", "descendant-or-self", "self":
		next, nt = sn, t
	case "ancestor", "parent":
		if t == nElem {
			next, nt = dataParent(sn), a.root
			if next != nil {
				nt = nElem
			}
		}
	case "descendant", "child":
		if t != nElem {
			for it.modIdx < len(a.mods) {
				it.mod = a.mods[it.modIdx]
				it.modIdx++
				if next = a.getnext(nil, nil, it.mod, a.output); next != nil {
					nt = nElem
					break
				}
			}
			if next == nil {
				it.mod = ""
			}
		} else if next = a.getnext(nil, sn, "", a.output); next != nil {
			nt = nElem
		}
	case "following", "following-sibling":
		if t == nElem {
			if next = a.getnext(sn, dataParent(sn), sn.Module(), a.output); next != nil {
				nt = nElem
			}
		}
	case "preceding", "preceding-sibling":
		if t == nElem {
			if next = a.getnext(nil, dataParent(sn), sn.Module(), a.output); next == sn {
				next = nil
			}
			if next != nil {
				nt = nElem
			}
		}
	}
	it.n, it.t = next, nt
	return nt != 0
}

// dfsForward is moveto_axis_scnode_next_dfs_forward.
func (a *atomizer) dfsForward(iter, stop SchemaNode) SchemaNode {
	next := child(iter)
	if next == nil {
		if iter == stop || dataParent(iter) == nil {
			return nil
		}
		next = a.getnext(iter, dataParent(iter), "", a.output)
	}
	for next == nil && iter != nil {
		iter = iter.Parent()
		if iter == stop || iter == nil || dataParent(iter) == nil {
			return nil
		}
		next = a.getnext(iter, dataParent(iter), "", a.output)
	}
	return next
}

// precedingSibling is moveto_axis_scnode_preceding_sibling.
func (a *atomizer) precedingSibling(sn SchemaNode) SchemaNode {
	var prev SchemaNode
	for n := a.getnext(nil, dataParent(sn), sn.Module(), a.output); n != nil && n != sn; n = a.getnext(n, dataParent(sn), sn.Module(), a.output) {
		prev = n
	}
	return prev
}

// ---- lysc tree primitives ----

func isDataNode(n SchemaNode) bool {
	switch n.Kind() {
	case KindChoice, KindCase, KindInput, KindOutput:
		return false
	}
	return true
}

// dataParent is lysc_data_parent.
func dataParent(n SchemaNode) SchemaNode {
	if n == nil {
		return nil
	}
	for p := n.Parent(); p != nil; p = p.Parent() {
		if isDataNode(p) {
			return p
		}
	}
	return nil
}

// child is lysc_node_child.
func child(n SchemaNode) SchemaNode {
	if n == nil {
		return nil
	}
	if k := n.Children(); len(k) > 0 {
		return k[0]
	}
	return nil
}

func head(l []SchemaNode) SchemaNode {
	if len(l) > 0 {
		return l[0]
	}
	return nil
}

func (a *atomizer) modLists(mod string) (data, rpcs, notifs []SchemaNode) {
	if a.info == nil {
		return nil, nil, nil
	}
	return a.info.ModuleNodes(mod)
}

// next is lysc_node.next: the following node in n's sibling list.
func (a *atomizer) next(n SchemaNode) SchemaNode {
	p := n.Parent()
	var list []SchemaNode
	switch k := n.Kind(); {
	case k == KindRPC || k == KindAction:
		if p != nil {
			list = p.Actions()
		} else {
			_, list, _ = a.modLists(n.Module())
		}
	case k == KindNotif:
		if p != nil {
			list = p.Notifications()
		} else {
			_, _, list = a.modLists(n.Module())
		}
	case p != nil:
		list = p.Children()
	default:
		list, _, _ = a.modLists(n.Module())
	}
	i, ok := a.pos[n]
	if !ok {
		if a.pos == nil {
			a.pos = map[SchemaNode]int{}
		}
		for j, x := range list {
			_ = a.tick()
			a.pos[x] = j
		}
		i = a.pos[n]
	}
	if i+1 < len(list) {
		return list[i+1]
	}
	return nil
}

// getnext is lys_getnext(last, parent, module, LYS_GETNEXT_OUTPUT if output).
func (a *atomizer) getnext(last, parent SchemaNode, mod string, output bool) SchemaNode {
	var next SchemaNode
	action, notif := false, false
	advance := func() { // the "next:" label with last set
		switch last.Kind() {
		case KindRPC, KindAction:
			action = true
		case KindNotif:
			action, notif = true, true
		}
		next = a.next(last)
	}
	if last == nil {
		if parent != nil {
			next = child(parent)
		} else {
			data, _, _ := a.modLists(mod)
			next = head(data)
		}
		last = next
	} else {
		advance()
	}
	// intoCase is lys_getnext_into_case
	intoCase := func(c SchemaNode) {
		for ; c != nil; c = a.next(c) {
			if k := child(c); k != nil {
				next = k
				return
			}
		}
		last, next = next, a.next(next)
	}
	for a.tick() == nil {
		if next == nil {
			switch {
			case last != nil && last.Parent() != parent:
				last = last.Parent()
				advance()
			case !action:
				action = true
				if parent != nil {
					next = head(parent.Actions())
				} else {
					_, rpcs, _ := a.modLists(mod)
					next = head(rpcs)
				}
			case !notif:
				notif = true
				if parent != nil {
					next = head(parent.Notifications())
				} else {
					_, _, ns := a.modLists(mod)
					next = head(ns)
				}
			default:
				return nil
			}
			continue
		}
		switch next.Kind() {
		case KindCase:
			intoCase(next)
		case KindChoice:
			if child(next) == nil {
				next = a.next(next)
			} else {
				intoCase(child(next))
			}
		case KindInput, KindOutput:
			if (next.Kind() == KindOutput) == output {
				next = child(next)
			} else {
				next = a.next(next)
			}
		default:
			return next
		}
	}
	return nil
}
