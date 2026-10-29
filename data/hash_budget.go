// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// Sparse LYB payloads can represent hundreds of MiB with an empty string. The
// per-zero-run checkpoint bound therefore needs a cumulative operation bound.
// Keep this internal, like the existing fixed XPath expression work limit.
const defaultMaxHashWork uint64 = 1 << 26

func (w *workCounter) beginHashWork() func() {
	if w.hashDepth == 0 {
		w.hashUsed, w.hashErr, w.unlinkPlan, w.sortKeys = 0, nil, nil, nil
	}
	w.hashDepth++
	return func() {
		w.hashDepth--
		if w.hashDepth == 0 {
			w.unlinkPlan, w.sortKeys = nil, nil
		}
	}
}

// overdraftPanics makes an overdrawn prepaid credit, and a fallible check inside a prepaid
// scope, a panic: tests set it, so every sweep checks every bound (design 13 §4.3).
var overdraftPanics bool

// prepaySkew under-reserves link's apply bound and its index term by that many units each, and overdraftHook observes the
// production overdraft path (tests of design 13 §4.3; zero and nil otherwise).
var (
	// unpaidReadPanics makes an index hook that fires with no credit inside a hash-work scope a
	// panic (tests: an unpaid read on a mutation path, design 13 §4.4)
	unpaidReadPanics bool
	prepaySkew       uint64
	overdraftHook    func(*workCounter)
)

// prepay opens a prepaid scope (design 13 §4.1): it debits amount now, refusing like any charge,
// and until closeCredit every chargeHash of w draws from the credit and cannot refuse. A write
// credit refunds what is left at closeCredit; a read credit (read) is not refunded: its bound is
// the charge. Inside an open scope it opens nothing and draws amount (nested reports it).
func (w *workCounter) prepay(amount uint64, read bool) (nested bool, err error) {
	if w.prepaid {
		w.draw(amount)
		return true, nil
	}
	if err := w.chargeHash(amount); err != nil {
		return false, err
	}
	w.prepaid, w.credit, w.readCredit = true, amount, read
	return false, nil
}

// openRead opens a read credit of bound for a class-A index read (design 13 §4.4): on a mutation
// path (a hash-work scope open) outside a prepaid scope, the reads that follow draw from it and
// the bound is their charge (not refunded). Inside a prepaid scope the write's bound pays them
// (class B), and a read-only path (no scope) or user code under a suspended state pays nothing
// (class C): opened is false then.
func (w *workCounter) openRead(bound uint64) (opened bool, err error) {
	if w == nil || w.prepaid || w.hashDepth == 0 || w.journal.suspendedState() {
		return false, nil // user code reading while its state waits is class C too
	}
	if _, err := w.prepay(bound, true); err != nil {
		return false, err
	}
	return true, nil
}

// closeRead ends a read credit openRead opened.
func (w *workCounter) closeRead(opened bool) {
	if opened {
		w.closeCredit(false)
	}
}

// closeCredit ends the scope prepay opened (nested: nothing to end).
func (w *workCounter) closeCredit(nested bool) {
	if nested || !w.prepaid {
		return
	}
	if !w.readCredit {
		w.hashUsed -= w.credit
	}
	w.prepaid, w.credit, w.readCredit = false, 0, false
}

// draw takes amount from the open credit. An overdraft means a bound is wrong: tests panic;
// production debits the excess without a limit check, so nothing after a storage write fails on
// budget, and the over-limit counter refuses at the next fallible check outside the scope.
func (w *workCounter) draw(amount uint64) {
	if amount <= w.credit {
		w.credit -= amount
		return
	}
	if overdraftPanics {
		panic(fmt.Sprintf("data: prepaid credit overdrawn by %d units", amount-w.credit))
	}
	w.hashUsed = addHashBound(w.hashUsed, amount-w.credit)
	w.credit = 0
	if overdraftHook != nil {
		overdraftHook(w)
	}
}

func (w *workCounter) checkHashWork(amount uint64) error {
	if w.prepaid {
		if overdraftPanics && amount != 0 {
			panic("data: a fallible budget check inside a prepaid scope")
		}
		return w.hashErr
	}
	return w.checkLimit(amount)
}

