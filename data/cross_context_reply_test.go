// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/snap"
)

// TestDiffCrossContextReplyOutputLeafList is ut-parser/rb.1: identical replies from separately
// compiled contexts. With four output instances the children table exists, and the foreign
// output leaf-list still matches by schema lineage (lyd_find_schema_ctx would walk the input
// schemas and miss it), so the diff is empty with and without the table.
func TestDiffCrossContextReplyOutputLeafList(t *testing.T) {
	const module = `module rb { yang-version 1.1; namespace "urn:rb"; prefix rb;
  rpc r { input { leaf in { type string; } } output { leaf-list v { type uint8; } } } }`
	reply := func(data string) *Tree {
		res, ds, err := ParseOp(context.Background(), strings.NewReader(data), FormatJSON,
			snap.New(reviewDiffSet(t, module)), OpReply, ParseOpOptions{})
		if err != nil {
			t.Fatalf("parse reply: %v (%v)", err, ds)
		}
		return res.Tree
	}
	for _, data := range []string{`{"rb:r":{"v":[1,2,3]}}`, `{"rb:r":{"v":[1,2,3,4]}}`} {
		first, second := reply(data), reply(data)
		if diff, err := Diff(first, second, DiffOptions{}); diff != nil || err != nil {
			t.Fatalf("%s: Diff = (%v, %v), want no difference", data, diff, err)
		}
	}
}
