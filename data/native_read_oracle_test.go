// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nativeG2ComparisonDriver resolves the unmodified comparison driver: the
// explicit variable, else driver-unmodified beside the instrumented driver.
// Empty only when neither driver is configured, so callers skip rather than
// silently losing the comparison when only the documented variable is set.
func nativeG2ComparisonDriver() string {
	if path := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER"); path != "" {
		return path
	}
	if driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER"); driver != "" {
		return filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	return ""
}

func nativeG2Unmodified(t *testing.T, script string, instrumented []nativeDriverResult) []nativeDriverResult {
	t.Helper()
	unmodified := nativeDriverReplayPath(t, script, "", false, nativeG2ComparisonDriver())
	if err := nativeG2CompareEvidence(script, instrumented, unmodified); err != nil {
		t.Fatal(err)
	}
	return unmodified
}

func TestNativeG2RetainedContextDiagnostics(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "read", "read-retained-diagnostics.calls"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	instrumented := nativeDriver(t, script)
	plain := nativeG2Unmodified(t, script, instrumented)
	for name, rows := range map[string][]nativeDriverResult{"instrumented": instrumented, "unmodified": plain} {
		count := 0
		for _, row := range rows {
			if row.Event != "equal" {
				continue
			}
			count++
			diagnostics := row.raw["diagnostics"].([]any)
			logs, _ := row.raw["logs"].([]any)
			if count == 3 {
				if row.RC != nativeOK || len(diagnostics) != 0 || len(logs) != 0 {
					t.Fatalf("%s retained error leaked into clean command: %v", name, row.raw)
				}
				continue
			}
			if row.RC != 11 || len(diagnostics) != 1 || len(logs) != 1 {
				t.Fatalf("%s missing retained error channels: %v", name, row.raw)
			}
			d := diagnostics[0].(map[string]any)
			if d["code"] != float64(3) || d["context_generation"] != float64(1) || d["msg"] != "Invalid argument prefix_len (ly_resolve_prefix())." || logs[0].(map[string]any)["msg"] != d["msg"] {
				t.Fatalf("%s wrong native diagnostic: %v", name, row.raw)
			}
		}
		if count != 3 {
			t.Fatalf("%s comparison controls=%d", name, count)
		}
	}
}

// Exercise every new native command without depending on later Go kernels.
func TestNativeG2ProtocolSmoke(t *testing.T) {
	const script = `context-keyless
root 1 c
term 2 1 p same
term 3 1 ll a
term 4 1 ll b
term 5 1 ll c
term 6 1 ll d
list 7 1 i ~
child 8 1 0
record 1 2 123 0
record-as 1 2 2 0
read-schema 2 2 1
read-instance 2 2 1
read-value 2 2 1 same
opaque 10 1 u plain
read-opaque 2 2 1 u
context
term 11 1 q same
list 12 1 i ~
opaque 13 1 u plain
parse-xml 14 <u xmlns="urn:unknown">plain</u>
dup-to-context 15 14 0 66
opaque-invalid-xml 16 0
equal 14 15 1
`
	rows := nativeDriver(t, script)
	nativeG2Unmodified(t, script, rows)
	completed, reads := 0, 0
	for _, row := range rows {
		if row.Event == "read.equal" || strings.HasPrefix(row.Event, "read.selection.") {
			reads++
		}
		if row.raw["nodes"] != nil {
			completed++
			if row.RC != nativeOK {
				t.Fatalf("native smoke %s: %v", row.Event, row.raw)
			}
		}
	}
	if completed != 24 || reads != 5 {
		t.Fatalf("smoke completion=%d results=%d", completed, reads)
	}
}
