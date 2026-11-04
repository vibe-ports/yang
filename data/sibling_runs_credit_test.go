// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strconv"
	"testing"
	"unsafe"
)

func runSelectedInput(count int) []string {
	input := make([]string, 0, count+2)
	for i := range count {
		input = append(input, "ll="+strconv.Itoa(i+1))
	}
	return append(input, "zz", "ll=1000000")
}

// Grow the selected tree, not the unrelated piece past the opaque separator.
func TestRunSelectedRetainedCredit(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, count := range []int{64, 4096} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			tr, c := runTree(t, set, runSelectedInput(count)...)
			tr.work.visits = true
			tr.work.Store(0)
			tr.work.hashUsed = 0
			if _, err := tr.NewTerm(c, "ll", strconv.Itoa(count+1), NewOptions{}); err != nil {
				t.Fatal(err)
			}
			t.Logf("selected=%d cold visits=%d charge=%d", count, tr.work.Load(), tr.work.hashUsed)
			if tr.work.hashUsed < uint64(count) { //nolint:gosec // positive test size
				t.Fatal("cold construction did not pay selected piece")
			}
			leader := c.kids.runLeader(c.kids.At(0).schema)
			if !c.kids.rbTree[leader] {
				t.Fatal("did not build selected retained tree")
			}
			tr.work.visits = true
			tr.work.Store(0)
			tr.work.hashUsed = 0
			if _, err := tr.NewTerm(c, "ll", strconv.Itoa(count+2), NewOptions{}); err != nil {
				t.Fatal(err)
			}
			t.Logf("selected=%d visits=%d charge=%d", count, tr.work.Load(), tr.work.hashUsed)
			if tr.work.Load() > 1000 {
				t.Fatal("retained insertion traversed selected piece")
			}
			checkTreeRetainedRB(t, tr)
			opidxAssertTreeInvariant(t, tr)
		})
	}
}

// A genuine boundary query must debit every inspected member even inside a refundable scope.
func TestRunSelectedBoundaryCredit(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, count := range []int{64, 4096} {
		tr, c := runTree(t, set, runSelectedInput(count)...)
		n := runNode(t, set, c, "ll=50000")
		w := &tr.work
		nested, err := w.prepay(1000000, false)
		if err != nil {
			t.Fatal(err)
		}
		before := w.credit
		lo, hi := tr.runBounds(&c.kids, n)
		consumed := before - w.credit
		w.closeCredit(nested)
		if lo != 0 || hi != count {
			t.Fatalf("bounds %d,%d", lo, hi)
		}
		if consumed < uint64(count) { //nolint:gosec // positive test size
			t.Fatalf("selected=%d consumed=%d, scan refunded", count, consumed)
		}
		t.Logf("selected=%d boundary consumed=%d", count, consumed)
	}
}

// Isolate the two data-side index passes from the occurrence hooks. Each must consume
// a unit per member, in addition to the three leaf-list occurrence registrations.
func TestRunPhysicalBuildConsumption(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, count := range []int{64, 4096} {
		tr, c := runTree(t, set, runSelectedInput(count)...)
		s, w := &c.kids, &tr.work
		nested, err := w.prepay(1000000, false)
		if err != nil {
			t.Fatal(err)
		}
		before := w.credit
		mustLink(t, s.opBuild(tr))
		consumed := before - w.credit
		w.closeCredit(nested)
		// No RB members: two schema occurrences per ll, two opaque occurrences,
		// and the handle assembly plus the physical summary for all siblings.
		want := uint64(4*(count+1) + 4) //nolint:gosec // positive test size
		if consumed != want {
			t.Fatalf("n=%d index consumed=%d want=%d", count, consumed, want)
		}
		t.Logf("n=%d index consumed=%d", count, consumed)
		opidxAssertTreeInvariant(t, tr)
	}
}

// Runs unchanged on main. The first retained nonleader has no metadata to transfer,
// including when its physical successor is opaque. Both removal primitives must agree.
func TestRunOrdinaryOpaqueUnlinkWork(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
			tr, c := runTree(t, set)
			for _, v := range []string{"1", "2"} {
				if _, err := tr.NewTerm(c, "ll", v, NewOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			n := c.kids.At(1)
			if _, err := tr.NewOpaque(c, "zz", "u", OpaqueOptions{Namespace: "urn:run-test", Format: FormatXML}); err != nil {
				t.Fatal(err)
			}
			tr.work.visits = true
			tr.work.Store(0)
			tr.work.hashUsed = 0
			if batch {
				mustLink(t, tr.unlinkAll([]*Node{n}))
			} else {
				mustLink(t, n.Unlink())
			}
			t.Logf("visits=%d charge=%d", tr.work.Load(), tr.work.hashUsed)
			// Measured on 46af50e6 with this unchanged probe.
			want := uint64(85)
			if batch {
				want = 103
			}
			if tr.work.hashUsed != want {
				t.Fatalf("ordinary unlink charge=%d", tr.work.hashUsed)
			}
			checkTreeRetainedRB(t, tr)
		})
	}
}

