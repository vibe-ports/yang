// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

const listIndexSchema = `module m {
  yang-version 1.1; namespace "urn:list-index"; prefix m;
  container r {
    leaf p { type string; } leaf q { type string; } leaf r { type string; }
    list l { key "a b"; %s
      leaf a { type string; } leaf b { type string; } leaf v { type string; }
    }
  }
}`

const listIndexCrossSchema = `module m {
  yang-version 1.1; namespace "urn:list-index"; prefix m;
  container r {
    list l { key "%s"; %s
      leaf a { type string; } leaf b { type string; } leaf c { type string; }
    }
  }
}`

// This reference uses only live children and the documented string LYB bytes,
// never lkeyN/lkeyFrozen, hashOf, predicateHash or refreshHash.
func liveStringListHash(n *Node) uint32 {
	h := types.HashMulti32(0, []byte(n.schema.Module.Name))
	h = types.HashMulti32(h, []byte(n.schema.Name))
	for _, c := range n.kids.list {
		if !c.schema.IsKey() {
			break
		}
		h = types.HashMulti32(h, []byte(c.Value()))
	}
	return types.FinishHash32(h)
}

// tree_data.c:1714-1716,1769-1785: compare the children at declared key
// positions, including schema and child hash, not children found by key name.
func positionalListEqual(a, b *Node) bool {
	if a.schema != b.schema || a.hash != b.hash {
		return false
	}
	for i := range a.schema.Keys {
		if i == len(a.kids.list) || i == len(b.kids.list) {
			return len(a.kids.list) == len(b.kids.list)
		}
		x, y := a.kids.list[i], b.kids.list[i]
		if x.schema != y.schema || x.hash != y.hash || x.Value() != y.Value() {
			return false
		}
	}
	return true
}

type listIndexModel struct {
	hashes  map[*Node]uint32
	order   []*Node
	records []modelRegistration
}

type modelRegistration struct {
	node *Node
	hash uint32
}

func (m *listIndexModel) register(n *Node, rehash bool) {
	old, known := m.hashes[n]
	if known {
		// Both value replacement and relocation remove one current-hash record;
		// successful key insertion uses keyInserted and removes none.
		for i, r := range m.records {
			if r.node == n && r.hash == old {
				m.records = slices.Delete(m.records, i, i+1)
				break
			}
		}
	}
	if rehash {
		m.hashes[n] = liveStringListHash(n)
	}
	m.records = append(m.records, modelRegistration{n, m.hashes[n]})
	m.refresh()
}

func (m *listIndexModel) refresh() {
	m.order = nil
	seen := map[*Node]bool{}
	m.records = slices.DeleteFunc(m.records, func(r modelRegistration) bool { _, ok := m.hashes[r.node]; return !ok })
	for _, r := range m.records {
		if r.hash == m.hashes[r.node] && !seen[r.node] {
			m.order = append(m.order, r.node)
			seen[r.node] = true
		}
	}
}

func (m *listIndexModel) keyInserted(n *Node) bool {
	for i, sn := range n.schema.Keys {
		if i >= len(n.kids.list) || n.kids.list[i].schema != sn {
			return false // tree_data.c:684-702,836-842: neither hash nor registration
		}
	}
	m.hashes[n] = liveStringListHash(n)
	m.records = append(m.records, modelRegistration{n, m.hashes[n]})
	m.refresh()
	return true
}

