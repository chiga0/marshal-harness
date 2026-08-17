package verification

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
)

func TestVerifierEndToEndPassesWithUntrackedDeliverable(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.MkdirAll(filepath.Join(fixture.worktree.Path, "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "pkg", "code.go"), []byte("package pkg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"pkg/**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	input.Deliverables = []Deliverable{{ID: "source", Kind: "code", Required: true, PathGlob: "pkg/*.go", MinimumCount: 1}}
	input.Commands = []CommandSpec{{ID: "exists", Argv: []string{"sh", "-c", "test -f pkg/code.go"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "pass" || !result.Report.Observed.HasUntrackedFiles {
		t.Fatalf("report = %+v", result.Report)
	}
	for _, name := range []string{"observed.patch", "verification-report.json", "artifact-manifest.json"} {
		if _, err := os.Stat(filepath.Join(input.RunDirectory, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}

func TestVerifyNormalizesGoFilesBeforeCommandGates(t *testing.T) {
	fixture := newVerificationFixture(t)
	writeFixtureGoFile(t, fixture.worktree.Path, "pkg/code.go", unformattedGoSource)
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"pkg/**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	input.Deliverables = []Deliverable{{ID: "source", Kind: "code", Required: true, PathGlob: "pkg/*.go", MinimumCount: 1}}
	input.Commands = []CommandSpec{{ID: "format-check", Argv: []string{"sh", "-c", "test -z \"$(gofmt -l pkg/code.go)\""}, CWD: ".", Timeout: 10 * time.Second, Required: true, MaxLogBytes: 4096}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "pass" {
		t.Fatalf("normalized report = %+v", result.Report)
	}
	scopeIndex, formatIndex, commandIndex := -1, -1, -1
	var formatGate *Gate
	for index := range result.Report.Gates {
		switch result.Report.Gates[index].ID {
		case "scope:changed-paths":
			scopeIndex = index
		case "format:normalize":
			formatIndex = index
			formatGate = &result.Report.Gates[index]
		case "command:format-check":
			commandIndex = index
		}
	}
	if formatGate == nil || formatGate.Status != "pass" || formatGate.Required || formatGate.Category != "other" {
		t.Fatalf("format gate = %+v", formatGate)
	}
	if len(formatGate.Evidence) != 1 || formatGate.Evidence[0] != "normalized:pkg/code.go" {
		t.Fatalf("format gate evidence = %+v", formatGate.Evidence)
	}
	if scopeIndex < 0 || commandIndex < 0 || scopeIndex >= formatIndex || formatIndex >= commandIndex {
		t.Fatalf("gate order scope=%d format=%d command=%d", scopeIndex, formatIndex, commandIndex)
	}
	data, err := os.ReadFile(filepath.Join(fixture.worktree.Path, "pkg", "code.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != formattedGoSource {
		t.Fatalf("worktree bytes after verification = %q", data)
	}
	var patchArtifact *Artifact
	for index := range result.Manifest.Artifacts {
		if result.Manifest.Artifacts[index].ID == "evidence:observed-patch" {
			patchArtifact = &result.Manifest.Artifacts[index]
		}
	}
	if patchArtifact == nil || patchArtifact.Digest != canonical.DigestBytes(result.Report.Observed.Patch) {
		t.Fatalf("observed patch artifact = %+v", patchArtifact)
	}
	persisted, err := os.ReadFile(filepath.Join(input.RunDirectory, "observed.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != string(result.Report.Observed.Patch) {
		t.Fatal("persisted observed patch diverges from the post-normalization report")
	}
}

func TestVerifyFailsClosedWhenNormalizationFails(t *testing.T) {
	fixture := newVerificationFixture(t)
	writeFixtureGoFile(t, fixture.worktree.Path, "pkg/broken.go", "package pkg\n\nfunc { oops\n")
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"pkg/**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{{ID: "unreached", Argv: []string{"sh", "-c", "true"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "format:normalize") != "fail" {
		t.Fatalf("fail-closed report = %+v", result.Report)
	}
	if gateStatus(result.Report.Gates, "command:unreached") != "missing" {
		t.Fatalf("command gate ran after normalization failure: %+v", result.Report.Gates)
	}
}

// Issue #142: the deterministic empty Observation bound by the early exits
// must be byte-compatible with a real observation of a change-free worktree,
// so review-time re-observation reproduces the frozen report identities.
func TestEmptyObservationMatchesCleanTreeObservation(t *testing.T) {
	fixture := newVerificationFixture(t)
	want, err := emptyObservation()
	if err != nil {
		t.Fatal(err)
	}
	observed, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if observed.SnapshotDigest != want.SnapshotDigest || observed.DiffDigest != want.DiffDigest {
		t.Fatalf("empty observation diverges from a clean-tree observation: empty=%+v clean=%+v", want, observed)
	}
	if want.DiffDigest != canonical.DigestBytes(nil) {
		t.Fatalf("empty observation diff digest must recompute from empty bytes: %s", want.DiffDigest)
	}
}

// Issue #142: a legacy fail-closed normalization exit must keep persisting
// the observed patch together with an artifact whose digest and byte size
// recompute from the persisted bytes, so ReviewPacket construction never
// strands the failed run.
func TestVerifyLegacyFormatFailureKeepsObservedPatchRecomputable(t *testing.T) {
	fixture := newVerificationFixture(t)
	writeFixtureGoFile(t, fixture.worktree.Path, "pkg/broken.go", "package pkg\n\nfunc { oops\n")
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"pkg/**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "format:normalize") != "fail" {
		t.Fatalf("fail-closed report = %+v", result.Report)
	}
	persisted, err := os.ReadFile(filepath.Join(input.RunDirectory, "observed.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != string(result.Report.Observed.Patch) || len(persisted) == 0 {
		t.Fatal("persisted observed patch diverges from the failed run's observation")
	}
	patchArtifact := findArtifact(t, result.Manifest, "evidence:observed-patch")
	if patchArtifact.Status != "validated" || patchArtifact.Digest != canonical.DigestBytes(persisted) || patchArtifact.ByteSize != int64(len(persisted)) || patchArtifact.CandidateDigest != "" {
		t.Fatalf("legacy failed-run observed-patch artifact must recompute from the persisted bytes: %+v", patchArtifact)
	}
	if !slices.Equal(patchArtifact.RelatedGates, []string{"diff:observe", "scope:changed-paths"}) {
		t.Fatalf("legacy failed-run observed-patch relatedGates changed: %+v", patchArtifact.RelatedGates)
	}
	assertPersistedEvidenceSchemaLegal(t, input.RunDirectory)
}

// Issue #142 (M10 scenario): candidate mode defers the observed-patch
// artifact until the head content is known, so a format:normalize failure
// used to persist observed.patch without any manifest artifact, breaking
// ReviewPacket construction. The failed run must now persist a validated,
// content-addressed observed-patch artifact without faking a head Candidate
// binding or a passing gate.
func TestVerifyCandidateFormatFailurePersistsReviewableObservedPatch(t *testing.T) {
	fixture := newVerificationFixture(t)
	writeFixtureGoFile(t, fixture.worktree.Path, "pkg/broken.go", "package pkg\n\nfunc { oops\n")
	rawObservation, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	input := fixture.candidateInput()
	input.Scope = ScopePolicy{AllowPaths: []string{"pkg/**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "format:normalize") != "fail" {
		t.Fatalf("candidate fail-closed report = %+v", result.Report)
	}
	if result.Report.CandidateDigest != "" || result.Report.WorkerCandidateDigest != "" {
		t.Fatalf("failed run must not carry candidate identities: %+v", result.Report)
	}
	persisted, err := os.ReadFile(filepath.Join(input.RunDirectory, "observed.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != string(rawObservation.Patch) || string(persisted) != string(result.Report.Observed.Patch) {
		t.Fatal("failed run must persist the worker's real observed bytes")
	}
	workerPersisted, err := os.ReadFile(filepath.Join(input.RunDirectory, "worker.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if string(workerPersisted) != string(persisted) {
		t.Fatal("without normalization the worker and observed patches must agree")
	}
	if len(result.Manifest.Artifacts) != 1 {
		t.Fatalf("failed candidate run must persist exactly the observed-patch artifact: %+v", result.Manifest.Artifacts)
	}
	patchArtifact := findArtifact(t, result.Manifest, "evidence:observed-patch")
	if patchArtifact.Status != "validated" || patchArtifact.Producer != "verifier" || patchArtifact.RelativePath != "observed.patch" ||
		patchArtifact.Digest != canonical.DigestBytes(persisted) || patchArtifact.ByteSize != int64(len(persisted)) || patchArtifact.CandidateDigest != "" {
		t.Fatalf("failed candidate run observed-patch artifact = %+v", patchArtifact)
	}
	if !slices.Equal(patchArtifact.RelatedGates, []string{"diff:observe", "scope:changed-paths", "format:normalize"}) {
		t.Fatalf("failed candidate run observed-patch relatedGates = %+v", patchArtifact.RelatedGates)
	}
	store := newLocalCandidateStore(input.RunDirectory)
	records, err := store.records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ProducerKind != domain.ProducerKindWorker {
		t.Fatalf("failed candidate run must admit only the worker candidate: %+v", records)
	}
	assertPersistedEvidenceSchemaLegal(t, input.RunDirectory)
}

func TestRenameRequiresBothPathsInScope(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.MkdirAll(filepath.Join(fixture.worktree.Path, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.worktree.Path, "mv", "README.md", "docs/README.md")
	observation, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	gate := EvaluateScope(observation, ScopePolicy{AllowPaths: []string{"docs/**"}, MaxDiffBytes: 1 << 20, MaxChangedFiles: 10})
	if gate.Status != "fail" || !strings.Contains(gate.Summary, "README.md") {
		t.Fatalf("scope gate = %+v", gate)
	}
}

func TestWorkerDeclarationCannotOverrideScope(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "forbidden.txt"), []byte("claimed allowed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"src/**"}, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20}
	input.WorkerDeclaredPaths = []string{"forbidden.txt"}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "scope:changed-paths") != "fail" {
		t.Fatalf("worker declaration changed scope result: %+v", result.Report)
	}
}

func TestOversizedDiffFailsScope(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "large.txt"), []byte(strings.Repeat("x", 8192)), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	gate := EvaluateScope(observation, ScopePolicy{AllowPaths: []string{"**"}, MaxChangedFiles: 10, MaxDiffBytes: 64})
	if gate.Status != "fail" || !strings.Contains(gate.Summary, "Diff 字节数") {
		t.Fatalf("scope gate = %+v", gate)
	}
}

func TestDefaultPatchCaptureLimitAllowsOrdinaryDiff(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "ordinary.txt"), []byte("ordinary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.PatchCaptureBytes = 0
	input.Scope = ScopePolicy{AllowPaths: []string{"ordinary.txt"}, MaxChangedFiles: 5}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "pass" || result.Report.Observed.DiffTruncated {
		t.Fatalf("default capture report = %+v", result.Report)
	}
}

func TestArtifactTraversalAndSymlinkEscapeFail(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	_, gates := CollectArtifacts(root, []Deliverable{{ID: "escape", Kind: "report", Required: true, PathGlob: "escape.txt", MinimumCount: 1}, {ID: "traversal", Kind: "report", Required: true, PathGlob: "../*", MinimumCount: 1}}, time.Now())
	if gateStatus(gates, "artifact:escape") != "fail" || gateStatus(gates, "artifact:traversal") != "error" {
		t.Fatalf("artifact gates = %+v", gates)
	}
}

func TestInvalidSymlinkArtifactStillPersistsEvidence(t *testing.T) {
	fixture := newVerificationFixture(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.worktree.Path, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"escape.txt"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	input.Deliverables = []Deliverable{{ID: "escape", Kind: "report", Required: true, PathGlob: "escape.txt", MinimumCount: 1}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "scope:changed-paths") != "fail" || gateStatus(result.Report.Gates, "artifact:escape") != "fail" {
		t.Fatalf("symlink escape report = %+v", result.Report)
	}
	for _, name := range []string{"verification-report.json", "artifact-manifest.json"} {
		if _, err := os.Stat(filepath.Join(input.RunDirectory, name)); err != nil {
			t.Fatalf("missing failure evidence %s: %v", name, err)
		}
	}
}

func TestRunnerResolvesRelativeExecutableFromCommandCWD(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "tools")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(bin, "check.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonicalScript, err := filepath.EvalSymlinks(script)
	if err != nil {
		t.Fatal(err)
	}
	result := (Runner{}).Run(context.Background(), root, CommandSpec{ID: "relative", Argv: []string{"./check.sh"}, CWD: "tools", Timeout: 10 * time.Second, Required: true})
	if result.Status != "pass" || result.Record.Executable != canonicalScript {
		t.Fatalf("relative executable result = %+v", result)
	}
	outsideDirectory := t.TempDir()
	outsideScript := filepath.Join(outsideDirectory, "outside.sh")
	if err := os.WriteFile(outsideScript, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	escapingPath, err := filepath.Rel(bin, outsideScript)
	if err != nil {
		t.Fatal(err)
	}
	result = (Runner{}).Run(context.Background(), root, CommandSpec{ID: "escape", Argv: []string{escapingPath}, CWD: "tools", Timeout: 10 * time.Second, Required: true})
	if result.Status != "error" {
		t.Fatalf("escaping executable result = %+v", result)
	}
}

func TestObserveHonorsCancelledContext(t *testing.T) {
	fixture := newVerificationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ObserveContext(ctx, fixture.worktree.Path, fixture.baseSHA, 1<<20); err == nil {
		t.Fatal("ObserveContext accepted a cancelled context")
	}
}

func TestRepositoryIntegrityHonorsCancelledContext(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gate := verifyRepository(ctx, input)
	if gate.Status != "error" {
		t.Fatalf("cancelled repository gate = %+v", gate)
	}
}

func TestScopeRejectsSymlinkThroughEscapingIntermediateComponent(t *testing.T) {
	fixture := newVerificationFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "target.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.worktree.Path, "redirect")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("redirect/target.txt", filepath.Join(fixture.worktree.Path, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	observation, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	gate := EvaluateScope(observation, ScopePolicy{AllowPaths: []string{"**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20})
	if gate.Status != "fail" || !strings.Contains(gate.Summary, "Symlink") {
		t.Fatalf("intermediate symlink gate = %+v, observation = %+v", gate, observation)
	}
}

func TestSubmoduleMutationFailsWhenNotAllowed(t *testing.T) {
	submodule := t.TempDir()
	gitTest(t, submodule, "init", "-q")
	gitTest(t, submodule, "config", "user.name", "Marshal Test")
	gitTest(t, submodule, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(submodule, "value.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, submodule, "add", "value.txt")
	gitTest(t, submodule, "commit", "-q", "-m", "one")
	firstSHA := strings.TrimSpace(gitTest(t, submodule, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(submodule, "value.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, submodule, "commit", "-q", "-am", "two")
	secondSHA := strings.TrimSpace(gitTest(t, submodule, "rev-parse", "HEAD"))

	parent := t.TempDir()
	gitTest(t, parent, "init", "-q")
	gitTest(t, parent, "config", "user.name", "Marshal Test")
	gitTest(t, parent, "config", "user.email", "marshal@example.invalid")
	gitTest(t, parent, "-c", "protocol.file.allow=always", "submodule", "add", "-q", submodule, "modules/dep")
	gitTest(t, filepath.Join(parent, "modules", "dep"), "checkout", "-q", firstSHA)
	gitTest(t, parent, "add", ".gitmodules", "modules/dep")
	gitTest(t, parent, "commit", "-q", "-m", "base with submodule")
	base := strings.TrimSpace(gitTest(t, parent, "rev-parse", "HEAD"))
	state, err := marshalRepository.Discover(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Init(); err != nil {
		t.Fatal(err)
	}
	manager, err := gitworktree.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(state.StateRoot, "task:submodule", base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = worktree.Release()
		_ = exec.Command("git", "-C", parent, "worktree", "remove", "--force", worktree.Path).Run()
		_ = exec.Command("git", "-C", parent, "branch", "-D", worktree.Branch).Run()
	})
	gitTest(t, worktree.Path, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "-q")
	gitTest(t, filepath.Join(worktree.Path, "modules", "dep"), "checkout", "-q", secondSHA)
	observation, err := Observe(worktree.Path, base, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	gate := EvaluateScope(observation, ScopePolicy{AllowPaths: []string{"modules/**"}, AllowSubmodules: false, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20})
	if gate.Status != "fail" || !strings.Contains(gate.Summary, "Submodule") {
		t.Fatalf("submodule gate = %+v, observation = %+v", gate, observation)
	}
}

func TestVerifyAppliesToolAllowlistGateEndToEnd(t *testing.T) {
	stageAttempt := func(t *testing.T, fixture verificationFixture, tools []string, toolNames []string) Input {
		t.Helper()
		if err := os.MkdirAll(fixture.runDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		worker := map[string]any{"preferredAdapter": "fake", "executionProfile": "workspace-write", "sessionPolicy": "ephemeral"}
		if tools != nil {
			worker["tools"] = tools
		}
		specData, err := json.Marshal(map[string]any{"apiVersion": "marshal.dev/v1alpha1", "kind": "Task", "worker": worker})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.runDirectory, "task-spec.json"), specData, 0o600); err != nil {
			t.Fatal(err)
		}
		specDigest, err := canonical.DigestJSON(specData)
		if err != nil {
			t.Fatal(err)
		}
		attemptDir := filepath.Join(fixture.runDirectory, "attempts", "attempt-1")
		outputDir := filepath.Join(attemptDir, "control", "output")
		if err := os.MkdirAll(outputDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(attemptDir, "worker-request.json"), []byte(`{"attemptNumber":1}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if toolNames != nil {
			metaData, err := json.Marshal(map[string]any{"toolNames": toolNames})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outputDir, "fake-transcript-meta.json"), metaData, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		input := fixture.input()
		input.SpecDigest = specDigest
		input.Scope = ScopePolicy{AllowPaths: []string{"**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
		return input
	}
	t.Run("compliant-declaration-passes", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		input := stageAttempt(t, fixture, []string{"read", "edit"}, []string{"read"})
		result, err := New().Verify(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Report.Status != "pass" || gateStatus(result.Report.Gates, "tool-allowlist") != "pass" {
			t.Fatalf("report = %+v", result.Report)
		}
	})
	t.Run("violation-fails-report", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		input := stageAttempt(t, fixture, []string{"read", "edit"}, []string{"read", "grep"})
		result, err := New().Verify(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "tool-allowlist") != "fail" {
			t.Fatalf("report = %+v", result.Report)
		}
		if _, err := os.Stat(filepath.Join(input.RunDirectory, toolAllowlistEvidenceFileName)); err != nil {
			t.Fatalf("violation evidence missing: %v", err)
		}
	})
	t.Run("undeclared-keeps-report-green", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		input := stageAttempt(t, fixture, nil, []string{"read", "bash"})
		result, err := New().Verify(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Report.Status != "pass" || gateStatus(result.Report.Gates, "tool-allowlist") != "skipped" {
			t.Fatalf("undeclared runs must keep the gate skipped and the report green: %+v", result.Report)
		}
	})
	t.Run("declared-but-evidence-missing-fails-closed", func(t *testing.T) {
		fixture := newVerificationFixture(t)
		input := stageAttempt(t, fixture, []string{"read"}, nil)
		result, err := New().Verify(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "tool-allowlist") != "fail" {
			t.Fatalf("missing evidence must fail closed: %+v", result.Report)
		}
	})
}

func TestVerifierDetectsDirtyCommandOutput(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktree.Path, "source.txt"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"source.txt"}, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{{ID: "dirty", Argv: []string{"sh", "-c", "echo generated > verifier-output.txt"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" || gateStatus(result.Report.Gates, "command:dirty") != "fail" {
		t.Fatalf("dirty verifier report = %+v", result.Report)
	}
}

func TestOnFailureBaselineClassifiesWithoutWaivingFailure(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.Remove(filepath.Join(fixture.worktree.Path, "README.md")); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.BaselinePath = fixture.repository
	input.Scope = ScopePolicy{AllowPaths: []string{"README.md"}, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{{ID: "readme", Argv: []string{"sh", "-c", "test -f README.md"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096, BaselinePolicy: "on-failure"}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" {
		t.Fatalf("baseline waived changed failure: %+v", result.Report)
	}
	for _, gate := range result.Report.Gates {
		if gate.ID == "command:readme" && (gate.Command == nil || gate.Command.BaselineStatus != "pass") {
			t.Fatalf("baseline status = %+v", gate.Command)
		}
	}
}

// Issue #87 baseline regression verdicts: comparing the candidate outcome
// with the locked-baseline rerun only produces a mark; it never changes
// the candidate's pass/fail outcome.

func TestBaselineRegressionVerdictMatrix(t *testing.T) {
	for _, test := range []struct {
		name        string
		candidate   string
		baseline    string
		wantVerdict string
	}{
		{name: "candidate fail baseline pass is regression confirmed", candidate: "fail", baseline: "pass", wantVerdict: BaselineVerdictRegressionConfirmed},
		{name: "candidate fail baseline fail is pre-existing", candidate: "fail", baseline: "fail", wantVerdict: BaselineVerdictPreExistingFailure},
		{name: "candidate fail baseline error carries no verdict", candidate: "fail", baseline: "error"},
		{name: "candidate fail baseline not-run carries no verdict", candidate: "fail", baseline: "not-run"},
		{name: "candidate pass baseline pass carries no verdict", candidate: "pass", baseline: "pass"},
		{name: "candidate pass baseline fail carries no verdict", candidate: "pass", baseline: "fail"},
		{name: "candidate error baseline pass carries no verdict", candidate: "error", baseline: "pass"},
		{name: "candidate cancelled baseline fail carries no verdict", candidate: "cancelled", baseline: "fail"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if verdict := baselineRegressionVerdict(test.candidate, test.baseline); verdict != test.wantVerdict {
				t.Fatalf("baselineRegressionVerdict(%q, %q) = %q, want %q", test.candidate, test.baseline, verdict, test.wantVerdict)
			}
		})
	}
}

func commandGateByID(t *testing.T, gates []Gate, id string) Gate {
	t.Helper()
	for _, gate := range gates {
		if gate.ID == id {
			return gate
		}
	}
	t.Fatalf("gate %s missing: %+v", id, gates)
	return Gate{}
}

func hasBaselineVerdictEvidence(gate Gate, verdict string) bool {
	want := baselineVerdictEvidencePrefix + verdict
	for _, evidence := range gate.Evidence {
		if evidence == want {
			return true
		}
	}
	return false
}

func TestOnFailureBaselineMarksRegressionConfirmed(t *testing.T) {
	fixture := newVerificationFixture(t)
	if err := os.Remove(filepath.Join(fixture.worktree.Path, "README.md")); err != nil {
		t.Fatal(err)
	}
	input := fixture.input()
	input.BaselinePath = fixture.repository
	input.Scope = ScopePolicy{AllowPaths: []string{"README.md"}, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{{ID: "readme", Argv: []string{"sh", "-c", "test -f README.md"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096, BaselinePolicy: "on-failure"}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" {
		t.Fatalf("regression verdict waived the candidate failure: %+v", result.Report)
	}
	gate := commandGateByID(t, result.Report.Gates, "command:readme")
	if gate.Status != "fail" {
		t.Fatalf("command gate status = %q, want fail", gate.Status)
	}
	if gate.Command == nil || gate.Command.BaselineStatus != "pass" {
		t.Fatalf("baseline status = %+v, want pass", gate.Command)
	}
	if !hasBaselineVerdictEvidence(gate, BaselineVerdictRegressionConfirmed) {
		t.Fatalf("gate evidence = %+v, want %s mark", gate.Evidence, baselineVerdictEvidencePrefix+BaselineVerdictRegressionConfirmed)
	}
	if !strings.Contains(gate.Summary, BaselineVerdictRegressionConfirmed) {
		t.Fatalf("gate summary %q does not carry the regression verdict", gate.Summary)
	}
}

func TestAlwaysBaselineMarksPreExistingFailure(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	input.BaselinePath = fixture.repository
	input.Scope = ScopePolicy{AllowPaths: []string{"README.md"}, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{{ID: "missing", Argv: []string{"sh", "-c", "test -f NO-SUCH-BASELINE-FILE"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096, BaselinePolicy: "always"}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "fail" {
		t.Fatalf("pre-existing verdict waived the candidate failure: %+v", result.Report)
	}
	gate := commandGateByID(t, result.Report.Gates, "command:missing")
	if gate.Status != "fail" {
		t.Fatalf("command gate status = %q, want fail", gate.Status)
	}
	if gate.Command == nil || gate.Command.BaselineStatus != "fail" {
		t.Fatalf("baseline status = %+v, want fail", gate.Command)
	}
	if !hasBaselineVerdictEvidence(gate, BaselineVerdictPreExistingFailure) {
		t.Fatalf("gate evidence = %+v, want %s mark", gate.Evidence, baselineVerdictEvidencePrefix+BaselineVerdictPreExistingFailure)
	}
	if !strings.Contains(gate.Summary, BaselineVerdictPreExistingFailure) {
		t.Fatalf("gate summary %q does not carry the pre-existing verdict", gate.Summary)
	}
}

func TestNoBaselineVerdictWithoutBaselinePolicy(t *testing.T) {
	fixture := newVerificationFixture(t)
	input := fixture.input()
	input.BaselinePath = fixture.repository
	input.Scope = ScopePolicy{AllowPaths: []string{"README.md"}, MaxChangedFiles: 10, MaxDiffBytes: 1 << 20}
	input.Commands = []CommandSpec{{ID: "none-policy", Argv: []string{"sh", "-c", "test -f NO-SUCH-FILE"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096, BaselinePolicy: "none"}}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	gate := commandGateByID(t, result.Report.Gates, "command:none-policy")
	if gate.Status != "fail" {
		t.Fatalf("command gate status = %q, want fail", gate.Status)
	}
	if gate.Command == nil || gate.Command.BaselineStatus != "not-run" {
		t.Fatalf("baseline status = %+v, want not-run", gate.Command)
	}
	for _, evidence := range gate.Evidence {
		if strings.HasPrefix(evidence, baselineVerdictEvidencePrefix) {
			t.Fatalf("baselinePolicy none produced a verdict mark: %+v", gate.Evidence)
		}
	}
	if strings.Contains(gate.Summary, BaselineVerdictRegressionConfirmed) || strings.Contains(gate.Summary, BaselineVerdictPreExistingFailure) {
		t.Fatalf("baselinePolicy none altered the summary with a verdict: %q", gate.Summary)
	}
}

func TestRunnerCancellationTerminatesProcessGroup(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process-group test is Unix-specific")
	}
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	resultChannel := make(chan CommandResult, 1)
	go func() {
		resultChannel <- (Runner{}).Run(ctx, root, CommandSpec{ID: "cancel", Argv: []string{"sh", "-c", "sleep 30 & echo $! > child.pid; wait"}, CWD: ".", Timeout: 30 * time.Second, Required: true, MaxLogBytes: 4096})
	}()
	pidPath := filepath.Join(root, "child.pid")
	deadline := time.Now().Add(5 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidPath)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child process PID was not recorded")
	}
	cancel()
	result := <-resultChannel
	if result.Status != "cancelled" {
		t.Fatalf("command status = %s", result.Status)
	}
	time.Sleep(50 * time.Millisecond)
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child process %d survived cancellation: %v", pid, err)
	}
}

func TestRunnerBoundsLogs(t *testing.T) {
	result := (Runner{}).Run(context.Background(), t.TempDir(), CommandSpec{ID: "logs", Argv: []string{"sh", "-c", "yes x | head -c 10000"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 1024})
	if result.Status != "pass" || !result.Record.Truncated || len(result.Stdout) > 1100 {
		t.Fatalf("bounded result status=%s truncated=%v bytes=%d", result.Status, result.Record.Truncated, len(result.Stdout))
	}
}

func TestRunnerCapturesSignaledCommand(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("real signal capture is Unix-specific")
	}
	result := (Runner{}).Run(context.Background(), t.TempDir(), CommandSpec{ID: "signal-term", Argv: []string{"sh", "-c", "kill -TERM $$"}, CWD: ".", Timeout: 5 * time.Second, Required: true, MaxLogBytes: 4096})
	if result.Status != "fail" {
		t.Fatalf("command status = %q, want fail", result.Status)
	}
	if result.Record.ExitCode == nil {
		t.Fatal("ExitCode is nil; runner must record ProcessState.ExitCode for signaled processes")
	}
	if *result.Record.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1", *result.Record.ExitCode)
	}
	wantSignal := syscall.SIGTERM.String()
	if result.Record.Signal == nil {
		t.Fatal("Signal is nil; runner must record WaitStatus.Signal().String() for signaled processes")
	}
	if got := *result.Record.Signal; got != wantSignal {
		t.Fatalf("Signal = %q, want %q", got, wantSignal)
	}
	summary := commandSummary(result)
	if !strings.Contains(summary, wantSignal) {
		t.Fatalf("summary %q does not contain real signal %q", summary, wantSignal)
	}
	exitCodePart := "退出码 " + strconv.Itoa(*result.Record.ExitCode)
	if !strings.Contains(summary, exitCodePart) {
		t.Fatalf("summary %q does not contain %q", summary, exitCodePart)
	}
}

func TestCommandSummaryFormatsExitCodeValue(t *testing.T) {
	for _, code := range []int{0, 1, 127, -1} {
		exitCode := code
		summary := commandSummary(CommandResult{Status: "fail", Record: CommandRecord{ExitCode: &exitCode}})
		if summary != "命令状态 fail，退出码 "+strconv.Itoa(code) {
			t.Fatalf("exit code %d summary = %q", code, summary)
		}
		if strings.Contains(summary, "0x") {
			t.Fatalf("exit code %d summary leaks pointer form: %q", code, summary)
		}
	}
	// An empty signal with a non-nil exit code must fall back to the exit-only summary.
	emptySignal := ""
	for _, code := range []int{0, 1, -1} {
		exitCode := code
		summary := commandSummary(CommandResult{Status: "fail", Record: CommandRecord{ExitCode: &exitCode, Signal: &emptySignal}})
		if want := "命令状态 fail，退出码 " + strconv.Itoa(code); summary != want {
			t.Fatalf("empty signal exit code %d summary = %q, want %q", code, summary, want)
		}
	}
	passSummary := commandSummary(CommandResult{Status: "pass", Record: CommandRecord{DurationMilliseconds: 42}})
	if passSummary != "命令通过，耗时 42ms" {
		t.Fatalf("pass summary changed: %q", passSummary)
	}
}

func TestCommandSummaryFormatsSignalWithExitCode(t *testing.T) {
	cases := []struct {
		status   string
		signal   string
		exitCode int
	}{
		{"fail", "terminated", -1},
		{"fail", "killed", -1},
		{"error", "aborted", -1},
	}
	for _, tc := range cases {
		signal := tc.signal
		exitCode := tc.exitCode
		summary := commandSummary(CommandResult{Status: tc.status, Record: CommandRecord{ExitCode: &exitCode, Signal: &signal}})
		want := "命令状态 " + tc.status + "，signal " + tc.signal + "（退出码 " + strconv.Itoa(tc.exitCode) + "）"
		if summary != want {
			t.Fatalf("signal %s exit code %d summary = %q, want %q", tc.signal, tc.exitCode, summary, want)
		}
		if strings.Contains(summary, "0x") {
			t.Fatalf("signal %s summary leaks pointer form: %q", tc.signal, summary)
		}
	}
}

func TestCommandSummaryFormatsSignalWithoutExitCode(t *testing.T) {
	for _, name := range []string{"SIGKILL", "SIGTERM"} {
		signal := name
		summary := commandSummary(CommandResult{Status: "fail", Record: CommandRecord{Signal: &signal}})
		if summary != "命令状态 fail，signal "+name {
			t.Fatalf("signal %s summary = %q", name, summary)
		}
		if strings.Contains(summary, "0x") {
			t.Fatalf("signal %s summary leaks pointer form: %q", name, summary)
		}
	}
}

func TestCommandSummaryHandlesMissingProcessOutcome(t *testing.T) {
	emptySignal := ""
	for _, record := range []CommandRecord{{ExitCode: nil, Signal: nil}, {ExitCode: nil, Signal: &emptySignal}} {
		summary := commandSummary(CommandResult{Status: "error", Record: record})
		if summary != "命令状态 error，退出结果不可用" {
			t.Fatalf("missing process outcome summary = %q", summary)
		}
		if strings.Contains(summary, "0x") || strings.Contains(summary, "<nil>") {
			t.Fatalf("missing process outcome summary leaks Go internals: %q", summary)
		}
	}
}

type verificationFixture struct {
	t            *testing.T
	repository   string
	baseSHA      string
	worktree     *gitworktree.Worktree
	commonDir    string
	runDirectory string
}

func newVerificationFixture(t *testing.T) verificationFixture {
	t.Helper()
	repository := t.TempDir()
	gitTest(t, repository, "init", "-q")
	gitTest(t, repository, "config", "user.name", "Marshal Test")
	gitTest(t, repository, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "README.md")
	gitTest(t, repository, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gitTest(t, repository, "rev-parse", "HEAD"))
	state, err := marshalRepository.Discover(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Init(); err != nil {
		t.Fatal(err)
	}
	manager, err := gitworktree.Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(state.StateRoot, "task:"+strings.ReplaceAll(t.Name(), "/", "-"), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = worktree.Release()
		command := exec.Command("git", "-C", repository, "worktree", "remove", "--force", worktree.Path)
		_ = command.Run()
		command = exec.Command("git", "-C", repository, "branch", "-D", worktree.Branch)
		_ = command.Run()
	})
	return verificationFixture{t: t, repository: repository, baseSHA: base, worktree: worktree, commonDir: manager.CommonDir, runDirectory: filepath.Join(t.TempDir(), "run")}
}

func (f verificationFixture) input() Input {
	return Input{TaskID: "task:fixture", RunID: "run:fixture", SpecDigest: "sha256:" + strings.Repeat("a", 64), BaseSHA: f.baseSHA, Worktree: f.worktree.Path, ExpectedCommonDir: f.commonDir, RunDirectory: f.runDirectory, PatchCaptureBytes: 1 << 20}
}

func gateStatus(gates []Gate, id string) string {
	for _, gate := range gates {
		if gate.ID == id {
			return gate.Status
		}
	}
	return "missing"
}

func gitTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

// candidateInput switches the fixture into ADR 0027 candidate mode exactly as
// the CLI orchestration layer does: the Attempt identity plus the frozen
// local authority namespace digest (tenantNamespace=local,
// controlPlaneId=default, authorityScopeId=repository identity).
func (f verificationFixture) candidateInput() Input {
	f.t.Helper()
	input := f.input()
	input.AttemptID = "attempt:fixture"
	namespace, err := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: f.repository}.Digest()
	if err != nil {
		f.t.Fatal(err)
	}
	input.AuthorityNamespaceID = namespace
	return input
}

func findArtifact(t *testing.T, manifest ArtifactManifest, id string) Artifact {
	t.Helper()
	for _, artifact := range manifest.Artifacts {
		if artifact.ID == id {
			return artifact
		}
	}
	t.Fatalf("artifact %s missing from manifest: %+v", id, manifest.Artifacts)
	return Artifact{}
}

func TestVerifyCandidateDualRecordChainBindsWorkerAndNormalizer(t *testing.T) {
	fixture := newVerificationFixture(t)
	writeFixtureGoFile(t, fixture.worktree.Path, "pkg/code.go", unformattedGoSource)
	// Capture the worker's raw bytes before Verify; normalization must not
	// lose them (§7.1).
	rawObservation, err := Observe(fixture.worktree.Path, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	input := fixture.candidateInput()
	input.Scope = ScopePolicy{AllowPaths: []string{"pkg/**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "pass" {
		t.Fatalf("candidate-mode report = %+v", result.Report)
	}

	workerPatchData, err := os.ReadFile(filepath.Join(input.RunDirectory, "worker.patch"))
	if err != nil {
		t.Fatal(err)
	}
	observedPatchData, err := os.ReadFile(filepath.Join(input.RunDirectory, "observed.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if string(workerPatchData) != string(rawObservation.Patch) {
		t.Fatal("worker.patch must preserve the pre-normalization observation bytes")
	}
	if string(observedPatchData) == string(workerPatchData) {
		t.Fatal("normalization changed no bytes; fixture must drift")
	}
	if string(observedPatchData) != string(result.Report.Observed.Patch) {
		t.Fatal("observed.patch must bind the head observation")
	}

	store := newLocalCandidateStore(input.RunDirectory)
	records, err := store.records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("chain length = %d, want exactly 2 candidates: %+v", len(records), records)
	}
	if _, statErr := os.Stat(store.quarantineDir()); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("quarantine must stay absent without conflicts: %v", statErr)
	}

	worker, err := store.ByDigest(result.Report.WorkerCandidateDigest)
	if err != nil {
		t.Fatal(err)
	}
	head, err := store.ByDigest(result.Report.CandidateDigest)
	if err != nil {
		t.Fatal(err)
	}
	if worker.ProducerKind != domain.ProducerKindWorker || worker.Producer != candidateProducerWorker || worker.PredecessorCandidateDigest != "" {
		t.Fatalf("worker candidate is not a chain root: %+v", worker)
	}
	if head.ProducerKind != domain.ProducerKindNormalizer || head.Producer != candidateProducerNormalizer {
		t.Fatalf("head candidate is not the normalizer record: %+v", head)
	}
	if head.PredecessorCandidateDigest != worker.CandidateDigest {
		t.Fatalf("predecessor = %s, worker digest = %s", head.PredecessorCandidateDigest, worker.CandidateDigest)
	}
	for _, record := range []domain.Candidate{worker, head} {
		if record.TaskID != input.TaskID || record.RunID != input.RunID || record.AttemptID != input.AttemptID ||
			record.BaseSHA != input.BaseSHA || record.AuthorityNamespaceID != input.AuthorityNamespaceID {
			t.Fatalf("candidate identity diverges from the verification input: %+v", record)
		}
	}
	if worker.ContentDigest != canonical.DigestBytes(workerPatchData) {
		t.Fatalf("worker contentDigest = %s, worker.patch digest = %s", worker.ContentDigest, canonical.DigestBytes(workerPatchData))
	}
	if head.ContentDigest != canonical.DigestBytes(observedPatchData) {
		t.Fatalf("head contentDigest = %s, observed.patch digest = %s", head.ContentDigest, canonical.DigestBytes(observedPatchData))
	}

	observedArtifact := findArtifact(t, result.Manifest, "evidence:observed-patch")
	if observedArtifact.Digest != canonical.DigestBytes(observedPatchData) || observedArtifact.CandidateDigest != head.CandidateDigest {
		t.Fatalf("observed-patch artifact must bind head content once: %+v", observedArtifact)
	}
	if !slices.Contains(observedArtifact.RelatedGates, "format:normalize") {
		t.Fatalf("normalized observed-patch must stay related to format:normalize: %+v", observedArtifact.RelatedGates)
	}
	workerArtifact := findArtifact(t, result.Manifest, "evidence:worker-patch")
	if workerArtifact.Digest != worker.ContentDigest || workerArtifact.CandidateDigest != worker.CandidateDigest ||
		workerArtifact.RelativePath != "worker.patch" || workerArtifact.ByteSize != int64(len(workerPatchData)) {
		t.Fatalf("worker-patch artifact must bind the worker candidate content: %+v", workerArtifact)
	}
	wantWorkerGates := []string{"diff:observe", "scope:changed-paths", "format:normalize"}
	if !slices.Equal(workerArtifact.RelatedGates, wantWorkerGates) {
		t.Fatalf("worker-patch relatedGates = %v, want %v", workerArtifact.RelatedGates, wantWorkerGates)
	}

	observeGate, formatGate := Gate{}, Gate{}
	for _, gate := range result.Report.Gates {
		switch gate.ID {
		case "diff:observe":
			observeGate = gate
		case "format:normalize":
			formatGate = gate
		}
	}
	if !slices.Equal(observeGate.Evidence, []string{"artifact://evidence:observed-patch", "artifact://evidence:worker-patch"}) {
		t.Fatalf("diff:observe evidence = %+v", observeGate.Evidence)
	}
	if !slices.Equal(formatGate.Evidence, []string{"normalized:pkg/code.go", "candidate:" + head.CandidateDigest}) {
		t.Fatalf("format:normalize evidence = %+v", formatGate.Evidence)
	}
}

func TestVerifyCandidateChainStableAcrossRepeatedVerify(t *testing.T) {
	fixture := newVerificationFixture(t)
	writeFixtureGoFile(t, fixture.worktree.Path, "pkg/code.go", unformattedGoSource)
	input := fixture.candidateInput()
	input.Scope = ScopePolicy{AllowPaths: []string{"pkg/**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	first, err := New().Verify(context.Background(), input)
	if err != nil || first.Report.Status != "pass" {
		t.Fatalf("first verify = %+v err = %v", first.Report, err)
	}
	headDigest := first.Report.CandidateDigest
	store := newLocalCandidateStore(input.RunDirectory)
	if records, err := store.records(); err != nil || len(records) != 2 {
		t.Fatalf("first chain = %+v err = %v", records, err)
	}

	// Second Verify over the already-normalized worktree: normalization is a
	// no-op and content-addressed admission coalesces onto the existing
	// records — no third Candidate, head unchanged (§7.2).
	second, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatalf("repeated verify must stay idempotent: %v", err)
	}
	if second.Report.Status != "pass" {
		t.Fatalf("repeated verify report = %+v", second.Report)
	}
	records, err := store.records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("repeated verify inflated the chain: %+v", records)
	}
	if entries, statErr := os.ReadDir(store.quarantineDir()); statErr == nil && len(entries) > 0 {
		t.Fatalf("repeated verify must not quarantine anything: %v", entries)
	}
	if second.Report.CandidateDigest != headDigest {
		t.Fatalf("head changed across repeated verify: %s -> %s", headDigest, second.Report.CandidateDigest)
	}
	if second.Report.WorkerCandidateDigest != headDigest {
		t.Fatalf("worker binding must coalesce onto the existing content fact: %s", second.Report.WorkerCandidateDigest)
	}
	head, err := store.ByDigest(headDigest)
	if err != nil {
		t.Fatal(err)
	}
	workerPatchData, err := os.ReadFile(filepath.Join(input.RunDirectory, "worker.patch"))
	if err != nil {
		t.Fatal(err)
	}
	observedPatchData, err := os.ReadFile(filepath.Join(input.RunDirectory, "observed.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if string(workerPatchData) != string(observedPatchData) || canonical.DigestBytes(observedPatchData) != head.ContentDigest {
		t.Fatalf("repeated verify must observe the admitted head content: worker.patch=%d bytes observed.patch=%d bytes head contentDigest=%s", len(workerPatchData), len(observedPatchData), head.ContentDigest)
	}
}

func TestVerifyCandidateChainRootOnlyWhenNormalizationIsNoop(t *testing.T) {
	fixture := newVerificationFixture(t)
	writeFixtureGoFile(t, fixture.worktree.Path, "pkg/code.go", formattedGoSource)
	input := fixture.candidateInput()
	input.Scope = ScopePolicy{AllowPaths: []string{"pkg/**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "pass" || gateStatus(result.Report.Gates, "format:normalize") != "pass" {
		t.Fatalf("no-op normalization report = %+v", result.Report)
	}
	if result.Report.WorkerCandidateDigest == "" || result.Report.WorkerCandidateDigest != result.Report.CandidateDigest {
		t.Fatalf("without drift the head must be the worker candidate itself: %+v", result.Report)
	}
	store := newLocalCandidateStore(input.RunDirectory)
	records, err := store.records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("no-op normalization must admit exactly one candidate: %+v", records)
	}
	record, err := store.ByDigest(result.Report.CandidateDigest)
	if err != nil {
		t.Fatal(err)
	}
	if record.ProducerKind != domain.ProducerKindWorker || record.PredecessorCandidateDigest != "" {
		t.Fatalf("head must be the worker chain root: %+v", record)
	}
	workerPatchData, err := os.ReadFile(filepath.Join(input.RunDirectory, "worker.patch"))
	if err != nil {
		t.Fatal(err)
	}
	observedPatchData, err := os.ReadFile(filepath.Join(input.RunDirectory, "observed.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if string(workerPatchData) != string(observedPatchData) {
		t.Fatal("without drift observed.patch must equal worker.patch")
	}
	if record.ContentDigest != canonical.DigestBytes(observedPatchData) {
		t.Fatalf("head contentDigest = %s, patch digest = %s", record.ContentDigest, canonical.DigestBytes(observedPatchData))
	}
	formatGate := Gate{}
	for _, gate := range result.Report.Gates {
		if gate.ID == "format:normalize" {
			formatGate = gate
		}
	}
	if !slices.Equal(formatGate.Evidence, []string{"candidate:" + record.CandidateDigest}) {
		t.Fatalf("no-op format gate evidence = %+v", formatGate.Evidence)
	}
}

func TestVerifyCandidateModeRequiresInjectedIdentity(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Input)
	}{
		{name: "attempt without authority namespace", mutate: func(input *Input) { input.AuthorityNamespaceID = "" }},
		{name: "authority namespace without attempt", mutate: func(input *Input) { input.AttemptID = "" }},
		{name: "malformed attempt identity", mutate: func(input *Input) { input.AttemptID = "not an id" }},
		{name: "malformed authority namespace", mutate: func(input *Input) { input.AuthorityNamespaceID = "not an id" }},
		{name: "short base sha", mutate: func(input *Input) { input.BaseSHA = "abc" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			input := fixture.candidateInput()
			input.Scope = ScopePolicy{AllowPaths: []string{"**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
			tc.mutate(&input)
			if _, err := New().Verify(context.Background(), input); err == nil {
				t.Fatalf("candidate mode accepted %s", tc.name)
			}
			if _, statErr := os.Stat(filepath.Join(input.RunDirectory, "candidates")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rejected input must not create candidate records: %v", statErr)
			}
		})
	}
}

func TestVerifyLegacyRunKeepsByteCompatibleEvidence(t *testing.T) {
	fixture := newVerificationFixture(t)
	writeFixtureGoFile(t, fixture.worktree.Path, "pkg/code.go", unformattedGoSource)
	input := fixture.input()
	input.Scope = ScopePolicy{AllowPaths: []string{"pkg/**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
	result, err := New().Verify(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Status != "pass" {
		t.Fatalf("legacy report = %+v", result.Report)
	}
	if result.Report.WorkerCandidateDigest != "" || result.Report.CandidateDigest != "" {
		t.Fatalf("legacy report must stay candidate-free: %+v", result.Report)
	}
	if _, statErr := os.Stat(filepath.Join(input.RunDirectory, "candidates")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("legacy run must not create a candidate store: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(input.RunDirectory, "worker.patch")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("legacy run must not write worker.patch: %v", statErr)
	}
	reportData, err := os.ReadFile(filepath.Join(input.RunDirectory, "verification-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile(filepath.Join(input.RunDirectory, "artifact-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(reportData), "candidateDigest") || strings.Contains(string(manifestData), "candidateDigest") {
		t.Fatal("legacy documents must not carry candidate bindings")
	}
	observedArtifact := findArtifact(t, result.Manifest, "evidence:observed-patch")
	for _, artifact := range result.Manifest.Artifacts {
		if artifact.ID == "evidence:worker-patch" {
			t.Fatalf("legacy manifest must stay worker-patch-free: %+v", artifact)
		}
	}
	persisted, err := os.ReadFile(filepath.Join(input.RunDirectory, "observed.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if string(persisted) != string(result.Report.Observed.Patch) {
		t.Fatal("persisted observed patch diverges from the report")
	}
	// The read-path invariant legacy review packets rely on: the artifact
	// digest recomputes from the persisted bytes (§7.5 zero regression).
	if observedArtifact.Digest != canonical.DigestBytes(persisted) || observedArtifact.ByteSize != int64(len(persisted)) {
		t.Fatalf("observed-patch artifact no longer recomputes: %+v", observedArtifact)
	}
	if !slices.Equal(observedArtifact.RelatedGates, []string{"diff:observe", "scope:changed-paths", "format:normalize"}) {
		t.Fatalf("legacy observed-patch relatedGates changed: %+v", observedArtifact.RelatedGates)
	}
	for _, gate := range result.Report.Gates {
		switch gate.ID {
		case "diff:observe":
			if !slices.Equal(gate.Evidence, []string{"artifact://evidence:observed-patch"}) {
				t.Fatalf("legacy diff:observe evidence changed: %+v", gate.Evidence)
			}
		case "format:normalize":
			if !slices.Equal(gate.Evidence, []string{"normalized:pkg/code.go"}) {
				t.Fatalf("legacy format:normalize evidence changed: %+v", gate.Evidence)
			}
		}
	}
}

// Issue #142: the early repository Gate fail/error exits precede every
// artifact observation. Verify must still complete without error and persist
// both evidence documents through the contract Validator, with the manifest
// encoding artifacts as a JSON array (never null) that binds the real empty
// observed.patch bytes to the deterministic empty Observation, so task verify
// can run its typed state transition instead of stranding the Run in
// VERIFYING, and task review can still build a ReviewPacket over the failed
// run.
func TestVerifyEarlyRepositoryGateExitsPersistSchemaLegalEvidence(t *testing.T) {
	cases := []struct {
		name       string
		candidate  bool
		mutate     func(*testing.T, *Input)
		wantStatus string
	}{
		{
			name: "gate fail on foreign common directory",
			mutate: func(t *testing.T, input *Input) {
				input.ExpectedCommonDir = filepath.Join(t.TempDir(), "foreign-common-dir")
			},
			wantStatus: "fail",
		},
		{
			name: "gate error on non-repository worktree",
			mutate: func(t *testing.T, input *Input) {
				input.Worktree = t.TempDir()
			},
			wantStatus: "error",
		},
		{
			name:      "candidate mode gate fail on foreign common directory",
			candidate: true,
			mutate: func(t *testing.T, input *Input) {
				input.ExpectedCommonDir = filepath.Join(t.TempDir(), "foreign-common-dir")
			},
			wantStatus: "fail",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newVerificationFixture(t)
			input := fixture.input()
			if tc.candidate {
				input = fixture.candidateInput()
			}
			input.Scope = ScopePolicy{AllowPaths: []string{"**"}, MaxChangedFiles: 5, MaxDiffBytes: 1 << 20}
			tc.mutate(t, &input)
			result, err := New().Verify(context.Background(), input)
			if err != nil {
				t.Fatalf("early repository gate exit must complete verification: %v", err)
			}
			if result.Report.Status != tc.wantStatus {
				t.Fatalf("report status = %q, want %q: %+v", result.Report.Status, tc.wantStatus, result.Report)
			}
			if gateStatus(result.Report.Gates, "repository:integrity") != tc.wantStatus {
				t.Fatalf("repository gate = %+v", result.Report.Gates)
			}
			if len(result.Report.Gates) != 1 {
				t.Fatalf("early exit must stop at the repository gate: %+v", result.Report.Gates)
			}
			wantObservation, observationErr := emptyObservation()
			if observationErr != nil {
				t.Fatal(observationErr)
			}
			if result.Report.Observed.SnapshotDigest != wantObservation.SnapshotDigest || result.Report.Observed.DiffDigest != wantObservation.DiffDigest ||
				result.Report.Observed.DiffDigest != canonical.DigestBytes(nil) || len(result.Report.Observed.ChangedFiles) != 0 || result.Report.Observed.ChangedFileCount != 0 {
				t.Fatalf("early exit must bind the deterministic empty observation: %+v", result.Report.Observed)
			}
			if len(result.Manifest.Artifacts) != 1 {
				t.Fatalf("early exit must persist exactly the observed-patch artifact: %+v", result.Manifest.Artifacts)
			}
			patchArtifact := result.Manifest.Artifacts[0]
			persisted, readErr := os.ReadFile(filepath.Join(input.RunDirectory, "observed.patch"))
			if readErr != nil {
				t.Fatalf("observed.patch must be persisted on early exits: %v", readErr)
			}
			if len(persisted) != 0 {
				t.Fatalf("early exit observed patch must bind the real empty bytes, got %d bytes", len(persisted))
			}
			if patchArtifact.ID != "evidence:observed-patch" || patchArtifact.Producer != "verifier" || patchArtifact.Status != "validated" ||
				patchArtifact.PathRoot != "run" || patchArtifact.RelativePath != "observed.patch" ||
				patchArtifact.ByteSize != 0 || patchArtifact.Digest != canonical.DigestBytes(nil) || patchArtifact.CandidateDigest != "" {
				t.Fatalf("early exit observed-patch artifact = %+v", patchArtifact)
			}
			if !slices.Equal(patchArtifact.RelatedGates, []string{"repository:integrity"}) {
				t.Fatalf("early exit observed-patch relatedGates = %+v", patchArtifact.RelatedGates)
			}
			assertPersistedEvidenceSchemaLegal(t, input.RunDirectory)
		})
	}
}

func assertPersistedEvidenceSchemaLegal(t *testing.T, runDirectory string) {
	t.Helper()
	reportData, err := os.ReadFile(filepath.Join(runDirectory, "verification-report.json"))
	if err != nil {
		t.Fatalf("verification report must be persisted on early exits: %v", err)
	}
	manifestData, err := os.ReadFile(filepath.Join(runDirectory, "artifact-manifest.json"))
	if err != nil {
		t.Fatalf("artifact manifest must be persisted on early exits: %v", err)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindVerificationReport, reportData); err != nil {
		t.Fatalf("persisted verification report violates contract: %v", err)
	}
	if err := validator.Validate(domain.KindArtifactManifest, manifestData); err != nil {
		t.Fatalf("persisted artifact manifest violates contract: %v", err)
	}
	var manifest ArtifactManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Artifacts == nil {
		t.Fatalf("persisted manifest encodes artifacts as null: %s", manifestData)
	}
}
