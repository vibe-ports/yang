// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// The binary suffix is a signed-byte native hash collision independently found
// for the m/l schema seed. Insertion must preserve A's earlier registration.
func TestStoredHashKeyInsertionHistory(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 container r { leaf p { type string; } leaf q { type string; } leaf z { type string; }
 list l { key k; ordered-by user; leaf k { type binary; } leaf marker { type string; } } } }`)
	for _, returns := range []bool{false, true} {
		t.Run(fmt.Sprint(returns), func(t *testing.T) {
			tr, _, err := parseJSONString(set, `{"m:r":{"p":"p","q":"q","z":"z","l":[{"k":"QQ==","marker":"A"},{"k":"QQ==","marker":"B"}]}}`, Reject, true)
			if err != nil {
				t.Fatal(err)
			}
			root := tr.top.first()
			a := root.kids.At(3)
			if a.hash != 0x9741a396 {
				t.Fatalf("initial hash %#x", a.hash)
			}
			value := "A3PU0pE="
			if returns {
				value = ""
			}
			key, err := tr.NewTerm(a, "k", value, NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if returns {
				if a.hash != 0xc613e99e {
					t.Fatalf("intermediate hash %#x", a.hash)
				}
				if _, err := key.SetValue("A3PU0pE="); err != nil {
					t.Fatal(err)
				}
			}
			if a.hash != 0x9741a396 {
				t.Fatalf("final hash %#x", a.hash)
			}
			const path = "/m:r/l[k='QQ==']/marker"
			selected, err := tr.Find(path)
			if err != nil || selected == nil || selected.Value() != "A" {
				t.Fatalf("Find selected %v, %v", selected, err)
			}
			if _, err := tr.NewPath(path, "updated", NewPathOptions{Update: true}); err != nil {
				t.Fatal(err)
			}
			if a.kids.At(a.kids.SchemaLen()-1).Value() != "updated" || root.kids.At(4).kids.At(1).Value() != "B" {
				t.Fatal("NewPath updated the wrong registration")
			}
			checkRecordOrder(t, &root.kids)
		})
	}
}

func exposedCopy(t *testing.T, kind string) (*Tree, *Node, *Node) {
	t.Helper()
	member := `leaf-list v { type string; ordered-by user; }`
	input := `"v":["old"]`
	if kind == "list" {
		member = `list v { key k; ordered-by user; leaf k { type string; } }`
		input = `"v":[{"k":"old"}]`
	}
	if kind == "union" {
		member = `leaf v { type union { type leafref { path "/m:target"; } type string; } }`
		input = `"v":"01"`
	}
	model := `module m { yang-version 1.1; namespace "urn:m"; prefix m; leaf target { type uint8; }
 container r { leaf p {type string;} leaf q {type string;} leaf z {type string;}
 list l {key "%s"; leaf a {type string;} %s ` + member + ` } } }`
	srcSet := opIndexSet(t, fmt.Sprintf(model, "a", ""))
	dstSet := opIndexSet(t, fmt.Sprintf(model, "a b", `leaf b {type string;}`))
	src, _, err := parseJSONString(srcSet, `{"m:r":{"p":"p","q":"q","z":"z","l":[{"a":"x",`+input+`}]}}`, Reject, true)
	if kind == "union" {
		src, _, err = parseXMLString(srcSet, `<r xmlns="urn:m"><p>p</p><q>q</q><z>z</z><l><a>x</a><v>01</v></l></r>`, Reject, true)
	}
	if err != nil {
		t.Fatal(err)
	}
	dst := newTree(dstSet)
	root, _, err := dst.Dup(src.top.first(), nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	list := root.kids.At(3)
	return dst, root, list.kids.At(1)
}

func TestStoredHashExposedMutations(t *testing.T) {
	for _, kind := range []string{"leaflist", "list", "union"} {
		t.Run(kind, func(t *testing.T) {
			tr, root, exposed := exposedCopy(t, kind)
			list := exposed.parent
			hash, order := list.hash, root.kids.exactByNode[list].order
			switch kind {
			case "leaflist":
				if _, err := exposed.SetValue("new"); err != nil {
					t.Fatal(err)
				}
			case "list":
				if _, err := exposed.kids.At(0).SetValue("new"); err != nil {
					t.Fatal(err)
				}
			case "union":
				if _, err := exposed.SetValue("02"); err != nil {
					t.Fatal(err)
				}
				if exposed.Value() != "2" {
					t.Fatalf("before validation = %q", exposed.Value())
				}
				// Resolve only the exposed value: the intentionally incomplete enclosing
				// list is legal to duplicate but final validation also diagnoses its key.
				if diags, err := tr.Validate(context.Background(), ValidateOptions{Present: true}); err != nil {
					t.Fatal(err, diags)
				}
				if exposed.Value() != "02" {
					t.Fatalf("after validation = %q", exposed.Value())
				}
			}
			if list.hash != hash || root.kids.exactByNode[list].order != order {
				t.Fatal("positional reindex changed native history")
			}
			if !root.Equal(root, CompareOptions{FullRecursion: true}) {
				t.Fatal("recursive self-comparison failed")
			}
			clone := newTree(tr.set)
			if _, _, err := clone.Dup(root, nil, DupOptions{Recursive: true}); err != nil {
				t.Fatal(err)
			}
			if err := tr.Merge(clone); err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, n := range root.kids.SchemaAll() {
				if n.schema.Kind == schema.List {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("merge produced %d lists", count)
			}
		})
	}
}

func TestStoredHashNonkeyEditWork(t *testing.T) {
	set := opIndexSet(t, `module m {yang-version 1.1; namespace "urn:m"; prefix m;
 container r {leaf p {type string;} leaf q {type string;} leaf z {type string;}
 list l {key k;leaf k {type string;} leaf x {type boolean;} leaf-list v {type string; ordered-by user;} } } }`)
	measure := func(n, length int) (int64, uint64) {
		tr, _, err := parseJSONString(set, `{"m:r":{"p":"p","q":"q","z":"z","l":[{"k":"`+strings.Repeat("A", length)+`","x":false}]}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		root := tr.top.first()
		list := root.kids.At(3)
		x := list.kids.At(1)
		tr.work.Store(0)
		tr.work.visits = true
		tr.work.shifts = true
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := range n {
			if _, err := x.SetValue([]string{"true", "false"}[i%2]); err != nil {
				t.Fatal(err)
			}
			v, err := tr.NewTerm(list, "v", "v", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := v.Remove(); err != nil {
				t.Fatal(err)
			}
		}
		runtime.ReadMemStats(&after)
		work := tr.work.Load()
		if !root.Equal(root, CompareOptions{FullRecursion: true}) {
			t.Fatal("self comparison after non-key edits")
		}
		return work, after.TotalAlloc - before.TotalAlloc
	}
	for _, vary := range []string{"mutations", "key-bytes", "both"} {
		t.Run(vary, func(t *testing.T) {
			sizes := []int{128, 512}
			var work [2]int64
			var alloc [2]uint64
			for i, n := range sizes {
				count, length := 128, 8192
				if vary != "key-bytes" {
					count = n
				}
				if vary != "mutations" {
					length = n * 64
				}
				work[i], alloc[i] = measure(count, length)
			}
			if work[1] > 6*work[0] {
				t.Fatalf("4N work %d > 6*N %d", work[1], work[0])
			}
			if alloc[1] > 6*alloc[0] {
				t.Fatalf("4N allocations %d > 6*N %d", alloc[1], alloc[0])
			}
			// A longer complete key must not be copied even once per mutation.
			if work[0] > 128*100 || alloc[0] > 128*8192 {
				t.Fatalf("unrelated edits copied key: work=%d bytes=%d", work[0], alloc[0])
			}
		})
	}
}

