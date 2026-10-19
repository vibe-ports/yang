package data

import (
	"strings"
	"testing"
)

// A foreign same-module root makes the top set schema-unordered; a later foreign
// sorted leaf-list still finds its run by name (lyd_find_sibling_schema) and sorts into it.
func TestApplyDiffForeignSortedRunUnordered(t *testing.T) {
	a := round12Set(t, false, "pv2-lyds")
	b := round12Set(t, false, "pv2-lyds")
	tr := round12Tree(t, a, `{"pv2-lyds:ll":[1,5],"pv2-lyds:bucket":[{"name":"x"}]}`)
	for _, in := range []string{`{"pv2-lyds:bucket":[{"name":"y"}]}`, `{"pv2-lyds:ll":[3]}`} {
		d, err := Diff(nil, round12Tree(t, b, in), DiffOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := tr.ApplyDiff(d, ApplyDiffOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	for n := range tr.Top() {
		s := n.Name()
		if n.value.Type() != nil {
			s += "=" + n.value.Canonical()
		}
		got = append(got, s)
	}
	if s := strings.Join(got, " "); s != "ll=1 ll=3 ll=5 bucket bucket" {
		t.Fatalf("order %q, want libyang's lyd_find_sibling_schema run", s)
	}
}
