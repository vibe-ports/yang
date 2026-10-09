// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/diff.c (lyd_diff_merge_all, lyd_diff_merge_module,
// lyd_diff_merge_tree, lyd_diff_merge_replace, lyd_diff_propagate_meta,
// lyd_diff_is_redundant_userord_move, lyd_diff_merge_metadata, lyd_diff_merge_metadata_r)
// (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"
	"slices"

	"github.com/vibe-ports/yang/internal/schema"
)

// MergeDiffOptions are the options of MergeDiff and MergeDiffTree.
type MergeDiffOptions struct {
	// Defaults treats default nodes in the diffs as possibly explicitly changed: a leaf created
	// with its default value over its delete is no change (LYD_DIFF_MERGE_DEFAULTS).
	Defaults bool
	// Module, when set, merges only the top-level subtrees of the source diff of the implemented
	// module of this name (lyd_diff_merge_module); MergeDiffTree ignores it.
	Module string
	// Callback, when set, is called after every source node is merged with the diff node it
	// changed, and with a nil source for a node copied into the diff whole (lyd_diff_cb). An
	// error stops the merge and is returned. It must not modify the diff being merged into or the
	// source diff.
	Callback func(src, diff *Node) error
}

// MergeDiff is lyd_diff_merge_all (lyd_diff_merge_module with o.Module): the diff src merged into
// the diff t, so that t turns the data t applied to into the data src applied to. Operations on
// the same node combine (create then delete is no change, delete then create a replace or none,
// replace then replace one replace, user-ordered moves fold into one move), nodes left without a
// change are removed, and src is not changed. A nil src merges nothing. Errors are a
// *ValidationError with libyang's diagnostics; t may be partly merged then.
//
// Limitations inherited from libyang (mirrored on purpose, not port bugs): merging diffs that
// create, delete or move user-ordered list or leaf-list instances is lossy.
//   - The merged diff can name an anchor instance that no longer exists. A create in t followed
//     by a delete in src cancels out and the instance leaves the diff, but a later instance whose
//     yang:value, yang:key or yang:position anchor names it keeps that anchor. Applying the merged
//     diff then fails with LY_EINVAL `Node "ll" instance to insert next to not found.`
//     (leaf-list [x] -> [y z] -> [w z]: z keeps yang:value="y"). Other sequences fail in apply
//     with LY_EINVAL from the sibling check of lyd_insert_before ([w x] -> [w] -> [x]).
//   - The merge itself fails when t deletes a user-ordered instance inside a deleted subtree (such
//     instances carry no orig-value, orig-key or orig-position) and src creates it again:
//     lyd_diff_merge_create returns LY_EINVAL with `Failed to find metadata "yang:orig-value" for
//     node "/ex:c/ll[.='w']".` and `Merging operation "create" failed.` (leaf-list [w] -> [] ->
//     [w], where the first diff deletes the container holding it).
//
// Workaround: keep the original tree and diff it against the final one instead of merging the
// intermediate diffs: Diff(first, last, DiffOptions{Defaults: true}), with Defaults for the reason
// given at ReverseDiff.
func (t *Tree) MergeDiff(src *Tree, o MergeDiffOptions) error {
	lg := &logger{set: t.set}
	if src == nil {
		return nil
	}
	mo := mergeOpts{defaults: o.Defaults, cb: o.Callback}
	if o.Module != "" {
		if mo.mod = t.set.Implemented(o.Module); mo.mod == nil {
			return lg.done(lg.logErr("LY_EINVAL", "Module \"%s\" not found.", o.Module))
		}
	}
	return diffDone(lg, t.diffMergeModule(src, mo))
}

// MergeDiffTree is lyd_diff_merge_tree: the subtree src of a source diff merged into the diff t
// under parent, a node of t (nil: the top level of t; another tree's node is LY_EINVAL); see
// MergeDiff, whose limitations for user-ordered instances apply here too. A nil src merges
// nothing.
func (t *Tree) MergeDiffTree(parent, src *Node, o MergeDiffOptions) error {
	lg := &logger{set: t.set}
	if src == nil {
		return nil
	}
	if parent != nil && treeOf(parent) != t {
		return diffDone(lg, argErr("parent (not a node of the diff)", "lyd_diff_merge_tree"))
	}
	err := t.diffMergeR(src, parent, &dupCache{}, mergeOpts{defaults: o.Defaults, cb: o.Callback})
	return diffDone(lg, err)
}

// anchorNames are the user-ordered anchor metadata of the instances of sn: position for key-less
// lists and state leaf-lists, key for lists, value for leaf-lists.
func anchorNames(sn *schema.Node) (name, orig string) {
	switch {
	case isDupInstList(sn):
		return "position", "orig-position"
	case sn.Kind == schema.List:
		return "key", "orig-key"
	}
	return "value", "orig-value"
}

