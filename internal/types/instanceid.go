// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_types/instanceid.c, src/path.c (ly_path_parse,
// ly_path_check_predicate, ly_path_compile, ly_path_compile_snode, ly_path_compile_predicate)
// and src/plugins_types.c (lyplg_type_lypath_new, lyplg_type_lypath_check_status)
// (BSD-3-Clause, © CESNET).

package types

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

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
	mandatory := a.f == FormatSchema || a.f == FormatSchemaResolved || a.f == FormatXML
	fail := func(kind, detail string) (Path, *Diag) {
		if detail == "" || a.quiet { // inside a union libyang logs nothing, so no detail is attached
			return nil, errf("Invalid instance-identifier \"%s\" value - %s error.", a.lex, kind)
		}
		return nil, errf("Invalid instance-identifier \"%s\" value - %s error: %s", a.lex, kind, detail)
	}
	e, msg := pathParse(a.lex, mandatory)
	if msg != "" {
		return fail("syntax", msg)
	}
	p, msg := pathCompile(a, e)
	if msg != "" {
		return fail("semantic", msg)
	}
	return p, nil
}

// pathParse ports ly_path_parse for LY_PATH_BEGIN_ABSOLUTE and LY_PATH_PRED_SIMPLE, with prefixes
// LY_PATH_PREFIX_MANDATORY (mandatory) or LY_PATH_PREFIX_STRICT_INHERIT.
func pathParse(src string, mandatory bool) (*xpExpr, string) {
	if src == "" || src[0] != '/' {
		return nil, fmt.Sprintf("XPath \"%s\" was expected to be absolute.", src)
	}
	e, msg := xpLex(src)
	if msg != "" {
		return nil, msg
	}
	i := 1 // the leading '/'
	prevPrefix := ""
	for {
		if msg := e.check(i, tokNameTest); msg != "" {
			return nil, msg
		}
		name := e.text(i)
		colon := strings.IndexByte(name, ':')
		switch {
		case mandatory && colon < 0:
			return nil, fmt.Sprintf("Prefix missing for \"%s\" in path.", name)
		case !mandatory && prevPrefix == "":
			if colon < 0 {
				return nil, fmt.Sprintf("Prefix missing for \"%s\" in path.", name)
			}
			prevPrefix = name[:colon]
		case !mandatory && colon >= 0:
			if name[:colon] == prevPrefix {
				return nil, fmt.Sprintf("Duplicate prefix for \"%s\" in path.", name)
			}
			prevPrefix = name[:colon]
		}
		i++
		var msg string
		if i, msg = checkPredicate(e, i, mandatory); msg != "" {
			return nil, msg
		}
		if !e.is(i, tokOperPath) {
			break
		}
		i++
	}
	if i < len(e.toks) {
		return nil, fmt.Sprintf("Unparsed characters \"%s\" left at the end of path.", e.rest(i))
	}
	return e, ""
}

