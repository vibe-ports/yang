// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func step16(h uint32) uint32 {
	t := uint32(uint16(1025 * h)) //nolint:gosec // 16-bit analogue states
	return t ^ (t >> 6)
}

func TestCycles16(t *testing.T) {
	// The production stride leaves every 16-bit cycle short (zero is a fixed
	// point). A smaller stride also exercises checkpoints, wraparound and tails.
	for _, block := range []uint32{65536, 256} {
		cycles, stats := enumerate(16, block)
		if stats.states != 1<<16 {
			t.Fatalf("sum of cycle lengths = %d", stats.states)
		}
		index := map[uint32][2]int{}
		var checkpoints uint64
		for ci, c := range cycles {
			if c.length < uint64(block) || uint64(len(c.states)) != (c.length+uint64(block)-1)/uint64(block) {
				t.Fatalf("invalid cycle: length %d, checkpoints %d", c.length, len(c.states))
			}
			for j, h := range c.states {
				if _, exists := index[h]; exists {
					t.Fatalf("duplicate checkpoint %d", h)
				}
				index[h] = [2]int{ci, j}
				checkpoints++
			}
		}
		if checkpoints != stats.checkpoints || block == 256 && checkpoints == 0 {
			t.Fatalf("checkpoint count = %d, stats %+v", checkpoints, stats)
		}
		skip := func(h uint32, n uint64) (uint32, uint64) {
			start := h
			var walked uint64
			for n != 0 {
				if ix, ok := index[h]; ok {
					c := cycles[ix[0]]
					q := (uint64(ix[1])*uint64(block) + n%c.length) % c.length //nolint:gosec // nonnegative count
					h = c.states[q/uint64(block)]
					n = q % uint64(block)
					break
				}
				h = step16(h)
				walked++
				n--
				if h == start {
					n %= walked
					break
				}
			}
			walked += n
			for ; n != 0; n-- {
				h = step16(h)
			}
			return h, walked
		}
		for h := uint32(0); h < 1<<16; h++ {
			want := h
			var at uint64
			for _, n := range []uint64{0, 1, 6, 255, 256, 257, 513} {
				for ; at < n; at++ {
					want = step16(want)
				}
				got, walked := skip(h, n)
				if got != want || walked >= 2*uint64(block) {
					t.Fatalf("B=%d skip(%d,%d) = %d (%d transitions), want %d", block, h, n, got, walked, want)
				}
			}
		}
		// Independently walk every recorded cycle, checking its exact length and checkpoints.
		for _, c := range cycles {
			h := c.states[0]
			for offset := uint64(0); offset < c.length; offset++ {
				if offset > 0 && h == c.states[0] {
					t.Fatal("cycle closes before its recorded length")
				}
				if offset%uint64(block) == 0 && h != c.states[offset/uint64(block)] {
					t.Fatal("checkpoint does not match direct walk")
				}
				h = step16(h)
			}
			if h != c.states[0] {
				t.Fatal("cycle does not close")
			}
		}
		again, _ := enumerate(16, block)
		if !bytes.Equal(encode(cycles, block), encode(again, block)) {
			t.Fatal("nondeterministic output")
		}
	}
}

func TestSmallGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cycles.bin")
	if err := run(16, path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // test temp file
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 12 || string(data[:4]) != "ZCY1" || binary.LittleEndian.Uint32(data[4:8]) != stride || binary.LittleEndian.Uint32(data[8:12]) != 0 {
		t.Fatalf("unexpected 16-bit table: %x", data)
	}
	if err := run(33, path); err == nil {
		t.Fatal("invalid width accepted")
	}
}
