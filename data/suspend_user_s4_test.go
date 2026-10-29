// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
)

// #240 step 4 (design 13 §2.4, G8 c, d, h, h2, j; G10e): user code reached through a Reader, a
// context and the methods of returned errors.

func s4OpsFixture(t *testing.T) (*schema.Set, func() (*Tree, *Node)) {
	t.Helper()
	set := opsSet(t, readOpsFixture(t, filepath.Join(opsManifest, "parse-xml-rpc-input.yaml")))
	return set, func() (*Tree, *Node) { return opParent(t, set) }
}

// mutatingReader runs mutate on its first Read, then yields in.
type mutatingReader struct {
	in     io.Reader
	mutate func()
	done   bool
}

func (r *mutatingReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		r.mutate()
	}
	return r.in.Read(p)
}

// mutatingCtx runs mutate in every Err.
type mutatingCtx struct {
	context.Context
	mutate func()
	err    error
	polls  int
}

func (c *mutatingCtx) Err() error {
	c.polls++
	c.mutate()
	return c.err
}

// TestS4GuardReaderAndContextDuringParse is G8(c) and (d): a ParseOp with a parent whose Reader,
// or whose context's Err, mutates the owner tree: every mutation is refused with LY_EINVAL and the
// owner ends as in the reference without the attempts. Precedence is main's: with the owner's
// startup capture exhausting the quota and the Reader failing, ErrBudget wins; an oversized
// input is still the budget error.
func TestS4GuardReaderAndContextDuringParse(t *testing.T) {
	set, parent := s4OpsFixture(t)
	ref, refOp := parent()
	if _, _, _, err := parseOp(context.Background(), strings.NewReader(`{}`), set, FormatJSON, opReply, refOp, Reject); err != nil {
		t.Fatal(err)
	}
	want := typedDump(ref)
	t.Run("reader", func(t *testing.T) {
		tree, op := parent()
		var mutErr error
		r := &mutatingReader{in: strings.NewReader(`{}`), mutate: func() { mutErr = tree.top.first().Remove() }}
		if _, _, _, err := parseOp(context.Background(), r, set, FormatJSON, opReply, op, Reject); err != nil {
			t.Fatal(err)
		}
		if !s4Refused(mutErr) {
			t.Fatalf("the Reader's mutation: %v, want the guard's refusal", mutErr)
		}
		if got := typedDump(tree); !reflect.DeepEqual(got, want) {
			t.Fatalf("owner %v, want %v", got, want)
		}
	})
	t.Run("context", func(t *testing.T) {
		tree, op := parent()
		refusals := 0
		ctx := &mutatingCtx{Context: context.Background()}
		ctx.mutate = func() {
			if err := tree.top.first().Remove(); s4Refused(err) {
				refusals++
			}
		}
		if _, _, _, err := parseOp(ctx, strings.NewReader(`{}`), set, FormatJSON, opReply, op, Reject); err != nil {
			t.Fatal(err)
		}
		if ctx.polls == 0 || refusals != ctx.polls {
			t.Fatalf("%d polls, %d refusals", ctx.polls, refusals)
		}
		if got := typedDump(tree); !reflect.DeepEqual(got, want) {
			t.Fatalf("owner %v, want %v", got, want)
		}
	})
	t.Run("precedence", func(t *testing.T) {
		tree, op := parent()
		for _, sn := range set.Modules[0].Top { // make the owner's startup capture exceed the quota
			tree.schemaRank(sn)
		}
		tree.work.hashLimit = 1
		readErr := errors.New("read failed")
		_, _, _, err := parseOp(context.Background(), errReader{readErr}, set, FormatJSON, opReply, op, Reject)
		if len(tree.rank) != 0 && !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("startup refusal and a Reader error: %v, want ErrBudget first", err)
		}
		tree.work.hashLimit = 0
		if !raceEnabled { // a DefaultMaxBytes input; TestParseOpInputErrors covers it under race
			big := strings.NewReader(`{"a:al":"` + strings.Repeat("1", DefaultMaxBytes) + `"}`)
			if _, _, _, err := parseOp(context.Background(), big, set, FormatJSON, opReply, op, Reject); !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("oversized input: %v", err)
			}
		}
		if tree.work.journal != nil {
			t.Fatal("a journal stayed installed")
		}
	})
}