// Ordinary plans escape via w.unlinkPlan. Check both sizeof and the bytes allocated,
// since AllocsPerRun cannot distinguish a move from the 96-byte to 112-byte class.
func TestRunOrdinaryUnlinkBytes(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	tr, c := runTree(t, set)
	gc := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(gc)
	const count = 512
	nodes := make([]*Node, count)
	for i := range nodes {
		nodes[i] = runNode(t, set, c, "a=x")
		mustLink(t, tr.link(c, &c.kids, nodes[i], c.kids.SchemaLen()))
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for _, n := range nodes {
		mustLink(t, n.Unlink())
	}
	runtime.ReadMemStats(&after)
	bytes := after.TotalAlloc - before.TotalAlloc
	t.Logf("ordinary unlink bytes=%d per-node=%d plan=%d", bytes, bytes/count, unsafe.Sizeof(unlinkPlan{}))
	if unsafe.Sizeof(unlinkPlan{}) > 8+10*unsafe.Sizeof(uintptr(0)) {
		t.Errorf("ordinary plan grew")
	}
	// Ordinary leaf removal keeps main's 96-byte plan class and 56-byte public-operation closure.
	wantPerNode := uint64(152)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		wantPerNode = 88
	}
	if bytes != count*wantPerNode {
		t.Fatalf("ordinary unlink allocation bytes=%d, main=%d", bytes, count*wantPerNode)
	}
}

// The reservation must be consumed, including the old records materialized before
// the first interleaving and the summary built after insertion. Observe the enclosing
// credit directly so closeCredit cannot disguise a missing debit as a valid bound.
func TestRunFirstInterleavingConsumption(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, count := range []int{64, 4096} {
		tr, c := runTree(t, set, runSelectedInput(count)[:count]...)
		s, w := &c.kids, &tr.work
		if s.ht == nil || s.order != nil || s.schemaUnordered {
			t.Fatal("not an ordinary table")
		}
		n := runNode(t, set, c, "zz")
		p := s.physicalPlacement(n, s.At(1))
		bound, err := tr.linkPlacedBound(c, s, n, p)
		if err != nil {
			t.Fatal(err)
		}
		nested, err := w.prepay(bound, false)
		if err != nil {
			t.Fatal(err)
		}
		w.visits = true
		w.Store(0)
		before := w.credit
		mustLink(t, tr.linkPlaced(c, s, n, p))
		consumed, visits := before-w.credit, w.Load()
		w.closeCredit(nested)
		t.Logf("n=%d transition visits=%d consumed=%d", count, visits, consumed)
		if consumed+64 < uint64(visits) { //nolint:gosec // counted work is nonnegative
			t.Fatalf("transition work refunded: visits=%d consumed=%d", visits, consumed)
		}
		opidxAssertTreeInvariant(t, tr)
	}
}

func TestRunSelectedEndCredits(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, count := range []int{64, 4096} {
		for _, retained := range []bool{false, true} {
			tr, c := runTree(t, set, runSelectedInput(count)...)
			w := &tr.work
			nested, err := w.prepay(1000000, false)
			if err != nil {
				t.Fatal(err)
			}
			before := w.credit
			var hi int
			if retained {
				hi = tr.retainedRunEnd(&c.kids, 0)
			} else {
				hi = tr.runEnd(&c.kids, 0)
			}
			consumed := before - w.credit
			w.closeCredit(nested)
			if hi != count || consumed != uint64(count) { //nolint:gosec // positive test size
				t.Fatalf("retained=%t selected=%d hi=%d consumed=%d", retained, count, hi, consumed)
			}
		}
	}
}

