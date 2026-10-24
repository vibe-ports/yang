// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"strings"
	"testing"
)

func TestOpIndexDefaultBudgetAllows250KLinks(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("250k-node scaling regression runs in normal architecture suites")
	}
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { input { leaf-list v { type uint32; } } } }`)
	input := `{"m:r":{"v":[` + opIndexValues(250_000, false) + `]}}`
	tr, op, diags, err := parseOp(context.Background(), strings.NewReader(input), set,
		FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	if got := len(op.kids.list); got != 250_000 {
		t.Fatalf("linked operation children = %d, want 250000", got)
	}
	if tr.work.hashUsed >= defaultMaxHashWork {
		t.Fatalf("stored-hash work = %d, limit %d", tr.work.hashUsed, defaultMaxHashWork)
	}
}

func TestOpIndexInstancesRemainLazy(t *testing.T) {
	const n = 4096
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 rpc r { input { leaf-list v { type uint32; ordered-by user; } } } }`)
	tr, op := opIndexParseRPC(t, set, `{"m:r":{"v":[`+opIndexValues(n, false)+`]}}`)
	sn := op.kids.list[0].schema
	tr.work.Store(0)
	for i := range n {
		reached := false
		for got := range tr.instances(&op.kids, sn) {
			reached = true
			if got != op.kids.list[0] {
				t.Fatalf("instances read %d first node = %p, want %p", i, got, op.kids.list[0])
			}
			break
		}
		if !reached {
			t.Fatalf("instances read %d did not reach the operation run", i)
		}
	}
	if work := tr.work.Load(); work > 4*n {
		t.Fatalf("%d first-instance reads used %d work units; run was materialized", n, work)
	}
	tr.work.Store(0)
	for i := range n {
		reached := false
		for got := range tr.opRun(&op.kids, sn) {
			reached = true
			if got != op.kids.list[0] {
				t.Fatalf("opRun read %d first node = %p, want %p", i, got, op.kids.list[0])
			}
			break
		}
		if !reached {
			t.Fatalf("opRun read %d did not reach the operation run", i)
		}
	}
	if work := tr.work.Load(); work > 4*n {
		t.Fatalf("%d opRun first-instance reads used %d work units; run was materialized", n, work)
	}
}
