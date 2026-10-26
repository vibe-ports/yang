// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
)

func TestParseOpRollbackKeyDiagnostic(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 list l { key "k j"; leaf k { type string; } leaf j { type string; } action a { input { leaf arg { type string; } } } } }`)
	for _, input := range []string{`{"k":"b"}`, `{"k":"b","j":"c"}`} {
		tr, _, diags, err := parseOp(context.Background(), strings.NewReader(`{"m:l":[{"k":"a","j":"z","a":{}}]}`), set, FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		parent := tr.top.At(0)
		before := parent.kids.SchemaLen()
		_, _, diags, err = parseOp(context.Background(), strings.NewReader(input), set, FormatJSON, opRPC, parent, Reject)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EVALID" {
			t.Fatalf("RC = %v", err)
		}
		want := []yang.Diagnostic{{Err: "LY_EVALID", Code: "LYVE_DATA", Msg: "Missing the operation node.", Line: 1}}
		if strings.Contains(input, `"j"`) {
			want = append(want, yang.Diagnostic{Err: "LY_EINVAL", Code: "LYVE_SUCCESS", Msg: `Cannot free a list key "j", free the list instance instead.`})
		}
		want = append(want, yang.Diagnostic{Err: "LY_EINVAL", Code: "LYVE_SUCCESS", Msg: `Cannot free a list key "k", free the list instance instead.`})
		if !reflect.DeepEqual(diags, want) || !reflect.DeepEqual(ve.Diags, want) {
			t.Fatalf("diagnostics: %+v; error diagnostics: %+v; want %+v", diags, ve.Diags, want)
		}
		if parent.kids.SchemaLen() != before+len(want)-1 {
			t.Fatalf("parsed keys removed: %d children", parent.kids.SchemaLen())
		}
	}
}
