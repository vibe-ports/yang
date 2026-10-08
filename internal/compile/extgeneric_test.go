// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// treeSubs is the substatement set of the test plugin: what structure declares, in its order.
var treeSubs = []parser.ExtSubstmt{{Keyword: "must", Many: true}, {Keyword: "status"}, {Keyword: "description"},
	{Keyword: "reference"}, {Keyword: "typedef", Many: true}, {Keyword: "grouping", Many: true},
	{Keyword: "container", Many: true}, {Keyword: "leaf", Many: true}, {Keyword: "leaf-list", Many: true},
	{Keyword: "list", Many: true}, {Keyword: "choice", Many: true}, {Keyword: "anydata", Many: true},
	{Keyword: "anyxml", Many: true}, {Keyword: "uses", Many: true}}

// withTreePlugin registers, for the test, plugins of extensions "flat" (nodes without a parent,
// as yang-data) and "boxed" (nodes under a container named by the argument, as structure) of
// module ext-def@2020-01-01, both with the generic substatement table.
func withTreePlugin(t *testing.T) {
	csubs := make([]extCSubstmt, len(treeSubs))
	for i, s := range treeSubs {
		csubs[i] = extCSubstmt{kw: s.Keyword, store: s.Keyword != "typedef" && s.Keyword != "grouping"}
	}
	parse := func(c *Context, x *extParse) error { _, err := c.parseExtInstance(x, treeSubs); return err }
	flat := func(w *nodeCtx, e *parser.Stmt, inst *schema.ExtInstance, _ *schema.Node) error {
		prev := w.opts
		w.opts |= optNoConfig | optNoDisabled
		defer func() { w.opts = prev }()
		return w.compileExtInstance(e, treeSubs, csubs, inst, nil)
	}
	boxed := func(w *nodeCtx, e *parser.Stmt, inst *schema.ExtInstance, _ *schema.Node) error {
		inst.Root = &schema.Node{Kind: schema.Container, Name: inst.Argument, Module: w.cur}
		prev := w.opts
		w.opts |= optNoConfig | optNoDisabled
		defer func() { w.opts = prev }()
		return w.compileExtInstance(e, treeSubs, csubs, inst, inst.Root)
	}
	saved := extPlugins
	extPlugins = append(extPlugins[:len(extPlugins):len(extPlugins)],
		extPlugin{"ext-def", "2020-01-01", "flat", "test flat", parse, flat},
		extPlugin{"ext-def", "2020-01-01", "boxed", "test boxed", parse, boxed})
	t.Cleanup(func() { extPlugins = saved })
}

const extDef = `module ext-def { namespace urn:ext-def; prefix d; revision 2020-01-01;
  extension flat { argument name; } extension boxed { argument name; } }`

func loadTree(t *testing.T, src string) (*schema.Module, []Diagnostic, error) {
	t.Helper()
	withTreePlugin(t)
	h := newNodeHarness(t, Options{}, mapFS(map[string]string{"ext-def.yang": extDef, "m.yang": src}))
	m, loadDiags, diags, loadErr, err := h.loadFeatures("m", nil)
	if loadErr != nil {
		return nil, loadDiags, loadErr
	}
	return m, diags, err
}

func names(ns []*schema.Node) string {
	var b []string
	for _, n := range ns {
		s := n.Name
		if len(n.Children) > 0 {
			s += "(" + names(n.Children) + ")"
		}
		b = append(b, s)
	}
	return strings.Join(b, " ")
}

