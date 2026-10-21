// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestClassification(t *testing.T) {
	valid := Response{"verdict": "valid", "tree": map[string]any{"json": "{}\n", "xml": ""}}
	for _, tc := range []struct {
		name                      string
		want, got                 Response
		err                       error
		artefact, category, class string
	}{
		{name: "match", want: valid, got: valid, category: "MATCH", class: "equal"},
		{name: "matching-request-error", want: Response{"verdict": "request-error", "request_error": "dup node /pv2:c not found", "protocol": "v2", "modules": []any{}, "steps": []any{}}, err: fmt.Errorf("dup node /pv2:c not found (a request-error)"), category: "MATCH", class: "equal"},
		{name: "wrong-tree", want: valid, got: Response{"verdict": "valid", "tree": map[string]any{"json": "{\"wrong\":true}\n", "xml": ""}}, category: "A", class: "output"},
		{name: "wrong-xml", want: valid, got: Response{"verdict": "valid", "tree": map[string]any{"json": "{}\n", "xml": "<wrong/>"}}, category: "A", class: "output"},
		{name: "missing-diff", want: Response{"verdict": "valid", "diff": map[string]any{"json": "{}"}}, got: Response{"verdict": "valid"}, category: "A", class: "output"},
		{name: "wrong-diagnostic", want: Response{"verdict": "invalid", "diagnostics": []any{map[string]any{"msg": "native"}}}, got: Response{"verdict": "invalid", "diagnostics": []any{map[string]any{"msg": "wrong"}}}, category: "A", class: "execution"},
		{name: "envelope", want: valid, err: fmt.Errorf("%w: notification envelope data_type notif-netconf (U-0110)", ErrUnsupported), category: "B", class: "notification-netconf"},
		{name: "gap-before-oracle-error", want: Response{"verdict": "request-error", "request_error": "missing data"}, err: fmt.Errorf("%w: notification envelope data_type notif-restconf (U-0110)", ErrUnsupported), category: "A", class: "request-error"},
		{name: "new-gap", want: valid, err: fmt.Errorf("%w: new capability (U-0110)", ErrUnsupported), category: "B", class: "unclassified"},
		{name: "new-gap-existing-id", want: valid, err: fmt.Errorf("%w: new capability (U-0106)", ErrUnsupported), category: "B", class: "unclassified"},
		{name: "concrete-error-is-not-gap", want: valid, err: fmt.Errorf("notification envelope data_type notif-netconf (U-0110)"), category: "A", class: "engine-error"},
		{name: "artefact-even-when-matching", want: valid, got: valid, artefact: "changed schema setup", category: "C", class: "outside-subject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			category, class := inventoryClassification(tc.want, tc.got, tc.err, tc.artefact)
			if category != tc.category || class != tc.class {
				t.Fatalf("%s/%s, want %s/%s", category, class, tc.category, tc.class)
			}
		})
	}
}

