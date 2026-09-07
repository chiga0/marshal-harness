package goal

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

const TaskTemplateOrderQuote = "order-quote/v1"

// TaskSubmission is public intent, never an execution/authority document.
// B1 intentionally supports inline text and one explicitly configured template.
type TaskSubmission struct {
	Template string      `json:"template"`
	Intent   string      `json:"intent"`
	Context  TaskContext `json:"context"`
}

type TaskContext struct {
	Text string `json:"text"`
}

func (v TaskSubmission) Validate() error {
	if v.Template != TaskTemplateOrderQuote || strings.TrimSpace(v.Intent) == "" || len(v.Intent) > 4096 || len(v.Context.Text) > 16384 || strings.ContainsRune(v.Intent+v.Context.Text, 0) {
		return errors.New("task: unsupported or invalid submission")
	}
	return nil
}

// TaskDraft is an immutable proposal in the existing Goal authority namespace.
// It does not reserve a budget or grant creation/dispatch authority.
type TaskDraft struct {
	GoalID           string          `json:"goalId"`
	Revision         int64           `json:"revision"`
	RequestKeyDigest string          `json:"requestKeyDigest"`
	RequestDigest    string          `json:"requestDigest"`
	Request          TaskSubmission  `json:"request"`
	TemplateDigest   string          `json:"templateDigest"`
	InputsDigest     string          `json:"inputsDigest"`
	Inputs           json.RawMessage `json:"inputs"`
	CreatedAt        string          `json:"createdAt"`
	ConfirmBefore    string          `json:"confirmBefore"`
	FactDigest       string          `json:"factDigest"`
}

func (v TaskDraft) Validate() error {
	fail := errors.New("task: invalid draft")
	if domain.ValidateID(v.GoalID) != nil || v.Revision != 1 || v.Request.Validate() != nil || len(v.Inputs) == 0 || len(v.Inputs) > MaxTeamInputsBytes {
		return fail
	}
	raw, err := json.Marshal(v.Request)
	if err != nil {
		return fail
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil || digest != v.RequestDigest || canonical.DigestBytes(v.Inputs) != v.InputsDigest {
		return fail
	}
	for _, d := range []string{v.RequestKeyDigest, v.RequestDigest, v.TemplateDigest, v.InputsDigest} {
		if !taskDigest(d) {
			return fail
		}
	}
	created, e1 := time.Parse(time.RFC3339Nano, v.CreatedAt)
	deadline, e2 := time.Parse(time.RFC3339Nano, v.ConfirmBefore)
	if e1 != nil || e2 != nil || created.UTC().Format(time.RFC3339Nano) != v.CreatedAt || deadline.UTC().Format(time.RFC3339Nano) != v.ConfirmBefore || !deadline.After(created) || deadline.Sub(created) > 30*time.Minute {
		return fail
	}
	var inputs TeamInputs
	if json.Unmarshal(v.Inputs, &inputs) != nil || inputs.Spec.GoalId != v.GoalID || inputs.Proposal.GoalId != v.GoalID {
		return fail
	}
	return nil
}

func taskDigest(v string) bool {
	if len(v) != 71 || !strings.HasPrefix(v, "sha256:") {
		return false
	}
	for _, c := range v[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

type TaskStop struct {
	GoalID           string `json:"goalId"`
	DraftFactDigest  string `json:"draftFactDigest"`
	RequestKeyDigest string `json:"requestKeyDigest"`
	RequestedAt      string `json:"requestedAt"`
	FactDigest       string `json:"factDigest"`
}

type TaskDeliveryFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type TaskDelivery struct {
	GoalID             string             `json:"goalId"`
	OutcomeFactDigest  string             `json:"outcomeFactDigest"`
	PlanFactDigest     string             `json:"planFactDigest"`
	IntegrationRunID   string             `json:"integrationRunId"`
	IntegrationBaseSHA string             `json:"integrationBaseSha"`
	CandidateDigests   []string           `json:"candidateDigests"`
	PatchDigests       []string           `json:"patchDigests"`
	DecisionDigests    []string           `json:"decisionDigests"`
	Files              []TaskDeliveryFile `json:"files"`
	ContentDigest      string             `json:"contentDigest"`
	ContentBytes       int64              `json:"contentBytes"`
	MediaType          string             `json:"mediaType"`
	FactDigest         string             `json:"factDigest"`
}
