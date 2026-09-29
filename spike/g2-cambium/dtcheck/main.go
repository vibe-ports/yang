// dtcheck: pure-Go datatree harness (no cgo). Usage: dtcheck data.{json,xml} mod1.yang [mod2.yang...]
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/signalbreak-labs/cambium/go/cambium"
	"github.com/signalbreak-labs/cambium/go/datatree"
)

func main() {
	b, err := cambium.NewContextBuilder(cambium.ContextFlags{})
	must("ctx", err)
	for _, p := range os.Args[2:] {
		must("load "+p, b.LoadModuleFromPath(p))
	}
	ctx, err := b.Build()
	must("build", err)
	data, err := os.ReadFile(os.Args[1])
	must("read", err)
	f := datatree.FormatJSONIETF
	if strings.HasSuffix(os.Args[1], ".xml") {
		f = datatree.FormatXML
	}
	tree, err := datatree.ParseModules(ctx.Modules(), f, data)
	if err != nil {
		fmt.Println("REJECT(parse):", err)
		os.Exit(1)
	}
	if err := tree.Validate(); err != nil {
		fmt.Println("REJECT(validate):", err)
		os.Exit(1)
	}
	tree.ApplyDefaults()
	out, _ := tree.Serialize(datatree.FormatJSONIETF)
	fmt.Println("OK", string(out))
}

func must(what string, err error) {
	if err != nil {
		fmt.Println("ERROR("+what+"):", err)
		os.Exit(2)
	}
}
