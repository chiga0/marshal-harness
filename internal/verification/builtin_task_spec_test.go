package verification

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/verificationbuiltin"
)

func TestMalformedReservedBuiltinFailsBeforeVerifierSideEffects(t *testing.T) {
	runDirectory := filepath.Join(t.TempDir(), "must-not-exist")
	_, err := New().Verify(context.Background(), Input{
		TaskID: "task:reserved", RunID: "run:reserved", SpecDigest: "sha256:" + strings.Repeat("a", 64),
		Worktree: t.TempDir(), RunDirectory: runDirectory,
		Commands: []CommandSpec{{ID: "reserved", Argv: []string{"marshal-builtin:unknown:v1"}, CWD: ".", Timeout: time.Second, Required: true, BaselinePolicy: "none"}},
	})
	if err == nil || err.Error() != verificationbuiltin.ReasonDenied {
		t.Fatalf("error = %v, want %s", err, verificationbuiltin.ReasonDenied)
	}
	if _, statErr := os.Stat(runDirectory); !os.IsNotExist(statErr) {
		t.Fatalf("malformed builtin reached verifier side effects: %v", statErr)
	}
}

func TestValidReservedBuiltinRequiresCandidateAuthorityBeforeVerifierSideEffects(t *testing.T) {
	runDirectory := filepath.Join(t.TempDir(), "must-not-exist")
	_, err := New().Verify(context.Background(), Input{
		TaskID: "task:reserved", RunID: "run:reserved", SpecDigest: "sha256:" + strings.Repeat("a", 64),
		Worktree: t.TempDir(), RunDirectory: runDirectory,
		Deliverables: []Deliverable{{ID: "task-spec", Required: true, PathGlob: "out/task.json", MinimumCount: 1}},
		Commands:     []CommandSpec{{ID: "reserved", Argv: []string{verificationbuiltin.TaskSpecV1, "deliverable:task-spec"}, CWD: ".", Timeout: time.Second, Required: true, BaselinePolicy: "none"}},
	})
	if err == nil || err.Error() != verificationbuiltin.ReasonDenied {
		t.Fatalf("error = %v, want %s", err, verificationbuiltin.ReasonDenied)
	}
	if _, statErr := os.Stat(runDirectory); !os.IsNotExist(statErr) {
		t.Fatalf("builtin without Candidate authority reached side effects: %v", statErr)
	}
}

func TestBuiltinInventoryFailureUsesClosedPathlessRecordAndLogs(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.candidateInput()
	input.Deliverables = []Deliverable{{ID: "task-spec", Kind: "documentation", Required: true, PathGlob: "missing/task.json", MinimumCount: 1}}
	input.Commands = []CommandSpec{{ID: "validate", Argv: []string{verificationbuiltin.TaskSpecV1, "deliverable:task-spec"}, CWD: ".", Timeout: time.Second, Required: true, BaselinePolicy: "none"}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	var commandGate Gate
	for _, gate := range result.Report.Gates {
		if gate.ID == "command:validate" {
			commandGate = gate
		}
	}
	if commandGate.Status != "error" || commandGate.Summary != reasonDeliverableDenied || commandGate.Command == nil || commandGate.Command.Executable != verificationbuiltin.TaskSpecV1 {
		t.Fatalf("command gate = %+v", commandGate)
	}
	stderr, err := os.ReadFile(filepath.Join(input.RunDirectory, "logs", "validate.stderr.log"))
	if err != nil || string(stderr) != reasonDeliverableDenied+"\n" {
		t.Fatalf("stderr = %q err=%v", stderr, err)
	}
}

func TestRunnerNeverFallsBackReservedNamespace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("reserved executable fixture uses Unix file names")
	}
	bin := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "must-not-exist")
	executable := filepath.Join(bin, verificationbuiltin.TaskSpecV1)
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf reached >\"$SENTINEL\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	result := (Runner{Environment: []string{"PATH=" + bin, "SENTINEL=" + sentinel}}).Run(context.Background(), t.TempDir(), CommandSpec{
		Argv: []string{verificationbuiltin.TaskSpecV1, "deliverable:any"}, CWD: ".", Timeout: time.Second,
	})
	if result.Status != "error" || result.Record.Executable != verificationbuiltin.ReservedPrefix+"denied" {
		t.Fatalf("reserved runner result = %+v", result)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("reserved command fell through to PATH: %v", err)
	}
}

