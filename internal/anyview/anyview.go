// SPDX-License-Identifier: BSD-3-Clause

// Package anyview is the in-module bridge for the conformance engine's typed
// dump. It keeps any payload traversal out of package data's public API.
package anyview

// Value is the representation reported by libyang's typed any observer.
type Value struct {
	Type     any
	Text     any
	Children []any
}

var inspect func(any) (Value, bool)

// Register installs the data package's observer during package initialization.
func Register(f func(any) (Value, bool)) { inspect = f }

// Inspect reports an any node's stored value and payload children.
func Inspect(n any) (Value, bool) {
	if inspect == nil {
		return Value{}, false
	}
	return inspect(n)
}
