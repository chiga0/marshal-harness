package publication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/port"
	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/verification"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

type fakePublisherMode string

const (
	fakePublishOK        fakePublisherMode = "ok"
	fakePublishTransient fakePublisherMode = "transient"
	fakePublishPermanent fakePublisherMode = "permanent"
)

type fakePublisher struct {
	mu       sync.Mutex
	mode     fakePublisherMode
	calls    int
	received [][]byte
}

var _ port.Publisher = (*fakePublisher)(nil)

func newFakePublisher(mode fakePublisherMode) *fakePublisher {
	return &fakePublisher{mode: mode}
}

func (p *fakePublisher) Publish(_ context.Context, record domain.Record) (domain.Record, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.received = append(p.received, append([]byte(nil), record.Data...))
	switch p.mode {
	case fakePublishTransient:
		return domain.Record{}, errors.New("simulated transient publisher outage")
	case fakePublishPermanent:
		return domain.Record{}, port.Permanent(errors.New("simulated permanent publisher rejection"))
	}
	var intent domain.PublicationIntent
	if err := json.Unmarshal(record.Data, &intent); err != nil {
		return domain.Record{}, err
	}
	data, err := json.Marshal(publicationRecordFor(intent))
	if err != nil {
		return domain.Record{}, err
	}
	return domain.Record{Kind: domain.KindPublicationRecord, Data: data}, nil
}

func publicationRecordFor(intent domain.PublicationIntent) domain.PublicationRecord {
	timestamp := time.Date(2026, 8, 4, 12, 15, 0, 0, time.UTC)
	return domain.PublicationRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPublicationRecord,
		TaskID: intent.TaskID, RunID: intent.RunID, Provider: intent.Provider,
		Repository: domain.PublicationRepository{ID: "R_marshaltest0001", NameWithOwner: intent.Repository, URL: "https://github.com/" + intent.Repository},
		Remote:     intent.Remote, BaseBranch: intent.BaseBranch, HeadBranch: intent.HeadBranch, ReviewRound: intent.ReviewRound,
		BaseSHA: intent.BaseSHA, PreviousHeadSHA: intent.PreviousHeadSHA, HeadSHA: intent.CommitSHA, CommitSHA: intent.CommitSHA,
		SnapshotDigest: intent.SnapshotDigest, DiffDigest: intent.DiffDigest,
		SpecDigest: intent.SpecDigest, PolicyDigest: intent.PolicyDigest,
		EvidenceDigest: intent.EvidenceDigest, VerificationDigest: intent.VerificationDigest,
		ReviewDecisionDigest: intent.ReviewDecisionDigest,
		Marker:               intent.Marker, Mode: intent.Mode, MergePolicy: intent.MergePolicy,
		Request: domain.PullRequestRecord{ID: "PR_marshaltest0001", Number: 7, URL: "https://github.com/" + intent.Repository + "/pull/7", Draft: true, State: "OPEN"},
		Actor:   "marshal-fake-publisher", PublishedAt: timestamp, UpdatedAt: timestamp,
	}
}

type fakeObserver struct {
	mu             sync.Mutex
	status         string
	failWith       error
	mutate         func(*domain.RemoteCheckRecord)
	calls          int
	requiredChecks [][]string
}

var _ port.RemoteCheckObserver = (*fakeObserver)(nil)

func (o *fakeObserver) ObserveChecks(_ context.Context, record domain.Record, required []string) (domain.Record, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	o.requiredChecks = append(o.requiredChecks, append([]string(nil), required...))
	if o.failWith != nil {
		return domain.Record{}, o.failWith
	}
	var published domain.PublicationRecord
	if err := json.Unmarshal(record.Data, &published); err != nil {
		return domain.Record{}, err
	}
	entryStatus := "pending"
	switch o.status {
	case "pass":
		entryStatus = "pass"
	case "fail", "external-failure":
		entryStatus = "fail"
	}
	checks := domain.RemoteCheckRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRemoteCheckRecord,
		TaskID: published.TaskID, RunID: published.RunID, Provider: "github",
		RepositoryID: published.Repository.ID, RequestID: published.Request.ID,
		HeadSHA: published.HeadSHA, Status: o.status,
		ObservedAt: time.Date(2026, 8, 4, 12, 30, 0, 0, time.UTC),
	}
	for _, name := range required {
		checks.Checks = append(checks.Checks, domain.RemoteCheck{Name: name, Required: true, Status: entryStatus})
	}
	if o.mutate != nil {
		o.mutate(&checks)
	}
	data, err := json.Marshal(checks)
	if err != nil {
		return domain.Record{}, err
	}
	return domain.Record{Kind: domain.KindRemoteCheckRecord, Data: data}, nil
}

type fixtureOptions struct {
	maxReworkRounds  int
	reworkRoundsUsed uint
	noRequiredChecks bool
	noExpectedRemote bool
	policyMerge      bool
}

type publicationFixture struct {
	t              *testing.T
	repository     string
	stateRoot      string
	runDirectory   string
	taskID         string
	runID          string
	baseSHA        string
	worktreePath   string
	specDigest     string
	policyDigest   string
	evidenceDigest string
	observation    verification.Observation
	decidedAt      time.Time
	validator      *contract.Validator
	store          *runstore.Store
}

func fixtureGitEnvironment() []string {
	environment := []string{"LC_ALL=C", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
	for _, key := range []string{"HOME", "PATH", "TMPDIR"} {
		if value := os.Getenv(key); value != "" {
			environment = append(environment, key+"="+value)
		}
	}
	return environment
}

func runFixtureGitBytes(t *testing.T, directory string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = fixtureGitEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return output
}

func runFixtureGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(string(runFixtureGitBytes(t, directory, args...)))
}

