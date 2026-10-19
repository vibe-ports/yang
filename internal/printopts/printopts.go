// SPDX-License-Identifier: BSD-3-Clause

// Package printopts carries printer flags that are intentionally not part of data.PrintOptions'
// public API. It is internal so the conformance engine can exercise libyang-only printer flags.
package printopts

import "io"

// Flags are internal libyang printer options.
type Flags uint8

const (
	// EmptyCont is LYD_PRINT_EMPTY_CONT.
	EmptyCont Flags = 1 << iota
	// JSONNoNestedPrefix is LYD_PRINT_JSON_NO_NESTED_PREFIX.
	JSONNoNestedPrefix
)

type writer struct {
	io.Writer
	flags Flags
}

// With attaches flags to w across the public printer API boundary.
func With(w io.Writer, flags Flags) io.Writer {
	w, existing := Unwrap(w)
	flags |= existing
	if flags == 0 {
		return w
	}
	return writer{Writer: w, flags: flags}
}

// Unwrap returns the underlying writer and any flags attached by With.
func Unwrap(w io.Writer) (io.Writer, Flags) {
	if fw, ok := w.(writer); ok {
		return fw.Writer, fw.flags
	}
	return w, 0
}
