// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"os"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

// TestInternalAnnotations looks up the annotations libyang adds to yang, ietf-netconf and
// ietf-netconf-with-defaults (lysp_add_internal_*) as metadata of those modules.
func TestInternalAnnotations(t *testing.T) {
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS("../conformance/corpus/ietf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ietf-netconf", "ietf-netconf-with-defaults"} {
		if _, diags, err := c.Load(name, "", nil); err != nil {
			t.Fatal(name, err, diags)
		}
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	enums := func(t *schema.Type) string {
		var s []string
		for _, e := range t.Enums {
			s = append(s, e.Name)
		}
		return strings.Join(s, " ")
	}
	for _, tc := range []struct{ mod, name, want string }{
		{"ietf-netconf", "operation", "enum merge replace create delete remove"},
		{"ietf-netconf", "type", "enum subtree xpath"},
		{"ietf-netconf", "select", "ietf-yang-types:xpath1.0"},
		{"ietf-netconf-with-defaults", "default", "bool"},
		{"yang", "lyds_tree", "yang:lyds_tree"},
		{"yang", "insert", "enum first last before after"}, // from yang.yang itself
	} {
		a := metaAnnotation(set.Module(tc.mod, ""), tc.name)
		if a == nil || a.Type == nil {
			t.Errorf("%s:%s: no annotation", tc.mod, tc.name)
			continue
		}
		got := ""
		switch {
		case a.Type.Base == schema.Enumeration:
			got = "enum " + enums(a.Type)
		case a.Type.Base == schema.Bool:
			got = "bool"
		case a.Type.TypedefModule != nil:
			got = a.Type.TypedefModule.Name + ":" + a.Type.Typedef
		}
		if got != tc.want {
			t.Errorf("%s:%s: type %q, want %q", tc.mod, tc.name, got, tc.want)
		}
	}
	if metaAnnotation(set.Module("ietf-netconf", ""), "default") != nil {
		t.Error("ietf-netconf has no default annotation")
	}
	// the internal data nodes, last of the data (before the rpcs and notifications)
	top := func(mod, name string) *schema.Node {
		for _, n := range set.Module(mod, "").Top {
			if n.Name == name {
				return n
			}
		}
		t.Fatalf("%s: no top node %s", mod, name)
		return nil
	}
	if n := top("ietf-netconf", "rpc-error"); !n.Presence || len(n.Children) != 5 {
		t.Errorf("rpc-error: presence %v, %d children", n.Presence, len(n.Children))
	}
	if n := top("yang", "date-and-time"); n.Type.Typedef != "date-and-time" {
		t.Errorf("date-and-time: type %s", n.Type.Typedef)
	}
}