func newPublicationFixture(t *testing.T, opts fixtureOptions) *publicationFixture {
	t.Helper()
	t.Setenv("MARSHAL_STATE_DIR", "")
	repository, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string { return runFixtureGit(t, repository, args...) }
	git("init", "-q")
	git("symbolic-ref", "HEAD", "refs/heads/main")
	git("config", "user.name", "Marshal Test")
	git("config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-q", "-m", "base")
	baseSHA := git("rev-parse", "HEAD")
	git("remote", "add", "origin", "https://github.com/marshal-test/task-repo.git")
	hookSentinel := filepath.Join(repository, ".git", "hooks-fired")
	for _, hook := range []string{"pre-commit", "post-commit", "commit-msg"} {
		script := "#!/bin/sh\ntouch '" + hookSentinel + "'\n"
		if err := os.WriteFile(filepath.Join(repository, ".git", "hooks", hook), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	location, err := marshalRepository.Discover(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := location.Init(); err != nil {
		t.Fatal(err)
	}
	taskID := "TASK-PUB"
	runID := "run:task-pub"
	manager, err := gitworktree.Open(location.RepositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(location.StateRoot, taskID, baseSHA)
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
	worktreePath := worktree.Path
	writeFile := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(filepath.Join(worktreePath, "README.md"), "base\nfeature\n", 0o600)
	writeFile(filepath.Join(worktreePath, ".gitattributes"), "*.go filter=marshal-evil\n", 0o600)
	writeFile(filepath.Join(worktreePath, "src", "code.go"), "package src\n\nfunc Value() int {\n\treturn 1\n}\n", 0o600)
	if err := os.Symlink("code.go", filepath.Join(worktreePath, "src", "current.go")); err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(worktreePath, "scripts", "build.sh"), "#!/bin/sh\necho build\n", 0o700)
	observation, err := verification.ObserveContext(context.Background(), worktreePath, baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	decidedAt := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	requiredChecks := []string{"ci/test"}
	if opts.noRequiredChecks {
		requiredChecks = []string{}
	}
	expectedRemoteURL := "https://github.com/marshal-test/task-repo.git"
	if opts.noExpectedRemote {
		expectedRemoteURL = ""
	}
	mergePolicy := domain.MergePolicyNever
	mergeMethod := ""
	if opts.policyMerge {
		mergePolicy = domain.MergePolicyPolicy
		mergeMethod = domain.MergeMethodSquash
	}
	task := domain.TaskSpec{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindTask,
		Metadata:   domain.TaskMetadata{ID: taskID, Title: "Publish controlled commit"},
		Repository: domain.TaskRepository{Path: repository, BaseRef: baseSHA, Remote: "origin", ExpectedRemoteURL: expectedRemoteURL},
		Work:       domain.TaskWork{Objective: "Publish a controlled commit for review.", Constraints: []string{}, NonGoals: []string{}},
		Scope:      domain.TaskScope{AllowPaths: []string{"**"}, DenyPaths: []string{}, AllowSubmodules: false, MaxChangedFiles: 10, MaxDiffBytes: 100000},
		Acceptance: domain.TaskAcceptance{Commands: []domain.TaskCommand{}, AllowNoChange: false},
		Deliverables: []domain.TaskDeliverable{
			{ID: "source", Kind: "code", Required: true, PathGlob: "src/*.go", MediaType: "text/x-go", MinimumCount: 1},
			{ID: "pull-request", Kind: "publication", Required: true, PathGlob: "docs/*.md", MediaType: "text/markdown", MinimumCount: 1},
		},
		Worker:  domain.TaskWorker{PreferredAdapter: "fake", FallbackAdapters: []string{}, ExecutionProfile: "workspace-write", SessionPolicy: "ephemeral"},
		Budgets: domain.TaskBudgets{RunTimeoutSeconds: 120, AttemptTimeoutSeconds: 60, MaxAttempts: 3, MaxOperationalRetries: 1, MaxReworkRounds: opts.maxReworkRounds, MaxOutputBytes: 100000},
		Publication: domain.TaskPublication{
			Required: true, Provider: "github", Mode: "draft", Remote: "origin",
			BaseBranch: "main", MergePolicy: mergePolicy, MergeMethod: mergeMethod, RequiredChecks: requiredChecks,
		},
	}
	taskData, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	specDigest, err := canonical.DigestJSON(taskData)
	if err != nil {
		t.Fatal(err)
	}
	policy := fixturePolicy{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPolicySnapshot,
		TaskID: taskID, RunID: runID,
		Sources:      []fixturePolicySource{{Scope: "builtin", Digest: "sha256:" + strings.Repeat("b", 64), Required: true}},
		Effective:    fixturePolicyEffective{MinimumExecutionProfile: "workspace-write", RequireEnforcedNetworkPolicy: false, NetworkPolicy: "unenforced", AllowFallbackWorkers: true, AllowWorkerSubagents: false, AllowPublication: true, AllowMerge: opts.policyMerge, AllowGateWaivers: false, AllowedAdapters: []string{"fake"}, EnvironmentAllowlist: []string{"PATH", "LANG", "TMPDIR"}, RetentionDays: 30},
		PolicyDigest: "sha256:" + strings.Repeat("c", 64),
		GeneratedAt:  now,
	}
	policyData, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	policyDigest, err := canonical.DigestJSON(policyData)
	if err != nil {
		t.Fatal(err)
	}
	report := verification.Report{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindVerificationReport,
		TaskID: taskID, RunID: runID, SpecDigest: specDigest, BaseSHA: baseSHA,
		Observed: observation, Status: "pass",
		Gates:     []verification.Gate{{ID: "scope", Category: "scope", Required: true, Status: "pass", Summary: "ok", Evidence: []string{"artifact://evidence:observed-patch"}}},
		StartedAt: now, CompletedAt: now,
	}
	reportData, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	reportDigest, err := canonical.DigestJSON(reportData)
	if err != nil {
		t.Fatal(err)
	}
	manifest := verification.ArtifactManifest{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindArtifactManifest,
		TaskID: taskID, RunID: runID, GeneratedAt: now,
		Artifacts: []verification.Artifact{{
			ID: "evidence:observed-patch", Kind: "patch", Producer: "verifier", Required: true,
			Status: "validated", PathRoot: "run", RelativePath: "observed.patch",
			ByteSize: int64(len(observation.Patch)), Digest: canonical.DigestBytes(observation.Patch),
			CreatedAt: now, Redacted: false, Truncated: observation.DiffTruncated, RelatedGates: []string{"scope"},
		}},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest, err := canonical.DigestJSON(manifestData)
	if err != nil {
		t.Fatal(err)
	}
	workerData, err := marshalSchemas.FS.ReadFile("examples/happy-path/worker-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var workerResult map[string]any
	if err := json.Unmarshal(workerData, &workerResult); err != nil {
		t.Fatal(err)
	}
	workerResult["taskId"], workerResult["runId"], workerResult["attemptId"] = taskID, runID, "attempt:1"
	workerData, err = json.Marshal(workerResult)
	if err != nil {
		t.Fatal(err)
	}
	workerDigest, err := canonical.DigestJSON(workerData)
	if err != nil {
		t.Fatal(err)
	}
	identity := fixtureEvidenceIdentity{
		SpecDigest: specDigest, PatchDigest: canonical.DigestBytes(observation.Patch),
		VerificationDigest: reportDigest, ArtifactManifestDigest: manifestDigest,
		WorkerResultDigests: []string{workerDigest}, PreviousFindings: []domain.PreviousFinding{},
	}
	identityData, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	evidenceDigest, err := canonical.DigestJSON(identityData)
	if err != nil {
		t.Fatal(err)
	}
	packet := domain.ReviewPacket{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewPacket,
		TaskID: taskID, RunID: runID, ReviewRound: 1,
		SpecDigest: specDigest, BaseSHA: baseSHA,
		SnapshotDigest: observation.SnapshotDigest, DiffDigest: observation.DiffDigest,
		VerificationDigest: reportDigest, ArtifactManifestDigest: manifestDigest,
		WorkerResultDigests: []string{workerDigest}, EvidenceDigest: evidenceDigest,
		Inputs: domain.PacketInputs{
			TaskSpec: "task-spec.json", Patch: "observed.patch",
			VerificationReport: "verification-report.json", ArtifactManifest: "artifact-manifest.json",
			WorkerResults: []string{"attempts/attempt:1/worker-result.json"},
		},
		PreviousBlockingFindings: []domain.PreviousFinding{}, GeneratedAt: now,
	}
	packetData, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	packetDigest, err := canonical.DigestJSON(packetData)
	if err != nil {
		t.Fatal(err)
	}
	mergeRecommendation := "do-not-merge"
	if opts.policyMerge {
		mergeRecommendation = mergeRecommendationEligible
	}
	decision := domain.ReviewDecision{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision,
		TaskID: taskID, RunID: runID, ReviewRound: 1,
		Reviewer:   domain.Reviewer{Type: "lead-agent", ID: "publication-integration"},
		SpecDigest: specDigest, ReviewPacketDigest: packetDigest,
		VerificationDigest: reportDigest, ArtifactManifestDigest: manifestDigest,
		EvidenceDigest: evidenceDigest, Verdict: "accept", Summary: "accept and publish",
		BlockingFindings: []domain.Finding{}, NonBlockingFindings: []domain.Finding{},
		PublicationRecommendation: "publish", MergeRecommendation: mergeRecommendation,
		DecidedAt: decidedAt,
	}
	decisionData, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	decisionDigest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		t.Fatal(err)
	}
	for kind, data := range map[domain.Kind][]byte{
		domain.KindTask: taskData, domain.KindPolicySnapshot: policyData,
		domain.KindVerificationReport: reportData, domain.KindArtifactManifest: manifestData,
		domain.KindReviewPacket: packetData, domain.KindReviewDecision: decisionData,
	} {
		if err := validator.Validate(kind, data); err != nil {
			t.Fatalf("fixture %s failed schema validation: %v", kind, err)
		}
	}
	runDirectory := filepath.Join(location.StateRoot, "runs", runID)
	writeRun := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(runDirectory, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRun("task-spec.json", taskData)
	writeRun("policy-snapshot.json", policyData)
	writeRun("verification-report.json", reportData)
	writeRun("artifact-manifest.json", manifestData)
	writeRun("observed.patch", observation.Patch)
	writeRun("review-packet.json", packetData)
	writeRun(filepath.Join("decisions", fmt.Sprintf("decision-%03d.json", decision.ReviewRound)), decisionData)
	writeRun(filepath.Join("attempts", "attempt:1", "worker-result.json"), workerData)
	store := runstore.New(location.StateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	transitions := []struct {
		from, to  domain.State
		eventType string
		attemptID string
	}{
		{domain.StateCreated, domain.StatePlanned, "task.planned", ""},
		{domain.StatePlanned, domain.StateReady, "task.ready", ""},
		{domain.StateReady, domain.StateRunning, "worker.started", "attempt:1"},
		{domain.StateRunning, domain.StateVerifying, "worker.completed", ""},
		{domain.StateVerifying, domain.StateReviewPending, "verification.completed", ""},
		{domain.StateReviewPending, domain.StatePublishing, "review.accept", ""},
	}
	for index, transition := range transitions {
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
			EventID: fmt.Sprintf("event:%s:%d", strings.ToLower(runID), index+1), RunID: runID,
			Sequence: uint64(index + 1), Type: transition.eventType,
			StateFrom: transition.from, StateTo: transition.to, Timestamp: now,
			Payload: map[string]any{"fixture": true},
		}
		if transition.attemptID != "" {
			event.AttemptID = transition.attemptID
		}
		if transition.eventType == "verification.completed" {
			event.Payload = map[string]any{"reportDigest": reportDigest, "artifactManifestDigest": manifestDigest, "status": "pass"}
		}
		if transition.eventType == "review.accept" {
			event.Payload = map[string]any{"verdict": "accept", "decisionDigest": decisionDigest, "evidenceDigest": evidenceDigest}
		}
		event.Actor = fixtureAuthorityActor(transition.eventType)
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
	}
	state := domain.NewRunState(taskID, runID, now)
	state.State = domain.StatePublishing
	state.Sequence = uint64(len(transitions))
	state.SpecDigest = specDigest
	state.PolicyDigest = policyDigest
	state.BaseSHA = baseSHA
	state.WorktreePath = worktreePath
	state.AttemptsUsed, state.ReviewRound, state.CurrentAttemptID = 1, 1, "attempt:1"
	state.ReworkRoundsUsed = opts.reworkRoundsUsed
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Inspect(runID); err != nil {
		t.Fatalf("fixture run state inconsistent: %v", err)
	}
	return &publicationFixture{
		t: t, repository: location.RepositoryRoot, stateRoot: location.StateRoot,
		runDirectory: runDirectory, taskID: taskID, runID: runID, baseSHA: baseSHA,
		worktreePath: worktreePath, specDigest: specDigest, policyDigest: policyDigest,
		evidenceDigest: evidenceDigest, observation: observation, decidedAt: decidedAt,
		validator: validator, store: store,
	}
}

type fixturePolicySource struct {
	Scope    string `json:"scope"`
	Digest   string `json:"digest"`
	Required bool   `json:"required"`
}

type fixturePolicyEffective struct {
	MinimumExecutionProfile      string   `json:"minimumExecutionProfile"`
	RequireEnforcedNetworkPolicy bool     `json:"requireEnforcedNetworkPolicy"`
	NetworkPolicy                string   `json:"networkPolicy"`
	AllowFallbackWorkers         bool     `json:"allowFallbackWorkers"`
	AllowWorkerSubagents         bool     `json:"allowWorkerSubagents"`
	AllowPublication             bool     `json:"allowPublication"`
	AllowMerge                   bool     `json:"allowMerge"`
	AllowGateWaivers             bool     `json:"allowGateWaivers"`
	AllowedAdapters              []string `json:"allowedAdapters"`
	EnvironmentAllowlist         []string `json:"environmentAllowlist"`
	RetentionDays                int      `json:"retentionDays"`
}

type fixturePolicy struct {
	APIVersion   domain.APIVersion      `json:"apiVersion"`
	Kind         domain.Kind            `json:"kind"`
	TaskID       string                 `json:"taskId"`
	RunID        string                 `json:"runId"`
	Sources      []fixturePolicySource  `json:"sources"`
	Effective    fixturePolicyEffective `json:"effective"`
	PolicyDigest string                 `json:"policyDigest"`
	GeneratedAt  time.Time              `json:"generatedAt"`
}

type fixtureEvidenceIdentity struct {
	SpecDigest             string                   `json:"specDigest"`
	PatchDigest            string                   `json:"patchDigest"`
	VerificationDigest     string                   `json:"verificationDigest"`
	ArtifactManifestDigest string                   `json:"artifactManifestDigest"`
	WorkerResultDigests    []string                 `json:"workerResultDigests"`
	PreviousFindings       []domain.PreviousFinding `json:"previousBlockingFindings"`
}

func (f *publicationFixture) publish(t *testing.T, publisher port.Publisher) (Result, error) {
	t.Helper()
	return Publish(context.Background(), Input{
		StateRoot: f.stateRoot, RepositoryRoot: f.repository, RunID: f.runID,
		Publisher: publisher, Validator: f.validator,
	})
}

func (f *publicationFixture) observe(t *testing.T, observer port.RemoteCheckObserver) (CheckResult, error) {
	t.Helper()
	return f.observeAt(t, observer, time.Time{})
}

func (f *publicationFixture) observeAt(t *testing.T, observer port.RemoteCheckObserver, now time.Time) (CheckResult, error) {
	t.Helper()
	return ObserveChecks(context.Background(), CheckInput{
		StateRoot: f.stateRoot, RunID: f.runID, Observer: observer, Validator: f.validator, Now: now,
	})
}

func (f *publicationFixture) inspect(t *testing.T) domain.RunState {
	t.Helper()
	state, err := f.store.Inspect(f.runID)
	if err != nil {
		t.Fatalf("inspect run state: %v", err)
	}
	return state
}

func (f *publicationFixture) readIntent(t *testing.T) domain.PublicationIntent {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.runDirectory, "publication-intent.json"))
	if err != nil {
		t.Fatalf("read publication intent: %v", err)
	}
	if err := f.validator.Validate(domain.KindPublicationIntent, data); err != nil {
		t.Fatalf("publication intent failed schema validation: %v", err)
	}
	var intent domain.PublicationIntent
	if err := json.Unmarshal(data, &intent); err != nil {
		t.Fatal(err)
	}
	return intent
}

func (f *publicationFixture) assertNoHookFired(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.repository, ".git", "hooks-fired")); !os.IsNotExist(err) {
		t.Fatalf("commit hooks were executed: %v", err)
	}
}

func (f *publicationFixture) assertBlockedOutcome(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.runDirectory, "outcome.json"))
	if err != nil {
		t.Fatalf("blocked outcome missing: %v", err)
	}
	if err := f.validator.Validate(domain.KindOutcome, data); err != nil {
		t.Fatalf("blocked outcome invalid: %v", err)
	}
	var outcome domain.OutcomeBundle
	if err := json.Unmarshal(data, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != domain.StateBlocked || outcome.Verdict != "blocked" || outcome.FinalEvidenceDigest != f.evidenceDigest {
		t.Fatalf("blocked outcome = %+v", outcome)
	}
}

func TestPublishCreatesControlledCommitAndAdvancesToCIPending(t *testing.T) {
	fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
	ambientConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(ambientConfig, []byte("[user]\n\tname = Ambient Author\n\temail = ambient@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", ambientConfig)
	t.Setenv("GIT_AUTHOR_NAME", "Ambient Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "ambient-author@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Ambient Committer")
	t.Setenv("GIT_COMMITTER_EMAIL", "ambient-committer@example.invalid")
	filterSentinel := filepath.Join(fixture.repository, ".git", "filter-fired")
	filterScript := filepath.Join(fixture.repository, ".git", "marshal-evil-filter")
	if err := os.WriteFile(filterScript, []byte("#!/bin/sh\ntouch '"+filterSentinel+"'\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.repository, "config", "--local", "filter.marshal-evil.clean", filterScript)
	publisher := newFakePublisher(fakePublishOK)
	result, err := fixture.publish(t, publisher)
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	if result.State.State != domain.StateCIPending {
		t.Fatalf("state = %s, want CI_PENDING", result.State.State)
	}
	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1", publisher.calls)
	}
	fixture.assertNoHookFired(t)
	if _, err := os.Stat(filterSentinel); !os.IsNotExist(err) {
		t.Fatalf("clean filter was executed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDirectory, "publication-error.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected publication error diagnostic: %v", err)
	}
	intent := fixture.readIntent(t)
	if intent.TaskID != fixture.taskID || intent.RunID != fixture.runID || intent.Provider != "github" {
		t.Fatalf("intent identity = %+v", intent)
	}
	if intent.Repository != "marshal-test/task-repo" || intent.Remote != "origin" || intent.BaseBranch != "main" {
		t.Fatalf("intent repository binding = %+v", intent)
	}
	if intent.HeadBranch != deriveBranch(fixture.taskID, fixture.runID) {
		t.Fatalf("head branch = %s", intent.HeadBranch)
	}
	if intent.BaseSHA != fixture.baseSHA {
		t.Fatalf("intent baseSha = %s, want %s", intent.BaseSHA, fixture.baseSHA)
	}
	if intent.SnapshotDigest != fixture.observation.SnapshotDigest || intent.DiffDigest != fixture.observation.DiffDigest ||
		intent.SpecDigest != fixture.specDigest || intent.PolicyDigest != fixture.policyDigest ||
		intent.EvidenceDigest != fixture.evidenceDigest {
		t.Fatalf("intent evidence digests do not match accepted evidence: %+v", intent)
	}
	if intent.Marker != "<!-- marshal task="+fixture.taskID+" run="+fixture.runID+" -->" {
		t.Fatalf("intent marker = %q", intent.Marker)
	}
	if intent.Mode != "draft" || intent.MergePolicy != "never" {
		t.Fatalf("intent mode/mergePolicy = %s/%s", intent.Mode, intent.MergePolicy)
	}
	recordData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "publication-record.json"))
	if err != nil {
		t.Fatalf("publication record missing: %v", err)
	}
	if err := fixture.validator.Validate(domain.KindPublicationRecord, recordData); err != nil {
		t.Fatalf("publication record failed schema validation: %v", err)
	}
	assertControlledCommit(t, fixture, intent.CommitSHA)
	state := fixture.inspect(t)
	if state.State != domain.StateCIPending || state.Sequence != 8 {
		t.Fatalf("state = %+v", state)
	}
	if state.Publication == nil || state.Publication.Provider != "github" || state.Publication.HeadSHA != intent.CommitSHA {
		t.Fatalf("state publication = %+v", state.Publication)
	}
	if result.Publication.HeadSHA != intent.CommitSHA || !result.Publication.Request.Draft || result.Publication.Request.State != "OPEN" {
		t.Fatalf("published record = %+v", result.Publication)
	}
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 8 || events[6].Type != "publication.completed" || events[7].Type != "publication.checks-requested" {
		t.Fatalf("journal tail = %+v", events[6:])
	}
}

func TestPublishWithoutRequiredChecksAcceptsAndWritesOutcome(t *testing.T) {
	fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1, noRequiredChecks: true})
	result, err := fixture.publish(t, newFakePublisher(fakePublishOK))
	if err != nil {
		t.Fatal(err)
	}
	if result.State.State != domain.StateAccepted || result.State.TerminalReason == "" {
		t.Fatalf("state = %+v", result.State)
	}
	data, err := os.ReadFile(filepath.Join(fixture.runDirectory, "outcome.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.validator.Validate(domain.KindOutcome, data); err != nil {
		t.Fatal(err)
	}
}

func TestPublishPolicyMergeProducesMergeEligiblePublication(t *testing.T) {
	fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1, policyMerge: true})
	result, err := fixture.publish(t, newFakePublisher(fakePublishOK))
	if err != nil {
		t.Fatalf("policy merge publication failed: %v", err)
	}
	if result.State.State != domain.StateCIPending {
		t.Fatalf("state = %s, want CI_PENDING", result.State.State)
	}
	if result.Publication.MergePolicy != domain.MergePolicyPolicy {
		t.Fatalf("publication mergePolicy = %q, want policy", result.Publication.MergePolicy)
	}
	intent := fixture.readIntent(t)
	if intent.MergePolicy != domain.MergePolicyPolicy {
		t.Fatalf("intent mergePolicy = %q, want policy", intent.MergePolicy)
	}
	decisionData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "decisions", "decision-001.json"))
	if err != nil {
		t.Fatal(err)
	}
	decisionDigest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		t.Fatal(err)
	}
	var decision domain.ReviewDecision
	if err := json.Unmarshal(decisionData, &decision); err != nil {
		t.Fatal(err)
	}
	state := fixture.inspect(t)
	capabilityDigest := fabricatedDigest("8")
	lease, err := fixture.store.Acquire(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	state.CapabilityDigest = capabilityDigest
	if err := fixture.store.WriteSnapshot(lease, state); err != nil {
		_ = lease.Release()
		t.Fatal(err)
	}
	approval := domain.ApprovalRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindApprovalRecord,
		RecordID: "approval-policy-merge", TaskID: fixture.taskID, RunID: fixture.runID, ControlSequence: 1,
		Gate: domain.ApprovalGatePublish, Source: domain.ControlSource{Type: domain.ControlSourceTypeHuman, ID: "maintainer"},
		Binding: domain.ApprovalBinding{StateSequence: state.Sequence, SpecDigest: fixture.specDigest, PolicyDigest: fixture.policyDigest,
			CapabilityDigest: capabilityDigest, BaseSHA: fixture.baseSHA, ReviewRound: state.ReviewRound,
			DecisionDigest: decisionDigest, EvidenceDigest: fixture.evidenceDigest},
		Outcome: domain.ApprovalOutcomeApproved, CreatedAt: time.Now().UTC(),
	}
	if err := fixture.store.AppendApproval(lease, fixture.validator, approval); err != nil {
		_ = lease.Release()
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	authorityNamespaceID, err := reconcileAuthorityNamespaceID(fixture.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	mergeFixture := &mergeFixture{
		t: fixture.t, stateRoot: fixture.stateRoot, runDirectory: fixture.runDirectory,
		taskID: fixture.taskID, runID: fixture.runID, baseSHA: fixture.baseSHA,
		headSHA: result.Publication.HeadSHA, specDigest: fixture.specDigest,
		policyDigest: fixture.policyDigest, decisionDigest: decisionDigest,
		evidenceDigest: fixture.evidenceDigest, verificationDigest: decision.VerificationDigest,
		capabilityDigest: capabilityDigest, authorityNamespaceID: authorityNamespaceID,
		validator: fixture.validator, store: fixture.store,
	}
	mergeHarness := newMergeHarness(t, mergeFixture)
	merged, err := mergeHarness.merge(t)
	if err != nil {
		t.Fatalf("published policy run did not enter controlled merge: %v", err)
	}
	if merged.State.State != domain.StateAccepted || merged.Intent.PublicationDigest == "" || merged.Receipt.ReceiptDigest == "" {
		t.Fatalf("controlled merge result = %+v", merged)
	}
}

func assertControlledCommit(t *testing.T, fixture *publicationFixture, commitSHA string) {
	t.Helper()
	raw := runFixtureGit(t, fixture.repository, "cat-file", "-p", commitSHA)
	lines := strings.Split(raw, "\n")
	var parents []string
	var tree, authorLine, committerLine string
	messageStart := 0
	for index, line := range lines {
		if line == "" {
			messageStart = index + 1
			break
		}
		if value, ok := strings.CutPrefix(line, "parent "); ok {
			parents = append(parents, value)
		} else if value, ok := strings.CutPrefix(line, "tree "); ok {
			tree = value
		} else if value, ok := strings.CutPrefix(line, "author "); ok {
			authorLine = value
		} else if value, ok := strings.CutPrefix(line, "committer "); ok {
			committerLine = value
		}
	}
	if len(parents) != 1 || parents[0] != fixture.baseSHA {
		t.Fatalf("commit parents = %v, want exactly [%s]", parents, fixture.baseSHA)
	}
	if tree == "" {
		t.Fatal("commit tree header missing")
	}
	assertFixedIdentity(t, "author", authorLine, fixture.decidedAt)
	assertFixedIdentity(t, "committer", committerLine, fixture.decidedAt)
	message := strings.Join(lines[messageStart:], "\n")
	if lines[messageStart] != "Publish controlled commit" {
		t.Fatalf("commit subject = %q", lines[messageStart])
	}
	for _, trailer := range []string{
		"Marshal-Task: " + fixture.taskID,
		"Marshal-Run: " + fixture.runID,
		"Marshal-Spec-Digest: " + fixture.specDigest,
		"Marshal-Evidence-Digest: " + fixture.evidenceDigest,
		"Marshal-Snapshot-Digest: " + fixture.observation.SnapshotDigest,
	} {
		if !strings.Contains(message, trailer) {
			t.Fatalf("commit message missing trailer %q", trailer)
		}
	}
	assertTreeMatchesWorktree(t, fixture, tree)
}

func assertFixedIdentity(t *testing.T, role, line string, decidedAt time.Time) {
	t.Helper()
	fields := strings.Fields(line)
	if len(fields) != 5 || fields[0] != "Marshal" || fields[1] != "Publisher" || fields[2] != "<marshal@localhost.invalid>" {
		t.Fatalf("%s identity = %q", role, line)
	}
	epoch, err := strconv.ParseInt(fields[3], 10, 64)
	if err != nil || epoch != decidedAt.Unix() || fields[4] != "+0000" {
		t.Fatalf("%s timestamp = %q, want %d +0000", role, line, decidedAt.Unix())
	}
}

type worktreeEntry struct {
	mode    string
	content []byte
}

func assertTreeMatchesWorktree(t *testing.T, fixture *publicationFixture, treeSHA string) {
	t.Helper()
	raw := runFixtureGit(t, fixture.repository, "ls-tree", "-r", treeSHA)
	type commitEntry struct {
		mode, sha string
	}
	committed := map[string]commitEntry{}
	var commitPaths []string
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			t.Fatalf("malformed ls-tree line %q", line)
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || fields[1] != "blob" {
			t.Fatalf("unexpected ls-tree entry %q", line)
		}
		if path == ".marshal" || strings.HasPrefix(path, ".marshal/") {
			t.Fatalf("forbidden .marshal path published: %s", path)
		}
		committed[path] = commitEntry{mode: fields[0], sha: fields[2]}
		commitPaths = append(commitPaths, path)
	}
	expected := snapshotWorktree(t, fixture.worktreePath)
	var expectedPaths []string
	for path := range expected {
		expectedPaths = append(expectedPaths, path)
	}
	sort.Strings(commitPaths)
	sort.Strings(expectedPaths)
	if strings.Join(commitPaths, "\x00") != strings.Join(expectedPaths, "\x00") {
		t.Fatalf("commit tree paths %v do not match accepted worktree %v", commitPaths, expectedPaths)
	}
	for _, path := range commitPaths {
		entry := committed[path]
		want := expected[path]
		if entry.mode != want.mode {
			t.Fatalf("mode for %s = %s, want %s", path, entry.mode, want.mode)
		}
		blob := runFixtureGitBytes(t, fixture.repository, "cat-file", "blob", entry.sha)
		if !bytes.Equal(blob, want.content) {
			t.Fatalf("blob content for %s differs from raw worktree content", path)
		}
	}
}

