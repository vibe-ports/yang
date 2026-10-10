// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"context"
	"fmt"
	"iter"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// seqKeys are the fields of each sequence step kind runSequence handles (lyoracle.c check_step).
var seqKeys = map[string]map[string]bool{
	"parse": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options"),
	"validate": set("do", "data_type", "validate_options"),
	"edit": set("do", "set", "delete", "new_meta", "free_meta", "insert_term", "insert_inner", "insert_list", "insert_list2", "merge",
		"merge_file", "format", "data_type", "unknown", "parse_options"),
	"dup":         set("do", "node", "parent", "options", "siblings", "target"),
	"link":        set("do"),
	"links":       set("do"),
	"change_term": set("do", "node", "value", "canon"),
	"dump":        set("do", "with_defaults"),
	"compare": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options", "first", "second", "options"),
	"diff": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options", "node", "data_node", "single", "options", "merge", "merge_options"),
	"diff_parse":   set("do", "format", "data_type", "data", "data_file", "unknown", "parse_options"),
	"diff_merge":   set("do", "format", "data_type", "data", "data_file", "unknown", "parse_options", "options", "module", "src_node", "parent"),
	"diff_apply":   set("do", "module"),
	"diff_reverse": set("do"),
	"trim":         set("do", "xpath", "vars"),
}

