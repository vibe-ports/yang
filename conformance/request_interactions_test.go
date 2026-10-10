// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type requestInteraction struct {
	ID         string         `json:"id"`
	Request    map[string]any `json:"request"`
	Verdict    string
	Gap        string
	ErrorClass string `json:"request_error_class"`
}

// Request-errors cannot be corpus goldens (cmd/golden rejects them). These
// request-layer fixtures retain their source/license and compare against the
// native runner separately, using the same cache and A/B/C classifier.
func requestInteractions(t *testing.T) []requestInteraction {
	t.Helper()
	paths, err := filepath.Glob("testdata/request-interactions/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no request interaction fixtures")
	}
	var out []requestInteraction
	for _, path := range paths {
		b, err := os.ReadFile(path) //nolint:gosec // Paths come from the fixed testdata glob, never request input.
		if err != nil {
			t.Fatal(err)
		}
		var fixture requestInteraction
		if err := json.Unmarshal(b, &fixture); err != nil {
			t.Fatal(err)
		}
		out = append(out, fixture)
	}
	for _, tc := range []struct{ name, verdict, gap string }{
		{"insert-output-fewer-keys", "valid", ""},
		{"prelude-xpath-bogus-flag", "schema-error", ""},
		{"reply-non-operation-parent", "", "reply-non-operation-parent"},
	} {
		b, err := os.ReadFile("corpus/manifest.d/protocol-v2/" + tc.name + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		var fragment struct {
			Fixtures []Fixture `yaml:"fixtures"`
		}
		if err := yaml.Unmarshal(b, &fragment); err != nil {
			t.Fatal(err)
		}
		if len(fragment.Fixtures) != 1 {
			t.Fatal("expected one interaction fixture")
		}
		f := fragment.Fixtures[0]
		f.Request["base_dir"] = "."
		out = append(out, requestInteraction{ID: f.ID, Request: f.Request, Verdict: tc.verdict, Gap: tc.gap})
	}
	return out
}

func TestRequestInteractions(t *testing.T) {
	for _, fixture := range requestInteractions(t) {
		t.Run(fixture.ID, func(t *testing.T) {
			got, err := inventoryRun(Request{BaseDir: "corpus/protocol-v2", Params: fixture.Request})
			if fixture.Gap != "" {
				if class := inventoryGap(err); class != fixture.Gap {
					t.Fatalf("gap %q (%v), want %q", class, err, fixture.Gap)
				}
				return
			}
			if fixture.Verdict != "" {
				if err != nil || got.Verdict() != fixture.Verdict {
					t.Fatalf("verdict %q (%v), want %q", got.Verdict(), err, fixture.Verdict)
				}
				return
			}
			if err == nil || !strings.HasSuffix(err.Error(), " (a request-error)") {
				t.Fatalf("want request-error, got %v", err)
			}
			if got := inventoryErrorClass(strings.TrimSuffix(err.Error(), " (a request-error)")); got != fixture.ErrorClass {
				t.Fatalf("request-error class %q, want %q", got, fixture.ErrorClass)
			}
		})
	}
}

// Deliberately outside Differential$: the existing single-field response cache
// predates these requests. Capture/replay with this test's explicit name.
func TestRequestInteractionsOracle(t *testing.T) {
	oracle, base := requestOracle(t)
	contexts := newInventoryContextCache()
	for _, fixture := range requestInteractions(t) {
		t.Run(fixture.ID, func(t *testing.T) {
			compareRequestOutcome(t, oracle, contexts, base, fixture.Request)
		})
	}
}
