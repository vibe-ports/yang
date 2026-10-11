// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/hash_table.c (BSD-3-Clause, © CESNET).

package types

import (
	_ "embed"
	"encoding/binary"
	"sync"
)

// zcycles.bin holds the checkpoints of the long cycles of the zero-byte step (13 of the 24
// cycles, 65542 checkpoints, 262336 bytes, sha256
// daf885ee26665a07ce32c58fe9ebbe05c35cadb992a8bd27b06acad3f6f62b0a, checked by
// TestZCyclesPresent). It is byte-for-byte reproducible: `go generate ./internal/types/` walks
// all 2^32 states (a few CPU minutes, a 512 MiB visited bitmap).
//go:generate go run ./internal/zgen -o zcycles.bin

//go:embed zcycles.bin
var zcyclesData []byte

const zeroStride = uint64(65536)

type zeroCycle struct {
	L   uint64
	cps []uint32
}

var zeroTable struct {
	once   sync.Once
	cycles []zeroCycle
	index  map[uint32]uint64 // high 32 bits: cycle index; low 32: checkpoint index
	filter [1 << 14]uint64   // 2^20 bits, indexed by state >> 12
}

func loadZeroCycles() {
	data := zcyclesData
	if len(data) == 0 {
		return // not generated: zeroRunWork walks directly; TestZCyclesPresent fails
	}
	bad := func() { panic("types: invalid generated zero-cycle table") }
	if len(data) < 12 || string(data[:4]) != "ZCY1" || binary.LittleEndian.Uint32(data[4:8]) != uint32(zeroStride) {
		bad()
	}
	count := binary.LittleEndian.Uint32(data[8:12])
	data = data[12:]
	if count == 0 || count > 1<<16 {
		bad()
	}
	zeroTable.index = make(map[uint32]uint64)
	for ci := uint32(0); ci < count; ci++ {
		if len(data) < 12 {
			bad()
		}
		L := binary.LittleEndian.Uint64(data[:8])
		k := binary.LittleEndian.Uint32(data[8:12])
		data = data[12:]
		if L < zeroStride || L > 1<<32 || uint64(k) != (L+zeroStride-1)/zeroStride || uint64(k)*4 > uint64(len(data)) {
			bad()
		}
		c := zeroCycle{L: L, cps: make([]uint32, k)}
		for j := range c.cps {
			h := binary.LittleEndian.Uint32(data[:4])
			data = data[4:]
			if _, exists := zeroTable.index[h]; exists {
				bad()
			}
			c.cps[j] = h
			zeroTable.index[h] = uint64(ci)<<32 | uint64(j)
			bucket := h >> 12
			zeroTable.filter[bucket/64] |= uint64(1) << (bucket % 64)
		}
		zeroTable.cycles = append(zeroTable.cycles, c)
	}
	if len(data) != 0 {
		bad()
	}
}

// zeroRunDirect applies hash_table.c:53-57 with key_part[i] == 0.
func zeroRunDirect(h uint32, n uint64) uint32 {
	sparseHashBytes.Add(int64(n)) //nolint:gosec // callers walk bitmap runs of at most 2^29 bytes
	for ; n != 0; n-- {
		h = hashByte(h, 0)
	}
	return h
}

// zeroRun skips exactly n zero bytes. Z(h) = (1025*h) ^ ((1025*h) >> 6), with
// uint32 multiplication, is a permutation (hash_table.c:53-57). Checkpoints on
// every long cycle bound a call to fewer than 2B transitions; short cycles close
// within B transitions. No work budget or per-value hash cache is needed.
func zeroRun(h uint32, n uint64) uint32 {
	h, _ = zeroRunWork(h, n)
	return h
}

func zeroRunWork(h uint32, n uint64) (uint32, uint64) {
	zeroTable.once.Do(loadZeroCycles)
	if len(zeroTable.cycles) == 0 {
		return zeroRunDirect(h, n), n
	}
	start := h
	var walked uint64
	for n != 0 {
		bucket := h >> 12
		if zeroTable.filter[bucket/64]&(uint64(1)<<(bucket%64)) != 0 {
			if packed, ok := zeroTable.index[h]; ok {
				c := &zeroTable.cycles[packed>>32]
				p := uint64(uint32(packed)) * zeroStride //nolint:gosec // low 32 bits are the checkpoint index
				q := (p + n%c.L) % c.L                   // reduce first to avoid overflow for large n
				sparseHashBytes.Add(int64(walked))
				return zeroRunDirect(c.cps[q/zeroStride], q%zeroStride), walked + q%zeroStride
			}
		}
		h = hashByte(h, 0)
		walked++
		n--
		if h == start {
			// No checkpoint: this is a short cycle, including the fixed point zero.
			n %= walked
			sparseHashBytes.Add(int64(walked))
			return zeroRunDirect(h, n), walked + n
		}
	}
	sparseHashBytes.Add(int64(walked))
	return h, walked
}
