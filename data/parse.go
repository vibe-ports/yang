// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data.c (lyd_parse, lyd_parse_op), src/parser_common.c (the
// data parser helpers lyd_parser_*), src/parser_internal.h (struct lyd_ctx, LY_DPARSER_ERR_GOTO) and
// src/tree_data_new.c (lyd_create_term, lyd_create_inner, lyd_create_opaq),
// src/tree_data_sorted.c (lyds_insert, lyds_additionally_create_rb_tree) (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyjson"
	"github.com/vibe-ports/yang/internal/lyxml"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// Format is a data encoding.
type Format uint8

// Data formats.
const (
	FormatJSON Format = iota // RFC 7951
	FormatXML                // RFC 7950 XML encoding
)

// UnknownPolicy is what parsing does with a node the schema does not have.
type UnknownPolicy uint8

// Unknown node policies.
const (
	Reject UnknownPolicy = iota // an error (LYD_PARSE_STRICT)
	Skip                        // ignored
	Opaque                      // kept as an opaque node (LYD_PARSE_OPAQ)
)

// ValidateOptions are libyang's LYD_VALIDATE_* options the M1 fixtures use.
type ValidateOptions struct {
	NoState     bool // LYD_VALIDATE_NO_STATE
	Present     bool // LYD_VALIDATE_PRESENT
	MultiError  bool // LYD_VALIDATE_MULTI_ERROR
	Operational bool // LYD_VALIDATE_OPERATIONAL
	NoDefaults  bool // LYD_VALIDATE_NO_DEFAULTS
}

// ParseOptions are libyang's LYD_PARSE_* options the M1 fixtures use, the validation that follows
// unless ParseOnly, and the resource limits.
type ParseOptions struct {
	Unknown   UnknownPolicy
	ParseOnly bool // LYD_PARSE_ONLY
	// WhenTrue marks parsed nodes with when conditions as previously valid, so
	// validation removes them when their conditions are false (LYD_PARSE_WHEN_TRUE).
	WhenTrue bool
	NoState  bool // LYD_PARSE_NO_STATE (with ValidateOptions.NoState for the validation part)
	// StoreOnly is LYD_PARSE_STORE_ONLY: values are stored without their restriction checks. It
	// implies ParseOnly: libyang's flag value includes LYD_PARSE_ONLY.
	StoreOnly bool
	// JSONNull is LYD_PARSE_JSON_NULL: a JSON null value creates nothing.
	JSONNull bool
	// AnyDataStrict is LYD_PARSE_ANYDATA_STRICT: anydata and anyxml payloads are parsed with
	// libyang's strict schema lookup instead of falling back to opaque nodes.
	AnyDataStrict bool
	// JSONStringDatatypes is LYD_PARSE_JSON_STRING_DATATYPES: JSON numbers and booleans may
	// come as strings.
	JSONStringDatatypes bool
	Validate            ValidateOptions
	Budget              Budget
}

// Budget limits what one Parse may use; zero fields take the defaults, exceeding one fails with
// an error wrapping yang.ErrBudget. libyang has none of these limits (deviations.md U-0040,
// U-0041).
type Budget struct {
	MaxBytes int // input size, default DefaultMaxBytes
	MaxNodes int // data nodes created, implicit ones included, default DefaultMaxNodes
	// MaxXPathSteps caps the XPath work of the validation (U-0042), default DefaultMaxXPathSteps
	MaxXPathSteps int64
}

// Defaults of Budget.
const (
	DefaultMaxBytes = 256 << 20
	DefaultMaxNodes = 1 << 22
)

// parseOpts are the LYD_PARSE_* options, the internal ones included.
type parseOpts struct {
	ParseOptions
	anyContent bool // LYD_INTOPT_ANY, for any payload nodes in JSON and internal text parsing
	zeroNodes  bool // an internal aggregate budget with no remaining capacity; zero Budget means default
	ordered    bool // LYD_PARSE_ORDERED: input is in schema order, siblings are appended
	storeOnly  bool // LYD_PARSE_STORE_ONLY: no restriction checks of values
	whenTrue   bool // LYD_PARSE_WHEN_TRUE: nodes with a when start as FlagWhenTrue
	noNew      bool // LYD_PARSE_NO_NEW: nodes are not FlagNew
	// LYD_PARSE_JSON_NULL: a JSON null value creates nothing
	jsonNull bool
	// LYD_PARSE_JSON_STRING_DATATYPES: numbers and booleans may come as JSON strings
	jsonStringDatatypes bool
}

