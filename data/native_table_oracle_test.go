// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type nativeDriverResult struct {
	Event string       `json:"event"`
	RC    nativeStatus `json:"rc"`
	Table struct {
		Present bool `json:"present"`
		nativeImage
		Slots []struct{ Next uint32 } `json:"slots"`
	} `json:"table"`
	Nodes []struct {
		ID    int `json:"id"`
		Table struct {
			Present bool `json:"present"`
			nativeImage
			Slots []struct{ Next uint32 } `json:"slots"`
		} `json:"table"`
	} `json:"nodes"`
}

func nativeDriver(t *testing.T, script string) []nativeDriverResult {
	t.Helper()
	path := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if path == "" {
		t.Skip("isolated static observer: set YANG_NATIVE_REGISTRATION_DRIVER after its test-only build")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	//nolint:gosec // Explicit opt-in path to the isolated test-only static native driver.
	cmd := exec.CommandContext(ctx, path, "--quiet")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native registration driver: %v\n%s\nscript:\n%s", err, out, script)
	}
	var results []nativeDriverResult
	scan := bufio.NewScanner(bytes.NewReader(out))
	scan.Buffer(make([]byte, 4096), 8<<20)
	for scan.Scan() {
		var r nativeDriverResult
		if err := json.Unmarshal(scan.Bytes(), &r); err != nil {
			t.Fatalf("native trace: %v: %s", err, scan.Bytes())
		}
		if r.Event == "driver.ready.actual-children-callback" {
			continue
		}
		for i := range r.Table.Chains {
			s := &r.Table.Chains[i]
			s.Next = r.Table.Slots[s.Slot].Next
		}
		for j := range r.Nodes {
			for i := range r.Nodes[j].Table.Chains {
				s := &r.Nodes[j].Table.Chains[i]
				s.Next = r.Nodes[j].Table.Slots[s.Slot].Next
			}
		}
		results = append(results, r)
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	return results
}

func TestNativeObserverEvidence(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	comparison := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER")
	if comparison == "" {
		comparison = filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// This independently checks occurrence identities, retirement, logging versus
	// context diagnostics, public controls and the labeled native stop boundaries.
	cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "check.py"),
		"--driver", driver, "--comparison-driver", comparison)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native observer evidence: %v\n%s", err, out)
	}
}

