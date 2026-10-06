// SPDX-License-Identifier: BSD-3-Clause

// Command golden runs every corpus fixture through lyoracle and writes (default) or compares
// (-check) the goldens byte-exactly. Run it from the conformance module root.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/vibe-ports/yang/conformance"
)

const timeout = 60 * time.Second

func main() {
	check := flag.Bool("check", false, "compare with the committed goldens instead of writing")
	run := flag.String("run", "", "only fixtures whose id matches this regexp")
	oracle := flag.String("oracle", conformance.OracleBinary("oracle"), "path to the lyoracle binary")
	manifest := flag.String("manifest", "corpus/manifest.yaml", "fixture manifest")
	reqProto := flag.Bool("require-protocol", false, "fail unless the response has protocol == 2 (oracle v2)")
	flag.Parse()
	if err := do(*check, *run, *oracle, *manifest, *reqProto); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func do(check bool, run, oracle, manifest string, reqProto bool) error {
	re, err := regexp.Compile(run)
	if err != nil {
		return err
	}
	m, err := conformance.LoadManifest(manifest)
	if err != nil {
		return err
	}
	if err := m.CheckFiles(check); err != nil {
		return err
	}
	oracle, err = filepath.Abs(oracle)
	if err != nil {
		return err
	}
	bad := 0
	for _, f := range m.Fixtures {
		if !re.MatchString(f.ID) {
			continue
		}
		if check && !f.RunsOn(runtime.GOARCH) {
			fmt.Println("skip", f.ID, "(golden pinned to", f.Host+")")
			continue
		}
		resp, err := runOracle(oracle, m.Corpus(), f, reqProto, !check)
		if err != nil {
			return fmt.Errorf("harness failure on %s: %w", f.ID, err)
		}
		text, err := resp.Marshal()
		if err != nil {
			return fmt.Errorf("%s: %w", f.ID, err)
		}
		path := m.GoldenPath(f)
		if check {
			old, rerr := os.ReadFile(path) //nolint:gosec // dev tool, manifest-derived path
			same := rerr == nil && bytes.Equal(old, text)
			if !same {
				bad++
			}
			fmt.Println(map[bool]string{true: "ok  ", false: "DIFF"}[same], f.ID, resp.Verdict())
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(path, text, 0o600); err != nil {
			return err
		}
		fmt.Println("wrote", f.ID, resp.Verdict())
	}
	if bad > 0 {
		return fmt.Errorf("%d golden(s) differ", bad)
	}
	return nil
}

func runOracle(oracle, corpus string, f conformance.Fixture, reqProto, write bool) (conformance.Response, error) {
	req := map[string]any{"base_dir": f.Dir}
	for k, v := range f.Request {
		req[k] = v
	}
	in, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, oracle)
	cmd.Dir = corpus
	// libyang prints date-and-time in the local zone; goldens are made in UTC (D-0025)
	cmd.Env = append(os.Environ(), "TZ=UTC")
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %.300s %.300s", err, stderr.String(), stdout.String())
	}
	resp, err := conformance.ParseResponse(stdout.Bytes())
	if err != nil {
		return nil, fmt.Errorf("bad oracle output: %w", err)
	}
	if v := resp.Verdict(); v == "" || v == "request-error" {
		return nil, fmt.Errorf("verdict %q: %.300s", v, stdout.String())
	}
	if err := conformance.CheckOracleArch(resp, write); err != nil {
		return nil, err
	}
	delete(resp, "arch") // goldens must not depend on the oracle build
	if p, _ := resp["protocol"].(json.Number); reqProto && p != "2" {
		return nil, fmt.Errorf("protocol %q, want 2: %.300s", p, stdout.String())
	}
	return resp, nil
}
