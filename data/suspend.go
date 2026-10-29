// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
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

// restoreUser gives the caller the original user error back; call it once, at the public
// return, after the operation's state has ended. A sealed error our own code wrapped keeps that
// wrapping: the result unwraps to both, and its text names the original. The search walks only
// wrappers of this package and the standard library, so it never calls a user method (an
// already restored error, or a user error that is not sealed, is returned as it is).
func restoreUser(err error) error {
	u := sealedIn(err)
	if u == nil {
		return err
	}
	if err == u { //nolint:errorlint // the sealed error itself, returned as is
		return u.err
	}
	return &restoredErr{outer: err, user: u}
}

// sealedIn finds a sealed user error through our own and the standard library's wrappers.
func sealedIn(err error) *userErr {
	stack := []error{err}
	for len(stack) != 0 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch x := e.(type) { //nolint:errorlint // walking the chain by hand: errors.As would call user methods
		case nil:
			continue
		case *userErr:
			return x
		case *restoredErr:
			continue // restored already: what it wraps is user code
		}
		if !ownWrapper(e) {
			continue
		}
		switch x := e.(type) { //nolint:errorlint // our own and the standard library's wrappers only
		case interface{ Unwrap() error }:
			stack = append(stack, x.Unwrap())
		case interface{ Unwrap() []error }:
			stack = append(stack, x.Unwrap()...)
		}
	}
	return nil
}

// ownWrapper reports whether e's Unwrap is ours or the standard library's: this package's
// wrapper types by name (a type of a caller's, even one declared in this package's tests, is
// never walked), the internal packages', and fmt's and errors' joins and wraps.
func ownWrapper(e error) bool {
	switch e.(type) { //nolint:errorlint // the dynamic type itself, not the chain
	case *ValidationError, *loggedContextError, *rcErr, *printerTypeError,
		*xpathPrintNotice, *xpathPrintFailure, *xpathPrintResourceFailure:
		return true
	}
	t := reflect.TypeOf(e)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch p := t.PkgPath(); p {
	case "fmt", "errors":
		return true
	default:
		return strings.HasPrefix(p, "github.com/vibe-ports/yang/internal/")
	}
}

type restoredErr struct {
	outer error
	user  *userErr
}

func (e *restoredErr) Error() string {
	return strings.Replace(e.outer.Error(), e.user.Error(), e.user.err.Error(), 1)
}

func (e *restoredErr) Unwrap() []error { return []error{e.outer, e.user.err} }

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
// its own has no capture to hook, design 13 §2.4 (5)). Each visit is charged to w before it
// is made (nil: inside a scope whose own walk pays it), and the visited set is allocated only
// once the walk recurses, so a refused budget stops it at its first units.
func linkGuard(n *Node, w *workCounter) error {
	if n == nil || n.links == nil {
		return nil
	}
	pay := func() error {
		if w == nil || w.prepaid {
			return nil
		}
		return w.chargeHash(1)
	}
	if governing(n).suspendedState() {
		return errInProgress // n's own operation waits: nothing of this one is involved yet
	}
	var seen map[*Node]bool
	stack := []*Node{n}
	for len(stack) != 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, side := range [2][]*Node{x.links.leafrefs, x.links.targets} {
			for _, c := range side {
				if err := pay(); err != nil {
					return err
				}
				if c == n || seen[c] {
					continue
				}
				if governing(c).suspendedState() {
					return refuseFor(n) // the operation editing n's records fails, and restores
				}
				// freeLinks recurses into a counterpart whose record it empties
				if c.links != nil && len(c.links.leafrefs)+len(c.links.targets) <= 1 {
					if seen == nil {
						seen = map[*Node]bool{n: true}
					}
					seen[c] = true
					stack = append(stack, c)
				}
			}
		}
	}
	return nil
}

// inProgress reports whether w's state waits on user code: a public mutator of a tree (or node)
// it governs is refused at entry, before it charges, touches or sets the shared counter's sticky
// error, which would otherwise fail the waiting operation.
func (w *workCounter) inProgress() bool { return w != nil && w.journal.suspendedState() }

// refusedEntry is that refusal, logged as LY_EINVAL in the caller's context: a *ValidationError
// carrying the diagnostic.
func refusedEntry(set *schema.Set) error { _, err := refusedEntryDiags(set); return err }

// refusedEntryDiags is refusedEntry with the diagnostics, for the calls that return them.
func refusedEntryDiags(set *schema.Set) ([]yang.Diagnostic, error) {
	lg := &logger{set: set}
	err := lg.done(errInProgress)
	return lg.diags, err
}

// isGuardRefusal reports whether err is, or carries, the guard's refusal in any of the forms our
// conversions give it: the error itself, an *opError or a *diffError item that copied its
// message, a *ValidationError that logged it. Only this module's and the standard library's
// wrappers are walked, so no user method runs (identity is not relied on: a conversion that
// logs or copies the message keeps the refusal visible).
func isGuardRefusal(err error) bool {
	stack := []error{err}
	for len(stack) != 0 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch x := e.(type) { //nolint:errorlint // walking the chain by hand: errors.As would call user methods
		case nil:
			continue
		case *opError:
			if x == errInProgress || x.Msg == errInProgress.Msg {
				return true
			}
		case *diffError:
			for _, it := range x.items {
				if it == errInProgress || it.Msg == errInProgress.Msg {
					return true
				}
			}
		case *ValidationError:
			if hasRefusalDiag(x.Diags) {
				return true
			}
		case *restoredErr, *userErr:
			continue // user code's own error
		}
		if !ownWrapper(e) {
			continue
		}
		switch x := e.(type) { //nolint:errorlint // our own and the standard library's wrappers only
		case interface{ Unwrap() error }:
			stack = append(stack, x.Unwrap())
		case interface{ Unwrap() []error }:
			stack = append(stack, x.Unwrap()...)
		}
	}
	return false
}

// hasRefusalDiag reports whether diags log the guard's refusal.
func hasRefusalDiag(diags []yang.Diagnostic) bool {
	for _, d := range diags {
		if d.Msg == errInProgress.Msg {
			return true
		}
	}
	return false
}

// normalizeRefusal gives a public call's guard refusal its one public form (design 13 §2.4),
// deferred first at every public boundary that can reach the guard, so it runs after the
// operation's journal has ended: a *ValidationError with RC LY_EINVAL whose diagnostics log the
// refusal, the diagnostic also in the call's returned diagnostics (diags, nil for error-only
// calls). Whatever internal form reached the boundary (raw, copied, wrapped, logged among other
// diagnostics), no raw *opError escapes.
func normalizeRefusal(set *schema.Set, err *error, diags *[]yang.Diagnostic) {
	if err == nil || !isGuardRefusal(*err) {
		return
	}
	ve, logged := (*err).(*ValidationError) //nolint:errorlint // our own type at the top of the chain only
	if !logged || ve.RC() != "LY_EINVAL" || !hasRefusalDiag(ve.Diags) {
		d, e := refusedEntryDiags(set)
		*err = e
		if diags != nil && !hasRefusalDiag(*diags) {
			*diags = append(*diags, d...)
		}
		return
	}
	if diags != nil && !hasRefusalDiag(*diags) {
		*diags = append(*diags, ve.Diags...)
	}
}
