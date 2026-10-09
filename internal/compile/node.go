// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/schema_compile_node.c and src/schema_compile.c (lys_compile, P3)
// (BSD-3-Clause, © CESNET).

package compile

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/xpath"
)

// ctx->compile_opts bits (plugins_exts.h LYS_COMPILE_*, tree_schema.h LYS_IS_*).
const (
	optGrouping   = 0x01
	optDisabled   = 0x02
	optNoConfig   = 0x04
	optNoDisabled = 0x08 // LYS_COMPILE_NO_DISABLED: if-feature and obsolete status do not disable
	optIsInput    = 0x1000
	optIsOutput   = 0x2000
	optIsNotif    = 0x4000
	optRPCInput   = optIsInput | optNoConfig
	optRPCOutput  = optIsOutput | optNoConfig
	optNotif      = optIsNotif | optNoConfig
)

// Compiled node flags that schema.Node does not carry (lysc_node.flags).
const (
	flConfigW   = 0x01 // LYS_CONFIG_W
	flConfigR   = 0x02 // LYS_CONFIG_R
	flConfig    = flConfigW | flConfigR
	flKey       = 0x0100 // LYS_KEY
	flSetDflt   = 0x0200 // LYS_SET_DFLT
	flSetUnits  = 0x0400 // LYS_SET_UNITS
	flSetConfig = 0x0800 // LYS_SET_CONFIG
	flIsInput   = optIsInput
	flIsOutput  = optIsOutput
	flIsNotif   = optIsNotif
)

// nodeCtx is the part of struct lysc_ctx the node walk uses, for one compiled module.
type nodeCtx struct {
	c      *Context
	cur    *schema.Module // ctx->cur_mod
	pm     *pmod          // ctx->pmod: the (sub)module whose text is compiled
	opts   int            // ctx->compile_opts
	path   cpath
	fl     map[*schema.Node]int
	data   []*schema.Node // cur_mod->compiled->data, rpcs, notifs
	rpcs   []*schema.Node
	notifs []*schema.Node
	depth  int
	tc     *typeCtx // type compilation (design 06 C5)
	seen   map[uniqKey]bool
	scans  int // uniqueness scans run (index hits)
	// design 06 C6: groupings, uses, refines, augments
	usesAugs  pending[*usesAug]                        // ctx->uses_augs
	augs      pending[*topAug]                         // ctx->augs
	usesRfns  pending[*usesRfn]                        // ctx->uses_rfns
	devs      pending[*devSet]                         // ctx->devs
	dev       devState                                 // what the deviated copies carry
	pendingOf map[*parser.Node]int                     // pending augments and refines per uses
	groupings map[*parser.Node]bool                    // ctx->groupings: the uses stack
	grpIdx    map[*parser.Node]map[string]*parser.Node // groupings by name per parent (module: &Parsed.Node)
	pparent   map[*parser.Node]*parser.Node
	indexed   map[*pmod]bool            // pparent holds the nodes of these (sub)modules
	from      map[any]*pmod             // origin: where values added by a refine are written
	rfnExts   map[*parser.Node][]stmtIn // refines whose extension instances a refined copy got
	mustLocal map[*schema.Must]*pmod    // musts a refine added: the module they are written in
	ext       *extState                 // ctx->ext: the extension instance being compiled
}

// newNodeCtx is the start of lys_compile / LYSC_CTX_INIT_PMOD: the compile context of m into out.
func (c *Context) newNodeCtx(m *Module, out *schema.Module) *nodeCtx {
	if c.typeCache == nil {
		c.typeCache = newTypeCache()
	}
	parsed := map[*schema.Module]*Module{}
	for _, lm := range c.Modules {
		if lm.mod != nil {
			parsed[lm.mod] = lm
		}
	}
	if c.usedGrp == nil {
		c.usedGrp = map[*parser.Node]bool{}
	}
	w := &nodeCtx{c: c, cur: out, pm: &m.pmod, fl: map[*schema.Node]int{},
		tc: &typeCtx{cur: out, pmod: &m.pmod, parsed: parsed, cache: c.typeCache, budget: c.opts.Budget, types: c.types,
			compat: c.opts.PatternCompat},
		pparent: map[*parser.Node]*parser.Node{}, indexed: map[*pmod]bool{}, from: map[any]*pmod{},
		rfnExts: map[*parser.Node][]stmtIn{}, mustLocal: map[*schema.Must]*pmod{},
		pendingOf: map[*parser.Node]int{}, groupings: map[*parser.Node]bool{}, grpIdx: map[*parser.Node]map[string]*parser.Node{}}
	w.tc.iff = w.iffeatures
	w.path.init(out)
	return w
}

// compileNodes is the data-node part of lys_compile (SC:1776-1814), the entry point of the
// dep-set loop's compile(m): data nodes, rpcs and notifications of the main module, then the
// same per included submodule, into out (ctx->cur_mod->compiled). The first error stops.
func (c *Context) compileNodes(m *Module, out *schema.Module) error {
	m.mod = out
	for _, inc := range m.Includes {
		if inc.Sub != nil {
			inc.Sub.mod = out
		}
	}
	w := c.newNodeCtx(m, out)
	defer func() { out.Top, c.types = slices.Concat(w.data, w.rpcs, w.notifs), w.tc.types }()
	out.Exts = nil
	w.precompileOwnAugments(m)
	if err := w.precompileOwnDeviations(m); err != nil {
		return err
	}
	if err := w.topLevel(m.Parsed); err != nil {
		return err
	}
	for _, inc := range m.Includes {
		if inc.Sub == nil {
			continue
		}
		w.pm = &inc.Sub.pmod
		if err := w.topLevel(inc.Sub.Parsed); err != nil {
			return err
		}
	}
	w.pm = &m.pmod
	if err := w.validateGroupings(m); err != nil {
		return err
	}
	augs := make([]pendingAug, 0, len(w.augs.items))
	for _, a := range w.augs.items {
		augs = append(augs, pendingAug{nodeid: a.nid.str, pm: a.pm, ext: a.ext})
	}
	if err := w.unresMod(augs); err != nil { // P5
		return err
	}
	return w.unresDeviations()
}

