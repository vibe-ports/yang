// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
)

// lrefFixture is, in module m (prefix "m"; module o, prefix "o", is imported, module x is not
// implemented):
//
//	container c { leaf a; list l { key k; leaf k; leaf v; leaf-list ll; container in { leaf deep } } anydata any; }
//	choice ch { case cs { leaf ct } }   leaf top   rpc r { input { leaf i } output { leaf o } }
//	leaf ref1 (type leafref ../c/a)     leaf ref2 (type union { leafref ../c/a; string })   leaf str
func lrefFixture() (m *schema.Module, n map[string]*schema.Node, ns schema.NSCtx) {
	m = &schema.Module{Name: "m", Prefix: "m", Implemented: true}
	o := &schema.Module{Name: "o", Prefix: "o", Implemented: true}
	x := &schema.Module{Name: "x", Prefix: "x"}
	ns = schema.NSCtx{"": m, "m": m, "o": o, "x": x}
	n = map[string]*schema.Node{}
	add := func(name string, parent *schema.Node, kind schema.Kind) *schema.Node {
		nd := &schema.Node{Kind: kind, Name: name, Module: m, Parent: parent}
		n[name] = nd
		switch {
		case parent != nil:
			parent.Children = append(parent.Children, nd)
		default:
			m.Top = append(m.Top, nd)
		}
		return nd
	}
	c := add("c", nil, schema.Container)
	add("a", c, schema.Leaf)
	l := add("l", c, schema.List)
	k := add("k", l, schema.Leaf)
	l.Keys = []*schema.Node{k}
	add("v", l, schema.Leaf)
	add("ll", l, schema.LeafList)
	add("deep", add("in", l, schema.Container), schema.Leaf)
	add("any", c, schema.AnyData)
	add("ct", add("cs", add("ch", nil, schema.Choice), schema.Case), schema.Leaf)
	add("top", nil, schema.Leaf)
	r := add("r", nil, schema.RPC)
	add("i", add("input", r, schema.Input), schema.Leaf)
	add("out", add("output", r, schema.Output), schema.Leaf)
	add("r2", nil, schema.RPC)
	add("str", nil, schema.Leaf)
	add("kl", nil, schema.List)
	add("da", nil, schema.Leaf).Type = &schema.Type{Base: schema.Leafref, Path: "deref(../db)/../top"}
	add("db", nil, schema.Leaf).Type = &schema.Type{Base: schema.Leafref, Path: "deref(../da)/../top"}
	add("ol", nil, schema.Leaf) // in module o's namespace, below
	n["ol"].Module = o
	o.Top = []*schema.Node{n["ol"]}
	m.Top = m.Top[:len(m.Top)-1]
	r1 := add("ref1", nil, schema.Leaf)
	r1.Type = &schema.Type{Base: schema.Leafref, Path: "../c/a", Prefixes: ns}
	r2 := add("ref2", nil, schema.Leaf)
	r2.Type = &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.String}, {Base: schema.Leafref, Path: "/c/a"}}}
	add("ref3", nil, schema.Leaf).Type = &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.String}}}
	add("ref4", nil, schema.Leaf).Type = &schema.Type{Base: schema.Union, Union: []*schema.Type{
		{Base: schema.Leafref, Path: "/c/zz"}, {Base: schema.Leafref, Path: "/c/a"}}}
	n["str"].Type = &schema.Type{Base: schema.String}
	return
}

func lrefCompile(t *testing.T, ctx *schema.Node, src string, ns schema.NSCtx, output, ext bool) (Path, *PathError) {
	t.Helper()
	p, _, err := lrefCompileLogged(t, ctx, src, ns, output, ext)
	return p, err
}

