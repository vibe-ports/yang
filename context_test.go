// SPDX-License-Identifier: BSD-3-Clause

package yang_test

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
)

func TestContextLoad(t *testing.T) {
	dir := fstest.MapFS{
		"a.yang": {Data: []byte(`module a { namespace urn:a; prefix a; import ietf-inet-types { prefix inet; } import b { prefix b; } }`)},
		"b.yang": {Data: []byte(`module b { namespace urn:b; prefix b; import a { prefix a; } }`)},
		"c.yang": {Data: []byte(`module c { namespace urn:c; prefix c; import ietf-inet-types { prefix inet; } }`)},
		"s.yang": {Data: []byte("module s { namespace urn:s; prefix s;\n  foo; }")},
	}
	ctx, diags, err := yang.NewContext(yang.Options{}, dir)
	if err != nil || len(diags) != 0 {
		t.Fatal(err, diags)
	}
	diags, err = ctx.Load("a", "", nil)
	if err == nil || len(diags) != 3 || diags[0].Code != "LYVE_REFERENCE" ||
		diags[0].Msg != `A circular dependency (import) for module "a".` || diags[2].Err != "LY_EOTHER" {
		t.Errorf("a: %v %+v", err, diags)
	}
	if diags, err := ctx.Load("c", "", nil); err != nil || len(diags) != 0 {
		t.Errorf("c: %v %+v", err, diags)
	}
	// a syntax error is followed by lys_parse_in's note naming the module
	diags, err = ctx.Load("s", "", nil)
	if err == nil || len(diags) != 2 || diags[0].Line != 2 || diags[1].Msg != `Parsing module "s" failed.` {
		t.Errorf("s: %v %+v", err, diags)
	}
}

func TestContextLoadFeatures(t *testing.T) {
	dir := fstest.MapFS{"f.yang": {Data: []byte(`module f { namespace urn:f; prefix f; feature a { if-feature b; } feature b; }`)}}
	ctx, _, err := yang.NewContext(yang.Options{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	diags, err := ctx.Load("f", "", []string{"x"})
	if err == nil || len(diags) != 1 || diags[0].Phase != "parse" || diags[0].Err != "LY_EINVAL" {
		t.Errorf("unknown feature: %v %+v", err, diags)
	}
	diags, err = ctx.Load("f", "", []string{"a"})
	if err == nil || len(diags) != 1 || diags[0].Phase != "compile" || diags[0].Err != "LY_EDENIED" {
		t.Errorf("if-feature not satisfied: %v %+v", err, diags)
	}
	if diags, err := ctx.Load("f", "", []string{"*"}); err != nil {
		t.Errorf("all: %v %+v", err, diags)
	}
}

func TestContextPatternCompat(t *testing.T) {
	dir := fstest.MapFS{
		"h.yang": {Data: []byte(`module h { namespace urn:h; prefix h; leaf l { type string { pattern '[\x20-\x7E]+'; } } }`)},
		"b.yang": {Data: []byte(`module b { namespace urn:b; prefix b; leaf l { type string { pattern '\bx'; } } }`)},
		"r.yang": {Data: []byte(`module r { namespace urn:r; prefix r; leaf l { type string { pattern 'a{1001}'; } } }`)},
	}
	strict, _, err := yang.NewContext(yang.Options{}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strict.Load("h", "", nil); err == nil {
		t.Error(`strict XSD accepted \x20`)
	}
	// a pattern xsdre cannot translate (U-0001) is ErrUnsupported, not an LY_EVALID diagnostic
	if diags, err := strict.Load("r", "", nil); !errors.Is(err, yang.ErrUnsupported) {
		t.Errorf("strict a{1001}: %v %+v, want ErrUnsupported", err, diags)
	}
	ctx, _, err := yang.NewContext(yang.Options{PatternCompat: true}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if diags, err := ctx.Load("h", "", nil); err != nil {
		t.Errorf("h: %v %+v", err, diags)
	}
	if _, err := ctx.Load("b", "", nil); !errors.Is(err, yang.ErrUnsupported) {
		t.Errorf(`\b: %v, want ErrUnsupported`, err)
	}
}
