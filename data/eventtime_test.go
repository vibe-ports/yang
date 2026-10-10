// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// TestEventTimeGoldens: an eventTime node (LYD_INTOPT_EVENTTIME) is validated as the oracle stores
// the same value into the yang:date-and-time leaf (ops/eventtime-*): the same verdict and the same
// error at /yang:date-and-time, in JSON and XML. The eventTime node is an opaque top-level node.
func TestEventTimeGoldens(t *testing.T) {
	c, _, err := compile.NewContext(compile.Options{})
	if err != nil {
		t.Fatal(err)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		if m.Schema != nil {
			set.Modules = append(set.Modules, m.Schema)
		}
	}
	paths, _ := filepath.Glob(filepath.Join(opsManifest, "eventtime-[0-9][0-9].yaml"))
	if len(paths) < 10 {
		t.Fatalf("%d eventtime fixtures", len(paths))
	}
	for _, path := range paths {
		f := readOpsFixture(t, path)
		var in map[string]string
		if err := json.Unmarshal([]byte(f.data), &in); err != nil {
			t.Fatal(err)
		}
		value := in["yang:date-and-time"]
		b, err := os.ReadFile(filepath.Join("../conformance/corpus", f.dir, f.golden))
		if err != nil {
			t.Fatal(err)
		}
		var want opsGolden
		if err := json.Unmarshal(b, &want); err != nil {
			t.Fatal(err)
		}
		var wantDiags []string
		for _, d := range want.Diagnostics {
			// ly_time_str2time prints the timezone from the value pointer with %s, past value_len:
			// in the oracle's data parse that is the rest of the JSON input ("}), while the eventTime
			// value is a NUL-terminated string (lyd_get_value) and prints empty
			msg := strings.Replace(d.Msg, `timezone hour ""}".`, `timezone hour "".`, 1)
			wantDiags = append(wantDiags, fmt.Sprintf("%s %s", *d.SchemaPath, msg))
		}
		for fmtName, fp := range map[string]formatParser{"json": parseJSON, "xml": parseXML} {
			src, _ := json.Marshal(map[string]string{"eventTime": value})
			if fmtName == "xml" {
				src = []byte("<eventTime>" + value + "</eventTime>")
			}
			tree, diags, err := parseWith(context.Background(), strings.NewReader(string(src)), set,
				parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: true}}, fp,
				func(lc *lydCtx) { lc.op.eventTime = true })
			name := filepath.Base(path) + " " + fmtName
			if (err == nil) != (want.Verdict == "valid") {
				t.Errorf("%s: err %v, verdict %s", name, err, want.Verdict)
				continue
			}
			var got []string
			for _, d := range diags {
				got = append(got, fmt.Sprintf("%s %s", d.SchemaPath, d.Msg))
			}
			if !reflect.DeepEqual(got, wantDiags) {
				t.Errorf("%s:\n got  %q\n want %q", name, got, wantDiags)
			}
			if err == nil {
				var nodes []*Node
				for n := range tree.Top() {
					nodes = append(nodes, n)
				}
				if len(nodes) != 1 || nodes[0].opaq == nil || nodes[0].opaq.Name != "eventTime" || nodes[0].opaq.Value != value {
					t.Errorf("%s: nodes %v", name, nodes)
				}
			}
		}
	}
}

// TestEventTimeShape: a JSON eventTime must be a string; the eventTime branch is taken only at
// the top level, without a prefix and with LYD_INTOPT_EVENTTIME.
func TestEventTimeShape(t *testing.T) {
	c, _, err := compile.NewContext(compile.Options{})
	if err != nil {
		t.Fatal(err)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		if m.Schema != nil {
			set.Modules = append(set.Modules, m.Schema)
		}
	}
	parse := func(src string, fp formatParser, et bool) []string {
		_, diags, _ := parseWith(context.Background(), strings.NewReader(src), set,
			parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: true}}, fp,
			func(lc *lydCtx) { lc.op.eventTime = et })
		var out []string
		for _, d := range diags {
			out = append(out, d.Code+" "+d.Msg)
		}
		return out
	}
	for _, tc := range []struct {
		src  string
		fp   formatParser
		et   bool
		want []string
	}{
		{`{"eventTime":5}`, parseJSON, true, []string{"LYVE_SYNTAX_JSON Expecting JSON string but number found."}},
		{`{"eventTime":"2010-12-06T08:00:01Z"}`, parseJSON, false,
			[]string{`LYVE_SYNTAX_JSON Top-level JSON object member "eventTime" must be namespace-qualified.`}},
		{`<eventTime xmlns="urn:x">2010-12-06T08:00:01Z</eventTime>`, parseXML, false,
			[]string{`LYVE_REFERENCE No module with namespace "urn:x" in the context.`}},
	} {
		if got := parse(tc.src, tc.fp, tc.et); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.src, got, tc.want)
		}
	}
}

