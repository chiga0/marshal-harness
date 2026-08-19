package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// blockedPostPublicationFixture fabricates the ADR 0026 qualification shape:
// a run terminally BLOCKED after a successful publication (CI_PENDING ->
// BLOCKED). All such blocks share one generic terminalReason, so eligibility
// must come from the current-ledger recheck, never the reason.
func blockedPostPublicationFixture(t *testing.T, opts fixtureOptions) *publicationFixture {
	t.Helper()
	fixture := newFabricatedRunFixture(t, opts, fabricatedBlocked)
	state := fixture.inspect(t)
	if state.State != domain.StateBlocked {
		t.Fatalf("state = %s, want BLOCKED", state.State)
	}
	if state.Publication == nil {
		t.Fatal("blocked fixture lost its publication snapshot")
	}
	return fixture
}

func fabricatedCIPendingFixture(t *testing.T, opts fixtureOptions) *publicationFixture {
	t.Helper()
	fixture := newFabricatedRunFixture(t, opts, fabricatedCIPending)
	if state := fixture.inspect(t); state.State != domain.StateCIPending {
		t.Fatalf("state = %s, want CI_PENDING", state.State)
	}
	return fixture
}

func fabricatedReworkRequestedFixture(t *testing.T, opts fixtureOptions) *publicationFixture {
	t.Helper()
	fixture := newFabricatedRunFixture(t, opts, fabricatedReworkRequested)
	if state := fixture.inspect(t); state.State != domain.StateReworkRequested {
		t.Fatalf("state = %s, want REWORK_REQUESTED", state.State)
	}
	return fixture
}

// fabricatedMode selects the lifecycle shape of a fabricated publication
// lineage.
type fabricatedMode int

const (
	fabricatedCIPending fabricatedMode = iota
	fabricatedBlocked
	fabricatedReworkRequested
	fabricatedBlockedBeforePublication
)

// fabricatedSHA assembles a deterministic 40-hex object id (helper
// construction keeps fixture literals gitleaks-safe).
func fabricatedSHA(fill string) string { return strings.Repeat(fill, 40) }

// fabricatedDigest assembles a canonical sha256 digest literal (helper
// construction keeps fixture literals gitleaks-safe).
func fabricatedDigest(fill string) string { return "sha256:" + strings.Repeat(fill, 64) }

