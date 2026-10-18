// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/hash_table.c (lyht_hash_multi) and
// src/plugins_types/union.c (lyplg_type_print_union) (BSD-3-Clause, © CESNET).

package types

import (
	"sync/atomic"

	"github.com/vibe-ports/yang/internal/schema"
)

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
	hash, _, ok, _ := HashLYBWork(hash, v)
	return hash, ok
}

// HashLYBWork also returns the value with any reselected union members and the
// number of bytes and synthetic enum declarations visited. Mutation-time hash callers install
// the returned value; read-only probes discard it. The input remains immutable.
func HashLYBWork(hash uint32, v Value) (uint32, Value, bool, uint64) {
	hash, ok, work := hashLYBWork(hash, &v, true)
	return hash, v, ok, work
}

// HashLYBWorkBound bounds the byte transitions of one hash before any are
// performed. Callers accumulate this reservation in their operation budget.
// Sparse zero runs use the checkpoint bound, rather than the enormous bitmap
// length; ordinary payloads charge their bytes. No bitmap or text copy is made.
func HashLYBWorkBound(v Value) uint64 {
	if u := v.Union(); u != nil {
		// Stored values have already selected this member without a tree.
		// Hand-built values without that cache cannot be safely preflighted.
		if u.noTree == nil {
			return ^uint64(0)
		}
		if !u.noTreeOK {
			return 1
		}
		member := HashLYBWorkBound(u.noTree.member)
		if member == ^uint64(0) {
			return member
		}
		return 1 + member
	}
	if v.typ == nil {
		return 1
	}
	if pluginFor(v.typ) != nil {
		// All supported plugin LYB forms are canonical text or fixed fields
		// plus its zone/fraction suffix; 32 covers the largest fixed prefix.
		return uint64(len(v.canon)) + 32
	}
	switch v.typ.Base {
	case schema.Bits:
		if len(v.typ.Bits) == 0 {
			return 1
		}
		bytes := (uint64(v.typ.Bits[len(v.typ.Bits)-1].Position) + 8) / 8
		// Each set bit can split another zero run and introduces at most one
		// nonzero byte. This also bounds adjacent bits in the same byte.
		runs := uint64(len(v.bits)) + 1
		return min(bytes, runs*(2*zeroStride+1)) + 1
	case schema.Binary:
		return uint64(len(v.bin)) + 1
	case schema.String, schema.InstanceID:
		return uint64(len(v.canon)) + 1
	case schema.IdentityRef:
		if v.ident != nil && v.ident.Module != nil {
			return uint64(len(v.ident.Module.Name)+len(v.ident.Name)) + 2
		}
	case schema.Enumeration:
		if v.typ.EnumLYBWidth == 0 {
			return uint64(len(v.typ.Enums)) + 5
		}
	}
	return 8
}

func hashLYBWork(hash uint32, v *Value, outer bool) (uint32, bool, uint64) {
	if u := v.Union(); u != nil {
		// lyb_union_print (union.c:633-666) selects again from the original
		// input without a data tree. Copy before replacing the member: a
		// duplicated node may still share its immutable value with the source.
		var selected UnionValue
		if u.noTree != nil {
			if !u.noTreeOK {
				return 0, false, 0
			}
			selected = *u
			selected.member, selected.index = u.noTree.member, u.noTree.index
		} else {
			// Direct callers may construct Values by hand. Operation callers
			// reject such a value at HashLYBWorkBound before reaching here.
			selected = *u
			if d := unionFind(v.typ, &selected, false, nil); d != nil {
				return 0, false, 0
			}
		}
		// Only the subvalue is replaced; the outer cached canonical text and
		// validation state survive the hash callback (union.c:649-655).
		v.union = &selected
		// LYPLG_UNION_TYPE_IDX_SIZE is 1 (union.c:50), despite the temporary
		// uint32 and htole32 used by lyb_union_print (union.c:664-666).
		hash = HashMulti32(hash, []byte{byte(selected.index)}) //nolint:gosec // native one-byte member index
		return hashLYBWork(hash, &selected.member, false)
	}
	r, ok := lybRepr(*v)
	if !ok {
		return 0, false, 0
	}
	// lyd_hash calls lyht_hash_multi once per whole payload (tree_data_hash.c:60-74).
	// An empty payload finalizes (hash_table.c:58-61), but a union always has its index
	// prefix (plugins_types/union.c:660-666), even when its member payload is empty.
	if outer && r.bits == 0 {
		return FinishHash32(hash), true, r.work
	}
	if !r.sparseBits {
		return HashMulti32(hash, r.data), true, r.work + uint64(len(r.data))
	}
	hash, walked := hashSparseBits(hash, r)
	return hash, true, walked
}

// sparseHashBytes counts transitions actually executed, excluding skipped zero bytes.
var sparseHashBytes atomic.Int64

// SparseHashBytes is the number of sparse bits hash transitions executed by this process,
// for work-bound tests.
func SparseHashBytes() int64 { return sparseHashBytes.Load() }

// hashSparseBits hashes the bitmap without allocating it. Each zero run takes
// fewer than 2B transitions, independently of bit positions (hash_table.c:53-57).
// Operation callers reserve HashLYBWorkBound cumulatively: the per-run bound
// alone does not prevent repeated tiny inputs from amplifying total CPU work.
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
