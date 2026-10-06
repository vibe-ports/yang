// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// schemaNS is LY_VALUE_SCHEMA_RESOLVED: prefix → module, "" the default module.
type schemaNS map[string]string

func (m schemaNS) Resolve(p string) (string, bool) { mod, ok := m[p]; return mod, ok && p != "" }
func (m schemaNS) Prefix(mod string) string        { return mod }
func (m schemaNS) Default() string                 { return m[""] }

const maMod = "must-atomize-axes"

// maSchema mirrors conformance/corpus/compile/schemas/must-atomize-axes.yang.
func maSchema() (map[string]*tschema, tinfo) {
	all := map[string]*tschema{}
	mk := func(parent *tschema, kind Kind, name string) *tschema {
		s := &tschema{kind: kind, mod: maMod, name: name, config: true}
		if parent != nil {
			parent.add(s)
		}
		all[name] = s
		return s
	}
	top := mk(nil, KindContainer, "top")
	a := mk(top, KindLeaf, "a")
	mk(top, KindLeaf, "b").lref = a
	l := mk(top, KindList, "l")
	l.keys = []string{"k"}
	mk(l, KindLeaf, "k")
	mk(l, KindLeaf, "v")
	ch := mk(top, KindChoice, "ch")
	mk(mk(ch, KindCase, "c1"), KindLeaf, "x")
	mk(mk(mk(ch, KindCase, "c2"), KindContainer, "y"), KindLeaf, "z")
	act := mk(top, KindAction, "act")
	mk(mk(act, KindInput, "input"), KindLeaf, "i")
	mk(mk(act, KindOutput, "output"), KindLeaf, "o")
	mk(mk(top, KindNotif, "ev"), KindLeaf, "n")
	ll := mk(nil, KindLeafList, "ll")
	st2 := mk(nil, KindContainer, "st2")
	mk(st2, KindLeaf, "s2")
	return all, tinfo{schema: []SchemaNode{top, ll, st2}}
}

func atomize(t *testing.T, x string, ctx *tschema, output bool, info tinfo) ([]Atom, []string, error) {
	t.Helper()
	e, err := Compile(x, schemaNS{"": maMod, "ma": maMod})
	if err != nil {
		t.Fatal(err)
	}
	var warns []string
	atoms, err := e.Atomize(AtomizeContext{Node: ctx, SchemaRules: true, Output: output, Schema: info,
		Warn: func(m string) { warns = append(warns, m) }})
	return atoms, warns, err
}

// TestAtomizeWarnings: the node-not-found warnings of the musts in
// must-atomize-axes.yang, as libyang v5.8.6 prints them (golden
// compile/golden/must-atomize-axes-warning.json); a must without warning
// reaches its nodes.
func TestAtomizeWarnings(t *testing.T) {
	all, info := maSchema()
	const p = "/must-atomize-axes:top"
	for _, c := range []struct {
		ctx, x string
		out    bool
		want   string
	}{
		{"top", "descendant::s2", false, ""}, // libyang: descendant:: runs on into the next top-level nodes
		{"top", "descendant::z", false, ""},
		{"top", "descendant::input", false, ""}, // raw children: input/output are nodes on this axis
		{"top", "descendant::zz", false, `Schema node "zz" not found; in expr "descendant::zz" with context node "` + p + `".`},
		{"top", "following-sibling::st2", false, ""},
		{"top", "preceding-sibling::ll", false, `Schema node "ll" not found; in expr "preceding-sibling::ll" with context node "` + p + `".`},
		{"top", "child::x", false, ""},
		{"top", "../ma:top/y/z", false, ""},
		{"top", "deref(b)/../l/v", false, ""},
		{"top", "//top", false, `Schema node "top" for parent "<config-root>" not found; in expr "//top" with context node "` + p + `".`},
		{"top", "zzz/bad:x", false, `Schema node "zzz" not found; in expr "zzz" with context node "` + p + `".`},
		{"top", "a/ma:* = 1 or true()", false, `Schema node "" for parent "` + p + `/a" not found; in expr "a/ma:* = 1 or true()" with context node "` + p + `".`},
		{"top", "count(q) and ma:a/../r", false, `Schema node "q" not found; in expr "count(q" with context node "` + p + `".` + "\n" +
			`Schema node "r" for parent "` + p + `" not found; in expr "count(q) and ma:a/../r" with context node "` + p + `".`},
		{"top", "l[k = current()/a]/v | nope", false, `Schema node "nope" not found; in expr "l[k = current()/a]/v | nope" with context node "` + p + `".`},
		{"i", "../o", false, `Schema node "o" for parent "` + p + `/act" not found; in expr "../o" with context node "` + p + `/act/input/i".`},
		{"i", "../i", false, ""},
		{"o", "../i", true, `Schema node "i" for parent "` + p + `/act" not found; in expr "../i" with context node "` + p + `/act/output/o".`},
		{"o", "zzz", true, `Schema node "zzz" not found; in expr "zzz" with context node "` + p + `/act/output/o".`},
	} {
		_, warns, err := atomize(t, c.x, all[c.ctx], c.out, info)
		if got := strings.Join(warns, "\n"); err != nil || got != c.want {
			t.Errorf("%s: %q\ngot  %q (%v)\nwant %q", c.ctx, c.x, got, err, c.want)
		}
	}
}

