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
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// The test observer reads roots directly so observation changes neither work nor
// state. It is not a kernel read route or an uncharged production helper.
func nativeInspect[T any](a nativeArray[T], index uint32) T {
	p := a.root
	for shift := 21; shift >= 6; shift -= 5 {
		p = p.child[(index>>shift)&31]
	}
	return p.cells[index&63]
}

type nativeLiveSlot struct {
	Bucket uint32 `json:"bucket"`
	Slot   uint32 `json:"slot"`
	Node   int    `json:"node"`
	Hash   uint32 `json:"hash"`
	Next   uint32 `json:"next"`
}

type nativeImage struct {
	RC        nativeStatus     `json:"rc"`
	Size      uint32           `json:"size"`
	Used      uint32           `json:"used"`
	Mode      uint8            `json:"mode"`
	Free      uint32           `json:"free"`
	Chains    []nativeLiveSlot `json:"chains"`
	FreeChain []uint32         `json:"free_chain"`
}

func nativeObserve(t *testing.T, table *nativeTable, nodes []*Node, rc nativeStatus) nativeImage {
	t.Helper()
	image := nativeImage{RC: rc, Size: table.size, Used: table.used, Mode: table.mode, Free: table.free,
		Chains: []nativeLiveSlot{}, FreeChain: []uint32{}}
	id := func(n *Node) int {
		for i, candidate := range nodes {
			if n == candidate {
				return i + 1
			}
		}
		t.Fatal("unregistered observer node")
		return 0
	}
	seen := map[uint32]bool{}
	for b := uint32(0); b < table.size; b++ {
		bucket := nativeInspect(table.buckets, b)
		last := nativeNoRecord
		for i := bucket.first; i != nativeNoRecord; {
			if i >= table.size || seen[i] {
				t.Fatalf("invalid/repeated live slot %d", i)
			}
			seen[i] = true
			r := nativeInspect(table.records, i)
			image.Chains = append(image.Chains, nativeLiveSlot{b, i, id(r.node), r.hash, r.next})
			last, i = i, r.next
		}
		if last != bucket.last {
			t.Fatalf("bucket %d last=%d want %d", b, bucket.last, last)
		}
	}
	if uint32(len(seen)) != table.used { //nolint:gosec // bounded by table.size
		t.Fatalf("used=%d live=%d", table.used, len(seen))
	}
	for i := table.free; i != table.size; {
		if i >= table.size || seen[i] {
			t.Fatalf("invalid/repeated free slot %d", i)
		}
		seen[i] = true
		image.FreeChain = append(image.FreeChain, i)
		r := nativeInspect(table.records, i)
		if r.node != nil {
			t.Fatalf("inactive slot %d retains a node", i)
		}
		i = r.next
	}
	if uint32(len(seen)) != table.size { //nolint:gosec // bounded by table.size
		t.Fatalf("lost slots: live+free=%d size=%d", len(seen), table.size)
	}
	return image
}

func nativeRequire(t *testing.T, got nativeStatus, err error, want nativeStatus) {
	t.Helper()
	if err != nil || got != want {
		t.Fatalf("status=%d error=%v, want status=%d", got, err, want)
	}
}

func nativeFresh(t *testing.T, size uint32, mode uint8) (*nativeHarness, *nativeTable) {
	t.Helper()
	h := newNativeHarness(&workCounter{visits: true})
	table := &nativeTable{owner: h}
	rc, err := h.create(table, size, mode)
	nativeRequire(t, rc, err, nativeOK)
	return h, table
}

func nativePut(t *testing.T, h *nativeHarness, table *nativeTable, node *Node, hash uint32, check bool) nativeStatus {
	t.Helper()
	rc, err := h.primitive(table, node, hash, check, false)
	if err != nil {
		t.Fatal(err)
	}
	return rc
}

func nativeRemove(t *testing.T, h *nativeHarness, table *nativeTable, node *Node, hash uint32) nativeStatus {
	t.Helper()
	rc, err := h.primitive(table, node, hash, false, true)
	if err != nil {
		t.Fatal(err)
	}
	return rc
}

