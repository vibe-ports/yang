// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/validation.c (lyd_validate, lyd_validate_all, lyd_validate_tree,
// lyd_validate_final_r, lyd_validate_siblings_schema_r, lyd_validate_mandatory,
// lyd_validate_minmax, lyd_validate_unique, lyd_val_uniq_list_equal, lyd_val_uniq_find_leaf,
// lyd_validate_obsolete), src/parser_common.c (lyd_parser_validate_new_implicit) and
// src/tree_data_common.c (lyd_owner_module, lyd_first_module_sibling, lyd_mod_next_module,
// lyd_data_next_module) (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// ownerModule is lyd_owner_module: the module of the top-level ancestor (of its schema node, or of
// a top-level opaque node's namespace or module name).
func ownerModule(set *schema.Set, n *Node) *schema.Module {
	for n.schema == nil && n.parent != nil {
		n = n.parent
	}
	if n.schema == nil {
		if n.opaq.ModuleNS == "" {
			return nil
		}
		if n.opaq.Format == types.FormatXML {
			return set.ByNamespace(n.opaq.ModuleNS)
		}
		return set.Implemented(n.opaq.ModuleNS)
	}
	s := n.schema
	for s.Parent != nil {
		s = s.Parent
	}
	return s.Module
}

// topList is the top level as libyang's sibling list: the schema nodes, then the opaque nodes.
func (vc *valCtx) topList() []*Node {
	t := &vc.t.top
	if len(t.opq) == 0 {
		return t.list
	}
	out := make([]*Node, 0, t.len())
	return append(append(out, t.list...), t.opq...)
}

// firstModuleSibling is lyd_first_module_sibling: the index of the first top-level node of mod,
// or start unchanged when the module has no data.
func (vc *valCtx) firstModuleSibling(top []*Node, start int, mod *schema.Module) int {
	if start >= len(top) {
		return start
	}
	first := start
	cmp := 1
	if own := ownerModule(vc.t.set, top[first]); own != nil {
		cmp = strings.Compare(own.Name, mod.Name)
	}
	if cmp > 0 { // there may be some preceding data
		for first > 0 {
			first--
			if ownerModule(vc.t.set, top[first]) == mod {
				cmp = 0
				break
			}
		}
	}
	if cmp == 0 { // there may be some preceding data of this module
		for first > 0 && ownerModule(vc.t.set, top[first-1]) == mod {
			first--
		}
		return first
	}
	if cmp < 0 { // there may be some following data
		for i := first; i < len(top); i++ {
			if ownerModule(vc.t.set, top[i]) == mod {
				return i
			}
		}
	}
	return start
}

// modules lists the modules lyd_validate traverses: the implemented ones in context order
// (lyd_mod_next_module), or with Present those of the top-level data in tree order
// (lyd_data_next_module, which stops at a top-level node of no module).
func (vc *valCtx) modules() []*schema.Module {
	var out []*schema.Module
	if !vc.opts.Present {
		for _, m := range vc.t.set.Modules {
			if m.Implemented {
				out = append(out, m)
			}
		}
		return out
	}
	top := vc.topList()
	for i := 0; i < len(top); {
		mod := ownerModule(vc.t.set, top[i])
		if mod == nil {
			break
		}
		out = append(out, mod)
		for i < len(top) && ownerModule(vc.t.set, top[i]) == mod {
			i++
		}
	}
	return out
}

func (vc *valCtx) implOpts() implOpts {
	var o implOpts
	if vc.opts.NoState {
		o |= implNoState
	}
	if vc.opts.NoDefaults {
		o |= implNoDefaults
	}
	return o
}

