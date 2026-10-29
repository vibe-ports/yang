// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// TestDiffUint8ToHexString: lyplg_type_store_hex_string zeroes the value and writes only its
// canonical string, so native lyplg_type_sort_uint reads a zero union for it deterministically.
// A plugin is never plain string storage (R2): an accepted compatibility restriction (D-0121).
func TestDiffUint8ToHexString(t *testing.T) {
	module := func(typ string) string {
		return `module rb-hex { yang-version 1.1; namespace "urn:rb-hex"; prefix r;
  import ietf-yang-types { prefix yang; }
  container c { leaf-list v { type ` + typ + `; } } }`
	}
	first := parseOnlyXML(t, reviewDiffSet(t, module("uint8")), `<c xmlns="urn:rb-hex"><v>1</v></c>`)
	second := parseOnlyXML(t, reviewDiffSet(t, module("yang:hex-string")), `<c xmlns="urn:rb-hex"><v>aa</v></c>`)
	requireD0121Refusal(t, first, second)
	requireD0121Refusal(t, second, first)
}

// TestForeignSchemaIndexKeepsOwnContextIdentity: with mixed siblings [A:output/x, A:input/x, B:z]
// lyd_find_sibling_schema skips A:output/x for A:input/x (same context, other schema pointer).
func TestForeignSchemaIndexKeepsOwnContextIdentity(t *testing.T) {
	const ops = `module rb-op { yang-version 1.1; namespace "urn:rb-op"; prefix r;
  rpc op { input { leaf x { type string; } } output { leaf x { type string; } } } leaf z { type string; } }`
	setA, setB := reviewDiffSet(t, ops), reviewDiffSet(t, ops)
	var in, out, z *schema.Node
	for _, m := range setA.Modules {
		for _, top := range m.Top {
			if top.Name == "op" {
				for sn := range schema.GetNext(top, nil, 0) {
					in = sn
				}
				for sn := range schema.GetNext(top, nil, schema.GetNextOutput) {
					out = sn
				}
			}
		}
	}
	z = retainedRBTopNamed(t, setB, "z")
	if in == nil || out == nil || in == out {
		t.Fatal("rpc input/output x not compiled")
	}
	s := &siblings{}
	mustLink(t, testBuild(s, retainedRBTerm(t, out, "o"), retainedRBTerm(t, in, "i"), retainedRBTerm(t, z, "z")))
	d := newTree(setA)
	end := d.work.beginLookupWork()
	defer end()
	if i := d.foreignSchemaIndex(s, in, false); i != 1 {
		t.Fatalf("lookup of input/x = %d, want 1 (output/x is another schema node of its context)", i)
	}
}

// TestCompatibilityPreflightChargesBeforeComparing: checkInsertCompatible charges each comparison's
// bound before the callback runs, so long values cannot be compared past the remaining quota.
func TestCompatibilityPreflightChargesBeforeComparing(t *testing.T) {
	set := reviewDiffSet(t, `module rb-pre { yang-version 1.1; namespace "urn:rb-pre"; prefix r; leaf-list v { type string; } }`)
	sn := retainedRBTopNamed(t, set, "v")
	long := strings.Repeat("x", 1<<16)
	d := &dirtyOrder{}
	_, _ = d.insertCompatible(retainedRBTerm(t, sn, long+"a"), func(a, b *Node) (int, bool) { return compareSorted(a, b), true })
	var work workCounter
	work.hashLimit = 1 << 10
	calls := 0
	_, err := d.checkInsertCompatible(&work, retainedRBTerm(t, sn, long+"b"), func(a, b *Node) (int, bool) {
		calls++
		return compareSorted(a, b), true
	})
	if !errors.Is(err, yang.ErrBudget) || calls != 0 {
		t.Fatalf("preflight error = %v after %d comparisons, want ErrBudget before any", err, calls)
	}
}

