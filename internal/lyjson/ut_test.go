// SPDX-License-Identifier: BSD-3-Clause
// Ported from libyang v5.8.6 tests/utests/basic/test_json.c (BSD-3-Clause, © CESNET).

package lyjson

import (
	"errors"
	"testing"
)

/*
Not ported (no Go equivalent):
  - test_json.c:29,34,40,49,58,66,78-517,529,533,550-668,680-758:
    ly_in_new_memory/ly_in_memory return checks and ly_in_free; Go accepts a []byte directly.
  - test_json.c:46,55,64,84-516,538,559-666,689-756: lyjson_ctx_free; Go garbage collection.
  - test_json.c:83,93,103,113,122,132,141,150,159,168,177,186,195,204,213,
    223,232,241,250,259,268,277,286,295,305,314,323,332,341,350,359,368,
    377,386,396,406,416,425,435,444,572,578,597,609,631,640,646,698,717,
    726,741: jsonctx->dynamic; Go values are strings, not caller/allocated C buffers.

Input coverage: test_general: 5 inputs, all ported; test_number: 53 inputs, all ported;
test_string: 1 input, ported (the empty str at line 528 only creates the ly_in);
test_object: 5 inputs, all ported; test_array: 4 inputs, all ported.
*/

type utJSONDiag struct {
	msg  string
	line uint64
}

type utJSONState struct {
	status     Token
	value      string
	checkValue bool
	after      string
	checkAfter bool
}

type utJSONStep struct {
	state utJSONState
	diags []utJSONDiag
}

type utJSONCase struct {
	input    string
	initial  utJSONState
	newDiags []utJSONDiag
	steps    []utJSONStep
}

func utJSONCheckDiags(t *testing.T, err error, want []utJSONDiag) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var got *Error
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if len(got.Diags) != len(want) {
		t.Fatalf("diagnostics = %+v, want %+v", got.Diags, want)
	}
	for i := range want {
		if got.Diags[i].Msg != want[i].msg || got.Diags[i].Line != want[i].line {
			t.Fatalf("diagnostic %d = (%q, %d), want (%q, %d)", i,
				got.Diags[i].Msg, got.Diags[i].Line, want[i].msg, want[i].line)
		}
	}
}

func utJSONCheckState(t *testing.T, l *Lexer, want utJSONState) {
	t.Helper()
	if got := l.Status(); got != want.status {
		t.Fatalf("status = %v, want %v", got, want.status)
	}
	if want.checkValue {
		if got := l.Value(); got != want.value {
			t.Errorf("value = %q, want %q", got, want.value)
		}
		if got := len(l.Value()); got != len(want.value) {
			t.Errorf("value length = %d, want %d", got, len(want.value))
		}
	}
	if want.checkAfter {
		if got := l.AfterString(len(want.after)); got != want.after {
			t.Errorf("input after string = %q, want %q", got, want.after)
		}
	}
}

func utRunJSONCases(t *testing.T, cases []utJSONCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			l, err := New([]byte(tc.input))
			if len(tc.newDiags) > 0 {
				utJSONCheckDiags(t, err, tc.newDiags)
				return
			}
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			utJSONCheckState(t, l, tc.initial)
			for i, step := range tc.steps {
				_, err = l.Next()
				if len(step.diags) > 0 {
					utJSONCheckDiags(t, err, step.diags)
					return
				}
				if err != nil {
					t.Fatalf("Next() step %d error = %v", i+1, err)
				}
				utJSONCheckState(t, l, step.state)
			}
		})
	}
}

func TestUTGeneral(t *testing.T) {
	utRunJSONCases(t, []utJSONCase{
		{input: "", newDiags: []utJSONDiag{{"Empty JSON file.", 1}}},                                                            // test_json.c:28
		{input: "  \n\t \n", newDiags: []utJSONDiag{{"Empty JSON file.", 3}}},                                                   // test_json.c:33
		{input: "true", initial: utJSONState{status: TokenTrue}, steps: []utJSONStep{{state: utJSONState{status: TokenEnd}}}},   // test_json.c:39
		{input: "false", initial: utJSONState{status: TokenFalse}, steps: []utJSONStep{{state: utJSONState{status: TokenEnd}}}}, // test_json.c:48
		{input: "null", initial: utJSONState{status: TokenNull}, steps: []utJSONStep{{state: utJSONState{status: TokenEnd}}}},   // test_json.c:57
	})
}