// validate is lyd_validate over the whole tree: per module the new top-level nodes, their
// implicit nodes (validateSubtree: lyd_validate_tree of every top-level node of the module, else
// the queues filled by the parser), the unres queues; then the final validation of every module.
// The queues are shared by all modules: on the parse path the first module drains everything the
// parser queued (design 07 §1.5).
func (vc *valCtx) validate(validateSubtree bool) error {
	var rc error
	mods := vc.modules()
	defer func() { vc.modFirst = nil }()
	for _, mod := range mods {
		vc.modFirst = nil
		if err := vc.validateNew(nil, nil, mod); err != nil {
			if rc = err; vc.stop(err) {
				return rc
			}
		}
		if top := vc.topList(); len(top) > 0 {
			if i := vc.firstModuleSibling(top, 0, mod); i > 0 && i < len(top) && ownerModule(vc.t.set, top[i]) == mod {
				vc.modFirst = top[i]
			}
		}
		var err error
		if validateSubtree {
			// descendants are validated below: the top-level implicit nodes go into no queue
			w, ty := vc.nodeWhen, vc.nodeTypes
			vc.nodeWhen, vc.nodeTypes = nil, nil
			err = vc.newImplicit(nil, nil, mod, vc.implOpts())
			vc.nodeWhen, vc.nodeTypes = w, ty
		} else {
			err = vc.newImplicitR(nil, nil, mod, vc.implOpts())
		}
		if err != nil {
			return err
		}
		if validateSubtree {
			top := vc.topList()
			start := vc.firstModuleSibling(top, 0, mod)
			if vc.modFirst != nil {
				start = vc.modStart() // LY_LIST_FOR(*first2): a node misplaced before it is not walked
			}
			for i := start; i < len(top) && ownerModule(vc.t.set, top[i]) == mod; i++ {
				if err := vc.validateTree(top[i]); err != nil {
					if rc = err; vc.stop(err) {
						return rc
					}
				}
			}
		}
		if err := vc.unres(); err != nil {
			if rc = err; vc.stop(err) {
				return rc
			}
		}
	}
	for _, mod := range mods {
		top := vc.topList()
		first := vc.firstModuleSibling(top, 0, mod)
		if err := vc.finalR(nil, top[first:], nil, mod); err != nil {
			if rc = err; vc.stop(err) {
				return rc
			}
		}
	}
	return rc
}

// hasValidateTree reports whether a type plugin has a validate_tree callback: leafref,
// instance-identifier and union.
func hasValidateTree(t *schema.Type) bool {
	return t != nil && (t.Base == schema.Leafref || t.Base == schema.InstanceID || t.Base == schema.Union)
}

// validateTree is lyd_validate_tree: the subtree in pre-order; terms re-check their restrictions
// (validate_value) and queue tree-time checks, inner nodes get lyd_validate_new and their implicit
// children (into no queue: the walk reaches them), nodes with a when are queued.
func (vc *valCtx) validateTree(root *Node) error {
	var rc error
	for n := range root.All() {
		if n.schema == nil {
			continue // opaque nodes are not validated
		}
		if vc.metaTypes != nil {
			for _, m := range n.meta {
				if ant := annotation(m.mod, m.name); ant != nil && hasValidateTree(ant.Type) {
					*vc.metaTypes = append(*vc.metaTypes, m)
				}
			}
		}
		var err error
		switch {
		case n.isTerm():
			if d := types.Validate(n.schema.Type, n.value); d != nil {
				err = vc.log.item(n, nil, false, "LY_EVALID", codeOf(d.Code), d.AppTag, d.Msg)
			}
			if hasValidateTree(n.schema.Type) && vc.nodeTypes != nil {
				vc.nodeTypes.add(n)
			}
		case n.schema.Kind == schema.Container || n.schema.Kind == schema.List || n.schema.Kind == schema.RPC ||
			n.schema.Kind == schema.Action || n.schema.Kind == schema.Notification:
			err = vc.validateNew(n, nil, nil)
			if err == nil || !vc.stop(err) {
				w, ty := vc.nodeWhen, vc.nodeTypes
				vc.nodeWhen, vc.nodeTypes = nil, nil
				ierr := vc.newImplicit(n, nil, nil, vc.implOpts())
				vc.nodeWhen, vc.nodeTypes = w, ty
				if ierr != nil {
					return ierr
				}
			}
		}
		if err != nil {
			if rc = err; vc.stop(err) {
				return rc
			}
		}
		if hasWhen(n.schema) && vc.nodeWhen != nil {
			vc.nodeWhen.add(n)
		}
	}
	return rc
}

