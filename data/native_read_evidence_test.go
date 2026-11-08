// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// LOGINT and argument-check messages include a build path and source line.
// Only that location is variable; message, argument, code, path, app-tag, log
// level and every other diagnostic field remain evidence.
var nativeG2Location = regexp.MustCompile(`^(Internal error) \([^()]*\.(?:c|h):[0-9]+\)(\.)$`)

// Observer wrapper names are the exact wrap() inventory in instrument.py.
var nativeG2ObserverFunction = regexp.MustCompile(`^(Invalid argument .+ \()(lyd_insert_hash|lyd_unlink_hash|lyd_insert_hash_add|lyht_resize|_lyht_insert_with_resize_cb|lyht_remove_with_resize_cb|lyht_find_next_with_collision_cb|lyd_insert_after_node|lyd_insert_before_node|lyd_unlink_ignore_lyds|lyd_insert_only_child|lyd_insert_node_last|lyds_move_meta|lyds_merge_nodes2_front|lyds_merge_nodes2_among|lyds_merge_nodes2_back|lyd_validate_duplicates)_nr_impl(\(\)\)\.)$`)

func nativeG2CompareEvidence(script string, left, right []nativeDriverResult) error {
	a, err := nativeG2Evidence(script, left)
	if err != nil {
		return err
	}
	b, err := nativeG2Evidence(script, right)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(a, b) {
		return fmt.Errorf("native G2 evidence differs:\ninstrumented=%v\nunmodified=%v", a, b)
	}
	return nil
}

// Shared with validation-only traces: G2 adds its nonempty read-result contract below.
type nativeCommandEvidence struct {
	identity   nativeIdentityEvidence
	snapshots  map[int]map[string]any
	projection []any
}

func nativeCompletedCommandEvidence(script string, rows []nativeDriverResult) (nativeCommandEvidence, error) {
	if err := nativeCompletedReplay(script, rows, ""); err != nil {
		return nativeCommandEvidence{}, err
	}
	var full []json.RawMessage
	if len(rows) > 0 && len(rows[0].bootstrap) > 0 {
		full = append(full, rows[0].bootstrap)
	}
	for _, row := range rows {
		b, err := json.Marshal(row.raw)
		if err != nil {
			return nativeCommandEvidence{}, err
		}
		full = append(full, b)
	}
	identity, err := nativeIdentityProjection(script, full)
	if err != nil {
		return nativeCommandEvidence{}, err
	}
	result := nativeCommandEvidence{identity: identity, snapshots: map[int]map[string]any{}, projection: append([]any(nil), identity.States...)}
	var commands []string
	for _, line := range strings.Split(script, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && !strings.HasPrefix(fields[0], "#") {
			commands = append(commands, fields[0])
		}
	}
	for _, row := range rows {
		raw := row.raw
		if raw == nil {
			return nativeCommandEvidence{}, fmt.Errorf("missing raw evidence")
		}
		if row.Operation <= 0 || row.Operation > len(commands) || row.Event != commands[row.Operation-1] {
			continue
		}
		if raw["rc"] == nil {
			return nativeCommandEvidence{}, fmt.Errorf("missing command status")
		}
		result.snapshots[row.Operation] = raw
		// Project every completed command, independently of snapshot discovery.
		diagnostics, ok := raw["diagnostics"].([]any)
		if !ok {
			return nativeCommandEvidence{}, fmt.Errorf("missing diagnostics at %d", row.Operation)
		}
		logs, ok := raw["logs"].([]any)
		if !ok {
			return nativeCommandEvidence{}, fmt.Errorf("missing logs at %d", row.Operation)
		}
		normalizeMessages := func(items []any) []any {
			result := make([]any, len(items))
			for i, item := range items {
				record := map[string]any{}
				for k, v := range item.(map[string]any) {
					if k == "msg" {
						v = nativeG2Location.ReplaceAllString(v.(string), "$1$2")
						v = nativeG2ObserverFunction.ReplaceAllString(v.(string), "$1$2$3")
					}
					record[k] = v
				}
				result[i] = record
			}
			return result
		}
		result.projection = append(result.projection, []any{row.Operation, row.Event, row.RC, normalizeMessages(diagnostics), normalizeMessages(logs)})
	}
	return result, nil
}

