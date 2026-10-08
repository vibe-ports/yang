// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestCompareBitsMemcmp checks compareBits against libyang's memcmp of little-endian bitmaps
// sized by the highest position, over random position sets.
func TestCompareBitsMemcmp(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test data
	bitmap := func(ps []uint32, last uint32) []byte {
		m := make([]byte, last/8+1)
		for _, p := range ps {
			m[p/8] |= 1 << (p % 8)
		}
		return m
	}
	set := func(last uint32) []*schema.Bit {
		var out []*schema.Bit
		for p := range last + 1 {
			if r.IntN(8) == 0 {
				out = append(out, &schema.Bit{Position: p})
			}
		}
		return out
	}
	pos := func(bs []*schema.Bit) []uint32 {
		var out []uint32
		for _, b := range bs {
			out = append(out, b.Position)
		}
		return out
	}
	for range 20000 {
		last := r.Uint32N(40)
		a, b := set(last), set(last)
		if r.IntN(4) == 0 {
			b = slices.Clone(a)
		}
		want := bytes.Compare(bitmap(pos(a), last), bitmap(pos(b), last))
		if got := compareBits(a, b); got != want {
			t.Fatalf("%v vs %v: got %d, want %d", pos(a), pos(b), got, want)
		}
	}
	hi := []*schema.Bit{{Position: 4294967295}}
	if compareBits(nil, hi) >= 0 || compareBits(hi, hi) != 0 {
		t.Error("position 4294967295")
	}
}
