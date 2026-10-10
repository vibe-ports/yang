// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// dataPresets are lyoracle.c dparams_of for the datastore data types: parse and validate flags.
var dataPresets = map[string]func(*data.ParseOptions){
	"data-operational": func(o *data.ParseOptions) { o.Validate.Operational = true },
	"data":             func(*data.ParseOptions) {},
	"config":           func(o *data.ParseOptions) { o.NoState, o.Validate.NoState = true, true },
	"get":              func(o *data.ParseOptions) { o.ParseOnly = true },
	"getconfig":        func(o *data.ParseOptions) { o.ParseOnly, o.NoState = true, true },
	"edit":             func(o *data.ParseOptions) { o.ParseOnly, o.NoState = true, true },
}

var parseFlags = map[string]func(*data.ParseOptions){
	"only":                  func(o *data.ParseOptions) { o.ParseOnly = true },
	"no_state":              func(o *data.ParseOptions) { o.NoState = true },
	"store_only":            func(o *data.ParseOptions) { o.StoreOnly = true },
	"json_null":             func(o *data.ParseOptions) { o.JSONNull = true },
	"json_string_datatypes": func(o *data.ParseOptions) { o.JSONStringDatatypes = true },
}

// parseUnsupported are the oracle's parse_options this engine refuses, with the reason.
var parseUnsupported = map[string]string{
	"anydata_strict": "(anydata/anyxml instances are M5, U-0043)",
	"ordered":        "(not exported: LYD_PARSE_ORDERED input order, D-0058)",
	"when_true":      "(not exported yet)",
}

var validateFlags = map[string]func(*data.ValidateOptions){
	"no_state":    func(o *data.ValidateOptions) { o.NoState = true },
	"present":     func(o *data.ValidateOptions) { o.Present = true },
	"multi_error": func(o *data.ValidateOptions) { o.MultiError = true },
	"operational": func(o *data.ValidateOptions) { o.Operational = true },
	"no_defaults": func(o *data.ValidateOptions) { o.NoDefaults = true },
}

var unknownPolicies = map[string]data.UnknownPolicy{"reject": data.Reject, "skip": data.Skip, "opaque": data.Opaque}

var wdModes = map[string]data.WD{"explicit": data.WDExplicit, "trim": data.WDTrim, "all": data.WDAll,
	"all-tagged": data.WDAllTagged, "implicit-tagged": data.WDImplicitTagged}

// dataKeys are the op data request fields runData handles; any other (print options, subtree
// printing, operations) makes the fixture unsupported rather than silently ignored.
var dataKeys = map[string]bool{"op": true, "base_dir": true, "searchdirs": true, "modules": true,
	"context_options": true, "format": true, "data_type": true, "data": true, "data_file": true, "unknown": true,
	"parse_only": true, "parse_options": true, "validate_options": true, "with_defaults": true,
	"operational": true, "operational_file": true, "operational_format": true, "rpc": true, "rpc_file": true,
	"keep_input": true}

// opTypes are lyoracle.c dparams_of's operation data types.
var opTypes = map[string]data.OpType{"rpc": data.OpRPC, "reply": data.OpReply, "notif": data.OpNotif}