func TestUTNumber(t *testing.T) {
	utRunJSONCases(t, []utJSONCase{
		{input: "11", initial: utJSONState{status: TokenNumber, value: "11", checkValue: true}},                                                 // test_json.c:77
		{input: "37.7668", initial: utJSONState{status: TokenNumber, value: "37.7668", checkValue: true}},                                       // test_json.c:87
		{input: "-122.3959", initial: utJSONState{status: TokenNumber, value: "-122.3959", checkValue: true}},                                   // test_json.c:97
		{input: "550E3", initial: utJSONState{status: TokenNumber, value: "550000", checkValue: true}},                                          // test_json.c:107
		{input: "-550E3", initial: utJSONState{status: TokenNumber, value: "-550000", checkValue: true}},                                        // test_json.c:116
		{input: "1E-1", initial: utJSONState{status: TokenNumber, value: "0.1", checkValue: true}},                                              // test_json.c:126
		{input: "15E-1", initial: utJSONState{status: TokenNumber, value: "1.5", checkValue: true}},                                             // test_json.c:135
		{input: "-15E-1", initial: utJSONState{status: TokenNumber, value: "-1.5", checkValue: true}},                                           // test_json.c:144
		{input: "16E-2", initial: utJSONState{status: TokenNumber, value: "0.16", checkValue: true}},                                            // test_json.c:153
		{input: "-16E-2", initial: utJSONState{status: TokenNumber, value: "-0.16", checkValue: true}},                                          // test_json.c:162
		{input: "17E-3", initial: utJSONState{status: TokenNumber, value: "0.017", checkValue: true}},                                           // test_json.c:171
		{input: "-17E-3", initial: utJSONState{status: TokenNumber, value: "-0.017", checkValue: true}},                                         // test_json.c:180
		{input: "21000E-2", initial: utJSONState{status: TokenNumber, value: "210", checkValue: true}},                                          // test_json.c:189
		{input: "21000E-4", initial: utJSONState{status: TokenNumber, value: "2.1", checkValue: true}},                                          // test_json.c:198
		{input: "21000E-7", initial: utJSONState{status: TokenNumber, value: "0.0021", checkValue: true}},                                       // test_json.c:207
		{input: "5.087E1", initial: utJSONState{status: TokenNumber, value: "50.87", checkValue: true}},                                         // test_json.c:217
		{input: "-5.087E1", initial: utJSONState{status: TokenNumber, value: "-50.87", checkValue: true}},                                       // test_json.c:226
		{input: "5.087E5", initial: utJSONState{status: TokenNumber, value: "508700", checkValue: true}},                                        // test_json.c:235
		{input: "59.1e+1", initial: utJSONState{status: TokenNumber, value: "591", checkValue: true}},                                           // test_json.c:244
		{input: "0.005087E1", initial: utJSONState{status: TokenNumber, value: "0.05087", checkValue: true}},                                    // test_json.c:253
		{input: "0.005087E2", initial: utJSONState{status: TokenNumber, value: "0.5087", checkValue: true}},                                     // test_json.c:262
		{input: "0.005087E6", initial: utJSONState{status: TokenNumber, value: "5087", checkValue: true}},                                       // test_json.c:271
		{input: "0.05087E6", initial: utJSONState{status: TokenNumber, value: "50870", checkValue: true}},                                       // test_json.c:280
		{input: "0.005087E8", initial: utJSONState{status: TokenNumber, value: "508700", checkValue: true}},                                     // test_json.c:289
		{input: "35.94e-1", initial: utJSONState{status: TokenNumber, value: "3.594", checkValue: true}},                                        // test_json.c:299
		{input: "-35.94e-1", initial: utJSONState{status: TokenNumber, value: "-3.594", checkValue: true}},                                      // test_json.c:308
		{input: "35.94e-2", initial: utJSONState{status: TokenNumber, value: "0.3594", checkValue: true}},                                       // test_json.c:317
		{input: "35.94e-3", initial: utJSONState{status: TokenNumber, value: "0.03594", checkValue: true}},                                      // test_json.c:326
		{input: "0.3594e-1", initial: utJSONState{status: TokenNumber, value: "0.03594", checkValue: true}},                                     // test_json.c:335
		{input: "0.03594e-1", initial: utJSONState{status: TokenNumber, value: "0.003594", checkValue: true}},                                   // test_json.c:344
		{input: "0.003594e-1", initial: utJSONState{status: TokenNumber, value: "0.0003594", checkValue: true}},                                 // test_json.c:353
		{input: "0.3594e-2", initial: utJSONState{status: TokenNumber, value: "0.003594", checkValue: true}},                                    // test_json.c:362
		{input: "0.03594e-2", initial: utJSONState{status: TokenNumber, value: "0.0003594", checkValue: true}},                                  // test_json.c:371
		{input: "0.003594e-2", initial: utJSONState{status: TokenNumber, value: "0.00003594", checkValue: true}},                                // test_json.c:380
		{input: "0", initial: utJSONState{status: TokenNumber, value: "0", checkValue: true}},                                                   // test_json.c:390
		{input: "-0", initial: utJSONState{status: TokenNumber, value: "-0", checkValue: true}},                                                 // test_json.c:399
		{input: "94E0", initial: utJSONState{status: TokenNumber, value: "94", checkValue: true}},                                               // test_json.c:409
		{input: "0E2", initial: utJSONState{status: TokenNumber, value: "0", checkValue: true}},                                                 // test_json.c:419
		{input: "-0E2", initial: utJSONState{status: TokenNumber, value: "-0", checkValue: true}},                                               // test_json.c:428
		{input: "5.320e+2", initial: utJSONState{status: TokenNumber, value: "532", checkValue: true}},                                          // test_json.c:438
		{input: "5.320e-1", initial: utJSONState{status: TokenNumber, value: "0.532", checkValue: true}},                                        // test_json.c:447
		{input: "-x", newDiags: []utJSONDiag{{`Invalid character in JSON Number value ("x").`, 1}}},                                             // test_json.c:457
		{input: "  -", newDiags: []utJSONDiag{{"Unexpected end-of-input.", 1}}},                                                                 // test_json.c:462
		{input: "--1", newDiags: []utJSONDiag{{`Invalid character in JSON Number value ("-").`, 1}}},                                            // test_json.c:467
		{input: "+1", newDiags: []utJSONDiag{{`Invalid character sequence "+1", expected a JSON value.`, 1}}},                                   // test_json.c:472
		{input: "  1.x ", newDiags: []utJSONDiag{{`Invalid character in JSON Number value ("x").`, 1}}},                                         // test_json.c:477
		{input: "1.", newDiags: []utJSONDiag{{"Unexpected end-of-input.", 1}}},                                                                  // test_json.c:482
		{input: "  1eo ", newDiags: []utJSONDiag{{`Invalid character in JSON Number value ("o").`, 1}}},                                         // test_json.c:487
		{input: "1e", newDiags: []utJSONDiag{{"Unexpected end-of-input.", 1}}},                                                                  // test_json.c:492
		{input: "1E1000", newDiags: []utJSONDiag{{"Number encoded as a string exceeded the LY_NUMBER_MAXLEN limit.", 1}}},                       // test_json.c:497
		{input: "1e9999999999999999999", newDiags: []utJSONDiag{{"Exponent out-of-bounds in a JSON Number value (1e9999999999999999999).", 1}}}, // test_json.c:502
		{input: "1.1e66000", newDiags: []utJSONDiag{{"Exponent out-of-bounds in a JSON Number value (1.1e66000).", 1}}},                         // test_json.c:507
		{input: "1.1e-66000", newDiags: []utJSONDiag{{"Exponent out-of-bounds in a JSON Number value (1.1e-66000).", 1}}},                       // test_json.c:512
	})
}

