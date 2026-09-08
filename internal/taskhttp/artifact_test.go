package taskhttp

import (
	"bytes"
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

type artifactPortFixture struct {
	taskPortFixture
	artifact application.TaskArtifact
	reads    int
}

func (p *artifactPortFixture) ReadTaskArtifact(context.Context, string) (application.TaskArtifact, error) {
	p.reads++
	return p.artifact, p.failure
}

func TestTaskHTTPArtifactCurrentOwnerAndBoundedExactBytes(t *testing.T) {
	d := canonical.DigestBytes([]byte("fixture"))
	content := []byte("transport fixture, not a genuine ZIP or acceptance proof")
	manifest := goal.TaskDelivery{GoalID: "task-one", OutcomeFactDigest: d, PlanFactDigest: d, IntegrationRunID: "run-integration", IntegrationBaseSHA: strings.Repeat("a", 40), CandidateDigests: []string{d, d, d}, PatchDigests: []string{d, d, d}, DecisionDigests: []string{d, d, d}, ContentDigest: canonical.DigestBytes(content), ContentBytes: int64(len(content)), MediaType: "application/zip", FactDigest: d}
	for _, name := range []string{"quote_api.py", "quote_client.py", "quote_delivery.json"} {
		manifest.Files = append(manifest.Files, goal.TaskDeliveryFile{Path: name, SHA256: d, Bytes: 1})
	}
	for _, mode := range []string{"valid", "not-ready", "not-found", "content-drift", "wrong-task", "oversized", "owner-before", "owner-after", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			p := &artifactPortFixture{artifact: application.TaskArtifact{Manifest: manifest, Content: content}}
			checks := 0
			want := http.StatusOK
			switch mode {
			case "not-ready":
				p.failure = application.NewError("fixture", application.ReasonTaskArtifactNotReady)
				want = 409
			case "not-found":
				p.failure = application.NewError("fixture", application.ReasonTaskNotFound)
				want = 404
			case "content-drift":
				p.artifact.Content = []byte("changed")
				want = 503
			case "wrong-task":
				p.artifact.Manifest.GoalID = "other"
				want = 503
			case "oversized":
				p.artifact.Manifest.ContentBytes = goal.MaxTaskDeliveryBytes + 1
				want = 503
			case "owner-before", "owner-after":
				want = 503
			case "unauthorized":
				want = 401
			}
			h, err := NewHandler(HandlerConfig{Application: p, Host: "127.0.0.1:1234", Token: testToken, AutomaticDecision: true, Recheck: func(context.Context) error {
				checks++
				if mode == "owner-before" || mode == "owner-after" && checks == 2 {
					return errors.New("lost owner")
				}
				return nil
			}, Mutation: func(context.Context, func(context.Context) error) error { t.Fatal("GET used writer"); return nil }})
			if err != nil {
				t.Fatal(err)
			}
			r := taskTestRequest(http.MethodGet, "/v1/tasks/task-one/artifact", "")
			if mode == "unauthorized" {
				r.Header.Del("Authorization")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("got %d want %d: %s", w.Code, want, w.Body.String())
			}
			if mode == "valid" && (w.Header().Get("Content-Type") != "application/zip" || !bytes.Equal(w.Body.Bytes(), content)) {
				t.Fatal("not exact downloadable bytes")
			}
			if (mode == "owner-before" || mode == "unauthorized") && p.reads != 0 {
				t.Fatal("unauthorized read")
			}
			if mode == "valid" {
				w = httptest.NewRecorder()
				h.ServeHTTP(w, taskTestRequest(http.MethodGet, "/v1/capabilities", ""))
				var capabilities struct {
					Supported []string `json:"supported"`
				}
				if json.Unmarshal(w.Body.Bytes(), &capabilities) != nil || !strings.Contains(w.Body.String(), "artifact-download") {
					t.Fatal("capability hidden")
				}
			}
		})
	}
}
