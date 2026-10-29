// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

const s4NestModule = `module s4n {yang-version 1.1;namespace "urn:s4n";prefix n;
 container c {leaf x {type string;}leaf y {type string;}leaf z {type string;}}
 rpc r {input {leaf i {type string;}leaf j {type string;}} output {leaf o {type string;}}}}`

// s4Side is one operation of the nesting matrix on a fresh fixture: run calls user from the
// operation's user code (once), held is a node the operation holds (its own object), stale a
// node detached from that object before it, and snap the object's state.
type s4Side struct {
	held, stale *Node
	snap        func() string
	run         func(user func() error) error
}

func s4Once(user func() error) func() error {
	done := false
	return func() error {
		if done {
			return nil
		}
		done = true
		return user()
	}
}

// s4UserReader runs user before its first Read; its error is the Read's.
type s4UserReader struct {
	user func() error
	in   io.Reader
}

func (r *s4UserReader) Read(p []byte) (int, error) {
	if err := r.user(); err != nil {
		return 0, err
	}
	return r.in.Read(p)
}

// s4UserCtx runs user at its first poll; its error stays the context's.
type s4UserCtx struct {
	context.Context
	user func() error
	err  error
}

func (c *s4UserCtx) Err() error {
	if err := c.user(); err != nil {
		c.err = err
	}
	return c.err
}

func s4NestSides(t *testing.T, set *schema.Set) map[string]func() s4Side {
	data := func(in string) *Tree { return s4ParseLinks(t, set, in) }
	named := func(parent *Node, name string) *Node {
		for n := range parent.kids.all() {
			if n.schema != nil && n.schema.Name == name {
				return n
			}
		}
		t.Fatalf("setup: no %s", name)
		return nil
	}
	unlink := func(n *Node) *Node {
		if err := n.Unlink(); err != nil {
			t.Fatal(err)
		}
		return n
	}
	diff := func(a, b *Tree) *Tree {
		d, err := Diff(a, b, DiffOptions{})
		if err != nil || d == nil {
			t.Fatal(d, err)
		}
		return d
	}
	dump := func(tr *Tree) func() string { return func() string { return fmt.Sprint(typedDump(tr)) } }
	return map[string]func() s4Side{
		"ApplyDiff": func() s4Side {
			o := data(`{"s4n:c":{"x":"1","y":"1"}}`)
			d := diff(o, data(`{"s4n:c":{"x":"2","y":"1"}}`))
			c := o.top.first()
			stale := unlink(named(c, "y"))
			return s4Side{named(c, "x"), stale, dump(o), func(user func() error) error {
				f := s4Once(user)
				return o.ApplyDiff(d, ApplyDiffOptions{Callback: func(_, _ *Node) error { return f() }})
			}}
		},
		"MergeDiff": func() s4Side {
			o := diff(data(`{"s4n:c":{"x":"1","y":"1"}}`), data(`{"s4n:c":{"x":"2","y":"2"}}`))
			src := diff(data(`{"s4n:c":{"x":"2"}}`), data(`{"s4n:c":{"x":"2","z":"1"}}`))
			c := o.top.first()
			stale := unlink(named(c, "y"))
			return s4Side{named(c, "x"), stale, dump(o), func(user func() error) error {
				f := s4Once(user)
				return o.MergeDiff(src, MergeDiffOptions{Callback: func(_, _ *Node) error { return f() }})
			}}
		},
		"ParseOp+Reader": func() s4Side {
			_, req, diags, err := parseOp(context.Background(), strings.NewReader(`{"s4n:r":{"i":"1","j":"1"}}`), set,
				FormatJSON, opRPC, nil, Reject)
			if err != nil {
				t.Fatal(err, diags)
			}
			unlink(req) // a request detached from its tree
			stale := unlink(named(req, "j"))
			snap := func() string {
				var b strings.Builder
				for n := range req.allAny() {
					fmt.Fprintf(&b, "%s=%s;", n.schema.Name, n.Value())
				}
				return b.String()
			}
			return s4Side{named(req, "i"), stale, snap, func(user func() error) error {
				r := &s4UserReader{user: s4Once(user), in: strings.NewReader(`{}`)}
				_, _, _, err := parseOp(context.Background(), r, set, FormatJSON, opReply, req, Reject)
				return err
			}}
		},
		"Validate+ctx": func() s4Side {
			o := data(`{"s4n:c":{"x":"1","y":"1"}}`)
			c := o.top.first()
			stale := unlink(named(c, "y"))
			return s4Side{named(c, "x"), stale, dump(o), func(user func() error) error {
				_, err := o.Validate(&s4UserCtx{Context: context.Background(), user: s4Once(user)}, ValidateOptions{})
				return err
			}}
		},
	}
}

