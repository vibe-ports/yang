// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// seqKeys are the fields of each sequence step kind runSequence handles (lyoracle.c check_step).
var seqKeys = map[string]map[string]bool{
	"parse": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options", "rpc", "keep_input", "parent"),
	"validate": set("do", "data_type", "validate_options", "module", "final_only"),
	"edit": set("do", "set", "delete", "new_meta", "free_meta", "insert_term", "insert_inner", "insert_list", "insert_list2", "insert_opaq", "merge",
		"merge_file", "format", "data_type", "unknown", "parse_options"),
	"dup":         set("do", "node", "parent", "options", "siblings", "target"),
	"link":        set("do"),
	"links":       set("do"),
	"change_term": set("do", "node", "value", "canon"),
	"insert": set("do", "node", "parent", "format", "data_type", "data", "data_file", "unknown", "parse_only",
		"parse_options", "validate_options"),
	"dump": set("do", "with_defaults"),
	"compare": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options", "first", "second", "options"),
	"diff": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options", "rpc", "keep_input", "node", "data_node", "single", "options", "merge", "merge_options", "target"),
	"diff_parse":   set("do", "format", "data_type", "data", "data_file", "unknown", "parse_options"),
	"diff_merge":   set("do", "format", "data_type", "data", "data_file", "unknown", "parse_options", "options", "module", "src_node", "parent"),
	"diff_apply":   set("do", "module"),
	"diff_reverse": set("do"),
	"trim":         set("do", "xpath", "vars"),
}

var seqContextKeys = set("searchdirs", "modules", "context_options")

// seqUnsupported are the oracle's step kinds and edits package data has no public API for.
var seqUnsupported = map[string]string{
	"insert_opaq": "(lyd_new_opaq / lyd_new_opaq2 are exported in M5 with the envelopes, U-0107)",
}

// seqOptionUnsupported is the reason a step's options cannot be expressed with the public API,
// "" when they can.
func seqOptionUnsupported(what string, st map[string]any) string {
	switch what {
	case "edit":
		for _, k := range []string{"insert_term", "insert_list"} {
			x, _ := st[k].(map[string]any)
			for _, o := range list(x["options"]) {
				if o != "output" {
					return fmt.Sprintf("edit %s option %v (LYD_NEW_VAL_STORE_ONLY and _CANON are not exported, U-0108)", k, o)
				}
			}
		}
	case "change_term":
		if st["canon"] == true {
			return "change_term canon (lyd_change_term_canon is not exported, U-0108)"
		}
	case "dup":
		for _, o := range list(st["options"]) {
			if o == "no_lyds" {
				return "dup option no_lyds (LYD_DUP_NO_LYDS is not exported, U-0105)"
			}
		}
	}
	return ""
}

