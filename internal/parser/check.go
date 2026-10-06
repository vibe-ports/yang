// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/parser_yang.c, src/tree_schema_common.c and
// src/tree_schema.c (BSD-3-Clause, © CESNET).

package parser

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/lyxp"
)

// Statement grammar: the substatements each parse_* function of parser_yang.c
// accepts. Suffix '?': at most once (LY_VCODE_DUPSTMT); '+': YANG 1.1 only
// (LY_VCODE_INCHILDSTMT2). Statements not listed take extension instances only.
const (
	meta    = "description? reference? status? "
	dataDef = " anydata+ anyxml choice container leaf leaf-list list uses "
	body    = " augment deviation extension feature grouping identity notification rpc typedef" + dataDef
	restr   = "description? reference? error-app-tag? error-message?"
	node    = meta + "config? if-feature when? typedef must action+ grouping notification+" + dataDef
)

var grammar = parseGrammar(map[string]string{
	"module":       "yang-version? namespace? prefix? include import organization? contact? description? reference? revision" + body,
	"submodule":    "yang-version? belongs-to? include import organization? contact? description? reference? revision" + body,
	"import":       "prefix? description?+ reference?+ revision-date?",
	"include":      "description?+ reference?+ revision-date?",
	"revision":     "description? reference?",
	"belongs-to":   "prefix?",
	"must":         restr,
	"length":       restr,
	"range":        restr,
	"pattern":      restr + " modifier?+",
	"when":         "description? reference?",
	"anydata":      meta + "config? if-feature mandatory? must when?",
	"anyxml":       meta + "config? if-feature mandatory? must when?",
	"enum":         meta + "if-feature+ value?",
	"bit":          meta + "if-feature+ position?",
	"type":         "base bit enum fraction-digits? length? path? pattern range? require-instance? type",
	"leaf":         meta + "config? default? if-feature mandatory? must type? units? when?",
	"leaf-list":    meta + "config? default+ if-feature max-elements? min-elements? must ordered-by? type? units? when?",
	"refine":       "config? default description? if-feature+ max-elements? min-elements? must mandatory? reference? presence?",
	"typedef":      meta + "default? type? units?",
	"input":        "typedef must+ grouping" + dataDef,
	"output":       "typedef must+ grouping" + dataDef,
	"rpc":          meta + "if-feature input? output? typedef grouping",
	"action":       meta + "if-feature input? output? typedef grouping",
	"notification": meta + "if-feature must+ typedef grouping" + dataDef,
	"grouping":     meta + "typedef action+ grouping notification+" + dataDef,
	"augment":      meta + "if-feature when? case action+ notification+" + dataDef,
	"uses":         meta + "if-feature when? refine augment",
	"case":         meta + "if-feature when?" + dataDef,
	"choice":       meta + "config? if-feature mandatory? when? default? anydata+ anyxml case choice+ container leaf leaf-list list",
	"container":    node + " presence?",
	"list":         node + " key? max-elements? min-elements? ordered-by? unique",
	"argument":     "yin-element?",
	"extension":    meta + "argument?",
	"deviation":    "description? deviate reference?",
	"deviate":      "config? default mandatory? max-elements? min-elements? must type? unique units?",
	"feature":      meta + "if-feature",
	"identity":     meta + "if-feature+ base",
})

const (
	once uint8 = 1 << iota
	v11
)

func parseGrammar(g map[string]string) map[string]map[string]uint8 {
	out := make(map[string]map[string]uint8, len(g))
	for parent, subs := range g {
		m := map[string]uint8{}
		for _, f := range strings.Fields(subs) {
			var fl uint8
			for ; strings.HasSuffix(f, "?") || strings.HasSuffix(f, "+"); f = f[:len(f)-1] {
				if f[len(f)-1] == '?' {
					fl |= once
				} else {
					fl |= v11
				}
			}
			m[f] = fl
		}
		out[parent] = m
	}
	return out
}

var mandatory = map[string][]string{
	"leaf": {"type"}, "leaf-list": {"type"}, "typedef": {"type"}, "import": {"prefix"}, "belongs-to": {"prefix"},
	"module": {"namespace", "prefix"}, "submodule": {"belongs-to"}, "deviation": {"deviate"},
}