// lydCtx is struct lyd_ctx: the state the format parsers share with the common helpers.
type lydCtx struct {
	ctx   context.Context
	tree  *Tree
	log   *logger
	opts  parseOpts
	nodes int     // nodes created (Budget.MaxNodes)
	vc    *valCtx // the validation context of the parse (valCtx)
	// limitHit: a libyang nesting limit of the lexers stopped the parse (the error wraps
	// yang.ErrBudget too)
	limitHit bool
	// The work queues of the Parse path (design 07 §1.5): filled in parse order and handed to the
	// validation, where the first module traversed drains them for every module.
	nodeWhen  nodeSet // node_when: nodes with a when, after their children (post-order)
	nodeTypes nodeSet // node_types: values that still need the data tree (LY_EINCOMPLETE)
	metaTypes []*meta // meta_types: metadata values that still need the data tree
	// validateNewImplicit is lyd_parser_validate_new_implicit, run when an inner node closes
	// without an error inside it: lyd_validate_new of its children and their implicit nodes
	// (design 07 D8; nil until then).
	validateNewImplicit func(lc *lydCtx, n *Node) error
	// validate is the lyd_validate of the Parse path over the queues (design 07 D10; nil until
	// then).
	validate func(lc *lydCtx) error
	// op is the operation part of the internal options (LYD_INTOPT_*), zero for datastore data;
	// parent is the node parsed into (nil: the top level) and opNode the operation node
	// (lyd_ctx.op_node), opSchema its schema node: the JSON parser also records an opaque node
	// parsed for an operation member (D-0110).
	op       opOpts
	parent   *Node
	opNode   *Node
	opSchema *schema.Node
	// rcOnly is lyd_parse_op's result rule: the format parser's return code is the result, so an
	// error it logged and went past (the YANG action element, lydxml_envelope) does not fail it
	rcOnly bool
	parsed []*Node // the nodes parsed directly under parent (lyd_parse_op's parsed set)
	// dirty holds, per sibling set, the system-ordered runs appended in parser order after
	// the first out-of-order value; dirtySibs is the order the sets first got one. Runs are stably
	// sorted before any parser reader can observe their order; a flush touches only its own set.
	dirty     map[*siblings][]*schema.Node
	dirtySibs []*siblings
	dirtySet  map[dirtyRun]bool        // the runs pending in dirty, for a constant-time repeat mark
	dirtyOrd  map[dirtyRun]*dirtyOrder // the dirty runs' trees
	runOrd    map[dirtyRun]*dirtyOrder // every run's tree once the parser inserted into it
	pos       struct {                 // position's cache: one sibling set at one generation
		sib *siblings
		gen uint64
		at  map[*Node]int
	}
}

// position is the index of n in sib.list for the parser's readers (metadata walks over split
// runs), cached while sib does not change (sib.gen): repeated metadata members for one run cost
// one scan. The cache belongs to this parse (a mutation), never to the shared sibling state that
// concurrent readers use.
func (lc *lydCtx) position(sib *siblings, n *Node) int {
	if lc.pos.sib != sib || lc.pos.gen != sib.gen {
		lc.pos.sib, lc.pos.gen, lc.pos.at = sib, sib.gen, nil
	}
	if at, ok := lc.pos.at[n]; ok {
		return at
	}
	at := indexFromEnd(&lc.tree.work, sib.list, n)
	if lc.pos.at == nil {
		lc.pos.at = map[*Node]int{}
	}
	lc.pos.at[n] = at
	return at
}

type dirtyRun struct {
	sib *siblings
	sn  *schema.Node
}

// opOpts are the LYD_INTOPT_* options of an operation parse.
type opOpts struct {
	rpc, action, notif, reply bool
	noSiblings                bool // LYD_INTOPT_NO_SIBLINGS: one top-level node only
	// eventTime is LYD_INTOPT_EVENTTIME: a top-level eventTime node is parsed and validated. Only
	// the NETCONF/RESTCONF notification envelopes set it (#95).
	eventTime bool
}

func (o opOpts) any() bool { return o.rpc || o.action || o.notif || o.reply }

// opType is the operation lyd_parse_op parses (LYD_TYPE_*_YANG).
type opType uint8

const (
	opRPC   opType = iota + 1 // LYD_TYPE_RPC_YANG: an rpc or action
	opNotif                   // LYD_TYPE_NOTIF_YANG
	opReply                   // LYD_TYPE_REPLY_YANG
)

// parseOp is lyd_parse_op for the YANG operation types (no NETCONF/RESTCONF envelopes): the
// operation in format f, parsed only (LYD_PARSE_ONLY), with the unknown-node policy (Skip is
// libyang's parse options 0: unknown nodes are dropped). With parent nil the result is a new tree holding the
// operation and its parents; with parent, the nodes are parsed as its children (a reply into
// its rpc or action) and the tree is parent's. op is the operation node. On error the nodes
// parsed are removed again (from parent's tree) and the tree is nil.
func parseOp(ctx context.Context, r io.Reader, s *schema.Set, f Format, t opType, parent *Node,
	unknown UnknownPolicy) (tree *Tree, op *Node, diags []yang.Diagnostic, err error) {
	return parseOpWith(ctx, r, s, f, t, parent, unknown, Budget{})
}

