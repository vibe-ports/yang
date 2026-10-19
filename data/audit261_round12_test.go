// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	unlink(foreign)
	if err := mixed.link(nil, &mixed.top, foreign, len(mixed.top.list)); err != nil {
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
	unlink(foreign)
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
	if len(c.kids.list) != 2 || c.kids.list[1].Name() != "base" {
		t.Fatalf("foreign child must precede the destination child: %v", c.kids.list)
	}
	extra := c.kids.list[0]
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
	unlink(foreign)
	if err := target.link(nil, &target.top, foreign, len(target.top.list)); err != nil {
		t.Fatal(err)
	}
	diff := parseOnlyXML(t, a, `<ll xmlns="urn:vibe-ports:yang:conformance:pv2-detached"`+
		` xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="create" yang:value="">c</ll>`)
	if err := target.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatalf("empty anchor must use A:a: %v", err)
	}
	if target.top.list[0].Value() != "c" || target.top.list[1].Value() != "a" || target.top.list[2].Value() != "b" {
		t.Fatalf("ordered run: %v", target.top.list)
	}
}

func TestDuplicateInstanceCacheIncludesForeignEqualSibling(t *testing.T) {
	a, b := round12Set(t, false, "pv2-detached"), round12Set(t, false, "pv2-detached")
	target := round12Tree(t, a, `{"pv2-detached:sll":["x"]}`)
	other := round12Tree(t, b, `{"pv2-detached:sll":["x"]}`)
	foreign := other.top.first()
	unlink(foreign)
	if err := target.link(nil, &target.top, foreign, len(target.top.list)); err != nil {
		t.Fatal(err)
	}
	first := target.top.list[0]
	cache := &dupCache{}
	if got := target.dupInstNext(first, cache); got != first {
		t.Fatalf("first instance: %v", got)
	}
	if got := target.dupInstNext(first, cache); got != foreign {
		t.Fatalf("second equal foreign instance: %v", got)
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
	first := parent.kids.list[len(parent.kids.list)-1]
	if parent.kids.ht == nil {
		t.Fatal("four schema children must have a hash table")
	}
	foreign := foreignTree.top.first().kids.list[0]
	unlink(foreign)
	if err := target.link(parent, &parent.kids, foreign, len(parent.kids.list)-1); err != nil {
		t.Fatal(err)
	}
	if parent.kids.list[len(parent.kids.list)-2] != foreign {
		t.Fatal("foreign equal instance was not prepended")
	}
	cache := &dupCache{}
	if got := target.dupInstNext(first, cache); got != first {
		t.Fatalf("first registered equal instance = %v", got)
	}
	if got := target.dupInstNext(first, cache); got != foreign {
		t.Fatalf("later registered foreign equal instance = %v", got)
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
		linkLeafrefNode(target, exemplar)
		for i := 1; i < count; i++ {
			leaf := &Node{schema: exemplar.schema, value: exemplar.value}
			if err := tr.insert(root, leaf, insertLast); err != nil {
				t.Fatal(err)
			}
			linkLeafrefNode(target, leaf)
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
	unlink(foreign)
	if err := tr.link(parent, &parent.kids, foreign, parent.kids.len()); err != nil {
		t.Fatal(err)
	}
	if !parent.kids.mixedContexts() {
		t.Fatal("test needs mixed child contexts")
	}
	probe := xs{s: parent.kids.first().schema, set: a}
	child := wrap(a, parent).(xn)
	if _, ok := child.FirstChild(probe); ok {
		t.Fatal("mixed FirstChild must defer to the charged XPath scan")
	}
	if _, ok := child.LookupChild(probe, []string{"a"}); ok {
		t.Fatal("mixed keyed lookup must defer to the charged XPath scan")
	}
}
