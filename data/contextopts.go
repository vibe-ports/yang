// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_free.c (BSD-3-Clause, © CESNET).

package data

import (
	"strings"

	"github.com/vibe-ports/yang/internal/contextopts"
	"github.com/vibe-ports/yang/internal/snap"
)

func init() {
	contextopts.NodeSchema = func(v any) *snap.Schema {
		n, _ := v.(*Node)
		if s := ctxOf(n); s != nil {
			return snap.New(s)
		}
		return nil
	}
	contextopts.TreeSchema = func(v any) *snap.Schema {
		t, _ := v.(*Tree)
		if t != nil {
			return snap.New(t.context())
		}
		return nil
	}
	contextopts.ApplyDiffModule = func(dst, diff any, s *snap.Schema, name string) error {
		return dst.(*Tree).applyDiffWithModule(diff.(*Tree), ApplyDiffOptions{Module: name}, snap.Set(s).Implemented(name))
	}
	contextopts.MergeDiffModule = func(dst, src any, s *snap.Schema, name string, defaults bool) error {
		return dst.(*Tree).mergeDiffWithModule(src.(*Tree), MergeDiffOptions{Module: name, Defaults: defaults}, snap.Set(s).Implemented(name))
	}
	contextopts.DiscardTree = func(v any) error {
		t, _ := v.(*Tree)
		if t == nil {
			return nil
		}
		roots := t.top.nodes()
		// lyd_free_all releases every descendant's reciprocal leafref links. The
		// sibling state can then be cleared once, without N individual unlinks.
		cleaner := bulkLinkCleaner{work: &t.work}
		for _, root := range roots {
			for n := range root.All() {
				cleaner.visit()
				cleaner.free(n)
			}
			root.tree = nil
		}
		t.top = siblings{}
		return nil
	}
	contextopts.DrainDiffDiagnostics = func(v any) []contextopts.ContextDiagnostic {
		t, _ := v.(*Tree)
		if t == nil {
			return nil
		}
		out := make([]contextopts.ContextDiagnostic, 0, len(t.diffLogs))
		for _, item := range t.diffLogs {
			out = append(out, contextopts.ContextDiagnostic{Context: snap.New(item.set), Diagnostic: item.diag})
		}
		t.diffLogs = nil
		return out
	}
	contextopts.CheckDupContexts = func(node, parent any) error {
		n, _ := node.(*Node)
		p, _ := parent.(*Node)
		if n != nil && p != nil && ctxOf(n) != ctxOf(p) {
			lg := &logger{set: ctxOf(n)}
			return lg.done(lg.logErr("LY_EINVAL", "Different \"node\" and \"parent\" contexts used in node duplication."))
		}
		return nil
	}
	contextopts.CheckInsertSchema = func(parent, node any) error {
		p, _ := parent.(*Node)
		n, _ := node.(*Node)
		if p == nil || n == nil {
			return nil
		}
		lg := &logger{set: ctxOf(n)}
		return lg.done(insertCheckSchema(p.schema, nil, n.schema))
	}
	contextopts.PrintNode = func(v any, mode uint8) (map[string]any, []snap.Diagnostic, error) {
		n, _ := v.(*Node)
		var j, x strings.Builder
		jsonOpts := PrintOptions{WithDefaults: WD(mode), dflt: &defaultCheck{}}
		if err := n.PrintJSON(&j, jsonOpts); err != nil {
			return nil, nil, err
		}
		diags := append([]snap.Diagnostic(nil), jsonOpts.dflt.diags...)
		xmlOpts := PrintOptions{WithDefaults: WD(mode), dflt: &defaultCheck{}}
		if err := n.PrintXML(&x, xmlOpts); err != nil {
			return nil, diags, err
		}
		diags = append(diags, xmlOpts.dflt.diags...)
		return map[string]any{"json": j.String(), "xml": x.String()}, diags, nil
	}
}
