// SPDX-License-Identifier: BSD-3-Clause

package yang_test

import (
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
