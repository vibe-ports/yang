// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"reflect"
	"testing"

	"github.com/vibe-ports/yang/internal/xpath"
)

// TestFindXPath: lyd_find_xpath3 / lyd_eval_xpath4 over a tree — node sets in document order,
// variables, the not-a-node-set refusal, casts of the single-type form, and where each error is
// logged (parse errors at the context node, evaluation errors without a node).
func TestFindXPath(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	for _, v := range []string{"5", "2", "9"} {
		tr.insert(c, f.term(t, f.ll, v), insertDefault)
	}
	vars := []xpath.Var{{Name: "min", Value: "3"}, {Name: "bad", Value: "1 +"}}

	l := &logger{set: f.set}
	got, err := tr.findXPath(l, nil, "/b:c/ll[. > $min]", vars)
	if err != nil || len(got) != 2 || got[0].value.Canonical() != "5" || got[1].value.Canonical() != "9" {
		t.Fatalf("query: %v %v %v", names(func(y func(*Node) bool) {
			for _, n := range got {
				y(n)
			}
		}), err, codes(l.diags))
	}
	if got, err := tr.findXPath(l, c, "ll", nil); err != nil || len(got) != 3 {
		t.Fatalf("relative: %d %v", len(got), err)
	}

	for _, tc := range []struct {
		ctx  *Node
		src  string
		want string
	}{
		{c, "ll[", `LY_EVALID LYVE_XPATH /b:c: Unexpected XPath expression end.`},
		{nil, "count(/b:c/ll)", `LY_EINVAL LYVE_SUCCESS : XPath "count(/b:c/ll)" result is not a node set.`},
		{c, "ll[$nope]", `LY_ENOTFOUND LYVE_SUCCESS : Variable "nope" not defined.`},
		{c, "ll[$bad]", `LY_EVALID LYVE_XPATH /b:c: Unexpected XPath expression end.`},
		{nil, "/zz:c", `LY_EVALID LYVE_XPATH : Unknown/non-implemented module "zz".`},
	} {
		l := &logger{set: f.set}
		if _, err := tr.findXPath(l, tc.ctx, tc.src, vars); err == nil || len(l.diags) != 1 || codes(l.diags)[0] != tc.want {
			t.Errorf("%s: %v %v", tc.src, err, codes(l.diags))
		}
	}

	// the single-type form casts; the ret_type form keeps the type
	for _, tc := range []struct {
		single bool
		to     xpath.ResultType
		want   xpath.Result
	}{
		{true, xpath.Boolean, xpath.Result{Type: xpath.Boolean, Bool: true}},
		{true, xpath.String, xpath.Result{Type: xpath.String, Str: "2"}},
		{true, xpath.Number, xpath.Result{Type: xpath.Number, Num: 2}},
		{false, xpath.NodeSet, xpath.Result{Type: xpath.NodeSet, Nodes: []xpath.Node{xn{c.kids.list[0], f.set}}}},
	} {
		src := "/b:c/ll"
		r, err := tr.evalXPath4(&logger{set: f.set}, nil, src, nil, tc.single, tc.to)
		r.Steps = 0
		if err != nil || !reflect.DeepEqual(r.Type, tc.want.Type) || r.Bool != tc.want.Bool || r.Str != tc.want.Str ||
			r.Num != tc.want.Num || tc.want.Type == xpath.NodeSet && len(r.Nodes) != 3 {
			t.Errorf("%v %d: %+v %v", tc.single, tc.to, r, err)
		}
	}

	// lyxp_eval refuses a tree whose first top-level node is not a top-level schema node
	bad := newTree(f.set)
	bad.insert(nil, f.term(t, f.ll, "1"), insertDefault)
	l = &logger{set: f.set}
	if _, err := bad.findXPath(l, nil, "/b:c", nil); err == nil ||
		codes(l.diags)[0] != `LY_EINVAL LYVE_SUCCESS : Data node "ll" has no parent but is not instance of a top-level schema node.` {
		t.Errorf("tree check: %v %v", err, codes(l.diags))
	}
}
