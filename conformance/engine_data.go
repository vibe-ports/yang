// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
	"github.com/vibe-ports/yang/internal/printopts"
)

// dataPresets are lyoracle.c dparams_of's parse and validate flags for every data type.
var dataPresets = map[string]func(*data.ParseOptions){
	"config":           func(o *data.ParseOptions) { o.NoState, o.Validate.NoState = true, true }, // lyoracle.c:1223-1225
	"data-operational": func(o *data.ParseOptions) { o.Validate.Operational = true },              // lyoracle.c:1226-1227
	"data":             func(*data.ParseOptions) {},                                               // lyoracle.c:1228-1230
	"get":              func(o *data.ParseOptions) { o.ParseOnly = true },                         // lyoracle.c:1231-1232
	"getconfig":        func(o *data.ParseOptions) { o.ParseOnly, o.NoState = true, true },        // lyoracle.c:1233-1234
	"edit":             func(o *data.ParseOptions) { o.ParseOnly, o.NoState = true, true },        // lyoracle.c:1233-1234
	"rpc":              func(*data.ParseOptions) {},                                               // lyoracle.c:1235-1236
	"reply":            func(*data.ParseOptions) {},                                               // lyoracle.c:1237-1238
	"notif":            func(*data.ParseOptions) {},                                               // lyoracle.c:1239-1240
	"rpc-netconf":      func(*data.ParseOptions) {},                                               // lyoracle.c:1240-1243
	"notif-netconf":    func(*data.ParseOptions) {},                                               // lyoracle.c:1240-1243
	"reply-netconf":    func(*data.ParseOptions) {},                                               // lyoracle.c:1240-1243
	"rpc-restconf":     func(*data.ParseOptions) {},                                               // lyoracle.c:1240-1243
	"notif-restconf":   func(*data.ParseOptions) {},                                               // lyoracle.c:1240-1243
	"reply-restconf":   func(*data.ParseOptions) {},                                               // lyoracle.c:1240-1243
}

var parseFlags = map[string]func(*data.ParseOptions){
	"when_true":             func(o *data.ParseOptions) { o.WhenTrue = true },
	"anydata_strict":        func(o *data.ParseOptions) { o.AnyDataStrict = true },
	"only":                  func(o *data.ParseOptions) { o.ParseOnly = true },
	"no_state":              func(o *data.ParseOptions) { o.NoState = true },
	"store_only":            func(o *data.ParseOptions) { o.StoreOnly = true },
	"json_null":             func(o *data.ParseOptions) { o.JSONNull = true },
	"json_string_datatypes": func(o *data.ParseOptions) { o.JSONStringDatatypes = true },
}

// parseUnsupported are the oracle's parse_options this engine refuses, with the reason.
var parseUnsupported = map[string]string{
	"ordered": "(not exported: LYD_PARSE_ORDERED input order, D-0058)",
}

var validateFlags = map[string]func(*data.ValidateOptions){
	"no_state":    func(o *data.ValidateOptions) { o.NoState = true },
	"present":     func(o *data.ValidateOptions) { o.Present = true },
	"multi_error": func(o *data.ValidateOptions) { o.MultiError = true },
	"operational": func(o *data.ValidateOptions) { o.Operational = true },
	"no_defaults": func(o *data.ValidateOptions) { o.NoDefaults = true },
	"not_final":   func(o *data.ValidateOptions) { o.NotFinal = true },
}

var unknownPolicies = map[string]data.UnknownPolicy{"reject": data.Reject, "skip": data.Skip, "opaque": data.Opaque}

var wdModes = map[string]data.WD{"explicit": data.WDExplicit, "trim": data.WDTrim, "all": data.WDAll,
	"all-tagged": data.WDAllTagged, "implicit-tagged": data.WDImplicitTagged}

// printFlags is lyoracle.c print_flags. The internal flags deliberately do not add public fields
// to data.PrintOptions; they exist only so this engine can exercise the complete oracle surface.
var printFlags = map[string]struct {
	set      func(*data.PrintOptions)
	internal printopts.Flags
}{
	"shrink":                {set: func(o *data.PrintOptions) { o.Shrink = true }},
	"empty_leaf_list":       {set: func(o *data.PrintOptions) { o.EmptyLeafList = true }},
	"empty_cont":            {internal: printopts.EmptyCont},
	"json_no_nested_prefix": {internal: printopts.JSONNoNestedPrefix},
}

