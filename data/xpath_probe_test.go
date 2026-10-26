// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/xpath"
)

// Native compares actual children positionally (tree_data.c:1769-1782), even
// when the parent's saved hash still describes the previously complete key set.
func TestXPathProbeRepeatedFirstKey(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	 grouping g { leaf p { type string; } leaf q { type string; } leaf z { type string; }
	 list l { key "a b"; ordered-by user;
	 leaf a { type string; } leaf b { type string; } leaf marker { type string; } } }
	 uses g; container r { uses g; }
	}`)
	for _, nested := range []bool{false, true} {
		for _, table := range []bool{false, true} {
			for _, duplicates := range []bool{false, true} {
				t.Run(fmt.Sprintf("nested=%t/table=%t/duplicates=%t", nested, table, duplicates), func(t *testing.T) {
					lists := `{"a":"x","b":"y","marker":"bad"}`
					if duplicates {
						lists += `,{"a":"x","b":"y","marker":"first"},{"a":"x","b":"y","marker":"second"}`
					}
					body, path := `"m:l":[`+lists+`]`, "/m:l"
					if table {
						body += `,"m:p":"p","m:q":"q","m:z":"z"`
					}
					if nested {
						body, path = `"m:r":{`+body+`}`, "/m:r/l"
					}
					tr, _, err := parseJSONString(set, `{`+body+`}`, Reject, true)
					if err != nil {
						t.Fatal(err)
					}
					first := findNode(t, tr, path+"[a='x'][b='y']")
					if _, err := tr.NewTerm(first, "a", "z", NewOptions{}); err != nil {
						t.Fatal(err)
					}
					got, d, err := tr.EvalXPath("string("+path+"[a='x'][b='y']/marker)", XPathOptions{})
					want := ""
					if duplicates {
						want = "first"
					}
					if err != nil || got.String != want {
						t.Fatalf("positional probe = %q, %v (%v), want %q", got.String, err, d, want)
					}
					control, _, err := tr.EvalXPath("count("+path+"[a=$key][b='y'])", XPathOptions{Vars: []XPathVar{{Name: "key", Value: "'x'"}}})
					if err != nil || control.Number != float64(1+2*boolInt(duplicates)) {
						t.Fatalf("ordinary predicate = %v, %v", control, err)
					}
				})
			}
		}
	}
}

// Dup maps the source leaf-list to a target key leaf by name, retaining its
// source hash. Parent hash/canonical strings alone cannot validate this probe.
func TestXPathProbeCopiedKeyHash(t *testing.T) {
	const source = `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	 grouping g { leaf p { type string; } leaf q { type string; } leaf z { type string; }
	 list l { key a; leaf a { type string; } leaf-list b { type string; } } }
	 uses g; container r { uses g; } }`
	target := strings.Replace(source, "key a; leaf a { type string; } leaf-list b", `key "a b"; leaf a { type string; } leaf b`, 1)
	srcSet, dstSet := opIndexSet(t, source), opIndexSet(t, target)
	for _, nested := range []bool{false, true} {
		for _, table := range []bool{false, true} {
			t.Run(fmt.Sprintf("nested=%t/table=%t", nested, table), func(t *testing.T) {
				body, path := `"m:l":[{"a":"x","b":["y"]}]`, "/m:l"
				if table {
					body += `,"m:p":"p","m:q":"q","m:z":"z"`
				}
				if nested {
					body, path = `"m:r":{`+body+`}`, "/m:r/l"
				}
				src, _, err := parseJSONString(srcSet, `{`+body+`}`, Reject, true)
				if err != nil {
					t.Fatal(err)
				}
				dst := newTree(dstSet)
				for n := range src.Top() {
					if _, _, err := dst.Dup(n, nil, DupOptions{Recursive: true}); err != nil {
						t.Fatal(err)
					}
				}
				got, d, err := dst.EvalXPath("count("+path+"[a='x'][b='y'])", XPathOptions{})
				if err != nil || got.Number != 0 {
					t.Fatalf("copied child hash = %v, %v (%v)", got, err, d)
				}
				control, _, err := dst.EvalXPath("count("+path+"[a=$key][b='y'])", XPathOptions{Vars: []XPathVar{{Name: "key", Value: "'x'"}}})
				if err != nil || control.Number != 1 {
					t.Fatalf("ordinary predicate = %v, %v", control, err)
				}
			})
		}
	}
}

func TestXPathProbeRegistrationOrder(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	 container r { leaf p { type string; } leaf q { type string; }
	 list l { key k; ordered-by user; leaf k { type string; } leaf v { type string; } } } }`)
	tr, _, err := parseJSONString(set, `{"m:r":{"p":"p","q":"q","l":[{"k":"a","v":"first"},{"k":"a","v":"second"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	first := findNode(t, tr, "/m:r/l[k='a']")
	for _, value := range []string{"b", "a"} {
		if _, err := first.kids.At(0).SetValue(value); err != nil {
			t.Fatal(err)
		}
	}
	if got := findNode(t, tr, "/m:r/l[k='a']/v").Value(); got != "second" {
		t.Fatalf("Find = %q", got)
	}
	got, d, err := tr.EvalXPath("string(/m:r/l[k='a']/v)", XPathOptions{})
	if err != nil || got.String != "second" {
		t.Fatalf("XPath = %v, %v (%v)", got, err, d)
	}
	if _, err := tr.NewPath("/m:r/l[k='a']/v", "updated", NewPathOptions{Update: true}); err != nil {
		t.Fatal(err)
	}
	if first.kids.At(1).Value() != "first" || findNode(t, tr, "/m:r/l[k='a']/v").Value() != "updated" {
		t.Fatal("NewPath updated the wrong duplicate")
	}
}

func TestXPathProbeStoreError(t *testing.T) {
	dir := "../conformance/corpus/ut-new/"
	original := corpusSet(t, []string{dir + "e.1", dir + "f.1"}, []string{"e", "f"})
	for _, nested := range []bool{false, true} {
		for _, table := range []bool{false, true} {
			t.Run(fmt.Sprintf("nested=%t/table=%t", nested, table), func(t *testing.T) {
				set := original
				input := `{"e:li":[{"id":"/f:lp[p='2001:0db8::/64']"}]}`
				path, value, schemaPath := "/e:li", "/f:lp[p='2001:0db8::/64']", "/e:li/id"
				if nested {
					set = opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
					 import ietf-inet-types { prefix inet; }
					 list lp { key p; leaf p { type inet:ipv6-prefix { pattern ".*0db8.*"; } } }
					 container r { leaf a { type string; } leaf b { type string; } leaf c { type string; }
					 list li { key id; leaf id { type instance-identifier { require-instance false; } } } } }`)
					padding := ""
					if table {
						padding = `"a":"a","b":"b","c":"c",`
					}
					input = `{"m:r":{` + padding + `"li":[{"id":"/m:lp[p='2001:0db8::/64']"}]}}`
					path, value, schemaPath = "/m:r/li", "/m:lp[p='2001:0db8::/64']", "/m:r/li/id"
				}
				tr, _, err := parseJSONString(set, input, Reject, true)
				if err != nil {
					t.Fatal(err)
				}
				if nested && (tr.top.first().kids.ht != nil) != table {
					t.Fatal("unexpected children table state")
				}
				_, d, err := tr.EvalXPath(`count(`+path+`[id="`+value+`"])`, XPathOptions{})
				var ve *ValidationError
				if !errors.As(err, &ve) || len(d) != 1 {
					t.Fatalf("re-store = %v, %v", err, d)
				}
				want := yang.Diagnostic{Err: "LY_EVALID", Code: "LYVE_DATA", SchemaPath: schemaPath,
					Msg: `Invalid instance-identifier "` + strings.Replace(value, "0db8", "db8", 1) + `" value - semantic error: Unsatisfied pattern - "2001:db8::/64" does not match ".*0db8.*".`}
				if d[0] != want || ve.RC() != "LY_EVALID" {
					t.Fatalf("diagnostic = %#v, want %#v", d[0], want)
				}
			})
		}
	}
}

