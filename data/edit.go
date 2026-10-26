// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_new.c (lyd_new_path and its helpers, lyd_create_list,
// lyd_change_term), src/tree_data.c (lyd_find_path, lyd_merge, lyd_dup_r, lyd_unlink_tree), src/path.c
// (ly_path_eval_partial) and src/tree_data_common.c (lyd_dup_inst_next,
// lyd_find_sibling_dup_inst_set) (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// NewPathOptions are the lyd_new_path options of the port (LYD_NEW_PATH_*).
type NewPathOptions struct {
	// Parent is lyd_new_path's parent: the node a relative path resolves from. It must belong
	// to the receiver tree; an absolute path searches from the tree root, but a missing root is
	// inserted beside Parent, as in libyang. An opaque node is allowed. Adding this field
	// breaks unkeyed NewPathOptions literals.
	Parent *Node
	// Update is LYD_NEW_PATH_UPDATE: an existing leaf (or state leaf-list instance) gets the new
	// value instead of the "already exists" error.
	Update bool
}

// errNotFound and errPartial are lyd_find_path's LY_ENOTFOUND and LY_EINCOMPLETE (no match,
// a partial match): nothing is logged.
var (
	errNotFound = errors.New("data: path not found")
	errPartial  = errors.New("data: path found only partially")
)

// done turns an operation's error into its result: the logged diagnostics as a
// *ValidationError, other errors (budget, not found) as they are.
func (l *logger) done(err error) error {
	var contextual *loggedContextError
	errors.As(err, &contextual)
	finish := func(result error) error {
		var ve *ValidationError
		if contextual != nil && errors.As(result, &ve) {
			ve.set = contextual.set
		}
		return result
	}
	var oe *opError
	if errors.As(err, &oe) {
		err = l.logErr(oe.Err, "%s", oe.Msg)
	}
	if errors.Is(err, errLogged) {
		return finish(l.result())
	}
	var rc rcError
	if errors.As(err, &rc) {
		return finish(&ValidationError{Diags: l.diags, rc: string(rc), set: l.set})
	}
	return finish(err)
}

// NewPath is lyd_new_path: it creates the nodes of a JSON path
// that do not exist yet, value being the value of a created leaf or leaf-list (JSON format), and
// returns the first created node, or the changed node on update (nil when nothing changed).
// An existing term is an error unless it is a default node or o.Update is set; then its value is changed
// (lyd_change_term: its Default flag and that of its NP-container ancestors cleared, New set
// when the value changed). The keys of a created list instance come from the path's predicate.
// With o.Parent, a relative path starts at that node and an absolute path searches from the
// root. If its first segment is missing, libyang inserts it beside o.Parent.
// A parent detached from t or belonging to another tree is rejected with LY_EINVAL and the
// message "Parent node does not belong to this tree." A parent from another context uses
// libyang's "Different contexts mixed in a \"lyd_new_path\" function call." diagnostic.
// Other errors are a *ValidationError with libyang's diagnostics.
func (t *Tree) NewPath(path, value string, o NewPathOptions) (*Node, error) {
	defer t.work.beginHashWork()()
	lg := &logger{set: t.context()}
	// The ordinary path preflight handles small edits without a journal. A caller-specified
	// boundary or large input can still fail after several insertions, so journal those.
	var journal *hashTransaction
	if t.work.hashLimit != 0 || t.work.hashTrace != nil || len(value) >= 1<<20 || len(path) >= 1<<20 {
		journal = beginHashTransaction(t)
	}
	t.updateCtx = nil
	n, err := t.newPath(lg, path, value, o)
	if t.work.hashErr != nil {
		err = t.work.hashErr
	}
	err = lg.done(err)
	journal.end(&err)
	if errors.Is(err, yang.ErrBudget) {
		return nil, err
	}
	return n, err
}

// Find is lyd_find_path from the first top-level node of t: the node an absolute JSON path names,
// or the node a relative JSON path names among that first node's children (list and leaf-list
// instances need their predicates). It returns nil when the path does not exist. A malformed path
// is a *ValidationError.
func (t *Tree) Find(path string) (*Node, error) {
	lg := &logger{set: t.context()}
	n, err := t.findPath(lg, path)
	if errors.Is(err, errNotFound) || errors.Is(err, errPartial) {
		return nil, nil
	}
	if ve, silent := err.(*ValidationError); silent { //nolint:errorlint // findPath's unlogged argument check, never wrapped
		return nil, ve
	}
	return n, lg.done(err)
}

// Remove is lyd_free_tree: n and its subtree leave the tree. A list key is refused (an
// LY_EINVAL *ValidationError) and nothing is removed.
func (n *Node) Remove() error {
	if n == nil {
		return nil
	}
	done, err := n.beginHashWork()
	if err != nil {
		return err
	}
	defer done()
	return (&logger{}).done(freeTree(n))
}

// Unlink is lyd_unlink_tree: n and its subtree leave their siblings but remain usable. A list
// key is refused (an LY_EINVAL *ValidationError) and nothing is unlinked. Unlike Remove, Unlink
// preserves leafref links of descendants; the links of n itself are removed as by libyang.
func (n *Node) Unlink() error {
	if n == nil {
		return nil
	}
	if n.siblingsOf() == nil {
		return nil
	}
	done, err := n.beginHashWork()
	if err != nil {
		return err
	}
	defer done()
	if err := preflightUnlink(n); err != nil {
		return (&logger{}).done(err)
	}
	return (&logger{}).done(unlinkTree(n))
}

// beginHashWork starts the hash work of a mutation of n: in its tree or, detached, bound to a
// fresh counter of its root's subtree. The returned func ends it.
func (n *Node) beginHashWork() (func(), error) {
	if owner := n.treeOf(); owner != nil {
		return owner.work.beginHashWork(), nil
	}
	root := n
	for root.parent != nil {
		root = root.parent
	}
	work := &workCounter{}
	done := work.beginHashWork()
	bound, err := bindSubtreeBound(root, work)
	if err == nil {
		err = work.checkHashWork(bound)
	}
	if err == nil {
		err = bindSubtreeWork(root, work)
	}
	if err != nil {
		done()
		return nil, err
	}
	return done, nil
}

// Merge is lyd_merge_siblings without options: the top-level subtrees of src are merged into t.
// Nodes missing in t are copied with their flags and metadata and marked New; an existing leaf
// takes src's value unless src's leaf is a default node; src is not changed. A nil src merges
// nothing. Unequal anydata/anyxml payloads replace the whole destination payload,
// copying the source flags plus New; equal payloads leave the destination unchanged.
func (t *Tree) Merge(src *Tree) (result error) {
	defer t.work.beginHashWork()()
	lg := &logger{set: t.context()}
	if src == nil {
		return nil
	}
	if src.context() != t.context() {
		return lg.done(lg.logErr("LY_EINVAL", "Different contexts mixed in a \"lyd_merge\" function call."))
	}
	if err := t.preflightBatchHash(src); err != nil {
		return err
	}
	journal := beginHashTransaction(t)
	defer journal.end(&result)
	lv := &mergeLevel{cache: &dupCache{}, work: newAnyWork(context.Background(), Budget{})}
	for _, n := range src.top.nodes() {
		if err := t.mergeSibling(lv, n); err != nil {
			if flushErr := t.flushMerge(lv); flushErr != nil {
				return flushErr
			}
			if t.work.hashErr != nil {
				return t.work.hashErr
			}
			return lg.done(err)
		}
	}
	if err := t.flushMerge(lv); err != nil {
		return err
	}
	return t.work.hashErr
}

// preflightBatchHash reserves input-dependent hash work across a multi-node
// merge or diff application before its first visible mutation. Per-link checks
// still account for destination-specific work at the final insertion point.
func (t *Tree) preflightBatchHash(src *Tree) error {
	var amount uint64
	for _, root := range src.top.nodes() {
		for n := range root.All() {
			if n.schema != nil {
				schemaHashWork(n.schema, &t.work)
				if t.work.hashErr != nil {
					return t.work.hashErr
				}
			}
			if n.isTerm() {
				amount = addHashBound(amount, types.HashLYBWorkBound(n.value))
			}
			amount = addHashBound(amount, indexWorkBound(n, nil, nil, &t.work))
			if t.work.hashErr != nil {
				return t.work.hashErr
			}
		}
	}
	if err := t.work.checkHashWork(amount); err != nil {
		t.work.hashErr = err
		return err
	}
	return nil
}

// nodes returns the siblings in order, schema nodes then opaque ones (a snapshot).
func (s *siblings) nodes() []*Node {
	out := make([]*Node, 0, s.len())
	for n := range s.all() {
		out = append(out, n)
	}
	return out
}

