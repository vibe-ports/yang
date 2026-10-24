// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"runtime"
	"testing"
	"testing/fstest"
	"weak"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
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
