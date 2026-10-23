// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/validation.c (lyd_validate_node_when, lyd_validate_when_false,
// lyd_validate_unres_when, lyd_validate_unres (when and type parts), lyd_validate_dummy_when,
// lyd_validate_must), src/tree_data_common.c (lyd_value_validate_incomplete),
// src/plugins_types.c (lyplg_type_resolve_leafref, _get_target_path) and src/path.c
// (ly_path_eval_partial), and src/xpath.c (lyxp_eval: the tree check) (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

// DefaultMaxXPathSteps is the default cumulative XPath budget of one Parse or Validate (U-0042).
const DefaultMaxXPathSteps int64 = 1 << 30

// errIncomplete is LY_EINCOMPLETE of an evaluation that met a node whose when is unresolved.
var errIncomplete = errors.New("data: when not resolved yet")

// xpathBudget is the cumulative XPath step budget and cancellation of one operation.
type xpathBudget struct {
	ctx   context.Context
	max   int64 // 0 = DefaultMaxXPathSteps
	steps int64
}

// eval evaluates e for the context node ctx (nil: the document root) over tree t, charging the
// steps; a budget overrun or a cancellation aborts (never a diagnostic).
func (vc *valCtx) xpathSet() *schema.Set {
	if vc.xpathContext != nil {
		return vc.xpathContext
	}
	return vc.t.context()
}

func (vc *valCtx) eval(e *xpath.Expr, ctx *Node, root xpath.RootKind, ignoreWhen bool, owner ...*schema.Set) (xpath.Result, error) {
	b := &vc.budget
	if b.ctx != nil {
		if err := b.ctx.Err(); err != nil {
			return xpath.Result{}, err
		}
	}
	// lyxp_eval's tree check, xpath.c:9947-9960; an opaque first node passes
	// (lysc_data_parent(NULL)).
	if first := vc.t.top.first(); first != nil && first.schema != nil && first.schema.DataParent() != nil {
		return xpath.Result{}, &xpath.Error{Err: "LY_EINVAL", Msg: fmt.Sprintf(
			"Data node \"%s\" has no parent but is not instance of a top-level schema node.", first.Name())}
	}
	limit := b.max
	if limit <= 0 {
		limit = DefaultMaxXPathSteps
	}
	left := limit - b.steps
	per := int64(xpath.DefaultMaxSteps)
	if left < per {
		per = left
	}
	if per <= 0 {
		return xpath.Result{}, fmt.Errorf("%w: more than %d XPath steps", yang.ErrBudget, limit)
	}
	set := vc.xpathSet()
	if len(owner) > 0 && owner[0] != nil {
		set = owner[0]
	} else if vc.xpathContext == nil && ctxOf(ctx) != nil {
		set = ctxOf(ctx)
	}
	r, err := e.EvalTo(xpath.EvalContext{Ctx: b.ctx, Node: wrap(set, ctx), Tree: vc.topNodes(), Root: root,
		IgnoreWhen: ignoreWhen, Schema: info{set}, Deref: vc.deref, Log: vc.xpathLog,
		MaxSteps: int(per), Vars: vc.vars}, vc.to)
	// A failed nested printer returns its own ValidationError. Route its log now;
	// queryErr separately preserves the return code.
	var nested *ValidationError
	if errors.As(err, &nested) {
		vc.xpathLog(err)
	}
	b.steps += r.Steps
	switch {
	case errors.Is(err, xpath.ErrBudget):
		// the cumulative budget, or the per-evaluation cap xpath.DefaultMaxSteps (U-0042)
		return r, fmt.Errorf("%w: XPath step budget (%d per evaluation, %d per operation): %w",
			yang.ErrBudget, xpath.DefaultMaxSteps, limit, err)
	case errors.Is(err, xpath.ErrIncomplete):
		return r, errIncomplete
	}
	return r, err
}

