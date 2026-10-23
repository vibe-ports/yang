// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/printer_data.c (lyd_print_all, lyd_print_tree), src/out.c
// (lyd_metadata_should_print) and src/printer_internal.h (BSD-3-Clause, © CESNET).

package data

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/printopts"
	"github.com/vibe-ports/yang/internal/schema"
	"github.com/vibe-ports/yang/internal/types"
)

// ErrUnsupported is returned for a node the printers do not handle.
var ErrUnsupported = errors.New("data: not supported")

// WD is a with-defaults mode (LYD_PRINT_WD_*, RFC 6243).
type WD uint8

// With-defaults modes.
const (
	WDExplicit       WD = iota // only the data explicitly present, and config false defaults (the default)
	WDTrim                     // no node with its default value
	WDAll                      // report-all
	WDAllTagged                // report-all-tagged: defaults carry the wd:default attribute
	WDImplicitTagged           // implicit nodes carry the wd:default attribute
)

// wdModule is the module that defines the default attribute in the JSON encoding.
const wdModule = "ietf-netconf-with-defaults"

// PrintOptions are the printer flags of the M1 fixtures.
type PrintOptions struct {
	Shrink        bool // LYD_PRINT_SHRINK: no newlines and no indentation
	EmptyLeafList bool // LYD_PRINT_EMPTY_LEAF_LIST: JSON prints lists and leaf-lists without instances as []
	WithDefaults  WD

	dflt     *defaultCheck // set by printData: the evaluation state shared by one print
	step     func() error  // XPath casts charge each printed node and poll cancellation
	byteStep func() error  // XPath casts charge each 64 output, type-conversion and diagnostic bytes
}

// defaultCheck is what one print shares for isDefault: the evaluation context (one XPath budget
// for the whole print, DefaultMaxXPathSteps) and the first budget error, which fails the print.
type defaultCheck struct {
	vc    *valCtx
	views map[*Node]*Tree
	diags []yang.Diagnostic
	err   error
}

// printDiagnostic is an item left in the context log by a printer call. XML metadata values are
// printed with LYD_CTX(node), which may differ from the outer tree's context after duplication.
type printDiagnostic struct {
	set  *schema.Set
	diag yang.Diagnostic
}

// npCont is lysc_is_np_cont: a non-presence container.
func npCont(s *schema.Node) bool { return s != nil && s.Kind == schema.Container && !s.Presence }

// inOpNotif reports whether s is in an rpc/action input or output or a notification
// (LYS_IS_INPUT, LYS_IS_OUTPUT, LYS_IS_NOTIF).
func inOpNotif(s *schema.Node) bool {
	for p := s; p != nil; p = p.Parent {
		if p.Kind == schema.Input || p.Kind == schema.Output || p.Kind == schema.Notification {
			return true
		}
	}
	return false
}

// isDefault is lyd_is_default: a leaf or leaf-list instance whose value is a default of its
// schema node (the canonical form of the default stored for the node). lysc_value_cmp
// (tree_schema_common.c:2939-2955) validates the default with lyd_value_validate3 and the node as
// the context node, so a default that needs the data tree (a require-instance leafref or
// instance-identifier) whose target is missing is not a default. libyang's leafref validate_tree
// also links the node to the default's targets under LY_CTX_LEAFREF_LINKING
// (plugins_types/leafref.c:94-107); here the check is read-only (D-0128). A budget overrun is
// kept in o.dflt and fails the print (printData).
func (o PrintOptions) isDefault(n *Node) bool {
	if !n.isTerm() {
		return false
	}
	for _, d := range n.schema.Default {
		v, diag := types.StoreDefault(n.schema, d)
		if diag == nil && v.NeedsTree() {
			dc := o.dflt
			if dc == nil {
				dc = &defaultCheck{}
			}
			t := n.treeOf()
			if t == nil {
				root := n
				for root.parent != nil {
					root = root.parent
				}
				if dc.views == nil {
					dc.views = map[*Node]*Tree{}
				}
				t = dc.views[root]
				if t == nil {
					// A detached subtree is the validation root for its values. This
					// view is read-only: printing must not link leafrefs (D-0128).
					t = newTree(ctxOf(root))
					if root.schema == nil {
						t.top.opq = []*Node{root}
					} else {
						t.top.list = []*Node{root}
						t.top.contexts = map[*schema.Set]int{ctxOf(root): 1}
					}
					dc.views[root] = t
				}
			}
			if dc.err != nil {
				return false
			}
			set := ctxOf(n)
			if set == nil {
				set = t.context() // ownerless test schema
			}
			if dc.vc == nil || dc.vc.t != t {
				dc.vc = &valCtx{t: t, log: &logger{set: set}}
			} else {
				dc.vc.log.set = set
			}
			tt := &typeTree{vc: dc.vc, n: n, readOnly: true}
			before := len(dc.vc.log.diags)
			if v, diag = types.ValidateTree(n.schema.Type, v, tt); tt.err != nil {
				dc.err = tt.err
				dc.diags = append(dc.diags, dc.vc.log.diags[before:]...)
				return false
			}
			if diag != nil {
				for _, probe := range tt.probes {
					_ = dc.vc.xpathErr(probe, n) // lyxp_eval's LOGERR precedes the plugin's message
				}
				_ = dc.vc.log.storeErr(n, nil, diag)
			}
			dc.diags = append(dc.diags, dc.vc.log.diags[before:]...)
		}
		if diag == nil && v.Canonical() == n.value.Canonical() {
			return true
		}
	}
	return false
}

