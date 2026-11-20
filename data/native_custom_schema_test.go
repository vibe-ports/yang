// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These are live-oracle request cases. The list protocol has one value token;
// keyless lists ignore it, one-key lists consume it, larger keys are rejected
// before allocation, output preparation, or mutation on either native build.
func TestNativeDriverListArity(t *testing.T) {
	nativeVerifyConfiguredDriverRoles(t)
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set native drivers")
	}
	for keys := 0; keys <= 3; keys++ {
		for _, nested := range []bool{false, true} {
			body := "config false;"
			if keys > 0 {
				body = "key \"" + strings.Join(strings.Split("abc"[:keys], ""), " ") + "\";"
			}
			for _, key := range "abc"[:keys] {
				body += fmt.Sprintf("leaf %c {type string;}", key)
			}
			body = "list pair {" + body + "}"
			parent := 0
			if nested {
				body = "container p {" + body + "}"
				parent = 1
			}
			prefix := "context module m {yang-version 1.1;namespace urn:m;prefix m;" + body + "}\n"
			if nested {
				prefix += "root 1 p\n"
			}
			for build, path := range map[string]string{"instrumented": driver, "unmodified": nativeComparisonDriver()} {
				for _, mode := range []string{"--quiet", "--events"} {
					t.Run(fmt.Sprintf("keys%d/nested%t/%s/%s", keys, nested, build, mode), func(t *testing.T) {
						execute := func(script string) ([]json.RawMessage, int) {
							t.Helper()
							ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
							defer cancel()
							//nolint:gosec // Opt-in native driver, bounded repository requests.
							cmd := exec.CommandContext(ctx, path, mode, "--read-schema")
							cmd.Stdin = strings.NewReader(script)
							out, err := nativeCommandOutput(cmd)
							exit := 0
							if err != nil {
								var failure *exec.ExitError
								ok := errors.As(err, &failure)
								if !ok {
									t.Fatal(err)
								}
								exit = failure.ExitCode()
							}
							rows, err := nativeTranscriptRows(out)
							if err != nil {
								t.Fatal(err)
							}
							return rows, exit
						}
						before, exit := execute(prefix)
						if exit != 0 {
							t.Fatalf("setup exit=%d", exit)
						}
						if err := nativeValidateEvidence(prefix, before); err != nil {
							t.Fatal(err)
						}
						command := fmt.Sprintf("list 2 %d pair A", parent)
						for _, suffix := range []string{"", " B", " B C"} {
							script := prefix + command + suffix + "\n"
							rows, exit := execute(script)
							if keys > 1 || suffix != "" {
								if exit != 4 || !reflect.DeepEqual(rows, before) {
									t.Fatalf("unrepresentable request: exit=%d want=4, changed prefix=%t", exit, !reflect.DeepEqual(rows, before))
								}
							} else {
								if exit != 0 {
									t.Fatalf("representable request exit=%d", exit)
								}
								if err := nativeValidateEvidence(script, rows); err != nil {
									t.Fatal(err)
								}
								var last struct {
									RC int `json:"rc"`
								}
								if err := json.Unmarshal(rows[len(rows)-1], &last); err != nil || last.RC != 0 {
									t.Fatalf("list constructor rc=%d: %v", last.RC, err)
								}
							}
						}
					})
				}
			}
		}
	}
}

func TestNativeCustomSchemaKinds(t *testing.T) {
	for _, name := range []string{"reused-names", "same-name-ancestors", "existing-key", "ignored-descendants"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "custom", name+".calls"))
			if err != nil {
				t.Fatal(err)
			}
			script := string(b)
			for _, verbose := range []bool{false, true} {
				left := nativeDriverReplay(t, script, "", verbose)
				right := nativeDriverReplayPath(t, script, "", verbose, nativeComparisonDriver())
				if err := nativeG2CompareEvidence(script, left, right); err != nil {
					t.Fatal(err)
				}
				if name == "existing-key" {
					nativeExistingKeyAliases(t, left)
					nativeExistingKeyAliases(t, right)
				}
				if name == "ignored-descendants" {
					nativeShallowCopyNodes(t, left)
					nativeShallowCopyNodes(t, right)
				}
			}
		})
	}
}

