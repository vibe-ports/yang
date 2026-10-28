// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
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

func stage4ProfileModule(typ string) string {
	return `module refusal-profile { yang-version 1.1; namespace "urn:refusal-profile"; prefix r;
 import ietf-inet-types { prefix inet; } import ietf-yang-types { prefix yang; }
 container c { leaf-list v { type ` + typ + ` } } }`
}

func stage4ProfileXML(v string) string { return `<c xmlns="urn:refusal-profile"><v>` + v + `</v></c>` }

// TestStage4ClosedSortProfiles: pairs outside D-0121's closed profile and R1-R3, several of them
// deterministic in C (accepted compatibility restrictions), fail at the direct comparison in
// both directions, before linking either value.
func TestStage4ClosedSortProfiles(t *testing.T) {
	cases := []struct{ name, a, b, av, bv string }{
		{"signed-unsigned", "int8;", "uint8;", "-1", "2"},
		{"signed-unsigned-width", "int16;", "uint32;", "1", "2"},
		{"integer-boolean", "int8;", "boolean;", "-1", "true"},
		{"integer-decimal", "int8;", "decimal64 { fraction-digits 1; }", "1", "2.0"},
		{"integer-empty", "int8;", "empty;", "1", ""},
		{"string-enumeration", "string;", "enumeration { enum b; }", "a", "b"},
		{"canonical-cross-base", "string;", "binary;", "a", "AQ=="},
		{"decimal-scale", "decimal64 { fraction-digits 1; }", "decimal64 { fraction-digits 2; }", "1.0", "2.0"},
		{"bits-width", "bits { bit a { position 0; } }", "bits { bit a { position 8; } }", "a", "a"},
		{"union", "union { type int8; type string; }", "union { type int8; type string; }", "-1", "2"},
		{"plugin-representation", "yang:date;", "yang:date-no-zone;", "2024-01-01Z", "2024-01-02"},
		{"integer-string-based-plugin", "int8;", "inet:ipv4-address;", "1", "192.0.2.1"},
		{"integer-hex-string", "uint8;", "yang:hex-string;", "1", "aa"},
		{"string-ipv4-no-zone", "string;", "inet:ipv4-address-no-zone;", "a", "192.0.2.1"},
		{"string-ipv6", "string;", "inet:ipv6-address;", "a", "::1"},
		{"string-date-and-time", "string;", "yang:date-and-time;", "a", "2024-01-01T00:00:00Z"},
		{"ipv4-ipv4-no-zone", "inet:ipv4-address;", "inet:ipv4-address-no-zone;", "192.0.2.1", "192.0.2.2"},
	}
	for _, tc := range cases {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reverse=%t", tc.name, reverse), func(t *testing.T) {
				first := parseOnlyXML(t, reviewDiffSet(t, stage4ProfileModule(tc.a)), stage4ProfileXML(tc.av))
				second := parseOnlyXML(t, reviewDiffSet(t, stage4ProfileModule(tc.b)), stage4ProfileXML(tc.bv))
				if reverse {
					first, second = second, first
				}
				d, err := Diff(first, second, DiffOptions{})
				if d != nil {
					t.Fatal("refused Diff returned a result")
				}
				assertStage4Refusal(t, err, "v", "lyd_insert_node")
				if strings.Contains(err.Error(), "undefined") {
					t.Fatalf("an accepted compatibility restriction is not undefined behavior: %v", err)
				}
			})
		}
	}
}

