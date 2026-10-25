// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"weak"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

const timePeriodTypedefModule = `module libnetconf2-netconf-server { yang-version 1.1;
  namespace "urn:cesnet:libnetconf2-netconf-server"; prefix ln2;
  typedef time-period { type string; } }`

func timePeriodSets(t *testing.T) (str, period *schema.Set) {
	t.Helper()
	load := func(files fstest.MapFS) *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, files)
		if err != nil {
			t.Fatal(err)
		}
		if _, diags, err := c.Load("rb-time-period", "", nil); err != nil {
			t.Fatal(err, diags)
		}
		return c.Snapshot()
	}
	str = load(fstest.MapFS{"rb-time-period.yang": {Data: []byte(`module rb-time-period { yang-version 1.1;
  namespace "urn:rb-time-period"; prefix r; container c { leaf-list v { type string; } } }`)}})
	period = load(fstest.MapFS{
		"libnetconf2-netconf-server.yang": {Data: []byte(timePeriodTypedefModule)},
		"rb-time-period.yang": {Data: []byte(`module rb-time-period { yang-version 1.1;
  namespace "urn:rb-time-period"; prefix r; import libnetconf2-netconf-server { prefix ln2; }
  container c { leaf-list v { type ln2:time-period; } } }`)},
	})
	return str, period
}

// TestDiffStringTimePeriodCompatible: time-period is stored by lyplg_type_store_string and its
// sort callback reads only canonical text (time_period.c:41-75), so a cross-context comparison
// with a string value is defined in both directions (no D-0121).
func TestDiffStringTimePeriodCompatible(t *testing.T) {
	str, period := timePeriodSets(t)
	for _, sets := range [][2]*schema.Set{{str, period}, {period, str}} {
		first := parseOnlyXML(t, sets[0], `<c xmlns="urn:rb-time-period"><v>1h</v><v>3h</v></c>`)
		second := parseOnlyXML(t, sets[1], `<c xmlns="urn:rb-time-period"><v>2h</v><v>4h</v></c>`)
		diff, err := Diff(first, second, DiffOptions{})
		if err != nil || diff == nil {
			t.Fatalf("string/time-period Diff = (%v, %v), want a diff", diff, err)
		}
		checkTreeRetainedRB(t, diff)
	}
}

// TestMovedSubtreeReleasesSourceTree: a retained RB topology follows its subtree to the new tree,
// so the moved container no longer keeps the source tree and its other roots alive.
func TestMovedSubtreeReleasesSourceTree(t *testing.T) {
	set := reviewDiffSet(t, `module rb-release { yang-version 1.1; namespace "urn:rb-release"; prefix r;
  leaf a { type string; } container c { leaf-list v { type uint32; } } }`)
	dst := newTree(set)
	var moved *Node
	ref := func() weak.Pointer[Tree] {
		src := parseOnlyXML(t, set, `<a xmlns="urn:rb-release">x</a><c xmlns="urn:rb-release"><v>3</v><v>1</v><v>2</v></c>`)
		moved = src.top.list[1]
		if moved.schema.Name != "c" || len(moved.kids.rbOrders) == 0 {
			t.Fatal("the source container has no retained RB tree")
		}
		if err := dst.Insert(nil, moved); err != nil {
			t.Fatal(err)
		}
		return weak.Make(src)
	}()
	for _, d := range moved.kids.rbOrders {
		if d.work != &dst.work {
			t.Fatal("the moved retained topology still charges the source tree")
		}
	}
	runtime.GC()
	runtime.GC()
	if ref.Value() != nil {
		t.Fatal("the moved container keeps the source tree alive")
	}
	runtime.KeepAlive(moved)
	checkTreeRetainedRB(t, dst)
}

