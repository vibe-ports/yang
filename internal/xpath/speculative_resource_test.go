// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"context"
	"errors"
	"testing"

	"github.com/vibe-ports/yang/internal/resource"
)

type speculativeBudgetValue struct {
	Value
	err   error
	calls *int
}

func (v speculativeBudgetValue) StringWithBudget(context.Context, func() error, func() error) (string, error) {
	*v.calls++
	return "", v.err
}

type speculativeValueNode struct {
	*tnode
	value Value
}

func (n speculativeValueNode) Value() Value { return n.value }

type speculativeProbe struct{}

func (speculativeProbe) Match(Node) (bool, error) { return false, nil }

type speculativeLookupSchema struct {
	SchemaNode
	calls *int
}

func (s *speculativeLookupSchema) PrepareInstance([]string, func(int64) error) (InstanceProbe, error) {
	*s.calls++
	return speculativeProbe{}, nil
}

type speculativeLookup struct {
	tree  []Node
	err   error
	calls *int
}

func (l speculativeLookup) LookupChild(sn SchemaNode, _ []string) ([]Node, bool) {
	for _, n := range l.tree {
		if sameSchema(n.Schema(), sn) {
			return []Node{n}, true
		}
	}
	return nil, true
}

func (l speculativeLookup) LookupPrepared(InstanceProbe) ([]Node, bool, error) {
	*l.calls++
	return nil, false, l.err
}

type speculativeInfo struct {
	tinfo
	lookup *speculativeLookupSchema
}

func (i speculativeInfo) TopLevel(module, name string) []SchemaNode {
	if name == i.lookup.Name() && (module == "" || module == i.lookup.Module()) {
		return []SchemaNode{i.lookup}
	}
	return i.tinfo.TopLevel(module, name)
}

// eval_name_test_try_compile_predicates owns ordinary failures from its tentative key value.
// Every evaluator entry point used by the data adapter must nevertheless preserve a structural
// resource error, including errors returned directly by a callback and errors left sticky by a
// printer or cast. The first real predicate removes the only row, so none of these failures can
// be rediscovered after fallback.
func TestSpeculativeDataCallbackResourceErrors(t *testing.T) {
	key1, key2 := leaf("k1", "present"), leaf("k2", "value")
	pair := keyed(list("pv2:pair", key1, key2), "k1", "k2")
	bad := leaf("pv2:bad", "bad")
	ref := leaf("pv2:ref", "ref")
	id := leaf("pv2:id", "pv2:two")
	id.val.ident = &Ident{Module: "pv2", Name: "two"}
	enum := leaf("pv2:enum", "one")
	enumValue := 1
	enum.val.enum = &enumValue
	bits := leaf("pv2:bits", "red")
	bits.val.bits = []string{"red"}
	lookup := leaf("pv2:lookup", "lookup")
	tree := top(pair, bad, ref, id, enum, bits, lookup)
	ref.sch.lref = bad.sch

	budgetErr := resource.New("test callback resource budget")
	printerCalls, prepareCalls, lookupCalls, derefCalls := 0, 0, 0, 0
	badNode := speculativeValueNode{tnode: bad, value: speculativeBudgetValue{
		Value: bad.val, err: budgetErr, calls: &printerCalls,
	}}
	for i, n := range tree {
		if n == bad {
			tree[i] = badNode
		}
	}
	lookupSchema := &speculativeLookupSchema{SchemaNode: lookup.sch, calls: &prepareCalls}
	info := speculativeInfo{tinfo: tinfo{tree: tree}, lookup: lookupSchema}

	tests := []struct {
		name, value string
		current     Node
		calls       *int
	}{
		{"deref", "deref(/pv2:ref)", nil, &derefCalls},
		{"derived-from", "concat(derived-from(/pv2:id,'pv2:one'),/pv2:bad)", nil, &printerCalls},
		{"re-match", "concat(re-match('x','x'),/pv2:bad)", nil, &printerCalls},
		{"enum-value", "concat(enum-value(/pv2:enum),/pv2:bad)", nil, &printerCalls},
		{"bit-is-set", "concat(bit-is-set(/pv2:bits,'red'),/pv2:bad)", nil, &printerCalls},
		{"current", "string(current())", badNode, &printerCalls},
		{"lookup", "/pv2:lookup", nil, &lookupCalls},
		{"printer", "/pv2:bad", nil, &printerCalls},
		{"cast", "number(/pv2:bad)", nil, &printerCalls},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := *tc.calls
			e, err := Compile("count(/pv2:pair[k1='absent'][k2="+tc.value+"])", jsonNS{"pv2": true})
			if err != nil {
				t.Fatal(err)
			}
			ec := EvalContext{Tree: tree, Current: tc.current, Schema: info,
				TreeLookup: speculativeLookup{tree: tree, err: budgetErr, calls: &lookupCalls},
				Deref: func(Node) ([]Node, error) {
					derefCalls++
					return nil, budgetErr
				}}
			if _, err := e.Eval(ec); !errors.Is(err, budgetErr) {
				t.Fatalf("error=%v; want structural resource error", err)
			}
			if *tc.calls == before {
				t.Fatal("resource callback was not reached speculatively")
			}
		})
	}
	if prepareCalls == 0 {
		t.Fatal("prepared lookup was not constructed")
	}
}