// newFabricatedRunFixture fabricates the publication lineage (journal,
// snapshot and frozen evidence files) directly, without any git repository,
// worktree or controlled commit: the reconcile and merged-head check paths
// under test consume only the frozen files and the journal. This keeps the
// ADR 0026 negative matrix hermetic and fast enough for the -race suite
// while exercising the identical consumption contract as the git-backed
// lineage (the end-to-end CLI tests still cover the full integration).
func newFabricatedRunFixture(t *testing.T, opts fixtureOptions, mode fabricatedMode) *publicationFixture {
	t.Helper()
	t.Setenv("MARSHAL_STATE_DIR", "")
	root := t.TempDir()
	stateRoot := filepath.Join(root, ".marshal")
	const taskID = "TASK-PUB"
	const runID = "run:task-pub"
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
	now := time.Now().UTC()
	baseSHA := fabricatedSHA("0")
	headSHA := fabricatedSHA("2")
	headBranch := deriveBranch(taskID, runID)
	publishedAt := time.Date(2026, 8, 4, 12, 15, 0, 0, time.UTC)

	// RepositoryIdentity binding consumed by the reconcile authority
	// namespace derivation.
	repoIdentity, err := json.Marshal(map[string]string{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": "RepositoryIdentity", "repositoryRoot": root,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(stateRoot, "repo.json"), append(repoIdentity, '\n'))

	task := domain.TaskSpec{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindTask,
		Metadata:   domain.TaskMetadata{ID: taskID, Title: "Publish controlled commit"},
		Repository: domain.TaskRepository{Path: root, BaseRef: baseSHA, Remote: "origin", ExpectedRemoteURL: "https://github.com/marshal-test/task-repo.git"},
		Work:       domain.TaskWork{Objective: "Publish a controlled commit for review.", Constraints: []string{}, NonGoals: []string{}},
		Scope:      domain.TaskScope{AllowPaths: []string{"**"}, DenyPaths: []string{}, AllowSubmodules: false, MaxChangedFiles: 10, MaxDiffBytes: 100000},
		Acceptance: domain.TaskAcceptance{Commands: []domain.TaskCommand{}, AllowNoChange: false},
		Deliverables: []domain.TaskDeliverable{
			{ID: "source", Kind: "code", Required: true, PathGlob: "src/*.go", MediaType: "text/x-go", MinimumCount: 1},
			{ID: "pull-request", Kind: "publication", Required: true, PathGlob: "docs/*.md", MediaType: "text/markdown", MinimumCount: 1},
		},
		Worker:  domain.TaskWorker{PreferredAdapter: "fake", FallbackAdapters: []string{}, ExecutionProfile: "workspace-write", SessionPolicy: "ephemeral"},
		Budgets: domain.TaskBudgets{RunTimeoutSeconds: 3600, AttemptTimeoutSeconds: 60, MaxAttempts: 3, MaxOperationalRetries: 1, MaxReworkRounds: opts.maxReworkRounds, MaxOutputBytes: 100000},
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
		t.Fatalf("fabricated TaskSpec failed schema validation: %v", err)
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
		Reviewer:   domain.Reviewer{Type: "lead-agent", ID: "publication-integration"},
		SpecDigest: specDigest, ReviewPacketDigest: fabricatedDigest("a"),
		VerificationDigest: fabricatedDigest("1"), ArtifactManifestDigest: fabricatedDigest("9"),
		EvidenceDigest: evidenceDigest, Verdict: "accept", Summary: "accept and publish",
		BlockingFindings: []domain.Finding{}, NonBlockingFindings: []domain.Finding{},
		PublicationRecommendation: "publish", MergeRecommendation: "do-not-merge",
		DecidedAt: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	}
	decisionData, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindReviewDecision, decisionData); err != nil {
		t.Fatalf("fabricated ReviewDecision failed schema validation: %v", err)
	}
	decisionDigest, err := canonical.DigestJSON(decisionData)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(runDirectory, "decisions", "decision-001.json"), decisionData)

	publication := domain.PublicationRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPublicationRecord,
		TaskID: taskID, RunID: runID, Provider: "github",
		Repository: domain.PublicationRepository{ID: "R_marshaltest0001", NameWithOwner: "marshal-test/task-repo", URL: "https://github.com/marshal-test/task-repo"},
		Remote:     "origin", BaseBranch: "main", HeadBranch: headBranch, ReviewRound: 1,
		BaseSHA: baseSHA, HeadSHA: headSHA, CommitSHA: headSHA,
		SnapshotDigest: fabricatedDigest("3"), DiffDigest: fabricatedDigest("5"),
		SpecDigest: specDigest, PolicyDigest: fabricatedDigest("c"),
		EvidenceDigest: evidenceDigest, VerificationDigest: fabricatedDigest("1"),
		ReviewDecisionDigest: decisionDigest,
		Marker:               marker(taskID, runID), Mode: "draft", MergePolicy: "never",
		Request: domain.PullRequestRecord{ID: "PR_marshaltest0001", Number: 7, URL: "https://github.com/marshal-test/task-repo/pull/7", Draft: true, State: "OPEN"},
		Actor:   "marshal-fake-publisher", PublishedAt: publishedAt, UpdatedAt: publishedAt,
	}
	publicationData, err := json.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindPublicationRecord, publicationData); err != nil {
		t.Fatalf("fabricated PublicationRecord failed schema validation: %v", err)
	}
	publicationDigest, err := canonical.DigestJSON(publicationData)
	if err != nil {
		t.Fatal(err)
	}
	if mode != fabricatedBlockedBeforePublication {
		writeFile(filepath.Join(runDirectory, "publication-record.json"), publicationData)
	}

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
	}
	if mode != fabricatedBlockedBeforePublication {
		transitions = append(transitions,
			fabricatedTransition{domain.StatePublishing, domain.StatePublished, "publication.completed", "", publisherActor, map[string]any{
				"publicationDigest": publicationDigest, "provider": "github", "repository": "marshal-test/task-repo",
				"headBranch": headBranch, "baseBranch": "main", "externalId": "PR_marshaltest0001",
				"headSha": headSHA, "uri": "https://github.com/marshal-test/task-repo/pull/7",
			}},
			fabricatedTransition{domain.StatePublished, domain.StateCIPending, "publication.checks-requested", "", publisherActor, map[string]any{"requiredChecks": []any{"ci/test"}}},
		)
	}
	switch mode {
	case fabricatedBlocked:
		transitions = append(transitions, fabricatedTransition{domain.StateCIPending, domain.StateBlocked, "publication.blocked", "", publisherActor, map[string]any{"error": "remote PR head or identity changed", "terminalReason": "publication safety gate failed"}})
	case fabricatedBlockedBeforePublication:
		transitions = append(transitions, fabricatedTransition{domain.StatePublishing, domain.StateBlocked, "publication.blocked", "", publisherActor, map[string]any{"error": "simulated permanent publisher rejection", "terminalReason": "publication safety gate failed"}})
	case fabricatedReworkRequested:
		transitions = append(transitions, fabricatedTransition{domain.StateCIPending, domain.StateReworkRequested, "publication.checks-failed", "", publisherActor, map[string]any{"headSha": headSHA}})
	case fabricatedCIPending:
		// CI_PENDING keeps the checks-requested tail without a further transition.
	}
	for index, transition := range transitions {
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
			EventID: fmt.Sprintf("event:%s:%d", strings.ToLower(runID), index+1), RunID: runID,
			Sequence: uint64(index + 1), Type: transition.eventType,
			StateFrom: transition.from, StateTo: transition.to, Timestamp: now,
			Actor: transition.actor, Payload: transition.payload,
		}
		if transition.attemptID != "" {
			event.AttemptID = transition.attemptID
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
	}
	state := domain.NewRunState(taskID, runID, now)
	state.Sequence = uint64(len(transitions))
	state.SpecDigest = specDigest
	state.BaseSHA = baseSHA
	state.AttemptsUsed, state.ReviewRound, state.CurrentAttemptID = 1, 1, "attempt:1"
	if mode != fabricatedBlockedBeforePublication {
		state.Publication = &domain.RunPublication{Provider: "github", Repository: "marshal-test/task-repo", HeadBranch: headBranch, BaseBranch: "main", ExternalID: "PR_marshaltest0001", URI: "https://github.com/marshal-test/task-repo/pull/7", HeadSHA: headSHA}
	}
	switch mode {
	case fabricatedCIPending:
		state.State = domain.StateCIPending
	case fabricatedReworkRequested:
		state.State = domain.StateReworkRequested
		state.ReworkRoundsUsed = 1
	default:
		state.State = domain.StateBlocked
		state.TerminalReason = "publication safety gate failed"
	}
	state.UpdatedAt = now
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Inspect(runID); err != nil {
		t.Fatalf("fabricated run lineage is inconsistent: %v", err)
	}
	if mode == fabricatedBlocked {
		prepared, err := review.PrepareOutcome(runDirectory, review.OutcomeData{
			TaskID: taskID, RunID: runID, TerminalState: domain.StateBlocked, Verdict: "blocked",
			FinalReviewRound: 1, FinalReviewDigest: decisionDigest, FinalEvidenceDigest: evidenceDigest,
			Summary: "publication safety gate failed: remote PR head or identity changed", GeneratedAt: now,
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

type reconcileHarness struct {
	fixture       *publicationFixture
	mergeObserver *fakeMergeObserver
	checkObserver *fakeObserver
	now           time.Time
}

func newReconcileHarness(t *testing.T, fixture *publicationFixture) *reconcileHarness {
	t.Helper()
	return &reconcileHarness{
		fixture:       fixture,
		mergeObserver: &fakeMergeObserver{build: receiptFromPublication},
		checkObserver: &fakeObserver{status: "pass"},
		now:           time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
	}
}

func (h *reconcileHarness) reconcile(t *testing.T) (ReconcileResult, error) {
	t.Helper()
	return Reconcile(context.Background(), ReconcileInput{
		StateRoot: h.fixture.stateRoot, RunID: h.fixture.runID,
		MergeObserver: h.mergeObserver, CheckObserver: h.checkObserver,
		Validator: h.fixture.validator, ReconciledBy: "maintainer", Now: h.now,
	})
}

func fixturePublicationBytes(t *testing.T, fixture *publicationFixture) ([]byte, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.runDirectory, "publication-record.json"))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return data, digest
}

func readReconcileRecords(t *testing.T, fixture *publicationFixture) []domain.PublicationReconcileRecord {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(fixture.runDirectory, "publication-reconcile-records"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var records []domain.PublicationReconcileRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixture.runDirectory, "publication-reconcile-records", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.validator.Validate(domain.KindPublicationReconcileRecord, data); err != nil {
			t.Fatalf("reconcile record failed schema validation: %v", err)
		}
		var record domain.PublicationReconcileRecord
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

func lastJournalEvent(t *testing.T, fixture *publicationFixture) domain.RunEvent {
	t.Helper()
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("journal is empty")
	}
	return events[len(events)-1]
}

func countJournalEvents(t *testing.T, fixture *publicationFixture, eventType string) int {
	t.Helper()
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

// TestReconcileMigratesBlockedRunToAcceptedAfterMerge is the Issue #25
// positive: a merged green PR whose run was terminally BLOCKED migrates to
// ACCEPTED even when the head branch has already been deleted — the receipt
// binds the PR node's immutable facts, not the branch.
func TestReconcileMigratesBlockedRunToAcceptedAfterMerge(t *testing.T) {
	fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
	harness := newReconcileHarness(t, fixture)
	_, publicationDigest := fixturePublicationBytes(t, fixture)

	result, err := harness.reconcile(t)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if result.State.State != domain.StateAccepted || result.State.TerminalReason != ReconcileTerminalReason {
		t.Fatalf("state = %+v", result.State)
	}
	if result.State.Publication == nil || result.State.Publication.HeadSHA == "" {
		t.Fatalf("publication snapshot lost on reconcile: %+v", result.State.Publication)
	}

	// Immutable receipt persisted and bound.
	receipt := readPersistedReceipt(t, fixture)
	if receipt.RunID != fixture.runID || receipt.PublicationRecordID != publicationDigest || receipt.ReceiptDigest != result.Receipt.ReceiptDigest {
		t.Fatalf("receipt binding = %+v", receipt)
	}

	// Append-only record with the closed evidence digest set.
	records := readReconcileRecords(t, fixture)
	if len(records) != 1 {
		t.Fatalf("reconcile records = %d, want 1", len(records))
	}
	record := records[0]
	if record.ReconcileType != ReconcileTypeAcceptAfterMerge || record.ObservedState != domain.StateBlocked || record.DecidedState != domain.StateAccepted {
		t.Fatalf("record enumerations = %+v", record)
	}
	if record.ReconcileReason != ReconcileReasonMergedHead || record.ReconciledBy != "maintainer" || record.SCMMergeReceiptID != receipt.ReceiptID {
		t.Fatalf("record identity = %+v", record)
	}
	if len(record.EvidenceDigests) != 4 || record.EvidenceDigests[0] != publicationDigest || record.EvidenceDigests[2] != receipt.ReceiptDigest {
		t.Fatalf("evidence digests = %+v", record.EvidenceDigests)
	}
	recomputed, err := record.Digest()
	if err != nil || record.ReconcileRecordDigest != recomputed {
		t.Fatalf("record digest mismatch: %v", err)
	}
	if record.ReconcileID != result.Record.ReconcileID || !strings.HasPrefix(record.ReconcileID, "reconcile-") {
		t.Fatalf("reconcileId = %q", record.ReconcileID)
	}

	// Materialized merged-head RemoteCheckRecord bound to the publication.
	checkData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "remote-check-record.json"))
	if err != nil {
		t.Fatalf("merged-head check record missing: %v", err)
	}
	var checks domain.RemoteCheckRecord
	if err := json.Unmarshal(checkData, &checks); err != nil {
		t.Fatal(err)
	}
	if checks.Status != "pass" || checks.RunID != fixture.runID {
		t.Fatalf("merged-head checks = %+v", checks)
	}
	if record.EvidenceDigests[3] != canonical.DigestBytes(mustCanonical(t, checkData)) {
		t.Fatalf("record does not bind the RemoteCheckRecord digest")
	}

	// Journal event with the frozen reconciliation actor and payload.
	event := lastJournalEvent(t, fixture)
	if event.Type != lifecycle.PublicationReconcileEventType || event.StateFrom != domain.StateBlocked || event.StateTo != domain.StateAccepted {
		t.Fatalf("reconcile event = %+v", event)
	}
	if event.Actor == nil || event.Actor.Type != "system" || event.Actor.ID != "marshal-reconciliation" {
		t.Fatalf("reconcile event actor = %+v", event.Actor)
	}
	for _, key := range []string{"receiptDigest", "reconcileId", "publicationDigest", "decisionDigest", "terminalReason"} {
		if value, _ := event.Payload[key].(string); value == "" {
			t.Fatalf("reconcile event payload lacks %s: %+v", key, event.Payload)
		}
	}
	if event.Payload["terminalReason"] != ReconcileTerminalReason || event.Payload["publicationDigest"] != publicationDigest {
		t.Fatalf("reconcile event payload = %+v", event.Payload)
	}
	if event.AttemptID != "" {
		t.Fatalf("reconcile event must carry no attempt id: %+v", event)
	}

	// Blocked outcome archived (never deleted), accepted outcome written.
	archives, err := filepath.Glob(filepath.Join(fixture.runDirectory, "outcomes", "blocked-*", "outcome.json"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("blocked outcome archive = %v (err=%v)", archives, err)
	}
	archivedData, err := os.ReadFile(archives[0])
	if err != nil {
		t.Fatal(err)
	}
	var archived domain.OutcomeBundle
	if err := json.Unmarshal(archivedData, &archived); err != nil {
		t.Fatal(err)
	}
	if archived.TerminalState != domain.StateBlocked {
		t.Fatalf("archived outcome = %+v", archived)
	}
	outcomeData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "outcome.json"))
	if err != nil {
		t.Fatalf("accepted outcome missing: %v", err)
	}
	var outcome domain.OutcomeBundle
	if err := json.Unmarshal(outcomeData, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != domain.StateAccepted || outcome.Verdict != "accept" {
		t.Fatalf("accepted outcome = %+v", outcome)
	}

	// PublicationRecord and ReviewDecision are never rewritten by reconcile.
	_, afterDigest := fixturePublicationBytes(t, fixture)
	if afterDigest != publicationDigest {
		t.Fatalf("PublicationRecord rewritten by reconcile: %s vs %s", afterDigest, publicationDigest)
	}

	// No sensitive material: receipt, record and event carry no local
	// absolute paths, state roots or credential markers.
	for label, document := range map[string]string{
		"receipt": mustMarshal(t, result.Receipt),
		"record":  mustMarshal(t, result.Record),
		"event":   mustMarshal(t, event),
	} {
		for _, forbidden := range []string{fixture.stateRoot, fixture.repository, "token", "GH_CONFIG_DIR"} {
			if strings.Contains(document, forbidden) {
				t.Fatalf("%s leaks %q", label, forbidden)
			}
		}
	}

	// Journal replay reaches the identical ACCEPTED state.
	replayed, err := fixture.store.Inspect(fixture.runID)
	if err != nil || replayed.State != domain.StateAccepted || replayed.TerminalReason != ReconcileTerminalReason {
		t.Fatalf("replayed state = %+v, err = %v", replayed, err)
	}
}

func mustMarshal(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustCanonical(t *testing.T, data []byte) []byte {
	t.Helper()
	canonicalBytes, err := canonical.JSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalBytes
}

func TestReconcileIdempotencyIdentityConflicts(t *testing.T) {
	t.Run("identical identity and content merges without a second event", func(t *testing.T) {
		fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
		harness := newReconcileHarness(t, fixture)
		first, err := harness.reconcile(t)
		if err != nil {
			t.Fatal(err)
		}
		second, err := harness.reconcile(t)
		if err != nil {
			t.Fatalf("offline idempotent replay failed: %v", err)
		}
		if second.Record.ReconcileID != first.Record.ReconcileID {
			t.Fatalf("second replay produced a different record: %s vs %s", second.Record.ReconcileID, first.Record.ReconcileID)
		}
		if count := countJournalEvents(t, fixture, lifecycle.PublicationReconcileEventType); count != 1 {
			t.Fatalf("reconcile events = %d, want exactly 1", count)
		}
		if len(readReconcileRecords(t, fixture)) != 1 {
			t.Fatal("idempotent replay duplicated the reconcile record")
		}
		if harness.mergeObserver.calls != 1 {
			t.Fatalf("offline replay must not observe the network again: %d calls", harness.mergeObserver.calls)
		}
	})

	t.Run("identical identity with different key content conflicts", func(t *testing.T) {
		fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
		harness := newReconcileHarness(t, fixture)
		record := harness.buildRecord(t, func(record *domain.PublicationReconcileRecord) { record.ReconciledBy = "intruder" })
		harness.writeRecord(t, record)
		_, err := harness.reconcile(t)
		if err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Fatalf("expected identity conflict, got %v", err)
		}
		if state := fixture.inspect(t); state.State != domain.StateBlocked {
			t.Fatalf("conflict must keep the run BLOCKED: %+v", state)
		}
		if count := countJournalEvents(t, fixture, lifecycle.PublicationReconcileEventType); count != 0 {
			t.Fatalf("conflict must not append a reconcile event: %d", count)
		}
	})

	t.Run("second receipt with a different merge commit conflicts", func(t *testing.T) {
		fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
		harness := newReconcileHarness(t, fixture)
		// Persist a receipt for a different merge commit first; the immutable
		// receipt file must then conflict with the newly observed fact.
		other := &fakeMergeObserver{build: func(publication domain.PublicationRecord, publicationDigest string) domain.Record {
			receipt := buildReceipt(publication, publicationDigest, strings.Repeat("9", 40))
			data, _ := json.Marshal(receipt)
			return domain.Record{Kind: domain.KindSCMMergeReceipt, Data: data}
		}}
		receiptRecord, err := other.ObserveMergeReceipt(context.Background(), harness.publicationRecord(t))
		if err != nil {
			t.Fatal(err)
		}
		_, digest := fixturePublicationBytes(t, fixture)
		var publication domain.PublicationRecord
		data, _ := fixturePublicationBytes(t, fixture)
		if err := json.Unmarshal(data, &publication); err != nil {
			t.Fatal(err)
		}
		if _, err := persistMergeReceipt(fixture.runDirectory, fixture.validator, receiptRecord, fixture.runID, publication, digest); err != nil {
			t.Fatal(err)
		}
		_, err = harness.reconcile(t)
		if err == nil || !strings.Contains(err.Error(), "SCMMergeReceipt conflicts") {
			t.Fatalf("expected immutable receipt conflict, got %v", err)
		}
		if state := fixture.inspect(t); state.State != domain.StateBlocked {
			t.Fatalf("receipt conflict must keep the run BLOCKED: %+v", state)
		}
	})

	t.Run("tampered stored record digest fails closed", func(t *testing.T) {
		fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
		harness := newReconcileHarness(t, fixture)
		record := harness.buildRecord(t, func(record *domain.PublicationReconcileRecord) {})
		record.ReconciledBy = "tampered-after-digest"
		harness.writeRecord(t, record)
		_, err := harness.reconcile(t)
		if err == nil || !strings.Contains(err.Error(), "digest") {
			t.Fatalf("expected digest recomputation conflict, got %v", err)
		}
		if state := fixture.inspect(t); state.State != domain.StateBlocked {
			t.Fatalf("tampered record must keep the run BLOCKED: %+v", state)
		}
	})
}

// buildRecord replicates the exact record Reconcile would build for the
// current fixture so crash-cut and conflict tests can pre-persist it. The
// mutation hook runs before the detached digest is computed; callers that
// need a stale digest tamper the returned record afterwards.
func (h *reconcileHarness) buildRecord(t *testing.T, mutate func(*domain.PublicationReconcileRecord)) domain.PublicationReconcileRecord {
	t.Helper()
	fixture := h.fixture
	publicationData, publicationDigest := fixturePublicationBytes(t, fixture)
	receiptRecord, err := h.mergeObserver.ObserveMergeReceipt(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := persistMergeReceipt(fixture.runDirectory, fixture.validator, receiptRecord, fixture.runID, mustPublication(t, publicationData), publicationDigest)
	if err != nil {
		t.Fatal(err)
	}
	checkRecord, err := h.checkObserver.ObserveChecks(context.Background(), domain.Record{Kind: domain.KindPublicationRecord, Data: publicationData}, []string{"ci/test"})
	if err != nil {
		t.Fatal(err)
	}
	checkDigest, err := canonical.DigestJSON(checkRecord.Data)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := frozenEvidence(fixture.store, fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	authorityNamespaceID, err := reconcileAuthorityNamespaceID(fixture.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	record := domain.PublicationReconcileRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPublicationReconcileRecord,
		ReconcileID:          reconcileID(authorityNamespaceID, fixture.runID, receipt.ReceiptID, ReconcileTypeAcceptAfterMerge),
		AuthorityNamespaceID: authorityNamespaceID,
		RunID:                fixture.runID,
		SCMMergeReceiptID:    receipt.ReceiptID,
		ReconcileType:        ReconcileTypeAcceptAfterMerge,
		ObservedState:        domain.StateBlocked,
		DecidedState:         domain.StateAccepted,
		EvidenceDigests:      []string{publicationDigest, frozen.decisionDigest, receipt.ReceiptDigest, checkDigest},
		ReconcileReason:      ReconcileReasonMergedHead,
		ReconciledBy:         "maintainer",
		ReconciledAt:         h.now,
	}
	if mutate != nil {
		mutate(&record)
	}
	digest, err := record.Digest()
	if err != nil {
		t.Fatal(err)
	}
	record.ReconcileRecordDigest = digest
	return record
}

func (h *reconcileHarness) writeRecord(t *testing.T, record domain.PublicationReconcileRecord) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.fixture.runDirectory, "publication-reconcile-records", record.ReconcileID+".json")
	if err := atomicWrite(path, append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

func (h *reconcileHarness) publicationRecord(t *testing.T) domain.Record {
	t.Helper()
	data, _ := fixturePublicationBytes(t, h.fixture)
	return domain.Record{Kind: domain.KindPublicationRecord, Data: data}
}

func mustPublication(t *testing.T, data []byte) domain.PublicationRecord {
	t.Helper()
	var publication domain.PublicationRecord
	if err := json.Unmarshal(data, &publication); err != nil {
		t.Fatal(err)
	}
	return publication
}

// TestReconcileCrashCutReplayMergesWithoutDuplicateEvents simulates a crash
// after the receipt and record were persisted but before the journal event
// was appended: replay must merge both artifacts and append exactly one event.
func TestReconcileCrashCutReplayMergesWithoutDuplicateEvents(t *testing.T) {
	fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
	harness := newReconcileHarness(t, fixture)
	crashInstant := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)
	harness.now = crashInstant
	record := harness.buildRecord(t, nil)
	// buildRecord already persisted the receipt; persist the record under its
	// deterministic identity to complete the pre-crash state.
	if _, err := persistReconcileRecord(fixture.runDirectory, fixture.validator, record, mustRecordBytes(t, record)); err != nil {
		t.Fatal(err)
	}
	recordedAt := record.ReconciledAt
	// The replay happens at a later instant: key-stable content must merge
	// without rewriting the original reconciledAt.
	harness.now = crashInstant.Add(time.Hour)

	result, err := harness.reconcile(t)
	if err != nil {
		t.Fatalf("crash-cut replay failed: %v", err)
	}
	if result.State.State != domain.StateAccepted {
		t.Fatalf("state = %+v", result.State)
	}
	if count := countJournalEvents(t, fixture, lifecycle.PublicationReconcileEventType); count != 1 {
		t.Fatalf("reconcile events after crash-cut replay = %d, want exactly 1", count)
	}
	records := readReconcileRecords(t, fixture)
	if len(records) != 1 {
		t.Fatalf("records = %d, want the merged single record", len(records))
	}
	// The original bytes survive the replay: reconciledAt is key-stable
	// content and is not rewritten with the replay instant.
	if !records[0].ReconciledAt.Equal(recordedAt) {
		t.Fatalf("crash-cut replay rewrote reconciledAt: %v vs %v", records[0].ReconciledAt, recordedAt)
	}
	if records[0].ReconcileRecordDigest != record.ReconcileRecordDigest {
		t.Fatalf("crash-cut replay rewrote the record digest")
	}
}

func mustRecordBytes(t *testing.T, record domain.PublicationReconcileRecord) []byte {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReconcileRejectsIneligibleRuns(t *testing.T) {
	t.Run("non-blocked run is rejected", func(t *testing.T) {
		fixture := fabricatedCIPendingFixture(t, fixtureOptions{maxReworkRounds: 1})
		harness := newReconcileHarness(t, fixture)
		_, err := harness.reconcile(t)
		if err == nil || !strings.Contains(err.Error(), "cannot be reconciled") {
			t.Fatalf("expected CI_PENDING rejection, got %v", err)
		}
		if harness.mergeObserver.calls != 0 {
			t.Fatal("rejected reconcile must not observe the network")
		}
	})

	t.Run("rework requested run is rejected", func(t *testing.T) {
		fixture := fabricatedReworkRequestedFixture(t, fixtureOptions{maxReworkRounds: 1})
		harness := newReconcileHarness(t, fixture)
		if _, err := harness.reconcile(t); err == nil || !strings.Contains(err.Error(), "cannot be reconciled") {
			t.Fatalf("expected REWORK_REQUESTED rejection, got %v", err)
		}
	})

	t.Run("accepted run without record fails closed", func(t *testing.T) {
		fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
		harness := newReconcileHarness(t, fixture)
		if _, err := harness.reconcile(t); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(fixture.runDirectory, "publication-reconcile-records")); err != nil {
			t.Fatal(err)
		}
		_, err := harness.reconcile(t)
		if err == nil || !strings.Contains(err.Error(), "without a publication reconcile record") {
			t.Fatalf("expected record-less ACCEPTED rejection, got %v", err)
		}
	})

	t.Run("blocked before publication has no frozen record", func(t *testing.T) {
		fixture := newFabricatedRunFixture(t, fixtureOptions{maxReworkRounds: 1}, fabricatedBlockedBeforePublication)
		if state := fixture.inspect(t); state.State != domain.StateBlocked {
			t.Fatalf("state = %s", state.State)
		}
		harness := newReconcileHarness(t, fixture)
		_, err := harness.reconcile(t)
		if err == nil {
			t.Fatal("expected rejection for BLOCKED without a frozen PublicationRecord")
		}
		if harness.mergeObserver.calls != 0 {
			t.Fatal("ineligible reconcile must not observe the network")
		}
		if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
			t.Fatal("rejected reconcile must not persist a receipt")
		}
	})

	t.Run("unmerged PR must use accept", func(t *testing.T) {
		fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
		harness := newReconcileHarness(t, fixture)
		harness.mergeObserver.failWith = port.ErrPRNotMerged
		_, err := harness.reconcile(t)
		if err == nil || !strings.Contains(err.Error(), "not merged") {
			t.Fatalf("expected unmerged rejection, got %v", err)
		}
		if state := fixture.inspect(t); state.State != domain.StateBlocked {
			t.Fatalf("state = %+v", state)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
			t.Fatal("unmerged reconcile must not persist a receipt")
		}
	})

	t.Run("observer unavailable keeps blocked with zero writes", func(t *testing.T) {
		fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
		before := fixture.inspect(t)
		harness := newReconcileHarness(t, fixture)
		harness.mergeObserver.failWith = errors.New("simulated observer timeout")
		_, err := harness.reconcile(t)
		if err == nil || port.IsPermanent(err) {
			t.Fatalf("expected retryable observer failure, got %v", err)
		}
		after := fixture.inspect(t)
		if after.State != domain.StateBlocked || after.Sequence != before.Sequence {
			t.Fatalf("observer failure mutated the run: %+v", after)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
			t.Fatal("observer failure must not persist a receipt")
		}
		if _, statErr := os.Lstat(filepath.Join(fixture.runDirectory, "publication-reconcile-records")); !os.IsNotExist(statErr) {
			t.Fatal("observer failure must not create reconcile records")
		}
	})

	t.Run("merged head checks not green are rejected", func(t *testing.T) {
		for _, status := range []string{"fail", "pending", "external-failure"} {
			status := status
			t.Run(status, func(t *testing.T) {
				fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
				harness := newReconcileHarness(t, fixture)
				harness.checkObserver.status = status
				_, err := harness.reconcile(t)
				if err == nil || !strings.Contains(err.Error(), "not all green") && !strings.Contains(err.Error(), "invalid RemoteCheckRecord") {
					t.Fatalf("expected merged-head checks rejection, got %v", err)
				}
				if state := fixture.inspect(t); state.State != domain.StateBlocked {
					t.Fatalf("state = %+v", state)
				}
				if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "remote-check-record.json")); !os.IsNotExist(statErr) {
					t.Fatal("rejected checks must not materialize a check record")
				}
				if count := countJournalEvents(t, fixture, lifecycle.PublicationReconcileEventType); count != 0 {
					t.Fatal("rejected checks must not append a reconcile event")
				}
			})
		}
	})
}

