// SPDX-License-Identifier: BSD-3-Clause

// extract-utests pulls YANG/data fixtures out of ONE libyang tests/utests C file.
// The embedded schemas/data and the asserted outcome (LY_EVALID, CHECK_LOG_CTX) are BSD-3-Clause,
// (c) CESNET (libyang tests/utests); every emitted case.json carries that attribution.
//
// Usage: extract-utests [-verify] [-tag v5.8.6] -out DIR path/to/tests/utests/types/int8.c
// -verify shells out to `yanglint` (test-only, must report the same libyang version) and compares
// its verdict with the verdict asserted in the C test: that is the precision measurement.
//
// The extractor is a tiny C-subset interpreter, not a C parser: it tokenizes, expands the
// file's own #define macros (token substitution, exact arity), tracks string variables and
// evaluates string-literal concatenation. Everything it cannot resolve with certainty is
// counted under "skipped" and never guessed; once a step that changes the module context is
// skipped, the rest of that test function is skipped too (its cases would run against the wrong
// context).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// libyangTag is the libyang release whose tests are extracted (and whose yanglint verifies them).
const libyangTag = "v5.8.6"

type tok struct {
	k    byte // 's' string, 'b' malformed string (never resolvable), 'i' ident, 'n' number, 'p' punct
	v    string
	line int
}

type def struct {
	params   []string
	variadic bool
	fn       bool
	body     []tok
	poisoned bool // defined in a conditional block or more than once: value not knowable
}

// ---- lexer ----

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// lexString decodes the C string literal whose opening quote is at s[i]; it returns the value,
// whether every escape was understood, and the index after the closing quote.
func lexString(s string, i int) (string, bool, int) {
	var b strings.Builder
	ok := true
	i++
	for i < len(s) && s[i] != '"' {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		i++
		switch c := s[i]; c {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'a':
			b.WriteByte(7)
		case 'b':
			b.WriteByte(8)
		case 'f':
			b.WriteByte(12)
		case 'v':
			b.WriteByte(11)
		case '"', '\\', '\'', '?':
			b.WriteByte(c)
		case 'x':
			j := i + 1
			for j < len(s) && isHex(s[j]) {
				j++
			}
			n, err := strconv.ParseUint(s[i+1:j], 16, 8)
			if err != nil || n == 0 { // no digits, > 0xFF (over-long) or embedded NUL
				ok = false
			} else {
				b.WriteByte(byte(n))
			}
			i = j - 1
		case '0', '1', '2', '3', '4', '5', '6', '7':
			j := i
			for j < len(s) && j < i+3 && s[j] >= '0' && s[j] <= '7' {
				j++
			}
			n, _ := strconv.ParseUint(s[i:j], 8, 16)
			if n == 0 || n > 0xFF {
				ok = false
			} else {
				b.WriteByte(byte(n))
			}
			i = j - 1
		default: // \u \U and every unknown escape
			ok = false
		}
		i++
	}
	return b.String(), ok, i + 1
}

func lex(s string, line int) []tok {
	var out []tok
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				if s[i] == '\n' {
					line++
				}
				i++
			}
			i += 2
		case c == '"':
			v, ok, next := lexString(s, i)
			k := byte('s')
			if !ok {
				k = 'b'
			}
			out = append(out, tok{k, v, line})
			line += strings.Count(s[i:min(next, len(s))], "\n")
			i = next
		case c == '\'':
			j := i + 1
			for j < len(s) && s[j] != '\'' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			out = append(out, tok{'n', s[i:min(j+1, len(s))], line})
			i = j + 1
		case isIdent(c) && !(c >= '0' && c <= '9'):
			j := i
			for j < len(s) && isIdent(s[j]) {
				j++
			}
			out = append(out, tok{'i', s[i:j], line})
			i = j
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && (s[j] == '.' || isIdent(s[j])) {
				j++
			}
			out = append(out, tok{'n', s[i:j], line})
			i = j
		default:
			out = append(out, tok{'p', string(c), line})
			i++
		}
	}
	return out
}

var (
	reDefine = regexp.MustCompile(`^\s*#\s*define\s+(\w+)(\(([^)]*)\))?\s*(.*)$`)
	reCond   = regexp.MustCompile(`^\s*#\s*(if|ifdef|ifndef|elif|else|endif)\b`)
)

