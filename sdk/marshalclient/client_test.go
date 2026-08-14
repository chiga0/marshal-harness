package marshalclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	fixtureScope     = "repo:/srv/fixture"
	fixturePrincipal = "fixture-operator"
)

var fixtureNamespace = AuthorityNamespaceId{
	TenantNamespace:  "local",
	ControlPlaneId:   "default",
	AuthorityScopeId: fixtureScope,
}

// verifyIdentity asserts the complete frozen identity envelope of one
// request and echoes the request id like the real server.
func verifyIdentity(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if got := r.Header.Get(HeaderProtocolVersion); got != DefaultProtocolVersion {
		t.Errorf("%s = %q, want %q", HeaderProtocolVersion, got, DefaultProtocolVersion)
	}
	if got := r.Header.Get(HeaderAudience); got != DefaultAudience {
		t.Errorf("%s = %q, want %q", HeaderAudience, got, DefaultAudience)
	}
	if got := r.Header.Get(HeaderScope); got != fixtureScope {
		t.Errorf("%s = %q, want %q", HeaderScope, got, fixtureScope)
	}
	if got := r.Header.Get(HeaderPrincipal); got != fixturePrincipal {
		t.Errorf("%s = %q, want %q", HeaderPrincipal, got, fixturePrincipal)
	}
	requestID := r.Header.Get(HeaderRequestID)
	if requestID == "" {
		t.Errorf("%s is empty", HeaderRequestID)
	} else {
		w.Header().Set(HeaderRequestID, requestID)
	}
	deadline := r.Header.Get(HeaderDeadline)
	parsed, err := time.Parse(time.RFC3339, deadline)
	if err != nil {
		t.Errorf("%s %q is not RFC 3339: %v", HeaderDeadline, deadline, err)
	} else if !parsed.After(time.Now()) {
		t.Errorf("%s %q is not in the future", HeaderDeadline, deadline)
	}
}

func writeFakeJSON(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	writeRawJSON(w, status, data)
}

func writeRawJSON(w http.ResponseWriter, status int, data []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}

func fakeError(code ErrorCode, reason, message string) map[string]any {
	return map[string]any{
		"apiVersion": "marshal.dev/v1alpha1",
		"kind":       "Error",
		"code":       string(code),
		"reason":     reason,
		"message":    message,
		"requestId":  "req-err-1",
	}
}

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := New(Config{
		BaseURL:   baseURL,
		Principal: fixturePrincipal,
		Scope:     fixtureScope,
	})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return client
}

type fakeEnvelope struct {
	IdempotencyKey string          `json:"idempotencyKey"`
	RequestDigest  string          `json:"requestDigest"`
	Payload        json.RawMessage `json:"payload"`
}

func decodeFakeEnvelope(t *testing.T, body []byte) fakeEnvelope {
	t.Helper()
	var envelope fakeEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Errorf("decode idempotency envelope: %v", err)
	}
	return envelope
}

// verifyRequestDigest recomputes the RFC 8785 canonical digest of the
// received payload and asserts it equals the envelope requestDigest, the
// identical check the real server performs.
func verifyRequestDigest(t *testing.T, envelope fakeEnvelope) {
	t.Helper()
	computed, err := RequestDigest(envelope.Payload)
	if err != nil {
		t.Errorf("canonicalize payload: %v", err)
		return
	}
	if computed != envelope.RequestDigest {
		t.Errorf("requestDigest = %q, recomputed canonical digest = %q", envelope.RequestDigest, computed)
	}
}

func fixtureSubmission() TaskSubmission {
	return TaskSubmission{
		APIVersion:           "marshal.dev/v1alpha1",
		Kind:                 "TaskSubmission",
		AuthorityNamespaceId: fixtureNamespace,
		TaskID:               "task-1",
		RunID:                "run-1",
		AdapterID:            "fixture-adapter",
		State: RunState{
			APIVersion: "marshal.dev/v1alpha1",
			Kind:       "RunState",
			TaskID:     "task-1",
			RunID:      "run-1",
			State:      "READY",
			Sequence:   3,
			CreatedAt:  time.Unix(1700000000, 0).UTC(),
			UpdatedAt:  time.Unix(1700000100, 0).UTC(),
		},
	}
}

