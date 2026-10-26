// SPDX-License-Identifier: BSD-3-Clause

package data

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const auditModulePath = "github.com/vibe-ports/yang"

// Every entry identifies one deliberate discard. The test rejects empty
// reasons and stale entries, so the allowlist remains an audited set rather
// than a way to silence new findings.
const (
	allowLoggedMarker = "the call appends the required diagnostic; its error is only a marker and the enclosing path returns its explicit native rc"
	allowNoFailWriter = "the concrete bytes/string/hash writer documents that Write cannot return an error"
	allowNoWork       = "the compatibility wrapper passes no work counter or callback, so the checked implementation cannot produce its budget error"
)

var ignoredErrorAllowlist = map[string]string{
	"diff_gen.go:262:Build":                  "the fresh sibling view has no work binding or transaction, so storage capture cannot fail",
	"diff_gen.go:265:Build":                  "the fresh sibling view has no work binding or transaction, so storage capture cannot fail",
	"print.go:115:Build":                     "the fresh print view has no work binding or transaction, so storage capture cannot fail",
	"print.go:118:Build":                     "the fresh print view has no work binding or transaction, so storage capture cannot fail",
	"any.go:326:logErr":                      allowLoggedMarker,
	"compare.go:31:compareSingleCheckedWork": allowNoWork,
	"diag.go:206:item":                       allowLoggedMarker,
	"diag.go:229:item":                       allowLoggedMarker,
	"diag.go:232:item":                       allowLoggedMarker,
	"diff.go:115:WriteString":                allowNoFailWriter,
	"diff.go:322:item":                       allowLoggedMarker,
	"diff.go:324:logErr":                     allowLoggedMarker,
	"diff.go:326:logErr":                     allowLoggedMarker,
	"diff.go:334:logErr":                     allowLoggedMarker,
	"diff_merge.go:34:findMeta":              "findMeta reports lookup failure through the logger diagnostics consumed immediately below",
	"dup.go:465:storeErr":                    allowLoggedMarker,
	"dup.go:466:logErr":                      allowLoggedMarker,
	"dup.go:564:logErr":                      allowLoggedMarker,
	"edit.go:514:val":                        allowLoggedMarker,
	"edit.go:714:val":                        allowLoggedMarker,
	"edit.go:716:val":                        allowLoggedMarker,
	"edit.go:712:val":                        allowLoggedMarker,
	"edit.go:738:val":                        allowLoggedMarker,
	"edit.go:745:val":                        allowLoggedMarker,
	"envelope_json.go:65:logErr":             allowLoggedMarker,
	"envelope_xml.go:161:val":                allowLoggedMarker,
	"envelope_xml.go:208:logErr":             allowLoggedMarker,
	"eventtime.go:29:logErr":                 allowLoggedMarker,
	"links.go:387:xpathErr":                  "the XPath error is logged and native leafref linking deliberately skips that failed link",
	"lyd_hash.go:14:CachedNameHash":          "the cache callback in this uncharged helper always returns a nil error",
	"meta.go:83:val":                         allowLoggedMarker,
	"meta.go:190:logErr":                     allowLoggedMarker,
	"meta.go:220:val":                        allowLoggedMarker,
	"new.go:138:logErr":                      allowLoggedMarker,
	"new.go:284:logErr":                      allowLoggedMarker,
	"new.go:344:item":                        allowLoggedMarker,
	"new.go:354:logErr":                      allowLoggedMarker,
	"op_index.go:76:Write":                   allowNoFailWriter,
	"op_index.go:87:Write":                   allowNoFailWriter,
	"op_index.go:89:Write":                   allowNoFailWriter,
	"op_index.go:99:Write":                   allowNoFailWriter,
	"op_index.go:100:Write":                  allowNoFailWriter,
	"op_index.go:108:Write":                  allowNoFailWriter,
	"op_index.go:119:Write":                  allowNoFailWriter,
	"op_index.go:120:Write":                  allowNoFailWriter,
	"parse.go:480:logErr":                    allowLoggedMarker,
	"parse.go:492:logErr":                    allowLoggedMarker,
	"parse_json.go:97:lexErr":                "lexErr performs the required logging; parser construction returns its fatal marker",
	"parse_json.go:113:val":                  allowLoggedMarker,
	"parse_json.go:254:val":                  allowLoggedMarker,
	"parse_json.go:440:getSnode":             "metadata replay intentionally uses only the resolved schema; getSnode has already logged a strict lookup failure",
	"parse_xml.go:78:val":                    allowLoggedMarker,
	"parse_xml.go:226:log":                   "the supplied not-found logger records the diagnostic; the closure returns LY_ENOTFOUND explicitly",
	"path.go:83:WriteByte":                   allowNoFailWriter,
	"path.go:91:WriteString":                 allowNoFailWriter,
	"path.go:92:WriteByte":                   allowNoFailWriter,
	"path.go:94:WriteString":                 allowNoFailWriter,
	"path.go:105:WriteString":                allowNoFailWriter,
	"path.go:108:WriteString":                allowNoFailWriter,
	"path.go:111:WriteString":                allowNoFailWriter,
	"path.go:117:WriteString":                allowNoFailWriter,
	"print.go:145:xpathErr":                  "default probing retains the emitted diagnostics separately and continues to the plugin diagnostic",
	"print.go:147:storeErr":                  "default probing retains the emitted diagnostics separately and returns its boolean verdict",
	"print.go:383:WriteString":               allowNoFailWriter,
	"print.go:388:WriteByte":                 allowNoFailWriter,
	"siblings.go:785:Write":                  allowNoFailWriter,
	"siblings.go:794:Write":                  allowNoFailWriter,
	"siblings.go:795:Write":                  allowNoFailWriter,
	"siblings.go:806:Write":                  allowNoFailWriter,
	"siblings.go:807:Write":                  allowNoFailWriter,
	"siblings.go:808:Write":                  allowNoFailWriter,
	"siblings.go:1900:findFirstWork":         allowNoWork,
	"siblings.go:2063:compareSingleWork":     allowNoWork,
	"siblings.go:2132:Fprintf":               "fmt writes to a strings.Builder, whose Write method cannot fail",
	"siblings.go:2134:Fprintf":               "fmt writes to a strings.Builder, whose Write method cannot fail",
	"siblings.go:2137:Fprintf":               "fmt writes to a strings.Builder, whose Write method cannot fail",
	"unres.go:105:logErr":                    allowLoggedMarker,
	"unres.go:178:item":                      allowLoggedMarker,
	"unres.go:586:xpathErr":                  "validation commits each saved probe diagnostic before returning the final type diagnostic",
	"unres.go:813:pathEvalChecked":           "legacy package tests exercise the node-only wrapper; production callers use pathEvalChecked and propagate its error",
	"unres.go:914:xpathErr":                  "deref logs the evaluator error before returning the plugin-level error that names it",
	"unres.go:967:logErr":                    allowLoggedMarker,
	"unres.go:1037:logErr":                   allowLoggedMarker,
	"validate.go:726:val":                    allowLoggedMarker,
	"validate.go:752:WriteByte":              allowNoFailWriter,
	"validate.go:754:WriteString":            allowNoFailWriter,
	"validate.go:813:WriteString":            allowNoFailWriter,
	"validate.go:814:WriteByte":              allowNoFailWriter,
	"xpathnode.go:83:LookupPrepared":         "LookupChild prepares without a charge callback, so LookupPrepared cannot return a work error",
	"xpathnode.go:88:LookupPrepared":         "the schema-only probe has no charge callback, so LookupPrepared cannot return a work error",
	"xpathnode.go:295:StringWithBudget":      "String uses a background context and nil budget callbacks; serialization errors remain available through StringWithBudget",
}

