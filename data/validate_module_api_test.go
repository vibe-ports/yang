// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

var validateModuleFS = fstest.MapFS{
	"vm-a.yang": {Data: []byte(`module vm-a {
  yang-version 1.1;
  namespace "urn:vm-a";
  prefix a;
  container top {
    leaf x { type string; }
    leaf dflt { type string; default "a-default"; }
  }
}`)},
	"vm-b.yang": {Data: []byte(`module vm-b {
  yang-version 1.1;
  namespace "urn:vm-b";
  prefix b;
  container top {
    must "false()";
    leaf dflt { type string; default "b-default"; }
  }
}`)},
	"vm-host.yang": {Data: []byte(`module vm-host {
  yang-version 1.1;
  namespace "urn:vm-host";
  prefix h;
  import vm-import { prefix i; }
}`)},
	"vm-import.yang": {Data: []byte(`module vm-import {
  yang-version 1.1;
  namespace "urn:vm-import";
  prefix i;
  leaf value { type string; }
}`)},
}

func validateModuleTree(t *testing.T) *data.Tree {
	t.Helper()
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, validateModuleFS)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"vm-a", "vm-b", "vm-host"} {
		if diags, err := c.Load(name, "", nil); err != nil {
			t.Fatalf("Load(%q): %v %v", name, err, diags)
		}
	}
	tr, diags, err := parse(c.Schema(), `{"vm-a:top":{"x":"set"},"vm-b:top":{}}`, data.FormatJSON,
		data.ParseOptions{ParseOnly: true})
	if err != nil {
		t.Fatal(err, diags)
	}
	return tr
}

func TestValidateModuleScope(t *testing.T) {
	tr := validateModuleTree(t)
	diags, err := tr.ValidateModule(context.Background(), "vm-a", data.ValidateOptions{})
	if err != nil || len(diags) != 0 {
		t.Fatalf("ValidateModule(vm-a) = %v, %v", diags, err)
	}
	if n, err := tr.Find("/vm-b:top/dflt"); err != nil || n != nil {
		t.Fatalf("other module default = %v, %v; want absent", n, err)
	}
}

func TestValidateModuleImplicitNodes(t *testing.T) {
	tr := validateModuleTree(t)
	if _, err := tr.ValidateModule(context.Background(), "vm-a", data.ValidateOptions{}); err != nil {
		t.Fatal(err)
	}
	n, err := tr.Find("/vm-a:top/dflt")
	if err != nil || n == nil || n.Value() != "a-default" || n.Flags()&data.FlagDefault == 0 {
		t.Fatalf("implicit default = %v, %v", n, err)
	}
}

func TestValidateModulePresentRefused(t *testing.T) {
	tr := validateModuleTree(t)
	diags, err := tr.ValidateModule(context.Background(), "vm-a", data.ValidateOptions{Present: true})
	wantSilentArgError(t, diags, err,
		"Invalid argument !(val_opts & LYD_VALIDATE_PRESENT) (lyd_validate_module()).")
}

func TestValidateModuleUnknownRefused(t *testing.T) {
	tr := validateModuleTree(t)
	diags, err := tr.ValidateModule(context.Background(), "missing", data.ValidateOptions{})
	wantSilentArgError(t, diags, err, "Invalid argument module (lyd_validate_module()).")
}

func TestValidateModuleImportOnlyRefused(t *testing.T) {
	tr := validateModuleTree(t)
	diags, err := tr.ValidateModule(context.Background(), "vm-import", data.ValidateOptions{})
	wantSilentArgError(t, diags, err, "Invalid argument module (lyd_validate_module()).")
}

func TestValidateModuleFinalArguments(t *testing.T) {
	for _, tc := range []struct {
		name, module, message string
		opts                  data.ValidateOptions
	}{
		{"present", "vm-a", "!(val_opts & (LYD_VALIDATE_PRESENT | LYD_VALIDATE_NOT_FINAL))", data.ValidateOptions{Present: true}},
		{"not final", "vm-a", "!(val_opts & (LYD_VALIDATE_PRESENT | LYD_VALIDATE_NOT_FINAL))", data.ValidateOptions{NotFinal: true}},
		{"both", "vm-a", "!(val_opts & (LYD_VALIDATE_PRESENT | LYD_VALIDATE_NOT_FINAL))", data.ValidateOptions{Present: true, NotFinal: true}},
		{"unknown", "missing", "module", data.ValidateOptions{}},
		{"import only", "vm-import", "module", data.ValidateOptions{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := validateModuleTree(t)
			ds, err := tr.ValidateModuleFinal(context.Background(), tc.module, tc.opts)
			wantSilentArgError(t, ds, err, "Invalid argument "+tc.message+" (lyd_validate_module_final()).")
		})
	}
}

