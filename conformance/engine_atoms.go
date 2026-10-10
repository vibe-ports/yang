// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vibe-ports/yang"
)

// runAtoms is lyoracle.c op_atoms: Schema.FindXPathAtoms for xpath (lys_find_xpath_atoms) or
// Schema.FindPathAtoms for path (lys_find_path_atoms) from the schema node of context_path.
func runAtoms(r Request, s *yang.Schema, resp map[string]any, _ executionContext) error {
	p := r.Params
	if err := checkOpInit(r, s, "atoms"); err != nil {
		return err
	}
	var o yang.AtomOptions
	for _, f := range list(p["atom_options"]) {
		switch f {
		case "schema":
			o.Schema = true
		case "output":
			o.Output = true
		case "no_match_error":
			o.NoMatchError = true
		default:
			return fmt.Errorf("unknown atom option %v", f)
		}
	}
	xp, isXPath := p["xpath"].(string)
	path, isPath := p["path"].(string)
	if isXPath == isPath {
		return requestError("atoms needs exactly one of xpath and path")
	}
	var node *yang.SchemaNode
	if cp, ok := p["context_path"].(string); ok {
		// lys_find_path; FindSchema looks into rpc input before output, as lys_find_path does
		// without output, so an output context below an rpc is not supported
		// lys_find_path parses with lyxp (tree_schema.c:851), which skips XML whitespace around
		// tokens; FindSchema takes the bare path, so the surrounding whitespace is dropped here.
		n, err := s.FindSchema(strings.Trim(cp, " \t\n\r"))
		if err != nil {
			return requestError("context_path %s not found", cp)
		}
		for a := n; o.Output && a != nil; a = a.Parent() {
			if k := a.Kind(); a != n && (k == yang.KindRPC || k == yang.KindAction) {
				return fmt.Errorf("%w: output context_path below an operation", ErrUnsupported)
			}
		}
		node = n
	}
	var atoms []*yang.SchemaNode
	var diags []yang.Diagnostic
	var err error
	phase := "xpath"
	if isXPath {
		atoms, diags, err = s.FindXPathAtoms(node, xp, o)
	} else {
		phase = "path"
		atoms, diags, err = s.FindPathAtoms(node, path, o)
	}
	resp["diagnostics"] = diagsJSON(diags, phase)
	if err != nil {
		if errors.Is(err, yang.ErrBudget) {
			return unsupported(err)
		}
		rc := "LY_EINT"
		for _, d := range diags {
			if !d.Warning {
				rc = d.Err
			}
		}
		resp["verdict"], resp["rc"], resp["atoms"] = "invalid", codeJSON(rc), nil
		return nil
	}
	out := []any{}
	for _, n := range atoms {
		out = append(out, n.Path())
	}
	resp["verdict"], resp["rc"], resp["atoms"] = "valid", codeJSON("LY_SUCCESS"), out
	return nil
}
