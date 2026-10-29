// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"iter"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/vibe-ports/yang"
	"github.com/vibe-ports/yang/internal/opidx"
	"github.com/vibe-ports/yang/internal/schema"
)

// storageWrites are the sequence and slot methods that change sibling storage (design 14).
var storageWrites = map[string]bool{"InsertBefore": true, "InsertAfter": true, "Remove": true,
	"MoveBefore": true, "MoveRange": true, "Splice": true, "ReplaceRange": true, "ReplaceHandles": true,
	"Republish": true, "Set": true}

// Check tests as well as production, so new callers cannot write sibling storage, or a node's
// slot, past the facade: a write method called on a store's sequence or on a slot.
func siblingStorageViolations(fset *token.FileSet, file *ast.File) []token.Position {
	var found []token.Position
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !storageWrites[sel.Sel.Name] {
			return true
		}
		if recv, ok := sel.X.(*ast.SelectorExpr); ok && (recv.Sel.Name == "seq" || recv.Sel.Name == "slot") {
			found = append(found, fset.Position(sel.Sel.Pos()))
		}
		return true
	})
	return found
}

func TestSiblingStorageBoundary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || entry.Name() == "siblings_access.go" || entry.Name() == "siblings_access_test.go" {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, pos := range siblingStorageViolations(fset, file) {
			t.Errorf("%s: sibling storage access outside facade", pos)
		}
	}
}

func TestSiblingStorageBoundaryRejectsAliases(t *testing.T) {
	for _, src := range []string{
		"package data;func f(s *siblings){s.st.seq.InsertBefore(nil, nil)}",
		"package data;func f(s *siblings){alias:=s.st;alias.seq.Remove(nil)}",
		"package data;func f(n *Node){n.slot.Set(nil, nil)}",
	} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "bypass.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(siblingStorageViolations(fset, file)) != 1 {
			t.Fatalf("did not reject %s", src)
		}
	}
}

func TestSiblingFacadeModel(t *testing.T) {
	for _, seed := range []uint64{1, 257, 65537} {
		rng := rand.New(rand.NewPCG(seed, 240)) //nolint:gosec // reproducible model input
		var s siblings
		var model []*Node
		schemaLen := 0
		fresh := func(opaque bool) *Node {
			n := &Node{}
			if !opaque {
				n.schema = &schema.Node{Kind: schema.Leaf}
			}
			return n
		}
		for step := 0; step < 800; step++ {
			switch rng.IntN(5) {
			case 0, 1:
				opaque := rng.IntN(2) == 0
				lo, hi := 0, schemaLen
				if opaque {
					lo, hi = schemaLen, len(model)
				}
				at := lo + rng.IntN(hi-lo+1)
				n := fresh(opaque)
				if err := s.InsertAt(at, n); err != nil {
					t.Fatal(err)
				}
				model = slices.Insert(model, at, n)
				if !opaque {
					schemaLen++
				}
			case 2:
				if len(model) > 0 {
					at := rng.IntN(len(model))
					if err := s.DeleteAt(at); err != nil {
						t.Fatal(err)
					}
					model = slices.Delete(model, at, at+1)
					if at < schemaLen {
						schemaLen--
					}
				}
			case 3:
				// republish a schema run in another order (replaceSchema)
				a := rng.IntN(schemaLen + 1)
				b := a + rng.IntN(schemaLen-a+1)
				run := slices.Clone(model[a:b])
				rng.Shuffle(len(run), func(i, j int) { run[i], run[j] = run[j], run[i] })
				mustLink(t, s.replaceSchema(a, b, spanOf(run)))
				copy(model[a:b], run)
			case 4:
				// publish a schema run with new members (publishRun)
				a := rng.IntN(schemaLen + 1)
				b := a + rng.IntN(schemaLen-a+1)
				run := slices.Clone(model[a:b])
				for range rng.IntN(4) {
					run = append(run, fresh(false))
				}
				rng.Shuffle(len(run), func(i, j int) { run[i], run[j] = run[j], run[i] })
				mustLink(t, s.publishRun(a, b, run))
				model = slices.Replace(model, a, b, run...)
				schemaLen += len(run) - (b - a)
			}
			if err := storeCheckInvariant(&s); err != nil {
				t.Fatalf("seed=%d step=%d: %v", seed, step, err)
			}
			if s.Len() != len(model) || s.SchemaLen() != schemaLen || s.OpaqueLen() != len(model)-schemaLen {
				t.Fatalf("seed=%d step=%d lengths", seed, step)
			}
			if got := collectSiblingIterator(s.All()); !slices.Equal(got, model) {
				t.Fatalf("seed=%d step=%d iteration", seed, step)
			}
			if got := collectSiblingIterator(s.SchemaAll()); !slices.Equal(got, model[:schemaLen]) {
				t.Fatal("schema iteration")
			}
			for i, n := range model {
				if s.At(i) != n || s.Index(n) != i {
					t.Fatal("At/Index")
				}
			}
			lo := rng.IntN(len(model) + 1)
			hi := lo + rng.IntN(len(model)-lo+1)
			if !slices.Equal(collectSiblingIterator(s.Range(lo, hi)), model[lo:hi]) || !slices.Equal(s.CollectRange(lo, hi), model[lo:hi]) {
				t.Fatal("bounded range")
			}
			positions := map[*Node]int{}
			for i, n := range model {
				positions[n] = i
			}
			cut := lo + rng.IntN(hi-lo+1)
			if got := s.LowerBound(lo, hi, func(n *Node) bool { return positions[n] >= cut }); got != cut {
				t.Fatalf("LowerBound=%d want %d", got, cut)
			}
			count := 0
			for range s.All() {
				count++
				break
			}
			if count != min(1, len(model)) {
				t.Fatal("iterator stop")
			}
		}
	}
}

