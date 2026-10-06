// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (BSD-3-Clause, © CESNET).

package xpath

import (
	"math"
)

// maxDepth is LYXP_MAX_BLOCK_DEPTH: nesting of OrExpr (parentheses, predicates, arguments).
const maxDepth = 100

// funcArity is the function table of reparse_function_call: name → min, max arguments.
var funcArity = map[string][2]int{
	"bit-is-set":           {2, 2},
	"boolean":              {1, 1},
	"ceiling":              {1, 1},
	"concat":               {2, math.MaxInt},
	"contains":             {2, 2},
	"count":                {1, 1},
	"current":              {0, 0},
	"deref":                {1, 1},
	"derived-from":         {2, 2},
	"derived-from-or-self": {2, 2},
	"enum-value":           {1, 1},
	"false":                {0, 0},
	"floor":                {1, 1},
	"lang":                 {1, 1},
	"last":                 {0, 0},
	"local-name":           {0, 1},
	"name":                 {0, 1},
	"namespace-uri":        {0, 1},
	"normalize-space":      {0, 1},
	"not":                  {1, 1},
	"number":               {0, 1},
	"position":             {0, 0},
	"re-match":             {2, 2},
	"round":                {1, 1},
	"starts-with":          {2, 2},
	"string":               {0, 1},
	"string-length":        {0, 1},
	"substring":            {2, 3},
	"substring-after":      {2, 2},
	"substring-before":     {2, 2},
	"sum":                  {1, 1},
	"translate":            {3, 3},
	"true":                 {0, 0},
}

type ast any

type (
	// chainExpr is a left-associative operator chain of one precedence
	// level: args[0] ops[0] args[1] ops[1] … — a list, not a tree, so long
	// chains are evaluated iteratively (no recursion per operator).
	chainExpr struct {
		ops  []string // or and = != < <= > >= + - * div mod |
		args []ast
		// pos[i] is the source offset of the token BEFORE ops[i]: libyang passes
		// tok_pos[this_op - 1] to warn_operands (schema mode warnings)
		pos []int
	}
	// negExpr is an odd number of unary '-'; pos is the offset of the first one.
	negExpr struct {
		x   ast
		pos int
	}
	litExpr  string
	numExpr  struct{ v ld } // Number token, as C long double
	varExpr  string
	callExpr struct {
		name string
		args []ast
	}
	// pathExpr is a LocationPath or a FilterExpr with an optional path after it.
	pathExpr struct {
		abs   bool // starts at the document root
		prim  ast  // PrimaryExpr, nil for a location path
		preds []ast
		steps []step
	}
	step struct {
		axis     string // "" for '.' and '..'
		allDesc  bool   // preceded by '//'
		explicit bool   // axis written out (AxisName '::' or '@')
		test     tokKind
		name     string // NameTest text, NodeType name, "." or ".."
		end      int    // offset just past the name in the source (schema warnings)
		preds    []ast
	}
)

type parser struct {
	src  string
	toks []token
	i    int
}

func (p *parser) text(i int) string { t := p.toks[i]; return p.src[t.pos : t.pos+t.len] }

func (p *parser) at15(i int) string {
	s := p.src[p.toks[i].pos:]
	if len(s) > 15 {
		s = s[:15]
	}
	return s
}

// check is lyxp_check_token with logging.
func (p *parser) check(want tokKind) error {
	if p.i >= len(p.toks) {
		return xpErr("Unexpected XPath expression end.")
	}
	if want != tNone && p.toks[p.i].k != want {
		return xpErr("Unexpected XPath token \"%s\" (\"%s\"), expected \"%s\".", p.toks[p.i].k, p.at15(p.i), want)
	}
	return nil
}

func (p *parser) unexpected() error {
	return xpErr("Unexpected XPath token \"%s\" (\"%s\").", p.toks[p.i].k, p.at15(p.i))
}

// peek is lyxp_check_token without logging.
func (p *parser) peek(ks ...tokKind) bool {
	if p.i >= len(p.toks) {
		return false
	}
	for _, k := range ks {
		if p.toks[p.i].k == k {
			return true
		}
	}
	return false
}

