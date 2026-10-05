// SPDX-License-Identifier: BSD-3-Clause
// Test cases ported from libyang v5.8.6 tests/utests/types/{int8,int16,int32,int64,uint8,uint16,
// uint32,uint64,decimal64,string,boolean,empty,enumeration,bits,binary}.c and
// tests/utests/restriction/{test_range,test_pattern}.c (BSD-3-Clause, © CESNET).

package types

import (
	"math"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// ---- hand-built compiled types (what compile produces for the C tests' YANG snippets) ----

type opt func(*schema.Type)

func typ(b schema.BaseType, opts ...opt) *schema.Type {
	t := &schema.Type{Base: b}
	for _, o := range opts {
		o(t)
	}
	return t
}

// rng is a signed range; pairs are min, max.
func rng(mm ...int64) opt {
	return func(t *schema.Type) {
		r := &schema.Range{}
		for i := 0; i < len(mm); i += 2 {
			r.Parts = append(r.Parts, schema.RangePart{Min: mm[i], Max: mm[i+1]})
		}
		t.Range = r
	}
}

// urng is an unsigned range.
func urng(mm ...uint64) opt {
	return func(t *schema.Type) { t.Range = uparts(mm) }
}

func length(mm ...uint64) opt {
	return func(t *schema.Type) { t.Length = uparts(mm) }
}

func uparts(mm []uint64) *schema.Range {
	r := &schema.Range{}
	for i := 0; i < len(mm); i += 2 {
		r.Parts = append(r.Parts, schema.RangePart{MinU: mm[i], MaxU: mm[i+1]})
	}
	return r
}

func emsg(msg, apptag string) opt {
	return func(t *schema.Type) {
		r := t.Range
		if r == nil {
			r = t.Length
		}
		r.Msg, r.AppTag = msg, apptag
	}
}

func pat(expr string, invert bool, msgTag ...string) opt {
	return func(t *schema.Type) {
		p := &schema.Pattern{Expr: expr, Invert: invert}
		if len(msgTag) == 2 {
			p.Msg, p.AppTag = msgTag[0], msgTag[1]
		}
		t.Patterns = append(t.Patterns, p)
	}
}

func fd(n uint8) opt { return func(t *schema.Type) { t.FracDigits = n } }

func enums(names ...string) opt {
	return func(t *schema.Type) {
		for i, n := range names {
			t.Enums = append(t.Enums, &schema.Enum{Name: n, Value: int32(i)})
		}
	}
}

// bits takes name, position pairs in position order.
func bits(np ...any) opt {
	return func(t *schema.Type) {
		for i := 0; i < len(np); i += 2 {
			t.Bits = append(t.Bits, &schema.Bit{Name: np[i].(string), Position: uint32(np[i+1].(int))}) //nolint:gosec,forcetypeassert // test data
		}
	}
}

// Hints the libyang data parsers pass (parser_xml.c: LYD_HINT_DATA; parser_json.c per token).
var (
	xmlH  = HintData
	jsonS = JSONHints("string")
	jsonN = JSONHints("number")
	jsonB = JSONHints("bool")
)

type storeCase struct {
	src      string // libyang test file:line
	t        *schema.Type
	lex      string
	f        Format
	h        Hints
	only     bool
	canonFmt bool   // f is really FormatCanon
	canon    string // expected canonical form on success
	err      string // expected message on failure
	apptag   string
}

func runStore(t *testing.T, cases []storeCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			f := c.f
			if f == FormatCanon && !c.canonFmt {
				f = FormatXML // the zero Format is FormatCanon; cases default to XML
			}
			st := Store
			if c.only {
				st = StoreOnly
			}
			v, d := st(c.t, c.lex, f, c.h, nil, nil)
			if c.err != "" {
				if d == nil {
					t.Fatalf("Store(%q) = %q, want error %q", c.lex, v.Canonical(), c.err)
				}
				if d.Msg != c.err || d.AppTag != c.apptag || d.Code != CodeData {
					t.Fatalf("Store(%q) error = %q [%s, %q], want %q [%q]", c.lex, d.Msg, d.Code, d.AppTag, c.err, c.apptag)
				}
				return
			}
			if d != nil {
				t.Fatalf("Store(%q) error %q", c.lex, d.Msg)
			}
			if v.Canonical() != c.canon || v.Type() != c.t {
				t.Fatalf("Store(%q) = %q, want %q", c.lex, v.Canonical(), c.canon)
			}
		})
	}
}

