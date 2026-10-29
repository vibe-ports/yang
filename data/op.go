// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_parse_op) and src/validation.c
// (lyd_validate_op) (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"io"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/snap"
)

// OpType is the operation ParseOp and ValidateOp handle (libyang's enum lyd_type); ValidateOp
// takes the YANG types only.
type OpType uint8

// Operation types; zero is invalid.
const (
	OpRPC   OpType = iota + 1 // an rpc or an action (LYD_TYPE_RPC_YANG)
	OpNotif                   // a notification (LYD_TYPE_NOTIF_YANG)
	OpReply                   // the reply of an rpc or action (LYD_TYPE_REPLY_YANG)
	// OpRPCNetconf is a NETCONF <rpc> message with an rpc, or an action in a YANG <action>
	// element (LYD_TYPE_RPC_NETCONF, XML only).
	OpRPCNetconf
	// OpNotifNetconf is a NETCONF <notification> message with its eventTime
	// (LYD_TYPE_NOTIF_NETCONF, XML only; also the XML encoding of a RESTCONF notification).
	OpNotifNetconf
	// OpReplyNetconf is a NETCONF <rpc-reply> message, <ok/>, <rpc-error> elements or the output
	// of Request (LYD_TYPE_REPLY_NETCONF, XML only). Unknown nodes are kept as opaque nodes.
	OpReplyNetconf
	// OpRPCRestconf is the RESTCONF "input" message-body of Request (LYD_TYPE_RPC_RESTCONF).
	OpRPCRestconf
	// OpNotifRestconf is a RESTCONF "ietf-restconf:notification" with its eventTime
	// (LYD_TYPE_NOTIF_RESTCONF, JSON only).
	OpNotifRestconf
	// OpReplyRestconf is the RESTCONF "output" message-body of Request (LYD_TYPE_REPLY_RESTCONF).
	OpReplyRestconf
)

// ParseOpOptions are the options of ParseOp.
type ParseOpOptions struct {
	// Unknown is the unknown-node policy: Reject = LYD_PARSE_STRICT, Opaque = LYD_PARSE_OPAQ,
	// Skip = neither (unknown nodes are dropped).
	Unknown UnknownPolicy
	// Request is lyd_parse_op's parent, the rpc or action node the parsed parameters become
	// children of: optional for OpReply, required for OpReplyNetconf, OpRPCRestconf and
	// OpReplyRestconf (a RESTCONF message-body does not name its operation), refused otherwise.
	// Its existing children are kept; remove them first if the result should hold only the
	// parsed ones.
	Request *Node
	Budget  Budget
}

// OpResult is what ParseOp parsed.
type OpResult struct {
	Tree *Tree // the operation with its parents (a nested action or notification), or Request's tree
	Op   *Node // the rpc, action or notification node, or Request
	// Envelope holds the protocol nodes of a NETCONF or RESTCONF message as opaque nodes with
	// their attributes (the <rpc> with its <action>, <rpc-reply> with <ok/> or <rpc-error>,
	// <notification> or "ietf-restconf:notification" with its eventTime, "input", "output"): a
	// tree apart from the operation, nil for the YANG types.
	Envelope *Tree
}

