// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"crypto/sha256"
	"encoding/binary"
	"iter"
	"math"
	"math/bits"

	"github.com/vibe-ports/yang/internal/opidx"
	"github.com/vibe-ports/yang/internal/schema"
)

// childOrder is the mutation-maintained ordered index used for operation
// children, unordered copies, and opaque XML anchors. The libyang-compatible
// children table and first-instance records remain separate in siblings.
type childOrder struct {
	seq         opidx.Seq[*Node]
	byNode      map[*Node]*opidx.Handle[*Node]
	bySchema    *opidx.Occurrences[*Node, *schema.Node, uint8]
	byExact     *opidx.Occurrences[*Node, *schema.Node, exactKey]
	byRB        *opidx.Occurrences[*Node, *schema.Node, *rbGroup]
	byOpaqName  *opidx.Occurrences[*Node, string, uint8]
	byOpaqExact *opidx.Occurrences[*Node, opaqueKey, uint8]
	entries     map[*schema.Node]*opEntry
	ranks       [2]opidx.Ranks
}

type exactKey struct {
	hash  uint32
	value string
}

type opaqueKey struct {
	name, namespace string
}

type opEntry struct {
	rank      int
	direction int
	gaps      int // extra physical runs, retained only as local boundary state
	count     int
	rb        int
}

func orderedWorkBound(items, depth, factor uint64) uint64 {
	limit := ^uint64(0)
	if items > limit/factor {
		return limit
	}
	items *= factor
	if items != 0 && depth > limit/items {
		return limit
	}
	return items * depth
}

func (s *siblings) orderInsertWorkBound(parent, n *Node) uint64 {
	needsOrder := s.order != nil || opChild(n.schema) || s.schemaUnordered || n.schema == nil && isOp(schemaOf(parent))
	if !needsOrder {
		return 0
	}
	depth := uint64(bits.Len(uint(s.len()+2)) + 1) //nolint:gosec // sibling counts are nonnegative
	if s.order == nil {
		return orderedWorkBound(uint64(s.len()+1), depth, 32) //nolint:gosec // sibling counts are nonnegative
	}
	return orderedWorkBound(1, depth, 32)
}

func (s *siblings) orderRemoveWorkBound(n *Node) uint64 {
	if s.order == nil || s.order.byNode[n] == nil {
		return 0
	}
	return orderedWorkBound(1, uint64(bits.Len(uint(s.len()+1))+1), 32) //nolint:gosec // sibling counts are nonnegative
}

func (s *siblings) orderRebuildWorkBound() uint64 {
	if s.order == nil {
		return 0
	}
	depth := uint64(bits.Len(uint(s.len()+2)) + 1)        //nolint:gosec // sibling counts are nonnegative
	return orderedWorkBound(uint64(s.len()+1), depth, 32) //nolint:gosec // sibling counts are nonnegative
}

func (s *siblings) orderReorderWorkBound(count int) uint64 {
	if s.order == nil || count <= 0 {
		return 0
	}
	depth := uint64(bits.Len(uint(s.len()+1)) + 1) //nolint:gosec // sibling counts are nonnegative
	return orderedWorkBound(uint64(count), depth, 16)
}

func opExactValue(n *Node) (exactKey, bool) {
	if n == nil || n.schema == nil || n.schema.Kind != schema.LeafList {
		return exactKey{}, false
	}
	// The stored hash is libyang's first equality gate; the canonical value
	// resolves its collisions without copying the string into an encoded key.
	return exactKey{n.hash, n.value.Canonical()}, true
}

