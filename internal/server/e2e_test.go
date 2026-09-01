package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// TestPublicAPILoopbackE2E drives the complete frozen surface over a real
// loopback listener: idempotent Task create, replay merge without a second
// business object, same-key different-digest conflict fail closed, Task get,
// Run approval with replay merge, Run status, and Task cancel with replay —
// plus the ADR 0018 §3 identity matrix rejections on the wire.
func TestPublicAPILoopbackE2E(t *testing.T) {
	sealedMigrationSkip(t)
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
	// identity tuple owned by the server's authority namespace: successful
	// submissions complete, rejected commands retain their durable pending
	// intent and stable failure identity, and a digest conflict cannot create a
	// second record for the same operation/resource/key.
	records, err := os.ReadDir(filepath.Join(fixture.stateRoot, "idempotency"))
	if err != nil || len(records) == 0 {
		t.Fatalf("no durable idempotency records: err=%v", err)
	}
	expectedRecords := map[string]string{
		"key-e2e-create":          idempotencyPhaseCompleted,
		"key-e2e-approve":         idempotencyPhaseCompleted,
		"key-e2e-cancel-early":    idempotencyPhasePending,
		"key-e2e-cancel":          idempotencyPhaseCompleted,
		"key-e2e-cancel-terminal": idempotencyPhasePending,
		"key-e2e-cancel-explicit": idempotencyPhasePending,
	}
	seenRecords := make(map[string]bool, len(expectedRecords))
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
			t.Fatalf("idempotency record %s lost the frozen identity: %+v", entry.Name(), record)
		}
		wantPhase, ok := expectedRecords[record.IdempotencyKey]
		if !ok {
			t.Fatalf("unexpected idempotency record %s: %+v", entry.Name(), record)
		}
		if seenRecords[record.IdempotencyKey] {
			t.Fatalf("duplicate idempotency record for %q", record.IdempotencyKey)
		}
		seenRecords[record.IdempotencyKey] = true
		if record.Phase != wantPhase {
			t.Fatalf("idempotency record %q phase = %q, want %q", record.IdempotencyKey, record.Phase, wantPhase)
		}
		if record.Operation == "" || record.Resource == "" {
			t.Fatalf("idempotency record %q lost its route identity: %+v", record.IdempotencyKey, record)
		}
		if wantPhase == idempotencyPhasePending && (record.LastFailureCode == "" || record.LastFailureReason == "") {
			t.Fatalf("pending idempotency record %q lost its stable failure receipt: %+v", record.IdempotencyKey, record)
		}
	}
	if len(seenRecords) != len(expectedRecords) {
		t.Fatalf("durable idempotency records = %d, want %d: %+v", len(seenRecords), len(expectedRecords), seenRecords)
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
// schema of the frozen endpoints.
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
		APIPrefix + "/runs/{runId}/start":    http.MethodPost,
		APIPrefix + "/runs/{runId}/approval": http.MethodPost,
		APIPrefix + "/runs/{runId}/status":   http.MethodGet,
		APIPrefix + "/events":                http.MethodGet,
		APIPrefix + "/events/poll":           http.MethodGet,
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
		"RunStartRequest", "RunStartPayload", "RunExecution",
		"RunApprovalRequest", "RunApprovalPayload", "ApprovalRecord", "ControlSource", "ApprovalBinding",
		"RunState", "RunLifecycleState", "RunPublication",
		"EventProjection", "EventPage", "EventResync",
	} {
		if _, ok := components.Schemas[schema]; !ok {
			t.Fatalf("openapi.json lacks the frozen schema %s", schema)
		}
	}

	// The submission result documents freeze the owning authorityNamespaceId
	// of the ADR 0018 §3 quadruple as a required member.
	for _, schemaName := range []string{"TaskSubmission", "TaskCancellation", "RunExecution"} {
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

	// The frozen idempotent submission identity includes the authenticated
	// route operation/resource in addition to the client envelope binding.
	var idempotentSubmission struct {
		Identity     []string `json:"identity"`
		LookupKey    []string `json:"lookupKey"`
		RecordKind   string   `json:"recordKind"`
		RecordSchema []string `json:"recordSchema"`
	}
	if err := json.Unmarshal(document.XIdempotentSubmission, &idempotentSubmission); err != nil {
		t.Fatalf("decode x-idempotent-submission: %v", err)
	}
	if !reflect.DeepEqual(idempotentSubmission.Identity, []string{"authorityNamespaceId", "scope", "operation", "resource", "idempotencyKey", "requestDigest"}) {
		t.Fatalf("x-idempotent-submission identity = %v, want the operation/resource-bound identity", idempotentSubmission.Identity)
	}
	if !reflect.DeepEqual(idempotentSubmission.LookupKey, []string{"authorityNamespaceId", "scope", "operation", "resource", "idempotencyKey"}) {
		t.Fatalf("x-idempotent-submission lookupKey = %v", idempotentSubmission.LookupKey)
	}
	if idempotentSubmission.RecordKind != idempotencyRecordKind {
		t.Fatalf("x-idempotent-submission recordKind = %q, want %q", idempotentSubmission.RecordKind, idempotencyRecordKind)
	}
	wantRecordSchema := []string{
		"apiVersion", "kind", "authorityNamespaceId", "scope", "operation", "resource",
		"idempotencyKey", "requestDigest", "phase", "intent", "status", "result", "createdAt",
		"completedAt", "lastFailureCode", "lastFailureReason", "lastFailureAt",
	}
	if !reflect.DeepEqual(idempotentSubmission.RecordSchema, wantRecordSchema) {
		t.Fatalf("x-idempotent-submission recordSchema = %v, want %v", idempotentSubmission.RecordSchema, wantRecordSchema)
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

// newSSEServerFixture assembles one server fixture with SSE knobs exposed,
// closing the projection after the test.
func newSSEServerFixture(t *testing.T, mutate func(*Config)) *serverFixture {
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
	config := Config{
		StateRoot:      stateRoot,
		RepositoryRoot: root,
		Selector:       selector,
		Now:            func() time.Time { return fixtureClock },
	}
	if mutate != nil {
		mutate(&config)
	}
	server, err := New(config)
	if err != nil {
		t.Fatalf("assemble the server: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return &serverFixture{
		t:              t,
		server:         server,
		repositoryRoot: root,
		stateRoot:      stateRoot,
		baseSHA:        fixtureBaseSHA(t, root),
		scope:          "repo:" + filepath.ToSlash(root),
	}
}

// sseStreamFixture serves one SSE fixture on a loopback listener.
type sseStreamFixture struct {
	fixture *serverFixture
	baseURL string
	server  *http.Server
}

func newSSELoopbackFixture(t *testing.T, mutate func(*Config)) *sseStreamFixture {
	t.Helper()
	fixture := newSSEServerFixture(t, mutate)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind loopback listener: %v", err)
	}
	httpServer := &http.Server{Handler: fixture.server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })
	return &sseStreamFixture{fixture: fixture, baseURL: "http://" + listener.Addr().String(), server: httpServer}
}

func doSSEHTTP(t *testing.T, client *http.Client, method, url string, headers map[string]string, body []byte) recordedResponse {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, url, err)
	}
	return recordedResponse{status: response.StatusCode, header: response.Header, body: data}
}

