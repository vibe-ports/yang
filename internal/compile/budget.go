// SPDX-License-Identifier: BSD-3-Clause

package compile

import "fmt"

// Budget limits what one Load may compile (Options.Budget, design 06 §5); zero fields take the
// defaults, an exceeded limit is an error wrapping ErrBudget. libyang has none of these limits
// (deviations.md "Unsupported").
type Budget struct {
	// MaxTypes caps compiled type objects plus union member slots (U-0030): union flattening is
	// exponential in the schema text.
	MaxTypes int
	// MaxUnionMembers caps the flattened members of one union (U-0030): each value is tried
	// against every member.
	MaxUnionMembers int
	// MaxBitPosition caps bit positions (U-0031): a bits value carries a bitmap up to the
	// highest position.
	MaxBitPosition uint32
}

// Defaults of Budget.
const (
	DefaultMaxTypes        = 1 << 20
	DefaultMaxUnionMembers = 1 << 16
	DefaultMaxBitPosition  = 1<<16 - 1
)

func budgetErr(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrBudget}, a...)...)
}
