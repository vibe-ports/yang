// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package conformance

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzOracleRequestDifferential turns a short byte stream into a bounded protocol
// request. The first bytes select the operation, corpus schema, format and tree
// shape; the remaining bytes become data values or bounded raw parser input.
// Every input uses the request runner and deviation predicates of the request
// differential tests.
func FuzzOracleRequestDifferential(f *testing.F) {
	for op := byte(0); op < 7; op++ {
		for format := byte(0); format < 2; format++ {
			f.Add([]byte{op, 0, format, op % 6, 7, 'a'})
			f.Add([]byte{op, 1, format, op % 6, 255, 'b'})
		}
	}
	oracle, base := requestOracle(f)
	contexts := newInventoryContextCache()
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 256 {
			return
		}
		p, ok := fuzzOracleRequest(input)
		if !ok {
			return // Inline request strings cannot carry these bytes unchanged to both runners.
		}
		requestJSON, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		var canonical map[string]any
		if err := json.Unmarshal(requestJSON, &canonical); err != nil {
			t.Fatal(err)
		}
		want, nativeErr := inventoryNativeResponse(oracle, base, canonical)
		got, engineErr := inventoryRunWithContextCache(Request{BaseDir: base, Params: canonical}, contexts)
		category, class := inventoryCaseClassification(canonical, want, nativeErr, got, engineErr, "")
		if category == "MATCH" || category == "DEV" || category == "DEFERRED" {
			return
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		t.Fatalf("%s/%s request=%s\noracle=%s (%v)\nengine=%s (%v)",
			category, class, requestJSON, wantJSON, nativeErr, gotJSON, engineErr)
	})
}

func fuzzOracleRequest(input []byte) (map[string]any, bool) {
	var b [5]byte
	copy(b[:], input)
	module := "pv2"
	if b[1]&1 != 0 {
		module = "pv2-edit"
	}
	format := "json"
	if b[2]&1 != 0 {
		format = "xml"
	}
	value := ""
	if len(input) > len(b) {
		value = hex.EncodeToString(input[len(b):min(len(input), len(b)+32)])
	}
	first := fuzzOracleData(module, format, b[3], b[4], value)
	second := fuzzOracleData(module, format, b[3]+1, b[4]+1, value+"b")
	if b[3]&7 == 6 {
		first = string(input[min(len(input), len(b)):min(len(input), len(b)+128)])
	}
	if b[3]&7 == 7 {
		second = string(input[min(len(input), len(b)):min(len(input), len(b)+128)])
	}
	if !fuzzInlineDataValid(first) || (b[0]%7 == 3 || b[0]%7 == 4) && !fuzzInlineDataValid(second) {
		return nil, false
	}
	steps := []any{map[string]any{
		"do": "parse", "format": format, "data_type": "config", "parse_only": true, "data": first,
	}}
	switch b[0] % 7 {
	case 0: // parse
	case 1: // validate
		steps = append(steps, map[string]any{"do": "validate", "data_type": "config"})
	case 2: // print
		steps = append(steps, map[string]any{"do": "dump", "with_defaults": "all"})
	case 3: // diff
		steps = append(steps, map[string]any{"do": "diff", "format": format, "data_type": "config", "parse_only": true, "data": second})
	case 4: // merge
		steps = append(steps, map[string]any{"do": "edit", "format": format, "data_type": "config", "merge": second})
	case 5: // dup
		steps = append(steps, map[string]any{"do": "dup", "node": "/" + module + ":c", "options": []any{"recursive"}})
	case 6: // new_path
		path := "/pv2:c/u"
		if module == "pv2-edit" {
			path = "/pv2-edit:c/a"
		}
		steps = append(steps, map[string]any{"do": "edit", "set": map[string]any{"path": path, "value": value}})
	}
	if b[0]%7 != 2 {
		steps = append(steps, map[string]any{"do": "dump", "with_defaults": "explicit"})
	}
	return map[string]any{
		"op": "sequence", "base_dir": ".", "searchdirs": []any{"schemas"},
		"context_options": []any{"no_yanglibrary"},
		"modules":         []any{map[string]any{"name": module}}, "steps": steps,
	}, true
}