// section is enum yang_module_stmt: the order of (sub)module substatements.
var section = map[string]int{"yang-version": 0, "namespace": 0, "prefix": 0, "belongs-to": 0, "include": 1,
	"import": 1, "organization": 2, "contact": 2, "description": 2, "reference": 2, "revision": 3}

// deviateAllows is the per-deviate-type part of parse_deviate.
var deviateAllows = map[string]string{
	"not-supported": "",
	"add":           " config default mandatory max-elements min-elements must unique units ",
	"replace":       " config default mandatory max-elements min-elements type units ",
	"delete":        " default must unique units ",
}

// extOwner: statements with their own exts array, added to ctx->ext_inst when
// they close (YANG_READ_SUBSTMT_NEXT_ITER with EXTS). Extension instances in
// other statements belong to the closest owner above them.
var extOwner = map[string]bool{
	"module": true, "submodule": true, "import": true, "include": true, "revision": true, "must": true,
	"length": true, "range": true, "pattern": true, "when": true, "anydata": true, "anyxml": true, "enum": true,
	"bit": true, "type": true, "leaf": true, "leaf-list": true, "refine": true, "typedef": true, "input": true,
	"output": true, "rpc": true, "action": true, "notification": true, "grouping": true, "augment": true,
	"uses": true, "case": true, "choice": true, "container": true, "list": true, "extension": true,
	"deviate": true, "deviation": true, "feature": true, "identity": true,
}

// frame is an open statement of the checked tree.
type frame struct {
	s    *Stmt
	live bool // read by libyang's typed parser: a YANG statement, or an extension instance resolved later
	seen map[string]bool
	sec  int
	prev string
}

// checker runs parser_yang.c's checks while lexer.stmt reads the text, so
// errors come in libyang's order and at its position.
type checker struct {
	v11       bool
	submodule bool
	name      string
	prefix    string
	imports   map[string]string // prefix → module
	includes  bool
	extDefs   map[string]*Stmt // extension statements of this module
	exts      []*Stmt          // extension instances in ctx->ext_inst order
	ctx       *Context
}

func parentName(kw string) string {
	if kw == "leaf-list" {
		return "llist" // parse_leaflist reports it so
	}
	return kw
}

// child runs the checks libyang makes after reading a substatement keyword
// of f: statement order, allowed child, YANG version, duplicate.
func (c *checker) child(l *lexer, f *frame, kw string, ext bool) error {
	if ext {
		f.prev = "extension instance"
		return nil
	}
	p := f.s.Keyword
	fl, ok := grammar[p][kw]
	if ok && (p == "module" || p == "submodule") { // CHECK_ORDER
		n, ok := section[kw]
		if !ok {
			n = 4
		}
		if n < f.sec {
			return l.errf(ly.SyntaxYang, "Invalid keyword \"%s\", it cannot appear after \"%s\".", kw, f.prev)
		}
		f.sec = n
	}
	f.prev = kw
	switch {
	case !ok:
		return l.errf(ly.SyntaxYang, "Invalid keyword \"%s\" as a child of \"%s\".", kw, parentName(p))
	case fl&v11 != 0 && !c.v11:
		return l.errf(ly.SyntaxYang,
			"Invalid keyword \"%s\" as a child of \"%s\" - the statement is allowed only in YANG 1.1 modules.", kw, p)
	case p == "identity" && kw == "base" && f.seen[kw] && !c.v11:
		return l.errf(ly.SyntaxYang, "Identity can be derived from multiple base identities only in YANG 1.1 modules")
	case p == "deviate" && !strings.Contains(deviateAllows[f.s.Arg], " "+kw+" "):
		return l.errf(ly.SyntaxYang, "Deviate \"%s\" does not support keyword \"%s\".", f.s.Arg, kw)
	case f.seen[kw] && (fl&once != 0 || p == "deviate" && f.s.Arg == "replace" && kw == "default"):
		return l.errf(ly.SyntaxYang, "Duplicate keyword \"%s\".", kw)
	}
	f.seen[kw] = true
	if kw == "include" && p == "submodule" && c.v11 && c.ctx != nil && c.ctx.Warn != nil {
		c.ctx.Warn("YANG version 1.1 expects all includes in main module, includes in submodules (" + f.s.Arg +
			") are not necessary.")
	}
	return nil
}

