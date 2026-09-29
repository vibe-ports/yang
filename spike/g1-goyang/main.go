// Spike for gate G1 (docs/decisions/0001-parser-reuse.md): can goyang's
// pkg/yang parser replace a port of libyang's parser_yang.c?
//
//	go run . cases              # accept/reject table: goyang Parse, goyang Parse+AST, yanglint
//	go run . fidelity           # argument strings: goyang vs libyang (via yanglint -f yin)
//	go run . bench <dir>        # time goyang Parse / Parse+AST over every *.yang in dir
//
// Cases marked [ly-utest] are adapted from libyang v5.8.6
// tests/utests/schema/test_yang.c (BSD-3, (c) CESNET); the rest are written
// from RFC 7950 §6 / §14 rules.
package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openconfig/goyang/pkg/yang"
)

func mod(ver, body string) string {
	v := ""
	if ver != "" {
		v = "  yang-version " + ver + ";\n"
	}
	return "module m {\n" + v + "  namespace \"urn:m\";\n  prefix m;\n" + body + "\n}\n"
}

type tc struct{ name, src string }

var cases = []tc{
	{"baseline valid module", mod("1.1", `  leaf a { type string; }`)},
	{`bad escape "\x" (1.1)`, mod("1.1", `  description "a\x";`)},
	{`bad escape "\x" (1.0)`, mod("", `  description "a\x";`)},
	{`bad escape "\s" [ly-utest]`, mod("1.1", `  description "\s";`)},
	{`pattern "\d" in dquotes (1.1)`, mod("1.1", `  leaf a { type string { pattern "\d+"; } }`)},
	{`single-quoted '\x' is literal [ly-utest]`, mod("1.1", `  description 'a\x';`)},
	{"unknown keyword, no prefix [ly-utest]", mod("1.1", `  container c { not-a-statement-nor-extension x; }`)},
	{"extension use, prefix not imported", mod("1.1", `  container c { zz:foo "a"; }`)},
	{"extension use, defined + own prefix", mod("1.1", "  extension e { argument v; }\n  container c { m:e \"a\"; }")},
	{"extension use, undefined ext own prefix", mod("1.1", `  container c { m:nosuch "a"; }`)},
	{"duplicate description in leaf", mod("1.1", `  leaf a { type string; description x; description y; }`)},
	{"duplicate type in leaf", mod("1.1", `  leaf a { type string; type int8; }`)},
	{"leaf without type", mod("1.1", `  leaf a { description x; }`)},
	{"module without namespace [ly-utest]", "module m { prefix m; }\n"},
	{"identifier starts with digit", mod("1.1", `  leaf 1abc { type string; }`)},
	{"identifier with bad char", mod("1.1", `  leaf a#b { type string; }`)},
	{"prefixed identifier pre:pre:x [ly-utest]", mod("1.1", `  leaf a { type m:m:string; }`)},
	{"yang-version 2", mod("2", "")},
	{`yang-version "1.1" quoted`, mod(`"1.1"`, `  leaf a { type string; }`)},
	{"duplicate yang-version [ly-utest]", mod("1.1", "  yang-version 1.1;")},
	{"stray ';' in module body", mod("1.1", "  ;\n  leaf a { type string; }")},
	{"';' after closing brace", mod("1.1", `  leaf a { type string; };`)},
	{`concat "a" + 'b' [ly-utest]`, mod("1.1", `  description "a" + 'b';`)},
	{`concat "a" + b (unquoted) [ly-utest]`, mod("1.1", `  description "a" + b;`)},
	{"unquoted arg followed by '\"' [ly-utest]", mod("1.1", `  description hello"x";`)},
	{"unterminated block comment [ly-utest]", mod("1.1", "  /* never closed\n")},
	{"unquoted arg containing //", mod("1.1", `  leaf a { type string; units abc//def; }`)},
	{"keyword given as quoted string", mod("1.1", `  "leaf" a { type string; }`)},
	{"missing argument [ly-utest]", mod("1.1", `  leaf { type string; }`)},
	{`empty identifier "" [ly-utest]`, mod("1.1", `  leaf "" { type string; }`)},
	{"missing closing brace", strings.TrimSuffix(mod("1.1", ""), "}\n")},
	{"extra closing brace", mod("1.1", "") + "}\n"},
	{"trailing garbage after module [ly-utest]", mod("1.1", "") + "module q { namespace urn:q; prefix q; }\n"},
	{"mandatory maybe", mod("1.1", `  leaf a { type string; mandatory maybe; }`)},
	{"config True", mod("1.1", `  container c { config True; }`)},
	{"bad revision date 2020-13-45", mod("1.1", `  revision 2020-13-45;`)},
	{"min-elements -1 [ly-utest]", mod("1.1", `  leaf-list a { type string; min-elements -1; }`)},
	{"position under enum [ly-utest]", mod("1.1", `  leaf a { type enumeration { enum x { position 1; } } }`)},
	{"import after body stmt (order)", mod("1.1", "  container c;\n  import ietf-yang-types { prefix yt; }")},
	{"control char U+0001 in string", mod("1.1", "  description \"a\x01b\";")},
	{"CRLF line endings", strings.ReplaceAll(mod("1.1", `  leaf a { type string; }`), "\n", "\r\n")},
	{"input outside rpc/action", mod("1.1", `  container c { input { leaf x { type string; } } }`)},
	// valid YANG 1.1 that occurs in published IETF modules (goyang false rejects in the corpus run)
	{"valid: two augments in one uses", mod("1.1", "  grouping g { container a; container b; }\n  container t { uses g { augment a { leaf x { type string; } } augment b { leaf y { type string; } } } }")},
	{"valid: choice as shorthand case of choice", mod("1.1", `  container t { choice c { choice d { leaf x { type string; } } } }`)},
	{"valid: when { reference }", mod("1.1", `  container t { when "true()" { reference r; } }`)},
}

