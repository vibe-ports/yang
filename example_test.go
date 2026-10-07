// SPDX-License-Identifier: BSD-3-Clause

package yang_test

import (
	"fmt"
	"testing/fstest"

	"github.com/vibe-ports/yang"
)

// modules is the module directory of the examples; os.DirFS or an embed.FS work the same way.
var modules = fstest.MapFS{"example.yang": {Data: []byte(`module example {
  yang-version 1.1;
  namespace "urn:example";
  prefix ex;
  feature tls;
  container system {
    leaf hostname { type string { pattern '[a-z][a-z0-9-]*'; } mandatory true; }
    leaf mtu { type uint16 { range "68..9000"; } default 1500; }
    list server {
      key name;
      leaf name { type string; }
      leaf port { type uint16; must ". != 0" { error-message "port 0 is reserved"; } }
      leaf tls { if-feature tls; type boolean; }
    }
  }
}`)}}

// Loading a module and walking its compiled schema.
func ExampleContext_Load() {
	ctx, _, err := yang.NewContext(yang.Options{}, modules)
	if err != nil {
		panic(err)
	}
	diags, err := ctx.Load("example", "", []string{"tls"})
	for _, d := range diags {
		fmt.Println(d.Code, d.SchemaPath, d.Msg)
	}
	if err != nil {
		panic(err)
	}
	mod := ctx.Schema().Implemented("example")
	fmt.Println(mod.Name(), mod.Namespace(), mod.FeatureEnabled("tls"))
	var walk func(n *yang.SchemaNode)
	walk = func(n *yang.SchemaNode) {
		line := fmt.Sprint(n.Kind(), " ", n.Path())
		if t := n.Type(); t != nil {
			line += " " + t.Base()
		}
		for d := range n.Defaults() {
			line += " default " + d
		}
		if n.Mandatory() {
			line += " mandatory"
		}
		fmt.Println(line)
		for c := range n.Children() {
			walk(c)
		}
	}
	for n := range mod.Top() {
		walk(n)
	}
	// Output:
	// example urn:example true
	// container /example:system mandatory
	// leaf /example:system/hostname string mandatory
	// leaf /example:system/mtu uint16 default 1500
	// list /example:system/server
	// leaf /example:system/server/name string
	// leaf /example:system/server/port uint16
	// leaf /example:system/server/tls boolean
}

// Looking up one schema node by its data path.
func ExampleSchema_FindSchema() {
	ctx, _, err := yang.NewContext(yang.Options{}, modules)
	if err != nil {
		panic(err)
	}
	if _, err := ctx.Load("example", "", nil); err != nil {
		panic(err)
	}
	port, err := ctx.Schema().FindSchema("/example:system/server/port")
	if err != nil {
		panic(err)
	}
	for m := range port.Musts() {
		fmt.Printf("%s: must %q (%s)\n", port.Path(), m.Expr(), m.ErrorMessage())
	}
	// The if-feature of tls is false: the node is not part of the schema.
	_, err = ctx.Schema().FindSchema("/example:system/server/tls")
	fmt.Println(err != nil)
	// Output:
	// /example:system/server/port: must ". != 0" (port 0 is reserved)
	// true
}
