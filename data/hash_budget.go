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
		w.hashUsed, w.hashErr, w.sortKeys = 0, nil, nil
	}
	w.hashDepth++
	return func() {
		w.hashDepth--
		if w.hashDepth == 0 {
			w.sortKeys = nil
		}
	}
}

func (w *workCounter) checkHashWork(amount uint64) error {
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
	if err := w.checkHashWork(amount); err != nil {
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
		case i < len(parent.kids.list) && i < at:
			current = parent.kids.list[i].schema
		case i > at && i-1 < len(parent.kids.list):
			current = parent.kids.list[i-1].schema
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
	for i := start; i <= len(parent.kids.list); i++ {
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
		if !parent.kids.list[old].isKey() {
			break
		}
		amount = addHashBound(amount, types.HashLYBWorkBound(parent.kids.list[old].value))
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
	for _, key := range n.parent.kids.list {
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
	if target == nil {
		return w.checkHashWork(native)
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
	if w.hashErr != nil {
		return w.hashErr
	}
	return w.checkHashWork(amount)
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
	for _, key := range n.kids.list {
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
