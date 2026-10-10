// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// TestValidateAllDropsOrphanAugmentDefault covers a libyang v5.8.6 crash: with the base module
// loaded before its augment, after the explicit data of a case is removed, lyd_validate_all over
// a case default added by the augment crashes (SIGSEGV, oracle exit 139; no golden). The port
// removes the orphan default.
func TestValidateAllDropsOrphanAugmentDefault(t *testing.T) {
	mods := fstest.MapFS{
		"base.yang": {Data: []byte(`module base {
  yang-version 1.1;
  namespace "urn:base";
  prefix b;
  choice ch {
    case selected {
      leaf explicit { type string; }
    }
  }
}`)},
		"augment.yang": {Data: []byte(`module augment {
  yang-version 1.1;
  namespace "urn:augment";
  prefix a;
  import base { prefix b; }
  augment "/b:ch/b:selected" {
    leaf implicit { type string; default "x"; }
  }
}`)},
	}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, mods)
	if err != nil {
		t.Fatal(err)
	}
	if diags, err := c.Load("base", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	if diags, err := c.Load("augment", "", nil); err != nil {
		t.Fatal(err, diags)
	}

	tr, diags, err := data.Parse(context.Background(), strings.NewReader(
		`{"base:explicit":"present"}`), data.FormatJSON, c.Schema(), data.ParseOptions{})
	if err != nil {
		t.Fatal(err, diags)
	}
	findTop := func(name string) *data.Node {
		for n := range tr.Top() {
			if n.Name() == name {
				return n
			}
		}
		return nil
	}
	implicit := findTop("implicit")
	if implicit == nil || implicit.Flags()&data.FlagDefault == 0 {
		t.Fatalf("augmented default before removal: %v", implicit)
	}
	explicit, err := tr.Find("/base:explicit")
	if err != nil || explicit == nil {
		t.Fatalf("explicit case data: %v, %v", explicit, err)
	}
	if err := explicit.Remove(); err != nil {
		t.Fatal(err)
	}
	if implicit := findTop("implicit"); implicit == nil || implicit.Flags()&data.FlagDefault == 0 {
		t.Fatalf("augmented default after explicit data removal: %v", implicit)
	}

	diags, err = tr.Validate(context.Background(), data.ValidateOptions{})
	if err != nil || len(diags) != 0 {
		t.Fatalf("validate all: %v, %v", err, diags)
	}
	if implicit := findTop("implicit"); implicit != nil {
		t.Fatalf("orphan augmented default after validation: %v", implicit)
	}
}
