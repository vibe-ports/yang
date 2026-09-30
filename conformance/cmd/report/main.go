// SPDX-License-Identifier: BSD-3-Clause

// Command report prints the per-area conformance report as Markdown.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/vibe-ports/yang/conformance"
)

func main() {
	engine := flag.String("engine", "replay", "engine to run: replay (returns the goldens)")
	manifest := flag.String("manifest", "corpus/manifest.yaml", "fixture manifest")
	flag.Parse()
	if err := do(*engine, *manifest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func do(engine, manifest string) error {
	m, err := conformance.LoadManifest(manifest)
	if err != nil {
		return err
	}
	if err := m.CheckFiles(true); err != nil {
		return err
	}
	if engine != "replay" {
		return fmt.Errorf("unknown engine %q", engine)
	}
	e, err := conformance.NewReplay(m)
	if err != nil {
		return err
	}
	rep, err := m.Compare(e)
	if err != nil {
		return err
	}
	fmt.Print(rep.Markdown())
	return nil
}
