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
	str := func(k, def string) string {
		if v, ok := p[k].(string); ok {
			return v
		}
		return def
	}
	o := data.ParseOptions{Validate: data.ValidateOptions{MultiError: true}}
	preset, ok := dataPresets[str("data_type", "data-operational")]
	if !ok {
		return fmt.Errorf("%w: data_type %v", ErrUnsupported, p["data_type"])
	}
	preset(&o)
	if o.Unknown, ok = unknownPolicies[str("unknown", "reject")]; !ok {
		return fmt.Errorf("%w: unknown %v", ErrUnsupported, p["unknown"])
	}
	if b, _ := p["parse_only"].(bool); b {
		o.ParseOnly = true
	}
	for _, f := range list(p["parse_options"]) {
		set, ok := parseFlags[fmt.Sprint(f)]
		if !ok {
			return fmt.Errorf("%w: parse option %v %s", ErrUnsupported, f, parseUnsupported[fmt.Sprint(f)])
		}
		set(&o)
	}
	for _, f := range list(p["validate_options"]) {
		set, ok := validateFlags[fmt.Sprint(f)]
		if !ok {
			return fmt.Errorf("%w: validate option %v", ErrUnsupported, f)
		}
		set(&o.Validate)
	}
	wd, ok := wdModes[str("with_defaults", "explicit")]
	if !ok {
		return fmt.Errorf("%w: with_defaults %v", ErrUnsupported, p["with_defaults"])
	}
	var f data.Format
	switch str("format", "") {
	case "json":
		f = data.FormatJSON
	case "xml":
		f = data.FormatXML
	default:
		return fmt.Errorf("%w: format %v", ErrUnsupported, p["format"])
	}
	in, ok := p["data"].(string)
	if file, isFile := p["data_file"].(string); isFile {
		b, err := os.ReadFile(filepath.Join(r.BaseDir, file)) //nolint:gosec // fixture path
		if err != nil {
			return err
		}
		in, ok = string(b), true
	}
	if !ok {
		return fmt.Errorf("%w: no data", ErrUnsupported)
	}
	tree, diags, err := data.Parse(context.Background(), strings.NewReader(in), f, s, o)
	var ve *data.ValidationError
	switch {
	case errors.Is(err, yang.ErrBudget) || errors.Is(err, data.ErrUnsupported) || errors.Is(err, yang.ErrUnsupported) ||
		errors.Is(err, errors.ErrUnsupported):
		return unsupported(err)
	case errors.As(err, &ve):
		resp["verdict"], resp["rc"] = "invalid", codeJSON(ve.RC())
	case err != nil:
		return err
	default:
		resp["verdict"], resp["rc"] = "valid", codeJSON("LY_SUCCESS")
	}
	resp["diagnostics"] = diagsJSON(diags, "data")
	resp["tree"] = nil
	if tree != nil && !empty(tree) {
		po := data.PrintOptions{WithDefaults: wd}
		var j, x strings.Builder
		if err := tree.PrintJSON(&j, po); err != nil {
			return unsupported(err)
		}
		if err := tree.PrintXML(&x, po); err != nil {
			return unsupported(err)
		}
		resp["tree"] = map[string]any{"json": j.String(), "xml": x.String()}
		resp["typed"] = typedJSON(tree)
	}
	return nil
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