func TestUnsupportedRequestErrorOrdering(t *testing.T) {
	netconf := fmt.Errorf("%w: notification envelope data_type notif-netconf (conformance engine capability gap, U-0110)", ErrUnsupported)
	restconf := fmt.Errorf("%w: notification envelope data_type notif-restconf (conformance engine capability gap, U-0110)", ErrUnsupported)
	rpc := fmt.Errorf("%w: an rpc request with data_type rpc (ParseOpOptions.Request is for replies only, deviations.md U-0106)", ErrUnsupported)
	anyDirect := fmt.Errorf("%w: native pointer-level any-value calls have no public Go equivalent, U-0112", ErrUnsupported)
	for _, tc := range []struct {
		name, oracleError, category, class string
		request                            map[string]any
		err                                error
	}{
		{"netconf-subtree-lookup", `print_subtree "/pv2:c" not found`, "B", "notification-netconf", map[string]any{"op": "data", "data_type": "notif-netconf"}, netconf},
		{"restconf-context-lookup", "context_path /pv2:c must select exactly one node", "B", "notification-restconf", map[string]any{"op": "xpath", "data_type": "notif-restconf"}, restconf},
		{"netconf-data-node-lookup", "diff data_node /pv2:c not found", "B", "notification-netconf", map[string]any{"op": "sequence", "steps": []any{map[string]any{"do": "diff", "data_type": "notif-netconf"}}}, netconf},
		{"restconf-module-lookup", "cur_module bogus not implemented", "B", "notification-restconf", map[string]any{"op": "xpath", "data_type": "notif-restconf"}, restconf},
		{"netconf-namespace-shape", "namespaces must be a list", "B", "notification-netconf", map[string]any{"op": "xpath", "data_type": "notif-netconf"}, netconf},
		{"restconf-namespace-entry", `field "prefix" must be a string`, "B", "notification-restconf", map[string]any{"op": "xpath", "data_type": "notif-restconf"}, restconf},
		{"netconf-namespace-uri", `field "uri" must be a string`, "B", "notification-netconf", map[string]any{"op": "xpath", "data_type": "notif-netconf"}, netconf},
		{"restconf-xpath-format", "unknown xpath_format bogus", "B", "notification-restconf", map[string]any{"op": "xpath", "data_type": "notif-restconf"}, restconf},
		{"unknown-error-text", "unknown flag in ", "B", "notification-netconf", map[string]any{"op": "data", "data_type": "notif-netconf"}, netconf},
		{"rpc-subtree-lookup", `print_subtree "/pv2:c" not found`, "B", "operation-parent", map[string]any{"op": "data", "data_type": "rpc", "rpc": inventoryRPC}, rpc},
		{"rpc-parent-lookup", "rpc_parent /pv2:missing not found", "B", "operation-parent", map[string]any{"op": "data", "data_type": "rpc", "rpc": inventoryRPC}, rpc},
		{"preparse-input", "missing data", "A", "request-error", map[string]any{"op": "data", "data_type": "notif-netconf"}, netconf},
		{"preparse-subtree-type", `field "print_subtree" must be a string`, "A", "request-error", map[string]any{"op": "data", "data_type": "notif-netconf"}, netconf},
		{"preparse-context-type", `field "context_path" must be a string`, "A", "request-error", map[string]any{"op": "xpath", "data_type": "notif-restconf"}, restconf},
		{"preparse-xpath-type", `field "xpath_format" must be a string`, "A", "request-error", map[string]any{"op": "xpath", "data_type": "notif-restconf"}, restconf},
		{"preparse-diff-selector-type", `field "data_node" must be a string`, "A", "request-error", map[string]any{"op": "sequence", "steps": []any{map[string]any{"do": "diff", "data_type": "notif-netconf"}}}, netconf},
		{"preparse-parse-flag", `unknown flag in "parse_options"`, "A", "request-error", map[string]any{"op": "data", "data_type": "notif-netconf"}, netconf},
		{"preparse-input-conflict", `both inline and _file given for "data"`, "A", "request-error", map[string]any{"op": "data", "data_type": "notif-netconf"}, netconf},
		{"any-direct-missing-target", "equal_repeated_collisions needs target", "A", "request-error", map[string]any{"op": "sequence", "steps": []any{map[string]any{"do": "any_direct", "case": "equal_repeated_collisions"}}}, anyDirect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := Response{"verdict": "request-error", "request_error": tc.oracleError}
			category, class := inventoryCaseClassification(tc.request, want, nil, nil, tc.err, "")
			if category != tc.category || class != tc.class {
				t.Fatalf("%s/%s, want %s/%s", category, class, tc.category, tc.class)
			}
		})
	}
	if inventoryPreparseRequestError(`unknown flag in "diff_options"`) {
		t.Fatal("diff_options is checked after both inputs parse")
	}
}

func TestUnknownPrintOptionUnsupported(t *testing.T) {
	_, _, err := requestPrintOptions(map[string]any{"print_options": []any{"bogus"}}, 0)
	if got := inventoryGap(err); got != "unclassified" {
		t.Fatalf("inventoryGap() = %q, want unclassified (error: %v)", got, err)
	}
}

