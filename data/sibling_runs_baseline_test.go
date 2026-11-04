// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/vibe-ports/yang/internal/schema"
)

const baselineRunSchema = `module baseline-run {yang-version 1.1; namespace "urn:baseline-run"; prefix r;
 container c {leaf b {type string;} leaf z {type string;} leaf a {type string;}
 leaf-list ll {type uint32;}}}`

// This helper and the probe below run unchanged on 46af50e6. All layouts are ordinary,
// including successive schema-only joins and a retained singleton before an opaque tail.
func baselineRunTree(t *testing.T, set *schema.Set, singleton bool) (*Tree, *Node, *Node, *Node) {
	t.Helper()
	tr := newTree(set)
	c, err := tr.NewInner(nil, "baseline-run:c", NewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if singleton {
		one, err := tr.NewTerm(c, "ll", "1", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		two, err := tr.NewTerm(c, "ll", "2", NewOptions{})
		if err != nil {
			t.Fatal(err)
		}
		mustLink(t, two.Unlink())
		if _, err := tr.NewOpaque(c, "zz", "u", OpaqueOptions{Namespace: "urn:baseline-run", Format: FormatXML}); err != nil {
			t.Fatal(err)
		}
		if !c.kids.rbTree[one] || len(c.kids.rbOrders[c.kids.rbGroups[one]].nodes) != 1 {
			t.Fatal("not a retained singleton")
		}
		return tr, c, one, nil
	}
	var first, second *Node
	for _, item := range []string{"ll=4", "ll=2", "b=x", "ll=9", "ll=10", "z=y", "ll=20", "ll=21"} {
		name, value, _ := strings.Cut(item, "=")
		var sn *schema.Node
		for _, child := range c.schema.Children {
			if child.Name == name {
				sn = child
				break
			}
		}
		if sn == nil {
			t.Fatal("missing schema")
		}
		n := retainedRBTerm(t, sn, value)
		mustLink(t, tr.link(c, &c.kids, n, c.kids.SchemaLen()))
		switch name {
		case "b":
			first = n
		case "z":
			second = n
		}
	}
	for _, value := range []string{"3", "11"} {
		if _, err := tr.NewTerm(c, "ll", value, NewOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	return tr, c, first, second
}

func TestRunOrdinaryRecoveryBaseline(t *testing.T) {
	set := reviewDiffSet(t, baselineRunSchema)
	// Measured by this unchanged probe on a 46af50e6 worktree in the dev container.
	// Columns are unjournaled/journaled; byte rows are amd64, amd64 race, and 386,
	// first with Go 1.27.1, then Go 1.26.8 (different batch map allocation sizes).
	want := map[string]struct {
		credit [2]uint64
		visits int64
		bytes  [6][2]uint64
	}{
		"schema-joins/arm":          {[2]uint64{94, 95}, 9, [6][2]uint64{{2272, 7800}, {2280, 7816}, {1336, 4496}, {2272, 7800}, {2280, 7816}, {1336, 4496}}},
		"schema-joins/batch":        {[2]uint64{282, 277}, 68, [6][2]uint64{{51712, 50272}, {52152, 50712}, {29208, 28352}, {52320, 50880}, {52808, 51368}, {29568, 28712}}},
		"schema-joins/first-batch":  {[2]uint64{307, 302}, 74, [6][2]uint64{{53216, 51776}, {53704, 52264}, {30104, 29248}, {53152, 51712}, {53704, 52264}, {30072, 29216}}},
		"schema-joins/first-single": {[2]uint64{23, 235}, 18, [6][2]uint64{{344, 43648}, {344, 44008}, {200, 24880}, {344, 43648}, {344, 44008}, {200, 24880}}},
		"schema-joins/plan":         {[2]uint64{16, 16}, 7, [6][2]uint64{{96, 96}, {96, 96}, {48, 48}, {96, 96}, {96, 96}, {48, 48}}},
		"schema-joins/single":       {[2]uint64{24, 221}, 18, [6][2]uint64{{152, 43456}, {152, 43784}, {88, 24768}, {152, 43456}, {152, 43784}, {88, 24768}}},
		"singleton-tail/arm":        {[2]uint64{301, 162}, 5, [6][2]uint64{{1112, 6640}, {1120, 6656}, {648, 3808}, {1112, 6640}, {1120, 6656}, {648, 3808}}},
		"singleton-tail/insert":     {[2]uint64{309, 201}, 15, [6][2]uint64{{1344, 14408}, {1352, 14424}, {800, 8080}, {1344, 14408}, {1352, 14424}, {800, 8080}}},
	}
	for _, layout := range []string{"schema-joins", "singleton-tail"} {
		for _, mode := range []string{"first-single", "first-batch", "plan", "single", "batch", "arm", "insert"} {
			if layout == "singleton-tail" && mode != "arm" && mode != "insert" || layout == "schema-joins" && mode == "insert" {
				continue
			}
			for _, journaled := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/journal=%t", layout, mode, journaled), func(t *testing.T) {
					procs := runtime.GOMAXPROCS(1)
					defer runtime.GOMAXPROCS(procs)
					gc := debug.SetGCPercent(-1)
					defer debug.SetGCPercent(gc)
					const count = 256
					type sample struct {
						tr             *Tree
						c, n, interior *Node
						journal        *hashTransaction
						end            func()
						restore        func()
						arm            func(*siblings) (func(), error)
						disarm         func()
					}
					samples := make([]sample, count)
					for i := range samples {
						s := &samples[i]
						tr, c, n, second := baselineRunTree(t, set, layout == "singleton-tail")
						if second != nil && !strings.HasPrefix(mode, "first-") {
							// The first join leaves two independent owners. The second removal
							// must keep main's single physical-owner bookkeeping in both modes.
							if mode == "batch" {
								mustLink(t, tr.unlinkAll([]*Node{n}))
							} else {
								mustLink(t, n.Unlink())
							}
							n = second
							s.interior = c.kids.docSpan().At(3)
							if !c.kids.rbTree[s.interior] || c.kids.unsorted[s.interior] {
								t.Fatal("missing sorted interior owner after first join")
							}
						}
						if c.kids.st.interleaved() {
							t.Fatal("probe must use ordinary layout")
						}
						s.tr, s.c, s.n = tr, c, n
						if journaled {
							s.journal = beginHashTransaction(tr)
						}
						s.end = tr.work.beginHashWork()
						tr.work.hashUsed = 0
						tr.work.visits = true
						tr.work.Store(0)
					}
					var before, after runtime.MemStats
					runtime.ReadMemStats(&before)
					for i := range samples {
						s := &samples[i]
						var err error
						switch mode {
						case "plan":
							_, err = prepareUnlink(s.n)
						case "single", "first-single":
							err = s.n.Unlink()
						case "batch", "first-batch":
							err = s.tr.unlinkAll([]*Node{s.n})
						case "arm":
							s.restore, s.arm, err = s.tr.linkOrigin(s.n)
							if err == nil {
								s.disarm, err = s.arm(&s.c.kids)
							}
						case "insert":
							err = s.tr.Insert(s.c, s.n)
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					runtime.ReadMemStats(&after)
					bytes := after.TotalAlloc - before.TotalAlloc
					row, column := 0, 0
					if strconv.IntSize == 32 {
						row = 2
					} else if raceEnabled {
						row = 1
					}
					if strings.HasPrefix(runtime.Version(), "go1.26.") {
						row += 3
					}
					if journaled {
						column = 1
					}
					baseline := want[layout+"/"+mode]
					// Integer bytes/op is the allocation metric used by testing.Benchmark.
					if bytes/count != baseline.bytes[row][column] {
						t.Errorf("bytes/op=%d, main=%d", bytes/count, baseline.bytes[row][column])
					}
					credit, visits := samples[0].tr.work.hashUsed, samples[0].tr.work.Load()
					if credit != baseline.credit[column] || visits != baseline.visits {
						t.Errorf("credit/visits=%d/%d, main=%d/%d", credit, visits, baseline.credit[column], baseline.visits)
					}
					for i := range samples {
						s := &samples[i]
						if s.tr.work.hashErr != nil {
							t.Fatal(s.tr.work.hashErr)
						}
						if s.tr.work.hashUsed != credit || s.tr.work.Load() != visits {
							t.Fatal("inconsistent sample work")
						}
						if s.interior != nil && s.c.kids.unsorted[s.interior] {
							t.Error("ordinary join marked an interior owner unsorted")
						}
						if s.disarm != nil {
							s.disarm()
						}
						s.end()
						var err error
						s.journal.end(&err)
						if err != nil {
							t.Fatal(err)
						}
					}
					runtime.KeepAlive(samples)
					t.Logf("credit=%d visits=%d bytes=%d per-op=%d arch=%d race=%t", credit, visits, bytes, bytes/count, strconv.IntSize, raceEnabled)
				})
			}
		}
	}
}
