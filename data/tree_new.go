// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/snap"
)

// NewTree is an empty data tree over the schema snapshot s: the NULL tree that lyd_new_path
// (with no parent and the context of s) and lyd_validate_all fill. A zero Tree has no schema, so
// NewPath on it finds no module; start from NewTree instead. s must not be nil.
func NewTree(s *yang.Schema) *Tree { return newTree(snap.Set(s)) }
