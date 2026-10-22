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
	"github.com/vibe-ports/yang/internal/contextopts"
	"github.com/vibe-ports/yang/internal/snap"
)

// seqKeys are the fields of each sequence step kind runSequence handles (lyoracle.c check_step).
var seqKeys = map[string]map[string]bool{
	"parse": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options", "rpc", "keep_input", "parent"),
	"validate": set("do", "data_type", "validate_options", "module", "final_only"),
	"edit": set("do", "set", "delete", "new_meta", "free_meta", "insert_term", "insert_inner", "insert_list", "insert_list2", "insert_opaq", "merge_self", "merge",
		"merge_file", "merge_into", "format", "data_type", "unknown", "parse_options"),
	"dup":         set("do", "node", "node_xpath", "parent", "options", "siblings", "target", "source", "detached"),
	"link":        set("do"),
	"links":       set("do", "detached"),
	"change_term": set("do", "node", "node_xpath", "child", "value", "canon"),
	"insert": set("do", "node", "parent", "format", "data_type", "data", "data_file", "unknown", "parse_only",
		"parse_options", "validate_options", "detached", "target"),
	"dump":          set("do", "with_defaults"),
	"detached_dump": set("do", "with_defaults", "child"),
	"print_xml":     set("do"),
	"any_direct":    set("do", "case", "target"),
	"compare": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options", "first", "second", "options", "insert_opaq", "self", "target"),
	"diff": set("do", "format", "data_type", "data", "data_file", "unknown", "parse_only", "parse_options",
		"validate_options", "rpc", "keep_input", "node", "data_node", "single", "options", "merge", "merge_options", "target", "detached"),
	"diff_parse":   set("do", "format", "data_type", "data", "data_file", "unknown", "parse_options"),
	"diff_merge":   set("do", "format", "data_type", "data", "data_file", "unknown", "parse_options", "options", "module", "src_node", "parent", "detached_parent"),
	"diff_apply":   set("do", "module"),
	"diff_reverse": set("do"),
	"trim":         set("do", "xpath", "vars"),
	"xpath":        set("do", "xpath", "vars", "detached", "node"),
	"unlink":       set("do", "node", "diff_node"),
	"find_meta":    set("do", "node", "name"),
}

var seqContextKeys = set("searchdirs", "modules", "context_options")