func TestReconcileReceiptBindingNegatives(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.SCMMergeReceipt)
		want   string
	}{
		{"missing merge commit sha", func(r *domain.SCMMergeReceipt) { r.MergeCommitSha = "" }, "invalid SCMMergeReceipt"},
		{"merge method outside enumeration", func(r *domain.SCMMergeReceipt) { r.MergeMethod = "unknown" }, "invalid SCMMergeReceipt"},
		{"pr number below one", func(r *domain.SCMMergeReceipt) { r.PRNumber = 0 }, "invalid SCMMergeReceipt"},
		{"malformed head sha", func(r *domain.SCMMergeReceipt) { r.HeadOid = "zzz" }, "invalid SCMMergeReceipt"},
		{"head oid mismatch", func(r *domain.SCMMergeReceipt) { r.HeadOid = strings.Repeat("3", 40) }, "does not bind"},
		{"base oid mismatch", func(r *domain.SCMMergeReceipt) { r.BaseOid = strings.Repeat("4", 40) }, "does not bind"},
		{"cross-run receipt", func(r *domain.SCMMergeReceipt) { r.RunID = "run:foreign" }, "does not bind"},
		{"foreign publication record id", func(r *domain.SCMMergeReceipt) { r.PublicationRecordID = "sha256:" + strings.Repeat("9", 64) }, "does not bind"},
		{"tampered receipt digest", func(r *domain.SCMMergeReceipt) { r.ReceiptDigest = "sha256:" + strings.Repeat("f", 64) }, "digest"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
			harness := newReconcileHarness(t, fixture)
			harness.mergeObserver.build = func(publication domain.PublicationRecord, publicationDigest string) domain.Record {
				receipt := buildReceipt(publication, publicationDigest, strings.Repeat("7", 40))
				test.mutate(&receipt)
				if test.want != "invalid SCMMergeReceipt" && test.want != "digest" {
					digest, err := receipt.Digest()
					if err != nil {
						t.Fatal(err)
					}
					receipt.ReceiptDigest = digest
				}
				data, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				return domain.Record{Kind: domain.KindSCMMergeReceipt, Data: data}
			}
			_, err := harness.reconcile(t)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			if state := fixture.inspect(t); state.State != domain.StateBlocked {
				t.Fatalf("state = %+v", state)
			}
			if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
				t.Fatal("rejected receipt must not be persisted")
			}
		})
	}
}

