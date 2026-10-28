// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func storeProfileValue(t *testing.T, ty *schema.Type, lex string) Value {
	t.Helper()
	v, d := Store(ty, lex, FormatXML, HintData, nil, nil)
	if d != nil {
		t.Fatalf("store %q: %v", lex, d.Msg)
	}
	return v
}

// abiRead is the D-0121 R1/R2 model on the canonical linux/amd64 build: the second value's
// payload is its declared-width little-endian two's complement in a zeroed union (all zero for a
// plain string), and the first value's callback reads its own field width from those bytes.
func abiRead(first schema.BaseType, payload uint64) (int64, uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], payload)
	switch first {
	case schema.Int8:
		return int64(int8(b[0])), 0 //nolint:gosec // reinterpretation under test
	case schema.Int16:
		return int64(int16(binary.LittleEndian.Uint16(b[:]))), 0 //nolint:gosec // reinterpretation under test
	case schema.Int32:
		return int64(int32(binary.LittleEndian.Uint32(b[:]))), 0 //nolint:gosec // reinterpretation under test
	case schema.Int64:
		return int64(binary.LittleEndian.Uint64(b[:])), 0 //nolint:gosec // reinterpretation under test
	case schema.Uint8:
		return 0, uint64(b[0])
	case schema.Uint16:
		return 0, uint64(binary.LittleEndian.Uint16(b[:]))
	case schema.Uint32:
		return 0, uint64(binary.LittleEndian.Uint32(b[:]))
	}
	return 0, binary.LittleEndian.Uint64(b[:])
}

func sign(n int) int { return cmp3(n < 0, n > 0) }

// TestCompareCompatibleR1Widths: every ordered pair of distinct widths within one signedness, at
// the extrema and around every truncation boundary, compares the first value with the second
// value's payload read at the first value's width, not numerically.
func TestCompareCompatibleR1Widths(t *testing.T) {
	signed := []schema.BaseType{schema.Int8, schema.Int16, schema.Int32, schema.Int64}
	unsigned := []schema.BaseType{schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64}
	var sv []int64
	var uv []uint64
	sv = append(sv, 0, 1, -1, 2, -2, math.MinInt64, math.MaxInt64)
	uv = append(uv, 0, 1, 2, math.MaxUint64)
	for _, k := range []uint{7, 8, 15, 16, 31, 32} {
		p := int64(1) << k
		sv = append(sv, p-1, p, p+1, -p-1, -p, -p+1)
		uv = append(uv, uint64(p)-1, uint64(p), uint64(p)+1) //nolint:gosec // positive boundaries
	}
	signedRange := map[schema.BaseType][2]int64{schema.Int8: {math.MinInt8, math.MaxInt8},
		schema.Int16: {math.MinInt16, math.MaxInt16}, schema.Int32: {math.MinInt32, math.MaxInt32},
		schema.Int64: {math.MinInt64, math.MaxInt64}}
	unsignedMax := map[schema.BaseType]uint64{schema.Uint8: math.MaxUint8, schema.Uint16: math.MaxUint16,
		schema.Uint32: math.MaxUint32, schema.Uint64: math.MaxUint64}
	pairs := 0
	for _, fa := range signed {
		for _, fb := range signed {
			if fa == fb {
				continue
			}
			ta, tb := &schema.Type{Base: fa}, &schema.Type{Base: fb}
			for _, x := range sv {
				if x < signedRange[fa][0] || x > signedRange[fa][1] {
					continue
				}
				for _, y := range sv {
					if y < signedRange[fb][0] || y > signedRange[fb][1] {
						continue
					}
					a, b := storeProfileValue(t, ta, fmt.Sprint(x)), storeProfileValue(t, tb, fmt.Sprint(y))
					read, _ := abiRead(fa, integerLYB(b))
					got, ok := CompareCompatible(a, b)
					if !ok || sign(got) != cmp3(x < read, x > read) {
						t.Fatalf("%v(%d) vs %v(%d) = (%d, %t), want sign of %d vs %d", fa, x, fb, y, got, ok, x, read)
					}
					pairs++
				}
			}
		}
	}
	for _, fa := range unsigned {
		for _, fb := range unsigned {
			if fa == fb {
				continue
			}
			ta, tb := &schema.Type{Base: fa}, &schema.Type{Base: fb}
			for _, x := range uv {
				if x > unsignedMax[fa] {
					continue
				}
				for _, y := range uv {
					if y > unsignedMax[fb] {
						continue
					}
					a, b := storeProfileValue(t, ta, fmt.Sprint(x)), storeProfileValue(t, tb, fmt.Sprint(y))
					_, read := abiRead(fa, integerLYB(b))
					got, ok := CompareCompatible(a, b)
					if !ok || sign(got) != cmp3(x < read, x > read) {
						t.Fatalf("%v(%d) vs %v(%d) = (%d, %t), want sign of %d vs %d", fa, x, fb, y, got, ok, x, read)
					}
					pairs++
				}
			}
		}
	}
	// The pinned non-numeric examples of D-0121 R1.
	i8, i16 := &schema.Type{Base: schema.Int8}, &schema.Type{Base: schema.Int16}
	if c, _ := CompareCompatible(storeProfileValue(t, i16, "1"), storeProfileValue(t, i8, "-1")); c >= 0 {
		t.Fatal("int16 callback must read int8(-1) as 255")
	}
	if c, _ := CompareCompatible(storeProfileValue(t, i8, "0"), storeProfileValue(t, i16, "256")); c != 0 {
		t.Fatal("int8 callback must read int16(256) as 0")
	}
	t.Logf("%d ordered pairs", pairs)
}

