// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"text/tabwriter"
	"time"

	"github.com/vibe-ports/yang"
)

// TestRequestValidationDifferential is generated exclusively from the C-key
// inventory. It compares execution outcomes, including diagnostic messages and
// codes, rather than merely accepting every ErrUnsupported as a valid request.
func TestRequestValidationDifferential(t *testing.T) {
	oracle, base := requestOracle(t)
	files := base
	var cases []generatedCase
	for _, seed := range inventorySeeds() {
		for _, row := range inventoryRows(seed.scope) {
			for _, value := range inventoryValues(row, seed) {
				modes := []bool{false}
				if len(seed.path) > 0 {
					modes = append(modes, true)
				}
				for _, skipped := range modes {
					p := inventoryMutation(t, seed, row, value, files, skipped)
					cases = append(cases, generatedCase{
						seed: seed.name, key: row.key, value: value.name, skipped: skipped,
						params: p, artefact: inventoryArtefact(seed, row, p, skipped),
					})
				}
			}
		}
	}

	runInventoryCases(t, oracle, base, newInventoryContextCache(), cases)
}

type generatedCase struct {
	seed, key, value string
	skipped          bool
	params           map[string]any
	artefact         string
}
type caseResult struct {
	want, got      Response
	nativeErr, err error
}

func runInventoryCases(t *testing.T, oracle *requestOracleRunner, base string, contexts *inventoryContextCache, cases []generatedCase) {
	t.Helper()
	started := time.Now()

	// A native process and an engine run are one job. Results occupy their
	// generation-order slot so scheduling cannot change reports or class counts.
	results := make([]caseResult, len(cases))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		workers.Go(func() {
			for i := range jobs {
				p := cases[i].params
				results[i].want, results[i].nativeErr = inventoryNativeResponse(oracle, base, p)
				results[i].got, results[i].err = inventoryRunWithContextCache(
					Request{BaseDir: base, Params: p}, contexts)
			}
		})
	}
	for i := range cases {
		jobs <- i
	}
	close(jobs)
	workers.Wait()

	var mismatches, exceptions strings.Builder
	tw := tabwriter.NewWriter(&mismatches, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "CASE\tKEY\tVALUE\tSKIPPED PROBE\tORACLE\tENGINE"); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for i, c := range cases {
		result := results[i]
		category, class := inventoryCaseClassification(c.params, result.want, result.nativeErr, result.got, result.err, c.artefact)
		counts[category]++
		counts[category+"/"+class]++
		if category == "B" {
			gap, ok := inventoryGapClasses[class]
			if !ok {
				t.Errorf("undocumented B class: %s/%s/%s: %v", c.seed, c.key, c.value, result.err)
			}
			fmt.Fprintf(&exceptions, "%s/%s/%s skipped=%v: B %s %s: %v\n", c.seed, c.key, c.value, c.skipped, class, gap.id, result.err)
		}
		if category == "DEFERRED" {
			d := inventoryDeferredClasses[class]
			fmt.Fprintf(&exceptions, "%s/%s/%s skipped=%v: DEFERRED %s #%d: %v\n", c.seed, c.key, c.value, c.skipped, class, d.issue, result.err)
		}
		if category == "DEV" {
			fmt.Fprintf(&exceptions, "%s/%s/%s skipped=%v: DEV %s %s: %v\n", c.seed, c.key, c.value, c.skipped, class, inventoryDevID(class), result.nativeErr)
		}
		if category == "A" || category == "C" {
			wb, _ := json.Marshal(result.want)
			gb, _ := json.Marshal(result.got)
			if _, writeErr := fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%s\t%s %v [%s/%s: %s]\n", c.seed, c.key, c.value, c.skipped, wb, gb, result.err, category, class, c.artefact); writeErr != nil {
				t.Fatal(writeErr)
			}
		}
	}
	if err := tw.Flush(); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	t.Logf("%d generated requests: MATCH=%d A=%d B=%d C=%d DEV=%d DEFERRED=%d", len(cases), counts["MATCH"], counts["A"], counts["B"], counts["C"], counts["DEV"], counts["DEFERRED"])
	t.Logf("%d workers, %d distinct schema contexts: %s total, %s/request", runtime.GOMAXPROCS(0), contexts.Len(), elapsed, elapsed/time.Duration(len(cases)))
	for _, key := range slices.Sorted(maps.Keys(counts)) {
		if strings.Contains(key, "/") {
			t.Logf("%s: %d", key, counts[key])
		}
	}
	if path := os.Getenv("YANG_REQUEST_MISMATCHES"); path != "" {
		if err := os.WriteFile(path, []byte(mismatches.String()+"\nB GAP / DEV CRASH / DEFERRED (separate from matches):\n"+exceptions.String()), 0600); err != nil { //nolint:gosec // Explicit test-runner ledger destination, never request input.
			t.Fatal(err)
		}
	}
	if counts["A"] != 0 || counts["C"] != 0 {
		t.Errorf("A=%d C=%d; both must be zero:\n%s", counts["A"], counts["C"], mismatches.String())
	}
}

// inventoryContextCache builds each distinct schema context once at each build-call
// position in a request. Context.Schema is an immutable snapshot; the response maps
// are mutable engine scaffolding, so every caller receives fresh top-level maps while
// sharing only the context with the same position in an independent request.
type inventoryContextCache struct {
	mu      sync.Mutex
	entries map[string]*inventoryContextCall
}

type inventoryContextCall struct {
	ready   chan struct{}
	schema  *yang.Schema
	resp    map[string]any
	mods    []map[string]any
	verdict string
	err     error
}

func newInventoryContextCache() *inventoryContextCache {
	return &inventoryContextCache{entries: map[string]*inventoryContextCall{}}
}

type inventoryContextView struct {
	cache *inventoryContextCache
	call  int
}

func (c *inventoryContextCache) request() *inventoryContextView {
	return &inventoryContextView{cache: c}
}