func nativeG2Evidence(script string, rows []nativeDriverResult) ([]any, error) {
	command, err := nativeCompletedCommandEvidence(script, rows)
	if err != nil {
		return nil, err
	}
	identity, snapshots, out := command.identity, command.snapshots, command.projection
	for _, row := range rows {
		raw := row.raw
		if row.Event != "read.equal" && !strings.HasPrefix(row.Event, "read.selection.") {
			continue
		}
		snapshot := snapshots[row.Operation]
		if snapshot == nil || raw["rc"] != snapshot["rc"] {
			return nil, fmt.Errorf("missing result snapshot at %d", row.Operation)
		}
		nodes, ok := snapshot["nodes"].([]any)
		if !ok {
			return nil, fmt.Errorf("missing identity registry")
		}
		registry := map[float64]map[string]any{}
		for _, item := range nodes {
			n := item.(map[string]any)
			g, ok := n["generation"].(float64)
			if !ok || g == 0 || registry[g] != nil {
				return nil, fmt.Errorf("invalid allocation identity")
			}
			registry[g] = n
		}
		// Never compare process-local generation numbers, nor trust script columns
		// without resolving their corresponding generation in this snapshot.
		resolve := func(value any) (any, error) {
			g, ok := value.(float64)
			if !ok {
				return nil, fmt.Errorf("missing generation")
			}
			if g == 0 {
				return float64(0), nil
			}
			n := registry[g]
			id := identity.Refs[row.Operation][int(g)]
			if n == nil || n["alive"] != true || id == nil {
				return nil, fmt.Errorf("unbound/retired generation %v", g)
			}
			return id, nil
		}
		result := map[string]any{"operation": row.Operation, "event": row.Event, "rc": row.RC}
		if row.Event != "read.equal" {
			for field, scriptField := range map[string]string{"winner": "winner_script", "current_parent": "parent_script", "next_generation": "next_script"} {
				id, err := resolve(raw[field])
				if err != nil {
					return nil, err
				}
				g := raw[field].(float64)
				label := any(float64(0))
				if g != 0 {
					label = registry[g]["id"]
				}
				if label != raw[scriptField] {
					return nil, fmt.Errorf("%s disagrees with script identity", field)
				}
				result[field] = id
			}
			// Normalization drops only observer serials and maps node generations
			// through script identities. Native table layout and links stay exact.
			for _, side := range []string{"before", "after"} {
				b, err := json.Marshal(raw[side])
				if err != nil {
					return nil, err
				}
				var table map[string]any
				if err = json.Unmarshal(b, &table); err != nil || table == nil {
					return nil, fmt.Errorf("missing %s table", side)
				}
				if _, ok := table["present"].(bool); !ok {
					return nil, fmt.Errorf("missing table presence")
				}
				if table["present"] == true {
					// A table's process identity is local; before/after must still agree.
					if raw["before"].(map[string]any)["identity"] != raw["after"].(map[string]any)["identity"] {
						return nil, fmt.Errorf("read replaced queried table")
					}
					delete(table, "identity")
					delete(table, "continuity")
					chains, ok := table["chains"].([]any)
					if !ok {
						return nil, fmt.Errorf("missing chains")
					}
					for _, item := range chains {
						rec := item.(map[string]any)
						id, err := resolve(rec["generation"])
						if err != nil {
							return nil, err
						}
						if rec["node"] != registry[rec["generation"].(float64)]["id"] {
							return nil, fmt.Errorf("occurrence script identity mismatch")
						}
						rec["generation"] = id
						delete(rec, "occurrence")
					}
				}
				result[side] = table
			}
			if !reflect.DeepEqual(result["before"], result["after"]) {
				return nil, fmt.Errorf("read changed queried table")
			}
			if g := raw["winner"].(float64); g != 0 {
				n := registry[g]
				links := map[string]any{}
				for _, field := range []string{"parent", "prev", "next"} {
					id, err := resolve(n[field])
					if err != nil {
						return nil, err
					}
					links[field] = id
				}
				result["links"] = links
				if !reflect.DeepEqual(links["parent"], result["current_parent"]) || !reflect.DeepEqual(links["next"], result["next_generation"]) {
					return nil, fmt.Errorf("result links disagree with snapshot")
				}
			}
		}
		out = append(out, result)
	}
	results := 0
	for _, row := range rows {
		if row.Event == "read.equal" || strings.HasPrefix(row.Event, "read.selection.") {
			results++
		}
	}
	if results == 0 {
		return nil, fmt.Errorf("empty G2 result set")
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty G2 evidence")
	}
	return out, nil
}