func TestAnyDirectMatrixGapClassification(t *testing.T) {
	step := func(do string, fields map[string]any) map[string]any {
		st := map[string]any{"do": do}
		maps.Copy(st, fields)
		return map[string]any{"op": "sequence", "steps": []any{st}}
	}
	for _, tc := range []struct {
		name, reason, class string
		request             map[string]any
	}{
		{"direct", "native pointer-level any-value calls have no public Go equivalent, U-0112", "any-direct", step("any_direct", nil)},
		{"direct-mixed-merge", "native pointer-level any-value calls have no public Go equivalent, U-0112", "any-direct", step("any_direct", map[string]any{"case": "merge_mixed_into_tree"})},
		{"opaque-source", "opaque source construction has no public Go equivalent, U-0107", "any-direct-opaque-source", step("any_direct", map[string]any{"case": "dup_siblings_into_empty_anydata"})},
		{"compare", "compare insert_opaq (lyd_new_opaq / lyd_new_opaq2 are exported in M5 with the envelopes, U-0107)", "compare-insert-opaq", step("compare", map[string]any{"insert_opaq": map[string]any{"name": "n"}})},
		{"wrong-step", "native pointer-level any-value calls have no public Go equivalent, U-0112", "unclassified", step("dup", nil)},
		{"wrong-reason", "native pointer-level any-value calls have no public Go equivalent, U-0112 (changed)", "unclassified", step("any_direct", nil)},
		{"compare-without-opaque", "compare insert_opaq (lyd_new_opaq / lyd_new_opaq2 are exported in M5 with the envelopes, U-0107)", "unclassified", step("compare", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("%w: %s", ErrUnsupported, tc.reason)
			category, class := inventoryCaseClassification(tc.request, Response{"verdict": "valid"}, nil, nil, err, "")
			if category != "B" || class != tc.class {
				t.Fatalf("%s/%s, want B/%s", category, class, tc.class)
			}
		})
	}
}

func TestInventoryDupTermParent(t *testing.T) {
	request := func(parent string, options ...any) map[string]any {
		return map[string]any{
			"op": "sequence",
			"steps": []any{
				map[string]any{"do": "parse", "data": `{"pv2:c":{"mode":"on","l":[{"k":"a"}],"ll":["a"]}}`},
				map[string]any{"do": "dup", "node": "/pv2:c", "parent": parent, "options": options},
				map[string]any{"do": "dump"},
			},
		}
	}
	for _, path := range []string{"/pv2:c/mode", "mode", "/pv2:c/l[k='a']/k", "/pv2:c/ll[.='a']"} {
		if !inventoryDupTermParent(request(path)) {
			t.Errorf("term parent %q did not match", path)
		}
	}
	for _, tc := range []struct {
		name string
		p    map[string]any
	}{
		{"inner", request("/pv2:c")},
		{"list", request("/pv2:c/l[k='a']")},
		{"missing-list-instance", request("/pv2:c/l[k='missing']/k")},
		{"missing-leaf-list-instance", request("/pv2:c/ll[.='missing']")},
		{"missing", request("/pv2:c/missing")},
		{"with-parents", request("mode", "with_parents")},
		{"not-sequence", map[string]any{"op": "data"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if inventoryDupTermParent(tc.p) {
				t.Fatal("unexpected crash-class match")
			}
		})
	}
}

