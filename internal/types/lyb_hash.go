// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/hash_table.c (lyht_hash_multi) and
// src/plugins_types/union.c (lyplg_type_print_union) (BSD-3-Clause, © CESNET).

package types

import "sync/atomic"

// HashMulti32 adds data to libyang's built-in (non-xxhash) 32-bit hash. Call FinishHash32 after
// adding all parts. The oracle deliberately builds libyang without xxhash. lyht_hash_multi adds
// `const char` bytes (hash_table.c:53-57), which are signed on the canonical linux/amd64: a byte
// of 0x80 or more is added sign-extended.
// This helper performs only the byte loop; HashLYB handles an empty value call's
// finalization separately (hash_table.c:58-61).
func HashMulti32(hash uint32, data []byte) uint32 {
	for _, b := range data {
		hash = hashByte(hash, b)
	}
	return hash
}

// hashByte is one step of lyht_hash_multi's loop with amd64's signed char.
func hashByte(hash uint32, b byte) uint32 {
	hash += uint32(int32(int8(b))) //nolint:gosec // sign extension is the point
	hash += hash << 10
	hash ^= hash >> 6
	return hash
}

// FinishHash32 finalizes the hash built by HashMulti32.
func FinishHash32(hash uint32) uint32 {
	hash += hash << 3
	hash ^= hash >> 11
	hash += hash << 15
	return hash
}

// HashLYB adds the byte payload a stored value's LY_VALUE_LYB print callback contributes to
// lyd_hash. It returns false only for a value whose plugin callback has not been ported.
func HashLYB(hash uint32, v Value) (uint32, bool) {
	hash, ok, _ := HashLYBWork(hash, v)
	return hash, ok
}

// HashLYBWork is HashLYB with the number of sparse bitmap transitions actually executed.
func HashLYBWork(hash uint32, v Value) (uint32, bool, uint64) {
	return hashLYBWork(hash, v, true)
}

func hashLYBWork(hash uint32, v Value, outer bool) (uint32, bool, uint64) {
	if u := v.Union(); u != nil {
		member, index := u.Member()
		hash = HashMulti32(hash, []byte{byte(index)}) //nolint:gosec // LYB stores the union index in one byte
		return hashLYBWork(hash, member, false)
	}
	r, ok := lybRepr(v)
	if !ok {
		return 0, false, 0
	}
	// lyd_hash calls lyht_hash_multi once per whole payload (tree_data_hash.c:60-74).
	// An empty payload finalizes (hash_table.c:58-61), but a union always has its index
	// prefix (plugins_types/union.c:660-666), even when its member payload is empty.
	if outer && r.bits == 0 {
		return FinishHash32(hash), true, 0
	}
	if !r.sparseBits {
		return HashMulti32(hash, r.data), true, 0
	}
	hash, walked := hashSparseBits(hash, r)
	return hash, true, walked
}

// sparseHashBytes counts transitions actually executed, excluding skipped zero bytes.
var sparseHashBytes atomic.Int64

// SparseHashBytes is the number of sparse bits hash transitions executed by this process,
// for work-bound tests.
func SparseHashBytes() int64 { return sparseHashBytes.Load() }

// hashSparseBits hashes the bitmap without allocating it. With the generated cycle table,
// no budget is needed: each zero run takes fewer than 2B transitions, so work is
// O((set bits + 1) * 2B), independent of bit positions (hash_table.c:53-57).
func hashSparseBits(hash uint32, r lybRepresentation) (uint32, uint64) {
	var next, walked uint64
	for set := 0; set < len(r.setBits); {
		byteIndex := uint64(r.setBits[set].Position / 8)
		var steps uint64
		hash, steps = zeroRunWork(hash, byteIndex-next)
		walked += steps
		var b byte
		for set < len(r.setBits) && uint64(r.setBits[set].Position/8) == byteIndex {
			b |= 1 << (r.setBits[set].Position % 8)
			set++
		}
		hash = hashByte(hash, b)
		sparseHashBytes.Add(1)
		walked++
		next = byteIndex + 1
	}
	hash, steps := zeroRunWork(hash, (r.bits+7)/8-next)
	return hash, walked + steps
}
