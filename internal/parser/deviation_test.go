// SPDX-License-Identifier: BSD-3-Clause

package parser

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestDeviationRecords: every deviation of the ut-deviation corpus schemas becomes a typed
// record holding each of its deviates in order, with every substatement reachable.
func TestDeviationRecords(t *testing.T) {
	corpus := os.DirFS("../../conformance/corpus/ut-deviation")
	files, err := fs.Glob(corpus, "*/*.yang")
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpus schemas: %v", err)
	}
	devs, deviates := 0, 0
	for _, f := range files {
		src, err := fs.ReadFile(corpus, f)
		if err != nil {
			t.Fatal(err)
		}
		s, err := Parse(filepath.Base(f), src, nil)
		if err != nil {
			continue // a schema the fixture expects to fail parsing
		}
		m, err := Build(s)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if got := len(subs(s, "deviation")); got != len(m.Deviations) {
			t.Errorf("%s: %d deviation records for %d statements", f, len(m.Deviations), got)
		}
		for i, d := range m.Deviations {
			devs++
			ds := subs(d.Stmt, "deviate")
			if d.Stmt != subs(s, "deviation")[i] || d.Nodeid != d.Stmt.Arg || len(ds) != len(d.Deviates) {
				t.Errorf("%s: deviation %q does not mirror its statement", f, d.Nodeid)
				continue
			}
			for j, dv := range d.Deviates {
				deviates++
				if dv.Stmt != ds[j] || dv.Mod != ds[j].Arg {
					t.Errorf("%s: deviate %d of %q out of order", f, j, d.Nodeid)
				}
				checkDeviate(t, f, dv)
			}
		}
	}
	if devs < 80 || deviates < devs {
		t.Errorf("only %d deviations, %d deviates read", devs, deviates)
	}
}

// checkDeviate: each substatement of the deviate statement is in its record.
func checkDeviate(t *testing.T, f string, d *Deviate) {
	t.Helper()
	count := map[string]int{}
	for _, c := range d.Stmt.Subs {
		if c.ExtPrefix == "" {
			count[c.Keyword]++
		}
	}
	got := map[string]int{"units": n(d.Units != nil), "must": len(d.Musts), "unique": len(d.Uniques),
		"default": len(d.Defaults), "config": n(d.Config != nil), "mandatory": n(d.Mandatory != nil),
		"min-elements": n(d.MinElements != nil), "max-elements": n(d.MaxElements != nil), "type": n(d.Type != nil)}
	for kw, c := range count {
		if got[kw] != c {
			t.Errorf("%s: deviate %s has %d %s, the record %d", f, d.Mod, c, kw, got[kw])
		}
	}
	if len(d.Exts) != len(OwnedExts(d.Stmt)) {
		t.Errorf("%s: deviate %s extension instances not kept", f, d.Mod)
	}
}

func n(b bool) int {
	if b {
		return 1
	}
	return 0
}

func subs(s *Stmt, kw string) []*Stmt {
	var r []*Stmt
	for _, c := range s.Subs {
		if c.ExtPrefix == "" && c.Keyword == kw {
			r = append(r, c)
		}
	}
	return r
}

func TestDeviationTyped(t *testing.T) {
	m, err := parseBuild(`module d {
  yang-version 1.1; namespace "urn:d"; prefix d;
  import t { prefix t; }
  extension e { argument a; }
  deviation /t:c {
    description "dsc"; reference "ref";
    d:e x;
    deviate not-supported;
  }
  deviation /t:l {
    deviate add { units "km" { d:e u; } must "1" { error-message m; } unique "a b"; default x; default y;
      config false; mandatory true; min-elements 1; max-elements unbounded; d:e add; }
    deviate delete { units km; must "1"; unique "a b"; default x; d:e del; }
    deviate replace { type int8 { range "1..2"; } units mi; default z; config true; mandatory false;
      min-elements 0; max-elements 3; }
  }
}`)
	if err != nil {
		t.Fatal(err)
	}
	ns, l := m.Deviations[0], m.Deviations[1]
	add, del, rpl := l.Deviates[0], l.Deviates[1], l.Deviates[2]
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"deviation", ns.Nodeid == "/t:c" && ns.Description == "dsc" && ns.Reference == "ref" && len(ns.Exts) == 1},
		{"not-supported", len(ns.Deviates) == 1 && ns.Deviates[0].Mod == "not-supported"},
		{"add", add.Mod == "add" && *add.Units == "km" && add.Musts[0].ErrorMessage == "m" && add.Uniques[0] == "a b" &&
			len(add.Defaults) == 2 && !*add.Config && *add.Mandatory && *add.MinElements == 1 && *add.MaxElements == 0},
		{"add exts", len(add.Exts) == 2 && add.Exts[0].Arg == "u" && add.Exts[1].Arg == "add"},
		{"delete", del.Mod == "delete" && *del.Units == "km" && del.Musts[0].Arg == "1" && del.Defaults[0] == "x" &&
			len(del.Exts) == 1},
		{"replace", rpl.Mod == "replace" && rpl.Type.Name == "int8" && rpl.Type.Range.Arg == "1..2" && *rpl.Units == "mi" &&
			rpl.Defaults[0] == "z" && *rpl.Config && !*rpl.Mandatory && *rpl.MinElements == 0 && *rpl.MaxElements == 3},
	} {
		if !c.ok {
			t.Errorf("%s: wrong", c.name)
		}
	}
}
