package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/verification"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

// TestTaskReviewRejectsForgedVerificationCompletedActor proves that review
// never consumes a verification.completed event whose producer actor is
// omitted or forged: frozenVerificationDigests fails closed before any packet
// or decision is accepted, leaving the run in REVIEW_PENDING.
func TestTaskReviewRejectsForgedVerificationCompletedActor(t *testing.T) {
	sealedMigrationSkip(t)
	for name, actor := range map[string]*domain.Actor{
		"omitted": nil,
		"forged":  {Type: "system", ID: "marshal-worker-runner"},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newReviewVerdictFixtureWithVerifier(t, false, actor)
			var stdout, stderr bytes.Buffer
			if exit := Run([]string{"task", "review", "--run", fixture.runID, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "system/marshal-verifier") {
				t.Fatalf("forged verification.completed actor accepted: exit=%d stderr=%s", exit, stderr.String())
			}
			state, err := fixture.store.Inspect(fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			if state.State != domain.StateReviewPending || state.Sequence != 5 {
				t.Fatalf("forged verification actor changed the run: %+v", state)
			}
		})
	}
}

func TestTaskReviewAllVerdictsEndToEnd(t *testing.T) {
	sealedMigrationSkip(t)
	tests := []struct {
		verdict      string
		noChange     bool
		wantState    domain.State
		wantOutcome  bool
		finding      bool
		blockerOwner string
	}{
		{"accept", false, domain.StateAccepted, true, false, ""},
		{"rework", false, domain.StateReworkRequested, false, true, ""},
		{"reject", false, domain.StateRejected, true, false, ""},
		{"blocked", false, domain.StateBlocked, true, false, "repository-owner"},
		{"no_change", true, domain.StateNoChange, true, false, ""},
	}
	for _, test := range tests {
		t.Run(test.verdict, func(t *testing.T) {
			fixture := newReviewVerdictFixture(t, test.noChange)
			var stdout, stderr bytes.Buffer
			if exit := Run([]string{"task", "review", "--run", fixture.runID, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
				t.Fatalf("packet exit = %d, stderr = %s", exit, stderr.String())
			}
			packetData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "review-packet.json"))
			if err != nil {
				t.Fatal(err)
			}
			var packet domain.ReviewPacket
			if err := json.Unmarshal(packetData, &packet); err != nil {
				t.Fatal(err)
			}
			packetDigest, err := canonical.DigestJSON(packetData)
			if err != nil {
				t.Fatal(err)
			}
			publication := "do-not-publish"
			if test.verdict == "accept" || test.verdict == "no_change" {
				publication = "not-applicable"
			}
			decision := domain.ReviewDecision{
				APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision,
				TaskID: fixture.taskID, RunID: fixture.runID, ReviewRound: packet.ReviewRound,
				Reviewer:   domain.Reviewer{Type: "lead-agent", ID: "codex-e2e"},
				SpecDigest: fixture.specDigest, ReviewPacketDigest: packetDigest,
				VerificationDigest: packet.VerificationDigest, ArtifactManifestDigest: packet.ArtifactManifestDigest,
				EvidenceDigest: packet.EvidenceDigest, Verdict: test.verdict, Summary: "CLI verdict E2E",
				BlockingFindings: []domain.Finding{}, NonBlockingFindings: []domain.Finding{},
				PublicationRecommendation: publication, MergeRecommendation: "do-not-merge",
				BlockerOwner: test.blockerOwner, DecidedAt: time.Now().UTC(),
			}
			if test.finding {
				decision.BlockingFindings = []domain.Finding{{ID: "F-E2E", Severity: "P1", Title: "需要返工", Description: "必须补充实现证据", RequiredOutcome: "下一轮验证通过"}}
			}
			decisionData, err := json.Marshal(decision)
			if err != nil {
				t.Fatal(err)
			}
			decisionPath := filepath.Join(fixture.repository, "decision-"+test.verdict+".json")
			if err := os.WriteFile(decisionPath, decisionData, 0o600); err != nil {
				t.Fatal(err)
			}
			stdout.Reset()
			stderr.Reset()
			if exit := Run([]string{"task", "review", "--run", fixture.runID, "--decision", decisionPath, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
				t.Fatalf("decision exit = %d, stderr = %s", exit, stderr.String())
			}
			state, err := fixture.store.Inspect(fixture.runID)
			if err != nil {
				t.Fatal(err)
			}
			if state.State != test.wantState || state.Sequence != 6 {
				t.Fatalf("state = %+v", state)
			}
			if test.wantOutcome && state.TerminalReason == "" {
				t.Fatalf("terminal reason was not preserved: %+v", state)
			}
			if _, err := os.Stat(filepath.Join(fixture.runDirectory, "decisions", "decision-001.json")); err != nil {
				t.Fatalf("decision record missing: %v", err)
			}
			_, outcomeErr := os.Stat(filepath.Join(fixture.runDirectory, "outcome.json"))
			if test.wantOutcome && outcomeErr != nil {
				t.Fatalf("outcome missing: %v", outcomeErr)
			}
			if !test.wantOutcome && !os.IsNotExist(outcomeErr) {
				t.Fatalf("non-terminal verdict unexpectedly produced outcome: %v", outcomeErr)
			}
		})
	}
}

