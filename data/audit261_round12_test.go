// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/vibe-ports/yang"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/contextopts"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
)

func round12Set(t *testing.T, extra bool, modules ...string) *schema.Set {
	t.Helper()
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(filepath.Join(pv2Dir, "schemas")))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range modules {
		var features []string
		if extra && name == "pv2-detached" {
			features = []string{"extra"}
		}
		if _, ds, err := c.Load(name, "", features); err != nil {
			t.Fatalf("load %s: %v (%v)", name, err, ds)
		}
	}
	return c.Snapshot()
}

func round12Tree(t *testing.T, set *schema.Set, input string) *Tree {
	t.Helper()
	tr, ds, err := parseJSONString(set, input, Reject, false)
	if err != nil {
		t.Fatalf("parse: %v (%v)", err, ds)
	}
	return tr
}

func TestInternalDupRetainsEachSourceRootContext(t *testing.T) {
	a := round12Set(t, false, "pv2-detached", "pv2-edit")
	b := round12Set(t, true, "pv2-detached")
	mixed := round12Tree(t, a, `{"pv2-detached:l":[{"k":"keep"}]}`)
	source := round12Tree(t, b, `{"pv2-detached:c":{"extra":"x"}}`)
	foreign := source.top.first()
	_ = unlink(foreign)
	if err := mixed.link(nil, &mixed.top, foreign, mixed.top.SchemaLen()); err != nil {
		t.Fatal(err)
	}
	if ctxOf(mixed.top.first()) != a {
		t.Fatal("A must remain the first root")
	}
	if _, err := Diff(nil, mixed, DiffOptions{}); err != nil {
		t.Fatalf("Diff of mixed roots: %v", err)
	}
	target := newTree(a)
	if err := target.Merge(mixed); err != nil {
		t.Fatalf("Merge of mixed roots: %v", err)
	}
	if n := target.schemaLookup(&target.top, foreign.schema); n == nil || ctxOf(n) != b {
		t.Fatalf("foreign root was converted into the first root's context: %v", n)
	}
}

// protocol-v2/merge-foreign-child records lyd_merge_siblings with a source
// context that enables extra and a destination context that does not.
func TestMergeRetainsForeignChildContext(t *testing.T) {
	a := round12Set(t, false, "pv2-detached")
	b := round12Set(t, true, "pv2-detached")
	target := round12Tree(t, a, `{"pv2-detached:l":[{"k":"a"}],"pv2-detached:c":{"base":"old"}}`)
	source := round12Tree(t, a, `{"pv2-detached:l":[{"k":"a"}]}`)
	foreignTree := round12Tree(t, b, `{"pv2-detached:c":{"extra":"x"}}`)
	foreign := foreignTree.top.first()
	_ = unlink(foreign)
	if err := source.link(nil, &source.top, foreign, source.top.len()); err != nil {
		t.Fatal(err)
	}
	if err := target.Merge(source); err != nil {
		t.Fatalf("merge of foreign child: %v", err)
	}
	c := target.schemaLookup(&target.top, foreign.schema)
	if c == nil || ctxOf(c) != a {
		t.Fatalf("matched destination parent changed context: %v", c)
	}
	if c.kids.SchemaLen() != 2 || c.kids.At(1).Name() != "base" {
		t.Fatalf("foreign child must precede the destination child: %v", c.kids.schemaSpan().Collect())
	}
	extra := c.kids.At(0)
	if extra.Name() != "extra" || extra.Value() != "x" || ctxOf(extra) != b {
		t.Fatalf("unmatched child did not retain source context: %v", extra)
	}
}

func TestDetachedOpaqueDiffSiblingLookup(t *testing.T) {
	set := round12Set(t, false, "pv2-detached")
	opaq := parseOnlyXML(t, set, `<u xmlns="urn:unknown">x</u>`).top.first()
	list := round12Tree(t, set, `{"pv2-detached:l":[{"k":"a"}]}`).top.first()
	for _, pair := range [][2]*Node{{opaq, list}, {list, opaq}} {
		for _, diff := range []func(*Node, *Node, DiffOptions) (*Tree, error){DiffTree, DiffSiblings} {
			if _, err := diff(pair[0], pair[1], DiffOptions{}); err != nil {
				t.Errorf("detached opaque diff: %v", err)
			}
		}
	}
}

