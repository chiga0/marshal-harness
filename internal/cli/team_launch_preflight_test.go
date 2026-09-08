package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/goal"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

// Exercise the actual reference renderer -> Core preview -> production launch
// builder. No provider is probed or executed and the doctor binding is a fixture.
func TestTeamReferenceRendererProductionLaunchPreflight(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	doctor, output := filepath.Join(dir, "doctor.json"), filepath.Join(dir, "request.json")
	fixture, err := json.Marshal(map[string]any{"policyEnvironmentBinding": planning.LocalDogfoodEnvironmentBinding{
		SchemaVersion: planning.LocalDogfoodEnvironmentBindingSchema, SelfProfile: selfidentity.LocalProfile,
		ActivationDigest: "sha256:" + strings.Repeat("a", 64), IdentitySubjectDigest: "sha256:" + strings.Repeat("b", 64),
		Assurance: "ordinary-user", Execution: "workspace-write", Production: false, Publication: "none",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doctor, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-B", filepath.Join(root, "scripts/fixed-server-team-inputs.py"),
		"--repository", root, "--base-ref", strings.Repeat("a", 40), "--doctor", doctor, "--model", "openai/qwen3.8-max",
		"--goal-id", "goal-quote", "--proposal-id", "proposal-quote", "--request-id", "approve-quote", "--deadline", "2030-01-01T00:00:00Z", "--out", output)
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("renderer: %v %s", err, result)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var request application.ApproveInitialTeamRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	var inputs goal.TeamInputs
	if err := json.Unmarshal(request.Inputs, &inputs); err != nil {
		t.Fatal(err)
	}
	for _, node := range inputs.Nodes {
		if err := validator.Validate(domain.KindTask, node.Task); err != nil {
			t.Fatalf("%s rendered Task schema: %v", node.NodeID, err)
		}
		if err := validator.Validate(domain.KindPolicySnapshot, node.Policy); err != nil {
			t.Fatalf("%s rendered Policy schema: %v", node.NodeID, err)
		}
	}
	preview, err := planning.PreviewTeamInputs(request.Inputs, validator)
	if err != nil {
		t.Fatal(err)
	}
	const runtime, entry = "/opt/pi/node", "/opt/pi/cli.js"
	if err := preflightPiTeamLaunch(runtime, entry, preview.Inputs); err != nil {
		t.Fatal(err)
	}
	for index, node := range preview.Inputs.Nodes {
		var task domain.TaskSpec
		if err := json.Unmarshal(node.Task, &task); err != nil {
			t.Fatal(err)
		}
		taskID, runID, err := goal.TeamNodeIDs(preview.Inputs.Proposal, node.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		launch, err := piProductionLaunchBuilder(runtime, entry, task)(productionruntime.AttemptLaunchIdentity{TaskID: taskID, RunID: runID, AttemptID: "attempt-real"})
		if err != nil {
			t.Fatal(err)
		}
		if len(task.Work.Context) == 0 {
			t.Fatal("typed decode dropped business context")
		}
		for _, values := range [][]string{task.Work.Context, task.Work.NonGoals, task.Work.Constraints} {
			for _, value := range values {
				if !strings.Contains(launch.Prompt, value) {
					t.Fatalf("%s prompt lost approved text", node.NodeID)
				}
			}
		}
		// Every node, including the not-yet-ready integration node, must be
		// preflighted. Catch the old client failure before approval/reservation.
		for _, bad := range []string{"POST /quote", "read /private/secret", "read .marshal", strings.Repeat("x", 17000)} {
			changed := preview.Inputs
			changed.Nodes = append([]goal.TeamNodeInputs(nil), preview.Inputs.Nodes...)
			task.Work.Context = []string{bad}
			changed.Nodes[index].Task, err = json.Marshal(task)
			if err != nil {
				t.Fatal(err)
			}
			if err := preflightPiTeamLaunch(runtime, entry, changed); err == nil {
				t.Fatalf("%s invalid context passed", node.NodeID)
			}
		}
	}
}

func TestPiProductionLaunchPreservesContextBoundaries(t *testing.T) {
	task := domain.TaskSpec{Worker: domain.TaskWorker{ExecutionProfile: "workspace-write"}, Work: domain.TaskWork{Objective: "Implement quote", Context: []string{"shared contract"}, NonGoals: []string{"no deployment"}}}
	id := productionruntime.AttemptLaunchIdentity{TaskID: "task-1", RunID: "run-1", AttemptID: "attempt-1"}
	launch, err := piProductionLaunchBuilder("/opt/pi/node", "/opt/pi/cli.js", task)(id)
	if err != nil || !strings.Contains(launch.Prompt, "shared contract") || !strings.Contains(launch.Prompt, "no deployment") {
		t.Fatal("incomplete prompt", err)
	}
	task.Work.Objective = ""
	if _, err := piProductionLaunchBuilder("/opt/pi/node", "/opt/pi/cli.js", task)(id); err == nil {
		t.Fatal("context made an empty objective valid")
	}
	for _, bad := range []string{"read /private/secret", "read .marshal", "NUL\x00"} {
		task.Work.Objective = "Implement quote"
		task.Work.NonGoals = []string{bad}
		if _, err := piProductionLaunchBuilder("/opt/pi/node", "/opt/pi/cli.js", task)(id); err == nil {
			t.Fatal("non-goal bypassed prompt guard")
		}
	}
}
