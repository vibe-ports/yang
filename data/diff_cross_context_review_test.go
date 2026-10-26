// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"weak"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

func reviewDiffSet(t *testing.T, module string) *schema.Set {
	t.Helper()
	name := strings.Fields(module)[1]
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{
		name + ".yang": {Data: []byte(module)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load(name, "", nil); err != nil {
		t.Fatal(err, diags)
	}
	return c.Snapshot()
}

// TestDiffCrossContextHashAnchorContinuesForeignSiblings covers the children_ht branch of
// lyd_insert_get_next_anchor. lys_getnext receives a foreign last node and a local parent, so it
// follows the foreign node's next sibling before it eventually restarts at the local children.
func TestDiffCrossContextHashAnchorContinuesForeignSiblings(t *testing.T) {
	module := `module review-anchor { yang-version 1.1; namespace "urn:review-anchor"; prefix r;
  container t {
    leaf a { type string; } leaf b { type string; } leaf c { type string; }
    leaf d { type string; } leaf e { type string; }
  }
}`
	a, b := reviewDiffSet(t, module), reviewDiffSet(t, module)
	first := parseOnlyXML(t, a, `<t xmlns="urn:review-anchor"><a>x</a><c>x</c><d>x</d><e>old</e></t>`)
	second := parseOnlyXML(t, b, `<t xmlns="urn:review-anchor"><b>y</b><e>new</e></t>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(topOf(diff).Children()), []string{"e=new", "a=x", "b=y", "c=x", "d=x"}; !slices.Equal(got, want) {
		t.Fatalf("diff order = %v, want %v", got, want)
	}
}

// TestDiffCrossContextSortedRunMissUsesAnchor covers a system-ordered node with no matching run.
// lyd_insert_node still asks lyd_insert_get_next_anchor for its foreign-context schema position.
func TestDiffCrossContextSortedRunMissUsesAnchor(t *testing.T) {
	module := `module review-sorted { yang-version 1.1; namespace "urn:review-sorted"; prefix r;
  container t { leaf a { type string; } leaf-list b { type string; } }
}`
	a, b := reviewDiffSet(t, module), reviewDiffSet(t, module)
	first := parseOnlyXML(t, a, `<t xmlns="urn:review-sorted"><a>x</a></t>`)
	second := parseOnlyXML(t, b, `<t xmlns="urn:review-sorted"><b>y</b></t>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(topOf(diff).Children()), []string{"b=y", "a=x"}; !slices.Equal(got, want) {
		t.Fatalf("diff order = %v, want %v", got, want)
	}
}

// TestDiffCrossContextSortedRunUsesRBTraversal pins rb_insert_node's comparison path after its
// deterministic rotation. int16 reads each int8 union payload at int16 width until inserting -1
// rotates -2 to the root; the final int8 comparator then puts 0 after -1.
func TestDiffCrossContextSortedRunUsesRBTraversal(t *testing.T) {
	module := func(typ string) string {
		return `module review-rb-order { yang-version 1.1; namespace "urn:review-rb-order"; prefix r;
  container top { presence "retain"; leaf-list v { type ` + typ + `; } }
}`
	}
	firstSet, secondSet := reviewDiffSet(t, module("int16")), reviewDiffSet(t, module("int8"))
	first := parseOnlyXML(t, firstSet, `<top xmlns="urn:review-rb-order"><v>1</v></top>`)
	second := parseOnlyXML(t, secondSet, `<top xmlns="urn:review-rb-order"><v>-2</v><v>-1</v><v>0</v></top>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(topOf(diff).Children()), []string{"v=1", "v=-2", "v=-1", "v=0"}; !slices.Equal(got, want) {
		t.Fatalf("diff order = %v, want native RB order %v", got, want)
	}
}

// TestDiffCrossContextIntegerCallbackReadsZeroedStringStorage covers different sort callbacks
// whose C storage comparison is nevertheless defined. string_store zeroes the value union, so
// the int8 callback compares -1 with zero and links string "2" after it.
func TestDiffCrossContextIntegerCallbackReadsZeroedStringStorage(t *testing.T) {
	module := func(typ string) string {
		return `module review-int-string { yang-version 1.1; namespace "urn:review-int-string"; prefix r;
  container top { presence "retain"; leaf-list v { type ` + typ + `; } }
}`
	}
	firstSet, secondSet := reviewDiffSet(t, module("int8")), reviewDiffSet(t, module("string"))
	first := parseOnlyXML(t, firstSet, `<top xmlns="urn:review-int-string"><v>-1</v></top>`)
	second := parseOnlyXML(t, secondSet, `<top xmlns="urn:review-int-string"><v>2</v></top>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(topOf(diff).Children()), []string{"v=-1", "v=2"}; !slices.Equal(got, want) {
		t.Fatalf("diff order = %v, want zero-initialized storage order %v", got, want)
	}
}

// TestDiffCrossContextIntegerCallbackReadsBooleanStorage covers scalar storage with a distinct
// callback. boolean_store writes 1 into the zeroed union, so int8 orders true after -1.
func TestDiffCrossContextIntegerCallbackReadsBooleanStorage(t *testing.T) {
	module := func(typ string) string {
		return `module review-int-boolean { yang-version 1.1; namespace "urn:review-int-boolean"; prefix r;
  container top { presence "retain"; leaf-list v { type ` + typ + `; } }
}`
	}
	firstSet, secondSet := reviewDiffSet(t, module("int8")), reviewDiffSet(t, module("boolean"))
	first := parseOnlyXML(t, firstSet, `<top xmlns="urn:review-int-boolean"><v>-1</v></top>`)
	second := parseOnlyXML(t, secondSet, `<top xmlns="urn:review-int-boolean"><v>true</v></top>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := names(topOf(diff).Children()), []string{"v=-1", "v=true"}; !slices.Equal(got, want) {
		t.Fatalf("diff order = %v, want boolean union payload order %v", got, want)
	}
}

type crossContextBuiltInCase struct {
	name, declaration, first, second string
}

var crossContextBuiltIns = []crossContextBuiltInCase{
	{"binary", "binary", "AQ==", "Ag=="},
	{"uint8", "uint8", "1", "2"}, {"uint16", "uint16", "1", "2"},
	{"uint32", "uint32", "1", "2"}, {"uint64", "uint64", "1", "2"},
	{"string", "string", "a", "b"},
	{"bits", "bits { bit a { position 0; } bit b { position 1; } }", "a", "b"},
	{"boolean", "boolean", "false", "true"},
	{"decimal64", "decimal64 { fraction-digits 1; }", "1.0", "2.0"},
	{"empty", "empty", "", ""},
	{"enumeration", "enumeration { enum a { value 1; } enum b { value 2; } }", "a", "b"},
	{"identityref", "identityref { base root; }", "id-a", "id-b"},
	{"instance-identifier", "instance-identifier { require-instance false; }", "/r:top/r:anchor", "/r:top/r:other"},
	{"leafref", "leafref { path /top/anchor; require-instance false; }", "a", "b"},
	{"union", "union { type int8; type string; }", "-1", "2"},
	{"int8", "int8", "-1", "2"}, {"int16", "int16", "-1", "2"},
	{"int32", "int32", "-1", "2"}, {"int64", "int64", "-1", "2"},
}

func crossContextBuiltInSupported(first, second string) bool {
	switch first {
	case "string", "empty", "instance-identifier", "leafref":
		// lyplg_type_sort_simple reads only canonical strings: defined for any second value
		return true
	case "binary":
		// lyplg_type_sort_binary reads its fixed-memory struct: all-zero string or empty
		// storage is the empty binary
		switch second {
		case "string", "empty", "leafref":
			return true
		}
	}
	store := func(name string) string {
		switch name {
		case "int8", "int16", "int32", "int64":
			return "int"
		case "uint8", "uint16", "uint32", "uint64":
			return "uint"
		case "leafref":
			return "string" // stored using its target realtype
		default:
			return name
		}
	}
	if store(first) == store(second) {
		return true
	}
	scalar := func(name string) bool {
		switch name {
		case "int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64",
			"boolean", "decimal64", "string", "empty", "leafref":
			return true
		}
		return false
	}
	if scalar(first) {
		return scalar(second)
	}
	return false
}

// TestDiffCrossContextPluginStorageRefused covers a string-based plugin that writes fixed_mem.
// Its Base is string, but uint8's callback must not silently read it as the zeroed built-in
// string union. libyang's native read sees the IPv4 address's first byte (192); D-0121 refuses
// this cross-callback state instead of modelling plugin-specific inline storage.
func TestDiffCrossContextPluginStorageRefused(t *testing.T) {
	module := func(typ string) string {
		return `module review-plugin-storage { yang-version 1.1;
  namespace "urn:review-plugin-storage"; prefix r;
  import ietf-inet-types { prefix inet; }
  container top { presence "retain"; leaf-list v { type ` + typ + `; } }
}`
	}
	firstSet := reviewDiffSet(t, module("uint8"))
	secondSet := reviewDiffSet(t, module("inet:ipv4-address-no-zone"))
	first := parseOnlyXML(t, firstSet, `<top xmlns="urn:review-plugin-storage"><v>1</v></top>`)
	second := parseOnlyXML(t, secondSet, `<top xmlns="urn:review-plugin-storage"><v>192.0.2.1</v></top>`)
	diff, err := Diff(first, second, DiffOptions{})
	var ve *ValidationError
	if diff != nil || !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("Diff = (%v, %T %v), want nil, LY_EINVAL D-0121 refusal", diff, err, err)
	}
}

// TestDiffApplyCrossContextTopLevelPluginRun uses independently compiled top-level leaf-lists.
// The compatible inet callback orders addresses numerically, not by their canonical strings.
func TestDiffApplyCrossContextTopLevelPluginRun(t *testing.T) {
	const module = `module review-top-level-inet { yang-version 1.1;
  namespace "urn:review-top-level-inet"; prefix r;
  import ietf-inet-types { prefix inet; }
  leaf-list address { type inet:ipv4-address-no-zone; }
}`
	a, b := reviewDiffSet(t, module), reviewDiffSet(t, module)
	first := parseOnlyXML(t, a, `<address xmlns="urn:review-top-level-inet">192.0.2.2</address>`)
	second := parseOnlyXML(t, b, `<address xmlns="urn:review-top-level-inet">192.0.2.2</address><address xmlns="urn:review-top-level-inet">192.0.2.10</address>`)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.ApplyDiff(diff, ApplyDiffOptions{}); err != nil {
		t.Fatal(err)
	}
	if got, want := names(first.Top()), []string{"address=192.0.2.2", "address=192.0.2.10"}; !slices.Equal(got, want) {
		t.Fatalf("applied top-level order = %v, want inet order %v", got, want)
	}
}

func crossContextBuiltInModule(tc crossContextBuiltInCase) string {
	typeStmt := "type " + tc.declaration + ";"
	if strings.Contains(tc.declaration, "{") {
		typeStmt = "type " + tc.declaration
	}
	return `module review-built-in-matrix { yang-version 1.1;
  namespace "urn:review-built-in-matrix"; prefix r;
  identity root; identity id-a { base root; } identity id-b { base root; }
  container top { presence "retain"; leaf anchor { type string; } leaf other { type string; }
    leaf-list v { ` + typeStmt + ` }
  }
}`
}

func crossContextBuiltInXML(value string) string {
	v := "<v>" + value + "</v>"
	if value == "" {
		v = "<v/>"
	}
	return `<top xmlns="urn:review-built-in-matrix" xmlns:r="urn:review-built-in-matrix"><anchor>a</anchor><other>b</other>` + v + `</top>`
}

// TestDiffCrossContextBuiltInTypeMatrix makes every built-in callback/storage pair explicit.
// Defined storage must diff; incompatible pointer and fixed-buffer reinterpretations must return
// D-0121 instead of quietly using schema order.
func TestDiffCrossContextBuiltInTypeMatrix(t *testing.T) {
	sets := make(map[string]*schema.Set, len(crossContextBuiltIns))
	for _, tc := range crossContextBuiltIns {
		sets[tc.name] = reviewDiffSet(t, crossContextBuiltInModule(tc))
	}
	for _, firstType := range crossContextBuiltIns {
		for _, secondType := range crossContextBuiltIns {
			name := firstType.name + "-" + secondType.name
			t.Run(name, func(t *testing.T) {
				first := parseOnlyXML(t, sets[firstType.name], crossContextBuiltInXML(firstType.first))
				second := parseOnlyXML(t, sets[secondType.name], crossContextBuiltInXML(secondType.second))
				diff, err := Diff(first, second, DiffOptions{})
				if crossContextBuiltInSupported(firstType.name, secondType.name) {
					if err != nil {
						t.Fatalf("defined storage pair refused: %v", err)
					}
					return
				}
				if diff != nil || err == nil || !strings.Contains(err.Error(), "D-0121") {
					t.Fatalf("indeterminate storage pair Diff = (%v, %v), want nil D-0121 refusal", diff, err)
				}
			})
		}
	}
}

// TestDiffCrossContextSchemaLookupScaling verifies that independently compiled snapshots build
// one lineage index per input sibling set instead of rescanning N siblings for every leaf. The
// lowered quota also proves that the public operation's fixed ceiling returns ErrBudget.
func TestDiffCrossContextSchemaLookupScaling(t *testing.T) {
	var previous int64
	for _, n := range []int{64, 128, 256, 512} {
		var module, data strings.Builder
		module.WriteString(`module review-scale { namespace "urn:review-scale"; prefix r; container t {`)
		data.WriteString(`<t xmlns="urn:review-scale">`)
		for i := range n {
			fmt.Fprintf(&module, "leaf v%d { type string; }", i)
			fmt.Fprintf(&data, "<v%d>x</v%d>", i, i)
		}
		module.WriteString("} }")
		data.WriteString("</t>")
		a, b := reviewDiffSet(t, module.String()), reviewDiffSet(t, module.String())
		first, second := parseOnlyXML(t, a, data.String()), parseOnlyXML(t, b, data.String())
		first.work.Store(0)
		second.work.Store(0)
		first.work.visits, second.work.visits = true, true
		if diff, err := Diff(first, second, DiffOptions{}); diff != nil || err != nil {
			t.Fatalf("%d leaves: Diff = (%v, %v)", n, diff, err)
		}
		work := first.work.Load() + second.work.Load()
		if work > int64(16*n) {
			t.Errorf("%d leaves: %d sibling visits, want linear work", n, work)
		}
		if previous != 0 && work > 3*previous {
			t.Errorf("doubling leaves grew work from %d to %d", previous, work)
		}
		previous = work

		if n == 64 {
			diff, err := diffNodesLookupLimit(topOf(first), topOf(second), DiffOptions{}, false, first, second, 32, 0)
			if diff != nil || !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("lookup limit: Diff = (%v, %v), want nil, ErrBudget", diff, err)
			}
		}
	}
}

// TestDiffCrossContextDisjointSchemaLookupScaling covers indexed misses, not only hits. Once the
// foreign lineage index misses, a second sibling scan is both redundant and outside its budget.
func TestDiffCrossContextDisjointSchemaLookupScaling(t *testing.T) {
	const n = 256
	module := func(side string) string {
		var text strings.Builder
		fmt.Fprint(&text, `module review-disjoint { namespace "urn:review-disjoint"; prefix r; container t {`)
		for i := range n {
			fmt.Fprintf(&text, "leaf %s%d { type string; }", side, i)
		}
		fmt.Fprint(&text, "} }")
		return text.String()
	}
	data := func(side string) string {
		var text strings.Builder
		fmt.Fprint(&text, `<t xmlns="urn:review-disjoint">`)
		for i := range n {
			fmt.Fprintf(&text, "<%s%d>x</%s%d>", side, i, side, i)
		}
		fmt.Fprint(&text, "</t>")
		return text.String()
	}
	firstSet, secondSet := reviewDiffSet(t, module("a")), reviewDiffSet(t, module("b"))
	first, second := parseOnlyXML(t, firstSet, data("a")), parseOnlyXML(t, secondSet, data("b"))
	first.work.Store(0)
	second.work.Store(0)
	first.work.visits, second.work.visits = true, true
	if _, err := diffNodesLookupLimit(topOf(first), topOf(second), DiffOptions{}, false, first, second, 8*n, 0); err != nil {
		t.Fatalf("disjoint leaves within linear lookup quota: %v", err)
	}
	if work := first.work.Load() + second.work.Load(); work > 16*n {
		t.Fatalf("%d disjoint leaves: %d foreign sibling visits, want linear work", n, work)
	}
}

// TestDiffCrossContextDeepLineageMemory exercises the default parser's deep, long-identifier
// envelope. Four hashed children share 256 16-KiB ancestry components. Interned parent identities
// keep the Diff allocation linear; concatenated ancestry prefixes exceed a GiB here.
func TestDiffCrossContextDeepLineageMemory(t *testing.T) {
	depth := 256
	if raceEnabled {
		depth = 128 // quadratic ancestry strings would still need 256 MiB
	}
	long := strings.Repeat("n", 16<<10)
	leafPrefix := long[:len(long)-1]

	var module, xml strings.Builder
	module.Grow(depth*len(long) + 1<<20)
	xml.Grow(2*depth*len(long) + 1<<20)
	module.WriteString(`module review-deep-lineage { namespace "urn:review-deep-lineage"; prefix r; `)
	for range depth {
		module.WriteString("container ")
		module.WriteString(long)
		module.WriteString(" {")
		xml.WriteByte('<')
		xml.WriteString(long)
		if xml.Len() == len(long)+1 {
			xml.WriteString(` xmlns="urn:review-deep-lineage"`)
		}
		xml.WriteByte('>')
	}
	for _, suffix := range []byte{'a', 'b', 'c', 'd'} {
		name := leafPrefix + string(suffix)
		module.WriteString("leaf ")
		module.WriteString(name)
		module.WriteString(" { type string; }")
		xml.WriteByte('<')
		xml.WriteString(name)
		xml.WriteString(">x</")
		xml.WriteString(name)
		xml.WriteByte('>')
	}
	for range depth {
		module.WriteString("}")
		xml.WriteString("</")
		xml.WriteString(long)
		xml.WriteByte('>')
	}
	module.WriteByte('}')

	a, b := reviewDiffSet(t, module.String()), reviewDiffSet(t, module.String())
	first, second := parseOnlyXML(t, a, xml.String()), parseOnlyXML(t, b, xml.String())
	deepest := topOf(first)
	for deepest != nil && deepest.kids.first() != nil && deepest.kids.first().schema.Kind == schema.Container {
		deepest = deepest.kids.first()
	}
	if deepest == nil || deepest.kids.ht == nil {
		t.Fatal("deepest four-child sibling set did not build the lineage hash-table path")
	}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	diff, err := Diff(first, second, DiffOptions{})
	runtime.ReadMemStats(&after)
	if diff != nil || err != nil {
		t.Fatalf("Diff = (%v, %v), want equal trees", diff, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<20 {
		t.Fatalf("Diff allocated %d bytes for shared lineage, want linear storage below 64 MiB", allocated)
	}

	diff, err = diffNodesLookupLimit(topOf(first), topOf(second), DiffOptions{}, false, first, second, uint64(depth/2), 0)
	if diff != nil || !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("lineage lookup limit: Diff = (%v, %v), want nil, ErrBudget", diff, err)
	}
}

func diffWithWeakInputs(t *testing.T, set *schema.Set) (*Tree, weak.Pointer[Tree], weak.Pointer[Tree]) {
	t.Helper()
	first := parseOnlyXML(t, set, `<top xmlns="urn:review-retention"><v>old</v></top>`)
	second := parseOnlyXML(t, set, `<top xmlns="urn:review-retention"><v>new</v></top>`)
	firstWeak, secondWeak := weak.Make(first), weak.Make(second)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil || diff == nil {
		t.Fatalf("Diff = (%v, %v), want a retained delta", diff, err)
	}
	return diff, firstWeak, secondWeak
}

// TestDiffResultDoesNotRetainInputs keeps the returned delta live while weakly observing both
// same-context inputs. Diff-time sibling/schema indexes must not escape with the result.
func TestDiffResultDoesNotRetainInputs(t *testing.T) {
	set := reviewDiffSet(t, `module review-retention { yang-version 1.1;
  namespace "urn:review-retention"; prefix r;
  container top { leaf v { type string; } }
}`)
	diff, first, second := diffWithWeakInputs(t, set)
	for range 20 {
		if first.Value() == nil && second.Value() == nil {
			break
		}
		runtime.GC()
		runtime.Gosched()
	}
	if first.Value() != nil || second.Value() != nil {
		t.Fatal("returned diff retains one or both input trees")
	}
	runtime.KeepAlive(diff)
}

func retainedRBNodeCount(root *dirtyOrderNode) int {
	if root == nil {
		return 0
	}
	return 1 + retainedRBNodeCount(root.left) + retainedRBNodeCount(root.right)
}

func checkRetainedRBTopology(t *testing.T, order *dirtyOrder, want int) {
	t.Helper()
	if order == nil {
		if want != 0 {
			t.Fatalf("nil retained topology, want %d nodes", want)
		}
		return
	}
	if order.root != nil && order.root.red {
		t.Error("retained RB root is red")
	}
	seen := make(map[*dirtyOrderNode]bool, want)
	var walk func(*dirtyOrderNode, *dirtyOrderNode) (int, int)
	walk = func(n, parent *dirtyOrderNode) (count, blackHeight int) {
		if n == nil {
			return 0, 1
		}
		if seen[n] {
			t.Fatal("cycle in retained RB topology")
		}
		seen[n] = true
		if n.parent != parent || !n.active || order.nodes[n.node] != n {
			t.Errorf("invalid retained RB node links for %p", n.node)
		}
		if n.red && (dirtyRed(n.left) || dirtyRed(n.right)) {
			t.Errorf("red retained RB node %p has a red child", n.node)
		}
		leftCount, leftBlack := walk(n.left, n)
		rightCount, rightBlack := walk(n.right, n)
		if leftBlack != rightBlack {
			t.Errorf("retained RB node %p black heights %d and %d differ", n.node, leftBlack, rightBlack)
		}
		count = leftCount + rightCount + 1
		if n.size != count {
			t.Errorf("retained RB node %p size=%d, want %d", n.node, n.size, count)
		}
		return count, leftBlack + btoi(!n.red)
	}
	count, _ := walk(order.root, nil)
	if count != want || len(order.nodes) != want {
		t.Fatalf("retained topology reachable=%d mapped=%d, want %d", count, len(order.nodes), want)
	}
}

// TestSortedSetValueRetainedRBTopologyBounded covers lyd_change_term's unlink/reinsert cycle.
// rb_remove_node must remove the old topology entry instead of leaving one tombstone per value.
func TestSortedSetValueRetainedRBTopologyBounded(t *testing.T) {
	set := reviewDiffSet(t, `module review-rb-retention { yang-version 1.1;
  namespace "urn:review-rb-retention"; prefix r; leaf-list v { type uint32; }
}`)
	tr := newTree(set)
	for _, value := range []string{"0", "1"} {
		if _, err := tr.NewTerm(nil, "review-rb-retention:v", value, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	member := tr.top.At(1)
	for i := range 256 {
		if changed, err := member.SetValue(fmt.Sprint(i + 2)); err != nil || !changed {
			t.Fatalf("SetValue %d: changed=%t error=%v", i, changed, err)
		}
	}
	group := tr.top.rbGroups[tr.top.At(0)]
	order := tr.top.rbOrders[group]
	checkRetainedRBTopology(t, order, 2)
	if retainedRBNodeCount(order.root) != 2 {
		t.Fatalf("retained topology: order=%p mapped=%d reachable=%d, want one entry per 2 live nodes",
			order, len(order.nodes), retainedRBNodeCount(order.root))
	}
}

func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}

// TestRetainedRBRemoveRebalances removes every position of a nontrivial topology and checks the
// rb_remove_color invariants after each unlink.
func TestRetainedRBRemoveRebalances(t *testing.T) {
	set := reviewDiffSet(t, `module review-rb-delete { yang-version 1.1;
  namespace "urn:review-rb-delete"; prefix r; leaf-list v { type uint32; }
}`)
	tr := newTree(set)
	const count = 64
	nodes := make([]*Node, count)
	for i := range count {
		n, err := tr.NewTerm(nil, "review-rb-delete:v", fmt.Sprint(i), NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		nodes[i] = n
	}
	group := tr.top.rbGroups[tr.top.At(0)]
	order := tr.top.rbOrders[group]
	checkRetainedRBTopology(t, order, count)
	for removed := range count - 1 {
		i := (removed * 17) % count
		for nodes[i] == nil {
			i = (i + 1) % count
		}
		if err := nodes[i].Remove(); err != nil {
			t.Fatal(err)
		}
		nodes[i] = nil
		checkRetainedRBTopology(t, order, count-removed-1)
	}
}

func removedSortedNodeWeak(t *testing.T, bulk bool) (*Tree, weak.Pointer[Node]) {
	t.Helper()
	set := reviewDiffSet(t, `module review-rb-collection { yang-version 1.1;
  namespace "urn:review-rb-collection"; prefix r; leaf-list v { type uint32; }
}`)
	tr := newTree(set)
	for _, value := range []string{"0", "1"} {
		if _, err := tr.NewTerm(nil, "review-rb-collection:v", value, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	victim := tr.top.At(1)
	observed := weak.Make(victim)
	if bulk {
		if err := tr.unlinkAll([]*Node{victim}); err != nil {
			t.Fatal(err)
		}
	} else if err := victim.Remove(); err != nil {
		t.Fatal(err)
	}
	return tr, observed
}

// TestSortedRemoveReleasesRBNode checks both lyds_unlink callers: single-node removal and the
// validation/free batch path. A surviving instance keeps the group live but not the removed node.
func TestSortedRemoveReleasesRBNode(t *testing.T) {
	for _, tc := range []struct {
		name string
		bulk bool
	}{{"remove", false}, {"unlink-all", true}} {
		t.Run(tc.name, func(t *testing.T) {
			tr, observed := removedSortedNodeWeak(t, tc.bulk)
			for range 20 {
				if observed.Value() == nil {
					break
				}
				runtime.GC()
				runtime.Gosched()
			}
			if observed.Value() != nil {
				t.Fatal("retained RB topology keeps the removed node alive")
			}
			runtime.KeepAlive(tr)
		})
	}
}
