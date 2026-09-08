//go:build darwin && arm64

package productionruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// The original Verifier, PacketBuilder, DecisionImporter, accepted-input
// reader, Git reconstruction and RB1 are real. Only model preparation/start/
// collection use the existing deterministic fixture. No cleanup receipt or
// terminal disposition is invented to make cancellation appear complete.
func TestRepositoryTaskCancelDeliveryStopBeforeOutcome(t *testing.T) {
	for _, acceptedNodes := range []int{2, 3} {
		name := "before-integration"
		if acceptedNodes == 3 {
			name = "accepted-integration-before-outcome"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newTaskCancelDeliveryFixture(t)
			runs := f.accept(t, acceptedNodes)
			upstreams, ready, err := f.session.ReadAcceptedTeamInputs(ctx, f.task.ID, "integration", f.task.Approval.FactDigest)
			if err != nil || !ready || len(upstreams) != 2 {
				t.Fatalf("original independently accepted upstreams unavailable: %v", err)
			}
			beforeRuns := f.runHeads(t, runs)
			current, err := f.session.ReadTask(ctx, f.task.ID)
			if err != nil || current.Outcome != nil {
				t.Fatal("fixture crossed the cancellation linearization point", err)
			}
			stopping, err := f.session.CancelTask(ctx, application.CancelTaskRequest{TaskID: f.task.ID, IdempotencyKey: "delivery:stop", ExpectedRevision: current.Revision})
			if err != nil || stopping.Status != "cancelling" || !stopping.CancellationRequested {
				t.Fatalf("stop intent: status=%s err=%v", stopping.Status, err)
			}
			before := f.ledger(t)
			combines, exports := f.combines.Load(), f.exports.Load()
			beforeBlobs := f.blobs(t)
			if values, ready, err := f.session.ReadAcceptedTeamInputs(ctx, f.task.ID, "integration", f.task.Approval.FactDigest); len(values) != 0 || ready || !taskCancelDeliveryStopped(err) {
				t.Fatalf("stopped Task exposed integration inputs: ready=%t err=%v", ready, err)
			}
			if acceptedNodes == 2 {
				if _, err := f.session.MaterializeApprovedInitialTeamRun(ctx, f.task.ID, "integration", f.task.Approval.FactDigest); !taskCancelDeliveryStopped(err) {
					t.Fatal("integration materialized after stop", err)
				}
				if _, created, err := f.session.ingress.ReadTeamRunCreation(f.session.acquisition.Scope, f.task.ID, "integration"); err != nil || created {
					t.Fatal("stop appended integration creation", err)
				}
			}
			// The resident must skip stopped work, not halt the whole service
			// because a legitimate stop defeated a stale finalization hint.
			if err := f.session.FinalizeReadyInitialTeams(ctx); err != nil {
				t.Fatal("stopped Task poisoned the finalization loop", err)
			}
			if _, err := f.session.BuildTaskDelivery(ctx, f.task.ID); !taskCancelDeliveryStopped(err) {
				t.Fatal("delivery producer crossed stop", err)
			}
			if _, err := f.session.ReadTaskArtifact(ctx, f.task.ID); !application.HasReason(err, application.ReasonTaskArtifactNotReady) {
				t.Fatal("stopped Task served an unproduced artifact", err)
			}
			if _, done, err := f.session.ingress.ReadTeamOutcome(f.session.acquisition.Scope, f.task.ID); err != nil || done {
				t.Fatal("stop appended a completed outcome", err)
			}
			if _, found, err := f.session.ingress.ReadTaskDelivery(f.session.acquisition.Scope, f.task.ID); err != nil || found {
				t.Fatal("stop appended a delivery reference", err)
			}
			if _, halted, err := f.session.ingress.ReadTeamPlanHalt(f.session.acquisition.Scope, f.task.ID); err != nil || halted {
				t.Fatal("expected stop created a failure halt", err)
			}
			if f.combines.Load() != combines || f.exports.Load() != exports || !reflect.DeepEqual(beforeBlobs, f.blobs(t)) || !bytes.Equal(before, f.ledger(t)) {
				t.Fatal("post-stop observation wrote Git, blob or authority")
			}
			if !reflect.DeepEqual(beforeRuns, f.runHeads(t, runs)) {
				t.Fatal("stop rewrote accepted nodes or their budgets")
			}
			pending, err := f.session.ReadTask(ctx, f.task.ID)
			if err != nil || pending.Status != "cancelling" || pending.Delivery != nil || pending.Outcome != nil {
				t.Fatalf("intent was misrepresented as cancellation/delivery completion: %s %v", pending.Status, err)
			}
			if _, err := f.session.CreateTask(ctx, application.CreateTaskRequest{IdempotencyKey: "delivery:unrelated", Submission: goal.TaskSubmission{Template: goal.TaskTemplateOrderQuote, Intent: "另一项独立订单报价任务"}}); err != nil {
				t.Fatal("a stopped Task blocked unrelated admission", err)
			}
		})
	}
}

