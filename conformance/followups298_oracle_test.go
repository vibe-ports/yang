// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestFollowups298SpeculativeDeref keeps the libyang side of the suppression boundary live.
// libyang owns an ordinary unresolved deref() failure from tentative key compilation; the port's
// additional resource failures are the exception tested in data.
func TestFollowups298SpeculativeDeref(t *testing.T) {
	base := t.TempDir()
	module := `module m {
  yang-version 1.1;
  namespace urn:m;
  prefix m;
  leaf target { type string; }
  leaf ref { type instance-identifier { require-instance false; } }
  list pair {
    key "k1 k2";
    leaf k1 { type string; }
    leaf k2 { type string; }
  }
}`
	if err := os.WriteFile(filepath.Join(base, "m.yang"), []byte(module), 0o644); err != nil {
		t.Fatal(err)
	}
	p := map[string]any{
		"op":              "xpath",
		"searchdirs":      []any{"."},
		"modules":         []any{map[string]any{"name": "m"}},
		"context_options": []any{"no_yanglibrary"},
		"format":          "json",
		"data_type":       "data",
		"parse_only":      true,
		"data":            `{"m:ref":"/m:target","m:pair":[{"k1":"present","k2":"value"}]}`,
		"xpath":           "count(/m:pair[k1='absent'][k2=string(deref(/m:ref))])",
	}
	want, err := nativeRequestRunner(t)(base, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	want, _ = withoutSkipped(want, Yang{}.SkippedFields("xpath"))
	got, _ = withoutSkipped(got, Yang{}.SkippedFields("xpath"))
	if diff := diffResponses(want, got); diff != "" {
		t.Fatalf("oracle mismatch: %s", diff)
	}
	result, _ := got["result"].(map[string]any)
	if got.Verdict() != "valid" || len(got.Diagnostics()) != 0 || result["type"] != "number" ||
		fmt.Sprint(result["value"]) != "0" {
		t.Fatalf("speculative deref response = %v", got)
	}
}

// TestFollowups298SpeculativeNestedLeafref keeps the libyang side of the suppression boundary
// live. Resolving a's extended leafref path recursively dereferences the dangling leafref b. Both
// its direct LY_EINVAL and deref()'s wrapper belong to tentative key compilation and disappear
// when ordinary evaluation rejects the only a:l1 row at its first predicate.
func TestFollowups298SpeculativeNestedLeafref(t *testing.T) {
	base := "corpus"
	p := map[string]any{
		"op":              "xpath",
		"searchdirs":      []any{"protocol-v2/schemas"},
		"modules":         []any{map[string]any{"name": "a"}, map[string]any{"name": "pv2-deref-xperr"}},
		"context_options": []any{"no_yanglibrary", "leafref_extended"},
		"format":          "json",
		"data_type":       "data",
		"parse_only":      true,
		"data": `{"a:l1":[{"a":"present","b":"value"}],` +
			`"pv2-deref-xperr:c":{"t":[{"name":"x"}],"b":"nope","a":"x"}}`,
		"xpath": "count(/a:l1[a='absent'][b=string(deref(/pv2-deref-xperr:c/a))])",
	}
	oracle := nativeRequestRunner(t)
	want, err := oracle(base, p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	want, _ = withoutSkipped(want, Yang{}.SkippedFields("xpath"))
	got, _ = withoutSkipped(got, Yang{}.SkippedFields("xpath"))
	if diff := diffResponses(want, got); diff != "" {
		t.Fatalf("oracle mismatch: %s", diff)
	}
	result, _ := got["result"].(map[string]any)
	if got.Verdict() != "valid" || len(got.Diagnostics()) != 0 || result["type"] != "number" ||
		fmt.Sprint(result["value"]) != "0" {
		t.Fatalf("speculative nested-leafref response = %v", got)
	}
}
