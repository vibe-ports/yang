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
	"strconv"
	"strings"
	"testing"
	"time"
)

type mixedParentCase struct {
	ID             string         `json:"id"`
	Source         map[string]any `json:"source"`
	Request        map[string]any `json:"request"`
	WantRC         string         `json:"want_rc"`
	WantFailedStep *int           `json:"want_failed_step"`
	WantJSONOrder  []string       `json:"want_json_order"`
	WantXMLOrder   []string       `json:"want_xml_order"`
}

// These requests are oracle fixtures without goldens: #267 does not permit writing goldens.
// The request omits base_dir so the same input runs in the engine and the native oracle.
func TestMixedParentOracleRequests(t *testing.T) {
	paths, err := filepath.Glob("testdata/mixed-parent/*.json")
	if err != nil || len(paths) != 26 {
		t.Fatalf("oracle requests: %v (%d files)", err, len(paths))
	}
	var cases []mixedParentCase
	for _, path := range paths {
		b, err := os.ReadFile(path) //nolint:gosec // fixed testdata glob
		if err != nil {
			t.Fatal(err)
		}
		// A file is one request, or a matrix: "cases" sharing the file's source.
		var tc struct {
			mixedParentCase
			Cases []mixedParentCase `json:"cases"`
		}
		if err := json.Unmarshal(b, &tc); err != nil {
			t.Fatal(err)
		}
		if tc.Cases == nil {
			cases = append(cases, tc.mixedParentCase)
		}
		for _, c := range tc.Cases {
			c.Source = tc.Source
			cases = append(cases, c)
		}
	}
	for _, tc := range cases {
		t.Run(tc.ID, func(t *testing.T) {
			if tc.ID == "" || tc.Source["license"] != "BSD-3-Clause" || tc.Request["base_dir"] != nil || tc.WantRC == "" {
				t.Fatal("incomplete oracle request")
			}
			got, err := (Yang{}).Run(Request{ID: tc.ID, BaseDir: "corpus/protocol-v2", Params: tc.Request})
			if err != nil {
				t.Fatal(err)
			}
			rc := got["rc"].(map[string]any)["name"]
			if rc != tc.WantRC {
				t.Fatalf("rc = %v, want %s", rc, tc.WantRC)
			}
			if tc.WantFailedStep != nil && got["failed_step"] != json.Number(strconv.Itoa(*tc.WantFailedStep)) {
				t.Fatalf("failed step = %v (%T), want %d", got["failed_step"], got["failed_step"], *tc.WantFailedStep)
			}
			if len(tc.WantJSONOrder) > 0 || len(tc.WantXMLOrder) > 0 {
				steps := list(got["steps"])
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

			oracle := os.Getenv("YANG_ORACLE_BIN")
			if oracle == "" {
				oracle = OracleBinary("oracle")
			}
			if _, err := os.Stat(oracle); err != nil { //nolint:gosec // caller-selected test binary only
				if os.Getenv("YANG_ORACLE_BIN") != "" {
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
		})
	}
}
