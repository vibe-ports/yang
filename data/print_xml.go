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

// printXML is xml_print_data from sibs[from]: the following siblings too with siblings, else
// only that subtree.
func printXML(p *printer, sibs []*Node, from int) error {
	x := &xmlPrinter{printer: p}
	for i := from; i < len(sibs); i++ {
		if err := x.node(sibs[i]); err != nil {
			return err
		}
		if !p.siblings {
			break
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

// meta is xml_print_meta (the NETCONF filter attributes are not part of M1).
func (x *xmlPrinter) meta(n *Node) error {
	if n.isTerm() && x.opts.tagged(n) {
		// the module "default" is libyang's internal one with the wd:default annotation, used only
		// when the context has ietf-netconf-with-defaults too
		if x.set.Module(wdModule, "") != nil {
			if df := x.set.Module("default", ""); df != nil {
				prefix, _ := x.printNS(df.Namespace, df.Prefix, true, 0)
				x.printf(" %s:default=\"true\"", prefix)
			}
		}
	}
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

// nsOpaq is xml_print_ns_opaq: the prefix of an opaque name's namespace (an opaque node or an
// attribute; moduleNS is the XML namespace or the JSON module name), declared if needed; ok false
// when the name has no namespace the printer can use or it is the default one.
func (x *xmlPrinter) nsOpaq(format types.Format, prefix, moduleNS string, opts int) (string, bool) {
	// the prefix is libyang's new_prefix: NULL (the default namespace) unless given and not DEFAULT
	prefixed := prefix != "" && opts&prefixDefault == 0
	switch format {
	case types.FormatXML:
		if moduleNS != "" {
			return x.printNS(moduleNS, prefix, prefixed, opts)
		}
	case types.FormatJSON:
		if moduleNS != "" {
			if m := x.set.Module(moduleNS, ""); m != nil {
				// the YANG module prefix, not the JSON one (the module name)
				return x.printNS(m.Namespace, m.Prefix, prefixed, opts)
			}
		}
	}
	return "", false
}

// prefixData is xml_print_ns_prefix_data: the declarations of a value's XML prefix data (the
// default namespace is not for the element). Other formats have no prefix data in libyang.
func (x *xmlPrinter) prefixData(pc types.PrefixCtx, opts int) {
	ns, ok := pc.(types.XMLNamespaces)
	if !ok {
		return
	}
	for _, p := range ns.Order {
		if p != "" {
			x.printNS(ns.NS[p], p, opts&prefixDefault == 0, opts)
		}
	}
}

// attrs is xml_print_attr: the attributes of an opaque node with their namespaces and those of
// their values' prefixes.
func (x *xmlPrinter) attrs(as []attr) {
	for _, a := range as {
		pref, ok := "", false
		if a.Prefix != "" {
			pref, ok = x.nsOpaq(a.Format, a.Prefix, a.ModuleNS, 0)
		}
		if a.Prefixes != nil {
			x.prefixData(a.Prefixes, prefixRequired)
		}
		if ok {
			x.printf(" %s:%s=\"", pref, a.Name)
		} else {
			x.printf(" %s=\"", a.Name)
		}
		x.dump(a.Value, true)
		x.buf.WriteString("\"")
	}
}

// opaq is xml_print_opaq (with xml_print_opaq_open): the name with its namespace as the default
// one, the attributes, the namespaces of the value's prefixes, then the text and the children.
func (x *xmlPrinter) opaq(n *Node) error {
	o := n.opaq
	x.printf("%s<%s", x.indent(), o.Name)
	if o.Prefix != "" || o.ModuleNS != "" {
		x.nsOpaq(o.Format, o.Prefix, o.ModuleNS, prefixDefault)
	}
	x.attrs(o.Attrs)
	children := kids(n)
	if o.Value != "" {
		if o.Prefixes != nil {
			x.prefixData(o.Prefixes, prefixRequired)
		}
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
