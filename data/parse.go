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

// ValidateOptions are libyang's LYD_VALIDATE_* options.
type ValidateOptions struct {
	NoState     bool // LYD_VALIDATE_NO_STATE
	Present     bool // LYD_VALIDATE_PRESENT
	MultiError  bool // LYD_VALIDATE_MULTI_ERROR
	Operational bool // LYD_VALIDATE_OPERATIONAL
	NoDefaults  bool // LYD_VALIDATE_NO_DEFAULTS
	// NotFinal skips only the final recursive pass (must, mandatory, min/max-elements,
	// unique, unexpected/obsolete nodes and non-presence-container default flags).
	// Earlier phases, including when and type-plugin validate_tree checks, still run.
	// Finalize each relevant module with ValidateModuleFinal once the tree is final.
	NotFinal bool // LYD_VALIDATE_NOT_FINAL
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
	// AnyDataStrict is LYD_PARSE_ANYDATA_STRICT: anydata and anyxml payloads, in JSON and XML
	// input, are parsed with libyang's strict schema lookup instead of falling back to opaque nodes.
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
	// owner is the counter of a ParseOp parent's tree, whose state is open while the Reader
	// and the context run (suspended, design 13 §2.4)
	owner *workCounter
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
	op     opOpts
	parent *Node
	// The existing request's occurrence generation before reply parsing.
	parsedParentGen uint64
	opNode          *Node
	opSchema        *schema.Node
	// rcOnly is lyd_parse_op's result rule: the format parser's return code is the result, so an
	// error it logged and went past (the YANG action element, lydxml_envelope) does not fail it
	rcOnly bool
	parsed []*Node // the nodes parsed directly under parent (lyd_parse_op's parsed set)
	// dataType is lyd_parse_op's data_type, which the NETCONF and RESTCONF parsers dispatch on;
	// env is the tree of the envelope they parsed (lyd_parse_op's envp), nil until then.
	dataType opType
	env      *Tree
	// dirty holds, per sibling set, the system-ordered runs appended in parser order after
	// the first out-of-order value; dirtySibs is the order the sets first got one. Runs are stably
	// sorted before any parser reader can observe their order; a flush touches only its own set.
	dirty     map[*siblings][]*schema.Node
	dirtySibs []*siblings
	dirtySet  map[dirtyRun]bool        // the runs pending in dirty, for a constant-time repeat mark
	dirtyOrd  map[dirtyRun]*dirtyOrder // the dirty runs' trees
	runOrd    map[dirtyRun]*dirtyOrder // aliases of retained trees used during this parse
	pos       struct {                 // position's cache: one sibling set at one generation
		sib *siblings
		gen uint64
		at  map[*Node]int
	}
}

// position is the index of n in sib.schemaSpan() for the parser's readers (metadata walks over split
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
	at := sib.schemaSpan().indexFromEnd(&lc.tree.work, n)
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

// opType is the operation lyd_parse_op parses (enum lyd_type without LYD_TYPE_DATA_YANG, in its
// order).
type opType uint8

const (
	opRPC           opType = iota + 1 // LYD_TYPE_RPC_YANG: an rpc or action
	opNotif                           // LYD_TYPE_NOTIF_YANG
	opReply                           // LYD_TYPE_REPLY_YANG
	opRPCNetconf                      // LYD_TYPE_RPC_NETCONF: an <rpc> message
	opNotifNetconf                    // LYD_TYPE_NOTIF_NETCONF: a <notification> message
	opReplyNetconf                    // LYD_TYPE_REPLY_NETCONF: an <rpc-reply> message
	opRPCRestconf                     // LYD_TYPE_RPC_RESTCONF: the "input" message-body
	opNotifRestconf                   // LYD_TYPE_NOTIF_RESTCONF: the JSON "notification" message
	opReplyRestconf                   // LYD_TYPE_REPLY_RESTCONF: the "output" message-body
)

