// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
)

func splitSet(t *testing.T) *schema.Set {
	t.Helper()
	c, ds, err := yang.NewContext(yang.Options{NoYangLibrary: true},
		os.DirFS("../conformance/corpus/protocol-v2/schemas"), os.DirFS("../conformance/corpus/ietf"))
	if err != nil {
		t.Fatal(ds, err)
	}
	for _, name := range []string{"split-validation", "ietf-netconf-with-defaults"} {
		if ds, err := c.Load(name, "", nil); err != nil {
			t.Fatal(ds, err)
		}
	}
	return snap.Set(c.Schema())
}

func splitParse(t *testing.T, set *schema.Set, input string, o ParseOptions) (*Tree, []yang.Diagnostic, error) {
	t.Helper()
	return Parse(context.Background(), strings.NewReader(input), FormatJSON, snap.New(set), o)
}

func TestSplitValidationNotFinal(t *testing.T) {
	set := splitSet(t)
	inputs := map[string]string{
		"mandatory": `{}`,
		"must":      `{"required":"yes","values":["a","b"],"must":"bad"}`,
		"min":       `{"required":"yes"}`,
		"max":       `{"required":"yes","values":["a","b","c","d"]}`,
		"unique":    `{"required":"yes","values":["a","b"],"item":[{"k":"a","v":"same"},{"k":"b","v":"same"}]}`,
		"state":     `{"required":"yes","values":["a","b"],"state":"state"}`,
		"opaque":    `{"required":"yes","values":["a","b"],"unknown":"opaque"}`,
	}
	for name, body := range inputs {
		for _, api := range []string{"Parse", "Validate", "ValidateDiff", "ValidateModule", "ValidateModuleDiff"} {
			t.Run(name+"/"+api, func(t *testing.T) {
				input := `{"split-validation:top":` + body + `}`
				var tr, diff *Tree
				var ds []yang.Diagnostic
				var err error
				o := ValidateOptions{NotFinal: true, NoState: name == "state"}
				po := ParseOptions{Unknown: Opaque}
				if api == "Parse" {
					po.Validate = o
					tr, ds, err = splitParse(t, set, input, po)
				} else {
					po.ParseOnly = true
					tr, ds, err = splitParse(t, set, input, po)
					if err != nil {
						t.Fatal(ds, err)
					}
					switch api {
					case "Validate":
						ds, err = tr.Validate(context.Background(), o)
					case "ValidateDiff":
						diff, ds, err = tr.ValidateDiff(context.Background(), o)
					case "ValidateModule":
						ds, err = tr.ValidateModule(context.Background(), "split-validation", o)
					case "ValidateModuleDiff":
						diff, ds, err = tr.ValidateModuleDiff(context.Background(), "split-validation", o)
					}
				}
				if err != nil || len(ds) != 0 {
					t.Fatalf("NotFinal: %v %v", ds, err)
				}
				if n, findErr := tr.Find("/split-validation:top/d"); findErr != nil || n == nil || n.flags&FlagDefault == 0 {
					t.Fatalf("preceding implicit phase: %v %v", n, findErr)
				}
				if strings.HasSuffix(api, "Diff") && diff == nil {
					t.Fatal("missing implicit diff")
				}
				if ds, err = tr.ValidateModuleFinal(context.Background(), "split-validation", ValidateOptions{NoState: o.NoState}); err == nil || len(ds) == 0 {
					t.Fatalf("final pass accepted %s: %v %v", name, ds, err)
				}
				if _, _, err = splitParse(t, set, input, ParseOptions{Unknown: Opaque, Validate: ValidateOptions{NoState: o.NoState}}); err == nil {
					t.Fatal("default behavior skipped final pass")
				}
			})
		}
	}
}

