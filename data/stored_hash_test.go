// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

func storedHashSet(t *testing.T, typ string) *schema.Set {
	t.Helper()
	return opIndexSet(t, fmt.Sprintf(`module m {
  yang-version 1.1; namespace "urn:stored-hash"; prefix m;
  container r {
    leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf-list s { config false; type %s; }
    list l { key k; leaf k { type %s; } }
  }
}`, typ, typ))
}

func storedValueIndexKey(t *testing.T, sn *schema.Node, value string) idxKey {
	t.Helper()
	v, d := types.StoreOnly(sn.Type, value, types.FormatJSON, types.HintData, nil, sn)
	if d != nil {
		t.Fatal(d.Msg)
	}
	k, _ := hashOf(newTerm(sn, v))
	return k
}

// Known byte vectors for the canonical amd64 signed-char loop in hash_table.c:34-64.
func TestStoredHashBytes(t *testing.T) {
	for _, tc := range []struct {
		parts []string
		want  uint32
	}{
		{nil, 0},
		{[]string{"a"}, 0xca2e9442},
		{[]string{"hello"}, 0xc8fd181b},
		{[]string{"he", "llo"}, 0xc8fd181b},
		{[]string{"\x80\xffA"}, 0x0d9b63e0},
	} {
		var h uint32
		for _, part := range tc.parts {
			h = types.HashMulti32(h, []byte(part))
		}
		if got := types.FinishHash32(h); got != tc.want {
			t.Errorf("hash(%q) = %#x, want %#x", tc.parts, got, tc.want)
		}
	}
}

func TestStoredHashLYBValues(t *testing.T) {
	for _, tc := range []struct {
		base  schema.BaseType
		text  string
		bytes []byte
	}{
		{schema.Int32, "770", []byte{2, 3}},
		{schema.Int8, "-1", []byte{255}},
		{schema.Binary, "YWJj", []byte("abc")},
		{schema.Uint32, "0", nil},
		{schema.String, "", nil},
	} {
		typ := &schema.Type{Base: tc.base}
		v, d := types.StoreOnly(typ, tc.text, types.FormatJSON, types.HintData, nil, nil)
		if d != nil {
			t.Fatal(d.Msg)
		}
		const seed = uint32(0x12345678)
		want := types.HashMulti32(seed, tc.bytes)
		if len(tc.bytes) == 0 {
			want = types.FinishHash32(seed)
		}
		if got, ok := types.HashLYB(seed, v); !ok || got != want {
			t.Errorf("%v %q hash = %#x, %v; want %#x", tc.base, tc.text, got, ok, want)
		}
	}
}

// tree_data.c:2280-2317 copies terminal hashes verbatim, including across types;
// path.c:1363-1420 reports a partial match when the fresh predicate hash misses.
func TestStoredHashCrossTypeDup(t *testing.T) {
	srcSet, dstSet := storedHashSet(t, "string"), storedHashSet(t, "uint32")
	for _, hashed := range []bool{false, true} {
		t.Run(fmt.Sprintf("table=%t", hashed), func(t *testing.T) {
			input := `{"m:r":{"s":["3"],"l":[{"k":"3"}]}}`
			if hashed {
				input = `{"m:r":{"a":"x","b":"y","c":"z","s":["3"],"l":[{"k":"3"}]}}`
			}
			src, _, err := parseJSONString(srcSet, input, Reject, false)
			if err != nil {
				t.Fatal(err)
			}
			dst := newTree(dstSet)
			root, _, err := dst.Dup(src.top.At(0), nil, DupOptions{Recursive: true})
			if err != nil {
				t.Fatal(err)
			}
			for repeat := range 3 {
				if root.kids.ht != nil != hashed {
					t.Fatalf("dup %d table history differs", repeat)
				}
				if n, err := dst.findPath(&logger{set: dstSet}, "/m:r/s[.='3']"); n != nil || !errors.Is(err, errPartial) {
					t.Errorf("dup %d: predicate = %v, %v; want incomplete", repeat, n, err)
				}
				findNode(t, dst, "/m:r/l[k='3']") // inner list hashes are recomputed
				next := newTree(dstSet)
				root, _, err = next.Dup(root, nil, DupOptions{Recursive: true})
				if err != nil {
					t.Fatal(err)
				}
				dst = next
			}
			leaf := findNode(t, dst, "/m:r/s[1]")
			for _, value := range []string{"3", "4", "3"} {
				v, diag := types.Store(leaf.schema.Type, value, types.FormatJSON, types.HintData, nil, leaf.schema)
				if diag != nil {
					t.Fatal(diag.Msg)
				}
				changed, _ := dst.changeTermVal(leaf, v, false)
				_, err := dst.findPath(&logger{set: dstSet}, "/m:r/s[.='"+value+"']")
				if !changed && !errors.Is(err, errPartial) {
					t.Errorf("equal assignment erased stale hash: %v", err)
				}
				if changed && err != nil {
					t.Errorf("changed assignment did not refresh hash: %v", err)
				}
			}
		})
	}
}