func (l *lexer) inval(s *Stmt) error {
	return l.errf(ly.SyntaxYang, "Invalid value \"%s\" of \"%s\".", s.Arg, s.Keyword)
}

func oneOf(a string, vals ...string) bool {
	for _, v := range vals {
		if a == v {
			return true
		}
	}
	return false
}

// arg checks the argument of s (a substatement of pf, nil for the module)
// just after it was read: the value checks of the parse_* functions.
func (c *checker) arg(l *lexer, pf *frame, s *Stmt) error {
	a := s.Arg
	if a == "" && emptyArgWarned[s.Keyword] && s.ExtPrefix == "" && c.ctx != nil && c.ctx.Warn != nil { // CHECK_NONEMPTY
		c.ctx.Warn("Empty argument of " + s.Keyword + " statement does not make sense.")
	}
	switch s.Keyword {
	case "module", "submodule":
		if pf == nil {
			c.name, c.submodule = a, s.Keyword == "submodule"
		}
	case "yang-version":
		if !oneOf(a, "1", "1.1") {
			return l.inval(s)
		}
		c.v11 = a == "1.1"
	case "config", "mandatory", "require-instance", "yin-element":
		if !oneOf(a, "true", "false") {
			return l.inval(s)
		}
	case "status":
		if !oneOf(a, "current", "deprecated", "obsolete") {
			return l.inval(s)
		}
	case "ordered-by":
		if !oneOf(a, "system", "user") {
			return l.inval(s)
		}
	case "modifier":
		if a != "invert-match" {
			return l.inval(s)
		}
	case "deviate":
		if _, ok := deviateAllows[a]; !ok {
			return l.inval(s)
		}
	case "revision", "revision-date": // lys_check_date
		if len(a) != 10 {
			return l.errf(ly.SyntaxYang, "Invalid length %d of a %s.", len(a), s.Keyword)
		}
		if _, err := time.Parse("2006-01-02", a); err != nil || !allDigits(a[:4]+a[5:7]+a[8:]) {
			return l.inval(s)
		}
	case "min-elements", "max-elements": // parse_minelements, parse_maxelements
		if s.Keyword == "max-elements" && a == "unbounded" {
			break
		}
		if a == "" || !allDigits(a) || a[0] == '0' && (len(a) > 1 || s.Keyword == "max-elements") {
			return l.inval(s)
		}
		if n, err := strconv.ParseUint(a, 10, 64); err != nil || n > math.MaxUint32 {
			return l.errf(ly.SyntaxYang, "Value \"%s\" is out of \"%s\" bounds.", a, s.Keyword)
		}
	case "fraction-digits": // parse_type_fracdigits
		if a == "" || a[0] == '0' || !allDigits(a) {
			return l.inval(s)
		}
		if n, err := strconv.ParseUint(a, 10, 64); err != nil || n > 18 {
			return l.errf(ly.SyntaxYang, "Value \"%s\" is out of \"%s\" bounds.", a, s.Keyword)
		}
	case "value", "position": // parse_type_enum_value_pos
		if _, ok := enumValue(s); !ok {
			return l.inval(s)
		}
	case "enum", "bit":
		if s.Keyword == "enum" { // lysp_check_enum_name
			if a == "" {
				return l.errf(ly.SyntaxYang, "Enum name must not be zero-length.")
			}
			if isCSpace(a[0]) || isCSpace(a[len(a)-1]) {
				return l.errf(ly.SyntaxYang, "Enum name must not have any leading or trailing whitespaces (\"%s\").", a)
			}
			for u := 0; u < len(a); u++ {
				if (a[u] < 0x20 || a[u] == 0x7f) && c.ctx != nil && c.ctx.Warn != nil { // iscntrl
					c.ctx.Warn("Control characters in enum name should be avoided (\"" + a + "\", character number " + strconv.Itoa(u+1) + ").")
					break
				}
			}
		}
		k := s.Keyword + " " + a
		if pf.seen[k] { // CHECK_UNIQUENESS
			return l.errf(ly.SyntaxYang, "Duplicate identifier \"%s\" of %s statement.", a, s.Keyword)
		}
		pf.seen[k] = true
	case "include":
		c.includes = true
		if a == c.name || c.ctx != nil && c.ctx.Module != nil && c.ctx.Module(a) {
			return l.errf(ly.Semantics, "Name collision between module and submodule of name \"%s\".", a)
		}
	case "belongs-to":
		if c.ctx != nil && a != c.ctx.Main {
			return l.errf(ly.SyntaxYang, "Submodule \"belongs-to\" value \"%s\" does not match its module name \"%s\".",
				a, c.ctx.Main)
		}
	case "extension":
		c.extDefs[a] = s
	}
	return nil
}