func TestReconcileRejectsTamperedLedgerEvidence(t *testing.T) {
	t.Run("decision file differs from frozen digest", func(t *testing.T) {
		fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
		path := filepath.Join(fixture.runDirectory, "decisions", fmt.Sprintf("decision-%03d.json", fixture.inspect(t).ReviewRound))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var decision domain.ReviewDecision
		if err := json.Unmarshal(data, &decision); err != nil {
			t.Fatal(err)
		}
		decision.Summary = "tampered after block"
		data, err = json.Marshal(decision)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		harness := newReconcileHarness(t, fixture)
		_, err = harness.reconcile(t)
		if err == nil || !strings.Contains(err.Error(), "frozen lifecycle event") {
			t.Fatalf("expected frozen decision mismatch, got %v", err)
		}
		if state := fixture.inspect(t); state.State != domain.StateBlocked {
			t.Fatalf("state = %+v", state)
		}
		if harness.mergeObserver.calls != 0 {
			t.Fatal("ledger recheck failure must not observe the network")
		}
	})

	t.Run("decision verdict no longer authorizes publication", func(t *testing.T) {
		fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
		round := fixture.inspect(t).ReviewRound
		path := filepath.Join(fixture.runDirectory, "decisions", fmt.Sprintf("decision-%03d.json", round))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var decision domain.ReviewDecision
		if err := json.Unmarshal(data, &decision); err != nil {
			t.Fatal(err)
		}
		decision.Verdict = "rework"
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
		digest, err := canonical.DigestJSON(data)
		if err != nil {
			t.Fatal(err)
		}
		// Re-freeze the journal to the tampered decision digest so the
		// rejection comes from the verdict gate itself: reconcile must never
		// bypass the ReviewDecision.
		mutateJournalPayload(t, fixture, "review.accept", "decisionDigest", digest)
		harness := newReconcileHarness(t, fixture)
		_, err = harness.reconcile(t)
		if err == nil || !strings.Contains(err.Error(), "does not authorize publication") {
			t.Fatalf("expected verdict rejection, got %v", err)
		}
		if state := fixture.inspect(t); state.State != domain.StateBlocked {
			t.Fatalf("state = %+v", state)
		}
	})
}