func lrefCompileLogged(t *testing.T, ctx *schema.Node, src string, ns schema.NSCtx, output, ext bool) (Path, []*PathError, *PathError) {
	t.Helper()
	e, msg := lyxp.ParsePath(src, lyxp.Opts{Begin: lyxp.BeginEither, Prefix: lyxp.PrefixOptional,
		Pred: lyxp.PredLeafref, Leafref: true, Extended: ext})
	if msg != "" {
		t.Fatalf("%q does not parse: %s", src, msg)
	}
	return CompileLeafref(ctx, e, ns, output, ext)
}

func names(p Path) string {
	var s []string
	for _, seg := range p {
		s = append(s, seg.Node.Name)
	}
	return strings.Join(s, "/")
}

func TestCompileLeafref(t *testing.T) {
	_, n, ns := lrefFixture()
	for _, c := range []struct {
		ctx, path string
		ns        schema.NSCtx
		output    bool
		want      string // path, or "!" + error message
	}{
		{"top", "/c/a", ns, false, "c/a"},
		{"top", "/m:c/m:a", ns, false, "c/a"},
		{"v", "../k", ns, false, "k"},
		{"v", "../../a", ns, false, "a"},
		{"deep", "../../v", ns, false, "v"},
		{"top", "../c/a", ns, false, "c/a"}, // '..' from a top-level node reaches the module top level
		{"top", "/ct", ns, false, "ct"},     // through choice and case
		{"top", "/c/any/a", ns, false, "!Not found node \"a\" in path."},
		{"top", "/c/l", ns, false, "c/l"},
		{"top", "/o:ol", ns, false, "ol"},
		{"v", "/c/l[k=current()/../k]/v", ns, false, "c/l/v"},
		{"v", "/c/l[k=current()/../../a]/v", ns, false, "c/l/v"},
		{"top", "../../c", ns, false, "!Too many parent references in path."},
		{"top", "/c/zz", ns, false, "!Not found node \"zz\" in path."},
		{"top", "/q:c", ns, false, "!No module connected with the prefix \"q\" found (prefix format schema stored mapping)."},
		{"top", "/c", schema.NSCtx{"": ns["x"]}, false, "!Not implemented module \"x\" in path."},
		{"top", "/c", schema.NSCtx{}, false, "!No module connected with the prefix \"\" found (prefix format schema stored mapping)."},
		{"top", "/c/a[k=current()/../k]", ns, false, "!List predicate defined for leaf \"a\" in path."},
		{"top", "/c[k=current()/../k]", ns, false, "!List predicate defined for container \"c\" in path."},
		{"top", "/kl[k=current()/../top]", ns, false, "!List predicate defined for keyless list \"kl\" in path."},
		{"top", "/c/l[v=current()/../top]", ns, false, "!Key expected instead of leaf \"v\" in path."},
		{"top", "/c/l[ll=current()/../top]", ns, false, "!Key expected instead of leaf-list \"ll\" in path."},
		{"top", "/c/l[k=current()/../../../../top]", ns, false, "!Too many parent references in path."},
		{"top", "/c/l[k=current()/../c]", ns, false, "!Leaf expected instead of container \"c\" in leafref predicate in path."},
		{"top", "/c/l[k=current()/../zz]", ns, false, "!Not found node \"zz\" in path."},
		// operations: only the one the node is in
		{"i", "/r/i", ns, false, "r/i"},
		{"i", "/r2/i", ns, false, "!Not found node \"r2\" in path."},
		{"top", "/r", ns, false, "r"}, // not inside an operation: any operation may be named
		{"out", "/r/out", ns, true, "r/out"},
		{"out", "/r/i", ns, true, "!Not found node \"i\" in path."}, // an output path does not see the input
	} {
		cn := n[c.ctx]
		p, err := lrefCompile(t, cn, c.path, c.ns, c.output, false)
		got := names(p)
		if err != nil {
			got = "!" + err.Msg
			if err.Node != cn {
				t.Errorf("%s %q: error node %v, want the context node", c.ctx, c.path, err.Node)
			}
		}
		if got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.ctx, c.path, got, c.want)
		}
	}
}

