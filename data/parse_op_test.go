// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vibe-ports/yang/internal/ly"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

const opsManifest = "../conformance/corpus/manifest.d/ops"

// opsFixture is the request of an ops/parse-* fixture (op data, parse_only, an operation
// data_type), read from its manifest fragment without a YAML library: every request field is on
// one line.
type opsFixture struct {
	dir, golden string
	searchdirs  []string
	modules     []struct {
		name, revision string
		features       []string
	}
	format, dataType, data string
	rpcParent              string
	rpc                    string
	hasRPC                 bool
	unknown                UnknownPolicy // the request's unknown (reject when absent)
	keepInput              bool          // keep_input: the request keeps its parsed children
	operational            string
	hasOper                bool
}

var (
	reField = regexp.MustCompile(`(?m)^      (\w+): (.*)$`)
	reTop   = regexp.MustCompile(`(?m)^    (dir|golden): (.*)$`)
	reMod   = regexp.MustCompile(`\{name: "?([\w-]+)"?(?:, features: \[([^\]]*)\])?\}`)
	reFlow  = regexp.MustCompile(`(?m)^    request: (\{.*\})$`)
)

func unquote(t *testing.T, v string) string {
	t.Helper()
	if !strings.HasPrefix(v, `"`) {
		return v
	}
	var s string
	if err := json.Unmarshal([]byte(v), &s); err != nil {
		t.Fatal(v, err)
	}
	return s
}

func readOpsFixture(t *testing.T, path string) opsFixture {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // corpus path
	if err != nil {
		t.Fatal(err)
	}
	var f opsFixture
	for _, m := range reTop.FindAllStringSubmatch(string(b), -1) {
		if m[1] == "dir" {
			f.dir = m[2]
		} else {
			f.golden = m[2]
		}
	}
	if m := reFlow.FindSubmatch(b); m != nil { // a request in JSON flow style
		var r struct {
			Searchdirs []string
			Modules    []struct {
				Name     string
				Features []string
			}
			Format, Data string
			DataType     string `json:"data_type"`
			RPC          *string
			Operational  *string
			Unknown      string
		}
		if err := json.Unmarshal(m[1], &r); err != nil {
			t.Fatal(path, err)
		}
		f.searchdirs, f.format, f.dataType, f.data = r.Searchdirs, r.Format, r.DataType, r.Data
		for _, mod := range r.Modules {
			f.modules = append(f.modules, struct {
				name, revision string
				features       []string
			}{name: mod.Name, features: mod.Features})
		}
		if r.RPC != nil {
			f.rpc, f.hasRPC = *r.RPC, true
		}
		if r.Operational != nil {
			f.operational, f.hasOper = *r.Operational, true
		}
		f.unknown = map[string]UnknownPolicy{"skip": Skip, "opaque": Opaque}[r.Unknown]
		return f
	}
	for _, m := range reField.FindAllStringSubmatch(string(b), -1) {
		v := m[2]
		switch m[1] {
		case "searchdirs":
			for _, d := range strings.Split(strings.Trim(v, "[]"), ",") {
				f.searchdirs = append(f.searchdirs, unquote(t, strings.TrimSpace(d)))
			}
		case "modules":
			for _, mm := range reMod.FindAllStringSubmatch(v, -1) {
				mod := struct {
					name, revision string
					features       []string
				}{name: mm[1]}
				for _, ft := range strings.Split(mm[2], ",") {
					if ft = strings.TrimSpace(ft); ft != "" {
						mod.features = append(mod.features, unquote(t, ft))
					}
				}
				f.modules = append(f.modules, mod)
			}
		case "format":
			f.format = unquote(t, v)
		case "data_type":
			f.dataType = unquote(t, v)
		case "data":
			f.data = unquote(t, v)
		case "rpc":
			f.rpc, f.hasRPC = unquote(t, v), true
		case "rpc_parent":
			f.rpcParent = unquote(t, v)
		case "keep_input":
			f.keepInput = v == "true"
		case "unknown":
			f.unknown = map[string]UnknownPolicy{"reject": Reject, "skip": Skip, "opaque": Opaque}[unquote(t, v)]
		case "operational":
			f.operational, f.hasOper = unquote(t, v), true
		}
	}
	return f
}

// opsSet compiles the modules of a fixture as the oracle's build_ctx does.
func opsSet(t *testing.T, f opsFixture) *schema.Set {
	t.Helper()
	base := filepath.Join("../conformance/corpus", f.dir)
	if filepath.IsAbs(f.dir) {
		base = f.dir
	}
	var dirs []fs.FS
	for _, d := range f.searchdirs {
		dirs = append(dirs, os.DirFS(filepath.Join(base, d)))
	}
	c, _, err := compile.NewContext(compile.Options{}, dirs...)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range f.modules {
		if _, diags, err := c.Load(m.name, "", m.features); err != nil {
			t.Fatal(m.name, err, diags)
		}
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		if m.Schema != nil {
			set.Modules = append(set.Modules, m.Schema)
		}
	}
	return set
}

// opsGolden is the part of an op data golden the parse decides.
type opsGolden struct {
	goldenStep
	Verdict string `json:"verdict"`
	Tree    *struct {
		JSON string `json:"json"`
	} `json:"tree"`
	// RequestTyped is the request's tree after a failed reply was parsed into it
	RequestTyped json.RawMessage `json:"request_typed"`
}