func (v *inventoryContextView) build(r Request) (*yang.Schema, map[string]any, []map[string]any, string, error) {
	key, err := inventorySchemaContextKey(r)
	if err != nil {
		return nil, nil, nil, "", err
	}
	key = fmt.Sprintf("%d\x00%s", v.call, key)
	v.call++
	c := v.cache
	c.mu.Lock()
	call, ok := c.entries[key]
	if !ok {
		call = &inventoryContextCall{ready: make(chan struct{})}
		c.entries[key] = call
	}
	c.mu.Unlock()
	if ok {
		<-call.ready
		return cloneInventoryContext(call)
	}
	func() {
		defer func() {
			if value := recover(); value != nil {
				call.err = fmt.Errorf("engine panic: %v", value)
			}
			close(call.ready)
		}()
		call.schema, call.resp, call.mods, call.verdict, call.err = buildSchemaContext(r)
	}()
	return cloneInventoryContext(call)
}

func (c *inventoryContextCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func inventorySchemaContextKey(r Request) (string, error) {
	p := map[string]any{"base_dir": r.BaseDir}
	for _, key := range []string{"context_options", "searchdirs", "modules"} {
		if value, ok := r.Params[key]; ok {
			p[key] = value
		}
	}
	b, err := json.Marshal(p)
	return string(b), err
}

func cloneInventoryContext(call *inventoryContextCall) (*yang.Schema, map[string]any, []map[string]any, string, error) {
	var resp map[string]any
	if call.resp != nil {
		resp = maps.Clone(call.resp)
	}
	var mods []map[string]any
	if call.mods != nil {
		mods = make([]map[string]any, len(call.mods))
		for i, mod := range call.mods {
			mods[i] = maps.Clone(mod)
		}
	}
	return call.schema, resp, mods, call.verdict, call.err
}

// Schema setup stays fixed outside schema/context cases. The whole generated
// request is checked, including setup/trailing steps and the skipped-step probe.
func inventoryContextKey(key string) bool {
	switch strings.Split(key, ".")[0] {
	case "base_dir", "context_options", "searchdirs", "modules", "modules[]":
		return true
	}
	return false
}

func inventoryArtefact(seed inventorySeed, row inventoryRow, p map[string]any, skipped bool) string {
	if inventoryContextKey(row.key) && seed.scope != "schema" && !strings.HasPrefix(seed.scope, "target:") {
		return "schema/context mutation outside the schema/context case"
	}
	expected := inventoryClone(seed.params)
	expected["base_dir"] = "."
	actual := p
	if skipped {
		actual = inventoryClone(p)
		steps := actual["steps"].([]any)
		if !sameJSON(steps[0], map[string]any{"do": "edit", "delete": "/missing:x"}) {
			return "changed failure probe"
		}
		original := expected["steps"].([]any)
		index := 0
		if seed.path[1] == "1" {
			index = 1
		}
		actual["steps"] = append(original[:index:index], steps[1:]...)
	}
	if len(seed.path) == 0 {
		// The entire root is the subject, but schema setup is not part of a data op.
		if seed.scope != "schema" {
			for _, key := range []string{"base_dir", "context_options", "searchdirs", "modules"} {
				if !sameJSON(expected[key], actual[key]) {
					return "changed schema setup: " + key
				}
			}
		}
		return ""
	}
	// Replace only the subject in the original request, then compare everything.
	return inventoryOutsideSubject(seed, expected, actual)
}

func inventoryOutsideSubject(seed inventorySeed, expected, actual map[string]any) string {
	index := 0
	if seed.path[1] == "1" {
		index = 1
	}
	steps := expected["steps"].([]any)
	if len(seed.path) == 2 {
		steps[index] = inventorySubject(actual, seed.path)
	} else {
		steps[index].(map[string]any)[seed.path[2]] = inventorySubject(actual, seed.path)
	}
	if !sameJSON(expected, actual) {
		return "changed a field outside the subject"
	}
	return ""
}

func inventoryMutation(t *testing.T, seed inventorySeed, row inventoryRow, value inventoryValue, files string, skipped bool) map[string]any {
	t.Helper()
	p := inventoryClone(seed.params)
	p["base_dir"] = "."
	subject := inventorySubject(p, seed.path)
	if strings.HasPrefix(row.key, "namespaces[].") {
		subject["namespaces"] = []any{map[string]any{"prefix": "pv2", "uri": "urn:vibe-ports:yang:conformance:pv2"}}
	}
	parts := strings.Split(row.key, ".")
	for _, part := range parts[:len(parts)-1] {
		if strings.HasSuffix(part, "[]") {
			k := strings.TrimSuffix(part, "[]")
			items, ok := subject[k].([]any)
			index := 0
			if k == "modules" {
				index = seed.moduleIndex
			}
			if !ok {
				items = nil
			}
			for len(items) <= index {
				items = append(items, map[string]any{})
				subject[k] = items
			}
			child, ok := items[index].(map[string]any)
			if !ok {
				child = map[string]any{}
				items[index] = child
			}
			subject = child
		} else {
			child, ok := subject[part].(map[string]any)
			if !ok {
				child = map[string]any{}
				subject[part] = child
			}
			subject = child
		}
	}
	key := parts[len(parts)-1]
	v := value.value
	if row.kind == "file" {
		inline := strings.TrimSuffix(key, "_file")
		input, _ := subject[inline].(string)
		if input == "" {
			input = inventoryData
		}
		if inline == "rpc" {
			input = inventoryRPC
		}
		delete(subject, inline)
		if value.name == "valid-file" || value.name == "empty-file" || value.name == "inline-conflict" {
			if value.name == "empty-file" {
				input = ""
			}
			path := filepath.Join(files, fmt.Sprintf("request-input-%x.json", sha256.Sum256([]byte(input))))
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			v = filepath.Base(path)
			if value.name == "inline-conflict" {
				subject[inline] = ""
			}
		}
	}
	if value.name == "valid-directory" {
		v = "."
	}
	if strings.HasPrefix(row.key, "namespaces[].") {
		inventorySubject(p, seed.path)["xpath_format"] = "xml"
	}
	delete(subject, key)
	if value.name != "absent" {
		subject[key] = v
	}
	if skipped {
		// Replace the successful setup with a guaranteed execution failure on a
		// NULL tree. Keep the mutated step and all trailing steps for preflight.
		steps := p["steps"].([]any)
		index := 0
		if seed.path[1] == "1" {
			index = 1
		}
		p["steps"] = append([]any{map[string]any{"do": "edit", "delete": "/missing:x"}}, steps[index:]...)
	}
	return p
}

func inventoryRun(r Request) (resp Response, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("engine panic: %v", v)
		}
	}()
	return (Yang{}).Run(r)
}

