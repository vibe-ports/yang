// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/snap"
)

// TestStage3FailedOpCleanupOrderOracle: operation data parsed into a retained request fails on
// a late value, and lyd_parse_op frees its parsed set backwards (tree_data.c:482-489). Some
// instances get a further "a" key leaf after they were linked, so where later insertions land
// depends on the retained topology the cleanup leaves. Replies (XML, JSON; user-ordered output)
// and RESTCONF input (system-ordered lists with retained RB trees) are compared step by step
// with a native probe; no golden files.
func TestStage3FailedOpCleanupOrderOracle(t *testing.T) {
	if os.Getenv("YANG_ORACLE_REQUIRED") == "" {
		t.Skip("run with YANG_ORACLE_REQUIRED=1 in the dev container")
	}
	const module = `module stage3-frc { yang-version 1.1; namespace "urn:stage3-frc"; prefix s;
 rpc r {
  input { list l { key "a b"; leaf a { type int32; } leaf b { type int32; } } leaf v { type int8; } }
  output { list l { key "a b"; leaf a { type int32; } leaf b { type int32; } } leaf v { type int8; } } } }`
	base := t.TempDir()
	model := filepath.Join(base, "stage3-frc.yang")
	if err := os.WriteFile(model, []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	prefix := os.Getenv("LIBYANG_PREFIX")
	if prefix == "" {
		prefix = "/opt/libyang"
	}
	probe := filepath.Join(base, "op-cleanup-probe")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	//nolint:gosec // Fixed test-only source and flags, private temporary output.
	cmd := exec.CommandContext(ctx, "cc", "-O2", "-Wall", "-Wextra", "-Werror", "-std=c11",
		"-I"+filepath.Join(prefix, "include"), "../conformance/oracle/stage3_reply_cleanup.c",
		"-L"+filepath.Join(prefix, "lib"), "-Wl,-rpath,"+filepath.Join(prefix, "lib"), "-lyang", "-o", probe)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile native probe: %v: %s", err, out)
	}
	s := snap.New(reviewDiffSet(t, module))
	run := func(t *testing.T, mode, in string, steps ...string) {
		t.Helper()
		got, refused := opCleanupSteps(t, s, mode, in, steps)
		if refused {
			// D-0121: the cleanup refused a removal that leaves libyang a detached RB member
			// (natively a dangling node, which can crash the probe).
			return
		}
		//nolint:gosec // Executes the native probe built immediately above.
		native, err := exec.CommandContext(ctx, probe, append([]string{model, mode, in}, steps...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("native probe: %v: %s", err, native)
		}
		if got != string(native) {
			t.Fatalf("in %s steps %q\nnative:\n%s\nGo:\n%s", in, steps, native, got)
		}
	}
	xml := func(a, b string) string { return `<l xmlns="urn:stage3-frc"><a>` + a + `</a><b>` + b + `</b></l>` }
	json := func(a, b string) string { return `{"a":` + a + `,"b":` + b + `}` }
	// The reviewed history: B=(1,15), A=(1,10) with a further a=20, a failed reply adding (1,0)
	// and (1,30), then (1,17).
	history := []string{"l:1:15", "l:1:10", "a:1:20", "d", "p", "d", "l:1:17", "d"}
	t.Run("xml", func(t *testing.T) {
		run(t, "xml", xml("1", "0")+xml("1", "30")+`<v xmlns="urn:stage3-frc">bad</v>`, history...)
	})
	t.Run("json", func(t *testing.T) {
		run(t, "json", `{"stage3-frc:l":[`+json("1", "0")+","+json("1", "30")+`],"stage3-frc:v":"bad"}`, history...)
	})
	t.Run("restconf", func(t *testing.T) {
		run(t, "restconf", `{"stage3-frc:input":{"l":[`+json("1", "0")+","+json("1", "30")+`],"v":"bad"}}`, history...)
	})
	// Seeded random histories: retained instances (some parsed first), further keys added to
	// already linked instances, a failed parse, and later insertions.
	n := 150
	if testing.Short() {
		n = 20
	}
	rng := rand.New(rand.NewPCG(1, 2))
	for it := range 3 * n {
		mode := []string{"xml", "json", "restconf"}[it%3]
		kr := []int{3, 6, 40}[it/3%3]
		k := func() string { return strconv.Itoa(rng.IntN(kr)) }
		doc := func(m int, bad bool) string {
			var xs, js []string
			for range m {
				a, b := k(), k()
				xs, js = append(xs, xml(a, b)), append(js, json(a, b))
			}
			switch {
			case mode == "xml" && bad:
				return strings.Join(xs, "") + `<v xmlns="urn:stage3-frc">bad</v>`
			case mode == "xml":
				return strings.Join(xs, "")
			case mode == "json" && bad:
				return `{"stage3-frc:l":[` + strings.Join(js, ",") + `],"stage3-frc:v":"bad"}`
			case mode == "json":
				return `{"stage3-frc:l":[` + strings.Join(js, ",") + `]}`
			case bad:
				return `{"stage3-frc:input":{"l":[` + strings.Join(js, ",") + `],"v":"bad"}}`
			}
			return `{"stage3-frc:input":{"l":[` + strings.Join(js, ",") + `]}}`
		}
		var steps []string
		if m := rng.IntN(8); m > 0 {
			steps = append(steps, "p:"+doc(m, false))
		}
		for i := range 1 + rng.IntN(12) {
			steps = append(steps, "l:"+k()+":"+k())
			if rng.IntN(3) == 0 {
				steps = append(steps, "a:"+strconv.Itoa(rng.IntN(i+1))+":"+k())
			}
		}
		steps = append(steps, "d", "p", "d", "l:"+k()+":"+k(), "d", "l:"+k()+":"+k(), "d")
		in := doc(2+rng.IntN(12), true)
		t.Run(fmt.Sprint(mode, it), func(t *testing.T) { run(t, mode, in, steps...) })
	}
}

// opCleanupSteps runs the probe's steps on a Go tree and prints what the probe prints.
// It reports a D-0121 refusal, whose history libyang does not share.
func opCleanupSteps(t *testing.T, s *yang.Schema, mode, in string, steps []string) (string, bool) {
	t.Helper()
	tr := NewTree(s)
	rpc, err := tr.NewInner(nil, "stage3-frc:r", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	f, typ, out := FormatJSON, OpReply, NewOptions{Output: true}
	switch mode {
	case "xml":
		f = FormatXML
	case "restconf":
		typ, out = OpRPCRestconf, NewOptions{}
	}
	var made []*Node
	var got strings.Builder
	for _, st := range steps {
		switch p := strings.SplitN(st, ":", 3); p[0] {
		case "l":
			n, err := tr.NewList(rpc, "l", p[1:], out)
			if err != nil {
				t.Fatal(err)
			}
			made = append(made, n)
		case "a":
			i, _ := strconv.Atoi(p[1])
			if _, err := tr.NewTerm(made[i], "a", p[2], out); err != nil {
				t.Fatal(err)
			}
		case "d":
			for l := range rpc.Children() {
				var vs []string
				for c := range l.Children() {
					vs = append(vs, c.Value())
				}
				fmt.Fprintf(&got, "(%s)", strings.Join(vs, ","))
			}
			got.WriteByte('\n')
		case "p":
			data := in
			if len(p) > 1 {
				data = strings.TrimPrefix(st, "p:")
			}
			rc := 0
			if _, ds, err := ParseOp(context.Background(), strings.NewReader(data), f, s, typ,
				ParseOpOptions{Unknown: Reject, Request: rpc}); err != nil {
				if strings.Contains(fmt.Sprint(err, ds), "D-0121") {
					return "", true
				}
				rc = 7 // LY_EVALID, the only failure these inputs reach
			}
			fmt.Fprintf(&got, "parse=%d\n", rc)
		}
	}
	checkTreeRetainedRB(t, tr)
	return got.String(), false
}