// mutateJournalPayload rewrites one payload field of the first journal event
// with the given type. The raw journal is authoritative for frozen evidence.
func mutateJournalPayload(t *testing.T, fixture *publicationFixture, eventType, key, value string) {
	t.Helper()
	path := filepath.Join(fixture.runDirectory, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for index, line := range lines {
		var event domain.RunEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != eventType {
			continue
		}
		event.Payload[key] = value
		mutated, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines[index] = string(mutated)
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("journal has no event with type %q", eventType)
}

func TestReconcileLedgerCASConflictRollsBack(t *testing.T) {
	fixture := blockedPostPublicationFixture(t, fixtureOptions{maxReworkRounds: 1})
	harness := newReconcileHarness(t, fixture)
	store := runstore.New(fixture.stateRoot)
	lease, err := store.Acquire(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	state, err := store.Inspect(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	// A concurrent ledger writer appends a valid repair audit event after the
	// reconcile read its state. The reconcile's append must hit the
	// expectedSequence CAS and roll the whole transition back.
	repairEvent := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
		EventID: "event:cas-repair", RunID: fixture.runID, Sequence: state.Sequence + 1,
		Type: lifecycle.RepairAuditEventType, StateFrom: domain.StateBlocked, StateTo: domain.StateBlocked,
		Timestamp: harness.now, Actor: &domain.Actor{Type: "system", ID: "marshal-reconciliation"},
		Payload: map[string]any{"repairKind": "snapshot-rebuild", "sourceJournalSequence": state.Sequence},
	}
	if err := store.Append(lease, repairEvent, state.Sequence); err != nil {
		t.Fatal(err)
	}
	reconcileEvent := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
		EventID: "event:cas-reconcile", RunID: fixture.runID, Sequence: state.Sequence + 1,
		Type: lifecycle.PublicationReconcileEventType, StateFrom: domain.StateBlocked, StateTo: domain.StateAccepted,
		Timestamp: harness.now, Actor: &domain.Actor{Type: "system", ID: "marshal-reconciliation"},
		Payload: map[string]any{"receiptDigest": "sha256:" + strings.Repeat("c", 64), "reconcileId": "reconcile:" + strings.Repeat("1", 64), "publicationDigest": "sha256:" + strings.Repeat("b", 64), "decisionDigest": "sha256:" + strings.Repeat("d", 64), "terminalReason": ReconcileTerminalReason},
	}
	if err := store.Append(lease, reconcileEvent, state.Sequence); !errors.Is(err, runstore.ErrConflict) {
		t.Fatalf("expected CAS conflict for stale expectedSequence, got %v", err)
	}
	if count := countJournalEvents(t, fixture, lifecycle.PublicationReconcileEventType); count != 0 {
		t.Fatal("CAS conflict must leave no reconcile event in the journal")
	}
}

// ADR 0028 deadline-block recovery (Issue #30): recovery after a deadline
// misfire only walks the append-only typed reconciliation, never a direct
// RunState mutation; the trusted-completion precondition admits an in-window
// re-observation and fails closed on late or unproven green lights.

// TestReconcileRecoversDeadlineBlockedRunOnTimelyCompletion is the Issue #30
// recovery positive: a run terminally BLOCKED by the deadline adjudication
// whose PR a maintainer merged with all required checks green on time is
// migrated to ACCEPTED by the typed reconciliation, recording the
// ci-deadline-reconciled reason code.
func TestReconcileRecoversDeadlineBlockedRunOnTimelyCompletion(t *testing.T) {
	createdAt, publishedAt, legacyDeadline, ciDeadline := deadlineFixtureInstants()
	fixture := newCIDeadlineFixture(t, deadlineFixtureConfig{createdAt: createdAt, publishedAt: publishedAt, runTimeout: 3600, blockError: errCIDeadlineExceeded.Error()})
	_, publicationDigest := fixturePublicationBytes(t, fixture)
	harness := newReconcileHarness(t, fixture)
	harness.now = legacyDeadline.Add(30 * time.Minute)
	if !harness.now.After(legacyDeadline) || !harness.now.Before(ciDeadline) {
		t.Fatalf("reconcile instant %s must sit between the legacy deadline %s and the frozen ciDeadline %s", harness.now, legacyDeadline, ciDeadline)
	}

	result, err := harness.reconcile(t)
	if err != nil {
		t.Fatalf("deadline-blocked reconcile failed: %v", err)
	}
	if result.State.State != domain.StateAccepted || result.State.TerminalReason != ReconcileTerminalReason {
		t.Fatalf("state = %+v", result.State)
	}
	if result.State.Publication == nil || result.State.Publication.HeadSHA == "" {
		t.Fatalf("publication snapshot lost on reconcile: %+v", result.State.Publication)
	}

	receipt := readPersistedReceipt(t, fixture)
	if receipt.RunID != fixture.runID || receipt.PublicationRecordID != publicationDigest || receipt.ReceiptDigest != result.Receipt.ReceiptDigest {
		t.Fatalf("receipt binding = %+v", receipt)
	}

	records := readReconcileRecords(t, fixture)
	if len(records) != 1 {
		t.Fatalf("reconcile records = %d, want 1", len(records))
	}
	record := records[0]
	if record.ReconcileType != ReconcileTypeAcceptAfterMerge || record.ObservedState != domain.StateBlocked || record.DecidedState != domain.StateAccepted {
		t.Fatalf("record enumerations = %+v", record)
	}
	if record.ReconcileReason != ReconcileReasonCIDeadlineReconciled {
		t.Fatalf("record reconcileReason = %q, want %q for a deadline recovery", record.ReconcileReason, ReconcileReasonCIDeadlineReconciled)
	}
	if record.SCMMergeReceiptID != receipt.ReceiptID || record.ReconciledBy != "maintainer" {
		t.Fatalf("record identity = %+v", record)
	}
	if len(record.EvidenceDigests) != 4 || record.EvidenceDigests[0] != publicationDigest || record.EvidenceDigests[2] != receipt.ReceiptDigest {
		t.Fatalf("evidence digests = %+v", record.EvidenceDigests)
	}
	checkData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "remote-check-record.json"))
	if err != nil {
		t.Fatalf("re-observed check record missing: %v", err)
	}
	if record.EvidenceDigests[3] != canonical.DigestBytes(mustCanonical(t, checkData)) {
		t.Fatal("record does not bind the re-observed RemoteCheckRecord digest")
	}

	event := lastJournalEvent(t, fixture)
	if event.Type != lifecycle.PublicationReconcileEventType || event.StateFrom != domain.StateBlocked || event.StateTo != domain.StateAccepted {
		t.Fatalf("reconcile event = %+v", event)
	}
	if event.Actor == nil || event.Actor.Type != "system" || event.Actor.ID != "marshal-reconciliation" {
		t.Fatalf("reconcile event actor = %+v", event.Actor)
	}

	archives, err := filepath.Glob(filepath.Join(fixture.runDirectory, "outcomes", "blocked-*", "outcome.json"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("blocked outcome archive = %v (err=%v)", archives, err)
	}
	outcomeData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "outcome.json"))
	if err != nil {
		t.Fatalf("accepted outcome missing: %v", err)
	}
	var outcome domain.OutcomeBundle
	if err := json.Unmarshal(outcomeData, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != domain.StateAccepted || outcome.Verdict != "accept" {
		t.Fatalf("accepted outcome = %+v", outcome)
	}

	// The frozen PublicationRecord is never rewritten by the recovery.
	_, afterDigest := fixturePublicationBytes(t, fixture)
	if afterDigest != publicationDigest {
		t.Fatalf("PublicationRecord rewritten by reconcile: %s vs %s", afterDigest, publicationDigest)
	}
}

