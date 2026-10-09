// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// qtree parses the interfaces data of the query examples.
func qtree() *data.Tree {
	models := fstest.MapFS{"ex.yang": {Data: []byte(`module ex {
  namespace "urn:ex";
  prefix ex;
  container interfaces {
    list interface {
      key name;
      leaf name { type string; }
      leaf mtu { type uint16; }
      leaf enabled { type boolean; default true; }
    }
  }
}`)}}
	ctx, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, models)
	if err != nil {
		panic(err)
	}
	if _, err := ctx.Load("ex", "", nil); err != nil {
		panic(err)
	}
	in := `{"ex:interfaces": {"interface": [
  {"name": "eth0", "mtu": 1500},
  {"name": "eth1", "mtu": 9000, "enabled": false},
  {"name": "lo", "mtu": 65535}]}}`
	t, _, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON, ctx.Schema(), data.ParseOptions{})
	if err != nil {
		panic(err)
	}
	return t
}

// FindXPath selects nodes: from the document root, or relative to a context node.
func ExampleTree_FindXPath() {
	t := qtree()
	nodes, _, err := t.FindXPath("/ex:interfaces/interface[mtu > 1500]/name", data.XPathOptions{})
	if err != nil {
		panic(err)
	}
	for _, n := range nodes {
		fmt.Println(n.Path(), n.Value())
	}

	eth1, _ := t.Find("/ex:interfaces/interface[name='eth1']")
	nodes, _, _ = t.FindXPath("../interface[enabled = 'true']/name", data.XPathOptions{Node: eth1})
	for _, n := range nodes {
		fmt.Println(n.Value())
	}
	// Output:
	// /ex:interfaces/interface[name='eth1']/name eth1
	// /ex:interfaces/interface[name='lo']/name lo
	// eth0
	// lo
}

// EvalXPath returns the result with the type the expression gives it.
func ExampleTree_EvalXPath() {
	t := qtree()
	for _, expr := range []string{
		"/ex:interfaces/interface[1]",
		"concat(/ex:interfaces/interface[2]/name, '!')",
		"sum(/ex:interfaces/interface/mtu)",
		"/ex:interfaces/interface/name div 2",
		"count(/ex:interfaces/interface) = 3",
	} {
		r, _, err := t.EvalXPath(expr, data.XPathOptions{})
		if err != nil {
			panic(err)
		}
		switch r.Type {
		case data.XPathNodeSet:
			fmt.Println(r.Type, len(r.Nodes), r.Nodes[0].Path())
		case data.XPathString:
			fmt.Println(r.Type, r.String)
		case data.XPathNumber:
			fmt.Println(r.Type, r.Number, math.IsNaN(r.Number))
		case data.XPathBoolean:
			fmt.Println(r.Type, r.Boolean)
		}
	}
	// Output:
	// node-set 1 /ex:interfaces/interface[name='eth0']
	// string eth1!
	// number 76035 false
	// number NaN true
	// boolean true
}

// EvalXPathAs converts the result to the type asked for, as lyd_eval_xpath does for a condition.
// An XPath error is a *data.ValidationError; the diagnostics are returned on success too.
func ExampleTree_EvalXPathAs() {
	t := qtree()
	r, _, _ := t.EvalXPathAs("/ex:interfaces/interface[mtu = 9000]", data.XPathBoolean, data.XPathOptions{})
	fmt.Println(r.Boolean)
	r, _, _ = t.EvalXPathAs("/ex:interfaces/interface/mtu", data.XPathNumber, data.XPathOptions{})
	fmt.Println(r.Number) // the first node's value

	_, diags, err := t.EvalXPathAs("/ex:interfaces/interface[", data.XPathBoolean, data.XPathOptions{})
	var ve *data.ValidationError
	if errors.As(err, &ve) {
		fmt.Println(ve.RC(), diags[0].Code, diags[0].Msg) // diags: also in ve.Diags
	}
	// Output:
	// true
	// 1500
	// LY_EVALID LYVE_XPATH Unexpected XPath expression end.
}

// XPathOptions: a context node and variables, whose values are XPath expressions.
func ExampleXPathOptions() {
	t := qtree()
	c, _ := t.Find("/ex:interfaces")
	nodes, _, _ := t.FindXPath("interface[mtu >= $min and name != $skip]/name", data.XPathOptions{
		Node: c,
		Vars: []data.XPathVar{{Name: "min", Value: "1500"}, {Name: "skip", Value: "'lo'"}},
	})
	for _, n := range nodes {
		fmt.Println(n.Value())
	}
	// Output:
	// eth0
	// eth1
}

// TestXPathAPI: the public wrappers — the not-a-node-set refusal, a foreign context node, an
// unknown result type, and a variable reference resolved by prefix as lyxp_vars_find.
func TestXPathAPI(t *testing.T) {
	tr, other := qtree(), qtree()
	if _, _, err := tr.FindXPath("count(/ex:interfaces/interface)", data.XPathOptions{}); err == nil ||
		err.Error() != `XPath "count(/ex:interfaces/interface)" result is not a node set.` {
		t.Errorf("not a node set: %v", err)
	}
	einval := func(what string, err error) {
		t.Helper()
		var ve *data.ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
			t.Errorf("%s: %v", what, err)
		}
	}
	n, _ := other.Find("/ex:interfaces")
	_, _, err := tr.EvalXPath(".", data.XPathOptions{Node: n})
	einval("foreign context node", err)
	gone, _ := tr.Find("/ex:interfaces/interface[name='lo']")
	if err := gone.Remove(); err != nil {
		t.Fatal(err)
	}
	_, _, err = tr.FindXPath(".", data.XPathOptions{Node: gone})
	einval("removed context node", err)
	_, _, err = tr.EvalXPathAs(".", data.XPathType(9), data.XPathOptions{})
	einval("unknown type", err)
	if data.XPathType(9).String() != "XPathType(9)" {
		t.Error(data.XPathType(9).String())
	}
	r, diags, err := tr.EvalXPath("$m + 1", data.XPathOptions{Vars: []data.XPathVar{{Name: "mx", Value: "2"}, {Name: "m", Value: "5"}}})
	if err != nil || r.Type != data.XPathNumber || r.Number != 3 || len(diags) != 0 {
		t.Errorf("vars: %+v %v %v", r, diags, err)
	}
	r, _, err = tr.EvalXPathAs("/ex:interfaces/interface/name", data.XPathString, data.XPathOptions{})
	if err != nil || r.Type != data.XPathString || r.String != "eth0" {
		t.Errorf("string cast: %+v %v", r, err)
	}
}
