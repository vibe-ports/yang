// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	"parse_only": true, "parse_options": true, "validate_options": true, "with_defaults": true}

// runData is lyoracle.c op_data for datastore data: lyd_parse_data with the preset's flags and
// LYD_VALIDATE_MULTI_ERROR, then the printed tree (none for an empty tree: libyang's is NULL).
func runData(r Request, s *yang.Schema, resp map[string]any) error {
	p := r.Params
	for k := range p {
		if !dataKeys[k] {
			return fmt.Errorf("%w: data request field %s", ErrUnsupported, k)
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

// printTree is lyoracle.c print_tree with siblings: the JSON and XML printouts, "" for no tree.
func printTree(tree *data.Tree, wd data.WD) (map[string]any, error) {
	po := data.PrintOptions{WithDefaults: wd}
	var j, x strings.Builder
	if tree != nil && !empty(tree) {
		if err := tree.PrintJSON(&j, po); err != nil {
			return nil, unsupported(err)
		}
		if err := tree.PrintXML(&x, po); err != nil {
			return nil, unsupported(err)
		}
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