type auditImporter struct {
	fset       *token.FileSet
	moduleRoot string
	standard   types.Importer
	packages   map[string]*types.Package
	loading    map[string]bool
}

func (i *auditImporter) Import(path string) (*types.Package, error) {
	if path != auditModulePath && !strings.HasPrefix(path, auditModulePath+"/") {
		return i.standard.Import(path)
	}
	if pkg := i.packages[path]; pkg != nil {
		return pkg, nil
	}
	if i.loading[path] {
		return nil, fmt.Errorf("import cycle while auditing %s", path)
	}
	i.loading[path] = true
	defer delete(i.loading, path)

	rel := strings.TrimPrefix(path, auditModulePath)
	rel = strings.TrimPrefix(rel, "/")
	dir := filepath.Join(i.moduleRoot, filepath.FromSlash(rel))
	files, err := auditPackageFiles(i.fset, dir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Go files for %s in %s", path, dir)
	}
	conf := types.Config{Importer: i}
	pkg, err := conf.Check(path, i.fset, files, nil)
	if err != nil {
		return nil, err
	}
	i.packages[path] = pkg
	return pkg, nil
}

func auditPackageFiles(fset *token.FileSet, dir string) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		match, err := build.Default.MatchFile(dir, name)
		if err != nil {
			return nil, err
		}
		if !match {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func auditCallName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.IndexExpr:
		return auditCallName(x.X)
	case *ast.IndexListExpr:
		return auditCallName(x.X)
	default:
		return "call"
	}
}