func TestIntegers(t *testing.T) {
	r050 := typ(schema.Int8, rng(0, 50, 105, 105))
	t0 := typ(schema.Int8)
	p := typ(schema.Int8, rng(-50, 50))
	tag := typ(schema.Int8, rng(0, 50, 105, 105), emsg("invalid range of value", "range-violation"))
	runStore(t, []storeCase{
		{src: "int8.c:1114", t: r050, lex: "+50", h: xmlH, canon: "50"},
		{src: "int8.c:1116", t: r050, lex: "105", h: xmlH, canon: "105"},
		{src: "int8.c:1118", t: r050, lex: "-0", h: xmlH, canon: "0"},
		{src: "int8.c:1119", t: r050, lex: "-1", h: xmlH, err: `Unsatisfied range - value "-1" is out of the allowed range.`},
		{src: "int8.c:1121", t: r050, lex: "51", h: xmlH, err: `Unsatisfied range - value "51" is out of the allowed range.`},
		{src: "int8.c:1123", t: r050, lex: "106", h: xmlH, err: `Unsatisfied range - value "106" is out of the allowed range.`},
		{src: "int8.c:1125", t: r050, lex: "104", h: xmlH, err: `Unsatisfied range - value "104" is out of the allowed range.`},
		{src: "int8.c:1132", t: t0, lex: "-128", h: xmlH, canon: "-128"},
		{src: "int8.c:1137", t: t0, lex: "127", h: xmlH, canon: "127"},
		{src: "int8.c:1141", t: t0, lex: "-129", h: xmlH, err: `Value "-129" is out of type int8 min/max bounds.`},
		{src: "int8.c:1143", t: t0, lex: "128", h: xmlH, err: `Value "128" is out of type int8 min/max bounds.`},
		{src: "int8.c:1147", t: t0, lex: "1024", h: xmlH, err: `Value "1024" is out of type int8 min/max bounds.`},
		{src: "int8.c:1208", t: r050, lex: "105", h: jsonN, canon: "105"},
		{src: "int8.c:1210", t: r050, lex: "-0", h: jsonN, canon: "0"},
		{src: "int8.c:1237", t: t0, lex: "-129", h: jsonN, err: `Value "-129" is out of type int8 min/max bounds.`},
		{src: "int8.c:1345", t: tag, lex: "120", h: xmlH, err: "invalid range of value", apptag: "range-violation"},
		// test_plugin_store: explicit hints select the number base.
		{src: "int8.c:1420", t: p, lex: "20", h: HintDecNum, canon: "20"},
		{src: "int8.c:1427", t: p, lex: "-20", h: HintDecNum, canon: "-20"},
		{src: "int8.c:1434", t: p, lex: "0xf", h: HintHexNum, canon: "15"},
		{src: "int8.c:1442", t: p, lex: "1B", h: HintHexNum, canon: "27"},
		{src: "int8.c:1450", t: p, lex: "-0xf", h: HintHexNum, canon: "-15"},
		{src: "int8.c:1458", t: p, lex: "027", h: HintOctNum, canon: "23"},
		{src: "int8.c:1466", t: p, lex: "-027", h: HintOctNum, canon: "-23"},
		{src: "int8.c:1500", t: p, lex: "", h: HintHexNum, err: "Invalid type int8 empty value."},
		{src: "int8.c:1514", t: p, lex: "10 b", h: HintHexNum, err: `Invalid type int8 value "10 b".`},
		{src: "int8.c:1521", t: p, lex: "a", h: HintDecNum, err: `Invalid type int8 value "a".`},
		{src: "int8.c:1530", t: p, lex: "-60", h: HintDecNum, only: true, canon: "-60"},
		// schema defaults (LYD_HINT_SCHEMA): C strtol base auto-detection.
		{src: "int8.c:498", t: t0, lex: "0xf", h: HintSchema, f: FormatSchema, canon: "15"},
		{src: "int8.c:514", t: t0, lex: "-0xf", h: HintSchema, f: FormatSchema, canon: "-15"},
		{src: "int8.c:530", t: t0, lex: "+0x7F", h: HintSchema, f: FormatSchema, canon: "127"},
		{src: "int8.c:553", t: t0, lex: "0xff", h: HintSchema, f: FormatSchema, err: `Value "0xff" is out of type int8 min/max bounds.`},
		{src: "int8.c:563", t: t0, lex: "-0x81", h: HintSchema, f: FormatSchema, err: `Value "-0x81" is out of type int8 min/max bounds.`},
		{src: "int8.c:583", t: t0, lex: "017", h: HintSchema, f: FormatSchema, canon: "15"},
		{src: "int8.c:615", t: t0, lex: "+017", h: HintSchema, f: FormatSchema, canon: "15"},
		{src: "int8.c:631", t: t0, lex: "0377", h: HintSchema, f: FormatSchema, err: `Value "0377" is out of type int8 min/max bounds.`},
		{src: "int8.c:651", t: t0, lex: "0200", h: HintSchema, f: FormatSchema, err: `Value "0200" is out of type int8 min/max bounds.`},
		{src: "int8.c:473", t: r050, lex: "-1", h: HintSchema, f: FormatSchema, err: `Unsatisfied range - value "-1" is out of the allowed range.`},
		{src: "int16.c:62", t: typ(schema.Int16, rng(-20, -10)), lex: "100", h: xmlH, err: `Unsatisfied range - value "100" is out of the allowed range.`},
		{src: "int32.c:62", t: typ(schema.Int32), lex: "0x01", h: xmlH, err: `Invalid type int32 value "0x01".`},
		{src: "int64.c:62", t: typ(schema.Int64), lex: "", h: xmlH, err: "Invalid type int64 empty value."},
		{src: "int64.c:65", t: typ(schema.Int64), lex: "   ", h: xmlH, err: "Invalid type int64 empty value."},
		{src: "int64.c:68", t: typ(schema.Int64), lex: "-10  xxx", h: xmlH, err: `Invalid type int64 value "-10  xxx".`},
		{src: "uint8.c:70", t: typ(schema.Uint8, urng(150, 200)), lex: "\n 150 \t\n  ", h: xmlH, canon: "150"},
		{src: "uint8.c:72", t: typ(schema.Uint8, urng(150, 200)), lex: "\n 15 \t\n  ", h: xmlH, err: `Unsatisfied range - value "15" is out of the allowed range.`},
		{src: "uint8.c:76", t: typ(schema.Uint8, urng(150, 200)), lex: "\n 15 \t\n  ", h: xmlH, only: true, canon: "15"},
		{src: "uint16.c:62", t: typ(schema.Uint16, urng(150, 200)), lex: "\n 1500 \t\n  ", h: xmlH, err: `Unsatisfied range - value "1500" is out of the allowed range.`},
		{src: "uint32.c:62", t: typ(schema.Uint32), lex: "-10", h: xmlH, err: `Value "-10" is out of type uint32 min/max bounds.`},
		{src: "uint64.c:62", t: typ(schema.Uint64), lex: "", h: xmlH, err: "Invalid type uint64 empty value."},
		{src: "uint64.c:65", t: typ(schema.Uint64), lex: "   ", h: xmlH, err: "Invalid type uint64 empty value."},
		{src: "uint64.c:68", t: typ(schema.Uint64), lex: "10  xxx", h: xmlH, err: `Invalid type uint64 value "10  xxx".`},
		{src: "test_range.c:TRANGE_0", t: typ(schema.Int8, rng(0, 50, 126, 126), emsg("error message", "err-apt-tag")), lex: "-1", h: xmlH, err: "error message", apptag: "err-apt-tag"},
		{src: "test_range.c:TRANGE_1", t: typ(schema.Uint8, urng(30, 50, 126, 126), emsg("error message", "err-apt-tag")), lex: "127", h: xmlH, err: "error message", apptag: "err-apt-tag"},
		{src: "test_range.c:TRANGE_1-ok", t: typ(schema.Uint8, urng(30, 50, 126, 126), emsg("error message", "err-apt-tag")), lex: "126", h: xmlH, canon: "126"},
	})
}

