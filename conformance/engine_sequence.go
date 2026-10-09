// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"context"
	"fmt"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// seqKeys are the fields of each sequence step kind runSequence handles (lyoracle.c check_step).
var seqKeys = map[string]map[string]bool{
	"parse": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options"),
	"validate": set("do", "data_type", "validate_options"),
	"edit": set("do", "set", "delete", "new_meta", "free_meta", "merge", "merge_file", "format", "data_type", "unknown",
		"parse_options"),
	"dump": set("do", "with_defaults"),
	"compare": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options", "first", "second", "options"),
	"diff": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options", "node", "data_node", "single", "options", "merge", "merge_options"),
	"diff_parse":   set("do", "format", "data_type", "data", "data_file", "unknown", "parse_options"),
	"diff_merge":   set("do", "format", "data_type", "data", "data_file", "unknown", "parse_options", "options", "module", "src_node", "parent"),
	"diff_apply":   set("do", "module"),
	"diff_reverse": set("do"),
}

var seqRequestKeys = set("op", "base_dir", "searchdirs", "modules", "context_options", "steps")

// seqUnsupported are the oracle's step kinds and edits package data has no public API for.
var seqUnsupported = map[string]string{
	"dup":          "(lyd_dup_single / lyd_dup_siblings are not exported by package data)",
	"link":         "(leafref links, LY_CTX_LEAFREF_LINKING, are not exported by package data)",
	"links":        "(leafref links, LY_CTX_LEAFREF_LINKING, are not exported by package data)",
	"insert_term":  "(lyd_new_term + lyd_insert_sibling: package data exports only NewPath)",
	"insert_inner": "(lyd_new_inner + lyd_insert_sibling: package data exports only NewPath)",
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
	for i, st := range steps {
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

// stepEdit is step_edit for set (lyd_new_path UPDATE), delete (lyd_find_path + lyd_free_tree),
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
