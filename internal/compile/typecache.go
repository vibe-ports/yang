// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_schema_free.c (lysc_type_free) and the refcount handling
// of src/schema_compile_node.c (BSD-3-Clause, © CESNET).

package compile

import (
	"maps"

	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// typeCache mirrors libyang's typedef cache: the compiled type stored on each parsed typedef
// (lysp_type.compiled) and the reference count of every compiled type (lysc_type.refcount).
//
// The count is per type object, not per typedef, because an unchanged derived typedef stores its
// base's object. Holders are exactly libyang's increments: the cache entry itself, an unchanged
// derived typedef reusing the base, every union member slot (including members copied from a
// union base), a leaf or leaf-list taking the type, and a leafref's realtype. A cached type held
// by nothing but its cache entry (count 1) is discarded and recompiled at the next lookup, so
// whether a later user shares a type (and its leafref members, bound to the module that compiled
// them first) depends on who still holds it. The cache lives as long as the context snapshot;
// clone it with the snapshot.
type typeCache struct {
	compiled map[*parser.Node]*schema.Type // typedef → its compiled type
	refs     map[*schema.Type]int          // holders of each live compiled type
}

func newTypeCache() *typeCache {
	return &typeCache{compiled: map[*parser.Node]*schema.Type{}, refs: map[*schema.Type]int{}}
}

func (c *typeCache) clone() *typeCache {
	return &typeCache{compiled: maps.Clone(c.compiled), refs: maps.Clone(c.refs)}
}

// hold adds a holder of t (LY_ATOMIC_INC_BARRIER(type->refcount)).
func (c *typeCache) hold(t *schema.Type) { c.refs[t]++ }

// release drops a holder of t; the last one frees it, which releases what t holds itself: its
// union members and a leafref's realtype (lysc_type_free). Iterative: member chains are
// bounded only by the budget.
func (c *typeCache) release(t *schema.Type) {
	for work := []*schema.Type{t}; len(work) > 0; {
		t, work = work[len(work)-1], work[:len(work)-1]
		if t == nil {
			continue
		}
		if n := c.refs[t]; n > 1 {
			c.refs[t] = n - 1
			continue
		}
		delete(c.refs, t)
		switch t.Base {
		case schema.Union:
			work = append(work, t.Union...)
		case schema.Leafref:
			work = append(work, t.Realtype)
		}
	}
}