// Issue #236 case 1: the hash includes every historical leading key, although
// tree_data.c:1758-1785 compares only the declared key positions after the hash gate.
func TestStoredHashRepeatedKeyDescendingMerge(t *testing.T) {
	set := pv2Set(t, "pv2-sorted")
	dst := newTree(set)
	for _, key := range []string{"m", "z"} {
		if _, err := dst.NewPath("/pv2-sorted:c/l[k='"+key+"']", "", NewPathOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	src, _, err := parseJSONString(set,
		`{"pv2-sorted:c":{"l":[{"k":"a","k":"c","v":"2"},{"k":"a","k":"b","v":"1"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	if n := dst.top.At(0).kids.SchemaLen(); n != 4 {
		t.Fatalf("merged %d instances, want 4", n)
	}
}

// tree_data_hash.c:181-245 never frees a live parent's table after shrinking.
// Recursive dup precreates it; a shallow ancestor copy starts without one.
func TestStoredHashChildrenTableHistory(t *testing.T) {
	set := storedHashSet(t, "string")
	tr, _, err := parseJSONString(set, `{"m:r":{"s":["3"]}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	r := tr.top.At(0)
	for i, name := range []string{"a", "b", "c"} {
		if _, err := tr.NewPath("/m:r/"+name, "x", NewPathOptions{}); err != nil {
			t.Fatal(err)
		}
		if (r.kids.ht != nil) != (i == 2) {
			t.Fatalf("table at %d children", i+2)
		}
	}
	for _, name := range []string{"a", "b", "c"} {
		_ = unlink(findNode(t, tr, "/m:r/"+name))
	}
	if r.kids.ht == nil {
		t.Fatal("shrink discarded table")
	}
	for _, recursive := range []bool{false, true} {
		dst := newTree(set)
		d, _, err := dst.Dup(r, nil, DupOptions{Recursive: recursive})
		if err != nil {
			t.Fatal(err)
		}
		if (d.kids.ht != nil) != recursive {
			t.Fatalf("recursive=%t table history", recursive)
		}
	}
	if _, err := tr.NewPath("/m:r/a", "again", NewPathOptions{}); err != nil {
		t.Fatal(err)
	}
	if r.kids.ht == nil {
		t.Fatal("regrow discarded table")
	}
}

// tree_data_hash.c:39-40 and 173-175 make both lyd_hash and lyd_insert_hash no-ops
// for an opaque node/parent. A top-level key copy can be moved under an opaque node,
// where changing its value must neither treat that parent as a list nor leave stale lookup state.
func TestStoredHashKeyUnderOpaqueParent(t *testing.T) {
	set := storedHashSet(t, "uint32")
	src, _, err := parseJSONString(set, `{"m:r":{"l":[{"k":3}]}}`, Reject, false)
	if err != nil {
		t.Fatal(err)
	}

	carrier := newTree(set)
	key, _, err := carrier.Dup(findNode(t, src, "/m:r/l[k='3']/k"), nil, DupOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dst := newTree(set)
	opaq := newOpaque(opaque{Name: "unknown", ModuleNS: "urn:unknown", Format: types.FormatJSON})
	mustLink(t, dst.insert(nil, opaq, insertDefault))
	if err := dst.Insert(opaq, key); err != nil {
		t.Fatal(err)
	}

	oldHash := key.hash
	if changed, err := key.SetValue("bad"); changed || err == nil {
		t.Fatalf("invalid SetValue = changed %t, error %v", changed, err)
	}
	if key.Value() != "3" || key.hash != oldHash {
		t.Fatalf("failed SetValue changed key: value=%q hash=%#x, want value 3 hash %#x", key.Value(), key.hash, oldHash)
	}
	if changed, err := key.SetValue("4"); !changed || err != nil {
		t.Fatalf("SetValue = changed %t, error %v", changed, err)
	}
	if opaq.keyN != 0 || opaq.lkeyN != 0 || opaq.lkeyOK {
		t.Fatalf("opaque parent acquired list-key state: keyN=%d lkeyN=%d ok=%t", opaq.keyN, opaq.lkeyN, opaq.lkeyOK)
	}
	if key.Value() != "4" || key.hash != oldHash || key.hashed || opaq.kids.ht != nil {
		t.Fatalf("changed key state: value=%q hash=%#x hashed=%t table=%v", key.Value(), key.hash, key.hashed, opaq.kids.ht)
	}
	if err := opaq.kids.checkRecords(); err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]*Node{"3": nil, "4": key} {
		v, d := types.StoreOnly(key.schema.Type, value, types.FormatJSON, types.HintData, nil, key.schema)
		if d != nil {
			t.Fatal(d.Msg)
		}
		if got := dst.findFirst(&opaq.kids, newTerm(key.schema, v)); got != want {
			t.Errorf("lookup %s = %p, want %p", value, got, want)
		}
	}
}