func TestForeignPositionalFirstInstanceStops(t *testing.T) {
	a, b := round12Set(t, false, "pv2-detached"), round12Set(t, false, "pv2-detached")
	target := round12Tree(t, a, `{"pv2-detached:sll":["a","b"]}`)
	diff, err := Diff(nil, round12Tree(t, b, `{"pv2-detached:l":[{"k":"b"}]}`), DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	if ctxOf(target.top.first()) != b {
		t.Fatal("foreign root must supply the path schema")
	}
	if n, err := target.Find("/pv2-detached:sll[1]"); err != nil || n != nil {
		t.Fatalf("foreign positional lookup = %v, %v", n, err)
	}
}

func TestEmptyAnchorChecksOnlySelectedInstance(t *testing.T) {
	a, b := round12Set(t, false, "pv2-detached"), round12Set(t, false, "pv2-detached")
	target := round12Tree(t, a, `{"pv2-detached:ll":["a"]}`)
	other := round12Tree(t, b, `{"pv2-detached:ll":["b"]}`)
	foreign := other.top.first()
	_ = unlink(foreign)
	if err := target.link(nil, &target.top, foreign, target.top.SchemaLen()); err != nil {
		t.Fatal(err)
	}
	diff := parseOnlyXML(t, a, `<ll xmlns="urn:vibe-ports:yang:conformance:pv2-detached"`+
		` xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="create" yang:value="">c</ll>`)
	if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatalf("empty anchor must use A:a: %v", err)
	}
	if target.top.At(0).Value() != "c" || target.top.At(1).Value() != "a" || target.top.At(2).Value() != "b" {
		t.Fatalf("ordered run: %v", target.top.schemaSpan().Collect())
	}
}

func TestDuplicateInstanceCacheIncludesForeignEqualSibling(t *testing.T) {
	a, b := round12Set(t, false, "pv2-detached"), round12Set(t, false, "pv2-detached")
	target := round12Tree(t, a, `{"pv2-detached:sll":["x"]}`)
	other := round12Tree(t, b, `{"pv2-detached:sll":["x"]}`)
	foreign := other.top.first()
	_ = unlink(foreign)
	if err := target.link(nil, &target.top, foreign, target.top.SchemaLen()); err != nil {
		t.Fatal(err)
	}
	first := target.top.At(0)
	cache := &dupCache{}
	if got, err := target.dupInstNext(first, cache); err != nil || got != first {
		t.Fatalf("first instance: %v, %v", got, err)
	}
	if got, err := target.dupInstNext(first, cache); err != nil || got != foreign {
		t.Fatalf("second equal foreign instance: %v, %v", got, err)
	}
}

func TestDuplicateInstanceCacheIncludesForeignEqualSiblingWithTable(t *testing.T) {
	module := `module m { namespace "urn:m"; prefix m;
	  container c {
	    leaf a { type string; } leaf b { type string; } leaf d { type string; }
	    leaf-list ll { config false; type string; }
	  }
	}`
	a, b := opIndexSet(t, module), opIndexSet(t, module)
	for _, mod := range a.Modules {
		mod.Owner = a
	}
	for _, mod := range b.Modules {
		mod.Owner = b
	}
	target := round12Tree(t, a, `{"m:c":{"a":"a","b":"b","d":"d","ll":["x"]}}`)
	foreignTree := round12Tree(t, b, `{"m:c":{"ll":["x"]}}`)
	parent := target.top.first()
	first := parent.kids.At(parent.kids.SchemaLen() - 1)
	if parent.kids.ht == nil {
		t.Fatal("four schema children must have a hash table")
	}
	foreign := foreignTree.top.first().kids.At(0)
	_ = unlink(foreign)
	if err := target.link(parent, &parent.kids, foreign, parent.kids.SchemaLen()-1); err != nil {
		t.Fatal(err)
	}
	if parent.kids.At(parent.kids.SchemaLen()-2) != foreign {
		t.Fatal("foreign equal instance was not prepended")
	}
	cache := &dupCache{}
	if got, err := target.dupInstNext(first, cache); err != nil || got != first {
		t.Fatalf("first registered equal instance = %v, %v", got, err)
	}
	if got, err := target.dupInstNext(first, cache); err != nil || got != foreign {
		t.Fatalf("later registered foreign equal instance = %v, %v", got, err)
	}
}

func round12VariantSet(t *testing.T, variant string, modules ...string) *schema.Set {
	t.Helper()
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true},
		os.DirFS(filepath.Join(pv2Dir, "schemas", variant)))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range modules {
		if _, ds, err := c.Load(name, "", nil); err != nil {
			t.Fatalf("load %s: %v (%v)", name, err, ds)
		}
	}
	return c.Snapshot()
}

