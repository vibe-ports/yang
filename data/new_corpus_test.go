// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/compile"
	"github.com/vibe-ports/yang/internal/schema"
)

// TestNewCorpus replays the oracle fixtures ut-new/* against newBuilder: the sequence edits
// insert_term, insert_inner, insert_list (lyd_new_list3), insert_list2 and insert_opaq
// (lyd_new_opaq/2), each then lyd_insert_sibling at the top level, with their LYD_NEW_VAL_*
// options and opaque parents, and the step change_term (lyd_change_term/_canon). Every step's
// return code (change_term: its status) and full diagnostics (code, validation code, message,
// data and schema path), the tree's paths, flags and values after it, and the printed dump. The
// engine runs the same fixtures once #99 wires the steps.
func TestNewCorpus(t *testing.T) {
	dir := filepath.Join("..", "conformance", "corpus")
	frags, err := filepath.Glob(filepath.Join(dir, "manifest.d", "ut-new", "*.yaml"))
	if err != nil || len(frags) == 0 {
		t.Fatal("no ut-new fixtures", err)
	}
	for _, frag := range frags {
		id := strings.TrimSuffix(filepath.Base(frag), ".yaml")
		t.Run(id, func(t *testing.T) {
			src, err := os.ReadFile(frag) //nolint:gosec // corpus path
			if err != nil {
				t.Fatal(err)
			}
			_, line, _ := strings.Cut(string(src), "request: ") // one line of JSON
			line, _, _ = strings.Cut(line, "\n")
			var req struct {
				Searchdirs []string
				Modules    []struct{ Name string }
				Steps      []map[string]any
			}
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "ut-new", "golden", id+".json")) //nolint:gosec // corpus path
			if err != nil {
				t.Fatal(err)
			}
			var golden struct{ Steps []goldenStep }
			if err := json.Unmarshal(raw, &golden); err != nil {
				t.Fatal(err)
			}
			set := corpusSet(t, filepath.Join(dir, "ut-new", req.Searchdirs[0]), req.Modules[0].Name)
			tr := newTree(set)
			for i, st := range req.Steps {
				g := golden.Steps[i]
				if g.Skipped {
					break
				}
				rc, diags := "LY_SUCCESS", []string(nil)
				switch st["do"] {
				case "parse":
					o := parseOpts{ParseOptions: ParseOptions{ParseOnly: st["parse_only"] == true}}
					if tr, _, err = parseWith(context.Background(), strings.NewReader(st["data"].(string)), set, o, parseJSON, nil); err != nil {
						t.Fatal(err)
					}
				case "edit":
					rc, diags = replayInsert(t, set, tr, st)
				case "change_term":
					var change string
					change, diags = replayChange(t, set, tr, st)
					if change != g.Change.Name {
						t.Errorf("step %d change: %s, want %s", i, change, g.Change.Name)
					}
					if change != "LY_EEXIST" && change != "LY_ENOT" {
						rc = change
					}
				case "dump":
					var j, x bytes.Buffer
					o := PrintOptions{WithDefaults: WDAllTagged}
					if tr.PrintJSON(&j, o) != nil || tr.PrintXML(&x, o) != nil {
						t.Fatal("print")
					}
					if j.String() != g.Tree.JSON || x.String() != g.Tree.XML {
						t.Errorf("step %d dump: %q %q\nwant %q %q", i, j.String(), x.String(), g.Tree.JSON, g.Tree.XML)
					}
				}
				var want []string
				for i, d := range g.diags() {
					want = append(want, g.Diagnostics[i].Code.Name+" "+d)
				}
				if rc != g.RC.Name || !reflect.DeepEqual(diags, want) {
					t.Errorf("step %d: %s %q\nwant %s %q", i, rc, diags, g.RC.Name, want)
				}
				if got, w := typedDump(tr), g.dump(); !reflect.DeepEqual(got, w) {
					t.Errorf("step %d tree:\n got  %q\n want %q", i, got, w)
				}
			}
		})
	}
}

