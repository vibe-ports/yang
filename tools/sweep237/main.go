// SPDX-License-Identifier: BSD-3-Clause

// Command sweep237 writes deterministic cross-context diff fixtures for the
// vibe-ports/yang conformance corpus.
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
)

const (
	fixtureCount = 40
	seed         = 586
)

type childKind int

const (
	leafString childKind = iota
	leafListStringSystem
	leafListStringUser
	keyedList
	leafListInteger
)

type child struct {
	name  string
	kind  childKind
	typeA string
	typeB string
}

func main() {
	out := flag.String("out", ".", "repository root")
	flag.Parse()

	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // fixed seed is the fixture contract
	for i := 0; i < fixtureCount; i++ {
		if err := writeFixture(*out, i, rng); err != nil {
			fmt.Fprintf(os.Stderr, "sweep237: %v\n", err)
			os.Exit(1)
		}
	}
}

func writeFixture(root string, index int, rng *rand.Rand) error {
	suffix := fmt.Sprintf("%02d", index)
	module := "diff-sweep-" + suffix
	namespace := "urn:vibe-ports:yang:ut-diff:sweep:" + suffix

	children := randomChildren(rng, 3+rng.Intn(3))
	childrenB := append([]child(nil), children...)
	switch index % 3 {
	case 1:
		childrenB = append(childrenB, randomChild(rng, len(childrenB)))
	case 2:
		integerIndex := findInteger(children)
		if integerIndex < 0 {
			integerIndex = rng.Intn(len(children))
			children[integerIndex] = integerChild(children[integerIndex].name, rng)
			childrenB[integerIndex] = children[integerIndex]
		}
		if childrenB[integerIndex].typeA == "int8" {
			childrenB[integerIndex].typeB = "int16"
		} else {
			childrenB[integerIndex].typeB = "int8"
		}
	}

	schemaA := renderSchema(module, namespace, children, false)
	schemaB := renderSchema(module, namespace, childrenB, true)
	dataA := renderData(namespace, children, rng)
	dataB := renderData(namespace, childrenB, rng)
	manifest := renderManifest(suffix, module, dataA, dataB)

	files := map[string]string{
		filepath.Join(root, "conformance", "corpus", "manifest.d", "ut-diff", "sweep", "sweep-"+suffix+".yaml"):  manifest,
		filepath.Join(root, "conformance", "corpus", "ut-diff", "schemas", "sweep-"+suffix+"-a", module+".yang"): schemaA,
		filepath.Join(root, "conformance", "corpus", "ut-diff", "schemas", "sweep-"+suffix+"-b", module+".yang"): schemaB,
	}
	for name, content := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(name), err)
		}
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

func randomChildren(rng *rand.Rand, count int) []child {
	children := make([]child, count)
	for i := range children {
		children[i] = randomChild(rng, i)
	}
	return children
}

func randomChild(rng *rand.Rand, index int) child {
	c := child{name: fmt.Sprintf("c%d", index), kind: childKind(rng.Intn(5))}
	if c.kind == leafListInteger {
		return integerChild(c.name, rng)
	}
	return c
}

func integerChild(name string, rng *rand.Rand) child {
	typeName := "int8"
	if rng.Intn(2) == 1 {
		typeName = "int16"
	}
	return child{name: name, kind: leafListInteger, typeA: typeName, typeB: typeName}
}

func findInteger(children []child) int {
	for i := range children {
		if children[i].kind == leafListInteger {
			return i
		}
	}
	return -1
}

