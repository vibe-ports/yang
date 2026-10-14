// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

var opidxR10Sizes = []int{512, 1024, 2048, 4096}

func opidxR10MetaInput(values []int) (string, map[int][]string) {
	var input strings.Builder
	input.WriteString(`{"m:r":{`)
	want := make(map[int][]string)
	minimum := int(^uint(0) >> 1)
	for i, value := range values {
		if i != 0 {
			input.WriteByte(',')
		}
		fmt.Fprintf(&input, `"v":[%d],"@v":[{"m:a":"%d"}]`, value, i+1)
		minimum = min(minimum, value)
		want[minimum] = append(want[minimum], strconv.Itoa(i+1))
	}
	input.WriteString(`}}`)
	return input.String(), want
}

func opidxR10AssertMeta(t *testing.T, op *Node, sn *schema.Node, n int, want map[int][]string) {
	t.Helper()
	nodes := opidxSchemaNodes(op, sn)
	if len(nodes) != n {
		t.Fatalf("v instances = %d, want %d", len(nodes), n)
	}
	for value, node := range nodes {
		if got := node.value.Canonical(); got != strconv.Itoa(value) {
			t.Fatalf("v[%d] = %s, want %d", value, got, value)
		}
		got := make([]string, 0, len(node.meta))
		for _, meta := range node.meta {
			if meta.mod.Name != "m" || meta.name != "a" {
				t.Fatalf("v=%d metadata = %s:%s, want only m:a", value, meta.mod.Name, meta.name)
			}
			got = append(got, meta.value.Canonical())
		}
		if !slices.Equal(got, want[value]) {
			t.Fatalf("v=%d metadata = %v, want %v", value, got, want[value])
		}
	}
}

func TestOpIndexMetaInterleaved(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  import ietf-yang-metadata { prefix md; }
  md:annotation a { type string; }
  rpc r { input { leaf-list v { type uint32; } } }
}`)
	mod := opIndexModule(t, set)
	rpc := mod.Child(nil, "r")
	if rpc == nil {
		t.Fatal("rpc r not compiled")
	}
	vs := schema.FindChild(rpc, nil, mod, "v", 0)
	if vs == nil {
		t.Fatal("metadata-interleaved schema not compiled")
	}

	for _, order := range []string{"ascending", "descending", "random"} {
		t.Run("JSON/"+order, func(t *testing.T) {
			opIndexScaling(t, opidxR10Sizes, func(n int) int64 {
				values := make([]int, n)
				for i := range values {
					values[i] = i
				}
				switch order {
				case "descending":
					slices.Reverse(values)
				case "random":
					rng := rand.New(rand.NewPCG(0x241, 10)) //nolint:gosec // deterministic test data
					rng.Shuffle(n, func(i, j int) { values[i], values[j] = values[j], values[i] })
				}
				input, want := opidxR10MetaInput(values)
				tr, op := opIndexParseRPC(t, set, input)
				opidxR10AssertMeta(t, op, vs, n, want)
				return tr.work.Load()
			})
		})
	}

	t.Run("XML", func(t *testing.T) {
		opIndexScaling(t, opidxR10Sizes, func(n int) int64 {
			var input strings.Builder
			input.WriteString(`<r xmlns="urn:m" xmlns:m="urn:m">`)
			for i := n - 1; i >= 0; i-- {
				fmt.Fprintf(&input, `<v m:a="%d">%d</v>`, i+1, i)
			}
			input.WriteString(`</r>`)
			tr, _ := opidxParseWork(t, set, input.String(), parseXML)
			return tr.work.Load()
		})
	})
}

func TestOpIndexRehashMiddle(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r { input { leaf-list v { type uint32; ordered-by user; } } }
}`)
	opIndexScaling(t, opidxR10Sizes, func(n int) int64 {
		input := `{"m:r":{"v":[` + opIndexValues(n+2, false) + `]}}`
		tr, op := opIndexParseRPC(t, set, input)
		vs := schema.FindChild(op.schema, nil, op.schema.Module, "v", 0)
		if vs == nil {
			t.Fatal("user-ordered v schema not compiled")
		}
		nodes := opidxSchemaNodes(op, vs)
		if len(nodes) != n+2 {
			t.Fatalf("v instances = %d, want %d", len(nodes), n+2)
		}
		if op.kids.ht == nil {
			t.Fatal("operation has no children hash table")
		}

		tr.work.Store(0)
		tr.work.visits = true
		tr.work.shifts = true
		for i, node := range nodes[1 : n+1] {
			want := strconv.Itoa(n + 2 + i)
			if rc := tr.changeTerm(node, want); rc != "" {
				t.Fatalf("changeTerm v[%d]: %s", i+1, rc)
			}
			if got := node.value.Canonical(); got != want {
				t.Fatalf("changed v[%d] = %s, want %s", i+1, got, want)
			}
		}
		work := tr.work.Load()
		opidxCheckRecords(t, &op.kids)
		return work
	})
}

