// SPDX-License-Identifier: BSD-3-Clause

// Package resource identifies errors caused by resource limits.
package resource

import "errors"

type sentinel struct{ text string }

func (e *sentinel) Error() string  { return e.text }
func (*sentinel) resourceFailure() {}

// New returns a distinct resource-limit sentinel with the supplied text.
func New(text string) error { return &sentinel{text: text} }

// Is reports whether err or an error it wraps is a resource-limit failure.
func Is(err error) bool {
	var target interface{ resourceFailure() }
	return errors.As(err, &target)
}
