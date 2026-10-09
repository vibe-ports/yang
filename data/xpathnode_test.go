// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"slices"
	"testing"

	"github.com/vibe-ports/yang/internal/xpath"
)

// TestLookupChild: what the children index lookup answers is what a scan of Children selects (the
// evaluator's fallback): instances by value or all of them, nothing for an absent value, dead
// nodes skipped. It answers from the hash table only and declines whatever needs a scan: no
// table, and a miss while an opaque child of the name could be the fallback.
func TestLookupChild(t *testing.T) {
	f := newUnresFixture(t)
	scan := func(p xn, sn xpath.SchemaNode, vals []string) []xpath.Node {
		var out, named []xpath.Node
		for _, c := range p.Children() {
			switch {
			case c.Schema() == sn && (vals == nil || c.Value().String() == vals[0]):
				out = append(out, c)
			case c.Schema() == nil && c.Name() == sn.Name():
				named = append(named, c)
			}
		}
		if len(named) > 0 && !slices.ContainsFunc(p.Children(), func(c xpath.Node) bool { return c.Schema() == sn }) {
			return named[:1]
		}
		return out
	}
	for _, leaves := range [][]any{
		{f.t, "1", f.t, "2", f.a, "x", f.t, "3", f.mm, "m"}, // a hash table
		{f.t, "2", f.a, "x"}, // none
	} {
		_, c, nodes := f.build(t, ValidateOptions{}, leaves...)
		p := xn{c, f.set}
		if c.kids.ht == nil == (len(leaves) > 4) {
			t.Fatalf("hash table: %v", c.kids.ht != nil)
		}
		nodes["t=2"].flags |= flagDead
		opq := newOpaque(opaque{Name: "e"})
		c.kids.opq = append(c.kids.opq, opq)
		opq.parent = c
		table := c.kids.ht != nil
		check := func(sn xpath.SchemaNode, vals []string) {
			got, ok := p.LookupChild(sn, vals)
			want := scan(p, sn, vals)
			if ok && !slices.Equal(got, want) || ok != (table && len(want) == 1 && want[0].Schema() != nil) {
				t.Errorf("%s %v: %v %v, scan %v", sn.Name(), vals, got, ok, want)
			}
		}
		for _, v := range []string{"1", "2", "3", "4"} {
			check(xs{f.t, f.set}, []string{v})
		}
		for _, sn := range []xpath.SchemaNode{xs{f.a, f.set}, xs{f.mm, f.set}, xs{f.e, f.set}} {
			check(sn, nil)
		}
	}
}