func (w *nodeCtx) topLevel(p *parser.Module) error {
	for _, list := range [][]*parser.Node{p.Children, p.Actions, p.Notifications} {
		for _, pn := range list {
			if err := w.node(pn, nil, 0, nil); err != nil {
				return err
			}
		}
	}
	exts, err := w.compileExts(p.Stmt, nil, w.cur.Exts) // module extension instances
	w.cur.Exts = exts
	return err
}

// --- logging ---

func (w *nodeCtx) errf(code ly.Code, format string, a ...any) error {
	return w.c.logPath(code, w.path.location(), format, a...)
}

func (w *nodeCtx) v11() bool { return w.pm.Parsed.Version == "1.1" }

// --- lys_compile_node, lys_compile_node_ ---

var kinds = map[string]schema.Kind{
	"container": schema.Container, "leaf": schema.Leaf, "list": schema.List, "leaf-list": schema.LeafList,
	"choice": schema.Choice, "case": schema.Case, "anyxml": schema.AnyXML, "anydata": schema.AnyData,
	"rpc": schema.RPC, "action": schema.Action, "notification": schema.Notification,
	"input": schema.Input, "output": schema.Output,
}

type specFunc func(w *nodeCtx, pn *parser.Node, n *schema.Node) error

// node is lys_compile_node; inherited is the statusOf value of an enclosing uses or augment.
func (w *nodeCtx) node(pn *parser.Node, parent *schema.Node, inherited int, childSet *[]*schema.Node) error {
	if pn.Kind == "uses" {
		w.path.update(nil, "{uses}")
		w.path.update(nil, pn.Name)
		err := w.uses(pn, parent, inherited, childSet)
		w.path.pop()
		w.path.pop()
		return err
	}
	var pmodule *schema.Module
	if parent != nil {
		pmodule = parent.Module
	}
	w.path.update(pmodule, pn.Name)
	if w.depth++; w.depth > orDefault(w.c.opts.Budget.MaxDepth, DefaultMaxDepth) {
		return fmt.Errorf("%w: schema nodes nested deeper than %d", ErrBudget, orDefault(w.c.opts.Budget.MaxDepth, DefaultMaxDepth))
	}
	defer func() { w.depth-- }()
	prev := w.opts
	var spec specFunc
	switch pn.Kind {
	case "container":
		spec = (*nodeCtx).container
	case "leaf":
		spec = (*nodeCtx).leaf
	case "list":
		spec = (*nodeCtx).list
	case "leaf-list":
		spec = (*nodeCtx).leafList
	case "choice":
		spec = (*nodeCtx).choice
	case "case":
		spec = (*nodeCtx).caseNode
	case "anyxml", "anydata":
		spec = (*nodeCtx).any
	case "rpc", "action":
		if w.opts&(optIsInput|optIsOutput|optIsNotif) != 0 {
			where := "another RPC/action"
			if w.opts&optIsNotif != 0 {
				where = "notification"
			}
			return w.errf(ly.Semantics, "Action \"%s\" is placed inside %s.", pn.Name, where)
		}
		spec = (*nodeCtx).action
		w.opts |= optNoConfig
	case "notification":
		if w.opts&(optIsInput|optIsOutput|optIsNotif) != 0 {
			where := "RPC/action"
			if w.opts&optIsNotif != 0 {
				where = "another notification"
			}
			return w.errf(ly.Semantics, "Notification \"%s\" is placed inside %s.", pn.Name, where)
		}
		spec = (*nodeCtx).notif
		w.opts |= optNotif
	default:
		return fmt.Errorf("compile: unexpected %q statement in the node tree", pn.Kind)
	}
	err := w.nodeGeneric(pn, parent, inherited, spec, &schema.Node{Kind: kinds[pn.Kind]}, childSet)
	w.opts = prev
	w.path.pop()
	return err
}

// nodeGeneric is lys_compile_node_.
func (w *nodeCtx) nodeGeneric(pn *parser.Node, parent *schema.Node, inherited int, spec specFunc, n *schema.Node,
	childSet *[]*schema.Node) error {
	if w.c.nodes++; w.c.nodes > orDefault(w.c.opts.Budget.MaxNodes, DefaultMaxNodes) {
		return fmt.Errorf("%w: more than %d compiled schema nodes", ErrBudget, orDefault(w.c.opts.Budget.MaxNodes, DefaultMaxNodes))
	}
	n.Module, n.Parent = w.cur, parent
	// refines and deviations of the node (lys_compile_node_deviations_refines)
	dev, err := w.nodeRefines(pn, parent)
	if err != nil {
		return err
	}
	dev, notSupported, err := w.nodeDeviations(pn, parent, dev)
	if err != nil {
		return err
	}
	prev := w.opts
	defer func() { w.opts = prev }()
	if notSupported {
		w.disable(n) // kept just like nodes disabled by if-feature
	}
	if dev == nil {
		return w.nodeGenericRest(pn, parent, inherited, spec, n, childSet)
	}
	err = w.nodeGenericRest(dev, parent, inherited, spec, n, childSet)
	var r rc
	if errors.As(err, &r) {
		_ = w.errf(ly.Other, "Compilation of a deviated and/or refined node failed.")
	}
	return err
}

