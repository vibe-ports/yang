// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"weak"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// A resolved placement is local even when reservation or application refuses. Keep the
// destination and its journal alive while checking that the entire source can be collected.
func TestSiblingPlacementRefusalReleasesSource(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	  container c { leaf a { type string; } } }`)
	for _, stage := range []string{"reserve", "apply"} {
		t.Run(stage, func(t *testing.T) {
			dst, diags, err := parseJSONString(set, `{"m:c":{"a":"kept"}}`, Reject, true)
			if err != nil {
				t.Fatal(err, diags)
			}
			original := dst.top.first()
			if dst.top.st == nil {
				t.Fatal("destination must have storage")
			}
			j := beginHashTransaction(dst)
			ref := func() weak.Pointer[Tree] {
				src, diags, err := parseJSONString(set, `{"m:c":{"a":"x"}}`, Reject, true)
				if err != nil {
					t.Fatal(err, diags)
				}
				n := src.top.first()
				p := dst.top.physicalPlacement(n, nil)
				amount, err := dst.linkPlacedBound(nil, &dst.top, n, p)
				if err != nil {
					t.Fatal(err)
				}
				dst.work.hashLimit = dst.work.hashUsed + 1
				if stage == "reserve" {
					err = dst.reserveHash(amount)
				} else {
					err = dst.linkPlaced(nil, &dst.top, n, p)
				}
				if !errors.Is(err, yang.ErrBudget) {
					t.Fatalf("refusal: %v", err)
				}
				if dst.top.Len() != 1 || dst.top.first() != original || n.tree != src {
					t.Fatal("refused placement changed ownership")
				}
				return weak.Make(src)
			}()
			runtime.GC()
			runtime.GC()
			if ref.Value() != nil {
				t.Fatal("refused placement retained source tree")
			}
			runtime.KeepAlive(dst)
			runtime.KeepAlive(j)
			err = yang.ErrBudget
			j.end(&err)
		})
	}
}

// Both directions matter: the default inserts b before the opaque tail and hashes a+b;
// an explicit physical append puts b behind that tail and must reserve and apply no hash.
func TestSiblingPlacementKeyPreflight(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	  list l { key "a b"; leaf a { type string; } leaf b { type string; } } }`)
	for _, afterOpaque := range []bool{false, true} {
		t.Run(fmt.Sprint(afterOpaque), func(t *testing.T) {
			tr := newTree(set)
			var sn *schema.Node
			for _, n := range set.Module("m", "").Top {
				if n.Name == "l" {
					sn = n
				}
			}
			if sn == nil {
				t.Fatal("list schema missing")
			}
			list := newInner(sn)
			list.kids.work = &tr.work
			value := func(key *schema.Node, text string) types.Value {
				v, diag := types.Store(key.Type, text, types.FormatJSON, types.HintData, types.ModuleNames{Set: set}, key)
				if diag != nil {
					t.Fatal(diag.Msg)
				}
				return v
			}
			a := newTerm(sn.Keys[0], value(sn.Keys[0], strings.Repeat("a", 4096)))
			o := newOpaque(opaque{Name: "zz"})
			mustLink(t, testBuild(&list.kids, a, o))
			a.parent, o.parent = list, list
			list.keyN = 1
			b := newTerm(sn.Keys[1], value(sn.Keys[1], "b"))
			p := list.kids.linkPlacement(b, 1)
			if afterOpaque {
				p = list.kids.physicalPlacement(b, nil)
			}
			if list.kids.linkAnchor(p) != p.anchor || list.kids.linkDocPos(p) != p.docAt {
				t.Fatal("placement changed")
			}
			before := tr.work.hashUsed
			bound, err := tr.work.termHashPlacedBound(list, b.schema, b.value, p.at, p.docAt)
			if err != nil {
				t.Fatal(err)
			}
			if afterOpaque && (bound != 0 || tr.work.hashUsed-before > 2) {
				t.Fatalf("hash reserved beyond opaque: %d", bound)
			}
			if !afterOpaque && bound < 4096 {
				t.Fatal("leading keys did not reserve value hash")
			}
			// The same result goes through link's full reservation and its actual link.
			amount, err := tr.linkPlacedBound(list, &list.kids, b, p)
			if err != nil {
				t.Fatal(err)
			}
			mustLink(t, tr.reserveHash(amount))
			mustLink(t, tr.linkPlaced(list, &list.kids, b, p))
			if list.kids.docSpan().At(p.docAt) != b {
				t.Fatal("link disagrees with preflight")
			}
			ok, err := keysLead(list, nil, -1, func() error { return nil })
			if err != nil || ok == afterOpaque {
				t.Fatalf("physical key prefix: %t %v", ok, err)
			}
			if afterOpaque && list.hash != 0 {
				t.Fatal("split keys caused a native hash event")
			}
			if !afterOpaque && list.hash == 0 {
				t.Fatal("leading keys did not cause a native hash event")
			}
		})
	}
}

