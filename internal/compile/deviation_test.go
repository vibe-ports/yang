// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"testing"
)

// TestDeviatedInputKeepsChildren: a deviation that changes an rpc input or output (here a must
// added to the input) keeps its children (D-0090; libyang crashes freeing its copy of the node).
func TestDeviatedInputKeepsChildren(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "b.yang", `module b { namespace urn:b; prefix b;
  rpc r { input { leaf a { type string; } } output { leaf o { type string; } } } }`)
	write(t, dir, "d.yang", `module d { namespace urn:d; prefix d; import b { prefix b; }
  deviation /b:r/b:input { deviate add { must "b:a != 'x'"; } } }`)
	c := newCtx(t, Options{}, dir)
	if _, _, err := c.Load("d", "", nil); err != nil {
		t.Fatal(err)
	}
	r := c.implemented("b").Schema.Top[0]
	in, out := r.Children[0], r.Children[1]
	if in.Name != "input" || len(in.Musts) != 1 || len(in.Children) != 1 || in.Children[0].Name != "a" ||
		len(out.Children) != 1 {
		t.Fatalf("input %+v, output %+v", in, out)
	}
}
