// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
)

// #240 step 4 (design 13 §2.4): every public entry that takes a context holds its trees from
// before the first poll until after the last. The sweep counts the polls each entry makes on a
// small fixture (N), then for every k = 1..N runs it again with a context whose Err attempts a
// set of mutations of the held tree at poll k. Each mutation must be refused in the public form
// (*ValidationError, LY_EINVAL, the diagnostic), nothing may panic, and the entry's result must
// equal the benign run's (reciprocal leafref records consistent).

// pollCtx counts its Err calls and runs at at poll k.
type pollCtx struct {
	context.Context
	polls, k int
	at       func()
}

func (c *pollCtx) Err() error {
	c.polls++
	if c.polls == c.k && c.at != nil {
		c.at()
	}
	return nil
}

// s4ctxFixture is one public ctx entry on fresh trees: held is the tree it must hold, term a
// term node in it, and run the call, its observable result returned as text.
type s4ctxFixture struct {
	held        *Tree
	term        *Node
	name, value string // a term NewTerm can create under held's first root
	run         func(ctx context.Context) string
}

// s4LinksConsistent fails if a leafref record of tr is not reciprocated.
func s4LinksConsistent(t *testing.T, where string, tr *Tree) {
	t.Helper()
	for root := range tr.Top() {
		for n := range root.allAny() {
			if n.links == nil {
				continue
			}
			for _, l := range n.links.leafrefs {
				if l.links == nil || !containsNode(l.links.targets, n) {
					t.Fatalf("%s: a leafref record is not reciprocated", where)
				}
			}
			for _, x := range n.links.targets {
				if x.links == nil || !containsNode(x.links.leafrefs, n) {
					t.Fatalf("%s: a target record is not reciprocated", where)
				}
			}
		}
	}
}

func containsNode(ns []*Node, n *Node) bool {
	for _, x := range ns {
		if x == n {
			return true
		}
	}
	return false
}