// These exercise the acceptance predicate used for the untouched driver too.
// Every mutant retains the selected result; incomplete transcripts cannot pass
// by preserving only its old four-column vector.
func nativeG2CloneRows(t *testing.T, rows []nativeDriverResult, mutate func(map[string]any)) []nativeDriverResult {
	t.Helper()
	result := make([]nativeDriverResult, len(rows))
	for i, row := range rows {
		b, err := json.Marshal(row.raw)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		mutate(raw)
		b, err = json.Marshal(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &result[i]); err != nil {
			t.Fatal(err)
		}
		if len(row.bootstrap) > 0 {
			var bootstrap map[string]any
			if err := json.Unmarshal(row.bootstrap, &bootstrap); err != nil {
				t.Fatal(err)
			}
			mutate(bootstrap)
			result[i].bootstrap, err = json.Marshal(bootstrap)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return result
}

func TestNativeG2UnmodifiedEvidenceRejections(t *testing.T) {
	script := "root 1 c\nterm 2 1 p same\nread-instance 2 2 1\n"
	valid := nativeDriver(t, script)
	if err := nativeG2CompareEvidence(script, valid, valid); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(map[string]any){
		"winner-generation": func(r map[string]any) {
			if r["event"] == "read.selection.instance" {
				r["winner"] = float64(1 << 30)
			}
		},
		"winner-script": func(r map[string]any) {
			if r["event"] == "read.selection.instance" {
				r["winner_script"] = float64(3)
			}
		},
		"changed-after": func(r map[string]any) {
			if r["event"] == "read.selection.instance" {
				r["after"] = map[string]any{"present": true}
			}
		},
		"internal-status": func(r map[string]any) {
			if r["event"] == "read.selection.instance" {
				r["rc"] = float64(6)
			}
		},
		"link-generation": func(r map[string]any) {
			if r["nodes"] != nil {
				r["nodes"].([]any)[0].(map[string]any)["prev"] = float64(1 << 30)
			}
		},
		"internal-diagnostic": func(r map[string]any) {
			if r["nodes"] != nil {
				r["diagnostics"] = []any{map[string]any{"code": 6, "msg": "Internal error."}}
			}
		},
		"diagnostic-message": func(r map[string]any) {
			if r["nodes"] != nil {
				r["diagnostics"] = []any{map[string]any{"code": 7, "msg": "changed"}}
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if err := nativeG2CompareEvidence(script, valid, nativeG2CloneRows(t, valid, mutate)); err == nil {
				t.Fatal("accepted corrupted unmodified evidence")
			}
		})
	}
	for _, name := range []string{"results-only", "missing-result", "truncated-completion", "wrong-operation", "extra-result"} {
		t.Run(name, func(t *testing.T) {
			mutant := append([]nativeDriverResult(nil), valid...)
			switch name {
			case "results-only":
				mutant = mutant[len(mutant)-2 : len(mutant)-1]
			case "missing-result":
				mutant = append(mutant[:len(mutant)-2], mutant[len(mutant)-1])
			case "truncated-completion":
				mutant = mutant[:len(mutant)-1]
			case "wrong-operation":
				mutant[len(mutant)-2].Operation++
			case "extra-result":
				mutant = append(mutant, mutant[len(mutant)-2])
			}
			if err := nativeG2CompareEvidence(script, valid, mutant); err == nil {
				t.Fatal("accepted incomplete evidence")
			}
		})
	}
}

func TestNativeG2ReadCompletion(t *testing.T) {
	for command, event := range map[string]string{"read-schema": "read.selection.schema", "read-instance": "read.selection.instance", "read-value": "read.selection.value", "read-opaque": "read.selection.opaque", "equal": "read.equal"} {
		t.Run(command, func(t *testing.T) {
			script := command + " 1 2 3\n"
			result := nativeDriverResult{Event: event, Operation: 1}
			completed := nativeDriverResult{Event: command, Operation: 1}
			if err := nativeCompletedReplay(script, []nativeDriverResult{result, completed}, ""); err != nil {
				t.Fatal(err)
			}
			for name, rows := range map[string][]nativeDriverResult{
				"missing-result": {completed}, "results-only": {result}, "duplicate-result": {result, result, completed},
				"wrong-operation":    {{Event: event, Operation: 2}, completed},
				"premature-boundary": {{Event: "boundary.early", Operation: 1}, result, completed},
			} {
				t.Run(name, func(t *testing.T) {
					if err := nativeCompletedReplay(script, rows, ""); err == nil {
						t.Fatal("accepted incomplete G2 replay")
					}
				})
			}
		})
	}
}

func TestNativeG2ExactErrorEvidence(t *testing.T) {
	const script = "parse-xml 1 <u xmlns=\"urn:m\">:value</u>\nopaque 2 0 u m:value\nequal 1 2 1\n"
	valid := nativeDriver(t, script)
	if err := nativeG2CompareEvidence(script, valid, valid); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ENOT-to-EINT", "required-message"} {
		t.Run(name, func(t *testing.T) {
			mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
				if name == "ENOT-to-EINT" && (r["event"] == "equal" || r["event"] == "read.equal") {
					r["rc"] = float64(6)
				}
				if name == "required-message" {
					for _, channel := range []string{"diagnostics", "logs"} {
						items, _ := r[channel].([]any)
						for _, item := range items {
							item.(map[string]any)["msg"] = "different argument"
						}
					}
				}
			})
			if err := nativeG2CompareEvidence(script, valid, mutant); err == nil {
				t.Fatal("lost exact native error evidence")
			}
		})
	}
}

