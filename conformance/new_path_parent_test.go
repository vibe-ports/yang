// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// These oracle requests have no golden files: #255 forbids generating or leaving new goldens.
// The engine assertions always run; when the local oracle is built, its full response is compared.
func TestNewPathParentOracleRequests(t *testing.T) {
	paths, err := filepath.Glob("testdata/new-path-parent/*.json")
	if err != nil || len(paths) != 3 {
		t.Fatalf("oracle requests: %v (%d files)", err, len(paths))
	}
	for _, path := range paths {
		b, err := os.ReadFile(path) //nolint:gosec // fixed testdata glob
		if err != nil {
			t.Fatal(err)
		}
		var tc struct {
			ID              string         `json:"id"`
			Source          map[string]any `json:"source"`
			Request         map[string]any `json:"request"`
			WantVerdict     string         `json:"want_verdict"`
			WantPath        string         `json:"want_path"`
			WantLookupPath  string         `json:"want_lookup_path"`
			WantUpdateValue string         `json:"want_update_value"`
			WantJSONOrder   []string       `json:"want_json_order"`
			WantXMLOrder    []string       `json:"want_xml_order"`
		}
		if err := json.Unmarshal(b, &tc); err != nil {
			t.Fatal(err)
		}
		t.Run(tc.ID, func(t *testing.T) {
			if tc.ID == "" || tc.Source["license"] != "BSD-3-Clause" || tc.WantVerdict == "" || tc.WantPath == "" {
				t.Fatal("incomplete oracle request")
			}
			got, err := (Yang{}).Run(Request{ID: tc.ID, BaseDir: "corpus/protocol-v2", Params: tc.Request})
			if err != nil || got.Verdict() != tc.WantVerdict {
				t.Fatalf("engine: %v, %v", got, err)
			}
			steps := list(got["steps"])
			wantSteps := 3
			if tc.WantLookupPath != "" {
				wantSteps = 5
			}
			if len(steps) != wantSteps {
				t.Fatalf("engine steps: %v", steps)
			}
			found := false
			for _, node := range list(steps[1].(map[string]any)["typed"]) {
				if node.(map[string]any)["path"] == tc.WantPath {
					found = true
				}
			}
			if !found {
				t.Fatalf("edit did not create %s", tc.WantPath)
			}
			if tc.WantLookupPath != "" {
				for _, i := range []int{2, 3} {
					count := 0
					for _, node := range list(steps[i].(map[string]any)["typed"]) {
						n := node.(map[string]any)
						if n["path"] != tc.WantLookupPath {
							continue
						}
						count++
						if i == 3 && n["value"].(map[string]any)["canonical"] != tc.WantUpdateValue {
							t.Fatalf("update of %s: %v", tc.WantLookupPath, n)
						}
					}
					if count != 1 {
						t.Fatalf("step %d: found %d instances of %s", i, count, tc.WantLookupPath)
					}
				}
			}
			if len(tc.WantJSONOrder) != 0 || len(tc.WantXMLOrder) != 0 {
				tree := steps[len(steps)-1].(map[string]any)["tree"].(map[string]any)
				for _, check := range []struct {
					name, output string
					order        []string
				}{
					{"JSON", tree["json"].(string), tc.WantJSONOrder},
					{"XML", tree["xml"].(string), tc.WantXMLOrder},
				} {
					last := -1
					for _, part := range check.order {
						pos := strings.Index(check.output, part)
						if pos <= last {
							t.Fatalf("%s sibling order %v: %s", check.name, check.order, check.output)
						}
						last = pos
					}
				}
			}

			configured := os.Getenv("YANG_ORACLE_BIN")
			oracle := configured
			if oracle == "" {
				oracle = OracleBinary("oracle")
			}
			if _, err := os.Stat(oracle); err != nil { //nolint:gosec // caller-selected test binary only
				if configured != "" {
					t.Fatal(err)
				}
				t.Log("oracle binary not built; native comparison skipped")
				return
			}
			p := make(map[string]any, len(tc.Request)+1)
			for k, v := range tc.Request {
				p[k] = v
			}
			p["base_dir"] = "corpus/protocol-v2"
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, oracle) //nolint:gosec // repo's own oracle binary
			cmd.Stdin = bytes.NewReader(raw)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("oracle: %v", err)
			}
			want, err := ParseResponse(out)
			if err != nil {
				t.Fatal(err)
			}
			arch := want["arch"]
			if want["libyang"] != "5.8.6" || arch != runtime.GOARCH && arch != unameArch(runtime.GOARCH) {
				t.Fatalf("unexpected oracle version or architecture: %v, %v", want["libyang"], arch)
			}
			delete(want, "arch")
			wm, _ := withoutSkipped(want, Yang{}.SkippedFields("sequence"))
			gm, _ := withoutSkipped(got, Yang{}.SkippedFields("sequence"))
			if d := diffResponses(wm, gm); d != "" {
				t.Fatal(d)
			}
			for i, ws := range list(want["steps"]) {
				gs := list(got["steps"])[i]
				wd, gd := list(ws.(map[string]any)["diagnostics"]), list(gs.(map[string]any)["diagnostics"])
				for j, w := range wd {
					if w.(map[string]any)["msg"] != gd[j].(map[string]any)["msg"] {
						t.Errorf("step %d diagnostic %d text differs", i, j)
					}
				}
			}
		})
	}
}
