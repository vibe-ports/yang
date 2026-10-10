// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
)

// FieldSkipper is implemented by an engine that does not produce some response fields for an op
// (e.g. the compiled-schema printout); the comparison drops them from both sides and reports
// such a fixture as agreeing with skipped fields, never silently.
type FieldSkipper interface {
	SkippedFields(op string) []string
}

// Yang is the engine of this repository (package yang).
type Yang struct{}

type contextBuilder func(Request) (*yang.Schema, map[string]any, []map[string]any, string, error)

// SkippedFields implements FieldSkipper: the YANG printer of compiled modules is not ported;
// the typed dump of data trees compares path, schema, kind, flags and canonical value, but not
// the details package data does not export (value types, union members, metadata, anydata
// values, opaque names and hints).
func (Yang) SkippedFields(op string) []string {
	switch op {
	case "schema":
		return []string{"compiled"}
	case "data":
		return append(typedSkipped("typed"), typedSkipped("request_typed")...)
	case "diff":
		return typedSkipped("typed")
	case "sequence":
		return append(typedSkipped("typed"), typedSkipped("diff_typed")...)
	}
	return nil
}

// typedSkipped are the fields of a typed dump (in the response field f) the engine does not
// produce: see SkippedFields.
func typedSkipped(f string) []string {
	var out []string
	for _, k := range []string{"value.type", "value.typedef", "value.union_member", "value.hints", "meta", "any", "opaque"} {
		out = append(out, f+"."+k)
	}
	return out
}

// ctxOptions are the oracle's context_options this engine supports.
var ctxOptions = map[string]func(*yang.Options){
	"all_implemented":      func(o *yang.Options) { o.AllImplemented = true },
	"no_yanglibrary":       func(o *yang.Options) { o.NoYangLibrary = true },
	"enable_imp_features":  func(o *yang.Options) { o.EnableImportFeatures = true },
	"compile_obsolete":     func(o *yang.Options) { o.CompileObsolete = true },
	"ref_implemented":      func(o *yang.Options) { o.RefImplemented = true },
	"leafref_extended":     func(o *yang.Options) { o.LeafrefExtended = true },
	"leafref_linking":      func(o *yang.Options) { o.LeafrefLinking = true },
	"builtin_plugins_only": func(o *yang.Options) { o.BuiltinPluginsOnly = true },
	"pattern_compat":       func(o *yang.Options) { o.PatternCompat = true },
}

// ctxUnsupported are the oracle's context_options this engine refuses, with the reason.
var ctxUnsupported = map[string]string{}

// Run implements Engine for ops "schema" (lyoracle.c op_schema/build_ctx/dump_schema), "data"
// (op_data, datastore data types), "diff" (op_diff), "xpath" (op_xpath), "atoms" (op_atoms) and
// "sequence" (op_sequence over the public data API).
func (Yang) Run(r Request) (Response, error) {
	if r.buildContext == nil {
		r.buildContext = buildSchemaContext
	}
	op, present, err := strField(r.Params, "op")
	if err != nil {
		return nil, err
	}
	base, hasBase, err := strField(r.Params, "base_dir")
	if err != nil {
		return nil, err
	}
	if hasBase {
		path := base
		if !filepath.IsAbs(path) {
			path = filepath.Join(r.BaseDir, path)
		}
		info, statErr := os.Stat(path)
		if base == "" || statErr != nil || !info.IsDir() {
			return nil, requestError("cannot chdir to %s", base)
		}
		r.BaseDir = path
	}
	if !present {
		return nil, requestError("missing op")
	}
	if op != "schema" && op != "data" && op != "sequence" && op != "diff" && op != "xpath" && op != "atoms" {
		return nil, requestError("unknown op %s", op)
	}
	schema, resp, mods, verdict, err := r.buildContext(r)
	if err != nil {
		return nil, err
	}
	resp["op"] = op
	resp["modules"] = mods
	if op != "schema" {
		if verdict != "valid" {
			if err := checkOpInit(r, schema, op); err != nil {
				return nil, err
			}
			resp["verdict"] = "schema-error"
			if op != "sequence" {
				resp["diagnostics"] = []any{}
			}
			b, marshalErr := json.Marshal(resp)
			if marshalErr != nil {
				return nil, marshalErr
			}
			return ParseResponse(b)
		}
		run := map[string]func(Request, *yang.Schema, map[string]any, executionContext) error{"data": runData, "sequence": runSequence,
			"diff": runDiff, "xpath": runXPath, "atoms": runAtoms}[op]
		if err := run(r, schema, resp, rootExecutionContexts[op]); err != nil {
			return nil, err
		}
	} else {
		s := schema
		for _, m := range mods {
			if m["accepted"] == true {
				if mod := s.Implemented(m["name"].(string)); mod != nil {
					dumpSchema(m, s, mod)
				}
			}
		}
		resp["verdict"] = verdict
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, err
	}
	return ParseResponse(b) // json.Number like the goldens
}

