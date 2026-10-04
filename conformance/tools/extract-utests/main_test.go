// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// extract runs the interpreter over the body of one test function (plus optional preamble).
func extract(pre, body string) *interp {
	src := pre + "\nstatic void\ntest_a(void **state)\n{\n" + body + "\n}\n"
	toks, defs, _ := splitDefines(src)
	ip := &interp{defs: defs, vars: map[string]string{}, file: "t.c", skipped: map[string]int{}, unextracted: map[string]int{}, tag: libyangTag}
	ip.run(toks)
	return ip
}

const mod = `UTEST_ADD_MODULE("module m {namespace urn:m; prefix m; leaf x {type int8;}}", LYS_IN_YANG, NULL, NULL);`

// Finding 2: C escapes.
func TestEscapes(t *testing.T) {
	tests := []struct {
		lit  string
		want string
		ok   bool
	}{
		{`"a\nb\t\"q\"\\"`, "a\nb\t\"q\"\\", true},
		{`"\a\b\f\v\?"`, "\a\b\f\v?", true},
		{`"\x41"`, "A", true},
		{`"\101"`, "A", true},
		{`"\x4142"`, "", false}, // over-long hex escape
		{`"\x"`, "", false},     // no digits
		{`"\0"`, "", false},     // NUL
		{`"\x00"`, "", false},
		{`"\u00e9"`, "", false}, // unsupported
		{`"\q"`, "", false},     // unknown escape
		{`"a" "b"`, "ab", true},
	}
	for _, tc := range tests {
		ip := &interp{defs: map[string]*def{}, vars: map[string]string{}}
		got, ok := ip.eval(lex(tc.lit, 1))
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", tc.lit, got, ok, tc.want, tc.ok)
		}
	}
}

// Finding 3: variables never keep a stale value.
func TestStaleVars(t *testing.T) {
	data := func(v string) string { return `CHECK_PARSE_LYD_PARAM(` + v + `, LYD_XML, 0, 0, LY_SUCCESS, tree);` }
	tests := []struct {
		name, body string
		cases      int // expected data cases
	}{
		{"fresh", mod + `s = "<x xmlns=\"urn:m\">1</x>";` + data("s"), 2},
		{"failed reassign deletes", mod + `s = "<x xmlns=\"urn:m\">1</x>"; s = unknown_fn(); ` + data("s"), 1},
		{"+= deletes", mod + `s = "<x xmlns=\"urn:m\">1</x>"; s += "tail"; ` + data("s"), 1},
		{"assignment in branch ignored", mod + `s = "<x xmlns=\"urn:m\">1</x>"; if (c) { s = "<y/>"; } ` + data("s"), 1},
	}
	for _, tc := range tests {
		ip := extract("", tc.body)
		n := 0
		for _, c := range ip.cases {
			if c.Kind == "data" {
				n++
			}
		}
		// "fresh": 1 data case; every other row must emit none.
		want := 0
		if tc.cases == 2 {
			want = 1
		}
		if n != want {
			t.Errorf("%s: %d data cases, want %d (skipped %v)", tc.name, n, want, ip.skipped)
		}
	}
}

// Finding 4: a rejected module must restore the previous context exactly.
func TestFailedLoadRestores(t *testing.T) {
	ip := extract("", mod+`
UTEST_ADD_MODULE("module n {namespace urn:n; prefix n;}", LYS_IN_YANG, NULL, NULL);
UTEST_INVALID_MODULE("module m {BROKEN}", LYS_IN_YANG, NULL, LY_EVALID);
CHECK_PARSE_LYD_PARAM("<x xmlns=\"urn:m\">1</x>", LYD_XML, 0, 0, LY_SUCCESS, tree);`)
	last := ip.cases[len(ip.cases)-1]
	if last.Kind != "data" || len(last.Modules) != 2 || last.Modules[0] != "m" || last.Modules[1] != "n" ||
		!strings.Contains(last.texts["m.yang"], "leaf x") {
		t.Errorf("context not restored: %+v texts=%v", last.Modules, last.texts)
	}
}

// Findings 5-7 and the skip paths.
func TestSkips(t *testing.T) {
	tests := []struct {
		name, pre, body string
		wantCases       int
		skip            string
	}{
		{"YIN taints the rest", "", `UTEST_ADD_MODULE("<module/>", LYS_IN_YIN, NULL, NULL);
CHECK_PARSE_LYD_PARAM("<x/>", LYD_XML, 0, 0, LY_SUCCESS, tree);`, 0, "context incomplete"},
		{"ctx options taint", "", mod + `ly_ctx_set_options(ctx, 1);
CHECK_PARSE_LYD_PARAM("<x xmlns=\"urn:m\"/>", LYD_XML, 0, 0, LY_SUCCESS, tree);`, 1, "context incomplete"},
		{"macro arity", `#define M(A, B) CHECK_PARSE_LYD_PARAM(A B, LYD_XML, 0, 0, LY_SUCCESS, tree)`,
			mod + `M("<x/>");`, 1, "arity"},
		{"#if poisons define", "#ifdef FOO\n#define V \"a\"\n#else\n#define V \"b\"\n#endif",
			mod + `CHECK_PARSE_LYD_PARAM(V, LYD_XML, 0, 0, LY_SUCCESS, tree);`, 1, "not resolvable"},
		{"control flow", "", `if (x) { ` + mod + ` }`, 0, "control flow"},
		{"submodule", "", `UTEST_ADD_MODULE("submodule s {belongs-to m {prefix m;}}", LYS_IN_YANG, NULL, NULL);`, 0, "submodule"},
		{"eexist", "", `UTEST_INVALID_MODULE("module m {}", LYS_IN_YANG, NULL, LY_EEXIST);`, 0, "LY_EEXIST"},
		{"lyb data", "", mod + `CHECK_PARSE_LYD_PARAM("x", LYD_LYB, 0, 0, LY_SUCCESS, tree);`, 1, "not xml/json"},
	}
	for _, tc := range tests {
		ip := extract(tc.pre, tc.body)
		if len(ip.cases) != tc.wantCases {
			t.Errorf("%s: %d cases, want %d (skipped %v)", tc.name, len(ip.cases), tc.wantCases, ip.skipped)
		}
		found := false
		for k := range ip.skipped {
			found = found || strings.Contains(k, tc.skip)
		}
		if !found {
			t.Errorf("%s: no skip reason containing %q in %v", tc.name, tc.skip, ip.skipped)
		}
	}
}

