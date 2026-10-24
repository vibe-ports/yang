// SPDX-License-Identifier: BSD-3-Clause

//go:build !oracle

package conformance

import "testing"

func nativeCrashSignal(t *testing.T, _ string, _ map[string]any) (string, error) {
	t.Skip("set -tags oracle to run the native oracle")
	return "", nil
}
