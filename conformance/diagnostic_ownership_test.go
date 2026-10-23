// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const diagnosticOwnershipModule = `module ac {
  yang-version 1.1;
  namespace "urn:diagnostic-ownership";
  prefix ac;
  import ietf-yang-metadata { prefix md; }
  import ietf-yang-types { prefix yang; }
  md:annotation select { type yang:xpath1.0; }
  md:annotation first { type string; }
  md:annotation second { type string; }
  container parent {
    presence "parent";
    list l { key k; leaf k { type string; } }
  }
  list pair {
    key "k1 k2";
    leaf k1 { type string; }
    leaf k2 { type string; }
  }
  leaf s { type string; }
  leaf xp { type yang:xpath1.0; }
  anydata data { must "string(.)"; }
}`

const diagnosticOwnershipData = `{"ac:data":{"ac:s":"value","@ac:s":{"ac:select":"/missing:node"}}}`

func diagnosticOwnershipSetup(t *testing.T) (string, map[string]any) {
	t.Helper()
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "ac.yang"), []byte(diagnosticOwnershipModule), 0o600); err != nil {
		t.Fatal(err)
	}
	ietf, err := filepath.Abs(filepath.Join("corpus", "ietf"))
	if err != nil {
		t.Fatal(err)
	}
	return base, map[string]any{
		"searchdirs":      []any{".", ietf},
		"modules":         []any{map[string]any{"name": "ac"}},
		"context_options": []any{"no_yanglibrary"},
	}
}

func diagnosticOwnershipTarget(context map[string]any) map[string]any {
	return map[string]any{
		"searchdirs":      context["searchdirs"],
		"modules":         context["modules"],
		"context_options": context["context_options"],
	}
}

func TestSequenceXPathHashPredicateSuppressesSpeculativeDiagnostics(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": diagnosticOwnershipData[:len(diagnosticOwnershipData)-1] + `,"ac:parent":{"l":[{"k":"x"}]}}`},
		map[string]any{"do": "xpath", "xpath": "count(/ac:parent/l[k=substring(string(/ac:data),1,0)])"},
	}

	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	step := got["steps"].([]any)[1].(map[string]any)
	result, _ := step["result"].(map[string]any)
	if got.Verdict() != "valid" || len(step["diagnostics"].([]any)) != 0 ||
		result["type"] != "number" || fmt.Sprint(result["value"]) != "0" {
		t.Fatalf("speculative hash predicate response = %v", got)
	}

	// The callback is restored before ordinary evaluation continues: the second cast is not
	// speculative, so its diagnostic is emitted exactly once.
	p["steps"].([]any)[1].(map[string]any)["xpath"] =
		"count(/ac:parent/l[k=substring(string(/ac:data),1,0)]) + string-length(string(/ac:data))"
	got, err = (Yang{}).Run(Request{ID: t.Name() + "/restore", BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	step = got["steps"].([]any)[1].(map[string]any)
	if got.Verdict() != "invalid" || len(step["diagnostics"].([]any)) != 1 {
		t.Fatalf("restored hash predicate diagnostics = %v", got)
	}
}

func TestSequenceChangeSelectorDiagnosticKeepsRequestContext(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": `{"ac:data":{"ac:s":"value"}}`},
		map[string]any{"do": "dup", "node": "/ac:data", "options": []any{},
			"target": diagnosticOwnershipTarget(p)},
		map[string]any{"do": "edit", "new_meta": map[string]any{
			"node": "/ac:data", "name": "ac:select", "value": "/missing:node"}},
		map[string]any{"do": "change_term", "node_xpath": "/ac:data/ac:s[string(/ac:data)]", "value": "changed"},
	}

	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	steps := got["steps"].([]any)
	change := steps[3].(map[string]any)
	diagnostics := change["diagnostics"].([]any)
	if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "3" || len(diagnostics) != 1 {
		t.Fatalf("verdict %s, failed step %v, change step %v", got.Verdict(), got["failed_step"], change)
	}
	if phase := diagnostics[0].(map[string]any)["phase"]; phase != "edit" {
		t.Fatalf("diagnostic phase = %v, want edit", phase)
	}
}

func TestSequenceDetachedDupCollectsPendingDiagnosticAsEdit(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": `{"ac:data":{"ac:s":"value","@ac:s":{"ac:select":"/missing:node"}}}`},
		map[string]any{"do": "dup", "node": "/ac:data", "options": []any{},
			"target": diagnosticOwnershipTarget(p)},
		map[string]any{"do": "xpath", "xpath": "string(/ac:data)"},
		map[string]any{"do": "unlink", "node": "/ac:data"},
		map[string]any{"do": "dup", "detached": true, "options": []any{}},
	}

	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	steps := got["steps"].([]any)
	dup := steps[4].(map[string]any)
	diagnostics := dup["diagnostics"].([]any)
	if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "4" || len(diagnostics) != 1 {
		t.Fatalf("verdict %s, failed step %v, dup step %v", got.Verdict(), got["failed_step"], dup)
	}
	if phase := diagnostics[0].(map[string]any)["phase"]; phase != "edit" {
		t.Fatalf("diagnostic phase = %v, want edit", phase)
	}
}

func TestSequenceDupSourceSelectorDiagnosticPrecedesContextFailure(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "diff", "format": "json", "data_type": "data",
			"target": diagnosticOwnershipTarget(p), "data": `{"ac:s":"foreign"}`},
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": `{"ac:data":{}}`},
		map[string]any{"do": "diff_apply"},
		map[string]any{"do": "dup", "node_xpath": "/ac:data[string(.)]", "parent": "/ac:data",
			"options": []any{}, "source": map[string]any{
				"format": "json", "data_type": "data", "parse_only": true,
				"data": `{"ac:data":{"ac:s":"value","@ac:s":{"ac:select":"/missing:node"}}}`}},
	}

	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	steps := got["steps"].([]any)
	dup := steps[3].(map[string]any)
	diagnostics := dup["diagnostics"].([]any)
	if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "3" || len(diagnostics) != 2 {
		t.Fatalf("verdict %s, failed step %v, dup step %v", got.Verdict(), got["failed_step"], dup)
	}
	for i, wantCode := range []string{"LY_EVALID", "LY_EINVAL"} {
		item := diagnostics[i]
		if phase := item.(map[string]any)["phase"]; phase != "edit" {
			t.Fatalf("diagnostic %d phase = %v, want edit", i, phase)
		}
		if code := item.(map[string]any)["code"].(map[string]any)["name"]; code != wantCode {
			t.Fatalf("diagnostic %d code = %v, want %s", i, code, wantCode)
		}
	}
}

func TestSequenceInsertForeignSourceCollectsDestinationDiagnosticAsEdit(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": `{"ac:data":{"ac:s":"value","@ac:s":{"ac:select":"/missing:node"}}}`},
		map[string]any{"do": "dump"},
		map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
			"target": diagnosticOwnershipTarget(p), "data": `{"ac:s":"foreign"}`},
	}

	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	steps := got["steps"].([]any)
	insert := steps[2].(map[string]any)
	diagnostics := insert["diagnostics"].([]any)
	if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "2" || len(diagnostics) != 1 {
		t.Fatalf("verdict %s, failed step %v, insert step %v", got.Verdict(), got["failed_step"], insert)
	}
	if phase := diagnostics[0].(map[string]any)["phase"]; phase != "edit" {
		t.Fatalf("diagnostic phase = %v, want edit", phase)
	}
}

func TestSequenceDetachedDupKeepsSourceDiagnosticBeforeFailure(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": `{"ac:parent":{}}`},
		map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
			"target": diagnosticOwnershipTarget(p), "data": `{"ac:data":{"ac:s":"value"}}`},
		map[string]any{"do": "edit", "new_meta": map[string]any{
			"node": "/ac:data", "name": "ac:select", "value": "/missing:node"}},
		map[string]any{"do": "unlink", "node": "/ac:data"},
		map[string]any{"do": "xpath", "detached": true, "xpath": "string(.)"},
		map[string]any{"do": "dup", "detached": true, "parent": "/ac:parent", "options": []any{}},
	}

	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	steps := got["steps"].([]any)
	dup := steps[5].(map[string]any)
	if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "5" {
		t.Fatalf("verdict %s, failed step %v, dup step %v", got.Verdict(), got["failed_step"], dup)
	}
	diagnostics := dup["diagnostics"].([]any)
	if len(diagnostics) != 2 {
		t.Fatalf("dup diagnostics = %v, want two", diagnostics)
	}
	for i, wantCode := range []string{"LY_EVALID", "LY_EINVAL"} {
		item := diagnostics[i].(map[string]any)
		if item["phase"] != "edit" || item["code"].(map[string]any)["name"] != wantCode {
			t.Fatalf("diagnostic %d = %v, want %s/edit", i, item, wantCode)
		}
	}
}

func TestSequenceDetachedInsertKeepsSourceDiagnosticBeforeFailure(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": `{"ac:parent":{}}`},
		map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
			"target": diagnosticOwnershipTarget(p), "data": `{"ac:data":{"ac:s":"value"}}`},
		map[string]any{"do": "edit", "new_meta": map[string]any{
			"node": "/ac:data", "name": "ac:select", "value": "/missing:node"}},
		map[string]any{"do": "unlink", "node": "/ac:data"},
		map[string]any{"do": "xpath", "detached": true, "xpath": "string(.)"},
		map[string]any{"do": "insert", "detached": true, "parent": "/ac:parent"},
	}

	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	steps := got["steps"].([]any)
	insert := steps[5].(map[string]any)
	if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "5" {
		t.Fatalf("verdict %s, failed step %v, insert step %v", got.Verdict(), got["failed_step"], insert)
	}
	diagnostics := insert["diagnostics"].([]any)
	if len(diagnostics) != 2 {
		t.Fatalf("insert diagnostics = %v, want two", diagnostics)
	}
	if code := diagnostics[0].(map[string]any)["code"].(map[string]any)["name"]; code != "LY_EVALID" {
		t.Fatalf("first diagnostic = %v, want pending LY_EVALID", diagnostics[0])
	}
	for i, item := range diagnostics {
		if phase := item.(map[string]any)["phase"]; phase != "edit" {
			t.Fatalf("diagnostic %d phase = %v, want edit", i, phase)
		}
	}
}

func TestSequenceForeignNewMetaDiagnosticStaysWithNodeContext(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "diff", "format": "json", "data_type": "data",
			"target": diagnosticOwnershipTarget(p), "data": `{"ac:s":"foreign"}`},
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": `{"ac:parent":{}}`},
		map[string]any{"do": "diff_apply"},
		map[string]any{"do": "edit", "new_meta": map[string]any{
			"node": "/ac:parent", "name": "missing:select", "value": "x"}},
	}

	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	steps := got["steps"].([]any)
	meta := steps[3].(map[string]any)
	if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "3" ||
		len(meta["diagnostics"].([]any)) != 0 {
		t.Fatalf("verdict %s, failed step %v, metadata step %v", got.Verdict(), got["failed_step"], meta)
	}
}

func TestSequenceForeignDeleteDiagnosticStaysWithNodeContext(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "diff", "format": "json", "data_type": "data",
			"target": diagnosticOwnershipTarget(p), "data": `{"ac:s":"foreign"}`},
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": `{"ac:parent":{"l":[{"k":"key"}]}}`},
		map[string]any{"do": "diff_apply"},
		map[string]any{"do": "edit", "delete": "/ac:parent/l[k='key']/k"},
		map[string]any{"do": "dup", "node": "/ac:parent", "options": []any{}},
	}

	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	steps := got["steps"].([]any)
	deleteStep := steps[3].(map[string]any)
	if len(deleteStep["diagnostics"].([]any)) != 0 || deleteStep["rc"].(map[string]any)["name"] != "LY_SUCCESS" {
		t.Fatalf("delete step %v", deleteStep)
	}
	dupStep := steps[4].(map[string]any)
	if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "4" ||
		len(dupStep["diagnostics"].([]any)) != 1 ||
		dupStep["diagnostics"].([]any)[0].(map[string]any)["phase"] != "edit" ||
		dupStep["diagnostics"].([]any)[0].(map[string]any)["code"].(map[string]any)["name"] != "LY_EINVAL" {
		t.Fatalf("verdict %s, failed step %v, dup step %v", got.Verdict(), got["failed_step"], dupStep)
	}
}

func TestSequenceForeignInsertConstructorDiagnosticStaysWithParentContext(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "diff", "format": "json", "data_type": "data",
			"target": diagnosticOwnershipTarget(p), "data": `{"ac:s":"foreign"}`},
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": `{"ac:parent":{}}`},
		map[string]any{"do": "diff_apply"},
		map[string]any{"do": "edit", "insert_term": map[string]any{
			"parent": "/ac:parent", "module": "ac", "name": "s", "value": "x"}},
	}

	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	steps := got["steps"].([]any)
	insert := steps[3].(map[string]any)
	if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "3" ||
		len(insert["diagnostics"].([]any)) != 0 || insert["rc"].(map[string]any)["name"] != "LY_EINVAL" {
		t.Fatalf("verdict %s, failed step %v, insert step %v", got.Verdict(), got["failed_step"], insert)
	}
}

func TestSequenceForeignInsertParseFailureDoesNotCollectDestination(t *testing.T) {
	base, p := diagnosticOwnershipSetup(t)
	p["op"] = "sequence"
	p["steps"] = []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true,
			"data": `{"ac:data":{"ac:s":"value","@ac:s":{"ac:select":"/missing:node"}}}`},
		map[string]any{"do": "dump"},
		map[string]any{"do": "insert", "format": "json", "data_type": "data", "parse_only": true,
			"target": diagnosticOwnershipTarget(p), "data": `{"ac:s":{}}`},
	}

	got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
	if err != nil {
		t.Fatal(err)
	}
	steps := got["steps"].([]any)
	insert := steps[2].(map[string]any)
	diagnostics := insert["diagnostics"].([]any)
	if got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != "2" || len(diagnostics) != 1 ||
		diagnostics[0].(map[string]any)["phase"] != "parse" {
		t.Fatalf("verdict %s, failed step %v, insert step %v", got.Verdict(), got["failed_step"], insert)
	}
}
