// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// TestConcurrentReads: read-only calls on one shared Tree from several goroutines are race-free
// (run under -race, as CI does) and give every goroutine the same answers: Find, FindXPath,
// EvalXPath, EvalXPathAs, the Node accessors and iterators, metadata and printing.
func TestConcurrentReads(t *testing.T) {
	mods := fstest.MapFS{"q.yang": {Data: []byte(`module q { yang-version 1.1; namespace "urn:q"; prefix q;
  import ietf-yang-metadata { prefix md; }
  md:annotation note { type string; }
  container c {
    leaf a { type string; } leaf b { type string; } leaf d { type string; }
    list l { key k; leaf k { type int32; } leaf v { type string; } }
    leaf-list ll { type int32; }
    leaf ref { type leafref { path "../l/k"; } }
  }
}`)}}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, mods)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := c.Load("q", "", nil); err != nil {
		t.Fatal(d, err)
	}
	var l, ll []string
	for i := range 200 {
		l = append(l, fmt.Sprintf(`{"k":%d,"v":"v%d"}`, i, i))
		ll = append(ll, fmt.Sprint(i))
	}
	in := `{"q:c":{"a":"x","@a":{"q:note":"n"},"b":"y","d":"z","ref":7,"l":[` + strings.Join(l, ",") +
		`],"ll":[` + strings.Join(ll, ",") + `]}}`
	tr, diags, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON, c.Schema(), data.ParseOptions{})
	if err != nil {
		t.Fatal(err, diags)
	}
	read := func() string {
		var b strings.Builder
		n, err := tr.Find("/q:c/l[k='42']/v")
		if err != nil || n == nil {
			return fmt.Sprint("find: ", err)
		}
		b.WriteString(n.Path() + "=" + n.Value() + ";")
		nodes, _, err := tr.FindXPath("/q:c/l[k = 100]/v | /q:c/ll[. = 5] | //l[v = 'v7']/k", data.XPathOptions{})
		if err != nil {
			return fmt.Sprint("findxpath: ", err)
		}
		for _, n := range nodes {
			b.WriteString(n.Path() + ",")
		}
		r, _, err := tr.EvalXPath("count(/q:c/l[k > 50]) + sum(/q:c/ll[. < 10])", data.XPathOptions{})
		if err != nil {
			return fmt.Sprint("evalxpath: ", err)
		}
		fmt.Fprintf(&b, "%v;", r.Number)
		s, _, err := tr.EvalXPathAs("/q:c/l[last()]/../a/@q:note", data.XPathString, data.XPathOptions{Node: n})
		if err != nil {
			return fmt.Sprint("evalxpathas: ", err)
		}
		b.WriteString(s.String + ";")
		for top := range tr.Top() {
			for ch := range top.Children() {
				b.WriteString(ch.Name() + ch.Schema().Name() + ch.Path())
				for m := range ch.Meta() {
					b.WriteString("@" + m.Name() + "=" + m.Value())
				}
				for range ch.Children() {
				}
			}
			if m, err := top.FindMeta("q:note"); err != nil || m != nil {
				return fmt.Sprint("findmeta: ", m, err)
			}
		}
		var out bytes.Buffer
		if err := tr.PrintJSON(&out, data.PrintOptions{Shrink: true}); err != nil {
			return fmt.Sprint("print: ", err)
		}
		if err := tr.PrintXML(&out, data.PrintOptions{}); err != nil {
			return fmt.Sprint("print: ", err)
		}
		fmt.Fprintf(&b, ";%d", out.Len())
		return b.String()
	}
	want := read()
	if !strings.HasPrefix(want, "/q:c/l[k='42']/v=v42;") || !strings.Contains(want, ";n;") || !strings.Contains(want, "@note=n") {
		t.Fatalf("single-goroutine read: %s", want)
	}
	var wg sync.WaitGroup
	got := make([]string, 8)
	for g := range got {
		wg.Go(func() {
			for range 5 {
				got[g] = read()
			}
		})
	}
	wg.Wait()
	for g, s := range got {
		if s != want {
			t.Errorf("goroutine %d: %s\nwant %s", g, s, want)
		}
	}
}
