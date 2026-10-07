// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"strings"
	"testing"

	"github.com/vibe-ports/yang/conformance"
)

func TestRender(t *testing.T) {
	fx := []conformance.Fixture{
		{ID: "a", Areas: []string{"types"}, Request: map[string]any{"op": "data"}},
		{ID: "b", Areas: []string{"types"}, Request: map[string]any{"op": "data"}},
		{ID: "c", Areas: []string{"xpath"}, Request: map[string]any{"op": "xpath"}},
		{ID: "d", Areas: []string{"schema"}, Request: map[string]any{"op": "schema"}},
		{ID: "e", Areas: []string{"schema"}, Request: map[string]any{"op": "schema"}},
	}
	rep := conformance.Report{Results: []conformance.Result{
		{ID: "a", Areas: fx[0].Areas, Status: conformance.Differ, Detail: "verdict\nmore"},
		{ID: "b", Areas: fx[1].Areas, Status: conformance.AgreeSkipped, Detail: "skipped: typed.meta, typed.any"},
		{ID: "c", Areas: fx[2].Areas, Status: conformance.Unsupported},
		{ID: "d", Areas: fx[3].Areas, Status: conformance.Deviation, Detail: "D-0070"},
		{ID: "e", Areas: fx[4].Areas, Status: conformance.Agree},
	}}
	got := render("yang", fx, rep, false)
	for _, want := range []string{
		"| **fixtures** | 1 | 1 | 1 | 1 | 1 |",
		"### Differ (1)\n\n- `a`: verdict\n",
		"### Deviation: intentional, see conformance/deviations.md (1)\n\n- `d`: D-0070\n",
		"- `typed.any`: 1 fixtures\n- `typed.meta`: 1 fixtures\n",
		"### Unsupported fixtures per operation (1)\n\n- xpath: 1\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "more") || strings.Contains(got, "plainly") {
		t.Errorf("unexpected detail without -all:\n%s", got)
	}
	if !strings.Contains(render("yang", fx, rep, true), "- `c`: unsupported") {
		t.Error("-all does not list the unsupported fixture")
	}
}
