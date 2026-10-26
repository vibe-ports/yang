// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

func TestValidationCancelAfterEntry(t *testing.T) {
	set := opIndexSet(t, cancelValidationModel)
	for _, nested := range []bool{false, true} {
		for _, multi := range []bool{false, true} {
			t.Run(fmt.Sprintf("nested=%t/multi=%t", nested, multi), func(t *testing.T) {
				tr := cancelValidationTree(t, set, 4096, nested)
				base, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx := &cancelAfterErrContext{Context: base, cancel: cancel}
				ds, err := tr.ValidateModuleFinal(ctx, "m", ValidateOptions{MultiError: multi})
				if !errors.Is(err, context.Canceled) || len(ds) != 0 {
					t.Fatalf("cancellation after successful entry check: %v %v", ds, err)
				}
			})
		}
	}
}

// Later checkpoints require polling inside the wide traversal, beyond entry and
// module setup. NotFinal must still poll the new-node and type-validation passes.
func TestValidationCancelDuringTraversal(t *testing.T) {
	set := opIndexSet(t, cancelValidationModel)
	for _, nested := range []bool{false, true} {
		for _, api := range []string{"final", "all", "diff", "module", "module-diff"} {
			for _, at := range []int{32, 2048} {
				t.Run(fmt.Sprintf("nested=%t/%s/at=%d", nested, api, at), func(t *testing.T) {
					tr := cancelValidationTree(t, set, 4096, nested)
					ctx := &cancelAtContext{Context: context.Background(), at: at}
					o := ValidateOptions{NotFinal: true, MultiError: true}
					var ds []yang.Diagnostic
					var err error
					switch api {
					case "final":
						ds, err = tr.ValidateModuleFinal(ctx, "m", ValidateOptions{MultiError: true})
					case "all":
						ds, err = tr.Validate(ctx, o)
					case "diff":
						_, ds, err = tr.ValidateDiff(ctx, o)
					case "module":
						ds, err = tr.ValidateModule(ctx, "m", o)
					case "module-diff":
						_, ds, err = tr.ValidateModuleDiff(ctx, "m", o)
					}
					if !errors.Is(err, context.Canceled) || len(ds) != 0 || ctx.calls != at {
						t.Fatalf("checkpoint %d: polls=%d, diagnostics=%v, error=%v", at, ctx.calls, ds, err)
					}
					if tr.work.journal != nil {
						t.Fatal("cancellation retained the mutation journal")
					}
					if ds, err := tr.ValidateModule(context.Background(), "m", ValidateOptions{}); err != nil || len(ds) != 0 {
						t.Fatal("retry after cancellation:", ds, err)
					}
				})
			}
		}
	}
}