// All duplicate registrations matter to future removals, but only the oldest
// current-hash record is visible. Extending history must not revisit old records.
func TestStoredHashRegistrationHistoryWork(t *testing.T) {
	set := opIndexSet(t, `module m {yang-version 1.1; namespace "urn:m"; prefix m;
 container r {leaf p {type string;} leaf q {type string;} leaf z {type string;}
 list l {key k;ordered-by user;leaf k {type binary;}leaf marker {type string;}}}}`)
	measure := func(n int) int64 {
		tr, _, err := parseJSONString(set, `{"m:r":{"p":"p","q":"q","z":"z","l":[{"k":"QQ==","marker":"A"},{"k":"QQ==","marker":"B"}]}}`, Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		root := tr.top.first()
		a := root.kids.At(3)
		tr.work.Store(0)
		tr.work.visits = true
		tr.work.shifts = true
		for range n {
			if _, err := tr.NewTerm(a, "k", "A3PU0pE=", NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		work := tr.work.Load()
		if a.hash != 0x9741a396 {
			t.Fatalf("hash %#x", a.hash)
		}
		if found := findNode(t, tr, "/m:r/l[k='QQ==']/marker"); found.Value() != "A" {
			t.Fatal("repeated registration discarded earliest match")
		}
		// Native removes one old registration on each value replacement. The next
		// remaining A registration was inserted after B, so B wins when A returns.
		key := a.kids.At(n)
		if _, err := key.SetValue(""); err != nil {
			t.Fatal(err)
		}
		if _, err := key.SetValue("A3PU0pE="); err != nil {
			t.Fatal(err)
		}
		if found := findNode(t, tr, "/m:r/l[k='QQ==']/marker"); found.Value() != "B" {
			t.Fatal("value replacement did not remove exactly the oldest registration")
		}
		checkRecordOrder(t, &root.kids)
		return work
	}
	n := measure(128)
	fourN := measure(512)
	if fourN > 6*n {
		t.Fatalf("4N history work %d > 6*N %d", fourN, n)
	}
}
