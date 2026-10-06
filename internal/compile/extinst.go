// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile.c (lys_compile_ext, COMPILE_EXTS_GOTO),
// src/tree_schema.c (lysp_resolve_ext_instance_records, parse callback loop),
// src/plugins_exts.c (lyplg_ext_parse_extension_instance, lyplg_ext_compile_extension_instance),
// src/plugins_exts/metadata.c and src/plugins_exts/nacm.c (BSD-3-Clause, © CESNET).

package compile

import (
	"errors"
	"fmt"
	"slices"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// extPlugin is a ported extension plugin record (lyplg_ext_record): the plugin of extension
// name defined in module@revision.
type extPlugin struct{ module, revision, name, id string }

const metadataID = "ly2 metadata"

func (p *extPlugin) parse(c *Context, x *extParse) error {
	if p.id == metadataID {
		return annotationParse(c, x)
	}
	return nacmParse(c, x)
}

func (p *extPlugin) compile(w *nodeCtx, e *parser.Stmt, inst *schema.ExtInstance, parent *schema.Node) error {
	if p.id == metadataID {
		return annotationCompile(w, e, inst)
	}
	return nacmCompile(inst, parent)
}

// extPlugins are the ported built-in plugins (plugins.c: plugins_metadata, plugins_nacm); the
// unported ones fail the load (unsupportedPlugins).
var extPlugins = []extPlugin{
	{"ietf-yang-metadata", "2016-08-05", "annotation", metadataID},
	{"ietf-netconf-acm", "2012-02-22", "default-deny-write", "ly2 NACM"},
	{"ietf-netconf-acm", "2018-02-14", "default-deny-write", "ly2 NACM"},
	{"ietf-netconf-acm", "2012-02-22", "default-deny-all", "ly2 NACM"},
	{"ietf-netconf-acm", "2018-02-14", "default-deny-all", "ly2 NACM"},
}

// pluginOf is lyplg_ext_plugin_find for extension name defined in m.
func pluginOf(m *Module, name string) *extPlugin {
	for i := range extPlugins {
		p := &extPlugins[i]
		if m != nil && p.module == m.Name && p.revision == m.Revision && p.name == name {
			return p
		}
	}
	return nil
}

// errNot is LY_ENOT from a plugin: the instance is dropped.
var errNot = rc("LY_ENOT")

// extParse is one instance handed to a plugin parse callback.
type extParse struct {
	e, parentStmt *parser.Stmt   // the instance and the statement it is written in (ext->parent_stmt)
	arr           []*parser.Stmt // the exts array it is in (its owner's)
	pm            *pmod          // the (sub)module it is written in
	main          *Module
	root          bool // parentStmt is the module or submodule statement
	v11           bool
	path          string
	plugin        *extPlugin
}

// log is lyplg_ext_parse_log: "Ext plugin" messages with LYVE_OTHER at the instance path; an
// error carries LY_EPLUGIN.
func (c *Context) extLog(x *extParse, warning bool, format string, a ...any) error {
	d := Diagnostic{Phase: "parse", Level: LevelError, Err: "LY_EPLUGIN|LY_EVALID", Code: ly.Other, SchemaPath: x.path,
		Msg: fmt.Sprintf("Ext plugin \"%s\": ", x.plugin.id) + fmt.Sprintf(format, a...)}
	if warning {
		d.Level, d.Err = LevelWarning, "LY_SUCCESS"
	}
	c.diags = append(c.diags, d)
	return eValid
}

// parseExtPlugins is the second loop of lysp_resolve_ext_instance_records: the parse callback
// of every instance with a ported plugin, per exts array in ctx->ext_inst order. An instance the
// callback rejects with LY_ENOT is removed, the last one of its array taking its place.
func (c *Context) parseExtPlugins(p *pctx, pms []*pmod) error {
	for _, pm := range pms {
		parent := map[*parser.Stmt]*parser.Stmt{}
		var link func(s *parser.Stmt)
		link = func(s *parser.Stmt) {
			for _, ch := range s.Subs {
				parent[ch] = s
				link(ch)
			}
		}
		link(pm.Parsed.Stmt)
		for _, owner := range parser.ExtOwners(pm.Parsed.Stmt) {
			arr := parser.OwnedExts(owner)
			changed := false
			for u := 0; u < len(arr); {
				e := arr[u]
				plg := pluginOf(c.prefixModule(pm, p.main, e.ExtPrefix), e.Keyword)
				if plg == nil {
					u++
					continue
				}
				x := &extParse{e: e, parentStmt: parent[e], arr: arr, pm: pm, main: p.main, root: parent[parent[e]] == nil, v11: pm.v11(),
					path: extPath(p.main.Name, parent, e), plugin: plg}
				switch err := plg.parse(c, x); {
				case errors.Is(err, errNot):
					arr[u] = arr[len(arr)-1]
					arr = arr[:len(arr)-1]
					changed = true
				case err != nil:
					return err
				default:
					u++
				}
			}
			if changed {
				if c.extArrs == nil {
					c.extArrs = map[*parser.Stmt][]*parser.Stmt{}
				}
				c.extArrs[owner] = arr
			}
		}
	}
	return nil
}

// ownedExts is the exts array of owner after the parse callbacks; removed reports that the array
// exists although every instance was removed (libyang keeps the empty array, so the compiled node
// gets an empty, not a NULL, exts array).
func (c *Context) ownedExts(owner *parser.Stmt) (exts []*parser.Stmt, exists bool) {
	if owner == nil {
		return nil, false
	}
	if arr, ok := c.extArrs[owner]; ok {
		return arr, true
	}
	arr := parser.OwnedExts(owner)
	return arr, len(arr) > 0
}

// stmtStr is lyplg_ext_stmt2str of a parent statement.
func stmtStr(s *parser.Stmt) string {
	if s.ExtPrefix != "" {
		return "extension instance"
	}
	return s.Keyword
}

// annotationSubs are the substatements metadata.c annotation_parse allows, in its substmts order.
var annotationSubs = []parser.ExtSubstmt{{Keyword: "if-feature", Many: true}, {Keyword: "units"},
	{Keyword: "status"}, {Keyword: "type"}, {Keyword: "description"}, {Keyword: "reference"}}

// annotationParse is metadata.c annotation_parse with lyplg_ext_parse_extension_instance; the
// parsed substatements are kept for the compile (lysp_ext_instance.parsed).
func annotationParse(c *Context, x *extParse) error {
	name := x.e.ExtPrefix + ":" + x.e.Keyword
	if !x.root {
		return c.extLog(x, false, "Extension %s is allowed only at the top level of a YANG module or submodule, "+
			"but it is placed in \"%s\" statement.", name, stmtStr(x.parentStmt))
	}
	for _, o := range x.arr {
		if o != x.e && o.ExtPrefix == x.e.ExtPrefix && o.Keyword == x.e.Keyword && o.Arg == x.e.Arg {
			return c.extLog(x, false, "Extension %s is instantiated multiple times.", name)
		}
	}
	n, perr := parser.ParseExtInstance(x.e, annotationSubs, x.v11)
	if perr != nil {
		return c.logPath(perr.Code, x.path, "%s", perr.Msg)
	}
	if n.Type == nil {
		return c.extLog(x, false, "Missing mandatory keyword \"type\" as a child of \"%s %s\".", name, x.e.Arg)
	}
	if c.extParsed == nil {
		c.extParsed = map[*parser.Stmt]*parser.Node{}
	}
	c.extParsed[x.e] = n
	return nil
}

// annotationCompile is metadata.c annotation_compile with lyplg_ext_compile_extension_instance:
// a false if-feature drops the instance, else the type is compiled (lys_compile_type with the
// annotation's status and "annotation" as the referring name) and held.
func annotationCompile(w *nodeCtx, e *parser.Stmt, inst *schema.ExtInstance) error {
	n := w.c.extParsed[e]
	if n == nil || n.Type == nil {
		return fmt.Errorf("compile: annotation %q was not parsed", e.Arg) // annotationParse ran at load
	}
	if on, err := w.iffeatures(w.pm, n.IfFeatures); err != nil || !on {
		if err == nil {
			err = errNot
		}
		return err
	}
	w.tc.pmod = w.pm
	t, _, _, err := w.tc.compileType(nil, parsedStatus(n.Status), "annotation", n.Type, w.pm, true, false)
	if err != nil {
		return w.vlog(err)
	}
	w.tc.cache.hold(t)
	inst.Type = t
	return nil
}

// nacmParse is nacm.c nacm_parse.
func nacmParse(c *Context, x *extParse) error {
	name := x.e.ExtPrefix + ":" + x.e.Keyword
	ps := x.parentStmt
	if ps.ExtPrefix != "" || !nodeStmt[ps.Keyword] {
		return c.nacmIgnore(x, "Extension %s is allowed only in a data nodes, but it is placed in \"%s\" statement.",
			name, stmtStr(ps))
	}
	switch ps.Keyword {
	case "container", "leaf", "leaf-list", "list", "choice", "anydata", "anyxml", "case": // LYS_ANYDATA covers anyxml
	case "rpc", "action", "notification":
		if x.e.Keyword != "default-deny-write" {
			break
		}
		fallthrough
	default:
		return c.nacmIgnore(x, "Extension %s is not allowed in %s statement.", name, nodetype2str(ps.Keyword))
	}
	// the exts array of the parent node is x.arr
	for _, o := range x.arr {
		if o == x.e || o.ExtPrefix == "" {
			continue
		}
		if op := pluginOf(c.prefixModule(x.pm, x.main, o.ExtPrefix), o.Keyword); op == nil || op.id != x.plugin.id {
			continue
		}
		if o.ExtPrefix == x.e.ExtPrefix && o.Keyword == x.e.Keyword {
			return c.extLog(x, false, "Extension %s is instantiated multiple times.", name)
		}
		return c.extLog(x, false, "Extension nacm:default-deny-write is mixed with nacm:default-deny-all.")
	}
	return nil
}

func (c *Context) nacmIgnore(x *extParse, format string, a ...any) error {
	_ = c.extLog(x, true, format, a...)
	return errNot
}

// nodeStmt is LY_STMT_NODE_MASK by keyword.
var nodeStmt = map[string]bool{"notification": true, "input": true, "output": true, "action": true, "rpc": true,
	"anydata": true, "anyxml": true, "augment": true, "case": true, "choice": true, "container": true,
	"grouping": true, "leaf": true, "leaf-list": true, "list": true, "uses": true}

// nodetype2str is lys_nodetype2str of a parsed node keyword.
func nodetype2str(kw string) string {
	switch kw {
	case "rpc":
		return "RPC"
	case "input", "output", "augment", "grouping":
		return "unknown"
	}
	return kw
}

// nacmCompile is nacm.c nacm_compile: the instance is inherited by every node of the parent's
// subtree (actions and notifications included, input and output skipped) down to a node that
// has its own instance of the same extension.
func nacmCompile(inst *schema.ExtInstance, parent *schema.Node) error {
	if parent == nil {
		return nil // nacmParse drops instances outside nodes
	}
	same := func(x *schema.ExtInstance) bool { return x.Def == inst.Def && x.Name == inst.Name }
	stack := []*schema.Node{parent}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n != parent && n.Kind != schema.Input && n.Kind != schema.Output {
			if slices.ContainsFunc(n.Exts, same) {
				continue // the child has its own: skip its subtree
			}
			n.Exts = append(n.Exts, &schema.ExtInstance{Def: inst.Def, Name: inst.Name, Argument: inst.Argument})
		}
		// lysc_tree_dfs_full order: the node, its actions, notifications, then its children
		for _, l := range [][]*schema.Node{n.Children, n.Notifs, n.Actions} {
			for i := len(l) - 1; i >= 0; i-- {
				stack = append(stack, l[i])
			}
		}
	}
	return nil
}