func TestUTString(t *testing.T) {
	utRunJSONCases(t, []utJSONCase{
		// CHECK_LOG_CTX reads and removes the LAST logged error (utests.h:1242-1257), so the C test
		// checks the two messages newest first; json.c:291-294 logs end-of-input, then the quote.
		{input: `"unterminated string`, newDiags: []utJSONDiag{
			{"Unexpected end-of-input.", 1},
			{"Missing quotation-mark at the end of a JSON string.", 1},
		}}, // test_json.c:532
	})
}

func TestUTObject(t *testing.T) {
	utRunJSONCases(t, []utJSONCase{
		{
			input: "  { }  ", initial: utJSONState{status: TokenObject},
			steps: []utJSONStep{{state: utJSONState{status: TokenObjectClosed}}, {state: utJSONState{status: TokenEnd}}},
		}, // test_json.c:549
		{
			input: `{"name" : "Radek"}`, initial: utJSONState{status: TokenObject},
			steps: []utJSONStep{
				{state: utJSONState{status: TokenObjectName, value: "name", checkValue: true, after: `" : "Radek"}`, checkAfter: true}},
				{state: utJSONState{status: TokenString, value: "Radek", checkValue: true, after: `"}`, checkAfter: true}},
				{state: utJSONState{status: TokenObjectClosed}},
				{state: utJSONState{status: TokenEnd}},
			},
		}, // test_json.c:562
		{
			input: `{"smart" : true,"handsom":false}`, initial: utJSONState{status: TokenObject},
			steps: []utJSONStep{
				{state: utJSONState{status: TokenObjectName, value: "smart", checkValue: true, after: `" : true,"handsom":false}`, checkAfter: true}},
				{state: utJSONState{status: TokenTrue}},
				{state: utJSONState{status: TokenObjectNext}},
				{state: utJSONState{status: TokenObjectName, value: "handsom", checkValue: true, after: `":false}`, checkAfter: true}},
				{state: utJSONState{status: TokenFalse}},
				{state: utJSONState{status: TokenObjectClosed}},
				{state: utJSONState{status: TokenEnd}},
			},
		}, // test_json.c:588
		{
			input: `{"person" : {"name":"Radek"}}`, initial: utJSONState{status: TokenObject},
			steps: []utJSONStep{
				{state: utJSONState{status: TokenObjectName, value: "person", checkValue: true, after: `" : {"name":"Radek"}}`, checkAfter: true}},
				{state: utJSONState{status: TokenObject}},
				{state: utJSONState{status: TokenObjectName, value: "name", checkValue: true, after: `":"Radek"}}`, checkAfter: true}},
				{state: utJSONState{status: TokenString, value: "Radek", checkValue: true, after: `"}}`, checkAfter: true}},
				{state: utJSONState{status: TokenObjectClosed}},
				{state: utJSONState{status: TokenObjectClosed}},
				{state: utJSONState{status: TokenEnd}},
			},
		}, // test_json.c:622
		{
			input: `{ unquoted : "data"}`, initial: utJSONState{status: TokenObject},
			steps: []utJSONStep{{diags: []utJSONDiag{{`Invalid character sequence "unquoted : "data"}", expected a JSON object name.`, 1}}}},
		}, // test_json.c:659
	})
}

