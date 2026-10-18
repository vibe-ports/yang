// SPDX-License-Identifier: BSD-3-Clause

//go:build !race

package data

// raceEnabled is true under the race detector; see scaleSizes.
const raceEnabled = false
