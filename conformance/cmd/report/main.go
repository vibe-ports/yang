// SPDX-License-Identifier: BSD-3-Clause

// Command report prints the conformance report as Markdown: every fixture of the manifest run
// through an engine and compared with the libyang v5.8.6 goldens. `-engine yang` measures this
// repository (make compat-report); the default `replay` engine returns the goldens and only proves
// the plumbing.
//
// The report has the per-area counts (agree, agree once skipped fields are dropped, differ,
// deviation, unsupported), every differing and deviation fixture, the skipped fields and the
// unsupported fixtures per operation. -all lists every fixture that does not plainly agree.
package main

import (
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/vibe-ports/yang/conformance"
)

func main() {
	engine := flag.String("engine", "replay", "engine to run: replay (returns the goldens) or yang (this repository)")
	manifest := flag.String("manifest", "corpus/manifest.yaml", "fixture manifest")
	all := flag.Bool("all", false, "also list every fixture with skipped fields and every unsupported fixture")
	flag.Parse()
	if err := do(os.Stdout, *engine, *manifest, *all); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func do(w io.Writer, engine, manifest string, all bool) error {
	m, err := conformance.LoadManifest(manifest)
	if err != nil {
		return err
	}
	if err := m.CheckFiles(true); err != nil {
		return err
	}
	var e conformance.Engine
	switch engine {
	case "replay":
		if e, err = conformance.NewReplay(m); err != nil {
			return err
		}
	case "yang":
		e = conformance.Yang{}
	default:
		return fmt.Errorf("unknown engine %q", engine)
	}
	rep, err := m.Compare(e)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, render(engine, m.Fixtures, rep, all))
	return err
}

// render is the report; fixtures are the manifest's, in rep.Results order (Compare keeps it).
func render(engine string, fixtures []conformance.Fixture, rep conformance.Report, all bool) string {
	md := rep.Markdown()
	table, list, _ := strings.Cut(md, "\n\n")
	var b strings.Builder
	fmt.Fprintf(&b, "## Compatibility with libyang v5.8.6 (engine %s)\n\n%s\n", engine, table)

	var differ, dev []string
	var nSkipped, nUnsupported int
	skipped, unsupported := map[string]int{}, map[string]int{}
	for i, r := range rep.Results {
		switch r.Status {
		case conformance.Differ:
			d, _, _ := strings.Cut(r.Detail, "\n")
			if len(d) > 200 {
				d = d[:200] + "…"
			}
			differ = append(differ, fmt.Sprintf("- `%s`: %s", r.ID, d))
		case conformance.Deviation:
			dev = append(dev, fmt.Sprintf("- `%s`: %s", r.ID, r.Detail))
		case conformance.AgreeSkipped:
			nSkipped++
			for f := range strings.SplitSeq(strings.TrimPrefix(r.Detail, "skipped: "), ", ") {
				skipped[f]++
			}
		case conformance.Unsupported:
			nUnsupported++
			op, _ := fixtures[i].Request["op"].(string)
			unsupported[op]++
		}
	}
	section := func(title string, n int, lines []string) {
		fmt.Fprintf(&b, "\n### %s (%d)\n\n", title, n)
		if len(lines) == 0 {
			b.WriteString("none\n")
		}
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
	}
	counts := func(m map[string]int, format string) []string {
		var l []string
		for _, k := range slices.Sorted(maps.Keys(m)) {
			l = append(l, fmt.Sprintf(format, k, m[k]))
		}
		return l
	}
	section("Differ", len(differ), differ)
	section("Deviation: intentional, see conformance/deviations.md", len(dev), dev)
	section("Skipped fields: not produced by the engine, dropped from both sides", nSkipped, counts(skipped, "- `%s`: %d fixtures"))
	section("Unsupported fixtures per operation", nUnsupported, counts(unsupported, "- %s: %d"))
	if all {
		b.WriteString("\n### Every fixture that does not plainly agree\n\n" + strings.TrimSpace(list) + "\n")
	}
	return b.String()
}
