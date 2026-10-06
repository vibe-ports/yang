// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ttype is a SchemaType for tests; the pointer is the type identity.
type ttype struct {
	base  BaseType
	union []SchemaType
	real  SchemaType
}

func (t *ttype) Base() BaseType      { return t.base }
func (t *ttype) Union() []SchemaType { return t.union }
func (t *ttype) Realtype() SchemaType {
	if t.real == nil {
		return nil
	}
	return t.real
}

func bt(b BaseType) *ttype           { return &ttype{base: b} }
func unionOf(m ...SchemaType) *ttype { return &ttype{base: TypeUnion, union: m} }
func leafrefTo(t SchemaType) *ttype  { return &ttype{base: TypeLeafref, real: t} }
func (s *tschema) leaf(n string, t *ttype) *tschema {
	return s.add(&tschema{kind: KindLeaf, mod: s.mod, name: n, config: true, typ: t})
}

func newTop(mod string) *tschema {
	return &tschema{kind: KindContainer, mod: mod, name: "top", config: true}
}

// warnsOf atomizes every must of top in order and returns all warnings.
func warnsOf(t *testing.T, top *tschema, musts []string) []string {
	t.Helper()
	var out []string
	for _, x := range musts {
		e, err := Compile(x, schemaNS{"": top.mod})
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.Atomize(AtomizeContext{Node: top, SchemaRules: true, Schema: tinfo{schema: []SchemaNode{top}},
			Warn: func(m string) { out = append(out, m) }})
		if err != nil {
			t.Fatalf("%s: %v", x, err)
		}
	}
	return out
}

// goldenWarnings are the warning messages of the first module of a compile golden.
func goldenWarnings(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "conformance", "corpus", "compile", "golden", name)) //nolint:gosec // fixture path
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Modules []struct {
			Diagnostics []struct{ Level, Msg string }
		}
	}
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range g.Modules[0].Diagnostics {
		if d.Level == "warning" {
			out = append(out, d.Msg)
		}
	}
	return out
}

// TestOperandWarningsGolden replays the musts of the must/operand-* fixtures (the schemas of
// conformance/corpus/compile/schemas, hand-built here) against Atomize: the warnings are the ones
// libyang logged at compile, in order.
func TestOperandWarningsGolden(t *testing.T) {
	t.Run("node-type", func(t *testing.T) {
		top := newTop("must-operand-node-type")
		top.leaf("n", bt(TypeUint8))
		c1 := top.add(&tschema{kind: KindContainer, mod: top.mod, name: "c1", config: true})
		c1.leaf("in", bt(TypeUint8))
		l := top.add(&tschema{kind: KindList, mod: top.mod, name: "l", config: true, keys: []string{"k"}})
		l.leaf("k", bt(TypeString))
		top.add(&tschema{kind: KindLeafList, mod: top.mod, name: "ll", config: true, typ: bt(TypeUint8)})
		replayWarn(t, top, "must-operand-node-type-warning.json", []string{"c1 = 1", "n + c1", "-c1 > 0", "l = ll",
			"n * 2 = 4 and 1 < c1/in and c1 != 3"})
	})
	t.Run("not-numeric", func(t *testing.T) {
		top := newTop("must-operand-not-numeric")
		s, n := bt(TypeString), bt(TypeUint8)
		top.leaf("s", s)
		top.leaf("n", n)
		top.leaf("d", bt(TypeDec64))
		top.leaf("b", bt(TypeBool))
		top.leaf("un", unionOf(bt(TypeString), bt(TypeInt16)))
		top.leaf("us", unionOf(bt(TypeString), bt(TypeBool)))
		top.leaf("lr", leafrefTo(s))
		top.leaf("ln", leafrefTo(n))
		top.add(&tschema{kind: KindLeafList, mod: top.mod, name: "ll", config: true, typ: bt(TypeString)})
		replayWarn(t, top, "must-operand-not-numeric-warning.json", []string{"s + 1 = 2", "n < s", "-s = 1", "un * d > 1",
			"us - 1", "lr < ln", "ln div 2 > d", "b mod 2 = ll", "(n + 1) * (d + 2) > (n - 1) * (s + 1) and ln = 1"})
	})
	t.Run("incompatible", func(t *testing.T) {
		top := newTop("must-operand-incompatible")
		e := bt(TypeEnum)
		top.leaf("s", bt(TypeString))
		top.leaf("n", bt(TypeUint8))
		top.leaf("d", bt(TypeDec64))
		top.leaf("b", bt(TypeBool))
		top.leaf("en", e)
		top.leaf("bt", bt(TypeBits))
		top.leaf("u1", unionOf(e, bt(TypeBits)))
		top.leaf("u2", unionOf(bt(TypeBits), bt(TypeString)))
		top.leaf("u3", unionOf(e, bt(TypeUint8)))
		top.leaf("lr", leafrefTo(e))
		top.add(&tschema{kind: KindLeafList, mod: top.mod, name: "ll", config: true, typ: bt(TypeString)})
		c := top.add(&tschema{kind: KindContainer, mod: top.mod, name: "c", config: true})
		c.leaf("x", bt(TypeString))
		replayWarn(t, top, "must-operand-incompatible-warning.json", []string{"s = n", "n = d", "s = en", "b != s", "u1 = u2",
			"u1 = u3", "lr = en", "lr = bt", "ll = s", "c = s", "s = c/x and en = b"})
	})
}

