// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// printFixture is the compiled form of the modules pt, ptb (identities, annotations) and pta
// (augments of pt): the oracle expectations below were printed by libyang v5.8.6 yanglint
// (-f json / -f xml) from one JSON input with the same nodes.
type printFixture struct {
	set          *schema.Set
	pt, ptb, pta *schema.Module
	nodes        map[string]*schema.Node
	tr           *Tree
}

func newPrintFixture(t *testing.T) *printFixture {
	t.Helper()
	f := &printFixture{nodes: map[string]*schema.Node{}, set: &schema.Set{}}
	mk := func(name, prefix string) *schema.Module {
		return &schema.Module{Name: name, Namespace: "urn:vibe-ports:" + name, Prefix: prefix, Implemented: true}
	}
	f.pt, f.ptb, f.pta = mk("pt", "pt"), mk("ptb", "ptb"), mk("pta", "pta")
	f.set.Modules = []*schema.Module{f.ptb, f.pt, f.pta}
	base := &schema.Identity{Name: "base", Module: f.ptb}
	der := &schema.Identity{Name: "der", Module: f.ptb}
	base.Derived = []*schema.Identity{der}
	f.ptb.Identities = []*schema.Identity{base, der}
	own := &schema.Identity{Name: "own", Module: f.pt}
	base.Derived = append(base.Derived, own)
	f.pt.Identities = []*schema.Identity{own}

	ty := func(b schema.BaseType, opts ...func(*schema.Type)) *schema.Type {
		x := &schema.Type{Base: b}
		for _, o := range opts {
			o(x)
		}
		return x
	}
	str, u8 := ty(schema.String), ty(schema.Uint8)
	idT := ty(schema.IdentityRef, func(x *schema.Type) { x.Bases = []*schema.Identity{base} })
	add := func(m *schema.Module, p *schema.Node, k schema.Kind, name string, typ *schema.Type) *schema.Node {
		n := &schema.Node{Kind: k, Name: name, Module: m, Parent: p, Type: typ, Config: true}
		if p == nil {
			m.Top = append(m.Top, n)
		} else {
			p.Children = append(p.Children, n)
		}
		f.nodes[name] = n
		return n
	}
	c := add(f.pt, nil, schema.Container, "c", nil)
	for _, l := range []struct {
		name string
		t    *schema.Type
	}{{"s", str}, {"n", u8}, {"big", ty(schema.Uint64)}, {"i64", ty(schema.Int64)},
		{"d", ty(schema.Dec64, func(x *schema.Type) { x.FracDigits = 2 })}, {"b", ty(schema.Bool)}, {"e", ty(schema.Empty)},
		{"id", idT}, {"idl", idT},
		{"bits", ty(schema.Bits, func(x *schema.Type) { x.Bits = []*schema.Bit{{Name: "x"}, {Name: "y", Position: 1}} })},
		{"bin", ty(schema.Binary)},
		{"en", ty(schema.Enumeration, func(x *schema.Type) { x.Enums = []*schema.Enum{{Name: "one"}, {Name: "two", Value: 1}} })},
		{"un", ty(schema.Union, func(x *schema.Type) { x.Union = []*schema.Type{u8, str} })},
		{"ii", ty(schema.InstanceID)}} {
		add(f.pt, c, schema.Leaf, l.name, l.t)
	}
	add(f.pt, c, schema.LeafList, "ll", str)
	add(f.pt, c, schema.LeafList, "ul", str).UserOrdered = true
	add(f.pt, c, schema.LeafList, "ln", u8)
	l := add(f.pt, c, schema.List, "l", nil)
	k1 := add(f.pt, l, schema.Leaf, "k1", str) // compile puts the keys first, in key order
	k2 := add(f.pt, l, schema.Leaf, "k2", u8)
	l.Keys = []*schema.Node{k1, k2}
	add(f.pt, l, schema.Leaf, "v", str)
	add(f.pt, add(f.pt, l, schema.Container, "inner", nil), schema.Leaf, "x", str)
	ol := add(f.pt, c, schema.List, "ol", nil)
	ol.UserOrdered = true
	ol.Keys = []*schema.Node{add(f.pt, ol, schema.Leaf, "k", str)}
	add(f.pt, c, schema.Leaf, "last", str)
	add(f.pta, c, schema.Leaf, "ext", str)
	add(f.pta, add(f.pta, c, schema.Container, "ec", nil), schema.Leaf, "y", str)
	add(f.pta, l, schema.Leaf, "lx", str)
	add(f.pt, nil, schema.Leaf, "top", str)
	add(f.pta, nil, schema.Leaf, "topa", str)
	f.tr = newTree(f.set)
	return f
}

