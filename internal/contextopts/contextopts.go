// SPDX-License-Identifier: BSD-3-Clause

// Package contextopts carries internal context and data operations for the conformance engine.
// The root yang and data packages register their implementations during initialization.
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

// NodeSchema is installed by data when it is initialized.
var NodeSchema func(any) *snap.Schema

// NewPathUpdateContext returns the context of the term the tree's last NewPath updated (it
// logs that update's store diagnostics), nil when that call updated no term.
var NewPathUpdateContext func(any) *snap.Schema

// TreeSchema returns the schema snapshot of a tree.
var TreeSchema func(any) *snap.Schema

// ApplyDiffModule retains the module selected in the sequence step's context when the
// data tree belongs to a different snapshot.
var ApplyDiffModule func(any, any, *snap.Schema, string) error

// MergeDiffModule retains the module selected in the sequence step's context when the
// diff tree belongs to a different snapshot.
var MergeDiffModule func(any, any, *snap.Schema, string, bool) error

// DiscardTree releases a tree and its reciprocal links in one traversal.
var DiscardTree func(any) error

// ContextDiagnostic is a diagnostic still stored on the context that emitted it.
type ContextDiagnostic struct {
	Context    *snap.Schema
	Diagnostic snap.Diagnostic
}

// DrainDiffDiagnostics reads the diagnostics emitted by context-specific
// lyd_find_meta calls during the last diff merge.
var DrainDiffDiagnostics func(any) []ContextDiagnostic

// DrainContextDiagnostics reads diagnostics retained by their emitting context on a data tree.
var DrainContextDiagnostics func(any) []ContextDiagnostic

// EnableContextDiagnostics opts a data tree into retaining diagnostics emitted by foreign
// contexts. It is used only by the conformance adapter, which models libyang context logs.
var EnableContextDiagnostics func(any)

// CheckDupContexts checks ordinary duplication's operand contexts.
var CheckDupContexts func(any, any) error

// CheckInsertSchema checks the node's schema against a parent before insertion.
var CheckInsertSchema func(any, any) error

// PrintNode prints a detached node for the conformance adapter.
var PrintNode func(any, uint8) (map[string]any, []snap.Diagnostic, error)
