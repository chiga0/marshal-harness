// Package taskhttp is a bounded loopback input adapter. It owns no Task state
// and cannot approve/start a Run except through its injected application Port.
package taskhttp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

const maxBody = 32 << 10
const maxResponse = 2 << 20

type MutationLane func(context.Context, func(context.Context) error) error

type HandlerConfig struct {
	Application application.TaskDraftPort
	Host        string
	Token       string
	Recheck     func(context.Context) error
	Mutation    MutationLane
}

type Handler struct {
	config HandlerConfig
	slots  chan struct{}
}

func NewHandler(config HandlerConfig) (*Handler, error) {
	if config.Application == nil || config.Host == "" || len(config.Token) != 64 || config.Recheck == nil || config.Mutation == nil {
		return nil, errors.New("task HTTP: incomplete composition")
	}
	return &Handler{config: config, slots: make(chan struct{}, 8)}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Host != h.config.Host || r.URL.Host != "" || r.URL.Scheme != "" {
		writeError(w, http.StatusForbidden, "untrusted-origin")
		return
	}
	if origins := r.Header.Values("Origin"); len(origins) > 1 || len(origins) == 1 && origins[0] != "http://"+h.config.Host {
		writeError(w, http.StatusForbidden, "untrusted-origin")
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte("Bearer "+h.config.Token)) != 1 {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		writeError(w, http.StatusServiceUnavailable, "capacity-busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if h.config.Recheck(ctx) != nil {
		writeError(w, http.StatusServiceUnavailable, "production-owner-not-current")
		return
	}
	if r.URL.RawPath != "" || strings.Contains(r.URL.Path, "//") || strings.Contains(r.URL.Path, "..") {
		writeError(w, http.StatusBadRequest, "invalid-request")
		return
	}
	if r.URL.Path == "/v1/capabilities" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"profile": "task-draft/v1", "supported": []string{"create", "query", "confirm", "graph", "workers"}, "pending": []string{"cancel", "automatic-decision", "artifact-download"}})
		return
	}
	var result any
	status := http.StatusOK
	var err error
	if r.URL.Path == "/v1/tasks" {
		switch r.Method {
		case http.MethodPost:
			var submission goal.TaskSubmission
			if r.URL.RawQuery != "" || readJSON(w, r, &submission) != nil {
				writeError(w, http.StatusBadRequest, "invalid-request")
				return
			}
			key, ok := requestKey(r)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid-idempotency-key")
				return
			}
			err = h.config.Mutation(ctx, func(call context.Context) error {
				var e error
				result, e = h.config.Application.CreateTask(call, application.CreateTaskRequest{IdempotencyKey: key, Submission: submission})
				return e
			})
			status = http.StatusCreated
		case http.MethodGet:
			query, e := url.ParseQuery(r.URL.RawQuery)
			if e != nil || !validListQuery(query) {
				writeError(w, http.StatusBadRequest, "invalid-request")
				return
			}
			limit := 10
			if query.Get("limit") != "" {
				limit, e = strconv.Atoi(query.Get("limit"))
				if e != nil {
					writeError(w, http.StatusBadRequest, "invalid-request")
					return
				}
			}
			result, err = h.config.Application.ListTasks(ctx, application.TaskListRequest{After: query.Get("after"), Limit: limit})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method-not-allowed")
			return
		}
	} else {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if len(parts) < 3 || len(parts) > 4 || parts[0] != "v1" || parts[1] != "tasks" || domain.ValidateID(parts[2]) != nil || r.URL.RawQuery != "" {
			writeError(w, http.StatusNotFound, "not-found")
			return
		}
		id := parts[2]
		if len(parts) == 4 && parts[3] == "approve" && r.Method == http.MethodPost {
			var request application.ApproveTaskRequest
			if readJSON(w, r, &request) != nil {
				writeError(w, http.StatusBadRequest, "invalid-request")
				return
			}
			key, ok := requestKey(r)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid-idempotency-key")
				return
			}
			request.TaskID, request.IdempotencyKey = id, key
			err = h.config.Mutation(ctx, func(call context.Context) error {
				var e error
				result, e = h.config.Application.ApproveTask(call, request)
				return e
			})
			status = http.StatusAccepted
		} else if r.Method == http.MethodGet && (len(parts) == 3 || parts[3] == "graph" || parts[3] == "workers") {
			var projection application.TaskProjection
			projection, err = h.config.Application.ReadTask(ctx, id)
			result = projection
			if len(parts) == 4 && parts[3] == "graph" {
				result = map[string]any{"taskId": id, "status": projection.Status, "nodes": projection.Workers, "edges": projection.Edges}
			}
			if len(parts) == 4 && parts[3] == "workers" {
				result = map[string]any{"taskId": id, "workers": projection.Workers}
			}
		} else {
			writeError(w, http.StatusNotFound, "capability-not-supported")
			return
		}
	}
	if err != nil {
		writeApplicationError(w, err)
		return
	}
	// Lost ownership after mutation is unknown to this connection; the caller
	// can recover the exact same operation through its durable idempotency key.
	if h.config.Recheck(ctx) != nil {
		writeError(w, http.StatusServiceUnavailable, "production-owner-not-current")
		return
	}
	writeJSON(w, status, result)
}

func requestKey(r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	return r.Header.Get("Idempotency-Key"), len(values) == 1 && len(values[0]) <= 128 && domain.ValidateID(values[0]) == nil
}

func validListQuery(query url.Values) bool {
	for key, values := range query {
		if key != "after" && key != "limit" || len(values) != 1 {
			return false
		}
	}
	return true
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || r.Header.Get("Content-Encoding") != "" {
		return errors.New("invalid JSON request")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		return err
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > maxResponse {
		writeError(w, http.StatusServiceUnavailable, "response-unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(raw, '\n'))
}
func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}
func writeApplicationError(w http.ResponseWriter, err error) {
	var typed *application.Error
	if !errors.As(err, &typed) {
		writeError(w, http.StatusServiceUnavailable, "operation-unavailable")
		return
	}
	status := http.StatusServiceUnavailable
	switch typed.Reason {
	case application.ReasonInvalidRequest:
		status = http.StatusBadRequest
	case application.ReasonTaskNotFound:
		status = http.StatusNotFound
	case application.ReasonAuthorityConflict:
		status = http.StatusConflict
	case application.ReasonTaskConfirmationExpired:
		status = http.StatusGone
	}
	writeError(w, status, string(typed.Reason))
}
