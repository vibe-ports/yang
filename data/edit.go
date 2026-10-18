// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_new.c (lyd_new_path and its helpers, lyd_create_list,
// lyd_change_term), src/tree_data.c (lyd_find_path, lyd_merge, lyd_dup_r), src/path.c
// (ly_path_eval_partial) and src/tree_data_common.c (lyd_dup_inst_next,
// lyd_find_sibling_dup_inst_set) (BSD-3-Clause, © CESNET).

package data

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// NewPathOptions are the lyd_new_path options of the port (LYD_NEW_PATH_*).
type NewPathOptions struct {
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
	var oe *opError
	if errors.As(err, &oe) {
		err = l.logErr(oe.Err, "%s", oe.Msg)
	}
	if errors.Is(err, errLogged) {
		return l.result()
	}
	var rc rcError
	if errors.As(err, &rc) {
		return &ValidationError{Diags: l.diags, rc: string(rc)}
	}
	return err
}

// NewPath is lyd_new_path on the top level of t: it creates the nodes of the absolute JSON path
// that do not exist yet, value being the value of a created leaf or leaf-list (JSON format), and
// returns the first created node (nil when nothing was created or changed). An existing term is
// an error unless it is a default node or o.Update is set; then its value is changed
// (lyd_change_term: its Default flag and that of its NP-container ancestors cleared, New set
// when the value changed). The keys of a created list instance come from the path's predicate.
// Errors are a *ValidationError with libyang's diagnostics.
func (t *Tree) NewPath(path, value string, o NewPathOptions) (*Node, error) {
	defer t.work.beginHashWork()()
	lg := &logger{set: t.set}
	// The ordinary path preflight handles small edits without a journal. A caller-specified
	// boundary or large input can still fail after several insertions, so journal those.
	var journal *hashTransaction
	if t.work.hashLimit != 0 || t.work.hashTrace != nil || len(value) >= 1<<20 || len(path) >= 1<<20 {
		journal = beginHashTransaction(t)
	}
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
	lg := &logger{set: t.set}
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
	if owner := n.treeOf(); owner != nil {
		defer owner.work.beginHashWork()()
	} else if n != nil {
		root := n
		for root.parent != nil {
			root = root.parent
		}
		bindSubtreeWork(root, &workCounter{})
	}
	return (&logger{}).done(freeTree(n))
}

