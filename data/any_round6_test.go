// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

func TestXMLTypedScannerBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		policy     UnknownPolicy
		rc         string
		messages   []string
	}{
		{"string", "<s><![CDATA[\ufffe]]></s>", Reject, "", nil},
		{"string-opaque-mode", "<s><![CDATA[\ufffe]]></s>", Opaque, "", nil},
		{"list-key", "<l><k><![CDATA[\ufffe]]></k></l>", Reject, "", nil},
		{"list-key-opaque-mode", "<l><k><![CDATA[\ufffe]]></k></l>", Opaque, "", nil},
		{"int-multi", "<n><![CDATA[\ufffe]]></n><ll>bad</ll>", Reject, "LY_EVALID", []string{
			"LY_EVALID LYVE_DATA /pj:c/n||1: Invalid type int32 value \"\ufffe\".",
			"LY_EVALID LYVE_DATA /pj:c/ll||1: Invalid type uint8 value \"bad\"."}},
		{"int-opaque-mode", "<n><![CDATA[\ufffe]]></n><ll>bad</ll>", Opaque, "LY_EINVAL", nil},
		{"unknown-opaque", "<unknown><![CDATA[\ufffe]]></unknown>", Opaque, "LY_EINVAL", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, ds, err := parseXMLString(pjSchema(), `<c xmlns="urn:pj">`+tc.body+`</c>`, tc.policy, true)
			if tc.rc == "" {
				if err != nil || len(ds) != 0 {
					t.Fatalf("parse: %v, %+v", err, ds)
				}
				var values []string
				for n := range tr.top.first().All() {
					if n.isTerm() {
						values = append(values, n.value.Canonical())
					}
				}
				if !slices.Equal(values, []string{"\ufffe"}) {
					t.Fatalf("typed values: %q", values)
				}
			} else {
				var ve *ValidationError
				if !errors.As(err, &ve) || ve.RC() != tc.rc {
					t.Fatalf("error: %v; want %s", err, tc.rc)
				}
			}
			var messages []string
			for _, d := range ds {
				messages = append(messages, diagLine(d))
				if d.AppTag != "" {
					t.Fatalf("unexpected app-tag: %+v", d)
				}
			}
			if !slices.Equal(messages, tc.messages) {
				t.Fatalf("diagnostics:\n%q\nwant:\n%q", messages, tc.messages)
			}
		})
	}
}

func round6PluginType(name string) *schema.Type {
	mod := "ietf-yang-types"
	if name == "instance-identifier-keys" {
		mod = "yang"
	}
	return &schema.Type{Base: schema.String, Typedef: name, TypedefModule: &schema.Module{Name: mod}}
}

func TestXMLItemlessStoreDiagnostic(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"xpath1.0", "'a\x01b'"}, {"instance-identifier-keys", "[k='a\x01b']"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			set := pjSchema()
			c := set.Modules[1].Top[0]
			sn := c.Children[0]
			sn.Type = round6PluginType(tc.name)
			tr := newTree(set)
			parent := newInner(c)
			mustLink(t, tr.insert(nil, parent, insertDefault))
			lc := &lydCtx{ctx: context.Background(), tree: tr, log: &logger{set: set}, opts: parseOpts{ParseOptions: ParseOptions{Validate: ValidateOptions{MultiError: true}}}}
			pc := types.XMLNamespaces{Set: set, NS: map[string]string{"": "urn:pj"}}
			n, err := lc.createTerm(sn, parent, tc.value, types.FormatXML, pc, types.HintData)
			var ve *ValidationError
			if n != nil || !lc.fatal(err) || !errors.As(lc.log.done(err), &ve) || ve.RC() != "LY_EINVAL" {
				t.Fatalf("store return: %v, %v", n, err)
			}
			want := []yang.Diagnostic{{Err: "LY_EVALID", Code: "LYVE_OTHER", Msg: "Storing value failed.", SchemaPath: "/pj:c/s"}}
			if !slices.Equal(lc.log.diags, want) {
				t.Fatalf("diagnostics: %+v", lc.log.diags)
			}
		})
	}
}