// runData is lyoracle.c op_data for datastore data: lyd_parse_data with the preset's flags and
// LYD_VALIDATE_MULTI_ERROR, then the printed tree (none for an empty tree: libyang's is NULL).
func runData(r Request, s *yang.Schema, resp map[string]any) error {
	p := r.Params
	for k := range p {
		if !dataKeys[k] {
			return fmt.Errorf("%w: data request field %s", ErrUnsupported, k)
		}
	}
	if typ, ok := opTypes[str(p, "data_type", "")]; ok {
		return runOp(r, s, resp, typ)
	}
	for _, k := range []string{"operational", "operational_file", "operational_format", "rpc", "rpc_file", "keep_input"} {
		if _, ok := p[k]; ok {
			return fmt.Errorf("%w: %s with a datastore data type", ErrUnsupported, k)
		}
	}
	o, err := dataOptions(p)
	if err != nil {
		return err
	}
	wd, err := wdOf(p)
	if err != nil {
		return err
	}
	f, err := formatOf(p)
	if err != nil {
		return err
	}
	in, err := inputOf(r, p, "data")
	if err != nil {
		return err
	}
	tree, diags, err := data.Parse(context.Background(), strings.NewReader(in), f, s, o)
	rc, err := rcOf(err)
	if err != nil {
		return err
	}
	resp["verdict"], resp["rc"] = "valid", codeJSON(rc)
	if rc != "LY_SUCCESS" {
		resp["verdict"] = "invalid"
	}
	resp["diagnostics"] = diagsJSON(diags, "data")
	resp["tree"] = nil
	if tree != nil && !empty(tree) {
		if resp["tree"], err = printTree(tree, wd); err != nil {
			return err
		}
		resp["typed"] = typedJSON(tree)
	}
	return nil
}

// printTree is lyoracle.c print_tree with siblings: the JSON and XML printouts. libyang prints
// a NULL tree as "{}\n" in JSON and nothing in XML (a trim can free every node).
func printTree(tree *data.Tree, wd data.WD) (map[string]any, error) {
	po := data.PrintOptions{WithDefaults: wd}
	var j, x strings.Builder
	if tree == nil || empty(tree) {
		return map[string]any{"json": "{}\n", "xml": ""}, nil
	}
	if err := tree.PrintJSON(&j, po); err != nil {
		return nil, unsupported(err)
	}
	if err := tree.PrintXML(&x, po); err != nil {
		return nil, unsupported(err)
	}
	return map[string]any{"json": j.String(), "xml": x.String()}, nil
}

// str is the string field k of p, def when absent.
func str(p map[string]any, k, def string) string {
	if v, ok := p[k].(string); ok {
		return v
	}
	return def
}

// dataOptions is lyoracle.c dparams_of for datastore data: the data_type preset, unknown policy,
// parse_only, parse_options and validate_options (LYD_VALIDATE_MULTI_ERROR always set).
func dataOptions(p map[string]any) (data.ParseOptions, error) {
	o := data.ParseOptions{Validate: data.ValidateOptions{MultiError: true}}
	preset, ok := dataPresets[str(p, "data_type", "data-operational")]
	if !ok {
		return o, fmt.Errorf("%w: data_type %v", ErrUnsupported, p["data_type"])
	}
	preset(&o)
	if o.Unknown, ok = unknownPolicies[str(p, "unknown", "reject")]; !ok {
		return o, fmt.Errorf("%w: unknown %v", ErrUnsupported, p["unknown"])
	}
	if b, _ := p["parse_only"].(bool); b {
		o.ParseOnly = true
	}
	for _, f := range list(p["parse_options"]) {
		set, ok := parseFlags[fmt.Sprint(f)]
		if !ok {
			return o, fmt.Errorf("%w: parse option %v %s", ErrUnsupported, f, parseUnsupported[fmt.Sprint(f)])
		}
		set(&o)
	}
	for _, f := range list(p["validate_options"]) {
		set, ok := validateFlags[fmt.Sprint(f)]
		if !ok {
			return o, fmt.Errorf("%w: validate option %v", ErrUnsupported, f)
		}
		set(&o.Validate)
	}
	return o, nil
}

// wdOf is lyoracle.c wd_of: the with_defaults print mode, explicit by default.
func wdOf(p map[string]any) (data.WD, error) {
	wd, ok := wdModes[str(p, "with_defaults", "explicit")]
	if !ok {
		return wd, fmt.Errorf("%w: with_defaults %v", ErrUnsupported, p["with_defaults"])
	}
	return wd, nil
}

// formatOf is lyoracle.c fmt_of: JSON unless the field says xml.
func formatOf(p map[string]any) (data.Format, error) {
	switch str(p, "format", "json") {
	case "json":
		return data.FormatJSON, nil
	case "xml":
		return data.FormatXML, nil
	}
	return 0, fmt.Errorf("%w: format %v", ErrUnsupported, p["format"])
}

