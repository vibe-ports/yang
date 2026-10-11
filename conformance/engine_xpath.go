// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// xpathKeys are the op xpath request fields runXPath handles; any other (operations) makes the
// fixture unsupported rather than silently ignored.
var xpathKeys = map[string]bool{"op": true, "base_dir": true, "searchdirs": true, "modules": true,
	"context_options": true, "format": true, "data_type": true, "data": true, "data_file": true, "unknown": true,
	"parse_only": true, "parse_options": true, "validate_options": true, "xpath": true, "context_path": true,
	"vars": true, "cur_module": true, "xpath_format": true, "namespaces": true}

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
	switch str(p, "data_type", "") {
	case "rpc", "reply", "notif":
		return fmt.Errorf("%w: xpath over operation data (data_type %v): the engine parses operations once ParseOp is exported (#97)",
			ErrUnsupported, p["data_type"])
	}
	// str_of is called while op_xpath initializes its locals, before it parses the data. Keep these
	// type checks here, but defer the option semantics until after parse_one, in oracle order.
	var hasCur bool
	if o.Module, hasCur, err = strField(p, "cur_module"); err != nil {
		return err
	}
	f, hasF, err := strField(p, "xpath_format")
	if err != nil {
		return err
	}
	tree, pd, err := parseInput(r, s, p, "data")
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
	// op_xpath, once the data parsed: a given cur_module ("" included) must be implemented
	// ("cur_module %s not implemented", a request-error)
	if hasCur && s.Implemented(o.Module) == nil {
		return fmt.Errorf("cur_module %s not implemented (a request-error)", o.Module)
	}
	// The remaining request checks follow cur_module: namespaces is a list, the format is known,
	// and namespace entries are accepted only for XML and need a URI.
	ns, hasNS := p["namespaces"]
	if hasNS {
		if _, isList := ns.([]any); !isList {
			return errors.New("namespaces must be a list (a request-error)")
		}
	}
	switch {
	case !hasF || f == "json":
	case f == "xml":
		o.Format = data.XPathXML
		for _, x := range list(ns) {
			n, _ := x.(map[string]any)
			uri, hasURI, err := strField(n, "uri")
			if err != nil {
				return err
			}
			if !hasURI {
				return errors.New("namespaces need a uri (a request-error)")
			}
			pfx, _, err := strField(n, "prefix")
			if err != nil {
				return err
			}
			o.Namespaces = append(o.Namespaces, data.XPathNamespace{Prefix: pfx, URI: uri})
		}
	case f == "schema":
		o.Format = data.XPathSchema
	default:
		return fmt.Errorf("unknown xpath_format %s (a request-error)", f)
	}
	if hasNS && o.Format != data.XPathXML {
		return errors.New("namespaces need xpath_format xml (a request-error)")
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

// strField is lyoracle.c str_of: the string field k of m and whether it is given; absent or null
// is not given, any other non-string a request-error.
func strField(m map[string]any, k string) (string, bool, error) {
	switch v := m[k].(type) {
	case nil:
		return "", false, nil
	case string:
		return v, true, nil
	}
	return "", false, fmt.Errorf("field %q must be a string (a request-error)", k)
}
