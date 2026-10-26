// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_xml.c (lydxml_envelope, lydxml_env_netconf_rpc,
// lydxml_env_netconf_reply, lyd_parse_xml_netconf) (BSD-3-Clause, © CESNET).

package data

import (
	"errors"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyxml"
	"github.com/vibe-ports/yang/internal/types"
)

// The namespaces of the NETCONF envelopes (RFC 6241, RFC 7950 7.15.2; nsNotification is RFC 5277's).
const (
	nsNetconf = "urn:ietf:params:xml:ns:netconf:base:1.0"
	nsYang    = "urn:ietf:params:xml:ns:yang:1"
)

// errNotEnvelope is lydxml_envelope's LY_ENOT: the current element is not the envelope.
var errNotEnvelope = errors.New("data: not the envelope element")

// envelope is lydxml_envelope (without its value parameter: every caller passes 0): when the
// current element is name in namespace uri, it is opened and becomes an opaque node with its
// attributes, the lexer past its content. errNotEnvelope when it is another element.
func (p *xmlParser) envelope(name, uri string) (*Node, error) {
	x := p.x
	if x.Status != lyxml.Element || x.Name != name {
		return nil, errNotEnvelope
	}
	prefix := x.Prefix
	ns, ok := p.getNS(prefix)
	if !ok {
		return nil, p.namespaceErr(nil, prefix, "")
	} else if ns != uri {
		return nil, errNotEnvelope
	}
	if err := p.next(); err != nil {
		return nil, err
	}
	var attrs []attr
	if x.Status == lyxml.Attribute {
		var err error
		if attrs, err = p.attrs(nil); err != nil {
			return nil, err
		}
	}
	if !x.WSOnly {
		return nil, p.lc.log.val(nil, "", ly.Syntax, "Unexpected value \"%s\" in the \"%s\" element.", x.Value, name)
	}
	// lyd_create_opaq: the value is empty, the element held only whitespace
	env := newOpaque(opaque{Name: name, Prefix: prefix, ModuleNS: uri, Format: types.FormatXML, Attrs: attrs,
		set: p.lc.tree.set})
	if err := p.next(); err != nil {
		return nil, err
	}
	return env, nil
}

// envTree is the tree of the envelope node env (lyd_parse_op's envp): its only top-level node.
func (lc *lydCtx) envTree(env *Node) (*Tree, error) {
	t := newTree(lc.tree.set)
	if err := t.insert(nil, env, insertLast); err != nil {
		return nil, err
	}
	return t, nil
}

// envNetconfRPC is lydxml_env_netconf_rpc: the <rpc> envelope and an optional YANG <action> in
// it, which set the internal options of the rest; closeElem is the number of elements opened.
func (p *xmlParser) envNetconfRPC() (env *Tree, closeElem int, err error) {
	rpc, err := p.envelope("rpc", nsNetconf)
	if err != nil {
		return nil, 0, err
	}
	if env, err = p.lc.envTree(rpc); err != nil {
		return nil, 0, err
	}
	child, err := p.envelope("action", nsYang)
	switch {
	case err == nil:
		if err := env.insert(rpc, child, insertDefault); err != nil { // opaque: appended
			return nil, 0, err
		}
		p.lc.op = opOpts{noSiblings: true, action: true} // NETCONF action
		return env, 2, nil
	case errors.Is(err, errNotEnvelope):
		p.lc.op = opOpts{noSiblings: true, rpc: true} // NETCONF RPC
		return env, 1, nil
	}
	return nil, 0, err
}

// envNetconfReply is lydxml_env_netconf_reply: the <rpc-reply> envelope with an <ok/> or any
// <rpc-error> elements in it, else the output data that follows. Not all of the reply's nodes are
// schema nodes, so the whole parse turns lenient (LYD_PARSE_OPAQ, not LYD_PARSE_STRICT).
func (p *xmlParser) envNetconfReply() (env *Tree, closeElem int, err error) {
	lc, x := p.lc, p.x
	p.strict, p.opaq = false, true
	reply, err := p.envelope("rpc-reply", nsNetconf)
	if err != nil {
		return nil, 0, err
	}
	if env, err = lc.envTree(reply); err != nil {
		return nil, 0, err
	}
	if x.Status == lyxml.ElemClose {
		return nil, 0, lc.log.val(nil, "", ly.Syntax, "Missing child elements of \"rpc-reply\".")
	}
	parsedElem := ""
	ok, err := p.envelope("ok", nsNetconf)
	switch {
	case err == nil:
		if err := env.insert(reply, ok, insertLast); err != nil {
			return nil, 0, err
		}
		if x.Status != lyxml.ElemClose { // finish the child
			return nil, 0, lc.log.val(nil, "", ly.Syntax, "Unexpected child element \"%s\" of \"ok\".", x.Name)
		}
		if err := p.next(); err != nil {
			return nil, 0, err
		}
		parsedElem = "ok"
	case !errors.Is(err, errNotEnvelope):
		return nil, 0, err
	default:
		// every "rpc-error" element, in any namespace, as opaque nodes of the envelope (its tree
		// takes them; libyang passes no parsed set)
		for x.Status == lyxml.Element && x.Name == "rpc-error" {
			data := lc.tree
			lc.tree = env
			err := p.subtree(reply)
			lc.tree = data
			if err != nil {
				return nil, 0, err
			}
			parsedElem = "rpc-error"
		}
	}
	if parsedElem != "" && x.Status != lyxml.ElemClose { // a reply with no data
		return nil, 0, lc.log.val(nil, "", ly.Syntax, "Unexpected sibling element \"%s\" of \"%s\".", x.Name, parsedElem)
	}
	lc.op = opOpts{reply: true} // NETCONF rpc-reply (LYD_INTOPT_WITH_SIBLINGS)
	return env, 1, nil
}