func (m *listIndexModel) check(t *testing.T, tr *Tree, root *Node) {
	t.Helper()
	opidxAssertTreeInvariant(t, tr)
	m.refresh()
	if root.kids.ht == nil {
		t.Fatal("children table disappeared")
	}
	if err := root.kids.checkRecords(); err != nil {
		t.Fatal(err)
	}
	checkRecordOrder(t, &root.kids)
	var previous uint64
	for _, n := range m.order {
		if n.hash != m.hashes[n] {
			t.Fatalf("stored hash = %#x, history requires %#x", n.hash, m.hashes[n])
		}
		r := root.kids.exactByNode[n]
		if r == nil || r.order <= previous {
			t.Fatal("registration order differs from native mutation history")
		}
		previous = r.order
		if k, ok := hashOf(n); !ok || n.hkey != k {
			t.Fatal("bucket discriminator no longer describes the live prefix")
		}
		m.checkProbe(t, tr, root, n)
	}
	// Exhaust the small value domain after EVERY mutation. No index helpers or
	// production equality functions choose the expected result.
	for _, a := range []string{"x", "y", "z"} {
		for _, b := range []string{"x", "y", "z"} {
			probe := newInner(m.order[0].schema)
			for i, val := range []string{a, b} {
				sn := probe.schema.Keys[i]
				v, d := types.StoreOnly(sn.Type, val, types.FormatJSON, types.HintData, nil, sn)
				if d != nil {
					t.Fatal(d.Msg)
				}
				c := newTerm(sn, v)
				c.parent = probe
				probe.kids.list = append(probe.kids.list, c)
			}
			probe.hash, probe.lkeyOK = liveStringListHash(probe), true
			matches := m.checkProbe(t, tr, root, probe)
			got, ok := (xn{root, tr.set}).LookupChild(wrapSchema(tr.set, probe.schema), []string{a, b})
			if !ok || len(got) != min(1, len(matches)) {
				t.Fatalf("LookupChild(%s,%s) = %v, %t; brute-force matches %d", a, b, got, ok, len(matches))
			}
			if ok && len(got) == 1 && got[0].(xn).n != matches[0] {
				t.Fatal("LookupChild returned the wrong instance")
			}
		}
	}
}

func (m *listIndexModel) checkProbe(t *testing.T, tr *Tree, root, probe *Node) []*Node {
	t.Helper()
	var matches []*Node
	for _, n := range m.order {
		if positionalListEqual(n, probe) {
			matches = append(matches, n)
		}
		if compareSingle(tr, n, probe, false) != positionalListEqual(n, probe) {
			t.Fatal("positional comparison differs from the reference")
		}
	}
	var first *Node
	if len(matches) > 0 {
		first = matches[0]
	}
	if tr.findFirst(&root.kids, probe) != first {
		t.Fatal("indexed lookup differs from brute-force positional comparison in registration order")
	}
	return matches
}