// subtreeEquivalenceKey is a collision-resistant narrowing key for keyless-list
// whole-subtree equality. Callers still run compareSingle on every candidate.
func subtreeEquivalenceKey(n *Node, work *workCounter) (string, bool) {
	if n == nil || n.schema == nil || n.schema.Kind != schema.List || !n.schema.Keyless() {
		return "", false
	}
	h := sha256.New()
	var size [8]byte
	type item struct {
		n       *Node
		end     bool
		shallow bool
	}
	stack := []item{{n: n}}
	for len(stack) != 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.end {
			h.Write([]byte{0})
			continue
		}
		c := current.n
		work.Add(1)
		if err := work.chargeHash(1); err != nil {
			return "", false
		}
		var identity string
		if c.schema == nil {
			// Opaque names and XML prefix spellings are not stable equality keys.
			h.Write([]byte{2})
		} else {
			h.Write([]byte{1, byte(c.schema.Kind)}) //nolint:gosec // schema kind is a small enum
			identity = c.schema.LogPath()
			if c.schema.Module != nil {
				identity = c.schema.Module.Name + identity
			}
			binary.BigEndian.PutUint64(size[:], uint64(len(identity)))
			h.Write(size[:])
			h.Write([]byte(identity))
		}
		// A system-ordered keyed child is matched by lookup, not by position.
		// lyd_compare_siblings_ may therefore reuse the same matching instance
		// for several source instances. Its per-instance hash is not a safe
		// discriminator for the enclosing keyless-list subtree.
		if !current.shallow {
			binary.BigEndian.PutUint64(size[:], uint64(c.hash))
			h.Write(size[:])
		}
		var value string
		if c.schema != nil && c.isTerm() && !current.shallow {
			value = c.value.Canonical()
		}
		bytes := addHashBound(uint64(len(identity)), uint64(len(value)))
		if err := work.chargeHash(addHashBound(bytes, bytes)); err != nil {
			return "", false
		}
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		h.Write(size[:])
		h.Write([]byte(value))
		stack = append(stack, item{end: true})
		// Native sibling comparison may look up every system-ordered instance
		// against the same first match, and any payload equality has asymmetric
		// representation cases. Keep these keys deliberately coarse so equality
		// can never be rejected by the narrowing digest.
		if current.shallow || c.schema != nil && isAny(c.schema) {
			continue
		}
		for i := len(c.kids.opq) - 1; i >= 0; i-- {
			stack = append(stack, item{n: c.kids.opq[i]})
		}
		for i := len(c.kids.list) - 1; i >= 0; i-- {
			child := c.kids.list[i]
			stack = append(stack, item{n: child, shallow: compareNeedsLookup(child)})
		}
	}
	return string(h.Sum(nil)), true
}

func opaqExactKey(n *Node) opaqueKey {
	if n == nil || n.opaq == nil {
		return opaqueKey{}
	}
	return opaqueKey{n.opaq.Name, n.opaq.ModuleNS}
}

func newChildOrder() *childOrder {
	o := &childOrder{byNode: map[*Node]*opidx.Handle[*Node]{}, entries: map[*schema.Node]*opEntry{}}
	o.bySchema = opidx.NewOccurrences[*Node, *schema.Node, uint8](&o.seq)
	o.byExact = opidx.NewOccurrences[*Node, *schema.Node, exactKey](&o.seq)
	o.byRB = opidx.NewOccurrences[*Node, *schema.Node, *rbGroup](&o.seq)
	o.byOpaqName = opidx.NewOccurrences[*Node, string, uint8](&o.seq)
	o.byOpaqExact = opidx.NewOccurrences[*Node, opaqueKey, uint8](&o.seq)
	return o
}

func (s *siblings) orderWork(amount uint64) {
	if s.work == nil || amount == 0 {
		return
	}
	if amount > math.MaxInt64 {
		s.work.Add(math.MaxInt64)
		return
	}
	s.work.Add(int64(amount))
}

func (s *siblings) setOrderHooks() {
	if s.order == nil {
		return
	}
	hook := s.orderWork
	s.order.seq.SetWorkHook(hook)
	s.order.bySchema.SetWorkHook(hook)
	s.order.byExact.SetWorkHook(hook)
	s.order.byRB.SetWorkHook(hook)
	s.order.byOpaqName.SetWorkHook(hook)
	s.order.byOpaqExact.SetWorkHook(hook)
	s.order.ranks[0].SetWorkHook(hook)
	s.order.ranks[1].SetWorkHook(hook)
}