// term stores lex as JSON input for the leaf name (the module of the node resolves prefixes).
func (f *printFixture) term(t *testing.T, name, lex string) *Node {
	t.Helper()
	s := f.nodes[name]
	h := types.JSONHints("string") | types.HintStringDatatypes
	if s.Type.Base == schema.Empty {
		h = types.JSONHints("empty")
	}
	v, d := types.Store(s.Type, lex, types.FormatJSON, h, types.ModuleNames{Set: f.set}, s)
	if d != nil {
		t.Fatalf("%s=%q: %s", name, lex, d.Msg)
	}
	return newTerm(s, v)
}

// note attaches the annotation ptb:name with the JSON value lex to n.
func (f *printFixture) note(t *testing.T, n *Node, name, typ, lex string) {
	t.Helper()
	var ty *schema.Type
	switch typ {
	case "string":
		ty = &schema.Type{Base: schema.String}
	case "uint8":
		ty = &schema.Type{Base: schema.Uint8}
	default:
		ty = f.nodes["id"].Type
	}
	v, d := types.Store(ty, lex, types.FormatJSON, types.JSONHints("string")|types.HintStringDatatypes, types.ModuleNames{Set: f.set}, nil)
	if d != nil {
		t.Fatal(d.Msg)
	}
	n.meta = append(n.meta, meta{f.ptb, name, v})
}

// build is the tree of the oracle input (the empty NP container and the state list are
// implicit nodes the explicit with-defaults mode drops, so they are not built).
func (f *printFixture) build(t *testing.T) *Tree {
	t.Helper()
	tr := f.tr
	ins := func(parent, n *Node) *Node { tr.insert(parent, n, insertDefault); return n }
	c := ins(nil, newInner(f.nodes["c"]))
	f.note(t, c, "note", "string", "on-container")
	s := ins(c, f.term(t, "s", "a \"q\" \\ \r\t\n é <&> ]]>"))
	f.note(t, s, "note", "string", "hello")
	f.note(t, s, "lvl", "uint8", "3")
	f.note(t, s, "who", "ident", "ptb:der")
	for _, kv := range [][2]string{{"n", "5"}, {"big", "18446744073709551615"}, {"i64", "-9223372036854775808"},
		{"d", "1.50"}, {"b", "true"}, {"e", ""}, {"id", "ptb:der"}, {"idl", "pt:own"}, {"bits", "y x"}, {"bin", "YQ=="},
		{"en", "two"}, {"un", "7"}, {"ii", "/pt:c/l[k1='a'][k2='1']/v"}} {
		ins(c, f.term(t, kv[0], kv[1]))
	}
	for _, v := range []string{"b", "a", "c"} {
		n := ins(c, f.term(t, "ll", v))
		if v == "b" {
			f.note(t, n, "note", "string", "second")
		}
	}
	for _, v := range []string{"z", "y"} {
		ins(c, f.term(t, "ul", v))
	}
	for _, v := range []string{"9", "3"} {
		ins(c, f.term(t, "ln", v))
	}
	list := func(k1, k2, v string) *Node {
		l := newInner(f.nodes["l"]) // linked last: the keys must be in place for the sorted insert
		ins(l, f.term(t, "v", v))
		ins(l, f.term(t, "k2", k2))
		ins(l, f.term(t, "k1", k1))
		return l
	}
	lb := list("b", "2", "y")
	f.note(t, lb, "lvl", "uint8", "1")
	la := list("a", "1", "x")
	ins(la, f.term(t, "lx", "lx"))
	ins(ins(la, newInner(f.nodes["inner"])), f.term(t, "x", "1"))
	ins(c, lb)
	ins(c, la)
	for _, k := range []string{"z", "a"} {
		ins(ins(c, newInner(f.nodes["ol"])), f.term(t, "k", k))
	}
	ins(c, f.term(t, "last", "end"))
	ins(c, f.term(t, "ext", "e"))
	ins(ins(c, newInner(f.nodes["ec"])), f.term(t, "y", "1"))
	ins(nil, f.term(t, "top", "t"))
	ins(nil, f.term(t, "topa", "ta"))
	return tr
}

