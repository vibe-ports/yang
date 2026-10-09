// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// TestLookupDupBudget: N musts `../big[.='5']` over a state leaf-list with N equal values cost
// O(N²) steps, all charged to the XPath step budget: the children index declines a bucket of
// duplicate instances and the evaluator's scan ticks per sibling (review of #173).
func TestLookupDupBudget(t *testing.T) {
	mods := fstest.MapFS{"q.yang": {Data: []byte(`module q { yang-version 1.1; namespace "urn:q"; prefix q;
  container c { config false;
    leaf a { type string; } leaf b { type string; } leaf d { type string; }
    leaf-list chk { type int32; must "../big[.='5']"; }
    leaf-list big { type int8; }
  }
}`)}}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, mods)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := c.Load("q", "", nil); err != nil {
		t.Fatal(d, err)
	}
	const n = 2000
	var big, chk []string
	for i := range n {
		big = append(big, "5")
		chk = append(chk, fmt.Sprint(i))
	}
	in := `{"q:c":{"a":"x","b":"x","d":"x","big":[` + strings.Join(big, ",") + `],"chk":[` + strings.Join(chk, ",") + `]}}`
	_, _, err = data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON, c.Schema(),
		data.ParseOptions{Budget: data.Budget{MaxXPathSteps: 200_000}})
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("want the XPath step budget, got %v", err)
	}
}

// TestHashFirstInstanceBudget: the hash shortcut's instance check (lyd_find_sibling_schema) is a
// binary search, not a scan of the siblings before the list: N musts `../l[k=current()]` after N
// leaf-list instances stay within a 1M step budget (review of #179).
func TestHashFirstInstanceBudget(t *testing.T) {
	mods := fstest.MapFS{"q.yang": {Data: []byte(`module q { yang-version 1.1; namespace "urn:q"; prefix q;
  container c {
    leaf-list big { type int32; }
    list l { key k; leaf k { type int32; } }
    leaf-list chk { type int32; must "../l[k = current()]"; }
  }
}`)}}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, mods)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := c.Load("q", "", nil); err != nil {
		t.Fatal(d, err)
	}
	const n = 1000
	var big, l, chk []string
	for i := range n {
		big = append(big, fmt.Sprint(i))
		l = append(l, fmt.Sprintf(`{"k":%d}`, i))
		chk = append(chk, fmt.Sprint(i))
	}
	in := `{"q:c":{"big":[` + strings.Join(big, ",") + `],"l":[` + strings.Join(l, ",") + `],"chk":[` +
		strings.Join(chk, ",") + `]}}`
	_, diags, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON, c.Schema(),
		data.ParseOptions{Budget: data.Budget{MaxXPathSteps: 1_000_000}})
	if err != nil {
		t.Fatal(err, diags)
	}
}
