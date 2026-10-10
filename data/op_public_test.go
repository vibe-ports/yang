// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// TestOpArgs: the argument refusals of ParseOp and ValidateOp (design 07 §6.1.1) are LY_EINVAL
// *ValidationErrors with libyang's text, or the port's own for the Request checks.
func TestOpArgs(t *testing.T) {
	s := opSchema()
	ctx := context.Background()
	msg := func(err error) string {
		var ve *data.ValidationError
		if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) == 0 {
			return "not an LY_EINVAL ValidationError: " + errString(err)
		}
		return ve.Diags[len(ve.Diags)-1].Msg
	}
	parse := func(in string, typ data.OpType, o data.ParseOpOptions) error {
		_, _, err := data.ParseOp(ctx, strings.NewReader(in), data.FormatJSON, s, typ, o)
		return err
	}
	req, _, err := data.ParseOp(ctx, strings.NewReader(`{"ops:ping": {}}`), data.FormatJSON, s, data.OpRPC, data.ParseOpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	notif, _, err := data.ParseOp(ctx, strings.NewReader(`{"ops:alarm": {}}`), data.FormatJSON, s, data.OpNotif, data.ParseOpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	other := opSchema() // another snapshot
	otherReq, _, err := data.ParseOp(ctx, strings.NewReader(`{"ops:ping": {}}`), data.FormatJSON, other, data.OpRPC, data.ParseOpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	oper := data.NewTree(other)
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"zero type", parse(`{}`, 0, data.ParseOpOptions{}), "Invalid argument data_type (lyd_parse_op())."},
		{"request with an rpc", parse(`{}`, data.OpRPC, data.ParseOpOptions{Request: req.Op}),
			"Invalid argument request (only with a reply) (lyd_parse_op())."},
		{"request not an operation", parse(`{}`, data.OpReply, data.ParseOpOptions{Request: notif.Op}),
			"Invalid argument request (not an RPC or action) (lyd_parse_op())."},
		{"request of another snapshot", parse(`{}`, data.OpReply, data.ParseOpOptions{Request: otherReq.Op}),
			"Invalid argument request (another schema snapshot) (lyd_parse_op())."},
	} {
		if got := msg(tc.err); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	// argument errors come back as the error and as the logged diagnostic
	if d, err := req.Tree.ValidateOp(ctx, 9, data.ValidateOpOptions{}); !strings.Contains(msg(err), "(lyd_validate_op())") ||
		len(d) != 1 || d[0].Msg != msg(err) {
		t.Errorf("bad type: %s %v", msg(err), d)
	}
	if d, err := req.Tree.ValidateOp(ctx, data.OpRPC, data.ValidateOpOptions{Operational: oper}); msg(err) !=
		"Invalid argument dep_tree (another schema snapshot) (lyd_validate_op())." || len(d) != 1 || d[0].Msg != msg(err) {
		t.Errorf("other snapshot: %s %v", msg(err), d)
	}
	if _, err := req.Tree.ValidateOp(ctx, data.OpNotif, data.ValidateOpOptions{}); msg(err) != "No notification to validate found." {
		t.Errorf("wrong type: %s", msg(err))
	}
	// the tree as its own dependency is ignored (op_tree == dep_tree)
	if _, err := req.Tree.ValidateOp(ctx, data.OpRPC, data.ValidateOpOptions{Operational: req.Tree}); err != nil {
		t.Errorf("own tree as dependency: %v", err)
	}
}

// TestParseOpBudget: ParseOp honours Budget (input size).
func TestParseOpBudget(t *testing.T) {
	s := opSchema()
	_, _, err := data.ParseOp(context.Background(), strings.NewReader(`{"ops:ping": {"count": 1}}`), data.FormatJSON, s,
		data.OpRPC, data.ParseOpOptions{Budget: data.Budget{MaxBytes: 8}})
	if !errors.Is(err, yang.ErrBudget) {
		t.Fatalf("%v", err)
	}
}

// TestParseOpReplyRollback: a failed reply into a request leaves the request unchanged.
func TestParseOpReplyRollback(t *testing.T) {
	s := opSchema()
	ctx := context.Background()
	req, _, err := data.ParseOp(ctx, strings.NewReader(`{"ops:ping": {"count": 1}}`), data.FormatJSON, s, data.OpRPC, data.ParseOpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var before, after strings.Builder
	_ = req.Tree.PrintJSON(&before, data.PrintOptions{Shrink: true})
	if _, _, err := data.ParseOp(ctx, strings.NewReader(`{"ops:rtt": 5, "ops:bad": 1}`), data.FormatJSON, s, data.OpReply,
		data.ParseOpOptions{Request: req.Op}); err == nil {
		t.Fatal("unknown output node accepted")
	}
	_ = req.Tree.PrintJSON(&after, data.PrintOptions{Shrink: true})
	if before.String() != after.String() {
		t.Fatalf("request changed: %s, was %s", after.String(), before.String())
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