// TestCompareCompatibleR2R3: an integer and a plain string, and the ipv4-address implementation
// and a plain string, in both operand directions.
func TestCompareCompatibleR2R3(t *testing.T) {
	str := &schema.Type{Base: schema.String}
	plain := []string{"", "-1", "0", "2", "abc", "1.2.3.4", "0.0.0.0"}
	for _, base := range []schema.BaseType{schema.Int8, schema.Int16, schema.Int32, schema.Int64,
		schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64} {
		ty := &schema.Type{Base: base}
		lexes := []string{"0", "1", "2", "127"}
		if base <= schema.Int64 && base >= schema.Int8 {
			lexes = append(lexes, "-1", "-128")
		}
		for _, lex := range lexes {
			n := storeProfileValue(t, ty, lex)
			for _, s := range plain {
				v := storeProfileValue(t, str, s)
				// integer first: the zero payload of string storage, so the sign of the integer
				x, _ := strconv.ParseInt(lex, 10, 64)
				want := cmp3(x < 0, x > 0)
				if got, ok := CompareCompatible(n, v); !ok || sign(got) != sign(want) {
					t.Fatalf("%v(%s) vs string %q = (%d, %t), want %d", base, lex, s, got, ok, want)
				}
				// string first: lyplg_type_sort_simple over canonical text
				if got, ok := CompareCompatible(v, n); !ok || sign(got) != strings.Compare(s, lex) {
					t.Fatalf("string %q vs %v(%s) = (%d, %t), want %d", s, base, lex, got, ok, strings.Compare(s, lex))
				}
			}
		}
	}
	inet := &schema.Module{Name: "ietf-inet-types"}
	ipv4 := &schema.Type{Base: schema.String, Typedef: "ipv4-address", TypedefModule: inet}
	link := &schema.Type{Base: schema.String, Typedef: "ipv4-address-link-local", TypedefModule: inet}
	for _, tc := range []struct {
		ty   *schema.Type
		addr string
		want int // ipv4 first: address bytes, then a zone sorts after none
	}{
		{ipv4, "0.0.0.0", 0}, {ipv4, "0.0.0.0%eth0", 1}, {ipv4, "1.2.3.4", 1},
		{ipv4, "1.2.3.4%z", 1}, {ipv4, "255.255.255.255", 1}, {link, "169.254.0.1", 1},
	} {
		a := storeProfileValue(t, tc.ty, tc.addr)
		for _, s := range plain {
			v := storeProfileValue(t, str, s)
			if got, ok := CompareCompatible(a, v); !ok || sign(got) != tc.want {
				t.Fatalf("ipv4 %q vs string %q = (%d, %t), want %d", tc.addr, s, got, ok, tc.want)
			}
			if got, ok := CompareCompatible(v, a); !ok || sign(got) != strings.Compare(s, a.Canonical()) {
				t.Fatalf("string %q vs ipv4 %q = (%d, %t)", s, tc.addr, got, ok)
			}
		}
	}
}

