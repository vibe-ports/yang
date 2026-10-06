// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/xpath.c (BSD-3-Clause, © CESNET).

// Package xpath parses and evaluates XPath 1.0 expressions with the YANG
// function library (RFC 7950 §10) over a data tree it reaches only through
// the Node interface, following libyang v5.8.6 semantics — including its
// quirks, which are marked "libyang:" in comments.
//
// The evaluation context is explicit (docs/design/03-xpath-context.md):
// the namespace context is bound at Compile, the context node, accessible
// tree and hooks at Eval.
package xpath

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Kind is the schema node type of a data node.
type Kind uint8

// Schema node kinds.
const (
	KindContainer Kind = iota + 1
	KindList
	KindLeaf
	KindLeafList
	KindAnydata
	KindAnyxml
	KindRPC
	KindAction
	KindNotif
)

// WhenState is the when-condition status of a data node.
type WhenState uint8

// When states.
const (
	WhenTrue       WhenState = iota // no when, or evaluated true
	WhenUnresolved                  // has a when that was not evaluated yet
	WhenFalse                       // when evaluated false: the node is invisible to XPath
)

// Ident names an identity.
type Ident struct{ Module, Name string }

// Value is read-only access to a term node's STORED value. YANG functions use
// the typed accessors, never the string: an identityref and a string union
// member can have the same canonical text (libyang xpath.c xpath_derived_).
type Value interface {
	String() string // canonical value (anydata/anyxml: serialized content)
	// Identity: ok only when the stored value (the selected union member) is an identityref.
	Identity() (module, name string, ok bool)
	// Enum: ok only when the leaf's schema type is an enumeration (libyang checks
	// the schema type, so an enum union member gives ok=false).
	Enum() (int, bool)
	// Bits: ok only when the stored value (the selected union member) is bits.
	Bits() ([]string, bool)
}

// Node is a read-only data-tree node. Implementations must be comparable
// (pointer types): node identity is ==.
type Node interface {
	Parent() Node     // nil for top-level nodes
	Children() []Node // document order
	Name() string
	Module() string     // module name
	Schema() SchemaNode // nil for opaque nodes, which never match a name test
	Value() Value       // nil for non-term nodes
	When() WhenState
}

// SchemaNode is what the evaluator needs from a data node's schema node.
// Implementations must be comparable: all instances of one schema node
// return the same (==) SchemaNode.
type SchemaNode interface {
	Kind() Kind
	Module() string    // module name
	Config() bool      // config true
	Namespace() string // module namespace URI
	Keys() []string    // list key names in order; nil for other nodes and keyless lists
	// Child is the data child (through choice/case, incl. augments) of module
	// with that name, as lys_getnext; for an RPC/action from its input or
	// output, nil when both have one. nil if none.
	Child(module, name string) SchemaNode
	// Canonical returns the canonical form of lexical for this node's type,
	// ok=false if the value is invalid or needs no canonization.
	Canonical(lexical string) (canon string, ok bool)
}

// SchemaInfo is the schema-wide hook: identities for derived-from[-or-self]()
// and top-level schema nodes for unprefixed names at the document root.
type SchemaInfo interface {
	HasIdentity(id Ident) bool
	IsDerived(base, id Ident) bool // id is strictly derived from base
	// TopLevel lists the top-level data nodes named name of the implemented
	// module (of all implemented modules when module is "").
	TopLevel(module, name string) []SchemaNode
}

// NamespaceCtx binds prefixes for one expression (design 03, rule 1).
type NamespaceCtx interface {
	// Resolve maps a prefix to an implemented module name.
	Resolve(prefix string) (module string, ok bool)
	// Prefix is the prefix name() prints for module.
	Prefix(module string) string
	// Default is the module of unprefixed names; "" matches any module (JSON format).
	Default() string
}

// RootKind selects the accessible tree.
type RootKind uint8

// Root kinds (libyang LYXP_NODE_ROOT / LYXP_NODE_ROOT_CONFIG).
const (
	RootAll    RootKind = iota // config + state
	RootConfig                 // config false nodes are invisible
)

