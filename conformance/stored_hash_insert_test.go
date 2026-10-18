// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Exercise the requests before oracle goldens are generated. Expectations follow
// lyd_insert_node and lyd_insert_get_next_anchor; compacting JSON preserves sibling
// order and duplicate members, so a split system-ordered run cannot pass unnoticed.
func TestStoredHashCrossSchemaInsert(t *testing.T) {
	wants := map[string]map[int]string{
		"leaf-list":       {3: `{"m:r":{"x":[1,2],"y":"y"}}`},
		"leaf-list-table": {3: `{"m:r":{"x":[2,3,4],"z":"z","y":"y"}}`},
		"list":            {3: `{"m:r":{"l":[{"k":1},{"k":2}],"y":"y"}}`},
		"anchor":          {3: `{"m:r":{"between":"b","x":[2],"z":"z","y":"y"}}`},
		"anchor-table":    {3: `{"m:r":{"x":[2,4],"between":"b","z":"z","y":"y"}}`},
		"user-list":       {3: `{"m:r":{"u":[2,1],"y":"y"}}`},
		"user-list-table": {
			3:  `{"m:r":{"u":[2,4],"z":"z","y":"y","u":[1]}}`,
			6:  `{"m:r":{"u":[4],"z":"z","y":"y","l":[{"k":1}],"u":[1]}}`,
			10: `{"m:r":{"l":[{"k":2}],"u":[4],"z":"z","y":"y","u":[3]}}`,
			14: `{"m:r":{"l":[{"k":3}],"u":[3]}}`,
		},
	}
	m, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, f := range m.Fixtures {
		suffix, ok := strings.CutPrefix(f.ID, "protocol-v2/hash-cross-schema-insert-")
		if !ok {
			continue
		}
		matched++
		t.Run(suffix, func(t *testing.T) {
			want, ok := wants[suffix]
			if !ok {
				t.Fatal("fixture has no source-backed expectation")
			}
			resp, err := (Yang{}).Run(Request{ID: f.ID, BaseDir: fixtureDir(m, f), Params: f.Request})
			if err != nil {
				t.Fatal(err)
			}
			if resp.Verdict() != "valid" {
				t.Fatalf("request failed: %v", resp)
			}
			steps := resp["steps"].([]any)
			for i, expected := range want {
				printed := steps[i].(map[string]any)["tree"].(map[string]any)["json"].(string)
				var compact bytes.Buffer
				if err := json.Compact(&compact, []byte(printed)); err != nil {
					t.Fatal(err)
				}
				if compact.String() != expected {
					t.Errorf("step %d JSON = %s, want %s", i, compact.String(), expected)
				}
			}
		})
	}
	if matched != len(wants) {
		t.Fatalf("found %d fixtures, want %d", matched, len(wants))
	}
}
