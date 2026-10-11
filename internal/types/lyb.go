// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/{binary,bits,boolean,date_and_time,
// decimal64,empty,enumeration,identityref,instanceid,integer,ipv4_address,
// ipv4_address_no_zone,ipv4_address_prefix,ipv6_address,ipv6_address_no_zone,
// ipv6_address_prefix,string,union}.c (LY_VALUE_LYB print callbacks) (BSD-3-Clause,
// © CESNET).

package types

import (
	"bytes"
	"encoding/binary"
	mathbits "math/bits"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// EqualLYB reports whether two stored values contribute the same byte payload to lyd_hash when
// their real type plugins print LY_VALUE_LYB. It is used only for libyang's cross-context
// data-node hash comparison; normal typed equality remains Equal.
// Each top-level empty payload causes the same finalization (hash_table.c:58-61);
// recursive union members are payload bytes, not separate hash calls (union.c:660-666).
func EqualLYB(a, b Value) bool {
	au, bu := a.Union(), b.Union()
	if au != nil || bu != nil {
		if au == nil || bu == nil {
			return false
		}
		am, ai := au.Member()
		bm, bi := bu.Member()
		// lyplg_type_print_union prefixes the member payload with its one-byte type index.
		return byte(ai) == byte(bi) && EqualLYB(am, bm) //nolint:gosec // LYB stores the union index in one byte
	}
	ar, aok := lybRepr(a)
	br, bok := lybRepr(b)
	return aok && bok && equalLYBRepr(ar, br)
}

// HasSparseBits reports whether v's LYB representation is the sparse bitmap used for bits.
// Union values use the selected member's representation.
func HasSparseBits(v Value) bool {
	if u := v.Union(); u != nil {
		member, _ := u.Member()
		return HasSparseBits(member)
	}
	r, ok := lybRepr(v)
	return ok && r.sparseBits
}

type lybRepresentation struct {
	data       []byte
	bits       uint64
	setBits    []*schema.Bit
	sparseBits bool
}

func equalLYBRepr(a, b lybRepresentation) bool {
	switch {
	case !a.sparseBits && !b.sparseBits:
		// lyd_hash() hashes LYPLG_BITS2BYTES(value_size_bits), so fixed bit widths that
		// round up to the same byte count do not by themselves distinguish values.
		return bytes.Equal(a.data, b.data)
	case a.sparseBits && b.sparseBits:
		if (a.bits+7)/8 != (b.bits+7)/8 || len(a.setBits) != len(b.setBits) {
			return false
		}
		for i := range a.setBits {
			if a.setBits[i].Position != b.setBits[i].Position {
				return false
			}
		}
		return true
	case b.sparseBits:
		return equalSparseLYB(b, a.data)
	default:
		return equalSparseLYB(a, b.data)
	}
}

func equalSparseLYB(sparse lybRepresentation, data []byte) bool {
	if byteCount(len(data)) != (sparse.bits+7)/8 {
		return false
	}
	bit := 0
	for i, byteIndex := 0, uint32(0); i < len(data); i, byteIndex = i+1, byteIndex+1 {
		var want byte
		for bit < len(sparse.setBits) && sparse.setBits[bit].Position/8 == byteIndex {
			want |= 1 << (sparse.setBits[bit].Position % 8)
			bit++
		}
		if data[i] != want {
			return false
		}
	}
	return true
}

func lybRepr(v Value) (lybRepresentation, bool) {
	if v.typ == nil {
		return lybRepresentation{}, false
	}
	if p := pluginFor(v.typ); p != nil {
		return pluginLYB(v, p.id)
	}

	switch v.typ.Base {
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64,
		schema.Int8, schema.Int16, schema.Int32, schema.Int64:
		n := integerLYB(v)
		return uintLYB(n, uint64(mathbits.Len64(n))), true //nolint:gosec // result is in [0, 64]
	case schema.Binary:
		return lybRepresentation{data: v.bin, bits: byteCount(len(v.bin)) * 8}, true
	case schema.String:
		return stringLYB(v.canon), true
	case schema.Bits:
		if len(v.typ.Bits) == 0 {
			// A synthetic Go value can have no declarations. libyang rejects this
			// schema (schema_compile_node.c:1639-1646); its printer assumes a last
			// declared bit (plugins_types/bits.c:46,405).
			return lybRepresentation{}, true
		}
		n := uint64(v.typ.Bits[len(v.typ.Bits)-1].Position) + 1
		// Keep the bitmap sparse so a legal bit at position MaxUint32 does not allocate 512 MiB.
		return lybRepresentation{bits: n, setBits: v.bits, sparseBits: true}, true
	case schema.Bool:
		return lybRepresentation{data: []byte{byte(v.i)}, bits: 1}, true //nolint:gosec // stored boolean is 0 or 1
	case schema.Dec64:
		return uintLYB(uint64(v.i), 64), true //nolint:gosec // two's-complement LYB bit pattern
	case schema.Empty:
		return lybRepresentation{}, true
	case schema.Enumeration:
		if v.enum == nil {
			return lybRepresentation{}, false
		}
		return enumLYB(v), true
	case schema.IdentityRef, schema.InstanceID:
		// Both callbacks print module-name prefixes in LY_VALUE_LYB, which is their canonical
		// representation in the Go value model.
		return stringLYB(v.canon), true
	default:
		return lybRepresentation{}, false
	}
}

func integerLYB(v Value) uint64 {
	switch v.typ.Base {
	case schema.Uint8, schema.Uint16, schema.Uint32, schema.Uint64:
		return v.u
	case schema.Int8:
		return uint64(v.i) & (1<<8 - 1) //nolint:gosec // two's-complement LYB bit pattern
	case schema.Int16:
		return uint64(v.i) & (1<<16 - 1) //nolint:gosec // two's-complement LYB bit pattern
	case schema.Int32:
		return uint64(v.i) & (1<<32 - 1) //nolint:gosec // two's-complement LYB bit pattern
	default:
		return uint64(v.i) //nolint:gosec // two's-complement LYB bit pattern
	}
}

func uintLYB(n, sizeBits uint64) lybRepresentation {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], n)
	return lybRepresentation{data: buf[:(sizeBits+7)/8], bits: sizeBits}
}

