// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_json.c (lydjson_envelope, lyd_parse_json_restconf)
// (BSD-3-Clause, © CESNET).

package data

import (
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyjson"
	"github.com/vibe-ports/yang/internal/types"
)

// envelope is lydjson_envelope: the first member of the object the lexer is at must be name of
// module; it becomes an opaque node and the lexer moves onto its value.
func (p *jsonParser) envelope(name, module string) (*Node, error) {
	lc := p.lc
	var status lyjson.Token
	if err := p.next(&status); err != nil {
		return nil, err
	}
	if status == lyjson.TokenObjectClosed {
		return nil, lc.log.val(nil, "", ly.Syntax, "Empty JSON object.")
	}
	nam, prefix, _, isMeta := parseName(p.lx.Value())
	switch {
	case isMeta:
		return nil, lc.log.val(nil, "", ly.Data, "Unexpected metadata.")
	case prefix != module:
		return nil, lc.log.val(nil, "", ly.Data, "Unexpected module \"%s\" instead of \"%s\".", prefix, module)
	case nam != name:
		return nil, lc.log.val(nil, "", ly.Data, "Unexpected object \"%s\" instead of \"%s\".", nam, name)
	}
	if err := p.next(&status); err != nil {
		return nil, err
	}
	// lyd_create_opaq: the module name is the prefix, no value
	return newOpaque(opaque{Name: name, Prefix: prefix, ModuleNS: prefix, Format: types.FormatJSON,
		Hints: types.HintString, set: lc.tree.set}), nil
}

// parseJSONRestconf is lyd_parse_json_restconf: the envelope object of a RESTCONF message of type
// lc.dataType into lc.env, then its members as lyd_parse_json parses them, into lc.parent for
// input or output, and the envelope's closing. The eventTime of a notification moves into its
// envelope.
func parseJSONRestconf(lc *lydCtx, in []byte) error {
	p, pop, err := newJSONParser(lc, in)
	if err != nil {
		return err
	}
	defer pop()
	var env *Node
	var oo opOpts
	switch lc.dataType {
	case opRPCRestconf:
		env, err = p.envelope("input", lc.parent.schema.Module.Name)
		oo = opOpts{rpc: true, action: true}
	case opNotifRestconf:
		env, err = p.envelope("notification", "ietf-restconf")
		oo = opOpts{notif: true, eventTime: true} // RESTCONF notification and eventTime
	case opReplyRestconf:
		env, err = p.envelope("output", lc.parent.schema.Module.Name)
		oo = opOpts{reply: true}
	default:
		// lyd_parse_op sends only the RESTCONF types here; JSON NETCONF types fail its arguments
		_ = lc.log.logErr("LY_EINT", "Internal error (%s:%d).", "parser_json.c", 2170)
		return fatalRC("LY_EINT")
	}
	if err != nil {
		return err
	}
	lc.op = oo
	if lc.env, err = lc.envTree(env); err != nil {
		return err
	}
	if err := lc.findOperation(); err != nil {
		return errLoggedFatal
	}
	for {
		if err := p.subtree(lc.parent); err != nil {
			return err
		}
		if p.lx.Status() != lyjson.TokenObjectNext {
			break
		}
	}
	// close the envelope's object
	if st := p.lx.Status(); st != lyjson.TokenObjectClosed {
		return lc.log.val(nil, "", ly.SyntaxJSON, "Expecting JSON %s but %s found.", lyjson.TokenObjectClosed, st)
	}
	if err := p.next(nil); err != nil {
		return err
	}
	if lc.op.any() && lc.opNode == nil {
		return lc.log.val(nil, "", ly.Data, "Missing the operation node.")
	}
	var eventTime *Node
	if lc.op.eventTime {
		if eventTime, err = lc.envEventTime(); err != nil {
			return err
		}
	}
	if err := p.metadataFinish(lc.parent); err != nil {
		// lyd_parse_op's cleanup frees the parsed eventTime from the envelope it moved into
		if eventTime != nil {
			if uerr := unlink(eventTime); uerr != nil {
				return uerr
			}
		}
		return err
	}
	return nil
}