// parseOp is lyd_parse_op for the YANG operation types (parseOpWith also takes the NETCONF and
// RESTCONF messages): the operation in format f, parsed only (LYD_PARSE_ONLY), with the unknown-node policy (Skip is
// libyang's parse options 0: unknown nodes are dropped). With parent nil the result is a new tree holding the
// operation and its parents; with parent, the nodes are parsed as its children (a reply into
// its rpc or action) and the tree is parent's. op is the operation node. On error the nodes
// parsed are removed again (from parent's tree) and the tree is nil.
func parseOp(ctx context.Context, r io.Reader, s *schema.Set, f Format, t opType, parent *Node,
	unknown UnknownPolicy) (tree *Tree, op *Node, diags []yang.Diagnostic, err error) {
	tree, op, _, diags, err = parseOpWith(ctx, r, s, f, t, parent, unknown, Budget{})
	return tree, op, diags, restoreUser(err) // its callers are public boundaries (tests)
}

// parseOpWith is parseOp with the resource limits b, for the NETCONF and RESTCONF messages too
// (lyd_parse_xml_netconf, lyd_parse_json_restconf): env is then the tree of the envelope nodes,
// returned also on error once the envelopes were parsed (lyd_parse_op's "special situation").
func parseOpWith(ctx context.Context, r io.Reader, s *schema.Set, f Format, t opType, parent *Node,
	unknown UnknownPolicy, b Budget) (tree *Tree, op *Node, env *Tree, diags []yang.Diagnostic, err error) {
	// Panic-safe finalization (design 13 §2.4), armed before the journal opens: it ends whatever
	// journal holds when it runs. On a panic (the Reader's, or its error's classification) the
	// journal restores and detaches, its hash-work scope closes, and the panic goes on; on a
	// normal return it is the former fail-then-end sequence, so precedence does not change.
	var journal *hashTransaction
	// a closure: it ends the journal assigned below; a user error stays sealed (the public
	// caller restores it last)
	defer func() {
		if p := recover(); p != nil {
			journal.abandon()
			panic(p)
		}
		journal.end(&err)
	}()
	var oo opOpts
	fp := parseJSON
	if f == FormatXML {
		fp = parseXML
	}
	switch t {
	case opRPC:
		oo.rpc, oo.action = true, true
	case opNotif:
		oo.notif = true
	case opReply:
		oo.reply = true
	case opRPCNetconf, opNotifNetconf, opReplyNetconf, opRPCRestconf, opNotifRestconf, opReplyRestconf:
		// a protocol message: the envelope parser sets the internal options
		fp = parseJSONRestconf
		if f == FormatXML {
			fp = parseXMLNetconf
		}
	default:
		return nil, nil, nil, nil, fmt.Errorf("data: unknown operation type %d", t)
	}
	if oo.any() {
		oo.noSiblings = parent == nil
	}
	o := parseOpts{ParseOptions: ParseOptions{Unknown: unknown, ParseOnly: true, Budget: b}}
	var detachedTree *Tree
	if parent != nil {
		if heldNode(parent) {
			return nil, nil, nil, nil, errInProgress // its operation waits on user code
		}
		if owner := parent.treeOf(); owner != nil {
			journal = beginHashTransaction(owner)
			o.owner = &owner.work
		} else {
			// A detached parent (LYD_CTX(parent)): the parser's own tree of s stands in for
			// lookups, and its journal, opened and capturing (enrolling) the whole detached request
			// before the Reader runs, restores the parent on any budget failure and makes the
			// Reader and the context reach a suspended state (design 13 §2.4)
			detachedTree = newTree(s)
			journal = beginHashTransaction(detachedTree)
			o.owner = &detachedTree.work
			if err := journal.captureDetachedParent(parent); err != nil {
				return nil, nil, nil, nil, err
			}
		}
	}
	var lc *lydCtx
	tree, diags, err = parseWith(ctx, r, s, o, fp, func(c *lydCtx) {
		lc, c.op, c.parent, c.rcOnly, c.dataType = c, oo, parent, true, t
		if parent != nil {
			c.parsedParentGen = parent.kids.gen
			if pt := parent.treeOf(); pt != nil {
				c.tree = pt
			} else {
				c.tree = detachedTree // its journal captured the request before the read
			}
		}
	})
	journal.fail(&err)
	if err != nil {
		if lc != nil && parent != nil {
			// Cleanup may log key refusals after the parse error; keep the parse RC.
			var ve *ValidationError
			if errors.As(err, &ve) {
				ve.rc = ve.RC()
			}
			// A budget failure is restored by the journal (the owner's, or the parser tree's for
			// a detached parent); other failures free the parsed nodes.
			if !errors.Is(err, yang.ErrBudget) {
				if cleanupErr := lc.freeParsed(); cleanupErr != nil {
					err = lc.log.done(cleanupErr)
				}
			} // nil lc: the input was not read, nothing was parsed
			diags = lc.log.diags
			if ve != nil {
				ve.Diags = diags
			}
		}
		if lc != nil {
			env = lc.env
		}
		return nil, nil, env, diags, err
	}
	if parent != nil {
		tree = parent.treeOf() // libyang returns no tree with a parent; nil for a detached parent
	}
	return tree, lc.opNode, lc.env, diags, nil
}

