// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// TestXPathDerivedImportOnly covers xpath-format/schema-not-implemented and
// xml-derived-not-implemented: derived-from requires an implemented identity module,
// even though typed-value canonicalization accepts import-only prefixes.
func TestXPathDerivedImportOnly(t *testing.T) {
	mods := fstest.MapFS{
		"m.yang": {Data: []byte(`module m {
  yang-version 1.1; namespace "urn:m"; prefix m;
  import imp { prefix i; }
  identity base;
  identity child { base base; }
  leaf id { type identityref { base base; } }
}`)},
		"imp.yang": {Data: []byte(`module imp {
  yang-version 1.1; namespace "urn:imp"; prefix imp;
  identity bird;
}`)},
	}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, mods)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load("m", "", nil); err != nil {
		t.Fatal(err)
	}
	tr, _, err := parse(c.Schema(), `{"m:id":"m:child"}`, data.FormatJSON, data.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []data.XPathFormat{data.XPathSchema, data.XPathXML} {
		for _, fn := range []string{"derived-from", "derived-from-or-self"} {
			o := data.XPathOptions{Format: format, Module: "m"}
			if format == data.XPathXML {
				o.Namespaces = []data.XPathNamespace{{Prefix: "m", URI: "urn:m"}, {Prefix: "i", URI: "urn:imp"}}
			}
			_, ds, err := tr.EvalXPath(fn+"(/m:id, 'i:bird')", o)
			var ve *data.ValidationError
			want := []yang.Diagnostic{{Err: "LY_EVALID", Code: "LYVE_XPATH", Msg: `Unknown/non-implemented module "i".`}}
			if !errors.As(err, &ve) || ve.RC() != "LY_EVALID" || !reflect.DeepEqual(ds, want) {
				t.Errorf("format %v, %s: error %v, diagnostics %+v", format, fn, err, ds)
			}
			// A failed query leaves the tree and the next query usable.
			r, _, err := tr.EvalXPath(fn+"(/m:id, 'm:base')", o)
			if err != nil || !r.Boolean {
				t.Errorf("format %v, %s after error: result %+v, error %v", format, fn, r, err)
			}
		}
	}
}

// TestXPathFormats: the expression formats of XPathOptions (lyd_eval_xpath4's format,
// prefix_data and cur_mod) over fz, whose prefix is also "fz", and the argument refusals; the
// libyang comparison is the oracle fixtures xpath-format/*.
func TestXPathFormats(t *testing.T) {
	s := fzContext(t).Schema()
	tr, _, err := data.Parse(context.Background(), strings.NewReader(`{"fz:c": {"s": "x", "id": "fz:i1"}}`),
		data.FormatJSON, s, data.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ns := []data.XPathNamespace{{Prefix: "f", URI: "urn:fz"}}
	for _, c := range []struct {
		expr string
		o    data.XPathOptions
		want string
	}{
		{"/f:c/f:s = 'x'", data.XPathOptions{Format: data.XPathXML, Namespaces: ns}, "true"},
		{"/f:c/f:id = 'f:i1'", data.XPathOptions{Format: data.XPathXML, Namespaces: ns}, "true"},
		{"/c/s", data.XPathOptions{Format: data.XPathXML, Namespaces: ns},
			`LY_EVALID | Non-prefixed node "c" in XML xpath found.`},
		{"derived-from(/c/id, 'b')", data.XPathOptions{Format: data.XPathSchema, Module: "fz"}, "true"},
		{"derived-from(/fz:c/id, 'b')", data.XPathOptions{Module: "fz"}, "true"}, // cur_mod
		{"/c", data.XPathOptions{Format: data.XPathSchema},
			"LY_EINVAL | Current module must be set if schema format is used."},
		{"/fz:c", data.XPathOptions{Module: "nope"},
			"LY_EINVAL | Invalid argument cur_mod (not implemented) (lyd_eval_xpath4())."},
		{"/fz:c", data.XPathOptions{Format: 9}, "LY_EINVAL | Invalid argument format (unknown) (lyd_eval_xpath4())."},
	} {
		r, _, err := tr.EvalXPathAs(c.expr, data.XPathBoolean, c.o)
		got := "false"
		if r.Boolean {
			got = "true"
		}
		var ve *data.ValidationError
		if errors.As(err, &ve) {
			got = ve.RC()
			for _, d := range ve.Diags {
				got += " | " + d.Msg
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s %+v:\n got %s\nwant %s", c.expr, c.o, got, c.want)
		}
	}
	if _, err := tr.TrimXPath("/fz:c", data.XPathOptions{Format: data.XPathXML}); err == nil {
		t.Error("TrimXPath took a format")
	}
}

// TestXPathSchemaParseFirst: lyd_eval_xpath4 parses the expression before lyxp_eval checks the
// current module of a schema-format expression (xpath-format/schema-parse-error).
func TestXPathSchemaParseFirst(t *testing.T) {
	tr := data.NewTree(fzContext(t).Schema())
	_, _, err := tr.EvalXPath("[", data.XPathOptions{Format: data.XPathSchema})
	var ve *data.ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EVALID" {
		t.Fatalf("%v", err)
	}
}

// TestXPathParseBeforeOptions: a syntax error is reported before a refused option (an unknown
// format, a module that is not implemented), as lyd_eval_xpath4 parses first.
func TestXPathParseBeforeOptions(t *testing.T) {
	tr := data.NewTree(fzContext(t).Schema())
	for _, o := range []data.XPathOptions{{Format: 9}, {Module: "nope"}, {Format: data.XPathSchema}} {
		_, _, err := tr.EvalXPath("[", o)
		var ve *data.ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EVALID" || len(ve.Diags) != 1 {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

// TestXPathCurModOpaqueContext: an unprefixed derived-from() identity is in the current node's
// schema module, and with an opaque current node (no schema node) in cur_mod, as
// xpath_derived_ident_module passes "set->cur_node ? set->cur_node->schema : NULL" to
// moveto_resolve_module (src/xpath.c) and falls back to set->cur_mod. lyoracle's context_path
// selects through XPath, which never selects opaque nodes, so this is a Go test.
func TestXPathCurModOpaqueContext(t *testing.T) {
	s := fzContext(t).Schema()
	tr, _, err := data.Parse(context.Background(), strings.NewReader(`{"fz2:zz": "x"}`), data.FormatJSON, s,
		data.ParseOptions{ParseOnly: true, Unknown: data.Opaque})
	if err != nil {
		t.Fatal(err)
	}
	var op *data.Node
	for n := range tr.Top() {
		op = n
	}
	r, _, err := tr.EvalXPathAs("derived-from(., 'b')", data.XPathBoolean, data.XPathOptions{Node: op, Module: "fz"})
	if err != nil || r.Boolean {
		t.Fatalf("%v %v", r.Boolean, err)
	}
}
