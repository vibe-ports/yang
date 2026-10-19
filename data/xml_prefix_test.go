// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"strings"
	"testing"
)

// ly_store_prefix_data (tree_data_common.c) and lyxml_value_compare (xml.c)
// both use ly_value_prefix_next. Non-name Unicode characters must separate
// prefixes in parsed values and attributes just as they do in comparison.
func TestXMLValuePrefixBoundaries(t *testing.T) {
	tr, _, _ := anyFixture()
	for _, tc := range []struct {
		name, literal, prefix string
	}{
		{"multiplication", "×", "p"},
		{"after-name", "text×", "p"},
		{"division", "÷", "p"},
		{"greek-question", "\u037e", "p"},
		{"combining-start", "\u0301", "p"},
		{"unicode-prefix", "×", "é"},
		{"supplementary-prefix", "×", "𠜎"},
		{"combining-continuation", "×", "p\u0301"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parse := func(prefix string, attrOnly bool) *Tree {
				t.Helper()
				value := tc.literal + prefix + ":v"
				text := value
				if attrOnly {
					text = "plain"
				}
				in := `<n xmlns="urn:anycore" xmlns:` + prefix + `="urn:anycore" note="` + value + `">` + text + `</n>`
				got, diags, err := parseXMLString(tr.set, in, Opaque, false)
				if err != nil || len(diags) != 0 {
					t.Fatalf("parse: %v, %v", err, diags)
				}
				return got
			}
			for _, attrOnly := range []bool{false, true} {
				a, b, renamed := parse(tc.prefix, attrOnly), parse(tc.prefix, attrOnly), parse("q", attrOnly)
				if !a.top.first().Equal(b.top.first(), CompareOptions{}) || !a.top.first().Equal(renamed.top.first(), CompareOptions{}) {
					t.Errorf("parsed values compare unequal (attribute only: %v)", attrOnly)
				}
				var printed strings.Builder
				if err := a.PrintXML(&printed, PrintOptions{Shrink: true}); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(printed.String(), `xmlns:`+tc.prefix+`="urn:anycore"`) {
					t.Errorf("value namespace missing from XML (attribute only: %v): %s", attrOnly, &printed)
				}
				roundTrip, diags, err := parseXMLString(tr.set, printed.String(), Opaque, false)
				if err != nil || len(diags) != 0 {
					t.Fatalf("reparse: %v, %v", err, diags)
				}
				if !a.top.first().Equal(roundTrip.top.first(), CompareOptions{}) {
					t.Error("value changed after XML round trip")
				}
			}
		})
	}
}
