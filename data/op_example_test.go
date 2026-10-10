// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// opSchema is the module of the operation examples: an rpc with an input default, an output
// leaf, and a notification whose must refers to the operational datastore.
func opSchema() *yang.Schema {
	ctx, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, fstest.MapFS{"ops.yang": {Data: []byte(`module ops {
  yang-version 1.1;
  namespace "urn:ops";
  prefix o;
  leaf mode { type string; }
  rpc ping {
    input { leaf count { type uint8; default 3; } }
    output { leaf rtt { type uint32; mandatory true; } }
  }
  notification alarm { must "/o:mode = 'on'"; leaf level { type uint8; } }
}`)}})
	if err != nil {
		panic(err)
	}
	if _, err := ctx.Load("ops", "", nil); err != nil {
		panic(err)
	}
	return ctx.Schema()
}

// An rpc parsed and validated, then its reply parsed into the request and validated.
func ExampleParseOp() {
	s := opSchema()
	ctx := context.Background()
	req, _, err := data.ParseOp(ctx, strings.NewReader(`{"ops:ping": {}}`), data.FormatJSON, s, data.OpRPC, data.ParseOpOptions{})
	if err != nil {
		panic(err)
	}
	if _, err := req.Tree.ValidateOp(ctx, data.OpRPC, data.ValidateOpOptions{}); err != nil {
		panic(err)
	}
	if err := req.Op.PrintJSON(os.Stdout, data.PrintOptions{WithDefaults: data.WDAll, Shrink: true}); err != nil {
		panic(err)
	}
	fmt.Println()

	// the reply: the input parameters removed, the output parsed into the request
	for _, c := range collect(req.Op) {
		if err := c.Remove(); err != nil {
			panic(err)
		}
	}
	reply, _, err := data.ParseOp(ctx, strings.NewReader(`{"ops:rtt": 25}`), data.FormatJSON, s, data.OpReply,
		data.ParseOpOptions{Request: req.Op})
	if err != nil {
		panic(err)
	}
	if _, err := reply.Tree.ValidateOp(ctx, data.OpReply, data.ValidateOpOptions{}); err != nil {
		panic(err)
	}
	if err := reply.Op.PrintJSON(os.Stdout, data.PrintOptions{Shrink: true}); err != nil {
		panic(err)
	}
	fmt.Println()
	// Output:
	// {"ops:ping":{"count":3}}
	// {"ops:ping":{"rtt":25}}
}

func collect(n *data.Node) []*data.Node {
	var out []*data.Node
	for c := range n.Children() {
		out = append(out, c)
	}
	return out
}

// A notification validated against the operational datastore it refers to.
func ExampleTree_ValidateOp() {
	s := opSchema()
	ctx := context.Background()
	oper, _, err := data.Parse(ctx, strings.NewReader(`{"ops:mode": "off"}`), data.FormatJSON, s, data.ParseOptions{ParseOnly: true})
	if err != nil {
		panic(err)
	}
	n, _, err := data.ParseOp(ctx, strings.NewReader(`{"ops:alarm": {"level": 2}}`), data.FormatJSON, s, data.OpNotif, data.ParseOpOptions{})
	if err != nil {
		panic(err)
	}
	diags, err := n.Tree.ValidateOp(ctx, data.OpNotif, data.ValidateOpOptions{Operational: oper})
	fmt.Println(err != nil)
	for _, d := range diags {
		fmt.Println(d.DataPath, d.Msg)
	}
	// Output:
	// true
	// /ops:alarm Must condition "/o:mode = 'on'" not satisfied.
}
