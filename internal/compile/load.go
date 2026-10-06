// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/context.c, src/tree_schema.c,
// src/tree_schema_common.c and src/parser_yang.c (BSD-3-Clause, © CESNET).

// Package compile turns parsed YANG modules into the compiled schema. This
// part is the loader: search, parse, imports and includes (design 06 §1).
package compile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"

	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/models"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/schema"
)

var (
	// ErrBudget is wrapped by errors caused by an exceeded resource limit.
	ErrBudget = errors.New("resource budget exceeded")
	// ErrUnsupported is wrapped by errors for input this port does not handle (yet).
	ErrUnsupported = errors.New("not supported")
)

// Options are the libyang context flags the loader reads.
type Options struct {
	AllImplemented    bool // LY_CTX_ALL_IMPLEMENTED
	NoYangLibrary     bool // LY_CTX_NO_YANGLIBRARY
	DisableSearchdirs bool // LY_CTX_DISABLE_SEARCHDIRS
	PreferSearchdirs  bool // LY_CTX_PREFER_SEARCHDIRS
	// Loader is the import callback (ly_module_imp_clb): YANG text of the
	// module (submodule "" ) or of the submodule, ok false when it has none.
	// It runs inside Load and must not call back into the Context.
	Loader        func(module, revision, submodule, subRevision string) (src []byte, ok bool)
	MaxSearchDirs int           // directories opened per search, default 10 000 (U-0022)
	Parse         parser.Budget // per (sub)module text; MaxBytes also bounds reading a file
	Budget        Budget        // compile limits per Load (design 06 §5)
}

// Level is a diagnostic's log level.
type Level uint8

// Log levels.
const (
	LevelError Level = iota
	LevelWarning
)

// Diagnostic is one message libyang logs against the context.
type Diagnostic struct {
	Phase      string // "parse" (ly_ctx_load_module) or "compile" (ly_ctx_compile), design 06 §1.6
	Level      Level
	Err        string  // LY_ERR name, e.g. "LY_EVALID"; "LY_SUCCESS" for warnings
	Code       ly.Code // vecode (LYVE_SUCCESS for LOGERR)
	SchemaPath string
	Line       int
	Msg        string
}

func (d *Diagnostic) Error() string { return d.Msg }

// rc is a libyang LY_ERR return code used as a Go error.
type rc string

func (e rc) Error() string { return string(e) }

const (
	eValid    rc = "LY_EVALID"
	eInval    rc = "LY_EINVAL"
	eNotFound rc = "LY_ENOTFOUND"
	eDenied   rc = "LY_EDENIED"
	eExist    rc = "LY_EEXIST"
)

func rcName(err error) string {
	var r rc
	if errors.As(err, &r) {
		return string(r)
	}
	return "LY_EOTHER"
}

// lys_module.latest_revision flags.
const (
	latestRev        = 0x01 // LYS_MOD_LATEST_REV
	latestSearchdirs = 0x02 // LYS_MOD_LATEST_SEARCHDIRS
	latestImpClb     = 0x04 // LYS_MOD_LATEST_IMPCLB
	importedRev      = 0x08 // LYS_MOD_IMPORTED_REV
)

// pmod is the part of struct lysp_module that modules and submodules share.
type pmod struct {
	Parsed   *parser.Module
	File     string    // path of the file it was read from ("" from the Loader)
	Imports  []*Module // per Parsed.Imports
	Includes []*Include
	parsing  bool
	mod      *schema.Module // the compiled module it belongs to (lysp_module.mod), bound by compile
}

// Module is a module in the context (struct lys_module).
type Module struct {
	pmod
	Name, Revision, Namespace string
	Implemented               bool
	latest                    uint8
}

// Submodule is a parsed submodule (struct lysp_submodule).
type Submodule struct {
	pmod
	Name, Revision string
	Main           *Module
	latest         uint8 // 0, 1 or 2 (latest found in the search directories)
}

// Include is an include of a module or submodule; Injected marks a YANG 1.0
// submodule's include copied into its main module.
type Include struct {
	Name, Rev string
	Sub       *Submodule
	Injected  bool
}

