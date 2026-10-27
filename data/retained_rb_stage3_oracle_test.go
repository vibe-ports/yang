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
	"strings"
	"testing"
	"time"
)

func TestStage3NativeMetadataMoves(t *testing.T) {
	if os.Getenv("YANG_ORACLE_REQUIRED") == "" {
		t.Skip("run with YANG_ORACLE_REQUIRED=1 in the dev container")
	}
	const module = `module stage3-extra {yang-version 1.1;namespace "urn:stage3-extra";prefix s;
 list l {key k;leaf k {type uint32;}leaf p {type string;}action act {input {leaf x {type string;}}}}
 container c {list l {key k;leaf k {type uint32;}}}}`
	base := t.TempDir()
	model := filepath.Join(base, "stage3-extra.yang")
	if err := os.WriteFile(model, []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	prefix := os.Getenv("LIBYANG_PREFIX")
	if prefix == "" {
		prefix = "/opt/libyang"
	}
	probe := filepath.Join(base, "metadata-probe")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	//nolint:gosec // Fixed test-only source and flags, private temporary output.
	cmd := exec.CommandContext(ctx, "cc", "-O2", "-Wall", "-Wextra", "-Werror", "-std=c11",
		"-I"+filepath.Join(prefix, "include"), "../conformance/oracle/retained_rb_stage3.c",
		"-L"+filepath.Join(prefix, "lib"), "-Wl,-rpath,"+filepath.Join(prefix, "lib"), "-lyang", "-o", probe)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile native probe: %v: %s", err, out)
	}
	t.Run("empty-selection", func(t *testing.T) {
		//nolint:gosec // Executes the native probe built immediately above.
		native, err := exec.CommandContext(ctx, probe, model, "empty").CombinedOutput()
		if err != nil {
			t.Fatalf("native empty selection: %v: %s", err, native)
		}
		set := reviewDiffSet(t, module)
		prototype, dst, src := newTree(set), newTree(set), newTree(set)
		add := func(tr *Tree, key string) {
			t.Helper()
			if _, err := tr.NewList(nil, "stage3-extra:l", []string{key}, NewOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		add(prototype, "3")
		add(prototype, "11")
		first, _, err := dst.Dup(prototype.top.first(), nil, DupOptions{Recursive: true, NoSort: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"5", "7", "9"} {
			add(src, key)
		}
		if err := dst.Insert(nil, src.top.first()); err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		fmt.Fprintf(&got, "dup-slots=%d\n", len(dst.top.metaGroups(first)))
		for _, key := range []string{"", "4", "6"} {
			if key != "" {
				add(dst, key)
			}
			checkTreeRetainedRB(t, dst)
			if err := dst.PrintJSON(&got, PrintOptions{Shrink: true}); err != nil {
				t.Fatal(err)
			}
			got.WriteByte('\n')
		}
		if strings.TrimSpace(got.String()) != strings.TrimSpace(string(native)) {
			t.Fatalf("native=%s\nGo=%s", native, got.String())
		}
		t.Logf("native NULL selection and subsequent rebuild match:\n%s", native)
	})
	t.Run("empty-unlink", func(t *testing.T) {
		//nolint:gosec // Executes the native probe built immediately above.
		native, err := exec.CommandContext(ctx, probe, model, "empty-unlink").CombinedOutput()
		if err != nil {
			t.Fatalf("native empty unlink: %v: %s", err, native)
		}
		set := reviewDiffSet(t, module)
		tr := newTree(set)
		parent, err := tr.NewInner(nil, "stage3-extra:c", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		add := func(key string) *Node {
			t.Helper()
			n, err := tr.NewList(parent, "l", []string{key}, NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		a, b := add("3"), add("5")
		kept := parent.kids.rbGroups[a]
		var last []*Node
		for _, key := range []string{"9", "1"} {
			fresh := newTree(set)
			holder, err := fresh.NewInner(nil, "stage3-extra:c", NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			n, err := fresh.NewList(holder, "l", []string{key}, NewOptions{})
			if err != nil {
				t.Fatal(err)
			}
			dup, _, err := tr.Dup(n, parent, DupOptions{Recursive: true, NoSort: true})
			if err != nil {
				t.Fatal(err)
			}
			last = append(last, dup)
		}
		for _, n := range []*Node{a, b} {
			if err := n.Remove(); err != nil {
				t.Fatal(err)
			}
		}
		if err := last[0].Unlink(); err != nil {
			t.Fatal(err)
		}
		checkTreeRetainedRB(t, tr)
		if parent.kids.rbGroups[last[1]] != kept || len(last[0].carriedRB) != 0 || strings.TrimSpace(string(native)) != "empty-unlink=ok" {
			t.Fatal("NULL metadata did not move to the native successor")
		}
	})
	t.Run("singleton", func(t *testing.T) {
		//nolint:gosec // Executes the native probe built immediately above.
		native, err := exec.CommandContext(ctx, probe, model, "singleton").CombinedOutput()
		if err != nil {
			t.Fatalf("native singleton round trip: %v: %s", err, native)
		}
		got := stage3SingletonRoundTrip(t, reviewDiffSet(t, module))
		if strings.TrimSpace(got) != strings.TrimSpace(string(native)) {
			t.Fatalf("native=%s\nGo=%s", native, got)
		}
		t.Logf("native singleton metadata round trip matches:\n%s", native)
	})
	for _, mode := range []string{"retain", "promote", "many", "unrebuilt"} {
		t.Run(mode, func(t *testing.T) {
			//nolint:gosec // Executes the native probe built immediately above.
			native, err := exec.CommandContext(ctx, probe, model, mode).CombinedOutput()
			if err != nil {
				t.Fatalf("native probe: %v: %s", err, native)
			}
			set := reviewDiffSet(t, module)
			src, _, _, err := parseOp(ctx, strings.NewReader(`<l xmlns="urn:stage3-extra"><k>1</k><act><x>q</x></act></l>`), set, FormatXML, opRPC, nil, Reject)
			if err != nil {
				t.Fatal(err)
			}
			add := func(tr *Tree, key string) {
				t.Helper()
				if _, err := tr.NewList(nil, "stage3-extra:l", []string{key}, NewOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			for _, key := range []string{"3", "5", "7", "9", "11", "13"} {
				add(src, key)
			}
			if _, err := src.ValidateOp(ctx, OpRPC, ValidateOpOptions{}); err != nil {
				t.Fatal(err)
			}
			owner := src.top.At(1)
			if mode != "unrebuilt" {
				add(src, "2")
				owner = src.top.At(2)
			}
			if mode == "promote" {
				for range 2 {
					if err := src.top.first().Remove(); err != nil {
						t.Fatal(err)
					}
				}
				owner = src.top.first()
			}
			if mode == "many" {
				src = newTree(set)
				add(src, "3")
				add(src, "11")
				for range 32 {
					dst := newTree(set)
					if _, _, err := dst.Dup(src.top.first(), nil, DupOptions{Recursive: true, NoSort: true}); err != nil {
						t.Fatal(err)
					}
					src = newTree(set)
					for _, key := range []string{"5", "7", "9"} {
						add(src, key)
					}
					if err := dst.Insert(nil, src.top.first()); err != nil {
						t.Fatal(err)
					}
					src = dst
				}
				owner = src.top.first()
			}
			groups := src.top.metaGroups(owner)
			kept := groups[len(groups)-1]
			keptOrder := src.top.rbOrders[kept]
			dupTree := newTree(set)
			dup, _, err := dupTree.Dup(owner, nil, DupOptions{Recursive: true, NoSort: true})
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			fmt.Fprintf(&got, "dup-slots=%d\n", len(dupTree.top.metaGroups(dup)))
			for _, keys := range [][]string{nil, {"0", "4", "8"}, {"10", "12", "14"}} {
				dst := newTree(set)
				for _, key := range keys {
					add(dst, key)
				}
				if err := dst.Insert(nil, src.top.first()); err != nil {
					t.Fatal(err)
				}
				checkTreeRetainedRB(t, dst)
				if !dst.top.ownsGroup(owner, kept) || dst.top.rbOrders[kept] != keptOrder || keptOrder.work != &dst.work {
					t.Fatal("whole transfer replaced or lost the retained metadata")
				}
				if err := dst.PrintJSON(&got, PrintOptions{Shrink: true}); err != nil {
					t.Fatal(err)
				}
				got.WriteByte('\n')
				src = dst
			}
			if strings.TrimSpace(got.String()) != strings.TrimSpace(string(native)) {
				t.Fatalf("native=%s\nGo=%s", native, got.String())
			}
			t.Logf("native metadata identity and output match:\n%s", native)
		})
	}
}
