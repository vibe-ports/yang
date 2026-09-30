// SPDX-License-Identifier: BSD-3-Clause

package main

import "testing"

// A miniature of a libyang utest: a macro that concatenates strings, a macro wrapping
// CHECK_PARSE_LYD_PARAM, a string variable, and a CHECK_LOG_CTX attached to the failing case.
const snippet = `
#define MOD(N, BODY) "module " N " {namespace \"urn:t:" N "\"; prefix p;" BODY "}"
#define BAD(V) { struct lyd_node *t; const char *d = "<x xmlns=\"urn:t:m\">" V "</x>"; \
    CHECK_PARSE_LYD_PARAM(d, LYD_XML, 0, LYD_VALIDATE_PRESENT, LY_EVALID, t); }
static void
test_a(void **state)
{
    const char *schema;
    schema = MOD("m", "leaf x {type int8;}");
    UTEST_ADD_MODULE(schema, LYS_IN_YANG, NULL, NULL);
    BAD("300");
    CHECK_LOG_CTX("Value \"300\" is out of int8's min/max bounds.", "/m:x", 1);
    UTEST_ADD_MODULE(schema, LYS_IN_YIN, NULL, NULL);
}
`

func TestExtract(t *testing.T) {
	toks, defs := splitDefines(snippet)
	ip := &interp{defs: defs, vars: map[string]string{}, file: "t.c", skipped: map[string]int{}, unextracted: map[string]int{}}
	ip.run(toks)
	if len(ip.cases) != 2 {
		t.Fatalf("want 2 cases (schema, data), got %d", len(ip.cases))
	}
	s, d := ip.cases[0], ip.cases[1]
	if s.Kind != "schema" || s.Verdict != "valid" || s.texts["m.yang"] != `module m {namespace "urn:t:m"; prefix p;leaf x {type int8;}}` {
		t.Errorf("schema case wrong: %+v", s)
	}
	if d.Kind != "data" || d.Verdict != "invalid" || d.data != `<x xmlns="urn:t:m">300</x>` || len(d.Log) != 1 || d.Log[0].Path != "/m:x" {
		t.Errorf("data case wrong: %+v", d)
	}
	if d.Source.Line != 11 { // call site of BAD(), not the #define line
		t.Errorf("line = %d, want call site 11", d.Source.Line)
	}
	if ip.skipped["schema format LYS_IN_YIN (YIN: out of v1 scope)"] != 1 {
		t.Errorf("YIN not skipped: %v", ip.skipped)
	}
}
