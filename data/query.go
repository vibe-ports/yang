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
// unprefixed names match any module (the evaluator restricts them to the context node's), and an
// unprefixed derived-from() identity without a current node is in cur, lyxp_eval's cur_mod.
type jsonNS struct {
	set *schema.Set
	cur string
}

func (j jsonNS) Resolve(prefix string) (string, bool) {
	if m := j.set.Implemented(prefix); m != nil {
		return m.Name, true
	}
	return "", false
}

func (jsonNS) Prefix(module string) string { return module }
func (jsonNS) Default() string             { return "" }
func (j jsonNS) CurModule() string         { return j.cur }

// xmlPrefixes binds the prefixes of an LY_VALUE_XML expression through its namespace declarations
// (ly_xml_resolve_prefix, lyxml_ns_get: the last declaration of a prefix wins, prefix "" is the
// default namespace) to the implemented module of the namespace. Node names need a prefix; an
// unprefixed value (an identityref) is in the default namespace.
// ponytail: libyang falls back to the latest non-implemented module of a namespace, which matters
// only to values; node tests refuse it either way.
type xmlPrefixes struct {
	set *schema.Set
	nss []XPathNamespace
}

func (x xmlPrefixes) Resolve(prefix string) (string, bool) {
	for i := len(x.nss) - 1; i >= 0; i-- {
		if x.nss[i].Prefix == prefix {
			if m := x.set.ByNamespace(x.nss[i].URI); m != nil {
				return m.Name, true
			}
			return "", false
		}
	}
	return "", false
}

// Prefix is ly_xml_get_prefix: the module's own prefix. libyang reads the namespace set as a set of
// modules there (it is one when printing), so name() in an XML query is not well defined in libyang.
func (x xmlPrefixes) Prefix(module string) string {
	if m := x.set.Module(module, ""); m != nil {
		return m.Prefix
	}
	return module
}
func (xmlPrefixes) Default() string { return "" }
func (xmlPrefixes) PrefixedOnly()   {}

// schemaNS binds the prefixes of an LY_VALUE_SCHEMA expression in the text of module cur (its
// prefix_data, the parsed module, and its cur_mod): cur's own prefix and its import prefixes, to
// implemented modules; unprefixed names are cur's.
type schemaNS struct{ cur *schema.Module }

func (s schemaNS) Resolve(prefix string) (string, bool) {
	if m := s.cur.Import(prefix); m != nil && m.Implemented {
		return m.Name, true
	}
	return "", false
}

// Prefix is ly_schema_get_prefix; a module cur does not import has none, which libyang's
// asprintf("%s:%s") prints as glibc does a NULL string.
func (s schemaNS) Prefix(module string) string {
	if module == s.cur.Name {
		return s.cur.Prefix
	}
	for _, im := range s.cur.Imports {
		if im.Module.Name == module {
			return im.Prefix
		}
	}
	return "(null)"
}
func (s schemaNS) Default() string { return s.cur.Name }

// queryNS is the namespace context of a query's options (lyd_eval_xpath4's format, prefix_data
// and cur_mod), or the argument refusal of a bad one.
func (t *Tree) queryNS(l *logger, o XPathOptions, fn string) (xpath.NamespaceCtx, error) {
	var cur *schema.Module
	if o.Module != "" {
		if cur = t.set.Implemented(o.Module); cur == nil {
			return nil, l.logErr("LY_EINVAL", "Invalid argument %s (%s()).", "cur_mod (not implemented)", fn)
		}
	}
	switch o.Format {
	case XPathJSON:
		return jsonNS{set: t.set, cur: o.Module}, nil
	case XPathXML:
		return xmlPrefixes{t.set, o.Namespaces}, nil
	case XPathSchema:
		if cur == nil {
			return nil, l.logErr("LY_EINVAL", "Current module must be set if schema format is used.")
		}
		return schemaNS{cur}, nil
	}
	return nil, l.logErr("LY_EINVAL", "Invalid argument %s (%s()).", "format (unknown)", fn)
}

// evalXPath4 is lyd_eval_xpath4 for a JSON expression over t, from ctxNode (nil: the document
// root), when conditions ignored (LYXP_IGNORE_WHEN). With single false the result is returned as
// it is (the ret_type form); with single true only one type is asked for: a node set must be
// one, and the other types are cast to.
func (t *Tree) evalXPath4(l *logger, ctxNode *Node, src string, ns xpath.NamespaceCtx, vars []xpath.Var, single bool,
	to xpath.ResultType) (xpath.Result, error) {
	e, err := xpath.Compile(src, ns)
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

// XPathFormat is the format of an XPath expression (lyd_eval_xpath4's LY_VALUE_FORMAT): how its
// prefixes, in node names and in values compared with nodes, name modules.
type XPathFormat uint8

// XPath formats.
const (
	// XPathJSON is LY_VALUE_JSON: a prefix is the name of an implemented module, and an unprefixed
	// name is in the module of its context node or, at the document root, in any module.
	XPathJSON XPathFormat = iota
	// XPathXML is LY_VALUE_XML: a prefix is bound by XPathOptions.Namespaces to the implemented
	// module with that namespace. Every node name needs a prefix; an unprefixed value is in the
	// default namespace (the binding of prefix "").
	XPathXML
	// XPathSchema is LY_VALUE_SCHEMA: the expression is written in the text of the module
	// XPathOptions.Module, so a prefix is that module's own or one of its import prefixes, and an
	// unprefixed name is in that module, as in a must or when statement.
	XPathSchema
)

// XPathNamespace is an XML namespace declaration (xmlns:Prefix="URI"; Prefix "" for xmlns="URI").
type XPathNamespace struct{ Prefix, URI string }

// XPathOptions are the inputs of an XPath query besides the expression.
type XPathOptions struct {
	// Node is the context node, also the node current() returns; nil is the document root.
	// It must be a node of the queried tree.
	Node *Node
	// Vars bind the $name references. As lyxp_vars_find, a reference takes the first variable
	// whose name starts with it, so $ab finds a variable "abc" listed before "ab".
	Vars []XPathVar
	// Format is the expression's format (prefix_data comes from Namespaces or Module).
	Format XPathFormat
	// Namespaces are the declarations in scope of an XPathXML expression, the outermost first:
	// for a prefix declared twice the last one counts.
	Namespaces []XPathNamespace
	// Module is lyxp_eval's cur_mod, an implemented module: the module of an XPathSchema
	// expression (required there), and in XPathJSON the module of an unprefixed derived-from()
	// identity when the query has no context node ("" there is none: the identity is not found).
	Module string
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
	ns, err := t.queryNS(l, o, "lyd_eval_xpath4")
	if err != nil {
		return XPathResult{}, l.diags, l.done(err)
	}
	vars := make([]xpath.Var, len(o.Vars))
	for i, v := range o.Vars {
		vars[i] = xpath.Var(v)
	}
	r, err := t.evalXPath4(l, o.Node, expr, ns, vars, single, to)
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
