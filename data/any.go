// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_new.c and src/tree_data_common.c (BSD-3-Clause, © CESNET).

package data

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// AnyValueString returns the content of an anydata or anyxml node in format.
// A payload tree is printed with all its siblings and default printer options;
// stored text is returned unchanged. The boolean distinguishes absent content
// from an explicitly stored empty string.
// Ported from lyd_any_value_str (tree_data_common.c).
func (n *Node) AnyValueString(format Format) (string, bool, error) {
	return n.anyValueString(format, nil)
}

func (n *Node) anyValueString(format Format, step func() error) (string, bool, error) {
	if n == nil || !isAny(n.schema) {
		return "", false, (&logger{}).done(argErr("any", "lyd_any_value_str"))
	}
	if n.any == nil {
		return "", false, nil
	}
	if n.any.children.len() == 0 {
		if n.any.text == nil {
			return "", false, nil
		}
		return *n.any.text, true, nil
	}
	if format != FormatJSON && format != FormatXML {
		return "", false, (&logger{}).done(argErr("format", "lyd_any_value_str"))
	}
	var b bytes.Buffer
	var children []*Node
	if step == nil {
		children = slices.Collect(n.any.children.all())
	} else {
		for c := range n.any.children.all() {
			if err := step(); err != nil {
				return "", false, err
			}
			children = append(children, c)
		}
	}
	set := ctxOf(children[0])
	if set == nil {
		set = ctxOf(n)
	}
	fn := printJSON
	if format == FormatXML {
		fn = printXML
	}
	if err := printData(&b, set, PrintOptions{step: step}, children, 0, true, fn); err != nil {
		return "", false, err
	}
	return b.String(), true, nil
}

// anyValue is lyd_node_any's child/value/hints. Ordinary traversal uses Node.kids,
// which stays empty for an any node. Only this node owns children and text.
type anyValue struct {
	children siblings
	text     *string
	hints    types.Hints
}

// guardAnyCopyInput is lyd_any_copy_value's input check (tree_data_new.c:326).
// Call it before clearing a destination, duplicating children, or parsing text.
func guardAnyCopyInput(child *Node, value *string) error {
	if child != nil && value != nil {
		// LY_CHECK_ARG_RET without a context returns LY_EINVAL without logging.
		return rcError("LY_EINVAL")
	}
	return nil
}

type anyValueType uint8

const (
	anyValueNone anyValueType = iota
	anyValueDataTree
	anyValueText
	anyValueMixed
)

func (n *Node) anyType() anyValueType {
	if n.any != nil {
		if n.any.children.len() != 0 {
			if n.any.text != nil {
				return anyValueMixed
			}
			return anyValueDataTree
		}
		if n.any.text != nil {
			return anyValueText
		}
	}
	return anyValueNone
}

// AnyValueType is the kind of payload an anydata or anyxml node holds.
type AnyValueType uint8

// AnyValueType values: AnyValueNone for no payload or a non-any node,
// AnyValueDataTree for a child forest (ChildrenAny), and AnyValueText for
// stored text (AnyValueString).
const (
	AnyValueNone AnyValueType = iota
	AnyValueDataTree
	AnyValueText
)

// AnyValueType reports the payload kind of an anydata or anyxml node. A node
// holding both a forest and text reports AnyValueDataTree, as the forest wins.
// Other nodes report AnyValueNone.
// Go observer over struct lyd_node_any.child/value (tree_data.h), following
// the oracle's child-first classification (lyoracle.c).
func (n *Node) AnyValueType() AnyValueType {
	if n == nil || !isAny(n.schema) {
		return AnyValueNone
	}
	switch n.anyType() {
	case anyValueDataTree, anyValueMixed:
		return AnyValueDataTree
	case anyValueText:
		return AnyValueText
	}
	return AnyValueNone
}

// ChildrenAny yields the ordinary children of n, or the payload forest of an
// anydata/anyxml node. Unlike Children, it includes payloads.
// Ported from lyd_child_any (tree_data.h).
func (n *Node) ChildrenAny() iter.Seq[*Node] {
	if n == nil {
		return func(func(*Node) bool) {}
	}
	if n.any != nil {
		return n.any.children.all()
	}
	return n.kids.all()
}

