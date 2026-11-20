// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeEvidenceEntryContracts(t *testing.T) {
	if os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER") == "" {
		t.Skip("set native drivers after isolated build")
	}
	for _, name := range []string{"test_terminal_boundary", "test_boundary_event_types", "test_mutation_copy_work", "test_native_roles", "test_explicit_reuse_roles", "test_empty_cli", "test_sensitivity_attribution", "test_completion_work"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			//nolint:gosec // Fixed repository regression driver and fixed named controls.
			cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "entry_evidence_test.py"), "EntryEvidence."+name)
			out, err := nativeCommandOutput(cmd)
			if err != nil {
				t.Fatalf("native evidence entry contracts: %v\n%s", err, out)
			}
			t.Logf("%s", out)
		})
	}

}

func TestNativeEvidenceRoles(t *testing.T) {
	primary := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if primary == "" {
		t.Skip("set native drivers after isolated build")
	}
	if selected := os.Getenv("YANG_NATIVE_ROLE_PATH_CHILD"); selected != "" {
		nativeDriverReplayPath(t, "equal 0 0 1\n", "", false, selected)
		return
	}
	if err := nativeDriverRole(primary, 1); err != nil {
		t.Fatal(err)
	}
	if err := nativeDriverRole(nativeComparisonDriver(), 0); err != nil {
		t.Fatal(err)
	}
	for expected, path := range []string{nativeComparisonDriver(), primary} {
		if role, err := nativeReplayPathRole(path); err != nil || role != expected {
			t.Fatalf("configured replay role=%d want=%d: %v", role, expected, err)
		}
	}
	if _, err := nativeReplayPathRole(filepath.Join(t.TempDir(), "unconfigured-driver")); err == nil {
		t.Fatal("accepted an unconfigured native replay executable")
	}
	if err := nativeDriverRole(primary, 0); err == nil {
		t.Fatal("accepted instrumented executable as the unmodified control")
	}
	binary, err := os.ReadFile(primary)
	if err != nil {
		t.Fatal(err)
	}
	unconfigured := filepath.Join(t.TempDir(), "unconfigured-driver")
	if err := os.WriteFile(unconfigured, binary, 0600); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // Private copied native driver exercises actual role-bound replay dispatch.
	if err := os.Chmod(unconfigured, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	//nolint:gosec // Current test executable and fixed selector exercise the actual replay wrapper.
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeEvidenceRoles$")
	child.Env = append(os.Environ(), "YANG_NATIVE_ROLE_PATH_CHILD="+unconfigured)
	rejected, failure := nativeCommandOutput(child)
	if failure == nil {
		t.Fatal("accepted an unconfigured native replay executable through wrapper")
	}
	if !strings.Contains(string(rejected), "native replay path is not a configured driver") {
		t.Fatalf("unrelated replay path failure: %v\n%s", failure, rejected)
	}
	//nolint:gosec // Current test executable and fixed selector probe the shared replay entry point.
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeG2ProtocolSmoke$")
	cmd.Env = append(os.Environ(), "YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER="+primary)
	out, err := nativeCommandOutput(cmd)
	if err == nil {
		t.Fatal("focused replay compared the instrumented driver against itself")
	}
	if !strings.Contains(string(out), "native ABI/role mismatch") {
		t.Fatalf("focused replay failed before its native role rejection: %v\n%s", err, out)
	}
}
