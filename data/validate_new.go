// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/validation.c (lyd_validate_new, lyd_validate_choice_r,
// lyd_validate_cases, lyd_validate_duplicates, lyd_validate_autodel_*, lyd_val_has_default)
// (BSD-3-Clause, © CESNET).

package data

import (
	"iter"
	"slices"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/schema"
)

// linkedIn reports whether n is still linked among sib (autodelete may have removed it).
func (vc *valCtx) linkedIn(n *Node, sib *siblings) bool {
	return n.siblingsOf() == sib
}

// validateNew is lyd_validate_new: of the siblings under parent (the top-level data of mod when
// parent is nil), check the cases of their choices, then every new or default node: superseded
// defaults are deleted, new instances are checked for duplicates and lose FlagNew, defaults of a
// case that no longer has explicit data are deleted.
func (vc *valCtx) validateNew(parent *Node, sparent *schema.Node, mod *schema.Module) error {
	if sparent == nil && parent != nil {
		sparent = parent.schema
	}
	sib := vc.t.childrenOf(parent)
	var rc error
	if err := vc.validateChoices(sib, sparent, mod); err != nil {
		if rc = err; vc.stop(err) {
			return rc
		}
	}
	nodes := sib.schemaSpan().Collect() // autodelete only removes; removed nodes are skipped below
	if mod != nil {
		// lyd_owner_module: an augment's node in mod's top-level choice is mod's data
		kept := nodes[:0]
		for _, n := range nodes {
			if err := vc.checkContext(); err != nil {
				return err
			}
			if ownerModule(vc.t.context(), n) == mod {
				kept = append(kept, n)
			}
		}
		nodes = kept
	}
	dups := dupIndex{sib: sib}
	explicit := map[*schema.Node]bool{}
	var lastDflt *schema.Node
	for _, n := range nodes {
		if err := vc.checkContext(); err != nil {
			return err
		}
		if !vc.linkedIn(n, sib) || n.flags&(FlagNew|FlagDefault) == 0 {
			continue
		}
		if hasDefault(n.schema) && n.schema != lastDflt && n.flags&FlagNew != 0 {
			// remove old default(s) of the new node if an explicit instance exists
			lastDflt = n.schema
			var gone bool
			var err error
			if n.schema.Kind == schema.LeafList {
				gone, err = vc.autodelLeafListDflt(sib, n)
			} else {
				gone, err = vc.autodelContLeafDflt(sib, n)
			}
			if err != nil {
				return err
			}
			if vc.t.work.hashErr != nil {
				return vc.t.work.hashErr
			}
			if gone {
				continue
			}
		}
		if n.flags&FlagNew != 0 {
			if err := vc.duplicates(&dups, n); err != nil {
				if rc = err; vc.stop(err) {
					return rc
				}
			}
			n.touch()
			n.flags &^= FlagNew // this node is valid
		}
		if n.flags&FlagDefault != 0 {
			gone, err := vc.autodelCaseDflt(sib, n, explicit)
			if err != nil {
				return err
			}
			if gone {
				continue
			}
		}
		if vc.t.work.hashErr != nil {
			return vc.t.work.hashErr
		}
	}
	return rc
}

// validateChoices is lyd_validate_choice_r: the cases of every choice under sparent, nested
// choices included.
func (vc *valCtx) validateChoices(sib *siblings, sparent *schema.Node, mod *schema.Module) error {
	var rc error
	gn, err := vc.getnextOf(sparent, mod, vc.output)
	if err != nil {
		return err
	}
	for _, ch := range gn.choices {
		if err := vc.checkContext(); err != nil {
			return err
		}
		if sib.SchemaLen() == 0 {
			break
		}
		if err := vc.validateCases(sib, ch); err != nil {
			if rc = err; vc.stop(err) {
				return rc
			}
		}
		if err := vc.validateChoices(sib, ch, mod); err != nil {
			if rc = err; vc.stop(err) {
				return rc
			}
		}
	}
	return rc
}

// validateCases is lyd_validate_cases: data of two cases, both new or both old, is an error (at
// the choice's schema path); old data of a case is deleted when another case has new data.
func (vc *valCtx) validateCases(sib *siblings, ch *schema.Node) error {
	var oldCase, newCase *schema.Node
	for _, cs := range ch.Children {
		if err := vc.checkContext(); err != nil {
			return err
		}
		found := 0
		err := vc.getnextData(sib, cs, func(n *Node) bool {
			if n.flags&FlagNew != 0 {
				found = 2 // a new case data found, nothing more to look for
				return false
			}
			found = 1
			return true
		})
		if err != nil {
			return err
		}
		var prev *schema.Node
		switch found {
		case 1:
			prev, oldCase = oldCase, cs
		case 2:
			prev, newCase = newCase, cs
		default:
			continue
		}
		if prev != nil {
			vc.log.locSet(ch)
			defer vc.log.locBack(1)
			return vc.log.val(nil, "", ly.Data, "Data for both cases \"%s\" and \"%s\" exist.", prev.Name, cs.Name)
		}
	}
	if oldCase != nil && newCase != nil {
		// auto-delete the old case: diff entries in data order, then the nodes
		var del []*Node
		if err := vc.getnextData(sib, oldCase, func(n *Node) bool { del = append(del, n); return true }); err != nil {
			return err
		}
		if err := vc.checkContext(); err != nil {
			return err
		}
		for _, n := range del {
			if vc.diff != nil {
				if err := vc.diff(n, diffDelete); err != nil {
					return err
				}
			}
		}
		if err := vc.t.unlinkAll(del); err != nil {
			return err
		}
		for _, n := range del {
			freeSubtreeLinks(vc.t.context(), n) // lyd_free_tree
		}
	}
	return vc.checkContext()
}