// Context is the loader state of a libyang context.
type Context struct {
	opts    Options
	dirs    []fs.FS
	Modules []*Module // ctx->modules, in insertion order
	diags   []Diagnostic
	// per Load (lys_glob_unres)
	creating, implementing []*Module
	nodes                  int        // schema nodes compiled (Budget.MaxNodes)
	types                  int        // compiled types and union member slots (Budget.MaxTypes)
	typeCache              *typeCache // compiled typedefs (design 06 §2.3), created by the first compile
}

// internal_modules[] of context.c.
var internalModules = []struct {
	name, rev   string
	implemented bool
}{
	{"ietf-inet-types", "2025-12-22", false},
	{"ietf-yang-types", "2025-12-22", false},
	{"ietf-yang-metadata", "2016-08-05", true},
	{"yang", "2025-01-29", true},
	{"default", "2025-06-18", true},
	{"ietf-yang-schema-mount", "2019-01-14", true},
	{"ietf-yang-structure-ext", "2020-06-17", false},
	{"ietf-datastores", "2018-02-14", true},
	{"ietf-yang-library", "2019-01-04", true},
}

// NewContext is ly_ctx_new with libyang's module directory (embedded) as the
// first search directory, followed by dirs, and the internal modules loaded.
// Option timing: the internal modules are loaded from the embedded directory
// only, with AllImplemented, MaxSearchDirs and Parse applied; dirs, Loader,
// DisableSearchdirs and PreferSearchdirs take effect after them (the oracle's
// ly_ctx_new followed by ly_ctx_set_searchdir).
func NewContext(opts Options, dirs ...fs.FS) (*Context, []Diagnostic, error) {
	if opts.MaxSearchDirs <= 0 {
		opts.MaxSearchDirs = 10000
	}
	if opts.Parse.MaxBytes <= 0 {
		opts.Parse.MaxBytes = 64 << 20 // the parser's default
	}
	c := &Context{opts: Options{AllImplemented: opts.AllImplemented, MaxSearchDirs: opts.MaxSearchDirs, Parse: opts.Parse},
		dirs: []fs.FS{models.Libyang}}
	n := len(internalModules)
	if opts.NoYangLibrary {
		n -= 2
	}
	for _, im := range internalModules[:n] {
		m, err := c.parseLoad(im.name, im.rev)
		if err == nil && (im.implemented || opts.AllImplemented) {
			err = c.implement(m)
		}
		if err != nil {
			return nil, c.diags, err
		}
	}
	diags := c.diags
	c.opts, c.diags = opts, nil
	c.dirs = append(c.dirs, dirs...)
	for _, m := range c.Modules { // ly_ctx_set_searchdir: possibly newer revisions available
		m.latest &^= latestSearchdirs
	}
	return c, diags, nil
}

// Load is the parse phase of ly_ctx_load_module: lys_parse_load and
// implementing the module. On error the modules created or implemented by
// this call are reverted (lys_unres_glob_revert); other flag changes stay,
// as in libyang. features nil leaves them untouched; setting features is
// not ported yet (design 06 C1b) and fails with ErrUnsupported.
func (c *Context) Load(name, rev string, features []string) (*Module, []Diagnostic, error) {
	if features != nil {
		return nil, nil, fmt.Errorf("%w: setting features (design 06 C1b)", ErrUnsupported)
	}
	c.diags, c.creating, c.implementing = nil, nil, nil
	c.nodes, c.types = 0, 0
	m, err := c.parseLoad(name, rev)
	if err == nil && !m.Implemented { // _lys_set_implemented
		err = c.implement(m)
		for i := 0; err == nil && c.opts.AllImplemented && i < len(c.creating); i++ {
			if !c.creating[i].Implemented {
				err = c.implement(c.creating[i])
			}
		}
	}
	if err != nil {
		for _, m := range c.implementing {
			m.Implemented = false
		}
		c.Modules = slices.DeleteFunc(c.Modules, func(m *Module) bool { return slices.Contains(c.creating, m) })
		return nil, c.diags, err
	}
	return m, c.diags, nil
}

// implement is the loader part of lys_implement.
func (c *Context) implement(m *Module) error {
	if o := c.implemented(m.Name); o != nil {
		return c.logErr(eDenied, "Module \"%s@%s\" is already implemented in revision \"%s\".",
			m.Name, orNone(m.Revision), orNone(o.Revision))
	}
	m.Implemented = true
	c.implementing = append(c.implementing, m)
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "<none>"
	}
	return s
}