// TestParseOpGoldens replays the ops/parse-* fixtures (lyd_parse_op, parse only): the verdict,
// the diagnostics, the typed dump (paths, flags, values) and the printed JSON of the tree.
func TestParseOpGoldens(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(opsManifest, "parse-*.yaml"))
	if err != nil || len(paths) < 20 {
		t.Fatalf("%d ops fixtures: %v", len(paths), err)
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			f := readOpsFixture(t, path)
			b, err := os.ReadFile(filepath.Join("../conformance/corpus", f.dir, f.golden))
			if err != nil {
				t.Fatal(err)
			}
			var want opsGolden
			if err := json.Unmarshal(b, &want); err != nil {
				t.Fatal(err)
			}
			set := opsSet(t, f)
			format := FormatJSON
			if f.format == "xml" {
				format = FormatXML
			}
			typ := map[string]opType{"rpc": opRPC, "notif": opNotif, "reply": opReply}[f.dataType]
			var tree *Tree
			var diags []yang.Diagnostic
			if f.hasRPC {
				var op *Node
				tree, op, diags, err = parseOp(context.Background(), strings.NewReader(f.rpc), set, format, opRPC, nil, f.unknown)
				if err != nil {
					t.Fatal("rpc", err, diags)
				}
				if !f.keepInput {
					for _, c := range slices.Collect(op.kids.all()) { // lyd_free_siblings(lyd_child(op))
						unlink(c)
					}
				}
				parent := op
				if f.rpcParent != "" {
					parent, err = tree.Find(f.rpcParent)
					if err != nil || parent == nil {
						t.Fatalf("rpc_parent %q: %v", f.rpcParent, err)
					}
				}
				_, _, diags, err = parseOp(context.Background(), strings.NewReader(f.data), set, format, typ, parent, f.unknown)
			} else {
				tree, _, diags, err = parseOp(context.Background(), strings.NewReader(f.data), set, format, typ, nil, f.unknown)
			}
			if errors.Is(err, yang.ErrUnsupported) {
				t.Skip(err) // anydata/anyxml instances (U-0043)
			}
			if (err == nil) != (want.Verdict == "valid") {
				t.Fatalf("err %v, verdict %s", err, want.Verdict)
			}
			var got []string
			for _, d := range diags {
				got = append(got, fmt.Sprintf("%s %s %s %s", d.Code, d.DataPath, d.SchemaPath, d.Msg))
			}
			if !reflect.DeepEqual(got, want.diags()) {
				t.Errorf("diagnostics\n got  %q\n want %q", got, want.diags())
			}
			if err != nil {
				if want.RequestTyped != nil { // what lyd_parse_op's cleanup left of the request
					var rw goldenStep
					if err := json.Unmarshal([]byte(`{"typed":`+string(want.RequestTyped)+`}`), &rw); err != nil {
						t.Fatal(err)
					}
					if g, w := typedDump(tree), rw.dump(); !reflect.DeepEqual(g, w) {
						t.Errorf("request after the failed reply\n got  %q\n want %q", g, w)
					}
				}
				return
			}
			if g, w := typedDump(tree), want.dump(); !reflect.DeepEqual(g, w) {
				t.Errorf("typed\n got  %q\n want %q", g, w)
			}
			var buf bytes.Buffer
			if err := tree.PrintJSON(&buf, PrintOptions{}); err != nil {
				t.Fatal(err)
			}
			if want.Tree != nil && buf.String() != want.Tree.JSON {
				t.Errorf("tree\n got  %s\n want %s", buf.String(), want.Tree.JSON)
			}
		})
	}
}

// TestParseOpSkip: lyd_parse_op with parse options 0 (neither STRICT nor OPAQ) drops unknown
// nodes, as native libyang does ({"a:r1":{"l1":"x"}}); the oracle has no such request.
func TestParseOpSkip(t *testing.T) {
	f := readOpsFixture(t, filepath.Join(opsManifest, "parse-xml-rpc-input.yaml"))
	set := opsSet(t, f)
	tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"a:r1":{"zz":1,"l1":"x"}}`), set,
		FormatJSON, opRPC, nil, Skip)
	if err != nil || len(diags) != 0 {
		t.Fatal(err, diags)
	}
	var buf bytes.Buffer
	if err := tree.PrintJSON(&buf, PrintOptions{Shrink: true}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != `{"a:r1":{"l1":"x"}}` || op == nil || op.schema.Name != "r1" {
		t.Fatalf("%s, op %v", got, op)
	}
}

// TestParseOpOpaqueOperation: an action member parsed as an opaque node is the operation node,
// and a second one fails with libyang's message instead of its crash (D-0110).
func TestParseOpOpaqueOperation(t *testing.T) {
	set := opsSet(t, readOpsFixture(t, filepath.Join(opsManifest, "parse-xml-rpc-input.yaml")))
	_, _, diags, err := parseOp(context.Background(), strings.NewReader(`{"a:c":{"act":42,"act":{}}}`), set,
		FormatJSON, opRPC, nil, Opaque)
	if err == nil || len(diags) == 0 || diags[len(diags)-1].Msg != `Unexpected action element "act", action "act" already parsed.` {
		t.Fatal(err, diags)
	}
}

// TestParseOpJSONInitFirst: lyd_parse_json_init checks the first token before
// lyd_parser_find_operation checks the parent, so input that is not an object fails with the
// JSON syntax error even under a parent the operation type refuses; a NUL ends the input before
// the sibling check.
func TestParseOpJSONInitFirst(t *testing.T) {
	set := opsSet(t, readOpsFixture(t, filepath.Join(opsManifest, "parse-xml-rpc-input.yaml")))
	_, op := opParent(t, set)
	_, _, diags, err := parseOp(context.Background(), strings.NewReader(`1`), set, FormatJSON, opNotif, op, Reject)
	if err == nil || len(diags) != 1 || diags[0].Code != ly.SyntaxJSON.String() {
		t.Fatal(err, diags)
	}
	if _, _, diags, err := parseOp(context.Background(), strings.NewReader("{\"a:r1\":{},\x00{\"a:foo\":1}"), set,
		FormatJSON, opRPC, nil, Reject); err != nil {
		t.Fatal(err, diags)
	}
}

// TestParseOpMixedInputOutput: output parameters parsed into a request that keeps its input go
// before the input nodes (lyd_insert_get_next_anchor), and both stay findable by path.
func TestParseOpMixedInputOutput(t *testing.T) {
	set := opsSet(t, readOpsFixture(t, filepath.Join(opsManifest, "parse-json-reply-keep-order.yaml")))
	tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"rb:r":{"in":"keep"}}`), set,
		FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	if _, _, diags, err := parseOp(context.Background(), strings.NewReader(`{"rb:v":[2],"rb:oc":{"n":1}}`), set,
		FormatJSON, opReply, op, Reject); err != nil {
		t.Fatal(err, diags)
	}
	var names []string
	for c := range op.kids.all() {
		names = append(names, c.schema.Name)
	}
	if !slices.Equal(names, []string{"v", "oc", "in"}) {
		t.Fatalf("children %v, want [v oc in]", names)
	}
	// lyd_find_path resolves the path in the input (no output flag); the XPath finds both
	if n, err := tree.Find("/rb:r/in"); n == nil || err != nil {
		t.Errorf("Find: %v %v", n, err)
	}
	for _, path := range []string{"/rb:r/in", "/rb:r/oc/n", "/rb:r/v"} {
		if ns, _, err := tree.FindXPath(path, XPathOptions{}); len(ns) != 1 || err != nil {
			t.Errorf("FindXPath(%s): %v %v", path, ns, err)
		}
	}
}