// trapErr records each method call with the state it ran under, and tries to mutate its tree.
type trapErr struct {
	tree   *Tree
	calls  *[]string
	panics bool
}

func (e *trapErr) Error() string { return "trap" }

func (e *trapErr) visit(m string) {
	state := "unsuspended"
	if j := e.tree.work.journal; j == nil {
		state = "ended"
	} else if j.suspendDepth > 0 {
		state = "suspended"
	}
	refused := "allowed"
	if state == "ended" {
		refused = "refused-or-ended"
	} else if err := e.tree.top.first().Remove(); s4Refused(err) {
		refused = "refused-or-ended"
	}
	*e.calls = append(*e.calls, m+":"+state+":"+refused)
	if e.panics {
		panic("trap " + m)
	}
}

func (e *trapErr) Is(_ error) bool {
	e.visit("Is")
	return false
}

func (e *trapErr) Unwrap() error {
	e.visit("Unwrap")
	return nil
}

// TestS4GuardUserErrorMethods is G8(h): a context's error, a callback's error and a Reader's
// error whose Is/Unwrap record when they run and try to mutate the tree being changed. They run
// only during classification, while suspended, and every mutation is refused; nothing calls them
// during end, fail, restore or cleanup; the caller receives the original error.
func TestS4GuardUserErrorMethods(t *testing.T) {
	set, parent := s4OpsFixture(t)
	check := func(t *testing.T, err error, trap *trapErr) {
		t.Helper()
		if err != trap { //nolint:errorlint // the original error itself
			t.Fatalf("the caller got %v, want the original error", err)
		}
		if len(*trap.calls) == 0 {
			t.Fatal("classification called no method")
		}
		for _, c := range *trap.calls {
			if !strings.HasPrefix(strings.SplitN(c, ":", 2)[1], "suspended:refused") {
				t.Fatalf("a user method ran outside classification: %v", *trap.calls)
			}
		}
	}
	t.Run("reader", func(t *testing.T) {
		tree, op := parent()
		trap := &trapErr{tree: tree, calls: &[]string{}}
		_, _, _, err := parseOp(context.Background(), errReader{trap}, set, FormatJSON, opReply, op, Reject)
		check(t, err, trap)
	})
	t.Run("public-reader", func(t *testing.T) {
		tree, op := parent()
		trap := &trapErr{tree: tree, calls: &[]string{}}
		_, _, err := ParseOp(context.Background(), errReader{trap}, FormatJSON, snap.New(set), OpReply,
			ParseOpOptions{Request: op})
		check(t, err, trap)
	})
	t.Run("public-context", func(t *testing.T) {
		tree, op := parent()
		trap := &trapErr{tree: tree, calls: &[]string{}}
		ctx := &mutatingCtx{Context: context.Background(), mutate: func() {}, err: trap}
		_, _, err := ParseOp(ctx, strings.NewReader(`{}`), FormatJSON, snap.New(set), OpReply,
			ParseOpOptions{Request: op})
		check(t, err, trap)
	})
	t.Run("context", func(t *testing.T) {
		tree, op := parent()
		trap := &trapErr{tree: tree, calls: &[]string{}}
		ctx := &mutatingCtx{Context: context.Background(), mutate: func() {}, err: trap}
		_, _, _, err := parseOp(ctx, strings.NewReader(`{}`), set, FormatJSON, opReply, op, Reject)
		check(t, err, trap)
	})
	t.Run("callback", func(t *testing.T) {
		lset := s4LinksSet(t)
		a, _, _, _ := s4LinkedPair(t, lset)
		before := typedDump(a)
		trap := &trapErr{tree: a, calls: &[]string{}}
		err := a.ApplyDiff(s4Diff(t, lset, a, false), ApplyDiffOptions{Callback: func(_, _ *Node) error { return trap }})
		check(t, err, trap)
		if got := typedDump(a); !reflect.DeepEqual(got, before) && len(*trap.calls) == 0 {
			t.Fatal("unreachable")
		}
	})
}