func snapshotWorktree(t *testing.T, root string) map[string]worktreeEntry {
	t.Helper()
	entries := map[string]worktreeEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" || d.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entries[relative] = worktreeEntry{mode: "120000", content: []byte(target)}
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			mode := "100644"
			if info.Mode().Perm()&0o100 != 0 {
				mode = "100755"
			}
			entries[relative] = worktreeEntry{mode: mode, content: data}
		default:
			return fmt.Errorf("unsupported worktree entry %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk worktree: %v", err)
	}
	return entries
}

func TestPublishBlocksWhenWorktreeDriftsAfterReview(t *testing.T) {
	fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "src", "drift.go"), []byte("package src\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	publisher := newFakePublisher(fakePublishOK)
	_, err := fixture.publish(t, publisher)
	if err == nil {
		t.Fatal("expected publish to fail after worktree drift")
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Fatalf("error = %v, want stale snapshot diagnosis", err)
	}
	if publisher.calls != 0 {
		t.Fatalf("publisher calls = %d, want 0", publisher.calls)
	}
	fixture.assertBlockedOutcome(t)
	state := fixture.inspect(t)
	if state.State != domain.StateBlocked || state.TerminalReason == "" {
		t.Fatalf("state = %+v", state)
	}
	for _, name := range []string{"publication-intent.json", "publication-record.json"} {
		if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, name)); !os.IsNotExist(statErr) {
			t.Fatalf("%s unexpectedly exists after blocked publish", name)
		}
	}
	fixture.assertNoHookFired(t)
}