// TestParseOpLinear: a large sorted leaf-list of an rpc output costs counted work linear in its
// size (the run of an operation's child is found run by run, appends take the fast path), with
// the other output nodes before and after it.
func TestParseOpLinear(t *testing.T) {
	set := opsSet(t, readOpsFixture(t, filepath.Join(opsManifest, "parse-json-reply-keep-order.yaml")))
	work := func(n int) int64 {
		vals := make([]string, n)
		for i := range vals {
			vals[i] = fmt.Sprintf(`"v%07d"`, i)
		}
		in := `{"rb:r":{"before":"b","s":[` + strings.Join(vals, ",") + `],"oc":{"n":1}}}`
		tree, _, diags, err := parseOp(context.Background(), strings.NewReader(in), set, FormatJSON, opReply, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		return tree.work.Load()
	}
	if w1, w4 := work(1000), work(4000); w4 > 5*w1 {
		t.Fatalf("work %d for 1000 values, %d for 4000: not linear", w1, w4)
	}
}

// TestParseOpSplitRun: an output leaf-list whose instances the anchor splits around a kept input
// node ([before, v1, v2, in, v3], ops/parse-*-reply-keep-order-split) keeps every node findable.
func TestParseOpSplitRun(t *testing.T) {
	set := opsSet(t, readOpsFixture(t, filepath.Join(opsManifest, "parse-json-reply-keep-order.yaml")))
	tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"rb:r":{"in":"keep"}}`), set,
		FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	if _, _, diags, err := parseOp(context.Background(), strings.NewReader(`{"rb:before":"b","rb:v":[1,2,3]}`), set,
		FormatJSON, opReply, op, Reject); err != nil {
		t.Fatal(err, diags)
	}
	if n, err := tree.Find("/rb:r/in"); n == nil || err != nil {
		t.Errorf("Find(/rb:r/in): %v %v", n, err)
	}
	if ns, _, err := tree.FindXPath("/rb:r/v", XPathOptions{}); len(ns) != 3 || err != nil {
		t.Errorf("FindXPath(/rb:r/v): %d nodes, %v", len(ns), err)
	}
	if n := tree.schemaLookup(&op.kids, op.kids.list[0].schema); n == nil || n.schema.Name != "before" {
		t.Errorf("schemaLookup(before): %v", n)
	}
}

// TestParseOpDupKeepsOrder replays ops/dup-json-reply-keep-order-split: a recursive copy of an
// operation keeps its children's order (lyd_dup_single inserts the copies with
// LYD_INSERT_NODE_LAST), also when the anchor split an output leaf-list around the kept input.
// The engine runs the fixture once Dup is exported.
func TestParseOpDupKeepsOrder(t *testing.T) {
	b, err := os.ReadFile("../conformance/corpus/ut-parser/golden/ops-dup-json-reply-keep-order-split.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Steps []struct {
			Typed []struct {
				Path  string `json:"path"`
				Value *struct {
					Canonical string `json:"canonical"`
				} `json:"value"`
			} `json:"typed"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(b, &g); err != nil || len(g.Steps) == 0 {
		t.Fatal(err)
	}
	var want []string
	for _, n := range g.Steps[len(g.Steps)-1].Typed[1:] { // the children of the copied rpc
		want = append(want, n.Path+"="+n.Value.Canonical)
	}
	set := opsSet(t, readOpsFixture(t, filepath.Join(opsManifest, "parse-json-reply-keep-order.yaml")))
	_, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"rb:r":{"in":"keep"}}`), set,
		FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	if _, _, diags, err := parseOp(context.Background(), strings.NewReader(`{"rb:before":"b","rb:v":[1,2,3]}`), set,
		FormatJSON, opReply, op, Reject); err != nil {
		t.Fatal(err, diags)
	}
	d, err := newTree(set).dup(op)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for c := range d.kids.all() {
		got = append(got, c.Path()+"="+c.Value())
	}
	if !slices.Equal(got, want) {
		t.Errorf("copy %v, oracle %v", got, want)
	}
}

// TestXPathOracleOps replays the libyang goldens of the conformance fixtures
// ops/xpath-json-reply-shared-name and -union (XPath over a reply; the engine runs them once op
// xpath takes operation data). A name in both input and output is matched by name; next
// to another parent's schema node (c/v) libyang matches the whole step by that node, so
// (/rbn:r3 | /rbn:r3/c)/v gives only /rbn:r3/c/v (xs.Child).
func TestXPathOracleOps(t *testing.T) {
	for _, tc := range []struct{ golden, module, data, xpath string }{
		{"ops-xpath-json-reply-shared-name", "rbm", `{"rbm:r2":{"v":"ok"}}`, "/rbm:r2/v"},
		{"ops-xpath-json-reply-shared-name-union", "rbn", `{"rbn:r3":{"v":"ok","c":{"v":"x"}}}`, "(/rbn:r3 | /rbn:r3/c)/v"},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("../conformance/corpus/ut-parser/golden", tc.golden+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var want struct {
				Verdict string `json:"verdict"`
				Result  struct {
					Nodes []string `json:"nodes"`
				} `json:"result"`
			}
			if err := json.Unmarshal(b, &want); err != nil || want.Verdict != "valid" {
				t.Fatal(err, want.Verdict)
			}
			f := readOpsFixture(t, filepath.Join(opsManifest, "parse-json-reply-keep-order.yaml"))
			f.modules = []struct {
				name, revision string
				features       []string
			}{{name: tc.module}}
			tree, _, diags, err := parseOp(context.Background(), strings.NewReader(tc.data), opsSet(t, f), FormatJSON,
				opReply, nil, Reject)
			if err != nil {
				t.Fatal(err, diags)
			}
			ns, _, err := tree.FindXPath(tc.xpath, XPathOptions{})
			got := []string{}
			for _, n := range ns {
				got = append(got, n.Path())
			}
			if err != nil || !slices.Equal(got, want.Result.Nodes) {
				t.Errorf("%s: %v %v, oracle %v", tc.xpath, got, err, want.Result.Nodes)
			}
		})
	}
}

// TestXPathOperationLinear: a predicate that looks a sibling list up from every instance of a
// large leaf-list among an operation's children ([big…, l, chk…], /q:r/chk[../l[k='0']]) costs
// counted work linear in the instances: the lookup takes the instance index, not a scan.
func TestXPathOperationLinear(t *testing.T) {
	dir := t.TempDir()
	y := `module q { yang-version 1.1; namespace urn:q; prefix q; rpc r { input {
		leaf-list big { type string; } list l { key k; leaf k { type string; } } leaf-list chk { type string; } } } }`
	if err := os.WriteFile(filepath.Join(dir, "q.yang"), []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	set := opsSet(t, opsFixture{dir: dir, searchdirs: []string{"."}, modules: []struct {
		name, revision string
		features       []string
	}{{name: "q"}}})
	work := func(n int) int64 {
		vals := func(p string) string {
			s := make([]string, n)
			for i := range s {
				s[i] = fmt.Sprintf(`"%s%07d"`, p, i)
			}
			return strings.Join(s, ",")
		}
		in := `{"q:r":{"big":[` + vals("b") + `],"l":[{"k":"0"}],"chk":[` + vals("c") + `]}}`
		tree, _, diags, err := parseOp(context.Background(), strings.NewReader(in), set, FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		before := tree.work.Load()
		ns, _, err := tree.FindXPath("/q:r/chk[../l[k='0']]", XPathOptions{})
		if err != nil || len(ns) != n {
			t.Fatal(err, len(ns))
		}
		return tree.work.Load() - before
	}
	if w1, w4 := work(1000), work(4000); w4 > 6*w1 {
		t.Fatalf("work %d for 1000 values, %d for 4000: not linear", w1, w4)
	}
}

// TestDiffDetachedOperationChild: an operation child unlinked from its rpc has no instance index
// (sibsOf gives it a singleton sibling set); lookups filter that set, so DiffTree of the attached
// leaf of one rpc and the equal detached leaf of another reports no change.
func TestDiffDetachedOperationChild(t *testing.T) {
	set := opsSet(t, readOpsFixture(t, filepath.Join(opsManifest, "parse-json-reply-keep-order.yaml")))
	parse := func() *Node {
		_, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"rb:r":{"in":"x"}}`), set,
			FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		return op.kids.list[0]
	}
	attached, detached := parse(), parse()
	unlink(detached)
	diff, err := DiffTree(attached, detached, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if diff != nil && len(diff.top.list)+len(diff.top.opq) > 0 {
		var b strings.Builder
		_ = diff.PrintJSON(&b, PrintOptions{})
		t.Errorf("diff of equal leaves: %s", b.String())
	}
}