func TestS4CtxEntriesHoldTheirTrees(t *testing.T) {
	lset := s4LinksSet(t)
	jset := reviewDiffSet(t, s4JournalModule)
	oset, oparent := s4OpsFixture(t)
	linked := func() *Tree {
		tr := s4ParseLinks(t, lset, `{"pv2-links:c":{"l":[{"k":"a"},{"k":"b"}],"t":["y"],"r":"a"}}`)
		if _, err := tr.LinkLeafrefs(context.Background()); err != nil {
			t.Fatal(err)
		}
		return tr
	}
	result := func(tr *Tree, diags any, err error) string {
		return fmt.Sprintf("%v|%v|%v", typedDump(tr), diags, err)
	}
	validate := func(f func(tr *Tree, ctx context.Context) (any, error)) func() s4ctxFixture {
		return func() s4ctxFixture {
			tr := linked()
			return s4ctxFixture{held: tr, term: s4Child(tr.top.first(), "r", "a"), name: "t", value: "n",
				run: func(ctx context.Context) string { d, err := f(tr, ctx); return result(tr, d, err) }}
		}
	}
	fixtures := map[string]func() s4ctxFixture{
		"Validate": validate(func(tr *Tree, ctx context.Context) (any, error) { return tr.Validate(ctx, ValidateOptions{}) }),
		"ValidateModule": validate(func(tr *Tree, ctx context.Context) (any, error) {
			return tr.ValidateModule(ctx, "pv2-links", ValidateOptions{})
		}),
		"ValidateModuleFinal": validate(func(tr *Tree, ctx context.Context) (any, error) {
			return tr.ValidateModuleFinal(ctx, "pv2-links", ValidateOptions{})
		}),
		"ValidateDiff": validate(func(tr *Tree, ctx context.Context) (any, error) {
			diff, d, err := tr.ValidateDiff(ctx, ValidateOptions{})
			if diff != nil {
				return fmt.Sprint(d, typedDump(diff)), err
			}
			return d, err
		}),
		"ValidateModuleDiff": validate(func(tr *Tree, ctx context.Context) (any, error) {
			diff, d, err := tr.ValidateModuleDiff(ctx, "pv2-links", ValidateOptions{})
			if diff != nil {
				return fmt.Sprint(d, typedDump(diff)), err
			}
			return d, err
		}),
		"LinkLeafrefs": validate(func(tr *Tree, ctx context.Context) (any, error) { return tr.LinkLeafrefs(ctx) }),
		"ValidateOp": func() s4ctxFixture {
			req, op := s4OpTree(t, jset, "5", "1", "9")
			oper := s4DataTree(t, jset, "3", "7")
			return s4ctxFixture{held: req, term: s4Child(op, "w", "5"), name: "w", value: "6",
				run: func(ctx context.Context) string {
					d, err := req.ValidateOp(ctx, OpRPC, ValidateOpOptions{Operational: oper})
					return result(req, d, err) + typedDump(oper)[0]
				}}
		},
		"ParseOp": func() s4ctxFixture {
			tree, op := oparent()
			return s4ctxFixture{held: tree, term: op.kids.first(), name: "al", value: "7",
				run: func(ctx context.Context) string {
					res, d, err := ParseOp(ctx, strings.NewReader(`{}`), FormatJSON, snap.New(oset), OpReply, ParseOpOptions{Request: op})
					return result(tree, d, err) + fmt.Sprint(res.Op != nil)
				}}
		},
	}
	total := 0
	for name, build := range fixtures {
		t.Run(name, func(t *testing.T) {
			benign := &pollCtx{Context: context.Background()}
			f := build()
			want := f.run(benign)
			s4LinksConsistent(t, "benign", f.held)
			if benign.polls == 0 {
				t.Fatalf("%s polls no context on the fixture", name)
			}
			for k := 1; k <= benign.polls; k++ {
				total++
				f := build()
				root := f.held.top.first()
				scratch := newTerm(f.term.schema, f.term.value)
				other := newTree(f.held.set)
				mutations := map[string]func() error{
					"SetValue":      func() error { _, err := f.term.SetValue("zz"); return err },
					"RemoveRoot":    func() error { return root.Remove() },
					"UnlinkRoot":    func() error { return root.Unlink() },
					"NewTermUnder":  func() error { _, err := f.held.NewTerm(root, f.name, f.value, NewOptions{}); return err },
					"InsertUnder":   func() error { return f.held.Insert(root, scratch) },
					"MergeIntoHeld": func() error { return f.held.Merge(other) },
					"NewPathInHeld": func() error { _, err := f.held.NewPath(root.Path(), "", NewPathOptions{}); return err },
				}
				ctx := &pollCtx{Context: context.Background(), k: k, at: func() {
					for m, mutate := range mutations {
						s4PublicRefusal(t, fmt.Sprintf("%s poll %d %s", name, k, m), nil, mutate(), false)
					}
				}}
				got := f.run(ctx)
				if ctx.polls < k {
					t.Fatalf("poll %d: the entry polled only %d times", k, ctx.polls)
				}
				if got != want {
					t.Fatalf("poll %d: the result differs from the benign run:\n%s\nwant\n%s", k, got, want)
				}
				s4LinksConsistent(t, fmt.Sprintf("poll %d", k), f.held)
			}
		})
	}
	t.Logf("%d entries, %d mutated polls", len(fixtures), total)
}

// TestS4DetachedNodeNotHeldByItsOldTree: a node unlinked before an operation keeps its old
// counter only as accounting: user code of a LinkLeafrefs or ApplyDiff on that tree may still
// edit it (snapshot review 4, finding 1).
func TestS4DetachedNodeNotHeldByItsOldTree(t *testing.T) {
	set := s4LinksSet(t)
	tr := s4ParseLinks(t, set, `{"pv2-links:c":{"l":[{"k":"a"}],"t":["y","x"],"r":"a"}}`)
	detached := s4Child(tr.top.first(), "t", "y")
	if err := detached.Unlink(); err != nil {
		t.Fatal(err)
	}
	var editErr error
	ctx := &pollCtx{Context: context.Background(), k: 1, at: func() { _, editErr = detached.SetValue("z") }}
	if _, err := tr.LinkLeafrefs(ctx); err != nil {
		t.Fatal(err)
	}
	if editErr != nil || detached.Value() != "z" {
		t.Fatalf("editing a node detached earlier: %v (value %q), want success", editErr, detached.Value())
	}
	other := s4ParseLinks(t, set, `{"pv2-links:c":{"t":["q"]}}`)
	err := other.ApplyDiff(s4Diff(t, set, other, false), ApplyDiffOptions{Callback: func(_, _ *Node) error {
		_, err := detached.SetValue("w")
		return err
	}})
	if err != nil {
		t.Fatalf("ApplyDiff whose callback edits an unrelated detached node: %v", err)
	}
	// inside ApplyDiff on its former tree, whose journal its retained binding still names
	err = tr.ApplyDiff(s4Diff(t, set, tr, false), ApplyDiffOptions{Callback: func(_, _ *Node) error {
		_, err := detached.SetValue("v")
		return err
	}})
	if err != nil || detached.Value() != "v" {
		t.Fatalf("ApplyDiff on its former tree whose callback edits the detached node: %v (value %q)", err, detached.Value())
	}
}

