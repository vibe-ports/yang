// SPDX-License-Identifier: BSD-3-Clause

package conformance

import (
	"path/filepath"
	"strings"
	"testing"
)

type followup295Case struct {
	id      string
	params  map[string]any
	verdict string
}

func followup295Cases() []followup295Case {
	data := func(format, dataType, input string) map[string]any {
		search := "a.1"
		if format == "xml" {
			search = "a.2"
		}
		return map[string]any{
			"op": "data", "searchdirs": []any{search}, "modules": []any{map[string]any{"name": "a"}},
			"format": format, "data_type": dataType, "unknown": "reject", "parse_only": true, "data": input,
		}
	}
	cases := []followup295Case{
		{"ut-parser/xml-anydata-text-truncation",
			data("xml", "data", `<any xmlns="urn:tests:a">abcdefghijklmnopqrstuvwxyz</any>`), "invalid"},
		{"ut-parser/json-anydata-metadata-after-payload",
			data("json", "data", `{"a:any":{"a:foo":"x","@a:foo":{}}}`), "valid"},
		{"ut-parser/json-anyxml-metadata-after-payload",
			data("json", "data", `{"a:axml":{"a:foo":"x","@a:foo":{}}}`), "valid"},
		{"ops/eventtime-env-json-preceding-data",
			data("json", "notif-restconf", `{"ietf-restconf:notification":{"eventTime":"2013-12-21T00:01:00Z","a:foo":"x","a:n2":{}}}`), "valid"},
		{"ops/eventtime-env-json-retained-no-operation",
			data("json", "notif-restconf", `{"ietf-restconf:notification":{"eventTime":"2013-12-21T00:01:00Z"}}`), "invalid"},
		{"ops/eventtime-env-json-retained-metadata-error",
			data("json", "notif-restconf", `{"ietf-restconf:notification":{"@a:n2":{"x:y":"v"},"a:n2":{},"eventTime":"2013-12-21T00:01:00Z"}}`), "invalid"},
	}
	replyError := data("json", "reply-restconf", `{"a:output":{"al":"bad"}}`)
	replyError["rpc"] = `{"a:c":{"act":{}}}`
	cases = append(cases, followup295Case{"ut-parser/json-restconf-retained-reply-error", replyError, "invalid"})
	return cases
}

func runFollowup295(t *testing.T, tc followup295Case) Response {
	t.Helper()
	got, err := (Yang{}).Run(Request{ID: tc.id, BaseDir: filepath.Join("corpus", "ut-parser"), Params: tc.params})
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict() != tc.verdict {
		t.Fatalf("verdict = %q, want %q: %#v", got.Verdict(), tc.verdict, got)
	}
	return got
}

func TestFollowup295AnyParsers(t *testing.T) {
	cases := followup295Cases()
	for _, tc := range cases[:3] {
		t.Run(tc.id, func(t *testing.T) {
			got := runFollowup295(t, tc)
			if tc.id == "ut-parser/xml-anydata-text-truncation" {
				diags := got.Diagnostics()
				if len(diags) != 1 || diags[0]["msg"] != `Text value "abcdefghijklmnopqrst" inside an anydata node "any" found.` {
					t.Fatalf("diagnostics = %#v", diags)
				}
			}
		})
	}
}

func TestFollowup295EnvelopeObservers(t *testing.T) {
	cases := followup295Cases()
	preceding := runFollowup295(t, cases[3])
	opTyped, _ := preceding["op_typed"].([]any)
	if len(opTyped) != 1 || opTyped[0].(map[string]any)["path"] != "/a:n2" {
		t.Fatalf("op_typed = %#v, want only /a:n2", opTyped)
	}

	for _, tc := range cases[4:] {
		t.Run(tc.id, func(t *testing.T) {
			got := runFollowup295(t, tc)
			tree, ok := got["tree"].(map[string]any)
			if !ok || tree["json"] == nil {
				t.Fatalf("retained envelope tree = %#v", got["tree"])
			}
			if strings.Contains(tree["json"].(string), "eventTime") {
				t.Fatalf("eventTime survived failed operation cleanup: %s", tree["json"])
			}
			wantPath := "/notification"
			if tc.id == "ut-parser/json-restconf-retained-reply-error" {
				wantPath = "/a:output"
			}
			typed, _ := got["typed"].([]any)
			if len(typed) != 1 || typed[0].(map[string]any)["path"] != wantPath {
				t.Fatalf("retained envelope typed = %#v, want %s", typed, wantPath)
			}
		})
	}
}