func TestNativePrimitiveFIFOAndFreeReuse(t *testing.T) {
	h, table := nativeFresh(t, 8, 0)
	a, b := new(Node), new(Node)
	nodes := []*Node{a, b}
	for _, x := range []struct {
		n    *Node
		hash uint32
	}{{a, 3}, {a, 3}, {b, 3}, {a, 11}} {
		nativeRequire(t, nativePut(t, h, table, x.n, x.hash, false), nil, nativeOK)
	}
	before := nativeObserve(t, table, nodes, nativeOK)
	nativeRequire(t, nativePut(t, h, table, a, 3, true), nil, nativeExists)
	nativeRequire(t, nativeRemove(t, h, table, a, 99), nil, nativeNotFound)
	if after := nativeObserve(t, table, nodes, nativeOK); !reflect.DeepEqual(before, after) {
		t.Fatal("EEXIST/ENOTFOUND changed table")
	}
	nativeRequire(t, nativeRemove(t, h, table, a, 3), nil, nativeOK)
	if table.free != 0 || table.used != 3 {
		t.Fatalf("earliest removal free=%d used=%d", table.free, table.used)
	}
	nativeRequire(t, nativePut(t, h, table, b, 19, false), nil, nativeOK)
	image := nativeObserve(t, table, nodes, nativeOK)
	var slots []uint32
	for _, r := range image.Chains {
		slots = append(slots, r.Slot)
	}
	if !reflect.DeepEqual(slots, []uint32{1, 2, 3, 0}) {
		t.Fatalf("FIFO/free reuse slots=%v", slots)
	}
	// Exhausting a disabled table leaves the native inert size link, never a
	// sentinel or a usable address. The following insert refuses before writes.
	for table.used < 8 {
		nativePut(t, h, table, a, table.used+100, false)
	}
	if table.free != 8 {
		t.Fatalf("terminal free=%d", table.free)
	}
	before = nativeObserve(t, table, nodes, nativeOK)
	_, err := h.primitive(table, a, 17, false, false)
	if !errors.Is(err, yang.ErrBudget) || !reflect.DeepEqual(before, nativeObserve(t, table, nodes, nativeOK)) {
		t.Fatalf("free exhaustion error=%v changed table", err)
	}
}

func TestNativeResizeIdentityAndBoundaries(t *testing.T) {
	for _, check := range []bool{false, true} {
		t.Run(fmt.Sprint(check), func(t *testing.T) {
			h, table := nativeFresh(t, 8, 1)
			a, b, c := new(Node), new(Node), new(Node)
			nodes := []*Node{a, b, c}
			for i := range 4 {
				nativePut(t, h, table, a, 7, false)
				wantMode := uint8(1)
				if i == 3 {
					wantMode = 2
				}
				if table.size != 8 || table.mode != wantMode {
					t.Fatalf("50%% at %d: %+v", i, table)
				}
			}
			nativePut(t, h, table, a, 15, false) // same pointer under another hash
			nativePut(t, h, table, b, 7, check)  // distinct equal-valued node
			want := uint32(6)
			if check {
				want = 3
			}
			if table.size != 16 || table.used != want || table.mode != 2 {
				t.Fatalf("75%% checked=%t size=%d used=%d mode=%d", check, table.size, table.used, table.mode)
			}
			nativeObserve(t, table, nodes, nativeOK)
		})
	}
	h, table := nativeFresh(t, 16, 2)
	a, b := new(Node), new(Node)
	nodes := []*Node{a, b}
	for _, x := range []struct {
		n    *Node
		hash uint32
	}{{a, 7}, {a, 7}, {a, 15}, {b, 7}} {
		nativePut(t, h, table, x.n, x.hash, false)
	}
	if table.size != 16 {
		t.Fatal("shrunk at 25 percent")
	}
	nativeRemove(t, h, table, b, 7)
	if table.size != 8 || table.used != 2 {
		t.Fatalf("checked shrink size=%d used=%d", table.size, table.used)
	}
	nativeObserve(t, table, nodes, nativeOK)
	nativeRemove(t, h, table, a, 7)
	nativeRemove(t, h, table, a, 15)
	if !table.present || table.used != 0 || table.size != 8 || table.mode != 2 {
		t.Fatal("lost present-empty state")
	}
}

const nativeBuildModule = `module m { yang-version 1.1; namespace urn:m; prefix m;
 container c { leaf p {type string;} leaf q {type string;}
 leaf-list ll {type binary; ordered-by user;} leaf-list v {type uint32;}
 leaf z {type string;} } }`

func nativeModel(t *testing.T) (*nativeHarness, *nativeTable, *nativeChild, []*schema.Node) {
	t.Helper()
	set := reviewDiffSet(t, nativeBuildModule)
	sn := opIndexModule(t, set).Top[0]
	h := newNativeHarness(&workCounter{visits: true})
	p := &nativeChild{owner: h, node: &Node{schema: sn}}
	return h, &nativeTable{owner: h}, p, sn.Children
}