func (p *parser) peekOp(k tokKind, ops ...string) (string, bool) {
	if k == tOperEqual && p.peek(tOperNEqual) {
		return "!=", true
	}
	if !p.peek(k) {
		return "", false
	}
	t := p.text(p.i)
	for _, o := range ops {
		if t == o {
			return t, true
		}
	}
	return "", false
}

// binary parses a left-associative chain of next separated by ops of kind k.
func (p *parser) binary(depth int, next func(int) (ast, error), k tokKind, ops ...string) (ast, error) {
	first, err := next(depth)
	if err != nil {
		return nil, err
	}
	c := chainExpr{args: []ast{first}}
	for {
		op, ok := p.peekOp(k, ops...)
		if !ok {
			break
		}
		pos := p.toks[p.i-1].pos
		p.i++
		r, err := next(depth)
		if err != nil {
			return nil, err
		}
		c.ops, c.args, c.pos = append(c.ops, op), append(c.args, r), append(c.pos, pos)
	}
	if len(c.ops) == 0 {
		return first, nil
	}
	return c, nil
}

// orExpr is reparse_or_expr: [11] OrExpr ::= AndExpr | OrExpr 'or' AndExpr.
func (p *parser) orExpr(depth int) (ast, error) {
	if depth++; depth > maxDepth {
		return nil, xpErr("The maximum nesting of expressions has been exceeded.")
	}
	return p.binary(depth, p.andExpr, tOperLog, "or")
}

func (p *parser) andExpr(depth int) (ast, error) {
	return p.binary(depth, p.equalityExpr, tOperLog, "and")
}

func (p *parser) equalityExpr(depth int) (ast, error) {
	return p.binary(depth, p.relationalExpr, tOperEqual, "=")
}

func (p *parser) relationalExpr(depth int) (ast, error) {
	return p.binary(depth, p.additiveExpr, tOperComp, "<", ">", "<=", ">=")
}

func (p *parser) additiveExpr(depth int) (ast, error) {
	return p.binary(depth, p.multiplicativeExpr, tOperMath, "+", "-")
}

func (p *parser) multiplicativeExpr(depth int) (ast, error) {
	return p.binary(depth, p.unaryExpr, tOperMath, "*", "div", "mod")
}

// unaryExpr is reparse_unary_expr: [17] UnaryExpr ::= UnionExpr | '-' UnaryExpr.
func (p *parser) unaryExpr(depth int) (ast, error) {
	neg, pos := false, 0
	if _, ok := p.peekOp(tOperMath, "-"); ok {
		pos = p.toks[p.i].pos
	}
	for _, ok := p.peekOp(tOperMath, "-"); ok; _, ok = p.peekOp(tOperMath, "-") {
		neg = !neg
		p.i++
	}
	x, err := p.binary(depth, p.pathExpr, tOperUni, "|")
	if err != nil || !neg {
		// libyang: an even number of '-' leaves the operand uncast
		return x, err
	}
	return negExpr{x, pos}, nil
}

// pathExpr is reparse_path_expr: [10] PathExpr ::= LocationPath | PrimaryExpr Predicate* (('/' | '//') RelativeLocationPath)?
func (p *parser) pathExpr(depth int) (ast, error) {
	if err := p.check(tNone); err != nil {
		return nil, err
	}
	var pe pathExpr
	var err error
	switch t := p.toks[p.i]; t.k {
	case tPar1:
		p.i++
		if pe.prim, err = p.orExpr(depth); err != nil {
			return nil, err
		}
		if err = p.check(tPar2); err != nil {
			return nil, err
		}
		p.i++
	case tDot, tDDot, tAxisName, tAt, tNameTest, tNodeType:
		pe.steps, err = p.relPath(depth, false)
		return pe, err
	case tVarRef:
		pe.prim = varExpr(p.text(p.i))
		p.i++
	case tFuncName:
		if pe.prim, err = p.call(depth); err != nil {
			return nil, err
		}
	case tOperPath, tOperRPath:
		pe.abs = true
		p.i++
		if t.k == tOperRPath {
			pe.steps, err = p.relPath(depth, true)
		} else if p.peek(tDot, tDDot, tAxisName, tAt, tNameTest, tNodeType) {
			pe.steps, err = p.relPath(depth, false)
		}
		return pe, err
	case tLiteral:
		pe.prim = litExpr(p.src[t.pos+1 : t.pos+t.len-1])
		p.i++
	case tNumber:
		pe.prim = numExpr{parseNumberToken(p.text(p.i))}
		p.i++
	default:
		return nil, p.unexpected()
	}
	if pe.preds, err = p.predicates(depth); err != nil {
		return nil, err
	}
	if p.peek(tOperPath, tOperRPath) {
		all := p.toks[p.i].k == tOperRPath
		p.i++
		pe.steps, err = p.relPath(depth, all)
	}
	if pe.preds == nil && pe.steps == nil {
		return pe.prim, err
	}
	return pe, err
}