func TestSiblingFacadeStorageRollback(t *testing.T) {
	for _, mutation := range []string{"insert", "insert-opaque", "delete", "publish", "publish-run", "compact", "transfer", "reset"} {
		t.Run(mutation, func(t *testing.T) {
			tr := newTree(nil)
			a, b, c := &Node{schema: &schema.Node{}}, &Node{schema: &schema.Node{}}, &Node{}
			mustLink(t, testBuild(&tr.top, a, b, c))
			tr.top.setWork(&tr.work)
			destination := siblings{work: &tr.work}
			before := tr.top.CollectRange(0, tr.top.Len())
			handles := []*opidx.Handle[*Node]{a.slot.Get(), b.slot.Get(), c.slot.Get()}
			added := &Node{schema: &schema.Node{}}
			j := beginHashTransaction(tr)
			switch mutation {
			case "insert":
				mustLink(t, tr.top.InsertAt(1, added))
			case "insert-opaque":
				added = &Node{}
				mustLink(t, tr.top.InsertAt(tr.top.Len(), added))
			case "delete":
				mustLink(t, tr.top.DeleteAt(0))
			case "publish":
				mustLink(t, tr.top.replaceSchema(0, 2, spanOf([]*Node{b, a})))
			case "publish-run":
				mustLink(t, tr.top.publishRun(0, 2, []*Node{b, added, a}))
			case "compact":
				mustLink(t, tr.top.deleteMatching(func(n *Node) bool { return n == a }, func(n *Node) bool { return n == c }))
			case "transfer":
				mustLink(t, destination.transferFrom(&tr.top))
			case "reset":
				mustLink(t, tr.top.reset())
			}
			if tr.work.hashUsed == 0 {
				t.Fatal("mutation did not journal/charge storage")
			}
			err := yang.ErrBudget
			j.end(&err)
			if !slices.Equal(tr.top.CollectRange(0, tr.top.Len()), before) || destination.Len() != 0 {
				t.Fatal("rollback failed")
			}
			for i, n := range before {
				if n.slot.Get() != handles[i] || tr.top.Index(n) != i {
					t.Fatalf("rollback did not restore the handle of position %d", i)
				}
			}
			if added.slot.Get() != nil || tr.top.handle(added) != nil {
				t.Fatal("a rolled-back insertion left its node slotted")
			}
			if err := storeCheckInvariant(&tr.top); err != nil {
				t.Fatal(err)
			}
			if tr.top.work != &tr.work {
				t.Fatal("rollback lost work binding")
			}
			mustLink(t, tr.top.InsertAt(1, &Node{schema: &schema.Node{}}))
			if tr.top.Len() != 4 || storeCheckInvariant(&tr.top) != nil {
				t.Fatal("insertion after rollback")
			}
		})
	}
}

// TestSiblingFacadeView: a view holds nodes of another set without re-slotting them; both sets
// find the node, and the owner's storage is unchanged.
func TestSiblingFacadeView(t *testing.T) {
	var owner siblings
	n, m := &Node{schema: &schema.Node{}}, &Node{}
	mustLink(t, testBuild(&owner, n, m))
	h := n.slot.Get()
	var view siblings
	view.view(n, m)
	if n.slot.Get() != h || owner.Index(n) != 0 || view.Index(n) != 0 || view.Index(m) != 1 {
		t.Fatal("a view changed a slot or cannot find its nodes")
	}
	if err := storeCheckInvariant(&owner); err != nil {
		t.Fatal(err)
	}
}

