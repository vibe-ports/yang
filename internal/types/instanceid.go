// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/instanceid.c, src/path.c (ly_path_compile, ly_path_compile_snode, ly_path_compile_predicate)
// and src/plugins_types.c (lyplg_type_lypath_new, lyplg_type_lypath_check_status)
// (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/vibe-ports/yang/internal/lyxp"
	"github.com/vibe-ports/yang/internal/schema"
)

// Path is a compiled instance-identifier (libyang struct ly_path): one segment per node.
type Path []PathSegment

// PathSegment is one node of a Path with its predicates.
type PathSegment struct {
	Node  *schema.Node
	Preds []PathPred
}

// PredKind is the kind of a path predicate.
type PredKind uint8

// Predicate kinds (LY_PATH_PREDTYPE_*).
const (
	PredPosition PredKind = iota + 1 // [n]
	PredKey                          // [key='value']
	PredLeafList                     // [.='value']
)

// PathPred is one predicate; Value is the stored key / leaf-list value.
type PathPred struct {
	Kind     PredKind
	Key      *schema.Node // PredKey
	Value    Value        // PredKey, PredLeafList
	Position uint64       // PredPosition
}

// String prints the path in JSON form (libyang instanceid_path2str with LY_VALUE_JSON), which is
// the canonical form of an instance-identifier.
func (p Path) String() string {
	var b strings.Builder
	var mod *schema.Module
	for _, s := range p {
		if s.Node.Module != mod {
			mod = s.Node.Module
			fmt.Fprintf(&b, "/%s:%s", mod.Name, s.Node.Name)
		} else {
			fmt.Fprintf(&b, "/%s", s.Node.Name)
		}
		for _, pr := range s.Preds {
			switch pr.Kind {
			case PredPosition:
				fmt.Fprintf(&b, "[%d]", pr.Position)
			case PredKey:
				fmt.Fprintf(&b, "[%s=%s]", pr.Key.Name, quoteXP(pr.Value.Canonical()))
			case PredLeafList:
				fmt.Fprintf(&b, "[.=%s]", quoteXP(pr.Value.Canonical()))
			}
		}
	}
	return b.String()
}

// clone copies the segments and predicate lists (the schema nodes are shared).
func (p Path) clone() Path {
	if p == nil {
		return nil
	}
	c := make(Path, len(p))
	for i, s := range p {
		c[i] = PathSegment{Node: s.Node, Preds: slices.Clone(s.Preds)}
	}
	return c
}

func quoteXP(s string) string {
	if strings.IndexByte(s, '\'') >= 0 {
		return `"` + s + `"`
	}
	return "'" + s + "'"
}

// storeInstanceID ports lyplg_type_store_instanceid.
func storeInstanceID(a *storeArgs) (Value, *Diag) {
	if _, d := checkHints(a.h, a.lex, a.t.Base); d != nil {
		return Value{}, d
	}
	p, d := lypathNew(a)
	if d != nil {
		return Value{}, d
	}
	if d := checkPathStatus(a, p); d != nil {
		return Value{}, d
	}
	v := Value{typ: a.t, path: p, canon: p.String(), needsTree: a.t.RequireInstance}
	if a.f == FormatCanon {
		v.canon = a.lex
	}
	return v, nil
}

// lypathNew ports lyplg_type_lypath_new: parse, then resolve on the schema.
func lypathNew(a *storeArgs) (Path, *Diag) {
	prefix := lyxp.PrefixStrictInherit
	if a.f == FormatSchema || a.f == FormatSchemaResolved || a.f == FormatXML {
		prefix = lyxp.PrefixMandatory
	}
	fail := func(kind, detail string) (Path, *Diag) {
		if detail == "" || a.quiet { // inside a union libyang logs nothing, so no detail is attached
			return nil, errf("Invalid instance-identifier \"%s\" value - %s error.", a.lex, kind)
		}
		return nil, errf("Invalid instance-identifier \"%s\" value - %s error: %s", a.lex, kind, detail)
	}
	e, msg := lyxp.ParsePath(a.lex, lyxp.Opts{Begin: lyxp.BeginAbsolute, Prefix: prefix, Pred: lyxp.PredSimple})
	if msg != "" {
		return fail("syntax", msg)
	}
	if a.impl != nil { // implement all prefixes (lys_compile_expr_implement)
		for i, tk := range e.Toks {
			if tk != lyxp.TokNameTest && tk != lyxp.TokLiteral {
				continue
			}
			tok := e.Src[e.Pos[i] : e.Pos[i]+e.Len[i]]
			c := strings.IndexByte(tok, ':')
			if c < 0 {
				continue
			}
			if m := resolveModule(tok[:c], a.f, a.pc, a.ctx); m != nil {
				if err := a.impl(m, true); err != nil {
					return nil, errf("Failed to implement a module referenced by instance-identifier \"%s\".", a.lex)
				}
			}
		}
	}
	p, msg := pathCompile(a, e)
	if msg != "" {
		return fail("semantic", msg)
	}
	return p, nil
}