func (s *siblings) orderRegister(n *Node, h *opidx.Handle[*Node]) {
	s.order.register(n, h)
}

func (o *childOrder) register(n *Node, h *opidx.Handle[*Node]) {
	o.byNode[n] = h
	if n.schema == nil {
		o.byOpaqName.Insert(n.Name(), 0, h)
		o.byOpaqExact.Insert(opaqExactKey(n), 0, h)
		return
	}
	o.bySchema.Insert(n.schema, 0, h)
	if key, ok := opExactValue(n); ok {
		o.byExact.Insert(n.schema, key, h)
	}
	if n.inRB && n.rbGroup != nil {
		o.byRB.Insert(n.schema, n.rbGroup, h)
	}
}

func cloneChildOrder(s *siblings) *childOrder {
	if s.order == nil {
		return nil
	}
	o := newChildOrder()
	for _, n := range s.list {
		h := o.seq.InsertBefore(nil, n)
		o.register(n, h)
	}
	for _, n := range s.opq {
		h := o.seq.InsertBefore(nil, n)
		o.register(n, h)
	}
	for sn, entry := range s.order.entries {
		cloned := *entry
		o.entries[sn] = &cloned
	}
	for sn, entry := range o.entries {
		if opChild(sn) {
			o.ranks[entry.direction].Insert(entry.rank)
		}
	}
	return o
}

func (s *siblings) orderUnregister(n *Node) *opidx.Handle[*Node] {
	if s.order == nil {
		return nil
	}
	h := s.order.byNode[n]
	if h == nil {
		return nil
	}
	if n.schema == nil {
		s.order.byOpaqName.Remove(h)
		s.order.byOpaqExact.Remove(h)
	} else {
		s.order.bySchema.Remove(h)
		if _, ok := opExactValue(n); ok {
			s.order.byExact.Remove(h)
		}
		// Removal is keyed by the handle's recorded class, so it also clears a
		// membership whose node flags have just been changed.
		s.order.byRB.Remove(h)
	}
	delete(s.order.byNode, n)
	return h
}

// orderBuild replaces only the ordered acceleration structures. It never
// derives or changes children_ht, ordinary hash records, schema-alias records,
// or first-instance record order.
func (s *siblings) orderBuild(t *Tree) {
	s.touch()
	o := newChildOrder()
	for _, n := range s.list {
		t.work.Add(1)
		h := o.seq.InsertBefore(nil, n)
		s.order = o
		s.orderRegister(n, h)
		d := o.entries[n.schema]
		if d == nil {
			d = s.orderNewEntry(t, n.schema)
			o.entries[n.schema] = d
		} else if prev := o.seq.Prev(h); prev == nil || prev.Value.schema != n.schema {
			d.gaps++
		}
		d.count++
		if n.inRB {
			d.rb++
		}
	}
	for _, n := range s.opq {
		t.work.Add(1)
		h := o.seq.InsertBefore(nil, n)
		s.order = o
		s.orderRegister(n, h)
	}
	s.order = o
	s.setOrderHooks()
}

func (s *siblings) orderNewEntry(t *Tree, sn *schema.Node) *opEntry {
	d := &opEntry{}
	if !opChild(sn) {
		return d
	}
	d.direction = 0
	if sn.InOutput() {
		d.direction = 1
	}
	d.rank = t.schemaRank(sn)
	s.order.ranks[d.direction].Insert(d.rank)
	return d
}

func (s *siblings) orderAddSchema(t *Tree, n *Node, at int) {
	if s.order == nil {
		s.orderBuild(t)
		return
	}
	o := s.order
	anchor := o.seq.Select(at)
	h := o.seq.InsertBefore(anchor, n)
	s.orderRegister(n, h)
	left, right := o.seq.Prev(h), o.seq.Next(h)
	if left != nil && right != nil && left.Value.schema != nil && left.Value.schema == right.Value.schema && left.Value.schema != n.schema {
		o.entries[left.Value.schema].gaps++
	}
	d := o.entries[n.schema]
	if d == nil {
		d = s.orderNewEntry(t, n.schema)
		o.entries[n.schema] = d
	}
	if (left == nil || left.Value.schema != n.schema) && (right == nil || right.Value.schema != n.schema) && d.count != 0 {
		d.gaps++
	}
	d.count++
	if n.inRB {
		d.rb++
	}
}

