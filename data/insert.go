// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_insert_node and its helpers, lyd_insert_before,
// lyd_unlink), src/tree_data_sorted.c (lyds_insert ordering, lyds_additionally_create_rb_tree),
// src/tree_data_free.c (lyd_free_tree, lyd_free_siblings) and src/tree_data_common.c
// (lyd_np_cont_dflt_*) (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"
	"math/bits"
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// insertOrder is lyd_insert_node's order argument (LYD_INSERT_NODE_*).
type insertOrder uint8

const (
	// insertDefault keeps schema order and sorts system-ordered lists and leaf-lists by value.
	insertDefault insertOrder = iota
	// insertLast appends (LYD_PARSE_ORDERED input); a node that would break schema order is
	// placed by schema instead, and schema nodes stay before opaque ones (see insertPos).
	insertLast
	// insertLastBySchema keeps schema order, the node after the instances of its schema node.
	insertLastBySchema
)

// opError is an error libyang logs with LOGERR (no validation code): Err is the LY_ERR name.
// The diagnostics of design 07 D1b turn it into a yang.Diagnostic.
type opError struct {
	Err string
	Msg string
}

func (e *opError) Error() string { return e.Msg }

// schemaRank is the position of s among its schema siblings in lys_getnext order: the data
// children of its data parent (choices and cases entered, the input or output of an operation),
// or the top-level nodes of its module.
func (t *Tree) schemaRank(s *schema.Node) int {
	t.rankMu.Lock()
	defer t.rankMu.Unlock()
	if r, ok := t.rank[s]; ok {
		return r
	}
	if t.rank == nil {
		t.rank = map[*schema.Node]int{}
	}
	dp := s.DataParent()
	var top []*schema.Node
	if dp == nil {
		top = s.Module.Top
	}
	var opts schema.GetNextOpt
	if s.InOutput() {
		opts = schema.GetNextOutput
	}
	i := 0
	for c := range schema.GetNext(dp, top, opts) {
		t.rank[c] = i
		i++
	}
	if _, ok := t.rank[s]; !ok {
		t.rank[s] = i // not reachable through lys_getnext (extension data): after all others
	}
	return t.rank[s]
}

// after reports whether sibling a goes after n in schema order (the anchor test of
// lyd_insert_get_next_anchor and lyd_insert_node_find_anchor): opaque nodes are always last;
// top-level schema nodes of different modules, at the top level or under an opaque parent, are
// ordered by strcmp of the module names.
func (t *Tree) after(a, n *Node) bool {
	switch {
	case a.schema == nil:
		return n.schema != nil
	case n.schema == nil:
		return false
	case a.schema.Module != n.schema.Module && a.schema.DataParent() == nil && n.schema.DataParent() == nil:
		// top-level schema nodes of different modules, at the top level or under an opaque
		// parent (no schema parent either way): by module name
		return strings.Compare(a.schema.Module.Name, n.schema.Module.Name) >= 0
	}
	ar, nr := t.schemaRank(a.schema), t.schemaRank(n.schema)
	return ar > nr || (ar == nr && a.schema != n.schema)
}

// crossContextInsertPos is lyd_insert_get_next_anchor's non-hash placement when the first sibling
// and new node have different contexts. An owner-module pointer change is normally the anchor;
// default insertion of a system-ordered node first reaches its schema run so compatible values
// from both contexts can be sorted together.
func (t *Tree) crossContextInsertPos(l []*Node, n *Node, order insertOrder) (int, bool) {
	ctxOf := func(x *Node) *schema.Set {
		if s := setOf(x); s != nil {
			return s
		}
		return t.set
	}
	if ctxOf(l[0]) == ctxOf(n) {
		// one context: the ordinary placement, value-sorted runs included (two modules of one
		// context differ in owner module, not in LYD_CTX)
		return 0, false
	}
	nOwner := ownerModule(t.set, n)
	if ownerModule(t.set, l[0]) == nOwner {
		return 0, false
	}

	i := 0
	if n.parent == nil {
		// At the top level libyang first skips data of lexically preceding modules.
		i = t.upper(l, 0, func(a *Node) bool {
			aOwner := ownerModule(t.set, a)
			return a.schema == nil || aOwner == nil || strings.Compare(aOwner.Name, nOwner.Name) >= 0
		})
		if i == len(l) {
			return i, true
		}
	}
	a := l[i]
	if ownerModule(t.set, a) != nOwner {
		if order == insertDefault && sortedSupported(n) {
			// lyd_insert_node sorts only after lyd_find_sibling_schema finds an existing instance
			// (tree_data.c:807-808); across contexts that is the manual scan by node and module
			// name (tree_data_common.c:1124-1137). Otherwise the non-hash anchor stops at the
			// foreign owner-module boundary (tree_data.c:556-562), regardless of schema rank.
			if !t.hasSchemaNamed(l, i, n.schema) {
				return i, true
			}
			nRank := t.schemaRank(n.schema)
			return t.upper(l, i, func(a *Node) bool {
				aRank := t.schemaRank(a.schema)
				if aRank != nRank {
					return aRank > nRank
				}
				if !compareSchemaEqual(a.schema, n.schema, false) {
					return true
				}
				if c, compatible := t.compareSortedCompatible(a, n); compatible {
					// lyds_insert shares a sorted run across equivalent schemas when both real
					// types use the same sort callback. A plugin/base-type change instead uses
					// the context boundary.
					return c > 0
				}
				return ownerModule(t.set, a) != nOwner
			}), true
		}
		return i, true
	}
	afterN := func(a *Node) bool { return t.after(a, n) }
	return t.upper(l, i, afterN), true
}

