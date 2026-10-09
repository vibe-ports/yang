// SPDX-License-Identifier: BSD-3-Clause

// Command manifest-split moves the inline fixtures of corpus/manifest.yaml into one fragment file
// each, corpus/manifest.d/<set>/<name>.yaml (conformance.SplitManifest), and proves the move: the
// decoded fixture set (ids, records, resolved golden and input paths) of the result must equal the
// current one, and splitting the result again must change nothing. -check proves it without
// writing. -since BASE moves only the lines appended to BASE (an earlier manifest.yaml, e.g. a
// PR's merge base: scripts/manifest-fragment-pr) and leaves manifest.yaml equal to BASE.
// Run from conformance/: scripts/manifest-split, scripts/manifest-fragment-pr.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/vibe-ports/yang/conformance"
)

func main() {
	corpus := flag.String("corpus", "corpus", "corpus directory holding manifest.yaml")
	check := flag.Bool("check", false, "prove the split without writing anything")
	since := flag.String("since", "", "move only the lines appended to this earlier manifest.yaml")
	flag.Parse()
	if err := do(*corpus, *since, *check); err != nil {
		fmt.Fprintln(os.Stderr, "manifest-split:", err)
		os.Exit(1)
	}
}

func do(corpus, since string, check bool) error {
	path := filepath.Join(corpus, "manifest.yaml")
	cur, err := os.ReadFile(path) //nolint:gosec // dev tool, caller-chosen path
	if err != nil {
		return err
	}
	before, err := conformance.LoadManifest(path)
	if err != nil {
		return err
	}
	man, frags, err := conformance.SplitManifest(cur, nil)
	if since != "" {
		base, rerr := os.ReadFile(since) //nolint:gosec // dev tool, caller-chosen path
		if rerr != nil {
			return rerr
		}
		if !bytes.HasPrefix(cur, base) {
			return fmt.Errorf("%s is not %s plus appended lines: convert the other changes by hand", path, since)
		}
		man = base
		_, frags, err = conformance.SplitManifest(append([]byte("fixtures:\n"), cur[len(base):]...), cur)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	after, err := conformance.SplitFS(os.DirFS(corpus), man, frags)
	if err != nil {
		return err
	}
	am, err := conformance.LoadManifestFS(after, "manifest.yaml", corpus)
	if err != nil {
		return fmt.Errorf("after the split: %w", err)
	}
	if err := conformance.SameFixtures(before, am); err != nil {
		return fmt.Errorf("decoded fixtures change:\n%w", err)
	}
	if since == "" {
		again, more, err := conformance.SplitManifest(man, nil)
		if err != nil {
			return fmt.Errorf("second split: %w", err)
		}
		if !bytes.Equal(again, man) || len(more) > 0 {
			return errors.New("not idempotent: a second split changes the result")
		}
	}
	digest, err := before.Digest()
	if err != nil {
		return err
	}
	fmt.Printf("manifest-split: %d fixtures, %d moved to %s; decoded set identical (digest %s)\n",
		len(before.Fixtures), len(frags), conformance.FragmentDir, digest)
	if check {
		return nil
	}

	for _, rel := range slices.Sorted(maps.Keys(frags)) {
		p := filepath.Join(corpus, conformance.FragmentDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil { //nolint:gosec // dev tool, id-derived path (fs.ValidPath)
			return err
		}
		if err := os.WriteFile(p, frags[rel], 0o600); err != nil { //nolint:gosec // as above
			return err
		}
	}
	if err := os.WriteFile(path, man, 0o600); err != nil { //nolint:gosec // dev tool, caller-chosen path
		return err
	}
	written, err := conformance.LoadManifest(path)
	if err != nil {
		return err
	}
	return conformance.SameFixtures(before, written)
}
