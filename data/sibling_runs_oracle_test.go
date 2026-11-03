// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Each replay compares every intermediate document order with pinned libyang. The ordered
// native parser supplies the layout that Go can currently construct only inside this package.
func runNativeProbe(t *testing.T, modelText string) (context.Context, string, string) {
	t.Helper()
	if os.Getenv("YANG_ORACLE_REQUIRED") == "" {
		t.Skip("requires live oracle in dev container")
	}
	prefix := os.Getenv("LIBYANG_PREFIX")
	if prefix == "" {
		prefix = "/opt/libyang"
	}
	base := t.TempDir()
	model := filepath.Join(base, "run-test.yang")
	if err := os.WriteFile(model, []byte(modelText), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(base, "runs-probe")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "cc", "-O2", "-Wall", "-Wextra", "-Werror", "-std=c11", "-I"+filepath.Join(prefix, "include"), "../conformance/oracle/sibling_runs.c", "-L"+filepath.Join(prefix, "lib"), "-Wl,-rpath,"+filepath.Join(prefix, "lib"), "-lyang", "-o", probe)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile: %v: %s", err, out)
	}
	return ctx, probe, model
}

func TestRunNativeReplay(t *testing.T) {
	ctx, probe, model := runNativeProbe(t, runSchema)
	set := reviewDiffSet(t, runSchema)
	for _, tc := range []struct {
		name         string
		input, steps []string
	}{
		{"ordinary-linked-leaf", []string{"a=x"}, []string{"move=0", "move=0", "move=0"}},
		{"ordinary-linked-leaf-journaled", []string{"a=x"}, []string{"move=0", "move=0", "move=0"}},
		{"joined-null-successor-queues", []string{"ll=1", "ll=2", "zz", "ll=3", "ll=4"}, []string{"add=0", "add=5", "del=3", "dup", "del=0", "del=0", "del=0"}},
		{"selected-retained-64", runSelectedInput(64), []string{"add=65", "add=66"}},
		{"selected-retained-4096", runSelectedInput(4096), []string{"add=4097", "add=4098"}},
		{"first-interleaving-64", runSelectedInput(64)[:64], []string{"opaq=1"}},
		{"first-interleaving-4096", runSelectedInput(4096)[:4096], []string{"opaq=1"}},
		{"ordinary-opaque-nonleader", []string{"ll=1", "zz"}, []string{"add=2", "del=1"}},
		{"opaque-neighbours", []string{"ll=1", "ll=2", "zz", "zz", "b=x", "yy"}, []string{"add=3", "del=5", "add=4"}},
		{"cold-singleton-piece-64", runAuditInput(64), []string{"add=60"}},
		{"cold-singleton-piece-4096", runAuditInput(4096), []string{"add=60"}},
		{"opaque-populated-successor", []string{"ll=1"}, []string{"add=2", "add=3", "add=4", "opaq=1", "del=0"}},
		{"opaque-null-successor", []string{"ll=1"}, []string{"add=2", "add=3", "add=4", "dup", "opaq=1", "del=0"}},
		{"successive-opaque-joins", []string{"ll=4", "ll=2", "zz", "ll=9", "ll=10", "yy", "ll=20", "ll=21"}, []string{"add=3", "add=11", "del=3", "del=6", "add=22"}},
		{"successive-schema-joins", []string{"ll=4", "ll=2", "b=y", "ll=9", "ll=10", "yy", "ll=20", "ll=21"}, []string{"add=3", "add=11", "del=3", "del=6", "add=22"}},
		{"split-no-table", []string{"ll=4", "zz", "ll=9"}, []string{"add=3", "add=2", "del=1", "add=5"}},
		{"split-table", []string{"a=x", "b=y", "ll=4", "ll=2", "zz", "ll=3", "z=z"}, []string{"add=1", "add=0", "del=2", "add=5"}},
		{"singleton", []string{"zz", "ll=1", "yy", "ll=9", "ll=10", "ll=11"}, []string{"add=2", "del=2", "del=1", "add=0"}},
		{"unlink-between", []string{"ll=4", "ll=2", "zz", "ll=9", "ll=10"}, []string{"add=3", "del=3", "add=5"}},
		{"schema-separator", []string{"ll=4", "ll=2", "b=y", "ll=9", "ll=10"}, []string{"add=3", "del=3", "add=5"}},
		{"cyclic-singleton", []string{"ll=1", "zz", "ll=9", "ll=10", "ll=11"}, []string{"add=2", "del=1", "del=0", "add=0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var xml strings.Builder
			xml.WriteString(`<c xmlns="urn:run-test">`)
			for _, item := range tc.input {
				name, value, ok := strings.Cut(item, "=")
				if !ok {
					value = "u"
				}
				xml.WriteString("<" + name + ">" + value + "</" + name + ">")
			}
			xml.WriteString("</c>")
			args := append([]string{model, xml.String()}, tc.steps...)
			var diagnostics bytes.Buffer
			command := exec.CommandContext(ctx, probe, args...)
			command.Stderr = &diagnostics
			native, err := command.Output()
			if err != nil {
				t.Fatalf("native: %v: stdout=%s stderr=%s", err, native, diagnostics.String())
			}
			if diagnostics.Len() != 0 {
				t.Logf("native diagnostics: %s", diagnostics.String())
			}
			tr, c := runTree(t, set, tc.input...)
			var journal *hashTransaction
			if tc.name == "ordinary-linked-leaf-journaled" {
				journal = beginHashTransaction(tr)
			}
			var got strings.Builder
			printState := func() { got.WriteString(runNativeState(tr.childrenOf(c))) }
			printState()
			for _, step := range tc.steps {
				op, value, _ := strings.Cut(step, "=")
				if op == "dup" {
					copyTree := newTree(set)
					c, _, err = copyTree.Dup(c, nil, DupOptions{Recursive: true, WithFlags: true})
					tr = copyTree
				} else if op == "add" {
					_, err = tr.NewTerm(c, "ll", value, NewOptions{})
				} else {
					at, parseErr := strconv.Atoi(value)
					if parseErr != nil {
						t.Fatal(parseErr)
					}
					if op == "opaq" {
						n := runNode(t, set, c, "zz")
						p := c.kids.physicalPlacement(n, c.kids.docSpan().At(at))
						var bound uint64
						bound, err = tr.linkPlacedBound(c, &c.kids, n, p)
						if err == nil {
							err = tr.reserveHash(bound)
						}
						if err == nil {
							err = tr.linkPlaced(c, &c.kids, n, p)
						}
					} else if op == "move" {
						err = tr.Insert(c, c.kids.docSpan().At(at))
					} else {
						err = c.kids.docSpan().At(at).Unlink()
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				opidxAssertTreeInvariant(t, tr)
				checkTreeRetainedRB(t, tr)
				printState()
			}
			journal.end(&err)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != string(native) {
				t.Fatalf("native:\n%sGo:\n%s", native, got.String())
			}
			t.Logf("%d intermediate states match pinned native", len(tc.steps)+1)
		})
	}
}

// Include metadata ownership in every comparison; document values alone cannot detect an
// orphaned retained graph or a NULL-root slot moved to the wrong physical successor.
func runNativeState(s *siblings) string {
	var out strings.Builder
	for _, n := range s.docSpan().All() {
		out.WriteString(n.Name())
		if n.schema != nil {
			out.WriteString("=" + n.Value())
		}
		for _, g := range s.metaGroups(n) {
			state := "null"
			if d := s.rbOrders[g]; d != nil && d.root != nil {
				state = "live"
			}
			out.WriteString(" [rb=" + state + "]")
		}
		out.WriteByte('\n')
	}
	out.WriteString("--\n")
	return out.String()
}

func TestRunNativeStoragePreflight(t *testing.T) {
	const topSchema = `module run-test {yang-version 1.1; namespace "urn:run-test"; prefix r; leaf-list ll {type uint32;} }`
	ctx, probe, model := runNativeProbe(t, topSchema)
	a, b := reviewDiffSet(t, topSchema), reviewDiffSet(t, topSchema)
	for _, interleaved := range []bool{false, true} {
		for _, foreign := range []bool{false, true} {
			for _, count := range []int{64, 256} {
				t.Run(fmt.Sprintf("interleaved=%t/foreign=%t/n=%d", interleaved, foreign, count), func(t *testing.T) {
					tr := newTree(a)
					if _, err := tr.NewTerm(nil, "run-test:ll", "1", NewOptions{}); err != nil {
						t.Fatal(err)
					}
					var xml strings.Builder
					xml.WriteString(`<ll xmlns="urn:run-test">1</ll>`)
					if interleaved {
						mustLink(t, tr.link(nil, &tr.top, newOpaque(opaque{Name: "zz", ModuleNS: "urn:run-test", Value: "u"}), -1))
						xml.WriteString(`<zz xmlns="urn:run-test">u</zz>`)
					}
					for i := range count {
						value := strconv.Itoa(100 + i)
						xml.WriteString(`<ll xmlns="urn:run-test">` + value + `</ll>`)
						n := retainedRBTerm(t, tr.top.At(0).schema, value)
						mustLink(t, tr.linkPlaced(nil, &tr.top, n, tr.top.physicalPlacement(n, nil)))
					}
					got := runNativeState(&tr.top)
					source := newTree(a)
					op := "insert=60"
					if foreign {
						source = newTree(b)
						op = "foreign=60"
					}
					n, err := source.NewTerm(nil, "run-test:ll", "60", NewOptions{})
					if err != nil {
						t.Fatal(err)
					}
					mustLink(t, n.Unlink())
					mustLink(t, tr.Insert(nil, n))
					got += runNativeState(&tr.top)
					command := exec.CommandContext(ctx, probe, model, xml.String(), op)
					var diagnostics bytes.Buffer
					command.Stderr = &diagnostics
					native, err := command.Output()
					if err != nil {
						t.Fatalf("native: %v: %s", err, diagnostics.String())
					}
					if got != string(native) {
						t.Fatalf("native:\n%sGo:\n%s", native, got)
					}
					checkTreeRetainedRB(t, tr)
					opidxAssertTreeInvariant(t, tr)
					t.Log("detached insertion and selected metadata match pinned native")
				})
			}
		}
	}
}
