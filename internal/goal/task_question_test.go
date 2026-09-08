package goal

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func questionJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = canonical.JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Shape-only fixture; production schema/preflight and actual RB1/Session
// behavior are covered by separate caller-chain tests, not asserted here.
func questionRootFixture(t *testing.T) TaskClarificationRoot {
	t.Helper()
	template := TaskQuestionTemplate{ID: "test-context/v1", TemplateDigest: canonical.DigestBytes([]byte("template")), ProducerDigest: canonical.DigestBytes([]byte("producer")), ValidatorDigest: canonical.DigestBytes([]byte("validator")), RendererDigest: TaskQuestionRendererDigest(), Slots: []TaskInputSlot{{ID: "audience", Prompt: "交付说明的目标读者？", NodeIDs: []string{"service", "integration"}}, {ID: "example", Prompt: "说明中要引用的业务示例？", NodeIDs: []string{"client", "integration"}}}}
	input := TeamInputs{SchemaVersion: TeamInputsVersion, Spec: GoalSpecRevision{GoalId: "question-task"}, Proposal: GoalPlanProposal{GoalId: "question-task"}}
	for _, id := range []string{"service", "client", "integration"} {
		input.Nodes = append(input.Nodes, TeamNodeInputs{NodeID: id, Role: "implement", Task: questionJSON(t, map[string]any{"work": map[string]any{"objective": "original objective", "context": []string{"original context"}, "constraints": []string{"frozen constraints"}, "nonGoals": []string{"no publish"}}, "acceptance": map[string]any{"oracle": "frozen bytes"}, "futureNumber": json.Number("9007199254740992")}), Policy: json.RawMessage(`{"frozen":true}`)})
	}
	request := TaskSubmission{Template: template.ID, Intent: "测试专用文本槽，不注册业务支持"}
	created := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	root := TaskClarificationRoot{TaskID: "question-task", Request: request, RequestKeyDigest: canonical.DigestBytes([]byte("key")), RequestDigest: canonical.DigestBytes(questionJSON(t, request)), CreatedAt: created.Format(time.RFC3339Nano), ConfirmBefore: created.Add(30 * time.Minute).Format(time.RFC3339Nano), Template: template, InitialValues: []TaskSlotValue{}, Inputs: questionJSON(t, input)}
	root.InputsDigest = canonical.DigestBytes(root.Inputs)
	for _, slot := range template.Slots {
		root.Questions = append(root.Questions, TaskQuestion{ID: QuestionID(root.TaskID, root.InputsDigest, slot.ID), SlotID: slot.ID, Revision: 1, Prompt: slot.Prompt})
	}
	if err := root.Validate(); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTaskQuestionMissingSlotsAndZeroQuestionOrderQuote(t *testing.T) {
	root := questionRootFixture(t)
	missing, err := MissingTaskSlots(root.Template, []TaskSlotValue{{SlotID: "audience", Value: "测试读者"}})
	if err != nil || len(missing) != 1 || missing[0].ID != "example" {
		t.Fatalf("missing: %v %v", missing, err)
	}
	missing[0].NodeIDs[0] = "changed"
	if root.Template.Slots[1].NodeIDs[0] == "changed" {
		t.Fatal("returned alias")
	}
	zero := root.Template
	zero.ID = TaskTemplateOrderQuote
	zero.Slots = []TaskInputSlot{}
	missing, err = MissingTaskSlots(zero, nil)
	if err != nil || len(missing) != 0 {
		t.Fatal("complete B1 made to wait")
	}
	zero.Slots = root.Template.Slots
	if zero.Validate() == nil {
		t.Fatal("invented order-quote missing slot")
	}
	for _, values := range [][]TaskSlotValue{{{SlotID: "foreign", Value: "x"}}, {{SlotID: "audience", Value: "a"}, {SlotID: "audience", Value: "b"}}, {{SlotID: "audience", Value: " "}}} {
		if _, err := MissingTaskSlots(root.Template, values); err == nil {
			t.Fatal("invalid supplied slots")
		}
	}
}

func TestTaskQuestionBoundsAndRootBindings(t *testing.T) {
	root := questionRootFixture(t)
	for _, value := range []string{strings.Repeat("a", MaxTaskAnswerBytes+1), strings.Repeat("界", 1366), "a\x00b", string([]byte{0xff}), " \n"} {
		if (TaskSlotValue{SlotID: "audience", Value: value}).Validate() == nil {
			t.Fatal("invalid answer accepted")
		}
	}
	if (TaskSlotValue{SlotID: "audience", Value: strings.Repeat("a", MaxTaskAnswerBytes)}).Validate() != nil {
		t.Fatal("maximum answer refused")
	}
	for _, tc := range []struct {
		name string
		edit func(*TaskClarificationRoot)
	}{
		{"question-id", func(v *TaskClarificationRoot) { v.Questions[0].ID = "other" }},
		{"subject", func(v *TaskClarificationRoot) { v.InputsDigest = canonical.DigestBytes([]byte("other")) }},
		{"revision", func(v *TaskClarificationRoot) { v.Questions[0].Revision = 2 }},
		{"prompt", func(v *TaskClarificationRoot) { v.Questions[0].Prompt = "different question" }},
		{"duplicate-slot", func(v *TaskClarificationRoot) { v.Template.Slots[1].ID = v.Template.Slots[0].ID }},
		{"unknown-node", func(v *TaskClarificationRoot) { v.Template.Slots[0].NodeIDs = []string{"foreign"} }},
		{"duplicate-node", func(v *TaskClarificationRoot) { v.Template.Slots[0].NodeIDs = []string{"service", "service"} }},
		{"bad-renderer", func(v *TaskClarificationRoot) { v.Template.RendererDigest = canonical.DigestBytes([]byte("other")) }},
		{"deadline-extension", func(v *TaskClarificationRoot) { v.ConfirmBefore = "2026-09-08T00:30:00.000000001Z" }},
		{"too-many", func(v *TaskClarificationRoot) { v.Template.Slots = append(v.Template.Slots, v.Template.Slots...) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v TaskClarificationRoot
			if json.Unmarshal(questionJSON(t, root), &v) != nil {
				t.Fatal("clone")
			}
			tc.edit(&v)
			if v.Validate() == nil {
				t.Fatal("invalid root accepted")
			}
		})
	}
}

func TestTaskQuestionRendererPreservesFrozenBoundary(t *testing.T) {
	root := questionRootFixture(t)
	before := bytes.Clone(root.Inputs)
	zero, err := RenderTaskQuestionInputs(root, nil)
	if err != nil || !bytes.Equal(zero, before) {
		t.Fatal("zero answers changed bytes")
	}
	values := []TaskSlotValue{{SlotID: "example", Value: "example text"}, {SlotID: "audience", Value: "new readers"}}
	got, err := RenderTaskQuestionInputs(root, values)
	if err != nil {
		t.Fatal(err)
	}
	again, err := RenderTaskQuestionInputs(root, []TaskSlotValue{values[1], values[0]})
	if err != nil || !bytes.Equal(got, again) || !bytes.Equal(before, root.Inputs) {
		t.Fatal("render order or original input drift")
	}
	if !bytes.Contains(got, []byte("9007199254740992")) {
		t.Fatal("JSON number precision lost")
	}
	var input TeamInputs
	if json.Unmarshal(got, &input) != nil {
		t.Fatal("decode")
	}
	for n, node := range input.Nodes {
		var task struct {
			Work struct {
				Context []string `json:"context"`
			} `json:"work"`
		}
		if json.Unmarshal(node.Task, &task) != nil {
			t.Fatal("task")
		}
		want := 2
		if n == 2 {
			want = 3
		}
		if len(task.Work.Context) != want || task.Work.Context[0] != "original context" {
			t.Fatal("target or prefix mismatch")
		}
	}
	if ValidateTaskQuestionInputs(root, values, got) != nil {
		t.Fatal("own exact candidate failed")
	}
	for _, pair := range [][2]string{{"frozen bytes", "forged oracle"}, {"no publish", "publish now"}, {"original objective", "new objective"}, {"original context", "erased context"}, {"9007199254740992", "9007199254740994"}, {`"frozen":true`, `"frozen":false`}} {
		bad := bytes.Replace(got, []byte(pair[0]), []byte(pair[1]), 1)
		if bytes.Equal(bad, got) {
			t.Fatalf("negative did not edit %q", pair[0])
		}
		if ValidateTaskQuestionInputs(root, values, bad) == nil {
			t.Fatalf("boundary changed %q", pair[0])
		}
	}
	for _, values := range [][]TaskSlotValue{{{SlotID: "foreign", Value: "x"}}, {{SlotID: "audience", Value: "a"}, {SlotID: "audience", Value: "b"}}} {
		if _, err := RenderTaskQuestionInputs(root, values); err == nil {
			t.Fatal("unknown/duplicate answer rendered")
		}
	}
}