func TestListIndexMutationInvariant(t *testing.T) {
	for _, order := range []string{"", "ordered-by user;"} {
		t.Run(order, func(t *testing.T) {
			set := opIndexSet(t, fmt.Sprintf(listIndexSchema, order))
			crossTarget := opIndexSet(t, fmt.Sprintf(listIndexCrossSchema, "b", order))
			crossSources := []*schema.Set{
				opIndexSet(t, fmt.Sprintf(listIndexCrossSchema, "a b", order)),
				opIndexSet(t, fmt.Sprintf(listIndexCrossSchema, "a c b", order)),
			}
			tr, _, err := parseJSONString(set, `{"m:r":{"p":"p","q":"q","r":"r","l":[{"a":"x","b":"y"},{"a":"y","b":"z"},{"a":"z","b":"x"}]}}`, Reject, true)
			if err != nil {
				t.Fatal(err)
			}
			root := tr.top.first()
			model := &listIndexModel{hashes: map[*Node]uint32{}}
			for _, n := range root.kids.list[3:] {
				model.register(n, true)
			}
			model.check(t, tr, root)
			rng := rand.New(rand.NewPCG(257, 5)) //nolint:gosec // reproducible bounded mutation sequences
			const operations = 16
			var covered [operations]int
			// 28 shuffled rounds cover each operation, with at most eight list
			// instances and six children each. The work counter bounds tree work
			// independently of machine speed; the reference scans are bounded too.
			tr.work.visits, tr.work.shifts = true, true
			for round := range 28 {
				for _, op := range rng.Perm(operations) {
					t.Run(fmt.Sprintf("%d/%d", round, op), func(t *testing.T) {
						if len(model.order) >= 8 {
							n := model.order[rng.IntN(len(model.order))]
							if err := n.Remove(); err != nil {
								t.Fatal(err)
							}
							delete(model.hashes, n)
							model.order = slices.DeleteFunc(model.order, func(x *Node) bool { return x == n })
							model.check(t, tr, root)
						}
						n := model.order[rng.IntN(len(model.order))]
						value := []string{"x", "y", "z"}[rng.IntN(3)]
						keys := slices.DeleteFunc(slices.Clone(n.kids.list), func(c *Node) bool { return !c.isKey() })
						if len(n.kids.list) >= 6 {
							unlink(keys[len(keys)-1])
							keys = keys[:len(keys)-1]
							model.check(t, tr, root)
						}
						if len(keys) == 0 {
							c, err := tr.NewTerm(n, "a", "x", NewOptions{})
							if err != nil {
								t.Fatal(err)
							}
							keys = append(keys, c)
							model.keyInserted(n)
							model.check(t, tr, root)
						}
						key := keys[rng.IntN(len(keys))]
						oldOrder := root.kids.exactByNode[n].order
						retainsOrder := false
						switch op {
						case 0, 1: // NewTerm: repeated earlier and repeated last key
							_, err = tr.NewTerm(n, []string{"a", "b"}[op], value, NewOptions{})
							retainsOrder = !model.keyInserted(n)
						case 2, 3: // public/internal changed and unchanged assignments
							changed := key.Value() != value
							if op == 2 {
								_, err = key.SetValue(value)
							} else {
								v, d := types.StoreOnly(key.schema.Type, value, types.FormatJSON, types.HintData, nil, key.schema)
								if d != nil {
									t.Fatal(d.Msg)
								}
								tr.changeTermVal(key, v, false)
							}
							if changed {
								model.register(n, true)
							}
							retainsOrder = !changed
						case 4: // NewPath must create keys when an old historical hash misses
							path := "/m:r/l[a='" + value + "'][b='y']/a"
							old, e := tr.Find(path)
							if e != nil {
								t.Fatal(e)
							}
							_, err = tr.NewPath(path, "z", NewPathOptions{Update: true})
							// An existing key's value comes from the predicate, so
							// LYD_NEW_PATH_UPDATE deliberately ignores the value.
							if old != nil && old.Value() != value {
								t.Fatal("NewPath updated an existing key")
							}
						case 5: // a parentless key copy can be inserted; a linked key cannot
							carrier := newTree(set)
							var c *Node
							c, _, err = carrier.Dup(key, nil, DupOptions{})
							if err == nil {
								err = tr.Insert(n, c)
							}
							retainsOrder = !model.keyInserted(n)
						case 6, 7: // Remove and public unlink refuse keys without mutation
							if op == 6 {
								err = key.Remove()
							} else {
								err = unlinkTree(key)
							}
							if err == nil {
								t.Fatal("key removal unexpectedly accepted")
							}
							err, retainsOrder = nil, true
						case 8: // internal unlink preserves native hash and registration
							unlink(key)
							retainsOrder = true
						case 9, 11: // merge/diff copy final keys into an existing indexed parent
							var src *Tree
							src, _, err = parseJSONString(set, `{"m:r":{"l":[{"a":"x","b":"`+value+`","b":"z"}]}}`, Reject, true)
							if err == nil && op == 9 {
								err = tr.Merge(src)
							} else if err == nil {
								base, _, e := parseJSONString(set, `{"m:r":{}}`, Reject, true)
								if e != nil {
									t.Fatal(e)
								}
								var diff *Tree
								diff, err = Diff(base, src, DiffOptions{})
								if err == nil {
									err = tr.ApplyDiff(diff, ApplyDiffOptions{})
								}
							}
						case 10:
							_, _, err = tr.Dup(n, root, DupOptions{Recursive: true})
						case 12: // unlink/reinsert changes order, but never recalculates hash
							err = tr.Insert(root, n)
							model.register(n, false)
						case 13: // Dup of a key into a parent reuses the existing key
							_, _, err = tr.Dup(key, n, DupOptions{})
							retainsOrder = true
						case 14, 15: // cross-schema copy, then single/bulk removal of exposed non-keys
							input := `{"m:r":{"l":[{"a":"z","b":"x"}]}}`
							if op == 15 {
								input = `{"m:r":{"l":[{"a":"z","c":"y","b":"x"}]}}`
							}
							var src *Tree
							src, _, err = parseJSONString(crossSources[op-14], input, Reject, true)
							if err != nil {
								break
							}
							dst := newTree(crossTarget)
							var dstRoot *Node
							dstRoot, err = dst.NewInner(nil, "m:r", NewOptions{})
							if err != nil {
								break
							}
							var copied *Node
							copied, _, err = dst.Dup(src.top.first().kids.list[0], dstRoot, DupOptions{Recursive: true})
							if err != nil {
								break
							}
							nonkeys := slices.DeleteFunc(slices.Clone(copied.kids.list), func(c *Node) bool { return c.isKey() })
							// The target context sorts its key first. Recreate the source copy's
							// leading non-key prefix while retaining the cross-context hash snapshot;
							// internal relocation/removal paths can leave exactly this native state.
							copied.lkeyFrozen = slices.Clone(snapshotKeys(copied))
							for _, c := range nonkeys {
								unlink(c)
							}
							for i, c := range nonkeys {
								if err = dst.link(copied, &copied.kids, c, i); err != nil {
									break
								}
							}
							if err != nil {
								break
							}
							copied.keyN = 0
							if op == 14 {
								unlink(nonkeys[0])
							} else {
								err = dst.unlinkAll(nonkeys)
							}
							if err == nil && copied.keyN != len(copied.schema.Keys) {
								t.Fatalf("removing non-keys exposed %d leading keys, want %d", copied.keyN, len(copied.schema.Keys))
							}
							if err == nil {
								_, err = dst.NewTerm(copied, "b", value, NewOptions{})
							}
							if err == nil {
								var duplicate *Node
								duplicate, _, err = dst.Dup(copied, dstRoot, DupOptions{Recursive: true})
								if err == nil && !copied.Equal(duplicate, CompareOptions{FullRecursion: true}) {
									t.Fatal("list differs from its recursive duplicate after non-key removal")
								}
							}
							retainsOrder = true
						}
						if err != nil {
							t.Fatal(err)
						}
						r := root.kids.exactByNode[n]
						if retainsOrder && (r == nil || r.order != oldOrder) {
							t.Fatal("mutation without native registration moved the record")
						}
						for _, c := range root.kids.list {
							if c.schema.Kind == schema.List {
								if _, known := model.hashes[c]; !known {
									model.register(c, true)
								}
							}
						}
						model.check(t, tr, root)
						covered[op]++
					})
					if t.Failed() {
						return
					}
				}
			}
			for op, count := range covered {
				if count != 28 {
					t.Fatalf("operation %d covered %d times", op, count)
				}
			}
			if work := tr.work.Load(); work > 2_000_000 {
				t.Fatalf("bounded mutation run used %d work units", work)
			}
		})
	}
}

