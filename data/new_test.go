// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

// newTestSet compiles module a of libyang's tests/utests/data/test_new.c (BSD-3-Clause, © CESNET).
func newTestSet(t *testing.T) (*schema.Set, *schema.Module) {
	t.Helper()
	src, err := os.ReadFile("testdata/test_new_a.yang")
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, fstest.MapFS{"a.yang": {Data: src}})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load("a", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	return set, set.Implemented("a")
}

// TestNewNodes: lyd_new_term, lyd_new_inner, lyd_new_list/2/3, lyd_new_opaq/2 and lyd_change_term
// against libyang v5.8.6: every call of test_new.c test_top_level and test_opaq, and the further
// branches of the same functions, each with libyang's return code and log (message, data path,
// schema path) as a native libyang 5.8.6 build reports them.
func TestNewNodes(t *testing.T) {
	set, a := newTestSet(t)
	var b newBuilder
	run := func(f func() (*Node, error)) (*Node, string) {
		b = newBuilder{set: set, log: &logger{set: set}}
		n, err := f()
		err = b.log.done(err)
		if err == nil {
			return n, "LY_SUCCESS"
		}
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("not a *ValidationError: %v", err)
		}
		s := ve.RC()
		for _, d := range b.log.diags {
			s += " | " + d.Err + " " + d.Code + " " + d.Msg + " dp=" + d.DataPath + " sp=" + d.SchemaPath
		}
		return n, s
	}
	no := newValOptions{}
	canon, storeOnly := newValOptions{canon: true}, newValOptions{storeOnly: true}
	both := newValOptions{canon: true, storeOnly: true}
	l := func(keys ...string) []string { return keys }
	var rpc, c, c2 *Node
	for _, tc := range []struct {
		name string
		f    func() (*Node, error)
		want string
	}{
		// test_top_level
		{"list", func() (*Node, error) { return b.newList(nil, a, "l1", l("val_a", "val_b"), no, "lyd_new_list") }, "LY_SUCCESS"},
		{"list2 []", func() (*Node, error) { return b.newList2(nil, a, "l1", "[]", no) },
			`LY_EVALID | LY_EVALID LYVE_XPATH Unexpected XPath token "]" ("]"). dp= sp=/a:l1`},
		{"list2 bad key", func() (*Node, error) { return b.newList2(nil, a, "l1", "[key1='a'][key2='b']", no) },
			`LY_ENOTFOUND | LY_EVALID LYVE_XPATH Not found node "key1" in path. dp= sp=/a:l1`},
		{"list2 non-key", func() (*Node, error) { return b.newList2(nil, a, "l1", "[a='a'][b='b'][c='c']", no) },
			`LY_EVALID | LY_EVALID LYVE_XPATH Key expected instead of leaf "c" in path. dp= sp=/a:l1`},
		{"list2 not a list", func() (*Node, error) { return b.newList2(nil, a, "c", "[a='a'][b='b']", no) },
			`LY_ENOTFOUND | LY_EINVAL LYVE_SUCCESS List node "c" not found. dp= sp=`},
		{"list2", func() (*Node, error) { return b.newList2(nil, a, "l1", "[a='a'][b='b']", no) }, "LY_SUCCESS"},
		{"list2 empty", func() (*Node, error) { return b.newList2(nil, a, "l1", "[a=''][b='']", no) }, "LY_SUCCESS"},
		{"list2 prefixed", func() (*Node, error) { return b.newList2(nil, a, "l1", "[a:a='a'][a:b='b']", no) }, "LY_SUCCESS"},
		{"list2 spaces", func() (*Node, error) { return b.newList2(nil, a, "l1", "[a=   'a']\n[b  =\t'b']", no) }, "LY_SUCCESS"},
		{"list3", func() (*Node, error) { return b.newList(nil, a, "l1", l("a", "b"), no, "lyd_new_list3") }, "LY_SUCCESS"},
		{"list3 canon", func() (*Node, error) { return b.newList(nil, a, "l1", l("a", "b"), canon, "lyd_new_list3") }, "LY_SUCCESS"},
		{"list3 canon store-only", func() (*Node, error) { return b.newList(nil, a, "l1", l("a", "b"), both, "lyd_new_list3") },
			`LY_EINVAL | LY_EINVAL LYVE_SUCCESS Invalid argument !(store_only && (format == LY_VALUE_CANON)) (lyd_new_list3()). dp= sp=`},
		{"list canon", func() (*Node, error) { return b.newList(nil, a, "l1", l("val_a", "val_b"), canon, "lyd_new_list") }, "LY_SUCCESS"},
		{"list canon store-only", func() (*Node, error) { return b.newList(nil, a, "l1", l("val_a", "val_b"), both, "lyd_new_list") },
			`LY_EINVAL | LY_EINVAL LYVE_SUCCESS Invalid argument !(store_only && (format == LY_VALUE_CANON)) (lyd_new_list()). dp= sp=`},
		{"term invalid", func() (*Node, error) { return b.newTerm(nil, a, "foo", "[a='a'][b='b'][c='c']", no) },
			`LY_EVALID | LY_EVALID LYVE_DATA Invalid type uint16 value "[a='a'][b='b'][c='c']". dp= sp=/a:foo`},
		{"term not a term", func() (*Node, error) { return b.newTerm(nil, a, "c", "value", no) },
			`LY_ENOTFOUND | LY_EINVAL LYVE_SUCCESS Term node "c" not found. dp= sp=`},
		{"term", func() (*Node, error) { return b.newTerm(nil, a, "foo", "256", no) }, "LY_SUCCESS"},
		{"term canon", func() (*Node, error) { return b.newTerm(nil, a, "foo", "25", canon) }, "LY_SUCCESS"},
		{"term canon store-only", func() (*Node, error) { return b.newTerm(nil, a, "foo", "25", both) },
			`LY_EINVAL | LY_EINVAL LYVE_SUCCESS Invalid argument !(store_only && (format == LY_VALUE_CANON)) (_lyd_new_term()). dp= sp=`},
		{"leaf-list", func() (*Node, error) { return b.newTerm(nil, a, "ll", "ahoy", no) }, "LY_SUCCESS"},
		{"container", func() (*Node, error) { return b.newInner(nil, a, "c", false) }, "LY_SUCCESS"},
		{"inner list", func() (*Node, error) { return b.newInner(nil, a, "l1", false) },
			`LY_ENOTFOUND | LY_EINVAL LYVE_SUCCESS Inner node (container, notif, RPC, or action) "l1" not found. dp= sp=`},
		{"inner keyless list", func() (*Node, error) { return b.newInner(nil, a, "l2", false) },
			`LY_ENOTFOUND | LY_EINVAL LYVE_SUCCESS Inner node (container, notif, RPC, or action) "l2" not found. dp= sp=`},
		{"list2 keyless with keys", func() (*Node, error) { return b.newList2(nil, a, "l2", "[a='a'][b='b']", no) },
			`LY_EVALID | LY_EVALID LYVE_XPATH List predicate defined for keyless list "l2" in path. dp= sp=/a:l2`},
		{"list2 keyless", func() (*Node, error) { return b.newList2(nil, a, "l2", "", no) }, "LY_SUCCESS"},
		{"list keyless", func() (*Node, error) { return b.newList(nil, a, "l2", nil, no, "lyd_new_list") }, "LY_SUCCESS"},
		// further branches (native libyang 5.8.6)
		{"list2 key type", func() (*Node, error) { return b.newList2(nil, a, "l11", "[a='x']", no) },
			`LY_EVALID | LY_EVALID LYVE_DATA Invalid type uint32 value "x". dp= sp=/a:l11/a`},
		{"list2 key prefix", func() (*Node, error) { return b.newList2(nil, a, "l11", "[zz:a='1']", no) },
			`LY_ENOTFOUND | LY_EVALID LYVE_XPATH No module connected with the prefix "zz" found (prefix format JSON module names). dp= sp=/a:l11`},
		{"list2 key missing", func() (*Node, error) { return b.newList2(nil, a, "l1", "[a='a']", no) },
			`LY_EVALID | LY_EVALID LYVE_XPATH Predicate missing for a key of list "l1" in path. dp= sp=/a:l1`},
		{"list key type", func() (*Node, error) { return b.newList(nil, a, "l11", l("x"), no, "lyd_new_list") },
			`LY_EVALID | LY_EVALID LYVE_DATA Invalid type uint32 value "x". dp= sp=/a:l11/a`},
		{"list key type store-only", func() (*Node, error) { return b.newList(nil, a, "l11", l("x"), storeOnly, "lyd_new_list") },
			`LY_EVALID | LY_EVALID LYVE_DATA Invalid type uint32 value "x". dp= sp=/a:l11/a`},
		{"list too few keys", func() (*Node, error) { return b.newList(nil, a, "l1", l("a"), no, "lyd_new_list") },
			`LY_EINVAL | LY_EINVAL LYVE_SUCCESS Invalid argument keys (lyd_new_list()). dp= sp=`},
		{"list nil keys", func() (*Node, error) { return b.newList(nil, a, "l1", nil, no, "lyd_new_list") },
			`LY_EINVAL | LY_EINVAL LYVE_SUCCESS Missing list "l1" keys. dp= sp=`},
		{"list extra keys ignored", func() (*Node, error) { return b.newList(nil, a, "l1", l("a", "b", "c"), no, "lyd_new_list") },
			"LY_SUCCESS"},
		{"list keyless keys ignored", func() (*Node, error) { return b.newList(nil, a, "l2", l("a"), no, "lyd_new_list") },
			"LY_SUCCESS"},
		{"list3 extra keys ignored", func() (*Node, error) { return b.newList(nil, a, "l1", l("a", "b", "c"), no, "lyd_new_list3") },
			"LY_SUCCESS"},
		{"list not a list", func() (*Node, error) { return b.newList(nil, a, "foo", l("x"), no, "lyd_new_list") },
			`LY_ENOTFOUND | LY_EINVAL LYVE_SUCCESS List node "foo" not found. dp= sp=`},
		{"list3 no keys", func() (*Node, error) { return b.newList(nil, a, "l1", nil, no, "lyd_new_list3") },
			`LY_EINVAL | LY_EINVAL LYVE_SUCCESS Missing list "l1" keys. dp= sp=`},
		{"list3 keyless", func() (*Node, error) { return b.newList(nil, a, "l2", nil, no, "lyd_new_list3") }, "LY_SUCCESS"},
		{"term canon invalid", func() (*Node, error) { return b.newTerm(nil, a, "foo", "0x10", canon) },
			`LY_EVALID | LY_EVALID LYVE_DATA Invalid type uint16 value "0x10". dp= sp=/a:foo`},
		{"term store-only range", func() (*Node, error) { return b.newTerm(nil, a, "foo", "70000", storeOnly) },
			`LY_EVALID | LY_EVALID LYVE_DATA Value "70000" is out of type uint16 min/max bounds. dp= sp=/a:foo`},
		{"rpc", func() (*Node, error) { var err error; rpc, err = b.newInner(nil, a, "oper", false); return rpc, err }, "LY_SUCCESS"},
		{"input param", func() (*Node, error) { return b.newTerm(rpc, a, "param", "22", no) }, "LY_SUCCESS"},
		{"output param", func() (*Node, error) { return b.newTerm(rpc, a, "param", "22", newValOptions{output: true}) }, "LY_SUCCESS"},
		{"output param range", func() (*Node, error) { return b.newTerm(rpc, a, "param", "300", newValOptions{output: true}) },
			`LY_EVALID | LY_EVALID LYVE_DATA Value "300" is out of type int8 min/max bounds. dp=/a:oper/param sp=`},
		{"container c", func() (*Node, error) { var err error; c, err = b.newInner(nil, a, "c", false); return c, err }, "LY_SUCCESS"},
		{"term in parent", func() (*Node, error) { return b.newTerm(c, nil, "x", "v1", no) }, "LY_SUCCESS"},
		{"term not in parent", func() (*Node, error) { return b.newTerm(c, nil, "nope", "v1", no) },
			`LY_ENOTFOUND | LY_EINVAL LYVE_SUCCESS Term node "nope" not found. dp= sp=`},
		{"container c2", func() (*Node, error) { var err error; c2, err = b.newInner(nil, a, "c2", false); return c2, err }, "LY_SUCCESS"},
		{"keyless list in parent", func() (*Node, error) { return b.newList(c2, nil, "l3", nil, no, "lyd_new_list") }, "LY_SUCCESS"},
		{"list2 store-only", func() (*Node, error) { return b.newList2(nil, a, "l1", "[a='a'][b='b']", storeOnly) }, "LY_SUCCESS"},
		{"no parent, no module", func() (*Node, error) { return b.newTerm(nil, nil, "foo", "1", no) },
			`LY_EINVAL | LY_EINVAL LYVE_SUCCESS Invalid argument parent || module (_lyd_new_term()). dp= sp=`},
		// test_opaq
		{"opaq", func() (*Node, error) { return b.newOpaq(nil, "node1", "", "", "my-module", false) }, "LY_SUCCESS"},
		{"opaq prefix", func() (*Node, error) { return b.newOpaq(nil, "node1", "v", "pfx", "my-module", false) },
			`LY_EINVAL | LY_EINVAL LYVE_SUCCESS Invalid argument !prefix || !strcmp(prefix, module_name) (lyd_new_opaq()). dp= sp=`},
		{"opaq null", func() (*Node, error) { return b.newOpaq(nil, "node1", "[null]", "my-module", "my-module", false) }, "LY_SUCCESS"},
		{"opaq2", func() (*Node, error) { return b.newOpaq(nil, "node1", "v", "p", "urn:x", true) }, "LY_SUCCESS"},
		// "" is a value, not NULL (native libyang 5.8.6)
		{"term empty name", func() (*Node, error) { return b.newTerm(nil, a, "", "1", no) },
			`LY_ENOTFOUND | LY_EINVAL LYVE_SUCCESS Term node "" not found. dp= sp=`},
		{"opaq empty module", func() (*Node, error) { return b.newOpaq(nil, "node1", "v", "", "", false) }, "LY_SUCCESS"},
		{"opaq2 empty namespace", func() (*Node, error) { return b.newOpaq(nil, "node1", "v", "", "", true) }, "LY_SUCCESS"},
	} {
		if _, got := run(tc.f); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}

	// the created nodes
	rpc, _ = run(func() (*Node, error) { return b.newInner(nil, a, "oper", false) })
	in, _ := run(func() (*Node, error) { return b.newTerm(rpc, a, "param", "22", no) })
	out, _ := run(func() (*Node, error) { return b.newTerm(rpc, a, "param", "22", newValOptions{output: true}) })
	if in.schema.Type.Base != schema.String || out.schema.Type.Base != schema.Int8 || in.parent != rpc || out.parent != rpc {
		t.Errorf("rpc params: %v %v", in.schema.Type.Base, out.schema.Type.Base)
	}
	// lyd_create_inner: a non-presence container is a default node until it gets a child
	nc, _ := run(func() (*Node, error) { return b.newInner(nil, a, "c", false) })
	if nc.flags&(FlagDefault|FlagNew) != FlagDefault|FlagNew {
		t.Errorf("new NP container flags %#x", nc.flags)
	}
	if _, got := run(func() (*Node, error) { return b.newTerm(nc, nil, "x", "v1", no) }); got != "LY_SUCCESS" ||
		nc.flags&FlagDefault != 0 {
		t.Errorf("NP container with a child: %s, flags %#x", got, nc.flags)
	}
	k, _ := run(func() (*Node, error) { return b.newList2(nil, a, "l11", "[a='01']", no) })
	if k.kids.list[0].value.Canonical() != "1" || k.flags&FlagNew == 0 {
		t.Errorf("list2 key: %s", k.kids.list[0].value.Canonical())
	}
	root, _ := run(func() (*Node, error) { return b.newOpaq(nil, "node1", "", "", "my-module", false) })
	n2, _ := run(func() (*Node, error) { return b.newOpaq(root, "node2", "value", "", "my-module2", false) })
	if root.schema != nil || root.opaq.Name != "node1" || root.opaq.ModuleNS != "my-module" || root.opaq.Value != "" ||
		n2.opaq.Name != "node2" || n2.opaq.ModuleNS != "my-module2" || n2.opaq.Value != "value" || n2.parent != root {
		t.Errorf("opaq: %+v %+v", root.opaq, n2.opaq)
	}

	// lyd_change_term
	foo, _ := run(func() (*Node, error) { return b.newTerm(nil, a, "foo", "5", no) })
	for _, tc := range []struct {
		val, want string
		canon     bool
		dflt      bool
	}{
		{"6", "LY_SUCCESS", false, false},
		{"6", "LY_ENOT", false, false},
		{"x", `LY_EVALID | LY_EVALID LYVE_DATA Invalid type uint16 value "x". dp=/a:foo sp=`, false, false},
		{"07", "LY_SUCCESS", true, false},
		{"07", "LY_EEXIST", false, true},
	} {
		if tc.dflt {
			foo.flags |= FlagDefault
		}
		if _, got := run(func() (*Node, error) { return nil, b.changeTerm(foo, tc.val, tc.canon) }); got != tc.want {
			t.Errorf("change %q: got %s, want %s", tc.val, got, tc.want)
		}
	}
	if foo.value.Canonical() != "07" || foo.flags&FlagDefault != 0 {
		t.Errorf("changed term: %q, default %v", foo.value.Canonical(), foo.flags&FlagDefault != 0)
	}
	if _, got := run(func() (*Node, error) { return nil, b.changeTerm(c, "x", false) }); !strings.HasPrefix(got,
		"LY_EINVAL | LY_EINVAL LYVE_SUCCESS Invalid argument term->schema->nodetype & (0x0004|0x0008) (lyd_change_term()).") {
		t.Errorf("change of a container: %s", got)
	}
	if _, got := run(func() (*Node, error) { return nil, b.changeTerm(root, "x", false) }); !strings.HasPrefix(got,
		"LY_EINVAL | LY_EINVAL LYVE_SUCCESS Invalid argument term->schema (lyd_change_term()).") {
		t.Errorf("change of an opaque node: %s", got)
	}
	if _, got := run(func() (*Node, error) { return nil, b.changeTerm(nil, "x", true) }); !strings.HasPrefix(got,
		"LY_EINVAL | LY_EINVAL LYVE_SUCCESS Invalid argument term (lyd_change_term_canon()).") {
		t.Errorf("change of nil: %s", got)
	}
}