// checkPredicate ports ly_path_check_predicate for LY_PATH_PRED_SIMPLE.
func checkPredicate(e *xpExpr, i int, mandatory bool) (int, string) {
	if !e.is(i, tokBrack1) {
		return i, ""
	}
	i++
	switch {
	case e.is(i, tokNameTest):
		var seen []string
		for {
			if msg := e.check(i, tokNameTest); msg != "" {
				return i, msg
			}
			full := e.text(i)
			name := full
			if c := strings.IndexByte(full, ':'); c >= 0 {
				if !mandatory {
					return i, fmt.Sprintf("Redundant prefix for \"%s\" in path.", full)
				}
				name = full[c+1:]
			} else if mandatory {
				return i, fmt.Sprintf("Prefix missing for \"%s\" in path.", full)
			}
			for _, s := range seen {
				if s == name {
					return i, fmt.Sprintf("Duplicate predicate key \"%s\" in path.", name)
				}
			}
			seen = append(seen, name)
			i++
			if msg := e.check(i, tokOperEqual); msg != "" {
				return i, msg
			}
			i++
			if !e.is(i, tokLiteral) && !e.is(i, tokNumber) && !e.is(i, tokVarRef) {
				return i, e.check(i, tokLiteral)
			}
			i++
			if msg := e.check(i, tokBrack2); msg != "" {
				return i, msg
			}
			i++
			if !e.is(i, tokBrack1) {
				return i, ""
			}
			i++
		}
	case e.is(i, tokDot):
		i++
		if msg := e.check(i, tokOperEqual); msg != "" {
			return i, msg
		}
		i++
		if i >= len(e.toks) {
			return i, errXPEOF
		}
		if !e.is(i, tokLiteral) && !e.is(i, tokNumber) {
			return i, fmt.Sprintf("Unexpected XPath token \"%s\" (\"%.15s\").", e.toks[i], e.rest(i))
		}
		i++
	case e.is(i, tokNumber):
		if n, _ := strconv.Atoi(leadingInt(e.text(i))); n == 0 {
			return i, fmt.Sprintf("Invalid positional predicate \"%s\".", e.text(i))
		}
		i++
	case i >= len(e.toks):
		return i, errXPEOF
	default:
		return i, fmt.Sprintf("Unexpected XPath token \"%s\" (\"%.15s\").", e.toks[i], e.rest(i))
	}
	if msg := e.check(i, tokBrack2); msg != "" {
		return i, msg
	}
	return i + 1, ""
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

// pathCompile ports _ly_path_compile (not leafref, LY_PATH_TARGET_SINGLE, not XPath).
func pathCompile(a *storeArgs, e *xpExpr) (Path, string) {
	output := a.ctx != nil && a.ctx.InOutput()
	var path Path
	var parent *schema.Node
	i := 1
	for {
		if n := len(path); n > 0 && path[n-1].Node.Kind == schema.List && path[n-1].Preds == nil {
			return nil, fmt.Sprintf("Predicate missing for %s \"%s\" in path.", kindName(schema.List), path[n-1].Node.Name)
		}
		if msg := e.check(i, tokNameTest); msg != "" {
			return nil, msg
		}
		node, msg := compileSNode(a, parent, e.text(i), output)
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
		if !e.is(i, tokOperPath) {
			break
		}
		i++
	}
	if i < len(e.toks) {
		return nil, fmt.Sprintf("Unexpected XPath token \"%s\" (\"%.15s\").", e.toks[i], e.rest(i))
	}
	if last := path[len(path)-1]; (last.Node.Kind == schema.List || last.Node.Kind == schema.LeafList) && last.Preds == nil {
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
	var nodes []*schema.Node
	if parent == nil {
		nodes = mod.Top
	} else if parent.Kind != schema.AnyData && parent.Kind != schema.AnyXML {
		nodes = parent.Children
	}
	if n := getNext(nodes, mod, name, output); n != nil {
		return n, ""
	}
	return nil, fmt.Sprintf("Not found node \"%s\" in path.", name)
}

// getNext finds a data child like lys_getnext: through choice, case and the input or output of an
// operation.
func getNext(nodes []*schema.Node, mod *schema.Module, name string, output bool) *schema.Node {
	for _, n := range nodes {
		switch n.Kind {
		case schema.Choice, schema.Case:
		case schema.Input:
			if output {
				continue
			}
		case schema.Output:
			if !output {
				continue
			}
		default:
			if n.Module == mod && n.Name == name {
				return n
			}
			continue
		}
		if r := getNext(n.Children, mod, name, output); r != nil {
			return r
		}
	}
	return nil
}

func literal(e *xpExpr, i int) string {
	if e.toks[i] == tokLiteral {
		return e.src[e.pos[i]+1 : e.pos[i]+e.len[i]-1]
	}
	return e.text(i)
}

// compilePredicate ports ly_path_compile_predicate.
func compilePredicate(a *storeArgs, node *schema.Node, e *xpExpr, i int) ([]PathPred, int, string) {
	if !e.is(i, tokBrack1) {
		return nil, i, ""
	}
	i++
	var preds []PathPred
	switch e.toks[i] {
	case tokNameTest:
		if node.Kind != schema.List {
			return nil, i, fmt.Sprintf("List predicate defined for %s \"%s\" in path.", kindName(node.Kind), node.Name)
		} else if len(node.Keys) == 0 {
			return nil, i, fmt.Sprintf("List predicate defined for keyless %s \"%s\" in path.", kindName(node.Kind), node.Name)
		}
		for {
			key, msg := compileSNode(a, node, e.text(i), false)
			if msg != "" {
				return nil, i, msg
			}
			if key.Kind != schema.Leaf || !key.IsKey() {
				return nil, i, fmt.Sprintf("Key expected instead of %s \"%s\" in path.", kindName(key.Kind), key.Name)
			}
			i += 2 // key, '='
			if e.toks[i] == tokVarRef {
				return nil, i, "Variable reference not allowed in an instance-identifier."
			}
			v, d := storeKey(a, key, literal(e, i))
			if d != nil {
				return nil, i, d.Msg
			}
			preds = append(preds, PathPred{Kind: PredKey, Key: key, Value: v})
			i += 2 // value, ']'
			if !e.is(i, tokBrack1) {
				break
			}
			i++
		}
		if len(preds) != len(node.Keys) {
			return nil, i, fmt.Sprintf("Predicate missing for a key of %s \"%s\" in path.", kindName(node.Kind), node.Name)
		}
	case tokDot:
		if node.Kind != schema.LeafList {
			return nil, i, fmt.Sprintf("Leaf-list predicate defined for %s \"%s\" in path.", kindName(node.Kind), node.Name)
		}
		v, d := storeKey(a, node, literal(e, i+2))
		if d != nil {
			return nil, i, d.Msg
		}
		preds = append(preds, PathPred{Kind: PredLeafList, Value: v})
		i += 4 // '.', '=', value, ']'
	default: // tokNumber
		if node.Kind != schema.LeafList && node.Kind != schema.List {
			return nil, i, fmt.Sprintf("Positional predicate defined for %s \"%s\" in path.", kindName(node.Kind), node.Name)
		} else if node.Config {
			return nil, i, fmt.Sprintf("Positional predicate defined for configuration %s \"%s\" in path.", kindName(node.Kind), node.Name)
		}
		pos, _ := strconv.ParseUint(leadingInt(e.text(i)), 10, 64)
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
