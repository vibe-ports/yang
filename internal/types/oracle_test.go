// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// oracleCase stores one leaf value of a conformance/corpus/types fixture (schema ty.yang) and
// expects what libyang v5.8.6 answered in the fixture's golden: the first diagnostic's message
// and app-tag, or the leaf's canonical value.
type oracleCase struct {
	t    *schema.Type
	lex  string
	h    Hints
	leaf string // data path of the leaf in the golden
}

func tyTypes() map[string]*schema.Type {
	return map[string]*schema.Type{
		"i8":  typ(schema.Int8, rng(0, 50, 105, 105)),
		"i32": typ(schema.Int32),
		"i64": typ(schema.Int64),
		"u64": typ(schema.Uint64),
		"d1":  typ(schema.Dec64, fd(1), rng(15, 100)),
		"d18": typ(schema.Dec64, fd(18)),
		"s":   typ(schema.String, length(2, 5), pat(`[a-z]+`, false), pat(`x.*`, true)),
		"b":   typ(schema.Bool),
		"e":   typ(schema.Empty),
		"en": typ(schema.Enumeration, func(t *schema.Type) {
			t.Enums = []*schema.Enum{{Name: "white"}, {Name: "black", Value: -1}}
		}),
		"bt":  typ(schema.Bits, bits("two", 2, "ten", 10)),
		"bin": typ(schema.Binary, length(1, 3)),
	}
}

func TestOracleGoldens(t *testing.T) {
	ty := tyTypes()
	c := func(leaf, lex string, h Hints) oracleCase { return oracleCase{ty[leaf], lex, h, "/ty:c/" + leaf} }
	cases := map[string][]oracleCase{
		"int-bounds-before-garbage": {c("i8", "300 x", xmlH)},
		"int-hex-in-xml":            {c("i32", "0x01", xmlH)},
		"int64-overflow":            {c("i64", "9223372036854775808", xmlH)},
		"uint64-minus-one":          {c("u64", "-1", xmlH)},
		"uint64-minus-zero":         {c("u64", "-0", xmlH)},
		"dec64-overflow":            {c("d18", "10", xmlH)},
		"dec64-point-end":           {c("d1", "2.", xmlH)},
		"dec64-canonical":           {c("d1", " +8.00 ", xmlH)},
		"string-invert-match":       {c("s", "xab", xmlH)},
		"string-length":             {c("s", "abcdef", xmlH)},
		"string-length-multibyte":   {c("s", "äää", xmlH)},
		"bits-duplicate":            {c("bt", "ten two ten", xmlH)},
		"bits-canonical":            {c("bt", " ten  two ", xmlH)},
		"binary-padding":            {c("bin", "Y===", xmlH)},
		"binary-length":             {c("bin", "MTIzNA==", xmlH)},
		"json-int8-string":          {c("i8", "5", jsonS)},
		"json-int64-number":         {c("i64", "5", jsonN)},
		"json-dec64-number":         {c("d1", "2.5", jsonN)},
		"json-bool-string":          {c("b", "true", jsonS)},
		"json-empty-string":         {c("e", "", jsonS)},
		"json-valid": {c("i8", "5", jsonN), c("i64", "-9223372036854775808", jsonS),
			c("u64", "18446744073709551615", jsonS), c("d18", "-9.223372036854775808", jsonS),
			c("b", "true", jsonB), c("e", "", JSONHints("empty")), c("en", "black", jsonS),
			c("bt", "ten two", jsonS), c("bin", "YQ==", jsonS)},
	}
	for id, cs := range cases {
		t.Run(id, func(t *testing.T) {
			g := loadGolden(t, filepath.Join("..", "..", "conformance", "corpus", "types", "golden", id+".json"))
			for _, oc := range cs {
				v, d := Store(oc.t, oc.lex, FormatXML, oc.h, nil, nil)
				if g.Verdict == "invalid" {
					want := g.Diagnostics[0]
					if d == nil || d.Msg != want.Msg || d.AppTag != want.AppTag || d.Code != want.VecodeName || want.DataPath != oc.leaf {
						t.Fatalf("%s %q: got %+v, golden %+v", oc.leaf, oc.lex, d, want)
					}
					continue
				}
				if d != nil {
					t.Fatalf("%s %q: %s, golden valid", oc.leaf, oc.lex, d.Msg)
				}
				if want := g.canonical(oc.leaf); v.Canonical() != want {
					t.Fatalf("%s %q: canonical %q, golden %q", oc.leaf, oc.lex, v.Canonical(), want)
				}
			}
		})
	}
}

type golden struct {
	Verdict     string `json:"verdict"`
	Diagnostics []struct {
		Msg        string `json:"msg"`
		DataPath   string `json:"data_path"`
		VecodeName string `json:"vecode_name"`
		AppTag     string `json:"apptag"`
	} `json:"diagnostics"`
	Tree *struct {
		JSON string `json:"json"`
		XML  string `json:"xml"`
	} `json:"tree"`
	Typed []struct {
		Path  string `json:"path"`
		Value *struct {
			Canonical string `json:"canonical"`
		} `json:"value"`
	} `json:"typed"`
}

func (g *golden) canonical(path string) string {
	for _, n := range g.Typed {
		if n.Path == path && n.Value != nil {
			return n.Value.Canonical
		}
	}
	return "<missing>"
}

func loadGolden(t *testing.T, path string) *golden {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatal(err)
	}
	var g golden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return &g
}
