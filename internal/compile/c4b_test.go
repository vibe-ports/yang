// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"errors"
	"reflect"
	"testing"

	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// TestUnresMod: P5 logs every unapplied augment (all of them, path restarted at the augmenting
// module) and fails with LY_ENOTFOUND (lys_compile_unres_mod).
func TestUnresMod(t *testing.T) {
	cur, aug := &schema.Module{Name: "t"}, &schema.Module{Name: "a"}
	c := &Context{}
	w := &nodeCtx{c: c, cur: cur}
	w.path.init(cur)
	pm := &pmod{Parsed: &parser.Module{Node: parser.Node{Name: "a-sub"}}, mod: aug}
	err := w.unresMod([]pendingAug{{nodeid: "/t:x", pm: pm}, {nodeid: "/t:y", pm: pm, ext: "e:ext"}})
	if !errors.Is(err, eNotFound) {
		t.Fatalf("err %v", err)
	}
	var got [][2]string
	for _, d := range c.diags {
		got = append(got, [2]string{d.SchemaPath, d.Msg})
	}
	want := [][2]string{
		{"/a:{augment='/t:x'}", "Augment target node \"/t:x\" from module \"a-sub\" was not found."},
		{"/a:{ext-inst='e:ext'}/{augment='/t:y'}", "Augment ext-inst target node \"/t:y\" from module \"a-sub\" was not found."},
	}
	if !reflect.DeepEqual(got, want) || w.path.String() != "/" {
		t.Fatalf("got %q, path %q", got, w.path.String())
	}
}

// TestCheckDisabledUnique: a disabled unique leaf leaves its unique statement; a unique left
// empty is removed (lys_compile_unres_check_disabled).
func TestCheckDisabledUnique(t *testing.T) {
	m := &schema.Module{Name: "m"}
	l := &schema.Node{Kind: schema.List, Name: "l", Module: m}
	a := &schema.Node{Kind: schema.Leaf, Name: "a", Module: m, Parent: l}
	b := &schema.Node{Kind: schema.Leaf, Name: "b", Module: m, Parent: l}
	l.Children = []*schema.Node{a, b}
	l.Uniques = [][]*schema.Node{{a}, {a, b}}
	m.Top = []*schema.Node{l}
	c := &Context{disabled: []*schema.Node{a}}
	if err := c.removeDisabled(); err != nil {
		t.Fatal(err)
	}
	// only the first unique naming a is fixed, as libyang breaks after it
	if !reflect.DeepEqual(l.Uniques, [][]*schema.Node{{a, b}}) || !reflect.DeepEqual(l.Children, []*schema.Node{b}) {
		t.Fatalf("uniques %v children %v", l.Uniques, l.Children)
	}
}

// TestIffTokens: the operands of an expression the second pass of lys_compile_iffeature tokenizes
// (used for "processing error" expressions, D-0036).
func TestIffTokens(t *testing.T) {
	if got := iffTokens("not a and (b or c)"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("got %q", got)
	}
}
