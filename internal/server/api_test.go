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
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/repository"
)

const (
	fixtureAdapterID = "server-fixture"
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
}

func (a *fixtureAdapter) ID() string { return a.id }

func (a *fixtureAdapter) Probe(ctx context.Context) (domain.Record, error) {
	return domain.Record{Kind: domain.KindCapabilitySnapshot, Data: a.capability}, nil
}

func (a *fixtureAdapter) Run(context.Context, domain.Record) (domain.Record, error) {
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
	if err := registry.Register(&fixtureAdapter{id: fixtureAdapterID, capability: fixtureCapability(fixtureAdapterID)}); err != nil {
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
