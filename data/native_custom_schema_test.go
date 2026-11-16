// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"context"
	"encoding/json"
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
							out, err := cmd.Output()
							exit := 0
							if err != nil {
								failure, ok := err.(*exec.ExitError)
								if !ok {
									t.Fatal(err)
								}
								exit = failure.ExitCode()
							}
							var rows []json.RawMessage
							for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
								if !json.Valid([]byte(line)) {
									t.Fatalf("invalid driver JSON: %q", line)
								}
								rows = append(rows, json.RawMessage(line))
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
	for _, name := range []string{"reused-names", "same-name-ancestors"} {
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("custom schema sweep: %v\n%s", err, out)
	}
	var summary struct {
		Passed   bool `json:"custom_schema_sweep"`
		Names    int  `json:"names"`
		Kinds    int  `json:"kinds"`
		Skips    int  `json:"skips"`
		Valid    int  `json:"valid_pairs"`
		Rejected int  `json:"rejected_pairs"`
		Runs     int  `json:"driver_runs"`
	}
	if err := json.Unmarshal(out, &summary); err != nil || !summary.Passed || summary.Names != 21 || summary.Kinds != 9 || summary.Skips != 0 || summary.Valid != 466 || summary.Rejected != 672 || summary.Runs != 2276 {
		t.Fatalf("incomplete sweep: %v, %s", err, out)
	}
	t.Logf("%s", out)
}