// nodeGenericRest is lys_compile_node_ after the refines.
func (w *nodeCtx) nodeGenericRest(pn *parser.Node, parent *schema.Node, inherited int, spec specFunc, n *schema.Node,
	childSet *[]*schema.Node) error {
	prev := w.opts
	defer func() { w.opts = prev }()
	n.Name = pn.Name
	if n.Kind == schema.Input || n.Kind == schema.Output {
		n.Name = pn.Kind
	}
	enabled, err := w.ifFeature(pn.IfFeatures) // refine-added ones in the refine's module
	if err != nil {
		return err
	}
	if !enabled {
		w.disable(n)
	}
	if err := w.nodeFlags(pn, inherited, n); err != nil {
		return err
	}
	if n.Status == schema.Obsolete && !w.c.opts.CompileObsolete {
		w.disable(n) // obsolete, will not be in the compiled tree
	}
	if n.Kind == schema.List || n.Kind == schema.LeafList { // list ordering
		n.UserOrdered = w.fl[n]&(flConfigR|flIsOutput|flIsNotif) != 0 || pn.OrderedBy == "user"
	}
	if err := w.connect(parent, n); err != nil {
		return err
	}
	if pn.When != nil {
		wh, err := w.when(pn.When, n, schema.DataNode(n))
		if err != nil {
			return err
		}
		n.Whens = append(n.Whens, wh)
		w.addWhen(wh, n)
	}
	if err := spec(w, pn, n); err != nil {
		return err
	}
	if l, ok := w.dev.exts[pn]; ok { // the exts array a deviation changed
		if n.Exts, err = w.compileExtList(l, n, n.Exts); err != nil {
			return err
		}
	} else {
		if n.Exts, err = w.compileExts(pn.Stmt, n, n.Exts); err != nil {
			return err
		}
		for _, rs := range w.rfnExts[pn] { // DUP_EXTS of lys_apply_refine: after the node's own
			if n.Exts, err = w.compileExtsIn(rs.pm, rs.stmt, n, n.Exts); err != nil {
				return err
			}
		}
	}
	if n.Mandatory {
		mandatoryParents(parent)
	}
	if childSet != nil {
		*childSet = append(*childSet, n)
	}
	return nil
}

// nodeFlags is lys_compile_node_flags.
func (w *nodeCtx) nodeFlags(pn *parser.Node, inherited int, n *schema.Node) error {
	f := 0
	if pn.Config != nil {
		f = flConfigR
		if *pn.Config {
			f = flConfigW
		}
	}
	n.Mandatory = pn.Mandatory != nil && *pn.Mandatory
	w.fl[n] = f
	if err := w.config(n); err != nil {
		return err
	}
	parentSt, parentName := 0, ""
	if n.Parent != nil {
		parentSt, parentName = int(n.Parent.Status)+1, n.Parent.Name
	}
	st, err := w.status(statusOf(pn.Status), inherited, parentSt, parentName, n.Name)
	if err != nil {
		return err
	}
	n.Status = st
	switch {
	case w.opts&optIsInput != 0 && n.Kind != schema.Input:
		w.fl[n] |= flIsInput
	case w.opts&optIsOutput != 0 && n.Kind != schema.Output:
		w.fl[n] |= flIsOutput
	case w.opts&optIsNotif != 0 && n.Kind != schema.Notification:
		w.fl[n] |= flIsNotif
	}
	return nil
}

// statusOf is the parsed status flag: 0 none, 1 current, 2 deprecated, 3 obsolete.
func statusOf(s string) int {
	switch s {
	case "current":
		return 1
	case "deprecated":
		return 2
	case "obsolete":
		return 3
	}
	return 0
}

var statusStr = [...]string{"", "current", "deprecated", "obsolete"}

// status is lys_compile_status with statuses as statusOf values.
func (w *nodeCtx) status(parsed, inherited, parent int, parentName, name string) (schema.Status, error) {
	switch {
	case parent != 0 && parsed != 0 && parent > parsed:
		return 0, w.errf(ly.Semantics, "Status \"%s\" of \"%s\" is in conflict with \"%s\" status of parent \"%s\".",
			statusStr[parsed], name, statusStr[parent], parentName)
	case inherited != 0 && parsed != 0 && inherited > parsed:
		return 0, w.errf(ly.Semantics, "Inherited schema-only status \"%s\" is in conflict with \"%s\" status of \"%s\".",
			statusStr[inherited], statusStr[parsed], name)
	case parent != 0 && inherited != 0 && parent > inherited:
		return 0, w.errf(ly.Semantics, "Status \"%s\" of parent \"%s\" is in conflict with inherited schema-only status \"%s\".",
			statusStr[parent], parentName, statusStr[inherited])
	}
	for _, s := range []int{parsed, inherited, parent} {
		if s != 0 {
			return [...]schema.Status{1: schema.Current, 2: schema.Deprecated, 3: schema.Obsolete}[s], nil
		}
	}
	return schema.Current, nil
}

// config is lys_compile_config.
func (w *nodeCtx) config(n *schema.Node) error {
	f := w.fl[n]
	switch {
	case w.opts&optNoConfig != 0:
		f &^= flConfig // ignore config inside rpc/action/notification data
	case f&flConfig == 0:
		if n.Parent != nil && w.fl[n.Parent]&flConfig != 0 {
			f |= w.fl[n.Parent] & flConfig
		} else {
			f |= flConfigW
		}
	default:
		f |= flSetConfig
	}
	w.fl[n] = f
	n.Config = f&flConfigW != 0
	n.ConfigUnset = f&flConfig == 0 && w.ext != nil
	if n.Parent != nil && w.fl[n.Parent]&flConfigR != 0 && f&flConfigW != 0 {
		return w.errf(ly.Semantics, "Configuration node cannot be child of any state data node.")
	}
	return nil
}

// mandatoryParents is lys_compile_mandatory_parents(parent, 1).
func mandatoryParents(p *schema.Node) {
	for ; p != nil && p.Kind == schema.Container && !p.Mandatory && !p.Presence; p = p.Parent {
		p.Mandatory = true
	}
}

// --- lys_compile_node_connect, lys_compile_node_uniqness ---

