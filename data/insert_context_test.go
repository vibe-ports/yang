// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"slices"
	"testing"
)

func TestCrossContextSortedSchemaAbsent(t *testing.T) {
	set1, set2 := utDiffNamedSet(t, "diff-cross-context"), utDiffNamedSet(t, "diff-cross-context")
	const before = `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><x><x><a>old</a></x></x></top>`
	const after = `<top xmlns="urn:vibe-ports:yang:diff-cross-context"><identities>derived</identities></top>`
	first, second := parseOnlyXML(t, set1, before), parseOnlyXML(t, set2, after)
	diff, err := Diff(first, second, DiffOptions{})
	if err != nil || diff == nil {
		t.Fatalf("Diff = (%v, %v)", diff, err)
	}
	// No existing identities instance: lyd_insert_node cannot use the sorted branch
	// (tree_data.c:820-822), so the foreign owner-module boundary wins (556-562).
	if got, want := names(topOf(diff).Children()), []string{"identities=diff-cross-context:derived", "x"}; !slices.Equal(got, want) {
		t.Fatalf("diff children = %v, want %v", got, want)
	}
}
