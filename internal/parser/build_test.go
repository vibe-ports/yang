// SPDX-License-Identifier: BSD-3-Clause
// Cases marked [ly] are adapted from libyang v5.8.6 tests/utests/schema/test_yang.c
// (BSD-3-Clause, © CESNET).

package parser

import (
	"errors"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/ly"
)

func parseBuild(src string) (*Module, error) {
	s, err := Parse("m.yang", []byte(src), nil)
	if err != nil {
		return nil, err
	}
	return Build(s)
}

// g1Cases are the parse-level cases of gate G1 (docs/decisions/0001-parser-reuse.md,
// spike/g1-goyang) with yanglint's verdict.
var g1Cases = []struct {
	name, src string
	ok        bool
}{
	{"baseline valid module", mod("1.1", `  leaf a { type string; }`), true},
	{`bad escape "\x" (1.1)`, mod("1.1", `  description "a\x";`), false},
	{`bad escape "\x" (1.0)`, mod("", `  description "a\x";`), false},
	{`bad escape "\s" [ly]`, mod("1.1", `  description "\s";`), false},
	{`pattern "\d" in dquotes (1.1)`, mod("1.1", `  leaf a { type string { pattern "\d+"; } }`), false},
	{`single-quoted '\x' is literal [ly]`, mod("1.1", `  description 'a\x';`), true},
	{"unknown keyword, no prefix [ly]", mod("1.1", `  container c { not-a-statement-nor-extension x; }`), false},
	{"extension use, prefix not imported", mod("1.1", `  container c { zz:foo "a"; }`), false},
	{"extension use, defined + own prefix", mod("1.1", "  extension e { argument v; }\n  container c { m:e \"a\"; }"), true},
	{"extension use, undefined ext own prefix", mod("1.1", `  container c { m:nosuch "a"; }`), false},
	{"duplicate description in leaf", mod("1.1", `  leaf a { type string; description x; description y; }`), false},
	{"duplicate type in leaf", mod("1.1", `  leaf a { type string; type int8; }`), false},
	{"leaf without type", mod("1.1", `  leaf a { description x; }`), false},
	{"module without namespace [ly]", "module m { prefix m; }\n", false},
	{"identifier starts with digit", mod("1.1", `  leaf 1abc { type string; }`), false},
	{"identifier with bad char", mod("1.1", `  leaf a#b { type string; }`), false},
	{"prefixed identifier pre:pre:x [ly]", mod("1.1", `  leaf a { type m:m:string; }`), false},
	{"yang-version 2", mod("2", ""), false},
	{`yang-version "1.1" quoted`, mod(`"1.1"`, `  leaf a { type string; }`), true},
	{"duplicate yang-version [ly]", mod("1.1", "  yang-version 1.1;"), false},
	{"stray ';' in module body", mod("1.1", "  ;\n  leaf a { type string; }"), false},
	{"';' after closing brace", mod("1.1", `  leaf a { type string; };`), false},
	{`concat "a" + 'b' [ly]`, mod("1.1", `  description "a" + 'b';`), true},
	{`concat "a" + b (unquoted) [ly]`, mod("1.1", `  description "a" + b;`), false},
	{"unquoted arg followed by '\"' [ly]", mod("1.1", `  description hello"x";`), false},
	{"unterminated block comment [ly]", mod("1.1", "  /* never closed\n"), false},
	{"unquoted arg containing //", mod("1.1", `  leaf a { type string; units abc//def; }`), false},
	{"keyword given as quoted string", mod("1.1", `  "leaf" a { type string; }`), false},
	{"missing argument [ly]", mod("1.1", `  leaf { type string; }`), false},
	{`empty identifier "" [ly]`, mod("1.1", `  leaf "" { type string; }`), false},
	{"missing closing brace", strings.TrimSuffix(mod("1.1", ""), "}\n"), false},
	{"extra closing brace", mod("1.1", "") + "}\n", false},
	{"trailing garbage after module [ly]", mod("1.1", "") + "module q { namespace urn:q; prefix q; }\n", false},
	{"mandatory maybe", mod("1.1", `  leaf a { type string; mandatory maybe; }`), false},
	{"config True", mod("1.1", `  container c { config True; }`), false},
	{"bad revision date 2020-13-45", mod("1.1", `  revision 2020-13-45;`), false},
	{"min-elements -1 [ly]", mod("1.1", `  leaf-list a { type string; min-elements -1; }`), false},
	{"position under enum [ly]", mod("1.1", `  leaf a { type enumeration { enum x { position 1; } } }`), false},
	{"import after body stmt (order)", mod("1.1", "  container c;\n  import ietf-yang-types { prefix yt; }"), false},
	{"control char U+0001 in string", mod("1.1", "  description \"a\x01b\";"), false},
	{"CRLF line endings", strings.ReplaceAll(mod("1.1", `  leaf a { type string; }`), "\n", "\r\n"), true},
	{"input outside rpc/action", mod("1.1", `  container c { input { leaf x { type string; } } }`), false},
	{"valid: two augments in one uses", mod("1.1", "  grouping g { container a; container b; }\n  container t { uses g { augment a { leaf x { type string; } } augment b { leaf y { type string; } } } }"), true},
	{"valid: choice as shorthand case of choice", mod("1.1", `  container t { choice c { choice d { leaf x { type string; } } } }`), true},
	{"valid: when { reference }", mod("1.1", `  container t { when "true()" { reference r; } }`), true},
}