// ParseOp is lyd_parse_op: one rpc, action, notification or reply, or a NETCONF or RESTCONF
// message holding one, in format f against the schema snapshot s. It only parses
// (LYD_PARSE_ONLY is forced); validate the result with Tree.ValidateOp and the matching YANG
// type. An action or a nested notification comes with its parents. Without o.Request a reply is
// the whole operation node with its output parameters (and parents); with it, the input holds
// only the output parameters, parsed as children of Request.
// The diagnostics are returned in log order, warnings included; err is nil when the parse
// succeeded, else a *ValidationError, or an error wrapping yang.ErrBudget or ctx.Err(). A failed
// parse returns no result but the Envelope, which libyang keeps once the envelope parsed. With
// Request it removes what libyang's cleanup removes, not necessarily all it added: lyd_parse_op
// frees its parsed set, which for JSON holds only the last instance of each member (earlier
// instances of a leaf-list or list stay in Request's tree) and for XML each top-level element,
// so Request's tree may stay modified after a failure.
func ParseOp(ctx context.Context, r io.Reader, f Format, s *yang.Schema, typ OpType, o ParseOpOptions) (_ OpResult, _ []yang.Diagnostic, err error) {
	defer func() { err = restoreUser(err) }() // a Reader's or context's own error, once, after every state ended
	fail := func(set *schema.Set, err error) (OpResult, []yang.Diagnostic, error) {
		lg := &logger{set: set}
		err = lg.done(err)
		return OpResult{}, lg.diags, err
	}
	if s == nil {
		return fail(nil, argErr("ctx || parent", "lyd_parse_op"))
	}
	set := snap.Set(s)
	if typ < OpRPC || typ > OpReplyRestconf {
		return fail(set, argErr("data_type", "lyd_parse_op"))
	}
	if f != FormatJSON && f != FormatXML {
		return fail(set, argErr("format", "lyd_parse_op"))
	}
	req := o.Request
	var argErrOf error
	switch typ {
	case OpRPCNetconf, OpNotifNetconf, OpNotifRestconf: // lyd_parse_op's checks, in its order
		switch {
		case typ != OpNotifRestconf && f != FormatXML:
			argErrOf = argErr("format == LYD_XML", "lyd_parse_op")
		case typ == OpNotifRestconf && f != FormatJSON:
			argErrOf = argErr("format == LYD_JSON", "lyd_parse_op")
		case req != nil:
			argErrOf = argErr("!parent", "lyd_parse_op")
		}
	case OpReplyNetconf, OpRPCRestconf, OpReplyRestconf:
		switch {
		case typ == OpReplyNetconf && f != FormatXML:
			argErrOf = argErr("format == LYD_XML", "lyd_parse_op")
		case req == nil:
			argErrOf = argErr("parent", "lyd_parse_op")
		case req.schema == nil:
			argErrOf = argErr("parent->schema", "lyd_parse_op")
		case req.schema.Kind != schema.RPC && req.schema.Kind != schema.Action:
			argErrOf = argErr("parent->schema->nodetype & (LYS_RPC | LYS_ACTION)", "lyd_parse_op")
		}
	default: // the YANG types: the port's own checks
		switch {
		case req == nil:
		case typ != OpReply:
			argErrOf = argErr("request (only with a reply)", "lyd_parse_op")
		case req.schema == nil || req.schema.Kind != schema.RPC && req.schema.Kind != schema.Action:
			argErrOf = argErr("request (not an RPC or action)", "lyd_parse_op")
		}
	}
	if argErrOf == nil && req != nil && !inContext(req, set) {
		argErrOf = argErr("request (another schema snapshot)", "lyd_parse_op")
	}
	if argErrOf != nil {
		return fail(set, argErrOf)
	}
	tree, op, env, diags, err := parseOpWith(ctx, r, set, f, opType(typ), req, o.Unknown, o.Budget)
	if err != nil {
		return OpResult{Envelope: env}, diags, err
	}
	if req != nil {
		return OpResult{Tree: req.treeOf(), Op: req, Envelope: env}, diags, nil
	}
	return OpResult{Tree: tree, Op: op, Envelope: env}, diags, nil
}

// ValidateOpOptions are the options of ValidateOp.
type ValidateOpOptions struct {
	// Operational is the dependency tree (yanglint's -O operational datastore) for the references
	// of the operation to data outside it; it is used as given, not validated. nil for none.
	Operational *Tree
	Budget      Budget
}

// ValidateOp is lyd_validate_op: the operation of type typ in t (the first rpc or action, or
// notification, of t in depth-first order) is validated, its subtree merged for the time of the
// validation into o.Operational under the parents that tree has: the implicit nodes of its input,
// output (OpReply) or notification are added to t, then the when, value, must and schema checks
// run. libyang has no multi-error mode here: the first error stops it. The diagnostics are
// returned in log order, warnings included; err is nil when only warnings were logged, else a
// *ValidationError, or an error wrapping yang.ErrBudget or ctx.Err().
//
// ValidateOp changes t and, while it runs, o.Operational (the operation is linked into it and
// unlinked again): it needs exclusive access to both.
func (t *Tree) ValidateOp(ctx context.Context, typ OpType, o ValidateOpOptions) (_ []yang.Diagnostic, err error) {
	ctx = suspending(ctx, &t.work) // its polls run the user's context suspended (design 13 §2.4)
	defer func() { err = restoreUser(err) }()
	lg := &logger{set: t.context()}
	fail := func(err error) ([]yang.Diagnostic, error) {
		err = lg.done(err) // logs the argument error first
		return lg.diags, err
	}
	var vt opType
	switch typ {
	case OpRPC:
		vt = opRPC
	case OpNotif:
		vt = opNotif
	case OpReply:
		vt = opReply
	default:
		return fail(argErr("(data_type == LYD_TYPE_RPC_YANG) || (data_type == LYD_TYPE_NOTIF_YANG) || "+
			"(data_type == LYD_TYPE_REPLY_YANG)", "lyd_validate_op"))
	}
	if o.Operational == t {
		o.Operational = nil // op_tree == dep_tree: a redundant dependency
	}
	if o.Operational != nil && o.Operational.context() != t.context() {
		return fail(argErr("dep_tree (another schema snapshot)", "lyd_validate_op"))
	}
	var first *Node
	for n := range t.Top() {
		first = n
		break
	}
	if first == nil {
		return fail(argErr("op_tree", "lyd_validate_op"))
	}
	// one recovery state over both trees (design 13 §3.2): the operation's, the dependency's
	// counter lent to it
	if o.Operational != nil {
		defer o.Operational.work.beginHashWork()()
	}
	journal, err := beginJointTransaction(t, o.Operational)
	// panic-safe finalization, armed at once (design 13 §2.4): a panic in user code (a context's
	// error classification) while the operation subtree sits in the dependency restores both
	// trees, detaches the state and closes its hash-work scope before the panic goes on
	defer journal.endPanicSafe(&err)
	if err != nil {
		return nil, err
	}
	// a failed journal charge fails the operation, and both trees roll back
	return validateOp(ctx, t.context(), first, o.Operational, vt, o.Budget)
}
