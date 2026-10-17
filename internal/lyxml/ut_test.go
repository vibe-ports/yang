// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 tests/utests/basic/test_xml.c (BSD-3-Clause, © CESNET).

package lyxml

import (
	"errors"
	"testing"
)

/*
Not ported (no Go equivalent):
  - test_xml.c:38-42,46-49,53-56,60-63,67-70,74-77,81-84,88-105,109-136,
    140-156,160-179,183-198,202-216,220-226,230-244,248-259,263-309,321-366,
    378-432,437-531,545-587,599-619,629-675: ly_in_new_memory return checks,
    ly_in_free and lyxml_ctx_free; Go accepts []byte directly and is garbage collected.
  - test_xml.c:352,388,400,412,445,458,470: xmlctx->dynamic; Go values are strings,
    not caller/allocated C buffers.
  - test_xml.c:446,471 and 549,550,554,603: free/strdup ownership; Go strings have no
    manual allocation ownership.
  - test_xml.c:556: xmlctx->ns.size; Go slice capacity is an implementation detail.
  - test_xml.c:394,406,418,426,452,464,477,485,493,501,509,517,525 and 433,532:
    ly_log_location/ly_log_location_revert; the Go lexer reports input lines in *Error.

Input coverage: test_element: 19 inputs, all ported; test_attribute: 5 inputs, all ported;
test_text: 15 inputs, all ported; test_ns: 1 input, all ported; test_ns2: 1 input, all
ported; test_simple_xml: 1 input, all ported.
*/

type utXMLDiag struct {
	msg  string
	line uint64
}

type utXMLNS struct {
	prefix string
	uri    string
}

type utXMLState struct {
	status Status

	checkQName bool
	prefix     string
	name       string

	checkValue bool
	value      string
	checkWS    bool
	ws         bool

	checkDepth bool
	depth      int
	checkNS    bool
	nsCount    int
	nsEntries  []utXMLNS

	checkRest bool
	rest      string
}

type utXMLStep struct {
	state utXMLState
	diag  *utXMLDiag
}

type utXMLCase struct {
	input   string
	initial utXMLState
	newDiag *utXMLDiag
	steps   []utXMLStep
}

func utXMLCheckDiag(t *testing.T, err error, want *utXMLDiag) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var got *Error
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if got.Msg != want.msg || got.Line != want.line {
		t.Fatalf("error = (%q, %d), want (%q, %d)", got.Msg, got.Line, want.msg, want.line)
	}
}

func utXMLCheckState(t *testing.T, c *Ctx, want utXMLState) {
	t.Helper()
	if c.Status != want.status {
		t.Fatalf("status = %v, want %v", c.Status, want.status)
	}
	if want.checkQName && (c.Prefix != want.prefix || c.Name != want.name) {
		t.Errorf("qname = (%q, %q), want (%q, %q)", c.Prefix, c.Name, want.prefix, want.name)
	}
	if want.checkValue && c.Value != want.value {
		t.Errorf("value = %q, want %q", c.Value, want.value)
	}
	if want.checkWS && c.WSOnly != want.ws {
		t.Errorf("WSOnly = %v, want %v", c.WSOnly, want.ws)
	}
	if want.checkDepth && c.Depth() != want.depth {
		t.Errorf("depth = %d, want %d", c.Depth(), want.depth)
	}
	if want.checkNS {
		if len(c.NS()) != want.nsCount {
			t.Errorf("namespace count = %d, want %d", len(c.NS()), want.nsCount)
		}
		for i, ns := range want.nsEntries {
			if i >= len(c.NS()) {
				break
			}
			if c.NS()[i].Prefix != ns.prefix || c.NS()[i].URI != ns.uri {
				t.Errorf("namespace %d = (%q, %q), want (%q, %q)", i,
					c.NS()[i].Prefix, c.NS()[i].URI, ns.prefix, ns.uri)
			}
		}
	}
	if want.checkRest {
		if got := string(c.in[c.pos:]); got != want.rest {
			t.Errorf("remaining input = %q, want %q", got, want.rest)
		}
	}
}

func utRunXMLSteps(t *testing.T, c *Ctx, steps []utXMLStep) {
	t.Helper()
	for i, step := range steps {
		err := c.Next()
		if step.diag != nil {
			utXMLCheckDiag(t, err, step.diag)
			return
		}
		if err != nil {
			t.Fatalf("Next() step %d error = %v", i+1, err)
		}
		utXMLCheckState(t, c, step.state)
	}
}

