// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// Request identifies one fixture run. Params is the lyoracle request verbatim; BaseDir is the
// fixture directory (absolute when the manifest path is).
type Request struct {
	ID      string
	BaseDir string
	Params  map[string]any
}

// Engine is a YANG implementation under test. Return ErrUnsupported for inputs it cannot handle.
type Engine interface {
	Run(Request) (Response, error)
}

// ErrUnsupported marks a fixture the engine does not implement (yet).
var ErrUnsupported = errors.New("unsupported")

// Status is the outcome of one fixture.
type Status int

// Outcomes, in report column order.
const (
	Agree Status = iota
	Differ
	Deviation
	Unsupported
	AgreeSkipped // agrees once the fields the engine does not produce (FieldSkipper) are dropped
)

func (s Status) String() string {
	return [...]string{"agree", "differ", "deviation", "unsupported", "agree (skipped fields)"}[s]
}

// Result is one fixture's outcome.
type Result struct {
	ID     string
	Areas  []string
	Status Status
	Detail string
}

// Report is the outcome of Compare.
type Report struct{ Results []Result }

// Compare runs every fixture through e (see manifest.schema.md for the rules).
func (m *Manifest) Compare(e Engine) (Report, error) {
	var rep Report
	for _, f := range m.Fixtures {
		golden, err := LoadGolden(m.GoldenPath(f))
		if err != nil {
			return rep, err
		}
		got, err := e.Run(Request{ID: f.ID, BaseDir: fixtureDir(m, f), Params: f.Request})
		r := Result{ID: f.ID, Areas: f.Areas}
		switch {
		case errors.Is(err, ErrUnsupported):
			r.Status = Unsupported
		case err != nil:
			r.Status, r.Detail = Differ, err.Error()
		default:
			var skipped []string
			if fs, ok := e.(FieldSkipper); ok {
				op, _ := f.Request["op"].(string)
				golden, skipped = withoutSkipped(golden, fs.SkippedFields(op))
				got, _ = withoutSkipped(got, fs.SkippedFields(op))
			}
			r.Status, r.Detail = classify(f, golden, got)
			if r.Status == Agree && len(skipped) > 0 {
				r.Status, r.Detail = AgreeSkipped, "skipped: "+strings.Join(skipped, ", ")
			}
		}
		rep.Results = append(rep.Results, r)
	}
	return rep, nil
}

// classify: without a deviation the engine must match the assert (if any) and the whole
// normalized golden. With a deviation (libyang differs from the spec on purpose) matching the
// assert but not the golden is Deviation; not matching the assert is Differ. A deviation with
// assert.waive waives only the named fields, everything else (verdict and diagnostics too) must
// match; a waiving fixture that matches the whole golden has a stale waive (Differ).
func classify(f Fixture, golden, got Response) (Status, string) {
	assertDiff, goldenDiff := "", diffResponses(golden, got)
	if f.Assert != nil {
		assertDiff = MatchAssert(f.Assert, got)
	}
	switch {
	case assertDiff != "":
		return Differ, assertDiff
	case goldenDiff == "" && f.Assert != nil && len(f.Assert.Waive) > 0:
		return Differ, fmt.Sprintf("stale waive %v: the result matches the golden", f.Assert.Waive)
	case goldenDiff == "":
		return Agree, ""
	case f.Assert != nil && f.Assert.Deviation != nil && len(f.Assert.Waive) > 0:
		if d := diffResponses(withoutFields(golden, f.Assert.Waive), withoutFields(got, f.Assert.Waive)); d != "" {
			return Differ, d
		}
		return Deviation, *f.Assert.Deviation
	case f.Assert != nil && f.Assert.Deviation != nil:
		// The deviation only waives what the assert covers (verdict, rc, diagnostics); the rest
		// of the golden (trees, xpath results, diffs) must still match, unless the verdict flips:
		// then everything derived from the accepted result follows from it.
		g, r := withoutAsserted(golden), withoutAsserted(got)
		if !sameJSON(golden["verdict"], got["verdict"]) {
			g, r = withoutDerived(g, r, firstFailed(golden, got))
		}
		if d := diffResponses(g, r); d != "" {
			return Differ, d
		}
		return Deviation, *f.Assert.Deviation
	default:
		return Differ, goldenDiff
	}
}

func fixtureDir(m *Manifest, f Fixture) string { return m.corpus + "/" + f.Dir }

// MatchAssert returns "" if resp satisfies a, else the first mismatch.
func MatchAssert(a *Assert, resp Response) string {
	if v := resp.Verdict(); v != a.Verdict {
		return fmt.Sprintf("verdict %q, want %q", v, a.Verdict)
	}
	diags := resp.Diagnostics()
	for _, want := range a.Diagnostics {
		if !slices.ContainsFunc(diags, func(d map[string]any) bool { return subset(want, d) }) {
			return fmt.Sprintf("no diagnostic matching %v", want)
		}
	}
	return ""
}