func TestInventoryCrashClassification(t *testing.T) {
	p := map[string]any{
		"op": "sequence",
		"steps": []any{
			map[string]any{"do": "parse", "data": `{"pv2:c":{"mode":"on"}}`},
			map[string]any{"do": "dup", "node": "/pv2:c", "parent": "mode"},
		},
	}
	got := Response{
		"verdict": "invalid", "failed_step": 1, "rc": codeJSON("LY_EINVAL"),
		"steps": []any{map[string]any{"do": "parse", "rc": codeJSON("LY_SUCCESS")}, map[string]any{"do": "dup", "rc": codeJSON("LY_EINVAL")}},
	}
	if category, class := inventoryCaseClassification(p, nil, fmt.Errorf("cached native error: oracle transport: signal: segmentation fault (core dumped): "), got, nil, ""); category != "DEV" || class != "dup-term-parent" {
		t.Fatalf("registered crash = %s/%s", category, class)
	}
	if category, class := inventoryCaseClassification(p, nil, fmt.Errorf("oracle transport: signal: killed"), got, nil, "changed setup"); category != "A" || class != "native-crash-unregistered" {
		t.Fatalf("wrong termination text = %s/%s", category, class)
	}
	deadlineSignal := fmt.Errorf("oracle transport: signal: segmentation fault: %w", context.DeadlineExceeded)
	if category, class := inventoryCaseClassification(p, nil, deadlineSignal, got, nil, "changed setup"); category != "A" || class != "native-transport" {
		t.Fatalf("deadline signal = %s/%s", category, class)
	}
	cachedDeadline := fmt.Errorf("cached native error: oracle transport deadline: context deadline exceeded")
	if category, class := inventoryCaseClassification(p, nil, cachedDeadline, got, nil, "changed setup"); category != "A" || class != "native-transport" {
		t.Fatalf("cached deadline = %s/%s", category, class)
	}
	if category, class := inventoryCaseClassification(map[string]any{"op": "data"}, nil, fmt.Errorf("oracle transport: signal: aborted"), nil, nil, ""); category != "A" || class != "native-crash-unregistered" {
		t.Fatalf("unregistered crash = %s/%s", category, class)
	}
	if category, class := inventoryCaseClassification(p, nil, fmt.Errorf("oracle transport: timeout"), got, nil, ""); category != "A" || class != "native-transport" {
		t.Fatalf("transport error = %s/%s", category, class)
	}
	if category, class := inventoryCaseClassification(p, nil, fmt.Errorf("oracle transport: signal: segmentation fault (core dumped): "), got, nil, "changed setup"); category != "C" || class != "outside-subject" {
		t.Fatalf("crashing artefact = %s/%s", category, class)
	}
	op := map[string]any{"op": "data", "data_type": "rpc", "rpc": `{"pv2:ping":{"n":1}}`, "keep_input": true,
		"unknown": "opaque", "rpc_parent": "n", "data": `{"n":1}`}
	gap := fmt.Errorf("%w: rpc input into an existing request parent is not exported (ParseOpOptions.Request is for replies only, U-0106)", ErrUnsupported)
	if category, class := inventoryCaseClassification(op, nil, fmt.Errorf("oracle transport: signal: segmentation fault (core dumped): "), nil, gap, ""); category != "DEV" || class != "op-parse-term-parent" {
		t.Fatalf("registered operation crash = %s/%s", category, class)
	}
	if category, _ := inventoryCaseClassification(op, nil, fmt.Errorf("oracle transport: signal: segmentation fault (core dumped): "), Response{"verdict": "valid"}, nil, ""); category != "A" {
		t.Fatalf("operation crash with a concrete engine result = %s", category)
	}
	for k, v := range map[string]any{"unknown": "reject", "rpc_parent": "/pv2:ping", "keep_input": false} {
		q := maps.Clone(op)
		q[k] = v
		if inventoryOpParseTermParent(q) {
			t.Errorf("op-parse-term-parent matches with %s=%v", k, v)
		}
	}
	wrong := maps.Clone(got)
	wrong["rc"] = codeJSON("LY_SUCCESS")
	if category, class := inventoryCaseClassification(p, nil, fmt.Errorf("oracle transport: signal: segmentation fault (core dumped): "), wrong, nil, ""); category != "A" || class != "native-crash-result" {
		t.Fatalf("wrong deviation result = %s/%s", category, class)
	}
}

// Runs without native evidence: generator isolation must not depend on whether
// the oracle/cache is available. The differential uses this same C check.
func TestRequestMatrixIsolation(t *testing.T) {
	files := t.TempDir()
	count := 0
	for _, seed := range inventorySeeds() {
		for _, row := range inventoryRows(seed.scope) {
			for _, value := range inventoryValues(row, seed) {
				modes := []bool{false}
				if len(seed.path) > 0 {
					modes = append(modes, true)
				}
				for _, skipped := range modes {
					p := inventoryMutation(t, seed, row, value, files, skipped)
					if reason := inventoryArtefact(seed, row, p, skipped); reason != "" {
						t.Fatalf("C: %s/%s/%s: %s", seed.name, row.key, value.name, reason)
					}
					count++
				}
			}
		}
	}
	t.Logf("%d generated requests, C=0", count)
	// A leaked schema mutation must fail even when the declared row is data.
	seed := inventorySeed{name: "data", scope: "data", params: inventoryCommon("data")}
	p := inventoryClone(seed.params)
	p["base_dir"] = "."
	p["modules"].([]any)[0].(map[string]any)["revision"] = ""
	if inventoryArtefact(seed, inventoryRow{key: "data"}, p, false) == "" {
		t.Fatal("schema setup leak was not classified C")
	}
}

