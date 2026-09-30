// SPDX-License-Identifier: BSD-3-Clause

// extract-utests pulls YANG/data fixtures out of ONE libyang tests/utests C file.
// The embedded schemas/data and the asserted outcome (LY_EVALID, CHECK_LOG_CTX) are BSD-3-Clause,
// (c) CESNET (libyang v5.8.6 tests/utests); every emitted case.json carries that attribution.
//
// Usage: extract-utests [-verify] -out DIR path/to/tests/utests/types/int8.c
// -verify shells out to `yanglint` (test-only, same libyang version) and compares its verdict
// with the verdict asserted in the C test: that is the precision measurement.
//
// The extractor is a tiny C-subset interpreter, not a C parser: it tokenizes, expands the
// file's own #define macros (token substitution), tracks string variables and evaluates
// string-literal concatenation. Anything else is counted as "unextracted", never guessed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type tok struct {
	k    byte // 's' string, 'i' ident, 'n' number, 'p' punct
	v    string
	line int
}

type def struct {
	params []string
	fn     bool
	body   []tok
}

// ---- lexer ----

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
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
			var b strings.Builder
			i++
			for i < len(s) && s[i] != '"' {
				if s[i] == '\\' && i+1 < len(s) {
					i++
					switch s[i] {
					case 'n':
						b.WriteByte('\n')
					case 't':
						b.WriteByte('\t')
					case 'r':
						b.WriteByte('\r')
					case 'x':
						j := i + 1
						for j < len(s) && j < i+3 && strings.IndexByte("0123456789abcdefABCDEF", s[j]) >= 0 {
							j++
						}
						n, _ := strconv.ParseUint(s[i+1:j], 16, 8)
						b.WriteByte(byte(n))
						i = j - 1
					case '0', '1', '2', '3', '4', '5', '6', '7':
						j := i
						for j < len(s) && j < i+3 && s[j] >= '0' && s[j] <= '7' {
							j++
						}
						n, _ := strconv.ParseUint(s[i:j], 8, 8)
						b.WriteByte(byte(n))
						i = j - 1
					default: // \" \\ \' and unknown
						b.WriteByte(s[i])
					}
				} else {
					b.WriteByte(s[i])
				}
				i++
			}
			i++
			out = append(out, tok{'s', b.String(), line})
		case c == '\'':
			j := i + 1
			for j < len(s) && s[j] != '\'' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			out = append(out, tok{'n', s[i : j+1], line})
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

var reDefine = regexp.MustCompile(`^\s*#\s*define\s+(\w+)(\(([^)]*)\))?\s*(.*)$`)

// splitDefines separates #define logical lines (into defs) from the rest of the file.
// ponytail: a '#' line inside a block comment would be misread as a directive; utests have none.
func splitDefines(src string) ([]tok, map[string]*def) {
	defs := map[string]*def{}
	var rest strings.Builder
	lines := strings.Split(src, "\n")
	for n := 0; n < len(lines); n++ {
		l := lines[n]
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			rest.WriteString(l + "\n")
			continue
		}
		start := n + 1
		for strings.HasSuffix(l, "\\") && n+1 < len(lines) {
			n++
			l = strings.TrimSuffix(l, "\\") + " " + lines[n]
		}
		rest.WriteString(strings.Repeat("\n", n-start+2))
		m := reDefine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		d := &def{fn: m[2] != "", body: lex(m[4], start)}
		for _, p := range strings.Split(m[3], ",") {
			if p = strings.TrimSpace(p); p != "" {
				d.params = append(d.params, p)
			}
		}
		defs[m[1]] = d
	}
	return lex(rest.String(), 1), defs
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

