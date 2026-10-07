// SPDX-License-Identifier: BSD-3-Clause

// Package yang loads and compiles YANG 1.0 and 1.1 modules (RFC 6020, RFC 7950) with the
// behaviour of libyang v5.8.6, in pure Go: no cgo, no libyang, no libpcre2.
//
// It is a source port of libyang made with AI coding agents. "Behaviour of libyang" is measured,
// not assumed: every fixture of the conformance corpus is run through libyang v5.8.6 and through
// this package, and the verdicts, diagnostics and printed output are compared. Intentional
// differences are listed in conformance/deviations.md; anything else that differs is a bug.
//
// # Contexts and schema snapshots
//
// A [Context] is libyang's ly_ctx: a set of modules searched for in [io/fs.FS] directories
// (os.DirFS, embed.FS, testing/fstest.MapFS) or supplied by [Options.Loader]. [NewContext] loads
// libyang's internal modules (ietf-inet-types, ietf-yang-types, ietf-yang-metadata, yang, default,
// ietf-yang-schema-mount, ietf-yang-structure-ext, and ietf-datastores and ietf-yang-library
// unless [Options.NoYangLibrary]), embedded from libyang's module directory;
// [Context.Load] loads a module with its imports and includes, implements it with the chosen
// features and compiles the context (grouping/uses/refine, augment, if-feature, identities,
// typedef chains, must/when/leafref XPath).
//
// [Context.Schema] returns a [Schema]: an immutable snapshot of the compiled modules. A later
// Load publishes a new snapshot; one obtained before stays valid and unchanged. A Schema may be
// read by any number of goroutines, and a Context is safe for concurrent use.
//
// The snapshot is read through handles — [Module], [SchemaNode], [Type], [Identity], [Must],
// [When], [Extension] — with no exported fields: their methods return values, copies and
// [iter.Seq] iterators, never the compiler's slices. The handle types are aliases of types in an
// internal package; their methods are part of this package's API all the same. Handles are fresh
// wrappers per call: compare what they return (names, paths), not the handles with ==.
//
// # Diagnostics and errors
//
// Load and NewContext return the messages libyang would log as [Diagnostic] values — LY_ERR and
// LY_VECODE names, schema path, line, message — separately from the error, which is non-nil only
// when the call failed. Warnings come with a nil error. Errors caused by a resource limit
// ([ParseBudget], [Options.MaxSearchDirs], the compiler's budgets) wrap [ErrBudget]; input this
// port does not handle yet (YIN modules, modules with deviation statements, schema-mount and
// yang-data extension instances; see the Unsupported table of conformance/deviations.md) wraps
// [ErrUnsupported].
//
// # Data
//
// Instance data lives in package [github.com/vibe-ports/yang/data]: JSON (RFC 7951) and XML
// parsing against a Schema, validation with libyang's diagnostics, defaults, printing with the
// with-defaults modes (RFC 6243), and tree edits.
//
// # Stability
//
// The module is v0: the API may change in any release until the API review before v1.
package yang
