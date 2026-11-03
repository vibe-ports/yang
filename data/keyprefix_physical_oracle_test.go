// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func keyPrefixProbe(t *testing.T) (context.Context, string) {
	t.Helper()
	if os.Getenv("YANG_ORACLE_REQUIRED") == "" {
		t.Skip("live oracle requires dev container")
	}
	prefix := os.Getenv("LIBYANG_PREFIX")
	if prefix == "" {
		prefix = "/opt/libyang"
	}
	probe := filepath.Join(t.TempDir(), "physical-prefix")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "cc", "-O2", "-Wall", "-Wextra", "-Werror", "-std=c11",
		"-I"+filepath.Join(prefix, "include"), "../conformance/oracle/issue247_key_prefix.c",
		"-L"+filepath.Join(prefix, "lib"), "-Wl,-rpath,"+filepath.Join(prefix, "lib"), "-lyang", "-o", probe)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v: %s", err, out)
	}
	return ctx, probe
}

// Replay the reference list-key-past-opaque case, plus separators exposed by
// unlink, unconditional value rehash, and table-threshold/repeated-key seeds.
// Paths exercise the key-reading consumer independently of stored hash equality.
func TestPhysicalKeyPrefixLiveOracle(t *testing.T) {
	ctx, probe := keyPrefixProbe(t)
	for _, count := range []int{1, 3, 4, 17} {
		for _, mode := range []string{"parse", "change", "unlink", "unlink-change"} {
			t.Run(fmt.Sprintf("%d/%s", count, mode), func(t *testing.T) {
				members := [][2]string{{"k", "x"}, {"zz", "u"}}
				for range count {
					members = append(members, [2]string{"k", strings.Repeat("y", 4096)})
				}
				tr, list := physicalKeyList(t, "pv2-sorted", members...)
				var json strings.Builder
				json.WriteString(`{"pv2-sorted:c":{"l":[{`)
				for i, m := range members {
					if i != 0 {
						json.WriteByte(',')
					}
					fmt.Fprintf(&json, "%q:%q", m[0], m[1])
				}
				json.WriteString(`}]}}`)
				args := []string{"../conformance/corpus/protocol-v2/schemas/pv2-sorted.yang", json.String()}
				if mode != "parse" {
					args = append(args, mode)
				}
				if strings.HasPrefix(mode, "unlink") {
					mustLink(t, list.kids.docSpan().At(1).Unlink())
				}
				if mode == "change" || mode == "unlink-change" {
					changed, err := list.kids.docLast().SetValue("S")
					if err != nil || !changed {
						t.Fatal(changed, err)
					}
				}
				out, err := exec.CommandContext(ctx, probe, args...).CombinedOutput()
				if err != nil {
					t.Fatalf("native: %v: %s", err, out)
				}
				var hash uint32
				var run, children int
				var path string
				if _, err := fmt.Sscan(string(out), &hash, &run, &children, &path); err != nil {
					t.Fatal(err, string(out))
				}
				if hash != list.hash || run != list.keyRun() || children != list.kids.Len() || path != list.Path() {
					t.Fatalf("native %sGo %d %d %d %s", out, list.hash, list.keyRun(), list.kids.Len(), list.Path())
				}
				opidxAssertTreeInvariant(t, tr)
			})
		}
	}
}

func TestPhysicalKeySortLiveOracle(t *testing.T) {
	ctx, probe := keyPrefixProbe(t)
	tr, list := placementKeyList(t)
	// Insertion sorts B before Z if it incorrectly reads through the separator.
	changed, err := list.kids.docLast().SetValue("Z")
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	placeKeyChild(t, tr, list, newOpaque(opaque{Name: "zz", Value: "u"}), list.kids.docLast())
	other := newInner(list.schema, &tr.work)
	placeKeyChild(t, tr, list.parent, other, nil)
	placeKeyChild(t, tr, other, modelFreshTerm(t, other, "a", "A"), nil)
	placeKeyChild(t, tr, other, modelFreshTerm(t, other, "b", "B"), nil)
	if err := tr.Insert(list.parent, other); err != nil {
		t.Fatal(err)
	}
	if list.parent.kids.docFirst() != list {
		t.Fatal("sort read through opaque")
	}
	input := `{"pv2-sort-keys:c":{"l":[{"a":"A","b":"Z","zz":"u"},{"a":"A","b":"B"}]}}`
	out, err := exec.CommandContext(ctx, probe, "../conformance/corpus/protocol-v2/schemas/pv2-sort-keys.yang", input, "sort").CombinedOutput()
	if err != nil {
		t.Fatalf("native sort: %v: %s", err, out)
	}
	if !strings.HasPrefix(string(out), "sort 1\n") {
		t.Fatalf("native sort: %s", out)
	}
	var hash uint32
	var run, children int
	var path string
	if _, err := fmt.Sscan(strings.TrimPrefix(string(out), "sort 1\n"), &hash, &run, &children, &path); err != nil {
		t.Fatal(err, string(out))
	}
	if hash != list.hash || run != list.keyRun() || children != list.kids.Len() || path != list.Path() {
		t.Fatalf("native displaced prefix: %s; Go %d %d %d %s", out, list.hash, list.keyRun(), list.kids.Len(), list.Path())
	}

}

func TestPhysicalKeyIndexedExposureLiveOracle(t *testing.T) {
	ctx, probe := keyPrefixProbe(t)
	for _, separator := range []string{"zz", "v"} {
		for _, batch := range []bool{false, true} {
			for _, change := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/batch=%t/change=%t", separator, batch, change), func(t *testing.T) {
					tr, list, sep := indexedExposureList(t, separator, 4096)
					if batch {
						mustLink(t, tr.unlinkAll([]*Node{sep}))
					} else {
						mustLink(t, sep.Unlink())
					}
					mode := "unlink"
					if change {
						changed, err := list.kids.docLast().SetValue("S")
						if err != nil || !changed {
							t.Fatal(changed, err)
						}
						mode = "unlink-change"
					}
					input := `{"pv2-sorted:c":{"l":[{"k":"` + strings.Repeat("x", 4096) + `","` + separator + `":"u"` + strings.Repeat(`,"k":"y"`, 17) + `}],"ll":["f","g"],"z":"z"}}`
					out, err := exec.CommandContext(ctx, probe, "../conformance/corpus/protocol-v2/schemas/pv2-sorted.yang", input, mode).CombinedOutput()
					if err != nil {
						t.Fatalf("native: %v: %s", err, out)
					}
					var hash uint32
					var run, children int
					var path string
					if _, err := fmt.Sscan(string(out), &hash, &run, &children, &path); err != nil {
						t.Fatal(err, string(out))
					}
					if hash != list.hash || run != list.keyRun() || children != list.kids.Len() || path != list.Path() {
						t.Fatalf("native %s; Go %d %d %d %s", out, list.hash, list.keyRun(), list.kids.Len(), list.Path())
					}
					opidxAssertTreeInvariant(t, tr)
				})
			}
		}
	}
}
