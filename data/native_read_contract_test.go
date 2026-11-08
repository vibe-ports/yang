// SPDX-License-Identifier: BSD-3-Clause

//go:build oracle

package data

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A malformed contract must fail even when both peers carry the same corruption.
// Valid but different semantic state must fail in either comparison direction.
func nativeContractReject(t *testing.T, script string, valid, mutant []nativeDriverResult, both bool) {
	t.Helper()
	sides := [][2][]nativeDriverResult{{valid, mutant}, {mutant, valid}}
	if both {
		sides = append(sides, [2][]nativeDriverResult{mutant, mutant})
	}
	for i, peers := range sides {
		if err := nativeG2CompareEvidence(script, peers[0], peers[1]); err == nil {
			t.Fatalf("accepted corrupted evidence on peer combination %d", i)
		}
	}
}

func TestNativeG2SelectionContract(t *testing.T) {
	const script = "root 1 c\nterm 2 1 p same\nterm 3 1 q same\nterm 4 1 ll a\nterm 5 1 ll b\nread-instance 2 2 1\n"
	valid := nativeDriver(t, script)
	nativeG2Unmodified(t, script, valid)
	for _, field := range []string{"identity", "occurrence", "continuity"} {
		for _, control := range []string{"delete", "add", "mutate"} {
			t.Run(field+"/"+control, func(t *testing.T) {
				mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
					if r["event"] != "read.selection.instance" {
						return
					}
					for _, side := range []string{"before", "after"} {
						table := r[side].(map[string]any)
						records := []map[string]any{table}
						if field == "occurrence" {
							records = nil
							for _, x := range table["chains"].([]any) {
								records = append(records, x.(map[string]any))
							}
						}
						for _, record := range records {
							switch control {
							case "delete":
								delete(record, field)
							case "add":
								record["unexpected_"+field] = float64(1)
							case "mutate":
								if field == "continuity" {
									record[field] = "unknown"
								} else {
									record[field] = record[field].(float64) + 1000
								}
							}
						}
					}
				})
				nativeContractReject(t, script, valid, mutant, true)
			})
		}
	}
}

func TestNativeG2EqualityResultContract(t *testing.T) {
	const script = "root 1 c\nterm 2 1 p same\ndup 3 2 0 66\nequal 2 3 0\n"
	valid := nativeDriver(t, script)
	nativeG2Unmodified(t, script, valid)
	snapshot := valid[len(valid)-1].raw
	for _, control := range []string{"delete", "add-nodes", "mutate"} {
		t.Run(control, func(t *testing.T) {
			mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
				if r["event"] != "read.equal" {
					return
				}
				switch control {
				case "delete":
					delete(r, "left_generation")
					delete(r, "right_generation")
				case "add-nodes":
					for k, v := range snapshot {
						if k != "event" && k != "operation" {
							r[k] = v
						}
					}
					delete(r, "left_generation")
					delete(r, "right_generation")
				case "mutate":
					r["left_generation"] = float64(0)
				}
			})
			nativeContractReject(t, script, valid, mutant, true)
		})
	}
}

func TestNativeG2OpaqueOperandContract(t *testing.T) {
	const script = "opaque 1 0 u A\nopaque 2 0 u B\nequal 1 2 0\n"
	valid := nativeDriver(t, script)
	nativeG2Unmodified(t, script, valid)
	other := nativeDriver(t, strings.ReplaceAll(strings.ReplaceAll(script, "u A", "u C"), "u B", "u D"))
	if valid[len(valid)-1].RC != other[len(other)-1].RC {
		t.Fatal("control changed verdict")
	}
	nativeContractReject(t, script, valid, other, false)
	for _, field := range []string{"opaque_name", "opaque_module", "opaque_prefix", "opaque_value"} {
		for _, control := range []string{"delete", "add", "mutate"} {
			t.Run(field+"/"+control, func(t *testing.T) {
				mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
					for _, item := range nativeEvidenceArray(r["nodes"]) {
						n := item.(map[string]any)
						if n["id"] != float64(1) {
							continue
						}
						switch control {
						case "delete":
							delete(n, field)
						case "add":
							n["unexpected_"+field] = "added"
						case "mutate":
							n[field] = "63"
							if field == "opaque_value" {
								n[field] = "43"
							}
						}
					}
				})
				nativeContractReject(t, script, valid, mutant, control != "mutate")
			})
		}
	}
}