// inputOf is lyoracle.c input_of: the inline field key or the file named by key+"_file".
func inputOf(r Request, p map[string]any, key string) (string, error) {
	if file, ok := p[key+"_file"].(string); ok {
		b, err := os.ReadFile(filepath.Join(r.BaseDir, file)) //nolint:gosec // fixture path
		return string(b), err
	}
	if in, ok := p[key].(string); ok {
		return in, nil
	}
	return "", fmt.Errorf("%w: no %s", ErrUnsupported, key)
}

// rcOf is the LY_ERR name of a data call's error; errors the port refuses on purpose (budgets,
// unsupported input) make the fixture unsupported, other errors are returned.
func rcOf(err error) (string, error) {
	var ve *data.ValidationError
	switch {
	case err == nil:
		return "LY_SUCCESS", nil
	case errors.Is(err, yang.ErrBudget) || errors.Is(err, data.ErrUnsupported) || errors.Is(err, yang.ErrUnsupported) ||
		errors.Is(err, errors.ErrUnsupported):
		return "", unsupported(err)
	case errors.As(err, &ve):
		return ve.RC(), nil
	}
	return "", err
}

func empty(t *data.Tree) bool {
	for range t.Top() {
		return false
	}
	return true
}

// typedJSON is lyoracle.c typed_json without the skipped fields (Yang.SkippedFields).
func typedJSON(tree *data.Tree) []any {
	out := []any{}
	for top := range tree.Top() {
		for n := range top.All() {
			f := n.Flags()
			o := map[string]any{"path": n.Path(), "schema": nil, "kind": "opaque", "value": nil,
				"flags": map[string]any{"default": f&data.FlagDefault != 0, "when_true": f&data.FlagWhenTrue != 0,
					"new": f&data.FlagNew != 0}}
			sn := n.Schema()
			if sn != nil {
				o["schema"], o["kind"] = sn.Path(), kindNames[sn.Kind()]
			}
			if sn == nil || sn.Kind() == yang.KindLeaf || sn.Kind() == yang.KindLeafList {
				o["value"] = map[string]any{"canonical": n.Value()}
			}
			out = append(out, o)
		}
	}
	return out
}