func TestG1Cases(t *testing.T) {
	for i, c := range g1Cases {
		_, err := parseBuild(c.src)
		if (err == nil) != c.ok {
			t.Errorf("G1 #%d %s: err %v, yanglint ok=%v", i+1, c.name, err, c.ok)
		}
	}
}

const (
	sb11 = "module name {yang-version 1.1;namespace urn:x;prefix \"x\";"
	sb10 = "module name {namespace urn:x;prefix \"x\";"
	ssb  = "submodule subname {belongs-to name {prefix x;}"
)

// buildErrors: grammar errors with libyang's code, message and line
// (TestOracleErrors compares message and line with yanglint).
var buildErrors = []struct {
	src  string
	line int
	code ly.Code
	msg  string
}{
	// [ly] test_module
	{"module name {}", 1, ly.SyntaxYang, `Missing mandatory keyword "namespace" as a child of "module".`},
	{"module name {namespace urn:name;}", 1, ly.SyntaxYang, `Missing mandatory keyword "prefix" as a child of "module".`},
	{sb11 + "namespace y;namespace z;}", 1, ly.SyntaxYang, `Duplicate keyword "namespace".`},
	{sb11 + "prefix y;}", 1, ly.SyntaxYang, `Duplicate keyword "prefix".`},
	{sb11 + "contact a;contact b;}", 1, ly.SyntaxYang, `Duplicate keyword "contact".`},
	{sb11 + "organization a;organization b;}", 1, ly.SyntaxYang, `Duplicate keyword "organization".`},
	{sb11 + "belongs-to master {prefix m;}}", 1, ly.SyntaxYang, `Invalid keyword "belongs-to" as a child of "module".`},
	{sb11 + "import zzz {prefix x;}}", 1, ly.Reference, `Prefix "x" already used as module prefix.`},
	{sb11 + "import zzz {prefix y;}import zzz {prefix y;}}", 1, ly.Reference, `Prefix "y" already used to import "zzz" module.`},
	{sb10 + "\n\tyang-version 10;}", 2, ly.SyntaxYang, `Invalid value "10" of "yang-version".`},
	{sb10 + "yang-version 1;yang-version 1.1;}", 1, ly.SyntaxYang, `Duplicate keyword "yang-version".`},
	{sb11 + "leaf enum {type enumeration {enum seven { position 7;}}}}", 1, ly.SyntaxYang, `Invalid keyword "position" as a child of "enum".`},
	{sb11 + "must false;}", 1, ly.SyntaxYang, `Invalid keyword "must" as a child of "module".`},
	{"submodule subname {}", 1, ly.SyntaxYang, `Missing mandatory keyword "belongs-to" as a child of "submodule".`},
	{ssb + "belongs-to module1;belongs-to module2;}", 1, ly.SyntaxYang, `Duplicate keyword "belongs-to".`},
	{ssb + "namespace \"urn:z\";}", 1, ly.SyntaxYang, `Invalid keyword "namespace" as a child of "submodule".`},
	{ssb + "prefix m;}", 1, ly.SyntaxYang, `Invalid keyword "prefix" as a child of "submodule".`},
	// [ly] test_deviation, test_deviate
	{sb11 + "deviation test {deviate not-supported; description a; description b;}}", 1, ly.SyntaxYang, `Duplicate keyword "description".`},
	{sb11 + "deviation test {description text;}}", 1, ly.SyntaxYang, `Missing mandatory keyword "deviate" as a child of "deviation".`},
	{sb11 + "deviation test {deviate not-supported; status obsolete;}}", 1, ly.SyntaxYang, `Invalid keyword "status" as a child of "deviation".`},
	{sb11 + "deviation test {deviate add {units km; units mi;}}}", 1, ly.SyntaxYang, `Duplicate keyword "units".`},
	{sb11 + "deviation test {deviate not-supported {units meters;}}}", 1, ly.SyntaxYang, `Deviate "not-supported" does not support keyword "units".`},
	{sb11 + "deviation test {deviate add {type string;}}}", 1, ly.SyntaxYang, `Deviate "add" does not support keyword "type".`},
	{sb11 + "deviation test {deviate delete {config true;}}}", 1, ly.SyntaxYang, `Deviate "delete" does not support keyword "config".`},
	{sb11 + "deviation test {deviate replace {must 1;}}}", 1, ly.SyntaxYang, `Deviate "replace" does not support keyword "must".`},
	{sb11 + "deviation test {deviate replace {default a; default b;}}}", 1, ly.SyntaxYang, `Duplicate keyword "default".`},
	{sb11 + "deviation test {deviate nonsense;}}", 1, ly.SyntaxYang, `Invalid value "nonsense" of "deviate".`},
	// [ly] test_container .. test_augment
	{sb11 + "container cont {when true; when false;}}", 1, ly.SyntaxYang, `Duplicate keyword "when".`},
	{sb11 + "container cont {augment /root;}}", 1, ly.SyntaxYang, `Invalid keyword "augment" as a child of "container".`},
	{sb11 + "container cont {nonsense true;}}", 1, ly.Syntax, `Invalid character sequence "nonsense", expected a keyword.`},
	{sb10 + "container cont {action x;}}", 1, ly.SyntaxYang, `Invalid keyword "action" as a child of "container" - the statement is allowed only in YANG 1.1 modules.`},
	{sb11 + "leaf l {type int8; type uint8;}}", 1, ly.SyntaxYang, `Duplicate keyword "type".`},
	{sb11 + "leaf l {description \"missing type\";}}", 1, ly.SyntaxYang, `Missing mandatory keyword "type" as a child of "leaf".`},
	{sb11 + "leaf-list ll {type string; ordered-by user; ordered-by system;}}", 1, ly.SyntaxYang, `Duplicate keyword "ordered-by".`},
	{sb11 + "leaf-list ll {description \"missing type\";}}", 1, ly.SyntaxYang, `Missing mandatory keyword "type" as a child of "leaf-list".`},
	{sb10 + "leaf-list ll {default xx; type string;}}", 1, ly.SyntaxYang, `Invalid keyword "default" as a child of "leaf-list" - the statement is allowed only in YANG 1.1 modules.`},
	{sb11 + "leaf-list ll {type string; presence x;}}", 1, ly.SyntaxYang, `Invalid keyword "presence" as a child of "llist".`},
	{sb11 + "list l {key one; key two;}}", 1, ly.SyntaxYang, `Duplicate keyword "key".`},
	{sb10 + "list l {action x;}}", 1, ly.SyntaxYang, `Invalid keyword "action" as a child of "list" - the statement is allowed only in YANG 1.1 modules.`},
	{sb11 + "choice ch {default a; default b;}}", 1, ly.SyntaxYang, `Duplicate keyword "default".`},
	{sb11 + "choice ch {case cs {config true;}}}", 1, ly.SyntaxYang, `Invalid keyword "config" as a child of "case".`},
	{sb11 + "anydata any {mandatory true; mandatory false;}}", 1, ly.SyntaxYang, `Duplicate keyword "mandatory".`},
	{sb11 + "grouping grp {config true;}}", 1, ly.SyntaxYang, `Invalid keyword "config" as a child of "grouping".`},
	{sb11 + "grouping grp {must 'expr';}}", 1, ly.SyntaxYang, `Invalid keyword "must" as a child of "grouping".`},
	{sb11 + "rpc func {input {leaf l1 {type empty;}} input {leaf l2 {type empty;}}}}", 1, ly.SyntaxYang, `Duplicate keyword "input".`},
	{sb11 + "rpc func {config true;}}", 1, ly.SyntaxYang, `Invalid keyword "config" as a child of "rpc".`},
	{sb11 + "notification ntf {config true;}}", 1, ly.SyntaxYang, `Invalid keyword "config" as a child of "notification".`},
	{sb11 + "uses grpref {when true; when false;}}", 1, ly.SyntaxYang, `Duplicate keyword "when".`},
	{sb11 + "augment /target/nodeid {status current; status obsolete;}}", 1, ly.SyntaxYang, `Duplicate keyword "status".`},
	{sb11 + "leaf l {type bits {bit b {position -0;}}}}", 1, ly.SyntaxYang, `Invalid value "-0" of "position".`},
	// [ly] test_minmax
	{sb11 + "leaf-list l {type string; min-elements 1invalid;}}", 1, ly.SyntaxYang, `Invalid value "1invalid" of "min-elements".`},
	{sb11 + "leaf-list l {type string; min-elements -1;}}", 1, ly.SyntaxYang, `Invalid value "-1" of "min-elements".`},
	{sb11 + "leaf-list l {type string; min-elements 4294967296;}}", 1, ly.SyntaxYang, `Value "4294967296" is out of "min-elements" bounds.`},
	{sb11 + "leaf-list l {type string; min-elements 1 {config true;}}}", 1, ly.SyntaxYang, `Invalid keyword "config" as a child of "min-elements".`},
	{sb11 + "leaf-list l {type string; max-elements 1invalid;}}", 1, ly.SyntaxYang, `Invalid value "1invalid" of "max-elements".`},
	{sb11 + "leaf-list l {type string; max-elements 4294967296;}}", 1, ly.SyntaxYang, `Value "4294967296" is out of "max-elements" bounds.`},
	// not from libyang's utests; messages from parser_yang.c
	{sb11 + "container c; import a {prefix a;}}", 1, ly.SyntaxYang, `Invalid keyword "import", it cannot appear after "container".`},
	{sb11 + "revision 2020-02-30;}", 1, ly.SyntaxYang, `Invalid value "2020-02-30" of "revision".`},
	{sb11 + "revision 2020-2-3;}", 1, ly.SyntaxYang, `Invalid length 8 of a revision.`},
	{sb11 + "include name;}", 1, ly.Semantics, `Name collision between module and submodule of name "name".`},
	{sb11 + "leaf l {type enumeration {enum a; enum a;}}}", 1, ly.SyntaxYang, `Duplicate identifier "a" of enum statement.`},
	{sb11 + "leaf l {type enumeration {enum \" a\";}}}", 1, ly.SyntaxYang, `Enum name must not have any leading or trailing whitespaces (" a").`},
	{sb11 + "leaf l {type enumeration {enum a {value 2147483648;}}}}", 1, ly.SyntaxYang, `Invalid value "2147483648" of "value".`},
	{sb11 + "leaf l {type decimal64 {fraction-digits 19;}}}", 1, ly.SyntaxYang, `Value "19" is out of "fraction-digits" bounds.`},
	{sb10 + "identity i {base a; base b;}}", 1, ly.SyntaxYang, `Identity can be derived from multiple base identities only in YANG 1.1 modules`},
	{sb11 + "x:e;}", 0, ly.Reference, `Extension definition of extension instance "x:e" not found.`},
	{sb11 + "leaf l {type string; zz:e;}}", 0, ly.Reference, `Invalid prefix "zz" used for extension instance identifier.`},
	// libyang's order and positions (review of the first version, which tokenized first)
	{sb10 + "augment /a { notification n; }\n  leaf", 1, ly.SyntaxYang, `Invalid keyword "notification" as a child of "augment" - the statement is allowed only in YANG 1.1 modules.`},
	{hdr + "  leaf l { type string; mandatory\n\n    maybe; }\n}", 6, ly.SyntaxYang, `Invalid value "maybe" of "mandatory".`},
	{hdr + "  leaf l {\n  }\n}", 5, ly.SyntaxYang, `Missing mandatory keyword "type" as a child of "leaf".`},
	{hdr + "  import i { prefix m;\n  }\n}", 4, ly.Reference, `Prefix "m" already used as module prefix.`},
	{sb10 + "import i {prefix i; description d;} yang-version 1.1;}", 1, ly.SyntaxYang, `Invalid keyword "description" as a child of "import" - the statement is allowed only in YANG 1.1 modules.`},
	{sb11 + "leaf l {type string; a:x; b:y {c:z;}}}", 0, ly.Reference, `Invalid prefix "c" used for extension instance identifier.`},
	{sb11 + "leaf l {type enumeration {enum a {value -+5;}}}}", 1, ly.SyntaxYang, `Invalid value "-+5" of "value".`},
	// lysp_ext_instance_resolve_argument (x1, x3, r20 of the review)
	{sb11 + "extension e {argument a;} x:e;}", 0, ly.Semantics, `Extension instance "x:e" missing argument "a".`},
	{sb11 + "extension e {argument a {yin-element true;}} x:e;}", 0, ly.Semantics, `Extension instance "x:e" missing argument element "a".`},
	{sb11 + "extension e {argument a;} leaf l {type string; x:e v {x:e;}}}", 0, ly.Semantics, `Extension instance "x:e" missing argument "a".`},
}

