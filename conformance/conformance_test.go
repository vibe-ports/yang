// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const manifestPath = "corpus/manifest.yaml"

func load(t *testing.T) *Manifest {
	t.Helper()
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CheckFiles(true); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestManifestWellFormed(t *testing.T) { load(t) }

func TestAssertsConsistentWithGoldens(t *testing.T) {
	m := load(t)
	for _, f := range m.Fixtures {
		if f.Assert == nil {
			continue
		}
		g, err := LoadGolden(m.GoldenPath(f))
		if err != nil {
			t.Fatal(err)
		}
		d := MatchAssert(f.Assert, g)
		if d != "" && f.Assert.Deviation == nil {
			t.Errorf("%s: assert contradicts golden without a deviation: %s", f.ID, d)
		}
		for _, w := range f.Assert.Waive {
			if !hasField(g, w) {
				t.Errorf("%s: assert.waive %q: the golden has no such field", f.ID, w)
			}
		}
		if d == "" && f.Assert.Deviation != nil && len(f.Assert.Waive) == 0 { // a waiver's staleness is the engine's (classify)
			t.Errorf("%s: stale deviation %s: assert already matches the golden", f.ID, *f.Assert.Deviation)
		}
	}
}

func TestGoldensRoundTrip(t *testing.T) {
	m := load(t)
	for _, f := range m.Fixtures {
		raw, err := os.ReadFile(m.GoldenPath(f))
		if err != nil {
			t.Fatal(err)
		}
		g, err := ParseResponse(raw)
		if err != nil {
			t.Fatal(err)
		}
		out, err := g.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != string(raw) {
			t.Errorf("%s: golden does not round-trip", f.ID)
		}
	}
}

func TestMarshalASCII(t *testing.T) {
	out, err := Response{"a": "é<😀\x7f"}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"a\": \"\\u00e9<\\ud83d\\ude00\\u007f\"\n}\n"; string(out) != want {
		t.Errorf("got %q want %q", out, want)
	}
}

func TestCompareReplayAllAgree(t *testing.T) {
	m := load(t)
	e, err := NewReplay(m)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := m.Compare(e)
	if err != nil {
		t.Fatal(err)
	}
	// The replay engine returns the goldens, which a deviation's assert contradicts by definition
	// (TestAssertsConsistentWithGoldens): those fixtures are Differ here and only a real engine
	// can make them Deviation.
	devs := 0
	for _, f := range m.Fixtures {
		if f.Assert != nil && f.Assert.Deviation != nil {
			devs++
		}
	}
	for _, r := range rep.Results {
		want := Agree
		if hasDeviation(m, r.ID) {
			want = Differ
		}
		if r.Status != want {
			t.Errorf("%s: %s %s, want %s", r.ID, r.Status, r.Detail, want)
		}
		if f := fixture(m, r.ID); f.Assert != nil && len(f.Assert.Waive) > 0 && !strings.HasPrefix(r.Detail, "stale waive") {
			t.Errorf("%s: %s, want a stale waive (replaying libyang)", r.ID, r.Detail)
		}
	}
	if len(rep.Results) != len(m.Fixtures) || !strings.Contains(rep.Markdown(), fmt.Sprintf("| **fixtures** | %d | 0 | %d | 0 | 0 |", len(m.Fixtures)-devs, devs)) {
		t.Errorf("unexpected report:\n%s", rep.Markdown())
	}
}

// fixture returns the fixture id (zero if none).
func fixture(m *Manifest, id string) Fixture {
	for _, f := range m.Fixtures {
		if f.ID == id {
			return f
		}
	}
	return Fixture{}
}

// hasDeviation reports whether fixture id asserts a deviation from libyang.
func hasDeviation(m *Manifest, id string) bool {
	for _, f := range m.Fixtures {
		if f.ID == id {
			return f.Assert != nil && f.Assert.Deviation != nil
		}
	}
	return false
}

// wrong replays goldens but flips one fixture's verdict.
type wrong struct {
	Engine
	id string
}

func (w wrong) Run(r Request) (Response, error) {
	g, err := w.Engine.Run(r)
	if err == nil && r.ID == w.id {
		g = maps.Clone(g)
		g["verdict"] = "valid"
	}
	return g, err
}

func TestCompareFakeEngineDiffers(t *testing.T) {
	m := load(t)
	rp, err := NewReplay(m)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := m.Compare(wrong{rp, "basic/range"})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		want := Agree
		if r.ID == "basic/range" || hasDeviation(m, r.ID) {
			want = Differ
		}
		if r.Status != want {
			t.Errorf("%s: %s, want %s", r.ID, r.Status, want)
		}
	}
}

