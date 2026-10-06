// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/validation.c (lyd_val_diff_add) and src/diff.c (lyd_diff_add,
// lyd_diff_dup, lyd_diff_add_create_nested_userord, lyd_diff_get_op, lyd_diff_find_meta,
// lyd_diff_del_meta, lyd_diff_insert_sibling, lyd_diff_change_op, lyd_diff_find_match,
// lyd_diff_merge_all, lyd_diff_merge_module, lyd_diff_merge_r, lyd_diff_merge_create,
// lyd_diff_merge_delete, lyd_diff_merge_none, lyd_diff_is_redundant, lyd_diff_is_redundant_meta),
// the subset the implicit diff of the validation needs (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// diffOps are the yang:operation values (lyd_diff_op2str); the implicit diff creates create,
// delete and none, and merging a create into a delete of a leaf makes replace.
const diffReplace, diffNone diffOp = 2, 3

func (op diffOp) String() string { return [...]string{"create", "delete", "replace", "none"}[op] }

// ValidateDiff is Validate that also returns lyd_validate_all's diff: the nodes the validation
// created (implicit defaults) and deleted (false when, auto-deleted cases) as a tree with
// yang:operation metadata on them and "none" on their parents; nil when nothing changed.
func (t *Tree) ValidateDiff(ctx context.Context, o ValidateOptions) (diff *Tree, d []yang.Diagnostic, err error) {
	dt := newTree(t.set)
	d, err = t.validateAll(ctx, o, Budget{}, dt.valDiffAdd)
	if dt.top.len() == 0 {
		dt = nil
	}
	return dt, d, err
}

// isUserOrdered is lysc_is_userordered.
func isUserOrdered(s *schema.Node) bool {
	return s != nil && s.UserOrdered && (s.Kind == schema.List || s.Kind == schema.LeafList)
}

// listPredicate is lyd_path_list_predicate: the key predicates of the list instance n.
func listPredicate(n *Node) string {
	var b strings.Builder
	for _, k := range n.kids.list {
		if k.schema == nil || !k.schema.IsKey() {
			break
		}
		b.WriteString("[" + k.schema.Name + "=" + quoted(k.value.Canonical()) + "]")
	}
	return b.String()
}

// prevInst is the previous sibling of n when it is an instance of the same schema node.
func prevInst(n *Node) *Node {
	sib := n.siblingsOf()
	for i := len(sib.list) - 1; i > 0; i-- {
		if sib.list[i] == n {
			if p := sib.list[i-1]; p.schema == n.schema {
				return p
			}
			return nil
		}
	}
	return nil
}

// valDiffAdd is lyd_val_diff_add: n created or deleted by the validation added to the diff t, the
// anchor of a created user-ordered instance (yang:key, yang:value or yang:position) included.
func (t *Tree) valDiffAdd(n *Node, op diffOp) error {
	var key, value, position *string
	str := func(s string) *string { return &s }
	if op == diffCreate && isUserOrdered(n.schema) {
		switch prev := prevInst(n); {
		case isDupInstList(n.schema):
			position = str("")
			if pos := listPos(n); pos > 1 {
				position = str(strconv.Itoa(pos - 1))
			}
		case n.schema.Kind == schema.List && prev != nil:
			key = str(listPredicate(prev))
		case n.schema.Kind == schema.List:
			key = str("")
		case prev != nil:
			value = str(prev.value.Canonical())
		default:
			value = str("")
		}
	}
	src := newTree(t.set)
	if err := src.diffAdd(n, op, key, value, position); err != nil {
		return err
	}
	return t.diffMergeAll(src)
}

// yangMeta adds the metadata name of the module yang with the value val to n
// (lyd_new_meta with LYD_NEW_VAL_STORE_ONLY).
func (t *Tree) yangMeta(n *Node, name, val string) error {
	mod := t.set.Implemented("yang")
	ant := metaAnnotation(mod, name)
	if ant == nil {
		return &opError{"LY_EINT", fmt.Sprintf("Annotation \"yang:%s\" not found.", name)} // libyang asserts the module
	}
	v, d := types.StoreOnly(ant.Type, val, types.FormatJSON, types.HintData, types.ModuleNames{Set: t.set}, n.schema)
	if d != nil {
		return &opError{"LY_EVALID", d.Msg}
	}
	n.meta = append(n.meta, &meta{mod: mod, name: name, value: v})
	return nil
}