// findPath is lyd_find_path from the first top-level node (ctx_node): errNotFound / errPartial
// when the path does not match fully. Absolute paths start at the document root; relative paths
// start at the context node's children, as ly_path_eval_partial does. An absolute path is checked
// on an empty tree too (the oracle's sequence reports LY_ENOTFOUND there without calling
// lyd_find_path); a relative one has no context node to compile against there and is not found.
func (t *Tree) findPath(lg *logger, path string) (*Node, error) {
	var ctx *Node
	for n := range t.top.all() {
		ctx = n
		break
	}
	// ly_path_parse decides absoluteness from the first token; lyxp skips XML whitespace first.
	trimmed := strings.TrimLeft(path, " \t\n\r")
	relative := trimmed == "" || trimmed[0] != '/'
	if ctx == nil && relative {
		return nil, errNotFound
	}
	var ctxNode *schema.Node
	if ctx != nil {
		if ctx.schema == nil {
			// LY_CHECK_ARG_RET(NULL, ...), tree_data.c:3707: no context to log in, no diagnostic.
			return nil, &ValidationError{err: argErr("ctx_node->schema", "lyd_find_path"), rc: "LY_EINVAL"}
		}
		ctxNode = ctx.schema
	}
	p, msg, at := types.CompilePath(t.context(), ctxNode, path, false, false)
	if msg != "" {
		if at == nil {
			at = ctxNode // ly_path_parse: LOG_LOCSET(ctx_node) before parsing (path.c:340)
		}
		return nil, lg.item(nil, at, false, "LY_EVALID", ly.XPath, "", msg)
	}
	if ctx == nil {
		return nil, errNotFound
	}
	sib := &t.top
	if relative {
		sib = t.childrenOf(ctx) // ly_path_eval_partial: lyd_child_any(ctx_node)
	}
	n, idx, err := t.evalPartialFrom(lg, sib, p)
	switch {
	case err != nil:
		return nil, err
	case idx == len(p):
		return n, nil
	case n != nil:
		return nil, errPartial
	}
	return nil, errNotFound
}

// evalPartial is ly_path_eval_partial from the top level: the last node matched and the number
// of path segments matched (len(p) for a full match).
func (t *Tree) evalPartial(lg *logger, p types.Path) (*Node, int, error) {
	return t.evalPartialFrom(lg, &t.top, p)
}

func (t *Tree) evalPartialFrom(lg *logger, sib *siblings, p types.Path) (*Node, int, error) {
	probes := newTree(t.set) // lookups share an operation quota, never mutate reader-visible work state
	defer probes.work.beginHashWork()()
	var prev *Node
	for u, seg := range p {
		var n *Node
		switch {
		case len(seg.Preds) == 0:
			n = t.schemaLookup(sib, seg.Node) // lyd_find_sibling_val without a value
		case seg.Preds[0].Kind == types.PredPosition:
			pos := uint64(1)
			for inst := range t.instances(sib, seg.Node) {
				if pos == seg.Preds[0].Position {
					n = inst
					break
				}
				pos++
			}
		case seg.Preds[0].Kind == types.PredLeafList:
			target := newTerm(seg.Node, seg.Preds[0].Value, &probes.work)
			if probes.work.hashErr != nil {
				return nil, u, probes.work.hashErr
			}
			var err error
			if n, err = t.findFirstWork(sib, target, &probes.work); err != nil {
				return nil, u, err
			}
		default:
			target, d, key, err := probes.lookupList(seg) // ly_path_eval_partial: lyd_create_list, store-only
			if err != nil {
				return nil, u, err
			}
			if d != nil {
				return nil, u, lg.storeErr(nil, key, d) // LY_CHECK_RET: logged at the key
			}
			if n, err = t.findFirstWork(sib, target, &probes.work); err != nil {
				return nil, u, err
			}
		}
		if probes.work.hashErr != nil {
			return nil, u, probes.work.hashErr
		}
		if n == nil {
			return prev, u, nil
		}
		prev, sib = n, t.childrenOf(n) // lyd_child_any: into an anydata value tree too
	}
	return prev, len(p), nil
}

// instances yields the instances of sn in s as LYD_LIST_FOR_INST does: from the first one while
// the schema node holds. Among an operation's children an output run split by the kept input
// ([v1, v2, in, v3]) thus yields v1 and v2, as libyang's walks do (lyd_validate_minmax,
// ly_path_eval positions; both unreachable on such a run, see
// ops/validate-json-reply-split-max-elements and ops/find-json-reply-split-position); the
// hash-bucket lookups for non-duplicate instances take their bucket instead.
func (t *Tree) instances(s *siblings, sn *schema.Node) func(func(*Node) bool) {
	return func(yield func(*Node) bool) {
		// LYD_LIST_FOR_INST tests the requested pointer on the first node too.
		for _, n := range t.instanceRun(s, sn) {
			if n.schema != sn || !yield(n) {
				return
			}
		}
	}
}

// instanceRun is the siblings from lyd_find_sibling_schema's (or lyd_find_sibling_val's)
// match for sn on; the caller decides how far the run of sn goes. The ordinal lets
// lys_getnext_data accept a foreign first match while LYD_LIST_FOR_INST rejects it.
func (t *Tree) instanceRun(s *siblings, sn *schema.Node) iter.Seq2[int, *Node] {
	return func(yield func(int, *Node) bool) {
		var run siblingSpan
		// With siblings of several contexts lyd_find_sibling_schema also matches a node of
		// another context (by name and schema parents), so the walk can start, and stop, at a
		// foreign instance the schema-pointer records do not see: the first registered record
		// of the children table, or without one the first sibling.
		mixed := s.schemaUnordered && !opChild(sn) && s.foreignFor(sn)
		switch {
		case mixed && s.ht != nil && s.firstRecordSchema(sn):
			if leader := s.leaderFirstMixed(sn); leader != nil {
				run = s.schemaSpan().Slice(s.schemaSpan().indexFromEnd(&t.work, leader), s.schemaSpan().Len())
			}
		case mixed:
			// A single instance (leaf, container, ...) has no first-instance record: the
			// schema lookup that matches other contexts by name finds it.
			if n := t.schemaLookup(s, sn); n != nil {
				run = s.schemaSpan().Slice(s.schemaSpan().indexFromEnd(&t.work, n), s.schemaSpan().Len())
			}
		case opChild(sn) || s.schemaUnordered:
			// Keep the ordered run lazy even when a caller needs only its first node.
			i := 0
			for n := range t.opRun(s, sn) {
				if !yield(i, n) {
					return
				}
				i++
			}
			return
		default:
			if i := t.schemaIndex(s, sn); i >= 0 {
				run = s.schemaSpan().Slice(i, s.schemaSpan().Len())
			}
		}
		for i, n := range run.All() {
			if !yield(i, n) {
				return
			}
		}
	}
}

// createList is lyd_create_list: an unlinked list instance with the predicate's keys, each stored
// again from its canonical text in the JSON format, so a union may take another member
// (enumeration "1" before uint8 for '01'); store-only for the lookups (ly_path_eval_partial, also
// under ly_path_eval, and lyd_find_sibling_val pass it), lyd_new_path its own option, which the
// port does not take. A key the re-store refuses (a canonical text its restriction rejects) fails
// with the store's diagnostic and that key's schema node.
func (t *Tree) createList(seg types.PathSegment, storeOnly bool) (*Node, *types.Diag, *schema.Node, error) {
	l := newInner(seg.Node, &t.work)
	if t.work.hashErr != nil {
		return nil, nil, nil, t.work.hashErr
	}
	l.flags = FlagNew
	store := types.Store
	if storeOnly {
		store = types.StoreOnly
	}
	for _, pr := range seg.Preds {
		if pr.Var != "" { // lyxp_vars_find without variables: LOGERR LY_ENOTFOUND, key by key
			return nil, &types.Diag{Code: types.CodeNone, Err: "LY_ENOTFOUND", Msg: fmt.Sprintf("Variable \"%s\" not defined.", pr.Var)}, nil, nil
		}
		v, d := store(pr.Key.Type, pr.Value.Canonical(), types.FormatJSON, types.HintData,
			types.ModuleNames{Set: seg.Node.Module.Owner}, pr.Key)
		if d != nil {
			return nil, d, pr.Key, nil
		}
		k := newTerm(pr.Key, v, &t.work)
		if t.work.hashErr != nil {
			return nil, nil, nil, t.work.hashErr
		}
		k.flags = FlagNew
		if err := t.insert(l, k, insertDefault); err != nil {
			return nil, nil, nil, err
		}
	}
	return l, nil, nil, nil
}

// lookupList is createList for a lookup (store-only). Store-only still validates what a value
// holds itself: an instance-identifier key re-stored from its canonical text checks its own
// predicates, which a restriction may now reject (ut-new/path-06); the diagnostic and the key's
// schema node are returned, as ly_path_eval_partial returns lyd_create_list's error.
func (t *Tree) lookupList(seg types.PathSegment) (*Node, *types.Diag, *schema.Node, error) {
	return t.createList(seg, true)
}