func fixtureTaskView() TaskView {
	return TaskView{
		APIVersion:  "marshal.dev/v1alpha1",
		Kind:        "TaskView",
		TaskID:      "task-1",
		Title:       "fixture task",
		LatestRunID: "run-1",
		Runs: []RunSummary{{
			RunID:     "run-1",
			State:     "READY",
			Sequence:  3,
			CreatedAt: time.Unix(1700000000, 0).UTC(),
			UpdatedAt: time.Unix(1700000100, 0).UTC(),
		}},
	}
}

func fixtureCancellation() TaskCancellation {
	return TaskCancellation{
		APIVersion:           "marshal.dev/v1alpha1",
		Kind:                 "TaskCancellation",
		AuthorityNamespaceId: fixtureNamespace,
		TaskID:               "task-1",
		RunID:                "run-1",
		State:                "BLOCKED",
		TerminalReason:       "aborted-by-operator",
		Actor:                fixturePrincipal,
		Sequence:             4,
	}
}

func fixtureApproval() ApprovalRecord {
	return ApprovalRecord{
		APIVersion:      "marshal.dev/v1alpha1",
		Kind:            "ApprovalRecord",
		RecordID:        "approval-1",
		TaskID:          "task-1",
		RunID:           "run-1",
		ControlSequence: 1,
		Gate:            GatePlan,
		Source:          ControlSource{Type: "human", ID: fixturePrincipal},
		Binding: ApprovalBinding{
			StateSequence:    3,
			SpecDigest:       "sha256:spec",
			PolicyDigest:     "sha256:policy",
			CapabilityDigest: "sha256:capability",
			BaseSHA:          "base-sha",
		},
		Outcome:   "approved",
		CreatedAt: time.Unix(1700000200, 0).UTC(),
	}
}

func fixtureRunState() RunState {
	return RunState{
		APIVersion:       "marshal.dev/v1alpha1",
		Kind:             "RunState",
		TaskID:           "task-1",
		RunID:            "run-1",
		State:            "RUNNING",
		Sequence:         5,
		CurrentAttemptID: "attempt-1",
		CreatedAt:        time.Unix(1700000000, 0).UTC(),
		UpdatedAt:        time.Unix(1700000300, 0).UTC(),
	}
}