// iffErrors: Build keeps these modules, the if-feature carries libyang's compile error.
var iffErrors = []struct {
	src  string
	code ly.Code
	msg  string
}{
	{sb10 + "leaf l {type string; if-feature \"a and b\";}}", ly.SyntaxYang, `Invalid value "a and b" of if-feature - YANG 1.1 expression in YANG 1.0 module.`},
	{sb11 + "leaf l {type string; if-feature \"a and\";}}", ly.SyntaxYang, `Invalid value "a and" of if-feature - unexpected end of expression.`},
	{sb11 + "leaf l {type string; if-feature \"a b\";}}", ly.SyntaxYang, `Invalid value "a b" of if-feature - number of features in expression does not match the required number of operands for the operations.`},
	{sb11 + "leaf l {type string; if-feature \"(a or b\";}}", ly.SyntaxYang, `Invalid value "(a or b" of if-feature - non-matching opening and closing parentheses.`},
	{sb11 + "leaf l {type string; if-feature \"or a\";}}", ly.SyntaxYang, `Invalid value "or a" of if-feature - missing feature/expression before "or" operation.`},
	{sb11 + "feature a; leaf l {type string; if-feature \"not (not a)\";}}", ly.SyntaxYang, `Invalid value "not (not a)" of if-feature - processing error.`}, // D-0021
	{sb11 + "leaf l {type string; if-feature \"\";}}", ly.SyntaxYang, `Invalid value "" of if-feature - number of features in expression does not match the required number of operands for the operations.`},
}

