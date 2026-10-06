// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestEmptyNodeid: an empty or blank augment, refine or uses-augment node-id is an error with
// libyang's diagnostics, the parser's "Empty argument" warning included, never a panic (issue
// #124).
func TestEmptyNodeid(t *testing.T) {
	dir := filepath.Join(corpus, "compile")
	for _, name := range []string{"aug-empty-nodeid", "aug-blank-nodeid", "refine-empty-nodeid", "uses-augment-empty-nodeid"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(dir, "golden", name+".json")) //nolint:gosec // corpus path
			if err != nil {
				t.Fatal(err)
			}
			var g nodeGolden
			if err := json.Unmarshal(b, &g); err != nil {
				t.Fatal(err)
			}
			c, _, err := NewContext(Options{}, os.DirFS(filepath.Join(dir, "schemas")))
			if err != nil {
				t.Fatal(err)
			}
			_, diags, err := c.Load(name, "", nil)
			if err == nil {
				t.Fatal("accepted")
			}
			var got, want []goldenDiag
			for _, d := range diags {
				got = append(got, d.golden())
			}
			want = append(want, g.Modules[0].Diagnostics...)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got  %+v\nwant %+v", got, want)
			}
		})
	}
}
