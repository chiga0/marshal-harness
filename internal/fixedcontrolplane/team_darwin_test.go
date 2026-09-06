//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

// Transport contract test with explicit application fixture. Actual held
// owner/ledger replay and readback are exercised in productionruntime tests.
type teamHTTPApplication struct {
	*httpApplicationStub
	approvals, reads int
	projection       application.InitialTeamApprovalProjection
	found            bool
	approveErr       error
	readErr          error
}

func (app *teamHTTPApplication) ApproveInitialTeam(context.Context, application.ApproveInitialTeamRequest) (application.InitialTeamApprovalProjection, error) {
	app.approvals++
	if app.approveErr == nil {
		app.found = true
	}
	return app.projection, app.approveErr
}
func (app *teamHTTPApplication) ReconcileInitialTeamApproval(context.Context, application.ApproveInitialTeamRequest) (application.InitialTeamApprovalProjection, bool, error) {
	app.reads++
	return app.projection, app.found, app.readErr
}

func TestAuthenticatedTeamApprovalAndReadOnlyResponseLossQuery(t *testing.T) {
	for _, mode := range []string{"approve", "query-present", "query-absent", "query-unknown", "wrong-binding", "wrong-key", "changed-deadline", "unknown-field", "missing-port", "commit-unknown"} {
		t.Run(mode, func(t *testing.T) {
			deadline := time.Now().UTC().Add(time.Minute)
			input := application.ApproveInitialTeamRequest{ProtocolRevision: application.InitialTeamApprovalProtocol, RequestID: "approval-1", Deadline: deadline.Format(time.RFC3339Nano), Inputs: []byte(`{"transportFixture":true}`)}
			input.InputsDigest = canonical.DigestBytes(input.Inputs)
			_, digest, err := input.Frozen()
			if err != nil {
				t.Fatal(err)
			}
			base, delivery := testHTTPApplication()
			app := &teamHTTPApplication{httpApplicationStub: base, projection: application.InitialTeamApprovalProjection{GoalID: "fixture-team", PlanRevision: 1, InputsDigest: input.InputsDigest, RequestDigest: digest, FactDigest: canonical.DigestBytes([]byte("fixture-fact")), ObligationCount: 3}}
			var port application.PublicApplicationPort = app
			operation, path, key := "approve-initial-team", "/v1/teams/approve", input.RequestID
			wantStatus, wantApprovals, wantReads := 200, 1, 1
			switch mode {
			case "query-present", "query-absent", "query-unknown":
				operation, path, key = "reconcile-team-approval", "/v1/teams/reconcile-approval", "fresh-query-key"
				// Original approval may have expired. This is a fresh read, not
				// another mutation with an implicitly extended deadline.
				input.Deadline = "2020-01-01T00:00:00Z"
				_, app.projection.RequestDigest, _ = input.Frozen()
				app.found = mode == "query-present"
				wantApprovals = 0
				if mode == "query-unknown" {
					app.readErr = errors.New("unknown ledger state")
					wantStatus = 503
				}
			case "wrong-binding", "wrong-key", "changed-deadline":
				wantStatus, wantApprovals, wantReads = 409, 0, 0
				if mode == "wrong-key" {
					key = "different-key"
				}
				if mode == "changed-deadline" {
					input.Deadline = deadline.Add(time.Second).Format(time.RFC3339Nano)
				}
			case "unknown-field":
				wantStatus, wantApprovals, wantReads = 400, 0, 0
			case "missing-port":
				port = base
				wantStatus, wantApprovals, wantReads = 404, 0, 0
			case "commit-unknown":
				app.approveErr = errors.New("unknown commit")
				wantStatus, wantReads = 503, 0
			}
			body := canonicalBody(t, input)
			if mode == "unknown-field" {
				body = append(body[:len(body)-1], []byte(`,"actor":"not-authority"}`)...)
			}
			binding := readBinding(key, body, operation, input, deadline)
			if mode == "wrong-binding" {
				binding.RequestDigest = canonical.DigestBytes([]byte("different-body"))
			}
			router, err := NewHTTPRouter(port, delivery)
			if err != nil {
				t.Fatal(err)
			}
			status, response, _ := callHTTPRouter(t, router, binding, path, key, body)
			if status != wantStatus || app.approvals != wantApprovals || app.reads != wantReads {
				t.Fatalf("status=%d approvals=%d reads=%d", status, app.approvals, app.reads)
			}
			if mode == "query-absent" && response.TeamApproval != nil {
				t.Fatal("absence became approval")
			}
			if (mode == "approve" || mode == "query-present") && (response.TeamApproval == nil || *response.TeamApproval != app.projection) {
				t.Fatal("response lost exact approval")
			}
		})
	}
}
