// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// fzModules are the modules of the public-API tests, examples and fuzz targets.
var fzModules = fstest.MapFS{
	"fz.yang": {Data: []byte(`module fz {
  yang-version 1.1;
  namespace "urn:fz";
  prefix fz;
  import ietf-yang-metadata { prefix md; }
  md:annotation ann { type string; }
  identity b;
  identity i1 { base b; }
  container c {
    must "count(l) < 50";
    leaf s { type string; }
    leaf n { type int32 { range "0..100"; } default 5; }
    leaf-list ll { type uint8; }
    list l {
      key k;
      unique v;
      leaf k { type string; }
      leaf v { type string; }
      leaf r { type leafref { path "../../s"; } }
    }
    leaf w { when "../s = 'x'"; type string; }
    leaf id { type identityref { base b; } }
    container st { config false; leaf z { type boolean; } }
  }
}`)},
	"fz2.yang": {Data: []byte(`module fz2 { namespace "urn:fz2"; prefix fz2; leaf t { type string; } }`)},
}

// fzContext is a context with fz loaded.
func fzContext(t testing.TB) *yang.Context {
	t.Helper()
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, fzModules)
	if err != nil {
		t.Fatal(err)
	}
	if diags, err := c.Load("fz", "", nil); err != nil {
		t.Fatal(err, diags)
	}
	return c
}

func parse(s *yang.Schema, in string, f data.Format, o data.ParseOptions) (*data.Tree, []yang.Diagnostic, error) {
	return data.Parse(context.Background(), strings.NewReader(in), f, s, o)
}

