package publication

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// Frozen-ciDeadline derivation (ADR 0028 plan B, Issue #30 expectation 4).

func TestFrozenCIDeadlineAnchorsAtPublication(t *testing.T) {
	createdAt := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	publishedAt := time.Date(2026, 8, 4, 10, 50, 0, 0, time.UTC)
	budgets := domain.TaskBudgets{RunTimeoutSeconds: 3600}
	deadline := frozenCIDeadline(createdAt, publishedAt, budgets)
	want := time.Date(2026, 8, 4, 11, 50, 0, 0, time.UTC)
	if !deadline.Equal(want) {
		t.Fatalf("ciDeadline = %s, want %s", deadline, want)
	}
	// The CI budget is independent of the pre-publication duration: the 50
	// minutes Worker/Review/Publish consumed must not erode the CI window
	// (the deadline is NOT createdAt + runTimeoutSeconds).
	if deadline.Equal(createdAt.Add(time.Hour)) {
		t.Fatal("ciDeadline is still anchored at run creation; the run budget would swallow CI time")
	}
}

func TestFrozenCIDeadlineKeepsCreationLowerBound(t *testing.T) {
	// Anomalous lineage (publishedAt earlier than createdAt, e.g. fabricated
	// or clock-skewed records): the deadline never moves earlier than the
	// legacy CreatedAt-anchored value, preserving the prior behavior exactly.
	createdAt := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	publishedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	budgets := domain.TaskBudgets{RunTimeoutSeconds: 120}
	deadline := frozenCIDeadline(createdAt, publishedAt, budgets)
	if want := createdAt.Add(120 * time.Second); !deadline.Equal(want) {
		t.Fatalf("ciDeadline = %s, want %s", deadline, want)
	}
}

func TestFrozenCIDeadlineIndependentOfZonePresentation(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	createdAt := time.Date(2026, 8, 4, 18, 0, 0, 0, cst) // 10:00Z
	publishedAt := time.Date(2026, 8, 4, 10, 50, 0, 0, time.UTC)
	budgets := domain.TaskBudgets{RunTimeoutSeconds: 3600}
	deadline := frozenCIDeadline(createdAt, publishedAt, budgets)
	if want := time.Date(2026, 8, 4, 11, 50, 0, 0, time.UTC); !deadline.Equal(want) {
		t.Fatalf("ciDeadline = %s, want %s", deadline, want)
	}
}

func TestCIClockSkewToleranceFrozenAt300Seconds(t *testing.T) {
	// ADR 0028 freezes the implementation Milestone value of Δskew as a
	// constant and asserts it by test.
	if ciClockSkewTolerance != 300*time.Second {
		t.Fatalf("Δskew = %s, want the frozen 300s", ciClockSkewTolerance)
	}
}

// Trusted-completion adjudication (ADR 0028 decision step 4, Issue #30
// expectation 2).

func deadlineAdjudicationInput() (domain.RemoteCheckRecord, time.Time, time.Time) {
	publishedAt := time.Date(2026, 8, 4, 10, 50, 0, 0, time.UTC)
	ciDeadline := time.Date(2026, 8, 4, 11, 50, 0, 0, time.UTC)
	checks := domain.RemoteCheckRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRemoteCheckRecord,
		TaskID: "TASK-DEADLINE", RunID: "run:task-deadline", Provider: "github",
		RepositoryID: "R_deadline0001", RequestID: "PR_deadline0001",
		HeadSHA: fabricatedSHA("2"), Status: domain.CheckStatusPass,
		Checks: []domain.RemoteCheck{
			{Name: "ci/test", Required: true, Status: domain.CheckStatusPass},
			// A non-required pending check never participates in the
			// timely-completion proof.
			{Name: "optional/lint", Required: false, Status: domain.CheckStatusPending},
		},
		ObservedAt: time.Date(2026, 8, 4, 11, 30, 0, 0, time.UTC),
	}
	return checks, ciDeadline, publishedAt
}

