// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"math"
	"slices"
	"strconv"
	"strings"
)

// In-memory data tree implementing Node for the tests.

type tnode struct {
	name, mod string
	parent    *tnode
	kids      []Node
	sch       *tschema
	val       *tval
	when      WhenState
}

// tschema is shared by all instances of a schema node (top merges them).
type tschema struct {
	kind      Kind
	mod, name string
	config    bool
	keys      []string
	canon     func(string) (string, bool)
	kids      map[[2]string]*tschema
	parent    *tschema
	order     []SchemaNode // Children()
	ops       [2][]SchemaNode
	lref      *tschema
	typ       *ttype
	check     func(string) (string, bool) // CheckValue; nil accepts everything
}

type tval struct {
	str   string
	ident *Ident
	enum  *int
	bits  []string
}

func (n *tnode) Parent() Node {
	if n.parent == nil {
		return nil
	}
	return n.parent
}
func (n *tnode) Children() []Node { return n.kids }
func (n *tnode) Name() string     { return n.name }
func (n *tnode) Module() string   { return n.mod }
func (n *tnode) Schema() SchemaNode {
	if n.sch == nil {
		return nil
	}
	return n.sch
}
func (n *tnode) Value() Value {
	if n.val == nil {
		return nil
	}
	return n.val
}
func (n *tnode) When() WhenState { return n.when }

func (s *tschema) Kind() Kind     { return s.kind }
func (s *tschema) Module() string { return s.mod }
func (s *tschema) Keys() []string { return s.keys }
func (s *tschema) Config() bool   { return s.config }
func (s *tschema) Child(mod, name string) SchemaNode {
	if c := s.kids[[2]string{mod, name}]; c != nil {
		return c
	}
	return nil
}
func (s *tschema) Name() string { return s.name }
func (s *tschema) Parent() SchemaNode {
	if s.parent == nil {
		return nil
	}
	return s.parent
}
func (s *tschema) Children() []SchemaNode      { return s.order }
func (s *tschema) Actions() []SchemaNode       { return s.ops[0] }
func (s *tschema) Notifications() []SchemaNode { return s.ops[1] }
func (s *tschema) Type() SchemaType {
	if s.typ == nil {
		if s.kind == KindLeaf || s.kind == KindLeafList {
			return &ttype{base: TypeString} // fixtures that do not care about types
		}
		return nil
	}
	return s.typ
}
func (s *tschema) CheckValue(v string, _ NamespaceCtx) (string, bool) {
	if s.check == nil {
		return "", true
	}
	return s.check(v)
}
func (s *tschema) LeafrefTarget() SchemaNode {
	if s.lref == nil {
		return nil
	}
	return s.lref
}

// Path is lysc_path(LYSC_PATH_LOG): choice/case and input/output included.
func (s *tschema) Path() string {
	if s.parent == nil {
		return "/" + s.mod + ":" + s.name
	}
	seg := "/"
	if s.parent.mod != s.mod {
		seg += s.mod + ":"
	}
	return s.parent.Path() + seg + s.name
}

// add links c under s as a data child, action or notification.
func (s *tschema) add(c *tschema) *tschema {
	c.parent = s
	switch c.kind {
	case KindAction:
		s.ops[0] = append(s.ops[0], c)
	case KindNotif:
		s.ops[1] = append(s.ops[1], c)
	default:
		s.order = append(s.order, c)
	}
	return c
}

func (s *tschema) Namespace() string { return "urn:vibe-ports:yang:conformance:pv2" }
func (s *tschema) Canonical(v string) (string, bool) {
	if s.canon == nil {
		return "", false
	}
	return s.canon(v)
}

func (v *tval) String() string { return v.str }
func (v *tval) Identity() (string, string, bool) {
	if v.ident == nil {
		return "", "", false
	}
	return v.ident.Module, v.ident.Name, true
}
func (v *tval) Enum() (int, bool) {
	if v.enum == nil {
		return 0, false
	}
	return *v.enum, true
}
func (v *tval) Bits() ([]string, bool) { return v.bits, v.bits != nil }

// node builders; "mod:name" sets the module, otherwise it is inherited.
func mk(kind Kind, name string, val *tval, kids ...*tnode) *tnode {
	n := &tnode{name: name, sch: &tschema{kind: kind, config: true}, val: val}
	if m, l, ok := strings.Cut(name, ":"); ok {
		n.mod, n.name = m, l
	}
	for _, k := range kids {
		k.parent = n
		n.kids = append(n.kids, k)
	}
	return n
}
func cont(name string, kids ...*tnode) *tnode { return mk(KindContainer, name, nil, kids...) }
func list(name string, kids ...*tnode) *tnode { return mk(KindList, name, nil, kids...) }
func leaf(name, v string) *tnode              { return mk(KindLeaf, name, &tval{str: v}) }
func leafl(name, v string) *tnode             { return mk(KindLeafList, name, &tval{str: v}) }

