// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestFindMetaDetachedContext pins lyd_find_meta's context lookup and module-identity match
// (libyang v5.8.6 src/tree_data.c:3137-3179) across lyd_free_tree.
func TestFindMetaDetachedContext(t *testing.T) {
	set := &schema.Set{}
	dataMod := &schema.Module{Name: "data", Owner: set}
	old := &schema.Module{Name: "annotations", Revision: "2020-01-01", Owner: set, Implemented: true}
	latest := &schema.Module{Name: "annotations", Revision: "2021-01-01", Owner: set}
	set.Modules = []*schema.Module{dataMod, old, latest}

	n := newInner(&schema.Node{Kind: schema.Container, Name: "c", Module: dataMod})
	n.meta = []*meta{{mod: old, name: "note"}, {mod: latest, name: "note"}}
	tr := newTree(set)
	if err := tr.insert(nil, n, insertDefault); err != nil {
		t.Fatal(err)
	}

	findRevision := func() string {
		t.Helper()
		m, err := n.FindMeta("annotations:note")
		if err != nil || m == nil {
			t.Fatalf("FindMeta annotations:note: %v %v", m, err)
		}
		return m.Module().Revision()
	}
	if got := findRevision(); got != latest.Revision {
		t.Fatalf("attached revision %q, want %q", got, latest.Revision)
	}

	type result struct {
		found bool
		rc    string
		msg   string
	}
	find := func(name string) result {
		t.Helper()
		m, err := n.FindMeta(name)
		if err == nil {
			return result{found: m != nil}
		}
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("FindMeta(%q): %T %v", name, err, err)
		}
		return result{rc: ve.RC(), msg: ve.Error()}
	}
	names := []string{"annotations:note", "annotations:a b", "unknown:note", "note"}
	attached := make(map[string]result, len(names))
	for _, name := range names {
		attached[name] = find(name)
	}

	if err := n.Remove(); err != nil {
		t.Fatal(err)
	}
	if got := findRevision(); got != latest.Revision {
		t.Fatalf("detached revision %q, want %q", got, latest.Revision)
	}
	for _, name := range names {
		if got := find(name); got != attached[name] {
			t.Errorf("FindMeta(%q) detached = %+v, attached = %+v", name, got, attached[name])
		}
	}
	if got := find("annotations:a b"); got.rc != "LY_EINVAL" || got.msg != `Metadata name "a b" is not valid.` {
		t.Errorf("invalid name: %+v", got)
	}
	if got := find("unknown:note"); got.rc != "LY_EINVAL" || got.msg != `Module "unknown" not found.` {
		t.Errorf("unknown module: %+v", got)
	}
}

// lyd_find_meta uses the owner of the first metadata in its input list, which may differ from
// the data node's schema context after a cross-context diff merge.
func TestFindMetaForeignFirstMetadata(t *testing.T) {
	a, b := &schema.Set{}, &schema.Set{}
	dataMod := &schema.Module{Name: "data", Owner: a}
	ma := &schema.Module{Name: "annotations", Owner: a, Implemented: true}
	mb := &schema.Module{Name: "annotations", Owner: b, Implemented: true}
	a.Modules = []*schema.Module{dataMod, ma}
	b.Modules = []*schema.Module{mb}
	n := newInner(&schema.Node{Kind: schema.Container, Name: "c", Module: dataMod})
	n.meta = []*meta{{mod: ma, name: "note"}, {mod: mb, name: "note"}}
	tr := newTree(a)
	if err := tr.insert(nil, n, insertDefault); err != nil {
		t.Fatal(err)
	}
	if m, err := n.FindMeta("annotations:note"); err != nil || m == nil || m.m.mod != ma {
		t.Fatalf("first metadata context: %v, %v", m, err)
	}
	removeMeta(n, n.meta[0])
	if err := n.Unlink(); err != nil {
		t.Fatal(err)
	}
	if m, err := n.FindMeta("annotations:note"); err != nil || m == nil || m.m.mod != mb {
		t.Fatalf("foreign first metadata after unlink: %v, %v", m, err)
	}
	for _, m := range metasNamedLatest(n, "annotations", "note") {
		if m.mod != mb {
			t.Fatalf("metasNamedLatest returned module %p, want %p", m.mod, mb)
		}
		return
	}
	t.Fatal("metasNamedLatest missed foreign metadata")
}
