// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package opidx

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"
)

// Seq is original Go infrastructure, with no native label/budget/journal API.
// The independent slice model checks every edit and rollback before these
// supplemental live requests check libyang's RFC 7950 section 7.7.7 user-ordered
// leaf-list parsing. Request data comes only from the model, never Seq snapshots.
// These parse requests do not validate Seq's internal edit/journal primitives.
// No golden files are generated.
func TestSequenceRound2LiveOracle(t *testing.T) {
	oracle, err := filepath.Abs(filepath.Join("..", "..", "conformance", "oracle", "lyoracle-"+runtime.GOARCH))
	if err != nil {
		t.Fatal(err)
	}
	// Oracle executable suffixes use uname -m, rather than Go architecture names.
	switch runtime.GOARCH {
	case "amd64":
		oracle = filepath.Join(filepath.Dir(oracle), "lyoracle-x86_64")
	case "arm64":
		oracle = filepath.Join(filepath.Dir(oracle), "lyoracle-aarch64")
	}
	if _, err := os.Stat(oracle); err != nil {
		if os.Getenv("YANG_ORACLE_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip("oracle not built; run make test-oracle inside the dev container")
	}
	dir := t.TempDir()
	const module = `module seq-round2 {
  yang-version 1.1;
  namespace "urn:seq-round2";
  prefix s;
  leaf-list v { type int32; ordered-by user; }
}`
	if err := os.WriteFile(filepath.Join(dir, "seq-round2.yang"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshots := sequenceEditModelSnapshots(t)
	steps := make([]map[string]any, len(snapshots))
	for i, order := range snapshots {
		data, err := json.Marshal(map[string]any{"seq-round2:v": order})
		if err != nil {
			t.Fatal(err)
		}
		steps[i] = map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true, "data": string(data)}
	}
	request, err := json.Marshal(map[string]any{
		"op": "sequence", "searchdirs": []string{dir},
		"modules": []map[string]string{{"name": "seq-round2"}}, "steps": steps,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, oracle) //nolint:gosec // fixed repository oracle executable, never request input
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(request)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle request: %v\n%s", err, out)
	}
	var response struct {
		Verdict string `json:"verdict"`
		Steps   []struct {
			Typed []struct {
				Value struct {
					Canonical string `json:"canonical"`
				} `json:"value"`
			} `json:"typed"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		t.Fatal(err)
	}
	if response.Verdict != "valid" || len(response.Steps) != len(snapshots) {
		t.Fatalf("oracle rejected independent model states: %s", out)
	}
	for i, step := range response.Steps {
		got := make([]int, 0, len(step.Typed))
		for _, node := range step.Typed {
			value, err := strconv.Atoi(node.Value.Canonical)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, value)
		}
		if !slices.Equal(got, snapshots[i]) {
			t.Fatalf("snapshot %d: native order %v, independent model order %v", i, got, snapshots[i])
		}
	}
}
