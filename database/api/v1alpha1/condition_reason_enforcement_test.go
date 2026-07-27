/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestProductionConditionReasonsUseConstants prevents condition reasons from
// becoming a stringly-typed API again. Test fixtures are intentionally excluded
// so defensive tests can still construct malformed external status.
func TestProductionConditionReasonsUseConstants(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	dirs := []string{
		filepath.Join(moduleRoot, "api", "v1alpha1"),
		filepath.Join(moduleRoot, "internal", "controller"),
		filepath.Join(moduleRoot, "internal", "ensure"),
	}

	fset := token.NewFileSet()
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			parsed, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			reasonCases := make(map[*ast.CaseClause]struct{})
			ast.Inspect(parsed, func(node ast.Node) bool {
				switchStmt, ok := node.(*ast.SwitchStmt)
				if !ok || !isReasonSelector(switchStmt.Tag) {
					return true
				}
				for _, stmt := range switchStmt.Body.List {
					if clause, ok := stmt.(*ast.CaseClause); ok {
						reasonCases[clause] = struct{}{}
					}
				}
				return true
			})
			ast.Inspect(parsed, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.KeyValueExpr:
					if ident, ok := n.Key.(*ast.Ident); ok && ident.Name == "Reason" {
						rejectStringLiteral(t, fset, file, n.Value)
					}
				case *ast.AssignStmt:
					for i := 0; i < len(n.Lhs) && i < len(n.Rhs); i++ {
						if isReasonSelector(n.Lhs[i]) {
							rejectStringLiteral(t, fset, file, n.Rhs[i])
						}
						if isReasonSelector(n.Rhs[i]) {
							rejectStringLiteral(t, fset, file, n.Lhs[i])
						}
					}
				case *ast.CaseClause:
					if _, ok := reasonCases[n]; ok {
						for _, expr := range n.List {
							rejectStringLiteral(t, fset, file, expr)
						}
					}
				case *ast.CallExpr:
					var name string
					switch fun := n.Fun.(type) {
					case *ast.Ident:
						name = fun.Name
					case *ast.SelectorExpr:
						name = fun.Sel.Name
					}
					reasonArg := -1
					switch name {
					case "SetCurrentCondition":
						reasonArg = 2
					case "Pending", "PendingAfter", "Terminal", "terminalErr":
						reasonArg = 0
					}
					if reasonArg >= 0 && len(n.Args) > reasonArg {
						rejectStringLiteral(t, fset, file, n.Args[reasonArg])
					}
				case *ast.BinaryExpr:
					if isReasonSelector(n.X) {
						rejectStringLiteral(t, fset, file, n.Y)
					}
					if isReasonSelector(n.Y) {
						rejectStringLiteral(t, fset, file, n.X)
					}
				}
				return true
			})
		}
	}
}

func rejectStringLiteral(t *testing.T, fset *token.FileSet, file string, expr ast.Expr) {
	t.Helper()
	if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		t.Errorf("%s:%d uses raw condition reason %s", file, fset.Position(expr.Pos()).Line, lit.Value)
	}
}

func isReasonSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Reason"
}