// --- logging (LOGVAL, LOGERR, LOGWRN) ---

func (c *Context) logVal(code ly.Code, line int, format string, a ...any) error {
	d := Diagnostic{Phase: "parse", Level: LevelError, Err: string(eValid), Code: code, Line: line, Msg: fmt.Sprintf(format, a...)}
	c.diags = append(c.diags, d)
	return eValid
}

// logPath is LOGVAL with a log location path (ly_log_location), no line.
func (c *Context) logPath(code ly.Code, path, format string, a ...any) error {
	d := Diagnostic{Phase: "parse", Level: LevelError, Err: string(eValid), Code: code, SchemaPath: path, Msg: fmt.Sprintf(format, a...)}
	c.diags = append(c.diags, d)
	return eValid
}

func (c *Context) logErr(err error, format string, a ...any) error {
	c.diags = append(c.diags, Diagnostic{Phase: "parse", Level: LevelError, Err: rcName(err), Msg: fmt.Sprintf(format, a...)})
	return err
}

func (c *Context) warn(format string, a ...any) {
	c.diags = append(c.diags, Diagnostic{Phase: "parse", Level: LevelWarning, Err: "LY_SUCCESS", Msg: fmt.Sprintf(format, a...)})
}

// --- context lookups (context.c) ---

func (c *Context) module(name, rev string) *Module { // ly_ctx_get_module
	for _, m := range c.Modules {
		if m.Name == name && m.Revision == rev {
			return m
		}
	}
	return nil
}

func (c *Context) latestBy(match func(*Module) bool) *Module { // ly_ctx_get_module_latest_by
	for _, m := range c.Modules {
		if match(m) && m.latest&latestRev != 0 {
			return m
		}
	}
	return nil
}

func (c *Context) latest(name string) *Module {
	return c.latestBy(func(m *Module) bool { return m.Name == name })
}

func (c *Context) implemented(name string) *Module { // ly_ctx_get_module_implemented
	for _, m := range c.Modules {
		if m.Name == name && m.Implemented {
			return m
		}
	}
	return nil
}

// submoduleLatest is ly_ctx_get_submodule2_latest (m != nil) or ly_ctx_get_submodule_latest.
func (c *Context) submoduleLatest(m *Module, name string) *Submodule {
	mods := c.Modules
	if m != nil {
		mods = []*Module{m}
	}
	for _, m := range mods {
		for _, inc := range m.Includes {
			// _ly_ctx_get_submodule2 with latest: the latest revision, or one without revision
			if inc.Sub != nil && inc.Sub.Name == name && (inc.Sub.latest != 0 || inc.Sub.Revision == "") {
				return inc.Sub
			}
		}
	}
	return nil
}

// --- lys_parse_load and its helpers (tree_schema_common.c) ---

// withoutRevision is lys_get_module_without_revision.
func (c *Context) withoutRevision(name string) *Module {
	for _, m := range c.Modules {
		if m.Name == name && m.latest&importedRev != 0 {
			return m
		}
	}
	if m := c.implemented(name); m != nil {
		return m
	}
	return c.latest(name)
}

// parseLoad is lys_parse_load: the module from the context or loaded.
func (c *Context) parseLoad(name, rev string) (*Module, error) {
	var m, cand *Module
	if rev != "" {
		m = c.module(name, rev)
	} else if m = c.withoutRevision(name); m != nil && !m.Implemented && m.latest&importedRev == 0 {
		cand, m = m, nil // look for a newer revision first
	}
	if m == nil {
		var err error
		if m, err = c.loadModule(name, rev, cand); err != nil {
			return nil, err
		}
		switch {
		case m == nil:
			cand.latest |= latestSearchdirs
			m = cand
		case rev == "" && m.latest&latestRev != 0:
			m.latest |= latestSearchdirs
		}
	}
	if m.parsing {
		return nil, c.logVal(ly.Reference, 0, "A circular dependency (import) for module \"%s\".", m.Name)
	}
	return m, nil
}

