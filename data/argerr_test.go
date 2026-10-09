// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/data"
)

// wantArgErr checks that the error of a call is libyang's LOGARG: an LY_EINVAL *ValidationError
// whose one diagnostic is msg (data/doc.go: the shape of every invalid argument).
func wantArgErr(t *testing.T, what, msg string, err error) {
	t.Helper()
	var ve *data.ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 || ve.Diags[0].Msg != msg ||
		ve.Diags[0].Err != "LY_EINVAL" {
		t.Errorf("%s: %#v, want an LY_EINVAL *ValidationError %q", what, err, msg)
	}
}

// TestArgErrors: the invalid arguments of the exported API outside the Meta API (TestMetaPublic)
// and the diff (TestMergeDiffTree).
func TestArgErrors(t *testing.T) {
	s := fzContext(t).Schema()
	_, diags, err := data.Parse(context.Background(), strings.NewReader(`{}`), data.FormatJSON, nil, data.ParseOptions{})
	wantArgErr(t, "Parse without a schema", "Invalid argument ctx || parent (lyd_parse_data()).", err)
	if len(diags) != 1 {
		t.Errorf("Parse without a schema: diagnostics %v", diags)
	}
	_, diags, err = data.Parse(context.Background(), strings.NewReader(`{}`), data.Format(99), s, data.ParseOptions{})
	wantArgErr(t, "Parse of an unknown format", "Invalid argument format (lyd_parse()).", err)
	if len(diags) != 1 {
		t.Errorf("Parse of an unknown format: diagnostics %v", diags)
	}

	tr, diags, err := parse(s, `{"fz:c": {"s": "x"}}`, data.FormatJSON, data.ParseOptions{ParseOnly: true})
	if err != nil {
		t.Fatal(err, diags)
	}
	_, err = tr.NewPath("fz:c/s", "y", data.NewPathOptions{})
	wantArgErr(t, "NewPath relative", "Invalid argument (path[0] == '/') || parent (lyd_new_path()).", err)
	_, err = tr.Find("fz:c")
	wantArgErr(t, "Find relative", "Invalid argument path (not absolute) (lyd_find_path()).", err)

	c, _ := tr.Find("/fz:c")
	if err := c.Remove(); err != nil {
		t.Fatal(err)
	}
	wantArgErr(t, "print a detached node", "Invalid argument node (not in a tree) (lyd_print_tree()).",
		c.PrintJSON(&bytes.Buffer{}, data.PrintOptions{}))
}