// allAny includes payloads for ownership cleanup, never for validation.
func (n *Node) allAny() iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		stack := []*Node{n}
		for len(stack) > 0 {
			d := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !yield(d) {
				return
			}
			sib := &d.kids
			if d.any != nil {
				sib = &d.any.children
			}
			for _, child := range sib.reverseAll() {
				stack = append(stack, child)
			}
		}
	}
}

// anyWork shares cancellation and the existing input/node budgets (U-0040/0041)
// across all speculative text parses in one copy operation. Tree copies use an
// explicit stack and do not impose an additional depth or node limit.
type anyWork struct {
	ctx          context.Context
	budget       Budget
	bytes, nodes int
	defs         types.DefWalk // the cross-context type definitions a Merge compared
}

func newAnyWork(ctx context.Context, b Budget) *anyWork {
	if ctx == nil {
		ctx = context.Background()
	}
	return &anyWork{ctx: ctx, budget: b}
}

// createAnyDatatree is lyd_create_any_datatree. Only object/XML text is tried;
// parse-only opaque mode preserves unknown nodes and allows unqualified roots.
// Return diagnostics even on success so callers can implement libyang's log
// argument: tree_data_new.c:203-206 saves/disables logging for !log, and
// :226-229 restores it. Required construction keeps them; speculative calls do not.
func (w *anyWork) createAnyDatatree(set *schema.Set, value string) (*Tree, []yang.Diagnostic, error) {
	if err := w.ctx.Err(); err != nil {
		return nil, nil, err
	}
	left := orDefault(w.budget.MaxBytes, DefaultMaxBytes) - w.bytes
	if len(value) > left {
		return nil, nil, fmt.Errorf("%w: cumulative any content input size", yang.ErrBudget)
	}
	w.bytes += len(value)
	var fp formatParser
	switch s := strings.TrimLeft(value, " \t\r\n\v\f"); {
	case strings.HasPrefix(s, "<"):
		fp = parseXML
	case strings.HasPrefix(s, "{"):
		fp = parseJSON
	default:
		return nil, nil, rcError("LY_ENOT")
	}
	remaining := orDefault(w.budget.MaxNodes, DefaultMaxNodes) - w.nodes
	o := parseOpts{ParseOptions: ParseOptions{Unknown: Opaque, ParseOnly: true,
		Budget: Budget{MaxBytes: left, MaxNodes: remaining}}, anyContent: true, zeroNodes: remaining <= 0}
	var lc *lydCtx
	tr, diags, err := parseWith(w.ctx, strings.NewReader(value), set, o, fp, func(c *lydCtx) { lc = c })
	if lc != nil {
		w.nodes += lc.nodes
	}
	return tr, diags, err
}

// anyParseFatal distinguishes ordinary text fallback from a resource failure or
// an unfinished parser path. Neither may silently turn a payload into text.
func anyParseFatal(err error) bool {
	return errors.Is(err, yang.ErrBudget) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, yang.ErrUnsupported)
}