// tagged reports whether the with-defaults mode puts the default attribute on n.
func (o PrintOptions) tagged(n *Node) bool {
	return (n.flags&FlagDefault != 0 && (o.WithDefaults == WDAllTagged || o.WithDefaults == WDImplicitTagged)) ||
		(o.WithDefaults == WDAllTagged && o.isDefault(n))
}

// shouldPrint is lyd_node_should_print.
func (o PrintOptions) shouldPrint(n *Node, flags printopts.Flags) bool {
	switch {
	case o.WithDefaults == WDTrim:
		switch {
		case n.flags&FlagDefault != 0:
			return false // an implicit node or an NP container with only default nodes
		case n.isTerm():
			return !o.isDefault(n)
		case npCont(n.schema):
			if flags&printopts.EmptyCont != 0 { // src/out.c:58-62 (LYD_PRINT_EMPTY_CONT)
				return true
			}
			for _, c := range kids(n) { // an NP container without printed children
				if o.shouldPrint(c, flags) {
					return true
				}
			}
			return false
		}
	case n.flags&FlagDefault != 0 && n.schema != nil && n.schema.Kind == schema.Container:
		if flags&printopts.EmptyCont != 0 { // src/out.c:73-77 (LYD_PRINT_EMPTY_CONT)
			return true
		}
		for d := range n.All() { // avoid empty default containers
			if d != n && o.shouldPrint(d, flags) {
				return true
			}
		}
		return false
	case n.flags&FlagDefault != 0 && o.WithDefaults == WDExplicit && n.schema != nil && !configR(n.schema):
		// explicit mode: print only if it contains status data in its subtree
		if !inOpNotif(n.schema) && n.schema.Config {
			for e := range n.All() {
				if e.schema != nil && (e.schema.Kind != schema.Container || e.schema.Presence) && configR(e.schema) {
					return true
				}
			}
		}
		return false
	}
	return true
}

// PrintJSON writes the top-level siblings of the tree as RFC 7951 JSON, as lyd_print_all with
// LYD_JSON and LYD_PRINT_WITHSIBLINGS: 2-space indentation unless o.Shrink.
func (t *Tree) PrintJSON(w io.Writer, o PrintOptions) error {
	return printData(w, t.context(), o, slices.Collect(t.top.all()), 0, true, printJSON)
}

// PrintXML writes the top-level siblings of the tree as XML, as lyd_print_all with LYD_XML and
// LYD_PRINT_WITHSIBLINGS.
func (t *Tree) PrintXML(w io.Writer, o PrintOptions) error {
	return printData(w, t.context(), o, slices.Collect(t.top.all()), 0, true, printXML)
}

// PrintJSON writes n and its descendants, not its siblings, as RFC 7951 JSON (lyd_print_tree with
// LYD_JSON): a member of the top-level object, qualified with its module name.
func (n *Node) PrintJSON(w io.Writer, o PrintOptions) error { return n.print(w, o, printJSON) }

// PrintXML writes n and its descendants, not its siblings, as XML (lyd_print_tree with LYD_XML).
func (n *Node) PrintXML(w io.Writer, o PrintOptions) error { return n.print(w, o, printXML) }

// print is lyd_print_tree: n with its real siblings, so that leaf-list arrays and empty
// (leaf-)lists see the instances around it as libyang does.
func (n *Node) print(w io.Writer, o PrintOptions, f func(*printer, []*Node, int) error) error {
	if n == nil {
		return printData(w, nil, o, nil, 0, false, f)
	}
	set := ctxOf(n)
	sibs := slices.Collect(sibsOf(n).all())
	return printData(w, set, o, sibs, slices.Index(sibs, n), false, f)
}