// xpathLog copies diagnostics from a nested data printer into lyxp_eval's operation log. The
// printer status is handled independently by the evaluator and queryErr.
func (vc *valCtx) xpathLog(err error) {
	var internal *xpath.Error
	if errors.As(err, &internal) && internal.Err == "LY_EINT" {
		if vc.log != nil {
			_ = vc.log.logErr(internal.Err, "%s", internal.Msg)
		}
		return
	}
	var printed interface{ xpathPrintDiagnostics() []printDiagnostic }
	if errors.As(err, &printed) {
		var successful *xpathPrintNotice
		notice := errors.As(err, &successful)
		for _, item := range printed.xpathPrintDiagnostics() {
			vc.xpathDiagnostic(item, notice)
		}
		return
	}
	var ve *ValidationError
	if errors.As(err, &ve) && vc.log != nil {
		vc.log.diags = append(vc.log.diags, ve.Diags...)
	}
}

// xpathDiagnostic routes a nested printer item exactly as the context-local libyang log does.
// A foreign item is retained separately from the operation log, including on a fatal printer
// failure, so collecting the evaluating context cannot consume it.
func (vc *valCtx) xpathDiagnostic(item printDiagnostic, notice bool) {
	if vc.log == nil {
		return
	}
	if item.set == nil || item.set == vc.log.set {
		if notice {
			vc.log.notice(item.diag)
		} else {
			vc.log.diags = append(vc.log.diags, item.diag)
		}
		return
	}
	if vc.t != nil && vc.t.contextLogsEnabled.Load() {
		vc.t.contextLogMu.Lock()
		vc.t.contextLogs = append(vc.t.contextLogs, diffLogEntry(item))
		vc.t.contextLogMu.Unlock()
	}
}

// topNodes is the accessible tree of the evaluations: the live top-level siblings, rebuilt only
// when the top level changed (siblings.gen) and charged to the step budget then, one step per
// node.
func (vc *valCtx) topNodes() []xpath.Node {
	if vc.top != nil && vc.topGen == vc.t.top.gen {
		return vc.top
	}
	vc.top = vc.top[:0]
	for n := range vc.t.top.all() {
		vc.top = append(vc.top, wrap(vc.xpathSet(), n)) // dead nodes are WhenFalse (xn.When)
	}
	vc.topGen = vc.t.top.gen
	vc.topBuilds++
	vc.budget.steps += int64(len(vc.top)) + 1
	return vc.top
}

// xpathErr logs an evaluation error as lyxp_eval does: LOGVAL_DXPATH at the current node cur
// (the must's node, the when's context node, the leafref), LOGERR errors without a location.
func (vc *valCtx) xpathErr(err error, cur *Node) error {
	var probeErr *xpathProbeError
	if errors.As(err, &probeErr) {
		return vc.log.storeErr(nil, probeErr.schema, probeErr.diag)
	}
	var xe *xpath.Error
	if errors.As(err, &xe) {
		if xe.VECode == "" {
			return vc.log.logErr(xe.Err, "%s", xe.Msg)
		}
		_ = vc.log.item(cur, nil, false, xe.Err, codeOf(xe.VECode), "", xe.Msg)
		return errLogged
	}
	return err // budget, cancellation, errIncomplete
}

// evalErrMsg reports whether err is a logged lyxp_eval failure (an XPath error or a predicate
// key re-store failure, xpathProbeError) rather than a budget or cancellation error, and its
// message (ly_err_last).
func evalErrMsg(err error) (string, bool) {
	var probeErr *xpathProbeError
	if errors.As(err, &probeErr) {
		return probeErr.diag.Msg, true
	}
	var xe *xpath.Error
	if errors.As(err, &xe) {
		return xe.Msg, true
	}
	return "", false
}

// whenOf is lyd_validate_node_when: the whens of the schema node and of its choice/case
// ancestors, each on its context node (the node itself or its parent); the first false one, or
// errIncomplete when one depends on an unresolved when.
func (vc *valCtx) whenOf(n *Node, sn *schema.Node, root *xpath.RootKind) (*schema.When, error) {
	for s := sn; ; {
		for _, w := range s.Whens {
			ctx := n
			if w.ContextNode != sn {
				ctx = n.parent
			}
			e, ok := w.Compiled.(*xpath.Expr)
			if !ok {
				return nil, fmt.Errorf("data: when %q of %s is not compiled", w.Src, sn.LogPath())
			}
			rk := rootType(ctx)
			if root != nil {
				rk = *root
			}
			r, err := vc.eval(e, ctx, rk, vc.ignoreWhen, ctxOf(n))
			if err != nil {
				return nil, vc.xpathErr(err, ctx)
			}
			if !truthy(r) {
				return w, nil
			}
		}
		s = s.Parent
		if s == nil || s.Kind != schema.Case && s.Kind != schema.Choice {
			return nil, nil
		}
	}
}

