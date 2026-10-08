// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
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

// load parses name with the loader and compiles its data nodes, then removes the disabled ones
// (P6 step j). loadErr reports a parse-phase failure; diags are the compile diagnostics only.
func (h *nodeHarness) load(name string) (mod *schema.Module, diags []Diagnostic, loadErr, err error) {
	mod, _, diags, loadErr, err = h.loadFeatures(name, nil)
	return mod, diags, loadErr, err
}

// loadFeatures is load with the module's features (Load's argument) that also returns the
// parse-phase diagnostics.
func (h *nodeHarness) loadFeatures(name string, features []string) (mod *schema.Module, loadDiags, diags []Diagnostic,
	loadErr, err error) {
	m, all, err := h.c.Load(name, "", features)
	parseErr := false
	for _, d := range all {
		if d.Phase == "parse" {
			loadDiags = append(loadDiags, d)
			parseErr = parseErr || d.Level == LevelError
		} else {
			diags = append(diags, d)
		}
	}
	switch {
	case err != nil && parseErr:
		return nil, loadDiags, diags, err, nil
	case err != nil:
		return nil, loadDiags, diags, nil, err
	}
	return m.Schema, loadDiags, diags, nil, nil
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
	Defaults  []string `json:"defaults"`
	Type      *gType   `json:"type"`
	Exts      *[]gExt  `json:"extensions"`
}

type gExt struct {
	Module   string  `json:"module"`
	Name     string  `json:"name"`
	Argument *string `json:"argument"`
}

var oracleKinds = map[schema.Kind]string{
	schema.Container: "container", schema.Choice: "choice", schema.Case: "case", schema.Leaf: "leaf",
	schema.LeafList: "leaflist", schema.List: "list", schema.AnyData: "anydata", schema.AnyXML: "anyxml",
	schema.RPC: "rpc", schema.Action: "action", schema.Notification: "notif", schema.Input: "input",
	schema.Output: "output",
}

