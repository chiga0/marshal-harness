package planning

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

func orderQuoteTemplateFixture(t *testing.T) TeamInputs {
	t.Helper()
	inputs := teamInputsFixture(t)
	inputs.Limits.MaxConcurrentNodes = 2
	inputs.Limits.MaxPlanRevisions = 1
	inputs.Limits.MaxTotalRuns, inputs.Limits.MaxTotalAttempts = 3, 3
	inputs.AdmissionPolicy.Paths = OrderQuotePaths("integration")
	for index := range inputs.Proposal.Nodes {
		node := &inputs.Proposal.Nodes[index]
		node.Paths = OrderQuotePaths(node.NodeId)
		node.Estimate.Attempts = 1
	}
	for index := range inputs.Nodes {
		node := &inputs.Nodes[index]
		var task map[string]any
		if err := json.Unmarshal(node.Task, &task); err != nil {
			t.Fatal(err)
		}
		task["scope"].(map[string]any)["allowPaths"] = OrderQuotePaths(node.NodeID)
		task["scope"].(map[string]any)["maxChangedFiles"] = len(OrderQuotePaths(node.NodeID))
		task["work"].(map[string]any)["context"] = []string{"原始跨节点接口契约", "原始边界条件"}
		deliverables := make([]domain.TaskDeliverable, 0, 3)
		for _, path := range OrderQuotePaths(node.NodeID) {
			deliverables = append(deliverables, domain.TaskDeliverable{ID: strings.ReplaceAll(path, ".", "-"), Kind: "code", Required: true, PathGlob: path, MinimumCount: 1, MediaType: "text/plain"})
		}
		task["deliverables"] = deliverables
		budgets := task["budgets"].(map[string]any)
		budgets["maxAttempts"], budgets["maxOperationalRetries"], budgets["maxReworkRounds"] = 1, 0, 0
		task["acceptance"] = domain.TaskAcceptance{Commands: []domain.TaskCommand{OrderQuoteOracleCommand(inputs.Spec.Repository, node.NodeID)}}
		node.Task = mustMarshal(t, task)
	}
	return inputs
}

func TestTaskTemplateBindsIdentityWithoutAlteringAuthority(t *testing.T) {
	validator := newValidator(t)
	fixture := orderQuoteTemplateFixture(t)
	template, err := OpenTaskTemplate(mustMarshal(t, fixture), validator)
	if err != nil {
		t.Fatal(err)
	}
	submission := goal.TaskSubmission{Template: goal.TaskTemplateOrderQuote, Intent: "实现报价接口和真实 HTTP 客户端", Context: goal.TaskContext{Text: "给演示者提供最终集成清单"}}
	first, err := template.Preview("task-demo", submission, validator)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := template.Preview("task-demo", submission, validator)
	if err != nil || !bytes.Equal(first.Canonical, replayed.Canonical) || first.Digest != replayed.Digest {
		t.Fatalf("deterministic preview drift: %v", err)
	}
	if first.Inputs.Spec.GoalId != "task-demo" || first.Inputs.Proposal.GoalId != "task-demo" || first.Inputs.BaseSHA != fixture.BaseSHA || template.Digest() == first.Digest {
		t.Fatal("identity or source template binding missing")
	}
	for index, input := range first.Inputs.Nodes {
		var before, after map[string]any
		if json.Unmarshal(fixture.Nodes[index].Task, &before) != nil || json.Unmarshal(input.Task, &after) != nil {
			t.Fatal("invalid task")
		}
		for _, key := range []string{"repository", "scope", "worker", "acceptance", "publication", "budgets"} {
			if !bytes.Equal(mustMarshal(t, before[key]), mustMarshal(t, after[key])) {
				t.Fatalf("template authority changed: %s", key)
			}
		}
		beforeWork, afterWork := before["work"].(map[string]any), after["work"].(map[string]any)
		for _, key := range []string{"objective", "constraints", "nonGoals"} {
			if !bytes.Equal(mustMarshal(t, beforeWork[key]), mustMarshal(t, afterWork[key])) {
				t.Fatalf("original work changed: %s", key)
			}
		}
		beforeContext := beforeWork["context"].([]any)
		afterContext, ok := afterWork["context"].([]any)
		if !ok || len(afterContext) != len(beforeContext)+3 || !bytes.Equal(mustMarshal(t, beforeContext), mustMarshal(t, afterContext[:len(beforeContext)])) || afterContext[len(beforeContext)+1] != submission.Intent || afterContext[len(beforeContext)+2] != submission.Context.Text {
			t.Fatal("original context prefix or public context discarded")
		}
	}
	other, err := template.Preview("task-other", submission, validator)
	if err != nil || other.Digest == first.Digest {
		t.Fatal("distinct public Task reused identities")
	}
	// Mutating a returned proposal must not mutate installed configuration.
	first.Inputs.Nodes[0].Task[0] = '!'
	again, err := template.Preview("task-demo", submission, validator)
	if err != nil || again.Digest != replayed.Digest {
		t.Fatal("caller modified installed template")
	}
}

