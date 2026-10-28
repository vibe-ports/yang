// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

// The NewPath mutation journal (design 12): a multi-segment NewPath records what it changes and a
// refusal restores those records. These tests sweep every hash-work unit of production NewPath
// calls (no trace in the probes), run the same calls with neither a trace nor a limit, and
// compare the history after a refusal, a retry and a further mutation with an unrefused control.

// npjUnionWidth is the member count of the wide key type: types.CompareCost charges a union
// comparison by its width.
const npjUnionWidth = 1024

func npjModule() string {
	var u strings.Builder
	for i := range npjUnionWidth - 1 {
		fmt.Fprintf(&u, "type enumeration {enum e%d;} ", i)
	}
	u.WriteString("type string;")
	return `module stage3-npj {yang-version 1.1;namespace "urn:stage3-npj";prefix s;
 import ietf-inet-types {prefix inet;}
 typedef wide {type union {` + u.String() + `}}
 container c {
  list u {key k; leaf k {type wide;} leaf v {type uint8;}}
  list p {key k; leaf k {type inet:ipv4-address;} leaf v {type uint8;}}
  list s {key k; leaf k {type string;} leaf v {type uint8;}}
  leaf-list w {type string;}
  list nk {config false; leaf a {type uint8;}}
  leaf-list st {config false; type string;}
  container m {leaf x {type uint8;}}
  container d {leaf y {type string;}}
 }
 list r {key k; leaf k {type string;} leaf v {type uint8;}}}`
}

type npjCase struct {
	name, data   string
	parent       string // NewPathOptions.Parent, found by path; "" none
	path, value  string
	created      string // the first created node, absent after a value failure (a root list beside a parent: "beside name key")
	then, thenAt string // a further NewPath (path, parent) after the operation
	prep         func(*Tree)
}

