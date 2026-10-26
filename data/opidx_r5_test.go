// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func opidxR5MetaID(n *Node) (string, bool) {
	for _, m := range n.meta {
		if m.mod.Name == "opidx239" && m.name == "id" {
			return m.value.Canonical(), true
		}
	}
	return "", false
}

// TestOpIndexMetaDocumentFirst replays ops/opidx-meta-doc-first. Freeing v1 leaves the
// first-instance records in v3,v2 order, but lydjson_parse_attribute finds the metadata target
// with LY_LIST_FOR in document sibling order (parser_json.c:1166-1170), so @v applies to v2.
func TestOpIndexMetaDocumentFirst(t *testing.T) {
	fixture := readOpsFixture(t, filepath.Join(opsManifest, "opidx-meta-doc-first.yaml"))
	set := opsSet(t, fixture)
	mod := set.Implemented("opidx239")

	replay := func(t *testing.T) (*Tree, *Node, []*Node) {
		t.Helper()
		tr, op, diags, err := parseOp(context.Background(), strings.NewReader(
			`{"opidx239:l":{"a":"1","a2":"2","v":[1,2]}}`), set, FormatJSON, opRPC, nil, Reject)
		if err != nil {
			t.Fatal(err, diags)
		}
		b := newBuilder{set: set, log: &logger{set: set}}
		add := func(name, value string, output bool) *Node {
			t.Helper()
			n, err := b.newTerm(op, mod, name, value, newValOptions{output: output})
			if err = b.log.done(err); err != nil {
				t.Fatal(err)
			}
			b.log = &logger{set: set}
			return n
		}
		add("o", "o", true)
		v3 := add("v", "3", false)
		vs := v3.schema
		values := opidxSchemaNodes(op, vs)
		if len(values) != 3 {
			t.Fatalf("v instances = %d, want 3", len(values))
		}
		if err := freeTree(values[0]); err != nil {
			t.Fatal(err)
		}
		values = opidxSchemaNodes(op, vs)
		if got, want := opidxRecords(op, vs), []string{"3", "2"}; !slices.Equal(got, want) {
			t.Fatalf("first-instance records = %v, want %v", got, want)
		}
		if got, want := opidxValues(op), []string{"a", "a2", "v:2", "o", "v:3"}; !slices.Equal(got, want) {
			t.Fatalf("children = %v, want %v", got, want)
		}
		return tr, op, values
	}

	t.Run("metadata-only-parse", func(t *testing.T) {
		tr, op, values := replay(t)
		if _, _, diags, err := parseOp(context.Background(), strings.NewReader(
			`{"@opidx239:v":[{"opidx239:id":"m"}]}`), set, FormatJSON, opRPC, op, Reject); err != nil {
			t.Fatal(err, diags)
		}
		if got, ok := opidxR5MetaID(values[0]); !ok || got != "m" {
			t.Errorf("v2 metadata = %q, %t; want id=m", got, ok)
		}
		if got, ok := opidxR5MetaID(values[1]); ok {
			t.Errorf("v3 metadata = %q; want none", got)
		}
		opidxCompareGoldens(t, fixture, []int{5}, []opidxSnapshot{opidxSnapshotOf(t, tr)})
	})

	t.Run("value-before-metadata-in-same-parse", func(t *testing.T) {
		_, op, _ := replay(t)
		if _, _, diags, err := parseOp(context.Background(), strings.NewReader(
			`{"opidx239:v":[4],"@opidx239:v":[{"opidx239:id":"n"}]}`),
			set, FormatJSON, opRPC, op, Reject); err != nil {
			t.Fatal(err, diags)
		}

		// lydjson_parse_attribute walks from the first sibling to the first matching schema
		// (parser_json.c:1166-1170); lydjson_meta_attr then counts leaf-list metadata instances
		// by advancing node->next (parser_json.c:828-834). The new v4 does not become instance 1.
		values := opidxSchemaNodes(op, op.kids.At(2).schema)
		if got, want := opidxValues(op), []string{"a", "a2", "v:2", "o", "v:3", "v:4"}; !slices.Equal(got, want) {
			t.Fatalf("children = %v, want %v", got, want)
		}
		if got, ok := opidxR5MetaID(values[0]); !ok || got != "n" {
			t.Errorf("v2 metadata = %q, %t; want id=n", got, ok)
		}
		for _, n := range values[1:] {
			if got, ok := opidxR5MetaID(n); ok {
				t.Errorf("v%s metadata = %q; want none", n.value.Canonical(), got)
			}
		}
	})
}

// TestOpIndexXMLDeferredSortPath replays ops/opidx-xml-deferred-sort-path. libyang inserts each
// system-ordered leaf-list instance through lyds_insert, so value 1 has already sorted before
// value 2 when the child-element error is logged and its data path is /rb:r/il[1].
func TestOpIndexXMLDeferredSortPath(t *testing.T) {
	fixture := readOpsFixture(t, filepath.Join(opsManifest, "opidx-xml-deferred-sort-path.yaml"))
	b, err := os.ReadFile(filepath.Join("../conformance/corpus", fixture.dir, fixture.golden))
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		RC struct {
			Name string `json:"name"`
		} `json:"rc"`
		Diagnostics []struct {
			Code struct {
				Name string `json:"name"`
			} `json:"code"`
			VecodeName string  `json:"vecode_name"`
			DataPath   *string `json:"data_path"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	if len(want.Diagnostics) != 1 || want.Diagnostics[0].DataPath == nil {
		t.Fatalf("golden diagnostics = %+v, want one diagnostic with a data path", want.Diagnostics)
	}

	t.Run("XML", func(t *testing.T) {
		set := opsSet(t, fixture)
		_, _, diags, err := parseOp(context.Background(), strings.NewReader(fixture.data),
			set, FormatXML, opRPC, nil, Reject)
		if err == nil {
			t.Fatal("parse succeeded, want golden failure")
		}
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Fatalf("error %T %v, want ValidationError", err, err)
		}
		if got := ve.RC(); got != want.RC.Name {
			t.Errorf("rc = %s, want %s", got, want.RC.Name)
		}
		if len(diags) != len(want.Diagnostics) {
			t.Fatalf("diagnostics = %+v, want %d", diags, len(want.Diagnostics))
		}
		if got := diags[0].Err; got != want.Diagnostics[0].Code.Name {
			t.Errorf("diagnostic error code = %s, want %s", got, want.Diagnostics[0].Code.Name)
		}
		if got := diags[0].Code; got != want.Diagnostics[0].VecodeName {
			t.Errorf("diagnostic validation code = %s, want %s", got, want.Diagnostics[0].VecodeName)
		}
		if got := diags[0].DataPath; got != *want.Diagnostics[0].DataPath {
			t.Errorf("diagnostic data path = %q, want %q", got, *want.Diagnostics[0].DataPath)
		}
	})

	t.Run("JSON", func(t *testing.T) {
		// JSON leaf-list instances are scalar array entries, so they cannot contain an
		// unexpected child. For an invalid scalar, lydjson_parse_instance_term validates and
		// creates the term before lyd_parser_node_insert (parser_json.c:1357-1367); on failure
		// there is no instance from which libyang could format a positional data path.
		t.Skip("libyang cannot produce the equivalent mid-instance positional path for a JSON leaf-list scalar")
	})
}