// findYangMeta is lyd_diff_find_meta / lyd_find_meta(…, "yang:name"): the index of the metadata
// name of the module yang of n, -1 if there is none.
func findYangMeta(n *Node, name string) int {
	for i, m := range n.meta {
		if m.name == name && m.mod.Name == "yang" {
			return i
		}
	}
	return -1
}

// delYangMeta is lyd_diff_del_meta.
func delYangMeta(n *Node, name string) {
	if i := findYangMeta(n, name); i >= 0 {
		n.meta = append(n.meta[:i:i], n.meta[i+1:]...)
	}
}

// diffGetOp is lyd_diff_get_op: the operation of n or of its nearest parent with one (a parent's
// replace does not count); found false when there is none.
func diffGetOp(n *Node) (op diffOp, found bool) {
	for p := n; p != nil; p = p.parent {
		i := findYangMeta(p, "operation")
		if i < 0 {
			continue
		}
		s := p.meta[i].value.Canonical()
		if s[0] == 'r' && p != n {
			continue
		}
		return map[string]diffOp{"create": diffCreate, "delete": diffDelete, "replace": diffReplace, "none": diffNone}[s], true
	}
	return 0, false
}

// diffOpOf is lyd_diff_get_op without found: no operation is an error.
func (t *Tree) diffOpOf(n *Node) (diffOp, error) {
	op, ok := diffGetOp(n)
	if !ok {
		return 0, &opError{"LY_EINVAL", fmt.Sprintf("Node \"%s\" without an operation.", lydPath(t.set, n, false))}
	}
	return op, nil
}

// diffChangeOp is lyd_diff_change_op.
func (t *Tree) diffChangeOp(n *Node, op diffOp) error {
	delYangMeta(n, "operation")
	return t.yangMeta(n, "operation", op.String())
}

// diffDup is lyd_diff_dup without a diff parent: n with its subtree, and its parents (with their
// keys, the topmost with the operation none), inserted at the top of t.
func (t *Tree) diffDup(n *Node) (*Node, error) {
	opts := dupNoMeta | dupWithFlags | dupRecursive // a replace never comes from the validation
	if isUserOrdered(n.schema) {
		opts |= dupNoLyds
	}
	dup, err := t.dupR(n, nil, false, insertDefault, opts)
	if err != nil {
		return nil, err
	}
	top, orig := dup, n
	for top.schema.DataParent() != nil {
		orig = orig.parent
		d, err := t.dupR(orig, nil, false, insertDefault, dupNoMeta|dupWithFlags)
		if err != nil {
			return nil, err
		}
		t.insert(d, top, insertDefault)
		top = d
	}
	t.insert(nil, top, insertLastBySchema) // lyd_diff_insert_sibling
	if top != dup {
		if err := t.yangMeta(top, "operation", diffNone.String()); err != nil {
			return nil, err
		}
	}
	return dup, nil
}

// diffAdd is lyd_diff_add into the empty diff t (as lyd_val_diff_add calls it): the copy of n
// with the operation op, the nested user-ordered instances of a created subtree with their
// anchors, and n's own anchor.
func (t *Tree) diffAdd(n *Node, op diffOp, key, value, position *string) error {
	dup, err := t.diffDup(n)
	if err != nil {
		return err
	}
	if cur, found := diffGetOp(dup); !found || cur != op {
		if err := t.yangMeta(dup, "operation", op.String()); err != nil {
			return err
		}
	}
	if op == diffCreate {
		for e := range dup.All() {
			if e != dup && isUserOrdered(e.schema) {
				if err := t.diffCreateNestedUserord(e); err != nil {
					return err
				}
			}
		}
	}
	for _, m := range []struct {
		name string
		val  *string
	}{{"key", key}, {"value", value}, {"position", position}} {
		if m.val != nil {
			if err := t.yangMeta(dup, m.name, *m.val); err != nil {
				return err
			}
		}
	}
	return nil
}