// Merge is lyd_merge_siblings without options: the top-level subtrees of src are merged into t.
// Nodes missing in t are copied with their flags and metadata and marked New; an existing leaf
// takes src's value unless src's leaf is a default node; src is not changed. A nil src merges
// nothing. Unequal anydata/anyxml payloads replace the whole destination payload,
// copying the source flags plus New; equal payloads leave the destination unchanged.
func (t *Tree) Merge(src *Tree) (result error) {
	defer t.work.beginHashWork()()
	lg := &logger{set: t.set}
	if src == nil {
		return nil
	}
	if src.set != t.set {
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
			t.flushMerge(lv)
			if t.work.hashErr != nil {
				return t.work.hashErr
			}
			return lg.done(err)
		}
	}
	t.flushMerge(lv)
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
	p, msg, at := types.CompilePath(t.set, ctxNode, path, false, false)
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
		sib = &ctx.kids
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
			n = t.findFirstWork(sib, target, &probes.work)
		default:
			target, d, key, err := probes.lookupList(seg) // ly_path_eval_partial: lyd_create_list, store-only
			if err != nil {
				return nil, u, err
			}
			if d != nil {
				return nil, u, lg.storeErr(nil, key, d) // LY_CHECK_RET: logged at the key
			}
			n = t.findFirstWork(sib, target, &probes.work)
		}
		if probes.work.hashErr != nil {
			return nil, u, probes.work.hashErr
		}
		if n == nil {
			return prev, u, nil
		}
		prev, sib = n, &n.kids
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
		var run []*Node
		if opChild(sn) || s.schemaUnordered {
			run = t.opRun(s, sn)
		} else if i := t.schemaIndex(s, sn); i >= 0 {
			run = s.list[i:]
		}
		for _, n := range run {
			if n.schema != sn || !yield(n) {
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
			types.ModuleNames{Set: t.set}, pr.Key)
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

// newPath is lyd_new_path_ with lyd_new_path_create (no parent node, absolute path; the top
// level of t is the parent's siblings).
func (t *Tree) newPath(lg *logger, path, value string, o NewPathOptions) (*Node, error) {
	if path == "" || path[0] != '/' {
		return nil, argErr("(path[0] == '/') || parent", "lyd_new_path")
	}
	var ctxNode *schema.Node // lyd_node_schema(parent): the first top-level node
	if len(t.top.list) > 0 {
		ctxNode = t.top.list[0].schema
	}
	p, msg, at := types.CompilePath(t.set, ctxNode, path, false, true)
	if msg != "" {
		return nil, lg.item(nil, at, false, "LY_EVALID", ly.XPath, "", msg)
	}
	search, err := t.checkFindPath(lg, p, path, value)
	if err != nil {
		return nil, err
	}
	var node *Node
	idx := 0
	if t.top.len() > 0 {
		var n *Node
		if n, idx, err = t.evalPartial(lg, search); err != nil {
			return nil, err
		}
		if idx == len(search) && len(search) == len(p) {
			// the node exists: update it or, a default node, set it
			if !o.Update && n.flags&FlagDefault == 0 {
				return nil, lg.val(n, "", ly.Reference, "Path \"%s\" already exists.", path)
			} else if o.Update && n.schema.IsKey() {
				return nil, nil // the key value is in the predicate, it cannot change
			}
			return t.newPathUpdate(lg, n, value)
		}
		node = n // nil when nothing matched: create from the top level
	}
	if idx < len(p) && isDupInstList(p[idx].Node) && len(p[idx].Preds) > 0 && p[idx].Preds[0].Kind == types.PredPosition {
		if err := t.checkPosition(lg, node, p[idx]); err != nil {
			return nil, err
		}
	}
	if err := t.preflightNewPathHash(p[idx:], node, value); err != nil {
		return nil, err
	}
	if idx < len(p) {
		if err := t.preflightInsertPosition(t.childrenOf(node), &Node{schema: p[idx].Node}, insertDefault); err != nil {
			return nil, err
		}
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
				unlink(first)
			}
			return nil, err
		}
		if err := t.insert(parent, node, insertDefault); err != nil {
			if created {
				unlink(first)
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
// before a newly created ancestor can enter an indexed sibling set.
func (t *Tree) preflightNewPathHash(segments types.Path, parent *Node, value string) error {
	if len(segments) == 0 {
		return nil
	}
	indexed := parent != nil && parent.schema != nil &&
		(parent.kids.ht != nil || len(parent.kids.list)+1 >= htMinItems)
	var amount uint64
	if indexed && parent.kids.ht == nil {
		for _, old := range parent.kids.list {
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
func (t *Tree) checkPosition(lg *logger, parent *Node, seg types.PathSegment) error {
	sib := &t.top
	if parent != nil {
		sib = &parent.kids
	}
	count := uint64(0)
	for range t.instances(sib, seg.Node) {
		count++
	}
	pos := seg.Preds[0].Position
	switch {
	case count+1 >= pos:
		return nil
	case count == 0:
		return lg.val(nil, "", ly.Reference, "Cannot create \"%s\" on position %d, no instances exist.", seg.Node.Name, pos)
	case count > 1:
		return lg.val(nil, "", ly.Reference, "Cannot create \"%s\" on position %d, only %d instances exist.", seg.Node.Name, pos, count)
	}
	return lg.val(nil, "", ly.Reference, "Cannot create \"%s\" on position %d, only %d instance exists.", seg.Node.Name, pos, count)
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
			v, d := types.Store(sn.Type, value, types.FormatJSON, types.HintData, types.ModuleNames{Set: t.set}, sn)
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
	v, d := types.Store(sn.Type, value, types.FormatJSON, types.HintData, types.ModuleNames{Set: t.set}, sn)
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
	v, d := types.Store(n.schema.Type, value, types.FormatJSON, types.HintData, types.ModuleNames{Set: t.set}, n.schema)
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
	if !types.Equal(n.value, v) {
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
		n.touch()
		n.flags |= FlagNew
	}
	switch {
	case n.flags&FlagDefault != 0 && !dflt:
		n.touch()
		n.flags &^= FlagDefault
		npContDfltDel(n.parent)
		dfltChange = true
	case n.flags&FlagDefault == 0 && dflt:
		n.touch()
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
		n.touch()
		n.value = v
		reindexComparedParent(n)
		return
	}
	sib, parent := target.siblingsOf(), target.parent
	if sib == nil {
		n.touch()
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
		entry := sib.opIdx[target.schema]
		if entry == nil || len(entry.list) != 1 || entry.list[0] != target {
			at, err := t.originIndex(sib, sib.list, target)
			if err != nil {
				t.work.hashErr = err
				return
			}
			left, right := sib.opNeighbours(at)
			// In libyang the first sibling's prev points to the last sibling.
			if at == 0 && len(sib.opq) == 0 && len(sib.list) > 1 {
				left = sib.list[len(sib.list)-1].schema
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
			for _, key := range target.kids.list {
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
		restore, err := t.linkOrigin(target)
		if err != nil {
			return
		}
		if err := t.work.checkChangedIndex(target, n, &v, hashAmount); err != nil {
			t.work.hashErr = err
			return
		}
		unlinkRetainingHistory(target)
		n.touch()
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
				s.key.touch()
				s.key.keySlot = s.old
			}
			restore()
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
			at = t.indexFromEnd(sib.list, target)
			sib.leaderUnlink(target, at, false)
		}
	}
	sib.hashRemove(target)
	n.touch()
	n.value = v
	refreshInstanceHash(&t.work, target)
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

// dupCache is the duplicate instance cache of one sibling level (lyd_dup_inst_get's hash table).
// A level without a children table gets a document-order index built once per schema node; a
// level with one reads the live lyd_hash bucket in registration order.
type dupCache struct {
	inst map[*Node]*dupInst
	runs map[*schema.Node]map[idxKey][]*Node
}

// run returns the instances of the siblings of inst that hash like it (opaque nodes: the opaque
// siblings). A key-less list hashes by schema node only, so its run is all its instances,
// compared whole (libyang parity).
func (t *Tree) run(c *dupCache, inst *Node) []*Node {
	sib := sibsOf(inst) // a detached node: its singleton sibling set
	if inst.schema == nil {
		return sib.opq
	}
	k, ok := indexedKey(inst, &t.work)
	if !ok {
		return nil
	}
	if sib.ht != nil {
		out := make([]*Node, 0)
		for n := range sib.bucketNodes(k) {
			out = append(out, n)
		}
		return out
	}
	if c.runs == nil {
		c.runs = map[*schema.Node]map[idxKey][]*Node{}
	}
	idx := c.runs[inst.schema]
	if idx == nil {
		idx = map[idxKey][]*Node{}
		all := t.instances(sib, inst.schema)
		if opChild(inst.schema) || sib.schemaUnordered {
			// Merge must find every instance, including runs split by an anchor in an
			// operation or an unordered copy.
			all = slices.Values(sib.opInst(inst.schema))
		}
		for n := range all {
			t.work.Add(1)
			if nk, ok := indexedKey(n, &t.work); ok {
				idx[nk] = append(idx[nk], n)
			}
		}
		c.runs[inst.schema] = idx
	}
	return idx[k]
}

// added records n, just inserted at the level of c, in the level's index.
func (c *dupCache) added(n *Node, work *workCounter) {
	if idx := c.runs[n.schema]; idx != nil {
		if k, ok := indexedKey(n, work); ok {
			idx[k] = append(idx[k], n)
		}
	}
}

// dupInstNext is lyd_dup_inst_next: equal target instances are matched one by one; once all are
// used, a key-less list, state leaf-list or user-ordered instance matches nothing more, others
// keep matching the first one. The equal instances (lyd_find_sibling_dup_inst_set) come from
// inst's equal-hash run: hash registration order with a children table, sibling order without one.
func (t *Tree) dupInstNext(inst *Node, c *dupCache) *Node {
	if inst == nil {
		return nil
	}
	d := c.inst[inst]
	if d == nil {
		d = &dupInst{set: []*Node{inst}}
		full := isDupInstList(inst.schema)
		for _, n := range t.run(c, inst) {
			t.work.Add(1)
			if n != inst && compareSingle(t, n, inst, full) {
				d.set = append(d.set, n)
			}
		}
		if c.inst == nil {
			c.inst = map[*Node]*dupInst{}
		}
		c.inst[inst] = d
	}
	if d.used == len(d.set) {
		if isDupInstList(inst.schema) || inst.schema != nil && inst.schema.UserOrdered {
			return nil
		}
		return inst
	}
	d.used++
	return d.set[d.used-1]
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
		switch {
		case c == 0 && !isDupInstList(src.schema) && compareSingle(t, last, src, false):
			// equal in sort order is not equal (identityrefs sort by local name only): only an
			// equal instance matches, an unequal one flushes and is looked up as usual
			// a source with duplicate instances (parsed only): the equal one matches the pending
			// copy, the first instance of its value (none was in the destination), as it would
			// once linked; the batch goes on
			return t.mergeMatch(last, src, lv.work)
		case c >= 0:
			t.flushMerge(lv)
			if t.work.hashErr != nil {
				return t.work.hashErr
			}
		}
	}
	parent, cache := lv.parent, lv.cache
	sib := t.childrenOf(parent)
	var match *Node
	switch {
	case src.schema == nil:
		match = opaqNext(sib, src.Name())
	case src.schema.Kind == schema.List || src.schema.Kind == schema.LeafList:
		match = t.findFirst(sib, src)
	default:
		match = t.schemaLookup(sib, src.schema)
	}
	firstInst := match == nil
	match = t.dupInstNext(match, cache)
	if match == nil {
		d, err := t.dupWork(src, nil, false, insertDefault, dupRecursive|dupWithFlags, lv.work)
		if t.work.hashErr != nil {
			return t.work.hashErr
		}
		if err != nil {
			return err
		}
		for e := range d.All() {
			e.flags |= FlagNew // required for validation
		}
		if sortedSupported(d) && sib.hasRB(d.schema) && !sib.hasUnsorted(d.schema) {
			// the run is sorted: lyd_insert_node puts d after its equal values, as splice does
			lv.pend, lv.first = append(lv.pend, d), append(lv.first, firstInst)
			return nil
		}
		if err := t.insert(parent, d, insertDefault); err != nil {
			return err
		}
		t.mergeAdded(cache, d, firstInst)
		return nil
	}
	return t.mergeMatch(match, src, lv.work)
}

// mergeMatch is lyd_merge_sibling_r for src matched by match: a leaf takes src's value, children
// are merged recursively.
func (t *Tree) mergeMatch(match, src *Node, w *anyWork) error {
	switch {
	case match.schema == nil:
		if !compareSingle(t, src, match, false) {
			o := *src.opaq
			match.touch()
			match.opaq.Value, match.opaq.Hints = o.Value, o.Hints
			match.opaq.Format, match.opaq.Prefixes = o.Format, dupPrefixes(o.Prefixes, t.set)
		}
	case isAny(match.schema):
		if !compareSingle(t, src, match, false) {
			var child *Node
			var text *string
			var hints types.Hints
			if src.any != nil {
				child, text, hints = src.any.children.first(), src.any.text, src.any.hints
			}
			match.touch()
			if err := match.copyAnyValue(w, child, text, hints); err != nil {
				return err
			}
			match.flags = src.flags | FlagNew
		}
	case match.schema.Kind == schema.Leaf && src.flags&FlagDefault == 0:
		t.changeTermVal(match, src.value, false)
		if t.work.hashErr != nil {
			return t.work.hashErr
		}
	}
	child := &mergeLevel{parent: match, cache: &dupCache{}, work: w}
	for _, c := range src.kids.nodes() {
		if !c.isKey() { // lyd_child_no_keys
			if err := t.mergeSibling(child, c); err != nil {
				t.flushMerge(child)
				return err
			}
		}
	}
	t.flushMerge(child)
	return nil
}

// mergeAdded records the new instance d in the level's duplicate cache.
func (t *Tree) mergeAdded(cache *dupCache, d *Node, firstInst bool) {
	if d.schema != nil {
		cache.added(d, &t.work)
	}
	if firstInst {
		t.dupInstNext(d, cache) // do not match this instance next time
	}
}

// flushMerge links the pending instances of lv.
func (t *Tree) flushMerge(lv *mergeLevel) {
	if len(lv.pend) == 0 {
		return
	}
	if len(lv.pend) == 1 {
		// one instance: the ordinary insertion (append fast path), not a pass over the level
		if err := t.insert(lv.parent, lv.pend[0], insertDefault); err != nil {
			return
		}
		t.mergeAdded(lv.cache, lv.pend[0], lv.first[0])
		lv.pend, lv.first = lv.pend[:0], lv.first[:0]
		return
	}
	sib := t.childrenOf(lv.parent)
	if err := t.preflightSplice(lv.parent, sib, lv.pend); err != nil {
		return
	}
	// Splice needs positions; it already walks/copies the sibling slice once.
	lo := t.schemaIndex(sib, lv.pend[0].schema)
	hi := lo
	for hi < len(sib.list) && sib.list[hi].schema == lv.pend[0].schema {
		hi++
	}
	for _, d := range lv.pend {
		d.inRB = true
		d.rbGroup = sib.rbGroups[sib.runLeader(d.schema)]
	}
	if err := t.splice(lv.parent, sib, lo, hi, lv.pend, false); err != nil {
		return
	}
	for i, d := range lv.pend {
		t.mergeAdded(lv.cache, d, lv.first[i])
	}
	lv.pend, lv.first = lv.pend[:0], lv.first[:0]
}

// opaqNext is lyd_find_sibling_opaq_next from the first opaque sibling: the first opaque node
// named name.
func opaqNext(s *siblings, name string) *Node {
	for _, n := range s.opq {
		if n.opaq.Name == name {
			return n
		}
	}
	return nil
}

// dup is lyd_dup_single with LYD_DUP_RECURSIVE | LYD_DUP_WITH_FLAGS: an unlinked copy of n and
// its subtree with the flags and metadata; children keep their order (LYD_INSERT_NODE_LAST).
func (t *Tree) dup(n *Node) (*Node, error) {
	return t.dupR(n, nil, false, insertDefault, dupRecursive|dupWithFlags)
}