func TestPublishRequiresFrozenRemoteURLAndRejectsLocalRewrite(t *testing.T) {
	t.Run("missing expected remote", func(t *testing.T) {
		fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1, noExpectedRemote: true})
		publisher := newFakePublisher(fakePublishOK)
		_, err := fixture.publish(t, publisher)
		if err == nil || !strings.Contains(err.Error(), "expectedRemoteUrl") || publisher.calls != 0 {
			t.Fatalf("error=%v publisher calls=%d", err, publisher.calls)
		}
	})
	t.Run("worker controlled insteadOf rewrite", func(t *testing.T) {
		fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
		runFixtureGit(t, fixture.repository, "config", "--local", "url.https://github.com/attacker/.insteadOf", "https://github.com/marshal-test/")
		publisher := newFakePublisher(fakePublishOK)
		_, err := fixture.publish(t, publisher)
		if err == nil || !strings.Contains(err.Error(), "remote URL differs") || publisher.calls != 0 {
			t.Fatalf("error=%v publisher calls=%d", err, publisher.calls)
		}
	})
}

func TestPublishRetriesTransientFailureWithSameIntentAndCommit(t *testing.T) {
	fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
	publisher := newFakePublisher(fakePublishTransient)
	_, err := fixture.publish(t, publisher)
	if err == nil {
		t.Fatal("expected transient publisher failure")
	}
	if port.IsPermanent(err) {
		t.Fatalf("transient failure misclassified as permanent: %v", err)
	}
	first := fixture.inspect(t)
	if first.State != domain.StatePublishing {
		t.Fatalf("state after transient failure = %s, want PUBLISHING", first.State)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDirectory, "publication-error.json")); err != nil {
		t.Fatalf("publication error diagnostic missing: %v", err)
	}
	intentBeforeRetry := fixture.readIntent(t)
	runFixtureGit(t, fixture.repository, "cat-file", "-e", intentBeforeRetry.CommitSHA)
	fixture.assertNoHookFired(t)
	publisher.mode = fakePublishOK
	result, err := fixture.publish(t, publisher)
	if err != nil {
		t.Fatalf("retry after transient failure failed: %v", err)
	}
	if publisher.calls != 2 {
		t.Fatalf("publisher calls = %d, want 2", publisher.calls)
	}
	if !bytes.Equal(publisher.received[0], publisher.received[1]) {
		t.Fatal("retry sent a different PublicationIntent record")
	}
	intentAfterRetry := fixture.readIntent(t)
	if intentAfterRetry.CommitSHA != intentBeforeRetry.CommitSHA {
		t.Fatalf("retry created a new commit %s, want reuse of %s", intentAfterRetry.CommitSHA, intentBeforeRetry.CommitSHA)
	}
	if result.Publication.HeadSHA != intentBeforeRetry.CommitSHA {
		t.Fatalf("published head = %s, want %s", result.Publication.HeadSHA, intentBeforeRetry.CommitSHA)
	}
	state := fixture.inspect(t)
	if state.State != domain.StateCIPending || state.Publication == nil || state.Publication.HeadSHA != intentBeforeRetry.CommitSHA {
		t.Fatalf("state = %+v", state)
	}
}