// runOp is lyoracle.c op_data for the operation data types (parse_one, load_oper): the
// operational tree parsed only (lyd_parse_data with LYD_PARSE_ONLY, unknown nodes dropped), the
// operation parsed (ParseOp; a reply with an rpc request is parsed into that request, its input
// removed), then, unless parse_only, ValidateOp against the operational tree and yanglint's
// check_operation_parent (a nested action or notification needs its parent in the operational
// tree). The tree is printed from its first node without siblings.
func runOp(r Request, s *yang.Schema, resp map[string]any, typ data.OpType) error {
	p := r.Params
	unknown, ok := unknownPolicies[str(p, "unknown", "reject")]
	if !ok {
		return fmt.Errorf("%w: unknown %v", ErrUnsupported, p["unknown"])
	}
	parseOnly, _ := p["parse_only"].(bool)
	wd, err := wdOf(p)
	if err != nil {
		return err
	}
	f, err := formatOf(p)
	if err != nil {
		return err
	}
	ctx := context.Background()
	diags := []any{}
	resp["diagnostics"] = diags
	var oper *data.Tree
	if _, ok := p["operational"]; ok || p["operational_file"] != nil {
		in, err := inputOf(r, p, "operational")
		if err != nil {
			return err
		}
		of := f
		if v, ok := p["operational_format"]; ok {
			if of, err = formatOf(map[string]any{"format": v}); err != nil {
				return err
			}
		}
		t, d, perr := data.Parse(ctx, strings.NewReader(in), of, s, data.ParseOptions{Unknown: data.Skip, ParseOnly: true})
		rc, err := rcOf(perr)
		if err != nil {
			return err
		}
		diags = append(diags, diagsJSON(d, "operational")...)
		resp["diagnostics"] = diags
		if rc != "LY_SUCCESS" {
			resp["verdict"] = "operational-error"
			return nil
		}
		oper = t
	}
	in, err := inputOf(r, p, "data")
	if err != nil {
		return err
	}
	var res data.OpResult
	var d []yang.Diagnostic
	var perr error
	_, hasRPC := p["rpc"]
	_, hasRPCFile := p["rpc_file"]
	given := hasRPC || hasRPCFile
	var rpcIn string
	if given {
		// an rpc that is missing or unreadable fails the request; it never falls back to a
		// standalone reply
		if rpcIn, err = inputOf(r, p, "rpc"); err != nil {
			return err
		}
	}
	if typ != data.OpReply && given {
		return fmt.Errorf("%w: an rpc request with data_type %v (ParseOpOptions.Request is for replies only, deviations.md U-0106)",
			ErrUnsupported, p["data_type"])
	}
	keep, _ := p["keep_input"].(bool)
	if typ == data.OpReply && given {
		req, rd, rerr := data.ParseOp(ctx, strings.NewReader(rpcIn), f, s, data.OpRPC, data.ParseOpOptions{Unknown: unknown})
		diags = append(diags, diagsJSON(rd, "rpc")...)
		perr = rerr
		if rerr == nil {
			for _, c := range slices.Collect(req.Op.Children()) { // lyd_free_siblings(lyd_child(op))
				if keep {
					break
				}
				if err := c.Remove(); err != nil {
					return err
				}
			}
			res, d, perr = data.ParseOp(ctx, strings.NewReader(in), f, s, typ,
				data.ParseOpOptions{Unknown: unknown, Request: req.Op})
			diags = append(diags, diagsJSON(d, "data")...)
			if perr != nil {
				resp["request_typed"] = typedJSON(req.Tree) // what the failed reply left of the request
			}
		}
	} else {
		res, d, perr = data.ParseOp(ctx, strings.NewReader(in), f, s, typ, data.ParseOpOptions{Unknown: unknown})
		diags = append(diags, diagsJSON(d, "data")...)
	}
	rc, err := rcOf(perr)
	if err != nil {
		return err
	}
	if rc == "LY_SUCCESS" && !parseOnly {
		vd, verr := res.Tree.ValidateOp(ctx, typ, data.ValidateOpOptions{Operational: oper})
		if rc, err = rcOf(verr); err != nil {
			return err
		}
		diags = append(diags, diagsJSON(vd, "validate_op")...)
		if par := res.Op.Parent(); rc == "LY_SUCCESS" && par != nil {
			// yanglint check_operation_parent: the operation's parent in the operational tree
			path := par.Path()
			found := false
			if oper != nil {
				nodes, _, ferr := oper.FindXPath(path, data.XPathOptions{})
				found = ferr == nil && len(nodes) > 0
			}
			if !found {
				diags = append(diags, map[string]any{"phase": "operation_parent", "level": "error",
					"code": codeJSON("LY_EVALID"), "source": "lyoracle", "data_path": path,
					"msg": "operation parent not found in the operational tree"})
				rc = "LY_EVALID"
			}
		}
	}
	resp["diagnostics"] = diags
	resp["verdict"], resp["rc"] = "valid", codeJSON(rc)
	resp["tree"] = nil
	if rc != "LY_SUCCESS" {
		resp["verdict"] = "invalid"
		return nil
	}
	var first *data.Node
	for n := range res.Tree.Top() {
		first = n
		break
	}
	po := data.PrintOptions{WithDefaults: wd}
	var j, x strings.Builder
	if err := first.PrintJSON(&j, po); err != nil {
		return unsupported(err)
	}
	if err := first.PrintXML(&x, po); err != nil {
		return unsupported(err)
	}
	resp["tree"] = map[string]any{"json": j.String(), "xml": x.String()}
	resp["typed"] = typedJSON(res.Tree)
	return nil
}