// splitRunReply parses an rbs:r4 reply with or without its input child kept. Keeping it produces
// [before, v1, v2, in, v3], while removing it first leaves one contiguous output run.
func splitRunReply(t *testing.T, set *schema.Set, keepInput bool, values string) (*Tree, *Node) {
	t.Helper()
	tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"rbs:r4":{"in":"keep"}}`), set,
		FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	if !keepInput {
		for _, c := range slices.Collect(op.kids.all()) {
			unlink(c)
		}
	}
	data := `{"rbs:before":"b","rbs:v":[` + values + `]}`
	if _, _, diags, err = parseOp(context.Background(), strings.NewReader(data), set,
		FormatJSON, opReply, op, Reject); err != nil {
		t.Fatal(err, diags)
	}
	return tree, op
}

func splitRunSet(t *testing.T) *schema.Set {
	t.Helper()
	f := readOpsFixture(t, filepath.Join(opsManifest, "parse-json-reply-keep-order.yaml"))
	f.modules = []struct {
		name, revision string
		features       []string
	}{{name: "rbs"}}
	return opsSet(t, f)
}

func wantSplitRunErr(t *testing.T, err error, node, path, fn string) {
	t.Helper()
	want := fmt.Sprintf(`Invalid arguments - the instances of %q in operation %q are split by its input, a diff of such data is not supported (D-0116) (%s()).`, node, path, fn)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 ||
		ve.Diags[0].Err != "LY_EINVAL" || ve.Diags[0].Code != "LYVE_SUCCESS" ||
		ve.Diags[0].Msg != want || ve.Diags[0].DataPath != "" || ve.Diags[0].SchemaPath != "" ||
		ve.Diags[0].AppTag != "" {
		t.Fatalf("error %#v, want LY_EINVAL %q", err, want)
	}
}

// TestSplitRunConsumers replays ops/find-json-reply-split-position on the reply whose output
// leaf-list v the anchor splits around the kept input ([before, v1, v2, in, v3]): lyd_find_path
// resolves the path in the input, so v[3] fails with libyang's message and the tree is unchanged
// (a positional walk over a split run is unreachable).
func TestSplitRunConsumers(t *testing.T) {
	b, err := os.ReadFile("../conformance/corpus/ut-parser/golden/ops-find-json-reply-split-position.json")
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Steps []struct {
			Diagnostics []struct {
				Msg string `json:"msg"`
			} `json:"diagnostics"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(b, &g); err != nil || len(g.Steps) < 2 || len(g.Steps[1].Diagnostics) == 0 {
		t.Fatal(err)
	}
	set := splitRunSet(t)
	tree, _ := splitRunReply(t, set, true, "1,2,3")
	var ve *ValidationError
	if n, err := tree.Find("/rbs:r4/v[3]"); n != nil || !errors.As(err, &ve) || ve.Diags[0].Msg != g.Steps[1].Diagnostics[0].Msg {
		t.Errorf("Find(v[3]): %v %v, oracle %q", n, err, g.Steps[1].Diagnostics[0].Msg)
	}
}

