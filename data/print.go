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

// WD is a with-defaults mode (LYD_PRINT_WD_*, RFC 6243).
type WD uint8

// With-defaults modes.
const (
	WDExplicit       WD = iota // only the data explicitly present, and config false defaults (the default)
	WDTrim                     // no node with its default value
	WDAll                      // report-all
	WDAllTagged                // report-all-tagged: defaults carry the wd:default attribute
	WDImplicitTagged           // implicit nodes carry the wd:default attribute
)

// wdModule is the module that defines the default attribute in the JSON encoding.
const wdModule = "ietf-netconf-with-defaults"

// PrintOptions are the printer flags of the M1 fixtures.
type PrintOptions struct {
	Shrink       bool // LYD_PRINT_SHRINK: no newlines and no indentation
	WithDefaults WD
}

// npCont is lysc_is_np_cont: a non-presence container.
func npCont(s *schema.Node) bool { return s != nil && s.Kind == schema.Container && !s.Presence }

// inOpNotif reports whether s is in an rpc/action input or output or a notification
// (LYS_IS_INPUT, LYS_IS_OUTPUT, LYS_IS_NOTIF).
func inOpNotif(s *schema.Node) bool {
	for p := s; p != nil; p = p.Parent {
		if p.Kind == schema.Input || p.Kind == schema.Output || p.Kind == schema.Notification {
			return true
		}
	}
	return false
}

// isDefault is lyd_is_default: a leaf or leaf-list instance whose value is a default of its
// schema node (the canonical form of the default stored for the node).
func isDefault(n *Node) bool {
	if !n.isTerm() {
		return false
	}
	for _, d := range n.schema.Default {
		if v, diag := types.StoreDefault(n.schema, d); diag == nil && v.Canonical() == n.value.Canonical() {
			return true
		}
	}
	return false
}

// tagged reports whether the with-defaults mode puts the default attribute on n.
func (o PrintOptions) tagged(n *Node) bool {
	return (n.flags&FlagDefault != 0 && (o.WithDefaults == WDAllTagged || o.WithDefaults == WDImplicitTagged)) ||
		(o.WithDefaults == WDAllTagged && isDefault(n))
}

// shouldPrint is lyd_node_should_print.
func (o PrintOptions) shouldPrint(n *Node) bool {
	switch {
	case o.WithDefaults == WDTrim:
		switch {
		case n.flags&FlagDefault != 0:
			return false // an implicit node or an NP container with only default nodes
		case n.isTerm():
			return !isDefault(n)
		case npCont(n.schema):
			for _, c := range kids(n) { // an NP container without printed children
				if o.shouldPrint(c) {
					return true
				}
			}
			return false
		}
	case n.flags&FlagDefault != 0 && n.schema != nil && n.schema.Kind == schema.Container:
		for d := range n.All() { // avoid empty default containers
			if d != n && o.shouldPrint(d) {
				return true
			}
		}
		return false
	case n.flags&FlagDefault != 0 && o.WithDefaults == WDExplicit && n.schema != nil && !configR(n.schema):
		// explicit mode: print only if it contains status data in its subtree
		if !inOpNotif(n.schema) && n.schema.Config {
			for e := range n.All() {
				if e.schema != nil && (e.schema.Kind != schema.Container || e.schema.Presence) && configR(e.schema) {
					return true
				}
			}
		}
		return false
	}
	return true
}

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