func TestBuiltinArtifactBindingRequiresOneExactValidatedDigest(t *testing.T) {
	plan := verificationbuiltin.Plan{DeliverableID: "task-spec", PathGlob: "out/task.json"}
	digest := "sha256:" + strings.Repeat("a", 64)
	candidate := "sha256:" + strings.Repeat("b", 64)
	stale := "sha256:" + strings.Repeat("c", 64)
	valid := ArtifactManifest{Artifacts: []Artifact{{ID: "task-spec", Required: true, Producer: "worker", Status: "validated", PathRoot: "repository", RelativePath: plan.PathGlob, Digest: digest, CandidateDigest: candidate}}}
	if !builtinArtifactBindingMatches(valid, plan, digest, candidate) {
		t.Fatal("exact artifact binding was rejected")
	}
	for name, manifest := range map[string]ArtifactManifest{
		"wrong-digest":    {Artifacts: []Artifact{{ID: "task-spec", Required: true, Producer: "worker", Status: "validated", PathRoot: "repository", RelativePath: plan.PathGlob, Digest: stale, CandidateDigest: candidate}}},
		"wrong-candidate": {Artifacts: []Artifact{{ID: "task-spec", Required: true, Producer: "worker", Status: "validated", PathRoot: "repository", RelativePath: plan.PathGlob, Digest: digest, CandidateDigest: stale}}},
		"wrong-path":      {Artifacts: []Artifact{{ID: "task-spec", Required: true, Producer: "worker", Status: "validated", PathRoot: "repository", RelativePath: "out/other.json", Digest: digest, CandidateDigest: candidate}}},
		"invalid":         {Artifacts: []Artifact{{ID: "task-spec", Required: true, Producer: "worker", Status: "invalid", PathRoot: "repository", RelativePath: plan.PathGlob, Digest: digest, CandidateDigest: candidate}}},
		"duplicate":       {Artifacts: []Artifact{{ID: "task-spec", Required: true, Producer: "worker", Status: "validated", PathRoot: "repository", RelativePath: plan.PathGlob, Digest: digest, CandidateDigest: candidate}, {ID: "task-spec", Required: true, Producer: "worker", Status: "validated", PathRoot: "repository", RelativePath: plan.PathGlob, Digest: digest, CandidateDigest: candidate}}},
	} {
		if builtinArtifactBindingMatches(manifest, plan, digest, candidate) {
			t.Fatalf("%s artifact binding was accepted", name)
		}
	}
}

func TestResolveBuiltinExecutionPlanRequiresOneValidatedInventoryArtifact(t *testing.T) {
	plan := verificationbuiltin.Plan{DeliverableID: "task-spec", PathGlob: "out/*.json"}
	manifest := ArtifactManifest{Artifacts: []Artifact{{ID: "task-spec", Required: true, Producer: "worker", Status: "validated", PathRoot: "repository", RelativePath: "out/task.json", CandidateDigest: "sha256:candidate"}}}
	resolved, err := resolveBuiltinExecutionPlan(manifest, plan, "sha256:candidate")
	if err != nil || resolved.PathGlob != "out/task.json" {
		t.Fatalf("resolved = %+v err=%v", resolved, err)
	}
	for name, candidate := range map[string]ArtifactManifest{
		"missing":    {},
		"invalid":    {Artifacts: []Artifact{{ID: "task-spec", Required: true, Producer: "worker", Status: "invalid", PathRoot: "repository", RelativePath: "out/task.json", CandidateDigest: "sha256:candidate"}}},
		"multiple":   {Artifacts: []Artifact{{ID: "task-spec:1", Required: true, Producer: "worker", Status: "validated", PathRoot: "repository", RelativePath: "out/a.json", CandidateDigest: "sha256:candidate"}, {ID: "task-spec:2", Required: true, Producer: "worker", Status: "validated", PathRoot: "repository", RelativePath: "out/b.json", CandidateDigest: "sha256:candidate"}}},
		"wrong-root": {Artifacts: []Artifact{{ID: "task-spec", Required: true, Producer: "worker", Status: "validated", PathRoot: "run", RelativePath: "out/task.json", CandidateDigest: "sha256:candidate"}}},
		"stale":      {Artifacts: []Artifact{{ID: "task-spec", Required: true, Producer: "worker", Status: "validated", PathRoot: "repository", RelativePath: "out/task.json", CandidateDigest: "sha256:stale"}}},
	} {
		if _, err := resolveBuiltinExecutionPlan(candidate, plan, "sha256:candidate"); err == nil || err.Error() != reasonDeliverableDenied {
			t.Fatalf("%s resolve error = %v", name, err)
		}
	}
}