// loadModule is lys_load_mod_from_clb_or_file. A nil module without error
// means only cand (the latest in the context) was found.
func (c *Context) loadModule(name, rev string, cand *Module) (*Module, error) {
	clbUsed := c.opts.Loader == nil || cand != nil && cand.latest&latestImpClb != 0
	dirsUsed := c.opts.DisableSearchdirs || cand != nil && cand.latest&latestSearchdirs != 0
	var m *Module
	for found := false; !found && (!clbUsed || !dirsUsed); {
		if (!c.opts.PreferSearchdirs || dirsUsed) && !clbUsed {
			if src, ok := c.opts.Loader(name, rev, "", ""); ok {
				var err error
				if m, err = c.parseModule(src, loadData{name: name, rev: rev}); err != nil {
					return nil, err
				}
				found = true
			}
			clbUsed = true
			if m != nil && rev == "" {
				m.latest |= latestImpClb
			}
		} else if !dirsUsed {
			f, err := c.search(name, rev)
			if err != nil {
				return nil, err
			}
			if f != nil {
				src, err := c.read(f)
				if err != nil {
					return nil, err
				}
				if m, err = c.parseModule(src, loadData{name: name, rev: rev, path: f.name}); err != nil {
					return nil, err
				}
				found = true
			}
			dirsUsed = true
			if m != nil && rev == "" {
				m.latest |= latestSearchdirs
			}
		}
	}
	if m == nil && cand == nil {
		r := rev
		if r == "" {
			r = "<any>"
		}
		_ = c.logVal(ly.Reference, 0, "Loading \"%s@%s\" module failed, not found.", name, r)
		return nil, eNotFound
	}
	return m, nil
}

// read returns the text of a found file; YIN is not supported (U-0021).
func (c *Context) read(f *file) ([]byte, error) {
	if f.yin {
		return nil, fmt.Errorf("%w: YIN module file %q (U-0021)", ErrUnsupported, f.name)
	}
	fd, err := f.fsys.Open(f.name)
	if err != nil {
		return nil, c.logErr(eInval, "Unable to create input handler for filepath %s.", f.name)
	}
	defer fd.Close() //nolint:errcheck // read-only
	src, err := io.ReadAll(io.LimitReader(fd, int64(c.opts.Parse.MaxBytes)+1))
	switch {
	case err != nil:
		return nil, c.logErr(eInval, "Unable to create input handler for filepath %s.", f.name)
	case len(src) > c.opts.Parse.MaxBytes:
		return nil, fmt.Errorf("%w: module file %q is larger than %d bytes", ErrBudget, f.name, c.opts.Parse.MaxBytes)
	}
	return src, nil
}

// loadData is struct lysp_load_module_data.
type loadData struct{ name, rev, path string }

// pctx is the part of the main module's parser context the loader needs.
type pctx struct {
	main   *Module
	parsed []*Submodule // parsed_mods; nil stands for the main module
	done   []*Submodule // submodules in the order their parser contexts were merged
}

// parse parses src as a module (main "") or as a submodule of main, with
// the context checks of the YANG parser, and logs a parse error the way
// libyang reports it.
func (c *Context) parse(d loadData, src []byte, main string) (*parser.Module, *parser.Stmt, error) {
	pc := &parser.Context{Submodule: main != "", Main: main,
		Module: func(name string) bool { return c.latest(name) != nil },
		SubmoduleOf: func(name string) string {
			if s := c.submoduleLatest(nil, name); s != nil {
				return s.Main.Name
			}
			return ""
		},
		Warn: func(msg string) { c.warn("%s", msg) }}
	st, err := parser.ParseIn(pc, d.path, src, &c.opts.Parse)
	if err != nil {
		var pe *parser.Error
		switch {
		case !errors.As(err, &pe):
			return nil, nil, err
		case errors.Is(err, parser.ErrBudget):
			return nil, nil, fmt.Errorf("%w: %s", ErrBudget, pe.Msg)
		case errors.Is(err, parser.ErrKind):
			_ = c.logErr(eDenied, "%s", pe.Msg)
			return nil, nil, eInval
		}
		_ = c.logVal(pe.Code, pe.Pos.Line, "%s", pe.Msg)
		if !pe.Submodule && pe.Module != "" {
			err = &namedErr{pe.Module}
		} else {
			err = eValid
		}
		return nil, nil, err
	}
	pm, err := parser.Build(st)
	if err != nil {
		return nil, nil, eValid
	}
	return pm, st, nil
}

