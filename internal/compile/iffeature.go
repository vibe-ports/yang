// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_features.c (lys_eval_iffeatures, lys_compile_iffeature,
// lysp_feature_find) and src/schema_compile_node.c (lys_compile_node_) (BSD-3-Clause, © CESNET).

package compile

import (
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
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

// iffValue compiles one if-feature (lys_compile_iffeature) and evaluates it
// (lysc_iffeature_value). The features are looked up right to left as libyang's second pass
// does, so the last unknown one is reported; an expression the second pass cannot process is
// reported after the lookup (D-0036).
func (w *nodeCtx) iffValue(pm *pmod, iff *parser.IfFeature) (bool, error) {
	var names []string
	processing := iff.Err != nil && iff.Processing
	switch {
	case processing:
		names = iffTokens(iff.Expr)
	case iff.Err != nil:
		return false, w.errf(iff.Err.Code, "%s", iff.Err.Msg)
	default:
		iff.AST.Eval(func(n string) bool { names = append(names, n); return false })
	}
	on := make([]bool, len(names))
	for i := len(names) - 1; i >= 0; i-- {
		var found bool
		if on[i], found = w.c.feature(pm, names[i]); !found {
			return false, w.errf(ly.SyntaxYang, "Invalid value \"%s\" of if-feature - unable to find feature \"%s\".",
				iff.Expr, names[i])
		}
	}
	if processing {
		_ = w.errf(iff.Err.Code, "%s", iff.Err.Msg)
		return false, rc("LY_EINT")
	}
	i := 0
	return iff.AST.Eval(func(string) bool { i++; return on[i-1] }), nil
}

// iffTokens are the feature operands of an if-feature expression in text order, tokenized as
// the second pass of lys_compile_iffeature does (right to left: ')' and '(' alone, a token runs
// left to a space or '('; "not", "and", "or" followed by a space are operators).
func iffTokens(e string) []string {
	if i := strings.IndexByte(e, 0); i >= 0 {
		e = e[:i]
	}
	space := func(b byte) bool { return b == ' ' || b >= '\t' && b <= '\r' }
	var names []string
	for i := len(e) - 1; i >= 0; i-- {
		if e[i] == '(' || e[i] == ')' || space(e[i]) {
			continue
		}
		end := i + 1
		for i >= 0 && !space(e[i]) && e[i] != '(' {
			i--
		}
		i++
		op := false
		for _, o := range []string{"not", "and", "or"} {
			op = op || strings.HasPrefix(e[i:], o) && i+len(o) < len(e) && space(e[i+len(o)])
		}
		if !op {
			names = append([]string{e[i:end]}, names...)
		}
	}
	return names
}

// feature is lysp_feature_find(pm, name, 1) with the feature's LYS_FENABLED flag: an optional
// prefix resolves through pm's imports, the feature is searched in that module and its
// submodules.
//
// Until the loader keeps the enabled flags on the parsed features (design 06 C1b, #40), the flag
// is read from the compiled module's Features; an import-only module has none enabled. When #40
// lands this becomes its featureFind(pm, main, name, true).enabled.
func (c *Context) feature(pm *pmod, name string) (enabled, found bool) {
	m := c.mainOf(pm)
	if i := strings.IndexByte(name, ':'); i >= 0 {
		if m = c.prefixModule(pm, m, name[:i]); m == nil {
			return false, false
		}
		name = name[i+1:]
	}
	if m == nil {
		return false, false
	}
	has := func(p *parser.Module) bool {
		for _, f := range p.Features {
			if f.Name == name {
				return true
			}
		}
		return false
	}
	found = has(m.Parsed)
	for _, inc := range m.Includes {
		found = found || inc.Sub != nil && has(inc.Sub.Parsed)
	}
	if found && m.mod != nil {
		if f := m.mod.Feature(name); f != nil {
			enabled = f.Enabled
		}
	}
	return enabled, found
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