func TestRepositoryTaskCancelDeliveryOutcomeWinsDuringExport(t *testing.T) {
	f := newTaskCancelDeliveryFixture(t)
	runs := f.accept(t, 3)
	beforeRuns := f.runHeads(t, runs)
	// The real Git/Verifier preparation is governed by the package test budget,
	// not the rendezvous deadline. Slow CI must not spend the scenario's lease
	// on setup and later misreport an expired context as an owner transition.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := f.session.FinalizeReadyInitialTeams(ctx); err != nil {
		t.Fatal(err)
	}
	outcome, found, err := f.session.ingress.ReadTeamOutcome(f.session.acquisition.Scope, f.task.ID)
	if err != nil || !found || outcome.FactDigest == "" {
		t.Fatal("real completed outcome missing", err)
	}
	current, err := f.session.ReadTask(ctx, f.task.ID)
	if err != nil || current.Outcome == nil || current.Delivery != nil {
		t.Fatal("expected outcome without delivery", err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseExport := func() { releaseOnce.Do(func() { close(release) }) }
	f.beforeExportReturn = func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	type buildResult struct {
		delivery goal.TaskDelivery
		err      error
	}
	built := make(chan buildResult, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		value, err := f.session.BuildTaskDelivery(ctx, f.task.ID)
		built <- buildResult{value, err}
	}()
	defer func() {
		releaseExport()
		cancel()
		<-finished
	}()
	select {
	case <-entered:
	case result := <-built:
		t.Fatal("producer did not reach the original Git exporter", result.err)
	case <-ctx.Done():
		t.Fatal("export rendezvous timed out", ctx.Err())
	}
	before := f.ledger(t)
	request := application.CancelTaskRequest{TaskID: f.task.ID, IdempotencyKey: "delivery:too-late", ExpectedRevision: current.Revision}
	if _, err := f.session.CancelTask(ctx, request); !application.HasReason(err, application.ReasonStopTooLate) {
		t.Fatal("outcome did not win over concurrent cancel", err)
	}
	if !bytes.Equal(before, f.ledger(t)) {
		t.Fatal("too-late cancel appended authority during export")
	}
	stop, disposition, _, err := f.session.ingress.ReadTaskCancellation(f.session.acquisition.Scope, f.task.ID)
	if err != nil || stop.FactDigest != "" || disposition.FactDigest != "" {
		t.Fatal("too-late cancel persisted an intent/disposition", err)
	}
	releaseExport()
	var result buildResult
	select {
	case result = <-built:
	case <-ctx.Done():
		t.Fatal("delivery did not finish after export release", ctx.Err())
	}
	if result.err != nil || result.delivery.OutcomeFactDigest != outcome.FactDigest || f.exports.Load() != 1 {
		t.Fatal("late cancel damaged the frozen producer chain", result.err)
	}
	artifact, err := f.session.ReadTaskArtifact(ctx, f.task.ID)
	if err != nil || canonical.DigestBytes(artifact.Content) != result.delivery.ContentDigest || !reflect.DeepEqual(artifact.Manifest, result.delivery) {
		t.Fatal("late cancel damaged download", err)
	}
	completed, err := f.session.ReadTask(ctx, f.task.ID)
	if err != nil || completed.Status != "completed" || completed.CancellationRequested || completed.Delivery == nil {
		t.Fatalf("late cancel changed the public completion projection: %s %v", completed.Status, err)
	}
	before = f.ledger(t)
	if _, err := f.session.CancelTask(ctx, request); !application.HasReason(err, application.ReasonStopTooLate) {
		t.Fatal("completed delivery accepted cancellation", err)
	}
	if !bytes.Equal(before, f.ledger(t)) || !reflect.DeepEqual(beforeRuns, f.runHeads(t, runs)) {
		t.Fatal("completed cancellation changed successful authority")
	}
	if _, halted, err := f.session.ingress.ReadTeamPlanHalt(f.session.acquisition.Scope, f.task.ID); err != nil || halted {
		t.Fatal("late cancel created a failure halt", err)
	}
	if err := f.session.Close(); err != nil {
		t.Fatal(err)
	}
	// Cold recovery is a separate bounded scenario, using the same original
	// inputs and persisted bytes; only the caller context is renewed.
	coldCtx, cancelCold := context.WithTimeout(context.Background(), time.Minute)
	defer cancelCold()
	f.session, err = OpenRepositorySession(coldCtx, f.fixture.inputs)
	if err != nil {
		t.Fatalf("cold reopen failed: err=%v contextErr=%v", err, coldCtx.Err())
	}
	cold, err := f.session.ReadTaskArtifact(coldCtx, f.task.ID)
	if err != nil || !bytes.Equal(cold.Content, artifact.Content) || !reflect.DeepEqual(cold.Manifest, artifact.Manifest) || f.exports.Load() != 1 {
		t.Fatalf("cold download rebuilt or changed completed artifact: err=%v contextErr=%v", err, coldCtx.Err())
	}
}

func taskCancelDeliveryStopped(err error) bool {
	return errors.Is(err, resultingress.ErrTaskStopped) || application.HasReason(err, application.ReasonRunStopped)
}

type taskCancelDeliveryFixture struct {
	fixture            publicFixedDeliveryInputs
	session            *RepositorySession
	manager            gitworktree.Repository
	task               application.TaskProjection
	combines           atomic.Int32
	exports            atomic.Int32
	beforeExportReturn func(context.Context) error
}

// Reuse the original producer fixture's components without changing its
// existing regression or production composition. Git roots are initialized
// before reopening the held root, exactly as the real CLI requires.
func newTaskCancelDeliveryFixture(t *testing.T) *taskCancelDeliveryFixture {
	t.Helper()
	ctx := context.Background()
	fixture, _, _, _ := materializationFixture(t)
	ns := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: fixture.repository}
	digest, err := ns.Digest()
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs.Acquisition.Scope.AuthorityNamespaceID = ns
	fixture.inputs.Acquisition.Scope.RepositoryIdentityDigest = digest
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", fixture.repository}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	oracle, err := os.ReadFile(filepath.Join("..", "..", "scripts", "order-quote-team-oracle.py"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fixture.repository, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repository, "scripts", "order-quote-team-oracle.py"), oracle, 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "scripts/order-quote-team-oracle.py")
	git("-c", "user.name=Marshal Test", "-c", "user.email=marshal@example.invalid", "commit", "-q", "-m", "frozen oracle")
	base := git("rev-parse", "HEAD")
	manager, err := gitworktree.Open(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(fixture.repository, ".marshal")
	for _, name := range []string{"locks", "worktrees"} {
		if err := os.MkdirAll(filepath.Join(stateRoot, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	var template goal.TeamInputs
	if err := json.Unmarshal(repositoryTaskTemplate(t, fixture), &template); err != nil {
		t.Fatal(err)
	}
	template.BaseSHA = base
	for i := range template.Nodes {
		var task domain.TaskSpec
		if err := json.Unmarshal(template.Nodes[i].Task, &task); err != nil {
			t.Fatal(err)
		}
		task.Repository.BaseRef = base
		template.Nodes[i].Task = repositoryTaskJSON(t, task)
	}
	fixture.inputs.TaskTemplate, err = planning.OpenTaskTemplate(repositoryTaskJSON(t, template), validator)
	if err != nil {
		t.Fatal(err)
	}
	fixture.inputs.TeamInputPreflight = func(raw []byte) error { _, err := planning.PreviewTeamInputs(raw, validator); return err }
	originalPrepare := fixture.inputs.TeamRunPreparer
	fixture.inputs.TeamRunPreparer = func(ctx context.Context, task, policy []byte, id string) ([]byte, error) {
		raw, err := originalPrepare(ctx, task, policy, id)
		if err != nil {
			return nil, err
		}
		var prepared map[string]json.RawMessage
		if err := json.Unmarshal(raw, &prepared); err != nil {
			return nil, err
		}
		var frozen domain.TaskSpec
		if err := json.Unmarshal(task, &frozen); err != nil {
			return nil, err
		}
		prepared["baseSha"], err = json.Marshal(frozen.Repository.BaseRef)
		if err != nil {
			return nil, err
		}
		return canonical.JSON(mustDeliveryJSON(t, prepared))
	}
	f := &taskCancelDeliveryFixture{manager: manager}
	fixture.inputs.TeamIntegrationBuilder = func(ctx context.Context, base, binding string, patches [][]byte) (string, string, error) {
		f.combines.Add(1)
		value, err := manager.CombineAcceptedPatches(ctx, stateRoot, base, binding, patches)
		return value.TreeSHA, value.CommitSHA, err
	}
	fixture.inputs.TeamDeliveryExporter = func(ctx context.Context, base, binding, tree, commit string, upstreams [][]byte, final []byte, paths []string) (map[string][]byte, error) {
		f.exports.Add(1)
		files, err := manager.ExportTeamDelivery(ctx, stateRoot, base, binding, tree, commit, upstreams, final, paths)
		if err == nil && f.beforeExportReturn != nil {
			err = f.beforeExportReturn(ctx)
		}
		return files, err
	}
	if err := fixture.inputs.HeldRepositoryRoot.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.inputs.HeldRepositoryRoot, err = OpenCanonicalRepositoryRoot(fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.inputs.HeldRepositoryRoot.Close() })
	f.fixture = fixture
	f.session, err = OpenRepositorySession(ctx, fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.session.Close() })
	draft, err := f.session.CreateTask(ctx, application.CreateTaskRequest{IdempotencyKey: "delivery:create", Submission: goal.TaskSubmission{Template: goal.TaskTemplateOrderQuote, Intent: "订单团队取消与交付组合回归"}})
	if err != nil {
		t.Fatal(err)
	}
	f.task, err = f.session.ApproveTask(ctx, application.ApproveTaskRequest{TaskID: draft.ID, IdempotencyKey: "delivery:approve", ExpectedRevision: 1, PreviewDigest: draft.PreviewDigest})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *taskCancelDeliveryFixture) accept(t *testing.T, count int) []string {
	t.Helper()
	files := repositoryDeliveryBusinessFiles(t)
	var runs []string
	for _, node := range []string{"service", "client", "integration"}[:count] {
		created, err := f.session.MaterializeApprovedInitialTeamRun(context.Background(), f.task.ID, node, f.task.Approval.FactDigest)
		if err != nil {
			t.Fatalf("materialize %s: %v", node, err)
		}
		acceptRepositoryDeliveryNode(t, f.fixture, f.session, f.manager, f.task.ID, node, created.RunID, files)
		runs = append(runs, created.RunID)
	}
	return runs
}

func (f *taskCancelDeliveryFixture) ledger(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.fixture.inputs.HeldIngressDir.Name(), "result-ingress.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func (f *taskCancelDeliveryFixture) blobs(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(f.fixture.repository, ".marshal", "runs", "*", "task-delivery-*.zip"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func (f *taskCancelDeliveryFixture) runHeads(t *testing.T, ids []string) []application.RunProjection {
	t.Helper()
	var runs []application.RunProjection
	for _, id := range ids {
		value, err := f.session.InspectRun(context.Background(), application.InspectRunRequest{RunID: id})
		if err != nil || value.State != domain.StateAccepted {
			t.Fatal("original independently accepted Run changed", err)
		}
		runs = append(runs, value)
	}
	return runs
}
