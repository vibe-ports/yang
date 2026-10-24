// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

// TestOpIndexMetaAfterSplit replays ops/opidx-meta-after-split. The kept input
// splits the output v run. lydjson_parse_attribute finds the first v in sibling
// order and lydjson_meta_attr advances with node->next, so metadata item 3
// reaches in instead of v3 and libyang reports a missing data instance.
func TestOpIndexMetaAfterSplit(t *testing.T) {
	fixture := readOpsFixture(t, filepath.Join(opsManifest, "opidx-meta-after-split.yaml"))
	b, err := os.ReadFile(filepath.Join("../conformance/corpus", fixture.dir, fixture.golden))
	if err != nil {
		t.Fatal(err)
	}
	var want opsGolden
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}

	set := opsSet(t, fixture)
	_, op, diags, err := parseOp(context.Background(), strings.NewReader(fixture.rpc),
		set, FormatJSON, opRPC, nil, fixture.unknown)
	if err != nil {
		t.Fatal("rpc", err, diags)
	}
	if !fixture.keepInput {
		t.Fatal("fixture must keep the request input")
	}
	_, _, diags, err = parseOp(context.Background(), strings.NewReader(fixture.data),
		set, FormatJSON, opReply, op, fixture.unknown)
	if err == nil {
		t.Fatal("reply parse succeeded, want golden failure")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error %T %v, want ValidationError", err, err)
	}
	if got := ve.RC(); got != want.RC.Name {
		t.Errorf("rc = %s, want %s", got, want.RC.Name)
	}
	var got []string
	for _, d := range diags {
		got = append(got, fmt.Sprintf("%s %s %s %s", d.Code, d.DataPath, d.SchemaPath, d.Msg))
	}
	if w := want.diags(); !reflect.DeepEqual(got, w) {
		t.Errorf("diagnostics\n got  %q\n want %q", got, w)
	}
}

func opidxCheckRecords(t *testing.T, s *siblings) {
	t.Helper()
	if err := s.checkRecords(); err != nil {
		t.Error(err)
	}
}

