// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/lyjson"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

// json is a JSON-format term store through the parser context.
func (lc *lydCtx) json(sn *schema.Node, lnode *Node, lex string) (*Node, error) {
	return lc.createTerm(sn, lnode, lex, types.FormatJSON, types.ModuleNames{Set: lc.tree.set}, types.JSONHints("string"))
}

func codes(ds []yang.Diagnostic) []string {
	var s []string
	for _, d := range ds {
		s = append(s, d.Err+" "+d.Code+" "+d.DataPath+d.SchemaPath+": "+d.Msg)
	}
	return s
}

// TestParseDriver: the lyd_parse frame — a tree on success, nil and every diagnostic on any
// error; multi-error goes on after LY_EVALID except after exactly LYVE_SYNTAX; the validation
// hook runs unless ParseOnly.
func TestParseDriver(t *testing.T) {
	f := newFixture()
	ok := func(lc *lydCtx, _ []byte) error {
		c, err := lc.createInner(f.c)
		if err != nil {
			return err
		}
		lc.nodeInsert(nil, nil, c)
		z, err := lc.json(f.z, c, "v")
		if err != nil {
			return err
		}
		lc.nodeInsert(c, nil, z)
		return lc.closeInner(c, nil)
	}
	validated := 0
	setup := func(lc *lydCtx) { lc.validate = func(*lydCtx) error { validated++; return nil } }
	tr, diags, err := parseWith(context.Background(), strings.NewReader("x"), f.set, parseOpts{}, ok, setup)
	if err != nil || len(diags) != 0 || validated != 1 {
		t.Fatalf("%v %v validated %d", err, diags, validated)
	}
	c := tr.top.list[0]
	if c.flags != FlagNew || c.kids.list[0].flags != FlagNew {
		t.Fatalf("flags %x %x: an explicit child clears the NP container's default", c.flags, c.kids.list[0].flags)
	}
	_, _, _ = parseWith(context.Background(), strings.NewReader(""), f.set, parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, ok, setup)
	if validated != 1 {
		t.Fatal("ParseOnly validated")
	}

	// two store errors: the second is reported only with multi-error
	bad := func(lc *lydCtx, _ []byte) error {
		c, _ := lc.createInner(f.c)
		lc.nodeInsert(nil, nil, c)
		for _, v := range []string{"x", "300"} {
			if _, err := lc.createTerm(f.ll, c, v, types.FormatJSON, nil, types.JSONHints("number")); lc.fatal(err) {
				return err
			}
		}
		return errLogged
	}
	want := []string{
		`LY_EVALID LYVE_DATA /b:c/ll: Invalid type uint8 value "x".`,
		`LY_EVALID LYVE_DATA /b:c/ll: Value "300" is out of type uint8 min/max bounds.`,
	}
	for _, multi := range []bool{false, true} {
		o := parseOpts{ParseOptions: ParseOptions{Validate: ValidateOptions{MultiError: multi}}}
		tr, diags, err := parseWith(context.Background(), strings.NewReader(""), f.set, o, bad, setup)
		var ve *ValidationError
		n := 1
		if multi {
			n = 2
		}
		if tr != nil || !errors.As(err, &ve) || !reflect.DeepEqual(codes(diags), want[:n]) {
			t.Fatalf("multi %v: tree %v err %v\n%v", multi, tr != nil, err, codes(diags))
		}
	}
	// exactly LYVE_SYNTAX stops even under multi-error
	lc := &lydCtx{log: &logger{set: f.set}, opts: parseOpts{ParseOptions: ParseOptions{Validate: ValidateOptions{MultiError: true}}}}
	lc.log.lexVal(1, "syntax", 3)
	if !lc.fatal(errLogged) {
		t.Fatal("LYVE_SYNTAX must stop")
	}
	lc.log.diags[0].Code = "LYVE_SYNTAX_JSON"
	if lc.fatal(errLogged) {
		t.Fatal("LYVE_SYNTAX_JSON goes on")
	}
}