// Confirm the existing Go API has no earlier D-0121 refusal on the exact public
// prefix. G1 does not route the final insertion through its private kernel.
func TestNativePublicRemotePrefix(t *testing.T) {
	set := reviewDiffSet(t, `module m {yang-version 1.1;namespace urn:m;prefix m;
 rpc r {input {leaf a {type string;}leaf a2 {type string;}leaf-list v {type uint32;}}
 output {leaf o {type string;}}}}`)
	p, q, r := newTree(set), newTree(set), newTree(set)
	root := func(tr *Tree) *Node {
		n, err := tr.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	pp, qq, rr := root(p), root(q), root(r)
	term := func(tr *Tree, parent *Node, name, value string, options NewOptions) *Node {
		n, err := tr.NewTerm(parent, name, value, options)
		if err != nil {
			t.Fatal(name, err)
		}
		return n
	}
	term(p, pp, "a", "a", NewOptions{})
	term(p, pp, "a2", "a2", NewOptions{})
	v1 := term(p, pp, "v", "1", NewOptions{})
	term(p, pp, "v", "2", NewOptions{})
	o := term(p, pp, "o", "o", NewOptions{Output: true})
	seed := term(r, rr, "v", "3", NewOptions{})
	v3, _, err := p.Dup(seed, pp, DupOptions{NoSort: true, NoMeta: true})
	if err != nil {
		t.Fatal("dup", err)
	}
	if err := o.Unlink(); err != nil {
		t.Fatal("unlink o", err)
	}
	if err := q.Insert(qq, v3); err != nil {
		t.Fatal("move v3", err)
	}
	if err := v1.Unlink(); err != nil {
		t.Fatal("unlink v1", err)
	}
	if v3 == nil || v3 == seed || v3.parent != qq || seed.parent != rr || o.parent != nil || v1.parent != nil {
		t.Fatal("public remote prefix did not retain the copy, source, move and unlink identities")
	}
}

func TestNativePrimitiveLiveOracle(t *testing.T) {
	// Fixed seeds are replayable public driver input, not generated goldens.
	for _, seed := range []uint64{247, 0x7be55cae, 0x257} {
		for _, mode := range []uint8{0, 1} {
			t.Run(fmt.Sprintf("%x/mode%d", seed, mode), func(t *testing.T) {
				h, table := nativeFresh(t, 8, mode)
				nodes := make([]*Node, 32)
				for i := range nodes {
					nodes[i] = new(Node)
				}
				var script strings.Builder
				fmt.Fprintf(&script, "new 8 %d\n", mode)
				expected := []nativeImage{nativeObserve(t, table, nodes, nativeOK)}
				rng := rand.New(rand.NewPCG(seed, seed+1))
				for range 192 {
					id := rng.IntN(len(nodes))
					hash := []uint32{0, 7, 15, 0x7be55cae, 0xffffffff}[rng.IntN(5)]
					check := rng.IntN(2) == 0
					remove := rng.IntN(3) == 0 || (mode == 0 && table.used == table.size)
					rc, err := h.primitive(table, nodes[id], hash, check, remove)
					if err != nil {
						t.Fatal(err)
					}
					if remove {
						fmt.Fprintf(&script, "remove %d %d\n", id+1, hash)
					} else {
						fmt.Fprintf(&script, "insert %d %d %d\n", id+1, hash, boolInt(check))
					}
					expected = append(expected, nativeObserve(t, table, nodes, rc))
				}
				results := nativeDriver(t, script.String())
				if len(results) != len(expected) {
					t.Fatalf("%d snapshots, want %d", len(results), len(expected))
				}
				for i, r := range results {
					got := r.Table.nativeImage
					got.RC = r.RC
					if !reflect.DeepEqual(got, expected[i]) {
						t.Fatalf("step %d\nnative=%+v\nGo=%+v\nreplay:\n%s", i, got, expected[i], script.String())
					}
				}
				t.Logf("seed=%x mode=%d: %d exact native snapshots", seed, mode, len(results))
			})
		}
	}
}

func TestNativePrimitiveDiscriminatingLiveOracle(t *testing.T) {
	for _, mode := range []uint8{0, 1} {
		t.Run(fmt.Sprintf("oversized-mode%d", mode), func(t *testing.T) {
			h, table := nativeFresh(t, 16, mode)
			a := new(Node)
			nodes := []*Node{a}
			expected := []nativeImage{nativeObserve(t, table, nodes, nativeOK)}
			rc := nativePut(t, h, table, a, 7, false)
			expected = append(expected, nativeObserve(t, table, nodes, rc))
			rc = nativeRemove(t, h, table, a, 7)
			expected = append(expected, nativeObserve(t, table, nodes, rc))
			script := fmt.Sprintf("new 16 %d\ninsert 1 7 0\nremove 1 7\n", mode)
			nativeComparePrimitive(t, script, expected)
		})
	}
	t.Run("checked-EEXIST-at-full-capacity", func(t *testing.T) {
		h, table := nativeFresh(t, 8, 0)
		a := new(Node)
		nodes := []*Node{a}
		script := "new 8 0\n"
		expected := []nativeImage{nativeObserve(t, table, nodes, nativeOK)}
		for range 8 {
			script += "insert 1 7 0\n"
			rc := nativePut(t, h, table, a, 7, false)
			expected = append(expected, nativeObserve(t, table, nodes, rc))
		}
		script += "insert 1 7 1\nremove 1 7\n"
		rc := nativePut(t, h, table, a, 7, true)
		nativeRequire(t, rc, nil, nativeExists)
		expected = append(expected, nativeObserve(t, table, nodes, rc))
		rc = nativeRemove(t, h, table, a, 7)
		expected = append(expected, nativeObserve(t, table, nodes, rc))
		nativeComparePrimitive(t, script, expected)
	})
}

func nativeComparePrimitive(t *testing.T, script string, expected []nativeImage) {
	t.Helper()
	results := nativeDriver(t, script)
	if len(results) != len(expected) {
		t.Fatalf("%d snapshots, want %d; replay:\n%s", len(results), len(expected), script)
	}
	for i, r := range results {
		got := r.Table.nativeImage
		got.RC = r.RC
		if !reflect.DeepEqual(got, expected[i]) {
			t.Fatalf("step %d native=%+v Go=%+v replay:\n%s", i, got, expected[i], script)
		}
	}
}

func TestNativeOpaqueLiveOracle(t *testing.T) {
	h, table, p, sn := nativeModel(t)
	a, b := nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q")
	x, y := nativeModelNode(t, h, sn[2], "QQ=="), nativeModelNode(t, h, sn[2], "Qg==")
	opaq := nativeModelNode(t, h, nil, "")
	nodes := []*Node{p.node, a.node, b.node, x.node, opaq.node, y.node}
	nativeModelLink(p, a, b, x, opaq)
	rc, err := h.event(table, x, false)
	nativeRequire(t, rc, err, nativeOK)
	if table.present {
		t.Fatal("opaque counted toward threshold")
	}
	nativeModelLink(p, a, b, x, opaq, y)
	rc, err = h.event(table, y, false)
	nativeRequire(t, rc, err, nativeOK)
	expected := nativeObserve(t, table, nodes, nativeOK)
	// Native term insertion places schema children before trailing opaque
	// children. Move the separator publicly before y while only three schema
	// children exist, then add q to cross the threshold in the separated order.
	script := "root 1 c\nterm 2 1 p p\nterm 4 1 ll QQ==\nopaque 5 1 separator value\nterm 6 1 ll Qg== 64\nbefore 6 5\nI 5\nU 5\nterm 3 1 q q\nI 5\nU 5\n"
	results := nativeDriver(t, script)
	if len(results) != 11 {
		t.Fatalf("opaque request snapshots=%d want=11", len(results))
	}
	for i, result := range results {
		foundParent := false
		for _, n := range result.Nodes {
			if n.ID != 1 {
				continue
			}
			foundParent = true
			if i < 8 {
				if n.Table.Present {
					t.Fatalf("event %d: three schema children plus opaque created native table", i)
				}
				continue
			}
			got := n.Table.nativeImage
			got.RC = result.RC
			if !n.Table.Present || !reflect.DeepEqual(got, expected) {
				t.Fatalf("event %d native=%+v Go=%+v", i, got, expected)
			}
		}
		if !foundParent || result.RC != nativeOK {
			t.Fatalf("event %d parent=%t enclosing rc=%d", i, foundParent, result.RC)
		}
	}
	// Opaque parents cannot own native children tables, even after four opaque
	// child constructions; explicit I/U exercise the parent's guard as well.
	script = "opaque 1 0 parent value\nopaque 2 1 a a\nopaque 3 1 b b\nopaque 4 1 c c\nopaque 5 1 d d\nI 2\nU 2\n"
	for _, result := range nativeDriver(t, script) {
		if result.RC != nativeOK {
			t.Fatalf("opaque-parent request failed: %d", result.RC)
		}
		for _, n := range result.Nodes {
			if n.ID != 0 && n.Table.Present {
				t.Fatal("opaque parent/child became table eligible")
			}
		}
	}
}

func TestNativeBuildLiveOracle(t *testing.T) {
	for _, values := range [][]string{{"KpfzBg==", "QQ==", "Qg=="}, {"QQ==", "KpfzBg==", "Qg=="}} {
		t.Run(values[0], func(t *testing.T) {
			h, table, p, sn := nativeModel(t)
			var script strings.Builder
			script.WriteString("root 1 c\nterm 2 1 p p\nterm 3 1 q q\n")
			children := []*nativeChild{nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q")}
			nodes := []*Node{p.node, children[0].node, children[1].node}
			var expected []nativeImage
			for i, value := range values {
				c := nativeModelNode(t, h, sn[2], value)
				children = append(children, c)
				nodes = append(nodes, c.node)
				nativeModelLink(p, children...)
				rc, err := h.event(table, c, false)
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&script, "term %d 1 ll %s\n", i+4, value)
				if table.present {
					expected = append(expected, nativeObserve(t, table, nodes, rc))
				}
			}
			results := nativeDriver(t, script.String())
			var step int
			for _, r := range results {
				for _, n := range r.Nodes {
					if n.ID != 1 || !n.Table.Present {
						continue
					}
					got := n.Table.nativeImage
					// lyd_new_term's void insertion caller ignores helper EINT.
					// Compare its table prefix; separate native event tests cover rc.
					got.RC = expected[step].RC
					if !reflect.DeepEqual(got, expected[step]) || r.RC != nativeOK {
						t.Fatalf("step=%d native=%+v Go=%+v enclosing=%d replay:\n%s", step, got, expected[step], r.RC, script.String())
					}
					step++
				}
			}
			if step != len(expected) {
				t.Fatalf("compared %s tables, want %d", strconv.Itoa(step), len(expected))
			}
		})
	}
}

func TestNativeExistingEventsLiveOracle(t *testing.T) {
	h, table, p, sn := nativeModel(t)
	children := []*nativeChild{nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q"), nativeModelNode(t, h, sn[2], "QQ=="), nativeModelNode(t, h, sn[2], "Qg==")}
	nativeModelLink(p, children...)
	rc, err := h.event(table, children[3], false)
	nativeRequire(t, rc, err, nativeOK)
	nodes := []*Node{p.node}
	for _, c := range children {
		nodes = append(nodes, c.node)
	}
	var script strings.Builder
	script.WriteString("root 1 c\nterm 2 1 p p\nterm 3 1 q q\nterm 4 1 ll QQ==\nterm 5 1 ll Qg==\n")
	expected := []nativeImage{nativeObserve(t, table, nodes, nativeOK)}
	// These I/U commands are explicitly low-level controls, separate from the
	// driver's public reachability scripts. U does not physically detach here.
	for _, step := range []struct {
		id     int
		unlink bool
	}{{4, false}, {4, true}, {4, true}, {5, true}, {5, false}, {4, true}, {1, false}} {
		c := p
		if step.id != 1 {
			c = children[step.id-2]
		}
		rc, err := h.event(table, c, step.unlink)
		if err != nil {
			t.Fatal(err)
		}
		command := "I"
		if step.unlink {
			command = "U"
			rc = nativeOK /* native U is void; helper stop is in diagnostics */
		}
		fmt.Fprintf(&script, "%s %d\n", command, step.id)
		expected = append(expected, nativeObserve(t, table, nodes, rc))
	}
	results := nativeDriver(t, script.String())
	var index int
	for _, r := range results {
		for _, n := range r.Nodes {
			if n.ID != 1 || !n.Table.Present {
				continue
			}
			got := n.Table.nativeImage
			got.RC = r.RC
			if index >= len(expected) || !reflect.DeepEqual(got, expected[index]) {
				t.Fatalf("event %d native=%+v expected=%+v replay:\n%s", index, got, expected, script.String())
			}
			index++
		}
	}
	if index != len(expected) {
		t.Fatalf("compared %d events, want %d", index, len(expected))
	}
}

func TestNativeHistoryScriptsLiveOracle(t *testing.T) {
	for _, name := range []string{"collision-build", "collision-resize", "repeated-pointer", "collision-history-retained", "collision-history-shrunk"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "conformance", "oracle", "native_registration", "cases", name+".calls")
			text, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			h, table, parent, schemas := nativeModel(t)
			children := []*nativeChild{}
			byID := map[int]*nativeChild{1: parent}
			nodes := make([]*Node, 4096)
			nodes[0] = parent.node
			var expected []nativeImage
			var script strings.Builder
			for _, line := range strings.Split(string(text), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
					continue
				}
				fmt.Fprintln(&script, line)
				id, err := strconv.Atoi(fields[1])
				if err != nil {
					t.Fatal(err)
				}
				switch fields[0] {
				case "root", "schema", "validate", "print": // no registration writes in these controls
				case "term":
					var c *nativeChild
					for _, sn := range schemas {
						if sn.Name == fields[3] {
							c = nativeModelNode(t, h, sn, fields[4])
							break
						}
					}
					if c == nil {
						t.Fatalf("unknown term %s", line)
					}
					byID[id], nodes[id-1] = c, c.node
					// All sorted v values in these scripts arrive in increasing
					// order. This model supplies physical frontiers; it is not a
					// production sorting, change-value or validation adapter.
					at := len(children)
					for i, old := range children {
						if slices.Index(schemas, c.node.schema) < slices.Index(schemas, old.node.schema) {
							at = i
							break
						}
					}
					children = slices.Insert(children, at, c)
					nativeModelLink(parent, children...)
					if _, err := h.event(table, c, false); err != nil {
						t.Fatal(err)
					}
				case "unlink":
					c := byID[id]
					if _, err := h.event(table, c, true); err != nil {
						t.Fatal(err)
					}
					children = slices.Delete(children, slices.Index(children, c), slices.Index(children, c)+1)
					nativeModelLink(parent, children...)
					c.parent, c.next, c.prev = nil, nil, c
				case "I":
					if _, err := h.event(table, byID[id], false); err != nil {
						t.Fatal(err)
					}
				case "change":
					c := byID[id]
					if _, err := h.event(table, c, true); err != nil {
						t.Fatal(err)
					}
					replacement := nativeModelNode(t, h, c.node.schema, fields[2])
					if err := c.node.touch(); err != nil {
						t.Fatal(err)
					}
					c.node.value, c.node.hash = replacement.node.value, replacement.node.hash
					if _, err := h.event(table, c, false); err != nil {
						t.Fatal(err)
					}
				default:
					t.Fatalf("unsupported model instruction %s", line)
				}
				if table.present {
					expected = append(expected, nativeObserve(t, table, nodes, nativeOK))
				}
			}
			results := nativeDriver(t, script.String())
			var index int
			for _, r := range results {
				for _, n := range r.Nodes {
					if n.ID != 1 || !n.Table.Present {
						continue
					}
					got := n.Table.nativeImage
					got.RC = nativeOK // helper/enclosing statuses are asserted in check.py
					if index >= len(expected) || !reflect.DeepEqual(got, expected[index]) {
						t.Fatalf("%s step=%d native=%+v expected=%+v", name, index, got, expected)
					}
					index++
				}
			}
			if index != len(expected) {
				t.Fatalf("%s compared %d tables, want %d", name, index, len(expected))
			}
			t.Logf("%s: %d exact event snapshots", name, index)
		})
	}
}