func TestPublishBlocksOnPermanentPublisherFailure(t *testing.T) {
	fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
	publisher := newFakePublisher(fakePublishPermanent)
	_, err := fixture.publish(t, publisher)
	if err == nil {
		t.Fatal("expected permanent publisher failure")
	}
	if !port.IsPermanent(err) {
		t.Fatalf("error %v should remain classified permanent", err)
	}
	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1", publisher.calls)
	}
	state := fixture.inspect(t)
	if state.State != domain.StateBlocked || state.TerminalReason == "" {
		t.Fatalf("state = %+v", state)
	}
	fixture.readIntent(t)
	if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "publication-record.json")); !os.IsNotExist(statErr) {
		t.Fatal("publication record unexpectedly exists after permanent failure")
	}
}

func TestPublishReconcilesMatchingRecordWrittenBeforeJournal(t *testing.T) {
	fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
	publisher := newFakePublisher(fakePublishTransient)
	if _, err := fixture.publish(t, publisher); err == nil {
		t.Fatal("expected initial transient failure")
	}
	intent := fixture.readIntent(t)
	recordData, _ := json.Marshal(publicationRecordFor(intent))
	if err := fixture.validator.Validate(domain.KindPublicationRecord, recordData); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.runDirectory, "publication-record.json"), recordData, 0o600); err != nil {
		t.Fatal(err)
	}
	publisher.mode = fakePublishOK
	result, err := fixture.publish(t, publisher)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.State != domain.StateCIPending || result.Publication.Request.ID != publicationRecordFor(intent).Request.ID {
		t.Fatalf("result = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDirectory, "publication-error.json")); !os.IsNotExist(err) {
		t.Fatalf("stale publication error remains: %v", err)
	}
}

func TestPublishBlocksWhenPersistedIntentCommitIsTampered(t *testing.T) {
	fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
	publisher := newFakePublisher(fakePublishTransient)
	if _, err := fixture.publish(t, publisher); err == nil {
		t.Fatal("expected transient failure to persist intent")
	}
	intent := fixture.readIntent(t)
	intent.CommitSHA = strings.Repeat("9", 40)
	intentData, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.runDirectory, "publication-intent.json"), append(intentData, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	publisher.mode = fakePublishOK
	_, err = fixture.publish(t, publisher)
	if err == nil {
		t.Fatal("expected tampered intent to block publication")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error = %v, want intent mismatch diagnosis", err)
	}
	if publisher.calls != 1 {
		t.Fatalf("publisher calls = %d, want 1 (tampered retry must not reach the publisher)", publisher.calls)
	}
	state := fixture.inspect(t)
	if state.State != domain.StateBlocked || state.TerminalReason == "" {
		t.Fatalf("state = %+v", state)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "publication-record.json")); !os.IsNotExist(statErr) {
		t.Fatal("publication record unexpectedly exists for tampered intent")
	}
}

func TestPublishBlocksWhenDecisionDiffersFromFrozenJournal(t *testing.T) {
	fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
	path := filepath.Join(fixture.runDirectory, "decisions", fmt.Sprintf("decision-%03d.json", fixture.inspect(t).ReviewRound))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decision domain.ReviewDecision
	if err := json.Unmarshal(data, &decision); err != nil {
		t.Fatal(err)
	}
	decision.Summary = "replacement decision with valid schema"
	data, err = json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.validator.Validate(domain.KindReviewDecision, data); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	publisher := newFakePublisher(fakePublishOK)
	result, err := fixture.publish(t, publisher)
	if err == nil || !strings.Contains(err.Error(), "frozen lifecycle event") {
		t.Fatalf("expected frozen decision mismatch, got result=%+v err=%v", result, err)
	}
	if publisher.calls != 0 {
		t.Fatalf("publisher calls = %d, want 0", publisher.calls)
	}
	if fixture.inspect(t).State != domain.StateBlocked {
		t.Fatal("run did not block after decision replacement")
	}
}

