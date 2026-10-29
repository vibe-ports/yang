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
// children table and first-instance records remain separate in siblings. It
// registers the handles of the set's storage (design 14) and owns no sequence:
// while it exists every stored node is registered.
type childOrder struct {
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

// orderBinding is the stable hook target of one store and its childOrder (#240 step 4): they find
// their owner's work counter and their journal through it at call time, so a whole-forest
// transfer or a rollback rebinds by one pointer write instead of reinstalling (allocating)
// method values.
type orderBinding struct {
	owner *siblings
	// state is the recovery state that captured owner whole: it journals every index write
	// until it ends, whichever counter the set is bound to meanwhile
	state *hashTransaction
}

// Work counts index work, and inside a prepaid scope of the owner's counter draws it from the
// credit (design 13 §4.4, class B; outside a scope it is counted only, class C).
func (b *orderBinding) Work(units uint64) {
	if b.owner == nil {
		return
	}
	b.owner.orderWork(units)
	w := b.owner.work
	switch {
	case w == nil:
	case w.prepaid:
		w.draw(units)
	case unpaidReadPanics && w.hashDepth > 0 && !w.held():
		panic("data: unpaid index read")
	}
}

// Entries pays for recorded index entries: from the owner's open credit, else as a capture of
// the recording state (a refusal is sticky, and the journal, which has recorded the entry
// already, restores the operation).
func (b *orderBinding) Entries(units uint64) {
	if b.owner != nil {
		if w := b.owner.work; w != nil && w.prepaid {
			w.draw(units)
			return
		}
	}
	if unpaidReadPanics && b.state != nil {
		panic("data: an unpaid index entry") // every journaled index write is prepaid
	}
	if b.state != nil {
		if err := b.state.charge(units); err != nil {
			return // sticky: j.err and the counter's hashErr carry it to the operation's end
		}
	}
}

func (b *orderBinding) Journal() *opidx.Journal {
	if b.state == nil {
		return nil
	}
	return &b.state.index
}

// newChildOrder returns an empty index over st's sequence whose hooks are the store's binding.
func newChildOrder(st *siblingStore, b *orderBinding) *childOrder {
	o := &childOrder{entries: map[*schema.Node]*opEntry{}}
	o.bySchema = opidx.NewOccurrences[*Node, *schema.Node, uint8](&st.seq)
	o.byExact = opidx.NewOccurrences[*Node, *schema.Node, exactKey](&st.seq)
	o.byRB = opidx.NewOccurrences[*Node, *schema.Node, *rbGroup](&st.seq)
	o.byOpaqName = opidx.NewOccurrences[*Node, string, uint8](&st.seq)
	o.byOpaqExact = opidx.NewOccurrences[*Node, opaqueKey, uint8](&st.seq)
	o.rebind(b)
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
	h := s.handle(n)
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
	return h
}

// orderBuild replaces only the ordered acceleration structures: it registers the handles the
// storage has (the new index is unreachable on rollback; the capture of s restores the old
// pointer). It never derives or changes children_ht, ordinary hash records, schema-alias
// records, or first-instance record order.
func (s *siblings) orderBuild(t *Tree) error {
	if err := s.touchSticky(); err != nil {
		return err
	}
	st := s.store()
	// the occurrence classes compare the storage's labels: label it first, paid (inside link's
	// scope link's bound has it: indexAddBound)
	paid, err := s.payStore(st.seq.LabelBound(s.storeJournaled()))
	if err != nil {
		return err
	}
	st.seq.SetLabeled(true)
	paid.close()
	// unbound while it is built: unreachable until published, it reports no work or entries
	o := newChildOrder(st, &orderBinding{})
	s.order = o
	var prev *Node
	for h := st.seq.First(); h != nil; h = st.seq.Next(h) {
		t.work.Add(1)
		n := h.Value
		o.register(n, h)
		if n.schema != nil {
			d := o.entries[n.schema]
			if d == nil {
				d = s.orderNewEntry(t, n.schema)
				o.entries[n.schema] = d
			} else if prev == nil || prev.schema != n.schema {
				d.gaps++
			}
			d.count++
			if n.inRB && n.rbGroup != nil {
				d.rb++
			}
		}
		prev = n
	}
	o.rebind(&st.bind)
	return nil
}

// rebind points every structure of o at b.
func (o *childOrder) rebind(b *orderBinding) {
	o.bind = b
	o.bySchema.SetHooks(b)
	o.byExact.SetHooks(b)
	o.byRB.SetHooks(b)
	o.byOpaqName.SetHooks(b)
	o.byOpaqExact.SetHooks(b)
	o.ranks[0].SetHooks(b)
	o.ranks[1].SetHooks(b)
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

func (s *siblings) orderAddSchema(t *Tree, n *Node) error {
	if s.order == nil {
		return s.orderBuild(t)
	}
	o := s.order
	j := o.bind
	h := s.handle(n)
	o.register(n, h)
	left, right := s.st.seq.Prev(h), s.st.seq.Next(h)
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
	return nil
}

func (s *siblings) orderAddOpaque(t *Tree, parent, n *Node) error {
	if s.order == nil {
		if !isOp(schemaOf(parent)) {
			return nil
		}
		return s.orderBuild(t)
	}
	s.order.register(n, s.handle(n))
	return nil
}

func schemaOf(n *Node) *schema.Node {
	if n == nil {
		return nil
	}
	return n.schema
}

// publicationIndexBound bounds what publishing the reordered run [lo, hi) of s does (rbPos):
// one refreshRB per member, then replaceSchema's republication with its index re-registration,
// its planning walk included. It plans before the run changes (refreshRB changes only occurrence
// classes, not labels), charging that plan through pay.
func (s *siblings) publicationIndexBound(lo, hi int) uint64 {
	if hi-lo < 2 {
		return 0
	}
	journaled := s.indexJournaled(s.work)
	var bound uint64
	if o := s.order; o != nil {
		for _, n := range s.schemaSpan().Slice(lo, hi).All() {
			h := s.handle(n)
			bound = addHashBound(bound, addHashBound(o.byRB.RemoveBound(h, journaled), o.byRB.InsertBoundAny(journaled)))
			bound = addHashBound(bound, orderCells(journaled)+s.orderRegisterBound(n, journaled))
		}
	}
	planned := uint64(0)
	pay := s.work.planPay()
	counted := opidx.PayFunc(func(units uint64) bool {
		planned += units
		return pay == nil || pay.Pay(units)
	})
	bound = addHashBound(bound, s.st.seq.RepublishBound(lo, hi, 0, journaled, counted))
	return addHashBound(bound, planned) // replaceSchema plans the same walk again
}

// orderRegisterBound bounds unregistering and registering n again (its occurrence classes and
// its entries cell).
func (s *siblings) orderRegisterBound(n *Node, journaled bool) uint64 {
	o := s.order
	h := s.handle(n)
	bound := 2 * orderCells(journaled)
	if n.schema == nil {
		bound = addHashBound(bound, o.byOpaqName.RemoveBound(h, journaled)+o.byOpaqName.InsertBoundAny(journaled))
		return addHashBound(bound, o.byOpaqExact.RemoveBound(h, journaled)+o.byOpaqExact.InsertBoundAny(journaled))
	}
	bound = addHashBound(bound, o.bySchema.RemoveBound(h, journaled)+o.bySchema.InsertBoundAny(journaled))
	bound = addHashBound(bound, o.byExact.RemoveBound(h, journaled)+o.byExact.InsertBoundAny(journaled))
	return addHashBound(bound, o.byRB.RemoveBound(h, journaled)+o.byRB.InsertBoundAny(journaled))
}

func (s *siblings) orderRemove(n *Node) (survives, survivesRB bool) {
	if s.order == nil || n == nil || n.schema == nil {
		return false, false
	}
	o, h := s.order, s.handle(n)
	if h == nil {
		return false, false
	}
	j := o.bind
	left, right := s.st.seq.Prev(h), s.st.seq.Next(h)
	d := o.entries[n.schema]
	if left != nil && right != nil && left.Value.schema != nil && left.Value.schema == right.Value.schema && left.Value.schema != n.schema {
		opidx.AddInt(j, &o.entries[left.Value.schema].gaps, -1)
	}
	if d != nil && d.count > 1 && (left == nil || left.Value.schema != n.schema) && (right == nil || right.Value.schema != n.schema) {
		opidx.AddInt(j, &d.gaps, -1)
	}
	s.orderUnregister(n)
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

func (s *siblings) orderRemoveOpaque(n *Node) { s.orderUnregister(n) }

// orderPosition is the position of n in s, -1 if absent or refused: the storage's rank walk. A
// class-A read on a mutation path pays it, reserved before it runs (design 13 §4.4); inside a
// prepaid scope the scope's bound pays it.
func (s *siblings) orderPosition(n *Node) int {
	h := s.handle(n)
	if h == nil {
		return -1
	}
	opened, err := s.work.openRead(rbHeight(s.Len()) + 1)
	if err != nil {
		return -1
	}
	rank, _ := s.st.seq.Rank(h)
	s.work.closeRead(opened)
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
		for h := s.order.bySchema.First(sn, 0); h != nil; {
			if !yield(h.Value) {
				return
			}
			opened, err := s.work.openRead(s.order.bySchema.ReadBound(sn, 0))
			if err != nil {
				return // the refusal is sticky; the walk ends here
			}
			h = s.order.bySchema.Next(sn, 0, h)
			s.work.closeRead(opened)
		}
	}
}

func (s *siblings) walkFrom(n *Node) []*Node {
	var out []*Node
	for h := s.handle(n); h != nil && h.Value.schema == n.schema; h = s.st.seq.Next(h) {
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
		for _, n := range s.opaqueSpan().Reverse() {
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
	h := s.handle(first)
	if h == nil {
		return nil
	}
	if firstKey, exact := opExactValue(first); exact && first.schema == sn && firstKey == key {
		return first
	}
	opened, err := s.work.openRead(s.order.byExact.ReadBound(sn, key))
	if err != nil {
		return nil
	}
	next := s.order.byExact.Next(sn, key, h)
	s.work.closeRead(opened)
	if next != nil {
		return next.Value
	}
	return nil
}

// refreshRB synchronizes one linked node's membership after inRB/rbGroup is
// changed. Occurrence registration is independent of physical reordering.
func (s *siblings) refreshRB(n *Node) error {
	if s.order == nil || n == nil || n.schema == nil {
		return nil
	}
	if err := s.touchSticky(); err != nil {
		return err
	}
	h := s.handle(n)
	if h == nil {
		return nil
	}
	// paid inside link's scope; on its own (rbPos's new tree) it prepays its point bound
	nested, err := s.work.prepay(s.refreshRBBound(n, s.indexJournaled(s.work)), false)
	if err != nil {
		return err // sticky: prepay's charge recorded it
	}
	defer s.work.closeCredit(nested)
	was := s.order.byRB.Remove(h)
	now := n.inRB && n.rbGroup != nil
	if now {
		s.order.byRB.Insert(n.schema, n.rbGroup, h)
	}
	if d := s.order.entries[n.schema]; d != nil && was != now {
		if now {
			opidx.AddInt(s.order.bind, &d.rb, 1)
		} else {
			opidx.AddInt(s.order.bind, &d.rb, -1)
		}
	}
	return nil
}

func (s *siblings) refreshExact(n *Node, change func()) {
	if s.order == nil || n == nil {
		change()
		return
	}
	h := s.handle(n)
	if h == nil {
		change()
		return
	}
	if err := s.touchSticky(); err != nil {
		return // the change is skipped: its caller tests the sticky error
	}
	// paid inside the value change's scope; on its own (validateIncomplete) it prepays
	nested, err := s.work.prepay(s.refreshExactBound(n, s.indexJournaled(s.work)), false)
	if err != nil {
		return // sticky (prepay's refusal): the change is skipped, its caller tests it
	}
	defer s.work.closeCredit(nested)
	if _, ok := opExactValue(n); ok {
		s.order.byExact.Remove(h)
	}
	change()
	if key, ok := opExactValue(n); ok {
		s.order.byExact.Insert(n.schema, key, h)
	}
}

// Prepaid bounds of the index edits (design 13 §4.2, §7): the hook units (work and entries)
// each edit reports, from the opidx point bounds for the actual heights. A missing index is
// built unbound, so its build reports nothing.

// orderCells bounds the data-side cells and counters of one add or removal: the entries cell
// and the opEntry counters, recorded under a journal.
func orderCells(journaled bool) uint64 {
	if !journaled {
		return 0
	}
	return 3 + 4*2
}

// indexJournaled reports whether s's index edits are recorded: s is captured, or will be at the
// edit's touch, by a running state (w's, or the one its own counter has).
func (s *siblings) indexJournaled(w *workCounter) bool {
	return w != nil && w.journal != nil || s.journal() != nil || s.storeJournaled()
}

// orderAddBound bounds placing n at position at of its partition (an opaque n: of the opaque
// nodes, appended when at is out of range), as link does: the storage insertion and the index's
// registration.
func (s *siblings) orderAddBound(n *Node, at int, journaled bool) uint64 {
	bound := s.insertBound(n, at, journaled)
	o := s.order
	if o == nil {
		return bound - min(bound, prepaySkew)
	}
	bound = addHashBound(bound, orderCells(journaled))
	if n.schema == nil {
		bound = addHashBound(bound, o.byOpaqName.InsertBound(n.Name(), 0, journaled))
		return addHashBound(bound, o.byOpaqExact.InsertBound(opaqExactKey(n), 0, journaled))
	}
	bound = addHashBound(bound, o.bySchema.InsertBound(n.schema, 0, journaled))
	if key, ok := opExactValue(n); ok {
		bound = addHashBound(bound, o.byExact.InsertBound(n.schema, key, journaled))
	}
	// RB membership is reserved whether or not n has it yet: a sorted insertion promotes n
	// between its reservation (linkCost) and link
	bound = addHashBound(bound, o.byRB.InsertBoundAny(journaled))
	if o.entries[n.schema] == nil && opChild(n.schema) {
		bound = addHashBound(bound, o.ranks[0].UpdateBound(journaled)+o.ranks[1].UpdateBound(journaled))
	}
	return bound - min(bound, prepaySkew)
}

// orderAddBoundAny bounds placing the schema node n anywhere in s, before an insertion's run
// is reordered or its index built (insertPositionBound): position-free and size-based.
func (s *siblings) orderAddBoundAny(n *Node, journaled bool) uint64 {
	length := s.Len()
	bound := addHashBound(opidx.InsertBoundAny(length, journaled), slotUnits(journaled)) // the storage insertion
	if s.order == nil && !opChild(n.schema) && !s.schemaUnordered {
		return bound // no index: link builds none for an ordinary schema-ordered set
	}
	occ := opidx.UpdateBoundAny(length, journaled)          // an occurrence class or a rank set of at most length
	return addHashBound(bound, 5*occ+orderCells(journaled)) // three classes, two rank sets
}

// orderRemoveBound bounds removing n: the storage removal and the index's (orderRemove,
// orderRemoveOpaque).
func (s *siblings) orderRemoveBound(n *Node, journaled bool) uint64 {
	h := s.handle(n)
	if h == nil {
		return 0
	}
	o := s.order
	if o == nil {
		return s.removeBound(journaled)
	}
	bound := addHashBound(s.removeBound(journaled), orderCells(journaled))
	if n.schema == nil {
		bound = addHashBound(bound, o.byOpaqName.RemoveBound(h, journaled))
		return addHashBound(bound, o.byOpaqExact.RemoveBound(h, journaled))
	}
	bound = addHashBound(bound, o.bySchema.RemoveBound(h, journaled))
	bound = addHashBound(bound, o.byExact.RemoveBound(h, journaled))
	bound = addHashBound(bound, o.byRB.RemoveBound(h, journaled))
	if d := o.entries[n.schema]; d != nil && d.count == 1 { // the last instance leaves its rank set
		bound = addHashBound(bound, o.ranks[d.direction].UpdateBound(journaled))
	}
	return bound
}

// refreshRBBound bounds refreshRB: the RB-class registration leaves and returns, and the count.
func (s *siblings) refreshRBBound(n *Node, journaled bool) uint64 {
	o := s.order
	h := s.handle(n)
	if o == nil || h == nil {
		return 0
	}
	return addHashBound(addHashBound(o.byRB.RemoveBound(h, journaled), o.byRB.InsertBoundAny(journaled)), orderCells(journaled))
}

// refreshExactBound bounds refreshExact: the exact-value registration leaves and returns.
func (s *siblings) refreshExactBound(n *Node, journaled bool) uint64 {
	o := s.order
	h := s.handle(n)
	if o == nil || h == nil {
		return 0
	}
	return addHashBound(o.byExact.RemoveBound(h, journaled), o.byExact.InsertBoundAny(journaled))
}
