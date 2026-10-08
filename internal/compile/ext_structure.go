// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/plugins_exts/structure.c (BSD-3-Clause, © CESNET).

package compile

import (
	"fmt"

	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// structureSubs are the substatements structure_parse and structure_compile declare, in order;
// the data definitions share one storage. The compiled typedefs and groupings are not stored.
var (
	structureSubs = []parser.ExtSubstmt{{Keyword: "must", Many: true}, {Keyword: "status"},
		{Keyword: "description"}, {Keyword: "reference"}, {Keyword: "typedef", Many: true},
		{Keyword: "grouping", Many: true}, {Keyword: "container", Many: true}, {Keyword: "leaf", Many: true},
		{Keyword: "leaf-list", Many: true}, {Keyword: "list", Many: true}, {Keyword: "choice", Many: true},
		{Keyword: "anydata", Many: true}, {Keyword: "anyxml", Many: true}, {Keyword: "uses", Many: true}}
	structureCSubs = []extCSubstmt{{"must", true}, {"status", true}, {"description", true}, {"reference", true},
		{"typedef", false}, {"grouping", false}, {"container", true}, {"leaf", true}, {"leaf-list", true},
		{"list", true}, {"choice", true}, {"anydata", true}, {"anyxml", true}, {"uses", true}}
)

// structureParse is structure_parse: only at the top level of a module or submodule, once per
// name.
func structureParse(c *Context, x *extParse) error {
	name := x.e.ExtPrefix + ":" + x.e.Keyword
	if !x.root {
		return c.extLog(x, false, "Extension %s must not be used as a non top-level statement in \"%s\" statement.",
			name, stmtStr(x.parentStmt))
	}
	if extDuplicate(x) {
		return c.extLog(x, false, "Extension %s is instantiated multiple times.", name)
	}
	_, err := c.parseExtInstance(x, structureSubs)
	return err
}

// structureCompile is structure_compile: the name may not be one of a top-level data node of
// the module; the substatements are compiled into a container named by the argument (must,
// status and the data definitions; no config, no if-feature disabling), which is then config
// true like any top-level container.
func structureCompile(w *nodeCtx, e *parser.Stmt, inst *schema.ExtInstance, _ *schema.Node) error {
	for _, n := range w.data { // mod_c->data: the data nodes compiled so far
		if n.Name == inst.Argument {
			return w.extCompileLog(inst, fmt.Sprintf("Extension %s:%s collides with a %s with the same identifier.",
				e.ExtPrefix, e.Keyword, lysNodetype2str(n.Kind)))
		}
	}
	// the top-level container with the instance name; all the other substatements go into it
	inst.Root = &schema.Node{Kind: schema.Container, Name: inst.Argument, Module: w.cur,
		StatusUnset: w.c.extParsed[e] == nil || w.c.extParsed[e].Status == ""}
	prev := w.opts
	w.opts |= optNoConfig | optNoDisabled
	err := w.compileExtInstance(e, structureSubs, structureCSubs, inst, inst.Root)
	w.opts = prev
	if err != nil {
		return err
	}
	// compile config properly even though it is ignored
	inst.Root.Config = true
	return nil
}