// TestParseBudgets: MaxBytes and MaxNodes fail with yang.ErrBudget (U-0040, U-0041); a lexer
// nesting limit is logged like libyang and the error wraps yang.ErrBudget too.
func TestParseBudgets(t *testing.T) {
	f := newFixture()
	many := func(lc *lydCtx, _ []byte) error {
		c, _ := lc.createInner(f.c)
		lc.nodeInsert(nil, nil, c)
		for {
			_, err := lc.json(f.z, c, "v")
			if err != nil {
				return err
			}
		}
	}
	o := parseOpts{ParseOptions: ParseOptions{Budget: Budget{MaxNodes: 10, MaxBytes: 4}}}
	if _, _, err := parseWith(context.Background(), strings.NewReader("12345"), f.set, o, many, nil); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("bytes: %v", err)
	}
	if _, _, err := parseWith(context.Background(), strings.NewReader("1234"), f.set, o, many, nil); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("nodes: %v", err)
	}
	nested := func(lc *lydCtx, in []byte) error {
		lx, err := lyjson.New(in)
		for err == nil {
			var tok lyjson.Token
			if tok, err = lx.Next(); tok == lyjson.TokenEnd {
				return nil
			}
		}
		return lc.lexErr(err)
	}
	in := strings.Repeat("[", 5001) + strings.Repeat("]", 5001)
	_, diags, err := parseWith(context.Background(), strings.NewReader(in), f.set, parseOpts{}, nested, nil)
	var ve *ValidationError
	if !errors.Is(err, yang.ErrBudget) || !errors.As(err, &ve) || len(diags) != 1 || diags[0].Err != "LY_EINVAL" ||
		diags[0].Code != "LYVE_SUCCESS" {
		t.Fatalf("nesting: %v %+v", err, diags)
	}
}

// TestParserHelpers: check_schema, the key check and the !rc close guard, the when/types queues
// in parse post-order, insertion of a list only with all its keys, WhenTrue seeding and the
// default metadata.
func TestParserHelpers(t *testing.T) {
	f := newFixture()
	tr := newTree(f.set)
	lc := &lydCtx{ctx: context.Background(), tree: tr, log: &logger{set: f.set}}
	lc.opts.NoState = true
	if err := lc.checkSchema(f.sl); err == nil ||
		codes(lc.log.diags)[0] != `LY_EVALID LYVE_DATA /b:c/sl: Unexpected data state node "sl" found.` {
		t.Fatalf("%v %v", err, codes(lc.log.diags))
	}
	rpc := &schema.Node{Kind: schema.RPC, Name: "r", Module: f.b}
	if err := lc.checkSchema(rpc); err == nil || lc.log.diags[1].Msg != `Unexpected RPC element "r".` {
		t.Fatal("rpc in datastore data")
	}

	// when queue: post-order (child before the parent closes), WhenTrue seeded on request
	when := &schema.When{Src: "true()"}
	if e, err := xpath.Compile("true()", nil); err == nil {
		when.Compiled = e
	}
	f.c.Whens, f.z.Whens = []*schema.When{when}, []*schema.When{when}
	f.x.Whens = nil
	f.x.Parent.Whens = []*schema.When{when} // on the case: lysc_has_when looks through choice/case
	lc.opts.whenTrue = true
	c, _ := lc.createInner(f.c)
	lc.nodeInsert(nil, nil, c)
	z, _ := lc.json(f.z, c, "v")
	lc.nodeInsert(c, nil, z)
	lc.setDataFlags(z, false)
	x, _ := lc.json(f.x, c, "v")
	lc.nodeInsert(c, nil, x)
	lc.setDataFlags(x, true)
	lc.setDataFlags(c, false)
	if !reflect.DeepEqual(lc.nodeWhen, []*Node{z, x, c}) || z.flags&FlagWhenTrue == 0 || x.flags&FlagDefault == 0 {
		t.Fatalf("when queue / flags: %d %x %x", len(lc.nodeWhen), z.flags, x.flags)
	}
	if c.flags&FlagDefault != 0 {
		t.Fatal("an explicit sibling keeps the container explicit")
	}

	// a list is linked only with all its keys; the close guard skips validation after an error
	calls := 0
	lc.validateNewImplicit = func(*lydCtx, *Node) error { calls++; return nil }
	l, _ := lc.createInner(f.l)
	v, _ := lc.json(f.lv, l, "x")
	lc.nodeInsert(l, nil, v)
	lc.nodeInsert(c, nil, l)
	if l.parent != nil {
		t.Fatal("list linked without its key")
	}
	lc.log.diags = nil
	if err := lc.closeInner(l, nil); err == nil || calls != 0 ||
		codes(lc.log.diags)[0] != `LY_EVALID LYVE_DATA /b:l: List instance is missing its key "k".` {
		t.Fatalf("missing key: %v %v calls %d", err, codes(lc.log.diags), calls)
	}
	k, _ := lc.json(f.lk, l, "k")
	lc.nodeInsert(l, nil, k)
	lc.nodeInsert(c, nil, l)
	if l.parent != c || lc.closeInner(l, nil) != nil || calls != 1 {
		t.Fatalf("list with key: linked %v calls %d", l.parent == c, calls)
	}
	if lc.closeInner(c, errLogged) == nil || calls != 1 {
		t.Fatal("an error inside the node must skip validate_new_implicit")
	}
	lc.nodeFree(k)
	if k.parent != l {
		t.Fatal("a key is never freed")
	}
	o, err := lc.createOpaq(opaque{Name: "u", ModuleNS: "zz", Format: types.FormatJSON})
	if err != nil || o.flags != FlagNew {
		t.Fatalf("opaque: %v", err)
	}
	lc.nodeInsert(c, nil, o)
	if c.kids.opq[len(c.kids.opq)-1] != o {
		t.Fatal("opaque node not last")
	}

	// node_types: a require-instance value waits for the tree
	iid := &schema.Node{Kind: schema.Leaf, Name: "iid", Module: f.b, Type: &schema.Type{Base: schema.InstanceID, RequireInstance: true}, Config: true}
	if _, err := lc.json(iid, nil, "/b:top"); err != nil || len(lc.nodeTypes) != 1 {
		t.Fatalf("node_types: %v %d %v", err, len(lc.nodeTypes), codes(lc.log.diags))
	}
	lc.opts.ParseOnly = true
	if _, _ = lc.json(iid, nil, "/b:top"); len(lc.nodeTypes) != 1 {
		t.Fatal("ParseOnly queued a value")
	}
}