// hasDefault is lyd_val_has_default: a leaf or leaf-list with a default, or an NP container.
func hasDefault(sn *schema.Node) bool {
	switch sn.Kind {
	case schema.Leaf, schema.LeafList:
		return len(sn.Default) > 0
	case schema.Container:
		return !sn.Presence
	}
	return false
}

// instances returns the instances of sn among sib.
func (vc *valCtx) instances(sib *siblings, sn *schema.Node) ([]*Node, error) {
	var inst []*Node
	for n := range vc.t.instances(sib, sn) {
		if err := vc.checkContext(); err != nil {
			return nil, err
		}
		inst = append(inst, n)
	}
	return inst, nil
}

func (vc *valCtx) hasExplicit(inst []*Node) (bool, error) {
	for _, n := range inst {
		if err := vc.checkContext(); err != nil {
			return false, err
		}
		if n.flags&FlagDefault == 0 {
			return true, nil
		}
	}
	return false, nil
}

// autodelLeafListDflt is lyd_validate_autodel_leaflist_dflt: with an explicit instance, every
// default instance goes. It reports whether n itself went.
func (vc *valCtx) autodelLeafListDflt(sib *siblings, n *Node) (bool, error) {
	inst, err := vc.instances(sib, n.schema)
	if err != nil {
		return false, err
	}
	has, err := vc.hasExplicit(inst)
	if err != nil || !has {
		return false, err // no explicit instance, keep defaults as they are
	}
	return vc.autodelDefaults(inst, n)
}

// autodelContLeafDflt is lyd_validate_autodel_cont_leaf_dflt: with an explicit instance, every
// default instance goes; without one, a single old default instance goes.
func (vc *valCtx) autodelContLeafDflt(sib *siblings, n *Node) (bool, error) {
	inst, err := vc.instances(sib, n.schema)
	if err != nil {
		return false, err
	}
	has, err := vc.hasExplicit(inst)
	if err != nil {
		return false, err
	}
	if has {
		return vc.autodelDefaults(inst, n)
	}
	for _, i := range inst {
		if err := vc.checkContext(); err != nil {
			return false, err
		}
		if i.flags&FlagDefault != 0 && i.flags&FlagNew == 0 {
			err := vc.autodel([]*Node{i}, false)
			return i == n, err
		}
	}
	return false, nil
}

// autodelDefaults deletes the default instances of inst in one batch; it reports whether n went.
func (vc *valCtx) autodelDefaults(inst []*Node, n *Node) (bool, error) {
	del := inst[:0]
	gone := false
	for _, i := range inst {
		if err := vc.checkContext(); err != nil {
			return false, err
		}
		if i.flags&FlagDefault != 0 {
			del = append(del, i)
			gone = gone || i == n
		}
	}
	err := vc.autodel(del, false)
	return gone, err
}

// autodelCaseDflt is lyd_validate_autodel_case_dflt: a default node of a non-default case that
// has no explicit data any more goes. Whether a case has explicit data is computed once per
// validateNew call (explicit), deleting defaults does not change it.
func (vc *valCtx) autodelCaseDflt(sib *siblings, n *Node, explicit map[*schema.Node]bool) (bool, error) {
	cs := n.schema.Parent
	if cs == nil || cs.Kind != schema.Case {
		return false, nil // not a descendant of a case
	}
	if cs.Parent.DefaultCase == cs {
		return false, nil // data of a default case, kept
	}
	has, ok := explicit[cs]
	if !ok {
		err := vc.getnextData(sib, cs, func(i *Node) bool {
			has = i.flags&FlagDefault == 0
			return !has
		})
		if err != nil {
			return false, err
		}
		explicit[cs] = has
	}
	if has {
		return false, nil
	}
	err := vc.autodel([]*Node{n}, false)
	return true, err
}

