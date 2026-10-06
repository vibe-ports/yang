// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"strings"
	"testing"
)

// TestNotFoundWarnings: the forms of eval_name_test_scnode_no_match_msg (ported with C2a) and the
// skip of the rest of the path and its predicates.
func TestNotFoundWarnings(t *testing.T) {
	top := newTop("m")
	top.leaf("a", bt(TypeString))
	top.add(&tschema{kind: KindContainer, mod: "m", name: "d", config: true}).leaf("e", bt(TypeString))
	st := &tschema{kind: KindContainer, mod: "m", name: "st"} // config false: the root is "<root>"
	st.leaf("v", bt(TypeString))
	ctx := `with context node "/m:top".`
	for _, c := range []struct {
		from *tschema
		x    string
		want []string
	}{
		{top, "zz", []string{`Schema node "zz" not found; in expr "zz" ` + ctx}},                                          // no parent in the set
		{top, "d/zz", []string{`Schema node "zz" for parent "/m:top/d" not found; in expr "d/zz" ` + ctx}},                // node parent
		{top, "/zz", []string{`Schema node "zz" for parent "<config-root>" not found; in expr "/zz" ` + ctx}},             // config root
		{st, "/zz", []string{`Schema node "zz" for parent "<root>" not found; in expr "/zz" with context node "/m:st".`}}, // plain root
		// the excerpt ends at the failing name, whatever follows
		{top, "a = 1 or d/zz = 2 and ../a", []string{`Schema node "zz" for parent "/m:top/d" not found; in expr "a = 1 or d/zz" ` + ctx,
			`Schema node "a" for parent "<config-root>" not found; in expr "a = 1 or d/zz = 2 and ../a" ` + ctx}},
		// the rest of the path and the predicates of the failing step are skipped: one warning
		{top, "d/zz[nope = 1]/more[evenmore]/gone", []string{`Schema node "zz" for parent "/m:top/d" not found; in expr "d/zz" ` + ctx}},
		{top, "zz[1]", []string{`Schema node "zz" not found; in expr "zz" ` + ctx}},
		{top, "d[nope]/e", []string{`Schema node "nope" for parent "/m:top/d" not found; in expr "d[nope" ` + ctx}}, // a predicate of a found step
	} {
		if got := warnsFrom(t, top, c.from, c.x); strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%q:\ngot  %q\nwant %q", c.x, got, c.want)
		}
	}
}