func utRunXMLCases(t *testing.T, cases []utXMLCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			c, err := New([]byte(tc.input))
			if tc.newDiag != nil {
				utXMLCheckDiag(t, err, tc.newDiag)
				return
			}
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			utXMLCheckState(t, c, tc.initial)
			utRunXMLSteps(t, c, tc.steps)
		})
	}
}

func utResetXML(c *Ctx, input string, status Status) {
	c.in = []byte(input)
	c.pos = 0
	c.line = 1
	c.Status = status
}

func TestUTElement(t *testing.T) {
	utRunXMLCases(t, []utXMLCase{
		{input: "", initial: utXMLState{status: End}},                                                                                                     // test_xml.c:37
		{input: "</element>", newDiag: &utXMLDiag{`Stray closing element tag ("element").`, 1}},                                                           // test_xml.c:45
		{input: "no data present", newDiag: &utXMLDiag{`Invalid character sequence "no data present", expected element tag start ('<').`, 1}},             // test_xml.c:52
		{input: `<!DOCTYPE greeting SYSTEM "hello.dtd"><greeting/>`, newDiag: &utXMLDiag{"Document Type Declaration not supported.", 1}},                  // test_xml.c:59
		{input: "<!NONSENSE/>", newDiag: &utXMLDiag{`Unknown XML section "<!NONSENSE/>".`, 1}},                                                            // test_xml.c:66
		{input: `<element xmlns="urn1" xmlns="urn2"/>`, newDiag: &utXMLDiag{`Duplicate default XML namespaces "urn1" and "urn2".`, 1}},                    // test_xml.c:73
		{input: `<element xmlns:a="urn1" xmlns:a="urn2"/>`, newDiag: &utXMLDiag{`Duplicate XML NS prefix "a" used for namespaces "urn1" and "urn2".`, 1}}, // test_xml.c:80
		{
			input:   "  <  element/>",
			initial: utXMLState{status: Element, checkQName: true, name: "element", checkDepth: true, depth: 1},
			steps: []utXMLStep{
				{state: utXMLState{status: ElemContent, checkValue: true, value: ""}},
				{state: utXMLState{status: ElemClose}},
				{state: utXMLState{status: End}},
			},
		}, // test_xml.c:87
		{
			input:   "  <  element attr='x'/>",
			initial: utXMLState{status: Element, checkQName: true, name: "element", checkDepth: true, depth: 1},
			steps: []utXMLStep{
				{state: utXMLState{status: Attribute, checkQName: true, name: "attr"}},
				{state: utXMLState{status: AttrContent, checkValue: true, value: "x", checkDepth: true, depth: 1}},
				{state: utXMLState{status: ElemContent}},
				{state: utXMLState{status: ElemClose, checkDepth: true, depth: 0}},
				{state: utXMLState{status: End}},
			},
		}, // test_xml.c:108
		{
			input:   `<?xml version="1.0"?>  <!-- comment --> <?TEST xxx?> <element/>`,
			initial: utXMLState{status: Element, checkQName: true, name: "element", checkDepth: true, depth: 1},
			steps: []utXMLStep{
				{state: utXMLState{status: ElemContent}},
				{state: utXMLState{status: ElemClose}},
				{state: utXMLState{status: End}},
			},
		}, // test_xml.c:139
		{
			input:   `<element xmlns="urn"></element>`,
			initial: utXMLState{status: Element, checkQName: true, name: "element", checkDepth: true, depth: 1, checkNS: true, nsCount: 1},
			steps: []utXMLStep{
				{state: utXMLState{status: ElemContent}},
				{state: utXMLState{status: ElemClose, checkDepth: true, depth: 0, checkNS: true, nsCount: 0}},
				{state: utXMLState{status: End}},
			},
		}, // test_xml.c:159
		{
			input:   "  <  yin:element/>",
			initial: utXMLState{status: Element, checkQName: true, prefix: "yin", name: "element"},
			steps: []utXMLStep{
				{state: utXMLState{status: ElemContent}},
				{state: utXMLState{status: ElemClose}},
				{state: utXMLState{status: End}},
			},
		}, // test_xml.c:182
		{
			input:   `<yin:element xmlns="urn"></element>`,
			initial: utXMLState{status: Element, checkQName: true, prefix: "yin", name: "element", checkDepth: true, depth: 1, checkNS: true, nsCount: 1},
			steps: []utXMLStep{
				{state: utXMLState{status: ElemContent}},
				{diag: &utXMLDiag{`Opening ("yin:element") and closing ("element") elements tag mismatch.`, 1}},
			},
		}, // test_xml.c:201
		{
			input:   `<yin:element xmlns="urn"></yin:element/>`,
			initial: utXMLState{status: Element},
			steps: []utXMLStep{
				{state: utXMLState{status: ElemContent}},
				{diag: &utXMLDiag{`Invalid character sequence "/>", expected element tag termination ('>').`, 1}},
			},
		}, // test_xml.c:219
		{
			input:   "<𠜎€𠜎Øn:𠜎€𠜎Øn/>",
			initial: utXMLState{status: Element, checkQName: true, prefix: "𠜎€𠜎Øn", name: "𠜎€𠜎Øn"},
			steps: []utXMLStep{
				{state: utXMLState{status: ElemContent}},
				{state: utXMLState{status: ElemClose}},
				{state: utXMLState{status: End}},
			},
		}, // test_xml.c:229
		{input: "<¢:element>", newDiag: &utXMLDiag{`Identifier "¢:element>" starts with an invalid character.`, 1}}, // test_xml.c:247
		{
			input: "<yin:c⁐element>", initial: utXMLState{status: Element},
			steps: []utXMLStep{{diag: &utXMLDiag{`Invalid character sequence "⁐element>", expected element tag end ('>' or '/>') or an attribute.`, 1}}},
		}, // test_xml.c:253
		{
			input:   "<a>text <b>x</b></a>",
			initial: utXMLState{status: Element, checkQName: true, name: "a"},
			steps: []utXMLStep{
				{state: utXMLState{status: ElemContent, checkValue: true, value: "text "}},
				{state: utXMLState{status: Element, checkQName: true, name: "b"}},
				{state: utXMLState{status: ElemContent, checkValue: true, value: "x"}},
				{state: utXMLState{status: ElemClose}},
				{state: utXMLState{status: ElemClose}},
				{state: utXMLState{status: End}},
			},
		}, // test_xml.c:262
		{
			input:   "<a>text</b>",
			initial: utXMLState{status: Element, checkQName: true, name: "a"},
			steps: []utXMLStep{
				{state: utXMLState{status: ElemContent, checkValue: true, value: "text"}},
				{diag: &utXMLDiag{`Opening ("a") and closing ("b") elements tag mismatch.`, 1}},
			},
		}, // test_xml.c:294
	})
}

