// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/vibe-ports/yang"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Native v5.8.6 lyd_validate_autodel_node_del / lyd_validate_unres, with the
// same internal mixed-context placement as TestWhenDeadLeaderMixedContexts.
// Source: https://github.com/CESNET/libyang/tree/v5.8.6 (BSD-3-Clause).
// RFC7950 section 7.21.5. No golden files are produced.
func TestWhenMixedContextChildLiveOracle(t *testing.T) {
	if os.Getenv("YANG_ORACLE_REQUIRED") == "" {
		t.Skip("run with YANG_ORACLE_REQUIRED=1 in the dev container")
	}
	prefix := os.Getenv("LIBYANG_PREFIX")
	if prefix == "" {
		prefix = "/opt/libyang"
	}
	base := t.TempDir()
	probe := filepath.Join(base, "when-context-probe")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cc", "-O2", "-Wall", "-Wextra", "-Werror", "-std=c11", //nolint:gosec // trusted compiler and library prefix; fixed source and test-owned output
		"-I"+filepath.Join(prefix, "include"), "../conformance/oracle/when_mixed_context.c",
		"-L"+filepath.Join(prefix, "lib"), "-Wl,-rpath,"+filepath.Join(prefix, "lib"), "-lyang", "-o", probe)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile native probe: %v: %s", err, out)
	}
	a, b := deadRunModule(`when "false()";`, false), deadRunModule("", false)
	for name, source := range map[string]string{"a.yang": a, "b.yang": b} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, module := range []bool{false, true} {
		t.Run(fmt.Sprint("module=", module), func(t *testing.T) {
			mode := "all"
			if module {
				mode = "module"
			}
			out, err := exec.CommandContext(ctx, probe, filepath.Join(base, "a.yang"), filepath.Join(base, "b.yang"), mode).CombinedOutput() //nolint:gosec // test-built executable, test-owned schemas and fixed modes
			if err != nil {
				t.Fatalf("native: %v: %s", err, out)
			}
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) != 3 || lines[0] != "0 0 0" {
				t.Fatalf("native verdict/diagnostics: %s", out)
			}
			setA, setB := reviewDiffSet(t, a), reviewDiffSet(t, b)
			tr := parseWhenTrue(t, setA, `{"m:c":{"g":"g","l":[{"k":"dead0"},{"k":"dead1"}],"f1":"1","f2":"2","f3":"3"}}`)
			c := tr.top.first()
			linkForeign(t, tr, c, setB, `{"m:c":{"l":[{"k":"live"}]}}`)
			if c.kids.ht == nil || !c.kids.mixedContexts() {
				t.Fatal("setup did not create mixed-context child table")
			}
			var diff *Tree
			var diags []yang.Diagnostic
			if module {
				diff, diags, err = tr.ValidateModuleDiff(ctx, "m", ValidateOptions{NotFinal: true})
			} else {
				diff, diags, err = tr.ValidateDiff(ctx, ValidateOptions{NotFinal: true, Present: true})
			}
			if err != nil || len(diags) != 0 {
				t.Fatalf("validation: %v, diagnostics: %v", err, diags)
			}
			if diff == nil || c.kids.len() != 5 {
				t.Fatalf("deletion/retention did not run: %s", dumpJSON(t, tr))
			}
			for i, got := range []string{dumpJSON(t, tr), dumpJSON(t, diff)} {
				var native, ported any
				if err := json.Unmarshal([]byte(lines[i+1]), &native); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(got), &ported); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(native, ported) {
					t.Fatalf("output %d: native %s; Go %s", i, lines[i+1], got)
				}
			}
		})
	}
}
