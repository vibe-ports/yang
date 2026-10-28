// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/diff.c (lyd_diff_apply_all, lyd_diff_apply_module,
// lyd_diff_apply_r, lyd_diff_insert, lyd_diff_apply_metadata, lyd_diff_apply_metadata_parse,
// lyd_diff_metadata_find, lyd_diff_meta_store, lyd_diff_metadata_replace_orig_align) and
// src/tree_data_new.c (lyd_change_term) (BSD-3-Clause, © CESNET).

package data

import (
	"fmt"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// ApplyDiffOptions are the options of ApplyDiff.
type ApplyDiffOptions struct {
	// Module, when set, applies only the top-level subtrees of the diff of the implemented
	// module of this name (lyd_diff_apply_module).
	Module string
	// Callback, when set, is called after every diff node is applied, with the data node it
	// created or changed (not for a delete); an error stops the application and is returned
	// (lyd_diff_cb). It must not modify the tree being changed or the diff.
	Callback func(diff, node *Node) error
}

// ApplyDiff is lyd_diff_apply_all (lyd_diff_apply_module with o.Module): the changes of diff, a
// tree annotated with yang:operation as Diff returns it, made to t. A create copies the node
// (user-ordered instances after their yang:key, yang:value or yang:position anchor), a delete
// removes it with its subtree, a replace changes a leaf's value or moves a user-ordered
// instance, a none only descends (and sets a term's default flag); yang:meta-* metadata
// operations change the metadata. A nil diff normally changes nothing. A diff that does not fit
// t (a node to change or delete that t lacks, a missing anchor) is an LY_EINVAL *ValidationError;
// t keeps the changes made before it. A diff made without DiffOptions.Defaults may leave
// duplicate default values until t is validated again. A split operation-child run in t is
// refused with LY_EINVAL before t is changed, including when diff is nil (D-0116). A diff tree
// may itself have such a split because libyang inserts generated diff nodes by their schema
// anchors; it is accepted and walked in its stored order.
func (t *Tree) ApplyDiff(diff *Tree, o ApplyDiffOptions) error {
	var mod *schema.Module
	if o.Module != "" {
		mod = t.context().Implemented(o.Module)
	}
	return t.applyDiffWithModule(diff, o, mod)
}

func (t *Tree) applyDiffWithModule(diff *Tree, o ApplyDiffOptions, mod *schema.Module) (result error) {
	defer t.work.beginHashWork()()
	lg := &logger{set: t.context()}
	if split, op := splitOpRun(t.top.first(), true); split != nil {
		fn := "lyd_diff_apply_all"
		if o.Module != "" {
			fn = "lyd_diff_apply_module"
		}
		return splitOpRefusal(lg, split, op, fn)
	}
	if diff == nil {
		return nil
	}
	if o.Module != "" && mod == nil {
		return lg.done(lg.logErr("LY_EINVAL", "Module \"%s\" not found.", o.Module))
	}
	if err := t.preflightBatchHash(diff); err != nil {
		return err
	}
	journal := beginHashTransaction(t)
	defer journal.end(&result)
	cache := &dupCache{}
	for _, root := range diff.top.nodes() {
		if mod != nil && nodeModule(diff.set, root) != mod {
			continue // data of another module
		}
		if err := t.applyR(nil, root, o.Callback, cache); err != nil {
			if t.work.hashErr != nil {
				return t.work.hashErr
			}
			return diffDone(lg, err)
		}
	}
	return t.work.hashErr
}

// noInstErr is LOGERR_NOINST.
func (t *Tree) noInstErr(dn *Node) error {
	return &opError{"LY_EINVAL", fmt.Sprintf("Failed to find node \"%s\" instance in data.", lydPath(ctxOf(dn), dn, false))}
}

// applyR is lyd_diff_apply_r: the diff node dn applied among the children of parent (nil: the
// top level of t), then its children under the node it matched or created.
func (t *Tree) applyR(parent, dn *Node, cb func(diff, node *Node) error, cache *dupCache) error {
	op, err := t.diffOpOf(dn)
	if err != nil {
		return err
	}
	if dn.schema == nil && (op == diffCreate || op == diffReplace) {
		return &opError{"LY_EINVAL", fmt.Sprintf("Operation \"%s\" is invalid for opaque node \"%s\".", op, dn.Name())}
	}
	sib := t.childrenOf(parent)
	var match *Node
	if isUserOrdered(dn.schema) && (op == diffCreate || op == diffReplace) {
		if op == diffReplace {
			if match, err = t.findMatch(sib, dn, true, cache); err != nil {
				return err
			}
			if match == nil { // moved: there are siblings
				return t.noInstErr(dn)
			}
		}
		name, _ := anchorNames(dn.schema)
		i := findYangLatest(dn, name)
		if i < 0 {
			return t.metaErr("yang:"+name, dn)
		}
		anchor := dn.meta[i].value.Canonical()
		if op == diffCreate {
			if err := t.preflightDiffUserordCreate(parent, dn, anchor); err != nil {
				return err
			}
			if match, err = diffDupCreate(dn); err != nil {
				return err
			}
		}
		insertSource := dn
		if op == diffReplace {
			insertSource = match // moved node already belongs to the destination context
		}
		if err := t.diffInsert(parent, match, insertSource, anchor); err != nil {
			return err
		}
		if op == diffCreate {
			t.created(cache, match)
		} else {
			t.moved(cache, match)
		}
	} else {
		switch op {
		case diffNone:
			if match, err = t.findMatch(sib, dn, true, cache); err != nil {
				return err
			}
			if match == nil {
				return t.noInstErr(dn)
			}
			if match.isTerm() { // only the default flag changed
				if err := match.touch(); err != nil {
					return err
				}
				match.flags = match.flags&^FlagDefault | dn.flags&FlagDefault
			}
		case diffCreate:
			if match, err = diffDupCreate(dn); err != nil {
				return err
			}
			switch {
			case parent != nil:
				err = t.insertChild(parent, match)
			case sib.len() == 0:
				err = t.insertDiffRoot(match, dn)
			default:
				err = t.insertSibling(sib.first(), match)
			}
			if err != nil {
				// lyd_insert_check_schema logs on the inserted node's schema context
				// (libyang v5.8.6 src/tree_data.c:1074-1081), which can differ from t.
				err = &loggedContextError{error: err, set: ctxOf(match)}
				return err
			}
			t.created(cache, match)
		case diffDelete:
			if match, err = t.findMatch(sib, dn, true, cache); err != nil {
				return err
			}
			if match == nil {
				return t.noInstErr(dn)
			}
			return freeTree(match) // the whole subtree is gone, nothing to recurse into
		case diffReplace:
			if dn.schema.Kind != schema.Leaf && !isAny(dn.schema) {
				return &opError{"LY_EINVAL", fmt.Sprintf("Operation \"replace\" is invalid for %s node \"%s\".",
					nodetypeStr(dn.schema.Kind), dn.Name())}
			}
			if match, err = t.findMatch(sib, dn, true, cache); err != nil {
				return err
			}
			if match == nil {
				return t.noInstErr(dn)
			}
			if isAny(dn.schema) { // their values await the public diff surface (U-0112)
				return fmt.Errorf("data: applying a replace of %s %q: %w", nodetypeStr(dn.schema.Kind), dn.Name(), ErrUnsupported)
			}
			if rc := t.changeTerm(match, dn.value.Canonical()); rc != "" && rc != "LY_EEXIST" {
				return &opError{"LY_EINVAL", fmt.Sprintf("Unexpected value of node \"%s\" in data.", lydPath(ctxOf(match), match, false))}
			}
			if err := match.touch(); err != nil {
				return err
			}
			match.flags = dn.flags // with the flags
		}
	}
	if t.work.hashErr != nil {
		return t.work.hashErr
	}
	if err := t.applyMetadata(match, dn, op); err != nil {
		return err
	}
	if cb != nil {
		if err := cb(dn, match); err != nil {
			return err
		}
	}
	// Descendant edits can change the enclosing keyless instance's narrowing
	// digest. Preserve the native consumed-instance sets while rebuilding only
	// digest buckets on the next sibling lookup, as Merge does.
	before, tracked, err := cache.keylessDigest(match, &t.work)
	if err != nil {
		return err
	}
	childCache := &dupCache{}
	for _, c := range dn.kids.nodes() {
		if c.isKey() { // lyd_child_no_keys
			continue
		}
		if err := t.applyR(match, c, cb, childCache); err != nil {
			return err
		}
	}
	return cache.invalidateChangedKeyless(match, before, tracked, &t.work)
}

// diffDupCreate is lyd_dup_single(diff_node, NULL, LYD_DUP_NO_META): the new
// node keeps the diff node's context even when the destination tree is empty.
func diffDupCreate(source *Node) (*Node, error) {
	return newTree(ctxOf(source)).dupR(source, nil, false, insertDefault, dupNoMeta)
}

// insertDiffRoot mirrors assigning *first_node when libyang has no parent or
// siblings. The first node gives an initially empty Tree its effective context.
func (t *Tree) insertDiffRoot(n, source *Node) error {
	if set := ctxOf(source); set != nil {
		t.set = set
	}
	return t.insert(nil, n, insertDefault)
}

// diffSourceInsertCheck preserves lyd_insert_check_schema's pointer identity on a
// cross-context create.
func diffSourceInsertCheck(parent, sibling, source *Node) error {
	var err error
	if parent != nil {
		err = insertCheckSchema(parent.schema, nil, source.schema)
	} else if sibling != nil {
		err = insertCheckSchema(nil, sibling.schema, source.schema)
	}
	if err != nil {
		return &loggedContextError{error: err, set: ctxOf(source)}
	}
	return nil
}

// preflightDiffUserordCreate follows the insertion branch selected by lyd_diff_insert
// while the source still has its original schema and context. An existing anchor
// reaches lyd_insert_before/after (context check first); without one,
// lyd_insert_child/sibling checks the source schema parent pointer.
func (t *Tree) preflightDiffUserordCreate(parent, source *Node, anchor string) error {
	sib := t.childrenOf(parent)
	if sib.len() == 0 {
		if parent != nil && anchor == "" {
			return diffSourceInsertCheck(parent, nil, source)
		}
		return nil
	}
	if anchor == "" {
		if n := t.schemaLookup(sib, source.schema); n != nil {
			if ctxOf(n) != ctxOf(source) {
				return &opError{"LY_EINVAL", "Different contexts mixed in a \"lyd_insert_before\" function call."}
			}
			return nil
		}
		return diffSourceInsertCheck(nil, sib.first(), source)
	}
	var at *Node
	if isDupInstList(source.schema) {
		pos := atoi(anchor)
		if pos == 0 {
			return &opError{"LY_EINVAL", fmt.Sprintf("Invalid user-ordered anchor value \"%s\".", anchor)}
		}
		for n := range t.instances(sib, source.schema) {
			pos--
			if pos == 0 {
				at = n
				break
			}
		}
	} else {
		var err error
		// lyd_diff_insert looks up the anchor using the unconverted duplicate's
		// schema and context, including its value and key types.
		search := t
		if sourceSet := ctxOf(source); sourceSet != nil && sourceSet != t.context() {
			search = newTree(sourceSet)
		}
		if at, err = search.findAnchor(sib, source.schema, anchor); err != nil {
			return err
		}
	}
	if at == nil {
		return anchorErr(source)
	}
	if ctxOf(at) != ctxOf(source) {
		return &opError{"LY_EINVAL", "Different contexts mixed in a \"lyd_insert_after\" function call."}
	}
	return nil
}

// anchorErr is the error of an anchor instance lyd_diff_insert does not find.
func anchorErr(n *Node) error {
	return &opError{"LY_EINVAL", fmt.Sprintf("Node \"%s\" instance to insert next to not found.", n.schema.Name)}
}

// atoi is C atoi: the leading decimal integer of s after spaces, 0 without one.
func atoi(s string) int {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	neg := false
	if s != "" && (s[0] == '-' || s[0] == '+') {
		neg = s[0] == '-'
		s = s[1:]
	}
	v := 0
	for ; s != "" && s[0] >= '0' && s[0] <= '9' && v < 1<<30; s = s[1:] { // stops before int overflows on 32-bit
		v = v*10 + int(s[0]-'0')
	}
	if neg {
		return -v
	}
	return v
}

// diffInsert is lyd_diff_insert: n inserted among the children of parent (nil: the top level of
// t); a user-ordered instance after the instance anchor names (its value, its key predicate, or
// for key-less lists and state leaf-lists its 1-based position), or first without an anchor.
// source is the original diff node, before n's schema was converted to t's snapshot.
func (t *Tree) diffInsert(parent, n, source *Node, anchor string) error {
	sib := t.childrenOf(parent)
	if sib.len() == 0 {
		if parent == nil {
			return t.insertDiffRoot(n, source)
		}
		if anchor != "" {
			return anchorErr(n)
		}
		return t.insertChild(parent, n)
	}
	if !isUserOrdered(n.schema) {
		if err := diffSourceInsertCheck(nil, sib.first(), source); err != nil {
			return err
		}
		return t.insertSibling(sib.first(), n)
	}
	if anchor == "" { // before the first instance
		first := t.schemaLookup(sib, n.schema)
		switch {
		case first == n:
			return argErr("sibling != node", "lyd_insert_before")
		case first != nil:
			if ctxOf(first) != ctxOf(source) {
				return &opError{"LY_EINVAL", "Different contexts mixed in a \"lyd_insert_before\" function call."}
			}
			restore, arm, err := t.linkOrigin(n)
			if err != nil {
				return err
			}
			disarm, err := arm()
			if err != nil {
				return err
			}
			defer disarm()
			if err := unlink(n); err != nil {
				return err
			}
			err = t.insertBefore(first, n)
			if err != nil {
				restore()
			}
			return err
		}
		if err := diffSourceInsertCheck(nil, sib.first(), source); err != nil {
			return err
		}
		return t.insertSibling(sib.first(), n)
	}
	var at *Node
	if isDupInstList(n.schema) {
		pos := atoi(anchor)
		if pos == 0 {
			return &opError{"LY_EINVAL", fmt.Sprintf("Invalid user-ordered anchor value \"%s\".", anchor)}
		}
		i := 1
		for inst := range t.instances(sib, n.schema) {
			if i == pos {
				at = inst
				break
			}
			i++
		}
	} else {
		var err error
		search := newTree(ctxOf(source))
		if at, err = search.findAnchor(sib, n.schema, anchor); err != nil {
			return err
		}
	}
	if at == nil {
		return anchorErr(n)
	}
	if ctxOf(at) != ctxOf(source) {
		return &opError{"LY_EINVAL", "Different contexts mixed in a \"lyd_insert_after\" function call."}
	}
	return t.insertAfter(at, n)
}

// anchorMissing is lyd_diff_insert's missing anchor after lyd_find_sibling_val's LY_ENOTFOUND:
// the item that lookup logged (first, located at at when set), then LY_EINVAL "Node … instance
// to insert next to not found.".
type anchorMissing struct {
	name  string
	first *types.Diag
	at    *schema.Node
}

func (e *anchorMissing) Error() string { return e.first.Msg }

// storeFailure is a type plugin's error for a value of the schema node at, which the call's
// logger reports with its location and nested items (ly_err_print, lyd_create_term without a
// parent).
type storeFailure struct {
	d  *types.Diag
	at *schema.Node
}

func (e *storeFailure) Error() string { return e.d.Msg }

// findAnchor is lyd_find_sibling_val of a leaf-list value or a list key predicate: the instance
// of sn among sib equal to the instance the anchor describes, its value or keys stored through
// their types (lyd_create_term, lyd_create_list2), nil if none. An anchor that does not parse or
// store is an error, as libyang logs it.
func (t *Tree) findAnchor(sib *siblings, sn *schema.Node, anchor string) (*Node, error) {
	var target *Node
	if sn.Kind == schema.LeafList {
		v, d := types.Store(sn.Type, anchor, types.FormatJSON, types.HintData, types.ModuleNames{Set: sn.Module.Owner}, sn)
		if d != nil {
			return nil, &storeFailure{d, sn}
		}
		target = newTerm(sn, v, &t.work)
		if t.work.hashErr != nil {
			return nil, t.work.hashErr
		}
	} else {
		// lyd_find_sibling_val returning LY_ENOTFOUND is a missing anchor to lyd_diff_insert
		// (src/diff.c: "if (ret == LY_ENOTFOUND) { LOGERR(... "instance to insert next to not
		// found." ...); return LY_EINVAL; }"), after what lyd_create_list2 logged: an unknown key
		// or module (ly_path_compile_snode's LY_ENOTFOUND, its LYVE_XPATH item at the list,
		// ut-new/path-12) or a variable lyd_create_list cannot find (lyxp_vars_find without
		// variables: LOGERR LY_ENOTFOUND, ut-new/path-11)
		preds, cd := types.CompileKeysDiag(sn.Module.Owner, sn, anchor)
		switch {
		case cd != nil && cd.RC() == "LY_ENOTFOUND":
			return nil, &anchorMissing{name: sn.Name, first: cd, at: sn}
		case cd != nil: // a key value's own error at the key, a syntax error at the list
			at := sn
			if cd.At != nil {
				at = cd.At
			}
			return nil, &storeFailure{cd, at}
		}
		// lyd_create_list, key by key in libyang's order: a variable it cannot find (LY_ENOTFOUND,
		// a missing anchor, ut-new/path-11) or a key value that does not store again (its error at
		// the key, path-09), whichever key comes first (path-14)
		var d *types.Diag
		var key *schema.Node
		var err error
		if target, d, key, err = t.lookupList(types.PathSegment{Node: sn, Preds: preds}); err != nil {
			return nil, err
		}
		if d != nil {
			if key == nil { // the variable
				return nil, &anchorMissing{name: sn.Name, first: d}
			}
			return nil, &storeFailure{d, key}
		}
	}
	return t.findFirstChecked(sib, target)
}

// changeTerm is lyd_change_term with a JSON value: "" when the value changed, LY_EEXIST when it
// was equal and only the default flag was cleared, LY_ENOT when nothing changed, else the code of
// the failed store.
func (t *Tree) changeTerm(n *Node, val string) string {
	v, d := types.Store(n.schema.Type, val, types.FormatJSON, types.HintData, types.ModuleNames{Set: ctxOf(n)}, n.schema)
	if d != nil {
		return d.RC()
	}
	valChange, dfltChange := t.changeTermVal(n, v, false)
	if t.work.hashErr != nil {
		return "LY_EINVAL"
	}
	switch {
	case valChange:
		return ""
	case dfltChange:
		return "LY_EEXIST"
	}
	return "LY_ENOT"
}

// splitMetaDiff is lyd_diff_apply_metadata_parse: the "module:name" and the value of a diff
// metadata value "module:name=value".
func splitMetaDiff(m *meta) (name, val string, err error) {
	name, val, ok := strings.Cut(m.value.Canonical(), "=")
	if !ok {
		return "", "", &opError{"LY_EINT", "Internal error."}
	}
	return name, val, nil
}

// findMetaNamed is lyd_diff_metadata_find without logging. Each repeated
// lyd_find_meta call resolves the prefix in the context of the first metadata
// in the remaining suffix (diff.c:1466-1485, tree_data.c:2904-2942).
func findMetaNamed(n *Node, name, val string) *meta {
	modName, local, _ := strings.Cut(name, ":")
	for i := 0; i < len(n.meta); {
		set := n.meta[i].mod.Owner
		if set == nil { // hand-built modules have no owner
			set = ctxOf(n)
		}
		mod := latestModule(set, modName)
		match := -1
		for j := i; j < len(n.meta); j++ {
			m := n.meta[j]
			if m.mod == mod && m.name == local {
				match = j
				break
			}
		}
		if match < 0 {
			return nil
		}
		if n.meta[match].value.Canonical() == val {
			return n.meta[match]
		}
		i = match + 1
	}
	return nil
}

// findMetaNamedChecked keeps lyd_find_meta's first diagnostic when a diff
// metadata name is malformed, then returns lyd_diff_metadata_find's LY_EINT.
func findMetaNamedChecked(n *Node, name, val, site string) (*meta, error) {
	set := ctxOf(n)
	if len(n.meta) > 0 && n.meta[0].mod.Owner != nil { // hand-built modules have no owner
		set = n.meta[0].mod.Owner
	}
	lg := &logger{set: set}
	_, err := lg.findMeta(n.meta, nil, name)
	if err != nil {
		items := make([]*opError, 0, len(lg.diags)+1)
		for _, d := range lg.diags {
			items = append(items, &opError{d.Err, d.Msg})
		}
		items = append(items, &opError{"LY_EINT", "Internal error (diff.c:" + site + ")."})
		return nil, &diffError{items: items, rc: "LY_EINT"}
	}
	return findMetaNamed(n, name, val), nil
}

// findMetaVal is findMetaNamed of the diff metadata name of the module yang.
func findMetaVal(n *Node, name, val string) *meta { return findMetaNamed(n, "yang:"+name, val) }

// metaNew is lyd_new_meta(NULL, n, yang, name, val, 0).
func (t *Tree) metaNew(n *Node, name, val string) error {
	lg := &logger{set: ctxOf(n)}
	if _, err := lg.newMeta(n, nil, "yang:"+name, val, false); err != nil {
		return lg.done(err)
	}
	return nil
}

// metaChange is lyd_change_meta of m, a metadata instance of n.
func (t *Tree) metaChange(n *Node, m *meta, val string) error {
	lg := &logger{set: ctxOf(n)}
	if _, err := lg.changeMeta(n, m, val); err != nil {
		return lg.done(err)
	}
	return nil
}

// alignReplaceOrig is lyd_diff_metadata_replace_orig_align: orig reordered so that each
// meta-orig names the same metadata as the meta-replace at its index.
func alignReplaceOrig(repl, orig []*meta) error {
	if len(repl) == 0 {
		return nil
	}
	if len(repl) != len(orig) {
		return &opError{"LY_EINT", "Internal error."}
	}
	for i, r := range repl {
		name, _, ok := strings.Cut(r.value.Canonical(), "=")
		if !ok {
			return &opError{"LY_EINT", "Internal error."}
		}
		j := i
		for j < len(orig) && !strings.HasPrefix(orig[j].value.Canonical(), name+"=") {
			j++
		}
		if j == len(orig) {
			return &opError{"LY_EINT", "Internal error."}
		}
		orig[i], orig[j] = orig[j], orig[i]
	}
	return nil
}

// applyMetadata is lyd_diff_apply_metadata: the yang:meta-* operations of dn made to the
// metadata of n, and those of a list's keys to its keys.
func (t *Tree) applyMetadata(n, dn *Node, op diffOp) error {
	if dn.schema == nil {
		// libyang dereferences match->schema for none before this function and crashes
		// (deviations.md crash cases). Create and replace are rejected earlier; delete never reaches metadata.
		return &opError{"LY_EINVAL", fmt.Sprintf("Operation \"%s\" is invalid for opaque node \"%s\".", op, dn.Name())}
	}
	var repl, orig []*meta
	for _, m := range dn.meta {
		if m.mod != ctxOf(n).Implemented("yang") {
			continue
		}
		switch m.name {
		case "meta-create":
			name, val, err := splitMetaDiff(m)
			if err != nil {
				return err
			}
			lg := &logger{set: ctxOf(n)}
			if _, err := lg.newMeta(n, nil, name, val, false); err != nil {
				return lg.done(err)
			}
		case "meta-delete":
			name, val, err := splitMetaDiff(m)
			if err != nil {
				return err
			}
			m2, err := findMetaNamedChecked(n, name, val, "1601")
			if err != nil {
				return err
			}
			if m2 == nil {
				return &opError{"LY_EINT", "Internal error."}
			}
			if err := removeMeta(n, m2); err != nil {
				return err
			}
		case "meta-replace":
			repl = append(repl, m)
		case "meta-orig":
			orig = append(orig, m)
		}
	}
	if err := alignReplaceOrig(repl, orig); err != nil {
		return err
	}
	if len(repl) != len(orig) {
		return &opError{"LY_EINT", "Internal error."}
	}
	for i, r := range repl {
		name, val, err := splitMetaDiff(r)
		if err != nil {
			return err
		}
		_, old, err := splitMetaDiff(orig[i])
		if err != nil {
			return err
		}
		m2, err := findMetaNamedChecked(n, name, old, "1753")
		if err != nil {
			return err
		}
		if m2 == nil {
			return &opError{"LY_EINT", "Internal error."}
		}
		if err := t.metaChange(n, m2, val); err != nil {
			return err
		}
	}
	if dn.schema.Kind == schema.List {
		for i, k := range dn.kids.SchemaAll() {
			if !k.isKey() {
				break
			}
			if err := t.applyMetadata(n.kids.At(i), k, op); err != nil {
				return err
			}
		}
	}
	return nil
}
