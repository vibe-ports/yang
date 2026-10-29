// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/vibe-ports/yang"
)

// One recovery state per operation, and a state waiting on user code is suspended (#240 step 4,
// design 13 §2.4). User code runs during a mutation only in the diff callbacks, the Reader of
// a ParseOp with a parent, and the methods of a context.Context (and of the errors they all
// return). Around each, the operation raises its state's suspendDepth and lowers it in a defer,
// so a panic restores it too and a nested site (a callback's XPath query polling its context)
// cannot unmask the outer one. Any capture, lend, begin or link-record edit that reaches an
// object governed by a suspended state is refused with errInProgress before it writes.

// errInProgress is the guard's refusal (D-0121 style).
var errInProgress = &opError{"LY_EINVAL", "Object owned by an operation in progress."}

// errPanicked is the result a panic-safe finalization ends a journal with: it restores.
var errPanicked = errors.New("data: operation interrupted by a panic")

// poll is the one context poll of every operation in data (design 13 §2.4): a public entry
// wraps the user's context in a suspending one (suspending), whose Err runs the user's Err with
// the operation's states suspended. TestS4PollsGoThroughOneHelper checks no poll bypasses it.
func poll(ctx context.Context) error {
	return ctx.Err()
}

// suspendedState reports whether j waits on user code.
func (j *hashTransaction) suspendedState() bool { return j != nil && j.suspendDepth > 0 }

// governing is the state an edit of n is recorded in (Node.touch's resolution).
func governing(n *Node) *hashTransaction {
	if j := childStorage(n).journal(); j != nil {
		return j
	}
	if t := n.treeOf(); t != nil {
		return t.work.journal
	}
	return nil
}

// suspend marks w's running state as waiting on user code until the returned func runs.
func (w *workCounter) suspend() func() {
	if w == nil || w.journal == nil {
		return func() {}
	}
	if overdraftPanics && w.prepaid {
		panic("data: a prepaid credit spans user code")
	}
	j := w.journal
	j.suspendDepth++
	return func() { j.suspendDepth-- }
}

// userErr is an error returned by user code, classified while its state was still suspended.
// It has no Unwrap or As: internal code (journal.fail, parse cleanup, diffDone) reaches only
// these flags through Is and never calls a user method. The public return restores the
// original (restoreUser), after the state has ended.
type userErr struct {
	err                        error
	canceled, deadline, budget bool
}

func (e *userErr) Error() string {
	switch {
	case e.budget:
		return yang.ErrBudget.Error()
	case e.canceled:
		return context.Canceled.Error()
	case e.deadline:
		return context.DeadlineExceeded.Error()
	}
	return "data: error returned by user code"
}

func (e *userErr) Is(target error) bool {
	switch target {
	case yang.ErrBudget:
		return e.budget
	case context.Canceled:
		return e.canceled
	case context.DeadlineExceeded:
		return e.deadline
	}
	return false
}

// classifyUser seals a user error; call it while suspended. The standard context sentinels are
// ours to keep as they are.
func classifyUser(err error) error {
	if err == nil || err == context.Canceled || err == context.DeadlineExceeded { //nolint:errorlint // the exact sentinels, no user method
		return err
	}
	return &userErr{err: err, canceled: errors.Is(err, context.Canceled),
		deadline: errors.Is(err, context.DeadlineExceeded), budget: errors.Is(err, yang.ErrBudget)}
}

// restoreUser gives the caller the original user error back; call it at the public return,
// after the operation's state has ended.
func restoreUser(err error) error {
	var u *userErr
	if !errors.As(err, &u) {
		return err
	}
	return u.err
}

// callUser runs user code f suspended, its error classified before the state resumes.
func (w *workCounter) callUser(f func() error) error {
	defer w.suspend()()
	return classifyUser(f())
}

// readUser is io.ReadAll of a user Reader, suspended, its error classified inside.
func readUser(w *workCounter, r io.Reader) ([]byte, error) {
	defer w.suspend()()
	in, err := io.ReadAll(r)
	return in, classifyUser(err)
}

// suspendCtx runs every method of a user context with the states of its counters suspended,
// and classifies Err's result before they resume; every context poll of an operation (in data
// and internal/xpath) goes through it.
type suspendCtx struct {
	user context.Context
	ws   []*workCounter
}

// suspending wraps ctx for an operation running on the counters ws (resolved at each call:
// a state opened later is suspended too).
func suspending(ctx context.Context, ws ...*workCounter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if s, ok := ctx.(*suspendCtx); ok {
		ctx = s.user
	}
	return &suspendCtx{user: ctx, ws: ws}
}

func (c *suspendCtx) hold() func() {
	var done [4]func()
	n := 0
	for _, w := range c.ws {
		if n < len(done) {
			done[n] = w.suspend()
			n++
		}
	}
	return func() {
		for i := n - 1; i >= 0; i-- {
			done[i]()
		}
	}
}

func (c *suspendCtx) Deadline() (time.Time, bool) {
	defer c.hold()()
	return c.user.Deadline()
}

func (c *suspendCtx) Done() <-chan struct{} {
	defer c.hold()()
	return c.user.Done()
}

func (c *suspendCtx) Err() error {
	defer c.hold()()
	return classifyUser(c.user.Err())
}

func (c *suspendCtx) Value(key any) any {
	defer c.hold()()
	return c.user.Value(key)
}

// linkGuard refuses, before any write, an edit of n's leafref records that would reach a record
// governed by a suspended state: freeLinks' walk, read-only (an operation without a journal of
// its own has no capture to hook, design 13 §2.4 (5)).
func linkGuard(n *Node) error {
	if n == nil || n.links == nil {
		return nil
	}
	seen := map[*Node]bool{n: true}
	stack := []*Node{n}
	for len(stack) != 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if governing(x).suspendedState() {
			return errInProgress
		}
		if x.links == nil {
			continue
		}
		for _, side := range [2][]*Node{x.links.leafrefs, x.links.targets} {
			for _, c := range side {
				if seen[c] {
					continue
				}
				seen[c] = true
				if governing(c).suspendedState() {
					return errInProgress
				}
				// freeLinks recurses into a counterpart whose record it empties
				if c.links != nil && len(c.links.leafrefs)+len(c.links.targets) <= 1 {
					stack = append(stack, c)
				}
			}
		}
	}
	return nil
}
