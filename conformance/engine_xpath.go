// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// xpathKeys are the op xpath request fields runXPath handles; any other (cur_module, operations)
// makes the fixture unsupported rather than silently ignored.
var xpathKeys = map[string]bool{"op": true, "base_dir": true, "searchdirs": true, "modules": true,
	"context_options": true, "format": true, "data_type": true, "data": true, "data_file": true, "unknown": true,
	"parse_only": true, "parse_options": true, "validate_options": true, "xpath": true, "context_path": true,
	"vars": true}

// runXPath is lyoracle.c op_xpath: the data parsed as op data parses it, the context node
// selected by context_path (lyd_find_xpath from the first top-level node), then lyd_eval_xpath4
// with every output asked for, so the result keeps its type. The harness sends the request as
// JSON with sorted keys, so the vars reach lyxp_vars_set in name order.
func runXPath(r Request, s *yang.Schema, resp map[string]any) error {
	p := r.Params
	for k := range p {
		if !xpathKeys[k] {
			return fmt.Errorf("%w: xpath request field %s", ErrUnsupported, k)
		}
	}
	expr, ok := p["xpath"].(string)
	if !ok {
		return errors.New("missing xpath")
	}
	var o data.XPathOptions
	var err error
	if o.Vars, err = xpathVars(p); err != nil {
		return err
	}
	tree, pd, err := parseXPathData(r, s, p)
	rc, err := rcOf(err)
	if err != nil {
		return err
	}
	diags := diagsJSON(pd, "data")
	resp["diagnostics"] = diags
	top := topNode(tree)
	if rc != "LY_SUCCESS" || top == nil {
		resp["verdict"] = "empty-tree"
		if rc != "LY_SUCCESS" {
			resp["verdict"] = "data-error"
		}
		return nil
	}
	if cpath, ok := p["context_path"].(string); ok {
		nodes, cd, err := tree.FindXPath(cpath, data.XPathOptions{Node: top})
		diags = append(diags, diagsJSON(cd, "context_path")...)
		if err != nil {
			return fmt.Errorf("context_path %s: %w", cpath, err)
		}
		if len(nodes) != 1 {
			return fmt.Errorf("context_path %s must select exactly one node, not %d", cpath, len(nodes))
		}
		o.Node = nodes[0]
	}
	res, xd, xerr := tree.EvalXPath(expr, o)
	if rc, err = rcOf(xerr); err != nil {
		return err
	}
	resp["diagnostics"] = append(diags, diagsJSON(xd, "xpath")...) // also on success: libyang's LOGINT
	resp["verdict"], resp["rc"] = "valid", codeJSON(rc)
	resp["result"] = nil
	if xerr != nil {
		resp["verdict"] = "invalid"
		return nil
	}
	out := map[string]any{"type": res.Type.String()}
	switch res.Type {
	case data.XPathNodeSet:
		nodes := []any{}
		for _, n := range res.Nodes {
			nodes = append(nodes, n.Path())
		}
		out["nodes"] = nodes
	case data.XPathString:
		out["value"] = res.String
	case data.XPathNumber:
		switch v := res.Number; {
		case math.IsNaN(v):
			out["value"] = "NaN"
		case math.IsInf(v, 1):
			out["value"] = "Infinity"
		case math.IsInf(v, -1):
			out["value"] = "-Infinity"
		default:
			out["value"] = v
		}
	case data.XPathBoolean:
		out["value"] = res.Boolean
	}
	resp["result"] = out
	return nil
}

// parseXPathData is op_xpath's parse_one with no rpc request: the data, or for the operation data
// types the operation parsed alone (lyd_parse_op without a parent). Validating an operation also
// needs yanglint's operation-parent check against an operational tree, which op xpath has no
// field for, so an operation must be parse_only.
func parseXPathData(r Request, s *yang.Schema, p map[string]any) (*data.Tree, []yang.Diagnostic, error) {
	typ, ok := opTypes[str(p, "data_type", "")]
	if !ok {
		return parseInput(r, s, p, "data")
	}
	if po, _ := p["parse_only"].(bool); !po {
		return nil, nil, fmt.Errorf("%w: xpath over a validated operation (data_type %v without parse_only)",
			ErrUnsupported, p["data_type"])
	}
	unknown, ok := unknownPolicies[str(p, "unknown", "reject")]
	if !ok {
		return nil, nil, fmt.Errorf("%w: unknown %v", ErrUnsupported, p["unknown"])
	}
	f, err := formatOf(p)
	if err != nil {
		return nil, nil, err
	}
	in, err := inputOf(r, p, "data")
	if err != nil {
		return nil, nil, err
	}
	res, d, err := data.ParseOp(context.Background(), strings.NewReader(in), f, s, typ, data.ParseOpOptions{Unknown: unknown})
	return res.Tree, d, err
}

// xpathVars is the "vars" object of a request or step, in the order lyoracle sets them (sorted
// keys).
func xpathVars(p map[string]any) ([]data.XPathVar, error) {
	v, ok := p["vars"]
	if !ok {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("vars must be an object")
	}
	var out []data.XPathVar
	for _, k := range slices.Sorted(maps.Keys(m)) {
		val, ok := m[k].(string)
		if !ok {
			return nil, fmt.Errorf("vars value of %s must be a string", k)
		}
		out = append(out, data.XPathVar{Name: k, Value: val})
	}
	return out, nil
}
