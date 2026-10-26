// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"context"
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
	"github.com/vibe-ports/yang/internal/schema"
)

// Check tests as well as production, including struct literals, so new callers
// cannot bypass the facade through an alias or a temporary nodeDeque builder.
func siblingStorageViolations(fset *token.FileSet, file *ast.File) []token.Position {
	var found []token.Position
	ast.Inspect(file, func(n ast.Node) bool {
		var field *ast.Ident
		switch n := n.(type) {
		case *ast.SelectorExpr:
			field = n.Sel
		case *ast.KeyValueExpr:
			field, _ = n.Key.(*ast.Ident)
		}
		if field != nil && (field.Name == "list" || field.Name == "opq") {
			found = append(found, fset.Position(field.Pos()))
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || entry.Name() == "siblings_access.go" {
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
		"package data;func f(s *siblings){_ = s.list}",
		"package data;func f(s *siblings){alias:=s;alias.opq=nil}",
		"package data;var s = nodeDeque{list:nil}",
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
				s.InsertAt(at, n)
				model = slices.Insert(model, at, n)
				if !opaque {
					schemaLen++
				}
			case 2:
				if len(model) > 0 {
					at := rng.IntN(len(model))
					s.DeleteAt(at)
					model = slices.Delete(model, at, at+1)
					if at < schemaLen {
						schemaLen--
					}
				}
			case 3:
				opaque := rng.IntN(2) == 0
				lo, hi := 0, schemaLen
				if opaque {
					lo, hi = schemaLen, len(model)
				}
				a := lo + rng.IntN(hi-lo+1)
				b := a + rng.IntN(hi-a+1)
				nodes := make([]*Node, rng.IntN(4))
				for i := range nodes {
					nodes[i] = fresh(opaque)
				}
				s.Splice(a, b, nodes)
				model = slices.Replace(model, a, b, nodes...)
				if !opaque {
					schemaLen += len(nodes) - (b - a)
				}
			case 4:
				s.Build(slices.Clone(model[:schemaLen]), slices.Clone(model[schemaLen:]))
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
	for _, mutation := range []string{"insert", "delete", "splice", "publish", "replay", "build", "compact", "transfer", "reset"} {
		t.Run(mutation, func(t *testing.T) {
			tr := newTree(nil)
			a, b, c := &Node{schema: &schema.Node{}}, &Node{schema: &schema.Node{}}, &Node{}
			tr.top.Build([]*Node{a, b}, []*Node{c})
			tr.top.setWork(&tr.work)
			destination := siblings{work: &tr.work}
			before := tr.top.CollectRange(0, tr.top.Len())
			j := beginHashTransaction(tr)
			switch mutation {
			case "insert":
				tr.top.InsertAt(1, &Node{schema: &schema.Node{}})
			case "delete":
				tr.top.DeleteAt(0)
			case "splice":
				tr.top.Splice(0, 1, []*Node{{schema: &schema.Node{}}})
			case "publish":
				tr.top.replaceSchema(0, 2, spanOf([]*Node{b, a}))
			case "replay":
				tr.top.exposeSchema([]*Node{a})
			case "build":
				tr.top.Build(nil, []*Node{c})
			case "compact":
				tr.top.deleteMatching(func(n *Node) bool { return n == a }, func(n *Node) bool { return n == c })
			case "transfer":
				destination.transferFrom(&tr.top)
			case "reset":
				tr.top.reset()
			}
			if tr.work.hashUsed == 0 {
				t.Fatal("mutation did not journal/charge storage")
			}
			err := yang.ErrBudget
			j.end(&err)
			if !slices.Equal(tr.top.CollectRange(0, tr.top.Len()), before) || destination.Len() != 0 {
				t.Fatal("rollback failed")
			}
			if tr.top.work != &tr.work {
				t.Fatal("rollback lost work binding")
			}
			tr.top.InsertAt(1, &Node{schema: &schema.Node{}})
			if tr.top.Len() != 4 {
				t.Fatal("insertion after rollback")
			}
		})
	}
}

func TestSiblingFacadeSnapshotAndView(t *testing.T) {
	n := &Node{schema: &schema.Node{}}
	owner := newTree(nil)
	n.tree = owner
	var view siblings
	view.Build([]*Node{n}, nil)
	snapshot := view.CollectRange(0, 1)
	view.replaceSchema(0, 1, spanOf([]*Node{{schema: &schema.Node{}}}))
	if snapshot[0] != n || n.tree != owner || n.parent != nil {
		t.Fatal("view changed ownership or snapshot")
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
						if expr, ok := stmt.(*ast.ExprStmt); ok {
							if call, ok := expr.X.(*ast.CallExpr); ok {
								if sel, ok := call.Fun.(*ast.SelectorExpr); ok &&
									(sel.Sel.Name == "replaceSchema" || sel.Sel.Name == "buildSchema" || sel.Sel.Name == "exposeSchema") {
									for name := range borrowed {
										stale[name] = true
									}
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
