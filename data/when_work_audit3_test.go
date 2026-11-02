// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// Keep this test compatible with origin/main so the same operation can be
// measured there. Duplicate-heavy input covers both when and metadata queues.
// Setup is excluded from allocation and work measurements.
func TestWhenDeletionPlanCountedWork(t *testing.T) {
	set := reviewDiffSet(t, `module m { yang-version 1.1; namespace "urn:m"; prefix m;
 leaf keep { type string; when "true()"; }
 container gone { presence "gone"; when "false()"; leaf-list x { config false; type string; } }
 }`)
	for _, duplicates := range []bool{false, true} {
		var previousHash, previousBytes uint64
		for _, size := range []int{512, 4096} {
			t.Run(fmt.Sprintf("duplicates=%v/n=%d", duplicates, size), func(t *testing.T) {
				var b strings.Builder
				b.WriteString(`{"m:keep":"v","m:gone":{"x":[`)
				width := size
				if duplicates {
					width = 1
				}
				for i := range width {
					if i > 0 {
						b.WriteByte(',')
					}
					fmt.Fprintf(&b, `"%d"`, width-i)
				} // adverse insertion order
				b.WriteString(`]}}`)
				tr, _, err := parseWith(context.Background(), strings.NewReader(b.String()), set,
					parseOpts{ParseOptions: ParseOptions{Unknown: Opaque, ParseOnly: true, WhenTrue: true}, whenTrue: true}, parseJSON, nil)
				if err != nil {
					t.Fatal(err)
				}
				keep, gone := tr.top.At(0), tr.top.At(1)
				q := &nodeSet{items: []*Node{keep, gone}}
				var mq []*meta
				if duplicates {
					for range size - 1 {
						q.add(gone)
					}
					m := &meta{name: "gone"}
					gone.meta = []*meta{m}
					for range size {
						mq = append(mq, m)
					}
				}
				ctx := &cancelAtContext{Context: context.Background()}
				vc := &valCtx{t: tr, log: &logger{set: set}, nodeWhen: q, nodeTypes: &nodeSet{}, metaTypes: &mq, budget: xpathBudget{ctx: ctx}}
				tr.work.hashUsed = 0
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				if err := vc.unres(); err != nil {
					t.Fatal(err)
				}
				runtime.ReadMemStats(&after)
				allocated := after.TotalAlloc - before.TotalAlloc
				t.Logf("hash=%d bytes=%d checks=%d xpath=%d position-builds=%d", tr.work.hashUsed, allocated, ctx.calls, vc.budget.steps, vc.posBuilds)
				if tr.top.len() != 1 || tr.top.first() != keep || q.len() != 0 || len(mq) != 0 || vc.posBuilds > 1 {
					t.Fatal("deletion or position reuse failed")
				}
				if ctx.calls > 10*(width+size+1) {
					t.Fatal("nonlinear cancellation work")
				}
				if previousHash != 0 && (tr.work.hashUsed > 9*previousHash || allocated > 16*previousBytes+(64<<10)) {
					t.Fatal("nonlinear deletion work")
				}
				previousHash, previousBytes = tr.work.hashUsed, allocated
			})
		}
	}
}