func buildSchemaContext(r Request) (*yang.Schema, map[string]any, []map[string]any, string, error) {
	ctx, resp, mods, verdict, err := buildContext(r)
	if ctx == nil {
		return nil, resp, mods, verdict, err
	}
	return ctx.Schema(), resp, mods, verdict, err
}

// buildContext is lyoracle.c build_ctx: the context, the response with its module entries and
// "valid" unless a module was rejected.
func buildContext(r Request) (*yang.Context, map[string]any, []map[string]any, string, error) {
	// Prelude ordering follows lyoracle.c:1094-1134. Context flags are rejected at
	// 1094 before ly_ctx_new (1098); search directories are checked at 1100-1104
	// before any requested module is loaded; and each module's name/revision plus
	// missing-name check happen at 1113-1122 before ly_ctx_load_module (1134).
	// Features add no request-level rejection (1124-1128): non-string children
	// terminate the list, as reproduced below.
	if err := requestFlags("context_options", "all_implemented ref_implemented no_yanglibrary enable_imp_features compile_obsolete leafref_extended leafref_linking builtin_plugins_only pattern_compat")(&requestCheck{p: r.Params}); err != nil {
		return nil, nil, nil, "", err
	}
	var opts yang.Options
	for _, o := range list(r.Params["context_options"]) {
		set, ok := ctxOptions[fmt.Sprint(o)]
		if !ok {
			return nil, nil, nil, "", fmt.Errorf("%w: context option %v %s", ErrUnsupported, o, ctxUnsupported[fmt.Sprint(o)])
		}
		set(&opts)
	}
	var dirs []fs.FS
	for _, d := range requestChildren(r.Params["searchdirs"]) {
		dir, ok := d.(string)
		if !ok {
			return nil, nil, nil, "", requestError("bad searchdir ?")
		}
		path := dir
		if !filepath.IsAbs(path) {
			path = filepath.Join(r.BaseDir, path)
		}
		info, err := os.Stat(path)
		if dir == "" || err != nil || !info.IsDir() {
			return nil, nil, nil, "", requestError("bad searchdir %s", dir)
		}
		dirs = append(dirs, os.DirFS(path))
	}
	ctx, cdiags, err := yang.NewContext(opts, dirs...)
	if err != nil {
		return nil, nil, nil, "", unsupported(err)
	}
	resp := map[string]any{"protocol": 2, "context_diagnostics": diagsJSON(cdiags, "context")}
	verdict := "valid"
	mods := []map[string]any{}
	for _, x := range requestChildren(r.Params["modules"]) {
		req, _ := x.(map[string]any)
		name, hasName, err := strField(req, "name")
		if err != nil {
			return nil, nil, nil, "", err
		}
		rev, hasRev, err := strField(req, "revision")
		if err != nil {
			return nil, nil, nil, "", err
		}
		if !hasName {
			return nil, nil, nil, "", requestError("module without name")
		}
		var feats []string
		if fa, ok := req["features"]; ok {
			feats = []string{}
			for _, f := range requestChildren(fa) {
				value, ok := f.(string)
				if !ok {
					break
				} // C valuestring is NULL: terminates features
				feats = append(feats, value)
			}
		}
		m := map[string]any{"name": name}
		// Load's empty revision means latest; the oracle distinguishes a non-NULL
		// empty revision at ly_ctx_load_module (lyoracle.c:1134). Probe that load in
		// a disposable context so a rejected entry cannot load a new module or
		// change features on one implemented by an earlier entry.
		var diags []yang.Diagnostic
		if hasRev && rev == "" {
			diags, err = probeEmptyRevision(opts, dirs, name)
		} else {
			diags, err = ctx.Load(name, rev, feats)
		}
		m["diagnostics"] = diagsJSON(diags, "")
		switch {
		case errors.Is(err, ErrUnsupported):
			return nil, nil, nil, "", err
		case errors.Is(err, yang.ErrUnsupported) || errors.Is(err, yang.ErrBudget):
			return nil, nil, nil, "", unsupported(err)
		case err != nil:
			verdict = "invalid"
			m["accepted"] = false
			m["phase"] = "parse"
			if len(diags) > 0 && diags[len(diags)-1].Phase == "compile" {
				m["phase"] = "compile"
				m["rc"] = codeJSON(err.Error())
			}
		default:
			m["accepted"] = true
			m["revision"] = nil
			if mod := ctx.Schema().Implemented(name); mod != nil && mod.Revision() != "" {
				m["revision"] = mod.Revision()
			}
		}
		mods = append(mods, m)
	}
	return ctx, resp, mods, verdict, nil
}