func nativeModelNode(t *testing.T, h *nativeHarness, sn *schema.Node, text string) *nativeChild {
	t.Helper()
	n := &Node{schema: sn}
	if sn != nil {
		n.hash = types.FinishHash32(schemaHash(sn))
		if sn.Kind == schema.LeafList || sn.Kind == schema.Leaf {
			hints := types.HintString
			if sn.Type.Base == schema.Uint32 {
				hints = types.HintDecNum
			}
			v, diag := types.Store(sn.Type, text, types.FormatJSON, hints, nil, sn)
			if diag != nil {
				t.Fatal(diag)
			}
			n.value = v
			if sn.Kind == schema.LeafList {
				hash, ok := types.HashLYB(schemaHash(sn), v)
				if !ok {
					t.Fatal("unsupported test value hash")
				}
				n.hash = types.FinishHash32(hash)
			}
		}
	}
	return &nativeChild{owner: h, node: n}
}

// Physical setup occurs privately before the model event. These are not native
// registration writes and never change a real Node's links or production index.
func nativeModelLink(parent *nativeChild, children ...*nativeChild) {
	parent.first = children[0]
	for i, c := range children {
		c.parent, c.prev, c.next = parent, children[(i+len(children)-1)%len(children)], nil
		if i+1 < len(children) {
			c.next = children[i+1]
		}
	}
}

func TestNativeBuildAbortsInterleavedCollision(t *testing.T) {
	for _, collisionFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(collisionFirst), func(t *testing.T) {
			h, table, p, sn := nativeModel(t)
			a, b := nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q")
			collision := nativeModelNode(t, h, sn[2], "KpfzBg==")
			ordinary := nativeModelNode(t, h, sn[2], "QQ==")
			if collision.node.hash != 0x7be55cae || collision.node.hash != types.FinishHash32(schemaHash(sn[2])) {
				t.Fatalf("collision hash=%08x", collision.node.hash)
			}
			x, y := collision, ordinary
			if !collisionFirst {
				x, y = y, x
			}
			nativeModelLink(p, a, b, x, y)
			rc, err := h.event(table, y, false)
			want, used := nativeOK, uint32(5)
			if collisionFirst {
				want, used = nativeInternal, 3
			}
			nativeRequire(t, rc, err, want)
			if !table.present || table.used != used {
				t.Fatalf("partial state used=%d want=%d", table.used, used)
			}
			if collisionFirst {
				found, _, err := h.pair(table, ordinary.node, ordinary.node.hash)
				if err != nil || found != nativeNoRecord {
					t.Fatalf("build continued into suffix: %d %v", found, err)
				}
				// A later I uses the retained partial table and cannot reconstruct QQ==.
				later := nativeModelNode(t, h, sn[2], "Qg==")
				nativeModelLink(p, a, b, x, y, later)
				rc, err = h.event(table, later, false)
				nativeRequire(t, rc, err, nativeOK)
				found, _, err = h.pair(table, ordinary.node, ordinary.node.hash)
				if err != nil || found != nativeNoRecord || table.used != 4 {
					t.Fatal("later I rebuilt missing suffix")
				}
			}
		})
	}
}

func TestNativeExistingEventsAndOpaqueSeparators(t *testing.T) {
	h, table, p, sn := nativeModel(t)
	a, b := nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q")
	x, y := nativeModelNode(t, h, sn[2], "QQ=="), nativeModelNode(t, h, sn[2], "Qg==")
	opaq := nativeModelNode(t, h, nil, "")
	nativeModelLink(p, a, b, x, opaq, y)
	rc, err := h.event(table, y, false)
	nativeRequire(t, rc, err, nativeOK)
	if table.used != 6 {
		t.Fatalf("opaque-separated run starts used=%d", table.used)
	}
	// Front insertion removes only the same-schema successor's schema pair.
	z := nativeModelNode(t, h, sn[2], "Qw==")
	nativeModelLink(p, a, b, z, x, opaq, y)
	rc, err = h.event(table, z, false)
	nativeRequire(t, rc, err, nativeOK)
	hash := types.FinishHash32(schemaHash(sn[2]))
	if i, _, err := h.pair(table, x.node, hash); err != nil || i != nativeNoRecord {
		t.Fatal("old successor schema pair survived")
	}
	// U must run while z still has its original linked neighbours.
	rc, err = h.event(table, z, true)
	nativeRequire(t, rc, err, nativeOK)
	if i, _, err := h.pair(table, x.node, hash); err != nil || i == nativeNoRecord {
		t.Fatal("successor schema pair not restored")
	}
	nativeModelLink(p, a, b, x, opaq, y)
	// Missing first removal stops U before its schema promotion.
	rc, err = h.event(table, z, true)
	nativeRequire(t, rc, err, nativeInternal)
	other := newNativeHarness(&workCounter{})
	before := *table
	_, err = other.event(table, x, false)
	if !errors.Is(err, errInProgress) || *table != before {
		t.Fatal("cross-owner event wrote state")
	}
}

