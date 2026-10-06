// SPDX-License-Identifier: BSD-3-Clause

// Command portcov prints the port coverage report (issue #77): the libyang functions reachable
// from the pilot's entry points that docs/port-map.md does not list yet, minus the out-of-scope
// areas of the configuration file.
//
//	go run ./cmd/portcov -src ../.cache/libyang -portmap ../docs/port-map.md -config port-coverage.conf
//
// The call graph comes from the tags file of scripts/lyfn (universal-ctags function and macro
// ranges; a wrong end line is recomputed by brace matching) and the calls written in each body.
// Calls made through macros are followed into the macro body; calls through function pointers
// (plugin callbacks) are not seen, so the configuration names such callbacks as extra roots.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func main() {
	src := flag.String("src", "../.cache/libyang", "libyang source tree (with the tags file of scripts/lyfn)")
	portmap := flag.String("portmap", "../docs/port-map.md", "port map")
	config := flag.String("config", "port-coverage.conf", "roots and out-of-scope allowlist")
	flag.Parse()
	if err := run(os.Stdout, *src, *portmap, *config); err != nil {
		fmt.Fprintln(os.Stderr, "portcov:", err)
		os.Exit(1)
	}
}

// def is one function or macro definition from the tags file.
type def struct {
	name, file string
	first, end int
	macro      bool
}

// Config is the parsed configuration: entry points and the out-of-scope patterns.
type Config struct {
	Roots []string  // function names or globs
	Skip  []skipPat // file or function globs, each with its reason
}

type skipPat struct{ pattern, reason string }

// parseConfig reads lines "root <glob>" and "skip <glob> <reason>"; '#' starts a comment.
func parseConfig(r io.Reader) (*Config, error) {
	c := &Config{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		l := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = strings.TrimSpace(l[:i])
		}
		if l == "" {
			continue
		}
		f := strings.Fields(l)
		switch {
		case f[0] == "root" && len(f) == 2:
			c.Roots = append(c.Roots, f[1])
		case f[0] == "skip" && len(f) >= 3:
			c.Skip = append(c.Skip, skipPat{f[1], strings.Join(f[2:], " ")})
		default:
			return nil, fmt.Errorf("config line %d: want \"root <glob>\" or \"skip <glob> <reason>\"", n)
		}
	}
	return c, sc.Err()
}

// skipped returns the reason d is out of scope ("" when it is not): a pattern with a '/' matches
// the file, any other the function name.
func (c *Config) skipped(d def) string {
	for _, s := range c.Skip {
		subject := d.name
		if strings.Contains(s.pattern, "/") {
			subject = d.file
		}
		if ok, _ := path.Match(s.pattern, subject); ok {
			return s.reason
		}
	}
	return ""
}

// readTags parses a universal-ctags file (--fields=+ne): functions (f) and macros (d).
func readTags(r io.Reader) (map[string][]def, error) {
	defs := map[string][]def{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 4 || strings.HasPrefix(f[0], "!") {
			continue
		}
		d := def{name: f[0], file: f[1]}
		kind := ""
		for _, x := range f[3:] {
			switch {
			case len(x) == 1:
				kind = x
			case strings.HasPrefix(x, "kind:"):
				kind = x[5:]
			case strings.HasPrefix(x, "line:"):
				d.first, _ = strconv.Atoi(x[5:])
			case strings.HasPrefix(x, "end:"):
				d.end, _ = strconv.Atoi(x[4:])
			}
		}
		switch kind {
		case "f", "function":
		case "d", "macro":
			d.macro = true
		default:
			continue
		}
		defs[d.name] = append(defs[d.name], d)
	}
	return defs, sc.Err()
}

// fixEnds replaces the end line of functions ctags got wrong (missing, or before the start: some
// universal-ctags builds report such ends for libyang) by brace matching, as scripts/lyfn does.
func fixEnds(sc *source, defs map[string][]def) {
	for name, ds := range defs {
		for i, d := range ds {
			if d.end <= d.first && !d.macro {
				ds[i].end = braceEnd(sc.lines(d.file), d.first)
			}
		}
		defs[name] = ds
	}
}

var literal = regexp.MustCompile(`"([^"\\]|\\.)*"|'([^'\\]|\\.)*'|/\*.*?\*/|//.*$`)

// braceEnd is the line closing the body that starts at line first (1-based).
func braceEnd(lines []string, first int) int {
	depth, seen := 0, false
	for n := first; n <= len(lines); n++ {
		for _, c := range literal.ReplaceAllString(lines[n-1], "") {
			switch {
			case c == '{':
				depth++
				seen = true
			case c == '}':
				depth--
			case c == ';' && !seen:
				return n
			}
		}
		if seen && depth <= 0 {
			return n
		}
	}
	return first
}

var ident = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// readPortMap returns every identifier of the "C function" column of the port map.
func readPortMap(r io.Reader) (map[string]bool, error) {
	ported := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		cells := strings.Split(sc.Text(), "|")
		if len(cells) < 4 || strings.HasPrefix(strings.TrimSpace(cells[1]), "---") {
			continue
		}
		for _, id := range ident.FindAllString(cells[2], -1) {
			ported[id] = true
		}
	}
	return ported, sc.Err()
}

// source caches the lines of the libyang files.
type source struct {
	dir   string
	files map[string][]string
}

func (s *source) lines(file string) []string {
	l, ok := s.files[file]
	if !ok {
		b, _ := os.ReadFile(path.Join(s.dir, file)) //nolint:gosec // dev tool over the libyang tree
		l = strings.Split(string(b), "\n")
		s.files[file] = l
	}
	return l
}