func inventoryRunWithContextCache(r Request, contexts *inventoryContextCache) (Response, error) {
	r.buildContext = contexts.request().build
	return inventoryRun(r)
}

func TestInventoryContextCachePreservesContextIdentity(t *testing.T) {
	target := func() map[string]any {
		p := inventoryCommon("schema")
		delete(p, "op")
		return p
	}
	cases := map[string]map[string]any{
		"diff target": {
			"op": "sequence", "searchdirs": []any{"schemas"},
			"modules": []any{map[string]any{"name": "pv2"}}, "context_options": []any{"no_yanglibrary"},
			"steps": []any{
				map[string]any{"do": "parse", "data": inventoryData, "parse_only": true},
				map[string]any{"do": "diff", "data": inventoryData, "parse_only": true, "target": target()},
			},
		},
		"dup target": {
			"op": "sequence", "searchdirs": []any{"schemas"},
			"modules": []any{map[string]any{"name": "pv2"}}, "context_options": []any{"no_yanglibrary"},
			"steps": []any{
				map[string]any{"do": "parse", "data": inventoryData, "parse_only": true},
				map[string]any{"do": "dup", "node": "/pv2:c", "options": []any{"recursive"}, "target": target()},
				map[string]any{"do": "dump"},
			},
		},
	}
	contexts := newInventoryContextCache()
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			r := Request{BaseDir: "corpus/protocol-v2", Params: params}
			got, err := inventoryRunWithContextCache(r, contexts)
			want, wantErr := inventoryRun(r)
			if !reflect.DeepEqual(got, want) || reflect.TypeOf(err) != reflect.TypeOf(wantErr) || fmt.Sprint(err) != fmt.Sprint(wantErr) {
				t.Fatalf("cached run = %#v, %v\nplain run = %#v, %v", got, err, want, wantErr)
			}
		})
	}
	if got := contexts.Len(); got != 2 {
		t.Fatalf("cached contexts = %d, want root and target identities shared by call index", got)
	}
}

// The verdict, rc, every diagnostic (including context/module diagnostics),
// every step rc/skipped and request-error text class are compared. Do not drop
// diagnostic msg, code, phase, paths or app-tag. inventoryResults separately
// compares output fields; this projection also checks the older captured
// execution-only regression records.
func inventoryOutcome(r Response) any {
	if r == nil {
		return nil
	}
	b, _ := json.Marshal(requestOutcome(r))
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if r["verdict"] == "request-error" {
		out["request_error_class"] = inventoryErrorClass(fmt.Sprint(r["request_error"]))
		return out
	}
	out["context_diagnostics"] = r["context_diagnostics"]
	var mods []any
	for _, x := range list(r["modules"]) {
		m := x.(map[string]any)
		n := map[string]any{}
		for _, k := range []string{"name", "accepted", "phase", "rc", "diagnostics"} {
			if v, ok := m[k]; ok {
				n[k] = v
			}
		}
		mods = append(mods, n)
	}
	out["modules"] = mods
	b, _ = json.Marshal(out)
	var result any
	_ = json.Unmarshal(b, &result)
	return result
}

// Wording differences within a class are intentional: Go adds quoting and
// OS-specific file errors. Preserve the field and the kind of rejection;
// unclassified messages compare verbatim, so new errors cannot silently pass.
func inventoryErrorClass(msg string) string {
	s := strings.ReplaceAll(msg, "\"", "")
	switch {
	case strings.HasPrefix(s, "unknown key "):
		return "unknown-key:" + strings.Fields(s)[2]
	case strings.HasPrefix(s, "unknown flag in "):
		return "unknown-flag:" + strings.Fields(s)[3]
	case strings.Contains(s, "must be a string"):
		return "string-type:" + strings.TrimPrefix(strings.Split(s, " must")[0], "field ")
	case strings.Contains(s, "must be an array of flags"):
		return "flags-type:" + strings.TrimPrefix(strings.Split(s, " must")[0], "field ")
	case strings.HasPrefix(s, "cannot open file "), strings.HasPrefix(s, "cannot read file "):
		return "file-read"
	case strings.HasPrefix(s, "both inline and _file given for "):
		return "input-conflict:" + strings.TrimPrefix(s, "both inline and _file given for ")
	case strings.HasPrefix(s, "unknown format"):
		return "unknown-format"
	case strings.HasPrefix(s, "unknown unknown"):
		return "unknown-policy"
	case strings.HasPrefix(s, "unknown with_defaults"):
		return "unknown-with-defaults"
	case strings.HasPrefix(s, "parse_options strict/opaq"):
		return "forbidden-parse-flags"
	case strings.HasPrefix(s, "unknown sequence step"):
		return "unknown-step"
	case strings.HasPrefix(s, "missing "):
		return "missing:" + strings.TrimPrefix(s, "missing ")
	case strings.HasPrefix(s, "step without"):
		return "missing:do"
	case strings.HasPrefix(s, "cannot chdir"):
		return "base-directory"
	}
	return s
}