// Exercise scans directly so unrelated entry or traversal polls cannot hide a
// missing checkpoint. Each operation must reach at least 32 iterations itself.
func TestValidationCancelScans(t *testing.T) {
	set := opIndexSet(t, cancelValidationModel)
	for _, name := range []string{"final", "final-descent", "minmax", "unique-gather", "new", "implicit-recursion", "module-scan", "module-start", "module-insert", "present-modules", "default-flags", "default-instances", "default-filter", "explicit-scan", "case-data", "duplicate-index", "meta-owners", "autodel-diff", "drop-types"} {
		t.Run(name, func(t *testing.T) {
			tr := cancelValidationTree(t, set, 512, false)
			ctx := &cancelAtContext{Context: context.Background(), at: 32}
			vc := &valCtx{t: tr, log: &logger{set: set}, opts: ValidateOptions{Present: true, MultiError: true},
				budget: xpathBudget{ctx: ctx}}
			sn := tr.top.At(0).schema
			var err error
			switch name {
			case "final", "final-descent":
				if name == "final-descent" {
					ctx.at += tr.top.SchemaLen() // finish the first sibling pass
				}
				err = vc.finalR(nil, tr.top.schemaSpan(), nil, set.Implemented("m"))
			case "minmax":
				err = vc.minmax(nil, &tr.top, sn)
			case "unique-gather":
				err = vc.unique(&tr.top, sn)
			case "new":
				err = vc.validateNew(nil, nil, nil)
			case "implicit-recursion":
				err = vc.newImplicitR(nil, nil, set.Implemented("m"), 0)
			case "module-scan":
				_, err = vc.firstModuleSibling(tr.top.schemaSpan(), tr.top.SchemaLen()-1, set.Implemented("m"))
			case "module-start":
				vc.modFirst = tr.top.At(tr.top.SchemaLen() - 1)
				_, err = vc.modStart()
			case "module-insert":
				vc.modFirst = tr.top.At(tr.top.SchemaLen() - 1)
				err = vc.modInsert(newTerm(sn, tr.top.At(0).value, &tr.work))
			case "present-modules":
				_, err = vc.modules()
			case "default-flags":
				tr = cancelValidationTree(t, set, 512, true)
				vc.t = tr
				for _, n := range tr.top.At(0).kids.SchemaAll() {
					n.flags |= FlagDefault
				}
				err = vc.setContainerDefault(tr.top.At(0))
			case "default-instances":
				_, err = vc.instances(&tr.top, sn)
			case "default-filter":
				_, err = vc.autodelDefaults(tr.top.schemaSpan().Collect(), tr.top.At(0))
			case "explicit-scan":
				for _, n := range tr.top.SchemaAll() {
					n.flags |= FlagDefault
				}
				_, err = vc.hasExplicit(tr.top.schemaSpan().Collect())
			case "case-data":
				err = vc.getnextData(&tr.top, &schema.Node{Kind: schema.Case, Children: []*schema.Node{sn}}, func(*Node) bool { return true })
			case "duplicate-index":
				sib := tr.top
				sib.ht = nil // the native no-children-table branch
				d := dupIndex{sib: &sib}
				_, err = d.bucket(idxKey{}, &tr.work, vc.checkContext)
			case "meta-owners":
				_, err = vc.metaOwners([]*meta{{}})
			case "autodel-diff":
				ctx.at = 2 // cancellation after emission must wait for the unlink
				vc.diff = func(*Node, diffOp) error { return nil }
				err = vc.autodel(slices.Clone(tr.top.schemaSpan().Collect()), false)
				if tr.top.SchemaLen() != 0 {
					t.Fatal("cancellation left emitted removals unapplied")
				}
			case "drop-types":
				tr = cancelValidationTree(t, set, 512, true)
				vc.t = tr
				vc.nodeTypes = &nodeSet{}
				vc.nodeTypes.add(tr.top.At(0).kids.At(0))
				err = vc.dropTypes(tr.top.At(0), true)
			}
			if !errors.Is(err, context.Canceled) || ctx.calls != ctx.at {
				t.Fatalf("scan: polls=%d, error=%v", ctx.calls, err)
			}
		})
	}
}