func (p *parser) predicates(depth int) ([]ast, error) {
	var preds []ast
	for p.peek(tBrack1) {
		p.i++
		e, err := p.orExpr(depth)
		if err != nil {
			return nil, err
		}
		if err = p.check(tBrack2); err != nil {
			return nil, err
		}
		p.i++
		preds = append(preds, e)
	}
	return preds, nil
}

// relPath is reparse_relative_location_path: [4] RelativeLocationPath ::= Step (('/' | '//') Step)*.
func (p *parser) relPath(depth int, allDesc bool) ([]step, error) {
	var steps []step
	for {
		if err := p.check(tNone); err != nil {
			return nil, err
		}
		s := step{allDesc: allDesc, axis: "child"}
		switch p.toks[p.i].k {
		case tDot, tDDot:
			s.axis, s.test, s.name = "", p.toks[p.i].k, p.text(p.i)
			p.i++
		case tAxisName, tAt, tNameTest, tNodeType:
			if p.peek(tAxisName) {
				s.axis, s.explicit = p.text(p.i), true
				p.i += 2 // AxisName '::' (the lexer always pairs them)
			} else if p.peek(tAt) {
				s.axis, s.explicit = "attribute", true
				p.i++
			}
			if err := p.check(tNone); err != nil {
				return nil, err
			}
			s.test, s.name, s.end = p.toks[p.i].k, p.text(p.i), p.toks[p.i].pos+p.toks[p.i].len
			switch s.test {
			case tNameTest:
				p.i++
			case tNodeType:
				p.i++
				for _, k := range []tokKind{tPar1, tPar2} {
					if err := p.check(k); err != nil {
						return nil, err
					}
					p.i++
				}
			default:
				return nil, p.unexpected()
			}
			var err error
			if s.preds, err = p.predicates(depth); err != nil {
				return nil, err
			}
		default:
			return nil, p.unexpected()
		}
		steps = append(steps, s)
		if !p.peek(tOperPath, tOperRPath) {
			return steps, nil
		}
		allDesc = p.toks[p.i].k == tOperRPath
		p.i++
	}
}

// call is reparse_function_call: [9] FunctionCall ::= FunctionName '(' ( Expr ( ',' Expr )* )? ')'.
func (p *parser) call(depth int) (ast, error) {
	name := p.text(p.i)
	arity, ok := funcArity[name]
	if !ok {
		return nil, xpErr("Unknown XPath function \"%s\".", name)
	}
	p.i++
	if err := p.check(tPar1); err != nil {
		return nil, err
	}
	p.i++
	if err := p.check(tNone); err != nil {
		return nil, err
	}
	c := callExpr{name: name}
	for !p.peek(tPar2) || len(c.args) > 0 {
		if len(c.args) > 0 {
			if !p.peek(tComma) {
				break
			}
			p.i++
		}
		a, err := p.orExpr(depth)
		if err != nil {
			return nil, err
		}
		c.args = append(c.args, a)
	}
	if err := p.check(tPar2); err != nil {
		return nil, err
	}
	p.i++
	if len(c.args) < arity[0] || len(c.args) > arity[1] {
		return nil, xpErr("Invalid number of arguments (%d) for the XPath function %s.", len(c.args), name)
	}
	return c, nil
}