// namedErr is LY_EVALID from a module whose name was parsed before the error.
type namedErr struct{ name string }

func (e *namedErr) Error() string { return string(eValid) }
func (e *namedErr) Unwrap() error { return eValid }

// parseModule is lys_parse_in; it returns nil without error when the module
// is a not newer revision of a context module (LY_EEXIST).
func (c *Context) parseModule(src []byte, d loadData) (m *Module, err error) {
	name := ""
	defer func() {
		// lys_parse_in cleanup: tell which module failed when the error has no path
		if err != nil && name != "" && len(c.diags) > 0 && !errors.Is(err, ErrBudget) && !errors.Is(err, ErrUnsupported) {
			if e := c.diags[len(c.diags)-1]; e.SchemaPath == "" || e.Line != 0 {
				_ = c.logErr(rc("LY_EOTHER"), "Parsing module \"%s\" failed.", name)
			}
		}
	}()
	pm, st, err := c.parse(d, src, "")
	if err != nil {
		var ne *namedErr
		if errors.As(err, &ne) {
			name = ne.name
		}
		return nil, err
	}
	name = st.Arg
	m = &Module{Name: st.Arg, Namespace: pm.Namespace, pmod: pmod{Parsed: pm}}
	m.Revision = c.lastRevision(pm.Revisions, "module", m.Name)
	latest := c.latest(m.Name)
	switch {
	case latest == nil:
		m.latest = latestRev
	case m.Revision != "" && (latest.Revision == "" || m.Revision > latest.Revision):
		m.latest = latest.latest & (latestRev | latestSearchdirs)
	default:
		latest = nil
	}
	if exists, err := c.checkLoadData(d, m.Name, m.Revision, m.latest != 0); err != nil || exists {
		return nil, err
	}
	if dup := c.module(m.Name, m.Revision); dup != nil {
		return dup, nil // already in the context
	}
	if dup := c.latestBy(func(o *Module) bool { return o.Namespace == m.Namespace }); dup != nil && dup.Revision == m.Revision {
		return nil, c.logErr(eInval, "Two different modules (\"%s\" and \"%s\") have the same namespace \"%s\".",
			dup.Name, m.Name, m.Namespace)
	}
	if d.path != "" {
		c.checkFilename(m.Name, m.Revision, d.path)
		m.File = d.path
	}
	if latest != nil {
		latest.latest &^= latestRev | latestSearchdirs
	}
	c.creating = append(c.creating, m)
	c.Modules = append(c.Modules, m)
	p := &pctx{main: m, parsed: []*Submodule{nil}}
	if err := c.resolveImportsIncludes(p, &m.pmod, pm, m.Name); err != nil {
		return nil, err
	}
	if err := c.resolveExts(p); err != nil {
		return nil, err
	}
	if err := c.checkDups(p); err != nil {
		return nil, err
	}
	return m, nil
}

// lastRevision is lysp_last_revision with its warnings.
func (c *Context) lastRevision(revs []*parser.Revision, kind, name string) string {
	if len(revs) == 0 {
		return ""
	}
	last := revs[0].Date
	for u := 0; u < len(revs)-1; u++ {
		a, b := revs[u].Date, revs[u+1].Date
		if a < b {
			c.warn("Older revision %s found after a newer revision %s in %s \"%s\".", a, b, kind, name)
			if b > last {
				last = b
			}
		} else if a == b {
			c.warn("Duplicate revision %s in %s \"%s\".", a, kind, name)
		}
	}
	return last
}

// checkLoadData is lysp_load_module_data_check; exists reports LY_EEXIST.
func (c *Context) checkLoadData(d loadData, name, rev string, latest bool) (exists bool, err error) {
	if d.name != "" && d.name != name {
		return false, c.logErr(eInval, "Unexpected module \"%s\" parsed instead of \"%s\".", name, d.name)
	}
	if d.rev != "" {
		if rev != d.rev {
			return false, c.logErr(eInval, "Module \"%s\" parsed with the wrong revision (\"%s\" instead \"%s\").",
				name, revOrNone(rev), d.rev)
		}
	} else if !latest {
		return true, nil
	}
	// The belongs-to check here never fails (the parser checked it) and the
	// include-cycle check never fires: yang_parse_submodule clears parsing.
	if d.path != "" {
		c.checkFilename(name, rev, d.path)
	}
	return false, nil
}

