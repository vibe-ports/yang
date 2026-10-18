// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// Enumeration and bits print their declared width, including unselected values.
// These byte-boundary vectors also distinguish a negative enum's fixed width.
func TestHashLYBDeclarationWidth(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  *schema.Type
		want []byte
	}{
		{"enum-byte", &schema.Type{Base: schema.Enumeration, Enums: []*schema.Enum{
			{Name: "keep", Value: 1}, {Name: "unused", Value: 255},
		}}, []byte{1}},
		{"enum-two-bytes", &schema.Type{Base: schema.Enumeration, Enums: []*schema.Enum{
			{Name: "keep", Value: 1}, {Name: "unused", Value: 256},
		}}, []byte{1, 0}},
		{"enum-negative-declaration", &schema.Type{Base: schema.Enumeration, Enums: []*schema.Enum{
			{Name: "unused", Value: -1}, {Name: "keep", Value: 1},
		}}, []byte{1, 0, 0, 0}},
		{"bits-byte", &schema.Type{Base: schema.Bits, Bits: []*schema.Bit{
			{Name: "keep", Position: 0}, {Name: "unused", Position: 7},
		}}, []byte{1}},
		{"bits-two-bytes", &schema.Type{Base: schema.Bits, Bits: []*schema.Bit{
			{Name: "keep", Position: 0}, {Name: "unused", Position: 8},
		}}, []byte{1, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, diag := StoreOnly(tc.typ, "keep", FormatJSON, HintData, nil, nil)
			if diag != nil {
				t.Fatal(diag.Msg)
			}
			const seed = uint32(0x12345678)
			if got, ok := HashLYB(seed, v); !ok || got != HashMulti32(seed, tc.want) {
				t.Fatalf("declaration-width hash = %#x, %t; want bytes %x", got, ok, tc.want)
			}
		})
	}
}
