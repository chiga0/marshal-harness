package productionruntime

import "path/filepath"

// Directory names under the repository state root that the fixed CLI
// composition owns. The held owner directory and the held result-ingress
// descriptor are opened from these locations and handed to ComposeRuntime as
// descriptors; the paths themselves never enter authority facts.
const (
	ResultIngressDirName  = "result-ingress"
	DispatchLedgerDirName = "dispatch-ledger"
	AllocationRootDirName = "allocations"
	OwnerDirName          = "owner"
)

// CompositionPaths derives the sealed composition directory layout from the
// repository state root.
func CompositionPaths(stateRoot string) (ingressDir, dispatchLedgerDir, allocationRoot, ownerDir string) {
	return filepath.Join(stateRoot, ResultIngressDirName),
		filepath.Join(stateRoot, DispatchLedgerDirName),
		filepath.Join(stateRoot, AllocationRootDirName),
		filepath.Join(stateRoot, OwnerDirName)
}