func TestXMLUnionPrefixScan(t *testing.T) {
	set := pjSchema()
	c := set.Modules[1].Top[0]
	b := &schemaBuilder{t: t, set: set}
	ty := &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.Uint8}, {Base: schema.String}}}
	b.add(c.Module, c, schema.Leaf, "u", ty)
	_, ds, err := parseXMLString(set, "<c xmlns=\"urn:pj\"><s><![CDATA[\ufffe]]></s><u><![CDATA[\ufffe]]></u><n>bad</n></c>", Reject, true)
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
		t.Fatalf("union: %v", err)
	}
	want := []yang.Diagnostic{{Err: "LY_EVALID", Code: "LYVE_OTHER", Msg: "Storing value failed.", SchemaPath: "/pj:c/u", Line: 1}}
	if !slices.Equal(ds, want) {
		t.Fatalf("diagnostics/continuation: %+v", ds)
	}
}

func TestXMLPrintFailure(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        []yang.Diagnostic
	}{
		{"scanner", "'a\x01b'", nil},
		{"unresolved-prefix", "/missing:node", []yang.Diagnostic{{
			Err: "LY_EVALID", Code: "LYVE_DATA", Msg: `Failed to resolve prefix "missing".`,
		}}},
	} {
		for _, union := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/union=%t", tc.name, union), func(t *testing.T) {
				set := pjSchema()
				c := set.Modules[1].Top[0]
				sn := c.Children[0]
				sn.Type = round6PluginType("xpath1.0")
				if union {
					sn.Type = &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.Uint8}, sn.Type}}
				}
				tr := newTree(set)
				if _, err := tr.NewPath("/pj:c/s", tc.value, NewPathOptions{}); err != nil {
					t.Fatal(err)
				}
				var out strings.Builder
				err := tr.PrintXML(&out, PrintOptions{})
				var ve *ValidationError
				if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || !slices.Equal(ve.Diags, tc.want) || out.String() != "<c xmlns=\"urn:pj\">\n" {
					t.Fatalf("print: %q, %#v", out.String(), err)
				}
			})
		}
	}
}

func TestXMLPrintMetadataFailureIsEmpty(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		wantDiag    bool
	}{{"scanner", "'a\x01b'", false}, {"unresolved-prefix", "/missing:node", true}} {
		for _, union := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/union=%t", tc.name, union), func(t *testing.T) {
				set := pjSchema()
				pj := set.Modules[1]
				c := pj.Top[0]
				xp := round6PluginType("xpath1.0")
				ty := xp
				if union {
					ty = &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.Uint8}, xp}}
				}
				v, d := types.Store(ty, tc.value, types.FormatJSON, types.HintData, types.ModuleNames{Set: set}, c)
				if d != nil {
					t.Fatal(d)
				}
				tr := newTree(set)
				n, err := tr.NewPath("/pj:c/s", "value", NewPathOptions{})
				if err != nil {
					t.Fatal(err)
				}
				n.meta = append(n.meta, &meta{mod: pj, name: "ann", value: v})
				var out strings.Builder
				err = tr.PrintXML(&out, PrintOptions{Shrink: true})
				var ve *ValidationError
				if tc.wantDiag {
					want := []yang.Diagnostic{{Err: "LY_EVALID", Code: "LYVE_DATA", Msg: `Failed to resolve prefix "missing".`}}
					if !errors.As(err, &ve) || ve.RC() != "LY_EVALID" || !slices.Equal(ve.Diags, want) {
						t.Fatalf("diagnostic = %v; want %v", err, want)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				want := `<c xmlns="urn:pj" xmlns:pj="urn:pj" pj:ann=""><s>value</s></c>`
				if out.String() != want {
					t.Fatalf("print: %q, want %q", out.String(), want)
				}
			})
		}
	}
}