func npjCases() []npjCase {
	long := func(c byte) string { return strings.Repeat(string(c), 4096) }
	var ws strings.Builder
	for i := range 12 {
		fmt.Fprintf(&ws, "<w>w%02d</w>", i)
	}
	var ss strings.Builder
	for i := range 15 {
		fmt.Fprintf(&ss, "<s><k>k%d</k></s>", i)
	}
	var out []npjCase
	for _, v := range []struct{ suffix, value string }{{"fail", "300"}, {"ok", "30"}} {
		out = append(out,
			npjCase{name: "union-" + v.suffix, data: `<c xmlns="urn:stage3-npj"><u><k>1</k></u><u><k>2</k></u></c>`,
				path: "/stage3-npj:c/u[k='3']/v", value: v.value, created: "/stage3-npj:c/u[k='3']",
				then: "/stage3-npj:c/u[k='0']/v"},
			npjCase{name: "plugin-" + v.suffix, data: `<c xmlns="urn:stage3-npj"><p><k>192.0.2.1</k></p><p><k>192.0.2.2</k></p></c>`,
				path: "/stage3-npj:c/p[k='192.0.2.3']/v", value: v.value, created: "/stage3-npj:c/p[k='192.0.2.3']",
				then: "/stage3-npj:c/p[k='192.0.2.0']/v"},
			npjCase{name: "long-" + v.suffix, data: `<c xmlns="urn:stage3-npj"><s><k>` + long('a') + `</k></s><s><k>` + long('b') + `</k></s></c>`,
				path: "/stage3-npj:c/s[k='" + long('z') + "']/v", value: v.value, created: "/stage3-npj:c/s[k='" + long('z') + "']",
				then: "/stage3-npj:c/s[k='" + long('0') + "']/v"},
			npjCase{name: "long-top-" + v.suffix, data: `<r xmlns="urn:stage3-npj"><k>` + long('a') + `</k></r><r xmlns="urn:stage3-npj"><k>` + long('b') + `</k></r>`,
				path: "/stage3-npj:r[k='" + long('z') + "']/v", value: v.value, created: "/stage3-npj:r[k='" + long('z') + "']",
				then: "/stage3-npj:r[k='" + long('0') + "']/v"},
			// the first retained tree of a run is built by this insertion (rbPos create)
			npjCase{name: "first-tree-" + v.suffix, data: `<c xmlns="urn:stage3-npj"><s><k>m</k></s></c>`,
				path: "/stage3-npj:c/s[k='a']/v", value: v.value, created: "/stage3-npj:c/s[k='a']",
				then: "/stage3-npj:c/s[k='b']/v"},
			// a new minimum moves the run's metadata to it (transferRBGroup)
			npjCase{name: "new-leader-" + v.suffix, data: `<c xmlns="urn:stage3-npj"><s><k>m</k></s><s><k>n</k></s><s><k>o</k></s></c>`,
				path: "/stage3-npj:c/s[k='a']/v", value: v.value, created: "/stage3-npj:c/s[k='a']",
				then: "/stage3-npj:c/s[k='0']/v"},
			// an absolute root list beside a nested parent: markMixed switches the destination
			npjCase{name: "mixed-indexed-" + v.suffix, data: `<c xmlns="urn:stage3-npj">` + ws.String() + `<d><y>1</y></d></c>`,
				parent: "/stage3-npj:c/d", path: "/stage3-npj:r[k='x']/v", value: v.value, created: "beside r x",
				then: "/stage3-npj:r[k='y']/v", thenAt: "/stage3-npj:c/d"},
			npjCase{name: "mixed-unindexed-" + v.suffix, data: `<c xmlns="urn:stage3-npj"><d><y>1</y></d></c>`,
				parent: "/stage3-npj:c/d", path: "/stage3-npj:r[k='x']/v", value: v.value, created: "beside r x",
				then: "/stage3-npj:r[k='y']/v", thenAt: "/stage3-npj:c/d"},
			// a container created between indexed siblings
			npjCase{name: "container-" + v.suffix, data: `<c xmlns="urn:stage3-npj">` + ws.String() + `<d><y>1</y></d></c>`,
				path: "/stage3-npj:c/m/x", value: v.value, created: "/stage3-npj:c/m",
				then: "/stage3-npj:c/w", thenAt: ""},
			// an empty destination (nil maps), and an empty tree (an unbound top level)
			npjCase{name: "empty-" + v.suffix, data: `<c xmlns="urn:stage3-npj"/>`,
				path: "/stage3-npj:c/s[k='a']/v", value: v.value, created: "/stage3-npj:c/s[k='a']",
				then: "/stage3-npj:c/s[k='b']/v"},
			npjCase{name: "empty-tree-" + v.suffix,
				path: "/stage3-npj:c/s[k='a']/v", value: v.value, created: "/stage3-npj:c",
				then: "/stage3-npj:c/s[k='b']/v"},
			// the fourth child creates the children table (hashAdd, captured whole)
			npjCase{name: "table-" + v.suffix, data: `<c xmlns="urn:stage3-npj"><s><k>m</k></s><s><k>n</k></s><w>w</w></c>`,
				path: "/stage3-npj:c/s[k='a']/v", value: v.value, created: "/stage3-npj:c/s[k='a']",
				then: "/stage3-npj:c/s[k='b']/v"},
			// a retained tree whose insertion recolours and rotates; the deque's middle and end
			npjCase{name: "rotate-middle-" + v.suffix, data: `<c xmlns="urn:stage3-npj">` + ss.String() + `</c>`,
				path: "/stage3-npj:c/s[k='k3x']/v", value: v.value, created: "/stage3-npj:c/s[k='k3x']",
				then: "/stage3-npj:c/s[k='k5x']/v"},
			// the third instance rotates at the graph's root
			npjCase{name: "root-rotate-" + v.suffix, data: `<c xmlns="urn:stage3-npj"><s><k>m</k></s><s><k>n</k></s></c>`,
				path: "/stage3-npj:c/s[k='o']/v", value: v.value, created: "/stage3-npj:c/s[k='o']",
				then: "/stage3-npj:c/s[k='p']/v"},
			npjCase{name: "rotate-end-" + v.suffix, data: `<c xmlns="urn:stage3-npj">` + ss.String() + `</c>`,
				path: "/stage3-npj:c/s[k='kz']/v", value: v.value, created: "/stage3-npj:c/s[k='kz']",
				then: "/stage3-npj:c/s[k='kzz']/v"},
		)
	}
	// keyless state instances appended to an exact chain (its last record), one whose ordered
	// index is built (recordOrderInsert's path), then their child
	for _, v := range []string{"300", "30"} {
		out = append(out, npjCase{name: "chain-" + v, data: `<c xmlns="urn:stage3-npj"><nk><a>1</a></nk><nk><a>1</a></nk><s><k>s</k></s><s><k>t</k></s><s><k>u</k></s></c>`,
			path: "/stage3-npj:c/nk/a", value: v, then: "/stage3-npj:c/nk/a"})
		out = append(out, npjCase{name: "indexed-chain-" + v, data: `<c xmlns="urn:stage3-npj"><nk><a>1</a></nk><nk><a>1</a></nk><nk><a>1</a></nk><s><k>s</k></s></c>`,
			path: "/stage3-npj:c/nk/a", value: v, then: "/stage3-npj:c/nk/a", prep: func(tr *Tree) {
				for _, c := range tr.top.At(0).kids.ht {
					if c.count == 3 {
						next := c.first
						c.root = recordOrderBuild(&next, c.count, nil)
					}
				}
			}})
	}
	return out
}

