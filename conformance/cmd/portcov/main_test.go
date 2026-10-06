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
// out-of-scope one, and a helper only reachable through that out-of-scope function.
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
    return 0;
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

const fixturePortMap = "| file | C function | Go |\n|---|---|---|\n| src/a.c | ported, `other` (note) | x |\n"

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
		"reachable from the pilot entry points: **4**; listed in docs/port-map.md: **1**; not listed: **3**. Out of scope (not followed): 1.",
		"| YIN out of scope | 1 |",
		"| entry | src/a.c:16 | entry | 0 |",
		"| hidden_by_macro | src/b.c:2 | entry | 1 |",
		"| missing_deep | src/a.c:10 | entry | 2 |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	for _, not := range []string{"behind_yin", "unreached", "| ported |"} {
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
	if err != nil || !pm["ported"] || !pm["other"] || pm["Go"] {
		t.Fatalf("%v %v", pm, err)
	}
}