// sseTestFrame is one parsed SSE frame of the test client.
type sseTestFrame struct {
	id    string
	event string
	data  []byte
}

// openSSEStream opens one SSE subscription and returns its frame channel, a
// channel reporting the stream's terminal error (io.EOF on a clean close)
// and the cancel function disconnecting the client.
func openSSEStream(t *testing.T, baseURL, path string, headers map[string]string) (chan sseTestFrame, chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		cancel()
		t.Fatalf("build SSE request: %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		cancel()
		t.Fatalf("open SSE stream: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		cancel()
		t.Fatalf("SSE stream status = %d, body: %s", response.StatusCode, data)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream" {
		_ = response.Body.Close()
		cancel()
		t.Fatalf("SSE stream content type = %q, want text/event-stream", contentType)
	}
	frames := make(chan sseTestFrame, 128)
	streamErr := make(chan error, 1)
	go func() {
		defer response.Body.Close()
		reader := bufio.NewReader(response.Body)
		var frame sseTestFrame
		haveContent := false
		for {
			line, readErr := reader.ReadString('\n')
			if len(line) > 0 {
				line = strings.TrimRight(line, "\r\n")
				switch {
				case line == "":
					if haveContent {
						// Non-blocking hand-off: a test that stops
						// consuming frames must not prevent the
						// reader from observing the stream close.
						select {
						case frames <- frame:
						default:
						}
						frame = sseTestFrame{}
						haveContent = false
					}
				case strings.HasPrefix(line, ":"):
					// heartbeat comment: never a data frame
				default:
					field, value, _ := strings.Cut(line, ":")
					value = strings.TrimPrefix(value, " ")
					switch field {
					case "id":
						frame.id = value
						haveContent = true
					case "event":
						frame.event = value
						haveContent = true
					case "data":
						frame.data = append(frame.data, []byte(value)...)
						haveContent = true
					}
				}
				continue
			}
			streamErr <- readErr
			return
		}
	}()
	return frames, streamErr, cancel
}

func nextFrame(t *testing.T, frames chan sseTestFrame, streamErr chan error, timeout time.Duration) sseTestFrame {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case err := <-streamErr:
		t.Fatalf("the SSE stream closed while waiting for a frame: %v", err)
		return sseTestFrame{}
	case <-time.After(timeout):
		t.Fatal("timed out waiting for an SSE frame")
		return sseTestFrame{}
	}
}

