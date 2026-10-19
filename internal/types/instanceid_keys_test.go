// SPDX-License-Identifier: BSD-3-Clause

package types

import (
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestInstanceIDKeys replays conformance/corpus/ut-ident-instid/instanceid-keys-02..05 (libyang
// tests/utests/types/instanceid_keys.c) and the prefix handling around them.
func TestInstanceIDKeys(t *testing.T) {
	set := &schema.Set{}
	defs := &schema.Module{Name: "defs", Namespace: "urn:tests:defs", Prefix: "pref", Implemented: true}
	other := &schema.Module{Name: "other", Namespace: "urn:tests:other", Prefix: "o", Implemented: true}
	set.Modules = append(set.Modules, defs, other)
	yang := &schema.Module{Name: "yang", Namespace: "urn:ietf:params:xml:ns:yang:1", Prefix: "yang"}
	ty := &schema.Type{Base: schema.String, Typedef: "instance-identifier-keys", TypedefModule: yang}
	xml := func(ns map[string]string) PrefixCtx { return XMLNamespaces{Set: set, NS: ns} }
	own := map[string]string{"": defs.Namespace, "px": defs.Namespace, "o": other.Namespace}

	runKeys := func(t *testing.T, lex string, f Format, pc PrefixCtx, canon, err string) {
		t.Helper()
		v, d := Store(ty, lex, f, HintData, pc, nil)
		if err != "" {
			if d == nil || d.Msg != err || d.Code != CodeData {
				t.Fatalf("Store(%q) = %q, %v, want error %q", lex, v.Canonical(), d, err)
			}
			return
		}
		if d != nil {
			t.Fatalf("Store(%q) error %q", lex, d.Msg)
		}
		if v.Canonical() != canon {
			t.Fatalf("Store(%q) = %q, want %q", lex, v.Canonical(), canon)
		}
	}
	for _, c := range []struct {
		src, lex string
		f        Format
		pc       PrefixCtx
		canon    string
		err      string
	}{
		{"keys-01 empty", "", FormatXML, xml(own), "", `Unexpected XPath expression end.`},
		{"keys-02 prefix rewrite", "[px:key='val']", FormatXML, xml(own), "[defs:key='val']", ""},
		{"keys-02 two modules", "[px:a='1'][o:b='2']", FormatXML, xml(own), "[defs:a='1'][other:b='2']", ""},
		{"keys-02 inherit", "[px:a='1'][px:b='2']", FormatXML, xml(own), "[defs:a='1'][b='2']", ""},
		{"keys-02 unprefixed", "[key='val']", FormatXML, xml(own), "[key='val']", ""},
		{"keys-03 first char", "black", FormatXML, xml(own), "", "Invalid first character 'b', list key predicates expected."},
		{"keys-04 not xpath", "[this is not a valid xpath]", FormatXML, xml(own),
			"", `Invalid character 0x69 ('i'), perhaps "this" is supposed to be a function call.`},
		{"keys-05 no prefix", "[px:key='val']", FormatXML, xml(map[string]string{"": defs.Namespace}), "", `Failed to resolve prefix "px".`},
		{"canonical as is", "[defs:key='val']", FormatCanon, nil, "[defs:key='val']", ""},
		{"json as is", "[defs:key='val']", FormatJSON, ModuleNames{Set: set}, "[defs:key='val']", ""},
		{"literal prefix", "[k='px:v']", FormatXML, xml(own), "[k='defs:v']", ""},
		{"trailing", "[a='1']]", FormatXML, xml(own), "", `Unparsed characters "]" left at the end of predicate.`},
	} {
		t.Run(c.src, func(t *testing.T) { runKeys(t, c.lex, c.f, c.pc, c.canon, c.err) })
	}

	// the prefixes of the value are kept: printing in other formats resolves them again
	v, d := Store(ty, "[px:a='1'][o:b='2']", FormatXML, HintData, xml(own), nil)
	if d != nil {
		t.Fatal(d)
	}
	pc := &PrintCtx{Local: defs}
	got, err := Print(v, FormatXML, pc)
	if err != nil || got != "[pref:a='1'][o:b='2']" {
		t.Fatalf("Print XML = %q, %v", got, err)
	}
	if got, _ := Print(v, FormatCanon, nil); got != "[defs:a='1'][other:b='2']" {
		t.Fatalf("Print canonical = %q", got)
	}

	// libyang prints through xpath10_print_subexpr_r: every bracket starts from the context module
	// of the enclosing expression, so the second predicate does not inherit the first one's module
	// (the canonical form is flat and does).
	v, d = Store(ty, "[px:k1='a'][k2='b']", FormatXML, HintData, xml(own), nil)
	if d != nil {
		t.Fatal(d)
	}
	if v.Canonical() != "[defs:k1='a'][k2='b']" {
		t.Fatalf("canonical = %q", v.Canonical())
	}
	if got, err := Print(v, FormatXML, &PrintCtx{Local: defs}); err != nil || got != "[pref:k1='a'][k2='b']" {
		t.Fatalf("Print XML = %q, %v", got, err)
	}
}

func TestPrefixDataScanError(t *testing.T) {
	_, d := prefixDataNew(&storeArgs{lex: "\x01a:b", f: FormatXML})
	if d == nil || d.RC() != "LY_EINVAL" || d.Code != CodeOther || !d.NoItem {
		t.Fatalf("prefix scan error = %+v", d)
	}
}