func TestManifestRejectsBad(t *testing.T) {
	root := t.TempDir()
	dir := root + "/corpus"
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	bad := "version: 1\noracle: {libyang: v, libyang_commit: c}\nfixtures:\n" +
		"  - {id: a, dir: d, golden: g, source: {url: u, license: l}, rfc: [], areas: [nope], request: {op: x}}\n" +
		"  - {id: a, dir: d, golden: g, source: {url: u, license: l, commit: null}, rfc: [], areas: [types], request: {op: x}, assert: {verdict: valid, deviation: D-9999}}\n" +
		"  - {id: b, dir: ../d, golden: g, source: {url: u, license: l, commit: null}, rfc: [], areas: [types], request: {}, assert: {verdict: bogus}}\n" +
		"  - {id: c, dir: d, golden: g, source: {url: u, license: l, commit: null}, rfc: [], areas: [types], request: {op: x}, assert: {verdict: valid, waive: [schema_tree]}}\n" +
		"  - {id: e, dir: d, golden: g, source: {url: u, license: l, commit: null}, rfc: [], areas: [types], request: {op: x}, assert: {verdict: valid, deviation: D-0001, waive: [modules]}}\n" +
		"  - {id: f, dir: d, golden: g, source: {url: u, license: l, commit: null}, rfc: [], areas: [types], request: {op: x}, assert: {verdict: valid, deviation: D-0001, waive: [tree]}}\n"
	if err := os.WriteFile(dir+"/manifest.yaml", []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root+"/deviations.md", []byte("| D-0001 | x |\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadManifest(dir + "/manifest.yaml")
	if err == nil {
		t.Fatal("bad manifest accepted")
	}
	for _, w := range []string{"version", "unknown area", "duplicate id", "D-9999", "source.commit", "local relative", "request.op", "bogus", "waive without", "waive \"modules\" not one of"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error lacks %q: %v", w, err)
		}
	}
}

func resp(t *testing.T, js string) Response {
	t.Helper()
	r, err := ParseResponse([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClassify(t *testing.T) {
	dev := "D-0001"
	golden := resp(t, `{"verdict":"valid","result":{"type":"number","value":1}}`)
	same := golden
	otherVal := resp(t, `{"verdict":"valid","result":{"type":"number","value":2}}`)
	otherVerdict := resp(t, `{"verdict":"invalid","result":{"type":"number","value":1}}`)
	plain := Fixture{}
	asserted := Fixture{Assert: &Assert{Verdict: "valid"}}
	deviating := Fixture{Assert: &Assert{Verdict: "invalid", Deviation: &dev}}
	tests := []struct {
		name string
		f    Fixture
		got  Response
		want Status
	}{
		{"no assert, equal", plain, same, Agree},
		{"no assert, value differs", plain, otherVal, Differ},
		{"assert ok, golden equal", asserted, same, Agree},
		{"assert ok, golden value differs", asserted, otherVal, Differ},
		{"assert fails", asserted, otherVerdict, Differ},
		{"deviation: assert ok, golden differs", deviating, otherVerdict, Deviation},
		{"deviation: golden ok, assert fails", deviating, same, Differ},
		{"deviation: neither", deviating, resp(t, `{"verdict":"data-error"}`), Differ},
		{"deviation: assert ok, but non-asserted value wrong", deviating,
			resp(t, `{"verdict":"invalid","result":{"type":"number","value":2}}`), Differ},
	}
	// assert.waive: the named fields of the response and of every module are not compared
	tree := resp(t, `{"verdict":"valid","modules":[{"name":"m","schema_tree":[1],"compiled":"a"}],"result":1}`)
	treeOff := resp(t, `{"verdict":"valid","modules":[{"name":"m","schema_tree":[2],"compiled":"b"}],"result":1}`)
	waiving := Fixture{Assert: &Assert{Verdict: "valid", Deviation: &dev, Waive: []string{"schema_tree", "compiled"}}}
	waiveTree := Fixture{Assert: &Assert{Verdict: "valid", Deviation: &dev, Waive: []string{"schema_tree"}}}
	for _, tc := range []struct {
		name string
		f    Fixture
		got  Response
		want Status
	}{
		{"waive: waived fields differ", waiving, treeOff, Deviation},
		{"waive: equal is a stale waive", waiving, tree, Differ},
		{"waive: context diagnostic differs", waiving,
			resp(t, `{"verdict":"valid","context_diagnostics":[{"level":"warning"}],"modules":[{"name":"m","schema_tree":[2],"compiled":"b"}],"result":1}`), Differ},
		{"waive: an unwaived module field differs", waiveTree, treeOff, Differ},
		{"waive: other field differs", waiving, resp(t, `{"verdict":"valid","modules":[{"name":"m"}],"result":2}`), Differ},
		{"waive: module name differs", waiving, resp(t, `{"verdict":"valid","modules":[{"name":"x"}],"result":1}`), Differ},
		{"no waive: tree differs", deviating, resp(t, `{"verdict":"invalid","modules":[{"name":"m","schema_tree":[2],"compiled":"a"}],"result":1}`), Differ},
	} {
		if got, d := classify(tc.f, tree, tc.got); got != tc.want {
			t.Errorf("%s: %s (%s), want %s", tc.name, got, d, tc.want)
		}
	}
	for _, tc := range tests {
		if got, d := classify(tc.f, golden, tc.got); got != tc.want {
			t.Errorf("%s: %s (%s), want %s", tc.name, got, d, tc.want)
		}
	}
}

type fixed struct{ err error }

func (f fixed) Run(Request) (Response, error) { return nil, f.err }

func TestCompareUnsupportedAndMarkdown(t *testing.T) {
	m := load(t)
	rep, err := m.Compare(fixed{ErrUnsupported})
	if err != nil {
		t.Fatal(err)
	}
	n, nval := len(m.Fixtures), 0
	for _, f := range m.Fixtures {
		if slices.Contains(f.Areas, "validation") {
			nval++
		}
	}
	md := rep.Markdown()
	if !strings.Contains(md, fmt.Sprintf("| **fixtures** | 0 | 0 | 0 | 0 | %d |", n)) ||
		!strings.Contains(md, fmt.Sprintf("| validation | 0 | 0 | 0 | 0 | %d |", nval)) {
		t.Errorf("unexpected report:\n%s", md)
	}
	rep, _ = m.Compare(fixed{errors.New("boom")})
	if rep.Results[0].Status != Differ || rep.Results[0].Detail != "boom" {
		t.Errorf("engine error not reported as differ: %+v", rep.Results[0])
	}
}

func TestDiffResponses(t *testing.T) {
	// msg/line are ignored in diagnostics but compared in data trees.
	a := resp(t, `{"verdict":"invalid","diagnostics":[{"msg":"a","line":1,"data_path":"/x"}],"tree":{"json":"{\"line\":1}","xml":"<a/>"}}`)
	b := resp(t, `{"verdict":"invalid","diagnostics":[{"msg":"b","line":9,"data_path":"/x"}],"tree":{"json":"{\"line\":1}","xml":"<b/>"}}`)
	if d := diffResponses(a, b); d != "" {
		t.Errorf("should ignore msg/line/xml: %s", d)
	}
	c := resp(t, `{"result":{"line":1,"n":1.0}}`)
	d := resp(t, `{"result":{"line":2,"n":1}}`)
	if diffResponses(c, d) == "" {
		t.Error("data field named line must be compared")
	}
	e := resp(t, `{"result":{"line":1,"n":1}}`)
	if diffResponses(c, e) != "" {
		t.Error("1.0 and 1 should compare equal")
	}
	f := resp(t, `{"verdict":"invalid","diagnostics":[{"data_path":"/y"}]}`)
	if diffResponses(a, f) == "" {
		t.Error("data_path change must differ")
	}
}

func TestMatchAssertTypes(t *testing.T) {
	a := &Assert{Verdict: "invalid", Diagnostics: []map[string]any{{"vecode": 9, "apptag": "1"}}}
	if d := MatchAssert(a, resp(t, `{"verdict":"invalid","diagnostics":[{"vecode":9,"apptag":"1"}]}`)); d != "" {
		t.Error(d)
	}
	if MatchAssert(a, resp(t, `{"verdict":"invalid","diagnostics":[{"vecode":9,"apptag":1}]}`)) == "" {
		t.Error(`"1" must not match 1`)
	}
	if MatchAssert(a, resp(t, `{"verdict":"invalid","context_diagnostics":[{"vecode":9,"apptag":"1"}]}`)) != "" {
		t.Error("context_diagnostics must be searched")
	}
}

func TestSequenceSteps(t *testing.T) {
	base := `{"verdict":"invalid","rc":{"err":3},"failed_step":1,"steps":[` +
		`{"do":"parse","rc":{"err":0},"diagnostics":[],"tree":null},` +
		`{"do":"edit","rc":{"err":3},"diagnostics":[{"level":"error","data_path":"/m:k","msg":"a","line":1}]},` +
		`{"do":"dump","skipped":true}]}`
	step := `{"do":"dump","rc":{"err":0},"diagnostics":[],"tree":{"json":"{}","xml":"<a/>"}}`
	for _, tc := range []struct {
		name, a, b string
		same       bool // diffResponses agrees
		sameWaived bool // diffResponses agrees after withoutAsserted
	}{
		{"identical", base, base, true, true},
		{"step msg/line ignored", base, strings.Replace(base, `"msg":"a","line":1`, `"msg":"b","line":7`, 1), true, true},
		{"step data_path compared", base, strings.Replace(base, `/m:k`, `/m:x`, 1), false, true},
		{"step rc compared, waived by deviation", base, strings.Replace(base, `"do":"edit","rc":{"err":3}`, `"do":"edit","rc":{"err":0}`, 1), false, true},
		{"failed_step waived by deviation", base, strings.Replace(base, `"failed_step":1`, `"failed_step":null`, 1), false, true},
		{"skipped is not waived", base, strings.Replace(base, `{"do":"dump","skipped":true}`, step, 1), false, false},
		{"step tree.xml ignored", `{"steps":[` + step + `]}`, `{"steps":[` + strings.Replace(step, `<a/>`, `<b/>`, 1) + `]}`, true, true},
		{"step tree.json compared", `{"steps":[` + step + `]}`, `{"steps":[` + strings.Replace(step, `"json":"{}"`, `"json":"{ }"`, 1) + `]}`, false, false},
	} {
		a, b := resp(t, tc.a), resp(t, tc.b)
		if got := diffResponses(a, b) == ""; got != tc.same {
			t.Errorf("%s: diffResponses equal=%v, want %v", tc.name, got, tc.same)
		}
		if got := diffResponses(withoutAsserted(a), withoutAsserted(b)) == ""; got != tc.sameWaived {
			t.Errorf("%s: waived equal=%v, want %v", tc.name, got, tc.sameWaived)
		}
	}
	// asserts see step diagnostics
	a := &Assert{Verdict: "invalid", Diagnostics: []map[string]any{{"level": "error", "data_path": "/m:k"}}}
	if d := MatchAssert(a, resp(t, base)); d != "" {
		t.Errorf("step diagnostic not matched: %s", d)
	}
	// normalize must not modify the response it is given
	r := resp(t, base)
	normalize(r)
	if len(r["steps"].([]any)[1].(map[string]any)["diagnostics"].([]any)[0].(map[string]any)) != 4 {
		t.Error("normalize mutated its input")
	}
}

// Two opaque XML elements that differ only in namespace must not compare equal (tree.xml, the
// only rendering with the namespace, is not compared; typed[].opaque carries it).
func TestOpaqueNamespaceDiffers(t *testing.T) {
	m := load(t)
	golden := func(id string) Response {
		for _, f := range m.Fixtures {
			if f.ID == id {
				r, err := LoadGolden(m.GoldenPath(f))
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
		}
		t.Fatalf("fixture %s missing", id)
		return nil
	}
	for _, set := range []string{"opaque-xml-ns", "anydata-xml-ns"} {
		a, b := golden("protocol-v2/"+set+"-a"), golden("protocol-v2/"+set+"-b")
		if diffResponses(a, b) == "" {
			t.Errorf("%s: nodes in different XML namespaces compare equal", set)
		}
		if diffResponses(withoutAsserted(a), withoutAsserted(b)) == "" {
			t.Errorf("%s: a deviation must not waive the namespace", set)
		}
	}
}

// Malformed sequence requests are request-errors (exit 2), found before any step runs. Needs the
// built oracle (oracle/lyoracle, i.e. make oracle-check); skipped without it.
func TestOracleRequestErrors(t *testing.T) {
	oracle, err := filepath.Abs(OracleBinary("oracle"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oracle); err != nil {
		t.Skip("oracle not built")
	}
	const head = `{"op":"sequence","base_dir":"corpus/protocol-v2","searchdirs":["schemas"],"modules":[{"name":"pv2"}],"steps":[`
	for _, tc := range []struct{ name, steps, want string }{
		{"empty key (issue #5)", `{"do":"dump","":1}`, `unknown key ""`},
		{"empty key in set", `{"do":"edit","set":{"path":"/pv2:c/mode","":1}}`, `unknown key ""`},
		{"prefix of an allowed key", `{"do":"parse","data":"{}","dat":1}`, `unknown key "dat"`},
		{"merge option on set", `{"do":"edit","set":{"path":"/pv2:c/mode","value":"on"},"format":"garbage"}`, `key "format" not allowed`},
		{"merge option on delete", `{"do":"edit","delete":"/pv2:c/mode","data_type":"config"}`, `key "data_type" not allowed`},
		{"malformed step after a failing one", `{"do":"edit","delete":"/pv2:c/nope"},{"do":"dump","with_defaults":"bogus"}`, `unknown with_defaults mode`},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, oracle) //nolint:gosec // test runs the repo's own oracle binary
		cmd.Stdin = strings.NewReader(head + tc.steps + "]}")
		out, err := cmd.Output()
		cancel()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 2 {
			t.Errorf("%s: want exit 2, got %v", tc.name, err)
			continue
		}
		r, err := ParseResponse(out)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if err := CheckOracleArch(r, false); err != nil {
			t.Fatal(err)
		}
		if msg, _ := r["request_error"].(string); r.Verdict() != "request-error" || !strings.Contains(msg, tc.want) {
			t.Errorf("%s: verdict %q, request_error %q, want %q", tc.name, r.Verdict(), msg, tc.want)
		}
	}
}

func TestParseResponseTrailing(t *testing.T) {
	if _, err := ParseResponse([]byte(`{"a":1} {"b":2}`)); err == nil {
		t.Error("trailing data accepted")
	}
}

func TestFixtureRunsOn(t *testing.T) {
	if !(Fixture{}).RunsOn("arm64") || !(Fixture{Host: "amd64"}).RunsOn("amd64") || (Fixture{Host: "amd64"}).RunsOn("arm64") {
		t.Fatal("RunsOn: an unpinned fixture runs everywhere, a pinned one only on its host")
	}
}

func TestHostFixtureIsPinned(t *testing.T) {
	m := load(t)
	for _, f := range m.Fixtures {
		if f.ID == "types/date-time-sort-wide" && f.Host != "amd64" {
			t.Errorf("%s: host = %q, want amd64 (D-0028)", f.ID, f.Host)
		}
	}
}
