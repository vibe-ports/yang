// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestXPathStepsBudget: a when/must check over the schema stops with ErrBudget past
// Budget.MaxXPathSteps (U-0036).
func TestXPathStepsBudget(t *testing.T) {
	src := "module b { namespace urn:b; prefix b; container c { leaf a { type string; } leaf b { type string; } " +
		"leaf x { type string; must \"count(//*[. = ../*]) > 0\"; } } }"
	h := newNodeHarness(t, Options{Budget: Budget{MaxXPathSteps: 10}}, mapFS(map[string]string{"b.yang": src}))
	if _, _, loadErr, err := h.load("b"); loadErr != nil || !errors.Is(err, ErrBudget) {
		t.Fatalf("got %v %v", loadErr, err)
	}
	h = newNodeHarness(t, Options{}, mapFS(map[string]string{"b.yang": src}))
	if _, _, _, err := h.load("b"); err != nil {
		t.Fatalf("default budget: %v", err)
	}
}

// whenChain is a module of n leaves, each conditioned on the one before: the cycle check of each
// when walks the whole chain behind it, so the work grows as n².
func whenChain(n int) string {
	var b strings.Builder
	b.WriteString("module w { namespace urn:w; prefix w; container c { leaf l0 { type string; }\n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "leaf l%d { when \"../l%d\"; type string; }\n", i, i-1)
	}
	b.WriteString("} }")
	return b.String()
}

// TestXPathStepsBudgetShared: the budget is one per Load, not per expression, so a when chain
// whose every single check is small still stops with ErrBudget (U-0036, issue #97).
func TestXPathStepsBudgetShared(t *testing.T) {
	const budget = 5000
	for _, tc := range []struct {
		n    int
		fail bool
	}{{3, false}, {80, true}} {
		h := newNodeHarness(t, Options{Budget: Budget{MaxXPathSteps: budget}}, mapFS(map[string]string{"w.yang": whenChain(tc.n)}))
		_, _, loadErr, err := h.load("w")
		if loadErr != nil || errors.Is(err, ErrBudget) != tc.fail || !tc.fail && err != nil {
			t.Fatalf("chain of %d: %v %v", tc.n, loadErr, err)
		}
	}
}
