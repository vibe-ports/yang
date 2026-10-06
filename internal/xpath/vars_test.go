// SPDX-License-Identifier: BSD-3-Clause

package xpath

import (
	"errors"
	"reflect"
	"testing"
)

// TestVars: eval_variable_reference / lyxp_vars_find — the value is an expression evaluated in
// the reference's context, names match by prefix of the variable name, and with
// LYXP_SKIP_EXPR (lazy and/or, predicates of an empty node set) references are still looked up.
func TestVars(t *testing.T) {
	vars := []Var{{"abc", "1 + 2"}, {"s", "'x'"}, {"self", "$self"}, {"bad", "1 +"}, {"und", "$nope"}}
	cases := []struct {
		src  string
		want any // float64, string, bool, or the error message
	}{
		{"$abc * 2", 6.0},
		{"$ab", 3.0}, // strncmp over the reference's length
		{"$s", "x"},
		{"$abcd", `Variable "abcd" not defined.`},
		{"$x", `Variable "x" not defined.`},
		{"false() and $x", `Variable "x" not defined.`},
		{"true() or $x", `Variable "x" not defined.`},
		{"true() or $und", `Variable "nope" not defined.`},
		{"true() or $s", true},
		{"/nothing[$x]", `Variable "x" not defined.`},
		{"/nothing[$s]", "[]"},
		{"$self", "The maximum nesting of expressions has been exceeded."},
		{"true() or $self", "The maximum nesting of expressions has been exceeded."},
		{"$bad", "Unexpected XPath expression end."},
	}
	for _, tc := range cases {
		e, err := Compile(tc.src, jsonNS{})
		if err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		r, err := e.Eval(EvalContext{Vars: vars})
		var got any
		var xe *Error
		switch {
		case errors.As(err, &xe):
			got = xe.Msg
		case err != nil:
			t.Fatalf("%s: %v", tc.src, err)
		case r.Type == Number:
			got = r.Num
		case r.Type == String:
			got = r.Str
		case r.Type == Boolean:
			got = r.Bool
		default:
			got = "[]"
		}
		if got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.src, got, tc.want)
		}
	}
}

// TestSetVar: lyxp_vars_set replaces the value of the variable lyxp_vars_find finds (by prefix),
// else appends.
func TestSetVar(t *testing.T) {
	vars := SetVar(nil, "abc", "1")
	vars = SetVar(vars, "ab", "2") // finds "abc"
	vars = SetVar(vars, "x", "3")
	if want := []Var{{"abc", "2"}, {"x", "3"}}; !reflect.DeepEqual(vars, want) {
		t.Fatalf("%v", vars)
	}
}

// TestEvalTo: lyd_eval_xpath4's single-output casts; NodeSet casts nothing.
func TestEvalTo(t *testing.T) {
	e, err := Compile("1 + 1", jsonNS{})
	if err != nil {
		t.Fatal(err)
	}
	for to, want := range map[ResultType]Result{
		Boolean: {Type: Boolean, Bool: true},
		String:  {Type: String, Str: "2"},
		Number:  {Type: Number, Num: 2},
		NodeSet: {Type: Number, Num: 2},
	} {
		r, err := e.EvalTo(EvalContext{}, to)
		r.Steps = 0
		if err != nil || !reflect.DeepEqual(r, want) {
			t.Errorf("%d: %+v %v", to, r, err)
		}
	}
}
