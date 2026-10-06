// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPrintCorpus: the printers against the goldens of the conformance fixtures print/* (libyang
// v5.8.6 through the oracle): the same inputs parsed over pjSchema, which mirrors the fixtures'
// pj and pk modules, printed in both formats as a whole tree (lyd_print_all) or as the fixture's
// subtree (lyd_print_tree). Opaque nodes with attributes and value prefixes, subtrees of every
// node kind, and empty (leaf-)lists.
func TestPrintCorpus(t *testing.T) {
	dir := filepath.Join("..", "conformance", "corpus", "print")
	// child is the first child of n named name (an instance with the value or key text val, if set)
	child := func(n *Node, name, val string) *Node {
		for c := range n.Children() {
			if c.Name() != name {
				continue
			}
			if val == "" || c.isTerm() && c.value.Canonical() == val {
				return c
			}
			for k := range c.Children() {
				if k.isTerm() && k.schema.Name == "k" && k.value.Canonical() == val {
					return c
				}
			}
		}
		t.Fatalf("no child %s %q of %s", name, val, n.Name())
		return nil
	}
	for _, c := range []struct {
		id, input string
		unknown   UnknownPolicy
		opts      PrintOptions
		subtree   func(top *Node) *Node // nil: the whole tree
	}{
		{"opaque-attrs-xml", "opaque-attrs.xml", Opaque, PrintOptions{}, nil},
		{"opaque-attrs-json", "opaque-attrs.json", Opaque, PrintOptions{}, nil},
		{"subtree-container", "subtree.json", Reject, PrintOptions{}, func(c *Node) *Node { return child(c, "in", "") }},
		{"subtree-list-instance", "subtree.json", Reject, PrintOptions{}, func(c *Node) *Node { return child(c, "l", "b") }},
		{"subtree-leaf-list-instance", "subtree.json", Reject, PrintOptions{}, func(c *Node) *Node { return child(c, "ll", "2") }},
		{"subtree-augment", "subtree.json", Reject, PrintOptions{}, func(c *Node) *Node { return child(c, "ext", "") }},
		{"empty-leaf-list", "empty-leaf-list.json", Reject, PrintOptions{EmptyLeafList: true}, nil},
	} {
		t.Run(c.id, func(t *testing.T) {
			in, err := os.ReadFile(filepath.Join(dir, "data", c.input)) //nolint:gosec // fixture path
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "golden", c.id+".json")) //nolint:gosec // fixture path
			if err != nil {
				t.Fatal(err)
			}
			type printed struct{ JSON, XML string }
			var golden struct{ Tree, Subtree *printed }
			if err := json.Unmarshal(raw, &golden); err != nil {
				t.Fatal(err)
			}
			parse := parseJSONString
			if filepath.Ext(c.input) == ".xml" {
				parse = parseXMLString
			}
			tr, diags, err := parse(pjSchema(), string(in), c.unknown, true)
			if err != nil {
				t.Fatalf("%v %v", err, diags)
			}
			want := golden.Tree
			printJSON, printXML := tr.PrintJSON, tr.PrintXML
			if c.subtree != nil {
				want = golden.Subtree
				var top *Node
				for n := range tr.Top() {
					top = n
					break
				}
				n := c.subtree(top)
				printJSON, printXML = n.PrintJSON, n.PrintXML
			}
			for _, f := range []struct {
				name  string
				print func(w *bytes.Buffer) error
				want  string
			}{
				{"JSON", func(w *bytes.Buffer) error { return printJSON(w, c.opts) }, want.JSON},
				{"XML", func(w *bytes.Buffer) error { return printXML(w, c.opts) }, want.XML},
			} {
				var b bytes.Buffer
				if err := f.print(&b); err != nil {
					t.Fatalf("%s: %v", f.name, err)
				}
				if b.String() != f.want {
					t.Errorf("%s:\n%s\nwant:\n%s", f.name, b.String(), f.want)
				}
			}
		})
	}
}