// TestNewList2KeyItems: a key value that does not store keeps the type plugin's whole error chain
// (lyd_new_list2 over a node-instance-identifier key: the path's LYVE_XPATH item, then the
// plugin's LYVE_DATA item, both at the key), as native libyang 5.8.6 logs it.
func TestNewList2KeyItems(t *testing.T) {
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS("../conformance/corpus/ietf"),
		fstest.MapFS{"k.yang": {Data: []byte(`module k { yang-version 1.1; namespace urn:k; prefix k;
  import ietf-netconf-acm { prefix nacm; }
  list nl { key p; leaf p { type nacm:node-instance-identifier; } } }`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load("k", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	k := set.Implemented("k")
	for _, tc := range []struct{ keys, want string }{
		{"[p='/zz:x']", `LY_EVALID LYVE_XPATH No module connected with the prefix "zz" found (prefix format JSON module names). sp=/k:nl/p|` +
			`LY_EVALID LYVE_DATA Invalid node-instance-identifier "/zz:x" value - semantic error. sp=/k:nl/p|`},
		{"[p='/k:nl[k:p=']", `LY_EVALID LYVE_XPATH Redundant prefix for "k:p" in path. sp=/k:nl/p|` +
			`LY_EVALID LYVE_DATA Invalid node-instance-identifier "/k:nl[k:p=" value - syntax error. sp=/k:nl/p|`},
		{"[p='/k:nl']", ""},
	} {
		b := newBuilder{set: set, log: &logger{set: set}}
		_, err := b.newList2(nil, k, "nl", tc.keys, newValOptions{})
		err = b.log.done(err)
		got := ""
		for _, d := range b.log.diags {
			got += d.Err + " " + d.Code + " " + d.Msg + " sp=" + d.SchemaPath + d.DataPath + "|"
		}
		var ve *ValidationError
		if got != tc.want || (tc.want == "") != (err == nil) || err != nil && (!errors.As(err, &ve) || ve.RC() != "LY_EVALID") {
			t.Errorf("%s:\n got %s (%v)\nwant %s", tc.keys, got, err, tc.want)
		}
	}
}