// EvalContext is the explicit evaluation context.
type EvalContext struct {
	Ctx        context.Context // cancellation; nil = Background
	Node       Node            // context node; nil = document root
	Current    Node            // current(); nil = Node
	Tree       []Node          // top-level siblings in document order; every node reached is under them
	Root       RootKind
	IgnoreWhen bool // LYXP_IGNORE_WHEN: do not fail on unresolved when
	Schema     SchemaInfo
	// Deref resolves a leafref / instance-identifier node to its targets.
	Deref    func(Node) ([]Node, error)
	MaxSteps int // evaluation step budget; 0 = DefaultMaxSteps
}

// DefaultMaxSteps bounds the work of one evaluation.
const DefaultMaxSteps = 10_000_000

// ResultType is the XPath type of an evaluation result.
type ResultType uint8

// Result types.
const (
	NodeSet ResultType = iota
	Boolean
	Number
	String
)

// Result is an evaluation result. Nodes holds only element nodes (as libyang
// lyd_eval_xpath4: the document root and text nodes are dropped).
type Result struct {
	Type  ResultType
	Nodes []Node
	Bool  bool
	Num   float64
	Str   string
	// Steps is the budget the evaluation consumed (also set on error), for
	// callers that keep a cumulative budget across evaluations.
	Steps int64
}

// Error is an XPath error with libyang's LY_ERR / LYVE codes.
type Error struct {
	Err    string // LY_EVALID, LY_EINVAL, LY_ENOTFOUND
	VECode string // LYVE_XPATH, LYVE_DATA, or "" for none
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

var (
	// ErrIncomplete means a node with an unresolved when was reached (libyang LY_EINCOMPLETE).
	ErrIncomplete = errors.New("xpath: unresolved when condition")
	// ErrBudget means the step budget was exhausted.
	ErrBudget = errors.New("xpath: evaluation step budget exceeded")
)

func xpErr(format string, a ...any) *Error {
	return &Error{Err: "LY_EVALID", VECode: "LYVE_XPATH", Msg: fmt.Sprintf(format, a...)}
}

// Expr is a compiled expression.
type Expr struct {
	src  string
	root ast
	ns   NamespaceCtx
	res  sync.Map     // re-match() pattern → *xsdre.Pattern or error
	nres atomic.Int32 // patterns cached in res, capped at maxCachedPatterns
}

// String returns the source text.
func (e *Expr) String() string { return e.src }

// Compile tokenizes and parses src (lyxp_expr_parse with reparse). Unknown
// functions and wrong argument counts are compile errors; prefixes are
// resolved through ns at evaluation, as libyang does.
func Compile(src string, ns NamespaceCtx) (*Expr, error) {
	toks, src, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{src: src, toks: toks}
	root, err := p.orExpr(0)
	if err != nil {
		return nil, err
	}
	if p.i < len(toks) {
		return nil, xpErr("Unparsed characters \"%s\" left at the end of an XPath expression.", src[toks[p.i].pos:])
	}
	return &Expr{src: src, root: root, ns: ns}, nil
}

// Eval evaluates e (lyxp_eval).
func (e *Expr) Eval(ec EvalContext) (Result, error) {
	ev := newEvaluator(e, &ec)
	v, err := ev.eval(e.root, ev.start())
	if err == nil {
		err = ev.err
	}
	used := int64(ev.budget) - int64(max(ev.steps, 0))
	if err != nil {
		return Result{Steps: used}, err
	}
	switch v.t {
	case vBool:
		return Result{Type: Boolean, Bool: v.b, Steps: used}, nil
	case vNum:
		return Result{Type: Number, Num: v.f.float(), Steps: used}, nil
	case vStr:
		return Result{Type: String, Str: v.s, Steps: used}, nil
	}
	r := Result{Type: NodeSet, Steps: used}
	for _, it := range v.nodes {
		if it.t == itElem {
			r.Nodes = append(r.Nodes, it.n)
		}
	}
	return r, nil
}