// Finding 5: semantics the request cannot carry are flagged, never dropped.
func TestNeedsExtension(t *testing.T) {
	ip := extract("", `UTEST_ADD_MODULE("module m {namespace urn:m; prefix m;}", LYS_IN_YANG, features, NULL);
CHECK_PARSE_LYD_PARAM("<x xmlns=\"urn:m\"/>", LYD_XML, LYD_PARSE_STRICT | LYD_PARSE_WEIRD, LYD_VALIDATE_PRESENT | LYD_VALIDATE_NO_STATE, LY_SUCCESS, tree);`)
	out := t.TempDir()
	for _, c := range ip.cases {
		if err := c.write(out); err != nil {
			t.Fatal(err)
		}
	}
	if c := ip.cases[0]; len(c.NeedsExtension) != 1 || c.Request != nil {
		t.Errorf("features must need extension: %+v", c.NeedsExtension)
	}
	d := ip.cases[1]
	if len(d.NeedsExtension) != 1 || d.NeedsExtension[0] != "parse_option=weird" {
		t.Errorf("unknown parse option must need extension: %+v", d.NeedsExtension)
	}
	d.Parse = "LYD_PARSE_STRICT"
	req, need := d.buildRequest()
	vo, _ := req["validate_options"].([]string)
	if len(need) != 0 || req["unknown"] != "reject" || len(vo) != 2 {
		t.Errorf("request = %v need = %v", req, need)
	}
}

// Finding 1: verify distinguishes exit codes from a tool that cannot run.
func TestVerify(t *testing.T) {
	old := yanglint
	defer func() { yanglint = old }()
	mk := func() *Case {
		return &Case{ID: "x", Kind: "schema", Verdict: "valid", Files: []string{"m.yang"}}
	}
	for _, tc := range []struct {
		code int
		err  error
		want string
		fail bool
	}{
		{0, nil, "agree", false},
		{1, nil, "differ (yanglint invalid)", false},
		{-1, nil, "n/a (yanglint exit status -1)", false},
		{2, nil, "n/a (yanglint exit status 2)", false},
		{0, errors.New("exec: not found"), "", true},
	} {
		yanglint = func(...string) (int, error) { return tc.code, tc.err }
		c := mk()
		err := c.verify(t.TempDir())
		if (err != nil) != tc.fail || c.Verified != tc.want {
			t.Errorf("code=%d err=%v: verified=%q err=%v", tc.code, tc.err, c.Verified, err)
		}
	}
}

func TestInsideRepo(t *testing.T) {
	if insideRepo(t.TempDir()) {
		t.Error("temp dir reported inside a repo")
	}
	if !insideRepo(".") {
		t.Error("tool dir not reported inside a repo")
	}
}

// Golden: whole pipeline on a mini utest, compared byte for byte.
func TestGoldenCaseJSON(t *testing.T) {
	ip := extract(`#define BAD(V) CHECK_PARSE_LYD_PARAM("<x xmlns=\"urn:m\">" V "</x>", LYD_XML, 0, LYD_VALIDATE_PRESENT, LY_EVALID, t)`,
		mod+`
BAD("300");
CHECK_LOG_CTX("Value \"300\" is out of int8's min/max bounds.", "/m:x", 1);`)
	out := t.TempDir()
	for _, c := range ip.cases {
		if err := c.write(out); err != nil {
			t.Fatal(err)
		}
	}
	if len(ip.cases) != 2 {
		t.Fatalf("%d cases", len(ip.cases))
	}
	got, err := os.ReadFile(filepath.Join(out, "t/test_a/002/case.json"))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{
  "id": "t/test_a/002",
  "kind": "data",
  "source": {
    "file": "t.c",
    "func": "test_a",
    "line": 6,
    "libyang_tag": "v5.8.6",
    "license": "BSD-3-Clause",
    "copyright": "CESNET"
  },
  "modules": [
    "m"
  ],
  "files": [
    "m.yang"
  ],
  "format": "xml",
  "data_file": "data.xml",
  "parse_options": "0",
  "validate_options": "LYD_VALIDATE_PRESENT",
  "asserted_ret": "LY_EVALID",
  "asserted_verdict": "invalid",
  "asserted_log": [
    {
      "msg": "Value \"300\" is out of int8's min/max bounds.",
      "path": "/m:x",
      "line": 1
    }
  ],
  "oracle_request": {
    "data_file": "data.xml",
    "data_type": "data",
    "format": "xml",
    "modules": [
      {
        "name": "m"
      }
    ],
    "op": "data",
    "parse_only": false,
    "parse_options": [],
    "searchdirs": [
      "."
    ],
    "unknown": "skip",
    "validate_options": [
      "present"
    ]
  }
}
`
	if string(got) != want {
		t.Errorf("case.json mismatch:\n%s", got)
	}
}
