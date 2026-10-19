// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_new.c (BSD-3-Clause, © CESNET).

package data

import (
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

// anyValue is lyd_node_any's child/value/hints. Ordinary traversal uses Node.kids,
// which stays empty for an any node. Only this node owns children and text.
type anyValue struct {
	children siblings
	text     *string
	hints    types.Hints
}

// guardAnyCopyInput is lyd_any_copy_value's input check (tree_data_new.c:326).
// Call it before clearing a destination, duplicating children, or parsing text.
func guardAnyCopyInput(child *Node, value *string, arg, fn string) error {
	if child != nil && value != nil {
		return argErr(arg, fn)
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

// childrenAny is lyd_child_any (tree_data.h): ordinary children, or any payloads.
// Keep it internal until the conformance engine can consume the public observer.
func (n *Node) childrenAny() iter.Seq[*Node] {
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
			for i := len(sib.opq) - 1; i >= 0; i-- {
				stack = append(stack, sib.opq[i])
			}
			for i := len(sib.list) - 1; i >= 0; i-- {
				stack = append(stack, sib.list[i])
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
}

func newAnyWork(ctx context.Context, b Budget) *anyWork {
	if ctx == nil {
		ctx = context.Background()
	}
	return &anyWork{ctx: ctx, budget: b}
}

// createAnyDatatree is lyd_create_any_datatree. Only object/XML text is tried;
// parse-only opaque mode preserves unknown nodes and allows unqualified roots.
// Nested any syntax is still explicitly unsupported until the parser PRs.
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
	if err := guardAnyCopyInput(child, value, "schema/child/value", "lyd_create_any"); err != nil {
		return nil, nil, err
	}
	if err := w.ctx.Err(); err != nil {
		return nil, nil, err
	}
	n := &Node{schema: sn, flags: FlagNew, any: &anyValue{}, payloadSet: t.set}
	if value != nil {
		// lyd_create_any and lyd_any_copy_value take C strings, including for
		// parsing. Match their strlen boundary before retaining or parsing text.
		s, _, _ := strings.Cut(*value, "\x00")
		value = &s
	}
	var diags []yang.Diagnostic
	if value != nil && (sn.Kind == schema.AnyData || tryParse && hints&0x0fff == 0) {
		tr, parsedDiags, err := w.createAnyDatatree(t.set, *value)
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
			n.takeAnyChildren(t, child)
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
		l.diags = slices.Clone(ve.Diags)
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
func anyFollowing(child *Node) []*Node {
	if child == nil {
		return nil
	}
	if sib := child.siblingsOf(); sib != nil {
		nodes := sib.nodes()
		return nodes[slices.Index(nodes, child):]
	}
	return []*Node{child}
}

func (n *Node) takeAnyChildren(t *Tree, child *Node) {
	nodes, set := anyFollowing(child), ctxOf(child)
	if sib := child.siblingsOf(); child.parent == nil && sib != nil && sib.first() == child {
		// The usual case is a complete owned forest. Move its sibling storage and
		// sorting state in one operation, leaving no storage shared with its tree.
		n.any.children, *sib = *sib, siblings{}
	} else {
		// Remove a suffix from the end so wide input does not repeatedly shift
		// its remaining siblings. Preserve their original order in the payload.
		for i := len(nodes) - 1; i >= 0; i-- {
			unlink(nodes[i])
		}
		for _, c := range nodes {
			if c.schema == nil {
				n.any.children.opq = append(n.any.children.opq, c)
			} else {
				n.any.children.list = append(n.any.children.list, c)
			}
		}
	}
	n.any.children.work = &t.work
	for _, c := range nodes {
		if c.payloadSet == nil {
			c.payloadSet = set
		}
		c.parent, c.tree = n, nil
	}
}

func (n *Node) clearAny() {
	if n.any == nil {
		n.any = &anyValue{}
		return
	}
	for c := range n.any.children.all() {
		for d := range c.allAny() {
			freeLinks(d)
		}
		c.parent, c.tree = nil, nil
	}
	n.any.children, n.any.text = siblings{}, nil
}

// copyAnyValue is lyd_any_copy_value, including the empty-input early return
// that leaves hints alone. The iterative duplicator handles the full forest with
// its own recursive options; payload flags are Default|New and metadata is kept.
func (n *Node) copyAnyValue(w *anyWork, child *Node, value *string, hints types.Hints) error {
	if n == nil || !isAny(n.schema) {
		return argErr("trg/child/value", "lyd_any_copy_value")
	}
	if err := guardAnyCopyInput(child, value, "trg/child/value", "lyd_any_copy_value"); err != nil {
		return err
	}
	set := ctxOf(n)
	n.clearAny()
	if child == nil && value == nil {
		return nil
	}
	src := &Node{schema: n.schema, any: &anyValue{text: value, hints: hints}, payloadSet: set}
	for _, c := range anyFollowing(child) {
		if c.schema == nil {
			src.any.children.opq = append(src.any.children.opq, c)
		} else {
			src.any.children.list = append(src.any.children.list, c)
		}
	}
	d, err := newTree(set).dupWork(src, nil, false, insertDefault, 0, w)
	if err != nil {
		return err
	}
	n.any = d.any
	for c := range n.any.children.all() {
		c.parent = n
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