func TestValidateModuleFinalScopeAndNoImplicit(t *testing.T) {
	tr := validateModuleTree(t)
	if ds, err := tr.ValidateModuleFinal(context.Background(), "vm-a", data.ValidateOptions{}); err != nil || len(ds) != 0 {
		t.Fatal(ds, err)
	}
	for _, path := range []string{"/vm-a:top/dflt", "/vm-b:top/dflt"} {
		if n, err := tr.Find(path); err != nil || n != nil {
			t.Fatalf("final-only created %s: %v %v", path, n, err)
		}
	}
	if ds, err := tr.ValidateModuleFinal(context.Background(), "vm-b", data.ValidateOptions{}); err == nil || len(ds) != 1 {
		t.Fatalf("other module not finalized: %v %v", ds, err)
	}
}

func TestValidateModuleDiffArgumentChecks(t *testing.T) {
	for _, tc := range []struct {
		name, module, message string
		opts                  data.ValidateOptions
	}{
		{"present", "vm-a", "Invalid argument !(val_opts & LYD_VALIDATE_PRESENT) (lyd_validate_module()).",
			data.ValidateOptions{Present: true}},
		{"unknown", "missing", "Invalid argument module (lyd_validate_module()).", data.ValidateOptions{}},
		{"import only", "vm-import", "Invalid argument module (lyd_validate_module()).", data.ValidateOptions{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diff, diags, err := validateModuleTree(t).ValidateModuleDiff(context.Background(), tc.module, tc.opts)
			if diff != nil {
				t.Errorf("diff = %v, want nil", diff)
			}
			wantSilentArgError(t, diags, err, tc.message)
		})
	}
}

func TestValidateModuleDiffNoChange(t *testing.T) {
	tr := validateModuleTree(t)
	if diff, diags, err := tr.ValidateModuleDiff(context.Background(), "vm-a", data.ValidateOptions{}); err != nil || len(diags) != 0 || diff == nil {
		t.Fatalf("first ValidateModuleDiff(vm-a) = diff %v, diagnostics %v, error %v", diff, diags, err)
	}
	diff, diags, err := tr.ValidateModuleDiff(context.Background(), "vm-a", data.ValidateOptions{})
	if err != nil || len(diags) != 0 || diff != nil {
		t.Fatalf("second ValidateModuleDiff(vm-a) = diff %v, diagnostics %v, error %v; want nil, nil, nil",
			diff, diags, err)
	}
}

func TestValidateModuleDiffPartialOnError(t *testing.T) {
	tr := validateModuleTree(t)
	diff, diags, err := tr.ValidateModuleDiff(context.Background(), "vm-b", data.ValidateOptions{})
	var ve *data.ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EVALID" || len(diags) == 0 {
		t.Fatalf("ValidateModuleDiff(vm-b) = diff %v, diagnostics %v, error %v; want validation error", diff, diags, err)
	}
	if diff == nil {
		t.Fatal("ValidateModuleDiff(vm-b) diff = nil; want partial implicit diff")
	}
	const path = "/vm-b:top/dflt"
	if n, findErr := diff.Find(path); findErr != nil || n == nil {
		t.Errorf("partial diff %s = %v, %v; want created default", path, n, findErr)
	}
	if n, findErr := tr.Find(path); findErr != nil || n == nil || n.Flags()&data.FlagDefault == 0 {
		t.Errorf("mutated tree %s = %v, %v; want implicit default", path, n, findErr)
	}
}

func wantSilentArgError(t *testing.T, diags []yang.Diagnostic, err error, msg string) {
	t.Helper()
	var ve *data.ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || err.Error() != msg {
		t.Fatalf("error = %v, RC = %q; want silent LY_EINVAL %q", err, rc(ve), msg)
	}
	if len(diags) != 0 || len(ve.Diags) != 0 {
		t.Fatalf("diagnostics = %#v, error diagnostics = %#v; want none", diags, ve.Diags)
	}
}

func rc(err *data.ValidationError) string {
	if err == nil {
		return ""
	}
	return err.RC()
}
