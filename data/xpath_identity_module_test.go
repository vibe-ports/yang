// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
)

func validateXPathIdentityModuleFixture(t *testing.T, modules []string, input string) ([]yang.Diagnostic, error) {
	t.Helper()

	dir := filepath.Join("..", "conformance", "corpus", "xpath-cur-mod", "schemas")
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range modules {
		if diags, err := c.Load(name, "", nil); err != nil {
			t.Fatalf("load %s: %v (%+v)", name, err, diags)
		}
	}

	tree, diags, err := Parse(context.Background(), strings.NewReader(input), FormatJSON, c.Schema(),
		ParseOptions{ParseOnly: true, NoState: true})
	if err != nil {
		t.Fatalf("parse: %v (%+v)", err, diags)
	}
	return tree.Validate(context.Background(), ValidateOptions{NoState: true, MultiError: true})
}

// TestXPathUnprefixedIdentityModule pins the libyang v5.8.6 distinction between
// derived-from(-or-self), which resolves an unprefixed identity in the instantiated
// schema node's module, and an identityref equality literal, which uses the module
// stored with the grouping's expression prefix data.
func TestXPathUnprefixedIdentityModule(t *testing.T) {
	tests := []struct {
		name    string
		modules []string
		input   string
		valid   bool
		appTag  string
	}{
		{
			name:    "derived-from-or-self/current-module-accepts",
			modules: []string{"xcm-base", "xcm-cur-g", "xcm-cur-m"},
			input:   `{"xcm-cur-m:current":{"id":"xcm-cur-m:local"}}`,
			valid:   true,
		},
		{
			name:    "derived-from/both-main-module-accepts",
			modules: []string{"xcm-base", "xcm-both-g", "xcm-both-m"},
			input:   `{"xcm-both-m:both":{"id":"xcm-both-m:m-child"}}`,
			valid:   true,
		},
		{
			name:    "derived-from/both-grouping-module-rejects",
			modules: []string{"xcm-base", "xcm-both-g", "xcm-both-m"},
			input:   `{"xcm-both-m:both":{"id":"xcm-both-g:g-child"}}`,
			valid:   false,
			appTag:  "xcm-both-module",
		},
		{
			name:    "identity-literal/grouping-module-accepts",
			modules: []string{"xcm-base", "xcm-literal-g", "xcm-literal-m"},
			input:   `{"xcm-literal-m:literal":{"id":"xcm-literal-g:local"}}`,
			valid:   true,
		},
		{
			name:    "identity-literal/main-module-rejects",
			modules: []string{"xcm-base", "xcm-literal-g", "xcm-literal-m"},
			input:   `{"xcm-literal-m:literal":{"id":"xcm-literal-m:local"}}`,
			valid:   false,
			appTag:  "xcm-literal-module",
		},
		{
			name:    "when-derived-from-or-self/current-module-accepts",
			modules: []string{"xcm-base", "xcm-when-g", "xcm-when-m"},
			input:   `{"xcm-when-m:guarded-case":{"id":"xcm-when-m:local","guarded":"present"}}`,
			valid:   true,
		},
		{
			name:    "node-step/current-module-accepts",
			modules: []string{"xcm-node-g", "xcm-node-m"},
			input:   `{"xcm-node-m:node-step":{"sibling":"ok","probe":"present"}}`,
			valid:   true,
		},
		{
			name:    "augment-derived-from-or-self/augmenting-module-accepts",
			modules: []string{"xcm-base", "xcm-augment-m", "xcm-augment-a"},
			input:   `{"xcm-augment-m:target":{"xcm-augment-a:id":"xcm-augment-a:x"}}`,
			valid:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diags, err := validateXPathIdentityModuleFixture(t, tc.modules, tc.input)
			if tc.valid {
				if err != nil {
					t.Fatalf("validation rejected data: %v (%+v)", err, diags)
				}
				return
			}

			var validationErr *ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("validation error = %v, want *ValidationError (%+v)", err, diags)
			}
			if len(diags) != 1 || diags[0].AppTag != tc.appTag {
				t.Fatalf("diagnostics = %+v, want one diagnostic with app-tag %q", diags, tc.appTag)
			}
		})
	}
}