func set(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// runSequence is lyoracle.c op_sequence over the public data API: one retained tree, every step's
// rc, diagnostics and typed dump, the steps after the first failure skipped. Every step is
// request-checked first; capability checks run only for steps that execute.
func runSequence(r Request, s *yang.Schema, resp map[string]any, exec executionContext) error {
	if err := checkRequest(r, s, r.Params, exec, sequenceRequestRules); err != nil {
		return err
	}
	var steps []map[string]any
	// check_step checks every step against the ORIGINAL context before execution.
	for _, x := range list(r.Params["steps"]) {
		st, _ := x.(map[string]any)
		if err := checkRequest(r, s, st, exec, stepEntryRules); err != nil {
			return err
		}
		stepExec := stepExecutionContexts[str(st, "do", "")]
		if err := checkRequest(r, s, st, stepExec, stepRequestRules[str(st, "do", "")]); err != nil {
			return err
		}
		if _, ok := seqKeys[str(st, "do", "")]; !ok {
			return requestError("unknown step %s", st["do"])
		}
		steps = append(steps, st)
	}
	var tree, diff *data.Tree // the retained tree and the diff register
	out := []any{}
	rc, failed := "LY_SUCCESS", any(nil)
	cur := s // the retained tree's snapshot: a dup step with a target moves the tree to another
	for i, st := range steps {
		if cur != s && (tree == nil || empty(tree)) {
			// the oracle's tree is NULL again and its steps use the request's context (lyoracle.c
			// op_sequence: sctx = tree ? LYD_CTX(tree) : ctx)
			tree, cur = nil, s
		}
		s := cur
		what := str(st, "do", "")
		so := map[string]any{"do": what}
		out = append(out, so)
		if rc != "LY_SUCCESS" {
			so["skipped"] = true
			continue
		}
		var (
			diags []yang.Diagnostic
			err   error
		)
		stepExec := stepExecutionContexts[what]
		if err := checkRequest(r, s, st, stepExec, stepExecutionRules[what]); err != nil {
			return err
		}
		// Execution-time selectors precede the API call, including for opaque
		// construction which the public API does not expose yet.
		if what == "edit" {
			if err := checkInsertSelectors(s, tree, st); err != nil {
				return err
			}
		}
		// A skipped step never needs an API capability (lyoracle.c:2502-2506).
		if why := seqOptionUnsupported(what, st); why != "" {
			return fmt.Errorf("%w: %s", ErrUnsupported, why)
		}
		for _, k := range slices.Sorted(maps.Keys(st)) { // the first refusal must not depend on map order
			if why, bad := seqUnsupported[k]; bad && what == "edit" {
				return fmt.Errorf("%w: edit %s %s", ErrUnsupported, k, why)
			}
		}
		if what == "validate" && st["final_only"] == true {
			return fmt.Errorf("%w: lyd_validate_module_final is not exported (split-validation speed path), U-0106", ErrUnsupported)
		}
		phase := what
		switch what {
		case "parse":
			phase = "" // parseOne distinguishes the request's "rpc" phase from "parse"
			tree, diags, err = stepParse(r, s, st, stepExec, tree)
		case "validate":
			tree, diags, err = stepValidate(s, st, tree, so)
		case "edit":
			tree, diags, rc, err = stepEdit(r, s, st, tree)
		case "dup":
			phase = "edit"
			tree, cur, diags, err = stepDup(r, s, st, tree)
		case "link":
			if tree == nil || empty(tree) {
				rc = "LY_EINVAL" // LY_CHECK_ARG_RET(NULL, tree): logged without a context
				break
			}
			if diags, err = tree.LinkLeafrefs(context.Background()); err != nil {
				diags = diagsOf(err)
			}
		case "links":
			so["leafref_links"] = linksJSON(tree)
		case "change_term":
			phase = "edit"
			diags, err = stepChange(st, tree, so)
		case "insert":
			tree, diags, phase, err = stepMove(r, s, st, tree)
		case "compare":
			phase = "parse"
			diags, err = stepCompare(r, s, st, tree, so)
		case "diff":
			phase = "" // the diagnostics carry theirs
			diags, err = stepDiff(r, s, st, tree, &diff, so)
		case "diff_parse":
			phase = "parse"
			var d *data.Tree
			if d, diags, err = parseDiff(r, s, st, stepExec); err == nil {
				diff = d
				if empty(diff) {
					diff = nil
				}
			}
		case "diff_merge":
			phase = ""
			diags, err = stepDiffMerge(r, s, st, &diff)
		case "diff_apply":
			phase = "diff"
			if tree == nil {
				if tree, err = emptyTree(s); err != nil {
					return err
				}
			}
			err = tree.ApplyDiff(diff, data.ApplyDiffOptions{Module: str(st, "module", "")})
			diags = diagsOf(err)
		case "trim":
			phase = "xpath"
			var o data.XPathOptions
			if o.Vars, err = xpathVars(st); err != nil {
				return err
			}
			if tree == nil {
				break // lyd_trim_xpath of a NULL tree does nothing
			}
			diags, err = tree.TrimXPath(str(st, "xpath", ""), o)
		case "diff_reverse":
			phase = "diff"
			diff, err = diff.ReverseDiff()
			diags = diagsOf(err)
		case "dump":
			wd, werr := wdOf(st)
			if werr != nil {
				return werr
			}
			if so["tree"], err = printTree(tree, wd); err != nil {
				return err
			}
		}
		if err != nil {
			if rc, err = rcOf(err); err != nil {
				return err
			}
		}
		so["diagnostics"] = diagsJSON(diags, phase)
		// an error-level item means failure even if the call returned success (lyoracle.c)
		for _, d := range diags {
			if rc == "LY_SUCCESS" && !d.Warning {
				rc = d.Err
			}
		}
		so["rc"] = codeJSON(rc)
		so["typed"] = []any{}
		if tree != nil {
			so["typed"] = typedJSON(tree)
		}
		if strings.HasPrefix(what, "diff") {
			if err := diffOut(diff, so); err != nil {
				return err
			}
		}
		if rc != "LY_SUCCESS" {
			failed = i
		}
	}
	resp["steps"] = out
	resp["verdict"], resp["rc"], resp["failed_step"] = "valid", codeJSON(rc), failed
	if rc != "LY_SUCCESS" {
		resp["verdict"] = "invalid"
	}
	return nil
}

// diagsOf is the diagnostics a data call logged.
func diagsOf(err error) []yang.Diagnostic {
	if ve, ok := err.(*data.ValidationError); ok { //nolint:errorlint // data returns it unwrapped
		return ve.Diags
	}
	return nil
}

// emptyTree is the NULL tree of the oracle's sequence, which lyd_new_path and lyd_validate_all
// fill.
func emptyTree(s *yang.Schema) (*data.Tree, error) { return data.NewTree(s), nil }

// stepCompare is step_compare: Node.Equal of the node at first in the tree and the node at second
// in a second tree parsed like a parse step (omitted: the first top-level node).
func stepCompare(r Request, s *yang.Schema, st map[string]any, tree *data.Tree, so map[string]any) (
	[]yang.Diagnostic, error) {
	var o data.CompareOptions
	for _, x := range list(st["options"]) {
		switch x {
		case "full_recursion":
			o.FullRecursion = true
		case "defaults":
			o.Defaults = true
		case "opaq":
			o.Opaque = true
		default:
			return nil, fmt.Errorf("%w: compare option %v", ErrUnsupported, x)
		}
	}
	other, diags, err := stepParse(r, s, st, execStepCompare, nil)
	if err != nil || other == nil {
		return diags, err
	}
	n1, err := selectedNode(tree, st, "first", "compare first")
	if err != nil {
		return nil, err
	}
	n2, err := selectedNode(other, st, "second", "compare second")
	if err != nil {
		return nil, err
	}
	so["compare"] = codeJSON("LY_ENOT")
	if n1.Equal(n2, o) {
		so["compare"] = codeJSON("LY_SUCCESS")
	}
	return diags, nil
}

// selectedNode distinguishes an omitted selector from an explicitly empty path.
func selectedNode(tree *data.Tree, p map[string]any, key, label string) (*data.Node, error) {
	if path, present, _ := strField(p, key); present {
		return findNode(tree, path, label)
	}
	return topNode(tree), nil
}

// stepParse is step_parse: the parsed tree replaces the retained one only on success.
func stepParse(r Request, s *yang.Schema, st map[string]any, exec executionContext, tree *data.Tree) (*data.Tree, []yang.Diagnostic, error) {
	parentPath, hasParent, err := strField(st, "parent")
	if err != nil {
		return tree, nil, err
	}
	if hasParent && exec == execStepParse {
		typ, ok := opTypes[str(st, "data_type", "")]
		if !ok {
			return tree, nil, requestError("parse parent requires data_type rpc, reply or notif")
		}
		_, hasRPC, err := requestInput(r, st, "rpc")
		if err != nil {
			return tree, nil, err
		}
		if hasRPC || st["parse_only"] != true {
			return tree, nil, requestError("parse parent excludes rpc and requires parse_only")
		}
		parent, err := findNode(tree, parentPath, "parse parent")
		if err != nil {
			return tree, nil, err
		}
		if typ != data.OpReply {
			return tree, nil, fmt.Errorf("%w: operation input into an existing parent is not exported (U-0106)", ErrUnsupported)
		}
		in, present, err := requestInput(r, st, "data")
		if err != nil {
			return tree, nil, err
		}
		f, err := formatOf(st)
		if err != nil {
			return tree, nil, err
		}
		if ps := parent.Schema(); ps == nil || ps.Kind() != yang.KindRPC && ps.Kind() != yang.KindAction {
			return tree, nil, fmt.Errorf("%w: reply into a non-operation parent (ParseOp requires an RPC or action, U-0110)", ErrUnsupported)
		}
		_, diags, err := data.ParseOp(context.Background(), inputReader(in, present), f, s, typ, data.ParseOpOptions{Unknown: unknownPolicies[str(st, "unknown", "reject")], Request: parent})
		return tree, phased(nil, diags, "parse"), err
	}

	t, diags, err := parseOne(r, s, st, exec, "data", "parse", nil)
	if err == nil && t != nil {
		tree = t
	}
	return tree, diags, err
}

// stepValidate is step_validate: lyd_validate_all or lyd_validate_module with the step's flags
// and its implicit diff.
func stepValidate(s *yang.Schema, st map[string]any, tree *data.Tree, so map[string]any) (*data.Tree,
	[]yang.Diagnostic, error) {
	o, err := dataOptions(st, execStepValidate)
	if err != nil {
		return nil, nil, err
	}
	if tree == nil {
		if tree, err = emptyTree(s); err != nil {
			return nil, nil, err
		}
	}
	module := str(st, "module", "")
	var diff *data.Tree
	var diags []yang.Diagnostic
	if module == "" {
		diff, diags, err = tree.ValidateDiff(context.Background(), o.Validate)
	} else {
		diff, diags, err = tree.ValidateModuleDiff(context.Background(), module, o.Validate)
	}
	so["implicit_diff"] = nil
	if diff != nil && !empty(diff) {
		var b strings.Builder
		if perr := diff.PrintJSON(&b, data.PrintOptions{WithDefaults: data.WDAll}); perr != nil {
			return nil, nil, unsupported(perr)
		}
		so["implicit_diff"] = b.String()
	}
	return tree, diags, err
}

// stepEdit is step_edit for insert_term, insert_inner and insert_list (Tree.NewTerm, NewInner,
// NewList), set (lyd_new_path UPDATE), delete (lyd_find_path + lyd_free_tree),
// new_meta (Tree.NewMeta), free_meta (Node.FindMeta + Meta.Remove) and merge (a parse-only parse
// + lyd_merge_siblings). rc is set when the outcome is an LY_ERR without a diagnostic (a delete
// that finds nothing).
func stepEdit(r Request, s *yang.Schema, st map[string]any, tree *data.Tree) (*data.Tree, []yang.Diagnostic, string,
	error) {
	rc := "LY_SUCCESS"
	var err error
	if tree == nil {
		if tree, err = emptyTree(s); err != nil {
			return nil, nil, rc, err
		}
	}
	switch {
	case st["set"] != nil:
		x, _ := st["set"].(map[string]any)
		path := str(x, "path", "")
		// lyoracle passes the retained first node as lyd_new_path's parent (NULL for an empty
		// tree). lyd_new_path checks (path[0] == '/') || parent before it tokenizes
		// (tree_data_new.c:1868), so without a parent the path goes to NewPath unchanged and a
		// leading space is its LY_EINVAL argument error. With a parent, any path that does not
		// start with '/' (relative, or whitespace-led and absolute only to the lexer) reaches
		// lyd_new_path_ with that parent and the original text, which Tree.NewPath cannot take:
		// it has no parent argument (#255), and trimming or rewriting the path changes libyang's
		// diagnostics (positions, path text), so the engine does not emulate it.
		if topNode(tree) != nil && (path == "" || path[0] != '/') {
			return nil, nil, rc, fmt.Errorf("%w: relative set under a retained parent, or a path with leading "+
				"whitespace (Tree.NewPath has no parent argument, #255, U-0110)", ErrUnsupported)
		}
		_, err = tree.NewPath(path, str(x, "value", ""), data.NewPathOptions{Update: true})
	case st["new_meta"] != nil || st["free_meta"] != nil:
		x, _ := st["new_meta"].(map[string]any)
		if x == nil {
			x, _ = st["free_meta"].(map[string]any)
		}
		path := str(x, "node", "")
		n, ferr := tree.Find(path)
		if ferr != nil || n == nil {
			return nil, nil, rc, fmt.Errorf("metadata node %s not found (a request-error)", path)
		}
		if st["new_meta"] != nil {
			_, err = tree.NewMeta(n, str(x, "name", ""), str(x, "value", ""))
		} else {
			var m *data.Meta
			if m, err = n.FindMeta(str(x, "name", "")); m != nil {
				m.Remove()
			}
		}
	case st["insert_list2"] != nil:
		err = stepInsertList2(s, st, tree)
	case st["insert_term"] != nil || st["insert_inner"] != nil || st["insert_list"] != nil:
		err = stepInsert(s, st, tree)
	case st["delete"] != nil:
		path := str(st, "delete", "")
		if empty(tree) {
			return tree, nil, "LY_ENOTFOUND", nil // the oracle does not search a NULL tree
		}
		// lyoracle.c:2305 calls lyd_find_path from the retained first node.
		var n *data.Node
		if n, err = tree.Find(path); err == nil {
			if n == nil {
				return tree, nil, notFoundRC(tree, path), nil
			}
			err = n.Remove()
		}
	default:
		o, oerr := dataOptions(st, execStepEdit)
		if oerr != nil {
			return nil, nil, rc, oerr
		}
		o.ParseOnly = true
		f, ferr := formatOf(st)
		if ferr != nil {
			return nil, nil, rc, ferr
		}
		in, present, ierr := requestInput(r, st, "merge")
		if ierr != nil {
			return nil, nil, rc, ierr
		}
		var src *data.Tree
		var diags []yang.Diagnostic
		if src, diags, err = data.Parse(context.Background(), inputReader(in, present), f, s, o); err != nil {
			return tree, diags, rc, err
		}
		err = tree.Merge(src)
		return tree, append(diags, diagsOf(err)...), rc, err
	}
	return tree, diagsOf(err), rc, err
}

// notFoundRC is lyd_find_path's return for a path Find did not find: LY_EINCOMPLETE when a leading
// part of it exists (a partial match), else LY_ENOTFOUND.
// ponytail: re-finds each shorter prefix of the path; data.Find does not tell partial from none.
func notFoundRC(tree *data.Tree, path string) string {
	for _, p := range pathPrefixes(path) {
		if n, err := tree.Find(p); err == nil && n != nil {
			return "LY_EINCOMPLETE"
		}
	}
	return "LY_ENOTFOUND"
}

// pathPrefixes are the proper prefixes of an absolute or relative JSON path, longest first, split at the
// '/' outside predicates and quotes.
func pathPrefixes(path string) []string {
	var cuts []int
	depth, quote := 0, byte(0)
	for i := 1; i < len(path); i++ {
		switch c := path[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			depth--
		case c == '/' && depth == 0:
			cuts = append(cuts, i)
		}
	}
	out := make([]string, 0, len(cuts))
	for i := len(cuts) - 1; i >= 0; i-- {
		out = append(out, path[:cuts[i]])
	}
	return out
}

// stepInsert is step_edit's insert_term, insert_inner and insert_list: the node under the node at
// "parent" (a path) or the top-level opaque node "parent_opaq", else a top-level node of the tree
// (lyd_new_* + lyd_insert_sibling); "module" qualifies the name.
func stepInsert(s *yang.Schema, st map[string]any, tree *data.Tree) error {
	x, kind := st["insert_term"], "term"
	if st["insert_inner"] != nil {
		x, kind = st["insert_inner"], "inner"
	} else if st["insert_list"] != nil {
		x, kind = st["insert_list"], "list"
	}
	o, _ := x.(map[string]any)
	if err := checkRequest(Request{}, s, o, execStepEdit, []requestRule{{0, requestModule}}); err != nil {
		return err
	}
	var parent *data.Node
	if p, present, _ := strField(o, "parent"); present {
		if parent, _ = tree.Find(p); parent == nil {
			return fmt.Errorf("insert parent %s not found (a request-error)", p)
		}
	}
	if p, present, _ := strField(o, "parent_opaq"); present {
		for n := range tree.Top() {
			if n.Schema() == nil && n.Name() == p {
				parent = n
				break
			}
		}
		if parent == nil {
			return fmt.Errorf("insert parent_opaq %s not found (a request-error)", p)
		}
	}
	name := str(o, "name", "")
	if m := str(o, "module", ""); m != "" {
		name = m + ":" + name
	}
	opts := data.NewOptions{Output: len(list(o["options"])) > 0} // only "output" gets here
	var err error
	switch kind {
	case "term":
		_, err = tree.NewTerm(parent, name, str(o, "value", ""), opts)
	case "inner":
		_, err = tree.NewInner(parent, name, opts)
	default:
		var keys []string // absent: lyd_new_list3's NULL array
		if ks, ok := o["keys"].([]any); ok {
			keys = []string{}
			for _, k := range ks {
				v, _ := k.(string) // null: libyang's NULL value, ""
				keys = append(keys, v)
			}
		}
		if keys != nil {
			var sn *yang.SchemaNode
			if parent != nil && parent.Schema() != nil {
				ps := parent.Schema()
				if ps.Kind() == yang.KindRPC || ps.Kind() == yang.KindAction {
					kind := yang.KindInput
					if opts.Output {
						kind = yang.KindOutput
					}
					for child := range ps.Children() {
						if child.Kind() == kind {
							ps = child
							break
						}
					}
				}
				sn = ps.Child(str(o, "module", ""), str(o, "name", ""))
			} else if m := s.Implemented(str(o, "module", "")); m != nil {
				sn = topChild(m.Top(), str(o, "name", ""))
			}
			if sn != nil && sn.Kind() == yang.KindList && len(keys) < len(slices.Collect(sn.Keys())) {
				return requestError("insert_list %s has fewer keys than the list", o["name"])
			}
		}
		_, err = tree.NewList(parent, name, keys, opts)
	}
	return err
}

// stepDup is step_dup: Tree.Dup (DupSiblings with "siblings") of the node at "node" under the
// node at "parent"; without a parent the copy, from its top duplicated parent, replaces the tree.
// With "target" (searchdirs, modules, context_options) the copy goes into a tree over a second
// context built like the request's (lyd_dup_*_to_ctx).
func stepDup(r Request, s *yang.Schema, st map[string]any, tree *data.Tree) (*data.Tree, *yang.Schema, []yang.Diagnostic,
	error) {
	n, _ := findNode(tree, str(st, "node", ""), "dup node")
	if n == nil {
		return nil, nil, nil, fmt.Errorf("dup node %s not found (a request-error)", str(st, "node", ""))
	}
	var parent *data.Node
	if p, present, _ := strField(st, "parent"); present {
		if parent, _ = tree.Find(p); parent == nil {
			return nil, nil, nil, fmt.Errorf("dup parent %s not found (a request-error)", p)
		}
	}
	var o data.DupOptions
	for _, f := range list(st["options"]) {
		switch f {
		case "recursive":
			o.Recursive = true
		case "no_meta":
			o.NoMeta = true
		case "with_parents":
			o.WithParents = true
		case "with_flags":
			o.WithFlags = true
		}
	}
	dst, ds := tree, s
	if parent == nil {
		dst = data.NewTree(s)
	}
	if tg, ok := st["target"].(map[string]any); ok {
		targetSchema, _, _, verdict, err := r.buildContext(Request{ID: r.ID, BaseDir: r.BaseDir, Params: tg, buildContext: r.buildContext})
		if err != nil {
			return nil, nil, nil, err
		}
		if verdict != "valid" || parent != nil {
			return nil, nil, nil, fmt.Errorf("dup target context failed (a request-error)")
		}
		ds = targetSchema
		dst = data.NewTree(ds)
	}
	dup := dst.Dup
	if st["siblings"] == true {
		dup = dst.DupSiblings
	}
	_, diags, err := dup(n, parent, o)
	if err != nil {
		return tree, s, diagsOf(err), err
	}
	return dst, ds, diags, nil
}

// stepChange is the change_term step: Node.SetValue of the node at "node"; "change" is
// lyd_change_term's rc, where LY_EEXIST (only the default flag cleared) and LY_ENOT (no change)
// are results. Node.SetValue tells changed from not, so the two are told apart by the node's
// Default flag before the call. libyang's argument checks log without a context: no diagnostics.
func stepChange(st map[string]any, tree *data.Tree, so map[string]any) ([]yang.Diagnostic, error) {
	n, _ := findNode(tree, str(st, "node", ""), "change_term node")
	if n == nil {
		return nil, fmt.Errorf("change_term node %s not found (a request-error)", str(st, "node", ""))
	}
	dflt := n.Flags()&data.FlagDefault != 0
	changed, err := n.SetValue(str(st, "value", ""))
	switch {
	case err != nil:
		rc, rerr := rcOf(err)
		if rerr != nil {
			return nil, rerr
		}
		so["change"] = codeJSON(rc)
		if !isTerm(n) {
			return nil, err // the argument checks log without a context
		}
		return diagsOf(err), err
	case changed:
		so["change"] = codeJSON("LY_SUCCESS")
	case dflt:
		so["change"] = codeJSON("LY_EEXIST")
	default:
		so["change"] = codeJSON("LY_ENOT")
	}
	return nil, nil
}

// isTerm reports whether n is a leaf or leaf-list instance.
func isTerm(n *data.Node) bool {
	sn := n.Schema()
	return sn != nil && (sn.Kind() == yang.KindLeaf || sn.Kind() == yang.KindLeafList)
}

// linksJSON is links_json: the leafref link records of the tree's term nodes in DFS order.
func linksJSON(tree *data.Tree) []any {
	out := []any{}
	if tree == nil {
		return out
	}
	paths := func(ns []*data.Node) []any {
		a := []any{}
		for _, n := range ns {
			a = append(a, n.Path())
		}
		return a
	}
	for top := range tree.Top() {
		for n := range top.All() {
			if !isTerm(n) {
				continue
			}
			if l, t := n.LeafrefLinks(); l != nil || t != nil {
				out = append(out, map[string]any{"node": n.Path(), "leafref_nodes": paths(l), "target_nodes": paths(t)})
			}
		}
	}
	return out
}

// stepInsertList2 is step_edit's insert_list2 (lyd_new_list2) through Tree.NewPath, which design 07
// §6.1.7 names its replacement: the path of the list instance with the predicates, "/" + module +
// ":" + name + keys at the top level, else below the node at "parent".
func stepInsertList2(s *yang.Schema, st map[string]any, tree *data.Tree) error {
	o, _ := st["insert_list2"].(map[string]any)
	name, mod := str(o, "name", ""), str(o, "module", "")
	if err := checkRequest(Request{}, s, o, execStepEdit, []requestRule{{0, requestModule}}); err != nil {
		return err
	}
	if p, present, _ := strField(o, "parent"); present {
		if _, err := findNode(tree, p, "insert parent"); err != nil {
			return err
		}
	}
	switch {
	case str(o, "parent_opaq", "") != "": // NewPath cannot build under an opaque node
		return fmt.Errorf("%w: insert_list2 under an opaque parent (U-0109: NewPath builds from the "+
			"schema tree, not under opaque nodes)", ErrUnsupported)
	case len(list(o["options"])) > 0: // NewPath has no LYD_NEW_VAL_OUTPUT
		return fmt.Errorf("%w: insert_list2 options %v (U-0109: NewPath selects an operation's input "+
			"nodes only)", ErrUnsupported, list(o["options"]))
	case !onlyPredicates(str(o, "keys", "")):
		return fmt.Errorf("%w: insert_list2 keys %q are not only predicates (U-0109: NewPath would "+
			"take the rest as a path)", ErrUnsupported, str(o, "keys", ""))
	}
	// lyd_new_list2 finds a list schema node first ("List node … not found." otherwise); NewPath
	// would build any node the path names
	var sn *yang.SchemaNode
	if p, present, _ := strField(o, "parent"); present {
		if pn, _ := tree.Find(p); pn != nil && pn.Schema() != nil {
			sn = pn.Schema().Child(mod, name)
		}
	} else if m := s.Implemented(mod); m != nil {
		sn = topChild(m.Top(), name)
	}
	if sn == nil || sn.Kind() != yang.KindList {
		return fmt.Errorf("%w: insert_list2 of %s, not a list (U-0109: lyd_new_list2's not-found error is not "+
			"NewPath's)", ErrUnsupported, name)
	}
	if mod != "" {
		name = mod + ":" + name
	}
	path := "/" + name + str(o, "keys", "")
	if p, present, _ := strField(o, "parent"); present {
		path = p + path
	}
	if _, err := tree.NewPath(path, "", data.NewPathOptions{}); err != nil {
		return fmt.Errorf("%w: insert_list2 failed through NewPath (U-0109: lyd_new_path's error items, "+
			"not lyd_new_list2's): %v", ErrUnsupported, err)
	}
	return nil
}

// topChild is the top-level data node name among nodes, looking through choice and case.
func topChild(nodes iter.Seq[*yang.SchemaNode], name string) *yang.SchemaNode {
	for n := range nodes {
		switch n.Kind() {
		case yang.KindChoice, yang.KindCase:
			if c := topChild(n.Children(), name); c != nil {
				return c
			}
		default:
			if n.Name() == name {
				return n
			}
		}
	}
	return nil
}

// onlyPredicates reports whether keys is a run of [...] predicates (quotes respected) and nothing
// else, as lyd_new_list2's keys are; NewPath would read anything after them as more path.
func onlyPredicates(keys string) bool {
	depth, quote := 0, byte(0)
	for i := 0; i < len(keys); i++ {
		switch c := keys[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case depth > 0 && (c == '\'' || c == '"'):
			quote = c
		case c == '[':
			depth++
		case c == ']' && depth > 0:
			depth--
		case depth == 0 && c != ' ' && c != '\t' && c != '\n':
			return false
		}
	}
	return true
}

// stepMove is step_insert: Tree.Insert of the node at "node" under the node at "parent", or at the
// top level without one. With "data" the node comes from a second tree parsed as by a parse step
// ("node" a path in it, omitted: its first top-level node, which moves with its siblings); what is
// left of that tree is then freed, as the oracle frees it (lyd_free_all: link records to it go).
// The parse's diagnostics (phase parse, its warnings too) come before the insert's (phase edit).
// lyd_insert_child's and lyd_insert_sibling's argument checks log without a context: those items
// are not reported.
func stepMove(r Request, s *yang.Schema, st map[string]any, tree *data.Tree) (*data.Tree, []yang.Diagnostic, string,
	error) {
	var err error
	if tree == nil {
		if tree, err = emptyTree(s); err != nil {
			return nil, nil, "", err
		}
	}
	src, path := tree, str(st, "node", "")
	var diags []yang.Diagnostic
	_, hasData, err := requestInput(r, st, "data")
	if err != nil {
		return tree, nil, "", err
	}
	if hasData {
		if src, diags, err = stepParse(r, s, st, execStepInsert, nil); err != nil {
			return tree, diags, "parse", err
		}
		for i := range diags {
			diags[i].Phase = "parse"
		}
	}
	n, _ := selectedNode(src, st, "node", "insert node")
	if n == nil {
		if hasData {
			return nil, nil, "", requestError("insert node %s not found in the data", path)
		}
		return nil, nil, "", requestError("insert node %s not found", path)
	}
	var parent *data.Node
	if p, present, _ := strField(st, "parent"); present {
		if parent, _ = tree.Find(p); parent == nil {
			return nil, nil, "", fmt.Errorf("insert parent %s not found (a request-error)", p)
		}
	}
	err = tree.Insert(parent, n)
	ins := diagsOf(err)
	if len(ins) == 1 && strings.HasPrefix(ins[0].Msg, "Invalid argument ") {
		ins = nil
	}
	for i := range ins {
		ins[i].Phase = "edit"
	}
	if src != tree {
		for _, rest := range slices.Collect(src.Top()) {
			if rerr := rest.Remove(); rerr != nil {
				return nil, nil, "", rerr
			}
		}
	}
	return tree, append(diags, ins...), "", err
}

// checkInsertSelectors is step_edit's module and parent lookup before construction.
func checkInsertSelectors(s *yang.Schema, tree *data.Tree, st map[string]any) error {
	for _, kind := range []string{"insert_term", "insert_inner", "insert_list", "insert_list2", "insert_opaq"} {
		o, ok := st[kind].(map[string]any)
		if !ok {
			continue
		}
		if kind != "insert_opaq" {
			if err := checkRequest(Request{}, s, o, execStepEdit, []requestRule{{0, requestModule}}); err != nil {
				return err
			}
		}
		var parent *data.Node
		if path, present, _ := strField(o, "parent"); present {
			var err error
			parent, err = findNode(tree, path, "insert parent")
			if err != nil {
				return err
			}
		}
		if name, present, _ := strField(o, "parent_opaq"); present {
			if tree != nil {
				for n := range tree.Top() {
					if n.Schema() == nil && n.Name() == name {
						parent = n
						break
					}
				}
			}
			if parent == nil {
				return requestError("insert parent_opaq %s not found", name)
			}
		}
		if kind == "insert_opaq" && parent != nil && isTerm(parent) {
			return requestError("insert_opaq under the leaf %s: libyang links it over the leaf's value", parent.Name())
		}
		if kind != "insert_opaq" && o["module"] == nil && (parent == nil || parent.Schema() == nil) {
			return requestError("insert %s under an opaque parent needs a module (libyang reads its NULL schema)", o["name"])
		}
	}
	return nil
}
