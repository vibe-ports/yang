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
				if _, err := s.InsertAt(at, n); err != nil {
					t.Fatal(err)
				}
				model = slices.Insert(model, at, n)
				if !opaque {
					schemaLen++
				}
			case 2:
				if len(model) > 0 {
					at := rng.IntN(len(model))
					if _, err := s.DeleteAt(at); err != nil {
						t.Fatal(err)
					}
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
				mustLink(t, s.Splice(a, b, nodes))
				model = slices.Replace(model, a, b, nodes...)
				if !opaque {
					schemaLen += len(nodes) - (b - a)
				}
			case 4:
				mustLink(t, s.Build(slices.Clone(model[:schemaLen]), slices.Clone(model[schemaLen:])))
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
			mustLink(t, tr.top.Build([]*Node{a, b}, []*Node{c}))
			tr.top.setWork(&tr.work)
			destination := siblings{work: &tr.work}
			before := tr.top.CollectRange(0, tr.top.Len())
			j := beginHashTransaction(tr)
			switch mutation {
			case "insert":
				if _, err := tr.top.InsertAt(1, &Node{schema: &schema.Node{}}); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if _, err := tr.top.DeleteAt(0); err != nil {
					t.Fatal(err)
				}
			case "splice":
				mustLink(t, tr.top.Splice(0, 1, []*Node{{schema: &schema.Node{}}}))
			case "publish":
				mustLink(t, tr.top.replaceSchema(0, 2, spanOf([]*Node{b, a})))
			case "replay":
				mustLink(t, tr.top.exposeSchema([]*Node{a}))
			case "build":
				mustLink(t, tr.top.Build(nil, []*Node{c}))
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
			if tr.top.work != &tr.work {
				t.Fatal("rollback lost work binding")
			}
			if _, err := tr.top.InsertAt(1, &Node{schema: &schema.Node{}}); err != nil {
				t.Fatal(err)
			}
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
	mustLink(t, view.Build([]*Node{n}, nil))
	snapshot := view.CollectRange(0, 1)
	mustLink(t, view.replaceSchema(0, 1, spanOf([]*Node{{schema: &schema.Node{}}})))
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
								(sel.Sel.Name == "replaceSchema" || sel.Sel.Name == "buildSchema" || sel.Sel.Name == "exposeSchema") {
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
		{"insert-schema", func(s, _ *siblings) error { _, err := s.InsertAt(1, &Node{schema: &schema.Node{}}); return err }},
		{"insert-opaque", func(s, _ *siblings) error { _, err := s.InsertAt(s.Len(), &Node{}); return err }},
		{"delete-schema", func(s, _ *siblings) error { _, err := s.DeleteAt(0); return err }},
		{"delete-opaque", func(s, _ *siblings) error { _, err := s.DeleteAt(s.SchemaLen()); return err }},
		{"splice", func(s, _ *siblings) error { return s.Splice(0, s.Len(), nil) }},
		{"publish", func(s, _ *siblings) error { return s.replaceSchema(0, 1, spanOf([]*Node{{schema: &schema.Node{}}})) }},
		{"replay", func(s, _ *siblings) error { return s.exposeSchema(nil) }},
		{"build", func(s, _ *siblings) error { return s.Build(nil, nil) }},
		{"build-schema", func(s, _ *siblings) error { return s.buildSchema(nil) }},
		{"append", func(s, _ *siblings) error { return s.appendNode(&Node{}) }},
		{"compact", func(s, _ *siblings) error { return s.deleteMatching(func(*Node) bool { return true }, nil) }},
		{"transfer-destination", func(s, src *siblings) error { return s.transferFrom(src) }},
		{"transfer-source", func(s, src *siblings) error { return s.transferFrom(src) }},
		{"reset", func(s, _ *siblings) error { return s.reset() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTree(nil)
			mustLink(t, tr.top.Build([]*Node{{schema: &schema.Node{}}}, []*Node{{}}))
			tr.top.setWork(&tr.work)
			var src siblings
			mustLink(t, src.Build([]*Node{{schema: &schema.Node{}}}, nil))
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
