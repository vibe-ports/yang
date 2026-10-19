// SPDX-License-Identifier: BSD-3-Clause

package lyxml

import "testing"

func TestValuePrefixNext(t *testing.T) {
	type seg struct {
		s      string
		prefix bool
	}
	for _, c := range []struct {
		in   string
		want []seg
	}{
		{"a:b", []seg{{"a", true}, {"b", false}}},
		{"x/a:b/c:d", []seg{{"x/", false}, {"a", true}, {"b/", false}, {"c", true}, {"d", false}}},
		{"'p:v'", []seg{{"'", false}, {"p", true}, {"v'", false}}},
		{"×p:v", []seg{{"×", false}, {"p", true}, {"v", false}}},
		{"a÷p:v", []seg{{"a÷", false}, {"p", true}, {"v", false}}},
		{";p:v", []seg{{";", false}, {"p", true}, {"v", false}}},
		{"é:v", []seg{{"é", true}, {"v", false}}},
		{"𠜎:v", []seg{{"𠜎", true}, {"v", false}}},
		{"p\u0301:v", []seg{{"p\u0301", true}, {"v", false}}},
		{"\u0301p:v", []seg{{"\u0301", false}, {"p", true}, {"v", false}}},
		{"p:", []seg{{"p", true}}},
		{"×p:", []seg{{"×", false}, {"p", true}}},
		{"\xf0\x81\x80\x80A", []seg{{"\xf0\x81\x80\x80A", false}}},
		{"", nil},
		{"plain", []seg{{"plain", false}}},
		{"1:2", []seg{{"1:2", false}}},
	} {
		var got []seg
		for i := 0; ; {
			n, p, next, ok := ValuePrefixNext(c.in, i)
			if !ok {
				t.Fatalf("%q: unexpected scan error", c.in)
			}
			if n == 0 {
				break
			}
			got = append(got, seg{c.in[i : i+n], p})
			if next < 0 {
				break
			}
			i = next
		}
		if len(got) != len(c.want) {
			t.Fatalf("%q: got %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%q: got %v, want %v", c.in, got, c.want)
			}
		}
	}
	if _, _, _, ok := ValuePrefixNext("\xffp:v", 0); ok {
		t.Fatal("invalid input reported as end of scan")
	}
}
