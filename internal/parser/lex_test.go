// SPDX-License-Identifier: BSD-3-Clause
// Cases marked [ly] are adapted from libyang v5.8.6 tests/utests/schema/test_yang.c
// (BSD-3-Clause, © CESNET).

package parser

import (
	"errors"
	"strings"
	"testing"
)

func mod(ver, body string) string {
	v := ""
	if ver != "" {
		v = "  yang-version " + ver + ";\n"
	}
	return "module m {\n" + v + "  namespace \"urn:m\";\n  prefix m;\n" + body + "\n}\n"
}

// fidelity are G1's argument-fidelity strings (description of module m);
// want is libyang's YIN <text>, checked against yanglint by TestOracleFidelity.
var fidelity = []struct{ src, want string }{
	{"  description \"hello \t\n\t\t     world!\";", "hello\n      world!"},
	{"  description \"hello \\t\n\t\t     \\t world!\";", "hello \t\n      \t world!"},
	{"  description \"hello\\n\t\t world!\";", "hello\n\t\t world!"},
	{"  description \"a\n     b\n        c\";", "a\nb\nc"},
	{"  description \"a\n\t  b\";", "a\nb"},
	{"  description\n    \"a\n\t  b\";", "a\n     b"},
	{"  description \"hel\"  +\t\n  \"lo\";", "hello"},
	{"  description 'a  \n     b';", "a  \n     b"},
	{`  description "x\n\t\"\\y";`, "x\n\t\"\\y"},
	{`  description "ünï ✓";`, "ünï ✓"},
	{"  description \"a  \";", "a  "},
	{"  description \"a\r\n   b\";", "a\nb"},
	{"  description 'a' + \"b\n     c\";", "ab\n     c"}, // indentation is set by the first part
	{"  description \"a\tb \\t\";", "a\tb \t"},
}

func TestFidelity(t *testing.T) {
	for _, c := range fidelity {
		s, err := Parse("", []byte(mod("1.1", c.src)), nil)
		if err != nil || s.Subs[3].Arg != c.want {
			t.Errorf("%q: got %v; want %q", c.src, err, c.want)
			continue
		}
	}
}

// lexArg runs getArgument on in with libyang's test setup (indent preset).
func lexArg(in string, arg argKind, indent int) (string, string, error) {
	l := &lexer{src: []byte(in), indent: indent, line: 1, b: Budget{MaxDepth: 500}}
	w, _, err := l.getArgument(arg)
	return string(w.buf), in[l.off:], err
}