// connect is lys_compile_node_connect.
func (w *nodeCtx) connect(parent, n *schema.Node) error {
	n.Parent = parent
	if parent == nil && w.ext != nil { // top level of an extension instance
		w.ext.inst.Nodes = append(w.ext.inst.Nodes, n)
		return w.uniqueness(nil, n.Name, n)
	}
	if parent == nil {
		switch n.Kind {
		case schema.RPC:
			w.rpcs = append(w.rpcs, n)
		case schema.Notification:
			w.notifs = append(w.notifs, n)
		default:
			w.data = append(w.data, n)
		}
		return w.uniqueness(nil, n.Name, n)
	}
	var list *[]*schema.Node
	switch n.Kind {
	case schema.Input, schema.Output:
		// part of the action: Children are always input, output
		parent.Children = append(parent.Children, n)
		return nil
	case schema.Action:
		list = &parent.Actions
	case schema.Notification:
		list = &parent.Notifs
	default:
		list = &parent.Children
	}
	ch := *list
	at := len(ch)
	switch last := len(ch) - 1; {
	case len(ch) == 0:
	case w.fl[n]&flKey != 0:
		at = 0
		if w.fl[ch[0]]&flKey != 0 { // insert after the last key
			for at < last && w.fl[ch[at]]&flKey != 0 {
				at++
			}
			at++
		}
	case ch[last].Module == n.Module:
		// last child is from the same module, keep the order and insert at the end
	case parent.Module == n.Module:
		// adding a module child after some augments were connected
		at = 0
		for ch[at].Module == n.Module {
			at++
		}
	default:
		// keep module name order of the augmenting modules
		for at = last; ; at-- {
			a := ch[at]
			if a.Module == n.Module || a.Module.Name < n.Module.Name || a.Module == parent.Module {
				at++
				break
			}
			if at == 0 {
				break
			}
		}
	}
	*list = slices.Insert(ch, at, n)
	return w.uniqueness(parent, n.Name, n)
}

// top is the compiled top level of the module being compiled in lys_getnext order.
func (w *nodeCtx) top() []*schema.Node { return slices.Concat(w.data, w.rpcs, w.notifs) }

// findChild is lys_find_child; the top level of the module being compiled is still w's lists.
func (w *nodeCtx) findChild(parent *schema.Node, mod *schema.Module, name string, opts schema.GetNextOpt) *schema.Node {
	if mod == nil {
		return nil
	}
	var top []*schema.Node
	switch {
	case parent != nil:
	case mod == w.cur:
		top = w.top()
	default:
		top = mod.Top
	}
	return schema.FindChild(parent, top, mod, name, opts)
}

// uniqKey is one name in the scope lys_compile_node_uniqness scans: the nearest ancestor that
// is not a choice or case (nil: top level), or the choice for a case.
type uniqKey struct {
	ext   *schema.ExtInstance // ctx->ext: its top level is a scope of its own
	scope *schema.Node
	mod   *schema.Module
	name  string
	cs    bool
}

// uniqueness is lys_compile_node_uniqness. A name not yet seen in its scope cannot clash, so
// the libyang scan (which decides the duplicate and its message) only runs on an index hit;
// without that, connecting n siblings would be O(n²). Names stay in the index (false hits are
// harmless: the scan finds nothing).
func (w *nodeCtx) uniqueness(parent *schema.Node, name string, excl *schema.Node) error {
	k := uniqKey{scope: parent, mod: excl.Module, name: name, cs: excl.Kind == schema.Case}
	if w.ext != nil {
		k.ext = w.ext.inst
	}
	for !k.cs && k.scope != nil && (k.scope.Kind == schema.Choice || k.scope.Kind == schema.Case) {
		k.scope = k.scope.Parent
	}
	if w.seen == nil {
		w.seen = map[uniqKey]bool{}
	}
	if !w.seen[k] {
		w.seen[k] = true
		return nil
	}
	w.scans++
	same := func(it *schema.Node) bool { return it != excl && it.Module == excl.Module && it.Name == name }
	what := "data definition/RPC/action/notification"
	var dup *schema.Node
	find := func() *schema.Node {
		if excl.Kind == schema.Case { // check restricted to the cases
			for _, it := range parent.Children {
				if same(it) {
					what = "case"
					return it
				}
			}
			return nil
		}
		var choices []*schema.Node
		if parent != nil && parent.Kind == schema.Case {
			// move to the first data definition parent, remembering the choices on the way
			stop := schema.DataNode(parent.Parent)
			for {
				parent = parent.Parent
				if parent != nil && parent.Kind == schema.Choice {
					choices = append(choices, parent)
				}
				if parent == stop {
					break
				}
			}
		}
		opts := schema.GetNextWithChoice
		if parent != nil && (parent.Kind == schema.RPC || parent.Kind == schema.Action) {
			// move to the input/output
			if w.fl[excl]&flIsOutput != 0 {
				opts |= schema.GetNextOutput
				parent = parent.Children[1]
			} else {
				parent = parent.Children[0]
			}
		}
		var top []*schema.Node
		if parent == nil {
			top = w.top()
			if w.ext != nil {
				top = w.ext.inst.Nodes
			}
		}
		for it := range schema.GetNext(parent, top, opts) {
			if !slices.Contains(choices, it) && same(it) {
				return it
			}
			if it.Kind == schema.Choice {
				for it2 := range schema.GetNext(it, nil, 0) {
					if same(it2) {
						return it2
					}
				}
			}
		}
		actions, notifs := w.rpcs, w.notifs
		if parent == nil && w.ext != nil {
			return nil // the extension's top level has no operations
		}
		if parent != nil {
			actions, notifs = parent.Actions, parent.Notifs
		}
		for _, it := range slices.Concat(actions, notifs) {
			if same(it) {
				return it
			}
		}
		return nil
	}
	if dup = find(); dup != nil {
		_ = w.errf(ly.SyntaxYang, "Duplicate identifier \"%s\" of %s statement.", dup.LogPath(), what)
		return eExist
	}
	return nil
}

// --- when, must ---

