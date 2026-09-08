package goal

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

const TaskTemplateOrderQuote = "order-quote/v1"

const MaxTaskDeliveryBytes = 8 << 20

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
	canonicalInputs, err := canonical.JSON(v.Inputs)
	if err != nil || !bytes.Equal(v.Inputs, canonicalInputs) || json.Unmarshal(v.Inputs, &inputs) != nil || inputs.Spec.GoalId != v.GoalID || inputs.Proposal.GoalId != v.GoalID {
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

// Validate checks a bounded immutable delivery reference, not acceptance or
// publication authority. The store must bind it to the current Team outcome.
func (v TaskDelivery) Validate() error {
	fail := errors.New("task: invalid delivery")
	if domain.ValidateID(v.GoalID) != nil || domain.ValidateID(v.IntegrationRunID) != nil || len(v.IntegrationBaseSHA) != 40 && len(v.IntegrationBaseSHA) != 64 || v.MediaType != "application/zip" || v.ContentBytes < 1 || v.ContentBytes > MaxTaskDeliveryBytes || len(v.Files) != 3 || len(v.CandidateDigests) != 3 || len(v.PatchDigests) != 3 || len(v.DecisionDigests) != 3 {
		return fail
	}
	for _, char := range v.IntegrationBaseSHA {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return fail
		}
	}
	for _, digest := range append(append(append([]string{v.OutcomeFactDigest, v.PlanFactDigest, v.ContentDigest}, v.CandidateDigests...), v.PatchDigests...), v.DecisionDigests...) {
		if !taskDigest(digest) {
			return fail
		}
	}
	paths := []string{"quote_api.py", "quote_client.py", "quote_delivery.json"}
	var total int64
	for index, file := range v.Files {
		if file.Path != paths[index] || !taskDigest(file.SHA256) || file.Bytes < 1 || file.Bytes > MaxTaskDeliveryBytes {
			return fail
		}
		total += file.Bytes
	}
	if total > MaxTaskDeliveryBytes || v.FactDigest != "" && !taskDigest(v.FactDigest) {
		return fail
	}
	return nil
}