// TestForeignSchemaIndexAllocationBudget: the lookup index grows after each charge, so a refused
// lookup over many siblings does not allocate a table for all of them first.
func TestForeignSchemaIndexAllocationBudget(t *testing.T) {
	set := reviewDiffSet(t, `module rb-alloc { yang-version 1.1; namespace "urn:rb-alloc"; prefix r; leaf-list v { type string; } }`)
	sn := retainedRBTopNamed(t, set, "v")
	s := &siblings{}
	for range 1 << 17 {
		mustLink(t, s.appendNode(retainedRBTerm(t, sn, "x")))
	}
	d := newTree(set)
	d.work.lookupLimit = 1
	end := d.work.beginLookupWork()
	defer end()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if i := d.foreignSchemaIndex(s, sn, false); i != -1 || d.work.lookupErr == nil {
		t.Fatalf("index = %d, lookup error %v: want the budget refusal", i, d.work.lookupErr)
	}
	runtime.ReadMemStats(&after)
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<10 {
		t.Fatalf("refused lookup allocated %d bytes before its charge", grew)
	}
}

// mixedLeaderTree builds the mixed run of #267's repro, as the sequence engine's merge_into does
// (the retained tree merged into freshly parsed data): A's root plus a foreign diff's B:c v=[a]
// are merged into A's c v=[b,c,d,e]. B:a ends up first physically; A:b holds the first
// registered first-instance record.
func mixedLeaderTree(t *testing.T, foreignType string) (*Tree, *Node) {
	t.Helper()
	module := func(typ string) string {
		return `module rb-lead { yang-version 1.1; namespace "urn:rb-lead"; prefix r;
  container c { leaf-list v { type ` + typ + ` } } }`
	}
	// A's root lives in a module ordered before rb-lead, so it stays the tree's first node
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{
		"rb-lead.yang": {Data: []byte(module("string;"))},
		"aa.yang":      {Data: []byte(`module aa { yang-version 1.1; namespace "urn:aa"; prefix a; leaf root { type string; } }`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"aa", "rb-lead"} {
		if _, diags, err := c.Load(name, "", nil); err != nil {
			t.Fatal(err, diags)
		}
	}
	setA, setB := c.Snapshot(), reviewDiffSet(t, module(foreignType))
	tr := parseOnlyXML(t, setA, `<root xmlns="urn:aa">r0</root>`)
	diff, err := Diff(nil, parseOnlyXML(t, setB, `<c xmlns="urn:rb-lead"><v>a</v></c>`), DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	dest := parseOnlyXML(t, setA, `<c xmlns="urn:rb-lead"><v>b</v><v>c</v><v>d</v><v>e</v></c>`)
	if err := dest.Merge(tr); err != nil {
		t.Fatal(err)
	}
	tr = dest
	for _, n := range tr.top.SchemaAll() {
		if n.schema.Name == "c" {
			return tr, n
		}
	}
	t.Fatal("no c")
	return nil, nil
}

func childValues(c *Node) string {
	var got []string
	for _, n := range c.kids.SchemaAll() {
		got = append(got, n.Value())
	}
	return strings.Join(got, " ")
}

// TestPreflightUsesRegisteredLeader: the storage preflight descends the tree of the leader
// lyd_insert_node will use (the registered A:b), not the physical first instance B:a. Both
// contexts use the string profile: a foreign enumeration is refused by D-0121 when it joins.
func TestPreflightUsesRegisteredLeader(t *testing.T) {
	tr, c := mixedLeaderTree(t, "string;")
	if _, err := tr.NewPath("v[.='aa']", "", NewPathOptions{Parent: c}); err != nil {
		t.Fatalf("insert past the foreign leader = %v, want the native insertion", err)
	}
	checkTreeRetainedRB(t, tr)
}

// TestBatchMergeUsesRegisteredLeader is astra's repro: Diff A:c/v=[b,c,d,e] against B:c/v=[a]
// (identical string schemas), then Merge A:c/v=[f,g,h] into that diff. The batch joins the
// registered leader A:b's tree and run, giving libyang's a b c d e f g h.
func TestBatchMergeUsesRegisteredLeader(t *testing.T) {
	const module = `module rb-lead { yang-version 1.1; namespace "urn:rb-lead"; prefix r;
  container c { leaf-list v { type string; } } }`
	setA, setB := reviewDiffSet(t, module), reviewDiffSet(t, module)
	first := parseOnlyXML(t, setA, `<c xmlns="urn:rb-lead"><v>b</v><v>c</v><v>d</v><v>e</v></c>`)
	diff, err := Diff(first, parseOnlyXML(t, setB, `<c xmlns="urn:rb-lead"><v>a</v></c>`), DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := diff.Merge(parseOnlyXML(t, setA, `<c xmlns="urn:rb-lead"><v>f</v><v>g</v><v>h</v></c>`)); err != nil {
		t.Fatal(err)
	}
	if got := childValues(topOf(diff)); got != "a b c d e f g h" {
		t.Fatalf("merged run = %s, want a b c d e f g h", got)
	}
	checkTreeRetainedRB(t, diff)
}

// TestMixedMergeReselectsLeaderPerInsertion: inserting aa makes it A's first instance and replaces
// A:b's first-instance record, so lyd_find_sibling_schema now finds B:a's earlier record and ab
// joins B:a's tree: libyang's a ab aa b c d e f. A batch may not keep A's tree for ab.
func TestMixedMergeReselectsLeaderPerInsertion(t *testing.T) {
	const module = `module rb-lead { yang-version 1.1; namespace "urn:rb-lead"; prefix r;
  container c { leaf-list v { type string; } } }`
	setA, setB := reviewDiffSet(t, module), reviewDiffSet(t, module)
	first := parseOnlyXML(t, setA, `<c xmlns="urn:rb-lead"><v>b</v><v>c</v><v>d</v><v>e</v></c>`)
	diff, err := Diff(first, parseOnlyXML(t, setB, `<c xmlns="urn:rb-lead"><v>a</v></c>`), DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{`<v>f</v>`, `<v>aa</v><v>ab</v>`} {
		if err := diff.Merge(parseOnlyXML(t, setA, `<c xmlns="urn:rb-lead">`+src+`</c>`)); err != nil {
			t.Fatal(err)
		}
	}
	if got := childValues(topOf(diff)); got != "a ab aa b c d e f" {
		t.Fatalf("merged run = %s, want a ab aa b c d e f", got)
	}
	checkTreeRetainedRB(t, diff)
}

// TestDiffUserOrderedConfigToStateSchema: lyd_diff_userord_attrs selects first->schema when first
// exists (diff.c:695). A config user-ordered leaf-list diffed against its state counterpart in
// another context therefore records value/orig-value, not position/orig-position.
func TestDiffUserOrderedConfigToStateSchema(t *testing.T) {
	module := func(config string) string {
		return `module rb-uo { yang-version 1.1; namespace "urn:rb-uo"; prefix r;
  container c { config ` + config + `; leaf-list v { type string; ordered-by user; } } }`
	}
	first := parseOnlyXML(t, reviewDiffSet(t, module("true")), `<c xmlns="urn:rb-uo"><v>a</v><v>b</v></c>`)
	second := parseOnlyXML(t, reviewDiffSet(t, module("false")), `<c xmlns="urn:rb-uo"><v>b</v><v>a</v></c>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := diff.PrintXML(&b, PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	if out := b.String(); !strings.Contains(out, `yang:orig-value="a"`) || strings.Contains(out, "position") {
		t.Fatalf("diff = %s, want value/orig-value attributes", out)
	}
}

// TestBatchMergeHashCharge: a batch charges each comparison's own cost and its rebalancing as it
// runs. Stage 3 additionally reserves publication slots and topology/journal visits that the
// former 65,196-unit main did not charge. Keep the comparison bound and a separate linear
// allowance for those visits, rather than hiding them in an unenforced counter.
func TestBatchMergeHashCharge(t *testing.T) {
	set := reviewDiffSet(t, `module rb-batch { yang-version 1.1; namespace "urn:rb-batch"; prefix r; leaf-list v { type uint32; } }`)
	var b strings.Builder
	for i := range 4096 {
		b.WriteString(`<v xmlns="urn:rb-batch">` + strconv.Itoa(i) + `</v>`)
	}
	src := parseOnlyXML(t, set, b.String())
	dst := newTree(set)
	if err := dst.Merge(src); err != nil {
		t.Fatal(err)
	}
	// comparison bound plus newly charged publication/metadata work, plus the storage's O(log n)
	// insertion of every published node (design 14)
	const bound = 4*65196 + 14*4096 + 64 + 4096*(2*13+4)
	if used := dst.work.hashUsed; used > bound {
		t.Fatalf("4096-value Merge consumed %d hash units, bound %d", used, bound)
	}
	t.Logf("4096-value Merge consumed %d hash units (main 65,196)", dst.work.hashUsed)
}

func manyRunsSet(t *testing.T, runs int) *schema.Set {
	t.Helper()
	var b strings.Builder
	b.WriteString(`module rb-runs { yang-version 1.1; namespace "urn:rb-runs"; prefix r;`)
	for i := range runs {
		b.WriteString(" leaf-list l" + strconv.Itoa(i) + " { type string; }")
	}
	b.WriteString(" }")
	return reviewDiffSet(t, b.String())
}

func manyRunsXML(runs int, values ...string) string {
	var b strings.Builder
	for i := range runs {
		for _, v := range values {
			b.WriteString(`<l` + strconv.Itoa(i) + ` xmlns="urn:rb-runs">` + v + `</l` + strconv.Itoa(i) + `>`)
		}
	}
	return b.String()
}

// TestTrimManyRunsLinear: removing the instances of N runs touches only their own retained trees
// (rbDual), so the removal work grows linearly with N, not as N removals times N trees.
func TestTrimManyRunsLinear(t *testing.T) {
	var previous int64
	for _, runs := range []int{128, 256, 512} {
		tr := parseOnlyXML(t, manyRunsSet(t, runs), manyRunsXML(runs, "a", "b"))
		tr.work.Store(0)
		tr.work.visits = true
		if _, err := tr.TrimXPath("/*[false()]", XPathOptions{}); err != nil {
			t.Fatal(err)
		}
		if tr.top.len() != 0 {
			t.Fatalf("%d runs: %d nodes left after the trim", runs, tr.top.len())
		}
		work := tr.work.Load()
		if previous != 0 && work > 3*previous {
			t.Fatalf("doubling the runs grew trim work from %d to %d", previous, work)
		}
		previous = work
	}
}

// TestMergeBatchesConsumeCumulatively: each batch charges its comparisons as they run, so many
// batches of long common-prefix strings in one Merge exhaust the operation's quota.
func TestMergeBatchesConsumeCumulatively(t *testing.T) {
	const prefixLen = 1024
	values := uint64(512)
	if raceEnabled {
		values = 192 // fewer insertions under the race detector; the floor keeps its form
	}
	prefix := strings.Repeat("x", prefixLen)
	merge := func(runs int, limit uint64) (uint64, error) {
		set := manyRunsSet(t, runs)
		add := make([]string, values)
		for i := range add {
			add[i] = prefix + fmt.Sprintf("%04d", 2*i+1)
		}
		dst := parseOnlyXML(t, set, manyRunsXML(runs, prefix+"0000", prefix+"9999"))
		src := parseOnlyXML(t, set, manyRunsXML(runs, add...))
		dst.work.hashLimit = limit
		err := dst.Merge(src)
		return dst.work.hashUsed, err
	}
	one, err := merge(1, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Insertion i descends a tree of 2+i nodes, at least floor(log2(2+i)) comparisons through the
	// common prefix, and hashes its value once. Measured: base 3,162,141 (below the 4,214,784
	// floor, its replay comparisons uncharged), head 9,827,748.
	var floor uint64
	for size := uint64(2); size < 2+values; size++ {
		for h := size; h > 1; h >>= 1 { // floor(log2(size)) comparisons
			floor += prefixLen
		}
		floor += prefixLen
	}
	if one < floor {
		t.Fatalf("one batch consumed %d units, below its %d comparison and hash floor", one, floor)
	}
	if _, err := merge(8, 3*one); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("8 batches within 3 batches' quota: error %v, want ErrBudget", err)
	}
}

// TestDiffCrossContextLeafListLinear: identical top-level leaf-lists from independently compiled
// contexts take lyd_find_sibling_first's linear cross-context branch for every instance, in both
// passes. The diff-time instance index keeps the matches linear and on the enforced quotas.
func TestDiffCrossContextLeafListLinear(t *testing.T) {
	const module = `module rb-ll { yang-version 1.1; namespace "urn:rb-ll"; prefix r; leaf-list l { type string; } }`
	diff := func(n int, lookupLimit, hashLimit uint64) (*Tree, error) {
		var data strings.Builder
		for i := range n {
			fmt.Fprintf(&data, `<l xmlns="urn:rb-ll">v%d</l>`, i)
		}
		first := parseOnlyXML(t, reviewDiffSet(t, module), data.String())
		second := parseOnlyXML(t, reviewDiffSet(t, module), data.String())
		return diffNodesLookupLimit(topOf(first), topOf(second), DiffOptions{}, false, first, second, lookupLimit, hashLimit)
	}
	sizes := []uint64{1024, 4096}
	if raceEnabled {
		sizes = sizes[:1]
	}
	for _, n := range sizes {
		// linear quotas (measured at 2048 values: 6n lookup, 40n hash units); the per-instance
		// scans charged about 2n*n hash units (8,465,744 at 2048 values)
		if d, err := diff(int(n), 8*n, 64*n); d != nil || err != nil { //nolint:gosec // a small constant
			t.Fatalf("%d values within linear quotas: Diff = (%v, %v)", n, d, err)
		}
	}
	if d, err := diff(1024, 512, 0); d != nil || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("lookup limit below one index build: Diff = (%v, %v), want nil, ErrBudget", d, err)
	}
}

// TestRelocationRefusesDualTrees: native unlink would retain a detached reference in an
// earlier tree. D-0121 refuses before mutation; both that refusal and every earlier budget
// failure preserve all topology and membership histories.
func TestRelocationRefusesDualTrees(t *testing.T) {
	const module = `module rb-lead { yang-version 1.1; namespace "urn:rb-lead"; prefix r;
  container c { leaf-list v { type string; } } }`
	setA, setB := reviewDiffSet(t, module), reviewDiffSet(t, module)
	build := func() (*Tree, *Node) {
		first := parseOnlyXML(t, setA, `<c xmlns="urn:rb-lead"><v>b</v><v>c</v><v>d</v><v>e</v></c>`)
		diff, err := Diff(first, parseOnlyXML(t, setB, `<c xmlns="urn:rb-lead"><v>a</v></c>`), DiffOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := diff.Merge(parseOnlyXML(t, setA, `<c xmlns="urn:rb-lead"><v>f</v></c>`)); err != nil {
			t.Fatal(err)
		}
		c := topOf(diff)
		for _, n := range c.kids.SchemaAll() {
			if len(c.kids.rbDual[n]) != 0 {
				return diff, n
			}
		}
		t.Fatal("fixture has no node with dual tree membership")
		return nil, nil
	}
	state := func(sib *siblings) string {
		var dual []string
		for n, groups := range sib.rbDual {
			dual = append(dual, fmt.Sprintf("%p=%v", n, groups))
		}
		for g, count := range sib.rbDualCount {
			dual = append(dual, fmt.Sprintf("%p#%d", g, count))
		}
		slices.Sort(dual)
		// the retained trees, their leaders and memberships (not the exact-registration counter)
		full := snapshotHashBudgetSiblings(sib)
		_, trees, _ := strings.Cut(full, " rb=")
		return trees + fmt.Sprint(dual)
	}
	// Native would retain a dangling reference: refuse before unlink.
	tr, n := build()
	sib := n.siblingsOf()
	before := state(sib)
	restore, arm, err := tr.linkOrigin(n)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := arm(nil); err == nil || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("dual-member unlink preflight: %v, want D-0121", err)
	}
	restore() // no unlink occurred; recovery must leave the original history alone
	if after := state(sib); after != before {
		t.Fatalf("restored relocation changed the retained trees:\n%s\n%s", before, after)
	}
	checkTreeRetainedRB(t, tr)
	// every budget refusal of the whole relocation, outside SetValue's #257 journal
	for limit := uint64(1); ; limit++ {
		tr, n := build()
		sib := n.siblingsOf()
		before := state(sib)
		v, diag := types.Store(n.schema.Type, "zz", types.FormatJSON, types.JSONHints("string"), nil, n.schema)
		if diag != nil {
			t.Fatal(diag)
		}
		tr.work.hashUsed, tr.work.hashErr, tr.work.hashLimit = 0, nil, limit
		tr.changeNodeValue(n, v, 0)
		err := tr.work.hashErr
		tr.work.hashLimit, tr.work.hashErr = 0, nil
		refused := err != nil && strings.Contains(err.Error(), "D-0121")
		if !errors.Is(err, yang.ErrBudget) && !refused {
			t.Fatalf("limit %d: relocation = %v, want ErrBudget or D-0121", limit, err)
		}
		if after := state(sib); after != before {
			t.Fatalf("limit %d: refused relocation changed the retained trees:\n%s\n%s", limit, before, after)
		}
		checkTreeRetainedRB(t, tr)
		if refused {
			break
		}
	}
}
