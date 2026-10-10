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
// operations change the metadata. A nil diff changes nothing. A diff that does not fit t (a node
// to change or delete that t lacks, a missing anchor) is an LY_EINVAL *ValidationError; t keeps
// the changes made before it. A diff made without DiffOptions.Defaults may leave duplicate
// default values until t is validated again.
func (t *Tree) ApplyDiff(diff *Tree, o ApplyDiffOptions) error {
	lg := &logger{set: t.set}
	if diff == nil {
		return nil
	}
	var mod *schema.Module
	if o.Module != "" {
		if mod = t.set.Implemented(o.Module); mod == nil {
			return lg.done(lg.logErr("LY_EINVAL", "Module \"%s\" not found.", o.Module))
		}
	}
	cache := &dupCache{}
	for _, root := range diff.top.nodes() {
		if mod != nil && nodeModule(diff.set, root) != mod {
			continue // data of another module
		}
		if err := t.applyR(nil, root, o.Callback, cache); err != nil {
			return diffDone(lg, err)
		}
	}
	return nil
}

// noInstErr is LOGERR_NOINST.
func (t *Tree) noInstErr(dn *Node) error {
	return &opError{"LY_EINVAL", fmt.Sprintf("Failed to find node \"%s\" instance in data.", lydPath(t.set, dn, false))}
}