// compileExts is COMPILE_EXTS_GOTO over the exts array of owner into exts: lys_compile_ext for
// each instance. parent is the compiled node owning the instances (nil for the module or another
// instance). An instance its plugin drops (LY_ENOT) is not added.
func (w *nodeCtx) compileExts(owner *parser.Stmt, parent *schema.Node, exts []*schema.ExtInstance) ([]*schema.ExtInstance, error) {
	arr, exists := w.c.ownedExts(owner)
	if exists && exts == nil {
		exts = []*schema.ExtInstance{}
	}
	for _, e := range arr {
		inst, err := w.compileExt(e, owner, parent)
		switch {
		case errors.Is(err, errNot):
		case err != nil:
			return exts, err
		default:
			exts = append(exts, inst)
		}
	}
	return exts, nil
}

// compileExt is lys_compile_ext.
func (w *nodeCtx) compileExt(e, owner *parser.Stmt, parent *schema.Node) (*schema.ExtInstance, error) {
	var pmod *schema.Module
	if parent != nil && slices.Contains(owner.Subs, e) { // ext->parent_stmt & LY_STMT_NODE_MASK
		pmod = parent.Module
	}
	w.path.update(pmod, "{ext-inst}")
	w.path.update(nil, e.ExtPrefix+":"+e.Keyword)
	defer func() { w.path.pop(); w.path.pop() }()
	// lysc_ext_find_definition: the prefix and the definition were checked at parse time
	def := w.c.prefixModule(w.pm, w.c.mainOf(w.pm), e.ExtPrefix)
	inst := &schema.ExtInstance{Def: def.mod, Name: e.Keyword, Argument: e.Arg}
	exts, err := w.compileExts(e, nil, nil)
	if err != nil {
		return nil, err
	}
	inst.Exts = exts
	plg := pluginOf(def, e.Keyword)
	if plg == nil {
		return inst, nil
	}
	if e.HasArg {
		w.path.update(w.cur, e.Arg)
		defer w.path.pop()
	}
	if err := plg.compile(w, e, inst, parent); err != nil {
		return nil, err
	}
	return inst, nil
}

// vlog logs an error of the type compiler (a vErr) at the current path.
func (w *nodeCtx) vlog(err error) error {
	var ve *vErr
	if errors.As(err, &ve) {
		_ = w.errf(ve.Code, "%s", ve.Msg)
		return rc(ve.Err)
	}
	return err
}
