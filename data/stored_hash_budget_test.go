// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/snap"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

const sparseEmptyModule = `module m {
  yang-version 1.1; namespace urn:m; prefix m;
  leaf-list s { config false; type bits { bit high { position 4294967294; } } }
}`

// The exact audit witness represents 512 MiB of zero bytes per empty string.
// A single bounded zero run does not bound a parse containing repeated values.
func TestStoredHashSparseEmptyParseBudget(t *testing.T) {
	set := opIndexSet(t, sparseEmptyModule)
	input := `{"m:s":[` + strings.Repeat(`"",`, 1023) + `""]}`
	var parsed *Tree
	before := types.SparseHashBytes()
	got, diags, err := parseWith(context.Background(), strings.NewReader(input), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, func(lc *lydCtx) {
			parsed = lc.tree
		})
	if !errors.Is(err, yang.ErrBudget) || got != nil || len(diags) != 0 {
		t.Fatalf("treeReturned=%t, diagnostics=%v, error=%v; want silent ErrBudget", got != nil, diags, err)
	}
	if work := types.SparseHashBytes() - before; work > int64(defaultMaxHashWork) {
		t.Fatalf("executed %d sparse transitions beyond quota %d", work, defaultMaxHashWork)
	}
	if parsed.work.hashUsed == 0 || parsed.work.hashUsed > defaultMaxHashWork {
		t.Fatalf("reserved hash work %d, limit %d", parsed.work.hashUsed, defaultMaxHashWork)
	}
}

func TestStoredHashSparseXMLKeyBudget(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  list l { key k; leaf k { type bits { bit high { position 4294967294; } } } }
}`)
	input := strings.Repeat(`<l xmlns="urn:m"><k/></l>`, 600)
	got, diags, err := parseWith(context.Background(), strings.NewReader(input), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseXML, nil)
	if !errors.Is(err, yang.ErrBudget) || got != nil || len(diags) != 0 {
		t.Fatalf("treeReturned=%t diagnostics=%v error=%v; want silent ErrBudget", got != nil, diags, err)
	}
}

// A failed hashed-predicate compilation falls back to the ordinary predicates. Resource
// failures from deref() are not part of that speculative failure: even when an earlier ordinary
// predicate would remove every row, the shared instance-identifier probe quota must stop the
// evaluation.
func TestXPathSpeculativeDerefStoredHashBudget(t *testing.T) {
	set := opIndexSet(t, `module m {
  yang-version 1.1; namespace urn:m; prefix m;
  leaf-list target { config false; type bits { bit high { position 4294967294; } } }
  leaf ref { config false; type instance-identifier; }
  list pair {
    config false; key "k1 k2";
    leaf k1 { type string; }
    leaf k2 { type string; }
  }
}`)
	tr, diags, err := Parse(context.Background(), strings.NewReader(
		`{"m:target":[""],"m:ref":"/m:target[.='']","m:pair":[{"k1":"present","k2":""}]}`),
		FormatJSON, snap.New(set), ParseOptions{ParseOnly: true})
	if err != nil || len(diags) != 0 {
		t.Fatalf("Parse diagnostics=%v error=%v", diags, err)
	}
	ref := findNode(t, tr, "/m:ref")
	measure := &valCtx{t: tr, probes: newTree(set)}
	if _, err := measure.deref(wrap(set, ref)); err != nil {
		t.Fatal(err)
	}
	oneDeref := measure.probes.work.hashUsed
	if oneDeref == 0 || oneDeref >= defaultMaxHashWork {
		t.Fatalf("one deref charged %d stored-hash units", oneDeref)
	}

	e, err := xpath.Compile(
		"count(/m:pair[k1='absent'][k2=concat(deref(/m:ref),deref(/m:ref))])",
		jsonNS{set: set})
	if err != nil {
		t.Fatal(err)
	}
	vc := &valCtx{t: tr, log: &logger{set: set}, probes: newTree(set)}
	vc.probes.work.hashLimit = oneDeref
	if _, err := vc.eval(e, nil, xpath.RootAll, true); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("speculative deref error=%v; want yang.ErrBudget", err)
	}
}

func TestStoredHashSparseEmptyParseWork(t *testing.T) {
	set := opIndexSet(t, sparseEmptyModule)
	measure := func(count int) int64 {
		input := `{"m:s":[` + strings.Repeat(`"",`, count-1) + `""]}`
		before := types.SparseHashBytes()
		tr, _, err := parseJSONString(set, input, Reject, false)
		if err != nil {
			t.Fatal(err)
		}
		work := types.SparseHashBytes() - before
		if len(tr.top.list) != count || work != int64(count)*65536 {
			t.Fatalf("%d values: nodes=%d transitions=%d, want exactly 65536 per value", count, len(tr.top.list), work)
		}
		return work
	}
	base, large := measure(16), measure(64)
	if large > 6*base {
		t.Fatalf("4N work %d > 6 * N work %d", large, base)
	}
}

// A long immutable schema name contributes one prefix to a parse, regardless
// of how many leaf-list values use it. Its first hash must also count toward
// the quota, so an even smaller quota refuses before publishing a value.
func TestStoredHashLongNameRepeatedValuesBudget(t *testing.T) {
	name := "n" + strings.Repeat("x", 1<<16)
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  leaf-list `+name+` { type string; ordered-by user; } }`)
	for _, mod := range set.Modules {
		mod.Owner = set // compiled snapshots share immutable prefixes across trees
	}
	input := `{"m:` + name + `":[` + strings.Repeat(`"",`, 63) + `""]}`
	limit := uint64(len("m") + len(name) + 64 + 8)
	var parsed *Tree
	got, diags, err := parseWith(context.Background(), strings.NewReader(input), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, func(lc *lydCtx) {
			lc.tree.work.hashLimit = limit
			parsed = lc.tree
		})
	if err != nil || len(diags) != 0 || got == nil || len(got.top.list) != 64 {
		t.Fatalf("nodes=%d diagnostics=%v error=%v", len(parsed.top.list), diags, err)
	}
	if used := parsed.work.hashUsed; used < uint64(len("m")+len(name)+64) || used > limit {
		t.Fatalf("reserved hash work = %d, want one schema prefix plus 64 values", used)
	}
	var repeated *Tree
	got, diags, err = parseWith(context.Background(), strings.NewReader(input), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, func(lc *lydCtx) {
			lc.tree.work.hashLimit = 64 + 8
			repeated = lc.tree
		})
	if err != nil || len(diags) != 0 || got == nil || len(got.top.list) != 64 || repeated.work.hashUsed != 64 {
		t.Fatalf("cached prefix: nodes=%d work=%d diagnostics=%v error=%v", len(repeated.top.list),
			repeated.work.hashUsed, diags, err)
	}
}