// Explicit B class registry, mirrored in docs/conformance/request-differential.md.
// Matching an id alone is insufficient: a new capability with an existing id
// must still be reviewed and added as a documented class.
var inventoryGapClasses = map[string]struct {
	id      string
	reasons []string
	exact   string
	request func(map[string]any) bool
}{
	"ordered-input":              {id: "D-0058", reasons: []string{"LYD_PARSE_ORDERED input order"}},
	"any-direct":                 {id: "U-0112", exact: "unsupported: native pointer-level any-value calls have no public Go equivalent, U-0112", request: inventoryAnyDirectStep},
	"operation-parent":           {id: "U-0106", reasons: []string{"rpc input into an existing", "operation input into an existing parent", "an rpc request with data_type", "a reply into an opaque request"}},
	"reply-non-operation-parent": {id: "U-0110", reasons: []string{"reply into a non-operation parent"}},
	"cross-context-diff":         {id: "D-0069", reasons: []string{"cross-context diff of populated trees"}},
}

func inventoryAnyDirectStep(p map[string]any) bool {
	if p["op"] != "sequence" {
		return false
	}
	for _, raw := range list(p["steps"]) {
		if step, ok := raw.(map[string]any); ok && step["do"] == "any_direct" {
			return true
		}
	}
	return false
}

// Explicit native-crash registry. A signal is a documented deviation only when exactly one
// predicate matches and the engine produces that deviation's required result. A crashed native
// run has no response, so nothing of it (earlier steps included) can be compared.
var inventoryCrashClasses = map[string]struct {
	id          string
	termination string
	match       func(map[string]any) bool
	result      func(p map[string]any, got Response, err error) bool
}{
	"dup-term-parent": {"D-0123", "signal: segmentation fault (core dumped)", inventoryDupTermParent, func(p map[string]any, got Response, err error) bool {
		return err == nil && inventoryCrashResult(p, got, "LY_EINVAL")
	}},
	// parse_one (lyoracle.c:1307-1313) parses the rpc input opaque into the rpc_parent it found;
	// a term parent ends the native process. The port cannot express that parent
	// (ParseOpOptions.Request binds replies to an RPC/action only), so the engine refuses it.
	"op-parse-term-parent": {"U-0106", "signal: segmentation fault (core dumped)", inventoryOpParseTermParent, func(_ map[string]any, _ Response, err error) bool {
		return inventoryGap(err) == "operation-parent"
	}},
}

// Registered deviations where the native run completes and the port refuses on purpose. A
// concrete mismatch (A) is DEV only when exactly one class holds: its precondition, checked on
// the request and the native response, never on the engine's; every step before the refused
// one compared in full; and the refused step's exact result. Only the refused step and the
// steps after it, which follow from the flipped verdict, may differ.
var inventoryDeviationClasses = map[string]struct {
	id    string
	holds func(p map[string]any, want, got Response, err error) bool
}{
	"dup-mismatched-parent": {"D-0124", inventoryDupMismatchedParent},
}

// inventoryDevID is the registry id of a DEV class (native crash or completed deviation).
func inventoryDevID(class string) string {
	if c, ok := inventoryCrashClasses[class]; ok {
		return c.id
	}
	return inventoryDeviationClasses[class].id
}

// inventoryDupMismatchedParent is D-0124: a sequence dup step (a parent, no with_parents, no
// target) the native run executes successfully, whose parent is not an instance of the node's
// schema parent (lysc_data_parent) in the native tree before the step; the engine refuses
// exactly that step with LY_EINVAL and the D-0124 diagnostic, and every earlier step agrees.
func inventoryDupMismatchedParent(p map[string]any, want, got Response, err error) bool {
	if err != nil || p["op"] != "sequence" {
		return false
	}
	k := -1
	var node, parent string
	for i, raw := range list(p["steps"]) {
		st, _ := raw.(map[string]any)
		if st["do"] == "dup" {
			n, nok := st["node"].(string)
			par, pok := st["parent"].(string)
			if !nok || !pok || st["target"] != nil || slices.Contains(list(st["options"]), "with_parents") {
				return false
			}
			k, node, parent = i, n, par
			break
		}
	}
	nativeSteps, engineSteps := list(want["steps"]), list(got["steps"])
	if k < 1 || k >= len(nativeSteps) || k >= len(engineSteps) {
		return false
	}
	// the native dup step ran and succeeded (lyd_dup has no such check)
	ns, _ := nativeSteps[k].(map[string]any)
	if ns["do"] != "dup" || ns["skipped"] == true || inventoryRCName(ns["rc"]) != "LY_SUCCESS" {
		return false
	}
	// precondition on the native tree before the step (typed dump of step k-1): node and parent
	// are schema-backed, and the node's data parent instance (an instance of its
	// lysc_data_parent) has another schema node than parent; a top-level node has none
	schemaOf := map[string]any{}
	for _, raw := range list(nativeSteps[k-1].(map[string]any)["typed"]) {
		if e, ok := raw.(map[string]any); ok {
			schemaOf[fmt.Sprint(e["path"])] = e["schema"]
		}
	}
	nodeSchema, parentSchema := schemaOf[node], schemaOf[parent]
	if nodeSchema == nil || parentSchema == nil {
		return false
	}
	if prefixes := pathPrefixes(node); len(prefixes) > 0 {
		srcParent := schemaOf[prefixes[0]]
		if srcParent == nil || srcParent == parentSchema {
			return false
		}
	}
	// the engine's refused step, exactly
	es, _ := engineSteps[k].(map[string]any)
	diags := list(es["diagnostics"])
	msg := fmt.Sprintf("Duplicating %q under %q, which is not an instance of its schema parent (D-0124).", node, parent)
	if es["do"] != "dup" || inventoryRCName(es["rc"]) != "LY_EINVAL" || fmt.Sprint(got["failed_step"]) != fmt.Sprint(k) ||
		len(diags) != 1 || fmt.Sprint(diags[0].(map[string]any)["msg"]) != msg {
		return false
	}
	// every earlier step (and the schema prelude) agrees in full
	before := func(r Response) Response {
		o := maps.Clone(r)
		o["steps"] = list(r["steps"])[:k]
		o["verdict"], o["rc"], o["failed_step"] = "valid", codeJSON("LY_SUCCESS"), -1
		return o
	}
	w, g := before(want), before(got)
	return reflect.DeepEqual(inventoryOutcome(w), inventoryOutcome(g)) && sameJSON(inventoryResults(w), inventoryResults(g))
}