// parseOpWith is parseOp with the resource limits b.
func parseOpWith(ctx context.Context, r io.Reader, s *schema.Set, f Format, t opType, parent *Node,
	unknown UnknownPolicy, b Budget) (tree *Tree, op *Node, diags []yang.Diagnostic, err error) {
	var oo opOpts
	switch t {
	case opRPC:
		oo.rpc, oo.action = true, true
	case opNotif:
		oo.notif = true
	case opReply:
		oo.reply = true
	default:
		return nil, nil, nil, fmt.Errorf("data: unknown operation type %d", t)
	}
	oo.noSiblings = parent == nil
	fp := parseJSON
	if f == FormatXML {
		fp = parseXML
	}
	var lc *lydCtx
	o := parseOpts{ParseOptions: ParseOptions{Unknown: unknown, ParseOnly: true, Budget: b}}
	tree, diags, err = parseWith(ctx, r, s, o, fp, func(c *lydCtx) {
		lc, c.op, c.parent, c.rcOnly = c, oo, parent, true
		if parent != nil {
			if pt := parent.treeOf(); pt != nil {
				c.tree = pt
			} // a detached parent (LYD_CTX(parent)): the parser's own tree of s stands in for lookups
		}
	})
	if err != nil {
		if lc != nil && parent != nil {
			// Cleanup may log key refusals after the parse error; keep the parse RC.
			var ve *ValidationError
			if errors.As(err, &ve) {
				ve.rc = ve.RC()
			}
			lc.freeParsed() // nil lc: the input was not read, nothing was parsed
			diags = lc.log.diags
			if ve != nil {
				ve.Diags = diags
			}
		}
		return nil, nil, diags, err
	}
	if parent != nil {
		tree = parent.treeOf() // libyang returns no tree with a parent; nil for a detached parent
	}
	return tree, lc.opNode, diags, nil
}

// freeParsed frees the nodes parsed under lc.parent again (lyd_parse_op's cleanup of its parsed
// set), in one bulk unlink. lyd_free_tree refuses keys, logging once per key in the
// cleanup order (tree_data.c:485-489 walks the parsed set backwards).
func (lc *lydCtx) freeParsed() {
	for i := len(lc.parsed) - 1; i >= 0; i-- {
		lc.tree.work.Add(1)
		if n := lc.parsed[i]; n.isKey() {
			// freeTree returns the same pathless LOGERR as lyd_free_tree; the key stays.
			_ = lc.log.done(freeTree(n))
		}
	}
	ns := slices.DeleteFunc(lc.parsed, func(n *Node) bool { return n.isKey() })
	_ = lc.tree.unlinkAll(ns) // no keys left: unlinkCheck cannot refuse
	for _, n := range ns {
		freeSubtreeLinks(lc.tree.set, n)
	}
	lc.parsed = nil
}

// findOperation is lyd_parser_find_operation: the operation node among parent and its ancestors,
// when the operation parsed allows it there.
func (lc *lydCtx) findOperation() error {
	lc.opNode = nil
	var it *Node
	for it = lc.parent; it != nil; it = it.parent {
		if it.schema != nil && (it.schema.Kind == schema.RPC || it.schema.Kind == schema.Action ||
			it.schema.Kind == schema.Notification) {
			break
		}
	}
	if it == nil {
		return nil
	}
	o, sn := lc.op, it.schema
	switch {
	case !o.any():
		return lc.log.logErr("LY_EINVAL", "Invalid parent %s \"%s\" node when not parsing any operation.",
			nodetypeStr(sn.Kind), sn.Name)
	case sn.Kind == schema.RPC && !o.rpc && !o.reply:
		return lc.log.logErr("LY_EINVAL", "Invalid parent RPC \"%s\" node when not parsing RPC nor rpc-reply.", sn.Name)
	case sn.Kind == schema.Action && !o.action && !o.reply:
		return lc.log.logErr("LY_EINVAL", "Invalid parent action \"%s\" node when not parsing action nor rpc-reply.",
			sn.Name)
	case sn.Kind == schema.Notification && !o.notif:
		return lc.log.logErr("LY_EINVAL", "Invalid parent notification \"%s\" node when not parsing a notification.",
			sn.Name)
	}
	lc.opNode, lc.opSchema = it, it.schema
	return nil
}

// getnextOpts are the lys_getnext options of a schema lookup: an rpc-reply looks into outputs.
func (lc *lydCtx) getnextOpts() schema.GetNextOpt {
	if lc.op.reply {
		return schema.GetNextOutput
	}
	return 0
}

// opParsed records n as the operation node when it is one.
func (lc *lydCtx) opParsed(n *Node) {
	if n != nil && n.schema != nil && (n.schema.Kind == schema.RPC || n.schema.Kind == schema.Action ||
		n.schema.Kind == schema.Notification) {
		lc.opNode, lc.opSchema = n, n.schema // remember the RPC/action/notification
	}
}

// formatParser parses the whole input into lc.tree, logging through lc.log; it returns the
// first error that stopped it (lyd_parse_json, lyd_parse_xml).
type formatParser func(lc *lydCtx, in []byte) error