func npjFind(t *testing.T, tr *Tree, path string) *Node {
	t.Helper()
	if path == "" {
		return nil
	}
	n, err := tr.Find(path)
	if err != nil {
		t.Fatalf("find %s: %v", path, err)
	}
	return n
}

// npjCreated finds c.created: a path, or "beside name key" for a root list put beside c.parent,
// which no schema path addresses.
func npjCreated(t *testing.T, tr *Tree, created, parent string) *Node {
	t.Helper()
	rest, beside := strings.CutPrefix(created, "beside ")
	if !beside {
		return npjFind(t, tr, created)
	}
	name, key, _ := strings.Cut(rest, " ")
	for n := range npjFind(t, tr, parent).parent.kids.all() {
		if n.schema.Name == name && n.kids.SchemaLen() > 0 && n.kids.At(0).Value() == key {
			return n
		}
	}
	return nil
}

func npjOp(t *testing.T, tr *Tree, path, value, parent string) error {
	t.Helper()
	o := NewPathOptions{}
	if parent != "" {
		if o.Parent = npjFind(t, tr, parent); o.Parent == nil {
			t.Fatalf("parent %s not found", parent)
		}
	}
	_, err := tr.NewPath(path, value, o)
	return err
}

// npjStructure is a pointer-free dump of everything a later operation can observe: the data, and
// per sibling set its order, generation, registration counters and history, exact chains in
// order with their ordered index, first-instance hints and records, contexts, metadata owners
// and every retained graph's shape (pre-order with colour and size).
func npjStructure(tr *Tree) []string {
	out := typedDump(tr)
	path := func(n *Node) string {
		if n == nil {
			return "-"
		}
		return n.Path()
	}
	var walk func(x *dirtyOrderNode) string
	walk = func(x *dirtyOrderNode) string {
		if x == nil {
			return "."
		}
		return fmt.Sprintf("(%s %t %d %t %s %s)", path(x.node), x.red, x.size, x.active, walk(x.left), walk(x.right))
	}
	var avl func(r *record) string
	avl = func(r *record) string {
		if r == nil {
			return "."
		}
		return fmt.Sprintf("(%s %d %s %s)", path(r.node), r.height, avl(r.left), avl(r.right))
	}
	set := func(where string, s *siblings) {
		var lines []string
		for _, c := range s.ht {
			var rs []string
			for r := c.first; r != nil; r = r.next {
				rs = append(rs, fmt.Sprintf("%s:%d:%v", path(r.node), r.order, r.more))
			}
			lines = append(lines, fmt.Sprintf("ht %d %v %s", c.count, rs, avl(c.root)))
		}
		for n, byHash := range s.history {
			var hs []string
			for h, r := range byHash {
				hs = append(hs, fmt.Sprintf("%d:%d:%v", h, r.order, r.more))
			}
			sort.Strings(hs)
			lines = append(lines, fmt.Sprintf("history %s %v", path(n), hs))
		}
		for sn, c := range s.leaders {
			var rs []string
			for r := c.first; r != nil; r = r.next {
				rs = append(rs, fmt.Sprintf("%s:%d", path(r.node), r.seq))
			}
			lines = append(lines, fmt.Sprintf("leaders %s %v", sn.Name, rs))
		}
		for sn, n := range s.firstRun {
			lines = append(lines, fmt.Sprintf("first %s %s", sn.Name, path(n)))
		}
		for n := range s.rbGroups {
			d := s.rbOrders[s.rbGroups[n]]
			var more []string
			for _, g := range s.rbMore[n] {
				if o := s.rbOrders[g]; o != nil {
					more = append(more, walk(o.root))
				}
			}
			graph := "nil"
			if d != nil {
				graph = walk(d.root)
			}
			lines = append(lines, fmt.Sprintf("rb %s tree=%t unsorted=%t %s more=%v", path(n), s.rbTree[n], s.unsorted[n], graph, more))
		}
		for n, groups := range s.rbDual {
			lines = append(lines, fmt.Sprintf("dual %s %d", path(n), len(groups)))
		}
		sort.Strings(lines)
		var kids []string
		for _, n := range s.schemaSpan().Collect() {
			kids = append(kids, path(n))
		}
		out = append(out, fmt.Sprintf("%s: gen=%d order=%d seq=%d unordered=%t contexts=%d entries=%d/%d/%d/%d kids=%v %v",
			where, s.gen, s.exactOrder, s.leaderSeq, s.schemaUnordered, len(s.contexts), len(s.ht), len(s.exactByNode),
			len(s.history), len(s.leaderByNode), kids, lines))
	}
	set("top", &tr.top)
	for _, root := range tr.top.nodes() {
		for n := range root.allAny() {
			set(n.Path(), childStorage(n))
			out = append(out, fmt.Sprintf("node %s flags=%d hash=%d hashed=%t rb=%t key=%d/%d", n.Path(), n.flags, n.hash, n.hashed, n.inRB, n.keyN, n.lkeyN))
		}
	}
	return out
}

