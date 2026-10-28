// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_find_sibling_opaq_next) and
// src/parser_xml.c (lydxml_get_hints_opaq) (BSD-3-Clause, © CESNET).

package data

import (
	"crypto/sha256"
	"encoding/binary"
	"iter"
	"math"

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
	bind        *orderBinding
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
	rb        int // registered byRB members; pending inRB flags have no group yet
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
			// compareSiblingFrames has already compared the containing data
			// parents, so recursive schema equality checks only this node's
			// kind, module name, and local name. Choice/case parents are not
			// data and may legitimately differ between equivalent contexts.
			identity = c.schema.Name
			if c.schema.Module != nil {
				identity = c.schema.Module.Name + "\x00" + identity
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
		for _, child := range c.kids.opaqueSpan().Reverse() {
			stack = append(stack, item{n: child})
		}
		for _, child := range c.kids.schemaSpan().Reverse() {
			// The digest is built independently for the two operands, whose
			// equivalent schema nodes may have different ordering/config flags.
			// Any list or leaf-list can be looked up on one side even when the
			// equivalent foreign schema compares it positionally. Key declarations
			// may differ between those schemas, so keep every repeated node shallow;
			// compareSingle resolves every digest collision.
			shallow := child.schema != nil && (child.schema.Kind == schema.LeafList ||
				child.schema.Kind == schema.List)
			stack = append(stack, item{n: child, shallow: shallow})
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

// orderBinding is the stable hook target of one childOrder (#240 step 4): its structures find
// their owner's work counter and their journal through it at call time, so a whole-forest
// transfer or a rollback rebinds by one pointer write instead of reinstalling (allocating)
// method values.
type orderBinding struct {
	owner *siblings
	// state is the recovery state that captured owner whole: it journals every index write
	// until it ends, whichever counter the set is bound to meanwhile
	state *hashTransaction
}

func (b *orderBinding) Work(units uint64) {
	if b.owner != nil {
		b.owner.orderWork(units)
	}
}

func (b *orderBinding) Journal() *opidx.Journal {
	if b.state == nil {
		return nil
	}
	return &b.state.index
}

func (o *childOrder) journal() *opidx.Journal { return o.bind.Journal() }

// newChildOrder returns an empty index whose hooks are installed once, on its binding. The
// binding has no owner yet: a build fills it uncounted and unjournaled (it is unreachable until
// published) and binds it at the end.
func newChildOrder() *childOrder {
	o := &childOrder{byNode: map[*Node]*opidx.Handle[*Node]{}, entries: map[*schema.Node]*opEntry{},
		bind: &orderBinding{}}
	o.bySchema = opidx.NewOccurrences[*Node, *schema.Node, uint8](&o.seq)
	o.byExact = opidx.NewOccurrences[*Node, *schema.Node, exactKey](&o.seq)
	o.byRB = opidx.NewOccurrences[*Node, *schema.Node, *rbGroup](&o.seq)
	o.byOpaqName = opidx.NewOccurrences[*Node, string, uint8](&o.seq)
	o.byOpaqExact = opidx.NewOccurrences[*Node, opaqueKey, uint8](&o.seq)
	o.seq.SetHooks(o.bind)
	o.bySchema.SetHooks(o.bind)
	o.byExact.SetHooks(o.bind)
	o.byRB.SetHooks(o.bind)
	o.byOpaqName.SetHooks(o.bind)
	o.byOpaqExact.SetHooks(o.bind)
	o.ranks[0].SetHooks(o.bind)
	o.ranks[1].SetHooks(o.bind)
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

func (s *siblings) orderRegister(n *Node, h *opidx.Handle[*Node]) {
	s.order.register(n, h)
}

func (o *childOrder) register(n *Node, h *opidx.Handle[*Node]) {
	opidx.SetCell(o.journal(), o.byNode, n, h)
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
	opidx.DeleteCell(s.order.journal(), s.order.byNode, n)
	return h
}

// orderBuild replaces only the ordered acceleration structures. It never
// derives or changes children_ht, ordinary hash records, schema-alias records,
// or first-instance record order.
func (s *siblings) orderBuild(t *Tree) {
	if err := s.touch(); err != nil {
		return
	}
	o := newChildOrder()
	for _, n := range s.SchemaAll() {
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
		if n.inRB && n.rbGroup != nil {
			d.rb++
		}
	}
	for _, n := range s.opaqueAll() {
		t.work.Add(1)
		h := o.seq.InsertBefore(nil, n)
		s.order = o
		s.orderRegister(n, h)
	}
	s.order = o
	o.bind.owner = s
	// the state that captured s journals the new index from now on (its earlier writes need no
	// entries: the whole capture of s restores the previous index pointer)
	o.bind.state = capturer(t, s)
	if o.bind.state != nil {
		o.bind.state.bound = append(o.bind.state.bound, o.bind)
	}
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
	j := o.journal()
	anchor := o.seq.Select(at)
	h := o.seq.InsertBefore(anchor, n)
	s.orderRegister(n, h)
	left, right := o.seq.Prev(h), o.seq.Next(h)
	if left != nil && right != nil && left.Value.schema != nil && left.Value.schema == right.Value.schema && left.Value.schema != n.schema {
		opidx.AddInt(j, &o.entries[left.Value.schema].gaps, 1)
	}
	d := o.entries[n.schema]
	if d == nil {
		d = s.orderNewEntry(t, n.schema)
		opidx.SetCell(j, o.entries, n.schema, d)
	}
	if (left == nil || left.Value.schema != n.schema) && (right == nil || right.Value.schema != n.schema) && d.count != 0 {
		opidx.AddInt(j, &d.gaps, 1)
	}
	opidx.AddInt(j, &d.count, 1)
	if n.inRB && n.rbGroup != nil {
		opidx.AddInt(j, &d.rb, 1)
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
	h := s.order.seq.InsertBefore(s.order.seq.Select(s.SchemaLen()+at), n)
	s.orderRegister(n, h)
}

func schemaOf(n *Node) *schema.Node {
	if n == nil {
		return nil
	}
	return n.schema
}

func (s *siblings) orderReorder(run siblingSpan) {
	if s.order == nil || run.Len() < 2 {
		return
	}
	start := s.order.seq.Len()
	for _, n := range run.All() {
		if rank, ok := s.order.seq.Rank(s.order.byNode[n]); ok {
			start = min(start, rank)
		}
	}
	anchor := s.order.seq.Select(start + run.Len())
	// Establish the desired suffix backwards. Moving one handle at a time avoids
	// allocating a run-sized side table and leaves every other occurrence tree
	// valid because their relative order never changes.
	for i := run.Len() - 1; i >= 0; i-- {
		n := run.At(i)
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
	j := o.journal()
	left, right := o.seq.Prev(h), o.seq.Next(h)
	d := o.entries[n.schema]
	if left != nil && right != nil && left.Value.schema != nil && left.Value.schema == right.Value.schema && left.Value.schema != n.schema {
		opidx.AddInt(j, &o.entries[left.Value.schema].gaps, -1)
	}
	if d != nil && d.count > 1 && (left == nil || left.Value.schema != n.schema) && (right == nil || right.Value.schema != n.schema) {
		opidx.AddInt(j, &d.gaps, -1)
	}
	s.orderUnregister(n)
	o.seq.Remove(h)
	if d == nil {
		return false, false
	}
	opidx.AddInt(j, &d.count, -1)
	if n.inRB && n.rbGroup != nil {
		opidx.AddInt(j, &d.rb, -1)
	}
	if d.count == 0 {
		opidx.DeleteCell(j, o.entries, n.schema)
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
		if at := s.schemaSpan().indexFromEnd(s.work, n); at >= 0 {
			return at
		}
		if at := s.opaqueSpan().indexFromEnd(s.work, n); at >= 0 {
			return s.SchemaLen() + at
		}
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
		// sibsOf represents a detached node as an unindexed singleton. Native
		// lyd_first_sibling/LYD_LIST_FOR_INST still sees that instance, so keep
		// this bounded fallback without walking ordinary unindexed sibling sets
		// for an absent schema.
		if s.len() <= 1 && s.SchemaLen() == 1 {
			s.visit()
			if s.At(0).schema == sn {
				return s.At(0)
			}
		}
		// Only operation children and explicitly unordered copies need this
		// document-order lookup, and both maintain childOrder. Ordinary schema-
		// ordered siblings use leaderFirst/firstRun before reaching here; an
		// absent schema must remain a constant-time miss.
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
			for _, n := range s.SchemaAll() {
				if s.work != nil {
					s.work.Add(1)
				}
				if n.schema == sn && !yield(n) {
					return
				}
			}
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
	if n == nil {
		return nil
	}
	if s.order == nil {
		if s.len() <= 1 {
			s.visit()
			if s.SchemaLen() == 1 && s.At(0) == n {
				return s.schemaSpan().Collect()
			}
			return nil
		}
		at := s.schemaSpan().indexFromEnd(s.work, n)
		if at < 0 {
			return nil
		}
		var out []*Node
		for ; at < s.SchemaLen() && s.At(at).schema == n.schema; at++ {
			if s.work != nil {
				s.work.Add(1)
			}
			out = append(out, s.At(at))
		}
		return out
	}
	h := s.order.byNode[n]
	if h == nil {
		return nil
	}
	var out []*Node
	for ; h != nil && h.Value.schema == n.schema; h = s.order.seq.Next(h) {
		if s.work != nil {
			s.work.Add(1)
		}
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
		for i := s.OpaqueLen() - 1; i >= 0; i-- {
			n := s.opaqueAt(i)
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
		for _, n := range s.opaqueAll() {
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
	if err := s.touch(); err != nil {
		return
	}
	h := s.order.byNode[n]
	if h == nil {
		return
	}
	was := s.order.byRB.Remove(h)
	now := n.inRB && n.rbGroup != nil
	if now {
		s.order.byRB.Insert(n.schema, n.rbGroup, h)
	}
	if d := s.order.entries[n.schema]; d != nil && was != now {
		if now {
			opidx.AddInt(s.order.journal(), &d.rb, 1)
		} else {
			opidx.AddInt(s.order.journal(), &d.rb, -1)
		}
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
	if err := s.touch(); err != nil {
		if s.work != nil {
			s.work.hashErr = err // the change is skipped: its caller tests the sticky error
		}
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
