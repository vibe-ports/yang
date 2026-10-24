// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// #279: a retained, removed opaque root is an opaque singleton (lyd_find_sibling_first checks
// siblings->schema before its data parent, tree_data.c:3197).
// Oracle: TestFollowups279Requests/detached-opaque-diff-leaflist.
func TestDetachedOpaqueDiffLeafList(t *testing.T) {
	set := round12Set(t, false, "pv2-detached")
	opaq := parseOnlyXML(t, set, `<u xmlns="urn:unknown">x</u>`).top.first()
	if err := opaq.Remove(); err != nil {
		t.Fatal(err)
	}
	ll := round12Tree(t, set, `{"pv2-detached:ll":["a"]}`).top.first()
	for _, pair := range [][2]*Node{{opaq, ll}, {ll, opaq}} {
		for _, diff := range []func(*Node, *Node, DiffOptions) (*Tree, error){DiffTree, DiffSiblings} {
			if _, err := diff(pair[0], pair[1], DiffOptions{}); err != nil {
				t.Errorf("detached opaque diff: %v", err)
			}
		}
	}
}

func followupsDiff(t *testing.T, set *schema.Set, in string) *Tree {
	t.Helper()
	tr, ds, err := parseWith(context.Background(), strings.NewReader(in), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseXML, nil)
	if err != nil {
		t.Fatal(err, ds)
	}
	return tr
}

const fuNS = `xmlns="urn:vibe-ports:yang:conformance:pv2-detached" xmlns:yang="urn:ietf:params:xml:ns:yang:1"`

// #280: lyd_diff_propagate_meta (diff.c:2186-2205). A list anchor missing on the replaced node is
// lyd_change_meta(meta1, NULL), the empty string; a missing leaf-list anchor is
// lyd_dup_meta_single(NULL) = LY_EINVAL after the sibling's anchor was deleted.
// Oracle: TestFollowups279Requests/propagate-meta-*.
func TestPropagateMetaMissingAnchor(t *testing.T) {
	set := round12Set(t, false, "pv2-detached")
	t.Run("list", func(t *testing.T) {
		dst := followupsDiff(t, set, `<l `+fuNS+` yang:operation="create"><k>a</k></l>`+
			`<l `+fuNS+` yang:operation="create" yang:key="[k='a']"><k>b</k></l>`)
		src := followupsDiff(t, set, `<l `+fuNS+` yang:operation="replace" yang:key="" yang:orig-key=""><k>a</k></l>`)
		if err := dst.MergeDiff(src, MergeDiffOptions{}); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := dst.PrintXML(&out, PrintOptions{Shrink: true}); err != nil {
			t.Fatal(err)
		}
		if want := `yang:key=""><k>b</k>`; !strings.Contains(out.String(), want) {
			t.Fatalf("got %s, want b's anchor %s", out.String(), want)
		}
	})
	t.Run("leaf-list", func(t *testing.T) {
		dst := followupsDiff(t, set, `<ll `+fuNS+` yang:operation="create">x</ll>`+
			`<ll `+fuNS+` yang:operation="create" yang:value="x">y</ll>`)
		src := followupsDiff(t, set, `<ll `+fuNS+` yang:operation="replace" yang:value="" yang:orig-value="">x</ll>`)
		err := dst.MergeDiff(src, MergeDiffOptions{})
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 ||
			ve.Diags[0].Msg != `Merging operation "replace" failed.` {
			t.Fatalf("merge = %#v; want LY_EINVAL from lyd_dup_meta_single", err)
		}
	})
}