// protocol-v2/compare-bits-unused-same-byte observes lyd_hash's byte-rounded
// bitmap. The extra unselected bit has no LYB representation.
func TestCrossContextBitsUnusedSameByte(t *testing.T) {
	a := round12VariantSet(t, "bits-a", "pv2-bytebits")
	b := round12VariantSet(t, "bits-b", "pv2-bytebits")
	left := round12Tree(t, a, `{"pv2-bytebits:v":["a"]}`)
	right := round12Tree(t, b, `{"pv2-bytebits:v":["a"]}`)
	if !left.top.first().Equal(right.top.first(), CompareOptions{}) {
		t.Fatal("same native one-byte bitmap must compare equal")
	}
	if got := left.findFirst(&left.top, right.top.first()); got != left.top.first() {
		t.Fatalf("cross-context value lookup missed equal bitmap: %v", got)
	}
}

func TestForeignSchemaLinearLookupAndOperationChild(t *testing.T) {
	a, b := round12VariantSet(t, "ctx-a", "aa", "pv2-ctxshape"),
		round12VariantSet(t, "ctx-b", "aa", "pv2-ctxshape")
	keep := round12Tree(t, b, `{"aa:keep":"b"}`)
	diff, err := Diff(nil, keep, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	root := round12Tree(t, a, `{"pv2-ctxshape:x":"a"}`)
	if err := root.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	if n, err := root.Find("/pv2-ctxshape:x"); err != nil || n == nil || ctxOf(n) != a {
		t.Fatalf("choice child after foreign root: %v, %v", n, err)
	}
	op, ds, err := ParseOp(context.Background(), strings.NewReader(`{"pv2-ctxshape:r":{"x":"a"}}`),
		FormatJSON, snap.New(a), OpRPC, ParseOpOptions{})
	if err != nil {
		t.Fatalf("parse RPC: %v (%v)", err, ds)
	}
	if err := op.Tree.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	if n, err := op.Tree.Find("/pv2-ctxshape:r/x"); err != nil || n == nil || ctxOf(n) != a {
		t.Fatalf("foreign operation child: %v, %v", n, err)
	}
	r, ds, err := op.Tree.EvalXPath("/pv2-ctxshape:r", XPathOptions{Node: op.Op})
	if err != nil || len(ds) != 0 || len(r.Nodes) != 0 {
		t.Fatalf("operation context must use pointer identity: %+v, %v, %v", r, ds, err)
	}
}

func TestForeignNativeHashUsesLYBValue(t *testing.T) {
	a, b := round12VariantSet(t, "ctx-a", "pv2-ctxshape"),
		round12VariantSet(t, "ctx-b", "pv2-ctxshape")
	strings := round12Tree(t, a, `{"pv2-ctxshape:ll":["1"]}`)
	numbers := round12Tree(t, b, `{"pv2-ctxshape:ll":[1]}`)
	stringNode, numberNode := strings.top.first(), numbers.top.first()
	if hashEqual(stringNode, numberNode) {
		t.Fatal("canonical text equality concealed different LYB hash bytes")
	}
	if strings.findFirst(&strings.top, numberNode) != nil {
		t.Fatal("foreign uint8 matched a string leaf-list value")
	}
	for _, name := range []string{"en", "bi"} {
		first := round12Tree(t, a, `{"pv2-ctxshape:`+name+`":["`+map[string]string{"en": "e", "bi": "a"}[name]+`"]}`)
		second := round12Tree(t, b, `{"pv2-ctxshape:`+name+`":["`+map[string]string{"en": "e", "bi": "a"}[name]+`"]}`)
		if hashEqual(first.top.first(), second.top.first()) {
			t.Fatalf("%s had equal canonical text but different native hash data", name)
		}
	}
	keyA, keyB := round12VariantSet(t, "ctx-a", "pv2-ctxref"),
		round12VariantSet(t, "ctx-b", "pv2-ctxref")
	stringList := round12Tree(t, keyA, `{"pv2-ctxref:l":[{"k":"1"}]}`).top.first()
	numericList := round12Tree(t, keyB, `{"pv2-ctxref:l":[{"k":1}]}`).top.first()
	if hashEqual(stringList, numericList) {
		t.Fatal("string and uint8 list keys shared a native hash")
	}
}

func TestMixedOpaquePrintUsesPrinterContext(t *testing.T) {
	a := round12VariantSet(t, "ctx-a", "pv2-ctxshape")
	b := round12VariantSet(t, "ctx-b", "aa")
	foreign := parseOnlyXML(t, a, `<u xmlns="urn:vibe-ports:yang:conformance:pv2-ctxshape">x</u>`)
	opaq := foreign.top.first()
	if opaq == nil || opaq.schema != nil {
		t.Fatal("expected a retained opaque node")
	}
	if err := opaq.Unlink(); err != nil {
		t.Fatal(err)
	}
	target := round12Tree(t, b, `{"aa:keep":"b"}`)
	if err := target.Insert(nil, opaq); err != nil {
		t.Fatal(err)
	}
	var whole, single bytes.Buffer
	if err := target.PrintJSON(&whole, PrintOptions{Shrink: true}); err != nil {
		t.Fatal(err)
	}
	if err := opaq.PrintJSON(&single, PrintOptions{Shrink: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(whole.String(), `"pv2-ctxshape:u"`) || !strings.Contains(whole.String(), `"u":"x"`) {
		t.Fatalf("tree printer used opaque node context: %s", whole.String())
	}
	if !strings.Contains(single.String(), `"pv2-ctxshape:u":"x"`) {
		t.Fatalf("node printer ignored its own context: %s", single.String())
	}
	jsonTree, ds, err := parseWith(context.Background(), strings.NewReader(`{"pv2-ctxshape:u":"x"}`), a,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true, Unknown: Opaque}}, parseJSON, nil)
	if err != nil {
		t.Fatalf("parse JSON opaque: %v (%v)", err, ds)
	}
	jsonOpaque := jsonTree.top.first()
	if err := jsonOpaque.Unlink(); err != nil {
		t.Fatal(err)
	}
	other := round12Tree(t, b, `{"aa:keep":"b"}`)
	if err := other.Insert(nil, jsonOpaque); err != nil {
		t.Fatal(err)
	}
	whole.Reset()
	single.Reset()
	if err := other.PrintXML(&whole, PrintOptions{Shrink: true}); err != nil {
		t.Fatal(err)
	}
	if err := jsonOpaque.PrintXML(&single, PrintOptions{Shrink: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(whole.String(), `urn:vibe-ports:yang:conformance:pv2-ctxshape`) ||
		!strings.Contains(whole.String(), `<u>x</u>`) {
		t.Fatalf("tree XML printer used opaque node context: %s", whole.String())
	}
	if !strings.Contains(single.String(), `urn:vibe-ports:yang:conformance:pv2-ctxshape`) {
		t.Fatalf("node XML printer ignored its own context: %s", single.String())
	}
}

func TestValidationXPathUsesOwningRevision(t *testing.T) {
	a, b := round12VariantSet(t, "ctx-a", "aa", "pv2-ctxid"),
		round12VariantSet(t, "ctx-b", "aa", "pv2-ctxid")
	target := round12Tree(t, a, `{"aa:keep":"a"}`)
	source := round12Tree(t, b, `{"pv2-ctxid:id":"pv2-ctxid:child"}`)
	diff, err := Diff(nil, source, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	if ds, err := target.Validate(context.Background(), ValidateOptions{Present: true}); err != nil || len(ds) != 0 {
		t.Fatalf("must in B's revision: %v (%v)", err, ds)
	}
}

func TestMixedLeafrefEvaluationUsesReferringContext(t *testing.T) {
	a, b := round12VariantSet(t, "ctx-a", "aa", "pv2-ctxref"),
		round12VariantSet(t, "ctx-b", "aa", "pv2-ctxref")
	target := round12Tree(t, a, `{"pv2-ctxref:l":[{"k":"01"}],"pv2-ctxref:r":"01"}`)
	marker := round12Tree(t, b, `{"aa:keep":"b"}`)
	diff, err := Diff(nil, marker, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	if ds, err := target.Validate(context.Background(), ValidateOptions{Present: true}); err != nil || len(ds) != 0 {
		t.Fatalf("leafref in A behind B root: %v (%v)", err, ds)
	}
	result, ds, err := target.EvalXPath("deref(/pv2-ctxref:r)", XPathOptions{})
	if err != nil || len(ds) != 0 || len(result.Nodes) != 1 || result.Nodes[0].Value() != "01" {
		t.Fatalf("deref in A behind B root: %+v (%v, %v)", result, ds, err)
	}
	var output bytes.Buffer
	if err := target.PrintJSON(&output, PrintOptions{WithDefaults: WDTrim, Shrink: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), `"r"`) {
		t.Fatalf("WDTrim kept A's explicit default: %s", output.String())
	}
}

func TestDiscardMixedLeafrefRecordsChargesLinearWork(t *testing.T) {
	set := round12Set(t, false, "pv2-links")
	for _, count := range []int{128, 256, 512} {
		tr := round12Tree(t, set, `{"pv2-links:c":{"t":["x"],"rl":["x"]}}`)
		root := tr.top.first()
		var target, exemplar *Node
		for c := range root.kids.all() {
			switch c.schema.Name {
			case "t":
				target = c
			case "rl":
				exemplar = c
			}
		}
		if target == nil || exemplar == nil {
			t.Fatal("leafref fixture did not parse")
		}
		if err := linkLeafrefNode(target, exemplar); err != nil {
			t.Fatal(err)
		}
		for i := 1; i < count; i++ {
			leaf := &Node{schema: exemplar.schema, value: exemplar.value}
			if err := tr.insert(root, leaf, insertLast); err != nil {
				t.Fatal(err)
			}
			if err := linkLeafrefNode(target, leaf); err != nil {
				t.Fatal(err)
			}
		}
		tr.work.Store(0)
		tr.work.visits = true
		if err := contextopts.DiscardTree(tr); err != nil {
			t.Fatal(err)
		}
		if target.links != nil || tr.top.len() != 0 {
			t.Fatal("discard left leafref records or tree roots")
		}
		if steps := tr.work.Load(); steps < int64(count) || steps > int64(12*count) {
			t.Fatalf("discard %d leafrefs: %d charged visits", count, steps)
		}
	}
}

// A mixed child set can require a linear schema lookup. XPath must perform that
// lookup itself so each visited sibling consumes its step budget.
func TestMixedFirstChildDefersToChargedXPathScan(t *testing.T) {
	a, b := round12Set(t, false, "pv2-detached"), round12Set(t, false, "pv2-detached")
	tr := round12Tree(t, a, `{"pv2-detached:c":{"l":[{"k":"a"}]}}`)
	foreignTree := round12Tree(t, b, `{"pv2-detached:c":{"base":"x"}}`)
	parent := tr.top.first()
	foreign := foreignTree.top.first().kids.first()
	_ = unlink(foreign)
	if err := tr.link(parent, &parent.kids, foreign, parent.kids.len()); err != nil {
		t.Fatal(err)
	}
	if !parent.kids.mixedContexts() {
		t.Fatal("test needs mixed child contexts")
	}
	probe := xs{s: parent.kids.first().schema, set: a}
	child := wrap(a, parent).(xn)
	if _, ok := child.FirstChild(probe, nil); ok {
		t.Fatal("mixed FirstChild must defer to the charged XPath scan")
	}
	// out of schema order (base after l) the keyed lookup answers by libyang's walk without a
	// children table, charged step by step (interleavedLookup, design 15)
	if got, ok := child.LookupChild(probe, []string{"a"}); !ok || len(got) != 1 || got[0].(xn).n != parent.kids.first() {
		t.Fatalf("mixed keyed lookup out of schema order: %v %v", got, ok)
	}
}

// #306: a foreign leaf merged over a local one goes through lyd_change_term_val with a value of
// another context. lyplg_type_compare_simple compares canonical pointers, so even an equal string
// differs, and lyplg_type_dup_simple's lydict_dup does not find the foreign string: LY_ENOTFOUND,
// nothing logged. The nodes merged before stay, as lyd_merge leaves them; the leaf keeps its value. (Equal integers compare by value and succeed:
// conformance/testdata/merge-contexts.)
func TestMergeForeignLeafChange(t *testing.T) {
	a := round12Set(t, false, "pv2-detached")
	b := round12Set(t, true, "pv2-detached")
	for _, tc := range []struct {
		foreign, rc string
	}{{"old", "LY_ENOTFOUND"}, {"new", "LY_ENOTFOUND"}} {
		target := round12Tree(t, a, `{"pv2-detached:c":{"base":"old"}}`)
		source := round12Tree(t, a, `{"pv2-detached:l":[{"k":"a"}]}`)
		foreignTree := round12Tree(t, b, `{"pv2-detached:c":{"base":"`+tc.foreign+`"}}`)
		foreign := foreignTree.top.first()
		_ = unlink(foreign)
		if err := source.link(nil, &source.top, foreign, source.top.len()); err != nil {
			t.Fatal(err)
		}
		err := target.Merge(source)
		var ve *ValidationError
		switch {
		case tc.rc == "" && err != nil:
			t.Fatalf("equal foreign value: %v", err)
		case tc.rc != "" && (!errors.As(err, &ve) || ve.RC() != tc.rc || len(ve.Diags) != 0):
			t.Fatalf("different foreign value: %v", err)
		}
		c := target.schemaLookup(&target.top, foreign.schema)
		if c == nil || c.kids.At(0).Value() != "old" || ctxOf(c.kids.At(0)) != a {
			t.Fatalf("target leaf changed: %v", c)
		}
	}
}

// D-0133 (#306): merging a union or leafref leaf of another schema context crashes libyang
// 5.8.6 in lyd_change_term_val's callbacks; the port refuses it, logged, and keeps the leaf.
func TestMergeForeignUnionLeafrefRefused(t *testing.T) {
	a := round12Set(t, false, "a", "pv2")
	b := round12Set(t, false, "a", "pv2")
	for _, tc := range []struct{ name, local, foreign, base string }{
		{"u", `{"pv2:c":{"u":-3}}`, `{"pv2:c":{"u":-3}}`, "union"},
		{"u", `{"pv2:c":{"u":"abc"}}`, `{"pv2:c":{"u":"xyz"}}`, "union"},
		{"ref", `{"pv2:c":{"l":[{"k":"k1"}],"ref":"k1"}}`, `{"pv2:c":{"ref":"k1"}}`, "leafref"},
	} {
		target := round12Tree(t, a, tc.local)
		before := target.top.first().kids.schemaSpan().Collect()
		var local *Node
		for _, n := range before {
			if n.Name() == tc.name {
				local = n
			}
		}
		was := local.Value()
		source := round12Tree(t, a, `{"a:foo":1}`) // a first root of the target's context
		foreign := round12Tree(t, b, tc.foreign).top.first()
		_ = unlink(foreign)
		if err := source.link(nil, &source.top, foreign, source.top.len()); err != nil {
			t.Fatal(err)
		}
		err := target.Merge(source)
		var ve *ValidationError
		want := `Invalid arguments - merging the ` + tc.base + ` value of "` + tc.name +
			`" from another schema context is undefined in libyang, not supported (D-0133) (lyd_merge()).`
		if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 || ve.Diags[0].Msg != want {
			t.Fatalf("%s %s: %v", tc.name, tc.foreign, err)
		}
		if local.Value() != was || ctxOf(local) != a {
			t.Fatalf("%s changed: %q", tc.name, local.Value())
		}
	}
}

// D-0133 (#306): ietf-inet-types ipv4-address with its type handler in one context and as a plain
// string in the other (builtin_plugins_only, which libyang cannot have next to a default context
// in one process, D-0030): the leaf type definitions differ, so the merge is refused, logged, in
// both directions, and the leaf is kept.
func TestMergeForeignTypeHandlerRefused(t *testing.T) {
	load := func(builtin bool) *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true, BuiltinPluginsOnly: builtin},
			os.DirFS(filepath.Join("..", "conformance", "corpus", "types", "schemas")))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"ty2", "ty3"} {
			if _, ds, err := c.Load(name, "", nil); err != nil {
				t.Fatalf("load %s: %v (%v)", name, err, ds)
			}
		}
		return c.Snapshot()
	}
	for _, foreignBuiltin := range []bool{true, false} {
		a, b := load(!foreignBuiltin), load(foreignBuiltin)
		target := round12Tree(t, a, `{"ty3:c":{"v4":"1.2.3.4"}}`)
		local := target.top.first().kids.At(0)
		source := round12Tree(t, a, `{"ty2:c":{"id":"ty2:der"}}`) // a first root of the target's context
		foreign := round12Tree(t, b, `{"ty3:c":{"v4":"1.2.3.4"}}`).top.first()
		_ = unlink(foreign)
		if err := source.link(nil, &source.top, foreign, source.top.len()); err != nil {
			t.Fatal(err)
		}
		err := target.Merge(source)
		var ve *ValidationError
		want := `Invalid arguments - the type of "v4" differs between the two schema contexts, merging its ` +
			`value is not supported (D-0133) (lyd_merge()).`
		if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) != 1 || ve.Diags[0].Msg != want {
			t.Fatalf("foreign builtin %v: %v", foreignBuiltin, err)
		}
		if local.Value() != "1.2.3.4" || ctxOf(local) != a {
			t.Fatalf("leaf changed: %q", local.Value())
		}
	}
}