func npjSet(t *testing.T) *schema.Set {
	t.Helper()
	return reviewDiffSet(t, npjModule())
}

// TestStage3NewPathJournalSweep refuses multi-segment NewPath calls at every hash-work unit
// (YANG_STAGE3_FUZZ_FULL=1; by default a long sweep is sampled): wide
// union keys, plugin (inet) keys, 4 KiB keys, a run's first retained tree, a new run leader,
// mixed marking into indexed and unindexed destinations, and a container between indexed
// siblings, each with a value the last segment accepts and one it rejects. Every refusal must
// leave the tree as it was (snapshotHashBudgetTree: topology, records, history, generations,
// contexts); a retry, then a further NewPath, must reproduce the unrefused history exactly.
// The same calls run with neither a trace nor a limit: a rejected value never strands what the
// call created nor turns into a budget error.
func TestStage3NewPathJournalSweep(t *testing.T) {
	set := npjSet(t)
	for _, c := range npjCases() {
		t.Run(c.name, func(t *testing.T) {
			fails := c.value == "300"
			build := func() *Tree {
				tr := newTree(set)
				if c.data != "" {
					tr = parseOnlyXML(t, set, c.data)
				}
				if c.prep != nil {
					c.prep(tr)
				}
				return tr
			}
			// control: production configuration, neither a trace nor a limit
			ctrl := build()
			err0 := npjOp(t, ctrl, c.path, c.value, c.parent)
			switch {
			case errors.Is(err0, yang.ErrBudget):
				t.Fatalf("ordinary call: %v", err0)
			case fails && err0 == nil:
				t.Fatal("an out-of-range value was accepted")
			case !fails && err0 != nil:
				t.Fatalf("ordinary call: %v", err0)
			}
			if ctrl.work.journal != nil {
				t.Fatal("journal left open")
			}
			if n := npjCreated(t, ctrl, c.created, c.parent); c.created != "" && fails == (n != nil) {
				t.Fatalf("created node %s present=%t after err %v", c.created, n != nil, err0)
			}
			checkTreeRetainedRB(t, ctrl)
			if err := npjOp(t, ctrl, c.then, "1", c.thenAt); err != nil {
				t.Fatalf("then: %v", err)
			}
			want := npjStructure(ctrl)
			// a trace observes the same path
			traced := build()
			var points []uint64
			traced.work.hashTrace = &points
			if err := npjOp(t, traced, c.path, c.value, c.parent); fzErrText(err) != fzErrText(err0) {
				t.Fatalf("traced: %v, want %v", err, err0)
			}
			need := traced.work.hashUsed
			traced.work.hashTrace = nil
			if err := npjOp(t, traced, c.then, "1", c.thenAt); err != nil {
				t.Fatalf("traced then: %v", err)
			}
			if got := npjStructure(traced); !reflect.DeepEqual(got, want) {
				t.Fatalf("traced history differs:\n%s", npjDiff(got, want))
			}
			// every unit; without YANG_STAGE3_FUZZ_FULL=1 a long sweep keeps its first and last 256
			// units and every 53rd (8 and every 1021st under the race detector)
			edge, stride := uint64(256), uint64(53)
			if raceEnabled {
				edge, stride = 8, 1021
			}
			var limits []uint64
			for limit := uint64(1); limit < need; limit++ {
				if fzFull || limit <= edge || need-limit <= edge || limit%stride == 0 {
					limits = append(limits, limit)
				}
			}
			refusals := 0
			for _, limit := range limits {
				tr := build()
				before := snapshotHashBudgetTree(tr)
				tr.work.hashLimit = limit
				err := npjOp(t, tr, c.path, c.value, c.parent)
				tr.work.hashLimit = 0
				if !errors.Is(err, yang.ErrBudget) {
					if fzErrText(err) != fzErrText(err0) {
						t.Fatalf("limit %d/%d: %v, want %v or ErrBudget", limit, need, err, err0)
					}
				} else {
					refusals++
					if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after, before) {
						t.Fatalf("limit %d/%d: refusal changed the tree:\n%s", limit, need, fzSnapDiff(before, after))
					}
					checkTreeRetainedRB(t, tr)
					if err := npjOp(t, tr, c.path, c.value, c.parent); fzErrText(err) != fzErrText(err0) {
						t.Fatalf("limit %d/%d: retry: %v, want %v", limit, need, err, err0)
					}
				}
				if err := npjOp(t, tr, c.then, "1", c.thenAt); err != nil {
					t.Fatalf("limit %d/%d: then: %v", limit, need, err)
				}
				if got := npjStructure(tr); !reflect.DeepEqual(got, want) {
					t.Fatalf("limit %d/%d: history differs from the unrefused one:\n%s", limit, need, npjDiff(got, want))
				}
			}
			if refusals == 0 {
				t.Fatalf("no refusal in %d limits", need)
			}
			t.Logf("%d of %d limits, %d refusals", len(limits), need-1, refusals)
		})
	}
}

