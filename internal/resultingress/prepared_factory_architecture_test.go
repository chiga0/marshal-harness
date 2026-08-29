package resultingress

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPreparedFactorySeparatesLedgerOpenFromCurrentCoreObservation(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Darwin arm64 production factory")
	}
	path := filepath.Join("prepared_factory_darwin.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	functions := map[string]*ast.FuncDecl{}
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok {
			functions[function.Name.Name] = function
		}
	}
	opened := functions["OpenDarwinResultIngressStore"]
	sealed := functions["SealPi0843DarwinPreparedExecutionStore"]
	if opened == nil || sealed == nil {
		t.Fatal("descriptor-open and prepared-seal constructors must both exist")
	}
	if countSelector(opened, "ObserveCurrentCore") != 0 || countCall(opened, "openHeldDarwinAuthorityFiles") != 1 {
		t.Fatal("descriptor-open constructor must only bind held authority files before OpenOwner")
	}
	if countSelector(sealed, "ObserveCurrentCore") != 1 || countSelector(sealed, "WithCurrentOwner") != 1 || countCall(sealed, "openHeldDarwinAuthorityFiles") != 0 || countComposite(sealed, "DurableStore") != 0 {
		t.Fatal("prepared seal must consume the current-owner-bound store in place, observe current Core once and never open or clone a store")
	}
	if countCall(opened, "OpenResultIngressStore") != 0 || countCall(sealed, "OpenResultIngressStore") != 0 {
		t.Fatal("sealed production constructors must not reopen ResultIngress by path")
	}
}

func countComposite(function *ast.FuncDecl, name string) int {
	count := 0
	ast.Inspect(function, func(node ast.Node) bool {
		literal, ok := node.(*ast.CompositeLit)
		if !ok {
			return true
		}
		identifier, ok := literal.Type.(*ast.Ident)
		if ok && identifier.Name == name {
			count++
		}
		return true
	})
	return count
}

func countSelector(function *ast.FuncDecl, name string) int {
	count := 0
	ast.Inspect(function, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if ok && selector.Sel.Name == name {
			count++
		}
		return true
	})
	return count
}

func countCall(function *ast.FuncDecl, name string) int {
	count := 0
	ast.Inspect(function, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		identifier, ok := call.Fun.(*ast.Ident)
		if ok && identifier.Name == name {
			count++
		}
		return true
	})
	return count
}
