// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// TestLongChainEval: a 1M-operator chain is evaluated iteratively.
func TestLongChainEval(t *testing.T) {
	r, err := eval("1"+strings.Repeat(" + 1", 1_000_000), EvalContext{})
	if err != nil || r.Num != 1_000_001 {
		t.Fatalf("got %v, %v", r.Num, err)
	}
}

func bigTree(n int) []Node {
	ls := make([]*tnode, n)
	for i := range ls {
		ls[i] = keyed(list("l", leaf("k", strconv.Itoa(i)), leaf("v", "x")), "k")
	}
	return top(cont("pv2:c", ls...))
}

// BenchmarkScaling: document order is numbered once per evaluation and child
// steps are not sorted, so time grows linearly with the tree (1k → 100k).
func BenchmarkScaling(b *testing.B) {
	const src = "count(/c/l[k = '7']) + count(//k) + count(/c/l[last()]/preceding-sibling::l) + " +
		"string-length(string(/c/l[last()]))"
	for _, n := range []int{1_000, 10_000, 100_000} {
		tree := bigTree(n)
		e, err := Compile(src, jsonNS{"pv2": true})
		if err != nil {
			b.Fatal(err)
		}
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			for b.Loop() {
				if _, err := e.Eval(EvalContext{Tree: tree, Schema: tinfo{tree: tree}}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestContextOp: nodes of an operation other than current()'s are invisible
// (moveto_node_check context_op).
func TestContextOp(t *testing.T) {
	sev := leaf("sev", "1")
	tree := top(cont("pv2:c"), mk(KindNotif, "pv2:alarm", nil, sev), mk(KindNotif, "pv2:other", nil, leaf("x", "1")))
	ec := EvalContext{Tree: tree, Node: sev}
	for src, want := range map[string]float64{"count(/*)": 2, "count(//x)": 0, "count(/other)": 0, "count(/alarm/sev)": 1} {
		if r, err := eval(src, ec); err != nil || r.Num != want {
			t.Errorf("%s: want %v, got %v (%v)", src, want, r.Num, err)
		}
	}
}

// TestBudgetInCasts: string-value and sorting are charged to MaxSteps.
func TestBudgetInCasts(t *testing.T) {
	tree := bigTree(1000)
	for _, src := range []string{"string(/)", "count(//k/ancestor::*)"} {
		if _, err := eval(src, EvalContext{Tree: tree, MaxSteps: 500}); !errors.Is(err, ErrBudget) {
			t.Errorf("%s: %v", src, err)
		}
	}
}
