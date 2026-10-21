// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"path/filepath"
	"testing"
)

// TestXMLAnyGoldens keeps the XML parser on the existing oracle cases:
// verdicts, diagnostics, printed trees and typed any values are compared in full.
func TestXMLAnyGoldens(t *testing.T) {
	m, err := LoadManifest("corpus/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{
		"ut-parser/xml-anydata-01",
		"ut-parser/xml-anyxml-01", "ut-parser/xml-anyxml-02", "ut-parser/xml-anyxml-03",
		"ut-parser/xml-anydata-strict-validation-01", "ut-parser/xml-anydata-strict-validation-02",
		"ut-parser/xml-anydata-strict-validation-03",
		"protocol-v2/anydata-xml-ns-a", "protocol-v2/anydata-xml-ns-b",
		"ut-parser/xml-any-inherited-shadowed-ns", "ut-parser/xml-any-mixed-content",
		"ut-parser/xml-any-escaped-markup", "ut-parser/xml-any-nested-anydata",
		"ut-parser/xml-any-multiple-siblings",
		"ut-parser/xml-anydata-strict-undeclared-prefix", "ut-parser/xml-anydata-strict-no-default-ns",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			var f Fixture
			for _, candidate := range m.Fixtures {
				if candidate.ID == id {
					f = candidate
					break
				}
			}
			if f.ID == "" {
				t.Fatal("fixture missing")
			}
			golden, err := LoadGolden(m.GoldenPath(f))
			if err != nil {
				t.Fatal(err)
			}
			got, err := (Yang{}).Run(Request{ID: f.ID, BaseDir: filepath.Join("corpus", f.Dir), Params: f.Request})
			if err != nil {
				t.Fatal(err)
			}
			fields := Yang{}.SkippedFields("data")
			golden, _ = withoutSkipped(golden, fields)
			got, _ = withoutSkipped(got, fields)
			if status, detail := classify(f, golden, got); status != Agree {
				t.Fatalf("%s: %s", status, detail)
			}
		})
	}
}