// lyd_dup_r inserts into a caller-supplied parent using lyd_insert_node; an explicit
// descendant clears Default on the any node and every default-marked ancestor.
func TestAnyDupIntoDefaultParent(t *testing.T) {
	for _, kind := range []schema.Kind{schema.AnyData, schema.AnyXML} {
		for _, siblings := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/siblings=%t", kind, siblings), func(t *testing.T) {
				tr, a, _ := anyFixture()
				mod := a.Module
				outerSchema := &schema.Node{Name: "outer", Kind: schema.Container, Module: mod}
				anySchema := &schema.Node{Name: "value", Kind: kind, Module: mod, Parent: outerSchema}
				outerSchema.Children = []*schema.Node{anySchema}
				mod.Top = append(mod.Top, outerSchema)
				outer := newInner(outerSchema)
				mustLink(t, tr.insert(nil, outer, insertDefault))
				parent := &Node{schema: anySchema, any: &anyValue{}, flags: FlagDefault}
				mustLink(t, tr.insert(outer, parent, insertDefault))
				outer.flags = FlagDefault
				src := newTree(tr.set)
				for _, name := range []string{"one", "two"} {
					mustLink(t, src.insert(nil, newOpaque(opaque{Name: name, Format: types.FormatJSON, set: tr.set}), insertLast))
				}
				dup := tr.Dup
				want := 1
				if siblings {
					dup, want = tr.DupSiblings, 2
				}
				if _, _, err := dup(src.top.first(), parent, DupOptions{}); err != nil {
					t.Fatal(err)
				}
				if parent.flags&FlagDefault != 0 || outer.flags&FlagDefault != 0 || parent.any.children.len() != want {
					t.Fatalf("default flags: parent %b, outer %b; children %d", parent.flags, outer.flags, parent.any.children.len())
				}
			})
		}
	}
}

