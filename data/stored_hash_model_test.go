// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

const prefixModelSchema = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  container r { list l { config false; key "%s";
    leaf b { type string; } leaf x { type string; } leaf a { type string; }
  } }
}`

func prefixModelCopy(t *testing.T) (*Tree, *Node) {
	t.Helper()
	srcSet := opIndexSet(t, fmt.Sprintf(prefixModelSchema, "b a"))
	dstSet := opIndexSet(t, fmt.Sprintf(prefixModelSchema, "a b"))
	src, _, err := parseJSONString(srcSet, `{"m:r":{"l":[{"b":"B","a":"A"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	dst := newTree(dstSet)
	root, err := dst.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	list, _, err := dst.Dup(src.top.first().kids.list[0], root, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	return dst, list
}

func prefixNames(n *Node) string {
	var b strings.Builder
	for _, c := range n.kids.list {
		b.WriteString(c.Name())
	}
	return b.String()
}

func TestStoredHashNonkeyInsertionPrefix(t *testing.T) {
	tr, list := prefixModelCopy(t)
	if got := prefixNames(list); got != "ba" {
		t.Fatalf("copy children %q", got)
	}
	historical := list.hash
	if _, err := tr.NewTerm(list, "x", "X", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := prefixNames(list); got != "bxa" || list.keyN != 1 || list.lkeyN != 2 ||
		list.lkeyFrozen == nil || list.hash != historical {
		t.Fatalf("non-key insert: children=%q keyN=%d lkeyN=%d frozen=%t hash=%#x (was %#x)",
			got, list.keyN, list.lkeyN, list.lkeyFrozen != nil, list.hash, historical)
	}
	if _, err := tr.NewTerm(list, "a", "A2", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := prefixNames(list); got != "abxa" || list.keyN != 2 {
		t.Fatalf("key insert: children=%q keyN=%d", got, list.keyN)
	}
	if got, err := tr.Find("/m:r/l[a='A2'][b='B']"); err != nil || got != list {
		t.Fatalf("Find complete key predicates = %v, %v", got, err)
	}
	if want := referenceLeadingHash(list); list.hash != want {
		t.Fatalf("stored hash %#x, fresh leading hash %#x", list.hash, want)
	}
}

func TestStoredHashNonkeyDisplacementBudget(t *testing.T) {
	tr, list := prefixModelCopy(t)
	child := modelFreshTerm(t, list, "x", "X")
	tr.work.hashLimit = 1
	before := list.hash
	if err := tr.link(list, &list.kids, child, 1); !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("link error = %v, want ErrBudget", err)
	}
	if prefixNames(list) != "ba" || list.keyN != 2 || list.lkeyFrozen != nil || list.hash != before {
		t.Fatal("failed insertion changed the list prefix or its stored hash")
	}
}

func referenceLeadingHash(n *Node) uint32 {
	h := types.HashMulti32(0, []byte(n.schema.Module.Name))
	h = types.HashMulti32(h, []byte(n.schema.Name))
	for _, c := range n.kids.list {
		if !c.isKey() {
			break
		}
		h = types.HashMulti32(h, []byte(c.Value()))
	}
	return types.FinishHash32(h)
}

type prefixCapture struct {
	node  *Node
	value string
}

// The model records values at native hash events. Its calculations read child
// order directly; they never use keyN, lkeyN, snapshotKeys, or indexed lookup.
type prefixReference struct {
	captured []prefixCapture
	frozen   bool
	stored   uint32
	running  uint32
}

func (m *prefixReference) rehash(n *Node) {
	m.captured = nil
	m.running = types.HashMulti32(types.HashMulti32(0, []byte(n.schema.Module.Name)), []byte(n.schema.Name))
	for _, c := range n.kids.list {
		if !c.isKey() {
			break
		}
		m.captured = append(m.captured, prefixCapture{c, c.Value()})
		m.running = types.HashMulti32(m.running, []byte(c.Value()))
	}
	m.stored = types.FinishHash32(m.running)
	m.frozen = false
}

func (m *prefixReference) displaced(at int) {
	if at < len(m.captured) {
		m.frozen = true
	}
}

func completePositionalKeys(n *Node) bool {
	for i, sn := range n.schema.Keys {
		if i >= len(n.kids.list) || n.kids.list[i].schema != sn {
			return false
		}
	}
	return true
}

func (m *prefixReference) check(t *testing.T, n *Node, step string) {
	t.Helper()
	keyN := 0
	for _, c := range n.kids.list {
		if !c.isKey() {
			break
		}
		keyN++
	}
	running := types.HashMulti32(types.HashMulti32(0, []byte(n.schema.Module.Name)), []byte(n.schema.Name))
	for _, old := range m.captured {
		running = types.HashMulti32(running, []byte(old.value))
	}
	stored := types.FinishHash32(running)
	if n.keyN != keyN || n.lkeyN != len(m.captured) || n.lkeyOK != true ||
		(n.lkeyFrozen != nil) != m.frozen || n.lkeyPrefix != running || n.hash != stored ||
		m.running != running || m.stored != stored {
		t.Fatalf("%s children=%q: keyN=%d want %d, lkeyN=%d want %d, frozen=%t want %t, prefix=%#x want %#x, hash=%#x want %#x",
			step, prefixNames(n), n.keyN, keyN, n.lkeyN, len(m.captured), n.lkeyFrozen != nil, m.frozen,
			n.lkeyPrefix, running, n.hash, stored)
	}
	if m.frozen {
		for i, old := range m.captured {
			if n.lkeyFrozen[i] != old.node {
				t.Fatalf("%s: captured key %d changed", step, i)
			}
		}
	} else {
		for i, old := range m.captured {
			if n.kids.list[i] != old.node {
				t.Fatalf("%s: live key %d changed", step, i)
			}
		}
	}
}

func checkPrefixFind(t *testing.T, tr *Tree, states map[*Node]*prefixReference, step string) {
	t.Helper()
	root := tr.top.first()
	queries := [][2]string{{"A", "B"}, {"A2", "B"}, {"B", "A"}, {"other", "other"}}
	for _, n := range root.kids.list {
		if n.schema.Kind != schema.List || !completePositionalKeys(n) {
			continue
		}
		queries = append(queries, [2]string{n.kids.list[0].Value(), n.kids.list[1].Value()})
	}
	for _, vals := range queries {
		h := types.HashMulti32(types.HashMulti32(0, []byte("m")), []byte("l"))
		for _, value := range vals {
			h = types.HashMulti32(h, []byte(value))
		}
		queryHash := types.FinishHash32(h)
		var expected *Node
		for _, n := range root.kids.list {
			if n.schema.Kind != schema.List || states[n].stored != queryHash || len(n.kids.list) < 2 {
				continue
			}
			if n.kids.list[0].schema == n.schema.Keys[0] && n.kids.list[0].Value() == vals[0] &&
				n.kids.list[1].schema == n.schema.Keys[1] && n.kids.list[1].Value() == vals[1] {
				expected = n
				break
			}
		}
		path := fmt.Sprintf("/m:r/l[a='%s'][b='%s']", vals[0], vals[1])
		got, err := tr.Find(path)
		if err != nil || got != expected {
			t.Fatalf("%s Find(%s)=%v, %v; linear positional scan=%v", step, path, got, err, expected)
		}
	}
}

func modelTree(t *testing.T, srcOrder, dstOrder string) (*Tree, *Node) {
	t.Helper()
	model := strings.Replace(prefixModelSchema, "leaf x { type string; }", "leaf-list x { type string; ordered-by user; }", 1)
	srcSet := opIndexSet(t, fmt.Sprintf(model, srcOrder))
	dstSet := opIndexSet(t, fmt.Sprintf(model, dstOrder))
	src, _, err := parseJSONString(srcSet, `{"m:r":{"l":[{"b":"B","a":"A"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	dst := newTree(dstSet)
	root, err := dst.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	list, _, err := dst.Dup(src.top.first().kids.list[0], root, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	return dst, list
}

func modelFreshTerm(t *testing.T, n *Node, name, value string) *Node {
	t.Helper()
	var sn *schema.Node
	for _, child := range n.schema.Children {
		if child.Name == name {
			sn = child
			break
		}
	}
	if sn == nil {
		t.Fatalf("missing schema child %s", name)
	}
	v, d := types.StoreOnly(sn.Type, value, types.FormatJSON, types.HintData, nil, sn)
	if d != nil {
		t.Fatal(d.Msg)
	}
	c := newTerm(sn, v)
	return c
}

func modelNewTerm(t *testing.T, tr *Tree, n *Node, name, value string, at int) *Node {
	t.Helper()
	c := modelFreshTerm(t, n, name, value)
	if err := tr.link(n, &n.kids, c, at); err != nil {
		t.Fatal(err)
	}
	return c
}

func runPrefixSequence(t *testing.T, input []byte) {
	t.Helper()
	tr, n := modelTree(t, "b a", "a b")
	m := &prefixReference{}
	m.rehash(n)
	m.check(t, n, "initial cross-context copy")
	states := map[*Node]*prefixReference{n: m}
	for i, b := range input {
		step := fmt.Sprintf("step %d op %d", i, b%12)
		switch b % 12 {
		case 0, 1, 2: // NewTerm at its schema anchor.
			name := []string{"a", "b", "x"}[b%3]
			value := []string{"A", "B", "A2", "B2", "X"}[int(b/10)%5]
			c, err := tr.NewTerm(n, name, value, NewOptions{})
			if err != nil {
				t.Fatalf("%s: %v", step, err)
			}
			m.displaced(slices.Index(n.kids.list, c))
			if c.isKey() && completePositionalKeys(n) {
				m.rehash(n)
			}
		case 3, 4: // Direct child insertion before or after an arbitrary position.
			name := []string{"x", "a", "b"}[int(b/10)%3]
			at := int(b/30) % (len(n.kids.list) + 1)
			m.displaced(at)
			c := modelNewTerm(t, tr, n, name, "A2", at)
			if c.isKey() && completePositionalKeys(n) {
				m.rehash(n)
			}
		case 5: // Remove a non-key, or internally unlink a key.
			if len(n.kids.list) > 0 {
				at := int(b/10) % len(n.kids.list)
				m.displaced(at)
				c := n.kids.list[at]
				if c.isKey() {
					unlink(c)
				} else if err := c.Remove(); err != nil {
					t.Fatalf("%s: %v", step, err)
				}
			}
		case 6: // Key value replacement is an unconditional native rehash.
			var keys []*Node
			for _, c := range n.kids.list {
				if c.isKey() {
					keys = append(keys, c)
				}
			}
			if len(keys) > 0 {
				c := keys[int(b/10)%len(keys)]
				value := []string{"A", "B", "A2", "B2"}[int(b/40)%4]
				if c.Value() != value {
					if _, err := c.SetValue(value); err != nil {
						t.Fatalf("%s: %v", step, err)
					}
					m.rehash(n)
				}
			}
		case 7: // Recursive and nonrecursive copies start a new native hash history.
			clone := newTree(tr.set)
			root, err := clone.NewInner(nil, "m:r", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			copied, _, err := clone.Dup(n, root, DupOptions{Recursive: b&16 != 0})
			if err != nil {
				t.Fatalf("%s: %v", step, err)
			}
			tr, n = clone, copied
			m.rehash(n)
			states = map[*Node]*prefixReference{n: m}
		case 8: // A new cross-context copy restores the reversed-key starting shape.
			tr, n = modelTree(t, "b a", "a b")
			m.rehash(n)
			states = map[*Node]*prefixReference{n: m}
		case 9: // Merge into a tree with a distinct list instance.
			src, _, err := parseJSONString(tr.set, `{"m:r":{"l":[{"a":"other","b":"other"}]}}`, Reject, true)
			if err != nil {
				t.Fatalf("%s: %v", step, err)
			}
			if err := tr.Merge(src); err != nil {
				t.Fatalf("%s: %v", step, err)
			}
		case 10, 11: // Explicit before/after insertion among user-ordered non-keys.
			var xs []*Node
			for _, c := range n.kids.list {
				if c.Name() == "x" {
					xs = append(xs, c)
				}
			}
			if len(xs) > 0 {
				anchor := xs[int(b/12)%len(xs)]
				at := slices.Index(n.kids.list, anchor)
				fresh := modelFreshTerm(t, n, "x", "X")
				var err error
				if b%12 == 10 {
					err = tr.insertBefore(anchor, fresh)
				} else {
					at++
					err = tr.insertAfter(anchor, fresh)
				}
				if err != nil {
					t.Fatalf("%s: %v", step, err)
				}
				m.displaced(at)
			}
		}
		for _, list := range tr.top.first().kids.list {
			if list.schema.Kind != schema.List {
				continue
			}
			ref := states[list]
			if ref == nil {
				ref = &prefixReference{}
				ref.rehash(list) // merge created and hashed this instance
				states[list] = ref
			}
			ref.check(t, list, step)
		}
		checkPrefixFind(t, tr, states, step)
	}
}

func TestStoredHashPrefixModel(t *testing.T) {
	for _, seed := range []uint64{1, 257, 4096, 65537} {
		rng := rand.New(rand.NewPCG(seed, 14)) //nolint:gosec // deterministic model sequence
		input := make([]byte, 180)
		for i := range input {
			input[i] = byte(rng.Uint32()) //nolint:gosec // test input deliberately uses low random bits
		}
		t.Run(fmt.Sprint(seed), func(t *testing.T) { runPrefixSequence(t, input) })
	}
}

// Removing x from [a,b,x,b,a] exposes both trailing keys. The live prefix is
// longer than the declaration, so its length must not gate the exposure scan.
func TestStoredHashPrefixExposeRepeatedKeys(t *testing.T) {
	runPrefixSequence(t, []byte{3, 96, 121, 173})
}

func FuzzStoredHashPrefixModel(f *testing.F) {
	f.Add([]byte{8, 2, 0, 3, 1, 6, 5, 7, 9})
	f.Add([]byte{3, 13, 23, 33, 43, 53, 63, 73})
	f.Add([]byte{7, 17, 27, 37, 47, 57, 67, 77})
	f.Add([]byte{3, 96, 121, 173})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 192 {
			input = input[:192]
		}
		runPrefixSequence(t, input)
	})
}

const splitRunModelSchema = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  container r { leaf-list u { type uint32; %s } leaf sep { type string; } leaf tail { type string; } }
}`

type splitLydsReference struct {
	runs    [2][]int
	indexed [2]bool
	records []int // run ids, in native first-instance record insertion order
	joined  bool
}

func (m *splitLydsReference) moveRecordBack(id int) {
	m.records = append(slices.DeleteFunc(m.records, func(other int) bool { return other == id }), id)
}

func (m *splitLydsReference) insert(value int) {
	id := m.records[0]
	run := m.runs[id]
	if !m.indexed[id] {
		oldFirst := run[0]
		slices.Sort(run)
		m.indexed[id] = true
		if run[0] != oldFirst {
			m.moveRecordBack(id)
		}
	}
	oldFirst := run[0]
	at, _ := slices.BinarySearch(run, value)
	run = slices.Insert(run, at, value)
	m.runs[id] = run
	if value < oldFirst {
		m.moveRecordBack(id)
	}
}

func (m *splitLydsReference) removeSelectedLeader() int {
	id := m.records[0]
	value := m.runs[id][0]
	m.runs[id] = m.runs[id][1:]
	if len(m.runs[id]) == 0 {
		m.records = slices.Delete(m.records, 0, 1)
	} else {
		m.moveRecordBack(id)
	}
	return value
}

func (m *splitLydsReference) order() []string {
	var out []string
	for _, v := range m.runs[0] {
		out = append(out, fmt.Sprintf("u=%d", v))
	}
	if !m.joined {
		out = append(out, "sep=x")
	}
	for _, v := range m.runs[1] {
		out = append(out, fmt.Sprintf("u=%d", v))
	}
	return out
}

// The reference keeps sorting state on each run and first-instance records in
// insertion order, as lyds metadata and lyd_insert_hash_add do. The copied
// user-ordered runs have no tree; a sorted insertion builds only the selected
// run's tree. Removing its leader appends the replacement record behind the
// other run's record, switching the run chosen by the next insertion.
func runSplitOrderModel(t *testing.T, input []byte) {
	t.Helper()
	if len(input) == 0 {
		return
	}
	base := 1000 + int(input[0])*10
	values := []int{base + 2, base + 4, base + 8, base + 6}
	ref := &splitLydsReference{runs: [2][]int{{values[0], values[1]}, {values[2], values[3]}}, records: []int{0, 1}}
	srcSet := opIndexSet(t, fmt.Sprintf(splitRunModelSchema, "ordered-by user;"))
	dstSet := opIndexSet(t, fmt.Sprintf(splitRunModelSchema, ""))
	src := newTree(srcSet)
	root, err := src.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for j, v := range values {
		if j == 2 {
			sep := modelFreshTerm(t, root, "sep", "x")
			if err := src.link(root, &root.kids, sep, len(root.kids.list)); err != nil {
				t.Fatal(err)
			}
		}
		u := modelFreshTerm(t, root, "u", fmt.Sprint(v))
		if err := src.link(root, &root.kids, u, len(root.kids.list)); err != nil {
			t.Fatal(err)
		}
	}
	dst := newTree(dstSet)
	copyRoot, _, err := dst.Dup(root, nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	dst.work.visits, dst.work.shifts = true, true
	checkWork := func(step string, before int64) {
		t.Helper()
		// Split runs may scan their own slots to locate a physical boundary,
		// but no edit may walk every earlier sibling without accounting for it.
		bound := int64(64 * (len(copyRoot.kids.list) + 1))
		if used := dst.work.Load() - before; used <= 0 || used > bound ||
			dst.work.hashUsed > uint64(bound) {
			t.Fatalf("%s: split-run work=%d hash charge=%d, bound=%d",
				step, used, dst.work.hashUsed, bound)
		}
	}
	check := func(step string) {
		t.Helper()
		var got []string
		for _, n := range copyRoot.kids.list {
			got = append(got, n.Name()+"="+n.Value())
		}
		want := ref.order()
		if !slices.Equal(got, want) {
			t.Fatalf("%s input=%v: sibling order %v, from-scratch lyds %v", step, input, got, want)
		}
	}
	insert := func(value int, step string) {
		t.Helper()
		before := dst.work.Load()
		if _, err := dst.NewTerm(copyRoot, "u", fmt.Sprint(value), NewOptions{}); err != nil {
			t.Fatal(err)
		}
		checkWork(step, before)
		ref.insert(value)
		check(step)
	}
	removeSelected := func(step string) {
		t.Helper()
		before := dst.work.Load()
		value := ref.removeSelectedLeader()
		var node *Node
		for _, n := range copyRoot.kids.list {
			if n.Name() == "u" && n.Value() == fmt.Sprint(value) {
				node = n
				break
			}
		}
		if node == nil {
			t.Fatalf("%s: selected leader %d missing", step, value)
		}
		if err := node.Remove(); err != nil {
			t.Fatal(err)
		}
		checkWork(step, before)
		check(step)
	}
	check("copy")
	insert(base+3, "first insert")
	// SetValue re-links an interior member of the first physical run while a
	// second run of the same schema remains after sep. Its run-leader walk is
	// part of the counted and charged work model too.
	before := dst.work.Load()
	firstMax := copyRoot.kids.list[2]
	if changed, err := firstMax.SetValue(fmt.Sprint(base + 7)); err != nil || !changed {
		t.Fatalf("split-run SetValue: changed=%t error=%v", changed, err)
	}
	checkWork("split-run SetValue", before)
	ref.runs[0][2] = base + 7
	check("split-run SetValue")
	removeSelected("leader switch")
	insert(base+5, "second insert")
	for i, b := range input[1:] {
		switch b % 3 {
		case 0: // A new global minimum replaces the selected leader.
			minimum := ref.runs[0][0]
			for _, run := range ref.runs {
				for _, v := range run {
					minimum = min(minimum, v)
				}
			}
			insert(minimum-1, fmt.Sprintf("step %d new minimum", i))
		case 1: // A new global maximum stays behind its run's existing members.
			maximum := 0
			for _, run := range ref.runs {
				for _, v := range run {
					maximum = max(maximum, v)
				}
			}
			insert(maximum+1, fmt.Sprintf("step %d new maximum", i))
		case 2:
			if len(ref.runs[ref.records[0]]) > 1 {
				removeSelected(fmt.Sprintf("step %d remove leader", i))
			}
		}
	}
	runSingletonSplitOrderModel(t, base)
}

// A recursive cross-context copy can have several physical runs of u. The
// middle u is alone even though other instances of its schema exist elsewhere.
// The reference changes the middle value at its existing position, as
// LYD_NODE_IS_ALONE makes lyd_change_node_value do for this singleton run.
func runSingletonSplitOrderModel(t *testing.T, base int) {
	t.Helper()
	srcSet := opIndexSet(t, fmt.Sprintf(splitRunModelSchema, "ordered-by user;"))
	dstSet := opIndexSet(t, fmt.Sprintf(splitRunModelSchema, ""))
	src := newTree(srcSet)
	root, err := src.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ name, value string }{
		{"u", fmt.Sprint(base + 1)}, {"sep", "x"}, {"u", fmt.Sprint(base + 2)},
		{"tail", "y"}, {"u", fmt.Sprint(base + 3)},
	} {
		if err := src.link(root, &root.kids, modelFreshTerm(t, root, item.name, item.value), len(root.kids.list)); err != nil {
			t.Fatal(err)
		}
	}
	dst := newTree(dstSet)
	copyRoot, _, err := dst.Dup(root, nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{fmt.Sprintf("u=%d", base+1), "sep=x", fmt.Sprintf("u=%d", base+2),
		"tail=y", fmt.Sprintf("u=%d", base+3)}
	check := func(step string) {
		t.Helper()
		var got []string
		for _, n := range copyRoot.kids.list {
			got = append(got, n.Name()+"="+n.Value())
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: copied sibling positions %v, reference %v", step, got, want)
		}
	}
	check("copy")
	middle := copyRoot.kids.list[2]
	if changed, err := middle.SetValue(fmt.Sprint(base + 4)); err != nil || !changed {
		t.Fatalf("middle singleton SetValue: changed=%t error=%v", changed, err)
	}
	want[2] = fmt.Sprintf("u=%d", base+4)
	check("middle singleton SetValue")
	if copyRoot.kids.list[2] != middle {
		t.Fatal("middle singleton moved from reference position 2")
	}
}

func TestStoredHashSplitRunLeaderModel(t *testing.T) {
	runSplitOrderModel(t, []byte{0, 1, 2, 3, 5, 8, 11, 14})
}

// The second copied run is physically adjacent after sep is removed, but its
// instances never joined the first run's RB tree. This reference inserts into
// the tree selected by the native first-instance record, then links after that
// tree's predecessor, leaving the other run's members in place.
func runJoinedSplitOrderModel(t *testing.T, input []byte) {
	t.Helper()
	base := 1000
	values := []int{base + 2, base + 4, base + 8, base + 6}
	ref := &splitLydsReference{runs: [2][]int{{values[0], values[1]}, {values[2], values[3]}}, records: []int{0, 1}}
	srcSet := opIndexSet(t, fmt.Sprintf(splitRunModelSchema, "ordered-by user;"))
	dstSet := opIndexSet(t, fmt.Sprintf(splitRunModelSchema, ""))
	src := newTree(srcSet)
	root, err := src.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for j, v := range values {
		if j == 2 {
			sep := modelFreshTerm(t, root, "sep", "x")
			if err := src.link(root, &root.kids, sep, len(root.kids.list)); err != nil {
				t.Fatal(err)
			}
		}
		u := modelFreshTerm(t, root, "u", fmt.Sprint(v))
		if err := src.link(root, &root.kids, u, len(root.kids.list)); err != nil {
			t.Fatal(err)
		}
	}
	dst := newTree(dstSet)
	copyRoot, _, err := dst.Dup(root, nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	check := func(step string) {
		t.Helper()
		var got []string
		for _, n := range copyRoot.kids.list {
			got = append(got, n.Name()+"="+n.Value())
		}
		if want := ref.order(); !slices.Equal(got, want) {
			t.Fatalf("%s input=%v: order %v, from-scratch lyds reference %v", step, input, got, want)
		}
	}
	insert := func(v int, step string) {
		t.Helper()
		if _, err := dst.NewTerm(copyRoot, "u", fmt.Sprint(v), NewOptions{}); err != nil {
			t.Fatal(err)
		}
		ref.insert(v)
		check(step)
	}
	insert(base+3, "create first tree")
	mode := 0
	if len(input) > 0 {
		mode = int(input[0] % 4)
	}
	bothTrees := mode == 1
	if bothTrees {
		insert(base+1, "select second run")
		insert(base+7, "create second tree")
	} else if mode == 2 {
		insert(base+1, "select unindexed second run")
	}
	var sep *Node
	for _, n := range copyRoot.kids.list {
		if n.Name() == "sep" {
			sep = n
			break
		}
	}
	if sep == nil {
		t.Fatal("separator missing")
	}
	switch {
	case len(input) > 0 && input[0]%3 == 1:
		if err := dst.unlinkAll([]*Node{sep}); err != nil {
			t.Fatal(err)
		}
	case len(input) > 0 && input[0]%3 == 2:
		if _, err := dst.TrimXPath("/m:r/u", XPathOptions{}); err != nil {
			t.Fatal(err)
		}
	default:
		if err := sep.Remove(); err != nil {
			t.Fatal(err)
		}
	}
	ref.joined = true
	check("remove separator")
	if mode == 3 {
		value := ref.removeSelectedLeader()
		var leader *Node
		for _, n := range copyRoot.kids.list {
			if n.Name() == "u" && n.Value() == fmt.Sprint(value) {
				leader = n
				break
			}
		}
		if leader == nil {
			t.Fatalf("selected leader %d missing", value)
		}
		if err := leader.Remove(); err != nil {
			t.Fatal(err)
		}
		check("remove joined leader")
	}
	if bothTrees {
		insert(base+5, "insert between indexed trees")
	} else {
		insert(base+7, "insert between trees")
	}
	for i, b := range input {
		if i == 32 {
			break
		}
		insert(base+100+int(b)*64+i, fmt.Sprintf("joined insert %d", i))
	}
}

func TestStoredHashJoinedSplitRunModel(t *testing.T) {
	runJoinedSplitOrderModel(t, []byte{0, 1, 2, 3})
	runJoinedSplitOrderModel(t, []byte{1, 2, 3})
	runJoinedSplitOrderModel(t, []byte{2, 3, 4})
	runJoinedSplitOrderModel(t, []byte{3, 4, 5})
}

func FuzzStoredHashJoinedSplitRunModel(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{1, 2, 3, 4, 5})
	f.Add([]byte{2, 3, 4, 5, 6})
	f.Add([]byte{3, 4, 5, 6, 7})
	f.Fuzz(func(t *testing.T, input []byte) { runJoinedSplitOrderModel(t, input) })
}

func TestStoredHashSplitRunFirstSiblingWrap(t *testing.T) {
	srcSet := opIndexSet(t, fmt.Sprintf(splitRunModelSchema, "ordered-by user;"))
	dstSet := opIndexSet(t, fmt.Sprintf(splitRunModelSchema, ""))
	src := newTree(srcSet)
	root, err := src.NewInner(nil, "m:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ name, value string }{
		{"u", "1"}, {"sep", "x"}, {"u", "3"},
	} {
		if err := src.link(root, &root.kids, modelFreshTerm(t, root, item.name, item.value), len(root.kids.list)); err != nil {
			t.Fatal(err)
		}
	}
	dst := newTree(dstSet)
	copyRoot, _, err := dst.Dup(root, nil, DupOptions{Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	first := copyRoot.kids.list[0]
	if changed, err := first.SetValue("4"); err != nil || !changed {
		t.Fatalf("wrapped first SetValue: changed=%t error=%v", changed, err)
	}
	var got []string
	for _, n := range copyRoot.kids.list {
		got = append(got, n.Name()+"="+n.Value())
	}
	if want := []string{"sep=x", "u=3", "u=4"}; !slices.Equal(got, want) || copyRoot.kids.list[2] != first {
		t.Fatalf("wrapped first position %v, reference %v", got, want)
	}
}

func FuzzStoredHashSplitRunLeaderModel(f *testing.F) {
	f.Add([]byte{0})
	f.Add([]byte{1, 17, 39, 5, 8, 11})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 32 {
			input = input[:32]
		}
		runSplitOrderModel(t, input)
	})
}

const parseInsertModelSchema = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
  list l { key k;
    leaf k { type uint32; } leaf v { type string; }
  }
}`

type parseInsertItem struct {
	Key uint32 `json:"k"`
	Tag string `json:"v"`
}

// lyds_merge_nodes2_front merges the source RB members ahead of destination
// instances with equal keys. Build that order from the input values, without
// consulting either tree's index or sorting flags.
func runParseInsertModel(t *testing.T, input []byte) {
	t.Helper()
	set := opIndexSet(t, parseInsertModelSchema)
	source := []parseInsertItem{{5, "s0"}, {3, "s1"}}
	for i, b := range input {
		if i == 8 {
			break
		}
		source = append(source, parseInsertItem{uint32(b % 6), fmt.Sprintf("s%d", i+2)})
	}
	dest := []parseInsertItem{{3, "d0"}}
	encode := func(items []parseInsertItem) string {
		t.Helper()
		b, err := json.Marshal(map[string]any{"m:l": items})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	src, _, err := parseJSONString(set, encode(source), Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	dst, _, err := parseJSONString(set, encode(dest), Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	if !src.top.rbTree[src.top.list[0]] ||
		src.top.firstRun[src.top.list[0].schema] != src.top.list[0] || dst.top.rbTree[dst.top.list[0]] {
		t.Fatalf("parse did not attach RB state and firstRun to the source leader (input=%v)", input)
	}
	if err := dst.Insert(nil, src.top.first()); err != nil {
		t.Fatal(err)
	}
	slices.SortStableFunc(source, func(a, b parseInsertItem) int { return int(a.Key) - int(b.Key) })
	reference := slices.Concat(source, dest)
	slices.SortStableFunc(reference, func(a, b parseInsertItem) int { return int(a.Key) - int(b.Key) })
	check := func(step string) {
		t.Helper()
		if !dst.top.rbTree[dst.top.list[0]] || dst.top.firstRun[dst.top.list[0].schema] != dst.top.list[0] {
			t.Fatalf("%s input=%v: RB state or firstRun left the physical leader", step, input)
		}
		want := make([]string, 0, len(reference))
		for _, item := range reference {
			want = append(want, fmt.Sprintf("%d:%s", item.Key, item.Tag))
		}
		got := make([]string, 0, len(dst.top.list))
		for _, n := range dst.top.list {
			got = append(got, n.kids.list[0].Value()+":"+n.kids.list[1].Value())
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s input=%v: order %v, from-scratch lyds reference %v", step, input, got, want)
		}
	}
	check("merge")
	if err := dst.top.list[0].Remove(); err != nil {
		t.Fatal(err)
	}
	reference = reference[1:]
	check("remove leader")
	fresh := parseInsertItem{reference[0].Key, "fresh"}
	n, err := dst.NewList(nil, "m:l", []string{fmt.Sprint(fresh.Key)}, NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dst.NewTerm(n, "v", fresh.Tag, NewOptions{}); err != nil {
		t.Fatal(err)
	}
	reference = append(reference, fresh)
	slices.SortStableFunc(reference, func(a, b parseInsertItem) int { return int(a.Key) - int(b.Key) })
	check("insert after removal")
}

func TestStoredHashParseInsertModel(t *testing.T) {
	runParseInsertModel(t, nil)
	runParseInsertModel(t, []byte{3, 5, 3, 1})
	runMergeAndMiddleRemovalModel(t, []byte{0, 1, 2, 3})
	runRPCLeaderChangesModel(t, []byte{0})
}

func TestStoredHashParserLeaderWithoutTable(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	  rpc r { input { leaf-list v { type uint32; } } } }`)
	tr, op, diags, err := parseOp(context.Background(), strings.NewReader(
		`{"m:r":{"v":[40,20,10]}}`), set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	if op.kids.ht != nil {
		t.Fatal("small run unexpectedly has a children table")
	}
	leader := op.kids.list[0]
	group := op.kids.rbGroups[leader]
	if leader.Value() != "10" || group == nil || !op.kids.rbTree[leader] {
		t.Fatalf("leader=%s group=%p", leader.Value(), group)
	}
	for _, n := range op.kids.list {
		if n.rbGroup != group {
			t.Fatalf("%s belongs to group %p, want %p", n.Value(), n.rbGroup, group)
		}
	}
	if _, err := tr.NewTerm(op, "v", "15", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := []string{op.kids.list[0].Value(), op.kids.list[1].Value(),
		op.kids.list[2].Value(), op.kids.list[3].Value()}; !slices.Equal(got, []string{"10", "15", "20", "40"}) {
		t.Fatalf("values %v", got)
	}
}

// Each new minimum transfers the parser's RB group before the physical run is
// published. A later copy and insertion must still sort against that group.
func runRPCLeaderChangesModel(t *testing.T, input []byte) {
	t.Helper()
	set := opIndexSet(t, `module m { yang-version 1.1; namespace urn:m; prefix m;
	  rpc r { input { leaf a { type string; } leaf b { type string; }
	    leaf c { type string; } leaf-list v { type uint32; } } } }`)
	base := 100 + int(input[0])*100
	jsonValues := func(offsets ...int) string {
		var b strings.Builder
		b.WriteString(`{"m:r":{"a":"a","b":"b","c":"c","v":[`)
		for i, offset := range offsets {
			if i != 0 {
				b.WriteByte(',')
			}
			fmt.Fprint(&b, base+offset)
		}
		b.WriteString(`]}}`)
		return b.String()
	}
	tr, op, diags, err := parseOp(context.Background(), strings.NewReader(
		jsonValues(40, 50, 60, 70, 20, 10)), set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	leader := op.kids.runLeader(op.kids.list[3].schema)
	group := op.kids.rbGroups[leader]
	if leader == nil || leader.Value() != fmt.Sprint(base+10) || group == nil {
		t.Fatalf("logical leader=%v group=%p", leader, group)
	}
	for _, n := range op.kids.list[3:] {
		if !n.inRB || n.rbGroup != group {
			t.Fatalf("initial value %s has group %p, leader group %p", n.Value(), n.rbGroup, group)
		}
	}
	_, source, diags, err := parseOp(context.Background(), strings.NewReader(jsonValues(80, 90)),
		set, FormatJSON, opRPC, nil, Reject)
	if err != nil {
		t.Fatal(err, diags)
	}
	if _, _, err := tr.DupSiblings(source.kids.list[3], op, DupOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.NewTerm(op, "v", fmt.Sprint(base+15), NewOptions{}); err != nil {
		t.Fatal(err)
	}
	var got []int
	for _, n := range op.kids.list {
		if n.schema.Kind == schema.LeafList {
			var v int
			if _, err := fmt.Sscan(n.Value(), &v); err != nil {
				t.Fatal(err)
			}
			got = append(got, v-base)
		}
	}
	if want := []int{10, 15, 20, 40, 50, 60, 70, 80, 90}; !slices.Equal(got, want) {
		t.Fatalf("input=%v: values %v, want %v", input, got, want)
	}
}

// A parsed source tree merges into an untreed destination whose first instance
// remains first. DupSiblings and NewTerm must then use the one surviving tree.
// Separately, removing an instance inside that tree leaves it sorted.
func runMergeAndMiddleRemovalModel(t *testing.T, input []byte) {
	t.Helper()
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
		leaf-list v { type uint32; } }`)
	base := 100 + int(input[0])*10
	values := func(offsets ...int) string {
		var b strings.Builder
		b.WriteString(`{"m:v":[`)
		for i, offset := range offsets {
			if i != 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "%d", base+offset)
		}
		b.WriteString(`]}`)
		return b.String()
	}
	parse := func(offsets ...int) *Tree {
		t.Helper()
		tr, _, err := parseJSONString(set, values(offsets...), Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		return tr
	}
	check := func(tr *Tree, want []int, step string) {
		t.Helper()
		var got []string
		for _, n := range tr.top.list {
			got = append(got, n.Value())
		}
		var expected []string
		for _, v := range want {
			expected = append(expected, fmt.Sprint(base+v))
		}
		if !slices.Equal(got, expected) {
			t.Fatalf("%s input=%v: values %v, reference %v", step, input, got, expected)
		}
		if leader := tr.top.list[0]; tr.top.rbTree[leader] {
			group := tr.top.rbGroups[leader]
			for _, n := range tr.top.list {
				if n.inRB && n.rbGroup != group {
					t.Fatalf("%s input=%v: %s retained a stale RB group", step, input, n.Value())
				}
			}
		}
	}
	src, dst, extra := parse(20, 30), parse(10), parse(40, 50)
	if err := dst.Insert(nil, src.top.first()); err != nil {
		t.Fatal(err)
	}
	check(dst, []int{10, 20, 30}, "merge into untreed destination")
	if _, _, err := dst.DupSiblings(extra.top.first(), nil, DupOptions{}); err != nil {
		t.Fatal(err)
	}
	check(dst, []int{10, 20, 30, 40, 50}, "duplicate siblings")
	if _, err := dst.NewTerm(nil, "m:v", fmt.Sprint(base+35), NewOptions{}); err != nil {
		t.Fatal(err)
	}
	check(dst, []int{10, 20, 30, 35, 40, 50}, "new term")
	middle := newTree(set)
	for _, offset := range []int{10, 20, 30, 40, 50} {
		if _, err := middle.NewTerm(nil, "m:v", fmt.Sprint(base+offset), NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := middle.top.list[2].Remove(); err != nil {
		t.Fatal(err)
	}
	check(middle, []int{10, 20, 40, 50}, "remove middle")
	want := []int{10, 20, 40, 50}
	for i, b := range input {
		if i == 16 {
			break
		}
		v := base + 256 + i*256 + int(b)
		if _, err := middle.NewTerm(nil, "m:v", fmt.Sprint(v), NewOptions{}); err != nil {
			t.Fatal(err)
		}
		want = append(want, 256+i*256+int(b))
		check(middle, want, "ascending append")
	}
}

func FuzzStoredHashMergeAndMiddleRemovalModel(f *testing.F) {
	f.Add([]byte{0})
	f.Add([]byte{0, 1, 2, 3})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) == 0 {
			return
		}
		runMergeAndMiddleRemovalModel(t, input)
	})
}

func TestStoredHashUnsortedLeaderTransfer(t *testing.T) {
	f := newFixture()
	tr, parent := f.cont(t)
	for _, value := range []string{"5", "3"} {
		if err := tr.insert(parent, f.term(t, f.ll, value), insertDefault); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.insert(parent, f.term(t, f.ll, "9"), insertLastBySchema); err != nil {
		t.Fatal(err)
	}
	check := func(step string, old *Node) {
		t.Helper()
		leader := parent.kids.list[0]
		if !parent.kids.rbTree[leader] || !parent.kids.unsorted[leader] ||
			parent.kids.firstRun[f.ll] != leader || parent.kids.rbTree[old] || parent.kids.unsorted[old] {
			t.Fatalf("%s: RB/unsorted state or firstRun did not follow the leader", step)
		}
	}
	old := parent.kids.list[0]
	if err := tr.insert(parent, f.term(t, f.ll, "1"), insertDefault); err != nil {
		t.Fatal(err)
	}
	check("front insert", old)
	old = parent.kids.list[0]
	if err := old.Remove(); err != nil {
		t.Fatal(err)
	}
	check("leader removal", old)
	old = parent.kids.list[0]
	if err := tr.unlinkAll([]*Node{old}); err != nil {
		t.Fatal(err)
	}
	check("batch leader removal", old)
}

func FuzzStoredHashParseInsertModel(f *testing.F) {
	f.Add([]byte{3, 5, 3, 1})
	f.Add([]byte{3})
	f.Fuzz(func(t *testing.T, input []byte) {
		runParseInsertModel(t, input)
		if len(input) != 0 {
			runRPCLeaderChangesModel(t, input)
		}
	})
}
