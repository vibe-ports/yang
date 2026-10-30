// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_hash.c and src/tree_data.c (BSD-3-Clause, © CESNET).

package data

import (
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// schemaHash is the unfinished module/name prefix of lyd_hash (tree_data_hash.c:45-47).
func schemaHash(sn *schema.Node) uint32 {
	if owner := sn.Module.Owner; owner != nil {
		h, _ := owner.CachedNameHash(sn, func() (uint32, error) { return rawSchemaHash(sn), nil })
		return h
	}
	return rawSchemaHash(sn)
}

func rawSchemaHash(sn *schema.Node) uint32 {
	return types.HashMulti32(types.HashMulti32(0, []byte(sn.Module.Name)), []byte(sn.Name))
}

// schemaHashWork charges a cold prefix before its first byte transition. A
// compiled set shares the prefix across trees; hand-built sets use tree work.
func schemaHashWork(sn *schema.Node, work *workCounter) uint32 {
	if work == nil {
		return schemaHash(sn)
	}
	if owner := sn.Module.Owner; owner != nil {
		h, err := owner.CachedNameHash(sn, func() (uint32, error) {
			if err := work.chargeHash(uint64(len(sn.Module.Name)) + uint64(len(sn.Name))); err != nil {
				return 0, err
			}
			return rawSchemaHash(sn), nil
		})
		if err != nil {
			work.hashErr = err
		}
		return h
	}
	if h, ok := work.hashNames[sn]; ok {
		return h
	}
	if err := work.chargeHash(uint64(len(sn.Module.Name)) + uint64(len(sn.Name))); err != nil {
		return 0
	}
	h := rawSchemaHash(sn)
	if work.hashNames == nil {
		work.hashNames = map[*schema.Node]uint32{}
	}
	work.hashNames[sn] = h
	return h
}

func hashValue(work *workCounter, hash uint32, v *types.Value) uint32 {
	if work != nil && work.chargeHash(types.HashLYBWorkBound(*v)) != nil {
		return hash
	}
	hash, selected, _, walked := types.HashLYBWork(hash, *v)
	*v = selected
	if work != nil {
		work.Add(1 + int64(walked/4096)) //nolint:gosec // a bitmap is at most 2^29 bytes
	}
	return hash
}

// refreshHash is lyd_hash (tree_data_hash.c:32-84). It runs only at native hash
// events; comparisons and table registration read the stored result. A list's
// input is its historical typed snapshot, never reconstructed from declared keys.
func (n *Node) refreshHash(work *workCounter) {
	if n.schema == nil {
		return
	}
	if err := n.touch(); err != nil {
		return
	}
	h := schemaHashWork(n.schema, work)
	if work != nil && work.hashErr != nil {
		return
	}
	switch {
	case n.schema.Kind == schema.LeafList:
		h = hashValue(work, h, &n.value)
	case n.schema.Kind == schema.List && n.schema.Keyless():
		h = types.FinishHash32(h)
	case n.schema.Kind == schema.List:
		if !n.lkeyOK {
			n.hash = 0
			return
		}
		for _, key := range snapshotKeys(n).All() {
			if work != nil {
				work.Add(1)
			}
			h = hashValue(work, h, &key.value)
		}
		n.lkeyPrefix, n.lkeyDirty = h, false
	}
	n.hash = types.FinishHash32(h)
}

// snapshotKeys is the typed input of the last keyed-list lyd_hash call. A live
// prefix avoids copying on every repeated last key; displacement freezes it once.
func snapshotKeys(n *Node) siblingSpan {
	if n.lkeyFrozen != nil {
		return spanOf(n.lkeyFrozen)
	}
	return n.kids.schemaSpan().Slice(0, n.lkeyN)
}

// keyPrefix is the schema children of list n before its first opaque child, lyd_child's
// document order: libyang's key walks (lyd_hash, rb_compare_lists, key predicates, the dup and
// diff key loops) stop at an opaque or a non-key child, and an Ordered ParseOnly input can put
// an opaque child between keys (design 15). Callers stop at the first non-key.
func keyPrefix(n *Node) siblingSpan { return n.kids.schemaSpan().Slice(0, n.kids.beforeOpaque()) }

// keyRun is the length of n's leading key run in document order: keyN (the logical run that the
// link and unlink bookkeeping maintain) cut at the first opaque child.
func (n *Node) keyRun() int { return min(n.keyN, n.kids.beforeOpaque()) }

func (t *Tree) beforeKeyLink(list, child *Node, at int) {
	// lyd_insert_hash leaves children of an opaque parent unhashed
	// (tree_data_hash.c:173-175); it has no list-key prefix state either.
	if list == nil || list.schema == nil || list.schema.Kind != schema.List || list.schema.Keyless() || child.schema == nil {
		return
	}
	t.work.Add(1)
	if err := list.touch(); err != nil {
		return
	}
	if err := child.touch(); err != nil {
		return
	}
	if at < list.lkeyN && list.lkeyFrozen == nil {
		t.work.Add(int64(list.lkeyN))
		list.lkeyFrozen = snapshotKeys(list).Collect()
	}
	if !child.schema.IsKey() {
		if at < list.keyN {
			list.keyN = at
		}
		return
	}
	if at <= list.keyN {
		list.keyN++
	}
	child.keySlot = 0
}

// keyLinked is lyd_insert_has_keys and the conditional lyd_hash call
// (tree_data.c:684-702,836-842). An unsuccessful check preserves even hash zero.
func (t *Tree) keyLinked(list, child *Node) {
	if list == nil || list.schema == nil || list.schema.Kind != schema.List || list.schema.Keyless() {
		return
	}
	if !child.isKey() {
		reindexComparedParent(child)
		return
	}
	prefix := positionalChildren(list) // lyd_child: the first children in document order
	for _, key := range list.schema.Keys {
		t.work.Add(1)
		if c := prefix.next(); c == nil || c.schema != key {
			// No native hash or registration event, but positional equality has
			// changed. Keep the existing record's place in its native chain.
			reindexList(list)
			return
		}
	}
	if err := list.touch(); err != nil {
		return
	}
	start := list.lkeyN
	run := list.keyRun()
	if !list.lkeyOK || list.lkeyFrozen != nil || list.lkeyDirty || start > run {
		start = 0
		list.lkeyPrefix = schemaHashWork(list.schema, &t.work)
	}
	list.lkeyN, list.lkeyFrozen, list.lkeyOK, list.lkeyDirty = run, nil, true, false
	for i, key := range snapshotKeys(list).Slice(start, snapshotKeys(list).Len()).All() {
		t.work.Add(1)
		if err := key.touch(); err != nil {
			return
		}
		key.keySlot = start + i + 1
		list.lkeyPrefix = hashValue(&t.work, list.lkeyPrefix, &key.value)
	}
	list.hash = types.FinishHash32(list.lkeyPrefix)
	registerParent(list)
	reindexComparedParent(list)
}

// reindexComparedParent follows the positional comparison dependency, including
// non-keys exposed by a cross-context copy or internal key removal. A complete
// leading key run excludes all non-key children in constant work.
func reindexComparedParent(child *Node) {
	list := child.parent
	if list == nil || list.schema == nil || list.schema.Kind != schema.List {
		return
	}
	if !child.isKey() && list.keyRun() >= len(list.schema.Keys) {
		return
	}
	children := positionalChildren(list)
	for range list.schema.Keys {
		if list.kids.work != nil {
			list.kids.work.Add(1)
		}
		if children.next() == child {
			reindexList(list)
			return
		}
	}
}

// reindexList also handles a non-key exposed in the compared prefix by internal
// key removal. It never creates a native hash or registration event.
func reindexList(list *Node) {
	if list != nil && list.schema != nil && list.schema.Kind == schema.List {
		if sib := list.siblingsOf(); sib != nil {
			sib.hashReindex(list)
		}
	}
}

// resetListKey captures the final leading keys at an unconditional native rehash:
// duplication (tree_data.c:2317) or changed key value (tree_data_new.c:1189).
// The scan is once per completed copy/edit, not once per inserted repeated key.
func resetListKey(work *workCounter, list *Node) {
	if list == nil || list.schema == nil {
		return
	}
	if err := list.touch(); err != nil {
		return
	}
	list.keyN = 0
	for _, key := range list.kids.SchemaAll() {
		if work != nil {
			work.Add(1)
		}
		if !key.isKey() {
			break
		}
		list.keyN++
		if err := key.touch(); err != nil {
			return
		}
		key.keySlot = 0
		if list.keyN <= list.keyRun() {
			key.keySlot = list.keyN // in lyd_hash's run
		}
	}
	list.lkeyN, list.lkeyFrozen, list.lkeyOK = list.keyRun(), nil, true
	list.refreshHash(work)
}

// beforeKeyUnlink preserves the last native hash input before the child slice
// moves. lyd_unlink_ignore_lyds does not rehash the parent (tree_data.c:1187-1239).
func beforeKeyUnlink(list *Node, at int, work *workCounter) {
	if list == nil || list.schema == nil || list.schema.Kind != schema.List {
		return
	}
	if err := list.touch(); err != nil {
		return
	}
	if at < list.lkeyN && list.lkeyFrozen == nil {
		if work != nil {
			work.Add(int64(list.lkeyN))
		}
		list.lkeyFrozen = snapshotKeys(list).Collect()
	}
	if at < list.keyN {
		list.keyN--
	}
}

// exposeListKeys updates only the live leading-key run after removing a non-key
// that preceded it. The last native hash input remains in lkeyN/lkeyFrozen until
// a later native rehash event replaces it.
func exposeListKeys(work *workCounter, list *Node) {
	if err := list.touch(); err != nil {
		return
	}
	// a removal reserves the walk: prepareUnlink, or a batch's charge per sibling set
	for _, key := range list.kids.Range(list.keyN, list.kids.SchemaLen()) {
		if work != nil {
			work.Add(1)
		}
		if !key.isKey() {
			break
		}
		list.keyN++
		if err := key.touch(); err != nil {
			return
		}
		key.keySlot = list.keyN
	}
}

func refreshInstanceHash(work *workCounter, n *Node) {
	// lyd_hash is a no-op for opaque nodes (tree_data_hash.c:39-40).
	if n == nil || n.schema == nil {
		return
	}
	if n.schema.Kind == schema.List && !n.schema.Keyless() {
		resetListKey(work, n)
	} else {
		n.refreshHash(work)
	}
}
