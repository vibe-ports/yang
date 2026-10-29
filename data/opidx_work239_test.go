// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func opidxParseWork(t *testing.T, set *schema.Set, input string, fp formatParser) (*Tree, *Node) {
	t.Helper()
	var lc *lydCtx
	o := parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: true}}
	tr, diags, err := parseWith(context.Background(), strings.NewReader(input), set, o, fp, func(c *lydCtx) {
		lc = c
		c.op = opOpts{rpc: true, action: true, noSiblings: true}
		c.rcOnly = true
		c.tree.work.visits = true
	})
	if err != nil {
		t.Fatal(err, diags)
	}
	return tr, lc.opNode
}

func opidxEvenOdd(n int, emit func(int)) {
	for i := 0; i < n; i += 2 {
		emit(i)
	}
	for i := 1; i < n; i += 2 {
		emit(i)
	}
}

func opidxEvenOddJSON(n int) string {
	var values, lists strings.Builder
	first := true
	opidxEvenOdd(n, func(v int) {
		if !first {
			values.WriteByte(',')
			lists.WriteByte(',')
		}
		first = false
		fmt.Fprint(&values, v)
		fmt.Fprintf(&lists, `{"k":%d}`, v)
	})
	return `{"m:r":{"v":[` + values.String() + `],"l":[` + lists.String() + `]}}`
}

func opidxEvenOddXML(n int) string {
	var b strings.Builder
	b.WriteString(`<r xmlns="urn:m">`)
	opidxEvenOdd(n, func(v int) { fmt.Fprintf(&b, "<v>%d</v>", v) })
	opidxEvenOdd(n, func(v int) { fmt.Fprintf(&b, "<l><k>%d</k></l>", v) })
	b.WriteString(`</r>`)
	return b.String()
}

func opidxAssertSortedInput(t *testing.T, op *Node, vs, ls *schema.Node, n int) {
	t.Helper()
	vi, li := 0, 0
	for child := range op.kids.all() {
		switch child.schema {
		case vs:
			if got := child.value.Canonical(); got != strconv.Itoa(vi) {
				t.Fatalf("v[%d] = %s", vi, got)
			}
			vi++
		case ls:
			if child.kids.SchemaLen() == 0 {
				t.Fatalf("l[%d] has no key", li)
			}
			if got := child.kids.At(0).value.Canonical(); got != strconv.Itoa(li) {
				t.Fatalf("l[%d].k = %s", li, got)
			}
			li++
		}
	}
	if vi != n || li != n {
		t.Fatalf("instances v=%d l=%d, want %d each", vi, li, n)
	}
}

// TestOpIndexEvenOddInput covers JSON and XML parsing of two interleaved sorted subsequences.
// The counter includes comparisons, sibling scans, and slice shifts/copies; libyang's lyds tree
// keeps system-ordered instances sorted and uses stable insertion for equal comparisons.
func TestOpIndexEvenOddInput(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r { input {
    leaf-list v { type uint32; }
    list l { key k; leaf k { type uint32; } }
  } }
}`)
	mod := opIndexModule(t, set)
	rpc := mod.Child(nil, "r")
	vs := schema.FindChild(rpc, nil, mod, "v", 0)
	ls := schema.FindChild(rpc, nil, mod, "l", 0)
	if vs == nil || ls == nil {
		t.Fatal("input schemas not compiled")
	}
	for _, tc := range []struct {
		name  string
		fp    formatParser
		input func(int) string
	}{
		{name: "json", fp: parseJSON, input: opidxEvenOddJSON},
		{name: "xml", fp: parseXML, input: opidxEvenOddXML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opIndexScaling(t, opIndexSizes, func(n int) int64 {
				tr, op := opidxParseWork(t, set, tc.input(n), tc.fp)
				opidxAssertSortedInput(t, op, vs, ls, n)
				return tr.work.Load()
			})
		})
	}
}

func opidxAscendingValues(n, value int) string {
	var b strings.Builder
	for i := range n {
		if i != 0 {
			b.WriteByte(',')
		}
		v := i
		if value >= 0 {
			v = value
		}
		fmt.Fprint(&b, v)
	}
	return b.String()
}

func opidxRemovalTree(t *testing.T, set *schema.Set, mode string, n int) (*Tree, *Node, *schema.Node) {
	t.Helper()
	switch mode {
	case "input":
		tr, op := opidxParseWork(t, set, `{"m:r":{"iv":[`+opidxAscendingValues(n, -1)+`]}}`, parseJSON)
		return tr, op, schema.FindChild(op.schema, nil, op.schema.Module, "iv", 0)
	case "output", "duplicates":
		tr, op := opidxParseWork(t, set, `{"m:r":{"keep":"x"}}`, parseJSON)
		name, value := "ov", -1
		if mode == "duplicates" {
			name, value = "dup", 1
		}
		reply := `{"m:` + name + `":[` + opidxAscendingValues(n, value) + `]}`
		if _, _, diags, err := parseOp(context.Background(), strings.NewReader(reply), set,
			FormatJSON, opReply, op, Reject); err != nil {
			t.Fatal(err, diags)
		}
		return tr, op, schema.FindChild(op.schema, nil, op.schema.Module, name, schema.GetNextOutput)
	default:
		t.Fatalf("unknown mode %q", mode)
		return nil, nil, nil
	}
}

func opidxFirstDocumentInstance(op *Node, sn *schema.Node) *Node {
	for n := range op.kids.all() {
		if n.schema == sn {
			return n
		}
	}
	return nil
}

// TestOpIndexRemoveEach covers single-node free/unlink in document and reverse-document order
// for an input leaf-list, a reply output leaf-list kept beside its input, and one duplicate-heavy
// output hash bucket. These orders remove list ends; repeatedly removing the middle is not covered.
// Work counts comparisons and scans as visits and all slice shifts/copies as shifts.
func TestOpIndexRemoveEach(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r {
    input {
      leaf keep { type string; }
      leaf-list iv { type uint32; }
    }
    output {
      leaf-list ov { type uint32; }
      leaf-list dup { type uint32; }
    }
  }
}`)
	for _, mode := range []string{"input", "output", "duplicates"} {
		for _, reverse := range []bool{false, true} {
			order := "document"
			if reverse {
				order = "reverse-document"
			}
			t.Run(mode+"/"+order, func(t *testing.T) {
				opIndexScaling(t, opIndexSizes, func(n int) int64 {
					tr, op, sn := opidxRemovalTree(t, set, mode, n)
					if sn == nil {
						t.Fatal("target schema not compiled")
					}
					nodes := opidxSchemaNodes(op, sn)
					if len(nodes) != n {
						t.Fatalf("instances %d, want %d", len(nodes), n)
					}
					tr.work.visits = true
					for i := range n {
						at := i
						if reverse {
							at = n - 1 - i
						}
						if err := freeTree(nodes[at]); err != nil {
							t.Fatal(err)
						}
						if i == n/2-1 {
							want := opidxFirstDocumentInstance(op, sn)
							if got := tr.schemaLookup(&op.kids, sn); got != want {
								t.Fatalf("midpoint first instance = %v, want %v", got, want)
							}
						}
					}
					if got := opidxSchemaNodes(op, sn); len(got) != 0 {
						t.Fatalf("%d instances remain", len(got))
					}
					if got := tr.schemaLookup(&op.kids, sn); got != nil {
						t.Fatalf("empty first-instance lookup = %v", got)
					}
					return tr.work.Load()
				})
			})
		}
	}
}
