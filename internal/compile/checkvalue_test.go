// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

type testNS map[string]string

func (n testNS) Resolve(p string) (string, bool) { m, ok := n[p]; return m, ok && p != "" }
func (n testNS) Prefix(m string) string          { return m }
func (n testNS) Default() string                 { return n[""] }

// TestCheckValue: the store of warn_equality_value over hand-built leaves.
func TestCheckValue(t *testing.T) {
	set := &schema.Set{}
	m := &schema.Module{Name: "m", Namespace: "urn:m", Prefix: "m", Implemented: true}
	o := &schema.Module{Name: "o", Namespace: "urn:o", Prefix: "o", Implemented: true}
	set.Modules = append(set.Modules, m, o)
	base := &schema.Identity{Name: "base", Module: o}
	der := &schema.Identity{Name: "der", Module: o}
	base.Derived = []*schema.Identity{der}
	o.Identities = []*schema.Identity{base, der}
	idT := &schema.Type{Base: schema.IdentityRef, Bases: []*schema.Identity{base}}
	enT := &schema.Type{Base: schema.Enumeration, Enums: []*schema.Enum{{Name: "red"}}}
	leaf := func(name string, t *schema.Type) *schema.Node {
		return &schema.Node{Kind: schema.Leaf, Name: name, Module: m, Type: t, Config: true}
	}
	ns := testNS{"": "m", "o": "o"}
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
		{"prefixed identity", leaf("i", idT), "o:der", ""},
		{"union member", leaf("u", &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.Uint8}, enT}}), "red", ""},
		{"leafref needs the tree", leaf("r", &schema.Type{Base: schema.Leafref, RequireInstance: true,
			Realtype: &schema.Type{Base: schema.Uint8}}), "7", ""},
	} {
		msg, ok := CheckValue(set, c.n, c.lex, ns)
		if ok != (c.msg == "") || msg != c.msg {
			t.Errorf("%s: %q ok=%v, want %q", c.name, msg, ok, c.msg)
		}
	}
}
