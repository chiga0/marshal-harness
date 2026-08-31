package productionruntime

import "github.com/chiga0/marshal-harness/internal/resultingress"

// ControlOwnerAcquisition is exposed as a composition boundary type so the
// CLI does not need to depend directly on the ingress implementation package.
type ControlOwnerAcquisition = resultingress.ControlOwnerAcquisition

// ControlOwnerScope keeps the CLI composition boundary provider-neutral while
// still allowing it to bind the acquisition to the exact authority namespace.
type ControlOwnerScope = resultingress.ControlOwnerScope
