// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/validation.c (lyd_validate_new, lyd_validate_choice_r,
// lyd_validate_cases, lyd_validate_duplicates, lyd_validate_autodel_*, lyd_val_has_default)
// (BSD-3-Clause, © CESNET).

package data

import (
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
	nodes := slices.Clone(sib.list) // autodelete only removes; removed nodes are skipped below
	if mod != nil {
		// lyd_owner_module: an augment's node in mod's top-level choice is mod's data
		nodes = slices.DeleteFunc(nodes, func(n *Node) bool { return ownerModule(vc.t.set, n) != mod })
	}
	dups := dupIndex{sib: sib}
	explicit := map[*schema.Node]bool{}
	var lastDflt *schema.Node
	for _, n := range nodes {
		if !vc.linkedIn(n, sib) || n.flags&(FlagNew|FlagDefault) == 0 {
			continue
		}
		if hasDefault(n.schema) && n.schema != lastDflt && n.flags&FlagNew != 0 {
			// remove old default(s) of the new node if an explicit instance exists
			lastDflt = n.schema
			var gone bool
			if n.schema.Kind == schema.LeafList {
				gone = vc.autodelLeafListDflt(sib, n)
			} else {
				gone = vc.autodelContLeafDflt(sib, n)
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
			n.flags &^= FlagNew // this node is valid
		}
		if n.flags&FlagDefault != 0 && vc.autodelCaseDflt(sib, n, explicit) {
			continue
		}
	}
	return rc
}

// validateChoices is lyd_validate_choice_r: the cases of every choice under sparent, nested
// choices included.
func (vc *valCtx) validateChoices(sib *siblings, sparent *schema.Node, mod *schema.Module) error {
	var rc error
	for _, ch := range vc.getnextOf(sparent, mod, vc.output).choices {
		if len(sib.list) == 0 {
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
		found := 0
		vc.getnextData(sib, cs, func(n *Node) bool {
			if n.flags&FlagNew != 0 {
				found = 2 // a new case data found, nothing more to look for
				return false
			}
			found = 1
			return true
		})
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
		vc.getnextData(sib, oldCase, func(n *Node) bool { del = append(del, n); return true })
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
			freeSubtreeLinks(vc.t.set, n) // lyd_free_tree
		}
	}
	return nil
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
func (vc *valCtx) instances(sib *siblings, sn *schema.Node) []*Node {
	i := vc.t.schemaIndex(sib, sn)
	if i < 0 {
		return nil
	}
	j := i
	for j < len(sib.list) && sib.list[j].schema == sn {
		j++
	}
	return slices.Clone(sib.list[i:j])
}

// autodelLeafListDflt is lyd_validate_autodel_leaflist_dflt: with an explicit instance, every
// default instance goes. It reports whether n itself went.
func (vc *valCtx) autodelLeafListDflt(sib *siblings, n *Node) bool {
	inst := vc.instances(sib, n.schema)
	if !slices.ContainsFunc(inst, func(i *Node) bool { return i.flags&FlagDefault == 0 }) {
		return false // no explicit instance, keep defaults as they are
	}
	return vc.autodelDefaults(inst, n)
}

// autodelContLeafDflt is lyd_validate_autodel_cont_leaf_dflt: with an explicit instance, every
// default instance goes; without one, a single old default instance goes.
func (vc *valCtx) autodelContLeafDflt(sib *siblings, n *Node) bool {
	inst := vc.instances(sib, n.schema)
	if slices.ContainsFunc(inst, func(i *Node) bool { return i.flags&FlagDefault == 0 }) {
		return vc.autodelDefaults(inst, n)
	}
	for _, i := range inst {
		if i.flags&FlagDefault != 0 && i.flags&FlagNew == 0 {
			vc.autodel([]*Node{i}, false)
			return i == n
		}
	}
	return false
}

// autodelDefaults deletes the default instances of inst in one batch; it reports whether n went.
func (vc *valCtx) autodelDefaults(inst []*Node, n *Node) bool {
	del := slices.DeleteFunc(inst, func(i *Node) bool { return i.flags&FlagDefault == 0 })
	vc.autodel(del, false)
	return slices.Contains(del, n)
}

// autodelCaseDflt is lyd_validate_autodel_case_dflt: a default node of a non-default case that
// has no explicit data any more goes. Whether a case has explicit data is computed once per
// validateNew call (explicit), deleting defaults does not change it.
func (vc *valCtx) autodelCaseDflt(sib *siblings, n *Node, explicit map[*schema.Node]bool) bool {
	cs := n.schema.Parent
	if cs == nil || cs.Kind != schema.Case {
		return false // not a descendant of a case
	}
	if cs.Parent.DefaultCase == cs {
		return false // data of a default case, kept
	}
	has, ok := explicit[cs]
	if !ok {
		vc.getnextData(sib, cs, func(i *Node) bool {
			has = i.flags&FlagDefault == 0
			return !has
		})
		explicit[cs] = has
	}
	if has {
		return false
	}
	vc.autodel([]*Node{n}, false)
	return true
}

// autodel is lyd_validate_autodel_node_del for a batch, in order: the diff of each node (an NP
// container's children instead of the container unless npContDiff), the node_types entries of
// each subtree (ly_set_rm_index per node in DFS order), then the nodes, unlinked in one pass.
// Autodelete never reaches a key (keys have no defaults, cases hold no keys).
func (vc *valCtx) autodel(del []*Node, npContDiff bool) {
	for _, n := range del {
		if vc.diff == nil {
			break
		}
		if !npContDiff && isNPCont(n.schema) {
			for c := range n.Children() {
				_ = vc.diff(c, diffDelete) // libyang ignores the result here
			}
		} else {
			_ = vc.diff(n, diffDelete)
		}
	}
	for _, n := range del {
		vc.dropTypes(n)
	}
	if len(del) == 1 {
		unlink(del[0]) // the usual single default: no batch bookkeeping
	} else {
		_ = vc.t.unlinkAll(del)
	}
	for _, n := range del {
		freeSubtreeLinks(vc.t.set, n) // lyd_free_tree
	}
}

// dropTypes removes the terms of n's subtree from node_types the way libyang does
// (ly_set_contains + ly_set_rm_index per node in DFS order: the last item fills each hole).
// libyang removes only terms whose type has a validate_tree callback; every queued term has one,
// so the set removed is the same, and removing any other term could only drop a node that is
// being freed (the safer side: libyang would keep a freed node queued).
func (vc *valCtx) dropTypes(n *Node) {
	if vc.nodeTypes == nil || vc.nodeTypes.len() == 0 {
		return
	}
	for d := range n.All() {
		if !d.isTerm() {
			continue
		}
		if i := vc.nodeTypes.contains(d); i >= 0 {
			vc.nodeTypes.rmIndex(i)
		}
	}
}

// dupIndex finds equal siblings for the duplicate check: the parent's children table when it has
// one (lyht_find_next_with_collision_cb), else an index built once per lyd_validate_new call
// where libyang scans the siblings (same answers: buckets hold the equal candidates, confirmed by
// htValEqual = lyd_compare_single without recursion).
type dupIndex struct {
	sib *siblings
	tmp map[idxKey][]*Node
}

func (d *dupIndex) bucket(k idxKey) []*Node {
	if d.sib.ht != nil {
		return d.sib.ht[k]
	}
	if d.tmp == nil {
		d.tmp = map[idxKey][]*Node{}
		for _, n := range d.sib.list {
			if hk, ok := hashOf(n); ok {
				d.tmp[hk] = append(d.tmp[hk], n)
			}
		}
	}
	return d.tmp[k]
}

// duplicates is lyd_validate_duplicates: another instance equal to the new node n (by keys or
// value; by schema node for leaves, containers and any nodes) is an error at n, a warning for
// lists and leaf-lists of operational data. Keyless lists and state leaf-lists may repeat.
func (vc *valCtx) duplicates(d *dupIndex, n *Node) error {
	if isDupInstList(n.schema) {
		return nil
	}
	k, ok := hashOf(n)
	if !ok {
		return nil
	}
	for _, m := range d.bucket(k) {
		vc.t.work.Add(1)
		if m == n || !vc.linkedIn(m, d.sib) || !htValEqual(vc.t, m, n) {
			continue
		}
		if (n.schema.Kind == schema.List || n.schema.Kind == schema.LeafList) && vc.opts.Operational {
			vc.log.warn("Duplicate instance of \"%s\".", n.schema.Name)
			return nil
		}
		return vc.log.val(n, "", ly.Data, "Duplicate instance of \"%s\".", n.schema.Name)
	}
	return nil
}