func TestNativeRefusalAndTouchRestore(t *testing.T) {
	for _, kind := range []string{"used", "double", "address", "cancel", "held", "prepaid", "journal"} {
		t.Run(kind, func(t *testing.T) {
			h, table := nativeFresh(t, 8, 1)
			a := new(Node)
			for range 5 {
				nativePut(t, h, table, a, 3, false)
			}
			before := nativeObserve(t, table, []*Node{a}, nativeOK)
			switch kind {
			case "used":
				h.maxUsed = 5
			case "double":
				h.maxSize = 8
			case "address":
				table.free = 8
			case "cancel":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				h.ctx = ctx
			case "held":
				h.work.suspendDepth = 1
			case "prepaid":
				h.work.prepaid = true
			case "journal":
				h.work.journal = &hashTransaction{}
			}
			_, err := h.primitive(table, a, 11, false, false)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if kind == "address" {
				table.free = before.Free
			}
			if !reflect.DeepEqual(before, nativeObserve(t, table, []*Node{a}, nativeOK)) {
				t.Fatal("refusal mutated table")
			}
		})
	}
	// Exhaust each actual charge boundary of a growing insertion. The old roots
	// must survive even if failure occurs after a page write or during reinsertion.
	h, table := nativeFresh(t, 8, 1)
	a := new(Node)
	for range 5 {
		nativePut(t, h, table, a, 3, false)
	}
	base := *table
	before := nativeObserve(t, table, []*Node{a}, nativeOK)
	start := h.work.hashUsed
	nativePut(t, h, table, a, 11, false)
	cost := h.work.hashUsed - start
	for allowance := uint64(1); allowance < cost; allowance++ {
		*table = base
		h.work.hashUsed, h.work.hashErr, h.work.hashLimit = 0, nil, allowance
		_, err := h.primitive(table, a, 11, false, false)
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("allowance=%d cost=%d error=%v", allowance, cost, err)
		}
		if *table != base {
			t.Fatalf("failed touch/resize did not restore roots/scalars at %d", allowance)
		}
		if !reflect.DeepEqual(before, nativeObserve(t, table, []*Node{a}, nativeOK)) {
			t.Fatalf("failed touch/resize did not restore at %d", allowance)
		}
	}
}

func TestNativePageAddressDomain(t *testing.T) {
	h := newNativeHarness(&workCounter{visits: true})
	table := &nativeTable{owner: h, records: nativeArray[nativeOccurrence]{size: nativeMaxSize}}
	indices := []uint32{0, 7, 8, 31, 32, 63, 64, 2047, 2048, 65535, 65536, 2097151, 2097152, nativeMaxSize - 1}
	nodes := make([]*Node, len(indices))
	for i := range nodes {
		nodes[i] = new(Node)
	}
	for i, index := range indices {
		rc, err := h.edit(table, func() (nativeStatus, error) {
			p, err := table.records.touch(h, index)
			if err != nil {
				return nativeOK, err
			}
			*p = nativeOccurrence{node: nodes[i], hash: index, next: nativeNoRecord}
			return nativeOK, nil
		})
		nativeRequire(t, rc, err, nativeOK)
	}
	for i, index := range indices {
		r, err := table.records.read(h, index)
		if err != nil || r.node != nodes[i] || r.hash != index {
			t.Fatalf("page index=%d record=%+v error=%v", index, r, err)
		}
	}
	_, err := table.records.read(h, nativeMaxSize)
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("out-of-domain read: %v", err)
	}
	for _, size := range []uint32{0, 1, 7, 9, nativeMaxSize + 1, 1 << 26, 1 << 31, nativeNoRecord} {
		before := *table
		_, err := h.create(table, size, 1)
		if !errors.Is(err, yang.ErrBudget) || *table != before {
			t.Fatalf("invalid size=%d error=%v changed=%t", size, err, *table != before)
		}
	}
}

