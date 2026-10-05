// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_features.c (BSD-3-Clause, © CESNET).

package parser

import (
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
)

// IfFeature is an if-feature argument and its parsed expression. libyang
// checks the expression only when it compiles the statement (lys_compile_iffeature),
// so Parse and Build do not fail on it: Err holds libyang's error for the
// compiler to report (with a schema path, so Err.Pos is empty).
type IfFeature struct {
	Expr string
	AST  *IffExpr
	Err  *Error
}

// IffOp is an if-feature expression node kind.
type IffOp uint8

// If-feature operators (RFC 7950 §7.20.2), with libyang's LYS_IFF_* values.
const (
	IffNot     IffOp = iota // X
	IffAnd                  // X and Y
	IffOr                   // X or Y
	IffFeature              // Name, a feature reference (prefix:name or name)
	iffRP      IffOp = 8    // LYS_IFF_RP, ')' on the operator stack
)

// IffExpr is an if-feature expression tree. Consumers must walk it without
// recursion: its depth is bounded only by the expression length.
type IffExpr struct {
	Op   IffOp
	Name string
	X, Y *IffExpr
}

func ifFeature(s *Stmt, v11 bool) *IfFeature {
	ast, why := parseIfFeature(s.Arg, v11)
	f := &IfFeature{Expr: s.Arg, AST: ast}
	if why != "" {
		f.Err = &Error{Code: ly.SyntaxYang, Msg: "Invalid value \"" + s.Arg + "\" of if-feature - " + why}
	}
	return f
}

// parseIfFeature is lys_compile_iffeature without the feature lookup: the
// same two passes (syntax pre-check, then a right-to-left shunting yard into
// prefix order), iterative, returning the expression tree or libyang's reason.
// Where libyang's second pass would read outside its arrays (e.g. "a)(",
// which crashes yanglint 5.8.6, D-0020) the result is "processing error.".
func parseIfFeature(c string, v11 bool) (*IffExpr, string) {
	if i := strings.IndexByte(c, 0); i >= 0 {
		c = c[:i] // a C string ends at NUL (arguments from Parse never contain one)
	}
	at := func(i int) byte {
		if i >= 0 && i < len(c) {
			return c[i]
		}
		return 0
	}
	isSpace := func(i int) bool { return i < len(c) && isCSpace(c[i]) }
	var j, fSize, exprSize int
	fExp, lastNot, checkVersion := 1, false, false
	i := 0
	for ; i < len(c); i++ {
		switch {
		case c[i] == '(':
			j++
			checkVersion = true
			continue
		case c[i] == ')':
			j--
			continue
		case isCSpace(c[i]):
			checkVersion = true
			continue
		}
		opLen := 0
		for _, op := range []string{"not", "and", "or"} {
			if strings.HasPrefix(c[i:], op) {
				opLen = len(op)
				break
			}
		}
		if opLen > 0 {
			k := i + opLen
			for isSpace(k) {
				k++
			}
			switch {
			case k >= len(c):
				return nil, "unexpected end of expression."
			case !isSpace(i + opLen): // a feature name starting with not/and/or
				lastNot = false
				fSize++
			case c[i] == 'n':
				if lastNot { // double not
					exprSize -= 2
					lastNot = false
				} else {
					lastNot = true
				}
			default:
				if fExp != fSize {
					return nil, "missing feature/expression before \"" + c[i:i+opLen] + "\" operation."
				}
				fExp++
				lastNot = false
			}
			i += opLen
		} else {
			fSize++
			lastNot = false
		}
		exprSize++
		for !isSpace(i) {
			if b := at(i); b == 0 || b == ')' || b == '(' {
				i--
				break
			}
			i++
		}
	}
	switch {
	case j != 0:
		return nil, "non-matching opening and closing parentheses."
	case fExp != fSize:
		return nil, "number of features in expression does not match the required number of operands for the operations."
	case (checkVersion || exprSize > 1) && !v11:
		return nil, "YANG 1.1 expression in YANG 1.0 module."
	}

	const processing = "processing error."
	ops := make([]IffOp, exprSize)
	names := make([]string, fSize)
	e, f := exprSize-1, fSize-1
	var stack []IffOp
	set := func(op IffOp) bool {
		if e < 0 || op > IffFeature {
			return false
		}
		ops[e] = op
		e--
		return true
	}
	for i--; i >= 0; i-- {
		switch {
		case c[i] == ')':
			stack = append(stack, iffRP)
			continue
		case c[i] == '(':
			for {
				if len(stack) == 0 {
					return nil, processing
				}
				op := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if op == iffRP {
					break
				}
				if !set(op) {
					return nil, processing
				}
			}
			continue
		case isCSpace(c[i]):
			continue
		}
		end := i + 1
		for i >= 0 && !isCSpace(c[i]) && c[i] != '(' {
			i--
		}
		i++
		switch tok := c[i:]; {
		case strings.HasPrefix(tok, "not") && isSpace(i+3):
			if n := len(stack); n > 0 && stack[n-1] == IffNot { // double not
				stack = stack[:n-1]
			} else {
				stack = append(stack, IffNot)
			}
		case strings.HasPrefix(tok, "and") && isSpace(i+3), strings.HasPrefix(tok, "or") && isSpace(i+2):
			op := IffAnd
			if tok[0] == 'o' {
				op = IffOr
			}
			for n := len(stack); n > 0 && stack[n-1] <= op; n = len(stack) {
				if !set(stack[n-1]) {
					return nil, processing
				}
				stack = stack[:n-1]
			}
			stack = append(stack, op)
		default:
			if f < 0 || !set(IffFeature) {
				return nil, processing
			}
			names[f] = c[i:end]
			f--
		}
	}
	for n := len(stack); n > 0; n = len(stack) {
		if !set(stack[n-1]) {
			return nil, processing
		}
		stack = stack[:n-1]
	}
	if e != -1 || f != -1 {
		return nil, processing
	}

	// prefix order → tree, right to left with an operand stack
	var vals []*IffExpr
	f = fSize - 1
	for k := len(ops) - 1; k >= 0; k-- {
		x := &IffExpr{Op: ops[k]}
		need := 2 // operands
		switch x.Op {
		case IffFeature:
			need = 0
		case IffNot:
			need = 1
		}
		switch {
		case len(vals) < need:
			return nil, processing
		case need == 0:
			x.Name = names[f]
			f--
		default:
			x.X, vals = vals[len(vals)-1], vals[:len(vals)-1]
			if need == 2 {
				x.Y, vals = vals[len(vals)-1], vals[:len(vals)-1]
			}
		}
		vals = append(vals, x)
	}
	if len(vals) != 1 {
		return nil, processing
	}
	return vals[0], ""
}

// Eval evaluates the expression with enabled deciding each feature
// (lysc_iffeature_value), iteratively.
func (e *IffExpr) Eval(enabled func(name string) bool) bool {
	type item struct {
		x    *IffExpr
		done bool // operands evaluated
	}
	stack := []item{{x: e}}
	var vals []bool
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		n := len(vals)
		switch {
		case it.x.Op == IffFeature:
			vals = append(vals, enabled(it.x.Name))
		case !it.done:
			stack = append(stack, item{it.x, true})
			if it.x.Y != nil {
				stack = append(stack, item{x: it.x.Y})
			}
			stack = append(stack, item{x: it.x.X})
		case it.x.Op == IffNot:
			vals[n-1] = !vals[n-1]
		case it.x.Op == IffAnd:
			vals = append(vals[:n-2], vals[n-2] && vals[n-1])
		default:
			vals = append(vals[:n-2], vals[n-2] || vals[n-1])
		}
	}
	return vals[0]
}