func TestLeafrefProbeFailureIsMemberValidation(t *testing.T) {
	dir := "../conformance/corpus/"
	set := corpusSet(t, []string{dir + "protocol-v2/hash-leafref-probe", dir + "ut-new/e.1", dir + "ut-new/f.1"},
		[]string{"g", "e", "f"})
	const value = `/f:lp[p='2001:0db8::/64']`
	const prefix = `{"e:li":[{"id":"` + value + `"}],`
	parse := func(input string) (*Tree, []yang.Diagnostic, error) {
		return parseWith(context.Background(), strings.NewReader(input), set,
			parseOpts{ParseOptions: ParseOptions{Validate: ValidateOptions{Present: true}}}, parseJSON, nil)
	}
	tr, diags, err := parse(prefix + `"g:c":{"selector":"` + value + `","fallback":"` + value + `"}}`)
	if err != nil || len(diags) != 0 || tr == nil {
		t.Fatalf("union fallback: tree=%t diagnostics=%v error=%v", tr != nil, diags, err)
	}
	fallback := tr.top.At(1).kids.At(1)
	_, member := fallback.value.Union().Member()
	if got := fallback.Value(); got != value || member != 1 {
		t.Fatalf("fallback value = %q, selected member = %d", got, member)
	}
	_, diags, err = parse(prefix + `"g:c":{"selector":"` + value + `","strict":"` + value + `"}}`)
	var ve *ValidationError
	if !errors.As(err, &ve) || len(diags) < 2 {
		t.Fatalf("standalone leafref: diagnostics=%v error=%v", diags, err)
	}
	if diags[0].SchemaPath != "/e:li/id" || diags[0].Code != "LYVE_DATA" ||
		diags[len(diags)-1].AppTag != "instance-required" {
		t.Fatalf("standalone leafref diagnostics=%v", diags)
	}
}