func TestRepeatedLoadSeedMutatesSecondModule(t *testing.T) {
	var seed inventorySeed
	for _, candidate := range inventorySeeds() {
		if candidate.name == "schema/repeated-load" {
			seed = candidate
			break
		}
	}
	if seed.params == nil {
		t.Fatal("schema/repeated-load seed not found")
	}
	values := map[string]inventoryValue{
		"modules[].revision": {name: "empty", value: ""},
		"modules[].features": {name: "feature", value: []any{"extra"}},
	}
	for _, row := range inventoryRows(seed.scope) {
		value, ok := values[row.key]
		if !ok {
			continue
		}
		p := inventoryMutation(t, seed, row, value, t.TempDir(), false)
		modules := p["modules"].([]any)
		if !sameJSON(modules[0], map[string]any{"name": "pv2"}) {
			t.Errorf("%s changed first module: %#v", row.key, modules[0])
		}
		second := modules[1].(map[string]any)
		field := strings.TrimPrefix(row.key, "modules[].")
		if !sameJSON(second[field], value.value) {
			t.Errorf("%s second module value = %#v, want %#v", row.key, second[field], value.value)
		}
		delete(values, row.key)
	}
	if len(values) != 0 {
		t.Fatalf("%d repeated-load rows not exercised", len(values))
	}
}

// Every deferred class carries its predicate, native reproducer, explanation and issue; a
// deferred result is never MATCH, and only a concrete mismatch can be deferred.
func TestInventoryDeferredClassification(t *testing.T) {
	for name, d := range inventoryDeferredClasses {
		if !d.complete() {
			t.Errorf("deferred class %s lacks its predicate, reproducer, explanation or issue", name)
		}
	}
	want := Response{"verdict": "request-error", "request_error": "a"}
	got := Response{"verdict": "request-error", "request_error": "a"}
	p := map[string]any{"op": "data", "probe": true}
	if category, _ := inventoryCaseClassification(p, Response{"verdict": "valid"}, nil, Response{"verdict": "invalid"}, nil, ""); category != "A" {
		t.Fatalf("mismatch without a deferred class = %s", category)
	}
	probe := func(q map[string]any, _, _ Response, _ error) bool { return q["probe"] == true }
	inventoryDeferredClasses["probe"] = inventoryDeferredClass{match: probe, reproducer: "{}", why: "test", issue: 247}
	t.Cleanup(func() {
		delete(inventoryDeferredClasses, "probe")
		delete(inventoryDeferredClasses, "probe-incomplete")
	})
	if category, class := inventoryCaseClassification(p, Response{"verdict": "valid"}, nil, Response{"verdict": "invalid"}, nil, ""); category != "DEFERRED" || class != "probe" {
		t.Fatalf("registered envelope-only mismatch = %s/%s", category, class)
	}
	if category, _ := inventoryCaseClassification(p, want, nil, got, nil, ""); category == "DEFERRED" {
		t.Fatal("an agreeing result was deferred")
	}
	if category, _ := inventoryCaseClassification(p, Response{"verdict": "valid"}, nil, Response{"verdict": "invalid"}, nil, "changed setup"); category != "C" {
		t.Fatalf("artefact = %s, want C", category)
	}
	if category, _ := inventoryCaseClassification(p, nil, fmt.Errorf("oracle transport: timeout"), nil, nil, ""); category != "A" {
		t.Fatalf("transport failure = %s, want A", category)
	}
	inventoryDeferredClasses["probe-incomplete"] = inventoryDeferredClass{match: probe}
	if category, class := inventoryCaseClassification(p, Response{"verdict": "valid"}, nil, Response{"verdict": "invalid"}, nil, ""); category != "DEFERRED" || class != "probe" {
		t.Fatalf("an incomplete class must not count as a second match: %s/%s", category, class)
	}
	delete(inventoryDeferredClasses, "probe")
	if category, _ := inventoryCaseClassification(p, Response{"verdict": "valid"}, nil, Response{"verdict": "invalid"}, nil, ""); category != "A" {
		t.Fatalf("incomplete class deferred a mismatch: %s", category)
	}
}

