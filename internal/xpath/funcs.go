// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (BSD-3-Clause, © CESNET).

package xpath

import (
	"math"
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/xsdre"
)

// funcImpls implements the functions of funcArity; set is the context set.
var funcImpls map[string]func(ev *evaluator, args []value, set value) (value, error)

func init() { // in init: the table refers to the evaluator, which refers to the table
	funcImpls = map[string]func(*evaluator, []value, value) (value, error){
		"bit-is-set":           fnBitIsSet,
		"boolean":              fnBoolean,
		"ceiling":              fnCeiling,
		"concat":               fnConcat,
		"contains":             fnContains,
		"count":                fnCount,
		"current":              fnCurrent,
		"deref":                fnDeref,
		"derived-from":         fnDerivedFrom,
		"derived-from-or-self": fnDerivedFromOrSelf,
		"enum-value":           fnEnumValue,
		"false":                fnFalse,
		"floor":                fnFloor,
		"lang":                 fnLang,
		"last":                 fnLast,
		"local-name":           fnLocalName,
		"name":                 fnName,
		"namespace-uri":        fnNamespaceURI,
		"normalize-space":      fnNormalizeSpace,
		"not":                  fnNot,
		"number":               fnNumber,
		"position":             fnPosition,
		"re-match":             fnReMatch,
		"round":                fnRound,
		"starts-with":          fnStartsWith,
		"string":               fnString,
		"string-length":        fnStringLength,
		"substring":            fnSubstring,
		"substring-after":      fnSubstringAfter,
		"substring-before":     fnSubstringBefore,
		"sum":                  fnSum,
		"translate":            fnTranslate,
		"true":                 fnTrue,
	}
}

func argType(n int, v value, sig string) error {
	return xpErr("Wrong type of argument #%d (%s) for the XPath function %s.", n, typeNames[v.t], sig)
}

// first returns the first element node of a node-set argument (libyang reads nodes[0]).
func first(v value) Node {
	if len(v.nodes) > 0 && v.nodes[0].t == itElem {
		return v.nodes[0].n
	}
	return nil
}

// firstTerm is the node bit-is-set/enum-value/deref read: nodes[0].node, which
// for a text() item is its leaf (D-0012: libyang crashes on the root).
func firstTerm(v value) Node {
	if len(v.nodes) > 0 && v.nodes[0].t != itRoot && isTerm(v.nodes[0].n) {
		return v.nodes[0].n
	}
	return nil
}

func fnBitIsSet(ev *evaluator, a []value, _ value) (value, error) {
	if a[0].t != vNodes {
		return value{}, argType(1, a[0], "bit-is-set(node-set, string)")
	}
	bit := ev.toString(a[1])
	if n := firstTerm(a[0]); n != nil {
		if bits, ok := valueOf(n).Bits(); ok {
			return boolV(slices.Contains(bits, bit)), nil
		}
	}
	return boolV(false), nil
}

func fnBoolean(ev *evaluator, a []value, _ value) (value, error) { return boolV(ev.toBool(a[0])), nil }

// fnCeiling: libyang computes (long long)x + 1, so ceiling(-1.5) = 0 and
// ceiling(NaN) = -9223372036854775807.
func fnCeiling(ev *evaluator, a []value, _ value) (value, error) {
	f := ev.toNum(a[0])
	if t := ctrunc(f); !ldInt(t).eq(f) {
		return numV(ldInt(t + 1)), nil // wraps like the C long long
	}
	return numV(f), nil
}

func fnConcat(ev *evaluator, a []value, _ value) (value, error) {
	var b strings.Builder
	for _, v := range a {
		b.WriteString(ev.toString(v))
	}
	return strV(b.String()), nil
}

func fnContains(ev *evaluator, a []value, _ value) (value, error) {
	return boolV(strings.Contains(ev.toString(a[0]), ev.toString(a[1]))), nil
}

func fnCount(_ *evaluator, a []value, _ value) (value, error) {
	if a[0].t != vNodes {
		return value{}, argType(1, a[0], "count(node-set)")
	}
	return intV(len(a[0].nodes)), nil
}

