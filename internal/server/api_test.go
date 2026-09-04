package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/execution"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

const (
	fixtureAdapterID = "fake"
	fixtureTaskID    = "task-server-fixture"
	fixtureRunID     = "run-server-fixture"
	// fixtureDeadline is one hour after the frozen fixture clock.
	fixtureDeadline = "2026-08-13T13:00:00Z"
)

// fixtureClock is the frozen clock of every server fixture.
var fixtureClock = time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

// fixtureAdapter is the in-process Worker adapter of the server fixtures:
// Probe returns the frozen capability; Run is never executed by the Public
// API endpoints and fails closed if it ever were.
type fixtureAdapter struct {
	id         string
	capability []byte
	run        func(context.Context, domain.Record) (domain.Record, error)
}

type fixtureApplicationPort struct {
	projection   application.RunProjection
	prepared     application.PreparedRunStart
	successor    application.RunProjection
	inspectCalls int
	prepareCalls int
	startCalls   int
}

func (port *fixtureApplicationPort) Status(context.Context, application.StatusRequest) (application.StatusProjection, error) {
	return application.StatusProjection{}, application.NewError("status", application.ReasonCompositionIncomplete)
}

func (port *fixtureApplicationPort) InspectRun(context.Context, application.InspectRunRequest) (application.RunProjection, error) {
	port.inspectCalls++
	return port.projection, nil
}

func (port *fixtureApplicationPort) PrepareRunStart(_ context.Context, request application.PrepareRunStartRequest) (application.PreparedRunStart, error) {
	port.prepareCalls++
	if request.RunID != port.projection.RunID || request.ExpectedSequence != port.projection.Sequence ||
		request.ExpectedAuthorityHead != port.projection.AuthorityHead {
		return application.PreparedRunStart{}, application.NewError("prepare-run-start", application.ReasonAuthorityConflict)
	}
	return port.prepared, nil
}

func (port *fixtureApplicationPort) StartPreparedRun(_ context.Context, prepared application.PreparedRunStart) (application.RunProjection, error) {
	port.startCalls++
	if prepared != port.prepared {
		return application.RunProjection{}, application.NewError("start-prepared-run", application.ReasonAuthorityConflict)
	}
	port.projection = port.successor
	return port.successor, nil
}

func (port *fixtureApplicationPort) StartRun(_ context.Context, request application.StartRunRequest) (application.RunStartProjection, error) {
	port.startCalls++
	if request.RunID != port.prepared.RunID || request.ExpectedSequence != port.prepared.Sequence ||
		request.ExpectedAuthorityHead != port.prepared.AuthorityHead {
		return application.RunStartProjection{}, application.NewError("start-run", application.ReasonAuthorityConflict)
	}
	if port.projection == port.successor {
		return application.RunStartProjection{Prepared: port.prepared, Run: port.successor}, nil
	}
	if port.projection.TaskID != port.prepared.TaskID || port.projection.RunID != port.prepared.RunID ||
		port.projection.Sequence != port.prepared.Sequence || port.projection.AuthorityHead != port.prepared.AuthorityHead {
		return application.RunStartProjection{}, application.NewError("start-run", application.ReasonAuthorityConflict)
	}
	port.prepareCalls++
	port.projection = port.successor
	return application.RunStartProjection{Prepared: port.prepared, Run: port.successor}, nil
}

func (port *fixtureApplicationPort) ReconcileStartRun(_ context.Context, request application.StartRunRequest) (application.RunStartProjection, bool, error) {
	if request.RunID != port.prepared.RunID || request.ExpectedSequence != port.prepared.Sequence || request.ExpectedAuthorityHead != port.prepared.AuthorityHead {
		return application.RunStartProjection{}, false, application.NewError("reconcile-start-run", application.ReasonAuthorityConflict)
	}
	if port.projection != port.successor {
		return application.RunStartProjection{}, false, nil
	}
	return application.RunStartProjection{Prepared: port.prepared, Run: port.successor}, true, nil
}

func (port *fixtureApplicationPort) CollectRunResult(context.Context, application.CollectRunResultRequest) (application.CollectedRunProjection, error) {
	return application.CollectedRunProjection{}, application.NewError("collect-run-result", application.ReasonCompositionIncomplete)
}

func (port *fixtureApplicationPort) VerifyRun(context.Context, application.VerifyRunRequest) (application.VerificationProjection, error) {
	return application.VerificationProjection{}, application.NewError("verify-run", application.ReasonCompositionIncomplete)
}

func (port *fixtureApplicationPort) BuildReviewPacket(context.Context, application.BuildReviewPacketRequest) (application.ReviewPacketProjection, error) {
	return application.ReviewPacketProjection{}, application.NewError("build-review-packet", application.ReasonCompositionIncomplete)
}

func (port *fixtureApplicationPort) ApplyReviewDecision(context.Context, application.ApplyReviewDecisionRequest) (application.ReviewDecisionProjection, error) {
	return application.ReviewDecisionProjection{}, application.NewError("apply-review-decision", application.ReasonCompositionIncomplete)
}

func readyFixtureApplicationPort() *fixtureApplicationPort {
	digest := func(character string) string { return "sha256:" + strings.Repeat(character, 64) }
	ready := application.RunProjection{
		TaskID: fixtureTaskID, RunID: fixtureRunID, State: domain.StateReady,
		Sequence: 3, AuthorityHead: digest("1"),
	}
	prepared := application.PreparedRunStart{
		ProtocolRevision: application.PreparedRunStartProtocolRevision,
		TaskID:           fixtureTaskID, RunID: fixtureRunID, AttemptID: "attempt-server-port-1",
		ReservationFactDigest: digest("2"), AttemptOpenedFactDigest: digest("3"),
		AttemptOrdinal: 1, AttemptsUsedBefore: 0, MaxAttempts: 3,
		State: domain.StateReady, Sequence: ready.Sequence, AuthorityHead: ready.AuthorityHead,
		PreparationDigest: digest("4"),
	}
	return &fixtureApplicationPort{
		projection: ready,
		prepared:   prepared,
		successor: application.RunProjection{
			TaskID: fixtureTaskID, RunID: fixtureRunID, AttemptID: prepared.AttemptID,
			State: domain.StateRunning, Sequence: ready.Sequence + 1, AuthorityHead: digest("5"),
		},
	}
}

type legacyFixtureApplicationPort struct {
	stateRoot string
	execute   func(context.Context, string) error
}

func (port *legacyFixtureApplicationPort) Status(context.Context, application.StatusRequest) (application.StatusProjection, error) {
	return application.StatusProjection{}, application.NewError("status", application.ReasonCompositionIncomplete)
}

func (port *legacyFixtureApplicationPort) InspectRun(_ context.Context, request application.InspectRunRequest) (application.RunProjection, error) {
	state, err := runstore.New(port.stateRoot).Inspect(request.RunID)
	if err != nil {
		return application.RunProjection{}, err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return application.RunProjection{}, err
	}
	return application.RunProjection{TaskID: state.TaskID, RunID: state.RunID, AttemptID: state.CurrentAttemptID,
		State: state.State, Sequence: state.Sequence, AuthorityHead: canonical.DigestBytes(data)}, nil
}

func (port *legacyFixtureApplicationPort) PrepareRunStart(ctx context.Context, request application.PrepareRunStartRequest) (application.PreparedRunStart, error) {
	projection, err := port.InspectRun(ctx, application.InspectRunRequest{RunID: request.RunID})
	if err != nil || projection.Sequence != request.ExpectedSequence || projection.AuthorityHead != request.ExpectedAuthorityHead {
		return application.PreparedRunStart{}, application.NewError("prepare-run-start", application.ReasonAuthorityConflict)
	}
	digest := "sha256:" + strings.Repeat("6", 64)
	return application.PreparedRunStart{ProtocolRevision: application.PreparedRunStartProtocolRevision,
		TaskID: projection.TaskID, RunID: projection.RunID, AttemptID: "attempt-legacy-fixture",
		ReservationFactDigest: digest, AttemptOpenedFactDigest: digest, AttemptOrdinal: 1, MaxAttempts: 3,
		State: domain.StateReady, Sequence: projection.Sequence, AuthorityHead: projection.AuthorityHead,
		PreparationDigest: digest}, nil
}