// A singleton retained tree is detached intact: rb_remove_node never runs.
// Ordinary layouts keep main's conservative structural allowance nonetheless.
func TestRunAloneUnlinkCredit(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	tr, c := runTree(t, set)
	for _, v := range []string{"1", "2"} {
		if _, err := tr.NewTerm(c, "ll", v, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	mustLink(t, c.kids.At(1).Unlink())
	n := c.kids.At(0)
	if !c.kids.rbTree[n] {
		t.Fatal("missing singleton retained tree")
	}
	nested, err := tr.work.prepay(1000000, false)
	if err != nil {
		t.Fatal(err)
	}
	before := tr.work.credit
	_, err = prepareUnlink(n)
	if err != nil {
		t.Fatal(err)
	}
	consumed := before - tr.work.credit
	tr.work.closeCredit(nested)
	if consumed != 70 {
		t.Fatalf("ordinary alone plan consumed=%d, main=70", consumed)
	}
	mustLink(t, n.Unlink())
	if n.detachedRB == nil {
		t.Fatal("singleton topology was removed instead of detached")
	}
}

// A schema separator with one ordinary left owner needs no cold owner collection.
// Reusing an otherwise unused plan slot must preserve this allocation path too.
func TestRunOrdinarySeparatorPlanBytes(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	tr, c := runTree(t, set, "ll=1", "ll=2", "b=x", "ll=3", "ll=4")
	if _, err := tr.NewTerm(c, "ll", "5", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	var separator *Node
	for n := range c.kids.all() {
		if n.Name() == "b" {
			separator = n
			break
		}
	}
	if separator == nil || c.kids.st.interleaved() {
		t.Fatal("not an ordinary schema separator")
	}
	plan, err := prepareUnlink(separator)
	if err != nil {
		t.Fatal(err)
	}
	if plan.left == nil {
		t.Fatal("did not reach left-owner preparation")
	}
	const count = 512
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range count {
		tr.work.unlinkPlan = nil
		if _, err := prepareUnlink(separator); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	bytes := after.TotalAlloc - before.TotalAlloc
	want := uint64(96)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		want = 48
	}
	t.Logf("ordinary separator plan bytes=%d per-plan=%d", bytes, bytes/count)
	if bytes != count*want {
		t.Fatalf("separator preparation bytes=%d want=%d", bytes, count*want)
	}
	mustLink(t, separator.Unlink())
	checkTreeRetainedRB(t, tr)
}

// The successor's NULL metadata survives duplication. Removing the singleton
// topology's leader transfers another slot to that successor before compaction.
func TestRunSuccessorQueueCompactionCredit(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	tr, c := runTree(t, set)
	for _, v := range []string{"1", "2"} {
		if _, err := tr.NewTerm(c, "ll", v, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	mustLink(t, c.kids.At(1).Unlink())
	leader := c.kids.At(0)
	copies := newTree(set)
	parent, _, err := copies.Dup(c, nil, DupOptions{Recursive: true, WithFlags: true})
	if err != nil {
		t.Fatal(err)
	}
	next := parent.kids.docFirst()
	mustLink(t, next.Unlink())
	// LAST retains the copied metadata, as later same-schema copies do in dupR.
	mustLink(t, tr.insert(c, next, insertLast))
	if c.kids.rbGroups[next] == nil || c.kids.rbTree[next] {
		t.Fatal("dup did not retain NULL metadata")
	}
	nested, err := tr.work.prepay(1000000, false)
	if err != nil {
		t.Fatal(err)
	}
	before := tr.work.credit
	_, err = prepareUnlink(leader)
	if err != nil {
		t.Fatal(err)
	}
	consumed := before - tr.work.credit
	tr.work.closeCredit(nested)
	t.Logf("populated to NULL successor: plan consumed=%d", consumed)
	// The ordinary layout preserves main's 78-unit plan debit. Exact physical
	// queue-compaction accounting applies only after interleaving.
	if consumed != 78 {
		t.Fatalf("ordinary plan consumed=%d, main=78", consumed)
	}
	mustLink(t, leader.Unlink())
	if len(c.kids.metaGroups(next)) != 2 {
		t.Fatal("compaction lost a NULL slot")
	}
	checkTreeRetainedRB(t, tr)
}

// Preparation reserves the later marking pass; it must consume that credit when
// marking actually runs, rather than charging it before a suffix can be refused.
func TestRunJoinedOwnerConsumption(t *testing.T) {
	set := reviewDiffSet(t, runSchema)
	for _, pieces := range []int{3, 8, 32} {
		var input []string
		for i := range pieces {
			if i != 0 {
				input = append(input, "zz")
			}
			input = append(input, "ll="+strconv.Itoa(100*i+2), "ll="+strconv.Itoa(100*i+4))
		}
		tr, c := runTree(t, set, input...)
		for i := range pieces {
			if _, err := tr.NewTerm(c, "ll", strconv.Itoa(100*i+1), NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		var separators []*Node
		for _, n := range c.kids.docSpan().All() {
			if n.schema == nil {
				separators = append(separators, n)
			}
		}
		for _, separator := range separators[:len(separators)-1] {
			mustLink(t, separator.Unlink())
		}
		separator := separators[len(separators)-1]
		plan, err := prepareUnlink(separator)
		if err != nil {
			t.Fatal(err)
		}
		nested, err := tr.work.prepay(100000, false)
		if err != nil {
			t.Fatal(err)
		}
		before := tr.work.credit
		plan.markJoinedRun()
		if tr.work.hashErr != nil {
			t.Fatal(tr.work.hashErr)
		}
		consumed := before - tr.work.credit
		tr.work.closeCredit(nested)
		want := uint64(pieces - 2) //nolint:gosec // positive test size
		if consumed != want {
			t.Fatalf("pieces=%d interior owner marking consumed=%d, want %d", pieces, consumed, want)
		}
		t.Logf("pieces=%d marking consumed=%d", pieces, consumed)
		mustLink(t, separator.Unlink())
		checkTreeRetainedRB(t, tr)
	}
}