// parseXMLNetconf is lyd_parse_xml_netconf: the envelope of a NETCONF message (or a RESTCONF XML
// message-body) of type lc.dataType into lc.env, then its content as lyd_parse_xml parses it,
// into lc.parent for a reply or RESTCONF input, and the envelope's closing elements. The
// eventTime of a notification moves into its envelope.
func parseXMLNetconf(lc *lydCtx, in []byte) error {
	p, pop, err := newXMLParser(lc, in)
	if err != nil {
		return err
	}
	defer pop()
	x := p.x
	missing := func(err error, msg string) error {
		if errors.Is(err, errNotEnvelope) {
			_ = lc.log.val(nil, "", ly.Data, "%s", msg)
			return fatalRC("LY_ENOT")
		}
		return err
	}
	var env *Tree
	var closeElem int
	switch lc.dataType {
	case opRPCNetconf:
		if env, closeElem, err = p.envNetconfRPC(); err != nil {
			return missing(err, "Missing NETCONF <rpc> envelope or in incorrect namespace.")
		}
	case opNotifNetconf:
		ntf, err := p.envelope("notification", nsNotification)
		if err != nil {
			return missing(err, "Missing NETCONF <notification> envelope or in incorrect namespace.")
		}
		if env, err = lc.envTree(ntf); err != nil {
			return err
		}
		closeElem = 1
		lc.op = opOpts{notif: true, eventTime: true} // NETCONF notification
	case opReplyNetconf:
		if env, closeElem, err = p.envNetconfReply(); err != nil {
			return missing(err, "Missing NETCONF <rpc-reply> envelope or in incorrect namespace.")
		}
	case opRPCRestconf:
		input, err := p.envelope("input", lc.parent.schema.Module.Namespace)
		if err != nil {
			return missing(err, "Missing RESTCONF \"input\" object or in incorrect namespace.")
		}
		if env, err = lc.envTree(input); err != nil {
			return err
		}
		closeElem = 1
		lc.op = opOpts{rpc: true, action: true}
	case opReplyRestconf:
		output, err := p.envelope("output", lc.parent.schema.Module.Namespace)
		if err != nil {
			return missing(err, "Missing RESTCONF \"output\" object or in incorrect namespace.")
		}
		if env, err = lc.envTree(output); err != nil {
			return err
		}
		closeElem = 1
		lc.op = opOpts{reply: true}
	default:
		_ = lc.log.logErr("LY_EINT", "Internal error (%s:%d).", "parser_xml.c", 1531)
		return fatalRC("LY_EINT")
	}
	lc.env = env
	if err := lc.findOperation(); err != nil {
		return errLoggedFatal
	}
	parsedData := false
	for x.Status == lyxml.Element {
		if err := p.subtree(lc.parent); err != nil {
			return err
		}
		parsedData = true
		if lc.op.noSiblings {
			break
		}
	}
	for range closeElem { // close all the opened elements
		if x.Status != lyxml.ElemClose {
			return lc.log.val(nil, "", ly.Syntax, "Unexpected child element \"%s\".", x.Name)
		}
		if err := p.next(); err != nil {
			return err
		}
	}
	if lc.op.noSiblings && x.Status == lyxml.Element {
		return lc.log.val(nil, "", ly.Syntax, "Unexpected sibling node.")
	}
	if lc.op.any() && lc.opNode == nil {
		return lc.log.val(nil, "", ly.Data, "Missing the operation node.")
	}
	if lc.op.eventTime {
		if _, err := lc.envEventTime(); err != nil {
			return err
		}
	}
	if !parsedData {
		lc.opNode = nil // no data nodes were parsed
	}
	return nil
}

// envEventTime is the LYD_INTOPT_EVENTTIME end of lyd_parse_xml_netconf and
// lyd_parse_json_restconf: the last top-level node, which can only be the opaque eventTime when
// it is opaque (opaque nodes come last), moves into the envelope; moved is that node.
func (lc *lydCtx) envEventTime() (moved *Node, err error) {
	top := &lc.tree.top
	var last *Node
	if n := top.OpaqueLen(); n > 0 {
		last = top.opaqueAt(n - 1)
	} else if n := top.SchemaLen(); n > 0 {
		last = top.At(n - 1)
	}
	if last == nil || last.schema != nil {
		return nil, lc.log.val(nil, "", ly.Data, "Missing notification \"eventTime\" node.")
	}
	if err := unlink(last); err != nil {
		return nil, err
	}
	return last, lc.env.insert(lc.env.top.first(), last, insertDefault) // lyd_insert_child
}
