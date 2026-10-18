// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/{binary,bits,boolean,date_and_time,
// decimal64,empty,enumeration,identityref,instanceid,integer,ipv4_address,
// ipv4_address_no_zone,ipv4_address_prefix,ipv6_address,ipv6_address_no_zone,
// ipv6_address_prefix,string,union}.c (LY_VALUE_LYB print callbacks) (BSD-3-Clause,
// © CESNET).

package types

import (
	"encoding/binary"
	mathbits "math/bits"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

type lybRepresentation struct {
	data       []byte
	bits       uint64
	setBits    []*schema.Bit
	sparseBits bool
	work       uint64 // declaration visits needed by a synthetic, unprepared type
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
	case schema.IdentityRef:
		// Only LY_VALUE_CANON uses the cache. LYB regenerates module:name from the
		// resolved identity, even when cross-context duplication retained another text.
		if v.ident == nil || v.ident.Module == nil {
			return lybRepresentation{}, false
		}
		return stringLYB(v.ident.Module.Name + ":" + v.ident.Name), true
	case schema.InstanceID:
		// Unlike identityref, instanceid explicitly returns the cache for LYB.
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
	width := v.typ.EnumLYBWidth
	var work uint64
	if width == 0 {
		// Hand-built internal types have no compilation stage. Do not mutate them:
		// callers may share these synthetic types between concurrent readers.
		width, work = enumLYBWidth(v.typ)
	}
	r := uintLYB(uint64(uint32(v.enum.Value)), uint64(width-1)) //nolint:gosec // two's-complement LYB bit pattern
	r.work = work
	return r
}

// PrepareEnumLYB precomputes declaration-dependent LYB width before a compiled
// type is published. A disabled declaration removal calls it again. Snapshot's
// scalar copy preserves the result without another declaration scan.
func PrepareEnumLYB(t *schema.Type) {
	if t.Base == schema.Enumeration {
		t.EnumLYBWidth, _ = enumLYBWidth(t)
	}
}

// enumLYBWidth is lyplg_type_lyb_size_enum (enumeration.c:40-66). Native rescans
// declarations at print time; immutable compiled types let us do this once.
func enumLYBWidth(t *schema.Type) (uint8, uint64) {
	var maxValue uint32
	for i, item := range t.Enums {
		if item.Value < 0 {
			// The fixed 32-bit LYB field preserves the signed enumeration bit pattern.
			return 33, uint64(i + 1)
		}
		maxValue = max(maxValue, uint32(item.Value))
	}
	// With no negative declaration, the selected value and highest-bit position are nonnegative.
	return uint8(mathbits.Len32(maxValue)) + 1, uint64(len(t.Enums)) //nolint:gosec // width [0, 32], nonnegative length
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