// nsCtx is the prefix context of schema text written in pm (lyplg_type_prefix_data_new with
// LY_VALUE_SCHEMA): "" and the module's own prefix are its module, then the imports.
func nsCtx(pm *pmod) schema.NSCtx {
	ns := schema.NSCtx{"": pm.mod, pm.Parsed.Prefix: pm.mod}
	for u, im := range pm.Parsed.Imports {
		if u < len(pm.Imports) && pm.Imports[u] != nil && pm.Imports[u].mod != nil {
			if _, dup := ns[im.Prefix]; !dup {
				ns[im.Prefix] = pm.Imports[u].mod
			}
		}
	}
	return ns
}

// xpathNS adapts a schema.NSCtx to xpath.NamespaceCtx; unprefixed names are def.
type xpathNS struct {
	ctx schema.NSCtx
	def string
}

func (x xpathNS) Resolve(prefix string) (string, bool) {
	if m := x.ctx[prefix]; m != nil && prefix != "" {
		return m.Name, true
	}
	return "", false
}

func (x xpathNS) Prefix(module string) string {
	best := ""
	for p, m := range x.ctx {
		if p != "" && m != nil && m.Name == module && (best == "" || p < best) {
			best = p
		}
	}
	if best == "" {
		return module
	}
	return best
}

func (x xpathNS) Default() string { return x.def }

// xpathCompile is lyxp_expr_parse with its error logged at the current path.
func (w *nodeCtx) xpathCompile(src string, ns schema.NSCtx) (*xpath.Expr, error) {
	e, err := xpath.Compile(src, xpathNS{ns, w.cur.Name})
	if err != nil {
		var xe *xpath.Error
		if !errors.As(err, &xe) {
			return nil, err
		}
		code := ly.XPath
		for c := ly.Success; c <= ly.Other; c++ {
			if c.String() == xe.VECode {
				code = c
			}
		}
		_ = w.errf(code, "%s", xe.Msg)
		return nil, rc(xe.Err)
	}
	return e, nil
}

// when is lys_compile_when for the node's own when (the unres part is design 06 C7).
func (w *nodeCtx) when(pw *parser.Restr, n, ctxNode *schema.Node) (*schema.When, error) {
	ns := nsCtx(w.pm)
	e, err := w.xpathCompile(pw.Arg, ns)
	if err != nil {
		return nil, err
	}
	// its extension instances compile to nothing observable: schema.When keeps none, and the
	// only plugins with effects reject or drop instances placed here at parse time
	// lys_compile_status(0, node's parsed status, node): the node's own status
	return &schema.When{Src: pw.Arg, Ctx: ns, ContextNode: ctxNode, Status: n.Status, Compiled: e}, nil
}

// musts is COMPILE_ARRAY of lys_compile_must (adding them to unres is design 06 C7).
func (w *nodeCtx) musts(pn *parser.Node, n *schema.Node) error {
	for _, pm := range pn.Musts {
		ns := nsCtx(w.origin(pm))
		e, err := w.xpathCompile(pm.Arg, ns)
		if err != nil {
			return err
		}
		must := &schema.Must{Src: pm.Arg, Msg: pm.ErrorMessage, AppTag: pm.ErrorAppTag, Ctx: ns, Compiled: e}
		if o := w.origin(pm); o != w.pm {
			w.mustLocal[must] = o // a refine's must: local module of its unres check
		}
		n.Musts = append(n.Musts, must)
	}
	if n.Kind != schema.Input && n.Kind != schema.Output { // their musts are added by action
		w.addMusts(n)
	}
	return nil
}

// --- nodetype-specific parts ---

func (w *nodeCtx) children(list []*parser.Node, n *schema.Node) error {
	for _, c := range list {
		if err := w.node(c, n, 0, nil); err != nil {
			return err
		}
	}
	return nil
}

// container is lys_compile_node_container.
func (w *nodeCtx) container(pn *parser.Node, n *schema.Node) error {
	n.Presence = pn.Presence != nil
	if err := w.children(pn.Children, n); err != nil {
		return err
	}
	if err := w.musts(pn, n); err != nil {
		return err
	}
	if err := w.augments(n); err != nil {
		return err
	}
	if err := w.children(pn.Actions, n); err != nil {
		return err
	}
	return w.children(pn.Notifications, n)
}

// nodeType is lys_compile_node_type without the unres parts (leafref, disabled bits/enums:
// design 06 C7).
func (w *nodeCtx) nodeType(pn *parser.Node, n *schema.Node) error {
	units, dflt, err := w.compileLeafType(n, pn, w.fl[n]&flSetUnits == 0)
	if err != nil {
		var ve *vErr
		if errors.As(err, &ve) {
			_ = w.errf(ve.Code, "%s", ve.Msg)
			return rc(ve.Err)
		}
		return err
	}
	if units != nil {
		n.Units = units
	}
	if dflt != nil && w.fl[n]&flSetDflt == 0 && w.opts&(optDisabled|optGrouping) == 0 {
		n.Default = []schema.DefaultValue{*dflt}
	}
	w.addTypeUnres(n, w.origin(pn.Type), dflt != nil && w.fl[n]&flSetDflt == 0)
	return nil
}

// leaf is lys_compile_node_leaf.
func (w *nodeCtx) leaf(pn *parser.Node, n *schema.Node) error {
	if err := w.musts(pn, n); err != nil {
		return err
	}
	if pn.Units != nil {
		n.Units = pn.Units
		w.fl[n] |= flSetUnits
	}
	if err := w.nodeType(pn, n); err != nil {
		return err
	}
	if len(pn.Defaults) > 0 {
		if w.opts&(optDisabled|optGrouping) == 0 {
			n.Default = []schema.DefaultValue{{Lex: pn.Defaults[0], NS: nsCtx(w.origin(dfltKey{pn}))}}
		}
		w.addDflt(n)
		w.fl[n] |= flSetDflt
	}
	n.DefaultSet = w.fl[n]&flSetDflt != 0
	if n.DefaultSet && n.Mandatory {
		return w.errf(ly.Semantics, "Invalid mandatory leaf with a default value.")
	}
	if n.Mandatory {
		// lys_compile_unres_leaf_dlft (SC:1026): a mandatory leaf never gets its typedef's default
		n.Default = nil
	}
	return nil
}