// TestSplitRunDiffRefused covers every generation shape, including the nil/empty reported case
// and both sides of a comparison. Refusal takes precedence even when both arguments are split.
func TestSplitRunDiffRefused(t *testing.T) {
	set := splitRunSet(t)
	splitTree, splitOp := splitRunReply(t, set, true, "1,2,3")
	_, unsplitOp := splitRunReply(t, set, false, "1,2,3")
	_, otherSplitOp := splitRunReply(t, set, true, "1,2,3")
	value := func(op *Node, want string) *Node {
		t.Helper()
		for _, n := range op.kids.list {
			if n.Name() == "v" && n.value.Canonical() == want {
				return n
			}
		}
		t.Fatalf("v=%s not found", want)
		return nil
	}
	v3, otherV3 := value(splitOp, "3"), value(otherSplitOp, "3")
	tests := []struct {
		name string
		call func() (*Tree, error)
	}{
		{"nil-to-split", func() (*Tree, error) { return Diff(nil, splitTree, DiffOptions{}) }},
		{"empty-to-split", func() (*Tree, error) { return Diff(newTree(set), splitTree, DiffOptions{}) }},
		{"tree-nil-to-split", func() (*Tree, error) { return DiffTree(nil, splitOp, DiffOptions{}) }},
		{"split-to-unsplit", func() (*Tree, error) { return DiffTree(splitOp, unsplitOp, DiffOptions{}) }},
		{"unsplit-to-split", func() (*Tree, error) { return DiffTree(unsplitOp, splitOp, DiffOptions{}) }},
		{"split-to-split", func() (*Tree, error) { return DiffTree(splitOp, splitOp, DiffOptions{}) }},
		{"siblings", func() (*Tree, error) { return DiffSiblings(nil, splitOp, DiffOptions{}) }},
		{"operation-children", func() (*Tree, error) { return DiffSiblings(splitOp.kids.list[0], nil, DiffOptions{}) }},
		{"tree-child-to-nil", func() (*Tree, error) { return DiffTree(v3, nil, DiffOptions{}) }},
		{"tree-nil-to-child", func() (*Tree, error) { return DiffTree(nil, v3, DiffOptions{}) }},
		{"tree-child-to-other-tree", func() (*Tree, error) { return DiffTree(v3, otherV3, DiffOptions{}) }},
		{"siblings-child-to-nil", func() (*Tree, error) { return DiffSiblings(v3, nil, DiffOptions{}) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call()
			if got != nil {
				t.Fatalf("diff %v, want nil", got)
			}
			wantSplitRunErr(t, err, "v", "/rbs:r4", "lyd_diff")
		})
	}
}

// splitGeneratedDiffs builds the two diffs from the native-oracle merge fixture. The first diff
// has operation children [v1, v2, v3, in, v4], split by lyd_diff_dup's schema-anchor insertion.
func splitGeneratedDiffs(t *testing.T) (base, middle, final, first, second *Tree) {
	t.Helper()
	fixture, steps := readDiffOpFixture(t, filepath.Join(opsManifest, "diff-merge-json-reply-keep-input.yaml"))
	if len(steps) < 4 {
		t.Fatalf("split diff fixture has %d steps", len(steps))
	}
	set := opsSet(t, fixture)
	var firstOp, middleOp, finalOp *Node
	base, firstOp = parseDiffOpStep(t, set, steps[0])
	middle, middleOp = parseDiffOpStep(t, set, steps[1])
	var err error
	first, err = DiffTree(firstOp, middleOp, DiffOptions{})
	if err != nil || first == nil {
		t.Fatalf("first DiffTree: %v, %v", first, err)
	}
	if split, _ := splitOpRun(first.top.first(), true); split == nil {
		t.Fatal("generated first diff is not split")
	}
	_, middleOp = parseDiffOpStep(t, set, steps[2])
	final, finalOp = parseDiffOpStep(t, set, steps[3])
	second, err = DiffTree(middleOp, finalOp, DiffOptions{})
	if err != nil || second == nil {
		t.Fatalf("second DiffTree: %v, %v", second, err)
	}
	return base, middle, final, first, second
}

