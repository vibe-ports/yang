// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle_full

package conformance

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// Prelude dimensions belong to the root even when the subject is a step.
type pairwiseDimension struct {
	path []string
	row  inventoryRow
}

func (d pairwiseDimension) name() string { return strings.Join(d.path, "/") + "/" + d.row.key }
func pairwiseDimensions(seed inventorySeed) []pairwiseDimension {
	var out []pairwiseDimension
	seen := map[string]bool{}
	add := func(path []string, row inventoryRow) {
		d := pairwiseDimension{path, row}
		if !seen[d.name()] {
			out = append(out, d)
			seen[d.name()] = true
		}
	}
	for _, row := range inventoryRows(seed.scope) {
		add(seed.path, row)
	}
	for _, row := range inventoryRows("schema") {
		if inventoryContextKey(row.key) {
			add(nil, row)
		}
	}
	// Cross target contents with parent/options/data as well as each other.
	if seed.scope == "step:dup" || seed.scope == "step:diff" {
		for _, row := range requestInventory {
			if strings.Contains(" "+row.scopes+" ", " target:"+strings.TrimPrefix(seed.scope, "step:")+" ") {
				row.key = "target." + row.key
				add(seed.path, row)
			}
		}
	}
	slices.SortFunc(out, func(a, b pairwiseDimension) int { return strings.Compare(a.name(), b.name()) })
	return out
}

// Ordinary dimensions have four representatives. Path dimensions replace the generic
// malformed/valid pair with the six absolute/relative/whitespace found/missing/malformed classes.
// Malformed modules use a rejected schema, exercising the prelude's NULL-context path rather
// than only JSON typing.
func pairwiseValues(seed inventorySeed, d pairwiseDimension) []inventoryValue {
	local := seed
	local.path = d.path
	row := d.row
	if strings.HasPrefix(row.key, "target.") {
		local.params = inventoryClone(seed.params)
		subject := inventorySubject(local.params, local.path)
		if _, ok := subject["target"].(map[string]any); !ok {
			target := inventoryCommon("schema")
			delete(target, "op")
			subject["target"] = target
		}
		local.path = append(slices.Clone(local.path), "target")
		row.key = strings.TrimPrefix(row.key, "target.")
	}
	if inventoryPathKey(row) {
		return append([]inventoryValue{{"absent", nil}, {"null", nil}}, inventoryPathValues(row)...)
	}
	malformed := inventoryValue{"malformed", 42}
	valid := inventoryValue{"valid", "x"}
	for _, v := range inventoryValues(row, local) {
		if strings.HasPrefix(v.name, "valid-") || v.name == "seed-value" {
			valid = v
		}
	}
	switch row.kind {
	case "flags":
		malformed.value = []any{"bogus"}
		if slices.Contains(strings.Fields(row.valid), "output") {
			valid = inventoryValue{"valid-output", []any{"output"}}
		}
	case "bool", "truth":
		valid.value = true
	case "features":
		valid.value = []any{"*"}
	case "object":
		if valid.name == "valid" {
			valid.value = map[string]any{}
		}
	case "array":
		valid.value = []any{}
	}
	switch row.key {
	case "modules":
		malformed.value = []any{map[string]any{"name": "pv2-rejected"}}
	case "modules[].name":
		malformed.value = "pv2-rejected"
	case "target":
		target := inventoryCommon("schema")
		delete(target, "op")
		valid.value = target
	}
	// Prefer the live seed over generic path/module representatives.
	if value, ok := inventorySubject(local.params, local.path)[row.key]; ok {
		valid = inventoryValue{"seed-value", value}
	}
	return []inventoryValue{{"absent", nil}, {"null", nil}, malformed, valid}
}

func pairwiseSeeds() []inventorySeed {
	seeds := inventorySeeds()
	for _, op := range []string{"fewer", "more"} {
		p := inventoryCommon("sequence")
		p["modules"] = []any{map[string]any{"name": "pv2-output-keys"}}
		p["steps"] = []any{
			map[string]any{"do": "parse", "data_type": "rpc", "parse_only": true, "data": fmt.Sprintf(`{"pv2-output-keys:%s":{}}`, op)},
			map[string]any{"do": "edit", "insert_list": map[string]any{"parent": "/pv2-output-keys:" + op, "name": "item", "keys": []any{"one"}, "options": []any{"output"}}},
			map[string]any{"do": "dump"},
		}
		seeds = append(seeds, inventorySeed{"output-keys/" + op, "edit:insert_list", p, []string{"steps", "1", "insert_list"}, 0})
	}
	p := inventoryCommon("sequence")
	p["steps"] = []any{map[string]any{"do": "parse", "parse_only": true, "data": inventoryData}, map[string]any{"do": "parse", "data_type": "reply", "parse_only": true, "parent": "/pv2:c", "data": "{}"}}
	return append(seeds, inventorySeed{"reply/non-operation-parent", "step:parse", p, []string{"steps", "1"}, 0})
}