const printJSONWant = `{
  "pt:c": {
    "@": {
      "ptb:note": "on-container"
    },
    "s": "a \"q\" \\ \r\t\u000A é <&> ]]>",
    "@s": {
      "ptb:note": "hello",
      "ptb:lvl": 3,
      "ptb:who": "ptb:der"
    },
    "n": 5,
    "big": "18446744073709551615",
    "i64": "-9223372036854775808",
    "d": "1.5",
    "b": true,
    "e": [null],
    "id": "ptb:der",
    "idl": "own",
    "bits": "x y",
    "bin": "YQ==",
    "en": "two",
    "un": 7,
    "ii": "/pt:c/l[k1='a'][k2='1']/v",
    "ll": [
      "a",
      "b",
      "c"
    ],
    "@ll": [
      null,
      {
        "ptb:note": "second"
      },
      null
    ],
    "ul": [
      "z",
      "y"
    ],
    "ln": [
      3,
      9
    ],
    "l": [
      {
        "k1": "a",
        "k2": 1,
        "v": "x",
        "inner": {
          "x": "1"
        },
        "pta:lx": "lx"
      },
      {
        "@": {
          "ptb:lvl": 1
        },
        "k1": "b",
        "k2": 2,
        "v": "y"
      }
    ],
    "ol": [
      {
        "k": "z"
      },
      {
        "k": "a"
      }
    ],
    "last": "end",
    "pta:ext": "e",
    "pta:ec": {
      "y": "1"
    }
  },
  "pt:top": "t",
  "pta:topa": "ta"
}
`

const printXMLWant = `<c xmlns="urn:vibe-ports:pt" xmlns:ptb="urn:vibe-ports:ptb" ptb:note="on-container">
  <s ptb:note="hello" ptb:lvl="3" ptb:who="ptb:der">a "q" \ ` + "\r\t\n" + ` é &lt;&amp;&gt; ]]&gt;</s>
  <n>5</n>
  <big>18446744073709551615</big>
  <i64>-9223372036854775808</i64>
  <d>1.5</d>
  <b>true</b>
  <e/>
  <id xmlns:ptb="urn:vibe-ports:ptb">ptb:der</id>
  <idl>own</idl>
  <bits>x y</bits>
  <bin>YQ==</bin>
  <en>two</en>
  <un>7</un>
  <ii xmlns:pt="urn:vibe-ports:pt">/pt:c/pt:l[pt:k1='a'][pt:k2='1']/pt:v</ii>
  <ll>a</ll>
  <ll ptb:note="second">b</ll>
  <ll>c</ll>
  <ul>z</ul>
  <ul>y</ul>
  <ln>3</ln>
  <ln>9</ln>
  <l>
    <k1>a</k1>
    <k2>1</k2>
    <v>x</v>
    <inner>
      <x>1</x>
    </inner>
    <lx xmlns="urn:vibe-ports:pta">lx</lx>
  </l>
  <l ptb:lvl="1">
    <k1>b</k1>
    <k2>2</k2>
    <v>y</v>
  </l>
  <ol>
    <k>z</k>
  </ol>
  <ol>
    <k>a</k>
  </ol>
  <last>end</last>
  <ext xmlns="urn:vibe-ports:pta">e</ext>
  <ec xmlns="urn:vibe-ports:pta">
    <y>1</y>
  </ec>
</c>
<top xmlns="urn:vibe-ports:pt">t</top>
<topa xmlns="urn:vibe-ports:pta">ta</topa>
`