var seqRequestKeys = set("op", "base_dir", "searchdirs", "modules", "context_options", "steps")

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
// checked first, so a fixture with a step the API cannot express is unsupported as a whole.
func runSequence(r Request, s *yang.Schema, resp map[string]any) error {
	for k := range r.Params {
		if !seqRequestKeys[k] {
			return fmt.Errorf("%w: sequence request field %s", ErrUnsupported, k)
		}
	}
	var steps []map[string]any
	for _, x := range list(r.Params["steps"]) {
		st, _ := x.(map[string]any)
		what := str(st, "do", "")
		if why, ok := seqUnsupported[what]; ok {
			return fmt.Errorf("%w: sequence step %s %s", ErrUnsupported, what, why)
		}
		keys, ok := seqKeys[what]
		if !ok {
			return fmt.Errorf("%w: sequence step %q", ErrUnsupported, what)
		}
		if why := seqOptionUnsupported(what, st); why != "" {
			return fmt.Errorf("%w: %s", ErrUnsupported, why)
		}
		for k := range st {
			if why, bad := seqUnsupported[k]; bad && what == "edit" {
				return fmt.Errorf("%w: edit %s %s", ErrUnsupported, k, why)
			}
			if !keys[k] {
				return fmt.Errorf("%w: %s step field %s", ErrUnsupported, what, k)
			}
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
		phase := what
		switch what {
		case "parse":
			tree, diags, err = stepParse(r, s, st, tree)
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
		case "compare":
			phase = "parse"
			diags, err = stepCompare(r, s, st, tree, so)
		case "diff":
			phase = "" // the diagnostics carry theirs
			diags, err = stepDiff(r, s, st, tree, &diff, so)
		case "diff_parse":
			phase = "parse"
			var d *data.Tree
			if d, diags, err = parseDiff(r, s, st); err == nil {
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
	other, diags, err := stepParse(r, s, st, nil)
	if err != nil || other == nil {
		return diags, err
	}
	n1, err := nodeAt(tree, str(st, "first", ""))
	if err != nil {
		return nil, err
	}
	n2, err := nodeAt(other, str(st, "second", ""))
	if err != nil {
		return nil, err
	}
	so["compare"] = codeJSON("LY_ENOT")
	if n1.Equal(n2, o) {
		so["compare"] = codeJSON("LY_SUCCESS")
	}
	return diags, nil
}

// nodeAt is the node at path in tree, its first top-level node for "" (nil for no tree).
func nodeAt(tree *data.Tree, path string) (*data.Node, error) {
	if tree == nil {
		return nil, nil
	}
	if path == "" {
		for n := range tree.Top() {
			return n, nil
		}
		return nil, nil
	}
	n, err := tree.Find(path)
	if err == nil && n == nil {
		err = fmt.Errorf("compare node %s not found", path)
	}
	return n, err
}

// stepParse is step_parse: the parsed tree replaces the retained one only on success.
func stepParse(r Request, s *yang.Schema, st map[string]any, tree *data.Tree) (*data.Tree, []yang.Diagnostic, error) {
	o, err := dataOptions(st)
	if err != nil {
		return nil, nil, err
	}
	f, err := formatOf(st)
	if err != nil {
		return nil, nil, err
	}
	in, err := inputOf(r, st, "data")
	if err != nil {
		return nil, nil, err
	}
	t, diags, err := data.Parse(context.Background(), strings.NewReader(in), f, s, o)
	if err == nil && t != nil {
		tree = t
	}
	return tree, diags, err
}

// stepValidate is step_validate: lyd_validate_all with the step's flags and its implicit diff.
func stepValidate(s *yang.Schema, st map[string]any, tree *data.Tree, so map[string]any) (*data.Tree,
	[]yang.Diagnostic, error) {
	o, err := dataOptions(st)
	if err != nil {
		return nil, nil, err
	}
	if tree == nil {
		if tree, err = emptyTree(s); err != nil {
			return nil, nil, err
		}
	}
	diff, diags, err := tree.ValidateDiff(context.Background(), o.Validate)
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
		_, err = tree.NewPath(str(x, "path", ""), str(x, "value", ""), data.NewPathOptions{Update: true})
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
		err = stepInsert(st, tree)
	case st["delete"] != nil:
		path := str(st, "delete", "")
		if empty(tree) {
			return tree, nil, "LY_ENOTFOUND", nil // the oracle does not search a NULL tree
		}
		var n *data.Node
		if n, err = tree.Find(path); err == nil {
			if n == nil {
				return tree, nil, notFoundRC(tree, path), nil
			}
			err = n.Remove()
		}
	default:
		o, oerr := dataOptions(st)
		if oerr != nil {
			return nil, nil, rc, oerr
		}
		o.ParseOnly = true
		f, ferr := formatOf(st)
		if ferr != nil {
			return nil, nil, rc, ferr
		}
		in, ierr := inputOf(r, st, "merge")
		if ierr != nil {
			return nil, nil, rc, ierr
		}
		var src *data.Tree
		var diags []yang.Diagnostic
		if src, diags, err = data.Parse(context.Background(), strings.NewReader(in), f, s, o); err != nil {
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

// pathPrefixes are the proper prefixes of an absolute JSON path, longest first, split at the
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
func stepInsert(st map[string]any, tree *data.Tree) error {
	x, kind := st["insert_term"], "term"
	if st["insert_inner"] != nil {
		x, kind = st["insert_inner"], "inner"
	} else if st["insert_list"] != nil {
		x, kind = st["insert_list"], "list"
	}
	o, _ := x.(map[string]any)
	var parent *data.Node
	if p := str(o, "parent", ""); p != "" {
		if parent, _ = tree.Find(p); parent == nil {
			return fmt.Errorf("insert parent %s not found (a request-error)", p)
		}
	}
	if p := str(o, "parent_opaq", ""); p != "" {
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
	n, _ := nodeAt(tree, str(st, "node", ""))
	if n == nil {
		return nil, nil, nil, fmt.Errorf("dup node %s not found (a request-error)", str(st, "node", ""))
	}
	var parent *data.Node
	if p := str(st, "parent", ""); p != "" {
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
		ctx, _, _, verdict, err := buildContext(Request{ID: r.ID, BaseDir: r.BaseDir, Params: tg})
		if err != nil {
			return nil, nil, nil, err
		}
		if verdict != "valid" || parent != nil {
			return nil, nil, nil, fmt.Errorf("dup target context failed (a request-error)")
		}
		ds = ctx.Schema()
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
	n, _ := nodeAt(tree, str(st, "node", ""))
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
	if p := str(o, "parent", ""); p != "" {
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
	if p := str(o, "parent", ""); p != "" {
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