func TestIfFeatureErrors(t *testing.T) {
	for _, c := range iffErrors {
		m, err := parseBuild(c.src)
		if err != nil {
			t.Errorf("%s: %v", c.src, err)
			continue
		}
		if e := m.Children[0].IfFeatures[0].Err; e == nil || e.Msg != c.msg || e.Code != c.code {
			t.Errorf("%s\n got %v\nwant %s", c.src, e, c.msg)
		}
	}
}

func TestBuildErrors(t *testing.T) {
	for _, c := range buildErrors {
		_, err := parseBuild(c.src)
		var e *Error
		if !errors.As(err, &e) || e.Msg != c.msg || e.Code != c.code || e.Pos.Line != c.line {
			t.Errorf("%q\n got %v\nwant %d: %s (%v)", c.src, err, c.line, c.msg, c.code)
		}
	}
}

func TestBuildValid(t *testing.T) {
	for _, src := range []string{
		sb11 + "import zzz {prefix y;}import zzz {prefix z;}}", // [ly]
		sb11 + "import i {prefix y;} y:e; leaf l {type string; y:e {y:f z; x:e;}} extension e;}",
		sb10 + "yang-version 1;}",
		sb11 + "leaf l {type enumeration {enum a {value \" -5\";}}}}",
		ssb + "x:anything;}",
		sb11 + "x:e v {leaf 1abc;} extension e {argument a {yin-element true;}}}",
		sb11 + "import i {prefix i;} i:e; extension f {argument a;} x:f \"\";}", // imported definition: the compiler's
		sb11 + "leaf l {type string; if-feature \"not (a or b:c) and d\";}}",
		sb11 + "deviation /x {deviate replace {type string; default a;}}}",
	} {
		if _, err := parseBuild(src); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
}

func TestBuildTyped(t *testing.T) {
	src := `module t {
  yang-version 1.1;
  namespace "urn:t";
  prefix t;
  import ietf-inet-types { prefix inet; revision-date 2013-07-15; }
  revision 2024-01-02 { description "first"; }
  feature f;
  identity base1;
  identity id { base base1; }
  typedef pct { type uint8 { range "0..100" { error-app-tag too-big; } } units percent; }
  grouping g { leaf x { type string { length 1..10; pattern "[a-z]*" { modifier invert-match; } } } }
  container top {
    presence "p";
    when "../a" { description d; }
    must "count(x) > 0" { error-message "msg"; }
    uses g { refine x { default "abc"; } augment "x2" { leaf y { type empty; } } }
    list l {
      key "k";
      unique "a b";
      min-elements 1;
      max-elements unbounded;
      ordered-by user;
      leaf k { type union { type int8; type leafref { path "../a"; require-instance false; } } }
      action reset { input { leaf at { type string; } } output { leaf ok { type boolean; } } }
    }
    choice ch { default c1; case c1 { leaf a { type decimal64 { fraction-digits 2; } } } leaf b { type bits { bit x { position 3; } } } }
    leaf-list ll { type t:pct; default 1; default 2; if-feature "f or not f"; config false; }
  }
  rpc r;
  notification n { anydata d; }
  augment "/top" { leaf z { type identityref { base base1; } mandatory true; } }
}`
	m, err := parseBuild(src)
	if err != nil {
		t.Fatal(err)
	}
	top := m.Children[0]
	l := top.Children[1]
	ch := top.Children[2]
	ll := top.Children[3]
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"header", m.Name == "t" && m.Version == "1.1" && m.Namespace == "urn:t" && m.Prefix == "t" && m.Kind == "module"},
		{"import", len(m.Imports) == 1 && m.Imports[0].Prefix == "inet" && m.Imports[0].RevisionDate == "2013-07-15"},
		{"revision", m.Revisions[0].Date == "2024-01-02" && m.Revisions[0].Description == "first"},
		{"identity", len(m.Identities) == 2 && m.Identities[1].Bases[0] == "base1" && len(m.Features) == 1},
		{"typedef", m.Typedefs[0].Type.Range.Arg == "0..100" && m.Typedefs[0].Type.Range.ErrorAppTag == "too-big" && *m.Typedefs[0].Units == "percent"},
		{"pattern", m.Groupings[0].Children[0].Type.Patterns[0].Invert && m.Groupings[0].Children[0].Type.Length.Arg == "1..10"},
		{"container", *top.Presence == "p" && top.When.Arg == "../a" && top.When.Description == "d" && top.Musts[0].ErrorMessage == "msg"},
		{"uses", top.Children[0].Kind == "uses" && top.Children[0].Refines[0].Defaults[0] == "abc" && top.Children[0].Augments[0].Children[0].Name == "y"},
		{"list", *l.Key == "k" && l.Uniques[0] == "a b" && *l.MinElements == 1 && *l.MaxElements == 0 && l.OrderedBy == "user"},
		{"union", len(l.Children[0].Type.Types) == 2 && l.Children[0].Type.Types[1].Path == "../a" && !*l.Children[0].Type.Types[1].RequireInstance},
		{"action", l.Actions[0].Input.Children[0].Name == "at" && l.Actions[0].Output.Children[0].Name == "ok"},
		{"choice", ch.Defaults[0] == "c1" && ch.Children[0].Kind == "case" && ch.Children[0].Children[0].Type.FractionDigits == 2},
		{"bits", *ch.Children[1].Type.Bits[0].Value == 3},
		{"leaf-list", len(ll.Defaults) == 2 && !*ll.Config && ll.IfFeatures[0].AST.Op == IffOr && ll.IfFeatures[0].AST.Y.Op == IffNot},
		{"rpc", m.Actions[0].Kind == "rpc" && m.Notifications[0].Children[0].Kind == "anydata"},
		{"augment", m.Augments[0].Name == "/top" && *m.Augments[0].Children[0].Mandatory && m.Augments[0].Children[0].Type.Bases[0] == "base1"},
	} {
		if !c.ok {
			t.Errorf("%s: wrong", c.name)
		}
	}
}