// printData runs f over sibs from index from: all of them with siblings, else only sibs[from].
func printData(w io.Writer, set *schema.Set, o PrintOptions, sibs []*Node, from int, siblings bool,
	f func(*printer, []*Node, int) error) error {
	diags, err := printDataResult(w, set, o, sibs, from, siblings, f)
	if err != nil {
		return err
	}
	if len(diags) != 0 {
		flat := make([]yang.Diagnostic, len(diags))
		for i := range diags {
			flat[i] = diags[i].diag
		}
		return &ValidationError{rc: flat[0].Err, Diags: flat}
	}
	return nil
}

// printDataResult keeps lyd_print_'s return status separate from diagnostics left in the
// context log. In particular, xml_print_meta is void: a type printer may log an error and
// return NULL, after which the XML printer emits an empty attribute and succeeds.
func printDataResult(w io.Writer, set *schema.Set, o PrintOptions, sibs []*Node, from int, siblings bool,
	f func(*printer, []*Node, int) error) ([]printDiagnostic, error) {
	w, flags := printopts.Unwrap(w)
	if o.dflt == nil { // callers cannot set it; tests do
		o.dflt = &defaultCheck{}
	}
	p := &printer{set: set, opts: o, flags: flags, siblings: siblings}
	p.buf.byteStep = o.byteStep
	if err := runPrint(f, p, sibs, from); err != nil {
		var d *types.Diag
		if errors.As(err, &d) {
			owner := set
			var pe *printerTypeError
			if errors.As(err, &pe) {
				owner = pe.set
			}
			// lyd_print_mem leaves bytes produced before a type-printer failure.
			if _, writeErr := w.Write(p.buf.Bytes()); writeErr != nil {
				return nil, writeErr
			}
			// A NULL type print callback becomes LY_EINVAL (printer_xml.c:367).
			ve := &ValidationError{rc: "LY_EINVAL"}
			out := make([]printDiagnostic, 0, len(p.printDiags)+1)
			for _, prior := range p.printDiags {
				lg := &logger{set: prior.set}
				lg.logged(nil, prior.diag)
				for _, item := range lg.diags {
					out = append(out, printDiagnostic{set: prior.set, diag: item})
				}
			}
			if !d.NoItem {
				// A plugin may log its own item before returning NULL
				// (lyplg_type_print_xpath10). Keep that item even though the
				// XML printer itself always returns LY_EINVAL for NULL.
				lg := &logger{set: owner}
				lg.logged(nil, d)
				for _, item := range lg.diags {
					out = append(out, printDiagnostic{set: owner, diag: item})
				}
			}
			ve.Diags = make([]yang.Diagnostic, len(out))
			for i := range out {
				ve.Diags[i] = out[i].diag
			}
			return out, ve
		}
		return nil, err
	}
	if o.dflt.err != nil {
		return nil, o.dflt.err // nothing written: the output would be wrong
	}
	_, err := w.Write(p.buf.Bytes())
	if err != nil {
		return nil, err
	}
	if len(p.printDiags) != 0 {
		out := make([]printDiagnostic, 0, len(p.printDiags))
		for _, item := range p.printDiags {
			lg := &logger{set: item.set}
			lg.logged(nil, item.diag)
			for _, d := range lg.diags {
				out = append(out, printDiagnostic{set: item.set, diag: d})
			}
		}
		return out, nil
	}
	return nil, nil
}

// printer is the state the JSON and XML printers share (jsonpr_ctx, xmlpr_ctx).
type printer struct {
	set        *schema.Set
	opts       PrintOptions
	flags      printopts.Flags
	siblings   bool          // LYD_PRINT_SIBLINGS: lyd_print_all, else lyd_print_tree
	printDiags []printerDiag // xml_print_meta may log without stopping the printer
	buf        printBuffer
	level      int // indentation level
	depth      int // recursion bound for constructed payloads deeper than their schema
}

type printerDiag struct {
	set  *schema.Set
	diag *types.Diag
}

// printerTypeError retains LYD_CTX(node) for a fatal type-printer error. The outer printer
// context can differ for payload nodes duplicated across contexts.
type printerTypeError struct {
	set *schema.Set
	err error
}

func (e *printerTypeError) Error() string { return e.err.Error() }
func (e *printerTypeError) Unwrap() error { return e.err }

// printBuffer charges output in 64-byte units before bytes.Buffer can grow.
// The private panic stops the printer at the exact failed write; runPrint only
// catches this sentinel and leaves other panics visible.
type printBuffer struct {
	buf      bytes.Buffer
	byteStep func() error
	charged  int
}

type printBudgetStop struct{ err error }

func (b *printBuffer) reserve(n int) {
	if b.byteStep == nil || n == 0 {
		return
	}
	for need := (b.buf.Len() + n + 63) / 64; b.charged < need; b.charged++ {
		if err := b.byteStep(); err != nil {
			panic(printBudgetStop{err})
		}
	}
}

func (b *printBuffer) Write(p []byte) (int, error) {
	b.reserve(len(p))
	return b.buf.Write(p)
}

