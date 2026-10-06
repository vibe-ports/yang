// SPDX-License-Identifier: BSD-3-Clause

package lyjson

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// lex walks the whole input and returns "token" / "token=value" entries (value only where libyang
// has one) and the first error. Expectations in this file come from libyang v5.8.6: json.c
// compiled verbatim in a harness (token, depth, line, value per step; ~30k inputs compared with
// this port), and yanglint for the messages as the data parser sees them.
func lex(in string) (toks []string, err error) {
	l, err := New([]byte(in))
	if err != nil {
		return nil, err
	}
	for {
		st := l.Status()
		switch st {
		case TokenString, TokenNumber, TokenObjectName, TokenTrue, TokenFalse, TokenNull:
			toks = append(toks, fmt.Sprintf("%s=%s", st, l.Value()))
		default:
			toks = append(toks, st.String())
		}
		if st == TokenEnd {
			return toks, nil
		}
		if st, err = l.Next(); err != nil {
			return toks, err
		}
		if st == TokenEnd {
			return toks, nil
		}
	}
}

func diags(err error) []string {
	var e *Error
	if !errors.As(err, &e) {
		return nil
	}
	var out []string
	for _, d := range e.Diags {
		out = append(out, fmt.Sprintf("%d@%d:%s", d.Code, d.Line, d.Msg))
	}
	return out
}