// hasSchemaNamed reports whether l[i:] has an instance whose schema node has sn's name and module
// name. Probes: l[i] (an earlier foreign insertion of the run), then the rank position of sn,
// where an equivalent schema sits when both snapshots declare it in the same place.
// ponytail: a schema absent there (absent or moved between snapshots) falls back to libyang's
// linear scan, so many such insertions in a row are quadratic like libyang; index by name if it
// matters.
func (t *Tree) hasSchemaNamed(l []*Node, i int, sn *schema.Node) bool {
	same := func(a *Node) bool {
		t.work.Add(1)
		return a.schema != nil && a.schema.Name == sn.Name && a.schema.Module.Name == sn.Module.Name
	}
	if i < len(l) && same(l[i]) {
		return true
	}
	rank := t.schemaRank(sn)
	if j := t.upper(l, i, func(a *Node) bool { return a.schema == nil || t.schemaRank(a.schema) >= rank }); j < len(l) && same(l[j]) {
		return true
	}
	return slices.ContainsFunc(l[i:], same)
}

// sameOrAfter reports whether a is an instance of n's schema node or goes after it.
func (t *Tree) sameOrAfter(a, n *Node) bool {
	return a.schema != nil && a.schema == n.schema || t.after(a, n)
}

// sortedSupported is lyds_is_supported: system-ordered leaf-lists and keyed lists keep their
// instances sorted by value.
func sortedSupported(n *Node) bool {
	s := n.schema
	return s != nil && !s.UserOrdered && (s.Kind == schema.LeafList || s.Kind == schema.List && !s.Keyless())
}

// compareSorted orders two instances like rb_compare_leaflists / rb_compare_lists: by value, a
// list key by key in key order.
func compareSorted(a, b *Node) int {
	if a.schema.Kind == schema.LeafList {
		return types.Compare(a.value, b.value)
	}
	for i := 0; i < len(a.kids.list) && i < len(b.kids.list); i++ {
		ka, kb := a.kids.list[i], b.kids.list[i]
		if !ka.isKey() || !kb.isKey() {
			break
		}
		if c := types.Compare(ka.value, kb.value); c != 0 {
			return c
		}
	}
	return 0
}

// compareSortedCompatible is compareSorted for equivalent schema nodes from separate contexts.
// It calls a registered plugin's shared sort callback even though the schema type pointers differ.
func (t *Tree) compareSortedCompatible(a, b *Node) (int, bool) {
	if a.schema == nil || b.schema == nil || a.schema.Kind != b.schema.Kind {
		return 0, false
	}
	if a.schema.Kind == schema.LeafList {
		return types.CompareCompatible(a.value, b.value)
	}
	if a.schema.Kind != schema.List {
		return 0, false
	}
	for i := 0; i < len(a.kids.list) && i < len(b.kids.list); i++ {
		t.work.Add(1)
		ka, kb := a.kids.list[i], b.kids.list[i]
		if !ka.isKey() || !kb.isKey() {
			break
		}
		c, compatible := types.CompareCompatible(ka.value, kb.value)
		if !compatible || c != 0 {
			return c, compatible
		}
	}
	return 0, true
}

// upper returns the first index of l[lo:] for which after holds (l is monotone in it), counting
// the comparisons.
func (t *Tree) upper(l []*Node, lo int, after func(*Node) bool) int {
	i, _ := slices.BinarySearchFunc(l[lo:], struct{}{}, func(a *Node, _ struct{}) int {
		t.work.Add(1)
		if after(a) {
			return 1
		}
		return -1
	})
	return lo + i
}