func TestNewValidatesConfigAndAppliesDefaults(t *testing.T) {
	invalid := []Config{
		{Principal: "p", Scope: "s"},
		{BaseURL: "http://127.0.0.1:7718", Scope: "s"},
		{BaseURL: "http://127.0.0.1:7718", Principal: "p"},
		{BaseURL: "not an absolute url", Principal: "p", Scope: "s"},
	}
	for _, config := range invalid {
		if _, err := New(config); err == nil {
			t.Errorf("New(%+v) succeeded, want error", config)
		}
	}
	client, err := New(Config{
		BaseURL:        "http://127.0.0.1:7718/",
		Principal:      "p",
		Scope:          "s",
		RequestTimeout: -1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.baseURL != "http://127.0.0.1:7718" {
		t.Errorf("baseURL = %q", client.baseURL)
	}
	if client.audience != DefaultAudience {
		t.Errorf("audience = %q, want %q", client.audience, DefaultAudience)
	}
	if client.protocolVersion != DefaultProtocolVersion {
		t.Errorf("protocolVersion = %q, want %q", client.protocolVersion, DefaultProtocolVersion)
	}
	if client.requestTimeout != DefaultRequestTimeout {
		t.Errorf("requestTimeout = %v, want %v", client.requestTimeout, DefaultRequestTimeout)
	}
}

func TestCanonicalJSONRFC8785(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"member order", `{"b":2,"a":1,"c":{"z":true,"a":[3,2,1]}}`, `{"a":1,"b":2,"c":{"a":[3,2,1],"z":true}}`},
		{"number normalization", `[1e1,1e-7,1e21,100,0.5,1.0,-0.000001,3.14159265358979]`, `[10,1e-7,1e+21,100,0.5,1,-0.000001,3.14159265358979]`},
		{"string escaping", `{"emoji":"🎭€","ctrl":"\u0001\n\t"}`, `{"ctrl":"\u0001\n\t","emoji":"🎭€"}`},
		{"empty containers", `{"a":[],"o":{}}`, `{"a":[],"o":{}}`},
		{"null and booleans", `[null,true,false]`, `[null,true,false]`},
		{"large integer", `[123456789012345678901]`, `[123456789012345680000]`},
		{"smallest positive double", `[5e-324]`, `[5e-324]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalizeJSON([]byte(tc.input))
			if err != nil {
				t.Fatalf("canonicalizeJSON: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("canonical form = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestCanonicalJSONRejects(t *testing.T) {
	for _, input := range []string{
		`{"a":1,"a":2}`,
		`{"a":}`,
		`not json`,
		`[1,2] trailing`,
		`{"n":1e400}`,
		`[1,2`,
	} {
		if _, err := canonicalizeJSON([]byte(input)); err == nil {
			t.Errorf("canonicalizeJSON(%q) succeeded, want rejection", input)
		}
	}
	if _, err := RequestDigest([]byte(`{"a":1,"a":2}`)); !errors.Is(err, ErrPayloadRejected) {
		t.Errorf("RequestDigest error = %v, want ErrPayloadRejected", err)
	}
}

func TestRequestDigestCanonicalEquivalence(t *testing.T) {
	digestA, err := RequestDigest([]byte(`{"taskSpec":{"b":2,"a":1},"n":1.0}`))
	if err != nil {
		t.Fatalf("RequestDigest: %v", err)
	}
	digestB, err := RequestDigest([]byte(`{"n":1,"taskSpec":{"a":1,"b":2}}`))
	if err != nil {
		t.Fatalf("RequestDigest: %v", err)
	}
	if digestA != digestB {
		t.Errorf("canonical digests differ: %q vs %q", digestA, digestB)
	}
	if !strings.HasPrefix(digestA, "sha256:") || len(digestA) != len("sha256:")+64 {
		t.Errorf("digest format = %q", digestA)
	}
}

func TestCreateTaskHappyPath(t *testing.T) {
	var recorded struct {
		method      string
		path        string
		contentType string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		recorded.method = r.Method
		recorded.path = r.URL.Path
		recorded.contentType = r.Header.Get("Content-Type")
		if r.Method != http.MethodPost || r.URL.Path != APIPrefix+"/tasks" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		envelope := decodeFakeEnvelope(t, body)
		if envelope.IdempotencyKey != "create-key-1" {
			t.Errorf("idempotencyKey = %q", envelope.IdempotencyKey)
		}
		verifyRequestDigest(t, envelope)
		var payload struct {
			RunID          string          `json:"runId"`
			TaskSpec       json.RawMessage `json:"taskSpec"`
			PolicySnapshot json.RawMessage `json:"policySnapshot"`
		}
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			t.Errorf("decode payload: %v", err)
		} else {
			if payload.RunID != "run-1" {
				t.Errorf("payload runId = %q", payload.RunID)
			}
			if len(payload.TaskSpec) == 0 || len(payload.PolicySnapshot) == 0 {
				t.Errorf("payload documents absent")
			}
		}
		writeFakeJSON(w, http.StatusCreated, fixtureSubmission())
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	submission, err := client.CreateTask(context.Background(), TaskCreateRequest{
		IdempotencyKey: "create-key-1",
		Payload: TaskCreatePayload{
			RunID:          "run-1",
			TaskSpec:       json.RawMessage(`{"taskId":"task-1"}`),
			PolicySnapshot: json.RawMessage(`{"policyDigest":"sha256:policy"}`),
		},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if submission.TaskID != "task-1" || submission.RunID != "run-1" || submission.AdapterID != "fixture-adapter" {
		t.Errorf("submission = %+v", submission)
	}
	if submission.State.State != "READY" {
		t.Errorf("submission state = %+v", submission.State)
	}
	if submission.AuthorityNamespaceId != fixtureNamespace {
		t.Errorf("authorityNamespaceId = %+v", submission.AuthorityNamespaceId)
	}
	if submission.Replayed {
		t.Errorf("first submission reported as replay")
	}
	if recorded.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", recorded.contentType)
	}
}

func TestGetTaskHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		if r.Method != http.MethodGet || r.URL.Path != APIPrefix+"/tasks/task-1" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		writeFakeJSON(w, http.StatusOK, fixtureTaskView())
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	view, err := client.GetTask(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if view.TaskID != "task-1" || view.Title != "fixture task" || view.LatestRunID != "run-1" {
		t.Errorf("view = %+v", view)
	}
	if len(view.Runs) != 1 || view.Runs[0].RunID != "run-1" || view.Runs[0].State != "READY" {
		t.Errorf("runs = %+v", view.Runs)
	}
}

func TestCancelTaskHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != APIPrefix+"/tasks/task-1/cancel" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		envelope := decodeFakeEnvelope(t, body)
		verifyRequestDigest(t, envelope)
		var payload struct {
			Actor  string `json:"actor"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			t.Errorf("decode payload: %v", err)
		} else if payload.Actor != fixturePrincipal || payload.Reason != "stop now" {
			t.Errorf("payload = %+v", payload)
		}
		writeFakeJSON(w, http.StatusOK, fixtureCancellation())
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	cancellation, err := client.CancelTask(context.Background(), "task-1", TaskCancelRequest{
		IdempotencyKey: "cancel-key-1",
		Payload:        TaskCancelPayload{Actor: fixturePrincipal, Reason: "stop now"},
	})
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if cancellation.TaskID != "task-1" || cancellation.RunID != "run-1" {
		t.Errorf("cancellation = %+v", cancellation)
	}
	if cancellation.State != "BLOCKED" || cancellation.TerminalReason != "aborted-by-operator" {
		t.Errorf("cancellation terminal state = %+v", cancellation)
	}
	if cancellation.Sequence != 4 {
		t.Errorf("cancellation sequence = %d", cancellation.Sequence)
	}
}

func TestApproveRunHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != APIPrefix+"/runs/run-1/approval" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		envelope := decodeFakeEnvelope(t, body)
		verifyRequestDigest(t, envelope)
		var payload struct {
			Gate  string `json:"gate"`
			Actor string `json:"actor"`
		}
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			t.Errorf("decode payload: %v", err)
		} else if payload.Gate != "plan" || payload.Actor != fixturePrincipal {
			t.Errorf("payload = %+v", payload)
		}
		writeFakeJSON(w, http.StatusCreated, fixtureApproval())
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	record, err := client.ApproveRun(context.Background(), "run-1", RunApprovalRequest{
		IdempotencyKey: "approval-key-1",
		Payload:        RunApprovalPayload{Gate: GatePlan, Actor: fixturePrincipal},
	})
	if err != nil {
		t.Fatalf("ApproveRun: %v", err)
	}
	if record.RecordID != "approval-1" || record.Gate != GatePlan || record.Outcome != "approved" {
		t.Errorf("record = %+v", record)
	}
	if record.Source.Type != "human" || record.Source.ID != fixturePrincipal {
		t.Errorf("record source = %+v", record.Source)
	}
	if record.Binding.SpecDigest != "sha256:spec" || record.Binding.BaseSHA != "base-sha" {
		t.Errorf("record binding = %+v", record.Binding)
	}
	if record.Replayed {
		t.Errorf("first approval reported as replay")
	}
}

func TestGetRunStatusHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		if r.Method != http.MethodGet || r.URL.Path != APIPrefix+"/runs/run-1/status" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		writeFakeJSON(w, http.StatusOK, fixtureRunState())
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	state, err := client.GetRunStatus(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetRunStatus: %v", err)
	}
	if state.RunID != "run-1" || state.State != "RUNNING" || state.Sequence != 5 {
		t.Errorf("state = %+v", state)
	}
	if state.CurrentAttemptID != "attempt-1" {
		t.Errorf("state attempt = %+v", state)
	}
}

func TestPollEventsHappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		if r.Method != http.MethodGet || r.URL.Path != APIPrefix+"/events/poll" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("cursor"); got != "2" {
			t.Errorf("cursor = %q, want 2", got)
		}
		if got := r.URL.Query().Get("limit"); got != "5" {
			t.Errorf("limit = %q, want 5", got)
		}
		page := EventPage{
			APIVersion:           "marshal.dev/v1alpha1",
			Kind:                 "EventPage",
			AuthorityNamespaceId: fixtureNamespace,
			Scope:                fixtureScope,
			Events:               []EventProjection{fixtureEvent(3, "evt-3"), fixtureEvent(4, "evt-4")},
			NextCursor:           4,
			SnapshotDigest:       "sha256:snap",
		}
		writeFakeJSON(w, http.StatusOK, page)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	page, err := client.PollEvents(context.Background(), 2, 5)
	if err != nil {
		t.Fatalf("PollEvents: %v", err)
	}
	if page.NextCursor != 4 || page.SnapshotDigest != "sha256:snap" {
		t.Errorf("page = %+v", page)
	}
	if len(page.Events) != 2 || page.Events[0].EventID != "evt-3" || page.Events[1].LedgerSequence != 4 {
		t.Errorf("events = %+v", page.Events)
	}
}