// #281: a key duplicated with its parents into another context is looked up in the new list by
// lyd_find_sibling_schema, which compares names across contexts (tree_data.c:2449-2453).
// Oracle: TestFollowups279Requests/dup-key-with-parents-foreign.
func TestDupKeyWithParentsForeignContext(t *testing.T) {
	set1, set2 := round12Set(t, false, "pv2-edit"), round12Set(t, false, "pv2-edit")
	tr := round12Tree(t, set1, `{"pv2-edit:c":{"l":[{"k":"a"},{"k":"b"}]}}`)
	d, err := dupTo(findNode(t, tr, "/pv2-edit:c/l[k='b']/k"), set2, nil, dupWithParents, false)
	if err != nil {
		t.Fatalf("key with parents across contexts: %v", err)
	}
	if d.Name() != "k" || d.Value() != "b" || ctxOf(d) != set2 || d.parent == nil || d.parent.Name() != "l" {
		t.Fatalf("duplicate = %v", d)
	}
}

// #282: lyd_compare_single_data rejects unequal lyd_hash values (tree_data.c:1714) before the
// canonical values; uint8 1 and string "1" differ in their LYB bytes.
// Oracle: TestFollowups279Requests/compare-foreign-leaflist-lyb.
func TestEqualForeignLeafListLYB(t *testing.T) {
	a, b := round12VariantSet(t, "ctx-a", "pv2-ctxshape"), round12VariantSet(t, "ctx-b", "pv2-ctxshape")
	s := round12Tree(t, a, `{"pv2-ctxshape:ll":["1"]}`).top.first()
	n := round12Tree(t, b, `{"pv2-ctxshape:ll":[1]}`).top.first()
	if s.Equal(n, CompareOptions{}) || n.Equal(s, CompareOptions{}) {
		t.Fatal("string \"1\" and uint8 1 compare equal across contexts")
	}
}

// #287: lyd_dup_inst_next builds a first instance's set from the live siblings on its first lookup
// (tree_data_common.c:121-125), so it includes an equal instance created earlier in the level.
// Oracle: TestFollowups279Requests/apply-created-duplicate-*.
func TestApplyDiffCreatedDuplicateVisible(t *testing.T) {
	set := round12Set(t, false, "pv2-dupinst")
	for _, tc := range []struct{ name, leaves string }{
		{"table", `"a":"x","b":"y","d":"z",`},
		{"no table", ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := round12Tree(t, set, `{"pv2-dupinst:c":{`+tc.leaves+`"sl":[1,2]}}`)
			diff, ds, err := parseWith(context.Background(), strings.NewReader(`{"pv2-dupinst:c":{"@":{"yang:operation":"none"},`+
				`"sl":[2,1,1,1],"@sl":[{"yang:operation":"none"},{"yang:operation":"create","yang:position":1},`+
				`{"yang:operation":"none","yang:meta-create":"pv2-dupinst:note=x"},`+
				`{"yang:operation":"none","yang:meta-create":"pv2-dupinst:note=y"}]}}`), set,
				parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
			if err != nil {
				t.Fatal(err, ds)
			}
			if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := tr.PrintJSON(&out, PrintOptions{Shrink: true}); err != nil {
				t.Fatal(err)
			}
			if want := `"sl":[1,1,2],"@sl":[{"pv2-dupinst:note":"x"},{"pv2-dupinst:note":"y"},null]`; !strings.Contains(out.String(), want) {
				t.Fatalf("got %s, want %s", out.String(), want)
			}
		})
	}
}