type reviewVerdictFixture struct {
	repository   string
	runDirectory string
	taskID       string
	runID        string
	specDigest   string
	store        *runstore.Store
}

func verifierActor() *domain.Actor { return &domain.Actor{Type: "system", ID: "marshal-verifier"} }

func newReviewVerdictFixture(t *testing.T, noChange bool) reviewVerdictFixture {
	t.Helper()
	return newReviewVerdictFixtureWithVerifier(t, noChange, verifierActor())
}

// newReviewVerdictFixtureWithVerifier builds the same fixture but records the
// verification.completed transition with the supplied producer actor, so
// forged or omitted verifier actors can be exercised end to end.
func newReviewVerdictFixtureWithVerifier(t *testing.T, noChange bool, verifier *domain.Actor) reviewVerdictFixture {
	t.Helper()
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	runGitCLI(t, repository, "init", "-q")
	runGitCLI(t, repository, "config", "user.name", "Marshal Test")
	runGitCLI(t, repository, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCLI(t, repository, "add", "README.md")
	runGitCLI(t, repository, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(runGitCLI(t, repository, "rev-parse", "HEAD"))
	location, err := marshalRepository.Discover(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := location.Init(); err != nil {
		t.Fatal(err)
	}
	taskID := "TASK-NOCHANGE"
	if !noChange {
		taskID = "TASK-VERDICT"
	}
	runID := "run:" + strings.ToLower(taskID)
	manager, err := gitworktree.Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(location.StateRoot, taskID, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repository, "worktree", "remove", "--force", worktree.Path).Run()
		_ = exec.Command("git", "-C", repository, "branch", "-D", worktree.Branch).Run()
	})
	if !noChange {
		if err := os.MkdirAll(filepath.Join(worktree.Path, "src"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree.Path, "src", "code.go"), []byte("package src\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deliverable := `{"id":"source","kind":"code","required":true,"pathGlob":"src/*.go","minimumCount":1}`
	if noChange {
		deliverable = `{"id":"diagnosis","kind":"diagnostic","required":true}`
	}
	taskData := []byte(fmt.Sprintf(`{"apiVersion":"marshal.dev/v1alpha1","kind":"Task","metadata":{"id":%q,"title":"verdict e2e"},"repository":{"path":%q,"baseRef":%q,"remote":"origin"},"work":{"objective":"验证所有审查结论。"},"scope":{"allowPaths":["src/**"],"denyPaths":[],"allowSubmodules":false,"maxChangedFiles":5,"maxDiffBytes":100000},"acceptance":{"commands":[],"allowNoChange":%t},"deliverables":[%s],"worker":{"preferredAdapter":"fake","fallbackAdapters":[],"executionProfile":"workspace-write","sessionPolicy":"ephemeral"},"budgets":{"runTimeoutSeconds":60,"attemptTimeoutSeconds":30,"maxAttempts":2,"maxOperationalRetries":0,"maxReworkRounds":1,"maxOutputBytes":100000},"publication":{"required":false,"provider":"none","mode":"none","remote":"origin","baseBranch":"main","mergePolicy":"never","requiredChecks":[]}}`, taskID, repository, base, noChange, deliverable))
	specDigest, err := canonical.DigestJSON(taskData)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := verification.ObserveContext(context.Background(), worktree.Path, base, 100000)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	report := verification.Report{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindVerificationReport, TaskID: taskID, RunID: runID, SpecDigest: specDigest, BaseSHA: base, Observed: observation, Status: "pass", Gates: []verification.Gate{{ID: "scope", Category: "scope", Required: true, Status: "pass", Summary: "ok", Evidence: []string{"artifact://evidence:observed-patch"}}}, StartedAt: now, CompletedAt: now}
	manifest := verification.ArtifactManifest{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindArtifactManifest, TaskID: taskID, RunID: runID, GeneratedAt: now, Artifacts: []verification.Artifact{{ID: "evidence:observed-patch", Kind: "patch", Producer: "verifier", Required: true, Status: "validated", PathRoot: "run", RelativePath: "observed.patch", ByteSize: int64(len(observation.Patch)), Digest: canonical.DigestBytes(observation.Patch), CreatedAt: now, Redacted: false, Truncated: observation.DiffTruncated, RelatedGates: []string{"scope"}}}}
	if noChange {
		manifest.Artifacts = append(manifest.Artifacts, verification.Artifact{ID: "diagnosis", Kind: "diagnostic", Producer: "verifier", Required: true, Status: "validated", PathRoot: "run", RelativePath: "diagnosis.txt", ByteSize: 2, Digest: canonical.DigestBytes([]byte("ok")), CreatedAt: now, RelatedGates: []string{"scope"}})
	}
	reportData, _ := json.Marshal(report)
	manifestData, _ := json.Marshal(manifest)
	runDirectory := filepath.Join(location.StateRoot, "runs", runID)
	for path, data := range map[string][]byte{"task-spec.json": taskData, "verification-report.json": reportData, "artifact-manifest.json": manifestData, "observed.patch": observation.Patch} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(runDirectory, path)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDirectory, path), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workerData, err := marshalSchemas.FS.ReadFile("examples/happy-path/worker-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var worker map[string]any
	_ = json.Unmarshal(workerData, &worker)
	worker["taskId"], worker["runId"], worker["attemptId"] = taskID, runID, "attempt:1"
	workerData, _ = json.Marshal(worker)
	workerPath := filepath.Join(runDirectory, "attempts", "attempt:1", "worker-result.json")
	if err := os.MkdirAll(filepath.Dir(workerPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workerPath, workerData, 0o600); err != nil {
		t.Fatal(err)
	}
	store := runstore.New(location.StateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	transitions := [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateVerifying}, {domain.StateVerifying, domain.StateReviewPending}}
	for index, transition := range transitions {
		payload := map[string]any{}
		if transition[1] == domain.StateReviewPending {
			reportDigest, _ := canonical.DigestJSON(reportData)
			manifestDigest, _ := canonical.DigestJSON(manifestData)
			payload = map[string]any{"reportDigest": reportDigest, "artifactManifestDigest": manifestDigest, "status": "pass"}
		}
		event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: fmt.Sprintf("event:%s:%d", strings.ToLower(taskID), index+1), RunID: runID, Sequence: uint64(index + 1), Type: "fixture.transition", StateFrom: transition[0], StateTo: transition[1], Timestamp: now, Payload: payload}
		if transition[1] == domain.StateRunning {
			event.AttemptID = "attempt:1"
		}
		if transition[1] == domain.StateReviewPending {
			event.Type = "verification.completed"
			event.Actor = verifier
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
	}
	state := domain.NewRunState(taskID, runID, now)
	state.State, state.Sequence, state.SpecDigest, state.BaseSHA, state.WorktreePath = domain.StateReviewPending, 5, specDigest, base, worktree.Path
	state.AttemptsUsed, state.ReviewRound, state.CurrentAttemptID = 1, 1, "attempt:1"
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repository); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	return reviewVerdictFixture{repository: repository, runDirectory: runDirectory, taskID: taskID, runID: runID, specDigest: specDigest, store: store}
}