// finalR is lyd_validate_final_r: of the siblings (the top level of mod when parent is nil),
// opaque nodes and unexpected nodes are errors, the others get the obsolete warning and their
// musts (not when-false ones); then the schema-based checks of the sibling set; then each child
// set, depth first, and the NP-container default flag.
func (vc *valCtx) finalR(parent *Node, sibs []*Node, sparent *schema.Node, mod *schema.Module) error {
	var rc error
	for _, n := range sibs {
		var err error
		switch {
		case n.schema == nil:
			err = vc.log.opaqError(n)
		case n.parent == nil && mod != nil && ownerModule(vc.t.set, n) != mod:
		default:
			if inn := unexpected(n.schema, vc.opts.NoState); inn != "" {
				err = vc.log.val(n, "", ly.Data, "Unexpected data %s node \"%s\" found.", inn, n.schema.Name)
			} else if n.flags&FlagWhenFalse == 0 {
				vc.obsolete(n)
				err = vc.validateMust(n)
			}
		}
		if n.schema != nil && n.parent == nil && mod != nil && ownerModule(vc.t.set, n) != mod {
			break // all top-level data of this module checked
		}
		if err != nil {
			if rc = err; vc.stop(err) {
				return rc
			}
		}
	}
	if err := vc.siblingsSchema(parent, sibs, sparent, mod); err != nil {
		if rc = err; vc.stop(err) {
			return rc
		}
	}
	for _, n := range sibs {
		if n.schema == nil || n.parent == nil && mod != nil && ownerModule(vc.t.set, n) != mod {
			break // condensed condition of the previous loop
		}
		if n.flags&FlagWhenFalse != 0 {
			continue // logically non-existent: no children, no container default
		}
		if err := vc.finalR(n, slicesOf(&n.kids), n.schema, nil); err != nil {
			if rc = err; vc.stop(err) {
				return rc
			}
		}
		npContDfltSet(n)
	}
	return rc
}

// slicesOf is the sibling list of s as libyang links it: schema nodes, then opaque nodes.
func slicesOf(s *siblings) []*Node {
	if len(s.opq) == 0 {
		return s.list
	}
	return append(append(make([]*Node, 0, s.len()), s.list...), s.opq...)
}

// unexpected is the "no state/input/output/op data" check of lyd_validate_final_r for datastore
// data: the kind of node that must not be here, or "".
func unexpected(sn *schema.Node, noState bool) string {
	switch {
	case noState && configR(sn):
		return "state"
	case sn.Kind == schema.RPC:
		return "rpc"
	case sn.Kind == schema.Action:
		return "action"
	case sn.Kind == schema.Notification:
		return "notification"
	}
	return ""
}

// obsolete is lyd_validate_obsolete: a warning for an obsolete node (an inner one only with
// children) or one in an obsolete choice/case.
func (vc *valCtx) obsolete(n *Node) {
	for s := n.schema; s != nil; s = s.Parent {
		inner := s.Kind == schema.Container || s.Kind == schema.List || s.Kind == schema.RPC ||
			s.Kind == schema.Action || s.Kind == schema.Notification
		if s.Status == schema.Obsolete && (!inner || n.kids.len() > 0) {
			vc.log.warn("Obsolete schema node \"%s\" instantiated in data.", s.Name)
			return
		}
		if s.Parent == nil || s.Parent.Kind != schema.Choice && s.Parent.Kind != schema.Case {
			return
		}
	}
}

// siblingsSchema is lyd_validate_siblings_schema_r: mandatory choices (and the existing case,
// recursively), then per schema child: list min/max and unique, leaf-list min/max, mandatory
// leaves, containers and any nodes.
func (vc *valCtx) siblingsSchema(parent *Node, sibs []*Node, sparent *schema.Node, mod *schema.Module) error {
	var rc error
	keep := func(err error) bool {
		if err != nil {
			rc = err
			return !vc.stop(err)
		}
		return true
	}
	sib := vc.t.childrenOf(parent)
	gn := vc.getnextOf(sparent, mod, vc.output)
	for _, ch := range gn.choices {
		if vc.opts.NoState && configR(ch) {
			continue
		}
		if ch.Mandatory && !keep(vc.mandatory(parent, sib, ch)) {
			return rc
		}
		for _, cs := range ch.Children {
			if vc.firstData(sib, cs) != nil {
				if !keep(vc.siblingsSchema(parent, sibs, cs, mod)) {
					return rc
				}
				break
			}
		}
	}
	for _, sn := range gn.snodes {
		if vc.opts.NoState && configR(sn) {
			continue
		}
		switch {
		case sn.Kind == schema.List:
			if (sn.Min > 0 || sn.Max > 0) && !keep(vc.minmax(parent, sib, sn)) {
				return rc
			}
			if len(sn.Uniques) > 0 && !keep(vc.unique(sib, sn)) {
				return rc
			}
		case sn.Kind == schema.LeafList:
			if (sn.Min > 0 || sn.Max > 0) && !keep(vc.minmax(parent, sib, sn)) {
				return rc
			}
		case sn.Mandatory:
			if !keep(vc.mandatory(parent, sib, sn)) {
				return rc
			}
		}
	}
	return rc
}

