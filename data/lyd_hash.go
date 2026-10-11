// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_hash.c (lyd_hash) (BSD-3-Clause, © CESNET).

package data

import (
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

type lydHashResult struct {
	hash uint32
	ok   bool
}

// lydHash calculates the stored node hash used by lyd_compare_single_data. A keyed list without
// all its keys has not been hashed by libyang yet and therefore has the zero hash. ok is false for
// a value plugin whose LYB callback has not been ported, which must not become a false match.
func lydHash(t *Tree, n *Node) (hash uint32, ok bool) {
	if t.diffHashes != nil {
		if h, exists := t.diffHashes[n]; exists {
			return h.hash, h.ok
		}
		defer func() { t.diffHashes[n] = lydHashResult{hash, ok} }()
	}
	if n.schema == nil {
		return 0, true
	}
	hash = types.HashMulti32(0, []byte(n.schema.Module.Name))
	hash = types.HashMulti32(hash, []byte(n.schema.Name))
	switch {
	case n.schema.Kind == schema.LeafList:
		var walked uint64
		if hash, ok, walked = types.HashLYBWork(hash, n.value); !ok {
			return 0, false
		}
		t.work.Add(1 + int64(walked/4096)) //nolint:gosec // at most 2^17
	case n.schema.Kind == schema.List && !n.schema.Keyless():
		for i, key := range n.schema.Keys {
			if i >= len(n.kids.list) || n.kids.list[i].schema != key {
				return 0, true
			}
			var walked uint64
			if hash, ok, walked = types.HashLYBWork(hash, n.kids.list[i].value); !ok {
				return 0, false
			}
			t.work.Add(1 + int64(walked/4096)) //nolint:gosec // at most 2^17
		}
	case n.schema.Kind == schema.List && n.schema.Keyless():
		hash = types.FinishHash32(hash) // lyht_hash_multi(hash, NULL, 0)
	}
	return types.FinishHash32(hash), true
}

// lydHashInputsEqual reports whether lydHash receives byte-identical inputs for a and b, or both
// keyed lists take its early zero-hash return. It mirrors lydHash's opaque, leaf-list, and keyed-
// list branches so equal sparse bits values never need their conceptual bitmap walked.
// Payload boundaries are preserved: each empty key or leaf-list payload finalizes once
// (tree_data_hash.c:60-74; hash_table.c:58-61), including when both payloads are empty.
func lydHashInputsEqual(t *Tree, a, b *Node) bool {
	switch {
	case a.schema == nil || b.schema == nil:
		return a.schema == nil && b.schema == nil
	case a.schema.Kind != b.schema.Kind || a.schema.Name != b.schema.Name ||
		a.schema.Module.Name != b.schema.Module.Name:
		return false
	case a.schema.Kind == schema.LeafList:
		return types.EqualLYB(a.value, b.value)
	case a.schema.Kind != schema.List:
		return true
	case a.schema.Keyless() || b.schema.Keyless():
		return a.schema.Keyless() && b.schema.Keyless()
	}

	if len(a.schema.Keys) != len(b.schema.Keys) {
		return false
	}
	for i := range a.schema.Keys {
		t.work.Add(1)
		ap := i < len(a.kids.list) && a.kids.list[i].schema == a.schema.Keys[i]
		bp := i < len(b.kids.list) && b.kids.list[i].schema == b.schema.Keys[i]
		if ap != bp {
			return false
		}
		if !ap {
			return true
		}
		if !types.EqualLYB(a.kids.list[i].value, b.kids.list[i].value) {
			return false
		}
	}
	return true
}

// lydHashHasSparseBits reports whether lydHash would consume a sparse bits value from n.
func lydHashHasSparseBits(t *Tree, n *Node) bool {
	if n.schema == nil {
		return false
	}
	if n.schema.Kind == schema.LeafList {
		return types.HasSparseBits(n.value)
	}
	if n.schema.Kind != schema.List || n.schema.Keyless() {
		return false
	}
	for i, key := range n.schema.Keys {
		t.work.Add(1)
		if i >= len(n.kids.list) || n.kids.list[i].schema != key {
			return false
		}
		if types.HasSparseBits(n.kids.list[i].value) {
			return true
		}
	}
	return false
}
