// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// TestNewOpaque: Tree.NewOpaque is lyd_new_opaq (FormatJSON, the zero value) and lyd_new_opaq2
// (FormatXML), libyang v5.8.6 src/tree_data_new.c:966 and :997. The name is taken literally, the
// module or namespace need not exist, the JSON prefix must be the module name, "[null]" is the
// empty hint for JSON only, a parentless node becomes a top-level node of t and a child is
// appended last.
func TestNewOpaque(t *testing.T) {
	set, _ := newTestSet(t)
	tr := newTree(set)
	pfx := "nomod"
	top, err := tr.NewOpaque(nil, "x:y", "[null]", OpaqueOptions{Prefix: &pfx, Module: "nomod", Namespace: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	pfx = "changed" // the prefix was copied
	if top.Name() != "x:y" || top.Parent() != nil || tr.top.first() != top {
		t.Fatalf("top = %q parent %v first %v", top.Name(), top.Parent(), tr.top.first())
	}
	info, ok := top.OpaqueInfo()
	if !ok || info.Prefix != "nomod" || info.Module != "nomod" || info.Namespace != "" || info.Format != FormatJSON ||
		!slices.Equal(info.Hints, []string{"empty"}) {
		t.Fatalf("json info = %+v %v", info, ok)
	}
	first, err := tr.NewOpaque(top, "c1", "v", OpaqueOptions{Module: "m"})
	if err != nil {
		t.Fatal(err)
	}
	xp := "p"
	second, err := tr.NewOpaque(top, "c2", "[null]", OpaqueOptions{Format: FormatXML, Prefix: &xp, Module: "ignored", Namespace: "urn:x"})
	if err != nil {
		t.Fatal(err)
	}
	if kids := slices.Collect(top.Children()); len(kids) != 2 || kids[0] != first || kids[1] != second {
		t.Fatalf("children = %v", kids)
	}
	info, _ = second.OpaqueInfo()
	if info.Format != FormatXML || info.Namespace != "urn:x" || info.Module != "" || info.Prefix != "p" ||
		len(info.Hints) != 0 || second.Value() != "[null]" {
		t.Fatalf("xml info = %+v value %q", info, second.Value())
	}
	var b strings.Builder
	if err := tr.PrintJSON(&b, PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	if b.Len() == 0 {
		t.Fatal("empty JSON print")
	}
}

// TestNewOpaqueRefusals: the argument errors of lyd_new_opaq/_opaq2 and the port's safe refusals
// keep their texts through the public call: the JSON prefix check (an explicit "" included), an
// unsupported format, a parent of another tree, a terminal parent (D-0113) and a NUL (D-0111).
// No refusal changes the tree.
func TestNewOpaqueRefusals(t *testing.T) {
	set, _ := newTestSet(t)
	tr := newTree(set)
	leaf, err := tr.NewTerm(nil, "a:foo", "5", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	other := newTree(set)
	foreign, err := other.NewOpaque(nil, "f", "", OpaqueOptions{Module: "m"})
	if err != nil {
		t.Fatal(err)
	}
	empty, wrong := "", "b"
	for _, c := range []struct {
		name   string
		parent *Node
		value  string
		o      OpaqueOptions
		want   string
	}{
		{"prefix mismatch", nil, "v", OpaqueOptions{Prefix: &wrong, Module: "a"},
			"LY_EINVAL Invalid argument !prefix || !strcmp(prefix, module_name) (lyd_new_opaq())."},
		{"empty prefix", nil, "v", OpaqueOptions{Prefix: &empty, Module: "a"},
			"LY_EINVAL Invalid argument !prefix || !strcmp(prefix, module_name) (lyd_new_opaq())."},
		{"format", nil, "v", OpaqueOptions{Format: Format(9), Module: "a"},
			"LY_EINVAL Invalid argument format (lyd_new_opaq())."},
		{"foreign parent", foreign, "v", OpaqueOptions{Module: "a"},
			"LY_EINVAL Invalid argument parent (not in the tree) (lyd_new_opaq())."},
		{"foreign parent xml", foreign, "v", OpaqueOptions{Format: FormatXML, Namespace: "urn:a"},
			"LY_EINVAL Invalid argument parent (not in the tree) (lyd_new_opaq2())."},
		{"leaf parent", leaf, "v", OpaqueOptions{Module: "a"},
			"LY_EINVAL Invalid argument parent (a leaf or leaf-list has no children) (lyd_new_opaq())."},
		{"leaf parent xml", leaf, "v", OpaqueOptions{Format: FormatXML, Namespace: "urn:a"},
			"LY_EINVAL Invalid argument parent (a leaf or leaf-list has no children) (lyd_new_opaq2())."},
		{"NUL", nil, "x\x00y", OpaqueOptions{Format: FormatXML, Namespace: "urn:a"},
			"LY_EVALID Invalid character 0x0."},
	} {
		n, err := tr.NewOpaque(c.parent, "x", c.value, c.o)
		var ve *ValidationError
		if n != nil || !errors.As(err, &ve) || len(ve.Diags) != 1 {
			t.Errorf("%s: %v %v", c.name, n, err)
			continue
		}
		if got := ve.RC() + " " + ve.Diags[0].Msg; got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
	if tr.top.len() != 1 || tr.top.first() != leaf || other.top.len() != 1 {
		t.Fatalf("refusals changed a tree: %d %d", tr.top.len(), other.top.len())
	}
}
