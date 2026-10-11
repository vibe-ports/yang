// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vibe-ports/yang/internal/types"
)

const sparseBitsNamespace = "urn:vibe-ports:yang:diff-sparse-bits"

func sparseBitsModel(bits int, firstPosition uint32) string {
	var b strings.Builder
	b.WriteString(`module diff-sparse-bits {
  yang-version 1.1;
  namespace "urn:vibe-ports:yang:diff-sparse-bits";
  prefix dsb;
  container top {
    leaf-list item {
      config false;
      type bits {
`)
	for i := range bits {
		fmt.Fprintf(&b, "        bit b%d { position %d; }\n", i, firstPosition+uint32(i))
	}
	b.WriteString(`      }
    }
  }
}`)
	return b.String()
}

func sparseBitsXML(n, distinct int) string {
	var b strings.Builder
	b.WriteString(`<top xmlns="`)
	b.WriteString(sparseBitsNamespace)
	b.WriteString(`">`)
	for i := range n {
		fmt.Fprintf(&b, "<item>b%d</item>", i%distinct)
	}
	b.WriteString(`</top>`)
	return b.String()
}

func diffSparseBitsWithin(t *testing.T, first, second *Tree) *Tree {
	t.Helper()
	type result struct {
		diff *Tree
		err  error
	}
	done := make(chan result, 1)
	go func() {
		diff, err := Diff(first, second, DiffOptions{})
		done <- result{diff: diff, err: err}
	}()

	timer := time.NewTimer(20 * time.Second * raceSlowdown)
	defer timer.Stop()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.diff
	case <-timer.C:
		t.Fatalf("Diff of sparse bits did not finish within %v", 20*time.Second*raceSlowdown)
		return nil
	}
}

func sparseBitsDiffOps(diff *Tree) (creates, deletes int) {
	if diff == nil {
		return 0, 0
	}
	var walk func(*siblings)
	walk = func(s *siblings) {
		for n := range s.all() {
			if op, ok := diffGetOp(n); ok {
				switch op {
				case diffCreate:
					creates++
				case diffDelete:
					deletes++
				}
			}
			walk(&n.kids)
		}
	}
	walk(&diff.top)
	return creates, deletes
}

// TestDiffSparseBitsScaling pins lyd_compare_single_data's hash-first semantics while bounding
// the work needed for sparse LYB bitmaps. The canonical values match, but the LYB bit positions
// (and therefore their hashes) differ, so every instance is deleted and created.
func TestDiffSparseBitsScaling(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		positionA, positionB uint32
		n                    int
	}{
		{"four-megabit-position", 1 << 22, 1<<22 - 1, 400},
		{"maximum-position", 1<<32 - 1, 1<<32 - 2, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setA := inlineDiffSet(t, sparseBitsModel(1, tc.positionA))
			setB := inlineDiffSet(t, sparseBitsModel(1, tc.positionB))
			xml := sparseBitsXML(tc.n, 1)
			first := parseOnlyXML(t, setA, xml)
			second := parseOnlyXML(t, setB, xml)

			diff := diffSparseBitsWithin(t, first, second)
			creates, deletes := sparseBitsDiffOps(diff)
			if creates != tc.n || deletes != tc.n {
				t.Fatalf("Diff operations = %d creates, %d deletes; want %d each", creates, deletes, tc.n)
			}
		})
	}
}

// TestDiffSparseBitsHashBound checks the whole Diff operation instead of an individual hash.
// A fourfold increase in nodes may do modest fixed extra work, but must not cause the sixteenfold
// conceptual bitmap walk of an all-pairs comparison.
func TestDiffSparseBitsHashBound(t *testing.T) {
	const (
		position     = uint32(1 << 20)
		maxBytesPerN = 4 * (4*65536 + 1) // two zero runs and one set byte per value
	)

	for _, tc := range []struct {
		name     string
		bits     int
		distinct int
	}{
		{"duplicates", 1, 1},
		{"sorted-distinct", 8, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setA := inlineDiffSet(t, sparseBitsModel(tc.bits, position))
			setB := inlineDiffSet(t, sparseBitsModel(tc.bits, position-1))
			work := make(map[int]int64, 2)
			for _, n := range []int{50, 200} {
				xml := sparseBitsXML(n, tc.distinct)
				first := parseOnlyXML(t, setA, xml)
				second := parseOnlyXML(t, setB, xml)
				before := types.SparseHashBytes()
				_, err := Diff(first, second, DiffOptions{})
				work[n] = types.SparseHashBytes() - before
				if err != nil {
					t.Fatalf("Diff with N=%d: %v", n, err)
				}
				if limit := maxBytesPerN * int64(n); work[n] > limit {
					t.Errorf("SparseHashBytes delta with N=%d = %d, want <= %d", n, work[n], limit)
				}
			}
			if work[200] > 6*work[50] {
				t.Errorf("SparseHashBytes deltas: N=50: %d, N=200: %d; want large <= 6 * small",
					work[50], work[200])
			}
		})
	}
}