// checkLimit tests amount against the limit, ignoring any prepaid scope: journal captures are
// checked against hashUsed, which already includes the whole prepaid amount.
func (w *workCounter) checkLimit(amount uint64) error {
	if w.hashErr != nil {
		return w.hashErr
	}
	if w.hashTrace != nil && amount != 0 {
		*w.hashTrace = append(*w.hashTrace, addHashBound(w.hashUsed, amount))
	}
	limit := w.hashLimit
	if limit == 0 {
		limit = defaultMaxHashWork
	}
	if amount > limit || w.hashUsed > limit-amount {
		return fmt.Errorf("%w: more than %d stored-hash work units", yang.ErrBudget, limit)
	}
	return nil
}

func (w *workCounter) chargeHash(amount uint64) error {
	if w.prepaid {
		w.draw(amount)
		return nil
	}
	return w.chargeLimited(amount)
}

// chargeLimited charges amount outside any credit: a journal capture never consumes units the
// prepaid suffix needs.
func (w *workCounter) chargeLimited(amount uint64) error {
	if err := w.checkLimit(amount); err != nil {
		w.hashErr = err
		return err
	}
	w.hashUsed += amount
	return nil
}

func addHashBound(total, next uint64) uint64 {
	if next > ^uint64(0)-total {
		return ^uint64(0)
	}
	return total + next
}

// checkTermHash preflights a key link at its final position. A partial positional
// prefix has no native hash event, even if many copies of its first key follow it.
func (w *workCounter) checkTermHash(parent *Node, sn *schema.Node, value types.Value, insertionAt ...int) error {
	amount, err := w.termHashBound(parent, sn, value, insertionAt...)
	if err != nil {
		return err
	}
	return w.checkHashWork(amount)
}

// termHashBound is the hash work checkTermHash reserves, for callers that add
// it to the rest of one link's work and check the total once.
func (w *workCounter) termHashBound(parent *Node, sn *schema.Node, value types.Value, insertionAt ...int) (uint64, error) {
	if sn.Kind == schema.LeafList {
		return types.HashLYBWorkBound(value), nil
	}
	if !sn.IsKey() || parent == nil || parent.schema == nil || len(insertionAt) == 0 {
		return 0, nil
	}
	at := insertionAt[0]
	if at < parent.lkeyN && parent.lkeyFrozen == nil {
		// beforeKeyLink copies the captured prefix before publishing the key.
		prefix := uint64(parent.lkeyN) //nolint:gosec // a count of existing nodes cannot be negative
		if err := w.chargeHash(prefix); err != nil {
			return 0, err
		}
	}
	// Inspect only the declared positional prefix before considering hash inputs.
	// In particular, repeated first keys must not rescan the entire incomplete run.
	for i, key := range parent.schema.Keys {
		w.Add(1)
		if err := w.chargeHash(1); err != nil {
			return 0, err
		}
		var current *schema.Node
		switch {
		case i == at:
			current = sn
		case i < parent.kids.SchemaLen() && i < at:
			current = parent.kids.At(i).schema
		case i > at && i-1 < parent.kids.SchemaLen():
			current = parent.kids.At(i - 1).schema
		}
		if current != key {
			return 0, nil
		}
	}
	// keyLinked can restart from the list's schema prefix after insertion.
	schemaHashWork(parent.schema, w)
	if w.hashErr != nil {
		return 0, w.hashErr
	}
	// A displaced snapshot or an incomplete old prefix rehashes all leading
	// keys. An intact prefix hashes only the new suffix.
	start := parent.lkeyN
	if !parent.lkeyOK || parent.lkeyDirty || parent.lkeyFrozen != nil || at < parent.lkeyN {
		start = 0
	}
	var amount uint64
	for i := start; i <= parent.kids.SchemaLen(); i++ {
		w.Add(1)
		if err := w.chargeHash(1); err != nil {
			return 0, err
		}
		if i == at {
			amount = addHashBound(amount, types.HashLYBWorkBound(value))
			continue
		}
		old := i
		if i > at {
			old--
		}
		if !parent.kids.At(old).isKey() {
			break
		}
		amount = addHashBound(amount, types.HashLYBWorkBound(parent.kids.At(old).value))
	}
	if ps := parent.siblingsOf(); ps != nil && ps.ht != nil {
		amount = addHashBound(amount, indexInsertedBound(parent, &Node{schema: sn, value: value}, at, w))
		if w.hashErr != nil {
			return 0, w.hashErr
		}
	}
	// keyLinked ends with reindexComparedParent(parent): an outer list whose
	// compared prefix exposes parent is reindexed in its own siblings.
	if gp := parent.parent; gp != nil && gp.schema != nil && gp.schema.Kind == schema.List &&
		gp.keyN < len(gp.schema.Keys) {
		if gs := gp.siblingsOf(); gs != nil && gs.ht != nil {
			for i := range gp.schema.Keys {
				w.Add(1)
				if positionalChild(gp, i) == parent {
					amount = addHashBound(amount, indexWorkBound(gp, nil, nil, w))
					break
				}
			}
			if w.hashErr != nil {
				return 0, w.hashErr
			}
		}
	}
	return amount, nil
}