func (port *legacyFixtureApplicationPort) StartPreparedRun(ctx context.Context, prepared application.PreparedRunStart) (application.RunProjection, error) {
	err := port.execute(ctx, prepared.RunID)
	projection, inspectErr := port.InspectRun(ctx, application.InspectRunRequest{RunID: prepared.RunID})
	if inspectErr != nil {
		return application.RunProjection{}, inspectErr
	}
	return projection, err
}

func (port *legacyFixtureApplicationPort) StartRun(ctx context.Context, request application.StartRunRequest) (application.RunStartProjection, error) {
	prepared, err := port.PrepareRunStart(ctx, application.PrepareRunStartRequest(request))
	if err != nil {
		return application.RunStartProjection{}, err
	}
	after, err := port.StartPreparedRun(ctx, prepared)
	if err != nil {
		return application.RunStartProjection{}, err
	}
	// This compatibility fixture emulates the legacy executor, whose AttemptID
	// is minted by execution.Run rather than by the fixture preparation.
	prepared.AttemptID = after.AttemptID
	return application.RunStartProjection{Prepared: prepared, Run: after}, nil
}

func (port *legacyFixtureApplicationPort) ReconcileStartRun(context.Context, application.StartRunRequest) (application.RunStartProjection, bool, error) {
	return application.RunStartProjection{}, false, nil
}

func (port *legacyFixtureApplicationPort) CollectRunResult(context.Context, application.CollectRunResultRequest) (application.CollectedRunProjection, error) {
	return application.CollectedRunProjection{}, application.NewError("collect-run-result", application.ReasonCompositionIncomplete)
}

func (port *legacyFixtureApplicationPort) VerifyRun(context.Context, application.VerifyRunRequest) (application.VerificationProjection, error) {
	return application.VerificationProjection{}, application.NewError("verify-run", application.ReasonCompositionIncomplete)
}

func (port *legacyFixtureApplicationPort) BuildReviewPacket(context.Context, application.BuildReviewPacketRequest) (application.ReviewPacketProjection, error) {
	return application.ReviewPacketProjection{}, application.NewError("build-review-packet", application.ReasonCompositionIncomplete)
}

func (port *legacyFixtureApplicationPort) ApplyReviewDecision(context.Context, application.ApplyReviewDecisionRequest) (application.ReviewDecisionProjection, error) {
	return application.ReviewDecisionProjection{}, application.NewError("apply-review-decision", application.ReasonCompositionIncomplete)
}

func (a *fixtureAdapter) ID() string { return a.id }

func (a *fixtureAdapter) Probe(ctx context.Context) (domain.Record, error) {
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: a.capability}, nil
}

func (a *fixtureAdapter) Run(ctx context.Context, request domain.Record) (domain.Record, error) {
	if a.run != nil {
		return a.run(ctx, request)
	}
	return domain.Record{}, fmt.Errorf("fixture adapter: the public API must never execute a Worker")
}

// fixtureRepository builds the hermetic git repository every server fixture
// binds and returns its canonical root.
func fixtureRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	git("config", "user.name", "Marshal Server Fixture")
	git("config", "user.email", "server-fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("server fixture base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-q", "-m", "base")
	git("remote", "add", "origin", "https://example.invalid/server-fixture.git")
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("canonicalize the fixture repository root: %v", err)
	}
	return canonicalRoot
}

// fixtureBaseSHA returns the HEAD commit of the fixture repository.
func fixtureBaseSHA(t *testing.T, root string) string {
	t.Helper()
	command := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	command.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("resolve fixture HEAD: %v", err)
	}
	return strings.TrimSpace(string(output))
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fixtureCapability renders the frozen CapabilitySnapshot of the fixture
// adapter.
func fixtureCapability(adapterID string) []byte {
	return mustMarshalValue(map[string]any{
		"apiVersion":       "marshal.dev/v1alpha1",
		"kind":             "CapabilitySnapshot",
		"adapterId":        adapterID,
		"adapterVersion":   "0.1.0",
		"executable":       "/fixture/adapter",
		"executableDigest": "sha256:" + strings.Repeat("a", 64),
		"binaryVersion":    "1",
		"probeStatus":      "supported",
		"capabilities": map[string]any{
			"structuredOutput":        []string{"jsonl"},
			"nonInteractiveEdit":      true,
			"sessionPolicies":         []string{"ephemeral"},
			"modelSelection":          false,
			"executionProfiles":       []string{"workspace-write"},
			"nativeBudgets":           []string{},
			"processTreeCancellation": true,
			"notes":                   []string{},
		},
		"probeErrors": []string{},
		"probedAt":    "2026-08-13T11:00:00Z",
	})
}