// inventoryDeviation is the registered completed-native deviation class a concrete mismatch
// belongs to, "" for none or more than one.
func inventoryDeviation(p map[string]any, want, got Response, err error) string {
	var matches []string
	for class, dev := range inventoryDeviationClasses {
		if dev.holds(p, want, got, err) {
			matches = append(matches, class)
		}
	}
	if len(matches) != 1 {
		return ""
	}
	return matches[0]
}

// inventoryOpParseTermParent: an op data request that parses rpc input (keep_input, unknown
// opaque) under an rpc_parent resolving in the inline rpc input to a leaf or leaf-list instance.
func inventoryOpParseTermParent(p map[string]any) bool {
	rpc, _ := p["rpc"].(string)
	parent, _ := p["rpc_parent"].(string)
	var seed any
	if p["op"] != "data" || p["data_type"] != "rpc" || p["keep_input"] != true || p["unknown"] != "opaque" ||
		rpc == "" || json.Unmarshal([]byte(rpc), &seed) != nil {
		return false
	}
	return inventoryJSONPathIsTerm(seed, parent)
}

func inventoryGap(err error, request ...map[string]any) string {
	if !errors.Is(err, ErrUnsupported) {
		return ""
	}
	for class, gap := range inventoryGapClasses {
		if !strings.Contains(err.Error(), gap.id) {
			continue
		}
		if gap.exact != "" {
			if err.Error() == gap.exact && len(request) == 1 && gap.request(request[0]) {
				return class
			}
			continue
		}
		for _, reason := range gap.reasons {
			if strings.Contains(err.Error(), reason) {
				return class
			}
		}
	}
	return "unclassified"
}

func inventoryClassification(want, got Response, err error, artefact string, request ...map[string]any) (string, string) {
	if artefact != "" {
		return "C", "outside-subject"
	}
	if errors.Is(err, ErrUnsupported) {
		// A gap cannot explain a request-shape rejection that precedes the
		// unsupported operation. Checks after parsing need that capability.
		if want["verdict"] == "request-error" && inventoryPreparseRequestError(fmt.Sprint(want["request_error"])) {
			return "A", "request-error"
		}
		return "B", inventoryGap(err, request...)
	}
	if err != nil && strings.HasSuffix(err.Error(), "(a request-error)") {
		got = Response{"verdict": "request-error", "request_error": strings.TrimSuffix(err.Error(), " (a request-error)")}
		err = nil
	}
	if err != nil {
		return "A", "engine-error"
	}
	if !reflect.DeepEqual(inventoryOutcome(want), inventoryOutcome(got)) {
		return "A", "execution"
	}
	if want["verdict"] != "request-error" && !sameJSON(inventoryResults(want), inventoryResults(got)) {
		return "A", "output"
	}
	return "MATCH", "equal"
}

// inventoryPreparseRequestError recognizes only checks the engine can reach before
// an unsupported capability. The oracle's selector lookups, cur_module lookup,
// namespace checks and xpath_format semantics follow parse_one; an unknown
// request-error stays with the registered capability gap until its order is known.
func inventoryPreparseRequestError(msg string) bool {
	s := strings.ReplaceAll(msg, "\"", "")
	if field, ok := strings.CutPrefix(s, "field "); ok {
		if field, ok := strings.CutSuffix(field, " must be a string"); ok {
			// str_of is used during request initialization and step preflight.
			// Namespace entries are read after XPath data parse.
			return field != "prefix" && field != "uri"
		}
		if key, ok := strings.CutSuffix(field, " must be an array of flags"); ok {
			return inventoryPreparseFlag(key)
		}
	}
	if key, ok := strings.CutPrefix(s, "unknown flag in "); ok {
		return inventoryPreparseFlag(key)
	}
	switch {
	case strings.HasPrefix(s, "unknown key "), strings.HasPrefix(s, "missing "),
		strings.HasPrefix(s, "step without"), strings.HasPrefix(s, "both inline and _file given for "):
		return true
	case strings.HasPrefix(s, "cannot chdir"), strings.HasPrefix(s, "cannot open file "),
		strings.HasPrefix(s, "cannot read file "), strings.HasPrefix(s, "unknown format"),
		strings.HasPrefix(s, "unknown unknown"), strings.HasPrefix(s, "unknown with_defaults"),
		strings.HasPrefix(s, "parse_options strict/opaq"), strings.HasPrefix(s, "unknown sequence step"):
		return true
	case strings.HasPrefix(s, "bad searchdir "), strings.HasPrefix(s, "data_type ") && strings.HasSuffix(s, " needs parse_only"),
		s == "equal_repeated_collisions needs target",
		strings.HasPrefix(s, "any_direct ") && strings.HasSuffix(s, " needs target"):
		return true
	}
	return false
}

func inventoryPreparseFlag(key string) bool {
	// diff_options is checked only after both diff inputs parse.
	switch key {
	case "context_options", "parse_options", "validate_options", "print_options",
		"atom_options", "options", "merge_options":
		return true
	}
	return false
}

// inventoryDeferredClass is a demonstrated envelope-only discrepancy deferred under the M4
// scope (docs/conformance/request-differential.md#scope): lyoracle's request envelope and the
// engine disagree in a way that cannot alter library arguments, options, context identity,
// execution, results or diagnostics. A deferred result is reported separately and never
// counted as MATCH. Every field is required.
type inventoryDeferredClass struct {
	match      func(p map[string]any, want, got Response, err error) bool // narrow predicate
	reproducer string                                                     // native request JSON showing it
	why        string                                                     // why it is envelope-only
	issue      int                                                        // follow-up issue
}