func pairwiseMutation(t *testing.T, seed inventorySeed, a, b pairwiseDimension, av, bv inventoryValue, base string) map[string]any {
	t.Helper()
	seed.params = inventoryClone(seed.params)
	seed.params["base_dir"] = "."
	for i, d := range []pairwiseDimension{a, b} {
		if strings.HasPrefix(d.row.key, "target.") {
			subject := inventorySubject(seed.params, d.path)
			if _, ok := subject["target"].(map[string]any); !ok {
				target := inventoryCommon("schema")
				delete(target, "op")
				subject["target"] = target
			}
		}
		local := seed
		local.path = d.path
		var companion string
		if i == 1 && slices.Equal(a.path, b.path) {
			if b.row.kind == "file" && a.row.key == strings.TrimSuffix(b.row.key, "_file") {
				companion = a.row.key
			}
			if strings.HasPrefix(b.row.key, "namespaces[].") && a.row.key == "xpath_format" {
				companion = a.row.key
			}
		}
		old, had := inventorySubject(seed.params, d.path)[companion]
		previous, present := seed.params["base_dir"]
		seed.params = inventoryMutation(t, local, d.row, []inventoryValue{av, bv}[i], base, false)
		if companion != "" {
			subject := inventorySubject(seed.params, d.path)
			delete(subject, companion)
			if had {
				subject[companion] = old
			}
		}
		// The single-field helper normalizes base_dir; preserve an earlier mutation.
		if d.row.key != "base_dir" || len(d.path) != 0 {
			delete(seed.params, "base_dir")
			if present {
				seed.params["base_dir"] = previous
			}
		}
	}
	return seed.params
}

// Authorize only the two field roots and the single-field helper's explicit
// companions (file/inline and namespace format), preserving setup/trailing
// steps. Ancestor/descendant pairs apply lexically; a child materializes its
// containing object if an earlier dimension removed it.
func pairwiseArtefact(seed inventorySeed, p map[string]any, ds ...pairwiseDimension) string {
	expected, actual := inventoryClone(seed.params), inventoryClone(p)
	expected["base_dir"] = "."
	for _, d := range ds {
		for _, q := range []map[string]any{expected, actual} {
			subject := inventorySubject(q, d.path)
			key := strings.TrimSuffix(strings.Split(d.row.key, ".")[0], "[]")
			delete(subject, key)
			if d.row.kind == "file" {
				delete(subject, strings.TrimSuffix(key, "_file"))
			}
			if strings.HasPrefix(key, "namespaces") {
				delete(subject, "xpath_format")
			}
		}
	}
	if !sameJSON(expected, actual) {
		return "PAIRWISE changed a field outside its two dimensions"
	}
	return ""
}

func TestRequestPairwiseInventory(t *testing.T) {
	total, pairs := 0, 0
	seeds := pairwiseSeeds()
	for _, seed := range seeds {
		n := len(pairwiseDimensions(seed))
		pairs += n * (n - 1) / 2
		dims := pairwiseDimensions(seed)
		for i, a := range dims {
			for _, b := range dims[i+1:] {
				total += len(pairwiseValues(seed, a)) * len(pairwiseValues(seed, b))
			}
		}
	}
	t.Logf("PAIRWISE: %d seeds, %d key pairs, %d requests", len(seeds), pairs, total)
}

func TestRequestPairwisePathCoverage(t *testing.T) {
	for _, seed := range pairwiseSeeds() {
		for _, d := range pairwiseDimensions(seed) {
			row := d.row
			row.key = strings.TrimPrefix(row.key, "target.")
			if !inventoryPathKey(row) {
				continue
			}
			got := map[string]bool{}
			for _, value := range pairwiseValues(seed, d) {
				got[value.name] = true
			}
			for _, name := range []string{"valid-abs", "spaced-abs", "valid-rel", "missing-abs", "missing-rel", "malformed"} {
				if !got[name] {
					t.Errorf("%s/%s lacks pairwise path class %s", seed.name, d.name(), name)
				}
			}
		}
	}
}

func TestRequestPairwiseDifferential(t *testing.T) {
	oracle, base := requestOracle(t)
	contexts := newInventoryContextCache()
	// Bounded batches, selectable by subtest name for native capture/replay.
	for _, seed := range pairwiseSeeds() {
		t.Run(seed.name, func(t *testing.T) {
			var cases []generatedCase
			dims := pairwiseDimensions(seed)
			for i, a := range dims {
				for _, b := range dims[i+1:] {
					for _, av := range pairwiseValues(seed, a) {
						for _, bv := range pairwiseValues(seed, b) {
							p := pairwiseMutation(t, seed, a, b, av, bv, base)
							cases = append(cases, generatedCase{seed: seed.name, key: a.name() + " + " + b.name(), value: av.name + " + " + bv.name, params: p, artefact: pairwiseArtefact(seed, p, a, b)})
						}
					}
				}
			}
			if path := os.Getenv("YANG_REQUEST_MISMATCHES"); path != "" {
				t.Setenv("YANG_REQUEST_MISMATCHES", path+"."+strings.ReplaceAll(seed.name, "/", "-"))
			}
			runInventoryCases(t, oracle, base, contexts, cases)
		})
	}
}

// The full isolation audit needs no oracle or response cache.
func TestRequestPairwiseIsolation(t *testing.T) {
	var total atomic.Int64
	t.Cleanup(func() {
		if !t.Failed() {
			t.Logf("PAIRWISE: %d requests, C=0", total.Load())
		}
	})
	for _, seed := range pairwiseSeeds() {
		t.Run(seed.name, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			dims := pairwiseDimensions(seed)
			for i, a := range dims {
				for _, b := range dims[i+1:] {
					for _, av := range pairwiseValues(seed, a) {
						for _, bv := range pairwiseValues(seed, b) {
							p := pairwiseMutation(t, seed, a, b, av, bv, base)
							if reason := pairwiseArtefact(seed, p, a, b); reason != "" {
								t.Fatalf("%s + %s (%s + %s): %s", a.name(), b.name(), av.name, bv.name, reason)
							}
							total.Add(1)
						}
					}
				}
			}
		})
	}
}