func TestTaskTemplatePreservesMaximumContextWithinSchema(t *testing.T) {
	validator := newValidator(t)
	template, err := OpenTaskTemplate(mustMarshal(t, orderQuoteTemplateFixture(t)), validator)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{strings.Repeat("x", 16384), strings.Repeat("界", 5461)} {
		submission := goal.TaskSubmission{Template: goal.TaskTemplateOrderQuote, Intent: strings.Repeat("i", 4096), Context: goal.TaskContext{Text: text}}
		preview, err := template.Preview("maximum-context", submission, validator)
		if err != nil {
			t.Fatalf("valid bounded context rejected: %v", err)
		}
		for _, node := range preview.Inputs.Nodes {
			var task domain.TaskSpec
			if json.Unmarshal(node.Task, &task) != nil || len(task.Work.Context) < 5 {
				t.Fatal("context array missing")
			}
			if strings.Join(task.Work.Context[4:], "") != text || task.Work.Context[3] != submission.Intent {
				t.Fatal("long context or intent truncated")
			}
			for _, item := range task.Work.Context {
				if !utf8.ValidString(item) || utf8.RuneCountInString(item) > 8000 {
					t.Fatal("context element exceeded schema boundary")
				}
			}
		}
	}
}

func TestTaskTemplateRejectsOracleAndScopeDrift(t *testing.T) {
	validator := newValidator(t)
	for _, tc := range []struct {
		name string
		edit func(*TeamInputs)
	}{
		{"oracle-digest", func(v *TeamInputs) {
			mutateTeamTask(t, v, func(task map[string]any) {
				task["acceptance"].(map[string]any)["commands"].([]any)[0].(map[string]any)["argv"].([]any)[6] = strings.Repeat("0", 64)
			})
		}},
		{"optional-oracle", func(v *TeamInputs) {
			mutateTeamTask(t, v, func(task map[string]any) {
				task["acceptance"].(map[string]any)["commands"].([]any)[0].(map[string]any)["required"] = false
			})
		}},
		{"oracle-zero-matches", func(v *TeamInputs) {
			mutateTeamTask(t, v, func(task map[string]any) {
				task["acceptance"].(map[string]any)["commands"].([]any)[0].(map[string]any)["argv"] = []string{"/usr/bin/true"}
			})
		}},
		{"retry-budget", func(v *TeamInputs) {
			mutateTeamTask(t, v, func(task map[string]any) { task["budgets"].(map[string]any)["maxAttempts"] = 2 })
		}},
		{"goal-budget", func(v *TeamInputs) { v.Limits.MaxTotalRuns = 4 }},
		{"role-swap", func(v *TeamInputs) { v.Nodes[0].Role, v.Nodes[2].Role = "integrate", "implement" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := orderQuoteTemplateFixture(t)
			tc.edit(&fixture)
			if template, err := OpenTaskTemplate(mustMarshal(t, fixture), validator); err == nil || template.Digest() != "" {
				t.Fatal("changed profile was accepted")
			}
		})
	}
}

func TestTaskTemplateOracleBytesRemainVersionBound(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "order-quote-team-oracle.py"))
	if err != nil {
		t.Fatal(err)
	}
	if canonical.DigestBytes(raw) != "sha256:"+OrderQuoteOracleDigest {
		t.Fatal("original oracle changed without updating the explicit Task template")
	}
}