// TestS4GuardUserPanics is G8(h2): a panicking Read, and a panicking Is on the Reader's error,
// propagate; the suspension depth returns, no journal stays installed, the hash-work scope
// closes, and the owner equals its state before the call. With the owner's startup capture
// exhausting the quota first, a fresh operation afterwards starts from a reset allowance.
func TestS4GuardUserPanics(t *testing.T) {
	set, parent := s4OpsFixture(t)
	for _, tc := range []string{"read", "is", "is-after-budget"} {
		t.Run(tc, func(t *testing.T) {
			tree, op := parent()
			before := typedDump(tree)
			depth := tree.work.hashDepth
			var r io.Reader
			switch tc {
			case "read":
				r = &mutatingReader{in: strings.NewReader(`{}`), mutate: func() { panic("read") }}
			default:
				r = errReader{&trapErr{tree: tree, calls: &[]string{}, panics: true}}
			}
			if tc == "is-after-budget" {
				for _, sn := range set.Modules[0].Top {
					tree.schemaRank(sn)
				}
				tree.work.hashLimit = 1
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("the panic did not propagate")
					}
				}()
				_, _, _, _ = parseOp(context.Background(), r, set, FormatJSON, opReply, op, Reject)
			}()
			tree.work.hashLimit = 0
			if tree.work.journal != nil || tree.work.hashDepth != depth {
				t.Fatalf("after the panic: journal %v, hash-work depth %d (was %d)", tree.work.journal, tree.work.hashDepth, depth)
			}
			if got := typedDump(tree); !reflect.DeepEqual(got, before) {
				t.Fatalf("owner %v, want %v", got, before)
			}
			if _, _, _, err := parseOp(context.Background(), strings.NewReader(`{}`), set, FormatJSON, opReply, op, Reject); err != nil {
				t.Fatalf("a fresh operation after the panic: %v", err)
			}
		})
	}
}

// TestS4GuardValidationPolls is G8(j): Validate with a context whose Err mutates the tree being
// validated: every mutation is refused.
func TestS4GuardValidationPolls(t *testing.T) {
	set := s4LinksSet(t)
	a, _, _, _ := s4LinkedPair(t, set)
	allowed := 0
	ctx := &mutatingCtx{Context: context.Background()}
	ctx.mutate = func() {
		if _, err := a.NewTerm(a.top.first(), "t", fmt.Sprint("v", ctx.polls), NewOptions{}); err == nil {
			allowed++
		}
	}
	if _, err := a.Validate(ctx, ValidateOptions{}); err != nil {
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatal(err)
		}
	}
	if ctx.polls == 0 || allowed != 0 {
		t.Fatalf("%d polls, %d mutations allowed", ctx.polls, allowed)
	}
}

