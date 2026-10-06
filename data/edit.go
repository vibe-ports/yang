// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/tree_data_new.c (lyd_new_path and its helpers, lyd_create_list,
// lyd_change_term), src/tree_data.c (lyd_find_path, lyd_merge, lyd_dup_r), src/path.c
// (ly_path_eval_partial) and src/tree_data_common.c (lyd_dup_inst_next) (BSD-3-Clause, © CESNET).

package data

import (
	"errors"
	"fmt"

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
	if errors.Is(err, errLogged) {
		return l.result()
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
	lg := &logger{set: t.set}
	n, err := t.newPath(lg, path, value, o)
	return n, lg.done(err)
}

// Find is lyd_find_path from the top level of t: the node an absolute JSON path names (list and
// leaf-list instances need their predicates), nil when it does not exist. A malformed path is a
// *ValidationError.
func (t *Tree) Find(path string) (*Node, error) {
	lg := &logger{set: t.set}
	n, err := t.findPath(lg, path)
	if errors.Is(err, errNotFound) || errors.Is(err, errPartial) {
		return nil, nil
	}
	return n, lg.done(err)
}

// Remove is lyd_free_tree: n and its subtree leave the tree. A list key is refused (an
// LY_EINVAL *ValidationError) and nothing is removed.
func (n *Node) Remove() error {
	lg := &logger{}
	if err := freeTree(n); err != nil {
		var oe *opError
		if errors.As(err, &oe) {
			return lg.done(lg.logErr(oe.Err, "%s", oe.Msg))
		}
		return err
	}
	return nil
}

// Merge is lyd_merge_siblings without options: the top-level subtrees of src are merged into t.
// Nodes missing in t are copied with their flags and metadata and marked New; an existing leaf
// takes src's value unless src's leaf is a default node; src is not changed.
func (t *Tree) Merge(src *Tree) error {
	lg := &logger{set: t.set}
	if src.set != t.set {
		return lg.done(lg.logErr("LY_EINVAL", "Different contexts mixed in a \"lyd_merge\" function call."))
	}
	dupInst := map[*Node]*dupInst{}
	for _, n := range src.top.nodes() {
		t.mergeSibling(nil, n, dupInst)
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

// findPath is lyd_find_path from the first top-level node: errNotFound / errPartial when the
// path does not match fully (the tree being empty is not found, as the oracle's sequence does).
func (t *Tree) findPath(lg *logger, path string) (*Node, error) {
	if len(t.top.list) == 0 {
		return nil, errNotFound
	}
	if path == "" || path[0] != '/' {
		return nil, fmt.Errorf("data: %q is not an absolute path", path)
	}
	p, msg, at := types.CompilePath(t.set, t.top.list[0].schema, path, false, false)
	if msg != "" {
		return nil, lg.item(nil, at, false, "LY_EVALID", ly.XPath, "", msg)
	}
	n, idx := t.evalPartial(p)
	switch {
	case idx == len(p):
		return n, nil
	case n != nil:
		return nil, errPartial
	}
	return nil, errNotFound
}

// evalPartial is ly_path_eval_partial from the top level: the last node matched and the number
// of path segments matched (len(p) for a full match).
func (t *Tree) evalPartial(p types.Path) (*Node, int) {
	sib := &t.top
	var prev *Node
	for u, seg := range p {
		var n *Node
		switch {
		case len(seg.Preds) == 0:
			n = t.findSchema(sib, seg.Node) // lyd_find_sibling_val without a value
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
			n = t.findFirst(sib, newTerm(seg.Node, seg.Preds[0].Value))
		default:
			n = t.findFirst(sib, t.createList(seg))
		}
		if n == nil {
			return prev, u
		}
		prev, sib = n, &n.kids
	}
	return prev, len(p)
}

// instances yields the instances of sn in s (LYD_LIST_FOR_INST).
func (t *Tree) instances(s *siblings, sn *schema.Node) func(func(*Node) bool) {
	return func(yield func(*Node) bool) {
		i := t.schemaIndex(s, sn)
		if i < 0 {
			return
		}
		for _, n := range s.list[i:] {
			if n.schema != sn || !yield(n) {
				return
			}
		}
	}
}

// createList is lyd_create_list: an unlinked list instance with the predicate's keys.
func (t *Tree) createList(seg types.PathSegment) *Node {
	l := newInner(seg.Node)
	l.flags = FlagNew
	for _, pr := range seg.Preds {
		k := newTerm(pr.Key, pr.Value)
		k.flags = FlagNew
		t.insert(l, k, insertDefault)
	}
	return l
}

// newPath is lyd_new_path_ with lyd_new_path_create (no parent node, absolute path; the top
// level of t is the parent's siblings).
func (t *Tree) newPath(lg *logger, path, value string, o NewPathOptions) (*Node, error) {
	if path == "" || path[0] != '/' {
		return nil, fmt.Errorf("data: %q is not an absolute path", path)
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
		n, idx = t.evalPartial(search)
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
	var first, last *Node
	created := false
	for ; idx < len(p); idx++ {
		seg, parent := p[idx], node
		var err error
		switch seg.Node.Kind {
		case schema.List:
			if isDupInstList(seg.Node) {
				node = newInner(seg.Node)
				node.flags = FlagNew
			} else {
				node = t.createList(seg)
			}
		case schema.Container:
			node = newInner(seg.Node)
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
				node = t.findSchema(&parent.kids, seg.Node) // created with its list
				if first == nil {
					first = node
				}
				last = node
				continue
			}
			node, err = t.createTerm(lg, last, seg.Node, value)
		default: // rpc, action, notification, anydata: operations are M4, anydata M5
			err = fmt.Errorf("data: creating %s %q is not supported: %w", nodetypeStr(seg.Node.Kind), seg.Node.Name, errors.ErrUnsupported)
		}
		if err != nil {
			if created {
				unlink(first)
			}
			return nil, err
		}
		t.insert(parent, node, insertDefault)
		if first == nil {
			first, created = node, true
		}
		last = node
	}
	return first, nil
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
// path to search for existing nodes.
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
				return nil, lg.val(nil, "", ly.XPath, "Invalid predicate for state %s \"%s\" in path \"%s\".",
					nodetypeStr(sn.Kind), sn.Name, path)
			}
		case sn.Kind == schema.List && (len(preds) == 0 || preds[0].Kind != types.PredKey):
			lg.locSet(sn)
			defer lg.locBack(1)
			return nil, lg.val(nil, "", ly.XPath, "Predicate missing for %s \"%s\" in path \"%s\".",
				nodetypeStr(sn.Kind), sn.Name, path)
		case sn.Kind == schema.LeafList && (len(preds) == 0 || preds[0].Kind != types.PredLeafList):
			v, d := types.Store(sn.Type, value, types.FormatJSON, types.HintData, types.ModuleNames{Set: t.set}, sn)
			if d != nil {
				return nil, lg.item(nil, sn, false, "LY_EVALID", codeOf(d.Code), d.AppTag, d.Msg)
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
		return nil, lg.item(lnode, sn, false, "LY_EVALID", codeOf(d.Code), d.AppTag, d.Msg)
	}
	n := newTerm(sn, v)
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
		return nil, lg.item(n, n.schema, false, "LY_EVALID", codeOf(d.Code), d.AppTag, d.Msg)
	}
	if valChange, dfltChange := t.changeTermVal(n, v, false); valChange || dfltChange {
		return n, nil
	}
	return nil, nil
}

// changeTermVal is lyd_change_term_val: the term takes the value v (if it differs) and the
// default flag dflt; it reports what changed.
func (t *Tree) changeTermVal(n *Node, v types.Value, dflt bool) (valChange, dfltChange bool) {
	if !types.Equal(n.value, v) {
		t.changeNodeValue(n, v)
		valChange = true
		n.flags |= FlagNew
	}
	switch {
	case n.flags&FlagDefault != 0 && !dflt:
		n.flags &^= FlagDefault
		npContDfltDel(n.parent)
		dfltChange = true
	case n.flags&FlagDefault == 0 && dflt:
		n.flags |= FlagDefault
		npContDfltSet(n.parent)
		dfltChange = true
	}
	return valChange, dfltChange
}

// changeNodeValue is lyd_change_node_value: a leaf-list instance or a list key changes the hash
// of its instance, which is re-inserted (re-sorted when it has siblings and is sorted).
func (t *Tree) changeNodeValue(n *Node, v types.Value) {
	target := n
	switch {
	case n.schema.Kind == schema.LeafList:
	case n.isKey():
		target = n.parent
	default:
		n.value = v
		return
	}
	sib, parent := target.siblingsOf(), target.parent
	if sib == nil {
		n.value = v
		return
	}
	if sortedSupported(target) && sib.len() > 1 {
		unlink(target)
		n.value = v
		t.insert(parent, target, insertDefault)
		return
	}
	sib.hashRemove(target)
	n.value = v
	sib.hashAdd(parent, target)
}

// dupInst is a lyd_dup_inst cache entry: the equal instances of a first instance and how many
// were used.
type dupInst struct {
	set  []*Node
	used int
}

// dupInstNext is lyd_dup_inst_next: equal target instances are matched one by one; once all are
// used, a key-less list, state leaf-list or user-ordered instance matches nothing more, others
// keep matching the first one.
// ponytail: the equal instances are collected by a scan of the siblings in order (libyang walks
// its hash bucket); fine while Merge runs on API-sized trees.
func (t *Tree) dupInstNext(inst *Node, cache map[*Node]*dupInst) *Node {
	if inst == nil {
		return nil
	}
	d := cache[inst]
	if d == nil {
		d = &dupInst{set: []*Node{inst}}
		full := isDupInstList(inst.schema)
		for n := range inst.siblingsOf().all() {
			if n != inst && compareSingle(t, n, inst, full) {
				d.set = append(d.set, n)
			}
		}
		cache[inst] = d
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

// mergeSibling is lyd_merge_sibling_r without options: src merged into the children of parent
// (nil: the top level of t).
func (t *Tree) mergeSibling(parent, src *Node, cache map[*Node]*dupInst) {
	sib := t.childrenOf(parent)
	var match *Node
	switch {
	case src.schema == nil:
		match = opaqNext(sib, src.Name())
	case src.schema.Kind == schema.List || src.schema.Kind == schema.LeafList:
		match = t.findFirst(sib, src)
	default:
		match = t.findSchema(sib, src.schema)
	}
	firstInst := match == nil
	match = t.dupInstNext(match, cache)
	if match == nil {
		d := t.dup(src)
		for e := range d.All() {
			e.flags |= FlagNew // required for validation
		}
		t.insert(parent, d, insertDefault)
		if firstInst {
			t.dupInstNext(d, cache) // do not match this instance next time
		}
		return
	}
	switch {
	case match.schema == nil:
		if !compareSingle(t, src, match, false) {
			o := *src.opaq
			match.opaq.Value, match.opaq.Hints = o.Value, o.Hints
			match.opaq.Format, match.opaq.Prefixes = o.Format, o.Prefixes
		}
	case match.schema.Kind == schema.Leaf && src.flags&FlagDefault == 0:
		t.changeTermVal(match, src.value, false)
	}
	childCache := map[*Node]*dupInst{}
	for _, c := range src.kids.nodes() {
		if !c.isKey() { // lyd_child_no_keys
			t.mergeSibling(match, c, childCache)
		}
	}
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
func (t *Tree) dup(n *Node) *Node {
	d := &Node{schema: n.schema, value: n.value, flags: n.flags, meta: append([]meta(nil), n.meta...)}
	if n.opaq != nil {
		o := *n.opaq
		d.opaq = &o
	}
	for _, c := range n.kids.nodes() {
		t.insert(d, t.dup(c), insertLast)
	}
	npContDfltSet(d)
	return d
}
