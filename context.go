// SPDX-License-Identifier: BSD-3-Clause

// Package yang loads and compiles YANG modules with the behaviour of libyang
// v5.8.6 (design docs/design/05, 06).
package yang

import (
	"io/fs"
	"sync"
	"sync/atomic"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/parser"
	"github.com/vibe-ports/yang/internal/snap"
)

var (
	// ErrBudget is wrapped by errors caused by an exceeded resource limit.
	ErrBudget = compile.ErrBudget
	// ErrUnsupported is wrapped by errors for input this port does not handle.
	ErrUnsupported = compile.ErrUnsupported
)

// Options are the libyang context flags (ly_ctx_new options).
type Options struct {
	AllImplemented    bool // LY_CTX_ALL_IMPLEMENTED
	NoYangLibrary     bool // LY_CTX_NO_YANGLIBRARY
	DisableSearchdirs bool // LY_CTX_DISABLE_SEARCHDIRS
	PreferSearchdirs  bool // LY_CTX_PREFER_SEARCHDIRS: search directories before Loader
	// EnableImportFeatures enables all features of modules that become
	// implemented implicitly (LY_CTX_ENABLE_IMP_FEATURES).
	EnableImportFeatures bool
	// CompileObsolete keeps obsolete nodes in the compiled tree (LY_CTX_COMPILE_OBSOLETE).
	CompileObsolete bool
	// Loader supplies modules and submodules not found otherwise (libyang's
	// import callback): the YANG text of module@revision, or of its submodule
	// when submodule is not empty; ok false when it has none. It is called
	// while Load holds the context lock and must not call the Context.
	Loader func(module, revision, submodule, subRevision string) (src []byte, ok bool)
	// MaxSearchDirs bounds the directories one module search opens
	// (default 10 000), so a symlink cycle fails with ErrBudget.
	MaxSearchDirs int
	// ParseBudget bounds every (sub)module text; MaxBytes (default 64 MiB)
	// also bounds reading a module file.
	ParseBudget ParseBudget
}

// ParseBudget bounds the resources parsing one module may use; zero fields
// take the defaults. Exceeding one fails with ErrBudget.
type ParseBudget struct {
	MaxBytes  int // input size, default 64 MiB
	MaxDepth  int // nested blocks, default 500 (libyang LY_MAX_BLOCK_DEPTH)
	MaxStmts  int // statements, default 1<<20
	MaxArgLen int // bytes of one argument, default 1<<24
}

// Diagnostic is an error or warning libyang would log against the context.
type Diagnostic struct {
	Phase      string // "parse" or "compile" (oracle phases, design 06 §1.6); empty for data trees
	Warning    bool
	Err        string // libyang LY_ERR name, e.g. "LY_EVALID"
	Code       string // libyang LY_VECODE name, e.g. "LYVE_REFERENCE"
	SchemaPath string
	DataPath   string // data trees: lyd_path of the node the message is about (design 07 §1.10)
	AppTag     string // data trees: error-app-tag
	Line       int    // 0 when unknown
	Msg        string
}

// Context is a set of loaded modules (ly_ctx). It is safe for concurrent use.
type Context struct {
	mu     sync.Mutex
	c      *compile.Context
	schema atomic.Pointer[Schema]
}

// Schema returns the compiled schema as of the last NewContext or Load: an immutable snapshot.
// A later Load publishes a new one; snapshots obtained before stay valid and unchanged (they just
// do not see later loads). Safe to call concurrently with Load.
func (x *Context) Schema() *Schema { return x.schema.Load() }

// publish makes the context's current state the snapshot Schema returns (callers hold mu).
func (x *Context) publish() { x.schema.Store(snap.New(x.c.Snapshot())) }

// NewContext creates a context with libyang's internal modules loaded;
// modules are searched in dirs (the last one first, as libyang does). The
// diagnostics are warnings logged while loading the internal modules, which
// come from the embedded libyang module directory only: dirs and the Loader
// options apply to later loads.
func NewContext(opts Options, dirs ...fs.FS) (*Context, []Diagnostic, error) {
	c, diags, err := compile.NewContext(compile.Options{AllImplemented: opts.AllImplemented,
		NoYangLibrary: opts.NoYangLibrary, DisableSearchdirs: opts.DisableSearchdirs,
		PreferSearchdirs: opts.PreferSearchdirs, EnableImportFeatures: opts.EnableImportFeatures, CompileObsolete: opts.CompileObsolete, Loader: opts.Loader, MaxSearchDirs: opts.MaxSearchDirs,
		Parse: parser.Budget(opts.ParseBudget)}, dirs...)
	if err != nil {
		return nil, convert(diags), err
	}
	x := &Context{c: c}
	x.publish()
	return x, convert(diags), nil
}

// Load loads module name (the newest available revision when revision is
// empty) with its imports and includes, implements it with the features and
// compiles the context. features nil leaves the module's features untouched
// (all disabled for a newly implemented module), an empty list disables
// all, ["*"] enables all, otherwise exactly the listed ones are enabled. On
// error the context is unchanged except where libyang keeps changes too
// (the features of a module implemented before). Afterwards Schema returns the new state.
func (x *Context) Load(name, revision string, features []string) ([]Diagnostic, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	_, diags, err := x.c.Load(name, revision, features)
	x.publish()
	return convert(diags), err
}

func convert(ds []compile.Diagnostic) []Diagnostic {
	r := make([]Diagnostic, len(ds))
	for i, d := range ds {
		r[i] = Diagnostic{Phase: d.Phase, Warning: d.Level == compile.LevelWarning, Err: d.Err, Code: d.Code.String(),
			SchemaPath: d.SchemaPath, Line: d.Line, Msg: d.Msg}
	}
	return r
}