func TestSplitValidationRetainsEarlierChecks(t *testing.T) {
	set := splitSet(t)
	for _, field := range []string{"ref", "iid", "union-ref", "conditional"} {
		for _, api := range []string{"Parse", "Validate", "ValidateDiff", "ValidateModule", "ValidateModuleDiff"} {
			t.Run(field+"/"+api, func(t *testing.T) {
				value := "missing"
				if field == "iid" {
					value = "/split-validation:top/target"
				}
				input := fmt.Sprintf(`{"split-validation:top":{"%s":%q}}`, field, value)
				o := ValidateOptions{NotFinal: true, MultiError: true}
				tr, ds, err := splitParse(t, set, input, ParseOptions{ParseOnly: api != "Parse", Validate: o})
				if api != "Parse" {
					if err != nil {
						t.Fatal(ds, err)
					}
					switch api {
					case "Validate":
						ds, err = tr.Validate(context.Background(), o)
					case "ValidateDiff":
						_, ds, err = tr.ValidateDiff(context.Background(), o)
					case "ValidateModule":
						ds, err = tr.ValidateModule(context.Background(), "split-validation", o)
					case "ValidateModuleDiff":
						_, ds, err = tr.ValidateModuleDiff(context.Background(), "split-validation", o)
					}
				}
				code := "LY_EVALID"
				if field == "iid" {
					code = "LY_ENOTFOUND"
				}
				if err == nil || len(ds) != 1 || ds[0].Err != code || !strings.Contains(ds[0].DataPath, field) {
					t.Fatalf("earlier check lost or final errors leaked: %v %v", ds, err)
				}
			})
		}
	}
	// XML parsing uses the same validation option, independently of JSON's parser queues.
	tr, ds, err := Parse(context.Background(), strings.NewReader(`<top xmlns="urn:split-validation"><must>bad</must></top>`),
		FormatXML, snap.New(set), ParseOptions{Validate: ValidateOptions{NotFinal: true}})
	if err != nil || len(ds) != 0 || tr == nil {
		t.Fatal(ds, err)
	}
}

func TestSplitValidationFinalMutationAndRollback(t *testing.T) {
	set := splitSet(t)
	build := func() *Tree {
		tr, ds, err := splitParse(t, set, `{"split-validation:flags":{"nested":{"d":"implicit"}}}`, ParseOptions{ParseOnly: true})
		if err != nil {
			t.Fatal(ds, err)
		}
		diff, ds, err := splitParse(t, set, `{"split-validation:flags":{"@":{"yang:operation":"none"},"nested":{"d":"implicit","@d":{"ietf-netconf-with-defaults:default":true,"yang:orig-default":false}}}}`, ParseOptions{ParseOnly: true})
		if err != nil {
			t.Fatal(ds, err)
		}
		if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
			t.Fatal(err)
		}
		return tr
	}
	tr := build()
	before := snapshotHashBudgetTree(tr)
	var trace []uint64
	tr.work.hashTrace = &trace
	if ds, err := tr.ValidateModuleFinal(context.Background(), "split-validation", ValidateOptions{}); err != nil || len(ds) != 0 {
		t.Fatal(ds, err)
	}
	for _, path := range []string{"/split-validation:flags", "/split-validation:flags/nested"} {
		n, err := tr.Find(path)
		if err != nil || n == nil || n.flags&FlagDefault == 0 {
			t.Fatalf("final-only default flag %s: %v %v", path, n, err)
		}
	}
	if reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
		t.Fatal("final-only did not mutate")
	}
	// Native validation errors keep the flag changes, unlike hash-budget failures.
	partial := build()
	if _, err := partial.NewInner(nil, "split-validation:top", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if ds, err := partial.ValidateModuleFinal(context.Background(), "split-validation", ValidateOptions{MultiError: true}); err == nil || len(ds) != 2 {
		t.Fatal(ds, err)
	}
	if n, err := partial.Find("/split-validation:flags"); err != nil || n == nil || n.flags&FlagDefault == 0 {
		t.Fatal("validation error lost flag changes", n, err)
	}
	if len(trace) < 2 {
		t.Fatal("flag mutations were not journaled and charged")
	}
	for _, threshold := range trace {
		if threshold <= 1 {
			continue
		} // zero selects the default budget
		tr = build()
		before = snapshotHashBudgetTree(tr)
		tr.work.hashLimit = threshold - 1
		ds, err := tr.ValidateModuleFinal(context.Background(), "split-validation", ValidateOptions{})
		if !errors.Is(err, yang.ErrBudget) || len(ds) != 0 {
			t.Fatalf("limit %d: %v %v", threshold-1, ds, err)
		}
		if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
			t.Fatal("failed final-only mutation did not roll back")
		}
		tr.work.hashLimit = 0
		if _, err := tr.ValidateModuleFinal(context.Background(), "split-validation", ValidateOptions{}); err != nil {
			t.Fatal("retry:", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr = build()
	before = snapshotHashBudgetTree(tr)
	if ds, err := tr.ValidateModuleFinal(ctx, "split-validation", ValidateOptions{}); !errors.Is(err, context.Canceled) || len(ds) != 0 {
		t.Fatal(ds, err)
	}
	if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
		t.Fatal("canceled final-only mutated tree")
	}
}
