// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Compare actual stored hashes with pinned libyang, including both sides of the opaque
// separator. Public Ordered producers remain outside PR 4; explicit placement exercises
// the identical physical layout without exposing another API or changing those producers.
func TestSiblingPlacementKeyPrefixLiveOracle(t *testing.T) {
	if os.Getenv("YANG_ORACLE_REQUIRED") == "" {
		t.Skip("run with YANG_ORACLE_REQUIRED=1 in the dev container")
	}
	prefix := os.Getenv("LIBYANG_PREFIX")
	if prefix == "" {
		prefix = "/opt/libyang"
	}
	probe := filepath.Join(t.TempDir(), "key-prefix-probe")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "cc", "-O2", "-Wall", "-Wextra", "-Werror", "-std=c11",
		"-I"+filepath.Join(prefix, "include"), "../conformance/oracle/issue247_key_prefix.c",
		"-L"+filepath.Join(prefix, "lib"), "-Wl,-rpath,"+filepath.Join(prefix, "lib"), "-lyang", "-o", probe)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile native probe: %v: %s", err, out)
	}
	for _, slot := range []int{2, 3} {
		for _, change := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/change=%t", slot, change), func(t *testing.T) {
				tr, list := placementKeyList(t)
				opaque := newOpaque(opaque{Name: "zz", Value: "opaque"})
				if slot == 2 {
					placeKeyChild(t, tr, list, opaque, nil)
				}
				repeated := modelFreshTerm(t, list, "b", "R")
				placeKeyChild(t, tr, list, repeated, nil)
				members := []string{`"a":"A"`, `"b":"B"`, `"b":"R"`}
				if slot == 3 {
					placeKeyChild(t, tr, list, opaque, nil)
				}
				members = slices.Insert(members, slot, `"zz":"opaque"`)
				input := `{"pv2-sort-keys:c":{"l":[{` + strings.Join(members, ",") + `}]}}`
				args := []string{"../conformance/corpus/protocol-v2/schemas/pv2-sort-keys.yang", input}
				if change {
					changed, err := repeated.SetValue("S")
					if err != nil || !changed {
						t.Fatalf("change repeated key: %t %v", changed, err)
					}
					args = append(args, "change")
				}
				out, err := exec.CommandContext(ctx, probe, args...).CombinedOutput()
				if err != nil {
					t.Fatalf("native: %v: %s", err, out)
				}
				var hash uint32
				var keyN, count int
				if _, err := fmt.Sscan(string(out), &hash, &keyN, &count); err != nil {
					t.Fatal(err, string(out))
				}
				if hash != list.hash || keyN != list.keyN || count != list.kids.Len() {
					t.Fatalf("native hash=%#x prefix=%d children=%d; Go hash=%#x prefix=%d children=%d", hash, keyN, count, list.hash, list.keyN, list.kids.Len())
				}
				t.Logf("native and Go: hash=%#x prefix=%d children=%d", hash, keyN, count)
			})
		}
	}
}