// newPath is lyd_new_path_ with lyd_new_path_create. Relative paths start at the parent's
// children; absolute paths start at the tree root even when a parent is supplied.
func (t *Tree) newPath(lg *logger, path, value string, o NewPathOptions) (*Node, error) {
	if o.Parent != nil {
		if c := ctxOf(o.Parent); c != nil && c != t.context() {
			return nil, lg.logErr("LY_EINVAL", "Different contexts mixed in a \"lyd_new_path\" function call.")
		}
		root := o.Parent
		for root.parent != nil {
			root = root.parent
		}
		if root.tree != t {
			return nil, lg.logErr("LY_EINVAL", "Parent node does not belong to this tree.")
		}
	}
	if (path == "" || path[0] != '/') && o.Parent == nil {
		return nil, argErr("(path[0] == '/') || parent", "lyd_new_path")
	}
	ctxNode := nodeSchema(t.context(), o.Parent)
	if o.Parent == nil && t.top.SchemaLen() > 0 {
		ctxNode = t.top.At(0).schema
	}
	p, msg, at := types.CompilePath(t.context(), ctxNode, path, false, true)
	if msg != "" {
		return nil, lg.item(nil, at, false, "LY_EVALID", ly.XPath, "", msg)
	}
	search, err := t.checkFindPath(lg, p, path, value)
	if err != nil {
		return nil, err
	}
	// ly_path_parse skips XML whitespace before deciding whether the path is absolute.
	absolute := strings.HasPrefix(strings.TrimLeft(path, " \t\n\r"), "/")
	var node *Node
	sib := &t.top
	if !absolute {
		sib = t.childrenOf(o.Parent) // lyd_child_any(parent)
	}
	idx := 0
	if sib.len() > 0 {
		var n *Node
		if n, idx, err = t.evalPartialFrom(lg, sib, search); err != nil {
			return nil, err
		}
		if idx == len(search) && len(search) == len(p) {
			// the node exists: update it or, a default node, set it
			if !o.Update && n.flags&FlagDefault == 0 {
				_ = lg.val(n, "", ly.Reference, "Path \"%s\" already exists.", path)
				return nil, rcError("LY_EEXIST") // LOGVAL, then ret = LY_EEXIST
			} else if o.Update && n.schema.IsKey() {
				return nil, nil // the key value is in the predicate, it cannot change
			}
			return t.newPathUpdate(lg, n, value)
		}
		node = n
	}
	beside := false
	if node == nil && o.Parent != nil {
		// LY_ENOTFOUND: lyd_new_path_create creates from the parent when the first segment
		// has a data parent; otherwise it passes the parent as lyd_insert_node's sibling,
		// so a missing absolute root joins the parent's siblings (tree_data_new.c:1615-1619,
		// 1772-1774).
		if p[0].Node.DataParent() != nil {
			node = o.Parent
		} else {
			node, beside = o.Parent.parent, true
		}
	}
	if idx < len(p) && isDupInstList(p[idx].Node) && len(p[idx].Preds) > 0 && p[idx].Preds[0].Kind == types.PredPosition {
		// LYD_LIST_FOR_INST(node ? lyd_child(node) : parent, ...): beside the parent, its own
		// siblings (an anydata value tree for a payload node)
		inst := &t.top
		switch {
		case beside:
			inst = o.Parent.siblingsOf()
		case node != nil:
			inst = &node.kids
		}
		if err := t.checkPosition(lg, inst, p[idx]); err != nil {
			return nil, err
		}
	}
	var position uint64
	if idx < len(p) {
		var err error
		if position, err = t.insertPositionBound(t.childrenOf(node), &Node{schema: p[idx].Node}, insertDefault); err != nil {
			return nil, err
		}
	}
	if err := t.preflightNewPathHash(p[idx:], node, value, position); err != nil {
		return nil, err
	}
	var first, last *Node
	created := false
	for ; idx < len(p); idx++ {
		seg, parent := p[idx], node
		var err error
		switch seg.Node.Kind {
		case schema.List:
			if isDupInstList(seg.Node) {
				node = newInner(seg.Node, &t.work)
				err = t.work.hashErr
				node.flags = FlagNew
			} else {
				var d *types.Diag
				var key *schema.Node
				if node, d, key, err = t.createList(seg, false); d != nil {
					err = lg.storeErr(nil, key, d) // lyd_create_term(key, NULL parent): at the key
				}
			}
		case schema.Container:
			node = newInner(seg.Node, &t.work)
			err = t.work.hashErr
			node.flags = FlagNew
			if isNPCont(seg.Node) {
				node.flags |= FlagDefault
			}
		case schema.LeafList:
			val := value
			if len(seg.Preds) > 0 && seg.Preds[0].Kind == types.PredLeafList {
				val = seg.Preds[0].Value.Canonical()
			}
			node, err = t.createTerm(lg, last, seg.Node, val)
		case schema.Leaf:
			if seg.Node.IsKey() && parent != nil && parent.schema != nil {
				node = t.schemaLookup(&parent.kids, seg.Node) // created with its list
				if first == nil {
					first = node
				}
				last = node
				continue
			}
			node, err = t.createTerm(lg, last, seg.Node, value)
		default: // rpc, action, notification, anydata: any construction awaits its public API
			limit := ""
			if seg.Node.Kind == schema.AnyData || seg.Node.Kind == schema.AnyXML {
				limit = " (U-0112)"
			}
			err = fmt.Errorf("data: creating %s %q is not supported%s: %w", nodetypeStr(seg.Node.Kind), seg.Node.Name, limit, errors.ErrUnsupported)
		}
		if err != nil {
			if created {
				if uerr := unlink(first); uerr != nil {
					return nil, uerr // the sticky budget/journal failure outranks err
				}
			}
			return nil, err
		}
		if err := t.insert(parent, node, insertDefault); err != nil {
			if created {
				if uerr := unlink(first); uerr != nil {
					return nil, uerr // the sticky budget/journal failure outranks err
				}
			}
			return nil, err
		}
		if first == nil {
			first, created = node, true
		}
		last = node
	}
	return first, nil
}

// preflightNewPathHash reserves the hashes of all remaining path segments
// before a newly created ancestor can enter an indexed sibling set, together
// with position, the first insertion's insertPositionBound.
func (t *Tree) preflightNewPathHash(segments types.Path, parent *Node, value string, position uint64) error {
	if len(segments) == 0 {
		return nil
	}
	indexed := parent != nil && parent.schema != nil &&
		(parent.kids.ht != nil || parent.kids.SchemaLen()+1 >= htMinItems)
	amount := position
	if indexed && parent.kids.ht == nil {
		for _, old := range parent.kids.SchemaAll() {
			amount = addHashBound(amount, indexWorkBound(old, nil, nil, &t.work))
		}
	}
	for _, seg := range segments {
		sn := seg.Node
		schemaHashWork(sn, &t.work)
		if t.work.hashErr != nil {
			return t.work.hashErr
		}
		switch sn.Kind {
		case schema.List:
			// Each key link can inspect the growing positional prefix;
			// the final link also scans the completed hash input.
			keys := uint64(len(sn.Keys))
			amount = addHashBound(amount, addHashBound(keys*keys, keys))
			for _, pred := range seg.Preds {
				if pred.Key == nil || pred.Var != "" {
					continue
				}
				schemaHashWork(pred.Key, &t.work)
				v, diag := types.Store(pred.Key.Type, pred.Value.Canonical(), types.FormatJSON,
					types.HintData, types.ModuleNames{Set: t.set}, pred.Key)
				if diag != nil {
					continue // the creation reports the value diagnostic
				}
				amount = addHashBound(amount, types.HashLYBWorkBound(v))
				if indexed {
					indexNameDigest(pred.Key, &t.work)
					amount = addHashBound(amount, 48+2*uint64(len(v.Canonical())))
				}
			}
		case schema.LeafList:
			text := value
			if len(seg.Preds) > 0 && seg.Preds[0].Kind == types.PredLeafList {
				text = seg.Preds[0].Value.Canonical()
			}
			v, diag := types.Store(sn.Type, text, types.FormatJSON, types.HintData,
				types.ModuleNames{Set: t.set}, sn)
			if diag == nil {
				amount = addHashBound(amount, types.HashLYBWorkBound(v))
				if indexed {
					amount = addHashBound(amount, 2*uint64(len(v.Canonical())))
				}
			}
		}
		if t.work.hashErr != nil {
			return t.work.hashErr
		}
		indexed = sn.Kind == schema.List && len(sn.Keys)+1 >= htMinItems
	}
	if err := t.work.checkHashWork(amount); err != nil {
		t.work.hashErr = err
		return err
	}
	return nil
}

// checkPosition is the position check of lyd_new_path_create: a key-less list or state
// leaf-list instance can be created at most right after the existing ones.
func (t *Tree) checkPosition(lg *logger, sib *siblings, seg types.PathSegment) error {
	count := uint64(0)
	for range t.instances(sib, seg.Node) {
		count++
	}
	pos := seg.Preds[0].Position
	switch {
	case count+1 >= pos:
		return nil
	case count == 0:
		_ = lg.val(nil, "", ly.Reference, "Cannot create \"%s\" on position %d, no instances exist.", seg.Node.Name, pos)
	case count > 1:
		_ = lg.val(nil, "", ly.Reference, "Cannot create \"%s\" on position %d, only %d instances exist.", seg.Node.Name, pos, count)
	default:
		_ = lg.val(nil, "", ly.Reference, "Cannot create \"%s\" on position %d, only %d instance exists.", seg.Node.Name, pos, count)
	}
	return rcError("LY_EINVAL") // LOGVAL, then ret = LY_EINVAL
}

