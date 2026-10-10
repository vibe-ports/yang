// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// TestMustMessageCannotSuppressValidation ensures schema-controlled diagnostic text cannot be
// mistaken for the nonfatal LY_EINT that the XPath evaluator itself logs.
func TestMustMessageCannotSuppressValidation(t *testing.T) {
	models := fstest.MapFS{"m.yang": {Data: []byte(`module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  container c {
    must "false()" {
      error-message "Internal error (xpath.c:1348).";
    }
  }
}`)}}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, models)
	if err != nil {
		t.Fatal(err)
	}
	if diags, err := c.Load("m", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	tr, diags, err := data.Parse(context.Background(), strings.NewReader(`{"m:c": {}}`),
		data.FormatJSON, c.Schema(), data.ParseOptions{ParseOnly: true})
	if err != nil {
		t.Fatal(err, diags)
	}
	diags, err = tr.Validate(context.Background(), data.ValidateOptions{})
	var ve *data.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Validate error = %v, want *ValidationError", err)
	}
	if ve.RC() != "LY_EVALID" || err.Error() != "Internal error (xpath.c:1348)." {
		t.Fatalf("Validate error = %v, RC = %q", err, ve.RC())
	}
	if len(diags) != 1 || diags[0].Err != "LY_EVALID" || diags[0].Code != "LYVE_DATA" ||
		diags[0].DataPath != "/m:c" || diags[0].AppTag != "must-violation" ||
		diags[0].Msg != "Internal error (xpath.c:1348)." {
		t.Fatalf("Validate diagnostics = %+v", diags)
	}
}

// TestXPathInternalDiagnosticRemainsNonfatal checks the other half of the classification: an
// LY_EINT emitted by the XPath evaluator's internal path is still returned as a diagnostic while
// successful validation remains successful.
func TestXPathInternalDiagnosticRemainsNonfatal(t *testing.T) {
	models := fstest.MapFS{"m.yang": {Data: []byte(`module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  container c {
    leaf x {
      type string;
      must "count(@*) >= 0";
    }
  }
}`)}}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, models)
	if err != nil {
		t.Fatal(err)
	}
	if diags, err := c.Load("m", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	in := `<c xmlns="urn:m" xmlns:yang="urn:ietf:params:xml:ns:yang:1"><x yang:operation="none" yang:value="k">v</x></c>`
	tr, diags, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatXML,
		c.Schema(), data.ParseOptions{})
	if err != nil || tr == nil {
		t.Fatalf("Parse error = %v, diagnostics = %+v", err, diags)
	}
	if len(diags) != 1 || diags[0].Err != "LY_EINT" || diags[0].Code != "LYVE_SUCCESS" ||
		diags[0].Msg != "Internal error (xpath.c:1348)." {
		t.Fatalf("Parse diagnostics = %+v", diags)
	}
}