// TestExtInstanceTree: the generic substatement compile (lyplg_ext_compile_extension_instance)
// puts an instance's nodes in its own top level, in the plugin's substatement order; groupings
// and typedefs of the instance are in scope; the instance's status is inherited; if-feature does
// not disable (LYS_COMPILE_NO_DISABLED) and config is ignored (LYS_COMPILE_NO_CONFIG); the
// instance's names do not collide with the module's.
func TestExtInstanceTree(t *testing.T) {
	m, diags, err := loadTree(t, `module m { yang-version 1.1; namespace urn:m; prefix m; import ext-def { prefix d; }
  feature f;
  leaf top { type string; }
  d:flat t1 {
    status deprecated;
    typedef my { type uint8; }
    grouping g { leaf gl { type my; } }
    leaf top { type string; if-feature f; config false; }
    uses g;
    container c { leaf x { type my; } }
  }
  d:boxed t2 { must "c"; container c { list l { key k; leaf k { type string; } } } leaf top { type int8; } }
}`)
	if err != nil {
		t.Fatalf("load: %v %v", err, diags)
	}
	if len(m.Exts) != 2 {
		t.Fatalf("exts %d", len(m.Exts))
	}
	flat, boxed := m.Exts[0], m.Exts[1]
	if got := names(flat.Nodes); got != "c(x) top gl" {
		t.Errorf("flat nodes %q", got)
	}
	if flat.Plugin != "test flat" || flat.Module != m || flat.Root != nil {
		t.Errorf("flat instance %+v", flat)
	}
	for _, n := range []*schema.Node{flat.Nodes[0], flat.Nodes[0].Children[0], flat.Nodes[1], flat.Nodes[2]} {
		if n.Status != schema.Deprecated || n.Config {
			t.Errorf("%s: status %v config %v", n.Name, n.Status, n.Config)
		}
	}
	if x := flat.Nodes[0].Children[0]; x.Type == nil || x.Type.Base != schema.Uint8 || flat.Nodes[0].Parent != nil {
		t.Errorf("typedef from the instance: %+v", x.Type)
	}
	if boxed.Root == nil || boxed.Root.Name != "t2" || names(boxed.DataNodes()) != "c(l(k)) top" ||
		len(boxed.Root.Musts) != 1 || boxed.Root.Musts[0].Src != "c" || boxed.DataNodes()[0].Parent != boxed.Root {
		t.Errorf("boxed instance %+v", boxed.Root)
	}
	if got := boxed.DataNodes()[0].Children[0].LogPath(); got != "/m:t2/c/l" {
		t.Errorf("log path %q", got)
	}
	if len(m.Top) != 1 || m.Top[0].Name != "top" {
		t.Errorf("module top %v", names(m.Top))
	}
	// lys_find_child_node_ext: these test plugins have no snode callbacks
	if n, e := schema.FindExtNode(nil, m, m, "top", true); n != nil || e != nil {
		t.Errorf("found %v in %v", n, e)
	}
}

// TestExtInstanceErrors: the diagnostics of the generic plumbing.
func TestExtInstanceErrors(t *testing.T) {
	for _, tc := range []struct{ name, body, path, msg string }{
		{"dup", `d:flat t { leaf a { type string; } container a; }`,
			"/m:{ext-inst='d:flat'}/t/m:a", `Duplicate identifier "/m:a" of data definition/RPC/action/notification statement.`},
		{"dup-boxed", `d:boxed t { leaf a { type string; } container a; }`,
			"/m:{ext-inst='d:boxed'}/t/a", `Duplicate identifier "/m:t/a" of data definition/RPC/action/notification statement.`},
		{"keyword", `d:flat t { rpc r; }`,
			"/m:{ext-inst='d:flat'}/t", `Invalid keyword "rpc" as a child of "d:flat t" extension instance.`},
		{"grouping", `d:flat t { uses nope; }`,
			"/m:{ext-inst='d:flat'}/t/{uses='nope'}", `Grouping "nope" referenced by a uses statement not found.`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, diags, err := loadTree(t, `module m { namespace urn:m; prefix m; import ext-def { prefix d; } `+tc.body+` }`)
			if err == nil || len(diags) == 0 {
				t.Fatalf("err %v diags %v", err, diags)
			}
			if d := diags[len(diags)-1]; d.SchemaPath != tc.path || d.Msg != tc.msg {
				t.Errorf("got %q %q", d.SchemaPath, d.Msg)
			}
		})
	}
}

