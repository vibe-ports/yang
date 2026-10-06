// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_common.c (lyd_parser_create_meta,
// lyd_parser_set_data_flags) and src/tree_data.c (lyd_get_meta_annotation, lyd_create_meta,
// lyd_create_attr) (BSD-3-Clause, © CESNET).

package data

import (
	"errors"

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
		return errLoggedFatal
	}
	store := types.Store
	if lc.opts.storeOnly {
		store = types.StoreOnly
	}
	v, d := store(ant.Type, lex, f, h, pc, ctxNode)
	if d != nil {
		return lc.log.item(lnode, ctxNode, false, "LY_EVALID", codeOf(d.Code), d.AppTag, d.Msg)
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

// errNot is libyang's LY_ENOT inside the data parsers: the input does not fit the schema node
// (parse it as opaque, or report its representation). Never logged.
var errNot = errors.New("data: LY_ENOT")

// isAny reports whether sn is an anydata or anyxml node (LYD_NODE_ANY).
func isAny(sn *schema.Node) bool { return sn.Kind == schema.AnyData || sn.Kind == schema.AnyXML }