// TestStage4ABIProfilesAccepted: R1-R3 (D-0121) sort across contexts as leaf-list values and as
// list keys, in both operand directions, retaining a valid native topology.
func TestStage4ABIProfilesAccepted(t *testing.T) {
	cases := []struct{ name, a, b, av, bv string }{
		{"R1-signed", "int8;", "int16;", "-1", "256"},
		{"R1-signed-extremes", "int64;", "int8;", "-9223372036854775808", "127"},
		{"R1-unsigned", "uint8;", "uint32;", "255", "256"},
		{"R1-unsigned-extremes", "uint64;", "uint16;", "18446744073709551615", "65535"},
		{"R2-signed", "int8;", "string;", "-1", "2"},
		{"R2-unsigned", "uint64;", "string;", "0", ""},
		{"R3", "inet:ipv4-address;", "string;", "1.2.3.4", "1.2.3.4"},
		{"R3-zero-zone", "inet:ipv4-address;", "string;", "0.0.0.0%eth0", "a"},
	}
	keyModule := func(typ string) string {
		return `module refusal-profile { yang-version 1.1; namespace "urn:refusal-profile"; prefix r;
 import ietf-inet-types { prefix inet; } container c { list l { key k; leaf k { type ` + typ + ` } } } }`
	}
	keyXML := func(v string) string { return `<c xmlns="urn:refusal-profile"><l><k>` + v + `</k></l></c>` }
	for _, tc := range cases {
		for _, list := range []bool{false, true} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/list=%t/reverse=%t", tc.name, list, reverse), func(t *testing.T) {
					module, doc := stage4ProfileModule, stage4ProfileXML
					if list {
						module, doc = keyModule, keyXML
					}
					first := parseOnlyXML(t, reviewDiffSet(t, module(tc.a)), doc(tc.av))
					second := parseOnlyXML(t, reviewDiffSet(t, module(tc.b)), doc(tc.bv))
					if reverse {
						first, second = second, first
					}
					d, err := Diff(first, second, DiffOptions{})
					if err != nil || d == nil {
						t.Fatalf("R1-R3 Diff = (%v, %v), want the native diff", d, err)
					}
					checkTreeRetainedRB(t, d)
				})
			}
		}
	}
}

// TestStage4ABIHistories replays mixed-context insertion histories whose rotations bring each
// representation's callback to the root, then exercises native search (rbFind), removal and value
// change on every member. A successful R1-R3 insertion does not imply that a later removal or
// value change succeeds: each one either succeeds with a valid topology or is the logged D-0121
// refusal of #318's retained-reference rules, which leaves the graph unchanged.
func TestStage4ABIHistories(t *testing.T) {
	type rep struct {
		typ    string
		values []string
	}
	pairs := []struct {
		name string
		a, b rep
	}{
		{"R1", rep{"int8", []string{"-2", "-1", "0", "1", "2", "127", "-128"}},
			rep{"int16", []string{"-2", "-1", "1", "2", "255", "256", "257", "-256"}}},
		{"R1-unsigned", rep{"uint8", []string{"0", "1", "2", "255"}}, rep{"uint32", []string{"1", "2", "256", "257", "65536"}}},
		{"R2", rep{"int8", []string{"-2", "-1", "0", "1", "2"}}, rep{"string", []string{"", "-1", "0", "1", "a"}}},
		{"R3", rep{"inet:ipv4-address", []string{"0.0.0.0", "0.0.0.0%z", "1.2.3.4", "1.2.3.4%z", "9.9.9.9"}},
			rep{"string", []string{"", "1.2.3.4", "a", "z"}}},
	}
	module := func(typ string) string {
		return `module abi-history { yang-version 1.1; namespace "urn:abi-history"; prefix h;
 import ietf-inet-types { prefix inet; } leaf-list top { type ` + typ + `; } }`
	}
	var refused, succeeded int
	for _, p := range pairs {
		setA, setB := reviewDiffSet(t, module(p.a.typ)), reviewDiffSet(t, module(p.b.typ))
		snA, snB := retainedRBTop(t, setA), retainedRBTop(t, setB)
		rng := rand.New(rand.NewPCG(0x0121, uint64(len(p.name)))) //nolint:gosec // deterministic histories
		for range 24 {
			type step struct {
				foreign bool
				value   string
			}
			var hist []step
			for range 3 + rng.IntN(6) {
				if rng.IntN(2) == 0 {
					hist = append(hist, step{false, p.a.values[rng.IntN(len(p.a.values))]})
				} else {
					hist = append(hist, step{true, p.b.values[rng.IntN(len(p.b.values))]})
				}
			}
			build := func() *Tree {
				tr := newTree(setA)
				for _, s := range hist {
					sn := snA
					if s.foreign {
						sn = snB
					}
					if err := tr.insert(nil, retainedRBTerm(t, sn, s.value), insertDefault); err != nil {
						t.Fatalf("%s %v: R1-R3 insertion refused: %v", p.name, hist, err)
					}
				}
				checkTreeRetainedRB(t, tr)
				return tr
			}
			tr := build()
			for _, d := range tr.top.rbOrders {
				for n := range d.nodes {
					if _, err := tr.work.rbFind(d, n); err != nil && !strings.Contains(err.Error(), "D-0121") {
						t.Fatalf("%s %v: rbFind %s: %v", p.name, hist, n.Value(), err)
					}
				}
			}
			for i := range tr.top.SchemaLen() {
				for _, op := range []string{"unlink", "set-value"} {
					tr := build()
					n := tr.top.At(i)
					if op == "set-value" && ctxOf(n) != setA {
						continue
					}
					before := snapshotHashBudgetTree(tr)
					var err error
					if op == "unlink" {
						err = n.Unlink()
					} else {
						_, err = n.SetValue(p.a.values[(i+1)%len(p.a.values)])
					}
					if err == nil {
						succeeded++
						checkTreeRetainedRB(t, tr)
						continue
					}
					refused++
					var ve *ValidationError
					if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || !strings.Contains(err.Error(), "D-0121") {
						t.Fatalf("%s %v: %s %s = %v, want success or the D-0121 refusal", p.name, hist, op, n.Value(), err)
					}
					if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
						t.Fatalf("%s %v: refused %s changed the graph", p.name, hist, op)
					}
					checkTreeRetainedRB(t, tr)
				}
			}
		}
	}
	if refused == 0 || succeeded == 0 {
		t.Fatalf("histories reached %d refusals and %d successes, want both", refused, succeeded)
	}
}