// TestS4HeldCheckIsConstantOnTheCommonPath: touching every node of a journaled deep chain,
// whose state waits on nothing, makes no guard root walk (snapshot review 4, finding 2).
func TestS4HeldCheckIsConstantOnTheCommonPath(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	tr, op := s4OpTree(t, set, "5", "1", "9")
	var nodes []*Node
	for n := range op.allAny() {
		nodes = append(nodes, n)
	}
	j := beginHashTransaction(tr)
	walks := 0
	heldWalkHook = func() { walks++ }
	defer func() { heldWalkHook = nil }()
	for _, n := range nodes {
		if err := n.touch(); err != nil {
			t.Fatal(err)
		}
		if err := childStorage(n).touch(); err != nil {
			t.Fatal(err)
		}
	}
	j.end(nil)
	if walks != 0 {
		t.Fatalf("%d guard root walks on the common path", walks)
	}
}

// TestS4SharedDiffSourceIsNotHeld: the diff given to ApplyDiff and the source of MergeDiff
// are read-only arguments (doc.go), not held: concurrent applies and merges of one diff into
// separate trees, with callbacks, do not race (run under -race).
func TestS4SharedDiffSourceIsNotHeld(t *testing.T) {
	set := s4LinksSet(t)
	diff := s4Diff(t, set, s4ParseLinks(t, set, `{"pv2-links:c":{"t":["y"]}}`), false)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		dst := s4ParseLinks(t, set, `{"pv2-links:c":{"t":["y"]}}`)
		if i%2 != 0 { // MergeDiff merges into a diff
			d, err := Diff(dst, s4ParseLinks(t, set, `{"pv2-links:c":{"t":["y","w"]}}`), DiffOptions{})
			if err != nil {
				t.Fatal(err)
			}
			dst = d
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				errs[i] = dst.ApplyDiff(diff, ApplyDiffOptions{Callback: func(_, _ *Node) error { return nil }})
				return
			}
			errs[i] = dst.MergeDiff(diff, MergeDiffOptions{Callback: func(_, _ *Node) error { return nil }})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
}

const s4DetachedModule = `module s4d {yang-version 1.1;namespace "urn:s4d";prefix d;
 import ietf-yang-metadata {prefix md;}
 md:annotation ann {type string;}
 container c {
  container e {presence "p";leaf a {type string;}container i {presence "p";}list m {key k;leaf k {type string;}}}
  leaf t {type string;}
 }}`