// TestFindExtNode: lys_find_child_node_ext with the yang-data and structure snode callbacks.
func TestFindExtNode(t *testing.T) {
	m := &schema.Module{Name: "m", Implemented: true}
	o := &schema.Module{Name: "o", Implemented: true}
	leaf := func(name string, p *schema.Node) *schema.Node {
		return &schema.Node{Kind: schema.Leaf, Name: name, Module: m, Parent: p}
	}
	yd := &schema.ExtInstance{Plugin: schema.PluginYangData, Module: m, Nodes: []*schema.Node{leaf("a", nil), leaf("b", nil)}}
	root := &schema.Node{Kind: schema.Container, Name: "s", Module: m}
	ch := &schema.Node{Kind: schema.Choice, Name: "ch", Module: m, Parent: root}
	cs := &schema.Node{Kind: schema.Case, Name: "cs", Module: m, Parent: ch}
	inCase := leaf("in", cs)
	cs.Children, ch.Children, root.Children = []*schema.Node{inCase}, []*schema.Node{cs}, []*schema.Node{ch}
	st := &schema.ExtInstance{Plugin: schema.PluginStructure, Module: m, Root: root}
	m.Exts = []*schema.ExtInstance{yd, st}
	for _, tc := range []struct {
		mod, prefix *schema.Module
		name        string
		xpath       bool
		want        *schema.Node
	}{
		{m, nil, "b", true, yd.Nodes[1]},
		{m, nil, "", true, yd.Nodes[0]},
		{m, m, "in", true, inCase}, // structure_snode_xpath enters choice and case
		{m, o, "in", true, nil},    // prefix of another module
		{m, nil, "s", false, root}, // structure_snode: the container
		{nil, m, "a", false, yd.Nodes[0]},
		{o, nil, "a", true, nil},
		{nil, nil, "s", false, root}, // unprefixed (JSON): the module of the parent
	} {
		var sparent *schema.Node
		if tc.mod == nil && tc.prefix == nil {
			sparent = &schema.Node{Kind: schema.Container, Name: "p", Module: m}
		}
		if n, _ := schema.FindExtNode(sparent, tc.mod, tc.prefix, tc.name, tc.xpath); n != tc.want {
			t.Errorf("%+v: got %v", tc, n)
		}
	}
}

// TestExtInstanceTypedefDup: the typedefs of nodes in an extension instance get the collision
// checks of the scoped typedefs (lysp_stmt_typedef records their parents in tpdfs_nodes).
func TestExtInstanceTypedefDup(t *testing.T) {
	for _, tc := range []struct{ body, msg string }{
		{`d:flat t { container c { typedef x { type string; } typedef x { type int8; } } }`,
			`Duplicate identifier "x" of typedef statement - name collision with sibling type.`},
		{`d:flat t { container c { typedef x { type string; } container d { typedef x { type int8; } } } }`,
			`Duplicate identifier "x" of typedef statement - name collision with another scoped type.`},
		{`typedef x { type string; } d:flat t { list l { typedef x { type int8; } } }`,
			`Duplicate identifier "x" of typedef statement - scoped type collide with a top-level type.`},
		{`d:flat t { container c { typedef string { type int8; } } }`,
			`Duplicate identifier "string" of typedef statement - name collision with a built-in type.`},
	} {
		_, diags, err := loadTree(t, `module m { namespace urn:m; prefix m; import ext-def { prefix d; } `+tc.body+` }`)
		if err == nil || len(diags) == 0 || diags[0].Msg != tc.msg {
			t.Errorf("%s: err %v diags %v", tc.body, err, diags)
		}
	}
	// the instance's own typedefs, and those of its groupings, are not checked (libyang)
	if _, diags, err := loadTree(t, `module m { namespace urn:m; prefix m; import ext-def { prefix d; }
  typedef x { type string; } d:flat t { typedef x { type int8; } grouping g { typedef x { type int8; } leaf l { type x; } } leaf k { type x; } } }`); err != nil {
		t.Errorf("unchecked scopes: %v %v", err, diags)
	}
}