func fnCurrent(ev *evaluator, _ []value, _ value) (value, error) {
	return nodesV([]item{ev.cur}), nil
}

func fnDeref(ev *evaluator, a []value, _ value) (value, error) {
	if a[0].t != vNodes {
		return value{}, argType(1, a[0], "deref(node-set)")
	}
	n := firstTerm(a[0])
	if n == nil {
		return nodesV(nil), nil
	}
	if ev.ec.Deref == nil {
		return value{}, &Error{Err: "LY_EINVAL", Msg: "deref() needs EvalContext.Deref."}
	}
	targets, err := ev.ec.Deref(n)
	if err != nil {
		return value{}, err
	}
	out := make([]item, len(targets))
	for i, t := range targets {
		out[i] = item{t, itElem}
	}
	return nodesV(out), nil // in target order: libyang (Release build) does not sort
}

func fnDerivedFrom(ev *evaluator, a []value, _ value) (value, error) { return derived(ev, a, false) }

func fnDerivedFromOrSelf(ev *evaluator, a []value, _ value) (value, error) {
	return derived(ev, a, true)
}

// derived is xpath_derived_.
func derived(ev *evaluator, a []value, orSelf bool) (value, error) {
	if a[0].t != vNodes {
		return value{}, argType(1, a[0], "derived-from(-or-self)(node-set, string)")
	}
	var id Ident
	name := ev.toString(a[1])
	if pref, local, ok := strings.Cut(name, ":"); ok {
		if pref == "" {
			return value{}, &Error{Err: "LY_EINVAL", Msg: "Invalid argument prefix_len (ly_resolve_prefix())."}
		}
		mod, ok := ev.ns.Resolve(pref)
		if !ok {
			return value{}, xpErr("Unknown/non-implemented module \"%s\".", pref)
		}
		id = Ident{mod, local}
	} else {
		id = Ident{ev.ns.Default(), name}
		if id.Module == "" && ev.cur.t == itElem { // JSON: the module of the current node
			id.Module = ev.cur.n.Module()
		}
	}
	if ev.ec.Schema == nil || !ev.ec.Schema.HasIdentity(id) {
		return value{}, xpErr("Identity \"%s\" not found in module \"%s\".", id.Name, id.Module)
	}
	for _, it := range a[0].nodes {
		if it.t != itElem || !isTerm(it.n) {
			continue
		}
		mod, name, ok := valueOf(it.n).Identity()
		if v := (Ident{mod, name}); ok && (orSelf && v == id || ev.ec.Schema.IsDerived(id, v)) {
			return boolV(true), nil
		}
	}
	return boolV(false), nil
}

func fnEnumValue(_ *evaluator, a []value, _ value) (value, error) {
	if a[0].t != vNodes {
		return value{}, argType(1, a[0], "enum-value(node-set)")
	}
	if n := firstTerm(a[0]); n != nil {
		if e, ok := valueOf(n).Enum(); ok {
			return intV(e), nil
		}
	}
	return numV(ldNaN), nil
}

func fnFalse(*evaluator, []value, value) (value, error) { return boolV(false), nil }

func fnTrue(*evaluator, []value, value) (value, error) { return boolV(true), nil }

// fnFloor: libyang truncates (floor(-1.5) = -1) and, for NaN/Infinity, returns
// the context set unchanged.
func fnFloor(ev *evaluator, a []value, set value) (value, error) {
	f := ev.toNum(a[0])
	if f.isNaN() || f.isInf() {
		return set, nil
	}
	return numV(ldInt(ctrunc(f))), nil
}

// fnLang: xml:lang is metadata, which Node does not expose, so never true.
func fnLang(_ *evaluator, _ []value, set value) (value, error) {
	if set.t != vNodes {
		return value{}, xpErr("Invalid context type %s in lang(string).", typeNames[set.t])
	}
	return boolV(false), nil
}