// Run the actual untouched-driver helper in a subprocess, since it reports
// failures through testing.T. The synthetic executable is a negative-control
// transport, never claimed as native semantic evidence.
func TestNativeG2UnmodifiedReplayControls(t *testing.T) {
	const script = "equal 0 0 1\n"
	var valid []nativeDriverResult
	if os.Getenv("YANG_NATIVE_G2_REPLAY_CHILD") == "1" {
		//nolint:gosec // Parent test supplies its temporary reference file to this child.
		b, err := os.ReadFile(os.Getenv("YANG_NATIVE_G2_REFERENCE"))
		if err != nil {
			t.Fatal(err)
		}
		var raw []json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		for _, line := range raw[1:] {
			var r nativeDriverResult
			if err := json.Unmarshal(line, &r); err != nil {
				t.Fatal(err)
			}
			r.bootstrap = raw[0]
			valid = append(valid, r)
		}
		nativeG2Unmodified(t, script, valid)
		return
	}
	valid = nativeDriver(t, script)
	reference := filepath.Join(t.TempDir(), "reference.json")
	b, err := json.Marshal([]json.RawMessage{valid[0].bootstrap, valid[0].Evidence, valid[1].Evidence})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reference, b, 0600); err != nil {
		t.Fatal(err)
	}
	result, completed := string(valid[0].Evidence), string(valid[1].Evidence)
	for name, rows := range map[string][]string{
		"valid": {result, completed}, "results-only": {result}, "truncated": {result},
		"missing-result": {completed}, "wrong-operation": {strings.Replace(result, `"operation":1`, `"operation":2`, 1), completed},
		"unexpected-boundary": {`{"event":"boundary.early","operation":1}`, result, completed},
	} {
		t.Run(name, func(t *testing.T) {
			driver := filepath.Join(t.TempDir(), "synthetic-control")
			body := "#!/bin/sh\ncat <<'NATIVE_CONTROL'\n" + string(valid[0].bootstrap) + "\n" + strings.Join(rows, "\n") + "\nNATIVE_CONTROL\n"
			if err := os.WriteFile(driver, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			//nolint:gosec // The synthetic transport must be executable inside its private test directory.
			if err := os.Chmod(driver, 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestNativeG2UnmodifiedReplayControls$") //nolint:gosec // current test executable, fixed test selector
			cmd.Env = append(os.Environ(), "YANG_NATIVE_G2_REPLAY_CHILD=1", "YANG_NATIVE_G2_REFERENCE="+reference, "YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER="+driver)
			output, err := cmd.CombinedOutput()
			if (err == nil) != (name == "valid") {
				t.Fatalf("replay control %s: %v\n%s", name, err, output)
			}
			if err != nil && !strings.Contains(string(output), "incomplete native replay") && !strings.Contains(string(output), "unexpected native boundary") && !strings.Contains(string(output), "native identity evidence:") && !strings.Contains(string(output), "missing result snapshot") && !strings.Contains(string(output), "empty G2 result set") {
				t.Fatalf("unrelated control failure: %s", output)
			}
		})
	}
}

func TestNativeG2EqualityIdentityEvidence(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "read", "read-empty-prefix.calls"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	valid := nativeDriver(t, script)
	if err := nativeG2CompareEvidence(script, valid, valid); err != nil {
		t.Fatal(err)
	}
	operands := map[string]any{}
	for _, row := range valid {
		if row.Event == "read.equal" {
			operands["left"], operands["right"] = row.raw["left_generation"], row.raw["right_generation"]
			break
		}
	}
	for _, mutation := range []string{"empty-registry", "left", "right", "missing-left-generation", "missing-right-generation", "wrong-binding", "missing-context-and-modules", "missing-diagnostic-context", "missing-context", "missing-flags", "missing-links", "context", "flags", "links"} {
		t.Run(mutation, func(t *testing.T) {
			mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
				if mutation == "missing-context-and-modules" {
					delete(r, "modules")
				}
				if mutation == "missing-diagnostic-context" {
					for _, d := range nativeEvidenceArray(r["diagnostics"]) {
						delete(d.(map[string]any), "context_generation")
					}
				}
				if r["event"] == "read.equal" {
					switch mutation {
					case "missing-left-generation":
						delete(r, "left_generation")
					case "missing-right-generation":
						delete(r, "right_generation")
					case "wrong-binding":
						r["left_generation"] = r["right_generation"]
					}
				}
				items, ok := r["nodes"].([]any)
				if !ok {
					return
				}
				if mutation == "empty-registry" {
					r["nodes"] = []any{}
					return
				}
				kept := []any{}
				for _, item := range items {
					n := item.(map[string]any)
					if (mutation == "left" || mutation == "right") && n["generation"] == operands[mutation] {
						continue
					}
					if n["generation"] == operands["left"] {
						switch mutation {
						case "missing-context", "missing-context-and-modules":
							delete(n, "context_generation")
						case "missing-flags":
							delete(n, "flags")
						case "missing-links":
							delete(n, "next")
						case "context":
							n["context_generation"] = float64(99)
						case "flags":
							n["flags"] = float64(int(n["flags"].(float64)) ^ 1)
						case "links":
							n["prev"] = float64(0)
						}
					}
					kept = append(kept, n)
				}
				r["nodes"] = kept
			})
			// State changes compare against the valid baseline. Deletions and
			// invalid bindings must also fail on identically corrupted peers.
			sides := []string{"left", "right", "both"}
			if mutation == "context" || mutation == "flags" || mutation == "links" {
				sides = sides[:2]
			}
			for _, side := range sides {
				t.Run(side, func(t *testing.T) {
					a, b := valid, mutant
					if side == "left" {
						a, b = mutant, valid
					}
					if side == "both" {
						a = mutant
					}
					if err := nativeG2CompareEvidence(script, a, b); err == nil {
						t.Fatal("accepted incomplete equality identities")
					}
				})
			}
		})
	}
}

