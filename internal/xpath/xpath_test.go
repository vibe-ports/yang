// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 tests/utests/basic/test_xpath.c (BSD-3-Clause, © CESNET):
// the data and expectations of TestLibyangAxes / TestLibyangPredicate.

package xpath

import (
	"bufio"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFuncTables(t *testing.T) {
	for name := range funcArity {
		if funcImpls[name] == nil {
			t.Errorf("%s has no implementation", name)
		}
	}
	if len(funcImpls) != len(funcArity) {
		t.Error("funcImpls and funcArity differ")
	}
}

func eval(src string, ec EvalContext) (Result, error) {
	e, err := Compile(src, jsonNS{"pv2": true, "pv2-aug": true, "pv2-xp": true, "a": true, "b": true})
	if err != nil {
		return Result{}, err
	}
	return e.Eval(ec)
}

// aTree is the data of libyang tests/utests/basic/test_xpath.c test_axes
// (module a; BSD-3-Clause, © CESNET), without the duplicate key leaf.
func aTree() []Node {
	ll := func(a string, kids ...*tnode) *tnode {
		return list("ll", append([]*tnode{leaf("a", a)}, kids...)...)
	}
	inner := func(a, b string) *tnode { return list("ll", leaf("a", a), leaf("b", b)) }
	return top(
		list("a:l1", leaf("a", "a1"), leaf("b", "b1"), leaf("c", "c1")),
		list("a:l1", leaf("a", "a2"), leaf("b", "b2")),
		list("a:l1", leaf("a", "a3"), leaf("b", "b3"), leaf("c", "c3")),
		cont("a:c", leaf("x", "val"),
			ll("key1", inner("key11", "val11"), inner("key12", "val12"), inner("key13", "val13")),
			ll("key2", inner("key21", "val21"), inner("key22", "val22"))),
	)
}

// TestLibyangAxes: test_axes of libyang test_xpath.c (the C data has a
// duplicate <a>key13</a> that libyang keeps once; it is omitted here).
func TestLibyangAxes(t *testing.T) {
	tree := aTree()
	for _, c := range []struct {
		x string
		n int
	}{
		{"//ll[a and b]/a/ancestor::node()", 8},
		{"//ll[a and b]/ancestor-or-self::ll", 7},
		{"/l1/@operation", 0},
		{"/l1/attribute::key", 0},
		{"/child::l1/child::a", 3},
		{"/descendant::c/descendant::b", 5},
		{"//c", 3},
		{"/descendant-or-self::node()/c", 3},
		{"/c/x/following::a", 7},
		{"/c/x/following-sibling::ll", 2},
		{"/child::*/c/parent::l1", 2},
		{"/child::c//..", 8},
		{"/c/preceding::a", 3},
		{"/c/ll/preceding-sibling::node()", 2},
		{"/c/self::c/ll/ll/b/self::b", 5},
		{"/a:*", 4}, {"/*", 4}, {"//*", 32},
	} {
		r, err := eval(c.x, EvalContext{Tree: tree})
		if err != nil || len(r.Nodes) != c.n {
			t.Errorf("%s: want %d nodes, got %d (%v)", c.x, c.n, len(r.Nodes), err)
		}
	}
}

// TestLibyangPredicate: test_predicate / test_union / test_number of libyang test_xpath.c.
func TestLibyangPredicate(t *testing.T) {
	tree := top(
		leaf("a:foo2", "50"),
		list("a:l1", leaf("a", "a1"), leaf("b", "b1"), leaf("c", "c1")),
		list("a:l1", leaf("a", "a2"), leaf("b", "b2")),
		cont("a:c", leaf("x", "key2"),
			list("ll", leaf("a", "key1"), list("ll", leaf("a", "key11")), list("ll", leaf("a", "key12")), list("ll", leaf("a", "key13"))),
			list("ll", leaf("a", "key2"), list("ll", leaf("a", "key21")), list("ll", leaf("a", "key22"))),
			list("ll", leaf("a", "key3"), list("ll", leaf("a", "key31")), list("ll", leaf("a", "key32")))),
	)
	for _, c := range []struct {
		x    string
		want string // value of the first result node's first child, or count
		n    int
	}{
		{"/foo2[4[3 = 3]]", "", 0},
		{"/c/child::ll[2]/preceding::ll[3]", "key11", 1},
		{"/a:l1[a=concat('a', '1')][b=substring('ab1',2)]", "a1", 1},
		{"/a:c/ll[a=../x]", "key2", 1},
		{"/a:c/ll[a=substring(ll/a,1,4)]", "key1", 3},
		{"/a:c/a:ll[a:a=string(/a:l1[a:a='foo']/a:a)]/a:a", "", 0},
		{"/l1[c[../a = 'a1'] | c]/a", "", 1},
	} {
		r, err := eval(c.x, EvalContext{Tree: tree})
		if err != nil || len(r.Nodes) != c.n {
			t.Errorf("%s: want %d nodes, got %d (%v)", c.x, c.n, len(r.Nodes), err)
			continue
		}
		if c.want != "" && r.Nodes[0].Children()[0].Value().String() != c.want {
			t.Errorf("%s: want %s, got %s", c.x, c.want, path(r.Nodes[0]))
		}
	}
	for _, x := range []string{"25.2 > 1.0", "25.200 = 25.2", "75 < 100", "2 <= 20", "-25.2 < -1.0", "-75 > -100", "-0 = 0"} {
		if r, err := eval(x, EvalContext{Tree: tree}); err != nil || !r.Bool {
			t.Errorf("%s: want true, got %v (%v)", x, r, err)
		}
	}
}

// TestStoredValue: derived-from*, enum-value and bit-is-set read the stored
// (union member) value, never the text (oracle: protocol-v2/xpath-union).
func TestStoredValue(t *testing.T) {
	tree := unionTree()
	ec := EvalContext{Tree: tree, Node: tree[0], Schema: tinfo{tree}}
	for x, want := range map[string]any{
		"derived-from-or-self(id-first, 'pv2:two')":  true,
		"derived-from-or-self(str-first, 'pv2:two')": false,
		"derived-from(id-first, 'pv2:one')":          true,
		"id-first = str-first":                       true,
		"enum-value(en)":                             "NaN",
		"bit-is-set(bi, 'red')":                      true,
	} {
		r, err := eval(x, ec)
		got := resultJSON(r)["value"]
		if err != nil || got != want {
			t.Errorf("%s: want %v, got %v (%v)", x, want, got, err)
		}
	}
}

func TestWhenConfigBudget(t *testing.T) {
	tree := pv2Tree()
	x := find(tree, "x", 0)
	ec := EvalContext{Tree: tree, Node: tree[0]}

	x.when = WhenUnresolved
	if _, err := eval("x", ec); !errors.Is(err, ErrIncomplete) {
		t.Errorf("unresolved when: %v", err)
	}
	ec.IgnoreWhen = true
	if r, _ := eval("x", ec); len(r.Nodes) != 1 {
		t.Errorf("IgnoreWhen: %v", r)
	}
	x.when = WhenFalse
	if r, _ := eval("count(x)", ec); r.Num != 0 {
		t.Errorf("when-false node visible: %v", r)
	}
	if r, _ := eval("count(x)", EvalContext{Tree: tree, Node: x}); r.Num != 0 {
		t.Errorf("when-false node visible from elsewhere: %v", r)
	}
	if r, _ := eval("count(.)", EvalContext{Tree: tree, Node: x}); r.Num != 1 {
		t.Errorf("when-false current node must stay visible: %v", r)
	}
	x.when = WhenTrue

	cfg := EvalContext{Tree: tree, Node: tree[0], Root: RootConfig}
	for src, want := range map[string]float64{"count(stats)": 0, "count(//hits)": 0, "count(l)": 3} {
		if r, err := eval(src, cfg); err != nil || r.Num != want {
			t.Errorf("RootConfig %s: want %v, got %v (%v)", src, want, r.Num, err)
		}
	}
	if r, _ := eval("string(.)", cfg); strings.Contains(r.Str, "    5\n") {
		t.Errorf("RootConfig string-value includes state: %q", r.Str)
	}

	if _, err := eval("count(//*[count(//*) > 0])", EvalContext{Tree: tree, MaxSteps: 100}); !errors.Is(err, ErrBudget) {
		t.Errorf("budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := eval(strings.Repeat("count(//*) + ", 300)+"1", EvalContext{Ctx: ctx, Tree: tree}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
	if _, err := eval("deref(ref)", EvalContext{Tree: tree, Node: tree[0]}); err == nil {
		t.Error("deref without a hook must fail")
	}
}

// TestNumbers covers the number conversions against C semantics.
func TestNumbers(t *testing.T) {
	for in, want := range map[string]string{
		" 12": "12", "12 ": "NaN", "0x1A": "26", "0x": "NaN", "1e3": "1000", "-.5": "-0.5", "inf": "Infinity",
		"-Infinity": "-Infinity", "nan": "NaN", "1_0": "NaN", "": "NaN", "+5": "5", "0X1p4": "16",
	} {
		if got := numToString(cStrtod(in)); got != want {
			t.Errorf("cStrtod(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{"0.25": "0.2", "0.15": "0.2", "0.14999999999999999": "0.1", "2.5": "2.5",
		"-0.5": "-0.5", "100000000000000000000": "100000000000000000000.0", "3": "3",
		"9223372036854775807": "9223372036854775807", "9007199254740993": "9007199254740993"} {
		if got := numToString(cStrtod(in)); got != want {
			t.Errorf("numToString(%s) = %s, want %s", in, got, want)
		}
	}
}

func FuzzEval(f *testing.F) {
	sc := bufio.NewScanner(strings.NewReader(fuzzSeeds))
	for sc.Scan() {
		f.Add(sc.Text())
	}
	f.Fuzz(func(_ *testing.T, src string) {
		e, err := Compile(src, jsonNS{"pv2": true})
		if err != nil {
			return
		}
		tree := pv2Tree()
		_, _ = e.Eval(EvalContext{Tree: tree, Node: tree[0], Schema: tinfo{tree}, Deref: pv2Deref(tree), MaxSteps: 100_000})
	})
}