// Native key duplication returns an existing destination key; copying list
// ancestors first creates a different key in that destination context. These
// independent identity/status assertions keep the positive controls nonvacuous.
func nativeExistingKeyAliases(t *testing.T, rows []nativeDriverResult) {
	t.Helper()
	var last struct {
		Bindings []struct{ Handle, Generation int }
		Nodes    []struct {
			Generation, Parent int
			Context            int `json:"context_generation"`
			Key                bool
			Table              struct {
				Present bool
				Chains  []struct{ Generation int }
			}
		}
	}
	if err := json.Unmarshal(rows[len(rows)-1].Evidence, &last); err != nil {
		t.Fatal(err)
	}
	bindings := make(map[int]int)
	for _, binding := range last.Bindings {
		bindings[binding.Handle] = binding.Generation
	}
	keys, parents, contexts := make(map[int]bool), make(map[int]int), make(map[int]int)
	tables, indexed := make(map[int]bool), make(map[int]map[int]bool)
	for _, node := range last.Nodes {
		keys[node.Generation], parents[node.Generation], contexts[node.Generation] = node.Key, node.Parent, node.Context
		tables[node.Generation] = node.Table.Present
		indexed[node.Generation] = make(map[int]bool)
		for _, record := range node.Table.Chains {
			indexed[node.Generation][record.Generation] = true
		}
	}
	target, copied := bindings[13], bindings[93]
	if target == 0 || !keys[target] {
		t.Fatal("missing existing destination key")
	}
	for _, handle := range []int{90, 91, 92} {
		if bindings[handle] != target {
			t.Fatalf("handle %d did not alias the existing key", handle)
		}
	}
	if copied == 0 || copied == target || copied == bindings[3] || !keys[copied] || contexts[copied] != contexts[target] || parents[parents[copied]] != bindings[11] {
		t.Fatal("copied list ancestor did not supply a new destination key")
	}
	equalities := 0
	for _, row := range rows {
		if row.Event == "read.equal" {
			equalities++
			if row.RC != 0 {
				t.Fatalf("key equality returned %d", row.RC)
			}
		}
	}
	if !tables[bindings[12]] || !indexed[bindings[12]][target] || tables[parents[copied]] {
		t.Fatal("key controls did not reach both indexed and linear lookup paths")
	}
	if equalities != 8 {
		t.Fatalf("key equality count=%d, want 8", equalities)
	}
}

// Nonrecursive containers and opaque nodes ignore descendants, including keys.
// Pin both the ignored inputs and one data allocation per successful copy.
func nativeShallowCopyNodes(t *testing.T, rows []nativeDriverResult) {
	t.Helper()
	var last struct {
		Bindings []struct{ Handle, Generation int }
		Nodes    []struct {
			Generation, First int
			Key               bool
			Operation         int `json:"creation_operation"`
		}
	}
	if err := json.Unmarshal(rows[len(rows)-1].Evidence, &last); err != nil {
		t.Fatal(err)
	}
	bindings, first, born, counts := make(map[int]int), make(map[int]int), make(map[int]int), make(map[int]int)
	keys := make(map[int]bool)
	for _, binding := range last.Bindings {
		bindings[binding.Handle] = binding.Generation
	}
	for _, node := range last.Nodes {
		first[node.Generation], born[node.Generation] = node.First, node.Operation
		counts[node.Operation]++
		keys[node.Generation] = node.Key
	}
	if first[bindings[9]] == 0 || first[bindings[10]] == 0 || !keys[first[bindings[9]]] || !keys[first[bindings[10]]] {
		t.Fatal("shallow sources lost their ignored descendants")
	}
	for _, handle := range []int{20, 21, 22, 23} {
		generation := bindings[handle]
		if generation == 0 || first[generation] != 0 || counts[born[generation]] != 1 {
			t.Fatalf("shallow copy %d must allocate one empty node", handle)
		}
	}
	prints, equalities := 0, 0
	for _, row := range rows {
		if row.Event == "dup" || row.Event == "dup-to-context" || row.Event == "public.result" || row.Event == "read.equal" {
			if row.RC != 0 {
				t.Fatalf("shallow control status=%d", row.RC)
			}
		}
		if row.Event == "public.result" {
			prints++
		}
		if row.Event == "read.equal" {
			equalities++
		}
	}
	if equalities != 4 {
		t.Fatalf("shallow equality count=%d, want 4", equalities)
	}
	if prints != 4 {
		t.Fatalf("shallow public result count=%d, want 4", prints)
	}
}

