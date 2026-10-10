// SPDX-License-Identifier: BSD-3-Clause

//go:build !oracle

package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func nativeRequestRunner(t *testing.T) func(string, map[string]any) (Response, error) {
	t.Helper()
	t.Skip("set YANG_ORACLE_CACHE to replay native differential responses offline")
	return nil
}

func nativeRequestAvailable() bool { return false }

func TestRequestOracleCacheTopUpWithoutOracle(t *testing.T) {
	dir := t.TempDir()
	cache, write := filepath.Join(dir, "cache.jsonl"), filepath.Join(dir, "top-up.jsonl")
	if err := os.WriteFile(cache, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YANG_ORACLE_CACHE", cache)
	t.Setenv("YANG_ORACLE_CACHE_WRITE", write)
	runner, base := requestOracle(t)
	_, err := inventoryNativeResponse(runner, base, map[string]any{"op": "data"})
	if err == nil || !strings.Contains(err.Error(), "oracle cache missing request") {
		t.Fatalf("offline top-up miss = %v", err)
	}
}