func (b *printBuffer) appendString(s string) {
	b.reserve(len(s))
	_, _ = b.buf.WriteString(s)
}

func (b *printBuffer) appendByte(c byte) {
	b.reserve(1)
	_ = b.buf.WriteByte(c)
}

func (b *printBuffer) Bytes() []byte { return b.buf.Bytes() }

func runPrint(f func(*printer, []*Node, int) error, p *printer, sibs []*Node, from int) (err error) {
	defer func() {
		if v := recover(); v != nil {
			if stop, ok := v.(printBudgetStop); ok {
				err = stop.err
			} else {
				panic(v)
			}
		}
	}()
	return f(p, sibs, from)
}

// A constructed data forest is not bounded by schema depth. The printer's
// recursive frames stop here before the Go stack can grow without limit.
const maxPrintDepth = 1024

func (p *printer) enter() error {
	if p.opts.step != nil {
		if err := p.opts.step(); err != nil {
			return err
		}
	}
	if p.depth >= maxPrintDepth {
		return fmt.Errorf("%w: print depth exceeds %d", yang.ErrBudget, maxPrintDepth)
	}
	p.depth++
	return nil
}

// nodeContext is LYD_CTX(n), with the print's context for ownerless test schemas.
func (p *printer) nodeContext(n *Node) *schema.Set {
	if set := ctxOf(n); set != nil {
		return set
	}
	return p.set
}

func (p *printer) typeError(n *Node, err error) error {
	return &printerTypeError{set: p.nodeContext(n), err: err}
}

// printTypeValue runs a type conversion under the XPath cast's byte-work budget. Public printers
// have no byteStep and retain the ordinary unbudgeted types.Print behavior.
func (p *printer) printTypeValue(v types.Value, f types.Format, pc *types.PrintCtx) (string, error) {
	if p.opts.byteStep == nil {
		return types.Print(v, f, pc)
	}
	// Dynamic type printers charge both conversion work and every converted fragment before
	// growing their builders. Canonical length alone is not a bound: one short xpath1.0 module
	// name can expand to an arbitrarily long XML prefix at every name test.
	value, err := types.Print(v, f, pc, p.opts.byteStep)
	var d *types.Diag
	if errors.As(err, &d) {
		// The conversion charged source work and message construction before returning. Account
		// separately for the diagnostic's retained strings before xml_print_meta can append it to
		// the successful print notices.
		if budgetErr := chargeXPathBytes(typePrintDiagBytes(d), p.opts.byteStep); budgetErr != nil {
			return "", budgetErr
		}
	}
	return value, err
}

func typePrintDiagBytes(d *types.Diag) int {
	if d == nil {
		return 0
	}
	return len(d.Code) + len(d.Msg) + len(d.AppTag) + len(d.Err) + typePrintDiagBytes(d.Logged)
}

func (p *printer) shouldPrint(n *Node) bool { return p.opts.shouldPrint(n, p.flags) }

func (p *printer) format() bool { return !p.opts.Shrink }

// printf handles the printer's fixed %s and %04X formats without staging an
// untrusted string in fmt's buffer before the byte budget sees it.
func (p *printer) printf(format string, a ...any) {
	arg := 0
	for {
		i := strings.IndexByte(format, '%')
		if i < 0 {
			p.buf.appendString(format)
			return
		}
		p.buf.appendString(format[:i])
		format = format[i+1:]
		switch {
		case strings.HasPrefix(format, "s"):
			p.buf.appendString(a[arg].(string))
			format = format[1:]
		case strings.HasPrefix(format, "04X"):
			v := a[arg].(byte)
			p.buf.appendString("00")
			const hex = "0123456789ABCDEF"
			p.buf.appendByte(hex[v>>4])
			p.buf.appendByte(hex[v&15])
			format = format[3:]
		default:
			panic("unsupported printer format")
		}
		arg++
	}
}

// nl is the newline of a formatted print.
func (p *printer) nl() string {
	if p.format() {
		return "\n"
	}
	return ""
}

// indent is the INDENT of the current level.
func (p *printer) indent() string {
	if p.format() {
		return spaces(p.level * 2)
	}
	return ""
}

func spaces(n int) string { return string(bytes.Repeat([]byte{' '}, n)) }

// kids returns the children of n in order.
func kids(n *Node) []*Node { return slices.Collect(n.kids.all()) }

// hasPrintableMeta is node_has_printable_meta.
func hasPrintableMeta(n *Node) bool { return len(n.meta) > 0 }

// leafBase is the base type of the stored value: the selected member for a union.
func leafBase(v types.Value) schema.BaseType {
	for v.Union() != nil {
		v, _ = v.Union().Member()
	}
	return v.Type().Base
}
