// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// The module "a" of libyang v5.8.6 tests/utests/data/test_parser_xml.c and test_parser_json.c.
const (
	envModuleXML = `module a {
    namespace urn:tests:a;
    prefix a;
    yang-version 1.1;
    import ietf-yang-metadata {prefix md;}
    list l1 { key "a b c"; leaf a {type string;} leaf b {type string;} leaf c {type int16;}
        leaf d {type string;}
        container cont {leaf e {type boolean;}}
    }
    leaf foo { type string;}
    container c {
        leaf x {type string;}
        action act { input { leaf al {type string;} } output { leaf al {type uint8;} } }
        notification n1 { leaf nl {type string;}}}
    container cp {presence "container switch"; leaf y {type string;} leaf z {type int8;}}
    anydata any {config false;}
    anyxml anyx;
    leaf foo2 { type string; default "default-val"; }
    leaf foo3 { type uint32; }
    notification n2;
    md:annotation attr {type enumeration {enum val;}}
}`
	envModuleJSON = `module a {namespace urn:tests:a;prefix a;yang-version 1.1; import ietf-yang-metadata {prefix md;}
md:annotation hint { type int8;}
list l1 { key "a b c"; leaf a {type string;} leaf b {type string;} leaf c {type int16;}
  leaf d {type string;}
  container cont {leaf e {type boolean;}}
}
leaf foo { type string;}
container c {
    leaf x {type string;}
    leaf-list y { type uint8; }
    action act { input { leaf al {type string;} } output { leaf al {type uint8;} } }
    notification n1 { leaf nl {type string;} }
}
container cp {presence "container switch"; leaf y {type string;} leaf z {type int8;}}
anydata any {config false;}
anyxml axml;
leaf-list ll1 { type uint8; }
leaf-list ll2 { type string; }
leaf foo2 { type string; default "default-val"; }
leaf foo3 { type uint32; }
leaf foo4 { type uint64; }
rpc r1 {input {leaf l1 {type string;} leaf l2 {type string;}}}
notification n2;
}`
)

