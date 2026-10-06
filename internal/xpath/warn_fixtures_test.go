// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"strings"
	"testing"
)

// fixtureRun is one libyang atomize call of a fixture: the context node and the expression.
type fixtureRun struct {
	ctx  *tschema
	expr string
}

// warnFixture is a warning-bearing compile fixture (conformance/corpus/compile): the schema of its
// module hand-built here and the musts/whens in the order libyang atomizes them.
type warnFixture struct {
	golden string
	build  func() (top *tschema, runs []fixtureRun)
}

// mustRuns evaluates every expression from top (musts).
func mustRuns(top *tschema, xs ...string) []fixtureRun {
	var runs []fixtureRun
	for _, x := range xs {
		runs = append(runs, fixtureRun{top, x})
	}
	return runs
}

// whenRuns evaluates the when of each leaf from the leaf itself, last leaf first (libyang's order
// for the whens of one container).
func whenRuns(ws []*tschema, whens []string) []fixtureRun {
	var runs []fixtureRun
	for i := len(ws) - 1; i >= 0; i-- {
		runs = append(runs, fixtureRun{ws[i], whens[i]})
	}
	return runs
}

func c2bFixtures() []warnFixture {
	str, u8 := bt(TypeString), bt(TypeUint8)
	return []warnFixture{
		{"must-unknown-node-warning.json", func() (*tschema, []fixtureRun) {
			c := &tschema{kind: KindContainer, mod: "must-unknown-node", name: "c", config: true}
			c.leaf("a", str)
			return c, mustRuns(c, "nope = 1")
		}},
		{"must-unknown-node-parent-warning.json", func() (*tschema, []fixtureRun) {
			mod := "must-unknown-node-parent"
			c := &tschema{kind: KindContainer, mod: mod, name: "c", config: true}
			c.leaf("a", str)
			c.add(&tschema{kind: KindContainer, mod: mod, name: "d", config: true}).leaf("e", str)
			st := &tschema{kind: KindContainer, mod: mod, name: "st"} // config false
			st.leaf("v", str).config = false
			// the data nodes of the module: st is compiled first in the golden (libyang's unres order)
			runs := append(mustRuns(st, "/nope", "v/leaf = 1"), mustRuns(c, "d/nope = 1", "/nope", "/munp:c/zz and a = 'x'",
				"d/nope[bad = 1]/also/gone", "d/e[nothere = 1] and ../a", "a | d/nada | d/e")...)
			return c, runs
		}},
		{"must-operand-node-type-warning.json", func() (*tschema, []fixtureRun) {
			top := newTop("must-operand-node-type")
			top.leaf("n", u8)
			top.add(&tschema{kind: KindContainer, mod: top.mod, name: "c1", config: true}).leaf("in", u8)
			top.add(&tschema{kind: KindList, mod: top.mod, name: "l", config: true, keys: []string{"k"}}).leaf("k", str)
			top.add(&tschema{kind: KindLeafList, mod: top.mod, name: "ll", config: true, typ: u8})
			return top, mustRuns(top, "c1 = 1", "n + c1", "-c1 > 0", "l = ll", "n * 2 = 4 and 1 < c1/in and c1 != 3")
		}},
		{"must-operand-not-numeric-warning.json", func() (*tschema, []fixtureRun) {
			top := newTop("must-operand-not-numeric")
			top.leaf("s", str)
			top.leaf("n", u8)
			top.leaf("d", bt(TypeDec64))
			top.leaf("b", bt(TypeBool))
			top.leaf("un", unionOf(str, bt(TypeInt16)))
			top.leaf("us", unionOf(str, bt(TypeBool)))
			top.leaf("lr", leafrefTo(str))
			top.leaf("ln", leafrefTo(u8))
			top.add(&tschema{kind: KindLeafList, mod: top.mod, name: "ll", config: true, typ: str})
			return top, mustRuns(top, "s + 1 = 2", "n < s", "-s = 1", "un * d > 1", "us - 1", "lr < ln", "ln div 2 > d",
				"b mod 2 = ll", "(n + 1) * (d + 2) > (n - 1) * (s + 1) and ln = 1")
		}},
		{"must-operand-incompatible-warning.json", func() (*tschema, []fixtureRun) {
			top := newTop("must-operand-incompatible")
			e := bt(TypeEnum)
			top.leaf("s", str)
			top.leaf("n", u8)
			top.leaf("d", bt(TypeDec64))
			top.leaf("b", bt(TypeBool))
			top.leaf("en", e)
			top.leaf("bt", bt(TypeBits))
			top.leaf("u1", unionOf(e, bt(TypeBits)))
			top.leaf("u2", unionOf(bt(TypeBits), str))
			top.leaf("u3", unionOf(e, u8))
			top.leaf("lr", leafrefTo(e))
			top.add(&tschema{kind: KindLeafList, mod: top.mod, name: "ll", config: true, typ: str})
			top.add(&tschema{kind: KindContainer, mod: top.mod, name: "c", config: true}).leaf("x", str)
			return top, mustRuns(top, "s = n", "n = d", "s = en", "b != s", "u1 = u2", "u1 = u3", "lr = en", "lr = bt",
				"ll = s", "c = s", "s = c/x and en = b")
		}},
		{"must-value-not-fit-warning.json", func() (*tschema, []fixtureRun) {
			top := &tschema{kind: KindContainer, mod: "must-value-nofit", name: "c", config: true}
			top.checked("n", KindLeaf, u8, intCheck("uint8", 255))
			return top, mustRuns(top, "n = 'abc'")
		}},
		{"must-value-not-fit-sides-warning.json", func() (*tschema, []fixtureRun) {
			top := newTop("must-value-nofit-sides")
			top.checked("n", KindLeaf, u8, intCheck("uint8", 255))
			top.checked("ll", KindLeafList, bt(TypeInt8), intCheck("int8", 127))
			top.checked("e", KindLeaf, bt(TypeEnum), enumCheck("red", "green"))
			top.checked("u", KindLeaf, unionOf(u8, bt(TypeEnum)), func(v string) (string, bool) {
				if _, ok := intCheck("uint8", 255)(v); ok {
					return "", true
				}
				if _, ok := enumCheck("big")(v); ok {
					return "", true
				}
				return "Invalid union value \"" + v + "\" - no matching subtype found:\n    ly2 integers: Invalid type uint8 value \"" + v +
					"\".\n    ly2 enumeration: Invalid enumeration value \"" + v + "\".\n", false
			})
			top.checked("s", KindLeaf, str, func(v string) (string, bool) {
				if len(v) > 3 {
					return "Unsatisfied length - string \"" + v + "\" length is not allowed.", false
				}
				return "", true
			})
			return top, mustRuns(top, "300 = n", "ll != 'x'", "e = 'blue'", "u = 'huge' or u != -1", "s = 'toolong'",
				"n = 1 + 'x'", "n = 1 and 'abc' = n and e = 'red'")
		}},
		{"must-identityref-no-prefix-warning.json", func() (*tschema, []fixtureRun) {
			top := newTop("must-identityref-no-prefix")
			neverStored := func(string) (string, bool) { panic("an identityref is never stored") }
			top.checked("id", KindLeaf, bt(TypeIdent), neverStored)
			top.checked("ids", KindLeafList, bt(TypeIdent), neverStored)
			top.leaf("s", str)
			return top, mustRuns(top, "id = 'der'", "'der' = id", "id != 'minp:der'", "ids = 'der' and s = 'x'", "id = s")
		}},
		{"when-string-func-arg-warning.json", func() (*tschema, []fixtureRun) {
			top, ws := argSchema("when-string-func-arg", 11)
			return top, whenRuns(ws, []string{
				"contains(../n, 'x')", "contains(../s, ../c)", "concat(../s, ../n, ../c, ../ll)", "lang(../c) or lang(../n)",
				"normalize-space(../n) = '' or normalize-space(../c) = '' or normalize-space() = ''",
				"re-match(../n, ../c)", "starts-with(../c, ../n)",
				"string-length(../n) > 1 and string-length(../c) > 1 and string-length(../s) > 1",
				"../n[string-length() > 1] and ../c[string-length() > 1] and ../s[string-length() > 1]",
				"substring-before(../n, ../c) = substring-after(../c, ../n)",
				"translate(../n, ../c, ../ll) = translate(../s, ../s, ../s)"})
		}},
		{"when-substring-numeric-arg-warning.json", func() (*tschema, []fixtureRun) {
			top, ws := argSchema("when-substring-numeric-arg", 4)
			return top, whenRuns(ws, []string{"substring(../s, ../s) = ''",
				"substring(../s, ../n) = '' and substring(../s, 1, ../n) = ''",
				"substring(../s, 1, ../c) = '' or substring(../s, ../n, ../s) = ''",
				"substring(../n, 1) = '' or substring(../c, ../ll) = ''"})
		}},
		{"when-func-arg-warning.json", func() (*tschema, []fixtureRun) {
			top := newTop("when-func-arg")
			top.leaf("s", str)
			return top, []fixtureRun{{top.leaf("a", str), "sum(../s) = 1"}}
		}},
		{"when-deref-arg-warning.json", func() (*tschema, []fixtureRun) {
			top, ws := nsSchema("when-deref-arg", 6)
			return top, whenRuns(ws, []string{"deref(../c) = 'a'", "deref(../s) = 'a'", "deref(../n) = 'a'",
				"deref(../lr) = 'a'", "deref(../ii) = 'a'", "deref(../un) = 'a'"})
		}},
		{"when-derived-from-arg-warning.json", func() (*tschema, []fixtureRun) {
			top, ws := nsSchema("when-derived-from-arg", 5)
			return top, whenRuns(ws, []string{"derived-from(../s, 'wdfa:der')", "derived-from(../c, ../n)",
				"derived-from-or-self(../n, ../c)",
				"derived-from(../id, ../s) and derived-from-or-self(../ul, 'wdfa:base')", "derived-from-or-self(../s, ../s)"})
		}},
		{"when-nodeset-func-arg-warning.json", func() (*tschema, []fixtureRun) {
			top, ws := nsSchema("when-nodeset-func-arg", 7)
			return top, whenRuns(ws, []string{
				"bit-is-set(../n, ../c) or bit-is-set(../b, ../s) or bit-is-set(../ub, 'x')",
				"ceiling(../n) = 1 and ceiling(../d) = 1 and ceiling(../c) = 1", "floor(../s) = 1 or floor(../d) = 1",
				"round(../e) = 1 or round(../c) = 1", "enum-value(../n) = 1 or enum-value(../e) = 1 or enum-value(../c) = 1",
				"sum(../s | ../n | ../c | ../d) = 1",
				"sum(1) = 1 or count('x') = 1 or floor('x') = 1 or count(../c) = 1 or sum(../n) = 1"})
		}},
	}
}

// TestC2bFixturesReplay is the end-to-end check of the schema-mode warnings: every warning-bearing
// must/when fixture of compile/ replayed through Atomize in libyang's order, the whole warning
// sequence compared with the golden (node-not-found, operand, value and argument warnings and the
// trailers between them).
func TestC2bFixturesReplay(t *testing.T) {
	for _, f := range c2bFixtures() {
		t.Run(strings.TrimSuffix(f.golden, "-warning.json"), func(t *testing.T) {
			top, runs := f.build()
			var got []string
			for _, r := range runs {
				got = append(got, warnsFrom(t, top, r.ctx, r.expr)...)
			}
			want := goldenWarnings(t, f.golden)
			if len(want) == 0 || strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}
