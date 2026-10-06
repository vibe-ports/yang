// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// groupingChain is a module whose grouping g<n> uses g<n-1> twice: 2^n uses of the empty g0
// from linear text, without a single schema node.
func groupingChain(n int, twice bool) string {
	var b strings.Builder
	b.WriteString("module b { namespace urn:b; prefix b; grouping g0; ")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "grouping g%d { uses g%d; ", i, i-1)
		if twice {
			fmt.Fprintf(&b, "uses g%d; ", i-1)
		}
		b.WriteString("} ")
	}
	fmt.Fprintf(&b, "container c { uses g%d; } }", n)
	return b.String()
}

// TestUsesBudget: uses instantiations count against MaxNodes (U-0037) and nest against MaxDepth
// (U-0035), so grouping explosion and deep uses chains stop with ErrBudget.
func TestUsesBudget(t *testing.T) {
	for _, tc := range []struct {
		src    string
		budget Budget
	}{
		{groupingChain(40, true), Budget{MaxNodes: 1 << 12}},
		{groupingChain(200, false), Budget{MaxDepth: 100}},
	} {
		h := newNodeHarness(t, Options{Budget: tc.budget}, mapFS(map[string]string{"b.yang": tc.src}))
		if _, _, loadErr, err := h.load("b"); loadErr != nil || !errors.Is(err, ErrBudget) {
			t.Fatalf("%+v: got %v %v", tc.budget, loadErr, err)
		}
	}
	h := newNodeHarness(t, Options{}, mapFS(map[string]string{"b.yang": groupingChain(10, true)}))
	if _, _, loadErr, err := h.load("b"); loadErr != nil || err != nil {
		t.Fatalf("within budget: %v %v", loadErr, err)
	}
}

// TestRefinedActionKeepsIO: a refined action keeps its input and output (D-0080; libyang's copy
// of the action has nameless, empty input and output).
func TestRefinedActionKeepsIO(t *testing.T) {
	src := `module b { yang-version 1.1; namespace urn:b; prefix b;
	  grouping g { action a { input { leaf i { type string; } } output { leaf o { type string; } } }
	    notification n { leaf l { type string; } } }
	  container c { uses g { refine a { description "x"; } refine n { description "y"; } } } }`
	h := newNodeHarness(t, Options{}, mapFS(map[string]string{"b.yang": src}))
	mod, _, loadErr, err := h.load("b")
	if loadErr != nil || err != nil {
		t.Fatal(loadErr, err)
	}
	var got []string
	for _, n := range dumpTree(mod) {
		got = append(got, n.Path)
	}
	want := "/b:c /b:c/a /b:c/a/input /b:c/a/input/i /b:c/a/output /b:c/a/output/o /b:c/n /b:c/n/l"
	if strings.Join(got, " ") != want {
		t.Fatalf("got %v, want %s", got, want)
	}
}

// TestUsesWork bounds the lookups of grouping resolution and of pending refines and augments
// by counting them (Context.work), not by wall clock: each stays linear in the schema text.
func TestUsesWork(t *testing.T) {
	const n, levels, r = 100000, 12, 2000
	var b strings.Builder
	b.WriteString("module b { namespace urn:b; prefix b; ")
	// n unused top-level groupings: each is validated on its own (P4) after a name lookup
	for i := range n {
		fmt.Fprintf(&b, "grouping u%d { leaf l { type string; } } ", i)
	}
	// a fan-out: 2^levels uses of a grouping chain
	b.WriteString("grouping g0 { leaf x { type string; } } ")
	for i := 1; i <= levels; i++ {
		fmt.Fprintf(&b, "grouping g%d { container a { uses g%d; } container b { uses g%d; } } ", i, i-1, i-1)
	}
	// r refines and r uses augments pending on one uses of a grouping with r containers
	b.WriteString("grouping wide { ")
	for i := range r {
		fmt.Fprintf(&b, "container c%d { leaf l { type string; } } ", i)
	}
	fmt.Fprintf(&b, "} container top { uses g%d; uses wide { ", levels)
	for i := range r {
		fmt.Fprintf(&b, "refine c%d/l { description d; } augment c%d { leaf m { type string; } } ", i, i)
	}
	b.WriteString("} } }")
	h := newNodeHarness(t, Options{}, mapFS(map[string]string{"b.yang": b.String()}))
	if _, _, loadErr, err := h.load("b"); loadErr != nil || err != nil {
		t.Fatal(loadErr, err)
	}
	// index entries plus one lookup per uses or validated grouping; per refine and augment a
	// few candidates and scan steps (merge check, the scans of its target and the target's child)
	if limit := 3*n + 4<<levels + 32*r; h.c.work > limit {
		t.Fatalf("work %d, want at most %d", h.c.work, limit)
	}
	t.Logf("work %d", h.c.work)
}

// TestUsesWorkWildcard: refines that libyang matches with a prefix the compiled module does not
// define are tried (and logged) at every node, as libyang does, but each node collects its
// candidates once: the work follows the logged lines, not their square. Refines with one
// target text merge through the names index.
func TestUsesWorkWildcard(t *testing.T) {
	const w = 300
	var b strings.Builder
	b.WriteString("module pb { namespace urn:pb; prefix b; grouping gi { container a { leaf l { type string; } } ")
	for i := range w {
		fmt.Fprintf(&b, "leaf l%d { type string; } ", i)
	}
	b.WriteString("} grouping gb { container z { uses gi { ")
	for i := range w {
		fmt.Fprintf(&b, "refine b:l%d { description d; } refine a/l { description d%d; } ", i, i)
	}
	b.WriteString("} } } }")
	h := newNodeHarness(t, Options{}, mapFS(map[string]string{
		"pa.yang": "module pa { namespace urn:pa; prefix a; import pb { prefix x; } container c { uses x:gb; } }",
		"pb.yang": b.String()}))
	_, diags, loadErr, err := h.load("pa")
	if loadErr != nil || !errors.Is(err, eNotFound) {
		t.Fatal(loadErr, err)
	}
	if limit := 3*len(diags) + 20*w; h.c.work > limit {
		t.Fatalf("work %d for %d diagnostics, want at most %d", h.c.work, len(diags), limit)
	}
	t.Logf("work %d, %d diagnostics", h.c.work, len(diags))
}