func fnLast(_ *evaluator, _ []value, set value) (value, error) {
	if set.t != vNodes {
		return value{}, xpErr("Invalid context type %s in last().", typeNames[set.t])
	}
	if len(set.nodes) == 0 {
		return intV(0), nil
	}
	return intV(set.size), nil
}

func fnPosition(_ *evaluator, _ []value, set value) (value, error) {
	if set.t != vNodes {
		return value{}, xpErr("Invalid context type %s in position().", typeNames[set.t])
	}
	if len(set.nodes) == 0 {
		return intV(0), nil
	}
	return intV(set.pos), nil
}

// nameArg picks the node of local-name/name/namespace-uri: ok=false → "".
func nameArg(a []value, set value, sig string) (Node, bool, error) {
	v := set
	if len(a) > 0 {
		if v = a[0]; v.t != vNodes {
			return nil, false, argType(1, v, sig)
		}
	} else if v.t != vNodes {
		return nil, false, xpErr("Invalid context type %s in %s.", typeNames[v.t], sig)
	}
	n := first(v)
	return n, n != nil, nil
}

func fnLocalName(_ *evaluator, a []value, set value) (value, error) {
	n, ok, err := nameArg(a, set, "local-name(node-set?)")
	if !ok {
		return strV(""), err
	}
	return strV(n.Name()), nil
}

func fnName(ev *evaluator, a []value, set value) (value, error) {
	n, ok, err := nameArg(a, set, "name(node-set?)")
	if !ok {
		return strV(""), err
	}
	return strV(ev.ns.Prefix(n.Module()) + ":" + n.Name()), nil
}

func fnNamespaceURI(_ *evaluator, a []value, set value) (value, error) {
	n, ok, err := nameArg(a, set, "namespace-uri(node-set?)")
	if !ok || n.Schema() == nil {
		return strV(""), err
	}
	return strV(n.Schema().Namespace()), nil
}

func strArg(ev *evaluator, a []value, set value) string {
	if len(a) > 0 {
		return ev.toString(a[0])
	}
	return ev.toString(set)
}

// fnNormalizeSpace normalizes only when libyang's pre-scan finds leading,
// trailing or repeated white space: "a\tb" stays as is.
func fnNormalizeSpace(ev *evaluator, a []value, set value) (value, error) {
	s := strArg(ev, a, set)
	need, before := false, false
	for i := 0; i < len(s) && !need; i++ {
		ws := isXMLWS(s[i])
		need = ws && (i == 0 || before || i == len(s)-1)
		before = ws
	}
	if !need {
		return strV(s), nil
	}
	var b []byte
	space := false
	for _, c := range []byte(s) {
		if isXMLWS(c) {
			space = true
			continue
		}
		if space && len(b) > 0 {
			b = append(b, ' ')
		}
		space = false
		b = append(b, c)
	}
	return strV(string(b)), nil
}

func fnNot(ev *evaluator, a []value, _ value) (value, error) { return boolV(!ev.toBool(a[0])), nil }

func fnNumber(ev *evaluator, a []value, set value) (value, error) {
	if len(a) > 0 {
		set = a[0]
	}
	return numV(ev.toNum(set)), nil
}

// maxCachedPatterns bounds the per-Expr re-match() cache.
const maxCachedPatterns = 64

func fnReMatch(ev *evaluator, a []value, _ value) (value, error) {
	s, pat := ev.toString(a[0]), ev.toString(a[1])
	c, ok := ev.e.res.Load(pat)
	if !ok {
		re, err := xsdre.Compile(pat)
		if c = any(re); err != nil {
			c = err
		}
		if ev.e.nres.Add(1) <= maxCachedPatterns { // patterns may come from data
			ev.e.res.Store(pat, c)
		}
	}
	re, ok := c.(*xsdre.Pattern)
	if !ok {
		err := c.(error)
		return value{}, &Error{Err: "LY_EVALID", Msg: "Regular expression \"" + pat + "\" is not valid (" + err.Error() + ")."}
	}
	return boolV(re.Match(s)), nil
}

// fnRound: libyang adds 0.5 and truncates, so round(-2.7) = -2.
func fnRound(ev *evaluator, a []value, _ value) (value, error) {
	return numV(round(ev.toNum(a[0]))), nil
}

