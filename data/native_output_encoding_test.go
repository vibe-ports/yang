// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func nativeByteText(t *testing.T, value any) string {
	t.Helper()
	wire, ok := value.(string)
	if !ok {
		t.Fatalf("expected encoded native bytes: %v", value)
	}
	decoded, err := hex.DecodeString(wire)
	if err != nil {
		t.Fatal(err)
	}
	return string(decoded)
}

func TestNativeOutputByteEmitters(t *testing.T) {
	if os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER") == "" {
		t.Skip("set native drivers")
	}
	cmd := exec.Command("python3", "byte_fields_test.py")
	cmd.Dir = filepath.Join("..", "conformance", "oracle", "native_registration")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("byte emitter controls: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func TestNativeOutputInvalidXML(t *testing.T) {
	path := filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "read", "output-invalid-xml.calls")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the printer result first, so the pre-fix failure reaches the
	// reported raw-byte output even if its bootstrap uses the older contract.
	for _, driver := range []string{os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER"), nativeComparisonDriver()} {
		if driver == "" {
			t.Skip("set native drivers")
		}
		cmd := exec.Command(driver, "--quiet")
		cmd.Stdin = bytes.NewReader(script)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("native printer replay: %v\n%s", err, output)
		}
		found := false
		for _, line := range bytes.Split(output, []byte{'\n'}) {
			if !bytes.Contains(line, []byte(`"event":"public.result"`)) {
				continue
			}
			found = true
			var row nativeDriverResult
			if err := json.Unmarshal(line, &row); err != nil {
				t.Fatalf("printer transport: %v", err)
			}
			if row.RC != nativeOK || !strings.Contains(nativeByteText(t, row.raw["json"]), "\xff") {
				t.Fatal("native printer byte missing")
			}
		}
		if !found {
			t.Fatal("missing native printer result")
		}
	}
	left := nativeDriver(t, string(script))
	right := nativeDriverReplayPath(t, string(script), "", false, nativeComparisonDriver())
	a, err := nativeCompletedCommandEvidence(string(script), left)
	if err != nil {
		t.Fatal(err)
	}
	b, err := nativeCompletedCommandEvidence(string(script), right)
	if err != nil {
		t.Fatal(err)
	}
	// Both builds must actually print the native byte, with no replacement.
	for _, rows := range [][]nativeDriverResult{left, right} {
		found := false
		for _, row := range rows {
			if row.Event == "public.result" {
				found = true
				output := nativeByteText(t, row.raw["json"])
				if row.RC != nativeOK || !strings.Contains(output, "\xff") || strings.Contains(output, "\xef\xbf\xbd") {
					t.Fatalf("lost native printer bytes: %q", output)
				}
			}
		}
		if !found {
			t.Fatal("missing public result")
		}
	}
	if len(a.projection) == 0 || len(b.projection) == 0 {
		t.Fatal("empty evidence")
	}
	// The shared comparison includes the printer result and current opaque value.
	if !reflect.DeepEqual(a.projection, b.projection) {
		t.Fatal("native printed evidence differs")
	}
}

func TestNativeOutputByteArguments(t *testing.T) {
	if os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER") == "" {
		t.Skip("set native drivers")
	}
	cmd := exec.Command("python3", "-c", "from evidence_test import byte_arguments; byte_arguments()")
	cmd.Dir = filepath.Join("..", "conformance", "oracle", "native_registration")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("byte request controls: %v\n%s", err, output)
	}
	t.Log(string(output))
}

// Expanded JSON evidence must not keep a chunk reserved for a real native
// constructor; all profiles still require the actual allocation to reuse it.
func TestNativeOutputJSONReuse(t *testing.T) {
	if os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER") == "" {
		t.Skip("set native drivers")
	}
	const script = `import json, os, pathlib
from reuse_test import execute, evidence, PROFILES
from drivers import comparison_driver
count = 0
for profile in PROFILES:
    for driver in (pathlib.Path(os.environ['YANG_NATIVE_REGISTRATION_DRIVER']), comparison_driver()):
        for verbose in (False, True):
            result = execute(driver, 'lifetime/validation-delete', verbose, profile)
            assert result.returncode == 0, (profile, driver.name, verbose, result.returncode, result.stderr)
            evidence('lifetime/validation-delete', [json.loads(line) for line in result.stdout.splitlines()])
            count += 1
assert count == 12
print(json.dumps({'output_reuse': count}))
`
	cmd := exec.Command("python3", "-c", script)
	cmd.Dir = filepath.Join("..", "conformance", "oracle", "native_registration")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("JSON allocation/reuse controls: %v\n%s", err, output)
	}
	t.Log(string(output))
}
