// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/diff.c (lyd_diff_merge_all, lyd_diff_merge_module,
// lyd_diff_merge_tree, lyd_diff_merge_replace, lyd_diff_propagate_meta,
// lyd_diff_is_redundant_userord_move, lyd_diff_merge_metadata, lyd_diff_merge_metadata_r)
// (BSD-3-Clause, © CESNET).

package data

import (
	"errors"
	"fmt"
	"slices"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

type diffLogEntry struct {
	set  *schema.Set
	diag yang.Diagnostic
}

// checkedYangModule is the explicit-module form of lyd_find_meta. Each call
// performs its own context check, including repeated sibling lookups.
func (t *Tree) checkedYangModule(n *Node, mod *schema.Module, name string) int {
	if n == nil || len(n.meta) == 0 {
		return -1
	}
	set := n.meta[0].mod.Owner
	if set == nil { // hand-built modules have no owner
		set = ctxOf(n)
	}
	lg := &logger{set: set}
	m, _ := lg.findMeta(n.meta, mod, name)
	for _, d := range lg.diags {
		t.diffLogs = append(t.diffLogs, diffLogEntry{set, d})
	}
	if m == nil {
		return -1
	}
	return slices.Index(n.meta, m)
}

func (t *Tree) withDiffLogs(err error) error {
	var ve *ValidationError
	if errors.As(err, &ve) && len(t.diffLogs) > 0 {
		diags := make([]yang.Diagnostic, 0, len(t.diffLogs)+len(ve.Diags))
		for _, item := range t.diffLogs {
			diags = append(diags, item.diag)
		}
		ve.Diags = append(diags, ve.Diags...)
	}
	return err
}

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
	// source diff: a change of the diff merged into, or of a node detached from it that still
	// keeps its counter, is refused (see the package doc).
	Callback func(src, diff *Node) error
}

// MergeDiff is lyd_diff_merge_all (lyd_diff_merge_module with o.Module): the diff src merged into
// the diff t, so that t turns the data t applied to into the data src applied to. Operations on
// the same node combine (create then delete is no change, delete then create a replace or none,
// replace then replace one replace, user-ordered moves fold into one move), nodes left without a
// change are removed, and src is not changed. A nil src normally merges nothing. Errors are a
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
// given at ReverseDiff. An operation's children in a diff tree may be split by schema-anchor
// insertion; MergeDiff accepts that order, as libyang does.
func (t *Tree) MergeDiff(src *Tree, o MergeDiffOptions) (err error) {
	defer normalizeRefusal(t.context(), &err, nil) // the guard's refusal in its one public form, after the journal ended
	if t.work.inProgress() {                       // an operation in progress governs t: refused before its counter is used
		return refusedEntry(t.context())
	}
	var mod *schema.Module
	if o.Module != "" {
		mod = t.context().Implemented(o.Module)
	}
	return t.mergeDiffWithModule(src, o, mod)
}

func (t *Tree) mergeDiffWithModule(src *Tree, o MergeDiffOptions, mod *schema.Module) (result error) {
	defer func() { result = restoreUser(result) }() // a callback's own error, after the state ended
	defer t.work.beginHashWork()()
	t.diffLogs = nil
	lg := &logger{set: t.context()}
	if src == nil {
		return nil
	}
	mo := mergeOpts{defaults: o.Defaults, cb: o.Callback}
	if o.Module != "" {
		if mod == nil {
			return lg.done(lg.logErr("LY_EINVAL", "Module \"%s\" not found.", o.Module))
		}
		mo.mod = mod
	}
	journal := beginHashTransaction(t)
	defer journal.end(&result)
	err := t.diffMergeModule(src, mo)
	if t.work.hashErr != nil {
		err = t.work.hashErr
	}
	return t.withDiffLogs(diffDone(lg, err))
}

