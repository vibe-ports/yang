// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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
	// Checked insertion must resolve EEXIST before asking for a free slot.
	nativeRequire(t, nativePut(t, h, table, a, 3, true), nil, nativeExists)
	if !reflect.DeepEqual(before, nativeObserve(t, table, nodes, nativeOK)) {
		t.Fatal("checked EEXIST changed an exhausted table")
	}
	_, err := h.primitive(table, a, 17, false, false)
	if !errors.Is(err, yang.ErrBudget) || !reflect.DeepEqual(before, nativeObserve(t, table, nodes, nativeOK)) {
		t.Fatalf("free exhaustion error=%v changed table", err)
	}
}

func TestNativeShrinkDisabledAboveMinimum(t *testing.T) {
	for _, mode := range []uint8{0, 1} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			h, table := nativeFresh(t, 16, mode)
			a := new(Node)
			nativeRequire(t, nativePut(t, h, table, a, 7, false), nil, nativeOK)
			nativeRequire(t, nativeRemove(t, h, table, a, 7), nil, nativeOK)
			if !table.present || table.size != 16 || table.mode != mode || table.used != 0 {
				t.Fatalf("disabled shrink mode=%d: %+v", mode, table)
			}
			nativeObserve(t, table, []*Node{a}, nativeOK)
		})
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

