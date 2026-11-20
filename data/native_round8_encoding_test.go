// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNativeOutputAllocationFailures(t *testing.T) {
	if os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER") == "" {
		t.Skip("set native drivers")
	}
	cmd := exec.Command("python3", "round8_encoding_test.py")
	cmd.Dir = filepath.Join("..", "conformance", "oracle", "native_registration")
	output, err := nativeCommandOutput(cmd)
	if err != nil {
		t.Fatalf("output allocation failure controls: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func TestNativeParserRequestFraming(t *testing.T) {
	nativeVerifyConfiguredDriverRoles(t)
	for _, script := range []string{"parse 1 {", "parse-xml 1 <", "context module x {"} {
		t.Run(strings.Fields(script)[0], func(t *testing.T) {
			for _, driver := range []string{os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER"), nativeComparisonDriver()} {
				if driver == "" {
					t.Skip("set native drivers")
				}
				capture := func(input string) map[string]any {
					cmd := exec.Command(driver, "--quiet", "--read-schema")
					cmd.Stdin = strings.NewReader(input)
					out, err := nativeCommandOutput(cmd)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := nativeTranscriptRows(out)
					if err != nil {
						t.Fatal(err)
					}
					var last map[string]any
					if err := json.Unmarshal(raw[len(raw)-1], &last); err != nil {
						t.Fatal(err)
					}
					return last
				}
				first, last := capture(script), capture(script+"\n")
				if !reflect.DeepEqual(first["request"], last["request"]) {
					t.Fatal("framing changed recorded parser arguments")
				}
				for _, channel := range []string{"diagnostics", "logs"} {
					if !reflect.DeepEqual(first[channel], last[channel]) {
						t.Fatalf("same recorded payload received different native parser input: %s", channel)
					}
				}
				a := nativeDriverReplayPath(t, script, "", false, driver)
				b := nativeDriverReplayPath(t, script+"\n", "", false, driver)
				left, err := nativeCompletedCommandEvidence(script, a)
				if err != nil {
					t.Fatal(err)
				}
				right, err := nativeCompletedCommandEvidence(script+"\n", b)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(left.projection, right.projection) {
					t.Fatal("framing changed native parser input while recording the same request")
				}
			}
		})
	}
}

func TestNativeValidationMessageOrigins(t *testing.T) {
	for _, message := range []string{"Internal error (first.c:10).", "Invalid argument node (lyd_insert_hash_nr_impl()).", "Internal error (first.c:10).\n"} {
		// YANG receives the literal bytes; hex remains the transport representation.
		quoted := strings.ReplaceAll(strings.ReplaceAll(message, "\\", "\\\\"), "\n", "\\n")
		script := "context module x {yang-version 1.1; namespace urn:x; prefix x; container c {presence p; must \"false()\" {error-message \"" + quoted + "\";}}}\nroot 1 c\nvalidate 1\n"
		valid := nativeDriver(t, script)
		// Both builds must retain the exact user-defined error-message in both channels.
		for _, rows := range [][]nativeDriverResult{valid, nativeDriverReplayPath(t, script, "", false, nativeComparisonDriver())} {
			found := map[string]bool{}
			for _, row := range rows {
				if row.Event != "validate" {
					continue
				}
				for _, channel := range []string{"diagnostics", "logs"} {
					for _, item := range nativeEvidenceArray(row.raw[channel]) {
						d := item.(map[string]any)
						if d["msg"] == hex.EncodeToString([]byte(message)) {
							found[channel] = true
							if d["code"] != float64(7) {
								t.Fatalf("wrong native validation origin: %v", d)
							}
						}
					}
				}
			}
			if !found["diagnostics"] || !found["logs"] {
				t.Fatalf("did not reach native must error in both channels for %q", message)
			}
		}
		baseline, err := nativeCompletedCommandEvidence(script, valid)
		if err != nil {
			t.Fatal(err)
		}
		for _, channel := range []string{"diagnostics", "logs"} {
			altered := nativeG2CloneRows(t, valid, func(r map[string]any) {
				if r["event"] != "validate" {
					return
				}
				for _, item := range nativeEvidenceArray(r[channel]) {
					d := item.(map[string]any)
					if d["msg"] == hex.EncodeToString([]byte(message)) {
						changed := strings.ReplaceAll(strings.ReplaceAll(message, "first.c:10", "second.h:20"), "lyd_insert_hash_nr_impl", "lyd_insert_hash")
						d["msg"] = hex.EncodeToString([]byte(changed))
					}
				}
			})
			changed, err := nativeCompletedCommandEvidence(script, altered)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(baseline.projection, changed.projection) {
				t.Fatalf("normalized user error-message in %s: %q", channel, message)
			}
		}
	}
}

func TestNativeDiagnosticNormalizationBytes(t *testing.T) {
	const script = "root 1 c\nvalidate 1\n"
	rows := nativeDriver(t, script)
	for _, message := range []string{"Internal error (first\xff.c:10).", "Internal error (first.c:10).\n", "Invalid argument node (lyd_insert_hash_nr_impl()).\n", "Invalid argument node\xff (lyd_insert_hash_nr_impl())."} {
		for _, channel := range []string{"diagnostics", "logs"} {
			baseline := nativeG2CloneRows(t, rows, func(r map[string]any) {
				if r["event"] != "validate" {
					return
				}
				d := map[string]any{"msg": hex.EncodeToString([]byte(message)), "code": float64(7)}
				if channel == "logs" {
					d["level"], d["line"] = float64(0), float64(0)
				} else {
					d["context_generation"] = float64(1)
				}
				r[channel] = []any{d}
			})
			altered := nativeG2CloneRows(t, baseline, func(r map[string]any) {
				if r["event"] != "validate" {
					return
				}
				for _, item := range nativeEvidenceArray(r[channel]) {
					d := item.(map[string]any)
					d["msg"] = hex.EncodeToString([]byte(strings.ReplaceAll(strings.ReplaceAll(message, "first", "second"), "lyd_insert_hash_nr_impl", "lyd_insert_hash")))
				}
			})
			a, err := nativeCompletedCommandEvidence(script, baseline)
			if err != nil {
				t.Fatal(err)
			}
			b, err := nativeCompletedCommandEvidence(script, altered)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(a.projection, b.projection) {
				t.Fatalf("lost validation bytes %q in %s", message, channel)
			}
		}
	}
}

func TestNativeMerge2SafePrefix(t *testing.T) {
	nativeVerifyConfiguredDriverRoles(t)
	// Includes both minimized runtime-check seeds and each full recorded replay.
	minimized := "root 1 c\nroot 2 c\nroot 3 c\nterm 32 3 v 4\ndup 12 32 1 66\nterm 19 1 z Z\nterm 21 2 v 1\nterm 23 2 v 5\ndetach-siblings 21\nattach 1 21\n"
	scripts := map[string]string{"runtime-minimized": minimized}
	for _, name := range []string{"merge2-defined", "merge2-before", "merge2-successor", "merge2-unsorted"} {
		b, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", name+".calls"))
		if err != nil {
			t.Fatal(err)
		}
		scripts[name] = string(b)
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			for _, driver := range []string{os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER"), nativeComparisonDriver()} {
				if driver == "" {
					t.Skip("set native drivers")
				}
				for _, mode := range []string{"--quiet", "--events", "--merge2-ub-stop"} {
					cmd := exec.Command(driver, mode)
					cmd.Stdin = strings.NewReader(script)
					output, err := nativeCommandOutput(cmd)
					if err != nil {
						t.Fatalf("safe-prefix replay %s %s: %v\n%s", filepath.Base(driver), mode, err, output)
					}
					lines := strings.Split(strings.TrimSpace(string(output)), "\n")
					if !strings.Contains(lines[len(lines)-1], `"event":"boundary.merge2-driver-safe-prefix"`) {
						t.Fatalf("continued past safe prefix %s %s", filepath.Base(driver), mode)
					}
				}
			}
		})
	}
}

