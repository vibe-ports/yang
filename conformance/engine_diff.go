// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
	"github.com/vibe-ports/yang/internal/contextopts"
	"github.com/vibe-ports/yang/internal/snap"
)

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

// runDiff is lyoracle.c op_diff: both datastore inputs parsed as op data parses them,
// lyd_diff_siblings, the diff printed with LYD_PRINT_WD_ALL.
func runDiff(r Request, s *yang.Schema, resp map[string]any, exec executionContext) error {
	p := r.Params
	if err := checkRequest(r, s, p, exec, dataPreludeRules); err != nil {
		return err
	}
	if _, err := loadOperational(r, s, resp); err != nil || resp["verdict"] == "operational-error" {
		return err
	}
	if err := checkRequest(r, s, p, exec, diffRequestRules); err != nil {
		return err
	}
	diags := list(resp["diagnostics"])
	var trees [2]*data.Tree
	failed := false
	for i, key := range []string{"first", "second"} {
		t, d, perr := parseOne(r, s, p, exec, key, key, nil)
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
	if err := requestFlags("diff_options", "defaults meta")(&requestCheck{p: p}); err != nil {
		return err
	}
	o, err := diffOptions(p, "diff_options")
	if err != nil {
		return err
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
		if resp["typed"], err = typedJSON(diff); err != nil {
			return err
		}
	}
	return nil
}

// stepDiff is lyoracle.c step_diff: lyd_diff_siblings (lyd_diff_tree with single) of the tree
// (or its node at "node", or the retained detached node with "detached") and the parsed data (or
// its node at "data_node"); the result replaces the diff register, or with merge is merged into it
// (reported as new_diff). The diagnostics carry their phase (parse, diff).
func stepDiff(r Request, s *yang.Schema, st map[string]any, tree *data.Tree, detached *data.Node, reg **data.Tree,
	regOwner **yang.Schema, so map[string]any, collectPending func(*yang.Schema, string) []yang.Diagnostic,
	logPrint func(*yang.Schema, []yang.Diagnostic)) ([]yang.Diagnostic, error) {
	_, hasData, err := requestInput(r, st, "data")
	if err != nil {
		return nil, err
	}
	o, err := diffOptions(st, "options")
	if err != nil {
		return nil, err
	}
	mo, err := mergeOptions(st, "merge_options")
	if err != nil {
		return nil, err
	}
	first := topNode(tree)
	if st["detached"] == true {
		if detached == nil {
			return nil, requestError("diff retained detached node not found")
		}
		first = detached
	} else if path, ok := st["node"].(string); ok {
		if first, err = r.findNode(tree, path, "diff node"); err != nil {
			return nil, err
		}
	}
	var second *data.Node
	var secondTree *data.Tree
	secondSchema := s
	var diags []yang.Diagnostic
	if hasData {
		// step_diff resolves data_node only after parse_one succeeds.
		if p, ok := st["target"].(map[string]any); ok {
			targetSchema, _, _, verdict, cerr := r.buildContext(Request{BaseDir: r.BaseDir, Params: p, buildContext: r.buildContext})
			if cerr != nil {
				return nil, cerr
			}
			if verdict != "valid" {
				return nil, requestError("diff target context failed")
			}
			secondSchema = targetSchema
		}
		if secondSchema == s {
			_, hasRPC := st["rpc"].(string)
			collectPhase := parseOnePendingPhase(st, hasRPC, "parse")
			diags = append(diags, collectPending(s, collectPhase)...)
		}
		t, d, perr := parseOne(r, secondSchema, st, execStepDiff, "data", "parse", nil)
		diags = append(diags, d...)
		if perr != nil {
			return diags, perr
		}
		second = topNode(t)
		secondTree = t
	}
	// lyoracle.c:2775 resolves data_node even when no data was supplied.
	if path, ok := st["data_node"].(string); ok {
		if second, err = r.findNode(secondTree, path, "diff data_node"); err != nil {
			return nil, err
		}
	}
	// lyoracle.c:2759-2780 permits populated trees from separate contexts.
	// DiffSiblings/DiffTree deliberately refuse this matching (D-0069, #236).
	// Preserve parse and selector failures above, and the supported NULL cases.
	if first != nil && second != nil && snap.Set(contextopts.NodeSchema(first)) != snap.Set(contextopts.NodeSchema(second)) {
		return nil, fmt.Errorf("%w: cross-context diff of populated trees (D-0069)", ErrUnsupported)
	}
	diff := data.DiffSiblings
	if b, _ := st["single"].(bool); b {
		diff = data.DiffTree
	}
	res, err := diff(first, second, o)
	// step_diff collects the retained context after diffing, including when
	// the parsed second tree belonged to a separate target context, then the
	// context of a foreign first input (lyoracle.c:2762).
	diags = append(diags, collectPending(s, "diff")...)
	if first != nil && snap.Set(contextopts.NodeSchema(first)) != snap.Set(s) {
		diags = append(diags, collectPending(contextopts.NodeSchema(first), "diff")...)
	}
	if err != nil {
		return phased(diags, diagsOf(err), "diff"), err
	}
	// A diff node is built in the context of whichever non-NULL input supplied it.
	// The target owns it when the retained tree is empty.
	// A populated diff belongs to the context of its own nodes (a retained detached
	// first input may come from another snapshot).
	resOwner := s
	switch {
	case res != nil && !empty(res):
		resOwner = contextopts.TreeSchema(res)
	case first == nil && second != nil:
		resOwner = secondSchema
	}
	if b, _ := st["merge"].(bool); !b {
		*reg = res
		*regOwner = nil
		if res != nil {
			*regOwner = resOwner
		}
		return diags, nil
	}
	so["new_diff"] = nil
	if res != nil {
		var printed []yang.Diagnostic
		if so["new_diff"], printed, err = printTreeLogged(res, data.WDAll); err != nil {
			return nil, err
		}
		// step_diff prints new_diff before the merge's collect(ctx, ..., "diff").
		// Its printer logs stay in the context of the new diff until collected.
		if snap.Set(resOwner) == snap.Set(s) {
			diags = phased(diags, printed, "diff")
		} else {
			logPrint(resOwner, printed)
		}
	}
	mergeOwner := *regOwner
	if *reg == nil {
		mergeOwner = resOwner
	}
	err = mergeInto(mergeOwner, reg, func(t *data.Tree) error { return t.MergeDiff(res, mo) })
	*regOwner = mergeOwner
	if *reg == nil {
		*regOwner = nil
	}
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
func parseDiff(r Request, s *yang.Schema, st map[string]any, exec executionContext) (*data.Tree, []yang.Diagnostic, error) {
	p := map[string]any{}
	for k, v := range st {
		p[k] = v
	}
	p["parse_only"] = true
	return parseOne(r, s, p, exec, "data", "parse", nil)
}

// stepDiffMerge is lyoracle.c step_diff_merge: the parsed source merged into the register
// (MergeDiff with the module filter, or MergeDiffTree of src_node under parent).
func stepDiffMerge(r Request, s *yang.Schema, st map[string]any, reg **data.Tree, detached *data.Node) ([]yang.Diagnostic, error) {
	o, err := mergeOptions(st, "options")
	if err != nil {
		return nil, err
	}
	o.Module = str(st, "module", "")
	src, d, err := parseDiff(r, s, st, execStepDiffMerge)
	diags := phased(nil, d, "parse")
	if err != nil {
		return diags, err
	}
	var merge func(*data.Tree) error
	if path, ok := st["src_node"].(string); ok {
		// lyoracle.c:2820-2824 resolves parent before src_node.
		var parent *data.Node
		if st["detached_parent"] == true {
			parent = detached
			if parent == nil {
				return nil, requestError("diff_merge retained detached parent not found")
			}
		} else if pp, ok := st["parent"].(string); ok {
			if parent, err = r.findNode(*reg, pp, "diff_merge parent"); err != nil {
				return nil, err
			}
		}
		sn, err := r.findNode(src, path, "diff_merge src_node")
		if err != nil {
			return nil, err
		}
		merge = func(t *data.Tree) error { return t.MergeDiffTree(parent, sn, o) }
		if *reg == nil && parent != nil {
			s = contextopts.NodeSchema(parent)
		}
	} else {
		merge = func(t *data.Tree) error {
			if o.Module != "" && src != nil {
				return contextopts.MergeDiffModule(t, src, s, o.Module, o.Defaults)
			}
			return t.MergeDiff(src, o)
		}
	}
	mergeSchema := s
	if o.Module == "" && src != nil && !empty(src) && (st["detached_parent"] != true || *reg != nil) {
		mergeSchema = contextopts.TreeSchema(src)
	}
	err = mergeInto(mergeSchema, reg, merge)
	mergeDiags := diagsOf(err)
	logged := contextopts.DrainDiffDiagnostics(*reg)
	if len(logged) > 0 {
		// step_diff_merge drains the request context before the retained diff
		// context. lyd_find_meta logs on its metadata context; the merge
		// wrapper logs on the source and LOGERR_META on the target diff.
		var source, target []yang.Diagnostic
		for _, item := range logged {
			if item.Context != nil && snap.Set(item.Context) == snap.Set(s) {
				source = append(source, item.Diagnostic)
			} else {
				target = append(target, item.Diagnostic)
			}
		}
		if len(mergeDiags) >= len(logged) { // withDiffLogs prepends these to the public error
			mergeDiags = mergeDiags[len(logged):]
		}
		for _, d := range mergeDiags {
			if strings.HasPrefix(d.Msg, "Merging operation ") {
				source = append(source, d)
			} else {
				target = append(target, d)
			}
		}
		mergeDiags = slices.Concat(source, target)
	}
	return phased(diags, mergeDiags, "diff"), err
}

// findNode mirrors lyoracle's lyd_find_path(tree, path, 0, ...): operation paths
// resolve against input, including when the retained tree contains reply output.
// Empty paths and a NULL tree fail as request-errors; Find's diagnostics are not
// collected by lyoracle.
func (r Request) findNode(t *data.Tree, path, label string) (*data.Node, error) {
	if t != nil && path != "" && r.selectorEvaluated != nil {
		r.selectorEvaluated(r.selectorStep, label)
	}
	return findNode(t, path, label)
}

func findNode(t *data.Tree, path, label string) (*data.Node, error) {
	if t != nil && path != "" {
		if n, err := t.Find(path); err == nil && n != nil {
			return n, nil
		}
	}
	return nil, requestError("%s %s not found", label, path)
}

// diffOut is the diff register of a diff step as lyoracle.c reports it: printed and dumped.
func diffOut(reg *data.Tree, so map[string]any, logPrint func([]yang.Diagnostic)) error {
	so["diff"], so["diff_typed"] = nil, []any{}
	if reg == nil {
		return nil
	}
	var err error
	var printed []yang.Diagnostic
	if so["diff"], printed, err = printTreeLogged(reg, data.WDAll); err != nil {
		return err
	}
	logPrint(printed)
	so["diff_typed"], err = typedJSON(reg)
	return err
}