// truthy is lyxp_set_cast to boolean.
func truthy(r xpath.Result) bool {
	switch r.Type {
	case xpath.Boolean:
		return r.Bool
	case xpath.Number:
		return r.Num != 0 && r.Num == r.Num // NaN is false
	case xpath.String:
		return r.Str != ""
	}
	return len(r.Nodes) > 0
}

// whenPass is the state of one lyd_validate_unres_when pass. Removals from node_when are
// tombstones (nil) compacted once at the end of the pass, which keeps the order of
// ly_set_rm_index_ordered without moving the queue per removal; live counts the entries left.
// pos maps a queued node to its live indices in increasing order (a node is queued once per
// lyd_parser_set_data_flags call, so the JSON parser queues a node with metadata several times
// and ly_set_contains finds the first entry), built when a when-false subtree needs it.
type whenPass struct {
	vc           *valCtx
	live         int
	pos          map[*Node][]int
	dead         []*Node // WhenTrue nodes deleted by this pass, unlinked at its end
	builds       int     // pos maps built (one per pass at most; work tests)
	initialWhen  []*Node
	initialFlags map[*Node]Flags
	initialTypes nodeSet
}

// drop is ly_set_rm_index_ordered of entry i (a tombstone).
func (p *whenPass) drop(i int) {
	q := p.vc.nodeWhen
	n := q.items[i]
	q.items[i] = nil
	p.live--
	if l := p.pos[n]; l != nil {
		p.pos[n] = slices.DeleteFunc(l, func(j int) bool { return j == i })
	}
}

// whenFalse is lyd_validate_when_false for the entry i of n: the node stays, flagged WhenFalse;
// its subtree leaves node_types and the first entry of each descendant leaves node_when. When
// that removed anything, the pass goes on from the first entry of n (libyang refreshes its index
// with ly_set_contains): that entry is the one resolved next, and the entries between it and i
// wait for the next pass.
func (p *whenPass) whenFalse(n *Node, i int) int {
	vc := p.vc
	n.touch()
	n.flags |= FlagWhenFalse
	vc.dropTypes(n)
	if p.live <= 1 {
		return i
	}
	if p.pos == nil {
		p.builds++
		p.pos = make(map[*Node][]int, p.live)
		for j, q := range vc.nodeWhen.items {
			if q != nil {
				p.pos[q] = append(p.pos[q], j)
			}
		}
	}
	removed := false
	for d := range n.All() {
		if l := p.pos[d]; d != n && len(l) > 0 {
			p.drop(l[0])
			removed = true
		}
	}
	if l := p.pos[n]; removed && len(l) > 0 {
		return l[0]
	}
	return i
}

// del is the autodelete of a WhenTrue node whose when became false. The node is unlinked at the
// end of the pass (one compaction per sibling list instead of one per node); until then it is
// dead: the XPath adapters do not see it, exactly as if it was gone already.
// A dead node reads as WhenFalse to XPath (xn.When), so no view needs rebuilding. A node queued
// twice (the JSON parser queues a node once per metadata member) is deleted once.
func (p *whenPass) del(n *Node) {
	if n.flags&flagDead != 0 {
		return
	}
	n.touch()
	n.flags |= flagDead
	p.dead = append(p.dead, n)
}