func TestNativeInputPhysicalFraming(t *testing.T) {
	nativeVerifyConfiguredDriverRoles(t)
	const prefix = "root 1 c\n"
	cases := map[string]string{
		"NUL":           "opaque 2 0 n A\x00B\n",
		"physical-line": "opaque 2 0 n " + strings.Repeat("A", 1024) + "\n",
		"opaque-name":   "opaque 2 0 " + strings.Repeat("n", 80) + " A\n",
		"opaque-value":  "opaque 2 0 n " + strings.Repeat("A", 256) + "\n",
		"term-name":     "term 2 1 " + strings.Repeat("n", 80) + " A\n",
		"term-value":    "term 2 1 p " + strings.Repeat("A", 256) + "\n",
		"change-value":  "change 1 " + strings.Repeat("A", 256) + "\n",
		"read-value":    "read-value 0 1 1 " + strings.Repeat("A", 256) + "\n",
	}
	for name, invalid := range cases {
		t.Run(name, func(t *testing.T) {
			for _, driver := range []string{os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER"), nativeComparisonDriver()} {
				if driver == "" {
					t.Skip("set native drivers")
				}
				for _, mode := range []string{"--quiet", "--events"} {
					capture := func(script string) ([]byte, error) {
						cmd := exec.Command(driver, mode)
						cmd.Stdin = strings.NewReader(script)
						return nativeCommandOutput(cmd)
					}
					before, err := capture(prefix)
					if err != nil {
						t.Fatal(err)
					}
					after, err := capture(prefix + invalid)
					if err == nil {
						t.Fatal("unsupported request executed native code")
					}
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 4 {
						t.Fatalf("wrong input admission error: %v", err)
					}
					if !bytes.Equal(before, after) {
						t.Fatal("rejected physical request changed completed evidence")
					}
				}
			}
		})
	}
}
