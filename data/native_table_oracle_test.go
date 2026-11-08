// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type nativeDriverResult struct {
	Evidence  json.RawMessage `json:"-"`
	bootstrap json.RawMessage
	raw       map[string]any

	Modules []struct {
		Name        string `json:"name"`
		Namespace   string `json:"namespace"`
		Implemented bool   `json:"implemented"`
		Latest      uint8  `json:"latest"`
	} `json:"modules"`
	Before       json.RawMessage `json:"before"`
	After        json.RawMessage `json:"after"`
	WinnerScript int             `json:"winner_script"`
	ParentScript int             `json:"parent_script"`
	NextScript   int             `json:"next_script"`
	Event        string          `json:"event"`
	Operation    int             `json:"operation"`
	RC           nativeStatus    `json:"rc"`
	Table        struct {
		Present bool `json:"present"`
		nativeImage
		Slots []struct{ Next uint32 } `json:"slots"`
	} `json:"table"`
	Nodes []struct {
		ID    int  `json:"id"`
		Alive bool `json:"alive"`
		Table struct {
			Present bool `json:"present"`
			nativeImage
			Slots []struct{ Next uint32 } `json:"slots"`
		} `json:"table"`
	} `json:"nodes"`
}

func (r *nativeDriverResult) UnmarshalJSON(b []byte) error {
	if !utf8.Valid(b) {
		return fmt.Errorf("invalid UTF-8 in native evidence")
	}
	type plain nativeDriverResult
	if err := json.Unmarshal(b, (*plain)(r)); err != nil {
		return err
	}
	r.Evidence = bytes.Clone(b)
	return json.Unmarshal(b, &r.raw)
}

// Both processes must finish P, Q, R release and still print the two detached
// allocations. A zero exit status or equal empty result sets proves neither.
func TestNativeReleaseControlCompletion(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	comparison := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER")
	if comparison == "" {
		comparison = filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	script, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "remote-last-release.calls"))
	if err != nil {
		t.Fatal(err)
	}
	for label, path := range map[string]string{"instrumented": driver, "unmodified": comparison} {
		t.Run(label, func(t *testing.T) {
			t.Setenv("YANG_NATIVE_REGISTRATION_DRIVER", path)
			rows := nativeDriver(t, string(script))
			var completed []string
			var released, prints int
			dead := make(map[int]bool)
			groups := [][]int{{1, 11, 12, 14}, {2, 16, 17}, {3, 31}}
			for _, row := range rows {
				if strings.HasPrefix(row.Event, "boundary.") {
					t.Fatalf("early stop at operation %d: %s", row.Operation, row.Event)
				}
				if row.Event == "public.result" {
					prints++
					continue
				}
				completed = append(completed, row.Event)
				if row.Event != "free-root" {
					continue
				}
				if released == len(groups) {
					t.Fatal("extra release")
				}
				for _, id := range groups[released] {
					dead[id] = true
				}
				released++
				seen := make(map[int]bool)
				for _, node := range row.Nodes {
					if node.ID == 0 {
						continue
					}
					seen[node.ID] = true
					if node.Alive == dead[node.ID] {
						t.Errorf("release %d: handle %d alive=%t", released, node.ID, node.Alive)
					}
				}
				if len(seen) != 11 {
					t.Errorf("release %d: got %d retained handles, want 11", released, len(seen))
				}
			}
			var expected []string
			for _, line := range strings.Split(string(script), "\n") {
				fields := strings.Fields(line)
				if len(fields) != 0 && !strings.HasPrefix(fields[0], "#") {
					expected = append(expected, fields[0])
				}
			}
			if !slices.Equal(completed, expected) || released != 3 || prints != 2 {
				t.Fatalf("incomplete release control: commands=%v want=%v releases=%d prints=%d", completed, expected, released, prints)
			}
		})
	}
}

func TestNativeControlReplayRejections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "check_test.py"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("control comparison regressions: %v\n%s", err, out)
	}
}