func TestUTAttribute(t *testing.T) {
	utRunXMLCases(t, []utXMLCase{
		{input: "<e unknown/>", newDiag: &utXMLDiag{`Invalid character sequence "/>", expected '='.`, 1}},                                           // test_xml.c:320
		{input: "<e xxx=/>", newDiag: &utXMLDiag{`Invalid character sequence "/>", expected either single or double quotation mark.`, 1}},           // test_xml.c:326
		{input: "<e xxx\n = yyy/>", newDiag: &utXMLDiag{`Invalid character sequence "yyy/>", expected either single or double quotation mark.`, 2}}, // test_xml.c:332
		{
			input: `<e attr="val"`, initial: utXMLState{status: Element},
			steps: []utXMLStep{
				{state: utXMLState{status: Attribute, checkQName: true, name: "attr"}},
				{state: utXMLState{status: AttrContent, checkValue: true, value: "val", checkWS: true, ws: false}},
			},
		}, // test_xml.c:339
		{
			input:   "<e xmlns:nc\n = 'urn'/>",
			initial: utXMLState{status: Element, checkNS: true, nsCount: 1, nsEntries: []utXMLNS{{prefix: "nc", uri: "urn"}}},
		}, // test_xml.c:357
	})
}

func TestUTText(t *testing.T) {
	type textCase struct {
		input       string
		newCtx      bool
		resetStatus Status
		initial     utXMLState
		steps       []utXMLStep
	}
	cases := []textCase{
		{
			input: "<e a=\"\"", newCtx: true, initial: utXMLState{status: Element},
			steps: []utXMLStep{
				{state: utXMLState{status: Attribute}},
				{state: utXMLState{status: AttrContent, checkValue: true, value: "", checkWS: true, ws: true}},
			},
		}, // test_xml.c:377
		{
			input: "=''", resetStatus: Attribute,
			steps: []utXMLStep{{state: utXMLState{status: AttrContent, checkValue: true, value: "", checkWS: true, ws: true}}},
		}, // test_xml.c:392
		{
			input: ">\n  <y>", resetStatus: Element,
			steps: []utXMLStep{{state: utXMLState{status: ElemContent, checkValue: true, value: "\n  ", checkWS: true, ws: true}}},
		}, // test_xml.c:404
		{
			input: "", resetStatus: ElemContent,
			steps: []utXMLStep{{diag: &utXMLDiag{"Unexpected end-of-input.", 1}}},
		}, // test_xml.c:416
		{
			input: "xxx", resetStatus: ElemContent,
			steps: []utXMLStep{{diag: &utXMLDiag{`Invalid character sequence "xxx", expected element tag start ('<').`, 1}}},
		}, // test_xml.c:424
		{
			input: "<a>€𠜎Øn \n&lt;&amp;&quot;&apos;&gt; &#82;&#x4f;&#x4B;</a>", newCtx: true,
			initial: utXMLState{status: Element},
			steps:   []utXMLStep{{state: utXMLState{status: ElemContent, checkValue: true, value: "€𠜎Øn \n<&\"'> ROK", checkWS: true, ws: false}}},
		}, // test_xml.c:436
		{
			input: "='&#x0024;&#x00A2;&#x20ac;&#x10348;'", resetStatus: Attribute,
			steps: []utXMLStep{{state: utXMLState{status: AttrContent, checkValue: true, value: "$¢€𐍈", checkWS: true, ws: false}}},
		}, // test_xml.c:450
		{
			input: ">   <![CDATA[    special non-escaped chars <>&\"'  ]]>  </a>", resetStatus: AttrContent,
			steps: []utXMLStep{{state: utXMLState{status: ElemContent, checkValue: true, value: "       special non-escaped chars <>&\"'    ", checkWS: true, ws: false}}},
		}, // test_xml.c:462
		{
			input: "='&#x52'", resetStatus: Attribute,
			steps: []utXMLStep{{diag: &utXMLDiag{`Invalid character sequence "'", expected ;.`, 1}}},
		}, // test_xml.c:475
		{
			input: `="&#82"`, resetStatus: Attribute,
			steps: []utXMLStep{{diag: &utXMLDiag{`Invalid character sequence """, expected ;.`, 1}}},
		}, // test_xml.c:483
		{
			input: `="&nonsense;"`, resetStatus: Attribute,
			steps: []utXMLStep{{diag: &utXMLDiag{`Entity reference "&nonsense;" not supported, only predefined references allowed.`, 1}}},
		}, // test_xml.c:491
		{
			input: ">&#o122;", resetStatus: Element,
			steps: []utXMLStep{{diag: &utXMLDiag{`Invalid character reference "&#o122;".`, 1}}},
		}, // test_xml.c:499
		{
			input: "='&#x06;'", resetStatus: Attribute,
			steps: []utXMLStep{{diag: &utXMLDiag{`Invalid character reference "&#x06;'" (0x00000006).`, 1}}},
		}, // test_xml.c:507
		{
			input: "='&#xfdd0;'", resetStatus: Attribute,
			steps: []utXMLStep{{diag: &utXMLDiag{`Invalid character reference "&#xfdd0;'" (0x0000fdd0).`, 1}}},
		}, // test_xml.c:515
		{
			input: "='&#xffff;'", resetStatus: Attribute,
			steps: []utXMLStep{{diag: &utXMLDiag{`Invalid character reference "&#xffff;'" (0x0000ffff).`, 1}}},
		}, // test_xml.c:523
	}

	// The C test reuses one context: each case that creates a context starts a chain, and the
	// following cases swap the input of that same context. One subtest per chain, so every
	// subtest sets up its own context.
	var chains [][]textCase
	for _, tc := range cases {
		if tc.newCtx {
			chains = append(chains, nil)
		}
		chains[len(chains)-1] = append(chains[len(chains)-1], tc)
	}
	for _, chain := range chains {
		t.Run(chain[0].input, func(t *testing.T) {
			c, err := New([]byte(chain[0].input))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			for i, tc := range chain {
				if i == 0 {
					utXMLCheckState(t, c, tc.initial)
				} else {
					t.Logf("case %q", tc.input)
					utResetXML(c, tc.input, tc.resetStatus)
				}
				utRunXMLSteps(t, c, tc.steps)
			}
		})
	}
}

