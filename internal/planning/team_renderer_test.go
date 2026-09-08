package planning

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
	"github.com/chiga0/marshal-harness/internal/goal"
)

// The real reference input adapter must pass Core's parser, not only its own
// Python tests. This runs no Agent, probe, Run mutation or approval operation.
func TestTeamReferenceRendererPassesCorePreview(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	doctor := filepath.Join(directory, "doctor.json")
	requestPath := filepath.Join(directory, "request.json")
	fixture := localDogfoodPolicyFixture()
	if err := os.WriteFile(doctor, mustMarshal(t, map[string]any{"policyEnvironmentBinding": fixture.EnvironmentBinding}), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/python3", "-I", "-B", filepath.Join(root, "scripts/fixed-server-team-inputs.py"),
		"--repository", root, "--base-ref", strings.Repeat("a", 40), "--doctor", doctor, "--model", "openai/qwen3.8-max",
		"--goal-id", "goal-quote", "--proposal-id", "proposal-quote", "--request-id", "approve-quote", "--deadline", "2030-01-01T00:00:00Z", "--out", requestPath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("reference renderer failed: %v %s", err, output)
	}
	raw, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	var request application.ApproveInitialTeamRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	frozen, _, err := request.Frozen()
	if err != nil {
		t.Fatalf("real renderer request failed application contract: %v", err)
	}
	preview, err := PreviewTeamInputs(frozen.Inputs, newValidator(t))
	if err != nil {
		t.Fatalf("real renderer failed Core Task/Policy/Goal preview: %v", err)
	}
	if preview.Digest != frozen.InputsDigest || len(preview.Inputs.Nodes) != 3 {
		t.Fatal("reference approval binding drift")
	}
	template, err := OpenTaskTemplate(frozen.Inputs, newValidator(t))
	if err != nil {
		t.Fatalf("real operator renderer failed installed Task template: %v", err)
	}
	if _, err := template.Preview("http-task-order", goal.TaskSubmission{Template: goal.TaskTemplateOrderQuote, Intent: "演示订单 API 与客户端交付"}, newValidator(t)); err != nil {
		t.Fatalf("real renderer failed public Task preview: %v", err)
	}
}
