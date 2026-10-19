// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// Exercise the public API without the sequence adapter's empty-tree schema choice.
// A and B load the same module independently; the oracle sequence creates its diff
// in B and applies it to a NULL tree retained by A.
func TestAudit261ApplyCreateIndependentSnapshots(t *testing.T) {
	oracle, base := requestOracle(t)
	load := func() *yang.Schema {
		t.Helper()
		c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, os.DirFS("corpus/protocol-v2/schemas"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Load("pv2-detached", "", []string{"extra"}); err != nil {
			t.Fatal(err)
		}
		return c.Schema()
	}
	a, b := load(), load()
	source, _, err := data.Parse(context.Background(), strings.NewReader(`{"pv2-detached:c":{"base":"x"}}`),
		data.FormatJSON, b, data.ParseOptions{ParseOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	diff, err := data.DiffSiblings(nil, topNode(source), data.DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	target := data.NewTree(a)
	if err := target.ApplyDiff(diff, data.ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	var printed bytes.Buffer
	if err := target.PrintJSON(&printed, data.PrintOptions{}); err != nil {
		t.Fatal(err)
	}

	ctxSpec := map[string]any{"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2-detached", "features": []any{"extra"}}},
		"context_options": []any{"no_yanglibrary"}}
	p := map[string]any{"op": "sequence", "searchdirs": ctxSpec["searchdirs"], "modules": ctxSpec["modules"],
		"context_options": ctxSpec["context_options"], "steps": []any{
			map[string]any{"do": "diff", "format": "json", "data_type": "config", "parse_only": true,
				"data": `{"pv2-detached:c":{"base":"x"}}`, "target": ctxSpec},
			map[string]any{"do": "diff_apply"}, map[string]any{"do": "dump"},
		}}
	want, err := inventoryNativeResponse(oracle, base, p)
	if err != nil {
		t.Fatal(err)
	}
	if want["verdict"] != "valid" {
		t.Fatalf("oracle verdict %v", want["verdict"])
	}
	wantJSON := want["steps"].([]any)[2].(map[string]any)["tree"].(map[string]any)["json"].(string)
	var gotValue, wantValue any
	if err := json.Unmarshal(printed.Bytes(), &gotValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(wantJSON), &wantValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("public API %s, oracle %s", printed.String(), wantJSON)
	}
}
