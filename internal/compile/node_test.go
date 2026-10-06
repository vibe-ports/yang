// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang/internal/schema"
)

// nodeHarness drives compileNodes the way the dep-set loop of design 06 C1b will: load with the
// C1a loader, bind a schema.Module to every loaded module, compile the requested one.
type nodeHarness struct{ c *Context }

func newNodeHarness(t *testing.T, opts Options, dir fs.FS) *nodeHarness {
	t.Helper()
	c, _, err := NewContext(opts, dir)
	if err != nil {
		t.Fatal(err)
	}
	return &nodeHarness{c}
}

func mapFS(files map[string]string) fs.FS {
	fsys := fstest.MapFS{}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	return fsys
}

// load parses name with the loader and compiles its data nodes. loadErr reports a parse-phase
// failure; diags are the compile diagnostics only.
func (h *nodeHarness) load(name string) (mod *schema.Module, diags []Diagnostic, loadErr, err error) {
	m, _, loadErr := h.c.Load(name, "", nil)
	if loadErr != nil {
		return nil, nil, loadErr, nil
	}
	for _, lm := range h.c.Modules {
		if lm.mod == nil {
			v := schema.Version1
			if lm.Parsed.Version == "1.1" {
				v = schema.Version11
			}
			lm.mod = &schema.Module{Name: lm.Name, Revision: lm.Revision, Namespace: lm.Namespace,
				Prefix: lm.Parsed.Prefix, Version: v}
		}
		lm.mod.Implemented = lm.Implemented
	}
	h.c.diags = nil
	err = h.c.compileNodes(m, m.mod)
	return m.mod, h.c.diags, nil, err
}

// augmented reports whether a module outside libyang's internal ones has an augment.
func (h *nodeHarness) augmented() bool {
	for _, m := range h.c.Modules[len(internalModules):] {
		if len(m.Parsed.Augments) > 0 {
			return true
		}
		for _, inc := range m.Includes {
			if inc.Sub != nil && len(inc.Sub.Parsed.Augments) > 0 {
				return true
			}
		}
	}
	return false
}

// gNode is the part of the oracle's schema_tree entry (lyoracle.c snode_cb) that C4a decides.
type gNode struct {
	Path      string   `json:"path"`
	Nodetype  string   `json:"nodetype"`
	Module    string   `json:"module"`
	Config    *bool    `json:"config"`
	Status    string   `json:"status"`
	Mandatory *bool    `json:"mandatory"`
	Presence  *bool    `json:"presence"`
	OrderedBy *string  `json:"ordered_by"`
	Keys      []string `json:"keys"`
	Min       *uint32  `json:"min_elements"`
	Max       *uint32  `json:"max_elements"`
	Defaults  []string `json:"defaults"` // choice only here (leaf defaults are unres, C7)
}

var oracleKinds = map[schema.Kind]string{
	schema.Container: "container", schema.Choice: "choice", schema.Case: "case", schema.Leaf: "leaf",
	schema.LeafList: "leaflist", schema.List: "list", schema.AnyData: "anydata", schema.AnyXML: "anyxml",
	schema.RPC: "rpc", schema.Action: "action", schema.Notification: "notif", schema.Input: "input",
	schema.Output: "output",
}