// mandatory is lyd_validate_mandatory: an instance (for a choice: data of a case) must exist
// unless a when of the absent node is false; the error is at the parent, or at the schema node at
// the top level.
func (vc *valCtx) mandatory(parent *Node, sib *siblings, sn *schema.Node) error {
	if sn.Kind == schema.Choice {
		if vc.firstData(sib, sn) != nil {
			return nil
		}
	} else if vc.t.findSchema(sib, sn) != nil {
		return nil
	}
	if hasWhen(sn) {
		w, err := vc.dummyWhen(parent, sn)
		if err != nil {
			return err
		}
		if w != nil {
			return nil
		}
	}
	if vc.opts.Operational {
		if sn.Kind == schema.Choice {
			vc.log.warn("Mandatory choice \"%s\" data do not exist.", sn.Name)
		} else {
			vc.log.warn("Mandatory node \"%s\" instance does not exist.", sn.Name)
		}
		return nil
	}
	if parent == nil {
		vc.log.locSet(sn)
		defer vc.log.locBack(1)
	}
	if sn.Kind == schema.Choice {
		return vc.log.val(parent, "missing-choice", ly.Data, "Mandatory choice \"%s\" data do not exist.", sn.Name)
	}
	return vc.log.val(parent, "", ly.Data, "Mandatory node \"%s\" instance does not exist.", sn.Name)
}

// minmax is lyd_validate_minmax: too few instances (unless a when of the absent node is false) or
// too many, at the last instance counted (else the parent, else the schema node).
func (vc *valCtx) minmax(parent *Node, sib *siblings, sn *schema.Node) error {
	lo, hi := sn.Min, sn.Max
	var count uint32
	var last *Node
	if i := vc.t.schemaIndex(sib, sn); i >= 0 {
		for _, n := range sib.list[i:] {
			if n.schema != sn {
				break
			}
			last = n
			count++
			if lo > 0 && count == lo {
				lo = 0 // satisfied
				if hi == 0 {
					break
				}
			}
			if hi > 0 && count > hi {
				break
			}
		}
	}
	if lo > 0 && hasWhen(sn) {
		w, err := vc.dummyWhen(parent, sn)
		if err != nil {
			return err
		}
		if w != nil {
			lo = 0
		}
	}
	if hi > 0 && count <= hi {
		hi = 0
	}
	if lo == 0 && hi == 0 {
		return nil
	}
	msg, tag := "Too few \"%s\" instances.", "too-few-elements"
	if lo == 0 {
		msg, tag = "Too many \"%s\" instances.", "too-many-elements"
	}
	if vc.opts.Operational {
		vc.log.warn(msg, sn.Name)
		return nil
	}
	at := last
	if at == nil {
		at = parent
		vc.log.locSet(sn)
		defer vc.log.locBack(1)
	}
	return vc.log.val(at, tag, ly.Data, msg, sn.Name)
}

// uniqFindLeaf is lyd_val_uniq_find_leaf: the instance of the unique leaf below the list
// instance, through its containers.
func (vc *valCtx) uniqFindLeaf(leaf *schema.Node, list *Node) *Node {
	var chain []*schema.Node
	for s := leaf; s != nil && s != list.schema; s = s.DataParent() {
		chain = append(chain, s)
	}
	n := list
	for i := len(chain) - 1; i >= 0 && n != nil; i-- {
		n = vc.t.findSchema(&n.kids, chain[i])
	}
	return n
}