func waitStreamClose(t *testing.T, streamErr chan error, timeout time.Duration) {
	t.Helper()
	select {
	case <-streamErr:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for the SSE stream to close")
	}
}

func pollEvents(t *testing.T, stream *sseStreamFixture, cursor string, limit int) EventPage {
	t.Helper()
	path := APIPrefix + "/events/poll"
	query := ""
	if cursor != "" {
		query = "cursor=" + cursor
	}
	if limit > 0 {
		if query != "" {
			query += "&"
		}
		query += "limit=" + strconv.Itoa(limit)
	}
	if query != "" {
		path += "?" + query
	}
	response := doSSEHTTP(t, &http.Client{Timeout: 30 * time.Second}, http.MethodGet,
		stream.baseURL+path, stream.fixture.identityHeaders("req-poll"), nil)
	if response.status != http.StatusOK {
		t.Fatalf("poll status = %d, body: %s", response.status, response.body)
	}
	var page EventPage
	if err := json.Unmarshal(response.body, &page); err != nil {
		t.Fatalf("decode EventPage: %v", err)
	}
	return page
}

// TestSSELoopbackSubscribeAndPollConsistency covers the normal subscription
// path over the wire: the backlog arrives with increasing sequences, the
// frame id carries the eventId, the SSE data frames are byte-identical to
// the polling fallback's EventProjection JSON, and a live journal append
// reaches the open stream with the identical cursor boundary as polling.
func TestSSELoopbackSubscribeAndPollConsistency(t *testing.T) {
	stream := newSSELoopbackFixture(t, nil)
	fixture := stream.fixture
	journal := appendJournalEvents(t, fixture.stateRoot, "run-sse", 0, 9, 0)
	fixture.server.events.ScanNow()

	frames, streamErr, cancel := openSSEStream(t, stream.baseURL, APIPrefix+"/events", fixture.identityHeaders("req-sse-consistency"))
	defer cancel()
	received := make([]sseTestFrame, 0, 9)
	for index := 0; index < 9; index++ {
		received = append(received, nextFrame(t, frames, streamErr, 5*time.Second))
	}

	page := pollEvents(t, stream, "", 0)
	if len(page.Events) != len(received) {
		t.Fatalf("poll count = %d, stream count = %d: the channels diverge", len(page.Events), len(received))
	}
	for index, frame := range received {
		if frame.id != page.Events[index].EventID {
			t.Fatalf("SSE frame id %q does not carry the eventId %q", frame.id, page.Events[index].EventID)
		}
		var projected EventProjection
		if err := json.Unmarshal(frame.data, &projected); err != nil {
			t.Fatalf("decode SSE data frame: %v", err)
		}
		if projected.Kind != KindEventProjection {
			t.Fatalf("SSE data frame kind = %q, want %q", projected.Kind, KindEventProjection)
		}
		if projected.LedgerSequence != uint64(index+1) {
			t.Fatalf("SSE ledgerSequence = %d, want %d", projected.LedgerSequence, index+1)
		}
		if projected.EventID != journal[index].EventID || projected.Scope != fixture.scope {
			t.Fatalf("SSE projection lost its identity: %+v", projected)
		}
		polledData, err := json.Marshal(page.Events[index])
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(frame.data, polledData) {
			t.Fatalf("SSE frame diverges from the poll projection at ledgerSequence %d:\n SSE: %s\npoll: %s",
				projected.LedgerSequence, frame.data, polledData)
		}
	}

	// A live event appended to the journal reaches the open subscription
	// and the polling boundary stays identical on both channels.
	appendJournalEvents(t, fixture.stateRoot, "run-sse", 9, 1, 0)
	live := nextFrame(t, frames, streamErr, 5*time.Second)
	var liveProjection EventProjection
	if err := json.Unmarshal(live.data, &liveProjection); err != nil {
		t.Fatal(err)
	}
	if liveProjection.LedgerSequence != 10 || live.id != liveProjection.EventID {
		t.Fatalf("live frame = %+v id %q, want ledgerSequence 10 with the matching eventId", liveProjection, live.id)
	}
	page = pollEvents(t, stream, "9", 0)
	if len(page.Events) != 1 || page.Events[0].EventID != live.id {
		t.Fatalf("poll after cursor 9 = %+v, want exactly the live event", page.Events)
	}
}

