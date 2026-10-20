// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// One small schema exercises both kinds of ordering, keyed lists, choice, anydata,
// and operation input/output. Keep compilation outside fuzz iterations.
var m4FuzzSchema = sync.OnceValue(func() *yang.Schema {
	mods := fstest.MapFS{"m.yang": {Data: []byte(`module m {
  yang-version 1.1; namespace "urn:m"; prefix m;
  container c {
    must "count(l) >= 0";
    leaf s { type string; }
    list l { key k; ordered-by user; leaf k { type string; } leaf v { type string; } }
    leaf-list u { type string; ordered-by user; }
    leaf-list sys { type string; }
    choice pick { leaf x { type string; } leaf y { type string; } }
    anydata payload;
  }
  rpc ping {
    input {
      leaf a { type string; }
      leaf b { type string; default "d"; }
      leaf c { type string; default "e"; }
    }
    output { leaf answer { type string; } }
  }
}`)}}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, mods)
	if err == nil {
		_, err = c.Load("m", "", nil)
	}
	if err != nil {
		panic(err)
	}
	return c.Schema()
})

func m4JSON(t *testing.T, tr *data.Tree) string {
	t.Helper()
	var b strings.Builder
	if err := tr.PrintJSON(&b, data.PrintOptions{Shrink: true}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func m4Tree(t *testing.T, in string) *data.Tree {
	t.Helper()
	tr, diags, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON,
		m4FuzzSchema(), data.ParseOptions{ParseOnly: true, Unknown: data.Opaque,
			Budget: data.Budget{MaxBytes: 4096, MaxNodes: 32}})
	if err != nil {
		t.Fatalf("parse %s: %v %v", in, err, diags)
	}
	return tr
}

func m4Stable(t *testing.T, tr *data.Tree) {
	t.Helper()
	first := m4JSON(t, tr)
	if got := m4JSON(t, m4Tree(t, first)); got != first {
		t.Fatalf("print/parse/print changed: %s -> %s", first, got)
	}
}

func m4Input(a, b, order uint8) string {
	list := `[{"k":"a","v":"` + fmt.Sprint(a%4) + `"},{"k":"b","v":"` + fmt.Sprint(b%4) + `"}]`
	if order&1 != 0 {
		list = `[{"k":"b","v":"` + fmt.Sprint(b%4) + `"},{"k":"a","v":"` + fmt.Sprint(a%4) + `"}]`
	}
	ll := `["p","q"]`
	if order&2 != 0 {
		ll = `["q","p"]`
	}
	name := "x"
	if order&4 != 0 {
		name = "y"
	}
	// U-0043: anydata instances are not supported by Parse, so the schema
	// includes the node but the round-trip inputs do not instantiate it.
	return `{"m:c":{"s":"` + fmt.Sprint(a%4) + `","l":` + list + `,"u":` + ll +
		`,"sys":["a","b"],"` + name + `":"` + fmt.Sprint(b%4) + `"}}`
}

func m4Quoted(s string) string {
	// libyang's data strings follow XML Char even in JSON. Hex keeps every
	// fuzz byte represented while producing a valid schema string.
	b, _ := json.Marshal(hex.EncodeToString([]byte(s)))
	return string(b)
}

func FuzzM4DiffApply(f *testing.F) {
	for _, seed := range [][6]uint8{{0, 0, 0, 1, 2, 3}, {3, 2, 7, 0, 1, 0}, {1, 1, 2, 1, 1, 6}} {
		f.Add(seed[0], seed[1], seed[2], seed[3], seed[4], seed[5])
	}
	f.Fuzz(func(t *testing.T, a, b, orderA, c, d, orderB uint8) {
		first := m4Tree(t, m4Input(a, b, orderA))
		second := m4Tree(t, m4Input(c, d, orderB))
		before := m4JSON(t, first)
		want := m4JSON(t, second)
		diff, err := data.Diff(first, second, data.DiffOptions{})
		if err != nil {
			t.Fatalf("Diff of valid trees: %v", err)
		}
		if err := first.ApplyDiff(diff, data.ApplyDiffOptions{}); err != nil {
			t.Fatalf("ApplyDiff(%s -> %s): %v", before, want, err)
		}
		if got := m4JSON(t, first); got != want {
			t.Fatalf("ApplyDiff(%s -> %s) = %s", before, want, got)
		}
		m4Stable(t, first)
	})
}

func FuzzM4Merge(f *testing.F) {
	f.Add(uint8(0), uint8(1), uint8(0))
	f.Add(uint8(2), uint8(3), uint8(7))
	f.Fuzz(func(t *testing.T, a, b, order uint8) {
		dst := m4Tree(t, m4Input(a, b, order))
		src := m4Tree(t, m4Input(b, a, order^3))
		before := m4JSON(t, src)
		if err := dst.Merge(src); err != nil {
			t.Fatalf("Merge: %v", err)
		}
		if got := m4JSON(t, src); got != before {
			t.Fatalf("Merge changed source: %s -> %s", before, got)
		}
		m4Stable(t, dst)
	})
}

func FuzzM4TrimXPath(f *testing.F) {
	for _, expr := range []string{"/m:c/l[k='a']", "/m:c/u[.='q']", "/m:c/x", "//.", "["} {
		f.Add(expr)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		if len(expr) > 128 {
			return
		}
		tr := m4Tree(t, m4Input(1, 2, 0))
		before := m4JSON(t, tr)
		_, err := tr.TrimXPath(expr, data.XPathOptions{})
		if err != nil {
			if got := m4JSON(t, tr); got != before {
				t.Fatalf("TrimXPath error %v changed tree: %s -> %s", err, before, got)
			}
			return // invalid XPath and documented LY_EEXIST are ordinary errors
		}
		m4Stable(t, tr)
	})
}