// TestAtomizeUses: the atoms and their in_ctx values (xpath.c set_scnode_*,
// derived by hand from the libyang code paths named per case).
func TestAtomizeUses(t *testing.T) {
	all, info := maSchema()
	for _, c := range []struct{ ctx, x, want string }{
		{"a", "../b", "a:-1 top:0 b:2"},
		// predicate: the context node is ATOM_CTX per iteration, '=' turns both sides into values
		{"a", "../l[k = current()/../a]/v", "a:1 top:0 l:0 k:1 v:2"},
		// deref(): the leafref target is added
		{"b", "deref(.)/../l", "b:0 a:0 top:0 l:2"},
		// union: lyxp_set_scnode_merge keeps both sides in context
		{"a", "../a | ../b", "a:2 top:0 b:2"},
		// count() leaves its argument a plain node, the literal clears to values
		{"top", "count(l) > 1", "top:-1 l:0"},
		// a not-found step skips its predicates and the rest of the path
		{"top", "zzz[bad:q]/bad:x", "top:-1"},
		{"top", "/ma:top", "top:2 /:0"},
	} {
		atoms, _, err := atomize(t, c.x, all[c.ctx], false, info)
		var got []string
		for _, a := range atoms {
			name := "/"
			if a.Node != nil {
				name = a.Node.Name()
			}
			got = append(got, fmt.Sprintf("%s:%d", name, a.Use))
		}
		if g := strings.Join(got, " "); err != nil || g != c.want {
			t.Errorf("%s: %q: got %q (%v), want %q", c.ctx, c.x, g, err, c.want)
		}
	}
}

func TestAtomizeErrors(t *testing.T) {
	all, info := maSchema()
	_, _, err := atomize(t, "bad:x", all["top"], false, info)
	var xe *Error
	if !errors.As(err, &xe) || xe.Msg != `Unknown/non-implemented module "bad".` || xe.VECode != "LYVE_XPATH" {
		t.Errorf("unknown prefix: %v", err)
	}
	if _, _, err = atomize(t, "$v", all["top"], false, info); !errors.As(err, &xe) || xe.Err != "LY_ENOTFOUND" {
		t.Errorf("variable: %v", err)
	}
	e, _ := Compile("//*//*//*", schemaNS{"": maMod})
	if _, err := e.Atomize(AtomizeContext{Node: all["top"], Schema: info, MaxSteps: 20}); !errors.Is(err, ErrBudget) {
		t.Errorf("budget: %v", err)
	}
}

// TestAtomizeBudget: on a 20 000-leaf schema the walk and the set work
// (index lookups, clones, merges) are charged per node: linear expressions
// fit a linear step budget, quadratic ones (a predicate re-copying the whole
// set per node) end in ErrBudget.
func TestAtomizeBudget(t *testing.T) {
	const n = 20_000
	top := &tschema{kind: KindContainer, mod: "m", name: "top", config: true}
	for i := range n {
		top.add(&tschema{kind: KindLeaf, mod: "m", name: fmt.Sprintf("l%d", i), config: true})
	}
	info := tinfo{schema: []SchemaNode{top}}
	run := func(x string, steps int) error {
		e, err := Compile(x, schemaNS{"": "m"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.Atomize(AtomizeContext{Node: top, Schema: info, MaxSteps: steps})
		return err
	}
	union := "//*" + strings.Repeat(" | //*", 5)
	for _, x := range []string{"//*", union, "count(" + union + ") = 1"} {
		if err := run(x, 100*n); err != nil {
			t.Errorf("%s: %v within %d steps", x, err, 100*n)
		}
		if err := run(x, n/2); !errors.Is(err, ErrBudget) {
			t.Errorf("%s: %v within %d steps, want ErrBudget", x, err, n/2)
		}
	}
	if err := run("//*[. = 1]", 0); !errors.Is(err, ErrBudget) {
		t.Errorf("quadratic predicate: %v, want ErrBudget", err)
	}
}
