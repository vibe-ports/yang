// SPDX-License-Identifier: BSD-3-Clause

package ly

import "testing"

func TestCodeString(t *testing.T) {
	for c, want := range map[Code]string{Success: "LYVE_SUCCESS", SyntaxYang: "LYVE_SYNTAX_YANG", Other: "LYVE_OTHER", 200: "LY_VECODE(200)"} {
		if got := c.String(); got != want {
			t.Errorf("%d: %q, want %q", c, got, want)
		}
	}
}
