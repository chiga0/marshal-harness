package taskhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
)

type questionPortFixture struct {
	taskPortFixture
	answers    int
	lastAnswer application.AnswerTaskQuestionRequest
}

func (p *questionPortFixture) ReadTaskQuestions(context.Context, string) (application.TaskQuestions, error) {
	return application.TaskQuestions{TaskID: "task-one", Revision: 1, PreviewDigest: canonical.DigestBytes([]byte("preview")), Questions: []application.TaskQuestionView{}}, p.failure
}
func (p *questionPortFixture) AnswerTaskQuestion(_ context.Context, r application.AnswerTaskQuestionRequest) (application.TaskQuestionAnswerResult, error) {
	p.answers++
	p.lastAnswer = r
	return application.TaskQuestionAnswerResult{TaskProjection: application.TaskProjection{ID: r.TaskID, Revision: 2, PreviewDigest: canonical.DigestBytes([]byte("answer")), Status: "awaiting-confirmation"}, QuestionID: r.QuestionID, AcceptedRevision: 2, AcceptedPreviewDigest: canonical.DigestBytes([]byte("answer")), AnswerFactDigest: canonical.DigestBytes([]byte("answer"))}, p.failure
}
func questionHandler(t *testing.T, p *questionPortFixture) *Handler {
	t.Helper()
	h, err := NewHandler(HandlerConfig{Application: p, Host: "127.0.0.1:1234", Token: testToken, Recheck: func(context.Context) error { return nil }, Mutation: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func questionBody(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func validQuestionRequest() application.AnswerTaskQuestionRequest {
	return application.AnswerTaskQuestionRequest{ExpectedRevision: 1, PreviewDigest: canonical.DigestBytes([]byte("preview")), QuestionRevision: 1, Answer: "developers"}
}

func TestTaskQuestionHTTPBoundaryAndWriterLane(t *testing.T) {
	for _, name := range []string{"revision-zero", "question-revision", "preview", "empty", "large", "nul", "authority-field", "duplicate", "object", "no-key", "busy", "owner"} {
		t.Run(name, func(t *testing.T) {
			p := &questionPortFixture{}
			h := questionHandler(t, p)
			v := validQuestionRequest()
			switch name {
			case "revision-zero":
				v.ExpectedRevision = 0
			case "question-revision":
				v.QuestionRevision = 2
			case "preview":
				v.PreviewDigest = "sha256:wrong"
			case "empty":
				v.Answer = " "
			case "large":
				v.Answer = strings.Repeat("a", goal.MaxTaskAnswerBytes+1)
			case "nul":
				v.Answer = "x\x00y"
			}
			body := questionBody(t, v)
			switch name {
			case "authority-field":
				body = strings.TrimSuffix(body, "}") + `,"oracle":"replace"}`
			case "duplicate":
				body = strings.TrimSuffix(body, "}") + `,"answer":"different"}`
			case "object":
				body = strings.Replace(body, `"answer":"developers"`, `"answer":{"value":"developers"}`, 1)
			}
			r := taskTestRequest(http.MethodPost, "/v1/tasks/task-one/questions/question-one/answers", body)
			switch name {
			case "no-key":
				r.Header.Del("Idempotency-Key")
			case "busy":
				h.config.Mutation = func(context.Context, func(context.Context) error) error {
					return application.NewError("fixture", application.ReasonCapacityBusy)
				}
			case "owner":
				h.config.Recheck = func(context.Context) error { return errors.New("lost") }
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code < 400 || p.answers != 0 {
				t.Fatalf("invalid request reached writer: %d %d", w.Code, p.answers)
			}
		})
	}
}
func TestTaskQuestionHTTPExactTransportAndNoRouteAliases(t *testing.T) {
	p := &questionPortFixture{}
	h := questionHandler(t, p)
	mutations := 0
	h.config.Mutation = func(ctx context.Context, fn func(context.Context) error) error { mutations++; return fn(ctx) }
	for _, path := range []string{"/v1/capabilities", "/v1/tasks/task-one/questions"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, taskTestRequest(http.MethodGet, path, ""))
		if w.Code != 200 {
			t.Fatal(path, w.Code)
		}
		if path == "/v1/capabilities" && !strings.Contains(w.Body.String(), `"questions","answer"`) {
			t.Fatal("missing caps")
		}
	}
	if mutations != 0 {
		t.Fatal("read mutated")
	}
	body := validQuestionRequest()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, taskTestRequest(http.MethodPost, "/v1/tasks/task-one/questions/question-one/answers", questionBody(t, body)))
	if w.Code != 200 || mutations != 1 || p.answers != 1 || p.lastAnswer.TaskID != "task-one" || p.lastAnswer.QuestionID != "question-one" || p.lastAnswer.IdempotencyKey != "request-one" || p.lastAnswer.Answer != body.Answer || p.lastAnswer.PreviewDigest != body.PreviewDigest {
		t.Fatal("exact command changed", w.Code, p.lastAnswer)
	}
	var result application.TaskQuestionAnswerResult
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ID != "task-one" || result.Revision != 2 || result.AcceptedRevision != 2 {
		t.Fatal("response not top-level Task and receipt")
	}
	for _, path := range []string{"/v1/tasks/task-one/graph/extra", "/v1/tasks/task-one/workers/extra", "/v1/tasks/task-one/questions/question-one", "/v1/tasks/task-one/questions/question-one/answers/extra"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, taskTestRequest(http.MethodGet, path, ""))
		if w.Code != 404 {
			t.Fatal("route alias", path, w.Code)
		}
	}
	p.failure = application.NewError("answer", application.ReasonAuthorityConflict)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, taskTestRequest(http.MethodPost, "/v1/tasks/task-one/questions/question-one/answers", questionBody(t, body)))
	if w.Code != 409 {
		t.Fatal("CAS failure lost")
	}
}