func (d *def) expand(a [][]tok) []tok {
	var out []tok
	for _, b := range d.body {
		if b.k == 'i' {
			if b.v == "__VA_ARGS__" {
				for k := len(d.params); k < len(a); k++ {
					if k > len(d.params) {
						out = append(out, tok{'p', ",", b.line})
					}
					out = append(out, a[k]...)
				}
				continue
			}
			hit := false
			for k, p := range d.params {
				if p == b.v && k < len(a) {
					out = append(out, a[k]...)
					hit = true
					break
				}
			}
			if hit {
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

// ---- interpreter ----

type module struct{ Name, File, Text string }

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
	ID       string         `json:"id"`
	Kind     string         `json:"kind"` // schema | data
	Source   source         `json:"source"`
	Modules  []string       `json:"modules"` // module file names, load order
	Format   string         `json:"format,omitempty"`
	DataFile string         `json:"data_file,omitempty"`
	Parse    string         `json:"parse_options,omitempty"`
	Validate string         `json:"validate_options,omitempty"`
	Features string         `json:"features,omitempty"`
	Ret      string         `json:"asserted_ret"`
	Verdict  string         `json:"asserted_verdict"`
	Log      []logEntry     `json:"asserted_log,omitempty"`
	Verified string         `json:"verified,omitempty"` // agree | differ | n/a  (-verify only)
	Request  map[string]any `json:"oracle_request"`
	texts    map[string]string
	data     string
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
	skipped     map[string]int // reason -> count
	unextracted map[string]int // API call -> count
}

var apiCalls = map[string]bool{"lyd_parse_data_mem": true, "lyd_parse_data": true, "lyd_parse_op": true, "lys_parse_mem": true,
	"lys_parse": true, "lys_parse_path": true, "lyd_new_path": true, "lyd_new_term": true, "lyd_validate_all": true,
	"lyd_validate_module": true, "lyd_diff_siblings": true, "lyd_merge_siblings": true, "lyd_find_path": true,
	"ly_ctx_new": true, "lyd_print_mem": true, "lys_print_mem": true, "ly_in_new_memory": true}

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
				if d.fn {
					if i+1 >= len(t) || t[i+1].v != "(" {
						return "", false
					}
					a, end := args(t, i+1)
					s, ok := ip.eval(d.expand(a))
					if !ok {
						return "", false
					}
					b.WriteString(s)
					i = end - 1
				} else {
					s, ok := ip.eval(d.body)
					if !ok {
						return "", false
					}
					b.WriteString(s)
				}
			} else if v, ok := ip.vars[t[i].v]; ok {
				b.WriteString(v)
			} else {
				return "", false
			}
		default:
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

var reModName = regexp.MustCompile(`(?m)^\s*(?:sub)?module\s+"?([\w.-]+)"?`)

func (ip *interp) newCase(kind string, line int) *Case {
	ip.n++
	if ip.depth > 0 {
		line = ip.site // report the call site, not the #define line
	}
	c := &Case{ID: fmt.Sprintf("%s/%s/%03d", strings.TrimSuffix(ip.file, ".c"), ip.fn, ip.n), Kind: kind,
		Source: source{ip.file, ip.fn, line, "v5.8.6", "BSD-3-Clause", "CESNET"}, texts: map[string]string{}}
	ip.cases = append(ip.cases, c)
	ip.last = c
	return c
}

// addModule registers a YANG module in the per-test context; nil when not extractable.
func (ip *interp) addModule(text, fmtTok string) bool {
	if fmtTok != "LYS_IN_YANG" {
		ip.skipped["schema format "+fmtTok+" (YIN: out of v1 scope)"]++
		return false
	}
	m := reModName.FindStringSubmatch(text)
	if m == nil {
		ip.skipped["module name not found"]++
		return false
	}
	for i := range ip.mods { // same name re-added in one context: keep the latest
		if ip.mods[i].Name == m[1] {
			ip.mods = append(ip.mods[:i], ip.mods[i+1:]...)
			break
		}
	}
	ip.mods = append(ip.mods, module{m[1], m[1] + ".yang", text})
	return true
}

func verdictOf(ret string) string {
	if ret == "LY_SUCCESS" {
		return "valid"
	}
	return "invalid"
}

func (ip *interp) snapshot(c *Case) {
	for _, m := range ip.mods {
		c.Modules = append(c.Modules, m.File)
		c.texts[m.File] = m.Text
	}
}

// schemaStep records a module-load step. A rejected module is not kept in the context.
func (ip *interp) schemaStep(textToks []tok, format, features, ret string, line int) {
	ip.last = nil // log checks that follow a skipped step must not attach to an older case
	text, ok := ip.eval(textToks)
	if !ok {
		ip.skipped["schema text not resolvable"]++
		return
	}
	if ret == "LY_EEXIST" {
		ip.skipped["context-dependent (LY_EEXIST: module already in ctx)"]++
		return
	}
	saved := ip.mods
	if ip.addModule(text, format) {
		c := ip.newCase("schema", line)
		c.Ret, c.Verdict, c.Features = ret, verdictOf(ret), features
		ip.snapshot(c)
	}
	if ret != "LY_SUCCESS" {
		ip.mods = saved
	}
}

// dataStep records a lyd_parse_data step against the modules loaded so far in this test function.
func (ip *interp) dataStep(dataToks []tok, f, parse, val, ret string, line int) {
	ip.last = nil
	data, ok := ip.eval(dataToks)
	if !ok || (f != "LYD_XML" && f != "LYD_JSON") {
		ip.skipped["data input not resolvable or not xml/json ("+f+")"]++
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
			ip.fn, ip.mods, ip.last, ip.vars = x.v, nil, nil, map[string]string{}
			continue
		}
		// string assignment: ident = <string expr> ;
		if i+1 < len(t) && t[i+1].v == "=" && !(i+2 < len(t) && t[i+2].v == "=") {
			j := i + 2
			for j < len(t) && t[j].v != ";" {
				j++
			}
			if s, ok := ip.eval(t[i+2 : j]); ok {
				ip.vars[x.v] = s
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
				ip.skipped["log check whose case was skipped (YIN/LYB/EEXIST/unresolvable)"]++
				break
			}
			msg, ok := ip.eval(a[0])
			if !ok {
				ip.skipped["log message not resolvable"]++
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
				if ip.depth == 0 {
					ip.site = x.line
				}
				ip.depth++
				ip.run(d.expand(a))
				ip.depth--
			} else if apiCalls[x.v] {
				ip.unextracted[x.v]++
				continue // keep scanning inside the arguments
			}
		}
		i = end - 1
	}
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
	req := map[string]any{"op": "schema", "searchdirs": []string{"."}}
	var mods []map[string]any
	for _, m := range c.Modules {
		mods = append(mods, map[string]any{"name": strings.TrimSuffix(m, ".yang")})
	}
	req["modules"] = mods
	if c.Kind == "data" {
		c.DataFile = "data." + c.Format
		if err := os.WriteFile(filepath.Join(dir, c.DataFile), []byte(c.data), 0o644); err != nil {
			return err
		}
		req["op"], req["format"], req["data_file"], req["data_type"] = "data", c.Format, c.DataFile, "data"
		req["parse_only"] = strings.Contains(c.Parse, "LYD_PARSE_ONLY")
		switch {
		case strings.Contains(c.Parse, "LYD_PARSE_STRICT"):
			req["unknown"] = "reject"
		case strings.Contains(c.Parse, "LYD_PARSE_OPAQ"):
			req["unknown"] = "opaque"
		default:
			req["unknown"] = "skip"
		}
	}
	c.Request = req
	return c.dump(dir)
}

func (c *Case) dump(dir string) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(filepath.Join(dir, "case.json"), append(b, '\n'), 0o644)
}