func keyed(n *tnode, keys ...string) *tnode { n.sch.keys = keys; return n }

// top links a tree: inherits modules, merges the schema nodes of all
// instances (first wins), returns the top-level siblings.
func top(roots ...*tnode) []Node {
	tops := map[[2]string]*tschema{}
	var fix func(n *tnode, mod string, reg map[[2]string]*tschema, parent *tschema)
	fix = func(n *tnode, mod string, reg map[[2]string]*tschema, parent *tschema) {
		if n.mod == "" {
			n.mod = mod
		}
		k := [2]string{n.mod, n.name}
		if sh := reg[k]; sh != nil {
			n.sch = sh
		} else {
			n.sch.mod, n.sch.name, n.sch.kids = n.mod, n.name, map[[2]string]*tschema{}
			reg[k] = n.sch
			if parent != nil {
				parent.add(n.sch)
			}
		}
		for _, c := range n.kids {
			fix(c.(*tnode), n.mod, n.sch.kids, n.sch)
		}
	}
	out := make([]Node, len(roots))
	for i, r := range roots {
		fix(r, "", tops, nil)
		out[i] = r
	}
	return out
}

// find returns the first node named name in preorder.
func find(tree []Node, name string, nth int) *tnode {
	var res *tnode
	var walk func(ns []Node)
	walk = func(ns []Node) {
		for _, n := range ns {
			if res != nil {
				return
			}
			if n.Name() == name {
				if nth == 0 {
					res = n.(*tnode)
					return
				}
				nth--
			}
			walk(n.Children())
		}
	}
	walk(tree)
	return res
}

// path is lyd_path(LYD_PATH_STD).
func path(n Node) string {
	if n == nil {
		return ""
	}
	p := path(n.Parent())
	seg := "/"
	if n.Parent() == nil || n.Parent().Module() != n.Module() {
		seg += n.Module() + ":"
	}
	seg += n.Name()
	switch n.Schema().Kind() {
	case KindList:
		var key *tnode
		for _, k := range n.Children() {
			if k.Name() == "k" {
				key = k.(*tnode)
			}
		}
		if key != nil {
			seg += "[k='" + key.val.str + "']"
		} else {
			i := 1
			for _, s := range n.Parent().Children() {
				if s == n {
					break
				}
				if s.Name() == n.Name() {
					i++
				}
			}
			seg += "[" + strconv.Itoa(i) + "]"
		}
	case KindLeafList:
		seg += "[.='" + n.Value().String() + "']"
	}
	return p + seg
}

// jsonNS is the LY_VALUE_JSON namespace context: prefixes are module names.
type jsonNS map[string]bool

func (m jsonNS) Resolve(p string) (string, bool) { return p, m[p] }
func (m jsonNS) Prefix(mod string) string        { return mod }
func (m jsonNS) Default() string                 { return "" }

// tinfo is SchemaInfo over the pv2 identities and the tree's top-level nodes.
type tinfo struct {
	tree   []Node
	schema []SchemaNode // top-level schema nodes (Atomize tests); nil = those of tree
}

var pv2Idents = map[Ident]Ident{{"pv2", "base-id"}: {}, {"pv2", "one"}: {"pv2", "base-id"}, {"pv2", "two"}: {"pv2", "one"}}

func (tinfo) HasIdentity(id Ident) bool { _, ok := pv2Idents[id]; return ok }
func (tinfo) IsDerived(base, id Ident) bool {
	for b, ok := pv2Idents[id]; ok && b != (Ident{}); b, ok = pv2Idents[b] {
		if b == base {
			return true
		}
	}
	return false
}
func (t tinfo) TopLevel(mod, name string) []SchemaNode {
	var out []SchemaNode
	for _, n := range t.tree {
		if sn := n.Schema(); n.Name() == name && (mod == "" || n.Module() == mod) && !slices.Contains(out, sn) {
			out = append(out, sn)
		}
	}
	return out
}

// tops is the top-level schema nodes: of the tree, or t.schema when set.
func (t tinfo) tops() []SchemaNode {
	if t.schema != nil {
		return t.schema
	}
	var out []SchemaNode
	for _, n := range t.tree {
		if sn := n.Schema(); sn != nil && !slices.Contains(out, sn) {
			out = append(out, sn)
		}
	}
	return out
}

func (t tinfo) Modules() []string {
	var out []string
	for _, sn := range t.tops() {
		if !slices.Contains(out, sn.Module()) {
			out = append(out, sn.Module())
		}
	}
	return out
}