// #306: a zoned address of another context never compares equal (lyplg_type_compare_ipv4_address
// compares the zone dictionary pointers), also after a first merge stored the source's value: the
// second merge replaces it again and sets LYD_NEW on the validated leaf.
func TestMergeForeignZonedAddressTwice(t *testing.T) {
	load := func() *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true},
			os.DirFS(filepath.Join("..", "conformance", "corpus", "types", "schemas")))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"ty2", "ty3"} {
			if _, ds, err := c.Load(name, "", nil); err != nil {
				t.Fatalf("load %s: %v (%v)", name, err, ds)
			}
		}
		return c.Snapshot()
	}
	a, b := load(), load()
	target := round12Tree(t, a, `{"ty3:c":{"v4":"1.2.3.4%eth0"}}`)
	local := target.top.first().kids.At(0)
	for round := range 2 {
		local.flags &^= FlagNew // validated
		source := round12Tree(t, a, `{"ty2:c":{"id":"ty2:der"}}`)
		foreign := round12Tree(t, b, `{"ty3:c":{"v4":"1.2.3.4%eth0"}}`).top.first()
		_ = unlink(foreign)
		if err := source.link(nil, &source.top, foreign, source.top.len()); err != nil {
			t.Fatal(err)
		}
		if err := target.Merge(source); err != nil {
			t.Fatalf("merge %d: %v", round, err)
		}
		if local.flags&FlagNew == 0 || local.Value() != "1.2.3.4%eth0" {
			t.Fatalf("merge %d: the zoned value was not replaced (flags %v)", round, local.flags)
		}
	}
}