// parseWith is lyd_parse with LYD_INTOPT_WITH_SIBLINGS over the schema set s and the format
// parser fp (the format dispatch comes with D5/D6): the tree, or nil when anything failed
// (libyang frees the whole tree on any error), and the diagnostics. setup prepares the parser
// context (the D8 and D10 hooks, tests).
func parseWith(ctx context.Context, r io.Reader, s *schema.Set, o parseOpts, fp formatParser,
	setup func(*lydCtx)) (*Tree, []yang.Diagnostic, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	maxBytes := orDefault(o.Budget.MaxBytes, DefaultMaxBytes)
	in, err := io.ReadAll(io.LimitReader(r, int64(maxBytes)+1))
	switch {
	case err != nil:
		return nil, nil, err
	case len(in) > maxBytes:
		return nil, nil, fmt.Errorf("%w: data input larger than %d bytes", yang.ErrBudget, maxBytes)
	}
	lc := &lydCtx{ctx: ctx, tree: newTree(s), log: &logger{set: s}, opts: o}
	lc.log.beforePath = lc.flushPath
	lc.validateNewImplicit = (*lydCtx).newImplicitClose
	lc.validate = (*lydCtx).validateParsed
	if setup != nil {
		setup(lc)
	}
	err = fp(lc, in)
	// Flush even after an error, before validation can inspect the retained nodes.
	if ferr := lc.flushDirtyAll(); ferr != nil {
		err = ferr
	}
	if err == nil || !lc.fatal(err) {
		if !o.ParseOnly && lc.validate != nil {
			// a budget or cancellation error of the validation outranks logged parse errors
			if verr := lc.validate(lc); verr != nil && (err == nil || !errors.Is(verr, errLogged)) {
				err = verr
			}
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && !lc.rcOnly {
		err = lc.log.result()
	} else if errors.Is(err, errLogged) {
		ve := &ValidationError{Diags: lc.log.diags}
		if re := (*rcErr)(nil); errors.As(err, &re) {
			ve.rc = re.rc
		}
		err = ve
	}
	var ve *ValidationError
	if errors.As(err, &ve) && ve.Diags == nil {
		ve.Diags = lc.log.diags // a silent lexer error keeps what was logged before it
	}
	var rc rcError
	if errors.As(err, &rc) {
		err = &ValidationError{Diags: lc.log.diags, rc: string(rc)}
	}
	if lc.limitHit && errors.As(err, &ve) {
		ve.err = yang.ErrBudget
	}
	if err != nil {
		return nil, lc.log.diags, err // lyd_free_all: an invalid input yields no tree
	}
	return lc.tree, lc.log.diags, nil
}

func orDefault(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}

// lexErr logs an error of the JSON or XML lexer as libyang does (LOGVAL with the parser's
// location, or LOGERR for the nesting limits) and returns errLogged. The nesting limits are
// libyang's own (XML 500 open elements, JSON status stack 5000, design 07 §4): they are logged
// like libyang and the operation's error also wraps yang.ErrBudget.
func (lc *lydCtx) lexErr(err error) error {
	var je *lyjson.Error
	var xe *lyxml.Error
	switch {
	case errors.As(err, &je):
		for _, d := range je.Diags {
			switch d.Code {
			case lyjson.VENone:
				_ = lc.log.logErr("LY_EINVAL", "%s", d.Msg)
			case lyjson.VESemantics:
				lc.log.lexVal(ly.Semantics, d.Msg, int(d.Line)) //nolint:gosec // line numbers fit
			default:
				lc.log.lexVal(ly.Syntax, d.Msg, int(d.Line)) //nolint:gosec // line numbers fit
			}
		}
	case errors.As(err, &xe):
		switch {
		case xe.Msg == "":
			return &ValidationError{err: err, rc: xe.RC} // libyang fails without logging: no diagnostic of its own
		case xe.Code == ly.Success:
			_ = lc.log.logErr("LY_EINVAL", "%s", xe.Msg)
		default:
			lc.log.lexVal(xe.Code, xe.Msg, int(xe.Line)) //nolint:gosec // line numbers fit
		}
	default:
		return err
	}
	if errors.Is(err, lyjson.ErrNesting) || errors.Is(err, lyxml.ErrBudget) {
		lc.limitHit = true
	}
	return errLogged
}

// fatal is the stop test of LY_DPARSER_ERR_GOTO (and of lyd_parse, TD:151): parsing goes on after
// an error only if it is LY_EVALID, multi-error validation is on and the last error's code is
// not exactly LYVE_SYNTAX (LYVE_SYNTAX_JSON/XML continue).
func (lc *lydCtx) fatal(err error) bool {
	if err == nil {
		return false
	}
	if !lc.isEValid(err) || !lc.opts.Validate.MultiError {
		return true
	}
	// the vecode is that of ly_err_last, the last message of any level
	return lc.log.diags[len(lc.log.diags)-1].Code == ly.Syntax.String()
}

// isEValid reports whether err stands for LY_EVALID: a logged error whose last error (r of the
// libyang caller; warnings after it do not change it) is LY_EVALID, and not errLoggedFatal.
func (lc *lydCtx) isEValid(err error) bool {
	if errors.Is(err, errLoggedFatal) || !errors.Is(err, errLogged) {
		return false
	}
	for i := len(lc.log.diags) - 1; i >= 0; i-- {
		if d := lc.log.diags[i]; failing(d) {
			return d.Err == "LY_EVALID"
		}
	}
	return false
}

// errLoggedFatal is a logged error whose libyang return code is not LY_EVALID although its
// message was LOGVAL (LY_EINVAL after an unknown annotation, an invalid value encoding, …): it
// always stops the parse.
var errLoggedFatal = fmt.Errorf("%w (fatal)", errLogged)

// rcErr is errLoggedFatal with the LY_ERR libyang returns (ValidationError.RC).
type rcErr struct{ rc string }

func (e *rcErr) Error() string { return "data: " + e.rc }
func (e *rcErr) Unwrap() error { return errLoggedFatal }

// fatalRC is errLoggedFatal returning rc.
func fatalRC(rc string) error { return &rcErr{rc} }

// countNode charges one created node against Budget.MaxNodes and checks the context every 1k
// nodes.
func (lc *lydCtx) countNode() error {
	lc.nodes++
	limit := orDefault(lc.opts.Budget.MaxNodes, DefaultMaxNodes)
	if lc.opts.zeroNodes {
		limit = 0
	}
	if lc.nodes > limit {
		return fmt.Errorf("%w: more than %d data nodes", yang.ErrBudget, limit)
	}
	if lc.nodes%1024 == 0 {
		return lc.ctx.Err()
	}
	return nil
}

// newFlags are the flags of a created node (lyd_create_*: LYD_NEW unless LYD_PARSE_NO_NEW, which
// lyd_parser_set_data_flags clears).
func (lc *lydCtx) newFlags() Flags {
	if lc.opts.noNew {
		return 0
	}
	return FlagNew
}

// codeOf maps a LY_VECODE name to its code.
func codeOf(name string) ly.Code {
	for c := ly.Success; c <= ly.Other; c++ {
		if c.String() == name {
			return c
		}
	}
	return ly.Other
}

// createTerm is lyd_parser_create_term (lyd_create_term, lyd_value_store): store lex with the
// format's prefix context and hints; a value that still needs the data tree is queued unless
// LYD_PARSE_ONLY. A rejected value is logged at lnode (the data parent) plus the schema node.
func (lc *lydCtx) createTerm(sn *schema.Node, lnode *Node, lex string, f types.Format, pc types.PrefixCtx,
	h types.Hints) (*Node, error) {
	if err := lc.countNode(); err != nil {
		return nil, err
	}
	store := types.Store
	if lc.opts.storeOnly {
		store = types.StoreOnly
	}
	v, d := store(sn.Type, lex, f, h, pc, sn)
	if d != nil {
		return nil, lc.log.storeErr(lnode, sn, d)
	}
	n := newTerm(sn, v)
	n.flags = lc.newFlags()
	if v.NeedsTree() && !lc.opts.ParseOnly {
		lc.nodeTypes.add(n)
	}
	return n, nil
}

// createInner is lyd_create_inner: an NP container starts as default until a non-default child
// is inserted.
func (lc *lydCtx) createInner(sn *schema.Node) (*Node, error) {
	if err := lc.countNode(); err != nil {
		return nil, err
	}
	n := newInner(sn)
	n.flags = lc.newFlags()
	if isNPCont(sn) {
		n.flags |= FlagDefault
	}
	return n, nil
}

// createOpaq is lyd_create_opaq; o.Format is the input format, o.ModuleNS the namespace (XML) or
// module name (JSON) of the node.
func (lc *lydCtx) createOpaq(o opaque) (*Node, error) {
	if err := lc.countNode(); err != nil {
		return nil, err
	}
	o.set = lc.tree.set
	return newOpaque(o), nil // lyd_create_opaq sets no flag (no LYD_NEW)
}

// checkSchema is lyd_parser_check_schema: a state node with LYD_PARSE_NO_STATE is unexpected, and
// an rpc, action or notification unless the parse is of that operation and none was parsed yet.
func (lc *lydCtx) checkSchema(sn *schema.Node) error {
	if lc.opts.anyContent {
		return nil
	}
	if lc.opts.NoState && !sn.Config && !inOperation(sn) {
		lc.log.locSet(sn)
		defer lc.log.locBack(1)
		return lc.log.val(nil, "", ly.Data, "Unexpected data %s node \"%s\" found.", "state", sn.Name)
	}
	var ok bool
	switch sn.Kind {
	case schema.RPC:
		ok = lc.op.rpc || lc.op.reply
	case schema.Action:
		ok = lc.op.action || lc.op.reply
	case schema.Notification:
		ok = lc.op.notif
	default:
		return nil
	}
	lc.log.locSet(sn)
	defer lc.log.locBack(1)
	if !ok {
		return lc.log.val(nil, "", ly.Data, "Unexpected %s element \"%s\".", nodetypeStr(sn.Kind), sn.Name)
	}
	if op := lc.opSchema; lc.opNode != nil {
		// libyang reads op_node->schema, NULL for an opaque operation node: SIGSEGV (D-0110)
		return lc.log.val(nil, "", ly.Data, "Unexpected %s element \"%s\", %s \"%s\" already parsed.",
			nodetypeStr(sn.Kind), sn.Name, nodetypeStr(op.Kind), op.Name)
	}
	return nil
}

// inOperation reports whether sn is inside an rpc, action or notification, where libyang's
// compiled nodes have no config flag at all (LYS_CONFIG_R is not set there).
func inOperation(sn *schema.Node) bool {
	for p := sn; p != nil; p = p.Parent {
		switch p.Kind {
		case schema.RPC, schema.Action, schema.Notification:
			return true
		}
	}
	return false
}

// nodetypeStr is lys_nodetype2str.
func nodetypeStr(k schema.Kind) string {
	switch k {
	case schema.Container:
		return "container"
	case schema.Choice:
		return "choice"
	case schema.Leaf:
		return "leaf"
	case schema.LeafList:
		return "leaf-list"
	case schema.List:
		return "list"
	case schema.AnyXML:
		return "anyxml"
	case schema.AnyData:
		return "anydata"
	case schema.Case:
		return "case"
	case schema.RPC:
		return "RPC"
	case schema.Action:
		return "action"
	case schema.Notification:
		return "notification"
	}
	return "unknown"
}

// checkKeys is lyd_parser_check_keys: the keys are the first children, in key order.
func (lc *lydCtx) checkKeys(list *Node) error {
	for i, k := range list.schema.Keys {
		lc.tree.work.Add(1)
		if i >= len(list.kids.list) || list.kids.list[i].schema != k {
			return lc.log.val(list, "", ly.Data, "List instance is missing its key \"%s\".", k.Name)
		}
	}
	return nil
}

// hasWhen is lysc_has_when: a when on the node or on a choice/case ancestor below its data parent.
func hasWhen(sn *schema.Node) bool {
	for p := sn; p != nil; p = p.Parent {
		if p != sn && p.Kind != schema.Choice && p.Kind != schema.Case {
			return false // the data parent's when is not the node's
		}
		if len(p.Whens) > 0 {
			return true
		}
	}
	return false
}

// closeInner is the end of an inner node in lydjson_parse_instance_inner / lydxml_subtree_inner:
// the key check of a list, then, only if nothing inside the node failed (rc of the node, the
// missing key included: design 07 §0.1), the new-node validation and implicit nodes.
func (lc *lydCtx) closeInner(n *Node, rc error) error {
	if err := lc.flushDirty(&n.kids); err != nil {
		return err
	}
	if n.schema.Kind == schema.List {
		if err := lc.checkKeys(n); err != nil && rc == nil {
			rc = err
		}
	}
	if rc == nil && !lc.opts.ParseOnly && lc.validateNewImplicit != nil {
		rc = lc.validateNewImplicit(lc, n)
	}
	return rc
}

// nodeInsert is lyd_parser_node_insert: a list is linked only once it has all its keys; an
// anchor puts n right after it (lyd_insert_after), else n goes by schema order, appended for
// LYD_PARSE_ORDERED input.
func (lc *lydCtx) nodeInsert(parent, anchor, n *Node) {
	if n == nil || n.parent != nil || n.tree != nil {
		return // already inserted
	}
	if n.schema != nil && n.schema.Kind == schema.List {
		if _, ok := hashOf(n); !ok && !n.schema.Keyless() {
			return // missing key(s)
		}
	}
	switch {
	case anchor != nil && anchor.schema == nil && n.schema == nil:
		// lyd_insert_after of the XML parser's anchor (lydxml_get_hints_opaq): an opaque node
		// right after the last opaque sibling with the same name, among the opaque nodes
		sib := anchor.siblingsOf()
		lc.tree.link(anchor.parent, sib, n, sib.indexOf(sib.opq, anchor)+1)
	case anchor != nil:
		// libyang's only insert anchor is the opaque one above
		panic("data: insert anchor of a schema node")
	case lc.opts.ordered:
		lc.tree.insert(parent, n, insertLast)
	case lc.appendSorted(parent, n):
	default:
		lc.tree.insert(parent, n, insertDefault)
	}
}

// appendSorted appends parser instances to a system-ordered run and defers publication
// of its stable order until a reader needs the physical siblings. Operation runs retain
// their indexed boundaries; ordinary runs are found by schema order.
func (lc *lydCtx) appendSorted(parent, n *Node) bool {
	if !sortedSupported(n) {
		return false
	}
	sib := lc.tree.childrenOf(parent)
	if sib.unsorted[n.schema] {
		return false
	}
	var run []*Node
	var at int
	if opChild(n.schema) {
		d := sib.opIdx[n.schema]
		if d == nil || len(d.list) == 0 {
			return false
		}
		run = d.list
		at = lc.tree.indexFromEnd(sib.list, run[len(run)-1]) + 1
	} else {
		lo, hi := lc.tree.runBounds(sib, n)
		if lo == hi {
			return false
		}
		run, at = sib.list[lo:hi], hi
	}
	k := dirtyRun{sib, n.schema}
	ord := lc.runOrd[k]
	if ord == nil || lc.dirtyOrd[k] == nil && dirtySize(ord.root) != len(run) {
		// The first parser insertion into the run, or the run changed outside the parser since:
		// a run without an RB tree gets one (lyds_additionally_create_rb_tree), sorting it.
		create := !sib.rbTree[n.schema]
		if create {
			lc.tree.applyRunReorderHash(sib, run, reorderExact)
			for _, old := range run {
				lc.tree.work.Add(1)
				old.inRB = true
			}
			if d := sib.opIdx[n.schema]; d != nil {
				d.rb = len(run)
			}
			markRB(sib, n.schema)
		}
		var moved bool
		ord, moved = newDirtyOrder(&lc.tree.work, run, lc.tree.compareSorted, create)
		if lc.runOrd == nil {
			lc.runOrd = map[dirtyRun]*dirtyOrder{}
		}
		lc.runOrd[k] = ord
		if moved {
			lc.markDirty(sib, n.schema, ord)
		}
		lc.applyLeaders(sib, n.schema, ord)
	}
	n.inRB = true
	// rb_insert_node; an instance that is not the new maximum makes the physical run stale
	if !ord.insert(n) {
		lc.markDirty(sib, n.schema, ord)
	}
	lc.tree.link(parent, sib, n, at)
	lc.applyLeaders(sib, n.schema, ord)
	return true
}

// applyLeaders applies the first-instance changes of the latest insertions into a run's tree
// when they happen, after the instance's own exact record, as lyd_insert_hash_add does after
// lyds_insert linked it before the old first instance (tree_data.c:820-842,
// tree_data_hash.c:128-163). Without a children table there are no records to change; a table
// built later starts from the tree order (siblings.logical).
func (lc *lydCtx) applyLeaders(sib *siblings, sn *schema.Node, ord *dirtyOrder) {
	if sib.ht != nil && firstRecordSchema(sn) {
		for _, c := range ord.leaders {
			lc.tree.work.Add(1)
			sib.leaderReplace(sn, c[0], c[1])
		}
	}
	ord.leaders = nil
}

func (lc *lydCtx) markDirty(sib *siblings, sn *schema.Node, ord *dirtyOrder) {
	lc.tree.work.Add(1)
	if lc.dirty == nil {
		lc.dirty = map[*siblings][]*schema.Node{}
	}
	k := dirtyRun{sib, sn}
	if lc.dirtySet[k] {
		return
	}
	if lc.dirtySet == nil {
		lc.dirtySet = map[dirtyRun]bool{}
	}
	lc.dirtySet[k] = true
	if lc.dirtyOrd == nil {
		lc.dirtyOrd = map[dirtyRun]*dirtyOrder{}
	}
	lc.dirtyOrd[k] = ord
	if sib.pending == nil {
		sib.pending = map[*schema.Node]*dirtyOrder{}
	}
	sib.pending[sn] = ord
	runs := lc.dirty[sib]
	if len(runs) == 0 {
		lc.dirtySibs = append(lc.dirtySibs, sib)
	}
	lc.dirty[sib] = append(runs, sn)
}

// flushDirty stably sorts the dirty runs of sib and forgets them; other sets are not looked at.
// Parser insertPos/opAnchor only need the run boundary, which appending preserves; JSON metadata
// and closeInner call this before reading instance order. Rollback can unlink dirty members in any
// order because opIdx and the leader chain are maintained per mutation. XML opaque anchors only
// distinguish schema-run boundaries, also unchanged.
func (lc *lydCtx) flushDirty(sib *siblings) error {
	if err := lc.ctx.Err(); err != nil {
		return err
	}
	lc.publishDirty(sib)
	return nil
}

// publishDirty is flushDirty without the cancellation check: once it starts it publishes every
// dirty run of sib, so no run is left out of order with its state already dropped. Its work is
// bounded, O(n log n) for a set of n siblings.
func (lc *lydCtx) publishDirty(sib *siblings) {
	lc.tree.work.Add(1)
	keys := lc.dirty[sib]
	if len(keys) == 0 {
		return
	}
	delete(lc.dirty, sib)
	for _, sn := range keys {
		lc.tree.work.Add(1)
		delete(lc.dirtySet, dirtyRun{sib, sn})
	}
	if len(keys) == 1 {
		lc.flushDirtyRun(dirtyRun{sib, keys[0]}, -1)
		return
	}
	// Many small dirty runs are located by one sibling pass, not one endpoint scan per run.
	starts := make(map[*schema.Node]int, len(keys))
	for i, n := range sib.list {
		lc.tree.work.Add(1)
		if _, ok := starts[n.schema]; !ok {
			starts[n.schema] = i
		}
	}
	for _, sn := range keys {
		lc.flushDirtyRun(dirtyRun{sib, sn}, starts[sn])
	}
}

// flushDirtyAll publishes every dirty run at the end of a parse, also after an error, a budget
// stop or a cancellation, and before lyd_parse_op's cleanup frees the parsed set. libyang links
// each instance into its RB tree when it is parsed (lyd_insert_node, tree_data.c:820-822), so the
// instances a failed parse leaves in an existing tree are in value order; publishing (not
// restoring the pre-parse order) gives that state.
func (lc *lydCtx) flushDirtyAll() error {
	for _, sib := range lc.dirtySibs {
		lc.publishDirty(sib)
	}
	lc.dirtySibs, lc.runOrd = nil, nil
	return lc.ctx.Err()
}

// flushPath gives lydPath the sorted positions of deferred-run nodes without publishing them.
// During a parse, the work for the instances of a deferred (dirty) system-ordered run
// is O(log n) per instance and per mid-parse order read, plus one O(n) or O(n log n) publication
// per sibling set; no reader may force a whole-run re-sort.
func (lc *lydCtx) flushPath(n *Node) map[*Node]int {
	var positions map[*Node]int
	for it := n; it != nil; it = it.parent {
		if it.schema == nil || it.schema.Kind != schema.LeafList || it.schema.Config {
			continue
		}
		sib := it.siblingsOf()
		if sib == nil {
			continue
		}
		if ord := lc.dirtyOrd[dirtyRun{sib, it.schema}]; ord != nil {
			if rank, ok := ord.rank(it); ok {
				if positions == nil {
					positions = map[*Node]int{}
				}
				positions[it] = rank
			}
		}
	}
	return positions
}

func (lc *lydCtx) flushDirtyRun(k dirtyRun, lo int) {
	defer func() {
		delete(lc.dirtyOrd, k)
		delete(k.sib.pending, k.sn)
	}()
	var count int
	if opChild(k.sn) {
		d := k.sib.opIdx[k.sn]
		if d == nil || len(d.list) < 2 {
			return
		}
		count = len(d.list)
		if lo < 0 {
			lo = lc.tree.runFrom(k.sib.list, d.list[0], d.list[count-1], count)
		}
	} else {
		if lo < 0 {
			lo = lc.tree.schemaIndex(k.sib, k.sn)
		}
		if lo >= 0 {
			hi := lc.tree.upper(k.sib.list, lo, func(n *Node) bool { return n.schema != k.sn })
			count = hi - lo
		}
	}
	if lo < 0 || count < 2 {
		return
	}
	ordered := lc.dirtyOrd[k].ordered()
	run := k.sib.list[lo : lo+count]
	if checkPublish {
		checkPublishedRun(k.sn, run, ordered)
	}
	// Publish as one mutation: cancellation must not leave duplicated/missing pointers in
	// a partially copied run, particularly when parsing into an existing operation.
	copy(run, ordered)
	lc.tree.work.Add(int64(len(run)))
	if opChild(k.sn) {
		k.sib.opRebuild(lc.tree, k.sn, run)
	}
	k.sib.gen++
}

// checkPublish turns on checkPublishedRun (tests): publication overwrites the run's slots, so
// they must hold exactly the run's instances, none of another schema node, none missing.
var checkPublish bool

func checkPublishedRun(sn *schema.Node, run, ordered []*Node) {
	if len(run) != len(ordered) {
		panic(fmt.Sprintf("data: publishing %d instances of %s into %d slots", len(ordered), sn.Name, len(run)))
	}
	in := make(map[*Node]bool, len(run))
	for _, n := range run {
		if n.schema != sn {
			panic(fmt.Sprintf("data: a %s node inside the deferred %s run", n.Name(), sn.Name))
		}
		in[n] = true
	}
	for _, n := range ordered {
		if !in[n] {
			panic(fmt.Sprintf("data: deferred %s instance outside its run's slots", sn.Name))
		}
	}
}

// nodeFree is lyd_parser_node_free: a key is never freed (its list goes instead).
func (lc *lydCtx) nodeFree(n *Node) {
	if n == nil || n.schema != nil && n.schema.IsKey() {
		return
	}
	if sib := n.siblingsOf(); sib != nil {
		if ord := lc.dirtyOrd[dirtyRun{sib, n.schema}]; ord != nil {
			ord.remove(n)
		}
	}
	unlink(n)
	freeSubtreeLinks(lc.tree.set, n)
}

// treeOf is the tree n is linked into, nil for an unlinked subtree.
func (n *Node) treeOf() *Tree {
	for n.parent != nil {
		n = n.parent
	}
	return n.tree
}