// close runs the checks libyang makes after the ';' or '}' of f's statement
// (a substatement of pf): mandatory substatements, prefix collisions, and
// registering the extension instances of an owner.
func (c *checker) close(l *lexer, pf, f *frame) error {
	s := f.s
	if s.ExtPrefix == "" {
		for _, m := range mandatory[s.Keyword] {
			if !f.seen[m] {
				return l.errf(ly.SyntaxYang, "Missing mandatory keyword \"%s\" as a child of \"%s\".", m, s.Keyword)
			}
		}
		if s.Keyword == "path" && pf != nil && pf.s.Keyword == "type" { // parse_type, after parse_text_field
			ext := c.ctx != nil && c.ctx.LeafrefExtended
			if _, msg := lyxp.ParsePath(s.Arg, lyxp.Opts{Begin: lyxp.BeginEither, Prefix: lyxp.PrefixOptional, Pred: lyxp.PredLeafref, Leafref: true, Extended: ext}); msg != "" {
				return l.errf(ly.XPath, "%s", msg)
			}
		}
		if pf == nil && c.ctx != nil && c.ctx.SubmoduleOf != nil { // end of parse_module / parse_submodule
			switch owner := c.ctx.SubmoduleOf(s.Arg); {
			case owner == "":
			case !c.submodule:
				return l.errf(ly.Semantics, "Name collision between module and submodule of name \"%s\".", s.Arg)
			case owner != c.ctx.Main:
				return l.errf(ly.Semantics, "Name collision between submodules of name \"%s\".", s.Arg)
			}
		}
		if s.Keyword == "prefix" && pf != nil { // lysp_check_prefix after parse_text_field
			if pf.s.Keyword != "import" {
				c.prefix = s.Arg
			} else if s.Arg == c.prefix {
				return l.errf(ly.Reference, "Prefix \"%s\" already used as module prefix.", s.Arg)
			} else if mod := c.imports[s.Arg]; mod != "" {
				return l.errf(ly.Reference, "Prefix \"%s\" already used to import \"%s\" module.", s.Arg, mod)
			} else {
				c.imports[s.Arg] = pf.s.Arg
			}
		}
	}
	if (s.ExtPrefix != "" || extOwner[s.Keyword]) && len(s.Subs) > 0 {
		c.exts = appendOwned(c.exts, s, s.ExtPrefix != "")
	}
	return nil
}

// ExtInstances returns the extension instances of a parsed (sub)module in
// libyang's ctx->ext_inst order (owners as they close), the order in which
// lysp_resolve_ext_instance_records resolves them.
func ExtInstances(root *Stmt) []*Stmt {
	var out []*Stmt
	for _, o := range ExtOwners(root) {
		out = append(out, OwnedExts(o)...)
	}
	return out
}

// ExtOwners returns the statements owning a non-empty exts array, in ctx->ext_inst order:
// the arrays lysp_resolve_ext_instance_records walks.
func ExtOwners(root *Stmt) []*Stmt {
	var out []*Stmt
	var walk func(s *Stmt)
	walk = func(s *Stmt) {
		for _, c := range s.Subs {
			if c.ExtPrefix != "" || s.ExtPrefix == "" { // statements libyang's parser reads
				walk(c)
			}
		}
		if (s.ExtPrefix != "" || extOwner[s.Keyword]) && len(OwnedExts(s)) > 0 {
			out = append(out, s)
		}
	}
	walk(root)
	return out
}