// TestIntegerEdges covers the value-space limits and C strtol behaviour (also on 32-bit).
func TestIntegerEdges(t *testing.T) {
	i64, u64 := typ(schema.Int64), typ(schema.Uint64)
	runStore(t, []storeCase{
		{src: "int64-min", t: i64, lex: "-9223372036854775808", h: xmlH, canon: "-9223372036854775808"},
		{src: "int64-max", t: i64, lex: "9223372036854775807", h: xmlH, canon: "9223372036854775807"},
		{src: "int64-erange", t: i64, lex: "9223372036854775808", h: xmlH, err: `Invalid type int64 value "9223372036854775808".`},
		{src: "int64-json-number", t: i64, lex: "1", h: jsonN, err: `Invalid non-num64-encoded int64 value "1".`},
		{src: "int64-json-string", t: i64, lex: "-1", h: jsonS, canon: "-1"},
		{src: "uint64-max", t: u64, lex: "18446744073709551615", h: xmlH, canon: "18446744073709551615"},
		{src: "uint64-erange", t: u64, lex: "18446744073709551616", h: xmlH, err: `Invalid type uint64 value "18446744073709551616".`},
		{src: "uint64-neg", t: u64, lex: "-1", h: xmlH, err: `Value "-1" is out of type uint64 min/max bounds.`},
		{src: "uint64-negzero", t: u64, lex: "-0", h: xmlH, canon: "0"},
		{src: "uint8-json-string", t: typ(schema.Uint8), lex: "1", h: jsonS, err: `Invalid non-number-encoded uint8 value "1".`},
		{src: "uint8-string-datatypes", t: typ(schema.Uint8), lex: "1", h: jsonS | HintStringDatatypes, canon: "1"},
		{src: "int8-bounds-before-garbage", t: typ(schema.Int8), lex: "300 x", h: xmlH, err: `Value "300 x" is out of type int8 min/max bounds.`},
		{src: "int8-sign-only", t: typ(schema.Int8), lex: "-", h: xmlH, err: `Invalid type int8 value "-".`},
		{src: "int8-hex-no-digits", t: typ(schema.Int8), lex: "0x", h: HintSchema, f: FormatSchema, err: `Invalid type int8 value "0x".`},
		{src: "int8-canon-kept", t: typ(schema.Int8), lex: "07", h: HintSchema, canonFmt: true, canon: "07"},
	})
}

