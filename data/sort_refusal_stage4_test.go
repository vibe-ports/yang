// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/types"
)

func assertStage4Refusal(t *testing.T, err error, node, operation string) {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) == 0 {
		t.Fatalf("refusal = %T %v, want logged LY_EINVAL", err, err)
	}
	for _, text := range []string{"D-0121", node, operation} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("refusal %q does not name %q", err, text)
		}
	}
}

// These pairs used to be accepted because C union bytes could be reinterpreted deterministically.
// Each direction must now fail at the direct comparison, before linking either value.
func TestStage4ClosedSortProfiles(t *testing.T) {
	cases := []struct{ name, a, b, av, bv string }{
		{"signed-width", "int8;", "int16;", "-1", "2"},
		{"unsigned-width", "uint8;", "uint16;", "1", "2"},
		{"signed-unsigned", "int8;", "uint8;", "-1", "2"},
		{"scalar-string", "int8;", "string;", "-1", "2"},
		{"canonical-cross-base", "string;", "binary;", "a", "AQ=="},
		{"decimal-scale", "decimal64 { fraction-digits 1; }", "decimal64 { fraction-digits 2; }", "1.0", "2.0"},
		{"bits-width", "bits { bit a { position 0; } }", "bits { bit a { position 8; } }", "a", "a"},
		{"union", "union { type int8; type string; }", "union { type int8; type string; }", "-1", "2"},
		{"plugin-store", "inet:ipv4-address;", "string;", "192.0.2.1", "192.0.2.2"},
		{"plugin-representation", "yang:date;", "yang:date-no-zone;", "2024-01-01Z", "2024-01-02"},
	}
	for _, tc := range cases {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%t", tc.name, reverse), func(t *testing.T) {
				module := func(typ string) string {
					return `module refusal-profile { yang-version 1.1; namespace "urn:refusal-profile"; prefix r;
 import ietf-inet-types { prefix inet; } import ietf-yang-types { prefix yang; }
 container c { leaf-list v { type ` + typ + ` } } }`
				}
				wrap := func(v string) string { return `<c xmlns="urn:refusal-profile"><v>` + v + `</v></c>` }
				first := parseOnlyXML(t, reviewDiffSet(t, module(tc.a)), wrap(tc.av))
				second := parseOnlyXML(t, reviewDiffSet(t, module(tc.b)), wrap(tc.bv))
				if reverse {
					first, second = second, first
				}
				d, err := Diff(first, second, DiffOptions{})
				if d != nil {
					t.Fatal("refused Diff returned a result")
				}
				assertStage4Refusal(t, err, "v", "lyd_insert_node")
			})
		}
	}
}

func TestStage4OrderedKeyProfiles(t *testing.T) {
	for _, keys := range []string{"a b", "b a"} {
		for _, typ := range []string{"uint8", "uint16"} {
			module := func(order, bt string) string {
				return `module refusal-keys { yang-version 1.1; namespace "urn:refusal-keys"; prefix r;
 container c { list l { key "` + order + `"; leaf a { type uint8; } leaf b { type ` + bt + `; } } } }`
			}
			a := parseOnlyXML(t, reviewDiffSet(t, module("a b", "uint8")), `<c xmlns="urn:refusal-keys"><l><a>1</a><b>1</b></l></c>`)
			b := parseOnlyXML(t, reviewDiffSet(t, module(keys, typ)), `<c xmlns="urn:refusal-keys"><l><a>2</a><b>2</b></l></c>`)
			d, err := Diff(a, b, DiffOptions{})
			if keys == "a b" && typ == "uint8" {
				if err != nil || d == nil {
					t.Fatal(d, err)
				}
				continue
			}
			if d != nil {
				t.Fatal("refused Diff returned a result")
			}
			assertStage4Refusal(t, err, "l", "lyd_insert_node")
		}
	}
}