func replayWarn(t *testing.T, top *tschema, golden string, musts []string) {
	t.Helper()
	want := goldenWarnings(t, golden)
	if len(want) == 0 {
		t.Fatal("golden has no warnings")
	}
	if got := warnsOf(t, top, musts); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestOperandWarnings: cases the golden fixtures do not reach.
func TestOperandWarnings(t *testing.T) {
	top := newTop("m")
	top.leaf("n", bt(TypeUint8))
	top.leaf("s", bt(TypeString))
	top.add(&tschema{kind: KindContainer, mod: "m", name: "c", config: true})
	act := top.add(&tschema{kind: KindAction, mod: "m", name: "act", config: true})
	act.add(&tschema{kind: KindInput, mod: "m", name: "input", config: true}).leaf("i", bt(TypeUint8))
	trailer := func(pos int, sub string) string {
		return `Previous warning generated by XPath subexpression[` + itoa(pos) + `] "` + sub + `" with context node "/m:top".`
	}
	for _, c := range []struct {
		x    string
		want []string
	}{
		{"s+1", []string{`Node "s" is not of a numeric type, but used where it was expected.`, trailer(0, "s+1")}}, // excerpt cut at the end
		{"1 + 2 = 3 and s > 5 or true()", []string{`Node "s" is not of a numeric type, but used where it was expected.`, trailer(14, "s > 5 or true()")}},
		{"-s", []string{`Node "s" is not of a numeric type, but used where it was expected.`, trailer(0, "-s")}},
		{"--s", nil},       // an even number of '-' is no operation
		{"1 + 2 * 3", nil}, // no node set
		{"n + 1 = 2", nil}, // numeric
		{"s = 'x'", nil},   // a literal is no node
		{"c div 1 > 0", []string{`Node type container "c" used as operand.`, trailer(0, "c div 1 > 0")}}, // the result of 'div' is a value, not a node set
		{"act = 1", []string{`Node type action "act" used as operand.`, trailer(0, "act = 1")}},
	} {
		got := warnsOf(t, top, []string{c.x})
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%q:\ngot  %q\nwant %q", c.x, got, c.want)
		}
	}
	// a long expression: 20 bytes of the text from the token (the cut falls between two runes here)
	long := "n = 1 and s + 1 = 2 and ('жжжжжжжжжжжжж' = 'a')"
	got := warnsOf(t, top, []string{long})
	if len(got) != 2 || !strings.Contains(got[1], `subexpression[10] "s + 1 = 2 and ('жж"`) {
		t.Errorf("long: %q", got)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestTypeHelpers: the type predicates through unions and leafrefs, the union walk.
func TestTypeHelpers(t *testing.T) {
	i8, str, en := bt(TypeInt8), bt(TypeString), bt(TypeEnum)
	u := unionOf(en, unionOf(str, i8))
	for _, c := range []struct {
		name string
		got  bool
		want bool
	}{
		{"numeric int", isNumericType(i8), true},
		{"numeric dec64", isNumericType(bt(TypeDec64)), true},
		{"numeric string", isNumericType(str), false},
		{"numeric union member", isNumericType(u), true},
		{"numeric leafref", isNumericType(leafrefTo(i8)), true},
		{"numeric bool", isNumericType(bt(TypeBool)), false},
		{"string enum", isStringType(en), true},
		{"string binary", isStringType(bt(TypeBinary)), false},
		{"string nested union", isStringType(unionOf(i8, unionOf(bt(TypeBits)))), true},
		{"string leafref", isStringType(leafrefTo(str)), true},
		{"string int union", isStringType(unionOf(i8, bt(TypeUint8))), false},
		{"specific", isSpecificType(u, TypeInt8), true},
		{"specific leafref", isSpecificType(leafrefTo(u), TypeEnum), true},
		{"specific none", isSpecificType(u, TypeBits), false},
		{"specific self", isSpecificType(i8, TypeInt8), true},
	} {
		if c.got != c.want {
			t.Errorf("%s: %v", c.name, c.got)
		}
	}
	for _, c := range []struct {
		name   string
		t1, t2 SchemaType
		want   bool
	}{
		{"same base", bt(TypeString), bt(TypeString), true},
		{"other base", bt(TypeString), en, false},
		{"union member", u, bt(TypeEnum), true},
		{"leafref to union", leafrefTo(u), unionOf(bt(TypeBool), bt(TypeInt8)), true},
		{"no common", unionOf(en, str), unionOf(bt(TypeBits), bt(TypeBool)), false},
	} {
		if got := equalType(c.t1, c.t2); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
}

// TestEqualTypeRepeated: a union that repeats one type object (libyang's walk cycles forever when
// no base matches, D-0015) gives the answer of the plain member comparison.
func TestEqualTypeRepeated(t *testing.T) {
	str, en := bt(TypeString), bt(TypeEnum)
	rep := unionOf(str, str)
	if equalType(rep, en) || equalType(en, rep) {
		t.Error("no common base")
	}
	if !equalType(rep, bt(TypeString)) || !equalType(unionOf(en, str, str), unionOf(bt(TypeBits), str)) {
		t.Error("common base")
	}
	if !equalType(unionOf(en, str), unionOf(bt(TypeBits), bt(TypeString))) {
		t.Error("the second outer member compares against every inner member")
	}
	// and from Atomize: a warning instead of a hang
	top := newTop("m")
	top.leaf("u", rep)
	top.leaf("en", en)
	got := warnsOf(t, top, []string{"u = en"})
	want := []string{`Incompatible types of operands "u" and "en" for comparison.`,
		`Previous warning generated by XPath subexpression[0] "u = en" with context node "/m:top".`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got %q", got)
	}
}
