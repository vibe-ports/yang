// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
)

func whenPayloadInput(n int) string {
	var b strings.Builder
	b.WriteString(`{"m:a":{`)
	for i := range n {
		if i != 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"payload:n%d":"v"`, i)
	}
	b.WriteString(`}}`)
	return b.String()
}

func TestWhenOwnedPayloadPreflight(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m; anydata a { when "false()"; } }`)
	t.Run("cancelled", func(t *testing.T) {
		tr := parseWhenTrue(t, set, whenPayloadInput(512))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		vc := &valCtx{t: tr, budget: xpathBudget{ctx: ctx}}
		if err := (&whenPass{vc: vc}).del(tr.top.first()); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled ownership preflight: %v", err)
		}
	})
	t.Run("inside-payload", func(t *testing.T) {
		tr := parseWhenTrue(t, set, whenPayloadInput(512))
		ctx := &cancelAtContext{Context: context.Background(), at: 32}
		vc := &valCtx{t: tr, budget: xpathBudget{ctx: ctx}}
		if err := (&whenPass{vc: vc}).del(tr.top.first()); !errors.Is(err, context.Canceled) || ctx.calls != ctx.at {
			t.Fatalf("ownership cancellation: %v after %d checks", err, ctx.calls)
		}
	})
	t.Run("quota", func(t *testing.T) {
		tr := parseWhenTrue(t, set, whenPayloadInput(512))
		tr.work.hashUsed, tr.work.hashLimit = 0, 64
		vc := &valCtx{t: tr, budget: xpathBudget{ctx: context.Background()}}
		if err := (&whenPass{vc: vc}).del(tr.top.first()); !errors.Is(err, yang.ErrBudget) || tr.top.len() != 1 {
			t.Fatalf("ownership reservation: %v, roots=%d", err, tr.top.len())
		}
	})
	used := func(n int) uint64 {
		tr := parseWhenTrue(t, set, whenPayloadInput(n))
		if _, err := tr.ValidateModule(context.Background(), "m", ValidateOptions{NotFinal: true}); err != nil {
			t.Fatal(err)
		}
		if tr.top.len() != 0 {
			t.Fatal("anydata was not deleted")
		}
		return tr.work.hashUsed
	}
	small, large := used(512), used(4096)
	t.Logf("owned payload hash work: 512=%d, 4096=%d", small, large)
	if large < small+2*(4096-512) {
		t.Fatalf("ownership and cleanup visits uncharged: 512=%d, 4096=%d", small, large)
	}
}

