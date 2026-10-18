// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"bytes"
	"testing"
)

// D-0130: time.c:387-399 allocates seconds + flag + fractions but skips the
// flag copy for time-no-zone. The port initializes that byte even after other
// variable-length values have been hashed. Fraction digits remain significant.
func TestHashLYBTimeNoZonePadding(t *testing.T) {
	typ := ietfTypes("2025-12-22")["time-no-zone"]
	for _, tc := range []struct {
		text string
		lyb  []byte
	}{
		{"00:00:01", []byte{1, 0, 0, 0}},
		{"00:00:01.25", []byte{1, 0, 0, 0, 0, '2', '5'}},
		{"00:00:01.0", []byte{1, 0, 0, 0, 0, '0'}},
		{"00:00:01.00", []byte{1, 0, 0, 0, 0, '0', '0'}},
	} {
		t.Run(tc.text, func(t *testing.T) {
			const seed = uint32(0x12345678)
			for range 64 {
				v, d := StoreOnly(typ, tc.text, FormatXML, HintString, nil, nil)
				if d != nil {
					t.Fatal(d.Msg)
				}
				r, ok := lybRepr(v)
				if !ok || !bytes.Equal(r.data, tc.lyb) || r.bits != uint64(len(tc.lyb))*8 {
					t.Fatalf("LYB = %x (%d bits), want %x", r.data, r.bits, tc.lyb)
				}
				if hash, ok := HashLYB(seed, v); !ok || hash != HashMulti32(seed, tc.lyb) {
					t.Fatalf("hash = %#x, ok = %t", hash, ok)
				}
			}
		})
	}
}

// The oracle fixture compares a retained string hash against a fresh fractional
// time-no-zone hash. Its result must not depend on the native uninitialized byte.
func TestHashLYBTimeNoZoneOracleComparison(t *testing.T) {
	seed := HashMulti32(0, []byte("pv2-hash-sweep"))
	seed = HashMulti32(seed, []byte("lyb-time-no-zone-fraction"))
	textHash := FinishHash32(HashMulti32(seed, []byte("00:00:01.25")))
	for flag := range 256 {
		payload := []byte{1, 0, 0, 0, byte(flag), '2', '5'}
		if FinishHash32(HashMulti32(seed, payload)) == textHash {
			t.Fatalf("native byte %d makes the oracle comparison allocator-dependent", flag)
		}
	}
}

// On a native ABI with 32-bit time_t, date[_and_time].c copies only four
// timestamp bytes into an eight-byte LYB field. Go always writes the full field,
// including both zero and sign-extension bytes (the conditional D-0130 case).
func TestHashLYBTimestampInitialization(t *testing.T) {
	ts := ietfTypes("2025-12-22")
	for _, tc := range []struct {
		typ, text string
		payload   []byte
	}{
		{"date", "1970-01-01Z", []byte{0, 0, 0, 0, 0, 0, 0, 0, 1}},
		{"date-and-time", "1970-01-01T00:00:00.25Z", []byte{0, 0, 0, 0, 0, 0, 0, 0, 1, '2', '5'}},
		{"date-and-time", "1969-12-31T23:59:59.25Z", []byte{255, 255, 255, 255, 255, 255, 255, 255, 1, '2', '5'}},
	} {
		t.Run(tc.text, func(t *testing.T) {
			v, d := StoreOnly(ts[tc.typ], tc.text, FormatXML, HintString, nil, nil)
			if d != nil {
				t.Fatal(d.Msg)
			}
			if r, ok := lybRepr(v); !ok || !bytes.Equal(r.data, tc.payload) {
				t.Fatalf("LYB = %x, want %x", r.data, tc.payload)
			}
		})
	}
}
