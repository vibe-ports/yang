// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/printer_data.c (lyd_print_all, lyd_print_tree), src/out.c
// (lyd_metadata_should_print) and src/printer_internal.h (BSD-3-Clause, © CESNET).

package data

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// ErrUnsupported is returned for a node the printers do not handle (anydata and anyxml are not
// part of M1).
var ErrUnsupported = errors.New("data: not supported")

// PrintOptions are the printer flags of the M1 fixtures. The with-defaults modes are design 07 D7b.
type PrintOptions struct {
	Shrink bool // LYD_PRINT_SHRINK: no newlines and no indentation
}

// meta is one metadata instance of a node (lyd_meta): the annotation's module, its name and the
// stored value. The parsers fill it (design 07 D4).
type meta struct {
	mod   *schema.Module
	name  string
	value types.Value
}

// shouldPrint is lyd_node_should_print for the printers without a with-defaults mode: every node
// prints. D7b adds the with-defaults filter here.
func (o PrintOptions) shouldPrint(*Node) bool { return true }

// PrintJSON writes the top-level siblings of the tree as RFC 7951 JSON, as lyd_print_all with
// LYD_JSON and LYD_PRINT_WITHSIBLINGS: 2-space indentation unless o.Shrink.
func (t *Tree) PrintJSON(w io.Writer, o PrintOptions) error {
	return t.print(w, o, printJSON)
}

// PrintXML writes the top-level siblings of the tree as XML, as lyd_print_all with LYD_XML and
// LYD_PRINT_WITHSIBLINGS.
func (t *Tree) PrintXML(w io.Writer, o PrintOptions) error {
	return t.print(w, o, printXML)
}

func (t *Tree) print(w io.Writer, o PrintOptions, f func(*printer, []*Node) error) error {
	p := &printer{set: t.set, opts: o}
	if err := f(p, slices.Collect(t.top.all())); err != nil {
		return err
	}
	_, err := w.Write(p.buf.Bytes())
	return err
}

// printer is the state the JSON and XML printers share (jsonpr_ctx, xmlpr_ctx).
type printer struct {
	set   *schema.Set
	opts  PrintOptions
	buf   bytes.Buffer
	level int // indentation level
}

func (p *printer) format() bool { return !p.opts.Shrink }

func (p *printer) printf(format string, a ...any) { fmt.Fprintf(&p.buf, format, a...) }

// nl is the newline of a formatted print.
func (p *printer) nl() string {
	if p.format() {
		return "\n"
	}
	return ""
}

// indent is the INDENT of the current level.
func (p *printer) indent() string {
	if p.format() {
		return spaces(p.level * 2)
	}
	return ""
}

func spaces(n int) string { return string(bytes.Repeat([]byte{' '}, n)) }

// kids returns the children of n in order.
func kids(n *Node) []*Node { return slices.Collect(n.kids.all()) }

// hasPrintableMeta is node_has_printable_meta.
func hasPrintableMeta(n *Node) bool { return len(n.meta) > 0 }

// leafBase is the base type of the stored value: the selected member for a union.
func leafBase(v types.Value) schema.BaseType {
	for v.Union() != nil {
		v, _ = v.Union().Member()
	}
	return v.Type().Base
}
