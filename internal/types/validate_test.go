// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"path/filepath"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestOracleValidate: a value stored without restrictions (StoreOnly) and re-checked by Validate
// is rejected with the diagnostic libyang gave for the fixture.
func TestOracleValidate(t *testing.T) {
	ty := tyTypes()
	for id, c := range map[string]struct{ leaf, lex string }{
		"string-length":           {"s", "abcdef"},
		"string-length-multibyte": {"s", "äää"},
		"string-invert-match":     {"s", "xab"},
		"binary-length":           {"bin", "MTIzNA=="},
	} {
		t.Run(id, func(t *testing.T) {
			g := loadGolden(t, filepath.Join("..", "..", "conformance", "corpus", "types", "golden", id+".json"))
			v, d := StoreOnly(ty[c.leaf], c.lex, FormatXML, xmlH, nil, nil)
			if d != nil {
				t.Fatalf("store only: %s", d.Msg)
			}
			want := g.Diagnostics[0]
			if d := Validate(ty[c.leaf], v); d == nil || d.Msg != want.Msg || d.AppTag != want.AppTag || d.Code != want.VecodeName {
				t.Fatalf("got %+v, golden %+v", d, want)
			}
		})
	}
}

// TestValidateParity: Validate says what Store says for every type that has a validate_value
// callback, and nothing for the others.
func TestValidateParity(t *testing.T) {
	hex := ietfTypes("2025-12-22")["hex-string"]
	msg := emsg("too big", "my-tag")
	rangeMsg := typ(schema.Int16, rng(1, 9))
	msg(rangeMsg)
	for _, tc := range []struct {
		name string
		t    *schema.Type
		lex  string
	}{
		{"int8 range", typ(schema.Int8, rng(-5, 5)), "6"},
		{"int64 range", typ(schema.Int64, rng(-5, 5)), "-9223372036854775808"},
		{"int range message", rangeMsg, "10"},
		{"uint8 range", typ(schema.Uint8, urng(1, 5)), "0"},
		{"uint64 range", typ(schema.Uint64, urng(1, 5)), "18446744073709551615"},
		{"dec64 range", typ(schema.Dec64, fd(2), rng(100, 200)), "2.01"},
		{"string length", typ(schema.String, length(1, 3)), "abcd"},
		{"string pattern", typ(schema.String, pat(`a+`, false)), "b"},
		{"binary length", typ(schema.Binary, length(1, 1)), "YWI="},
		{"hex-string pattern", hex, "D"},
		{"in range", typ(schema.Int8, rng(-5, 5)), "5"},
		{"no restriction", typ(schema.String), "anything"},
	} {
		_, want := Store(tc.t, tc.lex, FormatXML, xmlH, nil, nil)
		v, d := StoreOnly(tc.t, tc.lex, FormatXML, xmlH, nil, nil)
		if d != nil {
			t.Fatalf("%s: store only: %s", tc.name, d.Msg)
		}
		got := Validate(tc.t, v)
		if (got == nil) != (want == nil) || got != nil && *got != *want {
			t.Errorf("%s: Validate %+v, Store %+v", tc.name, got, want)
		}
	}
	// no callback: the stored value is accepted whatever the leaf's type restricts
	ts := ietfTypes("2025-12-22")
	v, d := StoreOnly(ts["ipv4-address"], "1.2.3.4", FormatXML, xmlH, nil, nil)
	if d != nil || Validate(ts["ipv4-address"], v) != nil {
		t.Errorf("ipv4-address: %v", d)
	}
	u := &schema.Type{Base: schema.Union, Union: []*schema.Type{typ(schema.Int8, rng(1, 2)), typ(schema.String, length(9, 9))}}
	v, d = StoreOnly(u, "5", FormatXML, xmlH, nil, nil)
	if d != nil || Validate(u, v) != nil {
		t.Errorf("union: %v", d)
	}
	lr := lref("/x", typ(schema.Int8, rng(1, 2)), true)
	v, d = StoreOnly(lr, "5", FormatXML, xmlH, nil, nil)
	if d != nil || Validate(lr, v) != nil {
		t.Errorf("leafref: %v", d)
	}
}