// dumpTree is lysc_module_dfs_full with snode_cb: a node, its actions and notifications
// subtrees, then its children.
func dumpTree(m *schema.Module) []gNode { // lysc_module_dfs_full order (Module.DFS)
	var out []gNode
	for n := range m.DFS() {
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
		if n.DefaultCaseName != "" {
			g.Defaults = []string{n.DefaultCaseName}
		}
		for _, d := range n.Default { // lyoracle dflt_json: canonical, the text when it fails
			v, diag := types.Store(n.Type, d.Lex, types.FormatSchemaResolved, types.HintSchema, d.NS, n)
			if diag != nil {
				g.Defaults = append(g.Defaults, d.Lex)
			} else {
				g.Defaults = append(g.Defaults, v.Canonical())
			}
		}
		if n.Type != nil {
			g.Type = leafType(n)
		}
		if n.Exts != nil {
			exts := []gExt{}
			for _, e := range n.Exts {
				x := gExt{Module: e.Def.Name, Name: e.Name}
				if e.Argument != "" {
					x.Argument = &e.Argument
				}
				exts = append(exts, x)
			}
			g.Exts = &exts
		}
		out = append(out, g)
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
	// design 06 C4b
	"Invalid value \"", "A current definition ", "A deprecated definition ", "Key \"", "Referenced type ",
	// design 06 C7 (unres)
	"Invalid leafref path ", "Not found node ", "Target of leafref ", "Invalid default ", "Node \"",
	"Configuration leaf-list has multiple", "Too many parent references", "No module connected with the prefix",
	"Key expected instead of", "List predicate defined for", "Leaf expected instead of", "Missing path substatement",
	"Leafref type ", "Invalid type \"", "Deref function", "Unexpected XPath token", "Not implemented module",
	"When condition ", "Invalid when condition", "Invalid must condition", "Unknown/non-implemented module",
	"Unexpected XPath expression end",
	// design 06 C6
	"Grouping \"", "Invalid prefix used for grouping", "Invalid child ", "Augment target node ", "Refine(s) target node ",
	"Invalid refine of ", "Invalid augment ", "Invalid schema-nodeid nametest",
}

// c6Warnings are the compile warnings the walk reports for C6.
var c6Warnings = []string{"Locally scoped grouping ", "Refining config inside "}

// knownDeviations turn the schema tree of a golden into the one a recorded deviation expects.
var knownDeviations = map[string]func(tree []gNode) []gNode{
	// D-0080: the refined action keeps its named input and output
	"uses-refine-action-description": func(tree []gNode) []gNode {
		io := []string{"input", "output"}
		for i := range tree {
			if strings.HasSuffix(tree[i].Path, "/(null)") {
				tree[i].Path = strings.TrimSuffix(tree[i].Path, "(null)") + io[0]
				io = io[1:]
			}
		}
		return tree
	},
	// D-0080: the refined input keeps its children
	"refine-action-input": func(tree []gNode) []gNode {
		i := slices.IndexFunc(tree, func(n gNode) bool { return n.Path == "/refine-action-input:c/a/input" })
		leaf := gNode{Path: "/refine-action-input:c/a/input/i", Nodetype: "leaf", Module: "refine-action-input",
			Status: "current", Mandatory: new(bool), Type: &gType{Base: "string"}}
		return slices.Insert(tree, i+1, leaf)
	},
}

// checkedWarnings are the warnings this harness compares: plugins (C4b), when/must status and
// not-implemented module, schema nodes not found by the XPath schema walk (C2a, C7), the value and
// operand warnings with their subexpression trailer (C2b).
var checkedWarnings = []string{"Ext plugin ", "When condition ", "Must condition ", "Schema node ", "Invalid value \"",
	"Previous warning generated", "Identityref \"", "Incompatible types"}

// c4bParseMessages are the parse-phase errors and warnings of the ported extension plugins
// (design 06 C4b); other parse-phase failures are the loader's.
var c4bParseMessages = []string{"Ext plugin "}

type manifestEntry struct {
	modules         []string
	features        map[string][]string // per module, as requested
	compileObsolete bool                // context option compile_obsolete
	refImplemented  bool                // context option ref_implemented
	skip            string              // why the request is outside this harness
}

// compileManifest reads the request of every fixture in dir compile from manifest.yaml and its
// fragments (a line scan: the root module has no YAML dependency).
func compileManifest(t *testing.T) map[string]manifestEntry {
	t.Helper()
	out := map[string]manifestEntry{}
	var cur manifestEntry
	inCompile := false
	for _, l := range manifestLines(t) {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "- id:"):
			cur, inCompile = manifestEntry{}, false
		case l == "dir: compile":
			inCompile = true
		case l == `context_options: ["compile_obsolete"]`:
			cur.compileObsolete = true
		case l == `context_options: ["ref_implemented"]`:
			cur.refImplemented = true
		case strings.HasPrefix(l, "context_options:") && l != "context_options: []":
			cur.skip = "context options"
		case strings.HasPrefix(l, "modules:"):
			var mods []map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(l, "modules:")), &mods); err != nil {
				cur.skip = "modules: " + err.Error()
				continue
			}
			cur.features = map[string][]string{}
			for _, m := range mods {
				name, _ := m["name"].(string)
				for k, v := range m {
					switch fs, _ := v.([]any); {
					case k == "features":
						cur.features[name] = []string{}
						for _, f := range fs {
							s, _ := f.(string)
							cur.features[name] = append(cur.features[name], s)
						}
					case k != "name":
						cur.skip = "module " + k
					}
				}
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
			opts := Options{CompileObsolete: req.compileObsolete, RefImplemented: req.refImplemented}
			h := newNodeHarness(t, opts, schemas)
			type dump struct {
				name string
				mod  *schema.Module
				want []gNode
			}
			var dumps []dump
			defer func() {
				// the oracle dumps every module after the last load (later loads may recompile)
				if t.Failed() || t.Skipped() {
					return
				}
				for _, d := range dumps {
					if got := dumpTree(d.mod); len(got)+len(d.want) > 0 && !reflect.DeepEqual(got, d.want) {
						gj, _ := json.MarshalIndent(got, "", " ")
						wj, _ := json.MarshalIndent(d.want, "", " ")
						t.Fatalf("%s tree:\n got  %s\n want %s", d.name, gj, wj)
					}
				}
			}()
			for _, gm := range g.Modules {
				mod, loadDiags, diags, loadErr, err := h.loadFeatures(gm.Name, req.features[gm.Name])
				if !gm.Accepted && gm.Phase == "parse" && len(gm.Diagnostics) > 0 &&
					(matchesAny(gm.Diagnostics[0].Msg, c4bParseMessages) || strings.Contains(gm.Diagnostics[0].SchemaPath, "{ext-inst=")) {
					var got []goldenDiag
					for _, d := range loadDiags {
						got = append(got, d.golden())
					}
					if loadErr == nil || !reflect.DeepEqual(got, gm.Diagnostics) {
						t.Fatalf("%s: got %v %+v\nwant %+v", gm.Name, loadErr, got, gm.Diagnostics)
					}
					ran++
					return
				}
				switch {
				case loadErr != nil && !gm.Accepted && gm.Phase == "parse":
					return // the parse phase is C1a's
				case loadErr != nil && !gm.Accepted && gm.Phase == "compile":
					var got []goldenDiag
					for _, d := range loadDiags {
						got = append(got, d.golden())
					}
					if !reflect.DeepEqual(got, gm.Diagnostics) { // a dep-set check of Load (design 06 C1b)
						t.Fatalf("%s: got %v %+v\nwant %+v", gm.Name, loadErr, got, gm.Diagnostics)
					}
					ran++
					return
				case errors.Is(loadErr, ErrUnsupported):
					t.Skip(loadErr) // U-0024, U-0025: engine-only
				case loadErr != nil:
					t.Fatalf("%s: load: %v", gm.Name, loadErr)
				case gm.Phase == "parse":
					t.Skip("parse-phase failure the C1a loader does not report yet (C1b, C4b)")
				case errors.Is(err, ErrUnsupported):
					t.Skip(err)
				}
				var want, got []goldenDiag
				var first *goldenDiag
				for _, d := range gm.Diagnostics {
					if d.Level == "error" || matchesAny(d.Msg, c6Warnings) {
						want = append(want, d)
					}
					if d.Level == "error" && first == nil {
						first = &d
					}
				}
				for _, d := range diags {
					if d.Level == LevelError || matchesAny(d.Msg, c6Warnings) {
						d.Phase = "compile"
						got = append(got, d.golden())
					}
				}
				if !gm.Accepted {
					if !matchesAny(first.Msg, c4aMessages) {
						t.Skipf("first error is a later PR's: %s", first.Msg)
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
				var gotW, wantW []goldenDiag
				for i, d := range append(loadDiags, diags...) {
					if i >= len(loadDiags) {
						d.Phase = "compile"
					}
					if d.Level == LevelWarning && matchesAny(d.Msg, checkedWarnings) {
						gotW = append(gotW, d.golden())
					}
				}
				for _, d := range gm.Diagnostics {
					if d.Level == "warning" && matchesAny(d.Msg, checkedWarnings) {
						wantW = append(wantW, d)
					}
				}
				if !reflect.DeepEqual(gotW, wantW) {
					t.Fatalf("%s: plugin warnings %+v\nwant %+v", gm.Name, gotW, wantW)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: errors and C6 warnings %+v\nwant %+v", gm.Name, got, want)
				}
				if gm.Tree == nil {
					continue // no schema dump in this request
				}
				wantTree := slices.Clone(*gm.Tree)
				if dev := knownDeviations[id]; dev != nil {
					wantTree = dev(wantTree)
				}
				dumps = append(dumps, dump{gm.Name, mod, wantTree})
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

// TestUnitsPresence: units "" is kept apart from no units, own or inherited from a typedef, as
// libyang prints `units "";` (fixture units/empty-vs-absent, issue #9).
func TestUnitsPresence(t *testing.T) {
	h := newNodeHarness(t, Options{}, os.DirFS("../../conformance/corpus/compile/schemas"))
	mod, _, loadErr, err := h.load("units-empty")
	if loadErr != nil || err != nil {
		t.Fatal(loadErr, err)
	}
	want := map[string]bool{"empty-units": true, "no-units": false, "inherited-empty": true, "override-empty": true,
		"ll-empty-units": true}
	for _, n := range mod.Top {
		if has := n.Units != nil; has != want[n.Name] || has && *n.Units != "" {
			t.Errorf("%s: units %v, want present %v and empty", n.Name, n.Units, want[n.Name])
		}
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

// leafType is lyoracle leaf_type_json: the type with each leafref's target, recompiled from the
// node as lysc_node_lref_target(s) do; in a union the targets are given only if all resolve.
func leafType(n *schema.Node) *gType {
	g := dumpType(n.Type)
	var targets []*string
	all := true
	for _, t := range leafrefs(n) {
		e, _ := lyxp.ParsePath(t.Path, lyxp.Opts{Begin: lyxp.BeginEither, Prefix: lyxp.PrefixOptional,
			Pred: lyxp.PredLeafref, Leafref: true})
		var target *string
		if e != nil {
			if p, _, err := types.CompileLeafref(n, e, t.Prefixes, n.InOutput(), false); err == nil {
				s := p[len(p)-1].Node.LogPath()
				target = &s
			}
		}
		all = all && target != nil
		targets = append(targets, target)
	}
	if g.Leafref != nil {
		g.Leafref.Target = targets[0]
	}
	i := 0
	for _, u := range g.Union {
		if u.Leafref != nil {
			u.Leafref.Target = nil
			if all {
				u.Leafref.Target = targets[i]
			}
			i++
		}
	}
	return g
}
