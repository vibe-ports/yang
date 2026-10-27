// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// A repeated nested key completes the inner list, whose keyLinked rehash then
// reindexes the outer list that compares it positionally (the
// hash-exposed-nested-list fixture shape). The preflight must reserve that
// ancestor discriminator, so a budget failure leaves the tree untouched.
func TestNewTermBudgetCoversAncestorReindex(t *testing.T) {
	sets := map[bool][2]*schema.Set{}
	build := func(tableGrows bool) (*Tree, *Node) {
		// With three existing children under v, the fourth builds v's child
		// table in the same link as the key and ancestor rehash.
		vBody, vData := "leaf k { type string; }", `{"k":"old"}`
		if tableGrows {
			vBody += " leaf-list s { type string; } leaf o { type string; }"
			vData = `{"k":"old","s":["` + strings.Repeat("s", 24<<10) + `"],"o":"o"}`
		}
		if _, ok := sets[tableGrows]; !ok {
			sets[tableGrows] = [2]*schema.Set{
				opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf p { type string; } leaf q { type string; } leaf z { type string; }
    list l { key a; leaf a { type string; } list v { key k; ordered-by user; `+vBody+` } } } }`),
				opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf p { type string; } leaf q { type string; } leaf z { type string; }
    list l { key "a b"; leaf a { type string; } leaf b { type string; } list v { key k; ordered-by user; `+vBody+` } } } }`),
			}
		}
		srcSet, dstSet := sets[tableGrows][0], sets[tableGrows][1]
		src, _, err := parseJSONString(srcSet, `{"m:r":{"l":[{"a":"`+strings.Repeat("A", 12<<10)+`","v":[`+vData+`]}]}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		dst := newTree(dstSet)
		root, err := dst.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"p", "q", "z"} {
			if _, err := dst.NewTerm(root, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		list, _, err := dst.Dup(src.top.first().kids.At(0), root, DupOptions{Recursive: true})
		if err != nil {
			t.Fatal(err)
		}
		if root.kids.ht == nil || list.kids.SchemaLen() != 2 || list.kids.At(1).schema.Name != "v" {
			t.Fatal("fixture needs an indexed copied prefix exposing v")
		}
		v := list.kids.At(1)
		if tableGrows && (v.kids.ht != nil || v.kids.SchemaLen() != htMinItems-1) {
			t.Fatal("fixture needs v one child short of its table")
		}
		return dst, v
	}
	inner := strings.Repeat("k", 48<<10)
	full, v := build(false)
	if _, err := full.NewTerm(v, "k", inner, NewOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		tableGrows bool
		limit      uint64
	}{
		{"ancestor", false, full.work.hashUsed - 1},
		// Key and ancestor need about 72 KiB, the table about 48 KiB: each
		// fits on its own, only one combined reservation refuses the link.
		{"ancestor-and-table", true, 80 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst, v := build(tc.tableGrows)
			before := snapshotHashBudgetTree(dst)
			dst.work.hashLimit = tc.limit
			if _, err := dst.NewTerm(v, "k", inner, NewOptions{}); !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("NewTerm at limit %d: %v; want ErrBudget", tc.limit, err)
			}
			if after := snapshotHashBudgetTree(dst); !reflect.DeepEqual(after, before) {
				t.Fatal("failed NewTerm mutated the tree")
			}
		})
	}
}

// union_find_type turns logging off for every member trial, so the default
// check of a detached union with a leafref member logs only the union error,
// not lyxp_eval's root error from the leafref member.
func TestPrintDetachedUnionDefaultSuppressesMemberProbe(t *testing.T) {
	_, r := dfltLeafrefTree(t, "x", "x")
	r.schema.Type = &schema.Type{Base: schema.Union, Union: []*schema.Type{r.schema.Type, {Base: schema.Uint8}}}
	if err := r.Unlink(); err != nil {
		t.Fatal(err)
	}
	o := printConfig{PrintOptions: PrintOptions{WithDefaults: WDTrim}, dflt: &defaultCheck{}}
	var b bytes.Buffer
	if err := r.print(&b, o, printJSON); err != nil {
		t.Fatal(err)
	}
	if len(o.dflt.diags) != 1 || o.dflt.diags[0].Err != "LY_EVALID" {
		t.Fatalf("diagnostics: %+v", o.dflt.diags)
	}
}

// lyd_dup_single compares the source and parent contexts only; a B parent
// inserted into an A tree is a valid destination for a B source.
func TestDupUnderForeignParentInTree(t *testing.T) {
	a := round12Set(t, false, "pv2-detached")
	b := round12Set(t, false, "pv2-detached")
	tr := round12Tree(t, a, `{"pv2-detached:l":[{"k":"a"}]}`)
	c := round12Tree(t, b, `{"pv2-detached:c":{"ll":["x","y"]}}`).top.first()
	if err := tr.Insert(nil, c); err != nil {
		t.Fatal(err)
	}
	d, _, err := tr.Dup(c.kids.At(0), c, DupOptions{})
	if err != nil {
		t.Fatalf("Dup under its own-context parent: %v", err)
	}
	if d.parent != c || ctxOf(d) != b || c.kids.SchemaLen() != 3 {
		t.Fatalf("copy parent=%p ctx=%p children=%d", d.parent, ctxOf(d), c.kids.SchemaLen())
	}
}

// lyd_dup_r converts each child from LYD_CTX(child): a B node under an opaque
// A parent needs its module in A, even when the parent itself does not.
func TestDupRecursiveChildOwnContext(t *testing.T) {
	a := round12Set(t, false, "pv2-edit")
	b := round12Set(t, false, "pv2-detached")
	tr, _, err := parseJSONString(a, `{"pv2-edit:unknown":{}}`, Opaque, false)
	if err != nil {
		t.Fatal(err)
	}
	o := tr.top.first()
	if o.schema != nil {
		t.Fatal("fixture needs an opaque parent")
	}
	if err := tr.Insert(o, round12Tree(t, b, `{"pv2-detached:c":{"base":"x"}}`).top.first()); err != nil {
		t.Fatal(err)
	}
	dst := newTree(a)
	_, _, err = dst.Dup(o, nil, DupOptions{Recursive: true})
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Diags) == 0 || ve.Diags[0].Err != "LY_ENOTFOUND" ||
		!strings.Contains(ve.Diags[0].Msg, `Module "pv2-detached" not present`) {
		t.Fatalf("Dup: %v; want LY_ENOTFOUND for the child's module", err)
	}
	if dst.top.len() != 0 {
		t.Fatal("failed Dup linked a copy")
	}
}

// A user-ordered run copied into a system-ordered schema is unsorted and its
// parent has no child table yet. insertPos sorts it and builds its RB state
// before link creates the table with the fourth child, so that link must be
// reserved first: a budget failure leaves order and RB state unchanged.
func TestBudgetFourthChildSortsUnindexedRun(t *testing.T) {
	user := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container c { leaf-list ll { type string; ordered-by user; } } }`)
	system := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container c { leaf-list ll { type string; } } }`)
	value := strings.Repeat("v", 24<<10)
	build := func() (*Tree, *Node) {
		src, _, err := parseJSONString(user, `{"m:c":{"ll":["c","b","a"]}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		dst := newTree(system)
		c, _, err := dst.Dup(src.top.first(), nil, DupOptions{Recursive: true})
		if err != nil {
			t.Fatal(err)
		}
		if c.kids.ht != nil || c.kids.SchemaLen() != htMinItems-1 || c.kids.rbTree[c.kids.At(0)] ||
			c.kids.At(0).value.Canonical() != "c" {
			t.Fatal("fixture needs an unsorted, unindexed run one child short of its table")
		}
		return dst, c
	}
	for _, tc := range []struct {
		name string
		add  func(dst *Tree, c *Node) error
	}{
		{"NewTerm", func(dst *Tree, c *Node) error {
			_, err := dst.NewTerm(c, "ll", value, NewOptions{})
			return err
		}},
		{"Insert", func(dst *Tree, c *Node) error {
			other := newTree(system)
			oc, err := other.NewInner(nil, "m:c", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			n, err := other.NewTerm(oc, "ll", value, NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return dst.Insert(c, n)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst, c := build()
			before := snapshotHashBudgetTree(dst)
			dst.work.hashLimit = 48 << 10
			if err := tc.add(dst, c); !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("%s: %v; want ErrBudget", tc.name, err)
			}
			if after := snapshotHashBudgetTree(dst); !reflect.DeepEqual(after, before) {
				t.Fatalf("failed %s sorted the run or built its RB state", tc.name)
			}
			dst.work.hashLimit = 0
			if err := tc.add(dst, c); err != nil {
				t.Fatalf("%s within the default quota: %v", tc.name, err)
			}
			if c.kids.ht == nil || c.kids.At(0).value.Canonical() != "a" {
				t.Fatal("successful insertion must sort the run and build the table")
			}
		})
	}
}