// TestDiffDisjointForeignAnchorWorkCharged: N disjoint leaves under one container in each
// context. Every create probes the remaining schema siblings for an anchor among the output
// siblings being built; that output set has no input lineage index, so each probe indexes it once
// and every step is charged to the enforced quota: quadratic in N, not cubic, and bounded.
func TestDiffDisjointForeignAnchorWorkCharged(t *testing.T) {
	build := func(prefix string, n int) (string, string) {
		var module, data strings.Builder
		module.WriteString(`module review-disjoint { yang-version 1.1; namespace "urn:review-disjoint"; prefix r; container t {`)
		data.WriteString(`<t xmlns="urn:review-disjoint">`)
		for i := range n {
			fmt.Fprintf(&module, " leaf %s%d { type string; }", prefix, i)
			fmt.Fprintf(&data, "<%s%d>x</%s%d>", prefix, i, prefix, i)
		}
		module.WriteString(" } }")
		data.WriteString("</t>")
		return module.String(), data.String()
	}
	measure := func(n int, hashLimit uint64) (uint64, error) {
		ma, da := build("a", n)
		mb, db := build("b", n)
		first := parseOnlyXML(t, reviewDiffSet(t, ma), da)
		second := parseOnlyXML(t, reviewDiffSet(t, mb), db)
		diff, err := diffNodesLookupLimit(topOf(first), topOf(second), DiffOptions{}, false, first, second, 0, hashLimit)
		if diff == nil {
			return 0, err
		}
		return diff.work.hashUsed, err
	}
	var previous uint64
	for _, n := range []int{32, 64, 128} {
		used, err := measure(n, 0)
		if err != nil {
			t.Fatalf("%d disjoint leaves: %v", n, err)
		}
		if previous != 0 && used > 5*previous {
			t.Fatalf("doubling disjoint leaves grew charged work from %d to %d (cubic)", previous, used)
		}
		previous = used
	}
	if _, err := measure(64, previous/16); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("limited disjoint diff error = %v, want ErrBudget", err)
	}
}

// TestSetValueForeignReinsertionRefusalReported: SetValue's unlink/change/reinsert can reach a
// foreign enumeration comparator, which dereferences the string value's enum_item (D-0121). The
// refusal must be the result, with the old value and the retained topology restored.
func TestSetValueForeignReinsertionRefusalReported(t *testing.T) {
	module := func(typ string) string {
		return `module rb-setvalue { yang-version 1.1; namespace "urn:rb-setvalue"; prefix r;
  leaf-list v { type ` + typ + ` } }`
	}
	strSet, enumSet := reviewDiffSet(t, module("string;")), reviewDiffSet(t, module("enumeration { enum b; }"))
	tr := parseOnlyXML(t, strSet, `<v xmlns="urn:rb-setvalue">a</v><v xmlns="urn:rb-setvalue">c</v><v xmlns="urn:rb-setvalue">d</v>`)
	// String first values sort canonically, so linking the foreign b (as a's right child) is
	// defined. c has the same-schema neighbour d, so lyd_change_node_value unlinks and reinserts
	// it; the new value aa descends d, a and then b, whose enumeration callback reads aa's
	// string storage as an enum_item pointer.
	foreign := retainedRBTerm(t, retainedRBTopNamed(t, enumSet, "v"), "b")
	mustLink(t, tr.insert(nil, foreign, insertDefault))
	var c *Node
	for _, n := range tr.top.list {
		if n.Value() == "c" {
			c = n
		}
	}
	before := retainedRBValues(&tr.top)
	changed, err := c.SetValue("aa")
	if err == nil || changed || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("SetValue = (%t, %v), want the D-0121 refusal", changed, err)
	}
	if c.Value() != "c" || !slices.Equal(retainedRBValues(&tr.top), before) {
		t.Fatalf("refused SetValue changed the run: %v", retainedRBValues(&tr.top))
	}
	checkTreeRetainedRB(t, tr)
}

