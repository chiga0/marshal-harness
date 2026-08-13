package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// TestPublicAPILoopbackE2E drives the complete frozen surface over a real
// loopback listener: idempotent Task create, replay merge without a second
// business object, same-key different-digest conflict fail closed, Task get,
// Run approval with replay merge, Run status, and Task cancel with replay —
// plus the ADR 0018 §3 identity matrix rejections on the wire.
func TestPublicAPILoopbackE2E(t *testing.T) {
	fixture := newServerFixture(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind loopback listener: %v", err)
	}
	httpServer := &http.Server{Handler: fixture.server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Close() }()
	baseURL := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 30 * time.Second}

	doHTTP := func(method, path string, headers map[string]string, body []byte) recordedResponse {
		t.Helper()
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request, err := http.NewRequest(method, baseURL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("%s %s: read body: %v", method, path, err)
		}
		return recordedResponse{status: response.StatusCode, header: response.Header, body: data}
	}
	runDirectory := filepath.Join(fixture.stateRoot, "runs", fixtureRunID)
	expectedNamespace := authority.AuthorityNamespaceId{
		TenantNamespace:  "local",
		ControlPlaneId:   "default",
		AuthorityScopeId: fixture.scope,
	}

	// Step 1 — Task create: the versioned endpoint plans the Run to READY
	// through the existing planning business path.
	taskSpec := fixtureTask(fixture.repositoryRoot, fixtureTaskID, fixtureAdapterID, fixture.baseSHA)
	policySnapshot := sealPolicy(t, fixturePolicy(fixtureTaskID, fixtureRunID, fixtureAdapterID))
	createPayload := map[string]any{
		"runId":          fixtureRunID,
		"taskSpec":       taskSpec,
		"policySnapshot": json.RawMessage(policySnapshot),
	}
	createBody := mutationBody(t, "key-e2e-create", createPayload)
	response := doHTTP(http.MethodPost, APIPrefix+"/tasks", withContentType(fixture.identityHeaders("req-e2e-create")), createBody)
	if response.status != http.StatusCreated {
		t.Fatalf("create status = %d, body: %s", response.status, response.body)
	}
	var submission TaskSubmission
	if err := json.Unmarshal(response.body, &submission); err != nil {
		t.Fatalf("decode TaskSubmission: %v", err)
	}
	if submission.Kind != "TaskSubmission" || submission.TaskID != fixtureTaskID || submission.RunID != fixtureRunID ||
		submission.AdapterID != fixtureAdapterID || submission.State.State != domain.StateReady {
		t.Fatalf("TaskSubmission = %+v", submission)
	}
	if !submission.AuthorityNamespaceId.Equal(expectedNamespace) {
		t.Fatalf("TaskSubmission lacks the owning authorityNamespaceId of the submission quadruple: %+v", submission.AuthorityNamespaceId)
	}

	// Step 2 — identical replay merges: same key, same digest, the stored
	// result returns verbatim and no second Run appears.
	replay := doHTTP(http.MethodPost, APIPrefix+"/tasks", withContentType(fixture.identityHeaders("req-e2e-replay")), createBody)
	if replay.status != http.StatusOK {
		t.Fatalf("replay status = %d, body: %s", replay.status, replay.body)
	}
	var replaySubmission TaskSubmission
	if err := json.Unmarshal(replay.body, &replaySubmission); err != nil {
		t.Fatalf("decode replay TaskSubmission: %v", err)
	}
	if !reflect.DeepEqual(replaySubmission, submission) {
		t.Fatalf("the replay diverged from the stored result:\n got %+v\nwant %+v", replaySubmission, submission)
	}
	if entries, err := os.ReadDir(filepath.Join(fixture.stateRoot, "runs")); err != nil || len(entries) != 1 {
		t.Fatalf("the replay produced a second business object: entries=%d err=%v", len(entries), err)
	}

	// Step 3 — the identical key with a different request digest conflicts
	// fail closed and never creates a business object.
	conflictBody := mutationBody(t, "key-e2e-create", map[string]any{
		"runId":          "run-conflict",
		"taskSpec":       taskSpec,
		"policySnapshot": json.RawMessage(policySnapshot),
	})
	conflict := doHTTP(http.MethodPost, APIPrefix+"/tasks", withContentType(fixture.identityHeaders("req-e2e-conflict")), conflictBody)
	if conflict.status != http.StatusConflict {
		t.Fatalf("conflict status = %d, body: %s", conflict.status, conflict.body)
	}
	if body := conflict.decodeError(t); body.Code != CodeIdempotencyConflict || body.Reason != "idempotency-key-conflict" {
		t.Fatalf("conflict error = %+v", body)
	}
	if entries, err := os.ReadDir(filepath.Join(fixture.stateRoot, "runs")); err != nil || len(entries) != 1 {
		t.Fatalf("the conflict created a business object: entries=%d err=%v", len(entries), err)
	}

	// Step 4 — Task get projects the Task and its Runs.
	response = doHTTP(http.MethodGet, APIPrefix+"/tasks/"+fixtureTaskID, fixture.identityHeaders("req-e2e-get"), nil)
	if response.status != http.StatusOK {
		t.Fatalf("task get status = %d, body: %s", response.status, response.body)
	}
	var view TaskView
	if err := json.Unmarshal(response.body, &view); err != nil {
		t.Fatalf("decode TaskView: %v", err)
	}
	if view.Kind != "TaskView" || view.TaskID != fixtureTaskID || view.Title != "server fixture" ||
		view.LatestRunID != fixtureRunID || len(view.Runs) != 1 || view.Runs[0].State != domain.StateReady {
		t.Fatalf("TaskView = %+v", view)
	}

	// Step 5 — Run approval appends one ApprovalRecord at the plan gate.
	approvalBody := mutationBody(t, "key-e2e-approve", map[string]any{
		"gate": domain.ApprovalGatePlan, "actor": "e2e-operator",
	})
	response = doHTTP(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/approval",
		withContentType(fixture.identityHeaders("req-e2e-approve")), approvalBody)
	if response.status != http.StatusCreated {
		t.Fatalf("approval status = %d, body: %s", response.status, response.body)
	}
	var approval domain.ApprovalRecord
	if err := json.Unmarshal(response.body, &approval); err != nil {
		t.Fatalf("decode ApprovalRecord: %v", err)
	}
	if approval.Kind != domain.KindApprovalRecord || approval.Gate != domain.ApprovalGatePlan ||
		approval.RunID != fixtureRunID || approval.TaskID != fixtureTaskID ||
		approval.Outcome != domain.ApprovalOutcomeApproved || approval.Binding.StateSequence != submission.State.Sequence {
		t.Fatalf("ApprovalRecord = %+v", approval)
	}
	if count := controlRecordCount(t, fixture.stateRoot, fixtureRunID); count != 1 {
		t.Fatalf("control journal records = %d, want 1", count)
	}

	// Step 6 — approval replay merges and appends no second record.
	replay = doHTTP(http.MethodPost, APIPrefix+"/runs/"+fixtureRunID+"/approval",
		withContentType(fixture.identityHeaders("req-e2e-approve-replay")), approvalBody)
	if replay.status != http.StatusOK {
		t.Fatalf("approval replay status = %d, body: %s", replay.status, replay.body)
	}
	var replayApproval domain.ApprovalRecord
	if err := json.Unmarshal(replay.body, &replayApproval); err != nil {
		t.Fatalf("decode replay ApprovalRecord: %v", err)
	}
	if replayApproval.RecordID != approval.RecordID {
		t.Fatalf("the approval replay created a second business object: %s != %s", replayApproval.RecordID, approval.RecordID)
	}
	if count := controlRecordCount(t, fixture.stateRoot, fixtureRunID); count != 1 {
		t.Fatalf("the approval replay appended a control record: count=%d", count)
	}

	// Step 7 — Run status reports the durable RunState.
	response = doHTTP(http.MethodGet, APIPrefix+"/runs/"+fixtureRunID+"/status", fixture.identityHeaders("req-e2e-status"), nil)
	if response.status != http.StatusOK {
		t.Fatalf("status = %d, body: %s", response.status, response.body)
	}
	var state domain.RunState
	if err := json.Unmarshal(response.body, &state); err != nil {
		t.Fatalf("decode RunState: %v", err)
	}
	if state.State != domain.StateReady || state.RunID != fixtureRunID || state.TaskID != fixtureTaskID {
		t.Fatalf("RunState = %+v", state)
	}

	// Step 8 — cancel fails closed while the Run is not RETRY_PENDING.
	cancelPayload := map[string]any{"actor": "e2e-operator", "reason": "e2e cancellation"}
	response = doHTTP(http.MethodPost, APIPrefix+"/tasks/"+fixtureTaskID+"/cancel",
		withContentType(fixture.identityHeaders("req-e2e-cancel-early")), mutationBody(t, "key-e2e-cancel-early", cancelPayload))
	if response.status != http.StatusConflict {
		t.Fatalf("early cancel status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeInvalidState || body.Reason != "invalid-lifecycle-transition" {
		t.Fatalf("early cancel error = %+v", body)
	}

	// Step 9 — advance the Run to RETRY_PENDING through the frozen
	// lifecycle (worker.started then worker.failed) so cancel becomes legal.
	advanceToRetryPending(t, fixture.stateRoot, fixtureRunID)
	response = doHTTP(http.MethodGet, APIPrefix+"/runs/"+fixtureRunID+"/status", fixture.identityHeaders("req-e2e-status-2"), nil)
	if response.status != http.StatusOK {
		t.Fatalf("status after retry = %d", response.status)
	}
	if err := json.Unmarshal(response.body, &state); err != nil {
		t.Fatal(err)
	}
	if state.State != domain.StateRetryPending {
		t.Fatalf("the fixture run did not reach RETRY_PENDING: %s", state.State)
	}
	events, _, err := runstore.New(fixture.stateRoot).ReadEvents(fixtureRunID)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	journalLength := len(events)

	// Step 10 — Task cancel aborts the RETRY_PENDING Run to BLOCKED.
	cancelBody := mutationBody(t, "key-e2e-cancel", cancelPayload)
	response = doHTTP(http.MethodPost, APIPrefix+"/tasks/"+fixtureTaskID+"/cancel",
		withContentType(fixture.identityHeaders("req-e2e-cancel")), cancelBody)
	if response.status != http.StatusOK {
		t.Fatalf("cancel status = %d, body: %s", response.status, response.body)
	}
	var cancellation TaskCancellation
	if err := json.Unmarshal(response.body, &cancellation); err != nil {
		t.Fatalf("decode TaskCancellation: %v", err)
	}
	if cancellation.Kind != "TaskCancellation" || cancellation.TaskID != fixtureTaskID || cancellation.RunID != fixtureRunID ||
		cancellation.State != domain.StateBlocked || cancellation.TerminalReason != lifecycle.AbortTerminalReason ||
		cancellation.Actor != "e2e-operator" {
		t.Fatalf("TaskCancellation = %+v", cancellation)
	}
	if !cancellation.AuthorityNamespaceId.Equal(expectedNamespace) {
		t.Fatalf("TaskCancellation lacks the owning authorityNamespaceId of the submission quadruple: %+v", cancellation.AuthorityNamespaceId)
	}
	for _, name := range []string{"outcome.json", "outcome.md", "result.md"} {
		if _, err := os.Lstat(filepath.Join(runDirectory, name)); err != nil {
			t.Fatalf("cancel did not commit %s: %v", name, err)
		}
	}

	// Step 11 — cancel replay merges: the stored result returns and no
	// second abort event enters the journal.
	replay = doHTTP(http.MethodPost, APIPrefix+"/tasks/"+fixtureTaskID+"/cancel",
		withContentType(fixture.identityHeaders("req-e2e-cancel-replay")), cancelBody)
	if replay.status != http.StatusOK {
		t.Fatalf("cancel replay status = %d, body: %s", replay.status, replay.body)
	}
	var replayCancellation TaskCancellation
	if err := json.Unmarshal(replay.body, &replayCancellation); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replayCancellation, cancellation) {
		t.Fatalf("the cancel replay diverged: got %+v want %+v", replayCancellation, cancellation)
	}
	events, _, err = runstore.New(fixture.stateRoot).ReadEvents(fixtureRunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != journalLength+1 {
		t.Fatalf("the cancel replay journaled a second abort: events=%d want %d", len(events), journalLength+1)
	}
	if last := events[len(events)-1]; last.Type != lifecycle.AbortEventType || last.StateTo != domain.StateBlocked {
		t.Fatalf("the final journal event is not the abort: %+v", last)
	}

	// Step 12 — fresh submissions against the terminal Run fail closed:
	// without a runId every Run is terminal, with the explicit runId the
	// terminal state itself is reported.
	response = doHTTP(http.MethodPost, APIPrefix+"/tasks/"+fixtureTaskID+"/cancel",
		withContentType(fixture.identityHeaders("req-e2e-cancel-terminal")), mutationBody(t, "key-e2e-cancel-terminal", cancelPayload))
	if response.status != http.StatusConflict {
		t.Fatalf("terminal cancel status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeInvalidState || body.Reason != "no-cancelable-run" {
		t.Fatalf("terminal cancel error = %+v", body)
	}
	response = doHTTP(http.MethodPost, APIPrefix+"/tasks/"+fixtureTaskID+"/cancel",
		withContentType(fixture.identityHeaders("req-e2e-cancel-explicit")), mutationBody(t, "key-e2e-cancel-explicit", map[string]any{
			"actor": "e2e-operator", "reason": "e2e cancellation", "runId": fixtureRunID,
		}))
	if response.status != http.StatusConflict {
		t.Fatalf("explicit terminal cancel status = %d, body: %s", response.status, response.body)
	}
	if body := response.decodeError(t); body.Code != CodeInvalidState || body.Reason != "run-terminal" {
		t.Fatalf("explicit terminal cancel error = %+v", body)
	}

	// Step 13 — Task get projects the terminal Run.
	response = doHTTP(http.MethodGet, APIPrefix+"/tasks/"+fixtureTaskID, fixture.identityHeaders("req-e2e-get-final"), nil)
	if response.status != http.StatusOK {
		t.Fatalf("final task get status = %d", response.status)
	}
	if err := json.Unmarshal(response.body, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Runs) != 1 || view.Runs[0].State != domain.StateBlocked ||
		view.Runs[0].TerminalReason != lifecycle.AbortTerminalReason {
		t.Fatalf("final TaskView = %+v", view)
	}

	// Step 14 — the idempotency authority records are durable under the
	// state root and every record carries the complete frozen submission
	// identity quadruple owned by the server's authority namespace: exactly
	// one record per accepted submission (create, approve, cancel) and none
	// for the failed or conflicting submissions.
	records, err := os.ReadDir(filepath.Join(fixture.stateRoot, "idempotency"))
	if err != nil || len(records) == 0 {
		t.Fatalf("no durable idempotency records: err=%v", err)
	}
	recordFiles := 0
	for _, entry := range records {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixture.stateRoot, "idempotency", entry.Name()))
		if err != nil {
			t.Fatalf("read idempotency record %s: %v", entry.Name(), err)
		}
		var record Record
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatalf("decode idempotency record %s: %v", entry.Name(), err)
		}
		if record.Kind != idempotencyRecordKind {
			t.Fatalf("idempotency record %s kind = %q", entry.Name(), record.Kind)
		}
		if !record.AuthorityNamespaceId.Equal(expectedNamespace) {
			t.Fatalf("idempotency record %s is not owned by the server authority namespace: %+v", entry.Name(), record.AuthorityNamespaceId)
		}
		if record.Scope != fixture.scope || record.IdempotencyKey == "" || !strings.HasPrefix(record.RequestDigest, "sha256:") {
			t.Fatalf("idempotency record %s lost the frozen quadruple: %+v", entry.Name(), record)
		}
		recordFiles++
	}
	if recordFiles != 3 {
		t.Fatalf("durable idempotency records = %d, want exactly 3 (create, approve, cancel)", recordFiles)
	}
}

