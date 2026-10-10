// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"regexp"
	"strings"
	"testing"
)

// agreeFloor is the number of fixtures that agree (with or without skipped fields) on main; a
// change that lowers it is a regression.
const agreeFloor = 2323

// TestYangEngineSchema runs every fixture through package yang and logs the tally. No fixture may
// differ: a disagreement is either fixed or recorded as a deviation (deviations.md) or as
// unsupported, never absorbed by new agreements under the floor. M2 exit gate (#43): an op schema
// fixture may be unsupported only for a limit in schemaLimits.
func TestYangEngineSchema(t *testing.T) {
	m := load(t)
	rep, err := m.Compare(Yang{})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[Status]int{}
	for _, r := range rep.Results {
		counts[r.Status]++
	}
	t.Logf("agree %d, agree (skipped fields) %d, differ %d, deviation %d, unsupported %d", counts[Agree],
		counts[AgreeSkipped], counts[Differ], counts[Deviation], counts[Unsupported])
	for _, r := range rep.Results {
		if r.Status == Differ {
			t.Errorf("differ %s: %s", r.ID, r.Detail)
		}
	}
	if n := counts[Agree] + counts[AgreeSkipped]; n < agreeFloor {
		t.Errorf("%d fixtures agree, fewer than %d", n, agreeFloor)
	}
	for i, r := range rep.Results {
		if op, _ := m.Fixtures[i].Request["op"].(string); op == "schema" && r.Status == Unsupported && schemaLimit(r.Detail) == "" {
			t.Errorf("unsupported schema fixture %s outside the v1 limits: %s", r.ID, r.Detail)
		}
	}
}

// TestYangEngineXPath is the M3 exit gate (#91): no op xpath or op atoms fixture, nor a
// sequence with a trim step (lyd_trim_xpath), may differ, and none may be unsupported unless
// xpathUnsupported lists it with the deviations.md entry its reason names.
func TestYangEngineXPath(t *testing.T) {
	m := load(t)
	rep, err := m.Compare(Yang{})
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for i, r := range rep.Results {
		kind := xpathKind(m.Fixtures[i].Request)
		if kind == "" {
			continue
		}
		n[kind]++
		switch {
		case r.Status == Differ:
			t.Errorf("differ %s: %s", r.ID, r.Detail)
		case r.Status == Unsupported && !xpathAllowed(r.ID, r.Detail):
			t.Errorf("unsupported xpath fixture %s without a listed deviation: %s", r.ID, r.Detail)
		}
	}
	t.Logf("gated fixtures: %v", n)
	for kind, floor := range xpathFloors {
		if n[kind] < floor {
			t.Errorf("%d %s fixtures gated, fewer than %d: the selection lost fixtures", n[kind], kind, floor)
		}
	}
}

// xpathFloors are the gated fixtures of each kind at the M3 exit; fewer means the selection or
// the corpus lost some, which would let the gate pass on nothing.
var xpathFloors = map[string]int{"xpath": 268, "atoms": 44, "trim": 30}

// xpathKind is the M3 gate kind of a request: "xpath", "atoms", "trim" (a sequence with a trim
// step) or "" for a fixture outside the gate.
func xpathKind(req map[string]any) string {
	switch op, _ := req["op"].(string); op {
	case "xpath", "atoms":
		return op
	case "sequence":
		for _, st := range list(req["steps"]) {
			if s, ok := st.(map[string]any); ok && s["do"] == "trim" {
				return "trim"
			}
		}
	}
	return ""
}

// xpathUnsupported are the xpath fixtures that may stay unsupported after M3, each with the
// deviations.md id its reason must name.
var xpathUnsupported = map[string]string{
	"ut-xpath/anydata-01": "U-0043", // anydata/anyxml data instances (M5)
}

// xpathAllowed reports whether fixture id may be unsupported with this reason.
func xpathAllowed(id, reason string) bool {
	u, ok := xpathUnsupported[id]
	return ok && strings.Contains(reason, u)
}

// schemaLimits are the reasons an op schema fixture may be unsupported after M2 (issue #43): the
// formats and extensions outside v1 and the resource budgets (conformance/deviations.md).
var schemaLimits = []struct{ id, re string }{
	{"U-0021", `\(U-0021\)`}, // YIN module file
	{"U-0022", `directories searched for module`},
	{"U-0024", `extension instance .* \(U-0024\)`}, // schema mount
	{"U-0025", `extension instance .* \(U-0025\)`}, // openconfig
	{"U-0030", `compiled types and union members|more than \d+ union members`},
	{"U-0034", `more than \d+ compiled schema nodes`}, // U-0037: "… nodes and uses"
	{"U-0035", `schema nodes nested deeper than`},
	{"U-0036", `XPath steps`},
	{"U-0038", `extension instances of a top-level uses`},
}

