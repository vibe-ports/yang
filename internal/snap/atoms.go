// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_schema.c (lys_find_xpath_atoms, lys_find_path_atoms)
// (BSD-3-Clause, © CESNET).

package snap

import (
	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

// Diagnostic is an error or warning libyang would log against the context.
type Diagnostic struct {
	Phase      string // "parse" or "compile" (oracle phases, design 06 §1.6); empty for data trees and queries
	Warning    bool
	Err        string // libyang LY_ERR name, e.g. "LY_EVALID"
	Code       string // libyang LY_VECODE name, e.g. "LYVE_REFERENCE"
	SchemaPath string
	DataPath   string // data trees: lyd_path of the node the message is about (design 07 §1.10)
	AppTag     string // data trees: error-app-tag
	Line       int    // 0 when unknown
	Msg        string
}

// Convert turns the compiler's diagnostics into Diagnostics.
func Convert(ds []compile.Diagnostic) []Diagnostic {
	r := make([]Diagnostic, len(ds))
	for i, d := range ds {
		r[i] = Diagnostic{Phase: d.Phase, Warning: d.Level == compile.LevelWarning, Err: d.Err, Code: d.Code.String(),
			SchemaPath: d.SchemaPath, Line: d.Line, Msg: d.Msg}
	}
	return r
}

// AtomOptions are the options of the atom queries (LYS_FIND_*).
type AtomOptions struct {
	// Schema applies the access rules of when and must expressions (LYS_FIND_XP_SCHEMA): from a
	// configuration node only configuration nodes are reachable.
	Schema bool
	// Output searches RPC and action output instead of input (LYS_FIND_XP_OUTPUT).
	Output bool
	// NoMatchError makes a step that matches no schema node an LY_ENOTFOUND error instead of a
	// warning (LYS_FIND_NO_MATCH_ERROR); a union fails only when none of its branches matches.
	NoMatchError bool
}

// FindXPathAtoms returns the schema nodes needed to evaluate the XPath expression expr
// (lys_find_xpath_atoms): every node a step of it reaches, in libyang's order. Prefixes are
// module names (JSON format); node is the context node, nil for the document root. The
// diagnostics are libyang's messages, also on success (warnings for steps that match nothing).
// A failed query returns a non-nil error; its diagnostic is the last error in the slice, whose
// Err is libyang's return code (LY_EVALID for a syntax error or an unknown prefix, LY_ENOTFOUND
// with NoMatchError). An error wrapping yang.ErrBudget means the step budget ran out.
// A node that does not belong to this snapshot (another context's, or an older snapshot of the
// same context) is an LY_EINVAL error, as libyang refuses mixed contexts.
func (x *Schema) FindXPathAtoms(node *Node, expr string, o AtomOptions) ([]*Node, []Diagnostic, error) {
	ns, ds, err := compile.FindXPathAtoms(x.s, unwrapNode(node), expr, compile.AtomOptions(o))
	return wrapNodes(ns), Convert(ds), err
}

// FindPathAtoms returns the schema nodes of a JSON data path such as "/mod:a/b[k='x']/c"
// (lys_find_path_atoms): every node of the path, each list followed by the keys of its predicate.
// The path starts at node when relative (nil: absolute paths only); only o.Output is read. A
// path that does not compile is an LY_EVALID error, its diagnostic in the slice; a node of
// another snapshot is an LY_EINVAL error, as for FindXPathAtoms.
func (x *Schema) FindPathAtoms(node *Node, path string, o AtomOptions) ([]*Node, []Diagnostic, error) {
	ns, ds, err := compile.FindPathAtoms(x.s, unwrapNode(node), path, o.Output)
	return wrapNodes(ns), Convert(ds), err
}

func unwrapNode(n *Node) *schema.Node {
	if n == nil {
		return nil
	}
	return n.n
}

func wrapNodes(ns []*schema.Node) []*Node {
	if ns == nil {
		return nil
	}
	out := make([]*Node, len(ns))
	for i, n := range ns {
		out[i] = &Node{n}
	}
	return out
}
