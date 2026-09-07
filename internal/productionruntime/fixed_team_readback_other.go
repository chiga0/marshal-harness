//go:build !darwin || !arm64

package productionruntime

import (
	"context"
	"github.com/chiga0/marshal-harness/internal/application"
)

func (*FixedEndpointAuthority) VerifyInitialTeamReadback(context.Context, application.ApproveInitialTeamRequest, *application.InitialTeamApprovalProjection) error {
	return ErrFixedDeliveryConflict
}

func (*FixedEndpointAuthority) VerifyInitialTeamOutcomeReadback(context.Context, *application.InitialTeamApprovalProjection, *application.InitialTeamOutcomeProjection) error {
	return ErrFixedDeliveryConflict
}