// splitDefines separates #define logical lines (into defs) from the rest of the file and blanks
// every line inside an #if/#ifdef/#else block (it returns how many it blanked: which side of the
// condition applies is not knowable). A name defined twice, or inside a conditional, is poisoned.
// ponytail: a '#' line inside a block comment would be misread as a directive; utests have none.
func splitDefines(src string) ([]tok, map[string]*def, int) {
	defs := map[string]*def{}
	var rest strings.Builder
	lines := strings.Split(src, "\n")
	cond, condLines := 0, 0
	for n := 0; n < len(lines); n++ {
		l := lines[n]
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			if cond > 0 {
				condLines++
				rest.WriteString("\n")
			} else {
				rest.WriteString(l + "\n")
			}
			continue
		}
		start := n + 1
		for strings.HasSuffix(l, "\\") && n+1 < len(lines) {
			n++
			l = strings.TrimSuffix(l, "\\") + " " + lines[n]
		}
		rest.WriteString(strings.Repeat("\n", n-start+2))
		if m := reCond.FindStringSubmatch(l); m != nil {
			switch m[1] {
			case "if", "ifdef", "ifndef":
				cond++
			case "endif":
				cond = max(cond-1, 0)
			}
			continue
		}
		m := reDefine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		d := &def{fn: m[2] != "", body: lex(m[4], start), poisoned: cond > 0}
		for _, p := range strings.Split(m[3], ",") {
			switch p = strings.TrimSpace(p); p {
			case "":
			case "...":
				d.variadic = true
			default:
				d.params = append(d.params, p)
			}
		}
		if _, dup := defs[m[1]]; dup {
			d.poisoned = true
		}
		defs[m[1]] = d
	}
	return lex(rest.String(), 1), defs, condLines
}

// ---- macro/args helpers ----

// args returns the top-level comma-separated argument token lists of the call whose '(' is at t[i],
// and the index just after the matching ')'.
func args(t []tok, i int) ([][]tok, int) {
	depth, cur := 0, []tok{}
	var out [][]tok
	for j := i; j < len(t); j++ {
		if t[j].k == 'p' {
			switch t[j].v {
			case "(", "{", "[":
				depth++
				if depth == 1 {
					continue
				}
			case ")", "}", "]":
				depth--
				if depth == 0 {
					if len(cur) > 0 || len(out) > 0 {
						out = append(out, cur)
					}
					return out, j + 1
				}
			case ",":
				if depth == 1 {
					out = append(out, cur)
					cur = nil
					continue
				}
			}
		}
		cur = append(cur, t[j])
	}
	return out, len(t)
}

// expand substitutes call arguments into the macro body. It refuses (ok=false) on a wrong
// number of arguments or a poisoned definition.
func (d *def) expand(a [][]tok) ([]tok, bool) {
	if d.poisoned || len(a) < len(d.params) || (!d.variadic && len(a) != len(d.params)) {
		return nil, false
	}
	var out []tok
	for _, b := range d.body {
		if b.k == 'i' {
			if b.v == "__VA_ARGS__" && d.variadic {
				for k := len(d.params); k < len(a); k++ {
					if k > len(d.params) {
						out = append(out, tok{'p', ",", b.line})
					}
					out = append(out, a[k]...)
				}
				continue
			}
			if k := slices.Index(d.params, b.v); k >= 0 {
				out = append(out, a[k]...)
				continue
			}
		}
		out = append(out, b)
	}
	return out, true
}

// ---- interpreter ----

type module struct {
	Name, File, Text string
	Sub              bool // submodule: search-dir file only, never an implemented module
}

