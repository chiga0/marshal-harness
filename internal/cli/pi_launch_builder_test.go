package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

// TestPiProductionLaunchBuilderIsPureMapping proves the CLI-injected argv
// builder is a pure mapping from the frozen task fields plus the precise
// reserved identity to adapter/pi.BuildProductionLaunch: identical inputs
// produce identical argv bytes, the output equals BuildProductionLaunch
// exactly, the argv carries --mode json --print and ends with the prompt, and
// a BuildProductionLaunch validation error propagates unchanged.
func TestPiProductionLaunchBuilderIsPureMapping(t *testing.T) {
	const node = "/opt/pi/bin/node"
	const entry = "/opt/pi/bundle/cli.js"
	task := domain.TaskSpec{
		Metadata: domain.TaskMetadata{ID: "TASK-7"},
		Work:     domain.TaskWork{Objective: "Wire the production launch builder", Constraints: []string{"No new ADR", "Keep tests green"}},
		Worker:   domain.TaskWorker{ExecutionProfile: "workspace-write", Model: "anthropic/claude-sonnet-4"},
	}
	identity := productionruntime.AttemptLaunchIdentity{TaskID: "TASK-7", RunID: "run-7", AttemptID: "attempt-7"}
	builder := piProductionLaunchBuilder(node, entry, task)

	want, err := pi.BuildProductionLaunch(pi.ProductionLaunchInput{
		NodeRuntime: node, Entrypoint: entry,
		Profile: task.Worker.ExecutionProfile, Model: task.Worker.Model,
		TaskID: identity.TaskID, RunID: identity.RunID, AttemptID: identity.AttemptID,
		Objective: task.Work.Objective, Constraints: task.Work.Constraints,
	})
	if err != nil {
		t.Fatalf("BuildProductionLaunch: %v", err)
	}

	got, err := builder(identity)
	if err != nil {
		t.Fatalf("builder: %v", err)
	}
	if !slices.Equal(got.Argv, want.Argv) {
		t.Fatalf("builder argv =\n%q\nwant BuildProductionLaunch argv\n%q", got.Argv, want.Argv)
	}
	if got.Prompt != want.Prompt {
		t.Fatalf("builder prompt = %q, want %q", got.Prompt, want.Prompt)
	}
	// The builder is pure: a second call produces byte-identical output.
	again, err := builder(identity)
	if err != nil || !slices.Equal(again.Argv, got.Argv) || again.Prompt != got.Prompt {
		t.Fatalf("builder is not pure: again=%+v err=%v", again, err)
	}
	// The argv carries the deterministic noninteractive surface and ends with
	// the single trailing prompt.
	if !slices.Contains(got.Argv, "--mode") || !slices.Contains(got.Argv, "json") || !slices.Contains(got.Argv, "--print") {
		t.Fatalf("builder argv missing --mode json --print: %q", got.Argv)
	}
	if got.Argv[len(got.Argv)-1] != got.Prompt {
		t.Fatalf("builder argv does not end with the prompt: %q", got.Argv[len(got.Argv)-1])
	}
	if strings.Contains(strings.Join(got.Argv, " "), "bash") {
		t.Fatalf("builder argv grants bash: %q", got.Argv)
	}

	// A different identity produces a different prompt (and argv), proving the
	// precise reserved identity flows through to BuildProductionLaunch.
	other, err := builder(productionruntime.AttemptLaunchIdentity{TaskID: "TASK-7", RunID: "run-7", AttemptID: "attempt-8"})
	if err != nil {
		t.Fatalf("builder other: %v", err)
	}
	if other.Prompt == got.Prompt || slices.Equal(other.Argv, got.Argv) {
		t.Fatalf("builder did not vary with the attempt identity")
	}

	// A BuildProductionLaunch validation error propagates unchanged: the
	// builder adds no suppression and no argv is produced.
	if _, err := builder(productionruntime.AttemptLaunchIdentity{TaskID: "", RunID: "run-7", AttemptID: "attempt-7"}); err == nil {
		t.Fatal("builder accepted an empty TaskID that BuildProductionLaunch rejects")
	}
}