func stringLYB(s string) lybRepresentation {
	// The simple print callback measures its C string with strlen.
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return lybRepresentation{data: []byte(s), bits: byteCount(len(s)) * 8}
}

func byteCount(n int) uint64 {
	return uint64(n) //nolint:gosec // callers pass Go string/slice lengths, which are nonnegative
}

func enumLYB(v Value) lybRepresentation {
	var maxValue uint32
	for _, item := range v.typ.Enums {
		if item.Value < 0 {
			// The fixed 32-bit LYB field preserves the signed enumeration bit pattern.
			return uintLYB(uint64(uint32(v.enum.Value)), 32) //nolint:gosec // two's-complement LYB bit pattern
		}
		maxValue = max(maxValue, uint32(item.Value))
	}
	// With no negative declaration, the selected value and highest-bit position are nonnegative.
	return uintLYB(uint64(uint32(v.enum.Value)), uint64(mathbits.Len32(maxValue))) //nolint:gosec // nonnegative enum; bit width is in [0, 32]
}

func pluginLYB(v Value, id string) (lybRepresentation, bool) {
	switch id {
	case "ipv4-address", "ipv4-address-no-zone", "ipv4-address-prefix",
		"ipv6-address", "ipv6-address-no-zone", "ipv6-address-prefix":
		ip, ok := v.ext.(*ipValue)
		if !ok {
			return lybRepresentation{}, false
		}
		data := append([]byte(nil), ip.addr...)
		switch id {
		case "ipv4-address", "ipv6-address":
			if ip.hasZ {
				data = append(data, ip.zone...)
			}
		case "ipv4-address-prefix", "ipv6-address-prefix":
			data = append(data, ip.prefix)
		}
		return lybRepresentation{data: data, bits: byteCount(len(data)) * 8}, true
	case "date-and-time":
		dt, ok := v.ext.(*dateTime)
		if !ok {
			return lybRepresentation{}, false
		}
		r := uintLYB(uint64(dt.unix), 64) //nolint:gosec // signed Unix time's LYB bit pattern
		if dt.unknownTZ || dt.hasFrac {
			r.data = append(r.data, byte(0))
			if dt.unknownTZ {
				r.data[8] = 1
			}
			r.data = append(r.data, dt.frac...)
			r.bits = byteCount(len(r.data)) * 8
		}
		return r, true
	case "hex-string", "time-period":
		// These records use the string LYB print callback.
		return stringLYB(v.canon), true
	default:
		return remainingPluginLYB(v, id)
	}
}