func TestAdjudicateTimelyCompletion(t *testing.T) {
	checks, ciDeadline, publishedAt := deadlineAdjudicationInput()
	completion := func(instant time.Time) map[string]time.Time {
		return map[string]time.Time{"ci/test": instant}
	}
	tests := []struct {
		name        string
		completions map[string]time.Time
		observedAt  time.Time
		want        error
	}{
		{"trusted completion admits late observation", completion(ciDeadline.Add(-30 * time.Minute)), ciDeadline.Add(time.Hour), nil},
		{"completion exactly at ciDeadline", completion(ciDeadline), ciDeadline.Add(time.Hour), nil},
		{"completion at ciDeadline plus skew", completion(ciDeadline.Add(ciClockSkewTolerance)), ciDeadline.Add(2 * time.Hour), nil},
		{"completion beyond ciDeadline plus skew fails closed", completion(ciDeadline.Add(ciClockSkewTolerance + time.Second)), ciDeadline.Add(2 * time.Hour), errCICompletedAtExceedsDeadline},
		{"completion at publishedAt minus skew consistent", completion(publishedAt.Add(-ciClockSkewTolerance)), publishedAt.Add(10 * time.Minute), nil},
		{"completion before publishedAt minus skew inconsistent", completion(publishedAt.Add(-ciClockSkewTolerance - time.Second)), publishedAt.Add(10 * time.Minute), errCICompletedAtInconsistent},
		{"observation before deadline proves missing completion", nil, ciDeadline.Add(-time.Second), nil},
		{"missing completion at deadline fails closed", nil, ciDeadline, errCICompletedAtMissing},
		{"missing completion after deadline fails closed", nil, ciDeadline.Add(time.Hour), errCICompletedAtMissing},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			err := adjudicateTimelyCompletion(checks, test.completions, ciDeadline, publishedAt, test.observedAt)
			if test.want == nil {
				if err != nil {
					t.Fatalf("adjudication = %v, want timely acceptance", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("adjudication = %v, want %v", err, test.want)
			}
		})
	}
}

// CompletedAt parsing contract (ADR 0028 backward compatibility: absent or
// unreadable facts are dropped, never presumed, never backfilled).

func TestParseCheckCompletionTimes(t *testing.T) {
	record := domain.RemoteCheckRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRemoteCheckRecord,
		TaskID: "TASK-DEADLINE", RunID: "run:task-deadline", Provider: "github",
		RepositoryID: "R_deadline0001", RequestID: "PR_deadline0001",
		HeadSHA: fabricatedSHA("2"), Status: domain.CheckStatusPass,
		Checks:     []domain.RemoteCheck{{Name: "ci/test", Required: true, Status: domain.CheckStatusPass}},
		ObservedAt: time.Date(2026, 8, 4, 11, 30, 0, 0, time.UTC),
	}

	t.Run("schema-valid record without completedAt yields no facts", func(t *testing.T) {
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if completions := parseCheckCompletionTimes(data); len(completions) != 0 {
			t.Fatalf("completions = %v, want none", completions)
		}
	})

	t.Run("completedAt facts are parsed and UTC-normalized", func(t *testing.T) {
		data := []byte(`{"checks":[{"name":"ci/test","required":true,"status":"pass","completedAt":"2026-08-04T11:20:00+08:00"}]}`)
		completions := parseCheckCompletionTimes(data)
		want := time.Date(2026, 8, 4, 3, 20, 0, 0, time.UTC)
		completedAt, ok := completions["ci/test"]
		if !ok || !completedAt.Equal(want) {
			t.Fatalf("completions = %v, want ci/test at %s", completions, want)
		}
	})

	t.Run("malformed timestamp drops the fact fail closed", func(t *testing.T) {
		data := []byte(`{"checks":[{"name":"ci/test","required":true,"status":"pass","completedAt":"not-a-time"}]}`)
		if completions := parseCheckCompletionTimes(data); len(completions) != 0 {
			t.Fatalf("completions = %v, want the malformed fact dropped", completions)
		}
	})

	t.Run("zero timestamp is dropped", func(t *testing.T) {
		data := []byte(`{"checks":[{"name":"ci/test","required":true,"status":"pass","completedAt":"0001-01-01T00:00:00Z"}]}`)
		if completions := parseCheckCompletionTimes(data); len(completions) != 0 {
			t.Fatalf("completions = %v, want the zero fact dropped", completions)
		}
	})

	t.Run("non-required entries are ignored", func(t *testing.T) {
		data := []byte(`{"checks":[{"name":"optional/lint","required":false,"status":"pass","completedAt":"2026-08-04T11:20:00Z"}]}`)
		if completions := parseCheckCompletionTimes(data); len(completions) != 0 {
			t.Fatalf("completions = %v, want non-required facts ignored", completions)
		}
	})
}

// Fabricated deadline fixtures: publication lineages with controlled
// createdAt / publishedAt / runTimeout shapes (the git-backed integration
// fixtures always create the run at wall-clock time, which cannot express a
// publication-anchored deadline that differs from the legacy one).

