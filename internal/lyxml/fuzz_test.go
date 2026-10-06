// SPDX-License-Identifier: BSD-3-Clause

package lyxml

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// FuzzXMLLex: the lexer never panics, stays within its depth and namespace
// budgets, every error is an *Error, Peek agrees with Next and Backup/Restore
// replays identically.
func FuzzXMLLex(f *testing.F) {
	for _, s := range []string{
		"", "<a/>", "<a x='1' xmlns:p='u'>t&amp;&#x41;<![CDATA[z]]><p:b/></a>", "<?xml version='1.0'?><!-- c --><a>\n</a>",
		"<!DOCTYPE a><a/>", "<a b=\"&bad;\"/>", "<a><b></a>", "<a xmlns='u' xmlns='v'/>",
	} {
		f.Add([]byte(s))
	}
	if files, _ := filepath.Glob(filepath.Join("..", "..", ".cache", "libyang", "tests", "fuzz", "corpus", "lyd_parse_mem_xml", "*")); files != nil {
		for _, p := range files {
			if b, err := os.ReadFile(p); err == nil { //nolint:gosec // local seed corpus
				f.Add(b)
			}
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := New(data)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("error type %T", err)
			}
			return
		}
		b := c.Backup()
		var seq []step
		failed := false
		for steps := 0; c.Status != End; steps++ {
			if steps > 2*len(data)+4 {
				t.Fatal("no progress")
			}
			if c.Depth() > MaxDepth+1 || len(c.NS()) > len(data)/8+1 {
				t.Fatalf("budget exceeded: depth %d ns %d", c.Depth(), len(c.NS()))
			}
			seq = append(seq, snap(c))
			want, perr := c.Peek()
			err := c.Next()
			if err != nil {
				var e *Error
				if !errors.As(err, &e) || c.Status != End {
					t.Fatalf("bad error %v status %v", err, c.Status)
				}
				failed = true
				break
			}
			if perr == nil && want != c.Status {
				t.Fatalf("peek %v, next %v", want, c.Status)
			}
		}
		c.Restore(b)
		for i, s := range seq {
			if got := snap(c); got != s {
				t.Fatalf("replay step %d: %+v != %+v", i, got, s)
			}
			if i < len(seq)-1 || !failed {
				if err := c.Next(); err != nil {
					t.Fatalf("replay failed at %d: %v", i, err)
				}
			}
		}
		if !failed {
			roundTrip(t, seq)
		}
	})
}

// roundTrip re-emits a successfully lexed sequence with AppendText and lexes it
// again: the steps (WSOnly aside, an entity can hide whitespace) must be equal.
func roundTrip(t *testing.T, seq []step) {
	var out []byte
	var open []string
	for _, s := range seq {
		q := qn(s.prefix, s.name)
		switch s.st {
		case Element:
			out = append(out, '<')
			out = append(out, q...)
			open = append(open, q)
		case Attribute:
			out = append(out, ' ')
			out = append(out, q...)
			out = append(out, '=')
		case AttrContent, ElemContent:
			// CDATA content is not UTF-8 checked by the lexer, plain text is
			v := &Ctx{in: []byte(s.value)}
			for ; v.pos < len(v.in); v.pos++ {
				_, n := v.getUTF8(v.pos)
				if n == 0 {
					return
				}
				v.pos += n - 1
			}
			if s.st == AttrContent {
				out = append(out, '"')
				out = AppendText(out, s.value, true)
				out = append(out, '"')
			} else {
				out = append(out, '>')
				out = AppendText(out, s.value, false)
			}
		case ElemClose:
			out = append(out, "</"...)
			out = append(out, open[len(open)-1]...)
			out = append(out, '>')
			open = open[:len(open)-1]
		}
	}
	got, _, err := walkBytes(out)
	if err != nil {
		t.Fatalf("re-lex of %q: %v", out, err)
	}
	if len(got) != len(seq) {
		t.Fatalf("round trip %q: %d steps, want %d", out, len(got), len(seq))
	}
	for i := range seq {
		a, b := seq[i], got[i]
		a.ws, b.ws = false, false
		if a != b {
			t.Fatalf("round trip %q step %d: %+v != %+v", out, i, b, a)
		}
	}
}

func walkBytes(b []byte) ([]step, *Ctx, error) {
	c, err := New(b)
	if err != nil {
		return nil, nil, err
	}
	var out []step
	for c.Status != End {
		out = append(out, snap(c))
		if err := c.Next(); err != nil {
			return out, c, err
		}
	}
	return out, c, nil
}