func TestNativeG2EqualityTableEvidence(t *testing.T) {
	const script = "root 1 c\nterm 2 1 p same\nterm 3 1 q same\nterm 4 1 ll a\nterm 5 1 ll b\nequal 1 1 1\n"
	valid := nativeDriver(t, script)
	if err := nativeG2CompareEvidence(script, valid, valid); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"missing-table", "changed-layout"} {
		t.Run(mutation, func(t *testing.T) {
			mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
				for _, item := range nativeEvidenceArray(r["nodes"]) {
					n := item.(map[string]any)
					if n["id"] != float64(1) {
						continue
					}
					if mutation == "missing-table" {
						delete(n, "table")
						continue
					}
					table := n["table"].(map[string]any)
					if table["present"] == true {
						table["mode"] = float64(1)
					}
				}
			})
			if err := nativeG2CompareEvidence(script, valid, mutant); err == nil {
				t.Fatal("accepted lost equality operand table state")
			}
			if mutation == "missing-table" {
				if err := nativeG2CompareEvidence(script, mutant, mutant); err == nil {
					t.Fatal("accepted identical missing operand tables")
				}
			}
		})
	}
}

func TestNativeG2EqualityIncarnations(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "read", "read-equality-incarnations.calls"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	valid := nativeDriver(t, script)
	nativeG2Unmodified(t, script, valid)
	var old, current any
	count := 0
	for _, row := range valid {
		if row.Event == "read.equal" {
			count++
			if row.RC != nativeOK {
				t.Fatal("new incarnation/alias comparison failed")
			}
			current = row.raw["left_generation"]
		}
		for _, item := range nativeEvidenceArray(row.raw["nodes"]) {
			n := item.(map[string]any)
			if n["id"] == float64(2) && n["alive"] == false {
				old = n["generation"]
			}
		}
	}
	if count != 3 || old == nil || current == nil || old == current {
		t.Fatal("did not exercise distinct handle incarnations")
	}
	mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
		if r["event"] == "read.equal" {
			r["left_generation"] = old
		}
	})
	if err := nativeG2CompareEvidence(script, mutant, mutant); err == nil {
		t.Fatal("accepted a retired operand incarnation on both sides")
	}
}