// unresWhen is lyd_validate_unres_when: one pass over node_when from the end. A resolved node
// leaves the queue (order kept); a false when deletes a WhenTrue node silently, warns for
// operational data, else is an error (the node kept as WhenFalse under multi-error).
func (vc *valCtx) unresWhen() (rc error) {
	p := &whenPass{vc: vc, live: vc.nodeWhen.len(), initialWhen: slices.Clone(vc.nodeWhen.items),
		initialFlags: map[*Node]Flags{}}
	for _, n := range p.initialWhen {
		if n != nil {
			p.initialFlags[n] = n.flags
		}
	}
	if vc.nodeTypes != nil {
		var positions map[*Node]int
		if vc.nodeTypes.pos != nil {
			positions = make(map[*Node]int, len(vc.nodeTypes.pos))
			for n, at := range vc.nodeTypes.pos {
				positions[n] = at
			}
		}
		p.initialTypes = nodeSet{items: slices.Clone(vc.nodeTypes.items), pos: positions, scans: vc.nodeTypes.scans}
	}
	defer func() {
		if err := p.finish(); err != nil {
			rc = err
		}
	}()
	for i := vc.nodeWhen.len() - 1; i >= 0; i-- {
		n := vc.nodeWhen.items[i]
		if n == nil {
			continue // removed with a when-false ancestor
		}
		if n.flags&flagDead != 0 {
			p.drop(i) // a second entry of a node deleted by this pass
			continue
		}
		w, err := vc.whenOf(n, n.schema, nil)
		switch {
		case errors.Is(err, errIncomplete):
			continue // stays queued
		case err != nil:
			if rc = err; vc.stop(err) {
				return rc
			}
			continue
		case w == nil:
			n.touch()
			n.flags = n.flags&^FlagWhenFalse | FlagWhenTrue
		case n.flags&FlagWhenTrue != 0:
			p.del(n) // nested queued nodes cannot exist (libyang asserts it)
		case vc.opts.Operational:
			vc.log.warn("When condition \"%s\" not satisfied.", w.Src)
		default:
			if vc.opts.MultiError {
				i = p.whenFalse(n, i)
			}
			err := vc.log.val(n, "", ly.Data, "When condition \"%s\" not satisfied.", w.Src)
			if rc = err; vc.stop(err) {
				p.drop(i)
				return rc
			}
		}
		p.drop(i) // resolved
	}
	return rc
}

// finish compacts node_when and unlinks the nodes the pass deleted.
func (p *whenPass) finish() error {
	if err := p.vc.t.preflightUnlinkAll(p.dead); err != nil {
		for n, flags := range p.initialFlags {
			n.touch()
			n.flags = flags
		}
		p.vc.nodeWhen.items, p.vc.nodeWhen.pos = p.initialWhen, nil
		if p.vc.nodeTypes != nil {
			*p.vc.nodeTypes = p.initialTypes
		}
		return err
	}
	for _, n := range p.dead {
		n.touch()
		n.flags &^= flagDead
		if p.vc.diff != nil {
			_ = p.vc.diff(n, diffDelete)
		}
		p.vc.dropTypes(n)
	}
	q := p.vc.nodeWhen
	live := q.items[:0]
	for _, n := range q.items {
		if n != nil {
			live = append(live, n)
		}
	}
	clear(q.items[len(live):])
	q.items, q.pos = live, nil
	p.vc.posBuilds += p.builds
	for _, n := range p.dead {
		n.touch()
		n.flags &^= flagDead
	}
	if err := p.vc.t.unlinkAll(p.dead); err != nil {
		return err
	}
	for _, n := range p.dead {
		freeSubtreeLinks(p.vc.t.context(), n) // lyd_free_tree
	}
	return nil
}

// unres is the when and type part of lyd_validate_unres: when passes while the queue shrinks,
// then the incomplete values from the end of node_types.
func (vc *valCtx) unres() error {
	var rc error
	if vc.nodeWhen != nil {
		for {
			prev := vc.nodeWhen.len()
			if err := vc.unresWhen(); err != nil {
				if rc = err; vc.stop(err) {
					return rc
				}
			}
			if vc.nodeWhen.len() >= prev {
				break
			}
		}
		*vc.nodeWhen = nodeSet{} // left only after an error (ly_set_erase)
	}
	if vc.nodeTypes != nil {
		for i := vc.nodeTypes.len() - 1; i >= 0; i-- {
			n := vc.nodeTypes.items[i]
			if err := vc.validateIncomplete(n); err != nil {
				if rc = err; vc.stop(err) {
					return rc
				}
			}
			vc.nodeTypes.rmIndex(i)
		}
	}
	if vc.metaTypes != nil && len(*vc.metaTypes) > 0 {
		q := *vc.metaTypes
		owners := vc.metaOwners(q)
		for i := len(q) - 1; i >= 0; i-- {
			if n := owners[q[i]]; n != nil { // nil: a default="true" metadata the parser dropped
				if err := vc.validateMetaIncomplete(n, q[i]); err != nil {
					if rc = err; vc.stop(err) {
						*vc.metaTypes = q[:i]
						return rc
					}
				}
			}
		}
		*vc.metaTypes = q[:0]
	}
	return rc
}