func (d inventoryDeferredClass) complete() bool {
	return d.match != nil && d.reproducer != "" && d.why != "" && d.issue > 0
}

// inventoryDeferredClasses is empty: no envelope-only class is deferred today.
var inventoryDeferredClasses = map[string]inventoryDeferredClass{}

// inventoryCaseClassification classifies one generated case. A concrete A (both sides
// produced a response) is DEFERRED only when exactly one complete deferred class matches;
// artefacts, transport failures and native terminations are never deferred.
func inventoryCaseClassification(p map[string]any, want Response, nativeErr error, got Response, err error, artefact string) (string, string) {
	category, class := inventoryBaseClassification(p, want, nativeErr, got, err, artefact)
	if category != "A" || nativeErr != nil || artefact != "" {
		return category, class
	}
	var matches []string
	for name, d := range inventoryDeferredClasses {
		if d.complete() && d.match(p, want, got, err) {
			matches = append(matches, name)
		}
	}
	if len(matches) == 1 {
		return "DEFERRED", matches[0]
	}
	return category, class
}

func inventoryBaseClassification(p map[string]any, want Response, nativeErr error, got Response, err error, artefact string) (string, string) {
	if nativeErr == nil {
		if artefact != "" {
			return "C", "outside-subject"
		}
		category, class := inventoryClassification(want, got, err, "", p)
		if category == "A" {
			if dev := inventoryDeviation(p, want, got, err); dev != "" {
				return "DEV", dev
			}
		}
		return category, class
	}
	if nativeDeadline(nativeErr) {
		return "A", "native-transport"
	}
	termination := nativeTerminationText(nativeErr)
	if termination == "" {
		return "A", "native-transport"
	}
	var matches []string
	for class, crash := range inventoryCrashClasses {
		if crash.match(p) {
			matches = append(matches, class)
		}
	}
	if len(matches) == 0 {
		return "A", "native-crash-unregistered"
	}
	if len(matches) != 1 {
		return "A", "native-crash-ambiguous"
	}
	class := matches[0]
	if termination != inventoryCrashClasses[class].termination {
		return "A", "native-crash-unregistered"
	}
	if artefact != "" {
		return "C", "outside-subject"
	}
	if !inventoryCrashClasses[class].result(p, got, err) {
		return "A", "native-crash-result"
	}
	return "DEV", class
}

func nativeDeadline(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || err != nil && strings.Contains(err.Error(), "oracle transport deadline:")
}

// nativeTerminationText extracts only exec.ExitError's termination text from the transport
// wrapper (and from the cache wrapper around it). Output after the transport error remains part
// of the text, so a noisy or otherwise different termination cannot match a registered crash.
func nativeTerminationText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if i := strings.LastIndex(s, "oracle transport: "); i >= 0 {
		s = s[i+len("oracle transport: "):]
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ":") // runNativeRequest's empty stdout separator
	if !strings.HasPrefix(s, "signal: ") {
		return ""
	}
	return s
}

func inventoryDupTermParent(p map[string]any) bool {
	_, ok := inventoryDupTermParentStep(p)
	return ok
}

// inventoryDupTermParentStep resolves the dup parent against the inline JSON seed used by the
// matrix. It intentionally accepts only a scalar leaf or a selected scalar leaf-list instance;
// objects (inner nodes and opaque payloads), missing paths, and WithParents are not this class.
func inventoryDupTermParentStep(p map[string]any) (int, bool) {
	if p["op"] != "sequence" {
		return 0, false
	}
	steps, ok := p["steps"].([]any)
	if !ok {
		return 0, false
	}
	var seed any
	for i, raw := range steps {
		st, ok := raw.(map[string]any)
		if !ok {
			return 0, false
		}
		switch st["do"] {
		case "parse":
			input, ok := st["data"].(string)
			if !ok || json.Unmarshal([]byte(input), &seed) != nil {
				seed = nil
			}
		case "dup":
			if seed == nil || slices.Contains(list(st["options"]), "with_parents") {
				return 0, false
			}
			parent, ok := st["parent"].(string)
			if !ok || !inventoryJSONPathIsTerm(seed, parent) {
				return 0, false
			}
			return i, true
		case "edit":
			// A successful edit may change which node the path selects. The registered matrix
			// seed has no edit between parse and dup, so declining avoids a broad crash waiver.
			if seed != nil {
				seed = nil
			}
		}
	}
	return 0, false
}

func inventoryJSONPathIsTerm(seed any, path string) bool {
	root, ok := seed.(map[string]any)
	if !ok || path == "" {
		return false
	}
	path = strings.TrimLeft(path, " \t\n\r")
	if path == "" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	var cur any = root
	if path[0] != '/' {
		if len(root) != 1 {
			return false
		}
		for _, value := range root {
			cur = value
			break
		}
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		name, predKey, predValue, predicate, ok := inventoryPathSegment(part)
		if !ok {
			return false
		}
		object, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = inventoryJSONField(object, name)
		if !ok {
			return false
		}
		if predicate {
			values, ok := cur.([]any)
			if !ok {
				return false
			}
			cur, ok = inventoryJSONPredicate(values, predKey, predValue)
			if !ok {
				return false
			}
		}
	}
	return inventoryJSONScalar(cur)
}

func inventoryPathSegment(part string) (name, key, value string, predicate, ok bool) {
	i := strings.IndexByte(part, '[')
	if i < 0 {
		return part, "", "", false, true
	}
	if i == 0 || !strings.HasSuffix(part, "]") || strings.Contains(part[i+1:len(part)-1], "][") {
		return "", "", "", false, false
	}
	key, quoted, found := strings.Cut(part[i+1:len(part)-1], "=")
	if !found || key == "" || len(quoted) < 2 || quoted[0] != '\'' || quoted[len(quoted)-1] != '\'' {
		return "", "", "", false, false
	}
	value = strings.ReplaceAll(quoted[1:len(quoted)-1], `\'`, `'`)
	return part[:i], key, value, true, true
}

