// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestCheckValue: the store of warn_equality_value over hand-built leaves.
func TestCheckValue(t *testing.T) {
	m := &schema.Module{Name: "m", Namespace: "urn:m", Prefix: "m", Implemented: true}
	o := &schema.Module{Name: "o", Namespace: "urn:o", Prefix: "o"} // import-only
	base := &schema.Identity{Name: "base", Module: o}
	der := &schema.Identity{Name: "der", Module: o}
	base.Derived = []*schema.Identity{der}
	o.Identities = []*schema.Identity{base, der}
	idT := &schema.Type{Base: schema.IdentityRef, Bases: []*schema.Identity{base}}
	enT := &schema.Type{Base: schema.Enumeration, Enums: []*schema.Enum{{Name: "red"}}}
	leaf := func(name string, t *schema.Type) *schema.Node {
		return &schema.Node{Kind: schema.Leaf, Name: name, Module: m, Type: t, Config: true}
	}
	// the prefix data of a must in module m, which imports o as "imp"
	ns := schema.NSCtx{"": m, "m": m, "imp": o}
	u := &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.Uint8}, enT, idT}}
	for _, c := range []struct {
		name string
		n    *schema.Node
		lex  string
		msg  string // "" = fits
	}{
		{"uint8 garbage", leaf("n", &schema.Type{Base: schema.Uint8}), "abc", `Invalid type uint8 value "abc".`},
		{"uint8 number", leaf("n", &schema.Type{Base: schema.Uint8}), "42", ""},
		{"enum name", leaf("e", enT), "red", ""},
		{"enum unknown", leaf("e", enT), "blue", `Invalid enumeration value "blue".`},
		{"union member", leaf("u", u), "red", ""},
		// the prefix resolves through the must's imports to an import-only module: libyang's own message
		{"union import-only identity", leaf("u", u), "imp:der", "Invalid union value \"imp:der\" - no matching subtype found:\n" +
			"    ly2 integers: Invalid type uint8 value \"imp:der\".\n" +
			"    ly2 enumeration: Invalid enumeration value \"imp:der\".\n" +
			"    ly2 identityref: Invalid identityref \"imp:der\" value - identity found in non-implemented module \"o\".\n"},
		{"union unknown prefix", leaf("u", u), "zzz:der", "Invalid union value \"zzz:der\" - no matching subtype found:\n" +
			"    ly2 integers: Invalid type uint8 value \"zzz:der\".\n" +
			"    ly2 enumeration: Invalid enumeration value \"zzz:der\".\n" +
			"    ly2 identityref: Invalid identityref \"zzz:der\" value - unable to map prefix to YANG schema.\n"},
		{"leafref needs the tree", leaf("r", &schema.Type{Base: schema.Leafref, RequireInstance: true,
			Realtype: &schema.Type{Base: schema.Uint8}}), "7", ""},
	} {
		msg, ok := CheckValue(c.n, c.lex, ns)
		if ok != (c.msg == "") || msg != c.msg {
			t.Errorf("%s: %q ok=%v, want %q", c.name, msg, ok, c.msg)
		}
	}
}
