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
