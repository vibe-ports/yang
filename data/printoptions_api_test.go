// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/vibe-ports/yang/data"
)

func distinct[T comparable](xs ...T) int {
	seen := map[T]bool{}
	for _, x := range xs {
		seen[x] = true
	}
	return len(seen)
}

// TestPrintOptionsComparable: PrintOptions is a comparable value (==, map key, the comparable
// constraint) and printing leaves it unchanged; per-print state lives outside it.
func TestPrintOptionsComparable(t *testing.T) {
	a := data.PrintOptions{Shrink: true, WithDefaults: data.WDAllTagged}
	b := a
	if a != b || a == (data.PrintOptions{}) {
		t.Fatal("PrintOptions equality")
	}
	m := map[data.PrintOptions]string{a: "tagged", {}: "plain"}
	if m[b] != "tagged" || len(m) != 2 {
		t.Fatalf("map key: %v", m)
	}
	if n := distinct(a, b, data.PrintOptions{EmptyLeafList: true}); n != 2 {
		t.Fatalf("comparable constraint: %d distinct", n)
	}
	var key any = a // interface comparison must not panic
	if key != any(b) {
		t.Fatal("interface comparison")
	}

	tr, diags, err := parse(fzContext(t).Schema(), `{"fz:c": {"s": "x"}}`, data.FormatJSON, data.ParseOptions{})
	if err != nil {
		t.Fatal(err, diags)
	}
	var out bytes.Buffer
	for _, wd := range []data.WD{data.WDTrim, data.WDAllTagged} {
		a.WithDefaults = wd
		before := a
		if err := tr.PrintJSON(&out, a); err != nil {
			t.Fatal(err)
		}
		if err := tr.PrintXML(&out, a); err != nil {
			t.Fatal(err)
		}
		if a != before || m[data.PrintOptions{Shrink: true, WithDefaults: data.WDAllTagged}] != "tagged" {
			t.Fatalf("printing changed the options: %+v, was %+v", a, before)
		}
	}
}

// reentrantWriter prints the same tree with the same options from inside Write.
type reentrantWriter struct {
	tr   *data.Tree
	o    data.PrintOptions
	buf  bytes.Buffer
	nest string
	err  error
}

func (w *reentrantWriter) Write(p []byte) (int, error) {
	if w.nest == "" {
		var b strings.Builder
		w.err = w.tr.PrintJSON(&b, w.o)
		w.nest = b.String()
	}
	return w.buf.Write(p)
}

// TestPrintOptionsShared: concurrent and reentrant prints of one tree with one options value
// produce the same output as a lone print (run under -race).
func TestPrintOptionsShared(t *testing.T) {
	tr, diags, err := parse(fzContext(t).Schema(), `{"fz:c": {"s": "x", "n": 5}}`, data.FormatJSON, data.ParseOptions{})
	if err != nil {
		t.Fatal(err, diags)
	}
	o := data.PrintOptions{WithDefaults: data.WDTrim}
	var want strings.Builder
	if err := tr.PrintJSON(&want, o); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(want.String(), `"n"`) || !strings.Contains(want.String(), `"s"`) {
		t.Fatalf("explicit default not trimmed: %s", want.String())
	}

	w := &reentrantWriter{tr: tr, o: o}
	if err := tr.PrintJSON(w, o); err != nil || w.err != nil {
		t.Fatal(err, w.err)
	}
	if w.buf.String() != want.String() || w.nest != want.String() {
		t.Fatalf("reentrant print:\n outer %q\n inner %q\n want  %q", w.buf.String(), w.nest, want.String())
	}

	var wg sync.WaitGroup
	got := make([]string, 8)
	errs := make([]error, len(got))
	for i := range got {
		wg.Go(func() {
			var b strings.Builder
			errs[i] = tr.PrintJSON(&b, o)
			got[i] = b.String()
		})
	}
	wg.Wait()
	for i := range got {
		if errs[i] != nil || got[i] != want.String() {
			t.Fatalf("goroutine %d: %v %q", i, errs[i], got[i])
		}
	}
	if o != (data.PrintOptions{WithDefaults: data.WDTrim}) {
		t.Fatalf("options changed: %+v", o)
	}
}
