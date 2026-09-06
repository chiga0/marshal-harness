package planning

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
)

func teamInputsFixture(t *testing.T) TeamInputs {
	t.Helper()
	namespace := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: "/fixture/team"}
	spec := goal.GoalSpecRevision{AuthorityNamespaceId: namespace, GoalId: "quote-team", Revision: 1, ProjectId: "orders", Repository: "/fixture/team", Title: "订单报价", Description: "实现 API 与客户端并通过独立集成验收"}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	proposal := goal.GoalPlanProposal{AuthorityNamespaceId: namespace, ProposalId: "proposal-1", GoalId: spec.GoalId, ProjectId: spec.ProjectId, Repository: spec.Repository, GoalSpecRevision: 1, GoalSpecDigest: specDigest, PlannerIdentity: "planner-not-approval"}
	paths := [][]string{{"service.py"}, {"client.py"}, {"service.py", "client.py"}}
	for index, id := range []string{"service", "client", "integration"} {
		proposal.Nodes = append(proposal.Nodes, goal.GoalNode{NodeId: id, ExecutorKind: goal.ExecutorKindImplement, Title: id, Repository: spec.Repository, Paths: paths[index], SideEffectClasses: []string{"workspace-write"}, Estimate: goal.NodeEstimate{Runs: 1, Attempts: 4, WallTimeSeconds: 10000, ArtifactBytes: 1 << 30}})
	}
	proposal.Edges = []goal.GoalEdge{{From: "service", To: "integration", Kind: goal.EdgeKindDependsOn}, {From: "client", To: "integration", Kind: goal.EdgeKindDependsOn}}
	inputs := TeamInputs{SchemaVersion: TeamInputsVersion, Spec: spec, Proposal: proposal, BaseSHA: strings.Repeat("a", 40)}
	for _, node := range proposal.Nodes {
		taskID, runID, err := TeamNodeIDs(proposal, node.NodeId)
		if err != nil {
			t.Fatal(err)
		}
		var task map[string]any
		if err := json.Unmarshal(planningTaskFixture(t, spec.Repository, taskID, "pi", "https://example.invalid/team.git", inputs.BaseSHA), &task); err != nil {
			t.Fatal(err)
		}
		task["scope"].(map[string]any)["allowPaths"] = node.Paths
		task["worker"].(map[string]any)["model"] = "openai/qwen3.8-max"
		task["admission"] = map[string]any{"status": "executable"}
		role := "implement"
		if node.NodeId == "integration" {
			role = "integrate"
		}
		inputs.Nodes = append(inputs.Nodes, TeamNodeInputs{NodeID: node.NodeId, Role: role, Task: mustMarshal(t, task), Policy: planningPolicyFixture(t, taskID, runID, "pi")})
	}
	return inputs
}