func collectSiblingIterator(seq iter.Seq2[int, *Node]) []*Node {
	var out []*Node
	for _, n := range seq {
		out = append(out, n)
	}
	return out
}

func TestSiblingFacadeAnyTransferThenInsert(t *testing.T) {
	tr, a, _ := anyFixture()
	b := &schemaBuilder{t: t, set: tr.set}
	cSchema := b.add(a.Module, nil, schema.Container, "c", nil)
	vSchema := b.add(a.Module, cSchema, schema.LeafList, "v", &schema.Type{Base: schema.String})
	source := newTree(tr.set)
	container := newInner(cSchema)
	mustLink(t, source.insert(nil, container, insertDefault))
	for _, value := range []string{"d", "b", "c", "a"} {
		mustLink(t, source.insert(container, b.term(vSchema, value), insertDefault))
	}
	if container.kids.ht == nil {
		t.Fatal("test did not reach indexed children")
	}
	owner, _, err := newAnyWork(context.Background(), Budget{}).createAny(tr, a, container, nil, hintContainer, true, false)
	if err != nil {
		t.Fatal(err)
	}
	mustLink(t, tr.insert(nil, owner, insertDefault))
	if source.top.Len() != 0 || owner.any.children.At(0) != container || container.parent != owner {
		t.Fatal("forest transfer failed")
	}
	if _, err := tr.NewTerm(container, "v", "e", NewOptions{}); err != nil {
		t.Fatal(err)
	}
	if container.kids.work != &tr.work || container.kids.Len() != 5 {
		t.Fatal("payload insertion lost storage/work binding")
	}
	// Reusing the emptied source must not change the transferred forest.
	mustLink(t, source.insert(nil, newInner(cSchema), insertDefault))
	if owner.any.children.Len() != 1 || owner.any.children.At(0) != container {
		t.Fatal("source storage still aliases payload")
	}
}

// Publication invalidates borrowed ranges even when the slice backend happens
// to keep the same allocation. Guard the publication/replay boundaries so
// later storage changes cannot silently revive a dependency on slice aliasing.
func TestSiblingPublicationBorrowedLifetime(t *testing.T) {
	for _, tc := range []struct{ file, function string }{{"parse.go", "flushDirtyRun"}, {"insert.go", "rbPublish"}, {"insert.go", "rbPos"}} {
		t.Run(tc.function, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, tc.file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Name.Name != tc.function {
					continue
				}
				borrowed, stale := map[string]bool{}, map[string]bool{}
				var inspectBlock func(*ast.BlockStmt)
				inspectBlock = func(block *ast.BlockStmt) {
					for _, stmt := range block.List {
						// Read assignment operands before rebinding its destination. In
						// particular, reacquisition must not itself read an invalid span.
						var reads []ast.Node
						assign, assignment := stmt.(*ast.AssignStmt)
						if assignment {
							for _, rhs := range assign.Rhs {
								reads = append(reads, rhs)
							}
						} else {
							reads = append(reads, stmt)
						}
						for _, read := range reads {
							ast.Inspect(read, func(n ast.Node) bool {
								if _, ok := n.(*ast.BlockStmt); ok {
									return false
								}
								if id, ok := n.(*ast.Ident); ok && stale[id.Name] {
									t.Errorf("%s: borrowed range %s used after publication; save nodes or reacquire the range", fset.Position(id.Pos()), id.Name)
								}
								return true
							})
						}
						if assignment {
							for i, lhs := range assign.Lhs {
								id, ok := lhs.(*ast.Ident)
								if !ok || i >= len(assign.Rhs) {
									continue
								}
								delete(stale, id.Name)
								delete(borrowed, id.Name)
								ast.Inspect(assign.Rhs[i], func(n ast.Node) bool {
									if call, ok := n.(*ast.CallExpr); ok {
										if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "schemaSpan" {
											borrowed[id.Name] = true
										}
									}
									return true
								})
							}
						}
						// Publication may be a statement or a checked error call in an
						// if initializer. Both invalidate all previously borrowed ranges.
						var publication ast.Expr
						switch stmt := stmt.(type) {
						case *ast.ExprStmt:
							publication = stmt.X
						case *ast.IfStmt:
							if init, ok := stmt.Init.(*ast.AssignStmt); ok && len(init.Rhs) == 1 {
								publication = init.Rhs[0]
							}
						}
						if call, ok := publication.(*ast.CallExpr); ok {
							if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
								(sel.Sel.Name == "replaceSchema" || sel.Sel.Name == "publishRun") {
								for name := range borrowed {
									stale[name] = true
								}
							}
						}
						ast.Inspect(stmt, func(n ast.Node) bool {
							if nested, ok := n.(*ast.BlockStmt); ok {
								inspectBlock(nested)
								return false
							}
							return true
						})
					}
				}
				inspectBlock(fn.Body)
				return
			}
			t.Fatalf("missing publication function %s", tc.function)
		})
	}
}