func TestPollEventsTypedResync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		writeFakeJSON(w, http.StatusConflict, EventResync{
			APIVersion:           "marshal.dev/v1alpha1",
			Kind:                 "EventResync",
			AuthorityNamespaceId: fixtureNamespace,
			Scope:                fixtureScope,
			Reason:               "cursor-expired",
			StartSequence:        1,
			SnapshotDigest:       "sha256:snap-resync",
		})
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.PollEvents(context.Background(), 99, 0)
	resync, ok := AsResyncRequired(err)
	if !ok {
		t.Fatalf("error = %v, want ResyncRequiredError", err)
	}
	if resync.Directive.Reason != "cursor-expired" || resync.Directive.StartSequence != 1 {
		t.Errorf("directive = %+v", resync.Directive)
	}
	if resync.Directive.SnapshotDigest != "sha256:snap-resync" {
		t.Errorf("snapshot digest = %q", resync.Directive.SnapshotDigest)
	}
}

func TestIdempotentReplayAndConflict(t *testing.T) {
	type storedRecord struct {
		digest string
		result []byte
	}
	var mu sync.Mutex
	records := map[string]storedRecord{}
	submission := fixtureSubmission()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		envelope := decodeFakeEnvelope(t, body)
		verifyRequestDigest(t, envelope)
		mu.Lock()
		defer mu.Unlock()
		if existing, ok := records[envelope.IdempotencyKey]; ok {
			if existing.digest != envelope.RequestDigest {
				writeFakeJSON(w, http.StatusConflict, fakeError(CodeIdempotencyConflict,
					"idempotency-key-conflict",
					"the idempotency key already belongs to a different request digest"))
				return
			}
			writeRawJSON(w, http.StatusOK, existing.result)
			return
		}
		result, err := json.Marshal(submission)
		if err != nil {
			t.Errorf("encode result: %v", err)
			return
		}
		records[envelope.IdempotencyKey] = storedRecord{digest: envelope.RequestDigest, result: result}
		writeRawJSON(w, http.StatusCreated, result)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	requestA := TaskCreateRequest{
		IdempotencyKey: "key-1",
		Payload: TaskCreatePayload{
			RunID:          "run-1",
			TaskSpec:       json.RawMessage(`{"taskId":"task-1"}`),
			PolicySnapshot: json.RawMessage(`{"policyDigest":"sha256:policy"}`),
		},
	}
	first, err := client.CreateTask(context.Background(), requestA)
	if err != nil {
		t.Fatalf("first submission: %v", err)
	}
	if first.Replayed {
		t.Errorf("first submission reported as replay")
	}
	second, err := client.CreateTask(context.Background(), requestA)
	if err != nil {
		t.Fatalf("replayed submission: %v", err)
	}
	if !second.Replayed {
		t.Errorf("identical quadruple replay not reported")
	}
	if second.TaskID != first.TaskID || second.RunID != first.RunID {
		t.Errorf("replay result differs: %+v vs %+v", first, second)
	}

	requestB := requestA
	requestB.Payload = TaskCreatePayload{
		RunID:          "run-1",
		TaskSpec:       json.RawMessage(`{"taskId":"task-OTHER"}`),
		PolicySnapshot: json.RawMessage(`{"policyDigest":"sha256:policy"}`),
	}
	_, err = client.CreateTask(context.Background(), requestB)
	if err == nil {
		t.Fatalf("same key with different digest succeeded, want conflict")
	}
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Errorf("error = %v, want ErrIdempotencyConflict", err)
	}
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("error = %v, want APIError", err)
	}
	if apiErr.Code != CodeIdempotencyConflict || apiErr.Reason != "idempotency-key-conflict" || apiErr.Status != http.StatusConflict {
		t.Errorf("api error = %+v", apiErr)
	}
}

