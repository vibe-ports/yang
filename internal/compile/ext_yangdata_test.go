// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"os"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// loadWith loads module name from the given sources plus the libyang test copy of
// ietf-restconf; it returns the compiled module or the load error.
func loadWith(t *testing.T, name string, files map[string]string) (*schema.Module, []Diagnostic, error) {
	t.Helper()
	rc, err := os.ReadFile("../../conformance/corpus/ut-ext/common/ietf-restconf@2017-01-26.yang")
	if err != nil {
		t.Fatal(err)
	}
	files["ietf-restconf@2017-01-26.yang"] = string(rc)
	h := newNodeHarness(t, Options{}, mapFS(files))
	m, loadDiags, diags, loadErr, err := h.loadFeatures(name, nil)
	if loadErr != nil {
		return nil, loadDiags, loadErr
	}
	return m, diags, err
}

// treeOf renders nodes as "name[kind,flags](children)" for the shape checks: c config,
// d deprecated, o obsolete, p presence.
func treeOf(ns []*schema.Node) string {
	s := ""
	for i, n := range ns {
		if i > 0 {
			s += " "
		}
		s += n.Name + "[" + nodetypeStr[n.Kind]
		if n.Config {
			s += ",c"
		}
		switch n.Status {
		case schema.Deprecated:
			s += ",d"
		case schema.Obsolete:
			s += ",o"
		}
		if n.Presence {
			s += ",p"
		}
		s += "]"
		if len(n.Children) > 0 {
			s += "(" + treeOf(n.Children) + ")"
		}
	}
	return s
}

// TestYangDataTree pins the compiled yang-data trees of test_yangdata.c test_schema (the libyang
// test compares them as compiled YANG text): config and if-feature ignored, uses expanded, two
// instances with the same node names beside a module node of that name.
func TestYangDataTree(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      []string
	}{
		{"a", `module a {yang-version 1.1; namespace urn:tests:extensions:yangdata:a; prefix self;` +
			`import ietf-restconf {revision-date 2017-01-26; prefix rc;}feature x;` +
			`rc:yang-data template { container x { list l { leaf x { type string;}} leaf y {if-feature x; type string; config false;}}}}`,
			[]string{"x[container](l[list](x[leaf]) y[leaf])"}},
		{"c", `module c {yang-version 1.1; namespace urn:tests:extensions:yangdata:c; prefix self;` +
			`import ietf-restconf {revision-date 2017-01-26; prefix rc;}` +
			`grouping g { choice ch { container a {presence a; config false;} container b {presence b; config true;}}}` +
			`rc:yang-data template { uses g;}}`,
			[]string{"ch[choice](a[case](a[container,p]) b[case](b[container,p]))"}},
		{"d", `module d {yang-version 1.1; namespace urn:tests:extensions:yangdata:d; prefix self;` +
			`import ietf-restconf {revision-date 2017-01-26; prefix rc;}leaf d { type string;}` +
			`rc:yang-data template1 { container d {presence d;}}rc:yang-data template2 { container d {presence d;}}}`,
			[]string{"d[container,p]", "d[container,p]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, diags, err := loadWith(t, tc.name, map[string]string{tc.name + ".yang": tc.src})
			if err != nil {
				t.Fatalf("%v %v", err, diags)
			}
			if len(m.Exts) != len(tc.want) {
				t.Fatalf("%d instances", len(m.Exts))
			}
			for i, e := range m.Exts {
				if got := treeOf(e.Nodes); got != tc.want[i] || e.Plugin != schema.PluginYangData || e.Root != nil {
					t.Errorf("instance %d: %s (plugin %q)", i, got, e.Plugin)
				}
				for _, n := range e.Nodes {
					if n.Parent != nil || n.Module != m {
						t.Errorf("%s: parent %v module %v", n.Name, n.Parent, n.Module)
					}
				}
			}
		})
	}
}

// TestYangDataRestconf: the yang-data instances of ietf-restconf itself compile their groupings.
func TestYangDataRestconf(t *testing.T) {
	m, diags, err := loadWith(t, "ietf-restconf", map[string]string{})
	if err != nil {
		t.Fatalf("%v %v", err, diags)
	}
	var got []string
	for _, e := range m.Exts {
		got = append(got, e.Argument+":"+e.Nodes[0].Name)
	}
	if len(got) != 2 || got[0] != "yang-errors:errors" || got[1] != "yang-api:restconf" {
		t.Fatalf("instances %v", got)
	}
	// lys_find_child_node_ext through yangdata_snode_xpath
	if n, e := schema.FindExtNode(nil, m, m, "restconf", true); n != m.Exts[1].Nodes[0] || e != m.Exts[1] {
		t.Errorf("FindExtNode: %v %v", n, e)
	}
}
