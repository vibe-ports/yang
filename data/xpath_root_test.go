// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

func TestXPathRoot(t *testing.T) {
	const module = `module pv2-links {
  yang-version 1.1;
  namespace "urn:vibe-ports:yang:test:pv2-links";
  prefix lk;

  container c {
    list l {
      key k;
      leaf k { type string; }
    }
    leaf r {
      type leafref { path "../l/k"; }
    }
  }
}`
	cx, _, err := yang.NewContext(yang.Options{NoYangLibrary: true, LeafrefLinking: true}, fstest.MapFS{
		"pv2-links.yang": {Data: []byte(module)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if diags, err := cx.Load("pv2-links", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	schema := cx.Schema()
	source, diags, err := data.Parse(context.Background(), strings.NewReader(
		`{"pv2-links:c":{"l":[{"k":"a"}],"r":"a"}}`), data.FormatJSON, schema,
		data.ParseOptions{ParseOnly: true})
	if err != nil || len(diags) != 0 {
		t.Fatal(err, diags)
	}
	r, err := source.Find("/pv2-links:c/r")
	if err != nil || r == nil {
		t.Fatalf("find source r: %v %v", r, err)
	}
	nested := func(t *testing.T) (*data.Tree, *data.Node) {
		t.Helper()
		tr := data.NewTree(schema)
		r, diags, err := tr.Dup(r, nil, data.DupOptions{})
		if err != nil || len(diags) != 0 {
			t.Fatalf("dup nested r: %v %v", err, diags)
		}
		return tr, r
	}

	const rootMsg = `Data node "r" has no parent but is not instance of a top-level schema node.`
	rootDiag := yang.Diagnostic{Err: "LY_EINVAL", Code: "LYVE_SUCCESS", Msg: rootMsg}
	checkRootError := func(t *testing.T, diags []yang.Diagnostic, err error) {
		t.Helper()
		var ve *data.ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
			t.Errorf("error: got %v, want LY_EINVAL *ValidationError", err)
		}
		if want := []yang.Diagnostic{rootDiag}; !reflect.DeepEqual(diags, want) {
			t.Errorf("diagnostics:\n got %#v\nwant %#v", diags, want)
		}
	}
	checkOnlyTop := func(t *testing.T, tr *data.Tree, want *data.Node) {
		t.Helper()
		if got := slices.Collect(tr.Top()); len(got) != 1 || got[0] != want {
			t.Errorf("top nodes after failure: got %v, want [%v]", got, want)
		}
	}

	t.Run("LinkLeafrefs", func(t *testing.T) {
		tr, r := nested(t)
		diags, err := tr.LinkLeafrefs(context.Background())
		if err != nil {
			t.Errorf("LinkLeafrefs: %v", err)
		}
		if want := []yang.Diagnostic{rootDiag}; !reflect.DeepEqual(diags, want) {
			t.Errorf("diagnostics:\n got %#v\nwant %#v", diags, want)
		}
		leafrefs, targets := r.LeafrefLinks()
		if len(leafrefs) != 0 || len(targets) != 0 {
			t.Errorf("links: leafrefs=%v targets=%v", leafrefs, targets)
		}
	})

	t.Run("FindXPath", func(t *testing.T) {
		tr, _ := nested(t)
		nodes, diags, err := tr.FindXPath("/pv2-links:c", data.XPathOptions{})
		if len(nodes) != 0 {
			t.Errorf("nodes: got %v, want none", nodes)
		}
		checkRootError(t, diags, err)
	})

	t.Run("TrimXPath", func(t *testing.T) {
		tr, r := nested(t)
		diags, err := tr.TrimXPath("/pv2-links:c", data.XPathOptions{})
		checkRootError(t, diags, err)
		checkOnlyTop(t, tr, r)
	})

	t.Run("OpaqueTop", func(t *testing.T) {
		opaque := func(t *testing.T) *data.Tree {
			t.Helper()
			tr, diags, err := data.Parse(context.Background(), strings.NewReader(
				`{"pv2-links:zz":{"q":1}}`), data.FormatJSON, schema,
				data.ParseOptions{ParseOnly: true, Unknown: data.Opaque})
			if err != nil || len(diags) != 0 {
				t.Fatalf("parse opaque: %v %v", err, diags)
			}
			return tr
		}
		tr := opaque(t)
		if nodes, diags, err := tr.FindXPath("/pv2-links:c", data.XPathOptions{}); err != nil ||
			len(diags) != 0 || len(nodes) != 0 {
			t.Errorf("FindXPath: nodes=%v diags=%v err=%v", nodes, diags, err)
		}
		tr = opaque(t)
		if diags, err := tr.TrimXPath("/pv2-links:c", data.XPathOptions{}); err != nil || len(diags) != 0 {
			t.Errorf("TrimXPath: diags=%v err=%v", diags, err)
		}
	})

	t.Run("Validate", func(t *testing.T) {
		tr, _ := nested(t)
		diags, err := tr.Validate(context.Background(), data.ValidateOptions{MultiError: true})
		// lyd_validate_all first adds the implicit top-level NP container c, so lyxp_eval sees c
		// as the first sibling and the leafref fails normally instead of failing the tree check.
		want := []yang.Diagnostic{
			{
				Err:      "LY_EVALID",
				Code:     "LYVE_DATA",
				DataPath: "/pv2-links:r",
				AppTag:   "instance-required",
				Msg:      `Invalid leafref value "a" - no target instance "../l/k" with the same value.`,
			},
		}
		var ve *data.ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EVALID" {
			t.Errorf("error: got %v, want LY_EVALID *ValidationError", err)
		}
		if !reflect.DeepEqual(diags, want) {
			t.Errorf("diagnostics:\n got %#v\nwant %#v", diags, want)
		}
	})
}
