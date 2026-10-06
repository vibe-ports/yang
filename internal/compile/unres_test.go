// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"errors"
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