// probeEmptyRevision loads name with the latest revision in a disposable context, so the request
// context is untouched. When it loads without any diagnostic, the oracle's explicit "" revision
// is libyang's mismatch with the parsed module's revision (lysp_load_module_check), reported in
// the parse phase. When the load logs any diagnostic (a warning included) or fails in any phase
// (parse, imports, compile), libyang would still check the
// revision right after parsing and before imports and compilation; the Load API has no
// parse-only step, so that ordering is not emulated and the request is unsupported (U-0110,
// class empty-revision-load-failure; the parse-phase check in the loader is tracked in #254).
func probeEmptyRevision(opts yang.Options, dirs []fs.FS, name string) ([]yang.Diagnostic, error) {
	scratch, _, err := yang.NewContext(opts, dirs...)
	if err != nil {
		return nil, err
	}
	// Revision validation happens while parsing the module, before requested features are
	// applied. Do not let a feature error replace the empty-revision diagnostic.
	diags, err := scratch.Load(name, "", nil)
	switch {
	case errors.Is(err, yang.ErrUnsupported) || errors.Is(err, yang.ErrBudget):
		return diags, err
	case err != nil:
		return nil, fmt.Errorf("%w: empty revision with a failing module load: %s "+
			"(libyang checks the revision before imports and compile, U-0110)", ErrUnsupported, err.Error())
	case len(diags) > 0:
		// libyang rejects the revision in lysp_load_module_data_check, before imports and
		// compilation log anything; which of the probe's diagnostics (warnings included) come
		// before that check cannot be told without a parse-only load.
		return nil, fmt.Errorf("%w: empty revision with a failing module load: the load logged %d "+
			"diagnostics (libyang checks the revision before imports and compile, U-0110)", ErrUnsupported, len(diags))
	}
	actual := "none"
	if mod := scratch.Schema().Implemented(name); mod != nil && mod.Revision() != "" {
		actual = mod.Revision()
	}
	diags = append([]yang.Diagnostic{},
		yang.Diagnostic{Phase: "parse", Err: "LY_EINVAL", Code: "LYVE_SUCCESS", Msg: fmt.Sprintf(`Module %q parsed with the wrong revision (%q instead "").`, name, actual)},
		yang.Diagnostic{Phase: "parse", Err: "LY_EOTHER", Code: "LYVE_SUCCESS", Msg: fmt.Sprintf("Parsing module %q failed.", name)})
	return diags, fmt.Errorf("module revision mismatch")
}

