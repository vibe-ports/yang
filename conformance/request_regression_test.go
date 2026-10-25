// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Full native responses preserve both execution and output checks for each
// pairwise root cause, even without the multi-gigabyte runtime cache.
func TestLoggedPairwiseExecution(t *testing.T) {
	b, err := os.ReadFile("testdata/request-pairwise-execution.json")
	if err != nil {
		t.Fatal(err)
	}
	var log struct {
		Cases []struct {
			ID, Gap string
			requestCacheEntry
		}
	}
	if err := json.Unmarshal(b, &log); err != nil {
		t.Fatal(err)
	}
	if len(log.Cases) != 5 {
		t.Fatal("expected all five pairwise root causes")
	}
	for _, tc := range log.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			var p map[string]any
			if err := json.Unmarshal(tc.Request, &p); err != nil {
				t.Fatal(err)
			}
			canonical, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(canonical)) != tc.SHA256 || tc.Error != "" {
				t.Fatal("native request digest or transport failure")
			}
			got, err := inventoryRun(Request{BaseDir: "corpus/protocol-v2", Params: p})
			if tc.ID == "cross-context-diff" {
				// The immutable native recording populates a user-ordered run in both
				// contexts. D-0121 now refuses that class even when this final array
				// transition happens to produce a deterministic native response.
				if err != nil {
					t.Fatal(err)
				}
				assertD0121Response(t, got)
				return
			}
			category, class := inventoryClassification(tc.Response, got, err, "")
			wantCategory, wantClass := "MATCH", "equal"
			if tc.Gap != "" {
				wantCategory, wantClass = "B", tc.Gap
			}
			if category != wantCategory || class != wantClass {
				t.Fatalf("got %s/%s (%v), want %s/%s\nnative: %v\nengine: %v", category, class, err, wantCategory, wantClass, tc.Response, got)
			}
		})
	}
}

// These execution outcomes were captured by the lead's native amd64 run. Keep
// representative failures runnable before the full native cache is available.
func TestLoggedRequestExecution(t *testing.T) {
	b, err := os.ReadFile("testdata/request-execution.json")
	if err != nil {
		t.Fatal(err)
	}
	var log struct {
		Cases []struct {
			ID      string         `json:"id"`
			Gap     string         `json:"gap"` // a registered B class: the native outcome stays on record
			Request map[string]any `json:"request"`
			Outcome any            `json:"outcome"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &log); err != nil {
		t.Fatal(err)
	}
	if len(log.Cases) == 0 {
		t.Fatal("no recorded execution outcomes")
	}
	for _, tc := range log.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			got, err := inventoryRun(Request{BaseDir: "corpus/protocol-v2", Params: tc.Request})
			if tc.Gap != "" {
				if inventoryGap(err) != tc.Gap {
					t.Fatalf("got %v, want B class %s", err, tc.Gap)
				}
				return
			}
			if err != nil && strings.HasSuffix(err.Error(), "(a request-error)") {
				got = Response{"verdict": "request-error", "request_error": strings.TrimSuffix(err.Error(), " (a request-error)")}
				err = nil
			}
			if err != nil {
				t.Fatal(err)
			}
			if actual := inventoryOutcome(got); !reflect.DeepEqual(tc.Outcome, actual) {
				w, _ := json.Marshal(tc.Outcome)
				g, _ := json.Marshal(actual)
				t.Fatalf("native: %s\nengine: %s", w, g)
			}
		})
	}
}