// checkChangedHash returns the pending native hash work without reserving it:
// the hash event charges it. Callers recheck after any later preflight charges
// and before unlinking or replacing the value.
func (w *workCounter) checkChangedHash(n *Node, value types.Value) (uint64, error) {
	if types.Equal(n.value, value) {
		return 0, nil
	}
	schemaHashWork(n.schema, w)
	if w.hashErr != nil {
		return 0, w.hashErr
	}
	if n.schema.Kind == schema.LeafList {
		amount := types.HashLYBWorkBound(value)
		return amount, w.checkChangedIndex(n, n, &value, amount)
	}
	if !n.isKey() {
		return 0, w.checkChangedIndex(n.parent, n, &value, 0)
	}
	if n.parent.schema != nil {
		schemaHashWork(n.parent.schema, w)
		if w.hashErr != nil {
			return 0, w.hashErr
		}
	}
	var amount uint64
	for _, key := range n.parent.kids.SchemaAll() {
		w.Add(1)
		if err := w.chargeHash(1); err != nil {
			return 0, err
		}
		if !key.isKey() {
			break
		}
		v := key.value
		if key == n {
			v = value
		}
		amount = addHashBound(amount, types.HashLYBWorkBound(v))
	}
	return amount, w.checkChangedIndex(n.parent, n, &value, amount)
}

func (w *workCounter) checkChangedIndex(target, changed *Node, value *types.Value, native uint64) error {
	amount, err := w.changedIndexAmount(target, changed, value, native)
	if err != nil {
		return err
	}
	return w.checkHashWork(amount)
}

// changedIndexAmount is the work of a value change checkChangedIndex checks: the native hash
// event, the target's discriminator and, for ancestor reindexing, the compared parent's. The
// in-place change prepays it (design 13 §4.2).
func (w *workCounter) changedIndexAmount(target, changed *Node, value *types.Value, native uint64) (uint64, error) {
	if target == nil {
		return native, w.hashErr
	}
	amount := native
	sib := target.siblingsOf()
	if sib != nil && sib.ht != nil {
		amount = addHashBound(amount, indexWorkBound(target, changed, value, w))
	}
	if parent := target.parent; parent != nil && parent.schema != nil && parent.schema.Kind == schema.List {
		if ps := parent.siblingsOf(); ps != nil && ps.ht != nil {
			for i := range parent.schema.Keys {
				if positionalChild(parent, i) == target {
					amount = addHashBound(amount, indexWorkBound(parent, changed, value, w))
					break
				}
			}
		}
	}
	return amount, w.hashErr
}

func (w *workCounter) checkRefreshHash(n *Node) error {
	if n.schema == nil {
		return nil
	}
	schemaHashWork(n.schema, w)
	if w.hashErr != nil {
		return w.hashErr
	}
	if n.schema.Kind == schema.LeafList {
		return w.checkHashWork(types.HashLYBWorkBound(n.value))
	}
	if n.schema.Kind != schema.List || n.schema.Keyless() {
		return nil
	}
	var amount uint64
	for _, key := range n.kids.SchemaAll() {
		w.Add(1)
		if err := w.chargeHash(1); err != nil {
			return err
		}
		if !key.isKey() {
			break
		}
		amount = addHashBound(amount, types.HashLYBWorkBound(key.value))
	}
	return w.checkHashWork(amount)
}
