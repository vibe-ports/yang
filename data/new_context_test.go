// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/contextopts"
	"github.com/vibe-ports/yang/internal/schema"
)

func implicitConstructorSet(t *testing.T) *schema.Set {
	t.Helper()
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{"a.yang": {Data: []byte(`module a {
  namespace urn:implicit-constructor; prefix a;
  container p {
    leaf-list term { type string; }
    container inner { presence inner; }
    list l { key k; leaf k { type uint32; } }
  }
}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load("a", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	return set
}

// Compare the bridge to the raw constructors already on main, including the
// same work scope and placement. Repeated values and descending keys exercise
// retained duplicate registrations and adverse insertion order.
func TestImplicitConstructorWorkMatchesMain(t *testing.T) {
	set := implicitConstructorSet(t)
	for _, kind := range []string{"term", "inner", "list", "list2"} {
		for _, count := range []int{64, 256} {
			t.Run(fmt.Sprintf("%s/%d", kind, count), func(t *testing.T) {
				measure := func(bridge bool) (int64, []string) {
					tr := newTree(set)
					p, err := tr.NewInner(nil, "a:p", NewOptions{})
					if err != nil {
						t.Fatal(err)
					}
					tr.work.visits = true
					for i := count - 1; i >= 0; i-- {
						value, name := fmt.Sprint(i%16), kind
						keys := []string{value}
						if kind == "list" || kind == "list2" {
							name = "l"
						}
						if kind == "list2" {
							value = "[k='" + value + "']"
						}
						if bridge {
							err = contextopts.NewImplicitNode(tr, p, kind, name, value, keys, contextopts.NewImplicitOptions{})
						} else {
							end := tr.work.beginHashWork()
							b := newBuilder{set: set, log: &logger{set: set}, work: &tr.work, tree: tr}
							var n *Node
							switch kind {
							case "term":
								n, err = b.newTerm(p, nil, name, value, newValOptions{})
							case "inner":
								n, err = b.newInner(p, nil, name, false)
							case "list":
								n, err = b.newList(p, nil, name, keys, newValOptions{}, "lyd_new_list3")
							case "list2":
								n, err = b.newList2(p, nil, name, value, newValOptions{})
							}
							_, err = tr.placed(b, p, n, err)
							end()
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					checkFindAgainstLinear(t, tr)
					return tr.work.Load(), typedDump(tr)
				}
				mainWork, mainDump := measure(false)
				headWork, headDump := measure(true)
				t.Logf("counted work main=%d head=%d", mainWork, headWork)
				if mainWork != headWork || !slices.Equal(mainDump, headDump) {
					t.Fatalf("bridge changed counted work or tree: main=%d head=%d", mainWork, headWork)
				}
			})
		}
	}
}

func TestImplicitConstructorBudgetRollback(t *testing.T) {
	set := implicitConstructorSet(t)
	for _, kind := range []string{"term", "inner", "list", "list2"} {
		t.Run(kind, func(t *testing.T) {
			checkEveryHashBudgetPoint(t, func() (*Tree, func() error) {
				tr := newTree(set)
				p, err := tr.NewInner(nil, "a:p", NewOptions{})
				if err != nil {
					t.Fatal(err)
				}
				name, value := kind, "1"
				if kind == "list" || kind == "list2" {
					name = "l"
				}
				if kind == "list2" {
					value = "[k='1']"
				}
				return tr, func() error {
					return contextopts.NewImplicitNode(tr, p, kind, name, value, []string{"1"}, contextopts.NewImplicitOptions{})
				}
			})
		})
	}
}