func TestAnyDupIntoPayload(t *testing.T) {
	for _, kind := range []string{"anydata", "anyxml"} {
		for _, siblings := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/siblings=%t", kind, siblings), func(t *testing.T) {
				tr, a, x := anyFixture()
				sn := a
				if kind == "anyxml" {
					sn = x
				}
				parent := &Node{schema: sn, any: &anyValue{}}
				mustLink(t, tr.insert(nil, parent, insertDefault))
				src := newTree(tr.set)
				for _, name := range []string{"one", "two"} {
					mustLink(t, src.insert(nil, newOpaque(opaque{Name: name, Format: types.FormatJSON, set: tr.set}), insertLast))
				}
				op := tr.Dup
				if siblings {
					op = tr.DupSiblings
				}
				copied, _, err := op(src.top.first(), parent, DupOptions{})
				want := 1
				if siblings {
					want = 2
				}
				if err != nil || copied == nil || copied == src.top.first() || copied.parent != parent || parent.any.children.len() != want || parent.kids.len() != 0 || src.top.len() != 2 {
					t.Fatalf("dup: %v, %v", copied, err)
				}
				for n := range parent.ChildrenAny() {
					if ctxOf(n) != tr.set {
						t.Fatal("wrong payload context")
					}
				}
			})
		}
	}
	// A legitimate schema-backed payload root is exempt from ordinary schema-parent checks.
	tr, a, _ := anyFixture()
	b := &schemaBuilder{t: t, set: tr.set}
	sn := b.add(a.Module, nil, schema.Leaf, "v", &schema.Type{Base: schema.String})
	parent := &Node{schema: a, any: &anyValue{}}
	mustLink(t, tr.insert(nil, parent, insertDefault))
	src := b.term(sn, "v")
	mustLink(t, tr.insert(nil, src, insertDefault))
	if _, _, err := tr.Dup(src, parent, DupOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestAnyDupPayloadContext(t *testing.T) {
	for _, siblings := range []bool{false, true} {
		t.Run(fmt.Sprint(siblings), func(t *testing.T) {
			outer, a, _ := anyFixture()
			payload, _, _ := anyFixture()
			parent := newOpaque(opaque{Name: "parent", Format: types.FormatJSON, set: payload.set})
			mustLink(t, payload.insert(nil, parent, insertDefault))
			owner, _, err := newAnyWork(context.Background(), Budget{}).createAny(outer, a, parent, nil, 0, true, false)
			if err != nil {
				t.Fatal(err)
			}
			mustLink(t, outer.insert(nil, owner, insertDefault))
			sourceA := newOpaque(opaque{Name: "a", Format: types.FormatJSON, set: outer.set})
			mustLink(t, outer.insert(nil, sourceA, insertDefault))
			op := outer.Dup
			if siblings {
				op = outer.DupSiblings
			}
			_, _, err = op(sourceA, parent, DupOptions{})
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || parent.kids.len() != 0 {
				t.Fatalf("mismatch mutated parent: %v", err)
			}
			sourceB := newOpaque(opaque{Name: "b", Format: types.FormatJSON, set: payload.set})
			mustLink(t, payload.insert(nil, sourceB, insertDefault))
			d, err := dupTo(sourceB, nil, parent, 0, siblings)
			if err != nil || d == nil || d.parent != parent || ctxOf(d) != payload.set || d.opaq.set != payload.set || treeOf(d) != outer {
				t.Fatalf("B-to-B duplication: %v, %v", d, err)
			}
		})
	}
}

func TestAnyTextNULBoundary(t *testing.T) {
	for _, action := range []string{"create-copy", "create-transfer", "copy", "dup"} {
		t.Run(action, func(t *testing.T) {
			tr, _, x := anyFixture()
			w := newAnyWork(context.Background(), Budget{})
			text := "a\x00b"
			var n *Node
			var err error
			switch action {
			case "create-copy", "create-transfer":
				n, _, err = w.createAny(tr, x, nil, &text, types.HintString, action == "create-transfer", false)
			case "copy":
				n = &Node{schema: x, payloadSet: tr.set}
				n.refreshHash(nil)
				err = n.copyAnyValue(w, nil, &text, types.HintString)
			case "dup":
				src := &Node{schema: x, any: &anyValue{text: &text, hints: types.HintString}}
				mustLink(t, tr.insert(nil, src, insertDefault))
				n, _, err = tr.Dup(src, nil, DupOptions{})
			}
			if err != nil || n == nil || n.any.text == nil || *n.any.text != "a" {
				t.Fatalf("%s: %v, %v", action, n, err)
			}
			want := anyText(t, tr, x, "a", types.HintString)
			if !n.Equal(want, CompareOptions{}) || text != "a\x00b" {
				t.Fatal("NUL boundary equality or source mutation")
			}
		})
	}
}

func TestAnyNilHelpers(t *testing.T) {
	t.Run("children", func(t *testing.T) {
		var n *Node
		if len(slices.Collect(n.ChildrenAny())) != 0 {
			t.Fatal("nil children")
		}
	})
	t.Run("copy", func(t *testing.T) {
		var n *Node
		var oe *opError
		err := n.copyAnyValue(newAnyWork(context.Background(), Budget{}), nil, nil, 0)
		if !errors.As(err, &oe) || oe.Err != "LY_EINVAL" {
			t.Fatalf("nil copy: %v", err)
		}
	})
}

func TestAnyExactNodeBudget(t *testing.T) {
	tr, a, x := anyFixture()
	w := newAnyWork(context.Background(), Budget{MaxNodes: 1})
	for _, value := range []string{"<n/>", "{}"} {
		if _, _, err := w.createAnyDatatree(tr.set, value); err != nil {
			t.Fatalf("%s: %v", value, err)
		}
	}
	if w.nodes != 1 {
		t.Fatalf("count: %d", w.nodes)
	}
	if _, _, err := w.createAnyDatatree(tr.set, "<extra/>"); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("additional node: %v", err)
	}
	root := &Node{schema: a, any: &anyValue{}}
	mustLink(t, tr.insert(nil, root, insertDefault))
	for _, value := range []string{"<n/>", "{}"} {
		v := value
		mustLink(t, tr.insert(root, &Node{schema: x, any: &anyValue{text: &v}, payloadSet: tr.set}, insertLast))
	}
	d, err := tr.dupWork(root, nil, false, insertDefault, 0, tr.dupDest(nil), newAnyWork(context.Background(), Budget{MaxNodes: 1}))
	if err != nil {
		t.Fatal(err)
	}
	children := d.any.children.schemaSpan().Collect()
	if len(children) != 2 || children[0].anyType() != anyValueDataTree || children[1].any.text == nil || *children[1].any.text != "{}" {
		t.Fatal("node then empty representation")
	}
}

