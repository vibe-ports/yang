// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/vibe-ports/yang/internal/opidx"
	"github.com/vibe-ports/yang/internal/schema"
)

// opidxOccurrenceValues materializes one public occurrence class. The debug
// invariant below derives every class from the live siblings instead of using
// the index's private bookkeeping as its oracle.
func opidxOccurrenceValues[S, K comparable](o *opidx.Occurrences[*Node, S, K], sn S, key K) []*Node {
	var out []*Node
	for h := o.First(sn, key); h != nil; h = o.Next(sn, key, h) {
		out = append(out, h.Value)
	}
	return out
}

func opidxOccurrenceCount(index any) int {
	return reflect.ValueOf(index).Elem().FieldByName("by").Len()
}

func opidxCheckOrderInvariant(tr *Tree, s *siblings) error {
	if err := s.checkRecords(); err != nil {
		return err
	}
	if s.order == nil {
		return nil
	}
	// Invariant probes must not become part of the scaling measurements they
	// guard. The installed hooks read s.work dynamically.
	work := s.work
	s.work = nil
	defer func() { s.work = work }()

	live := slices.Concat(slices.Clone(s.schemaSpan().Collect()), slices.Clone(s.opaqueSpan().Collect()))
	var sequence []*Node
	for h := s.order.seq.First(); h != nil; h = s.order.seq.Next(h) {
		sequence = append(sequence, h.Value)
	}
	if !slices.Equal(sequence, live) {
		return fmt.Errorf("ordered sequence differs from live siblings")
	}
	if s.order.seq.Len() != len(live) || len(s.order.byNode) != len(live) {
		return fmt.Errorf("ordered node counts = sequence %d, handles %d, live %d",
			s.order.seq.Len(), len(s.order.byNode), len(live))
	}
	for i, n := range live {
		h := s.order.byNode[n]
		if h == nil || h.Value != n || s.order.seq.Select(i) != h {
			return fmt.Errorf("ordered handle for live sibling %d is stale", i)
		}
	}

	bySchema := map[*schema.Node][]*Node{}
	byExact := map[struct {
		sn  *schema.Node
		key exactKey
	}][]*Node{}
	byRB := map[struct {
		sn    *schema.Node
		group *rbGroup
	}][]*Node{}
	byOpaqName := map[string][]*Node{}
	byOpaqExact := map[opaqueKey][]*Node{}
	for _, n := range live {
		if n.schema == nil {
			byOpaqName[n.Name()] = append(byOpaqName[n.Name()], n)
			key := opaqExactKey(n)
			byOpaqExact[key] = append(byOpaqExact[key], n)
			continue
		}
		bySchema[n.schema] = append(bySchema[n.schema], n)
		if key, ok := opExactValue(n); ok {
			class := struct {
				sn  *schema.Node
				key exactKey
			}{n.schema, key}
			byExact[class] = append(byExact[class], n)
		}
		if n.inRB && n.rbGroup != nil {
			class := struct {
				sn    *schema.Node
				group *rbGroup
			}{n.schema, n.rbGroup}
			byRB[class] = append(byRB[class], n)
		}
	}
	for sn, want := range bySchema {
		got := opidxOccurrenceValues(s.order.bySchema, sn, uint8(0))
		if !slices.Equal(got, want) || s.order.bySchema.Len(sn, 0) != len(want) {
			return fmt.Errorf("schema occurrence index for %s is stale", sn.Name)
		}
	}
	for class, want := range byExact {
		got := opidxOccurrenceValues(s.order.byExact, class.sn, class.key)
		if !slices.Equal(got, want) || s.order.byExact.Len(class.sn, class.key) != len(want) {
			return fmt.Errorf("exact-value occurrence index for %s/%q is stale", class.sn.Name, class.key.value)
		}
	}
	for class, want := range byRB {
		got := opidxOccurrenceValues(s.order.byRB, class.sn, class.group)
		if !slices.Equal(got, want) || s.order.byRB.Len(class.sn, class.group) != len(want) {
			return fmt.Errorf("RB occurrence index for %s is stale", class.sn.Name)
		}
	}
	for name, want := range byOpaqName {
		got := opidxOccurrenceValues(s.order.byOpaqName, name, uint8(0))
		if !slices.Equal(got, want) || s.order.byOpaqName.Len(name, 0) != len(want) {
			return fmt.Errorf("opaque-name occurrence index for %q is stale", name)
		}
	}
	for key, want := range byOpaqExact {
		got := opidxOccurrenceValues(s.order.byOpaqExact, key, uint8(0))
		if !slices.Equal(got, want) || s.order.byOpaqExact.Len(key, 0) != len(want) {
			return fmt.Errorf("opaque exact occurrence index for %q is stale", key.name)
		}
	}
	exactCount, rbCount := 0, 0
	for _, nodes := range byExact {
		exactCount += len(nodes)
	}
	for _, nodes := range byRB {
		rbCount += len(nodes)
	}
	if opidxOccurrenceCount(s.order.bySchema) != s.SchemaLen() ||
		opidxOccurrenceCount(s.order.byExact) != exactCount ||
		opidxOccurrenceCount(s.order.byRB) != rbCount ||
		opidxOccurrenceCount(s.order.byOpaqName) != s.OpaqueLen() ||
		opidxOccurrenceCount(s.order.byOpaqExact) != s.OpaqueLen() {
		return fmt.Errorf("ordered occurrence index retains a non-live class")
	}

	runs := map[*schema.Node]int{}
	var previous *schema.Node
	for i, n := range s.SchemaAll() {
		if i == 0 || n.schema != previous {
			runs[n.schema]++
		}
		previous = n.schema
	}
	var rankSets [2]map[int]bool
	rankSets[0], rankSets[1] = map[int]bool{}, map[int]bool{}
	if len(s.order.entries) != len(bySchema) {
		return fmt.Errorf("ordered schema entries = %d, live schemas %d", len(s.order.entries), len(bySchema))
	}
	for sn, nodes := range bySchema {
		d := s.order.entries[sn]
		if d == nil {
			return fmt.Errorf("ordered schema entry for %s is missing", sn.Name)
		}
		rb, rbIndexed := 0, 0
		for _, n := range nodes {
			if n.inRB {
				rb++
			}
			if n.inRB && n.rbGroup != nil {
				rbIndexed++
			}
		}
		if d.count != len(nodes) || d.gaps != runs[sn]-1 || d.rb != rb {
			return fmt.Errorf("ordered schema entry for %s = count %d gaps %d rb %d, want %d/%d/%d (grouped %d)",
				sn.Name, d.count, d.gaps, d.rb, len(nodes), runs[sn]-1, rb, rbIndexed)
		}
		if opChild(sn) {
			direction := 0
			if sn.InOutput() {
				direction = 1
			}
			if d.direction != direction || tr != nil && d.rank != tr.schemaRank(sn) {
				return fmt.Errorf("ordered rank entry for %s is stale", sn.Name)
			}
			rankSets[direction][d.rank] = true
		}
	}
	for direction, ranks := range rankSets {
		if s.order.ranks[direction].Len() != len(ranks) {
			return fmt.Errorf("ordered rank set %d has %d entries, want %d", direction,
				s.order.ranks[direction].Len(), len(ranks))
		}
		for rank := range ranks {
			if !s.order.ranks[direction].Contains(rank) {
				return fmt.Errorf("ordered rank set %d is missing %d", direction, rank)
			}
		}
	}
	return nil
}

