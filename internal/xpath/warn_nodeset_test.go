// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"strconv"
	"strings"
	"testing"
)

// nsSchema is the leaves of the nodeset fixtures: every type, a union and a leafref per wanted type.
func nsSchema(mod string, wn int) (top *tschema, ws []*tschema) {
	top = newTop(mod)
	str, dec := bt(TypeString), bt(TypeDec64)
	top.leaf("s", str)
	top.leaf("n", bt(TypeUint8))
	top.leaf("d", dec)
	top.leaf("b", bt(TypeBits))
	top.leaf("e", bt(TypeEnum))
	top.leaf("id", bt(TypeIdent))
	top.leaf("lr", leafrefTo(str))
	top.leaf("ii", bt(TypeInst))
	top.leaf("ub", unionOf(bt(TypeBits), str))
	top.leaf("ud", unionOf(bt(TypeUint8), dec))
	top.leaf("ul", unionOf(bt(TypeIdent), str))
	top.leaf("un", unionOf(bt(TypeUint8), leafrefTo(str)))
	top.leaf("ln", leafrefTo(dec))
	top.add(&tschema{kind: KindContainer, mod: mod, name: "c", config: true}).leaf("x", str)
	for i := 1; i <= wn; i++ {
		ws = append(ws, top.leaf("w"+strconv.Itoa(i), str))
	}
	return top, ws
}

// TestNodeSetWarnings: each function with a container, a wrong-type leaf and a right-type leaf
// (also through a union and a leafref); sum over several nodes; the second argument.
func TestNodeSetWarnings(t *testing.T) {
	top, ws := nsSchema("m", 1)
	ctx := ws[0]
	cont := func(fn string, n int) string {
		return "Argument #" + strconv.Itoa(n) + " of " + fn + ` is a container node "c".`
	}
	bad := func(fn, leaf, want string) string {
		return "Argument #1 of " + fn + " is node \"" + leaf + "\", not of " + want + "."
	}
	for _, c := range []struct {
		x    string
		want []string
	}{
		{"bit-is-set(../c, 'x')", []string{cont("xpath_bit_is_set", 1)}},
		{"bit-is-set(../n, 'x')", []string{bad("xpath_bit_is_set", "n", `type "bits"`)}},
		{"bit-is-set(../b, 'x')", nil},
		{"bit-is-set(../ub, 'x')", nil}, // through a union
		{"bit-is-set(../b, ../c)", []string{cont("xpath_bit_is_set", 2)}},
		{"bit-is-set(../b, ../n)", []string{"Argument #2 of xpath_bit_is_set is node \"n\", not of string-type."}},
		{"ceiling(../n)", []string{bad("xpath_ceiling", "n", `type "decimal64"`)}},
		{"ceiling(../d)", nil},
		{"ceiling(../ud)", nil},
		{"ceiling(../ln)", nil}, // through a leafref
		{"floor(../c)", []string{cont("xpath_floor", 1)}},
		{"floor(../s)", []string{bad("xpath_floor", "s", `type "decimal64"`)}},
		{"round(../e)", []string{bad("xpath_round", "e", `type "decimal64"`)}},
		{"round(../d)", nil},
		{"enum-value(../n)", []string{bad("xpath_enum_value", "n", `type "enumeration"`)}},
		{"enum-value(../e)", nil},
		{"enum-value(../c)", []string{cont("xpath_enum_value", 1)}},
		{"deref(../s)", []string{bad("xpath_deref", "s", `type "leafref" nor "instance-identifier"`)}},
		{"deref(../lr)", nil},
		{"deref(../ii)", nil},
		{"deref(../un)", nil},
		{"deref(../c)", []string{cont("xpath_deref", 1)}},
		{"derived-from(../s, 'x')", []string{bad("xpath_derived_from", "s", `type "identityref"`)}},
		{"derived-from(../id, 'x')", nil},
		{"derived-from-or-self(../ul, 'x')", nil},
		{"derived-from-or-self(../c, ../n)", []string{cont("xpath_derived_from_or_self", 1),
			"Argument #2 of xpath_derived_from_or_self is node \"n\", not of string-type."}},
		{"derived-from(../id, ../c)", []string{cont("xpath_derived_from", 2)}},
		{"sum(../n)", nil},
		{"sum(../ud)", nil},
		{"sum(../s)", []string{bad("xpath_sum", "s", "numeric type")}},
		{"sum(../c)", []string{cont("xpath_sum", 1)}},
		// every node in context, in set order (the last one only for the others)
		{"sum(../s | ../n | ../c | ../b)", []string{bad("xpath_sum", "s", "numeric type"), cont("xpath_sum", 1), bad("xpath_sum", "b", "numeric type")}},
		{"floor(../s | ../c)", []string{cont("xpath_floor", 1)}},
		// a literal or number is no node: nothing to check
		{"sum(1) + count('x') + floor('x') + ceiling(2) + round(3) + enum-value('a')", nil},
		{"count(../c) + count(../s)", nil},
	} {
		if got := warnsFrom(t, top, ctx, c.x); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%q:\ngot  %q\nwant %q", c.x, got, c.want)
		}
	}
}
