// SPDX-License-Identifier: BSD-3-Clause

package compile_test

import (
	"testing"
)

// Width follows the final enabled declaration set, including restricted typedefs;
// publishing a snapshot copies the prepared scalar rather than rescanning items.
func TestEnumLYBWidthSnapshot(t *testing.T) {
	c := snapHarness(t, map[string]string{"m.yang": `module m {
  yang-version 1.1; namespace urn:m; prefix m; feature wide;
  typedef value { type enumeration {
    enum zero { value 0; } enum small { value 1; }
    enum high { value 256; if-feature wide; }
  } }
  leaf a { type value; }
  leaf b { type value { enum zero; } }
}`})
	if _, _, err := c.Load("m", "", nil); err != nil {
		t.Fatal(err)
	}
	set := c.Snapshot()
	m := set.Module("m", "")
	for _, sn := range m.Top {
		want := uint8(2) // one value bit, encoded as width+1
		if sn.Name == "b" {
			want = 1 // enum zero has a zero-byte LYB payload
		}
		if sn.Type.EnumLYBWidth != want {
			t.Errorf("%s prepared width=%d, want %d", sn.Name, sn.Type.EnumLYBWidth, want)
		}
	}
}
