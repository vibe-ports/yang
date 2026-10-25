// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

const newAPI1Module = `module api1 {
  yang-version 1.1;
  namespace "urn:api1";
  prefix api1;
  leaf n { type uint8 { range "1..10"; } default 5; }
  list l { key k; leaf k { type uint8 { range "1..10"; } } }
  list keyless { config false; leaf v { type string; } }
  container c { leaf s { type string; } }
}`

func newAPI1Schema(t *testing.T) *yang.Schema {
	t.Helper()
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, fstest.MapFS{
		"api1.yang": {Data: []byte(newAPI1Module)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if diags, err := c.Load("api1", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	return c.Schema()
}

// TestCanonicalConstructorAPI reaches the exported LYD_NEW_VAL_STORE_ONLY and
// LYD_NEW_VAL_CANON paths. It does not restate the store: the restricted values distinguish the
// option from the old JSON-only public wrapper, and the combined-option diagnostics name the
// native entry points.
func TestCanonicalConstructorAPI(t *testing.T) {
	s := newAPI1Schema(t)

	tr := data.NewTree(s)
	stored, err := tr.NewTerm(nil, "api1:n", "20", data.NewOptions{StoreOnly: true})
	if err != nil || stored.Value() != "20" {
		t.Fatalf("NewTerm store-only = %v, %v", stored, err)
	}
	canonical, err := data.NewTree(s).NewTerm(nil, "api1:n", "01", data.NewOptions{Canonical: true})
	if err != nil || canonical.Value() != "01" {
		t.Fatalf("NewTerm canonical value = %q, error %v", canonical.Value(), err)
	}
	if _, err := data.NewTree(s).NewTerm(nil, "api1:n", "1",
		data.NewOptions{StoreOnly: true, Canonical: true}); rcMsg(t, err) !=
		"LY_EINVAL | Invalid argument !(store_only && (format == LY_VALUE_CANON)) (_lyd_new_term())." {
		t.Fatalf("NewTerm combined options: %s", rcMsg(t, err))
	}

	list, err := data.NewTree(s).NewList(nil, "api1:l", []string{"20"}, data.NewOptions{StoreOnly: true})
	if err != nil || list.Path() != "/api1:l[k='20']" {
		t.Fatalf("NewList store-only = %v, %v", list, err)
	}
	list, err = data.NewTree(s).NewList(nil, "api1:l", []string{"01"}, data.NewOptions{Canonical: true})
	if err != nil || list.Path() != "/api1:l[k='01']" {
		t.Fatalf("NewList canonical = %v, %v", list, err)
	}
	if _, err := data.NewTree(s).NewList(nil, "api1:l", []string{"1"},
		data.NewOptions{StoreOnly: true, Canonical: true}); rcMsg(t, err) !=
		"LY_EINVAL | Invalid argument !(store_only && (format == LY_VALUE_CANON)) (lyd_new_list())." {
		t.Fatalf("NewList combined options: %s", rcMsg(t, err))
	}

	inner, err := data.NewTree(s).NewInner(nil, "api1:c", data.NewOptions{StoreOnly: true, Canonical: true})
	if err != nil || inner.Path() != "/api1:c" {
		t.Fatalf("NewInner ignored value options = %v, %v", inner, err)
	}
}

// TestNewList2API covers the behavior that NewPath could not reproduce: its own lookup and
// diagnostics, keyless handling, JSON predicate compilation and Canonical being ignored even in
// combination with StoreOnly.
func TestNewList2API(t *testing.T) {
	s := newAPI1Schema(t)
	for _, opts := range []data.NewOptions{
		{Canonical: true},
		{StoreOnly: true, Canonical: true},
	} {
		tr := data.NewTree(s)
		list, err := tr.NewList2(nil, "api1:l", "[k='01']", opts)
		if err != nil || list.Path() != "/api1:l[k='1']" {
			t.Fatalf("NewList2 options %+v = %v, %v", opts, list, err)
		}
	}
	if _, err := data.NewTree(s).NewList2(nil, "api1:l", "", data.NewOptions{}); rcMsg(t, err) !=
		`LY_EVALID | Unexpected XPath expression end.` {
		t.Fatalf("keyed empty predicates: %s", rcMsg(t, err))
	}
	if n, err := data.NewTree(s).NewList2(nil, "api1:keyless", "", data.NewOptions{}); err != nil ||
		n.Path() != "/api1:keyless[1]" {
		if err != nil {
			t.Fatalf("keyless empty predicates: %v", err)
		}
		t.Fatalf("keyless empty predicates path = %q", n.Path())
	}
	if _, err := data.NewTree(s).NewList2(nil, "api1:c", "", data.NewOptions{}); rcMsg(t, err) !=
		`LY_ENOTFOUND | List node "c" not found.` {
		t.Fatalf("non-list target: %s", rcMsg(t, err))
	}
}

// TestSetValueCanonicalAPI proves the exported setter reaches LY_VALUE_CANON and preserves
// SetValue's result mapping when only the Default flag is cleared.
func TestSetValueCanonicalAPI(t *testing.T) {
	s := newAPI1Schema(t)
	tr := data.NewTree(s)
	n, err := tr.NewTerm(nil, "api1:n", "1", data.NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := n.SetValueCanonical("02"); err != nil || !changed || n.Value() != "02" {
		t.Fatalf("canonical change = %t, %q, %v", changed, n.Value(), err)
	}
	if changed, err := n.SetValueCanonical("02"); err != nil || changed {
		t.Fatalf("canonical no-op = %t, %v", changed, err)
	}

	defaults, _, err := data.Parse(context.Background(), strings.NewReader(`{}`), data.FormatJSON, s, data.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dflt, err := defaults.Find("/api1:n")
	if err != nil || dflt == nil || dflt.Flags()&data.FlagDefault == 0 {
		t.Fatalf("default node = %v, %v", dflt, err)
	}
	if changed, err := dflt.SetValueCanonical("5"); err != nil || changed || dflt.Flags()&data.FlagDefault != 0 {
		t.Fatalf("default-only change = %t, flags %#x, %v", changed, dflt.Flags(), err)
	}
}
