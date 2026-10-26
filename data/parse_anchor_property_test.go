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
)

// TestParseDeferredRunAnchors parses random inputs where schema siblings declared before and
// after a system-ordered leaf-list and list arrive interleaved with and after their shuffled
// instances (rpc input, action input, plain container; JSON and XML). Every parsed node must
// survive exactly once, the runs must be sorted and the siblings in schema order. The native
// comparison of these shapes is in the ledger (R15); this pins the invariants without an oracle.
func TestParseDeferredRunAnchors(t *testing.T) {
	set := pv2Set(t, "pv2-sort-anchor")
	const ns = "urn:vibe-ports:yang:conformance:pv2-sort-anchor"
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // a fixed, reproducible shuffle
	type item struct {
		name string
		val  int
	}
	for i := range 3000 {
		var items []item
		if rng.IntN(2) == 0 {
			items = append(items, item{"pre", 0})
		}
		if rng.IntN(2) == 0 {
			items = append(items, item{"post", 0})
		}
		for range rng.IntN(12) {
			items = append(items, item{"v", rng.IntN(10)})
		}
		for range rng.IntN(8) {
			items = append(items, item{"l", rng.IntN(250)})
		}
		rng.Shuffle(len(items), func(a, b int) { items[a], items[b] = items[b], items[a] })
		kind, xml := i%3, i%2 == 1
		var body strings.Builder
		for j, it := range items {
			switch {
			case xml && it.name == "l":
				fmt.Fprintf(&body, "<l><k>%d</k></l>", it.val)
			case xml && it.name == "v":
				fmt.Fprintf(&body, "<v>%d</v>", it.val)
			case xml:
				fmt.Fprintf(&body, "<%s>x</%s>", it.name, it.name)
			default:
				if j > 0 {
					body.WriteByte(',')
				}
				switch it.name { // one JSON member per item: a run split into many members
				case "l":
					fmt.Fprintf(&body, `"l":[{"k":%d}]`, it.val)
				case "v":
					fmt.Fprintf(&body, `"v":[%d]`, it.val)
				default:
					fmt.Fprintf(&body, `"%s":"x"`, it.name)
				}
			}
		}
		var in string
		switch {
		case xml && kind == 0:
			in = `<r xmlns="` + ns + `">` + body.String() + `</r>`
		case xml && kind == 1:
			in = `<c xmlns="` + ns + `"><act>` + body.String() + `</act></c>`
		case xml:
			in = `<c xmlns="` + ns + `">` + body.String() + `</c>`
		case kind == 0:
			in = `{"pv2-sort-anchor:r":{` + body.String() + `}}`
		case kind == 1:
			in = `{"pv2-sort-anchor:c":{"act":{` + body.String() + `}}}`
		default:
			in = `{"pv2-sort-anchor:c":{` + body.String() + `}}`
		}
		f, fp := FormatJSON, parseJSON
		if xml {
			f, fp = FormatXML, parseXML
		}
		var tr *Tree
		var parent *Node
		if kind == 2 {
			parsed, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
				parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: true}}, fp, nil)
			if err != nil {
				t.Fatal(in, err, diags)
			}
			tr = parsed
			for n := range tr.Top() {
				parent = n
			}
		} else {
			parsed, op, diags, err := parseOp(context.Background(), strings.NewReader(in), set, f, opRPC, nil, Reject)
			if err != nil {
				t.Fatal(in, err, diags)
			}
			tr = parsed
			parent = op
		}
		opidxAssertTreeInvariant(t, tr)
		var gotNames, wantNames []string
		var gotV, wantV, gotL, wantL []int
		for _, it := range items {
			if !slices.Contains(wantNames, it.name) {
				wantNames = append(wantNames, it.name)
			}
			switch it.name {
			case "v":
				wantV = append(wantV, it.val)
			case "l":
				wantL = append(wantL, it.val) // parse-only keeps duplicate instances
			}
		}
		for _, n := range parent.kids.SchemaAll() {
			if len(gotNames) == 0 || gotNames[len(gotNames)-1] != n.schema.Name {
				gotNames = append(gotNames, n.schema.Name)
			}
			switch n.schema.Name {
			case "v":
				x, _ := strconv.Atoi(n.value.Canonical())
				gotV = append(gotV, x)
			case "l":
				x, _ := strconv.Atoi(n.kids.At(0).value.Canonical())
				gotL = append(gotL, x)
			}
		}
		order := []string{"pre", "v", "l", "post"}
		slices.SortFunc(wantNames, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) })
		slices.Sort(wantV)
		slices.Sort(wantL)
		if !slices.Equal(gotNames, wantNames) || !slices.Equal(gotV, wantV) || !slices.Equal(gotL, wantL) {
			t.Fatalf("%s:\n got %v v=%v l=%v\nwant %v v=%v l=%v", in, gotNames, gotV, gotL, wantNames, wantV, wantL)
		}
	}
}
