// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"errors"
	"fmt"

	"github.com/vibe-ports/yang"
)

// atomKeys are the op atoms request fields runAtoms handles.
var atomKeys = map[string]bool{"op": true, "base_dir": true, "searchdirs": true, "modules": true,
	"context_options": true, "xpath": true, "path": true, "context_path": true, "atom_options": true}

// runAtoms is lyoracle.c op_atoms: Schema.FindXPathAtoms for xpath (lys_find_xpath_atoms) or
// Schema.FindPathAtoms for path (lys_find_path_atoms) from the schema node of context_path.
func runAtoms(r Request, s *yang.Schema, resp map[string]any) error {
	p := r.Params
	for k := range p {
		if !atomKeys[k] {
			return fmt.Errorf("%w: atoms request field %s", ErrUnsupported, k)
		}
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
		return errors.New("atoms needs exactly one of xpath and path")
	}
	var node *yang.SchemaNode
	if cp, ok := p["context_path"].(string); ok {
		// lys_find_path; FindSchema looks into rpc input before output, as lys_find_path does
		// without output, so an output context below an rpc is not supported
		n, err := s.FindSchema(cp)
		if err != nil {
			return fmt.Errorf("context_path %s: %w", cp, err)
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
