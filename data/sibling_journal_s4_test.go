// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// #240 step 4 preparation: failure/recovery histories that the sibling-sequence journal must
// keep. Each public operation is refused at every hash-work charge point; the refusal must leave
// every involved tree as it was, and then a further mutation (not a retry) must give exactly the
// trees that the further mutation gives when the refused operation never ran. A second history
// retries the refused operation and must equal the unrefused run. Observation is public:
// printed typed state plus key lookups through the sibling index. The internal sequence
// (childOrder) and retained-RB invariants are checked at every step.

const s4JournalModule = `module s4j {yang-version 1.1;namespace "urn:s4j";prefix j;
 container c {
  list l {key k;leaf k {type uint32;}leaf p {type string;}}
  leaf-list u {type string;ordered-by user;}
  leaf-list v {type string;}
 }
 rpc op {input {leaf-list w {type uint32;} list e {key k;leaf k {type uint32;}} leaf z {type string;}}
  output {leaf-list o {type uint32;} list f {key k;leaf k {type uint32;}}}}}`

// s4DataTree has a hashed keyed list (more than htMinItems), a user-ordered and a
// system-ordered leaf-list.
func s4DataTree(t *testing.T, set *schema.Set, keys ...string) *Tree {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<c xmlns="urn:s4j">`)
	for _, k := range keys {
		b.WriteString("<l><k>" + k + "</k><p>" + k + "</p></l>")
	}
	for _, v := range []string{"m", "c", "x"} {
		b.WriteString("<u>" + v + "</u><v>" + v + "</v>")
	}
	b.WriteString("</c>")
	return parseOnlyXML(t, set, b.String())
}

// s4OpTree is an RPC whose children carry the ordered sequence index (childOrder).
func s4OpTree(t *testing.T, set *schema.Set, w ...string) (*Tree, *Node) {
	t.Helper()
	tr, op := opIndexParseRPC(t, set, `{"s4j:op":{"w":[`+strings.Join(w, ",")+`],"e":[{"k":3},{"k":1}],"z":"q"}}`)
	if op.kids.order == nil {
		t.Fatal("setup: operation children have no sequence index")
	}
	return tr, op
}

func s4Child(parent *Node, name, value string) *Node {
	for n := range parent.kids.all() {
		if n.schema != nil && n.schema.Name == name {
			v := n.Value()
			if n.schema.Kind == schema.List {
				v = n.kids.first().Value()
			}
			if v == value {
				return n
			}
		}
	}
	return nil
}

// s4Observe is the public state of tr: typed dump plus keyed lookups, which go through the
// sibling hash index (and its sequence index under an operation).
func s4Observe(t *testing.T, tr *Tree) []string {
	t.Helper()
	out := typedDump(tr)
	for top := range tr.Top() {
		prefix := "/s4j:c/l"
		if top.schema != nil && top.schema.Name == "op" {
			prefix = "/s4j:op/e"
		}
		for k := range 20 {
			n, err := tr.Find(fmt.Sprintf("%s[k='%d']", prefix, k))
			out = append(out, fmt.Sprintf("find %s %d: %v %v", prefix, k, n != nil && n.Parent() == top, err))
		}
	}
	return out
}

func s4Check(t *testing.T, where string, trees []*Tree) {
	t.Helper()
	for i, tr := range trees {
		checkTreeRetainedRB(t, tr)
		stack := []*siblings{&tr.top}
		for len(stack) != 0 {
			s := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if err := opidxCheckOrderInvariant(tr, s); err != nil {
				t.Fatalf("%s: tree %d: %v", where, i, err)
			}
			if s.order != nil {
				if err := s.order.seq.CheckInvariants(); err != nil {
					t.Fatalf("%s: tree %d: sequence: %v", where, i, err)
				}
			}
			for n := range s.all() {
				stack = append(stack, &n.kids)
				if n.any != nil {
					stack = append(stack, &n.any.children) // payload sibling containers
				}
			}
		}
	}
}

type s4Scenario struct {
	name string
	// build returns fresh trees, the operation, and the further mutation run after it.
	build   func(t *testing.T) (trees []*Tree, op func() error, further func() error)
	limited []int // trees whose budget refuses the operation: both owners of a move
	// detached returns roots of the last build that belong to no returned tree (a detached
	// source); a refusal must leave them unchanged too.
	detached func() []*Node
}

// s4History runs one scenario's refusal histories against its two references.
func s4History(t *testing.T, sc s4Scenario, limited int) {
	observe := func(trees []*Tree) [][]string {
		out := make([][]string, len(trees))
		for i, tr := range trees {
			out[i] = s4Observe(t, tr)
		}
		return out
	}
	// reference: the refused operation never runs, only the further mutation
	trees, _, further := sc.build(t)
	if err := further(); err != nil {
		t.Fatalf("skip reference: %v", err)
	}
	s4Check(t, "skip reference", trees)
	wantSkip := observe(trees)
	// reference: the operation runs unrefused, then the further mutation
	trees, op, further := sc.build(t)
	var points []uint64
	trees[limited].work.hashTrace = &points
	if err := op(); err != nil {
		t.Fatalf("done reference: %v", err)
	}
	trees[limited].work.hashTrace = nil
	need := trees[limited].work.hashUsed
	if need < 2 {
		t.Fatalf("operation charged %d units to tree %d", need, limited)
	}
	if err := further(); err != nil {
		t.Fatalf("done reference: further: %v", err)
	}
	s4Check(t, "done reference", trees)
	wantDone := observe(trees)
	if reflect.DeepEqual(wantDone, wantSkip) {
		t.Fatal("setup: the operation is not observable")
	}
	limits := map[uint64]bool{}
	for limit := uint64(1); limit < need; limit++ {
		limits[limit] = true
	}
	for _, at := range points {
		if at > 1 {
			limits[at-1] = true
		}
	}
	for limit := range limits {
		for _, retry := range []bool{false, true} {
			trees, op, further := sc.build(t)
			before := make([]hashBudgetSnapshot, len(trees))
			for i, tr := range trees {
				before[i] = snapshotHashBudgetTree(tr)
			}
			var roots []*Node
			if sc.detached != nil {
				roots = sc.detached()
			}
			beforeDetached := hashBudgetSnapshot{index: map[*Node]string{}, sibs: map[*siblings]string{}}
			snapshotHashBudgetRoots(&beforeDetached, roots)
			trees[limited].work.hashLimit = limit
			err := op()
			trees[limited].work.hashLimit = 0
			want := wantSkip
			switch {
			case err == nil:
				// The operation fits: a reservation above its use, or (the parser) work split
				// over several hash-work scopes whose sum is need.
				want = wantDone
			case !errors.Is(err, yang.ErrBudget):
				t.Fatalf("limit %d/%d: %v, want ErrBudget", limit, need, err)
			default:
				for i, tr := range trees {
					if !reflect.DeepEqual(snapshotHashBudgetTree(tr), before[i]) {
						t.Fatalf("limit %d/%d: refusal changed tree %d", limit, need, i)
					}
				}
				afterDetached := hashBudgetSnapshot{index: map[*Node]string{}, sibs: map[*siblings]string{}}
				snapshotHashBudgetRoots(&afterDetached, roots)
				if !reflect.DeepEqual(afterDetached, beforeDetached) {
					t.Fatalf("limit %d/%d: refusal changed a detached source", limit, need)
				}
				s4Check(t, fmt.Sprintf("limit %d: after refusal", limit), trees)
				if retry {
					if err := op(); err != nil {
						t.Fatalf("limit %d: retry: %v", limit, err)
					}
					want = wantDone
				}
			}
			if err := further(); err != nil {
				t.Fatalf("limit %d retry=%t: further: %v", limit, retry, err)
			}
			s4Check(t, fmt.Sprintf("limit %d retry=%t: after further", limit, retry), trees)
			if got := observe(trees); !reflect.DeepEqual(got, want) {
				t.Fatalf("limit %d/%d retry=%t: history differs from the reference:\n%v\nwant\n%v",
					limit, need, retry, got, want)
			}
		}
	}
}

func TestS4JournalRefusalHistories(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	keys := []string{"3", "7", "11", "15", "19"}
	// furtherData inserts into and removes from every sibling set the operations touch.
	furtherData := func(trees ...*Tree) func() error {
		return func() error {
			for _, tr := range trees {
				c := tr.top.first()
				if _, err := tr.NewList(c, "l", []string{"8"}, NewOptions{}); err != nil {
					return err
				}
				if _, err := tr.NewTerm(c, "u", "r", NewOptions{}); err != nil {
					return err
				}
				if _, err := tr.NewTerm(c, "v", "d", NewOptions{}); err != nil {
					return err
				}
				if n := s4Child(c, "l", "11"); n != nil {
					if err := n.Remove(); err != nil {
						return err
					}
				}
				if n := s4Child(c, "u", "c"); n != nil {
					if err := n.Remove(); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	furtherOp := func(pairs ...any) func() error {
		return func() error {
			for i := 0; i < len(pairs); i += 2 {
				tr, op := pairs[i].(*Tree), pairs[i+1].(*Node)
				if _, err := tr.NewTerm(op, "w", "7", NewOptions{}); err != nil {
					return err
				}
				if _, err := tr.NewList(op, "e", []string{"5"}, NewOptions{}); err != nil {
					return err
				}
				if n := s4Child(op, "w", "1"); n != nil {
					if err := n.Remove(); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	var lastDetached *Node
	scenarios := []s4Scenario{
		{name: "data/insert-list", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			tr := s4DataTree(t, set, keys...)
			return []*Tree{tr}, func() error {
				_, err := tr.NewList(tr.top.first(), "l", []string{"9"}, NewOptions{})
				return err
			}, furtherData(tr)
		}},
		{name: "data/insert-userord", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			tr := s4DataTree(t, set, keys...)
			return []*Tree{tr}, func() error {
				_, err := tr.NewTerm(tr.top.first(), "u", "a", NewOptions{})
				return err
			}, furtherData(tr)
		}},
		{name: "data/remove", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			tr := s4DataTree(t, set, keys...)
			n := s4Child(tr.top.first(), "l", "7")
			return []*Tree{tr}, n.Remove, furtherData(tr)
		}},
		{name: "data/move-list", limited: []int{0, 1}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			dst, src := s4DataTree(t, set, keys...), s4DataTree(t, set, "1", "5", "9", "13", "17")
			n := s4Child(src.top.first(), "l", "9")
			return []*Tree{dst, src}, func() error { return dst.Insert(dst.top.first(), n) }, furtherData(dst, src)
		}},
		{name: "data/move-userord", limited: []int{0, 1}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			dst, src := s4DataTree(t, set, keys...), s4DataTree(t, set, "1", "5", "9", "13", "17")
			// src's "u" values equal dst's: move one with a fresh value
			n, err := src.NewTerm(src.top.first(), "u", "q", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return []*Tree{dst, src}, func() error { return dst.Insert(dst.top.first(), n) }, furtherData(dst, src)
		}},
		{name: "data/move-detached", limited: []int{0}, detached: func() []*Node { return []*Node{lastDetached} }, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			dst, src := s4DataTree(t, set, keys...), s4DataTree(t, set, "1", "5", "9", "13", "17")
			n := s4Child(src.top.first(), "l", "13")
			lastDetached = n
			if err := n.Unlink(); err != nil {
				t.Fatal(err)
			}
			return []*Tree{dst, src}, func() error { return dst.Insert(dst.top.first(), n) }, furtherData(dst, src)
		}},
		{name: "data/dup", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			dst, src := s4DataTree(t, set, keys...), s4DataTree(t, set, "1", "5", "9", "13", "17")
			return []*Tree{dst, src}, func() error {
				_, _, err := dst.Dup(s4Child(src.top.first(), "l", "5"), dst.top.first(), DupOptions{Recursive: true})
				return err
			}, furtherData(dst, src)
		}},
		{name: "data/merge", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			dst, src := s4DataTree(t, set, keys...), s4DataTree(t, set, "1", "5", "9", "13", "17")
			return []*Tree{dst, src}, func() error { return dst.Merge(src) }, furtherData(dst, src)
		}},
		{name: "op/insert-term", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			tr, op := s4OpTree(t, set, "5", "1", "9")
			return []*Tree{tr}, func() error {
				_, err := tr.NewTerm(op, "w", "4", NewOptions{})
				return err
			}, furtherOp(tr, op)
		}},
		{name: "op/insert-list", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			tr, op := s4OpTree(t, set, "5", "1", "9")
			return []*Tree{tr}, func() error {
				_, err := tr.NewList(op, "e", []string{"2"}, NewOptions{})
				return err
			}, furtherOp(tr, op)
		}},
		{name: "op/insert-opaque", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			tr, op := s4OpTree(t, set, "5", "1", "9")
			return []*Tree{tr}, func() error {
				_, err := tr.NewOpaque(op, "x", "y", OpaqueOptions{Module: "s4j"})
				return err
			}, furtherOp(tr, op)
		}},
		{name: "op/remove", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			tr, op := s4OpTree(t, set, "5", "1", "9")
			return []*Tree{tr}, s4Child(op, "w", "9").Remove, furtherOp(tr, op)
		}},
		// Multi-node operations: refusals after a storage or sequence edit, restored by the journal.
		{name: "data/apply-diff", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			tr := s4DataTree(t, set, keys...)
			after := s4DataTree(t, set, "3", "9", "15", "19", "23")
			diff, err := Diff(s4DataTree(t, set, keys...), after, DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return []*Tree{tr}, func() error { return tr.ApplyDiff(diff, ApplyDiffOptions{}) }, furtherData(tr)
		}},
		{name: "data/trim", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			tr := s4DataTree(t, set, keys...)
			return []*Tree{tr}, func() error {
				_, err := tr.TrimXPath("/s4j:c/l[k > 5] | /s4j:c/u[. = 'x']", XPathOptions{})
				return err
			}, furtherData(tr)
		}},
		{name: "op/merge", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			dst, dop := s4OpTree(t, set, "5", "1", "9")
			src, _ := s4OpTree(t, set, "6", "2", "8")
			return []*Tree{dst, src}, func() error { return dst.Merge(src) }, furtherOp(dst, dop)
		}},
		{name: "op/insert-forest", limited: []int{0, 1}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			dst, dop := s4OpTree(t, set, "5", "1", "9")
			_, parsed := s4OpTree(t, set, "6", "2", "8")
			src := newTree(set)
			first, _, err := src.DupSiblings(parsed.kids.At(0), nil, DupOptions{Recursive: true})
			if err != nil {
				t.Fatal(err)
			}
			if src.top.order == nil || src.top.Len() < 4 {
				t.Fatal("setup: copied operation children have no top-level sequence index")
			}
			return []*Tree{dst, src}, func() error { return dst.Insert(dop, first) }, furtherOp(dst, dop)
		}},
		{name: "op/parse-reply", limited: []int{0}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			tr, op := s4OpTree(t, set, "5", "1", "9")
			return []*Tree{tr}, func() error {
				_, _, ds, err := parseOp(context.Background(), strings.NewReader(
					`{"s4j:o":[4,2,8],"s4j:f":[{"k":7},{"k":3}]}`), set, FormatJSON, opReply, op, Reject)
				if err != nil && !errors.Is(err, yang.ErrBudget) {
					t.Fatal(err, ds)
				}
				return err
			}, furtherOp(tr, op)
		}},
		{name: "op/move", limited: []int{0, 1}, build: func(t *testing.T) ([]*Tree, func() error, func() error) {
			dst, dop := s4OpTree(t, set, "5", "1", "9")
			src, sop := s4OpTree(t, set, "6", "2", "8")
			n := s4Child(sop, "w", "2")
			return []*Tree{dst, src}, func() error { return dst.Insert(dop, n) }, furtherOp(dst, dop, src, sop)
		}},
	}
	for _, sc := range scenarios {
		for _, limited := range sc.limited {
			t.Run(fmt.Sprintf("%s/limited=%d", sc.name, limited), func(t *testing.T) {
				s4History(t, sc, limited)
			})
		}
	}
}
