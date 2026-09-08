//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/taskhttp"
)

// The application is an explicit lane fixture. This test proves that the
// injected Task HTTP adapter joins the existing real router lane, not durable
// Task semantics or a complete CLI/listener/Agent business execution.
type taskLaneFixture struct {
	creates int
	check   func()
	failure error
}

func (p *taskLaneFixture) CreateTask(context.Context, application.CreateTaskRequest) (application.TaskProjection, error) {
	p.creates++
	if p.check != nil {
		p.check()
	}
	return application.TaskProjection{ID: "task-lane"}, p.failure
}
func (p *taskLaneFixture) ReadTask(context.Context, string) (application.TaskProjection, error) {
	return application.TaskProjection{ID: "task-lane"}, nil
}
func (p *taskLaneFixture) ListTasks(context.Context, application.TaskListRequest) (application.TaskPage, error) {
	return application.TaskPage{}, nil
}
func (p *taskLaneFixture) ApproveTask(context.Context, application.ApproveTaskRequest) (application.TaskProjection, error) {
	panic("lane fixture does not approve")
}

func TestTaskHTTPUsesExistingResidentWriterLane(t *testing.T) {
	legacy, delivery := testHTTPApplication()
	router, err := NewHTTPRouter(legacy, delivery)
	if err != nil {
		t.Fatal(err)
	}
	port := &taskLaneFixture{}
	token := strings.Repeat("1", 64)
	h, err := taskhttp.NewHandler(taskhttp.HandlerConfig{Application: port, Host: "127.0.0.1:1234", Token: token, Recheck: func(context.Context) error { return nil }, Mutation: router.WithAvailableMutation})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "lane-one")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if err := router.acquireMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	w := call(http.MethodPost, "/v1/tasks", `{"template":"order-quote/v1","intent":"报价"}`)
	if w.Code != 503 || port.creates != 0 {
		t.Fatal("Task bypassed existing writer")
	}
	if w := call(http.MethodGet, "/v1/tasks/task-lane", ""); w.Code != 200 {
		t.Fatal("read queued behind writer")
	}
	router.releaseMutation()
	port.check = func() {
		called, err := router.TryBackgroundMutation(context.Background(), func(context.Context) error { t.Error("nested writer admitted"); return nil })
		if called || err != nil {
			t.Fatalf("Task did not hold real writer lane: %t %v", called, err)
		}
	}
	w = call(http.MethodPost, "/v1/tasks", `{"template":"order-quote/v1","intent":"报价"}`)
	if w.Code != 201 || port.creates != 1 {
		t.Fatal("Task mutation did not execute")
	}
	port.failure = application.NewError("create-task", application.ReasonAuthorityConflict)
	w = call(http.MethodPost, "/v1/tasks", `{"template":"order-quote/v1","intent":"报价"}`)
	if w.Code != 409 || port.creates != 2 {
		t.Fatal("failed callback classification changed")
	}
	if called, err := router.TryBackgroundMutation(context.Background(), func(context.Context) error { return nil }); !called || err != nil {
		t.Fatal("Task left writer lane locked")
	}
}
