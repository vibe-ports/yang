// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

// utDiffSet compiles the ut-diff module defaults (tests/utests/data/test_diff.c).
func utDiffSet(t *testing.T) *schema.Set {
	t.Helper()
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true},
		os.DirFS("../conformance/corpus/ut-diff/schemas"))
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load("defaults", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	return set
}

// parseOnlyXML parses XML data parse-only (unknown nodes opaque) over set.
func parseOnlyXML(t *testing.T, set *schema.Set, in string) *Tree {
	t.Helper()
	tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true, Unknown: Opaque}}, parseXML, nil)
	if err != nil {
		t.Fatal(err, diags)
	}
	return tr
}

// TestDiffKeyRefused: a list key as an argument is LY_EINVAL (libyang asserts it never is).
func TestDiffKeyRefused(t *testing.T) {
	set := utDiffSet(t)
	tr := parseOnlyXML(t, set, `<df xmlns="urn:libyang:tests:defaults"><list><name>a</name></list></df>`)
	l, err := tr.Find("/defaults:df/list[name='a']/name")
	if err != nil || l == nil {
		t.Fatal(l, err)
	}
	for _, f := range []func(a, b *Node, o DiffOptions) (*Tree, error){DiffSiblings, DiffTree} {
		_, err := f(l, nil, DiffOptions{})
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
			t.Errorf("key argument: %v", err)
		}
	}
}