func TestListIndexRepeatedEarlierKey(t *testing.T) {
	set := opIndexSet(t, fmt.Sprintf(listIndexSchema, ""))
	for _, value := range []string{"z", "y"} {
		tr, _, err := parseJSONString(set, `{"m:r":{"p":"p","q":"q","r":"r","l":[{"a":"x","b":"y"}]}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		root, list := tr.top.first(), tr.top.first().kids.list[3]
		hash, order := list.hash, root.kids.exactByNode[list].order
		if _, err := tr.NewTerm(list, "a", value, NewOptions{}); err != nil {
			t.Fatal(err)
		}
		if list.hash != hash || root.kids.exactByNode[list].order != order {
			t.Fatal("failed positional-key check changed native history")
		}
		got, ok := (xn{root, set}).LookupChild(wrapSchema(set, list.schema), []string{"x", "y"})
		if !ok || len(got) != 0 {
			t.Fatalf("repeated a=%s: original key lookup = %v, %t", value, got, ok)
		}
		if !root.Equal(root, CompareOptions{FullRecursion: true}) {
			t.Fatalf("repeated a=%s: recursive self comparison failed", value)
		}
	}
}

// Internal removal can expose non-key/opaque children in the compared prefix.
// tree_data.c:1769-1785 follows actual child/next pointers, including opaque ones.
func TestListIndexShortPrefix(t *testing.T) {
	set := opIndexSet(t, fmt.Sprintf(listIndexSchema, ""))
	var lists []*Node
	for _, value := range []string{"x", "y"} {
		tr, _, err := parseJSONString(set, `{"m:r":{"p":"p","q":"q","r":"r","l":[{"a":"x","b":"y","v":"v"}]}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		root, list := tr.top.first(), tr.top.first().kids.list[3]
		mustLink(t, tr.insert(list, newOpaque(opaque{Name: "extra", Value: value, Format: types.FormatJSON}), insertDefault))
		for range 2 {
			unlink(list.kids.list[0])
		}
		if tr.findFirst(&root.kids, list) != list {
			t.Fatal("short prefix lost its registration")
		}
		if _, err := list.kids.list[0].SetValue("changed"); err != nil {
			t.Fatal(err)
		}
		if tr.findFirst(&root.kids, list) != list {
			t.Fatal("exposed non-key value change left a stale discriminator")
		}
		lists = append(lists, list)
	}
	if lists[0].Equal(lists[1], CompareOptions{}) {
		t.Fatal("positional comparison ignored exposed opaque values")
	}
}