// opTypes are lyoracle.c dparams_of's operation data types.
var opTypes = map[string]data.OpType{"rpc": data.OpRPC, "reply": data.OpReply, "notif": data.OpNotif}

// envelopeTypes are lyoracle.c dparams_of's NETCONF and RESTCONF message data types.
var envelopeTypes = map[string]data.OpType{"rpc-netconf": data.OpRPCNetconf, "notif-netconf": data.OpNotifNetconf,
	"reply-netconf": data.OpReplyNetconf, "rpc-restconf": data.OpRPCRestconf, "notif-restconf": data.OpNotifRestconf,
	"reply-restconf": data.OpReplyRestconf}

// runEnvelope is lyoracle.c op_data for a NETCONF or RESTCONF message (parse_one, lines 1291-1320):
// the types that need a request parse into the parsed rpc, the others alone; the printed and typed
// tree is the envelope, op_typed the tree of the operation (or of the request).
func runEnvelope(r Request, s *yang.Schema, resp map[string]any, typ data.OpType) error {
	p := r.Params
	if err := checkRequest(r, s, p, execData, dataRequestRules); err != nil {
		return err
	}
	unknown, ok := unknownPolicies[str(p, "unknown", "reject")]
	if !ok {
		return fmt.Errorf("%w: unknown %v", ErrUnsupported, p["unknown"])
	}
	wd, err := wdOf(p)
	if err != nil {
		return err
	}
	f, err := formatOf(p)
	if err != nil {
		return err
	}
	in, present, err := requestInput(r, p, "data")
	if err != nil {
		return err
	}
	rpcIn, given, err := requestInput(r, p, "rpc")
	if err != nil {
		return err
	}
	keep, _ := p["keep_input"].(bool)
	ctx := context.Background()
	diags := list(resp["diagnostics"])
	o := data.ParseOpOptions{Unknown: unknown}
	var req data.OpResult
	var perr error
	if given && (typ == data.OpReplyNetconf || typ == data.OpRPCRestconf || typ == data.OpReplyRestconf) {
		var rd []yang.Diagnostic
		req, rd, perr, err = parseRequest(ctx, rpcIn, f, s, unknown, keep, typ)
		diags = append(diags, diagsJSON(rd, "rpc")...)
		if err != nil {
			return err
		}
		o.Request = req.Op
	}
	var res data.OpResult
	if perr == nil {
		var d []yang.Diagnostic
		res, d, perr = data.ParseOp(ctx, inputReader(in, present), f, s, typ, o)
		diags = append(diags, diagsJSON(d, "data")...)
		if perr != nil && o.Request != nil {
			if resp["request_typed"], err = typedJSON(req.Tree); err != nil { // what the failed parse left of the request
				return err
			}
		}
	}
	rc, err := rcOf(perr)
	if err != nil {
		return err
	}
	resp["diagnostics"] = diags
	resp["verdict"], resp["rc"] = "valid", codeJSON(rc)
	resp["tree"] = nil
	if res.Envelope != nil {
		var env *data.Node
		for n := range res.Envelope.Top() {
			env = n
			break
		}
		if resp["tree"], err = printRequestTree(res.Envelope, env, p, wd); err != nil {
			return err
		}
		if resp["typed"], err = typedJSON(res.Envelope); err != nil {
			return err
		}
		if err := printRequestSubtree(res.Envelope, p, resp, wd); err != nil {
			return err
		}
	}
	if rc != "LY_SUCCESS" {
		resp["verdict"] = "invalid"
		return nil
	}
	top := res.Op
	for top.Parent() != nil {
		top = top.Parent()
	}
	resp["op_typed"], err = typedJSONFrom(res.Tree, top)
	return err
}

