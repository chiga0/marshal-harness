package productionruntime

import "path/filepath"

// Directory names under the repository state root that the fixed CLI
// composition owns. The held owner directory and the held result-ingress
// descriptor are opened from these locations and handed to ComposeRuntime as
// descriptors; the paths themselves never enter authority facts.
const (
	RuntimeRootDirName    = "runtime-v1"
	ResultIngressDirName  = "result-ingress"
	DispatchLedgerDirName = "dispatch-ledger"
	AllocationRootDirName = "allocations"
	OwnerDirName          = "owner"
)

// RuntimeRootPath returns the single owner-only production authority root
// frozen by ADR 0066. StateRoot/runs deliberately remains outside this tree
// because it is the existing RB1 runstore layout.
func RuntimeRootPath(stateRoot string) string {
	return filepath.Join(stateRoot, RuntimeRootDirName)
}

// CompositionPaths derives the sealed composition directory layout from the
// repository state root.
func CompositionPaths(stateRoot string) (ingressDir, dispatchLedgerDir, allocationRoot, ownerDir string) {
	runtimeRoot := RuntimeRootPath(stateRoot)
	return filepath.Join(runtimeRoot, ResultIngressDirName),
		filepath.Join(runtimeRoot, DispatchLedgerDirName),
		filepath.Join(runtimeRoot, AllocationRootDirName),
		filepath.Join(runtimeRoot, OwnerDirName)
}
