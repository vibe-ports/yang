// SPDX-License-Identifier: BSD-3-Clause
// Test oracle derived from libyang v5.8.6 src/hash_table.c:53-57 (BSD-3-Clause, © CESNET).

package types

import (
	"math/rand"
	"testing"
)

const zeroRunBlock = uint64(65536)

// zeroRunOracle is the literal zero-byte loop from lyht_hash_multi. Keeping the
// oracle here independent of zeroRun makes every skipped transition observable.
func zeroRunOracle(h uint32, n uint64) uint32 {
	for range n {
		h += h << 10
		h ^= h >> 6
	}
	return h
}

func TestZeroRunMatchesDirect(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5eed586)) //nolint:gosec // reproducible property test
	length := func(i int) uint64 {
		switch i % 11 {
		case 0:
			return 0
		case 1:
			return 1
		case 2:
			return 2
		case 3:
			return zeroRunBlock - 1
		case 4:
			return zeroRunBlock
		case 5:
			return zeroRunBlock + 1
		case 6:
			return 2*zeroRunBlock - 1
		case 7:
			return 2 * zeroRunBlock
		case 8:
			return 3*zeroRunBlock + 7
		case 9:
			return 1_000_000
		default:
			return uint64(rng.Int63n(1<<24 + 1)) //nolint:gosec // nonnegative test count
		}
	}

	for i := range 2000 {
		h := rng.Uint32()
		n := length(i)
		if got, want := zeroRun(h, n), zeroRunOracle(h, n); got != want {
			t.Fatalf("case %d: zeroRun(%#08x, %d) = %#08x, want %#08x", i, h, n, got, want)
		}
	}
}

func TestZeroRunShortCycles(t *testing.T) {
	type cycle struct {
		state  uint32
		period uint64
	}
	var cycles []cycle
	const (
		stateSearch = 256
		periodCap   = uint64(1 << 16)
	)
	for state := uint32(0); state < stateSearch && len(cycles) < 4; state++ {
		h := state
		for period := uint64(1); period <= periodCap; period++ {
			h = zeroRunOracle(h, 1)
			if h == state {
				cycles = append(cycles, cycle{state: state, period: period})
				break
			}
		}
	}
	if len(cycles) == 0 {
		t.Fatal("no short zero-step cycle found in capped search")
	}

	for _, c := range cycles {
		for i, r := range []uint64{0, c.period / 2, c.period - 1} {
			n := c.period*uint64(17+i*65537) + r
			if got, want := zeroRun(c.state, n), zeroRunOracle(c.state, n); got != want {
				t.Errorf("state %#08x period %d, k/r case %d: got %#08x, want %#08x",
					c.state, c.period, i, got, want)
			}
		}
	}
}

func TestZeroRunWorkBoundAndComposition(t *testing.T) {
	const maxWork = int64(4 * zeroRunBlock)
	checked := func(h uint32, n uint64) uint32 {
		t.Helper()
		before := SparseHashBytes()
		got := zeroRun(h, n)
		if work := SparseHashBytes() - before; work >= maxWork {
			t.Errorf("zeroRun(%#08x, %d) executed %d zero-byte transitions, want fewer than %d",
				h, n, work, maxWork)
		}
		return got
	}

	const (
		a = uint64(1 << 29)
		b = uint64(1<<32 - 1)
	)
	h := uint32(0x9e3779b9)
	got := checked(checked(h, a), b)
	want := checked(h, a+b)
	if got != want {
		t.Fatalf("composition over long runs = %#08x, want %#08x", got, want)
	}
}
