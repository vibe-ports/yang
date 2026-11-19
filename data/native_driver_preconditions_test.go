// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Live-oracle requests for unchecked native preconditions. Each last command
// used to enter native code on 1b41fab; rejection must preserve the exact prefix.
func TestNativeDriverPreconditions(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set native drivers")
	}
	cases, err := filepath.Glob(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "preconditions", "*.calls"))
	if err != nil || len(cases) != 62 {
		t.Fatalf("native-precondition case inventory: %d, %v", len(cases), err)
	}
	for _, file := range cases {
		script, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(file), func(t *testing.T) {
			nativePreconditionRequest(t, string(script))
		})
	}
}

// Each named admission case entered native code or changed links on fa9d6c2.
// Both builds must refuse it without changing any completed prefix evidence.
func TestNativeDriverAdmission(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "admission", "*.calls"))
	if err != nil || len(cases) != 21 {
		t.Fatalf("admission inventory: %d, %v", len(cases), err)
	}
	for _, file := range cases {
		script, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(filepath.Base(file), func(t *testing.T) { nativePreconditionRequest(t, string(script)) })
	}
}

func nativePreconditionRequest(t *testing.T, script string) {
	t.Helper()
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set native drivers")
	}
	lines := strings.Split(strings.TrimSpace(script), "\n")
	prefix := strings.Join(lines[:len(lines)-1], "\n") + "\n"
	for build, path := range map[string]string{"instrumented": driver, "unmodified": nativeComparisonDriver()} {
		for _, mode := range []string{"--quiet", "--events"} {
			t.Run(build+"/"+mode, func(t *testing.T) {
				execute := func(input string) ([]byte, error) {
					t.Helper()
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					//nolint:gosec // Opt-in native driver and repository-owned bounded requests.
					cmd := exec.CommandContext(ctx, path, mode, "--read-schema")
					cmd.Env = append(os.Environ(), "GLIBC_TUNABLES=glibc.malloc.tcache_count=7")
					cmd.Stdin = strings.NewReader(input)
					return cmd.Output()
				}
				before, err := execute(prefix)
				gotExit := 0
				if err != nil {
					exit, ok := err.(*exec.ExitError)
					if !ok {
						t.Fatal(err)
					}
					gotExit = exit.ExitCode()
				}
				if gotExit != 0 {
					t.Fatalf("setup exit %d, want %d: %v", gotExit, 0, err)
				}
				after, err := execute(script)
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 4 {
					t.Fatalf("unsafe native request must exit 4: %v\n%s", err, after)
				}
				// Allocation addresses and build-path diagnostics never enter stdout.
				// Comparing decoded streams also proves the input never reached a
				// mutation hook, a read result, or a completed command snapshot.
				decode := func(output []byte) []map[string]any {
					t.Helper()
					var rows []map[string]any
					for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
						var row map[string]any
						if err := json.Unmarshal([]byte(line), &row); err != nil {
							t.Fatal(err)
						}
						rows = append(rows, row)
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

// Supported cross-context schema lookup must validate on both the linear and
// children-table paths. The native callback compares ancestry; the linear path
// compares module/name, as pinned in tree_data_common.c.
func TestNativeDriverForeignSchemaReads(t *testing.T) {
	for _, table := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			script := "root 1 c\nterm 2 1 p A\ncontext\nroot 11 c\nterm 12 11 p A\n"
			if table {
				script += "term 3 1 q B\nterm 4 1 ll A\nterm 5 1 ll B\nterm 13 11 q B\nterm 14 11 ll A\nterm 15 11 ll B\n"
			}
			if reverse {
				script += "schema 11 2\nequal 12 2 0\n"
			} else {
				script += "schema 1 12\nequal 2 12 0\n"
			}
			rows := nativeDriver(t, script)
			nativeG2Unmodified(t, script, rows)
		}
	}
}

// Context-private table allocation runs under a native write lock. Verbose
// snapshots must not re-enter ly_err_first from that unrelated allocation hook.
func TestNativeDriverContextSnapshots(t *testing.T) {
	const script = "root 1 c\nterm 2 1 p A\nopaque 3 0 u A\ncontext\nroot 11 c\nterm 12 11 p A\nopaque 13 0 u A\ncontext-keyless\nroot 21 c\nterm 22 21 p A\nopaque 23 0 u A\nequal 2 12 0\n"
	for build, path := range map[string]string{"instrumented": os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER"), "unmodified": nativeComparisonDriver()} {
		for _, verbose := range []bool{false, true} {
			t.Run(build+"/"+map[bool]string{false: "quiet", true: "events"}[verbose], func(t *testing.T) {
				rows := nativeDriverReplayPath(t, script, "", verbose, path)
				if _, err := nativeG2Evidence(script, rows); err != nil {
					t.Fatal(err)
				}
				nativeG2Unmodified(t, script, rows)
			})
		}
	}
}

// A failed current schema load must not prevent construction in a retained
// parent context. Only top-level constructors require the current module.
func TestNativeDriverFailedContextParent(t *testing.T) {
	const script = "root 1 c\ncontext invalid\nterm 2 1 p A\nlist 3 1 i A\nequal 2 2 0\n"
	rows := nativeDriver(t, script)
	nativeG2Unmodified(t, script, rows)
}

// Skipping context-setup callbacks must preserve unrelated allocation evidence
// from data operations. collision-build has three actual allocation callbacks;
// the untouched build cannot observe these internal callbacks.
func TestNativeDriverUnrelatedAllocationEvidence(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "collision-build.calls"))
	if err != nil {
		t.Fatal(err)
	}
	for build, path := range map[string]string{"instrumented": os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER"), "unmodified": nativeComparisonDriver()} {
		t.Run(build, func(t *testing.T) {
			rows := nativeDriverReplayPath(t, string(script), "", true, path)
			count := 0
			for _, row := range rows {
				if row.Event == "table.allocate" {
					count++
				}
			}
			want := 0
			if build == "instrumented" {
				want = 3
			}
			if count != want {
				t.Fatalf("native allocation callbacks=%d, want %d", count, want)
			}
		})
	}
}