// schemaLimit is the id of the allowed limit an unsupported reason names, "" for none.
func schemaLimit(reason string) string {
	for _, l := range schemaLimits {
		if regexp.MustCompile(l.re).MatchString(reason) {
			return l.id
		}
	}
	return ""
}

// TestYangEngineTargets: the m1 schema dumps agree with the oracle (compiled printout skipped).
func TestYangEngineTargets(t *testing.T) {
	m := load(t)
	for _, f := range m.Fixtures {
		if f.ID != "m1/schema-tree" && f.ID != "m1/schema-tree-no-features" {
			continue
		}
		got, err := Yang{}.Run(Request{ID: f.ID, BaseDir: fixtureDir(m, f), Params: f.Request})
		if err != nil {
			t.Fatal(f.ID, err)
		}
		golden, err := LoadGolden(m.GoldenPath(f))
		if err != nil {
			t.Fatal(err)
		}
		golden, _ = withoutSkipped(golden, Yang{}.SkippedFields("schema"))
		got, _ = withoutSkipped(got, Yang{}.SkippedFields("schema"))
		if d := diffResponses(golden, got); d != "" {
			t.Errorf("%s: %s", f.ID, d)
		}
	}
}

// TestTypedCompared: the data typed dump is compared without the skipped type details only.
func TestTypedCompared(t *testing.T) {
	r := Response{"typed": []any{map[string]any{"path": "/a:x", "flags": map[string]any{"new": false},
		"value": map[string]any{"canonical": "1", "type": "uint8", "union_member": nil}, "meta": []any{}}}}
	got, skipped := withoutSkipped(r, Yang{}.SkippedFields("data"))
	want := Response{"typed": []any{map[string]any{"path": "/a:x", "flags": map[string]any{"new": false},
		"value": map[string]any{"canonical": "1"}}}}
	if d := diffResponses(want, got); d != "" || len(skipped) != 3 || r["typed"].([]any)[0].(map[string]any)["meta"] == nil {
		t.Fatalf("%s %v %v", d, got, skipped)
	}
	got["typed"].([]any)[0].(map[string]any)["flags"] = map[string]any{"new": true}
	if diffResponses(want, got) == "" {
		t.Fatal("flags not compared")
	}
}

// TestPathPrefixes: the prefixes notFoundRC re-finds split only at '/' outside predicates.
func TestPathPrefixes(t *testing.T) {
	got := pathPrefixes(`/m:c/l[k='a/b'][n="x]y"]/v`)
	want := []string{`/m:c/l[k='a/b'][n="x]y"]`, `/m:c`}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("%q, want %q", got, want)
	}
}

// TestXPathGateSelection: the M3 gate takes xpath, atoms and trim sequences, and allows only the
// listed unsupported fixture with its deviation.
func TestXPathGateSelection(t *testing.T) {
	trim := map[string]any{"op": "sequence", "steps": []any{map[string]any{"do": "parse"}, map[string]any{"do": "trim"}}}
	other := map[string]any{"op": "sequence", "steps": []any{map[string]any{"do": "parse"}}}
	if xpathKind(map[string]any{"op": "xpath"}) != "xpath" || xpathKind(map[string]any{"op": "atoms"}) != "atoms" ||
		xpathKind(trim) != "trim" || xpathKind(other) != "" || xpathKind(map[string]any{"op": "data"}) != "" {
		t.Error("gate selection")
	}
	if !xpathAllowed("ut-xpath/anydata-01", "not supported: anydata (deviations.md U-0043)") ||
		xpathAllowed("ut-xpath/anydata-01", "some other reason") || xpathAllowed("ut-xpath/axes-01", "U-0043") {
		t.Error("allowlist")
	}
}

// TestSchemaLimit: only the allowlisted limits pass the M2 gate.
func TestSchemaLimit(t *testing.T) {
	for reason, want := range map[string]string{
		"unsupported: not supported: extension instance yangmnt:mount-point of ietf-yang-schema-mount (U-0024)": "U-0024",
		"unsupported: not supported: YIN module file \"a.yin\" (U-0021)":                                        "U-0021",
		"unsupported: resource budget exceeded: more than 1048576 compiled schema nodes and uses":               "U-0034",
		"unsupported: not supported: extension instance x of y (U-0060)":                                        "",
		"unsupported: XSD regular expression not supported":                                                     "",
	} {
		if got := schemaLimit(reason); got != want {
			t.Errorf("schemaLimit(%q) = %q, want %q", reason, got, want)
		}
	}
}
