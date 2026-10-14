// SPDX-License-Identifier: BSD-3-Clause
// Test cases derived from libyang v5.8.6 src/plugins_types/{date,time,xpath1.0,
// node_instanceid,instanceid_keys}.c (BSD-3-Clause, © CESNET).

package types

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func TestPluginLYBRepresentations(t *testing.T) {
	ts := ietfTypes("2025-12-22")
	store := func(typ *schema.Type, lex string) Value {
		t.Helper()
		v, d := StoreOnly(typ, lex, FormatXML, HintString, nil, nil)
		if d != nil {
			t.Fatal(d.Msg)
		}
		return v
	}
	wantDate := make([]byte, 9)
	const wantDateUnixSeconds uint64 = 1704153600 // 2024-01-02T00:00:00Z
	binary.LittleEndian.PutUint64(wantDate, wantDateUnixSeconds)
	wantDate[8] = 1
	wantTime := []byte{1, 0, 0, 0, 1, '2', '5'}

	for _, tc := range []struct {
		name string
		v    Value
		want []byte
		bits uint64
	}{
		{"date", store(ts["date"], "2024-01-02Z"), wantDate, 72},
		{"time", store(ts["time"], "00:00:01.25Z"), wantTime, 56},
		{"xpath1.0", Value{typ: ts["xpath1.0"], canon: "/mod:top/mod:item"}, []byte("/mod:top/mod:item"), 17 * 8},
		{"node-instance-identifier", Value{typ: pluginStringType("ietf-netconf-acm", "node-instance-identifier"), canon: "/mod:top"}, []byte("/mod:top"), 8 * 8},
		{"instance-identifier-keys", Value{typ: pluginStringType("yang", "instance-identifier-keys"), canon: "[mod:id='a']"}, []byte("[mod:id='a']"), 12 * 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := lybRepr(tc.v)
			if !ok || got.bits != tc.bits || !bytes.Equal(got.data, tc.want) {
				t.Fatalf("lybRepr = (%x, %d, %v), want (%x, %d, true)", got.data, got.bits, ok, tc.want, tc.bits)
			}
		})
	}
}

func pluginStringType(module, typedef string) *schema.Type {
	return &schema.Type{Base: schema.String, Typedef: typedef, TypedefModule: &schema.Module{Name: module}}
}

func TestCompareCompatiblePlugin(t *testing.T) {
	aTypes, bTypes := ietfTypes("2025-12-22"), ietfTypes("2025-12-22")
	a, da := StoreOnly(aTypes["ipv4-address"], "2.0.0.0", FormatXML, HintString, nil, nil)
	b, db := StoreOnly(bTypes["ipv4-address"], "10.0.0.0", FormatXML, HintString, nil, nil)
	if da != nil || db != nil {
		t.Fatal(da, db)
	}
	if Compare(a, b) <= 0 {
		t.Fatal("ordinary cross-type comparison unexpectedly used the plugin sort callback")
	}
	if got, compatible := CompareCompatible(a, b); !compatible || got >= 0 {
		t.Fatalf("CompareCompatible = (%d, %v), want negative, true", got, compatible)
	}
}