// seqUnsupported are the oracle's step kinds and edits package data has no public API for.
var seqUnsupported = map[string]string{
	"insert_opaq": "(lyd_new_opaq / lyd_new_opaq2 are exported in M5 with the envelopes, U-0107)",
	"any_direct":  "(native pointer-level any-value calls have no public Go equivalent, U-0112)",
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
	var tree, diff *data.Tree       // the retained tree and the diff register
	var diffOwner *yang.Schema      // the context that owns the retained diff, when non-NULL
	var detached *data.Node         // the node retained by unlink for find_meta
	var detachedSchema *yang.Schema // the retained node's snapshot, even while tree is empty
	out := []any{}
	rc, failed := "LY_SUCCESS", any(nil)
	requestSchema := s
	var cur *yang.Schema // recomputed from the retained first node before each step
	// ly_err_first/ly_err_clean belong to a context. Printers and links can log after
	// (or without) a collect(), so keep those items until that context is collected.
	// Keyed by the compiled set: schema handles are fresh wrappers per call.
	pending := map[any][]yang.Diagnostic{}
	pk := func(x *yang.Schema) any {
		if x == nil {
			return nil
		}
		return snap.Set(x)
	}
	for i, st := range steps {
		// lyoracle.c op_sequence: sctx = tree ? LYD_CTX(tree) : ctx.
		// ApplyDiff may replace the first root (or the last root of A with
		// one from B) without updating the context chosen by a prior step.
		cur = requestSchema
		if tree != nil && !empty(tree) {
			cur = contextopts.TreeSchema(tree)
		} else {
			tree = nil
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
			diags     []yang.Diagnostic
			postPrint []yang.Diagnostic
			err       error
		)
		stepExec := stepExecutionContexts[what]
		if err := checkRequest(r, s, st, stepExec, stepExecutionRules[what]); err != nil {
			return err
		}
		// Check selectors in the order the oracle reaches them, before a capability
		// refusal for the same step. Skipped steps never reach these checks.
		switch what {
		case "edit":
			err = checkInsertSelectors(s, tree, detached, st)
		case "change_term":
			if _, xpath, _ := strField(st, "node_xpath"); !xpath { // node_xpath: stepChange selects
				_, err = findNode(tree, str(st, "node", ""), "change_term node")
			}
		case "dup":
			_, xpath, _ := strField(st, "node_xpath")
			switch {
			case st["detached"] == true:
				if detached == nil {
					err = requestError("dup retained detached node not found")
				}
			case st["source"] == nil && !xpath: // node_xpath or a source tree: stepDup selects
				_, err = findNode(tree, str(st, "node", ""), "dup node")
			}
			if err == nil {
				if p, present, _ := strField(st, "parent"); present {
					_, err = findNode(tree, p, "dup parent")
				}
			}
			if err == nil && seqOptionUnsupported(what, st) != "" {
				if tg, ok := st["target"].(map[string]any); ok {
					_, _, _, verdict, buildErr := r.buildContext(Request{ID: r.ID, BaseDir: r.BaseDir, Params: tg, buildContext: r.buildContext})
					if buildErr != nil {
						err = buildErr
					} else if verdict != "valid" {
						err = requestError("dup target context failed")
					}
				}
			}
		}
		if err != nil {
			return err
		}
		// A skipped step never needs an API capability (lyoracle.c:2935-2939).
		if why := seqOptionUnsupported(what, st); why != "" {
			return fmt.Errorf("%w: %s", ErrUnsupported, why)
		}
		for _, k := range slices.Sorted(maps.Keys(st)) { // the first refusal must not depend on map order
			if why, bad := seqUnsupported[k]; bad && (what == "edit" || what == "compare") {
				return fmt.Errorf("%w: %s %s %s", ErrUnsupported, what, k, why)
			}
		}
		if what == "validate" && st["final_only"] == true {
			return fmt.Errorf("%w: lyd_validate_module_final is not exported (split-validation speed path), U-0106", ErrUnsupported)
		}
		phase := what
		// lyoracle.c collects collectOwner (the step's sctx unless noted) and then
		// the foreign contexts the step touched: a source node's, the diff
		// register's, a detached parent's.
		collectOwner := s
		var foreign []*yang.Schema
		switch what {
		case "parse":
			phase = "" // parseOne distinguishes the request's "rpc" phase from "parse"
			tree, diags, err = stepParse(r, s, st, stepExec, tree)
		case "validate":
			tree, diags, err = stepValidate(s, st, tree, so, func(ds []yang.Diagnostic) {
				postPrint = append(postPrint, ds...)
			})
		case "edit":
			tree, diags, rc, err = stepEdit(r, s, st, tree, detached, detachedSchema)
		case "dup":
			phase = ""               // source parse and duplication collect distinct diagnostic phases
			if st["source"] == nil { // a source tree is parsed in the step's own context
				src := detached // lyoracle.c:2107-2111
				if st["detached"] != true {
					src, _, _ = dupSelect(r, s, st, tree)
				}
				foreign = append(foreign, contextopts.NodeSchema(src))
			}
			tree, _, diags, err = stepDup(r, s, st, tree, detached)
		case "link":
			if tree == nil || empty(tree) {
				rc = "LY_EINVAL" // LY_CHECK_ARG_RET(NULL, tree): logged without a context
				break
			}
			if diags, err = tree.LinkLeafrefs(context.Background()); err != nil {
				diags = diagsOf(err)
			}
		case "links":
			if st["detached"] == true {
				so["leafref_links"] = linksJSONNode(detached)
			} else {
				so["leafref_links"] = linksJSON(tree)
			}
		case "change_term":
			phase = "edit"
			diags, err = stepChange(st, tree, so)
		case "insert":
			var usedDetached bool
			if st["detached"] == true && detached != nil {
				foreign = append(foreign, contextopts.NodeSchema(detached)) // lyoracle.c:2386
			}
			moveSchema := s
			if st["detached"] == true && (tree == nil || empty(tree)) && detachedSchema != nil {
				moveSchema = detachedSchema
			}
			tree, diags, phase, usedDetached, err = stepMove(r, moveSchema, st, tree, detached)
			if usedDetached && err == nil {
				detached, detachedSchema = nil, nil
			}
		case "compare":
			phase = "parse"
			diags, err = stepCompare(r, s, st, tree, so)
		case "diff":
			phase = "" // the diagnostics carry theirs
			diags, err = stepDiff(r, s, st, tree, detached, &diff, &diffOwner, so, func(owner *yang.Schema, collectPhase string) []yang.Diagnostic {
				prior := phased(nil, pending[pk(owner)], collectPhase)
				delete(pending, pk(owner))
				return prior
			}, func(owner *yang.Schema, ds []yang.Diagnostic) {
				pending[pk(owner)] = append(pending[pk(owner)], ds...)
			})
		case "diff_parse":
			phase = "parse"
			var d *data.Tree
			if d, diags, err = parseDiff(r, s, st, stepExec); err == nil {
				diff = d
				if empty(diff) {
					diff = nil
				}
				diffOwner = nil
				if diff != nil {
					diffOwner = s
				}
			}
		case "diff_merge":
			phase = ""
			foreign = append(foreign, diffOwner) // lyoracle.c:2801
			if st["detached_parent"] == true && detached != nil {
				foreign = append(foreign, contextopts.NodeSchema(detached))
			}
			hadDiff := diff != nil
			diags, err = stepDiffMerge(r, s, st, &diff, detached)
			if diff == nil {
				diffOwner = nil
			} else if !hadDiff {
				// a detached parent or a populated source decides the new register's context
				diffOwner = contextopts.TreeSchema(diff)
			}
		case "diff_apply":
			phase = "diff"
			foreign = append(foreign, diffOwner) // lyoracle.c:3013
			if tree == nil || empty(tree) {
				applySchema := s
				if str(st, "module", "") == "" && diff != nil && !empty(diff) {
					applySchema = contextopts.TreeSchema(diff)
				}
				if tree, err = emptyTree(applySchema); err != nil {
					return err
				}
			}
			if name := str(st, "module", ""); name != "" && diff != nil {
				err = contextopts.ApplyDiffModule(tree, diff, s, name)
			} else {
				err = tree.ApplyDiff(diff, data.ApplyDiffOptions{Module: str(st, "module", "")})
			}
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
		case "xpath":
			phase = "xpath"
			so["result"] = nil
			if tree == nil || empty(tree) {
				rc = "LY_EINVAL" // lyd_eval_xpath4 needs a context node or tree, checked before logging.
				break
			}
			node := topNode(tree) // the retained tree's first node is the default context
			if path, present, _ := strField(st, "node"); present {
				if st["detached"] == true {
					return requestError("xpath node and detached are mutually exclusive")
				}
				if path == "@last" {
					for n := range tree.Top() {
						node = n
					}
				} else {
					if node, err = findNode(tree, path, "xpath node"); err != nil {
						break
					}
				}
			}
			if st["detached"] == true {
				if detached == nil {
					return requestError("xpath retained detached node not found")
				}
				node = detached
			}
			o := data.XPathOptions{Node: node}
			if o.Vars, err = xpathVars(st); err != nil {
				return err
			}
			var result data.XPathResult
			result, diags, err = tree.EvalXPath(str(st, "xpath", ""), o)
			if err == nil {
				so["result"] = xpathResultJSON(result)
			}
		case "unlink":
			phase = "edit"
			diffPath, diffPresent, _ := strField(st, "diff_node")
			path := str(st, "node", "")
			if diffPresent && (diff == nil || empty(diff)) {
				return requestError("unlink diff_node %s not found", diffPath)
			}
			if !diffPresent && (tree == nil || empty(tree)) {
				return requestError("unlink node %s not found", path)
			}
			var selected *data.Node
			switch {
			case diffPresent:
				selected, err = findNode(diff, diffPath, "unlink diff_node")
			case path == "@top":
				selected = topNode(tree)
			default:
				selected, err = findNode(tree, path, "unlink node")
			}
			if err == nil && selected != nil {
				collectOwner = contextopts.NodeSchema(selected) // lyoracle.c:2994 collects only node_ctx
				err = selected.Unlink()
				if err == nil {
					if detached != nil {
						_ = detached.Remove() // the oracle frees the previous retained node on success
					}
					detached = selected
					detachedSchema = contextopts.NodeSchema(detached)
				}
			}
			diags = diagsOf(err)
		case "find_meta":
			phase = "find_meta"
			n := detached
			if _, present, _ := strField(st, "node"); present {
				if n, err = findNode(tree, str(st, "node", ""), "find_meta node"); err != nil {
					break
				}
			}
			if n == nil {
				return requestError("find_meta needs an attached node or a retained unlinked node")
			}
			name := str(st, "name", "")
			if !strings.Contains(name, ":") {
				// lyd_find_meta's argument check uses a NULL logging context
				// (tree_data.c:2913), so the harness collects no diagnostic and observes
				// its unchanged LY_SUCCESS instead of a return value.
				so["found"] = false
				break
			}
			collectOwner = contextopts.NodeSchema(n) // lyoracle.c:3009
			var m *data.Meta
			m, err = n.FindMeta(name)
			diags = diagsOf(err)
			so["found"] = m != nil
		case "diff_reverse":
			phase = "diff"
			foreign = append(foreign, diffOwner) // lyoracle.c:2899
			diff, err = diff.ReverseDiff()
			diags = diagsOf(err)
			if diff == nil {
				diffOwner = nil
			}
		case "dump":
			wd, werr := wdOf(st)
			if werr != nil {
				return werr
			}
			var logged []yang.Diagnostic
			if so["tree"], logged, err = printTreeLogged(tree, wd); err != nil {
				return err
			}
			pending[pk(s)] = append(pending[pk(s)], logged...)
		case "print_xml":
			phase = "data" // lyoracle collects printer diagnostics as data diagnostics
			var j, x strings.Builder
			if tree == nil || empty(tree) {
				so["tree"] = map[string]any{"json": "{}\n", "xml": ""}
				break
			}
			po := data.PrintOptions{WithDefaults: data.WDExplicit}
			jsonErr := tree.PrintJSON(&j, po)
			err = tree.PrintXML(&x, po)
			// xml_print_meta can log LY_EVALID after completing XML. A terminal
			// type-printer failure returns LY_EINVAL and this explicit step drops
			// its partial output, unlike ordinary print_tree calls.
			rc, rcErr := rcOf(err)
			if rcErr != nil {
				return rcErr
			}
			if err == nil || rc == "LY_EVALID" && x.Len() != 0 {
				so["tree"] = map[string]any{"json": j.String(), "xml": x.String()}
			} else {
				so["tree"] = nil
			}
			diags = append(diagsOf(jsonErr), diagsOf(err)...)
		case "any_direct":
			phase = "edit"
			if str(st, "case", "") == "equal_repeated_collisions" && s.Implemented("ac-r6") == nil {
				// lyd_new_list(NULL, ac, ...) fails before this case reads target
				// (lyoracle.c:2225-2232). The direct counters are still emitted.
				so["direct"] = map[string]any{"repeated_keys": 2, "collision_shaped_key": "Aa/BB"}
				rc = "LY_EINVAL"
				break
			}
			if c := str(st, "case", ""); c == "dup_siblings_into_empty_anydata" || c == "dup_siblings_into_empty_anyxml" {
				return fmt.Errorf("%w: opaque source construction has no public Go equivalent, U-0107", ErrUnsupported)
			}
			return fmt.Errorf("%w: native pointer-level any-value calls have no public Go equivalent, U-0112", ErrUnsupported)
		case "detached_dump":
			phase = "dump"
			if detached == nil {
				return requestError("detached_dump retained detached node not found")
			}
			probe := detached
			if st["child"] == true {
				probe = nil
				for c := range detached.Children() {
					probe = c
					break
				}
				if probe == nil {
					return requestError("detached_dump retained detached node not found")
				}
			}
			wd, werr := wdOf(st)
			if werr != nil {
				return werr
			}
			collectOwner = contextopts.NodeSchema(probe) // lyoracle.c:3036 collects only the probe's context
			so["detached_path"] = probe.Path()
			so["detached_tree"], diags, err = contextopts.PrintNode(probe, uint8(wd))
			if err != nil {
				return unsupported(err)
			}
		}
		// step_edit and step_change_term collect the first tree context. A
		// diagnostic emitted on a retained foreign node stays in that context's
		// queue until a later collect of it. free_meta is a void C call.
		if what == "edit" && st["free_meta"] != nil || what == "change_term" {
			path := str(st, "node", "")
			if what == "edit" {
				x, _ := st["free_meta"].(map[string]any)
				path = str(x, "node", "")
			}
			if tree != nil {
				if n, ferr := tree.Find(path); ferr == nil && n != nil {
					owner := contextopts.NodeSchema(n)
					if owner != nil && snap.Set(owner) != snap.Set(s) {
						pending[pk(owner)] = append(pending[pk(owner)], diags...)
						diags = nil
						if what == "edit" {
							err = nil
						}
					}
				}
			}
		}
		if err != nil {
			if rc, err = rcOf(err); err != nil {
				return err
			}
		}
		// Each branch that calls collect() consumes prior logs from the retained
		// context before its own diagnostics. links and dump do not collect.
		if what != "links" && what != "dump" && what != "diff" {
			prior := pending[pk(collectOwner)]
			if phase == "" {
				collectPhase := "parse"
				_, hasData, _ := requestInput(r, st, "data")
				if what == "insert" && !hasData {
					collectPhase = "edit"
				}
				typ := opTypes[str(st, "data_type", "data-operational")]
				if what == "parse" && st["parent"] == nil && (typ == data.OpRPC || typ == data.OpReply) && st["rpc"] != nil {
					collectPhase = "rpc"
				}
				prior = phased(nil, prior, collectPhase)
			}
			diags = append(prior, diags...)
			delete(pending, pk(collectOwner))
			// ponytail: foreign queues go after the step's own diagnostics; a step
			// logging into both contexts would interleave differently in C.
			fphase := phase
			switch {
			case fphase == "" && what == "dup":
				fphase = "edit" // the duplication's own collection
			case fphase == "":
				fphase = "diff" // diff_merge
			}
			for _, f := range foreign {
				if k := pk(f); k != nil && k != pk(collectOwner) {
					diags = append(diags, phased(nil, pending[k], fphase)...)
					delete(pending, k)
				}
			}
		}
		pending[pk(s)] = append(pending[pk(s)], postPrint...)
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
			if so["typed"], err = typedJSON(tree); err != nil {
				return err
			}
		}
		if strings.HasPrefix(what, "diff") {
			if err := diffOut(diff, so, func(ds []yang.Diagnostic) {
				pending[pk(diffOwner)] = append(pending[pk(diffOwner)], ds...)
			}); err != nil {
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
// in a second tree parsed like a parse step, or the retained tree with self:true
// (selectors omitted: the first top-level node).
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
	otherSchema := s
	if tg, ok := st["target"].(map[string]any); ok {
		targetSchema, _, _, verdict, err := r.buildContext(Request{ID: r.ID, BaseDir: r.BaseDir, Params: tg, buildContext: r.buildContext})
		if err != nil {
			return nil, err
		}
		if verdict != "valid" {
			return nil, requestError("compare target context failed")
		}
		otherSchema = targetSchema
	}
	other := tree
	var diags []yang.Diagnostic
	if st["self"] != true {
		var err error
		other, diags, err = stepParse(r, otherSchema, st, execStepCompare, nil)
		if err != nil || other == nil {
			return diags, err
		}
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
	if err == nil {
		if rerr := discardTree(tree); rerr != nil {
			return tree, diags, rerr
		}
		tree = t
	}
	return tree, diags, err
}

// discardTree is lyd_free_all for a retained Go tree: removing every root clears all reciprocal
// leafref links, including records kept on a separately retained detached subtree.
func discardTree(tree *data.Tree) error {
	return contextopts.DiscardTree(tree)
}

// stepValidate is step_validate: lyd_validate_all or lyd_validate_module with the step's flags
// and its implicit diff.
func stepValidate(s *yang.Schema, st map[string]any, tree *data.Tree, so map[string]any,
	logPrint func([]yang.Diagnostic)) (*data.Tree, []yang.Diagnostic, error) {
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
			if !printerDiagnostic(perr) {
				return nil, nil, unsupported(perr)
			}
			logPrint(diagsOf(perr))
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
func stepEdit(r Request, s *yang.Schema, st map[string]any, tree *data.Tree, detached *data.Node, detachedSchema *yang.Schema) (*data.Tree, []yang.Diagnostic, string,
	error) {
	rc := "LY_SUCCESS"
	var err error
	if tree == nil {
		if tree, err = emptyTree(s); err != nil {
			return nil, nil, rc, err
		}
	}
	switch {
	case st["merge_self"] == true:
		src := data.NewTree(s)
		if n := topNode(tree); n != nil {
			_, _, err = src.DupSiblings(n, nil, data.DupOptions{Recursive: true})
			if err == nil {
				err = tree.Merge(src)
			}
		}
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
		n, ferr := findNode(tree, path, "metadata node")
		if ferr != nil {
			return nil, nil, rc, ferr
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
		err = stepInsert(s, st, tree, detached, detachedSchema)
	case st["delete"] != nil:
		path := str(st, "delete", "")
		if empty(tree) {
			return tree, nil, "LY_ENOTFOUND", nil // the oracle does not search a NULL tree
		}
		// lyoracle.c:2644 calls lyd_find_path from the retained first node.
		var n *data.Node
		if n, err = tree.Find(path); err == nil {
			if n == nil {
				return tree, nil, notFoundRC(tree, path), nil
			}
			err = n.Remove()
		}
	case st["merge_into"] != nil:
		o, oerr := dataOptions(st, execStepEdit)
		if oerr != nil {
			return nil, nil, rc, oerr
		}
		o.ParseOnly = true
		f, ferr := formatOf(st)
		if ferr != nil {
			return nil, nil, rc, ferr
		}
		in, present, ierr := requestInput(r, st, "merge_into")
		if ierr != nil {
			return nil, nil, rc, ierr
		}
		dest, diags, perr := data.Parse(context.Background(), inputReader(in, present), f, s, o)
		if perr != nil {
			return tree, diags, rc, perr
		}
		if err = dest.Merge(tree); err != nil {
			return tree, append(diags, diagsOf(err)...), rc, err
		}
		return dest, diags, rc, nil
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
	diags := diagsOf(err)
	if detachedSchema != nil && snap.Set(s) != snap.Set(detachedSchema) {
		for _, kind := range []string{"insert_term", "insert_inner", "insert_list"} {
			o, _ := st[kind].(map[string]any)
			if o != nil && o["detached_parent"] == true && str(o, "module", "") != "" &&
				len(diags) == 1 && strings.HasPrefix(diags[0].Msg, "Different contexts mixed in a \"_lyd_new_") {
				// lyoracle.c: step_edit collects sctx, while _lyd_new_* logs this
				// context mismatch on the detached parent's foreign context.
				return tree, nil, rc, err
			}
		}
	}
	return tree, diags, rc, err
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
func stepInsert(s *yang.Schema, st map[string]any, tree *data.Tree, detached *data.Node, detachedSchema *yang.Schema) error {
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
		var err error
		if parent, err = findNode(tree, p, "insert parent"); err != nil {
			return err
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
	if o["detached_parent"] == true {
		if detached == nil {
			return requestError("insert retained detached parent not found")
		}
		parent = detached
		// libyang v5.8.6 src/tree_data_new.c (_lyd_new_term, lyd_new_inner,
		// lyd_new_list3) resolves an omitted module from parent->schema->module;
		// its constructor uses LYD_CTX(parent) even after the last root was unlinked.
		if str(o, "module", "") == "" && detachedSchema != nil {
			tree = data.NewTree(detachedSchema)
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

// dupSelect is step_dup's node selection without "detached": the node at "node" or the one
// node "node_xpath" selects, in the retained tree or, with "source", in a tree parsed from it.
func dupSelect(r Request, s *yang.Schema, st map[string]any, tree *data.Tree) (*data.Node, []yang.Diagnostic, error) {
	src := tree
	var parseDiags []yang.Diagnostic
	var err error
	if source, ok := st["source"].(map[string]any); ok {
		src, parseDiags, err = parseOne(r, s, source, execStepParse, "data", "parse", nil)
		if err != nil {
			return nil, parseDiags, err
		}
	}
	if xpath, ok := st["node_xpath"].(string); ok {
		if src == nil {
			return nil, nil, requestError("dup node_xpath %s must select exactly one node", xpath)
		}
		nodes, _, err := src.FindXPath(xpath, data.XPathOptions{})
		if err != nil || len(nodes) != 1 {
			return nil, nil, requestError("dup node_xpath %s must select exactly one node", xpath)
		}
		return nodes[0], parseDiags, nil
	}
	n, err := findNode(src, str(st, "node", ""), "dup node")
	if err != nil && st["source"] != nil {
		return nil, nil, requestError("dup node %s not found in the source", str(st, "node", ""))
	}
	return n, parseDiags, err
}

// stepDup is step_dup: Tree.Dup (DupSiblings with "siblings") of the node at "node", or the node
// retained by unlink with "detached", under the node at "parent"; without a parent the copy,
// from its top duplicated parent, replaces the tree.
// With "target" (searchdirs, modules, context_options) the copy goes into a tree over a second
// context built like the request's (lyd_dup_*_to_ctx).
func stepDup(r Request, s *yang.Schema, st map[string]any, tree *data.Tree, detached *data.Node) (*data.Tree, *yang.Schema, []yang.Diagnostic,
	error) {
	n := detached
	var parseDiags []yang.Diagnostic
	var err error
	if st["detached"] == true {
		if n == nil {
			return nil, nil, nil, requestError("dup retained detached node not found")
		}
	} else if n, parseDiags, err = dupSelect(r, s, st, tree); err != nil {
		return tree, s, parseDiags, err
	}
	var parent *data.Node
	if p, present, _ := strField(st, "parent"); present {
		if parent, err = findNode(tree, p, "dup parent"); err != nil {
			return nil, nil, nil, err
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
	if sourceSchema := contextopts.NodeSchema(n); sourceSchema != nil && st["target"] == nil {
		ds = sourceSchema
	}
	if parent == nil {
		dst = data.NewTree(ds)
	}
	if st["target"] == nil {
		if err := contextopts.CheckDupContexts(n, parent); err != nil {
			return tree, s, phased(nil, diagsOf(err), "edit"), err // lyoracle.c collects it in phase edit
		}
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
		return tree, s, append(parseDiags, phased(nil, diagsOf(err), "edit")...), err
	}
	if dst != tree {
		if err := discardTree(tree); err != nil {
			return tree, s, append(parseDiags, phased(nil, diags, "edit")...), err
		}
	}
	return dst, ds, append(parseDiags, phased(nil, diags, "edit")...), nil
}

// stepChange is the change_term step: Node.SetValue of the node at "node"; "change" is
// lyd_change_term's rc, where LY_EEXIST (only the default flag cleared) and LY_ENOT (no change)
// are results. Node.SetValue tells changed from not, so the two are told apart by the node's
// Default flag before the call. libyang's argument checks log without a context: no diagnostics.
func stepChange(st map[string]any, tree *data.Tree, so map[string]any) ([]yang.Diagnostic, error) {
	var n *data.Node
	if expr, present, _ := strField(st, "node_xpath"); present {
		if tree == nil || empty(tree) {
			return nil, requestError("change_term node_xpath %s must select exactly one node", expr)
		}
		nodes, _, err := tree.FindXPath(expr, data.XPathOptions{Node: topNode(tree)})
		if err != nil || len(nodes) != 1 {
			return nil, requestError("change_term node_xpath %s must select exactly one node", expr)
		}
		n = nodes[0]
	} else {
		var err error
		if n, err = findNode(tree, str(st, "node", ""), "change_term node"); err != nil {
			return nil, err
		}
	}
	if index, _ := changeChildIndex(st); index > 0 {
		var selected *data.Node
		for child := range n.Children() {
			index--
			if index == 0 {
				selected = child
				break
			}
		}
		if selected == nil {
			return nil, requestError("change_term child does not exist")
		}
		n = selected
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
	if tree == nil {
		return []any{}
	}
	var roots []*data.Node
	for top := range tree.Top() {
		roots = append(roots, top)
	}
	return linksJSONRoots(roots)
}

func linksJSONNode(root *data.Node) []any {
	if root == nil {
		return []any{}
	}
	return linksJSONRoots([]*data.Node{root})
}

func linksJSONRoots(roots []*data.Node) []any {
	out := []any{}
	paths := func(ns []*data.Node) []any {
		a := []any{}
		for _, n := range ns {
			a = append(a, n.Path())
		}
		return a
	}
	for _, top := range roots {
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

// stepMove is step_insert: Tree.Insert of the node at "node", or the node retained by unlink with
// "detached", under the node at "parent" ("@top": the first top-level node, also an opaque one),
// or at the top level without one. With "data" the node
// comes from a second tree parsed as by a parse step
// ("node" a path in it, omitted: its first top-level node, which moves with its siblings); what is
// left of that tree is then freed, as the oracle frees it (lyd_free_all: link records to it go).
// The parse's diagnostics (phase parse, its warnings too) come before the insert's (phase edit).
// lyd_insert_child's and lyd_insert_sibling's argument checks log without a context: those items
// are not reported.
func stepMove(r Request, s *yang.Schema, st map[string]any, tree *data.Tree, detached *data.Node) (*data.Tree, []yang.Diagnostic, string, bool,
	error) {
	var err error
	if tree == nil {
		if tree, err = emptyTree(s); err != nil {
			return nil, nil, "", false, err
		}
	} else if st["detached"] == true && empty(tree) && detached != nil && snap.Set(s) != snap.Set(contextopts.TreeSchema(tree)) {
		if tree, err = emptyTree(s); err != nil {
			return nil, nil, "", false, err
		}
	}
	src, path := tree, str(st, "node", "")
	useDetached := st["detached"] == true
	var diags []yang.Diagnostic
	_, hasData, err := requestInput(r, st, "data")
	if err != nil {
		return tree, nil, "", false, err
	}
	if hasData {
		otherSchema := s
		if tg, ok := st["target"].(map[string]any); ok {
			var verdict string
			otherSchema, _, _, verdict, err = r.buildContext(Request{ID: r.ID, BaseDir: r.BaseDir, Params: tg, buildContext: r.buildContext})
			if err != nil {
				return tree, nil, "", false, err
			}
			if verdict != "valid" {
				return tree, nil, "", false, requestError("insert target context failed")
			}
		}
		if src, diags, err = stepParse(r, otherSchema, st, execStepInsert, nil); err != nil {
			return tree, diags, "parse", false, err
		}
		for i := range diags {
			diags[i].Phase = "parse"
		}
	}
	n := detached
	if !useDetached {
		n, _ = selectedNode(src, st, "node", "insert node")
	}
	if n == nil {
		if useDetached {
			return nil, nil, "", false, requestError("insert retained detached node not found")
		}
		if hasData {
			return nil, nil, "", false, requestError("insert node %s not found in the data", path)
		}
		return nil, nil, "", false, requestError("insert node %s not found", path)
	}
	var parent *data.Node
	if p, present, _ := strField(st, "parent"); present && p == "@top" && topNode(tree) != nil {
		parent = topNode(tree) // the first top-level node, also an opaque one
	} else if present {
		if parent, err = findNode(tree, p, "insert parent"); err != nil {
			return nil, nil, "", false, err
		}
	}
	if parent != nil && snap.Set(contextopts.NodeSchema(parent)) != snap.Set(contextopts.NodeSchema(n)) {
		err = contextopts.CheckInsertSchema(parent, n)
	}
	if err == nil {
		err = tree.Insert(parent, n)
	}
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
				return nil, nil, "", false, rerr
			}
		}
	}
	return tree, append(diags, ins...), "", useDetached, err
}

// checkInsertSelectors is step_edit's module and parent lookup before construction.
func checkInsertSelectors(s *yang.Schema, tree *data.Tree, detached *data.Node, st map[string]any) error {
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
		if o["detached_parent"] == true {
			if detached == nil {
				return requestError("insert retained detached parent not found")
			}
			parent = detached
		}
		if kind == "insert_opaq" && parent != nil && isTerm(parent) {
			return requestError("insert_opaq under the leaf %s: libyang links it over the leaf's value", parent.Name())
		}
		if kind != "insert_opaq" && o["module"] == nil && (parent == nil || parent.Schema() == nil) {
			return requestError("insert %s under an opaque parent needs a module (libyang reads its NULL schema)", o["name"])
		}
		if kind == "insert_list" {
			if err := checkInsertListKeys(s, parent, o); err != nil {
				return err
			}
		}
	}
	return nil
}

// lyd_new_list3 reads every schema key from the supplied array. The oracle
// rejects a short array before calling it, even when its options need an API
// capability the engine cannot provide.
func checkInsertListKeys(s *yang.Schema, parent *data.Node, o map[string]any) error {
	keys, present := o["keys"].([]any)
	if !present {
		return nil
	}
	var sn *yang.SchemaNode
	if parent != nil && parent.Schema() != nil {
		ps := parent.Schema()
		if ps.Kind() == yang.KindRPC || ps.Kind() == yang.KindAction {
			kind := yang.KindInput
			if slices.Contains(list(o["options"]), any("output")) {
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
	return nil
}
