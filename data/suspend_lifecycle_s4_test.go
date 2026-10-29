// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vibe-ports/yang"
)

// s4IsErr is a user error whose Is method edits: error methods run while the state waits too.
type s4IsErr struct{ edit func() }

func (e *s4IsErr) Error() string { return "user error with methods" }

func (e *s4IsErr) Is(error) bool { e.edit(); return false }

// TestS4Lifecycle is the guard's lifecycle matrix (design 13, "Suspension guard"): for each
// entry that runs user code (ApplyDiff and MergeDiff callbacks, a ParseOp Reader, a Validate
// context; error methods through every outcome) and each outcome of that user code (success, an
// error, an error with methods, the budget, cancellation, a panic), user code meets:
//   - objects bound to the waiting counter, refused in the public form and unchanged: attached,
//     detached before the operation, detached by it (ApplyDiff+delete);
//   - objects of other counters, edited: a node of an independent idle tree, and a node built
//     during the wait (no counter of the waiting tree);
//
// and after the operation returns, no enrollment survives (every counter idle, no journal) and
// the same objects are edited (the counters reused). A budget failure or a panic restores the
// object exactly; success gives the result of a run without user code. Nesting (a counter
// suspended by another operation) is TestS4NestedOperationsHoldTheirOwn; savepoint rollback is
// TestS4SavepointRollbackKeepsTheHold.
func TestS4Lifecycle(t *testing.T) {
	set := reviewDiffSet(t, s4NestModule)
	sides := s4NestSides(t, set)
	outcomes := []string{"success", "error", "error methods", "budget", "cancel", "panic"}
	cases := 0
	for _, entry := range []string{"ApplyDiff", "ApplyDiff+delete", "MergeDiff", "ParseOp+Reader", "Validate+ctx"} {
		for _, outcome := range outcomes {
			name := entry + "/" + outcome
			benign := sides[entry]()
			if err := benign.run(func() error { return nil }); err != nil {
				t.Fatalf("%s: benign run: %v", name, err)
			}
			op := sides[entry]()
			pre := op.snap()
			held := []*Node{op.held, op.stale}
			if op.during != nil {
				held = append(held, op.during)
			}
			indep := s4ParseLinks(t, set, `{"s4n:c":{"x":"1"}}`)
			indepLeaf := s4Named(t, indep.top.first(), "x")
			var refusals, edits []error
			editHeld := func() {
				for _, n := range held {
					_, err := n.SetValue("9")
					refusals = append(refusals, err)
				}
			}
			user := func() error {
				editHeld()
				_, err := indepLeaf.SetValue("9")
				edits = append(edits, err)
				c, err := indep.NewInner(nil, "s4n:c", NewOptions{})
				if err == nil {
					_, err = indep.NewTerm(c, "x", "1", NewOptions{})
				}
				edits = append(edits, err)
				switch outcome {
				case "error":
					return errors.New("user error")
				case "error methods":
					return &s4IsErr{edit: editHeld}
				case "budget":
					return yang.ErrBudget
				case "cancel":
					return context.Canceled
				case "panic":
					panic("user panic")
				}
				return nil
			}
			var err error
			panicked := func() (p any) {
				defer func() { p = recover() }()
				err = op.run(user)
				return nil
			}()
			cases++
			if (panicked != nil) != (outcome == "panic") || outcome != "panic" && (err == nil) != (outcome == "success") {
				t.Errorf("%s: returned %v, panicked %v", name, err, panicked)
				continue
			}
			if len(refusals) < len(held) {
				t.Errorf("%s: user code made %d edits of held objects", name, len(refusals))
				continue
			}
			for i, err := range refusals {
				s4PublicRefusal(t, fmt.Sprintf("%s: held object %d", name, i%len(held)), nil, err, false)
			}
			for i, n := range held {
				if n.Value() == "9" {
					t.Errorf("%s: a refused edit changed held object %d", name, i)
				}
			}
			for i, err := range edits {
				if err != nil {
					t.Errorf("%s: edit %d of an independent tree: %v", name, i, err)
				}
			}
			switch outcome {
			case "success":
				if op.snap() != benign.snap() {
					t.Errorf("%s: result\n%s\nwant\n%s", name, op.snap(), benign.snap())
				}
			case "budget", "panic":
				if op.snap() != pre {
					t.Errorf("%s: not restored\n%s\nwant\n%s", name, op.snap(), pre)
				}
			}
			for i, n := range held {
				if w := childStorage(n).work; w.held() || w != nil && w.journal != nil {
					t.Errorf("%s: held object %d keeps an enrollment after the operation", name, i)
				}
				if _, err := n.SetValue("8"); err != nil {
					t.Errorf("%s: edit of object %d after the operation: %v", name, i, err)
				}
			}
		}
	}
	t.Logf("%d lifecycle cases", cases)
}

// TestS4SavepointRollbackKeepsTheHold: a level rolled back inside an operation restores what it
// captured and leaves the hold as it was: once the operation waits, a node detached earlier is
// refused; at end no enrollment survives.
func TestS4SavepointRollbackKeepsTheHold(t *testing.T) {
	set := reviewDiffSet(t, s4NestModule)
	tr := s4ParseLinks(t, set, `{"s4n:c":{"x":"1","y":"1"}}`)
	c := tr.top.first()
	x, y := s4Named(t, c, "x"), s4Named(t, c, "y")
	if err := y.Unlink(); err != nil {
		t.Fatal(err)
	}
	j := beginHashTransaction(tr)
	sp, err := j.savepoint()
	if err != nil {
		t.Fatal(err)
	}
	if err := x.touch(); err != nil {
		t.Fatal(err)
	}
	x.value = y.value // an edit inside the level, through its capture
	j.rollbackLevel(sp)
	resume := tr.work.suspend()
	_, xErr := x.SetValue("9")
	_, yErr := y.SetValue("9")
	resume()
	j.end(nil)
	if !s4Refused(xErr) || !s4Refused(yErr) {
		t.Fatalf("while the operation waits: attached %v, detached earlier %v", xErr, yErr)
	}
	if x.Value() != "1" || tr.work.journal != nil || tr.work.held() {
		t.Fatalf("after the rollback and end: value %q, journal %v", x.Value(), tr.work.journal)
	}
	if _, err := y.SetValue("2"); err != nil {
		t.Fatal(err)
	}
}