// A dup under a parent that is not an instance of the node's schema parent completes natively;
// the engine refuses it (D-0124), which the matrix counts as DEV. The native side is the
// fixture's amd64 golden. A wrongly refused valid parent, a corrupted earlier step or an altered
// refusal is A.
func TestInventoryDupMismatchedParentDeviation(t *testing.T) {
	for _, id := range []string{"dup-mismatched-parent", "dup-mismatched-parent-hashed", "dup-ancestor-into-itself", "dup-ancestor-into-itself-hashed"} {
		b, err := os.ReadFile(filepath.Join("corpus", "protocol-v2", "golden", "seq-"+id+".json")) //nolint:gosec // fixed corpus golden names
		if err != nil {
			t.Fatal(err)
		}
		want, err := ParseResponse(b)
		if err != nil {
			t.Fatal(err)
		}
		var f Fixture
		m, err := LoadManifest(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range m.Fixtures {
			if x.ID == "seq/"+id {
				f = x
			}
		}
		got, err := (Yang{}).Run(Request{BaseDir: filepath.Join("corpus", "protocol-v2"), Params: f.Request})
		if category, class := inventoryCaseClassification(f.Request, want, nil, got, err, ""); category != "DEV" || class != "dup-mismatched-parent" {
			t.Errorf("%s: %s/%s, want DEV/dup-mismatched-parent", id, category, class)
		}
		a := func(name string, p map[string]any, got Response) {
			t.Helper()
			if category, _ := inventoryCaseClassification(p, want, nil, got, nil, ""); category != "A" {
				t.Errorf("%s: %s classified %s, want A", id, name, category)
			}
		}
		// the JSON round trip the negative cases use keeps a legitimate refusal DEV
		if category, _ := inventoryCaseClassification(f.Request, want, nil, inventoryClone(got), nil, ""); category != "DEV" {
			t.Errorf("%s: cloned engine response classified %s", id, category)
		}
		// a different mismatch on the same request
		a("unrelated mismatch", f.Request, Response{"verdict": "valid"})
		// an earlier step corrupted before a legitimate refusal: diagnostics, then the tree
		bad := inventoryClone(map[string]any(got))
		first := list(bad["steps"])[0].(map[string]any)
		first["diagnostics"] = append(list(first["diagnostics"]), map[string]any{"msg": "corrupt"})
		a("corrupted earlier diagnostics", f.Request, bad)
		bad = inventoryClone(map[string]any(got))
		list(bad["steps"])[0].(map[string]any)["typed"] = []any{}
		a("corrupted earlier tree", f.Request, bad)
		// the refused step's diagnostic altered
		bad = inventoryClone(map[string]any(got))
		dupStep := list(bad["steps"])[1].(map[string]any)
		dupStep["diagnostics"] = []any{map[string]any{"msg": "Something else (D-0124)."}}
		a("altered refusal", f.Request, bad)
		// a valid parent (the node's own data parent) refused as D-0124: the precondition fails
		if id == "dup-mismatched-parent" || id == "dup-mismatched-parent-hashed" {
			p := inventoryClone(f.Request)
			list(p["steps"])[1].(map[string]any)["parent"] = "/pv2-edit:c/np"
			forged := inventoryClone(map[string]any(got))
			list(forged["steps"])[1].(map[string]any)["diagnostics"] = []any{map[string]any{
				"msg": `Duplicating "/pv2-edit:c/np/d" under "/pv2-edit:c/np", which is not an instance of its schema parent (D-0124).`}}
			a("valid parent refused", p, forged)
		}
	}
}