// TestCompareSparseBitsSameContextNoWalk checks the shortcuts before lyd_hash: identical LYB
// values compare directly, while different canonical values are unequal independently of hashes.
func TestCompareSparseBitsSameContextNoWalk(t *testing.T) {
	const position = uint32(1 << 22)
	set := inlineDiffSet(t, sparseBitsModel(2, position))
	item := func(value string) *Node {
		t.Helper()
		xml := fmt.Sprintf(`<top xmlns="%s"><item>%s</item></top>`, sparseBitsNamespace, value)
		return topOf(parseOnlyXML(t, set, xml)).kids.list[0]
	}
	equalA, equalB, different := item("b0"), item("b0"), item("b1")

	before := types.SparseHashBytes()
	if !equalA.Equal(equalB, CompareOptions{}) {
		t.Error("equal sparse bits values compare unequal")
	}
	if equalA.Equal(different, CompareOptions{}) {
		t.Error("different sparse bits values compare equal")
	}
	if got := types.SparseHashBytes() - before; got != 0 {
		t.Fatalf("SparseHashBytes delta = %d, want 0", got)
	}
}

func sparseBitsRetentionModel(high uint32) string {
	var b strings.Builder
	b.WriteString(`module diff-sparse-bits {
  yang-version 1.1;
  namespace "urn:vibe-ports:yang:diff-sparse-bits";
  prefix dsb;
  container top {
    leaf-list item {
      config false;
      type bits {
`)
	for i := range 300 {
		fmt.Fprintf(&b, "        bit b%d { position %d; }\n", i, i)
	}
	fmt.Fprintf(&b, "        bit high { position %d; }\n", high)
	b.WriteString(`      }
    }
  }
}`)
	return b.String()
}

// TestDiffSparseBitsRepeatWork checks that repeated diffs remain bounded without retaining
// hashes in values. Each zero run executes fewer than 2B transitions, regardless of position.
func TestDiffSparseBitsRepeatWork(t *testing.T) {
	const (
		n        = 300
		highA    = uint32(1 << 20)
		highB    = highA - 1
		maxFirst = 4 * n * (4*65536 + 1)
	)

	setA := inlineDiffSet(t, sparseBitsRetentionModel(highA))
	setB := inlineDiffSet(t, sparseBitsRetentionModel(highB))
	xml := sparseBitsXML(n, n)
	first := parseOnlyXML(t, setA, xml)
	second := parseOnlyXML(t, setB, xml)

	before := types.SparseHashBytes()
	diff := diffSparseBitsWithin(t, first, second)
	firstWalk := types.SparseHashBytes() - before
	if diff != nil && diff.diffHashes != nil {
		t.Fatal("Diff retained its operation-local hash cache")
	}
	// HashMulti32/HashLYB/FinishHash32 give 300 unique hashes on each side and no
	// cross-side collisions, so libyang deletes and creates every instance.
	creates, deletes := sparseBitsDiffOps(diff)
	if creates != n || deletes != n {
		t.Errorf("Diff operations = %d creates, %d deletes; want %d each", creates, deletes, n)
	}
	if firstWalk > maxFirst {
		t.Errorf("first Diff SparseHashBytes delta = %d, want <= %d", firstWalk, int64(maxFirst))
	}

	item := topOf(first).kids.list[0]
	before = types.SparseHashBytes()
	diff = diffSparseBitsWithin(t, first, second)
	if got := types.SparseHashBytes() - before; got != firstWalk {
		t.Errorf("second Diff SparseHashBytes delta = %d, want %d", got, firstWalk)
	}
	creates, deletes = sparseBitsDiffOps(diff)
	if creates != n || deletes != n {
		t.Errorf("second Diff operations = %d creates, %d deletes; want %d each", creates, deletes, n)
	}

	equal := parseOnlyXML(t, setA, sparseBitsXML(1, 1))
	before = types.SparseHashBytes()
	if !item.Equal(topOf(equal).kids.list[0], CompareOptions{}) {
		t.Error("Node.Equal reports equal bits values unequal")
	}
	if got := types.SparseHashBytes() - before; got != 0 {
		t.Errorf("Node.Equal SparseHashBytes delta = %d, want 0", got)
	}
}