// Same-context duplication rehashes the copied key. Its original lexical
// union input is much longer than the selected uint32 LYB payload; the stored
// no-tree selection makes repeated hashes independent of that lexical length.
func TestStoredHashLongLexicalUnionKeyDupBudget(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container r { list l { key k; leaf k { type union { type uint32; type string; } } } } }`)
	lexical := strings.Repeat("0", 1<<20) + "1"
	src, _, err := parseXMLString(set, `<r xmlns="urn:m"><l><k>`+lexical+`</k></l></r>`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	root := src.top.first()
	key := root.kids.list[0].kids.list[0]
	if bound := types.HashLYBWorkBound(key.value); bound > 64 {
		t.Fatalf("cached union hash bound = %d, want selected payload only", bound)
	}
	for i := range 16 {
		dst := newTree(set)
		dst.work.hashLimit = 128
		copyRoot, _, err := dst.Dup(root, nil, DupOptions{Recursive: true})
		if err != nil {
			t.Fatalf("dup %d: %v", i, err)
		}
		if used := dst.work.hashUsed; used > 128 {
			t.Fatalf("dup %d charged %d hash units", i, used)
		}
		if copyRoot.kids.list[0].kids.list[0].hash != key.hash {
			t.Fatalf("dup %d changed the key's stored hash", i)
		}
	}
}

// A copied list can retain a native hash over b although the destination
// declares a,b. Inserting a before b freezes the old prefix and rehashes both.
func TestStoredHashCopiedIncompletePrefixBudget(t *testing.T) {
	srcSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  list l { key b; leaf b { type string; } } }`)
	dstSet := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  list l { key "a b"; leaf a { type string; } leaf b { type string; } } }`)
	src, _, err := parseJSONString(srcSet, `{"m:l":[{"b":"`+strings.Repeat("x", 1024)+`"}]}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	dst := newTree(dstSet)
	list, _, err := dst.Dup(src.top.first(), nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if !list.lkeyOK || list.lkeyN != 1 || len(list.kids.list) != 1 || list.kids.list[0].schema.Name != "b" {
		t.Fatal("copy did not retain the incomplete b prefix")
	}
	aType := list.schema.Keys[0].Type
	aValue, diag := types.Store(aType, "a", types.FormatJSON, types.HintData, nil, list.schema.Keys[0])
	if diag != nil {
		t.Fatal(diag.Msg)
	}
	aWork := types.HashLYBWorkBound(aValue)
	bWork := types.HashLYBWorkBound(list.kids.list[0].value)
	// The positional-prefix and hash-input scans each visit both keys;
	// displacing the captured prefix copies its one existing key.
	dst.work.hashLimit = 2*aWork + bWork + 5 - 1
	beforeHash, beforePrefix := list.hash, list.lkeyPrefix
	if added, err := dst.NewTerm(list, "a", "a", NewOptions{}); added != nil || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("NewTerm node=%v err=%v; want ErrBudget", added, err)
	}
	if len(list.kids.list) != 1 || list.hash != beforeHash || list.lkeyPrefix != beforePrefix ||
		list.lkeyFrozen != nil || list.keyN != 1 || list.lkeyN != 1 {
		t.Fatal("failed insertion published a key or changed its parent's captured hash")
	}
	dst.work.hashLimit = 2*aWork + bWork + 5
	if added, err := dst.NewTerm(list, "a", "a", NewOptions{}); err != nil || added == nil {
		t.Fatalf("retry node=%v err=%v", added, err)
	}
}

