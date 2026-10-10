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

func nativeRequestRunner(t *testing.T) func(string, map[string]any) (Response, error) {
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
