// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_schema.c (lys_find_xpath_atoms, lys_find_expr_atoms,
// lys_find_path_atoms, lys_find_lypath_atoms) (BSD-3-Clause, © CESNET).

package compile

import (
	"errors"
	"fmt"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
	"github.com/vibe-ports/yang/internal/xpath"
)

// AtomOptions are the LYS_FIND_* options of the atom queries.
type AtomOptions struct {
	Schema       bool // LYS_FIND_XP_SCHEMA: the when/must accessible tree
	Output       bool // LYS_FIND_XP_OUTPUT: RPC/action output instead of input
	NoMatchError bool // LYS_FIND_NO_MATCH_ERROR: a step matching nothing is an error
}

// FindXPathAtoms is lys_find_xpath_atoms over a compiled set: the schema nodes the JSON-format
// expression src needs, from node (nil: the document root), in libyang's set order, roots left out.
// The diagnostics are what libyang logs, warnings included; a failed query returns the failing
// diagnostic as its error (Err = the return code), or an error wrapping ErrBudget.
func FindXPathAtoms(set *schema.Set, node *schema.Node, src string, o AtomOptions) ([]*schema.Node, []Diagnostic, error) {
	if err := sameCtx(set, node, "lys_find_xpath_atoms"); err != nil {
		return nil, []Diagnostic{*err}, err
	}
	var diags []Diagnostic
	log := func(level Level, rc string) func(string) {
		return func(msg string) { diags = append(diags, Diagnostic{Level: level, Err: rc, Msg: msg}) }
	}
	fail := func(err error) ([]*schema.Node, []Diagnostic, error) {
		var xe *xpath.Error
		switch {
		case errors.Is(err, xpath.ErrNoMatch):
			for i := len(diags) - 1; i >= 0; i-- {
				if diags[i].Level == LevelError {
					d := diags[i] // later warnings do not replace the failing no-match diagnostic
					return nil, diags, &d
				}
			}
			return nil, diags, err
		case errors.Is(err, xpath.ErrBudget):
			return nil, diags, fmt.Errorf("%w: the atom query needs more than %d XPath steps", ErrBudget, xpath.DefaultMaxSteps)
		case errors.As(err, &xe):
			code := ly.Success // LOGERR without a validation code (an undefined variable)
			for k := ly.Success; k <= ly.Other; k++ {
				if k.String() == xe.VECode {
					code = k
				}
			}
			d := Diagnostic{Level: LevelError, Err: xe.Err, Code: code, Msg: xe.Msg}
			// Validation errors from lexing/evaluation use the query context
			// (LOGVAL_SXPATH); reparse and plain LOGERR errors have no location.
			if node != nil && xe.VECode != "" && xe.Origin != xpath.OriginReparse {
				d.SchemaPath = node.LogPath()
			}
			diags = append(diags, d)
			last := diags[len(diags)-1]
			return nil, diags, &last
		}
		return nil, diags, err
	}
	e, err := xpath.Compile(src, jsonNames{set})
	if err != nil {
		return fail(err)
	}
	atoms, err := e.Atomize(xpath.AtomizeContext{Node: wrap(node), SchemaRules: o.Schema, Output: o.Output,
		Schema: setInfo{set}, Warn: log(LevelWarning, "LY_SUCCESS"), NoMatchError: o.NoMatchError,
		Error: log(LevelError, "LY_ENOTFOUND")})
	if err != nil {
		return fail(err)
	}
	var out []*schema.Node
	for _, a := range atoms {
		if a.Node != nil { // LYXP_NODE_ELEM only
			out = append(out, unwrap(a.Node))
		}
	}
	return out, diags, nil
}