// TestSSELoopbackResumeFromCursorAndLastEventID covers disconnect/reconnect:
// the cursor and Last-Event-ID resume with the exclusive boundary, and the
// eventId-keyed union of every session covers the complete backlog
// (at-least-once with client-side dedup).
func TestSSELoopbackResumeFromCursorAndLastEventID(t *testing.T) {
	stream := newSSELoopbackFixture(t, nil)
	fixture := stream.fixture
	journal := appendJournalEvents(t, fixture.stateRoot, "run-sse", 0, 9, 0)
	fixture.server.events.ScanNow()

	frames, streamErr, cancel := openSSEStream(t, stream.baseURL, APIPrefix+"/events", fixture.identityHeaders("req-sse-resume-1"))
	seen := map[string]uint64{}
	for index := 0; index < 4; index++ {
		frame := nextFrame(t, frames, streamErr, 5*time.Second)
		seen[frame.id] = uint64(index + 1)
	}
	cancel()

	frames, streamErr, cancel = openSSEStream(t, stream.baseURL, APIPrefix+"/events?cursor=4", fixture.identityHeaders("req-sse-resume-2"))
	for expected := uint64(5); expected <= 9; expected++ {
		frame := nextFrame(t, frames, streamErr, 5*time.Second)
		var projected EventProjection
		if err := json.Unmarshal(frame.data, &projected); err != nil {
			t.Fatal(err)
		}
		if projected.LedgerSequence != expected {
			t.Fatalf("cursor resume delivered ledgerSequence %d, want %d", projected.LedgerSequence, expected)
		}
		seen[frame.id] = expected
	}
	cancel()

	headers := fixture.identityHeaders("req-sse-resume-3")
	headers["Last-Event-ID"] = journal[6].EventID
	frames, streamErr, cancel = openSSEStream(t, stream.baseURL, APIPrefix+"/events", headers)
	for expected := uint64(8); expected <= 9; expected++ {
		frame := nextFrame(t, frames, streamErr, 5*time.Second)
		seen[frame.id] = expected
	}
	cancel()

	if len(seen) != 9 {
		t.Fatalf("at-least-once resume lost events: %d distinct eventIds, want 9", len(seen))
	}
	for _, event := range journal {
		if _, ok := seen[event.EventID]; !ok {
			t.Fatalf("the resume lost eventId %s", event.EventID)
		}
	}
}

