// SPDX-License-Identifier: BSD-3-Clause

package lyxml

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// These tests count work (copied stack entries), never time.

func manyNS(n int) string {
	var b strings.Builder
	b.WriteString("<r")
	for i := range n {
		fmt.Fprintf(&b, ` xmlns:p%d="u%d"`, i, i)
	}
	b.WriteString(">")
	return b.String()
}

func TestBackupRestoreCopiesNothing(t *testing.T) {
	const decls, kids = 20000, 5000
	c, err := New([]byte(manyNS(decls) + strings.Repeat("<x a='1'>t</x>", kids) + "</r>"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.NS()) != decls {
		t.Fatalf("ns = %d", len(c.NS()))
	}
	_ = c.Next() // ElemContent "" (before the first child)
	for i := 0; i < kids; i++ {
		b := c.Backup()
		for j := 0; j < 2; j++ { // lex the child twice from the same backup
			for _, want := range []Status{Element, Attribute, AttrContent, ElemContent, ElemClose} {
				if err := c.Next(); err != nil || c.Status != want {
					t.Fatalf("child %d: %v %v", i, c.Status, err)
				}
			}
			if j == 0 {
				c.Restore(b)
				b = c.Backup()
			} else {
				c.Discard(b)
			}
		}
	}
	if c.copied != 0 {
		t.Fatalf("copied %d entries, want 0", c.copied)
	}
}

func TestBackupCopyOnWriteBelowFloor(t *testing.T) {
	c, err := New([]byte("<r xmlns:a='1'><s xmlns:b='2' xmlns:c='3'>x</s><t xmlns:d='4'/></r>"))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Next() // content ""
	_ = c.Next() // s (ns a, b, c in scope)
	if len(c.NS()) != 3 {
		t.Fatalf("ns = %+v", c.NS())
	}
	b := c.Backup()
	for _, want := range []Status{ElemContent, ElemClose, Element} { // x, </s>, <t> pushes below the floor
		if err := c.Next(); err != nil || c.Status != want {
			t.Fatalf("%v %v", c.Status, err)
		}
	}
	if c.copied == 0 || len(c.NS()) != 2 || c.NS()[1].Prefix != "d" {
		t.Fatalf("copied %d ns %+v", c.copied, c.NS())
	}
	c.Restore(b) // the backup is untouched by the writes above
	if got := c.NS(); len(got) != 3 || got[1].Prefix != "b" || got[2].Prefix != "c" || c.Name != "s" {
		t.Fatalf("restored ns %+v name %q", got, c.Name)
	}
	if _, ok := c.GetNS("d"); ok {
		t.Fatal("d leaked into the backup")
	}
	if err := c.Next(); err != nil || c.Value != "x" {
		t.Fatal(c.Value, err)
	}
}

func TestManyDeclarationsOneElement(t *testing.T) {
	const n = 30000
	c, err := New([]byte(manyNS(n) + "</r>"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.NS()) != n || c.copied != 0 || len(c.nsSeen) != n {
		t.Fatalf("ns %d copied %d seen %d", len(c.NS()), c.copied, len(c.nsSeen))
	}
}

func TestDuplicateDeclarations(t *testing.T) {
	for _, tc := range []struct{ src, msg string }{
		{`<e xmlns:a="1" xmlns:b="2" xmlns:a="3"/>`, `Duplicate XML NS prefix "a" used for namespaces "1" and "3".`},
		{`<e xmlns="1" xmlns:a="x" xmlns="2"/>`, `Duplicate default XML namespaces "1" and "2".`},
		{`<e xmlns:a="1" a='z' xmlns:a="2"/>`, `Duplicate XML NS prefix "a" used for namespaces "1" and "2".`},
	} {
		_, err := New([]byte(tc.src))
		var e *Error
		if !errors.As(err, &e) || e.Msg != tc.msg {
			t.Errorf("%s: %v", tc.src, err)
		}
	}
	// same prefix and URI twice is ignored; a child may redeclare a parent's prefix
	c, err := New([]byte(`<e xmlns:a="1" xmlns:a="1"><f xmlns:a="2"/></e>`))
	if err != nil || len(c.NS()) != 1 {
		t.Fatalf("%v %+v", err, c)
	}
	for c.Status != End {
		if err := c.Next(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFieldsClearedAtEnd(t *testing.T) {
	c, _ := New([]byte(`<a xmlns:q="u"/>`))
	for c.Status != End {
		if err := c.Next(); err != nil {
			t.Fatal(err)
		}
		if c.Status == ElemContent && (c.Name != "" || c.Prefix != "") {
			t.Fatalf("stale name %q %q at ElemContent", c.Prefix, c.Name)
		}
	}
	if c.Prefix != "" || c.Name != "" || c.Value != "" || c.WSOnly {
		t.Fatalf("not cleared: %+v", c)
	}
}

// The D6 pattern (lydxml): at every child Backup, look ahead, Restore, then lex
// the child for real. Every child declares a namespace.
func TestD6BackupPatternCopiesNothing(t *testing.T) {
	const kids = 40000
	src := "<r>" + strings.Repeat("<x xmlns:p='u'/>", kids) + "</r>"
	c, err := New([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Next() // ElemContent of r ("")
	for i := 0; i < kids; i++ {
		if err := c.Next(); err != nil || c.Status != Element {
			t.Fatalf("child %d: %v %v", i, c.Status, err)
		}
		b := c.Backup()
		_ = c.Next() // ElemContent
		_ = c.Next() // ElemClose: the child's declaration is popped
		c.Restore(b)
		if err := c.Next(); err != nil || c.Status != ElemContent {
			t.Fatal(c.Status, err)
		}
		if err := c.Next(); err != nil || c.Status != ElemClose {
			t.Fatal(c.Status, err)
		}
	}
	if c.copied != 0 {
		t.Fatalf("copied %d entries, want 0", c.copied)
	}
}

func TestD6BackupPatternDeepNesting(t *testing.T) {
	const depth, kids = 400, 20000
	src := strings.Repeat("<d xmlns:q='u'>", depth) + strings.Repeat("<x xmlns:p='u'/>", kids) + strings.Repeat("</d>", depth)
	c, err := New([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for c.Depth() < depth { // descend to the innermost <d>
		if err := c.Next(); err != nil {
			t.Fatal(err)
		}
	}
	_ = c.Next() // ElemContent of the innermost d
	for i := 0; i < kids; i++ {
		if err := c.Next(); err != nil || c.Status != Element || c.Name != "x" {
			t.Fatalf("child %d: %v %v", i, c.Status, err)
		}
		b := c.Backup()
		_ = c.Next()
		_ = c.Next()
		c.Restore(b)
		_ = c.Next()
		if err := c.Next(); err != nil || c.Status != ElemClose {
			t.Fatal(c.Status, err)
		}
	}
	if c.copied != 0 || len(c.NS()) != depth {
		t.Fatalf("copied %d, ns %d", c.copied, len(c.NS()))
	}
}

func TestBackupSingleUse(t *testing.T) {
	c, _ := New([]byte("<a/>"))
	b := c.Backup()
	c.Restore(b)
	defer func() {
		if recover() == nil {
			t.Fatal("second Restore must panic")
		}
	}()
	c.Restore(b)
}