func yanglint(src string) (bool, string) {
	dir, _ := os.MkdirTemp("", "g1")
	defer os.RemoveAll(dir)
	f := filepath.Join(dir, "m.yang")
	os.WriteFile(f, []byte(src), 0o644)
	out, err := exec.Command("yanglint", "-p", dir, f).CombinedOutput()
	return err == nil, firstLine(string(out))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "libyang err : ")
	if len(s) > 70 {
		s = s[:70] + "…"
	}
	return s
}

func verdict(ok bool) string {
	if ok {
		return "accept"
	}
	return "reject"
}

func runCases() {
	fmt.Println("| # | case | goyang Parse | goyang +AST | yanglint | match(AST) |")
	fmt.Println("|---|---|---|---|---|---|")
	var mParse, mAST int
	for i, c := range cases {
		_, perr := yang.Parse(c.src, "m.yang")
		aerr := yang.NewModules().Parse(c.src, "m.yang")
		lok, lmsg := yanglint(c.src)
		pOK, aOK := perr == nil, aerr == nil
		if pOK == lok {
			mParse++
		}
		m := "no"
		if aOK == lok {
			mAST++
			m = "yes"
		}
		fmt.Printf("| %d | %s | %s | %s | %s | %s |\n", i+1, c.name, verdict(pOK), verdict(aOK), verdict(lok), m)
		if os.Getenv("V") != "" {
			fmt.Fprintf(os.Stderr, "%d goyang: %v\n   AST: %v\n   yanglint: %s\n", i+1, perr, aerr, lmsg)
		}
	}
	fmt.Printf("\nagreement with yanglint: Parse %d/%d, Parse+AST %d/%d\n", mParse, len(cases), mAST, len(cases))
}

// Fidelity: each case's description argument, goyang vs libyang (YIN <text>).
var fid = []tc{
	{"trim ws before newline + indent [ly-utest]", "  description \"hello \t\n\t\t     world!\";"},
	{"escaped \\t before newline kept [ly-utest]", "  description \"hello \\t\n\t\t     \\t world!\";"},
	{"ws after escaped \\n is not indent [ly-utest]", "  description \"hello\\n\t\t world!\";"},
	{"indent removal stops at quote column", "  description \"a\n     b\n        c\";"},
	{"tab counted as 8 cols", "  description \"a\n\t  b\";"},
	{"concat across lines [ly-utest]", "  description \"hel\"  +\t\n  \"lo\";"},
	{"single-quoted multi-line kept verbatim", "  description 'a  \n     b';"},
	{"escapes \\n \\t \\\" \\\\", `  description "x\n\t\"\\y";`},
	{"unicode", `  description "ünï ✓";`},
	{"trailing ws on last line kept", "  description \"a  \";"},
	{"CRLF inside dquoted string", "  description \"a\r\n   b\";"},
}