func TestStoredHashSparseMutationBudget(t *testing.T) {
	set := opIndexSet(t, sparseEmptyModule)
	tr, _, err := parseJSONString(set, `{"m:s":["high"]}`, Reject, false)
	if err != nil {
		t.Fatal(err)
	}
	n := tr.top.list[0]
	beforeHash := n.hash
	tr.work.hashLimit = 1
	before := types.SparseHashBytes()
	if changed, err := n.SetValue(""); changed || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("SetValue changed=%t err=%v; want unchanged ErrBudget", changed, err)
	}
	if n.Value() != "high" || n.hash != beforeHash || types.SparseHashBytes() != before {
		t.Fatal("SetValue exceeded its quota or changed the value/hash before refusing")
	}
	if added, err := tr.NewTerm(nil, "m:s", "", NewOptions{}); added != nil || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("NewTerm node=%v err=%v; want ErrBudget", added, err)
	}
	if len(tr.top.list) != 1 || types.SparseHashBytes() != before {
		t.Fatal("failed NewTerm performed hashing or published a node")
	}
	if added, err := tr.NewPath("/m:s[2]", "", NewPathOptions{}); added != nil || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("NewPath node=%v err=%v; want ErrBudget", added, err)
	}
	if len(tr.top.list) != 1 || types.SparseHashBytes() != before {
		t.Fatal("failed NewPath performed hashing or published a node")
	}
}

func TestStoredHashReplyBudgetThenValidateRequest(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  rpc r { input { leaf in { type string; } }
    output { leaf p { type string; } leaf-list s { type bits { bit high { position 4294967294; } } } } }
}`)
	tr, op, _, err := parseOp(context.Background(), strings.NewReader(`{"m:r":{"in":"kept"}}`),
		set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotHashBudgetTree(tr)
	tr.work.hashLimit = 128
	_, _, _, err = parseOp(context.Background(), strings.NewReader(`{"m:p":"parsed","m:s":[""]}`),
		set, FormatJSON, opReply, op, Reject)
	if !errors.Is(err, yang.ErrBudget) || !errors.Is(tr.work.hashErr, yang.ErrBudget) {
		t.Fatalf("reply error=%v retained error=%v; want budget exhaustion", err, tr.work.hashErr)
	}
	if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after, before) {
		t.Fatalf("reply cleanup changed request: before=%+v after=%+v", before, after)
	}
	if _, err := validateOp(context.Background(), set, op, nil, opRPC, Budget{}); err != nil {
		t.Fatalf("request validation after failed reply: %v", err)
	}
	if tr.top.first() != op || op.treeOf() != tr || len(op.kids.list) != 1 || op.kids.list[0].Name() != "in" {
		t.Fatal("validation detached the retained request")
	}
}

func TestStoredHashValidateOpBudgetRestoresRequest(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  rpc r { input { leaf x { type string; default "d"; } } }
}`)
	tr, op, _, err := parseOp(context.Background(), strings.NewReader(`{"m:r":{}}`),
		set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err)
	}
	dep := newTree(set)
	dep.work.hashLimit = 1
	if _, err := validateOp(context.Background(), set, op, dep, opRPC, Budget{}); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("validation error=%v; want hash budget", err)
	}
	if tr.top.first() != op || op.treeOf() != tr || dep.top.len() != 0 {
		t.Fatal("budget failure left the operation in the dependency tree or detached")
	}
}

