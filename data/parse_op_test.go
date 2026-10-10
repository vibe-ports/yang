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
	"time"

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
				_, _, diags, err = parseOp(context.Background(), strings.NewReader(f.data), set, format, typ, op, f.unknown)
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
	if n := tree.findSchema(&op.kids, op.kids.list[0].schema); n == nil || n.schema.Name != "before" {
		t.Errorf("findSchema(before): %v", n)
	}
}

// TestParseOpDupKeepsOrder: a recursive copy of an operation keeps its children's order
// (lyd_dup_single inserts the copies with LYD_INSERT_NODE_LAST), also when the anchor split an
// output leaf-list around the kept input. lyoracle's sequences take datastore data only, so this
// has no oracle fixture.
func TestParseOpDupKeepsOrder(t *testing.T) {
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
	order := func(n *Node) []string {
		var out []string
		for c := range n.kids.all() {
			out = append(out, c.Name()+"="+c.Value())
		}
		return out
	}
	want := []string{"before=b", "v=1", "v=2", "in=keep", "v=3"}
	if got := order(op); !slices.Equal(got, want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	tr := newTree(set)
	d := tr.dup(op)
	if got := order(d); !slices.Equal(got, want) {
		t.Errorf("copy %v, want %v", got, want)
	}
}

// TestParseOpSharedName: an output leaf named like an input leaf is found by XPath in a reply
// (eval_name_test_with_predicate_get_scnode: a name in both input and output is matched by name).
func TestParseOpSharedName(t *testing.T) {
	f := readOpsFixture(t, filepath.Join(opsManifest, "parse-json-reply-keep-order.yaml"))
	f.modules = append(f.modules, struct {
		name, revision string
		features       []string
	}{name: "rbm"})
	set := opsSet(t, f)
	tree, _, diags, err := parseOp(context.Background(), strings.NewReader(`{"rbm:r2":{"v":"ok"}}`), set,
		FormatJSON, opReply, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	if ns, _, err := tree.FindXPath("/rbm:r2/v", XPathOptions{}); len(ns) != 1 || err != nil {
		t.Errorf("FindXPath: %v %v", ns, err)
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
	if err := tree.findSchema(&op.kids, op.kids.list[1].schema).Remove(); err != nil {
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

// TestParseOpRollback: a reply that fails after many parsed nodes removes them all again in
// linear time, and the parent's own children stay.
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
	run := func(n int) time.Duration {
		tree, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"rb:r":{"in":"keep"}}`), set,
			FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		var b strings.Builder
		// XML: every element is a parsed node of its own (lydxml_subtree_r adds each to the
		// parsed set), so the rollback removes all of them
		b.WriteString(`<v xmlns="urn:rb">`)
		for i := range n {
			fmt.Fprintf(&b, "%d</v><v xmlns=\"urn:rb\">", i%250)
		}
		b.WriteString(`bad</v>`)
		start := time.Now()
		_, _, _, err = parseOp(context.Background(), strings.NewReader(b.String()), set, FormatXML, opReply, op, Reject)
		d := time.Since(start)
		if err == nil {
			t.Fatal("the bad value was accepted")
		}
		if got := treeJSON(t, tree); got != `{"rb:r":{"in":"keep"}}` {
			t.Fatalf("after rollback: %s", got)
		}
		return d
	}
	small, large := run(2000), run(32000)
	for range 2 { // the fastest of three runs
		small, large = min(small, run(2000)), min(large, run(32000))
	}
	if large > 40*small {
		t.Fatalf("rollback of 2000 nodes %v, of 32000 %v: not linear", small, large)
	}
}
