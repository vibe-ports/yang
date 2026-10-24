// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// TestSweepInputs keeps all 40 fixed-seed inputs reproducible. The three native undefined-
// behaviour inputs are deliberately not corpus fixtures with goldens; the Go port refuses each
// complete diff under D-0121.
func TestSweepInputs(t *testing.T) {
	root := t.TempDir()
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic conformance input
	for i := range fixtureCount {
		if err := writeFixture(root, i, rng); err != nil {
			t.Fatal(err)
		}
	}
	for i := range fixtureCount {
		suffix := fmt.Sprintf("%02d", i)
		manifest := filepath.Join(root, "conformance", "corpus", "manifest.d", "ut-diff", "sweep", "sweep-"+suffix+".yaml")
		if _, err := os.Stat(manifest); err != nil {
			t.Fatalf("input %s: %v", suffix, err)
		}
	}
	for _, index := range []int{4, 20, 22} {
		requireSweepD0121(t, root, index)
	}
}

func requireSweepD0121(t *testing.T, root string, index int) {
	t.Helper()
	suffix := fmt.Sprintf("%02d", index)
	module := "diff-sweep-" + suffix
	base := filepath.Join(root, "conformance", "corpus")
	read := func(name string) []byte {
		b, err := os.ReadFile(name) //nolint:gosec // generated below t.TempDir by this test
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	model := func(side string) *yang.Schema {
		path := filepath.Join(base, "ut-diff", "schemas", "sweep-"+suffix+"-"+side, module+".yang")
		ctx, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, fstest.MapFS{
			module + ".yang": {Data: read(path)},
		})
		if err != nil {
			t.Fatal(err)
		}
		if diags, err := ctx.Load(module, "", nil); err != nil {
			t.Fatal(err, diags)
		}
		return ctx.Schema()
	}
	manifest := read(filepath.Join(base, "manifest.d", "ut-diff", "sweep", "sweep-"+suffix+".yaml"))
	matches := regexp.MustCompile(`data: '([^']*)'`).FindAllSubmatch(manifest, -1)
	if len(matches) != 2 {
		t.Fatalf("sweep %s: found %d data inputs", suffix, len(matches))
	}
	parse := func(schema *yang.Schema, input []byte) *data.Tree {
		tr, diags, err := data.Parse(context.Background(), strings.NewReader(string(input)), data.FormatXML,
			schema, data.ParseOptions{ParseOnly: true})
		if err != nil {
			t.Fatal(err, diags)
		}
		return tr
	}
	first, second := parse(model("a"), matches[0][1]), parse(model("b"), matches[1][1])
	diff, err := data.Diff(first, second, data.DiffOptions{})
	var validation *data.ValidationError
	if diff != nil || !errors.As(err, &validation) || validation.RC() != "LY_EINVAL" || !strings.Contains(err.Error(), "D-0121") {
		t.Fatalf("sweep %s: Diff = (%v, %v), want nil LY_EINVAL D-0121", suffix, diff, err)
	}
}
