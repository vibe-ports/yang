// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"errors"
	"testing"
)

func TestFindXPathAtomsDiagnostics(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "m.yang", `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  container c;
  leaf ref { type instance-identifier { require-instance false; } }
  leaf target { type string; }
}`)
	c := newCtx(t, Options{}, dir)
	if _, diags, err := c.Load("m", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := c.Snapshot()
	m := set.Implemented("m")

	if _, diags, err := FindXPathAtoms(set, nil, "/m:ref = '/m:target'", AtomOptions{}); err != nil || len(diags) != 0 {
		t.Fatalf("JSON instance-identifier literal: err %v, diagnostics %+v", err, diags)
	}

	ctx := m.Top[0]
	if _, diags, err := FindXPathAtoms(set, ctx, "unknown:x", AtomOptions{}); err == nil || len(diags) != 1 || diags[0].SchemaPath != "/m:c" {
		t.Fatalf("context error: err %v, diagnostics %+v", err, diags)
	}

	_, diags, err := FindXPathAtoms(set, nil, "/m:missing + /m:c", AtomOptions{NoMatchError: true})
	var d *Diagnostic
	if !errors.As(err, &d) || d.Level != LevelError || d.Err != "LY_ENOTFOUND" {
		t.Fatalf("no-match error: %T %v, diagnostics %+v", err, err, diags)
	}
	if len(diags) < 2 || diags[len(diags)-1].Level != LevelWarning {
		t.Fatalf("expected a warning after the no-match error, got %+v", diags)
	}
}