func TestStoredHashIncompletePrefixVisitBudget(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  list l { key "a b"; leaf a { type string; } leaf b { type string; } }
}`)
	input := `{"m:l":[{` + strings.Repeat(`"a":"x",`, 1024) + `"b":"y"}]}`
	const limit = 128
	var parsed *Tree
	_, _, err := parseWith(context.Background(), strings.NewReader(input), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, func(lc *lydCtx) {
			parsed = lc.tree
			parsed.work.hashLimit = limit
			parsed.work.visits = true
		})
	if !errors.Is(err, yang.ErrBudget) || parsed == nil {
		t.Fatalf("parse error=%v tree=%v; want bounded prefix scan", err, parsed)
	}
	if parsed.work.hashUsed > limit || parsed.work.Load() > 16*limit {
		t.Fatalf("charged hash work=%d visited work=%d; limit=%d",
			parsed.work.hashUsed, parsed.work.Load(), limit)
	}
}

func TestStoredHashFailedRelinkRestoresOrigin(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container c { leaf x { type string; } }
}`)
	src, dst := newTree(set), newTree(set)
	srcParent, err := src.NewInner(nil, "m:c", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dstParent, err := dst.NewInner(nil, "m:c", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := src.NewTerm(srcParent, "x", "kept", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dst.work.hashErr = yang.ErrBudget
	if err := dst.insertChild(dstParent, leaf); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("failed insert: %v", err)
	}
	if leaf.parent != srcParent || len(srcParent.kids.list) != 1 || len(dstParent.kids.list) != 0 {
		t.Fatal("failed relink detached the source leaf")
	}
	src.work.hashErr = yang.ErrBudget
	if err := dst.Insert(dstParent, leaf); err != nil || leaf.parent != dstParent {
		t.Fatalf("fresh destination scope did not move leaf: %v", err)
	}
	if src.work.hashErr != nil {
		t.Fatal("source tree kept a stale hash error after Insert")
	}

	whole := newTree(set)
	if _, err := whole.NewInner(nil, "m:c", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	empty := newTree(set)
	empty.work.hashErr = yang.ErrBudget
	if err := empty.moveNodes(nil, whole); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("failed sibling-run move error=%v; want ErrBudget", err)
	}
	if whole.top.len() != 1 || empty.top.len() != 0 || whole.top.first().tree != whole {
		t.Fatal("failed sibling-run move detached its source")
	}
}

func TestStoredHashValueRelinkBudgetKeepsOldValue(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  rpc r { input { leaf-list x { type string; } leaf y { type string; }
    leaf z { type string; } leaf w { type string; } } }
}`)
	tr, op, _, err := parseOp(context.Background(), strings.NewReader(
		`{"m:r":{"x":["a","c"],"y":"y","z":"z","w":"w"}}`), set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err)
	}
	if op.kids.ht == nil {
		t.Fatal("operation children did not build a hash table")
	}
	x := op.kids.list[0]
	for _, child := range op.kids.list {
		if child.Name() == "y" {
			delete(tr.work.hashNames, child.schema) // make the next anchor lookup cold
		}
	}
	v, diag := types.Store(x.schema.Type, "b", types.FormatJSON, types.HintData, nil, x.schema)
	if diag != nil {
		t.Fatal(diag.Msg)
	}
	tr.work.hashLimit = types.HashLYBWorkBound(v)
	oldHash := x.hash
	if changed, err := x.SetValue("b"); changed || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("changed=%t error=%v; want atomic budget failure", changed, err)
	}
	if x.Value() != "a" || x.hash != oldHash || x.parent != op || op.kids.list[0] != x {
		t.Fatal("failed value change left the old instance detached or rehashed")
	}
	tr.work.hashLimit = 128 // removal updates the ordered operation index and charges its RB work
	if err := op.kids.list[1].Remove(); err != nil {
		t.Fatal(err)
	}
	if changed, err := x.SetValue("b"); err != nil || !changed {
		t.Fatalf("alone value change: changed=%t error=%v; want indexed hash update", changed, err)
	}
	if x.parent != op || op.kids.list[0] != x {
		t.Fatal("alone value change moved the instance")
	}
	tr.work.hashLimit = 1
	for _, child := range op.kids.list {
		if child.Name() == "y" {
			delete(tr.work.hashNames, child.schema)
		}
	}
	if err := tr.Insert(op, x); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("move with cold anchor error=%v; want ErrBudget", err)
	}
	if x.parent != op || op.kids.list[0] != x {
		t.Fatal("failed move detached or reordered its source")
	}
}

// A recursive cross-context copy retains source child order. The implicit default's
// insertion must report a cold hash failure while finding its anchor in that order.
func TestStoredHashImplicitDefaultAnchorBudget(t *testing.T) {
	const head = `module m { yang-version 1.1; namespace urn:m; prefix m; container r { `
	srcSet := opIndexSet(t, head+`leaf b { type string; } leaf c { type string; } leaf a { type string; } leaf e { type string; } } }`)
	dstSet := opIndexSet(t, head+`leaf a { type string; } leaf d { type string; default "default"; } `+
		`leaf b { type string; } leaf c { type string; } leaf e { type string; } } }`)
	src, _, err := parseJSONString(srcSet, `{"m:r":{"b":"b","c":"c","a":"a","e":"e"}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	dst := newTree(dstSet)
	root, _, err := dst.Dup(src.top.first(), nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	if root.kids.ht == nil || !root.kids.schemaUnordered || len(root.kids.list) != 4 {
		t.Fatal("copy lacks the reordered indexed children")
	}
	var diffs int
	vc := &valCtx{t: dst, log: &logger{set: dstSet}, nodeWhen: &nodeSet{}, nodeTypes: &nodeSet{},
		diff: func(*Node, diffOp) error { diffs++; return nil }}
	// The quota left by the Dup is spent: the default's (cached) name fits, its anchor does not.
	used := dst.work.hashUsed
	dst.work.hashLimit = used
	if err := vc.newImplicit(root, nil, nil, 0); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("implicit insertion error=%v; want ErrBudget", err)
	}
	if dst.work.hashUsed != used || len(root.kids.list) != 4 ||
		vc.nodeWhen.len() != 0 || vc.nodeTypes.len() != 0 || diffs != 0 {
		t.Fatalf("failed anchor published a default or diff: used=%d children=%d queues=%d/%d diffs=%d",
			dst.work.hashUsed, len(root.kids.list), vc.nodeWhen.len(), vc.nodeTypes.len(), diffs)
	}
	if _, err := dst.Validate(context.Background(), ValidateOptions{}); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("Validate error=%v; want ErrBudget", err)
	}
	if len(root.kids.list) != 4 {
		t.Fatal("Validate published a default after anchor failure")
	}
}