// insertPos is where lyd_insert_node links the schema node n among sib.list (opaque nodes are
// kept apart, always last).
// Schema nodes are monotone in (module name at the top level, schema rank); every search relies
// on it, so insertLast never breaks it: libyang appends after the last sibling whatever it is,
// which only LYD_PARSE_ORDERED input that is not in schema order, or that has unknown nodes,
// reaches (D-0058).
func (t *Tree) insertPos(sib *siblings, n *Node, order insertOrder) int {
	l := sib.list
	if len(l) == 0 {
		return 0
	}
	if at, ok := t.crossContextInsertPos(l, n, order); ok {
		if order != insertDefault && sortedSupported(n) && sib.rbTree[n.schema] {
			markUnsorted(sib, n.schema)
		}
		return at
	}
	afterN := func(a *Node) bool { return t.after(a, n) }
	if order == insertDefault && sortedSupported(n) {
		lo, hi := t.runBounds(sib, n)
		if lo == hi {
			return hi // no instance yet: by schema, no RB tree for a single instance
		}
		n.inRB = true
		if !sib.rbTree[n.schema] {
			// lyds_additionally_create_rb_tree: the run gets its RB tree from all its instances,
			// inserted in order, so it is sorted stably (rb_insert_node puts equal values after
			// the existing ones)
			slices.SortStableFunc(l[lo:hi], compareSorted)
			if sib.opIdx != nil {
				sib.opRebuild(t, n.schema, l[lo:hi]) // only this run was reordered
			}
			for _, a := range l[lo:hi] {
				a.inRB = true
			}
			markRB(sib, n.schema)
		} else if sib.unsorted[n.schema] {
			// instances appended after the tree was made are not in it (only diff.c's
			// LAST_BY_SCHEMA does that): lyds_link_data_node puts n right after its RB
			// predecessor, the greatest tree member not above it, or before the leader
			at := lo
			for i := lo; i < hi; i++ {
				t.work.Add(1)
				if l[i].inRB && compareSorted(l[i], n) <= 0 {
					at = i + 1
				}
			}
			return at
		}
		// after the equal values (rb_insert_node goes right on 0); append fast path
		t.work.Add(1)
		if compareSorted(l[hi-1], n) <= 0 {
			return hi
		}
		return t.upper(l[:hi], lo, func(a *Node) bool { return compareSorted(a, n) > 0 })
	}
	// lyd_insert_node_ordby_schema; append fast path
	at := len(l)
	t.work.Add(1)
	switch {
	case opChild(n.schema) && order == insertLast:
		// LYD_INSERT_NODE_LAST appends: an operation's children keep the order they are copied in
		// (lyd_dup), however the anchor split them
	case opChild(n.schema):
		at = t.opAnchor(sib, n)
	case t.after(l[at-1], n):
		at = t.upper(l, 0, afterN)
	}
	if order != insertDefault && sortedSupported(n) && sib.rbTree[n.schema] {
		markUnsorted(sib, n.schema) // n is not in the run's RB tree
	}
	return at
}

// opRebuild replaces one index entry after its contiguous sibling run was sorted.
// Reordering the run keeps its boundaries, so gaps is unchanged.
func (s *siblings) opRebuild(t *Tree, sn *schema.Node, run []*Node) {
	d := s.opIdx[sn]
	d.nodeDeque = nodeDeque{list: make([]*Node, len(run))}
	for i, n := range run {
		t.work.Add(1)
		d.list[i] = n
	}
	s.opIdx[sn] = d
}

// runBounds is the run of n's schema node in sib.list, or the empty run where its first instance
// goes. It serves system-ordered runs only (insertPos): under an operation those are input data,
// placed by the anchor or after their RB predecessor and so contiguous by construction; output
// and notification lists are user-ordered (LYS_ORDBY_USER) and never get here, so an output run
// split by the kept input ([before, v1, v2, in, v3]) keeps its anchor order
// (TestParseOpDupKeepsOrder, ops/parse-json-reply-keep-order-split).
func (t *Tree) runBounds(sib *siblings, n *Node) (lo, hi int) {
	l := sib.list
	if opChild(n.schema) {
		// a system-ordered run under an operation is input data: its instances are contiguous
		inst := sib.opInst(n.schema)
		if len(inst) == 0 {
			at := t.opAnchor(sib, n)
			return at, at
		}
		// Position locates the insertion run; the nearer endpoint scan is bounded by
		// the insertion shift plus the run walked/sorted here.
		lo = t.runFrom(l, inst[0], inst[len(inst)-1], len(inst))
		return lo, lo + len(inst)
	}
	lo = t.upper(l, 0, func(a *Node) bool { return t.sameOrAfter(a, n) })
	return lo, t.upper(l, lo, func(a *Node) bool { return t.after(a, n) })
}

// runFrom returns the start of a contiguous run, finding whichever of its endpoints is nearer
// an end of l. Each pair of examined slots is counted once, like indexFromEnd.
func (t *Tree) runFrom(l []*Node, first, last *Node, length int) int {
	for i, j := 0, len(l)-1; i <= j; i, j = i+1, j-1 {
		t.work.Add(1)
		switch {
		case l[i] == first:
			return i
		case l[i] == last:
			return i - length + 1
		case l[j] == last:
			return j - length + 1
		case l[j] == first:
			return j
		}
	}
	return -1
}

// opChild reports whether s is a child of an rpc or action's input or output. Such siblings are
// ordered by lyd_insert_get_next_anchor, which walks only the new node's own input or output
// schema list: input and output instances interleave, and the instances of one output node need
// not be contiguous (they are ordered by user, each placed by the anchor). They are found through
// the operation's instance index (siblings.opInst), as libyang finds them by hash.
func opChild(s *schema.Node) bool {
	if s == nil {
		return false
	}
	dp := s.DataParent()
	return dp != nil && (dp.Kind == schema.RPC || dp.Kind == schema.Action)
}

// indexFromEnd is the index of n in l, scanned from both ends at once, each step counted: the
// cost is the distance to the nearer end, which the slice insertion at that index moves anyway
// (insertAt shifts the shorter side), so it adds no order of growth.
func (t *Tree) indexFromEnd(l []*Node, n *Node) int {
	return indexFromEnd(&t.work, l, n)
}

func indexFromEnd(work *workCounter, l []*Node, n *Node) int {
	for i, j := 0, len(l)-1; i <= j; i, j = i+1, j-1 {
		work.Add(1)
		if l[j] == n {
			return j
		}
		if l[i] == n {
			return i
		}
	}
	return -1
}

