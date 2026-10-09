// SPDX-License-Identifier: BSD-3-Clause

package data_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing/fstest"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/data"
)

// The README example: load a module, take the schema snapshot, parse and validate instance data,
// print it, and read the diagnostics of invalid data.
func Example() {
	models := fstest.MapFS{"example.yang": {Data: []byte(`module example {
  yang-version 1.1;
  namespace "urn:example";
  prefix ex;
  container system {
    leaf hostname { type string { pattern '[a-z][a-z0-9-]*'; } mandatory true; }
    leaf mtu { type uint16 { range "68..9000"; } default 1500; }
    list server {
      key name;
      leaf name { type string; }
      leaf port { type uint16; must ". != 0" { error-message "port 0 is reserved"; } }
    }
  }
}`)}} // or os.DirFS("models"), an embed.FS, ...

	// NoYangLibrary: without it, validating a datastore also requires the ietf-yang-library data.
	ctx, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, models)
	if err != nil {
		panic(err)
	}
	if _, err := ctx.Load("example", "", nil); err != nil {
		panic(err)
	}
	schema := ctx.Schema() // immutable snapshot, safe to share between goroutines

	in := `{"example:system": {"hostname": "edge-1", "server": [{"name": "ntp", "port": 123}]}}`
	tree, _, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON, schema, data.ParseOptions{})
	if err != nil {
		panic(err)
	}
	if err := tree.PrintXML(os.Stdout, data.PrintOptions{WithDefaults: data.WDAll}); err != nil {
		panic(err)
	}

	bad := `{"example:system": {"hostname": "Edge_1", "mtu": 20, "server": [{"name": "ntp", "port": 0}]}}`
	_, diags, err := data.Parse(context.Background(), strings.NewReader(bad), data.FormatJSON, schema,
		data.ParseOptions{Validate: data.ValidateOptions{MultiError: true}})
	fmt.Println("valid:", err == nil)
	for _, d := range diags {
		fmt.Println(d.Code, d.DataPath, d.Msg)
	}
	// Output:
	// <system xmlns="urn:example">
	//   <hostname>edge-1</hostname>
	//   <mtu>1500</mtu>
	//   <server>
	//     <name>ntp</name>
	//     <port>123</port>
	//   </server>
	// </system>
	// valid: false
	// LYVE_DATA /example:system/hostname Unsatisfied pattern - "Edge_1" does not match "[a-z][a-z0-9-]*".
	// LYVE_DATA /example:system/mtu Unsatisfied range - value "20" is out of the allowed range.
	// LYVE_DATA /example:system Mandatory node "hostname" instance does not exist.
	// LYVE_DATA /example:system/server[name='ntp']/port port 0 is reserved
}

// Diff: the changes between two trees as a tree annotated with yang:operation.
func ExampleDiff() {
	models := fstest.MapFS{"ex.yang": {Data: []byte(`module ex {
  namespace "urn:ex";
  prefix ex;
  container c {
    leaf a { type string; }
    leaf-list ll { type string; ordered-by user; }
  }
}`)}}
	ctx, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, models)
	if err != nil {
		panic(err)
	}
	if _, err := ctx.Load("ex", "", nil); err != nil {
		panic(err)
	}
	parse := func(in string) *data.Tree {
		t, _, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON, ctx.Schema(),
			data.ParseOptions{})
		if err != nil {
			panic(err)
		}
		return t
	}
	first := parse(`{"ex:c": {"a": "x", "ll": ["1", "2"]}}`)
	second := parse(`{"ex:c": {"a": "y", "ll": ["2", "1"]}}`)
	diff, err := data.Diff(first, second, data.DiffOptions{})
	if err != nil {
		panic(err)
	}
	if err := diff.PrintXML(os.Stdout, data.PrintOptions{WithDefaults: data.WDAll}); err != nil {
		panic(err)
	}
	// Output:
	// <c xmlns="urn:ex" xmlns:yang="urn:ietf:params:xml:ns:yang:1" yang:operation="none">
	//   <a yang:operation="replace" yang:orig-default="false" yang:orig-value="x">y</a>
	//   <ll yang:operation="replace" yang:orig-default="false" yang:orig-value="1" yang:value="">2</ll>
	// </c>
}

// ApplyDiff, ReverseDiff and MergeDiff: a diff applied with a callback, undone by its reverse,
// and two diffs merged into one.
func ExampleTree_ApplyDiff() {
	models := fstest.MapFS{"ex.yang": {Data: []byte(`module ex {
  namespace "urn:ex";
  prefix ex;
  container c {
    leaf a { type string; }
    leaf-list ll { type string; ordered-by user; }
  }
}`)}}
	ctx, _, err := yang.NewContext(yang.Options{NoYangLibrary: true}, models)
	if err != nil {
		panic(err)
	}
	if _, err := ctx.Load("ex", "", nil); err != nil {
		panic(err)
	}
	parse := func(in string) *data.Tree {
		t, _, err := data.Parse(context.Background(), strings.NewReader(in), data.FormatJSON, ctx.Schema(),
			data.ParseOptions{})
		if err != nil {
			panic(err)
		}
		return t
	}
	show := func(t *data.Tree) {
		if err := t.PrintJSON(os.Stdout, data.PrintOptions{}); err != nil {
			panic(err)
		}
	}
	first := parse(`{"ex:c": {"a": "x", "ll": ["1", "2"]}}`)
	second := parse(`{"ex:c": {"a": "y", "ll": ["2", "1", "3"]}}`)
	third := parse(`{"ex:c": {"a": "x"}}`)
	d12, _ := data.Diff(first, second, data.DiffOptions{})
	d23, _ := data.Diff(second, third, data.DiffOptions{})

	err = first.ApplyDiff(d12, data.ApplyDiffOptions{Callback: func(_, node *data.Node) error {
		fmt.Println("applied", node.Path())
		return nil
	}})
	if err != nil {
		panic(err)
	}
	show(first) // now equal to second

	back, _ := d12.ReverseDiff()
	if err := first.ApplyDiff(back, data.ApplyDiffOptions{}); err != nil {
		panic(err)
	}
	show(first) // the original first again

	if err := d12.MergeDiff(d23, data.MergeDiffOptions{}); err != nil {
		panic(err)
	}
	if err := first.ApplyDiff(d12, data.ApplyDiffOptions{}); err != nil {
		panic(err)
	}
	show(first) // equal to third
	// Output:
	// applied /ex:c
	// applied /ex:c/a
	// applied /ex:c/ll[.='2']
	// applied /ex:c/ll[.='3']
	// {
	//   "ex:c": {
	//     "a": "y",
	//     "ll": [
	//       "2",
	//       "1",
	//       "3"
	//     ]
	//   }
	// }
	// {
	//   "ex:c": {
	//     "a": "x",
	//     "ll": [
	//       "1",
	//       "2"
	//     ]
	//   }
	// }
	// {
	//   "ex:c": {
	//     "a": "x"
	//   }
	// }
}
