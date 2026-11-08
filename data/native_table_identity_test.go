// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Each live-oracle case corrupts complete traces, including equal corruption
// on both sides, through the shared validator and checker comparison path.
func TestNativeEntryAuditRejections(t *testing.T) {
	if os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER") == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	for _, name := range []string{"entry_request_binding_rejections", "schema_identity_rejections", "schema_registry_lineage_comparison", "duplicate_operand_rejections"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			//nolint:gosec // Fixed repository script and finite regression names.
			cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "sweep_test.py"),
				"IdentitySweep.test_"+name)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("entry audit regression: %v\n%s", err, out)
			} else {
				t.Logf("%s", out)
			}
		})
	}
}

// A retained registration supplies no sibling when physical storage is empty.
// The opaque variant has exactly the same residual winner, but a non-null start.
func TestNativeRetainedReadEntries(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	comparison := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER")
	if comparison == "" {
		comparison = filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	for label, path := range map[string]string{"instrumented": driver, "unmodified": comparison} {
		t.Run(label, func(t *testing.T) {
			t.Setenv("YANG_NATIVE_REGISTRATION_DRIVER", path)
			for _, name := range []string{"retained-null", "retained-opaque"} {
				t.Run(name, func(t *testing.T) {
					//nolint:gosec // Fixed repository request names.
					script, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", name+".calls"))
					if err != nil {
						t.Fatal(err)
					}
					rows := nativeDriver(t, string(script)) // Includes shared full-evidence validation.
					var reads, nullReads int
					for _, row := range rows {
						if row.Event != "read.schema" && row.Event != "read.null" {
							continue
						}
						var read struct {
							Winner int `json:"winner"`
							Before struct {
								Present bool `json:"present"`
								Used    int  `json:"used"`
								Chains  []struct {
									Node       int `json:"node"`
									Generation int `json:"generation"`
								} `json:"chains"`
							} `json:"before"`
							SchemaRC       int `json:"schema_rc"`
							SchemaWinner   int `json:"schema_winner"`
							InstanceRC     int `json:"instance_rc"`
							InstanceWinner int `json:"instance_winner"`
							ValueRC        int `json:"value_rc"`
							ValueWinner    int `json:"value_winner"`
						}
						if err := json.Unmarshal(row.Evidence, &read); err != nil {
							t.Fatal(err)
						}
						if !read.Before.Present || read.Before.Used != 1 || len(read.Before.Chains) != 1 || read.Before.Chains[0].Node != 2 {
							t.Fatalf("missing retained p registration: %+v", read.Before)
						}
						if row.Event == "read.null" {
							nullReads++
							if read.SchemaRC != 5 || read.InstanceRC != 5 || read.ValueRC != 5 || read.SchemaWinner != 0 || read.InstanceWinner != 0 || read.ValueWinner != 0 {
								t.Fatalf("null entry results: %+v", read)
							}
							continue
						}
						reads++
						winner, rc := 0, nativeStatus(5)
						if name == "retained-opaque" {
							winner, rc = read.Before.Chains[0].Generation, nativeOK
						}
						if read.Winner != winner || row.RC != rc {
							t.Fatalf("winner=%d rc=%d want %d/%d", read.Winner, row.RC, winner, rc)
						}
					}
					wantNull := 0
					if name == "retained-null" {
						wantNull = 1
					}
					if reads != 1 || nullReads != wantNull {
						t.Fatalf("schema reads=%d null reads=%d want 1/%d", reads, nullReads, wantNull)
					}
				})
			}
		})
	}
}

// Neither harness may choose an adjacent binary when the configured comparison
// lives elsewhere. Poison that fallback and the environment in the explicit
// argument lane; then test the environment-only lane and a missing control.
func TestNativeSeparateDriverPaths(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	comparison := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER")
	if comparison == "" {
		comparison = filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	directory := t.TempDir()
	paths := make([]string, 2)
	for i, original := range []string{driver, comparison} {
		//nolint:gosec // Explicitly configured local test executables.
		binary, err := os.ReadFile(original)
		if err != nil {
			t.Fatal(err)
		}
		parent := filepath.Join(directory, []string{"instrumented", "comparison"}[i])
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		paths[i] = filepath.Join(parent, "selected-driver")
		// Copy rather than symlink: Python resolves CLI paths, so a symlink
		// would quietly put both drivers back in their original directory.
		//nolint:gosec // The private copied driver must be executable.
		if err := os.WriteFile(paths[i], binary, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	poison := filepath.Join(filepath.Dir(paths[0]), "driver-unmodified")
	//nolint:gosec // This private test trap must be executable.
	if err := os.WriteFile(poison, []byte("#!/bin/sh\nexit 86\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, harness := range []struct {
		file string
		args []string
	}{
		{"sweep_test.py", []string{"IdentitySweep.test_retained_null_and_opaque_entries"}},
		{"check.py", []string{"--lifetime-case", "recreate-lookup"}},
	} {
		for _, mode := range []string{"arguments", "environment", "missing-comparison"} {
			t.Run(harness.file+"/"+mode, func(t *testing.T) {
				selected := paths[1]
				if mode == "missing-comparison" {
					selected = filepath.Join(directory, "missing-driver")
				}
				args := []string{filepath.Join("..", "conformance", "oracle", "native_registration", harness.file)}
				if mode == "arguments" {
					args = append(args, "--driver", paths[0], "--comparison-driver", selected)
					t.Setenv("YANG_NATIVE_REGISTRATION_DRIVER", poison)
					t.Setenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER", poison)
				} else {
					t.Setenv("YANG_NATIVE_REGISTRATION_DRIVER", paths[0])
					t.Setenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER", selected)
				}
				args = append(args, harness.args...)
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				//nolint:gosec // Fixed repository scripts and private test executables.
				cmd := exec.CommandContext(ctx, "python3", args...)
				out, err := cmd.CombinedOutput()
				if (err != nil) != (mode == "missing-comparison") {
					t.Fatalf("%s: %v\n%s", mode, err, out)
				}
			})
		}
	}
}

// Exercise the same full-evidence validator the Go wrapper calls before any
// nativeImage projection or slot indexing, including equal corrupt traces.
func TestNativeIdentityEvidenceRejections(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	script := "root 1 c\nterm 2 1 p old\nfree 2\nterm 2 1 p new\nterm 3 1 q Q\nterm 4 1 ll QQ==\nterm 5 1 ll Qg==\ndup 10 1 0 1\nprint 1\n"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	//nolint:gosec // Explicit opt-in local test executable.
	cmd := exec.CommandContext(ctx, driver, "--quiet")
	cmd.Stdin = bytes.NewBufferString(script)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var raw []json.RawMessage
	for _, line := range bytes.Split(bytes.TrimSpace(out), []byte{'\n'}) {
		raw = append(raw, bytes.Clone(line))
	}
	if err := nativeValidateEvidence(script, raw); err != nil {
		t.Fatalf("valid control: %v", err)
	}
	for _, mutation := range []string{"all-nodes", "all-tables", "bootstrap", "duplicate-provenance", "internal-survivor", "bindings", "zero-table", "collapsed-tables", "occurrence-generation", "occurrence-serial", "tombstone-link"} {
		t.Run(mutation, func(t *testing.T) {
			var rows []map[string]any
			for _, line := range raw {
				var row map[string]any
				if err := json.Unmarshal(line, &row); err != nil {
					t.Fatal(err)
				}
				rows = append(rows, row)
			}
			var dead any
			for _, row := range rows {
				for _, item := range nativeEvidenceArray(row["nodes"]) {
					node := item.(map[string]any)
					if node["alive"] == false {
						dead = node["generation"]
					}
				}
			}
			for _, row := range rows {
				if row["nodes"] == nil {
					continue
				}
				switch mutation {
				case "all-nodes":
					row["nodes"] = []any{}
				case "all-tables":
					row["tables"], row["table_count"] = []any{}, float64(0)
				case "bindings":
					row["bindings"] = []any{}
				case "collapsed-tables":
					for _, item := range nativeEvidenceArray(row["tables"]) {
						item.(map[string]any)["identity"] = float64(1)
					}
				}
				var kept []any
				for _, item := range nativeEvidenceArray(row["nodes"]) {
					n := item.(map[string]any)
					if mutation == "all-tables" {
						delete(n, "table")
					}
					if mutation == "duplicate-provenance" && n["generation"] == float64(2) {
						n["creation_ordinal"] = float64(1)
					}
					if mutation == "bootstrap" && n["creation_operation"] == float64(0) {
						continue
					}
					if mutation == "internal-survivor" && n["id"] == float64(0) && n["creation_operation"] != float64(0) {
						continue
					}
					if mutation == "tombstone-link" && n["id"] == float64(5) {
						n["parent"] = dead
					}
					if table, ok := n["table"].(map[string]any); ok && table["present"] == true {
						switch mutation {
						case "zero-table":
							table["identity"] = float64(0)
						case "occurrence-generation", "occurrence-serial":
							for _, item := range nativeEvidenceArray(table["chains"]) {
								field := "generation"
								if mutation == "occurrence-serial" {
									field = "occurrence"
								}
								item.(map[string]any)[field] = float64(0)
							}
						}
					}
					kept = append(kept, item)
				}
				row["nodes"] = kept
			}
			var mutant []json.RawMessage
			for _, row := range rows {
				encoded, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				mutant = append(mutant, encoded)
			}
			if err := nativeValidateEvidence(script, mutant); err == nil {
				t.Fatal("accepted malformed identity evidence")
			}
		})
	}
}

func nativeEvidenceArray(value any) []any {
	items, _ := value.([]any)
	return items
}

func TestNativeSchemaReadLineage(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "entry-schema-lineage.calls"))
	if err != nil {
		t.Fatal(err)
	}
	var reads int
	for _, row := range nativeDriver(t, string(script)) {
		if row.Event == "read.schema" {
			reads++
			if row.RC != 5 {
				t.Fatalf("foreign data-parent schema selected: %s", row.Evidence)
			}
		}
	}
	if reads != 4 {
		t.Fatalf("schema reads=%d want 4", reads)
	}
}