func TestPrintOracle(t *testing.T) {
	tr := newPrintFixture(t).build(t)
	var j, x bytes.Buffer
	if err := tr.PrintJSON(&j, PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := tr.PrintXML(&x, PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, got, want string }{{"JSON", j.String(), printJSONWant}, {"XML", x.String(), printXMLWant}} {
		if c.got != c.want {
			g, w := strings.Split(c.got, "\n"), strings.Split(c.want, "\n")
			for i := 0; i < max(len(g), len(w)); i++ {
				if i >= len(g) || i >= len(w) || g[i] != w[i] {
					t.Errorf("%s line %d:\n got  %q\n want %q", c.name, i+1, g[min(i, len(g)-1)], w[min(i, len(w)-1)])
					break
				}
			}
		}
	}
}

func TestPrintEmpty(t *testing.T) {
	var j, x bytes.Buffer
	tr := newTree(&schema.Set{})
	_ = tr.PrintJSON(&j, PrintOptions{})
	_ = tr.PrintXML(&x, PrintOptions{})
	if j.String() != "{}\n" || x.String() != "" {
		t.Errorf("empty tree: %q %q", j.String(), x.String())
	}
	_ = tr.PrintJSON(&j, PrintOptions{Shrink: true})
	if j.String() != "{}\n{}" {
		t.Errorf("shrunk empty tree: %q", j.String())
	}
}

// TestPrintOpaque prints the opaque nodes of the types/print-opaque-xml fixture (libyang v5.8.6
// output, with the module and leaf names of this fixture): an opaque node under its parent's
// module needs no module name or namespace, another namespace is declared as the default one.
func TestPrintOpaque(t *testing.T) {
	f := newPrintFixture(t)
	tr := f.tr
	c := newInner(f.nodes["c"])
	tr.insert(nil, c, insertDefault)
	tr.insert(c, f.term(t, "last", "own"), insertDefault)
	pt := "urn:vibe-ports:pt"
	tr.insert(c, newOpaque(opaque{Name: "unk", ModuleNS: pt, Format: types.FormatXML, Value: "hello"}), insertDefault)
	u2 := newOpaque(opaque{Name: "unk2", ModuleNS: pt, Format: types.FormatXML})
	tr.insert(u2, newOpaque(opaque{Name: "a", ModuleNS: pt, Format: types.FormatXML, Value: "b"}), insertDefault)
	tr.insert(c, u2, insertDefault)
	tr.insert(c, newOpaque(opaque{Name: "x", ModuleNS: "urn:other", Format: types.FormatXML, Value: "v & w"}), insertDefault)
	wantJSON := "{\n  \"pt:c\": {\n    \"last\": \"own\",\n    \"unk\": \"hello\",\n    \"unk2\": {\n      \"a\": \"b\"\n    },\n" +
		"    \"x\": \"v & w\"\n  }\n}\n"
	wantXML := "<c xmlns=\"urn:vibe-ports:pt\">\n  <last>own</last>\n  <unk>hello</unk>\n  <unk2>\n    <a>b</a>\n  </unk2>\n" +
		"  <x xmlns=\"urn:other\">v &amp; w</x>\n</c>\n"
	var j, x bytes.Buffer
	if err := tr.PrintJSON(&j, PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := tr.PrintXML(&x, PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	if j.String() != wantJSON || x.String() != wantXML {
		t.Errorf("JSON:\n%s\nXML:\n%s", j.String(), x.String())
	}
}

// TestPrintShrink: LYD_PRINT_SHRINK drops the newlines, the indentation and the space after the
// member colon (derived from printer_json.c / printer_xml.c: yanglint has no such option); the
// text of a value is untouched.
func TestPrintShrink(t *testing.T) {
	f := newPrintFixture(t)
	tr := f.tr
	c := newInner(f.nodes["c"])
	tr.insert(nil, c, insertDefault)
	tr.insert(c, f.term(t, "n", "5"), insertDefault)
	for _, v := range []string{"b", "a"} {
		tr.insert(c, f.term(t, "ll", v), insertDefault)
	}
	l := newInner(f.nodes["l"])
	tr.insert(l, f.term(t, "k1", "a"), insertDefault)
	tr.insert(l, f.term(t, "k2", "1"), insertDefault)
	tr.insert(c, l, insertDefault)
	tr.insert(c, newInner(f.nodes["ec"]), insertDefault)
	f.note(t, c, "note", "string", "m")
	var j, x bytes.Buffer
	_ = tr.PrintJSON(&j, PrintOptions{Shrink: true})
	_ = tr.PrintXML(&x, PrintOptions{Shrink: true})
	wantJSON := `{"pt:c":{"@":{"ptb:note":"m"},"n":5,"ll":["a","b"],"l":[{"k1":"a","k2":1}],"pta:ec":{}}}`
	wantXML := `<c xmlns="urn:vibe-ports:pt" xmlns:ptb="urn:vibe-ports:ptb" ptb:note="m"><n>5</n><ll>a</ll><ll>b</ll>` +
		`<l><k1>a</k1><k2>1</k2></l><ec xmlns="urn:vibe-ports:pta"/></c>`
	if j.String() != wantJSON || x.String() != wantXML {
		t.Errorf("JSON: %s\nXML: %s", j.String(), x.String())
	}
}
