// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// structA is module a of test_structure.c test_schema.
const structA = `module a {yang-version 1.1; namespace urn:tests:extensions:structure:a; prefix a;` +
	`import ietf-yang-structure-ext {prefix sx;}` +
	`sx:structure struct {  must "/n2/l";  status deprecated;  description desc;  reference no-ref;` +
	`  typedef my-type {type string;}  grouping my-grp {leaf gl {type my-type;}}` +
	`  container n1 {leaf l {config false; type uint32;}}  list n2 {leaf l {type leafref {path /n1/l;}}}` +
	`  uses my-grp;}}`

// TestStructureTree pins the compiled structure tree of test_structure.c test_schema (the
// libyang test compares it as compiled YANG text): a container named by the argument holds the
// must and status, its nodes inherit the status and have no config, the structure's typedef
// and grouping are in scope, and the leafref path resolves into the structure.
func TestStructureTree(t *testing.T) {
	h := newNodeHarness(t, Options{}, mapFS(map[string]string{"a.yang": structA}))
	m, diags, loadErr, err := h.load("a")
	if loadErr != nil || err != nil {
		t.Fatalf("%v %v %v", loadErr, err, diags)
	}
	if len(m.Exts) != 1 || len(m.Top) != 0 {
		t.Fatalf("exts %d top %d", len(m.Exts), len(m.Top))
	}
	e := m.Exts[0]
	r := e.Root
	if e.Plugin != schema.PluginStructure || r == nil || r.Name != "struct" || r.Kind != schema.Container ||
		!r.Config || r.Status != schema.Deprecated || len(r.Musts) != 1 || r.Musts[0].Src != "/n2/l" || r.Parent != nil {
		t.Fatalf("root %+v", r)
	}
	if got, want := treeOf(e.DataNodes()), "n1[container,d](l[leaf,d]) n2[list,d](l[leaf,d]) gl[leaf,d]"; got != want {
		t.Errorf("tree %s, want %s", got, want)
	}
	n1, n2, gl := r.Children[0], r.Children[1], r.Children[2]
	if gl.Type.Base != schema.String || n1.Parent != r {
		t.Errorf("gl type %v, n1 parent %v", gl.Type.Base, n1.Parent)
	}
	lref := n2.Children[0].Type
	p, ok := lref.PathCompiled.(types.Path)
	if lref.Base != schema.Leafref || !ok || p[len(p)-1].Node != n1.Children[0] || lref.Realtype.Base != schema.Uint32 {
		t.Errorf("leafref %+v", lref)
	}
	// lys_find_child_node_ext through structure_snode_xpath and structure_snode
	if n, x := schema.FindExtNode(nil, m, m, "n1", true); n != n1 || x != e {
		t.Errorf("snode_xpath n1: %v", n)
	}
	if n, _ := schema.FindExtNode(nil, m, nil, "struct", false); n != r {
		t.Errorf("snode struct: %v", n)
	}
}

// TestStructureEmpty: a structure without substatements is an empty container.
func TestStructureEmpty(t *testing.T) {
	h := newNodeHarness(t, Options{}, mapFS(map[string]string{"c.yang": `module c {yang-version 1.1; ` +
		`namespace urn:tests:extensions:structure:c; prefix c;import ietf-yang-structure-ext {prefix sx;}sx:structure struct;}`}))
	m, diags, loadErr, err := h.load("c")
	if loadErr != nil || err != nil {
		t.Fatalf("%v %v %v", loadErr, err, diags)
	}
	if e := m.Exts[0]; e.Root == nil || e.Root.Name != "struct" || len(e.DataNodes()) != 0 {
		t.Errorf("instance %+v", e)
	}
}