// leafList is lys_compile_node_leaflist.
func (w *nodeCtx) leafList(pn *parser.Node, n *schema.Node) error {
	if err := w.musts(pn, n); err != nil {
		return err
	}
	if pn.Units != nil {
		n.Units = pn.Units
		w.fl[n] |= flSetUnits
	}
	if err := w.nodeType(pn, n); err != nil {
		return err
	}
	if len(pn.Defaults) > 0 {
		if !w.v11() {
			return w.errf(ly.Semantics, "Leaf-list default values are allowed only in YANG 1.1 modules.")
		}
		if w.opts&(optDisabled|optGrouping) == 0 {
			n.Default = nil
			for i, d := range pn.Defaults {
				n.Default = append(n.Default, schema.DefaultValue{Lex: d, NS: nsCtx(w.dfltOrigin(pn, i))})
			}
		}
		w.addDflt(n)
		w.fl[n] |= flSetDflt
	}
	minMax(pn, n)
	if w.fl[n]&flConfigR != 0 {
		n.UserOrdered = true // state leaf-list is always ordered-by user
	}
	n.DefaultSet = w.fl[n]&flSetDflt != 0
	if n.DefaultSet && n.Mandatory {
		return w.errf(ly.Semantics, "The default statement is present on leaf-list with a nonzero min-elements.")
	}
	if n.Max != 0 && n.Min > n.Max {
		return w.errf(ly.Semantics, "Leaf-list min-elements %d is bigger than max-elements %d.", n.Min, n.Max)
	}
	return nil
}

func minMax(pn *parser.Node, n *schema.Node) {
	if pn.MinElements != nil {
		n.Min = *pn.MinElements
	}
	if n.Min > 0 {
		n.Mandatory = true
	}
	if pn.MaxElements != nil {
		n.Max = *pn.MaxElements // 0 = unbounded
	}
}

// keyTokens splits a key or unique argument like the strpbrk/isspace loops of
// lys_compile_node_list(_unique): each token with the rest of the argument from it on.
func keyTokens(s string, yield func(tok, rest string) error) error {
	for {
		i := strings.IndexAny(s, " \t\n")
		if i < 0 {
			return yield(s, s)
		}
		tok, rest := s[:i], s
		s = strings.TrimLeft(s[i:], " \t\n\v\f\r")
		if err := yield(tok, rest); err != nil {
			return err
		}
	}
}

// list is lys_compile_node_list.
func (w *nodeCtx) list(pn *parser.Node, n *schema.Node) error {
	minMax(pn, n)
	if err := w.children(pn.Children, n); err != nil {
		return err
	}
	if err := w.musts(pn, n); err != nil {
		return err
	}
	if w.fl[n]&flConfigW != 0 {
		p := n
		if w.opts&optGrouping != 0 {
			// compiling an individual grouping: check only with an explicit config
			for p != nil && w.fl[p]&flSetConfig == 0 {
				p = p.Parent
			}
		}
		if p != nil && (pn.Key == nil || *pn.Key == "") {
			return w.errf(ly.Semantics, "Missing key in list representing configuration data.")
		}
	}
	if pn.Key == nil {
		n.UserOrdered = true // keyless list
	} else {
		var keys []*schema.Node
		err := keyTokens(*pn.Key, func(tok, _ string) error {
			key := w.findChild(n, n.Module, tok, schema.GetNextNoChoice)
			if key != nil && key.Kind != schema.Leaf {
				key = nil
			}
			if key == nil {
				return w.errf(ly.Reference, "The list's key \"%s\" not found.", tok)
			}
			if w.fl[key]&flKey != 0 {
				return w.errf(ly.Semantics, "Duplicated key identifier \"%s\".", tok)
			}
			w.path.update(n.Module, key.Name)
			if w.fl[n]&flConfig != w.fl[key]&flConfig {
				return w.errf(ly.Semantics, "Key of a configuration list must not be a state leaf.")
			}
			if !w.v11() {
				if key.Type != nil && key.Type.Base == schema.Empty {
					return w.errf(ly.Semantics, "List key of the \"empty\" type is allowed only in YANG 1.1 modules.")
				}
			} else if len(key.Whens) > 0 {
				return w.errf(ly.Semantics, "List's key must not have any \"when\" statement.")
			}
			if err := checkStatus(n.Status, n.Module, n.Name, key.Status, key.Module, key.Name); err != nil {
				return w.vlog(err)
			}
			key.Default = nil // keys ignore default values
			w.fl[key] |= flKey
			// move it after the previous key
			i := slices.Index(n.Children, key)
			n.Children = slices.Delete(n.Children, i, i+1)
			n.Children = slices.Insert(n.Children, len(keys), key)
			keys = append(keys, key)
			w.path.pop()
			return nil
		})
		if err != nil {
			return err
		}
		n.Keys = keys
	}
	if err := w.augments(n); err != nil {
		return err
	}
	for i, u := range pn.Uniques {
		if err := w.unique(u, w.uniqueOrigin(pn, i), n); err != nil {
			return err
		}
	}
	if err := w.children(pn.Actions, n); err != nil {
		return err
	}
	if err := w.children(pn.Notifications, n); err != nil {
		return err
	}
	if n.Max != 0 && n.Min > n.Max {
		return w.errf(ly.Semantics, "List min-elements %d is bigger than max-elements %d.", n.Min, n.Max)
	}
	return nil
}

var nodetypeStr = map[schema.Kind]string{
	schema.Container: "container", schema.Choice: "choice", schema.Leaf: "leaf", schema.LeafList: "leaf-list",
	schema.List: "list", schema.AnyXML: "anyxml", schema.AnyData: "anydata", schema.Case: "case",
	schema.RPC: "RPC", schema.Action: "action", schema.Notification: "notification",
}

