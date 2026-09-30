// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"
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
		if d == "" && f.Assert.Deviation != nil {
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
	for _, r := range rep.Results {
		if r.Status != Agree {
			t.Errorf("%s: %s %s", r.ID, r.Status, r.Detail)
		}
	}
	if len(rep.Results) != len(m.Fixtures) || !strings.Contains(rep.Markdown(), fmt.Sprintf("| **fixtures** | %d | 0 | 0 | 0 |", len(m.Fixtures))) {
		t.Errorf("unexpected report:\n%s", rep.Markdown())
	}
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
		if r.ID == "basic/range" {
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
		"  - {id: b, dir: ../d, golden: g, source: {url: u, license: l, commit: null}, rfc: [], areas: [types], request: {}, assert: {verdict: bogus}}\n"
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
	for _, w := range []string{"version", "unknown area", "duplicate id", "D-9999", "source.commit", "local relative", "request.op", "bogus"} {
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
	n := len(m.Fixtures)
	md := rep.Markdown()
	if !strings.Contains(md, fmt.Sprintf("| **fixtures** | 0 | 0 | 0 | %d |", n)) || !strings.Contains(md, "| validation | 0 | 0 | 0 | 4 |") {
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

func TestParseResponseTrailing(t *testing.T) {
	if _, err := ParseResponse([]byte(`{"a":1} {"b":2}`)); err == nil {
		t.Error("trailing data accepted")
	}
}
