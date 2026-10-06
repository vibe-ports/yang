// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 src/log.c (ly_log_location, ly_log_location_revert,
// ly_vlog_build_path_line, ly_vlog, ly_err_print, the LOGERR/LOGWRN/LOGVAL macros of
// ly_common.h) (BSD-3-Clause, © CESNET).

package data

import (
	"errors"
	"fmt"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/ly"
	"github.com/vibe-ports/yang/internal/schema"
)

// ValidationError is the error of a parse or validation that logged at least one error; Diags
// holds every diagnostic in log order, warnings included.
type ValidationError struct {
	Diags []yang.Diagnostic
	err   error // yang.ErrBudget when a libyang nesting limit stopped the parse
}

// Unwrap returns yang.ErrBudget when a nesting limit of the input stopped the parse.
func (e *ValidationError) Unwrap() error { return e.err }

func (e *ValidationError) Error() string {
	for _, d := range e.Diags {
		if !d.Warning {
			return d.Msg
		}
	}
	return "validation failed"
}

// logger is the libyang log state of one parse or validation: the stored diagnostics
// (LY_LOSTORE) and the location stack of ly_log_location (LOG_LOCSET/LOG_LOCBACK), held per
// operation instead of thread-local.
type logger struct {
	set     *schema.Set
	diags   []yang.Diagnostic
	scnodes []*schema.Node // log_location.scnodes
	paths   []string       // log_location.paths
	inputs  []func() int   // log_location.inputs: the current line of each input
}

// locSet is LOG_LOCSET(scnode).
func (l *logger) locSet(sn *schema.Node) { l.scnodes = append(l.scnodes, sn) }

// locBack is LOG_LOCBACK(steps).
func (l *logger) locBack(steps int) { l.scnodes = l.scnodes[:max(0, len(l.scnodes)-steps)] }

// pushPath and popPath are ly_log_location / ly_log_location_revert of a path string.
func (l *logger) pushPath(p string) { l.paths = append(l.paths, p) }
func (l *logger) popPath()          { l.paths = l.paths[:max(0, len(l.paths)-1)] }

// pushInput and popInput are ly_log_location / ly_log_location_revert of an input; line reports
// the input's current line.
func (l *logger) pushInput(line func() int) { l.inputs = append(l.inputs, line) }
func (l *logger) popInput()                 { l.inputs = l.inputs[:max(0, len(l.inputs)-1)] }

// location is ly_vlog_build_path_line: the data path of lnode, extended by the schema node sn
// (or the innermost LOG_LOCSET node) when lnode's schema node is its data parent; without a data
// node the schema path of sn or of the innermost LOG_LOCSET node; then the innermost location
// path appended; the line of the innermost input.
func (l *logger) location(lnode *Node, sn *schema.Node) (dataPath, schemaPath string, line int) {
	switch {
	case lnode != nil:
		dataPath = lydPath(l.set, lnode, false)
		if sn == nil && len(l.scnodes) > 0 {
			sn = l.scnodes[len(l.scnodes)-1]
		}
		if sn != nil && sn.DataParent() == lnode.schema {
			if nodeModule(l.set, lnode) != sn.Module {
				dataPath += "/" + sn.Module.Name + ":" + sn.Name
			} else {
				dataPath += "/" + sn.Name
			}
		}
	case sn != nil:
		schemaPath = sn.LogPath()
	case len(l.scnodes) > 0:
		schemaPath = l.scnodes[len(l.scnodes)-1].LogPath()
	}
	if n := len(l.paths); n > 0 && l.paths[n-1] != "" {
		switch {
		case dataPath != "":
			dataPath += l.paths[n-1]
		default:
			schemaPath += l.paths[n-1]
		}
	}
	if n := len(l.inputs); n > 0 {
		line = l.inputs[n-1]()
	}
	return dataPath, schemaPath, line
}

// val is LOGVAL / LOGVAL_APPTAG: a validation error located at lnode (nil: the location stack).
func (l *logger) val(lnode *Node, apptag string, code ly.Code, format string, a ...any) error {
	return l.item(lnode, nil, false, "LY_EVALID", code, apptag, fmt.Sprintf(format, a...))
}

// item is ly_err_print: an error item (e.g. a type plugin's) logged at lnode with the schema
// node sn being stored under it.
func (l *logger) item(lnode *Node, sn *schema.Node, warning bool, err string, code ly.Code, apptag, msg string) error {
	d := yang.Diagnostic{Warning: warning, Err: err, Code: code.String(), AppTag: apptag, Msg: msg}
	d.DataPath, d.SchemaPath, d.Line = l.location(lnode, sn)
	l.diags = append(l.diags, d)
	return errLogged
}

// warn is LOGWRN: a warning without location.
func (l *logger) warn(format string, a ...any) {
	l.diags = append(l.diags, yang.Diagnostic{Warning: true, Err: "LY_SUCCESS", Code: ly.Success.String(),
		Msg: fmt.Sprintf(format, a...)})
}

// logErr is LOGERR(ctx, errno, ...): an error without location or validation code; errno is the
// LY_ERR name (LY_EINVAL for the depth limits and the key refusal of the tree operations, whose
// *opError carries it).
func (l *logger) logErr(errno, format string, a ...any) error {
	l.diags = append(l.diags, yang.Diagnostic{Err: errno, Code: ly.Success.String(), Msg: fmt.Sprintf(format, a...)})
	return errLogged
}

// lexVal is LOGVAL from a lexer: the parser's location, the lexer's line.
func (l *logger) lexVal(code ly.Code, msg string, line int) {
	d := yang.Diagnostic{Err: "LY_EVALID", Code: code.String(), Msg: msg}
	d.DataPath, d.SchemaPath, d.Line = l.location(nil, nil)
	if line != 0 {
		d.Line = line
	}
	l.diags = append(l.diags, d)
}

// errLogged is returned by the logging helpers: the details are in the logger's diagnostics.
var errLogged = errors.New("data: error logged")

// result is the error of the operation: nil when only warnings were logged, else a
// *ValidationError with every diagnostic.
func (l *logger) result() error {
	for _, d := range l.diags {
		if !d.Warning {
			return &ValidationError{Diags: l.diags}
		}
	}
	return nil
}
