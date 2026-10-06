// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_features.c (lys_eval_iffeatures, lys_compile_iffeature,
// lysp_feature_find) and src/schema_compile_node.c (lys_compile_node_) (BSD-3-Clause, © CESNET).

package compile

import (
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

// iffeatures is lys_eval_iffeatures for if-features written in pm, with the errors of
// lys_compile_iffeature logged at the current compile path: the expressions in order, stopping
// at the first false one, so a later expression is never compiled (iff/unknown-feature-after-false).
func (w *nodeCtx) iffeatures(pm *pmod, iffs []*parser.IfFeature) (bool, error) {
	for _, iff := range iffs {
		on, err := w.iffValue(pm, iff)
		if err != nil || !on {
			return false, err
		}
	}
	return true, nil
}

// iffValue compiles one if-feature (lys_compile_iffeature, errors at the compile path) and
// evaluates it (lysc_iffeature_value).
func (w *nodeCtx) iffValue(pm *pmod, iff *parser.IfFeature) (bool, error) {
	fs, err := w.c.compileIff(pm, pm.main, iff, w.path.String())
	if err != nil {
		return false, err
	}
	return iffValue(iff.AST, fs), nil
}

// mainOf is the module pm belongs to (lysp_module.mod), set by the loader.
func (c *Context) mainOf(pm *pmod) *Module { return pm.main }

// disable is the LYS_COMPILE_DISABLED part of lys_compile_node_: a node whose if-feature is false
// (or an obsolete one) is compiled like any other, but goes to the disabled set and makes its
// subtree disabled; nothing is added inside an already disabled subtree or a grouping.
func (w *nodeCtx) disable(n *schema.Node) {
	if w.opts&(optDisabled|optGrouping) != 0 {
		return
	}
	w.c.disabled = append(w.c.disabled, n)
	w.opts |= optDisabled
}