func (s *siblings) orderAddOpaque(t *Tree, parent, n *Node, at int) {
	if s.order == nil {
		if !isOp(schemaOf(parent)) {
			return
		}
		s.orderBuild(t)
		return
	}
	h := s.order.seq.InsertBefore(s.order.seq.Select(len(s.list)+at), n)
	s.orderRegister(n, h)
}

func schemaOf(n *Node) *schema.Node {
	if n == nil {
		return nil
	}
	return n.schema
}

func (s *siblings) orderReorder(run []*Node) {
	if s.order == nil || len(run) < 2 {
		return
	}
	start := s.order.seq.Len()
	for _, n := range run {
		if rank, ok := s.order.seq.Rank(s.order.byNode[n]); ok {
			start = min(start, rank)
		}
	}
	anchor := s.order.seq.Select(start + len(run))
	// Establish the desired suffix backwards. Moving one handle at a time avoids
	// allocating a run-sized side table and leaves every other occurrence tree
	// valid because their relative order never changes.
	for i := len(run) - 1; i >= 0; i-- {
		n := run[i]
		h := s.orderUnregister(n)
		if h == nil || !s.order.seq.MoveBefore(h, anchor) {
			continue
		}
		s.orderRegister(n, h)
		anchor = h
	}
}

func (s *siblings) orderRemove(n *Node) (survives, survivesRB bool) {
	if s.order == nil || n == nil || n.schema == nil {
		return false, false
	}
	o, h := s.order, s.order.byNode[n]
	if h == nil {
		return false, false
	}
	left, right := o.seq.Prev(h), o.seq.Next(h)
	d := o.entries[n.schema]
	if left != nil && right != nil && left.Value.schema != nil && left.Value.schema == right.Value.schema && left.Value.schema != n.schema {
		o.entries[left.Value.schema].gaps--
	}
	if d != nil && d.count > 1 && (left == nil || left.Value.schema != n.schema) && (right == nil || right.Value.schema != n.schema) {
		d.gaps--
	}
	s.orderUnregister(n)
	o.seq.Remove(h)
	if d == nil {
		return false, false
	}
	d.count--
	if n.inRB {
		d.rb--
	}
	if d.count == 0 {
		delete(o.entries, n.schema)
		o.ranks[d.direction].Remove(d.rank)
		return false, false
	}
	return true, d.rb > 0
}

func (s *siblings) orderRemoveOpaque(n *Node) {
	if h := s.orderUnregister(n); h != nil {
		s.order.seq.Remove(h)
	}
}

func (s *siblings) orderPosition(n *Node) int {
	if s.order == nil {
		return -1
	}
	rank, ok := s.order.seq.Rank(s.order.byNode[n])
	if !ok {
		return -1
	}
	return rank
}

func (s *siblings) documentFirst(sn *schema.Node) *Node {
	if s.order == nil {
		return nil
	}
	if h := s.order.bySchema.First(sn, 0); h != nil {
		return h.Value
	}
	return nil
}

func (s *siblings) allInstances(sn *schema.Node) iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		if s.order == nil {
			return
		}
		for h := s.order.bySchema.First(sn, 0); h != nil; h = s.order.bySchema.Next(sn, 0, h) {
			if !yield(h.Value) {
				return
			}
		}
	}
}

func (s *siblings) walkFrom(n *Node) []*Node {
	if s.order == nil || n == nil {
		return nil
	}
	h := s.order.byNode[n]
	if h == nil {
		return nil
	}
	var out []*Node
	for ; h != nil && h.Value.schema == n.schema; h = s.order.seq.Next(h) {
		out = append(out, h.Value)
	}
	return out
}

