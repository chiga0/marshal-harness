//go:build !darwin || !arm64

package productionruntime

import "github.com/chiga0/marshal-harness/internal/application"

func platformProfile() (string, error) {
	return "", application.NewError("production-runtime", application.ReasonPlatformProfileUnavailable)
}