// checkFindPath is lyd_new_path_check_find_lypath (without LYD_NEW_PATH_OPAQ): lists need their
// keys, a leaf-list instance without a predicate gets one from the value, and the path is cut
// before a key-less list or state leaf-list instance, which is always created. It returns the
// path to search for existing nodes. An invalid leaf-list value is not logged (libyang validates
// it with log 0): the error carries the type's message only.
func (t *Tree) checkFindPath(lg *logger, p types.Path, path, value string) (types.Path, error) {
	newCount := len(p)
	for u := range p {
		sn := p[u].Node
		preds := p[u].Preds
		switch {
		case isDupInstList(sn):
			if len(preds) == 0 || sn.Kind == schema.LeafList && preds[0].Kind == types.PredLeafList {
				newCount = u
			} else if preds[0].Kind != types.PredPosition {
				lg.locSet(sn)
				defer lg.locBack(1)
				_ = lg.val(nil, "", ly.XPath, "Invalid predicate for state %s \"%s\" in path \"%s\".",
					nodetypeStr(sn.Kind), sn.Name, path)
				return nil, rcError("LY_EINVAL")
			}
		case sn.Kind == schema.List && (len(preds) == 0 || preds[0].Kind != types.PredKey):
			lg.locSet(sn)
			defer lg.locBack(1)
			_ = lg.val(nil, "", ly.XPath, "Predicate missing for %s \"%s\" in path \"%s\".",
				nodetypeStr(sn.Kind), sn.Name, path)
			return nil, rcError("LY_EINVAL")
		case sn.Kind == schema.LeafList && (len(preds) == 0 || preds[0].Kind != types.PredLeafList):
			v, d := types.Store(sn.Type, value, types.FormatJSON, types.HintData, types.ModuleNames{Set: sn.Module.Owner}, sn)
			if d != nil {
				return nil, rcError(d.RC()) // lyd_value_validate3 without logging: the plugin's rc
			}
			p[u].Preds = append(p[u].Preds, types.PathPred{Kind: types.PredLeafList, Value: v})
		}
	}
	return p[:newCount], nil
}

// createTerm is lyd_create_term with a JSON value and LYD_HINT_DATA; lnode is the data parent
// for the error location.
func (t *Tree) createTerm(lg *logger, lnode *Node, sn *schema.Node, value string) (*Node, error) {
	v, d := types.Store(sn.Type, value, types.FormatJSON, types.HintData, types.ModuleNames{Set: sn.Module.Owner}, sn)
	if d != nil {
		return nil, lg.storeErr(lnode, sn, d)
	}
	if err := t.work.checkTermHash(lnode, sn, v); err != nil {
		return nil, err
	}
	n := newTerm(sn, v, &t.work)
	if t.work.hashErr != nil {
		return nil, t.work.hashErr
	}
	n.flags = FlagNew
	return n, nil
}

// newPathUpdate is lyd_new_path_update: an existing term gets the value; the node is returned
// when its value or default flag changed.
func (t *Tree) newPathUpdate(lg *logger, n *Node, value string) (*Node, error) {
	switch n.schema.Kind {
	case schema.Leaf:
	case schema.LeafList:
		if !isDupInstList(n.schema) {
			return nil, nil
		}
	case schema.AnyData, schema.AnyXML:
		return nil, fmt.Errorf("data: anydata values: %w", errors.ErrUnsupported) // M5
	default:
		return nil, nil // an inner node has nothing to update
	}
	// _lyd_change_term
	t.updateCtx = ctxOf(n)
	v, d := types.Store(n.schema.Type, value, types.FormatJSON, types.HintData, types.ModuleNames{Set: ctxOf(n)}, n.schema)
	if d != nil {
		return nil, lg.storeErr(n, n.schema, d)
	}
	valChange, dfltChange := t.changeTermVal(n, v, false)
	if t.work.hashErr != nil {
		return nil, t.work.hashErr
	}
	if valChange || dfltChange {
		return n, nil
	}
	return nil, nil
}

// changeTermVal is lyd_change_term_val: the term takes the value v (if it differs) and the
// default flag dflt; it reports what changed.
func (t *Tree) changeTermVal(n *Node, v types.Value, dflt bool) (valChange, dfltChange bool) {
	return t.changeTermValCompared(n, v, dflt, types.Equal(n.value, v))
}

// changeTermValCompared is lyd_change_term_val with the compare callback's result given: across
// contexts the callbacks compare storage that the port's value equality does not see.
func (t *Tree) changeTermValCompared(n *Node, v types.Value, dflt, equal bool) (valChange, dfltChange bool) {
	if !equal {
		hashAmount, err := t.work.checkChangedHash(n, v)
		if err != nil {
			t.work.hashErr = err
			return false, false
		}
		t.changeNodeValue(n, v, hashAmount)
		if t.work.hashErr != nil {
			return false, false
		}
		valChange = true
		freeLinks(n) // the changed value drops its leafref links (LY_CTX_LEAFREF_LINKING)
		if err := n.touch(); err != nil {
			return false, false
		}
		n.flags |= FlagNew
	}
	switch {
	case n.flags&FlagDefault != 0 && !dflt:
		if err := n.touch(); err != nil {
			return false, false
		}
		n.flags &^= FlagDefault
		npContDfltDel(n.parent)
		dfltChange = true
	case n.flags&FlagDefault == 0 && dflt:
		if err := n.touch(); err != nil {
			return false, false
		}
		n.flags |= FlagDefault
		npContDfltSet(n.parent)
		dfltChange = true
	}
	return valChange, dfltChange
}

// changeNodeValue is lyd_change_node_value: a leaf-list instance or a list key changes the hash
// of its instance, which is re-inserted only when a same-schema neighbour can be re-sorted.
func (t *Tree) changeNodeValue(n *Node, v types.Value, hashAmount uint64) {
	target := n
	switch {
	case n.schema.Kind == schema.LeafList:
	case n.isKey():
		target = n.parent
	default:
		if err := n.touch(); err != nil {
			return
		}
		n.value = v
		reindexComparedParent(n)
		return
	}
	sib, parent := target.siblingsOf(), target.parent
	if sib == nil {
		if err := n.touch(); err != nil {
			return
		}
		n.value = v
		refreshInstanceHash(&t.work, target)
		return
	}
	// LYD_NODE_IS_ALONE tests the physical neighbours, not the number of siblings
	// or the number of instances in other runs of this schema.
	relocate := false
	if sortedSupported(target) {
		// One indexed instance is necessarily alone; avoid a charged position
		// lookup through unrelated siblings on its hash-only update.
		var entry *opEntry
		if sib.order != nil {
			entry = sib.order.entries[target.schema]
		}
		if entry == nil || entry.count != 1 || sib.documentFirst(target.schema) != target {
			at, err := t.originIndex(sib, sib.schemaSpan(), target)
			if err != nil {
				t.work.hashErr = err
				return
			}
			left, right := sib.opNeighbours(at)
			// In libyang the first sibling's prev points to the last sibling.
			if at == 0 && sib.OpaqueLen() == 0 && sib.SchemaLen() > 1 {
				left = sib.At(sib.SchemaLen() - 1).schema
			}
			relocate = left == target.schema || right == target.schema
		}
	}
	if relocate {
		// A cold anchor lookup during reinsertion can still exhaust the quota.
		// Preserve the old native snapshot until the node is linked again.
		oldValue, oldHash := n.value, target.hash
		oldKeyN, oldLkeyN := target.keyN, target.lkeyN
		oldFrozen, oldOK := target.lkeyFrozen, target.lkeyOK
		oldPrefix, oldDirty := target.lkeyPrefix, target.lkeyDirty
		type slot struct {
			key *Node
			old int
		}
		var slots []slot
		if n.isKey() {
			for _, key := range target.kids.SchemaAll() {
				t.work.Add(1)
				if err := t.work.chargeHash(1); err != nil {
					return
				}
				if !key.isKey() {
					break
				}
				slots = append(slots, slot{key, key.keySlot})
			}
		}
		// The earlier value preflight precedes this charged snapshot scan. Check
		// the remaining hash event against the updated total before unlinking.
		if err := t.work.checkChangedIndex(target, n, &v, hashAmount); err != nil {
			t.work.hashErr = err
			return
		}
		if err := t.preflightInsertAnchor(parent, target); err != nil {
			return
		}
		if err := t.work.checkChangedIndex(target, n, &v, hashAmount); err != nil {
			t.work.hashErr = err
			return
		}
		if err := preflightUnlink(target); err != nil {
			t.work.hashErr = err
			return
		}
		if err := t.preflightChangedReinsert(n, target, v); err != nil {
			t.work.hashErr = err
			return
		}
		restore, arm, err := t.linkOrigin(target)
		if err != nil {
			return
		}
		if err := t.work.checkChangedIndex(target, n, &v, hashAmount); err != nil {
			t.work.hashErr = err
			return
		}
		if err := arm(); err != nil {
			return
		}
		if err := unlinkRetainingHistory(target); err != nil {
			return
		}
		if err := n.touch(); err != nil {
			return
		}
		n.value = v
		refreshInstanceHash(&t.work, target)
		linkErr := t.work.hashErr
		if linkErr == nil {
			linkErr = t.insert(parent, target, insertDefault)
		}
		if linkErr != nil {
			n.value, target.hash = oldValue, oldHash
			target.keyN, target.lkeyN = oldKeyN, oldLkeyN
			target.lkeyFrozen, target.lkeyOK = oldFrozen, oldOK
			target.lkeyPrefix, target.lkeyDirty = oldPrefix, oldDirty
			for _, s := range slots {
				if err := s.key.touch(); err != nil {
					return
				}
				s.key.keySlot = s.old
			}
			restore()
			// the refused reinsertion is the change's result (SetValue, diff, Merge)
			if t.work.hashErr == nil {
				t.work.hashErr = linkErr
			}
		}
		return
	}
	// Position and registration preparation above can spend the last unit of
	// this operation's allowance even for a singleton that stays in place.
	if err := t.work.checkChangedIndex(target, n, &v, hashAmount); err != nil {
		t.work.hashErr = err
		return
	}
	at := -1
	var prev, next *Node
	indexed := false
	if sib.firstRecordSchema(target.schema) {
		if prev, next, indexed = sib.opRehashNeighbours(target); indexed {
			sib.leaderUnlinkBetween(target, prev, next, false)
		} else {
			// Split runs need the physical neighbours.
			at = sib.schemaSpan().indexFromEnd(&t.work, target)
			sib.leaderUnlink(target, at, false)
		}
	}
	sib.hashRemove(target)
	if err := n.touch(); err != nil {
		return
	}
	sib.refreshExact(target, func() {
		n.value = v
		refreshInstanceHash(&t.work, target)
	})
	if sib.ht != nil {
		sib.hashPut(target)
	} else {
		sib.hashAdd(parent, target)
	}
	reindexComparedParent(target)
	if indexed {
		sib.leaderInsertBetween(target, prev, next)
	} else if at >= 0 {
		sib.leaderInsert(target, at)
	}
}