func TestTeamInputsPreviewBindsCompleteFrozenInputs(t *testing.T) {
	inputs := teamInputsFixture(t)
	validator := newValidator(t)
	preview, err := PreviewTeamInputs(mustMarshal(t, inputs), validator)
	if err != nil {
		t.Fatal(err)
	}
	indented, err := json.MarshalIndent(inputs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := PreviewTeamInputs(indented, validator)
	if err != nil || replayed.Digest != preview.Digest || !bytes.Equal(replayed.Canonical, preview.Canonical) {
		t.Fatalf("canonical replay drift: %v", err)
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(inputs.Nodes[0].Task, &task); err != nil {
		t.Fatal(err)
	}
	task.Work.Objective += "；保持 API 输入不变"
	inputs.Nodes[0].Task = mustMarshal(t, task)
	changed, err := PreviewTeamInputs(mustMarshal(t, inputs), validator)
	if err != nil || changed.Digest == preview.Digest {
		t.Fatalf("changed work was not bound: %v", err)
	}
	// The input identity is a stable obligation; a durable approval/CAS must
	// reject the changed digest under the old key, not mint another Run.
	oldTask, oldRun, _ := TeamNodeIDs(preview.Inputs.Proposal, "service")
	newTask, newRun, _ := TeamNodeIDs(changed.Inputs.Proposal, "service")
	if oldTask != newTask || oldRun != newRun {
		t.Fatal("content change silently produced another Run")
	}
	if domain.ValidateID(oldTask) != nil || domain.ValidateID(oldRun) != nil {
		t.Fatal("derived IDs cannot enter existing planning")
	}
	inputs.Proposal.ProposalId = "proposal-2"
	_, nextRun, err := TeamNodeIDs(inputs.Proposal, "service")
	if err != nil || nextRun == oldRun {
		t.Fatal("new proposal reused old obligation")
	}
}

func mutateTeamTask(t *testing.T, inputs *TeamInputs, mutate func(map[string]any)) {
	t.Helper()
	var task map[string]any
	if err := json.Unmarshal(inputs.Nodes[0].Task, &task); err != nil {
		t.Fatal(err)
	}
	mutate(task)
	inputs.Nodes[0].Task = mustMarshal(t, task)
}

func TestTeamInputsRejectsUnexecutableAndMismatchedBundle(t *testing.T) {
	validator := newValidator(t)
	for _, tc := range []struct {
		name   string
		mutate func(*TeamInputs)
	}{
		{"wrong-version", func(i *TeamInputs) { i.SchemaVersion = "team/v2" }},
		{"moving-base", func(i *TeamInputs) { i.BaseSHA = "main" }},
		{"spec-drift", func(i *TeamInputs) { i.Spec.Description += "changed" }},
		{"missing-node", func(i *TeamInputs) { i.Nodes = i.Nodes[:2] }},
		{"duplicate-input", func(i *TeamInputs) { i.Nodes[1] = i.Nodes[0] }},
		{"duplicate-plan-node", func(i *TeamInputs) { i.Proposal.Nodes[1] = i.Proposal.Nodes[0] }},
		{"missing-integrator", func(i *TeamInputs) { i.Nodes[2].Role = "implement" }},
		{"extra-integrator", func(i *TeamInputs) { i.Nodes[1].Role = "integrate" }},
		{"reverse-edge", func(i *TeamInputs) { i.Proposal.Edges[0].From, i.Proposal.Edges[0].To = "integration", "service" }},
		{"duplicate-edge", func(i *TeamInputs) { i.Proposal.Edges[1] = i.Proposal.Edges[0] }},
		{"budget-underestimate", func(i *TeamInputs) { i.Proposal.Nodes[0].Estimate.WallTimeSeconds = 0 }},
		{"foreign-policy", func(i *TeamInputs) { i.Nodes[0].Policy = i.Nodes[1].Policy }},
		{"node-too-large", func(i *TeamInputs) {
			i.Nodes[0].Task = json.RawMessage(`"` + strings.Repeat("x", MaxTeamNodeBytes) + `"`)
		}},
		{"foreign-task-id", func(i *TeamInputs) {
			mutateTeamTask(t, i, func(task map[string]any) { task["metadata"].(map[string]any)["id"] = "foreign" })
		}},
		{"broader-scope", func(i *TeamInputs) {
			mutateTeamTask(t, i, func(task map[string]any) { task["scope"].(map[string]any)["allowPaths"] = []string{"**"} })
		}},
		{"unapproved-input", func(i *TeamInputs) {
			mutateTeamTask(t, i, func(task map[string]any) { task["admission"] = map[string]any{"status": "prepared"} })
		}},
		{"unknown-task-field", func(i *TeamInputs) {
			mutateTeamTask(t, i, func(task map[string]any) { task["authorityOverride"] = true })
		}},
		{"publication", func(i *TeamInputs) {
			mutateTeamTask(t, i, func(task map[string]any) { task["publication"].(map[string]any)["provider"] = "github" })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputs := teamInputsFixture(t)
			tc.mutate(&inputs)
			preview, err := PreviewTeamInputs(mustMarshal(t, inputs), validator)
			if !errors.Is(err, ErrTeamInputs) || preview.Digest != "" || preview.Canonical != nil {
				t.Fatalf("invalid preview escaped: %v", err)
			}
		})
	}
}

func TestTeamInputsRejectsAmbiguousOrOversizedJSON(t *testing.T) {
	validator := newValidator(t)
	raw := mustMarshal(t, teamInputsFixture(t))
	for _, invalid := range [][]byte{
		nil, bytes.Repeat([]byte(" "), MaxTeamInputsBytes+1), append(append([]byte(nil), raw...), raw...),
		bytes.Replace(raw, []byte(`"schemaVersion":`), []byte(`"schemaVersion":"duplicate","schemaVersion":`), 1),
		append([]byte(`{"unknown":true,`), raw[1:]...),
	} {
		if _, err := PreviewTeamInputs(invalid, validator); !errors.Is(err, ErrTeamInputs) {
			t.Fatalf("ambiguous/oversized input accepted: %v", err)
		}
	}
}