// TestDiffKeylessStateToKeyedConfigOrder: rb_compare_lists compares the first children of a
// keyless retained list unconditionally (tree_data_sorted.c:244-253): the retained keyless
// instances 2, 1 are ordered by their first child when the tree is built, and the created k=3
// sorts after them ([1 2 3], as libyang; conformance/TestRBCompareListsOracle).
func TestDiffKeylessStateToKeyedConfigOrder(t *testing.T) {
	first := parseOnlyXML(t, reviewDiffSet(t, `module rb-keyless { yang-version 1.1; namespace "urn:rb-keyless";
  prefix r; container c { list l { config false; leaf k { type uint8; } } } }`),
		`<c xmlns="urn:rb-keyless"><l><k>2</k></l><l><k>1</k></l></c>`)
	second := parseOnlyXML(t, reviewDiffSet(t, `module rb-keyless { yang-version 1.1; namespace "urn:rb-keyless";
  prefix r; container c { list l { key k; leaf k { type uint8; } } } }`),
		`<c xmlns="urn:rb-keyless"><l><k>3</k></l></c>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range topOf(diff).kids.list {
		got = append(got, l.kids.list[0].Value())
	}
	if !slices.Equal(got, []string{"1", "2", "3"}) {
		t.Fatalf("diff list order = %v, want [1 2 3]", got)
	}
}

func retainedRBTopNamed(t *testing.T, set *schema.Set, name string) *schema.Node {
	t.Helper()
	for _, module := range set.Modules {
		for _, sn := range module.Top {
			if sn.Name == name {
				return sn
			}
		}
	}
	t.Fatalf("%s not compiled", name)
	return nil
}

// TestRBCompareListsShapes drives rbCompareLists (rb_compare_lists) over list shapes: keyed and
// keyless, a first child that is a term, an inner node, an opaque node or absent, trailing
// opaque children, several keys with equal prefixes, and one or two contexts. A refusal stands
// for libyang reading storage it cannot interpret; the defined shapes have live-oracle
// counterparts in conformance/TestRBCompareListsOracle.
func TestRBCompareListsShapes(t *testing.T) {
	const (
		keyed    = `list l { key k; leaf k { type string; } leaf v { type string; } }`
		twoKeys  = `list l { key "a b"; leaf a { type string; } leaf b { type uint8; } }`
		keyless  = `list l { config false; leaf k { type uint8; } }`
		inner    = `list l { config false; container x { leaf y { type string; } } leaf k { type uint8; } }`
		keyedBin = `list l { key k; leaf k { type binary; } }`
	)
	set := func(list string) *schema.Set {
		return reviewDiffSet(t, `module rb-shapes { yang-version 1.1; namespace "urn:rb-shapes"; prefix r;
  container c { `+list+` } }`)
	}
	sets := map[string]*schema.Set{}
	setOf := func(key string, list string) *schema.Set {
		if sets[key] == nil {
			sets[key] = set(list)
		}
		return sets[key]
	}
	instance := func(s *schema.Set, xml string, opaqueChild bool) *Node {
		tr := parseOnlyXML(t, s, `<c xmlns="urn:rb-shapes">`+xml+`</c>`)
		l := topOf(tr).kids.list[0]
		if opaqueChild {
			mustLink(t, tr.insert(l, newOpaque(opaque{Name: "extra", Value: "x", Format: types.FormatJSON}), insertDefault))
		}
		return l
	}
	cases := []struct {
		name    string
		a, b    func() *Node
		order   int
		defined bool
	}{
		{"keyed", func() *Node { return instance(setOf("keyed", keyed), `<l><k>a</k></l>`, false) },
			func() *Node { return instance(setOf("keyed", keyed), `<l><k>b</k></l>`, false) }, -1, true},
		{"keyed-other-context", func() *Node { return instance(setOf("keyed", keyed), `<l><k>b</k></l>`, false) },
			func() *Node { return instance(setOf("keyed2", keyed), `<l><k>a</k></l>`, false) }, 1, true},
		{"keyed-trailing-opaque", func() *Node { return instance(setOf("keyed", keyed), `<l><k>a</k></l>`, true) },
			func() *Node { return instance(setOf("keyed2", keyed), `<l><k>b</k></l>`, false) }, -1, true},
		{"two-keys-equal-prefix", func() *Node { return instance(setOf("two", twoKeys), `<l><a>x</a><b>2</b></l>`, false) },
			func() *Node { return instance(setOf("two2", twoKeys), `<l><a>x</a><b>1</b></l>`, false) }, 1, true},
		{"two-keys-equal", func() *Node { return instance(setOf("two", twoKeys), `<l><a>x</a><b>1</b></l>`, false) },
			func() *Node { return instance(setOf("two", twoKeys), `<l><a>x</a><b>1</b></l>`, false) }, 0, true},
		{"keyless-first-term", func() *Node { return instance(setOf("keyless", keyless), `<l><k>2</k></l>`, false) },
			func() *Node { return instance(setOf("keyless", keyless), `<l><k>1</k></l>`, false) }, 1, true},
		{"keyless-to-keyed-other-context", func() *Node { return instance(setOf("keyless", keyless), `<l><k>2</k></l>`, false) },
			func() *Node {
				return instance(setOf("keyed-uint", `list l { key k; leaf k { type uint8; } }`), `<l><k>3</k></l>`, false)
			}, -1, true},
		{"keyed-string-before-binary", func() *Node { return instance(setOf("keyed", keyed), `<l><k>AQ==</k></l>`, false) },
			func() *Node { return instance(setOf("bin", keyedBin), `<l><k>Ag==</k></l>`, false) }, -1, true},
		{"keyed-binary-before-string", func() *Node { return instance(setOf("bin", keyedBin), `<l><k>Ag==</k></l>`, false) },
			func() *Node { return instance(setOf("keyed", keyed), `<l><k>AQ==</k></l>`, false) }, 1, true}, // zeroed {NULL, 0}
		{"keyed-enumeration-before-string", func() *Node {
			return instance(setOf("enum", `list l { key k; leaf k { type enumeration { enum a; } } }`), `<l><k>a</k></l>`, false)
		}, func() *Node { return instance(setOf("keyed", keyed), `<l><k>a</k></l>`, false) }, 0, false},
		{"first-child-inner", func() *Node { return instance(setOf("inner", inner), `<l><x><y>z</y></x><k>1</k></l>`, false) },
			func() *Node { return instance(setOf("inner", inner), `<l><k>2</k></l>`, false) }, 0, false},
		{"first-child-opaque", func() *Node { return instance(setOf("keyless", keyless), `<l/>`, true) },
			func() *Node { return instance(setOf("keyless", keyless), `<l><k>2</k></l>`, false) }, 0, false},
		{"first-child-absent", func() *Node { return instance(setOf("keyless", keyless), `<l/>`, false) },
			func() *Node { return instance(setOf("keyless", keyless), `<l><k>2</k></l>`, false) }, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			order, defined := rbCompareLists(nil, tc.a(), tc.b())
			if defined != tc.defined || defined && (order < 0) != (tc.order < 0) || defined && (order == 0) != (tc.order == 0) {
				t.Fatalf("rbCompareLists = (%d, %t), want (sign %d, %t)", order, defined, tc.order, tc.defined)
			}
		})
	}
}

// TestForeignAnchorAfterLookupExhaustion: an anchor probe outside a diff charges its lineage keys
// to the mutation's own quota. An exhausted diff lookup quota left on the tree must not make it
// miss the following-schema anchor and append the node after it.
func TestForeignAnchorAfterLookupExhaustion(t *testing.T) {
	const a = `module rb-anchor { yang-version 1.1; namespace "urn:rb-anchor"; prefix r;
  container c { leaf p { type string; } leaf q { type string; } leaf r { type string; } leaf s { type string; } } }`
	const b = `module rb-anchor { yang-version 1.1; namespace "urn:rb-anchor"; prefix r;
  container c { leaf p { type string; } leaf q { type string; } leaf r { type string; } leaf s { type string; }
    leaf z { type string; } } }`
	setA, setB := reviewDiffSet(t, a), reviewDiffSet(t, b)
	tr := parseOnlyXML(t, setA, `<c xmlns="urn:rb-anchor"><p>1</p><q>2</q><s>4</s></c>`)
	c := topOf(tr)
	// mixed contexts with a local first child: the cross-context anchor probe runs
	mustLink(t, tr.link(c, &c.kids, retainedRBLeaf(t, setB, "z", "9"), len(c.kids.list)))
	if c.kids.ht == nil || ctxOf(c.kids.list[0]) != setA || len(c.kids.contexts) < 2 {
		t.Fatal("setup does not reach the cross-context anchor probe")
	}
	tr.work.lookupUsed, tr.work.lookupErr = ^uint64(0), yang.ErrBudget // a spent diff quota
	end := tr.work.beginHashWork()
	err := tr.insert(c, retainedRBLeaf(t, setA, "r", "3"), insertDefault)
	end()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range c.kids.list {
		got = append(got, n.schema.Name)
	}
	if ir, is := slices.Index(got, "r"), slices.Index(got, "s"); ir < 0 || ir > is {
		t.Fatalf("children = %v: r must precede its anchor s", got)
	}
}

// TestDiffMetaForeignYangModule: lyd_diff_node_metadata_check ignores only the first node's
// context's yang module (diff.c:589). The second context's yang:operation is an ordinary
// annotation there, so identical leaves differ in their metadata.
func TestDiffMetaForeignYangModule(t *testing.T) {
	const module = `module rb-meta { yang-version 1.1; namespace "urn:rb-meta"; prefix r; leaf v { type string; } }`
	const doc = `<v xmlns="urn:rb-meta" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none">x</v>`
	first := parseOnlyXML(t, reviewDiffSet(t, module), `<v xmlns="urn:rb-meta">x</v>`)
	second := parseOnlyXML(t, reviewDiffSet(t, module), doc)
	diff, err := Diff(first, second, DiffOptions{Meta: true})
	if err != nil {
		t.Fatal(err)
	}
	if diff == nil || diff.top.len() == 0 {
		t.Fatal("cross-context yang annotation produced no metadata diff")
	}
	if same, err := Diff(second, parseOnlyXML(t, second.context(), doc), DiffOptions{Meta: true}); err != nil || same != nil && same.top.len() != 0 {
		t.Fatalf("same-context yang annotations must stay ignored: %v %v", same, err)
	}
}

func retainedRBLeaf(t *testing.T, set *schema.Set, name, value string) *Node {
	t.Helper()
	for _, module := range set.Modules {
		for _, top := range module.Top {
			for _, sn := range top.Children {
				if sn.Name == name {
					return retainedRBTerm(t, sn, value)
				}
			}
		}
	}
	t.Fatalf("%s not compiled", name)
	return nil
}

// TestDiffPluginSortZeroStorage: lyplg_type_sort_ipv4_address reads the second value's
// fixed-memory struct (LYD_VALUE_GET); a string value's storage is all zero, so it reads
// 0.0.0.0 without a zone and the comparison is defined (corpus ut-diff/cross-context-ipv4-string).
func TestDiffPluginSortZeroStorage(t *testing.T) {
	module := func(typ string) string {
		return `module rb-zero { yang-version 1.1; namespace "urn:rb-zero"; prefix r;
  import ietf-inet-types { prefix inet; }
  container top { presence "retain"; leaf-list address { type ` + typ + ` } } }`
	}
	const doc = `<top xmlns="urn:rb-zero"><address>1.2.3.4</address></top>`
	first := parseOnlyXML(t, reviewDiffSet(t, module("inet:ipv4-address;")), doc)
	second := parseOnlyXML(t, reviewDiffSet(t, module("string;")), doc)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil || diff == nil {
		t.Fatalf("ipv4/string Diff = (%v, %v), want the native create/delete diff", diff, err)
	}
	checkTreeRetainedRB(t, diff)
	// an enumeration callback dereferences enum_item: that pair stays refused
	firstEnum := parseOnlyXML(t, reviewDiffSet(t, module("enumeration { enum a; enum b; }")), `<top xmlns="urn:rb-zero"><address>a</address></top>`)
	secondStr := parseOnlyXML(t, reviewDiffSet(t, module("string;")), `<top xmlns="urn:rb-zero"><address>b</address></top>`)
	if _, err := Diff(firstEnum, secondStr, DiffOptions{}); err == nil || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("enumeration/string Diff error = %v, want D-0121", err)
	}
}
