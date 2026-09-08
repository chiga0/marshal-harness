package goal

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

const (
	MaxTaskQuestions     = 3
	MaxTaskAnswerBytes   = 4096
	TaskQuestionProtocol = "task-clarification/v1"
)

var ErrTaskQuestion = errors.New("task: invalid clarification")

func ValidateTaskQuestionDigest(digest string) error {
	if !taskDigest(digest) {
		return ErrTaskQuestion
	}
	return nil
}

// Only this closed context transform is supported. It grants no permission to
// replace TaskSpec fields, oracle bytes, Policy, graph or resource budgets.
func TaskQuestionRendererDigest() string {
	return canonical.DigestBytes([]byte("task-context-slots/v1"))
}

type TaskInputSlot struct {
	ID      string   `json:"id"`
	Prompt  string   `json:"prompt"`
	NodeIDs []string `json:"nodeIds"`
}

type TaskQuestionTemplate struct {
	ID              string          `json:"id"`
	TemplateDigest  string          `json:"templateDigest"`
	ProducerDigest  string          `json:"producerDigest"`
	ValidatorDigest string          `json:"validatorDigest"`
	RendererDigest  string          `json:"rendererDigest"`
	Slots           []TaskInputSlot `json:"slots"`
}

func (v TaskQuestionTemplate) Validate() error {
	if !questionText(v.ID, 128) || strings.ContainsAny(v.ID, " \t\r\n") || len(v.Slots) > MaxTaskQuestions || v.ID == TaskTemplateOrderQuote && len(v.Slots) != 0 || v.RendererDigest != TaskQuestionRendererDigest() {
		return ErrTaskQuestion
	}
	for _, digest := range []string{v.TemplateDigest, v.ProducerDigest, v.ValidatorDigest} {
		if !taskDigest(digest) {
			return ErrTaskQuestion
		}
	}
	seen := map[string]bool{}
	for _, slot := range v.Slots {
		if domain.ValidateID(slot.ID) != nil || len(slot.ID) > 128 || seen[slot.ID] || !questionText(slot.Prompt, 2048) || len(slot.NodeIDs) == 0 || len(slot.NodeIDs) > 3 {
			return ErrTaskQuestion
		}
		seen[slot.ID] = true
		nodes := map[string]bool{}
		for _, id := range slot.NodeIDs {
			if domain.ValidateID(id) != nil || nodes[id] {
				return ErrTaskQuestion
			}
			nodes[id] = true
		}
	}
	return nil
}

type TaskSlotValue struct {
	SlotID string `json:"slotId"`
	Value  string `json:"value"`
}

func (v TaskSlotValue) Validate() error {
	if domain.ValidateID(v.SlotID) != nil || !questionText(v.Value, MaxTaskAnswerBytes) {
		return ErrTaskQuestion
	}
	return nil
}

// MissingTaskSlots only compares declared input identities. Whether a supplied
// value satisfies the business contract is checked by the frozen DI validator.
func MissingTaskSlots(template TaskQuestionTemplate, values []TaskSlotValue) ([]TaskInputSlot, error) {
	if template.Validate() != nil || len(values) > len(template.Slots) {
		return nil, ErrTaskQuestion
	}
	known, seen := map[string]bool{}, map[string]bool{}
	for _, slot := range template.Slots {
		known[slot.ID] = true
	}
	for _, value := range values {
		if value.Validate() != nil || !known[value.SlotID] || seen[value.SlotID] {
			return nil, ErrTaskQuestion
		}
		seen[value.SlotID] = true
	}
	missing := []TaskInputSlot{}
	for _, slot := range template.Slots {
		if !seen[slot.ID] {
			slot.NodeIDs = slices.Clone(slot.NodeIDs)
			missing = append(missing, slot)
		}
	}
	return missing, nil
}

type TaskQuestion struct {
	ID       string `json:"id"`
	SlotID   string `json:"slotId"`
	Revision int64  `json:"revision"`
	Prompt   string `json:"prompt"`
}

// TaskClarificationRoot is a new, explicitly versioned initial record. It is
// never passed through the old immutable TaskDraft/v1 parser.
type TaskClarificationRoot struct {
	TaskID           string               `json:"taskId"`
	Request          TaskSubmission       `json:"request"`
	RequestKeyDigest string               `json:"requestKeyDigest"`
	RequestDigest    string               `json:"requestDigest"`
	CreatedAt        string               `json:"createdAt"`
	ConfirmBefore    string               `json:"confirmBefore"`
	Template         TaskQuestionTemplate `json:"template"`
	InitialValues    []TaskSlotValue      `json:"initialValues"`
	Inputs           json.RawMessage      `json:"inputs"`
	InputsDigest     string               `json:"inputsDigest"`
	Questions        []TaskQuestion       `json:"questions"`
}

func QuestionID(taskID, subject, slotID string) string {
	data, _ := json.Marshal([]string{TaskQuestionProtocol, taskID, subject, slotID})
	return "question-" + strings.TrimPrefix(canonical.DigestBytes(data), "sha256:")
}

