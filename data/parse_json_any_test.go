// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

func parseAnyJSONTest(t *testing.T, set *schema.Set, input string, opts ParseOptions) (*Tree, error) {
	t.Helper()
	tr, diags, err := parseWith(context.Background(), strings.NewReader(input), set,
		parseOpts{ParseOptions: opts, jsonNull: opts.JSONNull}, parseJSON, noValidation)
	if err != nil && len(diags) == 0 {
		t.Logf("parse error without diagnostics: %v", err)
	}
	return tr, err
}

func TestParseJSONAnyObjectKeepsMixedPayloadAndMetadata(t *testing.T) {
	set := pjSchema()
	input := `{"pj:ad":{"@pj:top":{"pj:ann":"note"},"pj:top":"known","other":{"x":1}},"pj:top":"after"}`
	tr, err := parseAnyJSONTest(t, set, input, ParseOptions{Unknown: Reject, ParseOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	ad := tr.top.list[1]
	if ad.schema.Name != "ad" || ad.anyType() != anyValueDataTree || ad.any.children.len() != 2 {
		t.Fatalf("any payload: %#v", ad.any)
	}
	known := ad.any.children.list[0]
	if known.schema == nil || known.schema.Name != "top" || len(known.meta) != 1 || known.meta[0].name != "ann" || known.meta[0].value.Canonical() != "note" {
		t.Fatalf("known child and metadata: %#v", known)
	}
	if ad.any.children.opq[0].opaq.Name != "other" || ad.any.children.opq[0].kids.opq[0].opaq.Name != "x" {
		t.Fatal("unknown descendant was not retained as an opaque subtree")
	}
	if len(ad.kids.list) != 0 || len(ad.kids.opq) != 0 || tr.top.list[0].value.Canonical() != "after" {
		t.Fatal("payload escaped ordinary traversal or parser state was not restored")
	}
}

func TestParseJSONAnyMetadataAfterPayloadIsDeferred(t *testing.T) {
	set := pjSchema()
	for _, anyName := range []string{"ad", "ax"} {
		t.Run(anyName, func(t *testing.T) {
			if anyName == "ax" {
				pj := set.Modules[1]
				pj.Top = append(pj.Top, &schema.Node{Kind: schema.AnyXML, Name: anyName, Module: pj, Config: true})
			}
			input := `{"pj:` + anyName + `":{"pj:top":"known","@pj:top":{}}}`
			tr, err := parseAnyJSONTest(t, set, input, ParseOptions{Unknown: Reject, ParseOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			payload := tr.top.first().any.children.first()
			if payload == nil || payload.schema == nil || payload.schema.Name != "top" {
				t.Fatalf("payload child: %#v", payload)
			}
		})
	}
}

func TestParseJSONAnyStrictAndNull(t *testing.T) {
	set := pjSchema()
	if _, err := parseAnyJSONTest(t, set, `{"pj:ad":{"missing":1}}`, ParseOptions{AnyDataStrict: true}); err == nil {
		t.Fatal("strict anydata accepted an unknown child")
	}
	tr, err := parseAnyJSONTest(t, set, `{"pj:ad":null,"pj:top":"ok"}`, ParseOptions{JSONNull: true})
	if err != nil || tr.top.len() != 1 || tr.top.first().schema.Name != "top" {
		t.Fatalf("JSONNull: %v, %#v", err, tr)
	}
	if _, err := parseAnyJSONTest(t, set, `{"pj:ad":null}`, ParseOptions{}); err == nil {
		t.Fatal("anydata accepted JSON null without LYD_PARSE_JSON_NULL")
	}
}

func TestParseJSONAnyXMLValues(t *testing.T) {
	set := pjSchema()
	pj := set.Modules[1]
	pj.Top = append(pj.Top, &schema.Node{Kind: schema.AnyXML, Name: "ax", Module: pj, Config: true})
	for _, tc := range []struct {
		input, text string
		hints       types.Hints
		present     bool
	}{
		{`"text"`, "text", types.HintString | types.HintNum64, true},
		{`35`, "35", types.HintDecNum, true},
		{`false`, "false", types.HintBoolean, true},
		{`null`, "", types.HintEmpty, false},
		{`[ null, {"k": [1,  2]} ]`, `[null, {"k": [1,  2]} ]`, hintList | hintLeafList, true},
	} {
		tr, err := parseAnyJSONTest(t, set, `{"pj:ax":`+tc.input+`}`, ParseOptions{})
		if err != nil {
			t.Fatalf("%s: %v", tc.input, err)
		}
		n := tr.top.first()
		if n.any.hints != tc.hints {
			t.Errorf("%s hints = %#x, want %#x", tc.input, n.any.hints, tc.hints)
		}
		text, present, err := n.AnyValueString(FormatJSON)
		if err != nil || text != tc.text || present != tc.present {
			t.Errorf("%s text = %q, %v, %v", tc.input, text, present, err)
		}
	}
}

func TestParseJSONAnyBudget(t *testing.T) {
	set := pjSchema()
	_, err := parseAnyJSONTest(t, set, `{"pj:ad":{"x":1,"y":2}}`, ParseOptions{ParseOnly: true,
		Budget: Budget{MaxNodes: 2}})
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatal("node budget was not enforced inside anydata")
	}
}

func TestParseJSONAnyStrictUnprefixedChild(t *testing.T) {
	// lydjson_get_snode: the parent arm wins over LYD_INTOPT_ANY, so the member takes the any
	// node's module.
	_, diags, err := parseWith(context.Background(), strings.NewReader(`{"pj:ad":{"missing":1}}`), pjSchema(),
		parseOpts{ParseOptions: ParseOptions{AnyDataStrict: true}}, parseJSON, noValidation)
	want := `LY_EVALID LYVE_REFERENCE /pj:ad||1: Node "missing" not found in the "pj" module.`
	if err == nil || len(diags) != 1 || diagLine(diags[0]) != want {
		var got []string
		for _, d := range diags {
			got = append(got, diagLine(d))
		}
		t.Fatalf("%v\n%s", err, strings.Join(got, "\n"))
	}
}

func TestParseJSONAnyStrictKeepsOpaque(t *testing.T) {
	// LYD_PARSE_ANYDATA_STRICT|LYD_PARSE_OPAQ: invalid known payload stays opaque.
	set := pjSchema()
	for _, in := range []string{`{"pj:ad":{"pj:c":{"n":"abc"}}}`, `{"pj:ad":{"pj:c":{"l":[{}]}}}`} {
		tr, err := parseAnyJSONTest(t, set, in, ParseOptions{AnyDataStrict: true, Unknown: Opaque, ParseOnly: true})
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		c := tr.top.first().any.children.first()
		if c.schema == nil || c.schema.Name != "c" || len(c.kids.list) != 0 || len(c.kids.opq) != 1 {
			t.Fatalf("%s: payload child was not kept opaque: %#v", in, c)
		}
		if _, err := parseAnyJSONTest(t, set, in, ParseOptions{AnyDataStrict: true, Unknown: Reject, ParseOnly: true}); err == nil {
			t.Fatalf("%s: accepted without opaque mode", in)
		}
	}
}