func TestGetArgument(t *testing.T) { // [ly] test_comments, test_arg
	for _, c := range []struct {
		in     string
		arg    argKind
		indent int
		word   string
		rest   string
		err    string
	}{
		{in: " // this is a text of / one * line */ comment\nargument;", arg: argStr, word: "argument", rest: ";"},
		{in: "/* this is a \n * text // of / block * comment */\"arg\" + \"ume\" \n + \n \"nt\";", arg: argStr, word: "argument", rest: ";"},
		{in: ";", arg: argMaybeStr, rest: ";"},
		{in: "{", arg: argStr, err: `Invalid character sequence "{", expected an argument.`},
		{in: `"\s"`, arg: argStr, err: `Double-quoted string unknown special character '\s'.`},
		{in: `'\s'`, arg: argStr, word: `\s`},
		{in: `hello"`, arg: argStr, err: `Invalid character sequence """, expected unquoted string character, optsep, semicolon or opening brace.`},
		{in: `hello}`, arg: argStr, err: `Invalid character sequence "}", expected unquoted string character, optsep, semicolon or opening brace.`},
		{in: "pre:pre:value", arg: argPrefIdent, err: `Invalid identifier character ':' (0x003a).`},
		{in: `"";`, arg: argIdent, err: "Statement argument is required."},
		{in: `"";`, arg: argPrefIdent, err: "Statement argument is required."},
		{in: "hello/x\t", arg: argStr, word: "hello/x", rest: "\t"},
		{in: "hello ", arg: argStr, word: "hello", rest: " "},
		{in: `"hello\n\t\"\\";`, arg: argStr, word: "hello\n\t\"\\", rest: ";"},
		{in: "\"hello \t\n\t\t world!\"", arg: argStr, indent: 14, word: "hello\n  world!"},
		{in: "\"hello \\t\n\t\\t world!\"", arg: argStr, indent: 14, word: "hello \t\n\t world!"},
		{in: "\"hello\\n\t\t world!\"", arg: argStr, indent: 14, word: "hello\n\t\t world!"},
		{in: "\"hello\n \tworld!\"", arg: argStr, indent: 14, word: "hello\nworld!"},
		{in: `'hello'`, arg: argStr, word: "hello"},
		{in: "\"hel\"  +\t\n\"lo\"", arg: argStr, word: "hello"},
		{in: "\"hel\"  +\t\nlo", arg: argStr, err: "Both string parts divided by '+' must be quoted."},
		{in: "'he'\t\n+ \"llo\"", arg: argStr, word: "hello"},
		{in: " \t\n\"he\"+'llo'", arg: argStr, word: "hello"},
		{in: ";", arg: argStr, err: `Invalid character sequence ";", expected an argument.`},
		// not from libyang
		{in: "\"a\r\n   b\"", arg: argStr, indent: 2, word: "a\nb"},
		{in: "'a\r\nb'", arg: argStr, word: "a\r\nb"},
		{in: "a//b", arg: argStr, err: `Invalid comment sequence "//" in an unquoted string.`},
		{in: "\"a\x01\"", arg: argStr, err: "Invalid character 0x1."},
		{in: "\"\xff\"", arg: argStr, err: "Invalid character 0xff."},
		{in: "1abc", arg: argIdent, err: "Invalid identifier first character '1' (0x0031)."},
		{in: "a\r", arg: argStr, err: "Invalid character 0xd."},
		{in: "abc", arg: argStr, err: "Unexpected end-of-input."},
	} {
		w, rest, err := lexArg(c.in, c.arg, c.indent)
		var msg string
		if e := (*Error)(nil); errors.As(err, &e) {
			msg = e.Msg
		}
		if msg != c.err || err == nil && (w != c.word || rest != c.rest) {
			t.Errorf("%q: got %q rest %q err %q, want %q rest %q err %q", c.in, w, rest, msg, c.word, c.rest, c.err)
		}
	}
}

func TestGetKeyword(t *testing.T) { // [ly] test_stmts
	for _, c := range []struct{ in, kw, rest, err string }{
		{in: "\n// comment\n\tinput\t{", kw: "input", rest: "\t{"},
		{in: "\t /* comment */\t output\n\t{", kw: "output", rest: "\n\t{"},
		{in: "/input { ", err: "Invalid identifier first character '/'."},
		{in: "not-a-statement-nor-extension { ", err: `Invalid character sequence "not-a-statement-nor-extension", expected a keyword.`},
		{in: "path;", err: `Invalid character sequence "path;", expected a keyword followed by a separator.`},
		{in: "input{", kw: "input", rest: "{"},
		{in: ";config false;", kw: ";", rest: "config false;"},
		{in: "nacm:default-deny-write;", kw: "nacm:default-deny-write", rest: ";"},
		{in: "leafy:x ", kw: "leafy:x", rest: " "},
		{in: "leaf-list ", kw: "leaf-list", rest: " "},
		{in: "typedef ", kw: "typedef", rest: " "},
		{in: "revision-date ", kw: "revision-date", rest: " "},
		{in: "type-x:y ", err: `Invalid character sequence "type-", expected a keyword followed by a separator.`},
		{in: "m:m:x ", err: `Invalid identifier character ':' (0x003a).`},
		{in: `"leaf" a`, err: `Invalid identifier first character '"' (0x0022).`},
	} {
		l := &lexer{src: []byte(c.in), line: 1, b: Budget{MaxDepth: 500}}
		kw, _, _, err := l.getKeyword()
		var msg string
		if e := (*Error)(nil); errors.As(err, &e) {
			msg = e.Msg
		}
		if msg != c.err || err == nil && (kw != c.kw || c.in[l.off:] != c.rest) {
			t.Errorf("%q: got %q rest %q err %q", c.in, kw, c.in[l.off:], msg)
		}
	}
	for _, kw := range strings.Fields(`action anydata anyxml argument augment base belongs-to bit case choice config
		contact container default description deviate deviation enum error-app-tag error-message extension feature
		fraction-digits grouping identity if-feature import include input key leaf leaf-list length list mandatory
		max-elements min-elements modifier module must namespace notification ordered-by organization output path
		pattern position prefix presence range reference refine require-instance revision revision-date rpc status
		submodule type typedef unique units uses value when yang-version yin-element`) {
		l := &lexer{src: []byte(kw + " "), line: 1}
		if got, ext, _, err := l.getKeyword(); err != nil || ext || got != kw {
			t.Errorf("%s: got %q ext=%v err %v", kw, got, ext, err)
		}
	}
}