// TestS4NestedOperationsHoldTheirOwn: an operation started from the user code of another
// (outer: ApplyDiff, MergeDiff, ParseOp with a Reader, Validate with a context; inner: ParseOp
// with a Reader, ApplyDiff, Validate with a context) holds its own objects, and the outer one
// still holds its own: from the inner user code, an edit of (a) the inner operation's object or
// (b) the outer one's is refused, and (c) of a node detached from either before it began
// succeeds. The inner operation fails on the budget and restores exactly; the outer one then
// either succeeds with the result of a run without user code, or fails and restores exactly.
func TestS4NestedOperationsHoldTheirOwn(t *testing.T) {
	set := reviewDiffSet(t, s4NestModule)
	sides := s4NestSides(t, set)
	cases := 0
	for _, outerName := range []string{"ApplyDiff", "MergeDiff", "ParseOp+Reader", "Validate+ctx"} {
		for _, innerName := range []string{"ParseOp+Reader", "ApplyDiff", "Validate+ctx"} {
			for _, outerFails := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/outer fails %t", outerName, innerName, outerFails)
				benign := sides[outerName]()
				if err := benign.run(func() error { return nil }); err != nil {
					t.Fatalf("%s: benign run: %v", name, err)
				}
				out := sides[outerName]()
				want := benign.snap()
				if outerFails {
					want = out.snap()
				}
				var edits [4]error
				var innerErr error
				innerRan, innerExact := false, false
				outerErr := out.run(func() error {
					in := sides[innerName]()
					before := in.snap()
					innerRan = true
					innerErr = in.run(func() error {
						_, edits[0] = in.held.SetValue("9")
						_, edits[1] = out.held.SetValue("9")
						_, edits[2] = out.stale.SetValue("9")
						_, edits[3] = in.stale.SetValue("9")
						return yang.ErrBudget
					})
					innerExact = in.snap() == before && in.stale.Value() == "9"
					if outerFails {
						return yang.ErrBudget
					}
					return nil
				})
				cases++
				switch {
				case !innerRan:
					t.Errorf("%s: the outer user code never ran", name)
				case innerErr == nil || !innerExact:
					t.Errorf("%s: inner operation %v, exact restore %t", name, innerErr, innerExact)
				case !s4Refused(edits[0]) || !s4Refused(edits[1]):
					t.Errorf("%s: edits of held objects (a) %v, (b) %v, want refusals", name, edits[0], edits[1])
				case edits[2] != nil || edits[3] != nil || out.stale.Value() != "9":
					t.Errorf("%s: edits of nodes detached earlier: outer's %v, inner's %v", name, edits[2], edits[3])
				case (outerErr != nil) != outerFails:
					t.Errorf("%s: outer operation %v", name, outerErr)
				case out.snap() != want:
					t.Errorf("%s: outer object\n%s\nwant\n%s", name, out.snap(), want)
				}
			}
		}
	}
	t.Logf("%d nested cases", cases)
}

// TestS4NestedParseOpOnStaleRequest: a request detached from tree A keeps a binding to A's
// counter. From the callback of an ApplyDiff on A, a ParseOp with that request as its parent
// holds it: the binding is stale (A's waiting state did not capture it), so ParseOp enrolls the
// request in its own state. An edit of a request leaf from the Reader is refused, and the
// Reader's budget error restores the request exactly (astra round 8).
func TestS4NestedParseOpOnStaleRequest(t *testing.T) {
	set := reviewDiffSet(t, s4NestModule)
	a, req, diags, err := parseOp(context.Background(), strings.NewReader(`{"s4n:r":{"i":"1","j":"1"}}`), set,
		FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	if err := req.Unlink(); err != nil {
		t.Fatal(err)
	}
	var leaf *Node
	for n := range req.kids.all() {
		if n.schema.Name == "i" {
			leaf = n
		}
	}
	d, err := Diff(a, s4ParseLinks(t, set, `{"s4n:c":{"x":"1"}}`), DiffOptions{})
	if err != nil || d == nil {
		t.Fatal(d, err)
	}
	var editErr, parseErr error
	called := false
	err = a.ApplyDiff(d, ApplyDiffOptions{Callback: func(_, _ *Node) error {
		if called {
			return nil
		}
		called = true
		r := &s4UserReader{in: strings.NewReader(`{}`), user: s4Once(func() error {
			_, editErr = leaf.SetValue("9")
			return yang.ErrBudget
		})}
		_, _, _, parseErr = parseOp(context.Background(), r, set, FormatJSON, opReply, req, Reject)
		return nil
	}})
	if err != nil || !called {
		t.Fatalf("ApplyDiff: %v (called %t)", err, called)
	}
	if !s4Refused(editErr) || parseErr == nil || leaf.Value() != "1" {
		t.Fatalf("Reader edit of the request: %v, ParseOp %v, value %q", editErr, parseErr, leaf.Value())
	}
	if req.kids.work != nil && req.kids.work.journal != nil {
		t.Fatal("the request's binding was not restored after ParseOp")
	}
}
