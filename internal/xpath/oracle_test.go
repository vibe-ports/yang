//go:build oracle

// SPDX-License-Identifier: BSD-3-Clause

// Live replay against libyang (lyoracle, conformance/oracle):
//
//	./dev go test -tags oracle ./internal/xpath/                # compare
//	./dev go test -tags oracle ./internal/xpath/ -run Oracle -update   # regenerate testdata/oracle-pv2.jsonl
//
// testdata/cases.jsonl lists the expressions; the stored file keeps libyang's
// result / error JSON verbatim (json.RawMessage), so strings stay byte-exact.
package xpath

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "regenerate testdata/oracle-pv2.jsonl from lyoracle")

// oracleSets are the lyoracle request bases of the case sets (dir conformance/corpus/protocol-v2).
var oracleSets = map[string]string{
	"":        `"data_type":"get","modules":[{"name":"pv2","features":["extra"]}],"data_file":"data/xpath.json"`,
	"union":   `"data_type":"get","modules":[{"name":"pv2"},{"name":"pv2-xp"}],"data_file":"data/xpath-union.json"`,
	"aug":     `"data_type":"get","modules":[{"name":"pv2","features":["extra"]},{"name":"pv2-aug"},{"name":"pv2-xp"}],"data_file":"data/xpath-aug.json"`,
	"hash":    `"data_type":"get","modules":[{"name":"pv2"},{"name":"pv2-hk"}],"data_file":"data/xpath-hash.json"`,
	"opq":     `"data_type":"get","modules":[{"name":"pv2"}],"data_file":"data/xpath-opq.json","parse_only":true,"unknown":"opaque"`,
	"anydata": `"data_type":"get","modules":[{"name":"pv2"}],"data_file":"data/xpath-anydata.json"`,
}

// oracleUname is oracleArch (GOARCH) as `uname -m`, the suffix of the lyoracle binary.
const oracleUname = "x86_64"

func lyoracle(t *testing.T) string {
	if p := os.Getenv("LYORACLE"); p != "" {
		return p
	}
	prefix := os.Getenv("LIBYANG_PREFIX")
	if prefix == "" {
		prefix = "/opt/libyang"
	}
	out, err := exec.Command("make", "-s", "-C", "../../conformance/oracle", "LIBYANG_PREFIX="+prefix).CombinedOutput()
	if err != nil {
		if os.Getenv("YANG_ORACLE_REQUIRED") != "" {
			t.Fatalf("building lyoracle: %v\n%s", err, out)
		}
		t.Skipf("lyoracle not available: %v", err)
	}
	p, _ := filepath.Abs("../../conformance/oracle/lyoracle-" + oracleUname)
	return p
}

// runOracle evaluates one case with libyang.
func runOracle(t *testing.T, bin string, c oracleCase) oracleCase {
	req := `{"op":"xpath","base_dir":"protocol-v2","searchdirs":["schemas"],"format":"json",` +
		oracleSets[c.Set]
	if c.CP != "" {
		cp, _ := json.Marshal(c.CP)
		req += `,"context_path":` + string(cp)
	}
	x, _ := json.Marshal(c.X)
	req += `,"xpath":` + string(x) + "}"
	cmd := exec.Command(bin)
	cmd.Dir = "../../conformance/corpus"
	cmd.Stdin = strings.NewReader(req)
	out, err := cmd.Output()
	r := oracleCase{Set: c.Set, CP: c.CP, X: c.X}
	var resp struct {
		Verdict     string
		Arch        string
		Result      json.RawMessage
		Diagnostics []struct {
			Level string
			Code  struct{ Name string }
			VE    string          `json:"vecode_name"`
			Msg   json.RawMessage `json:"msg"`
		}
	}
	if err != nil || json.Unmarshal(out, &resp) != nil || resp.Verdict == "" {
		r.Crash = true
		return r
	}
	if resp.Arch != oracleUname {
		t.Fatalf("oracle arch %q, want %s: foreign or stale lyoracle, rebuild with make oracle", resp.Arch, oracleUname)
	}
	if resp.Verdict == "valid" {
		r.Res = compact(t, resp.Result)
		return r
	}
	for _, d := range resp.Diagnostics {
		if d.Level == "error" {
			e, _ := json.Marshal(map[string]json.RawMessage{"err": quote(d.Code.Name), "vecode": quote(d.VE), "msg": d.Msg})
			r.Error = e
			return r
		}
	}
	t.Fatalf("%q: verdict %s without error diagnostic: %s", c.X, resp.Verdict, out)
	return r
}

func quote(s string) json.RawMessage { b, _ := json.Marshal(s); return b }

func compact(t *testing.T, b []byte) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestOracleLive asks libyang about every case and compares with the stored
// results (or rewrites them with -update), then replays them against Go.
func TestOracleLive(t *testing.T) {
	if runtime.GOARCH != oracleArch {
		// libyang computes in long double, whose width depends on the host
		// (80-bit x87 on amd64, 128-bit on arm64): string(0.15) differs.
		if *update {
			t.Fatalf("regenerate on %s: DEV_PLATFORM=linux/%s ./dev go test -tags oracle ./internal/xpath/ -run Oracle -update", oracleArch, oracleArch)
		}
		t.Skipf("live libyang comparison skipped: the canonical oracle runs on %s, this is %s (stored results are replayed by TestCompileOracle)", oracleArch, runtime.GOARCH)
	}
	bin := lyoracle(t)
	var live []oracleCase
	for _, c := range readCases(t, "cases.jsonl") {
		live = append(live, runOracle(t, bin, c))
	}
	if *update {
		var buf bytes.Buffer
		buf.WriteString(`{"arch":"` + runtime.GOARCH + `"}` + "\n")
		for _, c := range live {
			b, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			buf.Write(append(b, '\n'))
		}
		if err := os.WriteFile("testdata/oracle-pv2.jsonl", buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stored := readCases(t, "oracle-pv2.jsonl")
	if len(stored) != len(live) {
		t.Fatalf("stored %d cases, live %d: run with -update", len(stored), len(live))
	}
	for i, l := range live {
		s, _ := json.Marshal(stored[i])
		b, _ := json.Marshal(l)
		if !bytes.Equal(s, b) {
			t.Errorf("libyang result changed (run with -update):\n stored %s\n live   %s", s, b)
		}
	}
	replay(t, live)
}