// TestS4ReadOnlyFindFromSuspendedCallback is G10e: a read-only lookup from a suspended callback
// pays nothing (class C): no detector panic and an unchanged hashUsed.
func TestS4ReadOnlyFindFromSuspendedCallback(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	before := s4DataTree(t, set, "3", "7", "11")
	after := s4DataTree(t, set, "3", "9", "11")
	diff, err := Diff(before, after, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tr, op := s4OpTree(t, set, "5", "1", "9")
	_ = op
	dtr := s4DataTree(t, set, "3", "7", "11")
	err = dtr.ApplyDiff(diff, ApplyDiffOptions{Callback: func(_, _ *Node) error {
		used := dtr.work.hashUsed
		for k := range 12 {
			if _, err := dtr.Find("/s4j:c/l[k='" + string(rune('0'+k%10)) + "']"); err != nil {
				return err
			}
		}
		if dtr.work.hashUsed != used {
			t.Errorf("a read-only lookup charged %d units", dtr.work.hashUsed-used)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = tr
}

// TestS4PollsGoThroughOneHelper checks that every context poll in data and internal/xpath is
// the one poll helper's (design 13 §2.4): a public entry wraps the user's context, so no site
// can run the user's Err unsuspended by hand-editing.
func TestS4PollsGoThroughOneHelper(t *testing.T) {
	allowed := map[string]bool{
		"suspend.go:return ctx.Err()":                                       true,
		"suspend.go:return classifyUser(c.user.Err())":                      true,
		"eval.go:func poll(ctx context.Context) error { return ctx.Err() }": true,
	}
	re := regexp.MustCompile(`\.Err\(\)`)
	for _, dir := range []string{".", "../internal/xpath"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f) //nolint:gosec // the package's own source files
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(string(src), "\n") {
				code := strings.TrimSpace(line)
				if strings.HasPrefix(code, "//") || !re.MatchString(code) {
					continue
				}
				if !allowed[filepath.Base(f)+":"+code] {
					t.Errorf("%s: a context poll bypasses the helper: %s", filepath.Base(f), code)
				}
			}
		}
	}
}

// TestS4RestoreUserKeepsOurWrapping: a sealed user error that our own code wrapped comes back
// with that wrapping, unwrapping to both, its text naming the original.
func TestS4RestoreUserKeepsOurWrapping(t *testing.T) {
	orig := errors.New("user failure")
	sealed := classifyUser(orig)
	if errors.Is(sealed, orig) {
		t.Fatal("a sealed error must not reach the user's error before the state ends")
	}
	if got := restoreUser(sealed); got != orig { //nolint:errorlint // the original itself
		t.Fatalf("restoreUser(sealed) = %v", got)
	}
	ve := &ValidationError{err: fmt.Errorf("validating: %w", sealed)}
	got := restoreUser(ve)
	var back *ValidationError
	if !errors.Is(got, orig) || !errors.As(got, &back) || back != ve || !strings.Contains(got.Error(), "user failure") {
		t.Fatalf("restoreUser(wrapped) = %v", got)
	}
}

// TestS4GuardDetachedRequest: a ParseOp into a detached request (its tree is the parser's,
// the request captured whole) whose context mutates the request is refused: the captured
// detached sets are enrolled in the parse's state, so a mutation cannot write under its
// rollback (snapshot review 1, finding 2). Afterwards the request is mutable again.
func TestS4GuardDetachedRequest(t *testing.T) {
	set, parent := s4OpsFixture(t)
	for _, unbound := range []bool{false, true} {
		tree, op := parent()
		root := tree.top.first()
		if err := root.Unlink(); err != nil {
			t.Fatal(err)
		}
		if unbound {
			for n := range root.allAny() { // a set without a counter
				n.kids.setWork(nil)
			}
		}
		before := s4Dumps(newTreeWith(set, root))
		attempts, refusals := 0, 0
		ctx := &mutatingCtx{Context: context.Background()}
		ctx.mutate = func() {
			attempts++
			if err := op.kids.first().Remove(); s4Refused(err) {
				refusals++
			}
		}
		if _, _, _, err := parseOp(ctx, strings.NewReader(`{}`), set, FormatJSON, opReply, op, Reject); err != nil {
			t.Fatal(err)
		}
		if attempts == 0 || refusals != attempts {
			t.Fatalf("unbound=%t: %d attempts, %d refused", unbound, attempts, refusals)
		}
		if got := s4Dumps(newTreeWith(set, root)); !reflect.DeepEqual(got, before) {
			t.Fatalf("unbound=%t: request %v, want %v", unbound, got, before)
		}
		if unbound && op.kids.work != nil {
			t.Fatal("the temporary binding outlived the parse")
		}
		if err := op.kids.first().Remove(); err != nil {
			t.Fatalf("unbound=%t: after the parse: %v", unbound, err)
		}
	}
}

// newTreeWith is a dump view of a detached root (typedDump walks a tree's top level).
func newTreeWith(set *schema.Set, root *Node) *Tree {
	tr := newTree(set)
	if err := tr.top.Build([]*Node{root}, nil); err != nil { // a fresh view: nothing to capture
		panic(err)
	}
	return tr
}

// TestS4ValidateOpPanickingContextError: public ValidateOp with a dependency and a context whose
// error's Is panics while the operation subtree sits in the dependency: the panic propagates,
// both trees are restored, no state stays installed or enrolled, the hash-work scopes close,
// and both trees then validate and mutate again (snapshot review 2, finding 2).
func TestS4ValidateOpPanickingContextError(t *testing.T) {
	set := reviewDiffSet(t, s4JournalModule)
	req, _ := s4OpTree(t, set, "5", "1", "9")
	oper := s4DataTree(t, set, "3", "7")
	before := s4Dumps(req, oper)
	polls := 0
	ctx := &mutatingCtx{Context: context.Background(), mutate: func() { polls++ }}
	ctx.err = &trapErr{tree: req, calls: &[]string{}, panics: true}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic did not propagate")
			}
		}()
		_, _ = req.ValidateOp(ctx, OpRPC, ValidateOpOptions{Operational: oper})
	}()
	if polls == 0 {
		t.Fatal("validation never polled its context")
	}
	for _, tr := range []*Tree{req, oper} {
		if tr.work.journal != nil || tr.work.hashDepth != 0 {
			t.Fatalf("after the panic: journal %v, hash-work depth %d", tr.work.journal, tr.work.hashDepth)
		}
	}
	if got := s4Dumps(req, oper); !reflect.DeepEqual(got, before) {
		t.Fatalf("the trees were not restored:\n%v\nwant\n%v", got, before)
	}
	s4Check(t, "after", []*Tree{req, oper})
	if _, err := req.ValidateOp(context.Background(), OpRPC, ValidateOpOptions{Operational: oper}); err != nil {
		t.Fatal(err)
	}
	if _, err := oper.NewTerm(oper.top.first(), "u", "z", NewOptions{}); err != nil {
		t.Fatal(err)
	}
}

