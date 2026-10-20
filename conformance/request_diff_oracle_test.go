// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var nativeRequestTimeout = 15 * time.Second

func TestMain(m *testing.M) {
	if os.Getenv("YANG_ORACLE_DEADLINE_STANDIN") == "1" {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func nativeRequestRunner(t testing.TB) func(string, map[string]any) (Response, error) {
	t.Helper()
	oracle, err := filepath.Abs(OracleBinary("oracle"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oracle); err != nil {
		if os.Getenv("YANG_ORACLE_REQUIRED") != "" {
			t.Fatal(err)
		}
		t.Skip("oracle not built; run in the dev container with YANG_ORACLE_REQUIRED=1")
	}
	return func(base string, p map[string]any) (Response, error) { return runNativeRequest(oracle, base, p) }
}

func nativeRequestAvailable() bool { return true }

func runNativeRequest(oracle, base string, p map[string]any) (Response, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), nativeRequestTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, oracle)
	cmd.Dir = base
	cmd.Stdin = bytes.NewReader(b)
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("oracle transport deadline: %w", ctx.Err())
	}
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 2) {
		return nil, fmt.Errorf("oracle transport: %w: %s", err, out)
	}
	resp, parseErr := ParseResponse(out)
	if parseErr != nil {
		return nil, parseErr
	}
	if archErr := CheckOracleArch(resp, false); archErr != nil {
		return resp, archErr
	}
	if resp["verdict"] == nil {
		return resp, fmt.Errorf("oracle omitted verdict: %s", out)
	}
	return resp, nil
}

func TestRunNativeRequestDeadlineIsTransport(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("YANG_ORACLE_DEADLINE_STANDIN", "1")
	old := nativeRequestTimeout
	nativeRequestTimeout = 20 * time.Millisecond
	t.Cleanup(func() { nativeRequestTimeout = old })

	p := map[string]any{
		"op": "sequence",
		"steps": []any{
			map[string]any{"do": "parse", "data": `{"pv2:c":{"mode":"on"}}`},
			map[string]any{"do": "dup", "node": "/pv2:c", "parent": "mode"},
		},
	}
	_, nativeErr := runNativeRequest(exe, t.TempDir(), p)
	if !errors.Is(nativeErr, context.DeadlineExceeded) || !strings.Contains(nativeErr.Error(), "deadline") {
		t.Fatalf("runNativeRequest error = %v, want wrapped deadline", nativeErr)
	}
	got := Response{
		"verdict": "invalid", "failed_step": 1, "rc": codeJSON("LY_EINVAL"),
		"steps": []any{map[string]any{"do": "parse", "rc": codeJSON("LY_SUCCESS")}, map[string]any{"do": "dup", "rc": codeJSON("LY_EINVAL")}},
	}
	if category, class := inventoryCaseClassification(p, nil, nativeErr, got, nil, "changed setup"); category != "A" || class != "native-transport" {
		t.Fatalf("deadline classification = %s/%s, want A/native-transport", category, class)
	}
}

// TestExplicitEmptyRevisionLoaderStages is a live-oracle fixture table for revision-presence
// behavior whose diagnostics include details deliberately ignored by corpus normalization. It
// compares the complete diagnostic messages without writing golden files.
func TestExplicitEmptyRevisionLoaderStages(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := filepath.Join("corpus", "protocol-v2")
	tests := []struct {
		id      string
		params  map[string]any
		message string
	}{
		{
			id: "protocol-v2/empty-revision-search-first-directory",
			params: map[string]any{"op": "schema", "searchdirs": []any{"empty-rev-search-newer", "empty-rev-search-first"},
				"modules": []any{map[string]any{"name": "pv2-empty-rev-parse", "revision": ""}}},
			message: `Module "pv2-empty-rev-parse" parsed with the wrong revision ("2020-01-01" instead "").`,
		},
		{
			id: "protocol-v2/empty-revision-existing-module-collision",
			params: map[string]any{"op": "schema", "searchdirs": []any{"schemas"},
				"modules": []any{map[string]any{"name": "pv2-empty-rev-collision-a"},
					map[string]any{"name": "pv2-empty-rev-collision", "revision": ""}}},
			message: `Name collision between module and submodule of name "pv2-empty-rev-collision-a".`,
		},
		{
			id: "protocol-v2/empty-revision-module-not-found",
			params: map[string]any{"op": "schema", "searchdirs": []any{"schemas"},
				"modules": []any{map[string]any{"name": "pv2-empty-rev-not-found", "revision": ""}}},
			message: `Loading "pv2-empty-rev-not-found@" module failed, not found.`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			want, err := oracle(base, tt.params)
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{ID: tt.id, BaseDir: base, Params: tt.params})
			if err != nil {
				t.Fatal(err)
			}
			want, _ = withoutSkipped(want, Yang{}.SkippedFields("schema"))
			got, _ = withoutSkipped(got, Yang{}.SkippedFields("schema"))
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
			}
			for label, response := range map[string]Response{"oracle": want, "engine": got} {
				diagnostics := response.Diagnostics()
				if len(diagnostics) == 0 || diagnostics[0]["msg"] != tt.message {
					t.Errorf("%s first diagnostic = %#v, want msg %q", label, diagnostics, tt.message)
				}
			}
		})
	}
}

// The oracle and engine must inspect the same directory: readdir order is
// filesystem-defined and can differ from filename order.
func TestExplicitEmptyRevisionNativeDirectoryOrder(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := t.TempDir()
	const valid = "native-order@2020-01-01.yang"
	const malformed = "native-order@2021-01-01.yang"
	for _, file := range []struct{ name, source string }{
		{malformed, "module native-order { this is malformed; }"},
		{valid, "module native-order { namespace urn:native-order; prefix n; revision 2020-01-01; }"},
	} {
		if err := os.WriteFile(filepath.Join(base, file.name), []byte(file.source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if len(entries) != 2 {
		t.Fatalf("directory has %d entries, want 2", len(entries))
	}
	params := map[string]any{"op": "schema", "searchdirs": []any{"."},
		"modules": []any{map[string]any{"name": "native-order", "revision": ""}}}
	want, err := oracle(base, params)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Yang{}).Run(Request{ID: "native-order/explicit-empty-revision", BaseDir: base, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	want, _ = withoutSkipped(want, Yang{}.SkippedFields("schema"))
	got, _ = withoutSkipped(got, Yang{}.SkippedFields("schema"))
	if diff := diffResponses(want, got); diff != "" {
		t.Fatalf("native first entry %q: oracle mismatch: %s\noracle: %#v\nengine: %#v", entries[0].Name(), diff, want, got)
	}
	diagnostics := want.Diagnostics()
	if len(diagnostics) == 0 {
		t.Fatal("oracle returned no diagnostics")
	}
	wrongRevision := `Module "native-order" parsed with the wrong revision ("2020-01-01" instead "").`
	if first := entries[0].Name(); (first == valid) != (diagnostics[0]["msg"] == wrongRevision) {
		t.Fatalf("native first entry %q, oracle first diagnostic = %#v", first, diagnostics[0])
	}
}
