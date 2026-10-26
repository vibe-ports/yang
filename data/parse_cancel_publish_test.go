// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// cancelAtContext reports cancellation from its at-th Err call on (never when at is 0).
type cancelAtContext struct {
	context.Context
	calls, at int
}

func (c *cancelAtContext) Err() error {
	c.calls++
	if c.at > 0 && c.calls >= c.at {
		return context.Canceled
	}
	return nil
}

// eachCancelPoint runs parse once to count its cancellation checks, then once cancelled at each.
func eachCancelPoint(t *testing.T, parse func(ctx *cancelAtContext)) {
	t.Helper()
	ctx := &cancelAtContext{Context: context.Background()}
	parse(ctx)
	total := ctx.calls
	if total == 0 {
		t.Fatal("no cancellation check")
	}
	for at := 1; at <= total; at++ {
		parse(&cancelAtContext{Context: context.Background(), at: at})
	}
}

// TestParseCancelPublishes cancels a parse at every cancellation check. libyang links each parsed
// instance into its RB tree at once (lyd_insert_node, tree_data.c:820-822), so whatever a failed
// parse leaves is in value order; the port publishes every deferred run before returning, also
// when cancelled, so the tree the parser built (an existing tree with ParseOp's Request) never
// keeps an unsorted run whose ordering state is gone. One more insertion keeps the order.
func TestParseCancelPublishes(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  leaf-list ll { type string; }
  leaf z { type string; }
}`)
	mod := opIndexModule(t, set)
	ll := schema.FindChild(nil, mod.Top, mod, "ll", 0)
	for _, tc := range []struct {
		name string
		fp   formatParser
		in   string
	}{
		{"json", parseJSON, `{"m:ll":["3","1","5","2","4"],"m:z":"x"}`},
		{"xml", parseXML, `<ll xmlns="urn:m">3</ll><ll xmlns="urn:m">1</ll><ll xmlns="urn:m">5</ll>` +
			`<ll xmlns="urn:m">2</ll><ll xmlns="urn:m">4</ll><z xmlns="urn:m">x</z>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eachCancelPoint(t, func(ctx *cancelAtContext) {
				var lc *lydCtx
				_, _, err := parseWith(ctx, strings.NewReader(tc.in), set,
					parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, tc.fp, func(l *lydCtx) { lc = l })
				run := func() []string {
					var vals []string
					for n := range lc.tree.Top() {
						if n.schema == ll {
							vals = append(vals, n.value.Canonical())
						}
					}
					return vals
				}
				if got := run(); len(got) == 0 || !slices.IsSorted(got) {
					t.Fatalf("cancelled at check %d (%v): run %v", ctx.at, err, got)
				}
				v, d := types.Store(ll.Type, "25", types.FormatJSON, types.JSONHints("string"), nil, ll)
				if d != nil {
					t.Fatal(d.Msg)
				}
				mustLink(t, lc.tree.insert(nil, newTerm(ll, v), insertDefault))
				if got := run(); !slices.IsSorted(got) {
					t.Fatalf("cancelled at check %d, then 25 inserted: run %v", ctx.at, got)
				}
			})
		})
	}
}

// TestParseReplyCancelRequest cancels a reply parse into a request at every cancellation check.
// Output leaf-lists are ordered by user in libyang (lys_compile_node_flags sets LYS_ORDBY_USER
// for output, notification and state nodes, schema_compile_node.c:2565-2567), so there is no
// deferred run: what lyd_parse_op's
// cleanup leaves keeps the input order, and an insertion goes last.
func TestParseReplyCancelRequest(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r { input { leaf in { type string; } } output { leaf-list v { type uint8; } } }
}`)
	mod := opIndexModule(t, set)
	v := schema.FindChild(mod.Child(nil, "r"), nil, mod, "v", schema.GetNextOutput)
	for _, tc := range []struct {
		name  string
		f     Format
		reply string
	}{
		{"json", FormatJSON, `{"m:v":[3,1,2]}`},
		{"xml", FormatXML, `<v xmlns="urn:m">3</v><v xmlns="urn:m">1</v><v xmlns="urn:m">2</v>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eachCancelPoint(t, func(ctx *cancelAtContext) {
				tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"m:r":{"in":"x"}}`), set,
					FormatJSON, opRPC, nil, Reject)
				if err != nil {
					t.Fatal(err, diags)
				}
				_, _, _, err = parseOp(ctx, strings.NewReader(tc.reply), set, tc.f, opReply, op, Reject)
				run := func() []string {
					var vals []string
					for _, n := range op.kids.SchemaAll() {
						if n.schema == v {
							vals = append(vals, n.value.Canonical())
						}
					}
					return vals
				}
				got := run()
				if !slices.Equal(got, []string{"3", "1", "2"}[:len(got)]) {
					t.Fatalf("cancelled at check %d (%v): run %v, want an input-order prefix", ctx.at, err, got)
				}
				mustLink(t, tree.insert(op, opIndexTerm(t, v, 0), insertDefault))
				if got2 := run(); !slices.Equal(got2, append(got, "0")) {
					t.Fatalf("cancelled at check %d, then 0 inserted: run %v", ctx.at, got2)
				}
			})
		})
	}
}