// diffCreateNestedUserord is lyd_diff_add_create_nested_userord: the anchor metadata of a
// user-ordered instance inside a created subtree.
func (t *Tree) diffCreateNestedUserord(n *Node) error {
	prev := prevInst(n)
	switch {
	case isDupInstList(n.schema):
		v := ""
		if pos := listPos(n); pos > 1 {
			v = strconv.Itoa(pos - 1)
		}
		return t.yangMeta(n, "position", v)
	case n.schema.Kind == schema.List:
		v := ""
		if prev != nil {
			v = listPredicate(prev)
		}
		return t.yangMeta(n, "key", v)
	}
	v := ""
	if prev != nil {
		v = prev.value.Canonical()
	}
	return t.yangMeta(n, "value", v)
}

// diffMergeAll is lyd_diff_merge_all (no options, no callback) of the diff src into t.
func (t *Tree) diffMergeAll(src *Tree) error {
	cache := &dupCache{}
	for _, n := range src.top.nodes() {
		if err := t.diffMergeR(n, nil, cache); err != nil {
			return err
		}
	}
	return nil
}

// diffFindMatch is lyd_diff_find_match with defaults: the instance of target among the children
// of parent (nil: the top of t), the next equal one on every call for duplicate instances.
func (t *Tree) diffFindMatch(parent, target *Node, cache *dupCache) *Node {
	sib := t.childrenOf(parent)
	var m *Node
	switch {
	case target.schema == nil:
		m = opaqNext(sib, target.Name())
	case target.schema.Kind == schema.List || target.schema.Kind == schema.LeafList:
		m = t.findFirst(sib, target)
	default:
		m = t.findSchema(sib, target.schema)
	}
	return t.dupInstNext(m, cache)
}

// diffMergeR is lyd_diff_merge_r for the operations of an implicit diff (create, delete, none):
// src merged under parent; a node left without a change is removed. The diff metadata merge
// (meta-create, …) has nothing to act on in implicit diffs (M6).
func (t *Tree) diffMergeR(src, parent *Node, cache *dupCache) error {
	srcOp, err := t.diffOpOf(src)
	if err != nil {
		return err
	}
	dn := t.diffFindMatch(parent, src, cache)
	var cur diffOp
	if dn != nil {
		if cur, err = t.diffOpOf(dn); err != nil {
			return err
		}
		if srcOp == diffCreate && cur == diffCreate && isDupInstList(dn.schema) {
			dn = nil // another duplicate instance created: added as a new node
		}
	}
	if dn != nil {
		switch srcOp {
		case diffCreate:
			err = t.diffMergeCreate(dn, cur, src)
		case diffDelete:
			err = t.diffMergeDelete(dn, cur, src)
		case diffNone:
			err = t.diffMergeNone(dn, cur, src)
		default:
			err = &opError{"LY_EINT", "Internal error."} // replace comes only from lyd_diff (M6)
		}
		if err != nil {
			return err // libyang also logs "Merging operation \"%s\" failed."
		}
		parent = dn
		if !isDupInstList(src.schema) {
			childCache := &dupCache{}
			for _, c := range src.kids.nodes() {
				if c.isKey() { // lyd_child_no_keys
					continue
				}
				if err := t.diffMergeR(c, parent, childCache); err != nil {
					return err
				}
			}
		}
	} else {
		opts := dupRecursive | dupWithFlags
		order := insertDefault
		if isUserOrdered(src.schema) {
			opts |= dupNoLyds
			order = insertLastBySchema
		}
		if dn, err = t.dupR(src, parent, false, order, opts); err != nil {
			return err
		}
		if parent == nil {
			t.insert(nil, dn, insertLastBySchema) // lyd_diff_insert_sibling
		}
		if err := t.diffChangeOp(dn, srcOp); err != nil {
			return err
		}
		parent = dn
	}
	if t.diffIsRedundant(parent) {
		unlink(parent)
	}
	return nil
}

