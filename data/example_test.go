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