// TestS4EmptyDetachedInnerNotHeld: an empty inner node unlinked before an operation keeps its
// old counter only as accounting: a callback of ApplyDiff on its former tree may build under it,
// insert next to it and annotate it, through another tree of the snapshot.
func TestS4EmptyDetachedInnerNotHeld(t *testing.T) {
	set := reviewDiffSet(t, s4DetachedModule)
	edits := map[string]func(free *Tree, e *Node) error{
		"NewTerm":  func(free *Tree, e *Node) error { _, err := free.NewTerm(e, "a", "1", NewOptions{}); return err },
		"NewInner": func(free *Tree, e *Node) error { _, err := free.NewInner(e, "i", NewOptions{}); return err },
		"NewList": func(free *Tree, e *Node) error {
			_, err := free.NewList(e, "m", []string{"x"}, NewOptions{})
			return err
		},
		"Insert": func(free *Tree, e *Node) error {
			c, err := free.NewInner(nil, "s4d:c", NewOptions{})
			if err != nil {
				return err
			}
			e2, err := free.NewInner(c, "e", NewOptions{})
			if err != nil {
				return err
			}
			a, err := free.NewTerm(e2, "a", "2", NewOptions{})
			if err != nil {
				return err
			}
			return free.Insert(e, a)
		},
		"Dup": func(_ *Tree, e *Node) error {
			src := parseOnlyXML(t, set, `<c xmlns="urn:s4d"><e><a>3</a></e></c>`)
			a := childStorage(childStorage(src.top.first()).At(0)).At(0)
			_, _, err := src.Dup(a, e, DupOptions{})
			return err
		},
		"Insert itself": func(free *Tree, e *Node) error { return free.Insert(free.top.first(), e) }, // next to free's t
		"NewMeta":       func(free *Tree, e *Node) error { _, err := free.NewMeta(e, "s4d:ann", "v"); return err },
	}
	for name, edit := range edits {
		tr := parseOnlyXML(t, set, `<c xmlns="urn:s4d"><e/><t>1</t></c>`)
		var e *Node
		for n := range tr.top.first().allAny() {
			if n.schema != nil && n.schema.Name == "e" {
				e = n
			}
		}
		if e == nil || childStorage(e).len() != 0 {
			t.Fatal("setup: no empty e")
		}
		if err := e.Unlink(); err != nil {
			t.Fatal(err)
		}
		free := parseOnlyXML(t, set, `<c xmlns="urn:s4d"><t>9</t></c>`)
		diff, err := Diff(tr, parseOnlyXML(t, set, `<c xmlns="urn:s4d"><t>2</t></c>`), DiffOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var editErr error
		called := false
		err = tr.ApplyDiff(diff, ApplyDiffOptions{Callback: func(_, _ *Node) error {
			if !called {
				called = true
				editErr = edit(free, e)
			}
			return nil
		}})
		if err != nil || !called {
			t.Fatalf("%s: ApplyDiff %v (called %t)", name, err, called)
		}
		if editErr != nil {
			t.Errorf("%s under a node detached earlier, from a callback on its former tree: %v", name, editErr)
			continue
		}
		if childStorage(e).len() == 0 && e.meta == nil && e.parent == nil {
			t.Errorf("%s: the edit did not survive the operation", name)
		}
	}
}

// TestS4CapturedLinkOwnerIsHeld: with links v→r→k, k's list moved into A and the subtree
// holding r and v detached, ApplyDiff on A deleting the list captures r's link record (an
// owner capture, not a node one). A callback editing r is refused; the callback's budget error
// then rolls A back, and r's value and both link records are as before (astra round 7).
func TestS4CapturedLinkOwnerIsHeld(t *testing.T) {
	set := s4LinksSet(t)
	b := s4ParseLinks(t, set, `{"pv2-links:c":{"l":[{"k":"a"}],"r":"a","v":"a"}}`)
	if _, err := b.LinkLeafrefs(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := s4ParseLinks(t, set, `{"pv2-links:c":{"t":["y"]}}`)
	bc := b.top.first()
	if err := a.Insert(a.top.first(), s4Child(bc, "l", "a")); err != nil {
		t.Fatal(err)
	}
	r, v := s4Child(bc, "r", "a"), s4Child(bc, "v", "a")
	if err := bc.Unlink(); err != nil {
		t.Fatal(err)
	}
	links := func() string {
		rl, rt := r.LeafrefLinks()
		vl, vt := v.LeafrefLinks()
		return fmt.Sprint(r.Value(), len(rl), len(rt), len(vl), len(vt))
	}
	before := links()
	free := s4ParseLinks(t, set, `{}`)
	var editErr, moveErr error
	called := false
	err := a.ApplyDiff(s4Diff(t, set, a, true), ApplyDiffOptions{Callback: func(_, _ *Node) error {
		if s4Child(a.top.first(), "l", "a") != nil {
			return nil // the list's delete has not run yet
		}
		called = true
		moveErr = free.Insert(nil, bc) // rebinding the ancestors must not lift the hold
		_, editErr = r.SetValue("b")
		return yang.ErrBudget
	}})
	if !called || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("ApplyDiff: %v (called %t)", err, called)
	}
	s4PublicRefusal(t, "moving the detached root of a captured link owner", nil, moveErr, false)
	s4PublicRefusal(t, "SetValue of a captured link owner", nil, editErr, false)
	if bc.treeOf() != nil {
		t.Fatal("the refused move left the detached root in a tree")
	}
	if got := links(); got != before {
		t.Fatalf("after rollback: %s, want %s", got, before)
	}
}

// TestS4CapturedDetachedNodeIsHeld: for each capture map of the journal, a node of a subtree
// detached before the operation, captured that way by the waiting state, is held: an edit of
// it from user code is refused. Without a capture it is a stale binding and the edit proceeds.
func TestS4CapturedDetachedNodeIsHeld(t *testing.T) {
	keys := make([]string, 0, 12)
	for i := range 12 {
		keys = append(keys, fmt.Sprint(i+1))
	}
	j4 := reviewDiffSet(t, s4JournalModule)
	links := s4LinksSet(t)
	type fixture struct{ root, entry, leaf *Node }
	hashed := func() fixture { // a detached c whose list is hashed: entry l[k=1], leaf its p
		tr := s4DataTree(t, j4, keys...)
		c := tr.top.first()
		if err := c.Unlink(); err != nil {
			t.Fatal(err)
		}
		e := s4Child(c, "l", "1")
		return fixture{c, e, s4Child(e, "p", "1")}
	}
	linked := func() fixture { // a detached c whose r has a link record
		tr := s4ParseLinks(t, links, `{"pv2-links:c":{"l":[{"k":"a"}],"r":"a"}}`)
		if _, err := tr.LinkLeafrefs(context.Background()); err != nil {
			t.Fatal(err)
		}
		c := tr.top.first()
		if err := c.Unlink(); err != nil {
			t.Fatal(err)
		}
		return fixture{c, s4Child(c, "l", "a"), s4Child(c, "r", "a")}
	}
	setLeaf := func(f fixture) error { _, err := f.leaf.SetValue("2"); return err }
	unlinkLeaf := func(f fixture) error { return f.leaf.Unlink() }
	unlinkEntry := func(f fixture) error { return f.entry.Unlink() }
	order := func(f fixture) *dirtyOrder {
		return &dirtyOrder{nodes: map[*Node]*dirtyOrderNode{f.entry: {node: f.entry}}}
	}
	rows := []struct {
		name    string
		fix     func() fixture
		capture func(j *hashTransaction, f fixture) error
		edit    func(f fixture) error
		held    bool
	}{
		{"none (stale binding)", hashed, func(*hashTransaction, fixture) error { return nil }, setLeaf, false},
		{"nodes", hashed, func(j *hashTransaction, f fixture) error { return j.captureNode(f.leaf) }, setLeaf, true},
		{"owners", hashed, func(j *hashTransaction, f fixture) error { return j.captureOwner(f.leaf) }, setLeaf, true},
		{"links", linked, func(j *hashTransaction, f fixture) error { return j.captureLinks(f.leaf.links) }, setLeaf, true},
		{"detached", hashed, func(j *hashTransaction, f fixture) error {
			if j.detached == nil {
				j.detached = map[*Node]uint32{}
			}
			addWhole(j, j.detached, &j.img.detached, wholeDetached, f.root, struct{}{})
			return nil
		}, setLeaf, true},
		{"siblings (its set)", hashed, func(j *hashTransaction, f fixture) error { return j.captureSiblings(&f.entry.kids) }, unlinkLeaf, true},
		{"works (its set)", hashed, func(j *hashTransaction, f fixture) error {
			addWhole(j, j.works, &j.img.works, wholeWorks, &f.entry.kids, f.entry.kids.work)
			return nil
		}, unlinkLeaf, true},
		{"records", hashed, func(j *hashTransaction, f fixture) error {
			return j.captureRecords(f.root.kids.exactByNode[f.entry])
		}, unlinkEntry, true},
		{"chains", hashed, func(j *hashTransaction, f fixture) error {
			return j.captureChain(f.root.kids.exactByNode[f.entry].chain)
		}, unlinkEntry, true},
		{"pending, dirty (its set)", hashed, func(j *hashTransaction, f fixture) error {
			d := order(f)
			f.root.kids.pending = map[*schema.Node]*dirtyOrder{f.entry.schema: d}
			return j.captureOrder(d)
		}, unlinkEntry, true},
		{"pending (detachedRB)", hashed, func(j *hashTransaction, f fixture) error {
			f.entry.detachedRB = order(f)
			return j.captureOrder(f.entry.detachedRB)
		}, unlinkEntry, true},
	}
	for _, row := range rows {
		f := row.fix()
		if row.name == "records" || row.name == "chains" {
			if f.root.kids.exactByNode[f.entry] == nil {
				t.Fatalf("%s: setup: the list is not hashed", row.name)
			}
		}
		a := s4ParseLinks(t, links, `{"pv2-links:c":{"t":["y"]}}`)
		j := beginHashTransaction(a)
		j.maps()
		if err := j.enroll(&f.root.kids); err != nil { // the detached tree's counter joins the state
			t.Fatal(err)
		}
		if err := row.capture(j, f); err != nil {
			t.Fatalf("%s: capture: %v", row.name, err)
		}
		resume := a.work.suspend()
		err := row.edit(f)
		resume()
		f.root.kids.pending, f.entry.detachedRB = nil, nil
		j.end(nil)
		if row.held {
			if !isGuardRefusal(err) {
				t.Errorf("%s: edit of a captured detached node: %v, want the guard's refusal", row.name, err)
			}
		} else if err != nil {
			t.Errorf("%s: %v, want success", row.name, err)
		}
	}
}