// TestSSELoopbackExpiredCursorResync covers the deterministic resync path on
// both channels: a cursor beyond the ledger or an unknown Last-Event-ID
// never continues silently, and the directive is byte-identical across
// repeated observations.
func TestSSELoopbackExpiredCursorResync(t *testing.T) {
	stream := newSSELoopbackFixture(t, nil)
	fixture := stream.fixture
	appendJournalEvents(t, fixture.stateRoot, "run-sse", 0, 3, 0)
	fixture.server.events.ScanNow()
	client := &http.Client{Timeout: 30 * time.Second}

	first := doSSEHTTP(t, client, http.MethodGet, stream.baseURL+APIPrefix+"/events?cursor=999",
		fixture.identityHeaders("req-sse-expired-1"), nil)
	if first.status != http.StatusConflict {
		t.Fatalf("expired cursor status = %d, body: %s", first.status, first.body)
	}
	var resync EventResync
	if err := json.Unmarshal(first.body, &resync); err != nil {
		t.Fatalf("decode EventResync: %v", err)
	}
	if resync.Kind != KindEventResync || resync.Reason != resyncReasonCursorExpired || resync.StartSequence != 1 {
		t.Fatalf("EventResync = %+v, want kind %q reason %q startSequence 1", resync, KindEventResync, resyncReasonCursorExpired)
	}
	if !resync.AuthorityNamespaceId.Equal(fixture.server.Namespace()) || resync.Scope != fixture.scope {
		t.Fatalf("EventResync lost the cursor identity: %+v scope=%q", resync.AuthorityNamespaceId, resync.Scope)
	}
	if !strings.HasPrefix(resync.SnapshotDigest, "sha256:") {
		t.Fatalf("EventResync lacks the snapshot digest: %+v", resync)
	}

	second := doSSEHTTP(t, client, http.MethodGet, stream.baseURL+APIPrefix+"/events?cursor=999",
		fixture.identityHeaders("req-sse-expired-2"), nil)
	if !bytes.Equal(first.body, second.body) {
		t.Fatalf("the resync directive is not deterministic:\n%s\n%s", first.body, second.body)
	}

	pollResponse := doSSEHTTP(t, client, http.MethodGet, stream.baseURL+APIPrefix+"/events/poll?cursor=999",
		fixture.identityHeaders("req-poll-expired"), nil)
	if pollResponse.status != http.StatusConflict {
		t.Fatalf("poll expired cursor status = %d, body: %s", pollResponse.status, pollResponse.body)
	}
	var pollResync EventResync
	if err := json.Unmarshal(pollResponse.body, &pollResync); err != nil {
		t.Fatal(err)
	}
	if pollResync != resync {
		t.Fatalf("the poll resync diverges from the stream resync: %+v != %+v", pollResync, resync)
	}

	headers := fixture.identityHeaders("req-sse-expired-3")
	headers["Last-Event-ID"] = "event-unknown"
	response := doSSEHTTP(t, client, http.MethodGet, stream.baseURL+APIPrefix+"/events", headers, nil)
	if response.status != http.StatusConflict {
		t.Fatalf("unknown Last-Event-ID status = %d, body: %s", response.status, response.body)
	}
}