type deadlineFixtureConfig struct {
	createdAt   time.Time
	publishedAt time.Time
	runTimeout  int64
	// blockError terminates the lineage CI_PENDING -> BLOCKED with the given
	// publication.blocked error payload; empty keeps the run CI_PENDING.
	blockError string
}

// newCIDeadlineFixture fabricates the publication lineage (journal, snapshot
// and frozen evidence files) directly, mirroring newFabricatedRunFixture but
// with parametrized instants so the deadline facts under test are controlled:
// createdAt anchors the legacy run budget, publishedAt anchors the frozen
// ciDeadline.
func newCIDeadlineFixture(t *testing.T, config deadlineFixtureConfig) *publicationFixture {
	t.Helper()
	t.Setenv("MARSHAL_STATE_DIR", "")
	root := t.TempDir()
	stateRoot := filepath.Join(root, ".marshal")
	const taskID = "TASK-DEADLINE"
	const runID = "run:task-deadline"
	runDirectory := filepath.Join(stateRoot, "runs", runID)
	writeFile := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	baseSHA := fabricatedSHA("0")
	headSHA := fabricatedSHA("2")
	headBranch := deriveBranch(taskID, runID)

	repoIdentity, err := json.Marshal(map[string]string{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": "RepositoryIdentity", "repositoryRoot": root,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(stateRoot, "repo.json"), append(repoIdentity, '\n'))

	task := domain.TaskSpec{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindTask,
		Metadata:   domain.TaskMetadata{ID: taskID, Title: "CI deadline facts"},
		Repository: domain.TaskRepository{Path: root, BaseRef: baseSHA, Remote: "origin", ExpectedRemoteURL: "https://github.com/marshal-test/task-deadline.git"},
		Work:       domain.TaskWork{Objective: "Adjudicate CI deadline facts.", Constraints: []string{}, NonGoals: []string{}},
		Scope:      domain.TaskScope{AllowPaths: []string{"**"}, DenyPaths: []string{}, AllowSubmodules: false, MaxChangedFiles: 10, MaxDiffBytes: 100000},
		Acceptance: domain.TaskAcceptance{Commands: []domain.TaskCommand{}, AllowNoChange: false},
		Deliverables: []domain.TaskDeliverable{
			{ID: "source", Kind: "code", Required: true, PathGlob: "src/*.go", MediaType: "text/x-go", MinimumCount: 1},
			// publication.required=true demands a required publication
			// deliverable (contract semantic check
			// publication-deliverable-mismatch); the entry mirrors the
			// validated fabricated TaskSpec fixture field for field.
			{ID: "pull-request", Kind: "publication", Required: true, PathGlob: "docs/*.md", MediaType: "text/markdown", MinimumCount: 1},
		},
		Worker:  domain.TaskWorker{PreferredAdapter: "fake", FallbackAdapters: []string{}, ExecutionProfile: "workspace-write", SessionPolicy: "ephemeral"},
		Budgets: domain.TaskBudgets{RunTimeoutSeconds: config.runTimeout, AttemptTimeoutSeconds: 60, MaxAttempts: 3, MaxOperationalRetries: 1, MaxReworkRounds: 1, MaxOutputBytes: 100000},
		Publication: domain.TaskPublication{
			Required: true, Provider: "github", Mode: "draft", Remote: "origin",
			BaseBranch: "main", MergePolicy: "never", RequiredChecks: []string{"ci/test"},
		},
	}
	taskData, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindTask, taskData); err != nil {
		t.Fatalf("fabricated deadline TaskSpec failed schema validation: %v", err)
	}
	specDigest, err := canonical.DigestJSON(taskData)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(runDirectory, "task-spec.json"), taskData)

	evidenceDigest := fabricatedDigest("e")
	decision := domain.ReviewDecision{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision,
		TaskID: taskID, RunID: runID, ReviewRound: 1,
		Reviewer:   domain.Reviewer{Type: "lead-agent", ID: "deadline-facts"},
		SpecDigest: specDigest, ReviewPacketDigest: fabricatedDigest("a"),
		VerificationDigest: fabricatedDigest("1"), ArtifactManifestDigest: fabricatedDigest("9"),
		EvidenceDigest: evidenceDigest, Verdict: "accept", Summary: "accept and publish",
		BlockingFindings: []domain.Finding{}, NonBlockingFindings: []domain.Finding{},
		PublicationRecommendation: "publish", MergeRecommendation: "do-not-merge",
		DecidedAt: config.createdAt,
	}
	decisionData, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindReviewDecision, decisionData); err != nil {
		t.Fatalf("fabricated deadline ReviewDecision failed schema validation: %v", err)
	}
	decisionDigest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(runDirectory, "decisions", "decision-001.json"), decisionData)

	publication := domain.PublicationRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPublicationRecord,
		TaskID: taskID, RunID: runID, Provider: "github",
		Repository: domain.PublicationRepository{ID: "R_deadline0001", NameWithOwner: "marshal-test/task-deadline", URL: "https://github.com/marshal-test/task-deadline"},
		Remote:     "origin", BaseBranch: "main", HeadBranch: headBranch, ReviewRound: 1,
		BaseSHA: baseSHA, HeadSHA: headSHA, CommitSHA: headSHA,
		SnapshotDigest: fabricatedDigest("3"), DiffDigest: fabricatedDigest("5"),
		SpecDigest: specDigest, PolicyDigest: fabricatedDigest("c"),
		EvidenceDigest: evidenceDigest, VerificationDigest: fabricatedDigest("1"),
		ReviewDecisionDigest: decisionDigest,
		Marker:               marker(taskID, runID), Mode: "draft", MergePolicy: "never",
		Request: domain.PullRequestRecord{ID: "PR_deadline0001", Number: 7, URL: "https://github.com/marshal-test/task-deadline/pull/7", Draft: true, State: "OPEN"},
		Actor:   "marshal-fake-publisher", PublishedAt: config.publishedAt, UpdatedAt: config.publishedAt,
	}
	publicationData, err := json.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindPublicationRecord, publicationData); err != nil {
		t.Fatalf("fabricated deadline PublicationRecord failed schema validation: %v", err)
	}
	publicationDigest, err := canonical.DigestJSON(publicationData)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(runDirectory, "publication-record.json"), publicationData)

	store := runstore.New(stateRoot)
	lease, err := store.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	publisherActor := &domain.Actor{Type: "publisher", ID: "marshal-github-publisher"}
	type fabricatedTransition struct {
		from, to  domain.State
		eventType string
		attemptID string
		actor     *domain.Actor
		payload   map[string]any
	}
	transitions := []fabricatedTransition{
		{domain.StateCreated, domain.StatePlanned, "task.planned", "", nil, map[string]any{"fixture": true}},
		{domain.StatePlanned, domain.StateReady, "task.ready", "", nil, map[string]any{"fixture": true}},
		{domain.StateReady, domain.StateRunning, "worker.started", "attempt:1", &domain.Actor{Type: "system", ID: "marshal-worker-runner"}, map[string]any{"fixture": true}},
		{domain.StateRunning, domain.StateVerifying, "worker.completed", "", &domain.Actor{Type: "system", ID: "marshal-worker-runner"}, map[string]any{"fixture": true}},
		{domain.StateVerifying, domain.StateReviewPending, "verification.completed", "", &domain.Actor{Type: "system", ID: "marshal-verifier"}, map[string]any{"reportDigest": fabricatedDigest("1"), "artifactManifestDigest": fabricatedDigest("9"), "status": "pass"}},
		{domain.StateReviewPending, domain.StatePublishing, "review.accept", "", &domain.Actor{Type: "system", ID: "marshal-review"}, map[string]any{"verdict": "accept", "decisionDigest": decisionDigest, "evidenceDigest": evidenceDigest}},
		{domain.StatePublishing, domain.StatePublished, "publication.completed", "", publisherActor, map[string]any{
			"publicationDigest": publicationDigest, "provider": "github", "repository": "marshal-test/task-deadline",
			"headBranch": headBranch, "baseBranch": "main", "externalId": "PR_deadline0001",
			"headSha": headSHA, "uri": "https://github.com/marshal-test/task-deadline/pull/7",
		}},
		{domain.StatePublished, domain.StateCIPending, "publication.checks-requested", "", publisherActor, map[string]any{"requiredChecks": []any{"ci/test"}}},
	}
	if config.blockError != "" {
		transitions = append(transitions, fabricatedTransition{domain.StateCIPending, domain.StateBlocked, "publication.blocked", "", publisherActor, map[string]any{"error": config.blockError, "terminalReason": "publication safety gate failed"}})
	}
	for index, transition := range transitions {
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
			EventID: fmt.Sprintf("event:%s:%d", runID, index+1), RunID: runID,
			Sequence: uint64(index + 1), Type: transition.eventType,
			StateFrom: transition.from, StateTo: transition.to, Timestamp: config.createdAt,
			Actor: transition.actor, Payload: transition.payload,
		}
		if transition.attemptID != "" {
			event.AttemptID = transition.attemptID
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
	}
	state := domain.NewRunState(taskID, runID, config.createdAt)
	state.Sequence = uint64(len(transitions))
	state.SpecDigest = specDigest
	state.BaseSHA = baseSHA
	state.AttemptsUsed, state.ReviewRound, state.CurrentAttemptID = 1, 1, "attempt:1"
	state.Publication = &domain.RunPublication{Provider: "github", Repository: "marshal-test/task-deadline", HeadBranch: headBranch, BaseBranch: "main", ExternalID: "PR_deadline0001", URI: "https://github.com/marshal-test/task-deadline/pull/7", HeadSHA: headSHA}
	if config.blockError != "" {
		state.State = domain.StateBlocked
		state.TerminalReason = "publication safety gate failed"
	} else {
		state.State = domain.StateCIPending
	}
	state.UpdatedAt = config.createdAt
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Inspect(runID); err != nil {
		t.Fatalf("fabricated deadline run lineage is inconsistent: %v", err)
	}
	if config.blockError != "" {
		prepared, err := review.PrepareOutcome(runDirectory, review.OutcomeData{
			TaskID: taskID, RunID: runID, TerminalState: domain.StateBlocked, Verdict: "blocked",
			FinalReviewRound: 1, FinalReviewDigest: decisionDigest, FinalEvidenceDigest: evidenceDigest,
			Summary: "publication safety gate failed: " + config.blockError, GeneratedAt: config.createdAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := prepared.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	return &publicationFixture{
		t: t, repository: root, stateRoot: stateRoot, runDirectory: runDirectory,
		taskID: taskID, runID: runID, baseSHA: baseSHA,
		specDigest: specDigest, evidenceDigest: evidenceDigest,
		validator: validator, store: store,
	}
}

// deadlineFixtureInstants returns the canonical Issue #30 reproduction shape
// used across the deadline facts tests: the run budget expires at 11:00 while
// the frozen publication-anchored ciDeadline is 11:50, so observations in
// (11:00, 11:50) are late relative to the legacy anchor but inside the
// frozen adjudication window.
func deadlineFixtureInstants() (createdAt, publishedAt, legacyDeadline, ciDeadline time.Time) {
	createdAt = time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	publishedAt = time.Date(2026, 8, 4, 10, 50, 0, 0, time.UTC)
	legacyDeadline = createdAt.Add(time.Hour)
	ciDeadline = publishedAt.Add(time.Hour)
	return createdAt, publishedAt, legacyDeadline, ciDeadline
}

func TestRunBlockedByCIDeadlineDetection(t *testing.T) {
	createdAt, publishedAt, _, _ := deadlineFixtureInstants()
	t.Run("deadline block recognized", func(t *testing.T) {
		fixture := newCIDeadlineFixture(t, deadlineFixtureConfig{createdAt: createdAt, publishedAt: publishedAt, runTimeout: 3600, blockError: errCIDeadlineExceeded.Error()})
		blocked, err := runBlockedByCIDeadline(fixture.store, fixture.runID)
		if err != nil || !blocked {
			t.Fatalf("deadline-blocked run not recognized: blocked=%v err=%v", blocked, err)
		}
	})
	t.Run("other block not recognized", func(t *testing.T) {
		fixture := newCIDeadlineFixture(t, deadlineFixtureConfig{createdAt: createdAt, publishedAt: publishedAt, runTimeout: 3600, blockError: "remote PR head or identity changed"})
		blocked, err := runBlockedByCIDeadline(fixture.store, fixture.runID)
		if err != nil || blocked {
			t.Fatalf("non-deadline block misrecognized: blocked=%v err=%v", blocked, err)
		}
	})
	t.Run("unblocked run not recognized", func(t *testing.T) {
		fixture := newCIDeadlineFixture(t, deadlineFixtureConfig{createdAt: createdAt, publishedAt: publishedAt, runTimeout: 3600})
		blocked, err := runBlockedByCIDeadline(fixture.store, fixture.runID)
		if err != nil || blocked {
			t.Fatalf("CI_PENDING run misrecognized: blocked=%v err=%v", blocked, err)
		}
	})
}
