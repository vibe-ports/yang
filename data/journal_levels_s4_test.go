// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
)

// #240 step 4 (design 13): savepoint histories of the recovery state. G3 pins the whole-capture
// serial rule (wholeAt/prevAt), allocation-free rollback of the index entries near map growth
// boundaries and the map contents after a commit; G2 pins handle identity across a refused
// one-node move without a running state.

// s4IndexImage is the exact state of an indexed set's ordered index: handle identities in
// sequence order, the data-side maps with their tombstones, and the occurrence classes.
func s4IndexImage(s *siblings) string {
	if s.order == nil {
		return "no index"
	}
	o := s.order
	var b strings.Builder
	for h := o.seq.First(); h != nil; h = o.seq.Next(h) {
		rank, _ := o.seq.Rank(h)
		fmt.Fprintf(&b, "%p:%p:%d ", h, h.Value, rank)
	}
	keys := make([]string, 0, len(o.byNode))
	for n, h := range o.byNode {
		keys = append(keys, fmt.Sprintf("%p=%p", n, h))
	}
	sortStrings(keys)
	fmt.Fprintf(&b, "| byNode %v |", keys)
	keys = keys[:0]
	for sn, d := range o.entries {
		if d == nil {
			keys = append(keys, fmt.Sprintf("%p=nil", sn))
			continue
		}
		keys = append(keys, fmt.Sprintf("%p=%+v", sn, *d))
	}
	sortStrings(keys)
	fmt.Fprintf(&b, " entries %v | ranks %d %d |", keys, o.ranks[0].Len(), o.ranks[1].Len())
	for _, n := range s.SchemaAll() {
		first, last := o.bySchema.First(n.schema, 0), o.bySchema.Last(n.schema, 0)
		fmt.Fprintf(&b, " %p:%p:%p:%d", n, first, last, o.bySchema.Len(n.schema, 0))
		if key, ok := opExactValue(n); ok {
			fmt.Fprintf(&b, "/%p:%d", o.byExact.First(n.schema, key), o.byExact.Len(n.schema, key))
		}
	}
	fmt.Fprintf(&b, " | tombstones %d %d", o.bySchema.Tombstones(), o.byExact.Tombstones())
	return b.String()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for k := i; k > 0 && s[k] < s[k-1]; k-- {
			s[k], s[k-1] = s[k-1], s[k]
		}
	}
}

// s4NoIndexTombstones fails if a nil cell survived in any index map of s.
func s4NoIndexTombstones(t *testing.T, where string, s *siblings) {
	t.Helper()
	if s.order == nil {
		return
	}
	o := s.order
	for n, h := range o.byNode {
		if h == nil {
			t.Fatalf("%s: byNode tombstone %p survived", where, n)
		}
	}
	for sn, d := range o.entries {
		if d == nil {
			t.Fatalf("%s: entries tombstone %p survived", where, sn)
		}
	}
	for _, occ := range []interface{ Tombstones() int }{o.bySchema, o.byExact, o.byRB, o.byOpaqName, o.byOpaqExact} {
		if occ.Tombstones() != 0 {
			t.Fatalf("%s: occurrence tombstones survived", where)
		}
	}
}

func s4Mallocs() uint64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.Mallocs
}

