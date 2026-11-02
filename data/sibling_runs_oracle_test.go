// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Each replay compares every intermediate document order with pinned libyang. The ordered
// native parser supplies the layout that Go can currently construct only inside this package.
func TestRunNativeReplay(t *testing.T) {
	if os.Getenv("YANG_ORACLE_REQUIRED") == "" {
		t.Skip("requires live oracle in dev container")
	}
	prefix := os.Getenv("LIBYANG_PREFIX")
	if prefix == "" {
		prefix = "/opt/libyang"
	}
	base := t.TempDir()
	model := filepath.Join(base, "run-test.yang")
	if err := os.WriteFile(model, []byte(runSchema), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(base, "runs-probe")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cc", "-O2", "-Wall", "-Wextra", "-Werror", "-std=c11", "-I"+filepath.Join(prefix, "include"), "../conformance/oracle/sibling_runs.c", "-L"+filepath.Join(prefix, "lib"), "-Wl,-rpath,"+filepath.Join(prefix, "lib"), "-lyang", "-o", probe)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v: %s", err, out)
	}
	set := reviewDiffSet(t, runSchema)
	for _, tc := range []struct {
		name         string
		input, steps []string
	}{
		{"split-no-table", []string{"ll=4", "zz", "ll=9"}, []string{"add=3", "add=2", "del=1", "add=5"}},
		{"split-table", []string{"a=x", "b=y", "ll=4", "ll=2", "zz", "ll=3", "z=z"}, []string{"add=1", "add=0", "del=2", "add=5"}},
		{"singleton", []string{"zz", "ll=1", "yy", "ll=9", "ll=10", "ll=11"}, []string{"add=2", "del=2", "del=1", "add=0"}},
		{"unlink-between", []string{"ll=4", "ll=2", "zz", "ll=9", "ll=10"}, []string{"add=3", "del=3", "add=5"}},
		{"schema-separator", []string{"ll=4", "ll=2", "b=y", "ll=9", "ll=10"}, []string{"add=3", "del=3", "add=5"}},
		{"cyclic-singleton", []string{"ll=1", "zz", "ll=9", "ll=10", "ll=11"}, []string{"add=2", "del=1", "del=0", "add=0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var xml strings.Builder
			xml.WriteString(`<c xmlns="urn:run-test">`)
			for _, item := range tc.input {
				name, value, ok := strings.Cut(item, "=")
				if !ok {
					value = "u"
				}
				xml.WriteString("<" + name + ">" + value + "</" + name + ">")
			}
			xml.WriteString("</c>")
			args := append([]string{model, xml.String()}, tc.steps...)
			var diagnostics bytes.Buffer
			command := exec.CommandContext(ctx, probe, args...)
			command.Stderr = &diagnostics
			native, err := command.Output()
			if err != nil {
				t.Fatalf("native: %v: stdout=%s stderr=%s", err, native, diagnostics.String())
			}
			if diagnostics.Len() != 0 {
				t.Logf("native diagnostics: %s", diagnostics.String())
			}
			tr, c := runTree(t, set, tc.input...)
			var got strings.Builder
			printState := func() {
				for _, item := range runNames(c) {
					got.WriteString(item + "\n")
				}
				got.WriteString("--\n")
			}
			printState()
			for _, step := range tc.steps {
				op, value, _ := strings.Cut(step, "=")
				if op == "add" {
					_, err = tr.NewTerm(c, "ll", value, NewOptions{})
				} else {
					at, parseErr := strconv.Atoi(value)
					if parseErr != nil {
						t.Fatal(parseErr)
					}
					err = c.kids.docSpan().At(at).Unlink()
				}
				if err != nil {
					t.Fatal(err)
				}
				opidxAssertTreeInvariant(t, tr)
				printState()
			}
			if got.String() != string(native) {
				t.Fatalf("native:\n%sGo:\n%s", native, got.String())
			}
			t.Logf("%d intermediate states match pinned native", len(tc.steps)+1)
		})
	}
}
