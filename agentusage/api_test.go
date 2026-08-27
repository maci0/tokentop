// Copyright (C) 2026 Marcel W. Wysocki
// SPDX-License-Identifier: MIT

package agentusage

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

// publicAPI is the agentusage surface other modules compile against. A new
// name is additive; removing or renaming one, or dropping a struct field, is
// a breaking change that needs a changelog entry.
var publicTypes = map[string][]string{
	"Process": {"PID", "Tool", "Dir", "Started"},
	"Sample":  {"Output", "Thinking", "Total", "Input", "At"},
	"Spec":    {"Roots", "Suffix", "Cumulative", "HeaderCwd"},
	"Watcher": nil,
}

var publicFuncs = []string{
	"Agents",
	"ConnectedTo",
	"DefinitionsPath",
	"Discover",
	"EnableOpenCodeDB",
	"LoadDefinitions",
	"MatchingEndpoints",
	"Peers",
	"Rate",
	"RegisterSpec",
	"Supported",
	"Watch",
}

var publicMethods = map[string][]string{
	"Sample":  {"Empty"},
	"Watcher": {"Dir", "Poll", "Run", "Sample", "Tool"},
}

func TestPublicAPI(t *testing.T) {
	fset := token.NewFileSet()
	filter := func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}
	pkgs, err := parser.ParseDir(fset, ".", filter, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg := pkgs["agentusage"]
	if pkg == nil {
		t.Fatal("package agentusage not parsed")
	}

	gotTypes := map[string][]string{}
	gotFuncs := map[string]bool{}
	gotMethods := map[string][]string{}
	for _, f := range pkg.Files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ts.Name.IsExported() {
						continue
					}
					var fields []string
					st, ok := ts.Type.(*ast.StructType)
					if ok && st.Fields != nil {
						for _, field := range st.Fields.List {
							for _, n := range field.Names {
								if n.IsExported() {
									fields = append(fields, n.Name)
								}
							}
						}
					}
					gotTypes[ts.Name.Name] = fields
				}
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv == nil {
					gotFuncs[d.Name.Name] = true
					continue
				}
				recv := recvTypeName(d.Recv)
				if recv == "" || !ast.IsExported(recv) {
					continue
				}
				gotMethods[recv] = append(gotMethods[recv], d.Name.Name)
			}
		}
	}

	for name, fields := range gotTypes {
		want, ok := publicTypes[name]
		if !ok {
			t.Errorf("exported type %s is not in the public API snapshot; add it (additive) or unexport it", name)
			continue
		}
		if !slices.Equal(fields, want) {
			t.Errorf("type %s fields = %v, want %v", name, fields, want)
		}
	}
	for name := range publicTypes {
		if _, ok := gotTypes[name]; !ok {
			t.Errorf("public type %s is missing from the package", name)
		}
	}

	for name := range gotFuncs {
		if !slices.Contains(publicFuncs, name) {
			t.Errorf("exported func %s is not in the public API snapshot; add it (additive) or unexport it", name)
		}
	}
	for _, name := range publicFuncs {
		if !gotFuncs[name] {
			t.Errorf("public func %s is missing from the package", name)
		}
	}

	for recv, methods := range gotMethods {
		slices.Sort(methods)
		methods = slices.Compact(methods) // Discover/Peers live in several GOOS files
		want, ok := publicMethods[recv]
		if !ok {
			t.Errorf("exported methods on %s %v are not in the public API snapshot", recv, methods)
			continue
		}
		want = slices.Clone(want)
		slices.Sort(want)
		if !slices.Equal(methods, want) {
			t.Errorf("methods on %s = %v, want %v", recv, methods, want)
		}
	}
	for recv, want := range publicMethods {
		if _, ok := gotMethods[recv]; !ok {
			t.Errorf("public methods on %s %v are missing from the package", recv, want)
		}
	}
}

func recvTypeName(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	expr := fl.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	id, _ := expr.(*ast.Ident)
	if id == nil {
		return ""
	}
	return id.Name
}
