//go:build darwin && arm64

package productionruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// Real owner/RB1/frozen creation/held Run qualification; fake Prepare and Run
// materializer never execute a Worker, oracle, Decision or delivery producer.
func TestRepositoryTaskObjectiveRequiresExactNewTaskApproval(t *testing.T) {
	ctx := context.Background()
	fixture, _, _, _ := materializationFixture(t)
	ns := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: fixture.repository}
	digest, err := ns.Digest()
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs.Acquisition.Scope.AuthorityNamespaceID = ns
	fixture.inputs.Acquisition.Scope.RepositoryIdentityDigest = digest
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs.TaskTemplate, err = planning.OpenTaskTemplate(repositoryTaskTemplate(t, fixture), validator)
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs.TeamInputPreflight = func(raw []byte) error { _, err := planning.PreviewTeamInputs(raw, validator); return err }
	session, err := OpenRepositorySession(ctx, fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	draft, err := session.CreateTask(ctx, application.CreateTaskRequest{IdempotencyKey: "objective-1", Submission: goal.TaskSubmission{Template: goal.TaskTemplateOrderQuote, Intent: "创建订单报价"}})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := session.ApproveTask(ctx, application.ApproveTaskRequest{TaskID: draft.ID, IdempotencyKey: "approve-1", ExpectedRevision: 1, PreviewDigest: draft.PreviewDigest})
	if err != nil {
		t.Fatal(err)
	}
	created, err := session.MaterializeApprovedInitialTeamRun(ctx, draft.ID, "service", approved.Approval.FactDigest)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := session.runs.AcquireExisting(created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	taskData, err := runstore.ReadFileUnderLease(lease, 2<<20, "task-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	consume := func(policy *review.ObjectivePolicy) error {
		called = true
		var task domain.TaskSpec
		_ = json.Unmarshal(taskData, &task)
		if policy.NodeID != "service" || policy.Command.ID != task.Acceptance.Commands[0].ID || policy.OracleDigest != "sha256:"+planning.OrderQuoteOracleDigest || policy.ReadEvidence != nil {
			t.Fatal("wrong or preauthorized policy")
		}
		return nil
	}
	if err := session.WithCurrentTaskObjective(ctx, created.RunID, taskData, consume); err != nil || !called {
		t.Fatal("qualified Task denied", err)
	}
	called = false
	if err := session.WithCurrentTaskObjective(ctx, created.RunID, []byte("{}"), consume); err == nil || called {
		t.Fatal("changed task reached callback")
	}
	if _, err := session.HaltInitialTeam(ctx, draft.ID, "service", approved.Approval.FactDigest, "review"); err != nil {
		t.Fatal(err)
	}
	if err := session.WithCurrentTaskObjective(ctx, created.RunID, taskData, consume); err == nil || called {
		t.Fatal("halted Task granted auto Decision")
	}
	if _, err := session.ReadTaskArtifact(ctx, draft.ID); !application.HasReason(err, application.ReasonTaskArtifactNotReady) {
		t.Fatal("pending Task claimed artifact", err)
	}
}

func TestRepositoryOriginalTeamCannotInheritTaskObjective(t *testing.T) {
	fixture, request, _, _ := materializationFixture(t)
	ctx := context.Background()
	session, err := OpenRepositorySession(ctx, fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err := session.ApproveInitialTeam(ctx, request); err != nil {
		t.Fatal(err)
	}
	created, err := session.MaterializeInitialTeamRun(ctx, request, "service")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := session.runs.AcquireExisting(created.RunID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	task, err := runstore.ReadFileUnderLease(lease, 2<<20, "task-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := session.WithCurrentTaskObjective(ctx, created.RunID, task, func(*review.ObjectivePolicy) error { called = true; return nil }); err == nil || called {
		t.Fatal("AF_UNIX team inherited auto authority")
	}
}