func inventoryJSONPredicate(values []any, key, want string) (any, bool) {
	for _, value := range values {
		if key == "." {
			if inventoryJSONScalar(value) && fmt.Sprint(value) == want {
				return value, true
			}
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			continue
		}
		actual, ok := inventoryJSONField(object, key)
		if ok && inventoryJSONScalar(actual) && fmt.Sprint(actual) == want {
			return value, true
		}
	}
	return nil, false
}

func inventoryJSONField(object map[string]any, name string) (any, bool) {
	if value, ok := object[name]; ok {
		return value, true
	}
	local := name
	if i := strings.IndexByte(local, ':'); i >= 0 {
		local = local[i+1:]
	}
	var found any
	seen := false
	for key, value := range object {
		if i := strings.IndexByte(key, ':'); i >= 0 {
			key = key[i+1:]
		}
		if key == local {
			if seen {
				return nil, false
			}
			found, seen = value, true
		}
	}
	return found, seen
}

func inventoryJSONScalar(value any) bool {
	switch value.(type) {
	case nil, bool, string, float64, json.Number:
		return true
	}
	return false
}

func inventoryCrashResult(p map[string]any, got Response, wantRC string) bool {
	step, ok := inventoryDupTermParentStep(p)
	if !ok || got.Verdict() != "invalid" || fmt.Sprint(got["failed_step"]) != fmt.Sprint(step) || inventoryRCName(got["rc"]) != wantRC {
		return false
	}
	steps, ok := got["steps"].([]any)
	if !ok || step >= len(steps) {
		return false
	}
	result, ok := steps[step].(map[string]any)
	return ok && result["do"] == "dup" && inventoryRCName(result["rc"]) == wantRC
}

func inventoryRCName(value any) string {
	rc, _ := value.(map[string]any)
	name, _ := rc["name"].(string)
	return name
}

// Compare all result fields as well as execution status. Only the existing
// FieldSkipper contract (compiled schema text and unexported typed metadata) is
// omitted; tree JSON/XML, diffs, atoms and XPath values are never dropped.
func inventoryResults(r Response) Response {
	r, _ = withoutSkipped(r, (Yang{}).SkippedFields(str(r, "op", "")))
	out := maps.Clone(r)
	for _, k := range []string{"libyang", "arch", "op", "verdict", "rc", "failed_step", "diagnostics", "context_diagnostics", "request_error"} {
		delete(out, k)
	}
	mapItems(out, "modules", func(m map[string]any) {
		for _, k := range []string{"name", "accepted", "phase", "rc", "diagnostics"} {
			delete(m, k)
		}
	})
	mapItems(out, "steps", func(m map[string]any) {
		for _, k := range []string{"do", "skipped", "rc", "diagnostics"} {
			delete(m, k)
		}
	})
	return out
}

type requestDiffValue struct {
	name  string
	value any
}

func requestDiffMatrix() ([]string, []requestDiffValue) {
	return []string{"rpc", "rpc_file", "rpc_parent", "keep_input", "module", "final_only", "data", "data_file", "parse_only", "data_type", "format", "unknown", "parse_options", "validate_options"},
		[]requestDiffValue{{"absent", nil}, {"null", nil}, {"false", false}, {"true", true}, {"empty", ""}, {"string", "x"}, {"unknown-flag", []any{"bogus"}}, {"nonstring-flag", []any{false}}}
}

// Check every matrix mutation behind a failing step. A request-phase error
// still rejects the entire request; an execution-phase error must be skipped.
func TestRequestValidationSkippedDifferential(t *testing.T) {
	oracle, base := requestOracle(t)
	contexts := newInventoryContextCache()
	fields, values := requestDiffMatrix()
	for _, seed := range requestDiffSeeds() {
		if seed.step == nil {
			continue
		}
		for _, field := range fields {
			vals := values
			if strings.HasSuffix(field, "_file") {
				vals = append(append([]requestDiffValue{}, vals...), requestDiffValue{"missing-file", "request-diff-does-not-exist.json"})
			}
			for _, v := range vals {
				t.Run(seed.name+"/"+field+"/"+v.name, func(t *testing.T) {
					st := maps.Clone(seed.step)
					delete(st, field)
					if v.name != "absent" {
						st[field] = v.value
					}
					p := maps.Clone(seed.params)
					p["base_dir"] = "."
					p["steps"] = []any{map[string]any{"do": "edit", "delete": "/missing:x"}, st}
					compareRequestOutcome(t, oracle, contexts, base, p)
				})
			}
		}
	}
}

// Unlike the request-class matrix, compare the actual parse result, including
// diagnostic messages and codes. JSON null and absent are NULL in input_of;
// an empty string is a non-NULL memory input. Cover all dparams_of presets.
func TestInputPresenceDifferential(t *testing.T) {
	oracle, base := requestOracle(t)
	contexts := newInventoryContextCache()
	for _, seed := range requestDiffSeeds() {
		if seed.step != nil && seed.step["do"] != "parse" || strings.HasSuffix(seed.name, "reply-with-rpc") {
			continue
		}
		for _, format := range []string{"json", "xml"} {
			for _, presence := range []string{"absent", "null", "empty"} {
				t.Run(seed.name+"/"+format+"/"+presence, func(t *testing.T) {
					p := maps.Clone(seed.params)
					subject := p
					if seed.step != nil {
						subject = maps.Clone(seed.step)
						p["steps"] = []any{subject}
					}
					typ := str(subject, "data_type", "")
					envelope := typ == "notif-netconf" || typ == "notif-restconf"
					operation := typ == "rpc" || typ == "reply" || typ == "notif" || envelope
					// Use each preset's default validation policy, except where
					// the protocol requires parse_only for operation inputs.
					if !envelope && (seed.step == nil || !operation) {
						delete(subject, "parse_only")
					}
					subject["format"] = format
					delete(subject, "data")
					switch presence {
					case "null":
						subject["data"] = nil
					case "empty":
						subject["data"] = ""
					}
					p["base_dir"] = "."
					compareRequestOutcome(t, oracle, contexts, base, p)
				})
			}
		}
	}
}

