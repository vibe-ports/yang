// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
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
	if n, findErr := tr.Find("s"); findErr != nil || n == nil || n.Value() != "x" {
		t.Errorf("Find relative: %v, %v", n, findErr)
	}

	c, _ := tr.Find("/fz:c")
	if err := c.Remove(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := c.PrintJSON(&out, data.PrintOptions{Shrink: true}); err != nil || out.String() != `{"fz:c":{"s":"x"}}` {
		t.Errorf("print a detached node: %q, %v", out.String(), err)
	}
}

// TestNilNodeAPI exercises the zero-node side of every exported Node receiver family. Libyang's
// NULL-aware accessors return their zero result, its free/unlink calls succeed, and its printers
// emit an empty document; observers represented by direct C field access are total in the Go API.
func TestNilNodeAPI(t *testing.T) {
	var n *data.Node
	if n.Parent() != nil || n.Flags() != 0 || n.Name() != "" || n.Path() != "" || n.Value() != "" || n.Schema() != nil {
		t.Error("nil node scalar observer did not return its zero value")
	}
	if len(slices.Collect(n.Children())) != 0 || len(slices.Collect(n.ChildrenNoKeys())) != 0 || len(slices.Collect(n.All())) != 0 ||
		len(slices.Collect(n.Meta())) != 0 {
		t.Error("nil node iterator was not empty")
	}
	if n.ValueType() != nil {
		t.Error("nil ValueType was not nil")
	}
	if _, ok := n.UnionMember(); ok {
		t.Error("nil UnionMember reported a member")
	}
	if _, ok := n.OpaqueInfo(); ok || n.Attrs() != nil {
		t.Error("nil opaque observer was not empty")
	}
	if leafrefs, targets := n.LeafrefLinks(); leafrefs != nil || targets != nil {
		t.Error("nil LeafrefLinks was not empty")
	}
	if !n.Equal(nil, data.CompareOptions{}) {
		t.Error("two nil nodes were not equal")
	}
	if err := n.Remove(); err != nil {
		t.Errorf("nil Remove: %v", err)
	}
	if err := n.Unlink(); err != nil {
		t.Errorf("nil Unlink: %v", err)
	}
	if m, err := n.FindMeta("fz:ann"); m != nil || err != nil {
		t.Errorf("nil FindMeta: %v, %v", m, err)
	}
	if _, err := n.SetValue("x"); err == nil {
		t.Error("nil SetValue succeeded")
	}
	var jsonOut, xmlOut bytes.Buffer
	if err := n.PrintJSON(&jsonOut, data.PrintOptions{Shrink: true}); err != nil || jsonOut.String() != `{}` {
		t.Errorf("nil PrintJSON: %q, %v", jsonOut.String(), err)
	}
	if err := n.PrintXML(&xmlOut, data.PrintOptions{Shrink: true}); err != nil || xmlOut.String() != "" {
		t.Errorf("nil PrintXML: %q, %v", xmlOut.String(), err)
	}
}
