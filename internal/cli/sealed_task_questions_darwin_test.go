//go:build darwin && arm64

package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"github.com/chiga0/marshal-harness/internal/taskhttp"
)

// Exercise the exact concrete adapter handed to the production Task handler.
// The real Session is intentionally unclaimed: GET/POST must reach its owner
// rejection, not a missing-interface 404. The transport recheck below is only
// a boundary fixture; successful held-owner questions are tested in Session,
// not fabricated here or claimed as full CLI/server/Worker integration.
func TestSealedTaskQuestionsHTTPPortsReachOriginalOwner(t *testing.T) {
	a := &sealedRepositoryApplication{session: &productionruntime.RepositorySession{}}
	mutations := 0
	token := strings.Repeat("1", 64)
	h, err := taskhttp.NewHandler(taskhttp.HandlerConfig{
		Application: a, Host: "127.0.0.1:1234", Token: token,
		Recheck: func(context.Context) error { return nil },
		Mutation: func(ctx context.Context, fn func(context.Context) error) error {
			mutations++
			return fn(ctx)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Host = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "question-command")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := call(http.MethodGet, "/v1/capabilities", "")
	var capabilities struct{ Supported, Pending []string }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &capabilities) != nil || !slices.Contains(capabilities.Supported, "questions") || !slices.Contains(capabilities.Supported, "answer") || slices.Contains(capabilities.Pending, "questions") || slices.Contains(capabilities.Pending, "answer") {
		t.Fatal("sealed production adapter omitted its question ports")
	}
	w = call(http.MethodGet, "/v1/tasks/task-one/questions", "")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), string(application.ReasonOwnerUnavailable)) || mutations != 0 {
		t.Fatalf("query did not reach original Session owner: %d %s", w.Code, w.Body.String())
	}
	body, err := json.Marshal(application.AnswerTaskQuestionRequest{ExpectedRevision: 1, PreviewDigest: canonical.DigestBytes([]byte("frozen-preview")), QuestionRevision: 1, Answer: "developers"})
	if err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodPost, "/v1/tasks/task-one/questions/question-one/answers", string(body))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), string(application.ReasonOwnerUnavailable)) || mutations != 1 {
		t.Fatalf("answer did not use original Session and mutation lane: %d %s", w.Code, w.Body.String())
	}
}

func TestSealedTaskQuestionsReadAndCloseLockOrder(t *testing.T) {
	a := &sealedRepositoryApplication{session: &productionruntime.RepositorySession{}}
	a.mu.Lock()
	read := make(chan error, 1)
	go func() { _, err := a.ReadTaskQuestions(context.Background(), "task-one"); read <- err }()
	select {
	case err := <-read:
		if !application.HasReason(err, application.ReasonOwnerUnavailable) {
			a.mu.Unlock()
			t.Fatal("unclaimed read did not reach Session")
		}
	case <-time.After(5 * time.Second):
		a.mu.Unlock()
		t.Fatal("question read waited for mutation lock")
	}
	a.mu.Unlock()

	// Hold an existing reader while actual Close takes mu then waits for its
	// lifetime write guard. A queued answer must acquire mu first, not hold a
	// new status read lock while waiting for Close's mutation lock.
	a.statusMu.RLock()
	closed := make(chan error, 1)
	go func() { closed <- a.Close() }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for a.mu.TryLock() {
		a.mu.Unlock()
		select {
		case <-deadline.C:
			a.statusMu.RUnlock()
			t.Fatal("Close did not acquire mutation lock")
		default:
			runtime.Gosched()
		}
	}
	answered := make(chan error, 1)
	go func() {
		_, err := a.AnswerTaskQuestion(context.Background(), application.AnswerTaskQuestionRequest{})
		answered <- err
	}()
	a.statusMu.RUnlock()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close deadlocked with question writer")
	}
	select {
	case err := <-answered:
		if !application.HasReason(err, application.ReasonOwnerUnavailable) {
			t.Fatal("closed adapter forwarded an answer")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("answer retained a lifetime guard behind Close")
	}
	if _, err := a.ReadTaskQuestions(context.Background(), "task-one"); !application.HasReason(err, application.ReasonOwnerUnavailable) {
		t.Fatal("closed adapter forwarded a question read")
	}
}
