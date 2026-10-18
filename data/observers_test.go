// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

func TestValueObservers(t *testing.T) {
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, os.DirFS("../conformance/corpus/protocol-v2/schemas"))
	if err != nil {
		t.Fatal(err)
	}
	if d, err := c.Load("pv2-observers", "", nil); err != nil {
		t.Fatal(err, d)
	}
	tr, d, err := parse(c.Schema(), `{"pv2-observers:c":{"strings":"abc","integers":7,"target":7,"ref":7,"reference":7,"shared":8,"nested":7}}`,
		data.FormatJSON, data.ParseOptions{})
	if err != nil {
		t.Fatal(err, d)
	}
	for _, tc := range []struct {
		name, base, typedef, member, realtype, memberTypedef string
		index                                                int
	}{
		{"", "", "", "", "", "", 0},
		{"target", "int8", "second-int", "", "", "", 0},
		{"ref", "int8", "second-int", "", "", "", 0},
		{"strings", "union", "", "string", "string", "", 0},
		{"integers", "union", "", "int8", "int8", "second-int", 1},
		{"reference", "union", "", "leafref", "int8", "", 0},
		{"shared", "union", "", "leafref", "int8", "", 0},
		{"nested", "union", "", "int8", "int8", "second-int", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "/pv2-observers:c"
			if tc.name != "" {
				path += "/" + tc.name
			}
			n, err := tr.Find(path)
			if err != nil || n == nil {
				t.Fatal(path, err)
			}
			tp := n.ValueType()
			if tc.base == "" {
				if tp != nil {
					t.Fatal("non-terminal has a value type")
				}
			} else if tp == nil || tp.Base() != tc.base || tp.Typedef() != tc.typedef {
				t.Fatalf("ValueType = %v, want %s (%s)", tp, tc.base, tc.typedef)
			}
			m, ok := n.UnionMember()
			if tc.member == "" {
				if ok || m != (data.UnionMember{}) {
					t.Fatal("non-union has a member", m)
				}
			} else if !ok || m.Index != tc.index || m.Type.Base() != tc.member || m.RealType.Base() != tc.realtype || m.Type.Typedef() != tc.memberTypedef {
				t.Fatalf("UnionMember = %+v, %v, want index %d, %s (%s), stored %s", m, ok, tc.index, tc.member, tc.memberTypedef, tc.realtype)
			}
			if tc.member == "leafref" && m.RealType.Typedef() != "second-int" {
				t.Fatal("lost target typedef")
			}
			if info, ok := n.OpaqueInfo(); ok || !reflect.DeepEqual(info, data.OpaqueInfo{}) {
				t.Fatal("schema node is opaque", info)
			}
			if n.Attrs() != nil {
				t.Fatal("schema node has opaque attributes")
			}
		})
	}
}

func TestOpaqueObservers(t *testing.T) {
	s := fzContext(t).Schema()
	for _, tc := range []struct {
		name, input string
		format      data.Format
		info        data.OpaqueInfo
		attrs       []data.Attr
	}{
		{"json", `{"other:item":"text","@other:item":{"other:a":"first","other:b":""}}`, data.FormatJSON,
			data.OpaqueInfo{Prefix: "other", Module: "other", Format: data.FormatJSON, Hints: []string{"string", "num64"}},
			[]data.Attr{{Name: "a", Prefix: "other", Module: "other", Value: "first"}, {Name: "b", Prefix: "other", Module: "other"}}},
		{"xml", `<x:item xmlns:x="urn:other" xmlns:a="urn:attrs" a:first="one" plain="">text</x:item>`, data.FormatXML,
			data.OpaqueInfo{Prefix: "x", Namespace: "urn:other", Format: data.FormatXML, Hints: []string{"string"}},
			[]data.Attr{{Name: "first", Prefix: "a", Module: "urn:attrs", Value: "one"}, {Name: "plain"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, d, err := parse(s, tc.input, tc.format, data.ParseOptions{ParseOnly: true, Unknown: data.Opaque})
			if err != nil {
				t.Fatal(err, d)
			}
			n := slices.Collect(tr.Top())[0]
			info, ok := n.OpaqueInfo()
			if !ok || !reflect.DeepEqual(info, tc.info) {
				t.Fatalf("OpaqueInfo = %+v, %v, want %+v", info, ok, tc.info)
			}
			attrs := n.Attrs()
			if !reflect.DeepEqual(attrs, tc.attrs) {
				t.Fatalf("Attrs = %+v, want %+v", attrs, tc.attrs)
			}
			info.Hints[0] = "changed"
			attrs[0] = data.Attr{Name: "changed"}
			again, _ := n.OpaqueInfo()
			if !reflect.DeepEqual(again, tc.info) || !reflect.DeepEqual(n.Attrs(), tc.attrs) {
				t.Fatal("observer result aliases the tree")
			}
			if n.Name() != "item" || n.Value() != "text" || n.ValueType() != nil {
				t.Fatal("opaque name/value/type")
			}
			if _, ok := n.UnionMember(); ok {
				t.Fatal("opaque union member")
			}
			if len(slices.Collect(n.Meta())) != 0 {
				t.Fatal("opaque attributes exposed as metadata")
			}
		})
	}
}