// metaOwners maps each queued metadata to the node that holds it.
// ponytail: one walk of the tree per drain (the XML parser attaches a metadata list to the node it
// creates next, so the queue cannot record the owner); a parent field on meta if this shows up.
func (vc *valCtx) metaOwners(q []*meta) map[*meta]*Node {
	want := make(map[*meta]bool, len(q))
	for _, m := range q {
		want[m] = true
	}
	owners := make(map[*meta]*Node, len(q))
	for top := range vc.t.top.all() {
		for n := range top.All() {
			for _, m := range n.meta {
				if want[m] {
					owners[m] = n
				}
			}
		}
	}
	return owners
}

// validateMetaIncomplete is the meta_types part of lyd_validate_unres: lyd_value_validate_incomplete
// of the metadata m of n, errors logged at n.
func (vc *valCtx) validateMetaIncomplete(n *Node, m *meta) error {
	ant := annotation(m.mod, m.name)
	if ant == nil {
		return nil
	}
	v, err := vc.incomplete(n, ant.Type, m.value)
	if err == nil {
		n.touch()
		m.value = v
	}
	return err
}

// incomplete is lyd_value_validate_incomplete: the tree-time checks of the value v of type t
// whose context node is n, errors logged at n.
func (vc *valCtx) incomplete(n *Node, t *schema.Type, val types.Value) (types.Value, error) {
	tt := &typeTree{vc: vc, n: n}
	v, d := types.ValidateTree(t, val, tt)
	if tt.err != nil {
		return val, tt.err // budget or cancellation
	}
	if d != nil {
		for _, probe := range tt.probes {
			_ = vc.xpathErr(probe, n)
		}
		return val, vc.log.item(n, nil, false, d.RC(), codeOf(d.Code), d.AppTag, d.Msg)
	}
	return v, nil
}

// validateIncomplete is lyd_value_validate_incomplete for the value of n.
func (vc *valCtx) validateIncomplete(n *Node) error {
	v, err := vc.incomplete(n, n.schema.Type, n.value)
	if err != nil {
		return err
	}
	if v.Canonical() != n.value.Canonical() || !types.Equal(v, n.value) {
		target := n
		if n.schema.Kind != schema.LeafList {
			target = n.parent
		}
		if err := vc.t.work.checkChangedIndex(target, n, &v, 0); err != nil {
			return err
		}
		// Tree validation changes a union member without lyd_hash. Preserve the
		// stored list hash, but invalidate its running prefix for the next native
		// rehash (plugins_types/union.c:522-573; tree_data.c:836-842).
		if n.isKey() && n.parent != nil && n.keySlot != 0 {
			n.parent.touch()
			n.parent.lkeyDirty = true
		}
		n.touch()
		n.value = v
		if sib := n.siblingsOf(); sib != nil {
			sib.hashReindex(n)
		}
		reindexComparedParent(n)
	} else {
		n.touch()
		n.value = v
	}
	return nil
}

// typeTree is types.Tree for the node n.
type typeTree struct {
	vc     *valCtx
	n      *Node
	err    error   // a budget or cancellation error met on the way
	probes []error // value-probe diagnostics, committed only if validation fails
	// readOnly skips the leafref linking (printing must not change the tree, D-0128)
	readOnly bool
}

// LeafrefTarget is lyplg_type_resolve_leafref without the targets: the path with a value
// predicate (or the plain path when the value has both quote kinds, then compared), evaluated
// ignoring whens.
func (tt *typeTree) LeafrefTarget(t *schema.Type, v types.Value) (bool, error) {
	found, targets, err := tt.vc.leafrefTargets(tt.n, t, v)
	if err != nil {
		if errors.Is(err, yang.ErrBudget) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			tt.err = err
			return false, nil
		}
		tt.probes = append(tt.probes, err)
		var xe *xpath.Error
		if errors.As(err, &xe) {
			return false, errors.New(xe.Msg)
		}
		return false, err
	}
	if found && ctxOf(tt.n) != nil && ctxOf(tt.n).LeafrefLinking && !tt.readOnly {
		// lyplg_type_validate_tree_leafref: a resolved require-instance leafref is linked with
		// its targets (lyd_link_leafref_node)
		for _, target := range targets {
			if err := linkLeafrefNodeChecked(target, tt.n); err != nil {
				tt.err = err
				return false, nil
			}
		}
	}
	return found, nil
}