// copyYangMeta is lyd_dup_meta_single of the metadata name of the module yang of src onto n.
func copyYangMeta(src, n *Node, name string) bool {
	i := findYangMeta(src, name)
	if i < 0 {
		return false
	}
	m := *src.meta[i]
	n.meta = append(n.meta, &m)
	return true
}

// diffMergeReplace is lyd_diff_merge_replace: the source operation replace merged into dm.
func (t *Tree) diffMergeReplace(dm *Node, cur diffOp, src *Node) error {
	switch cur {
	case diffReplace, diffCreate:
		switch dm.schema.Kind {
		case schema.List, schema.LeafList:
			// created or moved somewhere, now moved elsewhere: the anchor replaced, the original
			// one kept
			name, _ := anchorNames(dm.schema)
			if err := t.propagateMeta(dm, name); err != nil {
				return err
			}
			delYangMeta(dm, name)
			if !copyYangMeta(src, dm, name) {
				return t.metaErr(name, src)
			}
		case schema.Leaf:
			if compareSingle(t, dm, src, false) {
				return &opError{"LY_EINVAL", fmt.Sprintf("Unexpected value of node \"%s\" in target diff.", lydPath(t.set, dm, false))}
			}
			if t.changeTerm(dm, src.value.Canonical()) != "" {
				return &opError{"LY_EINT", "Internal error."}
			}
			if cur == diffReplace {
				// back to the original value: no change at all
				oi := findYangMeta(dm, "orig-value")
				if oi < 0 {
					return t.metaErr("orig-value", dm)
				}
				lg := &logger{set: t.set}
				eq, err := lg.valueCompare(dm, dm.meta[oi].value.Canonical())
				if err != nil {
					return &opError{lg.diags[0].Err, lg.diags[0].Msg}
				}
				if eq {
					delYangMeta(dm, "orig-value")
					if err := t.diffChangeOp(dm, diffNone); err != nil {
						return err
					}
				}
			}
			dm.flags = dm.flags&^FlagDefault | src.flags&FlagDefault
		default: // anydata, anyxml: their values are M5 (U-0043)
			return fmt.Errorf("data: merging a replace of %s %q: %w", nodetypeStr(dm.schema.Kind), dm.schema.Name, ErrUnsupported)
		}
	case diffNone:
		switch dm.schema.Kind {
		case schema.List: // moved now
			if err := t.diffChangeOp(dm, diffReplace); err != nil {
				return err
			}
			name, orig := "key", "orig-key"
			if isDupInstList(dm.schema) {
				name, orig = "position", "orig-position"
			}
			if !copyYangMeta(src, dm, orig) {
				return t.metaErr(orig, src)
			}
			if !copyYangMeta(src, dm, name) {
				return t.metaErr(name, src)
			}
		case schema.Leaf: // only the default flag changed, now the value too
			if err := t.diffChangeOp(dm, diffReplace); err != nil {
				return err
			}
			if t.changeTerm(dm, src.value.Canonical()) != "" {
				return &opError{"LY_EINT", "Internal error."}
			}
		default:
			return &opError{"LY_EINT", "Internal error."}
		}
	default:
		return t.diffMergeOpErr(dm, diffReplace, cur)
	}
	return nil
}

// propagateMeta is lyd_diff_propagate_meta: the siblings from dm on whose anchor name points at
// dm get dm's anchor, before dm's changes.
func (t *Tree) propagateMeta(dm *Node, name string) error {
	for _, it := range fromNode(dm) {
		i := findYangMeta(it, name)
		if i < 0 {
			continue
		}
		if dm.schema.Kind == schema.LeafList {
			if it.meta[i].value.Canonical() == dm.value.Canonical() {
				delYangMeta(it, name)
				copyYangMeta(dm, it, name)
			}
			continue
		}
		if it.meta[i].value.Canonical() == listPredicate(dm) {
			lg := &logger{set: t.set}
			if _, err := lg.changeMeta(it, it.meta[i], dm.meta[findYangMeta(dm, name)].value.Canonical()); err != nil {
				return &opError{lg.diags[0].Err, lg.diags[0].Msg}
			}
		}
	}
	return nil
}