// A copied list whose keys are incomplete compares its first len(Keys)
// children positionally. Sorting the copied leaf-list run moves the long value
// into that prefix, so the parent's reindex bound must hold for any order.
func TestBudgetSortExposesParentPrefix(t *testing.T) {
	user := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf p { type string; } leaf q { type string; } leaf z { type string; }
    list l { key a; leaf a { type string; } leaf-list ll { type string; ordered-by user; } } } }`)
	system := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { leaf p { type string; } leaf q { type string; } leaf z { type string; }
    list l { key "a b"; leaf a { type string; } leaf b { type string; } leaf-list ll { type string; } } } }`)
	long := "a" + strings.Repeat("x", 24<<10)
	build := func() (*Tree, *Node) {
		src, _, err := parseJSONString(user, `{"m:r":{"l":[{"a":"k","ll":["z","`+long+`"]}]}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		dst := newTree(system)
		root, err := dst.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"p", "q", "z"} {
			if _, err := dst.NewTerm(root, name, name, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		l, _, err := dst.Dup(src.top.first().kids.At(0), root, DupOptions{Recursive: true})
		if err != nil {
			t.Fatal(err)
		}
		if root.kids.ht == nil || l.kids.SchemaLen() != 3 || l.kids.At(1).value.Canonical() != "z" ||
			l.kids.rbTree[l.kids.At(1)] {
			t.Fatal("fixture needs an indexed incomplete list with an unsorted copied run")
		}
		return dst, l
	}
	dst, l := build()
	before := snapshotHashBudgetTree(dst)
	dst.work.hashLimit = 60 << 10
	if _, err := dst.NewTerm(l, "ll", "m", NewOptions{}); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("NewTerm: %v; want ErrBudget", err)
	}
	if after := snapshotHashBudgetTree(dst); !reflect.DeepEqual(after, before) {
		t.Fatal("failed NewTerm sorted the run or built its RB state")
	}
	dst.work.hashLimit = 0
	if _, err := dst.NewTerm(l, "ll", "m", NewOptions{}); err != nil {
		t.Fatalf("NewTerm within the default quota: %v", err)
	}
	if l.kids.At(1).value.Canonical() != long {
		t.Fatal("successful insertion must sort the run")
	}
}