// TestSSELoopbackJournalRewriteForcesResync covers the gap/compaction path:
// a journal that lost projected events forces the deterministic resync with
// the rebuilt snapshot digest, and fresh subscriptions replay the rebuilt
// backlog from sequence 1.
func TestSSELoopbackJournalRewriteForcesResync(t *testing.T) {
	stream := newSSELoopbackFixture(t, nil)
	fixture := stream.fixture
	journal := appendJournalEvents(t, fixture.stateRoot, "run-sse", 0, 9, 0)
	fixture.server.events.ScanNow()
	if page := pollEvents(t, stream, "5", 0); len(page.Events) != 4 {
		t.Fatalf("poll before the rewrite = %d events, want 4", len(page.Events))
	}

	rewriteJournal(t, fixture.stateRoot, "run-sse", journal[:2])
	fixture.server.events.ScanNow()

	client := &http.Client{Timeout: 30 * time.Second}
	response := doSSEHTTP(t, client, http.MethodGet, stream.baseURL+APIPrefix+"/events?cursor=5",
		fixture.identityHeaders("req-sse-gap"), nil)
	if response.status != http.StatusConflict {
		t.Fatalf("gap cursor status = %d, body: %s", response.status, response.body)
	}
	var resync EventResync
	if err := json.Unmarshal(response.body, &resync); err != nil {
		t.Fatal(err)
	}
	rebuilt := pollEvents(t, stream, "", 0)
	if len(rebuilt.Events) != 2 {
		t.Fatalf("poll after the rewrite = %d events, want 2", len(rebuilt.Events))
	}
	if resync.StartSequence != 1 || resync.SnapshotDigest != rebuilt.SnapshotDigest {
		t.Fatalf("gap resync = %+v, want startSequence 1 with the rebuilt digest %q", resync, rebuilt.SnapshotDigest)
	}

	frames, streamErr, cancel := openSSEStream(t, stream.baseURL, APIPrefix+"/events", fixture.identityHeaders("req-sse-gap-replay"))
	defer cancel()
	for expected := uint64(1); expected <= 2; expected++ {
		frame := nextFrame(t, frames, streamErr, 5*time.Second)
		var projected EventProjection
		if err := json.Unmarshal(frame.data, &projected); err != nil {
			t.Fatal(err)
		}
		if projected.LedgerSequence != expected {
			t.Fatalf("rebuilt backlog delivered sequence %d, want %d", projected.LedgerSequence, expected)
		}
	}
}

// TestSSELoopbackSlowSubscriberBackpressure covers the bounded backpressure
// rule over the wire: a stalled subscriber is disconnected and guided to
// resync while the event source keeps ingesting without blocking.
func TestSSELoopbackSlowSubscriberBackpressure(t *testing.T) {
	stream := newSSELoopbackFixture(t, func(config *Config) {
		config.SSEBufferLimit = 2
		config.SSEHeartbeatInterval = 50 * time.Millisecond
	})
	fixture := stream.fixture
	appendJournalEvents(t, fixture.stateRoot, "run-sse", 0, 1, 0)
	fixture.server.events.ScanNow()

	frames, streamErr, cancel := openSSEStream(t, stream.baseURL, APIPrefix+"/events", fixture.identityHeaders("req-sse-slow"))
	defer cancel()
	nextFrame(t, frames, streamErr, 5*time.Second) // the client reads once, then stalls

	// Flood the stalled subscriber; the journal ingest must never block.
	appendJournalEvents(t, fixture.stateRoot, "run-sse", 1, 400, 2048)
	sourceStart := time.Now()
	fixture.server.events.ScanNow()
	if elapsed := time.Since(sourceStart); elapsed > 2*time.Second {
		t.Fatalf("the event source blocked for %v on the slow subscriber", elapsed)
	}

	waitStreamClose(t, streamErr, 10*time.Second)

	// Recovery path: the polling fallback still serves the complete
	// projection, so the disconnected client can resync deterministically.
	page := pollEvents(t, stream, "", 1000)
	if len(page.Events) != 401 {
		t.Fatalf("poll after backpressure = %d events, want 401", len(page.Events))
	}
}

// toggleAuthorizer is a runtime-flippable Authorizer for the fail-closed
// re-Authorization tests.
type toggleAuthorizer struct {
	mu      sync.Mutex
	allowed bool
}

func (a *toggleAuthorizer) authorize(principal string, namespace authority.AuthorityNamespaceId, scope string) error {
	a.mu.Lock()
	allowed := a.allowed
	a.mu.Unlock()
	if !allowed {
		return errors.New("sse fixture: authorization revoked")
	}
	return defaultAuthorizer(principal, namespace, scope)
}

