// SPDX-License-Identifier: BSD-3-Clause

package yang

import "github.com/vibe-ports/yang/internal/snap"

// The read-only handles of a schema snapshot (design 06 §4). They are aliases of opaque types:
// no exported fields, methods return values and iterators only.
type (
	// Schema is an immutable snapshot of a context's modules, from Context.Schema.
	Schema = snap.Schema
	// Module is a module of a snapshot.
	Module = snap.Module
	// SchemaNode is a compiled schema node.
	SchemaNode = snap.Node
	// Type is a compiled type.
	Type = snap.Type
	// Identity is a compiled identity.
	Identity = snap.Identity
	// Must is a must restriction.
	Must = snap.Must
	// When is a when condition.
	When = snap.When
	// Extension is a compiled extension instance.
	Extension = snap.Extension
	// Kind is a schema node kind.
	Kind = snap.Kind
	// Status is a YANG status.
	Status = snap.Status
)

// Schema node kinds.
const (
	KindContainer    = snap.Container
	KindChoice       = snap.Choice
	KindCase         = snap.Case
	KindLeaf         = snap.Leaf
	KindLeafList     = snap.LeafList
	KindList         = snap.List
	KindAnyXML       = snap.AnyXML
	KindAnyData      = snap.AnyData
	KindRPC          = snap.RPC
	KindAction       = snap.Action
	KindInput        = snap.Input
	KindOutput       = snap.Output
	KindNotification = snap.Notification
)

// Status values.
const (
	StatusCurrent    = snap.Current
	StatusDeprecated = snap.Deprecated
	StatusObsolete   = snap.Obsolete
)