// mustMarshalValue marshals without a testing handle so package-level
// fixture builders can share it.
func mustMarshalValue(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

// fixtureTask renders one schema-valid TaskSpec bound to the fixture
// repository, task, adapter and base SHA.
func fixtureTask(repositoryRoot, taskID, adapterID, baseSHA string) map[string]any {
	return map[string]any{
		"apiVersion":   "marshal.dev/v1alpha1",
		"kind":         "Task",
		"metadata":     map[string]any{"id": taskID, "title": "server fixture"},
		"repository":   map[string]any{"path": repositoryRoot, "baseRef": baseSHA, "remote": "origin"},
		"work":         map[string]any{"objective": "server fixture objective", "constraints": []string{}, "nonGoals": []string{}},
		"scope":        map[string]any{"allowPaths": []string{"change.txt"}, "denyPaths": []string{}, "allowSubmodules": false, "maxChangedFiles": 4, "maxDiffBytes": 10000},
		"acceptance":   map[string]any{"commands": []any{}, "allowNoChange": false},
		"deliverables": []any{map[string]any{"id": "code", "kind": "code", "required": true, "pathGlob": "change.txt"}},
		"worker":       map[string]any{"preferredAdapter": adapterID, "fallbackAdapters": []string{}, "executionProfile": "workspace-write", "sessionPolicy": "ephemeral"},
		"budgets":      map[string]any{"runTimeoutSeconds": 600, "attemptTimeoutSeconds": 300, "maxAttempts": 3, "maxOperationalRetries": 1, "maxReworkRounds": 1, "maxOutputBytes": 1000000},
		"publication":  map[string]any{"required": false, "provider": "none", "mode": "none", "remote": "origin", "baseBranch": "main", "mergePolicy": "never", "requiredChecks": []string{}},
	}
}

// fixturePolicy renders one schema-valid PolicySnapshot for the fixture task
// and run with the plan and publish approval gates required.
func fixturePolicy(taskID, runID, adapterID string) map[string]any {
	return map[string]any{
		"apiVersion": "marshal.dev/v1alpha1",
		"kind":       "PolicySnapshot",
		"taskId":     taskID,
		"runId":      runID,
		"sources":    []any{map[string]any{"scope": "builtin", "digest": "sha256:" + strings.Repeat("b", 64), "required": true}},
		"effective": map[string]any{
			"minimumExecutionProfile":      "workspace-write",
			"requireEnforcedNetworkPolicy": false,
			"networkPolicy":                "unenforced",
			"allowFallbackWorkers":         false,
			"allowWorkerSubagents":         false,
			"allowPublication":             false,
			"allowMerge":                   false,
			"allowGateWaivers":             false,
			"allowedAdapters":              []string{adapterID},
			"environmentAllowlist":         []string{"PATH"},
			"retentionDays":                1,
		},
		"policyDigest": "",
		"generatedAt":  "2026-08-13T11:00:00Z",
	}
}

// sealPolicy computes the detached policyDigest exactly as planning
// recomputes it: blank the field, canonicalize, digest.
func sealPolicy(t *testing.T, policy map[string]any) []byte {
	t.Helper()
	policy["policyDigest"] = ""
	canonicalized, err := canonical.JSON(mustMarshal(t, policy))
	if err != nil {
		t.Fatal(err)
	}
	policy["policyDigest"] = canonical.DigestBytes(canonicalized)
	return mustMarshal(t, policy)
}

// serverFixture is the shared hermetic assembly of the server tests: one
// bound fixture repository, one fake Worker adapter and one Server assembled
// over the frozen clock.
type serverFixture struct {
	t              *testing.T
	server         *Server
	adapter        *fixtureAdapter
	selector       *adapter.Selector
	repositoryRoot string
	stateRoot      string
	baseSHA        string
	scope          string
}

func newServerFixture(t *testing.T) *serverFixture {
	t.Helper()
	root := fixtureRepository(t)
	stateRoot := filepath.Join(root, ".marshal")
	if err := (repository.State{RepositoryRoot: root, StateRoot: stateRoot}).Init(); err != nil {
		t.Fatalf("bind the fixture repository identity: %v", err)
	}
	registry := adapter.NewRegistry()
	worker := &fixtureAdapter{id: fixtureAdapterID, capability: fixtureCapability(fixtureAdapterID)}
	if err := registry.Register(worker); err != nil {
		t.Fatalf("register the fixture adapter: %v", err)
	}
	selector, err := adapter.NewSelector(registry)
	if err != nil {
		t.Fatalf("build the fixture selector: %v", err)
	}
	server, err := New(Config{
		StateRoot:      stateRoot,
		RepositoryRoot: root,
		Selector:       selector,
		Now:            func() time.Time { return fixtureClock },
	})
	if err != nil {
		t.Fatalf("assemble the server: %v", err)
	}
	return &serverFixture{
		t:              t,
		server:         server,
		adapter:        worker,
		selector:       selector,
		repositoryRoot: root,
		stateRoot:      stateRoot,
		baseSHA:        fixtureBaseSHA(t, root),
		scope:          "repo:" + filepath.ToSlash(root),
	}
}

// identityHeaders renders the complete frozen identity envelope of one
// request.
func (f *serverFixture) identityHeaders(requestID string) map[string]string {
	return map[string]string{
		HeaderRequestID:       requestID,
		HeaderProtocolVersion: ProtocolFamily + "/" + ProtocolVersion,
		HeaderPrincipal:       "fixture-operator",
		HeaderAudience:        Audience,
		HeaderScope:           f.scope,
		HeaderDeadline:        fixtureDeadline,
	}
}

type recordedResponse struct {
	status int
	header http.Header
	body   []byte
}

// do executes one request against the fixture server in-process.
func (f *serverFixture) do(method, path string, headers map[string]string, body []byte) recordedResponse {
	f.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	f.server.ServeHTTP(recorder, request)
	return recordedResponse{status: recorder.Code, header: recorder.Result().Header, body: recorder.Body.Bytes()}
}

func (r recordedResponse) decodeError(t *testing.T) ErrorBody {
	t.Helper()
	var body ErrorBody
	if err := json.Unmarshal(r.body, &body); err != nil {
		t.Fatalf("decode error body %q: %v", r.body, err)
	}
	if body.APIVersion != domain.APIVersionV1Alpha1 || body.Kind != "Error" {
		t.Fatalf("error envelope = %+v", body)
	}
	return body
}

// mutationBody builds one idempotent submission body: the payload plus its
// canonical requestDigest.
func mutationBody(t *testing.T, key string, payload any) []byte {
	t.Helper()
	payloadRaw := mustMarshal(t, payload)
	digest, err := canonical.DigestJSON(payloadRaw)
	if err != nil {
		t.Fatal(err)
	}
	return mustMarshal(t, map[string]any{
		"idempotencyKey": key,
		"requestDigest":  digest,
		"payload":        json.RawMessage(payloadRaw),
	})
}

// mutationBodyWithDigest builds one submission body with an explicit
// requestDigest, so conflicts and mismatches can be exercised.
func mutationBodyWithDigest(t *testing.T, key, digest string, payload any) []byte {
	t.Helper()
	return mustMarshal(t, map[string]any{
		"idempotencyKey": key,
		"requestDigest":  digest,
		"payload":        json.RawMessage(mustMarshal(t, payload)),
	})
}

func TestUnknownRouteFailsClosed(t *testing.T) {
	fixture := newServerFixture(t)
	for _, target := range []string{"/", "/other", APIPrefix, APIPrefix + "/", APIPrefix + "/nope", APIPrefix + "/tasks/x/y/z"} {
		response := fixture.do(http.MethodGet, target, fixture.identityHeaders("req-route"), nil)
		if response.status != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", target, response.status)
		}
		body := response.decodeError(t)
		if body.Code != CodeNotFound || body.Reason != "unknown-route" {
			t.Fatalf("GET %s error = %+v", target, body)
		}
	}
}

func TestMethodNotAllowedFailsClosed(t *testing.T) {
	fixture := newServerFixture(t)
	response := fixture.do(http.MethodGet, APIPrefix+"/tasks", fixture.identityHeaders("req-method"), nil)
	if response.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET /tasks status = %d, want 405", response.status)
	}
	if allow := response.header.Get("Allow"); allow != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", allow)
	}
	body := response.decodeError(t)
	if body.Code != CodeInvalidRequest || body.Reason != "method-not-allowed" {
		t.Fatalf("error = %+v", body)
	}

	response = fixture.do(http.MethodPost, APIPrefix+"/runs/run-1/status",
		withContentType(fixture.identityHeaders("req-method-2")), []byte("{}"))
	if response.status != http.StatusMethodNotAllowed {
		t.Fatalf("POST /runs/run-1/status status = %d, want 405", response.status)
	}
}

func withContentType(headers map[string]string) map[string]string {
	headers["Content-Type"] = "application/json"
	return headers
}

func TestMissingIdentityHeadersFailClosed(t *testing.T) {
	fixture := newServerFixture(t)
	for _, missing := range []string{HeaderRequestID, HeaderProtocolVersion, HeaderPrincipal, HeaderAudience, HeaderScope, HeaderDeadline} {
		headers := fixture.identityHeaders("req-missing")
		delete(headers, missing)
		response := fixture.do(http.MethodGet, APIPrefix+"/runs/run-1/status", headers, nil)
		if response.status != http.StatusBadRequest {
			t.Fatalf("missing %s status = %d, want 400", missing, response.status)
		}
		body := response.decodeError(t)
		if body.Code != CodeMissingIdentity || body.Reason != "missing-header:"+missing {
			t.Fatalf("missing %s error = %+v", missing, body)
		}
	}
}

func TestIdentityEnvelopeValidatedFailClosed(t *testing.T) {
	fixture := newServerFixture(t)
	cases := []struct {
		name   string
		mutate func(map[string]string)
		code   ErrorCode
		reason string
		status int
	}{
		{"protocol version mismatch", func(h map[string]string) { h[HeaderProtocolVersion] = "marshal-public-api/v9" }, CodeInvalidRequest, "protocol-version-mismatch", http.StatusBadRequest},
		{"audience mismatch", func(h map[string]string) { h[HeaderAudience] = "other-audience" }, CodeInvalidRequest, "audience-mismatch", http.StatusBadRequest},
		{"scope mismatch", func(h map[string]string) { h[HeaderScope] = "repo:/other/repository" }, CodeScopeMismatch, "scope-mismatch", http.StatusBadRequest},
		{"deadline invalid", func(h map[string]string) { h[HeaderDeadline] = "not-a-timestamp" }, CodeInvalidRequest, "deadline-invalid", http.StatusBadRequest},
		{"deadline exceeded", func(h map[string]string) { h[HeaderDeadline] = "2026-08-13T11:00:00Z" }, CodeInvalidRequest, "deadline-exceeded", http.StatusBadRequest},
		{"header too long", func(h map[string]string) { h[HeaderPrincipal] = strings.Repeat("p", 513) }, CodeInvalidRequest, "header-too-long:" + HeaderPrincipal, http.StatusBadRequest},
	}
	for _, testCase := range cases {
		headers := fixture.identityHeaders("req-validated")
		testCase.mutate(headers)
		response := fixture.do(http.MethodGet, APIPrefix+"/runs/run-1/status", headers, nil)
		if response.status != testCase.status {
			t.Fatalf("%s status = %d, want %d", testCase.name, response.status, testCase.status)
		}
		body := response.decodeError(t)
		if body.Code != testCase.code || body.Reason != testCase.reason {
			t.Fatalf("%s error = %+v, want %s/%s", testCase.name, body, testCase.code, testCase.reason)
		}
	}
}