func TestStoredHashFailedKeyChangeKeepsDuplicateLookupOrder(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  container c { list l { key "k j"; leaf k { type string; } leaf j { type string; } }
    leaf x { type string; } leaf y { type string; } }
}`)
	tr, _, err := parseWith(context.Background(), strings.NewReader(
		`{"m:c":{"l":[{"k":"a","j":"q"},{"k":"a","j":"q"}],"x":"x","y":"y"}}`), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := tr.top.first()
	if c.kids.ht == nil {
		t.Fatal("children table was not built")
	}
	first := c.kids.list[0]
	before, err := tr.Find("/m:c/l[k='a'][j='q']")
	if err != nil || before != first {
		t.Fatalf("initial lookup=%p error=%v; want %p", before, err, first)
	}
	tr.work.hashLimit = 5 // both key scans fit; their combined rehash exceeds the remainder
	if changed, err := first.kids.list[0].SetValue("b"); changed || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("SetValue changed=%t error=%v; want ErrBudget", changed, err)
	}
	after, err := tr.Find("/m:c/l[k='a'][j='q']")
	if err != nil || after != first || first.kids.list[0].Value() != "a" || first.parent != c {
		t.Fatalf("failed edit changed lookup order or detached the first list: got=%p error=%v", after, err)
	}
}

// A multi-key rehash is one reservation: a quota that fits each key separately
// must still reject their combined work before unlinking or replacing a value.
func TestStoredHashSparseKeyBudget(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  typedef sparse { type bits { bit high { position 4294967294; } } }
  list l { key "a b"; ordered-by user; leaf a { type sparse; } leaf b { type sparse; } }
}`)
	tr, _, err := parseJSONString(set, `{"m:l":[{"a":"high","b":"high"}]}`, Reject, false)
	if err != nil {
		t.Fatal(err)
	}
	list := tr.top.list[0]
	a, b := list.kids.list[0], list.kids.list[1]
	empty, diag := types.Store(a.schema.Type, "", types.FormatJSON, types.HintData, nil, a.schema)
	if diag != nil {
		t.Fatal(diag.Msg)
	}
	tr.work.hashLimit = types.HashLYBWorkBound(empty) + types.HashLYBWorkBound(b.value) - 1
	beforeHash, before := list.hash, types.SparseHashBytes()
	if changed, err := a.SetValue(""); changed || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("key change=%t error=%v; want ErrBudget", changed, err)
	}
	if a.Value() != "high" || list.hash != beforeHash || types.SparseHashBytes() != before {
		t.Fatal("failed multi-key preflight changed the list or performed hashing")
	}
	tr.work.hashLimit = 1
	if added, err := tr.NewList(nil, "m:l", []string{"", ""}, NewOptions{}); added != nil || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("NewList node=%v error=%v; want ErrBudget", added, err)
	}
	if len(tr.top.list) != 1 || types.SparseHashBytes() != before {
		t.Fatal("failed list creation published a node or performed hashing")
	}
	if copied, _, err := tr.Dup(list, nil, DupOptions{Recursive: true}); copied != nil || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("Dup node=%v error=%v; want ErrBudget", copied, err)
	}
	if len(tr.top.list) != 1 || types.SparseHashBytes() != before {
		t.Fatal("failed duplication published a node or performed hashing")
	}
}

