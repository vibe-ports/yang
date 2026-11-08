// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Every positive reuse requirement must survive changes in startup allocation
// activity. Holding its exact native allocation must defeat the same control.
func TestNativeReuseEnvironmentMatrix(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	comparison := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER")
	if comparison == "" {
		comparison = filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "reuse_test.py"),
		"--driver", driver, "--comparison-driver", comparison, "-v")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reuse environment controls: %v\n%s", err, out)
	}
	for _, evidence := range []string{`"reuse_environment_passes": 192`, `"reuse_forcing_disabled_rejections": 192`} {
		if !bytes.Contains(out, []byte(evidence)) {
			t.Fatalf("missing %s\n%s", evidence, out)
		}
	}
	t.Logf("%s", out)
}