// TestCompareCompatibleClosedNeighbours: the neighbours of R1-R3 stay refused in both
// directions: signed against unsigned, plugins other than ipv4-address (whatever their base),
// integers against any plugin, and other built-in pairs with deterministic C storage.
func TestCompareCompatibleClosedNeighbours(t *testing.T) {
	inet := &schema.Module{Name: "ietf-inet-types"}
	yt := &schema.Module{Name: "ietf-yang-types"}
	ln2 := &schema.Module{Name: "libnetconf2-netconf-server"}
	i8, u8 := &schema.Type{Base: schema.Int8}, &schema.Type{Base: schema.Uint8}
	str := &schema.Type{Base: schema.String}
	ipv4 := &schema.Type{Base: schema.String, Typedef: "ipv4-address", TypedefModule: inet}
	noZone := &schema.Type{Base: schema.String, Typedef: "ipv4-address-no-zone", TypedefModule: inet}
	ipv6 := &schema.Type{Base: schema.String, Typedef: "ipv6-address", TypedefModule: inet}
	hex := &schema.Type{Base: schema.String, Typedef: "hex-string", TypedefModule: yt}
	period := &schema.Type{Base: schema.String, Typedef: "time-period", TypedefModule: ln2}
	boolean := &schema.Type{Base: schema.Bool}
	dec := &schema.Type{Base: schema.Dec64, FracDigits: 1}
	empty := &schema.Type{Base: schema.Empty}
	for _, tc := range []struct {
		a, b       *schema.Type
		alex, blex string
	}{
		{i8, u8, "1", "2"}, {i8, ipv4, "1", "1.2.3.4"}, {u8, hex, "1", "aa"}, {i8, period, "1", "1h"},
		{str, noZone, "a", "1.2.3.4"}, {str, ipv6, "a", "::1"}, {str, hex, "a", "aa"}, {str, period, "a", "1h"},
		{ipv4, noZone, "1.2.3.4", "1.2.3.4"}, {i8, boolean, "1", "true"}, {i8, dec, "1", "1.0"},
		{i8, empty, "1", ""}, {str, boolean, "a", "true"},
	} {
		a, b := storeProfileValue(t, tc.a, tc.alex), storeProfileValue(t, tc.b, tc.blex)
		for _, pair := range [][2]Value{{a, b}, {b, a}} {
			if _, ok := CompareCompatible(pair[0], pair[1]); ok {
				t.Errorf("%s/%s accepted, want the closed-profile refusal", pair[0].Canonical(), pair[1].Canonical())
			}
		}
	}
	// time-period indexes before an empty canonical value, within one realtype too
	e, h := storeProfileValue(t, period, ""), storeProfileValue(t, period, "1h")
	for _, pair := range [][2]Value{{e, h}, {h, e}, {e, e}} {
		if _, ok := CompareCompatible(pair[0], pair[1]); ok {
			t.Error("empty time-period sorted")
		}
	}
	if _, ok := CompareCompatible(h, h); !ok {
		t.Error("time-period refused")
	}
}

// TestCompareCompatibleUnionMember: lyplg_type_sort_union calls the selected members' realtype
// callback when both select one realtype (union.c), so that callback's refusal applies through
// the union, nested unions included. Different members order by member position, which invokes
// no member callback and is compatible.
func TestCompareCompatibleUnionMember(t *testing.T) {
	ln2 := &schema.Module{Name: "libnetconf2-netconf-server"}
	period := &schema.Type{Base: schema.String, Typedef: "time-period", TypedefModule: ln2}
	u8 := &schema.Type{Base: schema.Uint8}
	inner := &schema.Type{Base: schema.Union, Union: []*schema.Type{u8, period}} // "5" selects uint8
	nested := &schema.Type{Base: schema.Union, Union: []*schema.Type{inner, {Base: schema.String}}}
	for _, ut := range []*schema.Type{inner, nested} {
		e, h, n := storeProfileValue(t, ut, ""), storeProfileValue(t, ut, "1h"), storeProfileValue(t, ut, "5")
		for _, pair := range [][2]Value{{e, h}, {h, e}, {e, e}} {
			if _, ok := CompareCompatible(pair[0], pair[1]); ok {
				t.Errorf("union %q/%q: empty time-period member sorted", pair[0].Canonical(), pair[1].Canonical())
			}
		}
		for _, pair := range [][2]Value{{h, h}, {e, n}, {n, e}, {h, n}, {n, n}} {
			if got, ok := CompareCompatible(pair[0], pair[1]); !ok || got != Compare(pair[0], pair[1]) {
				t.Errorf("union %q/%q = (%d, %t), want (%d, true)", pair[0].Canonical(), pair[1].Canonical(), got, ok, Compare(pair[0], pair[1]))
			}
		}
	}
}