// TestReconcileDeadlineRecoveryIsIdempotent covers the repeated-reconcile
// matrix cell: the recovery merges offline and idempotently — no second
// record, no second event, no second network observation.
func TestReconcileDeadlineRecoveryIsIdempotent(t *testing.T) {
	createdAt, publishedAt, legacyDeadline, _ := deadlineFixtureInstants()
	fixture := newCIDeadlineFixture(t, deadlineFixtureConfig{createdAt: createdAt, publishedAt: publishedAt, runTimeout: 3600, blockError: errCIDeadlineExceeded.Error()})
	harness := newReconcileHarness(t, fixture)
	harness.now = legacyDeadline.Add(30 * time.Minute)
	first, err := harness.reconcile(t)
	if err != nil {
		t.Fatal(err)
	}
	second, err := harness.reconcile(t)
	if err != nil {
		t.Fatalf("offline idempotent replay failed: %v", err)
	}
	if second.Record.ReconcileID != first.Record.ReconcileID {
		t.Fatalf("second replay produced a different record: %s vs %s", second.Record.ReconcileID, first.Record.ReconcileID)
	}
	if second.State.State != domain.StateAccepted {
		t.Fatalf("replayed state = %+v", second.State)
	}
	if count := countJournalEvents(t, fixture, lifecycle.PublicationReconcileEventType); count != 1 {
		t.Fatalf("reconcile events = %d, want exactly 1", count)
	}
	if len(readReconcileRecords(t, fixture)) != 1 {
		t.Fatal("idempotent replay duplicated the reconcile record")
	}
	if harness.mergeObserver.calls != 1 {
		t.Fatalf("offline replay must not observe the network again: %d calls", harness.mergeObserver.calls)
	}
}