func TestDecimal64(t *testing.T) {
	l1 := typ(schema.Dec64, fd(1), rng(15, 100))
	l2 := typ(schema.Dec64, fd(18))
	runStore(t, []storeCase{
		{src: "decimal64.c:81", t: l1, lex: "\n +8 \t\n  ", h: xmlH, canon: "8.0"},
		{src: "decimal64.c:82", t: l1, lex: "8.00", h: xmlH, canon: "8.0"},
		{src: "decimal64.c:84", t: l2, lex: "-9.223372036854775808", h: xmlH, canon: "-9.223372036854775808"},
		{src: "decimal64.c:86", t: l2, lex: "9.223372036854775807", h: xmlH, canon: "9.223372036854775807"},
		{src: "decimal64.c:88", t: l1, lex: "\n 15 \t\n  ", h: xmlH, err: `Unsatisfied range - value "15.0" is out of the allowed range.`},
		{src: "decimal64.c:91", t: l1, lex: "\n 0 \t\n  ", h: xmlH, err: `Unsatisfied range - value "0.0" is out of the allowed range.`},
		{src: "decimal64.c:94", t: l1, lex: "xxx", h: xmlH, err: `Invalid 1. character of decimal64 value "xxx".`},
		{src: "decimal64.c:97", t: l1, lex: "", h: xmlH, err: "Invalid empty decimal64 value."},
		{src: "decimal64.c:100", t: l1, lex: "8.5  xxx", h: xmlH, err: `Invalid 6. character of decimal64 value "8.5  xxx".`},
		{src: "decimal64.c:103", t: l1, lex: "8.55  xxx", h: xmlH, err: `Value "8.55" of decimal64 type exceeds defined number (1) of fraction digits.`},
		{src: "decimal64.c:107", t: l1, lex: "\n 15 \t\n  ", h: xmlH, only: true, canon: "15.0"},
		{src: "dec64-small", t: typ(schema.Dec64, fd(3)), lex: "-0.05", h: xmlH, canon: "-0.05"},
		{src: "dec64-point-end", t: l2, lex: "1.", h: xmlH, err: `Invalid 2. character of decimal64 value "1.".`},
		{src: "dec64-overflow", t: l2, lex: "10", h: xmlH, err: `Invalid type decimal64 value "10000000000000000000".`},
		{src: "dec64-json-number", t: l1, lex: "2.5", h: jsonN, err: `Invalid non-string-encoded decimal64 value "2.5".`},
		{src: "dec64-json-string", t: l1, lex: "2.5", h: jsonS, canon: "2.5"},
	})
}