// lysNodetype2str is lys_nodetype2str.
func lysNodetype2str(k schema.Kind) string {
	if s, ok := nodetypeStr[k]; ok {
		return s
	}
	return "unknown"
}

// unique is one statement of lys_compile_node_list_unique, written in pm.
func (w *nodeCtx) unique(u string, pm *pmod, list *schema.Node) error {
	config := -1
	var leaves []*schema.Node
	err := keyTokens(u, func(tok, rest string) error {
		key, flags, err := w.resolveNodeid(rest, len(tok), list, pm, schema.Leaf)
		switch {
		case errors.Is(err, eDenied):
			_ = w.errf(ly.Reference, "Unique's descendant-schema-nodeid \"%s\" refers to %s node instead of a leaf.",
				tok, lysNodetype2str(key.Kind))
			return eValid
		case err != nil:
			return eValid
		case flags != 0:
			where := "RPC/action"
			if flags&optIsNotif != 0 {
				where = "notification"
			}
			return w.errf(ly.Reference, "Unique's descendant-schema-nodeid \"%s\" refers into %s node.", tok, where)
		}
		switch {
		case config != -1 && (w.fl[key]&flConfigW != 0 && config == 0 || w.fl[key]&flConfigR != 0 && config == 1):
			return w.errf(ly.Semantics, "Unique statement \"%s\" refers to leaves with different config type.", u)
		case w.fl[key]&flConfigW != 0:
			config = 1
		default:
			config = 0
		}
		for p := key.Parent; p != list; p = p.Parent {
			if p.Kind == schema.List {
				return w.errf(ly.Semantics, "Unique statement \"%s\" refers to a leaf in nested list \"%s\".", u, p.Name)
			}
		}
		if err := checkStatus(list.Status, pm.mod, list.Name, key.Status, key.Module, key.Name); err != nil {
			return w.vlog(err)
		}
		leaves = append(leaves, key)
		return nil
	})
	if err != nil {
		return err
	}
	list.Uniques = append(list.Uniques, leaves)
	return nil
}

func isIdentStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

func isIdentChar(c byte) bool {
	return isIdentStart(c) || c >= '0' && c <= '9' || c == '-' || c == '.'
}

// parseID is lys_parse_id: the end of the identifier at s[i:], ok false if there is none.
func parseID(s string, i int) (int, bool) {
	if i >= len(s) || !isIdentStart(s[i]) {
		return i, false
	}
	i++
	for i < len(s) && isIdentChar(s[i]) {
		i++
	}
	return i, true
}

// resolvePrefix is ly_schema_resolve_prefix: the own prefix or an import of pm, as the
// compiled module (nil if that is not bound); ok false when the prefix is unknown.
func resolvePrefix(pm *pmod, prefix string) (*schema.Module, bool) {
	if prefix == pm.Parsed.Prefix {
		return pm.mod, true
	}
	for u, im := range pm.Parsed.Imports {
		if im.Prefix == prefix && u < len(pm.Imports) && pm.Imports[u] != nil {
			return pm.Imports[u].mod, true
		}
	}
	return nil, false
}

// resolveNodeid is lysc_resolve_schema_nodeid for LY_VALUE_SCHEMA prefixes of pm: s is the
// nodeid with the rest of its argument, n its length (0: all of s). A nil ctxNode means an
// absolute nodeid. The flags are optNotif/optRPCInput/optRPCOutput for a path through them.
func (w *nodeCtx) resolveNodeid(s string, n int, ctxNode *schema.Node, pm *pmod, nodetype schema.Kind) (*schema.Node, int, error) {
	typ := "absolute"
	if n == 0 {
		n = len(s)
	}
	id := 0
	if ctxNode != nil {
		typ = "descendant"
		if strings.HasPrefix(s, "/") {
			return nil, 0, w.errf(ly.Reference, "Invalid descendant-schema-nodeid value \"%s\" - absolute-schema-nodeid used.", s[:n])
		}
	} else {
		if !strings.HasPrefix(s, "/") {
			return nil, 0, w.errf(ly.Reference, "Invalid absolute-schema-nodeid value \"%s\" - missing starting \"/\".", s[:n])
		}
		id++
	}
	ok := false
	flags, extra := 0, schema.GetNextOpt(0)
	var kind schema.Kind
	for id < len(s) {
		start := id
		if id, ok = parseID(s, id); !ok {
			break
		}
		prefix, name := "", s[start:id]
		if id < len(s) && s[id] == ':' {
			prefix = name
			nstart := id + 1
			if id, ok = parseID(s, nstart); !ok {
				break
			}
			name = s[nstart:id]
		}
		mod := w.cur
		if prefix != "" {
			m, ok := resolvePrefix(pm, prefix)
			if !ok {
				_ = w.errf(ly.Reference, "Invalid %s-schema-nodeid value \"%s\" - prefix \"%s\" not defined in module \"%s\".",
					typ, s[:id], prefix, pm.Parsed.Name)
				return nil, 0, eNotFound
			}
			mod = m
		}
		if ctxNode != nil && (ctxNode.Kind == schema.RPC || ctxNode.Kind == schema.Action) {
			switch {
			case mod != ctxNode.Module:
				ctxNode = nil
			case name == "input":
				ctxNode = ctxNode.Children[0]
			case name == "output":
				ctxNode = ctxNode.Children[1]
				extra = schema.GetNextOutput
			default:
				ctxNode = nil // only input or output is valid
			}
		} else {
			ctxNode = w.findChild(ctxNode, mod, name, extra|schema.GetNextWithChoice|schema.GetNextWithCase)
			extra = 0
		}
		if ctxNode == nil {
			_ = w.errf(ly.Reference, "Invalid %s-schema-nodeid value \"%s\" - target node not found.", typ, s[:id])
			return nil, 0, eNotFound
		}
		kind = ctxNode.Kind
		switch kind {
		case schema.Notification:
			flags |= optNotif
		case schema.Input:
			flags |= optRPCInput
		case schema.Output:
			flags |= optRPCOutput
		}
		if id >= len(s) || id >= n {
			break
		}
		if s[id] != '/' {
			return nil, 0, w.errf(ly.Reference, "Invalid %s-schema-nodeid value \"%s\" - missing \"/\" as node-identifier separator.",
				typ, s[:id+1])
		}
		id++
	}
	if !ok {
		return nil, 0, w.errf(ly.Reference, "Invalid %s-schema-nodeid value \"%s\" - unexpected end of expression.", typ, s[:n])
	}
	if nodetype != 0 && kind != nodetype {
		return ctxNode, flags, eDenied
	}
	return ctxNode, flags, nil
}