// dumpTree is lysc_module_dfs_full with snode_cb: a node, its actions and notifications
// subtrees, then its children.
func dumpTree(m *schema.Module) []gNode {
	var out []gNode
	var dfs func(n *schema.Node)
	dfs = func(n *schema.Node) {
		g := gNode{Path: n.LogPath(), Nodetype: oracleKinds[n.Kind], Module: n.Module.Name,
			Status: [...]string{"current", "deprecated", "obsolete"}[n.Status]}
		noConfig := false
		for p := n; p != nil; p = p.Parent {
			switch p.Kind {
			case schema.RPC, schema.Action, schema.Notification, schema.Input, schema.Output:
				noConfig = true
			}
		}
		if !noConfig {
			g.Config = &n.Config
		}
		switch n.Kind {
		case schema.Leaf, schema.LeafList, schema.List, schema.Choice, schema.AnyData, schema.AnyXML, schema.Container:
			g.Mandatory = &n.Mandatory
		}
		if n.Kind == schema.Container {
			g.Presence = &n.Presence
		}
		if n.Kind == schema.List || n.Kind == schema.LeafList {
			s := "system"
			if n.UserOrdered {
				s = "user"
			}
			g.OrderedBy = &s
			lo, hi := n.Min, n.Max
			g.Min = &lo
			if hi != 0 {
				g.Max = &hi
			}
		}
		if n.Kind == schema.List {
			g.Keys = []string{}
			for _, k := range n.Keys {
				g.Keys = append(g.Keys, k.Name)
			}
		}
		if n.DefaultCase != nil {
			g.Defaults = []string{n.DefaultCase.Name}
		}
		out = append(out, g)
		for _, a := range n.Actions {
			dfs(a)
		}
		for _, a := range n.Notifs {
			dfs(a)
		}
		for _, c := range n.Children {
			dfs(c)
		}
	}
	for _, n := range m.Top {
		dfs(n)
	}
	return out
}

// c4aMessages are the compile errors this PR can report; a golden failing with another error
// is decided by a later PR (if-feature, status, types, unres...) and is skipped.
var c4aMessages = []string{
	"Duplicate identifier ", "Configuration node cannot be child", "Status \"", "Inherited schema-only status",
	"Action \"", "Notification \"", "Invalid mandatory leaf with a default value.",
	"Leaf-list default values are allowed only", "The default statement is present on leaf-list",
	"Leaf-list min-elements", "List min-elements", "Missing key in list", "The list's key ",
	"Duplicated key identifier", "Key of a configuration list", "List key of the \"empty\" type",
	"List's key must not have", "Unique's descendant-schema-nodeid", "Unique statement ",
	"Invalid descendant-schema-nodeid", "Default case ", "Mandatory node \"", "Invalid mandatory choice",
	"Leaf-list of type \"empty\"",
}

type manifestEntry struct {
	modules []string
	skip    string // why the request is outside this harness
}

// compileManifest reads the request of every fixture in dir compile from manifest.yaml (a line
// scan: the root module has no YAML dependency).
func compileManifest(t *testing.T) map[string]manifestEntry {
	t.Helper()
	f, err := os.Open(filepath.Join(corpus, "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	out := map[string]manifestEntry{}
	var cur manifestEntry
	inCompile := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(l, "- id:"):
			cur, inCompile = manifestEntry{}, false
		case l == "dir: compile":
			inCompile = true
		case strings.HasPrefix(l, "context_options:") && l != "context_options: []":
			cur.skip = "context options"
		case strings.HasPrefix(l, "modules:"):
			var mods []map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(l, "modules:")), &mods); err != nil {
				cur.skip = "modules: " + err.Error()
				continue
			}
			for _, m := range mods {
				if len(m) != 1 {
					cur.skip = "module revision/features"
				}
				name, _ := m["name"].(string)
				cur.modules = append(cur.modules, name)
			}
		case strings.HasPrefix(l, "golden:") && inCompile:
			out[strings.TrimSpace(strings.TrimPrefix(l, "golden:"))] = cur
		}
	}
	return out
}

type nodeGolden struct {
	Modules []struct {
		Name        string                 `json:"name"`
		Accepted    bool                   `json:"accepted"`
		Phase       string                 `json:"phase"`
		Rc          *struct{ Name string } `json:"rc"`
		Diagnostics []goldenDiag           `json:"diagnostics"`
		Tree        *[]gNode               `json:"schema_tree"`
	} `json:"modules"`
}