func TestPublishReadsRoundBoundDecisionWithoutLegacyFile(t *testing.T) {
	fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
	state := fixture.inspect(t)
	if _, err := os.Stat(filepath.Join(fixture.runDirectory, "review-decision.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy review-decision.json should not exist, stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDirectory, "decisions", fmt.Sprintf("decision-%03d.json", state.ReviewRound))); err != nil {
		t.Fatalf("round-bound decision missing: %v", err)
	}
	result, err := fixture.publish(t, newFakePublisher(fakePublishOK))
	if err != nil {
		t.Fatalf("evidence re-validation failed without legacy review-decision.json: %v", err)
	}
	if result.State.State != domain.StateCIPending {
		t.Fatalf("state = %s, want CI_PENDING", result.State.State)
	}
}

func publishToCIPending(t *testing.T, opts fixtureOptions) *publicationFixture {
	t.Helper()
	fixture := newPublicationFixture(t, opts)
	publisher := newFakePublisher(fakePublishOK)
	result, err := fixture.publish(t, publisher)
	if err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	if result.State.State != domain.StateCIPending {
		t.Fatalf("state = %s, want CI_PENDING", result.State.State)
	}
	return fixture
}

func advanceReworkToPublishing(t *testing.T, fixture *publicationFixture) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "src", "code.go"), []byte("package src\n\nfunc Value() int {\n\treturn 2\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := verification.ObserveContext(context.Background(), fixture.worktreePath, fixture.baseSHA, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	report := verification.Report{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindVerificationReport, TaskID: fixture.taskID, RunID: fixture.runID, SpecDigest: fixture.specDigest, BaseSHA: fixture.baseSHA, Observed: observation, Status: "pass", Gates: []verification.Gate{{ID: "scope", Category: "scope", Required: true, Status: "pass", Summary: "ok", Evidence: []string{"artifact://evidence:observed-patch"}}}, StartedAt: now, CompletedAt: now}
	reportData, _ := json.Marshal(report)
	reportDigest, _ := canonical.DigestJSON(reportData)
	manifest := verification.ArtifactManifest{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindArtifactManifest, TaskID: fixture.taskID, RunID: fixture.runID, GeneratedAt: now, Artifacts: []verification.Artifact{{ID: "evidence:observed-patch", Kind: "patch", Producer: "verifier", Required: true, Status: "validated", PathRoot: "run", RelativePath: "observed.patch", ByteSize: int64(len(observation.Patch)), Digest: canonical.DigestBytes(observation.Patch), CreatedAt: now, RelatedGates: []string{"scope"}}}}
	manifestData, _ := json.Marshal(manifest)
	manifestDigest, _ := canonical.DigestJSON(manifestData)
	workerData, err := marshalSchemas.FS.ReadFile("examples/happy-path/worker-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var worker map[string]any
	_ = json.Unmarshal(workerData, &worker)
	worker["taskId"], worker["runId"], worker["attemptId"] = fixture.taskID, fixture.runID, "attempt:2"
	workerData, _ = json.Marshal(worker)
	workerDigest, _ := canonical.DigestJSON(workerData)
	oldWorkerData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "attempts", "attempt:1", "worker-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	oldWorkerDigest, _ := canonical.DigestJSON(oldWorkerData)
	identity := fixtureEvidenceIdentity{SpecDigest: fixture.specDigest, PatchDigest: canonical.DigestBytes(observation.Patch), VerificationDigest: reportDigest, ArtifactManifestDigest: manifestDigest, WorkerResultDigests: []string{oldWorkerDigest, workerDigest}, PreviousFindings: []domain.PreviousFinding{}}
	identityData, _ := json.Marshal(identity)
	evidenceDigest, _ := canonical.DigestJSON(identityData)
	packet := domain.ReviewPacket{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewPacket, TaskID: fixture.taskID, RunID: fixture.runID, ReviewRound: 2, SpecDigest: fixture.specDigest, BaseSHA: fixture.baseSHA, SnapshotDigest: observation.SnapshotDigest, DiffDigest: observation.DiffDigest, VerificationDigest: reportDigest, ArtifactManifestDigest: manifestDigest, WorkerResultDigests: []string{oldWorkerDigest, workerDigest}, EvidenceDigest: evidenceDigest, Inputs: domain.PacketInputs{TaskSpec: "task-spec.json", Patch: "observed.patch", VerificationReport: "verification-report.json", ArtifactManifest: "artifact-manifest.json", WorkerResults: []string{"attempts/attempt:1/worker-result.json", "attempts/attempt:2/worker-result.json"}}, PreviousBlockingFindings: []domain.PreviousFinding{}, GeneratedAt: now}
	packetData, _ := json.Marshal(packet)
	packetDigest, _ := canonical.DigestJSON(packetData)
	decision := domain.ReviewDecision{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision, TaskID: fixture.taskID, RunID: fixture.runID, ReviewRound: 2, Reviewer: domain.Reviewer{Type: "lead-agent", ID: "publication-rework"}, SpecDigest: fixture.specDigest, ReviewPacketDigest: packetDigest, VerificationDigest: reportDigest, ArtifactManifestDigest: manifestDigest, EvidenceDigest: evidenceDigest, Verdict: "accept", Summary: "accept rework and update draft", BlockingFindings: []domain.Finding{}, NonBlockingFindings: []domain.Finding{}, PublicationRecommendation: "publish", MergeRecommendation: "do-not-merge", DecidedAt: fixture.decidedAt.Add(time.Minute)}
	decisionData, _ := json.Marshal(decision)
	decisionDigest, _ := canonical.DigestJSON(decisionData)
	for kind, data := range map[domain.Kind][]byte{domain.KindVerificationReport: reportData, domain.KindArtifactManifest: manifestData, domain.KindReviewPacket: packetData, domain.KindReviewDecision: decisionData} {
		if err := fixture.validator.Validate(kind, data); err != nil {
			t.Fatalf("rework %s invalid: %v", kind, err)
		}
	}
	for name, data := range map[string][]byte{"verification-report.json": reportData, "artifact-manifest.json": manifestData, "review-packet.json": packetData, filepath.Join("decisions", fmt.Sprintf("decision-%03d.json", decision.ReviewRound)): decisionData, "observed.patch": observation.Patch, filepath.Join("attempts", "attempt:2", "worker-result.json"): workerData} {
		path := filepath.Join(fixture.runDirectory, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state := fixture.inspect(t)
	if state.State != domain.StateReworkRequested {
		t.Fatalf("state before rework = %s", state.State)
	}
	lease, err := fixture.store.Acquire(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	transitions := []struct {
		to        domain.State
		eventType string
		attemptID string
		payload   map[string]any
	}{{domain.StateRunning, "worker.started", "attempt:2", map[string]any{}}, {domain.StateVerifying, "worker.completed", "", map[string]any{}}, {domain.StateReviewPending, "verification.completed", "", map[string]any{"reportDigest": reportDigest, "artifactManifestDigest": manifestDigest, "status": "pass"}}, {domain.StatePublishing, "review.accept", "", map[string]any{"decisionDigest": decisionDigest, "evidenceDigest": evidenceDigest, "verdict": "accept"}}}
	current := state.State
	for _, transition := range transitions {
		sequence := state.Sequence + 1
		event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: fmt.Sprintf("event:rework:%d", sequence), RunID: fixture.runID, AttemptID: transition.attemptID, Sequence: sequence, Type: transition.eventType, StateFrom: current, StateTo: transition.to, Timestamp: now, Actor: fixtureAuthorityActor(transition.eventType), Payload: transition.payload}
		if err := fixture.store.Append(lease, event, state.Sequence); err != nil {
			t.Fatal(err)
		}
		state.Sequence, current = sequence, transition.to
	}
	state.State, state.AttemptsUsed, state.CurrentAttemptID, state.ReviewRound, state.UpdatedAt = domain.StatePublishing, state.AttemptsUsed+1, "attempt:2", state.ReviewRound+1, now
	if err := fixture.store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	fixture.observation, fixture.evidenceDigest, fixture.decidedAt = observation, evidenceDigest, decision.DecidedAt
}

func TestCIFailureReworkFastForwardsSamePublicationBranch(t *testing.T) {
	assertCIFailureReworkFastForward(t, false)
}

func TestCIFailureReworkRecoversPartiallyArchivedGeneration(t *testing.T) {
	assertCIFailureReworkFastForward(t, true)
}

func assertCIFailureReworkFastForward(t *testing.T, simulateArchiveCrash bool) {
	t.Helper()
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	firstIntent := fixture.readIntent(t)
	if _, err := fixture.observe(t, &fakeObserver{status: "fail"}); err != nil {
		t.Fatal(err)
	}
	advanceReworkToPublishing(t, fixture)
	archive := filepath.Join(fixture.runDirectory, "publications", fmt.Sprintf("review-%03d-%s", firstIntent.ReviewRound, firstIntent.CommitSHA[:12]))
	if simulateArchiveCrash {
		if err := os.MkdirAll(archive, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(fixture.runDirectory, "publication-record.json"), filepath.Join(archive, "publication-record.json")); err != nil {
			t.Fatal(err)
		}
	}
	result, err := fixture.publish(t, newFakePublisher(fakePublishOK))
	if err != nil {
		t.Fatal(err)
	}
	secondIntent := fixture.readIntent(t)
	if result.State.State != domain.StateCIPending || secondIntent.ReviewRound != 2 || secondIntent.PreviousHeadSHA != firstIntent.CommitSHA || secondIntent.HeadBranch != firstIntent.HeadBranch || secondIntent.CommitSHA == firstIntent.CommitSHA {
		t.Fatalf("second publication = %+v, state=%+v", secondIntent, result.State)
	}
	parent := runFixtureGit(t, fixture.repository, "rev-parse", secondIntent.CommitSHA+"^")
	if parent != firstIntent.CommitSHA {
		t.Fatalf("rework commit parent = %s, want %s", parent, firstIntent.CommitSHA)
	}
	for _, name := range []string{"publication-intent.json", "publication-record.json", "remote-check-record.json"} {
		if _, err := os.Stat(filepath.Join(archive, name)); err != nil {
			t.Fatalf("archived %s missing: %v", name, err)
		}
	}
}

func TestObserveChecksPendingKeepsRunInCIPending(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	before := fixture.inspect(t)
	observer := &fakeObserver{status: "pending"}
	result, err := fixture.observe(t, observer)
	if err != nil {
		t.Fatalf("observe failed: %v", err)
	}
	if result.State.State != domain.StateCIPending || result.Checks.Status != "pending" {
		t.Fatalf("result = %+v", result)
	}
	after := fixture.inspect(t)
	if after.State != domain.StateCIPending || after.Sequence != before.Sequence {
		t.Fatalf("pending observation advanced the run: %+v", after)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDirectory, "remote-check-record.json")); err != nil {
		t.Fatalf("remote check record missing: %v", err)
	}
	if observer.calls != 1 || len(observer.requiredChecks) != 1 || len(observer.requiredChecks[0]) != 1 || observer.requiredChecks[0][0] != "ci/test" {
		t.Fatalf("observer required checks = %+v", observer.requiredChecks)
	}
}

func TestObserveChecksPassAcceptsRun(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	observer := &fakeObserver{status: "pass"}
	result, err := fixture.observe(t, observer)
	if err != nil {
		t.Fatalf("observe failed: %v", err)
	}
	if result.State.State != domain.StateAccepted || result.Checks.Status != "pass" {
		t.Fatalf("result = %+v", result)
	}
	state := fixture.inspect(t)
	if state.State != domain.StateAccepted || state.TerminalReason == "" {
		t.Fatalf("state = %+v", state)
	}
	if state.Publication == nil || state.Publication.HeadSHA == "" {
		t.Fatalf("state publication lost on accept: %+v", state.Publication)
	}
	outcomeData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "outcome.json"))
	if err != nil {
		t.Fatalf("accepted run outcome missing: %v", err)
	}
	if err := fixture.validator.Validate(domain.KindOutcome, outcomeData); err != nil {
		t.Fatalf("accepted run outcome invalid: %v", err)
	}
	var outcome domain.OutcomeBundle
	if err := json.Unmarshal(outcomeData, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != domain.StateAccepted || outcome.Verdict != "accept" || outcome.FinalEvidenceDigest != fixture.evidenceDigest {
		t.Fatalf("outcome = %+v", outcome)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDirectory, "outcome.md")); err != nil {
		t.Fatalf("human-readable outcome missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDirectory, "review-decision.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy review-decision.json should not exist, stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.runDirectory, "decisions", fmt.Sprintf("decision-%03d.json", state.ReviewRound))); err != nil {
		t.Fatalf("round-bound decision missing: %v", err)
	}
}