// MergeDiffTree is lyd_diff_merge_tree: the subtree src of a source diff merged into the diff t
// under parent, a node of t or a detached node of its snapshot (nil: the top level of t; another
// tree's node is LY_EINVAL). An attached parent supplies the destination context even when a
// different root is first in t; see
// MergeDiff, whose limitations for user-ordered instances apply here too. A nil src normally
// merges nothing. Operation-child runs in either diff may be split by schema-anchor insertion
// and are accepted in that stored order, as in libyang.
func (t *Tree) MergeDiffTree(parent, src *Node, o MergeDiffOptions) (result error) {
	defer normalizeRefusal(t.context(), &result, nil) // the guard's refusal in its one public form, after the journal ended
	if t.work.inProgress() || heldNode(parent) {      // t, or a node argument, is held by an operation waiting on user code: refused before any counter is used
		return refusedEntry(t.context())
	}
	defer func() { result = restoreUser(result) }() // a callback's own error, after the state ended
	defer t.work.beginHashWork()()
	t.diffLogs = nil
	dest := t.context()
	if parent != nil && ctxOf(parent) != nil {
		dest = ctxOf(parent)
	}
	lg := &logger{set: dest}
	if src == nil {
		return nil
	}
	if parent != nil && treeOf(parent) != nil && treeOf(parent) != t {
		return diffDone(lg, argErr("parent (not a node of the diff)", "lyd_diff_merge_tree"))
	}
	journal := beginHashTransaction(t)
	defer journal.end(&result)
	if err := t.work.journal.captureDetachedParent(parent); err != nil {
		return err
	}
	err := t.diffMergeR(src, parent, &dupCache{}, mergeOpts{defaults: o.Defaults, cb: o.Callback})
	if t.work.hashErr != nil {
		err = t.work.hashErr
	}
	return t.withDiffLogs(diffDone(lg, err))
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
func copyYangMeta(src, n *Node, name string, mod *schema.Module) (bool, error) {
	i := findYangModule(src, mod, name)
	if i < 0 {
		return false, nil
	}
	m := *src.meta[i]
	if err := n.touch(); err != nil {
		return false, err
	}
	n.meta = append(n.meta, &m)
	return true, nil
}

// diffMergeReplace is lyd_diff_merge_replace: the source operation replace merged into dm.
func (t *Tree) diffMergeReplace(dm *Node, cur diffOp, src *Node) error {
	// src/diff.c:2060-2063 selects the latest yang revision in the target context.
	mod := latestModule(ctxOf(dm), "yang")
	switch cur {
	case diffReplace, diffCreate:
		if dm.schema == nil {
			return &opError{"LY_EINVAL", fmt.Sprintf("Operation \"replace\" is invalid for opaque node \"%s\".", src.Name())}
		}
		switch dm.schema.Kind {
		case schema.List, schema.LeafList:
			// created or moved somewhere, now moved elsewhere: the anchor replaced, the original
			// one kept
			name, _ := anchorNames(dm.schema)
			if err := t.propagateMeta(dm, name, mod); err != nil {
				return err
			}
			if err := delYangMeta(dm, name); err != nil {
				return err
			}
			if i := t.checkedYangModule(src, mod, name); i >= 0 {
				m := *src.meta[i]
				if err := dm.touch(); err != nil { // delYangMeta captured dm only if it had an anchor
					return err
				}
				dm.meta = append(dm.meta, &m)
			} else {
				return t.metaErr(name, src)
			}
		case schema.Leaf:
			equal, err := compareSingleErr(t, dm, src, false)
			if err != nil {
				return err
			}
			if equal {
				return &opError{"LY_EINVAL", fmt.Sprintf("Unexpected value of node \"%s\" in target diff.", lydPath(ctxOf(dm), dm, false))}
			}
			if t.changeTerm(dm, src.value.Canonical()) != "" {
				return &opError{"LY_EINT", "Internal error."}
			}
			if cur == diffReplace {
				// back to the original value: no change at all
				oi := t.checkedYangModule(dm, mod, "orig-value")
				if oi < 0 {
					return t.metaErr("orig-value", dm)
				}
				lg := &logger{set: ctxOf(dm)}
				eq, err := lg.valueCompare(dm, dm.meta[oi].value.Canonical())
				if err != nil {
					return diagErr(lg, err)
				}
				if eq {
					if err := delYangMeta(dm, "orig-value"); err != nil {
						return err
					}
					if err := t.diffChangeOp(dm, diffNone); err != nil {
						return err
					}
				}
			}
			if err := dm.touch(); err != nil {
				return err
			}
			dm.flags = dm.flags&^FlagDefault | src.flags&FlagDefault
		default: // anydata, anyxml values await the public diff surface (U-0112)
			return fmt.Errorf("data: merging a replace of %s %q: %w", nodetypeStr(dm.schema.Kind), dm.schema.Name, ErrUnsupported)
		}
	case diffNone:
		if dm.schema == nil {
			return &opError{"LY_EINVAL", fmt.Sprintf("Operation \"replace\" is invalid for opaque node \"%s\".", src.Name())}
		}
		switch dm.schema.Kind {
		case schema.List: // moved now
			if err := t.diffChangeOp(dm, diffReplace); err != nil {
				return err
			}
			name, orig := "key", "orig-key"
			if isDupInstList(dm.schema) {
				name, orig = "position", "orig-position"
			}
			_ = t.checkedYangModule(src, mod, orig)
			if ok, err := copyYangMeta(src, dm, orig, mod); err != nil {
				return err
			} else if !ok {
				return t.metaErr(orig, src)
			}
			_ = t.checkedYangModule(src, mod, name)
			if ok, err := copyYangMeta(src, dm, name, mod); err != nil {
				return err
			} else if !ok {
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
func (t *Tree) propagateMeta(dm *Node, name string, mod *schema.Module) error {
	for _, it := range fromNode(dm) {
		i := t.checkedYangModule(it, mod, name)
		if i < 0 {
			continue
		}
		if dm.schema.Kind == schema.LeafList {
			if it.meta[i].value.Canonical() == dm.value.Canonical() {
				if err := delYangMeta(it, name); err != nil {
					return err
				}
				_ = t.checkedYangModule(dm, mod, name)
				if ok, err := copyYangMeta(dm, it, name, mod); err != nil {
					return err
				} else if !ok {
					// lyd_dup_meta_single(NULL, ...) is LY_EINVAL (diff.c:2192); its LOGARG has
					// no context, so nothing reaches the diff context's log
					return &diffError{rc: "LY_EINVAL"}
				}
			}
			continue
		}
		if it.meta[i].value.Canonical() == listPredicate(dm) {
			lg := &logger{set: ctxOf(dm)}
			// lyd_get_meta_value(NULL) is NULL, which lyd_change_meta takes as "" (diff.c:2203-2205,
			// tree_data_new.c:1321-1323)
			val := ""
			if j := t.checkedYangModule(dm, mod, name); j >= 0 {
				val = dm.meta[j].value.Canonical()
			}
			if _, err := lg.changeMeta(it, it.meta[i], val); err != nil {
				return diagErr(lg, err)
			}
		}
	}
	return nil
}

// isRedundantUserordMove is lyd_diff_is_redundant_userord_move: a user-ordered replace that
// undoes an earlier move of a sibling (each one's anchor is the other's original anchor and each
// points at the other) is redundant. The original and new anchors are different annotations, so
// libyang's "no move" check (lyd_compare_meta of the two) never fires; neither does it here.
func (t *Tree) isRedundantUserordMove(dm, child *Node) (bool, error) {
	name, orig := anchorNames(dm.schema)
	oi, vi := findYangLatest(dm, orig), findYangLatest(dm, name)
	if oi < 0 || vi < 0 {
		return false, nil // libyang asserts them
	}
	if compareMeta(dm.meta[oi], dm.meta[vi]) {
		if err := delYangMeta(dm, orig); err != nil {
			return false, err
		}
		if err := delYangMeta(dm, name); err != nil {
			return false, err
		}
		if child != nil {
			if err := t.diffChangeOp(dm, diffNone); err != nil {
				return false, err
			}
			return false, nil
		}
		return true, nil
	}
	sib := dm.siblingsOf()
	if sib == nil {
		return false, nil
	}
	all := sib.docNodes()
	at := slices.Index(all, dm)
	for k := 1; k < len(all); k++ { // the previous siblings, cyclically (prev of the first is the last)
		it := all[(at-k+len(all))%len(all)]
		mi, io := findYangLatest(dm, name), findYangLatest(it, orig)
		if mi < 0 || io < 0 || dm.meta[mi].value.Canonical() != it.meta[io].value.Canonical() {
			continue
		}
		mo, iv := findYangLatest(dm, orig), findYangLatest(it, name)
		if mo < 0 || iv < 0 {
			continue
		}
		origVal, itVal := dm.meta[mo].value.Canonical(), it.meta[iv].value.Canonical()
		if dm.schema.Kind == schema.List {
			if listPredicate(dm) == itVal && listPredicate(it) == origVal {
				return true, nil // a cyclic change
			}
		} else if it.isTerm() && dm.value.Canonical() == itVal && it.value.Canonical() == origVal {
			return true, nil
		}
	}
	return false, nil
}

// metaOfName collects diff metadata using n's own implemented yang module,
// as lyd_diff_reverse_metadata_diff does (diff.c:3204-3222).
func metaOfName(n *Node, name string) []*meta {
	var mod *schema.Module
	if set := ctxOf(n); set != nil {
		mod = set.Implemented("yang")
	}
	return metaOfModule(n, mod, name)
}

// metaOfModule collects only metadata of the caller-selected module. Merge
// passes the source's implemented yang module for both source and target.
func metaOfModule(n *Node, mod *schema.Module, name string) []*meta {
	var out []*meta
	for _, m := range metasNamedModule(n, mod, name) {
		out = append(out, m)
	}
	return out
}

// diffMergeMetadata is lyd_diff_merge_metadata: the diff metadata of src (meta-create,
// meta-delete, meta-replace with meta-orig) merged into those of trg.
func (t *Tree) diffMergeMetadata(src, trg *Node) error {
	mod := ctxOf(src).Implemented("yang")
	trgRepl, trgOrig := metaOfModule(trg, mod, "meta-replace"), metaOfModule(trg, mod, "meta-orig")
	if err := alignReplaceOrig(trgRepl, trgOrig); err != nil {
		return err
	}
	var srcRepl, srcOrig []*meta
	for _, m := range slices.Clone(src.meta) {
		if m.mod != mod {
			continue
		}
		val := m.value.Canonical()
		switch m.name {
		case "meta-create":
			m1 := findMetaVal(trg, "meta-delete", val)
			i := slices.IndexFunc(trgOrig, func(o *meta) bool { return o.value.Canonical() == val })
			switch {
			case m1 != nil: // create + delete: no change
				if err := removeMeta(trg, m1); err != nil {
					return err
				}
			case i >= 0: // create + replace: a create of the new value
				// lyd_new_meta(NULL, trg_diff, mod, ...) uses the source's
				// implemented yang module, which must share the target context.
				if !sameSet(mod.Owner, ctxOf(trg)) {
					return &opError{"LY_EINVAL", "Different contexts mixed in a \"lyd_new_meta\" function call."}
				}
				if err := t.metaNew(trg, "meta-create", trgRepl[i].value.Canonical()); err != nil {
					return err
				}
				if err := removeMeta(trg, trgRepl[i]); err != nil {
					return err
				}
				if err := removeMeta(trg, trgOrig[i]); err != nil {
					return err
				}
				trgRepl = slices.Delete(trgRepl, i, i+1)
				trgOrig = slices.Delete(trgOrig, i, i+1)
			default:
				c := *m
				if err := trg.touch(); err != nil {
					return err
				}
				trg.meta = append(trg.meta, &c)
			}
		case "meta-delete":
			if m1 := findMetaVal(trg, "meta-create", val); m1 != nil { // delete + create: no change
				if err := removeMeta(trg, m1); err != nil {
					return err
				}
			} else {
				c := *m
				if err := trg.touch(); err != nil {
					return err
				}
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
			if err := trg.touch(); err != nil {
				return err
			}
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
	tc := trg.kids.docNodes()
	for i, c := range src.kids.docNodes() {
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
