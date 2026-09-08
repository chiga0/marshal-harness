package taskhttp

import (
	"context"
	"github.com/chiga0/marshal-harness/internal/application"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type cancellingPort struct {
	taskPortFixture
	calls   int
	request application.CancelTaskRequest
}

func (p *cancellingPort) CancelTask(_ context.Context, r application.CancelTaskRequest) (application.TaskProjection, error) {
	p.calls++
	p.request = r
	return application.TaskProjection{ID: r.TaskID, Status: "cancelling", Revision: r.ExpectedRevision + 1, CancellationRequested: true}, p.failure
}

func TestTaskHTTPCancelUsesAuthenticatedRevisionAndWriter(t *testing.T) {
	p := &cancellingPort{}
	mutations := 0
	h, err := NewHandler(HandlerConfig{Application: p, Host: "127.0.0.1:1234", Token: testToken, Recheck: func(context.Context) error { return nil }, Mutation: func(ctx context.Context, fn func(context.Context) error) error { mutations++; return fn(ctx) }})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `{"expectedRevision":0}`, `{"expectedRevision":2,"pid":123}`, `{"expectedRevision":2.1}`, `{"expectedRevision":9223372036854775808}`} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, taskTestRequest(http.MethodPost, "/v1/tasks/task-one/cancel", body))
		if w.Code != 400 || p.calls != 0 || mutations != 0 {
			t.Fatalf("invalid cancel %s: %d", body, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, taskTestRequest(http.MethodPost, "/v1/tasks/task-one/cancel", `{"expectedRevision":2}`))
	if w.Code != 202 || p.calls != 1 || mutations != 1 || p.request.TaskID != "task-one" || p.request.ExpectedRevision != 2 || p.request.IdempotencyKey == "" || !strings.Contains(w.Body.String(), `"cancellationRequested":true`) {
		t.Fatalf("cancel: %d %+v %s", w.Code, p.request, w.Body.String())
	}
	p.failure = application.NewError("cancel-task", application.ReasonStopTooLate)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, taskTestRequest(http.MethodPost, "/v1/tasks/task-one/cancel", `{"expectedRevision":2}`))
	if w.Code != 409 {
		t.Fatal("too late not conflict")
	}
	r := taskTestRequest(http.MethodPost, "/v1/tasks/task-one/cancel", `{"expectedRevision":2}`)
	r.Header.Del("Authorization")
	before := p.calls
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || p.calls != before {
		t.Fatal("unauthenticated cancel reached port")
	}
}