// TestNodeGoldens replays the conformance/corpus/compile goldens through the node walk: per
// requested module the verdict, the compile errors (message, code, path, rc) and the schema
// tree fields C4a decides. Fixtures needing later PRs (uses, augments, if-feature, obsolete,
// extensions, non-built-in types: ErrUnsupported; or an error only a later PR reports) skip.
func TestNodeGoldens(t *testing.T) {
	dir := filepath.Join(corpus, "compile")
	goldens, _ := filepath.Glob(filepath.Join(dir, "golden", "*.json"))
	if len(goldens) == 0 {
		t.Skip("no compile fixtures")
	}
	manifest := compileManifest(t)
	ran := 0
	for _, gf := range goldens {
		id := strings.TrimSuffix(filepath.Base(gf), ".json")
		t.Run(id, func(t *testing.T) {
			req, ok := manifest["golden/"+id+".json"]
			switch {
			case !ok:
				t.Skip("not in the manifest")
			case req.skip != "":
				t.Skip(req.skip)
			}
			b, err := os.ReadFile(gf) //nolint:gosec // corpus path
			if err != nil {
				t.Fatal(err)
			}
			var g nodeGolden
			if err := json.Unmarshal(b, &g); err != nil {
				t.Fatal(err)
			}
			schemas := os.DirFS(filepath.Join(dir, "schemas"))
			pre := newNodeHarness(t, Options{}, schemas)
			for _, gm := range g.Modules {
				_, _, _ = pre.c.Load(gm.Name, "", nil)
			}
			if pre.augmented() {
				t.Skip("augments are applied by design 06 C6")
			}
			h := newNodeHarness(t, Options{}, schemas)
			for _, gm := range g.Modules {
				mod, diags, loadErr, err := h.load(gm.Name)
				switch {
				case loadErr != nil && !gm.Accepted && gm.Phase == "parse":
					return // the parse phase is C1a's
				case loadErr != nil:
					t.Fatalf("%s: load: %v", gm.Name, loadErr)
				case gm.Phase == "parse":
					t.Skip("parse-phase failure the C1a loader does not report yet (C1b, C4b)")
				case errors.Is(err, ErrUnsupported):
					t.Skip(err)
				case h.augmented():
					t.Skip("augments are applied by design 06 C6")
				}
				var want []goldenDiag
				for _, d := range gm.Diagnostics {
					if d.Level == "error" {
						want = append(want, d)
					}
				}
				if !gm.Accepted {
					if !matchesAny(want[0].Msg, c4aMessages) || strings.Contains(want[0].SchemaPath, "{grouping=") {
						t.Skipf("first error is a later PR's: %s", want[0].Msg)
					}
					var got []goldenDiag
					for _, d := range diags {
						d.Phase = "compile"
						got = append(got, d.golden())
					}
					if err == nil || rcName(err) != gm.Rc.Name || !reflect.DeepEqual(got, want) {
						t.Fatalf("%s: got %v %+v\nwant %s %+v", gm.Name, err, got, gm.Rc.Name, want)
					}
					ran++
					return
				}
				if err != nil {
					t.Fatalf("%s: %v %+v, golden accepted", gm.Name, err, diags)
				}
				if gm.Tree == nil {
					continue // no schema dump in this request
				}
				wantTree := *gm.Tree
				for i := range wantTree {
					if wantTree[i].Nodetype != "choice" {
						wantTree[i].Defaults = nil
					}
				}
				if got := dumpTree(mod); len(got)+len(wantTree) > 0 && !reflect.DeepEqual(got, wantTree) {
					gj, _ := json.MarshalIndent(got, "", " ")
					wj, _ := json.MarshalIndent(wantTree, "", " ")
					t.Fatalf("%s tree:\n got  %s\n want %s", gm.Name, gj, wj)
				}
				ran++
			}
		})
	}
	t.Logf("%d of %d compile fixtures replayed", ran, len(goldens))
}