func TestObserveChecksBlocksWhenDecisionChangedAfterPublication(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	path := filepath.Join(fixture.runDirectory, "decisions", fmt.Sprintf("decision-%03d.json", fixture.inspect(t).ReviewRound))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decision domain.ReviewDecision
	_ = json.Unmarshal(data, &decision)
	decision.Summary = "changed after publication"
	data, _ = json.Marshal(decision)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.observe(t, &fakeObserver{status: "pass"})
	if err == nil || result.State.State != domain.StateBlocked {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	fixture.assertBlockedOutcome(t)
}

// fixtureAuthorityActor maps a fixture event type to the exact producer
// actor admission and publication consumers require; generic fixture
// transitions carry no actor.
func fixtureAuthorityActor(eventType string) *domain.Actor {
	switch eventType {
	case "worker.started", "worker.completed", "worker.failed":
		return &domain.Actor{Type: "system", ID: "marshal-worker-runner"}
	case "verification.completed":
		return &domain.Actor{Type: "system", ID: "marshal-verifier"}
	case "review.accept":
		return &domain.Actor{Type: "system", ID: "marshal-review"}
	default:
		return nil
	}
}

// mutateJournalActor rewrites the actor bytes of the first journal line with
// the given event type, replacing old with new. The raw journal is the
// authoritative source, so the mutation is visible to every consumer.
func mutateJournalActor(t *testing.T, fixture *publicationFixture, eventType, old, new string) {
	t.Helper()
	path := filepath.Join(fixture.runDirectory, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for index, line := range lines {
		if !strings.Contains(line, `"type":"`+eventType+`"`) {
			continue
		}
		if !strings.Contains(line, old) {
			t.Fatalf("journal line for %s does not contain %q", eventType, old)
		}
		lines[index] = strings.Replace(line, old, new, 1)
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("journal has no event with type %q", eventType)
}

func TestPublishRejectsForgedVerificationCompletedActor(t *testing.T) {
	for name, mutation := range map[string]struct{ old, new string }{
		"omitted": {`,"actor":{"type":"system","id":"marshal-verifier"}`, ""},
		"forged":  {`"id":"marshal-verifier"`, `"id":"marshal-worker-runner"`},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
			mutateJournalActor(t, fixture, "verification.completed", mutation.old, mutation.new)
			_, err := fixture.publish(t, newFakePublisher(fakePublishOK))
			if err == nil || !strings.Contains(err.Error(), "system/marshal-verifier") {
				t.Fatalf("forged verification.completed actor accepted: %v", err)
			}
			if state := fixture.inspect(t); state.State != domain.StateBlocked {
				t.Fatalf("forged verification actor left the run in %s", state.State)
			}
		})
	}
}

func TestPublishRejectsForgedReviewAcceptActor(t *testing.T) {
	for name, mutation := range map[string]struct{ old, new string }{
		"omitted": {`,"actor":{"type":"system","id":"marshal-review"}`, ""},
		"forged":  {`"id":"marshal-review"`, `"id":"marshal-github-publisher"`},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
			mutateJournalActor(t, fixture, "review.accept", mutation.old, mutation.new)
			_, err := fixture.publish(t, newFakePublisher(fakePublishOK))
			if err == nil || !strings.Contains(err.Error(), "system/marshal-review") {
				t.Fatalf("forged review.accept actor accepted: %v", err)
			}
			if state := fixture.inspect(t); state.State != domain.StateBlocked {
				t.Fatalf("forged review actor left the run in %s", state.State)
			}
		})
	}
}

func TestObserveChecksRejectsForgedPublicationCompletedActor(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	before := fixture.inspect(t)
	mutateJournalActor(t, fixture, "publication.completed", `"id":"marshal-github-publisher"`, `"id":"marshal-forged-publisher"`)
	observer := &fakeObserver{status: "pass"}
	_, err := fixture.observe(t, observer)
	if err == nil || !strings.Contains(err.Error(), "publisher/marshal-github-publisher") {
		t.Fatalf("forged publication.completed actor accepted: %v", err)
	}
	if observer.calls != 0 {
		t.Fatalf("observer was invoked despite forged publication authority: %d", observer.calls)
	}
	after := fixture.inspect(t)
	if after.State != domain.StateCIPending || after.Sequence != before.Sequence {
		t.Fatalf("forged publication actor changed the run: %+v", after)
	}
}

func TestObserveChecksBlocksWhenPublicationRecordDiffersFromJournal(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	path := filepath.Join(fixture.runDirectory, "publication-record.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var published domain.PublicationRecord
	_ = json.Unmarshal(data, &published)
	published.Actor = "replacement-actor"
	data, _ = json.Marshal(published)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	observer := &fakeObserver{status: "pass"}
	result, err := fixture.observe(t, observer)
	if err == nil || result.State.State != domain.StateBlocked || observer.calls != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, observer.calls, err)
	}
	fixture.assertBlockedOutcome(t)
}

