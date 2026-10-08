// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"path/filepath"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// nacmFixture is ty6.yang of conformance/corpus/types and the nacm rule path leaf's type.
func nacmFixture() (*schema.Set, *schema.Module, *schema.Type, *schema.Node) {
	set := &schema.Set{}
	m := &schema.Module{Name: "ty6", Namespace: "urn:vibe-ports:ty6", Prefix: "t6", Implemented: true}
	nacm := &schema.Module{Name: "ietf-netconf-acm", Namespace: "urn:ietf:params:xml:ns:yang:ietf-netconf-acm",
		Prefix: "nacm", Implemented: true}
	set.Modules = append(set.Modules, m, nacm)
	c := node(m, nil, schema.Container, "c", nil)
	l := list(m, c, "l", typ(schema.String))
	l.Keys[0].Name = "k"
	node(m, l, schema.Leaf, "v", typ(schema.String))
	node(m, c, schema.LeafList, "ll", typ(schema.String))
	nid := &schema.Type{Base: schema.String, Typedef: "node-instance-identifier", TypedefModule: nacm}
	return set, m, nid, node(nacm, nil, schema.Leaf, "path", nid)
}

// TestOracleGoldensNodeInstanceID replays the types/nacm-path-* fixtures: the canonical value, or
// the logged path error and the plugin's error item.
func TestOracleGoldensNodeInstanceID(t *testing.T) {
	set, m, nid, path := nacmFixture()
	xml := XMLNamespaces{Set: set, NS: map[string]string{"t": m.Namespace}}
	for _, c := range []struct {
		id, lex string
		f       Format
	}{
		{"nacm-path-root", "/", FormatXML},
		{"nacm-path-list-no-keys", "/t:c/t:l", FormatXML},
		{"nacm-path-with-key", `/t:c/t:l[t:k="it's"]/t:v`, FormatXML},
		{"nacm-path-json", "/ty6:c/ll[.='x']", FormatJSON},
		{"nacm-path-unknown-node", "/t:c/t:nope", FormatXML},
		{"nacm-path-missing-prefix", "/c/l", FormatXML},
	} {
		g := loadGolden(t, filepath.Join("..", "..", "conformance", "corpus", "types", "golden", c.id+".json"))
		var pc PrefixCtx = xml
		h := HintData
		if c.f == FormatJSON {
			pc, h = ModuleNames{set}, JSONHints("string")
		}
		v, d := Store(nid, c.lex, c.f, h, pc, path)
		if g.Verdict == "invalid" {
			if d == nil || d.Logged == nil || len(g.Diagnostics) != 2 || d.Logged.Msg != g.Diagnostics[0].Msg ||
				d.Logged.Code != g.Diagnostics[0].VecodeName || d.Msg != g.Diagnostics[1].Msg {
				t.Errorf("%s: got %+v, golden %+v", c.id, d, g.Diagnostics)
			}
			continue
		}
		want := g.canonical("/ietf-netconf-acm:nacm/rule-list[name='r']/rule[name='a']/path")
		if d != nil || v.Canonical() != want {
			t.Errorf("%s: got %v %q, golden %q", c.id, d, v.Canonical(), want)
		}
	}
}