// TestEventTimeEnvelopeGoldens replays ops/eventtime-env-*: the oracle parses a NETCONF or
// RESTCONF notification envelope (lyd_parse_op with LYD_TYPE_NOTIF_NETCONF/RESTCONF), whose
// content libyang parses with LYD_INTOPT_NOTIF | LYD_INTOPT_EVENTTIME | LYD_INTOPT_WITH_SIBLINGS.
// The envelope itself is M5, so the test strips it and parses the content with those options:
// the diagnostics, the eventTime opaque node (name, namespace or module, format, value, hints)
// and the notification tree (op_typed) must equal the oracle's.
func TestEventTimeEnvelopeGoldens(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join(opsManifest, "eventtime-env-*.yaml"))
	if len(paths) < 14 {
		t.Fatalf("%d fixtures", len(paths))
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			f := readOpsFixture(t, path)
			set := opsSet(t, f)
			b, err := os.ReadFile(filepath.Join("../conformance/corpus", f.dir, f.golden))
			if err != nil {
				t.Fatal(err)
			}
			var want struct {
				opsGolden
				Typed []struct {
					Path   string `json:"path"`
					Opaque *struct {
						Name, Format      string
						Namespace, Module *string
					}
					Value *struct {
						Canonical string
						Hints     []string
					}
				} `json:"typed"`
				OpTyped json.RawMessage `json:"op_typed"`
			}
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			var opWant goldenStep
			if want.OpTyped != nil {
				if err := json.Unmarshal([]byte(`{"typed":`+string(want.OpTyped)+`}`), &opWant); err != nil {
					t.Fatal(err)
				}
			}
			src, fp := f.data, parseJSON
			if f.format == "xml" {
				fp = parseXML
				src = strings.TrimSuffix(strings.TrimPrefix(src,
					`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">`), `</notification>`)
			} else {
				src = strings.TrimSuffix(strings.TrimPrefix(src, `{"ietf-restconf:notification":`), `}`)
			}
			tree, diags, err := parseWith(context.Background(), strings.NewReader(src), set,
				parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: true}}, fp,
				func(lc *lydCtx) { lc.op = opOpts{notif: true, eventTime: true} })
			if (err == nil) != (want.Verdict == "valid") {
				t.Fatalf("err %v, verdict %s", err, want.Verdict)
			}
			var got []string
			for _, d := range diags {
				got = append(got, fmt.Sprintf("%s %s %s %s", d.Code, d.DataPath, d.SchemaPath, d.Msg))
			}
			if !reflect.DeepEqual(got, want.diags()) {
				t.Errorf("diagnostics\n got  %q\n want %q", got, want.diags())
			}
			if err != nil {
				return
			}
			var et *Node
			for n := range tree.Top() {
				if n.opaq != nil && n.opaq.Name == "eventTime" {
					et = n
				}
			}
			w := want.Typed[1] // /notification/eventTime under the envelope
			ns := w.Opaque.Namespace
			if ns == nil {
				ns = w.Opaque.Module
			}
			wantNS := ""
			if ns != nil {
				wantNS = *ns
			}
			hints := types.HintData
			if len(w.Value.Hints) == 1 && w.Value.Hints[0] == "string" {
				hints = types.HintString
			}
			if et == nil || w.Path != "/notification/eventTime" || et.opaq.ModuleNS != wantNS || et.opaq.Value != w.Value.Canonical ||
				map[types.Format]string{types.FormatXML: "xml", types.FormatJSON: "json"}[et.opaq.Format] != w.Opaque.Format ||
				et.opaq.Hints != hints {
				t.Errorf("eventTime %+v, want %+v %+v %s", et, *w.Opaque, *w.Value, wantNS)
			}
			if et != nil {
				unlink(et) // libyang moves it under the envelope
			}
			if g := typedDump(tree); !reflect.DeepEqual(g, opWant.dump()) {
				t.Errorf("notification\n got  %q\n want %q", g, opWant.dump())
			}
		})
	}
}
