// SPDX-License-Identifier: BSD-3-Clause

package parser

import (
	"slices"
	"testing"
)

// TestEmptyArgWarning: CHECK_NONEMPTY warns about an empty argument of must, length, range,
// when, refine, augment and deviation (not of other statements, nor inside an extension
// instance).
func TestEmptyArgWarning(t *testing.T) {
	var warns []string
	ctx := &Context{Warn: func(msg string) { warns = append(warns, msg) }}
	src := `module e { yang-version 1.1; namespace urn:e; prefix e;
  extension x { argument a; }
  grouping g { container c; }
  container k { must ""; when ""; description ""; e:x "" { must ""; } }
  leaf s { type string { length ""; } }
  leaf i { type int8 { range ""; } }
  uses g { refine "" { description d; } augment "" { leaf z { type string; } } } }`
	_, _ = ParseIn(ctx, "e.yang", []byte(src), nil) // the empty node-ids are errors later
	var want []string
	for _, kw := range []string{"must", "when", "length", "range", "refine", "augment"} {
		want = append(want, "Empty argument of "+kw+" statement does not make sense.")
	}
	if !slices.Equal(warns, want) {
		t.Errorf("warnings %q\nwant %q", warns, want)
	}
}