// InstanceExists is ly_path_eval: the instance-identifier target, found segment by segment.
func (tt *typeTree) InstanceExists(p types.Path) bool {
	n := tt.vc.pathEval(p)
	if tt.vc.probes != nil && tt.vc.probes.work.hashErr != nil {
		tt.err = tt.vc.probes.work.hashErr
	}
	return n != nil
}

// lrefTemplate is what lyplg_type_resolve_leafref_get_target_path derives from a path once:
// whether the value goes into a key predicate of the list before the last step; disabled when
// the path no longer compiles (its target was removed by if-feature: success).
type lrefTemplate struct {
	disabled bool
	listKey  bool
	head     string // the path without "/key" (listKey)
	key      string
}

type lrefKey struct {
	t *schema.Type
	s *schema.Node
}

func (vc *valCtx) template(t *schema.Type, sn *schema.Node) (*lrefTemplate, error) {
	k := lrefKey{t, sn}
	if tp, ok := vc.lrefs[k]; ok {
		return tp, nil
	}
	tp := &lrefTemplate{}
	e, msg := lyxp.Lex(t.Path)
	if msg != "" {
		return nil, errors.New(msg)
	}
	p, _, perr := types.CompileLeafref(sn, e, t.Prefixes, sn.InOutput(), t.PathExtended)
	if perr != nil {
		tp.disabled = true
	} else if last := p[len(p)-1].Node; last.IsKey() && len(p) >= 2 && p[len(p)-2].Node.Kind == schema.List {
		u := len(e.Toks)
		if u >= 3 && e.Toks[u-1] == lyxp.TokNameTest && e.Toks[u-2] == lyxp.TokOperPath && e.Toks[u-3] == lyxp.TokNameTest {
			tp.listKey = true
			tp.head = e.Src[:e.Pos[u-3]+e.Len[u-3]]
			tp.key = e.Src[e.Pos[u-1]:]
		}
	}
	if vc.lrefs == nil {
		vc.lrefs = map[lrefKey]*lrefTemplate{}
	}
	vc.lrefs[k] = tp
	return tp, nil
}

// leafrefTargets is lyplg_type_resolve_leafref: whether the leafref path of t, evaluated for the
// node n with the value v, finds a target (with the value predicate: any node; with the plain path:
// one of the same realtype and an equal value), and the targets of the same realtype and an equal
// value (deref()).
func (vc *valCtx) leafrefTargets(n *Node, t *schema.Type, v types.Value) (bool, []*Node, error) {
	val := v.Canonical()
	src := t.Path
	exact := !strings.Contains(val, `"`) || !strings.Contains(val, "'")
	if exact {
		tp, err := vc.template(t, n.schema)
		if err != nil {
			return false, nil, err
		}
		if tp.disabled {
			return true, nil, nil // the target was disabled and removed: success, no targets
		}
		q := "'"
		if strings.Contains(val, "'") {
			q = `"`
		}
		if tp.listKey {
			src = tp.head + "[" + tp.key + "=" + q + val + q + "]/" + tp.key
		} else {
			src = t.Path + "[.=" + q + val + q + "]"
		}
	}
	def := ""
	if m := t.Prefixes[""]; m != nil {
		def = m.Name
	}
	e, err := xpath.Compile(src, lrefNS{t.Prefixes, def})
	if err != nil {
		return false, nil, err
	}
	r, err := vc.eval(e, n, xpath.RootAll, true, ctxOf(n))
	if err != nil {
		return false, nil, err
	}
	var out []*Node
	for _, x := range r.Nodes {
		if m, ok := x.(xn); ok && m.n.isTerm() && m.n.value.Type() == v.Type() && types.Equal(m.n.value, v) {
			out = append(out, m.n)
		}
	}
	if exact {
		return len(r.Nodes) > 0, out, nil // the predicate selected them: no match check (i = 0)
	}
	return len(out) > 0, out, nil
}

// lrefNS binds a leafref path's prefixes (schema-resolved: "" is the instantiating module).
type lrefNS struct {
	p   schema.NSCtx
	def string
}

