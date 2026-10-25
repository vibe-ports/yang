// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"

	"github.com/vibe-ports/yang"
)

// A cross-context diff indexes both input sibling sets. Keep a fixed internal ceiling, like the
// stored-hash and XPath ceilings, so an API without an explicit Budget cannot consume unbounded
// CPU or memory on a generated schema (U-0111).
const defaultMaxDiffLookupWork uint64 = 1 << 16

func (w *workCounter) beginLookupWork() func() {
	if w.lookupDepth == 0 {
		w.lookupUsed, w.lookupErr = 0, nil
	}
	w.lookupDepth++
	return func() { w.lookupDepth-- }
}

func (w *workCounter) chargeLookup(amount uint64) error {
	if w.lookupErr != nil {
		return w.lookupErr
	}
	limit := w.lookupLimit
	if limit == 0 {
		limit = defaultMaxDiffLookupWork
	}
	if amount > limit || w.lookupUsed > limit-amount {
		w.lookupErr = fmt.Errorf("%w: more than %d cross-context diff lookup work units", yang.ErrBudget, limit)
		return w.lookupErr
	}
	w.lookupUsed += amount
	return nil
}