func TestForbiddenHeadersFailClosed(t *testing.T) {
	fixture := newServerFixture(t)
	for _, header := range forbiddenHeaders {
		headers := fixture.identityHeaders("req-forbidden-header")
		headers[header] = "anything"
		response := fixture.do(http.MethodGet, APIPrefix+"/runs/run-1/status", headers, nil)
		if response.status != http.StatusForbidden {
			t.Fatalf("header %s status = %d, want 403", header, response.status)
		}
		body := response.decodeError(t)
		if body.Code != CodeForbiddenIdentity || body.Reason != "forbidden-header:"+header {
			t.Fatalf("header %s error = %+v", header, body)
		}
	}
}

func TestForbiddenQueryFieldsFailClosed(t *testing.T) {
	fixture := newServerFixture(t)
	for _, field := range []string{"providerType", "workloadRole", "allocationId", "generation", "fencingToken", "dispatchLease", "leaseId"} {
		response := fixture.do(http.MethodGet, APIPrefix+"/runs/run-1/status?"+field+"=1", fixture.identityHeaders("req-forbidden-query"), nil)
		if response.status != http.StatusForbidden {
			t.Fatalf("query %s status = %d, want 403", field, response.status)
		}
		body := response.decodeError(t)
		if body.Code != CodeForbiddenIdentity || body.Reason != "forbidden-query:"+field {
			t.Fatalf("query %s error = %+v", field, body)
		}
	}
}

func TestForbiddenBodyFieldsFailClosed(t *testing.T) {
	fixture := newServerFixture(t)
	for _, field := range []string{"providerType", "workloadRole", "allocationId", "generation", "fencingToken", "dispatchLease", "leaseId"} {
		top := mustMarshal(t, map[string]any{field: "1", "idempotencyKey": "k", "requestDigest": testDigest("x"), "payload": map[string]any{}})
		response := fixture.do(http.MethodPost, APIPrefix+"/tasks", withContentType(fixture.identityHeaders("req-forbidden-body")), top)
		if response.status != http.StatusForbidden {
			t.Fatalf("body field %s status = %d, want 403", field, response.status)
		}
		body := response.decodeError(t)
		if body.Code != CodeForbiddenIdentity || body.Reason != "forbidden-field:"+field {
			t.Fatalf("body field %s error = %+v", field, body)
		}

		nested := mustMarshal(t, map[string]any{
			"idempotencyKey": "k",
			"requestDigest":  testDigest("x"),
			"payload":        map[string]any{"taskSpec": map[string]any{field: "1"}},
		})
		response = fixture.do(http.MethodPost, APIPrefix+"/tasks", withContentType(fixture.identityHeaders("req-forbidden-nested")), nested)
		if response.status != http.StatusForbidden {
			t.Fatalf("nested field %s status = %d, want 403", field, response.status)
		}
		body = response.decodeError(t)
		if body.Code != CodeForbiddenIdentity || body.Reason != "forbidden-field:"+field {
			t.Fatalf("nested field %s error = %+v", field, body)
		}
	}
}

func TestMutationBodyEnvelopeValidated(t *testing.T) {
	fixture := newServerFixture(t)
	headers := withContentType(fixture.identityHeaders("req-envelope"))

	response := fixture.do(http.MethodPost, APIPrefix+"/tasks", fixture.identityHeaders("req-envelope-ct"), []byte("{}"))
	if response.status != http.StatusBadRequest {
		t.Fatalf("wrong content type status = %d, want 400", response.status)
	}
	if body := response.decodeError(t); body.Reason != "content-type-invalid" {
		t.Fatalf("wrong content type error = %+v", body)
	}

	response = fixture.do(http.MethodPost, APIPrefix+"/tasks", headers, nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("empty body status = %d, want 400", response.status)
	}
	if body := response.decodeError(t); body.Reason != "empty-body" {
		t.Fatalf("empty body error = %+v", body)
	}

	response = fixture.do(http.MethodPost, APIPrefix+"/tasks", headers, mustMarshal(t, map[string]any{
		"idempotencyKey": "k", "requestDigest": testDigest("x"), "payload": map[string]any{}, "extra": 1,
	}))
	if response.status != http.StatusBadRequest {
		t.Fatalf("unknown member status = %d, want 400", response.status)
	}
	if body := response.decodeError(t); body.Code != CodeInvalidRequest || body.Reason != "unknown-member:extra" {
		t.Fatalf("unknown member error = %+v", body)
	}

	response = fixture.do(http.MethodPost, APIPrefix+"/tasks", headers, mustMarshal(t, map[string]any{
		"requestDigest": testDigest("x"), "payload": map[string]any{},
	}))
	if response.status != http.StatusBadRequest {
		t.Fatalf("missing idempotencyKey status = %d, want 400", response.status)
	}
	if body := response.decodeError(t); body.Reason != "missing-member:idempotencyKey" {
		t.Fatalf("missing idempotencyKey error = %+v", body)
	}

	response = fixture.do(http.MethodPost, APIPrefix+"/tasks", headers, mustMarshal(t, map[string]any{
		"idempotencyKey": "k", "requestDigest": "md5:zzz", "payload": map[string]any{},
	}))
	if response.status != http.StatusBadRequest {
		t.Fatalf("invalid digest status = %d, want 400", response.status)
	}
	if body := response.decodeError(t); body.Reason != "request-digest-invalid" {
		t.Fatalf("invalid digest error = %+v", body)
	}

	response = fixture.do(http.MethodPost, APIPrefix+"/tasks", headers, mutationBodyWithDigest(t, "k", testDigest("other"), map[string]any{}))
	if response.status != http.StatusBadRequest {
		t.Fatalf("digest mismatch status = %d, want 400", response.status)
	}
	if body := response.decodeError(t); body.Reason != "request-digest-mismatch" {
		t.Fatalf("digest mismatch error = %+v", body)
	}
}

func TestTaskGetNotFound(t *testing.T) {
	fixture := newServerFixture(t)
	response := fixture.do(http.MethodGet, APIPrefix+"/tasks/task-absent", fixture.identityHeaders("req-task-get"), nil)
	if response.status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.status)
	}
	if body := response.decodeError(t); body.Code != CodeNotFound || body.Reason != "task-not-found" {
		t.Fatalf("error = %+v", body)
	}

	response = fixture.do(http.MethodGet, APIPrefix+"/tasks/-invalid", fixture.identityHeaders("req-task-get-invalid"), nil)
	if response.status != http.StatusBadRequest {
		t.Fatalf("invalid taskId status = %d, want 400", response.status)
	}
	if body := response.decodeError(t); body.Reason != "invalid-id" {
		t.Fatalf("invalid taskId error = %+v", body)
	}
}

func TestRunStatusNotFound(t *testing.T) {
	fixture := newServerFixture(t)
	response := fixture.do(http.MethodGet, APIPrefix+"/runs/run-absent/status", fixture.identityHeaders("req-status"), nil)
	if response.status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.status)
	}
	if body := response.decodeError(t); body.Code != CodeNotFound || body.Reason != "run-not-found" {
		t.Fatalf("error = %+v", body)
	}

	headers := fixture.identityHeaders("req-status-body")
	response = fixture.do(http.MethodGet, APIPrefix+"/runs/run-absent/status", headers, []byte("{}"))
	if response.status != http.StatusBadRequest {
		t.Fatalf("GET with body status = %d, want 400", response.status)
	}
	if body := response.decodeError(t); body.Reason != "body-not-allowed" {
		t.Fatalf("GET with body error = %+v", body)
	}
}

