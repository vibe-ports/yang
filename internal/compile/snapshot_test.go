// SPDX-License-Identifier: BSD-3-Clause

package compile_test

import (
	"reflect"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
)

// useHandles calls the handle methods that compute (types.Store, the leafref path compile) on
// every node of x.
func useHandles(x *snap.Schema) (uses int) {
	var walk func(n *snap.Node)
	walk = func(n *snap.Node) {
		for range n.Defaults() {
			uses++
		}
		for range n.LeafrefTargets() {
			uses++
		}
		for c := range n.Children() {
			walk(c)
		}
		for c := range n.Actions() {
			walk(c)
		}
	}
	for m := range x.Modules() {
		for n := range m.Top() {
			walk(n)
		}
	}
	return uses
}

// reach collects every schema object reachable from v (pointers to schema structs), skipping
// the opaque compiled expressions and patterns, which are immutable and may be shared.
func reach(v reflect.Value, seen map[uintptr]reflect.Type) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		if _, ok := seen[v.Pointer()]; ok {
			return
		}
		if v.Type().Elem().PkgPath() == "github.com/vibe-ports/yang/internal/schema" {
			if v.Type().Elem() == reflect.TypeFor[schema.Pattern]() {
				return
			}
			seen[v.Pointer()] = v.Type()
		}
		reach(v.Elem(), seen)
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Type().Field(i); f.Name == "Compiled" || f.Name == "PathCompiled" {
				continue
			}
			reach(v.Field(i), seen)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			reach(v.Index(i), seen)
		}
	case reflect.Map:
		for it := v.MapRange(); it.Next(); {
			reach(it.Value(), seen)
		}
	case reflect.Interface:
		if !v.IsNil() {
			reach(v.Elem(), seen)
		}
	}
}

// snapHarness is a context over files whose Loads run the whole compile (dep sets, unres). An
// external test: package snap imports compile.
func snapHarness(t *testing.T, files map[string]string) *compile.Context {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	c, _, err := compile.NewContext(compile.Options{}, fsys)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var snapFiles = map[string]string{
	"a.yang": `module a { yang-version 1.1; namespace urn:a; prefix a; feature f;
  identity base; identity d { base base; }
  typedef t { type int8 { range "1..5"; } }
  container c { must "x > 0"; leaf x { type t; default 2; } leaf r { type leafref { path "../x"; } }
    list l { key k; unique "v"; leaf k { type string; } leaf v { type identityref { base base; } } }
    choice ch { default one; leaf one { type string; } leaf two { when "../x = 1"; type empty; } }
    action act { input { leaf i { type string; } } } }
  leaf e { if-feature f; type enumeration { enum p; enum q { if-feature f; } } } }`,
	"b.yang": `module b { namespace urn:b; prefix b; import a { prefix a; }
  identity d2 { base a:base; } leaf y { type leafref { path "/a:c/a:x"; } } }`,
}

// TestSnapshotDeepCopy: a snapshot shares no schema object with the context it was taken from.
func TestSnapshotDeepCopy(t *testing.T) {
	c := snapHarness(t, snapFiles)
	for _, m := range []string{"a", "b"} {
		if _, diags, err := c.Load(m, "", []string{"*"}); err != nil {
			t.Fatal(m, err, diags)
		}
	}
	s := c.Snapshot()
	orig, cp := map[uintptr]reflect.Type{}, map[uintptr]reflect.Type{}
	for _, m := range c.Modules {
		reach(reflect.ValueOf(m.Schema), orig)
	}
	reach(reflect.ValueOf(s), cp)
	for p, typ := range cp {
		if _, ok := orig[p]; ok {
			t.Errorf("snapshot shares a %v with the context", typ)
		}
	}
	a := s.Implemented("a")
	if a == nil || len(a.Top) != 2 || a.Top[0].Children[0].Type.Range == nil || a.Top[0].Children[1].Type.Realtype != a.Top[0].Children[0].Type {
		t.Fatalf("snapshot content: %+v", a)
	}
	if l := a.Top[0].Children[2]; l.Keys[0] != l.Children[0] || l.Uniques[0][0] != l.Children[1] {
		t.Fatal("list references point outside the copy")
	}
	if len(a.Identity("base").Derived) != 2 {
		t.Fatal("derived identities")
	}
}

// TestSnapshotRace: readers walk a snapshot while the context loads and compiles more modules
// (run with -race).
func TestSnapshotRace(t *testing.T) {
	c := snapHarness(t, snapFiles)
	if _, diags, err := c.Load("a", "", []string{"*"}); err != nil {
		t.Fatal(err, diags)
	}
	s := c.Snapshot()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				reach(reflect.ValueOf(s), map[uintptr]reflect.Type{})
				useHandles(snap.New(s)) // canonical defaults and leafref targets compute on the copy
			}
		}()
	}
	if _, diags, err := c.Load("b", "", nil); err != nil {
		t.Error(err, diags)
	}
	if _, diags, err := c.Load("a", "", []string{}); err != nil { // a recompile of a
		t.Error(err, diags)
	}
	wg.Wait()
}