// withoutSkipped drops the fields of a response and of its module and step items; skipped are
// those that were there. A dotted field ("typed.value.type") goes into the objects, or the
// lists of objects, under its first parts.
func withoutSkipped(r Response, fields []string) (Response, []string) {
	if len(fields) == 0 {
		return r, nil
	}
	o := maps.Clone(map[string]any(r))
	var skipped []string
	drop := func(m map[string]any) {
		for _, f := range fields {
			if dropPath(m, strings.Split(f, ".")) && !slices.Contains(skipped, f) {
				skipped = append(skipped, f)
			}
		}
	}
	drop(o)
	mapItems(o, "modules", drop)
	mapItems(o, "steps", drop)
	return o, skipped
}

// dropPath deletes the field path from m in place (cloning what it edits); true if it was there.
func dropPath(m map[string]any, path []string) bool {
	v, ok := m[path[0]]
	if !ok {
		return false
	}
	if len(path) == 1 {
		delete(m, path[0])
		return true
	}
	found := false
	switch x := v.(type) {
	case map[string]any:
		c := maps.Clone(x)
		found = dropPath(c, path[1:])
		m[path[0]] = c
	case []any:
		l := make([]any, len(x))
		for i, it := range x {
			l[i] = it
			if im, isMap := it.(map[string]any); isMap {
				c := maps.Clone(im)
				if dropPath(c, path[1:]) {
					found = true
				}
				l[i] = c
			}
		}
		m[path[0]] = l
	}
	return found
}

// withoutAsserted drops what a deviation waives: the verdict with its rc and failed_step, every
// diagnostic list, and each sequence step's rc and diagnostics. Step trees and `skipped` stay
// compared.
func withoutAsserted(r Response) Response {
	o := maps.Clone(map[string]any(r))
	for _, k := range []string{"verdict", "rc", "failed_step", "diagnostics", "context_diagnostics"} {
		delete(o, k)
	}
	mapItems(o, "steps", func(s map[string]any) {
		delete(s, "rc")
		delete(s, "diagnostics")
	})
	return o
}

// flippedModuleFields are the fields of a module item whose acceptance flipped that follow from
// it: the flip itself (accepted, phase, rc, the module's diagnostics) and what only an accepted
// module has (revision, schema_tree, compiled, features, identities).
var flippedModuleFields = []string{"accepted", "phase", "rc", "diagnostics", "revision", "schema_tree", "compiled",
	"features", "identities"}

// withoutDerived drops, from a golden g and a result r whose verdicts differ under a deviation,
// what follows from the verdict: tree and typed; the fields of module items (paired by
// position) whose acceptance flipped, other module items stay compared; and the sequence steps
// from the first failing one (first, -1 for none) on, earlier steps stay compared.
func withoutDerived(g, r Response, first int) (Response, Response) {
	g, r = maps.Clone(map[string]any(g)), maps.Clone(map[string]any(r))
	for _, o := range []map[string]any{g, r} {
		delete(o, "tree")
		delete(o, "typed")
	}
	gm, _ := g["modules"].([]any)
	rm, _ := r["modules"].([]any)
	gm, rm = slices.Clone(gm), slices.Clone(rm)
	for i := range min(len(gm), len(rm)) {
		a, aok := gm[i].(map[string]any)
		b, bok := rm[i].(map[string]any)
		if !aok || !bok || sameJSON(a["accepted"], b["accepted"]) {
			continue
		}
		a, b = maps.Clone(a), maps.Clone(b)
		for _, k := range flippedModuleFields {
			delete(a, k)
			delete(b, k)
		}
		gm[i], rm[i] = a, b
	}
	if gm != nil {
		g["modules"] = gm
	}
	if rm != nil {
		r["modules"] = rm
	}
	if first >= 0 {
		for _, o := range []map[string]any{g, r} {
			if st, ok := o["steps"].([]any); ok && len(st) > first {
				o["steps"] = st[:first]
			}
		}
	}
	return g, r
}

// firstFailed is the earlier failed_step of golden and got, -1 when neither has one.
func firstFailed(golden, got Response) int {
	first := -1
	for _, o := range []Response{golden, got} {
		if f, err := strconv.Atoi(fmt.Sprint(o["failed_step"])); err == nil && (first < 0 || f < first) {
			first = f
		}
	}
	return first
}

// Waivable are the response fields assert.waive may name.
var Waivable = []string{"schema_tree", "compiled", "tree", "typed"}

// withoutFields drops the fields of an assert.waive at the top level and in every module.
func withoutFields(r Response, fields []string) Response {
	o := maps.Clone(map[string]any(r))
	for _, k := range fields {
		delete(o, k)
	}
	mapItems(o, "modules", func(m map[string]any) {
		for _, k := range fields {
			delete(m, k)
		}
	})
	return o
}

// hasField reports whether r has field at the top level or in some module.
func hasField(r Response, field string) bool {
	if _, ok := r[field]; ok {
		return true
	}
	l, _ := r["modules"].([]any)
	for _, e := range l {
		if m, ok := e.(map[string]any); ok {
			if _, ok := m[field]; ok {
				return true
			}
		}
	}
	return false
}