// corpusSet compiles module name from the fixture's search directory, without ietf-yang-library.
func corpusSet(t *testing.T, dir, name string) *schema.Set {
	t.Helper()
	c, _, err := compile.NewContext(compile.Options{NoYangLibrary: true}, os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, diags, err := c.Load(name, "", nil); err != nil {
		t.Fatal(err, diags)
	}
	set := &schema.Set{}
	for _, m := range c.Modules {
		set.Modules = append(set.Modules, m.Schema)
	}
	return set
}

// replayResult is the rc and diagnostics of a builder call, as the oracle reports them.
func replayResult(t *testing.T, err error) (rc string, diags []string) {
	t.Helper()
	if err == nil {
		return "LY_SUCCESS", nil
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("not a *ValidationError: %v", err)
	}
	for _, d := range ve.Diags {
		diags = append(diags, d.Err+" "+d.Code+" "+d.DataPath+" "+d.SchemaPath+" "+d.Msg)
	}
	return ve.RC(), diags
}

// replayInsert runs an edit step's insert_* as lyoracle does: the lyd_new_* call under the node at
// "parent" (a path) or the top-level opaque node "parent_opaq", else lyd_insert_sibling of the
// new node into the tree.
func replayInsert(t *testing.T, set *schema.Set, tr *Tree, st map[string]any) (rc string, diags []string) {
	t.Helper()
	var kind string
	var o map[string]any
	for _, k := range []string{"insert_term", "insert_inner", "insert_list", "insert_list2", "insert_opaq"} {
		if v, ok := st[k].(map[string]any); ok {
			kind, o = k, v
		}
	}
	str := func(k string) string { s, _ := o[k].(string); return s }
	var parent *Node
	if p := str("parent"); p != "" {
		var err error
		if parent, err = tr.Find(p); err != nil || parent == nil {
			t.Fatalf("parent %s: %v", p, err)
		}
	}
	if p := str("parent_opaq"); p != "" {
		for n := range tr.Top() {
			if n.schema == nil && n.Name() == p {
				parent = n
				break
			}
		}
	}
	var opts newValOptions
	opt, _ := o["options"].([]any)
	for _, f := range opt {
		switch f {
		case "output":
			opts.output = true
		case "store_only":
			opts.storeOnly = true
		case "canon":
			opts.canon = true
		}
	}
	b := newBuilder{set: set, log: &logger{set: set}}
	mod := set.Implemented(str("module"))
	var n *Node
	var err error
	switch kind {
	case "insert_term":
		n, err = b.newTerm(parent, mod, str("name"), str("value"), opts)
	case "insert_inner":
		n, err = b.newInner(parent, mod, str("name"), opts.output)
	case "insert_list":
		var keys []string
		if ks, ok := o["keys"].([]any); ok {
			keys = []string{}
			for _, k := range ks {
				s, _ := k.(string) // null: libyang's NULL, an empty value
				keys = append(keys, s)
			}
		}
		n, err = b.newList(parent, mod, str("name"), keys, opts, "lyd_new_list3")
	case "insert_list2":
		n, err = b.newList2(parent, mod, str("name"), str("keys"), opts)
	case "insert_opaq":
		n, err = b.newOpaq(parent, str("name"), str("value"), str("prefix"), str("module"), o["xml"] == true)
	}
	if err = b.log.done(err); err == nil && parent == nil {
		tr.insert(nil, n, insertDefault) // lyd_insert_sibling
	}
	return replayResult(t, err)
}

// replayChange runs a change_term step: its status (the rc name) and diagnostics. libyang's
// argument checks of lyd_change_term log without a context (LY_CHECK_ARG_RET(NULL, …)), so the
// oracle collects no item for them, which Go reports.
func replayChange(t *testing.T, set *schema.Set, tr *Tree, st map[string]any) (string, []string) {
	t.Helper()
	n, err := tr.Find(st["node"].(string))
	if err != nil || n == nil {
		t.Fatalf("change_term node %v: %v", st["node"], err)
	}
	b := newBuilder{set: set, log: &logger{set: set}}
	rc, diags := replayResult(t, b.log.done(b.changeTerm(n, st["value"].(string), st["canon"] == true)))
	if rc == "LY_EINVAL" && len(diags) == 1 && strings.Contains(diags[0], " Invalid argument ") {
		diags = nil
	}
	return rc, diags
}
