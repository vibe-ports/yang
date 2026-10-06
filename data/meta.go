// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_common.c (lyd_parser_create_meta,
// lyd_parser_set_data_flags), src/tree_data.c (lyd_get_meta_annotation, lyd_create_meta,
// lyd_create_attr, lyd_compare_meta, lyd_find_meta), src/tree_data_new.c (lyd_new_meta,
// lyd_new_attr, lyd_change_meta) and src/ly_common.c (ly_parse_nodeid) (BSD-3-Clause, © CESNET).

package data

import (
	"errors"
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// meta is one metadata instance of a node (lyd_meta): the annotation's module, its name and the
// stored value. The parsers fill it; the printers (design 07 D7) read it.
type meta struct {
	mod   *schema.Module
	name  string
	value types.Value
}

// attr is a generic attribute of an opaque node (lyd_attr): the metadata the parsers could not
// resolve, kept as text like the opaque node itself.
type attr struct {
	Name     string
	Prefix   string // as written in the input
	ModuleNS string // XML namespace, JSON module name
	Value    string
	Format   types.Format
	Prefixes types.PrefixCtx
	Hints    types.Hints
}

// Node hints of an opaque node (LYD_NODEHINT_*), next to the value hints in opaque.Hints: what
// the input looked like.
const (
	hintList      types.Hints = 0x1000 // LYD_NODEHINT_LIST: an instance in an array of objects
	hintLeafList  types.Hints = 0x2000 // LYD_NODEHINT_LEAFLIST: an instance in an array of values
	hintContainer types.Hints = 0x4000 // LYD_NODEHINT_CONTAINER: an object
)

// annotation is lyd_get_meta_annotation: the md:annotation extension instance of mod named name.
func annotation(mod *schema.Module, name string) *schema.ExtInstance {
	if mod == nil {
		return nil
	}
	for _, e := range mod.Exts {
		if e.Def != nil && e.Def.Name == "ietf-yang-metadata" && e.Name == "annotation" && e.Argument == name {
			return e
		}
	}
	return nil
}

// createMeta is lyd_parser_create_meta + lyd_create_meta: the metadata name of module mod with the
// value lex, appended to *list (the metadata of parent, or a list the XML parser attaches to the
// node it creates next). An unknown annotation is LY_EINVAL (errLoggedFatal); a rejected value is
// logged at lnode plus "/@mod:name". A value that still needs the data tree is queued unless
// LYD_PARSE_ONLY.
func (lc *lydCtx) createMeta(parent *Node, list *[]*meta, mod *schema.Module, name, lex string, f types.Format,
	pc types.PrefixCtx, h types.Hints, ctxNode *schema.Node, lnode *Node) error {
	lc.log.pushPath("/@" + mod.Name + ":" + name)
	defer lc.log.popPath()
	ant := annotation(mod, name)
	if ant == nil {
		if parent == nil && ctxNode != nil {
			lc.log.locSet(ctxNode)
			defer lc.log.locBack(1)
		}
		_ = lc.log.val(parent, "", ly.Reference, "Annotation definition for attribute \"%s:%s\" not found.", mod.Name, name)
		return fatalRC("LY_EINVAL")
	}
	store := types.Store
	if lc.opts.storeOnly {
		store = types.StoreOnly
	}
	v, d := store(ant.Type, lex, f, h, pc, ctxNode)
	if d != nil {
		return lc.log.item(lnode, ctxNode, false, d.RC(), codeOf(d.Code), d.AppTag, d.Msg)
	}
	m := &meta{mod: mod, name: name, value: v}
	*list = append(*list, m)
	if v.NeedsTree() && !lc.opts.ParseOnly {
		lc.metaTypes = append(lc.metaTypes, m)
	}
	return nil
}

// setDataFlags is lyd_parser_set_data_flags: LYD_NEW cleared for LYD_PARSE_NO_NEW, a node with a
// when queued (post-order: after its children) and seeded WhenTrue for LYD_PARSE_WHEN_TRUE, and
// the first default="true" metadata of ietf-netconf-with-defaults (or a module named "default")
// in *metas turned into FlagDefault and removed. libyang queues the node again on every call (the
// JSON parser calls it once more per metadata object), so this does too.
func (lc *lydCtx) setDataFlags(n *Node, metas *[]*meta) {
	if lc.opts.noNew {
		n.flags &^= FlagNew
	}
	if hasWhen(n.schema) {
		if lc.opts.whenTrue {
			n.flags |= FlagWhenTrue
		}
		if !lc.opts.ParseOnly {
			lc.nodeWhen.add(n)
		}
	}
	for i, m := range *metas {
		if m.name == "default" && (m.mod.Name == "default" || m.mod.Name == "ietf-netconf-with-defaults") &&
			m.value.Bool() {
			n.flags |= FlagDefault
			*metas = append((*metas)[:i:i], (*metas)[i+1:]...)
			npContDfltSet(n.parent)
			break
		}
	}
}

// createAttr is lyd_create_attr: a generic attribute appended to the opaque node n.
func createAttr(n *Node, a attr) { n.opaq.Attrs = append(n.opaq.Attrs, a) }

// errMetaArg is LY_CHECK_ARG_RET of the metadata API: a call libyang refuses before looking at
// the data (no module for an unprefixed name, an attribute on a schema node).
var errMetaArg = errors.New("data: invalid metadata argument")

// parseNodeID is ly_parse_nodeid over all of s ([prefix:]name). shown is what libyang prints as
// the name when s is not valid: from the name's start to the end of s, "(null)" when the
// identifier does not even start.
func parseNodeID(s string) (prefix, name string, hasPrefix bool, shown string, ok bool) {
	isStart := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
	ident := func(i int) (int, bool) { // lys_parse_id
		if i >= len(s) || !isStart(s[i]) {
			return i, false
		}
		i++
		for i < len(s) && (isStart(s[i]) || s[i] >= '0' && s[i] <= '9' || s[i] == '-' || s[i] == '.') {
			i++
		}
		return i, true
	}
	end, ok := ident(0)
	if !ok {
		return "", "", false, "(null)", false
	}
	start := 0
	if end < len(s) && s[end] == ':' {
		prefix, hasPrefix, start = s[:end], true, end+1
		if end, ok = ident(start); !ok {
			return "", "", false, s[start:], false
		}
	}
	if end < len(s) {
		return "", "", false, s[start:], false // trailing characters
	}
	return prefix, s[start:end], hasPrefix, "", true
}

// newMeta is lyd_new_meta: the metadata name ("prefix:name" with the implemented module of that
// name, else of mod) with the JSON value val, appended to the metadata of parent (nil: a detached
// instance). clearDflt is LYD_NEW_META_CLEAR_DFLT. Errors are logged as libyang does.
func (l *logger) newMeta(parent *Node, mod *schema.Module, name, val string, clearDflt bool) (*meta, error) {
	if mod == nil && !strings.Contains(name, ":") {
		return nil, errMetaArg
	}
	if parent != nil && parent.schema == nil {
		return nil, l.logErr("LY_EINVAL", "Cannot add metadata \"%s\" to an opaque node \"%s\".", name, parent.Name())
	}
	prefix, local, hasPrefix, shown, ok := parseNodeID(name)
	if !ok {
		return nil, l.logErr("LY_EINVAL", "Metadata name \"%s\" is not valid.", shown)
	}
	if hasPrefix {
		if mod = l.set.Implemented(prefix); mod == nil {
			return nil, l.logErr("LY_EINVAL", "Module \"%s\" not found.", prefix) // LY_ENOTFOUND
		}
	}
	var ctxNode *schema.Node
	if parent != nil {
		ctxNode = parent.schema
	}
	m, err := l.storeMeta(parent, mod, local, val, ctxNode)
	if err != nil {
		return nil, err
	}
	if parent != nil {
		parent.meta = append(parent.meta, m) // lyd_insert_meta
		if clearDflt {
			npContDfltDel(parent)
		}
	}
	return m, nil
}

// storeMeta is lyd_create_meta of the API: the annotation name of mod, the JSON value val stored
// with ctxNode as the context node; an unknown annotation is logged at parent.
func (l *logger) storeMeta(parent *Node, mod *schema.Module, name, val string, ctxNode *schema.Node) (*meta, error) {
	ant := annotation(mod, name)
	if ant == nil {
		return nil, l.val(parent, "", ly.Reference, "Annotation definition for attribute \"%s:%s\" not found.", mod.Name, name)
	}
	v, d := types.Store(ant.Type, val, types.FormatJSON, types.HintData, types.ModuleNames{Set: l.set}, ctxNode)
	if d != nil {
		return nil, l.item(nil, ctxNode, false, d.RC(), codeOf(d.Code), d.AppTag, d.Msg) // ly_err_print
	}
	return &meta{mod: mod, name: name, value: v}, nil
}

// changeMeta is lyd_change_meta: m, a metadata instance of parent, gets the JSON value val;
// changed false (libyang LY_ENOT) when the new value equals the old one.
func (l *logger) changeMeta(parent *Node, m *meta, val string) (changed bool, err error) {
	var ctxNode *schema.Node
	if parent != nil {
		ctxNode = parent.schema
	}
	m2, err := l.storeMeta(nil, m.mod, m.name, val, ctxNode)
	if err != nil {
		return false, err
	}
	if compareMeta(m, m2) {
		return false, nil
	}
	m.value = m2.value
	return true, nil
}

// compareMeta is lyd_compare_meta: equal when both are nil, or instances of the same annotation
// with values the type compares equal.
func compareMeta(a, b *meta) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.mod == b.mod && a.name == b.name && types.Equal(a.value, b.value)
}