// TestOpaqError: lyd_parse_opaq_error, every branch reachable with datastore data.
func TestOpaqError(t *testing.T) {
	f := newFixture()
	f.b.Namespace = "urn:b"
	tr := newTree(f.set)
	c := newInner(f.c)
	tr.insert(nil, c, insertDefault)
	op := func(name, mod, val string, fm types.Format) *Node {
		n := newOpaque(opaque{Name: name, ModuleNS: mod, Value: val, Format: fm, Hints: types.HintData})
		return n
	}
	cases := []struct {
		parent *Node
		n      *Node
		want   string
	}{
		{c, op("q", "", "", types.FormatJSON), `LY_EVALID LYVE_REFERENCE /b:c: Unknown module of node "q".`},
		{nil, op("q", "zz", "", types.FormatJSON), `LY_EVALID LYVE_REFERENCE : No (implemented) module named "zz" of node "q" in the context.`},
		{nil, op("q", "urn:zz", "", types.FormatXML), `LY_EVALID LYVE_REFERENCE : No (implemented) module with namespace "urn:zz" of node "q" in the context.`},
		{c, op("q", "b", "", types.FormatJSON), `LY_EVALID LYVE_REFERENCE /b:c: Node "q" not found as a child of "c" node.`},
		{nil, op("q", "b", "", types.FormatJSON), `LY_EVALID LYVE_REFERENCE : Node "q" not found in the "b" module.`},
		{c, op("ll", "urn:b", "x", types.FormatXML), `LY_EVALID LYVE_DATA /b:c/ll: Invalid type uint8 value "x".`},
		{c, op("ll", "b", "7", types.FormatJSON), `LY_EINVAL LYVE_SUCCESS : Unexpected valid opaque node leaf-list "ll".`},
		{nil, op("c", "b", "v", types.FormatJSON), `LY_EVALID LYVE_DATA /b:c: Invalid value "v" for container "c".`},
		{c, op("l", "b", "", types.FormatJSON), `LY_EVALID LYVE_DATA /b:c/l: List instance is missing its key "k".`},
	}
	for _, tc := range cases {
		tr.insert(tc.parent, tc.n, insertDefault)
		l := &logger{set: f.set}
		if err := l.opaqError(tc.n); err == nil || len(l.diags) != 1 || codes(l.diags)[0] != tc.want {
			t.Errorf("%s: %v %v", tc.want, err, codes(l.diags))
		}
		unlink(tc.n)
	}
	// an opaque list with an invalid opaque key value
	l := op("l", "b", "", types.FormatJSON)
	tr.insert(c, l, insertDefault)
	tr.insert(l, op("k", "", "", types.FormatJSON), insertDefault)
	f.lk.Type = f.u8
	lg := &logger{set: f.set}
	if lg.opaqError(l) == nil || codes(lg.diags)[0] != `LY_EVALID LYVE_DATA /b:c/l/k: Invalid type uint8 empty value.` {
		t.Errorf("opaque key: %v", codes(lg.diags))
	}
	// lyd_node_schema of an opaque node under an opaque node
	in := op("k", "", "", types.FormatJSON)
	tr.insert(l, in, insertDefault)
	if nodeSchema(f.set, in) != f.lk || nodeSchema(f.set, l) != f.l {
		t.Error("nodeSchema")
	}
}