func unsupported(err error) error { return fmt.Errorf("%w: %s", ErrUnsupported, err.Error()) }

func list(v any) []any { l, _ := v.([]any); return l }

// errNums are libyang's LY_ERR values (log.h); LY_EPLUGIN is or-ed in.
var errNums = map[string]int{"LY_SUCCESS": 0, "LY_EMEM": 1, "LY_ESYS": 2, "LY_EINVAL": 3, "LY_EEXIST": 4,
	"LY_ENOTFOUND": 5, "LY_EINT": 6, "LY_EVALID": 7, "LY_EDENIED": 8, "LY_EINCOMPLETE": 9, "LY_ERECOMPILE": 10,
	"LY_ENOT": 11, "LY_EOTHER": 12}

var vecodes = []string{"LYVE_SUCCESS", "LYVE_SYNTAX", "LYVE_SYNTAX_YANG", "LYVE_SYNTAX_YIN", "LYVE_REFERENCE",
	"LYVE_XPATH", "LYVE_SEMANTICS", "LYVE_SYNTAX_XML", "LYVE_SYNTAX_JSON", "LYVE_DATA", "LYVE_OTHER"}

func codeJSON(name string) map[string]any {
	n := 0
	for _, part := range strings.Split(name, "|") {
		if part == "LY_EPLUGIN" {
			n |= 128
		} else {
			n |= errNums[part]
		}
	}
	return map[string]any{"err": n, "name": name}
}

// diagsJSON renders diagnostics as lyoracle.c collect; phase "" keeps each one's own.
func diagsJSON(ds []yang.Diagnostic, phase string) []any {
	out := []any{}
	for _, d := range ds {
		p, level := d.Phase, "error"
		if phase != "" {
			p = phase
		}
		if d.Warning {
			level = "warning"
		}
		out = append(out, map[string]any{"phase": p, "level": level, "code": codeJSON(d.Err),
			"vecode": slices.Index(vecodes, d.Code), "vecode_name": d.Code, "data_path": opt(d.DataPath),
			"schema_path": opt(d.SchemaPath), "apptag": opt(d.AppTag), "line": d.Line, "msg": d.Msg})
	}
	return out
}

