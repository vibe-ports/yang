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
// EvalXPath, EvalXPathAs, the Node accessors and iterators, metadata and printing, Equal, the diff
// functions, and shared trees passed as read-only arguments (a Merge source, the diff given to
// ApplyDiff and MergeDiff) of calls that modify only a tree of their own.
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
	in2 := strings.Replace(strings.Replace(in, `"b":"y"`, `"b":"w"`, 1), `{"k":3,"v":"v3"},`, "", 1)
	other, diags, err := data.Parse(context.Background(), strings.NewReader(in2), data.FormatJSON, c.Schema(), data.ParseOptions{})
	if err != nil {
		t.Fatal(err, diags)
	}
	diff, err := data.Diff(tr, other, data.DiffOptions{}) // shared too, read-only
	if err != nil {
		t.Fatal(err)
	}
	first := func(t *data.Tree) *data.Node { // the container c
		n, _ := t.Find("/q:c")
		return n
	}
	dump := func(t *data.Tree) string {
		var out bytes.Buffer
		if err := t.PrintJSON(&out, data.PrintOptions{Shrink: true}); err != nil {
			return fmt.Sprint("print: ", err)
		}
		return out.String()
	}
	read := func() string {
		var b strings.Builder
		// the diff functions and Equal over the shared trees
		d, err := data.Diff(tr, other, data.DiffOptions{})
		if err != nil {
			return fmt.Sprint("diff: ", err)
		}
		ds, err := data.DiffSiblings(first(tr), first(other), data.DiffOptions{})
		if err != nil {
			return fmt.Sprint("diffsiblings: ", err)
		}
		dt, err := data.DiffTree(first(tr), first(other), data.DiffOptions{})
		if err != nil {
			return fmt.Sprint("difftree: ", err)
		}
		rev, err := diff.ReverseDiff()
		if err != nil {
			return fmt.Sprint("reversediff: ", err)
		}
		fmt.Fprintf(&b, "%s|%s|%s|%s|%v;", dump(d), dump(ds), dump(dt), dump(rev),
			first(tr).Equal(first(other), data.CompareOptions{FullRecursion: true}))
		// shared trees as read-only arguments of calls that modify only a tree of their own
		mine := data.NewTree(c.Schema())
		if err := mine.Merge(tr); err != nil {
			return fmt.Sprint("merge: ", err)
		}
		if err := mine.ApplyDiff(diff, data.ApplyDiffOptions{}); err != nil {
			return fmt.Sprint("applydiff: ", err)
		}
		md := data.NewTree(c.Schema())
		if err := md.MergeDiff(diff, data.MergeDiffOptions{}); err != nil {
			return fmt.Sprint("mergediff: ", err)
		}
		b.WriteString(fmt.Sprint(dump(mine) == dump(other), dump(md) == dump(diff)) + ";")
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
				for k := range ch.ChildrenNoKeys() {
					b.WriteString(k.Name())
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
	if !strings.Contains(want, "|false;true true;") || !strings.Contains(want, "/q:c/l[k='42']/v=v42;") || !strings.Contains(want, ";n;") || !strings.Contains(want, "@note=n") {
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

// BenchmarkConcurrentEvalXPath: EvalXPath from all Ps on one shared tree against one tree per
// goroutine. With nothing written on the read path (the work counter counts sibling visits only
// for tests, #186) the shared tree costs about what private trees do.
func BenchmarkConcurrentEvalXPath(b *testing.B) {
	mods := fstest.MapFS{"q.yang": {Data: []byte(`module q { yang-version 1.1; namespace "urn:q"; prefix q;
  container c { list l { key k; leaf k { type int32; } leaf v { type string; } } leaf-list ll { type int32; } }
}`)}}
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, mods)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := c.Load("q", "", nil); err != nil {
		b.Fatal(err)
	}
	var l, ll []string
	for i := range 500 {
		l = append(l, fmt.Sprintf(`{"k":%d,"v":"v%d"}`, i, i))
		ll = append(ll, fmt.Sprint(i))
	}
	in := `{"q:c":{"l":[` + strings.Join(l, ",") + `],"ll":[` + strings.Join(ll, ",") + `]}}`
	parse := func() *data.Tree {
		tr, d, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON, c.Schema(), data.ParseOptions{})
		if err != nil {
			b.Fatal(err, d)
		}
		return tr
	}
	const expr = "count(/q:c/l[v = 'v250']) + count(/q:c/ll[. > 400]) + count(//k)"
	query := func(b *testing.B, tr *data.Tree) {
		if r, _, err := tr.EvalXPath(expr, data.XPathOptions{}); err != nil || r.Number != 600 {
			b.Fatal(r.Number, err)
		}
	}
	b.Run("shared", func(b *testing.B) {
		tr := parse()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				query(b, tr)
			}
		})
	})
	b.Run("per-goroutine", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			tr := parse()
			for pb.Next() {
				query(b, tr)
			}
		})
	})
}