func TestTaskCreateInputRejections(t *testing.T) {
	fixture := newServerFixture(t)
	headers := withContentType(fixture.identityHeaders("req-create-invalid"))

	// Invalid runId.
	response := fixture.do(http.MethodPost, APIPrefix+"/tasks", headers, mutationBody(t, "key-create-1", map[string]any{
		"runId":          "-invalid",
		"taskSpec":       fixtureTask(fixture.repositoryRoot, fixtureTaskID, fixtureAdapterID, fixture.baseSHA),
		"policySnapshot": json.RawMessage(sealPolicy(t, fixturePolicy(fixtureTaskID, fixtureRunID, fixtureAdapterID))),
	}))
	if response.status != http.StatusBadRequest {
		t.Fatalf("invalid runId status = %d, want 400", response.status)
	}
	if body := response.decodeError(t); body.Reason != "invalid-id" {
		t.Fatalf("invalid runId error = %+v", body)
	}

	// Schema-invalid TaskSpec.
	response = fixture.do(http.MethodPost, APIPrefix+"/tasks", headers, mutationBody(t, "key-create-2", map[string]any{
		"runId":          fixtureRunID,
		"taskSpec":       map[string]any{"apiVersion": "marshal.dev/v1alpha1", "kind": "Task"},
		"policySnapshot": json.RawMessage(sealPolicy(t, fixturePolicy(fixtureTaskID, fixtureRunID, fixtureAdapterID))),
	}))
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid TaskSpec status = %d, want 422", response.status)
	}
	if body := response.decodeError(t); body.Code != CodeRejected || body.Reason != "task-spec-invalid" {
		t.Fatalf("invalid TaskSpec error = %+v", body)
	}

	// Schema-invalid PolicySnapshot.
	response = fixture.do(http.MethodPost, APIPrefix+"/tasks", headers, mutationBody(t, "key-create-3", map[string]any{
		"runId":          fixtureRunID,
		"taskSpec":       fixtureTask(fixture.repositoryRoot, fixtureTaskID, fixtureAdapterID, fixture.baseSHA),
		"policySnapshot": map[string]any{"apiVersion": "marshal.dev/v1alpha1", "kind": "PolicySnapshot"},
	}))
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid PolicySnapshot status = %d, want 422", response.status)
	}
	if body := response.decodeError(t); body.Code != CodeRejected || body.Reason != "policy-snapshot-invalid" {
		t.Fatalf("invalid PolicySnapshot error = %+v", body)
	}

	// Planning rejection: the PolicySnapshot runId does not match.
	mismatched := fixturePolicy(fixtureTaskID, "run-other", fixtureAdapterID)
	response = fixture.do(http.MethodPost, APIPrefix+"/tasks", headers, mutationBody(t, "key-create-4", map[string]any{
		"runId":          fixtureRunID,
		"taskSpec":       fixtureTask(fixture.repositoryRoot, fixtureTaskID, fixtureAdapterID, fixture.baseSHA),
		"policySnapshot": json.RawMessage(sealPolicy(t, mismatched)),
	}))
	if response.status != http.StatusUnprocessableEntity {
		t.Fatalf("planning rejection status = %d, want 422", response.status)
	}
	if body := response.decodeError(t); body.Code != CodeRejected || body.Reason != "planning-rejected" {
		t.Fatalf("planning rejection error = %+v", body)
	}
}

func TestDefaultServerSelectorRejectsUncomposedProductionAdaptersWithoutRun(t *testing.T) {
	root := fixtureRepository(t)
	stateRoot := filepath.Join(root, ".marshal")
	if err := (repository.State{RepositoryRoot: root, StateRoot: stateRoot}).Init(); err != nil {
		t.Fatalf("bind identity: %v", err)
	}
	writeVersionExecutable := func(name, version string) (string, string) {
		t.Helper()
		marker := filepath.Join(t.TempDir(), name+"-invoked")
		path := filepath.Join(t.TempDir(), name)
		script := fmt.Sprintf("#!/bin/sh\n: > %q\nprintf '%%s\\n' %q\n", marker, version)
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		return path, marker
	}
	qoderPath, qoderMarker := writeVersionExecutable("qodercli", "1.1.27")
	piPath, piMarker := writeVersionExecutable("pi", "0.84.4")
	env := map[string]string{
		"MARSHAL_QODER_PATH": qoderPath,
		"MARSHAL_QODER_MODE": "ordinary-user",
		"MARSHAL_PI_PATH":    piPath,
	}
	server, err := New(Config{
		StateRoot: stateRoot, RepositoryRoot: root, Selector: nil,
		Getenv: func(name string) string { return env[name] },
		Now:    func() time.Time { return fixtureClock },
	})
	if err != nil {
		t.Fatalf("assemble default server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	fixture := &serverFixture{
		t: t, server: server, repositoryRoot: root, stateRoot: stateRoot,
		baseSHA: fixtureBaseSHA(t, root), scope: "repo:" + filepath.ToSlash(root),
	}

	const rejectedTaskID, rejectedRunID = "task-qoder-production-rejected", "run-qoder-production-rejected"
	rejected := fixture.do(http.MethodPost, APIPrefix+"/tasks",
		withContentType(fixture.identityHeaders("req-qoder-production-rejected")),
		mutationBody(t, "key-qoder-production-rejected", map[string]any{
			"runId":          rejectedRunID,
			"taskSpec":       fixtureTask(root, rejectedTaskID, "qoder", fixture.baseSHA),
			"policySnapshot": json.RawMessage(sealPolicy(t, fixturePolicy(rejectedTaskID, rejectedRunID, "qoder"))),
		}))
	if rejected.status != http.StatusUnprocessableEntity {
		t.Fatalf("qoder production status=%d body=%s", rejected.status, rejected.body)
	}
	if body := rejected.decodeError(t); body.Code != CodeRejected || body.Reason != "planning-rejected" {
		t.Fatalf("qoder production error=%+v", body)
	}
	if _, err := runstore.New(stateRoot).Inspect(rejectedRunID); !os.IsNotExist(err) {
		t.Fatalf("rejected qoder left Run state: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(stateRoot, "runs", rejectedRunID)); !os.IsNotExist(err) {
		t.Fatalf("rejected qoder left Run directory: %v", err)
	}
	if _, err := os.Lstat(qoderMarker); !os.IsNotExist(err) {
		t.Fatalf("production selector probed non-LaunchCapable qoder: %v", err)
	}

	const piTaskID, piRunID = "task-pi-production", "run-pi-production"
	created := fixture.do(http.MethodPost, APIPrefix+"/tasks",
		withContentType(fixture.identityHeaders("req-pi-production")),
		mutationBody(t, "key-pi-production", map[string]any{
			"runId":          piRunID,
			"taskSpec":       fixtureTask(root, piTaskID, "pi", fixture.baseSHA),
			"policySnapshot": json.RawMessage(sealPolicy(t, fixturePolicy(piTaskID, piRunID, "pi"))),
		}))
	if created.status != http.StatusUnprocessableEntity {
		t.Fatalf("pi production status=%d body=%s", created.status, created.body)
	}
	if body := created.decodeError(t); body.Code != CodeRejected || body.Reason != "planning-rejected" {
		t.Fatalf("pi production error=%+v", body)
	}
	if _, err := runstore.New(stateRoot).Inspect(piRunID); !os.IsNotExist(err) {
		t.Fatalf("uncomposed Pi runtime left Run state: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(stateRoot, "runs", piRunID)); !os.IsNotExist(err) {
		t.Fatalf("uncomposed Pi runtime left Run directory: %v", err)
	}
	if _, err := os.Lstat(piMarker); !os.IsNotExist(err) {
		t.Fatalf("production selector probed Pi before supervisor composition: %v", err)
	}
}

func TestRunApprovalInputRejections(t *testing.T) {
	fixture := newServerFixture(t)
	headers := withContentType(fixture.identityHeaders("req-approve-invalid"))

	response := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/approval", headers, mutationBody(t, "key-approve-gate", map[string]any{
		"gate": "merge", "actor": "operator",
	}))
	if response.status != http.StatusBadRequest {
		t.Fatalf("invalid gate status = %d, want 400", response.status)
	}
	if body := response.decodeError(t); body.Reason != "invalid-gate" {
		t.Fatalf("invalid gate error = %+v", body)
	}

	response = fixture.do(http.MethodPost, APIPrefix+"/runs/run-absent/approval", headers, mutationBody(t, "key-approve-absent", map[string]any{
		"gate": domain.ApprovalGatePlan, "actor": "operator",
	}))
	if response.status != http.StatusNotFound {
		t.Fatalf("absent run approval status = %d, want 404", response.status)
	}
	if body := response.decodeError(t); body.Code != CodeNotFound || body.Reason != "run-not-found" {
		t.Fatalf("absent run approval error = %+v", body)
	}
}