func TestString(t *testing.T) {
	t1 := typ(schema.String, length(5, 10, 20, 20), pat(`[a-zA-Z_][a-zA-Z0-9\-_.<]*\n[a-zA-Z0-9\-_.<]*`, false), pat(`p4.*\n`, true))
	t1x := typ(schema.String, length(5, 10, 20, 20), pat(`[a-zA-Z_][a-zA-Z0-9\-_.<]*`, false),
		pat(`p4.*`, true, "invalid pattern of value", "pattern-violation"))
	utf := typ(schema.String, length(5, 10), pat(`[€]{5,7}`, false))
	anchor := typ(schema.String, pat(`a.*b`, false))
	tp0 := typ(schema.String, pat(`[A-Za-z]*`, false, "pattern 0 error message", "pattern 0 err-apt-tag"),
		pat(`[A-Z]*`, false, "pattern 1 error message", "pattern 1 err-apt-tag"))
	runStore(t, []storeCase{
		{src: "string.c:938", t: t1, lex: "a\nbcde", h: jsonS, canon: "a\nbcde"},
		{src: "string.c:940", t: t1, lex: "p4abc\n", h: jsonS, err: "Unsatisfied pattern - \"p4abc\n\" does not match inverted \"p4.*\\n\"."},
		{src: "string.c:943", t: t1, lex: "ahojahojaho\njahojaho", h: jsonS, canon: "ahojahojaho\njahojaho"},
		{src: "string.c:946", t: t1, lex: "p4aЯ", h: jsonS, err: `Unsatisfied length - string "p4aЯ" length is not allowed.`},
		{src: "string.c:760", t: t1x, lex: "p4abc", h: xmlH, err: "invalid pattern of value", apptag: "pattern-violation"},
		{src: "string.c:763", t: t1x, lex: "p4a<", h: xmlH, err: `Unsatisfied length - string "p4a<" length is not allowed.`},
		{src: "string.c:806", t: utf, lex: "€€€€€", h: xmlH, canon: "€€€€€"},
		{src: "string.c:807", t: utf, lex: "€€€", h: xmlH, err: `Unsatisfied length - string "€€€" length is not allowed.`},
		{src: "string.c:809", t: utf, lex: "€€€€€€€€", h: xmlH, err: `Unsatisfied pattern - "€€€€€€€€" does not match "[€]{5,7}".`},
		{src: "string.c:815", t: anchor, lex: "aaaabbbb", h: xmlH, canon: "aaaabbbb"},
		{src: "string.c:817", t: anchor, lex: "abc", h: xmlH, err: `Unsatisfied pattern - "abc" does not match "a.*b".`},
		{src: "string.c:819", t: anchor, lex: "cab", h: xmlH, err: `Unsatisfied pattern - "cab" does not match "a.*b".`},
		{src: "string.c:876", t: typ(schema.String, pat(`[\p{IsSpecials}]+`, false)), lex: "￟�", h: xmlH,
			err: "Unsatisfied pattern - \"￟�\" does not match \"[\\p{IsSpecials}]+\"."},
		{src: "string.c:1062", t: typ(schema.String, length(6, 50, 120, 120)), lex: "121", h: xmlH, err: `Unsatisfied length - string "121" length is not allowed.`},
		{src: "test_pattern.c:AHOJ", t: tp0, lex: "AHOJ", h: xmlH, canon: "AHOJ"},
		{src: "test_pattern.c:T128", t: tp0, lex: "T128", h: xmlH, err: "pattern 0 error message", apptag: "pattern 0 err-apt-tag"},
		{src: "test_pattern.c:ahoj", t: tp0, lex: "ahoj", h: xmlH, err: "pattern 1 error message", apptag: "pattern 1 err-apt-tag"},
		{src: "string-store-only", t: anchor, lex: "abc", h: xmlH, only: true, canon: "abc"},
		{src: "string-ctrl-char", t: typ(schema.String), lex: "a\bb", h: xmlH, err: "Invalid character 0x8."},
		{src: "string-bad-utf8", t: typ(schema.String), lex: "a\xffb", h: xmlH, err: "Invalid character 0xff."},
		{src: "string-surrogate", t: typ(schema.String), lex: "\xed\xa0\x80", h: xmlH, err: "Invalid character 0xed."},
		{src: "string-json-number", t: typ(schema.String), lex: "1", h: jsonN, err: `Invalid non-string-encoded string value "1".`},
	})
}