func renderSchema(module, namespace string, children []child, contextB bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// SPDX-License-Identifier: BSD-3-Clause\n")
	fmt.Fprintf(&b, "// Deterministically generated conformance fixture (this repo).\n")
	fmt.Fprintf(&b, "module %s {\n", module)
	fmt.Fprintf(&b, "  yang-version 1.1;\n")
	fmt.Fprintf(&b, "  namespace %q;\n", namespace)
	fmt.Fprintf(&b, "  prefix ds%s;\n\n", strings.TrimPrefix(module, "diff-sweep-"))
	fmt.Fprintf(&b, "  container top {\n")
	for _, c := range children {
		switch c.kind {
		case leafString:
			fmt.Fprintf(&b, "    leaf %s { type string; }\n", c.name)
		case leafListStringSystem:
			fmt.Fprintf(&b, "    leaf-list %s { type string; ordered-by system; }\n", c.name)
		case leafListStringUser:
			fmt.Fprintf(&b, "    leaf-list %s { type string; ordered-by user; }\n", c.name)
		case keyedList:
			fmt.Fprintf(&b, "    list %s {\n", c.name)
			fmt.Fprintf(&b, "      key \"key\";\n")
			fmt.Fprintf(&b, "      leaf key { type string; }\n")
			fmt.Fprintf(&b, "      leaf value { type string; }\n")
			fmt.Fprintf(&b, "    }\n")
		case leafListInteger:
			typeName := c.typeA
			if contextB {
				typeName = c.typeB
			}
			fmt.Fprintf(&b, "    leaf-list %s { type %s; ordered-by system; }\n", c.name, typeName)
		}
	}
	fmt.Fprintf(&b, "  }\n}\n")
	return b.String()
}

func renderData(namespace string, children []child, rng *rand.Rand) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<top xmlns=\"%s\">", namespace)
	for _, c := range children {
		switch c.kind {
		case leafString:
			if rng.Intn(2) == 1 {
				fmt.Fprintf(&b, "<%s>%s</%s>", c.name, randomValue(rng), c.name)
			}
		case leafListStringSystem, leafListStringUser:
			for _, value := range distinctValues(rng, rng.Intn(4), []string{"a", "b", "c", "d"}) {
				fmt.Fprintf(&b, "<%s>%s</%s>", c.name, value, c.name)
			}
		case keyedList:
			for _, key := range distinctValues(rng, rng.Intn(4), []string{"k0", "k1", "k2", "k3"}) {
				fmt.Fprintf(&b, "<%s><key>%s</key><value>%s</value></%s>", c.name, key, randomValue(rng), c.name)
			}
		case leafListInteger:
			values := []string{"-2", "-1", "0", "1"}
			for _, value := range distinctValues(rng, rng.Intn(4), values) {
				fmt.Fprintf(&b, "<%s>%s</%s>", c.name, value, c.name)
			}
		}
	}
	fmt.Fprintf(&b, "</top>")
	return b.String()
}

func distinctValues(rng *rand.Rand, count int, pool []string) []string {
	perm := rng.Perm(len(pool))
	values := make([]string, count)
	for i := range values {
		values[i] = pool[perm[i]]
	}
	return values
}

func randomValue(rng *rand.Rand) string {
	return []string{"a", "b", "c"}[rng.Intn(3)]
}

func renderManifest(suffix, module, dataA, dataB string) string {
	return fmt.Sprintf(`fixtures:
  - id: ut-diff/sweep/sweep-%s
    dir: ut-diff
    source: {url: "hand-written (this repo)", commit: null, license: BSD-3-Clause}
    # Expected behavior rests on libyang v5.8.6 src/diff.c:383-527,1225-1375 and
    # src/tree_data.c:503-592,712-730,778-835; the oracle golden decides the exact diff tree.
    rfc: []   # deterministic differential coverage of libyang diff mechanics, no RFC
    areas: [diff]
    request:
      op: sequence
      searchdirs: [schemas/sweep-%s-a]
      modules: [{name: %s}]
      steps:
        - {do: parse, format: xml, data_type: data, parse_only: true, data: '%s'}
        - do: diff
          format: xml
          data_type: data
          parse_only: true
          data: '%s'
          target: {searchdirs: [schemas/sweep-%s-b], modules: [{name: %s}]}
    golden: golden/sweep-%s.json
`, suffix, suffix, module, dataA, dataB, suffix, module, suffix)
}