// dupInst is a lyd_dup_inst cache entry: the equal instances of a first instance and how many
// were used.
type dupInst struct {
	set  []*Node
	used int
}

// keylessFirstIndex is the narrowing index of the one native run selected by
// lyd_find_sibling_first. With a children table, first is the identity of the
// selected first-instance record; gen ties that selection to the sibling-table
// generation in which it was observed. members lets an insertion extend the
// index only when it is adjacent to that same selected run.
type keylessFirstIndex struct {
	first   *record
	gen     uint64
	buckets map[string]*cacheDeque
	members map[*Node]struct{}
}

// dupCache is the duplicate instance cache of one sibling level (lyd_dup_inst_get's hash table).
// A level without a children table gets a document-order index built once per schema node; a
// level with one reads the live lyd_hash bucket in registration order, or, with instances of
// several contexts, a registration-order index by native key. Derived indexes follow
// live candidate order; consumed duplicate sets retain history. dupInstNext skips
// indexed candidates that have since left the sibling level.
type dupCache struct {
	inst         map[*Node]*dupInst
	runs         map[*schema.Node]map[idxKey]*cacheDeque // sibling order; a deque, creates prepend
	tableRuns    map[*siblings]map[string][]*Node
	keylessFirst map[*siblings]map[*schema.Node]*keylessFirstIndex
	keylessDup   map[*siblings]map[*schema.Node]*keylessDupIndex
}

// keylessDupIndex records the native candidate-order branch in which its
// buckets were built. Creating a children table registers live physical order,
// so a no-table bucket cannot simply append that transition's inserted node.
type keylessDupIndex struct {
	table   bool
	buckets map[string]*cacheDeque
}

func selectedFirstRecord(sib *siblings, sn *schema.Node) *record {
	if sib == nil || sib.ht == nil {
		return nil
	}
	if chain := sib.leaders[sn]; chain != nil {
		return chain.first
	}
	return nil
}

// keylessDigest returns n's digest only when this cache has an index that can become stale.
func (c *dupCache) keylessDigest(n *Node, work *workCounter) (string, bool, error) {
	if n == nil || n.schema == nil {
		return "", false, nil
	}
	sib := n.siblingsOf()
	if sib == nil {
		return "", false, nil
	}
	first := c.keylessFirst[sib]
	dup := c.keylessDup[sib]
	if first[n.schema] == nil && dup[n.schema] == nil {
		return "", false, nil
	}
	key, ok := subtreeEquivalenceKey(n, work)
	return key, ok, workError(work)
}

// invalidateChangedKeyless removes subtree-digest buckets only when descendant edits changed n's key.
// The native duplicate-instance cache deliberately remains intact: once an instance has been
// consumed, lyd_dup_inst_next does not make it available again after its descendants change.
func (c *dupCache) invalidateChangedKeyless(n *Node, before string, tracked bool, work *workCounter) error {
	if !tracked {
		return nil
	}
	after, ok := subtreeEquivalenceKey(n, work)
	if err := workError(work); err != nil {
		return err
	}
	if ok && after == before {
		return nil
	}
	sib := n.siblingsOf()
	if bySchema := c.keylessFirst[sib]; bySchema != nil {
		delete(bySchema, n.schema)
	}
	if bySchema := c.keylessDup[sib]; bySchema != nil {
		delete(bySchema, n.schema)
	}
	return nil
}

func (t *Tree) keylessFirstRun(c *dupCache, sib *siblings, sn *schema.Node, target *Node) ([]*Node, error) {
	if c.keylessFirst == nil {
		c.keylessFirst = map[*siblings]map[*schema.Node]*keylessFirstIndex{}
	}
	bySchema := c.keylessFirst[sib]
	if bySchema == nil {
		bySchema = map[*schema.Node]*keylessFirstIndex{}
		c.keylessFirst[sib] = bySchema
	}
	first := selectedFirstRecord(sib, sn)
	entry := bySchema[sn]
	if entry == nil || entry.first != first || entry.gen != sib.gen {
		entry = &keylessFirstIndex{first: first, gen: sib.gen, buckets: map[string]*cacheDeque{}, members: map[*Node]struct{}{}}
		// With a children table, lyd_find_sibling_first starts at the native
		// first-instance record and walks only that contiguous physical run.
		// Without one it walks every sibling, so split operation runs must all be
		// indexed in document order.
		var all iter.Seq[*Node]
		if sib.ht == nil {
			all = sib.all()
		} else {
			all = t.instances(sib, sn)
		}
		for n := range all {
			if n.schema != sn {
				continue
			}
			key, ok := subtreeEquivalenceKey(n, &t.work)
			if !ok {
				return nil, workError(&t.work)
			}
			appendKeylessBucket(entry.buckets, key, n)
			entry.members[n] = struct{}{}
		}
		bySchema[sn] = entry
	}
	key, ok := subtreeEquivalenceKey(target, &t.work)
	if !ok {
		return nil, workError(&t.work)
	}
	if bucket := entry.buckets[key]; bucket != nil {
		return bucket.nodes, nil
	}
	return nil, nil
}

func (t *Tree) keylessFirst(c *dupCache, sib *siblings, target *Node) (*Node, error) {
	if target == nil || sib == nil || sib.len() == 0 {
		return nil, nil
	}
	// lyd_find_sibling_first rejects a schema-parent mismatch before finding the
	// first sibling, resolving a foreign schema, or touching the children table.
	if sib.SchemaLen() > 0 && sib.At(0).schema != nil && target.schema != nil &&
		!compareSchemaEqual(sib.At(0).schema.DataParent(), target.schema.DataParent(), true) {
		return nil, nil
	}
	sn := target.schema
	if sib.SchemaLen() != 0 {
		first := sib.At(0)
		if !sameSet(ctxOf(first), ctxOf(target)) {
			// lyd_find_sibling_first maps no schema in its linear branch. In
			// particular, lyd_find_schema_ctx always walks operation input, so it
			// cannot map an output child unless the table branch actually needs it.
			if sib.ht == nil {
				for candidate := range sib.all() {
					equal, err := compareSingleErr(t, candidate, target, true)
					if err != nil || equal {
						return candidate, err
					}
				}
				return nil, nil
			}
			var err error
			if sn, err = findSchemaCtx(target.schema, ctxOf(first), first.parent); err != nil {
				return nil, nil
			}
		}
	}
	// lyd_find_sibling_first selects its full-recursion branch from the source
	// schema, but lyd_find_sibling_val may resolve that schema to a keyed list in
	// the destination context. Such candidates have no keyless-subtree digest;
	// retain the native comparison walk instead of rejecting the mapped run.
	if !sn.Keyless() {
		for candidate := range t.instances(sib, sn) {
			// Cross-context merge has no direct native call (libyang rejects mixed
			// contexts). Once the source schema maps to a non-duplicate destination
			// schema, preserve this compatibility path's candidate-first verdict.
			equal, err := compareSingleErr(t, candidate, target, true)
			if err != nil || equal {
				return candidate, err
			}
		}
		return nil, nil
	}
	run, err := t.keylessFirstRun(c, sib, sn, target)
	if err != nil {
		return nil, err
	}
	for _, candidate := range run {
		first, second := candidate, target
		if sib.ht != nil {
			// lyd_find_sibling_first's children-table branch compares the
			// lookup target against each candidate. Its linear branch compares
			// each sibling against the target.
			first, second = target, candidate
		}
		equal, err := compareSingleErr(t, first, second, true)
		if err != nil || equal {
			return candidate, err
		}
	}
	return nil, nil
}