// createAny is lyd_create_any. useValue transfers an owned forest; otherwise the
// forest is duplicated. Go strings are copied as nullable values, never pointers.
func (w *anyWork) createAny(t *Tree, sn *schema.Node, child *Node, value *string,
	hints types.Hints, useValue, tryParse bool) (*Node, []yang.Diagnostic, error) {
	if !isAny(sn) {
		return nil, nil, argErr("schema/child/value", "lyd_create_any")
	}
	if child != nil && value != nil {
		return nil, nil, argErr("schema/child/value", "lyd_create_any")
	}
	if err := w.ctx.Err(); err != nil {
		return nil, nil, err
	}
	// lyd_create_any works in schema->module->ctx, which in a mixed-context tree
	// need not be the tree's constructor snapshot.
	set := t.set
	if sn.Module != nil && sn.Module.Owner != nil {
		set = sn.Module.Owner
	}
	n := &Node{schema: sn, flags: FlagNew, any: &anyValue{}, payloadSet: set}
	if n.refreshHash(&t.work); t.work.hashErr != nil { // lyd_create_any's lyd_hash
		return nil, nil, t.work.hashErr
	}
	if value != nil {
		// lyd_create_any and lyd_any_copy_value take C strings, including for
		// parsing. Match their strlen boundary before retaining or parsing text.
		s, _, _ := strings.Cut(*value, "\x00")
		value = &s
	}
	var diags []yang.Diagnostic
	if value != nil && (sn.Kind == schema.AnyData || tryParse && hints&0x0fff == 0) {
		tr, parsedDiags, err := w.createAnyDatatree(set, *value)
		// lyd_create_any passes log=1 for required anydata parsing (:260),
		// but log=0 for speculative anyxml parsing (:282), even on success.
		if sn.Kind == schema.AnyData {
			diags = parsedDiags
		}
		if err != nil {
			if sn.Kind == schema.AnyData {
				err = anyCreateError(err)
				var ve *ValidationError
				if errors.As(err, &ve) {
					diags = ve.Diags
				}
				return nil, diags, err
			}
			if anyParseFatal(err) {
				return nil, nil, err
			}
		} else {
			if useValue {
				value = nil // C consumes the input even when parsing produced an empty forest
			}
			child, useValue = tr.top.first(), true
		}
	}
	if useValue {
		if child != nil {
			if err := n.takeAnyChildren(t, child); err != nil {
				return nil, diags, err
			}
		} else if value != nil {
			s := *value
			n.any.text = &s
		}
		n.any.hints = hints
	} else if err := n.copyAnyValue(w, child, value, hints); err != nil {
		return nil, diags, err
	}
	return n, diags, nil
}

// anyCreateError preserves the parser's return code and diagnostics before
// lyd_create_any's LOGERR. Its rc is still LY_SUCCESS when the message is logged;
// only afterwards does libyang replace rc with the parser's failure code.
func anyCreateError(err error) error {
	l := &logger{}
	result := &ValidationError{}
	var ve *ValidationError
	var rc rcError
	switch {
	case errors.As(err, &ve):
		*result = *ve
		l.emitAll(ve.Diags)
		result.rc = ve.RC()
	case errors.As(err, &rc):
		result.rc = string(rc)
	default:
		return err
	}
	_ = l.logErr("LY_SUCCESS", "Failed to parse any content into a data tree.")
	result.Diags = l.diags
	return result
}

// anyFollowing snapshots the forest beginning at child (not preceding siblings).
func anyFollowing(child *Node, work *workCounter) ([]*Node, error) {
	if child == nil {
		return nil, nil
	}
	if sib := child.siblingsOf(); sib != nil {
		if err := work.chargeHash(2*(uint64(sib.SchemaLen())+uint64(sib.OpaqueLen())) + 1); err != nil {
			return nil, err
		}
		nodes := sib.nodes()
		return nodes[slices.Index(nodes, child):], nil
	}
	return []*Node{child}, nil
}

func (n *Node) takeAnyChildren(t *Tree, child *Node) error {
	if err := n.touch(); err != nil {
		return err
	}
	if err := t.touch(&n.any.children); err != nil {
		return err
	}
	nodes, err := anyFollowing(child, &t.work)
	if err != nil {
		return err
	}
	set := ctxOf(child)
	if sib := child.siblingsOf(); child.parent == nil && sib != nil && sib.first() == child {
		if err := t.touch(sib); err != nil {
			return err
		}
		if err := sib.touch(); err != nil {
			return err
		}
		// A whole forest transfers exact topology and all its indexes, with no mutable sharing.
		if err := n.any.children.transferFrom(sib); err != nil {
			return err
		}
	} else {
		// Only internal freshly parsed forests reach the native use_value branch. A partial
		// forest supplied internally must use independent removals, never schema-mount's split.
		for i := len(nodes) - 1; i >= 0; i-- {
			if err := preflightUnlink(nodes[i]); err != nil {
				return err
			}
			if err := unlink(nodes[i]); err != nil {
				return err
			}
		}
		for _, c := range nodes {
			if err := n.any.children.appendNode(c); err != nil {
				return err
			}
		}
	}
	if err := bindSubtreeWork(n, &t.work); err != nil {
		return err
	}
	for _, c := range nodes {
		if err := c.touch(); err != nil {
			return err
		}
		if c.payloadSet == nil {
			c.payloadSet = set
		}
		c.parent, c.tree = n, nil
	}
	return nil
}