// checkFilename is ly_check_module_filename.
func (c *Context) checkFilename(name, rev, file string) {
	base := path.Base(file)
	at, dot := -1, -1
	for i := range len(base) {
		if base[i] == '@' && at < 0 {
			at = i
		}
		if base[i] == '.' {
			dot = i
		}
	}
	if len(base) < len(name) || base[:len(name)] != name || at >= 0 && at != len(name) || at < 0 && dot != len(name) {
		c.warn("File name \"%s\" does not match module name \"%s\".", base, name)
	}
	if at >= 0 {
		fr := ""
		if dot > at {
			fr = base[at+1 : dot]
		}
		if rev == "" || len(fr) != 10 || fr != rev {
			c.warn("File name \"%s\" does not match module revision \"%s\".", base, revOrNone(rev))
		}
	}
}

func revOrNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// resolveImportsIncludes is lysp_resolve_import_include for the module or
// submodule pm (named name) of the parse context p.
func (c *Context) resolveImportsIncludes(p *pctx, pm *pmod, parsed *parser.Module, name string) error {
	pm.parsing = true
	pm.Imports = make([]*Module, len(parsed.Imports))
	for u, imp := range parsed.Imports {
		m, err := c.parseLoad(imp.Name, imp.RevisionDate)
		if err != nil {
			return err
		}
		pm.Imports[u] = m
		if imp.RevisionDate == "" {
			// later revision-less imports must bind to the same revision
			m.latest |= importedRev
		}
		if slices.Contains(pm.Imports[:u], m) {
			c.warn("Single revision of the module \"%s\" imported twice.", imp.Name)
		}
	}
	pm.Includes = nil
	for _, inc := range parsed.Includes {
		pm.Includes = append(pm.Includes, &Include{Name: inc.Name, Rev: inc.RevisionDate})
	}
	if err := c.loadSubmodules(p, pm, parsed.Submodule, name); err != nil {
		return err
	}
	pm.parsing = false
	return nil
}

// loadSubmodules is lysp_load_submodules.
func (c *Context) loadSubmodules(p *pctx, pm *pmod, isSub bool, name string) error {
	for u := 0; u < len(pm.Includes); u++ { // the main module's includes may grow (injection)
		inc := pm.Includes[u]
		if inc.Sub != nil {
			continue
		}
		included := true
		if isSub {
			found, err := c.mainSubmodule(p, inc, name)
			switch {
			case err != nil:
				return err
			case !found:
				included = false
			case inc.Rev != "" || inc.Sub.latest == 2:
				continue
			}
		}
		found, err := c.parsedSubmodule(p, inc, name)
		if err != nil {
			return err
		}
		if found && (inc.Sub.latest == 2 || inc.Sub.parsing) {
			continue
		}
		sub, err := c.loadSubmodule(p, inc.Name, inc.Rev, inc.Sub, name)
		if err != nil {
			return err
		}
		if sub != nil {
			inc.Sub = sub
			if !included { // lysp_inject_submodule
				if i := slices.IndexFunc(p.main.Includes, func(i *Include) bool { return i.Name == inc.Name }); i >= 0 {
					p.main.Includes[i].Sub = sub
				} else {
					p.main.Includes = append(p.main.Includes, &Include{Name: inc.Name, Rev: inc.Rev, Sub: sub, Injected: true})
				}
			}
		}
	}
	return nil
}

// mainSubmodule is lysp_main_pmod_get_submodule.
func (c *Context) mainSubmodule(p *pctx, inc *Include, name string) (bool, error) {
	for _, mi := range p.main.Includes {
		if mi.Name != inc.Name {
			continue
		}
		if inc.Rev != "" && inc.Rev != mi.Rev {
			return false, c.logVal(ly.Reference, 0,
				"Submodule %s includes different revision (%s) of the submodule %s:%s included by the main module %s.",
				name, inc.Rev, mi.Name, mi.Rev, p.main.Name)
		}
		inc.Sub = mi.Sub
		return inc.Sub != nil, nil
	}
	if p.main.Parsed.Version == "1.1" {
		return false, c.logVal(ly.Reference, 0, "YANG 1.1 requires all submodules to be included from main module. "+
			"But submodule \"%s\" includes submodule \"%s\" which is not included by main module \"%s\".",
			name, inc.Name, p.main.Name)
	}
	return false, nil
}