func dupCreateDiff(t *testing.T, set *schema.Set, in string) *Tree {
	t.Helper()
	diff, ds, err := parseWith(context.Background(), strings.NewReader(in), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
	if err != nil {
		t.Fatal(err, ds)
	}
	return diff
}

// #287, system-ordered: a lookup of a builds the level's sibling-order index (no children table at
// the top level), then a duplicate of b is created, then two none entries on b find both
// instances. The diff's own instances are sorted, so its order is the values' order. Oracle: TestFollowups279Requests/apply-created-duplicate-top-level.
func TestApplyDiffCreatedDuplicateTopLevel(t *testing.T) {
	set := round12Set(t, false, "pv2-dupcreate")
	tr := round12Tree(t, set, `{"pv2-dupcreate:tl":["a","b"]}`)
	diff := dupCreateDiff(t, set, `{"pv2-dupcreate:tl":["a","b","b","b"],"@pv2-dupcreate:tl":[`+
		`{"yang:operation":"none"},{"yang:operation":"create"},`+
		`{"yang:operation":"none","yang:meta-create":"pv2-dupcreate:note=x"},`+
		`{"yang:operation":"none","yang:meta-create":"pv2-dupcreate:note=y"}]}`)
	if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tr.PrintJSON(&out, PrintOptions{Shrink: true}); err != nil {
		t.Fatal(err)
	}
	want := `{"pv2-dupcreate:tl":["a","b","b"],"@pv2-dupcreate:tl":[null,{"pv2-dupcreate:note":"x"},{"pv2-dupcreate:note":"y"}]}`
	if out.String() != want {
		t.Fatalf("got %s, want %s", out.String(), want)
	}
}

// #287, mixed contexts with a children table: the level's registration-order index (tableRuns) is
// built by the lookup of a, the foreign z making the level mixed; the created duplicate of x is
// appended to it. The foreign instance is linked directly, as a merge of a foreign source leaves
// it (TestMergeRetainsForeignChildContext). Oracle (built by such a merge):
// TestFollowups279Requests/apply-created-duplicate-mixed-table.
func TestApplyDiffCreatedDuplicateMixedTable(t *testing.T) {
	a, b := round12Set(t, false, "pv2-dupcreate"), round12Set(t, false, "pv2-dupcreate")
	tr := round12Tree(t, a, `{"pv2-dupcreate:c":{"a":"1","b":"2","d":"3","ll":["a","x"]}}`)
	z := findNode(t, round12Tree(t, b, `{"pv2-dupcreate:c":{"ll":["z"]}}`), "/pv2-dupcreate:c/ll[.='z']")
	unlink(z)
	c := tr.top.first()
	if err := tr.link(c, &c.kids, z, c.kids.len()); err != nil {
		t.Fatal(err)
	}
	if c.kids.ht == nil || !c.kids.mixedContexts() {
		t.Fatal("the level must have a children table and mixed contexts")
	}
	diff := dupCreateDiff(t, a, `{"pv2-dupcreate:c":{"@":{"yang:operation":"none"},"ll":["a","x","x","x"],"@ll":[`+
		`{"yang:operation":"none"},{"yang:operation":"create"},`+
		`{"yang:operation":"none","yang:meta-create":"pv2-dupcreate:note=x"},`+
		`{"yang:operation":"none","yang:meta-create":"pv2-dupcreate:note=y"}]}}`)
	if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, n := range c.kids.list {
		if n.Name() == "ll" && n.Value() == "x" {
			var own []string
			for _, m := range n.meta {
				own = append(own, m.value.Canonical())
			}
			notes = append(notes, strings.Join(own, "+"))
		}
	}
	if strings.Join(notes, ",") != "x,y" {
		var out bytes.Buffer
		_ = tr.PrintJSON(&out, PrintOptions{Shrink: true})
		t.Fatalf("notes on the two x instances = %v, want one each, x then y: %s", notes, out.String())
	}
}

// #287: a user-ordered replace moves the created xC behind xA and xB (lyd_diff_insert unlinks and
// reinserts it); the next first lookup builds its set in the live order xA, xB, xC, so the two
// none entries annotate xA and xB. Oracle: TestFollowups279Requests/apply-created-duplicate-moved.
func TestApplyDiffMovedDuplicateLiveOrder(t *testing.T) {
	set := round12Set(t, false, "pv2-dupcreate")
	const ns = `xmlns="urn:vibe-ports:yang:conformance:pv2-dupcreate" xmlns:yang="urn:ietf:params:xml:ns:yang:1" ` +
		`xmlns:pdc="urn:vibe-ports:yang:conformance:pv2-dupcreate"`
	tr := parseOnlyXML(t, set, `<tl `+ns+`>q</tl><sl `+ns+`>z</sl><sl `+ns+` pdc:note="A">x</sl><sl `+ns+` pdc:note="B">x</sl>`)
	diff := followupsDiff(t, set, `<sl `+ns+` yang:operation="none">z</sl>`+
		`<sl `+ns+` yang:operation="create" yang:position="">x</sl>`+
		`<sl `+ns+` yang:operation="replace" yang:position="4" yang:orig-position="1">x</sl>`+
		`<sl `+ns+` yang:operation="none" yang:meta-create="pv2-dupcreate:note=p">x</sl>`+
		`<sl `+ns+` yang:operation="none" yang:meta-create="pv2-dupcreate:note=q">x</sl>`)
	if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, n := range tr.top.list {
		var notes []string
		for _, m := range n.meta {
			notes = append(notes, m.value.Canonical())
		}
		got = append(got, n.Value()+"["+strings.Join(notes, ",")+"]")
	}
	if want := "q[] z[] x[A,p] x[B,q] x[]"; strings.Join(got, " ") != want {
		t.Fatalf("got %s, want %s", strings.Join(got, " "), want)
	}
}

// #287 scaling: the level's sibling-order index is updated in place per create, never rebuilt, and
// a run that grows at its front takes amortized O(1) per prepend. Under the race detector the
// sizes shrink; 4x the creates must still cost well under 16x the work.
func TestApplyDiffCreateLookupScaling(t *testing.T) {
	n1 := 1000
	if raceEnabled {
		n1 = 150
	}
	ratio := func(t *testing.T, work func(n int) int64) {
		t.Helper()
		if w1, w4 := work(n1), work(4*n1); w4 > 6*w1 {
			t.Fatalf("work %d for %d creates, %d for %d: not linear", w1, n1, w4, 4*n1)
		}
	}
	// Distinct values appended, each followed by its first duplicate-instance lookup. Only the
	// cache work is measured: the lookup itself is lyd_find_sibling_first's linear scan at the top
	// level, which has no children table in libyang.
	t.Run("append", func(t *testing.T) {
		set := round12Set(t, false, "pv2-dupcreate")
		ratio(t, func(n int) int64 {
			tr := round12Tree(t, set, `{"pv2-dupcreate:tl":["a"]}`)
			cache := &dupCache{}
			if first, err := tr.dupInstNext(tr.top.first(), cache); err != nil || first == nil { // builds the index
				t.Fatal("no first instance", err)
			}
			tr.work.visits, tr.work.shifts = true, true
			var total int64
			for i := range n {
				node, err := tr.NewPath("/pv2-dupcreate:tl", fmt.Sprintf("v%07d", i), NewPathOptions{})
				if err != nil {
					t.Fatal(err)
				}
				tr.work.Store(0)
				tr.created(cache, node)
				if got, err := tr.dupInstNext(node, cache); err != nil || got != node {
					t.Fatal("created instance is not its own first instance", err)
				}
				total += tr.work.Load()
			}
			return total
		})
	})
	// A none entry on z builds the index, then every create of x goes first among the sll
	// instances (yang:position=""), after a leading ll: the run of x grows at its front. The whole
	// ApplyDiff is measured.
	t.Run("prepend", func(t *testing.T) {
		set := round12Set(t, false, "pv2-detached")
		ratio(t, func(n int) int64 {
			tr := parseOnlyXML(t, set, `<ll xmlns="urn:vibe-ports:yang:conformance:pv2-detached">p</ll>`+
				`<sll xmlns="urn:vibe-ports:yang:conformance:pv2-detached">x</sll>`+
				`<sll xmlns="urn:vibe-ports:yang:conformance:pv2-detached">z</sll>`)
			var b strings.Builder
			b.WriteString(`<sll ` + fuNS + ` yang:operation="none">z</sll>`)
			for range n {
				b.WriteString(`<sll ` + fuNS + ` yang:operation="create" yang:position="">x</sll>`)
			}
			diff := followupsDiff(t, set, b.String())
			tr.work.Store(0)
			tr.work.visits, tr.work.shifts = true, true
			if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
				t.Fatal(err)
			}
			if got := tr.top.len(); got != n+3 {
				t.Fatalf("%d instances, want %d", got, n+3)
			}
			return tr.work.Load()
		})
	})
}