func TestSiblingFacadeInterleavedBounds(t *testing.T) {
	overdraftPanics = true
	t.Cleanup(func() { overdraftPanics = false })
	for _, journaled := range []bool{false, true} {
		for _, operation := range []string{"interleave", "opaque", "remove", "keep", "replace", "publish"} {
			t.Run(fmt.Sprintf("%t/%s", journaled, operation), func(t *testing.T) {
				tr := newTree(nil)
				s := &tr.top
				for range 65 {
					mustLink(t, s.appendNode(&Node{schema: &schema.Node{}}))
					mustLink(t, s.appendNode(&Node{}))
				}
				s.setWork(&tr.work)
				var j *hashTransaction
				if journaled {
					j = beginHashTransaction(tr)
				}
				mustLink(t, s.touch())
				a := &Node{schema: &schema.Node{}}
				p := s.physicalPlacement(a, nil)
				mustLink(t, s.insertPlaced(p, a))
				if operation == "interleave" {
					if !s.st.interleaved() {
						t.Fatal("not interleaved")
					}
				} else {
					switch operation {
					case "opaque":
						opaque := &Node{}
						mustLink(t, s.insertPlaced(s.physicalPlacement(opaque, s.docFirst()), opaque))
					case "remove":
						mustLink(t, s.DeleteAt(3))
					case "keep":
						mustLink(t, s.deleteMatching(func(n *Node) bool { return n == a }, func(*Node) bool { return true }))
					case "replace":
						run := s.schemaSpan().Collect()
						slices.Reverse(run)
						mustLink(t, s.replaceSchema(0, s.SchemaLen(), spanOf(run)))
					case "publish":
						run := s.schemaSpan().Collect()
						slices.Reverse(run)
						run = append(run, &Node{schema: &schema.Node{}})
						mustLink(t, s.publishRun(0, s.SchemaLen(), run))
					}
				}
				if err := storeCheckInvariant(s); err != nil {
					t.Fatal(err)
				}
				var err error
				j.end(&err)
			})
		}
	}
}

// The storage capability stays dark: public parse, duplication, construction, merge and moves
// retain the legacy schema/opaque partition even when input members arrive interspersed.
func TestSiblingFacadePublicLayouts(t *testing.T) {
	set := pjSchema()
	for _, format := range []string{"json", "xml"} {
		t.Run(format, func(t *testing.T) {
			var tr *Tree
			var err error
			if format == "json" {
				tr, _, err = parseJSONString(set, `{"pj:c":{"zz":"x","s":"a","yy":"y","n":1,"ll":[3,1]}}`, Opaque, true)
			} else {
				tr, _, err = parseXMLString(set, `<c xmlns="urn:pj"><zz>x</zz><s>a</s><yy>y</yy><n>1</n><ll>3</ll><ll>1</ll></c>`, Opaque, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			c := tr.top.first()
			if c.schema == nil {
				t.Fatal("fixture namespace must produce a schema container")
			}
			assertLayout := func(tree *Tree) {
				t.Helper()
				stack := []*siblings{&tree.top}
				for len(stack) > 0 {
					s := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					if s.st.interleaved() || !slices.Equal(s.docNodes(), s.CollectRange(0, s.Len())) {
						t.Fatal("public producer interleaved kinds")
					}
					if err := storeCheckInvariant(s); err != nil {
						t.Fatal(err)
					}
					for _, n := range s.All() {
						stack = append(stack, &n.kids)
					}
				}
			}
			assertLayout(tr)
			dst := newTree(set)
			cp, diags, err := dst.Dup(c, nil, DupOptions{Recursive: true})
			if err != nil || len(diags) != 0 {
				t.Fatal(err, diags)
			}
			if _, err := dst.NewTerm(cp, "ll", "2", NewOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := dst.NewOpaque(cp, "tail", "z", OpaqueOptions{Module: "pj"}); err != nil {
				t.Fatal(err)
			}
			assertLayout(dst)
			if err := dst.Merge(tr); err != nil {
				t.Fatal(err)
			}
			assertLayout(dst)
			if err := dst.Insert(cp, cp.kids.docFirst()); err != nil {
				t.Fatal(err)
			}
			assertLayout(dst)
		})
	}
}

// Preflight sees the prospective layout, including an opaque insertion that first makes the
// set interleaved. Its parent discriminator must reserve the physical prefix, before the
// sticky flag has been set. Appending the same value stays on main's ordinary path.
func TestSiblingPlacementOpaquePrefixBound(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	  container c { list l { key "a b"; leaf a { type string; } leaf b { type string; } } } }`)
	for _, before := range []bool{false, true} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			tr, diags, err := parseJSONString(set, `{"m:c":{"l":[{"a":"0","b":"x"},{"a":"1","b":"x"},{"a":"2","b":"x"},{"a":"3","b":"x"}]}}`, Reject, true)
			if err != nil {
				t.Fatal(err, diags)
			}
			c := tr.top.first()
			list := c.kids.At(0)
			if c.kids.ht == nil || !list.lkeyOK {
				t.Fatal("fixture needs an indexed complete list")
			}
			n := newOpaque(opaque{Name: "zz", Value: strings.Repeat("x", 4096)})
			var anchor *Node
			if before {
				anchor = list.kids.docFirst()
			}
			p := list.kids.physicalPlacement(n, anchor)
			bound := tr.linkIndexPlacedBound(list, &list.kids, n, p)
			j := beginHashTransaction(tr)
			defer func() { err := yang.ErrBudget; j.end(&err) }()
			mustLink(t, list.kids.insertPlaced(p, n))
			if list.kids.docSpan().At(p.docAt) != n {
				t.Fatal("physical placement changed")
			}
			actual := indexWorkBound(list, nil, nil, &tr.work)
			if bound != actual {
				t.Fatalf("reserved %d, physical prefix needs %d", bound, actual)
			}
		})
	}
}