func TestBoolEmptyEnum(t *testing.T) {
	b := typ(schema.Bool)
	e := typ(schema.Empty)
	en := typ(schema.Enumeration, enums("white"))
	runStore(t, []storeCase{
		{src: "boolean.c:78", t: b, lex: "true", h: xmlH, canon: "true"},
		{src: "boolean.c:79", t: b, lex: "false", h: xmlH, canon: "false"},
		{src: "boolean.c:82", t: b, lex: "unsure", h: xmlH, err: `Invalid boolean value "unsure".`},
		{src: "boolean.c:85", t: b, lex: " true", h: xmlH, err: `Invalid boolean value " true".`},
		{src: "bool-json-string", t: b, lex: "true", h: jsonS, err: `Invalid non-boolean-encoded boolean value "true".`},
		{src: "bool-json", t: b, lex: "false", h: jsonB, canon: "false"},
		{src: "empty.c:79", t: e, lex: "", h: xmlH, canon: ""},
		{src: "empty.c:82", t: e, lex: "x", h: xmlH, err: "Invalid empty value size 8 b."},
		{src: "empty-json-string", t: e, lex: "", h: jsonS, err: `Invalid non-empty-encoded empty value "".`},
		{src: "empty-json", t: e, lex: "", h: JSONHints("empty"), canon: ""},
		{src: "enumeration.c:88", t: en, lex: "white", h: xmlH, canon: "white"},
		{src: "enumeration.c:89", t: en, lex: "yellow", h: xmlH, err: `Invalid enumeration value "yellow".`},
		{src: "enumeration.c:91", t: en, lex: " white", h: xmlH, err: `Invalid enumeration value " white".`},
		{src: "enumeration.c:93", t: en, lex: "white\n", h: xmlH, err: "Invalid enumeration value \"white\n\"."},
	})
}