// opFirstFrom returns the list position of the first indexed instance. System-ordered instances
// are one contiguous run, so either endpoint locates it; user-ordered output may be split.
// Callers need a mutation position (cost bounded by the shift/run), except opRun
// at the bounded anchor-created split boundaries described there.
func (t *Tree) opFirstFrom(l, inst []*Node) int {
	if len(inst) == 1 || sortedSupported(inst[0]) {
		return t.runFrom(l, inst[0], inst[len(inst)-1], len(inst))
	}
	return t.indexFromEnd(l, inst[0])
}

// orderKey is an operation's input (out false) or output children.
type orderKey struct {
	op  *schema.Node
	out bool
}

// rankBits is a 64-ary summary tree. Level zero stores schema ranks; every higher level marks
// the nonempty words below it, so next skips absent schema siblings in logarithmic word visits.
type rankBits struct {
	levels [][]uint64
	bits   int
}

func (b *rankBits) reset(work *workCounter, n int) {
	b.levels, b.bits = nil, n
	for n > 0 {
		words := (n + 63) / 64
		work.Add(int64(words))
		b.levels = append(b.levels, make([]uint64, words))
		if words == 1 {
			return
		}
		n = words
	}
}

func (b *rankBits) set(work *workCounter, rank int) {
	if rank < 0 || rank >= b.bits {
		return
	}
	for _, words := range b.levels {
		work.Add(1)
		word, bit := rank/64, uint(rank%64)
		old := words[word]
		words[word] |= uint64(1) << bit
		if old != 0 {
			return
		}
		rank = word
	}
}

func (b *rankBits) clear(work *workCounter, rank int) {
	if rank < 0 || rank >= b.bits {
		return
	}
	for _, words := range b.levels {
		work.Add(1)
		word, bit := rank/64, uint(rank%64)
		words[word] &^= uint64(1) << bit
		if words[word] != 0 {
			return
		}
		rank = word
	}
}

func (b *rankBits) next(t *Tree, rank int) int {
	if len(b.levels) == 0 || rank >= b.bits-1 {
		return -1
	}
	return b.nextAt(t, 0, rank)
}

func (b *rankBits) nextAt(t *Tree, level, rank int) int {
	words := b.levels[level]
	start := rank + 1
	word := start / 64
	if word >= len(words) {
		return -1
	}
	t.work.Add(1)
	w := words[word] & (^uint64(0) << uint(start%64))
	if w != 0 {
		return word*64 + bits.TrailingZeros64(w)
	}
	if level+1 == len(b.levels) {
		return -1
	}
	word = b.nextAt(t, level+1, word)
	if word < 0 {
		return -1
	}
	t.work.Add(1)
	return word*64 + bits.TrailingZeros64(words[word])
}

// opOrder is lys_getnext over an operation's input or output, cached per tree.
func (t *Tree) opOrder(op *schema.Node, out bool) []*schema.Node {
	t.rankMu.Lock()
	defer t.rankMu.Unlock()
	k := orderKey{op, out}
	if o, ok := t.orders[k]; ok {
		return o
	}
	var opts schema.GetNextOpt
	if out {
		opts = schema.GetNextOutput
	}
	o := slices.Collect(schema.GetNext(op, nil, opts))
	if t.orders == nil {
		t.orders = map[orderKey][]*schema.Node{}
	}
	t.orders[k] = o
	return o
}

type opEntry struct {
	nodeDeque
	rank      int
	direction int
	gaps      int // extra runs in sibling order; zero means every instance is adjacent
}

// opInst is the instances of the operation child sn in s.list, in list order. It only reads: the
// index is kept by the mutations alone (link, unlink, the RB sort), so concurrent readers of an
// unchanged tree share it safely.
func (s *siblings) opInst(sn *schema.Node) []*Node {
	if s.opIdx == nil {
		// a sibling set never linked through link (sibsOf's singleton of a detached node):
		// no index, the few nodes are filtered
		var inst []*Node
		for _, a := range s.list {
			s.visit()
			if a.schema == sn {
				inst = append(inst, a)
			}
		}
		return inst
	}
	if d := s.opIdx[sn]; d != nil {
		return d.list
	}
	return nil
}

// opRun is LYD_LIST_FOR_INST from the first instance: stop at the first other schema.
// Most entries are contiguous, so their index slice is already the run and needs no position.
// ponytail: anchor-created split entries arise only around the hash-table transition with
// fewer than four kept input nodes; those boundaries bound the position lookups on replies.
func (t *Tree) opRun(s *siblings, sn *schema.Node) []*Node {
	if d := s.opIdx[sn]; d != nil && d.gaps == 0 {
		return d.list
	}
	inst := s.opInst(sn)
	if len(inst) == 0 {
		return nil
	}
	i := t.opFirstFrom(s.list, inst)
	j := i
	for j < len(s.list) && s.list[j].schema == sn {
		t.work.Add(1)
		j++
	}
	return s.list[i:j]
}

// opNeighbours reads only the slots adjacent to the changed slot, before unlink or after link.
func (s *siblings) opNeighbours(at int) (left, right *schema.Node) {
	if at > 0 {
		left = s.list[at-1].schema
	}
	if at+1 < len(s.list) {
		right = s.list[at+1].schema
	}
	return left, right
}

