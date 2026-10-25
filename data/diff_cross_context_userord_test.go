// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

type crossContextUserOrderedCase struct {
	name, module, element, namespace string
	list                             bool
}

var crossContextUserOrderedCases = []crossContextUserOrderedCase{
	{
		name: "leaf-list", element: "ll", namespace: "urn:vibe-ports:yang:diff-cross-context-userord-leaf-list",
		module: `module diff-cross-context-userord-leaf-list {
  yang-version 1.1;
  namespace "urn:vibe-ports:yang:diff-cross-context-userord-leaf-list";
  prefix dccull;
  leaf-list ll { ordered-by user; type string; }
}`,
	},
	{
		name: "list", element: "l", list: true, namespace: "urn:vibe-ports:yang:diff-cross-context-userord-list",
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

func crossContextUserOrderedSet(t *testing.T, module string) *schema.Set {
	t.Helper()
	name := strings.Fields(module)[1]
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{
		name + ".yang": {Data: []byte(module)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load(name, "", nil); err != nil {
		t.Fatal(err, diags)
	}
	return c.Snapshot()
}

// TestDiffCrossContextUserOrderedRefusal covers the native assertion/out-of-bounds boundary in
// lyd_diff_userord_attrs. D-0121 turns that undefined behavior into an explicit refusal including the formerly accepted final transition.
func TestDiffCrossContextUserOrderedRefusal(t *testing.T) {
	for _, tc := range crossContextUserOrderedCases {
		t.Run(tc.name+"/prepend", func(t *testing.T) {
			setA, setB := crossContextUserOrderedSet(t, tc.module), crossContextUserOrderedSet(t, tc.module)
			requireD0121Refusal(t,
				parseOnlyXML(t, setA, tc.xml([]string{"a", "b"})),
				parseOnlyXML(t, setB, tc.xml([]string{"x", "a", "b"})))
		})
		t.Run(tc.name+"/delete-before", func(t *testing.T) {
			setA, setB := crossContextUserOrderedSet(t, tc.module), crossContextUserOrderedSet(t, tc.module)
			requireD0121Refusal(t,
				parseOnlyXML(t, setA, tc.xml([]string{"a", "b"})),
				parseOnlyXML(t, setB, tc.xml([]string{"b"})))
		})
	}
}

// TestDiffCrossContextUserOrderedNoPanic samples the complete small-instance shape. A
// cross-context undefined case may return D-0121; every same-context combination must remain
// defined.
func TestDiffCrossContextUserOrderedNoPanic(t *testing.T) {
	type pair struct{ first, second []string }
	rng := rand.New(rand.NewSource(0xD0121)) //nolint:gosec // deterministic test data
	pairs := make([]pair, 2000)
	for i := range pairs {
		pairs[i] = pair{randomUserOrderedValues(rng), randomUserOrderedValues(rng)}
	}
	for _, tc := range crossContextUserOrderedCases {
		t.Run(tc.name, func(t *testing.T) {
			setA, setB := crossContextUserOrderedSet(t, tc.module), crossContextUserOrderedSet(t, tc.module)
			t.Run("cross-context", func(t *testing.T) {
				for i, p := range pairs {
					diff, err := diffUserOrderedNoPanic(t,
						parseOnlyXML(t, setA, tc.xml(p.first)), parseOnlyXML(t, setB, tc.xml(p.second)))
					if err != nil {
						assertD0121Error(t, diff, err, i, p.first, p.second)
					}
				}
			})
			t.Run("same-context", func(t *testing.T) {
				for i, p := range pairs {
					if diff, err := diffUserOrderedNoPanic(t,
						parseOnlyXML(t, setA, tc.xml(p.first)), parseOnlyXML(t, setA, tc.xml(p.second))); err != nil {
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
		b.WriteString("<" + tc.element + ` xmlns="` + tc.namespace + `">`)
		if tc.list {
			b.WriteString("<k>")
		}
		b.WriteString(value)
		if tc.list {
			b.WriteString("</k>")
		}
		b.WriteString("</" + tc.element + ">")
	}
	return b.String()
}

func randomUserOrderedValues(rng *rand.Rand) []string {
	values := []string{"a", "b", "c", "d", "e"}
	rng.Shuffle(len(values), func(i, j int) { values[i], values[j] = values[j], values[i] })
	return values[:rng.Intn(len(values))]
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
