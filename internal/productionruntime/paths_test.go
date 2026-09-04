package productionruntime

import (
	"path/filepath"
	"testing"
)

func TestCompositionPathsStayBelowRuntimeV1(t *testing.T) {
	stateRoot := filepath.Join(string(filepath.Separator), "repository", ".marshal")
	runtimeRoot := filepath.Join(stateRoot, RuntimeRootDirName)
	if got := RuntimeRootPath(stateRoot); got != runtimeRoot {
		t.Fatalf("runtime root=%q want=%q", got, runtimeRoot)
	}
	ingress, dispatch, allocations, owner := CompositionPaths(stateRoot)
	want := []string{
		filepath.Join(runtimeRoot, ResultIngressDirName),
		filepath.Join(runtimeRoot, DispatchLedgerDirName),
		filepath.Join(runtimeRoot, AllocationRootDirName),
		filepath.Join(runtimeRoot, OwnerDirName),
	}
	for index, got := range []string{ingress, dispatch, allocations, owner} {
		if got != want[index] {
			t.Fatalf("path[%d]=%q want=%q", index, got, want[index])
		}
	}
}