func (l lrefNS) Resolve(prefix string) (string, bool) {
	if m := l.p[prefix]; m != nil && prefix != "" {
		return m.Name, true
	}
	return "", false
}

func (l lrefNS) ResolveIdentity(prefix string) (xpath.Ident, bool) {
	m := l.p[prefix]
	if m == nil {
		return xpath.Ident{}, false
	}
	return xpath.Ident{Module: m.Name, Revision: m.Revision, Exact: true}, true
}

func (l lrefNS) Prefix(module string) string { return module }
func (l lrefNS) Default() string             { return l.def }

// pathEval is ly_path_eval_partial for a full match: the node at the end of p, or nil.
func (vc *valCtx) pathEval(p types.Path) *Node {
	if vc.probes == nil {
		vc.probes = newTree(vc.t.set)
	}
	sib := &vc.t.top
	var node *Node
	for _, seg := range p {
		node = nil
		if len(seg.Preds) == 0 {
			node = vc.t.schemaLookup(sib, seg.Node)
		} else {
			switch pr := seg.Preds[0]; pr.Kind {
			case types.PredPosition:
				// the instance at that position; a position beyond the instances (strtoull
				// saturates huge numbers, as ParseUint does) selects nothing
				if opChild(seg.Node) || sib.schemaUnordered {
					run := vc.t.opRun(sib, seg.Node)
					if pr.Position > 0 && pr.Position <= uint64(len(run)) {
						node = run[pr.Position-1]
					}
				} else if pr.Position > 0 {
					pos := pr.Position
					for c := range vc.t.instances(sib, seg.Node) {
						pos--
						if pos == 0 {
							node = c
							break
						}
					}
				}
			case types.PredLeafList:
				target := newTerm(seg.Node, pr.Value, &vc.probes.work)
				if vc.probes.work.hashErr != nil {
					return nil
				}
				node = vc.t.findFirst(sib, target)
			case types.PredKey: // ly_path_eval → ly_path_eval_partial: lyd_create_list, store-only
				// a key that does not re-store finds nothing: libyang's instance-identifier
				// validation reports "required instance not found" then (ut-new/path-08)
				if target, d, _, err := vc.probes.lookupList(seg); d == nil && err == nil {
					node = vc.t.findFirst(sib, target)
				}
			}
		}
		if node == nil || node.flags&flagDead != 0 {
			return nil
		}
		sib = &node.kids
	}
	return node
}

// deref resolves deref() (xpath_deref): a leafref's targets (lyplg_type_resolve_leafref with
// targets), or an instance-identifier's target; an unresolved one is libyang's LOGERR LY_EINVAL,
// returned as an *xpath.Error that aborts the evaluation.
func (vc *valCtx) deref(x xpath.Node) ([]xpath.Node, error) {
	m, ok := x.(xn)
	if !ok || !m.n.isTerm() {
		return nil, nil
	}
	return vc.derefType(m.n, m.n.value, m.n.schema.Type, true)
}

// errDerefMember is an unresolved union member of deref(): not logged (log = 0), the next member
// is tried.
var errDerefMember = errors.New("data: deref member not resolved")

// derefType is xpath_deref_type: the targets of the value v of n for its type t, or LY_EINVAL
// (with the message when log, else errDerefMember) when a leafref, instance-identifier or union
// does not resolve. Other types resolve to no nodes. A union tries its members in order with the
// selected member's value (value.subvalue->value) and takes the first that resolves.
func (vc *valCtx) derefType(n *Node, v types.Value, t *schema.Type, log bool) ([]xpath.Node, error) {
	fail := func(format string, a ...any) error {
		if !log {
			return errDerefMember
		}
		return &xpath.Error{Err: "LY_EINVAL", Msg: fmt.Sprintf(format, a...)}
	}
	switch t.Base {
	case schema.Leafref:
		found, nodes, err := vc.leafrefTargets(n, t, v)
		if msg, ok := evalErrMsg(err); ok {
			_ = vc.xpathErr(err, n) // lyxp_eval logs it, then the plugin message names it
			return nil, fail("Invalid leafref value \"%s\" - XPath evaluation error (%s).", v.Canonical(), msg)
		}
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fail("Invalid leafref value \"%s\" - no target instance \"%s\" with the same value.",
				v.Canonical(), t.Path)
		}
		out := make([]xpath.Node, len(nodes))
		for i, target := range nodes {
			out[i] = wrap(ctxOf(target), target)
		}
		return out, nil
	case schema.InstanceID:
		target := vc.pathEval(v.Path())
		if vc.probes != nil && vc.probes.work.hashErr != nil {
			return nil, vc.probes.work.hashErr
		}
		if target == nil {
			return nil, fail("Invalid instance-identifier \"%s\" value - required instance not found.",
				n.value.Canonical())
		}
		return []xpath.Node{wrap(vc.xpathSet(), target)}, nil
	case schema.Union:
		if u := v.Union(); u != nil {
			mv, _ := u.Member()
			for _, mt := range t.Union {
				if out, err := vc.derefType(n, mv, mt, false); !errors.Is(err, errDerefMember) {
					return out, err
				}
			}
		}
		return nil, fail("Invalid leafref or instance-identifier \"%s\" value - required instance not found.",
			n.value.Canonical())
	}
	return nil, nil
}