// leadingInt is the digit prefix C atoi reads.
func leadingInt(s string) string {
	n := 0
	for n < len(s) && isDigit(s[n]) {
		n++
	}
	if n == 0 {
		return "0"
	}
	return s[:n]
}

var kindNames = map[schema.Kind]string{schema.Container: "container", schema.Choice: "choice", schema.Leaf: "leaf",
	schema.LeafList: "leaf-list", schema.List: "list", schema.AnyXML: "anyxml", schema.AnyData: "anydata",
	schema.Case: "case", schema.RPC: "RPC", schema.Action: "action", schema.Notification: "notification"}

func kindName(k schema.Kind) string {
	if n, ok := kindNames[k]; ok {
		return n
	}
	return "unknown"
}

var formatNames = map[Format]string{FormatCanon: "canonical", FormatSchema: "schema imports",
	FormatSchemaResolved: "schema stored mapping", FormatXML: "XML prefixes", FormatJSON: "JSON module names"}

// pathCompile ports _ly_path_compile (not leafref, LY_PATH_TARGET_SINGLE, not XPath) for an
// absolute instance-identifier.
func pathCompile(a *storeArgs, e *lyxp.Expr) (Path, string) {
	return pathCompileAt(a, e, nil, a.ctx != nil && a.ctx.InOutput(), false)
}

// pathCompileAt ports _ly_path_compile (not leafref, not XPath): a relative path starts at
// ctxNode; many is LY_PATH_TARGET_MANY (no list or leaf-list needs a predicate).
func pathCompileAt(a *storeArgs, e *lyxp.Expr, ctxNode *schema.Node, output, many bool) (Path, string) {
	var path Path
	var parent *schema.Node
	i := 1
	if !e.Is(0, lyxp.TokOperPath) { // relative path
		if ctxNode == nil {
			return nil, "No initial schema parent for a relative path."
		}
		i, parent = 0, ctxNode
	}
	for {
		if n := len(path); !many && n > 0 && path[n-1].Node.Kind == schema.List && path[n-1].Preds == nil {
			return nil, fmt.Sprintf("Predicate missing for %s \"%s\" in path.", kindName(schema.List), path[n-1].Node.Name)
		}
		if msg := e.Check(i, lyxp.TokNameTest); msg != "" {
			return nil, msg
		}
		node, msg := compileSNode(a, parent, e.Text(i), output)
		if msg != "" {
			return nil, msg
		}
		i++
		parent = node
		seg := PathSegment{Node: node}
		if seg.Preds, i, msg = compilePredicate(a, node, e, i); msg != "" {
			return nil, msg
		}
		path = append(path, seg)
		if !e.Is(i, lyxp.TokOperPath) {
			break
		}
		i++
	}
	if i < len(e.Toks) {
		return nil, fmt.Sprintf("Unexpected XPath token \"%s\" (\"%s\").", e.Toks[i], lyxp.Trunc15(e.Rest(i)))
	}
	if last := path[len(path)-1]; !many && (last.Node.Kind == schema.List || last.Node.Kind == schema.LeafList) && last.Preds == nil {
		return nil, fmt.Sprintf("Predicate missing for %s \"%s\" in path.", kindName(last.Node.Kind), last.Node.Name)
	}
	return path, ""
}

// resolveModule ports lys_find_module.
func resolveModule(prefix string, f Format, pc PrefixCtx, ctx *schema.Node) *schema.Module {
	if prefix == "" && (f == FormatCanon || f == FormatJSON) {
		if ctx == nil {
			return nil
		}
		return ctx.Module
	}
	if pc == nil {
		return nil
	}
	return pc.Resolve(prefix)
}

// compileSNode ports ly_path_compile_snode: the child of parent (top level when nil) named by
// the QName.
func compileSNode(a *storeArgs, parent *schema.Node, qname string, output bool) (*schema.Node, string) {
	prefix, name := "", qname
	if c := strings.IndexByte(qname, ':'); c >= 0 {
		prefix, name = qname[:c], qname[c+1:]
	}
	mod := resolveModule(prefix, a.f, a.pc, parent)
	switch {
	case mod == nil:
		return nil, fmt.Sprintf("No module connected with the prefix \"%s\" found (prefix format %s).", prefix, formatNames[a.f])
	case !mod.Implemented:
		return nil, fmt.Sprintf("Not implemented module \"%s\" in path.", mod.Name)
	}
	if n := findSNode(parent, mod, name, output); n != nil {
		return n, ""
	}
	return nil, fmt.Sprintf("Not found node \"%s\" in path.", name)
}

// findSNode is the lys_getnext loop of ly_path_compile_snode (path.c:583): an anydata or anyxml
// context node is no schema parent, so the search falls back to the module's top level.
func findSNode(parent *schema.Node, mod *schema.Module, name string, output bool) *schema.Node {
	if parent != nil && (parent.Kind == schema.AnyData || parent.Kind == schema.AnyXML) {
		parent = nil
	}
	return schema.FindChild(parent, mod.Top, mod, name, getNextOpts(output))
}

