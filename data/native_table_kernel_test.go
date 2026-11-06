// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/hash_table.c and src/tree_data_hash.c (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"fmt"
	"math/bits"
	"unsafe"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// Lifecycle A is deliberately compiled only into tests. No Node, Tree or siblings
// field points here. All interacting model forests are bound to one owner before
// construction. There is no legacy-tree promotion or public operation adapter.
const (
	nativeNoRecord = ^uint32(0)
	nativeMaxSize  = uint32(1 << 25)
	nativeMaxUsed  = ^uint32(0) / 100
)

type nativeStatus uint8

const (
	nativeOK       nativeStatus = 0
	nativeExists   nativeStatus = 4
	nativeNotFound nativeStatus = 5
	nativeInternal nativeStatus = 6
)

type nativeOccurrence struct {
	node *Node
	hash uint32
	next uint32
}

type nativeBucket struct{ first, last uint32 }

// Four radix-32 directory levels address 2^20 pages of at most 64 cells.
// The first page has 8/16/32 cells for the small capacities. Each allocation and
// copy is bounded; neither table growth nor undo appends an input-sized slice.
type nativePageDir[T any] struct {
	epoch uint64
	child [32]*nativePageDir[T]
	cells []T
}

type nativeArray[T any] struct {
	root *nativePageDir[T]
	size uint32
}

type nativeTable struct {
	owner            *nativeHarness
	records          nativeArray[nativeOccurrence]
	buckets          nativeArray[nativeBucket]
	size, used, free uint32
	mode             uint8
	present          bool
}

type nativeHarness struct {
	work             *workCounter
	ctx              context.Context
	epoch            uint64
	active           bool
	maxSize, maxUsed uint32 // lowering these tests resource refusal, not native semantics
}

func newNativeHarness(w *workCounter) *nativeHarness {
	return &nativeHarness{work: w, ctx: context.Background(), maxSize: nativeMaxSize, maxUsed: nativeMaxUsed}
}

// refuse latches resource and arithmetic failures in the governing work scope,
// just like chargeHash. Later continuation and cancellation preserve the first
// failure until that scope is explicitly ended by its owner.
func (h *nativeHarness) refuse(what string) error {
	if h.work.hashErr == nil {
		h.work.hashErr = fmt.Errorf("%w: native occurrence table %s", yang.ErrBudget, what)
	}
	return h.work.hashErr
}

// pay uses the governing operation's existing cumulative hash allowance. It never
// opens a scope, resets a quota, or executes inside an existing prepaid suffix.
func (h *nativeHarness) pay(n uint64) error {
	if h.work == nil || h.work.prepaid || h.work.journal != nil || h.work.held() {
		return errInProgress // G3 supplies the paired production bridge, not G1
	}
	if h.work.hashErr != nil {
		return h.work.hashErr
	}
	if err := h.ctx.Err(); err != nil {
		return err
	}
	if err := h.work.chargeHash(n); err != nil {
		return err
	}
	if h.work.visits {
		h.work.Add(1)
	}
	return nil
}

// alloc pays a power-of-two upper bound on allocator rounding, plus every
// initialized/copied cell (including inactive cells), before allocation.
func (h *nativeHarness) alloc(bytes, cells uint64) error {
	if bytes == 0 || bytes > 1<<20 {
		return h.refuse("page address")
	}
	rounded := uint64(1) << bits.Len64(bytes-1)
	return h.pay(rounded + cells)
}

// touch captures the scalar state and the two persistent page roots before any
// table write. Directory/page touch below secures paid bounded copies before
// changing a cell. On Go failure restoring this image allocates and charges
// nothing. A semantic stop retains the successful native prefix.
func (t *nativeTable) touch(h *nativeHarness) (nativeTable, error) {
	if t.owner != h || h.active || h.epoch == ^uint64(0) {
		return nativeTable{}, errInProgress
	}
	if err := h.pay(1); err != nil {
		return nativeTable{}, err
	}
	old := *t
	h.epoch++
	h.active = true
	return old, nil
}

func (h *nativeHarness) edit(t *nativeTable, fn func() (nativeStatus, error)) (status nativeStatus, err error) {
	old, err := t.touch(h)
	if err != nil {
		return nativeOK, err
	}
	defer func() {
		if caught := recover(); caught != nil {
			*t = old
			h.active = false
			panic(caught)
		}
		if err != nil {
			*t = old
		}
		h.active = false
	}()
	status, err = fn()
	if err == nil {
		err = h.pay(1) // final cancellation/sticky-error check before exposing state
	}
	return status, err
}