func TestOpIndexLookupStaleLeader(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r {
    input { leaf-list v { type uint32; ordered-by user; } }
    output { leaf o { type string; } }
  }
}`)
	mod := opIndexModule(t, set)
	opIndexScaling(t, opidxR10Sizes, func(n int) int64 {
		input := `{"m:r":{"v":[` + opIndexValues(n, false) + `]}}`
		tr, op := opIndexParseRPC(t, set, input)
		b := newBuilder{set: set, log: &logger{set: set}}
		add := func(name, value string, output bool) *Node {
			t.Helper()
			node, err := b.newTerm(op, mod, name, value, newValOptions{output: output})
			if err = b.log.done(err); err != nil {
				t.Fatal(err)
			}
			b.log = &logger{set: set}
			return node
		}
		o := add("o", "split", true)
		last := add("v", strconv.Itoa(n), false)
		vs := last.schema
		first := opidxSchemaNodes(op, vs)[0]
		if got, want := op.kids.records(vs), []*Node{first, last}; !slices.Equal(got, want) {
			t.Fatalf("split-run records = %v, want v0,v%d", opidxRecords(op, vs), n)
		}
		if err := freeTree(o); err != nil {
			t.Fatal(err)
		}
		if got, want := op.kids.records(vs), []*Node{first, last}; !slices.Equal(got, want) {
			t.Fatalf("joined-run records = %v, want v0,v%d", opidxRecords(op, vs), n)
		}
		if err := freeTree(first); err != nil {
			t.Fatal(err)
		}
		if records := op.kids.records(vs); len(records) == 0 || records[0] != last {
			t.Fatalf("record leader = %v, want v%d", opidxRecords(op, vs), n)
		}
		opidxCheckRecords(t, &op.kids)

		tr.work.Store(0)
		tr.work.visits = true
		tr.work.shifts = true
		for i := range n {
			if got := tr.schemaLookup(&op.kids, vs); got != last {
				t.Fatalf("lookup %d = %s, want v%d", i, opidxLabel(got), n)
			}
			count := 0
			for node := range tr.instances(&op.kids, vs) {
				if count != 0 || node != last {
					t.Fatalf("instance walk %d returned %s after %d nodes, want only v%d", i, opidxLabel(node), count, n)
				}
				count++
			}
			if count != 1 {
				t.Fatalf("instance walk %d returned %d nodes, want 1", i, count)
			}
		}
		return tr.work.Load()
	})
}

func TestOpIndexMetaSplitInterior(t *testing.T) {
	opIndexScaling(t, opidxR10Sizes, func(n int) int64 {
		block := n / 2
		var yang strings.Builder
		yang.WriteString(`module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  import ietf-yang-metadata { prefix md; }
  md:annotation a { type string; }
  rpc r { input {`)
		for i := range block {
			fmt.Fprintf(&yang, " leaf p%d { type string; }", i)
		}
		yang.WriteString(` leaf-list v { type uint32; ordered-by user; }`)
		for i := range block {
			fmt.Fprintf(&yang, " leaf s%d { type string; }", i)
		}
		yang.WriteString(` } output { leaf o { type string; } } } }`)
		set := opIndexSet(t, yang.String())
		mod := opIndexModule(t, set)
		rpc := mod.Child(nil, "r")
		if rpc == nil {
			t.Fatal("rpc r not compiled")
		}
		vs := schema.FindChild(rpc, nil, mod, "v", 0)
		os := schema.FindChild(rpc, nil, mod, "o", schema.GetNextOutput)
		if vs == nil || os == nil {
			t.Fatal("metadata split-interior schema not compiled")
		}
		tr, op := opIndexParseRPC(t, set, `{"m:r":{}}`)
		for i := range block {
			sn := schema.FindChild(rpc, nil, mod, fmt.Sprintf("p%d", i), 0)
			if sn == nil {
				t.Fatalf("prefix schema p%d not compiled", i)
			}
			tr.insert(op, opidxStringTerm(t, sn, "p"), insertLast)
		}
		first := opIndexTerm(t, vs, 1)
		tr.insert(op, first, insertLast)
		tr.insert(op, opidxStringTerm(t, os, "split"), insertLast)
		second := opIndexTerm(t, vs, 2)
		tr.insert(op, second, insertLast)
		for i := range block {
			sn := schema.FindChild(rpc, nil, mod, fmt.Sprintf("s%d", i), 0)
			if sn == nil {
				t.Fatalf("suffix schema s%d not compiled", i)
			}
			tr.insert(op, opidxStringTerm(t, sn, "s"), insertLast)
		}
		if got := opidxFirstDocumentInstance(op, vs); got != first {
			t.Fatalf("document-first v = %s, want v1", opidxLabel(got))
		}
		if got, want := op.kids.records(vs), []*Node{first, second}; !slices.Equal(got, want) {
			t.Fatalf("split-run records = %v, want v1,v2", opidxRecords(op, vs))
		}

		var metadata strings.Builder
		metadata.WriteByte('{')
		for i := range n {
			if i != 0 {
				metadata.WriteByte(',')
			}
			metadata.WriteString(`"@m:v":[null]`)
		}
		metadata.WriteByte('}')
		tr.work.Store(0)
		tr.work.visits = true
		tr.work.shifts = true
		if _, _, diags, err := parseOp(context.Background(), strings.NewReader(metadata.String()),
			set, FormatJSON, opRPC, op, Reject); err != nil {
			t.Fatal(err, diags)
		}
		work := tr.work.Load()
		if got := opidxFirstDocumentInstance(op, vs); got != first {
			t.Fatalf("document-first v after metadata = %s, want v1", opidxLabel(got))
		}
		if len(first.meta) != 0 {
			t.Fatalf("null metadata created %d annotations", len(first.meta))
		}
		return work
	})
}
