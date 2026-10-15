// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"github.com/vibe-ports/yang/internal/schema"
	"testing"
)

func TestSortCompatibleCallbacks(t *testing.T) {
	groups := [][]schema.BaseType{
		{schema.Int8, schema.Int16, schema.Int32, schema.Int64},
		{schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64},
		{schema.String, schema.Empty, schema.InstanceID},
		{schema.Binary}, {schema.Bits}, {schema.Bool}, {schema.Dec64},
		{schema.Enumeration}, {schema.IdentityRef}, {schema.Union}, {schema.Leafref},
	}
	for i, ga := range groups {
		for j, gb := range groups {
			for _, a := range ga {
				for _, b := range gb {
					va, vb := Value{typ: &schema.Type{Base: a}}, Value{typ: &schema.Type{Base: b}}
					if got := SortCompatible(va, vb); got != (i == j) {
						t.Fatalf("%s/%s compatible=%v", a, b, got)
					}
				}
			}
		}
	}
	// Every registered ietf-* record with .sort=lyplg_type_sort_simple shares
	// the built-in string callback, independently of its plugin record identity.
	for key, p := range plugins {
		v := Value{typ: &schema.Type{Base: schema.String, TypedefModule: &schema.Module{Name: key.module, Revision: key.revision}, Typedef: key.name}, canon: "b"}
		plain := Value{typ: &schema.Type{Base: schema.String}, canon: "a"}
		if got := SortCompatible(v, plain); got != (p.compare == nil) {
			t.Fatalf("%v/string compatible=%v", key, got)
		}
		if p.compare == nil {
			if order, ok := CompareCompatible(v, plain); !ok || order <= 0 {
				t.Fatalf("%v/string order=%d compatible=%v", key, order, ok)
			}
		}
	}
}

func TestCompareCompatibleIntegerWidths(t *testing.T) {
	for _, tc := range []struct {
		a, b Value
		want int
	}{
		{Value{typ: &schema.Type{Base: schema.Int8}, i: 2}, Value{typ: &schema.Type{Base: schema.Int16}, i: 10}, -1},
		{Value{typ: &schema.Type{Base: schema.Int8}, i: 2}, Value{typ: &schema.Type{Base: schema.Int16}, i: 256}, 1},
		{Value{typ: &schema.Type{Base: schema.Int16}, i: 2}, Value{typ: &schema.Type{Base: schema.Int8}, i: -1}, -1},
		{Value{typ: &schema.Type{Base: schema.Uint8}, u: 2}, Value{typ: &schema.Type{Base: schema.Uint16}, u: 256}, 1},
	} {
		if got, ok := CompareCompatible(tc.a, tc.b); !ok || got != tc.want {
			t.Fatalf("CompareCompatible=%d,%v; want %d,true", got, ok, tc.want)
		}
	}
}