// The dump reproducer retains completion/results while deleting state on either
// peer. Bootstrap snapshots have the same independently required obligation.
func TestNativeG2RequiredSnapshots(t *testing.T) {
	const script = "root 1 c\ndump\nequal 1 1 0\n"
	valid := nativeDriver(t, script)
	nativeG2Unmodified(t, script, valid)
	for _, event := range []string{"dump", "driver.ready.actual-children-callback"} {
		for _, field := range []string{"nodes", "tables", "schemas", "bindings"} {
			t.Run(event+"/"+field, func(t *testing.T) {
				mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
					if r["event"] == event {
						delete(r, field)
					}
				})
				for _, side := range []string{"left", "right", "both"} {
					t.Run(side, func(t *testing.T) {
						a, b := mutant, valid
						if side == "right" {
							a, b = valid, mutant
						}
						if side == "both" {
							b = mutant
						}
						if err := nativeG2CompareEvidence(script, a, b); err == nil {
							t.Fatal("accepted missing required snapshot")
						}
					})
				}
			})
		}
	}
	t.Run("dump-statuses", func(t *testing.T) {
		left := nativeG2CloneRows(t, valid, func(r map[string]any) {
			if r["event"] == "dump" {
				delete(r, "nodes")
				r["rc"] = float64(0)
			}
		})
		right := nativeG2CloneRows(t, left, func(r map[string]any) {
			if r["event"] == "dump" {
				r["rc"] = float64(7)
			}
		})
		if err := nativeG2CompareEvidence(script, left, right); err == nil {
			t.Fatal("accepted incomplete dump with differing statuses")
		}
	})
}

func TestNativeG2CurrentHashEvidence(t *testing.T) {
	const script = "root 1 c\nterm 2 1 p same\ndup 3 2 0 66\nequal 2 3 0\n"
	valid := nativeDriver(t, script)
	nativeG2Unmodified(t, script, valid)
	mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
		if r["event"] != "equal" {
			return
		}
		for _, item := range nativeEvidenceArray(r["nodes"]) {
			n := item.(map[string]any)
			if n["id"] == float64(2) {
				n["hash"] = n["hash"].(float64) + 1
			}
		}
	})
	for _, side := range []string{"left", "right"} {
		t.Run(side, func(t *testing.T) {
			a, b := mutant, valid
			if side == "right" {
				a, b = valid, mutant
			}
			if err := nativeG2CompareEvidence(script, a, b); err == nil {
				t.Fatal("accepted changed current operand hash")
			}
		})
	}
	// Identical peers with a changed current hash remain valid: recorded
	// occurrence hashes are immutable history, independent of current state.
	if err := nativeG2CompareEvidence(script, mutant, mutant); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCompletedCommandProjection(t *testing.T) {
	const script = "root 1 c\nvalidate 1\n"
	valid := nativeDriver(t, script)
	a, err := nativeCompletedCommandEvidence(script, valid)
	if err != nil {
		t.Fatal(err)
	}
	plain := nativeDriverReplayPath(t, script, "", false, os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER"))
	b, err := nativeCompletedCommandEvidence(script, plain)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.projection, b.projection) {
		t.Fatal("validation-only state differs")
	}
	if _, err := nativeG2Evidence(script, valid); err == nil {
		t.Fatal("accepted G2 without read results")
	}
	for _, field := range []string{"rc", "diagnostics", "logs"} {
		t.Run(field, func(t *testing.T) {
			mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
				if r["event"] != "validate" {
					return
				}
				switch field {
				case "rc":
					r[field] = float64(7)
				case "logs":
					r[field] = []any{map[string]any{"level": float64(0), "line": float64(0), "msg": "changed"}}
				default:
					r[field] = []any{map[string]any{"code": float64(7), "context_generation": float64(1), "msg": "changed"}}
				}
			})
			b, err := nativeCompletedCommandEvidence(script, mutant)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(a.projection, b.projection) {
				t.Fatal("lost completed-command status/diagnostics")
			}
		})
	}
}
