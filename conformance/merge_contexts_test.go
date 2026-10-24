// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMergeContextsMatrix merges another context's subtree over existing nodes (lyd_merge with
// trees of two contexts, #306): leaf, container, keyed list, leaf-list, keyless list and state
// leaf-list targets with and without a children table, and the leaf types. These requests have no
// generated goldens: want_rc pins the native result, and with the oracle the whole response is
// compared.
func TestMergeContextsMatrix(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "merge-contexts", "matrix.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Source map[string]any `json:"source"`
		Cases  []struct {
			ID             string         `json:"id"`
			Request        map[string]any `json:"request"`
			WantRC         string         `json:"want_rc"`
			WantFailedStep *int           `json:"want_failed_step"`
			WantMsg        string         `json:"want_msg"`
			MergeStep      int            `json:"merge_step"`
			NativeCrash    bool           `json:"native_crash"`
			NativeUnported bool           `json:"native_unported"`
			NativeRC       string         `json:"native_rc"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.Source["license"] != "BSD-3-Clause" || len(m.Cases) == 0 {
		t.Fatal("incomplete matrix")
	}
	base := filepath.Join("corpus", "protocol-v2")
	for _, tc := range m.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			got, err := (Yang{}).Run(Request{ID: tc.ID, BaseDir: base, Params: tc.Request})
			if err != nil {
				t.Fatal(err)
			}
			if rc := got["rc"].(map[string]any)["name"]; rc != tc.WantRC {
				t.Fatalf("rc = %v, want %s", rc, tc.WantRC)
			}
			if tc.WantFailedStep != nil && fmt.Sprint(got["failed_step"]) != fmt.Sprint(*tc.WantFailedStep) {
				t.Fatalf("failed step = %v, want %d", got["failed_step"], *tc.WantFailedStep)
			}
			reached := func(r Response) error {
				// every case is about its merge step: the setup before it must succeed
				if steps := list(r["steps"]); len(steps) <= tc.MergeStep {
					return fmt.Errorf("%d steps, merge step %d", len(steps), tc.MergeStep)
				}
				if fs, ok := r["failed_step"]; ok && fs != nil {
					var n int
					if _, err := fmt.Sscan(fmt.Sprint(fs), &n); err != nil || n < tc.MergeStep {
						return fmt.Errorf("failed at step %v before the merge step %d", fs, tc.MergeStep)
					}
				}
				return nil
			}
			if tc.MergeStep == 0 {
				t.Fatal("case without a merge step")
			}
			if err := reached(got); err != nil {
				t.Fatalf("engine: %v", err)
			}
			if tc.WantMsg != "" && !strings.Contains(fmt.Sprint(got["steps"]), tc.WantMsg) {
				t.Fatalf("no diagnostic containing %q: %v", tc.WantMsg, got["steps"])
			}
			t.Run("oracle", func(t *testing.T) {
				if tc.NativeUnported {
					// the leaf types differ between the contexts: not ported (D-0133); libyang's
					// result (native_rc) is recorded for reference only
					t.Skipf("type definitions differ (D-0133); libyang %s", tc.NativeRC)
				}
				if tc.NativeCrash {
					// libyang 5.8.6 crashes on this request (undefined behaviour: D-0133). The setup
					// before the merge step must succeed, and the request must end by SIGSEGV or
					// SIGABRT: a timeout, a response or another error is a failure.
					setup := maps.Clone(tc.Request)
					setup["steps"] = list(tc.Request["steps"])[:tc.MergeStep]
					want, err := nativeRequestRunner(t)(base, setup)
					if err != nil {
						t.Fatalf("setup: %v", err)
					}
					if fs := want["failed_step"]; fs != nil || want.Verdict() == "request-error" {
						t.Fatalf("setup failed at step %v (%v)", fs, want.Verdict())
					}
					if _, err := nativeCrashSignal(t, base, tc.Request); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := nativeRequestRunner(t)(base, tc.Request)
				if err != nil {
					t.Fatal(err)
				}
				if err := reached(want); err != nil {
					t.Fatalf("oracle: %v", err)
				}
				want, _ = withoutSkipped(want, Yang{}.SkippedFields("sequence"))
				got, _ := withoutSkipped(got, Yang{}.SkippedFields("sequence"))
				if diff := diffResponses(want, got); diff != "" {
					t.Fatalf("oracle mismatch: %s", diff)
				}
			})
		})
	}
}
