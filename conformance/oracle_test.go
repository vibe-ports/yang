// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"strings"
	"testing"
)

func TestCheckArch(t *testing.T) {
	for _, tc := range []struct {
		got, goarch string
		write       bool
		want        string // "" = ok
	}{
		{"x86_64", "amd64", true, ""},
		{"x86_64", "amd64", false, ""},
		{"aarch64", "arm64", false, ""},
		{"aarch64", "arm64", true, "refusing to write"},
		{"aarch64", "amd64", false, "foreign or stale"},
		{"x86_64", "arm64", true, "foreign or stale"},
	} {
		err := checkArch(tc.got, tc.goarch, tc.write)
		if (err == nil) != (tc.want == "") || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	if err := CheckOracleArch(Response{}, false); err == nil || !strings.Contains(err.Error(), "no \"arch\"") {
		t.Errorf("missing arch: %v", err)
	}
}