func FuzzM4NewPath(f *testing.F) {
	for _, path := range []string{"/m:c/s", "/m:c/l[k='a']/v", "/m:c/u", "/m:c/sys", "/m:c/x", "/m:c/y", "/m:c/bad"} {
		f.Add(path, "v")
	}
	f.Fuzz(func(t *testing.T, path, value string) {
		if len(path) > 128 || len(value) > 64 {
			return
		}
		tr := data.NewTree(m4FuzzSchema())
		_, err := tr.NewPath(path, value, data.NewPathOptions{})
		if err != nil {
			return // malformed paths and invalid values are documented validation errors
		}
		m4Stable(t, tr)
	})
}

func FuzzM4Dup(f *testing.F) {
	for _, pick := range []uint8{0, 1, 2, 3, 4, 5} {
		f.Add(pick)
	}
	f.Fuzz(func(t *testing.T, pick uint8) {
		src := m4Tree(t, m4Input(pick, pick+1, pick))
		paths := []string{"/m:c", "/m:c/s", "/m:c/l[k='a']", "/m:c/l[k='b']/v", "/m:c/u[.='p']", "/m:c/sys[.='a']"}
		n, err := src.Find(paths[int(pick)%len(paths)])
		if err != nil || n == nil {
			t.Fatalf("Find: %v", err)
		}
		dst := data.NewTree(m4FuzzSchema())
		dup, diags, err := dst.Dup(n, nil, data.DupOptions{Recursive: true})
		if err != nil {
			t.Fatalf("Dup: %v %v", err, diags)
		}
		if !dup.Equal(n, data.CompareOptions{FullRecursion: true}) {
			t.Fatalf("Dup(%s) is unequal", paths[int(pick)%len(paths)])
		}
		m4Stable(t, dst)
	})
}

func FuzzM4Insert(f *testing.F) {
	for _, value := range []string{"x", "", "abc"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 64 {
			return
		}
		tr := m4Tree(t, `{"m:c":{"s":`+m4Quoted(value)+`},"m:unknown":{"inner":{"v":`+m4Quoted(value)+`}}}`)
		before := m4JSON(t, tr)
		var ancestor *data.Node
		for n := range tr.Top() {
			if n.Name() == "unknown" {
				ancestor = n
			}
		}
		if ancestor == nil {
			t.Fatal("opaque ancestor missing")
		}
		var descendant *data.Node
		for n := range ancestor.Children() {
			descendant = n
		}
		if descendant == nil {
			t.Fatal("opaque descendant missing")
		}
		if err := tr.Insert(descendant, ancestor); err == nil || !strings.Contains(err.Error(), "destination is inside it") {
			t.Fatalf("ancestor insertion: %v", err)
		}
		if got := m4JSON(t, tr); got != before {
			t.Fatalf("failed Insert changed tree: %s -> %s", before, got)
		}
		m4Stable(t, tr)
		other := m4Tree(t, m4Input(1, 2, 0))
		otherBefore := m4JSON(t, other)
		if err := other.Insert(descendant, ancestor); err == nil {
			t.Fatal("foreign parent accepted")
		}
		if m4JSON(t, tr) != before || m4JSON(t, other) != otherBefore {
			t.Fatal("failed cross-tree Insert changed an input tree")
		}
	})
}