func TestObserveChecksFailRequestsReworkWhileBudgetAvailable(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	observer := &fakeObserver{status: "fail"}
	result, err := fixture.observe(t, observer)
	if err != nil {
		t.Fatalf("observe failed: %v", err)
	}
	if result.State.State != domain.StateReworkRequested || result.Checks.Status != "fail" {
		t.Fatalf("result = %+v", result)
	}
	state := fixture.inspect(t)
	if state.State != domain.StateReworkRequested || state.ReworkRoundsUsed != 1 {
		t.Fatalf("state = %+v", state)
	}
}

func TestObserveChecksFailBlocksWhenBudgetExhausted(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 0})
	observer := &fakeObserver{status: "fail"}
	result, err := fixture.observe(t, observer)
	if err == nil {
		t.Fatal("expected exhausted rework budget to block")
	}
	if !strings.Contains(err.Error(), "budget") {
		t.Fatalf("error = %v, want budget exhaustion diagnosis", err)
	}
	if result.Checks.Status != "fail" {
		t.Fatalf("checks = %+v", result.Checks)
	}
	state := fixture.inspect(t)
	if state.State != domain.StateBlocked || state.TerminalReason == "" {
		t.Fatalf("state = %+v", state)
	}
	fixture.assertBlockedOutcome(t)
}

func TestObserveChecksRejectsIdentityDrift(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	observer := &fakeObserver{status: "pass", mutate: func(checks *domain.RemoteCheckRecord) {
		checks.HeadSHA = strings.Repeat("3", 40)
	}}
	_, err := fixture.observe(t, observer)
	if err == nil {
		t.Fatal("expected identity drift to be rejected")
	}
	if !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("error = %v, want identity mismatch diagnosis", err)
	}
	state := fixture.inspect(t)
	if state.State != domain.StateBlocked || state.TerminalReason == "" {
		t.Fatalf("identity drift did not block the run: %+v", state)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "remote-check-record.json")); !os.IsNotExist(statErr) {
		t.Fatal("drifted RemoteCheckRecord must not be persisted")
	}
	fixture.assertBlockedOutcome(t)
}

func TestObserveChecksPermanentObserverFailureBlocksRun(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	observer := &fakeObserver{failWith: port.Permanent(errors.New("provider access revoked"))}
	_, err := fixture.observe(t, observer)
	if err == nil {
		t.Fatal("expected permanent observer failure")
	}
	if !port.IsPermanent(err) {
		t.Fatalf("error %v should remain classified permanent", err)
	}
	state := fixture.inspect(t)
	if state.State != domain.StateBlocked || state.TerminalReason == "" {
		t.Fatalf("state = %+v", state)
	}
}

func TestObserveChecksTransientObserverFailureKeepsCIPending(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	before := fixture.inspect(t)
	observer := &fakeObserver{failWith: errors.New("simulated network blip")}
	if _, err := fixture.observe(t, observer); err == nil {
		t.Fatal("expected transient observer failure")
	}
	state := fixture.inspect(t)
	if state.State != domain.StateCIPending || state.Sequence != before.Sequence {
		t.Fatalf("transient observer failure mutated the run: %+v", state)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "remote-check-record.json")); !os.IsNotExist(statErr) {
		t.Fatal("failed observation must not persist a check record")
	}
}

func TestObserveChecksExternalFailureBlocksRun(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	observer := &fakeObserver{status: "external-failure"}
	result, err := fixture.observe(t, observer)
	if err == nil {
		t.Fatal("expected external failure to block")
	}
	if result.Checks.Status != "external-failure" {
		t.Fatalf("checks = %+v", result.Checks)
	}
	state := fixture.inspect(t)
	if state.State != domain.StateBlocked || state.TerminalReason == "" {
		t.Fatalf("state = %+v", state)
	}
}

func fixtureRunDeadline(t *testing.T, fixture *publicationFixture) time.Time {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.runDirectory, "task-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	var task domain.TaskSpec
	if err := json.Unmarshal(data, &task); err != nil {
		t.Fatal(err)
	}
	return fixture.inspect(t).CreatedAt.Add(time.Duration(task.Budgets.RunTimeoutSeconds) * time.Second)
}

func assertCIDeadlineBlocked(t *testing.T, fixture *publicationFixture, result CheckResult, observeErr error, observer *fakeObserver) {
	t.Helper()
	if observeErr == nil {
		t.Fatal("expected frozen run deadline to block remote check observation")
	}
	if observeErr.Error() != "ci-deadline-exceeded" {
		t.Fatalf("error = %q, want fixed code ci-deadline-exceeded", observeErr.Error())
	}
	if result.State.State != domain.StateBlocked {
		t.Fatalf("result state = %s, want BLOCKED", result.State.State)
	}
	if observer.calls != 0 {
		t.Fatalf("observer calls after run deadline = %d, want 0", observer.calls)
	}
	state := fixture.inspect(t)
	if state.State != domain.StateBlocked || state.TerminalReason == "" {
		t.Fatalf("snapshot state = %+v", state)
	}
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Type != "publication.blocked" {
		t.Fatalf("journal tail event = %s, want publication.blocked", last.Type)
	}
	if code, _ := last.Payload["error"].(string); code != "ci-deadline-exceeded" {
		t.Fatalf("blocked event payload = %+v, want fixed code ci-deadline-exceeded", last.Payload)
	}
	fixture.assertBlockedOutcome(t)
}

func blockedEventCount(events []domain.RunEvent) int {
	count := 0
	for _, event := range events {
		if event.Type == "publication.blocked" {
			count++
		}
	}
	return count
}

func TestObserveChecksKeepsCIPendingBeforeRunDeadline(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	before := fixture.inspect(t)
	deadline := fixtureRunDeadline(t, fixture)
	observer := &fakeObserver{status: "pending"}
	result, err := fixture.observeAt(t, observer, deadline.Add(-time.Second))
	if err != nil {
		t.Fatalf("observe before run deadline failed: %v", err)
	}
	if result.State.State != domain.StateCIPending || result.Checks.Status != "pending" {
		t.Fatalf("result = %+v", result)
	}
	after := fixture.inspect(t)
	if after.State != domain.StateCIPending || after.Sequence != before.Sequence {
		t.Fatalf("observation before run deadline advanced the run: %+v", after)
	}
	if observer.calls != 1 {
		t.Fatalf("observer calls = %d, want 1 before run deadline", observer.calls)
	}
}

func TestObserveChecksBlocksAtOrAfterRunDeadline(t *testing.T) {
	t.Run("exactly at deadline", func(t *testing.T) {
		fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
		deadline := fixtureRunDeadline(t, fixture)
		observer := &fakeObserver{status: "pending"}
		result, err := fixture.observeAt(t, observer, deadline.In(time.FixedZone("CST", 8*3600)))
		assertCIDeadlineBlocked(t, fixture, result, err, observer)
	})
	t.Run("after deadline", func(t *testing.T) {
		fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
		deadline := fixtureRunDeadline(t, fixture)
		observer := &fakeObserver{status: "pending"}
		result, err := fixture.observeAt(t, observer, deadline.Add(30*time.Minute))
		assertCIDeadlineBlocked(t, fixture, result, err, observer)
	})
}

func TestObserveChecksRunDeadlineBlockIsNotAppendedTwice(t *testing.T) {
	fixture := publishToCIPending(t, fixtureOptions{maxReworkRounds: 1})
	deadline := fixtureRunDeadline(t, fixture)
	observer := &fakeObserver{status: "pending"}
	result, err := fixture.observeAt(t, observer, deadline)
	assertCIDeadlineBlocked(t, fixture, result, err, observer)
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	first := blockedEventCount(events)
	if first != 1 {
		t.Fatalf("blocked events after deadline block = %d, want 1", first)
	}
	if _, err := fixture.observeAt(t, observer, deadline.Add(time.Hour)); err == nil {
		t.Fatal("expected observation of an already blocked run to fail")
	}
	if _, err := fixture.observeAt(t, observer, deadline.Add(2*time.Hour)); err == nil {
		t.Fatal("expected repeated observation of an already blocked run to fail")
	}
	events, _, err = fixture.store.ReadEvents(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if got := blockedEventCount(events); got != first {
		t.Fatalf("repeated observations appended %d block events, want 0", got-first)
	}
	if state := fixture.inspect(t); state.State != domain.StateBlocked {
		t.Fatalf("state = %s, want BLOCKED", state.State)
	}
	if observer.calls != 0 {
		t.Fatalf("observer calls = %d, want 0", observer.calls)
	}
}