func TestStage4PopulatedUserordClass(t *testing.T) {
	for _, tc := range crossContextUserOrderedCases {
		a, b := crossContextUserOrderedSet(t, tc.module), crossContextUserOrderedSet(t, tc.module)
		for _, pair := range [][2][]string{
			{{"a"}, {"a"}}, {{"a"}, {"b"}}, {{"a", "b"}, {"b"}}, {{"a"}, {"a", "b"}},
		} {
			d, err := Diff(parseOnlyXML(t, a, tc.xml(pair[0])), parseOnlyXML(t, b, tc.xml(pair[1])), DiffOptions{})
			if d != nil {
				t.Fatal("refused Diff returned a result")
			}
			assertStage4Refusal(t, err, tc.element, "lyd_diff")
		}
	}
}

func TestStage4ApplyDiffRefusalKeepsPrefix(t *testing.T) {
	module := func(typ string) string {
		return `module refusal-apply { yang-version 1.1; namespace "urn:refusal-apply"; prefix r;
 leaf a { type string; } leaf-list v { type ` + typ + `; } }`
	}
	dst := parseOnlyXML(t, reviewDiffSet(t, module("uint8")), `<v xmlns="urn:refusal-apply">1</v>`)
	src := parseOnlyXML(t, reviewDiffSet(t, module("uint16")), `<a xmlns="urn:refusal-apply">prefix</a><v xmlns="urn:refusal-apply">2</v>`)
	d, err := Diff(nil, src, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	err = dst.ApplyDiff(d, ApplyDiffOptions{})
	assertStage4Refusal(t, err, "v", "lyd_insert_node")
	if got := names(dst.Top()); !slices.Equal(got, []string{"a=prefix", "v=1"}) {
		t.Fatalf("completed prefix = %v", got)
	}
	checkTreeRetainedRB(t, dst)
}

func TestStage4DetachedMemberRefusal(t *testing.T) {
	for _, operation := range []string{"unlink", "remove", "apply-delete", "set-value", "move"} {
		t.Run(operation, func(t *testing.T) {
			a, b := retainedRBSets(t)
			tr := parseOnlyXML(t, a, retainedRBXML("top", 1, 3, 5))
			foreign := retainedRBTerm(t, retainedRBTop(t, b), "2")
			mustLink(t, tr.insert(nil, foreign, insertDefault))
			before := retainedRBValues(&tr.top)
			var err error
			switch operation {
			case "unlink":
				err = foreign.Unlink()
			case "remove":
				err = foreign.Remove()
			case "move":
				err = newTree(b).Insert(nil, foreign)
			case "set-value":
				// A local member after the foreign separator also selects a tree other than its owner.
				var n *Node
				for _, c := range tr.top.list {
					if c.Value() == "3" {
						n = c
					}
				}
				_, err = n.SetValue("4")
			case "apply-delete":
				source := parseOnlyXML(t, b, retainedRBXML("top", 2))
				var d *Tree
				d, err = Diff(source, nil, DiffOptions{})
				if err != nil {
					t.Fatal(err)
				}
				err = tr.ApplyDiff(d, ApplyDiffOptions{})
			}
			assertStage4Refusal(t, err, "top", "lyd_unlink")
			if !slices.Equal(retainedRBValues(&tr.top), before) || foreign.siblingsOf() != &tr.top {
				t.Fatal("refusal detached a retained member")
			}
			checkTreeRetainedRB(t, tr)
		})
	}
}

func TestStage4NativeSearchMissRefusal(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := parseOnlyXML(t, a, retainedRBXML("top", 1, 2, 3))
	n := tr.top.list[0]
	// Model an in-place parser key update: retained shape and membership stay unchanged.
	n.value = retainedRBTerm(t, n.schema, "9").value
	before := slices.Clone(tr.top.list)
	err := n.Remove()
	assertStage4Refusal(t, err, "top", "lyd_unlink")
	if !slices.Equal(tr.top.list, before) {
		t.Fatal("native search miss changed siblings")
	}
}

func TestStage4SearchBudgetBeforeRemoval(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := parseOnlyXML(t, a, retainedRBXML("top", 1, 2, 3))
	tr.work.hashLimit = 1
	before := slices.Clone(tr.top.list)
	err := tr.top.list[2].Unlink()
	if !errors.Is(err, yang.ErrBudget) || !slices.Equal(before, tr.top.list) {
		t.Fatalf("budget removal = %v", err)
	}
}

// Keep the storage import used here to prove that leafrefs are compared by their stored realtype.
func TestStage4LeafrefSortNeighbour(t *testing.T) {
	module := func(typ string) string {
		return `module refusal-ref { yang-version 1.1; namespace "urn:refusal-ref"; prefix r;
 leaf anchor { type uint8; } leaf-list top { type ` + typ + ` } }`
	}
	a := reviewDiffSet(t, module("uint8;"))
	b := reviewDiffSet(t, module("leafref { path /anchor; require-instance false; }"))
	x := retainedRBTerm(t, retainedRBTop(t, a), "1")
	y := retainedRBTerm(t, retainedRBTop(t, b), "2")
	if order, ok := types.CompareCompatible(x.value, y.value); !ok || order >= 0 {
		t.Fatalf("resolved leafref sort = %d %t", order, ok)
	}
}

func TestStage4NativeEqualSearch(t *testing.T) {
	a, _ := retainedRBSets(t)
	tr := newTree(a)
	sn := retainedRBTop(t, a)
	for range 63 {
		mustLink(t, tr.insert(nil, retainedRBTerm(t, sn, "1"), insertDefault))
	}
	d := tr.top.rbOrders[tr.top.rbGroups[tr.top.list[0]]]
	for _, n := range tr.top.list {
		found, err := retainedFind(&tr.work, d, n)
		if err != nil || !found {
			t.Fatalf("equal search found=%t err=%v", found, err)
		}
	}
	// A low budget must stop even an equal-value walk before any removal.
	tr.work.hashLimit = 2
	err := tr.top.list[len(tr.top.list)-1].Remove()
	if !errors.Is(err, yang.ErrBudget) || len(tr.top.list) != 63 {
		t.Fatalf("equal search budget = %v", err)
	}
}

func TestStage4DualAndBulkRefusals(t *testing.T) {
	const module = `module rb-lead { yang-version 1.1; namespace "urn:rb-lead"; prefix r; container c { leaf-list v { type string; } } }`
	a, b := reviewDiffSet(t, module), reviewDiffSet(t, module)
	build := func() (*Tree, *Node) {
		first := parseOnlyXML(t, a, `<c xmlns="urn:rb-lead"><v>b</v><v>c</v><v>d</v><v>e</v></c>`)
		d, err := Diff(first, parseOnlyXML(t, b, `<c xmlns="urn:rb-lead"><v>a</v></c>`), DiffOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Merge(parseOnlyXML(t, a, `<c xmlns="urn:rb-lead"><v>f</v></c>`)); err != nil {
			t.Fatal(err)
		}
		c := topOf(d)
		for _, n := range c.kids.list {
			if len(c.kids.rbDual[n]) != 0 {
				return d, n
			}
		}
		t.Fatal("no dual member")
		return nil, nil
	}
	for _, bulk := range []bool{false, true} {
		tr, n := build()
		sib := n.siblingsOf()
		before := slices.Clone(sib.list)
		var err error
		if bulk {
			err = (&logger{}).done(tr.unlinkAll([]*Node{n}))
		} else {
			err = n.Remove()
		}
		assertStage4Refusal(t, err, "v", "lyd_unlink")
		if !slices.Equal(sib.list, before) || len(sib.rbDual[n]) == 0 {
			t.Fatal("refusal changed dual membership")
		}
		checkTreeRetainedRB(t, tr)
	}
	tr, _ := build()
	c := topOf(tr)
	if err := tr.unlinkAll(slices.Clone(c.kids.list)); err != nil {
		t.Fatalf("complete forest release: %v", err)
	}
	if len(c.kids.rbOrders) != 0 || len(c.kids.rbDual) != 0 {
		t.Fatal("complete release retained topology")
	}
}

func TestStage4PluginImplementationIdentity(t *testing.T) {
	module := func(revision string) string {
		return `module refusal-plugin { yang-version 1.1; namespace "urn:refusal-plugin"; prefix r;
 import ietf-yang-types { prefix yang; revision-date ` + revision + `; } container c { leaf-list v { type yang:date-and-time; } } }`
	}
	load := func(revision string) *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{
			"refusal-plugin.yang":                   {Data: []byte(module(revision))},
			"ietf-yang-types@" + revision + ".yang": {Data: []byte(`module ietf-yang-types { yang-version 1.1; namespace "urn:ietf:params:xml:ns:yang:ietf-yang-types"; prefix yang; revision ` + revision + `; typedef date-and-time { type string; } }`)},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, diags, err := c.Load("refusal-plugin", "", nil); err != nil {
			t.Fatal(err, diags)
		}
		return c.Snapshot()
	}
	a, b := load("2013-07-15"), load("2025-12-22")
	first := parseOnlyXML(t, a, `<c xmlns="urn:refusal-plugin"><v>2024-01-01T00:00:00Z</v></c>`)
	second := parseOnlyXML(t, b, `<c xmlns="urn:refusal-plugin"><v>2024-01-02T00:00:00Z</v></c>`)
	for _, pair := range [][2]*Tree{{first, second}, {second, first}} {
		d, err := Diff(pair[0], pair[1], DiffOptions{})
		if d != nil {
			t.Fatal("refused Diff returned a result")
		}
		assertStage4Refusal(t, err, "v", "lyd_insert_node")
	}
}

func TestStage4KeyProfileBudgetAndLifetime(t *testing.T) {
	name := strings.Repeat("k", 1<<16)
	a, b := &schema.Node{Keys: []*schema.Node{{Name: name}}}, &schema.Node{Keys: []*schema.Node{{Name: name}}}
	var w workCounter
	w.hashLimit = 1
	end := w.beginHashWork()
	if sortKeyProfile(&w, a, b) || !errors.Is(w.hashErr, yang.ErrBudget) || len(w.sortKeys) != 0 {
		t.Fatal("profile constructed before its enforced charge")
	}
	end()
	w.hashLimit = 0
	end = w.beginHashWork()
	if !sortKeyProfile(&w, a, b) {
		t.Fatal(w.hashErr)
	}
	used := w.hashUsed
	if !sortKeyProfile(&w, a, b) || w.hashUsed != used {
		t.Fatal("cached schema profile was rescanned")
	}
	end()
	if w.sortKeys != nil {
		t.Fatal("operation cache retained compiled schemas")
	}
}

func TestStage4KeyProfileDoesNotInvokeUnusedCallback(t *testing.T) {
	load := func() *schema.Set {
		const module = `module refusal-unused-key { yang-version 1.1; namespace "urn:refusal-unused-key"; prefix r;
 import libnetconf2-netconf-server { prefix ln2; }
 container c { list l { key "a b"; leaf a { type uint8; } leaf b { type ln2:time-period; } } } }`
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{
			"refusal-unused-key.yang": {Data: []byte(module)}, "libnetconf2-netconf-server.yang": {Data: []byte(timePeriodTypedefModule)},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, diags, err := c.Load("refusal-unused-key", "", nil); err != nil {
			t.Fatal(err, diags)
		}
		return c.Snapshot()
	}
	a, b := load(), load()
	first := parseOnlyXML(t, a, `<c xmlns="urn:refusal-unused-key"><l><a>1</a><b/></l></c>`)
	second := parseOnlyXML(t, b, `<c xmlns="urn:refusal-unused-key"><l><a>2</a><b/></l></c>`)
	for _, pair := range [][2]*Tree{{first, second}, {second, first}} {
		if d, err := Diff(pair[0], pair[1], DiffOptions{}); err != nil || d == nil {
			t.Fatalf("unused key callback was invoked: %v", err)
		}
	}
}
