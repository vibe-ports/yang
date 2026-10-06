// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/set.c (ly_set_add, ly_set_rm_index) (BSD-3-Clause, © CESNET).

package compile

import (
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
)

// wildName is the match key of a pending node-id that must be tried against every node: one
// with a prefix its matching module does not define, which libyang logs at every attempt.
const wildName = "\x00"

// pending is a struct ly_set of pending refines or augments (ctx->uses_rfns, ctx->uses_augs)
// with libyang's order: added at the end, removed by moving the last item into the gap
// (ly_set_rm_index). Indexes keep libyang's scans to the items that can match or log:
//   - byKey: the match key, the names of the node-id ("a/b/c") or wildName, so a compiled
//     node only looks at the items whose names end its own path;
//   - byNames: the names ("a/b/c") whatever the prefixes, and byTuple: the prefixes per
//     node-id length, for the refine merge (lys_abs_schema_nodeid_match), which resolves the
//     prefixes of every same-length item in another module.
type pending[T comparable] struct {
	items   []T
	pos     map[T]int
	byKey   map[string]map[T]bool
	key     map[T]string
	byNames map[string]map[T]bool
	names   map[T]string
	byTuple map[int]map[string]map[T]bool // node-id length -> prefixes ("p1/p2") -> items
	tuple   map[T]string
	maxLen  int // most names in a node-id added
}

func addTo[T comparable](idx map[string]map[T]bool, k string, x T) {
	if idx[k] == nil {
		idx[k] = map[T]bool{}
	}
	idx[k][x] = true
}

// add is ly_set_add: key is the match key (nidKey) of nid.
func (p *pending[T]) add(x T, key string, nid *nodeid) {
	if p.pos == nil {
		p.pos, p.key, p.names, p.tuple = map[T]int{}, map[T]string{}, map[T]string{}, map[T]string{}
		p.byKey, p.byNames, p.byTuple = map[string]map[T]bool{}, map[string]map[T]bool{}, map[int]map[string]map[T]bool{}
	}
	n := len(nid.name)
	p.maxLen = max(p.maxLen, n)
	p.pos[x], p.key[x] = len(p.items), key
	p.names[x], p.tuple[x] = strings.Join(nid.name, "/"), strings.Join(nid.prefix, "/")
	p.items = append(p.items, x)
	addTo(p.byKey, key, x)
	addTo(p.byNames, p.names[x], x)
	if p.byTuple[n] == nil {
		p.byTuple[n] = map[string]map[T]bool{}
	}
	addTo(p.byTuple[n], p.tuple[x], x)
}

// remove is ly_set_rm: the last item takes the place of x.
func (p *pending[T]) remove(x T) {
	i, last := p.pos[x], p.items[len(p.items)-1]
	p.items[i], p.pos[last] = last, i
	p.items = p.items[:len(p.items)-1]
	n := strings.Count(p.names[x], "/") + 1
	delete(p.byKey[p.key[x]], x)
	delete(p.byNames[p.names[x]], x)
	delete(p.byTuple[n][p.tuple[x]], x)
	delete(p.pos, x)
	delete(p.key, x)
	delete(p.names, x)
	delete(p.tuple, x)
}

// sorted is the items of sets in set order.
func (p *pending[T]) sorted(work *int, sets ...map[T]bool) []T {
	var out []T
	for _, s := range sets {
		for x := range s {
			*work++
			out = append(out, x)
		}
	}
	slices.SortFunc(out, func(a, b T) int { return p.pos[a] - p.pos[b] })
	return slices.Compact(out)
}

// keys are the match keys that can match a node named name under parent: its name, then the
// names of its parents prepended, up to the longest pending node-id.
func (p *pending[T]) keys(name string, parent *schema.Node, work *int) []string {
	if len(p.items) == 0 {
		return nil
	}
	keys := []string{name}
	for k := name; len(keys) < p.maxLen && parent != nil; parent = parent.Parent {
		*work += len(keys)
		k = parent.Name + "/" + k
		keys = append(keys, k)
	}
	return keys
}

// scan walks the items with one of keys (or wildName) in set order, as libyang's loop over the
// whole set reaches them; it follows removals (the moved last item keeps its turn).
type scan[T comparable] struct {
	p    *pending[T]
	keys map[string]bool
	cand []T
	work *int
}

func (p *pending[T]) scan(keys []string, work *int) *scan[T] {
	s := &scan[T]{p: p, keys: map[string]bool{wildName: true}, work: work}
	sets := []map[T]bool{p.byKey[wildName]}
	for _, k := range keys {
		s.keys[k] = true
		sets = append(sets, p.byKey[k])
	}
	s.cand = p.sorted(work, sets...)
	return s
}

// next is the candidate at the lowest set position >= i.
func (s *scan[T]) next(i int) (T, int, bool) {
	k, _ := slices.BinarySearchFunc(s.cand, i, func(x T, i int) int { return s.p.pos[x] - i })
	*s.work++
	if k == len(s.cand) {
		var zero T
		return zero, 0, false
	}
	return s.cand[k], s.p.pos[s.cand[k]], true
}

// remove removes x from the set (ly_set_rm) and the candidates.
func (s *scan[T]) remove(x T) {
	last := s.p.items[len(s.p.items)-1]
	s.cand = slices.DeleteFunc(s.cand, func(y T) bool { return y == x || y == last && last != x })
	s.p.remove(x)
	if last != x && s.keys[s.p.key[last]] {
		k, _ := slices.BinarySearchFunc(s.cand, s.p.pos[last], func(y T, i int) int { return s.p.pos[y] - i })
		s.cand = slices.Insert(s.cand, k, last)
	}
	*s.work += len(s.cand)
}

// mergeCandidates are the items lys_abs_schema_nodeid_match can match or log for nid, in set
// order: those with the same names, and those of the same length with a prefix that undefined
// reports as not defined in the scanning module (resolving it logs).
func (p *pending[T]) mergeCandidates(nid *nodeid, undefined func(prefix string) bool, work *int) []T {
	sets := []map[T]bool{p.byNames[strings.Join(nid.name, "/")]}
	for tuple, set := range p.byTuple[len(nid.name)] {
		*work++
		if slices.ContainsFunc(strings.Split(tuple, "/"), undefined) {
			sets = append(sets, set)
		}
	}
	return p.sorted(work, sets...)
}