func TestTaskCancelInputRejections(t *testing.T) {
	fixture := newServerFixture(t)
	headers := withContentType(fixture.identityHeaders("req-cancel-invalid"))

	response := fixture.do(http.MethodPost, APIPrefix+"/tasks/task-absent/cancel", headers, mutationBody(t, "key-cancel-absent", map[string]any{
		"actor": "operator", "reason": "not needed",
	}))
	if response.status != http.StatusNotFound {
		t.Fatalf("absent task cancel status = %d, want 404", response.status)
	}
	if body := response.decodeError(t); body.Code != CodeNotFound || body.Reason != "task-not-found" {
		t.Fatalf("absent task cancel error = %+v", body)
	}

	response = fixture.do(http.MethodPost, APIPrefix+"/tasks/task-absent/cancel", headers, mutationBody(t, "key-cancel-members", map[string]any{
		"actor": "operator",
	}))
	if response.status != http.StatusBadRequest {
		t.Fatalf("missing reason status = %d, want 400", response.status)
	}
	if body := response.decodeError(t); body.Reason != "missing-member:reason" {
		t.Fatalf("missing reason error = %+v", body)
	}
}

// TestNewFailsClosedWithoutIdentity proves server construction refuses an
// unbound or mismatched repository identity.
func TestNewFailsClosedWithoutIdentity(t *testing.T) {
	root := fixtureRepository(t)
	stateRoot := filepath.Join(root, ".marshal")

	// No identity record at all.
	if _, err := New(Config{StateRoot: stateRoot, RepositoryRoot: root, Selector: nil, Getenv: func(string) string { return "" }}); err == nil {
		t.Fatal("New accepted a state root without a repository identity record")
	}

	// Identity record bound to a different repository.
	if err := (repository.State{RepositoryRoot: root, StateRoot: stateRoot}).Init(); err != nil {
		t.Fatalf("bind identity: %v", err)
	}
	if _, err := New(Config{StateRoot: stateRoot, RepositoryRoot: t.TempDir(), Selector: nil, Getenv: func(string) string { return "" }}); err == nil {
		t.Fatal("New accepted a repository root that does not match the identity record")
	}
}

// TestServerBindsAuthorityNamespace proves the server derives the complete
// authority-side key space of ADR 0018 §10 from the bound repository
// identity: the local MVP triple tenantNamespace/controlPlaneId/
// authorityScopeId that owns every idempotent submission record.
func TestServerBindsAuthorityNamespace(t *testing.T) {
	fixture := newServerFixture(t)
	namespace := fixture.server.Namespace()
	expected := authority.AuthorityNamespaceId{
		TenantNamespace:  "local",
		ControlPlaneId:   "default",
		AuthorityScopeId: fixture.scope,
	}
	if !namespace.Equal(expected) {
		t.Fatalf("authority namespace = %+v, want %+v", namespace, expected)
	}
	if err := namespace.Validate(); err != nil {
		t.Fatalf("the derived authority namespace is invalid: %v", err)
	}
}

func TestServerAcceptsOnlyExactFixedCompositionNamespace(t *testing.T) {
	root := fixtureRepository(t)
	stateRoot := filepath.Join(root, ".marshal")
	if err := (repository.State{RepositoryRoot: root, StateRoot: stateRoot}).Init(); err != nil {
		t.Fatalf("bind identity: %v", err)
	}
	exact := authority.AuthorityNamespaceId{
		TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: root,
	}
	server, err := New(Config{
		StateRoot: stateRoot, RepositoryRoot: root, AuthorityNamespace: exact,
		DisableMutations: true,
	})
	if err != nil {
		t.Fatalf("New rejected exact fixed composition namespace: %v", err)
	}
	if !server.Namespace().Equal(exact) {
		t.Fatalf("server namespace = %+v, want %+v", server.Namespace(), exact)
	}

	for name, drift := range map[string]authority.AuthorityNamespaceId{
		"tenant":  {TenantNamespace: "other", ControlPlaneId: "default", AuthorityScopeId: root},
		"control": {TenantNamespace: "local", ControlPlaneId: "other", AuthorityScopeId: root},
		"scope":   {TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: "repo:" + root},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(Config{
				StateRoot: stateRoot, RepositoryRoot: root, AuthorityNamespace: drift,
				DisableMutations: true,
			}); err == nil {
				t.Fatal("New accepted drifted fixed composition namespace")
			}
		})
	}
}

// TestRunStartExecutesThroughCoreAndReplaysAcrossServerRestart proves the
// missing ADR 0052 server vertical slice without manufacturing a second state
// machine: the injected composition invokes execution.Run, Core journals the
// Attempt to VERIFYING, a fresh Server rebuilds status from disk, and replay
// of the same submission returns the durable result without a second Attempt.
func TestRunStartExecutesThroughCoreAndReplaysAcrossServerRestart(t *testing.T) {
	sealedMigrationSkip(t)
	fixture := newServerFixture(t)
	fixture.adapter.run = successfulServerWorker
	executions := 0
	runExecutor := func(ctx context.Context, runID string) error {
		executions++
		_, err := execution.Run(ctx, execution.Input{
			StateRoot:      fixture.stateRoot,
			RepositoryRoot: fixture.repositoryRoot,
			RunID:          runID,
			Adapter:        fixture.adapter,
			Validator:      fixture.server.validator,
		})
		return err
	}
	fixture.server.applicationPort = &legacyFixtureApplicationPort{stateRoot: fixture.stateRoot, execute: runExecutor}

	createAndApproveServerRun(t, fixture)
	startBody := mutationBody(t, "key-start-real", map[string]any{})
	response := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
		withContentType(fixture.identityHeaders("req-start-real")), startBody)
	if response.status != http.StatusAccepted {
		t.Fatalf("start status = %d, body: %s", response.status, response.body)
	}
	var started RunExecution
	if err := json.Unmarshal(response.body, &started); err != nil {
		t.Fatalf("decode RunExecution: %v", err)
	}
	if started.Kind != "RunExecution" || started.RunID != fixtureRunID || started.TaskID != fixtureTaskID ||
		started.AttemptID == "" || started.State.State != domain.StateVerifying {
		t.Fatalf("RunExecution = %+v", started)
	}
	if executions != 1 {
		t.Fatalf("executions = %d, want 1", executions)
	}

	// Rebuild the entire HTTP adapter over the same durable state root. The
	// status projection and idempotency result must survive the restart.
	restarted, err := New(Config{
		StateRoot:       fixture.stateRoot,
		RepositoryRoot:  fixture.repositoryRoot,
		Selector:        fixture.selector,
		Now:             func() time.Time { return fixtureClock },
		ApplicationPort: &legacyFixtureApplicationPort{stateRoot: fixture.stateRoot, execute: runExecutor},
	})
	if err != nil {
		t.Fatalf("restart server: %v", err)
	}
	fixture.server = restarted
	status := fixture.do(http.MethodGet, APIPrefix+"/runs/"+fixtureRunID+"/status",
		fixture.identityHeaders("req-status-after-restart"), nil)
	if status.status != http.StatusOK {
		t.Fatalf("restart status query = %d, body: %s", status.status, status.body)
	}
	var restored domain.RunState
	if err := json.Unmarshal(status.body, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.State != domain.StateVerifying || restored.CurrentAttemptID != started.AttemptID {
		t.Fatalf("restored state = %+v", restored)
	}

	replay := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
		withContentType(fixture.identityHeaders("req-start-replay")), startBody)
	if replay.status != http.StatusOK {
		t.Fatalf("restart replay = %d, body: %s", replay.status, replay.body)
	}
	var replayed RunExecution
	if err := json.Unmarshal(replay.body, &replayed); err != nil {
		t.Fatal(err)
	}
	if replayed.AttemptID != started.AttemptID || replayed.State.Sequence != started.State.Sequence || executions != 1 {
		t.Fatalf("replay launched another Attempt: first=%+v replay=%+v executions=%d", started, replayed, executions)
	}

	// Fault injection: put the accepted record back into its durable pending
	// intent phase, modelling a crash after Core commits worker.completed but
	// before the HTTP result receipt is renamed into place. The next submission
	// must reconcile that intent from VERIFYING without a second Attempt.
	commandIdentity := Identity{
		Namespace: fixture.server.namespace,
		Scope:     fixture.server.namespace.AuthorityScopeId,
		Operation: "run.start",
		Resource:  fixtureRunID,
		Key:       "key-start-real",
	}
	record, found, err := fixture.server.idempotency.Get(commandIdentity)
	if err != nil || !found {
		t.Fatalf("read completed command receipt: found=%v err=%v", found, err)
	}
	record.Phase, record.Result, record.Status, record.CompletedAt = idempotencyPhasePending, nil, 0, nil
	recordPath, _ := fixture.server.idempotency.recordPaths(commandIdentity)
	if err := fixture.server.idempotency.writeRecord(recordPath, record); err != nil {
		t.Fatalf("inject lost command receipt: %v", err)
	}
	recovered := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
		withContentType(fixture.identityHeaders("req-start-lost-response")), startBody)
	if recovered.status != http.StatusAccepted {
		t.Fatalf("lost-response recovery = %d, body: %s", recovered.status, recovered.body)
	}
	var recoveredExecution RunExecution
	if err := json.Unmarshal(recovered.body, &recoveredExecution); err != nil {
		t.Fatal(err)
	}
	if recoveredExecution.AttemptID != started.AttemptID || executions != 1 {
		t.Fatalf("lost-response recovery launched another Attempt: recovered=%+v executions=%d", recoveredExecution, executions)
	}

	newCommand := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
		withContentType(fixture.identityHeaders("req-start-new-after-verifying")),
		mutationBody(t, "key-start-new-after-verifying", map[string]any{}))
	if newCommand.status != http.StatusConflict {
		t.Fatalf("new start in VERIFYING = %d, body: %s", newCommand.status, newCommand.body)
	}
	if executions != 1 {
		t.Fatalf("non-startable VERIFYING Run reached executor: %d", executions)
	}
}

