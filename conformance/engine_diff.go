// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"context"
	"fmt"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// diffKeys are the op diff request fields runDiff handles.
var diffKeys = map[string]bool{"op": true, "base_dir": true, "searchdirs": true, "modules": true,
	"context_options": true, "format": true, "data_type": true, "first": true, "first_file": true, "second": true,
	"second_file": true, "unknown": true, "parse_only": true, "parse_options": true, "validate_options": true,
	"diff_options": true}

// diffOptions are lyoracle.c diff_flags.
func diffOptions(p map[string]any, key string) (data.DiffOptions, error) {
	var o data.DiffOptions
	for _, f := range list(p[key]) {
		switch f {
		case "defaults":
			o.Defaults = true
		case "meta":
			o.Meta = true
		default:
			return o, fmt.Errorf("%w: diff option %v", ErrUnsupported, f)
		}
	}
	return o, nil
}

// topNode is the first top-level node of t, nil for no tree (libyang's NULL tree).
func topNode(t *data.Tree) *data.Node {
	if t != nil {
		for n := range t.Top() {
			return n
		}
	}
	return nil
}

// parseInput is parse_one of the input key for datastore data.
func parseInput(r Request, s *yang.Schema, p map[string]any, key string) (*data.Tree, []yang.Diagnostic, error) {
	o, err := dataOptions(p)
	if err != nil {
		return nil, nil, err
	}
	f, err := formatOf(p)
	if err != nil {
		return nil, nil, err
	}
	in, err := inputOf(r, p, key)
	if err != nil {
		return nil, nil, err
	}
	return data.Parse(context.Background(), strings.NewReader(in), f, s, o)
}

// runDiff is lyoracle.c op_diff: both inputs parsed as op data parses them, lyd_diff_siblings,
// the diff printed with LYD_PRINT_WD_ALL.
func runDiff(r Request, s *yang.Schema, resp map[string]any) error {
	p := r.Params
	for k := range p {
		if !diffKeys[k] {
			return fmt.Errorf("%w: diff request field %s", ErrUnsupported, k)
		}
	}
	o, err := diffOptions(p, "diff_options")
	if err != nil {
		return err
	}
	diags := []any{}
	var trees [2]*data.Tree
	failed := false
	for i, key := range []string{"first", "second"} {
		t, d, perr := parseInput(r, s, p, key)
		rc, err := rcOf(perr)
		if err != nil {
			return err
		}
		diags = append(diags, diagsJSON(d, key)...)
		failed = failed || rc != "LY_SUCCESS"
		trees[i] = t
	}
	resp["diagnostics"] = diags
	if failed {
		resp["verdict"] = "data-error"
		return nil
	}
	diff, err := data.DiffSiblings(topNode(trees[0]), topNode(trees[1]), o)
	rc, rerr := rcOf(err)
	if rerr != nil {
		return rerr
	}
	resp["diagnostics"] = append(diags, diagsJSON(diagsOf(err), "diff")...)
	resp["verdict"], resp["rc"] = "valid", codeJSON(rc)
	if rc != "LY_SUCCESS" {
		resp["verdict"] = "invalid"
	}
	resp["diff"] = nil
	if diff != nil {
		if resp["diff"], err = printTree(diff, data.WDAll); err != nil {
			return err
		}
		resp["typed"] = typedJSON(diff)
	}
	return nil
}

// stepDiff is lyoracle.c step_diff: lyd_diff_siblings (lyd_diff_tree with single) of the tree
// (or its node at "node") and the parsed data (or its node at "data_node"); the result replaces
// the diff register, or with merge is merged into it (reported as new_diff). The diagnostics carry
// their phase (parse, diff).
func stepDiff(r Request, s *yang.Schema, st map[string]any, tree *data.Tree, reg **data.Tree,
	so map[string]any) ([]yang.Diagnostic, error) {
	o, err := diffOptions(st, "options")
	if err != nil {
		return nil, err
	}
	mo, err := mergeOptions(st, "merge_options")
	if err != nil {
		return nil, err
	}
	first := topNode(tree)
	if path, ok := st["node"].(string); ok {
		if first, err = findNode(tree, path); err != nil {
			return nil, err
		}
	}
	var second *data.Node
	var diags []yang.Diagnostic
	if st["data"] != nil || st["data_file"] != nil {
		t, d, perr := parseInput(r, s, st, "data")
		diags = phased(diags, d, "parse")
		if perr != nil {
			return diags, perr
		}
		second = topNode(t)
		if path, ok := st["data_node"].(string); ok {
			if second, err = findNode(t, path); err != nil {
				return nil, err
			}
		}
	}
	diff := data.DiffSiblings
	if b, _ := st["single"].(bool); b {
		diff = data.DiffTree
	}
	res, err := diff(first, second, o)
	if err != nil {
		return phased(diags, diagsOf(err), "diff"), err
	}
	if b, _ := st["merge"].(bool); !b {
		*reg = res
		return diags, nil
	}
	so["new_diff"] = nil
	if res != nil {
		if so["new_diff"], err = printTree(res, data.WDAll); err != nil {
			return nil, err
		}
	}
	err = mergeInto(s, reg, func(t *data.Tree) error { return t.MergeDiff(res, mo) })
	return phased(diags, diagsOf(err), "diff"), err
}