func TestApprovalReplayMerged(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyIdentity(t, w, r)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		envelope := decodeFakeEnvelope(t, body)
		verifyRequestDigest(t, envelope)
		call := atomic.AddInt32(&calls, 1)
		if call == 1 {
			writeFakeJSON(w, http.StatusCreated, fixtureApproval())
			return
		}
		writeFakeJSON(w, http.StatusOK, fixtureApproval())
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	request := RunApprovalRequest{
		IdempotencyKey: "approval-key-2",
		Payload:        RunApprovalPayload{Gate: GatePublish, Actor: fixturePrincipal},
	}
	first, err := client.ApproveRun(context.Background(), "run-1", request)
	if err != nil {
		t.Fatalf("first approval: %v", err)
	}
	if first.Replayed {
		t.Errorf("first approval reported as replay")
	}
	second, err := client.ApproveRun(context.Background(), "run-1", request)
	if err != nil {
		t.Fatalf("replayed approval: %v", err)
	}
	if !second.Replayed {
		t.Errorf("identical approval replay not reported")
	}
	if second.RecordID != first.RecordID {
		t.Errorf("replay record differs: %+v vs %+v", first, second)
	}
}

func TestAPIErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   ErrorCode
		reason string
		want   error
	}{
		{"invalid request", http.StatusBadRequest, CodeInvalidRequest, "unknown-member:foo", ErrInvalidRequest},
		{"missing identity", http.StatusBadRequest, CodeMissingIdentity, "missing-header:Marshal-Scope", ErrMissingIdentity},
		{"forbidden identity", http.StatusForbidden, CodeForbiddenIdentity, "forbidden-header:Marshal-Workload-Role", ErrForbiddenIdentity},
		{"scope mismatch", http.StatusBadRequest, CodeScopeMismatch, "scope-mismatch", ErrScopeMismatch},
		{"not found", http.StatusNotFound, CodeNotFound, "task-not-found", ErrNotFound},
		{"idempotency conflict", http.StatusConflict, CodeIdempotencyConflict, "idempotency-key-conflict", ErrIdempotencyConflict},
		{"invalid state", http.StatusConflict, CodeInvalidState, "run-terminal", ErrInvalidState},
		{"rejected", http.StatusUnprocessableEntity, CodeRejected, "task-spec-invalid", ErrRejected},
		{"internal", http.StatusInternalServerError, CodeInternal, "internal", ErrInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				verifyIdentity(t, w, r)
				writeFakeJSON(w, tc.status, fakeError(tc.code, tc.reason, "fixture rejection"))
			}))
			defer server.Close()

			client := newTestClient(t, server.URL)
			_, err := client.GetTask(context.Background(), "task-1")
			if err == nil {
				t.Fatalf("request succeeded, want error")
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("errors.Is(%v, %v) = false", err, tc.want)
			}
			apiErr, ok := AsAPIError(err)
			if !ok {
				t.Fatalf("error = %v, want APIError", err)
			}
			if apiErr.Code != tc.code || apiErr.Reason != tc.reason || apiErr.Status != tc.status {
				t.Errorf("api error = %+v", apiErr)
			}
			if apiErr.RequestID != "req-err-1" {
				t.Errorf("requestId = %q", apiErr.RequestID)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error text swallows the body reason: %q", err.Error())
			}
		})
	}
}

func TestProtocolVersionRejectionTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(HeaderProtocolVersion); got != DefaultProtocolVersion {
			writeFakeJSON(w, http.StatusBadRequest, fakeError(CodeInvalidRequest,
				"protocol-version-mismatch",
				"the request protocol version is not part of this protocol family"))
			return
		}
		verifyIdentity(t, w, r)
		writeFakeJSON(w, http.StatusOK, fixtureTaskView())
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:         server.URL,
		Principal:       fixturePrincipal,
		Scope:           fixtureScope,
		ProtocolVersion: "marshal-public-api/v9",
	})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	_, err = client.GetTask(context.Background(), "task-1")
	if err == nil {
		t.Fatalf("request succeeded, want protocol version rejection")
	}
	if !errors.Is(err, ErrProtocolVersionRejected) || !IsProtocolVersionRejected(err) {
		t.Errorf("error = %v, want ErrProtocolVersionRejected", err)
	}
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("error = %v, want APIError", err)
	}
	if apiErr.Code != CodeInvalidRequest || apiErr.Reason != "protocol-version-mismatch" {
		t.Errorf("api error = %+v", apiErr)
	}
}

func TestAudienceRejectionTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(HeaderAudience); got != DefaultAudience {
			writeFakeJSON(w, http.StatusBadRequest, fakeError(CodeInvalidRequest,
				"audience-mismatch",
				"the request audience does not address the public-api Port"))
			return
		}
		verifyIdentity(t, w, r)
		writeFakeJSON(w, http.StatusOK, fixtureTaskView())
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:   server.URL,
		Principal: fixturePrincipal,
		Scope:     fixtureScope,
		Audience:  "wrong-audience",
	})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	_, err = client.GetTask(context.Background(), "task-1")
	if err == nil {
		t.Fatalf("request succeeded, want audience rejection")
	}
	if !errors.Is(err, ErrAudienceRejected) || !IsAudienceRejected(err) {
		t.Errorf("error = %v, want ErrAudienceRejected", err)
	}
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("error = %v, want APIError", err)
	}
	if apiErr.Code != CodeInvalidRequest || apiErr.Reason != "audience-mismatch" {
		t.Errorf("api error = %+v", apiErr)
	}
}

func TestTransportErrorTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	target := server.URL
	server.Close()

	client := newTestClient(t, target)
	_, err := client.GetTask(context.Background(), "task-1")
	if err == nil {
		t.Fatalf("request succeeded against a closed server")
	}
	if !IsTransportError(err) {
		t.Fatalf("error = %v, want TransportError", err)
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Errorf("transport error does not wrap the url.Error: %v", err)
	}
	if IsAPIError(err) {
		t.Errorf("transport error misclassified as API error: %v", err)
	}
}

func TestUnexpectedResponsePreservesBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "proxy exploded")
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	_, err := client.GetTask(context.Background(), "task-1")
	var unexpected *UnexpectedResponseError
	if !errors.As(err, &unexpected) {
		t.Fatalf("error = %v, want UnexpectedResponseError", err)
	}
	if unexpected.Status != http.StatusBadGateway || !strings.Contains(unexpected.Body, "proxy exploded") {
		t.Errorf("unexpected = %+v", unexpected)
	}
	if !strings.Contains(err.Error(), "proxy exploded") {
		t.Errorf("error text swallows the body: %q", err.Error())
	}
}

