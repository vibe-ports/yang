// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// TestEvalXPathDerefPredicateDependency exercises the schema-only deref() used while deciding
// whether a keyed-list predicate can use one precomputed hash lookup. The leafref has two data
// targets, so treating the first target's sibling value as a constant would lose the second list.
func TestEvalXPathDerefPredicateDependency(t *testing.T) {
	mods := fstest.MapFS{"snapshot.yang": {Data: []byte(`module snapshot {
  yang-version 1.1;
  namespace "urn:snapshot";
  prefix s;
  container top {
    list l {
      key k;
      leaf k { type string; }
      leaf marker { type string; }
      leaf other { type string; }
    }
    leaf ref { type leafref { path "../l/marker"; } }
  }
}`)}}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, mods)
	if err != nil {
		t.Fatal(err)
	}
	if diags, err := c.Load("snapshot", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	tr, diags, err := parse(c.Schema(), `{"snapshot:top":{"l":[{"k":"a","marker":"x","other":"a"},{"k":"b","marker":"x","other":"b"}],"ref":"x"}}`,
		data.FormatJSON, data.ParseOptions{})
	if err != nil {
		t.Fatal(err, diags)
	}
	r, diags, err := tr.EvalXPath(`/snapshot:top/l[k = deref(../ref)/../other]/k`, data.XPathOptions{})
	if err != nil {
		t.Fatal(err, diags)
	}
	if len(r.Nodes) != 2 || r.Nodes[0].Value() != "a" || r.Nodes[1].Value() != "b" {
		t.Fatalf("deref predicate result: %#v", r.Nodes)
	}
}

// eval_name_test_try_compile_predicates suppresses every diagnostic emitted while it evaluates
// candidate key values. The matrix covers nested data callbacks and conversions; in particular,
// resolving a's extended leafref path recursively dereferences the dangling leafref b and logs
// from the nested valCtx before deref() returns its own error.
func TestEvalXPathSpeculativeNestedCallbacksSuppressDiagnostics(t *testing.T) {
	mods := fstest.MapFS{
		"a.yang": {Data: []byte(`module a {
  yang-version 1.1;
  namespace "urn:a";
  prefix a;
  list l1 {
    key "a b";
    leaf a { type string; }
    leaf b { type string; }
  }
}`)},
		"nested.yang": {Data: []byte(`module nested {
  yang-version 1.1;
  namespace "urn:nested";
  prefix nested;
  identity base;
  identity child { base base; }
  leaf id { type identityref { base base; } }
  leaf target { type string; }
  leaf iid { type instance-identifier { require-instance false; } }
  list lookup {
    key "x y";
    leaf x { type string; }
    leaf y { type string; }
  }
  container c {
    list t { key name; leaf name { type string; } }
    leaf b {
      type leafref { path "../t/name"; require-instance false; }
    }
    leaf a {
      type leafref { path "deref(../b)/../name"; require-instance false; }
    }
  }
}`)},
	}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true, LeafrefExtended: true}, mods)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "nested"} {
		if diags, err := c.Load(name, "", nil); err != nil {
			t.Fatal(err, diags)
		}
	}
	tr, diags, err := parse(c.Schema(), `{"a:l1":[{"a":"present","b":"value"}],`+
		`"nested:id":"nested:child","nested:iid":"/nested:target",`+
		`"nested:lookup":[{"x":"present","y":"value"}],`+
		`"nested:c":{"t":[{"name":"x"}],"b":"nope","a":"x"}}`,
		data.FormatJSON, data.ParseOptions{ParseOnly: true})
	if err != nil || len(diags) != 0 {
		t.Fatalf("parse diagnostics=%v error=%v", diags, err)
	}
	for _, tc := range []struct {
		name, value string
	}{
		{"deref", "string(deref(/nested:iid))"},
		{"derived-from", "derived-from(/nested:id,'nested:missing')"},
		{"leafref-resolution", "string(deref(/nested:c/a))"},
		{"nested-casts", "string(number(string(deref(/nested:iid))))"},
		{"nested-speculation", "string(/nested:lookup[x='present'][y=string(deref(/nested:c/a))])"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, first := range []string{"absent", "present"} {
				t.Run(first, func(t *testing.T) {
					r, diags, err := tr.EvalXPath(
						"count(/a:l1[a='"+first+"'][b="+tc.value+"])", data.XPathOptions{})
					if first == "absent" {
						if err != nil || len(diags) != 0 || r.Type != data.XPathNumber || r.Number != 0 {
							t.Fatalf("result=%+v diagnostics=%v error=%v", r, diags, err)
						}
					} else if err == nil || len(diags) == 0 {
						t.Fatalf("real callback result=%+v diagnostics=%v error=%v", r, diags, err)
					}
				})
			}
		})
	}
}