// TestS4ValidatePanickingContextError: Validate and ValidateDiff with a context whose error's Is
// panics end their journal panic-safely: after recovering, no journal is installed, the
// hash-work scope is closed, and the next mutation runs from a reset quota (PR #327 review).
func TestS4ValidatePanickingContextError(t *testing.T) {
	set := s4LinksSet(t)
	for _, diff := range []bool{false, true} {
		a, _, _, _ := s4LinkedPair(t, set)
		ctx := &mutatingCtx{Context: context.Background(), mutate: func() {}}
		ctx.err = &trapErr{tree: a, calls: &[]string{}, panics: true}
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("diff=%t: the panic did not propagate", diff)
				}
			}()
			if diff {
				_, _, _ = a.ValidateDiff(ctx, ValidateOptions{})
			} else {
				_, _ = a.Validate(ctx, ValidateOptions{})
			}
		}()
		if a.work.journal != nil || a.work.hashDepth != 0 {
			t.Fatalf("diff=%t: journal %v, hash-work depth %d left behind", diff, a.work.journal, a.work.hashDepth)
		}
		a.work.hashUsed = 1 << 40 // a stale total would refuse; the next operation resets it
		if _, err := a.NewTerm(a.top.first(), "t", "after", NewOptions{}); err != nil {
			t.Fatalf("diff=%t: the next mutation: %v", diff, err)
		}
		if a.work.journal != nil {
			t.Fatalf("diff=%t: the next mutation left a journal", diff)
		}
	}
}
