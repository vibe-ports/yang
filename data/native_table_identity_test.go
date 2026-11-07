// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
)

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