// TestS4LevelWholeCaptureSerials pins design 13 §2.2's whole-capture rule at the level
// boundaries (G3, the whole-capture cases).
func TestS4LevelWholeCaptureSerials(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	type world struct {
		tr  *Tree
		op  *Node
		s   *siblings
		j   *hashTransaction
		got int
	}
	open := func(t *testing.T) *world {
		tr, op := s4OpTree(t, set, "4", "8")
		w := &world{tr: tr, op: op, s: &op.kids}
		w.j = beginHashTransaction(tr)
		captureHook = func(s *siblings) {
			if s == w.s {
				w.got++
			}
		}
		t.Cleanup(func() { captureHook = nil })
		return w
	}
	touch := func(t *testing.T, w *world) {
		t.Helper()
		if err := w.s.touch(); err != nil {
			t.Fatal(err)
		}
	}
	savepoint := func(t *testing.T, w *world) int {
		t.Helper()
		sp, err := w.j.savepoint()
		if err != nil {
			t.Fatal(err)
		}
		return sp
	}
	t.Run("parent captured: inner recaptures, commit keeps it", func(t *testing.T) {
		w := open(t)
		touch(t, w)
		sp := savepoint(t, w)
		touch(t, w)
		w.j.commitLevel(sp)
		touch(t, w)
		if w.got != 2 {
			t.Fatalf("captures %d, want 2 (parent, inner; none after the commit)", w.got)
		}
		w.j.end(nil)
	})
	t.Run("parent captured: inner rollback restores the parent's serial", func(t *testing.T) {
		w := open(t)
		touch(t, w)
		sp := savepoint(t, w)
		touch(t, w)
		w.j.rollbackLevel(sp)
		touch(t, w)
		if w.got != 2 {
			t.Fatalf("captures %d, want 2 (no recapture after the inner rollback)", w.got)
		}
		w.j.end(nil)
	})
	t.Run("parent not captured: inner rollback leaves it uncaptured", func(t *testing.T) {
		w := open(t)
		sp := savepoint(t, w)
		touch(t, w)
		w.j.rollbackLevel(sp)
		touch(t, w)
		if w.got != 2 {
			t.Fatalf("captures %d, want 2 (the parent captures afresh)", w.got)
		}
		w.j.end(nil)
	})
	t.Run("successive sibling levels capture afresh", func(t *testing.T) {
		w := open(t)
		for range 3 {
			sp := savepoint(t, w)
			touch(t, w)
			w.j.commitLevel(sp)
		}
		if w.got != 3 {
			t.Fatalf("captures %d, want one per sibling level", w.got)
		}
		w.j.end(nil)
	})
	t.Run("three levels, innermost capture, commit twice, outer rollback", func(t *testing.T) {
		w := open(t)
		before := snapshotHashBudgetTree(w.tr)
		index := s4IndexImage(w.s)
		outer := savepoint(t, w)
		mid := savepoint(t, w)
		inner := savepoint(t, w)
		if _, err := w.tr.NewTerm(w.op, "w", "6", NewOptions{}); err != nil {
			t.Fatal(err)
		}
		used, counted := w.tr.work.hashUsed, w.tr.work.Load()
		w.j.commitLevel(inner)
		w.j.commitLevel(mid)
		if w.tr.work.hashUsed != used || w.tr.work.Load() != counted {
			t.Fatal("a commit did work")
		}
		w.j.rollbackLevel(outer)
		if !reflect.DeepEqual(snapshotHashBudgetTree(w.tr), before) || s4IndexImage(w.s) != index {
			t.Fatal("the outer rollback did not restore the tree")
		}
		w.j.end(nil)
		s4Check(t, "after", []*Tree{w.tr})
	})
	t.Run("narrow entries, inner whole capture, outer rollback", func(t *testing.T) {
		for _, commit := range []bool{true, false} {
			tr, op := s4OpTree(t, set, "4", "8")
			before := snapshotHashBudgetTree(tr)
			j := beginNarrowTransaction(tr)
			if err := recordField(j, &op.flags); err != nil { // a narrow entry of the outer level
				t.Fatal(err)
			}
			op.flags |= FlagNew
			sp, err := j.savepoint()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tr.NewTerm(op, "w", "6", NewOptions{}); err != nil { // whole capture
				t.Fatal(err)
			}
			if commit {
				j.commitLevel(sp)
			} else {
				j.rollbackLevel(sp)
			}
			outer := errors.New("refused")
			j.rollback = true
			j.end(&outer)
			if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
				t.Fatalf("commit=%t: the outer rollback did not restore the tree", commit)
			}
			s4Check(t, "narrow", []*Tree{tr})
		}
	})
}