func (v TaskClarificationRoot) Validate() error {
	if domain.ValidateID(v.TaskID) != nil || v.Template.Validate() != nil || v.Request.Template != v.Template.ID || v.Request.Template == TaskTemplateOrderQuote || !questionText(v.Request.Intent, 4096) || len(v.Request.Context.Text) > 16384 || !utf8.ValidString(v.Request.Context.Text) || strings.ContainsRune(v.Request.Context.Text, 0) {
		return ErrTaskQuestion
	}
	raw, err := json.Marshal(v.Request)
	if err != nil {
		return ErrTaskQuestion
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil || digest != v.RequestDigest || !taskDigest(v.RequestKeyDigest) || !validQuestionInputs(v.Inputs, v.TaskID) || canonical.DigestBytes(v.Inputs) != v.InputsDigest {
		return ErrTaskQuestion
	}
	created, e1 := time.Parse(time.RFC3339Nano, v.CreatedAt)
	deadline, e2 := time.Parse(time.RFC3339Nano, v.ConfirmBefore)
	if e1 != nil || e2 != nil || created.UTC().Format(time.RFC3339Nano) != v.CreatedAt || deadline.UTC().Format(time.RFC3339Nano) != v.ConfirmBefore || !deadline.After(created) || deadline.Sub(created) > 30*time.Minute {
		return ErrTaskQuestion
	}
	missing, err := MissingTaskSlots(v.Template, v.InitialValues)
	if err != nil || len(missing) == 0 || len(v.Questions) != len(missing) {
		return ErrTaskQuestion
	}
	var inputs TeamInputs
	if json.Unmarshal(v.Inputs, &inputs) != nil {
		return ErrTaskQuestion
	}
	for _, slot := range v.Template.Slots {
		for _, id := range slot.NodeIDs {
			if !slices.ContainsFunc(inputs.Nodes, func(n TeamNodeInputs) bool { return n.NodeID == id }) {
				return ErrTaskQuestion
			}
		}
	}
	for n, slot := range missing {
		q := v.Questions[n]
		if q.ID != QuestionID(v.TaskID, v.InputsDigest, slot.ID) || q.SlotID != slot.ID || q.Prompt != slot.Prompt || q.Revision != 1 {
			return ErrTaskQuestion
		}
	}
	return nil
}

// Values contains answers to the initially missing slots, not supplied input
// values. Original context remains a byte-preserved prefix on every rendering.
func RenderTaskQuestionInputs(root TaskClarificationRoot, values []TaskSlotValue) ([]byte, error) {
	if root.Validate() != nil || len(values) > len(root.Questions) {
		return nil, ErrTaskQuestion
	}
	bySlot := map[string]string{}
	for _, v := range values {
		if v.Validate() != nil || bySlot[v.SlotID] != "" || !slices.ContainsFunc(root.Questions, func(q TaskQuestion) bool { return q.SlotID == v.SlotID }) {
			return nil, ErrTaskQuestion
		}
		bySlot[v.SlotID] = v.Value
	}
	// Retain every original field (including future schema fields and exact
	// JSON numbers). Never strip authority fields before comparing candidates.
	var doc map[string]json.RawMessage
	if json.Unmarshal(root.Inputs, &doc) != nil {
		return nil, ErrTaskQuestion
	}
	var nodes []map[string]json.RawMessage
	if json.Unmarshal(doc["nodes"], &nodes) != nil {
		return nil, ErrTaskQuestion
	}
	for _, node := range nodes {
		var nodeID string
		var task, work map[string]json.RawMessage
		if json.Unmarshal(node["nodeId"], &nodeID) != nil || json.Unmarshal(node["task"], &task) != nil || json.Unmarshal(task["work"], &work) != nil {
			return nil, ErrTaskQuestion
		}
		var context []string
		if raw, ok := work["context"]; ok && json.Unmarshal(raw, &context) != nil {
			return nil, ErrTaskQuestion
		}
		changed := false
		for _, slot := range root.Template.Slots {
			value, answered := bySlot[slot.ID]
			if answered && slices.Contains(slot.NodeIDs, nodeID) {
				context = append(context, "[Marshal task input "+slot.ID+"]\n"+value)
				changed = true
			}
		}
		if changed {
			work["context"], _ = json.Marshal(context)
			task["work"], _ = json.Marshal(work)
			node["task"], _ = json.Marshal(task)
		}
	}
	doc["nodes"], _ = json.Marshal(nodes)
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, ErrTaskQuestion
	}
	raw, err = canonical.JSON(raw)
	if err != nil || !validQuestionInputs(raw, root.TaskID) {
		return nil, ErrTaskQuestion
	}
	return raw, nil
}

func ValidateTaskQuestionInputs(root TaskClarificationRoot, values []TaskSlotValue, candidate []byte) error {
	expected, err := RenderTaskQuestionInputs(root, values)
	if err != nil || !bytes.Equal(expected, candidate) {
		return ErrTaskQuestion
	}
	return nil
}

func validQuestionInputs(raw []byte, taskID string) bool {
	if len(raw) == 0 || len(raw) > MaxTeamInputsBytes {
		return false
	}
	canon, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(canon, raw) {
		return false
	}
	var inputs TeamInputs
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(&inputs) == nil && inputs.SchemaVersion == TeamInputsVersion && inputs.Spec.GoalId == taskID && inputs.Proposal.GoalId == taskID && len(inputs.Nodes) == 3
}

func questionText(s string, maximum int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= maximum && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