// The native constructor accepts byte names. They must remain distinct instead
// of both being decoded as a Unicode replacement character.
func TestNativeG2OpaqueByteNames(t *testing.T) {
	const script = "opaque 1 0 \xff A\nopaque 2 0 u B\nequal 1 2 0\n"
	valid := nativeDriver(t, script)
	nativeG2Unmodified(t, script, valid)
	other := nativeDriver(t, strings.ReplaceAll(script, "\xff", "\xfe"))
	if valid[len(valid)-1].RC != other[len(other)-1].RC {
		t.Fatal("control changed verdict")
	}
	nativeContractReject(t, script, valid, other, false)
}

func TestNativeG2EvidenceUTF8(t *testing.T) {
	var row nativeDriverResult
	if err := json.Unmarshal([]byte("{\"event\":\"\xff\",\"rc\":0}"), &row); err == nil {
		t.Fatal("accepted invalid UTF-8 native transport")
	}
}

func TestNativeG2ContextMetadataContract(t *testing.T) {
	const script = "root 1 c\nequal 1 1 0\n"
	valid := nativeDriver(t, script)
	nativeG2Unmodified(t, script, valid)
	for _, event := range []string{"driver.ready.actual-children-callback", "equal"} {
		for _, field := range []string{"modules", "metadata"} {
			for _, control := range []string{"delete", "null", "empty", "add", "mutate"} {
				if field == "metadata" && control == "empty" {
					continue
				}
				t.Run(event+"/"+field+"/"+control, func(t *testing.T) {
					mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
						if r["event"] != event {
							return
						}
						record := r
						if field == "metadata" {
							record = nil
							for _, item := range r["nodes"].([]any) {
								n := item.(map[string]any)
								if event == "driver.ready.actual-children-callback" || n["id"] == float64(1) {
									record = n
									break
								}
							}
							if record == nil {
								t.Fatal("missing audit metadata target")
							}
						}
						switch control {
						case "delete":
							delete(record, field)
						case "null":
							record[field] = nil
						case "empty":
							record[field] = []any{}
						case "add":
							record["unexpected_"+field] = []any{}
						case "mutate":
							if field == "metadata" {
								record[field] = []any{"changed"}
							} else {
								record[field].([]any)[0].(map[string]any)["namespace"] = "urn:changed"
							}
						}
					})
					nativeContractReject(t, script, valid, mutant, control != "mutate")
				})
			}
		}
	}
}

func TestNativeG2BootstrapChannelsContract(t *testing.T) {
	const script = "equal 0 0 1\n"
	valid := nativeDriver(t, script)
	nativeG2Unmodified(t, script, valid)
	for _, field := range []string{"rc", "diagnostics", "logs"} {
		for _, control := range []string{"delete", "add", "mutate"} {
			t.Run(field+"/"+control, func(t *testing.T) {
				mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
					if r["event"] != "driver.ready.actual-children-callback" {
						return
					}
					switch control {
					case "delete":
						delete(r, field)
					case "add":
						r["unexpected_"+field] = float64(7)
					case "mutate":
						switch field {
						case "rc":
							r[field] = float64(7)
						case "diagnostics":
							r[field] = []any{map[string]any{"code": float64(7), "context_generation": float64(1), "msg": "changed"}}
						case "logs":
							r[field] = []any{map[string]any{"level": float64(0), "line": float64(0), "msg": "changed"}}
						}
					}
				})
				nativeContractReject(t, script, valid, mutant, control != "mutate")
			})
		}
	}
}

// The generic control walks actual native JSON, including every nested field.
// It uses the same Python acceptance predicate as the Go and standalone checker.
func TestNativeG2EveryEmittedField(t *testing.T) {
	if os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER") == "" {
		t.Skip("set native drivers")
	}
	cmd := exec.Command("python3", "evidence_test.py")
	cmd.Dir = filepath.Join("..", "conformance", "oracle", "native_registration")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("field perturbation control: %v\n%s", err, output)
	}
	t.Log(string(output))
}

