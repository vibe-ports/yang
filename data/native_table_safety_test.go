// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"testing"

	"github.com/vibe-ports/yang"
)

func TestNativeResourceRefusalsAreSticky(t *testing.T) {
	for _, kind := range []string{"free-slot", "used-limit", "doubling"} {
		t.Run(kind, func(t *testing.T) {
			mode, count := uint8(0), 8
			if kind == "doubling" {
				mode, count = 1, 5
			}
			h, table := nativeFresh(t, 8, mode)
			n := new(Node)
			for range count {
				nativePut(t, h, table, n, 3, false)
			}
			if kind == "used-limit" {
				h.maxUsed = table.used
				// Distinguish the used refusal from exhausted free slots.
				nativeRemove(t, h, table, n, 3)
				h.maxUsed = table.used
			}
			if kind == "doubling" {
				h.maxSize = 8
			}
			before := *table
			image := nativeObserve(t, table, []*Node{n}, nativeOK)
			_, first := h.primitive(table, n, 11, false, false)
			if !errors.Is(first, yang.ErrBudget) || !errors.Is(h.work.hashErr, first) {
				t.Fatalf("refusal not latched: returned=%v governing=%v", first, h.work.hashErr)
			}
			charges := h.work.hashUsed
			// A successful removal would publish a different used/free history.
			_, err := h.primitive(table, n, 3, false, true)
			if !errors.Is(err, first) || *table != before || h.work.hashUsed != charges {
				t.Fatalf("continuation: first=%v next=%v changed=%t", first, err, *table != before)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			polls := &nativePollContext{Context: ctx}
			h.ctx = polls
			_, err = h.primitive(table, n, 3, false, true)
			if !errors.Is(err, first) || polls.calls != 0 || *table != before || h.work.hashUsed != charges {
				t.Fatalf("cancellation replaced refusal: error=%v polls=%d changed=%t", err, polls.calls, *table != before)
			}
			// A second direct arithmetic failure also preserves the first error.
			if err := h.alloc(0, 0); !errors.Is(err, first) {
				t.Fatalf("second refusal replaced first: %v", err)
			}
			if !reflect.DeepEqual(image, nativeObserve(t, table, []*Node{n}, nativeOK)) {
				t.Fatal("refused continuation changed live/free topology")
			}
		})
	}
}

func TestNativeFirstBuildChecksEveryFrontier(t *testing.T) {
	for _, field := range []string{"owner", "node", "prev", "next", "parent"} {
		t.Run(field, func(t *testing.T) {
			h, table, p, sn := nativeModel(t)
			children := []*nativeChild{
				nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q"),
				nativeModelNode(t, h, sn[2], "QQ=="), nativeModelNode(t, h, sn[2], "Qg=="),
			}
			nativeModelLink(p, children...)
			foreign := newNativeHarness(&workCounter{})
			bad := *children[1]
			bad.owner = foreign
			switch field {
			case "owner":
				children[0].owner = foreign
			case "node":
				children[0].node = nil
			case "prev":
				children[2].prev = &bad // triggering child's own frontier remains valid
			case "next":
				children[0].next = &bad
			case "parent":
				children[0].parent = &bad
			}
			before := *table
			_, err := h.event(table, children[3], false)
			if !errors.Is(err, errInProgress) || *table != before || table.present {
				t.Fatalf("first build accepted foreign %s: error=%v changed=%t", field, err, *table != before)
			}
		})
	}
}

// Each paid checkpoint can inject cancellation or panic after earlier writes.
// The context is operation-private and never changes production cancellation.
type nativeStopContext struct {
	context.Context
	calls, stop int
	panicAtStop bool
}

func (c *nativeStopContext) Err() error {
	c.calls++
	if c.calls == c.stop {
		if c.panicAtStop {
			panic("native rollback control")
		}
		return context.Canceled
	}
	return nil
}

func TestNativeBuildAndShrinkRollback(t *testing.T) {
	for _, operation := range []string{"build", "shrink", "multi-page-shrink"} {
		t.Run(operation, func(t *testing.T) {
			h, table, p, sn := nativeModel(t)
			children := []*nativeChild{
				nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q"),
				nativeModelNode(t, h, sn[2], "QQ=="), nativeModelNode(t, h, sn[2], "Qg=="),
			}
			nativeModelLink(p, children...)
			nodes := []*Node{children[0].node, children[1].node, children[2].node, children[3].node}
			if operation != "build" {
				size := uint32(16)
				if operation == "multi-page-shrink" {
					size = 256
				}
				rc, err := h.create(table, size, 2)
				nativeRequire(t, rc, err, nativeOK)
				for i := uint32(0); i < size/4; i++ {
					nativePut(t, h, table, nodes[0], i*8, false)
				}
			}
			base := *table
			before := nativeObserve(t, table, nodes, nativeOK)
			perform := func() (nativeStatus, error) {
				if operation == "build" {
					return h.event(table, children[3], false)
				}
				return h.primitive(table, nodes[0], 0, false, true)
			}
			polls := &nativeStopContext{Context: context.Background()}
			h.ctx = polls
			rc, err := perform()
			nativeRequire(t, rc, err, nativeOK)
			checkpoints := polls.calls
			if operation != "build" && table.size != base.size/2 {
				t.Fatal("control did not reach shrink")
			}
			// Cover every poll for build/small shrink; sample directory/page and
			// late reinsertion boundaries for the larger, multi-page case.
			stride := 1
			if operation == "multi-page-shrink" {
				stride = max(1, checkpoints/64)
			}
			for _, panicAtStop := range []bool{false, true} {
				for stop := 1; stop <= checkpoints; stop += stride {
					*table = base
					h.ctx = &nativeStopContext{Context: context.Background(), stop: stop, panicAtStop: panicAtStop}
					var caught any
					func() {
						defer func() { caught = recover() }()
						_, err = perform()
						if !panicAtStop && !errors.Is(err, context.Canceled) {
							t.Fatalf("stop=%d error=%v", stop, err)
						}
					}()
					if panicAtStop && caught != "native rollback control" {
						t.Fatalf("stop=%d panic=%v", stop, caught)
					}
					if *table != base || h.active || !reflect.DeepEqual(before, nativeObserve(t, table, nodes, nativeOK)) {
						t.Fatalf("%s stop=%d/%d panic=%t: rollback lost roots/scalars/topology", operation, stop, checkpoints, panicAtStop)
					}
				}
			}
			t.Logf("%s: %d checkpoints, stride %d, cancellation and panic rollback", operation, checkpoints, stride)
		})
	}
}

func TestNativeFirstBuildFrontierAccounting(t *testing.T) {
	// Ownership validation is constant work in the already paid count pass.
	// Duplicate-heavy descending values force adverse FIFO insertion order.
	for _, count := range []int{64, 256} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			h, table, p, sn := nativeModel(t)
			children := make([]*nativeChild, count)
			for i := range children {
				children[i] = nativeModelNode(t, h, sn[3], fmt.Sprint((count-i)%3))
			}
			nativeModelLink(p, children...)
			rc, err := h.event(table, children[count-1], false)
			nativeRequire(t, rc, err, nativeOK)
			// Measured against the pre-fix G1 kernel: validation adds neither a
			// second traversal nor a new budget, and all executed count steps
			// remain charged. Production main/head controls run separately.
			want := map[int][2]uint64{64: {4305, 21237}, 256: {17200, 58864}}[count]
			if uint64(h.work.Load()) != want[0] || (strconv.IntSize == 64 && h.work.hashUsed != want[1]) { //nolint:gosec // positive test-only visit counts
				t.Fatalf("build work grew: visits=%d charges=%d want=%v", h.work.Load(), h.work.hashUsed, want)
			}
			t.Logf("children=%d visits=%d charges=%d", count, h.work.Load(), h.work.hashUsed)
		})
	}
}
