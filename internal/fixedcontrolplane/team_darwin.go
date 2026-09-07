//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"errors"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

func (router *HTTPRouter) initialTeam(ctx context.Context, authenticated RequestBinding, request httpRequest, deadline time.Time) (httpResponse, int, error) {
	var input application.ApproveInitialTeamRequest
	if decodeHTTPBody(request.body, &input) != nil {
		return httpResponse{}, 400, ErrInvalid
	}
	if _, _, err := input.Frozen(); err != nil {
		return httpResponse{}, 400, ErrInvalid
	}
	if readBinding(request.requestKey, request.body, request.operation, input, deadline) != authenticated {
		return httpResponse{}, 409, ErrConflict
	}
	port, ok := router.application.(application.InitialTeamApplicationPort)
	if !ok {
		return httpResponse{}, 404, errHTTPUnsupported
	}
	var committed application.InitialTeamApprovalProjection
	if request.operation == "approve-initial-team" {
		if input.RequestID != request.requestKey || input.Deadline != deadline.Format(time.RFC3339Nano) {
			return httpResponse{}, 409, ErrConflict
		}
		var err error
		committed, err = port.ApproveInitialTeam(ctx, input)
		if err != nil {
			return httpResponse{}, applicationHTTPStatus(err), err
		}
		if committed.Validate() != nil {
			return httpResponse{}, 409, ErrConflict
		}
	}
	// Read after commit (or read only) through the current owner/RB1. Never
	// automatically reissue approval when its commit/response is uncertain.
	projection, found, err := port.ReconcileInitialTeamApproval(ctx, input)
	if err != nil {
		return httpResponse{}, applicationHTTPStatus(err), err
	}
	if request.operation == "approve-initial-team" && (!found || projection != committed) {
		return httpResponse{}, 409, ErrConflict
	}
	response := successHTTPResponse(request.operation, nil, nil, nil, nil)
	if found {
		_, digest, _ := input.Frozen()
		if projection.Validate() != nil || projection.InputsDigest != input.InputsDigest || projection.RequestDigest != digest {
			return httpResponse{}, 409, ErrConflict
		}
		response.TeamApproval = &projection
		if request.operation == "reconcile-team-approval" {
			if outcomes, ok := router.application.(application.InitialTeamOutcomePort); ok {
				result, exists, err := outcomes.ReadInitialTeamOutcome(ctx, input)
				if err != nil {
					return httpResponse{}, applicationHTTPStatus(err), err
				}
				if exists {
					if result.Validate() != nil || result.Outcome.GoalId != projection.GoalID || result.PlanFactDigest != projection.FactDigest {
						return httpResponse{}, 409, ErrConflict
					}
					response.TeamOutcome = &result
				}
			}
		}
	}
	return response, 200, nil
}

func CallApproveInitialTeam(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, request application.ApproveInitialTeamRequest) (application.InitialTeamApprovalProjection, error) {
	if ctx == nil {
		return application.InitialTeamApprovalProjection{}, ErrInvalid
	}
	if _, _, err := request.Frozen(); err != nil {
		return application.InitialTeamApprovalProjection{}, ErrInvalid
	}
	deadline, _ := time.Parse(time.RFC3339Nano, request.Deadline)
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	response, err := call(ctx, authority, "approve-initial-team", "/v1/teams/approve", request.RequestID, request, deadline)
	if err != nil {
		return application.InitialTeamApprovalProjection{}, err
	}
	if validateInitialTeamHTTPResponse(response) != nil || response.TeamApproval == nil || authority.VerifyInitialTeamReadback(ctx, request, response.TeamApproval) != nil {
		return application.InitialTeamApprovalProjection{}, ErrConflict
	}
	return *response.TeamApproval, nil
}

func CallReconcileInitialTeamApproval(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, requestKey string, request application.ApproveInitialTeamRequest, deadline time.Time) (application.InitialTeamApprovalProjection, bool, error) {
	approval, _, found, err := CallReadInitialTeamResult(ctx, authority, requestKey, request, deadline)
	return approval, found, err
}

func CallReadInitialTeamResult(ctx context.Context, authority *productionruntime.FixedEndpointAuthority, requestKey string, request application.ApproveInitialTeamRequest, deadline time.Time) (application.InitialTeamApprovalProjection, *application.InitialTeamOutcomeProjection, bool, error) {
	if ctx == nil {
		return application.InitialTeamApprovalProjection{}, nil, false, ErrInvalid
	}
	if _, _, err := request.Frozen(); err != nil {
		return application.InitialTeamApprovalProjection{}, nil, false, ErrInvalid
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	response, err := call(ctx, authority, "reconcile-team-approval", "/v1/teams/reconcile-approval", requestKey, request, deadline)
	if err != nil {
		return application.InitialTeamApprovalProjection{}, nil, false, err
	}
	if validateInitialTeamHTTPResponse(response) != nil || authority.VerifyInitialTeamReadback(ctx, request, response.TeamApproval) != nil || authority.VerifyInitialTeamOutcomeReadback(ctx, response.TeamApproval, response.TeamOutcome) != nil {
		return application.InitialTeamApprovalProjection{}, nil, false, errors.Join(ErrConflict, err)
	}
	if response.TeamApproval == nil {
		return application.InitialTeamApprovalProjection{}, nil, false, nil
	}
	return *response.TeamApproval, response.TeamOutcome, true, nil
}

func validateInitialTeamHTTPResponse(response httpResponse) error {
	if response.Disposition != "success" || response.ReasonCode != "" || response.Status != nil || response.Run != nil || response.Started != nil || response.DeliveryReceipt != nil || response.Collected != nil || response.Verification != nil || response.ReviewPacket != nil || response.Decision != nil || response.Stopped != nil || response.LifecycleReceipt != nil {
		return ErrConflict
	}
	if response.TeamApproval != nil && response.TeamApproval.Validate() != nil {
		return ErrConflict
	}
	if response.TeamOutcome != nil && (response.TeamOutcome.Validate() != nil || response.TeamApproval == nil || response.TeamOutcome.Outcome.GoalId != response.TeamApproval.GoalID || response.TeamOutcome.PlanFactDigest != response.TeamApproval.FactDigest) {
		return ErrConflict
	}
	return nil
}