// clearAny journals n and the old payload roots first (Node.touch) and replaces,
// never edits, the old value: a rolled-back operation restores the journaled pointer.
func (n *Node) clearAny() error {
	if err := n.touch(); err != nil {
		return err
	}
	if n.any == nil {
		n.any = &anyValue{}
		return nil
	}
	old := &n.any.children
	if err := old.touch(); err != nil {
		return err
	}
	if old.work != nil {
		if err := old.work.chargeHash(uint64(old.SchemaLen()) + uint64(old.OpaqueLen()) + 1); err != nil {
			return err
		}
	}
	work := old.work
	if work == nil {
		work = &workCounter{}
	}
	if err := work.chargeHash(uint64(old.SchemaLen()) + uint64(old.OpaqueLen())); err != nil {
		return err
	}
	nodes, err := ownedNodes(old.nodes(), work)
	if err != nil {
		return err
	}
	if err := reserveFreeLinks(nodes, work); err != nil {
		return err
	}
	cleaner := bulkLinkCleaner{work: work}
	for _, c := range nodes {
		cleaner.free(c)
		releaseOwnedRB(c)
	}
	if work.hashErr != nil {
		return work.hashErr
	}
	for c := range old.all() {
		if err := c.touch(); err != nil {
			return err
		}
		c.parent, c.tree, c.inRB, c.rbGroup = nil, nil, false, nil
	}
	// lyd_free_siblings releases the lyds_tree metadata via its plugin callback: no survivor
	// needs rotations. Drop the entire owner, including pending parser references.
	if err := old.reset(); err != nil {
		return err
	}
	n.any = &anyValue{hints: n.any.hints}
	return nil
}

// copyAnyValue is lyd_any_copy_value, including the empty-input early return
// that leaves hints alone. The iterative duplicator handles the full forest with
// its own recursive options; payload flags are Default|New and metadata is kept.
func (n *Node) copyAnyValue(w *anyWork, child *Node, value *string, hints types.Hints) error {
	if n == nil || !isAny(n.schema) {
		return argErr("trg/child/value", "lyd_any_copy_value")
	}
	if err := guardAnyCopyInput(child, value); err != nil {
		return err
	}
	set := ctxOf(n)
	if err := n.clearAny(); err != nil {
		return err
	}
	if child == nil && value == nil {
		return nil
	}
	src := &Node{schema: n.schema, any: &anyValue{text: value, hints: hints}, payloadSet: set}
	work := &workCounter{}
	if owner := treeOf(n); owner != nil {
		work = &owner.work
	}
	children, err := anyFollowing(child, work)
	if err != nil {
		return err
	}
	for _, c := range children {
		if err := src.any.children.appendNode(c); err != nil {
			return err
		}
	}
	d, err := newTree(set).dupWork(src, nil, false, insertDefault, 0, set, w)
	if err != nil {
		return err
	}
	n.any = d.any
	for c := range n.any.children.all() {
		c.parent = n
	}
	if t := treeOf(n); t != nil {
		if err := bindSubtreeWork(n, &t.work); err != nil { // the copy was made in a temporary tree
			return err
		}
	}
	return nil
}

// dupPrefixes copies mutable namespace snapshots. Other PrefixCtx implementations
// refer only to immutable schema objects; value objects themselves are immutable.
func dupPrefixes(p types.PrefixCtx, target *schema.Set) types.PrefixCtx {
	if ns, ok := p.(types.XMLNamespaces); ok {
		ns.NS, ns.Order, ns.Set = maps.Clone(ns.NS), slices.Clone(ns.Order), target
		return ns
	}
	if _, ok := p.(types.ModuleNames); ok {
		return types.ModuleNames{Set: target}
	}
	return p
}