// phased appends ds to diags with the phase set.
func phased(diags, ds []yang.Diagnostic, phase string) []yang.Diagnostic {
	for _, d := range ds {
		d.Phase = phase
		diags = append(diags, d)
	}
	return diags
}

// mergeOptions are lyoracle.c diff_merge_flags.
func mergeOptions(p map[string]any, key string) (data.MergeDiffOptions, error) {
	var o data.MergeDiffOptions
	for _, f := range list(p[key]) {
		if f != "defaults" {
			return o, fmt.Errorf("%w: diff merge option %v", ErrUnsupported, f)
		}
		o.Defaults = true
	}
	return o, nil
}

// mergeInto runs merge on the diff register, an empty diff for libyang's NULL one, which an empty
// result becomes again.
func mergeInto(s *yang.Schema, reg **data.Tree, merge func(*data.Tree) error) error {
	if *reg == nil {
		t, err := emptyTree(s)
		if err != nil {
			return err
		}
		*reg = t
	}
	err := merge(*reg)
	if empty(*reg) {
		*reg = nil
	}
	return err
}

// parseDiff is the parse of a diff_parse or diff_merge step: the data parsed with LYD_PARSE_ONLY.
func parseDiff(r Request, s *yang.Schema, st map[string]any) (*data.Tree, []yang.Diagnostic, error) {
	p := map[string]any{}
	for k, v := range st {
		p[k] = v
	}
	p["parse_only"] = true
	return parseInput(r, s, p, "data")
}

// stepDiffMerge is lyoracle.c step_diff_merge: the parsed source merged into the register
// (MergeDiff with the module filter, or MergeDiffTree of src_node under parent).
func stepDiffMerge(r Request, s *yang.Schema, st map[string]any, reg **data.Tree) ([]yang.Diagnostic, error) {
	o, err := mergeOptions(st, "options")
	if err != nil {
		return nil, err
	}
	o.Module = str(st, "module", "")
	src, d, err := parseDiff(r, s, st)
	diags := phased(nil, d, "parse")
	if err != nil {
		return diags, err
	}
	var merge func(*data.Tree) error
	if path, ok := st["src_node"].(string); ok {
		sn, err := findNode(src, path)
		if err != nil {
			return nil, err
		}
		var parent *data.Node
		if pp, ok := st["parent"].(string); ok {
			if parent, err = findNode(*reg, pp); err != nil {
				return nil, err
			}
		}
		merge = func(t *data.Tree) error { return t.MergeDiffTree(parent, sn, o) }
	} else {
		merge = func(t *data.Tree) error { return t.MergeDiff(src, o) }
	}
	err = mergeInto(s, reg, merge)
	return phased(diags, diagsOf(err), "diff"), err
}

// findNode is the node at path in t; none is an engine error (the oracle's request-error).
func findNode(t *data.Tree, path string) (*data.Node, error) {
	if t == nil {
		return nil, fmt.Errorf("conformance: no tree for %s", path)
	}
	n, err := t.Find(path)
	if err == nil && n == nil {
		err = fmt.Errorf("conformance: %s not found", path)
	}
	return n, err
}

// diffOut is the diff register of a diff step as lyoracle.c reports it: printed and dumped.
func diffOut(reg *data.Tree, so map[string]any) error {
	so["diff"], so["diff_typed"] = nil, []any{}
	if reg == nil {
		return nil
	}
	var err error
	if so["diff"], err = printTree(reg, data.WDAll); err != nil {
		return err
	}
	so["diff_typed"] = typedJSON(reg)
	return nil
}
