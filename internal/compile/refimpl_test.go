// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRefImplemented: with LY_CTX_REF_IMPLEMENTED the module a default, must or when references
// becomes implemented and compiled; without it the module stays import-only.
func TestRefImplemented(t *testing.T) {
	for _, mod := range []string{"ref-impl-ident", "ref-impl-instid", "ref-impl-must"} {
		for _, on := range []bool{true, false} {
			c, _, err := NewContext(Options{RefImplemented: on}, os.DirFS(filepath.Join(corpus, "compile", "schemas")))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = c.Load(mod, "", nil)
			if on && err != nil {
				t.Fatalf("%s: %v", mod, err)
			}
			tg := c.latest("ref-impl-target")
			if !on && err != nil && tg == nil {
				continue // the failed load removed the import too
			}
			if tg == nil || tg.Implemented != on || on && !tg.compiled {
				t.Errorf("%s, ref_implemented %v: target implemented %v", mod, on, tg != nil && tg.Implemented)
			}
		}
	}
}
