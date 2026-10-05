// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

// Differential test against libyang (yanglint):
//
//	./dev go test -tags oracle -run Oracle -v ./internal/parser/
//
// YANGLINT overrides the binary (default: yanglint from PATH).
package parser

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func yanglint(t *testing.T, src string, args ...string) (string, bool) {
	t.Helper()
	bin := os.Getenv("YANGLINT")
	if bin == "" {
		bin = "yanglint"
	}
	if _, err := exec.LookPath(bin); err != nil {
		if os.Getenv("YANG_ORACLE_REQUIRED") != "" {
			t.Fatalf("yanglint not found: %v", err)
		}
		t.Skip("yanglint not found")
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "m.yang")
	if err := os.WriteFile(f, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, append(append([]string{"-p", dir}, args...), f)...).CombinedOutput() //nolint:gosec // test oracle
	return string(out), err == nil
}

var lyLine = regexp.MustCompile(`^(.*) \(line (\d+)\)$`)

// lyError is libyang's first error: message and line (0 when it reports a path or nothing).
func lyError(out string) (string, int) {
	for _, s := range strings.Split(out, "\n") {
		if msg, ok := strings.CutPrefix(s, "libyang err : "); ok {
			if m := lyLine.FindStringSubmatch(msg); m != nil {
				n, _ := strconv.Atoi(m[2])
				return m[1], n
			}
			if i := strings.LastIndex(msg, " (/"); i > 0 {
				msg = msg[:i]
			}
			return msg, 0
		}
	}
	return "", 0
}

// oracleDiff reports how libyang's verdict on src differs from rejecting it with msg at line.
func oracleDiff(t *testing.T, src string, line int, msg string) string {
	t.Helper()
	out, ok := yanglint(t, src)
	lmsg, lline := lyError(out)
	if ok || lmsg != msg || lline != line {
		return fmt.Sprintf("libyang ok=%v line %d: %s\n    ours line %d: %s", ok, lline, lmsg, line, msg)
	}
	return ""
}

// oracleError checks that libyang rejects src with msg at line.
func oracleError(t *testing.T, src string, line int, msg string) {
	t.Helper()
	if d := oracleDiff(t, src, line, msg); d != "" {
		t.Errorf("%q\n %s", src, d)
	}
}

func TestOracleParseErrors(t *testing.T) {
	for _, c := range parseErrors {
		oracleError(t, c.src, c.line, c.msg)
	}
	if out, ok := yanglint(t, hdr+"  extension e;\n  m:e x { ;; }\n}"); !ok {
		t.Errorf("';' in extension instance: %s", out)
	}
}

// TestOracleG1 checks the recorded yanglint verdicts of the G1 cases.
func TestOracleG1(t *testing.T) {
	for i, c := range g1Cases {
		if out, ok := yanglint(t, c.src); ok != c.ok {
			t.Errorf("G1 #%d %s: yanglint ok=%v, recorded %v: %s", i+1, c.name, ok, c.ok, out)
		}
	}
}

// TestOracleErrors: libyang rejects every buildErrors / iffErrors module with
// the same message and line (0: libyang reports a schema path).
func TestOracleErrors(t *testing.T) {
	for _, c := range buildErrors {
		if !strings.HasPrefix(c.src, "submodule") { // yanglint does not parse a submodule alone
			oracleError(t, c.src, c.line, c.msg)
		}
	}
	for _, c := range iffErrors {
		if strings.HasSuffix(c.msg, "processing error.") {
			// D-0021: libyang rejects, but its message is architecture-dependent
			// ("processing error." on arm64, empty on x86_64), so compare the verdict only.
			if out, ok := yanglint(t, c.src); ok {
				t.Errorf("%q: libyang accepted, we reject (D-0021): %s", c.src, out)
			}
			continue
		}
		oracleError(t, c.src, 0, c.msg)
	}
}

// corpusKnown are corpus files libyang rejects at load time for a reason
// Parse leaves to others: the argument of an extension defined in an imported
// module (the compiler).
var corpusKnown = map[string]bool{"issue728.yang": true}

// extResolution tells libyang's extension-resolution errors, reported with a
// schema path (line 0) after parsing, which Parse makes too.
func extResolution(msg string) bool {
	return strings.Contains(msg, "used for extension instance identifier.") ||
		strings.HasPrefix(msg, "Extension definition of extension instance ") ||
		strings.HasPrefix(msg, "Extension instance ") && strings.Contains(msg, " missing argument ")
}

// TestOracleCorpus: on libyang's own modules and fuzz corpus (or $ORACLE_CORPUS), Parse rejects
// exactly what libyang rejects while parsing, with the same message and line.
func TestOracleCorpus(t *testing.T) {
	root := libyangSrc()
	if r := os.Getenv("ORACLE_CORPUS"); r != "" { // e.g. a YangModels checkout
		root = r
	}
	if root == "" {
		if os.Getenv("YANG_ORACLE_REQUIRED") != "" {
			t.Fatal("no libyang corpus: .cache/libyang (make libyang-src) or the dev image's /opt/libyang/src")
		}
		t.Skip("no libyang corpus (make libyang-src)")
	}
	n := 0
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".yang") && !strings.Contains(p, "lys_parse_mem") {
			return nil
		}
		src, _ := os.ReadFile(p) //nolint:gosec // local test corpus
		_, err = Parse("", src, nil)
		var e *Error
		if errors.As(err, &e) {
			n++
			if d := oracleDiff(t, string(src), e.Pos.Line, e.Msg); d != "" {
				t.Errorf("%s\n %s", p, d)
			}
		} else if out, ok := yanglint(t, string(src)); !ok && !corpusKnown[filepath.Base(p)] {
			if msg, line := lyError(out); line > 0 || extResolution(msg) { // what Parse checks
				t.Errorf("%s: accepted, libyang line %d: %s", p, line, msg)
			}
		}
		return nil
	})
	t.Logf("%d rejected files compared", n)
}

// TestOracleFidelity compares argument strings with libyang's YIN output.
func TestOracleFidelity(t *testing.T) {
	for _, c := range fidelity {
		out, ok := yanglint(t, mod("1.1", c.src), "-f", "yin")
		if !ok {
			t.Errorf("%q: yanglint failed: %s", c.src, out)
			continue
		}
		d := xml.NewDecoder(bytes.NewReader([]byte(out)))
		var text string
		for in := false; ; {
			tok, err := d.Token()
			if err != nil {
				break
			}
			switch tok := tok.(type) {
			case xml.StartElement:
				in = tok.Name.Local == "text"
			case xml.CharData:
				if in && text == "" {
					text = string(tok)
				}
			case xml.EndElement:
				in = false
			}
		}
		if text != c.want {
			t.Errorf("%q: libyang %q, recorded %q", c.src, text, c.want)
		}
	}
}