// runData is lyoracle.c op_data for datastore data: lyd_parse_data with the preset's flags and
// LYD_VALIDATE_MULTI_ERROR, then the printed tree (none for an empty tree: libyang's is NULL).
func runData(r Request, s *yang.Schema, resp map[string]any, exec executionContext) error {
	p := r.Params
	if err := checkRequest(r, s, p, exec, dataPreludeRules); err != nil {
		return err
	}
	oper, err := loadOperational(r, s, resp)
	if err != nil {
		return err
	}
	if err := checkOpInit(r, s, "data"); err != nil {
		return err
	}
	if resp["verdict"] == "operational-error" {
		return nil
	}
	dataType := str(p, "data_type", "data-operational")
	if typ, ok := opTypes[dataType]; ok {
		return runOp(r, s, resp, typ, oper)
	}
	if typ, ok := envelopeTypes[dataType]; ok {
		return runEnvelope(r, s, resp, typ)
	}
	if err := checkRequest(r, s, p, exec, dataRequestRules); err != nil {
		return err
	}
	// op_data line 1451 passes rpc to parse_one, but its datastore branch (1285-1287) ignores
	// rpc/rpc_file and keep_input.
	o, err := dataOptions(p, exec)
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
	in, present, err := requestInput(r, p, "data")
	if err != nil {
		return err
	}
	tree, diags, err := data.Parse(context.Background(), inputReader(in, present), f, s, o)
	rc, err := rcOf(err)
	if err != nil {
		return err
	}
	resp["verdict"], resp["rc"] = "valid", codeJSON(rc)
	if rc != "LY_SUCCESS" {
		resp["verdict"] = "invalid"
	}
	resp["diagnostics"] = append(list(resp["diagnostics"]), diagsJSON(diags, "data")...)
	resp["tree"] = nil
	if tree != nil && !empty(tree) {
		if resp["tree"], err = printRequestTree(tree, nil, p, wd); err != nil {
			return err
		}
		if resp["typed"], err = typedJSON(tree); err != nil {
			return err
		}
		return printRequestSubtree(tree, p, resp, wd)
	}
	return nil
}

// printTree is lyoracle.c print_tree with siblings: the JSON and XML printouts. libyang prints
// a NULL tree as "{}\n" in JSON and nothing in XML (a trim can free every node).
func printTree(tree *data.Tree, wd data.WD) (map[string]any, error) {
	out, _, err := printTreeLogged(tree, wd)
	return out, err
}

// printTreeLogged returns diagnostics retained by libyang's context until its next collect().
// print_tree ignores both printer return codes, but their logged items survive the call.
func printTreeLogged(tree *data.Tree, wd data.WD) (map[string]any, []yang.Diagnostic, error) {
	po := data.PrintOptions{WithDefaults: wd}
	var j, x strings.Builder
	if tree == nil || empty(tree) {
		return map[string]any{"json": "{}\n", "xml": ""}, nil, nil
	}
	var diags []yang.Diagnostic
	if err := tree.PrintJSON(&j, po); err != nil {
		if !printerDiagnostic(err) {
			return nil, nil, unsupported(err)
		}
		diags = append(diags, diagsOf(err)...)
	}
	if err := tree.PrintXML(&x, po); err != nil {
		if !printerDiagnostic(err) {
			return nil, nil, unsupported(err)
		}
		diags = append(diags, diagsOf(err)...)
	}
	return map[string]any{"json": j.String(), "xml": x.String()}, diags, nil
}

// print_tree ignores both lyd_print_mem return codes and does not collect printer
// diagnostics (lyoracle.c:1370-1387). A ValidationError can accompany completed
// output, notably an unresolved prefix in XML metadata. Keep the bytes it wrote.
func printerDiagnostic(err error) bool {
	var ve *data.ValidationError
	return errors.As(err, &ve)
}

// str is the string field k of p, def when absent.
func str(p map[string]any, k, def string) string {
	if v, ok := p[k].(string); ok {
		return v
	}
	return def
}

