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
// user-ordered instance. Either tree may be nil (no data). The result is nil when the trees are
// equal. Errors are a *ValidationError with libyang's diagnostics.
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
// list key is an LY_EINVAL error (libyang asserts it is none).
func DiffSiblings(first, second *Node, o DiffOptions) (*Tree, error) {
	return diffNodes(first, second, o, false, nil, nil)
}

// DiffTree is lyd_diff_tree: the diff of the subtrees first and second only, without their
// siblings; see DiffSiblings.
func DiffTree(first, second *Node, o DiffOptions) (*Tree, error) {
	return diffNodes(first, second, o, true, nil, nil)
}

// diffNodes is lyd_diff; ft and st are the trees of first and second when known (an empty tree
// still names its schema set).
func diffNodes(first, second *Node, o DiffOptions, nosiblings bool, ft, st *Tree) (*Tree, error) {
	var set *schema.Set
	switch {
	case first != nil:
		set = setOf(first)
	case second != nil:
		set = setOf(second)
	case ft != nil:
		set = ft.set
	case st != nil:
		set = st.set
	}
	lg := &logger{set: set}
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
	if err := d.diffSiblingsR(first, second, o, nosiblings); err != nil {
		var oe *opError
		if errors.As(err, &oe) {
			return nil, lg.done(lg.logErr(oe.Err, "%s", oe.Msg))
		}
		return nil, err
	}
	if d.top.len() == 0 {
		return nil, nil
	}
	return d, nil
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
	all := slices.Concat(sib.list, sib.opq)
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
	return &siblings{list: []*Node{n}}
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
func (t *Tree) findMatch(sib *siblings, target *Node, defaults bool, cache *dupCache) *Node {
	if sib == nil || sib.len() == 0 {
		return nil
	}
	var m *Node
	switch {
	case target.schema == nil:
		m = opaqNext(sib, target.Name())
	case target.schema.Kind == schema.List || target.schema.Kind == schema.LeafList:
		m = t.findFirst(sib, target)
	default:
		m = t.findSchema(sib, target.schema)
	}
	m = t.dupInstNext(m, cache)
	if m != nil && m.flags&FlagDefault != 0 && !defaults {
		return nil // default nodes ignored
	}
	return m
}

// userord is struct lyd_diff_userord: the current (virtually changed) order of the instances of
// one user-ordered list or leaf-list of the first tree, and the position of the next instance
// of the second tree.
type userord struct {
	schema *schema.Node
	pos    int
	inst   []*Node
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
		for n := range t.instances(first.siblingsOf(), first.schema) {
			u.inst = append(u.inst, n)
		}
	}
	*items = append(*items, u)
	return u
}

// errNoChange is libyang's LY_ENOT of the attrs functions: nothing to add to the diff.
var errNoChange = errors.New("data: no change")

// userordAttrs is lyd_diff_userord_attrs: the operation and metadata of a user-ordered instance
// (first nil: created, second nil: deleted), applied to the virtual order u.
func (t *Tree) userordAttrs(first, second *Node, o DiffOptions, u *userord) (diffOp, diffAttrs, error) {
	var a diffAttrs
	sn := u.schema
	firstPos := 0
	if first != nil {
		firstPos = slices.Index(u.inst, first)
	}
	secondPos := u.pos
	u.pos++
	var op diffOp
	switch {
	case second == nil:
		op = diffDelete
	case first == nil:
		op = diffCreate
	case secondPos >= len(u.inst) || !compareSingle(t, second, u.inst[secondPos], isDupInstList(second.schema)):
		op = diffReplace // another instance on this position in first: first is moved here
	case o.Defaults && first.flags&FlagDefault != second.flags&FlagDefault:
		op = diffNone
	case o.Meta && t.metaDiffers(first, second):
		op = diffNone
	default:
		return 0, a, errNoChange
	}
	str := func(s string) *string { return &s }
	pred := func(pos int) *string { // the anchor: the instance before pos, "" for the first
		switch {
		case pos == 0:
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
	switch op { // the change applied to the virtual order
	case diffCreate:
		u.inst = slices.Insert(u.inst, min(secondPos, len(u.inst)), second)
	case diffDelete:
		u.inst = slices.Delete(u.inst, firstPos, firstPos+1)
	case diffReplace:
		copy(u.inst[secondPos+1:firstPos+1], u.inst[secondPos:firstPos])
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
			switch {
			case !compareSingle(t, first, second, false):
				op = diffReplace
			case o.Defaults && first.flags&FlagDefault != second.flags&FlagDefault:
				op = diffNone
			case o.Meta && t.metaDiffers(first, second):
				op = diffNone
			default:
				return 0, a, errNoChange
			}
		default: // anydata, anyxml: their values come with M5 (U-0043)
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

// metaDiffers is lyd_diff_node_metadata_check: the metadata of first and second (those of the
// module yang aside) differ as multisets, or those of a list's keys do.
func (t *Tree) metaDiffers(first, second *Node) bool {
	var rest []*meta
	for _, m := range second.meta {
		if m.mod.Name != "yang" {
			rest = append(rest, m)
		}
	}
	for _, m := range first.meta {
		if m.mod.Name == "yang" {
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
		for i, k := range first.kids.list {
			if !k.isKey() {
				break
			}
			if t.metaDiffers(k, second.kids.list[i]) {
				return true
			}
		}
	}
	return false
}

// metaAdd is lyd_diff_node_metadata_add: the diff metadata name ("meta-create", …) with the
// value "module:name=value" on n.
func (t *Tree) metaAdd(n *Node, name string, m *meta) error {
	lg := &logger{set: t.set}
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
	var rest []*meta
	if second != nil {
		for _, m := range second.meta {
			if m.mod.Name != "yang" {
				rest = append(rest, m)
			}
		}
	}
	if first != nil {
		for _, m := range first.meta {
			if m.mod.Name == "yang" {
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
			f = t.findFirst(&first.kids, c)
		}
		if second != nil {
			s = t.findFirst(&second.kids, c)
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
	for _, f := range fromNode(first) {
		if f.schema == nil {
			continue
		}
		if f.flags&FlagDefault != 0 && !o.Defaults {
			continue // default nodes skipped
		}
		m := t.findMatch(ss, f, o.Defaults, secondCache)
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
		m := t.findMatch(fs, s, o.Defaults, firstCache)
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
