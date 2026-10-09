// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_find_xpath, lyd_find_xpath2, lyd_find_xpath3,
// lyd_eval_xpath4) and src/xpath.c (lyxp_eval: the tree check) (BSD-3-Clause, © CESNET).

package data

import (
	"errors"
	"fmt"

	"github.com/vibe-ports/yang"
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
	// lyxp_eval's tree check of the first top-level sibling (opaque ones are last and have no
	// schema node). The public API cannot build such a tree (insertions check the parent), so this
	// is reached only by trees the package builds by hand: it stays for parity with libyang.
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

// nodesOf is the data nodes of a node-set result, in its order (element nodes only).
func nodesOf(r xpath.Result) []*Node {
	out := make([]*Node, 0, len(r.Nodes))
	for _, x := range r.Nodes {
		if m, ok := x.(xn); ok {
			out = append(out, m.n)
		}
	}
	return out
}

// XPathType is the type of an XPath result (LY_XPATH_TYPE).
type XPathType uint8

// XPath result types.
const (
	XPathNodeSet XPathType = iota // LY_XPATH_NODE_SET
	XPathString                   // LY_XPATH_STRING
	XPathNumber                   // LY_XPATH_NUMBER
	XPathBoolean                  // LY_XPATH_BOOLEAN
)

var xpathTypes = [...]xpath.ResultType{XPathNodeSet: xpath.NodeSet, XPathString: xpath.String,
	XPathNumber: xpath.Number, XPathBoolean: xpath.Boolean}

// String is the XPath 1.0 name of t: "node-set", "string", "number" or "boolean".
func (t XPathType) String() string {
	if int(t) < len(xpathTypes) {
		return [...]string{"node-set", "string", "number", "boolean"}[t]
	}
	return fmt.Sprintf("XPathType(%d)", uint8(t))
}

// XPathVar is a variable binding (struct lyxp_var). Its value is an XPath expression, parsed and
// evaluated at each reference, in the context of that reference: "'text'" binds a string, "42" a
// number, "/m:c/x" a node set. Text from outside is therefore code, not data: quote it as an XPath
// literal ('…' or "…"; XPath 1.0 has no escapes, so text holding both quote kinds must be built
// with concat()), or the caller is open to XPath injection.
type XPathVar struct{ Name, Value string }

// XPathOptions are the inputs of an XPath query besides the expression.
type XPathOptions struct {
	// Node is the context node, also the node current() returns; nil is the document root.
	// It must be a node of the queried tree.
	Node *Node
	// Vars bind the $name references. As lyxp_vars_find, a reference takes the first variable
	// whose name starts with it, so $ab finds a variable "abc" listed before "ab".
	Vars []XPathVar
}

// XPathResult is a typed XPath result: Nodes for a node set (in document order, element nodes
// only), else String, Number or Boolean. Number is libyang's long double narrowed to a float64
// (D-0010): NaN and ±Inf are those of package math.
type XPathResult struct {
	Type    XPathType
	Nodes   []*Node
	String  string
	Number  float64
	Boolean bool
}

// FindXPath is lyd_find_xpath3 (lyd_find_xpath, lyd_find_xpath2): the nodes the expression
// selects, in document order. The expression is in libyang's JSON format: a prefix is a module
// name, an unprefixed name matches the module of its context node and, at the document root,
// any module. When conditions are ignored (LYXP_IGNORE_WHEN). An expression whose result is not
// a node set, and any XPath error, is a *ValidationError with libyang's diagnostics; a context
// node that is not in t is an LY_EINVAL *ValidationError. One query evaluates at most 10 000 000
// steps (then an error wrapping yang.ErrBudget). The diagnostics are libyang's log of the call in
// order, also on success: libyang logs some internal errors (LY_EINT) and goes on, so err is nil
// unless the query failed. Queries may run concurrently with other read-only calls on t, but not
// with a modification (see the package documentation).
func (t *Tree) FindXPath(expr string, o XPathOptions) ([]*Node, []yang.Diagnostic, error) {
	r, d, err := t.EvalXPathAs(expr, XPathNodeSet, o)
	return r.Nodes, d, err
}

// EvalXPath is lyd_eval_xpath4 asked for any result type: the result as the expression gives it,
// never converted. Expression, diagnostics, errors, budget and concurrency as for FindXPath.
func (t *Tree) EvalXPath(expr string, o XPathOptions) (XPathResult, []yang.Diagnostic, error) {
	return t.evalXPath(expr, o, false, xpath.NodeSet)
}

// EvalXPathAs is lyd_eval_xpath4 asked for one result type (lyd_eval_xpath, lyd_eval_xpath2 and
// lyd_eval_xpath3 are typ XPathBoolean): a result of another type is converted to typ with the
// XPath 1.0 string(), number() and boolean() rules, except that XPathNodeSet converts nothing and
// any other result is an error. An unknown typ is an LY_EINVAL *ValidationError. Expression,
// diagnostics, errors, budget and concurrency as for FindXPath.
func (t *Tree) EvalXPathAs(expr string, typ XPathType, o XPathOptions) (XPathResult, []yang.Diagnostic, error) {
	if int(typ) >= len(xpathTypes) {
		l := &logger{set: t.set}
		err := l.done(l.logErr("LY_EINVAL", "Invalid argument %s (%s()).", "ret_type (unknown XPath type)", "lyd_eval_xpath4"))
		return XPathResult{}, l.diags, err
	}
	return t.evalXPath(expr, o, true, xpathTypes[typ])
}

// evalXPath is lyd_eval_xpath4 with its log: the diagnostics of the call, warnings and the
// internal errors libyang logs and goes past included.
func (t *Tree) evalXPath(expr string, o XPathOptions, single bool, to xpath.ResultType) (XPathResult, []yang.Diagnostic, error) {
	l := &logger{set: t.set}
	if o.Node != nil {
		root := o.Node
		for root.parent != nil {
			root = root.parent
		}
		if root.tree != t {
			err := l.done(l.logErr("LY_EINVAL", "Invalid argument %s (%s()).", "ctx_node (not in the tree)", "lyd_eval_xpath4"))
			return XPathResult{}, l.diags, err
		}
	}
	vars := make([]xpath.Var, len(o.Vars))
	for i, v := range o.Vars {
		vars[i] = xpath.Var(v)
	}
	r, err := t.evalXPath4(l, o.Node, expr, vars, single, to)
	if err != nil {
		err = l.done(err)
		return XPathResult{}, l.diags, err
	}
	out := XPathResult{String: r.Str, Number: r.Num, Boolean: r.Bool}
	switch r.Type {
	case xpath.NodeSet:
		out.Type = XPathNodeSet
		out.Nodes = nodesOf(r)
	case xpath.String:
		out.Type = XPathString
	case xpath.Number:
		out.Type = XPathNumber
	case xpath.Boolean:
		out.Type = XPathBoolean
	}
	return out, l.diags, nil
}
