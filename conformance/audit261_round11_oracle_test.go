// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

func round11Schema(t *testing.T, module string) *yang.Schema {
	t.Helper()
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, os.DirFS("corpus/protocol-v2/schemas"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load(module, "", nil); err != nil {
		t.Fatal(err)
	}
	return c.Schema()
}

func round11Parse(t *testing.T, s *yang.Schema, format data.Format, input string) *data.Tree {
	t.Helper()
	tr, diags, err := data.Parse(context.Background(), strings.NewReader(input), format, s,
		data.ParseOptions{ParseOnly: true})
	if err != nil {
		t.Fatalf("parse: %v (%v)", err, diags)
	}
	return tr
}

func round11JSON(t *testing.T, tr *data.Tree) string {
	t.Helper()
	var out bytes.Buffer
	if err := tr.PrintJSON(&out, data.PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, out.Bytes()); err != nil {
		t.Fatal(err)
	}
	return compact.String()
}

// The first create into NewTree(A) supplies B's effective context. The ordered
// creates that follow must compare their actual B anchors, then Find uses B.
func TestAudit261ApplyEmptyTreeAdoptsSourceContext(t *testing.T) {
	oracle, base := requestOracle(t)
	const input = `{"pv2-detached:c":{"base":"x","l":[{"k":"a"},{"k":"b"}]}}`
	a, b := round11Schema(t, "pv2-detached"), round11Schema(t, "pv2-detached")
	source := round11Parse(t, b, data.FormatJSON, input)
	diff, err := data.Diff(nil, source, data.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target := data.NewTree(a)
	if err := target.ApplyDiff(diff, data.ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/pv2-detached:c/base", "/pv2-detached:c/l[k='a']", "/pv2-detached:c/l[k='b']",
	} {
		if n, err := target.Find(path); err != nil || n == nil {
			t.Fatalf("Find(%q) after ApplyDiff: %v, %v", path, n, err)
		}
	}
	p := map[string]any{"op": "sequence", "searchdirs": []any{"schemas"},
		"modules":         []any{map[string]any{"name": "pv2-detached"}},
		"context_options": []any{"no_yanglibrary"}, "steps": []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config", "parse_only": true,
				"data": input, "target": map[string]any{"searchdirs": []any{"schemas"},
					"modules": []any{map[string]any{"name": "pv2-detached"}}, "context_options": []any{"no_yanglibrary"}}},
			map[string]any{"do": "diff_apply"}, map[string]any{"do": "dump"},
		}}
	want, err := inventoryNativeResponse(oracle, base, p)
	if err != nil {
		t.Fatal(err)
	}
	if want["verdict"] != "valid" {
		t.Fatalf("oracle verdict: %v (%v)", want["verdict"], want["request_error"])
	}
	wantJSON := want["steps"].([]any)[2].(map[string]any)["tree"].(map[string]any)["json"].(string)
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(wantJSON)); err != nil {
		t.Fatal(err)
	}
	if got := round11JSON(t, target); got != compact.String() {
		t.Fatalf("public API %s, oracle %s", got, compact.String())
	}
}

// The Go API retains metadata from independently compiled contexts in a merged
// diff. The native sequence establishes the same final cancellation in one
// context; libyang's merge asserts schema pointer identity across contexts.
func TestAudit261MergeMixedContextMetaCancellation(t *testing.T) {
	oracle, base := requestOracle(t)
	const ns = `urn:vibe-ports:yang:conformance:pv2-mdapi`
	const yn = `urn:ietf:params:xml:ns:yang:1`
	makeDiff := func(kind, value string) string {
		return `<c xmlns="` + ns + `" xmlns:yang="` + yn + `" yang:operation="none">` +
			`<s yang:operation="none" yang:` + kind + `="pv2-mdapi:num=` + value + `">x</s></c>`
	}
	a, b := round11Schema(t, "pv2-mdapi"), round11Schema(t, "pv2-mdapi")
	target := round11Parse(t, a, data.FormatXML, makeDiff("meta-create", "1"))
	for _, item := range []struct{ kind, value string }{{"meta-create", "2"}, {"meta-delete", "2"}} {
		source := round11Parse(t, b, data.FormatXML, makeDiff(item.kind, item.value))
		if err := target.MergeDiff(source, data.MergeDiffOptions{}); err != nil {
			t.Fatalf("MergeDiff(%s): %v", item.kind, err)
		}
	}
	s, err := target.Find("/pv2-mdapi:c/s")
	if err != nil || s == nil {
		t.Fatalf("Find merged leaf: %v, %v", s, err)
	}
	var creates []string
	for m := range s.Meta() {
		if m.Module().Name() == "yang" && m.Name() == "meta-create" {
			creates = append(creates, m.Value())
		}
	}
	if len(creates) != 1 || creates[0] != "pv2-mdapi:num=1" {
		t.Fatalf("remaining meta-create values: %q", creates)
	}
	steps := []any{}
	for i, item := range []struct{ kind, value string }{{"meta-create", "1"}, {"meta-create", "2"}, {"meta-delete", "2"}} {
		do := "diff_merge"
		if i == 0 {
			do = "diff_parse"
		}
		steps = append(steps, map[string]any{"do": do, "format": "xml", "data_type": "config",
			"data": makeDiff(item.kind, item.value)})
	}
	want, err := inventoryNativeResponse(oracle, base, map[string]any{
		"op": "sequence", "searchdirs": []any{"schemas"},
		"modules":         []any{map[string]any{"name": "pv2-mdapi"}},
		"context_options": []any{"no_yanglibrary"}, "steps": steps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want["verdict"] != "valid" {
		t.Fatalf("oracle verdict: %v (%v)", want["verdict"], want["request_error"])
	}
	wantJSON := want["steps"].([]any)[2].(map[string]any)["diff"].(map[string]any)["json"].(string)
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(wantJSON)); err != nil {
		t.Fatal(err)
	}
	if got := round11JSON(t, target); got != compact.String() {
		t.Fatalf("public API %s, oracle %s", got, compact.String())
	}
}
