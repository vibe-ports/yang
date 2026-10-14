// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// opidxR11Read exercises the public reads which either walk an operation's instances or need
// their positions. Its result is also the single-threaded reference for the concurrent reads.
func opidxR11Read(tr *Tree, op *Node, paths []string) string {
	var b strings.Builder
	for _, path := range paths {
		n, err := tr.Find(path)
		switch {
		case err != nil:
			fmt.Fprintf(&b, "%s=error:%v;", path, err)
		case n == nil:
			fmt.Fprintf(&b, "%s=nil;", path)
		default:
			fmt.Fprintf(&b, "%s=%s:%s;", path, n.Name(), n.Value())
		}
	}
	b.WriteString("children=")
	for n := range op.Children() {
		fmt.Fprintf(&b, "%s:%s,", n.Name(), n.Value())
	}
	return b.String()
}

func opidxR11ConcurrentReads(t *testing.T, tr *Tree, op *Node, paths []string) {
	t.Helper()
	// One unrelated insertion and removal moves the sibling generation on, as any edit between
	// two read phases does, so no read-side state from building the tree is current.
	b := newBuilder{set: tr.set, log: &logger{set: tr.set}}
	o, err := b.newTerm(op, op.schema.Module, "o", "x", newValOptions{output: true})
	if err = b.log.done(err); err != nil {
		t.Fatal(err)
	}
	if err := o.Remove(); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	got := make([]string, 8)
	var wg sync.WaitGroup
	for g := range got {
		wg.Go(func() {
			<-start
			for range 40 {
				got[g] = opidxR11Read(tr, op, paths)
			}
		})
	}
	close(start)
	wg.Wait()
	// The reference is computed after the concurrent phase, so the readers start from whatever
	// state the tree had after building: a read path that fills lazy state races under -race.
	want := opidxR11Read(tr, op, paths)
	if strings.Contains(want, "=error:") {
		t.Fatalf("single-threaded reference: %s", want)
	}
	for g, s := range got {
		if s != want {
			t.Errorf("goroutine %d:\n got %s\nwant %s", g, s, want)
		}
	}
}

func TestOpIndexConcurrentPositionalReads(t *testing.T) {
	const n = 384
	set := opIndexSet(t, `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r {
    input {
      leaf-list v { type uint32; ordered-by user; }
      leaf-list s { type uint32; }
      leaf-list u { type uint32; ordered-by user; }
    }
    output { leaf o { type string; } }
  }
}`)
	mod := opIndexModule(t, set)

	t.Run("stale-leader", func(t *testing.T) {
		tr, op := opIndexParseRPC(t, set, `{"m:r":{"v":[`+opIndexValues(n, false)+`]}}`)
		b := newBuilder{set: set, log: &logger{set: set}}
		add := func(name, value string, output bool) *Node {
			t.Helper()
			node, err := b.newTerm(op, mod, name, value, newValOptions{output: output})
			if err = b.log.done(err); err != nil {
				t.Fatal(err)
			}
			b.log = &logger{set: set}
			return node
		}
		separator := add("o", "split", true)
		last := add("v", strconv.Itoa(n), false)
		first := opidxSchemaNodes(op, last.schema)[0]
		if err := separator.Remove(); err != nil {
			t.Fatal(err)
		}
		if err := first.Remove(); err != nil {
			t.Fatal(err)
		}
		if records := op.kids.records(last.schema); len(records) == 0 || records[0] != last {
			t.Fatalf("record leader = %v, want v%d", opidxRecords(op, last.schema), n)
		}

		paths := []string{
			"/m:r/v[1]", "/m:r/v[2]", "/m:r/v[97]",
			"/m:r/v[.='17']", "/m:r/v[.='192']", fmt.Sprintf("/m:r/v[.='%d']", n),
		}
		if got, err := tr.Find("/m:r/v[1]"); err != nil || got != last {
			t.Fatalf("Find(v[1]) = %v, %v; want v%d", got, err, n)
		}
		if got, err := tr.Find("/m:r/v[2]"); err != nil || got != nil {
			t.Fatalf("Find(v[2]) = %v, %v; want nil", got, err)
		}
		opidxR11ConcurrentReads(t, tr, op, paths)
	})

	t.Run("fresh-parse", func(t *testing.T) {
		var system, user []string
		for i := range n {
			system = append(system, strconv.Itoa(n-1-i))
			user = append(user, strconv.Itoa(n-1-i))
		}
		input := `{"m:r":{"s":[` + strings.Join(system, ",") + `],"u":[` + strings.Join(user, ",") + `]}}`
		tr, op := opIndexParseRPC(t, set, input)
		paths := []string{
			"/m:r/s[1]", "/m:r/s[97]", fmt.Sprintf("/m:r/s[%d]", n),
			"/m:r/s[.='123']", "/m:r/u[1]", "/m:r/u[97]", fmt.Sprintf("/m:r/u[%d]", n),
			"/m:r/u[.='123']",
		}
		checks := []struct {
			path, want string
		}{
			{"/m:r/s[1]", "0"}, {"/m:r/s[97]", "96"}, {fmt.Sprintf("/m:r/s[%d]", n), strconv.Itoa(n - 1)},
			{"/m:r/u[1]", strconv.Itoa(n - 1)}, {"/m:r/u[97]", strconv.Itoa(n - 97)}, {fmt.Sprintf("/m:r/u[%d]", n), "0"},
		}
		for _, check := range checks {
			got, err := tr.Find(check.path)
			if err != nil || got == nil || got.Value() != check.want {
				t.Fatalf("Find(%s) = %v, %v; want value %s", check.path, got, err, check.want)
			}
		}
		var names []string
		for child := range op.Children() {
			names = append(names, child.Name())
		}
		if len(names) != 2*n {
			t.Fatalf("operation has %d children, want %d", len(names), 2*n)
		}
		if !slices.Equal(names[:n], slices.Repeat([]string{"s"}, n)) ||
			!slices.Equal(names[n:], slices.Repeat([]string{"u"}, n)) {
			t.Fatalf("operation children are not %d s followed by %d u", n, n)
		}
		opidxR11ConcurrentReads(t, tr, op, paths)
	})
}