// appendOwned appends the extension instances whose exts array is s's.
func appendOwned(exts []*Stmt, s *Stmt, isExt bool) []*Stmt {
	for _, c := range s.Subs {
		switch {
		case c.ExtPrefix != "":
			exts = append(exts, c)
		case !isExt && !extOwner[c.Keyword]:
			exts = appendOwned(exts, c, false)
		}
	}
	return exts
}

// resolveExts is the prefix part of lysp_resolve_ext_instance_records /
// lysp_ext_find_definition, run after parsing: the prefix must be the
// module's or an imported one, and an extension of this module must be
// defined in it. Definitions in imported modules and submodules are the
// compiler's job. libyang reports a schema path here, not a line.
//
// For a definition found here, the argument is checked as
// lysp_ext_instance_resolve_argument does, in the same loop.
func (c *checker) resolveExts(file string) error {
	for _, e := range c.exts {
		name := e.ExtPrefix + ":" + e.Keyword
		def := c.extDefs[e.Keyword]
		switch {
		case e.ExtPrefix == c.prefix:
			if def == nil && !c.submodule && !c.includes {
				return &Error{File: file, Code: ly.Reference,
					Msg: "Extension definition of extension instance \"" + name + "\" not found."}
			}
		case c.imports[e.ExtPrefix] == "":
			return &Error{File: file, Code: ly.Reference,
				Msg: "Invalid prefix \"" + e.ExtPrefix + "\" used for extension instance identifier."}
		default:
			def = nil // defined in an imported module: the compiler checks the argument
		}
		if arg := subStmt(def, "argument"); arg != nil && !e.HasArg {
			elem := ""
			if subArg(arg, "yin-element") == "true" {
				elem = "element "
			}
			return &Error{File: file, Code: ly.Semantics,
				Msg: "Extension instance \"" + name + "\" missing argument " + elem + "\"" + arg.Arg + "\"."}
		}
	}
	return nil
}

func subStmt(s *Stmt, kw string) *Stmt {
	if s == nil {
		return nil
	}
	for _, c := range s.Subs {
		if c.ExtPrefix == "" && c.Keyword == kw {
			return c
		}
	}
	return nil
}

func allDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

func isCSpace(c byte) bool { return c == ' ' || c >= '\t' && c <= '\r' }

// enumValue is the numeric part of parse_type_enum_value_pos, with strtoll /
// strtoull semantics (leading whitespace and one sign are accepted after the
// first-character checks).
func enumValue(s *Stmt) (int64, bool) {
	a := s.Arg
	if a == "" || a[0] == '+' || a[0] == '0' && len(a) > 1 || s.Keyword == "position" && strings.HasPrefix(a, "-0") {
		return 0, false
	}
	t := strings.TrimLeft(a, " \t\n\v\f\r")
	neg := strings.HasPrefix(t, "-")
	if neg || strings.HasPrefix(t, "+") {
		t = t[1:]
	}
	if !allDigits(t) {
		return 0, false
	}
	n, err := strconv.ParseUint(t, 10, 64)
	switch {
	case s.Keyword == "value":
		if err != nil || n > math.MaxInt32+1 || !neg && n > math.MaxInt32 {
			return 0, false
		}
		if neg {
			return -int64(n), true
		}
	case err != nil || n > math.MaxUint32 || neg && n != 0: // strtoull negates
		return 0, false
	}
	return int64(n), true //nolint:gosec // n <= MaxUint32
}

// emptyArgWarned are the statements whose empty argument libyang's YANG parser warns about
// (CHECK_NONEMPTY in parse_restr, parse_when, parse_refine, parse_augment, parse_deviation).
var emptyArgWarned = map[string]bool{"must": true, "length": true, "range": true, "when": true, "refine": true,
	"augment": true, "deviation": true}