// TestPublicAPIForbiddenMatrixOnTheWire exercises every ADR 0018 §3 public-api
// rejection path over the loopback transport: providerType is rejected
// outright and every workload lease field fails closed.
func TestPublicAPIForbiddenMatrixOnTheWire(t *testing.T) {
	fixture := newServerFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind loopback listener: %v", err)
	}
	httpServer := &http.Server{Handler: fixture.server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Close() }()
	baseURL := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: 30 * time.Second}

	doHTTP := func(method, path string, headers map[string]string, body []byte) recordedResponse {
		t.Helper()
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request, err := http.NewRequest(method, baseURL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return recordedResponse{status: response.StatusCode, header: response.Header, body: data}
	}

	for _, field := range []string{"providerType", "workloadRole", "allocationId", "generation", "fencingToken", "dispatchLease", "leaseId"} {
		body := mustMarshal(t, map[string]any{field: "1"})
		response := doHTTP(http.MethodPost, APIPrefix+"/tasks", withContentType(fixture.identityHeaders("req-wire-forbidden")), body)
		if response.status != http.StatusForbidden {
			t.Fatalf("body field %s status = %d, want 403", field, response.status)
		}
		if got := response.decodeError(t); got.Code != CodeForbiddenIdentity || got.Reason != "forbidden-field:"+field {
			t.Fatalf("body field %s error = %+v", field, got)
		}
	}

	for _, header := range forbiddenHeaders {
		headers := fixture.identityHeaders("req-wire-header")
		headers[header] = "1"
		response := doHTTP(http.MethodGet, APIPrefix+"/runs/run-1/status", headers, nil)
		if response.status != http.StatusForbidden {
			t.Fatalf("header %s status = %d, want 403", header, response.status)
		}
		if got := response.decodeError(t); got.Code != CodeForbiddenIdentity || got.Reason != "forbidden-header:"+header {
			t.Fatalf("header %s error = %+v", header, got)
		}
	}
}

