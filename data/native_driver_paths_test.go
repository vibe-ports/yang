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

func TestNativeComparisonDriverResolution(t *testing.T) {
	for _, setting := range []string{"unset", "empty", "explicit", "unconfigured"} {
		t.Run(setting, func(t *testing.T) {
			t.Setenv("YANG_NATIVE_REGISTRATION_DRIVER", filepath.Join("selected", "driver"))
			t.Setenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER", "")
			want := filepath.Join("selected", "driver-unmodified")
			switch setting {
			case "unset":
				if err := os.Unsetenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER"); err != nil {
					t.Fatal(err)
				}
			case "explicit":
				want = filepath.Join("elsewhere", "control")
				t.Setenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER", want)
			case "unconfigured":
				t.Setenv("YANG_NATIVE_REGISTRATION_DRIVER", "")
				want = ""
			}
			if got := nativeComparisonDriver(); got != want {
				t.Fatalf("comparison driver=%q, want %q", got, want)
			}
		})
	}
}

// Exercise the Python field inventory replay entry with only the instrumented
// setting. Both native peers must run for unset and empty comparisons.
func TestNativeComparisonDriverFallbackCommand(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set native drivers")
	}
	driver, err := filepath.Abs(driver)
	if err != nil {
		t.Fatal(err)
	}
	for _, setting := range []string{"unset", "empty"} {
		t.Run(setting, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", "-c", `
from evidence_test import replay
from identity import validate
script = 'root 1 c\nterm 2 1 p same\nread-instance 2 2 1\n'
left, right = replay(script), replay(script, plain=True)
assert validate(script, left) == validate(script, right)
assert any(r['event'] == 'read.selection.instance' for r in left)
assert any(r['event'] == 'read.selection.instance' for r in right)
print('comparison_driver_live_fallback: both native peers passed')
`)
			cmd.Dir = filepath.Join("..", "conformance", "oracle", "native_registration")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "YANG_NATIVE_REGISTRATION_DRIVER=") && !strings.HasPrefix(entry, "YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "YANG_NATIVE_REGISTRATION_DRIVER="+driver)
			if setting == "empty" {
				cmd.Env = append(cmd.Env, "YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER=")
			}
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "comparison_driver_live_fallback") || strings.Contains(string(out), "--- SKIP:") {
				t.Fatalf("single-variable Python replay: %v\n%s", err, out)
			}
			t.Logf("%s comparison: Go/Python replay passed with both native peers", setting)
		})
	}
}