// TestReconcileDeadlineRecoveryAfterCIDeadlineFailsClosed covers the
// missing-completion-time matrix cell at the reconcile boundary: a
// re-observation at or after the frozen ciDeadline carries no trusted
// completedAt proof, so the recovery fails closed with the run kept in
// BLOCKED and zero writes.
func TestReconcileDeadlineRecoveryAfterCIDeadlineFailsClosed(t *testing.T) {
	createdAt, publishedAt, _, ciDeadline := deadlineFixtureInstants()
	fixture := newCIDeadlineFixture(t, deadlineFixtureConfig{createdAt: createdAt, publishedAt: publishedAt, runTimeout: 3600, blockError: errCIDeadlineExceeded.Error()})
	before := fixture.inspect(t)
	harness := newReconcileHarness(t, fixture)
	harness.now = ciDeadline.Add(10 * time.Minute)
	harness.checkObserver.mutate = func(checks *domain.RemoteCheckRecord) {
		checks.Checks[0].CompletedAt = nil
	}
	_, err := harness.reconcile(t)
	if !errors.Is(err, errCICompletedAtMissing) {
		t.Fatalf("err = %v, want ci-completed-at-missing fail closed", err)
	}
	after := fixture.inspect(t)
	if after.State != domain.StateBlocked || after.Sequence != before.Sequence {
		t.Fatalf("failed recovery mutated the run: %+v", after)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
		t.Fatal("rejected recovery must not persist a receipt")
	}
	if _, statErr := os.Lstat(filepath.Join(fixture.runDirectory, "publication-reconcile-records")); !os.IsNotExist(statErr) {
		t.Fatal("rejected recovery must not create reconcile records")
	}
	if count := countJournalEvents(t, fixture, lifecycle.PublicationReconcileEventType); count != 0 {
		t.Fatalf("rejected recovery appended %d reconcile events", count)
	}
}

