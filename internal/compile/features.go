// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_features.c (BSD-3-Clause, © CESNET).

package compile

import (
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
)

// feature is a parsed feature (struct lysp_feature) with its flags.
type feature struct {
	p       *parser.Node
	pm      *pmod        // the (sub)module defining it: its if-feature prefixes resolve there
	enabled bool         // LYS_FENABLED
	iffs    [][]*feature // iffeatures_c: the features of each if-feature, in text order
	deps    []*feature   // depfeatures: features with an if-feature referencing this one
}

// collectFeatures lists the features of m and its submodules in
// lysp_feature_next order (module, then each include).
func collectFeatures(m *Module) {
	add := func(pm *pmod) {
		for _, f := range pm.Parsed.Features {
			m.features = append(m.features, &feature{p: f, pm: pm})
		}
	}
	add(&m.pmod)
	for _, inc := range m.Includes {
		add(&inc.Sub.pmod)
	}
}

// featureFind is lysp_feature_find: name (with an optional prefix when
// prefixed) as referenced from pm, a (sub)module of main.
func (c *Context) featureFind(pm *pmod, main *Module, name string, prefixed bool) *feature {
	if i := strings.IndexByte(name, ':'); prefixed && i >= 0 {
		if main = c.prefixModule(pm, main, name[:i]); main == nil {
			return nil
		}
		name = name[i+1:]
	}
	for _, f := range main.features {
		if f.p.Name == name {
			return f
		}
	}
	return nil
}

// iffNames are the feature names of an if-feature expression in text order
// (Eval visits every operand, left to right).
func iffNames(e *parser.IffExpr) []string {
	var names []string
	e.Eval(func(n string) bool { names = append(names, n); return false })
	return names
}

// iffValue is lysc_iffeature_value: the expression with fs its features in text order.
func iffValue(e *parser.IffExpr, fs []*feature) bool {
	i := 0
	return e.Eval(func(string) bool { i++; return fs[i-1].enabled })
}

// compileIff is lys_compile_iffeature for an if-feature written in pm:
// the parser's syntax error, or the features, looked up right to left as
// libyang does (so the last unknown one is reported). Errors are logged at
// path: the compile path for nodes, none for features (P0). An expression
// the second pass cannot process is reported after the lookup, with
// LY_EINT (D-0036).
func (c *Context) compileIff(pm *pmod, main *Module, iff *parser.IfFeature, path string) ([]*feature, error) {
	var names []string
	switch {
	case iff.Err != nil && iff.Processing:
		// detected after the second pass, which has looked the features up
		names = iffOperands(iff.Expr)
	case iff.Err != nil:
		return nil, c.logPath(iff.Err.Code, path, "%s", iff.Err.Msg)
	default:
		names = iffNames(iff.AST)
	}
	fs := make([]*feature, len(names))
	for i := len(names) - 1; i >= 0; i-- {
		if fs[i] = c.featureFind(pm, main, names[i], true); fs[i] == nil {
			return nil, c.logPath(ly.SyntaxYang, path, "Invalid value \"%s\" of if-feature - unable to find feature \"%s\".",
				iff.Expr, names[i])
		}
	}
	if iff.Err != nil {
		_ = c.logPath(iff.Err.Code, path, "%s", iff.Err.Msg)
		return nil, rc("LY_EINT")
	}
	return fs, nil
}

// iffOperands are the feature operands of an if-feature expression in text
// order, tokenized as the second pass of lys_compile_iffeature does (right
// to left: ')' and '(' alone, a token runs left to a space or '('; "not",
// "and", "or" followed by a space are operators).
func iffOperands(e string) []string {
	if i := strings.IndexByte(e, 0); i >= 0 {
		e = e[:i]
	}
	var names []string
	for i := len(e) - 1; i >= 0; i-- {
		if e[i] == '(' || e[i] == ')' || isCSpace(e[i]) {
			continue
		}
		end := i + 1
		for i >= 0 && !isCSpace(e[i]) && e[i] != '(' {
			i--
		}
		i++
		op := false
		for _, o := range []string{"not", "and", "or"} {
			op = op || strings.HasPrefix(e[i:], o) && i+len(o) < len(e) && isCSpace(e[i+len(o)])
		}
		if !op {
			names = append([]string{e[i:end]}, names...)
		}
	}
	return names
}

func isCSpace(b byte) bool { return b == ' ' || b >= '\t' && b <= '\r' }

// compileFeatureIffeatures is lys_compile_feature_iffeatures (P0): compile
// the if-features of every feature of m and reject circular references.
func (c *Context) compileFeatureIffeatures(m *Module) error {
	for _, f := range m.features {
		if len(f.p.IfFeatures) == 0 {
			continue
		}
		for _, iff := range f.p.IfFeatures {
			fs, err := c.compileIff(f.pm, m, iff, "")
			if err != nil {
				return err
			}
			f.iffs = append(f.iffs, fs)
		}
		for _, fs := range f.iffs {
			for _, g := range fs {
				if g == f {
					return c.logVal(ly.Reference, 0, "Feature \"%s\" is referenced from itself.", f.p.Name)
				}
				// lys_compile_feature_circular_check
				if reaches(g, f.deps, func(d *feature) []*feature { return d.deps }) {
					return c.logVal(ly.Reference, 0, "Feature \"%s\" is indirectly referenced from itself.", g.p.Name)
				}
				g.deps = append(g.deps, f)
			}
		}
	}
	return nil
}

// reaches reports whether target is in start or reachable from it through
// next: the breadth-first set walk of lys_compile_feature_circular_check
// and lys_compile_identity_circular_check.
func reaches[T comparable](target T, start []T, next func(T) []T) bool {
	seen := map[T]bool{}
	var queue []T
	add := func(xs []T) bool {
		for _, x := range xs {
			if x == target {
				return true
			}
			if !seen[x] {
				seen[x] = true
				queue = append(queue, x)
			}
		}
		return false
	}
	if add(start) {
		return true
	}
	for i := 0; i < len(queue); i++ {
		if add(next(queue[i])) {
			return true
		}
	}
	return false
}

// setFeatures is lys_set_features: nil leaves the features untouched, an
// empty list disables all, "*" first enables all, otherwise exactly the
// listed ones are enabled. It reports whether a flag changed.
func (c *Context) setFeatures(m *Module, features []string) (bool, error) {
	if features == nil {
		return false, nil
	}
	all := len(features) > 0 && features[0] == "*"
	if len(features) > 0 && !all {
		for _, n := range features {
			if c.featureFind(&m.pmod, m, n, false) == nil {
				return false, c.logErr(eInval, "Feature \"%s\" not found in module \"%s@%s\".", n, m.Name, orNone(m.Revision))
			}
		}
	}
	changed := false
	for _, f := range m.features {
		on := all
		for _, n := range features {
			on = on || n == f.p.Name
		}
		if on != f.enabled {
			f.enabled, changed = on, true
		}
	}
	return changed, nil
}

// checkFeatures is lys_check_features (P1). libyang passes the array of
// compiled if-features as one, so only the first if-feature of an enabled
// feature is checked (D-0036).
func (c *Context) checkFeatures(m *Module) error {
	for _, f := range m.features {
		if !f.enabled || len(f.iffs) == 0 {
			continue
		}
		if !iffValue(f.p.IfFeatures[0].AST, f.iffs[0]) {
			return c.logErr(eDenied, "Feature \"%s\" cannot be enabled because its \"if-feature\" is not satisfied.", f.p.Name)
		}
	}
	return nil
}