// opBuild (re)builds the instance index of an operation's children from s.list; mutations only.
func (s *siblings) opBuild(t *Tree) {
	s.opIdx = map[*schema.Node]*opEntry{}
	s.opRanks = [2]rankBits{}
	if len(s.list) > 0 {
		s.opRanks[0].reset(&t.work, len(t.opOrder(s.list[0].schema.DataParent(), false)))
		s.opRanks[1].reset(&t.work, len(t.opOrder(s.list[0].schema.DataParent(), true)))
	}
	for i, a := range s.list {
		t.work.Add(1)
		d := s.opIdx[a.schema]
		if d == nil {
			d = s.opNewEntry(t, a.schema)
			s.opIdx[a.schema] = d
		} else if s.list[i-1].schema != a.schema {
			d.gaps++
		}
		d.list = append(d.list, a)
	}
}

// opRemove drops n, being unlinked, from the instance index.
func (s *siblings) opRemove(n *Node, at int) {
	if s.opIdx == nil {
		return
	}
	d := s.opIdx[n.schema]
	if d == nil {
		return
	}
	left, right := s.opNeighbours(at)
	if left != nil && left == right && left != n.schema {
		s.opIdx[left].gaps-- // removing n joins the neighbouring runs
	}
	if len(d.list) > 1 && left != n.schema && right != n.schema {
		d.gaps-- // a singleton run disappears
	}
	// unlink already scans the sibling slice once on main, so this index scan and the shorter-side
	// compaction add no order of growth.
	i := indexFromEnd(s.work, d.list, n)
	if i < 0 {
		return
	}
	moved := d.deleteAt(i)
	if s.work.shifts {
		s.work.Add(int64(moved))
	}
	if len(d.list) == 0 {
		delete(s.opIdx, n.schema)
		s.opRanks[d.direction].clear(s.work, d.rank)
	}
}

// opNewEntry records the first instance of sn in the occupied-rank summary.
func (s *siblings) opNewEntry(t *Tree, sn *schema.Node) *opEntry {
	if len(s.opRanks[0].levels) == 0 && len(s.opRanks[1].levels) == 0 {
		op := sn.DataParent()
		s.opRanks[0].reset(&t.work, len(t.opOrder(op, false)))
		s.opRanks[1].reset(&t.work, len(t.opOrder(op, true)))
	}
	d := 0
	if sn.InOutput() {
		d = 1
	}
	rank := t.schemaRank(sn)
	s.opRanks[d].set(&t.work, rank)
	return &opEntry{rank: rank, direction: d}
}

// opIdxAdd records n, just placed at index at of s.list, in the operation's instance index.
// An adjacent instance determines the index position locally; otherwise n appends after the last
// instance or the split output run is rebuilt in sibling order.
func (s *siblings) opIdxAdd(t *Tree, n *Node, at int) {
	left, right := s.opNeighbours(at)
	if left != nil && left == right && left != n.schema {
		s.opIdx[left].gaps++ // n splits the neighbouring run
	}
	d := s.opIdx[n.schema]
	if d == nil {
		d = s.opNewEntry(t, n.schema)
		d.list = []*Node{n}
		s.opIdx[n.schema] = d
		return
	}
	if left != n.schema && right != n.schema {
		d.gaps++ // a new separate run of n
	}
	add := func(i int) {
		moved := d.insertAt(i, n)
		if t.work.shifts {
			t.work.Add(int64(moved))
		}
	}
	// Neighbour positions cost no more than the index insertion shift plus one.
	// The nonadjacent case occurs at the bounded anchor-created split boundaries.
	switch {
	case at+1 < len(s.list) && s.list[at+1].schema == n.schema:
		add(t.indexFromEnd(d.list, s.list[at+1]))
	case at > 0 && s.list[at-1].schema == n.schema:
		add(t.indexFromEnd(d.list, s.list[at-1]) + 1)
	case t.indexFromEnd(s.list, d.list[len(d.list)-1]) < at:
		add(len(d.list))
	default:
		// ponytail: the full rebuild is reached once per anchor-created split boundary of an
		// output run; adjacent insertions on either side use the neighbour cases above.
		d = &opEntry{rank: d.rank, direction: d.direction, gaps: d.gaps}
		for _, a := range s.list {
			t.work.Add(1)
			if a.schema == n.schema {
				d.list = append(d.list, a)
			}
		}
		s.opIdx[n.schema] = d
	}
}

// opAnchor is lyd_insert_get_next_anchor for an operation's child n: the index of the sibling n
// goes before, len(sib.list) to append (before the opaque nodes).
//   - with the parent's children hash table: the first instance of the closest following schema
//     sibling in n's own input or output (lyd_find_sibling_schema: the instance index);
//   - without it (fewer than htMinItems children): the siblings walked against n's input or output
//     schema list, the anchor being the first sibling past n's schema node there; a sibling of the
//     other direction is never in that list, so an output node goes before the first input node
//     that does not precede it in the walk, and an input node before such an output node.
func (t *Tree) opAnchor(sib *siblings, n *Node) int {
	l := sib.list
	var opts schema.GetNextOpt
	if n.schema.InOutput() {
		opts = schema.GetNextOutput
	}
	order := t.opOrder(n.schema.DataParent(), opts == schema.GetNextOutput)
	if sib.ht != nil {
		// lyd_insert_get_next_anchor finds the closest following schema sibling that has an
		// instance. The occupied-rank summary skips absent schema siblings while preserving that
		// exact input/output-local search.
		d := 0
		if opts == schema.GetNextOutput {
			d = 1
		}
		r := sib.opRanks[d].next(t, t.schemaRank(n.schema))
		if r < 0 || r >= len(order) {
			return len(l)
		}
		inst := sib.opInst(order[r])
		// The anchor position scan is bounded by the ensuing insertion shift plus its run.
		return t.opFirstFrom(l, inst)
	}
	own := ownerModule(t.set, n)
	k, found := 0, false
	for m, a := range l {
		t.work.Add(1)
		if ownerModule(t.set, a) != own {
			return m // lyd_owner_module differs: the data of the next module
		}
		for !found {
			if k >= len(order) {
				return len(l) // extension instance data: no anchor
			}
			if order[k] == n.schema {
				found = true
				break
			}
			if a.schema == order[k] {
				break
			}
			k++
		}
		if found && a.schema != n.schema {
			return m
		}
	}
	return len(l)
}