// findMeta is lyd_find_meta: the first of metas that is the annotation name ("prefix:name" with
// the latest module of that name, else of mod); nil if none (an invalid name or unknown module is
// logged).
func (l *logger) findMeta(metas []*meta, mod *schema.Module, name string) (*meta, error) {
	if mod == nil && !strings.Contains(name, ":") {
		return nil, errMetaArg
	}
	if len(metas) == 0 {
		return nil, nil
	}
	prefix, local, hasPrefix, shown, ok := parseNodeID(name)
	if !ok {
		return nil, l.logErr("LY_EINVAL", "Metadata name \"%s\" is not valid.", shown)
	}
	if hasPrefix {
		if mod = l.set.Module(prefix, ""); mod == nil {
			return nil, l.logErr("LY_EINVAL", "Module \"%s\" not found.", prefix)
		}
	}
	for _, m := range metas {
		if m.mod == mod && m.name == local {
			return m, nil
		}
	}
	return nil, nil
}

// newAttr is lyd_new_attr: the attribute name ("[prefix:]name"; "xml:..." is a name) with the
// value val appended to the opaque node parent, in the JSON format with module moduleName (""
// for the prefix). The new attribute is the last of parent's.
func (l *logger) newAttr(parent *Node, moduleName, name, val string) error {
	if parent == nil || parent.schema != nil {
		return errMetaArg
	}
	prefix, local, _, shown, ok := parseNodeID(name)
	if !ok {
		return l.logErr("LY_EINVAL", "Attribute name \"%s\" is not valid.", shown) // LY_EVALID
	}
	if prefix == "xml" {
		prefix, local = "", "xml:"+local // not a prefix but a special name
	}
	if moduleName == "" {
		moduleName = prefix
	}
	createAttr(parent, attr{Name: local, Prefix: prefix, ModuleNS: moduleName, Value: val, Format: types.FormatJSON,
		Hints: types.HintData})
	return nil
}

// errNot is libyang's LY_ENOT inside the data parsers: the input does not fit the schema node
// (parse it as opaque, or report its representation). Never logged.
var errNot = errors.New("data: LY_ENOT")

// isAny reports whether sn is an anydata or anyxml node (LYD_NODE_ANY).
func isAny(sn *schema.Node) bool { return sn.Kind == schema.AnyData || sn.Kind == schema.AnyXML }