func TestRunStartUsesPublicApplicationPortAndRecoversLostResponse(t *testing.T) {
	fixture := newServerFixture(t)
	createAndApproveServerRun(t, fixture)
	port := readyFixtureApplicationPort()
	fixture.server.applicationPort = port
	body := mutationBody(t, "key-start-application-port", map[string]any{})

	first := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
		withContentType(fixture.identityHeaders("req-start-application-port")), body)
	if first.status != http.StatusAccepted {
		t.Fatalf("start status=%d body=%s", first.status, first.body)
	}
	var receipt RunExecution
	if err := json.Unmarshal(first.body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.State != port.successor || receipt.AttemptID != port.prepared.AttemptID ||
		port.prepareCalls != 1 || port.startCalls != 1 {
		t.Fatalf("receipt=%+v prepare=%d start=%d", receipt, port.prepareCalls, port.startCalls)
	}

	replay := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
		withContentType(fixture.identityHeaders("req-start-application-port-replay")), body)
	if replay.status != http.StatusOK || port.prepareCalls != 1 || port.startCalls != 1 {
		t.Fatalf("replay status=%d prepare=%d start=%d body=%s", replay.status, port.prepareCalls, port.startCalls, replay.body)
	}

	identity := Identity{Namespace: fixture.server.namespace, Scope: fixture.server.namespace.AuthorityScopeId,
		Operation: "run.start", Resource: fixtureRunID, Key: "key-start-application-port"}
	record, found, err := fixture.server.idempotency.Get(identity)
	if err != nil || !found {
		t.Fatalf("read completed command: found=%t err=%v", found, err)
	}
	if bytes.Contains(record.Intent, []byte(`"prepared"`)) || !bytes.Contains(record.Intent, []byte(port.prepared.AuthorityHead)) {
		t.Fatalf("transport intent must bind the predecessor head without carrying PreparedRunStart: %s", record.Intent)
	}
	record.Phase, record.Result, record.Status, record.CompletedAt = idempotencyPhasePending, nil, 0, nil
	recordPath, _ := fixture.server.idempotency.recordPaths(identity)
	if err := fixture.server.idempotency.writeRecord(recordPath, record); err != nil {
		t.Fatal(err)
	}
	committed := port.projection
	port.projection.AttemptID = "attempt-from-another-start"
	wrongAttempt := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
		withContentType(fixture.identityHeaders("req-start-wrong-attempt")), body)
	if wrongAttempt.status != http.StatusConflict || wrongAttempt.decodeError(t).Reason != "run-start-progress-conflict" ||
		port.prepareCalls != 1 || port.startCalls != 2 {
		t.Fatalf("wrong-attempt recovery status=%d prepare=%d start=%d body=%s",
			wrongAttempt.status, port.prepareCalls, port.startCalls, wrongAttempt.body)
	}
	port.projection = committed
	recovered := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
		withContentType(fixture.identityHeaders("req-start-application-port-recovery")), body)
	if recovered.status != http.StatusAccepted || port.prepareCalls != 1 || port.startCalls != 3 {
		t.Fatalf("recovery status=%d prepare=%d start=%d body=%s", recovered.status, port.prepareCalls, port.startCalls, recovered.body)
	}
}

// TestRunStartFailureCanBeCancelledIdempotently proves controller failure does
// not hide Core progress: an actual failed execution reaches RETRY_PENDING,
// the existing explicit-abort path closes it, and cancel replay adds no second
// terminal transition.
func TestRunStartFailureCanBeCancelledIdempotently(t *testing.T) {
	sealedMigrationSkip(t)
	fixture := newServerFixture(t)
	fixture.adapter.run = func(context.Context, domain.Record) (domain.Record, error) {
		failure, err := port.NewAdapterFailure(port.AdapterIDFake, port.FailureKindConnectionFailure,
			port.RetryDispositionRetryable, nil, nil, fixtureClock)
		if err != nil {
			return domain.Record{}, err
		}
		return domain.Record{}, failure
	}
	executions := 0
	fixture.server.applicationPort = &legacyFixtureApplicationPort{stateRoot: fixture.stateRoot, execute: func(ctx context.Context, runID string) error {
		executions++
		_, err := execution.Run(ctx, execution.Input{
			StateRoot:      fixture.stateRoot,
			RepositoryRoot: fixture.repositoryRoot,
			RunID:          runID,
			Adapter:        fixture.adapter,
			Validator:      fixture.server.validator,
		})
		return err
	}}
	createAndApproveServerRun(t, fixture)

	start := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
		withContentType(fixture.identityHeaders("req-start-failure")), mutationBody(t, "key-start-failure", map[string]any{}))
	if start.status != http.StatusAccepted {
		t.Fatalf("failed start status = %d, body: %s", start.status, start.body)
	}
	var failureReceipt RunExecution
	if err := json.Unmarshal(start.body, &failureReceipt); err != nil {
		t.Fatalf("decode failed Attempt receipt: %v", err)
	}
	if failureReceipt.State.State != domain.StateRetryPending || failureReceipt.AttemptID == "" {
		t.Fatalf("failed Attempt receipt = %+v", failureReceipt)
	}
	replayedStart := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
		withContentType(fixture.identityHeaders("req-start-failure-replay")), mutationBody(t, "key-start-failure", map[string]any{}))
	if replayedStart.status != http.StatusOK || executions != 1 {
		t.Fatalf("failed Attempt replay status=%d executions=%d body=%s", replayedStart.status, executions, replayedStart.body)
	}
	state, err := runstore.New(fixture.stateRoot).Inspect(fixtureRunID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != domain.StateRetryPending {
		t.Fatalf("failed execution state = %s, want RETRY_PENDING", state.State)
	}

	cancelPayload := map[string]any{"actor": "server-operator", "reason": "stop after failed attempt", "runId": fixtureRunID}
	cancelBody := mutationBody(t, "key-cancel-after-start", cancelPayload)
	cancel := fixture.do(http.MethodPost, APIPrefix+"/tasks/"+fixtureTaskID+"/cancel",
		withContentType(fixture.identityHeaders("req-cancel-after-start")), cancelBody)
	if cancel.status != http.StatusOK {
		t.Fatalf("cancel status = %d, body: %s", cancel.status, cancel.body)
	}
	replay := fixture.do(http.MethodPost, APIPrefix+"/tasks/"+fixtureTaskID+"/cancel",
		withContentType(fixture.identityHeaders("req-cancel-after-start-replay")), cancelBody)
	if replay.status != http.StatusOK || !bytes.Equal(bytes.TrimSpace(replay.body), bytes.TrimSpace(cancel.body)) {
		t.Fatalf("cancel replay diverged: first=%d %s replay=%d %s", cancel.status, cancel.body, replay.status, replay.body)
	}
}

