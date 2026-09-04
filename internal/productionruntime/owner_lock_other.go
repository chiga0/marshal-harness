//go:build !darwin || !arm64

package productionruntime

import (
	"os"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

func repositoryOwnerTransitionLabel(error) (string, bool) {
	return "", false
}

func openRepositoryOwnerScopeLock(*os.File, resultingress.ControlOwnerScope) (repositoryOwnerScopeLock, error) {
	return nil, application.NewError("repository-owner-lock", application.ReasonPlatformProfileUnavailable)
}
