// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeLifetimeIdentityContracts(t *testing.T) {
	if os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER") == "" {
		t.Skip("set native drivers")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "lifetime_identity_test.py", "-v")
	cmd.Dir = filepath.Join("..", "conformance", "oracle", "native_registration")
	output, err := nativeCommandOutput(cmd)
	if err != nil {
		t.Fatalf("lifetime contracts: %v\n%s", err, output)
	}
	t.Log(string(output))
}
