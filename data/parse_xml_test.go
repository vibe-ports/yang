// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/lyxml"
	"github.com/vibe-ports/yang/internal/schema"
)

func parseXMLString(set *schema.Set, in string, unknown UnknownPolicy, multi bool) (*Tree, []yang.Diagnostic, error) {
	o := parseOpts{ParseOptions: ParseOptions{Unknown: unknown, Validate: ValidateOptions{MultiError: multi}}}
	return parseWith(context.Background(), strings.NewReader(in), set, o, parseXML, noValidation)
}

// TestParseXML: lyd_parse_xml against libyang v5.8.6 (the expectations are oracle probes over the
// schema of TestParseJSON; multi is LYD_VALIDATE_MULTI_ERROR). Validation (D8/D10) is not wired
// yet, so only parse-time diagnostics and no implicit nodes appear.
func TestParseXML(t *testing.T) {
	set := pjSchema()
	const (
		rej = Reject
		skp = Skip
		opq = Opaque
	)
	pj := ` xmlns="urn:pj"`
	cases := []struct {
		name    string
		unknown UnknownPolicy
		multi   bool
		in      string
		want    []string // diagnostics, then the tree dump when there is no error
	}{
		{"empty", rej, true, ``, nil},
		{"valid", rej, false, `<c` + pj + `><s>a</s><ll>3</ll><ll>1</ll><ext xmlns="urn:pk">e</ext></c><top xmlns="urn:pk">b</top><top` + pj + `>a</top>`, []string{
			"/pj:c", "/pj:c/s=a", "/pj:c/ll[.='1']=1", "/pj:c/ll[.='3']=3", "/pj:c/pk:ext=e", "/pj:top=a", "/pk:top=b"}},
		{"no-ns", rej, true, `<c><s>a</s></c><top` + pj + `>a</top>`, []string{
			"LY_EVALID LYVE_REFERENCE ||1: Missing XML namespace."}},
		{"unknown-prefix", rej, true, `<x:c><s>a</s></x:c><top` + pj + `>a</top>`, []string{
			`LY_EVALID LYVE_REFERENCE ||1: Unknown XML prefix "x".`}},
		{"unknown-ns", rej, true, `<c xmlns="urn:zz"/><top` + pj + `>a</top>`, []string{
			`LY_EVALID LYVE_REFERENCE ||1: No module with namespace "urn:zz" in the context.`}},
		{"unknown-ns-skip", skp, false, `<c xmlns="urn:zz"><a/></c><top` + pj + `>a</top>`, []string{"/pj:top=a"}},
		{"unknown-child", rej, true, `<c` + pj + `><q>1</q><s>a</s><pk:q xmlns:pk="urn:pk">1</pk:q></c>`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c||1: Node "q" not found as a child of "c" node.`,
			`LY_EVALID LYVE_REFERENCE /pj:c||1: Node "q" not found as a child of "c" node.`}},
		{"text-in-inner", rej, true, `<c` + pj + `>txt<s>a</s></c>`, []string{
			`LY_EVALID LYVE_SYNTAX ||1: Text value "txt" inside an inner node "c" found.`}},
		{"child-in-term", rej, true, `<c` + pj + `><s>a<x/></s></c>`, []string{
			`LY_EVALID LYVE_SYNTAX /pj:c/s||1: Child element "x" inside a terminal node "s" found.`}},
		// whitespace-only text is the value of a term, not of an inner node
		{"ws-string", rej, false, "<c" + pj + "><s>   </s><in>\n  <x>\n</x>\n</in></c>", []string{
			"/pj:c", "/pj:c/s=   ", "/pj:c/in", "/pj:c/in/x=\n"}},
		{"keys-order-reject", rej, true, `<c` + pj + `><l2><b>2</b><a>1</a></l2><s>1</s><n>x</n></c>`, []string{
			`LY_EVALID LYVE_DATA /pj:l2[a='1'][b='2']/a||1: Invalid position of the key "a" in a list.`,
			`LY_EVALID LYVE_DATA /pj:c/n||1: Invalid type int32 value "x".`}},
		{"keys-order-skip", skp, false, `<c` + pj + `><l2><b>2</b><a>1</a></l2></c>`, []string{
			`LY_SUCCESS LYVE_SUCCESS ||0: Invalid position of the key "a" in a list.`,
			"/pj:c", "/pj:c/l2[a='1'][b='2']", "/pj:c/l2[a='1'][b='2']/a=1", "/pj:c/l2[a='1'][b='2']/b=2"}},
		{"key-after-nonkey", rej, false, `<c` + pj + `><l><v>x</v><k>a</k></l></c>`, []string{
			"/pj:c", "/pj:c/l[k='a']", "/pj:c/l[k='a']/k=a", "/pj:c/l[k='a']/v=x"}},
		{"missing-key", rej, true, `<c` + pj + `><l><v>x</v></l><n>x</n></c>`, []string{
			`LY_EVALID LYVE_DATA /pj:l||1: List instance is missing its key "k".`,
			`LY_EVALID LYVE_DATA /pj:c/n||1: Invalid type int32 value "x".`}},
		{"types-multi", rej, true, `<c` + pj + `><n>x</n><ll>300</ll><ll>2</ll><e>a</e></c><top` + pj + `/>`, []string{
			`LY_EVALID LYVE_DATA /pj:c/n||1: Invalid type int32 value "x".`,
			`LY_EVALID LYVE_DATA /pj:c/ll||1: Value "300" is out of type uint8 min/max bounds.`,
			`LY_EVALID LYVE_DATA /pj:c/e||1: Invalid empty value size 8 b.`}},
		{"types-single", rej, false, `<c` + pj + `><n>x</n><ll>300</ll></c>`, []string{
			`LY_EVALID LYVE_DATA /pj:c/n||1: Invalid type int32 value "x".`}},
		{"lines", rej, true, "<c" + pj + ">\n <s>a</s>\n <n>\nx</n>\n</c>", []string{
			`LY_EVALID LYVE_DATA /pj:c/n||4: Invalid type int32 value "x".`}},

		// metadata
		{"meta", rej, false, `<c` + pj + ` xmlns:pj="urn:pj"><s pj:ann="x" pj:num="7">a</s><ll pj:ann="o">1</ll></c>`, []string{
			"/pj:c", "/pj:c/s=a @pj:ann=x @pj:num=7", "/pj:c/ll[.='1']=1 @pj:ann=o"}},
		{"meta-unprefixed", rej, true, `<c` + pj + `><s ann="x" pj:ann="y" xmlns:pj="urn:pj">a</s><n>y</n></c>`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c/s||1: Missing mandatory prefix for XML metadata "ann".`,
			`LY_EVALID LYVE_DATA /pj:c/n||1: Invalid type int32 value "y".`}},
		{"meta-unprefixed-skip", skp, false, `<c` + pj + `><s ann="x">a</s></c>`, []string{"/pj:c", "/pj:c/s=a"}},
		{"meta-unknown-prefix", rej, true, `<c` + pj + `><s q:ann="x">a</s><n>y</n></c>`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c/s||1: Unknown XML prefix "q" at attribute "ann".`}},
		{"meta-unknown-ns", rej, true, `<c` + pj + `><s xmlns:z="urn:zz" z:ann="x">a</s><n>y</n></c>`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c/s||1: Unknown (or not implemented) YANG module with namespace "urn:zz" for metadata "z:ann".`}},
		{"meta-unknown-ns-skip", skp, false, `<c` + pj + `><s xmlns:z="urn:zz" z:ann="x">a</s></c>`, []string{"/pj:c", "/pj:c/s=a"}},
		{"meta-unknown-ann", rej, true, `<c` + pj + ` xmlns:pj="urn:pj"><s pj:nope="x">a</s><n>y</n></c>`, []string{
			`LY_EVALID LYVE_REFERENCE |/pj:c/s/@pj:nope|1: Annotation definition for attribute "pj:nope" not found.`}},
		// the parse goes on from the attribute value, which ends the container early (libyang)
		{"meta-bad-value", rej, true, `<c` + pj + ` xmlns:pj="urn:pj"><s pj:num="x">a</s><n>y</n></c>`, []string{
			`LY_EVALID LYVE_DATA /pj:c/s/@pj:num||1: Invalid type uint8 value "x".`,
			`LY_EVALID LYVE_REFERENCE ||1: Node "n" not found in the "pj" module.`}},
		{"meta-bad-value-inner", rej, true, `<c` + pj + ` xmlns:pj="urn:pj" pj:num="x"><s>a</s></c><top` + pj + `>a</top>`, []string{
			`LY_EVALID LYVE_DATA |/pj:c/@pj:num|1: Invalid type uint8 value "x".`,
			`LY_EVALID LYVE_SYNTAX ||1: Text value "x" inside an inner node "c" found.`}},

		// opaque nodes
		{"opaq-shapes", opq, false, `<c` + pj + `><q>1</q><w>tru</w><q>2</q><r><a>1</a></r><r><b/></r><z>99999999999</z><y> 5</y><x>-</x><v/></c>`, []string{
			"/pj:c", `/pj:c/q opaque="1" 0x2002`, `/pj:c/q opaque="2" 0x2002`, `/pj:c/w opaque="tru" 0x20`,
			`/pj:c/r opaque="" 0x1041`, `/pj:c/r/a opaque="1" 0x2`, `/pj:c/r opaque="" 0x1041`, `/pj:c/r/b opaque="" 0x41`,
			`/pj:c/z opaque="99999999999" 0x12`, `/pj:c/y opaque=" 5" 0x2`, `/pj:c/x opaque="-" 0x1`, `/pj:c/v opaque="" 0x41`}},
		{"opaq-invalid", opq, false, `<c` + pj + `><n>x</n><ll>300</ll><ll>1</ll><in>t</in><l><v>a</v></l><l><k>b</k></l></c>`, []string{
			"/pj:c", "/pj:c/ll[.='1']=1", "/pj:c/l[k='b']", "/pj:c/l[k='b']/k=b", `/pj:c/n opaque="x" 0x1`,
			`/pj:c/ll opaque="300" 0x2`, `/pj:c/in opaque="t" 0x20`, `/pj:c/l opaque="" 0x41`, `/pj:c/l/v opaque="a" 0x1`}},
		{"opaq-mixed", opq, true, `<c` + pj + `><q>t<a/></q><n>y</n></c>`, []string{
			`LY_EVALID LYVE_SYNTAX_XML /pj:c/q||1: Mixed XML content node "q" found, not supported.`}},
		{"opaq-attrs", opq, false, `<c` + pj + `><q xmlns:pj="urn:pj" pj:ann="x" xml:lang="en" foo="1">1</q></c>`, []string{
			"/pj:c", `/pj:c/q opaque="1" 0x2 @pj:ann=x @:xml:lang=en @:foo=1`}},
		{"opaq-attrs-badprefix", opq, true, `<c` + pj + `><q z:ann="x">1</q><n>y</n></c>`, []string{
			`LY_EVALID LYVE_REFERENCE /pj:c||1: Unknown XML prefix "z" at attribute "ann".`}},
		{"opaq-ns", opq, false, `<c` + pj + `><q>1</q><q xmlns="urn:zz">2</q><q>3</q></c><x xmlns="urn:zz"/>`, []string{
			"/pj:c", `/pj:c/q opaque="1" 0x2002`, `/pj:c/q opaque="3" 0x2002`, `/pj:c/q opaque="2" 0x2`, `/x opaque="" 0x41`}},
		// keys are matched by name only
		{"opaq-list-key-other-ns", opq, false, `<c` + pj + `><l><k xmlns="urn:zz">a</k></l></c>`, []string{
			`LY_EVALID LYVE_DATA /pj:l||1: List instance is missing its key "k".`}},
		{"opaq-checklist", opq, false, `<c` + pj + `><l2><v><d/></v><a f="1">1</a><b>2</b></l2></c>`, []string{
			`LY_EVALID LYVE_SYNTAX /pj:l2/v||1: Child element "d" inside a terminal node "v" found.`}},
		{"opaq-checklist2", opq, false, `<c` + pj + `><l2><x><d><e/></d></x><a f="1">1</a><b>2</b></l2><l><k>a</k><k>b</k></l></c>`, []string{
			"/pj:c", "/pj:c/l[k='a'][k='b']", "/pj:c/l[k='a'][k='b']/k=a", "/pj:c/l[k='a'][k='b']/k=b",
			"/pj:c/l2[a='1'][b='2']", "/pj:c/l2[a='1'][b='2']/a=1", "/pj:c/l2[a='1'][b='2']/b=2",
			`/pj:c/l2[a='1'][b='2']/x opaque="" 0x41`, `/pj:c/l2[a='1'][b='2']/x/d opaque="" 0x41`,
			`/pj:c/l2[a='1'][b='2']/x/d/e opaque="" 0x41`}},
		{"opaq-cont-attr", opq, false, `<c` + pj + ` xmlns:pj="urn:pj"><in pj:ann="x"> </in><in pj:ann="y">t</in></c>`, []string{
			"/pj:c", "/pj:c/in @pj:ann=x", `/pj:c/in opaque="t" 0x20 @pj:ann=y`}},
		{"opaq-prefixed-value", opq, false, `<c` + pj + ` xmlns:p="urn:pj"><q>p:x</q></c>`, []string{
			"/pj:c", `/pj:c/q opaque="p:x" 0x1`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, diags, err := parseXMLString(set, tc.in, tc.unknown, tc.multi)
			var got []string
			for _, d := range diags {
				got = append(got, diagLine(d))
			}
			if err == nil {
				got = append(got, dumpTree(tr)...)
			} else if tr != nil || !errors.As(err, new(*ValidationError)) {
				t.Fatalf("tree %v err %v", tr != nil, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

// TestParseXMLUnsupported: anydata/anyxml instances are yang.ErrUnsupported (U-0043); an RPC in
// datastore data is rejected as libyang does; a lexer failure libyang does not log is a
// *ValidationError without diagnostics wrapping the lexer's error.
func TestParseXMLUnsupported(t *testing.T) {
	set := pjSchema()
	if _, diags, err := parseXMLString(set, `<ad xmlns="urn:pj"><x/></ad>`, Reject, true); !errors.Is(err, yang.ErrUnsupported) || len(diags) != 0 {
		t.Fatalf("anydata: %v %v", err, diags)
	}
	pj := set.Modules[1]
	pj.Top = append(pj.Top, &schema.Node{Kind: schema.RPC, Name: "r", Module: pj})
	_, diags, err := parseXMLString(set, `<r xmlns="urn:pj"/><top xmlns="urn:pj">a</top><n xmlns="urn:pj"/>`, Reject, true)
	var got []string
	for _, d := range diags {
		got = append(got, diagLine(d))
	}
	want := []string{
		`LY_EVALID LYVE_DATA |/pj:r|1: Unexpected RPC element "r".`,
		`LY_EVALID LYVE_REFERENCE ||1: Node "n" not found in the "pj" module.`,
	}
	if err == nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("rpc: %v\n%s", err, strings.Join(got, "\n"))
	}
	// an invalid UTF-8 sequence where an attribute may start: libyang fails without a message
	_, diags, err = parseXMLString(set, "<c xmlns=\"urn:pj\" \xff/>", Reject, true)
	var ve *ValidationError
	var xe *lyxml.Error
	if !errors.As(err, &ve) || len(ve.Diags) != 0 || !errors.As(err, &xe) || len(diags) != 0 ||
		err.Error() != xe.Error() {
		t.Fatalf("silent lexer error: %v %v", err, diags)
	}
}

// TestParseXMLFlags: metadata attach after the flags (the default metadata becomes FlagDefault),
// the when queue in post-order, and the nesting limit as yang.ErrBudget.
func TestParseXMLFlags(t *testing.T) {
	set := pjSchema()
	wd := &schema.Module{Name: "ietf-netconf-with-defaults", Namespace: "urn:ietf:params:xml:ns:netconf:default:1.0", Implemented: true,
		Exts: []*schema.ExtInstance{{Def: set.Modules[0], Name: "annotation", Argument: "default", Type: &schema.Type{Base: schema.Bool}}}}
	set.Modules = append(set.Modules, wd)
	c := set.Modules[1].Top[0]
	s := c.Children[0]
	s.Whens, c.Whens = []*schema.When{{Src: "true()"}}, []*schema.When{{Src: "true()"}}
	o := parseOpts{ParseOptions: ParseOptions{Validate: ValidateOptions{MultiError: true}}}
	var lc *lydCtx
	in := `<c xmlns="urn:pj" xmlns:pj="urn:pj" xmlns:wd="urn:ietf:params:xml:ns:netconf:default:1.0">` +
		`<s wd:default="true" pj:ann="x">a</s></c>`
	tr, diags, err := parseWith(context.Background(), strings.NewReader(in), set, o, parseXML, func(l *lydCtx) { lc = l; noValidation(l) })
	if err != nil {
		t.Fatalf("%v %v", err, diags)
	}
	cn := tr.top.list[0]
	sn := cn.kids.list[0]
	if !reflect.DeepEqual(lc.nodeWhen.items, []*Node{sn, cn}) || sn.flags&FlagDefault == 0 || len(sn.meta) != 1 ||
		sn.meta[0].name != "ann" {
		t.Fatalf("when queue %d, flags %x, meta %v", lc.nodeWhen.len(), sn.flags, sn.meta)
	}
	deep := strings.Repeat("<a>", 501)
	if _, diags, err := parseXMLString(set, `<c xmlns="urn:pj"><q>`+deep+`</q></c>`, Skip, true); !errors.Is(err, yang.ErrBudget) ||
		diags[len(diags)-1].Msg != "The maximum number of open elements has been exceeded." {
		t.Fatalf("nesting: %v %v", err, diags)
	}
}

// FuzzParseXML: parsing any input over the probe schema terminates without panicking, and every
// error is a *ValidationError (non-empty unless it wraps a lexer error libyang does not log) or
// wraps yang.ErrUnsupported/yang.ErrBudget.
func FuzzParseXML(f *testing.F) {
	for _, s := range []string{
		`<c xmlns="urn:pj" xmlns:pj="urn:pj"><s pj:ann="x">a</s><ll>3</ll><ll>1</ll><l><k>a</k></l><l2><b>2</b><a>1</a></l2></c>`,
		`<c xmlns="urn:pj"><q>1</q><q>2</q><r><a/></r><n>x</n><in>t</in><l><v/></l></c><x xmlns="urn:zz" foo="1"/>`,
		`<c xmlns="urn:pj" xmlns:pj="urn:pj" pj:num="x"><s ann="1" q:a="2">a<b/></s></c>`, ``, `<c>`,
	} {
		f.Add([]byte(s), uint8(0), true)
	}
	set := pjSchema()
	f.Fuzz(func(t *testing.T, in []byte, unknown uint8, multi bool) {
		_, diags, err := parseXMLString(set, string(in), UnknownPolicy(unknown%3), multi)
		var ve *ValidationError
		var xe *lyxml.Error
		switch {
		case err == nil:
		case errors.As(err, &ve):
			if (len(ve.Diags) == 0 || len(diags) == 0) && !errors.As(err, &xe) {
				t.Fatal("validation error without diagnostics")
			}
		case errors.Is(err, yang.ErrUnsupported), errors.Is(err, yang.ErrBudget):
		default:
			t.Fatalf("unexpected error %v", err)
		}
	})
}

// TestXMLNamespaceSync: the parser's namespace index agrees with the lexer's stack after every
// step and after every restored backup of the opaque checks (lists, containers, terms).
func TestXMLNamespaceSync(t *testing.T) {
	set := pjSchema()
	c := set.Modules[1].Top[0]
	in := `<c xmlns="urn:pj" xmlns:a="urn:a1"><l xmlns:a="urn:a2"><k xmlns:b="urn:b">x</k><v xmlns="urn:v"/></l>` +
		`<in xmlns:b="urn:b2"><x>t</x></in><s xmlns:a="urn:a3">a:z</s></c><top xmlns="urn:pj" xmlns:b="urn:b3"/>`
	x, err := lyxml.New([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	lc := &lydCtx{ctx: context.Background(), tree: newTree(set), log: &logger{set: set}}
	p := &xmlParser{lc: lc, x: x, opaq: true, nsMap: map[string][]string{}}
	p.syncNS()
	for x.Status != lyxml.End {
		if x.Status == lyxml.Element {
			for _, sn := range c.Children {
				if sn.Name == x.Name {
					b := x.Backup() // the check runs from after the element name
					if err := p.next(); err != nil {
						t.Fatal(err)
					}
					if _, err := p.checkOpaq(sn); err != nil {
						t.Fatal(err)
					}
					x.Restore(b)
					p.syncNS()
				}
			}
		}
		ns := x.NS()
		if len(p.nsPfx) != len(ns) {
			t.Fatalf("stack %d, index %d", len(ns), len(p.nsPfx))
		}
		for _, d := range ns {
			want, _ := x.GetNS(d.Prefix)
			if got, _ := p.getNS(d.Prefix); got != want.URI {
				t.Fatalf("%q: %q, lexer %q", d.Prefix, got, want.URI)
			}
		}
		if err := p.next(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestParseXMLLinear: the namespace prefixes of values and elements cost no more than the value
// and the declarations themselves. work counts the parser's namespace steps, every namespace
// entry a prefix snapshot copies, every sibling visit and the insertions; 4× the input must cost
// about 4× the work. Cases: many declarations on the container and many
// values in it (terms, prefixed terms, opaque nodes, metadata), and a declaration on every value.
func TestParseXMLLinear(t *testing.T) {
	set := pjSchema()
	c := set.Modules[1].Top[0]
	c.Children = append(c.Children, &schema.Node{Kind: schema.LeafList, Name: "ls", Module: c.Module, Parent: c,
		Type: &schema.Type{Base: schema.String}, Config: true})
	rep := func(n int, f func(i int) string) string {
		var b strings.Builder
		for i := range n {
			b.WriteString(f(i))
		}
		return b.String()
	}
	decls := func(n int) string {
		return rep(n, func(i int) string { return fmt.Sprintf(` xmlns:p%d="urn:x%d"`, i, i) })
	}
	cases := []struct {
		name    string
		unknown UnknownPolicy
		in      func(n int) string
	}{
		{"terms", Reject, func(n int) string {
			return `<c xmlns="urn:pj"` + decls(n) + `>` + rep(n, func(i int) string { return fmt.Sprintf(`<ls>v%07d</ls>`, i) }) + `</c>`
		}},
		{"prefixed-terms", Reject, func(n int) string {
			return `<c xmlns="urn:pj"` + decls(n) + `>` + rep(n, func(i int) string { return fmt.Sprintf(`<ls>p%d:v</ls>`, i) }) + `</c>`
		}},
		{"opaque", Opaque, func(n int) string {
			return `<c xmlns="urn:pj"` + decls(n) + `>` + rep(n, func(i int) string { return fmt.Sprintf(`<q>p%d:v</q>`, i) }) + `</c>`
		}},
		{"metadata", Reject, func(n int) string {
			return `<c xmlns="urn:pj" xmlns:pj="urn:pj"` + decls(n) + `>` +
				rep(n, func(i int) string { return fmt.Sprintf(`<ls pj:ann="p%d:a">v%07d</ls>`, i, i) }) + `</c>`
		}},
		{"declaration-per-value", Reject, func(n int) string {
			return `<c xmlns="urn:pj"` + decls(n) + `>` +
				rep(n, func(i int) string { return fmt.Sprintf(`<ls xmlns:y="urn:y%d">y:v%07d</ls>`, i, i) }) + `</c>`
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work := func(n int) int {
				var lc *lydCtx
				o := parseOpts{ParseOptions: ParseOptions{Unknown: tc.unknown, ParseOnly: true}}
				if _, diags, err := parseWith(context.Background(), strings.NewReader(tc.in(n)), set, o, parseXML,
					func(l *lydCtx) { lc = l }); err != nil {
					t.Fatalf("%v %v", err, diags)
				}
				return int(lc.tree.work.Load())
			}
			if w1, w4 := work(1000), work(4000); w4 > 5*w1 {
				t.Fatalf("work %d for 1000 values, %d for 4000: not linear", w1, w4)
			}
		})
	}
}