func unsplitRunDiff(t *testing.T, set *schema.Set) *Tree {
	t.Helper()
	_, first := splitRunReply(t, set, false, "1,2,3")
	_, second := splitRunReply(t, set, false, "1,2,4")
	diff, err := DiffTree(first, second, DiffOptions{})
	if err != nil || diff == nil {
		t.Fatalf("unsplit DiffTree: %v, %v", diff, err)
	}
	return diff
}

// TestSplitRunDiffApplyRefused checks that an ordinary diff cannot be applied to split operation
// data before mutation. A generated split diff is not data and is accepted and replayed.
func TestSplitRunDiffApplyRefused(t *testing.T) {
	set := splitRunSet(t)
	for _, tc := range []struct {
		name    string
		target  func() *Tree
		diff    func() *Tree
		options ApplyDiffOptions
		fn      string
	}{
		{
			name:   "split-target",
			target: func() *Tree { tr, _ := splitRunReply(t, set, true, "1,2,3"); return tr },
			diff:   func() *Tree { return unsplitRunDiff(t, set) },
			fn:     "lyd_diff_apply_all",
		},
		{
			name:    "split-target-module",
			target:  func() *Tree { tr, _ := splitRunReply(t, set, true, "1,2,3"); return tr },
			diff:    func() *Tree { return unsplitRunDiff(t, set) },
			options: ApplyDiffOptions{Module: "rbs"},
			fn:      "lyd_diff_apply_module",
		},
		{
			name:   "split-target-nil-diff",
			target: func() *Tree { tr, _ := splitRunReply(t, set, true, "1,2,3"); return tr },
			diff:   func() *Tree { return nil },
			fn:     "lyd_diff_apply_all",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, diff := tc.target(), tc.diff()
			before := treeJSON(t, target)
			err := target.ApplyDiff(diff, tc.options)
			wantSplitRunErr(t, err, "v", "/rbs:r4", tc.fn)
			if after := treeJSON(t, target); after != before {
				t.Fatalf("target changed: %s, was %s", after, before)
			}
		})
	}
	t.Run("generated-split-diff", func(t *testing.T) {
		target, want, _, diff, _ := splitGeneratedDiffs(t)
		if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
			t.Fatalf("ApplyDiff: %v", err)
		}
		if got := typedDump(target); !reflect.DeepEqual(got, typedDump(want)) {
			t.Fatalf("applied tree:\n got  %q\n want %q", got, typedDump(want))
		}
	})
}

