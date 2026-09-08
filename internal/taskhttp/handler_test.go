package taskhttp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
)

// Explicit transport fixture, never a substitute for the real held-session
// and RB1 tests. No Worker or model is invoked by these boundary cases.
type taskPortFixture struct {
	creates, approves int
	last              application.ApproveTaskRequest
	failure           error
}

func (p *taskPortFixture) CreateTask(_ context.Context, r application.CreateTaskRequest) (application.TaskProjection, error) {
	p.creates++
	return application.TaskProjection{ID: "task-one", Request: r.Submission}, p.failure
}
func (p *taskPortFixture) ReadTask(context.Context, string) (application.TaskProjection, error) {
	return application.TaskProjection{ID: "task-one", Status: "review-pending"}, p.failure
}
func (p *taskPortFixture) ListTasks(context.Context, application.TaskListRequest) (application.TaskPage, error) {
	return application.TaskPage{Items: []application.TaskProjection{}}, p.failure
}
func (p *taskPortFixture) ApproveTask(_ context.Context, r application.ApproveTaskRequest) (application.TaskProjection, error) {
	p.approves++
	p.last = r
	return application.TaskProjection{ID: r.TaskID, Status: "approved"}, p.failure
}

const testToken = "1111111111111111111111111111111111111111111111111111111111111111"

func taskTestHandler(t *testing.T, p *taskPortFixture) *Handler {
	t.Helper()
	h, err := NewHandler(HandlerConfig{Application: p, Host: "127.0.0.1:1234", Token: testToken, Recheck: func(context.Context) error { return nil }, Mutation: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func taskTestRequest(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "request-one")
	return r
}

func TestTaskHTTPRejectsBeforeMutation(t *testing.T) {
	for _, name := range []string{"token", "host", "origin", "duplicate-auth", "duplicate-key", "authority-input", "duplicate-json", "encoded-path", "oversized", "missing-key"} {
		t.Run(name, func(t *testing.T) {
			p := &taskPortFixture{}
			h := taskTestHandler(t, p)
			body := `{"template":"order-quote/v1","intent":"报价"}`
			r := taskTestRequest(http.MethodPost, "/v1/tasks", body)
			switch name {
			case "token":
				r.Header.Set("Authorization", "Bearer invalid")
			case "host":
				r.Host = "attacker.invalid"
			case "origin":
				r.Header.Set("Origin", "https://attacker.invalid")
			case "duplicate-auth":
				r.Header.Add("Authorization", "Bearer "+testToken)
			case "duplicate-key":
				r.Header.Add("Idempotency-Key", "another")
			case "missing-key":
				r.Header.Del("Idempotency-Key")
			case "authority-input":
				r = taskTestRequest(http.MethodPost, "/v1/tasks", `{"template":"order-quote/v1","intent":"报价","inputs":{"policy":{}}}`)
			case "duplicate-json":
				r = taskTestRequest(http.MethodPost, "/v1/tasks", `{"template":"order-quote/v1","intent":"报价","intent":"替换"}`)
			case "encoded-path":
				r.URL.RawPath = "/v1/%74asks"
			case "oversized":
				r = taskTestRequest(http.MethodPost, "/v1/tasks", `{"context":{"text":"`+strings.Repeat("x", maxBody)+`"}}`)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code < 400 || p.creates != 0 || p.approves != 0 {
				t.Fatalf("rejected request reached app: %d", w.Code)
			}
		})
	}
}
func TestTaskHTTPThinPortAndTruthfulCapabilities(t *testing.T) {
	p := &taskPortFixture{}
	h := taskTestHandler(t, p)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, taskTestRequest(http.MethodPost, "/v1/tasks", `{"template":"order-quote/v1","intent":"报价"}`))
	if w.Code != 201 || p.creates != 1 {
		t.Fatalf("create: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, taskTestRequest(http.MethodPost, "/v1/tasks/task-one/approve", `{"expectedRevision":1,"previewDigest":"sha256:exact"}`))
	if w.Code != 202 || p.last.TaskID != "task-one" || p.last.IdempotencyKey != "request-one" || p.last.ExpectedRevision != 1 {
		t.Fatalf("confirm: %d %+v", w.Code, p.last)
	}
	for _, path := range []string{"/v1/tasks", "/v1/tasks/task-one", "/v1/tasks/task-one/graph", "/v1/tasks/task-one/workers", "/v1/capabilities"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, taskTestRequest(http.MethodGet, path, ""))
		if w.Code != 200 {
			t.Fatalf("query %s: %d", path, w.Code)
		}
		if path == "/v1/capabilities" && !bytes.Contains(w.Body.Bytes(), []byte(`"pending":["questions","answer","cancel","automatic-decision","artifact-download"]`)) {
			t.Fatal("unsupported capabilities hidden")
		}
	}
	for _, path := range []string{"/v1/tasks/task-one/cancel", "/v1/tasks/task-one/artifact"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, taskTestRequest(http.MethodPost, path, `{}`))
		if w.Code != 404 {
			t.Fatal("unimplemented action advertised")
		}
	}
	p.failure = application.NewError("confirm", application.ReasonAuthorityConflict)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, taskTestRequest(http.MethodPost, "/v1/tasks/task-one/approve", `{"expectedRevision":1}`))
	if w.Code != 409 {
		t.Fatal("conflict not preserved")
	}
	p.failure = errors.New("do-not-expose-host-path-or-provider-output")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, taskTestRequest(http.MethodGet, "/v1/tasks/task-one", ""))
	if w.Code != 503 || bytes.Contains(w.Body.Bytes(), []byte("do-not-expose")) {
		t.Fatal("unsafe error response")
	}
}
func TestTaskHTTPCurrentOwnerAndWriterLane(t *testing.T) {
	p := &taskPortFixture{}
	h := taskTestHandler(t, p)
	h.config.Mutation = func(context.Context, func(context.Context) error) error {
		return application.NewError("task", application.ReasonCapacityBusy)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, taskTestRequest(http.MethodPost, "/v1/tasks", `{"template":"order-quote/v1","intent":"报价"}`))
	if w.Code != 503 || p.creates != 0 {
		t.Fatal("busy writer did not short-circuit")
	}
	h.config.Recheck = func(context.Context) error { return errors.New("owner-lost") }
	w = httptest.NewRecorder()
	h.ServeHTTP(w, taskTestRequest(http.MethodGet, "/v1/tasks/task-one", ""))
	if w.Code != 503 {
		t.Fatal("stale owner exposed projection")
	}
}