func TestCompileLeafrefAnyFallsBack(t *testing.T) {
	// libyang searches the top level of the module when the context node is an anydata
	_, n, ns := lrefFixture()
	p, err := lrefCompile(t, n["top"], "/c/any/top", ns, false, false)
	if err != nil || names(p) != "c/any/top" {
		t.Fatalf("got %v %v", names(p), err)
	}
}

func TestCompileLeafrefDeref(t *testing.T) {
	_, n, ns := lrefFixture()
	for _, c := range []struct{ ctx, path, want string }{
		{"top", "deref(../ref1)/../any", "ref1/c/a/any"},
		{"top", "deref(../ref2)/../any", "ref2/c/a/any"}, // union: the leafref member
		{"top", "deref(../ref3)/../any", "!Deref function target node \"ref3\" is union type with no leafrefs."},
		{"top", "deref(../str)/../any", "!Deref function target node \"str\" is not leafref."},
		{"top", "deref(../c)/../any", "!Deref function target node \"c\" is not leaf nor leaflist."},
		{"ref1", "deref(../ref1)/../any", "!Deref function target node \"ref1\" is node itself."},
	} {
		p, err := lrefCompile(t, n[c.ctx], c.path, ns, false, true)
		got := names(p)
		if err != nil {
			got = "!" + err.Msg
			if err.Node != n[c.ctx] {
				t.Errorf("%s %q: error node %v, want the context node", c.ctx, c.path, err.Node)
			}
		}
		if got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.ctx, c.path, got, c.want)
		}
	}
}

// A union member failing after deref() is skipped but its error is returned for logging.
func TestCompileLeafrefDerefLogged(t *testing.T) {
	_, n, ns := lrefFixture()
	p, logged, err := lrefCompileLogged(t, n["top"], "deref(../ref4)/../any", ns, false, true)
	if err != nil || names(p) != "ref4/c/a/any" {
		t.Fatalf("got %q %v", names(p), err)
	}
	if len(logged) != 1 || logged[0].Msg != `Not found node "zz" in path.` || logged[0].Node != n["ref4"] {
		t.Fatalf("logged %+v", logged)
	}
}

func TestCompileLeafrefDerefCycle(t *testing.T) { // D-0042
	_, n, ns := lrefFixture()
	_, _, err := lrefCompileLogged(t, n["top"], "deref(../da)/../top", ns, false, true)
	if err == nil || !strings.Contains(err.Msg, "dereference cycle") {
		t.Fatalf("got %v", err)
	}
}

// D-0043: the leafref reached through deref() is compiled with the outer prefix data.
func TestCompileLeafrefDerefOuterPrefix(t *testing.T) {
	a := &schema.Module{Name: "a", Prefix: "a", Implemented: true}
	b := &schema.Module{Name: "b", Prefix: "b", Implemented: true}
	nsA := schema.NSCtx{"": a, "a": a, "b": b}
	nsB := schema.NSCtx{"": b, "b": b}
	cb := &schema.Node{Kind: schema.Container, Name: "cb", Module: b}
	x := &schema.Node{Kind: schema.Leaf, Name: "x", Module: b, Parent: cb}
	cb.Children = []*schema.Node{x}
	ref := &schema.Node{Kind: schema.Leaf, Name: "ref", Module: b,
		Type: &schema.Type{Base: schema.Leafref, Path: "/cb/x", Prefixes: nsB}}
	b.Top = []*schema.Node{cb, ref}
	r := &schema.Node{Kind: schema.Leaf, Name: "r", Module: a}
	r.Type = &schema.Type{Base: schema.Leafref, Path: "deref(/b:ref)/../x", Prefixes: nsA}
	a.Top = []*schema.Node{r}
	_, _, err := lrefCompileLogged(t, r, "deref(/b:ref)/../x", nsA, false, true)
	if err == nil || err.Msg != `Not found node "cb" in path.` || err.Node != ref {
		t.Fatalf("got %v", err)
	}
}
