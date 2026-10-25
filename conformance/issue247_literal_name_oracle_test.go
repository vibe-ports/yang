// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestIssue247MixedConstructorContextsLiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	for _, kind := range []string{"insert_term", "insert_inner", "insert_list", "insert_list2"} {
		for _, literal := range []bool{false, true} {
			for _, reverse := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/literal=%t/reverse=%t", kind, literal, reverse), func(t *testing.T) {
					base, p := issue247MixedConstructorRequest(t, kind, literal, reverse)
					want, err := oracle(base, p)
					if err != nil {
						t.Fatal(err)
					}
					got, err := (Yang{}).Run(Request{ID: t.Name(), BaseDir: base, Params: p})
					if err != nil {
						t.Fatal(err)
					}
					if diff := diffResponses(want, got); diff != "" || !reflect.DeepEqual(want.Diagnostics(), got.Diagnostics()) {
						t.Fatalf("%s\nnative=%#v\ngo=%#v", diff, want, got)
					}
				})
			}
		}
	}
}

func TestIssue247LiteralConstructorNamesLiveOracle(t *testing.T) {
	oracle := nativeRequestRunner(t)
	base := filepath.Join("corpus", "ut-new")
	for _, tc := range issue247LiteralNameCases() {
		t.Run(tc.id, func(t *testing.T) {
			want, err := oracle(base, tc.params)
			if err != nil {
				t.Fatal(err)
			}
			got := issue247Run(t, base, tc)
			if diff := diffResponses(want, got); diff != "" {
				t.Fatalf("oracle mismatch: %s\noracle: %#v\nengine: %#v", diff, want, got)
			}
			if !reflect.DeepEqual(want.Diagnostics(), got.Diagnostics()) {
				t.Fatalf("full diagnostics mismatch\noracle: %#v\nengine: %#v", want.Diagnostics(), got.Diagnostics())
			}
		})
	}
}

// The protocol has no general new_any, varargs new_list or new_attr request.
// Exercise all nine native entry
// points directly, so the audit also checks their distinct metadata/attribute name grammar.
// Only the test-only oracle directory contains C; this invokes the pinned container library.
func TestIssue247NativeConstructorNameRules(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "constructor-names")
	//nolint:gosec // Fixed source and flags; output is inside the test's private temporary directory.
	compile := exec.CommandContext(ctx, "cc", "-Wall", "-Wextra", "-Werror", "-std=c11",
		"-I/opt/libyang/include", "oracle/constructor_names.c", "-L/opt/libyang/lib",
		"-Wl,-rpath,/opt/libyang/lib", "-lyang", "-o", binary)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile native probe: %v\n%s", err, out)
	}
	out, err := exec.CommandContext(ctx, binary).CombinedOutput() //nolint:gosec // Executes only the native probe just built above.
	if err != nil {
		t.Fatalf("native probe: %v\n%s", err, out)
	}
	want := []string{
		`new_term|5|3|Term node "names:term" not found.`,
		`new_inner|5|3|Inner node (container, notif, RPC, or action) "names:inner" not found.`,
		`new_list|5|3|List node "names:list" not found.`,
		`new_list2|5|3|List node "names:list" not found.`,
		`new_list3|5|3|List node "names:list" not found.`,
		`new_any|5|3|Any node "names:any" not found.`,
		`new_meta|0|names|note`,
		`new_attr|0|names|note`,
		`new_opaque|0|names:c`,
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native constructor rules:\n got %q\nwant %q", got, want)
	}
	t.Logf("libyang v5.8.6 constructor name evidence:\n%s", out)
}

// Keep the constructor active after duplication into another context and a merge back into
// the retained context. Assert the native return code so an earlier setup failure cannot
// make the two responses agree by skipping the constructor.
func TestIssue247LiteralConstructorAfterForeignMergeLiveOracle(t *testing.T) {
	target := map[string]any{"searchdirs": []any{"schemas"}, "modules": []any{map[string]any{"name": "pv2-edit"}}, "context_options": []any{"no_yanglibrary"}}
	p := map[string]any{"op": "sequence", "searchdirs": target["searchdirs"], "modules": target["modules"], "context_options": target["context_options"], "steps": []any{
		map[string]any{"do": "parse", "format": "json", "data_type": "data", "parse_only": true, "data": `{"pv2-edit:c":{"l":[{"k":"keep","v":1}]}}`},
		map[string]any{"do": "dup", "node": "/pv2-edit:c", "options": []any{"recursive"}, "target": target},
		map[string]any{"do": "edit", "format": "json", "data_type": "data", "merge_into": `{"pv2-edit:c":{"a":"new"}}`},
		map[string]any{"do": "edit", "insert_term": map[string]any{"parent": "/pv2-edit:c/l[k='keep']", "name": "pv2-edit:v", "value": "2"}},
	}}
	want, err := nativeRequestRunner(t)("corpus/protocol-v2", p)
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Yang{}).Run(Request{ID: "protocol-v2/issue247-literal-after-foreign-merge", BaseDir: "corpus/protocol-v2", Params: p})
	if err != nil {
		t.Fatal(err)
	}
	if want["rc"].(map[string]any)["name"] != "LY_ENOTFOUND" {
		t.Fatalf("native did not reach not-found constructor: %#v", want)
	}
	if diff := diffResponses(want, got); diff != "" {
		t.Fatalf("%s\nnative=%#v\ngo=%#v", diff, want, got)
	}
}
