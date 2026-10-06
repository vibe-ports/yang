// SPDX-License-Identifier: BSD-3-Clause

package compile

import "testing"

// TestCompileExtensions: lys_compile_extensions keeps the extension definitions of the module and
// of its submodules (module first), and the instances written inside a definition are compiled
// with it, nested ones included.
func TestCompileExtensions(t *testing.T) {
	h := newNodeHarness(t, Options{}, mapFS(map[string]string{
		"m.yang": `module m { namespace urn:m; prefix m; include s;
  extension marker { argument arg; }
  extension holder { m:marker one; m:marker two { m:marker nested; } }
}`,
		"s.yang": `submodule s { belongs-to m { prefix m; }
  extension sub-ext { argument a2; m:marker in-sub; }
}`,
	}))
	m, _, loadErr, err := h.load("m")
	if loadErr != nil || err != nil {
		t.Fatalf("load %v %v", loadErr, err)
	}
	var names []string
	for _, e := range m.Extensions {
		names = append(names, e.Name+"/"+e.ArgName)
		if e.Module != m {
			t.Errorf("%s: module %v", e.Name, e.Module)
		}
	}
	if got, want := names, []string{"marker/arg", "holder/", "sub-ext/a2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("definitions %v, want %v", got, want)
	}
	holder, sub := m.Extensions[1], m.Extensions[2]
	if len(m.Extensions[0].Exts) != 0 || len(holder.Exts) != 2 || len(sub.Exts) != 1 {
		t.Fatalf("instances: marker %d holder %d sub-ext %d", len(m.Extensions[0].Exts), len(holder.Exts), len(sub.Exts))
	}
	if i := holder.Exts[1]; i.Name != "marker" || i.Argument != "two" || len(i.Exts) != 1 || i.Exts[0].Argument != "nested" {
		t.Errorf("second instance %+v", i)
	}
	if sub.Exts[0].Argument != "in-sub" || sub.Exts[0].Def != m {
		t.Errorf("submodule definition instance %+v", sub.Exts[0])
	}
}