func opidxAssertTreeInvariant(t *testing.T, tr *Tree) {
	t.Helper()
	stack := []*siblings{&tr.top}
	for len(stack) != 0 {
		s := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if err := opidxCheckOrderInvariant(tr, s); err != nil {
			t.Fatal(err)
		}
		for n := range s.all() {
			stack = append(stack, &n.kids)
			if n.any != nil {
				stack = append(stack, &n.any.children)
			}
		}
	}
}

// opidxCheckKeylessCache recomputes the digest buckets that persist during a
// merge level. Native duplicate sets intentionally retain consumed entries and
// are not live indexes, so only the two narrowing tables are checked here.
func opidxCheckKeylessCache(tr *Tree, c *dupCache) error {
	var work workCounter
	check := func(index map[string]*cacheDeque, candidates []*Node) error {
		want := map[string][]*Node{}
		for _, n := range candidates {
			key, ok := subtreeEquivalenceKey(n, &work)
			if !ok {
				return fmt.Errorf("cannot recompute keyless digest")
			}
			want[key] = append(want[key], n)
		}
		if len(index) != len(want) {
			return fmt.Errorf("keyless digest buckets = %d, want %d", len(index), len(want))
		}
		for key, nodes := range want {
			if index[key] == nil || !slices.Equal(index[key].schemaSpan().Collect(), nodes) {
				return fmt.Errorf("keyless digest bucket is stale")
			}
		}
		return nil
	}
	for sib, bySchema := range c.keylessFirst {
		for sn, index := range bySchema {
			var candidates []*Node
			if sib.ht == nil {
				for n := range sib.all() {
					if n.schema == sn {
						candidates = append(candidates, n)
					}
				}
			} else {
				for n := range tr.instances(sib, sn) {
					candidates = append(candidates, n)
				}
			}
			if index.first != selectedFirstRecord(sib, sn) || index.gen != sib.gen {
				return fmt.Errorf("keyless selected-run identity is stale")
			}
			if err := check(index.buckets, candidates); err != nil {
				return err
			}
		}
	}
	for sib, bySchema := range c.keylessDup {
		for sn, index := range bySchema {
			var candidates []*Node
			var first *Node
			for _, n := range sib.SchemaAll() {
				if n.schema == sn {
					first = n
					break
				}
			}
			switch {
			case first != nil && sib.ht != nil:
				key, ok := indexedKey(first, &work)
				if !ok {
					return fmt.Errorf("cannot recompute keyless bucket")
				}
				for n := range sib.bucketNodes(key) {
					if n.schema == sn {
						candidates = append(candidates, n)
					}
				}
			case sib.order != nil && (opChild(sn) || sib.schemaUnordered):
				candidates = slices.Collect(sib.allInstances(sn))
			default:
				for n := range sib.all() {
					if n.schema == sn {
						candidates = append(candidates, n)
					}
				}
			}
			if err := check(index.buckets, candidates); err != nil {
				return err
			}
		}
	}
	return nil
}