// markRB records that the run of s has libyang's RB tree (created by its first sorted insertion
// into an existing run).
func markRB(sib *siblings, s *schema.Node) {
	if sib.rbTree == nil {
		sib.rbTree = map[*schema.Node]bool{}
	}
	sib.rbTree[s] = true
}

// markUnsorted records that the run of s has instances outside its RB tree (appended by
// insertLast/insertLastBySchema after the tree was made).
func markUnsorted(sib *siblings, s *schema.Node) {
	if sib.unsorted == nil {
		sib.unsorted = map[*schema.Node]bool{}
	}
	sib.unsorted[s] = true
}

// insert is lyd_insert_node: link the unlinked node n under parent (nil: the top level of t).
// A list instance must have all its keys (libyang links it into its parent only then).
func (t *Tree) insert(parent, n *Node, order insertOrder) {
	sib := t.childrenOf(parent)
	at := -1 // opaque: appended to the opaque nodes
	if n.schema != nil {
		at = t.insertPos(sib, n, order)
	}
	t.link(parent, sib, n, at)
}

// insertBefore is lyd_insert_before for a user-ordered instance: n goes right before anchor,
// an instance of the same schema node.
func (t *Tree) insertBefore(anchor, n *Node) {
	sib := anchor.siblingsOf()
	t.link(anchor.parent, sib, n, slices.Index(sib.list, anchor))
}

func (t *Tree) childrenOf(parent *Node) *siblings {
	if parent == nil {
		return &t.top
	}
	return &parent.kids
}

// link puts the unlinked n at position at of sib.list (an opaque node: of sib.opq, appended when
// at is -1) under parent, and updates the children index and the default flags of the NP-container
// ancestors.
func (t *Tree) link(parent *Node, sib *siblings, n *Node, at int) {
	sib.work = &t.work
	switch {
	case n.schema == nil && (at < 0 || at >= len(sib.opq)):
		sib.opq = append(sib.opq, n)
	case n.schema == nil:
		sib.opq = slices.Insert(sib.opq, at, n)
	default:
		moved := sib.insertAt(at, n)
		if t.work.shifts {
			t.work.Add(int64(moved))
		}
		switch {
		case !opChild(n.schema):
		case sib.opIdx == nil:
			sib.opBuild(t) // the operation's first child (or the first since a bulk removal)
		default:
			sib.opIdxAdd(t, n, at)
		}
	}
	t.attach(parent, sib, n)
}

// nodeDeque is a node slice with its backing allocation retained for front headroom.
type nodeDeque struct {
	list []*Node
	base []*Node
}

// insertAt puts n at position at and returns how many slots it shifted: the shorter side moves,
// the front into retained headroom, so appending and prepending are amortized O(1).
// ponytail: a single edit in the middle stays O(n/2) shifts (a 160k list: ~40µs); an ordered
// structure if API edits of huge sibling lists in random order ever matter.
func (d *nodeDeque) insertAt(at int, n *Node) int {
	l := d.list
	if at >= len(l)/2 || len(l) == 0 {
		c := cap(l)
		d.list = slices.Insert(l, at, n)
		if cap(d.list) != c {
			d.base = nil // reallocated: drop the old array
		}
		return len(l) - at
	}
	off := cap(d.base) - cap(l)
	if off <= 0 || off >= len(d.base) || &d.base[off] != &l[0] {
		// no headroom: copy the list between as much free space as it is long on either side
		buf := make([]*Node, 3*len(l)+1)
		off = len(l) + 1
		copy(buf[off:], l)
		d.base = buf
		l = buf[off : off+len(l)]
	}
	nl := d.base[off-1 : off+len(l)]
	copy(nl, l[:at])
	nl[at] = n
	d.list = nl
	return at
}

// deleteAt removes position at and returns the number of slots shifted, moving the shorter side.
func (d *nodeDeque) deleteAt(at int) int {
	l := d.list
	if at < len(l)-1-at {
		copy(l[1:at+1], l[:at])
		if len(d.base) == 0 {
			d.base = l[:cap(l)]
		}
		l[0] = nil
		d.list = l[1:]
		return at
	}
	copy(l[at:], l[at+1:])
	l[len(l)-1] = nil
	d.list = l[:len(l)-1]
	return len(l) - 1 - at
}

// insertAt applies the shared deque insertion to a sibling list. Batches (Merge, moved runs) are
// spliced in one pass instead (splice).
func (s *siblings) insertAt(at int, n *Node) int { return s.nodeDeque.insertAt(at, n) }