func TestIffEval(t *testing.T) {
	on := func(n string) bool { return n == "a" || n == "c" }
	for in, want := range map[string]bool{"a": true, "b": false, "not b": true, "a and b": false, "a and c": true,
		"b or c": true, "not (a or b) or b": false, "a and not b and c": true,
		strings.Repeat("(", 1<<20) + "a" + strings.Repeat(")", 1<<20): true,
		strings.Repeat("b or ", 1<<18) + "a":                          true} {
		e, why := parseIfFeature(in, true)
		if why != "" || e.Eval(on) != want {
			t.Errorf("%.40q: %v %s, want %v", in, e != nil && e.Eval(on), why, want)
		}
	}
}

func TestParseIfFeature(t *testing.T) {
	str := func(e *IffExpr) string {
		var f func(e *IffExpr) string
		f = func(e *IffExpr) string {
			switch e.Op {
			case IffNot:
				return "!" + f(e.X)
			case IffAnd:
				return "(" + f(e.X) + "&" + f(e.Y) + ")"
			case IffOr:
				return "(" + f(e.X) + "|" + f(e.Y) + ")"
			}
			return e.Name
		}
		return f(e)
	}
	for _, c := range []struct{ in, want string }{
		{"a", "a"},
		{"p:a", "p:a"},
		{"a or b and c", "(a|(b&c))"},
		{"(a or b) and c", "((a|b)&c)"},
		{"not not a", "a"}, // libyang drops a double not
		{"( a )", "a"},
		{"android or notify", "(android|notify)"},
		{"a b", "ERR"},
		{"a or", "ERR"},
		{"or a", "ERR"},
		{"(a", "ERR"},
		{"a)", "ERR"},
		{"", "ERR"},
		{"not(a)", "ERR"},
		{"a)(", "ERR"}, // crashes yanglint 5.8.6 (D-0020)
		{strings.Repeat("(", 1<<20) + "a" + strings.Repeat(")", 1<<20), "a"},
	} {
		e, why := parseIfFeature(c.in, true)
		got := "ERR"
		if why == "" {
			got = str(e)
		}
		if got != c.want {
			t.Errorf("%q: got %s (%v), want %s", c.in, got, why, c.want)
		}
	}
}
