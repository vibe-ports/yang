// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"testing"

	"github.com/vibe-ports/yang"
)

// #240 step 4 (design 13 §4.4, G10): read accounting. Index reads on a mutation path are paid
// by a read credit opened before the read (class A); reads inside a prepaid write draw from the
// write's credit (class B); read-only paths pay nothing (class C). unpaidReadPanics is on in
// every test build, so G10a is every sweep, history and fuzz seed of the package.

// TestS4ReadCreditFirstUnitRefusal is G10b: refused at its first units, an insertion into an
// operation's indexed children does the same counted work and allocations with 8 or 2,048
// children: no unpaid traversal runs before the refusal.
func TestS4ReadCreditFirstUnitRefusal(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	measure := func(children int, limit uint64) (int64, uint64) {
		values := make([]string, children)
		for i := range values {
			values[i] = fmt.Sprint(2 * i)
		}
		tr, op := s4OpTree(t, set, values...)
		tr.work.Store(0)
		tr.work.visits = true
		tr.work.hashLimit = limit
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := tr.NewTerm(op, "w", "1", NewOptions{})
		runtime.ReadMemStats(&after)
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("children %d limit %d: %v, want a refusal", children, limit, err)
		}
		return tr.work.Load(), after.Mallocs - before.Mallocs
	}
	for limit := uint64(1); limit <= 8; limit++ {
		if raceEnabled && !fzFull && limit > 1 {
			break // swept in the normal suite
		}
		smallWork, smallAllocs := measure(8, limit)
		largeWork, largeAllocs := measure(2048, limit)
		if largeWork > 2*smallWork+16 || largeAllocs > 2*smallAllocs+16 {
			t.Fatalf("limit %d: refusal work %d -> %d, allocations %d -> %d", limit, smallWork, largeWork,
				smallAllocs, largeAllocs)
		}
	}
}

// TestS4ReadCreditRefusesBeforeWrite is G10c: within one quota scope whose only over-quota work
// is class-A reads, the refusal is a read credit's, before any write; the operation's next step
// finds the sticky refusal and writes nothing either.
func TestS4ReadCreditRefusesBeforeWrite(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	tr, op := s4OpTree(t, set, "5", "1", "9", "13", "17")
	n := s4Child(op, "w", "9")
	before := snapshotHashBudgetTree(tr)
	done := tr.work.beginHashWork()
	defer done()
	tr.work.hashLimit = tr.work.hashUsed + 3*(rbHeight(op.kids.Len())+1) + 1
	reads := 0
	for op.kids.orderPosition(n) >= 0 {
		reads++
		if reads > 16 {
			t.Fatal("class-A reads were not charged")
		}
	}
	if reads != 3 || !errors.Is(tr.work.hashErr, yang.ErrBudget) {
		t.Fatalf("%d paid reads, then %v; want 3 and a budget refusal", reads, tr.work.hashErr)
	}
	if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
		t.Fatal("a read refusal wrote")
	}
	if err := tr.link(op, &op.kids, newTerm(n.schema, n.value, &tr.work), 0); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("the next step: %v, want the sticky refusal", err)
	}
	if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before) {
		t.Fatal("the next step wrote after a read refusal")
	}
}

// TestS4ReadCreditChargedIndexUnits is G10d: chargedIndex's read credit charges main's units
// (rbHeight(len)+1, once, not refunded) at main's single trace point.
func TestS4ReadCreditChargedIndexUnits(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	tr, op := s4OpTree(t, set, "5", "1", "9", "13", "17")
	n := s4Child(op, "w", "13")
	done := tr.work.beginHashWork()
	defer done()
	used := tr.work.hashUsed
	var points []uint64
	tr.work.hashTrace = &points
	at, err := tr.chargedIndex(&op.kids, n)
	tr.work.hashTrace = nil
	if err != nil || op.kids.At(at) != n {
		t.Fatalf("chargedIndex = %d, %v", at, err)
	}
	want := rbHeight(op.kids.Len()) + 1
	if got := tr.work.hashUsed - used; got != want {
		t.Fatalf("chargedIndex charged %d units, main charges %d", got, want)
	}
	if !reflect.DeepEqual(points, []uint64{used + want}) {
		t.Fatalf("trace points %v, want [%d]", points, used+want)
	}
}
