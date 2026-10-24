// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"bytes"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestCompareBitsMemcmp checks compareBits against libyang's memcmp of little-endian bitmaps
// sized by the highest position, over random position sets.
func TestCompareBitsMemcmp(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // deterministic test data
	bitmap := func(ps []uint32, last uint32) []byte {
		m := make([]byte, last/8+1)
		for _, p := range ps {
			m[p/8] |= 1 << (p % 8)
		}
		return m
	}
	set := func(last uint32) []*schema.Bit {
		var out []*schema.Bit
		for p := range last + 1 {
			if r.IntN(8) == 0 {
				out = append(out, &schema.Bit{Position: p})
			}
		}
		return out
	}
	pos := func(bs []*schema.Bit) []uint32 {
		var out []uint32
		for _, b := range bs {
			out = append(out, b.Position)
		}
		return out
	}
	for range 20000 {
		last := r.Uint32N(40)
		a, b := set(last), set(last)
		if r.IntN(4) == 0 {
			b = slices.Clone(a)
		}
		want := bytes.Compare(bitmap(pos(a), last), bitmap(pos(b), last))
		if got := compareBits(a, b); got != want {
			t.Fatalf("%v vs %v: got %d, want %d", pos(a), pos(b), got, want)
		}
	}
	hi := []*schema.Bit{{Position: 4294967295}}
	if compareBits(nil, hi) >= 0 || compareBits(hi, hi) != 0 {
		t.Error("position 4294967295")
	}
}

// #306: a bits type whose highest position is near 2^32 compares its positions without int
// arithmetic, so a cross-context merge of such a value is not dropped on GOARCH=386 (make test-386).
func TestCrossContextBitsHighPosition(t *testing.T) {
	for _, last := range []uint32{4294967294, 4294967295} {
		typ := func() (*schema.Type, *schema.Bit) {
			high := &schema.Bit{Name: "high", Position: last}
			return &schema.Type{Base: schema.Bits, Bits: []*schema.Bit{{Name: "low", Position: 0}, high}}, high
		}
		dstType, _ := typ()
		srcType, high := typ()
		equal, dupOK, undefined, _ := CrossContextChange(nil, dstType, srcType, Value{typ: dstType},
			Value{typ: srcType, bits: []*schema.Bit{high}}, nil)
		if equal || !dupOK || undefined != 0 {
			t.Fatalf("position %d: equal %v, dupOK %v, undefined %v; want a different value lyplg_type_dup_bits copies",
				last, equal, dupOK, undefined)
		}
		dstHigh := dstType.Bits[1]
		if equal, _, _, _ := CrossContextChange(nil, dstType, srcType, Value{typ: dstType, bits: []*schema.Bit{dstHigh}},
			Value{typ: srcType, bits: []*schema.Bit{high}}, nil); !equal {
			t.Fatalf("position %d: equal bitmaps compare different", last)
		}
	}
}

// #306: only identical definitions are ported; a union value whose member realtype is the
// destination's own (merged there and back) compares its member again.
func TestCrossContextDefinitions(t *testing.T) {
	u8 := func() *schema.Type { return &schema.Type{Base: schema.Uint8} }
	enum := func(v int32) *schema.Type {
		return &schema.Type{Base: schema.Enumeration, Enums: []*schema.Enum{{Name: "a", Value: v}}}
	}
	for _, tc := range []struct {
		name string
		a, b *schema.Type
		same bool
	}{
		{"uint8", u8(), u8(), true},
		{"uint8-uint16", u8(), &schema.Type{Base: schema.Uint16}, false},
		{"string-bits", &schema.Type{Base: schema.String}, &schema.Type{Base: schema.Bits}, false},
		{"enum-revalued", enum(0), enum(7), false},
		{"dec-digits", &schema.Type{Base: schema.Dec64, FracDigits: 1}, &schema.Type{Base: schema.Dec64, FracDigits: 2}, false},
		{"union", &schema.Type{Base: schema.Union, Union: []*schema.Type{u8()}}, &schema.Type{Base: schema.Union, Union: []*schema.Type{u8()}}, true},
		{"union-members", &schema.Type{Base: schema.Union, Union: []*schema.Type{u8()}}, &schema.Type{Base: schema.Union, Union: []*schema.Type{enum(0)}}, false},
	} {
		if got := (&DefWalk{}).same(tc.a, tc.b); got != tc.same {
			t.Errorf("%s: same definition %v", tc.name, got)
		}
	}
	bits := func() *schema.Type { return &schema.Type{Base: schema.Bits, Bits: []*schema.Bit{{Name: "x"}}} }
	ua, ub := &schema.Type{Base: schema.Union, Union: []*schema.Type{bits()}}, &schema.Type{Base: schema.Union, Union: []*schema.Type{bits()}}
	member := func(u *schema.Type, m *schema.Type) Value {
		return Value{typ: u, union: &UnionValue{member: Value{typ: m, bits: m.Bits}}}
	}
	if equal, _, _, _ := CrossContextChange(nil, ua, ub, member(ua, ua.Union[0]), member(ub, ub.Union[0]), nil); equal {
		t.Error("union members of two contexts compared equal")
	}
	if equal, _, _, _ := CrossContextChange(nil, ua, ub, member(ua, ua.Union[0]), member(ub, ua.Union[0]), nil); !equal {
		t.Error("a union value merged back (the destination's own member realtype) compared different")
	}
}