// keylessDupRun narrows lyd_find_sibling_dup_inst_set's complete candidate
// set while retaining its order: hash registration order with a children
// table, physical sibling order without one. Unlike keylessFirstRun it must
// include equal instances from every split run.
func (t *Tree) keylessDupRun(c *dupCache, sib *siblings, target *Node, k idxKey) ([]*Node, error) {
	if c.keylessDup == nil {
		c.keylessDup = map[*siblings]map[*schema.Node]*keylessDupIndex{}
	}
	bySchema := c.keylessDup[sib]
	if bySchema == nil {
		bySchema = map[*schema.Node]*keylessDupIndex{}
		c.keylessDup[sib] = bySchema
	}
	entry := bySchema[target.schema]
	if entry == nil || entry.table != (sib.ht != nil) {
		entry = &keylessDupIndex{table: sib.ht != nil, buckets: map[string]*cacheDeque{}}
		var all iter.Seq[*Node]
		switch {
		case sib.ht != nil:
			all = sib.bucketNodes(k)
		case sib.order != nil && (opChild(target.schema) || sib.schemaUnordered):
			all = sib.allInstances(target.schema)
		default:
			all = sib.all()
		}
		for n := range all {
			if n.schema != target.schema {
				continue
			}
			key, ok := subtreeEquivalenceKey(n, &t.work)
			if !ok {
				return nil, workError(&t.work)
			}
			appendKeylessBucket(entry.buckets, key, n)
		}
		bySchema[target.schema] = entry
	}
	key, ok := subtreeEquivalenceKey(target, &t.work)
	if !ok {
		return nil, workError(&t.work)
	}
	if bucket := entry.buckets[key]; bucket != nil {
		return bucket.nodes, nil
	}
	return nil, nil
}

func dupNativeKey(n *Node) string {
	k, _ := hashOf(n)
	return n.schema.Module.Name + "\x00" + n.schema.Name + "\x00" + k.key
}

// run returns the instances of the siblings of inst that hash like it (opaque nodes: the opaque
// siblings). A key-less list hashes by schema node only, so its run is all its instances,
// compared whole (libyang parity).
func (t *Tree) run(c *dupCache, inst *Node) (siblingSpan, error) {
	sib := sibsOf(inst) // a detached node: its singleton sibling set
	if inst.schema == nil {
		return sib.opaqueSpan(), nil
	}
	k, ok := indexedKey(inst, &t.work)
	if !ok {
		return siblingSpan{}, workError(&t.work)
	}
	if inst.schema.Kind == schema.List && inst.schema.Keyless() && !sib.mixedContexts() {
		nodes, err := t.keylessDupRun(c, sib, inst, k)
		return spanOf(nodes), err
	}
	if sib.ht != nil && !sib.mixedContexts() {
		// lyd_find_sibling_dup_inst_set reads the live hash bucket on each first lookup.
		out := make([]*Node, 0)
		for n := range sib.bucketNodes(k) {
			out = append(out, n)
		}
		return spanOf(out), nil
	}
	if sib.ht != nil {
		// Instances compiled in another context hash alike in libyang (names, not schema
		// pointers), so one index over the level groups them by native key.
		if c.tableRuns == nil {
			c.tableRuns = map[*siblings]map[string][]*Node{}
		}
		idx := c.tableRuns[sib]
		if idx == nil {
			idx = map[string][]*Node{}
			records := make([]*record, 0, len(sib.exactByNode))
			for _, r := range sib.exactByNode {
				records = append(records, r)
			}
			slices.SortFunc(records, func(a, b *record) int {
				switch {
				case a.order < b.order:
					return -1
				case a.order > b.order:
					return 1
				}
				return 0
			})
			for _, r := range records {
				t.work.Add(1)
				idx[dupNativeKey(r.node)] = append(idx[dupNativeKey(r.node)], r.node)
			}
			c.tableRuns[sib] = idx
		}
		return spanOf(idx[dupNativeKey(inst)]), nil
	}
	if sib.mixedContexts() {
		// lyd_find_sibling_dup_inst_set's no-table branch compares each
		// sibling, including instances compiled in another context.
		return sib.schemaSpan(), nil
	}
	if c.runs == nil {
		c.runs = map[*schema.Node]map[idxKey]*cacheDeque{}
	}
	idx := c.runs[inst.schema]
	if idx == nil {
		idx = map[idxKey]*cacheDeque{}
		all := t.instances(sib, inst.schema)
		if opChild(inst.schema) || sib.schemaUnordered {
			// Merge must find every instance, including runs split by an anchor in an
			// operation or an unordered copy.
			all = slices.Values(sib.opInst(inst.schema))
		}
		for n := range all {
			t.work.Add(1)
			if nk, ok := indexedKey(n, &t.work); ok {
				if idx[nk] == nil {
					idx[nk] = &cacheDeque{}
				}
				idx[nk].nodes = append(idx[nk].nodes, n)
			}
		}
		c.runs[inst.schema] = idx
	}
	if d := idx[k]; d != nil {
		return spanOf(d.nodes), nil
	}
	return siblingSpan{}, nil
}

// appendKeylessBucket builds a digest bucket in its caller's native iteration order.
func appendKeylessBucket(index map[string]*cacheDeque, key string, n *Node) {
	if index[key] == nil {
		index[key] = &cacheDeque{}
	}
	index[key].nodes = append(index[key].nodes, n)
}

// insertKeylessBucket extends a physical-order bucket. An operation's schema
// anchor can insert before an older equal instance, even during Merge. Deque
// headroom keeps repeated prepends amortized constant; middle moves are charged.
func insertKeylessBucket(index map[string]*cacheDeque, key string, n *Node, sib *siblings, work *workCounter) {
	bucket := index[key]
	if bucket == nil {
		appendKeylessBucket(index, key, n)
		return
	}
	var at int
	if sib.order != nil {
		at, _ = slices.BinarySearchFunc(bucket.nodes, n, func(a, b *Node) int {
			work.Add(1)
			if err := work.chargeHash(1); err != nil {
				return 0
			}
			cmp, _ := sib.order.seq.Compare(sib.order.byNode[a], sib.order.byNode[b])
			return cmp
		})
	} else {
		// Without the operation sequence, Merge inserts at the end of the
		// schema run. Ordinary siblings are contiguous and append here.
		at = len(bucket.nodes)
	}
	if work.hashErr != nil {
		return
	}
	// Charge shifts and backing-storage copies before changing this local cache.
	l := bucket.nodes
	copies := 0
	if at >= len(l)/2 || len(l) == 0 {
		if len(l) == cap(l) {
			copies = len(l)
		}
	} else {
		off := cap(bucket.base) - cap(l)
		if off <= 0 || off >= len(bucket.base) || &bucket.base[off] != &l[0] {
			copies = len(l)
		}
	}
	moves := len(l) - at
	if at < len(l)/2 {
		moves = at
	}
	if err := work.chargeHash(uint64(moves + copies + 1)); err != nil {
		return
	}
	work.Add(int64(copies + bucket.insertAt(at, n)))
}

// added records n, just inserted at the level of c, in the level's index.
func (c *dupCache) added(n *Node, work *workCounter) {
	if n.schema != nil && n.schema.Kind == schema.List && n.schema.Keyless() && c.keylessFirst != nil {
		if sib := n.siblingsOf(); sib != nil {
			if bySchema := c.keylessFirst[sib]; bySchema != nil {
				if index := bySchema[n.schema]; index != nil {
					first := selectedFirstRecord(sib, n.schema)
					switch {
					case index.first != first:
						delete(bySchema, n.schema)
					case sib.ht == nil || keylessRunAdjacent(sib, index, n):
						if key, ok := subtreeEquivalenceKey(n, work); ok {
							insertKeylessBucket(index.buckets, key, n, sib, work)
							index.members[n] = struct{}{}
							index.gen = sib.gen
						}
					default:
						// The table generation changed, but the insertion was outside
						// the selected run. Keep its contents and advance only its key.
						index.gen = sib.gen
					}
				}
			}
		}
	}
	if n.schema != nil && n.schema.Kind == schema.List && n.schema.Keyless() && c.keylessDup != nil {
		if sib := n.siblingsOf(); sib != nil {
			if bySchema := c.keylessDup[sib]; bySchema != nil {
				if index := bySchema[n.schema]; index != nil {
					if index.table != (sib.ht != nil) {
						delete(bySchema, n.schema)
					} else if key, ok := subtreeEquivalenceKey(n, work); ok {
						if sib.ht != nil {
							// Native registration order always appends.
							appendKeylessBucket(index.buckets, key, n)
						} else {
							insertKeylessBucket(index.buckets, key, n, sib, work)
						}
					}
				}
			}
		}
	}
	if sib := n.siblingsOf(); sib != nil && c.tableRuns != nil {
		if idx := c.tableRuns[sib]; idx != nil && n.schema != nil {
			key := dupNativeKey(n)
			idx[key] = append(idx[key], n)
		}
	}
	if idx := c.runs[n.schema]; idx != nil {
		if k, ok := indexedKey(n, work); ok {
			if idx[k] == nil {
				idx[k] = &cacheDeque{}
			}
			idx[k].nodes = append(idx[k].nodes, n)
		}
	}
}