type logEntry struct {
	Msg    string `json:"msg"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	AppTag string `json:"apptag,omitempty"`
}

type source struct {
	File      string `json:"file"`
	Func      string `json:"func"`
	Line      int    `json:"line"`
	Tag       string `json:"libyang_tag"`
	License   string `json:"license"`
	Copyright string `json:"copyright"`
}

// Case is one extracted fixture candidate.
type Case struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"` // schema | data
	Source   source     `json:"source"`
	Modules  []string   `json:"modules"` // implemented module names, load order
	Files    []string   `json:"files"`   // every module/submodule file written next to the case
	Format   string     `json:"format,omitempty"`
	DataFile string     `json:"data_file,omitempty"`
	Parse    string     `json:"parse_options,omitempty"`
	Validate string     `json:"validate_options,omitempty"`
	Features string     `json:"features,omitempty"`
	Ret      string     `json:"asserted_ret"`
	Verdict  string     `json:"asserted_verdict"`
	Log      []logEntry `json:"asserted_log,omitempty"`
	Verified string     `json:"verified,omitempty"` // agree | differ | n/a  (-verify only)
	// NeedsExtension lists C-side semantics the oracle request cannot express yet; when non-empty
	// there is no oracle_request (a request with dropped semantics would give wrong goldens).
	NeedsExtension []string       `json:"needs_extension,omitempty"`
	Request        map[string]any `json:"oracle_request,omitempty"`
	texts          map[string]string
	data           string
}

type interp struct {
	defs        map[string]*def
	vars        map[string]string
	file        string
	fn          string
	mods        []module
	cases       []*Case
	last        *Case
	n           int
	depth       int            // macro expansion depth
	site        int            // line of the outermost macro call being expanded
	tainted     string         // non-empty: the context of this test function is no longer known
	ctrl        bool           // saw if/else/for/while/switch in this test function
	skipped     map[string]int // reason -> count
	unextracted map[string]int // API call -> count
	tag         string
}

var apiCalls = map[string]bool{"lyd_parse_data_mem": true, "lyd_parse_data": true, "lyd_parse_op": true, "lys_parse_mem": true,
	"lys_parse": true, "lys_parse_path": true, "lyd_new_path": true, "lyd_new_term": true, "lyd_validate_all": true,
	"lyd_validate_module": true, "lyd_diff_siblings": true, "lyd_merge_siblings": true, "lyd_find_path": true,
	"lyd_print_mem": true, "lys_print_mem": true, "ly_in_new_memory": true}

// ctxCalls change the libyang context in ways the extractor does not model.
var ctxCalls = map[string]bool{"ly_ctx_new": true, "ly_ctx_new_ylpath": true, "ly_ctx_new_ylmem": true, "ly_ctx_new_yldata": true,
	"ly_ctx_set_options": true, "ly_ctx_unset_options": true, "ly_ctx_set_module_imp_clb": true, "ly_ctx_set_searchdir": true,
	"ly_ctx_unset_searchdir": true, "ly_ctx_destroy": true, "lys_set_implemented": true, "lys_feature_enable": true,
	"lys_feature_disable": true, "ly_ctx_set_ext_data_clb": true, "lys_parse_path": true, "lys_parse": true}

var ctrlWords = map[string]bool{"if": true, "else": true, "for": true, "while": true, "do": true, "switch": true, "goto": true}

// eval evaluates a string-literal concatenation, expanding string-valued macros and variables.
func (ip *interp) eval(t []tok) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		switch t[i].k {
		case 's':
			b.WriteString(t[i].v)
		case 'i':
			if t[i].v == "NULL" && len(t) == 1 {
				return "", true
			}
			if d, ok := ip.defs[t[i].v]; ok {
				var body []tok
				if d.fn {
					if i+1 >= len(t) || t[i+1].v != "(" {
						return "", false
					}
					a, end := args(t, i+1)
					if body, ok = d.expand(a); !ok {
						return "", false
					}
					i = end - 1
				} else if d.poisoned {
					return "", false
				} else {
					body = d.body
				}
				s, ok := ip.eval(body)
				if !ok {
					return "", false
				}
				b.WriteString(s)
			} else if v, ok := ip.vars[t[i].v]; ok {
				b.WriteString(v)
			} else {
				return "", false
			}
		default: // includes 'b' (malformed literal)
			return "", false
		}
	}
	return b.String(), len(t) > 0
}

func flat(t []tok) string {
	var p []string
	for _, x := range t {
		p = append(p, x.v)
	}
	return strings.Join(p, "")
}

var reModName = regexp.MustCompile(`(?m)^\s*(sub)?module\s+(?:"([^"]+)"|'([^']+)'|([^\s{;"']+))`)

func (ip *interp) newCase(kind string, line int) *Case {
	ip.n++
	if ip.depth > 0 {
		line = ip.site // report the call site, not the #define line
	}
	c := &Case{ID: fmt.Sprintf("%s/%s/%03d", strings.TrimSuffix(ip.file, ".c"), ip.fn, ip.n), Kind: kind,
		Source: source{ip.file, ip.fn, line, ip.tag, "BSD-3-Clause", "CESNET"}, texts: map[string]string{}}
	ip.cases = append(ip.cases, c)
	ip.last = c
	return c
}

