// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_common.c (lyd_parser_notif_eventtime_validate),
// src/parser_json.c and src/parser_xml.c (the eventTime branches of lydjson_subtree_r and
// lydxml_subtree_r) (BSD-3-Clause, © CESNET).

package data

import (
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyjson"
	"github.com/vibe-ports/yang/internal/lyxml"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// nsNotification is the namespace of the NETCONF notification envelope (RFC 5277), which the XML
// eventTime node is created in.
const nsNotification = "urn:ietf:params:xml:ns:netconf:notification:1.0"

// eventTimeValidate is lyd_parser_notif_eventtime_validate: the value of an eventTime node stores
// as yang:date-and-time (the leaf the context's yang module has for this, JSON format, data
// hints); a failure is logged at that schema node, without a data node (ly_err_print).
func (lc *lydCtx) eventTimeValidate(value string) error {
	var sn *schema.Node
	if m := lc.tree.set.Implemented("yang"); m != nil {
		sn = schema.FindChild(nil, m.Top, m, "date-and-time", 0)
	}
	if sn == nil {
		_ = lc.log.logErr("LY_EINT", "Internal error (%s:%d).", "parser_common.c", 80)
		return fatalRC("LY_EINT")
	}
	if _, d := types.Store(sn.Type, value, types.FormatJSON, types.HintData, types.ModuleNames{Set: lc.tree.set}, sn); d != nil {
		return lc.log.storeErr(nil, sn, d)
	}
	return nil
}

// eventTimeJSON is the eventTime branch of lydjson_subtree_r (LYD_INTOPT_EVENTTIME, a top-level
// "eventTime" member): an opaque node of the string value, validated as a date and time.
func (p *jsonParser) eventTimeJSON(status *lyjson.Token) error {
	lc := p.lc
	if err := p.next(status); err != nil {
		return err
	}
	if *status != lyjson.TokenString {
		return lc.log.val(nil, "", ly.SyntaxJSON, "Expecting JSON %s but %s found.", lyjson.TokenString, *status)
	}
	value := p.lx.Value()
	node, err := lc.createOpaq(opaque{Name: "eventTime", Value: value, Format: types.FormatJSON, Hints: types.HintString})
	if err != nil {
		return err
	}
	lc.nodeInsert(nil, nil, node)
	if err := lc.eventTimeValidate(value); err != nil {
		lc.nodeFree(node)
		return err
	}
	return p.next(status) // move after the item
}

// eventTimeXML is the eventTime branch of lydxml_subtree_r (LYD_INTOPT_EVENTTIME, a top-level
// unprefixed eventTime element, the lexer at its content): an opaque node in the NETCONF
// notification namespace, validated as a date and time, without children.
func (p *xmlParser) eventTimeXML() error {
	lc, x := p.lc, p.x
	value := x.Value
	if x.WSOnly {
		value = ""
	}
	node, err := lc.createOpaq(opaque{Name: "eventTime", ModuleNS: nsNotification, Value: value,
		Format: types.FormatXML, Hints: types.HintData})
	if err != nil {
		return err
	}
	lc.nodeInsert(nil, nil, node)
	fail := func(err error) error {
		lc.nodeFree(node)
		return err
	}
	if err := lc.eventTimeValidate(value); err != nil {
		return fail(err)
	}
	if err := p.next(); err != nil {
		return fail(err)
	}
	if x.Status != lyxml.ElemClose {
		return fail(lc.log.val(nil, "", ly.Data, "Unexpected notification \"eventTime\" node children."))
	}
	return p.next() // past the element's close
}