func opidxR6SetPair(t *testing.T, source, target string) (*schema.Set, *schema.Set) {
	t.Helper()
	dir := t.TempDir()
	sets := make([]*schema.Set, 0, 2)
	for i, text := range []string{source, target} {
		subdir := filepath.Join(dir, fmt.Sprintf("ctx%d", i+1))
		if err := os.MkdirAll(subdir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(subdir, "m.yang"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		sets = append(sets, opsSet(t, opsFixture{dir: subdir, searchdirs: []string{"."}, modules: []struct {
			name, revision string
			features       []string
		}{{name: "m"}}}))
	}
	return sets[0], sets[1]
}

// TestOpIndexRecordUniqueness covers a user-ordered source copied into a
// system-ordered target. Removing sep joins [5] and [1] while both nodes still
// own first-instance records. Building the missing sorted tree makes 1 first,
// but lyht_insert(check=1) permits only one record for that node pointer.
func TestOpIndexRecordUniqueness(t *testing.T) {
	const source = `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r { input {
    leaf a { type string; }
    leaf a2 { type string; }
    leaf-list v { type uint32; ordered-by user; }
    leaf sep { type string; }
  } }
}`
	const target = `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r { input {
    leaf a { type string; }
    leaf a2 { type string; }
    leaf-list v { type uint32; }
    leaf sep { type string; }
  } }
}`
	set1, set2 := opidxR6SetPair(t, source, target)
	mod1 := opIndexModule(t, set1)
	rpc1 := mod1.Child(nil, "r")
	a1 := schema.FindChild(rpc1, nil, mod1, "a", 0)
	a2s := schema.FindChild(rpc1, nil, mod1, "a2", 0)
	vs1 := schema.FindChild(rpc1, nil, mod1, "v", 0)
	sep1 := schema.FindChild(rpc1, nil, mod1, "sep", 0)
	if rpc1 == nil || a1 == nil || a2s == nil || vs1 == nil || sep1 == nil {
		t.Fatal("source schema not compiled")
	}

	srcTree := newTree(set1)
	src := newInner(rpc1)
	mustLink(t, srcTree.insert(nil, src, insertDefault))
	for _, n := range []*Node{
		opidxStringTerm(t, a1, "a"),
		opidxStringTerm(t, a2s, "a2"),
		opIndexTerm(t, vs1, 5),
		opidxStringTerm(t, sep1, "split"),
		opIndexTerm(t, vs1, 1),
	} {
		mustLink(t, srcTree.insert(src, n, insertLast))
	}
	opidxCheckRecords(t, &src.kids)

	op, err := dupTo(src, set2, nil, dupRecursive, false)
	if err != nil {
		t.Fatal(err)
	}
	tr := treeOf(op)
	mod2 := opIndexModule(t, set2)
	rpc2 := mod2.Child(nil, "r")
	vs2 := schema.FindChild(rpc2, nil, mod2, "v", 0)
	if vs2 == nil {
		t.Fatal("target v schema not compiled")
	}
	sep := opidxChildNamed(op, "sep")
	if sep == nil {
		t.Fatal("copied separator not found")
	}
	if err := sep.Remove(); err != nil {
		t.Fatal(err)
	}
	values := opidxSchemaNodes(op, vs2)
	if len(values) != 2 {
		t.Fatalf("joined run has %d v instances, want 2", len(values))
	}
	if got := []string{values[0].value.Canonical(), values[1].value.Canonical()}; !slices.Equal(got, []string{"5", "1"}) {
		t.Fatalf("joined run = %v, want [5 1]", got)
	}
	if got := opidxRecords(op, vs2); !slices.Equal(got, []string{"5", "1"}) {
		t.Fatalf("records before sorted insertion = %v, want [5 1]", got)
	}

	one := values[1]
	mustLink(t, tr.insert(op, opIndexTerm(t, vs2, 6), insertDefault))
	opidxCheckRecords(t, &op.kids)
	count := 0
	for _, n := range op.kids.records(vs2) {
		if n == one {
			count++
		}
	}
	if count != 1 {
		t.Errorf("node 1 has %d first-instance records, want exactly 1", count)
	}

	if err := one.Remove(); err != nil {
		t.Fatal(err)
	}
	opidxCheckRecords(t, &op.kids)
	if _, ok := op.kids.leaderByNode[one]; ok {
		t.Error("removed node 1 remains in the record node index")
	}
	for _, n := range op.kids.records(vs2) {
		if n == one {
			t.Error("removed node 1 remains in the record chain")
		}
	}
	if got := tr.schemaLookup(&op.kids, vs2); got == one || got != nil && got.parent != op {
		t.Errorf("first-instance lookup returned detached node %p (%s)", got, opidxLabel(got))
	}
}

func TestOpIndexRecordRandomized(t *testing.T) {
	const model = `module m {
  yang-version 1.1;
  namespace "urn:m";
  prefix m;
  rpc r {
    input {
      leaf a { type string; }
      leaf a2 { type string; }
      leaf-list is { type uint32; }
      leaf-list iu { type uint32; ordered-by user; }
    }
    output {
      leaf-list os { type uint32; }
      leaf-list ou { type uint32; ordered-by user; }
      leaf-list sep { type uint32; ordered-by user; }
    }
  }
}`
	set1, set2 := opidxR6SetPair(t, model, model)
	type seed struct{ a, b uint64 }
	for _, sd := range []seed{{1, 2}, {0x241, 6}, {0xfeed, 0xbeef}} {
		t.Run(fmt.Sprintf("%x-%x", sd.a, sd.b), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(sd.a, sd.b)) //nolint:gosec // deterministic test data
			mod1 := opIndexModule(t, set1)
			rpc1 := mod1.Child(nil, "r")
			find := func(name string, output bool) *schema.Node {
				t.Helper()
				var opts schema.GetNextOpt
				if output {
					opts = schema.GetNextOutput
				}
				sn := schema.FindChild(rpc1, nil, mod1, name, opts)
				if sn == nil {
					t.Fatalf("schema %s not compiled", name)
				}
				return sn
			}
			a := find("a", false)
			a2s := find("a2", false)
			is := find("is", false)
			iu := find("iu", false)
			os := find("os", true)
			ou := find("ou", true)
			sepSchema := find("sep", true)
			inputTerms := []*schema.Node{is, iu}

			tr := newTree(set1)
			op := newInner(rpc1)
			mustLink(t, tr.insert(nil, op, insertDefault))
			for _, n := range []*Node{
				opidxStringTerm(t, a, "a"),
				opidxStringTerm(t, a2s, "a2"),
				opIndexTerm(t, is, 5),
				opIndexTerm(t, sepSchema, 1),
				opIndexTerm(t, is, 1),
			} {
				mustLink(t, tr.insert(op, n, insertLast))
			}

			type copyState struct {
				op *Node
			}
			var copies []copyState
			nextValue := 100
			fresh := func() int {
				nextValue++
				return nextValue
			}
			add := func(parent *Node, set *schema.Set, mod *schema.Module, name string, value int, output bool) *Node {
				t.Helper()
				b := newBuilder{set: set, log: &logger{set: set}}
				n, err := b.newTerm(parent, mod, name, fmt.Sprint(value), newValOptions{output: output})
				if err = b.log.done(err); err != nil {
					t.Fatal(err)
				}
				return n
			}
			children := func(parent *Node) []*Node {
				return slices.Collect(parent.kids.all())
			}
			leafLists := func(parent *Node) []*Node {
				var out []*Node
				for n := range parent.kids.all() {
					if n.schema != nil && n.schema.Kind == schema.LeafList {
						out = append(out, n)
					}
				}
				return out
			}
			checkAll := func() {
				t.Helper()
				opidxAssertTreeInvariant(t, tr)
				opidxCheckRecords(t, &op.kids)
				for i := range copies {
					opidxAssertTreeInvariant(t, treeOf(copies[i].op))
					opidxCheckRecords(t, &copies[i].op.kids)
				}
				if t.Failed() {
					t.FailNow()
				}
			}
			// Guarantee that the sequence includes each input/output ordering
			// combination and an output separator, independently of random choices.
			add(op, set1, mod1, "iu", fresh(), false)
			add(op, set1, mod1, "os", fresh(), true)
			add(op, set1, mod1, "ou", fresh(), true)
			add(op, set1, mod1, "sep", fresh(), true)
			checkAll()

			for step := range 320 {
				kind := rng.IntN(9)
				if step < 9 {
					kind = step
				}
				switch kind {
				case 0: // input leaf-list insertion, user- or system-ordered
					name := inputTerms[rng.IntN(len(inputTerms))].Name
					add(op, set1, mod1, name, fresh(), false)
				case 1: // output leaf-list or separator insertion
					name := []*schema.Node{os, ou, sepSchema}[rng.IntN(3)].Name
					add(op, set1, mod1, name, fresh(), true)
				case 2: // single removal
					all := children(op)
					if len(all) != 0 {
						if err := all[rng.IntN(len(all))].Remove(); err != nil {
							t.Fatal(err)
						}
					}
				case 3: // bulk removal
					all := children(op)
					if len(all) != 0 {
						n := 1 + rng.IntN(min(4, len(all)))
						rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
						if err := tr.unlinkAll(all[:n]); err != nil {
							t.Fatal(err)
						}
					}
				case 4: // value change and hash replacement
					all := leafLists(op)
					if len(all) != 0 {
						n := all[rng.IntN(len(all))]
						if rc := tr.changeTerm(n, fmt.Sprint(fresh())); rc != "" {
							t.Fatalf("changeTerm: %s", rc)
						}
					}
				case 5: // recursive copy of the live operation
					dup, err := dupTo(op, nil, nil, dupRecursive, false)
					if err != nil {
						t.Fatal(err)
					}
					copies = append(copies, copyState{op: dup})
				case 6: // recursive cross-context copy with a populated table
					scratchTree := newTree(set1)
					scratch := newInner(rpc1)
					mustLink(t, scratchTree.insert(nil, scratch, insertDefault))
					add(scratch, set1, mod1, "a", fresh(), false)
					add(scratch, set1, mod1, "a2", fresh(), false)
					add(scratch, set1, mod1, "is", fresh(), false)
					add(scratch, set1, mod1, "iu", fresh(), false)
					add(scratch, set1, mod1, "is", fresh(), false)
					dup, err := dupTo(scratch, set2, nil, dupRecursive, false)
					if err != nil {
						t.Fatal(err)
					}
					copies = append(copies, copyState{op: dup})
				case 7: // merge two runs with records, then trigger sorting
					add(op, set1, mod1, "is", fresh(), false)
					separator := add(op, set1, mod1, "sep", fresh(), true)
					add(op, set1, mod1, "is", fresh(), false)
					if err := separator.Remove(); err != nil {
						t.Fatal(err)
					}
					add(op, set1, mod1, "is", fresh(), false)
				case 8: // recursive copy, then build a missing sorted index
					scratchTree := newTree(set1)
					scratch := newInner(rpc1)
					mustLink(t, scratchTree.insert(nil, scratch, insertDefault))
					hi, lo := fresh()+4, fresh()
					for _, n := range []*Node{
						opidxStringTerm(t, a, fmt.Sprint(fresh())),
						opidxStringTerm(t, a2s, fmt.Sprint(fresh())),
						opIndexTerm(t, is, hi),
						opIndexTerm(t, is, lo),
					} {
						mustLink(t, scratchTree.insert(scratch, n, insertLast))
					}
					dup, err := dupTo(scratch, nil, nil, dupRecursive, false)
					if err != nil {
						t.Fatal(err)
					}
					add(dup, set1, mod1, "is", fresh(), false)
					copies = append(copies, copyState{op: dup})
				}
				checkAll()
			}
		})
	}
}
