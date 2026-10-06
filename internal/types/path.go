// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/path.c (ly_path_compile_leafref, _ly_path_compile with lref set,
// ly_path_compile_snode, ly_path_compile_predicate_leafref, ly_path_compile_deref,
// ly_path_compile_deref_type, ly_path_append) (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"
	"strings"

	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
)

// PathError is a leafref path compile error: libyang's LYVE_XPATH with the schema node whose
// path the message is logged under (LOGVAL_PATH: the current node), nil when none.
type PathError struct {
	Msg  string
	Node *schema.Node
}

func (e *PathError) Error() string { return e.Msg }

// leafrefCompiler is the fixed arguments of one ly_path_compile_leafref call chain: the prefix
// data (format LY_VALUE_SCHEMA_RESOLVED), the oper option and LY_CTX_LEAFREF_EXTENDED.
type leafrefCompiler struct {
	prefixes schema.NSCtx
	output   bool
	extended bool
	// logged are the errors libyang logs for deref() union members it then skips.
	logged []*PathError
	// active are the (target, leafref type) pairs being expanded, to reject deref cycles.
	active map[derefKey]bool
}

type derefKey struct {
	target *schema.Node
	t      *schema.Type
}

// CompileLeafref ports ly_path_compile_leafref: the path of a parsed leafref path expression e
// (lyxp.ParsePath with the leafref options) for the leaf or leaf-list ctxNode, resolving prefixes
// through prefixes (the leafref's lysc_prefix array). output is LY_PATH_OPER_OUTPUT (the node is
// in an output). Predicates are only checked, so segments carry none. The target is the last
// segment. Nodes provided by extension instances are not found (extensions are unsupported).
//
// Return contract: logged are the errors libyang logs at error level for union members of a
// deref() that it then skips; they are emitted whether or not the compile succeeds, in order, and
// before the returned error (if any). A deref cycle (a dereferencing b dereferencing a) is
// rejected with an error; libyang 5.8.6 crashes on it (D-0042).
func CompileLeafref(ctxNode *schema.Node, e *lyxp.Expr, prefixes schema.NSCtx, output, extended bool) (path Path, logged []*PathError, err *PathError) {
	c := &leafrefCompiler{prefixes: prefixes, output: output, extended: extended, active: map[derefKey]bool{}}
	path, err = c.compile(ctxNode, e)
	return path, c.logged, err
}

func isOp(n *schema.Node) bool {
	return n.Kind == schema.RPC || n.Kind == schema.Action || n.Kind == schema.Notification
}

func sub(e *lyxp.Expr, from, to int) *lyxp.Expr {
	return &lyxp.Expr{Src: e.Src, Toks: e.Toks[from:to], Pos: e.Pos[from:to], Len: e.Len[from:to]}
}

func xpErr(cur *schema.Node, format string, args ...any) *PathError {
	return &PathError{Msg: fmt.Sprintf(format, args...), Node: cur}
}

// compile ports _ly_path_compile for lref with is_xpath set.
func (c *leafrefCompiler) compile(ctxNode *schema.Node, e *lyxp.Expr) (Path, *PathError) {
	op := ctxNode // the operation ctxNode is in, if any
	for op != nil && !isOp(op) {
		op = op.Parent
	}
	cur := ctxNode
	if c.extended && e.Is(0, lyxp.TokFuncName) {
		return c.deref(cur, ctxNode, e)
	}
	i := 0
	if e.Is(i, lyxp.TokOperPath) {
		ctxNode = nil
		i++
	} else {
		for e.Is(i, lyxp.TokDDot) {
			if ctxNode == nil {
				return nil, xpErr(cur, "Too many parent references in path.")
			}
			ctxNode = ctxNode.DataParent()
			i += 2 // '..', '/'
		}
	}
	var path Path
	for {
		if msg := e.Check(i, lyxp.TokNameTest); msg != "" {
			return nil, &PathError{Msg: msg}
		}
		node, msg := c.snode(ctxNode, e.Text(i), c.output)
		if msg != "" {
			return nil, xpErr(cur, "%s", msg)
		}
		i++
		if op != nil && isOp(node) && node != op {
			return nil, xpErr(cur, "Not found node \"%s\" in path.", node.Name)
		}
		ctxNode = node
		path = append(path, PathSegment{Node: node})
		var perr *PathError
		if i, perr = c.predicate(ctxNode, cur, e, i); perr != nil {
			return nil, perr
		}
		if !e.Is(i, lyxp.TokOperPath) {
			break
		}
		i++
	}
	if i < len(e.Toks) {
		return nil, xpErr(cur, "Unexpected XPath token \"%s\" (\"%s\").", e.Toks[i], lyxp.Trunc15(e.Rest(i)))
	}
	return path, nil
}

// snode ports ly_path_compile_snode for the schema-resolved format: the child of ctxNode (top
// level of the module when nil or any-data) named by the QName.
func (c *leafrefCompiler) snode(ctxNode *schema.Node, qname string, output bool) (*schema.Node, string) {
	prefix, name := "", qname
	if i := strings.IndexByte(qname, ':'); i >= 0 {
		prefix, name = qname[:i], qname[i+1:]
	}
	mod := c.prefixes[prefix] // "" is the module the path is written in
	switch {
	case mod == nil:
		return nil, fmt.Sprintf("No module connected with the prefix \"%s\" found (prefix format %s).", prefix, formatNames[FormatSchemaResolved])
	case !mod.Implemented:
		return nil, fmt.Sprintf("Not implemented module \"%s\" in path.", mod.Name)
	}
	if n := findSNode(ctxNode, mod, name, output); n != nil {
		return n, ""
	}
	return nil, fmt.Sprintf("Not found node \"%s\" in path.", name)
}

