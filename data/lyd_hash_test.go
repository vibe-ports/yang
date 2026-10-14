// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"testing"

	"github.com/vibe-ports/yang/internal/types"
)

func TestLydHashEmptyPayloads(t *testing.T) {
	const model = `module empty-hash {
  yang-version 1.1;
  namespace "urn:empty-hash";
  prefix eh;
  leaf-list item { type string; }
  list entry { key "a b"; leaf a { type string; } leaf b { type string; } }
	list keyless { config false; leaf value { type string; } }
}`
	setA, setB := inlineDiffSet(t, model), inlineDiffSet(t, model)
	const xml = `<item xmlns="urn:empty-hash"/><entry xmlns="urn:empty-hash"><a/><b/></entry>` +
		`<keyless xmlns="urn:empty-hash"><value>x</value></keyless>`
	a, b := parseOnlyXML(t, setA, xml), parseOnlyXML(t, setB, xml)
	for i, n := range a.top.list {
		h := types.HashMulti32(0, []byte("empty-hash"))
		h = types.HashMulti32(h, []byte(n.Name()))
		// One finalization per empty value, plus lyd_hash's final call
		// (tree_data_hash.c:60-81; hash_table.c:58-61).
		for range 1 + max(1, len(n.schema.Keys)) {
			h = types.FinishHash32(h)
		}
		got, ok := lydHash(a, n)
		other, otherOK := lydHash(b, b.top.list[i])
		if !ok || !otherOK || got != h || other != h || !lydHashInputsEqual(a, n, b.top.list[i]) {
			t.Fatalf("%s hashes: %#x, %#x; want %#x", n.Name(), got, other, h)
		}
	}
}

func TestLydHashKeylessFinalize(t *testing.T) {
	const model = `module keyless-finalize {
  yang-version 1.1;
  namespace "urn:keyless-finalize";
  prefix kf;
  container top {
    list l {
      config false;
      leaf k { type uint8; }
    }
  }
}`
	tr := parseOnlyXML(t, inlineDiffSet(t, model),
		`<top xmlns="urn:keyless-finalize"><l><k>0</k></l></top>`)
	n := topOf(tr).kids.list[0] // a keyless list has no path predicate to find it by

	want := types.HashMulti32(0, []byte("keyless-finalize"))
	want = types.HashMulti32(want, []byte("l"))
	want = types.FinishHash32(want) // keyless list's empty key part
	want = types.FinishHash32(want) // common finalization
	got, ok := lydHash(tr, n)
	if !ok || got != want {
		t.Fatalf("lydHash(keyless list) = (%#x, %t), want (%#x, true)", got, ok, want)
	}
}