// uniqValue is the canonical text of a unique leaf of the list instance, its default when it has
// none; ok false when neither exists.
func (vc *valCtx) uniqValue(leaf *schema.Node, list *Node, useDefault bool) (string, bool) {
	if n := vc.uniqFindLeaf(leaf, list); n != nil {
		return n.value.Canonical(), true
	}
	if !useDefault || len(leaf.Default) == 0 {
		return "", false
	}
	d, ok := vc.uniqDefs[leaf]
	if !ok {
		vc.t.work++
		v, diag := types.StoreDefault(leaf, leaf.Default[0])
		if d.ok = diag == nil; d.ok {
			d.canon = v.Canonical()
		}
		vc.uniqDefs[leaf] = d
	}
	return d.canon, d.ok
}

// uniqDef is the cached default canonical of a unique leaf (ok false: none valid).
type uniqDef struct {
	canon string
	ok    bool
}

// uniqStep charges one collision comparison of unique() to the XPath step budget (U-0042) and
// checks for cancellation: instances colliding without being equal (all lacking a defaulted
// leaf) cost a comparison per earlier instance, as in libyang's hash table.
func (vc *valCtx) uniqStep() error {
	b := &vc.budget
	if b.ctx != nil {
		if err := b.ctx.Err(); err != nil {
			return err
		}
	}
	limit := b.max
	if limit <= 0 {
		limit = DefaultMaxXPathSteps
	}
	if b.steps++; b.steps > limit {
		return fmt.Errorf("%w: more than %d XPath steps (unique checks)", yang.ErrBudget, limit)
	}
	return nil
}

// uniqEqual is lyd_val_uniq_list_equal for the unique u (all of them when u < 0): it reports an
// error (a warning for operational data) when every leaf of a unique has the same value in both
// instances and returns true for the error. A leaf missing in first counts as different even
// when it has a default: libyang stores that default into the wrong variable (canon2), so only
// second's missing leaves take their default.
func (vc *valCtx) uniqEqual(first, second *Node, u int) bool {
	sn := first.schema
	us := sn.Uniques
	if u >= 0 {
		us = sn.Uniques[u : u+1]
	}
	for _, uniq := range us {
		v := 0
		for ; v < len(uniq); v++ {
			a, ok1 := vc.uniqValue(uniq[v], first, false)
			b, ok2 := vc.uniqValue(uniq[v], second, true)
			if !ok1 || !ok2 || a != b {
				break
			}
		}
		if v > 0 && v == len(uniq) {
			var names []string
			for _, l := range uniq {
				names = append(names, relPath(l, sn))
			}
			p1, p2 := lydPath(vc.t.set, first, false), lydPath(vc.t.set, second, false)
			if vc.opts.Operational {
				vc.log.warn("Unique data leaf(s) \"%s\" not satisfied in \"%s\" and \"%s\".", strings.Join(names, " "), p1, p2)
			} else {
				_ = vc.log.val(second, "data-not-unique", ly.Data, "Unique data leaf(s) \"%s\" not satisfied in \"%s\" and \"%s\".",
					strings.Join(names, " "), p1, p2)
				return true
			}
		}
		if u >= 0 {
			return false
		}
	}
	return false
}

// relPath is lysc_path_until(leaf, list, LYSC_PATH_LOG): the path from the list, the module
// name on a module change.
func relPath(leaf, list *schema.Node) string {
	var segs []string
	for s := leaf; s != nil && s != list; s = s.Parent {
		seg := s.Name
		if s.Parent == nil || s.Parent.Module != s.Module {
			seg = s.Module.Name + ":" + seg
		}
		segs = append(segs, seg)
	}
	var b strings.Builder
	for i := len(segs) - 1; i >= 0; i-- {
		if i != len(segs)-1 {
			b.WriteByte('/')
		}
		b.WriteString(segs[i])
	}
	return b.String()
}

