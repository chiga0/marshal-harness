package productionruntime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"runtime"
	"testing"
)

func TestRepositoryOwnerTransitionHasOneAtomicProductionCallsite(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Darwin arm64 owner transition")
	}
	file, err := parser.ParseFile(token.NewFileSet(), "owner_lock_darwin.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var transition *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if function.Name.Name == "acquireOwner" || function.Name.Name == "bindAcquisition" {
			t.Fatalf("split owner transition entrypoint %q remains reachable", function.Name.Name)
		}
		if function.Name.Name == "acquireAndBind" {
			transition = function
		}
	}
	if transition == nil {
		t.Fatal("atomic acquireAndBind transition is missing")
	}
	counts := map[string]int{}
	ast.Inspect(transition, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if ok {
			counts[selector.Sel.Name]++
		}
		return true
	})
	if counts["withHeld"] != 1 || counts["AcquireOwner"] != 1 || counts["OpenOwner"] != 2 || counts["revalidateLocked"] != 1 {
		t.Fatalf("atomic owner transition call shape drifted: %#v", counts)
	}
}
