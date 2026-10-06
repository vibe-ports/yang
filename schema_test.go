// SPDX-License-Identifier: BSD-3-Clause

package yang_test

import (
	"reflect"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
)

// TestHandlesOpaque: no handle type exposes a field, so a caller can reach nothing to write.
func TestHandlesOpaque(t *testing.T) {
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
