// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_find_xpath, lyd_find_xpath2, lyd_find_xpath3,
// lyd_eval_xpath4) and src/xpath.c (lyxp_eval: the tree check) (BSD-3-Clause, © CESNET).

package data

import (
	"errors"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/xpath"
)

// jsonNS binds the prefixes of an LY_VALUE_JSON expression: implemented module names;
// unprefixed names match any module (the evaluator restricts them to the context node's).
type jsonNS struct{ set *schema.Set }

func (j jsonNS) Resolve(prefix string) (string, bool) {
	if m := j.set.Implemented(prefix); m != nil {
		return m.Name, true
	}
	return "", false
}

func (jsonNS) Prefix(module string) string { return module }
func (jsonNS) Default() string             { return "" }

// evalXPath4 is lyd_eval_xpath4 for a JSON expression over t, from ctxNode (nil: the document
// root), when conditions ignored (LYXP_IGNORE_WHEN). With single false the result is returned as
// it is (the ret_type form); with single true only one type is asked for: a node set must be
// one, and the other types are cast to.
func (t *Tree) evalXPath4(l *logger, ctxNode *Node, src string, vars []xpath.Var, single bool,
	to xpath.ResultType) (xpath.Result, error) {
	e, err := xpath.Compile(src, jsonNS{t.set})
	if err != nil {
		return xpath.Result{}, queryErr(l, ctxNode, err)
	}
	// the first top-level sibling (opaque ones are last and have no schema node)
	if l0 := t.top.list; len(l0) > 0 && l0[0].schema.DataParent() != nil {
		first := l0[0]
		return xpath.Result{}, l.logErr("LY_EINVAL",
			"Data node \"%s\" has no parent but is not instance of a top-level schema node.", first.Name())
	}
	vc := &valCtx{t: t, log: l, vars: vars}
	if single {
		vc.to = to
	}
	r, err := vc.eval(e, ctxNode, xpath.RootAll, true)
	if err != nil {
		return r, queryErr(l, ctxNode, err)
	}
	if single && to == xpath.NodeSet && r.Type != xpath.NodeSet {
		return r, l.logErr("LY_EINVAL", "XPath \"%s\" result is not a node set.", src)
	}
	return r, nil
}

// queryErr logs an error of a query where libyang does: lexer errors at the node the
// expression is parsed for and evaluation errors at the current node, both ctxNode here
// (lyd_eval_xpath4 passes the context node as both); reparse errors without a node; LOGERR
// errors (no validation code) without a location. Budget and cancellation errors pass through.
func queryErr(l *logger, ctxNode *Node, err error) error {
	var xe *xpath.Error
	if !errors.As(err, &xe) {
		return err
	}
	if xe.VECode == "" {
		return l.logErr(xe.Err, "%s", xe.Msg)
	}
	n := ctxNode
	if xe.Origin == xpath.OriginReparse {
		n = nil
	}
	return l.item(n, nil, false, xe.Err, codeOf(xe.VECode), "", xe.Msg)
}

// findXPath is lyd_find_xpath3 (lyd_find_xpath and lyd_find_xpath2 pass the context node as the
// tree): the data nodes the expression selects, in document order.
func (t *Tree) findXPath(l *logger, ctxNode *Node, src string, vars []xpath.Var) ([]*Node, error) {
	r, err := t.evalXPath4(l, ctxNode, src, vars, true, xpath.NodeSet)
	if err != nil {
		return nil, err
	}
	out := make([]*Node, 0, len(r.Nodes))
	for _, x := range r.Nodes {
		if m, ok := x.(xn); ok {
			out = append(out, m.n)
		}
	}
	return out, nil
}