func opt(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// dumpSchema is lyoracle.c dump_schema: schema_tree, identities, features and ext_trees of mod.
func dumpSchema(m map[string]any, s *yang.Schema, mod *yang.Module) {
	var tree []any
	var dfs func(n *yang.SchemaNode)
	dfs = func(n *yang.SchemaNode) { // lysc_module_dfs_full: node, actions, notifications, children
		tree = append(tree, nodeJSON(n))
		for c := range n.Actions() {
			dfs(c)
		}
		for c := range n.Notifications() {
			dfs(c)
		}
		for c := range n.Children() {
			dfs(c)
		}
	}
	tree = []any{}
	for n := range mod.Top() {
		dfs(n)
	}
	m["schema_tree"] = tree
	// ext_trees: the schema trees of the module's extension instances, omitted when none has one
	var extTrees []any
	for e := range mod.Extensions() {
		tree = []any{}
		for n := range e.Tree() {
			empty := true
			for range n.Children() {
				empty = false
				break
			}
			if e.Name() == "structure" && e.Module() == "ietf-yang-structure-ext" && empty {
				continue // the oracle walks the data-node storage: an empty structure has none
			}
			dfs(n)
		}
		if len(tree) > 0 {
			extTrees = append(extTrees, map[string]any{"module": e.Module(), "name": e.Name(),
				"argument": opt(e.Argument()), "schema_tree": tree})
		}
	}
	if extTrees != nil {
		m["ext_trees"] = extTrees
	}
	ids := []any{}
	for id := range mod.Identities() {
		bases, derived := []any{}, []any{}
		for o := range s.Modules() { // bases by scanning the context, as the oracle
			for b := range o.Identities() {
				for d := range b.Derived() {
					if d.Name() == id.Name() && d.Module().Name() == mod.Name() && d.Module().Revision() == mod.Revision() {
						bases = append(bases, identName(b))
					}
				}
			}
		}
		for d := range id.Derived() {
			derived = append(derived, identName(d))
		}
		ids = append(ids, map[string]any{"name": identName(id), "bases": bases, "derived": derived})
	}
	m["identities"] = ids
	feats := []any{}
	for f, on := range mod.Features() {
		feats = append(feats, map[string]any{"name": f, "enabled": on})
	}
	m["features"] = feats
}

func identName(i *yang.Identity) string { return i.Module().Name() + ":" + i.Name() }

var kindNames = map[yang.Kind]string{yang.KindContainer: "container", yang.KindChoice: "choice",
	yang.KindCase: "case", yang.KindLeaf: "leaf", yang.KindLeafList: "leaflist", yang.KindList: "list",
	yang.KindAnyData: "anydata", yang.KindAnyXML: "anyxml", yang.KindRPC: "rpc", yang.KindAction: "action",
	yang.KindInput: "input", yang.KindOutput: "output", yang.KindNotification: "notif"}

// nodeJSON is lyoracle.c snode_cb.
func nodeJSON(n *yang.SchemaNode) map[string]any {
	k := n.Kind()
	o := map[string]any{"path": n.Path(), "nodetype": kindNames[k], "module": n.Module().Name(),
		"status": n.Status().String(), "config": nil, "mandatory": nil, "presence": nil, "ordered_by": nil,
		"keys": nil, "min_elements": nil, "max_elements": nil, "defaults": nil, "type": nil, "when": nil,
		"musts": nil, "extensions": nil}
	inOp := false
	for p := n; p != nil; p = p.Parent() {
		switch p.Kind() {
		case yang.KindRPC, yang.KindAction, yang.KindNotification, yang.KindInput, yang.KindOutput:
			inOp = true
		}
	}
	if !inOp && n.HasConfig() {
		o["config"] = n.Config()
	}
	if !n.HasStatus() {
		o["status"] = nil
	}
	switch k {
	case yang.KindLeaf, yang.KindLeafList, yang.KindList, yang.KindChoice, yang.KindAnyData, yang.KindAnyXML,
		yang.KindContainer:
		o["mandatory"] = n.Mandatory()
	}
	if k == yang.KindContainer {
		o["presence"] = n.Presence()
	}
	if k == yang.KindList || k == yang.KindLeafList {
		o["ordered_by"] = "system"
		if n.UserOrdered() {
			o["ordered_by"] = "user"
		}
		o["min_elements"] = n.MinElements()
		if limit, ok := n.MaxElements(); ok {
			o["max_elements"] = limit
		}
	}
	if k == yang.KindList {
		keys := []any{}
		for c := range n.Children() { // lyoracle.c: the leading children that lysc_is_key
			if !c.IsKey() {
				break
			}
			keys = append(keys, c.Name())
		}
		o["keys"] = keys
	}
	var dflts []any
	for d := range n.Defaults() {
		dflts = append(dflts, d)
	}
	if c := n.DefaultCaseName(); c != "" { // also a default case removed as disabled (D-0070)
		dflts = []any{c}
	}
	if dflts != nil {
		o["defaults"] = dflts
	}
	if t := n.Type(); t != nil {
		o["type"] = leafTypeJSON(n, t)
	}
	var whens, musts []any
	for w := range n.Whens() {
		var ctx any
		if c := w.ContextNode(); c != nil {
			ctx = c.Path()
		}
		whens = append(whens, map[string]any{"expr": w.Expr(), "context": ctx, "module": opt(w.Module())})
	}
	for mu := range n.Musts() {
		musts = append(musts, map[string]any{"expr": mu.Expr(), "apptag": opt(mu.ErrorAppTag()),
			"message": opt(mu.ErrorMessage())})
	}
	if whens != nil {
		o["when"] = whens
	}
	if musts != nil {
		o["musts"] = musts
	}
	if n.HasExtensionList() { // [] when every instance was dropped, as the oracle dumps it
		exts := []any{}
		for e := range n.Extensions() {
			exts = append(exts, map[string]any{"module": e.Module(), "name": e.Name(), "argument": opt(e.Argument())})
		}
		o["extensions"] = exts
	}
	return o
}

// leafTypeJSON is lyoracle.c leaf_type_json: union leafref members get their targets only when
// every one resolves (lysc_node_lref_targets skips the others, so the counts would differ).
func leafTypeJSON(n *yang.SchemaNode, t *yang.Type) map[string]any {
	var targets []any
	all := true
	for _, tg := range n.LeafrefTargets() {
		if tg == nil {
			targets = append(targets, nil)
			all = false
		} else {
			targets = append(targets, tg.Path())
		}
	}
	if t.Base() == "leafref" {
		return typeJSON(t, targets[0], nil)
	}
	if !all {
		targets = make([]any, len(targets))
	}
	return typeJSON(t, nil, targets)
}

// typeJSON is lyoracle.c type_json.
func typeJSON(t *yang.Type, target any, utargets []any) map[string]any {
	o := map[string]any{"base": t.Base(), "typedefs": nil, "range": nil, "length": nil, "patterns": nil,
		"fraction_digits": nil, "enums": nil, "bits": nil, "bases": nil, "leafref": nil, "union": nil}
	if td := t.Typedef(); td != "" {
		o["typedefs"] = []any{td}
	}
	bounds := func(parts func(func(string, string) bool), ok bool) any {
		if !ok {
			return nil
		}
		var b strings.Builder
		for lo, hi := range parts {
			if b.Len() > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(lo)
			if lo != hi {
				b.WriteString(".." + hi)
			}
		}
		return b.String()
	}
	switch t.Base() {
	case "int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64", "decimal64":
		o["range"] = bounds(t.Range())
	case "string", "binary":
		o["length"] = bounds(t.Length())
	}
	if t.Base() == "string" {
		var pats []any
		for e, inv := range t.Patterns() {
			pats = append(pats, map[string]any{"expr": e, "invert": inv})
		}
		if pats != nil {
			o["patterns"] = pats
		}
	}
	switch t.Base() {
	case "decimal64":
		o["fraction_digits"] = t.FractionDigits()
	case "enumeration":
		items := []any{}
		for name, v := range t.Enums() {
			items = append(items, map[string]any{"name": name, "value": v})
		}
		o["enums"] = items
	case "bits":
		items := []any{}
		for name, p := range t.Bits() {
			items = append(items, map[string]any{"name": name, "position": p})
		}
		o["bits"] = items
	case "identityref":
		bases := []any{}
		for b := range t.Bases() {
			bases = append(bases, identName(b))
		}
		o["bases"] = bases
	case "leafref":
		o["leafref"] = map[string]any{"path": t.LeafrefPath(), "require_instance": t.RequireInstance(), "target": target}
	case "union":
		members := []any{}
		k := 0
		for mt := range t.Members() {
			var tg any
			if mt.Base() == "leafref" && utargets != nil {
				tg = utargets[k]
				k++
			}
			members = append(members, typeJSON(mt, tg, nil))
		}
		o["union"] = members
	}
	return o
}

// requestChildren follows cJSON_ArrayForEach for context inputs: arrays and
// object values have children; null and scalar JSON values do not. JSON object
// serialization is sorted by key, the same order in which the oracle sees it.
func requestChildren(v any) []any {
	if a, ok := v.([]any); ok {
		return a
	}
	if m, ok := v.(map[string]any); ok {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		out := make([]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, m[k])
		}
		return out
	}
	return nil
}
