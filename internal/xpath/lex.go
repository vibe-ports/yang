// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (BSD-3-Clause, © CESNET).

package xpath

import (
	"strings"

	"github.com/vibe-ports/yang/internal/lyxp"
)

// Token kinds are lyxp's (enum lyxp_token).
type tokKind = lyxp.Tok

const (
	tNone       = lyxp.TokNone
	tPar1       = lyxp.TokPar1
	tPar2       = lyxp.TokPar2
	tBrack1     = lyxp.TokBrack1
	tBrack2     = lyxp.TokBrack2
	tDot        = lyxp.TokDot
	tDDot       = lyxp.TokDDot
	tAt         = lyxp.TokAt
	tComma      = lyxp.TokComma
	tDColon     = lyxp.TokDColon
	tNameTest   = lyxp.TokNameTest
	tNodeType   = lyxp.TokNodeType
	tVarRef     = lyxp.TokVarRef
	tFuncName   = lyxp.TokFuncName
	tOperLog    = lyxp.TokOperLog
	tOperEqual  = lyxp.TokOperEqual
	tOperNEqual = lyxp.TokOperNEqual
	tOperComp   = lyxp.TokOperComp
	tOperMath   = lyxp.TokOperMath
	tOperUni    = lyxp.TokOperUni
	tOperPath   = lyxp.TokOperPath
	tOperRPath  = lyxp.TokOperRPath
	tAxisName   = lyxp.TokAxisName
	tLiteral    = lyxp.TokLiteral
	tNumber     = lyxp.TokNumber
)

type token struct {
	k        tokKind
	pos, len int
}

func isXMLWS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// MaxTokens caps the size of an expression (and so of its AST); libyang's only
// limit is the UINT32_MAX expression length (U-0003).
const MaxTokens = 1 << 22

// lex tokenizes through lyxp.Lex (lyxp_expr_parse) and returns the source truncated at the
// first NUL (the C string ends there).
func lex(src string) ([]token, string, error) {
	if i := strings.IndexByte(src, 0); i >= 0 {
		src = src[:i]
	}
	e, msg, tooMany := lyxp.LexMax(src, MaxTokens)
	switch {
	case tooMany:
		return nil, src, xpErr("XPath expression has more than %d tokens.", MaxTokens)
	case msg != "":
		return nil, src, xpErr("%s", msg)
	}
	toks := make([]token, len(e.Toks))
	for i, k := range e.Toks {
		toks[i] = token{k, e.Pos[i], e.Len[i]}
	}
	return toks, src, nil
}
