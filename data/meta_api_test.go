// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

func metaList(n *data.Node) string {
	var s []string
	for m := range n.Meta() {
		s = append(s, fmt.Sprintf("%s:%s=%s", m.Module().Name(), m.Name(), m.Value()))
	}
	return strings.Join(s, " ")
}

// TestMetaPublic: Node.Meta, Node.FindMeta, Tree.NewMeta and Meta.Remove over parsed metadata
// and the yang:* metadata of libyang's diff format; the libyang behaviour is pinned by the
// protocol-v2/meta-api-* oracle fixtures (sequence edits new_meta / free_meta).
func TestMetaPublic(t *testing.T) {
	s := fzContext(t).Schema()
	tr, diags, err := parse(s, `{"fz:c": {"s": "x", "@s": {"fz:ann": "a"}, "ll": [1]}}`, data.FormatJSON,
		data.ParseOptions{ParseOnly: true})
	if err != nil {
		t.Fatal(err, diags)
	}
	n, _ := tr.Find("/fz:c/s")
	if got := metaList(n); got != "fz:ann=a" {
		t.Fatalf("parsed: %s", got)
	}
	for _, e := range [][2]string{
		{"yang:operation", "create"}, {"yang:key", "[k='a']"}, {"yang:position", "2"}, {"yang:orig-default", "true"},
		{"yang:value", ""}, {"fz:ref", "/fz:c/s"}, {"fz:ann", "b"},
	} {
		if m, err := tr.NewMeta(n, e[0], e[1]); err != nil || m.Value() != e[1] {
			t.Fatalf("new %s: %v", e[0], err)
		}
	}
	want := `fz:ann=a yang:operation=create yang:key=[k='a'] yang:position=2 yang:orig-default=true yang:value= ` +
		`fz:ref=/fz:c/s fz:ann=b`
	if got := metaList(n); got != want {
		t.Fatalf("after NewMeta:\n%s\nwant\n%s", got, want)
	}
	if got := printJSON(t, tr); !strings.Contains(got, `"yang:operation": "create"`) {
		t.Fatalf("printed: %s", got)
	}

	// errors: the RC is libyang's return code
	var ve *data.ValidationError
	for _, e := range []struct{ name, value, rc, msg string }{
		{"yang:operation", "bogus", "LY_EVALID", `Invalid enumeration value "bogus".`},
		{"yang:position", "0", "LY_EVALID", ""},
		{"nope:x", "1", "LY_ENOTFOUND", `Module "nope" not found.`},
		{"fz:zz", "1", "LY_EINVAL", `Annotation definition for attribute "fz:zz" not found.`},
		{"fz:a b", "1", "LY_EINVAL", `Metadata name "a b" is not valid.`},
	} {
		_, err := tr.NewMeta(n, e.name, e.value)
		if !errors.As(err, &ve) || ve.RC() != e.rc || e.msg != "" && ve.Diags[len(ve.Diags)-1].Msg != e.msg {
			t.Errorf("new %s=%s: %v", e.name, e.value, err)
		}
	}
	if _, err := tr.NewMeta(n, "operation", "create"); err == nil || errors.As(err, &ve) {
		t.Errorf("unprefixed name: %v", err)
	}
	if _, err := tr.NewMeta(nil, "fz:ann", "x"); err == nil {
		t.Error("nil node")
	}
	other, _, _ := parse(fzContext(t).Schema(), `{"fz:c": {}}`, data.FormatJSON, data.ParseOptions{ParseOnly: true})
	if _, err := other.NewMeta(n, "fz:ann", "x"); !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
		t.Errorf("another context: %v", err)
	}
	if got := metaList(n); got != want {
		t.Fatalf("a failed NewMeta changed the metadata: %s", got)
	}

	// FindMeta: the first instance
	if m, err := n.FindMeta("fz:ann"); err != nil || m.Value() != "a" {
		t.Fatalf("find fz:ann: %v %v", m, err)
	}
	if m, err := n.FindMeta("yang:insert"); m != nil || err != nil {
		t.Fatalf("find absent: %v %v", m, err)
	}
	if _, err := n.FindMeta("nope:x"); !errors.As(err, &ve) || ve.Diags[0].Msg != `Module "nope" not found.` {
		t.Fatalf("find unknown module: %v", err)
	}
	if _, err := n.FindMeta("ann"); err == nil || errors.As(err, &ve) {
		t.Fatalf("find unprefixed: %v", err)
	}

	// Remove, also while iterating; a second Remove does nothing
	m, _ := n.FindMeta("yang:operation")
	m.Remove()
	m.Remove()
	for m := range n.Meta() {
		if m.Module().Name() == "fz" {
			m.Remove()
		}
	}
	if got := metaList(n); got != "yang:key=[k='a'] yang:position=2 yang:orig-default=true yang:value=" {
		t.Fatalf("after Remove: %s", got)
	}

	// a removed subtree keeps its metadata, found by module name
	c, _ := tr.Find("/fz:c")
	if _, err := tr.NewMeta(c, "yang:operation", "none"); err != nil {
		t.Fatal(err)
	}
	if err := c.Remove(); err != nil {
		t.Fatal(err)
	}
	if m, err := c.FindMeta("yang:operation"); err != nil || m == nil || m.Value() != "none" {
		t.Fatalf("detached: %v %v", m, err)
	}
	if m, err := c.FindMeta("yang:a b"); m != nil || err != nil {
		t.Fatalf("detached, invalid name: %v %v", m, err)
	}
}

// ExampleNode_Meta reads, adds and removes metadata of a data node.
func ExampleNode_Meta() {
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, fzModules)
	if err != nil {
		panic(err)
	}
	if _, err := c.Load("fz", "", nil); err != nil {
		panic(err)
	}
	tr, _, _ := data.Parse(context.Background(), strings.NewReader(`{"fz:c": {"s": "x", "@s": {"fz:ann": "a"}}}`),
		data.FormatJSON, c.Schema(), data.ParseOptions{ParseOnly: true})
	n, _ := tr.Find("/fz:c/s")
	if _, err := tr.NewMeta(n, "yang:operation", "replace"); err != nil {
		fmt.Println(err)
	}
	if _, err := tr.NewMeta(n, "yang:operation", "bogus"); err != nil {
		fmt.Println(err)
	}
	if m, _ := n.FindMeta("fz:ann"); m != nil {
		m.Remove()
	}
	for m := range n.Meta() {
		fmt.Printf("%s:%s = %s\n", m.Module().Name(), m.Name(), m.Value())
	}
	// Output:
	// Invalid enumeration value "bogus".
	// yang:operation = replace
}