// attach finishes linking n, already placed in sib's lists, under parent.
func (t *Tree) attach(parent *Node, sib *siblings, n *Node) {
	if n.parent != nil || n.tree != nil {
		panic("data: inserting a linked node") // internal invariant: callers unlink first
	}
	n.parent = parent
	if parent == nil {
		n.tree = t
	}
	sib.gen++
	sib.hashAdd(parent, n)
	if n.flags&FlagDefault == 0 {
		npContDfltDel(parent)
	}
	if n.isKey() {
		rehashParent(parent) // the list's keys changed: lyd_hash + lyd_insert_hash of the parent
	}
}

// splice links the unlinked nodes ns, ascending, into the sorted run lo..hi of sib.list in one
// pass: each after the equal values, or before them when first. Linking them one by one would
// shift the list once per node (quadratic for a merged run).
func (t *Tree) splice(parent *Node, sib *siblings, lo, hi int, ns []*Node, first bool) {
	sib.work = &t.work
	old := sib.list
	out := make([]*Node, 0, len(old)+len(ns))
	out = append(out, old[:lo]...)
	j := lo
	for _, n := range ns {
		for j < hi && (first && t.less(old[j], n) || !first && !t.less(n, old[j])) {
			out = append(out, old[j])
			j++
		}
		out = append(out, n)
	}
	out = append(out, old[j:]...)
	sib.list, sib.base = out, nil
	if t.work.shifts {
		t.work.Add(int64(len(sib.list)))
	}
	if len(ns) > 0 && opChild(ns[0].schema) {
		sib.opBuild(t) // one counted pass, like the splice itself
	}
	for _, n := range ns {
		t.attach(parent, sib, n)
	}
}

// rehashParent moves the list instance l to the bucket of its current keys in its parent's index.
func rehashParent(l *Node) {
	if ls := l.siblingsOf(); ls != nil && l.parent != nil {
		ls.hashRemove(l)
		ls.hashAdd(l.parent, l)
	}
}

// unlinkCheck is lyd_unlink_check.
func unlinkCheck(n *Node) error {
	if n.isKey() {
		return &opError{"LY_EINVAL", fmt.Sprintf("Cannot unlink a list key \"%s\", unlink the list instance instead.", n.Name())}
	}
	return nil
}

// unlinkTree is lyd_unlink_tree: n with its subtree leaves its siblings.
func unlinkTree(n *Node) error {
	if err := unlinkCheck(n); err != nil {
		return err
	}
	unlink(n)
	return nil
}

// afterRemove applies rb_remove_node's run-state rule after instances of sn have left s. The
// surviving flags may be computed while compacting a batch so unlinkAll does not rescan s once
// per affected schema node.
func (s *siblings) afterRemove(sn *schema.Node, wasInRB, survives, survivesInRB bool) {
	if !survives || wasInRB && !survivesInRB {
		s.runGone(sn)
	}
}

// unlink is lyd_unlink (without the key check); n's own leafref links go
// (lyd_unlink_ignore_lyds).
func unlink(n *Node) {
	sib := n.siblingsOf()
	if sib == nil {
		return
	}
	freeLinks(n)
	if n.schema == nil {
		if i := slices.Index(sib.opq, n); i >= 0 {
			sib.opq = slices.Delete(sib.opq, i, i+1)
		}
	} else if i := slices.Index(sib.list, n); i >= 0 {
		sib.opRemove(n, i) // neighbours must still surround n
		sib.list = slices.Delete(sib.list, i, i+1)
	}
	sib.hashRemove(n)
	sib.gen++
	if n.schema != nil {
		survives := sib.has(n.schema)
		survivesInRB := !n.inRB || slices.ContainsFunc(sib.list, func(x *Node) bool {
			return x.schema == n.schema && x.inRB
		})
		sib.afterRemove(n.schema, n.inRB, survives, survivesInRB)
	}
	npContDfltSet(detach(n)) // the last non-default node may be gone
}

// detach clears the links of n, already removed from its siblings and their index, and returns
// its former parent.
func detach(n *Node) *Node {
	parent := n.parent
	wasKey := n.isKey()
	n.parent, n.tree, n.inRB = nil, nil, false
	if wasKey {
		rehashParent(parent)
	}
	return parent
}

// freeTree is lyd_free_tree: a list key is refused, nothing is freed but the leafref links of
// the subtree (lyd_free_subtree).
func freeTree(n *Node) error {
	if n.isKey() {
		return &opError{"LY_EINVAL", fmt.Sprintf("Cannot free a list key \"%s\", free the list instance instead.", n.Name())}
	}
	unlink(n)
	for d := range n.All() {
		freeLinks(d)
	}
	return nil
}

// unlinkAll unlinks every node of ns with one compaction per sibling list, for the bulk
// removals of validation (auto-deleted nodes) and lyd_free_siblings. A list key is refused
// before anything is unlinked.
func (t *Tree) unlinkAll(ns []*Node) error {
	return t.unlinkAllDefaults(ns, npContDfltAffected(ns))
}