func (a *toggleAuthorizer) revoke() {
	a.mu.Lock()
	a.allowed = false
	a.mu.Unlock()
}

// TestSSELoopbackSensitiveChangeReauthorizationFailClosed covers immediate
// re-Authorization: a sensitive change revalidates every subscription at
// once and a failed check closes the connection fail closed within 5s,
// never degraded to full visibility.
func TestSSELoopbackSensitiveChangeReauthorizationFailClosed(t *testing.T) {
	gate := &toggleAuthorizer{allowed: true}
	stream := newSSELoopbackFixture(t, func(config *Config) {
		config.SSEReauthzInterval = 50 * time.Millisecond
		config.Authorizer = gate.authorize
	})
	fixture := stream.fixture
	appendJournalEvents(t, fixture.stateRoot, "run-sse", 0, 1, 0)
	fixture.server.events.ScanNow()

	frames, streamErr, cancel := openSSEStream(t, stream.baseURL, APIPrefix+"/events", fixture.identityHeaders("req-sse-reauth-now"))
	defer cancel()
	nextFrame(t, frames, streamErr, 5*time.Second)

	gate.revoke()
	fixture.server.NotifySensitiveChange()
	waitStreamClose(t, streamErr, 5*time.Second)
}

// TestSSELoopbackPeriodicReauthorizationFailClosed covers periodic
// re-Authorization: without any manual trigger the failed check closes the
// connection within the 5s bound.
func TestSSELoopbackPeriodicReauthorizationFailClosed(t *testing.T) {
	gate := &toggleAuthorizer{allowed: true}
	stream := newSSELoopbackFixture(t, func(config *Config) {
		config.SSEReauthzInterval = 50 * time.Millisecond
		config.Authorizer = gate.authorize
	})
	fixture := stream.fixture
	appendJournalEvents(t, fixture.stateRoot, "run-sse", 0, 1, 0)
	fixture.server.events.ScanNow()

	frames, streamErr, cancel := openSSEStream(t, stream.baseURL, APIPrefix+"/events", fixture.identityHeaders("req-sse-reauth-periodic"))
	defer cancel()
	nextFrame(t, frames, streamErr, 5*time.Second)

	gate.revoke()
	waitStreamClose(t, streamErr, 5*time.Second)
}

