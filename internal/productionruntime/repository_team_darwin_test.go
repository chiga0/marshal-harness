//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
)

// This test exercises real held owner/root/RB1 composition, not HTTP operator
// authentication. Opaque node inputs and explicit preflight are session-only
// fixtures. Full Task/Policy validation is covered by planning and installed
// at the actual CLI composition root; no fixture is a production approval.
func repositoryTeamRequest(t *testing.T, fixture publicFixedDeliveryInputs) application.ApproveInitialTeamRequest {
	t.Helper()
	ns := fixture.inputs.Acquisition.Scope.AuthorityNamespaceID
	spec := goal.GoalSpecRevision{AuthorityNamespaceId: ns, GoalId: "team-session", Revision: 1, ProjectId: "orders", Repository: fixture.repository, Title: "订单交付", Description: "会话接纳测试"}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	proposal := goal.GoalPlanProposal{AuthorityNamespaceId: ns, ProposalId: "proposal-1", GoalId: spec.GoalId, ProjectId: spec.ProjectId, Repository: spec.Repository, GoalSpecRevision: 1, GoalSpecDigest: specDigest, PlannerIdentity: "not-approval"}
	for i, id := range []string{"service", "client", "integration"} {
		paths := [][]string{{"service.py"}, {"client.py"}, {"service.py", "client.py"}}
		proposal.Nodes = append(proposal.Nodes, goal.GoalNode{NodeId: id, ExecutorKind: goal.ExecutorKindImplement, Title: id, Repository: spec.Repository, Paths: paths[i], SideEffectClasses: []string{"workspace-write"}, Estimate: goal.NodeEstimate{Runs: 1, Attempts: 1, WallTimeSeconds: 60, ArtifactBytes: 100}})
	}
	proposal.Edges = []goal.GoalEdge{{From: "service", To: "integration", Kind: goal.EdgeKindDependsOn}, {From: "client", To: "integration", Kind: goal.EdgeKindDependsOn}}
	inputs := goal.TeamInputs{SchemaVersion: goal.TeamInputsVersion, Spec: spec, Proposal: proposal, BaseSHA: strings.Repeat("a", 40), Limits: goal.Guardrails{MaxNodes: 3, MaxDepth: 3, MaxFanOut: 2, MaxConcurrentNodes: 3, MaxPlanRevisions: 2, MaxTotalRuns: 6, MaxTotalAttempts: 12, MaxWallTimeSeconds: 1000, MaxComputeUnits: 100, MaxTokens: 10000, MaxArtifactBytes: 10000}, AdmissionPolicy: goal.AdmissionPolicy{ExecutorKinds: []goal.ExecutorKind{goal.ExecutorKindImplement}, Repositories: []string{spec.Repository}, Paths: []string{"service.py", "client.py"}, SideEffectClasses: []string{"workspace-write"}}}
	for _, node := range proposal.Nodes {
		role := "implement"
		if node.NodeId == "integration" {
			role = "integrate"
		}
		inputs.Nodes = append(inputs.Nodes, goal.TeamNodeInputs{NodeID: node.NodeId, Role: role, Task: json.RawMessage(`{"sessionFixture":"task"}`), Policy: json.RawMessage(`{"sessionFixture":"policy"}`)})
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return application.ApproveInitialTeamRequest{ProtocolRevision: application.InitialTeamApprovalProtocol, RequestID: "approval-1", InputsDigest: canonical.DigestBytes(raw), Inputs: raw}
}

func TestRepositoryTeamApprovalUsesHeldOwnerAndColdReplay(t *testing.T) {
	fixture := newPublicFixedDeliveryInputs(t)
	request := repositoryTeamRequest(t, fixture)
	preflights := 0
	fixture.inputs.TeamInputPreflight = func(raw []byte) error {
		preflights++
		if !bytes.Equal(raw, request.Inputs) {
			return errors.New("fixture mismatch")
		}
		return nil
	}
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	first, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil || first.GoalID != "team-session" || first.PlanRevision != 1 || first.ObligationCount != 3 || first.FactDigest == "" {
		t.Fatalf("approval=%+v err=%v", first, err)
	}
	replay, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil || replay != first || preflights != 2 {
		t.Fatalf("replay=%+v err=%v preflights=%d", replay, err, preflights)
	}
	changed := request
	changed.RequestID = "approval-2"
	if _, err := session.ApproveInitialTeam(context.Background(), changed); !application.HasReason(err, application.ReasonAuthorityConflict) {
		t.Fatalf("changed approval: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.ApproveInitialTeam(context.Background(), request); !application.HasReason(err, application.ReasonOwnerUnavailable) {
		t.Fatalf("closed owner: %v", err)
	}
	successor, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer successor.Close()
	replay, err = successor.ApproveInitialTeam(context.Background(), request)
	if err != nil || replay != first {
		t.Fatalf("cold owner replay=%+v err=%v", replay, err)
	}
}

func TestRepositoryTeamApprovalRejectsMissingDeniedOrMutatingPreflight(t *testing.T) {
	for _, mode := range []string{"missing", "denied", "mutating", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newPublicFixedDeliveryInputs(t)
			request := repositoryTeamRequest(t, fixture)
			if mode != "missing" {
				fixture.inputs.TeamInputPreflight = func(raw []byte) error {
					if mode == "denied" {
						return errors.New("reject fixture")
					}
					if mode == "mutating" {
						raw[0] = '['
					}
					return nil
				}
			}
			session, err := OpenRepositorySession(context.Background(), fixture.inputs)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if _, err := session.ApproveInitialTeam(ctx, request); err == nil {
				t.Fatal("invalid preflight admitted")
			}
			if _, found, err := session.ingress.ReadTeamPlan(session.acquisition.Scope, "team-session"); err != nil || found {
				t.Fatalf("rejection wrote fact: found=%t err=%v", found, err)
			}
		})
	}
}