// diffMergeOpErr is LOGERR_MERGEOP.
func (t *Tree) diffMergeOpErr(n *Node, src, trg diffOp) error {
	return &opError{"LY_EINVAL", fmt.Sprintf("Unable to merge operation \"%s\" with \"%s\" for node \"%s\".", trg, src, lydPath(t.set, n, false))}
}

// metaErr is LOGERR_META.
func (t *Tree) metaErr(name string, n *Node) error {
	return &opError{"LY_EINVAL", fmt.Sprintf("Failed to find metadata \"%s\" for node \"%s\".", name, lydPath(t.set, n, false))}
}

// diffMergeCreate is lyd_diff_merge_create without LYD_DIFF_MERGE_DEFAULTS (the opaque-node
// replace has no implicit diff to come from).
func (t *Tree) diffMergeCreate(dm *Node, cur diffOp, src *Node) error {
	if cur != diffDelete {
		return t.diffMergeOpErr(dm, diffCreate, cur)
	}
	trgFlags := dm.flags
	switch {
	case isUserOrdered(src.schema):
		name, orig := "value", "orig-value"
		switch {
		case isDupInstList(dm.schema):
			name, orig = "position", "orig-position"
		case dm.schema.Kind == schema.List:
			name, orig = "key", "orig-key"
		}
		mi := findYangMeta(src, name)
		if mi < 0 {
			return t.metaErr("yang:"+name, src)
		}
		oi := findYangMeta(dm, orig)
		if oi < 0 {
			return t.metaErr("yang:"+orig, dm)
		}
		if src.meta[mi].value.Canonical() != dm.meta[oi].value.Canonical() {
			if err := t.diffChangeOp(dm, diffReplace); err != nil { // created at another position
				return err
			}
			anchor := *src.meta[mi] // lyd_dup_meta_single
			dm.meta = append(dm.meta, &anchor)
			// the anchors of later creates count it: moved after the instances (lyd_insert_after)
			if last := lastInst(dm); last != dm {
				if err := t.insertAfter(last, dm); err != nil {
					return err
				}
			}
		} else {
			if err := t.diffChangeOp(dm, diffNone); err != nil {
				return err
			}
			delYangMeta(dm, orig)
		}
	case src.schema.Kind == schema.Leaf:
		if compareSingle(t, dm, src, false) {
			if err := t.diffChangeOp(dm, diffNone); err != nil { // deleted + created
				return err
			}
		} else {
			if err := t.diffChangeOp(dm, diffReplace); err != nil { // created with another value
				return err
			}
			if err := t.yangMeta(dm, "orig-value", dm.value.Canonical()); err != nil {
				return err
			}
			t.changeTermVal(dm, src.value, false)
		}
	default:
		if err := t.diffChangeOp(dm, diffNone); err != nil {
			return err
		}
	}
	if dm.isTerm() {
		if err := t.yangMeta(dm, "orig-default", strconv.FormatBool(trgFlags&FlagDefault != 0)); err != nil {
			return err
		}
		dm.flags = dm.flags&^FlagDefault | src.flags&FlagDefault
	}
	for _, c := range dm.kids.nodes() { // the children stay deleted
		if !c.isKey() {
			if err := t.diffChangeOp(c, diffDelete); err != nil {
				return err
			}
		}
	}
	return nil
}

// lastInst is the last instance of n's schema node from n on.
func lastInst(n *Node) *Node {
	sib, last := n.siblingsOf(), n
	for i := sib.indexOf(sib.list, n) + 1; i < len(sib.list) && sib.list[i].schema == n.schema; i++ {
		last = sib.list[i]
	}
	return last
}