// mapItems replaces m[key] (a list of objects) by clones edited by f.
func mapItems(m map[string]any, key string, f func(map[string]any)) {
	l, ok := m[key].([]any)
	if !ok {
		return
	}
	out := make([]any, len(l))
	for i, e := range l {
		if d, ok := e.(map[string]any); ok {
			d = maps.Clone(d)
			f(d)
			e = d
		}
		out[i] = e
	}
	m[key] = out
}

// sameJSON compares by JSON form after number canonicalisation, so 1, 1.0 and a yaml int are
// equal but "1" != 1.
func sameJSON(a, b any) bool {
	x, err1 := json.Marshal(canonNumbers(a))
	y, err2 := json.Marshal(canonNumbers(b))
	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

func subset(want, got map[string]any) bool {
	for k, v := range want {
		g, ok := got[k]
		if !ok || !sameJSON(v, g) {
			return false
		}
	}
	return true
}

// diffResponses compares what PLAN §5 fixes: everything except msg, line, libyang version and
// the XML tree rendering (also inside sequence steps). "" = equal.
func diffResponses(want, got Response) string {
	w, g := normalize(want), normalize(got)
	if reflect.DeepEqual(w, g) {
		return ""
	}
	for _, k := range slices.Sorted(maps.Keys(w)) {
		if !reflect.DeepEqual(w[k], g[k]) {
			return "field " + k + " differs"
		}
	}
	return "extra fields in response"
}

func normalize(r Response) map[string]any {
	o := maps.Clone(map[string]any(r))
	// msg/line are stripped only inside diagnostic items, never from data trees.
	stripDiags(o, "diagnostics")
	stripDiags(o, "context_diagnostics")
	mapItems(o, "modules", func(m map[string]any) { stripDiags(m, "diagnostics") })
	delete(o, "libyang")
	delete(o, "arch")
	dropXML(o)
	mapItems(o, "steps", func(s map[string]any) {
		stripDiags(s, "diagnostics")
		dropXML(s)
	})
	return canonNumbers(o).(map[string]any)
}

// dropXML removes the XML rendering of m["tree"]; the JSON one is compared.
func dropXML(m map[string]any) {
	if t, ok := m["tree"].(map[string]any); ok {
		t = maps.Clone(t)
		delete(t, "xml")
		m["tree"] = t
	}
}

func stripDiags(m map[string]any, key string) {
	mapItems(m, key, func(d map[string]any) {
		delete(d, "msg")
		delete(d, "line")
	})
}

// canonNumbers makes 1, 1.0 and 1e0 compare equal (ponytail: float64, exact to 2^53).
func canonNumbers(v any) any {
	switch v := v.(type) {
	case map[string]any:
		o := make(map[string]any, len(v))
		for k, x := range v {
			o[k] = canonNumbers(x)
		}
		return o
	case []any:
		o := make([]any, len(v))
		for i, x := range v {
			o[i] = canonNumbers(x)
		}
		return o
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	}
	return v
}

// Markdown renders the per-area table (fixtures with several areas count in each) followed by
// every non-agreeing fixture.
func (r Report) Markdown() string {
	type tally [5]int
	byArea := map[string]*tally{}
	var total tally
	for _, x := range r.Results {
		total[x.Status]++
		for _, a := range x.Areas {
			if byArea[a] == nil {
				byArea[a] = &tally{}
			}
			byArea[a][x.Status]++
		}
	}
	var b strings.Builder
	b.WriteString("| area | agree | agree (skipped fields) | differ | deviation | unsupported |\n|---|--:|--:|--:|--:|--:|\n")
	for _, a := range slices.Sorted(maps.Keys(byArea)) {
		t := byArea[a]
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d |\n", a, t[Agree], t[AgreeSkipped], t[Differ], t[Deviation], t[Unsupported])
	}
	fmt.Fprintf(&b, "| **fixtures** | %d | %d | %d | %d | %d |\n", total[Agree], total[AgreeSkipped], total[Differ],
		total[Deviation], total[Unsupported])
	for _, x := range r.Results {
		if x.Status != Agree {
			fmt.Fprintf(&b, "\n- `%s`: %s %s", x.ID, x.Status, x.Detail)
		}
	}
	b.WriteString("\n")
	return b.String()
}

// Replay is an Engine that returns the goldens: it proves the plumbing, nothing more.
type Replay struct{ golden map[string]Response }

// NewReplay loads every golden of m.
func NewReplay(m *Manifest) (*Replay, error) {
	rp := &Replay{golden: map[string]Response{}}
	for _, f := range m.Fixtures {
		g, err := LoadGolden(m.GoldenPath(f))
		if err != nil {
			return nil, err
		}
		rp.golden[f.ID] = g
	}
	return rp, nil
}

// Run implements Engine.
func (rp *Replay) Run(r Request) (Response, error) {
	g, ok := rp.golden[r.ID]
	if !ok {
		return nil, ErrUnsupported
	}
	return g, nil
}