func TestAnyTrimPayloadBudgetAtomic(t *testing.T) {
	for _, tc := range []struct {
		name         string
		depth, links int
	}{{"nodes", 40, 1}, {"links", 2, 80}} {
		t.Run(tc.name, func(t *testing.T) {
			outer, a, _ := anyFixture()
			payload, pa, _ := anyFixture()
			payload.set.LeafrefLinking = true
			b := &schemaBuilder{t: t, set: payload.set}
			sn := b.add(pa.Module, nil, schema.Leaf, "v", &schema.Type{Base: schema.String})
			root := &Node{schema: a, any: &anyValue{}}
			mustLink(t, outer.insert(nil, root, insertDefault))
			parent := root
			for range tc.depth {
				n := &Node{schema: pa, any: &anyValue{}, payloadSet: payload.set}
				mustLink(t, outer.insert(parent, n, insertLast))
				parent = n
			}
			leaf := b.term(sn, "v")
			mustLink(t, outer.insert(parent, leaf, insertLast))
			var outside []*Node
			for range tc.links {
				n := b.term(sn, "v")
				outside = append(outside, n)
				linkLeafrefNode(leaf, n)
			}
			old := trimStepBudget
			t.Cleanup(func() { trimStepBudget = old })
			trimStepBudget = 40
			if _, err := outer.TrimXPath("/*[false()]", XPathOptions{}); !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("budget: %v", err)
			}
			if outer.top.first() != root || leaf.parent != parent || leaf.links == nil || len(leaf.links.leafrefs) != tc.links {
				t.Fatal("failed trim changed tree or links")
			}
			for _, n := range outside {
				if n.links == nil || len(n.links.targets) != 1 || n.links.targets[0] != leaf {
					t.Fatal("failed trim changed outside links")
				}
			}
			trimStepBudget = old
			if _, err := outer.TrimXPath("/*[false()]", XPathOptions{}); err != nil {
				t.Fatal(err)
			}
			if outer.top.len() != 0 || leaf.links != nil {
				t.Fatal("nested payload links survived trim")
			}
			for _, n := range outside {
				if n.links != nil {
					t.Fatal("outside links survived trim")
				}
			}
		})
	}
}

func TestAnyWideComparisonWork(t *testing.T) {
	for _, width := range []int{64, 256, 1024} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			tr, a, _ := anyFixture()
			b := &schemaBuilder{t: t, set: tr.set}
			sn := b.add(a.Module, nil, schema.LeafList, "v", &schema.Type{Base: schema.String})
			makeAny := func(dst *Tree, reverse bool) *Node {
				n := &Node{schema: a, any: &anyValue{}}
				mustLink(t, dst.insert(nil, n, insertDefault))
				// Deliberately reverse physical input order; lookup must find the first matching value.
				for i := range width {
					j := i
					if reverse {
						j = width - i - 1
					}
					c := b.term(sn, fmt.Sprint(j))
					c.parent = n
					c.payloadSet = tr.set
					mustLink(t, n.any.children.appendNode(c))
				}
				n.any.children.work = &dst.work
				return n
			}
			src := newTree(tr.set)
			left, right := makeAny(tr, false), makeAny(src, true)
			tr.work.visits = true
			src.work.visits = true
			tr.work.Store(0)
			src.work.Store(0)
			if !left.Equal(right, CompareOptions{}) {
				t.Fatal("equal reversed forest")
			}
			if work := tr.work.Load() + src.work.Load(); work > int64(12*width) {
				t.Fatalf("quadratic equality: width=%d work=%d", width, work)
			}
			kept := left.any.children.first()
			tr.work.Store(0)
			src.work.Store(0)
			if err := tr.Merge(src); err != nil {
				t.Fatal(err)
			}
			if left.any.children.first() != kept {
				t.Fatal("equal Merge replaced payload")
			}
			if work := tr.work.Load() + src.work.Load(); work > int64(16*width) {
				t.Fatalf("quadratic Merge: %d", work)
			}
		})
	}
}

