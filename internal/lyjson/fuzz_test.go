// SPDX-License-Identifier: BSD-3-Clause

package lyjson

import (
	"strings"
	"testing"
)

// FuzzJSONLex: any input terminates without panicking, every step consumes input, the status
// stack respects the nesting budget, and every failure carries a diagnostic.
func FuzzJSONLex(f *testing.F) {
	for _, s := range []string{
		``, ` `, `{"a":[1,-0.0,1.5e-3,"xé\n",true,false,null,{}]}`, `[1e65536]`, `0.5e1`, `"\ud800"`,
		`["\u12`, `{"a" 1}`, "[\"\xf0\x91\x80\x80\"]", `[[[[[[[[[[[[`, `{"a":{"b":[]}} x`, "[1\x00]",
		`[0.0000000000000000000001e22,123456789012345678901e-20,0.1234567890123456789e19]`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		l, err := New(in)
		if err != nil {
			if len(diags(err)) == 0 {
				t.Fatal("error without diagnostics")
			}
			return
		}
		for {
			before, line := l.Offset(), l.Line()
			st, err := l.Next()
			if err != nil {
				if len(diags(err)) == 0 {
					t.Fatal("error without diagnostics")
				}
				return
			}
			if l.Depth() > MaxDepth+1 {
				t.Fatalf("depth %d", l.Depth())
			}
			if l.Line() < line || strings.IndexByte(l.Value(), 0) >= 0 {
				t.Fatalf("line went back or NUL in value at %d", before)
			}
			if st == TokenEnd {
				return
			}
			if l.Offset() <= before {
				t.Fatalf("no progress at %d (%v)", before, st)
			}
		}
	})
}