// Sweep every preflight checkpoint through both diff APIs. Cancellation must
// leave the payload intact or publish its complete deletion and matching diff.
func TestWhenOwnedPayloadCancelDiff(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m; anydata a { when "false()"; } }`)
	for _, module := range []bool{false, true} {
		t.Run(strconv.FormatBool(module), func(t *testing.T) {
			eachCancelPoint(t, func(ctx *cancelAtContext) {
				tr, original := parseWhenTrue(t, set, whenPayloadInput(8)), parseWhenTrue(t, set, whenPayloadInput(8))
				var diff *Tree
				var err error
				if module {
					diff, _, err = tr.ValidateModuleDiff(ctx, "m", ValidateOptions{NotFinal: true})
				} else {
					diff, _, err = tr.ValidateDiff(ctx, ValidateOptions{NotFinal: true, Present: true})
				}
				if err != nil && !errors.Is(err, context.Canceled) {
					t.Fatalf("checkpoint %d: %v", ctx.at, err)
				}
				if diff != nil {
					if err := original.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				if got, want := dumpJSON(t, original), dumpJSON(t, tr); got != want {
					t.Fatalf("checkpoint %d: applied diff %s, retained tree %s", ctx.at, got, want)
				}
			})
		})
	}
}

// The gate follows the deleted run and precedes a retained run of equal size. The
// cached view remains at least half live, so geometric rebuilding cannot hide a
// repeated scan of the dead prefix. Exercise both schema and keyed root lookups.
func TestWhenDeletedPrefixLookupWork(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		t.Run(strconv.FormatBool(keyed), func(t *testing.T) {
			steps := func(n int) int64 {
				gate, expr, inputGate := `leaf gate { type string; }`, `/m:gate = 'on'`, `"m:gate":"on"`
				if keyed {
					gate, expr, inputGate = `list gate { key k; leaf k { type string; } leaf v { type string; } }`, `/m:gate[k='g']/v = 'on'`, `"m:gate":[{"k":"g","v":"on"}]`
				}
				set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
				  container c { leaf-list q { type uint32; when "`+expr+`"; } }
				  leaf-list x { type uint32; when "false()"; } `+gate+` leaf-list f { type uint32; } }`)
				vals := make([]string, n)
				for i := range vals {
					vals[i] = strconv.Itoa(i)
				}
				array := `[` + strings.Join(vals, ",") + `]`
				tr := parseWhenTrue(t, set, `{"m:c":{"q":`+array+`},"m:x":`+array+`,`+inputGate+`,"m:f":`+array+`}`)
				vc := &valCtx{t: tr, log: &logger{set: set}, nodeWhen: &nodeSet{}, nodeTypes: &nodeSet{}, metaTypes: &[]*meta{}, budget: xpathBudget{ctx: context.Background()}}
				for root := range tr.top.all() {
					for d := range root.All() {
						if len(d.schema.Whens) != 0 {
							vc.nodeWhen.add(d)
						}
					}
				}
				if err := vc.unres(); err != nil {
					t.Fatal(err)
				}
				if tr.top.len() != n+2 || tr.top.first().kids.len() != n {
					t.Fatal("unexpected retained tree")
				}
				return vc.budget.steps
			}
			small, large := steps(512), steps(4096)
			t.Logf("root lookup XPath work: 512=%d, 4096=%d", small, large)
			if large > 10*small {
				t.Fatalf("deleted-prefix amplification: 512=%d, 4096=%d", small, large)
			}
		})
	}
}