func TestTokens(t *testing.T) {
	toks, err := lex(` {"a" : [1, "x" ,true,false,null,{}], "b":-0.0}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "object|object name=a|array|number=1|array next|string=x|array next|true=true|array next|false=false|" +
		"array next|null=|array next|object|object closed|array closed|object next|object name=b|number=-0|object closed"
	if got := strings.Join(toks, "|"); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestNumbers(t *testing.T) {
	for in, want := range map[string]string{
		"0": "0", "-0": "-0", "0.0": "0", "-0.0": "-0", "0e5": "0", "0.000e-3": "0",
		"1.50": "1.50", "1.50e0": "1.50", "1e+00": "1", "1E-0": "1", // zero exponent: mantissa as is
		"1e2": "100", "1.5e2": "150", "1.5e1": "15", "1.5e-1": "0.15", "1.0e-1": "0.1", "100e-1": "10",
		"0.5e2": "50", "0.05e1": "0.5", "0.005e2": "0.5", "0.0005e2": "0.05", "-1.5e-2": "-0.015",
		"123.456e2": "12345.6", "123.456e-2": "1.23456", "123.4560e1": "1234.56", "1e20": "100000000000000000000",
		"0.01e2": "1", "1000e-4": "0.1", "12e-3": "0.012", "0.0000000000000000001e19": "1",
		"12345678901234567890.1": "12345678901234567890.1", // 22 bytes: the longest plain number
		// libyang's exponent rewrite is wrong when a leading-zero mantissa shifts the point inside
		// its digits; a conforming port reproduces the text (fixture json/exp-number, D-0054 notes).
		"0.5e1": ".", "-0.5e1": "-.", "0.123e3": "12.", "0.10e1": ".", "0.1e1": ".",
	} {
		toks, err := lex("[" + in + "]")
		if err != nil || len(toks) != 3 || toks[1] != "number="+want {
			t.Errorf("%s: got %v %v, want %q", in, toks, diags(err), want)
		}
	}
}

func TestErrors(t *testing.T) {
	// want is "<code>@<line>:<message>", code 1 = LYVE_SYNTAX, 2 = LYVE_SEMANTICS, 0 = none.
	long := "Number encoded as a string exceeded the LY_NUMBER_MAXLEN limit."
	for _, c := range []struct{ in, want string }{
		{"", "1@1:Empty JSON file."},
		{" \n\n  \n", "1@4:Empty JSON file."},
		{`{`, "1@1:Unexpected end-of-input."},
		{`{"m:c"`, `1@1:Invalid character sequence "", expected a JSON value name-separator ':'.`},
		{`{"m:c" 1}`, `1@1:Invalid character sequence "1}", expected a JSON value name-separator ':'.`},
		{`{,}`, `1@1:Invalid character sequence ",}", expected a JSON object name.`},
		{`{"a":1 "b":2}`, `1@1:Invalid character sequence ""b":2}", expected a JSON object-end or next item.`},
		{`[1 2]`, `1@1:Invalid character sequence "2]", expected a JSON array-end or next item.`},
		{"[1,\n2,\n\n}", `1@4:Invalid character sequence "}", expected a JSON value.`},
		{`[tru]`, `1@1:Invalid character sequence "tru]", expected a JSON value.`},
		{`[+1]`, `1@1:Invalid character sequence "+1]", expected a JSON value.`},
		{`[1,]`, `1@1:Invalid character sequence "]", expected a JSON value.`},
		{`[abcdefghijklmnopqrstuvwxyz]`, `1@1:Invalid character sequence "abcdefghijklmnopqrst", expected a JSON value.`},
		{`[-]`, `1@1:Invalid character in JSON Number value ("]").`},
		{`[1.]`, `1@1:Invalid character in JSON Number value ("]").`},
		{`[1.e5]`, `1@1:Invalid character in JSON Number value ("e").`},
		{`[1e+]`, `1@1:Invalid character in JSON Number value ("]").`},
		{`[1e`, `1@1:Unexpected end-of-input.`},
		{`[1e21]`, "2@1:" + long},
		{`[1e-20]`, "2@1:" + long},
		{`[123456789012345678901234]`, "2@1:" + long},
		{`[1e65536]`, "2@1:Exponent out-of-bounds in a JSON Number value (1e65536)."},
		{`[1e99999999999999999999]`, "2@1:Exponent out-of-bounds in a JSON Number value (1e99999999999999999999)."},
		{`["a`, "1@1:Unexpected end-of-input.|1@1:Missing quotation-mark at the end of a JSON string."},
		{"\n\n[\"a\\u12\"]", "1@3:Unexpected end-of-input.|1@3:Missing quotation-mark at the end of a JSON string."},
		{"\"a\nb\"", "1@1:Invalid character in JSON string \"a\n\" (0x0000000a)."},
		{"[\n\"tab\there\"]", "1@2:Invalid character in JSON string \"tab\t\" (0x00000009)."},
		{"[\"\x01\"]", "1@1:Invalid character 0x1."},
		{"[\"\xc3(\"]", "1@1:Invalid character 0xc3."},
		{"[\"\xef\xbf\xbe\"]", "1@1:Invalid character 0xef."},
		{`["\q"]`, `1@1:Invalid character escape sequence \q.`},
		{`["x\`, `1@1:Invalid character escape sequence \`}, // C: %c of NUL ends the message
		{`["\ud83d\ude00"]`, `1@1:Invalid character reference "\ud83d" (0x0000d83d).`},
		{`["\u0000"]`, `1@1:Invalid character reference "\u0000" (0x00000000).`},
		{`["\b"]`, `1@1:Invalid character reference "\b" (0x00000008).`},
		{`["\uFFFE"]`, `1@1:Invalid character reference "\uFFFE" (0x0000fffe).`},
		{`["\uFDD0"]`, `1@1:Invalid character reference "\uFDD0" (0x0000fdd0).`},
		{`["\u"]`, `1@1:Invalid basic multilingual plane character "\u"]".`}, // the rest of the input
		{`["\u12"`, `1@1:Invalid basic multilingual plane character "\u12"".`},
		{`["\u"g00"]`, `1@1:Invalid character reference "\u"g00" (0xfffec000).`}, // non-hex wraps like C uint32
	} {
		toks, err := lex(c.in)
		if err == nil {
			t.Errorf("%q: no error, tokens %v", c.in, toks)
			continue
		}
		got := strings.Join(diags(err), "|")
		if got != c.want {
			t.Errorf("%q:\n got %s\nwant %s", c.in, got, c.want)
		}
	}
}

func TestStrings(t *testing.T) {
	for in, want := range map[string]string{
		`"abc"`: "abc", `""`: "", `"a\nb"`: "a\nb", `"\u0041\u00e9\u20ac"`: "Aé€", `"\/\\\""`: `/\"`,
		`"\u0009"`: "\t", `"\uFFFD"`: "\uFFFD", `"\u007f"`: "\x7f", `"é€😀"`: "é€😀",
		"\"\xf0\x91\x80\x80\"":         "\xf0\x91\x80\x80", // libyang accepts 4-byte forms of U+1000..U+FFFF
		`"\u12G4"`:                     "\u1104",           // 'G' > 'F' takes the lower-case branch: 10+('G'-'a') wraps
		`"\u00zz"`:                     "\u0253",           // 'z'-'a'+10 = 35 per digit
		`"x\u0041y\u0042z"`:            "xAyBz",
		"\"\xef\xbf\xbd\xed\x9f\xbf\"": "\ufffd\ud7ff",
	} {
		toks, err := lex(in)
		if err == nil && len(toks) == 1 && toks[0] == "string="+want {
			continue
		}
		t.Errorf("%s: %q %v", in, toks, diags(err))
	}
}

func TestNULEndsInput(t *testing.T) {
	// libyang parses NUL-terminated memory: a NUL byte is the end of the input.
	if _, err := New([]byte("\x00{}")); diags(err)[0] != "1@1:Empty JSON file." {
		t.Fatalf("%v", err)
	}
	toks, err := lex("[1]\x00garbage")
	if err != nil || strings.Join(toks, "|") != "array|number=1|array closed" {
		t.Fatalf("%v %v", toks, err)
	}
	if _, err := lex("[1\x00]"); diags(err)[0] != "1@1:Unexpected end-of-input." {
		t.Fatalf("%v", err)
	}
}

func TestTrailingData(t *testing.T) {
	// After the top-level value libyang stops lexing: trailing input is the caller's business.
	l, err := New([]byte(`{"a":1} x`))
	if err != nil {
		t.Fatal(err)
	}
	var st Token
	for st != TokenEnd {
		if st, err = l.Next(); err != nil {
			t.Fatal(err)
		}
	}
	if l.Depth() != 0 || l.Offset() != 8 {
		t.Fatalf("depth %d offset %d", l.Depth(), l.Offset())
	}
}

func TestLines(t *testing.T) {
	l, err := New([]byte("\n\n{\n\"a\":\n[1,\n\n2]}"))
	if err != nil {
		t.Fatal(err)
	}
	var lines []uint64
	for st := l.Status(); st != TokenEnd; {
		lines = append(lines, l.Line())
		if st, err = l.Next(); err != nil {
			t.Fatal(err)
		}
	}
	// the line is that of the position after the token and the whitespace following it
	if fmt.Sprint(lines) != "[4 5 5 5 7 7 7 7]" {
		t.Fatalf("%v", lines)
	}
}

// Boundary pinned by design 07 (depth/json-5000 and its 4998 control): libyang fails when the
// status stack exceeds 5000 after a push, which includes the push of the `]` closing an empty array.
func TestNestingLimit(t *testing.T) {
	arrays := func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }
	wrapped := func(n int) string { return `{"m:zz":` + arrays(n) + `}` } // how the data parser meets it
	for _, c := range []struct {
		in   string
		fail bool
	}{
		{arrays(4999), false}, {arrays(5000), true}, {arrays(5001), true},
		{wrapped(4998), false}, {wrapped(4999), true},
		{strings.Repeat(`{"a":`, 4999) + "1" + strings.Repeat("}", 4999), false},
		{strings.Repeat(`{"a":`, 5000) + "1" + strings.Repeat("}", 5000), true},
		{strings.Repeat("[", 5000), false}, // 5000 openings alone still fit; the input just ends
	} {
		_, err := lex(c.in)
		switch {
		case c.fail && !errors.Is(err, ErrNesting):
			t.Errorf("%.20s…: want ErrNesting, got %v", c.in, err)
		case !c.fail && errors.Is(err, ErrNesting):
			t.Errorf("%.20s…: unexpected %v", c.in, err)
		case c.fail:
			if d := diags(err); len(d) != 1 || d[0] != "0@0:Maximum number 5000 of nestings has been exceeded." {
				t.Errorf("%v", d)
			}
		}
	}
	l, _ := New([]byte(arrays(5000)))
	for {
		if _, err := l.Next(); err != nil {
			break
		}
	}
	if l.Depth() != MaxDepth+1 {
		t.Fatalf("depth after failing push: %d", l.Depth())
	}
}

func TestBackupRestore(t *testing.T) {
	l, err := New([]byte(`{"a":[1,"x\n"],"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	next := func() { // object -> name a -> array -> 1 -> ...
		t.Helper()
		if _, err := l.Next(); err != nil {
			t.Fatal(err)
		}
	}
	next() // object name a
	next() // array
	next() // number 1
	l.Backup()
	for i := 0; i < 3; i++ {
		next()
	}
	if l.Status() != TokenArrayClosed {
		t.Fatalf("%v", l.Status())
	}
	l.Restore()
	if l.Status() != TokenNumber || l.Value() != "1" || l.Depth() != 3 {
		t.Fatalf("%v %q %d", l.Status(), l.Value(), l.Depth())
	}
	next()
	if l.Status() != TokenArrayNext {
		t.Fatalf("%v", l.Status())
	}
	next()
	if l.Status() != TokenString || l.Value() != "x\n" {
		t.Fatalf("%v %q", l.Status(), l.Value())
	}
	// Restore without Backup does nothing (libyang: undefined); Restore twice repeats.
	l2, _ := New([]byte("[1]"))
	l2.Restore()
	if l2.Status() != TokenArray {
		t.Fatal(l2.Status())
	}
}

func TestTokenString(t *testing.T) {
	want := "error|object|object next|object closed|array|array next|array closed|object name|number|string|true|false|null|end of input"
	var got []string
	for tok := TokenError; tok <= TokenEnd; tok++ {
		got = append(got, tok.String())
	}
	if strings.Join(got, "|") != want || Token(99).String() != "" {
		t.Fatal(got)
	}
}