// round is xpath_round in long double: -0 for [-0.5, 0], else floor(x + 0.5)
// where floor truncates (and leaves NaN/Infinity).
func round(f ld) ld {
	if c, ok := f.cmp(ldFloat(-0.5)); f.isZero() || f.sign() < 0 && ok && c >= 0 {
		return ld{f: newF().Neg(newF())}
	}
	if f = ldOp("+", f, ldFloat(0.5)); f.isNaN() || f.isInf() {
		return f
	}
	return ldInt(ctrunc(f))
}

func fnStartsWith(ev *evaluator, a []value, _ value) (value, error) {
	return boolV(strings.HasPrefix(ev.toString(a[0]), ev.toString(a[1]))), nil
}

func fnString(ev *evaluator, a []value, set value) (value, error) {
	return strV(strArg(ev, a, set)), nil
}

// fnStringLength counts bytes, as libyang (strlen).
func fnStringLength(ev *evaluator, a []value, set value) (value, error) {
	return intV(len(strArg(ev, a, set))), nil
}

// fnSubstring works on bytes, as libyang.
func fnSubstring(ev *evaluator, a []value, _ value) (value, error) {
	s := ev.toString(a[0])
	start := int64(math.MaxInt32)
	switch f := round(ev.toNum(a[1])); {
	case f.isInf() && f.sign() < 0:
		start = math.MinInt32
	case !f.isNaN() && !f.isInf():
		start = ctrunc(ldOp("-", f, ldInt(1)))
	}
	if start >= int64(len(s)) {
		return strV(""), nil
	}
	length := int64(math.MaxInt32)
	if len(a) == 3 {
		switch f := round(ev.toNum(a[2])); {
		case f.isNaN() || f.big().Signbit():
			length = 0
		case f.isInf():
		case ctrunc(f) >= 1<<31 || ctrunc(f) < 0:
			length = math.MinInt32 // C (int32_t) out of range: x86 integer indefinite (D-0011)
		default:
			length = ctrunc(f)
		}
	}
	from := min(max(start, 0), int64(len(s)))
	to := min(max(start+length, from), int64(len(s)))
	return strV(s[from:to]), nil
}

func fnSubstringAfter(ev *evaluator, a []value, _ value) (value, error) {
	_, after, ok := strings.Cut(ev.toString(a[0]), ev.toString(a[1]))
	if !ok {
		return strV(""), nil
	}
	return strV(after), nil
}

func fnSubstringBefore(ev *evaluator, a []value, _ value) (value, error) {
	before, _, ok := strings.Cut(ev.toString(a[0]), ev.toString(a[1]))
	if !ok {
		return strV(""), nil
	}
	return strV(before), nil
}

func fnSum(ev *evaluator, a []value, _ value) (value, error) {
	if a[0].t != vNodes {
		return value{}, argType(1, a[0], "sum(node-set)")
	}
	var sum ld
	for _, it := range a[0].nodes {
		sum = ldOp("+", sum, cStrtod(ev.stringValue(it)))
	}
	return numV(sum), nil
}

// fnTranslate works on bytes, as libyang.
func fnTranslate(ev *evaluator, a []value, _ value) (value, error) {
	s, from, to := ev.toString(a[0]), ev.toString(a[1]), ev.toString(a[2])
	var b []byte
	for i := 0; i < len(s); i++ {
		switch j := strings.IndexByte(from, s[i]); {
		case j < 0:
			b = append(b, s[i])
		case j < len(to):
			b = append(b, to[j])
		}
	}
	return strV(string(b)), nil
}

// noValue stands in for a missing Value.
type noValue struct{}

func (noValue) String() string                   { return "" }
func (noValue) Identity() (string, string, bool) { return "", "", false }
func (noValue) Enum() (int, bool)                { return 0, false }
func (noValue) Bits() ([]string, bool)           { return nil, false }

func valueOf(n Node) Value {
	if v := n.Value(); v != nil {
		return v
	}
	return noValue{}
}
