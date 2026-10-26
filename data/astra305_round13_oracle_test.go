// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These literal RPC diff and merge sources cannot be parsed by the sequence
// protocol's diff_parse/edit steps. Exercise the pinned library directly, with
// exactly the same RPC parser and edit entry points as the Go regression.
func TestAstra305Round13NativeRPC(t *testing.T) {
	if os.Getenv("YANG_ORACLE_REQUIRED") == "" {
		t.Skip("run with YANG_ORACLE_REQUIRED=1 in the dev container")
	}
	prefix := os.Getenv("LIBYANG_PREFIX")
	if prefix == "" {
		prefix = "/opt/libyang"
	}
	probe := filepath.Join(t.TempDir(), "rpc-probe")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cc", "-O2", "-Wall", "-Wextra", "-Werror", "-std=c11",
		"-I"+filepath.Join(prefix, "include"), "../conformance/oracle/astra305_round13.c",
		"-L"+filepath.Join(prefix, "lib"), "-Wl,-rpath,"+filepath.Join(prefix, "lib"), "-lyang", "-o", probe)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile native probe: %v: %s", err, out)
	}
	set := round13Set(t)
	for _, tc := range []struct{ id, mode, target, source, reply string }{
		{"apply-descendant-create", "apply", round13ApplyTarget, round13ApplySource, ""},
		{"move-copied-rpc-siblings", "move", `{}`, `{"l":[{"b":"a"},{"b":"b"}]}`, ""},
		{"merge-before-output-no-table", "merge", round13MergeTarget, round13MergeSource, round13Reply},
		{"merge-before-output-table-growth", "merge", round13MergeTarget, `{"l":[{"b":"b"},{"b":"b"},{"b":"b"},{"b":"b"},{"b":"b"},{"b":"b"}]}`, round13Reply},
		{"merge-descendant-forward", "merge",
			`{"outer":[{"inner":[{"id":1},{"id":1}]},{"inner":[{"id":1},{"id":1},{"id":2}]}]}`,
			`{"outer":[{"inner":[{"id":1},{"id":2}]},{"inner":[{"id":1},{"id":1},{"id":2}]}]}`, ""},
		{"merge-descendant-reverse", "merge",
			`{"outer":[{"inner":[{"id":1},{"id":2}]},{"inner":[{"id":1},{"id":1},{"id":2}]}]}`,
			`{"outer":[{"inner":[{"id":1},{"id":1}]},{"inner":[{"id":1},{"id":1},{"id":2}]}]}`, ""},
	} {
		t.Run(tc.id, func(t *testing.T) {
			target, source, mode := tc.target, tc.source, tc.mode
			wrap := func(body string) string { return `{"pv2-astra305-r13:r":` + body + `}` }
			args := []string{"../conformance/corpus/protocol-v2/astra305-round13/pv2-astra305-r13.yang", mode, wrap(target), wrap(source)}
			dst, op := round13RPC(t, set, target)
			if tc.reply != "" {
				args = append(args, tc.reply)
				if _, _, ds, err := parseOp(context.Background(), strings.NewReader(tc.reply), set, FormatJSON, opReply, op, Reject); err != nil {
					t.Fatal(err, ds)
				}
			}
			src, srcOp := round13RPC(t, set, source)
			var err error
			switch mode {
			case "apply":
				err = dst.ApplyDiff(src, ApplyDiffOptions{})
			case "move":
				forest := newTree(set)
				first, _, dupErr := forest.DupSiblings(srcOp.kids.At(0), nil, DupOptions{Recursive: true})
				if dupErr != nil {
					t.Fatal(dupErr)
				}
				err = dst.Insert(op, first)
			default:
				err = dst.Merge(src)
			}
			if err != nil {
				t.Fatal(err)
			}
			native, err := exec.CommandContext(ctx, probe, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("native %s: %v: %s", mode, err, native)
			}
			var printed bytes.Buffer
			if err := dst.PrintJSON(&printed, PrintOptions{Shrink: true}); err != nil {
				t.Fatal(err)
			}
			// Compact preserves repeated object keys and their order in split RPC
			// child runs; unmarshaling into a map would silently drop instances.
			var got, want bytes.Buffer
			if err := json.Compact(&want, native); err != nil {
				t.Fatal(err, string(native))
			}
			if err := json.Compact(&got, printed.Bytes()); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatalf("native=%s\nGo=%s", native, printed.String())
			}
			t.Logf("native and Go: %s", native)
		})
	}
}