// isRedundantUserordMove is lyd_diff_is_redundant_userord_move: a user-ordered replace that
// undoes an earlier move of a sibling (each one's anchor is the other's original anchor and each
// points at the other) is redundant. The original and new anchors are different annotations, so
// libyang's "no move" check (lyd_compare_meta of the two) never fires; neither does it here.
func (t *Tree) isRedundantUserordMove(dm, child *Node) bool {
	name, orig := anchorNames(dm.schema)
	oi, vi := findYangMeta(dm, orig), findYangMeta(dm, name)
	if oi < 0 || vi < 0 {
		return false // libyang asserts them
	}
	if compareMeta(dm.meta[oi], dm.meta[vi]) {
		delYangMeta(dm, orig)
		delYangMeta(dm, name)
		if child != nil {
			_ = t.diffChangeOp(dm, diffNone)
			return false
		}
		return true
	}
	sib := dm.siblingsOf()
	all := slices.Concat(sib.list, sib.opq)
	at := slices.Index(all, dm)
	for k := 1; k < len(all); k++ { // the previous siblings, cyclically (prev of the first is the last)
		it := all[(at-k+len(all))%len(all)]
		mi, io := findYangMeta(dm, name), findYangMeta(it, orig)
		if mi < 0 || io < 0 || dm.meta[mi].value.Canonical() != it.meta[io].value.Canonical() {
			continue
		}
		mo, iv := findYangMeta(dm, orig), findYangMeta(it, name)
		if mo < 0 || iv < 0 {
			continue
		}
		origVal, itVal := dm.meta[mo].value.Canonical(), it.meta[iv].value.Canonical()
		if dm.schema.Kind == schema.List {
			if listPredicate(dm) == itVal && listPredicate(it) == origVal {
				return true // a cyclic change
			}
		} else if it.isTerm() && dm.value.Canonical() == itVal && it.value.Canonical() == origVal {
			return true
		}
	}
	return false
}

// metaOfName is the diff metadata of n of the module yang named name (lyd_diff_meta_store).
func metaOfName(n *Node, name string) []*meta {
	var out []*meta
	for _, m := range metasNamed(n, "yang", name) {
		out = append(out, m)
	}
	return out
}

// diffMergeMetadata is lyd_diff_merge_metadata: the diff metadata of src (meta-create,
// meta-delete, meta-replace with meta-orig) merged into those of trg.
func (t *Tree) diffMergeMetadata(src, trg *Node) error {
	trgRepl, trgOrig := metaOfName(trg, "meta-replace"), metaOfName(trg, "meta-orig")
	if err := alignReplaceOrig(trgRepl, trgOrig); err != nil {
		return err
	}
	var srcRepl, srcOrig []*meta
	for _, m := range slices.Clone(src.meta) {
		if m.mod.Name != "yang" {
			continue
		}
		val := m.value.Canonical()
		switch m.name {
		case "meta-create":
			m1 := findMetaVal(trg, "meta-delete", val)
			i := slices.IndexFunc(trgOrig, func(o *meta) bool { return o.value.Canonical() == val })
			switch {
			case m1 != nil: // create + delete: no change
				removeMeta(trg, m1)
			case i >= 0: // create + replace: a create of the new value
				if err := t.metaNew(trg, "meta-create", trgRepl[i].value.Canonical()); err != nil {
					return err
				}
				removeMeta(trg, trgRepl[i])
				removeMeta(trg, trgOrig[i])
				trgRepl = slices.Delete(trgRepl, i, i+1)
				trgOrig = slices.Delete(trgOrig, i, i+1)
			default:
				c := *m
				trg.meta = append(trg.meta, &c)
			}
		case "meta-delete":
			if m1 := findMetaVal(trg, "meta-create", val); m1 != nil { // delete + create: no change
				removeMeta(trg, m1)
			} else {
				c := *m
				trg.meta = append(trg.meta, &c)
			}
		case "meta-replace":
			srcRepl = append(srcRepl, m)
		case "meta-orig":
			srcOrig = append(srcOrig, m)
		}
	}
	if err := alignReplaceOrig(srcRepl, srcOrig); err != nil {
		return err
	}
	for i, r := range srcRepl {
		val := r.value.Canonical()
		m1 := findMetaVal(trg, "meta-delete", val)
		j := slices.IndexFunc(trgOrig, func(o *meta) bool { return o.value.Canonical() == val })
		switch {
		case m1 != nil: // replace + delete: a delete of the original value
			if err := t.metaChange(trg, m1, srcOrig[i].value.Canonical()); err != nil {
				return err
			}
		case j >= 0: // replace + replace: one replace from the original value
			if err := t.metaChange(trg, trgOrig[j], srcOrig[i].value.Canonical()); err != nil {
				return err
			}
		default:
			c, o := *r, *srcOrig[i]
			trg.meta = append(trg.meta, &c, &o)
		}
	}
	return nil
}

// diffMergeMetadataR is lyd_diff_merge_metadata_r: diffMergeMetadata on the nodes and on their
// children in order (with keysOnly the keys only).
func (t *Tree) diffMergeMetadataR(src, trg *Node, keysOnly bool) error {
	if err := t.diffMergeMetadata(src, trg); err != nil {
		return err
	}
	tc := slices.Concat(trg.kids.list, trg.kids.opq)
	for i, c := range slices.Concat(src.kids.list, src.kids.opq) {
		if keysOnly && !c.isKey() {
			break
		}
		if i >= len(tc) {
			return &opError{"LY_EINT", "Internal error."} // libyang dereferences NULL
		}
		if err := t.diffMergeMetadata(c, tc[i]); err != nil {
			return err
		}
	}
	return nil
}

// treeOf is the tree n is linked in, nil for an unlinked node.
func treeOf(n *Node) *Tree {
	for n.parent != nil {
		n = n.parent
	}
	return n.tree
}
