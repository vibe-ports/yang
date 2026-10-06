// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"errors"
	"testing"
)

// TestResultSteps: Result.Steps is the exact budget consumed: a budget of
// exactly Steps succeeds, one less gives ErrBudget (and reports Steps-1).
func TestResultSteps(t *testing.T) {
	tree := aTree()
	for _, src := range []string{"1 + 2", "count(//ll)", "//ll[a and b]/a/ancestor::node()", "string(a:c)"} {
		r, err := eval(src, EvalContext{Tree: tree})
		if err != nil || r.Steps <= 0 {
			t.Fatalf("%s: steps %d, %v", src, r.Steps, err)
		}
		if r2, err := eval(src, EvalContext{Tree: tree}); err != nil || r2.Steps != r.Steps {
			t.Errorf("%s: steps not deterministic: %d vs %d", src, r.Steps, r2.Steps)
		}
		if _, err := eval(src, EvalContext{Tree: tree, MaxSteps: int(r.Steps)}); err != nil {
			t.Errorf("%s: budget == Steps (%d) must succeed: %v", src, r.Steps, err)
		}
		r3, err := eval(src, EvalContext{Tree: tree, MaxSteps: int(r.Steps) - 1})
		if !errors.Is(err, ErrBudget) {
			t.Errorf("%s: budget Steps-1 must give ErrBudget, got %v", src, err)
		}
		if r3.Steps != r.Steps-1 {
			t.Errorf("%s: error Steps %d, want %d", src, r3.Steps, r.Steps-1)
		}
	}
}