// #306: lyplg_type_dup_union keeps the source member realtype, so a union<bits> value merged from
// A into B and back into A compares its member again (the destination's own realtype): equal, the
// validated leaf stays as it is.
func TestMergeForeignUnionRoundTrip(t *testing.T) {
	fsys := fstest.MapFS{
		"rt0.yang": {Data: []byte(`module rt0 { yang-version 1.1; namespace "urn:rt0"; prefix r0; leaf r { type string; } }`)},
		"rt.yang": {Data: []byte(`module rt { yang-version 1.1; namespace "urn:rt"; prefix rt;
  container c { leaf u { type union { type bits { bit x; bit y; } type uint8; } } } }`)},
	}
	load := func() *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fsys)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"rt0", "rt"} {
			if _, ds, err := c.Load(name, "", nil); err != nil {
				t.Fatalf("load %s: %v (%v)", name, err, ds)
			}
		}
		return c.Snapshot()
	}
	a, b := load(), load()
	merge := func(dst *Tree, dstSet, srcSet *schema.Set, from *Node) {
		t.Helper()
		source := round12Tree(t, dstSet, `{"rt0:r":"x"}`)
		c := from
		if c == nil {
			c = round12Tree(t, srcSet, `{"rt:c":{"u":"x"}}`).top.first()
		}
		_ = unlink(c)
		if err := source.link(nil, &source.top, c, source.top.len()); err != nil {
			t.Fatal(err)
		}
		if err := dst.Merge(source); err != nil {
			t.Fatal(err)
		}
	}
	treeA := round12Tree(t, a, `{"rt:c":{"u":"x"}}`)
	leafA := treeA.top.first().kids.At(0)
	treeB := round12Tree(t, b, `{"rt:c":{"u":"x"}}`)
	leafB := treeB.top.first().kids.At(0)
	// A -> B: the member realtypes differ, lyplg_type_dup_bits copies A's value with A's member
	carrier := round12Tree(t, a, `{"rt:c":{"u":"x"}}`).top.first()
	leafB.flags &^= FlagNew
	merge(treeB, b, a, carrier)
	memberType := func(n *Node) *schema.Type { m, _ := n.value.Union().Member(); return m.Type() }
	if leafB.flags&FlagNew == 0 || memberType(leafB) != memberType(leafA) {
		t.Fatal("A's value did not replace B's")
	}
	// B -> A: the same member realtype, the bits compare equal
	cB := treeB.top.first()
	_ = unlink(cB)
	leafA.flags &^= FlagNew
	merge(treeA, a, b, cB)
	if leafA.flags&FlagNew != 0 {
		t.Fatal("the value merged back compared different")
	}
}