// TestSplitRunDiffMergeRefused checks that generated split diff sources and destinations are
// accepted by both merge entry points and replay to the same final tree as libyang.
func TestSplitRunDiffMergeRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(first, second *Tree) error
	}{
		{"merge", func(first, second *Tree) error { return first.MergeDiff(second, MergeDiffOptions{}) }},
		{"merge-module", func(first, second *Tree) error {
			return first.MergeDiff(second, MergeDiffOptions{Module: "rb"})
		}},
		{"merge-tree", func(first, second *Tree) error {
			return first.MergeDiffTree(nil, second.top.first(), MergeDiffOptions{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, _, want, first, second := splitGeneratedDiffs(t)
			if err := tc.run(first, second); err != nil {
				t.Fatalf("merge: %v", err)
			}
			if err := base.ApplyDiff(first, ApplyDiffOptions{}); err != nil {
				t.Fatalf("ApplyDiff merged: %v", err)
			}
			if got := typedDump(base); !reflect.DeepEqual(got, typedDump(want)) {
				t.Fatalf("merged apply:\n got  %q\n want %q", got, typedDump(want))
			}
		})
	}
	t.Run("split-source-into-empty", func(t *testing.T) {
		base, want, _, split, _ := splitGeneratedDiffs(t)
		dst := newTree(split.set)
		if err := dst.MergeDiff(split, MergeDiffOptions{}); err != nil {
			t.Fatalf("MergeDiff: %v", err)
		}
		if err := base.ApplyDiff(dst, ApplyDiffOptions{}); err != nil {
			t.Fatalf("ApplyDiff copied merge: %v", err)
		}
		if got := typedDump(base); !reflect.DeepEqual(got, typedDump(want)) {
			t.Fatalf("copied merge apply:\n got  %q\n want %q", got, typedDump(want))
		}
	})
	t.Run("split-tree-source-into-empty", func(t *testing.T) {
		base, want, _, split, _ := splitGeneratedDiffs(t)
		dst := newTree(split.set)
		if err := dst.MergeDiffTree(nil, split.top.first(), MergeDiffOptions{}); err != nil {
			t.Fatalf("MergeDiffTree: %v", err)
		}
		if err := base.ApplyDiff(dst, ApplyDiffOptions{}); err != nil {
			t.Fatalf("ApplyDiff copied tree merge: %v", err)
		}
		if got := typedDump(base); !reflect.DeepEqual(got, typedDump(want)) {
			t.Fatalf("copied tree merge apply:\n got  %q\n want %q", got, typedDump(want))
		}
	})
	t.Run("split-destination-nil", func(t *testing.T) {
		_, _, _, split, _ := splitGeneratedDiffs(t)
		before := treeJSON(t, split)
		if err := split.MergeDiff(nil, MergeDiffOptions{}); err != nil {
			t.Fatalf("MergeDiff(nil): %v", err)
		}
		if after := treeJSON(t, split); after != before {
			t.Fatalf("destination changed: %s, was %s", after, before)
		}
		if err := split.MergeDiffTree(nil, nil, MergeDiffOptions{}); err != nil {
			t.Fatalf("MergeDiffTree(nil): %v", err)
		}
		if after := treeJSON(t, split); after != before {
			t.Fatalf("tree destination changed: %s, was %s", after, before)
		}
	})
}

// TestSplitRunNestedActionRefused proves the scan is recursive and includes actions below data
// nodes, not only top-level RPCs.
func TestSplitRunNestedActionRefused(t *testing.T) {
	dir := t.TempDir()
	y := `module m { yang-version 1.1; namespace "urn:m"; prefix m;
		container c { action a { input { leaf in { type string; } } output {
			leaf before { type string; } leaf-list v { type string; }
		} } } }`
	if err := os.WriteFile(filepath.Join(dir, "m.yang"), []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	set := opsSet(t, opsFixture{dir: dir, searchdirs: []string{"."}, modules: []struct {
		name, revision string
		features       []string
	}{{name: "m"}}})
	tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"m:c":{"a":{"in":"keep"}}}`), set,
		FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	if _, _, diags, err = parseOp(context.Background(), strings.NewReader(`{"m:before":"b","m:v":["1","2","3"]}`), set,
		FormatJSON, opReply, op, Reject); err != nil {
		t.Fatal(err, diags)
	}
	if split, owner := splitOpRun(tree.top.first(), true); split == nil || owner != op {
		t.Fatalf("nested action was not split: child %v, owner %v", split, owner)
	}
	_, err = Diff(nil, tree, DiffOptions{})
	wantSplitRunErr(t, err, "v", "/m:c/a", "lyd_diff")
}

// TestSplitRunDiffLinear measures the refusal preflight on successful, equal, unsplit replies,
// both from the operation roots and from one direct child (the DiffTree parent scan). Every
// doubling from 64 through 512 nodes stays within 2.5x for several order distributions.
func TestSplitRunDiffLinear(t *testing.T) {
	dir := t.TempDir()
	y := `module m { yang-version 1.1; namespace "urn:m"; prefix m; rpc r {
		input { leaf in { type string; } } output { leaf before { type string; }
		leaf-list v { type string; ordered-by user; } } } }`
	if err := os.WriteFile(filepath.Join(dir, "m.yang"), []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	set := opsSet(t, opsFixture{dir: dir, searchdirs: []string{"."}, modules: []struct {
		name, revision string
		features       []string
	}{{name: "m"}}})
	values := func(n int, order string) string {
		v := make([]string, n)
		for i := range v {
			j := i
			switch order {
			case "reverse":
				j = n - 1 - i
			case "shuffled":
				j = i * 5 % n
			case "duplicate-heavy":
				j = i % 4
			}
			v[i] = fmt.Sprintf("%q", fmt.Sprintf("v%04d", j))
		}
		return strings.Join(v, ",")
	}
	parse := func(vals string) (*Tree, *Node) {
		tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"m:r":{"in":"keep"}}`), set,
			FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		for _, c := range slices.Collect(op.kids.all()) {
			unlink(c)
		}
		data := `{"m:before":"b","m:v":[` + vals + `]}`
		if _, _, diags, err = parseOp(context.Background(), strings.NewReader(data), set,
			FormatJSON, opReply, op, Reject); err != nil {
			t.Fatal(err, diags)
		}
		tree.work.Store(0)
		return tree, op.kids.list[len(op.kids.list)-1]
	}
	for _, api := range []string{"siblings", "tree-child"} {
		for _, order := range []string{"sorted", "reverse", "shuffled", "duplicate-heavy"} {
			t.Run(api+"/"+order, func(t *testing.T) {
				sizes := []int{64, 128, 256, 512}
				works := make([]int64, len(sizes))
				var prev [2]int64 // work of the previous size and of the one before it
				for i, n := range sizes {
					vals := values(n, order)
					first, firstChild := parse(vals)
					second, secondChild := parse(vals)
					var diff *Tree
					var err error
					if api == "tree-child" {
						diff, err = DiffTree(firstChild, secondChild, DiffOptions{})
					} else {
						diff, err = Diff(first, second, DiffOptions{})
					}
					// a single duplicate instance (DiffTree) may match an equal earlier one and
					// report a move; only the siblings diff of equal replies is empty
					if err != nil || api == "siblings" && diff != nil {
						t.Fatalf("%s(%d) = (%v, %v)", api, n, diff, err)
					}
					works[i] = first.work.Load() + second.work.Load()
					if i > 0 && 2*works[i] > 5*prev[0] {
						t.Errorf("work(%d)=%d exceeds 2.5 * the previous size's %d", n, works[i], prev[0])
					}
					if i > 1 && works[i] > 6*prev[1] {
						t.Errorf("work(%d)=%d exceeds 6 * the size before's %d", n, works[i], prev[1])
					}
					prev = [2]int64{works[i], prev[0]}
				}
				t.Logf("sizes %v: work %v", sizes, works)
			})
		}
	}
}

// TestParseOpManyParameters: an rpc with many distinct input leaves costs counted work linear in
// their count (n log n with headroom), given in schema order (each appends) or in reverse (each
// lands at the front, before the next rank).
func TestParseOpManyParameters(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		work := func(n int) int64 {
			var y strings.Builder
			y.WriteString("module mp { yang-version 1.1; namespace urn:mp; prefix mp; rpc r { input {")
			params := make([]string, n)
			for i := range n {
				fmt.Fprintf(&y, " leaf p%05d { type string; }", i)
				j := i
				if reverse {
					j = n - 1 - i
				}
				params[i] = fmt.Sprintf(`"p%05d":"x"`, j)
			}
			y.WriteString(" } } }")
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "mp.yang"), []byte(y.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			set := opsSet(t, opsFixture{dir: dir, searchdirs: []string{"."}, modules: []struct {
				name, revision string
				features       []string
			}{{name: "mp"}}})
			in := `{"mp:r":{` + strings.Join(params, ",") + `}}`
			tree, op, diags, err := parseOp(context.Background(), strings.NewReader(in), set, FormatJSON, opRPC, nil, Reject)
			if err != nil {
				t.Fatal(err, diags)
			}
			if l := op.kids.list; len(l) != n || l[0].schema.Name != "p00000" || l[n-1].schema.Name != fmt.Sprintf("p%05d", n-1) {
				t.Fatalf("%d children, not in schema order", len(l))
			}
			return tree.work.Load()
		}
		w1, w4 := work(250), work(1000)
		t.Logf("reverse %v: %d %d", reverse, w1, w4)
		if w4 > 6*w1 {
			t.Errorf("reverse %v: work %d for 250 parameters, %d for 1000: not linear", reverse, w1, w4)
		}
	}
}

