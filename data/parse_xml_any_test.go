// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

func TestXMLAnyPayload(t *testing.T) {
	tr, ad, ax := anyFixture()
	set := tr.set
	leaf := &schema.Node{Name: "known", Kind: schema.Leaf, Module: ad.Module,
		Type: &schema.Type{Base: schema.String}}
	ad.Module.Top = append(ad.Module.Top, leaf)
	parse := func(input string, strict bool) (*Tree, error) {
		o := parseOpts{ParseOptions: ParseOptions{Unknown: Skip, AnyDataStrict: strict, ParseOnly: true}}
		got, _, err := parseWith(context.Background(), strings.NewReader(input), set, o, parseXML, nil)
		return got, err
	}
	const ns = ` xmlns="urn:anycore"`
	t.Run("namespaces and siblings", func(t *testing.T) {
		got, err := parse(`<a`+ns+` xmlns:p="urn:outer"><p:inner xmlns:p="urn:inner" p:a="p:v">p:x</p:inner><p:outer>p:y</p:outer></a>`, false)
		if err != nil {
			t.Fatal(err)
		}
		n := got.top.first()
		if n.schema != ad || n.kids.len() != 0 || n.any.children.len() != 2 {
			t.Fatalf("any ownership: %+v", n)
		}
		first, second := n.any.children.opaqueAt(0), n.any.children.opaqueAt(1)
		if first.opaq.ModuleNS != "urn:inner" || second.opaq.ModuleNS != "urn:outer" ||
			first.opaq.Value != "p:x" || second.opaq.Value != "p:y" {
			t.Fatalf("namespace shadowing: %+v %+v", first.opaq, second.opaq)
		}
		ctx, ok := first.opaq.Prefixes.(types.XMLNamespaces)
		if !ok || ctx.NS["p"] != "urn:inner" {
			t.Fatalf("value prefix snapshot: %+v", first.opaq.Prefixes)
		}
	})
	t.Run("escaped text", func(t *testing.T) {
		got, err := parse(`<x`+ns+`>&lt;tag/&gt;</x>`, false)
		if err != nil || got.top.first().schema != ax || got.top.first().any.text == nil || *got.top.first().any.text != "<tag/>" {
			t.Fatalf("text: %v, %+v", err, got)
		}
	})
	t.Run("nested any", func(t *testing.T) {
		got, err := parse(`<x`+ns+`><a><known>v</known></a></x>`, false)
		if err != nil {
			t.Fatal(err)
		}
		inner := got.top.first().any.children.At(0)
		if inner.schema != ad || inner.any.children.len() != 1 || inner.any.children.At(0).Value() != "v" {
			t.Fatalf("nested payload: %+v", inner)
		}
	})
	t.Run("strict", func(t *testing.T) {
		if _, err := parse(`<a`+ns+`><unknown/></a>`, true); err == nil || !strings.Contains(err.Error(), "No module") && !strings.Contains(err.Error(), "Node") {
			t.Fatalf("strict unknown: %v", err)
		}
		// parser_xml.c:614-620: an undeclared prefix, then a missing default namespace
		for in, want := range map[string]string{
			`<a` + ns + `><q:b/></a>`:               `No module with namespace "q" in the context.`,
			`<p:a xmlns:p="urn:anycore"><b/></p:a>`: `No default namespace in the context.`,
		} {
			if _, err := parse(in, true); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("strict %s: %v; want %s", in, err, want)
			}
		}
		got, err := parse(`<a`+ns+`><known>v</known></a>`, true)
		if err != nil || got.top.first().any.children.At(0).schema != leaf {
			t.Fatalf("strict known: %v, %+v", err, got)
		}
	})
	t.Run("mixed content", func(t *testing.T) {
		if _, err := parse(`<x`+ns+`><outer>text<inner/></outer></x>`, false); err == nil || !strings.Contains(err.Error(), "Mixed XML content") {
			t.Fatalf("mixed: %v", err)
		}
	})
	t.Run("direct mixed content", func(t *testing.T) {
		// libyang keeps the leading text and checks no mixed content for anyxml itself
		// (any-core/xpath-anyxml-mixed-text-first).
		got, err := parse(`<x`+ns+`>text<inner/></x>`, false)
		if err != nil || got.top.first().any.text == nil || *got.top.first().any.text != "text" {
			t.Fatalf("direct mixed: %v, %+v", err, got)
		}
	})
}