// TestMergeForeignSharedLeafrefGraph: l0...l32 are each a union of bits and two leafrefs to the
// next leaf, so the type definitions form a small shared graph with 2^32 paths. The cross-context
// definition comparison is linear in it and charged to the merge's hash work budget.
func TestMergeForeignSharedLeafrefGraph(t *testing.T) {
	var mod strings.Builder
	mod.WriteString(`module sg { yang-version 1.1; namespace "urn:sg"; prefix sg; container c {` + "\n")
	for i := range 32 {
		fmt.Fprintf(&mod, `leaf l%d { type union { type bits { bit x; } type leafref { path "../l%d"; } `+
			`type leafref { path "../l%d"; } } }`+"\n", i, i+1, i+1)
	}
	mod.WriteString(`leaf l32 { type bits { bit x; } } } }`)
	fsys := fstest.MapFS{
		"sg0.yang": {Data: []byte(`module sg0 { yang-version 1.1; namespace "urn:sg0"; prefix s0; leaf r { type string; } }`)},
		"sg.yang":  {Data: []byte(mod.String())},
	}
	load := func() *schema.Set {
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fsys)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"sg0", "sg"} {
			if _, ds, err := c.Load(name, "", nil); err != nil {
				t.Fatalf("load %s: %v (%v)", name, err, ds)
			}
		}
		return c.Snapshot()
	}
	a, b := load(), load()
	merge := func(limit uint64) error {
		dst := round12Tree(t, a, `{"sg:c":{"l0":"x"}}`)
		dst.work.hashLimit = limit
		source := round12Tree(t, a, `{"sg0:r":"x"}`)
		c := round12Tree(t, b, `{"sg:c":{"l0":"x"}}`).top.first()
		_ = unlink(c)
		if err := source.link(nil, &source.top, c, source.top.len()); err != nil {
			t.Fatal(err)
		}
		return dst.Merge(source)
	}
	done := make(chan error, 1)
	go func() { done <- merge(0) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the definition comparison walks every path of the shared graph")
	}
	var limit uint64
	for limit = 1; ; limit++ { // the smallest budget the merge fits in
		if err := merge(limit); err == nil {
			break
		} else if !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("limit %d: %v", limit, err)
		}
		if limit > 1<<12 {
			t.Fatal("no small budget fits the merge")
		}
	}
	if limit <= 4*32 { // the compared pairs: per level a union, its bits and two leafrefs
		t.Fatalf("the merge fits in %d units; the definition comparison is not charged", limit)
	}
	if err := merge(limit - 1); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("one unit short: %v; want %v", err, yang.ErrBudget)
	}
}