// keylessRunAdjacent reports whether newly linked n joined the cached selected
// run. One insertion can extend a run only beside one of its previous members;
// checking the mutation-maintained sequence avoids walking the growing run.
func keylessRunAdjacent(sib *siblings, index *keylessFirstIndex, n *Node) bool {
	if index.first != nil && index.first.node == n {
		return true
	}
	if sib.order != nil {
		h := sib.order.byNode[n]
		if h == nil {
			return false
		}
		if prev := sib.order.seq.Prev(h); prev != nil {
			if _, ok := index.members[prev.Value]; ok {
				return true
			}
		}
		if next := sib.order.seq.Next(h); next != nil {
			_, ok := index.members[next.Value]
			return ok
		}
		return false
	}
	at := sib.schemaSpan().indexFromEnd(sib.work, n)
	return at > 0 && hasNode(index.members, sib.At(at-1)) ||
		at >= 0 && at+1 < sib.SchemaLen() && hasNode(index.members, sib.At(at+1))
}

func hasNode(set map[*Node]struct{}, n *Node) bool {
	_, ok := set[n]
	return ok
}

// created records n, just created at the level of c by ApplyDiff: lyd_find_sibling_dup_inst_set
// reads the live siblings on each first lookup (tree_data_common.c:121-125), so a set built later
// must include n. The registration-order index gets n at its end (n is registered last); the
// sibling-order index gets it where it now is among the live siblings of its run.
func (t *Tree) created(c *dupCache, n *Node) {
	if n.schema == nil {
		return
	}
	if n.schema.Kind == schema.List && n.schema.Keyless() && c.keylessFirst != nil {
		if sib := n.siblingsOf(); sib != nil {
			if bySchema := c.keylessFirst[sib]; bySchema != nil && bySchema[n.schema] != nil {
				// ApplyDiff can place a new duplicate before an indexed instance.
				// Rebuild on the next lookup so candidate order remains native.
				delete(bySchema, n.schema)
			}
		}
	}
	if n.schema.Kind == schema.List && n.schema.Keyless() && c.keylessDup != nil {
		if sib := n.siblingsOf(); sib != nil {
			if bySchema := c.keylessDup[sib]; bySchema != nil {
				delete(bySchema, n.schema)
			}
		}
	}
	if sib := n.siblingsOf(); sib != nil && c.tableRuns != nil {
		if idx := c.tableRuns[sib]; idx != nil {
			key := dupNativeKey(n)
			idx[key] = append(idx[key], n)
		}
	}
	idx := c.runs[n.schema]
	if idx == nil {
		return // built from the live siblings on its first lookup
	}
	sib := n.siblingsOf()
	if sib == nil || opChild(n.schema) || sib.schemaUnordered {
		// ponytail: operation children and unordered copies are indexed in document order, not
		// sibling-list order; rebuild on the next lookup (O(n) per create), slot them if it matters.
		delete(c.runs, n.schema)
		return
	}
	k, ok := indexedKey(n, &t.work)
	if !ok {
		return
	}
	run := idx[k]
	if run == nil {
		run = &cacheDeque{}
		idx[k] = run
	}
	// the deque moves the shorter side: prepends and appends are amortized O(1), a middle slot
	// is charged per moved entry
	t.work.Add(int64(run.insertAt(t.runSlot(sib.schemaSpan(), run.nodes, n, k), n)))
}

// moved re-records n, just moved within the level of c by an ApplyDiff replace: lyd_diff_insert
// unlinks and reinserts it, so a set built later sees it at its new sibling position and, in a
// children table, registered last. Sets already built for a first instance stay as they are.
func (t *Tree) moved(c *dupCache, n *Node) {
	if n.schema == nil {
		return
	}
	if n.schema.Kind == schema.List && n.schema.Keyless() && c.keylessFirst != nil {
		if sib := n.siblingsOf(); sib != nil {
			if bySchema := c.keylessFirst[sib]; bySchema != nil {
				delete(bySchema, n.schema)
			}
		}
	}
	if n.schema.Kind == schema.List && n.schema.Keyless() && c.keylessDup != nil {
		if sib := n.siblingsOf(); sib != nil {
			if bySchema := c.keylessDup[sib]; bySchema != nil {
				delete(bySchema, n.schema)
			}
		}
	}
	if sib := n.siblingsOf(); sib != nil && c.tableRuns != nil {
		if idx := c.tableRuns[sib]; idx != nil {
			key := dupNativeKey(n)
			if i := t.indexFromEnd(idx[key], n); i >= 0 {
				t.work.Add(int64(len(idx[key]) - 1 - i))
				idx[key] = slices.Delete(idx[key], i, i+1)
			}
		}
	}
	if idx := c.runs[n.schema]; idx != nil {
		if k, ok := indexedKey(n, &t.work); ok && idx[k] != nil {
			if i := t.indexFromEnd(idx[k].nodes, n); i >= 0 {
				t.work.Add(int64(idx[k].deleteAt(i)))
			}
		}
	}
	t.created(c, n)
}

// runSlot is where n goes in run, its equal instances in sibling order: before the nearest run
// member after n, after the nearest one before it. It walks out from n both ways at once, so the
// cost is at most the distance to the nearer end of l, which the insertion of n shifted anyway.
func (t *Tree) runSlot(l siblingSpan, run []*Node, n *Node, k idxKey) int {
	at := l.indexFromEnd(&t.work, n)
	member := func(c *Node) int {
		if c.schema != n.schema {
			return -1
		}
		if ck, ok := indexedKey(c, &t.work); !ok || ck != k {
			return -1
		}
		return t.indexFromEnd(run, c) // the nearer run end: a prepend or append finds it at once
	}
	for f, b := at+1, at-1; ; f, b = f+1, b-1 {
		t.work.Add(1)
		if f >= l.Len() {
			return len(run) // no equal live instance after n
		}
		if b < 0 {
			return 0 // none before n
		}
		if i := member(l.At(f)); i >= 0 {
			return i
		}
		if i := member(l.At(b)); i >= 0 {
			return i + 1
		}
	}
}

// dupInstNext is lyd_dup_inst_next: equal target instances are matched one by one; once all are
// used, a key-less list, state leaf-list or user-ordered instance matches nothing more, others
// keep matching the first one. The equal instances (lyd_find_sibling_dup_inst_set) come from
// inst's equal-hash run: hash registration order with a children table, sibling order without one.
func (t *Tree) dupInstNext(inst *Node, c *dupCache) (*Node, error) {
	if inst == nil {
		return nil, nil
	}
	d := c.inst[inst]
	if d == nil {
		d = &dupInst{set: []*Node{inst}}
		full := isDupInstList(inst.schema)
		sib := inst.siblingsOf()
		run, err := t.run(c, inst)
		if err != nil {
			return nil, err
		}
		for _, n := range run.All() {
			t.work.Add(1)
			// The level indexes outlive removals (ApplyDiff delete, merge); only the live
			// siblings are candidates, as libyang looks them up anew.
			if n != inst && n.siblingsOf() == sib {
				first, second := inst, n
				if sib.ht != nil {
					// After lyd_find_sibling_first supplies the first match, the
					// children-table branch of lyd_find_sibling_dup_inst_set compares
					// each later hash-table candidate against the target. Its linear
					// branch compares the target against each sibling.
					first, second = n, inst
				}
				equal, err := compareSingleErr(t, first, second, full)
				if err != nil {
					return nil, err
				}
				if equal {
					d.set = append(d.set, n)
				}
			}
		}
		if c.inst == nil {
			c.inst = map[*Node]*dupInst{}
		}
		c.inst[inst] = d
	}
	if d.used == len(d.set) {
		if isDupInstList(inst.schema) || inst.schema != nil && inst.schema.UserOrdered {
			return nil, nil
		}
		return inst, nil
	}
	d.used++
	return d.set[d.used-1], nil
}

// mergeLevel is one sibling level of Merge: its duplicate cache and the new instances of a
// system-ordered run waiting to be spliced in (src runs are sorted, so linking them one by one
// would shift the destination run once per instance).
type mergeLevel struct {
	work   *anyWork
	parent *Node
	cache  *dupCache
	pend   []*Node // ascending, strictly: none matches another
	first  []bool  // pend[i] matched no instance (lyd_merge_sibling_r's first_inst)
}