// choice is lys_compile_node_choice.
func (w *nodeCtx) choice(pn *parser.Node, n *schema.Node) error {
	for _, c := range pn.Children {
		if err := w.choiceChild(c, n, nil); err != nil {
			return err
		}
	}
	if err := w.augments(n); err != nil {
		return err
	}
	if len(pn.Defaults) > 0 {
		return w.choiceDflt(pn.Defaults[0], w.origin(dfltKey{pn}), n)
	}
	return nil
}

// choiceChild is lys_compile_node_choice_child: a non-case child gets an implicit case.
func (w *nodeCtx) choiceChild(c *parser.Node, n *schema.Node, childSet *[]*schema.Node) error {
	if c.Kind == "case" {
		return w.node(c, n, 0, childSet)
	}
	cs := &parser.Node{Kind: "case", Name: c.Name, Children: []*parser.Node{c}}
	if err := w.node(cs, n, 0, childSet); err != nil {
		return err
	}
	for _, cc := range n.Children { // find our case node
		if cc.Name == cs.Name {
			if len(cc.Children) > 0 {
				cc.Status = cc.Children[0].Status // status is copied from its child
			}
			break
		}
	}
	return nil
}

// choiceDflt is lys_compile_node_choice_dflt; pm is where the default is written.
func (w *nodeCtx) choiceDflt(dflt string, pm *pmod, ch *schema.Node) error {
	mod, name := ch.Module, dflt
	if i := strings.IndexByte(dflt, ':'); i >= 0 {
		m, ok := resolvePrefix(pm, dflt[:i])
		if !ok {
			return w.errf(ly.Reference, "Default case prefix \"%s\" not found in imports of \"%s\".", dflt[:i], pm.Parsed.Name)
		}
		mod, name = m, dflt[i+1:]
	}
	cs := w.findChild(ch, mod, name, schema.GetNextWithCase)
	if cs != nil && cs.Kind != schema.Case {
		cs = nil
	}
	if cs == nil {
		return w.errf(ly.Semantics, "Default case \"%s\" not found.", dflt)
	}
	for _, it := range cs.Children {
		if it.Mandatory {
			return w.errf(ly.Semantics, "Mandatory node \"%s\" under the default case \"%s\".", it.Name, dflt)
		}
	}
	if ch.Mandatory {
		return w.errf(ly.Semantics, "Invalid mandatory choice with a default case.")
	}
	ch.DefaultCase, ch.DefaultCaseName = cs, cs.Name
	cs.DefaultSet = true
	return nil
}

// caseNode is lys_compile_node_case for an explicit (or shorthand) case.
func (w *nodeCtx) caseNode(pn *parser.Node, n *schema.Node) error {
	if err := w.children(pn.Children, n); err != nil {
		return err
	}
	return w.augments(n)
}

// any is lys_compile_node_any.
func (w *nodeCtx) any(pn *parser.Node, n *schema.Node) error {
	return w.musts(pn, n)
}

// action is lys_compile_node_action: input and output always exist.
func (w *nodeCtx) action(pn *parser.Node, n *schema.Node) error {
	for _, io := range []struct {
		p    *parser.Node
		kind string
	}{{pn.Input, "input"}, {pn.Output, "output"}} {
		w.path.update(n.Module, io.kind)
		p := io.p
		if p == nil {
			p = &parser.Node{Kind: io.kind}
		}
		inout := &schema.Node{Kind: kinds[io.kind]}
		err := w.nodeGeneric(p, n, 0, (*nodeCtx).inout, inout, nil)
		w.path.pop()
		if err != nil {
			return err
		}
		w.addMusts(inout)
	}
	return nil
}

// inout is lys_compile_node_action_inout.
func (w *nodeCtx) inout(pn *parser.Node, n *schema.Node) error {
	if err := w.musts(pn, n); err != nil {
		return err
	}
	if n.Kind == schema.Input {
		w.opts |= optRPCInput
	} else {
		w.opts |= optRPCOutput
	}
	if err := w.children(pn.Children, n); err != nil {
		return err
	}
	return w.augments(n)
}

// notif is lys_compile_node_notif.
func (w *nodeCtx) notif(pn *parser.Node, n *schema.Node) error {
	if err := w.musts(pn, n); err != nil {
		return err
	}
	if err := w.children(pn.Children, n); err != nil {
		return err
	}
	return w.augments(n)
}

// compileLeafType joins the type compiler (typeCtx.compileNodeType, design 06 C5) to the walk:
// the scope chain is pn's parsed ancestors (lysp_node.parent), innermost first, and the typedef
// default keeps the prefixes of the (sub)module it is written in.
func (w *nodeCtx) compileLeafType(n *schema.Node, pn *parser.Node, wantUnits bool) (
	units *string, dflt *schema.DefaultValue, err error) {
	sc := w.scopeOf(pn)
	w.tc.pmod = w.pm
	t, units, td, err := w.tc.compileNodeType(sc, n, pn.Type, w.origin(pn.Type), wantUnits) // a deviation's type in its module
	if err != nil {
		return nil, nil, err
	}
	n.Type = t
	if td != nil {
		dflt = &schema.DefaultValue{Lex: td.lex, NS: nsCtx(td.pm)}
	}
	return units, dflt, nil
}