// TestParseOpConcurrentFind: the instance index of an operation's children is built by the
// mutations, so concurrent lookups on a parsed operation (also after a removal) only read it
// (run with -race).
func TestParseOpConcurrentFind(t *testing.T) {
	set := opsSet(t, readOpsFixture(t, filepath.Join(opsManifest, "parse-json-reply-keep-order.yaml")))
	tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"rb:r":{"in":"a","in2":"b"}}`), set,
		FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	find := func(path string, want bool) {
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if n, err := tree.Find(path); (n != nil) != want || err != nil && want {
					t.Errorf("Find(%s): %v %v", path, n, err)
				}
			})
		}
		wg.Wait()
	}
	find("/rb:r/in", true)
	if err := tree.schemaLookup(&op.kids, op.kids.list[1].schema).Remove(); err != nil {
		t.Fatal(err)
	}
	find("/rb:r/in", true)
}

// errReader fails every read.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// opParent is the action node of {"a:c":{"act":{"al":"v"}}}, with its input kept.
func opParent(t *testing.T, set *schema.Set) (*Tree, *Node) {
	t.Helper()
	tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"a:c":{"act":{"al":"v"}}}`), set,
		FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	return tree, op
}

func treeJSON(t *testing.T, tr *Tree) string {
	t.Helper()
	var buf bytes.Buffer
	if err := tr.PrintJSON(&buf, PrintOptions{Shrink: true}); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// TestParseOpInputErrors: a reply whose input cannot be read, or is over Budget.MaxBytes, fails
// with that error before anything is parsed, and the parent is unchanged.
func TestParseOpInputErrors(t *testing.T) {
	set := opsSet(t, readOpsFixture(t, filepath.Join(opsManifest, "parse-xml-rpc-input.yaml")))
	tree, op := opParent(t, set)
	before := treeJSON(t, tree)
	readErr := errors.New("read failed")
	if _, _, _, err := parseOp(context.Background(), errReader{readErr}, set, FormatJSON, opReply, op, Reject); !errors.Is(err, readErr) {
		t.Fatalf("reader failure: %v", err)
	}
	big := strings.NewReader(`{"a:al":"` + strings.Repeat("1", DefaultMaxBytes) + `"}`)
	if _, _, _, err := parseOp(context.Background(), big, set, FormatJSON, opReply, op, Reject); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("oversized input: %v", err)
	}
	if after := treeJSON(t, tree); after != before {
		t.Fatalf("parent changed: %s, was %s", after, before)
	}
}

// TestParseOpDetachedParent: a reply parsed into an action whose tree was removed (a detached
// subtree; lyd_parse_op takes the context from the parent), in both formats.
func TestParseOpDetachedParent(t *testing.T) {
	set := opsSet(t, readOpsFixture(t, filepath.Join(opsManifest, "parse-xml-rpc-input.yaml")))
	for _, tc := range []struct {
		f    Format
		data string
	}{{FormatJSON, `{"a:al":25}`}, {FormatXML, `<al xmlns="urn:tests:a">25</al>`}} {
		_, op := opParent(t, set)
		for _, c := range slices.Collect(op.kids.all()) {
			unlink(c)
		}
		top := op.parent
		if err := top.Remove(); err != nil {
			t.Fatal(err)
		}
		tree, got, diags, err := parseOp(context.Background(), strings.NewReader(tc.data), set, tc.f, opReply, op, Reject)
		if err != nil || tree != nil || got != op {
			t.Fatalf("%v: %v %v tree %v op %v", tc.f, err, diags, tree, got)
		}
		if k := slices.Collect(op.kids.all()); len(k) != 1 || k[0].schema.Name != "al" || k[0].value.Canonical() != "25" {
			t.Fatalf("%v: children %v", tc.f, k)
		}
	}
}

// TestParseOpRollback: a reply that fails after many parsed nodes removes them all again with
// linear counted work, and the parent's own children stay.
func TestParseOpRollback(t *testing.T) {
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{"rb.yang": {Data: []byte(
		`module rb { yang-version 1.1; namespace urn:rb; prefix rb;
		  rpc r { input { leaf in { type string; } } output { leaf-list v { type uint8; } } } }`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load("rb", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	run := func(n int) int64 {
		tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"rb:r":{"in":"keep"}}`), set,
			FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		tree.work.Store(0)
		tree.work.visits, tree.work.shifts = true, true
		var b strings.Builder
		// XML: every element is a parsed node of its own (lydxml_subtree_r adds each to the
		// parsed set), so the rollback removes all of them
		b.WriteString(`<v xmlns="urn:rb">`)
		for i := range n {
			fmt.Fprintf(&b, "%d</v><v xmlns=\"urn:rb\">", i%250)
		}
		b.WriteString(`bad</v>`)
		_, _, _, err = parseOp(context.Background(), strings.NewReader(b.String()), set, FormatXML, opReply, op, Reject)
		work := tree.work.Load()
		if err == nil {
			t.Fatal("the bad value was accepted")
		}
		if got := treeJSON(t, tree); got != `{"rb:r":{"in":"keep"}}` {
			t.Fatalf("after rollback: %s", got)
		}
		return work
	}
	small, large := run(2000), run(32000)
	if small <= 0 || large > 20*small { // 16x input, with room for fixed setup work
		t.Fatalf("rollback work for 2000 nodes %d, for 32000 nodes %d: not linear", small, large)
	}
}
