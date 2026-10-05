// SPDX-License-Identifier: BSD-3-Clause

package schema

import (
	"slices"
	"testing"
)

func TestSetAll(t *testing.T) {
	a1 := &Module{Name: "a", Revision: "2020-01-01"}
	a2 := &Module{Name: "a", Revision: "2021-01-01", Implemented: true}
	s := &Set{Modules: []*Module{a1, {Name: "b"}, a2}}
	if got := slices.Collect(s.All("a")); !slices.Equal(got, []*Module{a1, a2}) {
		t.Fatalf("All(a) = %v", got)
	}
	n := 0
	for range s.All("a") {
		n++
		break
	}
	if n != 1 {
		t.Fatal("break did not stop the iteration")
	}
	if got := slices.Collect(s.All("c")); got != nil {
		t.Fatalf("All(c) = %v", got)
	}
}

func TestKeyless(t *testing.T) {
	k := &Node{Kind: Leaf}
	for _, tc := range []struct {
		n    *Node
		want bool
	}{
		{&Node{Kind: List}, true},
		{&Node{Kind: List, Keys: []*Node{k}}, false},
		{&Node{Kind: Container}, false},
	} {
		if got := tc.n.Keyless(); got != tc.want {
			t.Errorf("%v Keyless() = %v", tc.n.Kind, got)
		}
	}
}
