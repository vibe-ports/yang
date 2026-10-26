// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/diff.c (lyd_diff_siblings, lyd_diff_tree, lyd_diff,
// lyd_diff_siblings_r, lyd_diff_userord_get, lyd_diff_userord_attrs, lyd_diff_attrs,
// lyd_diff_find_match, lyd_diff_node_metadata_check, lyd_diff_node_metadata,
// lyd_diff_node_metadata_add, lyd_diff_node_metadata_r) (BSD-3-Clause, © CESNET).

package data

import (
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// DiffOptions are the options of Diff, DiffSiblings and DiffTree (LYD_DIFF_*).
type DiffOptions struct {
	// Defaults treats default nodes like explicit ones, and reports a leaf or leaf-list whose
	// default flag alone changed (LYD_DIFF_DEFAULTS).
	Defaults bool
	// Meta compares all metadata and reports the changes as yang:meta-create, yang:meta-delete
	// and yang:meta-replace with yang:meta-orig; nodes whose metadata alone changed get the
	// operation none (LYD_DIFF_META).
	Meta bool
}

// Diff is lyd_diff_siblings of the top-level nodes of first and second: the changes that turn
// first into second, as a tree whose nodes carry the yang:operation metadata (create, delete,
// replace, none) with yang:orig-value and yang:orig-default on a changed leaf, and the anchors
// yang:key, yang:value or yang:position (and their orig- counterparts) on a created or moved
// user-ordered instance. Either tree may be nil or empty (no data). The result is nil when the
// trees are equal. The trees may use different schema snapshots; sibling schema nodes then match
// by module and node-name ancestry as in libyang. Operation data with a child schema run split
// by another child is refused with LY_EINVAL (D-0116), including when an input node is only a
// subtree of the operation. Errors are a *ValidationError with libyang's diagnostics.
func Diff(first, second *Tree, o DiffOptions) (*Tree, error) {
	var a, b *Node
	if first != nil {
		a = first.top.first()
	}
	if second != nil {
		b = second.top.first()
	}
	return diffNodes(a, b, o, false, first, second)
}

// DiffSiblings is lyd_diff_siblings: the diff of first and the siblings after it with second and
// the siblings after it (either nil: no nodes), siblings in the same schema parent; see Diff. A
// list key is an LY_EINVAL error (libyang asserts it is none). Split operation-child runs are
// refused with LY_EINVAL (D-0116).
func DiffSiblings(first, second *Node, o DiffOptions) (*Tree, error) {
	return diffNodes(first, second, o, false, nil, nil)
}

// DiffTree is lyd_diff_tree: the diff of the subtrees first and second only, without their
// siblings; see DiffSiblings. When either subtree is a direct operation child, the complete
// operation parent's child runs are checked and a split run is refused with LY_EINVAL (D-0116).
func DiffTree(first, second *Node, o DiffOptions) (*Tree, error) {
	return diffNodes(first, second, o, true, nil, nil)
}

// diffNodes is lyd_diff; ft and st are the trees of first and second when known.
func diffNodes(first, second *Node, o DiffOptions, nosiblings bool, ft, st *Tree) (*Tree, error) {
	return diffNodesLookupLimit(first, second, o, nosiblings, ft, st, 0, 0)
}

// diffNodesLookupLimit is diffNodes with a test seam for the fixed internal cross-context lookup
// ceiling. Production callers pass zero and use defaultMaxDiffLookupWork.
func diffNodesLookupLimit(first, second *Node, o DiffOptions, nosiblings bool, ft, st *Tree, lookupLimit, hashLimit uint64) (*Tree, error) {
	fs, ss := ctxOf(first), ctxOf(second)
	var set *schema.Set
	switch {
	case fs != nil:
		set = fs
	case ss != nil:
		set = ss
	case ft != nil:
		set = ft.set
	case st != nil:
		set = st.set
	}
	lg := &logger{set: set}
	for _, in := range []struct {
		first    *Node
		siblings bool
	}{{first, !nosiblings}, {second, !nosiblings}} {
		if split, op := splitOpRun(in.first, in.siblings); split != nil {
			return nil, splitOpRefusal(lg, split, op, "lyd_diff")
		}
	}
	dataParent := func(n *Node) *schema.Node { // lysc_data_parent(n->schema), NULL for an opaque node
		if n.schema == nil {
			return nil
		}
		return n.schema.DataParent()
	}
	for _, n := range []*Node{first, second} {
		if n != nil && n.schema != nil && n.schema.IsKey() {
			// libyang asserts !(schema->flags & LYS_KEY) in lyd_diff_siblings_r
			return nil, lg.done(lg.logErr("LY_EINVAL", "Invalid argument a list key \"%s\" (lyd_diff()).", n.Name()))
		}
	}
	if first != nil && second != nil && dataParent(first) != dataParent(second) {
		return nil, lg.done(lg.logErr("LY_EINVAL", "Invalid arguments - cannot create diff for unrelated data (lyd_diff())."))
	}
	if set == nil {
		return nil, nil
	}
	d := newTree(set)
	// These indexes belong to this call. In particular, their sibling keys point into both input
	// trees and must not make the returned one-leaf delta retain either complete snapshot.
	defer func() {
		d.foreignSchemaInput = nil
		d.foreignSchema = nil
		d.foreignInstance = nil
		d.schemaLineage = nil
		d.lineageComponents = nil
		d.nextLineageID = 0
	}()
	d.work.lookupLimit, d.work.hashLimit = lookupLimit, hashLimit
	if fs == nil && ft != nil {
		fs = ft.set
	}
	if ss == nil && st != nil {
		ss = st.set
	}
	d.mixedDiff = fs != nil && ss != nil && fs != ss
	defer d.work.beginHashWork()()
	defer d.work.beginLookupWork()()
	if err := d.diffSiblingsR(first, second, o, nosiblings); err != nil {
		return nil, lg.done(err)
	}
	if d.work.lookupErr != nil {
		return nil, d.work.lookupErr
	}
	if d.work.hashErr != nil {
		return nil, d.work.hashErr
	}
	if d.top.len() == 0 {
		return nil, nil
	}
	return d, nil
}

// splitOpRun returns the first child whose schema node reappears after another child, and its
// rpc or action. It scans each input node once and charges that visit to the node's tree. An
// operation parent is checked as well because both lyd_diff_siblings and lyd_diff_tree can
// receive one of its children without receiving the operation node itself.
func splitOpRun(first *Node, withSiblings bool) (split, op *Node) {
	if first == nil {
		return nil, nil
	}
	t := treeOf(first)
	count := func() {
		if t != nil {
			t.work.Add(1)
		}
	}
	check := func(n *Node, enqueue func(*Node)) *Node {
		isOp := n.schema != nil && (n.schema.Kind == schema.RPC || n.schema.Kind == schema.Action)
		var closed map[*schema.Node]bool
		if isOp {
			closed = map[*schema.Node]bool{}
		}
		var prev *schema.Node
		havePrev := false
		visit := func(c *Node) *Node {
			count()
			enqueue(c)
			if !isOp {
				return nil
			}
			if !havePrev {
				prev, havePrev = c.schema, true
				return nil
			}
			if c.schema == prev {
				return nil
			}
			if prev != nil {
				closed[prev] = true
			}
			prev = c.schema
			if c.schema != nil && closed[c.schema] {
				return c
			}
			return nil
		}
		for _, c := range n.kids.SchemaAll() {
			if found := visit(c); found != nil {
				return found
			}
		}
		for _, c := range n.kids.opaqueAll() {
			if found := visit(c); found != nil {
				return found
			}
		}
		return nil
	}

	// An input can start at a child of the operation whose complete child list is split. Check
	// that list even for DiffTree, but enqueue only the nodes that are actually an input.
	if first.parent != nil && first.parent.schema != nil &&
		(first.parent.schema.Kind == schema.RPC || first.parent.schema.Kind == schema.Action) {
		if found := check(first.parent, func(*Node) {}); found != nil {
			return found, first.parent
		}
	}

	queue := fromNode(first)
	if !withSiblings {
		queue = []*Node{first}
	}
	for range queue {
		count()
	}
	for i := 0; i < len(queue); i++ {
		n := queue[i]
		if found := check(n, func(c *Node) { queue = append(queue, c) }); found != nil {
			return found, n
		}
	}
	return nil, nil
}

// splitOpRefusal logs D-0116 in the shape of the public diff entry point's other argument
// errors. The operation path is stable even when the split instance itself has a predicate.
func splitOpRefusal(lg *logger, split, op *Node, fn string) error {
	return lg.done(lg.logErr("LY_EINVAL",
		"Invalid arguments - the instances of \"%s\" in operation \"%s\" are split by its input, a diff of such data is not supported (D-0116) (%s()).",
		split.Name(), lydPath(lg.set, op, false), fn))
}

// fromNode is n and the siblings after it, schema nodes then opaque ones; nil for nil.
func fromNode(n *Node) []*Node {
	if n == nil {
		return nil
	}
	sib := n.siblingsOf()
	if sib == nil {
		return []*Node{n}
	}
	all := sib.CollectRange(0, sib.Len())
	return all[slices.Index(all, n):]
}

// sibsOf is the sibling list n is in, nil for nil (lyd_find_sibling_* search all siblings).
func sibsOf(n *Node) *siblings {
	if n == nil {
		return nil
	}
	if sib := n.siblingsOf(); sib != nil {
		return sib
	}
	s := &siblings{}
	if n.schema == nil {
		// No work binding or journal exists on this fresh view.
		s.Build(nil, []*Node{n}) //nolint:errcheck,gosec // unbound storage cannot fail journal capture
	} else {
		// No work binding or journal exists on this fresh view.
		s.Build([]*Node{n}, nil) //nolint:errcheck,gosec // unbound storage cannot fail journal capture
	}
	s.addContext(n)
	return s
}

// firstNoKeys is lyd_child_no_keys: the first child of n that is not a list key, nil if none.
func firstNoKeys(n *Node) *Node {
	for c := range n.kids.all() {
		if !c.isKey() {
			return c
		}
	}
	return nil
}

// findMatch is lyd_diff_find_match: the instance of target among sib (nil: none), the next equal
// one on every call for duplicate instances; a default node does not count without defaults.
func (t *Tree) findMatch(sib *siblings, target *Node, defaults bool, cache *dupCache) (*Node, error) {
	if sib == nil || sib.len() == 0 {
		return nil, nil
	}
	var m *Node
	switch {
	case target.schema == nil:
		m = opaqNext(sib, target.Name())
	case target.schema.Kind == schema.List || target.schema.Kind == schema.LeafList:
		var err error
		if m, err = t.findFirstChecked(sib, target); err != nil {
			return nil, err
		}
	default:
		lookupBefore := t.work.lookupUsed
		m = t.schemaLookup(sib, target.schema)
		indexed := t.work.lookupUsed != lookupBefore
		// A diff-time index uses the same equality as native lookup, so its miss is final. An
		// ownerless schema can instead take schemaLookup's ordinary pointer path; retain its full
		// lineage fallback, but charge every candidate to the same bounded lookup quota.
		if m == nil && !indexed && len(sib.contexts) == 1 && ctxOf(sib.At(0)) != ctxOf(target) {
			for _, candidate := range sib.SchemaAll() {
				if err := t.work.chargeLookup(1); err != nil {
					return nil, err
				}
				sib.visit()
				if compareSchemaEqual(candidate.schema, target.schema, true) {
					m = candidate
					break
				}
			}
		}
	}
	var err error
	m, err = t.dupInstNext(m, cache)
	if err != nil {
		return nil, err
	}
	if m != nil && m.flags&FlagDefault != 0 && !defaults {
		return nil, nil // default nodes ignored
	}
	return m, nil
}

// userord is struct lyd_diff_userord: the current (virtually changed) order of the instances of
// one user-ordered list or leaf-list of the first tree, and the position of the next instance
// of the second tree.
type userord struct {
	schema *schema.Node
	pos    int
	inst   []*Node // allocated slots; slots at or after count model native out-of-count writes
	count  int
	unsafe bool // a previous native out-of-count transition makes the next access undefined
}

// userordGet is lyd_diff_userord_get: the entry of sn, created with the instances of first's
// siblings in their order (none for a nil first).
func (t *Tree) userordGet(first *Node, sn *schema.Node, items *[]*userord) *userord {
	for _, u := range *items {
		if u.schema == sn {
			return u
		}
	}
	u := &userord{schema: sn}
	if first != nil {
		for n := range t.instances(sibsOf(first), first.schema) {
			u.inst = append(u.inst, n)
		}
	}
	u.count = len(u.inst)
	*items = append(*items, u)
	return u
}

// errNoChange is libyang's LY_ENOT of the attrs functions: nothing to add to the diff.
var errNoChange = errors.New("data: no change")

// userordAttrs is lyd_diff_userord_attrs: the operation and metadata of a user-ordered instance
// (first nil: created, second nil: deleted), applied to the virtual order u.
func (t *Tree) userordAttrs(first, second *Node, o DiffOptions, u *userord) (diffOp, diffAttrs, error) {
	var a diffAttrs
	// schema = first ? first->schema : second->schema (diff.c:695), not the entry's key: across
	// contexts a config leaf-list and its state counterpart select different attributes
	sn := u.schema
	switch {
	case first != nil:
		sn = first.schema
	case second != nil:
		sn = second.schema
	}
	firstPos := 0
	wasUnsafe := u.unsafe
	if first != nil {
		firstPos = slices.Index(u.inst[:u.count], first)
		if firstPos < 0 {
			// LY_ARRAY_FOR leaves first_pos at the logical count after the release assertion.
			firstPos = u.count
			u.unsafe = true
		}
	}
	secondPos := u.pos
	// Entries are keyed by schema pointer (diff.c:546-547). Across contexts the entry used in
	// the second pass can therefore omit first or end before second_pos; native
	// lyd_diff_userord_attrs then asserts or accesses outside the array. Refuse only those native
	// undefined states before reading them (D-0121; libyang v5.8.6 src/diff.c:700-705,827-849).
	refuse := func() error {
		return &opError{"LY_EINVAL", fmt.Sprintf("Invalid arguments - the user-ordered instances of %q "+
			"in two schema contexts leave libyang's diff bookkeeping out of range, a diff of such data is not "+
			"supported (D-0121) (lyd_diff()).", sn.Name)}
	}
	if wasUnsafe && second != nil {
		return 0, a, refuse()
	}
	u.pos++
	positionEqual := false
	if second != nil && first != nil && secondPos < u.count {
		var err error
		positionEqual, err = compareSingleErr(t, second, u.inst[secondPos], isDupInstList(second.schema))
		if err != nil {
			return 0, a, err
		}
	}
	var op diffOp
	switch {
	case second == nil:
		op = diffDelete
	case first == nil:
		op = diffCreate
	case secondPos >= u.count || !positionEqual:
		op = diffReplace // another instance on this position in first: first is moved here
	case o.Defaults && first.flags&FlagDefault != second.flags&FlagDefault:
		op = diffNone
	case o.Meta && t.metaDiffers(first, second):
		op = diffNone
	default:
		return 0, a, errNoChange
	}
	if op == diffReplace && firstPos < secondPos {
		// The release build reaches the write with an underflowed move range. Preserve the
		// observed slots for this last operation, but refuse before a later operation consumes
		// that undefined state. This keeps defined sweeps 06 and 37 compatible while refusing
		// the crashing three-operation shapes 04, 20 and 22.
		u.unsafe = true
	}
	str := func(s string) *string { return &s }
	pred := func(pos int) *string { // the anchor: the instance before pos, "" for the first
		switch {
		case pos == 0:
			return str("")
		case pos-1 >= len(u.inst) || u.inst[pos-1] == nil:
			return str("")
		case sn.Kind == schema.List:
			return str(listPredicate(u.inst[pos-1]))
		}
		return str(u.inst[pos-1].value.Canonical())
	}
	posStr := func(pos int) *string {
		if pos == 0 {
			return str("")
		}
		return str(strconv.Itoa(pos))
	}
	dup := isDupInstList(sn)
	if sn.Kind == schema.LeafList && (op == diffReplace || op == diffNone) {
		a.origDefault = str(strconv.FormatBool(first.flags&FlagDefault != 0))
	}
	switch {
	case dup:
		if op == diffReplace || op == diffCreate {
			a.position = posStr(secondPos)
		}
		if op == diffReplace || op == diffDelete {
			a.origPosition = posStr(firstPos)
		}
	case sn.Kind == schema.LeafList:
		if op == diffReplace || op == diffCreate {
			a.value = pred(secondPos)
		}
		if op == diffReplace || op == diffDelete {
			a.origValue = pred(firstPos)
		}
	default:
		if op == diffReplace || op == diffCreate {
			a.key = pred(secondPos)
		}
		if op == diffReplace || op == diffDelete {
			a.origKey = pred(firstPos)
		}
	}
	ensure := func(n int) {
		if n > len(u.inst) {
			u.inst = append(u.inst, make([]*Node, n-len(u.inst))...)
		}
	}
	switch op { // the change applied to the virtual order
	case diffCreate:
		if secondPos > u.count {
			u.unsafe = true
		}
		ensure(max(u.count+1, secondPos+1))
		if secondPos < u.count {
			copy(u.inst[secondPos+1:u.count+1], u.inst[secondPos:u.count])
		}
		u.count++
		u.inst[secondPos] = second
	case diffDelete:
		if firstPos < u.count {
			copy(u.inst[firstPos:u.count-1], u.inst[firstPos+1:u.count])
			u.count--
		} else {
			u.unsafe = true
		}
	case diffReplace:
		ensure(max(u.count+1, secondPos+1))
		if firstPos >= secondPos {
			copy(u.inst[secondPos+1:firstPos+1], u.inst[secondPos:firstPos])
		}
		u.inst[secondPos] = first
	}
	return op, a, nil
}

// attrs is lyd_diff_attrs: the operation and metadata of a node that is not user-ordered (first
// nil: created, second nil: deleted).
func (t *Tree) attrs(first, second *Node, o DiffOptions) (diffOp, diffAttrs, error) {
	var a diffAttrs
	sn := second
	if first != nil {
		sn = first
	}
	var op diffOp
	switch {
	case second == nil:
		op = diffDelete
	case first == nil:
		op = diffCreate
	default:
		switch sn.schema.Kind {
		case schema.Container, schema.RPC, schema.Action, schema.Notification:
			if !o.Meta || !t.metaDiffers(first, second) {
				return 0, a, errNoChange
			}
			op = diffNone
		case schema.List, schema.LeafList:
			switch {
			case o.Defaults && first.flags&FlagDefault != second.flags&FlagDefault:
				op = diffNone
			case o.Meta && t.metaDiffers(first, second):
				op = diffNone
			default:
				return 0, a, errNoChange
			}
		case schema.Leaf:
			equal, err := compareSingleErr(t, first, second, false)
			if err != nil {
				return 0, a, err
			}
			switch {
			case !equal:
				op = diffReplace
			case o.Defaults && first.flags&FlagDefault != second.flags&FlagDefault:
				op = diffNone
			case o.Meta && t.metaDiffers(first, second):
				op = diffNone
			default:
				return 0, a, errNoChange
			}
		default: // anydata, anyxml values await the public diff surface (U-0112)
			return 0, a, fmt.Errorf("data: diff of %s %q: %w", nodetypeStr(sn.schema.Kind), sn.schema.Name, ErrUnsupported)
		}
	}
	if sn.isTerm() && (op == diffReplace || op == diffNone) {
		s := strconv.FormatBool(first.flags&FlagDefault != 0)
		a.origDefault = &s
	}
	if sn.schema.Kind == schema.Leaf && op == diffReplace {
		s := first.value.Canonical()
		a.origValue = &s
	}
	return op, a, nil
}

// diffYangModule is ly_ctx_get_module_implemented(LYD_CTX(n), "yang"): the one module whose
// annotations a metadata diff ignores. It is a module identity, so another context's yang
// module is an ordinary annotation module there (diff.c:589,1068).
func (t *Tree) diffYangModule(n *Node) *schema.Module {
	set := ctxOf(n)
	if set == nil {
		set = t.set
	}
	if set == nil {
		return nil
	}
	return set.Implemented("yang")
}

// metaDiffers is lyd_diff_node_metadata_check: the metadata of first and second (those of first's
// context's yang module aside) differ as multisets, or those of a list's keys do.
func (t *Tree) metaDiffers(first, second *Node) bool {
	yang := t.diffYangModule(first)
	var rest []*meta
	for _, m := range second.meta {
		if m.mod != yang {
			rest = append(rest, m)
		}
	}
	for _, m := range first.meta {
		if m.mod == yang {
			continue
		}
		i := slices.IndexFunc(rest, func(r *meta) bool { return compareMeta(m, r) })
		if i < 0 {
			return true
		}
		rest = slices.Delete(rest, i, i+1)
	}
	if len(rest) > 0 {
		return true
	}
	if first.schema.Kind == schema.List {
		for i, k := range first.kids.SchemaAll() {
			if !k.isKey() {
				break
			}
			if t.metaDiffers(k, second.kids.At(i)) {
				return true
			}
		}
	}
	return false
}

// metaAdd is lyd_diff_node_metadata_add: the diff metadata name ("meta-create", …) with the
// value "module:name=value" on n.
func (t *Tree) metaAdd(n *Node, name string, m *meta) error {
	lg := &logger{set: ctxOf(n)}
	if _, err := lg.newMeta(n, nil, "yang:"+name, m.mod.Name+":"+m.name+"="+m.value.Canonical(), false); err != nil {
		if len(lg.diags) > 0 {
			return &opError{lg.diags[0].Err, lg.diags[0].Msg}
		}
		return err
	}
	return nil
}

// nodeMeta is lyd_diff_node_metadata: the metadata changes from first to second (either nil) as
// diff metadata on n: an annotation instance of first without an equal one in second is
// replaced by the first instance of the same annotation left in second, else deleted; the
// instances of second left are created.
func (t *Tree) nodeMeta(first, second, n *Node) error {
	yang := t.diffYangModule(n) // LYD_CTX(diff_node)
	var rest []*meta
	if second != nil {
		for _, m := range second.meta {
			if m.mod != yang {
				rest = append(rest, m)
			}
		}
	}
	if first != nil {
		for _, m := range first.meta {
			if m.mod == yang {
				continue
			}
			same := func(r *meta) bool { return r.mod == m.mod && r.name == m.name }
			ann := slices.IndexFunc(rest, same)
			eq := slices.IndexFunc(rest, func(r *meta) bool { return same(r) && types.Equal(m.value, r.value) })
			switch {
			case eq >= 0: // no change
				rest = slices.Delete(rest, eq, eq+1)
			case ann >= 0:
				if err := t.metaAdd(n, "meta-replace", rest[ann]); err != nil {
					return err
				}
				if err := t.metaAdd(n, "meta-orig", m); err != nil {
					return err
				}
				rest = slices.Delete(rest, ann, ann+1)
			default:
				if err := t.metaAdd(n, "meta-delete", m); err != nil {
					return err
				}
			}
		}
	}
	for _, m := range rest {
		if err := t.metaAdd(n, "meta-create", m); err != nil {
			return err
		}
	}
	return nil
}

// nodeMetaR is lyd_diff_node_metadata_r: nodeMeta on the diff subtree n and on its children
// (with keysOnly the keys only), matched in the subtrees first and second (either nil).
func (t *Tree) nodeMetaR(first, second *Node, keysOnly bool, n *Node) error {
	if err := t.nodeMeta(first, second, n); err != nil {
		return err
	}
	for _, c := range n.kids.nodes() {
		if keysOnly && !c.isKey() {
			break
		}
		var f, s *Node
		if first != nil {
			var err error
			if f, err = t.findFirstChecked(&first.kids, c); err != nil {
				return err
			}
		}
		if second != nil {
			var err error
			if s, err = t.findFirstChecked(&second.kids, c); err != nil {
				return err
			}
		}
		if err := t.nodeMetaR(f, s, keysOnly, c); err != nil {
			return err
		}
	}
	return nil
}

// diffSiblingsR is lyd_diff_siblings_r: the diff of first and the siblings after it with second
// and the siblings after it, recursively, added to t. Deletes, replaces and nones come first,
// from first's side; then creates and user-ordered moves, from second's side, with each move
// applied to the virtual order of the first tree so that the anchors of the later operations
// are final (see diff.c for the whole argument).
func (t *Tree) diffSiblingsR(first, second *Node, o DiffOptions, nosiblings bool) error {
	var items []*userord
	firstCache, secondCache := &dupCache{}, &dupCache{}
	fs, ss := sibsOf(first), sibsOf(second)
	if t.foreignSchemaInput == nil {
		t.foreignSchemaInput = make(map[*siblings]bool)
	}
	if fs != nil {
		t.foreignSchemaInput[fs] = true
	}
	if ss != nil {
		t.foreignSchemaInput[ss] = true
	}
	for _, f := range fromNode(first) {
		if f.schema == nil {
			continue
		}
		if f.flags&FlagDefault != 0 && !o.Defaults {
			continue // default nodes skipped
		}
		m, err := t.findMatch(ss, f, o.Defaults, secondCache)
		if err != nil {
			return err
		}
		if t.work.lookupErr != nil {
			return t.work.lookupErr
		}
		var dn *Node
		if isUserOrdered(f.schema) {
			u := t.userordGet(f, f.schema, &items)
			if m == nil { // only the deletes of user-ordered instances now
				op, a, err := t.userordAttrs(f, nil, o, u)
				if err != nil {
					return err
				}
				if dn, err = t.diffAdd(f, op, a); err != nil {
					return err
				}
			}
		} else {
			op, a, err := t.attrs(f, m, o)
			switch {
			case err == nil:
				src := m
				if op == diffDelete {
					src = f
				}
				if dn, err = t.diffAdd(src, op, a); err != nil {
					return err
				}
			case !errors.Is(err, errNoChange):
				return err
			}
		}
		if m != nil {
			if o.Meta && dn != nil {
				if err := t.nodeMetaR(f, m, true, dn); err != nil {
					return err
				}
			}
			if err := t.diffSiblingsR(firstNoKeys(f), firstNoKeys(m), o, false); err != nil {
				return err
			}
		} else if o.Meta && dn != nil {
			if err := t.nodeMetaR(f, nil, false, dn); err != nil {
				return err
			}
		}
		if nosiblings {
			break
		}
	}
	for _, u := range items {
		u.pos = 0
	}
	for _, s := range fromNode(second) {
		if s.schema == nil {
			continue
		}
		if s.flags&FlagDefault != 0 && !o.Defaults {
			continue
		}
		m, err := t.findMatch(fs, s, o.Defaults, firstCache)
		if err != nil {
			return err
		}
		if t.work.lookupErr != nil {
			return t.work.lookupErr
		}
		var dn *Node
		switch {
		case isUserOrdered(s.schema):
			u := t.userordGet(m, s.schema, &items)
			op, a, err := t.userordAttrs(m, s, o, u)
			switch {
			case err == nil:
				if dn, err = t.diffAdd(s, op, a); err != nil {
					return err
				}
			case !errors.Is(err, errNoChange):
				return err
			}
		case m == nil:
			op, a, err := t.attrs(nil, s, o)
			if err != nil {
				return err
			}
			if dn, err = t.diffAdd(s, op, a); err != nil {
				return err
			}
		}
		if o.Meta && dn != nil {
			if err := t.nodeMetaR(m, s, false, dn); err != nil {
				return err
			}
		}
		if nosiblings {
			break
		}
	}
	return nil
}
