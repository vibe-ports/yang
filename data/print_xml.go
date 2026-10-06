// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/printer_xml.c and src/xml.c (lyxml_dump_text) (BSD-3-Clause,
// © CESNET).

package data

import (
	"fmt"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// Prefix options of xml_print_ns.
const (
	prefixRequired = 0x01 // LYXML_PREFIX_REQUIRED: the prefix is a requirement, not a suggestion
	prefixDefault  = 0x02 // LYXML_PREFIX_DEFAULT: the namespace must be the default one
)

// xmlNS is one namespace declaration in scope (pctx->ns and pctx->prefix).
type xmlNS struct {
	ns        string
	prefix    string
	hasPrefix bool
}

type xmlPrinter struct {
	*printer
	ns []xmlNS
}

// printXML is xml_print_data over the top-level siblings.
func printXML(p *printer, top []*Node) error {
	x := &xmlPrinter{printer: p}
	for i := range top {
		if err := x.node(top[i]); err != nil {
			return err
		}
	}
	return nil
}

// dump is lyxml_dump_text: the text with & < > (and, in attributes, ") escaped.
func (x *xmlPrinter) dump(s string, attr bool) {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '&':
			x.buf.WriteString("&amp;")
		case c == '<':
			x.buf.WriteString("&lt;")
		case c == '>':
			x.buf.WriteString("&gt;")
		case c == '"' && attr:
			x.buf.WriteString("&quot;")
		default:
			x.buf.WriteByte(c)
		}
	}
}

// printNS is xml_print_ns: the prefix to use for ns (hasNew false: the default namespace),
// declaring it first unless a suitable declaration is in scope.
func (x *xmlPrinter) printNS(ns, newPrefix string, hasNew bool, opts int) (string, bool) {
	for i := len(x.ns) - 1; i >= 0; i-- {
		e := x.ns[i]
		if !hasNew {
			if !e.hasPrefix { // the default namespace in scope
				if e.ns == ns {
					return "", false
				}
				break
			}
			continue
		}
		if e.ns != ns || !e.hasPrefix {
			continue
		}
		if e.prefix == newPrefix || opts&prefixRequired == 0 {
			return e.prefix, true
		}
	}
	if hasNew {
		x.printf(" xmlns:%s=\"%s\"", newPrefix, ns)
	} else {
		x.printf(" xmlns=\"%s\"", ns)
	}
	x.ns = append(x.ns, xmlNS{ns, newPrefix, hasNew})
	return newPrefix, hasNew
}

// meta is xml_print_meta (the with-defaults tag is D7b; the NETCONF filter attributes are not
// part of M1).
func (x *xmlPrinter) meta(n *Node) error {
	for _, m := range n.meta {
		pc := &types.PrintCtx{}
		value, err := types.Print(m.value, types.FormatXML, pc)
		if err != nil {
			return err
		}
		for _, um := range pc.Used {
			x.printNS(um.Namespace, um.Prefix, true, prefixRequired)
		}
		prefix, _ := x.printNS(m.mod.Namespace, m.mod.Prefix, true, prefixRequired)
		x.printf(" %s:%s=\"", prefix, m.name)
		x.dump(value, true)
		x.buf.WriteString("\"")
	}
	return nil
}

// open is xml_print_node_open: the element name, its default namespace and its metadata.
func (x *xmlPrinter) open(n *Node) error {
	x.printf("%s<%s", x.indent(), n.schema.Name)
	x.printNS(n.schema.Module.Namespace, "", false, 0)
	return x.meta(n)
}

func (x *xmlPrinter) term(n *Node) error {
	pc := &types.PrintCtx{Local: n.schema.Module}
	value, err := types.Print(n.value, types.FormatXML, pc)
	if err != nil {
		return err
	}
	if err := x.open(n); err != nil {
		return err
	}
	for _, m := range pc.Used { // not through printNS: the declaration is repeated per element
		x.printf(" xmlns:%s=\"%s\"", m.Prefix, m.Namespace)
	}
	if value == "" {
		x.printf("/>%s", x.nl())
		return nil
	}
	x.buf.WriteString(">")
	x.dump(value, false)
	x.printf("</%s>%s", n.schema.Name, x.nl())
	return nil
}

func (x *xmlPrinter) inner(n *Node) error {
	if err := x.open(n); err != nil {
		return err
	}
	children := kids(n)
	printable := false
	for _, c := range children {
		if x.opts.shouldPrint(c) {
			printable = true
			break
		}
	}
	if !printable {
		x.printf("/>%s", x.nl())
		return nil
	}
	x.printf(">%s", x.nl())
	x.level++
	for _, c := range children {
		if err := x.node(c); err != nil {
			x.level--
			return err
		}
	}
	x.level--
	x.printf("%s</%s>%s", x.indent(), n.schema.Name, x.nl())
	return nil
}

// opaq is xml_print_opaq for an opaque node without attributes or prefix data: the name with its
// namespace as the default one, then the text and the children.
func (x *xmlPrinter) opaq(n *Node) error {
	o := n.opaq
	x.printf("%s<%s", x.indent(), o.Name)
	if o.Prefix != "" || o.ModuleNS != "" {
		if o.Format == types.FormatXML {
			if o.ModuleNS != "" {
				x.printNS(o.ModuleNS, "", false, prefixDefault)
			}
		} else if o.ModuleNS != "" {
			if m := x.set.Module(o.ModuleNS, ""); m != nil {
				x.printNS(m.Namespace, "", false, prefixDefault)
			}
		}
	}
	children := kids(n)
	if o.Value != "" {
		x.buf.WriteString(">")
		x.dump(o.Value, false)
	}
	switch {
	case len(children) > 0:
		if o.Value == "" {
			x.printf(">%s", x.nl())
		}
		x.level++
		for _, c := range children {
			if err := x.node(c); err != nil {
				x.level--
				return err
			}
		}
		x.level--
		x.printf("%s</%s>%s", x.indent(), o.Name, x.nl())
	case o.Value != "":
		x.printf("</%s>%s", o.Name, x.nl())
	default:
		x.printf("/>%s", x.nl())
	}
	return nil
}

// node is xml_print_node: the namespaces it declares go out of scope with it.
func (x *xmlPrinter) node(n *Node) error {
	if !x.opts.shouldPrint(n) {
		return nil
	}
	count := len(x.ns)
	var err error
	switch {
	case n.schema == nil:
		err = x.opaq(n)
	case n.schema.Kind == schema.Container || n.schema.Kind == schema.List || n.schema.Kind == schema.Notification ||
		n.schema.Kind == schema.RPC || n.schema.Kind == schema.Action:
		err = x.inner(n)
	case n.schema.Kind == schema.Leaf || n.schema.Kind == schema.LeafList:
		err = x.term(n)
	default:
		err = fmt.Errorf("%w: anydata or anyxml node %q", ErrUnsupported, n.Name())
	}
	x.ns = x.ns[:count]
	return err
}