func TestMergeDiffHashWorkScope(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
  list l { key "k"; leaf k { type bits { bit high { position 4294967294; } } } }
}`)
	after, _, err := parseJSONString(set, `{"m:l":[{"k":""}]}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	src, err := Diff(newTree(set), after, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	key := src.top.first().kids.list[0]
	// dupR hashes the key when it is linked and again at the list's unconditional
	// final rehash (tree_data.c:2317).
	// This hand-built test set has no schema owner. The destination tree computes
	// the list's immutable module/name prefix once alongside the two key hashes.
	hashing := 2*types.HashLYBWorkBound(key.value) + uint64(len(key.parent.schema.Module.Name)+len(key.parent.schema.Name)) + 3
	if hashing < 2 {
		t.Fatalf("unexpected sparse-key work bound %d", hashing)
	}

	for _, tc := range []struct {
		name  string
		merge func(dst *Tree) error
	}{
		{"MergeDiff", func(dst *Tree) error { return dst.MergeDiff(src, MergeDiffOptions{}) }},
		{"MergeDiffTree", func(dst *Tree) error {
			return dst.MergeDiffTree(nil, src.top.first(), MergeDiffOptions{})
		}},
	} {
		// The limit is that hashing plus the undo journal of the merge (the destination's top
		// level and the copied nodes), a few units charged with it.
		probe := newTree(set)
		if err := tc.merge(probe); err != nil {
			t.Fatal(err)
		}
		limit := probe.work.hashUsed
		if limit < hashing || limit > hashing+64 {
			t.Fatalf("%s charged %d, want the hashing %d and a small journal", tc.name, limit, hashing)
		}
		t.Run(tc.name+" repeated", func(t *testing.T) {
			dst := newTree(set)
			dst.work.hashLimit = limit
			for i := range 8 {
				if err := tc.merge(dst); err != nil {
					t.Fatalf("merge %d: %v", i, err)
				}
				if dst.top.len() != 1 {
					t.Fatalf("merge %d published %d nodes", i, dst.top.len())
				}
				if err := dst.top.first().Remove(); err != nil {
					t.Fatal(err)
				}
			}
		})
		t.Run(tc.name+" recovery", func(t *testing.T) {
			dst := newTree(set)
			dst.work.hashLimit = limit - 1
			if err := tc.merge(dst); !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("under-budget merge error = %v", err)
			}
			if dst.top.len() != 0 {
				t.Fatal("failed merge published a partial node")
			}
			dst.work.hashLimit = limit
			if err := tc.merge(dst); err != nil {
				t.Fatalf("retry after budget failure: %v", err)
			}
			if dst.top.len() != 1 {
				t.Fatal("successful retry did not publish the diff node")
			}
		})
	}
}
