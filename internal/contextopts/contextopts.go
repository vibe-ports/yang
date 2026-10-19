// SPDX-License-Identifier: BSD-3-Clause

// Package contextopts carries context operations that are intentionally not part of the public
// API. The root yang package registers their implementations for the conformance engine.
package contextopts

import "github.com/vibe-ports/yang/internal/snap"

// LoadRevisionFunc loads a module while preserving nil versus an explicit empty revision.
type LoadRevisionFunc func(ctx any, name string, revision *string, features []string) ([]snap.Diagnostic, error)

var loadRevision LoadRevisionFunc

// RegisterLoadRevision registers the root package implementation.
func RegisterLoadRevision(f LoadRevisionFunc) {
	if loadRevision != nil {
		panic("contextopts: LoadRevision already registered")
	}
	loadRevision = f
}

// LoadRevision invokes the root package implementation registered during package initialization.
func LoadRevision(ctx any, name string, revision *string, features []string) ([]snap.Diagnostic, error) {
	if loadRevision == nil {
		panic("contextopts: LoadRevision not registered")
	}
	return loadRevision(ctx, name, revision, features)
}
