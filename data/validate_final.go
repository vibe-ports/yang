// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/validation.c (BSD-3-Clause, © CESNET).

package data

import (
	"context"

	"github.com/vibe-ports/yang"
)

// ValidateModuleFinal runs libyang's final recursive pass for one implemented module.
// It checks must, mandatory, min/max-elements, unique, unexpected and obsolete nodes,
// and mutates non-presence-container default flags. It creates no implicit nodes or diff
// and does not run when or type-plugin value/reference checks. Use it after NotFinal
// validation once the tree is final; finalize every relevant module. It is not full
// validation after arbitrary intervening edits.
//
// Present or NotFinal, or an unknown or not implemented module, returns LY_EINVAL
// without a diagnostic. Other errors and diagnostics follow Validate's contract,
// including cancellation and resource budgets. Validation errors may leave partial
// mutations. Like Validate, this method requires exclusive access to t.
func (t *Tree) ValidateModuleFinal(ctx context.Context, module string, o ValidateOptions) (_ []yang.Diagnostic, err error) {
	if t.work.inProgress() { // an operation in progress governs t: refused before its counter is used
		return refusedEntryDiags(t.context())
	}
	ctx = suspending(ctx, &t.work) // its polls run the user's context suspended (design 13 §2.4)
	defer func() { err = restoreUser(err) }()
	// validation.c:2322 uses a NULL logging context for both argument checks.
	mod := t.context().Implemented(module)
	if mod == nil {
		return nil, &ValidationError{err: argErr("module", "lyd_validate_module_final"), rc: "LY_EINVAL"}
	}
	return t.validateModuleFinal(ctx, mod, o, Budget{})
}