// diffMergeDelete is lyd_diff_merge_delete.
func (t *Tree) diffMergeDelete(dm *Node, cur diffOp, src *Node) error {
	if !compareSingle(t, dm, src, false) {
		return &opError{"LY_EINT", "Internal error."} // only an existing node can be deleted
	}
	switch cur {
	case diffCreate: // created, then deleted
		if err := t.diffChangeOp(dm, diffNone); err != nil {
			return err
		}
		if dm.isTerm() {
			if err := t.yangMeta(dm, "orig-default", strconv.FormatBool(src.flags&FlagDefault != 0)); err != nil {
				return err
			}
		}
	case diffReplace:
		name := "orig-value"
		switch {
		case isDupInstList(dm.schema):
			name = "position"
		case dm.schema.Kind == schema.List && isUserOrdered(dm.schema):
			name = "key"
		case isUserOrdered(dm.schema):
			name = "value"
		default: // a leaf: back to the original value and default flag
			oi := findYangMeta(dm, "orig-value")
			if oi < 0 {
				return t.metaErr("yang:orig-value", dm)
			}
			v, d := types.Store(dm.schema.Type, dm.meta[oi].value.Canonical(), types.FormatJSON, types.HintData,
				types.ModuleNames{Set: t.set}, dm.schema)
			if d != nil {
				return &opError{"LY_EINVAL", fmt.Sprintf("Unexpected value of node \"%s\" in target diff.", lydPath(t.set, dm, false))}
			}
			t.changeTermVal(dm, v, false)
			di := findYangMeta(dm, "orig-default")
			if di < 0 {
				return t.metaErr("yang:orig-default", dm)
			}
			dm.flags &^= FlagDefault
			if dm.meta[di].value.Bool() {
				dm.flags |= FlagDefault
			}
			delYangMeta(dm, "orig-default")
		}
		delYangMeta(dm, name)
		if err := t.diffChangeOp(dm, diffDelete); err != nil {
			return err
		}
	case diffNone: // not modified, now deleted
		if err := t.diffChangeOp(dm, diffDelete); err != nil {
			return err
		}
	default:
		return t.diffMergeOpErr(dm, diffDelete, cur)
	}
	if isDupInstList(dm.schema) {
		return nil // a key-less list: all the descendants act as keys
	}
	for _, c := range dm.kids.nodes() { // the descendants yet to be merged keep the old operation
		if c.isKey() || findYangMeta(c, "operation") >= 0 {
			continue
		}
		var found *Node
		switch {
		case c.schema == nil:
			found = opaqNext(&src.kids, c.Name())
		case c.schema.Kind == schema.List || c.schema.Kind == schema.LeafList:
			found = t.findFirst(&src.kids, c)
		default:
			found = t.findSchema(&src.kids, c.schema)
		}
		if found != nil {
			if err := t.diffChangeOp(c, cur); err != nil {
				return err
			}
		}
	}
	return nil
}

// diffMergeNone is lyd_diff_merge_none: on a term only its default flag changed.
func (t *Tree) diffMergeNone(dm *Node, cur diffOp, src *Node) error {
	if cur == diffDelete {
		return t.diffMergeOpErr(dm, diffNone, cur)
	}
	if src.isTerm() {
		dm.flags = dm.flags&^FlagDefault | src.flags&FlagDefault
	}
	return nil
}

// diffIsRedundant is lyd_diff_is_redundant for implicit diffs: a none without children, or a none
// on a term whose default flag did not change. (A user-ordered replace, which needs
// lyd_diff_is_redundant_userord_move, cannot come from the validation: its create merge fails for
// lack of the orig anchor first.)
func (t *Tree) diffIsRedundant(n *Node) bool {
	op, found := diffGetOp(n)
	if !found {
		return false // LY_CHECK_RET(…, 0)
	}
	if op != diffNone {
		return false
	}
	if n.schema == nil {
		return true // an opaque node with none
	}
	for _, m := range n.meta { // lyd_diff_is_redundant_meta: diff metadata on the node
		if strings.HasPrefix(m.name, "meta-") {
			return false
		}
	}
	for _, k := range n.kids.list { // … and on its keys
		if !k.isKey() {
			break
		}
		for _, m := range k.meta {
			if strings.HasPrefix(m.name, "meta-") {
				return false
			}
		}
	}
	if n.isTerm() {
		i := findYangMeta(n, "orig-default")
		return i >= 0 && n.meta[i].value.Bool() == (n.flags&FlagDefault != 0)
	}
	if isDupInstList(n.schema) {
		return true // all its descendants are keys
	}
	for _, c := range n.kids.nodes() {
		if !c.isKey() {
			return false
		}
	}
	return true
}