func npjDiff(got, want []string) string {
	var out []string
	for i := range max(len(got), len(want)) {
		var g, w string
		if i < len(got) {
			g = got[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if g != w {
			out = append(out, fmt.Sprintf("  got  %.400s\n  want %.400s", g, w))
		}
	}
	return strings.Join(out, "\n")
}

// TestStage3NewPathMixedDefaultQuota is r13's mixed-marking case at the default quota: an
// absolute root list put beside a nested parent among 20,000 siblings, keyed by 4 KiB. The
// destination switches to unordered lookups (markMixed) before anything is created; the call must
// complete (or fail on its value alone) instead of refusing after that switch.
func TestStage3NewPathMixedDefaultQuota(t *testing.T) {
	set := npjSet(t)
	var in strings.Builder
	in.WriteString(`<c xmlns="urn:stage3-npj">`)
	for i := range 20000 {
		fmt.Fprintf(&in, "<w>w%05d</w>", i)
	}
	in.WriteString(`<d><y>1</y></d></c>`)
	key := strings.Repeat("k", 4096)
	for _, value := range []string{"3", "300"} {
		tr := parseOnlyXML(t, set, in.String())
		err := npjOp(t, tr, "/stage3-npj:r[k='"+key+"']/v", value, "/stage3-npj:c/d")
		if errors.Is(err, yang.ErrBudget) || (value == "3") != (err == nil) {
			t.Fatalf("value %s: %.300v", value, err)
		}
		if n := npjCreated(t, tr, "beside r "+key, "/stage3-npj:c/d"); (n != nil) != (value == "3") {
			t.Fatalf("value %s: created list present=%t", value, n != nil)
		}
		checkTreeRetainedRB(t, tr)
	}
}

// TestStage3NewPathJournalCapturesWrites: an ordinary NewPath into a wide indexed destination (a
// keyed list instance with a retained tree, a leaf-list instance, a container among them) never
// copies the destination set whole, with or without a trace or limit; the work it charges does
// not grow with the destination beyond its own search and shift. A run's first tree, mixed
// marking and the cleanup of a rejected value are the bulk transitions that may.
func TestStage3NewPathJournalCapturesWrites(t *testing.T) {
	set := npjSet(t)
	build := func(n int) *Tree {
		var in strings.Builder
		in.WriteString(`<c xmlns="urn:stage3-npj">`)
		for i := range n {
			fmt.Fprintf(&in, "<s><k>s%05d</k></s><w>w%05d</w>", 2*i, 2*i)
		}
		in.WriteString(`</c>`)
		return parseOnlyXML(t, set, in.String())
	}
	defer func() { captureHook = nil }()
	for _, mode := range []string{"ordinary", "traced", "limited"} {
		for _, path := range []string{"/stage3-npj:c/s[k='s01001']/v", "/stage3-npj:c/w[.='w01001']", "/stage3-npj:c/m/x"} {
			tr := build(raceScale(2000, 2))
			dest := &tr.top.At(0).kids
			whole := 0
			captureHook = func(s *siblings) {
				if s == dest {
					whole++
				}
			}
			var points []uint64
			switch mode {
			case "traced":
				tr.work.hashTrace = &points
			case "limited":
				tr.work.hashLimit = 1 << 40
			}
			value := "1"
			if strings.HasSuffix(path, "]") {
				value = ""
			}
			if err := npjOp(t, tr, path, value, ""); err != nil {
				t.Fatalf("%s %s: %v", mode, path, err)
			}
			captureHook = nil
			if whole != 0 {
				t.Errorf("%s %s: the %d-sibling destination was captured whole %d times", mode, path, dest.Len(), whole)
			}
		}
	}
	// entries follow writes: beyond the forward insertion's own work (measured on NewList, which
	// creates and links the same instance without a journal) the call charges a bounded amount.
	// The new maximum is appended (no shifted slots, whose undo is prepaid with them).
	beyond := func(n int) uint64 {
		var in strings.Builder
		in.WriteString(`<c xmlns="urn:stage3-npj">`)
		for i := range n {
			fmt.Fprintf(&in, "<s><k>s%05d</k></s>", i)
		}
		in.WriteString(`</c>`)
		tr, ref := parseOnlyXML(t, set, in.String()), parseOnlyXML(t, set, in.String())
		if err := npjOp(t, tr, "/stage3-npj:c/s[k='s99999']/v", "1", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ref.NewList(ref.top.At(0), "s", []string{"s99999"}, NewOptions{}); err != nil {
			t.Fatal(err)
		}
		return tr.work.hashUsed - min(tr.work.hashUsed, ref.work.hashUsed)
	}
	wide := raceScale(8000, 4)
	if small, large := beyond(250), beyond(wide); large > small+128 {
		t.Errorf("NewPath's work beyond the forward insertion grows with the destination: %d at 250 siblings, %d at %d", small, large, wide)
	}
}

// TestStage3NewPathUpdateParity: the update branch (_lyd_change_term, no journal, as SetValue)
// refuses identically with and without a trace: every refusal leaves the tree as it was, and a
// retry gives the unrefused history.
func TestStage3NewPathUpdateParity(t *testing.T) {
	set := npjSet(t)
	// a state leaf-list instance among indexed siblings: its new value is hashed and reindexed
	const data = `<c xmlns="urn:stage3-npj"><st>a</st><st>b</st><st>c</st><s><k>m</k></s><w>a</w></c>`
	const path = "/stage3-npj:c/st[.='b']"
	op := func(tr *Tree) error {
		_, err := tr.NewPath(path, "q", NewPathOptions{Update: true})
		return err
	}
	ctrl := parseOnlyXML(t, set, data)
	err0 := op(ctrl)
	want := npjStructure(ctrl)
	need, refusals := ctrl.work.hashUsed, 0
	for limit := uint64(1); limit < need; limit++ {
		if !raceSweep(limit, need) {
			continue
		}
		var outcome [2][]string
		for i, traced := range []bool{false, true} {
			tr := parseOnlyXML(t, set, data)
			var points []uint64
			if traced {
				tr.work.hashTrace = &points
			}
			before := snapshotHashBudgetTree(tr)
			tr.work.hashLimit = limit
			err := op(tr)
			tr.work.hashLimit, tr.work.hashTrace = 0, nil
			if errors.Is(err, yang.ErrBudget) {
				if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after, before) {
					t.Fatalf("traced=%t limit %d/%d: refusal changed the tree:\n%s", traced, limit, need, fzSnapDiff(before, after))
				}
				refusals++
				outcome[i] = append(outcome[i], "refused")
				err = op(tr)
			}
			if fzErrText(err) != fzErrText(err0) {
				t.Fatalf("traced=%t limit %d: %v, want %v", traced, limit, err, err0)
			}
			got := npjStructure(tr)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("traced=%t limit %d: history differs:\n%s", traced, limit, npjDiff(got, want))
			}
			outcome[i] = append(outcome[i], got...)
		}
		if !reflect.DeepEqual(outcome[0], outcome[1]) {
			t.Fatalf("limit %d: a trace changes the outcome", limit)
		}
	}
	if refusals == 0 {
		t.Fatalf("no refusal in %d limits", need)
	}
}

// TestStage3NewPathDeepDefaultChain: an explicit leaf created under 48 default NP containers
// clears the default flag of every ancestor (npContDfltDel), each pre-existing ancestor captured
// once; every refusal restores all of them, and the charged work grows linearly with the depth
// (NewPath classifies its own new nodes without walking their ancestry).
func TestStage3NewPathDeepDefaultChain(t *testing.T) {
	build := func(depth int) (*schema.Set, string) {
		var m, p strings.Builder
		m.WriteString(`module stage3-deep {yang-version 1.1;namespace "urn:stage3-deep";prefix s;`)
		for i := range depth {
			fmt.Fprintf(&m, "container c%d {", i)
			if i == 0 {
				p.WriteString("/stage3-deep:c0")
			} else {
				fmt.Fprintf(&p, "/c%d", i)
			}
		}
		m.WriteString(`leaf x {type uint8;} leaf d {type string; default "z";}`)
		m.WriteString(strings.Repeat("}", depth) + "}")
		return reviewDiffSet(t, m.String()), p.String()
	}
	used := map[int]uint64{}
	for _, depth := range []int{12, 48} {
		set, chain := build(depth)
		fresh := func() *Tree {
			tr := newTree(set)
			if _, err := tr.NewPath(chain, "", NewPathOptions{}); err != nil {
				t.Fatal(err)
			}
			if tr.top.At(0).flags&FlagDefault == 0 {
				t.Fatal("setup: the chain is not default")
			}
			return tr
		}
		ctrl := fresh()
		if _, err := ctrl.NewPath(chain+"/x", "1", NewPathOptions{}); err != nil {
			t.Fatal(err)
		}
		used[depth] = ctrl.work.hashUsed
		for n := range ctrl.top.At(0).All() {
			if n.schema.Kind == schema.Container && n.flags&FlagDefault != 0 {
				t.Fatalf("depth %d: %s stays default", depth, n.Path())
			}
		}
		want := npjStructure(ctrl)
		for limit := uint64(1); limit < used[depth]; limit++ {
			tr := fresh()
			before := snapshotHashBudgetTree(tr)
			tr.work.hashLimit = limit
			_, err := tr.NewPath(chain+"/x", "1", NewPathOptions{})
			tr.work.hashLimit = 0
			if err == nil {
				continue
			}
			if !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("depth %d limit %d: %v", depth, limit, err)
			}
			if after := snapshotHashBudgetTree(tr); !reflect.DeepEqual(after, before) {
				t.Fatalf("depth %d limit %d: refusal changed the tree:\n%s", depth, limit, fzSnapDiff(before, after))
			}
			if _, err := tr.NewPath(chain+"/x", "1", NewPathOptions{}); err != nil {
				t.Fatalf("depth %d limit %d: retry: %v", depth, limit, err)
			}
			if got := npjStructure(tr); !reflect.DeepEqual(got, want) {
				t.Fatalf("depth %d limit %d: history differs:\n%s", depth, limit, npjDiff(got, want))
			}
		}
	}
	if used[48] > 5*used[12] {
		t.Errorf("charged work is not linear in the depth: %d at 12, %d at 48", used[12], used[48])
	}
}