func TestNativeOpaqueThresholdAndEligibility(t *testing.T) {
	h, table, p, sn := nativeModel(t)
	a, b := nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q")
	x, y := nativeModelNode(t, h, sn[2], "QQ=="), nativeModelNode(t, h, sn[2], "Qg==")
	opaq := nativeModelNode(t, h, nil, "")
	nativeModelLink(p, a, b, x, opaq)
	rc, err := h.event(table, x, false)
	nativeRequire(t, rc, err, nativeOK)
	if table.present {
		t.Fatal("three schema children plus opaque created a table")
	}
	nativeModelLink(p, a, b, x, opaq, y)
	rc, err = h.event(table, y, false)
	nativeRequire(t, rc, err, nativeOK)
	if !table.present || table.used != 6 {
		t.Fatalf("four schema children with opaque separator: present=%t used=%d", table.present, table.used)
	}
	sh := types.FinishHash32(schemaHash(sn[2]))
	for _, c := range []*nativeChild{x, y} {
		if i, _, err := h.pair(table, c.node, sh); err != nil || i == nativeNoRecord {
			t.Fatalf("opaque-separated run start missing schema occurrence: %v", err)
		}
	}
	if i, _, err := h.pair(table, opaq.node, 0); err != nil || i != nativeNoRecord {
		t.Fatalf("opaque child acquired occurrence: %v", err)
	}
	for _, unlink := range []bool{false, true} {
		before := *table
		rc, err := h.event(table, opaq, unlink)
		nativeRequire(t, rc, err, nativeOK)
		if *table != before {
			t.Fatal("opaque-node event changed table")
		}
	}
	for _, kind := range []string{"opaque-parent", "top-level", "anydata", "anyxml"} {
		t.Run(kind, func(t *testing.T) {
			h, table, p, sn := nativeModel(t)
			children := []*nativeChild{nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q"), nativeModelNode(t, h, sn[2], "QQ=="), nativeModelNode(t, h, sn[2], "Qg==")}
			nativeModelLink(p, children...)
			switch kind {
			case "opaque-parent":
				p.node.schema = nil
			case "top-level":
				children[3].parent = nil
			default:
				parentSchema := *p.node.schema
				parentSchema.Kind = schema.AnyData
				if kind == "anyxml" {
					parentSchema.Kind = schema.AnyXML
				}
				p.node.schema = &parentSchema
			}
			before := *table
			rc, err := h.event(table, children[3], false)
			nativeRequire(t, rc, err, nativeOK)
			if *table != before {
				t.Fatalf("ineligible %s created a table", kind)
			}
		})
	}
}

// D-0062 policy controls: these frontiers deliberately differ from the native
// undefined prefix. G1 supplies them to its private kernel, without routing the
// production merge adapter through that kernel.
func TestNativeMerge2PolicyFrontiers(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, opaqueSeparator := range []bool{false, true} {
			t.Run(fmt.Sprintf("existing=%t/opaque=%t", existing, opaqueSeparator), func(t *testing.T) {
				h, table, parent, sn := nativeModel(t)
				d4, d1 := nativeModelNode(t, h, sn[3], "4"), nativeModelNode(t, h, sn[3], "1")
				s2, s3 := nativeModelNode(t, h, sn[3], "2"), nativeModelNode(t, h, sn[3], "3")
				var prefix []*nativeChild
				if existing {
					prefix = []*nativeChild{nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q")}
				}
				opaq := nativeModelNode(t, h, nil, "")
				frontier := func(run ...*nativeChild) []*nativeChild {
					children := append([]*nativeChild{}, prefix...)
					children = append(children, run...)
					if opaqueSeparator {
						// The physical-extent rule preserves the opaque slot at
						// the second position of the affected destination extent.
						at := len(prefix) + 1
						children = append(children, nil)
						copy(children[at+1:], children[at:])
						children[at] = opaq
					}
					return children
				}
				nativeModelLink(parent, frontier(d4, d1)...)
				rc, err := h.event(table, d1, false)
				nativeRequire(t, rc, err, nativeOK)
				nodes := []*Node{parent.node, d4.node, d1.node, s2.node, s3.node, opaq.node}
				for _, c := range prefix {
					nodes = append(nodes, c.node)
				}
				before, charges := nativeObserve(t, table, nodes, nativeOK), h.work.hashUsed
				initialSize, initialUsed := uint32(8), uint32(5)
				if opaqueSeparator {
					initialSize, initialUsed = 16, 6
				}
				if table.present != existing || (existing && (table.size != initialSize || table.mode != 2 || table.used != initialUsed)) {
					t.Fatalf("initial table: %+v", before)
				}
				// Permute D without I/U/H: [D4,D1] -> [D1,D4].
				nativeModelLink(parent, frontier(d1, d4)...)
				if !reflect.DeepEqual(before, nativeObserve(t, table, nodes, nativeOK)) || h.work.hashUsed != charges {
					t.Fatal("destination permutation changed registrations or charged native events")
				}
				for i, step := range []struct {
					arrival *nativeChild
					run     []*nativeChild
				}{{s3, []*nativeChild{d1, s3, d4}}, {s2, []*nativeChild{d1, s2, s3, d4}}} {
					nativeModelLink(parent, frontier(step.run...)...)
					if !opaqueSeparator && (step.arrival.prev != d1 || step.arrival.next != []*nativeChild{d4, s3}[i]) {
						t.Fatal("arrival does not see the filtered attachment frontier")
					}
					rc, err := h.event(table, step.arrival, false)
					nativeRequire(t, rc, err, nativeOK)
					if !existing && i == 0 {
						if table.present {
							t.Fatal("future S2 was visible at S3's threshold check")
						}
						continue
					}
					wantUsed, wantSize := uint32(5), uint32(8)
					if existing {
						wantUsed, wantSize = uint32(6+i), 16
					}
					if opaqueSeparator {
						// An opaque predecessor makes each arrival a run start;
						// the existing-table I removes the successor schema pair.
						wantUsed++
						wantSize = 16
					}
					if table.used != wantUsed || table.size != wantSize {
						t.Fatalf("step=%d table size/used=%d/%d want=%d/%d", i, table.size, table.used, wantSize, wantUsed)
					}
				}
				sh := types.FinishHash32(schemaHash(sn[3]))
				var winner *Node
				for _, slot := range nativeObserve(t, table, nodes, nativeOK).Chains {
					if slot.Hash == sh {
						winner = nodes[slot.Node-1]
						break
					}
				}
				wantWinner := d1.node
				if existing && !opaqueSeparator {
					wantWinner = d4.node
				}
				if winner != wantWinner {
					t.Fatal("schema occurrence no longer selects the historical winner")
				}
				if existing && !opaqueSeparator {
					if i, _, err := h.pair(table, d1.node, sh); err != nil || i != nativeNoRecord {
						t.Fatalf("permutation invented D1 schema occurrence: %v", err)
					}
				}
			})
		}
	}
}

func TestNativeMerge2GoPolicyPermutation(t *testing.T) {
	if os.Getenv("YANG_NATIVE_POLICY_OVERLAY") == "" {
		nativePolicyOverlay(t)
		return
	}
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			set := reviewDiffSet(t, nativeBuildModule)
			dst, prototype, src := newTree(set), newTree(set), newTree(set)
			parent, err := dst.NewInner(nil, "m:c", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			seedParent, err := prototype.NewInner(nil, "m:c", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if existing {
				for _, name := range []string{"p", "q"} {
					if _, err := dst.NewTerm(parent, name, name, NewOptions{}); err != nil {
						t.Fatal(err)
					}
				}
			}
			var dest []*Node
			for _, value := range []string{"4", "1"} {
				seed, err := prototype.NewTerm(seedParent, "v", value, NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				n, _, err := dst.Dup(seed, parent, DupOptions{NoSort: true, NoMeta: true})
				if err != nil {
					t.Fatal(err)
				}
				dest = append(dest, n)
			}
			if parent.kids.rbTree[dest[0]] || dest[0].inRB || dest[1].inRB || (parent.kids.ht != nil) != existing {
				t.Fatal("public NoSort/NoMeta duplications did not establish nodes2 preconditions")
			}
			var source []*Node
			// Go exposes no free sibling-list API. As in TestMergeSorted,
			// prepare its parentless source forest through the private linker;
			// the native public request independently proves reachability.
			for _, value := range []string{"2", "3"} {
				n := nativeModelNode(t, newNativeHarness(&workCounter{}), dest[0].schema, value).node
				if err := src.insert(nil, n, insertDefault); err != nil {
					t.Fatal(err)
				}
				source = append(source, n)
			}
			group := src.top.rbGroups[source[0]]
			if group == nil || !src.top.hasRB(source[0].schema) {
				t.Fatal("source has no retained RB topology")
			}
			if source[0].parent != nil || source[1].parent != nil {
				t.Fatal("source is not a parentless sibling forest")
			}
			observations := 0
			// Only the temporary compiler overlay calls this existing test
			// hook at rbPublish attachment boundaries. The callback reads the
			// real intermediate state; it neither charges nor edits it.
			overdraftHook = func(w *workCounter) {
				if w != &dst.work {
					t.Fatal("policy observer reached a different work scope")
				}
				if !parent.kids.ownsGroup(dest[1], group) || len(parent.kids.metaGroups(dest[0])) != 0 {
					t.Fatal("attachment boundary preceded metadata transfer to D1")
				}
				wantS3, wantS2 := observations >= 2, observations >= 4
				if (source[1].parent == parent) != wantS3 || (source[0].parent == parent) != wantS2 {
					t.Fatalf("boundary %d: attachment replay is not S3 then S2", observations)
				}
				var frontier []*Node
				for _, n := range parent.kids.docSpan().All() {
					if n.schema == dest[0].schema && !parent.kids.unattached[n] {
						frontier = append(frontier, n)
					}
				}
				want := []*Node{dest[1], dest[0]}
				if observations >= 1 {
					want = []*Node{dest[1], source[1], dest[0]}
				}
				if observations >= 3 {
					want = []*Node{dest[1], source[0], source[1], dest[0]}
				}
				if !reflect.DeepEqual(frontier, want) {
					t.Fatalf("boundary %d: incorrect visible attachment frontier", observations)
				}
				observations++
			}
			defer func() { overdraftHook = nil }()
			if err := dst.Insert(parent, source[0]); err != nil {
				t.Fatal(err)
			}
			overdraftHook = nil
			if observations != 5 {
				t.Fatalf("policy observer boundaries=%d want=5", observations)
			}
			t.Log("observed metadata on D1 before arrival; frontiers [D1,D4], [D1,S3,D4], [D1,S2,S3,D4]; attachment order S3,S2")
			var got []*Node
			for n := range parent.Children() {
				if n.schema == dest[0].schema {
					got = append(got, n)
				}
			}
			if !reflect.DeepEqual(got, []*Node{dest[1], source[0], source[1], dest[0]}) {
				t.Fatal("Go-policy merge did not publish [D1,S2,S3,D4]")
			}
			if !parent.kids.ownsGroup(dest[1], group) || len(parent.kids.metaGroups(dest[1])) != 1 || len(parent.kids.metaGroups(dest[0])) != 0 {
				t.Fatal("retained source metadata did not transfer to final run's first member")
			}
			checkTreeRetainedRB(t, dst)
		})
	}
}

func nativePolicyOverlay(t *testing.T) {
	t.Helper()
	path, err := filepath.Abs("insert.go")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(path) //nolint:gosec // Fixed repository source used only for the test compiler overlay.
	if err != nil {
		t.Fatal(err)
	}
	needle := "\t\tdelete(pending, n)\n\t\tt.attach(parent, sib, n)\n"
	if strings.Count(string(source), needle) != 1 {
		t.Fatal("rbPublish observer boundary no longer matches exactly")
	}
	replacement := "\t\tdelete(pending, n)\n\t\tif overdraftHook != nil { overdraftHook(&t.work) }\n\t\tt.attach(parent, sib, n)\n\t\tif overdraftHook != nil { overdraftHook(&t.work) }\n"
	initial := "\tsib.unattached = pending\n"
	if strings.Count(string(source), initial) != 1 {
		t.Fatal("rbPublish initial observer boundary no longer matches exactly")
	}
	instrumentation := strings.Replace(string(source), needle, replacement, 1)
	instrumentation = strings.Replace(instrumentation, initial, initial+"\tif overdraftHook != nil { overdraftHook(&t.work) }\n", 1)
	dir := t.TempDir()
	instrumented := filepath.Join(dir, "insert.go")
	if err := os.WriteFile(instrumented, []byte(instrumentation), 0o600); err != nil { //nolint:gosec // Private test directory and fixed overlay filename.
		t.Fatal(err)
	}
	manifest, err := json.Marshal(struct{ Replace map[string]string }{map[string]string{path: instrumented}})
	if err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlay, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	// The child compiler sees observational calls only in its temporary overlay;
	// tracked production sources, routing, layout and public API stay unchanged.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"test", "-overlay", overlay, "-run", "^TestNativeMerge2GoPolicyPermutation$", "-count=1", "-v"}
	if raceEnabled {
		args = append(args, "-race")
	}
	cmd := exec.CommandContext(ctx, "go", append(args, ".")...) //nolint:gosec // Fixed Go test command; the overlay is generated from this repository in a private test directory.
	cmd.Env = append(os.Environ(), "YANG_NATIVE_POLICY_OVERLAY=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Go policy observer: %v\n%s", err, out)
	}
	t.Logf("Go policy observer:\n%s", out)
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