// unique is lyd_validate_unique: two instances are compared directly; more are put into a table
// per unique keyed by the values of its leaves (an instance without a value or default for one of
// them skipped), where an insertion meeting an equal earlier instance fails with the new instance
// as "first" and the earlier as "second" (lyht_insert calls the callback that way).
func (vc *valCtx) unique(sib *siblings, sn *schema.Node) error {
	if vc.uniqDefs == nil {
		vc.uniqDefs = map[*schema.Node]uniqDef{}
	}
	clear(vc.uniqDefs)
	var inst []*Node
	for _, n := range slicesOf(sib) {
		if n.schema == sn {
			inst = append(inst, n)
		}
	}
	switch {
	case len(inst) == 2:
		if vc.uniqEqual(inst[0], inst[1], -1) {
			return errLogged
		}
	case len(inst) > 2:
		tables := make([]map[string][]*Node, len(sn.Uniques))
		for i := range tables {
			tables[i] = map[string][]*Node{}
		}
		for _, n := range inst {
			for u, uniq := range sn.Uniques {
				var key strings.Builder
				ok := true
				for _, l := range uniq {
					val, has := vc.uniqValue(l, n, true)
					if !has {
						ok = false
						break
					}
					key.WriteString(val)
					key.WriteByte(0)
				}
				if !ok {
					continue // the unique set is incomplete
				}
				k := key.String()
				for _, prev := range tables[u][k] {
					vc.t.work++
					if err := vc.uniqStep(); err != nil {
						return err
					}
					if vc.uniqEqual(n, prev, u) {
						return errLogged
					}
				}
				tables[u][k] = append(tables[u][k], n)
			}
		}
	}
	return nil
}

// newImplicitClose is lyd_parser_validate_new_implicit, the parser's hook at the close of an
// inner node: lyd_validate_new of its children, then their implicit nodes into the parser's
// queues (only NO_STATE is forwarded, design 07 §1.8).
func (lc *lydCtx) newImplicitClose(n *Node) error {
	vc := lc.valCtx()
	var rc error
	if err := vc.validateNew(n, nil, nil); err != nil {
		if rc = err; lc.fatal(err) {
			return rc
		}
	}
	var o implOpts
	if vc.opts.NoState {
		o |= implNoState
	}
	if err := vc.newImplicitR(n, nil, nil, o); err != nil {
		return err
	}
	return rc
}

// valCtx is the validation context of a parse: the parser's queues, log and node budget.
func (lc *lydCtx) valCtx() *valCtx {
	if lc.vc == nil {
		o := lc.opts.Validate
		o.NoState = o.NoState || lc.opts.NoState
		lc.vc = &valCtx{t: lc.tree, log: lc.log, opts: o, nodeWhen: &lc.nodeWhen, nodeTypes: &lc.nodeTypes, metaTypes: &lc.metaTypes,
			charge: lc.countNode, budget: xpathBudget{ctx: lc.ctx, max: lc.opts.Budget.MaxXPathSteps}}
	}
	return lc.vc
}

// validateParsed is the lyd_validate of lyd_parse: no subtree walk, the parser's queues.
func (lc *lydCtx) validateParsed() error { return lc.valCtx().validate(false) }

// validateAll is lyd_validate_all over the tree t (validate_subtree): the queues are its own.
// diff receives the implicit diff (design 07 D8b), nil for none.
func (t *Tree) validateAll(ctx context.Context, o ValidateOptions, b Budget, diff func(*Node, diffOp) error) ([]yang.Diagnostic, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	charged := 0 // implicit nodes created by this validation (Budget.MaxNodes)
	vc := &valCtx{t: t, log: &logger{set: t.set}, opts: o, nodeWhen: &nodeSet{}, nodeTypes: &nodeSet{}, metaTypes: &[]*meta{}, diff: diff,
		budget: xpathBudget{ctx: ctx, max: b.MaxXPathSteps}}
	vc.charge = func() error {
		charged++
		if limit := orDefault(b.MaxNodes, DefaultMaxNodes); charged > limit {
			return fmt.Errorf("%w: more than %d implicit data nodes", yang.ErrBudget, limit)
		}
		if charged%1024 == 0 {
			return ctx.Err()
		}
		return nil
	}
	err := vc.validate(true)
	if err == nil || errors.Is(err, errLogged) {
		err = vc.log.result() // nil when only warnings were logged
	}
	return vc.log.diags, err
}