func TestParseTree(t *testing.T) {
	src := "module m { namespace urn:m; prefix m; extension e; extension f;\n  m:e \"x\" + 'y' { m:f; leaf 1; }\n  container c {\n    leaf a { type string; }\n  }\n}\n"
	s, err := Parse("m.yang", []byte(src), nil)
	if err != nil {
		t.Fatal(err)
	}
	e := s.Subs[4]
	if e.ExtPrefix != "m" || e.Keyword != "e" || e.Arg != "xy" || e.Quote != '"' || len(e.Subs) != 2 ||
		e.Subs[1].Keyword != "leaf" || e.Subs[1].Arg != "1" || e.Pos != (Pos{2, 3}) {
		t.Errorf("ext: %+v", e)
	}
	leaf := s.Subs[5].Subs[0]
	if leaf.Keyword != "leaf" || leaf.Arg != "a" || leaf.Pos != (Pos{4, 5}) || leaf.End != (Pos{4, 27}) {
		t.Errorf("leaf: %+v", leaf)
	}
	if leaf.Subs[0].Arg != "string" {
		t.Errorf("type: %+v", leaf.Subs[0])
	}
}

const hdr = "module m {\n  namespace urn:m;\n  prefix m;\n"

// parseErrors are tokenizer errors in otherwise valid modules; TestOracleErrors
// checks message and line against yanglint.
var parseErrors = []struct {
	src  string
	line int
	msg  string
}{
	{"module m { namespace urn:m; prefix m; } module q {namespace urn:q;prefix q;}", 1, `Trailing garbage "module q {names..." after module, expected end-of-input.`}, // [ly]
	{"prefix m {}", 1, `Invalid keyword "prefix", expected "module" or "submodule".`},                                                                                 // [ly]
	{hdr + "  ;\n}", 4, `Invalid keyword ";" as a child of "module".`},
	{hdr + "  leaf a }\n", 4, `Invalid keyword "}", expected ";" or "{".`},
	{hdr + "  leaf a} }", 4, `Invalid character sequence "}", expected unquoted string character, optsep, semicolon or opening brace.`},
	{hdr + "  leaf a b; }", 4, `Invalid character sequence "b", expected a keyword.`},
	{hdr + "  /* x\n", 5, "Unexpected end-of-input, non-terminated comment."},
	{hdr, 4, "Unexpected end-of-input."},
	{hdr + "  rpc r { input x; }\n}", 4, `Invalid character sequence "x", expected a keyword.`},
	{hdr + "  /leaf a;\n}", 4, "Invalid identifier first character '/'."},
	{hdr + "  path;\n}", 4, `Invalid character sequence "path;", expected a keyword followed by a separator.`},
	{hdr + "  not-a-statement x;\n}", 4, `Invalid character sequence "not-a-statement", expected a keyword.`},
	{hdr + "  m:m:x;\n}", 4, `Invalid identifier character ':' (0x003a).`},
	{hdr + "  \"leaf\" a;\n}", 4, `Invalid identifier first character '"' (0x0022).`},
	{hdr + "  leaf 1abc;\n}", 4, `Invalid identifier first character '1' (0x0031).`},
	{hdr + "  leaf \"\";\n}", 4, "Statement argument is required."},
	{hdr + "  uses pre:pre:value;\n}", 4, `Invalid identifier character ':' (0x003a).`},
	{hdr + "  description \"\\s\";\n}", 4, `Double-quoted string unknown special character '\s'.`},
	{hdr + "  description hello\"x\";\n}", 4, `Invalid character sequence """, expected unquoted string character, optsep, semicolon or opening brace.`},
	{hdr + "  description \"a\x01\";\n}", 4, "Invalid character 0x1."},
	{hdr + "  description \"\xff\";\n}", 4, "Invalid character 0xff."},
	{hdr + "  description a//b;\n}", 4, `Invalid comment sequence "//" in an unquoted string.`},
	{hdr + "  description \"hel\"  +\t\nlo;\n}", 5, "Both string parts divided by '+' must be quoted."},
	{hdr + "  description ;\n}", 4, `Invalid character sequence ";", expected an argument.`},
	{hdr + "  description a\rb;\n}", 4, "Invalid character 0xd."},
	// libyang counts a line for a '\r' stored as '\n'
	{hdr + "  description \"a\rb\" x;\n}", 5, `Invalid character sequence "x", expected a keyword.`},
	{hdr + "  description \"a\r\\nb\" x;\n}", 5, `Invalid character sequence "x", expected a keyword.`},
	{hdr + "  description \"a\\nb\" x;\n}", 4, `Invalid character sequence "x", expected a keyword.`},
	{hdr + "  description 'a\r\nb' x;\n}", 5, `Invalid character sequence "x", expected a keyword.`},
	{hdr + "  // c\n  /* d\n */ x;\n}", 6, `Invalid character sequence "x", expected a keyword.`},
}

