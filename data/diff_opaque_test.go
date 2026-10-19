// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"errors"
	"testing"
)

func TestReverseOpaqueOperations(t *testing.T) {
	set := utDiffSet(t)
	for _, tc := range []struct{ from, to string }{{"create", "delete"}, {"delete", "create"}} {
		t.Run(tc.from, func(t *testing.T) {
			diff := parseOnlyXML(t, set, `<unknown xmlns="urn:libyang:tests:defaults" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="`+tc.from+`">v</unknown>`)
			reversed, err := diff.ReverseDiff()
			if err != nil {
				t.Fatal(err)
			}
			n := topOf(reversed)
			if op, found := diffGetOp(n); !found || op.String() != tc.to {
				t.Fatalf("operation = %v, found %v", op, found)
			}
			if len(n.opaq.Attrs) != 1 || n.opaq.Attrs[0].Value != tc.to {
				t.Fatalf("attributes = %+v", n.opaq.Attrs)
			}
		})
	}
}

func TestReverseOpaqueReplaceRefused(t *testing.T) {
	set := utDiffSet(t)
	diff := parseOnlyXML(t, set, `<unknown xmlns="urn:libyang:tests:defaults" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="replace">v</unknown>`)
	_, err := diff.ReverseDiff()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
		t.Fatalf("ReverseDiff = %v", err)
	}
}

func TestReverseOpaqueNoneRefused(t *testing.T) {
	set := utDiffSet(t)
	diff := parseOnlyXML(t, set, `<unknown xmlns="urn:libyang:tests:defaults" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none">v</unknown>`)
	_, err := diff.ReverseDiff()
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" || len(ve.Diags) == 0 ||
		ve.Diags[0].Msg != `Operation "none" is invalid for opaque node "unknown".` {
		t.Fatalf("ReverseDiff = %v", err)
	}
}

func TestMergeOpaqueMatchedRefused(t *testing.T) {
	set := utDiffSet(t)
	create := parseOnlyXML(t, set, `<unknown xmlns="urn:libyang:tests:defaults" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="create">v</unknown>`)
	deleteDiff := parseOnlyXML(t, set, `<unknown xmlns="urn:libyang:tests:defaults" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="delete">v</unknown>`)
	err := create.MergeDiff(deleteDiff, MergeDiffOptions{})
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
		t.Fatalf("MergeDiff = %v", err)
	}
}
