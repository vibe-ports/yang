// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// TestTrimXPath: lyd_trim_xpath over the public API (the oracle fixtures seq/trim-* cover the
// trims of test_xpath.c): variables, the refused context node, and an XPath error, which leaves
// the tree as it was.
func TestTrimXPath(t *testing.T) {
	mods := fstest.MapFS{"q.yang": {Data: []byte(`module q { yang-version 1.1; namespace "urn:q"; prefix q;
  container c { list l { key k; leaf k { type string; } leaf v { type string; } } leaf x { type string; } }
  leaf top { type string; }
}`)}}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, mods)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := c.Load("q", "", nil); err != nil {
		t.Fatal(d, err)
	}
	parse := func() *data.Tree {
		tr, d, err := data.Parse(context.Background(), strings.NewReader(
			`{"q:c":{"l":[{"k":"a","v":"1"},{"k":"b","v":"2"}],"x":"y"},"q:top":"t"}`), data.FormatJSON, c.Schema(), data.ParseOptions{})
		if err != nil {
			t.Fatal(err, d)
		}
		return tr
	}
	dump := func(tr *data.Tree) string {
		var b bytes.Buffer
		if err := tr.PrintJSON(&b, data.PrintOptions{Shrink: true}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	tr := parse()
	if _, err := tr.TrimXPath("/q:c/l[k = $key]/v", data.XPathOptions{Vars: []data.XPathVar{{Name: "key", Value: "'b'"}}}); err != nil {
		t.Fatal(err)
	}
	if got := dump(tr); got != `{"q:c":{"l":[{"k":"b","v":"2"}]}}` {
		t.Errorf("trim with a variable: %s", got)
	}

	tr = parse()
	want := dump(tr)
	var first *data.Node
	for n := range tr.Top() {
		first = n
		break
	}
	_, err = tr.TrimXPath("/q:c", data.XPathOptions{Node: first})
	var ve *data.ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || dump(tr) != want {
		t.Errorf("context node: %v", err)
	}
	d, err := tr.TrimXPath("/zz:c", data.XPathOptions{})
	if !errors.As(err, &ve) || ve.RC() != "LY_EVALID" || len(d) != 1 || dump(tr) != want {
		t.Errorf("XPath error: %v %v, tree %s", err, d, dump(tr))
	}
}