// The stale handle must name storage actually reused by a live native node,
// including when verbose observer allocations perturb the allocator bins.
func TestNativeTombstoneAddressReuse(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	comparison := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER")
	if comparison == "" {
		comparison = filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	for label, path := range map[string]string{"instrumented": driver, "unmodified": comparison} {
		for _, name := range []string{"handle-tombstone", "handle-tombstone-probe", "lifetime/stale-handle"} {
			//nolint:gosec // Fixed repository replay names from the literal case list.
			script, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", name+".calls"))
			if err != nil {
				t.Fatal(err)
			}
			for _, option := range []string{"--quiet", "--events"} {
				t.Run(label+"/"+name+"/"+option, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
					defer cancel()
					//nolint:gosec // Explicit opt-in path and fixed replay arguments.
					cmd := exec.CommandContext(ctx, path, option)
					cmd.Env = append(os.Environ(), "GLIBC_TUNABLES=glibc.malloc.tcache_count=7")
					cmd.Stdin = bytes.NewReader(script)
					out, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("tombstone replay: %v\n%s", err, out)
					}
					var rows []json.RawMessage
					for _, line := range bytes.Split(bytes.TrimSpace(out), []byte{'\n'}) {
						rows = append(rows, json.RawMessage(line))
					}
					if err := nativeValidateEvidence(string(script), rows); err != nil {
						t.Fatal(err)
					}
					var last struct {
						Event      string `json:"event"`
						Handle     int    `json:"handle"`
						Generation int    `json:"generation"`
					}
					if err := json.Unmarshal(rows[len(rows)-1], &last); err != nil {
						t.Fatal(err)
					}
					if last.Event != "boundary.tombstoned-handle" || last.Handle != 14 || last.Generation == 0 {
						t.Fatalf("wrong stale-handle boundary: %+v", last)
					}
					var replacement int
					var liveReplacement bool
					var completed []nativeDriverResult
					for _, row := range rows {
						var r struct {
							Event         string `json:"event"`
							Operation     int    `json:"operation"`
							OldGeneration int    `json:"old_generation"`
							NewGeneration int    `json:"new_generation"`
							Nodes         []struct {
								ID         int  `json:"id"`
								Generation int  `json:"generation"`
								Alive      bool `json:"alive"`
							} `json:"nodes"`
						}
						if err := json.Unmarshal(row, &r); err != nil {
							t.Fatal(err)
						}
						completed = append(completed, nativeDriverResult{Event: r.Event, Operation: r.Operation})
						if r.Event == "allocation.address-reuse" && (r.OldGeneration == last.Generation || replacement > 0 && r.OldGeneration == replacement) {
							replacement = r.NewGeneration
						}
						for _, n := range r.Nodes {
							if replacement > last.Generation && n.Generation == replacement && n.ID == 104 && n.Alive {
								liveReplacement = true
							}
						}
					}
					if !liveReplacement {
						t.Fatalf("stale generation %d was not reused by live handle 104 (replacement %d)", last.Generation, replacement)
					}
					if err := nativeCompletedReplay(string(script), completed, "boundary.tombstoned-handle"); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestNativeRetainedAddressReuse(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	comparison := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER")
	if comparison == "" {
		comparison = filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	//nolint:gosec // Fixed checker and explicit opt-in local driver paths.
	cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "check.py"),
		"--driver", driver, "--comparison-driver", comparison, "--retained-reuse")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("retained address reuse: %v\n%s", err, out)
	}
	if bytes.Count(out, []byte(`"retained_reuse_verified": true`)) != 4 {
		t.Fatalf("missing quiet/verbose evidence from either build:\n%s", out)
	}
}

// Each live request requires actual address reuse in both builds, complete
// command/result sequences, persistent tombstones and equal public outcomes.
func TestNativeComparisonLifetimes(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	comparison := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER")
	if comparison == "" {
		comparison = filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	for _, name := range []string{"recreate-print", "recreate-lookup", "dup-descendant", "parse-descendant", "list-key", "table-reuse", "validation-delete", "retained-same-hash", "stale-handle", "retained-resize", "retained-remove"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "check.py"),
				"--driver", driver, "--comparison-driver", comparison, "--lifetime-case", name)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("lifetime comparison: %v\n%s", err, out)
			}
			for _, label := range []string{"truncated-control-both", "early-stop-control-both", "missing-command-control-both", "empty-results-control-both", "empty-compared-results", "missing-reuse-instrumented", "missing-reuse-unmodified", "invalid-reuse-generation-instrumented", "invalid-reuse-generation-unmodified"} {
				if !bytes.Contains(out, []byte(`"negative_evidence": "`+label+`"`)) {
					t.Fatalf("missing replay rejection %s\n%s", label, out)
				}
			}
			t.Logf("%s", out)
		})
	}
}

func TestNativeReleaseRetirementPaths(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	comparison := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER")
	if comparison == "" {
		comparison = filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	remote, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "remote-last-release.calls"))
	if err != nil {
		t.Fatal(err)
	}
	remotePrefix, _, ok := strings.Cut(string(remote), "free-root 1")
	if !ok {
		t.Fatal("missing remote release frontier")
	}
	for label, path := range map[string]string{"instrumented": driver, "unmodified": comparison} {
		t.Run(label, func(t *testing.T) {
			t.Setenv("YANG_NATIVE_REGISTRATION_DRIVER", path)
			for _, tc := range []struct {
				name, script, terminal string
				operation              int
			}{
				{"replaced-generic", "root 1 c\nnew 8 0\ninsert 7 7 0\nnew 8 0\nfree 7\nprint 1\n", "print", 6},
				{"removed-child", "root 1 c\nterm 11 1 p P\nterm 12 1 q Q\nterm 13 1 v 1\nterm 14 1 v 2\nfree 14\nprint 1\n", "print", 7},
				{"retained-generic", "new 8 0\ninsert 1 7 0\ninsert 1 7 0\nfree 1\nfind-next-loop 1 7 1\n", "boundary.tombstoned-semantic-probe", 5},
				{"retained-foreign", remotePrefix + "free-root 2\n", "boundary.retained-dangling-links", 15},
			} {
				t.Run(tc.name, func(t *testing.T) {
					terminal := ""
					if strings.HasPrefix(tc.terminal, "boundary.") {
						terminal = tc.terminal
					}
					rows := nativeDriverUntil(t, tc.script, terminal)
					if len(rows) == 0 || rows[len(rows)-1].Event != tc.terminal {
						t.Fatalf("unexpected terminal: %v, want %s", rows, tc.terminal)
					}
					last := rows[len(rows)-1]
					if last.Operation != tc.operation {
						t.Fatalf("stopped at operation %d, want %d", last.Operation, tc.operation)
					}
				})
			}
		})
	}
}

func nativeDriver(t *testing.T, script string) []nativeDriverResult {
	t.Helper()
	return nativeDriverUntil(t, script, "")
}

func nativeDriverUntil(t *testing.T, script, terminal string) []nativeDriverResult {
	t.Helper()
	return nativeDriverReplay(t, script, terminal, false)
}