// parsedSubmodule is lysp_parsed_mods_get_submodule (the last parsed
// (sub)module is skipped, as in libyang).
func (c *Context) parsedSubmodule(p *pctx, inc *Include, name string) (bool, error) {
	for _, s := range p.parsed[:len(p.parsed)-1] {
		if s == nil || s.Name != inc.Name {
			continue
		}
		if inc.Rev != "" && s.Revision != "" && inc.Rev != s.Revision {
			cur := p.main.Name // PARSER_CUR_PMOD(pctx)->mod->name
			return false, c.logVal(ly.Reference, 0,
				"Submodule %s includes different revision (%s) of the submodule %s:%s included by the main module %s.",
				name, inc.Rev, s.Name, s.Revision, cur)
		}
		inc.Sub = s
		return true, nil
	}
	return false, nil
}

// loadSubmodule is lysp_load_submod_from_clb_or_file.
func (c *Context) loadSubmodule(p *pctx, name, rev string, cand *Submodule, cur string) (*Submodule, error) {
	clbUsed, dirsUsed := c.opts.Loader == nil, c.opts.DisableSearchdirs
	var s *Submodule
	for found := false; !found && (!clbUsed || !dirsUsed); {
		if (!c.opts.PreferSearchdirs || dirsUsed) && !clbUsed {
			if src, ok := c.opts.Loader(p.main.Name, "", name, rev); ok {
				var err error
				if s, err = c.parseSubmodule(p, src, loadData{name: name, rev: rev}, false); err != nil {
					return nil, err
				}
				found = true
			}
			clbUsed = true
		} else if !dirsUsed {
			f, err := c.search(name, rev)
			if err != nil {
				return nil, err
			}
			if f != nil {
				src, err := c.read(f)
				if err != nil {
					return nil, err
				}
				if s, err = c.parseSubmodule(p, src, loadData{name: name, rev: rev, path: f.name}, true); err != nil {
					return nil, err
				}
				found = true
			}
			dirsUsed = true
		}
	}
	if s == nil && cand == nil {
		_ = c.logVal(ly.Reference, 0, "Including \"%s\" submodule into \"%s\" failed, not found.", name, cur)
		return nil, eNotFound
	}
	return s, nil
}

// parseSubmodule is lys_parse_submodule; nil without error means LY_EEXIST.
func (c *Context) parseSubmodule(p *pctx, src []byte, d loadData, inDirs bool) (s *Submodule, err error) {
	defer func() {
		if err != nil && !errors.Is(err, ErrBudget) && !errors.Is(err, ErrUnsupported) {
			_ = c.logErr(err, "Parsing submodule \"%s\" failed.", d.name)
		}
	}()
	pm, st, err := c.parse(d, src, p.main.Name)
	if err != nil {
		return nil, err
	}
	s = &Submodule{Name: st.Arg, Main: p.main, pmod: pmod{Parsed: pm}}
	p.parsed = append(p.parsed, s) // parsed_mods is a stack: popped when this parser context is freed
	defer func() { p.parsed = p.parsed[:len(p.parsed)-1] }()
	s.Revision = c.lastRevision(pm.Revisions, "submodule", s.Name)
	latest := c.submoduleLatest(p.main, s.Name)
	switch {
	case latest == nil && inDirs && d.rev == "":
		s.latest = 2
	case latest == nil:
		s.latest = 1
	case s.Revision != "" && (latest.Revision == "" || s.Revision > latest.Revision):
		s.latest = latest.latest
	default:
		latest = nil
	}
	if exists, err := c.checkLoadData(d, s.Name, s.Revision, s.latest != 0); err != nil || exists {
		return nil, err
	}
	if latest != nil {
		latest.latest = 0
	}
	s.File = d.path
	if err := c.resolveImportsIncludes(p, &s.pmod, pm, s.Name); err != nil {
		return nil, err
	}
	p.done = append(p.done, s)
	return s, nil
}
