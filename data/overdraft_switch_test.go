// SPDX-License-Identifier: BSD-3-Clause

package data

// Every test build checks every prepaid bound (design 13 §4.3): an overdrawn credit, or a
// fallible check inside a prepaid scope, panics instead of debiting silently; and every borrowed
// sibling span is checked against its storage (design 14).
func init() { overdraftPanics, unpaidReadPanics, checkSpans = true, true, true }