func FuzzM4ParseOp(f *testing.F) {
	for _, value := range []string{"x", "", "abc"} {
		f.Add(value, uint8(0))
		f.Add(value, uint8(1))
		f.Add(value, uint8(2))
	}
	f.Fuzz(func(t *testing.T, value string, limit uint8) {
		if len(value) > 64 {
			return
		}
		in := `{"m:ping":{"a":` + m4Quoted(value) + `}}`
		budget := data.Budget{MaxBytes: 256, MaxNodes: 8}
		switch limit % 3 {
		case 1:
			budget.MaxBytes = 8
		case 2:
			budget.MaxNodes = 1
		}
		res, diags, err := data.ParseOp(context.Background(), strings.NewReader(in), data.FormatJSON,
			m4FuzzSchema(), data.OpRPC, data.ParseOpOptions{Budget: budget})
		if limit%3 != 0 {
			if !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("small ParseOp budget: %v %v", err, diags)
			}
			return
		}
		if err != nil {
			t.Fatalf("ParseOp: %v %v", err, diags)
		}
		first := m4JSON(t, res.Tree)
		again, _, err := data.ParseOp(context.Background(), strings.NewReader(first), data.FormatJSON,
			m4FuzzSchema(), data.OpRPC, data.ParseOpOptions{})
		if err != nil || m4JSON(t, again.Tree) != first {
			t.Fatalf("ParseOp print/parse/print: %v %s", err, first)
		}
	})
}

// FuzzM4Budgets checks the caller-set limits that participate in the M4 flows.
// Exhaustion must be reported as ErrBudget, never as a missing node or mismatch.
func FuzzM4Budgets(f *testing.F) {
	for _, limit := range []uint8{0, 1, 2, 3} {
		f.Add(limit, uint8(0))
		f.Add(limit, uint8(7))
	}
	f.Fuzz(func(t *testing.T, limit, value uint8) {
		in := m4Input(value, value+1, value)
		budget := data.Budget{MaxBytes: 4096, MaxNodes: 32, MaxXPathSteps: 4096}
		switch limit % 4 {
		case 1:
			budget.MaxBytes = 8
		case 2:
			budget.MaxNodes = 1
		case 3:
			budget.MaxXPathSteps = 1
		}
		tr, diags, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON,
			m4FuzzSchema(), data.ParseOptions{Budget: budget})
		if limit%4 != 0 {
			if !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("small Parse budget: %v %v", err, diags)
			}
		} else {
			if err != nil {
				t.Fatalf("Parse: %v %v", err, diags)
			}
			m4Stable(t, tr)
		}

		// ValidateOp has its own implicit-node budget. The two default leaves
		// exceed MaxNodes=1; even then the input operation must remain printable.
		op, _, err := data.ParseOp(context.Background(), strings.NewReader(`{"m:ping":{"a":"x"}}`),
			data.FormatJSON, m4FuzzSchema(), data.OpRPC, data.ParseOpOptions{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = op.Tree.ValidateOp(context.Background(), data.OpRPC,
			data.ValidateOpOptions{Budget: data.Budget{MaxNodes: 1}})
		if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("small ValidateOp budget: %v", err)
		}
		printed := m4JSON(t, op.Tree)
		again, _, err := data.ParseOp(context.Background(), strings.NewReader(printed),
			data.FormatJSON, m4FuzzSchema(), data.OpRPC, data.ParseOpOptions{})
		if err != nil || m4JSON(t, again.Tree) != printed {
			t.Fatalf("ValidateOp budget left invalid operation: %v %s", err, printed)
		}
	})
}
