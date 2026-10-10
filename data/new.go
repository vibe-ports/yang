// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_new.c (_lyd_new_term, lyd_new_term, lyd_new_inner,
// _lyd_new_list_node, lyd_new_list, lyd_new_list2, lyd_new_list3, lyd_create_list2, lyd_new_opaq,
// lyd_new_opaq2, lyd_change_term, lyd_change_term_canon, lyd_new_val_get_format)
// (BSD-3-Clause, © CESNET).

package data

import (
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// newValOptions are the LYD_NEW_VAL_* options of the tree-building functions.
type newValOptions struct {
	output    bool // LYD_NEW_VAL_OUTPUT: the node is in an operation's output
	storeOnly bool // LYD_NEW_VAL_STORE_ONLY: values are stored without their restriction checks
	canon     bool // LYD_NEW_VAL_CANON: values are in their canonical form (LY_VALUE_CANON)
}

// newBuilder holds what the lyd_new_* functions read besides their arguments: the schema set
// (libyang's context) and the log.
type newBuilder struct {
	set *schema.Set
	log *logger
}

// treeFor is the tree whose sibling lists a node is inserted into: the parent's, or a tree of its
// own for a detached parent (insertion only reads the schema set then).
func (b newBuilder) treeFor(parent *Node) *Tree {
	if parent != nil {
		if t := treeOf(parent); t != nil {
			return t
		}
	}
	return newTree(b.set)
}

// valFormat is lyd_new_val_get_format plus the check of the callers that store-only is not
// combined with canonical values; fn names the caller for the argument error.
func (b newBuilder) valFormat(o newValOptions, fn string) (types.Format, error) {
	if o.storeOnly && o.canon {
		return 0, argErr("!(store_only && (format == LY_VALUE_CANON))", fn)
	}
	if o.canon {
		return types.FormatCanon, nil
	}
	return types.FormatJSON, nil
}

// findChild is lys_find_child_node by module and name under parent's schema node (the module's
// top level without a parent or under an opaque one, whose schema is NULL), in an operation's
// output with o.output. Extension-instance children are not searched (U-0060).
func (b newBuilder) findChild(parent *Node, mod *schema.Module, name string, output bool) *schema.Node {
	var sp *schema.Node
	var top []*schema.Node
	if parent != nil && parent.schema != nil {
		sp = parent.schema
	} else {
		top = mod.Top
	}
	var opts schema.GetNextOpt
	if output {
		opts = schema.GetNextOutput
	}
	return schema.FindChild(sp, top, mod, name, opts)
}

// moduleOf is the module of the new node: mod, else the parent's, after the LY_CHECK_ARG_RET of
// the lyd_new_* functions that Go can fail (a parent or a module). A Go string is never NULL, so
// an empty name is a name, as libyang treats "": it is not found.
func (b newBuilder) moduleOf(parent *Node, mod *schema.Module, fn string) (*schema.Module, error) {
	if parent == nil && mod == nil {
		return nil, argErr("parent || module", fn)
	}
	if err := b.sameContext(parent, mod, fn); err != nil {
		return nil, err
	}
	if mod == nil {
		if parent.schema == nil {
			return nil, argErr("parent || module", fn) // libyang reads the opaque parent's NULL schema
		}
		mod = parent.schema.Module
	}
	return mod, nil
}

// sameContext is LY_CHECK_CTX_EQUAL_RET(__func__, parent's context, module's context) against the
// builder's: a parent of another tree's snapshot, or a module of another snapshot, is LOGERR
// LY_EINVAL "Different contexts mixed in a "fn" function call.".
func (b newBuilder) sameContext(parent *Node, mod *schema.Module, fn string) error {
	if parent != nil && !inContext(parent, b.set) || mod != nil && !slices.Contains(b.set.Modules, mod) {
		return b.log.logErr("LY_EINVAL", "Different contexts mixed in a \"%s\" function call.", fn)
	}
	return nil
}

// inContext reports whether n belongs to the context set (LYD_CTX(n) == set), also when n is
// detached: a schema node's module is one of the set's, an opaque node keeps its context.
func inContext(n *Node, set *schema.Set) bool {
	switch {
	case setOf(n) != nil:
		return setOf(n) == set
	case n.schema != nil:
		return slices.Contains(set.Modules, n.schema.Module)
	case n.opaq != nil && n.opaq.set != nil:
		return n.opaq.set == set
	}
	return true // an opaque node built without a context (none in the port)
}

// notFound is the LOGERR(LY_EINVAL) of a node the name does not denote, returned as LY_ENOTFOUND.
func (b newBuilder) notFound(format, name string) error {
	_ = b.log.logErr("LY_EINVAL", format, name)
	return rcError("LY_ENOTFOUND")
}

// createTerm is lyd_create_term with LYD_HINT_DATA, no prefix data and the caller's format; lnode
// is the parent, where a store error is located.
func (b newBuilder) createTerm(lnode *Node, sn *schema.Node, value string, f types.Format, storeOnly bool) (*Node, error) {
	if d := nulDiag(value); d != nil {
		return nil, b.log.storeErr(lnode, sn, d)
	}
	store := types.Store
	if storeOnly {
		store = types.StoreOnly
	}
	v, d := store(sn.Type, value, f, types.HintData, types.ModuleNames{Set: b.set}, sn)
	if d != nil {
		return nil, b.log.storeErr(lnode, sn, d)
	}
	n := newTerm(sn, v)
	n.flags = FlagNew
	return n, nil
}

// nulDiag is the refusal of a value holding a NUL byte (D-0111): libyang's C strings end there, so
// its API never sees the rest; the port refuses the value instead of cutting it, as the string type
// does any 0x0 (RFC 7950 §9.4, XML Char).
func nulDiag(value string) *types.Diag {
	if strings.IndexByte(value, 0) < 0 {
		return nil
	}
	return &types.Diag{Code: types.CodeData, Msg: "Invalid character 0x0."}
}

// newTerm is lyd_new_term: a leaf or leaf-list instance of the module's (else the parent's) node
// name with value, inserted into parent when there is one.
func (b newBuilder) newTerm(parent *Node, mod *schema.Module, name, value string, o newValOptions) (*Node, error) {
	mod, err := b.moduleOf(parent, mod, "_lyd_new_term")
	if err != nil {
		return nil, err
	}
	f, err := b.valFormat(o, "_lyd_new_term")
	if err != nil {
		return nil, err
	}
	sn := b.findChild(parent, mod, name, o.output)
	if sn == nil || sn.Kind != schema.Leaf && sn.Kind != schema.LeafList {
		return nil, b.notFound("Term node \"%s\" not found.", name)
	}
	n, err := b.createTerm(parent, sn, value, f, o.storeOnly)
	if err != nil {
		return nil, err
	}
	if parent != nil {
		b.treeFor(parent).insert(parent, n, insertDefault)
	}
	return n, nil
}

// newInner is lyd_new_inner: a container, notification, RPC or action of the module's (else the
// parent's) node name, inserted into parent when there is one.
func (b newBuilder) newInner(parent *Node, mod *schema.Module, name string, output bool) (*Node, error) {
	mod, err := b.moduleOf(parent, mod, "lyd_new_inner")
	if err != nil {
		return nil, err
	}
	sn := b.findChild(parent, mod, name, output)
	if sn == nil || sn.Kind != schema.Container && sn.Kind != schema.Notification && sn.Kind != schema.RPC &&
		sn.Kind != schema.Action {
		return nil, b.notFound("Inner node (container, notif, RPC, or action) \"%s\" not found.", name)
	}
	n := newInner(sn)
	n.flags = FlagNew
	if isNPCont(sn) {
		n.flags |= FlagDefault // lyd_create_inner
	}
	if parent != nil {
		b.treeFor(parent).insert(parent, n, insertDefault)
	}
	return n, nil
}

// newListNode is _lyd_new_list_node: the list instance without keys.
func (b newBuilder) newListNode(parent *Node, mod *schema.Module, name string, output bool) (*Node, error) {
	sn := b.findChild(parent, mod, name, output)
	if sn == nil || sn.Kind != schema.List {
		return nil, b.notFound("List node \"%s\" not found.", name)
	}
	n := newInner(sn)
	n.flags = FlagNew
	return n, nil
}

// newList is lyd_new_list and lyd_new_list3 (fn names the caller); keys are the key values in key
// order ("" for libyang's NULL, an empty value). As in libyang, values past the list's keys are
// ignored, and so are all of them for a keyless list; nil keys of a keyed list is lyd_new_list3's
// error for a NULL array, and fewer values than keys an argument error, "keys".
func (b newBuilder) newList(parent *Node, mod *schema.Module, name string, keys []string, o newValOptions,
	fn string) (*Node, error) {
	mod, err := b.moduleOf(parent, mod, fn)
	if err != nil {
		return nil, err
	}
	f, err := b.valFormat(o, fn)
	if err != nil {
		return nil, err
	}
	l, err := b.newListNode(parent, mod, name, o.output)
	if err != nil {
		return nil, err
	}
	switch {
	case len(l.schema.Keys) == 0 || len(keys) >= len(l.schema.Keys):
	case keys == nil: // lyd_new_list3's NULL key array
		_ = b.log.logErr("LY_EINVAL", "Missing list \"%s\" keys.", l.Name())
		return nil, rcError("LY_EINVAL")
	default: // libyang reads past the end of its varargs or array (design 07 §6.1.2)
		return nil, argErr("keys", fn)
	}
	t := b.treeFor(parent)
	for i, v := range keys[:len(l.schema.Keys)] { // values past the keys ignored
		k, err := b.createTerm(parent, l.schema.Keys[i], v, f, o.storeOnly) // lnode: the list's parent
		if err != nil {
			return nil, err
		}
		t.insert(l, k, insertLast)
	}
	if parent != nil {
		t.insert(parent, l, insertDefault)
	}
	return l, nil
}

// newList2 is lyd_new_list2: the list instance with the keys of the predicates keys
// ("[key1='value1'][key2='value2']…"), a keyless list without them.
func (b newBuilder) newList2(parent *Node, mod *schema.Module, name, keys string, o newValOptions) (*Node, error) {
	mod, err := b.moduleOf(parent, mod, "lyd_new_list2")
	if err != nil {
		return nil, err
	}
	sn := b.findChild(parent, mod, name, o.output)
	if sn == nil || sn.Kind != schema.List {
		return nil, b.notFound("List node \"%s\" not found.", name)
	}
	t := b.treeFor(parent)
	var l *Node
	if sn.Keyless() && keys == "" {
		l = newInner(sn)
		l.flags = FlagNew
	} else {
		// lyd_create_list2
		if d := nulDiag(keys); d != nil {
			return nil, b.log.storeErr(nil, sn, d)
		}
		preds, d := types.CompileKeysDiag(b.set, sn, keys)
		switch {
		case d != nil && d.Code != "LYVE_XPATH": // a key value's own error items, at the key
			at := sn
			if d.At != nil {
				at = d.At
			}
			return nil, b.log.storeErr(nil, at, d)
		case d != nil: // LOGVAL (LY_EVALID), then ly_path_compile_snode's return code
			_ = b.log.item(nil, sn, false, "LY_EVALID", codeOf(d.Code), "", d.Msg)
			return nil, rcError(d.RC())
		}
		// lyd_create_list: each key from its predicate's canonical text, stored again in the JSON
		// format with the requested store-only option (a union may take another member then); a
		// variable is looked up in no variables (lyxp_vars_find: LOGERR LY_ENOTFOUND)
		l = newInner(sn)
		l.flags = FlagNew
		for _, pr := range preds {
			if pr.Var != "" {
				_ = b.log.logErr("LY_ENOTFOUND", "Variable \"%s\" not defined.", pr.Var)
				return nil, rcError("LY_ENOTFOUND")
			}
			k, err := b.createTerm(nil, pr.Key, pr.Value.Canonical(), types.FormatJSON, o.storeOnly)
			if err != nil {
				return nil, err
			}
			t.insert(l, k, insertDefault)
		}
	}
	if parent != nil {
		t.insert(parent, l, insertDefault)
	}
	return l, nil
}

// newOpaq is lyd_new_opaq (xml false: the JSON module name; the prefix, when given, must be it) and
// lyd_new_opaq2 (xml: the XML namespace): an opaque node appended to parent when there is one.
func (b newBuilder) newOpaq(parent *Node, name, value, prefix, module string, xml bool) (*Node, error) {
	fn := "lyd_new_opaq"
	if xml {
		fn = "lyd_new_opaq2"
	}
	// a Go string is never NULL: name, module_name and module_ns "" are values, as libyang treats
	// ""; prefix "" stands for no prefix (NULL)
	if !xml && prefix != "" && prefix != module {
		return nil, argErr("!prefix || !strcmp(prefix, module_name)", fn)
	}
	if err := b.sameContext(parent, nil, fn); err != nil {
		return nil, err
	}
	if parent != nil && parent.isTerm() {
		// libyang links the node as the child of a leaf, over the leaf's value (D-0113)
		return nil, argErr("parent (a leaf or leaf-list has no children)", fn)
	}
	for _, v := range []string{name, value, prefix, module} {
		if d := nulDiag(v); d != nil { // D-0111: libyang's C strings end at the NUL
			return nil, b.log.storeErr(parent, nil, d)
		}
	}
	o := opaque{Name: name, Prefix: prefix, ModuleNS: module, Value: value, Format: types.FormatJSON, set: b.set}
	if xml {
		o.Format = types.FormatXML
	} else if value == "[null]" {
		o.Hints = types.HintEmpty
	}
	n := newOpaque(o)
	if parent != nil {
		b.treeFor(parent).insert(parent, n, insertLast)
	}
	return n, nil
}

// changeTerm is lyd_change_term (canon: lyd_change_term_canon): the term takes the value, stored
// with LYD_HINT_DATA; nil when the value changed, LY_EEXIST when it was equal but the node was a
// default (now cleared, and on its NP-container parents), LY_ENOT when nothing changed.
func (b newBuilder) changeTerm(n *Node, value string, canon bool) error {
	fn := "lyd_change_term"
	if canon {
		fn = "lyd_change_term_canon"
	}
	switch { // LY_CHECK_ARG_RET names the first argument check that fails
	case n == nil:
		return argErr("term", fn)
	case n.schema == nil:
		return argErr("term->schema", fn)
	case !n.isTerm():
		return argErr("term->schema->nodetype & (0x0004|0x0008)", fn)
	}
	f := types.FormatJSON
	if canon {
		f = types.FormatCanon
	}
	if d := nulDiag(value); d != nil {
		return b.log.storeErr(n, n.schema, d)
	}
	v, d := types.Store(n.schema.Type, value, f, types.HintData, types.ModuleNames{Set: b.set}, n.schema)
	if d != nil {
		return b.log.storeErr(n, n.schema, d)
	}
	switch valChange, dfltChange := b.treeFor(n).changeTermVal(n, v, false); {
	case valChange:
		return nil
	case dfltChange:
		return rcError("LY_EEXIST")
	}
	return rcError("LY_ENOT")
}