func (t tinfo) ModuleNodes(mod string) (data, rpcs, notifs []SchemaNode) {
	for _, sn := range t.tops() {
		switch {
		case sn.Module() != mod:
		case sn.Kind() == KindRPC:
			rpcs = append(rpcs, sn)
		case sn.Kind() == KindNotif:
			notifs = append(notifs, sn)
		default:
			data = append(data, sn)
		}
	}
	return data, rpcs, notifs
}

func uintCanon(s string) (string, bool) {
	u, err := strconv.ParseUint(s, 10, 8)
	if err != nil {
		return "", false
	}
	return strconv.FormatUint(u, 10), true
}

func pintp(i int) *int { return &i }

// pv2Tree mirrors conformance/corpus/protocol-v2/data/xpath.json as libyang
// parses it (parse-only: no implicit defaults).
func pv2Tree() []Node {
	mode := leaf("mode", "on")
	mode.val.enum = pintp(0)
	id := leaf("id", "pv2:two")
	id.val.ident = &Ident{"pv2", "two"}
	id.sch.canon = func(s string) (string, bool) {
		if !strings.Contains(s, ":") {
			s = "pv2:" + s
		}
		return s, true
	}
	dec := leaf("dec", "2.5")
	dec.sch.canon = func(s string) (string, bool) {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return "", false
		}
		return strconv.FormatFloat(f, 'f', -1, 64), true
	}
	u2 := leaf("u2", "50")
	u2.sch.canon = uintCanon
	flags := leaf("flags", "x y")
	flags.val.bits = []string{"x", "y"}
	stats := []*tnode{list("stats", leaf("hits", "5")), list("stats", leaf("hits", "7"))}
	for _, s := range stats {
		s.sch.config = false
		s.kids[0].(*tnode).sch.config = false
		s.kids[0].(*tnode).sch.canon = uintCanon
	}
	return top(cont("pv2:c",
		mode, leaf("x", "hi"), leaf("u", "abc"), dec, id,
		keyed(list("l", leaf("k", "a")), "k"), list("l", leaf("k", "b")), list("l", leaf("k", "c")),
		stats[0], stats[1],
		leaf("grouped", "g1"), leaf("ref", "b"), u2,
		leafl("ll", "x"), leafl("ll", "y"), leafl("ll", "z"), flags))
}

// unionTree mirrors protocol-v2/data/xpath-union.json (module pv2-xp):
// union members with identical canonical text.
func unionTree() []Node {
	idFirst := leaf("id-first", "pv2:two")
	idFirst.val.ident = &Ident{"pv2", "two"}
	strFirst := leaf("str-first", "pv2:two") // string member, same text
	en := leaf("en", "red")                  // enum member of a union
	bi := leaf("bi", "red")
	bi.val.bits = []string{"red"}
	return top(cont("pv2-xp:xp", idFirst, strFirst, en, bi))
}

// augTree mirrors protocol-v2/data/xpath-aug.json: pv2-aug augments c with
// its own "grouped" and "extra", pv2-xp with a keyed list "l".
func augTree() []Node {
	return top(cont("pv2:c", leaf("mode", "on"),
		keyed(list("l", leaf("k", "a")), "k"), list("l", leaf("k", "b")),
		leaf("grouped", "g1"), leaf("ref", "b"), leafl("ll", "b"), leafl("ll", "z"),
		leaf("pv2-aug:extra", "e"), leaf("pv2-aug:grouped", "g2"),
		keyed(list("pv2-xp:l", leaf("k", "b")), "k"), list("pv2-xp:l", leaf("k", "c"))))
}

// pv2Deref resolves the leafrefs of pv2 (ref → ../l/k; u2 holds a percent).
func pv2Deref(tree []Node) func(Node) ([]Node, error) {
	return func(n Node) ([]Node, error) {
		if n.Name() != "ref" {
			return nil, nil
		}
		var out []Node
		for i := 0; find(tree, "k", i) != nil; i++ {
			if k := find(tree, "k", i); k.val.str == n.Value().String() {
				out = append(out, k)
			}
		}
		return out, nil
	}
}

// resultJSON renders a result like lyoracle.
func resultJSON(r Result) map[string]any {
	switch r.Type {
	case Boolean:
		return map[string]any{"type": "boolean", "value": r.Bool}
	case String:
		return map[string]any{"type": "string", "value": r.Str}
	case Number:
		var v any = r.Num
		switch {
		case r.Num != r.Num:
			v = "NaN"
		case math.IsInf(r.Num, 1):
			v = "Infinity"
		case math.IsInf(r.Num, -1):
			v = "-Infinity"
		}
		return map[string]any{"type": "number", "value": v}
	}
	nodes := []any{}
	for _, n := range r.Nodes {
		nodes = append(nodes, path(n))
	}
	return map[string]any{"type": "node-set", "nodes": nodes}
}