func yinTexts(src string) ([]string, error) {
	dir, _ := os.MkdirTemp("", "g1")
	defer os.RemoveAll(dir)
	f := filepath.Join(dir, "m.yang")
	os.WriteFile(f, []byte(src), 0o644)
	out, err := exec.Command("yanglint", "-f", "yin", f).Output()
	if err != nil {
		return nil, err
	}
	var texts []string
	d := xml.NewDecoder(bytes.NewReader(out))
	in := false
	for {
		t, err := d.Token()
		if err != nil {
			break
		}
		switch t := t.(type) {
		case xml.StartElement:
			in = t.Name.Local == "text"
		case xml.CharData:
			if in {
				texts = append(texts, string(t))
			}
		case xml.EndElement:
			in = false
		}
	}
	return texts, nil
}

func runFidelity() {
	fmt.Println("| # | string case | goyang | libyang | equal |")
	fmt.Println("|---|---|---|---|---|")
	eq := 0
	for i, c := range fid {
		src := mod("1.1", c.src)
		ss, err := yang.Parse(src, "m.yang")
		g := fmt.Sprint(err)
		if err == nil {
			for _, s := range ss[0].SubStatements() {
				if s.Keyword == "description" {
					g = fmt.Sprintf("%q", s.Argument)
				}
			}
		}
		l := "error"
		if t, err := yinTexts(src); err == nil && len(t) > 0 {
			l = fmt.Sprintf("%q", t[0])
		}
		e := "no"
		if g == l {
			e, eq = "yes", eq+1
		}
		fmt.Printf("| %d | %s | `%s` | `%s` | %s |\n", i+1, c.name, g, l, e)
	}
	fmt.Printf("\nequal: %d/%d\n", eq, len(fid))
	// Positions / order / extensions on the raw tree.
	src := mod("1.1", "  extension e { argument v; }\n  m:e \"first\";\n  container c {\n    m:e 'x' + \"y\" { m:e z; }\n    leaf a { type string; }\n  }")
	ss, _ := yang.Parse(src, "m.yang")
	var walk func(s *yang.Statement, d int)
	walk = func(s *yang.Statement, d int) {
		fmt.Printf("%s%s %q @%s\n", strings.Repeat("  ", d), s.Keyword, s.Argument, s.Location())
		for _, c := range s.SubStatements() {
			walk(c, d+1)
		}
	}
	fmt.Println("\nraw tree (order, ext args, positions):")
	walk(ss[0], 0)
}

func runBench(dir string) {
	var files []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".yang") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	var srcs []string
	var bytesTotal int
	for _, f := range files {
		b, _ := os.ReadFile(f)
		srcs = append(srcs, string(b))
		bytesTotal += len(b)
	}
	best := func(fn func() int) (time.Duration, int) {
		var bd time.Duration
		var fails int
		for r := 0; r < 5; r++ {
			t := time.Now()
			fails = fn()
			if d := time.Since(t); r == 0 || d < bd {
				bd = d
			}
		}
		return bd, fails
	}
	dp, fp := best(func() int {
		n := 0
		for i, s := range srcs {
			if _, err := yang.Parse(s, files[i]); err != nil {
				n++
				if os.Getenv("V") != "" {
					fmt.Fprintln(os.Stderr, filepath.Base(files[i]), err)
				}
			}
		}
		return n
	})
	da, fa := best(func() int {
		n := 0
		for i, s := range srcs {
			if err := yang.NewModules().Parse(s, files[i]); err != nil {
				n++
				if os.Getenv("V") != "" {
					fmt.Fprintln(os.Stderr, filepath.Base(files[i]), err)
				}
			}
		}
		return n
	})
	mb := float64(bytesTotal) / 1e6
	fmt.Printf("files=%d size=%.1fMB\n", len(files), mb)
	fmt.Printf("goyang Parse     : %v (%.0f MB/s), failures=%d\n", dp, mb/dp.Seconds(), fp)
	fmt.Printf("goyang Parse+AST : %v (%.0f MB/s), failures=%d\n", da, mb/da.Seconds(), fa)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: g1 cases|fidelity|bench <dir>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "cases":
		runCases()
	case "fidelity":
		runFidelity()
	case "bench":
		runBench(os.Args[2])
	}
}