// A failed capture must be returned before any physical write, including when
// the destination of a transfer was captured but its source cannot be saved.
func TestSiblingFacadeCaptureFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*siblings, *siblings) error
	}{
		{"insert-schema", func(s, _ *siblings) error { return s.InsertAt(1, &Node{schema: &schema.Node{}}) }},
		{"insert-opaque", func(s, _ *siblings) error { return s.InsertAt(s.Len(), &Node{}) }},
		{"delete-schema", func(s, _ *siblings) error { return s.DeleteAt(0) }},
		{"delete-opaque", func(s, _ *siblings) error { return s.DeleteAt(s.SchemaLen()) }},
		{"publish", func(s, _ *siblings) error { return s.replaceSchema(0, 1, spanOf([]*Node{s.At(0)})) }},
		{"publish-run", func(s, _ *siblings) error { return s.publishRun(0, 1, []*Node{s.At(0), {schema: &schema.Node{}}}) }},
		{"append", func(s, _ *siblings) error { return s.appendNode(&Node{}) }},
		{"compact", func(s, _ *siblings) error { return s.deleteMatching(func(*Node) bool { return true }, nil) }},
		{"transfer-destination", func(s, src *siblings) error { return s.transferFrom(src) }},
		{"transfer-source", func(s, src *siblings) error { return s.transferFrom(src) }},
		{"reset", func(s, _ *siblings) error { return s.reset() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTree(nil)
			mustLink(t, testBuild(&tr.top, &Node{schema: &schema.Node{}}, &Node{}))
			tr.top.setWork(&tr.work)
			var src siblings
			mustLink(t, testBuild(&src, &Node{schema: &schema.Node{}}))
			src.setWork(&tr.work)
			before, sourceBefore := tr.top.CollectRange(0, tr.top.Len()), src.CollectRange(0, src.Len())
			j := beginHashTransaction(tr)
			if tc.name == "transfer-source" {
				mustLink(t, tr.top.touch())
			}
			tr.work.hashLimit = tr.work.hashUsed
			err := tc.mutate(&tr.top, &src)
			if !errors.Is(err, yang.ErrBudget) {
				t.Fatalf("capture error = %v", err)
			}
			if !slices.Equal(tr.top.CollectRange(0, tr.top.Len()), before) ||
				!slices.Equal(src.CollectRange(0, src.Len()), sourceBefore) ||
				tr.top.work != &tr.work || src.work != &tr.work {
				t.Fatal("failed capture changed storage before rollback")
			}
			j.end(&err)
		})
	}
}

// testBuild replaces the content of s with nodes (schema nodes first) through the storage, as a
// fixture: members of s keep their handles, other nodes are slotted, the rest leave s.
func testBuild(s *siblings, nodes ...*Node) error {
	keep := make(map[*Node]bool, len(nodes))
	for _, n := range nodes {
		keep[n] = true
	}
	gone := func(n *Node) bool { return !keep[n] }
	if err := s.deleteMatching(gone, gone); err != nil {
		return err
	}
	st := s.store()
	journaled := s.storeJournaled()
	fresh := len(nodes) - st.seq.Len()
	paid, err := s.payStore(addHashBound(st.seq.RepublishBound(0, st.seq.Len(), fresh, journaled, nil),
		mulHashBound(uint64(len(nodes)), slotUnits(journaled))))
	if err != nil {
		return err
	}
	defer paid.close()
	st.ver++
	hs, ok := st.seq.Republish(0, st.seq.Len(), nodes, s.handle)
	if !ok {
		panic("testBuild: invalid nodes")
	}
	schemaLen := 0
	for i, n := range nodes {
		if n.slot.Get() != hs[i] {
			n.slot.Set(&st.bind, hs[i])
		}
		if n.schema != nil {
			schemaLen++
		}
	}
	opidx.AddInt(&st.bind, &st.nschema, schemaLen-st.nschema)
	return nil
}

// storageAllowance is the sum of the storage's prepaid point bounds for n schema nodes appended
// one by one to an unindexed set (design 14), journaled or not: what a budget must leave room for
// besides the hashing, since each link reserves its insertion's bound before it is charged.
func storageAllowance(n int, journaled bool) uint64 {
	var seq opidx.Seq[*Node]
	seq.SetLabeled(false)
	var total uint64
	for range n {
		total += seq.InsertBound(nil, journaled, nil) + slotUnits(journaled)
		seq.InsertBefore(nil, nil)
	}
	return total
}