func TestClientRejectsInvalidIDsBeforeAnyRequest(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	ctx := context.Background()
	if _, err := client.GetTask(ctx, "invalid id"); !errors.Is(err, ErrInvalidID) {
		t.Errorf("GetTask error = %v, want ErrInvalidID", err)
	}
	if _, err := client.GetRunStatus(ctx, ""); !errors.Is(err, ErrInvalidID) {
		t.Errorf("GetRunStatus error = %v, want ErrInvalidID", err)
	}
	if _, err := client.CancelTask(ctx, "bad id", TaskCancelRequest{IdempotencyKey: "k", Payload: TaskCancelPayload{Actor: "a", Reason: "r"}}); !errors.Is(err, ErrInvalidID) {
		t.Errorf("CancelTask error = %v, want ErrInvalidID", err)
	}
	if _, err := client.ApproveRun(ctx, "bad id", RunApprovalRequest{IdempotencyKey: "k", Payload: RunApprovalPayload{Gate: GatePlan, Actor: "a"}}); !errors.Is(err, ErrInvalidID) {
		t.Errorf("ApproveRun error = %v, want ErrInvalidID", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("invalid IDs reached the server %d times", got)
	}
}

func TestClientSidePayloadValidation(t *testing.T) {
	client := newTestClient(t, "http://127.0.0.1:1")
	ctx := context.Background()
	if _, err := client.CreateTask(ctx, TaskCreateRequest{IdempotencyKey: "k", Payload: TaskCreatePayload{}}); err == nil {
		t.Errorf("empty create payload accepted")
	}
	if _, err := client.CreateTask(ctx, TaskCreateRequest{Payload: TaskCreatePayload{RunID: "run-1", TaskSpec: json.RawMessage("{}"), PolicySnapshot: json.RawMessage("{}")}}); err == nil {
		t.Errorf("empty idempotency key accepted")
	}
	if _, err := client.ApproveRun(ctx, "run-1", RunApprovalRequest{IdempotencyKey: "k", Payload: RunApprovalPayload{Gate: "merge", Actor: "a"}}); err == nil {
		t.Errorf("invalid approval gate accepted")
	}
	if _, err := client.CancelTask(ctx, "task-1", TaskCancelRequest{IdempotencyKey: "k", Payload: TaskCancelPayload{Actor: "a"}}); err == nil {
		t.Errorf("cancel without reason accepted")
	}
}

func TestDeadlineHeaderDerivation(t *testing.T) {
	var recorded string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorded = r.Header.Get(HeaderDeadline)
		writeFakeJSON(w, http.StatusOK, fixtureTaskView())
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(5*time.Second))
	defer cancel()
	if _, err := client.GetTask(ctx, "task-1"); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	want := ctxDeadline(ctx).UTC().Truncate(time.Second)
	parsed, err := time.Parse(time.RFC3339, recorded)
	if err != nil {
		t.Fatalf("deadline header %q: %v", recorded, err)
	}
	if !parsed.Equal(want) {
		t.Errorf("deadline header = %v, want the context deadline %v", parsed, want)
	}

	if _, err := client.GetTask(context.Background(), "task-1"); err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	parsed, err = time.Parse(time.RFC3339, recorded)
	if err != nil {
		t.Fatalf("deadline header %q: %v", recorded, err)
	}
	if !parsed.After(time.Now().Add(-5*time.Second)) || !parsed.Before(time.Now().Add(DefaultRequestTimeout+5*time.Second)) {
		t.Errorf("default deadline header = %v, want within the default horizon", parsed)
	}
}

// ctxDeadline exposes the context deadline for the assertion above.
func ctxDeadline(ctx context.Context) time.Time {
	deadline, _ := ctx.Deadline()
	return deadline
}