func (a nativeArray[T]) read(h *nativeHarness, index uint32) (T, error) {
	var zero T
	if index >= a.size {
		return zero, h.refuse("read address")
	}
	p := a.root
	for shift := 21; shift >= 6; shift -= 5 {
		if err := h.pay(1); err != nil {
			return zero, err
		}
		if p == nil {
			return zero, h.refuse("uninitialized directory")
		}
		p = p.child[(index>>shift)&31]
	}
	if err := h.pay(1); err != nil {
		return zero, err
	}
	if p == nil || int(index&63) >= len(p.cells) {
		return zero, h.refuse("uninitialized page")
	}
	return p.cells[index&63], nil
}

func (a *nativeArray[T]) touch(h *nativeHarness, index uint32) (*T, error) {
	if !h.active || index >= a.size {
		return nil, h.refuse("write address")
	}
	link := &a.root
	for shift := 21; shift >= 1; shift -= 5 {
		if err := h.pay(1); err != nil {
			return nil, err
		}
		p := *link
		if p == nil || p.epoch != h.epoch {
			cells := uint64(32)
			if p != nil {
				cells += 32
			} // initialize the new directory, then copy the old one
			if err := h.alloc(uint64(unsafe.Sizeof(nativePageDir[T]{})), cells); err != nil {
				return nil, err
			}
			q := new(nativePageDir[T])
			if p != nil {
				*q = *p
			}
			q.epoch = h.epoch
			if shift == 1 {
				n := min(a.size, 64)
				cells := uint64(n)
				if p != nil {
					cells += uint64(n)
				}
				if err := h.alloc(uint64(n)*uint64(unsafe.Sizeof(*new(T))), cells); err != nil {
					return nil, err
				}
				q.cells = make([]T, n)
				if p != nil {
					copy(q.cells, p.cells)
				}
			}
			*link = q
			p = q
		}
		if shift == 1 {
			return &p.cells[index&63], nil
		}
		link = &p.child[(index>>shift)&31]
	}
	panic("unreachable native page depth")
}

func (h *nativeHarness) initialize(t *nativeTable, size uint32, mode uint8) error {
	if size < 8 || size > min(h.maxSize, nativeMaxSize) || size&(size-1) != 0 || mode > 2 {
		return h.refuse("capacity/mode")
	}
	// Reset only after edit's scalar touch. The arrays remain private until this
	// edit succeeds, and allocation failure restores the old roots.
	t.records = nativeArray[nativeOccurrence]{size: size}
	t.buckets = nativeArray[nativeBucket]{size: size}
	t.size, t.used, t.free, t.mode, t.present = size, 0, 0, mode, true
	for i := uint32(0); i < size; i++ {
		r, err := t.records.touch(h, i)
		if err != nil {
			return err
		}
		*r = nativeOccurrence{next: i + 1} // native's inert terminal link is size
		b, err := t.buckets.touch(h, i)
		if err != nil {
			return err
		}
		*b = nativeBucket{nativeNoRecord, nativeNoRecord}
	}
	return nil
}

func (h *nativeHarness) create(t *nativeTable, size uint32, mode uint8) (nativeStatus, error) {
	return h.edit(t, func() (nativeStatus, error) { return nativeOK, h.initialize(t, size, mode) })
}

// pair finds the earliest pointer/full-hash occurrence. This is only the native
// modification callback; read/value callback selection belongs to G2.
func (h *nativeHarness) pair(t *nativeTable, n *Node, hash uint32) (uint32, uint32, error) {
	b, err := t.buckets.read(h, hash&(t.size-1))
	if err != nil {
		return 0, 0, err
	}
	prev := nativeNoRecord
	for i := b.first; i != nativeNoRecord; {
		r, err := t.records.read(h, i)
		if err != nil {
			return 0, 0, err
		}
		if r.hash == hash && r.node == n {
			return i, prev, nil
		}
		prev, i = i, r.next
	}
	return nativeNoRecord, prev, nil
}