// Predicate bytes and parent count vary independently and together. Result.Steps
// includes type storage, native hashing, digest construction, and bucket lookup;
// charging only one evaluator tick per parent would fail the byte-work assertion.
func TestXPathProbeMissWork(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	 list p { key id; leaf id { type uint32; } leaf a { type string; } leaf b { type string; }
	 list l { key k; leaf k { type string; } } } }`)
	measure := func(t *testing.T, parents, size int) int64 {
		t.Helper()
		var input strings.Builder
		input.WriteString(`{"m:p":[`)
		for i := range parents {
			if i > 0 {
				input.WriteByte(',')
			}
			fmt.Fprintf(&input, `{"id":%d,"a":"a","b":"b","l":[{"k":"present"}]}`, i)
		}
		input.WriteString(`]}`)
		tr, _, err := parseJSONString(set, input.String(), Reject, true)
		if err != nil {
			t.Fatal(err)
		}
		expr, err := xpath.Compile("/m:p/l[k='"+strings.Repeat("A", size)+"']", jsonNS{set: set})
		if err != nil {
			t.Fatal(err)
		}
		ec := xpath.EvalContext{Schema: info{set: set}}
		for n := range tr.Top() {
			if n.kids.ht == nil {
				t.Fatal("parent has no children table")
			}
			ec.Tree = append(ec.Tree, xn{n, set})
		}
		got, err := expr.Eval(ec)
		if err != nil || len(got.Nodes) != 0 {
			t.Fatalf("miss = %v, %v", got, err)
		}
		if got.Steps < int64(size) {
			t.Fatalf("%d predicate bytes only charged %d work", size, got.Steps)
		}
		ec.MaxSteps = size / 2
		if _, err := expr.Eval(ec); !errors.Is(err, xpath.ErrBudget) {
			t.Fatalf("predicate bytes escaped budget: %v", err)
		}
		return got.Steps
	}
	for _, mode := range []string{"parents", "bytes", "both"} {
		t.Run(mode, func(t *testing.T) {
			work := func(n int) int64 {
				p, size := 8, 1024
				if mode != "bytes" {
					p *= n
				}
				if mode != "parents" {
					size *= n
				}
				return measure(t, p, size)
			}
			for _, n := range []int{1, 4, 16} {
				a, b := work(n), work(4*n)
				t.Logf("N=%d: work(N)=%d, work(4N)=%d", n, a, b)
				if b > 6*a {
					t.Fatalf("work(4N)=%d > 6*work(N)=%d", b, 6*a)
				}
			}
		})
	}
	measure(t, 32, 1<<20)
}

// Force both hash discriminators to collide without searching SHA256's space.
// Even then an indexed lookup must verify the actual positional child values.
func TestXPathProbeBucketCollision(t *testing.T) {
	set := opIndexSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
	 container r { leaf p { type string; } leaf q { type string; } leaf z { type string; }
	 list l { key k; leaf k { type string; } } } }`)
	tr, _, err := parseJSONString(set, `{"m:r":{"p":"p","q":"q","z":"z","l":[{"k":"wrong"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _, err := parseJSONString(set, `{"m:r":{"l":[{"k":"wanted"}]}}`, Reject, true)
	if err != nil {
		t.Fatal(err)
	}
	root := tr.top.first()
	wrong := findNode(t, tr, "/m:r/l[k='wrong']")
	wanted := findNode(t, fresh, "/m:r/l[k='wanted']")
	oldKey, _ := hashOf(wrong)
	collisionKey, _ := hashOf(wanted)
	root.kids.ht[collisionKey] = root.kids.ht[oldKey]
	delete(root.kids.ht, oldKey)
	wrong.hash = wanted.hash
	got, d, err := tr.EvalXPath("count(/m:r/l[k='wanted'])", XPathOptions{})
	if err != nil || got.Number != 0 {
		t.Fatalf("collision returned a different value: %v, %v (%v)", got, err, d)
	}
}

// lyd_leafref_link_node_tree_type: a predicate key re-store failure inside
// lyplg_type_resolve_leafref is logged by lyxp_eval, the link is skipped and
// linking goes on (the plain and the union leafref alike).
func TestLinkLeafrefsProbeFailureSkipsLink(t *testing.T) {
	dir := "../conformance/corpus/"
	set := corpusSet(t, []string{dir + "protocol-v2/hash-leafref-probe", dir + "ut-new/e.1", dir + "ut-new/f.1"},
		[]string{"g", "e", "f"})
	set.LeafrefLinking = true
	const value = `/f:lp[p='2001:0db8::/64']`
	tr, _, err := parseWith(context.Background(), strings.NewReader(`{"e:li":[{"id":"`+value+`"}],"g:c":{"selector":"`+
		value+`","strict":"`+value+`","fallback":"`+value+`"}}`), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	diags, err := tr.LinkLeafrefs(context.Background())
	if err != nil || len(diags) != 2 {
		t.Fatalf("LinkLeafrefs = %v, %v; want the two probe diagnostics and success", diags, err)
	}
	for _, d := range diags {
		if d.SchemaPath != "/e:li/id" || d.Code != "LYVE_DATA" {
			t.Fatalf("diagnostic = %#v", d)
		}
	}
	for n := range tr.top.At(1).All() {
		if l, tg := n.LeafrefLinks(); len(l)+len(tg) != 0 {
			t.Fatalf("%s linked: %v %v", n.Path(), l, tg)
		}
	}
}

// xpath_deref_type: a union's leafref member whose predicate key re-store fails
// does not resolve, so the next member is tried (a string resolves to nothing).
func TestDerefProbeFailureTriesNextMember(t *testing.T) {
	dir := "../conformance/corpus/"
	set := corpusSet(t, []string{dir + "protocol-v2/hash-leafref-probe", dir + "ut-new/e.1", dir + "ut-new/f.1"},
		[]string{"g", "e", "f"})
	const value = `/f:lp[p='2001:0db8::/64']`
	tr, _, err := parseWith(context.Background(), strings.NewReader(`{"e:li":[{"id":"`+value+`"}],"g:c":{"selector":"`+
		value+`","strict":"`+value+`","fallback":"`+value+`"}}`), set,
		parseOpts{ParseOptions: ParseOptions{ParseOnly: true}}, parseJSON, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, diags, err := tr.EvalXPath("count(deref(/g:c/fallback))", XPathOptions{})
	if err != nil || got.Number != 0 {
		t.Fatalf("deref(fallback) = %v, %v, %v", got, diags, err)
	}
	// a plain leafref: lyxp_eval's store diagnostic, then the plugin message naming it (LOGERR)
	_, diags, err = tr.EvalXPath("count(deref(/g:c/strict))", XPathOptions{})
	if err == nil || len(diags) != 2 || diags[0].SchemaPath != "/e:li/id" || diags[1].Err != "LY_EINVAL" ||
		!strings.Contains(diags[1].Msg, "XPath evaluation error (Invalid instance-identifier") {
		t.Fatalf("deref(strict) = %v, %v", diags, err)
	}
}