// TestReconcileDeadlineRecoveryRejectsCheckIdentityDrift covers the
// head/identity matrix cell at the reconcile boundary: green checks from a
// stale head never authorize the recovery.
func TestReconcileDeadlineRecoveryRejectsCheckIdentityDrift(t *testing.T) {
	createdAt, publishedAt, legacyDeadline, _ := deadlineFixtureInstants()
	fixture := newCIDeadlineFixture(t, deadlineFixtureConfig{createdAt: createdAt, publishedAt: publishedAt, runTimeout: 3600, blockError: errCIDeadlineExceeded.Error()})
	harness := newReconcileHarness(t, fixture)
	harness.now = legacyDeadline.Add(30 * time.Minute)
	harness.checkObserver.mutate = func(checks *domain.RemoteCheckRecord) {
		checks.HeadSHA = fabricatedSHA("3")
	}
	_, err := harness.reconcile(t)
	if err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("err = %v, want identity mismatch diagnosis", err)
	}
	if state := fixture.inspect(t); state.State != domain.StateBlocked {
		t.Fatalf("identity drift must keep the run BLOCKED: %+v", state)
	}
	if count := countJournalEvents(t, fixture, lifecycle.PublicationReconcileEventType); count != 0 {
		t.Fatal("identity drift must not append a reconcile event")
	}
}

// TestReconcileDeadlineRecoveryObserverUnavailableFailsClosed covers the
// observer-unavailable matrix cell for a deadline-blocked run: no acceptance,
// no receipt, no record, no event.
func TestReconcileDeadlineRecoveryObserverUnavailableFailsClosed(t *testing.T) {
	createdAt, publishedAt, legacyDeadline, _ := deadlineFixtureInstants()
	fixture := newCIDeadlineFixture(t, deadlineFixtureConfig{createdAt: createdAt, publishedAt: publishedAt, runTimeout: 3600, blockError: errCIDeadlineExceeded.Error()})
	before := fixture.inspect(t)
	harness := newReconcileHarness(t, fixture)
	harness.now = legacyDeadline.Add(30 * time.Minute)
	harness.checkObserver.failWith = errors.New("simulated observer outage")
	_, err := harness.reconcile(t)
	if err == nil || port.IsPermanent(err) {
		t.Fatalf("expected the retryable observer outage to surface, got %v", err)
	}
	after := fixture.inspect(t)
	if after.State != domain.StateBlocked || after.Sequence != before.Sequence {
		t.Fatalf("observer outage mutated the run: %+v", after)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
		t.Fatal("observer outage must not persist a receipt")
	}
	if _, statErr := os.Lstat(filepath.Join(fixture.runDirectory, "publication-reconcile-records")); !os.IsNotExist(statErr) {
		t.Fatal("observer outage must not create reconcile records")
	}
	if count := countJournalEvents(t, fixture, lifecycle.PublicationReconcileEventType); count != 0 {
		t.Fatalf("observer outage appended %d reconcile events", count)
	}
}