func TestValidationCancelAutodelDiffConsistency(t *testing.T) {
	set := opIndexSet(t, cancelValidationModel)
	tr := cancelValidationTree(t, set, 512, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vc := &valCtx{t: tr, log: &logger{set: set}, budget: xpathBudget{ctx: ctx}}
	emitted := 0
	vc.diff = func(*Node, diffOp) error {
		emitted++
		if emitted == 32 {
			cancel()
		}
		return nil
	}
	journal := beginHashTransaction(tr)
	err := vc.autodel(slices.Clone(tr.top.schemaSpan().Collect()), false)
	journal.end(nil)
	if !errors.Is(err, context.Canceled) || emitted != 512 || tr.top.len() != 0 {
		t.Fatalf("canceled removal: diff entries=%d retained=%d error=%v", emitted, tr.top.len(), err)
	}
}

func TestValidationCancelSchemaScans(t *testing.T) {
	var model strings.Builder
	model.WriteString(`module m { yang-version 1.1; namespace "urn:cancel-schema"; prefix m; container r {`)
	for i := range 128 {
		fmt.Fprintf(&model, `leaf v%d { type string; } choice c%d { case a { leaf a%d { type string; } } }`, i, i, i)
	}
	model.WriteString(`} }`)
	set := opIndexSet(t, model.String())
	parent := set.Implemented("m").Top[0]
	for _, name := range []string{"cache-build", "schema-final", "schema-implicit", "choices", "cases"} {
		t.Run(name, func(t *testing.T) {
			tr := newTree(set)
			vc := &valCtx{t: tr, log: &logger{set: set}}
			// Warm the cache so only the target loop can observe cancellation.
			if _, err := vc.getnextOf(parent, nil, false); err != nil {
				t.Fatal(err)
			}
			ctx := &cancelAtContext{Context: context.Background(), at: 32}
			vc.budget.ctx = ctx
			var err error
			switch name {
			case "cache-build":
				vc.getnext = nil
				_, err = vc.getnextOf(parent, nil, false)
				if len(vc.getnext) != 0 {
					t.Fatal("canceled build cached partial schema children")
				}
			case "schema-final":
				err = vc.siblingsSchema(nil, siblingSpan{}, parent, nil)
			case "schema-implicit":
				err = vc.newImplicit(nil, parent, nil, 0)
			case "choices":
				// A retained sibling makes validateChoices enter its choice loop.
				tr = cancelValidationTree(t, opIndexSet(t, cancelValidationModel), 1, false)
				err = vc.validateChoices(&tr.top, parent, nil)
			case "cases":
				// One wide choice, with absent data in every case.
				var cases []*schema.Node
				for _, ch := range parent.Children {
					if ch.Kind == schema.Choice {
						cases = append(cases, ch.Children...)
					}
				}
				err = vc.validateCases(&tr.top, &schema.Node{Kind: schema.Choice, Children: cases})
			}
			if !errors.Is(err, context.Canceled) || ctx.calls != ctx.at {
				t.Fatalf("schema scan: polls=%d, error=%v", ctx.calls, err)
			}
		})
	}
}

func TestValidationCancelUniqueComparison(t *testing.T) {
	var model, unique, input strings.Builder
	model.WriteString(`module m { yang-version 1.1; namespace "urn:cancel-unique"; prefix m; list l { key k; leaf k { type string; }`)
	for i := range 64 {
		fmt.Fprintf(&model, `leaf v%d { type string; }`, i)
		fmt.Fprintf(&unique, " v%d", i)
	}
	fmt.Fprintf(&model, `unique "%s"; } }`, strings.TrimSpace(unique.String()))
	input.WriteString(`{"m:l":[`)
	for i := range 2 {
		if i != 0 {
			input.WriteByte(',')
		}
		fmt.Fprintf(&input, `{"k":"%d"`, i)
		for v := range 64 {
			fmt.Fprintf(&input, `,"v%d":"same"`, v)
		}
		input.WriteByte('}')
	}
	input.WriteString(`]}`)
	set := opIndexSet(t, model.String())
	tr, ds, err := parseWith(context.Background(), strings.NewReader(input.String()), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
	if err != nil {
		t.Fatal(ds, err)
	}
	ctx := &cancelAtContext{Context: context.Background(), at: 32}
	vc := &valCtx{t: tr, log: &logger{set: set}, budget: xpathBudget{ctx: ctx}}
	if err := vc.unique(&tr.top, tr.top.At(0).schema); !errors.Is(err, context.Canceled) || len(vc.log.diags) != 0 {
		t.Fatalf("two-instance unique comparison: %v %v", vc.log.diags, err)
	}
}

// Arm cancellation at the parser/validator boundary, after all parser checks.
func TestValidationNotFinalParseCancel(t *testing.T) {
	set := opIndexSet(t, cancelValidationModel)
	for _, fp := range []struct {
		name  string
		parse formatParser
	}{{"json", parseJSON}, {"xml", parseXML}} {
		t.Run(fp.name, func(t *testing.T) {
			input := cancelValidationInput(512, true)
			if fp.name == "xml" {
				input = `<r xmlns="urn:validation-cancel">` + strings.Repeat(`<v>x</v>`, 512) + `</r>`
			}
			ctx := &cancelAtContext{Context: context.Background()}
			tr, ds, err := parseWith(ctx, strings.NewReader(input), set,
				parseOpts{ParseOptions: ParseOptions{Validate: ValidateOptions{NotFinal: true, MultiError: true}}}, fp.parse,
				func(lc *lydCtx) {
					lc.validate = func(lc *lydCtx) error {
						ctx.at = ctx.calls + 32
						return lc.validateParsed()
					}
				})
			if tr != nil || !errors.Is(err, context.Canceled) || len(ds) != 0 || ctx.calls != ctx.at {
				t.Fatalf("parse validation: treePresent=%t polls=%d/%d diagnostics=%v error=%v", tr != nil, ctx.calls, ctx.at, ds, err)
			}
		})
	}
}