// dataOptions is lyoracle.c dparams_of: the data_type preset, unknown policy,
// parse_only, parse_options and validate_options (LYD_VALIDATE_MULTI_ERROR initially set).
func dataOptions(p map[string]any, exec executionContext) (data.ParseOptions, error) {
	o := data.ParseOptions{Validate: data.ValidateOptions{MultiError: true}}
	// An absent data_type selects data-operational (lyoracle.c:1215).
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
	// dparams_of always constructs both flag words (lyoracle.c:1208-1260), but callers select
	// which one reaches libyang: parse_one suppresses vopts for parse-only data (1273-1287).
	// Without LYD_VALIDATE_MULTI_ERROR, LY_DPARSER_ERR_GOTO stops at the first value error
	// (parser_internal.h:39-47), in both parser_json.c:1472-1475 and parser_xml.c:768-770.
	// The same flag word reaches the LYB parser, whose lyb_parse_node_leaf returns the first
	// term error directly (parser_lyb.c:1428-1430, 1624-1632).
	//
	// edit merge passes popts and zero vopts (2193-2229), and validate passes vopts only
	// (2160-2188). The dispatch-owned context is authoritative; a root "do" key is ignored.
	if (o.ParseOnly || o.StoreOnly || exec == execStepEdit) && exec != execStepValidate {
		o.Validate = data.ValidateOptions{}
		return o, nil
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

// inputReader preserves input_of's NULL versus non-NULL empty string. Callers
// that require an input reject absence during preflight; parse_one can also
// receive NULL (op_xpath), for which libyang returns LY_EINVAL.
func inputReader(in string, present bool) io.Reader {
	if !present {
		return nil
	}
	return strings.NewReader(in)
}

var errNoInput = errors.New("NULL input to parse_one")

// rcOf is the LY_ERR name of a data call's error; errors the port refuses on purpose (budgets,
// unsupported input) make the fixture unsupported, other errors are returned.
func rcOf(err error) (string, error) {
	var ve *data.ValidationError
	var ope *operationParentError
	switch {
	case err == nil:
		return "LY_SUCCESS", nil
	case errors.Is(err, errNoInput):
		return "LY_EINVAL", nil
	case errors.Is(err, yang.ErrBudget) || errors.Is(err, data.ErrUnsupported) || errors.Is(err, yang.ErrUnsupported) ||
		errors.Is(err, errors.ErrUnsupported):
		return "", unsupported(err)
	case errors.As(err, &ve):
		return ve.RC(), nil
	case errors.As(err, &ope):
		return "LY_EVALID", nil
	}
	return "", err
}

func empty(t *data.Tree) bool {
	for range t.Top() {
		return false
	}
	return true
}

// typedJSON is lyoracle.c typed_json, including any values and payload descendants.
func typedJSON(tree *data.Tree) ([]any, error) {
	return typedJSONFrom(tree, nil)
}

// typedJSONFrom is typed_json when its C tree argument points into a top-level sibling list:
// start and its following siblings are observed, but siblings before start are not.
func typedJSONFrom(tree *data.Tree, start *data.Node) ([]any, error) {
	out := []any{}
	started := start == nil
	for top := range tree.Top() {
		if top == start {
			started = true
		}
		if !started {
			continue
		}
		stack := []*data.Node{top}
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			f := n.Flags()
			o := map[string]any{"path": n.Path(), "schema": nil, "kind": "opaque", "value": nil, "opaque": nil, "any": nil,
				"flags": map[string]any{"default": f&data.FlagDefault != 0, "when_true": f&data.FlagWhenTrue != 0,
					"new": f&data.FlagNew != 0}}
			sn := n.Schema()
			if sn != nil {
				o["schema"], o["kind"] = sn.Path(), kindNames[sn.Kind()]
			}
			meta := []any{}
			if info, ok := n.OpaqueInfo(); ok {
				format := "json"
				if info.Format == data.FormatXML {
					format = "xml"
				}
				o["opaque"] = map[string]any{"name": n.Name(), "prefix": opt(info.Prefix), "format": format,
					"namespace": opt(info.Namespace), "module": opt(info.Module)}
				o["value"] = map[string]any{"canonical": n.Value(), "type": nil, "typedef": nil,
					"union_member": nil, "hints": info.Hints}
				for _, a := range n.Attrs() {
					meta = append(meta, map[string]any{"module": opt(a.Module), "name": a.Name, "value": a.Value})
				}
			} else if tp := n.ValueType(); tp != nil {
				v := valueTypeRef(tp)
				v["canonical"], v["union_member"] = n.Value(), nil
				if member, ok := n.UnionMember(); ok {
					v["union_member"] = unionMemberJSON(member)
				}
				o["value"] = v
			}
			for m := range n.Meta() {
				var module any
				if mod := m.Module(); mod != nil {
					module = mod.Name()
				}
				meta = append(meta, map[string]any{"module": module, "name": m.Name(), "value": m.Value()})
			}
			o["meta"] = meta
			if sn != nil && (sn.Kind() == yang.KindAnyData || sn.Kind() == yang.KindAnyXML) {
				value := map[string]any{"value_type": nil, "text": nil}
				if t := n.AnyValueType(); t != data.AnyValueNone {
					value["value_type"] = "string"
					if t == data.AnyValueDataTree {
						value["value_type"] = "datatree"
					}
					text, present, err := n.AnyValueString(data.FormatJSON)
					if err != nil {
						return nil, unsupported(err)
					}
					if present {
						value["text"] = text
					}
				}
				o["any"] = value
			}
			children := slices.Collect(n.ChildrenAny())
			out = append(out, o)
			for i := len(children) - 1; i >= 0; i-- {
				stack = append(stack, children[i])
			}
		}
	}
	return out, nil
}

// valueTypeRef is lyoracle.c add_type_ref, for a stored or declared member type.
func valueTypeRef(t *yang.Type) map[string]any {
	return map[string]any{"type": t.Base(), "typedef": opt(t.Typedef())}
}

// unionMemberJSON preserves the oracle's null index for an unmatched realtype.
func unionMemberJSON(member data.UnionMember) map[string]any {
	o := valueTypeRef(member.Type)
	o["index"] = nil
	if member.Index >= 0 {
		o["index"] = member.Index
	}
	o["realtype"] = valueTypeRef(member.RealType)
	return o
}

// runOp is lyoracle.c op_data for the operation data types (parse_one, load_oper): the
// operational tree parsed only (lyd_parse_data with LYD_PARSE_ONLY, unknown nodes dropped), the
// operation parsed (ParseOp; a reply with an rpc request is parsed into that request, its input
// removed), then, unless parse_only, ValidateOp against the operational tree and yanglint's
// check_operation_parent (a nested action or notification needs its parent in the operational
// tree). The tree is printed from its first node without siblings.
func runOp(r Request, s *yang.Schema, resp map[string]any, typ data.OpType, oper *data.Tree) error {
	p := r.Params
	if err := checkRequest(r, s, p, execData, dataRequestRules); err != nil {
		return err
	}
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
	diags := list(resp["diagnostics"])
	if p["rpc_parent"] != nil || typ == data.OpRPC && p["rpc"] != nil {
		// parse_one parses rpc_src and resolves rpc_parent against that request
		// before it attempts the unsupported RPC-with-request operation
		// (lyoracle.c:1281, 1298-1310). The same request root is also the
		// printed operation root for an RPC input.
		rpcIn, _, readErr := requestInput(r, p, "rpc")
		if readErr != nil {
			return readErr
		}
		f, formatErr := formatOf(p)
		if formatErr != nil {
			return formatErr
		}
		req, _, perr, parseErr := parseRequest(context.Background(), rpcIn, f, s, unknown, true, typ)
		if parseErr != nil {
			return parseErr
		}
		if perr == nil && req.Tree != nil {
			if path, present, _ := strField(p, "rpc_parent"); present {
				if n, findErr := req.Tree.Find(path); findErr != nil || n == nil {
					return requestError("rpc_parent %s not found", path)
				}
			}
		}
	}
	if p["rpc_parent"] != nil {
		return fmt.Errorf("%w: rpc input into an existing request parent is not exported (ParseOpOptions.Request is for replies only, U-0106)",
			ErrUnsupported)
	}
	in, present, err := requestInput(r, p, "data")
	if err != nil {
		return err
	}
	var res data.OpResult
	var d []yang.Diagnostic
	var perr error
	rpcIn, given, err := requestInput(r, p, "rpc")
	if err != nil {
		return err
	}
	if typ == data.OpRPC && given {
		return fmt.Errorf("%w: an rpc request with data_type %v (ParseOpOptions.Request is for replies only, deviations.md U-0106)",
			ErrUnsupported, p["data_type"])
	}
	keep, _ := p["keep_input"].(bool)
	if typ == data.OpReply && given {
		req, rd, rerr, err := parseRequest(ctx, rpcIn, f, s, unknown, keep, typ)
		diags = append(diags, diagsJSON(rd, "rpc")...)
		perr = rerr
		if err != nil {
			return err
		}
		if rerr == nil {
			res, d, perr = data.ParseOp(ctx, inputReader(in, present), f, s, typ,
				data.ParseOpOptions{Unknown: unknown, Request: req.Op})
			diags = append(diags, diagsJSON(d, "data")...)
			if perr != nil {
				if resp["request_typed"], err = typedJSON(req.Tree); err != nil { // what the failed reply left of the request
					return err
				}
			}
		}
	} else {
		res, d, perr = data.ParseOp(ctx, inputReader(in, present), f, s, typ, data.ParseOpOptions{Unknown: unknown})
		diags = append(diags, diagsJSON(d, "data")...)
	}
	rc, err := rcOf(perr)
	if err != nil {
		return err
	}
	if rc == "LY_SUCCESS" && !parseOnly {
		vd, verr := validateOperation(ctx, res.Tree, res.Op, typ, oper)
		diags = append(diags, diagsJSON(vd, "")...)
		if parent := operationParentDiagnostic(verr); parent != nil {
			diags = append(diags, parent)
		}
		if rc, err = rcOf(verr); err != nil {
			return err
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
	if resp["tree"], err = printRequestTree(res.Tree, first, p, wd); err != nil {
		return err
	}
	if resp["typed"], err = typedJSON(res.Tree); err != nil {
		return err
	}
	return printRequestSubtree(res.Tree, p, resp, wd)
}

// parseOne is lyoracle.c parse_one for an engine sequence input. Datastore data uses Parse;
// operation data uses ParseOp, with a reply optionally parsed into its separately parsed request.
// Every returned diagnostic already carries the phase in which parse_one collected it.
func parseOne(r Request, s *yang.Schema, p map[string]any, exec executionContext, key, phase string, oper *data.Tree) (*data.Tree, []yang.Diagnostic, error) {
	in, present, err := requestInput(r, p, key)
	if err != nil {
		return nil, nil, err
	}
	var rpc string
	hasRPC := false
	// Only op_data and the parse/diff sequence steps pass rpc_src to parse_one; op_xpath,
	// op_diff, insert, compare and diff_parse/diff_merge pass NULL (lyoracle.c:1451, 1529,
	// 1653, 2050, 2105, 2151, 2377, 2417, 2442). Never infer this from a root "do" key.
	if exec == execData || exec == execStepParse || exec == execStepDiff {
		rpc, hasRPC, err = requestInput(r, p, "rpc")
		if err != nil {
			return nil, nil, err
		}
	}
	c := &requestCheck{r: r, p: p, schema: s, inputs: map[string]bool{"rpc": hasRPC}, exec: exec}
	if err := requestParent(c); err != nil {
		return nil, nil, err
	}
	if !present && requestOperation(c) {
		// parse_one calls lyd_parse_op with NULL input (lyoracle.c:1284, 1317).
		// LY_CHECK_ARG_RET logs the generic argument error on this context for every
		// operation data type before returning LY_EINVAL.
		return nil, []yang.Diagnostic{{Phase: phase, Err: "LY_EINVAL", Code: "LYVE_SUCCESS", Msg: "Invalid argument in (lyd_parse_op())."}}, errNoInput
	}
	dataType := str(p, "data_type", "data-operational")
	typ, operation := opTypes[dataType]
	envelope := false
	if !operation {
		typ, operation = envelopeTypes[dataType]
		envelope = operation
	}
	if !operation {
		o, err := dataOptions(p, exec)
		if err != nil {
			return nil, nil, err
		}
		f, err := formatOf(p)
		if err != nil {
			return nil, nil, err
		}
		tree, diags, err := data.Parse(context.Background(), inputReader(in, present), f, s, o)
		return tree, phased(nil, diags, phase), err
	}

	unknown, ok := unknownPolicies[str(p, "unknown", "reject")]
	if !ok {
		return nil, nil, fmt.Errorf("%w: unknown %v", ErrUnsupported, p["unknown"])
	}
	// dparams_of already checked flag syntax. parse_one masks parse flags to
	// STRICT/OPAQ and never passes validate_options to lyd_validate_op.
	f, err := formatOf(p)
	if err != nil {
		return nil, nil, err
	}
	ctx := context.Background()
	keep, _ := p["keep_input"].(bool)
	if hasRPC && !parseOneConsumesRPC(typ) {
		// parse_one reads rpc_src before this point, but only these operation types use it as
		// a request parent (lyoracle.c:1291-1319). In particular, a dup source may carry rpc
		// for the ignored types because check_step's reply-only rule applies only to parse and
		// diff steps.
		hasRPC = false
	}
	if !hasRPC {
		res, diags, err := data.ParseOp(ctx, inputReader(in, present), f, s, typ, data.ParseOpOptions{Unknown: unknown})
		diags = phased(nil, diags, phase)
		if envelope {
			return res.Envelope, diags, err
		}
		if err == nil && p["parse_only"] != true {
			var vd []yang.Diagnostic
			vd, err = validateOperation(ctx, res.Tree, res.Op, typ, oper)
			diags = append(diags, vd...)
			if err != nil {
				return nil, diags, err
			}
		}
		return res.Tree, diags, err
	}

	req, rd, perr, err := parseRequest(ctx, rpc, f, s, unknown, keep, typ)
	diags := phased(nil, rd, "rpc")
	if err != nil {
		return nil, nil, err
	}
	if perr != nil {
		return nil, diags, perr
	}
	if typ == data.OpRPC {
		// parse_one has already parsed and collected rpc_src at this point. Parsing the main RPC
		// into that request remains outside ParseOp's public API (U-0106).
		return nil, diags, fmt.Errorf("%w: rpc input into an existing request is not exported (ParseOpOptions.Request is for replies only, U-0106)",
			ErrUnsupported)
	}
	res, parsed, err := data.ParseOp(ctx, inputReader(in, present), f, s, typ,
		data.ParseOpOptions{Unknown: unknown, Request: req.Op})
	diags = phased(diags, parsed, phase)
	if envelope {
		return res.Envelope, diags, err
	}
	if err != nil {
		return nil, diags, err
	}
	if p["parse_only"] != true {
		var vd []yang.Diagnostic
		vd, err = validateOperation(ctx, res.Tree, res.Op, typ, oper)
		diags = append(diags, vd...)
		if err != nil {
			return nil, diags, err
		}
	}
	return res.Tree, diags, nil
}

// parseOneConsumesRPC reports whether lyoracle.c parse_one parses a non-NULL rpc_src before the
// main input (lines 1291-1319). All other operation types and datastore data ignore rpc_src.
func parseOneConsumesRPC(typ data.OpType) bool {
	switch typ {
	case data.OpRPC, data.OpReply, data.OpReplyNetconf, data.OpRPCRestconf, data.OpReplyRestconf:
		return true
	default:
		return false
	}
}

// parseOnePendingPhase is the phase parse_one assigns to diagnostics already pending on ctx when
// it starts. An RPC-consuming type first collects ctx as "rpc"; every other path reaches the
// caller-supplied phase at done (lyoracle.c:1298, 1356).
func parseOnePendingPhase(p map[string]any, hasRPC bool, phase string) string {
	if !hasRPC {
		return phase
	}
	typ, ok := opTypes[str(p, "data_type", "data-operational")]
	if !ok {
		typ, ok = envelopeTypes[str(p, "data_type", "data-operational")]
	}
	if ok && parseOneConsumesRPC(typ) {
		return "rpc"
	}
	return phase
}

type operationParentError struct{ path string }

func (e *operationParentError) Error() string {
	return "operation parent not found in the operational tree"
}

// validateOperation is parse_one's post-parse operation validation followed by yanglint's
// check_operation_parent. Diagnostics already carry the phase in which the oracle collects them.
func validateOperation(ctx context.Context, tree *data.Tree, op *data.Node, typ data.OpType, oper *data.Tree) ([]yang.Diagnostic, error) {
	diags, err := tree.ValidateOp(ctx, typ, data.ValidateOpOptions{Operational: oper})
	diags = phased(nil, diags, "validate_op")
	if err != nil || op == nil || op.Parent() == nil {
		return diags, err
	}
	path := op.Parent().Path()
	found := false
	if oper != nil {
		nodes, _, ferr := oper.FindXPath(path, data.XPathOptions{})
		found = ferr == nil && len(nodes) > 0
	}
	if !found {
		return diags, &operationParentError{path: path}
	}
	return diags, nil
}

func operationParentDiagnostic(err error) map[string]any {
	var ope *operationParentError
	if !errors.As(err, &ope) {
		return nil
	}
	return map[string]any{"phase": "operation_parent", "level": "error", "code": codeJSON("LY_EVALID"),
		"source": "lyoracle", "data_path": ope.path, "msg": ope.Error()}
}

// parseRequest parses the request a reply is parsed into (lyoracle.c parse_one: the rpc parsed as
// RPC, its input dropped unless keep): perr is the parse's own error, err an engine refusal. Every
// place that builds a ParseOpOptions.Request goes through it. For a protocol message, ParseOp
// mirrors lyd_parse_op's native parent->schema argument check, so even an opaque request must
// reach it. For a YANG operation, Request binds a reply to a schema RPC/action only (design 07
// §6.1.1), so the engine retains the U-0106 refusal for an opaque request.
func parseRequest(ctx context.Context, rpc string, f data.Format, s *yang.Schema, unknown data.UnknownPolicy, keep bool,
	consumer data.OpType) (
	req data.OpResult, diags []yang.Diagnostic, perr, err error) {
	req, diags, perr = data.ParseOp(ctx, strings.NewReader(rpc), f, s, data.OpRPC, data.ParseOpOptions{Unknown: unknown})
	if perr != nil {
		return req, diags, perr, nil
	}
	if req.Op.Schema() == nil && !protocolRequestConsumer(consumer) {
		return req, diags, nil, fmt.Errorf("%w: a reply into an opaque request (ParseOpOptions.Request is for replies on a schema RPC or action, design 07 §6.1.1, U-0106)",
			ErrUnsupported)
	}
	if !keep {
		for _, c := range slices.Collect(req.Op.Children()) { // lyd_free_siblings(lyd_child(op))
			if err := c.Remove(); err != nil {
				return req, diags, nil, err
			}
		}
	}
	return req, diags, nil, nil
}

// protocolRequestConsumer reports the parse_one operation types whose native lyd_parse_op call
// has a parent->schema argument check, which ParseOp implements directly.
func protocolRequestConsumer(typ data.OpType) bool {
	switch typ {
	case data.OpReplyNetconf, data.OpRPCRestconf, data.OpReplyRestconf:
		return true
	default:
		return false
	}
}

// loadOperational is load_oper, shared by all data_prelude callers, including
// datastore and diff requests. str_of is checked even when input is absent;
// format semantics and parsing are only evaluated for a present input.
func loadOperational(r Request, s *yang.Schema, resp map[string]any) (*data.Tree, error) {
	p := r.Params
	in, present, err := requestInput(r, p, "operational")
	if err != nil {
		return nil, err
	}
	of, hasFormat, err := strField(p, "operational_format")
	if err != nil {
		return nil, err
	}
	resp["diagnostics"] = []any{}
	if !present {
		return nil, nil
	}
	if !hasFormat {
		of = str(p, "format", "json")
	}
	if err := requestEnum("format", "json", "json xml")(&requestCheck{p: map[string]any{"format": of}}); err != nil {
		return nil, err
	}
	f, err := formatOf(map[string]any{"format": of})
	if err != nil {
		return nil, err
	}
	tree, diags, perr := data.Parse(context.Background(), inputReader(in, present), f, s, data.ParseOptions{Unknown: data.Skip, ParseOnly: true})
	rc, err := rcOf(perr)
	if err != nil {
		return nil, err
	}
	resp["diagnostics"] = diagsJSON(diags, "operational")
	if rc != "LY_SUCCESS" {
		resp["verdict"] = "operational-error"
	}
	return tree, nil
}

func printRequestTree(tree *data.Tree, node *data.Node, p map[string]any, wd data.WD) (map[string]any, error) {
	po, flags, err := requestPrintOptions(p, wd)
	if err != nil {
		return nil, err
	}
	var j, x strings.Builder
	if node != nil {
		if err := node.PrintJSON(printopts.With(&j, flags), po); err != nil && !printerDiagnostic(err) {
			return nil, unsupported(err)
		}
		if err := node.PrintXML(printopts.With(&x, flags), po); err != nil && !printerDiagnostic(err) {
			return nil, unsupported(err)
		}
	} else {
		if err := tree.PrintJSON(printopts.With(&j, flags), po); err != nil && !printerDiagnostic(err) {
			return nil, unsupported(err)
		}
		if err := tree.PrintXML(printopts.With(&x, flags), po); err != nil && !printerDiagnostic(err) {
			return nil, unsupported(err)
		}
	}
	return map[string]any{"json": j.String(), "xml": x.String()}, nil
}

// requestPrintOptions is op_data:1442's wd_of OR flags_of(print_flags).
func requestPrintOptions(p map[string]any, wd data.WD) (data.PrintOptions, printopts.Flags, error) {
	po := data.PrintOptions{WithDefaults: wd}
	var flags printopts.Flags
	for _, f := range list(p["print_options"]) {
		option, ok := printFlags[fmt.Sprint(f)]
		if !ok {
			return po, flags, fmt.Errorf("%w: print option %v", ErrUnsupported, f)
		}
		if option.set != nil {
			option.set(&po)
		}
		flags |= option.internal
	}
	return po, flags, nil
}

func printRequestSubtree(tree *data.Tree, p map[string]any, resp map[string]any, wd data.WD) error {
	path, present, err := strField(p, "print_subtree")
	if err != nil || !present {
		return err
	}
	node, err := tree.Find(path)
	if err != nil || node == nil {
		return requestError("print_subtree %q not found", path)
	}
	resp["subtree"], err = printRequestTree(tree, node, p, wd)
	return err
}