// FindPathAtoms is lys_find_path_atoms: the nodes of the JSON data path (simple predicates, any
// number of targets) from node (nil: the root), each followed by the keys of its list predicate,
// as lys_find_lypath_atoms adds them; a failure is a LYVE_XPATH error at the node it is about.
func FindPathAtoms(set *schema.Set, node *schema.Node, path string, output bool) ([]*schema.Node, []Diagnostic, error) {
	if err := sameCtx(set, node, "lys_find_path_atoms"); err != nil {
		return nil, []Diagnostic{*err}, err
	}
	p, msg, at := types.CompilePath(set, node, path, output, true)
	if msg != "" {
		if at == nil {
			at = node // ly_path_parse logs at the context node (LOG_LOCSET)
		}
		d := Diagnostic{Level: LevelError, Err: string(eValid), Code: ly.XPath, Msg: msg}
		if at != nil {
			d.SchemaPath = at.LogPath()
		}
		return nil, []Diagnostic{d}, &d
	}
	var out []*schema.Node
	add := func(n *schema.Node) { // ly_set_add without duplicates
		for _, x := range out {
			if x == n {
				return
			}
		}
		out = append(out, n)
	}
	for _, seg := range p {
		add(seg.Node)
		for _, pr := range seg.Preds {
			if pr.Kind == types.PredKey {
				add(pr.Key)
			}
		}
	}
	return out, nil, nil
}

// sameCtx is LY_CHECK_CTX_EQUAL_RET: the context node must belong to set (a node of another
// snapshot, even of the same Context, is foreign).
func sameCtx(set *schema.Set, node *schema.Node, fn string) *Diagnostic {
	if node == nil {
		return nil
	}
	for _, m := range set.Modules {
		if m == node.Module {
			return nil
		}
	}
	return &Diagnostic{Level: LevelError, Err: string(eInval), Code: ly.Success,
		Msg: fmt.Sprintf("Different contexts mixed in a \"%s\" function call.", fn)}
}

// jsonNames binds the prefixes of an LY_VALUE_JSON expression: implemented module names;
// unprefixed names match any module.
type jsonNames struct{ set *schema.Set }

func (j jsonNames) Resolve(prefix string) (string, bool) {
	if m := j.set.Implemented(prefix); m != nil {
		return m.Name, true
	}
	return "", false
}

func (jsonNames) Prefix(module string) string { return module }
func (jsonNames) Default() string             { return "" }

// setInfo is schemaInfo over a compiled set instead of a context being compiled.
type setInfo struct{ set *schema.Set }

func (si setInfo) ident(id xpath.Ident) *schema.Identity {
	for m := range si.set.All(id.Module) {
		if x := m.Identity(id.Name); x != nil {
			return x
		}
	}
	return nil
}

func (si setInfo) HasIdentity(id xpath.Ident) bool { return si.ident(id) != nil }

func (si setInfo) IsDerived(base, id xpath.Ident) bool {
	b, x := si.ident(base), si.ident(id)
	return b != nil && x != nil && types.IsDerived(b, x)
}

func (si setInfo) TopLevel(module, name string) []xpath.SchemaNode {
	var out []xpath.SchemaNode
	for _, m := range si.set.Modules {
		if !m.Implemented || module != "" && m.Name != module {
			continue
		}
		for n := range schema.GetNext(nil, m.Top, 0) {
			if n.Name == name && n.Kind != schema.RPC && n.Kind != schema.Notification {
				out = append(out, snode{n})
			}
		}
	}
	return out
}

func (si setInfo) ExtNode(parent xpath.SchemaNode, module, name string) xpath.SchemaNode {
	mod := si.set.Implemented(module)
	var sparent *schema.Node
	if parent != nil {
		sparent = unwrap(parent)
	}
	n, _ := schema.FindExtNode(sparent, mod, mod, name, true)
	return wrap(n)
}

func (si setInfo) Modules() []string {
	var out []string
	for _, m := range si.set.Modules {
		if m.Implemented {
			out = append(out, m.Name)
		}
	}
	return out
}

func (si setInfo) ModuleNodes(module string) (data, rpcs, notifs []xpath.SchemaNode) {
	m := si.set.Implemented(module)
	if m == nil {
		return nil, nil, nil
	}
	for _, n := range m.Top {
		switch n.Kind {
		case schema.RPC:
			rpcs = append(rpcs, snode{n})
		case schema.Notification:
			notifs = append(notifs, snode{n})
		default:
			data = append(data, snode{n})
		}
	}
	return data, rpcs, notifs
}