func TestParseErrors(t *testing.T) {
	for _, c := range parseErrors {
		_, err := Parse("", []byte(c.src), nil)
		var e *Error
		if !errors.As(err, &e) || e.Msg != c.msg || e.Pos.Line != c.line {
			t.Errorf("%q: got %v, want %d: %q", c.src, err, c.line, c.msg)
		}
	}
	if s, err := Parse("", []byte("module m {namespace urn:m; prefix m;}\x00garbage"), nil); err != nil || s.Keyword != "module" {
		t.Errorf("NUL ends input: %v", err)
	}
	// parse_ext_substmt keeps ';' and '{' as generic statements
	if s, err := Parse("", []byte(hdr+"  extension e;\n  m:e x { ;; }\n}"), nil); err != nil || s.Subs[3].Subs[0].Keyword != ";" {
		t.Errorf("';' in extension instance: %v", err)
	}
}

func TestBudget(t *testing.T) {
	deep := "module m { namespace urn:m; prefix m;" + strings.Repeat("container c {", 10) + strings.Repeat("}", 11)
	for _, c := range []struct {
		src string
		b   Budget
	}{
		{deep, Budget{MaxDepth: 5}},
		{deep, Budget{MaxStmts: 5}},
		{"module m { description \"" + strings.Repeat("x", 100) + "\"; }", Budget{MaxArgLen: 10}},
		{deep, Budget{MaxBytes: 10}},
	} {
		if _, err := Parse("", []byte(c.src), &c.b); !errors.Is(err, ErrBudget) {
			t.Errorf("%+v: got %v, want ErrBudget", c.b, err)
		}
	}
	if _, err := Parse("", []byte(deep), nil); err != nil {
		t.Error(err)
	}
}
