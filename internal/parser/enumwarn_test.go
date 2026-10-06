// SPDX-License-Identifier: BSD-3-Clause

package parser

import (
	"slices"
	"testing"
)

// TestEnumControlCharWarning: lysp_check_enum_name warns (once, at the first control character)
// about an enum name with control characters; the module is still valid.
func TestEnumControlCharWarning(t *testing.T) {
	var warns []string
	ctx := &Context{Warn: func(msg string) { warns = append(warns, msg) }}
	src := "module aa { namespace urn:aa; prefix aa;\n" +
		"  leaf l { type enumeration { enum \"inva\\nl\\tid\"; enum ok; } } }"
	if _, err := ParseIn(ctx, "aa.yang", []byte(src), nil); err != nil {
		t.Fatal(err)
	}
	want := []string{"Control characters in enum name should be avoided (\"inva\nl\tid\", character number 5)."}
	if !slices.Equal(warns, want) {
		t.Errorf("warnings %q, want %q", warns, want)
	}
}