// getNextOpts are the lys_getnext options of a path step in an operation's input or output.
func getNextOpts(output bool) schema.GetNextOpt {
	if output {
		return schema.GetNextOutput
	}
	return 0
}

func literal(e *lyxp.Expr, i int) string {
	if e.Toks[i] == lyxp.TokLiteral {
		return e.Src[e.Pos[i]+1 : e.Pos[i]+e.Len[i]-1]
	}
	return e.Text(i)
}

// compilePredicate ports ly_path_compile_predicate.
func compilePredicate(a *storeArgs, node *schema.Node, e *lyxp.Expr, i int) ([]PathPred, int, string) {
	if !e.Is(i, lyxp.TokBrack1) {
		return nil, i, ""
	}
	i++
	var preds []PathPred
	switch e.Toks[i] {
	case lyxp.TokNameTest:
		if node.Kind != schema.List {
			return nil, i, fmt.Sprintf("List predicate defined for %s \"%s\" in path.", kindName(node.Kind), node.Name)
		} else if len(node.Keys) == 0 {
			return nil, i, fmt.Sprintf("List predicate defined for keyless %s \"%s\" in path.", kindName(node.Kind), node.Name)
		}
		for {
			key, msg := compileSNode(a, node, e.Text(i), false)
			if msg != "" {
				return nil, i, msg
			}
			if key.Kind != schema.Leaf || !key.IsKey() {
				return nil, i, fmt.Sprintf("Key expected instead of %s \"%s\" in path.", kindName(key.Kind), key.Name)
			}
			i += 2 // key, '='
			if e.Toks[i] == lyxp.TokVarRef {
				return nil, i, "Variable reference not allowed in an instance-identifier."
			}
			v, d := storeKey(a, key, literal(e, i))
			if d != nil {
				return nil, i, d.Msg
			}
			preds = append(preds, PathPred{Kind: PredKey, Key: key, Value: v})
			i += 2 // value, ']'
			if !e.Is(i, lyxp.TokBrack1) {
				break
			}
			i++
		}
		if len(preds) != len(node.Keys) {
			return nil, i, fmt.Sprintf("Predicate missing for a key of %s \"%s\" in path.", kindName(node.Kind), node.Name)
		}
	case lyxp.TokDot:
		if node.Kind != schema.LeafList {
			return nil, i, fmt.Sprintf("Leaf-list predicate defined for %s \"%s\" in path.", kindName(node.Kind), node.Name)
		}
		v, d := storeKey(a, node, literal(e, i+2))
		if d != nil {
			return nil, i, d.Msg
		}
		preds = append(preds, PathPred{Kind: PredLeafList, Value: v})
		i += 4 // '.', '=', value, ']'
	default: // lyxp.TokNumber
		if node.Kind != schema.LeafList && node.Kind != schema.List {
			return nil, i, fmt.Sprintf("Positional predicate defined for %s \"%s\" in path.", kindName(node.Kind), node.Name)
		} else if node.Config {
			return nil, i, fmt.Sprintf("Positional predicate defined for configuration %s \"%s\" in path.", kindName(node.Kind), node.Name)
		}
		pos, _ := strconv.ParseUint(leadingInt(e.Text(i)), 10, 64)
		preds = append(preds, PathPred{Kind: PredPosition, Position: pos})
		i += 2 // number, ']'
	}
	return preds, i, ""
}

// storeKey stores a predicate value of node with the instance-identifier's format and prefixes
// (lyd_value_validate3 with LYD_HINT_DATA).
func storeKey(a *storeArgs, node *schema.Node, lex string) (Value, *Diag) {
	b := *a
	b.t, b.lex, b.h, b.ctx, b.only = node.Type, lex, HintData, node, false
	return storeArgsDispatch(&b)
}

// checkPathStatus ports lyplg_type_lypath_check_status.
func checkPathStatus(a *storeArgs, p Path) *Diag {
	if a.f != FormatSchema || a.ctx == nil || a.pc == nil {
		return nil
	}
	valMod := a.pc.Resolve("")
	for _, s := range p {
		if d := checkStatus(a.ctx, valMod, s.Node.Status, s.Node.Name, valMod == s.Node.Module); d != nil {
			return d
		}
	}
	return nil
}

// checkStatus ports the shared part of lyplg_type_check_status and lyplg_type_lypath_check_status:
// a definition must not reference a less current one (sameMod: the callers' module condition).
func checkStatus(ctx *schema.Node, valMod *schema.Module, refStatus schema.Status, refName string, sameMod bool) *Diag {
	flg1 := schema.Current
	if valMod == ctx.Module {
		flg1 = ctx.Status
	}
	if flg1 < refStatus && sameMod {
		cur, ref := "current", "deprecated"
		if flg1 == schema.Deprecated {
			cur = "deprecated"
		}
		if refStatus == schema.Obsolete {
			ref = "obsolete"
		}
		return &Diag{Code: CodeReference,
			Msg: fmt.Sprintf("A %s definition \"%s\" is not allowed to reference %s value \"%s\".", cur, ctx.Name, ref, refName)}
	}
	return nil
}
