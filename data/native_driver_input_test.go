// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These are live-oracle requests, including existing guarded positions. Reject
// the final request before any primitive, result, snapshot or output binding.
func TestNativeDriverNodeKinds(t *testing.T) {
	nativeVerifyConfiguredDriverRoles(t)
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set native drivers")
	}
	cases, err := filepath.Glob(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "input", "*.calls"))
	if err != nil || len(cases) != 42 {
		t.Fatalf("node-kind case inventory: %d, %v", len(cases), err)
	}
	for _, file := range cases {
		script, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(string(script)), "\n")
		prefix := strings.Join(lines[:len(lines)-1], "\n") + "\n"
		for build, path := range map[string]string{"instrumented": driver, "unmodified": nativeComparisonDriver()} {
			for _, mode := range []string{"--quiet", "--events"} {
				t.Run(filepath.Base(file)+"/"+build+"/"+mode, func(t *testing.T) {
					execute := func(input string) ([]byte, error) {
						t.Helper()
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						//nolint:gosec // Opt-in native driver and repository-owned bounded requests.
						cmd := exec.CommandContext(ctx, path, mode, "--read-schema")
						cmd.Env = append(os.Environ(), "GLIBC_TUNABLES=glibc.malloc.tcache_count=7")
						cmd.Stdin = strings.NewReader(input)
						return nativeCommandOutput(cmd)
					}
					before, err := execute(prefix)
					prefixExit := 0
					if strings.HasSuffix(file, "term-reuse-target.calls") || strings.HasSuffix(file, "insert-reuse-target.calls") {
						prefixExit = 100 // EOF with a reserved reuse target, before construction.
					}
					gotExit := 0
					if err != nil {
						var exit *exec.ExitError
						ok := errors.As(err, &exit)
						if !ok {
							t.Fatal(err)
						}
						gotExit = exit.ExitCode()
					}
					if gotExit != prefixExit {
						t.Fatalf("setup exit %d, want %d: %v", gotExit, prefixExit, err)
					}
					after, err := execute(string(script))
					var exit *exec.ExitError
					ok := errors.As(err, &exit)
					if !ok || exit.ExitCode() != 4 {
						t.Fatalf("wrong-kind request must exit 4: %v\n%s", err, after)
					}
					// Allocation addresses and build-path diagnostics never enter stdout.
					// Comparing decoded streams also proves the input never reached a
					// mutation hook, a read result, or a completed command snapshot.
					decode := func(output []byte) []json.RawMessage {
						t.Helper()
						rows, err := nativeDecodePrefix(prefix, output)
						if err != nil {
							t.Fatal(err)
						}
						return rows
					}
					if !reflect.DeepEqual(decode(before), decode(after)) {
						t.Fatal("rejected input changed setup evidence")
					}
				})
			}
		}
	}
}