// TestStage4ABIRetainedReferenceRefusal: an R1 foreign member joins a local run (an accepted
// comparison), but native lyd_unlink selects it as its own leader, so detaching it would leave
// the retained tree referencing it (#318, D-0121 §2.4).
func TestStage4ABIRetainedReferenceRefusal(t *testing.T) {
	module := func(typ string) string {
		return `module abi-ref { yang-version 1.1; namespace "urn:abi-ref"; prefix r; leaf-list top { type ` + typ + `; } }`
	}
	a, b := reviewDiffSet(t, module("int8")), reviewDiffSet(t, module("int16"))
	tr := parseOnlyXML(t, a, `<top xmlns="urn:abi-ref">1</top><top xmlns="urn:abi-ref">3</top><top xmlns="urn:abi-ref">5</top><top xmlns="urn:abi-ref">7</top>`)
	foreign := retainedRBTerm(t, retainedRBTop(t, b), "4")
	mustLink(t, tr.insert(nil, foreign, insertDefault))
	before := snapshotHashBudgetTree(tr)
	assertStage4Refusal(t, foreign.Unlink(), "top", "lyd_unlink")
	if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
		t.Fatal("refused removal changed the graph")
	}
	checkTreeRetainedRB(t, tr)
}

// TestStage4ABIBudget: every charge point of an R1-R3 comparison path reports ErrBudget, never
// a D-0121 refusal, and leaves no result: a value pair, an ordered key profile and an insertion.
func TestStage4ABIBudget(t *testing.T) {
	leafList := func(typ string) string {
		return `module abi-budget { yang-version 1.1; namespace "urn:abi-budget"; prefix b;
 import ietf-inet-types { prefix inet; } container c { leaf-list v { type ` + typ + `; } } }`
	}
	keyed := func(typ string) string {
		return `module abi-budget { yang-version 1.1; namespace "urn:abi-budget"; prefix b;
 container c { list l { key "k j"; leaf k { type ` + typ + `; } leaf j { type string; } } } }`
	}
	cases := []struct{ name, a, b, ax, bx string }{
		{"R1", leafList("int8"), leafList("int16"), `<v>-1</v><v>3</v>`, `<v>256</v><v>2</v>`},
		{"R2", leafList("int8"), leafList("string"), `<v>-1</v><v>3</v>`, `<v>2</v><v>a</v>`},
		{"R3", leafList("inet:ipv4-address"), leafList("string"), `<v>1.2.3.4</v>`, `<v>a</v><v>b</v>`},
		{"R1-keys", keyed("uint8"), keyed("uint16"), `<l><k>1</k><j>a</j></l>`, `<l><k>2</k><j>b</j></l><l><k>300</k><j>c</j></l>`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := parseOnlyXML(t, reviewDiffSet(t, tc.a), `<c xmlns="urn:abi-budget">`+tc.ax+`</c>`)
			second := parseOnlyXML(t, reviewDiffSet(t, tc.b), `<c xmlns="urn:abi-budget">`+tc.bx+`</c>`)
			for limit := uint64(1); ; limit++ {
				d, err := diffNodesLookupLimit(topOf(first), topOf(second), DiffOptions{}, false, first, second, 0, limit)
				if err == nil {
					if d == nil {
						t.Fatal("no diff")
					}
					checkTreeRetainedRB(t, d)
					break
				}
				if d != nil || !errors.Is(err, yang.ErrBudget) {
					t.Fatalf("limit %d: Diff = (%v, %v), want (nil, ErrBudget)", limit, d, err)
				}
				if limit > 1<<16 {
					t.Fatal("no limit succeeded")
				}
			}
		})
	}
	// insertion of a foreign R1 member, charged before its comparisons
	module := func(typ string) string {
		return `module abi-budget { yang-version 1.1; namespace "urn:abi-budget"; prefix b; leaf-list top { type ` + typ + `; } }`
	}
	a, b := reviewDiffSet(t, module("int8")), reviewDiffSet(t, module("int16"))
	for limit := uint64(1); ; limit++ {
		tr := parseOnlyXML(t, a, `<top xmlns="urn:abi-budget">1</top><top xmlns="urn:abi-budget">3</top><top xmlns="urn:abi-budget">5</top>`)
		before := snapshotHashBudgetTree(tr)
		tr.work.hashLimit = limit
		err := tr.Insert(nil, retainedRBTerm(t, retainedRBTop(t, b), "256"))
		if err == nil {
			checkTreeRetainedRB(t, tr)
			break
		}
		if !errors.Is(err, yang.ErrBudget) || !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
			t.Fatalf("limit %d: insertion = %v, want ErrBudget and no change", limit, err)
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
			if keys == "a b" { // matching layout: uint8/uint16 is R1
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
	// a missing leading key: rb_compare_lists would read a non-key first child
	module := func(keyType string) string {
		return `module refusal-keys { yang-version 1.1; namespace "urn:refusal-keys"; prefix r;
 container c { list l { key "a"; leaf a { type ` + keyType + `; } } } }`
	}
	tr := parseOnlyXML(t, reviewDiffSet(t, module("uint8")), `<c xmlns="urn:refusal-keys"><l><a>1</a></l></c>`)
	other := parseOnlyXML(t, reviewDiffSet(t, module("uint8")), `<c xmlns="urn:refusal-keys"><l><a>2</a></l></c>`)
	l := newInner(topOf(other).kids.At(0).schema) // no key child
	if order, ok := rbCompareLists(nil, topOf(tr).kids.At(0), l); ok {
		t.Fatalf("a list without its key compared: %d", order)
	}
}

// TestStage4ApplyDiffRefusalKeepsPrefix: ApplyDiff refuses before the unsafe primitive (int8 into
// a uint8 run: signed against unsigned) and keeps the already applied prefix.
func TestStage4ApplyDiffRefusalKeepsPrefix(t *testing.T) {
	module := func(typ string) string {
		return `module refusal-apply { yang-version 1.1; namespace "urn:refusal-apply"; prefix r;
 leaf a { type string; } leaf-list v { type ` + typ + `; } }`
	}
	dst := parseOnlyXML(t, reviewDiffSet(t, module("uint8")), `<v xmlns="urn:refusal-apply">1</v>`)
	src := parseOnlyXML(t, reviewDiffSet(t, module("int8")), `<a xmlns="urn:refusal-apply">prefix</a><v xmlns="urn:refusal-apply">2</v>`)
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

// TestStage4LeafrefSortNeighbour: a leafref compares as its stored target realtype.
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

// TestStage4PluginImplementationIdentity: the two date-and-time revisions have distinct
// registered implementations, so they never share a profile, whatever their display id.
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

// TestStage4KeyProfileBudgetAndLifetime: the key profile charges before it allocates, is built
// once per operation and is dropped with it.
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

func stage4UnusedKeySet(t *testing.T) *schema.Set {
	t.Helper()
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

// TestStage4KeyProfileDoesNotInvokeUnusedCallback: a later key's profile is checked, but its
// value callback (an empty time-period) never runs once the first key decides.
func TestStage4KeyProfileDoesNotInvokeUnusedCallback(t *testing.T) {
	a, b := stage4UnusedKeySet(t), stage4UnusedKeySet(t)
	first := parseOnlyXML(t, a, `<c xmlns="urn:refusal-unused-key"><l><a>1</a><b/></l></c>`)
	second := parseOnlyXML(t, b, `<c xmlns="urn:refusal-unused-key"><l><a>2</a><b/></l></c>`)
	for _, pair := range [][2]*Tree{{first, second}, {second, first}} {
		if d, err := Diff(pair[0], pair[1], DiffOptions{}); err != nil || d == nil {
			t.Fatalf("unused key callback was invoked: %v", err)
		}
	}
}

// TestStage4TimePeriodEmptySameContext: lyplg_type_sort_time_period reads before an empty
// canonical value (time_period.c:52), also for one realtype in one context. Every sort path
// refuses where that callback would run: insertion descent, native search, diff-tree insertion
// and a list key comparison.
func TestStage4TimePeriodEmptySameContext(t *testing.T) {
	_, period := timePeriodSets(t)
	var sn *schema.Node
	for _, m := range period.Modules {
		for _, top := range m.Top {
			if top.Name == "c" {
				sn = top.Children[0]
			}
		}
	}
	// insertion descent
	tr := parseOnlyXML(t, period, `<c xmlns="urn:rb-time-period"><v>1h</v><v>2h</v></c>`)
	c := topOf(tr)
	before := snapshotHashBudgetTree(tr)
	err := (&logger{}).done(tr.insert(c, retainedRBTerm(t, sn, ""), insertDefault))
	assertStage4Refusal(t, err, "v", "lyd_insert_node")
	if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
		t.Fatal("refused insertion changed the graph")
	}
	// native search: a value changed in place to "" is compared on removal
	tr = parseOnlyXML(t, period, `<c xmlns="urn:rb-time-period"><v>1h</v><v>2h</v><v>3h</v></c>`)
	n := topOf(tr).kids.At(2)
	n.value = retainedRBTerm(t, sn, "").value
	err = n.Unlink()
	assertStage4Refusal(t, err, "v", "lyd_unlink")
	// diff-tree insertion
	first := parseOnlyXML(t, period, `<c xmlns="urn:rb-time-period"><v>1h</v></c>`)
	second := parseOnlyXML(t, period, `<c xmlns="urn:rb-time-period"><v/></c>`)
	d, err := Diff(first, second, DiffOptions{})
	if d != nil {
		t.Fatal("refused Diff returned a result")
	}
	assertStage4Refusal(t, err, "v", "D-0121")
	// a list key compared after an equal first key
	keys := stage4UnusedKeySet(t)
	first = parseOnlyXML(t, keys, `<c xmlns="urn:refusal-unused-key"><l><a>1</a><b>1h</b></l></c>`)
	second = parseOnlyXML(t, keys, `<c xmlns="urn:refusal-unused-key"><l><a>1</a><b/></l></c>`)
	d, err = Diff(first, second, DiffOptions{})
	if d != nil {
		t.Fatal("refused Diff returned a result")
	}
	assertStage4Refusal(t, err, "l", "D-0121")
}

func userordRefusal(t *testing.T, err error, why string) {
	t.Helper()
	assertStage4Refusal(t, err, "", "lyd_diff")
	if !strings.Contains(err.Error(), why) || strings.Contains(err.Error(), "two schema contexts") {
		t.Fatalf("refusal %q, want the %q precondition without a two-context claim", err, why)
	}
}

// TestStage4UserordPreconditions: distinct schema identities are no refusal reason in
// themselves (D-0121 §2.3). In-range histories diff like libyang (conformance
// TestStage4UserordPreconditionsOracle); {a}/{b,a} fails P1 on its final transition, which
// nothing reads afterwards, and is still refused with no result.
func TestStage4UserordPreconditions(t *testing.T) {
	for _, tc := range crossContextUserOrderedCases {
		a, b := crossContextUserOrderedSet(t, tc.module), crossContextUserOrderedSet(t, tc.module)
		for _, pair := range [][2][]string{{{"a"}, {"a"}}, {{"a"}, {"b"}}, {{"a", "b"}, {"b"}}, {{"a"}, {"a", "b"}}} {
			if _, err := Diff(parseOnlyXML(t, a, tc.xml(pair[0])), parseOnlyXML(t, b, tc.xml(pair[1])), DiffOptions{}); err != nil {
				t.Fatalf("%s %v: in-range Diff refused: %v", tc.name, pair, err)
			}
		}
		d, err := Diff(parseOnlyXML(t, a, tc.xml([]string{"a"})), parseOnlyXML(t, b, tc.xml([]string{"b", "a"})), DiffOptions{})
		if d != nil {
			t.Fatal("refused Diff returned a result")
		}
		userordRefusal(t, err, "first instance missing")
		if !strings.Contains(err.Error(), tc.element) {
			t.Fatalf("refusal %q does not name %q", err, tc.element)
		}
	}
}

// TestStage4UserordDeleteOnly: [a,b,c] → [] deletes with second_pos beyond the shrinking array;
// a delete never reads a second-position anchor, so it succeeds in one and in two contexts.
func TestStage4UserordDeleteOnly(t *testing.T) {
	for _, tc := range crossContextUserOrderedCases {
		a, b := crossContextUserOrderedSet(t, tc.module), crossContextUserOrderedSet(t, tc.module)
		for _, other := range []*schema.Set{a, b} {
			d, err := Diff(parseOnlyXML(t, a, tc.xml([]string{"a", "b", "c"})), newTree(other), DiffOptions{})
			if err != nil || d == nil || d.top.SchemaLen() != 3 {
				t.Fatalf("%s delete-only Diff = (%v, %v)", tc.name, d, err)
			}
		}
	}
	// the same with duplicate instances of a state leaf-list
	set := reviewDiffSet(t, `module uo-del { yang-version 1.1; namespace "urn:uo-del"; prefix u;
  container c { config false; leaf-list v { type string; } } }`)
	first := parseOnlyXML(t, set, `<c xmlns="urn:uo-del"><v>a</v><v>a</v><v>b</v></c>`)
	if _, err := Diff(first, parseOnlyXML(t, set, `<c xmlns="urn:uo-del"/>`), DiffOptions{}); err != nil {
		t.Fatal(err)
	}
}

// stage4MixedUserordTree is the diff of ut-diff/cross-context-userord-delete-before: its
// user-ordered run holds b from the second context and a from the first.
func stage4MixedUserordTree(t *testing.T) *Tree {
	t.Helper()
	const module = `module diff-cross-context-userord { yang-version 1.1; namespace "urn:uo"; prefix u;
  leaf-list v { ordered-by user; type string; } }`
	a, b := reviewDiffSet(t, module), reviewDiffSet(t, module)
	d, err := Diff(parseOnlyXML(t, a, `<v xmlns="urn:uo">a</v><v xmlns="urn:uo">b</v>`),
		parseOnlyXML(t, b, `<v xmlns="urn:uo">b</v>`), DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(d.Top()); !slices.Equal(got, []string{"v=b", "v=a"}) || ctxOf(d.top.At(0)) == ctxOf(d.top.At(1)) {
		t.Fatalf("mixed diff = %v", got)
	}
	return d
}

// TestStage4UserordOneSidedP1: diffing the mixed tree against nil, the schema lookup returns the
// foreign first sibling, LYD_LIST_FOR_INST stops at once and a later first-pass node is missing
// from the entry (P1, one-sided input).
func TestStage4UserordOneSidedP1(t *testing.T) {
	d, err := Diff(stage4MixedUserordTree(t), nil, DiffOptions{})
	if d != nil {
		t.Fatal("refused Diff returned a result")
	}
	userordRefusal(t, err, "first instance missing")
}

// TestStage4UserordBudgetPrecedence: building the entry can exhaust the lookup budget and leave
// it empty; the budget error is returned, not "first instance missing".
func TestStage4UserordBudgetPrecedence(t *testing.T) {
	mixed := stage4MixedUserordTree(t)
	d, err := diffNodesLookupLimit(mixed.top.first(), nil, DiffOptions{}, false, mixed, nil, 1, 0)
	if d != nil || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("limited Diff = (%v, %v), want ErrBudget", d, err)
	}
}

// TestStage4UserordSameContextP5: one context. A state leaf-list [1,3,2,1] (a,b,c,d) under a
// container with a children hash table; b changes to 1, so duplicate-instance matching follows
// registration order a,d,b. Against [1,1,1,2] the third comparison matches b with first_pos=1,
// second_pos=2: P1 holds, the replace's move length underflows (P5).
func TestStage4UserordSameContextP5(t *testing.T) {
	set := reviewDiffSet(t, `module uo-p5 { yang-version 1.1; namespace "urn:uo-p5"; prefix u;
  container c { config false; leaf a { type string; } leaf b { type string; } leaf x { type string; }
    leaf-list v { type string; } } }`)
	doc := func(vs ...string) string {
		s := `<c xmlns="urn:uo-p5"><a>1</a><b>2</b><x>3</x>`
		for _, v := range vs {
			s += "<v>" + v + "</v>"
		}
		return s + "</c>"
	}
	first := parseOnlyXML(t, set, doc("1", "3", "2", "1"))
	b := topOf(first).kids.At(4)
	if b.Value() != "3" {
		t.Fatalf("b = %s", b.Value())
	}
	if _, err := b.SetValue("1"); err != nil {
		t.Fatal(err)
	}
	d, err := Diff(first, parseOnlyXML(t, set, doc("1", "1", "1", "2")), DiffOptions{})
	if d != nil {
		t.Fatal("refused Diff returned a result")
	}
	userordRefusal(t, err, "move-length underflow")
}

// TestStage4UserordP2BeforeCompare: a matched pair whose second_pos is past the array is
// refused before lyd_compare_single would read inst[second_pos] (P2), and nothing changes.
func TestStage4UserordP2BeforeCompare(t *testing.T) {
	set := crossContextUserOrderedSet(t, crossContextUserOrderedCases[0].module)
	tr := parseOnlyXML(t, set, crossContextUserOrderedCases[0].xml([]string{"a"}))
	other := parseOnlyXML(t, set, crossContextUserOrderedCases[0].xml([]string{"a"}))
	first, second := tr.top.At(0), other.top.At(0)
	u := &userord{schema: first.schema, pos: 1, inst: []*Node{first}}
	_, _, err := newTree(set).userordAttrs(first, second, DiffOptions{}, u)
	userordRefusal(t, (&logger{}).done(err), "comparison index")
	if u.pos != 2 || len(u.inst) != 1 || u.inst[0] != first {
		t.Fatalf("refusal changed the array: %+v", u)
	}
}

// TestStage4TimePeriodEmptyUnionMember: a union whose values both select time-period calls the
// time-period callback (lyplg_type_sort_union, union.c), so an empty member is refused like a
// plain time-period, for leaf-list values and list keys alike. Values selecting different
// members order by member position and never reach that callback.
func TestStage4TimePeriodEmptyUnionMember(t *testing.T) {
	const module = `module refusal-union-period { yang-version 1.1; namespace "urn:rup"; prefix r;
 import libnetconf2-netconf-server { prefix ln2; }
 typedef u { type union { type uint8; type ln2:time-period; } }
 container c { leaf-list v { type u; } list l { key k; leaf k { type u; } } } }`
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{
		"refusal-union-period.yang": {Data: []byte(module)}, "libnetconf2-netconf-server.yang": {Data: []byte(timePeriodTypedefModule)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load("refusal-union-period", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := c.Snapshot()
	tree := func(xml string) *Tree { return parseOnlyXML(t, set, `<c xmlns="urn:rup">`+xml+`</c>`) }
	// insertion descent into a leaf-list of union values is refused without a change
	tr := tree(`<v>1h</v><v>2h</v>`)
	ct := topOf(tr)
	before := snapshotHashBudgetTree(tr)
	err = (&logger{}).done(tr.insert(ct, retainedRBTerm(t, ct.kids.At(0).schema, ""), insertDefault))
	assertStage4Refusal(t, err, "v", "lyd_insert_node")
	if !reflect.DeepEqual(before, snapshotHashBudgetTree(tr)) {
		t.Fatal("refused insertion changed the graph")
	}
	for _, pair := range [][2]string{{`<v>1h</v>`, `<v/>`}, {`<l><k>1h</k></l>`, `<l><k/></l>`}} {
		for _, p := range [][2]string{pair, {pair[1], pair[0]}} {
			d, err := Diff(tree(p[0]), tree(p[1]), DiffOptions{})
			if d != nil {
				t.Fatalf("%s/%s: refused Diff returned a result", p[0], p[1])
			}
			assertStage4Refusal(t, err, p[0][1:2], "D-0121")
		}
	}
	// different members: member order, no time-period callback
	for _, pair := range [][2]string{{`<v>5</v>`, `<v/>`}, {`<l><k>5</k></l>`, `<l><k/></l>`}} {
		if d, err := Diff(tree(pair[0]), tree(pair[1]), DiffOptions{}); err != nil || d == nil {
			t.Fatalf("%s/%s: Diff = (%v, %v)", pair[0], pair[1], d, err)
		}
	}
}