func TestUTNs(t *testing.T) {
	for _, tc := range []struct{ input string }{
		{input: "<element1/>"}, // test_xml.c:544
	} {
		t.Run(tc.input, func(t *testing.T) {
			c, err := New([]byte(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			if err := c.nsAdd("", "urn:default"); err != nil {
				t.Fatal(err)
			}
			if err := c.nsAdd("nc", "urn:nc1"); err != nil {
				t.Fatal(err)
			}
			c.elems.push(elem{}, &c.copied)
			if err := c.nsAdd("nc", "urn:nc2"); err != nil {
				t.Fatal(err)
			}
			if len(c.NS()) != 3 {
				t.Fatalf("namespace count = %d, want 3", len(c.NS()))
			}
			if ns, ok := c.GetNS(""); !ok || ns.Prefix != "" || ns.URI != "urn:default" {
				t.Fatalf("default namespace = %+v, found %v", ns, ok)
			}
			if ns, ok := c.GetNS("nc"); !ok || ns.Prefix != "nc" || ns.URI != "urn:nc2" {
				t.Fatalf("inner nc namespace = %+v, found %v", ns, ok)
			}
			c.elems.s = c.elems.s[:len(c.elems.s)-1]
			c.nsRm()
			if len(c.NS()) != 2 {
				t.Fatalf("namespace count after child = %d, want 2", len(c.NS()))
			}
			if ns, ok := c.GetNS("nc"); !ok || ns.Prefix != "nc" || ns.URI != "urn:nc1" {
				t.Fatalf("parent nc namespace = %+v, found %v", ns, ok)
			}
			if err := c.Next(); err != nil {
				t.Fatal(err)
			}
			if err := c.Next(); err != nil {
				t.Fatal(err)
			}
			if len(c.NS()) != 0 {
				t.Fatalf("namespace count at end = %d, want 0", len(c.NS()))
			}
			if _, ok := c.GetNS("nc"); ok {
				t.Fatal("nc namespace still present")
			}
			if _, ok := c.GetNS(""); ok {
				t.Fatal("default namespace still present")
			}
		})
	}
}

func TestUTNs2(t *testing.T) {
	for _, tc := range []struct{ input string }{
		{input: "<element1/>"}, // test_xml.c:598
	} {
		t.Run(tc.input, func(t *testing.T) {
			c, err := New([]byte(tc.input))
			if err != nil {
				t.Fatal(err)
			}
			if err := c.nsAdd("", "urn:default"); err != nil {
				t.Fatal(err)
			}
			if len(c.NS()) != 1 {
				t.Fatalf("namespace count = %d, want 1", len(c.NS()))
			}
			c.elems.push(elem{}, &c.copied)
			c.elems.s = c.elems.s[:len(c.elems.s)-1]
			c.nsRm()
			if len(c.NS()) != 1 {
				t.Fatalf("namespace count after child = %d, want 1", len(c.NS()))
			}
			if err := c.Next(); err != nil {
				t.Fatal(err)
			}
			if err := c.Next(); err != nil {
				t.Fatal(err)
			}
			if len(c.NS()) != 0 {
				t.Fatalf("namespace count at end = %d, want 0", len(c.NS()))
			}
		})
	}
}

func TestUTSimpleXML(t *testing.T) {
	utRunXMLCases(t, []utXMLCase{
		{
			input:   `<elem1 attr1="value"> <elem2 attr2="value" /> </elem1>`,
			initial: utXMLState{status: Element, checkRest: true, rest: `attr1="value"> <elem2 attr2="value" /> </elem1>`},
			steps: []utXMLStep{
				{state: utXMLState{status: Attribute, checkRest: true, rest: `="value"> <elem2 attr2="value" /> </elem1>`}},
				{state: utXMLState{status: AttrContent, checkRest: true, rest: `> <elem2 attr2="value" /> </elem1>`}},
				{state: utXMLState{status: ElemContent, checkRest: true, rest: `<elem2 attr2="value" /> </elem1>`}},
				{state: utXMLState{status: Element, checkRest: true, rest: `attr2="value" /> </elem1>`}},
				{state: utXMLState{status: Attribute, checkRest: true, rest: `="value" /> </elem1>`}},
				{state: utXMLState{status: AttrContent, checkRest: true, rest: ` /> </elem1>`}},
				{state: utXMLState{status: ElemContent, checkRest: true, rest: `/> </elem1>`}},
				{state: utXMLState{status: ElemClose, checkRest: true, rest: ` </elem1>`}},
				{state: utXMLState{status: ElemClose, checkRest: true, rest: ""}},
				{state: utXMLState{status: End, checkRest: true, rest: ""}},
			},
		}, // test_xml.c:627
	})
}