// contiguousBounds returns the physical bounds of the selected run without
// materializing its nodes. The last occurrence bounds a schema with one
// physical run even when the native first-instance record selects its middle;
// split runs retain the native walk fallback.
func (s *siblings) contiguousBounds(n *Node) (lo, hi int, ok bool) {
	if s.order == nil || n == nil || n.schema == nil {
		return 0, 0, false
	}
	d := s.order.entries[n.schema]
	if d == nil || d.gaps != 0 {
		return 0, 0, false
	}
	lo = s.orderPosition(n)
	if lo < 0 {
		return 0, 0, false
	}
	last := s.order.bySchema.Last(n.schema, 0)
	if last == nil {
		return 0, 0, false
	}
	hi = s.orderPosition(last.Value) + 1
	if hi <= lo {
		return 0, 0, false
	}
	return lo, hi, true
}

func (s *siblings) opaqueLast(name, namespace string) *Node {
	if s.order == nil {
		for i := len(s.opq) - 1; i >= 0; i-- {
			n := s.opq[i]
			if n.opaq.Name == name && n.opaq.ModuleNS == namespace {
				return n
			}
		}
		return nil
	}
	probe := &Node{opaq: &opaque{Name: name, ModuleNS: namespace}}
	if h := s.order.byOpaqExact.Last(opaqExactKey(probe), 0); h != nil {
		return h.Value
	}
	return nil
}

func (s *siblings) opaqueFirst(name string) *Node {
	if s.order == nil {
		for _, n := range s.opq {
			if n.opaq.Name == name {
				return n
			}
		}
		return nil
	}
	if h := s.order.byOpaqName.First(name, 0); h != nil {
		return h.Value
	}
	return nil
}

func (s *siblings) exactFirst(sn *schema.Node, target *Node) *Node {
	if s.order == nil {
		return nil
	}
	key, ok := opExactValue(target)
	if !ok {
		return nil
	}
	if h := s.order.byExact.First(sn, key); h != nil {
		return h.Value
	}
	return nil
}

func (s *siblings) exactAtOrAfter(sn *schema.Node, target, first *Node) *Node {
	if s.order == nil || first == nil {
		return nil
	}
	key, ok := opExactValue(target)
	if !ok {
		return nil
	}
	h := s.order.byNode[first]
	if h == nil {
		return nil
	}
	if firstKey, exact := opExactValue(first); exact && first.schema == sn && firstKey == key {
		return first
	}
	if next := s.order.byExact.Next(sn, key, h); next != nil {
		return next.Value
	}
	return nil
}

// refreshRB synchronizes one linked node's membership after inRB/rbGroup is
// changed. Occurrence registration is independent of physical reordering.
func (s *siblings) refreshRB(n *Node) {
	if s.order == nil || n == nil || n.schema == nil {
		return
	}
	s.touch()
	h := s.order.byNode[n]
	if h == nil {
		return
	}
	s.order.byRB.Remove(h)
	if n.inRB && n.rbGroup != nil {
		s.order.byRB.Insert(n.schema, n.rbGroup, h)
	}
}

func (s *siblings) refreshExact(n *Node, change func()) {
	if s.order == nil || n == nil {
		change()
		return
	}
	h := s.order.byNode[n]
	if h == nil {
		change()
		return
	}
	if _, ok := opExactValue(n); ok {
		s.order.byExact.Remove(h)
	}
	change()
	if key, ok := opExactValue(n); ok {
		s.order.byExact.Insert(n.schema, key, h)
	}
}

func (s *siblings) rbPredecessor(t *Tree, n *Node) *Node {
	if s.order == nil || n == nil || n.rbGroup == nil {
		return nil
	}
	h := s.order.byRB.LastWhere(n.schema, n.rbGroup, func(candidate *Node) bool {
		return t.compareSorted(candidate, n) <= 0
	})
	if h == nil {
		return nil
	}
	return h.Value
}
