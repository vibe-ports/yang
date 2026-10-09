// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/xpath"
)

// metaXPathData is the data of the conformance fixtures ut-xpath/meta-*: the l1 instances of
// test_xpath.c's test_axes with xml:lang (module xml, ut-xpath/meta.1) and ietf-origin metadata.
const metaXPathData = `<l1 xmlns="urn:tests:a" xmlns:yang="urn:ietf:params:xml:ns:yang:1" xmlns:xml="http://www.w3.org/XML/1998/namespace" xmlns:or="urn:ietf:params:xml:ns:yang:ietf-origin" xml:lang="en-US">
  <a yang:operation="none" yang:key="[no-key='no-value']" yang:value="v">a1</a>
  <b or:origin="or:learned" xml:lang="de">b1</b>
  <c yang:operation="replace">c1</c>
</l1>
<c xmlns="urn:tests:a" xmlns:xml="http://www.w3.org/XML/1998/namespace" xmlns:yang="urn:ietf:params:xml:ns:yang:1"><x xml:lang="EN" yang:operation="delete">val</x></c>
<foo xmlns="urn:tests:a">f</foo>
`

// metaXPathCases are the fixtures ut-xpath/meta-NN in order: context path and expression.
var metaXPathCases = [][2]string{
	{"", "count(//@*)"},
	{"", "string(//@*[2])"},
	{"", "count(//@yang:*)"},
	{"", "count(/@*)"},
	{"", "count(//@operation)"},
	{"", "string(//l1/c/@yang:operation)"},
	{"", "string(//l1/a/@yang:key)"},
	{"", "count(//l1/a/@*)"},
	{"", "count(//l1/*/@*)"},
	{"", "string(//@yang:operation[2])"},
	{"", "string((//l1/a | //l1/c/@*)[2])"},
	{"", "local-name(//l1/a/@*)"},
	{"", "name(//l1/b/@*)"},
	{"", "namespace-uri(//@yang:operation)"},
	{"", "namespace-uri(//@xml:lang)"},
	{"", "//l1[c/@yang:operation='replace']/a"},
	{"", "//@yang:operation/.."},
	{"", "count(//@yang:operation/ancestor::*)"},
	{"", "count(//@yang:operation[. = 'none'])"},
	{"", "count(//@*/text())"},
	{"", "count(//@yang:operation/*)"},
	{"", "count(//x/@*/following::*)"},
	{"", "string(//@ietf-origin:origin)"},
	{"", "derived-from(//@ietf-origin:origin, 'ietf-origin:origin')"},
	{"", "derived-from(//l1/b/@ietf-origin:origin, 'ietf-origin:learned')"},
	{"", "//@nope:x"},
	{"", "sum(//@yang:operation)"},
	{"", "//@yang:operation = 'delete'"},
	{"/a:c/x", "lang('en')"},
	{"/a:c/x", "lang('en-')"},
	{"/a:c", "lang('en')"},
	{"/a:l1/b", "lang('DE')"},
	{"/a:l1/a", "lang('en-us')"},
	{"/a:l1/a", "lang('')"},
	{"/a:l1", "count(@*[lang('en')])"},
	{"/a:foo", "lang('en')"},
	{"", "lang('en')"},
	{"", "//l1/*[lang('de')]"},
	{"", "concat(name((//l1/*/@*)[2]), ' ', name((//l1/*/@*)[3]), ' ', count(//l1/*/@*))"},
	{"", "name((//l1/*/@* | //foo)[2])"},
	{"", "name((//l1/*/@* | //foo)[3])"},
	{"", "count(//l1/*/@* | //l1/*/@*)"},
	{"", "name(((//l1/* | //foo)/@* | //foo)[3])"},
	{"", "count(//x/text()[lang('en')])"},
	{"", "string(//l1/a/@*[last()])"},
	{"", "//@yang:operation != 'none'"},
	{"", "//l1/a/@yang:operation = //l1/c/@yang:operation"},
	{"", "count(//l1/@*)"},
	{"", "name(//@xml:lang)"},
	{"", "count(//l1/c/@yang:operation/ancestor-or-self::node())"},
	{"", "count(//l1/c/@yang:operation/self::node())"},
	{"", "count(//l1/c/@yang:operation/preceding-sibling::*)"},
	{"", "count(//l1/c/@yang:operation/following-sibling::*)"},
	{"", "count(//l1/c/@yang:operation/preceding::*)"},
	{"", "count(//l1/c | //l1/c/@*)"},
	{"", "string(//l1/b/@*[2])"},
	{"", "count(//l1/b/@*)"},
	{"", "name((//l1/a/@* | //l1/c)[2])"},
	{"", "count(//l1/a/@*/..)"},
	{"/a:l1/a", "count(@*)"},
	{"/a:l1/a", "lang('EN')"},
	{"", "count(/descendant-or-self::node()/@*)"},
	{"", "count(//l1//@*)"},
	{"", "name(((//l1/* | //foo)/@* | //l1/b/@*)[3])"},
	{"", "concat(name(((//l1/* | //foo)/@* | //l1/b/@*)[3]), ' ', name(((//l1/* | //foo)/@* | //l1/b/@*)[4]), ' ', count((//l1/* | //foo)/@* | //l1/b/@*))"},
	{"", "name((//l1/*/@* | //l1/b/@*)[3])"},
	{"", "count(//l1/*/@* | //l1/a/@*)"},
	{"", "concat(name((//l1/a/@* | //l1/*/@*)[2]), ' ', name((//l1/a/@* | //l1/*/@*)[3]))"},
	{"", "name(((//l1/*/following-sibling::* | //foo)/@* | //l1/b/@*)[3])"},
	{"", "name(((//l1/* | //foo)/@* | //l1/a/@yang:value)[4])"},
	{"", "count((//l1/* | //foo)/@* | (//l1/* | //foo)/@*)"},
	{"", "name(((/a:l1 | /a:c)//*/@* | //l1/b/@*)[3])"},
	{"", "concat(name(((/a:l1 | /a:c)//*/@* | //l1/b/@*)[3]), ' ', name(((/a:l1 | /a:c)//*/@* | //l1/b/@*)[4]))"},
	{"", "name(((//l1/* | //foo)/@* | //l1/b/@* | //l1/a/@*)[5])"},
	{"", "concat(name((((//l1/* | //foo)/@* | //foo) | //l1/b/@*)[3]), ' ', count(((//l1/* | //foo)/@* | //foo) | //l1/b/@*))"},
	{"", "name(((//l1/* | //foo)/@*[1] | //l1/b/@*)[2])"},
	{"", "name(((//l1/* | //foo)//@* | //l1/b/@*)[3])"},
	{"", "name((//l1/b/@* | (//l1/* | //foo)/@*)[3])"},
	{"", "name(((//l1/*/following-sibling::*/preceding-sibling::*)/@* | //l1/b/@*)[3])"},
	{"", "count(((//l1/* | //foo)/@*)/.. | //l1/b)"},
	{"", "name(((//l1/* | //foo)/@* | //l1/b | //l1/b/@*)[4])"},
	{"", "name((//l1/*//@* | //l1/b/@*)[3])"},
	{"", "concat(name((//l1/*//@* | //l1/b/@*)[3]), ' ', name((//l1/*//@* | //l1/b/@*)[4]), ' ', count(//l1/*//@* | //l1/b/@*))"},
	{"", "name((/a:l1[a='a1'][b='b1']/*/@* | //l1/b/@*)[3])"},
	{"", "name(((/a:l1[a='a1'][b='b1'] | //foo)/*/@* | //l1/b/@*)[3])"},
	{"", "name(((//foo | /a:l1[a='a1'][b='b1'])/*/@* | //l1/b/@*)[3])"},
	{"", "name(((/a:l1[a='a1'][b='b1'] | /a:c)//*/@* | //l1/b/@*)[3])"},
	{"", "name((/a:l1[a='a1'][b='b1']//@* | //l1/b/@*)[3])"},
	{"", "name(((/a:l1[a='a1'][b='b1'] | //foo)//@* | //l1/b/@*)[3])"},
	{"", "name((/a:l1[a='a1'][b='b1']/a/@* | /a:l1[a='a1'][b='b1']/b/@*)[2])"},
	{"", "count(/a:l1[a='a1'][b='b1']/*/@* | (/a:l1[a='a1'][b='b1'] | //foo)/*/@*)"},
}