func nativeDriverReplay(t *testing.T, script, terminal string, verbose bool) []nativeDriverResult {
	t.Helper()
	return nativeDriverReplayPath(t, script, terminal, verbose, os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER"))
}

func nativeDriverReplayPath(t *testing.T, script, terminal string, verbose bool, path string) []nativeDriverResult {
	t.Helper()
	if path == "" {
		t.Skip("isolated static observer: set YANG_NATIVE_REGISTRATION_DRIVER after its test-only build")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	options := []string{"--quiet"}
	if verbose {
		options[0] = "--events"
	}
	for _, line := range strings.Split(script, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && (fields[0] == "list" || fields[0] == "frontier") {
			options = append(options, "--event-schema")
			break
		}
	}
	for _, line := range strings.Split(script, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && (strings.HasPrefix(fields[0], "read-") || fields[0] == "equal" || fields[0] == "context" || fields[0] == "context-keyless") {
			options = append(options, "--read-schema")
			break
		}
	}
	//nolint:gosec // Explicit opt-in path and bounded test-only arguments.
	cmd := exec.CommandContext(ctx, path, options...)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native registration driver: %v\n%s\nscript:\n%s", err, out, script)
	}
	var raw []json.RawMessage
	ready := 0

	scan := bufio.NewScanner(bytes.NewReader(out))
	scan.Buffer(make([]byte, 4096), 8<<20)
	for scan.Scan() {
		raw = append(raw, bytes.Clone(scan.Bytes()))
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if err := nativeValidateEvidence(script, raw); err != nil {
		t.Fatal(err)
	}
	var results []nativeDriverResult
	for _, line := range raw {
		var r nativeDriverResult
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		r.bootstrap = raw[0]
		if r.Event == "driver.ready.actual-children-callback" {
			ready++
			if ready != 1 || r.Operation != 0 || len(results) != 0 {
				t.Fatal("invalid native ready event")
			}

			continue
		}
		if r.Event == "allocation.address-reuse" {
			continue
		}
		for i := range r.Table.Chains {
			s := &r.Table.Chains[i]
			s.Next = r.Table.Slots[s.Slot].Next
		}
		for j := range r.Nodes {
			for i := range r.Nodes[j].Table.Chains {
				s := &r.Nodes[j].Table.Chains[i]
				s.Next = r.Nodes[j].Table.Slots[s.Slot].Next
			}
		}
		results = append(results, r)
	}
	if ready != 1 {
		t.Fatal("missing native ready event")
	}

	if err := nativeCompletedReplay(script, results, terminal); err != nil {
		t.Fatal(err)
	}
	return results
}

// Validate the full evidence before projecting it into the private table model.
// Python and Go consumers deliberately share this validator, including mutants.
func nativeValidateEvidence(script string, rows []json.RawMessage) error {
	_, err := nativeIdentityEvidenceRun(script, rows, false)
	return err
}

type nativeIdentityEvidence struct {
	States []any               `json:"states"`
	Refs   map[int]map[int]any `json:"refs"`
}

func nativeIdentityProjection(script string, rows []json.RawMessage) (nativeIdentityEvidence, error) {
	return nativeIdentityEvidenceRun(script, rows, true)
}

// G1 validation keeps its validation-only transport. G2 additionally requests
// canonical states/refs after the same complete-evidence checks have succeeded.
func nativeIdentityEvidenceRun(script string, rows []json.RawMessage, project bool) (nativeIdentityEvidence, error) {
	var result nativeIdentityEvidence
	payload, err := json.Marshal(struct {
		Script  string            `json:"script"`
		Rows    []json.RawMessage `json:"rows"`
		Project bool              `json:"project"`
	}{script, rows, project})
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	//nolint:gosec // Fixed repository validator receives local test evidence.
	cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "identity.py"))
	cmd.Stdin = bytes.NewReader(payload)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return result, fmt.Errorf("native identity evidence: %w: %s", err, out)
	}
	if !project {
		return result, nil
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return result, err
	}
	return result, nil
}

func TestNativeIdentitySweep(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	comparison := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER")
	if comparison == "" {
		comparison = filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	//nolint:gosec // Fixed test script and explicitly opted-in local driver.
	cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "sweep_test.py"),
		"--driver", driver, "--comparison-driver", comparison)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("identity/lifetime sweep: %v\n%s", err, out)
	} else {
		t.Logf("%s", out)
	}
}

// Shared by every Go differential caller, including loops that could otherwise
// accept zero snapshots or compare only a selected prefix of the request.
func nativeCompletedReplay(script string, results []nativeDriverResult, terminal string) error {
	type event struct {
		operation int
		name      string
	}
	var expected, expectedReads []event
	commandNames := make(map[string]bool)
	reads := map[string]string{"print": "public.result", "schema": "read.schema", "rewind": "read.first-sibling", "null-reads": "read.null", "instance": "read.instance", "opaq-find": "read.opaque", "read-schema": "read.selection.schema", "read-instance": "read.selection.instance", "read-value": "read.selection.value", "read-opaque": "read.selection.opaque", "equal": "read.equal"}
	for _, line := range strings.Split(script, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		commandNames[fields[0]] = true
		expected = append(expected, event{len(expected) + 1, fields[0]})
	}
	if len(expected) == 0 || len(results) == 0 {
		return fmt.Errorf("empty native replay")
	}
	wantTerminal := expected[len(expected)-1]
	if terminal != "" {
		wantTerminal.name = terminal
		expected = expected[:len(expected)-1]
	}
	for _, command := range expected {
		if result := reads[command.name]; result != "" {
			expectedReads = append(expectedReads, event{command.operation, result})
		}
	}
	var actual, actualReads []event
	for i, row := range results {
		if strings.HasPrefix(row.Event, "boundary.") && (row.Event != terminal || i != len(results)-1) {
			return fmt.Errorf("unexpected native boundary: %s", row.Event)
		}
		if commandNames[row.Event] {
			actual = append(actual, event{row.Operation, row.Event})
		}
		if row.Event == "public.result" || row.Event == "read.schema" || row.Event == "read.first-sibling" || row.Event == "read.null" || row.Event == "read.instance" || row.Event == "read.opaque" || row.Event == "read.equal" || strings.HasPrefix(row.Event, "read.selection.") {
			actualReads = append(actualReads, event{row.Operation, row.Event})
		}
	}
	last := results[len(results)-1]
	if !slices.Equal(actual, expected) || !slices.Equal(actualReads, expectedReads) || (event{last.Operation, last.Event}) != wantTerminal {
		return fmt.Errorf("incomplete native replay: commands=%v want=%v reads=%v want=%v terminal=%d/%s want=%v",
			actual, expected, actualReads, expectedReads, last.Operation, last.Event, wantTerminal)
	}
	return nil
}