// TestS4LevelIndexRollbackAllocs runs the design 13 §11 G3 map-cell histories on an operation's
// indexed children, near the growth boundaries of the index maps: every rollback allocates
// nothing and restores the exact index state, and an outermost commit leaves no tombstone.
func TestS4LevelIndexRollbackAllocs(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1)) // as AllocsPerRun
	newTerms := func(t *testing.T, tr *Tree, op *Node, from, count int) {
		t.Helper()
		for i := range count {
			if _, err := tr.NewTerm(op, "w", fmt.Sprint(from+i), NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	removeTerms := func(t *testing.T, op *Node, count int) {
		t.Helper()
		for range count {
			var last *Node
			for n := range op.kids.all() {
				if n.schema.Name == "w" {
					last = n
				}
			}
			if err := last.Remove(); err != nil {
				t.Fatal(err)
			}
		}
	}
	rollback := func(t *testing.T, j *hashTransaction, sp int, s *siblings, want string) {
		t.Helper()
		before := s4Mallocs()
		j.rollbackLevel(sp)
		if n := s4Mallocs() - before; n != 0 {
			t.Fatalf("rollback allocated %d times", n)
		}
		if got := s4IndexImage(s); got != want {
			t.Fatalf("rollback did not restore the index:\n%s\nwant\n%s", got, want)
		}
	}
	for _, size := range []int{5, 6, 7, 12, 13, 14, 26, 27, 28} { // byNode/occurrence growth boundaries
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			tr, op := s4OpTree(t, set, "100")
			s := &op.kids
			t.Run("insert, inner rollback, outer commit", func(t *testing.T) {
				j := beginHashTransaction(tr)
				want := s4IndexImage(s)
				sp, _ := j.savepoint()
				newTerms(t, tr, op, 200, size)
				rollback(t, j, sp, s, want)
				j.end(nil)
				s4NoIndexTombstones(t, "commit", s)
			})
			newTerms(t, tr, op, 300, size)
			t.Run("remove, inner rollback, outer commit", func(t *testing.T) {
				j := beginHashTransaction(tr)
				want := s4IndexImage(s)
				sp, _ := j.savepoint()
				removeTerms(t, op, size)
				rollback(t, j, sp, s, want)
				j.end(nil)
				s4NoIndexTombstones(t, "commit", s)
			})
			t.Run("remove, recreate, commit keeps the live cells", func(t *testing.T) {
				j := beginHashTransaction(tr)
				removeTerms(t, op, size)
				newTerms(t, tr, op, 300, size)
				j.end(nil)
				s4NoIndexTombstones(t, "commit", s)
				s4Check(t, "recreate", []*Tree{tr})
			})
			t.Run("insert absent keys, remove them, outer commit", func(t *testing.T) {
				j := beginHashTransaction(tr)
				newTerms(t, tr, op, 400, size)
				removeTerms(t, op, size)
				j.end(nil)
				s4NoIndexTombstones(t, "commit", s)
				s4Check(t, "absent", []*Tree{tr})
			})
			t.Run("outer remove, inner recreate, inner rollback, outer rollback", func(t *testing.T) {
				before := snapshotHashBudgetTree(tr)
				want := s4IndexImage(s)
				j := beginHashTransaction(tr)
				removeTerms(t, op, size)
				mid := s4IndexImage(s)
				sp, _ := j.savepoint()
				newTerms(t, tr, op, 300, size)
				rollback(t, j, sp, s, mid)
				failed := errors.New("refused")
				j.rollback = true
				j.end(&failed)
				if s4IndexImage(s) != want || !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
					t.Fatal("outer rollback did not restore")
				}
			})
			t.Run("duplicate inner writes, inner commit, outer rollback", func(t *testing.T) {
				want := s4IndexImage(s)
				j := beginHashTransaction(tr)
				sp, _ := j.savepoint()
				for range 3 {
					newTerms(t, tr, op, 500, size)
					removeTerms(t, op, size)
				}
				j.commitLevel(sp)
				failed := errors.New("refused")
				j.rollback = true
				j.end(&failed)
				if s4IndexImage(s) != want {
					t.Fatal("outer rollback did not restore")
				}
			})
			s4Check(t, "end", []*Tree{tr})
		})
	}
}

// TestS4RefusedLocalMoveRevivesHandle is G2: a one-node move between indexed sets with no
// running state is refused at every budget point; the original source handle stays live (no
// fresh handle), and then both owners are mutated independently.
func TestS4RefusedLocalMoveRevivesHandle(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	build := func(t *testing.T) (*Tree, *Node, *Tree, *Node, *Node) {
		src, srcOp := s4OpTree(t, set, "4", "8", "12")
		dst, dstOp := s4OpTree(t, set, "5", "9")
		moved := s4Child(srcOp, "w", "8")
		return src, srcOp, dst, dstOp, moved
	}
	src, _, dst, dstOp, moved := build(t)
	var points []uint64
	dst.work.hashTrace = &points
	if err := dst.Insert(dstOp, moved); err != nil {
		t.Fatal(err)
	}
	need := dst.work.hashUsed
	refused := 0
	for limit := uint64(1); limit < need; limit++ {
		if raceEnabled && limit%7 != 0 && limit != need-1 {
			continue
		}
		src, srcOp, dst, dstOp, moved := build(t)
		handle := srcOp.kids.order.byNode[moved]
		before := []hashBudgetSnapshot{snapshotHashBudgetTree(src), snapshotHashBudgetTree(dst)}
		dst.work.hashLimit = limit
		err := dst.Insert(dstOp, moved)
		dst.work.hashLimit = 0
		if err == nil {
			continue
		}
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("limit %d: %v", limit, err)
		}
		refused++
		if !reflect.DeepEqual(snapshotHashBudgetTree(src), before[0]) || !reflect.DeepEqual(snapshotHashBudgetTree(dst), before[1]) {
			t.Fatalf("limit %d: the refusal changed a tree", limit)
		}
		if srcOp.kids.order.byNode[moved] != handle {
			t.Fatalf("limit %d: the source handle was replaced", limit)
		}
		if _, live := srcOp.kids.order.seq.Rank(handle); !live {
			t.Fatalf("limit %d: the original source handle is not live", limit)
		}
		if _, err := src.NewTerm(srcOp, "w", "10", NewOptions{}); err != nil {
			t.Fatalf("limit %d: source: %v", limit, err)
		}
		if _, err := dst.NewTerm(dstOp, "w", "7", NewOptions{}); err != nil {
			t.Fatalf("limit %d: destination: %v", limit, err)
		}
		s4Check(t, fmt.Sprintf("limit %d", limit), []*Tree{src, dst})
	}
	if refused == 0 {
		t.Fatal("no limit refused the move")
	}
	_ = src
}