// applyR is lyd_diff_apply_r: the diff node dn applied among the children of parent (nil: the
// top level of t), then its children under the node it matched or created.
func (t *Tree) applyR(parent, dn *Node, cb func(diff, node *Node) error, cache *dupCache) error {
	op, err := t.diffOpOf(dn)
	if err != nil {
		return err
	}
	sib := t.childrenOf(parent)
	var match *Node
	if isUserOrdered(dn.schema) && (op == diffCreate || op == diffReplace) {
		if op == diffReplace {
			if match = t.findMatch(sib, dn, true, cache); match == nil { // moved: there are siblings
				return t.noInstErr(dn)
			}
		} else if match, err = t.dupR(dn, nil, false, insertDefault, dupNoMeta); err != nil {
			return err
		}
		name, _ := anchorNames(dn.schema)
		i := findYangMeta(dn, name)
		if i < 0 {
			return t.metaErr("yang:"+name, dn)
		}
		anchor := dn.meta[i].value.Canonical()
		if err := t.diffInsert(parent, match, anchor); err != nil {
			return err
		}
	} else {
		switch op {
		case diffNone:
			if match = t.findMatch(sib, dn, true, cache); match == nil {
				return t.noInstErr(dn)
			}
			if match.isTerm() { // only the default flag changed
				match.flags = match.flags&^FlagDefault | dn.flags&FlagDefault
			}
		case diffCreate:
			if match, err = t.dupR(dn, nil, false, insertDefault, dupNoMeta); err != nil {
				return err
			}
			if parent != nil {
				err = t.insertChild(parent, match)
			} else {
				err = t.insertSibling(sib.first(), match)
			}
			if err != nil {
				return err
			}
		case diffDelete:
			if match = t.findMatch(sib, dn, true, cache); match == nil {
				return t.noInstErr(dn)
			}
			return freeTree(match) // the whole subtree is gone, nothing to recurse into
		case diffReplace:
			if dn.schema.Kind != schema.Leaf && !isAny(dn.schema) {
				return &opError{"LY_EINVAL", fmt.Sprintf("Operation \"replace\" is invalid for %s node \"%s\".",
					nodetypeStr(dn.schema.Kind), dn.Name())}
			}
			if match = t.findMatch(sib, dn, true, cache); match == nil {
				return t.noInstErr(dn)
			}
			if isAny(dn.schema) { // their values are M5 (U-0043)
				return fmt.Errorf("data: applying a replace of %s %q: %w", nodetypeStr(dn.schema.Kind), dn.Name(), ErrUnsupported)
			}
			if rc := t.changeTerm(match, dn.value.Canonical()); rc != "" && rc != "LY_EEXIST" {
				return &opError{"LY_EINVAL", fmt.Sprintf("Unexpected value of node \"%s\" in data.", lydPath(t.set, match, false))}
			}
			match.flags = dn.flags // with the flags
		}
	}
	if err := t.applyMetadata(match, dn); err != nil {
		return err
	}
	if cb != nil {
		if err := cb(dn, match); err != nil {
			return err
		}
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
func (t *Tree) diffInsert(parent, n *Node, anchor string) error {
	sib := t.childrenOf(parent)
	if sib.len() == 0 {
		if parent == nil {
			t.insert(nil, n, insertDefault)
			return nil
		}
		if anchor != "" {
			return anchorErr(n)
		}
		return t.insertChild(parent, n)
	}
	if !isUserOrdered(n.schema) {
		return t.insertSibling(sib.first(), n)
	}
	if anchor == "" { // before the first instance
		first := t.findSchema(sib, n.schema)
		switch {
		case first == n:
			return argErr("sibling != node", "lyd_insert_before")
		case first != nil:
			unlink(n)
			t.insertBefore(first, n)
			return nil
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
		if at, err = t.findAnchor(sib, n.schema, anchor); err != nil {
			return err
		}
	}
	if at == nil {
		return anchorErr(n)
	}
	return t.insertAfter(at, n)
}

// findAnchor is lyd_find_sibling_val of a leaf-list value or a list key predicate: the instance
// of sn among sib equal to the instance the anchor describes, its value or keys stored through
// their types (lyd_create_term, lyd_create_list2), nil if none. An anchor that does not parse or
// store is an error, as libyang logs it.
func (t *Tree) findAnchor(sib *siblings, sn *schema.Node, anchor string) (*Node, error) {
	var target *Node
	if sn.Kind == schema.LeafList {
		v, d := types.Store(sn.Type, anchor, types.FormatJSON, types.HintData, types.ModuleNames{Set: t.set}, sn)
		if d != nil {
			return nil, &opError{d.RC(), d.Msg}
		}
		target = newTerm(sn, v)
	} else {
		preds, msg := types.CompileKeys(t.set, sn, anchor)
		if msg != "" {
			return nil, &opError{"LY_EVALID", msg}
		}
		if target = t.lookupList(types.PathSegment{Node: sn, Preds: preds}); target == nil { // lyd_find_sibling_val
			return nil, nil
		}
	}
	return t.findFirst(sib, target), nil
}

// changeTerm is lyd_change_term with a JSON value: "" when the value changed, LY_EEXIST when it
// was equal and only the default flag was cleared, LY_ENOT when nothing changed, else the code of
// the failed store.
func (t *Tree) changeTerm(n *Node, val string) string {
	v, d := types.Store(n.schema.Type, val, types.FormatJSON, types.HintData, types.ModuleNames{Set: t.set}, n.schema)
	if d != nil {
		return d.RC()
	}
	switch valChange, dfltChange := t.changeTermVal(n, v, false); {
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

// findMetaNamed is lyd_diff_metadata_find without logging: the metadata of n named "module:name"
// with the canonical value val, nil if none.
func findMetaNamed(n *Node, name, val string) *meta {
	mod, local, _ := strings.Cut(name, ":")
	for _, m := range metasNamed(n, mod, local) {
		if m.value.Canonical() == val {
			return m
		}
	}
	return nil
}

// findMetaVal is findMetaNamed of the diff metadata name of the module yang.
func findMetaVal(n *Node, name, val string) *meta { return findMetaNamed(n, "yang:"+name, val) }

// loggedErr is the first error item a helper logged into lg, as an *opError.
func loggedErr(lg *logger, err error) error {
	for _, d := range lg.diags {
		if failing(d) {
			return &opError{d.Err, d.Msg}
		}
	}
	return err
}

// metaNew is lyd_new_meta(NULL, n, yang, name, val, 0).
func (t *Tree) metaNew(n *Node, name, val string) error {
	lg := &logger{set: t.set}
	if _, err := lg.newMeta(n, nil, "yang:"+name, val, false); err != nil {
		return loggedErr(lg, err)
	}
	return nil
}

// metaChange is lyd_change_meta of m, a metadata instance of n.
func (t *Tree) metaChange(n *Node, m *meta, val string) error {
	lg := &logger{set: t.set}
	if _, err := lg.changeMeta(n, m, val); err != nil {
		return loggedErr(lg, err)
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
func (t *Tree) applyMetadata(n, dn *Node) error {
	var repl, orig []*meta
	for _, m := range dn.meta {
		if m.mod.Name != "yang" {
			continue
		}
		switch m.name {
		case "meta-create":
			name, val, err := splitMetaDiff(m)
			if err != nil {
				return err
			}
			lg := &logger{set: t.set}
			if _, err := lg.newMeta(n, nil, name, val, false); err != nil {
				return loggedErr(lg, err)
			}
		case "meta-delete":
			name, val, err := splitMetaDiff(m)
			if err != nil {
				return err
			}
			m2 := findMetaNamed(n, name, val)
			if m2 == nil {
				return &opError{"LY_EINT", "Internal error."}
			}
			removeMeta(n, m2)
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
		m2 := findMetaNamed(n, name, old)
		if m2 == nil {
			return &opError{"LY_EINT", "Internal error."}
		}
		if err := t.metaChange(n, m2, val); err != nil {
			return err
		}
	}
	if dn.schema.Kind == schema.List {
		for i, k := range dn.kids.list {
			if !k.isKey() {
				break
			}
			if err := t.applyMetadata(n.kids.list[i], k); err != nil {
				return err
			}
		}
	}
	return nil
}