// controlRecordCount counts the control journal records of one Run.
func controlRecordCount(t *testing.T, stateRoot, runID string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateRoot, "runs", runID, "control", "records.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read control journal: %v", err)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return 0
	}
	return bytes.Count(trimmed, []byte("\n")) + 1
}

// advanceToRetryPending moves one READY Run to RETRY_PENDING through the
// frozen lifecycle events (worker.started then worker.failed), exactly as
// the execution path records them.
func advanceToRetryPending(t *testing.T, stateRoot, runID string) {
	t.Helper()
	store := runstore.New(stateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatalf("acquire run lease: %v", err)
	}
	defer func() { _ = lease.Release() }()
	state, err := store.Inspect(runID)
	if err != nil {
		t.Fatalf("inspect run: %v", err)
	}
	append := func(event domain.RunEvent, current domain.RunState, guard lifecycle.Guard) domain.RunState {
		t.Helper()
		next, err := lifecycle.Reduce(current, event, guard)
		if err != nil {
			t.Fatalf("reduce %s: %v", event.Type, err)
		}
		if err := store.Append(lease, event, current.Sequence); err != nil {
			t.Fatalf("append %s: %v", event.Type, err)
		}
		if err := store.WriteSnapshot(lease, next); err != nil {
			t.Fatalf("write snapshot after %s: %v", event.Type, err)
		}
		return next
	}
	build := func(eventType string, attemptID string, from, to domain.State, sequence uint64, payload map[string]any) domain.RunEvent {
		t.Helper()
		eventID, err := domain.NewID("event")
		if err != nil {
			t.Fatalf("generate event ID: %v", err)
		}
		return domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1,
			Kind:       domain.KindRunEvent,
			EventID:    eventID,
			RunID:      runID,
			AttemptID:  attemptID,
			Sequence:   sequence,
			Type:       eventType,
			StateFrom:  from,
			StateTo:    to,
			Timestamp:  fixtureClock,
			Actor:      &domain.Actor{Type: "system", ID: "marshal-worker-runner"},
			Payload:    payload,
		}
	}
	running := append(
		build("worker.started", "attempt-fixture-1", state.State, domain.StateRunning, state.Sequence+1, map[string]any{"attemptId": "attempt-fixture-1"}),
		state,
		lifecycle.Guard{LeaseHeld: true, BudgetAvailable: true},
	)
	append(
		build("worker.failed", "attempt-fixture-1", running.State, domain.StateRetryPending, running.Sequence+1, map[string]any{"error": "fixture operational failure"}),
		running,
		lifecycle.Guard{LeaseHeld: true, BudgetAvailable: true},
	)
}