func TestAnyComparisonFirstMatch(t *testing.T) {
	// Colliding canonical strings from different union members and duplicate
	// instances must match as findFirst does, without consuming a matched node.
	tr, a, _ := anyFixture()
	b := &schemaBuilder{t: t, set: tr.set}
	ty := &schema.Type{Base: schema.Union, Union: []*schema.Type{{Base: schema.Uint8}, {Base: schema.String}}}
	sn := b.add(a.Module, nil, schema.LeafList, "u", ty)
	makeNode := func(h types.Hints) *Node {
		v, d := types.Store(ty, "1", types.FormatJSON, h, nil, sn)
		if d != nil {
			t.Fatal(d)
		}
		return newTerm(sn, v)
	}
	for _, cross := range []bool{false, true} {
		t.Run(fmt.Sprint(cross), func(t *testing.T) {
			var other *schema.Node
			if cross {
				dst, da, _ := anyFixture()
				db := &schemaBuilder{t: t, set: dst.set}
				// The same union shape in the other context: lyd_hash covers the
				// member index with the value's LYB form, so a string member never
				// hashes like a numeric one across contexts either.
				other = db.add(da.Module, nil, schema.LeafList, "u", &schema.Type{Base: schema.Union,
					Union: []*schema.Type{{Base: schema.Uint8}, {Base: schema.String}}})
			}
			left, right := &Node{schema: a, any: &anyValue{}}, &Node{schema: a, any: &anyValue{}}
			mustLink(t, tr.insert(nil, left, insertLast))
			mustLink(t, tr.insert(nil, right, insertLast))
			for i := range 128 {
				h := types.JSONHints("number")
				if i%2 != 0 {
					h = types.JSONHints("string")
				}
				x, y := makeNode(h), makeNode(h)
				if cross {
					y.schema = other
					v, d := types.Store(other.Type, "1", types.FormatJSON, h, nil, other)
					if d != nil {
						t.Fatal(d)
					}
					y.value = v
					y.payloadSet = &schema.Set{}
				}
				x.parent = left
				y.parent = right
				mustLink(t, left.any.children.appendNode(x))
				mustLink(t, right.any.children.appendNode(y))
			}
			if !left.Equal(right, CompareOptions{}) {
				t.Fatal("union/cross-context canonical match")
			}
			// The second duplicate is default; first-match lookup must keep choosing
			// the first nondefault instance, so comparing defaults rejects equality.
			left.any.children.At(2).flags = FlagDefault
			if left.Equal(right, CompareOptions{Defaults: true}) {
				t.Fatal("duplicate lookup consumed the first match")
			}
		})
	}
	// Lists with equal keys but different non-key children also retain first-match
	// behavior: later duplicates never override the first sibling's full subtree.
	list := b.add(a.Module, nil, schema.List, "list", nil)
	key := b.add(a.Module, list, schema.Leaf, "key", &schema.Type{Base: schema.String})
	list.Keys = []*schema.Node{key}
	val := b.add(a.Module, list, schema.Leaf, "value", &schema.Type{Base: schema.String})
	left, right := &Node{schema: a, any: &anyValue{}}, &Node{schema: a, any: &anyValue{}}
	mustLink(t, tr.insert(nil, left, insertLast))
	mustLink(t, tr.insert(nil, right, insertLast))
	for _, owner := range []*Node{left, right} {
		for _, text := range []string{"first", "second"} {
			n := newInner(list)
			mustLink(t, tr.insert(owner, n, insertLast))
			mustLink(t, tr.insert(n, b.term(key, "same"), insertLast))
			mustLink(t, tr.insert(n, b.term(val, text), insertLast))
		}
	}
	if left.Equal(right, CompareOptions{}) {
		t.Fatal("duplicate keyed lists must compare against first matching sibling")
	}
}