func TestRunStartRejectsNullPayloadBeforeExecution(t *testing.T) {
	fixture := newServerFixture(t)
	executions := 0
	fixture.server.applicationPort = &legacyFixtureApplicationPort{stateRoot: fixture.stateRoot, execute: func(context.Context, string) error {
		executions++
		return nil
	}}
	createAndApproveServerRun(t, fixture)

	response := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
		withContentType(fixture.identityHeaders("req-start-null")), mutationBody(t, "key-start-null", nil))
	if response.status != http.StatusBadRequest {
		t.Fatalf("null payload status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Reason != "malformed-json" {
		t.Fatalf("null payload error = %+v", body)
	}
	if executions != 0 {
		t.Fatalf("null payload reached production executor %d times", executions)
	}
}

func TestRunStartPendingIntentRecoversAuthorityStates(t *testing.T) {
	sealedMigrationSkip(t)
	for _, target := range []domain.State{
		domain.StateRunning, domain.StateRetryPending, domain.StateBlocked, domain.StateVerifying,
	} {
		t.Run(string(target), func(t *testing.T) {
			fixture := newServerFixture(t)
			var workerRelease chan struct{}
			workerStarted := make(chan struct{})
			if target == domain.StateRunning {
				workerRelease = make(chan struct{})
				fixture.adapter.run = func(ctx context.Context, request domain.Record) (domain.Record, error) {
					close(workerStarted)
					select {
					case <-workerRelease:
						return successfulServerWorker(ctx, request)
					case <-ctx.Done():
						return domain.Record{}, ctx.Err()
					}
				}
			} else if target == domain.StateVerifying {
				fixture.adapter.run = successfulServerWorker
			} else {
				fixture.adapter.run = func(context.Context, domain.Record) (domain.Record, error) {
					failure, err := port.NewAdapterFailure(port.AdapterIDFake, port.FailureKindConnectionFailure,
						port.RetryDispositionRetryable, nil, nil, fixtureClock)
					if err != nil {
						return domain.Record{}, err
					}
					return domain.Record{}, failure
				}
			}
			executions := 0
			runExecutor := func(ctx context.Context, runID string) error {
				executions++
				_, err := execution.Run(ctx, execution.Input{
					StateRoot: fixture.stateRoot, RepositoryRoot: fixture.repositoryRoot,
					RunID: runID, Adapter: fixture.adapter, Validator: fixture.server.validator,
				})
				return err
			}
			fixture.server.applicationPort = &legacyFixtureApplicationPort{stateRoot: fixture.stateRoot, execute: runExecutor}
			createAndApproveServerRun(t, fixture)
			body := mutationBody(t, "key-start-recovery", map[string]any{})
			persistPendingRunStart(t, fixture, "key-start-recovery", body)

			executionDone := make(chan error, 1)
			if target == domain.StateRunning {
				go func() { executionDone <- runExecutor(context.Background(), fixtureRunID) }()
				<-workerStarted
			} else {
				err := runExecutor(context.Background(), fixtureRunID)
				if target == domain.StateVerifying && err != nil {
					t.Fatalf("successful execution: %v", err)
				}
				if target != domain.StateVerifying && err == nil {
					t.Fatal("failure execution unexpectedly succeeded")
				}
				if target == domain.StateBlocked {
					state, inspectErr := fixture.server.store.Inspect(fixtureRunID)
					if inspectErr != nil {
						t.Fatal(inspectErr)
					}
					if _, _, apiErr := fixture.server.abortRun(context.Background(), state, "server-operator", "stop failed attempt"); apiErr != nil {
						t.Fatalf("abort retry-pending Run: %v", apiErr)
					}
				}
			}

			response := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/start",
				withContentType(fixture.identityHeaders("req-start-recovery")), body)
			if response.status != http.StatusAccepted {
				t.Fatalf("recovery status=%d body=%s", response.status, response.body)
			}
			var receipt RunExecution
			if err := json.Unmarshal(response.body, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.State.State != target || receipt.AttemptID == "" || executions != 1 {
				t.Fatalf("receipt=%+v executions=%d", receipt, executions)
			}
			if target == domain.StateRunning {
				close(workerRelease)
				if err := <-executionDone; err != nil {
					t.Fatalf("release running execution: %v", err)
				}
			}
		})
	}
}

func persistPendingRunStart(t *testing.T, fixture *serverFixture, key string, body []byte) {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	intent, err := fixture.server.prepareRunStartIntent(context.Background(), fixtureRunID)
	if err != nil {
		t.Fatalf("prepare pending Run start: %v", err)
	}
	identity := Identity{
		Namespace: fixture.server.namespace, Scope: fixture.server.namespace.AuthorityScopeId,
		Operation: "run.start", Resource: fixtureRunID, Key: key,
	}
	path, _ := fixture.server.idempotency.recordPaths(identity)
	if err := fixture.server.idempotency.writeRecord(path, Record{
		APIVersion: domain.APIVersionV1Alpha1, Kind: idempotencyRecordKind,
		AuthorityNamespaceId: identity.Namespace, Scope: identity.Scope,
		Operation: identity.Operation, Resource: identity.Resource,
		IdempotencyKey: key, RequestDigest: env.RequestDigest,
		Phase: idempotencyPhasePending, Intent: intent, CreatedAt: fixtureClock,
	}); err != nil {
		t.Fatal(err)
	}
}

func createAndApproveServerRun(t *testing.T, fixture *serverFixture) {
	t.Helper()
	createPayload := map[string]any{
		"runId":          fixtureRunID,
		"taskSpec":       fixtureTask(fixture.repositoryRoot, fixtureTaskID, fixtureAdapterID, fixture.baseSHA),
		"policySnapshot": json.RawMessage(sealPolicy(t, fixturePolicy(fixtureTaskID, fixtureRunID, fixtureAdapterID))),
	}
	created := fixture.do(http.MethodPost, APIPrefix+"/tasks",
		withContentType(fixture.identityHeaders("req-controller-create")), mutationBody(t, "key-controller-create", createPayload))
	if created.status != http.StatusCreated {
		t.Fatalf("create status = %d, body: %s", created.status, created.body)
	}
	approved := fixture.do(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/approval",
		withContentType(fixture.identityHeaders("req-controller-approve")), mutationBody(t, "key-controller-approve", map[string]any{
			"gate": domain.ApprovalGatePlan, "actor": "server-operator",
		}))
	if approved.status != http.StatusCreated {
		t.Fatalf("approval status = %d, body: %s", approved.status, approved.body)
	}
}

func successfulServerWorker(_ context.Context, request domain.Record) (domain.Record, error) {
	var input struct {
		TaskID       string `json:"taskId"`
		RunID        string `json:"runId"`
		AttemptID    string `json:"attemptId"`
		WorktreePath string `json:"worktreePath"`
	}
	if err := json.Unmarshal(request.Data, &input); err != nil {
		return domain.Record{}, err
	}
	if err := os.WriteFile(filepath.Join(input.WorktreePath, "change.txt"), []byte("server worker change\n"), 0o600); err != nil {
		return domain.Record{}, err
	}
	data, err := json.Marshal(map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerResult",
		"taskId": input.TaskID, "runId": input.RunID, "attemptId": input.AttemptID,
		"adapter": map[string]any{"id": fixtureAdapterID, "executable": "/fixture/adapter", "version": "1"},
		"status":  "completed", "summary": "done",
		"declaredChangedFiles": []string{"change.txt"}, "declaredArtifacts": []any{},
		"declaredCommands": []any{}, "declaredRisks": []string{},
		"startedAt": "2026-08-13T12:00:00Z", "completedAt": "2026-08-13T12:00:01Z",
	})
	return domain.Record{Kind: domain.KindWorkerResult, Data: data}, err
}
