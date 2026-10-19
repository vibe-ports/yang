// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"errors"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

func TestUnionPrefixScanBeforeMembers(t *testing.T) {
	str := &schema.Type{Base: schema.String}
	union := &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.Uint8}, str}}
	if _, d := Store(str, "\ufffe", FormatXML, HintData, nil, nil); d != nil {
		t.Fatal(d)
	}
	if _, d := Store(union, "\ufffe", FormatXML, HintData, nil, nil); d == nil || !d.NoItem || d.RC() != "LY_EINVAL" {
		t.Fatalf("union scan: %+v", d)
	}
	// The union owns its prefix snapshot, including the default namespace.
	m := &schema.Module{Name: "m", Namespace: "urn:m", Implemented: true}
	id := &schema.Identity{Name: "id", Module: m}
	m.Identities = []*schema.Identity{id}
	base := &schema.Identity{Name: "base", Module: m, Derived: []*schema.Identity{id}}
	ty := &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.IdentityRef, Bases: []*schema.Identity{base}}}}
	ns := XMLNamespaces{Set: &schema.Set{Modules: []*schema.Module{m}}, NS: map[string]string{"": "urn:m"}}
	v, d := Store(ty, "id", FormatXML, HintData, ns, nil)
	if d != nil {
		t.Fatal(d)
	}
	ns.NS[""] = "changed"
	if v.Union().pc.Resolve("") != m {
		t.Fatal("union did not retain default namespace")
	}
}

func TestPrintXPathScannerError(t *testing.T) {
	xp := &schema.Type{Base: schema.String, Typedef: "xpath1.0", TypedefModule: &schema.Module{Name: "ietf-yang-types"}}
	for _, ty := range []*schema.Type{xp, {Base: schema.Union, Union: []*schema.Type{{Base: schema.Uint8}, xp}}} {
		v, d := Store(ty, "'a\x01b'", FormatJSON, HintData, nil, nil)
		if d != nil {
			t.Fatal(d)
		}
		got, err := Print(v, FormatXML, nil)
		var diag *Diag
		if got != "" || !errors.As(err, &diag) || !diag.NoItem || diag.RC() != "LY_EINVAL" || diag.Msg != "" {
			t.Fatalf("print: %q, %+v", got, err)
		}
	}
}
