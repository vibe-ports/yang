// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-ports/yang/data"
)

// agreeFloor is the number of fixtures that agree (with or without skipped fields) on main; a
// change that lowers it is a regression.
const agreeFloor = 2332

func TestDataOptionsPresetsMatchDparamsOf(t *testing.T) {
	neutral := data.ParseOptions{Validate: data.ValidateOptions{MultiError: true}}
	operational := neutral
	operational.Validate.Operational = true
	config := neutral
	config.NoState, config.Validate.NoState = true, true
	get := neutral
	get.ParseOnly = true
	get.Validate = data.ValidateOptions{}
	getconfig := neutral
	getconfig.ParseOnly, getconfig.NoState = true, true
	getconfig.Validate = data.ValidateOptions{}

	for _, tc := range []struct {
		name string
		typ  any
		want data.ParseOptions
	}{
		{"absent", nil, operational},
		{"config", "config", config},
		{"data-operational", "data-operational", operational},
		{"data", "data", neutral},
		{"get", "get", get},
		{"getconfig", "getconfig", getconfig},
		{"edit", "edit", getconfig},
		{"rpc", "rpc", neutral},
		{"reply", "reply", neutral},
		{"notif", "notif", neutral},
		{"notif-netconf", "notif-netconf", neutral},
		{"notif-restconf", "notif-restconf", neutral},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := map[string]any{}
			if tc.typ != nil {
				p["data_type"] = tc.typ
			}
			got, err := dataOptions(p, execData)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("dataOptions(%v) = %+v, want %+v", p, got, tc.want)
			}
		})
	}
}

func TestSequenceParseOnlyStopsAfterFirstInvalidValue(t *testing.T) {
	for _, tc := range []struct {
		name, format, input, wantPath, wantMessage string
	}{
		{"json", "json", `{"pv2:c":{"u":"","dec":"48"}}`, "/pv2:c/u", "Invalid union value"},
		{"xml", "xml", `<c xmlns="urn:vibe-ports:yang:conformance:pv2"><u></u><dec>48</dec></c>`, "/pv2:c/u", "Invalid union value"},
		{"valid-before-invalid", "json", `{"pv2:c":{"dec":"1","u":""}}`, "/pv2:c/u", "Invalid union value"},
		{"range-before-union", "json", `{"pv2:c":{"dec":"48","u":""}}`, "/pv2:c/dec", "Unsatisfied range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: map[string]any{
				"op": "sequence", "searchdirs": []any{"schemas"},
				"modules":         []any{map[string]any{"name": "pv2"}},
				"context_options": []any{"no_yanglibrary"},
				"steps": []any{
					map[string]any{"do": "parse", "format": tc.format, "data_type": "config", "parse_only": true, "data": tc.input},
					map[string]any{"do": "dump", "with_defaults": "explicit"},
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if got := resp["failed_step"]; fmt.Sprint(got) != "0" {
				t.Errorf("failed_step = %v, want 0", got)
			}
			steps := resp["steps"].([]any)
			parse := steps[0].(map[string]any)
			if got := parse["rc"].(map[string]any)["name"]; got != "LY_EVALID" {
				t.Errorf("parse rc = %v, want LY_EVALID", got)
			}
			diags := parse["diagnostics"].([]any)
			if len(diags) != 1 {
				t.Fatalf("diagnostics = %#v, want one value error", diags)
			}
			if msg := fmt.Sprint(diags[0]); !strings.Contains(msg, tc.wantMessage) || !strings.Contains(msg, tc.wantPath) {
				t.Errorf("first diagnostic = %s, want %s at %s", msg, tc.wantMessage, tc.wantPath)
			}
		})
	}
}

func TestRootDoDoesNotSetExecutionContext(t *testing.T) {
	common := func(op string) map[string]any {
		return map[string]any{
			"op": op, "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
			"context_options": []any{"no_yanglibrary"}, "data_type": "data",
		}
	}
	dataReq := common("data")
	dataReq["data"], dataReq["validate_options"] = inventoryStateData, []any{"no_state"}
	xpathReq := common("xpath")
	xpathReq["data"], xpathReq["xpath"], xpathReq["rpc"] = inventoryData, "/pv2:c", 42
	diffReq := common("diff")
	diffReq["first"], diffReq["second"], diffReq["rpc"] = inventoryData, inventoryData, 42
	for _, tc := range []struct {
		name, do string
		params   map[string]any
	}{
		{"data/edit-do-cannot-drop-no-state", "edit", dataReq},
		{"xpath/parse-do-cannot-read-rpc", "parse", xpathReq},
		{"diff/parse-do-cannot-read-rpc", "parse", diffReq},
	} {
		t.Run(tc.name, func(t *testing.T) {
			without := maps.Clone(tc.params)
			with := maps.Clone(tc.params)
			with["do"] = tc.do
			want, wantErr := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: without})
			got, gotErr := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: with})
			if fmt.Sprint(gotErr) != fmt.Sprint(wantErr) || !reflect.DeepEqual(got, want) {
				t.Fatalf("root do changed execution\nwithout: %v, %v\nwith: %v, %v", want, wantErr, got, gotErr)
			}
		})
	}
}