// predicate ports ly_path_compile_predicate_leafref: the predicates at token i of the segment
// node (ctxNode), only checked; returns the index after them.
func (c *leafrefCompiler) predicate(ctxNode, cur *schema.Node, e *lyxp.Expr, i int) (int, *PathError) {
	if !e.Is(i, lyxp.TokBrack1) {
		return i, nil
	}
	i++
	switch {
	case ctxNode.Kind != schema.List:
		return i, xpErr(cur, "List predicate defined for %s \"%s\" in path.", kindName(ctxNode.Kind), ctxNode.Name)
	case len(ctxNode.Keys) == 0:
		return i, xpErr(cur, "List predicate defined for keyless %s \"%s\" in path.", kindName(ctxNode.Kind), ctxNode.Name)
	}
	for {
		key, msg := c.snode(ctxNode, e.Text(i), false)
		if msg != "" {
			return i, xpErr(cur, "%s", msg)
		}
		if key.Kind != schema.Leaf || !key.IsKey() {
			return i, xpErr(cur, "Key expected instead of %s \"%s\" in path.", kindName(key.Kind), key.Name)
		}
		i += 5 // key, '=', current, '(', ')'
		node := cur
		for { // '/' '..' (the parser guarantees at least one)
			i++
			if node == nil {
				return i, xpErr(cur, "Too many parent references in path.")
			}
			node = node.DataParent()
			i++
			if !e.Is(i+1, lyxp.TokDDot) {
				break
			}
		}
		for { // '/' NameTest
			i++
			n2, msg := c.snode(node, e.Text(i), false)
			if msg != "" {
				return i, xpErr(cur, "%s", msg)
			}
			node = n2
			i++
			if !e.Is(i+1, lyxp.TokNameTest) {
				break
			}
		}
		if node.Kind != schema.Leaf {
			return i, xpErr(cur, "Leaf expected instead of %s \"%s\" in leafref predicate in path.", kindName(node.Kind), node.Name)
		}
		i++ // ']'
		if !e.Is(i, lyxp.TokBrack1) {
			return i, nil
		}
		i++
	}
}

// deref ports ly_path_compile_deref: e starts with `deref(`.
func (c *leafrefCompiler) deref(cur, ctxNode *schema.Node, e *lyxp.Expr) (Path, *PathError) {
	begin := 2 // after the name and '('
	i := begin
	for i < len(e.Toks) && e.Toks[i] != lyxp.TokPar2 {
		i++
	}
	path, perr := c.compile(ctxNode, sub(e, begin, i))
	if perr != nil {
		return nil, perr
	}
	node2 := path[len(path)-1].Node
	if node2 == ctxNode {
		return nil, xpErr(cur, "Deref function target node \"%s\" is node itself.", node2.Name)
	}
	if node2.Kind != schema.Leaf && node2.Kind != schema.LeafList {
		return nil, xpErr(cur, "Deref function target node \"%s\" is not leaf nor leaflist.", node2.Name)
	}
	if perr = c.derefType(cur, node2, node2.Type, e, i, true, &path); perr != nil {
		return nil, perr
	}
	return path, nil
}

// derefType ports ly_path_compile_deref_type: it appends to *path the path of the leafref type t
// of the dereferenced node, then the rest of e after the deref() at token i (its ')'). Union
// members are tried in turn and every success appends (libyang does the same).
func (c *leafrefCompiler) derefType(cur, target *schema.Node, t *schema.Type, e *lyxp.Expr, i int, log bool, path *Path) *PathError {
	switch t.Base {
	case schema.Union:
		ok := false
		for _, m := range t.Union {
			if err := c.derefType(cur, target, m, e, i, false, path); err == nil {
				ok = true
			} else if err.Msg != "" {
				c.logged = append(c.logged, err)
			}
		}
		if ok {
			return nil
		}
		if log {
			return xpErr(cur, "Deref function target node \"%s\" is union type with no leafrefs.", target.Name)
		}
		return &PathError{}
	case schema.Leafref:
	default:
		if log {
			return xpErr(cur, "Deref function target node \"%s\" is not leafref.", target.Name)
		}
		return &PathError{}
	}
	k := derefKey{target, t}
	if c.active[k] {
		return xpErr(cur, "Deref function target node \"%s\" is part of a dereference cycle.", target.Name)
	}
	c.active[k] = true
	defer delete(c.active, k)
	// libyang compiles the nested leafref's own path with the outer prefix data (D-0043).
	le, msg := lyxp.ParsePath(t.Path, lyxp.Opts{Begin: lyxp.BeginEither, Prefix: lyxp.PrefixOptional,
		Pred: lyxp.PredLeafref, Leafref: true, Extended: c.extended})
	if msg != "" {
		return xpErr(cur, "%s", msg)
	}
	p2, perr := c.compile(target, le)
	if perr != nil {
		return perr
	}
	target = p2[len(p2)-1].Node
	*path = append(*path, p2...)
	i += 2 // ')', '/'
	p3, perr := c.compile(target, sub(e, i, len(e.Toks)))
	if perr != nil {
		return perr
	}
	*path = append(*path, p3...)
	return nil
}
