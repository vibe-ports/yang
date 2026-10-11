// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

type diffOpStep struct {
	Do        string `json:"do"`
	Format    string `json:"format"`
	DataType  string `json:"data_type"`
	Unknown   string `json:"unknown"`
	RPC       string `json:"rpc"`
	Data      string `json:"data"`
	ParseOnly bool   `json:"parse_only"`
	Merge     bool   `json:"merge"`
}

type diffOpPrinted struct {
	JSON string `json:"json"`
}

type diffOpGoldenStep struct {
	goldenStep
	Do      string         `json:"do"`
	Diff    *diffOpPrinted `json:"diff"`
	NewDiff *diffOpPrinted `json:"new_diff"`
	Tree    *diffOpPrinted `json:"tree"`
}

var diffOpStepRE = regexp.MustCompile(`(?m)^        - (\{.*\})$`)

func readDiffOpFixture(t *testing.T, path string) (opsFixture, []diffOpStep) {
	t.Helper()
	f := readOpsFixture(t, path)
	b, err := os.ReadFile(path) //nolint:gosec // corpus path
	if err != nil {
		t.Fatal(err)
	}
	var steps []diffOpStep
	for _, match := range diffOpStepRE.FindAllSubmatch(b, -1) {
		var step diffOpStep
		if err := json.Unmarshal(match[1], &step); err != nil {
			t.Fatalf("step %q: %v", match[1], err)
		}
		steps = append(steps, step)
	}
	if len(steps) == 0 {
		t.Fatal("fixture has no sequence steps")
	}
	return f, steps
}

func parseDiffOpStep(t *testing.T, set *schema.Set, step diffOpStep) (*Tree, *Node) {
	t.Helper()
	if !step.ParseOnly || step.Unknown != "reject" {
		t.Fatalf("operation parse must use parse_only and unknown reject: %+v", step)
	}
	format := FormatJSON
	if step.Format == "xml" {
		format = FormatXML
	} else if step.Format != "json" {
		t.Fatalf("unsupported fixture format %q", step.Format)
	}

	if step.DataType == "reply" {
		tree, op, diags, err := parseOp(context.Background(), strings.NewReader(step.RPC), set,
			format, opRPC, nil, Reject)
		if err != nil {
			t.Fatalf("parse reply request: %v (%v)", err, diags)
		}
		if _, _, diags, err = parseOp(context.Background(), strings.NewReader(step.Data), set,
			format, opReply, op, Reject); err != nil {
			t.Fatalf("parse reply: %v (%v)", err, diags)
		}
		return tree, op
	}
	if step.DataType != "rpc" {
		t.Fatalf("unsupported fixture data_type %q", step.DataType)
	}
	tree, op, diags, err := parseOp(context.Background(), strings.NewReader(step.Data), set,
		format, opRPC, nil, Reject)
	if err != nil {
		t.Fatalf("parse rpc: %v (%v)", err, diags)
	}
	return tree, op
}

func compareDiffOpJSON(t *testing.T, label, got, want string) {
	t.Helper()
	var gc, wc bytes.Buffer
	if err := json.Compact(&gc, []byte(got)); err != nil {
		t.Fatalf("%s: parse got JSON: %v\n%s", label, err, got)
	}
	if err := json.Compact(&wc, []byte(want)); err != nil {
		t.Fatalf("%s: parse golden JSON: %v\n%s", label, err, want)
	}
	// Compare the compact token text, not unmarshaled maps: operation diff JSON can contain two
	// runs of the same schema child and therefore duplicate member names, whose order is part of
	// libyang's output.
	if !bytes.Equal(gc.Bytes(), wc.Bytes()) {
		t.Errorf("%s JSON:\n got  %s\n want %s", label, got, want)
	}
}

func compareDiffOpPrinted(t *testing.T, label string, got *Tree, want *diffOpPrinted, options PrintOptions) {
	t.Helper()
	if got == nil || want == nil {
		if (got == nil) != (want == nil) {
			t.Errorf("%s: got nil=%t, want nil=%t", label, got == nil, want == nil)
		}
		return
	}
	var out bytes.Buffer
	if err := got.PrintJSON(&out, options); err != nil {
		t.Fatalf("%s: PrintJSON: %v", label, err)
	}
	compareDiffOpJSON(t, label, out.String(), want.JSON)
}

// TestDiffOpOracle replays the operation-child diff fixtures against their libyang sequence
// goldens: generated and merged diffs, diff application, and the final operation tree.
func TestDiffOpOracle(t *testing.T) {
	fixtures := []string{
		"diff-json-reply-equal",
		"diff-json-reply-change",
		"diff-json-reply-reorder",
		"diff-json-reply-dup",
		"diff-json-rpc-input",
		"diff-merge-json-reply",
		"diff-xml-reply-change",
		"diff-json-reply-keep-input-delete",
		"diff-merge-json-reply-keep-input",
	}
	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			fixture, steps := readDiffOpFixture(t, filepath.Join(opsManifest, name+".yaml"))
			b, err := os.ReadFile(filepath.Join("../conformance/corpus", fixture.dir, fixture.golden))
			if errors.Is(err, fs.ErrNotExist) {
				t.Skip("golden not generated yet")
			}
			if err != nil {
				t.Fatal(err)
			}
			var golden struct {
				Steps []diffOpGoldenStep `json:"steps"`
			}
			if err := json.Unmarshal(b, &golden); err != nil {
				t.Fatal(err)
			}
			if len(golden.Steps) != len(steps) {
				t.Fatalf("golden has %d steps, fixture has %d", len(golden.Steps), len(steps))
			}

			set := opsSet(t, fixture)
			var tree, register *Tree
			var op *Node
			for i, step := range steps {
				want := golden.Steps[i]
				if want.Do != step.Do {
					t.Fatalf("step %d: golden do %q, fixture do %q", i, want.Do, step.Do)
				}
				switch step.Do {
				case "parse":
					tree, op = parseDiffOpStep(t, set, step)
				case "diff":
					if op == nil {
						t.Fatalf("step %d: diff before parse", i)
					}
					_, second := parseDiffOpStep(t, set, step)
					newDiff, err := DiffTree(op, second, DiffOptions{})
					if err != nil {
						t.Fatalf("step %d: DiffTree: %v", i, err)
					}
					var reportedNewDiff *Tree
					if step.Merge {
						if register == nil {
							t.Fatalf("step %d: merge with an empty diff register", i)
						}
						if err := register.MergeDiff(newDiff, MergeDiffOptions{}); err != nil {
							t.Fatalf("step %d: MergeDiff: %v", i, err)
						}
						reportedNewDiff = newDiff
					} else {
						register = newDiff
					}
					compareDiffOpPrinted(t, "diff", register, want.Diff, PrintOptions{WithDefaults: WDAll})
					compareDiffOpPrinted(t, "new_diff", reportedNewDiff, want.NewDiff, PrintOptions{WithDefaults: WDAll})
				case "diff_apply":
					if err := tree.ApplyDiff(register, ApplyDiffOptions{}); err != nil {
						t.Fatalf("step %d: ApplyDiff: %v", i, err)
					}
				case "dump":
					compareDiffOpPrinted(t, "tree", tree, want.Tree, PrintOptions{})
					if got := typedDump(tree); !reflect.DeepEqual(got, want.dump()) {
						t.Errorf("typed:\n got  %q\n want %q", got, want.dump())
					}
				default:
					t.Fatalf("step %d: unsupported do %q", i, step.Do)
				}
			}
		})
	}
}