func TestBits(t *testing.T) {
	t0 := typ(schema.Bits, bits("two", 2, "ten", 10, "eleven", 11, "twelve", 12, "_test-end...", 13))
	runStore(t, []storeCase{
		{src: "bits.c:613", t: t0, lex: "ten two twelve", h: xmlH, canon: "two ten twelve"},
		{src: "bits.c:614", t: t0, lex: "ten\ntwo\ttwelve", h: xmlH, canon: "two ten twelve"},
		{src: "bits.c:616", t: t0, lex: "_test-end...", h: xmlH, canon: "_test-end..."},
		{src: "bits.c:617", t: t0, lex: "twelve\nten\ttwo  \n eleven", h: xmlH, canon: "two ten eleven twelve"},
		{src: "bits.c:618", t: t0, lex: "", h: xmlH, canon: ""},
		{src: "bits.c:619", t: t0, lex: "\n\t", h: xmlH, canon: ""},
		{src: "bits.c:625", t: t0, lex: "twelvea", h: xmlH, err: `Invalid bit "twelvea".`},
		{src: "bits.c:627", t: t0, lex: "twelve t", h: xmlH, err: `Invalid bit "t".`},
		{src: "bits.c:629", t: t0, lex: "ELEVEN", h: xmlH, err: `Invalid bit "ELEVEN".`},
		{src: "bits-duplicate", t: t0, lex: "ten two ten", h: xmlH, err: `Duplicate bit "ten".`},
		{src: "bits-json-number", t: t0, lex: "1", h: jsonN, err: `Invalid non-string-encoded bits value "1".`},
	})
}

func TestBinary(t *testing.T) {
	b := typ(schema.Binary)
	b2 := typ(schema.Binary, length(4, 4, 8, 8)) // binary.c: length "4 | 8"
	pem := strings.Repeat("QUJD", 16) + "\n" + "QUJD"
	runStore(t, []storeCase{
		{src: "binary.c:158", t: b, lex: "", h: HintString, canon: ""},
		{src: "binary.c:166", t: b, lex: "YQ==", h: HintString, canon: "YQ=="},
		{src: "binary.c:181", t: b2, lex: "Zm91cg==", h: HintString, canon: "Zm91cg=="},
		{src: "binary.c:195", t: b2, lex: "ZWlnaHQwMTI=", h: HintString, canon: "ZWlnaHQwMTI="},
		{src: "binary.c:212", t: b, lex: "q80.", h: HintString, err: "Invalid Base64 character '.'."},
		{src: "binary.c:220", t: b, lex: "q80", h: HintString, err: "Base64 encoded value length must be divisible by 4."},
		{src: "binary.c:228", t: b2, lex: "MTIz", h: HintString, err: `Unsatisfied length - string "MTIz" length is not allowed.`},
		{src: "binary.c:236", t: b2, lex: "MTIz", h: HintString, only: true, canon: "MTIz"},
		{src: "binary-pem", t: b, lex: pem, h: HintString, canon: strings.ReplaceAll(pem, "\n", "")},
		{src: "binary-pem-bad", t: b, lex: pem[:64] + "\n" + strings.Repeat("Q", 70), h: HintString, err: "Newlines are expected every 64 Base64 characters."},
		{src: "binary-nonprint", t: b, lex: "YQ=\x01", h: HintString, err: "Invalid Base64 character 0x1."},
		{src: "binary-high", t: b, lex: "YQ\xff=", h: HintString, err: "Invalid Base64 character 0xffffffff."},
		{src: "binary-pad3", t: b, lex: "Y===", h: HintString, err: "Invalid Base64 character '='."},
	})
}

