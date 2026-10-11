// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
)

type crossContextUserOrderedCase struct {
	name      string
	module    string
	element   string
	list      bool
	namespace string
}

var crossContextUserOrderedCases = []crossContextUserOrderedCase{
	{
		name:      "leaf-list",
		element:   "ll",
		namespace: "urn:vibe-ports:yang:diff-cross-context-userord-leaf-list",
		module: `module diff-cross-context-userord-leaf-list {
  yang-version 1.1;
  namespace "urn:vibe-ports:yang:diff-cross-context-userord-leaf-list";
  prefix dccull;
  leaf-list ll { ordered-by user; type string; }
}`,
	},
	{
		name:      "list",
		element:   "l",
		list:      true,
		namespace: "urn:vibe-ports:yang:diff-cross-context-userord-list",
		module: `module diff-cross-context-userord-list {
  yang-version 1.1;
  namespace "urn:vibe-ports:yang:diff-cross-context-userord-list";
  prefix dccul;
  list l {
    key k;
    ordered-by user;
    leaf k { type string; }
  }
}`,
	},
}

// TestDiffCrossContextUserOrderedRefusal covers the cross-context path where
// lyd_diff_userord_get creates a second-context entry that does not contain a matching node
// from the first context. lyd_diff_userord_attrs then asserts in a debug build and indexes past
// the entry in a release build; D-0121 makes that undefined case an explicit refusal.
func TestDiffCrossContextUserOrderedRefusal(t *testing.T) {
	for _, tc := range crossContextUserOrderedCases {
		t.Run(tc.name+"/prepend", func(t *testing.T) {
			setA, setB := inlineDiffSet(t, tc.module), inlineDiffSet(t, tc.module)
			first := parseOnlyXML(t, setA, tc.xml([]string{"a", "b"}))
			second := parseOnlyXML(t, setB, tc.xml([]string{"x", "a", "b"}))
			requireD0121Refusal(t, first, second)
		})

		t.Run(tc.name+"/delete-before", func(t *testing.T) {
			setA, setB := inlineDiffSet(t, tc.module), inlineDiffSet(t, tc.module)
			first := parseOnlyXML(t, setA, tc.xml([]string{"a", "b"}))
			second := parseOnlyXML(t, setB, tc.xml([]string{"b"}))
			if diff, err := diffUserOrderedNoPanic(t, first, second); err != nil {
				t.Fatalf("in-range cross-context Diff = (%v, %v), want no error", diff, err)
			}
		})
	}
}

// TestDiffCrossContextUserOrderedNoPanic samples the complete small-instance shape rather than
// any particular implementation branch. Cross-context undefined cases may be refused under
// D-0121; all same-context cases remain defined libyang inputs.
func TestDiffCrossContextUserOrderedNoPanic(t *testing.T) {
	type pair struct{ first, second []string }
	rng := rand.New(rand.NewSource(0xD0121)) //nolint:gosec // deterministic test input
	pairs := make([]pair, 2000)
	for i := range pairs {
		pairs[i] = pair{randomUserOrderedValues(rng), randomUserOrderedValues(rng)}
	}

	for _, tc := range crossContextUserOrderedCases {
		t.Run(tc.name, func(t *testing.T) {
			setA, setB := inlineDiffSet(t, tc.module), inlineDiffSet(t, tc.module)
			t.Run("cross-context", func(t *testing.T) {
				for i, p := range pairs {
					first := parseOnlyXML(t, setA, tc.xml(p.first))
					second := parseOnlyXML(t, setB, tc.xml(p.second))
					diff, err := diffUserOrderedNoPanic(t, first, second)
					if err != nil {
						assertD0121Error(t, diff, err, i, p.first, p.second)
					}
				}
			})
			t.Run("same-context", func(t *testing.T) {
				for i, p := range pairs {
					first := parseOnlyXML(t, setA, tc.xml(p.first))
					second := parseOnlyXML(t, setA, tc.xml(p.second))
					if diff, err := diffUserOrderedNoPanic(t, first, second); err != nil {
						t.Fatalf("pair %d same-context Diff(%v, %v) = (%v, %v)", i, p.first, p.second, diff, err)
					}
				}
			})
		})
	}
}

func (tc crossContextUserOrderedCase) xml(values []string) string {
	var b strings.Builder
	for _, value := range values {
		b.WriteByte('<')
		b.WriteString(tc.element)
		b.WriteString(` xmlns="`)
		b.WriteString(tc.namespace)
		b.WriteString(`">`)
		if tc.list {
			b.WriteString("<k>")
		}
		b.WriteString(value)
		if tc.list {
			b.WriteString("</k>")
		}
		b.WriteString("</")
		b.WriteString(tc.element)
		b.WriteByte('>')
	}
	return b.String()
}

func randomUserOrderedValues(rng *rand.Rand) []string {
	values := []string{"a", "b", "c", "d", "e"}
	rng.Shuffle(len(values), func(i, j int) { values[i], values[j] = values[j], values[i] })
	return values[:rng.Intn(5)]
}

func diffUserOrderedNoPanic(t *testing.T, first, second *Tree) (diff *Tree, err error) {
	t.Helper()
	defer func() {
		if v := recover(); v != nil {
			t.Fatalf("cross-context user-ordered Diff panicked: %v", v)
		}
	}()
	return Diff(first, second, DiffOptions{})
}

func requireD0121Refusal(t *testing.T, first, second *Tree) {
	t.Helper()
	diff, err := diffUserOrderedNoPanic(t, first, second)
	if err == nil {
		t.Fatalf("unsafe cross-context Diff = (%v, nil), want LY_EINVAL D-0121 refusal", diff)
	}
	assertD0121Error(t, diff, err, -1, nil, nil)
}

func assertD0121Error(t *testing.T, diff *Tree, err error, pair int, first, second []string) {
	t.Helper()
	var ve *ValidationError
	if diff != nil || !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("pair %d cross-context Diff(%v, %v) = (%v, %T %v), want (nil, *ValidationError LY_EINVAL containing D-0121)",
			pair, first, second, diff, err, err)
	}
}
