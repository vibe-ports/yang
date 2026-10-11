// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestZCyclesPresent(t *testing.T) {
	if len(zcyclesData) == 0 {
		t.Fatal("zero-cycle table is empty; run go generate ./internal/types/")
	}
	const want = "daf885ee26665a07ce32c58fe9ebbe05c35cadb992a8bd27b06acad3f6f62b0a"
	if got := fmt.Sprintf("%x", sha256.Sum256(zcyclesData)); got != want {
		t.Fatalf("zcycles.bin sha256 %s, want %s", got, want)
	}
	zeroTable.once.Do(loadZeroCycles)
	if len(zeroTable.cycles) == 0 {
		t.Fatal("zero-cycle table contains no long cycles")
	}
}

func TestZeroRun(t *testing.T) {
	for _, h := range []uint32{0, 1, 0xffffffff, 0x12345678, 0x87654321} {
		for _, n := range []uint64{0, 1, 63, 65535, 65536, 65537, 200003} {
			want := zeroRunDirect(h, n)
			before := SparseHashBytes()
			got := zeroRun(h, n)
			walked := SparseHashBytes() - before
			if got != want {
				t.Fatalf("zeroRun(%#x,%d) = %#x, want %#x", h, n, got, want)
			}
			if len(zcyclesData) > 0 && walked >= 2*int64(zeroStride) {
				t.Fatalf("zeroRun executed %d transitions", walked)
			}
		}
	}
}

func TestZeroRunCheckpoints(t *testing.T) {
	zeroTable.once.Do(loadZeroCycles)
	for _, c := range zeroTable.cycles {
		for _, i := range []int{0, len(c.cps) - 1} {
			h := c.cps[i]
			for _, n := range []uint64{c.L, c.L + 1, ^uint64(0)} {
				q := (uint64(i)*zeroStride + n%c.L) % c.L //nolint:gosec // nonnegative index
				want := zeroRunDirect(c.cps[q/zeroStride], q%zeroStride)
				if got := zeroRun(h, n); got != want {
					t.Fatalf("checkpoint skip(%#x,%d) = %#x, want %#x", h, n, got, want)
				}
			}
		}
	}
}