// TestConnectOrder: children added by augments go after the module's own children, grouped per
// augmenting module in module-name order, whatever the connect order (SCN:2330-2370; observed
// in order/two-augmenters: own, a:aa, z:zz).
func TestConnectOrder(t *testing.T) {
	base, a, z := &schema.Module{Name: "base"}, &schema.Module{Name: "a"}, &schema.Module{Name: "z"}
	w := &nodeCtx{cur: base, fl: map[*schema.Node]int{}}
	parent := &schema.Node{Kind: schema.Container, Name: "c", Module: base}
	for _, n := range []*schema.Node{
		{Kind: schema.Leaf, Name: "own1", Module: base}, {Kind: schema.Leaf, Name: "zz", Module: z},
		{Kind: schema.Leaf, Name: "aa", Module: a}, {Kind: schema.Leaf, Name: "own2", Module: base},
		{Kind: schema.Leaf, Name: "zz2", Module: z},
	} {
		if err := w.connect(parent, n); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for _, n := range parent.Children {
		got = append(got, n.Name)
	}
	if want := []string{"own1", "own2", "aa", "zz", "zz2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestUpdatePath: lysc_update_path segments, special tags and LYSC_CTX_BUFSIZE truncation.
func TestUpdatePath(t *testing.T) {
	m, o := &schema.Module{Name: "m"}, &schema.Module{Name: "o"}
	var p cpath
	p.init(m)
	steps := []struct {
		mod  *schema.Module
		name string // "" pops
		want string
	}{
		{nil, "c", "/m:c"},
		{m, "l", "/m:c/l"},
		{o, "x", "/m:c/l/m:x"}, // a parent of another module: cur_mod's prefix
		{nil, "", "/m:c/l"},
		{nil, "{uses}", "/m:c/l/{uses}"},
		{nil, "g", "/m:c/l/{uses='g'}"},
		{m, "k", "/m:c/l/{uses='g'}/k"},
		{nil, "", "/m:c/l/{uses='g'}"},
		{nil, "", "/m:c/l/{uses}"},
		{nil, "", "/m:c/l"},
		{nil, "", "/m:c"},
		{nil, "", "/"},
		{nil, "{augment}", "/m:{augment}"},
		{nil, "/x:y", "/m:{augment='/x:y'}"},
		{nil, "", "/m:{augment}"},
		{nil, "", "/"},
	}
	for i, s := range steps {
		if s.name == "" {
			p.pop()
		} else {
			p.update(s.mod, s.name)
		}
		if got := p.String(); got != s.want {
			t.Fatalf("step %d: %q, want %q", i, got, s.want)
		}
	}
	long := strings.Repeat("x", lyscCtxBufsize)
	p.update(m, long)
	if got := p.String(); len(got) != lyscCtxBufsize-1 || !strings.HasPrefix(got, "/xxx") {
		t.Fatalf("truncated path: len %d prefix %q", len(got), got[:4])
	}
}

// TestNodeBudget: MaxNodes and MaxDepth stop the walk with ErrBudget (U-0034, U-0035).
func TestNodeBudget(t *testing.T) {
	src := "module b { namespace urn:b; prefix b; container c1 { container c2 { container c3 { leaf l { type string; } } } } leaf x { type string; } }"
	for _, budget := range []Budget{{MaxNodes: 3}, {MaxDepth: 3}} {
		h := newNodeHarness(t, Options{Budget: budget}, mapFS(map[string]string{"b.yang": src}))
		if _, _, loadErr, err := h.load("b"); loadErr != nil || !errors.Is(err, ErrBudget) {
			t.Fatalf("%+v: got %v %v", budget, loadErr, err)
		}
	}
	h := newNodeHarness(t, Options{Budget: Budget{MaxNodes: 5, MaxDepth: 4}}, mapFS(map[string]string{"b.yang": src}))
	if _, _, _, err := h.load("b"); err != nil {
		t.Fatalf("within budget: %v", err)
	}
}

// TestTypeBudgetPerLoad: MaxTypes counts across every module compiled in one Load (U-0030), not
// per module, and starts again with the next Load.
func TestTypeBudgetPerLoad(t *testing.T) {
	src := "module b { namespace urn:b; prefix b; leaf x { type string { length 1; } } leaf y { type string { length 2; } } }"
	h := newNodeHarness(t, Options{Budget: Budget{MaxTypes: 3}}, mapFS(map[string]string{"b.yang": src}))
	if _, _, _, err := h.load("b"); err != nil {
		t.Fatal(err)
	}
	m := h.c.Modules[len(h.c.Modules)-1]
	if err := h.c.compileNodes(m, &schema.Module{Name: "b", Implemented: true}); !errors.Is(err, ErrBudget) {
		t.Fatalf("second compile in the same Load: %v, want ErrBudget", err)
	}
	h.c.nodes, h.c.types = 0, 0 // what the next Load does
	if err := h.c.compileNodes(m, &schema.Module{Name: "b", Implemented: true}); err != nil {
		t.Fatalf("after reset: %v", err)
	}
}

// TestMandatoryLeafTypedefDefault: a mandatory leaf ignores its typedef's default (SC:1026,
// fixture mand/leaf-with-typedef-default-ignored); a non-mandatory one keeps it.
func TestMandatoryLeafTypedefDefault(t *testing.T) {
	src := `module d { namespace urn:d; prefix d; typedef t { type string; default "x"; }
		leaf m { type t; mandatory true; } leaf o { type t; } }`
	h := newNodeHarness(t, Options{}, mapFS(map[string]string{"d.yang": src}))
	mod, _, loadErr, err := h.load("d")
	if loadErr != nil || err != nil {
		t.Fatal(loadErr, err)
	}
	if m, o := mod.Top[0], mod.Top[1]; m.Default != nil || len(o.Default) != 1 || o.Default[0].Lex != "x" {
		t.Fatalf("m %v, o %v", m.Default, o.Default)
	}
}

// TestUniquenessIndex: connecting many distinct siblings runs no sibling scan; a duplicate (also
// one inside a choice, and a case name) still runs the libyang scan and reports it.
func TestUniquenessIndex(t *testing.T) {
	m := &schema.Module{Name: "m"}
	w := &nodeCtx{c: &Context{}, cur: m, fl: map[*schema.Node]int{}}
	w.path.init(m)
	c := &schema.Node{Kind: schema.Container, Name: "c", Module: m}
	const n = 40000
	for i := range n {
		if err := w.connect(c, &schema.Node{Kind: schema.Leaf, Name: fmt.Sprint("l", i), Module: m}); err != nil {
			t.Fatal(err)
		}
	}
	ch := &schema.Node{Kind: schema.Choice, Name: "ch", Module: m}
	cs := &schema.Node{Kind: schema.Case, Name: "a", Module: m}
	for _, step := range []struct{ parent, node *schema.Node }{{c, ch}, {ch, cs}} {
		if err := w.connect(step.parent, step.node); err != nil {
			t.Fatal(err)
		}
	}
	if w.scans != 0 || len(c.Children) != n+1 {
		t.Fatalf("%d scans, %d children", w.scans, len(c.Children))
	}
	for _, step := range []struct {
		parent *schema.Node
		node   *schema.Node
		msg    string
	}{
		{cs, &schema.Node{Kind: schema.Leaf, Name: "l7", Module: m}, `Duplicate identifier "/m:c/l7" of data definition/RPC/action/notification statement.`},
		{ch, &schema.Node{Kind: schema.Case, Name: "a", Module: m}, `Duplicate identifier "/m:c/ch/a" of case statement.`},
		{c, &schema.Node{Kind: schema.Leaf, Name: "ch", Module: m}, `Duplicate identifier "/m:c/ch" of data definition/RPC/action/notification statement.`},
	} {
		w.c.diags = nil
		if err := w.connect(step.parent, step.node); !errors.Is(err, eExist) || len(w.c.diags) != 1 || w.c.diags[0].Msg != step.msg {
			t.Fatalf("%s: %v %+v", step.node.Name, err, w.c.diags)
		}
	}
	if w.scans != 3 {
		t.Fatalf("%d scans, want 3", w.scans)
	}
}

func matchesAny(msg string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}