func TestNoDiscardedErrorResults(t *testing.T) {
	fset := token.NewFileSet()
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	files, err := auditPackageFiles(fset, ".")
	if err != nil {
		t.Fatal(err)
	}
	imp := &auditImporter{
		fset: fset, moduleRoot: moduleRoot, standard: importer.Default(),
		packages: map[string]*types.Package{}, loading: map[string]bool{},
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	conf := types.Config{Importer: imp}
	if _, err := conf.Check(auditModulePath+"/data", fset, files, info); err != nil {
		t.Fatal(err)
	}

	errorType := types.Universe.Lookup("error").Type()
	isErrorCall := func(expr ast.Expr) (*ast.CallExpr, bool) {
		call, ok := expr.(*ast.CallExpr)
		if !ok {
			return nil, false
		}
		sig, ok := info.TypeOf(call.Fun).(*types.Signature)
		if !ok || sig.Results().Len() == 0 {
			return nil, false
		}
		last := sig.Results().At(sig.Results().Len() - 1).Type()
		return call, types.Identical(last, errorType)
	}
	isBlank := func(expr ast.Expr) bool {
		id, ok := expr.(*ast.Ident)
		return ok && id.Name == "_"
	}

	found := map[string]string{}
	record := func(call *ast.CallExpr) {
		pos := fset.Position(call.Pos())
		id := fmt.Sprintf("%s:%d:%s", filepath.Base(pos.Filename), pos.Line, auditCallName(call.Fun))
		found[id] = pos.String()
	}
	for _, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.AssignStmt:
				if len(n.Rhs) == 1 {
					if call, ok := isErrorCall(n.Rhs[0]); ok && len(n.Lhs) > 0 && isBlank(n.Lhs[len(n.Lhs)-1]) {
						record(call)
					}
				} else if len(n.Lhs) == len(n.Rhs) {
					for at, rhs := range n.Rhs {
						if call, ok := isErrorCall(rhs); ok && isBlank(n.Lhs[at]) {
							record(call)
						}
					}
				}
			case *ast.ValueSpec:
				if len(n.Values) == 1 {
					if call, ok := isErrorCall(n.Values[0]); ok && len(n.Names) > 0 && n.Names[len(n.Names)-1].Name == "_" {
						record(call)
					}
				} else if len(n.Names) == len(n.Values) {
					for at, value := range n.Values {
						if call, ok := isErrorCall(value); ok && n.Names[at].Name == "_" {
							record(call)
						}
					}
				}
			case *ast.ExprStmt:
				if call, ok := isErrorCall(n.X); ok {
					record(call)
				}
			case *ast.GoStmt:
				if call, ok := isErrorCall(n.Call); ok {
					record(call)
				}
			case *ast.DeferStmt:
				if call, ok := isErrorCall(n.Call); ok {
					record(call)
				}
			}
			return true
		})
	}

	var failures []string
	for id, pos := range found {
		reason, ok := ignoredErrorAllowlist[id]
		if !ok {
			failures = append(failures, pos+": discarded final error result (allow only with a documented reason)")
		} else if strings.TrimSpace(reason) == "" {
			failures = append(failures, id+": allowlist reason is empty")
		}
	}
	for id, reason := range ignoredErrorAllowlist {
		if strings.TrimSpace(reason) == "" {
			failures = append(failures, id+": allowlist reason is empty")
		}
		if _, ok := found[id]; !ok {
			failures = append(failures, id+": stale allowlist entry")
		}
	}
	sort.Strings(failures)
	if len(failures) != 0 {
		t.Fatal("discarded error-result audit:\n" + strings.Join(failures, "\n"))
	}
}