// autodel is lyd_validate_autodel_node_del for a batch, in order: the diff of each node (an NP
// container's children instead of the container unless npContDiff), the node_types entries of
// each subtree (ly_set_rm_index per node in DFS order), then the nodes, unlinked in one pass.
// Autodelete never reaches a key (keys have no defaults, cases hold no keys).
func (vc *valCtx) autodel(del []*Node, npContDiff bool) error {
	if err := vc.checkContext(); err != nil {
		return err
	}
	if err := vc.t.preflightUnlinkAll(del); err != nil {
		vc.t.work.hashErr = err
		return err
	}
	// Diff entries describe the unlink below. Once emission begins, complete
	// the queue updates, unlink and link cleanup before returning cancellation.
	for _, n := range del {
		if vc.diff == nil {
			break
		}
		if !npContDiff && isNPCont(n.schema) {
			for c := range n.Children() {
				if err := vc.diff(c, diffDelete); err != nil {
					return err
				}
			}
		} else {
			if err := vc.diff(n, diffDelete); err != nil {
				return err
			}
		}
	}
	for _, n := range del {
		if err := vc.dropTypes(n, false); err != nil {
			return err
		}
	}
	if err := vc.t.unlinkAll(del); err != nil {
		vc.t.work.hashErr = err
		return err
	}
	for _, n := range del {
		// Complete link cleanup after the atomic unlink, even when canceled.
		freeSubtreeLinks(vc.t.context(), n) // lyd_free_tree
	}
	return vc.checkContext()
}

// dropTypes removes the terms of n's subtree from node_types the way libyang does
// (ly_set_contains + ly_set_rm_index per node in DFS order: the last item fills each hole).
// libyang removes only terms whose type has a validate_tree callback; every queued term has one,
// so the set removed is the same, and removing any other term could only drop a node that is
// being freed (the safer side: libyang would keep a freed node queued).
func (vc *valCtx) dropTypes(n *Node, cancellable bool) error {
	if vc.nodeTypes == nil || vc.nodeTypes.len() == 0 {
		return nil
	}
	for d := range n.All() {
		if cancellable {
			if err := vc.checkContext(); err != nil {
				return err
			}
		}
		if !d.isTerm() {
			continue
		}
		if i := vc.nodeTypes.contains(d); i >= 0 {
			vc.nodeTypes.rmIndex(i)
		}
	}
	return nil
}

// dupIndex finds equal siblings for the duplicate check: the parent's children table when it has
// one (lyht_find_next_with_collision_cb), else an index built once per lyd_validate_new call
// where libyang scans the siblings (same answers: buckets hold the equal candidates, confirmed by
// htValEqual = lyd_compare_single without recursion).
type dupIndex struct {
	sib *siblings
	tmp map[idxKey][]*Node
}

// bucket iterates the instances that hash like k, the children table's chain in registration
// order when the parent has one (lazily: duplicates stops at the first equal instance).
func (d *dupIndex) bucket(k idxKey, work *workCounter, check func() error) (iter.Seq[*Node], error) {
	if d.sib.ht != nil {
		return d.sib.bucketNodes(k), nil
	}
	if d.tmp == nil {
		d.tmp = map[idxKey][]*Node{}
		for _, n := range d.sib.SchemaAll() {
			if err := check(); err != nil {
				return nil, err
			}
			if hk, ok := indexedKey(n, work); ok {
				d.tmp[hk] = append(d.tmp[hk], n)
			}
		}
	}
	return slices.Values(d.tmp[k]), nil
}

// duplicates is lyd_validate_duplicates: another instance equal to the new node n (by keys or
// value; by schema node for leaves, containers and any nodes) is an error at n, a warning for
// lists and leaf-lists of operational data. Keyless lists and state leaf-lists may repeat.
func (vc *valCtx) duplicates(d *dupIndex, n *Node) error {
	if isDupInstList(n.schema) {
		return nil
	}
	k, ok := indexedKey(n, &vc.t.work)
	if !ok {
		if vc.t.work.hashErr != nil {
			return vc.t.work.hashErr
		}
		return nil
	}
	bucket, err := d.bucket(k, &vc.t.work, vc.checkContext)
	if err != nil {
		return err
	}
	for m := range bucket {
		if err := vc.checkContext(); err != nil {
			return err
		}
		if vc.t.work.hashErr != nil {
			return vc.t.work.hashErr
		}
		vc.t.work.Add(1)
		if m == n || !vc.linkedIn(m, d.sib) {
			continue
		}
		equal, err := htValEqual(vc.t, &vc.t.work, m, n)
		if err != nil {
			return err
		}
		if !equal {
			continue
		}
		if (n.schema.Kind == schema.List || n.schema.Kind == schema.LeafList) && vc.opts.Operational {
			vc.log.warn("Duplicate instance of \"%s\".", n.schema.Name)
			return nil
		}
		return vc.log.val(n, "", ly.Data, "Duplicate instance of \"%s\".", n.schema.Name)
	}
	if vc.t.work.hashErr != nil {
		return vc.t.work.hashErr
	}
	return nil
}
