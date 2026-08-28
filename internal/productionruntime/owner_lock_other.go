//go:build !darwin || !arm64

package productionruntime

import (
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

func openRepositoryOwnerLock(string, resultingress.ControlOwnerAcquisition) (repositoryOwnerLock, error) {
	return nil, application.NewError("repository-owner-lock", application.ReasonPlatformProfileUnavailable)
}