func TestUTArray(t *testing.T) {
	utRunJSONCases(t, []utJSONCase{
		{
			input: "  [  ]  ", initial: utJSONState{status: TokenArray},
			steps: []utJSONStep{{state: utJSONState{status: TokenArrayClosed}}, {state: utJSONState{status: TokenEnd}}},
		}, // test_json.c:679
		{
			input: "[ null]", initial: utJSONState{status: TokenArray, value: "", checkValue: true},
			steps: []utJSONStep{
				{state: utJSONState{status: TokenNull}},
				{state: utJSONState{status: TokenArrayClosed}},
				{state: utJSONState{status: TokenEnd}},
			},
		}, // test_json.c:692
		{
			input: `[{"a":null},"x"]`, initial: utJSONState{status: TokenArray, value: "", checkValue: true},
			steps: []utJSONStep{
				{state: utJSONState{status: TokenObject}},
				{state: utJSONState{status: TokenObjectName, value: "a", checkValue: true, after: `":null},"x"]`, checkAfter: true}},
				{state: utJSONState{status: TokenNull}},
				{state: utJSONState{status: TokenObjectClosed}},
				{state: utJSONState{status: TokenArrayNext}},
				{state: utJSONState{status: TokenString, value: "x", checkValue: true, after: `"]`, checkAfter: true}},
				{state: utJSONState{status: TokenArrayClosed}},
				{state: utJSONState{status: TokenEnd}},
			},
		}, // test_json.c:711
		{
			input: "[ , null]", initial: utJSONState{status: TokenArray},
			steps: []utJSONStep{{diags: []utJSONDiag{{`Invalid character sequence ", null]", expected a JSON value.`, 1}}}},
		}, // test_json.c:751
	})
}