// A mutually consistent fresh registry entry must not hide storage replacement
// between the result observation and its completed read snapshot.
func TestNativeG2ReadStorageContinuity(t *testing.T) {
	for _, name := range []string{"owned", "generic"} {
		t.Run(name, func(t *testing.T) {
			script := "new 8 1\nequal 0 0 1\n"
			command := "equal"
			if name == "owned" {
				script = "root 1 c\nterm 2 1 p same\nterm 3 1 q same\nterm 4 1 ll a\nterm 5 1 ll b\nread-instance 2 2 1\n"
				command = "read-instance"
			}
			valid := nativeDriver(t, script)
			nativeG2Unmodified(t, script, valid)
			mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
				if r["event"] != command {
					return
				}
				var table map[string]any
				if name == "generic" {
					table = r["table"].(map[string]any)
				} else {
					for _, item := range r["nodes"].([]any) {
						n := item.(map[string]any)
						if n["id"] == float64(1) {
							table = n["table"].(map[string]any)
							break
						}
					}
				}
				if table == nil || table["present"] != true {
					t.Fatal("missing populated audit storage")
				}
				old, fresh := table["identity"], r["table_count"].(float64)+1
				found := false
				entries := r["tables"].([]any)
				for _, item := range entries {
					entry := item.(map[string]any)
					if entry["identity"] == old {
						entry["alive"] = false
						found = true
					}
				}
				if !found {
					t.Fatal("missing old storage registry entry")
				}
				r["tables"] = append(entries, map[string]any{"identity": fresh, "alive": true})
				r["table_count"], table["identity"] = fresh, fresh
				for _, item := range table["chains"].([]any) {
					chain := item.(map[string]any)
					chain["occurrence"] = chain["occurrence"].(float64) + 1000000
				}
			})
			nativeContractReject(t, script, valid, mutant, true)
		})
	}
}

func TestNativeG2TransientSchemaRegistry(t *testing.T) {
	const script = "parse 1 {\"m:c\":{\"p\":\"P\",\"l\":[{}]}}\nroot 2 c\nlist 3 2 l x\nprint 2\n"
	left := nativeDriverReplayPath(t, script, "", true, os.Getenv("YANG_NATIVE_REGISTRATION_DRIVER"))
	right := nativeDriverReplayPath(t, script, "", true, os.Getenv("YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER"))
	a, err := nativeCompletedCommandEvidence(script, left)
	if err != nil {
		t.Fatal(err)
	}
	b, err := nativeCompletedCommandEvidence(script, right)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.projection, b.projection) {
		t.Fatal("transient observer schemas changed current evidence")
	}
	// The transient descriptors differ before the later successful list becomes
	// live, at which point its compiled ancestry must be compared normally.
	if len(left[0].bootstrap) == 0 || len(right[0].bootstrap) == 0 {
		t.Fatal("missing bootstrap")
	}
}

func TestNativeG2ObserverFunctionAnnotation(t *testing.T) {
	const script = "new 8 1\nroot 1 c\nremove 1 0\nequal 1 1 0\n"
	valid := nativeDriver(t, script)
	nativeG2Unmodified(t, script, valid)
	found := false
	for _, row := range valid {
		if row.Event != "remove" {
			continue
		}
		for _, channel := range []string{"diagnostics", "logs"} {
			for _, item := range nativeEvidenceArray(row.raw[channel]) {
				msg, _ := item.(map[string]any)["msg"].(string)
				if strings.Contains(msg, "_nr_impl()") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("did not reach observer function annotation")
	}
	mutant := nativeG2CloneRows(t, valid, func(r map[string]any) {
		if r["event"] != "remove" {
			return
		}
		for _, channel := range []string{"diagnostics", "logs"} {
			for _, item := range nativeEvidenceArray(r[channel]) {
				d := item.(map[string]any)
				if msg, ok := d["msg"].(string); ok {
					d["msg"] = strings.ReplaceAll(msg, "argument hash", "argument changed")
				}
			}
		}
	})
	nativeContractReject(t, script, valid, mutant, false)
}

func TestNativeG2ObserverWithoutStatus(t *testing.T) {
	const script = "root 1 c\nterm 2 1 p same\ndup 6 2 0 66\ninstance 2 6\nequal 2 6 0\n"
	rows := nativeDriver(t, script)
	found := false
	for _, row := range rows {
		if row.Event == "read.compare" {
			found = true
			if _, ok := row.raw["rc"]; ok {
				t.Fatal("unexpected status on comparison observer")
			}
		}
	}
	if !found {
		t.Fatal("did not reach comparison observer")
	}
	nativeG2Unmodified(t, script, rows)
}