// envSchema is the context of the envelope tests: module a and the loads of the test.
func envSchema(t *testing.T, module string, load ...[]string) *yang.Schema {
	t.Helper()
	fsys := fstest.MapFS{"a.yang": {Data: []byte(module)}}
	for _, f := range []string{"ietf-netconf@2011-06-01.yang", "ietf-netconf-acm@2018-02-14.yang",
		"ietf-netconf-nmda@2019-01-07.yang", "ietf-netconf-with-defaults@2011-06-01.yang", "ietf-origin@2018-02-14.yang"} {
		b, err := os.ReadFile("../conformance/corpus/ietf/" + f) //nolint:gosec // corpus path
		if err != nil {
			t.Fatal(err)
		}
		fsys[f] = &fstest.MapFile{Data: b}
	}
	ctx, _, err := yang.NewContext(yang.Options{}, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.Load("a", "", nil); err != nil {
		t.Fatal(err)
	}
	for _, l := range load {
		if _, err := ctx.Load(l[0], "", l[1:]); err != nil {
			t.Fatal(err)
		}
	}
	return ctx.Schema()
}

func parseEnv(t *testing.T, s *yang.Schema, f data.Format, typ data.OpType, req *data.Node, in string) (data.OpResult, error) {
	t.Helper()
	res, _, err := data.ParseOp(context.Background(), strings.NewReader(in), f, s, typ,
		data.ParseOpOptions{Unknown: data.Reject, Request: req})
	return res, err
}

// request is an rpc or action without input to parse a reply or RESTCONF input into.
func request(t *testing.T, s *yang.Schema, in string) data.OpResult {
	t.Helper()
	res, err := parseEnv(t, s, data.FormatJSON, data.OpRPC, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func envXML(t *testing.T, tr *data.Tree, shrink bool) string {
	t.Helper()
	var b strings.Builder
	if err := tr.PrintXML(&b, data.PrintOptions{Shrink: shrink}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func envJSON(t *testing.T, tr *data.Tree, shrink bool) string {
	t.Helper()
	var b strings.Builder
	if err := tr.PrintJSON(&b, data.PrintOptions{Shrink: shrink}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func nodeXML(t *testing.T, n *data.Node) string {
	t.Helper()
	var b strings.Builder
	if err := n.PrintXML(&b, data.PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func wantDiag(t *testing.T, err error, rc, msg string) {
	t.Helper()
	var ve *data.ValidationError
	if !errors.As(err, &ve) || ve.RC() != rc || len(ve.Diags) == 0 || ve.Diags[len(ve.Diags)-1].Msg != msg {
		t.Fatalf("got %v, want %s %q", err, rc, msg)
	}
}

func envTop(t *testing.T, res data.OpResult) *data.Node {
	t.Helper()
	if res.Envelope == nil {
		t.Fatal("no envelope")
	}
	for n := range res.Envelope.Top() {
		return n
	}
	t.Fatal("empty envelope")
	return nil
}

// TestNetconfRPC is test_parser_xml.c test_netconf_rpc.
func TestNetconfRPC(t *testing.T) {
	s := envSchema(t, envModuleXML, []string{"ietf-netconf", "writable-running"})
	in := "<rpc message-id=\"25\" xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\">" +
		"<edit-config>\n" +
		"  <target>\n" +
		"    <running/>\n" +
		"  </target>\n" +
		"  <config xmlns:nc=\"urn:ietf:params:xml:ns:netconf:base:1.0\">\n" +
		"    <l1 xmlns=\"urn:tests:a\" nc:operation=\"replace\">\n" +
		"      <a>val_a</a>\n" +
		"      <b>val_b</b>\n" +
		"      <c>val_c</c>\n" +
		"    </l1>\n" +
		"    <cp xmlns=\"urn:tests:a\">\n" +
		"      <z nc:operation=\"delete\"/>\n" +
		"    </cp>\n" +
		"  </config>\n" +
		"</edit-config>\n" +
		"</rpc>\n"
	res, err := parseEnv(t, s, data.FormatXML, data.OpRPCNetconf, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	env := envTop(t, res)
	if info, ok := env.OpaqueInfo(); !ok || env.Name() != "rpc" || env.Value() != "" || info.Format != data.FormatXML ||
		info.Namespace != "urn:ietf:params:xml:ns:netconf:base:1.0" {
		t.Errorf("envelope %s %+v", env.Name(), info)
	}
	if a := env.Attrs(); len(a) != 1 || a[0].Name != "message-id" || a[0].Value != "25" {
		t.Errorf("attributes %+v", a)
	}
	if res.Op == nil || res.Op.Name() != "edit-config" {
		t.Fatalf("op %v", res.Op)
	}
	if got, want := envXML(t, res.Envelope, false),
		"<rpc xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\" message-id=\"25\"/>\n"; got != want {
		t.Errorf("envelope:\n%s\nwant\n%s", got, want)
	}
	if got, want := envXML(t, res.Tree, false), "<edit-config xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\">\n"+
		"  <target>\n"+
		"    <running/>\n"+
		"  </target>\n"+
		"  <config>\n"+
		"    <cp xmlns=\"urn:tests:a\">\n"+
		"      <z xmlns:nc=\"urn:ietf:params:xml:ns:netconf:base:1.0\" nc:operation=\"delete\"/>\n"+
		"    </cp>\n"+
		"    <l1 xmlns=\"urn:tests:a\" xmlns:nc=\"urn:ietf:params:xml:ns:netconf:base:1.0\" nc:operation=\"replace\">\n"+
		"      <a>val_a</a>\n"+
		"      <b>val_b</b>\n"+
		"      <c>val_c</c>\n"+
		"    </l1>\n"+
		"  </config>\n"+
		"</edit-config>\n"; got != want {
		t.Errorf("op:\n%s\nwant\n%s", got, want)
	}

	// invalid anyxml nested metadata value: the envelope stays (lyd_parse_op's special situation)
	in = "<rpc xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\" message-id=\"1\" pid=\"4114692032\">\n" +
		"  <copy-config>\n" +
		"    <target>\n" +
		"      <running/>\n" +
		"    </target>\n" +
		"    <source>\n" +
		"      <config>\n" +
		"        <l1 xmlns=\"urn:tests:a\" xmlns:nc=\"urn:ietf:params:xml:ns:netconf:base:1.0\">\n" +
		"          <a>val_a</a>\n" +
		"          <b>val_b</b>\n" +
		"          <c>5</c>\n" +
		"          <cont nc:operation=\"merge\">\n" +
		"            <e nc:operation=\"merge2\">false</e>\n" +
		"          </cont>\n" +
		"        </l1>\n" +
		"      </config>\n" +
		"    </source>\n" +
		"  </copy-config>\n" +
		"</rpc>\n"
	res, err = parseEnv(t, s, data.FormatXML, data.OpRPCNetconf, nil, in)
	wantDiag(t, err, "LY_EVALID", "Invalid enumeration value \"merge2\".")
	var ve *data.ValidationError
	errors.As(err, &ve)
	d := ve.Diags[len(ve.Diags)-1]
	if d.DataPath != "/ietf-netconf:copy-config/source/config/a:l1[a='val_a'][b='val_b'][c='5']/cont/e/@ietf-netconf:operation" ||
		d.Line != 13 {
		t.Errorf("diagnostic %+v", d)
	}
	if res.Op != nil || res.Tree != nil || envTop(t, res).Name() != "rpc" {
		t.Errorf("partial result %+v", res)
	}
}

// TestPrintXMLFilterAttributesUnqualified is xml_print_meta's NETCONF filter exception: type and
// select are ietf-netconf metadata internally but print without an XML namespace. The two cases
// are oracle fixtures ut-parser/xml-filter-attributes-01 and -02.
func TestPrintXMLFilterAttributesUnqualified(t *testing.T) {
	ctx, _, err := yang.NewContext(yang.Options{}, os.DirFS("../conformance/corpus/ietf"),
		os.DirFS("../conformance/corpus/ut-parser/notif.1"))
	if err != nil {
		t.Fatal(err)
	}
	if diags, err := ctx.Load("ietf-netconf", "2011-06-01", []string{"writable-running"}); err != nil {
		t.Fatal(err, diags)
	}
	if diags, err := ctx.Load("notifications", "2008-07-14", nil); err != nil {
		t.Fatal(err, diags)
	}
	s := ctx.Schema()
	for _, tc := range []struct {
		name, input, want string
	}{
		{"extension", `<get xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><filter type="xpath" select="/*"/></get>`,
			`<get xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><filter type="xpath" select="/*"/></get>`},
		{"legacy notifications", `<create-subscription xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><filter type="subtree"><inner-node xmlns="my:urn"/></filter></create-subscription>`,
			`<create-subscription xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><filter type="subtree"><inner-node xmlns="my:urn"/></filter></create-subscription>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := parseEnv(t, s, data.FormatXML, data.OpRPC, nil, tc.input)
			if err != nil {
				t.Fatal(err)
			}
			if got := envXML(t, res.Tree, true); got != tc.want {
				t.Errorf("PrintXML = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNetconfAction is test_parser_xml.c test_netconf_action.
func TestNetconfAction(t *testing.T) {
	s := envSchema(t, envModuleXML)
	in := "<rpc message-id=\"25\" xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\">" +
		"<action xmlns=\"urn:ietf:params:xml:ns:yang:1\">" +
		"<c xmlns=\"urn:tests:a\">\n" +
		"  <act>\n" +
		"    <al>value</al>\n" +
		"  </act>\n" +
		"</c>\n" +
		"</action>\n" +
		"</rpc>\n"
	res, err := parseEnv(t, s, data.FormatXML, data.OpRPCNetconf, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Op == nil || res.Op.Name() != "act" {
		t.Fatalf("op %v", res.Op)
	}
	if got, want := envXML(t, res.Envelope, false), "<rpc xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\" message-id=\"25\">\n"+
		"  <action xmlns=\"urn:ietf:params:xml:ns:yang:1\"/>\n"+
		"</rpc>\n"; got != want {
		t.Errorf("envelope:\n%s\nwant\n%s", got, want)
	}
	if got, want := nodeXML(t, res.Op), "<act xmlns=\"urn:tests:a\">\n"+
		"  <al>value</al>\n"+
		"</act>\n"; got != want {
		t.Errorf("op:\n%s\nwant\n%s", got, want)
	}
}

// TestNetconfReplyOrNotification is test_parser_xml.c test_netconf_reply_or_notification.
func TestNetconfReplyOrNotification(t *testing.T) {
	s := envSchema(t, envModuleXML, []string{"ietf-netconf"})
	action, err := parseEnv(t, s, data.FormatXML, data.OpRPC, nil, "<c xmlns=\"urn:tests:a\">\n"+
		"  <act>\n"+
		"    <al>value</al>\n"+
		"  </act>\n"+
		"</c>\n")
	if err != nil {
		t.Fatal(err)
	}

	// notification first
	res, err := parseEnv(t, s, data.FormatXML, data.OpNotifNetconf, nil,
		"<notification xmlns=\"urn:ietf:params:xml:ns:netconf:notification:1.0\">\n"+
			"<eventTime>2010-12-06T08:00:01Z</eventTime>\n"+
			"<c xmlns=\"urn:tests:a\">\n"+
			"  <n1>\n"+
			"    <nl>value</nl>\n"+
			"  </n1>\n"+
			"</c>\n"+
			"</notification>\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.Op == nil || res.Op.Name() != "n1" {
		t.Fatalf("op %v", res.Op)
	}
	if got, want := envXML(t, res.Envelope, false), "<notification xmlns=\"urn:ietf:params:xml:ns:netconf:notification:1.0\">\n"+
		"  <eventTime>2010-12-06T08:00:01Z</eventTime>\n"+
		"</notification>\n"; got != want {
		t.Errorf("envelope:\n%s\nwant\n%s", got, want)
	}
	if got, want := nodeXML(t, res.Op), "<n1 xmlns=\"urn:tests:a\">\n"+
		"  <nl>value</nl>\n"+
		"</n1>\n"; got != want {
		t.Errorf("op:\n%s\nwant\n%s", got, want)
	}
	if got := envXML(t, res.Tree, true); got != `<c xmlns="urn:tests:a"><n1><nl>value</nl></n1></c>` {
		t.Errorf("the eventTime stayed in the tree: %s", got)
	}

	// notification with a different order
	if _, err := parseEnv(t, s, data.FormatXML, data.OpNotifNetconf, nil,
		"<notification xmlns=\"urn:ietf:params:xml:ns:netconf:notification:1.0\">\n"+
			"<c xmlns=\"urn:tests:a\">\n"+
			"  <n1>\n"+
			"    <nl>value</nl>\n"+
			"  </n1>\n"+
			"</c>\n"+
			"<eventTime>2010-12-06T08:00:01Z</eventTime>\n"+
			"</notification>\n"); err != nil {
		t.Fatal(err)
	}

	// a data reply
	res, err = parseEnv(t, s, data.FormatXML, data.OpReplyNetconf, action.Op,
		"<rpc-reply message-id=\"55\" xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\">\n"+
			"  <al xmlns=\"urn:tests:a\">25</al>\n"+
			"</rpc-reply>\n")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := envXML(t, res.Envelope, false),
		"<rpc-reply xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\" message-id=\"55\"/>\n"; got != want {
		t.Errorf("envelope:\n%s\nwant\n%s", got, want)
	}
	if res.Op != action.Op || res.Tree != action.Tree {
		t.Errorf("reply result %+v", res)
	}

	// an ok reply and an error reply print as they came
	for _, in := range []string{
		"<rpc-reply xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\" message-id=\"55\">\n" +
			"  <ok/>\n" +
			"</rpc-reply>\n",
		"<rpc-reply xmlns=\"urn:ietf:params:xml:ns:netconf:base:1.0\" message-id=\"55\">\n" +
			"  <rpc-error>\n" +
			"    <error-type>rpc</error-type>\n" +
			"    <error-tag>missing-attribute</error-tag>\n" +
			"    <error-severity>error</error-severity>\n" +
			"    <error-path xmlns:a=\"urn:tests:a\">/a:c/a:x</error-path>\n" +
			"    <error-info>\n" +
			"      <bad-attribute>message-id</bad-attribute>\n" +
			"      <bad-element>rpc</bad-element>\n" +
			"    </error-info>\n" +
			"  </rpc-error>\n" +
			"</rpc-reply>\n",
	} {
		res, err := parseEnv(t, s, data.FormatXML, data.OpReplyNetconf, action.Op, in)
		if err != nil {
			t.Fatal(err)
		}
		if got := envXML(t, res.Envelope, false); got != in {
			t.Errorf("envelope:\n%s\nwant\n%s", got, in)
		}
	}
}

// TestRestconfRPC is test_parser_xml.c and test_parser_json.c test_restconf_rpc.
func TestRestconfRPC(t *testing.T) {
	for _, tc := range []struct {
		f        data.Format
		in, want string
	}{{data.FormatXML,
		"<input xmlns=\"urn:ietf:params:xml:ns:yang:ietf-netconf-nmda\">" +
			"<datastore xmlns:ds=\"urn:ietf:params:xml:ns:yang:ietf-datastores\">ds:running</datastore>" +
			"<config>" +
			"<cp xmlns=\"urn:tests:a\"><z xmlns:nc=\"urn:ietf:params:xml:ns:netconf:base:1.0\" nc:operation=\"replace\"/></cp>" +
			"<l1 xmlns=\"urn:tests:a\" xmlns:nc=\"urn:ietf:params:xml:ns:netconf:base:1.0\" nc:operation=\"replace\">" +
			"<a>val_a</a><b>val_b</b><c>val_c</c>" +
			"</l1>" +
			"</config></input>",
		"<edit-data xmlns=\"urn:ietf:params:xml:ns:yang:ietf-netconf-nmda\">" +
			"<datastore xmlns:ds=\"urn:ietf:params:xml:ns:yang:ietf-datastores\">ds:running</datastore>" +
			"<config>" +
			"<cp xmlns=\"urn:tests:a\"><z xmlns:nc=\"urn:ietf:params:xml:ns:netconf:base:1.0\" nc:operation=\"replace\"/></cp>" +
			"<l1 xmlns=\"urn:tests:a\" xmlns:nc=\"urn:ietf:params:xml:ns:netconf:base:1.0\" nc:operation=\"replace\">" +
			"<a>val_a</a><b>val_b</b><c>val_c</c>" +
			"</l1>" +
			"</config></edit-data>"}, {data.FormatJSON,
		"{\"ietf-netconf-nmda:input\":{" +
			"\"datastore\":\"ietf-datastores:running\"," +
			"\"config\":{\"a:cp\":{\"z\":[null],\"@z\":{\"ietf-netconf:operation\":\"replace\"}}," +
			"\"a:l1\":[{\"@\":{\"ietf-netconf:operation\":\"replace\"},\"a\":\"val_a\",\"b\":\"val_b\",\"c\":\"val_c\"}]}" +
			"}}",
		"{\"ietf-netconf-nmda:edit-data\":{" +
			"\"datastore\":\"ietf-datastores:running\"," +
			"\"config\":{\"a:cp\":{\"z\":[null],\"@z\":{\"ietf-netconf:operation\":\"replace\"}}," +
			"\"a:l1\":[{\"@\":{\"ietf-netconf:operation\":\"replace\"},\"a\":\"val_a\",\"b\":\"val_b\",\"c\":\"val_c\"}]}" +
			"}}"}} {
		module := envModuleXML
		if tc.f == data.FormatJSON {
			module = envModuleJSON
		}
		s := envSchema(t, module, []string{"ietf-netconf-nmda"})
		// lyd_new_path of the rpc: Tree.NewPath creates no operation nodes, an empty request does
		req := request(t, s, `{"ietf-netconf-nmda:edit-data":{}}`)
		tree := req.Tree
		res, err := parseEnv(t, s, tc.f, data.OpRPCRestconf, req.Op, tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if env := envTop(t, res); env.Name() != "input" {
			t.Errorf("envelope %s", env.Name())
		}
		got := envXML(t, tree, true)
		if tc.f == data.FormatJSON {
			got = envJSON(t, tree, true)
		}
		if got != tc.want {
			t.Errorf("%v:\n%s\nwant\n%s", tc.f, got, tc.want)
		}
	}
}

// TestRestconfReply is test_parser_xml.c and test_parser_json.c test_restconf_reply.
func TestRestconfReply(t *testing.T) {
	for _, tc := range []struct {
		f        data.Format
		in, want string
	}{
		{data.FormatXML, `<output xmlns="urn:tests:a"><al>25</al></output>`, `<c xmlns="urn:tests:a"><act><al>25</al></act></c>`},
		{data.FormatJSON, `{"a:output":{"al":25}}`, `{"a:c":{"act":{"al":25}}}`},
	} {
		module := envModuleXML
		if tc.f == data.FormatJSON {
			module = envModuleJSON
		}
		s := envSchema(t, module)
		req := request(t, s, `{"a:c":{"act":{}}}`) // lyd_new_path "/a:c/act"
		tree := req.Tree
		res, err := parseEnv(t, s, tc.f, data.OpReplyRestconf, req.Op, tc.in)
		if err != nil {
			t.Fatal(err)
		}
		if env := envTop(t, res); env.Name() != "output" {
			t.Errorf("envelope %s", env.Name())
		}
		got := envXML(t, tree, true)
		if tc.f == data.FormatJSON {
			got = envJSON(t, tree, true)
		}
		if got != tc.want {
			t.Errorf("%v:\n%s\nwant\n%s", tc.f, got, tc.want)
		}
	}
}

// TestRestconfNotification is test_parser_json.c test_restconf_notification.
func TestRestconfNotification(t *testing.T) {
	s := envSchema(t, envModuleJSON)
	res, err := parseEnv(t, s, data.FormatJSON, data.OpNotifRestconf, nil,
		`{"ietf-restconf:notification":{"eventTime":"2013-12-21T00:01:00Z","a:c":{"n1":{"nl":"value"}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := envJSON(t, res.Envelope, true), `{"ietf-restconf:notification":{"eventTime":"2013-12-21T00:01:00Z"}}`; got != want {
		t.Errorf("envelope %s, want %s", got, want)
	}
	if got, want := envJSON(t, res.Tree, true), `{"a:c":{"n1":{"nl":"value"}}}`; got != want {
		t.Errorf("notification %s, want %s", got, want)
	}
	// wrong order
	if _, err := parseEnv(t, s, data.FormatJSON, data.OpNotifRestconf, nil,
		`{"ietf-restconf:notification":{"a:n2":{},"eventTime":"2013-12-21T00:01:00Z"}}`); err != nil {
		t.Fatal(err)
	}
	// unknown notification: the envelope stays
	res, err = parseEnv(t, s, data.FormatJSON, data.OpNotifRestconf, nil,
		`{"ietf-restconf:notification":{"eventTime":"2013-12-21T00:01:00Z","invalid:n2":{}}}`)
	if err == nil || res.Envelope == nil || res.Tree != nil {
		t.Fatalf("unknown notification: %v %+v", err, res)
	}
}

// TestEnvelopeErrors: the argument checks of lyd_parse_op for the protocol types and the
// envelope errors, with the envelope kept where libyang keeps it.
func TestEnvelopeErrors(t *testing.T) {
	s := envSchema(t, envModuleJSON)
	req := request(t, s, `{"a:c":{"act":{}}}`)
	act, c := req.Op, req.Op.Parent()
	for _, tc := range []struct {
		name string
		f    data.Format
		typ  data.OpType
		req  *data.Node
		in   string
		rc   string
		msg  string
		env  bool
	}{
		{"rpc json", data.FormatJSON, data.OpRPCNetconf, nil, `{}`, "LY_EINVAL", "Invalid argument format == LYD_XML (lyd_parse_op()).", false},
		{"notif with parent", data.FormatXML, data.OpNotifNetconf, act, `<a/>`, "LY_EINVAL", "Invalid argument !parent (lyd_parse_op()).", false},
		{"notif restconf xml", data.FormatXML, data.OpNotifRestconf, nil, `<a/>`, "LY_EINVAL", "Invalid argument format == LYD_JSON (lyd_parse_op()).", false},
		{"reply no parent", data.FormatXML, data.OpReplyNetconf, nil, `<a/>`, "LY_EINVAL", "Invalid argument parent (lyd_parse_op()).", false},
		{"input of a container", data.FormatJSON, data.OpRPCRestconf, c, `{}`, "LY_EINVAL",
			"Invalid argument parent->schema->nodetype & (LYS_RPC | LYS_ACTION) (lyd_parse_op()).", false},
		{"no rpc envelope", data.FormatXML, data.OpRPCNetconf, nil, `<rpc xmlns="urn:x"/>`, "LY_ENOT",
			"Missing NETCONF <rpc> envelope or in incorrect namespace.", false},
		{"no output envelope", data.FormatXML, data.OpReplyRestconf, act, `<output xmlns="urn:x"/>`, "LY_ENOT",
			"Missing RESTCONF \"output\" object or in incorrect namespace.", false},
		{"envelope value", data.FormatXML, data.OpNotifNetconf, nil,
			`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0">x</notification>`, "LY_EVALID",
			"Unexpected value \"x\" in the \"notification\" element.", false},
		{"empty reply", data.FormatXML, data.OpReplyNetconf, act,
			`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"/>`, "LY_EVALID", "Missing child elements of \"rpc-reply\".", false},
		{"ok and data", data.FormatXML, data.OpReplyNetconf, act,
			`<rpc-reply xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><ok/><al xmlns="urn:tests:a">1</al></rpc-reply>`,
			"LY_EVALID", "Unexpected sibling element \"al\" of \"ok\".", false},
		{"no eventTime", data.FormatXML, data.OpNotifNetconf, nil,
			`<notification xmlns="urn:ietf:params:xml:ns:netconf:notification:1.0"><n2 xmlns="urn:tests:a"/></notification>`,
			"LY_EVALID", "Missing notification \"eventTime\" node.", true},
		{"two actions", data.FormatXML, data.OpRPCNetconf, nil,
			`<rpc xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><r1 xmlns="urn:tests:a"/><r1 xmlns="urn:tests:a"/></rpc>`,
			"LY_EVALID", "Unexpected child element \"r1\".", true},
		{"empty object", data.FormatJSON, data.OpNotifRestconf, nil, `{}`, "LY_EVALID", "Empty JSON object.", false},
		{"wrong module", data.FormatJSON, data.OpReplyRestconf, act, `{"b:output":{}}`, "LY_EVALID",
			"Unexpected module \"b\" instead of \"a\".", false},
		{"wrong object", data.FormatJSON, data.OpRPCRestconf, act, `{"a:output":{}}`, "LY_EVALID",
			"Unexpected object \"output\" instead of \"input\".", false},
		{"metadata", data.FormatJSON, data.OpNotifRestconf, nil, `{"@ietf-restconf:notification":{}}`, "LY_EVALID",
			"Unexpected metadata.", false},
		{"no operation", data.FormatJSON, data.OpNotifRestconf, nil,
			`{"ietf-restconf:notification":{"eventTime":"2013-12-21T00:01:00Z"}}`, "LY_EVALID", "Missing the operation node.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := parseEnv(t, s, tc.f, tc.typ, tc.req, tc.in)
			wantDiag(t, err, tc.rc, tc.msg)
			if (res.Envelope != nil) != tc.env || res.Tree != nil || res.Op != nil {
				t.Errorf("partial result %+v", res)
			}
		})
	}
}

// TestRestconfNotificationMetadataFailure: when the metadata finish fails after the eventTime moved
// into the envelope, lyd_parse_op's cleanup frees the parsed eventTime: the envelope stays without it.
func TestRestconfNotificationMetadataFailure(t *testing.T) {
	s := envSchema(t, envModuleJSON)
	res, err := parseEnv(t, s, data.FormatJSON, data.OpNotifRestconf, nil,
		`{"ietf-restconf:notification":{"@a:n2":{"x:y":"v"},"a:n2":{},"eventTime":"2013-12-21T00:01:00Z"}}`)
	wantDiag(t, err, "LY_EVALID", `Unknown (or not implemented) YANG module "x" of metadata "x:y".`)
	if res.Tree != nil || res.Op != nil {
		t.Errorf("partial result %+v", res)
	}
	if got, want := envJSON(t, res.Envelope, true), `{"ietf-restconf:notification":""}`; got != want {
		t.Errorf("envelope %s, want %s", got, want)
	}
}