func (h *nativeHarness) insert(t *nativeTable, n *Node, hash uint32, check bool) (nativeStatus, error) {
	if !t.present || n == nil {
		return nativeOK, h.refuse("insertion precondition")
	}
	if check {
		i, _, err := h.pair(t, n, hash)
		if err != nil {
			return nativeOK, err
		}
		if i != nativeNoRecord {
			return nativeExists, nil
		}
	}
	if t.used >= min(h.maxUsed, nativeMaxUsed) || t.free >= t.size {
		return nativeOK, h.refuse("used/free slot")
	}
	// uint64 cannot overflow in the supported domain; reject rather than wrap
	// before each insertion, including every resize reinsertion.
	percent := (uint64(t.used) + 1) * 100 / uint64(t.size)
	mode := t.mode
	if mode == 1 && percent >= 50 {
		mode = 2
	}
	if mode == 2 && percent >= 75 && t.size > min(h.maxSize, nativeMaxSize)/2 {
		return nativeOK, h.refuse("doubling")
	}
	i := t.free
	r, err := t.records.touch(h, i)
	if err != nil {
		return nativeOK, err
	}
	free := r.next
	b, err := t.buckets.touch(h, hash&(t.size-1))
	if err != nil {
		return nativeOK, err
	}
	if b.first == nativeNoRecord {
		b.first = i
	} else {
		tail, err := t.records.touch(h, b.last)
		if err != nil {
			return nativeOK, err
		}
		tail.next = i
	}
	*r = nativeOccurrence{n, hash, nativeNoRecord}
	b.last = i
	t.free, t.used, t.mode = free, t.used+1, mode
	if mode == 2 && percent >= 75 {
		return nativeOK, h.resize(t, t.size*2, check)
	}
	return nativeOK, nil
}

func (h *nativeHarness) remove(t *nativeTable, n *Node, hash uint32) (nativeStatus, error) {
	i, prev, err := h.pair(t, n, hash)
	if err != nil {
		return nativeOK, err
	}
	if i == nativeNoRecord {
		return nativeNotFound, nil
	}
	r, err := t.records.touch(h, i)
	if err != nil {
		return nativeOK, err
	}
	b, err := t.buckets.touch(h, hash&(t.size-1))
	if err != nil {
		return nativeOK, err
	}
	if prev == nativeNoRecord {
		b.first = r.next
	} else {
		p, err := t.records.touch(h, prev)
		if err != nil {
			return nativeOK, err
		}
		p.next = r.next
	}
	if b.last == i {
		b.last = prev
	}
	*r = nativeOccurrence{next: t.free} // do not retain an inactive node pointer
	t.free, t.used = i, t.used-1
	if t.mode == 2 && uint64(t.used)*100/uint64(t.size) < 25 && t.size > 8 {
		return nativeOK, h.resize(t, t.size/2, true)
	}
	return nativeOK, nil
}

func (h *nativeHarness) resize(t *nativeTable, size uint32, check bool) error {
	old := *t
	if err := h.initialize(t, size, t.mode); err != nil {
		return err
	}
	for i := uint32(0); i < old.size; i++ {
		b, err := old.buckets.read(h, i)
		if err != nil {
			return err
		}
		for slot := b.first; slot != nativeNoRecord; {
			r, err := old.records.read(h, slot)
			if err != nil {
				return err
			}
			rc, err := h.insert(t, r.node, r.hash, check)
			if err != nil {
				return err
			}
			// Release/NDEBUG ignores EEXIST during checked reinsertion. The first
			// identical pointer/hash survives; other hashes and pointers survive.
			if rc != nativeOK && rc != nativeExists {
				return fmt.Errorf("unexpected native resize status %d", rc)
			}
			slot = r.next
		}
	}
	return nil
}

func (h *nativeHarness) primitive(t *nativeTable, n *Node, hash uint32, check, remove bool) (nativeStatus, error) {
	return h.edit(t, func() (nativeStatus, error) {
		if remove {
			return h.remove(t, n, hash)
		}
		return h.insert(t, n, hash, check)
	})
}

// nativeChild is a private physical-frontier model. It does not borrow a mutated
// legacy sibling set. Producers supply already-linked neighbours to I/U; G1
// changes registrations only, never production links, metadata or stored hashes.
type nativeChild struct {
	owner                     *nativeHarness
	node                      *Node
	parent, prev, next, first *nativeChild
}

func (h *nativeHarness) schemaHash(sn *schema.Node) (uint32, error) {
	var hash uint32
	for _, name := range []string{sn.Module.Name, sn.Name} {
		for off := 0; off < len(name); {
			end := min(off+256, len(name))
			if err := h.pay(uint64(end - off)); err != nil { //nolint:gosec // 0 < end-off <= 256
				return 0, err
			}
			hash = types.HashMulti32(hash, []byte(name[off:end]))
			off = end
		}
	}
	return types.FinishHash32(hash), nil
}