// TestXPathMeta: the attribute axis, the metadata items of a node set and lang() (moveto_attr,
// moveto_attr_alldesc, get_meta_pos, xpath_lang) over the data tree, against the goldens of the
// conformance fixtures ut-xpath/meta-* (libyang v5.8.6 through the oracle).
func TestXPathMeta(t *testing.T) {
	corpus := filepath.Join("..", "conformance", "corpus")
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true},
		os.DirFS(filepath.Join(corpus, "ut-xpath", "a.1")), os.DirFS(filepath.Join(corpus, "ut-xpath", "meta.1")),
		os.DirFS(filepath.Join(corpus, "ietf")))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "xml", "ietf-origin"} {
		if _, diags, err := c.Load(name, "", nil); err != nil {
			t.Fatal(name, err, diags)
		}
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	tr, diags, err := parseWith(context.Background(), strings.NewReader(metaXPathData), set,
		parseOpts{ParseOptions: ParseOptions{Validate: ValidateOptions{Present: true}}}, parseXML, nil)
	if err != nil {
		t.Fatal(err, diags)
	}
	for i, tc := range metaXPathCases {
		id := fmt.Sprintf("meta-%02d", i+1)
		var ctxNode *Node
		if tc[0] != "" {
			nodes, _, err := tr.FindXPath(tc[0], XPathOptions{})
			if err != nil || len(nodes) != 1 {
				t.Fatalf("%s: context %s: %v %v", id, tc[0], nodes, err)
			}
			ctxNode = nodes[0]
		}
		l := &logger{set: set}
		r, err := tr.evalXPath4(l, ctxNode, tc[1], nil, false, 0)
		got := resultJSON(r, err)
		for _, d := range l.diags {
			got += " | " + d.Err + " " + d.Msg
		}
		if os.Getenv("XPATH_META_PROBE") != "" {
			t.Logf("%s %s | %s => %s", id, tc[0], tc[1], got)
			continue
		}
		frag, err := os.ReadFile(filepath.Join(corpus, "manifest.d", "ut-xpath", id+".yaml")) //nolint:gosec // fixture path
		if err != nil {
			t.Fatal(err)
		}
		x, _ := json.Marshal(tc[1])
		cp, _ := json.Marshal(tc[0])
		var d bytes.Buffer // as the fragments quote it: JSON without HTML escapes
		enc := json.NewEncoder(&d)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(metaXPathData) // with the newline the check needs
		if !strings.Contains(string(frag), "xpath: "+string(x)+"\n") || !strings.Contains(string(frag), "data: "+d.String()) ||
			!strings.Contains(string(frag), "modules: [{name: a}, {name: xml}, {name: ietf-origin}]\n") ||
			strings.Contains(string(frag), "context_path: ") != (tc[0] != "") ||
			tc[0] != "" && !strings.Contains(string(frag), "context_path: "+string(cp)+"\n") {
			t.Errorf("%s: the fixture is not %s %s", id, cp, x)
		}
		raw, err := os.ReadFile(filepath.Join(corpus, "ut-xpath", "golden", id+".json")) //nolint:gosec // fixture path
		if err != nil {
			t.Fatal(err)
		}
		var golden struct {
			Result      json.RawMessage
			Diagnostics []struct {
				Code struct{ Name string }
				Msg  string
			}
		}
		if err := json.Unmarshal(raw, &golden); err != nil {
			t.Fatal(err)
		}
		want := "error"
		if string(golden.Result) != "null" {
			var v any
			_ = json.Unmarshal(golden.Result, &v)
			b, _ := json.Marshal(v) // sorted keys, as resultJSON
			want = string(b)
		}
		for _, d := range golden.Diagnostics {
			want += " | " + d.Code.Name + " " + srcPath.ReplaceAllString(d.Msg, "(")
		}
		if got != want {
			t.Errorf("%s: %s:\n got %s\nwant %s", id, tc[1], got, want)
		}
	}
}

// srcPath is the build directory in the file name of libyang's internal error messages.
var srcPath = regexp.MustCompile(`\(/\S*/src/`)

// resultJSON renders an evaluation like the golden's result (keys sorted), or "error".
func resultJSON(r xpath.Result, err error) string {
	if err != nil {
		return "error"
	}
	var v map[string]any
	switch r.Type {
	case xpath.Boolean:
		v = map[string]any{"type": "boolean", "value": r.Bool}
	case xpath.Number:
		v = map[string]any{"type": "number", "value": r.Num}
		if math.IsNaN(r.Num) {
			v["value"] = "NaN"
		}
	case xpath.String:
		v = map[string]any{"type": "string", "value": r.Str}
	default:
		nodes := []string{}
		for _, n := range r.Nodes {
			nodes = append(nodes, n.(xn).n.Path())
		}
		v = map[string]any{"type": "node-set", "nodes": nodes}
	}
	b, _ := json.Marshal(v)
	return string(b)
}
