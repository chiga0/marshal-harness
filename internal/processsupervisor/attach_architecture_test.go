package processsupervisor

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

// TestAttachExportedAPIIsBorrowedCallbackOnly mechanically locks the ADR 0067
// §4 contract: the only production Attach entry point is WithAttached, which
// hands a callback-scoped *AttachedSession to a borrower and returns nothing
// borrowed. There must be no exported Attach that returns a usable client or
// observation outside a callback, and AttachedSession must expose no method
// other than Observation.
func TestAttachExportedAPIIsBorrowedCallbackOnly(t *testing.T) {
	file := parseAttachArchitectureFile(t, "attach.go")
	exportedTypes := map[string]bool{}
	exportedFuncs := map[string]bool{}
	exportedVars := map[string]bool{}
	for _, declaration := range file.Decls {
		switch value := declaration.(type) {
		case *ast.FuncDecl:
			if value.Recv == nil && ast.IsExported(value.Name.Name) {
				exportedFuncs[value.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, specification := range value.Specs {
				switch spec := specification.(type) {
				case *ast.TypeSpec:
					if ast.IsExported(spec.Name.Name) {
						exportedTypes[spec.Name.Name] = true
					}
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						if ast.IsExported(name.Name) {
							exportedVars[name.Name] = true
						}
					}
				}
			}
		}
	}
	for _, name := range []string{"AttachOwnerAcquisition", "AttachOwnerBoundFact", "AttachAuthority", "AttachOwnerVerifier", "AttachOptions", "AttachObservation", "AttachedSession"} {
		if !exportedTypes[name] {
			t.Fatalf("attach.go lost exported type %s", name)
		}
	}
	for _, name := range []string{"ValidateAttachObservation"} {
		if !exportedFuncs[name] {
			t.Fatalf("attach.go lost exported function %s", name)
		}
	}
	for _, name := range []string{"AttachSchema", "AttachObservationSchema"} {
		if !exportedVars[name] {
			t.Fatalf("attach.go lost exported constant %s", name)
		}
	}
	for name := range exportedFuncs {
		if name == "ValidateAttachObservation" {
			continue
		}
		t.Fatalf("attach.go gained unexpected exported function %s", name)
	}
	if exportedFuncs["Attach"] || exportedTypes["Attach"] {
		t.Fatal("attach.go exports a non-borrowed Attach entry point")
	}
}

// TestAttachedSessionHasNoCommandOrTransportMethod scans attach.go for methods
// on *AttachedSession and requires Observation plus the explicit ADR 0067/0071
// continuations to be the only exported methods, so a future change cannot
// silently add a generic command or transport escape valve.
func TestAttachedSessionHasNoCommandOrTransportMethod(t *testing.T) {
	file := parseAttachArchitectureFile(t, "attach.go")
	methods := map[string]bool{}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil {
			continue
		}
		star, ok := function.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		identifier, ok := star.X.(*ast.Ident)
		if !ok || identifier.Name != "AttachedSession" {
			continue
		}
		if ast.IsExported(function.Name.Name) {
			methods[function.Name.Name] = true
		}
	}
	if len(methods) != 5 || !methods["Observation"] || !methods["ExecutePreparedBindAuthority"] || !methods["ExecutePreparedCollect"] || !methods["ExecutePreparedInspect"] || !methods["ExecutePreparedClose"] {
		t.Fatalf("AttachedSession exported methods = %v, want only Observation and the explicit BindAuthority, Collect, Inspect and Close continuations", methods)
	}
}

// TestAttachFileHasNoInternalPackageImports ensures the Attach primitive's
// type/validation substrate depends only on the standard library, so it cannot
// acquire hidden cross-package authority (launchidentity, resultingress,
// runstore, productionruntime, etc.).
func TestAttachFileHasNoInternalPackageImports(t *testing.T) {
	file := parseAttachArchitectureFile(t, "attach.go")
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(path, "/internal/") || strings.HasPrefix(path, "github.com/") {
			t.Fatalf("attach.go imports non-stdlib package %s", path)
		}
	}
}

// TestWithAttachedHasNoProductionCallsiteOutsideProcessSupervisor enforces that
// the read-only Attach primitive has exactly one production callsite outside
// internal/processsupervisor: the ADR 0067 §4 owner-rebind transport adapter
// (productionRebindTransport) in resultingress. Any additional callsite is a
// forbidden escape. The rebind orchestration itself
// (RebindOwnerSuccessorForAttachedRecovery) has exactly one production caller:
// productionruntime construction recovery while the phase-B owner stays held.
// StartIntegrationTestRebind is test-only support that currently must live in
// a non-_test file for cross-package integration coverage; production callers
// are mechanically forbidden until that support can be moved behind a test
// target.
func TestWithAttachedHasNoProductionCallsiteOutsideProcessSupervisor(t *testing.T) {
	roots := []string{filepath.Join(".."), filepath.Join("..", "..", "cmd")}
	withAttachedCallsites := 0
	rebindCallsites := 0
	integrationTestRebindCallsites := 0
	for _, root := range roots {
		filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if strings.Contains(filepath.ToSlash(path), filepath.ToSlash(filepath.Join("..", "processsupervisor"))) {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if parseErr != nil {
				return nil
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
				if selector.Sel.Name == "WithAttached" {
					withAttachedCallsites++
				}
				if selector.Sel.Name == "RebindOwnerSuccessorForAttachedRecovery" {
					rebindCallsites++
				}
				if selector.Sel.Name == "StartIntegrationTestRebind" {
					integrationTestRebindCallsites++
				}
				return true
			})
			return nil
		})
	}
	if withAttachedCallsites != 1 {
		t.Fatalf("WithAttached has %d production callsite(s) outside internal/processsupervisor; want exactly 1 (the rebind transport adapter)", withAttachedCallsites)
	}
	if rebindCallsites != 1 {
		t.Fatalf("RebindOwnerSuccessorForAttachedRecovery has %d production callsite(s); want exactly 1 in productionruntime construction recovery", rebindCallsites)
	}
	if integrationTestRebindCallsites != 0 {
		t.Fatalf("StartIntegrationTestRebind has %d production callsite(s); test support must never be reachable from production code", integrationTestRebindCallsites)
	}
}

func parseAttachArchitectureFile(t *testing.T, name string) *ast.File {
	t.Helper()
	path := filepath.Join(".", name)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}