func printJSON(t testing.TB, tr *data.Tree) string {
	t.Helper()
	var b strings.Builder
	if err := tr.PrintJSON(&b, data.PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestParsePublic: Parse validates (implicit default, when, leafref) unless ParseOnly, Validate
// does it afterwards, ValidationError.RC names the LY_ERR, and a tree keeps its snapshot.
func TestParsePublic(t *testing.T) {
	c := fzContext(t)
	s := c.Schema()
	in := `{"fz:c": {"s": "x", "w": "y", "l": [{"k": "a", "r": "x"}]}}`
	tr, diags, err := parse(s, in, data.FormatJSON, data.ParseOptions{})
	if err != nil || len(diags) != 0 {
		t.Fatal(err, diags)
	}
	n, err := tr.Find("/fz:c/n")
	if err != nil || n == nil || n.Flags()&data.FlagDefault == 0 {
		t.Fatalf("implicit default: %v %v", n, err)
	}
	// parse-only accepts the dangling leafref; Validate reports it
	bad := `{"fz:c": {"s": "x", "l": [{"k": "a", "r": "nope"}]}}`
	tr, _, err = parse(s, bad, data.FormatJSON, data.ParseOptions{ParseOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	diags, err = tr.Validate(context.Background(), data.ValidateOptions{MultiError: true})
	var ve *data.ValidationError
	if !errors.As(err, &ve) || ve.RC() != "LY_EVALID" || len(diags) != 1 || diags[0].AppTag != "instance-required" {
		t.Fatalf("%v %v", err, diags)
	}
	// LY_EINVAL: the key refusal of Remove
	k, _ := tr.Find("/fz:c/l[k='a']/k")
	if err := k.Remove(); !errors.As(err, &ve) || ve.RC() != "LY_EINVAL" {
		t.Fatalf("key removal: %v", err)
	}
	// the tree keeps its snapshot: fz2 loaded later is unknown to it
	if _, err := c.Load("fz2", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.NewPath("/fz2:t", "v", data.NewPathOptions{}); err == nil {
		t.Fatal("fz2 visible to a tree of the older snapshot")
	}
	if _, _, err := parse(c.Schema(), `{"fz2:t": "v"}`, data.FormatJSON, data.ParseOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := parse(nil, "", data.FormatJSON, data.ParseOptions{}); err == nil {
		t.Fatal("nil schema")
	}
}

// TestWhenMetaRequeue: the JSON parser queues a node with a when once more per metadata member
// (libyang does too), and the when pass resumes from the node's first queue entry after a
// when-false subtree lost entries; the diagnostics are the oracle's (protocol-v2/when-meta-requeue).
func TestWhenMetaRequeue(t *testing.T) {
	const dir = "../conformance/corpus/protocol-v2"
	c, _, err := yang.NewContext(yang.Options{}, os.DirFS(filepath.Join(dir, "schemas")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load("pv2-meta", "", nil); err != nil {
		t.Fatal(err)
	}
	in, err := os.ReadFile(filepath.Join(dir, "data", "when-meta-requeue.json"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "golden", "when-meta-requeue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Diagnostics []struct {
			DataPath string `json:"data_path"`
		}
	}
	if err := json.Unmarshal(b, &golden); err != nil {
		t.Fatal(err)
	}
	var want, got []string
	for _, d := range golden.Diagnostics {
		want = append(want, d.DataPath)
	}
	o := data.ParseOptions{NoState: true, Validate: data.ValidateOptions{NoState: true, MultiError: true}}
	_, diags, err := parse(c.Schema(), string(in), data.FormatJSON, o)
	for _, d := range diags {
		got = append(got, d.DataPath)
	}
	if err == nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q (%v)", got, want, err)
	}
}

// TestConcurrentUse: one snapshot shared by goroutines that parse, validate, print and edit
// their own trees while the context loads another module (run under -race in CI).
func TestConcurrentUse(t *testing.T) {
	c := fzContext(t)
	s := c.Schema()
	const in = `{"fz:c": {"s": "x", "w": "y", "ll": [3, 1, 2], "l": [{"k": "a", "v": "1", "r": "x"}, {"k": "b", "v": "2"}]}}`
	var wg sync.WaitGroup
	errs := make(chan error, 9)
	for range 8 {
		wg.Go(func() {
			for range 20 {
				tr, _, err := parse(s, in, data.FormatJSON, data.ParseOptions{})
				if err != nil {
					errs <- err
					return
				}
				if _, err := tr.NewPath("/fz:c/l[k='c']/v", "3", data.NewPathOptions{}); err != nil {
					errs <- err
					return
				}
				if _, err := tr.Validate(context.Background(), data.ValidateOptions{}); err != nil {
					errs <- err
					return
				}
				var b strings.Builder
				if err := tr.PrintXML(&b, data.PrintOptions{WithDefaults: data.WDAllTagged}); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Go(func() {
		if _, err := c.Load("fz2", "", nil); err != nil {
			errs <- err
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// roundTrip is the round-trip property: a parsed tree printed and parsed again (JSON and XML)
// prints the same.
func roundTrip(t *testing.T, s *yang.Schema, tr *data.Tree) {
	t.Helper()
	first := printJSON(t, tr)
	again, _, err := parse(s, first, data.FormatJSON, data.ParseOptions{})
	if err != nil {
		t.Fatalf("reparse of\n%s: %v", first, err)
	}
	if got := printJSON(t, again); got != first {
		t.Fatalf("JSON round trip:\n%s\n%s", first, got)
	}
	var x strings.Builder
	if err := tr.PrintXML(&x, data.PrintOptions{}); err != nil {
		t.Fatal(err)
	}
	fromXML, _, err := parse(s, x.String(), data.FormatXML, data.ParseOptions{})
	if err != nil {
		t.Fatalf("reparse of\n%s: %v", x.String(), err)
	}
	if got := printJSON(t, fromXML); got != first {
		t.Fatalf("XML round trip:\n%s\n%s", first, got)
	}
}

var fuzzSchema = sync.OnceValue(func() *yang.Schema {
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, fzModules)
	if err == nil {
		_, err = c.Load("fz", "", nil)
	}
	if err != nil {
		panic(err)
	}
	return c.Schema()
})

// fuzzParse parses in with every unknown policy, never panicking; a valid result under Reject
// must round-trip.
func fuzzParse(t *testing.T, in string, f data.Format) {
	s := fuzzSchema()
	for _, u := range []data.UnknownPolicy{data.Reject, data.Skip, data.Opaque} {
		o := data.ParseOptions{Unknown: u, Validate: data.ValidateOptions{MultiError: true},
			Budget: data.Budget{MaxNodes: 1 << 12, MaxXPathSteps: 1 << 16}}
		tr, _, err := parse(s, in, f, o)
		if err == nil && u == data.Reject {
			roundTrip(t, s, tr)
		}
	}
}

func FuzzRoundTripJSON(f *testing.F) {
	for _, seed := range []string{
		`{"fz:c": {"s": "x", "w": "y", "ll": [3, 1], "l": [{"k": "a", "v": "1", "r": "x"}], "id": "fz:i1"}}`,
		`{"fz:c": {"n": 7, "@n": {"fz:ann": "a"}, "st": {"z": true}}}`,
		`{"fz:c": {"l": [{"k": "a", "v": "1"}, {"k": "b", "v": "1"}]}}`,
		`{"fz:c": {"bogus": [1, {"x": null}]}}`,
		`[`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) { fuzzParse(t, in, data.FormatJSON) })
}

func FuzzRoundTripXML(f *testing.F) {
	for _, seed := range []string{
		`<c xmlns="urn:fz"><s>x</s><w>y</w><ll>3</ll><ll>1</ll><l><k>a</k><v>1</v><r>x</r></l><id xmlns:f="urn:fz">f:i1</id></c>`,
		`<c xmlns="urn:fz" xmlns:fz="urn:fz"><n fz:ann="a">7</n><st><z>true</z></st></c>`,
		`<c xmlns="urn:fz"><bogus><x/></bogus></c>`,
		`<c xmlns="urn:fz"><![CDATA[x]]></c>`,
		`<!DOCTYPE c><c/>`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) { fuzzParse(t, in, data.FormatXML) })
}

func ExampleParse() {
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, fzModules)
	if err != nil {
		panic(err)
	}
	if _, err := c.Load("fz", "", nil); err != nil {
		panic(err)
	}
	in := `{"fz:c": {"s": "x", "l": [{"k": "a", "r": "x"}]}}`
	tree, _, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON, c.Schema(), data.ParseOptions{})
	if err != nil {
		panic(err)
	}
	_ = tree.PrintJSON(os.Stdout, data.PrintOptions{WithDefaults: data.WDAll})
	// Output:
	// {
	//   "fz:c": {
	//     "s": "x",
	//     "n": 5,
	//     "l": [
	//       {
	//         "k": "a",
	//         "r": "x"
	//       }
	//     ]
	//   }
	// }
}

func ExampleParse_invalid() {
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, fzModules)
	if err != nil {
		panic(err)
	}
	if _, err := c.Load("fz", "", nil); err != nil {
		panic(err)
	}
	in := `<c xmlns="urn:fz"><n>500</n><l><k>a</k><v>1</v></l><l><k>b</k><v>1</v></l></c>`
	o := data.ParseOptions{Validate: data.ValidateOptions{MultiError: true}}
	_, diags, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatXML, c.Schema(), o)
	var ve *data.ValidationError
	if errors.As(err, &ve) {
		fmt.Println(ve.RC())
	}
	for _, d := range diags {
		fmt.Println(d.Code, d.DataPath, d.Msg)
	}
	// Output:
	// LY_EVALID
	// LYVE_DATA /fz:c/n Unsatisfied range - value "500" is out of the allowed range.
	// LYVE_DATA /fz:c/l[k='b'] Unique data leaf(s) "v" not satisfied in "/fz:c/l[k='a']" and "/fz:c/l[k='b']".
}

func ExampleTree_Validate() {
	c, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, fzModules)
	if err != nil {
		panic(err)
	}
	if _, err := c.Load("fz", "", nil); err != nil {
		panic(err)
	}
	tree, _, err := data.Parse(context.Background(), strings.NewReader(`{"fz:c": {"s": "y", "w": "v"}}`),
		data.FormatJSON, c.Schema(), data.ParseOptions{ParseOnly: true})
	if err != nil {
		panic(err)
	}
	diags, err := tree.Validate(context.Background(), data.ValidateOptions{})
	fmt.Println(err != nil, diags[0].DataPath, diags[0].Msg)
	// Output:
	// true /fz:c/w When condition "../s = 'x'" not satisfied.
}