// TestOpenAPIDocumentFrozen proves the openapi.json freeze tracks the
// implemented surface: identical paths and methods, the complete frozen
// error-code/status table, the full identity matrix and every response
// schema of the five endpoints.
func TestOpenAPIDocumentFrozen(t *testing.T) {
	data, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatalf("read openapi.json: %v", err)
	}
	var document struct {
		OpenAPI               string                     `json:"openapi"`
		Info                  json.RawMessage            `json:"info"`
		Paths                 map[string]json.RawMessage `json:"paths"`
		Components            json.RawMessage            `json:"components"`
		XErrorCodes           map[string]json.RawMessage `json:"x-error-codes"`
		XForbiddenIdentity    json.RawMessage            `json:"x-forbidden-identity"`
		XIdempotentSubmission json.RawMessage            `json:"x-idempotent-submission"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("openapi = %q, want 3.1.0", document.OpenAPI)
	}
	var info struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(document.Info, &info); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if info.Version != ProtocolVersion {
		t.Fatalf("info.version = %q, want %q", info.Version, ProtocolVersion)
	}

	expectedPaths := map[string]string{
		APIPrefix + "/tasks":                 http.MethodPost,
		APIPrefix + "/tasks/{taskId}":        http.MethodGet,
		APIPrefix + "/tasks/{taskId}/cancel": http.MethodPost,
		APIPrefix + "/runs/{runId}/approval": http.MethodPost,
		APIPrefix + "/runs/{runId}/status":   http.MethodGet,
	}
	if len(document.Paths) != len(expectedPaths) {
		t.Fatalf("openapi.json freezes %d paths, the server implements %d", len(document.Paths), len(expectedPaths))
	}
	httpMethods := map[string]bool{
		"get": true, "put": true, "post": true, "delete": true,
		"options": true, "head": true, "patch": true, "trace": true,
	}
	for path, method := range expectedPaths {
		itemRaw, ok := document.Paths[path]
		if !ok {
			t.Fatalf("openapi.json lacks the frozen path %s", path)
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal(itemRaw, &item); err != nil {
			t.Fatalf("decode path item %s: %v", path, err)
		}
		found := ""
		for key := range item {
			if httpMethods[key] {
				if found != "" {
					t.Fatalf("path %s freezes multiple methods", path)
				}
				found = key
			}
		}
		if found != strings.ToLower(method) {
			t.Fatalf("path %s freezes method %q, want %q", path, found, method)
		}
	}

	var components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	}
	if err := json.Unmarshal(document.Components, &components); err != nil {
		t.Fatalf("decode components: %v", err)
	}
	for _, schema := range []string{
		"Error", "MarshalId", "IdempotencyEnvelope", "AuthorityNamespaceId",
		"TaskCreateRequest", "TaskCreatePayload", "TaskSubmission",
		"TaskView", "RunSummary",
		"TaskCancelRequest", "TaskCancelPayload", "TaskCancellation",
		"RunApprovalRequest", "RunApprovalPayload", "ApprovalRecord", "ControlSource", "ApprovalBinding",
		"RunState", "RunLifecycleState", "RunPublication",
	} {
		if _, ok := components.Schemas[schema]; !ok {
			t.Fatalf("openapi.json lacks the frozen schema %s", schema)
		}
	}

	// The submission result documents freeze the owning authorityNamespaceId
	// of the ADR 0018 §3 quadruple as a required member.
	for _, schemaName := range []string{"TaskSubmission", "TaskCancellation"} {
		var schema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(components.Schemas[schemaName], &schema); err != nil {
			t.Fatalf("decode schema %s: %v", schemaName, err)
		}
		if _, ok := schema.Properties["authorityNamespaceId"]; !ok {
			t.Fatalf("schema %s does not freeze the authorityNamespaceId member", schemaName)
		}
		required := false
		for _, name := range schema.Required {
			if name == "authorityNamespaceId" {
				required = true
			}
		}
		if !required {
			t.Fatalf("schema %s does not require the authorityNamespaceId member", schemaName)
		}
	}

	// The frozen idempotent submission identity is the complete quadruple
	// (authorityNamespaceId, scope, idempotencyKey, requestDigest).
	var idempotentSubmission struct {
		Identity   []string `json:"identity"`
		LookupKey  []string `json:"lookupKey"`
		RecordKind string   `json:"recordKind"`
	}
	if err := json.Unmarshal(document.XIdempotentSubmission, &idempotentSubmission); err != nil {
		t.Fatalf("decode x-idempotent-submission: %v", err)
	}
	if !reflect.DeepEqual(idempotentSubmission.Identity, []string{"authorityNamespaceId", "scope", "idempotencyKey", "requestDigest"}) {
		t.Fatalf("x-idempotent-submission identity = %v, want the frozen quadruple", idempotentSubmission.Identity)
	}
	if !reflect.DeepEqual(idempotentSubmission.LookupKey, []string{"authorityNamespaceId", "scope", "idempotencyKey"}) {
		t.Fatalf("x-idempotent-submission lookupKey = %v", idempotentSubmission.LookupKey)
	}
	if idempotentSubmission.RecordKind != idempotencyRecordKind {
		t.Fatalf("x-idempotent-submission recordKind = %q, want %q", idempotentSubmission.RecordKind, idempotencyRecordKind)
	}

	for _, code := range []ErrorCode{
		CodeInvalidRequest, CodeMissingIdentity, CodeForbiddenIdentity, CodeScopeMismatch,
		CodeNotFound, CodeIdempotencyConflict, CodeInvalidState, CodeRejected, CodeInternal,
	} {
		frozenRaw, ok := document.XErrorCodes[string(code)]
		if !ok {
			t.Fatalf("openapi.json lacks the frozen error code %s", code)
		}
		var frozen struct {
			Status int `json:"status"`
		}
		if err := json.Unmarshal(frozenRaw, &frozen); err != nil {
			t.Fatalf("decode error code %s: %v", code, err)
		}
		if frozen.Status != code.status() {
			t.Fatalf("error code %s freezes status %d, the implementation maps %d", code, frozen.Status, code.status())
		}
	}

	var forbiddenIdentity struct {
		ForbiddenFields  []string `json:"forbiddenFields"`
		ForbiddenHeaders []string `json:"forbiddenHeaders"`
	}
	if err := json.Unmarshal(document.XForbiddenIdentity, &forbiddenIdentity); err != nil {
		t.Fatalf("decode x-forbidden-identity: %v", err)
	}
	documentedFields := map[string]bool{}
	for _, field := range forbiddenIdentity.ForbiddenFields {
		documentedFields[field] = true
	}
	for _, canonicalName := range forbiddenFieldNames {
		if !documentedFields[canonicalName] {
			t.Fatalf("openapi.json does not freeze the forbidden field %s", canonicalName)
		}
	}
	if len(documentedFields) != len(forbiddenFieldNames) {
		t.Fatalf("openapi.json freezes %d forbidden fields, the implementation rejects %d",
			len(documentedFields), len(forbiddenFieldNames))
	}
	documentedHeaders := map[string]bool{}
	for _, header := range forbiddenIdentity.ForbiddenHeaders {
		documentedHeaders[header] = true
	}
	for _, header := range forbiddenHeaders {
		if !documentedHeaders[header] {
			t.Fatalf("openapi.json does not freeze the forbidden header %s", header)
		}
	}
	if len(documentedHeaders) != len(forbiddenHeaders) {
		t.Fatalf("openapi.json freezes %d forbidden headers, the implementation rejects %d",
			len(documentedHeaders), len(forbiddenHeaders))
	}
}
