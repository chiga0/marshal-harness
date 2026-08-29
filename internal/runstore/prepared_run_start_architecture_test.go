package runstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPreparedRunStartHasOneExportedMutationSeam(t *testing.T) {
	t.Helper()
	file := parseArchitectureFile(t, "prepared_run_start_authority.go")
	methods := map[string]int{}
	selectors := map[string]int{}
	ast.Inspect(file, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.FuncDecl:
			if value.Recv != nil {
				methods[value.Name.Name]++
			}
		case *ast.CallExpr:
			if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
				selectors[selector.Sel.Name]++
			}
			if identifier, ok := value.Fun.(*ast.Ident); ok {
				selectors[identifier.Name]++
			}
		}
		return true
	})
	if methods["WithPreparedRunStartAuthority"] != 1 || methods["ProjectCommittedRunStart"] != 1 {
		t.Fatalf("mutation seam shape changed: methods=%v", methods)
	}
	if selectors["WithClaim"] != 1 || selectors["appendPreparedRunStartClaim"] != 1 {
		t.Fatalf("proof/projector producer count changed: calls=%v", selectors)
	}
	if selectors["OpenRunAuthority"] != 0 {
		t.Fatalf("sealed Run-start authority reopens its borrowed descriptor: calls=%v", selectors)
	}
	for name := range methods {
		if ast.IsExported(name) && name != "WithPreparedRunStartAuthority" && name != "ReadRunStartAuthorityUnderLease" && name != "ProjectCommittedRunStart" {
			t.Fatalf("unexpected exported prepared Run-start method %s", name)
		}
	}
}

func TestResultIngressPreparedExecutionDoesNotDependOnRunStoreOrMechanics(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "resultingress"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file := parseArchitectureFile(t, filepath.Join("..", "resultingress", entry.Name()))
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(path, "/internal/runstore") {
				t.Fatalf("ResultIngress production file %s imports runstore", entry.Name())
			}
		}
		if entry.Name() != "prepared_execution.go" {
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			identifier, ok := selector.X.(*ast.Ident)
			if ok && identifier.Name == "processsupervisor" {
				t.Fatalf("ResultIngress directly invokes processsupervisor.%s", selector.Sel.Name)
			}
			return true
		})
	}
}

func parseArchitectureFile(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}