// validateMust is lyd_validate_must: every must of the node (of an rpc or action: of its input,
// or of its output for a reply), false ones reported with error-message/error-app-tag (warnings
// for operational data).
func (vc *valCtx) validateMust(n *Node) error {
	var rc error
	musts := n.schema.Musts
	if k := n.schema.Kind; k == schema.RPC || k == schema.Action {
		switch {
		case vc.op.rpc || vc.op.action:
			musts = n.schema.Children[0].Musts // input
		case vc.op.reply:
			musts = n.schema.Children[1].Musts // output
		default:
			_ = vc.log.logErr("LY_EINT", "Internal error (%s:%d).", "validation.c", 1726)
			return fatalRC("LY_EINT")
		}
	}
	for _, m := range musts {
		e, ok := m.Compiled.(*xpath.Expr)
		if !ok {
			return fmt.Errorf("data: must %q of %s is not compiled", m.Src, n.schema.LogPath())
		}
		r, err := vc.eval(e, n, rootType(n), vc.ignoreWhen)
		if errors.Is(err, errIncomplete) {
			return vc.log.logErr("LY_EINCOMPLETE",
				"Must \"%s\" depends on a node with a when condition, which has not been evaluated.", m.Src)
		}
		if err != nil {
			return vc.xpathErr(err, n)
		}
		if truthy(r) {
			continue
		}
		if vc.opts.Operational {
			if m.Msg != "" {
				vc.log.warn("%s", m.Msg)
			} else {
				vc.log.warn("Must condition \"%s\" not satisfied.", m.Src)
			}
			continue
		}
		tag := m.AppTag
		if tag == "" {
			tag = "must-violation"
		}
		if m.Msg != "" {
			err = vc.log.val(n, tag, ly.Data, "%s", m.Msg)
		} else {
			err = vc.log.val(n, tag, ly.Data, "Must condition \"%s\" not satisfied.", m.Src)
		}
		if rc = err; vc.stop(err) {
			return rc
		}
	}
	return rc
}

// dummyWhen is lyd_validate_dummy_when: the whens of an absent node sn under parent (nil: the top
// level), evaluated on an opaque stand-in linked at its place; the first false one.
func (vc *valCtx) dummyWhen(parent *Node, sn *schema.Node) (*schema.When, error) {
	// lyd_validate_dummy_when evaluates without LYXP_IGNORE_WHEN, also in operation validation
	iw := vc.ignoreWhen
	vc.ignoreWhen = false
	defer func() { vc.ignoreWhen = iw }()
	dummy := newOpaque(opaque{Name: sn.Name, ModuleNS: sn.Module.Name, Format: types.FormatJSON, set: sn.Module.Owner})
	if err := vc.t.insert(parent, dummy, insertDefault); err != nil {
		return nil, err
	}
	defer unlink(dummy)
	root := xpath.RootAll
	if sn.Config && !inOperation(sn) {
		root = xpath.RootConfig
	}
	w, err := vc.whenOf(dummy, sn, &root)
	if errors.Is(err, errIncomplete) {
		if vc.opts.MultiError {
			return nil, nil // cannot evaluate properly, ignored
		}
		_ = vc.log.logErr("LY_EINT", "Internal error (%s:%d).", "validation.c", 1132) // LOGINT
		return nil, fatalRC("LY_EINT")
	}
	return w, err
}
