// SPDX-License-Identifier: BSD-3-Clause

package yang_test

import (
	"fmt"

	"github.com/vibe-ports/yang"
)

// exampleSchema is the compiled schema of the module of the examples.
func exampleSchema() *yang.Schema {
	ctx, _, err := yang.NewContext(yang.Options{}, modules)
	if err != nil {
		panic(err)
	}
	if _, err := ctx.Load("example", "", nil); err != nil {
		panic(err)
	}
	return ctx.Schema()
}

// The schema nodes an XPath expression depends on, for example to know which changes can
// alter its result.
func ExampleSchema_FindXPathAtoms() {
	s := exampleSchema()
	atoms, _, err := s.FindXPathAtoms(nil, "/example:system/server[port = 443]/name", yang.AtomOptions{})
	if err != nil {
		panic(err)
	}
	for _, n := range atoms {
		fmt.Println(n.Path())
	}
	// A step that matches nothing is a warning, not an error.
	_, diags, err := s.FindXPathAtoms(nil, "/example:system/nope", yang.AtomOptions{})
	fmt.Println(err, diags[0].Warning, diags[0].Msg)
	// Output:
	// /example:system
	// /example:system/server
	// /example:system/server/port
	// /example:system/server/name
	// <nil> true Schema node "nope" for parent "/example:system" not found; in expr "/example:system/nope" with context node "/".
}

// The schema nodes of a data path: each node of the path and the keys of its list predicates.
func ExampleSchema_FindPathAtoms() {
	s := exampleSchema()
	atoms, _, err := s.FindPathAtoms(nil, "/example:system/server[name='a']/port", yang.AtomOptions{})
	if err != nil {
		panic(err)
	}
	for _, n := range atoms {
		fmt.Println(n.Path())
	}
	// Output:
	// /example:system
	// /example:system/server
	// /example:system/server/name
	// /example:system/server/port
}

// NoMatchError turns a step that matches nothing into an error; the failing diagnostic carries
// libyang's return code.
func ExampleAtomOptions() {
	s := exampleSchema()
	_, diags, err := s.FindXPathAtoms(nil, "/example:system/nope", yang.AtomOptions{NoMatchError: true})
	fmt.Println(err != nil, diags[len(diags)-1].Err)
	// Output:
	// true LY_ENOTFOUND
}
