//go:build darwin && arm64

package productionruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/verification"
)

// The owner, RB1, original Verifier/Packet/Importer, Git combination/export,
// durable blob producer and cold download are real. Existing deterministic
// preparation/start/collection fixtures replace model execution only; this is
// not evidence of a real Agent or public HTTP/CLI end-to-end canary.
func TestRepositoryTaskDeliveryProducerColdReplayAndDrift(t *testing.T) {
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
		cmd := exec.Command("git", append([]string{"-C", fixture.repository}, args...)...)
		out, err := cmd.CombinedOutput()
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
	if err := os.MkdirAll(filepath.Join(stateRoot, "locks"), 0700); err != nil {
		t.Fatal(err)
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
	fixture.inputs.TeamIntegrationBuilder = func(ctx context.Context, base, binding string, patches [][]byte) (string, string, error) {
		value, err := manager.CombineAcceptedPatches(ctx, stateRoot, base, binding, patches)
		return value.TreeSHA, value.CommitSHA, err
	}
	exports := 0
	drift := false
	fixture.inputs.TeamDeliveryExporter = func(ctx context.Context, base, binding, tree, commit string, upstreams [][]byte, final []byte, paths []string) (map[string][]byte, error) {
		exports++
		if drift {
			tree = strings.Repeat("f", 40)
		}
		return manager.ExportTeamDelivery(ctx, stateRoot, base, binding, tree, commit, upstreams, final, paths)
	}
	session, err := OpenRepositorySession(ctx, fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	draft, err := session.CreateTask(ctx, application.CreateTaskRequest{IdempotencyKey: "delivery:create", Submission: goal.TaskSubmission{Template: goal.TaskTemplateOrderQuote, Intent: "交付订单报价 API 与客户端"}})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := session.ApproveTask(ctx, application.ApproveTaskRequest{TaskID: draft.ID, IdempotencyKey: "delivery:approve", ExpectedRevision: 1, PreviewDigest: draft.PreviewDigest})
	if err != nil {
		t.Fatal(err)
	}
	files := repositoryDeliveryBusinessFiles(t)
	var integrationID string
	for _, node := range []string{"service", "client", "integration"} {
		created, err := session.MaterializeApprovedInitialTeamRun(ctx, draft.ID, node, approved.Approval.FactDigest)
		if err != nil {
			t.Fatalf("materialize %s: %v", node, err)
		}
		if node == "integration" {
			integrationID = created.RunID
		}
		acceptRepositoryDeliveryNode(t, fixture, session, manager, draft.ID, node, created.RunID, files)
	}
	if err := session.FinalizeReadyInitialTeams(ctx); err != nil {
		t.Fatal("aggregate", err)
	}
	if _, err := session.ReadTaskArtifact(ctx, draft.ID); !application.HasReason(err, application.ReasonTaskArtifactNotReady) {
		t.Fatal("reference absent", err)
	}
	// A tree drift must not publish even an empty or last-patch-only ref.
	drift = true
	if _, err := session.BuildTaskDelivery(ctx, draft.ID); err == nil {
		t.Fatal("drift exported")
	}
	if _, found, err := session.ingress.ReadTaskDelivery(session.acquisition.Scope, draft.ID); err != nil || found {
		t.Fatal("drift appended delivery", err)
	}
	drift = false
	delivery, err := session.BuildTaskDelivery(ctx, draft.ID)
	if err != nil {
		t.Fatal("build durable delivery", err)
	}
	stored, found, err := session.ingress.ReadTaskDelivery(session.acquisition.Scope, draft.ID)
	if err != nil || !found || !reflect.DeepEqual(stored, delivery) || delivery.FactDigest == "" {
		t.Fatal("RB1 reference", err)
	}
	artifact, err := session.ReadTaskArtifact(ctx, draft.ID)
	if err != nil || !reflect.DeepEqual(artifact.Manifest, delivery) {
		t.Fatal("held artifact", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(artifact.Content), int64(len(artifact.Content)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]byte{}
	for _, entry := range reader.File {
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		got[entry.Name] = data
	}
	if !reflect.DeepEqual(got, files) {
		t.Fatal("download omitted accepted upstream or changed bytes")
	}
	replay, err := session.BuildTaskDelivery(ctx, draft.ID)
	if err != nil || !reflect.DeepEqual(replay, delivery) {
		t.Fatal("delivery replay appended", err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	session, err = OpenRepositorySession(ctx, fixture.inputs)
	if err != nil {
		t.Fatal(err)
	}
	before := exports
	cold, err := session.ReadTaskArtifact(ctx, draft.ID)
	if err != nil || !bytes.Equal(cold.Content, artifact.Content) || !reflect.DeepEqual(cold.Manifest, delivery) || exports != before {
		t.Fatal("cold download regenerated or drifted", err)
	}
	// Corruption after reference append is denied and never repaired or
	// blessed by a subsequent read; RB1 keeps the original immutable identity.
	blob := filepath.Join(stateRoot, "runs", integrationID, taskDeliveryFileName(delivery))
	if err := os.WriteFile(blob, []byte("drift"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := session.ReadTaskArtifact(ctx, draft.ID); err == nil {
		t.Fatal("corrupted stored blob served")
	}
	after, found, err := session.ingress.ReadTaskDelivery(session.acquisition.Scope, draft.ID)
	if err != nil || !found || !reflect.DeepEqual(after, delivery) {
		t.Fatal("read changed reference", err)
	}
}

func mustDeliveryJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func acceptRepositoryDeliveryNode(t *testing.T, fixture publicFixedDeliveryInputs, session *RepositorySession, manager gitworktree.Repository, taskID, node, runID string, files map[string][]byte) {
	t.Helper()
	ctx := context.Background()
	starting := fixedDeliveryFixture{repository: fixture.repository, session: session, request: application.StartRunRequest{RunID: runID}}
	readyLease, err := session.runs.AcquireExisting(runID)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := session.runs.ReadRunStartAuthorityUnderLease(ctx, readyLease)
	if releaseErr := readyLease.Release(); err == nil {
		err = releaseErr
	}
	if err != nil {
		t.Fatal(err)
	}
	starting.request.ExpectedSequence, starting.request.ExpectedAuthorityHead = ready.Run.Sequence, ready.Run.AuthorityHead
	running := advanceFixedDeliveryRunToRunningWithBudget(t, starting, 1)
	advanceFixedDeliveryRunToVerifying(t, starting, running)
	lease, err := session.runs.AcquireExisting(runID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	state, err := runstore.InspectUnderLease(lease)
	if err != nil {
		t.Fatal(err)
	}
	rawTask, err := runstore.ReadFileUnderLease(lease, 2<<20, "task-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(rawTask, &task); err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(filepath.Join(fixture.repository, ".marshal"), "delivery-"+node, state.BaseSHA)
	if err != nil {
		t.Fatal(err)
	}
	defer worktree.Release()
	for _, path := range planning.OrderQuotePaths(node) {
		if err := os.WriteFile(filepath.Join(worktree.Path, path), files[path], 0644); err != nil {
			t.Fatal(err)
		}
	}
	runDirectory := filepath.Join(fixture.repository, ".marshal", "runs", runID)
	worker, err := os.ReadFile(filepath.Join("..", "..", "schemas", "examples", "happy-path", "worker-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(worker, &result); err != nil {
		t.Fatal(err)
	}
	result["taskId"], result["runId"], result["attemptId"] = state.TaskID, state.RunID, state.CurrentAttemptID
	resultPath := filepath.Join(runDirectory, "attempts", state.CurrentAttemptID, "worker-result.json")
	if err := os.MkdirAll(filepath.Dir(resultPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, mustDeliveryJSON(t, result), 0600); err != nil {
		t.Fatal(err)
	}
	scope, deliverables, commands := verification.PolicyFromTask(task)
	namespace, err := session.acquisition.Scope.AuthorityNamespaceID.Digest()
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verification.New().Verify(ctx, verification.Input{TaskID: state.TaskID, RunID: state.RunID, AttemptID: state.CurrentAttemptID, AuthorityNamespaceID: namespace,
		SpecDigest: state.SpecDigest, BaseSHA: state.BaseSHA, Worktree: worktree.Path, ExpectedCommonDir: manager.CommonDir, RunDirectory: runDirectory,
		Scope: scope, Deliverables: deliverables, Commands: commands, ToolAllowlist: verification.ToolAllowlistFromTask(task), PatchCaptureBytes: 1 << 20})
	if err != nil || verified.Report.Status != "pass" {
		t.Fatalf("%s original verifier %v: %+v", node, err, verified.Report.Gates)
	}
	reportData, manifestData := mustDeliveryJSON(t, verified.Report), mustDeliveryJSON(t, verified.Manifest)
	reportDigest, _ := canonical.DigestJSON(reportData)
	manifestDigest, _ := canonical.DigestJSON(manifestData)
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "verify-" + node, RunID: runID, AttemptID: state.CurrentAttemptID, Sequence: state.Sequence + 1,
		Type: "verification.completed", StateFrom: domain.StateVerifying, StateTo: domain.StateReviewPending, Timestamp: verified.Report.CompletedAt, Actor: &domain.Actor{Type: "system", ID: "marshal-verifier"},
		Payload: map[string]any{"reportDigest": reportDigest, "artifactManifestDigest": manifestDigest, "status": "pass"}}
	next, err := lifecycle.Reduce(state, event, lifecycle.Guard{LeaseHeld: true, EvidenceCurrent: true, ReportComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.runs.Append(lease, event, state.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := session.runs.WriteSnapshot(lease, next); err != nil {
		t.Fatal(err)
	}
	state = next
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	packet, _, err := (&review.PacketBuilder{RunDirectory: runDirectory, Validator: validator}).Build(review.PacketBuildInput{Task: task, TaskData: rawTask, Report: verified.Report, ReportData: reportData,
		Manifest: verified.Manifest, ManifestData: manifestData, TaskID: state.TaskID, RunID: runID, SpecDigest: state.SpecDigest, BaseSHA: state.BaseSHA, ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed})
	if err != nil {
		t.Fatal(err)
	}
	rawPacket, err := os.ReadFile(filepath.Join(runDirectory, "review-packet.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = session.WithCurrentTaskObjective(ctx, runID, rawTask, func(policy *review.ObjectivePolicy) error {
		policy.VerificationDigest, policy.ArtifactManifestDigest = reportDigest, manifestDigest
		policy.ReadEvidence = func(limit int64, parts ...string) ([]byte, error) {
			return runstore.ReadFileUnderLease(lease, limit, parts...)
		}
		input := review.DecisionInput{Task: task, TaskID: state.TaskID, RunID: runID, SpecDigest: state.SpecDigest, ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed, Report: verified.Report, Manifest: verified.Manifest, Objective: policy}
		now := time.Now().UTC()
		decision, err := review.BuildObjectiveDecision(input, *packet, rawPacket, now)
		if err != nil {
			return err
		}
		imported, err := (&review.DecisionImporter{RunDirectory: runDirectory, Validator: validator}).ImportBytes(input, decision)
		if err != nil {
			return err
		}
		event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "accept-" + node, RunID: runID, AttemptID: state.CurrentAttemptID, Sequence: state.Sequence + 1,
			Type: "review.accept", StateFrom: domain.StateReviewPending, StateTo: domain.StateAccepted, Timestamp: now, Actor: &domain.Actor{Type: "system", ID: "marshal-review"},
			Payload: map[string]any{"verdict": "accept", "decisionDigest": imported.DecisionDigest, "evidenceDigest": imported.Decision.EvidenceDigest}}
		next, err := lifecycle.Reduce(state, event, lifecycle.Guard{LeaseHeld: true, EvidenceCurrent: true, RequiredGatesPass: true, DecisionCurrent: true, BudgetAvailable: true})
		if err != nil {
			return err
		}
		records, err := review.PrepareRecords(runDirectory, imported, review.TerminalOutcome(state.TaskID, runID, domain.StateAccepted, imported, now))
		if err != nil {
			return err
		}
		if err := session.runs.Append(lease, event, state.Sequence); err != nil {
			records.Abort()
			return err
		}
		if err := records.Commit(); err != nil {
			return err
		}
		return session.runs.WriteSnapshot(lease, next)
	})
	if err != nil {
		t.Fatalf("accept %s for %s: %v", node, taskID, err)
	}
}

func repositoryDeliveryBusinessFiles(t *testing.T) map[string][]byte {
	t.Helper()
	api := []byte(`import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from socketserver import TCPServer
def create_server(host, port):
    class Server(HTTPServer):
        def server_bind(self):
            TCPServer.server_bind(self)
            self.server_name = 'localhost'
            self.server_port = self.server_address[1]
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args): pass
        def do_POST(self):
            try:
                value = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            except ValueError:
                status, result = 400, {'error': 'invalid-json'}
            else:
                try:
                    if type(value) is not dict or set(value) != {'items'}: raise ValueError()
                    items = value['items']
                    if type(items) is not list or not items: raise ValueError()
                    subtotal = 0
                    for item in items:
                        if type(item) is not dict or set(item) != {'unit_price_cents', 'quantity'}: raise ValueError()
                        price, count = item['unit_price_cents'], item['quantity']
                        if type(price) is not int or price < 0 or type(count) is not int or count <= 0: raise ValueError()
                        subtotal += price * count
                    shipping = 0 if subtotal >= 5000 else 500
                    status, result = 200, {'subtotal_cents': subtotal, 'shipping_cents': shipping, 'total_cents': subtotal+shipping}
                except ValueError:
                    status, result = 422, {'error': 'invalid-order'}
            if self.path != '/quote': status, result = 404, {'error': 'not-found'}
            data = json.dumps(result).encode()
            self.send_response(status)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)
    return Server((host, port), Handler)
`)
	client := []byte(`import json, http.client
from urllib.parse import urlsplit
def quote_order(url, items):
    endpoint = urlsplit(url)
    if endpoint.scheme != 'http' or endpoint.hostname != '127.0.0.1' or not endpoint.port or endpoint.username is not None or endpoint.password is not None or endpoint.path or endpoint.query or endpoint.fragment: raise ValueError()
    connection = http.client.HTTPConnection('127.0.0.1', endpoint.port, timeout=2)
    try:
        connection.request('POST', '/quote', json.dumps({'items': items}), {'Content-Type': 'application/json'})
        response = connection.getresponse()
        result = json.loads(response.read(65537))
        if response.status != 200 or type(result) is not dict or set(result) != {'subtotal_cents', 'shipping_cents', 'total_cents'} or any(type(v) is not int for v in result.values()): raise ValueError()
        return result
    finally:
        connection.close()
`)
	delivery := map[string]any{"version": "order-quote-delivery/v1", "apiSha256": strings.TrimPrefix(canonical.DigestBytes(api), "sha256:"), "clientSha256": strings.TrimPrefix(canonical.DigestBytes(client), "sha256:"),
		"apiEntryPoint": "quote_api.create_server", "clientEntryPoint": "quote_client.quote_order", "sampleItems": []any{map[string]any{"unit_price_cents": 1200, "quantity": 2}},
		"sampleQuote": map[string]any{"subtotal_cents": 2400, "shipping_cents": 500, "total_cents": 2900}}
	return map[string][]byte{"quote_api.py": api, "quote_client.py": client, "quote_delivery.json": mustDeliveryJSON(t, delivery)}
}