// TestMergeForeignFragmentedTypedefBudget: many matched leaves of one typedef whose range has many
// intervals. The definition comparison charges every interval it scans, and the memo of one Merge
// compares each type pair once however many leaves use it.
func TestMergeForeignFragmentedTypedefBudget(t *testing.T) {
	const leaves, parts = 64, 128
	load := func(rng string) *schema.Set {
		var mod strings.Builder
		fmt.Fprintf(&mod, `module fr { yang-version 1.1; namespace "urn:fr"; prefix fr;
  typedef frag { type uint32 { range "%s"; } } container c {`, rng)
		for i := range leaves {
			fmt.Fprintf(&mod, " leaf l%d { type frag; }", i)
		}
		mod.WriteString(" } }")
		fsys := fstest.MapFS{
			"fr0.yang": {Data: []byte(`module fr0 { yang-version 1.1; namespace "urn:fr0"; prefix f0; leaf r { type string; } }`)},
			"fr.yang":  {Data: []byte(mod.String())},
		}
		c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fsys)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"fr0", "fr"} {
			if _, ds, err := c.Load(name, "", nil); err != nil {
				t.Fatalf("load %s: %v (%v)", name, err, ds)
			}
		}
		return c.Snapshot()
	}
	var frag []string
	for i := range parts {
		frag = append(frag, fmt.Sprint(2*i+1))
	}
	var doc strings.Builder
	doc.WriteString(`{"fr:c":{`)
	for i := range leaves {
		if i > 0 {
			doc.WriteString(",")
		}
		fmt.Fprintf(&doc, `"l%d":1`, i)
	}
	doc.WriteString("}}")
	// used is the hash work the cross-context merge of every leaf charged; it must also fail
	// with ErrBudget one unit short of it.
	used := func(rng string) uint64 {
		a, b := load(rng), load(rng)
		merge := func(limit uint64) (uint64, error) {
			dst := round12Tree(t, a, doc.String())
			dst.work.hashLimit = limit
			source := round12Tree(t, a, `{"fr0:r":"x"}`)
			c := round12Tree(t, b, doc.String()).top.first()
			_ = unlink(c)
			if err := source.link(nil, &source.top, c, source.top.len()); err != nil {
				t.Fatal(err)
			}
			err := dst.Merge(source)
			return dst.work.hashUsed, err
		}
		n, err := merge(0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := merge(n - 1); !errors.Is(err, yang.ErrBudget) {
			t.Fatalf("one unit short of %d: %v; want %v", n, err, yang.ErrBudget)
		}
		return n
	}
	extra := used(strings.Join(frag, "|")) - used("1")
	if extra < parts-1 {
		t.Fatalf("%d more work units for %d more range intervals; the scan is not charged", extra, parts-1)
	}
	if extra > 4*parts {
		t.Fatalf("%d more work units for %d leaves of one typedef: compared once per leaf, not once per merge",
			extra, leaves)
	}
}