func fuzzInlineDataValid(s string) bool {
	// JSON replaces invalid UTF-8, and the native runner reads NUL-terminated C strings.
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func TestFuzzOracleRequestRawByteGate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []byte
		want  bool
	}{
		{"valid first", []byte{0, 1, 1, 6, 0, 'a'}, true},
		{"invalid UTF-8 first", []byte{0, 1, 1, 6, 0, 0xff}, false},
		{"invalid UTF-8 second", []byte{3, 1, 1, 7, 0, 0xff}, false},
		{"NUL first", []byte{0, 1, 1, 6, 0, 0}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := fuzzOracleRequest(tc.input)
			if ok != tc.want {
				t.Fatalf("request accepted = %t, want %t", ok, tc.want)
			}
		})
	}
}

func fuzzOracleData(module, format string, shape, number byte, value string) string {
	name, ns := "pv2", "urn:vibe-ports:yang:conformance:pv2"
	if module == "pv2-edit" {
		name, ns = "pv2-edit", "urn:vibe-ports:yang:conformance:pv2-edit"
	}
	if format == "json" {
		quoted, _ := json.Marshal(value)
		var body string
		if module == "pv2" {
			switch shape % 6 {
			case 0:
				body = fmt.Sprintf(`"mode":"on","x":%s`, quoted)
			case 1:
				body = fmt.Sprintf(`"mode":"off","x":%s`, quoted)
			case 2:
				body = fmt.Sprintf(`"u":%s,"dec":"%d"`, quoted, number)
			case 3:
				body = fmt.Sprintf(`"l":[{"k":%s}],"ref":%s`, quoted, quoted)
			case 4:
				body = fmt.Sprintf(`"pres":{"name":%s}`, quoted)
			default:
				body = fmt.Sprintf(`"unknown":%s`, quoted)
			}
		} else {
			switch shape % 6 {
			case 0, 1:
				body = fmt.Sprintf(`"a":%s,"b":%d`, quoted, number)
			case 2:
				body = fmt.Sprintf(`"l":[{"k":%s,"v":%d}]`, quoted, number)
			case 3:
				body = fmt.Sprintf(`"ll":[%s]`, quoted)
			case 4:
				body = fmt.Sprintf(`"np":{"d":%d}`, number)
			default:
				body = fmt.Sprintf(`"unknown":%s`, quoted)
			}
		}
		return fmt.Sprintf(`{"%s:c":{%s}}`, name, body)
	}
	var body string
	if module == "pv2" {
		switch shape % 6 {
		case 0:
			body = "<mode>on</mode><x>" + value + "</x>"
		case 1:
			body = "<mode>off</mode><x>" + value + "</x>"
		case 2:
			body = fmt.Sprintf("<u>%s</u><dec>%d</dec>", value, number)
		case 3:
			body = "<l><k>" + value + "</k></l><ref>" + value + "</ref>"
		case 4:
			body = "<pres><name>" + value + "</name></pres>"
		default:
			body = "<unknown>" + value + "</unknown>"
		}
	} else {
		switch shape % 6 {
		case 0, 1:
			body = fmt.Sprintf("<a>%s</a><b>%d</b>", value, number)
		case 2:
			body = fmt.Sprintf("<l><k>%s</k><v>%d</v></l>", value, number)
		case 3:
			body = "<ll>" + value + "</ll>"
		case 4:
			body = fmt.Sprintf("<np><d>%d</d></np>", number)
		default:
			body = "<unknown>" + value + "</unknown>"
		}
	}
	return fmt.Sprintf(`<c xmlns="%s">%s</c>`, ns, body)
}