var callRE = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// calls are the identifiers followed by '(' in the text of d (a function body, or a macro with
// its continuation lines), strings and comments removed: the functions and macros it calls.
// ponytail: a textual scan, not a C parser; cscope's scoping differs between builds (it
// attributes call sites to neighbouring functions on some), so the report does not use it.
func (s *source) calls(d def) []string {
	lines := s.lines(d.file)
	var body strings.Builder
	for n := d.first; n <= len(lines) && n >= 1; n++ {
		body.WriteString(literal.ReplaceAllString(lines[n-1], "") + "\n")
		if d.macro && !strings.HasSuffix(strings.TrimRight(lines[n-1], " \t"), "\\") || !d.macro && n >= d.end {
			break
		}
	}
	var out []string
	for i, m := range callRE.FindAllStringSubmatch(body.String(), -1) {
		if i == 0 || m[1] == d.name {
			continue // the definition's own name
		}
		out = append(out, m[1])
	}
	return out
}

// Row is one reachable function the port map does not list.
type Row struct {
	Name, Loc, From string
	Depth           int
}

// Report is the result of a walk.
type Report struct {
	Reachable, Ported, Skipped int
	Missing                    []Row
	SkipReasons                map[string]int // reason → functions left out
}

// walk is a breadth-first search over functions from the roots; skipped functions are neither
// counted nor descended into, macros are passed through.
func walk(cfg *Config, defs map[string][]def, ported map[string]bool, calls func(def) []string) (*Report, error) {
	rep := &Report{SkipReasons: map[string]int{}}
	type item struct {
		name, from string
		depth      int
	}
	var queue []item
	seen := map[string]bool{}
	var roots []string
	for name := range defs {
		for _, r := range cfg.Roots {
			if ok, _ := path.Match(r, name); ok {
				roots = append(roots, name)
				break
			}
		}
	}
	sort.Strings(roots)
	for _, r := range roots {
		seen[r] = true
		queue = append(queue, item{r, r, 0})
	}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		ds := defs[it.name]
		var fns []def
		for _, d := range ds {
			if !d.macro {
				fns = append(fns, d)
			}
		}
		next := []string{}
		if len(fns) > 0 {
			if reason := cfg.skipped(fns[0]); reason != "" {
				rep.Skipped++
				rep.SkipReasons[reason]++
				continue
			}
			rep.Reachable++
			if ported[it.name] {
				rep.Ported++
			} else {
				rep.Missing = append(rep.Missing, Row{it.name, fmt.Sprintf("%s:%d", fns[0].file, fns[0].first), it.from, it.depth})
			}
			for _, d := range fns {
				next = append(next, calls(d)...)
			}
		} else {
			for _, d := range ds { // a macro: what its body calls
				next = append(next, calls(d)...)
			}
		}
		for _, c := range next {
			if seen[c] || len(defs[c]) == 0 {
				continue
			}
			seen[c] = true
			depth := it.depth
			if len(fns) > 0 {
				depth++
			}
			queue = append(queue, item{c, it.from, depth})
		}
	}
	sort.Slice(rep.Missing, func(i, j int) bool {
		a, b := rep.Missing[i], rep.Missing[j]
		if a.Depth != b.Depth {
			return a.Depth < b.Depth
		}
		return a.Name < b.Name
	})
	return rep, nil
}

// Markdown renders the report.
func (r *Report) Markdown(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "## Port coverage\n\nFunctions reachable from the pilot entry points: **%d**; listed in docs/port-map.md: **%d**; "+
		"not listed: **%d**. Out of scope (not followed): %d.\n\n", r.Reachable, r.Ported, len(r.Missing), r.Skipped)
	if len(r.SkipReasons) > 0 {
		var reasons []string
		for k := range r.SkipReasons {
			reasons = append(reasons, k)
		}
		sort.Strings(reasons)
		b.WriteString("| out of scope | functions |\n|---|--:|\n")
		for _, k := range reasons {
			fmt.Fprintf(&b, "| %s | %d |\n", k, r.SkipReasons[k])
		}
		b.WriteString("\n")
	}
	b.WriteString("Calls through function pointers are not followed; see the roots in port-coverage.conf.\n")
	b.WriteString("\n| function | file:line | reachable from | depth |\n|---|---|---|--:|\n")
	for _, m := range r.Missing {
		fmt.Fprintf(&b, "| %s | %s | %s | %d |\n", m.Name, m.Loc, m.From, m.Depth)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func run(w io.Writer, src, portmap, config string) error {
	cf, err := os.Open(config) //nolint:gosec // dev tool, caller-chosen paths
	if err != nil {
		return err
	}
	defer cf.Close() //nolint:errcheck // read-only
	cfg, err := parseConfig(cf)
	if err != nil {
		return err
	}
	tf, err := os.Open(path.Join(src, "tags")) //nolint:gosec // dev tool
	if err != nil {
		return fmt.Errorf("%w (run scripts/lyfn -n once to build the tags file)", err)
	}
	defer tf.Close() //nolint:errcheck // read-only
	defs, err := readTags(tf)
	if err != nil {
		return err
	}
	sc := &source{dir: src, files: map[string][]string{}}
	fixEnds(sc, defs)
	pf, err := os.Open(portmap) //nolint:gosec // dev tool
	if err != nil {
		return err
	}
	defer pf.Close() //nolint:errcheck // read-only
	ported, err := readPortMap(pf)
	if err != nil {
		return err
	}
	rep, err := walk(cfg, defs, ported, sc.calls)
	if err != nil {
		return err
	}
	return rep.Markdown(w)
}
