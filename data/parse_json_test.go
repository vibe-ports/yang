// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/schema"
)

func parseJSONString(set *schema.Set, in string, unknown UnknownPolicy, multi bool) (*Tree, []yang.Diagnostic, error) {
	o := parseOpts{ParseOptions: ParseOptions{Unknown: unknown, Validate: ValidateOptions{MultiError: multi}}}
	return parseWith(context.Background(), strings.NewReader(in), set, o, parseJSON, noValidation)
}

// noValidation keeps these parser tests at parse level (their expectations predate D8/D10).
func noValidation(lc *lydCtx) { lc.validateNewImplicit, lc.validate = nil, nil }

// TestParseJSON: lyd_parse_json against libyang v5.8.6 (the expectations are oracle probes over
// the same schema; multi is LYD_VALIDATE_MULTI_ERROR). Validation (D8/D10) is not wired yet, so
// only parse-time diagnostics and no implicit nodes appear.
func TestParseJSON(t *testing.T) {
	set := pjSchema()
	const (
		rej = Reject
		skp = Skip
		opq = Opaque
	)
	cases := []struct {
		name    string
		unknown UnknownPolicy
		multi   bool
		in      string
		want    []string // diagnostics, else the tree dump
	}{
		{"empty", rej, true, ``, []string{"LY_EVALID LYVE_SYNTAX ||1: Empty JSON file."}},
		{"top-array", rej, true, `[1]`, []string{
			"LY_EVALID LYVE_SYNTAX_JSON ||1: Expected top-level JSON object or correct bare value, but array found."}},
		{"unqualified-top", rej, true, `{"c": {}, "pj:top": 1}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON ||1: Top-level JSON object member "c" must be namespace-qualified.`,
			`LY_EVALID LYVE_DATA |/pj:top|1: Invalid non-string-encoded string value "1".`}},
		{"unknown-mod-reject", rej, true, `{"zz:x": 1, "pj:top": "a"}`, []string{
			`LY_EVALID LYVE_REFERENCE ||1: No module named "zz" in the context.`}},
		{"unknown-mod-skip", skp, true, `{"zz:x": {"a":[1,2]}, "pj:top": "a"}`, []string{"/pj:top=a"}},
		{"unknown-child-reject", rej, true, `{"pj:c": {"q": 1, "s": "a"}}`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c||1: Node "q" not found as a child of "c" node.`}},
		{"unknown-child-pref", rej, true, `{"pj:c": {"zz:q": 1, "pk:q": 2, "s": "a"}}`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c||1: No module named "zz" in the context.`,
			`LY_EVALID LYVE_REFERENCE /pj:c||1: Node "q" not found as a child of "c" node.`}},
		{"repr-container", rej, true, `{"pj:c": 5, "pj:top": "a"}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON ||1: Expecting JSON name/object but container "c" is represented in input data as name/number.`}},
		{"repr-ll", rej, true, `{"pj:c": {"ll": 1, "s": "a"}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c||1: Expecting JSON name/array of values but leaf-list "ll" is represented in input data as name/number.`}},
		// the skip leaves the lexer on the skipped object's end, so "c" closes there
		{"repr-list-obj", rej, true, `{"pj:c": {"l": {"k": "a"}, "s": "a"}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c||1: Expecting JSON name/array of objects but list "l" is represented in input data as name/object.`,
			`LY_EVALID LYVE_SYNTAX_JSON ||1: Top-level JSON object member "s" must be namespace-qualified.`}},
		{"string-in-list-arr", rej, true, `{"pj:c": {"l": ["x"], "s": "a"}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c||1: Expecting JSON name/array of objects but list "l" is represented in input data as name/string.`,
			`LY_EVALID LYVE_SYNTAX_JSON ||1: Top-level JSON object member "s" must be namespace-qualified.`}},
		{"repr-leaf-obj", rej, true, `{"pj:c": {"s": {"x": 1}, "n": 1}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON |/pj:c/s|1: Unexpected input data object.`}},
		{"repr-leaf-arr", rej, true, `{"pj:c": {"s": [1], "n": 1}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON |/pj:c/s|1: Expected JSON name/value or special name/[null], but input data contains name/[number].`}},
		{"repr-leaf-arr2", rej, true, `{"pj:c": {"s": [], "n": 1}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON |/pj:c/s|1: Expected JSON name/value or special name/[null], but input data contains name/[array closed].`}},
		{"empty-ok", rej, true, `{"pj:c": {"e": [null]}}`, []string{"/pj:c", "/pj:c/e="}},
		{"empty-bad", rej, true, `{"pj:c": {"e": [null, 1], "n": 1}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON |/pj:c/e|1: Expected array end, but input data contains null.`}},
		{"empty-null", rej, true, `{"pj:c": {"e": null, "n": 1}}`, []string{
			`LY_EVALID LYVE_DATA /pj:c/e||1: Invalid non-empty-encoded empty value "".`}},
		{"types-multi", rej, true, `{"pj:c": {"n": "x", "ll": [1, 300, 2, "4"], "s": 5}}`, []string{
			`LY_EVALID LYVE_DATA /pj:c/n||1: Invalid non-number-encoded int32 value "x".`,
			`LY_EVALID LYVE_DATA /pj:c/ll||1: Value "300" is out of type uint8 min/max bounds.`,
			`LY_EVALID LYVE_DATA /pj:c/ll||1: Invalid non-number-encoded uint8 value "4".`,
			`LY_EVALID LYVE_DATA /pj:c/s||1: Invalid non-string-encoded string value "5".`}},
		{"types-single", rej, false, `{"pj:c": {"n": "x", "ll": [1, 300, 2, "4"], "s": 5}}`, []string{
			`LY_EVALID LYVE_DATA /pj:c/n||1: Invalid non-number-encoded int32 value "x".`}},
		{"lines", rej, true, "{\n \"pj:c\": {\n  \"s\": \"a\",\n  \"n\": \"x\"\n }\n}", []string{
			`LY_EVALID LYVE_DATA /pj:c/n||5: Invalid non-number-encoded int32 value "x".`}},
		{"list-missing-key", rej, true, `{"pj:c": {"l": [{"v": "a"}, {"k": "b"}]}}`, []string{
			`LY_EVALID LYVE_DATA /pj:l||1: List instance is missing its key "k".`}},
		{"list-key-order", rej, true, `{"pj:c": {"l2": [{"v": "x", "b": "2", "a": "1"}]}}`, []string{
			"/pj:c", "/pj:c/l2[a='1'][b='2']", "/pj:c/l2[a='1'][b='2']/a=1", "/pj:c/l2[a='1'][b='2']/b=2",
			"/pj:c/l2[a='1'][b='2']/v=x"}},
		{"augment-and-sorted", rej, true, `{"pk:top": "b", "pj:c": {"pk:ext": "e", "ll": [3, 1, 2]}, "pj:top": "a"}`, []string{
			"/pj:c", "/pj:c/ll[.='1']=1", "/pj:c/ll[.='2']=2", "/pj:c/ll[.='3']=3", "/pj:c/pk:ext=e", "/pj:top=a",
			"/pk:top=b"}},

		// metadata (probes in single-error mode)
		{"meta-leaf-after", rej, false, `{"pj:c": {"s": "a", "@s": {"pj:ann": "x", "pj:num": 7}}}`, []string{
			"/pj:c", "/pj:c/s=a @pj:ann=x @pj:num=7"}},
		{"meta-leaf-before", rej, false, `{"pj:c": {"@s": {"pj:ann": "x"}, "s": "a"}}`, []string{
			"/pj:c", "/pj:c/s=a @pj:ann=x"}},
		{"meta-ll-after", rej, false, `{"pj:c": {"ll": [3, 1, 2], "@ll": [{"pj:ann": "three"}, null, {"pj:ann": "two"}]}}`, []string{
			"/pj:c", "/pj:c/ll[.='1']=1 @pj:ann=three", "/pj:c/ll[.='2']=2", "/pj:c/ll[.='3']=3 @pj:ann=two"}},
		{"meta-ll-before", rej, false, `{"pj:c": {"@ll": [{"pj:ann": "three"}, {"pj:ann": "one"}, {"pj:ann": "two"}], "ll": [3, 1, 2]}}`, []string{
			"/pj:c", "/pj:c/ll[.='1']=1 @pj:ann=three", "/pj:c/ll[.='2']=2 @pj:ann=one", "/pj:c/ll[.='3']=3 @pj:ann=two"}},
		{"meta-ll-too-many", rej, false, `{"pj:c": {"ll": [3], "@ll": [null, {"pj:ann": "two"}], "s": "a"}}`, []string{
			`LY_EVALID LYVE_REFERENCE |/pj:c/ll|1: Missing JSON data instance #2 of pj:ll to be coupled with metadata.`}},
		{"meta-ll-before-too-many", rej, false, `{"pj:c": {"@ll": [{"pj:ann": "a"}, {"pj:ann": "b"}], "ll": [3], "s": "a"}}`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c/@ll||1: Missing JSON data instance #2 to be coupled with @ll metadata.`}},
		{"meta-unknown-ann", rej, false, `{"pj:c": {"s": "a", "@s": {"pj:nope": "x"}, "n": 1}}`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c/s/@pj:nope||1: Annotation definition for attribute "pj:nope" not found.`}},
		{"meta-unknown-ann-multi", rej, true, `{"pj:c": {"s": "a", "@s": {"pj:nope": "x"}, "n": "y"}}`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c/s/@pj:nope||1: Annotation definition for attribute "pj:nope" not found.`}},
		{"meta-unprefixed", rej, false, `{"pj:c": {"s": "a", "@s": {"ann": "x"}, "n": 1}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c/s||1: Metadata in JSON must be namespace-qualified, missing prefix for "ann".`}},
		{"meta-unknown-mod-reject", rej, false, `{"pj:c": {"s": "a", "@s": {"zz:ann": "x"}, "n": 1}}`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c/s||1: Prefix "zz" of the metadata "ann" does not match any module in the context.`}},
		{"meta-unknown-mod-skip", skp, false, `{"pj:c": {"s": "a", "@s": {"zz:ann": "x"}, "n": 1}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c/s||1: The attribute(s) of leaf "s" is expected to be represented as JSON @name/object, but input data contains @string/name.`}},
		{"meta-bad-value", rej, false, `{"pj:c": {"s": "a", "@s": {"pj:num": "x"}, "n": 1}}`, []string{
			`LY_EVALID LYVE_DATA /pj:c/s/@pj:num||1: Invalid non-number-encoded uint8 value "x".`}},
		{"meta-bad-value-before", rej, false, `{"pj:c": {"@s": {"pj:num": "x"}, "s": "a", "n": 1}}`, []string{
			`LY_EVALID LYVE_DATA /pj:c/s/@pj:num||1: Invalid non-number-encoded uint8 value "x".`}},
		{"meta-missing-inst", rej, false, `{"pj:c": {"@s": {"pj:ann": "x"}, "n": 1}}`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c/@s||1: Missing JSON data instance to be coupled with @s metadata.`}},
		{"meta-container", rej, false, `{"pj:c": {"@": {"pj:ann": "x"}, "s": "a"}}`, []string{
			"/pj:c @pj:ann=x", "/pj:c/s=a"}},
		{"meta-top-at", rej, false, `{"@": {"pj:ann": "x"}, "pj:top": "a"}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON ||1: Invalid metadata format - "@" can be used only inside anydata, container or list entries.`}},
		{"meta-top-at-multi", rej, true, `{"pj:top": "a", "@": {"pj:ann": "x"}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON ||1: Invalid metadata format - "@" can be used only inside anydata, container or list entries.`,
			`LY_EVALID LYVE_SYNTAX_JSON ||1: Top-level JSON object member "@" must be namespace-qualified.`,
			`LY_EVALID LYVE_REFERENCE /@||1: Missing JSON data instance to be coupled with @ metadata.`}},
		// libyang logs the second error once per top-level schema sibling, twice here (D-0060)
		{"meta-top-at-two-siblings", rej, true, `{"pj:top": "a", "pk:top": "b", "@": {"pj:ann": "x"}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON ||1: Invalid metadata format - "@" can be used only inside anydata, container or list entries.`,
			`LY_EVALID LYVE_SYNTAX_JSON ||1: Top-level JSON object member "@" must be namespace-qualified.`,
			`LY_EVALID LYVE_REFERENCE /@||1: Missing JSON data instance to be coupled with @ metadata.`}},
		{"meta-top", rej, false, `{"pj:top": "a", "@pj:top": {"pj:ann": "x"}}`, []string{"/pj:top=a @pj:ann=x"}},
		{"meta-top-unq", rej, false, `{"pj:top": "a", "@top": {"pj:ann": "x"}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON ||1: Top-level JSON object member "@top" must be namespace-qualified.`}},
		{"meta-repr-leaf", rej, false, `{"pj:c": {"s": "a", "@s": [1], "n": 1}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c/s||1: The attribute(s) of leaf "s" is expected to be represented as JSON @name/object, but input data contains @array/name.`}},
		{"meta-repr-ll", rej, false, `{"pj:c": {"ll": [1], "@ll": {"pj:ann": "x"}, "n": 1}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c/ll[.='1']||1: The attribute(s) of leaf-list "ll" is expected to be represented as JSON @name/array of objects/nulls, but input data contains @object/name.`}},
		{"meta-empty-name", rej, false, `{"pj:c": {"s": "a", "@s": {"": "x"}, "n": 1}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c/s||1: Metadata in JSON found with an empty name, followed by: ": "x"}, "`}},
		// an escaped name is a NUL-terminated copy in libyang: nothing follows it
		{"meta-empty-name-escaped", rej, false, `{"pj:c": {"s": "a", "@s": {"p` + string(rune(92)) + `u006a:": "x"}, "n": 1}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c/s||1: Metadata in JSON found with an empty name, followed by: `}},
		{"meta-at-in", rej, false, `{"pj:c": {"s": "a", "@s": {"@pj:ann": "x"}, "n": 1}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c/s||1: Invalid format of the Metadata identifier in JSON, unexpected '@' in "@pj:ann"`}},
		{"meta-list", rej, false, `{"pj:c": {"l": [{"k": "a", "@": {"pj:ann": "x"}}]}}`, []string{
			"/pj:c", "/pj:c/l[k='a'] @pj:ann=x", "/pj:c/l[k='a']/k=a"}},
		{"meta-list-sorted", rej, false, `{"pj:c": {"l": [{"k": "b"}, {"k": "a"}], "@l": [{"pj:ann": "x"}]}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c/l[k='a']||1: The attribute(s) of list "l" is expected to be represented as JSON @/object, but input data contains @array/.`}},
		{"meta-unknown-mod-skip-opaq", skp, false, `{"pj:c": {"q": 1, "@q": {"zz:ann": "x"}, "n": 1}}`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c/@q||1: Missing JSON data instance to be coupled with @q metadata.`}},
		// libyang crashes on a metadata member without a prefix before its node (D-0059)
		{"meta-before-unprefixed", rej, false, `{"pj:c": {"@s": {"ann": "x"}, "s": "a"}}`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c/@s||1: Missing YANG module of metadata "ann".`}},

		// opaque nodes
		{"opaq-shapes", opq, false, `{"pj:c": {"q": [1, "x"], "r": {"a": 1}, "t": [null], "u": [{"a": 1}, {"b": 2}], "w": true}}`, []string{
			"/pj:c",
			`/pj:c/q opaque="1" 0x2002`, `/pj:c/q opaque="x" 0x2011`,
			`/pj:c/r opaque="" 0x4000`, `/pj:c/r/a opaque="1" 0x2`,
			`/pj:c/t opaque="" 0x40`,
			`/pj:c/u opaque="" 0x1000`, `/pj:c/u/a opaque="1" 0x2`, `/pj:c/u opaque="" 0x1000`, `/pj:c/u/b opaque="2" 0x2`,
			`/pj:c/w opaque="true" 0x20`}},
		{"opaq-invalid-value", opq, false, `{"pj:c": {"n": "x", "ll": [1, 300], "e": [null], "s": {"a": 1}}}`, []string{
			"/pj:c", "/pj:c/e=", "/pj:c/ll[.='1']=1",
			`/pj:c/n opaque="x" 0x11`, `/pj:c/ll opaque="300" 0x2002`, `/pj:c/s opaque="" 0x4000`, `/pj:c/s/a opaque="1" 0x2`}},
		{"opaq-list-key-bad", opq, false, `{"pj:c": {"l": [{"v": "a"}, {"k": "b"}]}}`, []string{
			"/pj:c", "/pj:c/l[k='b']", "/pj:c/l[k='b']/k=b", `/pj:c/l opaque="" 0x5000`, `/pj:c/l/v opaque="a" 0x11`}},
		{"opaq-top", opq, false, `{"zz:x": 1, "pj:top": "a"}`, []string{"/pj:top=a", `/x opaque="1" 0x2`}},
		{"opaq-zero-name", opq, false, `{"pj:c": {"": 1, "s": "a"}}`, []string{
			`LY_EVALID LYVE_SYNTAX_JSON /pj:c||1: JSON object member name cannot be a zero-length string.`}},
		{"opaq-meta-list", opq, false, `{"pj:c": {"@q": {"pj:ann": "x"}, "q": [{"a": 1}]}}`, []string{
			`LY_EVALID LYVE_SYNTAX /pj:c/@q||1: Metadata container references a sibling list node q.`}},
		{"opaq-meta-on-opaq", opq, false, `{"pj:c": {"q": 1, "@q": {"pj:ann": "x"}}}`, []string{
			"/pj:c", `/pj:c/q opaque="1" 0x2 @pj:ann=x`}},
		{"opaq-meta-on-opaq-before", opq, false, `{"pj:c": {"@q": {"pj:ann": "x"}, "q": 1}}`, []string{
			"/pj:c", `/pj:c/q opaque="1" 0x2 @pj:ann=x`}},
		{"opaq-list-key-obj", opq, false, `{"pj:c": {"l2": [{"pk:x": 1, "a": "1", "b": {"z": 1}}]}}`, []string{
			"/pj:c", `/pj:c/l2 opaque="" 0x5000`, `/pj:c/l2/pk:x opaque="1" 0x2`, `/pj:c/l2/a opaque="1" 0x11`,
			`/pj:c/l2/b opaque="" 0x4000`, `/pj:c/l2/b/z opaque="1" 0x2`}},
		{"opaq-list-attr", opq, false, `{"pj:c": {"l": [{"@k": {"pj:ann": "x"}, "k": "a"}]}}`, []string{
			"/pj:c", "/pj:c/l[k='a']", "/pj:c/l[k='a']/k=a @pj:ann=x"}},
		{"opaq-list-twokeys", opq, false, `{"pj:c": {"l2": [{"pk:x": 1, "a": "1", "b": "2"}]}}`, []string{
			"/pj:c", "/pj:c/l2[a='1'][b='2']", "/pj:c/l2[a='1'][b='2']/a=1", "/pj:c/l2[a='1'][b='2']/b=2",
			`/pj:c/l2[a='1'][b='2']/pk:x opaque="1" 0x2`}},
		{"opaq-ll-obj", opq, false, `{"pj:c": {"ll": {"a": 1}}}`, []string{
			"/pj:c", `/pj:c/ll opaque="" 0x6000`, `/pj:c/ll/a opaque="1" 0x2`}},
		{"opaq-cont-val", opq, false, `{"pj:c": 1}`, []string{`/pj:c opaque="1" 0x2`}},
		{"inner-multi", rej, true, `{"pj:c": {"n": "x", "s": 1, "in": {"x": 2}}, "pj:top": 2}`, []string{
			`LY_EVALID LYVE_DATA /pj:c/n||1: Invalid non-number-encoded int32 value "x".`,
			`LY_EVALID LYVE_DATA /pj:c/s||1: Invalid non-string-encoded string value "1".`,
			`LY_EVALID LYVE_DATA /pj:c/in/x||1: Invalid non-string-encoded string value "2".`,
			`LY_EVALID LYVE_DATA |/pj:top|1: Invalid non-string-encoded string value "2".`}},
		{"key-missing-multi", rej, true, `{"pj:c": {"l": [{"v": "a"}], "s": 1}}`, []string{
			`LY_EVALID LYVE_DATA /pj:l||1: List instance is missing its key "k".`,
			`LY_EVALID LYVE_DATA /pj:c/s||1: Invalid non-string-encoded string value "1".`}},
		{"opaq-null-member", opq, false, `{"pj:c": {"q": [null, 1]}}`, []string{
			`LY_EVALID LYVE_SYNTAX /pj:c/q||1: Array "null" member with another member.`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, diags, err := parseJSONString(set, tc.in, tc.unknown, tc.multi)
			var got []string
			for _, d := range diags {
				got = append(got, diagLine(d))
			}
			if err == nil {
				got = append(got, dumpTree(tr)...)
			} else if tr != nil || errors.As(err, new(*ValidationError)) != (len(diags) > 0) {
				t.Fatalf("tree %v err %v", tr != nil, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// TestParseJSONUnsupported: anydata/anyxml instances are yang.ErrUnsupported (U-0043); RPCs in
// datastore data are rejected as libyang does.
func TestParseJSONUnsupported(t *testing.T) {
	set := pjSchema()
	if _, diags, err := parseJSONString(set, `{"pj:ad": {"x": 1}}`, Reject, true); !errors.Is(err, yang.ErrUnsupported) || len(diags) != 0 {
		t.Fatalf("anydata: %v %v", err, diags)
	}
	pj := set.Modules[1]
	pj.Top = append(pj.Top, &schema.Node{Kind: schema.RPC, Name: "r", Module: pj})
	_, diags, err := parseJSONString(set, `{"pj:r": {}, "pj:top": 1}`, Reject, true)
	want := []string{
		`LY_EVALID LYVE_DATA |/pj:r|1: Unexpected RPC element "r".`,
		`LY_EVALID LYVE_DATA |/pj:top|1: Invalid non-string-encoded string value "1".`,
	}
	var got []string
	for _, d := range diags {
		got = append(got, diagLine(d))
	}
	if err == nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("rpc: %v\n%s", err, strings.Join(got, "\n"))
	}
}

// TestParseJSONFlags: the parser's queues and flags — when nodes in post-order (again per
// metadata, as libyang), the default metadata as FlagDefault, LYD_PARSE_JSON_NULL,
// LYD_PARSE_JSON_STRING_DATATYPES, and the nesting limit as yang.ErrBudget.
func TestParseJSONFlags(t *testing.T) {
	set := pjSchema()
	pj := set.Modules[1]
	wd := &schema.Module{Name: "ietf-netconf-with-defaults", Implemented: true, Exts: []*schema.ExtInstance{
		{Def: set.Modules[0], Name: "annotation", Argument: "default", Type: &schema.Type{Base: schema.Bool}}}}
	set.Modules = append(set.Modules, wd)
	c := pj.Top[0]
	s := c.Children[0]
	s.Whens = []*schema.When{{Src: "true()"}}
	o := parseOpts{ParseOptions: ParseOptions{Validate: ValidateOptions{MultiError: true}}}
	var lc *lydCtx
	keep := func(l *lydCtx) { lc = l; noValidation(l) }
	in := `{"pj:c": {"s": "a", "@s": {"ietf-netconf-with-defaults:default": true, "pj:ann": "x"}, "n": null, "ll": ["7"]}}`
	_, diags, err := parseWith(context.Background(), strings.NewReader(in), set, o, parseJSON, keep)
	if err == nil || len(diags) != 2 {
		t.Fatalf("%v %v", err, diags)
	}
	sn := lc.tree.top.list[0].kids.list[0]
	// queued when created and again after each of its two metadata
	if !reflect.DeepEqual(lc.nodeWhen.items, []*Node{sn, sn, sn}) || sn.flags&FlagDefault == 0 ||
		len(sn.meta) != 1 || sn.meta[0].name != "ann" {
		t.Fatalf("when queue %d, flags %x, meta %v", lc.nodeWhen.len(), sn.flags, sn.meta)
	}
	o.jsonNull, o.jsonStringDatatypes = true, true
	if tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set, o, parseJSON, noValidation); err != nil {
		t.Fatalf("json_null/string_datatypes: %v %v", err, diags)
	} else if got := dumpTree(tr); !reflect.DeepEqual(got, []string{"/pj:c", "/pj:c/s=a @pj:ann=x", "/pj:c/ll[.='7']=7"}) {
		t.Fatalf("%q", got)
	}
	deep := `{"pj:c": {"q": ` + strings.Repeat("[", 5000) + strings.Repeat("]", 5000) + `}}`
	if _, diags, err := parseJSONString(set, deep, Skip, true); !errors.Is(err, yang.ErrBudget) ||
		diags[len(diags)-1].Msg != "Maximum number 5000 of nestings has been exceeded." {
		t.Fatalf("nesting: %v %v", err, diags)
	}
}

// FuzzParseJSON: parsing any input over the probe schema terminates without panicking, and every
// error is either a non-empty *ValidationError or wraps yang.ErrUnsupported/yang.ErrBudget.
func FuzzParseJSON(f *testing.F) {
	for _, s := range []string{
		`{"pj:c": {"s": "a", "@s": {"pj:ann": "x"}, "ll": [3, 1], "@ll": [null, {"pj:num": 1}], "l": [{"k": "a"}]}}`,
		`{"pj:c": {"@ll": [{"pj:ann": "x"}], "ll": [1], "q": [{"a": [null]}], "@": {"pj:ann": "y"}}, "zz:x": 1}`,
		`{"pj:c": {"l": {"k": 1}, "e": [null, 1], "@q": {"ann": 1}, "q": 1}}`, `{"@": {}}`, `[1]`, ``,
	} {
		f.Add([]byte(s), uint8(0), true)
	}
	set := pjSchema()
	f.Fuzz(func(t *testing.T, in []byte, unknown uint8, multi bool) {
		_, diags, err := parseJSONString(set, string(in), UnknownPolicy(unknown%3), multi)
		var ve *ValidationError
		switch {
		case err == nil:
		case errors.As(err, &ve):
			if len(ve.Diags) == 0 || len(diags) == 0 {
				t.Fatal("validation error without diagnostics")
			}
		case errors.Is(err, yang.ErrUnsupported), errors.Is(err, yang.ErrBudget):
		default:
			t.Fatalf("unexpected error %v", err)
		}
	})
}

// TestParseJSONLinear: attaching metadata is linear in the number of siblings. work counts the
// insertions and every sibling visit: those of siblings.all and siblings.indexOf (which any
// sibling scan goes through, the quadratic ones of the old parser included) and the parser's own
// slice walks; 4× the input must cost about 4× the work. Cases: a
// leaf-list metadata array after and before its instances, and the metadata of many opaque
// siblings after and before them.
func TestParseJSONLinear(t *testing.T) {
	set := pjSchema()
	c := set.Modules[1].Top[0]
	c.Children = append(c.Children, &schema.Node{Kind: schema.LeafList, Name: "ls", Module: c.Module, Parent: c,
		Type: &schema.Type{Base: schema.String}, Config: true})
	join := func(n int, f func(i int) string) string {
		s := make([]string, n)
		for i := range s {
			s[i] = f(i)
		}
		return strings.Join(s, ", ")
	}
	values := func(n int) string {
		return `"ls": [` + join(n, func(i int) string { return fmt.Sprintf(`"v%07d"`, i) }) + `]`
	}
	metas := func(n int) string { return `"@ls": [` + join(n, func(int) string { return `{"pj:ann": "a"}` }) + `]` }
	opaq := func(n int) string { return join(n, func(i int) string { return fmt.Sprintf(`"q%d": 1`, i) }) }
	attrs := func(n int) string {
		return join(n, func(i int) string { return fmt.Sprintf(`"@q%d": {"pj:ann": "a"}`, i) })
	}
	cases := []struct {
		name    string
		unknown UnknownPolicy
		in      func(n int) string
	}{
		{"leaf-list-after", Reject, func(n int) string { return values(n) + ", " + metas(n) }},
		{"leaf-list-singleton-members", Reject, func(n int) string {
			// one member per value, each followed by its metadata: every "@ls" reads the run
			return join(n, func(i int) string { return fmt.Sprintf(`"ls": ["v%07d"], "@ls": [null]`, i) })
		}},
		{"leaf-list-shuffled", Reject, func(n int) string {
			p := rand.New(rand.NewSource(1)).Perm(n)
			return `"ls": [` + join(n, func(i int) string { return fmt.Sprintf(`"v%07d"`, p[i]) }) + `], ` + metas(n)
		}},
		{"leaf-list-before", Reject, func(n int) string { return metas(n) + ", " + values(n) }},
		{"opaque-after", Opaque, func(n int) string { return opaq(n) + ", " + attrs(n) }},
		{"opaque-before", Opaque, func(n int) string { return attrs(n) + ", " + opaq(n) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := func(n int) int {
				var lc *lydCtx
				o := parseOpts{ParseOptions: ParseOptions{Unknown: tc.unknown, ParseOnly: true}}
				in := `{"pj:c": {` + tc.in(n) + `}}`
				if _, diags, err := parseWith(context.Background(), strings.NewReader(in), set, o, parseJSON,
					func(l *lydCtx) { lc = l; l.tree.work.visits, l.tree.work.shifts = true, true }); err != nil {
					t.Fatalf("%v %v", err, diags)
				}
				return int(lc.tree.work.Load())
			}
			if w1, w4 := work(1000), work(4000); w4 > 5*w1 {
				t.Fatalf("work %d for 1000 siblings, %d for 4000: not linear", w1, w4)
			}
		})
	}
}
