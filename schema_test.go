// SPDX-License-Identifier: BSD-3-Clause

package yang_test

import (
	"reflect"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
)

// TestDisabledDefaultCase: a default case removed by its if-feature is not reachable; only its
// name is kept (D-0070).
func TestDisabledDefaultCase(t *testing.T) {
	ctx, _, err := yang.NewContext(yang.Options{}, fstest.MapFS{"d.yang": {Data: []byte(`module d { yang-version 1.1;
  namespace urn:d; prefix d; feature f;
  choice ch { default a; case a { if-feature f; leaf x { type string; } } case b { leaf y { type string; } } } }`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.Load("d", "", nil); err != nil {
		t.Fatal(err)
	}
	for ch := range ctx.Schema().Implemented("d").Top() {
		if ch.DefaultCase() != nil || ch.DefaultCaseName() != "a" {
			t.Fatalf("default case %v, name %q", ch.DefaultCase(), ch.DefaultCaseName())
		}
		return
	}
	t.Fatal("no choice")
}

// TestHandlesOpaque: no handle type exposes a field, so a caller can reach nothing to write.
// The query results are the exception: slices built per call, holding fresh handles.
func TestHandlesOpaque(t *testing.T) {
	fresh := map[string]bool{"FindXPathAtoms": true, "FindPathAtoms": true}
	for _, typ := range []reflect.Type{reflect.TypeFor[yang.Schema](), reflect.TypeFor[yang.Module](),
		reflect.TypeFor[yang.SchemaNode](), reflect.TypeFor[yang.Type](), reflect.TypeFor[yang.Identity](),
		reflect.TypeFor[yang.Must](), reflect.TypeFor[yang.When](), reflect.TypeFor[yang.Extension]()} {
		for i := range typ.NumField() {
			if typ.Field(i).IsExported() {
				t.Errorf("%v exports field %s", typ, typ.Field(i).Name)
			}
		}
		for i := range reflect.PointerTo(typ).NumMethod() {
			m := reflect.PointerTo(typ).Method(i)
			if fresh[m.Name] {
				continue
			}
			for j := range m.Type.NumOut() {
				if k := m.Type.Out(j).Kind(); k == reflect.Slice || k == reflect.Map {
					t.Errorf("%v.%s returns a %v", typ, m.Name, k)
				}
			}
		}
	}
}

// TestSchemaSnapshots: a snapshot does not change when the context loads more, and readers can
// use it while Load runs (run with -race).
func TestSchemaSnapshots(t *testing.T) {
	dir := fstest.MapFS{
		"a.yang": {Data: []byte(`module a { namespace urn:a; prefix a; feature f; identity base; }`)},
		"b.yang": {Data: []byte(`module b { namespace urn:b; prefix b; import a { prefix a; } identity d { base a:base; } }`)},
	}
	ctx, _, err := yang.NewContext(yang.Options{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.Load("a", "", []string{"f"}); err != nil {
		t.Fatal(err)
	}
	s := ctx.Schema()
	walk := func(s *yang.Schema) (n int) {
		for m := range s.Modules() {
			for range m.Features() {
				n++
			}
			for id := range m.Identities() {
				for range id.Derived() {
					n++
				}
			}
		}
		return n
	}
	before := walk(s)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				if walk(s) != before {
					t.Error("snapshot changed")
					return
				}
				walk(ctx.Schema())
			}
		}()
	}
	if _, err := ctx.Load("b", "", nil); err != nil {
		t.Error(err)
	}
	if _, err := ctx.Load("a", "", []string{}); err != nil {
		t.Error(err)
	}
	wg.Wait()
	if s.Implemented("b") != nil || ctx.Schema().Implemented("b") == nil || !s.Implemented("a").FeatureEnabled("f") ||
		ctx.Schema().Implemented("a").FeatureEnabled("f") {
		t.Fatal("snapshots do not follow the loads")
	}
}

// TestAtomsForeignNode: a context node of another snapshot is LY_EINVAL (LY_CHECK_CTX_EQUAL_RET),
// even one of an older snapshot of the same context.
func TestAtomsForeignNode(t *testing.T) {
	ctx, _, err := yang.NewContext(yang.Options{}, fstest.MapFS{
		"a.yang": {Data: []byte(`module a { namespace urn:a; prefix a; container c { leaf x { type string; } } }`)},
		"b.yang": {Data: []byte(`module b { namespace urn:b; prefix b; leaf y { type string; } }`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.Load("a", "", nil); err != nil {
		t.Fatal(err)
	}
	old := ctx.Schema()
	c, err := old.FindSchema("/a:c")
	if err != nil {
		t.Fatal(err)
	}
	if atoms, _, err := old.FindXPathAtoms(c, "x", yang.AtomOptions{}); err != nil || len(atoms) != 2 {
		t.Fatalf("own snapshot: %v %v", atoms, err)
	}
	if _, err := ctx.Load("b", "", nil); err != nil {
		t.Fatal(err)
	}
	s := ctx.Schema()
	for fn, call := range map[string]func() ([]*yang.SchemaNode, []yang.Diagnostic, error){
		"lys_find_xpath_atoms": func() ([]*yang.SchemaNode, []yang.Diagnostic, error) {
			return s.FindXPathAtoms(c, "x", yang.AtomOptions{})
		},
		"lys_find_path_atoms": func() ([]*yang.SchemaNode, []yang.Diagnostic, error) {
			return s.FindPathAtoms(c, "x", yang.AtomOptions{})
		},
	} {
		atoms, diags, err := call()
		want := `Different contexts mixed in a "` + fn + `" function call.`
		if err == nil || atoms != nil || len(diags) != 1 || diags[0].Err != "LY_EINVAL" || diags[0].Msg != want {
			t.Errorf("%s: %v %+v %v", fn, atoms, diags, err)
		}
	}
}
