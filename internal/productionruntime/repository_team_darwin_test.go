//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
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
	return application.ApproveInitialTeamRequest{ProtocolRevision: application.InitialTeamApprovalProtocol, RequestID: "approval-1", Deadline: "2030-01-01T00:00:00Z", InputsDigest: canonical.DigestBytes(raw), Inputs: raw}
}

func TestRepositoryTeamApprovalUsesHeldOwnerAndColdReplay(t *testing.T) {
	fixture := newPublicFixedDeliveryInputs(t)
	ns := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: fixture.repository}
	repoDigest, err := ns.Digest()
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs.Acquisition.Scope.AuthorityNamespaceID = ns
	fixture.inputs.Acquisition.Scope.RepositoryIdentityDigest = repoDigest
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
	client, err := OpenFixedEndpointClientAuthority(context.Background(), fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.VerifyInitialTeamReadback(context.Background(), request, nil); err != nil {
		t.Fatalf("absence: %v", err)
	}
	first, err := session.ApproveInitialTeam(context.Background(), request)
	if err != nil || first.GoalID != "team-session" || first.PlanRevision != 1 || first.ObligationCount != 3 || first.FactDigest == "" {
		t.Fatalf("approval=%+v err=%v", first, err)
	}
	if err := client.VerifyInitialTeamReadback(context.Background(), request, &first); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if err := client.VerifyInitialTeamReadback(context.Background(), request, nil); err == nil {
		t.Fatal("false absence accepted")
	}
	forged := first
	forged.FactDigest = canonical.DigestBytes([]byte("forged"))
	if err := client.VerifyInitialTeamReadback(context.Background(), request, &forged); err == nil {
		t.Fatal("forged approval accepted")
	}
	queried, found, err := session.ReconcileInitialTeamApproval(context.Background(), request)
	if err != nil || !found || queried != first {
		t.Fatalf("query=%+v found=%t err=%v", queried, found, err)
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
	if err := client.VerifyInitialTeamReadback(context.Background(), request, &first); err == nil {
		t.Fatal("stale client owner accepted")
	}
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

func repositoryTeamCreationRequest(t *testing.T, fixture publicFixedDeliveryInputs) application.ApproveInitialTeamRequest {
	t.Helper()
	request := repositoryTeamRequest(t, fixture)
	var inputs goal.TeamInputs
	if json.Unmarshal(request.Inputs, &inputs) != nil {
		t.Fatal("decode fixture")
	}
	for i := range inputs.Nodes {
		node := &inputs.Nodes[i]
		taskID, runID, err := goal.TeamNodeIDs(inputs.Proposal, node.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		node.Task, err = json.Marshal(map[string]any{
			"metadata":   map[string]any{"id": taskID},
			"repository": map[string]any{"path": fixture.repository, "baseRef": inputs.BaseSHA},
			"work":       map[string]any{"context": "session-only fixture"},
		})
		if err != nil {
			t.Fatal(err)
		}
		node.Policy, err = json.Marshal(map[string]any{"taskId": taskID, "runId": runID})
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	request.Inputs, err = canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	request.InputsDigest = canonical.DigestBytes(request.Inputs)
	return request
}

func TestRepositoryTeamPreparationRequiresApprovalAndReusesColdFactWithoutProbe(t *testing.T) {
	fixture := newPublicFixedDeliveryInputs(t)
	request := repositoryTeamCreationRequest(t, fixture)
	fixture.inputs.TeamInputPreflight = func(raw []byte) error {
		if !bytes.Equal(raw, request.Inputs) {
			return errors.New("fixture mismatch")
		}
		return nil
	}
	preparations := 0
	fixture.inputs.TeamRunPreparer = func(ctx context.Context, task, policy []byte, runID string) ([]byte, error) {
		preparations++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Explicit no-process preparation fixture. It does not claim a real Pi
		// probe; the constructor in sealed_application supplies planning.Prepare.
		return json.Marshal(map[string]any{
			"runId": runID, "repositoryRoot": fixture.repository, "baseSha": strings.Repeat("a", 40),
			"preparedAt": time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
			"task":       json.RawMessage(task), "policy": json.RawMessage(policy),
			"capability":        map[string]any{"adapterId": "pi", "probeStatus": "supported"},
			"selectionAttempts": []any{map[string]any{"AdapterID": "pi", "Outcome": "selected"}},
		})
	}
	session, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if _, err := session.PrepareInitialTeamRun(context.Background(), request, "service"); err == nil || preparations != 0 {
		t.Fatalf("unapproved plan reached preparation: count=%d err=%v", preparations, err)
	}
	if _, err := session.ApproveInitialTeam(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := session.PrepareInitialTeamRun(context.Background(), request, "integration"); err == nil || preparations != 0 {
		t.Fatalf("integration prepared without upstream candidate: count=%d err=%v", preparations, err)
	}
	first, err := session.PrepareInitialTeamRun(context.Background(), request, "service")
	if err != nil || first.FactDigest == "" || preparations != 1 {
		t.Fatalf("prepare: count=%d err=%v", preparations, err)
	}
	second, err := session.PrepareInitialTeamRun(context.Background(), request, "service")
	if err != nil || second.FactDigest != first.FactDigest || preparations != 1 {
		t.Fatalf("exact replay: count=%d err=%v", preparations, err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	successor, err := OpenRepositorySession(context.Background(), fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer successor.Close()
	cold, err := successor.PrepareInitialTeamRun(context.Background(), request, "service")
	if err != nil || cold.FactDigest != first.FactDigest || !bytes.Equal(cold.Inputs, first.Inputs) || preparations != 1 {
		t.Fatalf("cold replay re-probed or changed frozen input: count=%d err=%v", preparations, err)
	}
	changed := request
	changed.RequestID = "different-approval"
	if _, err := successor.PrepareInitialTeamRun(context.Background(), changed, "service"); err == nil || preparations != 1 {
		t.Fatal("changed approval accepted or re-probed")
	}
}

func TestRepositoryTeamPreparationFailureLeavesNoCreationFact(t *testing.T) {
	for _, mode := range []string{"missing", "error", "changed", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newPublicFixedDeliveryInputs(t)
			request := repositoryTeamCreationRequest(t, fixture)
			fixture.inputs.TeamInputPreflight = func([]byte) error { return nil }
			if mode != "missing" {
				fixture.inputs.TeamRunPreparer = func(context.Context, []byte, []byte, string) ([]byte, error) {
					if mode == "error" {
						return nil, errors.New("fixture preparation failed")
					}
					return []byte("{}"), nil
				}
			}
			session, err := OpenRepositorySession(context.Background(), fixture.inputs)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			if _, err := session.ApproveInitialTeam(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if _, err := session.PrepareInitialTeamRun(ctx, request, "service"); err == nil {
				t.Fatal("invalid preparation accepted")
			}
			if _, found, err := session.ingress.ReadTeamRunCreation(session.acquisition.Scope, "team-session", "service"); err != nil || found {
				t.Fatalf("failed preparation wrote creation fact: found=%v err=%v", found, err)
			}
		})
	}
}
