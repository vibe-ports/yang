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

// OpType is the operation ParseOp and ValidateOp handle (libyang's LYD_TYPE_*_YANG).
type OpType uint8

// Operation types; zero is invalid.
const (
	OpRPC   OpType = iota + 1 // an rpc or an action (LYD_TYPE_RPC_YANG)
	OpNotif                   // a notification (LYD_TYPE_NOTIF_YANG)
	OpReply                   // the reply of an rpc or action (LYD_TYPE_REPLY_YANG)
)

// ParseOpOptions are the options of ParseOp.
type ParseOpOptions struct {
	// Unknown is the unknown-node policy: Reject = LYD_PARSE_STRICT, Opaque = LYD_PARSE_OPAQ,
	// Skip = neither (unknown nodes are dropped).
	Unknown UnknownPolicy
	// Request is, for OpReply only, the rpc or action node the reply belongs to (lyd_parse_op's
	// parent): the output parameters become its children. Its input children are kept; remove
	// them first if the reply should hold only the output.
	Request *Node
	Budget  Budget
}

// OpResult is what ParseOp parsed.
type OpResult struct {
	Tree *Tree // the operation with its parents (a nested action or notification), or Request's tree
	Op   *Node // the rpc, action or notification node
}

// ParseOp is lyd_parse_op for the YANG operation types: one rpc, action, notification or reply
// in format f against the schema snapshot s. It only parses (LYD_PARSE_ONLY is forced); validate
// the result with Tree.ValidateOp. An action or a nested notification comes with its parents.
// Without o.Request a reply is the whole operation node with its output parameters (and
// parents); with it, the input holds only the output parameters, parsed as children of Request.
// The diagnostics are returned in log order, warnings included; err is nil when the parse
// succeeded, else a *ValidationError, or an error wrapping yang.ErrBudget or ctx.Err(). A failed
// parse returns no result. With Request it removes what libyang's cleanup removes, not
// necessarily all it added: lyd_parse_op frees its parsed set, which for JSON holds only the last
// instance of each member (earlier instances of a leaf-list or list stay in Request's tree) and
// for XML each top-level element, so Request's tree may stay modified after a failure.
func ParseOp(ctx context.Context, r io.Reader, f Format, s *yang.Schema, typ OpType, o ParseOpOptions) (OpResult, []yang.Diagnostic, error) {
	fail := func(set *schema.Set, err error) (OpResult, []yang.Diagnostic, error) {
		lg := &logger{set: set}
		err = lg.done(err)
		return OpResult{}, lg.diags, err
	}
	if s == nil {
		return fail(nil, argErr("ctx || parent", "lyd_parse_op"))
	}
	set := snap.Set(s)
	var t opType
	switch typ {
	case OpRPC:
		t = opRPC
	case OpNotif:
		t = opNotif
	case OpReply:
		t = opReply
	default:
		return fail(set, argErr("data_type", "lyd_parse_op"))
	}
	if f != FormatJSON && f != FormatXML {
		return fail(set, argErr("format", "lyd_parse_op"))
	}
	if req := o.Request; req != nil {
		switch {
		case typ != OpReply:
			return fail(set, argErr("request (only with a reply)", "lyd_parse_op"))
		case req.schema == nil || req.schema.Kind != schema.RPC && req.schema.Kind != schema.Action:
			return fail(set, argErr("request (not an RPC or action)", "lyd_parse_op"))
		case req.treeOf() == nil || req.treeOf().set != set:
			return fail(set, argErr("request (another schema snapshot)", "lyd_parse_op"))
		}
	}
	tree, op, diags, err := parseOpWith(ctx, r, set, f, t, o.Request, o.Unknown, o.Budget)
	if err != nil {
		return OpResult{}, diags, err
	}
	if o.Request != nil {
		return OpResult{Tree: o.Request.treeOf(), Op: o.Request}, diags, nil
	}
	return OpResult{Tree: tree, Op: op}, diags, nil
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
func (t *Tree) ValidateOp(ctx context.Context, typ OpType, o ValidateOpOptions) ([]yang.Diagnostic, error) {
	lg := &logger{set: t.set}
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
	if o.Operational != nil && o.Operational.set != t.set {
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
	if o.Operational == t {
		o.Operational = nil // op_tree == dep_tree: a redundant dependency
	}
	return validateOp(ctx, t.set, first, o.Operational, vt, o.Budget)
}
