// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

var reIntPath = regexp.MustCompile(`Internal error \([^):]*/`)

// TestValidateOpGoldens replays operation fixtures as the oracle's parse_one does with
// validation: the operational tree parsed only (lyd_parse_data with LYD_PARSE_ONLY), the
// operation parsed (lyd_parse_op; a reply into its rpc when the fixture has one), then
// validateOp (lyd_validate_op) against it. The verdict, the diagnostics, and for a valid one the
// typed dump and the printed operation tree must equal the golden's.
func TestValidateOpGoldens(t *testing.T) {
	var paths []string
	for _, p := range []string{
		"ut-validation-ops/rpc-01", "ut-validation-ops/action-0[1-3]", "ut-validation-ops/reply-0[1-3]",
		"ut-validation-ops/when-rpc-reply-01", "ut-parser/*-rpc-0[1-9]", "ut-parser/*-action-0[1-9]",
		"ut-parser/*-notification-0[1-9]", "ut-parser/*-reply-0[1-9]", "ops/validate-*",
	} {
		m, err := filepath.Glob(filepath.Join("../conformance/corpus/manifest.d", p+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, m...)
	}
	if len(paths) < 18 {
		t.Fatalf("%d fixtures", len(paths))
	}
	for _, path := range paths {
		name := filepath.Base(filepath.Dir(path)) + "/" + strings.TrimSuffix(filepath.Base(path), ".yaml")
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
			ctx := context.Background()
			var oper *Tree
			if f.hasOper {
				var diags []yang.Diagnostic
				oper, diags, err = parseWith(ctx, strings.NewReader(f.operational), set,
					parseOpts{ParseOptions: ParseOptions{Unknown: Skip, ParseOnly: true}}, map[Format]formatParser{
						FormatJSON: parseJSON, FormatXML: parseXML}[format], nil)
				if err != nil {
					t.Fatal("operational", err, diags)
				}
			}
			typ := map[string]opType{"rpc": opRPC, "notif": opNotif, "reply": opReply}[f.dataType]
			var tree *Tree
			var op *Node
			var all []yang.Diagnostic
			if f.hasRPC {
				var diags []yang.Diagnostic
				tree, op, diags, err = parseOp(ctx, strings.NewReader(f.rpc), set, format, opRPC, nil, f.unknown)
				all = append(all, diags...)
				if err == nil {
					for _, c := range slices.Collect(op.kids.all()) {
						unlink(c)
					}
					_, _, diags, err = parseOp(ctx, strings.NewReader(f.data), set, format, typ, op, f.unknown)
					all = append(all, diags...)
				}
			} else {
				var diags []yang.Diagnostic
				tree, op, diags, err = parseOp(ctx, strings.NewReader(f.data), set, format, typ, nil, f.unknown)
				all = append(all, diags...)
			}
			if errors.Is(err, yang.ErrUnsupported) {
				t.Skip(err) // anydata/anyxml instances (U-0043)
			}
			if err == nil {
				var diags []yang.Diagnostic
				diags, err = validateOp(ctx, set, tree.top.list[0], oper, typ, Budget{})
				all = append(all, diags...)
			}
			if (err == nil) != (want.Verdict == "valid") {
				t.Fatalf("err %v, verdict %s %q", err, want.Verdict, want.diags())
			}
			var got []string
			for _, d := range all {
				got = append(got, fmt.Sprintf("%s %s %s %s", d.Code, d.DataPath, d.SchemaPath, d.Msg))
			}
			// LOGINT prints __FILE__, the oracle's build path; the port uses the file name as the
			// rest of the package does
			wd := want.diags()
			for i, d := range wd {
				wd[i] = reIntPath.ReplaceAllString(d, "Internal error (")
			}
			if !reflect.DeepEqual(got, wd) {
				t.Errorf("diagnostics\n got  %q\n want %q", got, wd)
			}
			if err != nil {
				return
			}
			_ = op
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

// TestValidateOpSiblings: a notification with a top-level sibling (a tree built by hand, as the
// tree API allows). Without a dependency tree libyang validates the operation's top node alone,
// so the must over the sibling fails; with the sibling as the dependency (not op itself, so not
// redundant) it is merged next to it and holds (native libyang v5.8.6: LY_EVALID, then
// LY_SUCCESS).
func TestValidateOpSiblings(t *testing.T) {
	f := readOpsFixture(t, filepath.Join(opsManifest, "validate-notif-must-true.yaml"))
	set := opsSet(t, f)
	ctx := context.Background()
	build := func() (*Tree, *Node) {
		tr, diags, err := parseWith(ctx, strings.NewReader(`{"opv:mode":"on"}`), set,
			parseOpts{ParseOptions: ParseOptions{Unknown: Reject, ParseOnly: true}}, parseJSON, nil)
		if err != nil {
			t.Fatal(err, diags)
		}
		_, op, diags, err := parseOp(ctx, strings.NewReader(`{"opv:alarm":{}}`), set, FormatJSON, opNotif, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		unlink(op)
		tr.insert(nil, op, insertDefault)
		return tr, op
	}
	_, op := build()
	diags, err := validateOp(ctx, set, op, nil, opNotif, Budget{})
	if err == nil || len(diags) != 1 || diags[0].Msg != `Must condition "/o:mode = 'on'" not satisfied.` {
		t.Fatalf("no dependency tree: %v %v", err, diags)
	}
	if op.treeOf() == nil || len(slices.Collect(op.treeOf().Top())) != 2 {
		t.Fatal("the operation is not back in its tree")
	}
	tr, op := build()
	if diags, err := validateOp(ctx, set, op, tr, opNotif, Budget{}); err != nil {
		t.Fatalf("the sibling as dependency: %v %v", err, diags)
	}
	// op is its own tree's first node: that tree as the dependency is dropped as redundant
	optree, op2, _, err := parseOp(ctx, strings.NewReader(`{"opv:alarm":{}}`), set, FormatJSON, opNotif, nil, Reject)
	if err != nil {
		t.Fatal(err)
	}
	if diags, err := validateOp(ctx, set, op2, optree, opNotif, Budget{}); err == nil {
		t.Fatalf("op as the dependency's first node is redundant: %v", diags)
	}
}

// TestValidateOpDetached: a reply parsed into a detached action (#198's detached parent) and then
// validated, as libyang validates unlinked operation trees: the output default is added, the
// operation stays detached; and a detached rpc input failing its must is reported.
func TestValidateOpDetached(t *testing.T) {
	f := readOpsFixture(t, filepath.Join(opsManifest, "validate-reply-parent.yaml"))
	set := opsSet(t, f)
	ctx := context.Background()
	_, op, diags, err := parseOp(ctx, strings.NewReader(`{"opv:run":{"fast":[null]}}`), set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	for _, c := range slices.Collect(op.kids.all()) {
		unlink(c)
	}
	if err := op.Remove(); err != nil { // the rpc is its tree's top node: now detached
		t.Fatal(err)
	}
	if _, _, diags, err := parseOp(ctx, strings.NewReader(`{"opv:result":"ok"}`), set, FormatJSON, opReply, op, Reject); err != nil {
		t.Fatal(err, diags)
	}
	if diags, err := validateOp(ctx, set, op, nil, opReply, Budget{}); err != nil {
		t.Fatal(err, diags)
	}
	var kids []string
	for c := range op.kids.all() {
		kids = append(kids, typedLine(set, c))
	}
	if want := []string{"/opv:run/result new = ok", "/opv:run/code default = 0"}; !reflect.DeepEqual(kids, want) || op.treeOf() != nil {
		t.Fatalf("children %q, want %q; tree %v", kids, want, op.treeOf())
	}
	// a detached rpc whose input misses its mandatory choice
	_, op2, _, err := parseOp(ctx, strings.NewReader(`{"opv:run":{"speed":3}}`), set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err)
	}
	if err := op2.Remove(); err != nil {
		t.Fatal(err)
	}
	diags, err = validateOp(ctx, set, op2, nil, opRPC, Budget{})
	if err == nil || len(diags) != 1 || diags[0].Msg != `Mandatory choice "how" data do not exist.` {
		t.Fatalf("%v %v", err, diags)
	}
}

// TestValidateOpMaxNodes: the implicit nodes of an operation count against Budget.MaxNodes.
func TestValidateOpMaxNodes(t *testing.T) {
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{"dn.yang": {Data: []byte(
		`module dn { yang-version 1.1; namespace urn:dn; prefix dn;
		  rpc r { input { leaf a { type int8; default 1; } leaf b { type int8; default 2; } leaf c { type int8; default 3; } } } }`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load("dn", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	for _, tc := range []struct {
		max  int
		fail bool
	}{{2, true}, {3, false}} {
		_, op, diags, err := parseOp(context.Background(), strings.NewReader(`{"dn:r":{}}`), set, FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		_, err = validateOp(context.Background(), set, op, nil, opRPC, Budget{MaxNodes: tc.max})
		if errors.Is(err, yang.ErrBudget) != tc.fail {
			t.Fatalf("MaxNodes %d: %v", tc.max, err)
		}
	}
}

// TestValidateOpDetachedSubtree: an action inside a detached list instance is merged under the
// matching instance of the dependency tree for the validation and goes back under its own list
// instance after it (the operation's tree stays detached).
func TestValidateOpDetachedSubtree(t *testing.T) {
	f := readOpsFixture(t, filepath.Join(opsManifest, "validate-action-leafref.yaml"))
	set := opsSet(t, f)
	ctx := context.Background()
	dep, diags, err := parseWith(ctx, strings.NewReader(f.operational), set,
		parseOpts{ParseOptions: ParseOptions{Unknown: Skip, ParseOnly: true}}, parseJSON, nil)
	if err != nil {
		t.Fatal(err, diags)
	}
	_, op, diags, err := parseOp(ctx, strings.NewReader(f.data), set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	item := op.parent
	if err := item.Remove(); err != nil { // the list instance is the tree's top node: now detached
		t.Fatal(err)
	}
	before := len(slices.Collect(dep.Top()))
	if diags, err := validateOp(ctx, set, op, dep, opRPC, Budget{}); err != nil {
		t.Fatal(err, diags)
	}
	if op.parent != item || item.treeOf() != nil || !slices.Contains(slices.Collect(item.kids.all()), op) {
		t.Fatalf("the action is not back under its list instance: parent %v", op.parent)
	}
	if after := len(slices.Collect(dep.Top())); after != before {
		t.Fatalf("dependency tree changed: %d top nodes, was %d", after, before)
	}
	for _, n := range slices.Collect(dep.Top()) {
		for d := range n.All() {
			if d.schema != nil && d.schema.Kind == schema.Action {
				t.Fatal("the action stayed in the dependency tree")
			}
		}
	}
}

// TestValidateOpOpaqueAncestor: an rpc parsed under an opaque node x that precedes an opaque
// sibling y goes back to its place after the validation: [x, y] stays [x, y].
func TestValidateOpOpaqueAncestor(t *testing.T) {
	f := readOpsFixture(t, filepath.Join(opsManifest, "parse-xml-rpc-input.yaml"))
	set := opsSet(t, f)
	ctx := context.Background()
	tree, op, diags, err := parseOp(ctx, strings.NewReader(`{"a:x":{"a:r1":{"l1":"a"}}}`), set, FormatJSON, opRPC, nil, Opaque)
	if err != nil {
		t.Fatal(err, diags)
	}
	x := op.parent
	if x == nil || x.schema != nil {
		t.Fatalf("rpc parent %v", x)
	}
	tree.insert(nil, newOpaque(opaque{Name: "y", ModuleNS: "a", Format: types.FormatJSON}), insertDefault)
	names := func() (out []string) {
		for n := range tree.Top() {
			out = append(out, n.opaq.Name)
		}
		return out
	}
	if got := names(); !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Fatalf("before: %v", got)
	}
	_, _ = validateOp(ctx, set, op, nil, opRPC, Budget{})
	if got := names(); !reflect.DeepEqual(got, []string{"x", "y"}) || op.parent != x {
		t.Fatalf("after: %v, rpc parent %v", got, op.parent)
	}
}