// unlinkAllDefaults is unlinkAll with the non-presence-container ancestors whose default flags
// may change already collected. TrimXPath supplies them from its read-only preflight so their
// later scans can be charged before any mutation.
func (t *Tree) unlinkAllDefaults(ns, defaults []*Node) error {
	for _, n := range ns {
		if err := unlinkCheck(n); err != nil {
			return err
		}
	}
	gone := map[*Node]bool{}
	sibs := map[*siblings]map[idxKey]bool{}              // touched lists and their touched buckets
	removedRuns := map[*siblings]map[*schema.Node]bool{} // affected schemas; value: any removed node was inRB
	var order []*siblings
	for _, n := range ns {
		sib := n.siblingsOf()
		if sib == nil || gone[n] {
			continue
		}
		gone[n] = true
		freeLinks(n) // lyd_unlink_ignore_lyds
		if sibs[sib] == nil {
			sibs[sib] = map[idxKey]bool{}
			order = append(order, sib)
		}
		if n.schema != nil {
			if removedRuns[sib] == nil {
				removedRuns[sib] = map[*schema.Node]bool{}
			}
			removedRuns[sib][n.schema] = removedRuns[sib][n.schema] || n.inRB
		}
		if n.hashed {
			sibs[sib][n.hkey] = true
			n.hashed = false
		}
	}
	isGone := func(n *Node) bool { t.work.Add(1); return gone[n] }
	for _, sib := range order {
		sib.gen++
		sib.list = slices.DeleteFunc(sib.list, isGone)
		if sib.opIdx != nil {
			sib.opBuild(t)
		}
		sib.opq = slices.DeleteFunc(sib.opq, isGone)
		for k := range sibs[sib] { // each bucket compacted once
			if b := slices.DeleteFunc(sib.ht[k], isGone); len(b) > 0 {
				sib.ht[k] = b
			} else {
				delete(sib.ht, k)
			}
		}
	}
	for _, sib := range order {
		present := map[*schema.Node]bool{}
		indexed := map[*schema.Node]bool{}
		for _, n := range sib.list {
			present[n.schema] = true
			if n.inRB {
				indexed[n.schema] = true
			}
		}
		for s, wasInRB := range removedRuns[sib] {
			sib.afterRemove(s, wasInRB, present[s], indexed[s])
		}
	}
	for _, n := range ns {
		if gone[n] {
			delete(gone, n)
			detach(n)
		}
	}
	npContDfltSetAll(defaults)
	return nil
}

// npContDfltAffected returns the retained non-presence-container ancestors whose default flags
// may change when ns are removed, deepest first. Each ancestor is returned once so a wide batch
// cannot repeatedly scan a shared parent's growing prefix.
func npContDfltAffected(ns []*Node) []*Node {
	gone := make(map[*Node]bool, len(ns))
	for _, n := range ns {
		gone[n] = true
	}
	seen := map[*Node]bool{}
	var affected []*Node
	for _, n := range ns {
		p := n.parent
		for p != nil && gone[p] {
			p = p.parent
		}
		for p != nil && p.flags&FlagDefault == 0 && isNPCont(p.schema) {
			if seen[p] {
				break
			}
			seen[p] = true
			affected = append(affected, p)
			p = p.parent
		}
	}

	// Bucket by tree depth instead of sorting with repeated ancestor walks. The memoized depth
	// calculation visits every ancestor chain only once.
	depths := map[*Node]int{}
	maxDepth := 0
	for _, n := range affected {
		path := []*Node{}
		p := n
		base := 0
		for p != nil {
			if d, ok := depths[p]; ok {
				base = d
				break
			}
			path = append(path, p)
			p = p.parent
		}
		for i := len(path) - 1; i >= 0; i-- {
			base++
			depths[path[i]] = base
		}
		if depths[n] > maxDepth {
			maxDepth = depths[n]
		}
	}
	buckets := make([][]*Node, maxDepth+1)
	for _, n := range affected {
		buckets[depths[n]] = append(buckets[depths[n]], n)
	}
	affected = affected[:0]
	for depth := maxDepth; depth > 0; depth-- {
		affected = append(affected, buckets[depth]...)
	}
	return affected
}

// npContDfltSetAll recomputes each affected container once. Children precede parents, so a
// parent observes the final default flags of all of its affected children.
func npContDfltSetAll(affected []*Node) {
	for _, p := range affected {
		if p.flags&FlagDefault != 0 {
			continue
		}
		allDefault := true
		for c := range p.kids.all() {
			if c.flags&FlagDefault == 0 {
				allDefault = false
				break
			}
		}
		if allDefault {
			p.flags |= FlagDefault
		}
	}
}

// isNPCont is lysc_is_np_cont.
func isNPCont(s *schema.Node) bool { return s != nil && s.Kind == schema.Container && !s.Presence }

// npContDfltSet is lyd_np_cont_dflt_set: an NP container whose children are all default becomes
// default, up the ancestors.
func npContDfltSet(p *Node) {
	for ; p != nil && p.flags&FlagDefault == 0 && isNPCont(p.schema); p = p.parent {
		for c := range p.kids.all() {
			if c.flags&FlagDefault == 0 {
				return
			}
		}
		p.flags |= FlagDefault
	}
}

// npContDfltDel is lyd_np_cont_dflt_del: ancestors with an explicit descendant are not default.
func npContDfltDel(p *Node) {
	for ; p != nil && p.flags&FlagDefault != 0; p = p.parent {
		p.flags &^= FlagDefault
	}
}