func compareRequestOutcome(t *testing.T, oracle *requestOracleRunner, contexts *inventoryContextCache, base string, p map[string]any) {
	t.Helper()
	want, nativeErr := inventoryNativeResponse(oracle, base, p)
	got, err := inventoryRunWithContextCache(Request{BaseDir: base, Params: p}, contexts)
	category, class := inventoryCaseClassification(p, want, nativeErr, got, err, "")
	switch category {
	case "B":
		gap, ok := inventoryGapClasses[class]
		if !ok {
			t.Fatalf("undocumented B class: %v", err)
		}
		t.Logf("B %s (%s): %v", class, gap.id, err)
	case "DEV":
		t.Logf("DEV %s (%s): %v", class, inventoryDevID(class), nativeErr)
	case "DEFERRED":
		t.Logf("DEFERRED %s (#%d): %v", class, inventoryDeferredClasses[class].issue, err)
	case "MATCH":
	default:
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got)
		t.Errorf("%s/%s: oracle: %s\nengine: %s (%v)", category, class, wb, gb, err)
	}
}

// Keep every step outcome (including skipped, rc and diagnostics) and compare
// diagnostic messages verbatim. Schema dumps and tree rendering are covered by
// the corpus; request-error wording is deliberately outside this comparison.
func requestOutcome(r Response) any {
	o := map[string]any{"verdict": r["verdict"]}
	if r["verdict"] == "request-error" {
		return o
	}
	for _, key := range []string{"rc", "failed_step", "diagnostics"} {
		if v, ok := r[key]; ok {
			o[key] = v
		}
	}
	if steps, ok := r["steps"].([]any); ok {
		out := make([]any, 0, len(steps))
		for _, step := range steps {
			s := step.(map[string]any)
			v := map[string]any{}
			for _, key := range []string{"do", "skipped", "rc", "diagnostics", "result"} {
				if x, ok := s[key]; ok {
					v[key] = x
				}
			}
			out = append(out, v)
		}
		o["steps"] = out
	}
	// Marshal/unmarshal also equates Go ints with the oracle's JSON numbers.
	b, _ := json.Marshal(o)
	var result any
	_ = json.Unmarshal(b, &result)
	return result
}

type requestDiffSeed struct {
	name         string
	params, step map[string]any
}

func requestDiffSeeds() []requestDiffSeed {
	var seeds []requestDiffSeed
	common := func(op string) map[string]any {
		return map[string]any{
			"op": op, "searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2"}},
			"context_options": []any{"no_yanglibrary"},
		}
	}
	for _, typ := range []string{"data-operational", "data", "config", "get", "getconfig", "edit", "rpc", "reply", "notif", "notif-netconf", "notif-restconf"} {
		input, format := `{"pv2:c":{"mode":"on"}}`, "json"
		switch typ {
		case "rpc":
			input = `{"pv2:ping":{"n":1}}`
		case "reply":
			input = `{"pv2:ping":{"rtt":1}}`
		case "notif":
			input = `{"pv2:alarm":{"sev":1}}`
		case "notif-netconf":
			format, input = "xml", `<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><eventTime>2026-01-01T00:00:00Z</eventTime><alarm xmlns="urn:vibe-ports:yang:conformance:pv2"><sev>1</sev></alarm></notification>`
		case "notif-restconf":
			input = `{"ietf-restconf:notification":{"eventTime":"2026-01-01T00:00:00Z","pv2:alarm":{"sev":1}}}`
		}
		fields := map[string]any{"format": format, "data_type": typ, "parse_only": true, "data": input}
		p := common("data")
		maps.Copy(p, fields)
		seeds = append(seeds, requestDiffSeed{"data/" + typ, p, nil})
		for _, kind := range []string{"parse", "diff"} {
			st := maps.Clone(fields)
			st["do"] = kind
			seeds = append(seeds, requestDiffSeed{"sequence/" + kind + "/" + typ, common("sequence"), st})
		}
	}
	for _, kind := range []string{"validate", "diff_parse", "diff_merge", "edit", "insert", "compare"} {
		st := map[string]any{"do": kind, "data_type": "data"}
		switch kind {
		case "validate":
			st["module"] = "pv2"
		case "edit":
			st["merge"] = `{}`
		default:
			st["data"] = `{}`
		}
		seeds = append(seeds, requestDiffSeed{"sequence/" + kind, common("sequence"), st})
	}
	// Exercise keep_input with an actual request too, not only the missing-rpc arm.
	for _, kind := range []string{"parse", "diff"} {
		st := map[string]any{"do": kind, "data_type": "reply", "parse_only": true, "rpc": `{"pv2:ping":{"n":1}}`, "data": `{"pv2:rtt":1}`}
		seeds = append(seeds, requestDiffSeed{"sequence/" + kind + "/reply-with-rpc", common("sequence"), st})
	}
	return seeds
}

// Exercise when_true where it changes behavior, and anydata_strict on ordinary
// data. These generated requests use the same native cache and outcome checker.
func TestParseFlagsDifferential(t *testing.T) {
	oracle, base := requestOracle(t)
	contexts := newInventoryContextCache()
	for _, flag := range []string{"when_true", "anydata_strict"} {
		for _, format := range []string{"json", "xml"} {
			t.Run(flag+"/"+format, func(t *testing.T) {
				p := inventoryCommon("data")
				p["base_dir"], p["data_type"], p["format"] = ".", "config", format
				p["data"] = `{"pv2:c":{"mode":"off","x":"remove"}}`
				if format == "xml" {
					p["data"] = `<c xmlns="urn:vibe-ports:yang:conformance:pv2"><mode>off</mode><x>remove</x></c>`
				}
				p["parse_options"] = []any{flag}
				compareRequestOutcome(t, oracle, contexts, base, p)
			})
		}
	}
}