func TestSequenceParseReplyIntoRequestKeepsInput(t *testing.T) {
	resp, err := Yang{}.Run(Request{
		BaseDir: filepath.Join("corpus", "ut-parser"),
		Params: map[string]any{
			"op":         "sequence",
			"searchdirs": []any{"rb.1"},
			"modules":    []any{map[string]any{"name": "rb"}},
			"steps": []any{map[string]any{
				"do":         "parse",
				"format":     "json",
				"data_type":  "reply",
				"unknown":    "reject",
				"parse_only": true,
				"keep_input": true,
				"rpc":        `{"rb:r":{"in":"keep"}}`,
				"data":       `{"rb:before":"out"}`,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	steps := resp["steps"].([]any)
	typed := steps[0].(map[string]any)["typed"].([]any)
	var paths []string
	for _, item := range typed {
		paths = append(paths, item.(map[string]any)["path"].(string))
	}
	for _, want := range []string{"/rb:r/in", "/rb:r/before"} {
		if !slices.Contains(paths, want) {
			t.Errorf("typed paths %v do not contain %s", paths, want)
		}
	}
}

func TestSequenceXMLControlByteReturnCode(t *testing.T) {
	for _, tc := range []struct {
		name, input, rc string
		diags           int
	}{
		{"after element name", "<A \x0f", "LY_EINVAL", 0},
		{"after opening bracket", "<\x0f", "LY_EVALID", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := Yang{}.Run(Request{BaseDir: "corpus/protocol-v2", Params: map[string]any{
				"context_options": []any{"no_yanglibrary"},
				"modules":         []any{map[string]any{"name": "pv2"}},
				"op":              "sequence",
				"searchdirs":      []any{"schemas"},
				"steps": []any{map[string]any{"data": tc.input, "data_type": "config", "do": "parse",
					"format": "xml", "parse_only": true}, map[string]any{"do": "dump", "with_defaults": "explicit"}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			parse := list(resp["steps"])[0].(map[string]any)
			if resp.Verdict() != "invalid" || inventoryRCName(parse["rc"]) != tc.rc || len(list(parse["diagnostics"])) != tc.diags {
				t.Fatalf("response = %v; want %s and %d diagnostics", resp, tc.rc, tc.diags)
			}
		})
	}
}

func TestDataRPCParentIsUnsupportedU0106(t *testing.T) {
	_, err := Yang{}.Run(Request{
		BaseDir: filepath.Join("corpus", "ut-parser"),
		Params: map[string]any{
			"op":         "data",
			"searchdirs": []any{"rollback-key"},
			"modules":    []any{map[string]any{"name": "rollback-key"}},
			"format":     "json",
			"data_type":  "rpc",
			"parse_only": true,
			"rpc":        `{"rollback-key:l":[{"k":"a","j":"z","a":{}}]}`,
			"keep_input": true,
			"rpc_parent": "/rollback-key:l[k='a'][j='z']",
			"data":       `{"k":"b"}`,
		},
	})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "U-0106") {
		t.Fatalf("Yang.Run rpc_parent error = %v, want unsupported U-0106", err)
	}
}

func TestSequenceDiffRPCIntoExistingRequestIsRequestError(t *testing.T) {
	step := map[string]any{
		"do":         "diff",
		"data_type":  "rpc",
		"parse_only": true,
		"data":       `{}`,
		"rpc":        `{}`,
		"keep_input": true,
	}
	err := runSequence(Request{Params: map[string]any{"steps": []any{step}}}, nil, map[string]any{}, execSequence)
	if err == nil || err.Error() != "diff step: rpc only with data_type reply (a request-error)" {
		t.Fatalf("runSequence operation diff error = %v, want request-error", err)
	}
}

func TestSequenceValidateModuleChecks(t *testing.T) {
	for _, tc := range []struct {
		name        string
		step        map[string]any
		want        string
		unsupported bool
	}{
		{"final only", map[string]any{"do": "validate", "module": "vm-a", "final_only": true},
			"lyd_validate_module_final is not exported (split-validation speed path), U-0106", true},
		{"final only without module", map[string]any{"do": "validate", "final_only": true},
			"final_only needs module", false},
		{"module not implemented", map[string]any{"do": "validate", "module": "missing"},
			"validate module missing not implemented", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Yang{}.Run(Request{
				BaseDir: filepath.Join("corpus", "protocol-v2"),
				Params: map[string]any{
					"op":              "sequence",
					"searchdirs":      []any{"schemas"},
					"modules":         []any{map[string]any{"name": "vm-a"}},
					"context_options": []any{"no_yanglibrary"},
					"steps":           []any{tc.step},
				},
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) || errors.Is(err, ErrUnsupported) != tc.unsupported {
				t.Fatalf("Yang.Run error = %v, unsupported = %v; want %q, unsupported = %v",
					err, errors.Is(err, ErrUnsupported), tc.want, tc.unsupported)
			}
		})
	}
}

func TestSequenceValidateModulePresentHasNoDiagnostic(t *testing.T) {
	resp, err := Yang{}.Run(Request{
		BaseDir: filepath.Join("corpus", "protocol-v2"),
		Params: map[string]any{
			"op":              "sequence",
			"searchdirs":      []any{"schemas"},
			"modules":         []any{map[string]any{"name": "vm-a"}},
			"context_options": []any{"no_yanglibrary"},
			"steps": []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true,
					"data": `{"vm-a:top":{"x":"1"}}`},
				map[string]any{"do": "validate", "module": "vm-a", "validate_options": []any{"present"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	steps := resp["steps"].([]any)
	validate := steps[1].(map[string]any)
	rc := validate["rc"].(map[string]any)
	if got := rc["name"]; got != "LY_EINVAL" {
		t.Errorf("validate rc = %v, want LY_EINVAL", got)
	}
	if diags := validate["diagnostics"].([]any); len(diags) != 0 {
		t.Errorf("validate diagnostics = %#v, want none", diags)
	}
}

func TestSequenceValidateModuleDiffAnchors(t *testing.T) {
	resp, err := Yang{}.Run(Request{
		BaseDir: filepath.Join("corpus", "protocol-v2"),
		Params: map[string]any{
			"op":         "sequence",
			"searchdirs": []any{"schemas"},
			"modules":    []any{map[string]any{"name": "pv2-diff"}},
			"steps": []any{
				map[string]any{"do": "parse", "format": "json", "data_type": "config", "parse_only": true,
					"data": `{"pv2-diff:c":{"mode":"off"}}`},
				map[string]any{"do": "validate", "data_type": "config", "module": "pv2-diff"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	diff := resp["steps"].([]any)[1].(map[string]any)["implicit_diff"].(string)
	for _, anchor := range []string{`"yang:value": ""`, `"yang:value": "b"`} {
		if !strings.Contains(diff, anchor) {
			t.Errorf("module diff does not contain anchor %s:\n%s", anchor, diff)
		}
	}
}

func TestDiffRejectsOperationDataType(t *testing.T) {
	_, err := Yang{}.Run(Request{
		BaseDir: filepath.Join("corpus", "ut-parser"),
		Params: map[string]any{
			"op":         "diff",
			"searchdirs": []any{"rb.1"},
			"modules":    []any{map[string]any{"name": "rb"}},
			"format":     "json",
			"data_type":  "reply",
			"first":      `not JSON`,
			"second":     `not JSON`,
		},
	})
	want := "diff supports datastore data types only (a request-error)"
	if err == nil || err.Error() != want {
		t.Fatalf("Yang.Run operation diff error = %v, want %q", err, want)
	}
}

func TestDatastoreOperationFieldChecks(t *testing.T) {
	base := map[string]any{
		"op":        "data",
		"format":    "json",
		"data_type": "config",
		"data":      `{}`,
	}
	t.Run("rpc_parent", func(t *testing.T) {
		params := maps.Clone(base)
		params["rpc_parent"] = "/x"
		_, err := Yang{}.Run(Request{Params: params})
		want := "rpc_parent requires data_type rpc, rpc, and keep_input true (a request-error)"
		if err == nil || err.Error() != want || errors.Is(err, ErrUnsupported) {
			t.Fatalf("Yang.Run error = %v; want %q request-error", err, want)
		}
	})
	t.Run("rpc and keep_input ignored", func(t *testing.T) {
		params := maps.Clone(base)
		params["rpc"] = `not JSON`
		params["keep_input"] = true
		resp, err := Yang{}.Run(Request{Params: params})
		if err != nil || resp.Verdict() != "valid" {
			t.Fatalf("Yang.Run = verdict %q, error %v; want valid", resp.Verdict(), err)
		}
	})
}

func TestSequenceDatastoreOperationFieldChecks(t *testing.T) {
	for _, tc := range []struct {
		name string
		step map[string]any
		want string
	}{
		{"parse rpc", map[string]any{"do": "parse", "data_type": "config", "data": `{}`, "rpc": `{}`},
			"parse step: rpc only with data_type reply (a request-error)"},
		{"parse rpc_parent", map[string]any{"do": "parse", "data_type": "config", "data": `{}`, "rpc_parent": "/x"},
			`unknown key "rpc_parent" in a sequence step (a request-error)`},
		{"diff rpc", map[string]any{"do": "diff", "data_type": "config", "data": `{}`, "rpc": `{}`},
			"diff step: rpc only with data_type reply (a request-error)"},
		{"diff rpc_parent", map[string]any{"do": "diff", "data_type": "config", "data": `{}`, "rpc_parent": "/x"},
			`unknown key "rpc_parent" in a sequence step (a request-error)`},
		{"diff keep_input true", map[string]any{"do": "diff", "data_type": "config", "data": `{}`, "keep_input": true},
			"diff step: keep_input only with rpc (a request-error)"},
		{"diff keep_input false", map[string]any{"do": "diff", "data_type": "config", "data": `{}`, "keep_input": false},
			"diff step: keep_input only with rpc (a request-error)"},
		{"diff keep_input null", map[string]any{"do": "diff", "data_type": "config", "data": `{}`, "keep_input": nil},
			"diff step: keep_input only with rpc (a request-error)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runSequence(Request{Params: map[string]any{"steps": []any{tc.step}}}, nil, map[string]any{}, execSequence)
			if err == nil || err.Error() != tc.want || errors.Is(err, ErrUnsupported) {
				t.Fatalf("runSequence error = %v; want %q request-error", err, tc.want)
			}
		})
	}
	t.Run("parse keep_input ignored", func(t *testing.T) {
		resp, err := Yang{}.Run(Request{Params: map[string]any{
			"op": "sequence",
			"steps": []any{map[string]any{
				"do": "parse", "format": "json", "data_type": "config", "parse_only": true,
				"data": `{}`, "keep_input": true,
			}},
		}})
		if err != nil || resp.Verdict() != "valid" {
			t.Fatalf("Yang.Run = verdict %q, error %v; want valid", resp.Verdict(), err)
		}
	})
}

// TestYangEngineSchema runs every fixture through package yang and logs the tally. No fixture may
// differ: a disagreement is either fixed or recorded as a deviation (deviations.md) or as
// unsupported, never absorbed by new agreements under the floor. M2 exit gate (#43): an op schema
// fixture may be unsupported only for a limit in schemaLimits.
func TestYangEngineSchema(t *testing.T) {
	m, rep := yangReport(t)
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
	m, rep := yangReport(t)
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
// step) or "" for a fixture outside the gate (also XPath over operation data, an M4 fixture).
func xpathKind(req map[string]any) string {
	switch op, _ := req["op"].(string); op {
	case "xpath", "atoms":
		if dt, _ := req["data_type"].(string); dt == "rpc" || dt == "reply" || dt == "notif" {
			return "" // XPath over operation data: an M4 operation fixture (#105), not M3
		}
		return op
	case "sequence":
		kind := ""
		for _, st := range list(req["steps"]) {
			s, ok := st.(map[string]any)
			if !ok {
				continue
			}
			if dt, _ := s["data_type"].(string); s["do"] == "parse" && (dt == "rpc" || dt == "reply" || dt == "notif") {
				return "" // a trim of operation data, like XPath over it: an M4 operation fixture
			}
			if s["do"] == "trim" {
				kind = "trim"
			}
		}
		return kind
	}
	return ""
}

// xpathUnsupported are the xpath fixtures that may stay unsupported after M3, each with the
// deviations.md id its reason must name.
var xpathUnsupported = map[string]string{
	// native evidence for TrimXPath's run and link cleanup; the Go side is TestRBTreeRunEmptied
	// and the Go-expressible seq/trim-lyds-all-indexed, seq/trim-links-lkorder-* fixtures
	// step 2 parses with LYD_PARSE_ORDERED (D-0058) before step 3's LYD_DUP_NO_LYDS (U-0105): the
	// engine refuses at the first unsupported step it executes
	"seq/trim-lyds-emptied-run":     "D-0058",
	"seq/trim-lyds-partial-run":     "D-0058",
	"seq/trim-links-referrer-first": "D-0058", // LYD_PARSE_ORDERED puts referrers first
}

// xpathAllowed reports whether fixture id may be unsupported with this reason.
func xpathAllowed(id, reason string) bool {
	u, ok := xpathUnsupported[id]
	return ok && strings.Contains(reason, u)
}

// schemaLimits are the reasons an op schema fixture may be unsupported after M2 (issue #43): the
// formats and extensions outside v1 and the resource budgets (conformance/deviations.md).
var schemaLimits = []struct{ id, re string }{
	{"U-0001", `character-class expansion exceeds \d+ ranges \(U-0001\)`},
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

// TestYangEngineValidation is the M4 exit gate (#105): no validation-area fixture and no operation
// fixture (area operations, or an rpc, reply or notif data type in the request or a step) may
// differ, and none may be unsupported unless validationUnsupported lists it with the
// deviations.md entry its reason names.
func TestYangEngineValidation(t *testing.T) {
	m, rep := yangReport(t)
	n := map[string]int{}
	for i, r := range rep.Results {
		kind := validationKind(m.Fixtures[i])
		if kind == "" {
			continue
		}
		n[kind]++
		switch {
		case r.Status == Differ:
			t.Errorf("differ %s: %s", r.ID, r.Detail)
		case r.Status == Unsupported && !validationAllowed(r.ID, r.Detail):
			t.Errorf("unsupported %s fixture %s without a listed deviation: %s", kind, r.ID, r.Detail)
		}
	}
	t.Logf("gated fixtures: %v", n)
	for kind, floor := range validationFloors {
		if n[kind] < floor {
			t.Errorf("%d %s fixtures gated, fewer than %d: the selection lost fixtures", n[kind], kind, floor)
		}
	}
}

// validationFloors are the gated fixtures of each kind at the M4 exit; fewer means the selection
// or the corpus lost some, which would let the gate pass on nothing.
var validationFloors = map[string]int{"validation": 166, "operation": 90}

// validationKind is the M4 gate kind of a fixture: "operation" (area operations, or an operation
// data type in the request or one of its steps), "validation" (area validation) or "" outside the
// gate.
func validationKind(f Fixture) string {
	isOp := func(m map[string]any) bool { _, ok := opTypes[str(m, "data_type", "")]; return ok }
	op := slices.Contains(f.Areas, "operations") || isOp(f.Request)
	for _, st := range list(f.Request["steps"]) {
		if s, ok := st.(map[string]any); ok && isOp(s) {
			op = true
		}
	}
	switch {
	case op:
		return "operation"
	case slices.Contains(f.Areas, "validation"):
		return "validation"
	}
	return ""
}

// validationUnsupported are the validation and operation fixtures that may stay unsupported after
// M4, each with the deviations.md id its reason must name.
var validationUnsupported = map[string]string{
	// U-0110: ParseOp.Request requires an RPC/action, unlike the oracle parent.
	"protocol-v2/reply-non-operation-parent": "U-0110",
	// U-0112: public any edits and pointer-level probes await a later API.
	"any-core/create-invalid-json":           "U-0112",
	"any-core/create-invalid-text":           "U-0112",
	"any-core/create-invalid-xml":            "U-0112",
	"any-core/create-xml-scan-error-anydata": "U-0112",
	"any-core/create-xml-scan-error-anyxml":  "U-0112",
	"any-core/create-warning-xml-key-order":  "U-0112",
	"any-core/equal-xml-scan-error":          "U-0107",
	"any-core/any-text-embedded-nul":         "U-0112",
	"any-core/dup-node-then-empty-object":    "U-0112",
	"any-core/dup-payload-parent-context":    "U-0112",
	"any-core/equal-wide-system-ordered":     "U-0112",
	"any-core/nil-any-boundaries":            "U-0112",
	"any-core/trim-payload-links":            "U-0112",
	"any-core/merge-mixed-into-tree":         "U-0112",
	"any-core/dup-into-anydata":              "U-0107",
	"any-core/dup-into-default-any-parent":   "U-0112",
	"any-core/equal-mixed-parent-payload":    "U-0112",
	"any-core/dup-into-anyxml":               "U-0107",
	"ut-new/any-create-json":                 "U-0112",
	"ut-new/any-create-xml":                  "U-0112",
	// D-0058: LYD_PARSE_ORDERED input order is not exported.
	"seq/trim-links-referrer-first": "D-0058",
	// U-0105: LYD_DUP_NO_LYDS is not exported.
	"seq/trim-lyds-emptied-run": "D-0058", // parse ordered (step 2) before dup no_lyds (U-0105, step 3)
	"seq/trim-lyds-partial-run": "D-0058",
	// U-0106: an rpc input parsed into an existing request (Request is for replies only).
	"ops/parse-json-rpc-parent-opaque-meta":   "U-0106",
	"ops/opidx-meta-doc-first":                "U-0106",
	"ops/parse-json-reply-opaque-request":     "U-0106",
	"ops/seq-parse-json-reply-opaque-request": "U-0106",
	"ops/parse-json-rpc-rollback-key":         "U-0106",
	"ops/parse-json-rpc-rollback-keys-order":  "U-0106",
	"ops/parse-xml-rpc-parent-envelope-value": "U-0106",
	"ops/parse-xml-rpc-parent-input":          "U-0106",
	"seq/validate-module-final-a":             "U-0106",
	"seq/validate-module-final-a-none":        "U-0106",
	"seq/validate-module-final-b":             "U-0106",
	"seq/validate-module-final-when-absent":   "U-0106",
	// U-0107: lyd_new_opaq and lyd_new_opaq2 are deferred to M5 with the envelopes.
	"ut-new/insert-13": "U-0107",
	"ut-new/insert-14": "U-0107",
	"ut-new/insert-15": "U-0107",
	"ut-new/insert-16": "U-0107",
	"ut-new/insert-41": "U-0107",
	"ut-new/insert-42": "U-0107",
	"ut-new/insert-56": "U-0107",
	"ut-new/insert-61": "U-0107",
	"ut-new/insert-62": "U-0107",
	// U-0108: canonical/store-only constructors and lyd_change_term_canon are not exported.
	"ut-new/change-01": "U-0108",
	"ut-new/change-03": "U-0108",
	"ut-new/insert-23": "U-0108",
	"ut-new/insert-24": "U-0108",
	"ut-new/insert-25": "U-0108",
	"ut-new/insert-36": "U-0108",
	"ut-new/insert-37": "U-0108",
	"ut-new/insert-38": "U-0108",
	"ut-new/insert-44": "U-0108",
	"ut-new/insert-46": "U-0108",
	"ut-new/insert-48": "U-0108",
	"ut-new/insert-50": "U-0108",
	// U-0109: NewPath cannot reproduce lyd_new_list2's accepted inputs and diagnostics.
	"ut-new/insert-27": "U-0109",
	"ut-new/insert-28": "U-0109",
	"ut-new/insert-29": "U-0109",
	"ut-new/insert-30": "U-0109",
	"ut-new/insert-32": "U-0109",
	"ut-new/insert-33": "U-0109",
	"ut-new/insert-34": "U-0109",
	"ut-new/insert-35": "U-0109",
	"ut-new/insert-51": "U-0109",
	"ut-new/insert-53": "U-0109",
	"ut-new/insert-57": "U-0109",
	"ut-new/insert-58": "U-0109",
	"ut-new/insert-59": "U-0109",
	"ut-new/path-05":   "U-0109",
}

// validationAllowed reports whether fixture id may be unsupported with this reason.
func validationAllowed(id, reason string) bool {
	u, ok := validationUnsupported[id]
	return ok && strings.Contains(reason, u)
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

func TestEmptyRevisionDoesNotMutateEarlierModule(t *testing.T) {
	base := filepath.Join("corpus", "protocol-v2")
	request := func(modules ...any) Response {
		t.Helper()
		got, err := (Yang{}).Run(Request{BaseDir: base, Params: map[string]any{
			"op": "schema", "searchdirs": []any{"schemas"}, "modules": modules,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	one := request(map[string]any{"name": "pv2"})
	two := request(
		map[string]any{"name": "pv2"},
		map[string]any{"name": "pv2", "revision": "", "features": []any{"extra"}},
	)
	single := request(map[string]any{"name": "pv2", "revision": "", "features": []any{"extra"}})
	oneModule := one["modules"].([]any)[0]
	twoModules := two["modules"].([]any)
	if !reflect.DeepEqual(oneModule, twoModules[0]) {
		t.Fatalf("first module changed after rejected second load:\nalone: %#v\nrepeated: %#v", oneModule, twoModules[0])
	}
	second := twoModules[1].(map[string]any)
	if two["verdict"] != "invalid" || second["accepted"] != false || second["phase"] != "parse" {
		t.Fatalf("second module = %#v, verdict %v; want rejected in parse", second, two["verdict"])
	}
	if singleModule := single["modules"].([]any)[0]; !reflect.DeepEqual(singleModule, second) {
		t.Fatalf("single empty-revision response = %#v, repeated response = %#v", singleModule, second)
	}
	wantDiagnostics := []struct{ err, msg string }{
		{"LY_EINVAL", `Module "pv2" parsed with the wrong revision ("none" instead "").`},
		{"LY_EOTHER", `Parsing module "pv2" failed.`},
	}
	diagnostics := second["diagnostics"].([]any)
	if len(diagnostics) != len(wantDiagnostics) {
		t.Fatalf("diagnostics = %#v", diagnostics)
	}
	for i, want := range wantDiagnostics {
		diagnostic := diagnostics[i].(map[string]any)
		code := diagnostic["code"].(map[string]any)
		if diagnostic["phase"] != "parse" || diagnostic["level"] != "error" || code["name"] != want.err ||
			diagnostic["vecode_name"] != "LYVE_SUCCESS" || diagnostic["msg"] != want.msg ||
			diagnostic["data_path"] != nil || diagnostic["schema_path"] != nil || diagnostic["apptag"] != nil {
			t.Errorf("diagnostic %d = %#v, want %s/%q", i, diagnostic, want.err, want.msg)
		}
	}
}

func TestEmptyRevisionUsesExistingContextForParserChecks(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a.yang": "module a { yang-version 1.1; namespace urn:a; prefix a; }",
		"b.yang": "module b { yang-version 1.1; namespace urn:b; prefix b; include a; revision 2020-01-01; }",
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := (Yang{}).Run(Request{BaseDir: dir, Params: map[string]any{
		"op": "schema", "searchdirs": []any{"."}, "context_options": []any{"no_yanglibrary"},
		"modules": []any{map[string]any{"name": "a"}, map[string]any{"name": "b", "revision": ""}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	modules := got["modules"].([]any)
	if len(modules) != 2 || modules[0].(map[string]any)["accepted"] != true {
		t.Fatalf("modules = %#v", modules)
	}
	second := modules[1].(map[string]any)
	diagnostics := second["diagnostics"].([]any)
	if second["accepted"] != false || second["phase"] != "parse" || len(diagnostics) == 0 {
		t.Fatalf("second module = %#v", second)
	}
	diagnostic := diagnostics[0].(map[string]any)
	code := diagnostic["code"].(map[string]any)
	if code["name"] != "LY_EVALID" || diagnostic["vecode_name"] != "LYVE_SEMANTICS" ||
		diagnostic["msg"] != `Name collision between module and submodule of name "a".` {
		t.Fatalf("first diagnostic = %#v", diagnostic)
	}
}

// TestTypedCompared: typed fields, including any values, remain compared.
func TestTypedCompared(t *testing.T) {
	r := Response{"typed": []any{map[string]any{"path": "/a:x", "flags": map[string]any{"new": false},
		"value": map[string]any{"canonical": "1", "type": "uint8", "typedef": nil, "union_member": nil, "hints": []any{}},
		"meta":  []any{}, "opaque": nil, "any": map[string]any{"value": "payload"}}}}
	got, skipped := withoutSkipped(r, Yang{}.SkippedFields("data"))
	want := Response{"typed": []any{map[string]any{"path": "/a:x", "flags": map[string]any{"new": false},
		"value": map[string]any{"canonical": "1", "type": "uint8", "typedef": nil, "union_member": nil, "hints": []any{}},
		"meta":  []any{}, "opaque": nil, "any": map[string]any{"value": "payload"}}}}
	if d := diffResponses(want, got); d != "" || len(skipped) != 0 {
		t.Fatalf("%s %v %v", d, got, skipped)
	}
	for _, field := range []string{"type", "typedef", "union_member", "hints", "meta", "opaque", "flags", "any"} {
		got, _ := withoutSkipped(r, Yang{}.SkippedFields("data"))
		node := got["typed"].([]any)[0].(map[string]any)
		if slices.Contains([]string{"meta", "opaque", "flags", "any"}, field) {
			node[field] = "changed"
		} else {
			node["value"].(map[string]any)[field] = "changed"
		}
		if diffResponses(want, got) == "" {
			t.Errorf("%s not compared", field)
		}
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

func TestFindPathFixupRequests(t *testing.T) {
	common := func(steps ...any) map[string]any {
		return map[string]any{"op": "sequence", "searchdirs": []any{"schemas"},
			"modules": []any{map[string]any{"name": "pv2"}}, "steps": steps}
	}
	t.Run("print subtree spaced", func(t *testing.T) {
		resp, err := Yang{}.Run(Request{BaseDir: "corpus/protocol-v2", Params: map[string]any{
			"op": "data", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
			"data": `{"pv2:c":{"mode":"on"}}`, "parse_only": true, "print_subtree": " /pv2:c/mode",
		}})
		if err != nil || resp.Verdict() != "valid" || resp["subtree"] == nil {
			t.Fatalf("spaced subtree = %v, %v; want a valid subtree", resp, err)
		}
	})
	t.Run("delete spaced", func(t *testing.T) {
		resp, err := Yang{}.Run(Request{BaseDir: "corpus/protocol-v2", Params: common(
			map[string]any{"do": "parse", "data": `{"pv2:c":{"mode":"on"}}`, "parse_only": true},
			map[string]any{"do": "edit", "delete": " /pv2:c/mode"}, map[string]any{"do": "dump"},
		)})
		if err != nil {
			t.Fatal(err)
		}
		steps := list(resp["steps"])
		dump := steps[2].(map[string]any)["tree"].(map[string]any)["json"].(string)
		if resp.Verdict() != "valid" || strings.Contains(dump, "mode") {
			t.Fatalf("spaced delete = %v, %v; want a valid tree without mode", resp, err)
		}
	})
	t.Run("delete opaque first", func(t *testing.T) {
		resp, err := Yang{}.Run(Request{BaseDir: "corpus/protocol-v2", Params: common(
			map[string]any{"do": "parse", "data": `{"pv2:nosuch":"x"}`, "unknown": "opaque", "parse_only": true},
			map[string]any{"do": "edit", "delete": "/pv2:c/mode"}, map[string]any{"do": "dump"},
		)})
		if err != nil {
			t.Fatal(err)
		}
		steps := list(resp["steps"])
		edit := steps[1].(map[string]any)
		rc, _ := edit["rc"].(map[string]any)
		if resp.Verdict() != "invalid" || rc["name"] != "LY_EINVAL" || len(list(edit["diagnostics"])) != 0 {
			t.Fatalf("opaque-first delete = %v, %v; want LY_EINVAL with no diagnostics", resp, err)
		}
	})
}

// TestXPathGateSelection: the M3 gate takes xpath, atoms and trim sequences, and allows only the
// listed unsupported fixture with its deviation.
func TestXPathGateSelection(t *testing.T) {
	trim := map[string]any{"op": "sequence", "steps": []any{map[string]any{"do": "parse"}, map[string]any{"do": "trim"}}}
	other := map[string]any{"op": "sequence", "steps": []any{map[string]any{"do": "parse"}}}
	if xpathKind(map[string]any{"op": "xpath", "data_type": "reply"}) != "" {
		t.Error("xpath over operation data is not M3")
	}
	if xpathKind(map[string]any{"op": "xpath"}) != "xpath" || xpathKind(map[string]any{"op": "atoms"}) != "atoms" ||
		xpathKind(trim) != "trim" || xpathKind(other) != "" || xpathKind(map[string]any{"op": "data"}) != "" {
		t.Error("gate selection")
	}
	if xpathAllowed("ut-xpath/anydata-01", "D-0058") || xpathAllowed("ut-xpath/axes-01", "D-0058") {
		t.Error("allowlist")
	}
}

// TestValidationGateSelection: the M4 gate takes validation-area fixtures and operation fixtures
// (by area, request data type or step data type), and allows only listed fixtures with their id.
func TestValidationGateSelection(t *testing.T) {
	for _, c := range []struct {
		f    Fixture
		want string
	}{
		{Fixture{Areas: []string{"validation"}, Request: map[string]any{"op": "data"}}, "validation"},
		{Fixture{Areas: []string{"operations"}, Request: map[string]any{"op": "data"}}, "operation"},
		{Fixture{Areas: []string{"codecs"}, Request: map[string]any{"op": "data", "data_type": "reply"}}, "operation"},
		{Fixture{Areas: []string{"validation"}, Request: map[string]any{"op": "sequence",
			"steps": []any{map[string]any{"do": "parse", "data_type": "notif"}}}}, "operation"},
		{Fixture{Areas: []string{"codecs"}, Request: map[string]any{"op": "data", "data_type": "config"}}, ""},
	} {
		if got := validationKind(c.f); got != c.want {
			t.Errorf("%v: %q, want %q", c.f, got, c.want)
		}
	}
	if !validationAllowed("protocol-v2/reply-non-operation-parent", "reply into a non-operation parent (U-0110)") ||
		validationAllowed("protocol-v2/reply-non-operation-parent", "some other reason") ||
		validationAllowed("ops/parse-xml-reply-01", "U-0110") {
		t.Error("allowlist")
	}
}

// TestSchemaLimit: only the allowlisted limits pass the M2 gate.
func TestSchemaLimit(t *testing.T) {
	for reason, want := range map[string]string{
		"unsupported: pattern: character-class expansion exceeds 1048576 ranges (U-0001)":                       "U-0001",
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

// TestXPathRequestChecks: the engine refuses the xpath requests lyoracle.c op_xpath refuses (an
// unknown or non-string xpath_format, namespaces outside XML, not a list, without a uri) instead
// of running another query.
func TestXPathRequestChecks(t *testing.T) {
	base := map[string]any{"op": "xpath", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "xpf"}},
		"context_options": []any{"no_yanglibrary"}, "format": "json", "data_type": "data", "data": `{"xpf:c": {}}`, "xpath": "1"}
	for name, extra := range map[string]map[string]any{
		"unknown format":          {"xpath_format": "yaml"},
		"format not a string":     {"xpath_format": 1.0},
		"namespaces with json":    {"namespaces": []any{}},
		"namespaces not list":     {"xpath_format": "xml", "namespaces": map[string]any{}},
		"namespace no uri":        {"xpath_format": "xml", "namespaces": []any{map[string]any{"prefix": "p"}}},
		"cur_module not a string": {"cur_module": 1.0},
		"namespaces null":         {"xpath_format": "xml", "namespaces": nil},
		"prefix not a string":     {"xpath_format": "xml", "namespaces": []any{map[string]any{"prefix": 1.0, "uri": "urn:x"}}},
		"uri not a string":        {"xpath_format": "xml", "namespaces": []any{map[string]any{"uri": 1.0}}},
		"empty format":            {"xpath_format": ""},
		"cur_module unknown":      {"cur_module": "nope"},
		"cur_module import-only":  {"cur_module": "xpf-imp"},
		"cur_module empty":        {"cur_module": ""},
	} {
		p := map[string]any{}
		maps.Copy(p, base)
		maps.Copy(p, extra)
		r := Request{ID: name, BaseDir: filepath.Join("corpus", "xpath-format"), Params: p}
		if _, err := (Yang{}).Run(r); err == nil || errors.Is(err, ErrUnsupported) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestXPathRequestCheckOrder covers combined failures: op_xpath parses data before interpreting
// format/namespaces, then validates cur_module before either of them.
func TestXPathRequestCheckOrder(t *testing.T) {
	base := map[string]any{"op": "xpath", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "xpf"}},
		"context_options": []any{"no_yanglibrary"}, "format": "json", "data_type": "data", "xpath": "1"}
	for name, extra := range map[string]map[string]any{
		"data before format":     {"data": "[", "xpath_format": "yaml"},
		"data before namespaces": {"data": "[", "xpath_format": "xml", "namespaces": map[string]any{}},
	} {
		p := map[string]any{}
		maps.Copy(p, base)
		maps.Copy(p, extra)
		r, err := (Yang{}).Run(Request{ID: name, BaseDir: filepath.Join("corpus", "xpath-format"), Params: p})
		if err != nil || r["verdict"] != "data-error" {
			t.Errorf("%s: verdict %v, error %v", name, r["verdict"], err)
		}
	}
	p := map[string]any{}
	maps.Copy(p, base)
	maps.Copy(p, map[string]any{"data": `{"xpf:c": {}}`, "cur_module": "nope", "xpath_format": "yaml",
		"namespaces": map[string]any{}})
	_, err := (Yang{}).Run(Request{ID: "cur module first", BaseDir: filepath.Join("corpus", "xpath-format"), Params: p})
	if err == nil || !strings.Contains(err.Error(), "cur_module nope not implemented") {
		t.Errorf("cur module first: %v", err)
	}
}

// TestXPathIdentityExactRevision keeps the old import-only revision ahead of the implemented one
// in context order. The base identity in a schema-format expression and the stored derived value
// must nevertheless both resolve in the implemented revision.
func TestXPathIdentityExactRevision(t *testing.T) {
	p := map[string]any{
		"op": "xpath", "searchdirs": []any{"schemas"}, "context_options": []any{"no_yanglibrary"},
		"modules": []any{map[string]any{"name": "xpf-user"}, map[string]any{"name": "xpf-rev", "revision": "2021-01-01"}},
		"format":  "json", "data_type": "data", "data": `{"xpf-rev:id": "xpf-rev:added"}`,
		"xpath": "derived-from(/id, 'base')", "xpath_format": "schema", "cur_module": "xpf-rev",
	}
	r, err := (Yang{}).Run(Request{ID: "exact revision", BaseDir: filepath.Join("corpus", "xpath-format"), Params: p})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := r["result"].(map[string]any)
	if r["verdict"] != "valid" || result["value"] != true {
		t.Fatalf("verdict %v, result %v", r["verdict"], result)
	}
}

// TestOpMissingRPC: a reply whose rpc or rpc_file was given but cannot be read fails the request;
// it is never parsed as a standalone reply.
func TestOpMissingRPC(t *testing.T) {
	base := map[string]any{"op": "data", "searchdirs": []any{"rb.1"}, "modules": []any{map[string]any{"name": "rb"}},
		"format": "json", "data_type": "reply", "parse_only": true, "data": `{"rb:r":{}}`}
	for name, extra := range map[string]map[string]any{
		"unreadable rpc_file": {"rpc_file": "no-such-file.json"},
		"rpc not a string":    {"rpc": 42.0},
	} {
		p := maps.Clone(base)
		maps.Copy(p, extra)
		if _, err := (Yang{}).Run(Request{ID: name, BaseDir: "corpus/ut-parser", Params: p}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestEngineRequestErrorsAsOracle covers representative requests the native oracle refuses at
// the request layer (TestOracleRequestErrors), including checks inside parse_one and subtree
// selection after a successful parse.
func TestEngineRequestErrorsAsOracle(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}}}
	}
	seq := base()
	seq["op"] = "sequence"
	seq["steps"] = []any{
		map[string]any{"do": "edit", "delete": "/missing:x"},
		map[string]any{"do": "edit", "merge": "{}", "format": "bogus"},
	}
	dataReq := base()
	dataReq["op"], dataReq["format"], dataReq["data_type"] = "data", "xml", "get"
	nullReq := base()
	nullReq["op"], nullReq["format"], nullReq["data_type"], nullReq["data"] = "data", "xml", "get", nil
	xpathParent := base()
	xpathParent["op"], xpathParent["data_type"], xpathParent["data"], xpathParent["parse_only"] = "xpath", "rpc", `{"pv2:ping":{"n":1}}`, true
	xpathParent["rpc_parent"], xpathParent["xpath"] = "/pv2:ping", "/pv2:ping"
	missingSubtree := base()
	missingSubtree["op"], missingSubtree["data_type"], missingSubtree["data"], missingSubtree["parse_only"] = "data", "data", `{"pv2:c":{"mode":"on"}}`, true
	missingSubtree["print_subtree"] = "grouped"
	trimVars := base()
	trimVars["op"] = "sequence"
	trimVars["steps"] = []any{map[string]any{"do": "trim", "xpath": "/pv2:c", "vars": map[string]any{"b": false, "a": false, "c": 1}}}
	for _, tc := range []struct {
		name, want string
		params     map[string]any
	}{
		{"merge after failing step", "unknown format bogus", seq},
		{"data op without data", "missing data", dataReq},
		{"data op with null data", "missing data", nullReq},
		{"xpath rpc_parent without rpc source", "rpc_parent requires data_type rpc, rpc, and keep_input true", xpathParent},
		{"relative print_subtree missing", `print_subtree "grouped" not found`, missingSubtree},
		{"several invalid trim vars", "trim vars value of a must be a string", trimVars},
	} {
		_, err := (Yang{}).Run(Request{ID: tc.name, BaseDir: filepath.Join("corpus", "protocol-v2"), Params: tc.params})
		if err == nil || errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "request-error") {
			t.Errorf("%s: %v, want a request-error", tc.name, err)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want text %q", tc.name, err, tc.want)
		}
	}
}

func TestEngineDupTermParent(t *testing.T) {
	p := map[string]any{
		"op": "sequence", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
		"steps": []any{
			map[string]any{"do": "parse", "data": `{"pv2:c":{"mode":"on","l":[{"k":"a"}]}}`, "parse_only": true},
			map[string]any{"do": "dup", "node": "/pv2:c", "parent": "/pv2:c/mode", "options": []any{"recursive"}},
			map[string]any{"do": "dump"},
		},
	}
	resp, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: p})
	if err != nil {
		t.Fatal(err)
	}
	steps, ok := resp["steps"].([]any)
	if !ok || len(steps) != 3 {
		t.Fatalf("steps = %v", resp["steps"])
	}
	dup := steps[1].(map[string]any)
	if resp.Verdict() != "invalid" || fmt.Sprint(resp["failed_step"]) != "1" || inventoryRCName(resp["rc"]) != "LY_EINVAL" ||
		inventoryRCName(dup["rc"]) != "LY_EINVAL" || !strings.Contains(fmt.Sprint(dup["diagnostics"]),
		"Invalid argument parent (not an inner node) (lyd_dup_single()).") || !strings.Contains(fmt.Sprint(dup["diagnostics"]), "edit") {
		t.Fatalf("dup failure shape = %v", resp)
	}
	if skipped, _ := steps[2].(map[string]any)["skipped"].(bool); !skipped {
		t.Fatalf("dump was not skipped: %v", steps[2])
	}
}

func TestEngineRelativeFindAndValidatedXPath(t *testing.T) {
	base := func(op string) map[string]any {
		return map[string]any{"op": op, "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}}}
	}
	run := func(t *testing.T, p map[string]any) Response {
		t.Helper()
		resp, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: p})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	p := base("data")
	p["data_type"], p["data"], p["parse_only"], p["print_subtree"] = "data", `{"pv2:c":{"mode":"on"}}`, true, "mode"
	resp := run(t, p)
	if resp.Verdict() != "valid" || !strings.Contains(fmt.Sprint(resp["subtree"]), `pv2:mode`) {
		t.Fatalf("relative print_subtree response = %v", resp)
	}

	p = base("sequence")
	p["steps"] = []any{
		map[string]any{"do": "parse", "data": `{"pv2:c":{"mode":"on","l":[{"k":"a"}]}}`, "parse_only": true},
		map[string]any{"do": "edit", "delete": "mode"}, map[string]any{"do": "dump"},
	}
	resp = run(t, p)
	if resp.Verdict() != "valid" || strings.Contains(fmt.Sprint(resp["tree"]), `mode`) {
		t.Fatalf("relative delete response = %v", resp)
	}

	p = base("xpath")
	p["data_type"], p["data"], p["xpath"] = "rpc", `{"pv2:ping":{"n":1}}`, "/pv2:ping/n"
	resp = run(t, p)
	if resp.Verdict() != "valid" || !strings.Contains(fmt.Sprint(resp["result"]), "/pv2:ping/n") {
		t.Fatalf("validated RPC XPath response = %v", resp)
	}

	p["data"] = `{"pv2:ping":{"n":10}}`
	resp = run(t, p)
	if resp.Verdict() != "data-error" || resp["rc"] != nil || !strings.Contains(fmt.Sprint(resp["diagnostics"]), "validate_op") {
		t.Fatalf("invalid validated RPC XPath response = %v", resp)
	}

	p["data"], p["xpath"] = `{"pv2:c":{"l":[{"k":"a","reset":{"why":"x"}}]}}`, "/pv2:c/pv2:l/pv2:reset"
	resp = run(t, p)
	if resp.Verdict() != "data-error" || !strings.Contains(fmt.Sprint(resp["diagnostics"]), "operation_parent") {
		t.Fatalf("missing operational parent XPath response = %v", resp)
	}
}

// step_insert selects its source tree with input_of(data): absent and null inline/file inputs are
// NULL, so the node lookup is against the retained tree and uses the non-data error text.
func TestSequenceInsertNullInputUsesRetainedTree(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		null bool
	}{
		{"data absent", "data", false},
		{"data null", "data", true},
		{"data_file absent", "data_file", false},
		{"data_file null", "data_file", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			insert := map[string]any{"do": "insert", "node": "/pv2:c/grouped"}
			if tc.null {
				insert[tc.key] = nil
			}
			p := map[string]any{
				"op":         "sequence",
				"searchdirs": []any{"schemas"},
				"modules":    []any{map[string]any{"name": "pv2"}},
				"steps": []any{
					map[string]any{"do": "parse", "data": `{"pv2:c":{"mode":"on","l":[{"k":"a"}]}}`, "parse_only": true},
					insert,
				},
			}
			_, err := (Yang{}).Run(Request{ID: tc.name, BaseDir: filepath.Join("corpus", "protocol-v2"), Params: p})
			want := "insert node /pv2:c/grouped not found (a request-error)"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
}

var (
	yangOnce sync.Once
	yangM    *Manifest
	yangRep  Report
	yangErr  error
)

// yangReport is the corpus run through the Yang engine, shared by the engine gates (schema,
// XPath, validation): one run instead of one per gate, read-only afterwards.
func yangReport(t *testing.T) (*Manifest, Report) {
	t.Helper()
	yangOnce.Do(func() {
		if yangM, yangErr = LoadManifest(manifestPath); yangErr == nil {
			if yangErr = yangM.CheckFiles(true); yangErr == nil {
				yangRep, yangErr = yangM.Compare(Yang{})
			}
		}
	})
	if yangErr != nil {
		t.Fatal(yangErr)
	}
	return yangM, yangRep
}

// lys_find_path skips XML whitespace around the atoms context_path, as lyxp does (the request
// matrix's spaced-abs class: the oracle returns the atoms).
func TestAtomsContextPathWhitespace(t *testing.T) {
	for _, cp := range []string{" /pv2:c/mode", "/pv2:c/mode \t", "\n/pv2:c/mode"} {
		p := map[string]any{"op": "atoms", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
			"context_options": []any{"no_yanglibrary"}, "xpath": "..", "context_path": cp}
		r, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: p})
		if err != nil || r["verdict"] != "valid" {
			t.Errorf("context_path %q: %v, %v", cp, r["verdict"], err)
		}
	}
}

// lyd_new_path checks (path[0] == '/') || parent before tokenizing: with an empty retained tree
// (no parent) a leading space is LY_EINVAL; with a retained parent the original path reaches
// lyd_new_path_ with that parent, which Tree.NewPath cannot take: B relative-set-parent (#255).
func TestSequenceSetLeadingSpace(t *testing.T) {
	base := func(steps ...any) map[string]any {
		return map[string]any{"op": "sequence", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
			"context_options": []any{"no_yanglibrary"}, "steps": append(steps, map[string]any{"do": "dump"})}
	}
	set := map[string]any{"do": "edit", "set": map[string]any{"path": " /pv2:c/mode", "value": "on"}}
	r, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: base(set)})
	if err != nil || r["verdict"] != "invalid" || fmt.Sprint(r["failed_step"]) != "0" {
		t.Fatalf("empty tree: %v, %v; want the step to fail", r, err)
	}
	parse := map[string]any{"do": "parse", "data": `{"pv2:c":{"mode":"off"}}`, "parse_only": true}
	if _, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: base(parse, set)}); inventoryGap(err) != "relative-set-parent" {
		t.Fatalf("retained parent: %v; want B relative-set-parent", err)
	}
}

// An explicit empty revision is rejected with the parsed module's revision (an inline revision
// statement, a revision inside a comment ignored). The check precedes includes, imports, and
// compilation, including when those later phases would fail.
func TestEmptyRevisionProbe(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"inline.yang": "module inline { yang-version 1.1; namespace \"urn:inline\"; prefix m; revision 2020-01-01;\n" +
			"  /* revision 2099-01-01; */ leaf x { type string; } }\n",
		"sub/nested.yang": "module nested { yang-version 1.1; namespace \"urn:nested\"; prefix n; leaf bad { type uint8; default 256; } }\n",
		"warning.yang": "module warning { yang-version 1.1; namespace \"urn:w\"; prefix w;\n" +
			"  container top { grouping g { leaf x { type string; } } leaf y { type string; } } }\n",
		"missing-import.yang": "module missing-import { yang-version 1.1; namespace \"urn:mi\"; prefix mi; import nosuch { prefix ns; } }\n",
		"broken-import.yang": "module broken-import { yang-version 1.1; namespace \"urn:bi\"; prefix bi; " +
			"import broken-dependency { prefix bd; } }\n",
		"broken-dependency.yang": "module broken-dependency { yang-version 1.1; namespace \"urn:bd\"; prefix bd; " +
			"leaf bad { type ; } }\n",
		"parse-error.yang": "module parse-error { yang-version 1.1; namespace \"urn:pe\"; prefix pe; " +
			"include parse-error-sub; }\n",
		"parse-error-sub.yang": "submodule parse-error-sub { yang-version 1.1; belongs-to parse-error { prefix pe; } " +
			"leaf bad { type ; } }\n",
	}
	for name, src := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(name string) (Response, error) {
		return (Yang{}).Run(Request{BaseDir: dir, Params: map[string]any{"op": "schema", "searchdirs": []any{"."},
			"context_options": []any{"no_yanglibrary"}, "modules": []any{map[string]any{"name": name, "revision": ""}}}})
	}
	for name, actual := range map[string]string{
		"inline":         "2020-01-01",
		"nested":         "none",
		"warning":        "none",
		"missing-import": "none",
		"broken-import":  "none",
		"parse-error":    "none",
	} {
		r, err := run(name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		m, _ := r["modules"].([]any)[0].(map[string]any)
		ds, _ := m["diagnostics"].([]any)
		want := fmt.Sprintf(`parsed with the wrong revision (%q instead "")`, actual)
		if len(ds) != 2 || m["phase"] != "parse" || !strings.Contains(fmt.Sprint(ds[0]), want) {
			t.Errorf("%s module entry = %v", name, m)
		}
	}
	// With no requested module to parse, there is no parsed revision to reject.
	if r, err := run("nosuch"); err != nil || r["verdict"] != "invalid" {
		t.Errorf("nosuch: response = %v, err = %v", r, err)
	}
}

// A relative set under a retained parent is unsupported (U-0110 relative-set-parent, #255):
// Tree.NewPath has no parent argument, and rewriting the path diverges from lyd_new_path's
// diagnostics; a whitespace-led path too, since trimming it shifts libyang's positions. Without a
// retained tree a relative path keeps lyd_new_path's LY_EINVAL argument check.
func TestRelativeSetParent(t *testing.T) {
	run := func(module string, steps ...any) (Response, error) {
		return (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: map[string]any{
			"op": "sequence", "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": module}}, "steps": steps}})
	}
	set := func(path string) map[string]any {
		return map[string]any{"do": "edit", "set": map[string]any{"path": path, "value": "on"}}
	}
	opaque := map[string]any{"do": "parse", "data": `{"pv2:nosuch":"x"}`, "unknown": "opaque", "parse_only": true}
	for name, tc := range map[string]struct {
		module string
		steps  []any
	}{
		"quoted key parent": {"a", []any{map[string]any{"do": "parse", "data": `{"a:l1":[{"a":"x'y\"z","b":"b","c":"c"}]}`, "parse_only": true}, set("c")}},
		"opaque parent":     {"pv2", []any{opaque, set("mode")}},
		"opaque malformed":  {"pv2", []any{opaque, set("[")}},
		"missing key":       {"pv2", []any{set("/pv2:c/mode"), set("l")}},
		"empty path":        {"pv2", []any{set("/pv2:c/mode"), set("")}},
		"spaced absolute":   {"pv2", []any{set("/pv2:c/mode"), set(" /pv2:c/x")}},
		"spaced malformed":  {"pv2", []any{set("/pv2:c/mode"), set(" /pv2:c/#")}},
	} {
		if _, err := run(tc.module, tc.steps...); !errors.Is(err, ErrUnsupported) || inventoryGap(err) != "relative-set-parent" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	r, err := run("pv2", set("mode"))
	if err != nil || r["rc"].(map[string]any)["name"] != "LY_EINVAL" {
		t.Fatalf("relative set on an empty tree = %v, %v; want LY_EINVAL", r, err)
	}
	if r, err := run("pv2", set("/pv2:c/mode"), set("/pv2:c/x")); err != nil || r["verdict"] != "valid" {
		t.Fatalf("absolute set under a parent = %v, %v", r, err)
	}
}

func TestSequencePrintXMLTypeFailures(t *testing.T) {
	run := func(steps ...any) Response {
		t.Helper()
		r, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "types"), Params: map[string]any{
			"op": "sequence", "searchdirs": []any{"schemas"},
			"modules": []any{map[string]any{"name": "print-failure"}}, "steps": steps,
		}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	inner := map[string]any{"do": "edit", "insert_inner": map[string]any{"module": "print-failure", "name": "c"}}
	term := func(name, value string) map[string]any {
		return map[string]any{"do": "edit", "insert_term": map[string]any{
			"module": "print-failure", "name": name, "parent": "/print-failure:c", "value": value,
		}}
	}
	printXML := map[string]any{"do": "print_xml"}

	meta := run(inner, term("s", "value"), map[string]any{"do": "edit", "new_meta": map[string]any{
		"node": "/print-failure:c/s", "name": "print-failure:select", "value": "'a\x01b'",
	}}, printXML)
	metaStep := meta["steps"].([]any)[3].(map[string]any)
	xml := metaStep["tree"].(map[string]any)["xml"]
	if meta["verdict"] != "valid" || !strings.Contains(xml.(string), `pf:select=""`) {
		t.Fatalf("metadata print = %v", metaStep)
	}
	badMeta := run(inner, term("s", "value"), map[string]any{"do": "edit", "new_meta": map[string]any{
		"node": "/print-failure:c/s", "name": "print-failure:select", "value": "/missing:node",
	}}, printXML)
	badStep := badMeta["steps"].([]any)[3].(map[string]any)
	badDiags := badStep["diagnostics"].([]any)
	if badMeta["verdict"] != "invalid" || badMeta["rc"].(map[string]any)["name"] != "LY_EVALID" ||
		badStep["rc"].(map[string]any)["name"] != "LY_EVALID" || len(badDiags) != 1 ||
		!strings.Contains(badStep["tree"].(map[string]any)["xml"].(string), `pf:select=""`) {
		t.Fatalf("unresolved metadata print = %v", badStep)
	}
	badDiag := badDiags[0].(map[string]any)
	if badDiag["phase"] != "data" || badDiag["vecode_name"] != "LYVE_DATA" || badDiag["msg"] != `Failed to resolve prefix "missing".` {
		t.Fatalf("unresolved metadata diagnostic = %v", badDiag)
	}

	terminal := run(inner, term("xp", "/missing:node"), printXML)
	terminalStep := terminal["steps"].([]any)[2].(map[string]any)
	ds := terminalStep["diagnostics"].([]any)
	if terminal["verdict"] != "invalid" || terminal["rc"].(map[string]any)["name"] != "LY_EINVAL" || len(ds) != 1 {
		t.Fatalf("terminal print = %v", terminalStep)
	}
	if terminalStep["tree"] != nil {
		t.Fatalf("terminal print_xml retained partial tree: %v", terminalStep["tree"])
	}
	d := ds[0].(map[string]any)
	if d["phase"] != "data" || d["vecode_name"] != "LYVE_DATA" || d["msg"] != `Failed to resolve prefix "missing".` {
		t.Fatalf("terminal diagnostic = %v", d)
	}
}

func TestSequenceTargetDiffPrinterKeepsItsContext(t *testing.T) {
	for _, merge := range []bool{false, true} {
		t.Run(fmt.Sprintf("merge=%v", merge), func(t *testing.T) {
			modules := []any{map[string]any{"name": "print-failure"}}
			diff := map[string]any{
				"do": "diff", "format": "json", "data_type": "data", "parse_only": true,
				"data":   `{"print-failure:c":{"xp":"/missing:node"}}`,
				"target": map[string]any{"searchdirs": []any{"schemas"}, "modules": modules},
				"merge":  merge,
			}
			resp, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "types"), Params: map[string]any{
				"op": "sequence", "searchdirs": []any{"schemas"}, "modules": modules,
				"steps": []any{diff, map[string]any{"do": "edit", "set": map[string]any{
					"path": "/print-failure:c/s", "value": "valid",
				}}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			steps := resp["steps"].([]any)
			gotDiff := steps[0].(map[string]any)
			edit := steps[1].(map[string]any)
			if gotDiff["rc"].(map[string]any)["name"] != "LY_SUCCESS" ||
				!strings.Contains(gotDiff["diff"].(map[string]any)["json"].(string), "/missing:node") {
				t.Fatalf("target diff did not retain the failing printer value: %v", gotDiff)
			}
			if merge && !strings.Contains(gotDiff["new_diff"].(map[string]any)["json"].(string), "/missing:node") {
				t.Fatalf("new_diff did not retain the failing printer value: %v", gotDiff)
			}
			if resp["verdict"] != "valid" || edit["rc"].(map[string]any)["name"] != "LY_SUCCESS" ||
				len(edit["diagnostics"].([]any)) != 0 {
				t.Fatalf("target diff printer diagnostics reached original context: %v", resp)
			}
		})
	}
}

// print_tree ignores printer return codes and keeps any completed output
// (lyoracle.c:1370-1387). The same behavior applies to dump and op_data.
func TestPrintTreeKeepsPrinterDiagnosticOutput(t *testing.T) {
	base := filepath.Join("corpus", "types")
	common := map[string]any{"searchdirs": []any{"schemas"},
		"modules": []any{map[string]any{"name": "print-failure"}}}
	seq := map[string]any{"op": "sequence", "searchdirs": common["searchdirs"], "modules": common["modules"],
		"steps": []any{
			map[string]any{"do": "edit", "insert_inner": map[string]any{"module": "print-failure", "name": "c"}},
			map[string]any{"do": "edit", "insert_term": map[string]any{"module": "print-failure", "name": "s", "parent": "/print-failure:c", "value": "value"}},
			map[string]any{"do": "edit", "new_meta": map[string]any{"node": "/print-failure:c/s", "name": "print-failure:select", "value": "/missing:node"}},
			map[string]any{"do": "dump"},
		}}
	r, err := (Yang{}).Run(Request{BaseDir: base, Params: seq})
	if err != nil || r["verdict"] != "valid" {
		t.Fatalf("dump: %v, %v", r, err)
	}
	step := r["steps"].([]any)[3].(map[string]any)
	if xml := step["tree"].(map[string]any)["xml"].(string); !strings.Contains(xml, `pf:select=""`) {
		t.Fatalf("dump XML: %q", xml)
	}
	setup := seq["steps"].([]any)
	// The dump does not collect its XML printer error. free_meta is a void API
	// followed by collect(ctx, ..., "edit"), so the stored error fails that step.
	seq["steps"] = append(append([]any(nil), setup...),
		map[string]any{"do": "edit", "free_meta": map[string]any{"node": "/print-failure:c/s", "name": "print-failure:select"}},
		map[string]any{"do": "dump"})
	r, err = (Yang{}).Run(Request{BaseDir: base, Params: seq})
	if err != nil {
		t.Fatalf("dump then free_meta: %v", err)
	}
	steps := r["steps"].([]any)
	free := steps[4].(map[string]any)
	if r["verdict"] != "invalid" || fmt.Sprint(r["failed_step"]) != "4" || free["rc"].(map[string]any)["name"] != "LY_EVALID" {
		t.Fatalf("pending printer diagnostic did not fail free_meta: %v", r)
	}
	ds := free["diagnostics"].([]any)
	if len(ds) != 1 || ds[0].(map[string]any)["phase"] != "edit" ||
		ds[0].(map[string]any)["msg"] != `Failed to resolve prefix "missing".` {
		t.Fatalf("free_meta diagnostics = %v", ds)
	}
	if steps[5].(map[string]any)["skipped"] != true {
		t.Fatalf("step after failed free_meta ran: %v", steps[5])
	}
	// links reads the tree without collect(); print_xml collects its own printer
	// item together with the earlier dump item.
	seq["steps"] = append(append([]any(nil), setup...),
		map[string]any{"do": "links"},
		map[string]any{"do": "edit", "free_meta": map[string]any{"node": "/print-failure:c/s", "name": "print-failure:select"}})
	r, err = (Yang{}).Run(Request{BaseDir: base, Params: seq})
	if err != nil || fmt.Sprint(r["failed_step"]) != "5" ||
		len(r["steps"].([]any)[4].(map[string]any)["diagnostics"].([]any)) != 0 ||
		len(r["steps"].([]any)[5].(map[string]any)["diagnostics"].([]any)) != 1 {
		t.Fatalf("links consumed a pending printer diagnostic: %v, %v", r, err)
	}
	seq["steps"] = append(append([]any(nil), setup...), map[string]any{"do": "print_xml"})
	r, err = (Yang{}).Run(Request{BaseDir: base, Params: seq})
	if err != nil || fmt.Sprint(r["failed_step"]) != "4" ||
		len(r["steps"].([]any)[4].(map[string]any)["diagnostics"].([]any)) != 2 {
		t.Fatalf("print_xml did not collect both printer diagnostics: %v, %v", r, err)
	}
	dataReq := map[string]any{"op": "data", "searchdirs": common["searchdirs"], "modules": common["modules"],
		"data_type": "data", "format": "json", "parse_only": true,
		"data": `{"print-failure:c":{"xp":"/missing:node"}}`}
	r, err = (Yang{}).Run(Request{BaseDir: base, Params: dataReq})
	if err != nil || r["verdict"] != "valid" {
		t.Fatalf("data print: %v, %v", r, err)
	}
	golden, err := LoadGolden(filepath.Join(base, "golden", "xpath10-data-print-unresolved-prefix.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := r["tree"].(map[string]any)["xml"], golden["tree"].(map[string]any)["xml"]; got != want {
		t.Fatalf("data XML = %q, want committed golden %q", got, want)
	}
}
