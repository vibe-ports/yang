// SPDX-License-Identifier: BSD-3-Clause

package yang_test

import (
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
)

// TestSchemaNodeFlags: IsKey (lysc_is_key), IsDupInstList (lysc_is_dup_inst_list) and DefaultSet
// (LYS_SET_DFLT: own, or refined default, a default case; not a typedef's default).
func TestSchemaNodeFlags(t *testing.T) {
	ctx, _, err := yang.NewContext(yang.Options{}, fstest.MapFS{
		"f.yang": {Data: []byte(`module f { yang-version 1.1; namespace urn:f; prefix f;
  typedef td { type string; default "t"; }
  grouping g { leaf r { type string; } }
  container c {
    list l { key "k"; leaf k { type string; default "x"; } leaf v { type string; } }
    list kl { config false; leaf a { type string; } }
    leaf-list cll { type string; default "a"; default "b"; }
    leaf-list sll { config false; type string; }
    leaf own { type string; default "o"; }
    leaf typed { type td; }
    leaf k { type string; }
    uses g { refine r { default "rf"; } }
    choice ch { default b; case a { leaf x { type string; } } case b { leaf y { type string; } } }
  }
}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.Load("f", "", nil); err != nil {
		t.Fatal(err)
	}
	s := ctx.Schema()
	node := func(path string) *yang.SchemaNode {
		n, err := s.FindSchema(path)
		if err != nil || n == nil {
			t.Fatalf("%s: %v", path, err)
		}
		return n
	}
	var ch *yang.SchemaNode
	for c := range node("/f:c").Children() {
		if c.Kind() == yang.KindChoice {
			ch = c
		}
	}
	cases := map[string]*yang.SchemaNode{}
	for c := range ch.Children() {
		cases[c.Name()] = c
	}
	for _, tc := range []struct {
		n                    *yang.SchemaNode
		key, dup, defaultSet bool
	}{
		{node("/f:c/l/k"), true, false, true}, // a key keeps LYS_SET_DFLT, its default is dropped
		{node("/f:c/l/v"), false, false, false},
		{node("/f:c/l"), false, false, false},
		{node("/f:c/k"), false, false, false},
		{node("/f:c/kl"), false, true, false},
		{node("/f:c/cll"), false, false, true},
		{node("/f:c/sll"), false, true, false},
		{node("/f:c/own"), false, false, true},
		{node("/f:c/typed"), false, false, false},
		{node("/f:c/r"), false, false, true},
		{cases["a"], false, false, false},
		{cases["b"], false, false, true},
		{ch, false, false, false},
	} {
		if tc.n.IsKey() != tc.key || tc.n.IsDupInstList() != tc.dup || tc.n.DefaultSet() != tc.defaultSet {
			t.Errorf("%s: IsKey %v IsDupInstList %v DefaultSet %v", tc.n.Path(), tc.n.IsKey(), tc.n.IsDupInstList(),
				tc.n.DefaultSet())
		}
	}
	if n := node("/f:c/typed"); !hasDefault(n) {
		t.Error("typed leaf lost its type default")
	}
}

func hasDefault(n *yang.SchemaNode) bool {
	for range n.Defaults() {
		return true
	}
	return false
}