// verify runs the system yanglint (same libyang version) and compares valid/invalid.
func (c *Case) verify(out string) {
	dir := filepath.Join(out, c.ID)
	a := []string{"-p", dir}
	if c.Kind == "data" {
		if strings.Contains(c.Parse, "STORE_ONLY") || strings.Contains(c.Parse, "LYD_PARSE_ONLY") || strings.Contains(c.Parse, "LYD_PARSE_OPAQ") {
			c.Verified = "n/a (parse-only/store-only/opaque flags have no yanglint equivalent)"
			return
		}
		// yanglint "-t data" is operational (violations become warnings); lyd_parse_data without
		// LYD_VALIDATE_OPERATIONAL is closest to "-t config" (ponytail: state-data cases mis-verify).
		a = append(a, "-t", "config", "-e")
		if !strings.Contains(c.Parse, "LYD_PARSE_STRICT") {
			a = append(a, "-n")
		}
	}
	for _, m := range c.Modules {
		a = append(a, filepath.Join(dir, m))
	}
	if c.Kind == "data" {
		a = append(a, filepath.Join(dir, c.DataFile))
	}
	got := "valid"
	if exec.Command("yanglint", a...).Run() != nil {
		got = "invalid"
	}
	if got == c.Verdict {
		c.Verified = "agree"
	} else {
		c.Verified = "differ (yanglint " + got + ")"
	}
}

func main() {
	out := flag.String("out", "", "output directory (outside the repo)")
	ver := flag.Bool("verify", false, "compare asserted verdict with system yanglint")
	flag.Parse()
	if *out == "" || flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: extract-utests [-verify] -out DIR tests/utests/.../x.c")
		os.Exit(2)
	}
	src, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	toks, defs := splitDefines(string(src))
	ip := &interp{defs: defs, vars: map[string]string{}, file: filepath.Base(flag.Arg(0)), skipped: map[string]int{}, unextracted: map[string]int{}}
	ip.run(toks)
	agree, differ, na := 0, 0, 0
	byKind := map[string]int{}
	for _, c := range ip.cases {
		if err := c.write(*out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		byKind[c.Kind+"/"+c.Verdict]++
		if !*ver {
			continue
		}
		c.verify(*out)
		if err := c.dump(filepath.Join(*out, c.ID)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
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
	fmt.Printf("%s: %d cases %v\n", ip.file, len(ip.cases), byKind)
	fmt.Printf("  skipped: %v\n  unextracted API calls: %v\n", ip.skipped, ip.unextracted)
	if *ver {
		fmt.Printf("  verify vs yanglint: agree=%d differ=%d n/a=%d\n", agree, differ, na)
	}
}
