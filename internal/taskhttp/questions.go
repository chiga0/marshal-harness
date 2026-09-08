package taskhttp

import (
	"context"
	"net/http"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

func (h *Handler) serveTaskQuestions(w http.ResponseWriter, r *http.Request, ctx context.Context, id string, parts []string) bool {
	listing := len(parts) == 4 && parts[3] == "questions" && r.Method == http.MethodGet
	answering := len(parts) == 6 && parts[3] == "questions" && domain.ValidateID(parts[4]) == nil && parts[5] == "answers" && r.Method == http.MethodPost
	if !listing && !answering {
		return false
	}
	port, ok := h.config.Application.(application.TaskQuestionPort)
	if !ok {
		writeError(w, http.StatusNotFound, "capability-not-supported")
		return true
	}
	var result any
	var err error
	if listing {
		result, err = port.ReadTaskQuestions(ctx, id)
	} else {
		var request application.AnswerTaskQuestionRequest
		if readJSON(w, r, &request) != nil || request.ExpectedRevision < 1 || request.QuestionRevision != 1 || goal.ValidateTaskQuestionDigest(request.PreviewDigest) != nil || (goal.TaskSlotValue{SlotID: "answer", Value: request.Answer}).Validate() != nil {
			writeError(w, http.StatusBadRequest, "invalid-request")
			return true
		}
		key, valid := requestKey(r)
		if !valid {
			writeError(w, http.StatusBadRequest, "invalid-idempotency-key")
			return true
		}
		request.TaskID, request.QuestionID, request.IdempotencyKey = id, parts[4], key
		err = h.config.Mutation(ctx, func(call context.Context) error {
			var e error
			result, e = port.AnswerTaskQuestion(call, request)
			return e
		})
	}
	if err != nil {
		writeApplicationError(w, err)
		return true
	}
	if h.config.Recheck(ctx) != nil {
		writeError(w, http.StatusServiceUnavailable, "production-owner-not-current")
		return true
	}
	writeJSON(w, http.StatusOK, result)
	return true
}