// Guard whens resolve one per pass. Each resolved guard then permits deletion
// of its already-WhenTrue sibling in the following pass. None changes the root
// list, so unrelated roots must be wrapped only once across all these passes.
func TestWhenChildPassesKeepRootView(t *testing.T) {
	for _, passes := range []int{4, 16} {
		t.Run(strconv.Itoa(passes), func(t *testing.T) {
			var model, input strings.Builder
			model.WriteString(`module m { yang-version 1.1; namespace "urn:m"; prefix m; container c { presence p;`)
			input.WriteString(`{"m:c":{`)
			for i := 1; i <= passes; i++ {
				guard := "true()"
				if i > 1 {
					guard = fmt.Sprintf("../g%d", i-1)
				}
				fmt.Fprintf(&model, `leaf g%d { type string; when "%s"; } leaf d%d { type string; when "not(../g%d)"; }`, i, guard, i, i)
				if i > 1 {
					input.WriteByte(',')
				}
				fmt.Fprintf(&input, `"d%d":"d"`, i)
			}
			model.WriteString(`} leaf-list f { type uint32; } }`)
			input.WriteString(`},"m:f":[`)
			for i := range 512 {
				if i > 0 {
					input.WriteByte(',')
				}
				input.WriteString(strconv.Itoa(i))
			}
			input.WriteString(`]}`)
			set := reviewDiffSet(t, model.String())
			tr := parseWhenTrue(t, set, input.String())
			parent := tr.top.first()
			for i := 1; i <= passes; i++ {
				if _, err := tr.NewTerm(parent, fmt.Sprintf("g%d", i), "v", NewOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			vc := &valCtx{t: tr, log: &logger{set: set}, nodeWhen: &nodeSet{}, nodeTypes: &nodeSet{}, metaTypes: &[]*meta{}, budget: xpathBudget{ctx: context.Background()}}
			for n := range parent.Children() {
				vc.nodeWhen.add(n)
			}
			if err := vc.unres(); err != nil {
				t.Fatal(err)
			}
			t.Logf("%d child deletions: root builds=%d, XPath work=%d", passes, vc.topBuilds, vc.budget.steps)
			if vc.topBuilds != 1 || parent.kids.len() != passes {
				t.Fatalf("%d passes: root builds=%d, retained children=%d", passes, vc.topBuilds, parent.kids.len())
			}
		})
	}
}

func TestWhenFinalViewRebuildBudget(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m; leaf-list x { type uint32; when "false()"; } }`)
	for _, limit := range []int64{9, 10, 11} {
		t.Run(strconv.FormatInt(limit, 10), func(t *testing.T) {
			tr := parseWhenTrue(t, set, `{"m:x":[0,1,2,3]}`)
			vc := &valCtx{t: tr, log: &logger{set: set}, nodeWhen: &nodeSet{}, nodeTypes: &nodeSet{}, metaTypes: &[]*meta{}, budget: xpathBudget{ctx: context.Background(), max: limit}}
			for n := range tr.top.all() {
				vc.nodeWhen.add(n)
			}
			err := vc.unres()
			if limit < 11 {
				if !errors.Is(err, yang.ErrBudget) || tr.top.len() != 1 || vc.budget.steps > limit {
					t.Fatalf("final rebuild: err=%v, roots=%d, steps=%d/%d", err, tr.top.len(), vc.budget.steps, limit)
				}
			} else if err != nil || tr.top.len() != 0 || vc.budget.steps != limit {
				t.Fatalf("exact allowance: err=%v, roots=%d, steps=%d/%d", err, tr.top.len(), vc.budget.steps, limit)
			}
		})
	}
}

// A query uses the root-view builder too, but cannot consume or poison a
// suspended mutation's shared work quota. Successor storage is deletion-only.
func TestWhenViewQueryNoSharedWork(t *testing.T) {
	set := reviewDiffSet(t, `module m { namespace "urn:m"; prefix m; leaf x { type string; } }`)
	tr := parseWhenTrue(t, set, `{"m:x":"v"}`)
	tr.work.hashUsed, tr.work.hashLimit = 0, 1
	for range 3 {
		nodes, _, err := tr.FindXPath("/m:x", XPathOptions{})
		if err != nil || len(nodes) != 1 || tr.work.hashUsed != 0 || tr.work.hashErr != nil {
			t.Fatalf("query mutated shared quota: err=%v, nodes=%d, work=%d, sticky=%v", err, len(nodes), tr.work.hashUsed, tr.work.hashErr)
		}
	}
}

func TestWhenIgnoredDiffRealFailure(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	  leaf-list x { config false; type string; when "false()"; }
	  leaf target { type string; }
	  leaf r { type leafref { path "/m:target"; } }
	  leaf bad { type string; when "false()"; }
	  leaf must { type string; must "false()"; } }`)
	for _, module := range []bool{false, true} {
		for _, failure := range []string{"when-leafref", "when", "leafref", "must"} {
			t.Run(fmt.Sprintf("module=%v/%s", module, failure), func(t *testing.T) {
				input := `{"m:x":["a","a","b"],"m:r":"missing"}`
				switch failure {
				case "must":
					input = `{"m:x":["a","a","b"],"m:must":"v"}`
				case "when":
					input = `{"m:x":["a","a","b"]}`
				}
				tr := parseWhenTrue(t, set, input)
				if strings.HasPrefix(failure, "when") {
					if _, err := tr.NewTerm(nil, "m:bad", "bad", NewOptions{}); err != nil {
						t.Fatal(err)
					}
				}
				o := ValidateOptions{MultiError: strings.HasPrefix(failure, "when"), NotFinal: failure != "must", Present: !module}
				var diags []yang.Diagnostic
				var err error
				if module {
					_, diags, err = tr.ValidateModuleDiff(context.Background(), "m", o)
				} else {
					_, diags, err = tr.ValidateDiff(context.Background(), o)
				}
				var ve *ValidationError
				if !errors.As(err, &ve) || ve.RC() != "LY_EVALID" {
					t.Errorf("actual validation failure lost: %v, diagnostics=%v", err, diags)
				}
				want := "instance-required"
				switch failure {
				case "must":
					want = "must-violation"
				case "when":
					if len(diags) != 3 || diags[0].DataPath != "/m:bad" || diags[2].Err != "LY_EOTHER" {
						t.Fatalf("when and diff diagnostics: %v", diags)
					}
					return
				}
				found := false
				for _, d := range diags {
					found = found || d.AppTag == want
				}
				if !found {
					t.Fatalf("missing %s after ignored diff error: %v", want, diags)
				}
			})
		}
	}
}
