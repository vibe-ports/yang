// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixture is a tiny C tree: entry() calls a ported function, an unported one through a macro, an
// out-of-scope one, and a helper only reachable through that out-of-scope function. It names
// more functions only in a string, a char-literal brace, a // comment and a multi-line block
// comment (with an apostrophe): none of them may be reached. one_line() is a whole function on
// one line.
var fixture = map[string]string{
	"src/a.c": `#include "a.h"

int
ported(int x)
{
    return missing_deep(x);
}

int
missing_deep(int x)
{
    return x;
}

int
entry(void)
{
    LY_CHECK_RET(ported(1));
    CALL_HIDDEN();
    yin_thing();
    log_it("in_string() { }", '{');
    // in_line_comment();
    /* it's a
     * multi-line comment naming in_block_comment()
     */
    one_line();
    return 0;
}

int one_line(void) { return after_one_line(); }

int
after_one_line(void)
{
    return 0;
}

void
in_string(void)
{
}

void
in_line_comment(void)
{
}

void
in_block_comment(void)
{
}

void
log_it(const char *s, char c)
{
}

static void
unreached(void)
{
}
`,
	"src/a.h": `#define LY_CHECK_RET(x) do { if (x) return x; } while (0)
#define CALL_HIDDEN() hidden_by_macro()
`,
	"src/b.c": `int
hidden_by_macro(void)
{
    return 0;
}
`,
	"src/parser_yin.c": `void
yin_thing(void)
{
    behind_yin();
}

void
behind_yin(void)
{
}
`,
}

const fixtureConf = `root entry
skip src/parser_yin.c YIN out of scope
`

const fixturePortMap = "| file | C function | Go |\n|---|---|---|\n| src/a.c | ported, `other` (note naming missing_deep (nested)) | x |\n"

// TestPortCoverageFixture builds the tags file with scripts/lyfn over the fixture tree and
// checks the walk: macros followed, out-of-scope files neither counted nor followed.
func TestPortCoverageFixture(t *testing.T) {
	for _, tool := range []string{"bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	dir := t.TempDir()
	for name, body := range fixture {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"-n", "entry"}} {
		cmd := exec.Command("../../../scripts/lyfn", args...) //nolint:gosec // test
		cmd.Env = append(os.Environ(), "LYFN_SRC="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			if strings.Contains(string(out), "universal-ctags not found") {
				t.Skip("universal-ctags not installed")
			}
			t.Fatalf("lyfn %v: %v\n%s", args, err, out)
		}
	}
	conf := filepath.Join(dir, "conf")
	pm := filepath.Join(dir, "port-map.md")
	if err := os.WriteFile(conf, []byte(fixtureConf), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pm, []byte(fixturePortMap), 0o600); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := run(&b, dir, pm, conf); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"reachable from the pilot entry points: **7**; listed in docs/port-map.md: **1**; not listed: **6**. Out of scope (not followed): 1.",
		"| YIN out of scope | 1 |",
		"| entry | src/a.c:16 | entry | 0 |",
		"| hidden_by_macro | src/b.c:2 | entry | 1 |",
		"| log_it | src/a.c:",
		"| one_line | src/a.c:30 | entry | 1 |",
		"| after_one_line | src/a.c:33 | entry | 2 |",
		"| missing_deep | src/a.c:10 | entry | 2 |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	for _, not := range []string{"behind_yin", "unreached", "| ported |", "in_string", "in_line_comment", "in_block_comment"} {
		if strings.Contains(out, not) {
			t.Errorf("%s should not be listed:\n%s", not, out)
		}
	}
}

func TestParsers(t *testing.T) {
	cfg, err := parseConfig(strings.NewReader(fixtureConf + "# comment\n\n"))
	if err != nil || !reflect.DeepEqual(cfg.Roots, []string{"entry"}) || cfg.Skip[0].reason != "YIN out of scope" {
		t.Fatalf("%+v %v", cfg, err)
	}
	if _, err := parseConfig(strings.NewReader("skip x\n")); err == nil {
		t.Error("a skip without a reason is accepted")
	}
	pm, err := readPortMap(strings.NewReader(fixturePortMap))
	if err != nil || !pm["ported"] || !pm["other"] || pm["Go"] || pm["missing_deep"] || pm["note"] || pm["nested"] {
		t.Fatalf("%v %v", pm, err)
	}
}