func TestValueAccessorsAndOrder(t *testing.T) {
	must := func(v Value, d *Diag) Value {
		t.Helper()
		if d != nil {
			t.Fatal(d.Msg)
		}
		return v
	}
	i8 := typ(schema.Int8)
	a, b := must(Store(i8, "-5", FormatXML, xmlH, nil, nil)), must(Store(i8, "7", FormatXML, xmlH, nil, nil))
	if a.Int() != -5 || Compare(a, b) >= 0 || Compare(b, a) <= 0 || Equal(a, b) || !Equal(a, a) {
		t.Fatal("int8 order")
	}
	u64 := typ(schema.Uint64)
	if v := must(Store(u64, "18446744073709551615", FormatXML, xmlH, nil, nil)); v.Uint() != math.MaxUint64 {
		t.Fatal("uint64 max")
	}
	d := typ(schema.Dec64, fd(2))
	x, y := must(Store(d, "1.5", FormatXML, xmlH, nil, nil)), must(Store(d, "1.50", FormatXML, xmlH, nil, nil))
	if x.Dec64() != 150 || !Equal(x, y) || Compare(x, y) != 0 {
		t.Fatal("decimal64 equality")
	}
	en := typ(schema.Enumeration, func(t *schema.Type) {
		t.Enums = []*schema.Enum{{Name: "b", Value: -1}, {Name: "a", Value: 3}}
	})
	if Compare(must(Store(en, "b", FormatXML, xmlH, nil, nil)), must(Store(en, "a", FormatXML, xmlH, nil, nil))) >= 0 {
		t.Fatal("enum sorts by value")
	}
	bt := typ(schema.Bits, bits("a", 0, "b", 9))
	ba, bb := must(Store(bt, "b a", FormatXML, xmlH, nil, nil)), must(Store(bt, "a b", FormatXML, xmlH, nil, nil))
	if !Equal(ba, bb) || len(ba.Bits()) != 2 || ba.Canonical() != "a b" {
		t.Fatal("bits equality")
	}
	bin := typ(schema.Binary)
	if v := must(Store(bin, "YQ==", FormatXML, HintString, nil, nil)); string(v.Bytes()) != "a" {
		t.Fatal("binary decode")
	}
	if Compare(must(Store(bin, "YQ==", FormatXML, HintString, nil, nil)), must(Store(bin, "", FormatXML, HintString, nil, nil))) <= 0 {
		t.Fatal("binary sorts by size first")
	}
	bo := typ(schema.Bool)
	if !must(Store(bo, "true", FormatXML, xmlH, nil, nil)).Bool() {
		t.Fatal("bool")
	}
	if Equal(a, must(Store(typ(schema.Int8), "-5", FormatXML, xmlH, nil, nil))) {
		t.Fatal("values of different types are never equal")
	}
}

// FuzzStore: no input may panic, and success always yields a value re-storable from its
// canonical form with the same canonical form.
func FuzzStore(f *testing.F) {
	ts := []*schema.Type{
		typ(schema.Int8, rng(-50, 50)), typ(schema.Int64), typ(schema.Uint64, urng(1, 10)), typ(schema.Uint16),
		typ(schema.Dec64, fd(1)), typ(schema.Dec64, fd(18)), typ(schema.String, length(0, 3), pat(`a+`, false)),
		typ(schema.Bool), typ(schema.Empty), typ(schema.Enumeration, enums("a", "b")),
		typ(schema.Bits, bits("x", 0, "y", 33)), typ(schema.Binary, length(0, 5)),
	}
	for _, s := range []string{"", "0", "-1", "+0x7F", "1.25", "a b", "x y", "YQ==", "aaa", "true", "-9223372036854775808", "\xff"} {
		for i := range ts {
			f.Add(uint8(i), s, uint16(xmlH)) //nolint:gosec // seed corpus
		}
	}
	f.Fuzz(func(t *testing.T, ti uint8, lex string, h uint16) {
		tp := ts[int(ti)%len(ts)]
		v, d := Store(tp, lex, FormatXML, Hints(h), nil, nil)
		if d != nil {
			if d.Msg == "" || d.Code != CodeData {
				t.Fatalf("empty diag for %q", lex)
			}
			return
		}
		v2, d := Store(tp, v.Canonical(), FormatXML, HintSchema, nil, nil)
		if tp.Base == schema.Empty || tp.Base == schema.Binary {
			return // canonical == lexical for these
		}
		if d != nil || v2.Canonical() != v.Canonical() || !Equal(v, v2) {
			t.Fatalf("canonical %q of %q does not round-trip: %v", v.Canonical(), lex, d)
		}
	})
}