func (c *nativeChild) runStart() bool {
	return c.prev.next == nil || c.prev.node.schema != c.node.schema
}

func (c *nativeChild) list() bool {
	return c.node.schema.Kind == schema.List || c.node.schema.Kind == schema.LeafList
}

func (h *nativeHarness) add(t *nativeTable, c *nativeChild, empty bool) (nativeStatus, error) {
	if rc, err := h.insert(t, c.node, c.node.hash, false); rc != nativeOK || err != nil {
		return rc, err
	}
	if !c.list() || !c.runStart() {
		return nativeOK, nil
	}
	hash, err := h.schemaHash(c.node.schema)
	if err != nil {
		return nativeOK, err
	}
	if !empty && c.next != nil && c.next.node.schema == c.node.schema {
		if rc, err := h.remove(t, c.next.node, hash); rc != nativeOK || err != nil {
			return nativeInternal, err
		}
	}
	if rc, err := h.insert(t, c.node, hash, true); rc != nativeOK || err != nil {
		return nativeInternal, err
	}
	return nativeOK, nil
}

// frontierOwned checks every descriptor whose fields add/unlink inspect. It
// does not recursively traverse neighbours or charge a hidden scan; the build's
// already paid count pass invokes it once per child before table initialization.
func (h *nativeHarness) frontierOwned(c *nativeChild) bool {
	if c == nil || c.owner != h || c.node == nil {
		return false
	}
	for _, neighbour := range [...]*nativeChild{c.parent, c.prev, c.next} {
		if neighbour != nil && (neighbour.owner != h || neighbour.node == nil) {
			return false
		}
	}
	return true
}

// event implements only lyd_insert_hash/lyd_unlink_hash. A semantic stop is a
// status, not a Go failure: its prefix remains. U observes the linked frontier.
func (h *nativeHarness) event(t *nativeTable, c *nativeChild, unlink bool) (nativeStatus, error) {
	if !h.frontierOwned(c) {
		return nativeOK, errInProgress
	}
	return h.edit(t, func() (nativeStatus, error) {
		if c.parent == nil || c.node.schema == nil || c.parent.node.schema == nil {
			return nativeOK, nil
		}
		if unlink {
			if !t.present {
				return nativeOK, nil
			}
			return h.unlink(t, c)
		}
		if k := c.parent.node.schema.Kind; k == schema.AnyData || k == schema.AnyXML {
			return nativeOK, nil // U deliberately has a different guard
		}
		if t.present {
			return h.add(t, c, false)
		}
		var count uint32
		for it := c.parent.first; it != nil; it = it.next {
			if err := h.pay(1); err != nil {
				return nativeOK, err
			}
			// add reads both neighbours (including opaque separators) after the
			// count pass. Validate that entire frontier before initializing any
			// table; validating just the triggering child's neighbours misses it.
			if !h.frontierOwned(it) {
				return nativeOK, errInProgress
			}
			if it.node.schema != nil {
				if count == min(h.maxUsed, nativeMaxUsed) {
					return nativeOK, h.refuse("child count")
				}
				count++
			}
		}
		if count < 4 {
			return nativeOK, nil
		}
		size := max(uint32(8), uint32(1)<<bits.Len32(count-1))
		if err := h.initialize(t, size, 1); err != nil {
			return nativeOK, err
		}
		for it := c.parent.first; it != nil; it = it.next {
			if err := h.pay(1); err != nil {
				return nativeOK, err
			}
			if it.node.schema != nil {
				if rc, err := h.add(t, it, true); rc != nativeOK || err != nil {
					return rc, err // abort immediately, retain partial table on semantic stop
				}
			}
		}
		return nativeOK, nil
	})
}

func (h *nativeHarness) unlink(t *nativeTable, c *nativeChild) (nativeStatus, error) {
	if rc, err := h.remove(t, c.node, c.node.hash); rc != nativeOK || err != nil {
		return nativeInternal, err
	}
	if !c.list() || !c.runStart() {
		return nativeOK, nil
	}
	hash, err := h.schemaHash(c.node.schema)
	if err != nil {
		return nativeOK, err
	}
	if rc, err := h.remove(t, c.node, hash); rc != nativeOK || err != nil {
		return nativeInternal, err
	}
	if c.next != nil && c.next.node.schema == c.node.schema {
		if rc, err := h.insert(t, c.next.node, hash, true); rc != nativeOK || err != nil {
			return nativeInternal, err
		}
	}
	return nativeOK, nil
}