// TestStrip: literals and comments become blanks, newlines stay, a block comment spans lines and
// an apostrophe inside it is no char literal.
func TestStrip(t *testing.T) {
	in := "a(\"x(\\\"y\");'{' /* it's\n b() */ c(); // d()\ne('\\'', \"\\\\\", f());"
	got := strip(in)
	if len(got) != len(in) || strings.Count(got, "\n") != 2 {
		t.Fatalf("length or lines changed: %q", got)
	}
	for _, gone := range []string{"x(", "y", "{", "it", "b(", "d(", "/", "*"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q left in %q", gone, got)
		}
	}
	for _, kept := range []string{"a(", " c();", "e(", "f());"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q lost in %q", kept, got)
		}
	}
}

// TestBraceEnd: the closing line of a body, braces in literals ignored (callers pass stripped
// lines), a prototype ends at its ';', and a body that never closes ends at its first line.
func TestBraceEnd(t *testing.T) {
	src := strings.Split(strip(`int
f(void)
{
    char *s = "}";
    if (x) { y('}'); }
}
int g(void);
int h(void) { return 0; }
int
open(void)
{
`), "\n")
	for _, c := range []struct{ first, want int }{
		{1, 6}, // f
		{7, 7}, // prototype
		{8, 8}, // one-line body
		{9, 9}, // never closed
		{0, 0}, // before the file
		{99, 99},
	} {
		if got := braceEnd(src, c.first); got != c.want {
			t.Errorf("braceEnd(%d) = %d, want %d", c.first, got, c.want)
		}
	}
}

// TestFixEnds: ends ctags left missing or before the start are recomputed, macros keep theirs.
func TestFixEnds(t *testing.T) {
	sc := &source{files: map[string][]string{"a.c": strings.Split(strip("int\nf(void)\n{\n  /* } */\n}\n"), "\n")}}
	defs := map[string][]def{
		"f": {{name: "f", file: "a.c", first: 1, end: 0}},
		"g": {{name: "g", file: "a.c", first: 2, end: 1}},
		"m": {{name: "m", file: "a.c", first: 1, end: 0, macro: true}},
	}
	fixEnds(sc, defs)
	if defs["f"][0].end != 5 || defs["g"][0].end != 5 || defs["m"][0].end != 0 {
		t.Fatalf("%+v", defs)
	}
}

// TestCallsMacroOwnName: a function-like macro does not call itself, an object-like macro's body
// is a call.
func TestCallsMacroOwnName(t *testing.T) {
	sc := &source{files: map[string][]string{"a.h": strings.Split("#define F(x) bar(x)\n#define X baz(1)\n", "\n")}}
	if got := sc.calls(def{name: "F", file: "a.h", first: 1, macro: true}); !reflect.DeepEqual(got, []string{"bar"}) {
		t.Errorf("F calls %v", got)
	}
	if got := sc.calls(def{name: "X", file: "a.h", first: 2, macro: true}); !reflect.DeepEqual(got, []string{"baz"}) {
		t.Errorf("X calls %v", got)
	}
}

// TestUnmatchedRoot: a root glob matching no definition is reported.
func TestUnmatchedRoot(t *testing.T) {
	cfg := &Config{Roots: []string{"entry", "nothing_*"}}
	rep, err := walk(cfg, map[string][]def{"entry": {{name: "entry", file: "a.c", first: 1, end: 1}}}, nil,
		func(def) []string { return nil })
	if err != nil || !reflect.DeepEqual(rep.UnmatchedRoots, []string{"nothing_*"}) {
		t.Fatalf("%+v %v", rep, err)
	}
}