// TestSSESurfaceReadOnly covers the interface-surface rule: the SSE endpoint
// is strictly read-only — no business ACK, lease heartbeat or command
// channel exists in routes, methods or frames.
func TestSSESurfaceReadOnly(t *testing.T) {
	stream := newSSELoopbackFixture(t, nil)
	fixture := stream.fixture
	client := &http.Client{Timeout: 30 * time.Second}

	response := doSSEHTTP(t, client, http.MethodPost, stream.baseURL+APIPrefix+"/events",
		withContentType(fixture.identityHeaders("req-sse-surface-post")), []byte("{}"))
	if response.status != http.StatusMethodNotAllowed || response.header.Get("Allow") != http.MethodGet {
		t.Fatalf("POST /events status = %d allow=%q, want 405 Allow GET", response.status, response.header.Get("Allow"))
	}
	response = doSSEHTTP(t, client, http.MethodPut, stream.baseURL+APIPrefix+"/events/poll",
		withContentType(fixture.identityHeaders("req-sse-surface-put")), []byte("{}"))
	if response.status != http.StatusMethodNotAllowed {
		t.Fatalf("PUT /events/poll status = %d, want 405", response.status)
	}

	for _, path := range []string{"/events/ack", "/events/command", "/events/lease"} {
		response = doSSEHTTP(t, client, http.MethodGet, stream.baseURL+APIPrefix+path,
			fixture.identityHeaders("req-sse-surface-route"), nil)
		if response.status != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, response.status)
		}
		if body := response.decodeError(t); body.Code != CodeNotFound || body.Reason != "unknown-route" {
			t.Fatalf("GET %s error = %+v", path, body)
		}
	}

	response = doSSEHTTP(t, client, http.MethodGet, stream.baseURL+APIPrefix+"/events",
		fixture.identityHeaders("req-sse-surface-body"), []byte("{}"))
	if response.status != http.StatusBadRequest || response.decodeError(t).Reason != "body-not-allowed" {
		t.Fatalf("GET /events with body status = %d body: %s", response.status, response.body)
	}
	response = doSSEHTTP(t, client, http.MethodGet, stream.baseURL+APIPrefix+"/events?bogus=1",
		fixture.identityHeaders("req-sse-surface-query"), nil)
	if response.status != http.StatusBadRequest || response.decodeError(t).Reason != "unknown-query:bogus" {
		t.Fatalf("unknown query status = %d body: %s", response.status, response.body)
	}
	response = doSSEHTTP(t, client, http.MethodGet, stream.baseURL+APIPrefix+"/events?cursor=abc",
		fixture.identityHeaders("req-sse-surface-cursor"), nil)
	if response.status != http.StatusBadRequest || response.decodeError(t).Reason != "cursor-invalid" {
		t.Fatalf("malformed cursor status = %d body: %s", response.status, response.body)
	}
	response = doSSEHTTP(t, client, http.MethodGet, stream.baseURL+APIPrefix+"/events/poll?limit=0",
		fixture.identityHeaders("req-sse-surface-limit"), nil)
	if response.status != http.StatusBadRequest || response.decodeError(t).Reason != "limit-invalid" {
		t.Fatalf("invalid limit status = %d body: %s", response.status, response.body)
	}

	// An established stream never carries ACK or command frames: every
	// data frame is one EventProjection and nothing else.
	appendJournalEvents(t, fixture.stateRoot, "run-sse", 0, 3, 0)
	fixture.server.events.ScanNow()
	frames, streamErr, cancel := openSSEStream(t, stream.baseURL, APIPrefix+"/events", fixture.identityHeaders("req-sse-surface-stream"))
	defer cancel()
	for index := 0; index < 3; index++ {
		frame := nextFrame(t, frames, streamErr, 5*time.Second)
		var members map[string]json.RawMessage
		if err := json.Unmarshal(frame.data, &members); err != nil {
			t.Fatalf("decode SSE frame: %v", err)
		}
		for forbidden := range map[string]bool{"ack": true, "command": true, "lease": true, "heartbeat": true} {
			if _, present := members[forbidden]; present {
				t.Fatalf("the SSE stream carries a %q channel member: %s", forbidden, frame.data)
			}
		}
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(frame.data, &kind); err != nil {
			t.Fatal(err)
		}
		if kind.Kind != KindEventProjection {
			t.Fatalf("the SSE stream carries a non-projection frame kind %q", kind.Kind)
		}
	}
}

// TestSSENotifyHookAdaptation covers the MARSHAL_NOTIFY_CMD read-only
// adaptation: with the watcher interval effectively disabled, only the hook
// payload wake may deliver the journaled event, and invalid payloads are
// dropped without effect.
func TestSSENotifyHookAdaptation(t *testing.T) {
	stream := newSSELoopbackFixture(t, func(config *Config) {
		config.EventWatchInterval = time.Hour
	})
	fixture := stream.fixture
	appendJournalEvents(t, fixture.stateRoot, "run-sse", 0, 1, 0)

	fixture.server.HandleNotifyHook([]byte("not-json"))
	time.Sleep(50 * time.Millisecond)
	if page, resync := fixture.server.events.Poll(0, 10); resync != nil || len(page.Events) != 0 {
		t.Fatalf("an invalid notify payload ingested %d events", len(page.Events))
	}

	payload := mustMarshal(t, map[string]any{
		"runId":         "run-sse",
		"taskId":        "task-sse",
		"stateFrom":     "CREATED",
		"stateTo":       "PLANNED",
		"eventSequence": 1,
		"timestamp":     fixtureClock.Format(time.RFC3339),
	})
	fixture.server.HandleNotifyHook(payload)
	waitFor(t, 2*time.Second, func() bool {
		page, resync := fixture.server.events.Poll(0, 10)
		return resync == nil && len(page.Events) == 1
	}, "the notify wake to ingest the journal event")
}