func TestNativeDriverReplayCompletion(t *testing.T) {
	row := func(operation int, name string) nativeDriverResult {
		return nativeDriverResult{Event: name, Operation: operation}
	}
	for _, tc := range []struct {
		name, script, terminal string
		rows                   []nativeDriverResult
		valid                  bool
	}{
		{"complete", "dump\nprint 1\n", "", []nativeDriverResult{row(1, "dump"), row(2, "public.result"), row(2, "print")}, true},
		{"empty", "dump\n", "", nil, false},
		{"truncated", "dump\nprint 1\n", "", []nativeDriverResult{row(1, "dump")}, false},
		{"missing-command", "dump\nprint 1\n", "", []nativeDriverResult{row(2, "public.result"), row(2, "print")}, false},
		{"missing-result", "print 1\n", "", []nativeDriverResult{row(1, "print")}, false},
		{"early-stop", "dump\nprint 1\n", "", []nativeDriverResult{row(1, "dump"), row(2, "boundary.tombstoned-semantic-probe")}, false},
		{"expected-stop", "dump\nprint 1\n", "boundary.tombstoned-semantic-probe", []nativeDriverResult{row(1, "dump"), row(2, "boundary.tombstoned-semantic-probe")}, true},
		{"wrong-stop-operation", "dump\nprint 1\n", "boundary.tombstoned-semantic-probe", []nativeDriverResult{row(1, "dump"), row(1, "boundary.tombstoned-semantic-probe")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := nativeCompletedReplay(tc.script, tc.rows, tc.terminal); (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
		})
	}
}

// G1 entry controls and G2 selections must share the completion validator.
func TestNativeMergedReadCompletion(t *testing.T) {
	commands := []string{"instance", "opaq-find", "null-reads", "read-schema", "read-instance", "read-value", "read-opaque", "equal"}
	reads := []string{"read.instance", "read.opaque", "read.null", "read.selection.schema", "read.selection.instance", "read.selection.value", "read.selection.opaque", "read.equal"}
	var script strings.Builder
	var rows []nativeDriverResult
	for i, command := range commands {
		script.WriteString(command + " 1 2 3\n")
		rows = append(rows, nativeDriverResult{Operation: i + 1, Event: reads[i]}, nativeDriverResult{Operation: i + 1, Event: command})
	}
	if err := nativeCompletedReplay(script.String(), rows, ""); err != nil {
		t.Fatal(err)
	}
	for i, command := range commands {
		t.Run(command, func(t *testing.T) {
			missing := append([]nativeDriverResult(nil), rows[:2*i]...)
			missing = append(missing, rows[2*i+1:]...)
			if err := nativeCompletedReplay(script.String(), missing, ""); err == nil {
				t.Fatal("accepted missing read result")
			}
		})
	}
}

func TestNativeObserverEvidence(t *testing.T) {
	driver := os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER")
	if driver == "" {
		t.Skip("set YANG_NATIVE_REGISTRATION_DRIVER after the isolated static build")
	}
	comparison := os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER")
	if comparison == "" {
		comparison = filepath.Join(filepath.Dir(driver), "driver-unmodified")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// This independently checks occurrence identities, retirement, logging versus
	// context diagnostics, public controls and the labeled native stop boundaries.
	cmd := exec.CommandContext(ctx, "python3", filepath.Join("..", "conformance", "oracle", "native_registration", "check.py"),
		"--driver", driver, "--comparison-driver", comparison)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native observer evidence: %v\n%s", err, out)
	}
	// The checker must actually exercise the audit's evidence mutants. The
	// earlier checker accepted all three and emitted no negative controls.
	for _, mutant := range []string{"zeroed-creation-and-source", "completed-dup-swapped-for-initialization", "removed-resize-pairs",
		"zeroed-dup-creation_operation", "zeroed-dup-creation_ordinal", "zeroed-dup-source", "wrong-existing-dup-source", "removed-dup-initialization",
		"incorrect-dup-initial-size", "incorrect-dup-initial-mode", "incorrect-dup-initial-used", "incorrect-dup-initial-first", "drifting-creation-identity",
		"removed-lyht_resize.before", "removed-lyht_resize.after", "resize-after-before-its-before"} {
		if !bytes.Contains(out, []byte(`"negative_evidence": "`+mutant+`"`)) {
			t.Fatalf("checker omitted negative evidence control %s\n%s", mutant, out)
		}
	}
	// Require the third audit's deletion controls per applicable replay, rather
	// than allowing one case to stand in for the entire retained evidence set.
	controls := make(map[string]map[string]bool)
	scan := bufio.NewScanner(bytes.NewReader(out))
	for scan.Scan() {
		var record struct {
			Case     string `json:"case"`
			Negative string `json:"negative_evidence"`
			Rejected bool   `json:"rejected"`
		}
		if err := json.Unmarshal(scan.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.Negative != "" && record.Rejected {
			if controls[record.Case] == nil {
				controls[record.Case] = make(map[string]bool)
			}
			controls[record.Case][record.Negative] = true
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	require := func(name string, labels ...string) {
		t.Helper()
		for _, label := range labels {
			if !controls[name][label] {
				t.Errorf("%s: checker omitted rejection of %s", name, label)
			}
		}
	}
	cases, err := filepath.Glob(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", "*.calls"))
	if err != nil || len(cases) == 0 {
		t.Fatalf("native cases: %v (%d scripts)", err, len(cases))
	}
	for _, path := range cases {
		name := strings.TrimSuffix(filepath.Base(path), ".calls")
		require(name, "removed-bootstrap-records", "removed-retained-identity", "removed-snapshot-nodes",
			"removed-snapshot-diagnostics", "removed-snapshot-node_generation", "removed-snapshot-source_generation",
			"truncated-replay", "unexpected-terminal")
		for _, field := range []string{"hash", "flags", "metadata", "schema", "prev", "next", "parent", "first", "tail", "table"} {
			require(name, "removed-current-"+field)
		}
		for _, field := range []string{"generation", "id", "creation_operation", "creation_ordinal", "source", "alive"} {
			require(name, "removed-identity-"+field)
		}
	}
	for _, name := range []string{"remote-last", "remote-nonlast", "collision-build", "collision-resize", "repeated-pointer",
		"merge2-defined", "internal-build", "collision-history-retained", "collision-history-shrunk", "free-slot-cap",
		"remote-last-results", "remote-last-release", "opaque-threshold", "opaque-separated", "opaque-eligibility",
		"resize-disabled", "list-events", "schema-pointer", "schema-pointer-reverse", "invalid-capacity", "invalid-mode"} {
		require(name, "empty-compared-results")
		for _, side := range []string{"instrumented", "unmodified", "both"} {
			for _, mutant := range []string{"truncated-control", "early-stop-control", "missing-command-control", "empty-results-control"} {
				require(name, mutant+"-"+side)
			}
		}
	}
	require("internal-build", "removed-parser-child-records", "removed-failed-constructor-records",
		"removed-allocation-1-2", "removed-allocation-1-3", "removed-allocation-1-4", "removed-allocation-1-5", "removed-allocation-3-1",
		"missing-expected-parser-allocations", "missing-expected-failed-allocation")
	for _, name := range []string{"internal-build", "driver-lifetime", "list-events"} {
		require(name, "removed-internal-allocation-records")
	}
	require("driver-lifetime", "removed-rb-topology", "removed-tombstone-records", "removed-table-occurrences", "missing-duplicate-child-suffix", "zero-free-generation")
	require("remote-last-results", "removed-event-public.result", "removed-event-read.schema", "removed-lookup-before", "removed-lookup-after")
	require("remote-last", "removed-event-read.first-sibling")
	require("resize-disabled", "removed-generic-table-new", "removed-generic-table-insert", "removed-generic-table-remove")
	require("find-next-watchdog", "removed-generic-table-insert", "removed-generic-table-boundary.watchdog.find-next-cycle")
	require("driver-lifetime", "removed-event-field-allocation.address-reuse-old_generation", "removed-event-field-allocation.address-reuse-new_generation",
		"zero-reuse-old_generation", "zero-reuse-new_generation")
	require("remote-last-results", "removed-event-field-public.result-rc", "removed-event-field-public.result-json",
		"removed-event-field-read.schema-queried_parent", "removed-event-field-read.schema-winner", "removed-event-field-read.schema-current_parent")
	require("remote-last", "removed-event-field-read.first-sibling-winner")
	require("internal-build", "removed-record-field-occurrence-bucket", "removed-record-field-occurrence-ordinal", "removed-record-field-rb-color",
		"removed-record-field-diagnostic-code", "removed-record-field-log-level", "removed-table-buckets", "removed-table-slots", "removed-table-free_chain",
		"removed-command-diagnostics", "removed-command-logs")
	require("collision-build", "removed-command-diagnostics", "removed-command-logs")
}

// Confirm the existing Go API has no earlier D-0121 refusal on the exact public
// prefix. G1 does not route the final insertion through its private kernel.
func TestNativePublicRemotePrefix(t *testing.T) {
	set := reviewDiffSet(t, `module m {yang-version 1.1;namespace urn:m;prefix m;
 rpc r {input {leaf a {type string;}leaf a2 {type string;}leaf-list v {type uint32;}}
 output {leaf o {type string;}}}}`)
	p, q, r := newTree(set), newTree(set), newTree(set)
	root := func(tr *Tree) *Node {
		n, err := tr.NewInner(nil, "m:r", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	pp, qq, rr := root(p), root(q), root(r)
	term := func(tr *Tree, parent *Node, name, value string, options NewOptions) *Node {
		n, err := tr.NewTerm(parent, name, value, options)
		if err != nil {
			t.Fatal(name, err)
		}
		return n
	}
	term(p, pp, "a", "a", NewOptions{})
	term(p, pp, "a2", "a2", NewOptions{})
	v1 := term(p, pp, "v", "1", NewOptions{})
	term(p, pp, "v", "2", NewOptions{})
	o := term(p, pp, "o", "o", NewOptions{Output: true})
	seed := term(r, rr, "v", "3", NewOptions{})
	v3, _, err := p.Dup(seed, pp, DupOptions{NoSort: true, NoMeta: true})
	if err != nil {
		t.Fatal("dup", err)
	}
	if err := o.Unlink(); err != nil {
		t.Fatal("unlink o", err)
	}
	if err := q.Insert(qq, v3); err != nil {
		t.Fatal("move v3", err)
	}
	if err := v1.Unlink(); err != nil {
		t.Fatal("unlink v1", err)
	}
	if v3 == nil || v3 == seed || v3.parent != qq || seed.parent != rr || o.parent != nil || v1.parent != nil {
		t.Fatal("public remote prefix did not retain the copy, source, move and unlink identities")
	}
}

func TestNativePrimitiveLiveOracle(t *testing.T) {
	// Fixed seeds are replayable public driver input, not generated goldens.
	for _, seed := range []uint64{247, 0x7be55cae, 0x257} {
		for _, mode := range []uint8{0, 1} {
			t.Run(fmt.Sprintf("%x/mode%d", seed, mode), func(t *testing.T) {
				h, table := nativeFresh(t, 8, mode)
				nodes := make([]*Node, 32)
				for i := range nodes {
					nodes[i] = new(Node)
				}
				var script strings.Builder
				fmt.Fprintf(&script, "new 8 %d\n", mode)
				expected := []nativeImage{nativeObserve(t, table, nodes, nativeOK)}
				rng := rand.New(rand.NewPCG(seed, seed+1))
				for range 192 {
					id := rng.IntN(len(nodes))
					hash := []uint32{0, 7, 15, 0x7be55cae, 0xffffffff}[rng.IntN(5)]
					check := rng.IntN(2) == 0
					remove := rng.IntN(3) == 0 || (mode == 0 && table.used == table.size)
					rc, err := h.primitive(table, nodes[id], hash, check, remove)
					if err != nil {
						t.Fatal(err)
					}
					if remove {
						fmt.Fprintf(&script, "remove %d %d\n", id+1, hash)
					} else {
						fmt.Fprintf(&script, "insert %d %d %d\n", id+1, hash, boolInt(check))
					}
					expected = append(expected, nativeObserve(t, table, nodes, rc))
				}
				results := nativeDriver(t, script.String())
				if len(results) != len(expected) {
					t.Fatalf("%d snapshots, want %d", len(results), len(expected))
				}
				for i, r := range results {
					got := r.Table.nativeImage
					got.RC = r.RC
					if !reflect.DeepEqual(got, expected[i]) {
						t.Fatalf("step %d\nnative=%+v\nGo=%+v\nreplay:\n%s", i, got, expected[i], script.String())
					}
				}
				t.Logf("seed=%x mode=%d: %d exact native snapshots", seed, mode, len(results))
			})
		}
	}
}

func TestNativePrimitiveDiscriminatingLiveOracle(t *testing.T) {
	for _, mode := range []uint8{0, 1} {
		t.Run(fmt.Sprintf("oversized-mode%d", mode), func(t *testing.T) {
			h, table := nativeFresh(t, 16, mode)
			a := new(Node)
			nodes := []*Node{a}
			expected := []nativeImage{nativeObserve(t, table, nodes, nativeOK)}
			rc := nativePut(t, h, table, a, 7, false)
			expected = append(expected, nativeObserve(t, table, nodes, rc))
			rc = nativeRemove(t, h, table, a, 7)
			expected = append(expected, nativeObserve(t, table, nodes, rc))
			script := fmt.Sprintf("new 16 %d\ninsert 1 7 0\nremove 1 7\n", mode)
			nativeComparePrimitive(t, script, expected)
		})
	}
	t.Run("checked-EEXIST-at-full-capacity", func(t *testing.T) {
		h, table := nativeFresh(t, 8, 0)
		a := new(Node)
		nodes := []*Node{a}
		script := "new 8 0\n"
		expected := []nativeImage{nativeObserve(t, table, nodes, nativeOK)}
		for range 8 {
			script += "insert 1 7 0\n"
			rc := nativePut(t, h, table, a, 7, false)
			expected = append(expected, nativeObserve(t, table, nodes, rc))
		}
		script += "insert 1 7 1\nremove 1 7\n"
		rc := nativePut(t, h, table, a, 7, true)
		nativeRequire(t, rc, nil, nativeExists)
		expected = append(expected, nativeObserve(t, table, nodes, rc))
		rc = nativeRemove(t, h, table, a, 7)
		expected = append(expected, nativeObserve(t, table, nodes, rc))
		nativeComparePrimitive(t, script, expected)
	})
}

func nativeComparePrimitive(t *testing.T, script string, expected []nativeImage) {
	t.Helper()
	results := nativeDriver(t, script)
	if len(results) != len(expected) {
		t.Fatalf("%d snapshots, want %d; replay:\n%s", len(results), len(expected), script)
	}
	for i, r := range results {
		got := r.Table.nativeImage
		got.RC = r.RC
		if !reflect.DeepEqual(got, expected[i]) {
			t.Fatalf("step %d native=%+v Go=%+v replay:\n%s", i, got, expected[i], script)
		}
	}
}

func TestNativeOpaqueLiveOracle(t *testing.T) {
	h, table, p, sn := nativeModel(t)
	a, b := nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q")
	x, y := nativeModelNode(t, h, sn[2], "QQ=="), nativeModelNode(t, h, sn[2], "Qg==")
	opaq := nativeModelNode(t, h, nil, "")
	nodes := []*Node{p.node, a.node, b.node, x.node, opaq.node, y.node}
	nativeModelLink(p, a, b, x, opaq)
	rc, err := h.event(table, x, false)
	nativeRequire(t, rc, err, nativeOK)
	if table.present {
		t.Fatal("opaque counted toward threshold")
	}
	nativeModelLink(p, a, b, x, opaq, y)
	rc, err = h.event(table, y, false)
	nativeRequire(t, rc, err, nativeOK)
	expected := nativeObserve(t, table, nodes, nativeOK)
	// Native term insertion places schema children before trailing opaque
	// children. Move the separator publicly before y while only three schema
	// children exist, then add q to cross the threshold in the separated order.
	script := "root 1 c\nterm 2 1 p p\nterm 4 1 ll QQ==\nopaque 5 1 separator value\nterm 6 1 ll Qg== 64\nbefore 6 5\nI 5\nU 5\nterm 3 1 q q\nI 5\nU 5\n"
	results := nativeDriver(t, script)
	if len(results) != 11 {
		t.Fatalf("opaque request snapshots=%d want=11", len(results))
	}
	for i, result := range results {
		foundParent := false
		for _, n := range result.Nodes {
			if n.ID != 1 {
				continue
			}
			foundParent = true
			if i < 8 {
				if n.Table.Present {
					t.Fatalf("event %d: three schema children plus opaque created native table", i)
				}
				continue
			}
			got := n.Table.nativeImage
			got.RC = result.RC
			if !n.Table.Present || !reflect.DeepEqual(got, expected) {
				t.Fatalf("event %d native=%+v Go=%+v", i, got, expected)
			}
		}
		if !foundParent || result.RC != nativeOK {
			t.Fatalf("event %d parent=%t enclosing rc=%d", i, foundParent, result.RC)
		}
	}
	// Opaque parents cannot own native children tables, even after four opaque
	// child constructions; explicit I/U exercise the parent's guard as well.
	script = "opaque 1 0 parent value\nopaque 2 1 a a\nopaque 3 1 b b\nopaque 4 1 c c\nopaque 5 1 d d\nI 2\nU 2\n"
	for _, result := range nativeDriver(t, script) {
		if result.RC != nativeOK {
			t.Fatalf("opaque-parent request failed: %d", result.RC)
		}
		for _, n := range result.Nodes {
			if n.ID != 0 && n.Table.Present {
				t.Fatal("opaque parent/child became table eligible")
			}
		}
	}
}

func TestNativeListAndSchemaPointerLiveOracle(t *testing.T) {
	for _, name := range []string{"list-events", "schema-pointer", "schema-pointer-reverse"} {
		t.Run(name, func(t *testing.T) {
			h, table, p, sn := nativeModel(t)
			a, b := nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q")
			left, right, xv, yv := sn[5], sn[5], "x", "y"
			if name != "list-events" {
				// Low-level frontier, corresponding to c/ll and d/ll in the
				// native request. G1 does not permit this through public Go APIs.
				copySchema := *sn[2]
				left, right, xv, yv = sn[2], &copySchema, "QQ==", "Qg=="
				if name == "schema-pointer-reverse" {
					left, right = right, left
				}
			}
			x, y := nativeModelNode(t, h, left, xv), nativeModelNode(t, h, right, yv)
			nodes := []*Node{p.node, a.node, b.node, x.node, y.node}
			nativeModelLink(p, a, b, x, y)
			rc, err := h.event(table, y, false)
			nativeRequire(t, rc, err, nativeOK)
			expected := []nativeImage{nativeObserve(t, table, nodes, nativeOK)}
			rc, err = h.event(table, x, true)
			nativeRequire(t, rc, err, nativeOK)
			expected = append(expected, nativeObserve(t, table, nodes, nativeOK))
			indices := []int{8, 9}
			if name == "list-events" {
				nativeModelLink(p, a, b, y)
				z := nativeModelNode(t, h, sn[5], "z")
				nodes = append(nodes, z.node)
				nativeModelLink(p, a, b, z, y)
				rc, err = h.event(table, z, false)
				nativeRequire(t, rc, err, nativeOK)
				expected = append(expected, nativeObserve(t, table, nodes, nativeOK))
				rc, err = h.event(table, z, true)
				nativeRequire(t, rc, err, nativeOK)
				expected = append(expected, nativeObserve(t, table, nodes, nativeOK))
				indices = []int{4, 6, 11, 13}
			}
			text, err := os.ReadFile(filepath.Join("..", "conformance", "oracle", "native_registration", "cases", name+".calls"))
			if err != nil {
				t.Fatal(err)
			}
			results := nativeDriver(t, string(text))
			results = slices.DeleteFunc(results, func(r nativeDriverResult) bool {
				return r.Event == "read.schema" || r.Event == "public.result"
			})
			for i, index := range indices {
				if len(results) <= index {
					t.Fatalf("missing native command %d: %d snapshots", index, len(results))
				}
				parent := false
				for _, n := range results[index].Nodes {
					if n.ID == 1 {
						parent = true
						got := n.Table.nativeImage
						got.RC = results[index].RC
						if !n.Table.Present || !reflect.DeepEqual(got, expected[i]) {
							t.Fatalf("command %d native=%+v Go=%+v", index, got, expected[i])
						}
					}
				}
				if !parent {
					t.Fatal("missing native parent")
				}
			}
			t.Logf("%s: %d exact event snapshots", name, len(expected))
		})
	}
}

func TestNativeBuildLiveOracle(t *testing.T) {
	for _, values := range [][]string{{"KpfzBg==", "QQ==", "Qg=="}, {"QQ==", "KpfzBg==", "Qg=="}} {
		t.Run(values[0], func(t *testing.T) {
			h, table, p, sn := nativeModel(t)
			var script strings.Builder
			script.WriteString("root 1 c\nterm 2 1 p p\nterm 3 1 q q\n")
			children := []*nativeChild{nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q")}
			nodes := []*Node{p.node, children[0].node, children[1].node}
			var expected []nativeImage
			for i, value := range values {
				c := nativeModelNode(t, h, sn[2], value)
				children = append(children, c)
				nodes = append(nodes, c.node)
				nativeModelLink(p, children...)
				rc, err := h.event(table, c, false)
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprintf(&script, "term %d 1 ll %s\n", i+4, value)
				if table.present {
					expected = append(expected, nativeObserve(t, table, nodes, rc))
				}
			}
			results := nativeDriver(t, script.String())
			var step int
			for _, r := range results {
				for _, n := range r.Nodes {
					if n.ID != 1 || !n.Table.Present {
						continue
					}
					got := n.Table.nativeImage
					// lyd_new_term's void insertion caller ignores helper EINT.
					// Compare its table prefix; separate native event tests cover rc.
					got.RC = expected[step].RC
					if !reflect.DeepEqual(got, expected[step]) || r.RC != nativeOK {
						t.Fatalf("step=%d native=%+v Go=%+v enclosing=%d replay:\n%s", step, got, expected[step], r.RC, script.String())
					}
					step++
				}
			}
			if step != len(expected) {
				t.Fatalf("compared %s tables, want %d", strconv.Itoa(step), len(expected))
			}
		})
	}
}

func TestNativeExistingEventsLiveOracle(t *testing.T) {
	h, table, p, sn := nativeModel(t)
	children := []*nativeChild{nativeModelNode(t, h, sn[0], "p"), nativeModelNode(t, h, sn[1], "q"), nativeModelNode(t, h, sn[2], "QQ=="), nativeModelNode(t, h, sn[2], "Qg==")}
	nativeModelLink(p, children...)
	rc, err := h.event(table, children[3], false)
	nativeRequire(t, rc, err, nativeOK)
	nodes := []*Node{p.node}
	for _, c := range children {
		nodes = append(nodes, c.node)
	}
	var script strings.Builder
	script.WriteString("root 1 c\nterm 2 1 p p\nterm 3 1 q q\nterm 4 1 ll QQ==\nterm 5 1 ll Qg==\n")
	expected := []nativeImage{nativeObserve(t, table, nodes, nativeOK)}
	// These I/U commands are explicitly low-level controls, separate from the
	// driver's public reachability scripts. U does not physically detach here.
	for _, step := range []struct {
		id     int
		unlink bool
	}{{4, false}, {4, true}, {4, true}, {5, true}, {5, false}, {4, true}, {1, false}} {
		c := p
		if step.id != 1 {
			c = children[step.id-2]
		}
		rc, err := h.event(table, c, step.unlink)
		if err != nil {
			t.Fatal(err)
		}
		command := "I"
		if step.unlink {
			command = "U"
			rc = nativeOK /* native U is void; helper stop is in diagnostics */
		}
		fmt.Fprintf(&script, "%s %d\n", command, step.id)
		expected = append(expected, nativeObserve(t, table, nodes, rc))
	}
	results := nativeDriver(t, script.String())
	var index int
	for _, r := range results {
		for _, n := range r.Nodes {
			if n.ID != 1 || !n.Table.Present {
				continue
			}
			got := n.Table.nativeImage
			got.RC = r.RC
			if index >= len(expected) || !reflect.DeepEqual(got, expected[index]) {
				t.Fatalf("event %d native=%+v expected=%+v replay:\n%s", index, got, expected, script.String())
			}
			index++
		}
	}
	if index != len(expected) {
		t.Fatalf("compared %d events, want %d", index, len(expected))
	}
}

func TestNativeHistoryScriptsLiveOracle(t *testing.T) {
	for _, name := range []string{"collision-build", "collision-resize", "repeated-pointer", "collision-history-retained", "collision-history-shrunk"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("..", "conformance", "oracle", "native_registration", "cases", name+".calls")
			text, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			h, table, parent, schemas := nativeModel(t)
			children := []*nativeChild{}
			byID := map[int]*nativeChild{1: parent}
			nodes := make([]*Node, 4096)
			nodes[0] = parent.node
			var expected []nativeImage
			var script strings.Builder
			for _, line := range strings.Split(string(text), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
					continue
				}
				fmt.Fprintln(&script, line)
				id, err := strconv.Atoi(fields[1])
				if err != nil {
					t.Fatal(err)
				}
				switch fields[0] {
				case "root", "schema", "validate", "print": // no registration writes in these controls
				case "term":
					var c *nativeChild
					for _, sn := range schemas {
						if sn.Name == fields[3] {
							c = nativeModelNode(t, h, sn, fields[4])
							break
						}
					}
					if c == nil {
						t.Fatalf("unknown term %s", line)
					}
					byID[id], nodes[id-1] = c, c.node
					// All sorted v values in these scripts arrive in increasing
					// order. This model supplies physical frontiers; it is not a
					// production sorting, change-value or validation adapter.
					at := len(children)
					for i, old := range children {
						if slices.Index(schemas, c.node.schema) < slices.Index(schemas, old.node.schema) {
							at = i
							break
						}
					}
					children = slices.Insert(children, at, c)
					nativeModelLink(parent, children...)
					if _, err := h.event(table, c, false); err != nil {
						t.Fatal(err)
					}
				case "unlink":
					c := byID[id]
					if _, err := h.event(table, c, true); err != nil {
						t.Fatal(err)
					}
					children = slices.Delete(children, slices.Index(children, c), slices.Index(children, c)+1)
					nativeModelLink(parent, children...)
					c.parent, c.next, c.prev = nil, nil, c
				case "I":
					if _, err := h.event(table, byID[id], false); err != nil {
						t.Fatal(err)
					}
				case "change":
					c := byID[id]
					if _, err := h.event(table, c, true); err != nil {
						t.Fatal(err)
					}
					replacement := nativeModelNode(t, h, c.node.schema, fields[2])
					if err := c.node.touch(); err != nil {
						t.Fatal(err)
					}
					c.node.value, c.node.hash = replacement.node.value, replacement.node.hash
					if _, err := h.event(table, c, false); err != nil {
						t.Fatal(err)
					}
				default:
					t.Fatalf("unsupported model instruction %s", line)
				}
				if table.present {
					expected = append(expected, nativeObserve(t, table, nodes, nativeOK))
				}
			}
			results := nativeDriver(t, script.String())
			var index int
			for _, r := range results {
				for _, n := range r.Nodes {
					if n.ID != 1 || !n.Table.Present {
						continue
					}
					got := n.Table.nativeImage
					got.RC = nativeOK // helper/enclosing statuses are asserted in check.py
					if index >= len(expected) || !reflect.DeepEqual(got, expected[index]) {
						t.Fatalf("%s step=%d native=%+v expected=%+v", name, index, got, expected)
					}
					index++
				}
			}
			if index != len(expected) {
				t.Fatalf("%s compared %d tables, want %d", name, index, len(expected))
			}
			t.Logf("%s: %d exact event snapshots", name, index)
		})
	}
}