func TestNativeUncheckedAppendWork(t *testing.T) {
	// Duplicate-heavy reverse hashes exercise FIFO insertion's tail, capacity
	// transitions and page copies. An accidental bucket scan on unchecked append
	// changes this from linear work to quadratic; native performs no such scan.
	var previousVisits int64
	var previousCharges uint64
	for _, count := range []int{64, 256, 1024} {
		h, table := nativeFresh(t, 8, 1)
		a := new(Node)
		for i := count - 1; i >= 0; i-- {
			nativePut(t, h, table, a, uint32(i%3)*8, false)
		}
		visits, charges := h.work.Load(), h.work.hashUsed
		if table.used != uint32(count) { //nolint:gosec // fixed small counts
			t.Fatal("unchecked append lost occurrences")
		}
		if previousVisits != 0 && (visits > 5*previousVisits || charges > 5*previousCharges) {
			t.Fatalf("nonlinear append: n=%d visits=%d charges=%d previous=%d/%d", count, visits, charges, previousVisits, previousCharges)
		}
		t.Logf("n=%d executed checkpoints=%d charged=%d", count, visits, charges)
		previousVisits, previousCharges = visits, charges
	}
}

func TestNativePageTouchAccounting(t *testing.T) {
	h, table := nativeFresh(t, 8, 0)
	start, visits := h.work.hashUsed, h.work.Load()
	rc, err := h.edit(table, func() (nativeStatus, error) {
		r, err := table.records.touch(h, 3)
		if err != nil {
			return nativeOK, err
		}
		r.hash = 99
		return nativeOK, nil
	})
	nativeRequire(t, rc, err, nativeOK)
	// Five copied radix directories: each initializes and copies 32 cells,
	// with 512 rounded allocated bytes on amd64 (256 on 386). The leaf
	// initializes/copies eight cells and allocates 128 rounded bytes on both.
	// Seven other units are scalar touch, five address steps and final check.
	want := uint64(3031)
	if strconv.IntSize == 32 {
		want = 1751
	}
	if charge, steps := h.work.hashUsed-start, h.work.Load()-visits; charge != want || steps != 13 {
		t.Fatalf("page touch charged=%d checkpoints=%d want=%d/13", charge, steps, want)
	}
}

func TestNativeForeignFrontierRefusedBeforeWrite(t *testing.T) {
	h, table, p, sn := nativeModel(t)
	children := []*nativeChild{nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q"), nativeModelNode(t, h, sn[2], "QQ=="), nativeModelNode(t, h, sn[2], "Qg==")}
	nativeModelLink(p, children...)
	rc, err := h.event(table, children[3], false)
	nativeRequire(t, rc, err, nativeOK)
	foreign := newNativeHarness(&workCounter{})
	for _, previous := range []bool{true, false} {
		bad := *children[2]
		neighbour := *children[3]
		neighbour.owner = foreign
		if previous {
			bad.prev = &neighbour
		} else {
			bad.next = &neighbour
		}
		for _, unlink := range []bool{true, false} {
			before, charges := *table, h.work.hashUsed
			_, err := h.event(table, &bad, unlink)
			if !errors.Is(err, errInProgress) || *table != before || h.work.hashUsed != charges {
				t.Fatalf("foreign frontier previous=%t U=%t: error=%v wrote=%t", previous, unlink, err, *table != before)
			}
		}
	}
}

type nativePollContext struct {
	context.Context
	calls int
}

func (c *nativePollContext) Err() error {
	c.calls++
	return c.Context.Err()
}

func TestNativeStickyBudgetPrecedesCancellation(t *testing.T) {
	h := newNativeHarness(&workCounter{hashLimit: 1})
	table := &nativeTable{owner: h}
	_, err := h.create(table, 8, 1)
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("initial error=%v", err)
	}
	before := *table
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	polls := &nativePollContext{Context: ctx}
	h.ctx = polls
	h.work.hashLimit = defaultMaxHashWork // a larger limit does not clear the sticky failure
	_, err = h.create(table, 8, 1)
	if !errors.Is(err, yang.ErrBudget) || polls.calls != 0 || *table != before {
		t.Fatalf("sticky error=%v polls=%d mutated=%t", err, polls.calls, *table != before)
	}
}
