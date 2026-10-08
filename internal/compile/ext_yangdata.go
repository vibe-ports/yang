// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_exts/yangdata.c (BSD-3-Clause, © CESNET).

package compile

import (
	"fmt"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// yangDataSubs are the substatements yangdata_parse and yangdata_compile declare, in order; all
// three share one storage (ext->parsed, ext->compiled).
var (
	yangDataSubs  = []parser.ExtSubstmt{{Keyword: "container", Many: true}, {Keyword: "choice", Many: true}, {Keyword: "uses", Many: true}}
	yangDataCSubs = []extCSubstmt{{"container", true}, {"choice", true}, {"uses", true}}
)

// yangDataParse is yangdata_parse: a non-top-level instance is ignored with a warning, a second
// instance with the same name and argument is an error.
func yangDataParse(c *Context, x *extParse) error {
	name := x.e.ExtPrefix + ":" + x.e.Keyword
	if !x.root {
		_ = c.extLog(x, true, "Extension %s is ignored since it appears as a non top-level statement in \"%s\" statement.",
			name, stmtStr(x.parentStmt))
		return errNot
	}
	if extDuplicate(x) {
		return c.extLog(x, false, "Extension %s is instantiated multiple times.", name)
	}
	_, err := c.parseExtInstance(x, yangDataSubs)
	return err
}

// extDuplicate is the duplicate check of yangdata_parse and structure_parse: another instance
// of the module's exts array written with the same name and argument.
func extDuplicate(x *extParse) bool {
	for _, o := range x.arr {
		if o != x.e && o.ExtPrefix == x.e.ExtPrefix && o.Keyword == x.e.Keyword && o.Arg == x.e.Arg {
			return true
		}
	}
	return false
}

// yangDataCompile is yangdata_compile: the substatements compiled without config and if-feature
// disabling into the instance's own top level, which must then be exactly one container (a
// choice whose every case holds exactly one container counts).
func yangDataCompile(w *nodeCtx, e *parser.Stmt, inst *schema.ExtInstance, _ *schema.Node) error {
	prev := w.opts
	w.opts |= optNoConfig | optNoDisabled
	err := w.compileExtInstance(e, yangDataSubs, yangDataCSubs, inst, nil)
	w.opts = prev
	if err != nil {
		return err
	}
	name := e.ExtPrefix + ":" + e.Keyword
	nodes, msg := inst.Nodes, ""
	switch {
	case len(nodes) == 0:
		msg = fmt.Sprintf("Extension %s is instantiated without any top level data node, but exactly one container data "+
			"node is expected.", name)
	case len(nodes) > 1:
		msg = fmt.Sprintf("Extension %s is instantiated with multiple top level data nodes, but only a single container "+
			"data node is allowed.", name)
	case nodes[0].Kind == schema.Choice:
		// all the choice's cases are expected to result in a single container node
		for sn := range schema.GetNext(nodes[0], nil, 0) {
			if sib := sn.Parent.Children; sib[len(sib)-1] != sn { // snode->next
				msg = fmt.Sprintf("Extension %s is instantiated with multiple top level data nodes (inside a single "+
					"choice's case), but only a single container data node is allowed.", name)
				break
			}
			if sn.Kind != schema.Container {
				msg = fmt.Sprintf("Extension %s is instantiated with %s top level data node (inside a choice), but only "+
					"a single container data node is allowed.", name, lysNodetype2str(sn.Kind))
				break
			}
		}
	case nodes[0].Kind != schema.Container: // via uses
		msg = fmt.Sprintf("Extension %s is instantiated with %s top level data node, but only a single container data "+
			"node is allowed.", name, lysNodetype2str(nodes[0].Kind))
	}
	if msg != "" {
		inst.Nodes = nil
		return w.extCompileLog(inst, msg)
	}
	return nil
}

// extCompileLog is lyplg_ext_compile_log of an error with LY_EVALID: an "Ext plugin" message
// with LYVE_OTHER at the current compile path.
func (w *nodeCtx) extCompileLog(inst *schema.ExtInstance, msg string) error {
	w.c.diags = append(w.c.diags, Diagnostic{Phase: w.c.phase, Level: LevelError, Err: "LY_EPLUGIN|LY_EVALID",
		Code: ly.Other, SchemaPath: w.path.String(), Msg: fmt.Sprintf("Ext plugin \"%s\": %s", inst.Plugin, msg)})
	return eValid
}