// Named live fixtures exercise the checker independently of the exhaustive
// admission matrix, so sensitivity controls retain their bounded run budget.
func TestNativeCustomSchemaCheckerCases(t *testing.T) {
	if os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER") == "" {
		t.Skip("set native drivers")
	}
	t.Run("unknown-case", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "custom_schema_test.py"), "--case", "unknown")
		_, err := nativeCommandOutput(cmd)
		var failure *exec.ExitError
		if !errors.As(err, &failure) || failure.ExitCode() != 2 || !strings.Contains(err.Error(), "invalid choice") {
			t.Fatalf("unknown custom schema case must be rejected: %v", err)
		}
	})
	for _, name := range []string{"reused-names", "same-name-ancestors"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "custom_schema_test.py"), "--case", name)
			out, err := nativeCommandOutput(cmd)
			if err != nil {
				t.Fatalf("custom schema checker case: %v\n%s", err, out)
			}
			var summary struct {
				Name  string `json:"custom_schema_case"`
				Runs  int    `json:"driver_runs"`
				Skips int    `json:"skips"`
			}
			if err := json.Unmarshal(out, &summary); err != nil || summary.Name != name || summary.Runs != 4 || summary.Skips != 0 {
				t.Fatalf("incomplete checker case: %v, %s", err, out)
			}
		})
	}
}

func TestNativeCustomSchemaSweep(t *testing.T) {
	if os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER") == "" {
		t.Skip("set native drivers")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "custom_schema_test.py"))
	out, err := nativeCommandOutput(cmd)
	if err != nil {
		t.Fatalf("custom schema sweep: %v\n%s", err, out)
	}
	var summary struct {
		Passed    bool `json:"custom_schema_sweep"`
		Names     int  `json:"names"`
		Kinds     int  `json:"kinds"`
		Skips     int  `json:"skips"`
		Valid     int  `json:"valid_pairs"`
		Rejected  int  `json:"rejected_pairs"`
		Runs      int  `json:"driver_runs"`
		Admission struct {
			Aliased          int `json:"aliased_commands"`
			Outputs          int `json:"output_alias_commands"`
			Historical       int `json:"historical_alias_commands"`
			SourceKinds      int `json:"source_kinds"`
			DestinationKinds int `json:"destination_kinds"`
			Ancestries       int `json:"ancestries"`
			Valid            int `json:"valid_pairs"`
			Rejected         int `json:"rejected_pairs"`
			Runs             int `json:"driver_runs"`
			Skips            int `json:"skips"`
		} `json:"admission"`

		Preconditions struct {
			Contexts int `json:"contexts"`
			Commands int `json:"commands"`
			Valid    int `json:"valid_pairs"`
			Rejected int `json:"rejected_pairs"`
			Runs     int `json:"driver_runs"`
			Skips    int `json:"skips"`
		} `json:"preconditions"`
	}
	if err := json.Unmarshal(out, &summary); err != nil || !summary.Passed || summary.Names != 21 || summary.Kinds != 9 || summary.Skips != 0 || summary.Valid != 470 || summary.Rejected != 672 || summary.Runs != 2284 {
		t.Fatalf("incomplete sweep: %v, %s", err, out)
	}
	if p := summary.Preconditions; p.Contexts != 3 || p.Commands != 17 || p.Valid != 756 || p.Rejected != 2774 || p.Runs != 7320 || p.Skips != 0 {
		t.Fatalf("incomplete input validation sweep: %+v", p)
	}
	if a := summary.Admission; a.Aliased != 15 || a.Outputs != 5 || a.Historical != 4 || a.SourceKinds != 9 || a.DestinationKinds != 10 || a.Ancestries != 3 || a.Valid != 108 || a.Rejected != 764 || a.Runs != 3488 || a.Skips != 0 {
		t.Fatalf("incomplete destination/alias sweep: %+v", a)
	}
	t.Logf("%s", out)
}