func TestCompareDateCanonicalCollision(t *testing.T) {
	tr := newTree(&schema.Set{})
	b := &schemaBuilder{t: t, set: tr.set}
	m := b.module("dates")
	c := b.add(m, nil, schema.Container, "c", nil)
	dt := &schema.Type{Base: schema.String, Typedef: "date", TypedefModule: &schema.Module{Name: "ietf-yang-types"}}
	dl := b.add(m, c, schema.LeafList, "dl", dt)
	makeContainer := func(values ...string) *Node {
		n := &Node{schema: c}
		for _, value := range values {
			leaf := b.term(dl, value)
			leaf.parent = n
			mustLink(t, n.kids.appendNode(leaf))
		}
		return n
	}
	a := makeContainer("2024-01-01+01:00", "2023-12-31+00:00")
	both := makeContainer("2023-12-31+00:00", "2024-01-01+01:00")
	if a.kids.At(0).value.Canonical() != a.kids.At(1).value.Canonical() ||
		types.Equal(a.kids.At(0).value, a.kids.At(1).value) {
		t.Fatal("fixture must have equal canonical text and distinct stored values")
	}
	if !a.Equal(both, CompareOptions{FullRecursion: true}) {
		t.Fatal("reordered distinct date values compare unequal")
	}
	duplicate := makeContainer("2024-01-01+01:00", "2024-01-01+01:00")
	if a.Equal(duplicate, CompareOptions{FullRecursion: true}) {
		t.Fatal("distinct date values compare equal to a repeated value")
	}
	list := b.add(m, c, schema.List, "dated", nil)
	key := b.add(m, list, schema.Leaf, "key", dt)
	list.Keys = []*schema.Node{key}
	makeLists := func(values ...string) *Node {
		n := &Node{schema: c}
		for _, value := range values {
			entry := &Node{schema: list, parent: n}
			leaf := b.term(key, value)
			leaf.parent = entry
			mustLink(t, entry.kids.buildSchema([]*Node{leaf}))
			mustLink(t, n.kids.appendNode(entry))
		}
		return n
	}
	if !makeLists("2024-01-01+01:00", "2023-12-31+00:00").Equal(
		makeLists("2023-12-31+00:00", "2024-01-01+01:00"), CompareOptions{FullRecursion: true}) {
		t.Fatal("reordered list keys with colliding canonical dates compare unequal")
	}

	// Equal has only a boolean result, so its read-only comparison cannot turn a
	// private work-budget failure into inequality. Each value in the second half
	// scans past all the canonically equal but stored-distinct values in the first.
	// Under the race detector the quadratic scan uses a tenth of the values and a
	// lowered private budget, so the budget failure stays reachable.
	anySchema := b.add(m, nil, schema.AnyData, "payload", nil)
	payload := &Node{schema: anySchema, any: &anyValue{}}
	per := uint64(6000)
	if raceEnabled {
		per = 600
	}
	for _, value := range []string{"2024-01-01+01:00", "2023-12-31+00:00"} {
		for range per {
			leaf := b.term(dl, value)
			leaf.parent = payload
			mustLink(t, payload.any.children.appendNode(leaf))
		}
	}
	if payload.any.children.ht != nil {
		t.Fatal("anydata payload unexpectedly acquired a children table")
	}
	if !payload.Equal(payload, CompareOptions{}) {
		t.Fatal("repeated canonical-collision payload compares unequal to itself")
	}
	compare := compareSingleErr
	if raceEnabled {
		compare = func(t *Tree, a, b *Node, full bool) (bool, error) {
			work := workCounter{hashLimit: per * per / 2}
			return compareSingleWork(t, &work, a, b, full)
		}
	}
	if equal, err := compare(&Tree{}, payload, payload, false); equal || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("charged repeated comparison = (%t, %v), want false and ErrBudget", equal, err)
	}
}

func TestCompareCrossContextInstanceOrdering(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      schema.Kind
		rhsConfig bool
	}{
		{"leaf-list user", schema.LeafList, true},
		{"leaf-list state user", schema.LeafList, false},
		{"keyed list user", schema.List, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			makeTree := func(userOrdered, config bool) (*Tree, *Node) {
				set := &schema.Set{}
				b := &schemaBuilder{t: t, set: set}
				mod := b.module("order")
				container := b.add(mod, nil, schema.Container, "c", nil)
				inst := b.add(mod, container, tc.kind, "item", &schema.Type{Base: schema.String})
				inst.UserOrdered = userOrdered
				inst.Config = config
				if tc.kind == schema.List {
					key := b.add(mod, inst, schema.Leaf, "key", &schema.Type{Base: schema.String})
					inst.Keys = []*schema.Node{key}
				}
				tr := newTree(set)
				root := newInner(container)
				mustLink(t, tr.insert(nil, root, insertDefault))
				for _, value := range []string{"b", "a"} {
					if tc.kind == schema.LeafList {
						mustLink(t, tr.insert(root, b.term(inst, value), insertDefault))
					} else {
						item := newInner(inst)
						mustLink(t, tr.insert(item, b.term(inst.Keys[0], value), insertDefault))
						mustLink(t, tr.insert(root, item, insertDefault))
					}
				}
				if root.kids.ht != nil {
					t.Fatal("expected lookup without a children hash table")
				}
				return tr, root
			}
			_, system := makeTree(false, true)
			_, user := makeTree(true, tc.rhsConfig)
			if !system.Equal(user, CompareOptions{FullRecursion: true}) {
				t.Fatal("system-ordered LHS did not find user-ordered RHS instances")
			}
			if user.Equal(system, CompareOptions{FullRecursion: true}) {
				t.Fatal("user-ordered LHS ignored instance order")
			}
		})
	}
}