// mergeSibling is lyd_merge_sibling_r without options: src merged into the children of lv.parent
// (nil: the top level of t).
func (t *Tree) mergeSibling(lv *mergeLevel, src *Node) error {
	if t.work.hashErr != nil {
		return t.work.hashErr
	}
	if lv.work == nil {
		lv.work = newAnyWork(context.Background(), Budget{})
	}
	if len(lv.pend) > 0 {
		last := lv.pend[len(lv.pend)-1]
		c := 1
		if src.schema == last.schema {
			t.work.Add(1)
			c = t.compareSorted(last, src)
		}
		pendingEqual := false
		if c == 0 && !isDupInstList(src.schema) {
			var err error
			if pendingEqual, err = compareSingleErr(t, last, src, false); err != nil {
				return err
			}
		}
		switch {
		case pendingEqual:
			// equal in sort order is not equal (identityrefs sort by local name only): only an
			// equal instance matches, an unequal one flushes and is looked up as usual
			// a source with duplicate instances (parsed only): the equal one matches the pending
			// copy, the first instance of its value (none was in the destination), as it would
			// once linked; the batch goes on
			before, tracked, err := lv.cache.keylessDigest(last, &t.work)
			if err != nil {
				return err
			}
			if err = t.mergeMatch(last, src, lv.work); err != nil {
				return err
			}
			return lv.cache.invalidateChangedKeyless(last, before, tracked, &t.work)
		case c >= 0:
			if err := t.flushMerge(lv); err != nil {
				return err
			}
		}
	}
	parent, cache := lv.parent, lv.cache
	sib := t.childrenOf(parent)
	var match *Node
	switch {
	case src.schema == nil:
		match = opaqNext(sib, src.Name())
	case src.schema.Kind == schema.List && src.schema.Keyless() && !sib.mixedContexts():
		var err error
		if match, err = t.keylessFirst(cache, sib, src); err != nil {
			return err
		}
	case src.schema.Kind == schema.List || src.schema.Kind == schema.LeafList:
		var err error
		if match, err = t.findFirstChecked(sib, src); err != nil {
			return err
		}
	default:
		match = t.schemaLookup(sib, src.schema)
	}
	firstInst := match == nil
	var err error
	match, err = t.dupInstNext(match, cache)
	if err != nil {
		return err
	}
	if match == nil {
		// lyd_merge_sibling_r duplicates an unmatched source node without a target
		// context, then inserts it below the matched destination parent. A child
		// introduced in another context keeps its own compiled schema.
		d, err := t.dupWork(src, nil, false, insertDefault, dupRecursive|dupWithFlags, ctxOf(src), lv.work)
		if t.work.hashErr != nil {
			return t.work.hashErr
		}
		if err != nil {
			return err
		}
		for e := range d.All() {
			e.flags |= FlagNew // required for validation
		}
		if sortedSupported(d) && t.sortedRunBatchable(sib, d) {
			// the run is sorted: lyd_insert_node puts d after its equal values, as splice does
			lv.pend, lv.first = append(lv.pend, d), append(lv.first, firstInst)
			return nil
		}
		if err := t.insert(parent, d, insertDefault); err != nil {
			return err
		}
		return t.mergeAdded(cache, d, firstInst)
	}
	before, tracked, err := cache.keylessDigest(match, &t.work)
	if err != nil {
		return err
	}
	if err = t.mergeMatch(match, src, lv.work); err != nil {
		return err
	}
	return cache.invalidateChangedKeyless(match, before, tracked, &t.work)
}

// mergeCtx is LYD_CTX(match), the context the merged opaque value is stored in.
func (t *Tree) mergeCtx(match *Node) *schema.Set {
	if set := ctxOf(match); set != nil {
		return set
	}
	return t.set
}

// mergeMatch is lyd_merge_sibling_r for src matched by match: a leaf takes src's value, children
// are merged recursively.
func (t *Tree) mergeMatch(match, src *Node, w *anyWork) error {
	switch {
	case match.schema == nil:
		equal, err := compareSingleErr(t, src, match, false)
		if err != nil {
			return err
		}
		if !equal {
			o := *src.opaq
			if err := match.touch(); err != nil {
				return err
			}
			match.opaq.Value, match.opaq.Hints = o.Value, o.Hints
			match.opaq.Format, match.opaq.Prefixes = o.Format, dupPrefixes(o.Prefixes, t.mergeCtx(match))
		}
	case isAny(match.schema):
		equal, err := compareSingleErr(t, src, match, false)
		if err != nil {
			return err
		}
		if !equal {
			var child *Node
			var text *string
			var hints types.Hints
			if src.any != nil {
				child, text, hints = src.any.children.first(), src.any.text, src.any.hints
			}
			if err := match.touch(); err != nil {
				return err
			}
			if err := match.copyAnyValue(w, child, text, hints); err != nil {
				return err
			}
			match.flags = src.flags | FlagNew
		}
	case match.schema.Kind == schema.Leaf && src.flags&FlagDefault == 0:
		if !sameSet(ctxOf(match), ctxOf(src)) {
			// lyd_change_term_val on a value of another context: an equal one changes only the
			// flags, a different one may fail in the duplicate callback (LY_ENOTFOUND, no log)
			equal, dupOK, undefined, err := types.CrossContextChange(&w.defs, match.schema.Type, src.schema.Type,
				match.value, src.value, t.work.chargeHash)
			base := match.schema.Type.Base
			switch {
			case err != nil:
				return err
			case undefined == types.CrossDefinition:
				return &opError{"LY_EINVAL", fmt.Sprintf("Invalid arguments - the type of \"%s\" differs between the two "+
					"schema contexts, merging its value is not supported (D-0133) (lyd_merge()).", match.schema.Name)}
			case undefined == types.CrossPredicate:
				// ly_path_dup_predicates leaves NULL predicate values (lydict_dup of a foreign
				// string, LY_ENOTFOUND ignored) and libyang 5.8.6 crashes printing them: refused
				return &opError{"LY_EINVAL", fmt.Sprintf("Invalid arguments - merging the path predicates of "+
					"\"%s\" from another schema context is undefined in libyang, not supported (D-0133) "+
					"(lyd_merge()).", match.schema.Name)}
			case !equal && !dupOK && (base == schema.Union || base == schema.Leafref):
				// the failed duplicate leaves the leafref/union storage half freed and libyang 5.8.6
				// crashes (SIGSEGV) when it frees or prints the node: undefined, refused (D-0133)
				return &opError{"LY_EINVAL", fmt.Sprintf("Invalid arguments - merging the %s value of \"%s\" from "+
					"another schema context is undefined in libyang, not supported (D-0133) (lyd_merge()).",
					base, match.schema.Name)}
			case !equal && !dupOK:
				return rcError("LY_ENOTFOUND")
			}
			t.changeTermValCompared(match, src.value.RebindContext(ctxOf(match)), false, equal)
		} else {
			t.changeTermVal(match, src.value, false)
		}
		if t.work.hashErr != nil {
			return t.work.hashErr
		}
	}
	child := &mergeLevel{parent: match, cache: &dupCache{}, work: w}
	for _, c := range src.kids.nodes() {
		if !c.isKey() { // lyd_child_no_keys
			if err := t.mergeSibling(child, c); err != nil {
				if flushErr := t.flushMerge(child); flushErr != nil {
					return flushErr
				}
				return err
			}
		}
	}
	return t.flushMerge(child)
}

// mergeAdded records the new instance d in the level's duplicate cache.
func (t *Tree) mergeAdded(cache *dupCache, d *Node, firstInst bool) error {
	if d.schema != nil {
		cache.added(d, &t.work)
		if t.work.hashErr != nil {
			return t.work.hashErr
		}
	}
	if firstInst {
		if _, err := t.dupInstNext(d, cache); err != nil { // do not match this instance next time
			return err
		}
	}
	return nil
}

// flushMerge links the pending instances of lv.
func (t *Tree) flushMerge(lv *mergeLevel) error {
	if len(lv.pend) == 0 {
		return nil
	}
	if len(lv.pend) == 1 {
		// one instance: the ordinary insertion (append fast path), not a pass over the level
		if err := t.insert(lv.parent, lv.pend[0], insertDefault); err != nil {
			return err
		}
		if err := t.mergeAdded(lv.cache, lv.pend[0], lv.first[0]); err != nil {
			return err
		}
		lv.pend, lv.first = lv.pend[:0], lv.first[:0]
		return nil
	}
	sib := t.childrenOf(lv.parent)
	if err := t.preflightSplice(lv.parent, sib, lv.pend); err != nil {
		return err
	}
	// lyd_merge_sibling_r inserts each pending copy with lyd_insert_node: replay those RB
	// insertions on a copy of the run's topology, then publish the run once. The copy is
	// detached until everything is charged; the publication already walks the sibling slice.
	lo := t.sortedLeaderIndex(sib, lv.pend[0]) // lyd_insert_node's leader (lyd_find_sibling_schema)
	if lo < 0 {
		return &opError{"LY_EINT", "Missing retained red-black topology for a system-ordered run."}
	}
	hi := t.retainedRunEnd(sib, lo)
	group := sib.rbGroups[sib.At(lo)]
	current := sib.rbOrders[group]
	if current == nil {
		return &opError{"LY_EINT", "Missing retained red-black topology for a system-ordered run."}
	}
	if err := t.work.chargeHash(uint64(len(current.nodes)) + 1); err != nil {
		return err
	}
	order := cloneDirtyOrder(current)
	if err := t.rbReplay(order, lv.pend); err != nil {
		return err
	}
	if err := t.rbPublish(lv.parent, sib, lo, hi, group, order, lv.pend); err != nil {
		return err
	}
	for i, d := range lv.pend {
		if err := t.mergeAdded(lv.cache, d, lv.first[i]); err != nil {
			return err
		}
	}
	lv.pend, lv.first = lv.pend[:0], lv.first[:0]
	return nil
}

// opaqNext is lyd_find_sibling_opaq_next from the first opaque sibling: the first opaque node
// named name.
func opaqNext(s *siblings, name string) *Node {
	return s.opaqueFirst(name)
}

// dup is lyd_dup_single with LYD_DUP_RECURSIVE | LYD_DUP_WITH_FLAGS: an unlinked copy of n and
// its subtree with the flags and metadata; children keep their order (LYD_INSERT_NODE_LAST).
func (t *Tree) dup(n *Node) (*Node, error) {
	return t.dupWork(n, nil, false, insertDefault, dupRecursive|dupWithFlags, ctxOf(n), newAnyWork(context.Background(), Budget{}))
}