// freeParsed frees the nodes parsed under lc.parent again (lyd_parse_op's cleanup of its parsed
// set), in one bulk unlink replaying the native order: tree_data.c:482-489 walks the parsed set
// backwards. lyd_free_tree refuses keys, logging once per key in that order.
func (lc *lydCtx) freeParsed() error {
	for i := len(lc.parsed) - 1; i >= 0; i-- {
		lc.tree.work.Add(1)
		if n := lc.parsed[i]; n.isKey() {
			// freeTree returns the same pathless LOGERR as lyd_free_tree; the key stays.
			if err := lc.log.done(freeTree(n)); errors.Is(err, yang.ErrBudget) {
				return err
			}
		}
	}
	ns := slices.DeleteFunc(lc.parsed, func(n *Node) bool { return n.isKey() })
	slices.Reverse(ns)
	// Budget failures are restored by the enclosing journal. Ordinary parser cleanup spends
	// the same enforced operation allowance; exhausting it also rolls back the operation.
	err := lc.tree.unlinkAll(ns)
	if err != nil {
		return err
	}
	if lc.parent != nil {
		lc.parent.kids.gen = lc.parsedParentGen
	}
	for _, n := range ns {
		freeSubtreeLinks(lc.tree.set, n)
	}
	lc.parsed = nil
	return nil
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
	// the Reader is user code: it runs with the owner's state suspended, its error sealed inside
	in, err := readUser(o.owner, io.LimitReader(r, int64(maxBytes)+1))
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
	lc.ctx = suspending(ctx, &lc.tree.work, o.owner) // every poll suspends the parse's states
	defer lc.tree.work.beginHashWork()()
	err = fp(lc, in)
	if lc.tree.work.hashErr != nil {
		err = lc.tree.work.hashErr
	}
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
		err = poll(lc.ctx)
	}
	if err == nil && !lc.rcOnly {
		err = lc.log.result()
	} else if errors.Is(err, errLogged) {
		ve := &ValidationError{Diags: lc.log.diags, guard: lc.log.guard}
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
		err = &ValidationError{Diags: lc.log.diags, rc: string(rc), guard: lc.log.guard}
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
	if lc.tree.work.hashErr != nil {
		return lc.tree.work.hashErr
	}
	lc.nodes++
	limit := orDefault(lc.opts.Budget.MaxNodes, DefaultMaxNodes)
	if lc.opts.zeroNodes {
		limit = 0
	}
	if lc.nodes > limit {
		return fmt.Errorf("%w: more than %d data nodes", yang.ErrBudget, limit)
	}
	if lc.nodes%1024 == 0 {
		return poll(lc.ctx)
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
	n := newTerm(sn, v, &lc.tree.work)
	if lc.tree.work.hashErr != nil {
		return nil, lc.tree.work.hashErr
	}
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
	n := newInner(sn, &lc.tree.work)
	if lc.tree.work.hashErr != nil {
		return nil, lc.tree.work.hashErr
	}
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
		if i >= list.kids.SchemaLen() || list.kids.At(i).schema != k {
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
func (lc *lydCtx) nodeInsert(parent, anchor, n *Node) error {
	if n == nil || n.parent != nil || n.tree != nil {
		return nil // already inserted
	}
	if n.schema != nil && n.schema.Kind == schema.List {
		if !n.lkeyOK && !n.schema.Keyless() {
			return nil // missing key(s)
		}
	}
	if anchor != nil && anchor.schema == nil && n.schema == nil {
		// lyd_insert_after of the XML parser's anchor (lydxml_get_hints_opaq): an opaque node
		// right after the last opaque sibling with the same name, among the opaque nodes
		sib := anchor.siblingsOf()
		return lc.tree.link(anchor.parent, sib, n, sib.indexOf(sib.opaqueSpan(), anchor)+1)
	}
	if anchor != nil {
		// libyang's only insert anchor is the opaque one above
		panic("data: insert anchor of a schema node")
	}
	if lc.opts.ordered {
		return lc.tree.insert(parent, n, insertLast)
	}
	if sortedSupported(n) {
		if inserted, err := lc.appendSorted(parent, n); err != nil || inserted {
			return err
		}
	}
	if err := lc.flushDirty(lc.tree.childrenOf(parent)); err != nil {
		return err
	}
	return lc.tree.insert(parent, n, insertDefault)
}

// appendSorted appends parser instances to a system-ordered run and defers publication
// of its stable order until a reader needs the physical siblings. Operation runs retain
// their indexed boundaries; ordinary runs are found by schema order.
func (lc *lydCtx) appendSorted(parent, n *Node) (bool, error) {
	if !sortedSupported(n) {
		return false, nil
	}
	if err := lc.tree.work.hashErr; err != nil {
		return false, err
	}
	sib := lc.tree.childrenOf(parent)
	if err := lc.tree.touch(sib); err != nil {
		return false, err
	}
	var run siblingSpan
	var at, runLen int
	var first *Node
	if opChild(n.schema) && sib.order != nil && sib.order.entries[n.schema] != nil && sib.order.entries[n.schema].gaps == 0 {
		d := sib.order.entries[n.schema]
		if d.count == 0 {
			return false, nil
		}
		first = sib.documentFirst(n.schema)
		last := sib.order.bySchema.Last(n.schema, 0)
		runLen = d.count
		at = sib.orderPosition(last.Value) + 1
		lo := at - runLen
		if lo < 0 || at > sib.SchemaLen() || sib.At(lo) != first {
			// The gap-free operation invariant normally gives the physical slice
			// directly. Keep malformed/stale index state on a counted error path
			// instead of allowing parser input to cause a slice-bounds panic.
			lo = sib.schemaSpan().indexFromEnd(sib.work, first)
			at = lo + runLen
			if lo < 0 || at > sib.SchemaLen() {
				return false, &opError{"LY_EINT", "Invalid ordered sibling index."}
			}
		}
		run = sib.schemaSpan().Slice(lo, at)
	} else {
		lo, hi := lc.tree.runBounds(sib, n)
		if lo == hi {
			return false, nil
		}
		run, at = sib.schemaSpan().Slice(lo, hi), hi
		runLen, first = run.Len(), run.At(0)
	}
	k := dirtyRun{sib, n.schema}
	ord := sib.pending[n.schema]
	leader := first
	if ord != nil {
		leader = ord.first()
	} else {
		ord = sib.rbOrders[sib.rbGroups[leader]]
	}
	// Native creation and runs with nonmembers use the regular insertion primitive.
	// A parser resume selects the retained root; it never reconstructs its history.
	if !sib.rbTree[leader] || sib.unsorted[leader] || len(sib.contexts) > 1 {
		return false, nil
	}
	if ord == nil {
		return false, &opError{"LY_EINT", "Missing retained red-black topology for a system-ordered run."}
	}
	if err := lc.tree.work.hashErr; err != nil {
		return false, err
	}
	p, left, pred, ok := ord.search(n, lc.tree.chargedCompare(lc.tree.compareSortedCompatible))
	if !ok {
		if lc.tree.work.hashErr != nil {
			return false, lc.tree.work.hashErr
		}
		return false, crossContextStorageError(n)
	}
	// Reserve the topology node, rebalancing, physical link and eventual publication before
	// changing the authoritative graph. Existing slots are paid once when deferral starts.
	publication := uint64(3)
	if sib.pending[n.schema] == nil && pred != run.At(run.Len()-1) {
		publication += 3 * uint64(runLen) //nolint:gosec // sibling counts are nonnegative
	}
	if pred == nil {
		publication = addHashBound(publication, sib.transferCost(leader, n))
	}
	if err := lc.tree.work.chargeHash(1 + rbStructBound(len(ord.nodes)) + publication); err != nil {
		return false, err
	}
	amount, err := lc.tree.linkBound(parent, sib, n, at)
	if err != nil {
		return false, err
	}
	if err := lc.tree.reserveHash(amount); err != nil {
		return false, err
	}
	if err := n.touch(); err != nil {
		return false, err
	}
	if pred == nil {
		ord.leaders = append(ord.leaders, [2]*Node{leader, n})
	}
	n.inRB, n.rbGroup = true, sib.rbGroups[leader]
	ord.place(n, p, left)
	if lc.runOrd == nil {
		lc.runOrd = map[dirtyRun]*dirtyOrder{}
	}
	lc.runOrd[k] = ord // an alias, never another topology
	if pred != run.At(run.Len()-1) {
		lc.markDirty(sib, n.schema, ord)
	}
	if err := lc.tree.link(parent, sib, n, at); err != nil {
		return false, err
	}
	lc.applyLeaders(sib, n.schema, ord)
	return true, nil
}

// applyLeaders applies the first-instance changes of the latest insertions into a run's tree
// when they happen, after the instance's own exact record, as lyd_insert_hash_add does after
// lyds_insert linked it before the old first instance (tree_data.c:820-842,
// tree_data_hash.c:128-163). Without a children table there are no records to change; a table
// built later starts from the tree order (siblings.logical).
func (lc *lydCtx) applyLeaders(sib *siblings, sn *schema.Node, ord *dirtyOrder) {
	if err := sib.touch(); err != nil {
		return
	}
	for _, c := range ord.leaders {
		lc.tree.work.Add(1)
		if sib.ht != nil && sib.firstRecordSchema(sn) {
			sib.leaderReplace(sn, c[0], c[1])
		}
		// Metadata follows the logical leader even when there is no native
		// first-instance record (or a transition's old record was absent).
		sib.transferRun(c[0], c[1])
	}
	ord.leaders = nil
}

func (lc *lydCtx) markDirty(sib *siblings, sn *schema.Node, ord *dirtyOrder) {
	if err := sib.touch(); err != nil {
		return
	}
	lc.tree.work.Add(1)
	if lc.dirty == nil {
		lc.dirty = map[*siblings][]*schema.Node{}
	}
	k := dirtyRun{sib, sn}
	already := lc.dirtySet[k]
	if already && sib.pending[sn] != nil {
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
	if already {
		return
	} // rollback may have restored an empty authoritative pending map
	runs := lc.dirty[sib]
	if len(runs) == 0 {
		lc.dirtySibs = append(lc.dirtySibs, sib)
	}
	lc.dirty[sib] = append(runs, sn)
}

// flushDirty stably sorts the dirty runs of sib and forgets them; other sets are not looked at.
// Parser insertPos/opAnchor only need the run boundary, which appending preserves; JSON metadata
// and closeInner call this before reading instance order. Rollback can unlink dirty members in any
// order because childOrder and the leader chain are maintained per mutation. XML opaque anchors only
// distinguish schema-run boundaries, also unchanged.
func (lc *lydCtx) flushDirty(sib *siblings) error {
	if err := poll(lc.ctx); err != nil {
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
	for i, n := range sib.SchemaAll() {
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
	return poll(lc.ctx)
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
		if ord := sib.pending[it.schema]; ord != nil {
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
	if err := k.sib.touch(); err != nil {
		return
	}
	defer func() {
		delete(lc.dirtyOrd, k)
		delete(k.sib.pending, k.sn)
	}()
	ord := k.sib.pending[k.sn]
	if ord == nil {
		return
	} // a journal rollback cancelled this deferred publication
	var count int
	if k.sib.order != nil {
		if d := k.sib.order.entries[k.sn]; d != nil && d.gaps == 0 {
			if d.count < 2 {
				return
			}
			first := k.sib.documentFirst(k.sn)
			count = d.count
			if lo < 0 {
				lo = k.sib.orderPosition(first)
			}
		}
	}
	if count == 0 {
		if lo < 0 {
			lo = lc.tree.schemaIndex(k.sib, k.sn)
		}
		if lo >= 0 {
			hi := lc.tree.runEnd(k.sib, lo)
			count = hi - lo
		}
	}
	if lo < 0 || count < 2 {
		return
	}
	ordered := spanOf(ord.ordered())
	run := k.sib.schemaSpan().Slice(lo, lo+count)
	if ordered.Len() != run.Len() {
		// Another insertion path linked a node beside this deferred run without
		// joining its RB tree. Keep that node in its physical slot, as logical()
		// does for hash-table construction, when publishing the run too.
		ordered = k.sib.logical().Slice(lo, lo+count)
	}
	if checkPublish {
		checkPublishedRun(k.sn, run, ordered)
	}
	oldLeader := run.At(0)
	// Publish as one mutation: cancellation must not leave duplicated/missing pointers in
	// a partially copied run, particularly when parsing into an existing operation.
	if err := k.sib.replaceSchema(lo, lo+count, ordered); err != nil {
		if lc.tree.work.hashErr == nil {
			lc.tree.work.hashErr = err // the publication is refused: the parse fails and restores
		}
		return
	}
	run = k.sib.schemaSpan().Slice(lo, lo+count)
	if oldLeader != run.At(0) {
		if k.sib.firstRun[k.sn] == oldLeader {
			k.sib.firstRun[k.sn] = run.At(0)
		}
	}
	lc.tree.work.Add(int64(run.Len()))
	if k.sib.order != nil {
		if d := k.sib.order.entries[k.sn]; d != nil {
			if err := k.sib.opRebuild(run); err != nil && lc.tree.work.hashErr == nil {
				lc.tree.work.hashErr = err // the publication is refused: the parse fails and restores
			}
		}
	}
	k.sib.gen++
}

// checkPublish turns on checkPublishedRun (tests): publication overwrites the run's slots, so
// they must hold exactly the run's instances, none of another schema node, none missing.
var checkPublish bool

func checkPublishedRun(sn *schema.Node, run, ordered siblingSpan) {
	if run.Len() != ordered.Len() {
		panic(fmt.Sprintf("data: publishing %d instances of %s into %d slots", ordered.Len(), sn.Name, run.Len()))
	}
	in := make(map[*Node]bool, run.Len())
	for _, n := range run.All() {
		if n.schema != sn {
			panic(fmt.Sprintf("data: a %s node inside the deferred %s run", n.Name(), sn.Name))
		}
		in[n] = true
	}
	for _, n := range ordered.All() {
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
		// Materialize native link order before unlink selects its leader. unlink alone removes
		// the authoritative RB node; a second parser removal would destroy its history.
		lc.publishDirty(sib)
	}
	if err := freeTree(n); err != nil {
		lc.tree.work.hashErr = lc.log.done(err)
	}
}

// treeOf is the tree n is linked into, nil for an unlinked subtree.
func (n *Node) treeOf() *Tree {
	for n.parent != nil {
		n = n.parent
	}
	return n.tree
}
