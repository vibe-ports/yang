// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/data"
)

// TestXPathFormats: the expression formats of XPathOptions (lyd_eval_xpath4's format,
// prefix_data and cur_mod) over fz, whose prefix is also "fz", and the argument refusals; the
// libyang comparison is the oracle fixtures xpath-format/*.
func TestXPathFormats(t *testing.T) {
	s := fzContext(t).Schema()
	tr, _, err := data.Parse(context.Background(), strings.NewReader(`{"fz:c": {"s": "x", "id": "fz:i1"}}`),
		data.FormatJSON, s, data.ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ns := []data.XPathNamespace{{Prefix: "f", URI: "urn:fz"}}
	for _, c := range []struct {
		expr string
		o    data.XPathOptions
		want string
	}{
		{"/f:c/f:s = 'x'", data.XPathOptions{Format: data.XPathXML, Namespaces: ns}, "true"},
		{"/f:c/f:id = 'f:i1'", data.XPathOptions{Format: data.XPathXML, Namespaces: ns}, "true"},
		{"/c/s", data.XPathOptions{Format: data.XPathXML, Namespaces: ns},
			`LY_EVALID | Non-prefixed node "c" in XML xpath found.`},
		{"derived-from(/c/id, 'b')", data.XPathOptions{Format: data.XPathSchema, Module: "fz"}, "true"},
		{"derived-from(/fz:c/id, 'b')", data.XPathOptions{Module: "fz"}, "true"}, // cur_mod
		{"/c", data.XPathOptions{Format: data.XPathSchema},
			"LY_EINVAL | Current module must be set if schema format is used."},
		{"/fz:c", data.XPathOptions{Module: "nope"},
			"LY_EINVAL | Invalid argument cur_mod (not implemented) (lyd_eval_xpath4())."},
		{"/fz:c", data.XPathOptions{Format: 9}, "LY_EINVAL | Invalid argument format (unknown) (lyd_eval_xpath4())."},
	} {
		r, _, err := tr.EvalXPathAs(c.expr, data.XPathBoolean, c.o)
		got := "false"
		if r.Boolean {
			got = "true"
		}
		var ve *data.ValidationError
		if errors.As(err, &ve) {
			got = ve.RC()
			for _, d := range ve.Diags {
				got += " | " + d.Msg
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s %+v:\n got %s\nwant %s", c.expr, c.o, got, c.want)
		}
	}
	if _, err := tr.TrimXPath("/fz:c", data.XPathOptions{Format: data.XPathXML}); err == nil {
		t.Error("TrimXPath took a format")
	}
}
