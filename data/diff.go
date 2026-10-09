// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/validation.c (lyd_val_diff_add) and src/diff.c (lyd_diff_add,
// lyd_diff_dup, lyd_diff_add_create_nested_userord, lyd_diff_get_op, lyd_diff_find_meta,
// lyd_diff_del_meta, lyd_diff_insert_sibling, lyd_diff_change_op, lyd_diff_find_match,
// lyd_diff_merge_all, lyd_diff_merge_module, lyd_diff_merge_r, lyd_diff_merge_create,
// lyd_diff_merge_delete, lyd_diff_merge_none, lyd_diff_is_redundant, lyd_diff_is_redundant_meta),
// the subset the implicit diff of the validation needs; lyd_diff_add and lyd_diff_dup in full, the
// diff generation shares them (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
	var a diffAttrs
	str := func(s string) *string { return &s }
	if op == diffCreate && isUserOrdered(n.schema) {
		switch prev := prevInst(n); {
		case isDupInstList(n.schema):
			a.position = str("")
			if pos := listPos(n); pos > 1 {
				a.position = str(strconv.Itoa(pos - 1))
			}
		case n.schema.Kind == schema.List && prev != nil:
			a.key = str(listPredicate(prev))
		case n.schema.Kind == schema.List:
			a.key = str("")
		case prev != nil:
			a.value = str(prev.value.Canonical())
		default:
			a.value = str("")
		}
	}
	src := newTree(t.set)
	if _, err := src.diffAdd(n, op, a); err != nil {
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

// diffOpOf is lyd_diff_get_op without found: no operation is an error (logged LY_EINVAL,
// returned LY_EINT).
func (t *Tree) diffOpOf(n *Node) (diffOp, error) {
	op, ok := diffGetOp(n)
	if !ok {
		return 0, &diffError{items: []*opError{{"LY_EINVAL", fmt.Sprintf("Node \"%s\" without an operation.",
			lydPath(t.set, n, false))}}, rc: "LY_EINT"}
	}
	return op, nil
}

// diffError is a failed diff call: the LOGERR items libyang logs, in order, and the LY_ERR it
// returns when that is not the code of the last item.
type diffError struct {
	items []*opError
	rc    string
}

func (e *diffError) Error() string { return e.items[0].Msg }

// diffDone is the error of a public diff call: the items of a *diffError logged (done logs an
// *opError).
func diffDone(lg *logger, err error) error {
	var de *diffError
	if !errors.As(err, &de) {
		return lg.done(err)
	}
	for _, it := range de.items {
		_ = lg.logErr(it.Err, "%s", it.Msg)
	}
	if de.rc != "" {
		return lg.done(rcError(de.rc))
	}
	return lg.done(errLogged)
}

// diffChangeOp is lyd_diff_change_op.
func (t *Tree) diffChangeOp(n *Node, op diffOp) error {
	delYangMeta(n, "operation")
	return t.yangMeta(n, "operation", op.String())
}

// diffDup is lyd_diff_dup: n (with its subtree, except for the move of a configuration
// user-ordered instance) and its parents up to the schema child of parent, connected to parent
// or, without one, at the top of t; the topmost duplicated parent gets the operation none.
func (t *Tree) diffDup(n *Node, op diffOp, parent *Node) (*Node, error) {
	opts := dupNoMeta | dupWithFlags
	if isUserOrdered(n.schema) {
		opts |= dupNoLyds
	}
	if op != diffReplace || !isUserOrdered(n.schema) || !n.schema.Config {
		opts |= dupRecursive // a move applies to the user-ordered instance only, no descendants
	}
	dup, err := t.dupR(n, nil, false, insertDefault, opts)
	if err != nil {
		return nil, err
	}
	var sparent *schema.Node
	if parent != nil {
		sparent = parent.schema
	}
	top, orig := dup, n
	for top.schema.DataParent() != nil && !compareSchemaEqual(top.schema.DataParent(), sparent, false) {
		orig = orig.parent
		d, err := t.dupR(orig, nil, false, insertDefault, dupNoMeta|dupWithFlags)
		if err != nil {
			return nil, err
		}
		t.insert(d, top, insertDefault)
		top = d
	}
	if parent != nil {
		t.insert(parent, top, insertDefault)
	} else {
		t.insert(nil, top, insertLastBySchema) // lyd_diff_insert_sibling
	}
	if top != dup {
		if err := t.yangMeta(top, "operation", diffNone.String()); err != nil {
			return nil, err
		}
	}
	return dup, nil
}

// diffAttrs are the optional metadata lyd_diff_add puts on a diff node, nil when absent.
type diffAttrs struct {
	origDefault, origValue, key, value, position, origKey, origPosition *string
}

// diffAdd is lyd_diff_add: the copy of n with the operation op and the metadata a added to the
// diff t under the deepest of n's parents t already has (a parent already in t that is n itself
// takes the operation, its children keep theirs, none by default); a created subtree gets the
// anchors of its nested user-ordered instances. It returns the diff node.
func (t *Tree) diffAdd(n *Node, op diffOp, a diffAttrs) (*Node, error) {
	sib := &t.top
	var diffParent, match, parent *Node
	for {
		parent = n
		for parent.parent != nil && (diffParent == nil || parent.parent.schema != diffParent.schema) {
			parent = parent.parent
		}
		if isDupInstList(parent.schema) {
			match = nil // never found: the instances cannot be told apart
			break
		}
		if match = t.findFirst(sib, parent); match == nil {
			break
		}
		diffParent = match
		sib = &match.kids
		if parent == n {
			break
		}
	}
	var dup *Node
	if match != nil && parent == n {
		// an operation is already on a descendant
		if isUserOrdered(diffParent.schema) {
			// moved to the end of its instances, where it is expected
			if last := lastInst(diffParent); last != diffParent {
				if err := t.insertAfter(last, diffParent); err != nil {
					return nil, err
				}
			}
		}
		delYangMeta(diffParent, "operation")
		for _, c := range diffParent.kids.nodes() {
			if c.isKey() || findYangMeta(c, "operation") >= 0 {
				continue
			}
			if err := t.yangMeta(c, "operation", diffNone.String()); err != nil {
				return nil, err
			}
		}
		dup = diffParent
	} else {
		var err error
		if dup, err = t.diffDup(n, op, diffParent); err != nil {
			return nil, err
		}
	}
	if cur, found := diffGetOp(dup); !found || cur != op {
		if err := t.yangMeta(dup, "operation", op.String()); err != nil {
			return nil, err
		}
	}
	if op == diffCreate {
		for e := range dup.All() {
			if e != dup && isUserOrdered(e.schema) {
				if err := t.diffCreateNestedUserord(e); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, m := range []struct {
		name string
		val  *string
	}{{"orig-default", a.origDefault}, {"orig-value", a.origValue}, {"key", a.key}, {"value", a.value},
		{"position", a.position}, {"orig-key", a.origKey}, {"orig-position", a.origPosition}} {
		if m.val != nil {
			if err := t.yangMeta(dup, m.name, *m.val); err != nil {
				return nil, err
			}
		}
	}
	return dup, nil
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

// diffMergeAll is lyd_diff_merge_all of the diff src into t without options.
func (t *Tree) diffMergeAll(src *Tree) error {
	return t.diffMergeModule(src, mergeOpts{})
}

// mergeOpts are the arguments of lyd_diff_merge_module besides the diffs: LYD_DIFF_MERGE_DEFAULTS,
// the module filter (nil: all) and the callback.
type mergeOpts struct {
	defaults bool
	mod      *schema.Module
	cb       func(src, diff *Node) error
}

// diffMergeModule is lyd_diff_merge_module: the top-level subtrees of src (of o.mod only, when
// set) merged into the diff t.
func (t *Tree) diffMergeModule(src *Tree, o mergeOpts) error {
	cache := &dupCache{}
	for _, n := range src.top.nodes() {
		if o.mod != nil && nodeModule(t.set, n) != o.mod {
			continue // data of another module
		}
		if err := t.diffMergeR(n, nil, cache, o); err != nil {
			return err
		}
	}
	return nil
}

// diffFindMatch is lyd_diff_find_match with defaults among the children of parent (nil: the top
// of t).
func (t *Tree) diffFindMatch(parent, target *Node, cache *dupCache) *Node {
	return t.findMatch(t.childrenOf(parent), target, true, cache)
}

// diffMergeR is lyd_diff_merge_r: src merged under parent (nil: the top of t); a node left
// without a change is removed.
func (t *Tree) diffMergeR(src, parent *Node, cache *dupCache, o mergeOpts) error {
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
		case diffReplace:
			err = t.diffMergeReplace(dn, cur, src)
		case diffCreate:
			dn, err = t.diffMergeCreate(dn, cur, src, o.defaults)
		case diffDelete:
			err = t.diffMergeDelete(dn, cur, src)
		case diffNone:
			err = t.diffMergeNone(dn, cur, src)
		}
		if err != nil {
			de := &diffError{}
			var oe *opError
			switch {
			case errors.As(err, &de):
				de = &diffError{items: slices.Clone(de.items), rc: de.rc}
			case errors.As(err, &oe):
				de = &diffError{items: []*opError{oe}}
			default:
				return err
			}
			if de.rc == "" {
				de.rc = de.items[len(de.items)-1].Err
			}
			de.items = append(de.items, &opError{"LY_EOTHER", fmt.Sprintf("Merging operation \"%s\" failed.", srcOp)})
			return de
		}
		if err := t.diffMergeMetadataR(src, dn, !isDupInstList(src.schema)); err != nil {
			return err
		}
		if o.cb != nil {
			if err := o.cb(src, dn); err != nil {
				return err
			}
		}
		parent = dn
		if !isDupInstList(src.schema) { // a key-less list: all its descendants act as keys
			childCache := &dupCache{}
			for _, c := range src.kids.nodes() {
				if c.isKey() { // lyd_child_no_keys
					continue
				}
				if err := t.diffMergeR(c, parent, childCache, o); err != nil {
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
		if o.cb != nil {
			if err := o.cb(nil, dn); err != nil {
				return err
			}
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

// diffMergeCreate is lyd_diff_merge_create; with defaults (LYD_DIFF_MERGE_DEFAULTS) a leaf
// created with its schema default over its delete is no change. It returns the diff node, which
// stays dm (the opaque-node replace has no yang attributes to come from here, see diffGetOp).
func (t *Tree) diffMergeCreate(dm *Node, cur diffOp, src *Node, defaults bool) (*Node, error) {
	if src.schema == nil {
		return nil, &opError{"LY_EINT", "Internal error."}
	}
	if cur != diffDelete {
		return nil, t.diffMergeOpErr(dm, diffCreate, cur)
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
			return nil, t.metaErr("yang:"+name, src)
		}
		oi := findYangMeta(dm, orig)
		if oi < 0 {
			return nil, t.metaErr("yang:"+orig, dm)
		}
		if src.meta[mi].value.Canonical() != dm.meta[oi].value.Canonical() {
			if err := t.diffChangeOp(dm, diffReplace); err != nil { // created at another position
				return nil, err
			}
			anchor := *src.meta[mi] // lyd_dup_meta_single
			dm.meta = append(dm.meta, &anchor)
			// the anchors of later creates count it: moved after the instances (lyd_insert_after)
			if last := lastInst(dm); last != dm {
				if err := t.insertAfter(last, dm); err != nil {
					return nil, err
				}
			}
		} else {
			if err := t.diffChangeOp(dm, diffNone); err != nil {
				return nil, err
			}
			delYangMeta(dm, orig)
		}
	case src.schema.Kind == schema.Leaf:
		switch {
		case defaults && len(src.schema.Default) > 0 && defaultEquals(src):
			// deleted, so its default was in use, and it is created with that value
			if err := t.diffChangeOp(dm, diffNone); err != nil {
				return nil, err
			}
		case compareSingle(t, dm, src, false):
			if err := t.diffChangeOp(dm, diffNone); err != nil { // deleted + created
				return nil, err
			}
		default:
			if err := t.diffChangeOp(dm, diffReplace); err != nil { // created with another value
				return nil, err
			}
			if err := t.yangMeta(dm, "orig-value", dm.value.Canonical()); err != nil {
				return nil, err
			}
			t.changeTermVal(dm, src.value, false)
		}
	default:
		if err := t.diffChangeOp(dm, diffNone); err != nil {
			return nil, err
		}
	}
	if dm.isTerm() {
		if err := t.yangMeta(dm, "orig-default", strconv.FormatBool(trgFlags&FlagDefault != 0)); err != nil {
			return nil, err
		}
		dm.flags = dm.flags&^FlagDefault | src.flags&FlagDefault
	}
	for _, c := range dm.kids.nodes() { // the children stay deleted
		if !c.isKey() {
			if err := t.diffChangeOp(c, diffDelete); err != nil {
				return nil, err
			}
		}
	}
	return dm, nil
}

// defaultEquals is !lysc_value_cmp of a leaf's schema default and n's value: the canonical texts
// equal.
func defaultEquals(n *Node) bool {
	v, d := types.StoreDefault(n.schema, n.schema.Default[0])
	return d == nil && v.Canonical() == n.value.Canonical()
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
			if t.changeTerm(dm, dm.meta[oi].value.Canonical()) != "" {
				return &opError{"LY_EINVAL", fmt.Sprintf("Unexpected value of node \"%s\" in target diff.", lydPath(t.set, dm, false))}
			}
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

// diffIsRedundant is lyd_diff_is_redundant: a user-ordered move that is no move or undoes an
// earlier one, a none without children (all descendants of a key-less list are keys), or a none
// on a term whose default flag did not change; a none with diff metadata stays.
func (t *Tree) diffIsRedundant(n *Node) bool {
	op, found := diffGetOp(n)
	if !found {
		return false // LY_CHECK_RET(…, 0)
	}
	var child *Node
	if !isDupInstList(n.schema) {
		child = firstNoKeys(n)
	}
	switch {
	case op == diffReplace && isUserOrdered(n.schema):
		if t.isRedundantUserordMove(n, child) {
			return true
		}
	case op == diffNone:
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
	}
	return child == nil && op == diffNone
}
