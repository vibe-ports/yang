// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestFindPath: lys_find_path (LY_PATH_BEGIN_EITHER, LY_PATH_PREFIX_FIRST, LY_PATH_PRED_SIMPLE,
// LY_PATH_TARGET_MANY, JSON module-name prefixes), with libyang's messages for the failures.
func TestFindPath(t *testing.T) {
	m, n, _ := lrefFixture()
	n["k"].Type = &schema.Type{Base: schema.String}
	n["ll"].Type = &schema.Type{Base: schema.String}
	set := &schema.Set{Modules: []*schema.Module{m}}
	for _, tc := range []struct {
		ctx    *schema.Node
		path   string
		output bool
		want   string // node name, or the message
	}{
		{nil, "/m:c/a", false, "a"},
		{nil, "/m:c/l/v", false, "v"},        // no predicate needed on an inner list (TARGET_MANY)
		{nil, "/m:c/l", false, "l"},          // nor on the last node
		{nil, "/m:c/l[k='x']/v", false, "v"}, // a key predicate is compiled
		{nil, "/m:c/l/ll[.='1']", false, "ll"},
		{nil, "/m:ct", false, "ct"}, // choice and case are not path steps
		{nil, "/m:r/i", false, "i"},
		{nil, "/m:r/out", true, "out"},
		{n["c"], "l/v", false, "v"}, // relative to the context node
		{nil, "l/v", false, "No initial schema parent for a relative path."},
		{nil, "/c/a", false, "Prefix missing for \"c\" in path."},
		{nil, "/m:c/nope", false, "Not found node \"nope\" in path."},
		{nil, "/m:r/out", false, "Not found node \"out\" in path."},
		{nil, "/zz:c", false, "No module connected with the prefix \"zz\" found (prefix format JSON module names)."},
		{nil, "/m:c/a[k='x']", false, "List predicate defined for leaf \"a\" in path."},
	} {
		got, msg := FindPath(set, tc.ctx, tc.path, tc.output)
		switch {
		case got != nil && got.Name != tc.want, got == nil && msg != tc.want:
			t.Errorf("%q: got %v %q, want %q", tc.path, got, msg, tc.want)
		}
	}
}
