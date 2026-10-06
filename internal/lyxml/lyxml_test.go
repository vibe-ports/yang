// SPDX-License-Identifier: BSD-3-Clause

package lyxml

import (
	"errors"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/ly"
)

// Expectations below come from libyang v5.8.6 tests/utests/basic/test_xml.c and from reading xml.c.

type step struct {
	st     Status
	prefix string
	name   string
	value  string
	ws     bool
}

// walk runs Next until End or an error and returns the steps and the error.
func walk(t *testing.T, src string) ([]step, *Ctx, error) {
	t.Helper()
	c, err := New([]byte(src))
	if err != nil {
		return nil, nil, err
	}
	var out []step
	for {
		out = append(out, snap(c))
		if c.Status == End {
			return out, c, nil
		}
		if err := c.Next(); err != nil {
			return out, c, err
		}
	}
}

func snap(c *Ctx) step {
	switch c.Status {
	case Element, Attribute:
		return step{st: c.Status, prefix: c.Prefix, name: c.Name}
	case ElemContent, AttrContent:
		return step{st: c.Status, value: c.Value, ws: c.WSOnly}
	}
	return step{st: c.Status}
}

func TestSequences(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      []step
	}{
		{"empty", "", []step{{st: End}}},
		{"unqualified", "  <  element/>", []step{
			{st: Element, name: "element"}, {st: ElemContent, ws: true}, {st: ElemClose}, {st: End}}},
		{"attribute", "  <  element attr='x'/>", []step{
			{st: Element, name: "element"}, {st: Attribute, name: "attr"}, {st: AttrContent, value: "x"},
			{st: ElemContent, ws: true}, {st: ElemClose}, {st: End}}},
		{"header and comments", `<?xml version="1.0"?>  <!-- comment --> <?TEST xxx?> <element/>`, []step{
			{st: Element, name: "element"}, {st: ElemContent, ws: true}, {st: ElemClose}, {st: End}}},
		{"namespace attr is skipped", `<element xmlns="urn"></element>`, []step{
			{st: Element, name: "element"}, {st: ElemContent, ws: true}, {st: ElemClose}, {st: End}}},
		{"qualified", "  <  yin:element/>", []step{
			{st: Element, prefix: "yin", name: "element"}, {st: ElemContent, ws: true}, {st: ElemClose}, {st: End}}},
		{"utf8 names", "<𠜎€𠜎Øn:𠜎€𠜎Øn/>", []step{
			{st: Element, prefix: "𠜎€𠜎Øn", name: "𠜎€𠜎Øn"}, {st: ElemContent, ws: true}, {st: ElemClose}, {st: End}}},
		{"mixed content", "<a>text <b>x</b></a>", []step{
			{st: Element, name: "a"}, {st: ElemContent, value: "text "}, {st: Element, name: "b"},
			{st: ElemContent, value: "x"}, {st: ElemClose}, {st: ElemClose}, {st: End}}},
		{"whitespace-only text is kept", "<a>\n  <b/>\t</a>", []step{
			{st: Element, name: "a"}, {st: ElemContent, value: "\n  ", ws: true}, {st: Element, name: "b"},
			{st: ElemContent, ws: true}, {st: ElemClose}, {st: ElemClose}, {st: End}}},
		{"empty vs space string", "<a> </a>", []step{
			{st: Element, name: "a"}, {st: ElemContent, value: " ", ws: true}, {st: ElemClose}, {st: End}}},
		{"attr after namespaces, ns attr in the middle", `<e xmlns:a="u" b='1' xmlns:c="v" d="2"/>`, []step{
			{st: Element, name: "e"}, {st: Attribute, name: "b"}, {st: AttrContent, value: "1"},
			{st: Attribute, name: "d"}, {st: AttrContent, value: "2"}, {st: ElemContent, ws: true},
			{st: ElemClose}, {st: End}}},
		{"empty attribute value", `<e a=""`, []step{
			{st: Element, name: "e"}, {st: Attribute, name: "a"}, {st: AttrContent, ws: true}}},
		{"entities and char refs", "<a>€𠜎Øn \n&lt;&amp;&quot;&apos;&gt; &#82;&#x4f;&#x4B;</a>", []step{
			{st: Element, name: "a"}, {st: ElemContent, value: "€𠜎Øn \n<&\"'> ROK"}, {st: ElemClose}, {st: End}}},
		{"n-byte char refs", "<a b='&#x0024;&#x00A2;&#x20ac;&#x10348;'/>", []step{
			{st: Element, name: "a"}, {st: Attribute, name: "b"}, {st: AttrContent, value: "$¢€𐍈"},
			{st: ElemContent, ws: true}, {st: ElemClose}, {st: End}}},
		{"cdata", "<a>   <![CDATA[    special non-escaped chars <>&\"'  ]]>  </a>", []step{
			{st: Element, name: "a"}, {st: ElemContent, value: "       special non-escaped chars <>&\"'    "},
			{st: ElemClose}, {st: End}}},
		{"cdata with only whitespace is ws-only", "<a><![CDATA[ \n ]]></a>", []step{
			{st: Element, name: "a"}, {st: ElemContent, value: " \n ", ws: true}, {st: ElemClose}, {st: End}}},
		{"empty cdata", "<a><![CDATA[]]></a>", []step{
			{st: Element, name: "a"}, {st: ElemContent, ws: true}, {st: ElemClose}, {st: End}}},
		{"cdata is not utf-8 checked", "<a><![CDATA[\x01\xff]]></a>", []step{
			{st: Element, name: "a"}, {st: ElemContent, value: "\x01\xff"}, {st: ElemClose}, {st: End}}},
		{"entity makes text non-whitespace", "<a>&#32;</a>", []step{
			{st: Element, name: "a"}, {st: ElemContent, value: " "}, {st: ElemClose}, {st: End}}},
		{"input ends at NUL", "<a/>\x00junk", []step{
			{st: Element, name: "a"}, {st: ElemContent, ws: true}, {st: ElemClose}, {st: End}}},
		{"declaration shortcut <?>", "<?><a/>", []step{
			{st: Element, name: "a"}, {st: ElemContent, ws: true}, {st: ElemClose}, {st: End}}},
		{"trailing comment after root", "<a/><!-- x -->\n", []step{
			{st: Element, name: "a"}, {st: ElemContent, ws: true}, {st: ElemClose}, {st: End}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := walk(t, tc.src)
			if tc.name == "empty attribute value" { // unterminated tag: error after the steps
				if err == nil {
					t.Fatal("want error")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("steps = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("step %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		msg       string
		line      uint64
	}{
		{"stray close at start", "</element>", `Stray closing element tag ("element").`, 1},
		{"no element", "no data present", `Invalid character sequence "no data present", expected element tag start ('<').`, 1},
		{"doctype", `<!DOCTYPE greeting SYSTEM "hello.dtd"><greeting/>`, "Document Type Declaration not supported.", 1},
		{"unknown section", "<!NONSENSE/>", `Unknown XML section "<!NONSENSE/>".`, 1},
		{"dup default ns", `<element xmlns="urn1" xmlns="urn2"/>`, `Duplicate default XML namespaces "urn1" and "urn2".`, 1},
		{"dup prefix", `<element xmlns:a="urn1" xmlns:a="urn2"/>`, `Duplicate XML NS prefix "a" used for namespaces "urn1" and "urn2".`, 1},
		{"bad identifier start", "<¢:element>", `Identifier "¢:element>" starts with an invalid character.`, 1},
		{"bad char after name", "<yin:c⁐element>", `Invalid character sequence "⁐element>", expected element tag end ('>' or '/>') or an attribute.`, 1},
		{"attr without =", "<e unknown/>", `Invalid character sequence "/>", expected '='.`, 1},
		{"attr without value", "<e xxx=/>", `Invalid character sequence "/>", expected either single or double quotation mark.`, 1},
		{"attr unquoted, line", "<e xxx\n = yyy/>", `Invalid character sequence "yyy/>", expected either single or double quotation mark.`, 2},
		{"mismatch with prefix", `<yin:element xmlns="urn"></element>`, `Opening ("yin:element") and closing ("element") elements tag mismatch.`, 1},
		{"mismatch", "<a>text</b>", `Opening ("a") and closing ("b") elements tag mismatch.`, 1},
		{"closing with slash", `<yin:element xmlns="urn"></yin:element/>`, `Invalid character sequence "/>", expected element tag termination ('>').`, 1},
		{"unterminated char ref ;", "<a b='&#x52'/>", `Invalid character sequence "'/>", expected ;.`, 1},
		{"unterminated dec char ref", `<a b="&#82"/>`, `Invalid character sequence ""/>", expected ;.`, 1},
		{"entity", `<a b="&nonsense;"/>`, `Entity reference "&nonsense;" not supported, only predefined references allowed.`, 1},
		{"bad char ref", "<a>&#o122;</a>", `Invalid character reference "&#o122;</a>".`, 1},
		{"control char ref", "<a b='&#x06;'/>", `Invalid character reference "&#x06;'/>" (0x00000006).`, 1},
		{"noncharacter ref fdd0", "<a b='&#xfdd0;'/>", `Invalid character reference "&#xfdd0;'/>" (0x0000fdd0).`, 1},
		{"noncharacter ref ffff", "<a b='&#xffff;'/>", `Invalid character reference "&#xffff;'/>" (0x0000ffff).`, 1},
		{"unterminated cdata", "<a><![CDATA[x</a>", "CDATA not terminated.", 1},
		{"unterminated comment", "<!-- x", "Comment not terminated.", 1},
		{"comment opener at eof", "<!--", "Unexpected end-of-input.", 1},
		{"lt at eof", "<", "Unexpected end-of-input.", 1},
		{"eof in name", "<a", "Invalid character 0x0.", 1},
		{"eof in text", "<a>text", "Unexpected end-of-input.", 1},
		{"eof after >", "<a>", "Unexpected end-of-input.", 1},
		{"eof in open element", "<a>x<b>y</b>", "Unexpected end-of-input.", 1},
		{"control char in text", "<a>\x01</a>", "Invalid character 0x1.", 1},
		{"invalid utf-8 in text", "<a>\xc3(</a>", "Invalid character 0xc3.", 1},
		{"lines in text counted", "<a>x\n\n&bad;</a>", `Entity reference "&bad;</a>" not supported, only predefined references allowed.`, 3},
		{"lines in cdata counted", "<a><![CDATA[x\ny\n]]>z\nq</b>", `Opening ("a") and closing ("b") elements tag mismatch.`, 4},
		{"lines in comment counted", "<!--\n\n-->\n<a>x</b>", `Opening ("a") and closing ("b") elements tag mismatch.`, 4},
		{"yanglint: unterminated comment, line", "<c>\n<a>x</a>\n\n<b/>\n<!-- x\n", "Comment not terminated.", 5},
		{"yanglint: entity in text", "<c xmlns=\"urn:m\">\n<a>x\n&bad;</a></c>", `Entity reference "&bad;</a><" not supported, only predefined references allowed.`, 3},
		{"yanglint: cdata lines", "<c xmlns=\"urn:m\"><a>\n\n<![CDATA[x\ny]]></b></c>", `Opening ("a") and closing ("b") elements tag mismatch.`, 4},
		{"second root closing", "<a/></b>", `Stray closing element tag ("b").`, 1},
		{"garbage after root", "<a/>x", `Invalid character sequence "x", expected element tag start ('<').`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := walk(t, tc.src)
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if e.Msg != tc.msg || e.Line != tc.line || e.Code != ly.Syntax {
				t.Fatalf("got %q line %d code %v, want %q line %d", e.Msg, e.Line, e.Code, tc.msg, tc.line)
			}
		})
	}
}

func TestSilentInvalidUTF8(t *testing.T) { // open_element fails without a message
	_, _, err := walk(t, "<a \xff>")
	var e *Error
	if !errors.As(err, &e) || e.Msg != "" || e.Code != ly.Success {
		t.Fatalf("err = %#v", err)
	}
}

func TestStatusEndAfterError(t *testing.T) {
	c, _ := New([]byte("<a>x</b>"))
	_ = c.Next()
	if c.Next() == nil || c.Status != End {
		t.Fatal("want error and End")
	}
	if c.Next() != nil {
		t.Fatal("Next at End is a no-op")
	}
}

func TestDepthLimit(t *testing.T) {
	// run drains the lexer and returns the deepest nesting seen and the error.
	run := func(src string) (int, error) {
		c, err := New([]byte(src))
		deepest := 0
		for err == nil && c.Status != End {
			deepest = max(deepest, c.Depth())
			err = c.Next()
		}
		return deepest, err
	}
	// 500 nested elements are fine (libyang: count > LY_MAX_BLOCK_DEPTH fails)...
	if d, err := run(strings.Repeat("<a>", MaxDepth) + strings.Repeat("</a>", MaxDepth)); err != nil || d != MaxDepth {
		t.Fatalf("500: depth %d err %v", d, err)
	}
	// ...the 501st is not, with a vecode-less LOGERR message.
	_, err := run(strings.Repeat("<a>", MaxDepth+1) + strings.Repeat("</a>", MaxDepth+1))
	var e *Error
	if !errors.As(err, &e) || e.Msg != "The maximum number of open elements has been exceeded." ||
		e.Code != ly.Success || !errors.Is(err, ErrBudget) {
		t.Fatalf("501: err = %#v", err)
	}
}

func TestNamespaces(t *testing.T) {
	c, err := New([]byte(`<e xmlns:nc = 'urn:nc' xmlns="urn:d" a="1"><f xmlns:nc="urn:nc2"><g/></f></e>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.NS()) != 2 {
		t.Fatalf("ns = %+v", c.NS())
	}
	if n, ok := c.GetNS("nc"); !ok || n.URI != "urn:nc" {
		t.Fatal(n)
	}
	if n, ok := c.GetNS(""); !ok || n.URI != "urn:d" {
		t.Fatal(n)
	}
	for c.Status != Element || c.Name != "g" {
		if err := c.Next(); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := c.GetNS("nc"); n.URI != "urn:nc2" {
		t.Fatalf("inner redefinition = %+v", n)
	}
	for c.Status != End {
		if err := c.Next(); err != nil {
			t.Fatal(err)
		}
		if c.Status == ElemClose && c.Depth() == 1 {
			if n, _ := c.GetNS("nc"); n.URI != "urn:nc" {
				t.Fatalf("after leaving f = %+v", n)
			}
		}
	}
	if len(c.NS()) != 0 {
		t.Fatal("namespaces left")
	}
}

func TestNsAddDirect(t *testing.T) { // test_ns / test_ns2
	c, _ := New([]byte("<element1/>"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.nsAdd("", "urn:default"))
	must(c.nsAdd("nc", "urn:nc1"))
	c.elems.push(elem{}, &c.copied) // element2 opened
	must(c.nsAdd("nc", "urn:nc2"))
	must(c.nsAdd("nc", "urn:nc2")) // same prefix and URI in one element: ignored
	if len(c.ns.s) != 3 {
		t.Fatalf("ns = %+v", c.ns.s)
	}
	if n, _ := c.GetNS("nc"); n.URI != "urn:nc2" {
		t.Fatal(n)
	}
	c.elems.s = c.elems.s[:1]
	c.nsRm()
	if n, _ := c.GetNS("nc"); len(c.ns.s) != 2 || n.URI != "urn:nc1" {
		t.Fatalf("ns = %+v", c.ns.s)
	}
	must(c.Next())
	must(c.Next())
	if len(c.ns.s) != 0 {
		t.Fatalf("ns = %+v", c.ns.s)
	}
	if _, ok := c.GetNS("nc"); ok {
		t.Fatal("found nc")
	}
	if _, ok := c.GetNS(""); ok {
		t.Fatal("found default")
	}
}

func TestPositions(t *testing.T) { // test_simple_xml: where the input stands after each step
	c, err := New([]byte(`<elem1 attr1="value"> <elem2 attr2="value" /> </elem1>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`attr1="value"> <elem2 attr2="value" /> </elem1>`,
		`="value"> <elem2 attr2="value" /> </elem1>`,
		`> <elem2 attr2="value" /> </elem1>`,
		`<elem2 attr2="value" /> </elem1>`,
		`attr2="value" /> </elem1>`,
		`="value" /> </elem1>`,
		` /> </elem1>`,
		`/> </elem1>`,
		` </elem1>`,
		``,
		``,
	} {
		if got := string(c.in[c.pos:]); got != want {
			t.Fatalf("at %v: rest = %q, want %q", c.Status, got, want)
		}
		if err := c.Next(); err != nil {
			t.Fatal(err)
		}
	}
	if c.Status != End || string(c.in[c.pos:]) != "" {
		t.Fatalf("end: %v %q", c.Status, c.in[c.pos:])
	}
}

func TestPeek(t *testing.T) {
	for _, src := range []string{
		"<a/>", "<a x='1'>t<b>u</b>\n</a>", "<a><!-- c --><b/></a>", "<?xml ?>\n<a xmlns='u' x='1'></a>\n",
	} {
		c, err := New([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		for c.Status != End {
			pos := c.pos
			want, perr := c.Peek()
			if perr != nil {
				t.Fatalf("%q: peek: %v", src, perr)
			}
			if c.pos != pos {
				t.Fatalf("%q: peek moved the input", src)
			}
			if err := c.Next(); err != nil {
				t.Fatal(err)
			}
			if c.Status != want {
				t.Fatalf("%q: peek = %v, next = %v", src, want, c.Status)
			}
		}
	}
}

// libyang's lyxml_ctx_peek restores the input pointer but not the line counter, so
// whitespace newlines scanned while peeking are counted again by the following Next.
func TestPeekDoesNotRestoreLine(t *testing.T) {
	c, _ := New([]byte("<a>x</a>\n\n<b/>"))
	_ = c.Next() // content x
	_ = c.Next() // close a
	l := c.Line()
	if _, err := c.Peek(); err != nil {
		t.Fatal(err)
	}
	if c.Line() != l+2 {
		t.Fatalf("line %d -> %d", l, c.Line())
	}
}

func TestPeekError(t *testing.T) {
	c, _ := New([]byte("<a>x</a>garbage"))
	_ = c.Next()
	_ = c.Next()
	if _, err := c.Peek(); err == nil {
		t.Fatal("want error")
	}
}

func TestBackupRestore(t *testing.T) {
	c, err := New([]byte("<a xmlns='u'>\n<b xmlns:p='v'>t</b>\n<c/></a>"))
	if err != nil {
		t.Fatal(err)
	}
	var steps []step
	b := c.Backup()
	for c.Status != End {
		steps = append(steps, snap(c))
		if err := c.Next(); err != nil {
			t.Fatal(err)
		}
	}
	for round := 0; round < 2; round++ { // a backup is single use: take a new one
		c.Restore(b)
		b = c.Backup()
		if c.Status != Element || c.Name != "a" || c.Line() != 1 || len(c.NS()) != 1 || c.Depth() != 1 {
			t.Fatalf("restored: %v %q line %d ns %d", c.Status, c.Name, c.Line(), len(c.NS()))
		}
		for i := range steps {
			if got := snap(c); got != steps[i] {
				t.Fatalf("round %d step %d = %+v want %+v", round, i, got, steps[i])
			}
			if err := c.Next(); err != nil {
				t.Fatal(err)
			}
		}
	}
	// restore in the middle of an element keeps its namespaces and open elements
	c.Restore(b)
	_ = c.Next() // ElemContent "\n"
	_ = c.Next() // b
	mid := c.Backup()
	_ = c.Next()
	_ = c.Next()
	_ = c.Next() // close b
	c.Restore(mid)
	if c.Name != "b" || c.Depth() != 2 || len(c.NS()) != 2 {
		t.Fatalf("mid: %q depth %d ns %d", c.Name, c.Depth(), len(c.NS()))
	}
}

func TestAppendText(t *testing.T) {
	const in = `a<b>&"c"`
	if got := string(AppendText(nil, in, false)); got != `a&lt;b&gt;&amp;"c"` {
		t.Fatal(got)
	}
	if got := string(AppendText([]byte("x"), in, true)); got != `xa&lt;b&gt;&amp;&quot;c&quot;` {
		t.Fatal(got)
	}
}

func TestPutUTF8(t *testing.T) {
	for _, tc := range []struct {
		v  uint32
		ok bool
		n  int
	}{
		{0x24, true, 1}, {0x0, false, 0}, {0x9, true, 1}, {0x1f, false, 0}, {0xa2, true, 2}, {0x7ff, true, 2},
		{0x800, true, 3}, {0xd800, false, 0}, {0xdfff, false, 0}, {0xfdd0, false, 0}, {0xfdef, false, 0},
		{0xfdf0, true, 3}, {0xfffd, true, 3}, {0xfffe, false, 0}, {0xffff, false, 0}, {0x10000, true, 4},
		{0x1fffe, false, 0}, {0x10348, true, 4}, {0x10fffd, true, 4}, {0x10fffe, false, 0}, {0x110000, false, 0},
		{0xffffffff, false, 0},
	} {
		b, ok := putUTF8(tc.v)
		if ok != tc.ok || len(b) != tc.n {
			t.Errorf("putUTF8(%#x) = %x, %v", tc.v, b, ok)
		}
	}
}

func TestCharRefOverflowWraps(t *testing.T) { // uint32 arithmetic like libyang: 2^32+36 is '$'
	c, err := New([]byte("<a>&#4294967332;</a>"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Next(); err != nil || c.Value != "$" {
		t.Fatalf("%q %v", c.Value, err)
	}
}

func TestLargeInputLinear(t *testing.T) { // no assertion on time: just must finish
	src := "<a>" + strings.Repeat("<b>text &amp; more</b>\n", 20000) + "</a>"
	c, err := New([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for c.Status != End {
		if err := c.Next(); err != nil {
			t.Fatal(err)
		}
		n++
	}
	if c.Line() != 20001 {
		t.Fatalf("line = %d", c.Line())
	}
}
