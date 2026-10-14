// SPDX-License-Identifier: BSD-3-Clause

//go:build race

package data

// raceSlowdown scales wall-clock limits of tests under the race detector.
const raceSlowdown = 10
