// SPDX-License-Identifier: BSD-3-Clause

package compile

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/xpath"
)

// TestXPathStepsRealModels measures the per-Load XPath step totals of U-0036 on real models
// (#14, design 06 §5): every published IETF/IANA module of conformance/corpus/public-ietf is
// loaded with all features into one context, as the M2 bundle would be. Each Load gets the
// default budget; the largest total must stay far below it, so the default never refuses a
// real model set libyang loads.
func TestXPathStepsRealModels(t *testing.T) {
	dir := "../../conformance/corpus/public-ietf/schemas"
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, _, err := NewContext(Options{}, os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	var maxUsed, total, loads int
	maxMod := ""
	for _, e := range ents {
		name, _, _ := strings.Cut(strings.TrimSuffix(e.Name(), ".yang"), "@")
		if name == "ietf-ipv6-router-advertisements" { // submodule
			continue
		}
		_, _, err := c.Load(name, "", []string{"*"})
		if errors.Is(err, ErrUnsupported) { // U-0024 schema-mount, U-0025 openconfig POSIX patterns
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		used := xpath.DefaultMaxSteps - c.xpathSteps
		loads++
		total += used
		if used > maxUsed {
			maxUsed, maxMod = used, name
		}
	}
	t.Logf("%d Loads, %d steps in all, largest Load %d steps (%s), default budget %d",
		loads, total, maxUsed, maxMod, xpath.DefaultMaxSteps)
	if loads < 25 || maxUsed == 0 {
		t.Fatalf("measured nothing: %d Loads, largest %d", loads, maxUsed)
	}
	if maxUsed*100 > xpath.DefaultMaxSteps {
		t.Errorf("largest Load %s takes %d XPath steps, over 1%% of the default %d: revisit U-0036",
			maxMod, maxUsed, xpath.DefaultMaxSteps)
	}
}