// #306: lyplg_type_dup_xpath10 sets the destination context; RebindContext moves a JSON value's
// module-name resolution there and leaves the source value as it was.
func TestRebindContextXPath(t *testing.T) {
	a, b := &schema.Set{}, &schema.Set{}
	src := Value{ext: &keysValue{f: FormatJSON, pc: ModuleNames{Set: a}}}
	got := src.RebindContext(b)
	if got.ext.(*keysValue).pc != (ModuleNames{Set: b}) || src.ext.(*keysValue).pc != (ModuleNames{Set: a}) {
		t.Fatal("JSON prefixes not rebound to the destination, or the source changed")
	}
	// XML prefix data is prefix - URI pairs, resolved in the destination context after the dup
	q := &schema.Module{Name: "q", Namespace: "urn:q"}
	a.Modules = []*schema.Module{q}
	xml := Value{ext: &keysValue{f: FormatXML, pc: XMLNamespaces{Set: a, NS: map[string]string{"q": "urn:q"}}}}
	if xml.ext.(*keysValue).pc.Resolve("q") != q {
		t.Fatal("the source does not resolve its own namespace")
	}
	if m := xml.RebindContext(b).ext.(*keysValue).pc.Resolve("q"); m != nil {
		t.Fatalf("XML namespace of a module the destination lacks resolved to %v", m.Name)
	}
	if xml.ext.(*keysValue).pc.Resolve("q") != q {
		t.Fatal("the source's XML prefix data changed")
	}
	sch := Value{ext: &keysValue{f: FormatSchema, pc: prefixSnap{"q": q}}}
	if sch.RebindContext(b).ext.(*keysValue).pc.Resolve("q") != q {
		t.Fatal("schema prefix data must keep its module pointers")
	}
}

// sharedLeafrefGraph is n0...n(depth-1), each a union of bits and two leafrefs whose realtype is
// the next one, the last plain bits: a small acyclic graph with 2^depth paths.
func sharedLeafrefGraph(depth int) *schema.Type {
	next := &schema.Type{Base: schema.Bits, Bits: []*schema.Bit{{Name: "x"}}}
	for range depth {
		lr := func() *schema.Type { return &schema.Type{Base: schema.Leafref, Realtype: next} }
		next = &schema.Type{Base: schema.Union, Union: []*schema.Type{
			{Base: schema.Bits, Bits: []*schema.Bit{{Name: "x"}}}, lr(), lr()}}
	}
	return next
}

func TestCrossContextSharedGraphLinear(t *testing.T) {
	a, b := sharedLeafrefGraph(33), sharedLeafrefGraph(33)
	member := func(u *schema.Type) Value {
		return Value{typ: u, union: &UnionValue{member: Value{typ: u.Union[0], bits: u.Union[0].Bits}}}
	}
	var pairs uint64
	charge := func(uint64) error { pairs++; return nil }
	_, _, undefined, err := CrossContextChange(nil, a, b, member(a), member(b), charge)
	if err != nil || undefined != 0 {
		t.Fatalf("undefined %v, err %v; want identical definitions", undefined, err)
	}
	if pairs > 4*34 {
		t.Fatalf("%d type pairs compared; want each shared pair once", pairs)
	}
	budget := errors.New("budget")
	var left uint64 = 10
	_, _, _, err = CrossContextChange(nil, a, b, member(a), member(b), func(n uint64) error {
		if n > left {
			return budget
		}
		left -= n
		return nil
	})
	if !errors.Is(err, budget) {
		t.Fatalf("err %v; want the exhausted budget", err)
	}
}

func TestCrossContextCyclicDefinition(t *testing.T) {
	cyclic := func() *schema.Type {
		u := &schema.Type{Base: schema.Union}
		u.Union = []*schema.Type{{Base: schema.Leafref, Realtype: u}}
		return u
	}
	if !(&DefWalk{}).same(cyclic(), cyclic()) {
		t.Fatal("identical cyclic definitions compared different")
	}
	a, b := cyclic(), cyclic()
	b.Union = append(b.Union, &schema.Type{Base: schema.Bits})
	if (&DefWalk{}).same(a, b) {
		t.Fatal("different cyclic definitions compared identical")
	}
}