func (ip *interp) skip(reason string) { ip.skipped[reason]++ }

// taint records that a module-load step could not be modelled: every later case of this test
// function would run against an unknown context, so none of them is emitted.
func (ip *interp) taint(why string) {
	if ip.tainted == "" {
		ip.tainted = why
	}
}

// addModule registers a YANG module in the per-test context; false when it is not extractable.
func (ip *interp) addModule(text, fmtTok string) (isSub, ok bool) {
	if fmtTok != "LYS_IN_YANG" {
		ip.skip("schema format " + fmtTok + " (YIN: out of v1 scope)")
		return false, false
	}
	m := reModName.FindStringSubmatch(text)
	if m == nil {
		ip.skip("module name not found")
		return false, false
	}
	name := m[2] + m[3] + m[4]
	if strings.ContainsAny(name, `/\`) || name == ".." || name == "." {
		ip.skip("unsafe module name")
		return false, false
	}
	mod := module{Name: name, File: name + ".yang", Text: text, Sub: m[1] != ""}
	ip.mods = slices.DeleteFunc(ip.mods, func(x module) bool { return x.Name == name })
	ip.mods = append(ip.mods, mod)
	return mod.Sub, true
}

func verdictOf(ret string) string {
	if ret == "LY_SUCCESS" {
		return "valid"
	}
	return "invalid"
}

func (ip *interp) snapshot(c *Case) {
	for _, m := range ip.mods {
		c.Files = append(c.Files, m.File)
		if !m.Sub {
			c.Modules = append(c.Modules, m.Name)
		}
		c.texts[m.File] = m.Text
	}
}

// schemaStep records a module-load step. A rejected module is not kept in the context.
func (ip *interp) schemaStep(textToks []tok, format, features, ret string, line int) {
	ip.last = nil // log checks that follow a skipped step must not attach to an older case
	loads := ret == "LY_SUCCESS"
	fail := func(reason string) {
		ip.skip(reason)
		if loads { // the module would be in the context: later cases cannot be trusted
			ip.taint(reason)
		}
	}
	if ip.tainted != "" {
		ip.skip("context incomplete after: " + ip.tainted)
		return
	}
	if ip.ctrl {
		fail("step after control flow (if/else/for/while/switch)")
		return
	}
	text, ok := ip.eval(textToks)
	if !ok {
		fail("schema text not resolvable")
		return
	}
	if ret == "LY_EEXIST" {
		ip.skip("context-dependent (LY_EEXIST: module already in ctx)")
		return
	}
	saved := slices.Clone(ip.mods)
	isSub, ok := ip.addModule(text, format)
	if !ok {
		ip.mods = saved
		if loads {
			ip.taint("module load skipped (" + format + ")")
		}
		return
	}
	if isSub {
		ip.skip("submodule text (kept as search-dir file only)")
		return
	}
	c := ip.newCase("schema", line)
	c.Ret, c.Verdict, c.Features = ret, verdictOf(ret), features
	ip.snapshot(c)
	if !loads {
		ip.mods = saved
	}
}

// dataStep records a lyd_parse_data step against the modules loaded so far in this test function.
func (ip *interp) dataStep(dataToks []tok, f, parse, val, ret string, line int) {
	ip.last = nil
	if ip.tainted != "" {
		ip.skip("context incomplete after: " + ip.tainted)
		return
	}
	if ip.ctrl {
		ip.skip("step after control flow (if/else/for/while/switch)")
		return
	}
	data, ok := ip.eval(dataToks)
	if !ok || (f != "LYD_XML" && f != "LYD_JSON") {
		ip.skip("data input not resolvable or not xml/json (" + f + ")")
		return
	}
	c := ip.newCase("data", line)
	c.Format, c.Parse, c.Validate, c.Ret = strings.ToLower(strings.TrimPrefix(f, "LYD_")), parse, val, ret
	c.Verdict = verdictOf(ret)
	c.data = data
	ip.snapshot(c)
}

func (ip *interp) run(t []tok) {
	for i := 0; i < len(t); i++ {
		x := t[i]
		if x.k != 'i' {
			continue
		}
		// function header: name ( void * * state )
		if i+6 < len(t) && t[i+1].v == "(" && t[i+2].v == "void" && t[i+3].v == "*" && t[i+4].v == "*" && t[i+5].v == "state" {
			ip.fn, ip.mods, ip.last, ip.vars, ip.tainted, ip.ctrl = x.v, nil, nil, map[string]string{}, "", false
			continue
		}
		if ctrlWords[x.v] {
			ip.ctrl = true
			continue
		}
		// compound assignment (x += ...): the value is no longer a known literal
		if i+2 < len(t) && strings.Contains("+-*/%&|^", t[i+1].v) && t[i+1].k == 'p' && t[i+2].v == "=" {
			delete(ip.vars, x.v)
			continue
		}
		// string assignment: ident = <string expr> ;
		if i+1 < len(t) && t[i+1].v == "=" && !(i+2 < len(t) && t[i+2].v == "=") {
			j := i + 2
			for j < len(t) && t[j].v != ";" {
				j++
			}
			s, ok := ip.eval(t[i+2 : j])
			if ok && !ip.ctrl {
				ip.vars[x.v] = s
			} else {
				delete(ip.vars, x.v) // never keep a stale value
			}
			i = j
			continue
		}
		if i+1 >= len(t) || t[i+1].v != "(" {
			continue
		}
		a, end := args(t, i+1)
		switch x.v {
		case "UTEST_ADD_MODULE", "UTEST_INVALID_MODULE":
			if len(a) < 4 {
				break
			}
			ret := "LY_SUCCESS"
			if x.v == "UTEST_INVALID_MODULE" {
				ret = flat(a[3])
			}
			ip.schemaStep(a[0], flat(a[1]), flat(a[2]), ret, x.line)
		case "CHECK_PARSE_LYD_PARAM":
			if len(a) >= 5 {
				ip.dataStep(a[0], flat(a[1]), flat(a[2]), flat(a[3]), flat(a[4]), x.line)
			}
		case "assert_int_equal": // assert_int_equal(LY_x, lys_parse_mem(ctx, text, fmt, &mod))
			if len(a) != 2 || len(a[1]) < 2 || a[1][1].v != "(" {
				continue
			}
			in, _ := args(a[1], 1)
			switch {
			case a[1][0].v == "lys_parse_mem" && len(in) >= 3:
				ip.schemaStep(in[1], flat(in[2]), "", flat(a[0]), x.line)
			case a[1][0].v == "lyd_parse_data_mem" && len(in) >= 5:
				ip.dataStep(in[1], flat(in[2]), flat(in[3]), flat(in[4]), flat(a[0]), x.line)
			default:
				continue
			}
		case "CHECK_LOG_CTX", "CHECK_LOG_CTX_APPTAG":
			if len(a) < 3 {
				break
			}
			if ip.last == nil {
				ip.skip("log check whose case was skipped (YIN/LYB/EEXIST/unresolvable)")
				break
			}
			msg, ok := ip.eval(a[0])
			if !ok {
				ip.skip("log message not resolvable")
				break
			}
			path, _ := ip.eval(a[1]) // NULL -> ""
			ln, _ := strconv.Atoi(flat(a[2]))
			le := logEntry{msg, path, ln, ""}
			if x.v == "CHECK_LOG_CTX_APPTAG" && len(a) > 3 {
				le.AppTag, _ = ip.eval(a[3])
			}
			ip.last.Log = append(ip.last.Log, le)
		default:
			if d, ok := ip.defs[x.v]; ok && d.fn {
				body, ok := d.expand(a)
				if !ok {
					ip.skip("macro " + x.v + ": arity mismatch or ambiguous definition")
					ip.taint("macro " + x.v + " not expanded")
					break
				}
				if ip.depth == 0 {
					ip.site = x.line
				}
				ip.depth++
				ip.run(body)
				ip.depth--
			} else {
				if ctxCalls[x.v] {
					ip.taint("context configured by C code (" + x.v + ")")
				}
				if apiCalls[x.v] {
					ip.unextracted[x.v]++
				}
				continue // keep scanning inside the arguments
			}
		}
		i = end - 1
	}
}

// ---- oracle request ----

var (
	knownParse    = []string{"no_state", "ordered", "when_true", "store_only", "json_null", "json_string_datatypes", "anydata_strict"}
	knownValidate = []string{"no_state", "present", "multi_error", "operational", "no_defaults", "not_final"}
)

func flagList(s, prefix string) (names []string) {
	for _, f := range strings.Split(s, "|") {
		if f = strings.TrimSpace(f); f != "" && f != "0" {
			names = append(names, strings.ToLower(strings.TrimPrefix(f, prefix)))
		}
	}
	return names
}

// buildRequest returns the protocol-v2 request draft, or the list of C-side semantics it cannot express.
func (c *Case) buildRequest() (map[string]any, []string) {
	var need []string
	req := map[string]any{"op": "schema", "searchdirs": []string{"."}}
	mods := []map[string]any{}
	for _, m := range c.Modules {
		mods = append(mods, map[string]any{"name": m})
	}
	if f := strings.TrimSpace(c.Features); f != "" && f != "NULL" && f != "0" {
		need = append(need, "features="+f) // per-module feature lists are not recovered from C
	}
	req["modules"] = mods
	if c.Kind == "data" {
		req["op"], req["format"], req["data_file"], req["data_type"] = "data", c.Format, c.DataFile, "data"
		req["unknown"] = "skip"
		var po, vo []string
		for _, n := range flagList(c.Parse, "LYD_PARSE_") {
			switch {
			case n == "only":
				req["parse_only"] = true
			case n == "strict":
				req["unknown"] = "reject"
			case n == "opaq":
				req["unknown"] = "opaque"
			case slices.Contains(knownParse, n):
				po = append(po, n)
			default:
				need = append(need, "parse_option="+n)
			}
		}
		for _, n := range flagList(c.Validate, "LYD_VALIDATE_") {
			if slices.Contains(knownValidate, n) {
				vo = append(vo, n)
			} else {
				need = append(need, "validate_option="+n)
			}
		}
		if _, ok := req["parse_only"]; !ok {
			req["parse_only"] = false
		}
		req["parse_options"], req["validate_options"] = nonNil(po), nonNil(vo)
	}
	return req, need
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---- output + verification ----

func (c *Case) write(out string) error {
	dir := filepath.Join(out, c.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for n, tx := range c.texts {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(tx), 0o644); err != nil {
			return err
		}
	}
	if c.Kind == "data" {
		c.DataFile = "data." + c.Format
		if err := os.WriteFile(filepath.Join(dir, c.DataFile), []byte(c.data), 0o644); err != nil {
			return err
		}
	}
	req, need := c.buildRequest()
	if len(need) > 0 {
		c.NeedsExtension = need
	} else {
		c.Request = req
	}
	return c.dump(dir)
}

func (c *Case) dump(dir string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "case.json"), append(b, '\n'), 0o644)
}

// yanglint runs the verifier; it returns the exit code, or an error when it could not be run at
// all. A variable so tests can replace it.
var yanglint = func(args ...string) (int, error) {
	err := exec.Command("yanglint", args...).Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, nil
	case errors.As(err, &ee):
		return ee.ExitCode(), nil // -1 when killed by a signal
	}
	return 0, err
}

// checkYanglint refuses to verify against a yanglint of another libyang version.
func checkYanglint(tag string) error {
	out, err := exec.Command("yanglint", "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("yanglint not usable: %w", err)
	}
	if want := strings.TrimPrefix(tag, "v"); !strings.Contains(string(out), want) {
		return fmt.Errorf("yanglint reports %q, want libyang %s", strings.TrimSpace(string(out)), want)
	}
	return nil
}

// verify compares the asserted verdict with yanglint's. Exit 0 = valid, 1 = libyang error;
// anything else (signal, usage error) is n/a, and a tool that cannot run aborts the whole run.
func (c *Case) verify(out string) error {
	dir := filepath.Join(out, c.ID)
	a := []string{"-p", dir}
	if c.Kind == "data" {
		if strings.Contains(c.Parse, "STORE_ONLY") || strings.Contains(c.Parse, "LYD_PARSE_ONLY") || strings.Contains(c.Parse, "LYD_PARSE_OPAQ") {
			c.Verified = "n/a (parse-only/store-only/opaque flags have no yanglint equivalent)"
			return nil
		}
		// yanglint "-t data" is operational (violations become warnings); lyd_parse_data without
		// LYD_VALIDATE_OPERATIONAL is closest to "-t config" (ponytail: state-data cases mis-verify).
		a = append(a, "-t", "config", "-e")
		if !strings.Contains(c.Parse, "LYD_PARSE_STRICT") {
			a = append(a, "-n")
		}
	}
	// yanglint enables every feature unless told otherwise; the C tests load with none (NULL).
	if f := strings.TrimSpace(c.Features); f != "" && f != "NULL" && f != "0" {
		c.Verified = "n/a (per-module feature list not recovered)"
		return nil
	}
	for _, m := range c.Modules {
		a = append(a, "-F", m+":")
	}
	for _, f := range c.Files {
		a = append(a, filepath.Join(dir, f))
	}
	if c.Kind == "data" {
		a = append(a, filepath.Join(dir, c.DataFile))
	}
	code, err := yanglint(a...)
	if err != nil {
		return err
	}
	got := map[int]string{0: "valid", 1: "invalid"}[code]
	switch {
	case got == "":
		c.Verified = fmt.Sprintf("n/a (yanglint exit status %d)", code)
	case got == c.Verdict:
		c.Verified = "agree"
	default:
		c.Verified = "differ (yanglint " + got + ")"
	}
	return nil
}

// insideRepo reports whether dir (existing or not) lies in a git work tree.
func insideRepo(dir string) bool {
	d, err := filepath.Abs(dir)
	if err != nil {
		return true
	}
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		if p := filepath.Dir(d); p != d {
			d = p
		} else {
			return false
		}
	}
}

func main() {
	out := flag.String("out", "", "output directory (must be outside any git work tree)")
	ver := flag.Bool("verify", false, "compare asserted verdict with system yanglint")
	tag := flag.String("tag", libyangTag, "libyang tag the tests come from (recorded in case.json; yanglint must match)")
	inRepo := flag.Bool("allow-in-repo", false, "allow -out inside a git work tree (fixtures are not for the manifest yet)")
	flag.Parse()
	if *out == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: extract-utests [-verify] [-tag vX.Y.Z] -out DIR tests/utests/.../x.c")
		os.Exit(2)
	}
	if insideRepo(*out) && !*inRepo {
		fmt.Fprintln(os.Stderr, "refusing to write extracted fixtures inside a git work tree; use another -out or -allow-in-repo")
		os.Exit(2)
	}
	fatal := func(err error) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *ver {
		if err := checkYanglint(*tag); err != nil {
			fatal(err)
		}
	}
	src, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fatal(err)
	}
	toks, defs, condLines := splitDefines(string(src))
	ip := &interp{defs: defs, vars: map[string]string{}, file: filepath.Base(flag.Arg(0)), skipped: map[string]int{}, unextracted: map[string]int{}, tag: *tag}
	if condLines > 0 {
		ip.skipped["source lines inside #if/#else blocks (ignored)"] = condLines
	}
	ip.run(toks)
	agree, differ, na, needExt := 0, 0, 0, 0
	byKind := map[string]int{}
	for _, c := range ip.cases {
		if err := c.write(*out); err != nil {
			fatal(err)
		}
		byKind[c.Kind+"/"+c.Verdict]++
		if len(c.NeedsExtension) > 0 {
			needExt++
		}
		if !*ver {
			continue
		}
		if err := c.verify(*out); err != nil {
			fatal(fmt.Errorf("yanglint could not be run (results would be meaningless): %w", err))
		}
		if err := c.dump(filepath.Join(*out, c.ID)); err != nil {
			fatal(err)
		}
		switch {
		case c.Verified == "agree":
			agree++
		case strings.HasPrefix(c.Verified, "n/a"):
			na++
		default:
			differ++
			fmt.Printf("DIFFER %s (%s:%d) asserted=%s %s\n", c.ID, c.Source.File, c.Source.Line, c.Verdict, c.Verified)
		}
	}
	fmt.Printf("%s: %d cases %v (%d need oracle extension)\n", ip.file, len(ip.cases), byKind, needExt)
	fmt.Printf("  skipped: %v\n  unextracted API calls: %v\n", ip.skipped, ip.unextracted)
	if *ver {
		fmt.Printf("  verify vs yanglint: agree=%d differ=%d n/a=%d\n", agree, differ, na)
	}
}
