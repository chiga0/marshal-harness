package publication

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// mergeFixture fabricates a CI_PENDING policy-merge lineage (task, policy,
// decision, publication, human publish approval, journal and snapshot)
// without any git repository or remote side effect.
type mergeFixture struct {
	t                    *testing.T
	stateRoot            string
	runDirectory         string
	taskID               string
	runID                string
	baseSHA              string
	headSHA              string
	specDigest           string
	policyDigest         string
	decisionDigest       string
	evidenceDigest       string
	verificationDigest   string
	capabilityDigest     string
	authorityNamespaceID string
	validator            *contract.Validator
	store                *runstore.Store
}

func newMergeFixture(t *testing.T) *mergeFixture {
	t.Helper()
	t.Setenv("MARSHAL_STATE_DIR", "")
	root := t.TempDir()
	stateRoot := filepath.Join(root, ".marshal")
	const taskID = "TASK-MERGE"
	const runID = "run:task-merge"
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
	verificationDigest := fabricatedDigest("1")
	evidenceDigest := fabricatedDigest("e")
	capabilityDigest := fabricatedDigest("8")
	headBranch := deriveBranch(taskID, runID)
	publishedAt := time.Date(2026, 8, 4, 12, 15, 0, 0, time.UTC)

	repoIdentity, err := json.Marshal(map[string]string{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": "RepositoryIdentity", "repositoryRoot": root,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(stateRoot, "repo.json"), append(repoIdentity, '\n'))

	task := domain.TaskSpec{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindTask,
		Metadata:   domain.TaskMetadata{ID: taskID, Title: "Controlled merge"},
		Repository: domain.TaskRepository{Path: root, BaseRef: baseSHA, Remote: "origin", ExpectedRemoteURL: "https://github.com/marshal-test/task-repo.git"},
		Work:       domain.TaskWork{Objective: "Merge a controlled commit.", Constraints: []string{}, NonGoals: []string{}},
		Scope:      domain.TaskScope{AllowPaths: []string{"**"}, DenyPaths: []string{}, AllowSubmodules: false, MaxChangedFiles: 10, MaxDiffBytes: 100000},
		Acceptance: domain.TaskAcceptance{Commands: []domain.TaskCommand{{ID: "ci-test", Argv: []string{"go", "test"}, CWD: ".", TimeoutSeconds: 60, Required: true, BaselinePolicy: "none", MaxLogBytes: 100000}}, AllowNoChange: false},
		Deliverables: []domain.TaskDeliverable{
			{ID: "source", Kind: "code", Required: true, PathGlob: "src/*.go", MediaType: "text/x-go", MinimumCount: 1},
			{ID: "pull-request", Kind: "publication", Required: true, PathGlob: "docs/*.md", MediaType: "text/markdown", MinimumCount: 1},
		},
		Worker:  domain.TaskWorker{PreferredAdapter: "fake", FallbackAdapters: []string{}, ExecutionProfile: "workspace-write", SessionPolicy: "ephemeral"},
		Budgets: domain.TaskBudgets{RunTimeoutSeconds: 3600, AttemptTimeoutSeconds: 60, MaxAttempts: 3, MaxOperationalRetries: 1, MaxReworkRounds: 1, MaxOutputBytes: 100000},
		Publication: domain.TaskPublication{
			Required: true, Provider: "github", Mode: "draft", Remote: "origin",
			BaseBranch: "main", MergePolicy: "policy", MergeMethod: "squash", RequiredChecks: []string{"ci/test"},
		},
	}
	taskData, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindTask, taskData); err != nil {
		t.Fatalf("merge fixture TaskSpec failed schema validation: %v", err)
	}
	specDigest, err := canonical.DigestJSON(taskData)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(runDirectory, "task-spec.json"), taskData)

	policy := fixturePolicy{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindPolicySnapshot,
		TaskID: taskID, RunID: runID,
		Sources: []fixturePolicySource{{Scope: "builtin", Digest: "sha256:" + strings.Repeat("b", 64), Required: true}},
		Effective: fixturePolicyEffective{
			MinimumExecutionProfile: "workspace-write", RequireEnforcedNetworkPolicy: false, NetworkPolicy: "unenforced",
			AllowFallbackWorkers: true, AllowWorkerSubagents: false, AllowPublication: true, AllowMerge: true,
			AllowGateWaivers: false, AllowedAdapters: []string{"fake"}, EnvironmentAllowlist: []string{"PATH", "LANG", "TMPDIR"}, RetentionDays: 30,
		},
		PolicyDigest: "sha256:" + strings.Repeat("c", 64),
		GeneratedAt:  now,
	}
	policyData, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindPolicySnapshot, policyData); err != nil {
		t.Fatalf("merge fixture PolicySnapshot failed schema validation: %v", err)
	}
	policyDigest, err := canonical.DigestJSON(policyData)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(filepath.Join(runDirectory, "policy-snapshot.json"), policyData)

	decision := domain.ReviewDecision{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision,
		TaskID: taskID, RunID: runID, ReviewRound: 1,
		Reviewer:   domain.Reviewer{Type: "lead-agent", ID: "merge-integration"},
		SpecDigest: specDigest, ReviewPacketDigest: fabricatedDigest("a"),
		VerificationDigest: verificationDigest, ArtifactManifestDigest: fabricatedDigest("9"),
		EvidenceDigest: evidenceDigest, Verdict: "accept", Summary: "accept, publish and merge",
		BlockingFindings: []domain.Finding{}, NonBlockingFindings: []domain.Finding{},
		PublicationRecommendation: "publish", MergeRecommendation: "eligible-after-policy",
		DecidedAt: time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	}
	decisionData, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindReviewDecision, decisionData); err != nil {
		t.Fatalf("merge fixture ReviewDecision failed schema validation: %v", err)
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
		SpecDigest: specDigest, PolicyDigest: policyDigest,
		EvidenceDigest: evidenceDigest, VerificationDigest: verificationDigest,
		ReviewDecisionDigest: decisionDigest,
		Marker:               marker(taskID, runID), Mode: "draft", MergePolicy: "policy",
		Request: domain.PullRequestRecord{ID: "PR_marshaltest0001", Number: 7, URL: "https://github.com/marshal-test/task-repo/pull/7", Draft: true, State: "OPEN"},
		Actor:   "marshal-fake-publisher", PublishedAt: publishedAt, UpdatedAt: publishedAt,
	}
	publicationData, err := json.Marshal(publication)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator.Validate(domain.KindPublicationRecord, publicationData); err != nil {
		t.Fatalf("merge fixture PublicationRecord failed schema validation: %v", err)
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
	type mergeTransition struct {
		from, to  domain.State
		eventType string
		attemptID string
		actor     *domain.Actor
		payload   map[string]any
	}
	publisherActor := &domain.Actor{Type: "publisher", ID: "marshal-github-publisher"}
	transitions := []mergeTransition{
		{domain.StateCreated, domain.StatePlanned, "task.planned", "", nil, map[string]any{"fixture": true}},
		{domain.StatePlanned, domain.StateReady, "task.ready", "", nil, map[string]any{"fixture": true}},
		{domain.StateReady, domain.StateRunning, "worker.started", "attempt:1", &domain.Actor{Type: "system", ID: "marshal-worker-runner"}, map[string]any{"fixture": true}},
		{domain.StateRunning, domain.StateVerifying, "worker.completed", "", &domain.Actor{Type: "system", ID: "marshal-worker-runner"}, map[string]any{"fixture": true}},
		{domain.StateVerifying, domain.StateReviewPending, "verification.completed", "", &domain.Actor{Type: "system", ID: "marshal-verifier"}, map[string]any{"reportDigest": verificationDigest, "artifactManifestDigest": fabricatedDigest("9"), "status": "pass"}},
		{domain.StateReviewPending, domain.StatePublishing, "review.accept", "", &domain.Actor{Type: "system", ID: "marshal-review"}, map[string]any{"verdict": "accept", "decisionDigest": decisionDigest, "evidenceDigest": evidenceDigest}},
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
	// Snapshot the PUBLISHING state and record the human publish approval
	// bound to it before the publication/CI tail advances the sequence.
	publishingState := domain.NewRunState(taskID, runID, now)
	publishingState.State = domain.StatePublishing
	publishingState.Sequence = uint64(len(transitions))
	publishingState.SpecDigest = specDigest
	publishingState.PolicyDigest = policyDigest
	publishingState.CapabilityDigest = capabilityDigest
	publishingState.BaseSHA = baseSHA
	publishingState.AttemptsUsed, publishingState.ReviewRound, publishingState.CurrentAttemptID = 1, 1, "attempt:1"
	publishingState.UpdatedAt = now
	if err := store.WriteSnapshot(lease, publishingState); err != nil {
		t.Fatal(err)
	}
	approval := domain.ApprovalRecord{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindApprovalRecord,
		RecordID: "approval-merge", TaskID: taskID, RunID: runID, ControlSequence: 1,
		Gate: domain.ApprovalGatePublish, Source: domain.ControlSource{Type: domain.ControlSourceTypeHuman, ID: "maintainer"},
		Binding: domain.ApprovalBinding{
			StateSequence: publishingState.Sequence, SpecDigest: specDigest, PolicyDigest: policyDigest,
			CapabilityDigest: capabilityDigest, BaseSHA: baseSHA, ReviewRound: 1,
			DecisionDigest: decisionDigest, EvidenceDigest: evidenceDigest,
		},
		Outcome: domain.ApprovalOutcomeApproved, CreatedAt: now,
	}
	if err := store.AppendApproval(lease, validator, approval); err != nil {
		t.Fatal(err)
	}
	// Advance to CI_PENDING.
	tail := []mergeTransition{
		{domain.StatePublishing, domain.StatePublished, "publication.completed", "", publisherActor, map[string]any{
			"publicationDigest": publicationDigest, "provider": "github", "repository": "marshal-test/task-repo",
			"headBranch": headBranch, "baseBranch": "main", "externalId": "PR_marshaltest0001",
			"headSha": headSHA, "uri": "https://github.com/marshal-test/task-repo/pull/7",
		}},
		{domain.StatePublished, domain.StateCIPending, "publication.checks-requested", "", publisherActor, map[string]any{"requiredChecks": []any{"ci/test"}}},
	}
	for index, transition := range tail {
		event := domain.RunEvent{
			APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent,
			EventID: fmt.Sprintf("event:%s:%d", strings.ToLower(runID), len(transitions)+index+1), RunID: runID,
			Sequence: uint64(len(transitions) + index + 1), Type: transition.eventType,
			StateFrom: transition.from, StateTo: transition.to, Timestamp: now,
			Actor: transition.actor, Payload: transition.payload,
		}
		if err := store.Append(lease, event, uint64(len(transitions)+index)); err != nil {
			t.Fatal(err)
		}
	}
	state := publishingState
	state.State = domain.StateCIPending
	state.Sequence = uint64(len(transitions) + len(tail))
	state.Publication = &domain.RunPublication{Provider: "github", Repository: "marshal-test/task-repo", HeadBranch: headBranch, BaseBranch: "main", ExternalID: "PR_marshaltest0001", URI: "https://github.com/marshal-test/task-repo/pull/7", HeadSHA: headSHA}
	state.UpdatedAt = now
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Inspect(runID); err != nil {
		t.Fatalf("merge fixture lineage is inconsistent: %v", err)
	}
	authorityNamespaceID, err := reconcileAuthorityNamespaceID(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	return &mergeFixture{
		t: t, stateRoot: stateRoot, runDirectory: runDirectory, taskID: taskID, runID: runID,
		baseSHA: baseSHA, headSHA: headSHA, specDigest: specDigest, policyDigest: policyDigest,
		decisionDigest: decisionDigest, evidenceDigest: evidenceDigest, verificationDigest: verificationDigest,
		capabilityDigest: capabilityDigest, authorityNamespaceID: authorityNamespaceID,
		validator: validator, store: store,
	}
}

// ---- fake merge ports ----

type fakeSCMMerger struct {
	mu               sync.Mutex
	bindsHead        bool
	securityDomainID string
	readyErr         error
	mergeErr         error
	readyCalls       int
	mergeCalls       int
	readyIntents     []domain.SCMMergeIntent
	mergeIntents     []domain.SCMMergeIntent
}

var _ port.SCMMerger = (*fakeSCMMerger)(nil)

func (m *fakeSCMMerger) ReadyForReview(_ context.Context, intent domain.SCMMergeIntent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.readyCalls++
	m.readyIntents = append(m.readyIntents, intent)
	return m.readyErr
}

func (m *fakeSCMMerger) Merge(_ context.Context, intent domain.SCMMergeIntent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mergeCalls++
	m.mergeIntents = append(m.mergeIntents, intent)
	return m.mergeErr
}

func (m *fakeSCMMerger) BindsExpectedHead() bool { return m.bindsHead }

func (m *fakeSCMMerger) SecurityDomainID() string { return m.securityDomainID }

type fakeTargetObserver struct {
	mu     sync.Mutex
	target domain.SCMMergeTarget
	err    error
	calls  int
	seq    []domain.SCMMergeTarget
}

var _ port.MergeTargetObserver = (*fakeTargetObserver)(nil)

func (o *fakeTargetObserver) ObserveTarget(_ context.Context, _ domain.SCMMergeIntent) (domain.SCMMergeTarget, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if o.err != nil {
		return domain.SCMMergeTarget{}, o.err
	}
	if o.calls <= len(o.seq) {
		return o.seq[o.calls-1], nil
	}
	return o.target, nil
}

type fakeCredentialObserver struct {
	principal string
	digest    string
	err       error
	calls     int
}

var _ port.SCMMergeCredentialObserver = (*fakeCredentialObserver)(nil)

func (o *fakeCredentialObserver) ObserveCredentialIdentity(_ context.Context) (string, string, error) {
	o.calls++
	if o.err != nil {
		return "", "", o.err
	}
	return o.principal, o.digest, nil
}

// mergeReceiptObserver derives a receipt bound to the expected intent fields,
// so the ADR 0032 §5 binding table passes for the happy path and each field
// can be mutated per test.
type mergeReceiptObserver struct {
	mu                   sync.Mutex
	authorityNamespaceID string
	mergedBy             string
	method               string
	mergeCommitSHA       string
	failWith             error
	calls                int
}

var _ port.MergeReceiptObserver = (*mergeReceiptObserver)(nil)

func (o *mergeReceiptObserver) ObserveMergeReceipt(_ context.Context, record domain.Record) (domain.Record, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if o.failWith != nil {
		return domain.Record{}, o.failWith
	}
	var publication domain.PublicationRecord
	if err := json.Unmarshal(record.Data, &publication); err != nil {
		return domain.Record{}, err
	}
	digest, err := canonical.DigestJSON(record.Data)
	if err != nil {
		return domain.Record{}, err
	}
	mergeCommitSHA := o.mergeCommitSHA
	if mergeCommitSHA == "" {
		mergeCommitSHA = fabricatedSHA("4")
	}
	receipt := domain.SCMMergeReceipt{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindSCMMergeReceipt,
		ReceiptID: "receipt-" + strings.Repeat("7", 64), AuthorityNamespaceID: o.authorityNamespaceID,
		RunID: publication.RunID, PublicationRecordID: digest, RepositoryRef: publication.Repository.NameWithOwner,
		PRNumber: publication.Request.Number, HeadOid: publication.HeadSHA, BaseOid: publication.BaseSHA,
		MergeCommitSha: mergeCommitSHA, MergedAt: time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC),
		MergedBy: o.mergedBy, MergeMethod: o.method, CapturedAt: time.Date(2026, 8, 12, 10, 5, 0, 0, time.UTC),
	}
	receiptDigest, err := receipt.Digest()
	if err != nil {
		return domain.Record{}, err
	}
	receipt.ReceiptDigest = receiptDigest
	data, err := json.Marshal(receipt)
	if err != nil {
		return domain.Record{}, err
	}
	return domain.Record{Kind: domain.KindSCMMergeReceipt, Data: data}, nil
}

// mergeHarness wires the fake ports for a controlled merge test.
type mergeHarness struct {
	fixture             *mergeFixture
	merger              *fakeSCMMerger
	targetObserver      *fakeTargetObserver
	credentialObserver  *fakeCredentialObserver
	receiptObserver     *mergeReceiptObserver
	checkObserver       *fakeObserver
	now                 time.Time
	requestedBy         string
	authorization       *authority.EdgeRuntime
	authorizationSource authority.SecurityDomainId
	authorizationTarget authority.SecurityDomainId
	eligibility         *mergeTargetEligibility
}

type mergeTargetEligibility struct{ eligible bool }

func (resolver *mergeTargetEligibility) TargetEligible(authority.SecurityDomainId) (bool, error) {
	return resolver.eligible, nil
}

func newMergeHarness(t *testing.T, fixture *mergeFixture) *mergeHarness {
	t.Helper()
	namespace := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: filepath.Dir(fixture.stateRoot)}
	runtime, err := authority.NewEdgeRuntime(namespace)
	if err != nil {
		t.Fatal(err)
	}
	eligibility := &mergeTargetEligibility{eligible: true}
	runtime.BindTargetEligibilityResolver(eligibility)
	source := authority.SecurityDomainId{TenantNamespace: "local", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "merge-controller"}
	target := authority.SecurityDomainId{TenantNamespace: "local", TrustDomainKind: authority.TrustDomainKindPublication, IsolationDomainId: "scm-merger"}
	targetDigest, err := target.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return &mergeHarness{
		fixture:             fixture,
		merger:              &fakeSCMMerger{bindsHead: true, securityDomainID: targetDigest},
		targetObserver:      &fakeTargetObserver{target: mergeTargetFor(fixture)},
		credentialObserver:  &fakeCredentialObserver{principal: "github-login:maintainer", digest: fabricatedDigest("4")},
		receiptObserver:     &mergeReceiptObserver{authorityNamespaceID: fixture.authorityNamespaceID, mergedBy: "maintainer", method: domain.MergeMethodSquash},
		checkObserver:       &fakeObserver{status: "pass"},
		now:                 time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC),
		requestedBy:         "maintainer",
		authorization:       runtime,
		authorizationSource: source,
		authorizationTarget: target,
		eligibility:         eligibility,
	}
}

func mergeTargetFor(fixture *mergeFixture) domain.SCMMergeTarget {
	return domain.SCMMergeTarget{
		Repository: "marshal-test/task-repo", PRNumber: 7, HeadOid: fixture.headSHA, BaseOid: fixture.baseSHA,
		BaseBranch: "main",
		Draft:      true, State: domain.MergeTargetStateOpen, MarkerPresent: true,
	}
}

func (h *mergeHarness) merge(t *testing.T) (MergeResult, error) {
	t.Helper()
	return Merge(context.Background(), MergeInput{
		StateRoot: h.fixture.stateRoot, RunID: h.fixture.runID,
		Merger: h.merger, TargetObserver: h.targetObserver, CredentialObserver: h.credentialObserver,
		ReceiptObserver: h.receiptObserver, CheckObserver: h.checkObserver,
		AuthorizationRuntime: h.authorization, AuthorizationSource: h.authorizationSource, AuthorizationTarget: h.authorizationTarget,
		Validator: h.fixture.validator, RequestedBy: h.requestedBy, Now: h.now,
	})
}

func (h *mergeHarness) inspect(t *testing.T) domain.RunState {
	t.Helper()
	state, err := h.fixture.store.Inspect(h.fixture.runID)
	if err != nil {
		t.Fatalf("inspect run state: %v", err)
	}
	return state
}

func readPersistedIntent(t *testing.T, fixture *mergeFixture) domain.SCMMergeIntent {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(fixture.runDirectory, "merge-intents"))
	if err != nil {
		t.Fatalf("merge-intents missing: %v", err)
	}
	var intent domain.SCMMergeIntent
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixture.runDirectory, "merge-intents", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.validator.Validate(domain.KindSCMMergeIntent, data); err != nil {
			t.Fatalf("merge intent failed schema validation: %v", err)
		}
		if err := json.Unmarshal(data, &intent); err != nil {
			t.Fatal(err)
		}
		if err := intent.Validate(); err != nil {
			t.Fatalf("merge intent failed validation: %v", err)
		}
		return intent
	}
	t.Fatal("no merge intent persisted")
	return domain.SCMMergeIntent{}
}

func TestMergeHappyPathConvergesToAccepted(t *testing.T) {
	fixture := newMergeFixture(t)
	harness := newMergeHarness(t, fixture)

	result, err := harness.merge(t)
	if err != nil {
		t.Fatalf("Merge() failed: %v", err)
	}
	if result.State.State != domain.StateAccepted {
		t.Fatalf("state = %s, want ACCEPTED", result.State.State)
	}
	if result.State.Publication == nil || result.State.Publication.HeadSHA != fixture.headSHA {
		t.Fatalf("publication snapshot lost: %+v", result.State.Publication)
	}

	// Intent persisted and bound to the frozen evidence/authorization digests.
	intent := readPersistedIntent(t, fixture)
	if intent.IntentID != result.Intent.IntentID || intent.IntentDigest != result.Intent.IntentDigest {
		t.Fatalf("intent identity = %+v, result = %+v", intent, result.Intent)
	}
	if intent.TaskID != fixture.taskID || intent.RunID != fixture.runID || intent.BaseOid != fixture.baseSHA || intent.HeadOid != fixture.headSHA {
		t.Fatalf("intent binding = %+v", intent)
	}
	if intent.PublicationRecordID != intent.PublicationDigest || intent.ExpectedMergedBy != "github-login:maintainer" {
		t.Fatalf("intent dual-identity or executor = %+v", intent)
	}
	if intent.MergeMethod != domain.MergeMethodSquash || intent.PRNumber != 7 {
		t.Fatalf("intent merge fields = %+v", intent)
	}

	// Receipt persisted and bound to the intent.
	receipt := readPersistedReceiptFromRun(t, fixture.runDirectory, fixture.validator)
	if receipt.ReceiptID != result.Receipt.ReceiptID || receipt.ReceiptDigest != result.Receipt.ReceiptDigest {
		t.Fatalf("receipt = %+v, result = %+v", receipt, result.Receipt)
	}
	if receipt.AuthorityNamespaceID != fixture.authorityNamespaceID || receipt.MergedBy != "maintainer" || receipt.MergeMethod != domain.MergeMethodSquash {
		t.Fatalf("receipt binding = %+v", receipt)
	}

	// Both mutations ran once with the bound intent, in order.
	harness.merger.mu.Lock()
	readyCalls, mergeCalls := harness.merger.readyCalls, harness.merger.mergeCalls
	harness.merger.mu.Unlock()
	if readyCalls != 1 || mergeCalls != 1 {
		t.Fatalf("ready=%d merge=%d, want 1 each", readyCalls, mergeCalls)
	}

	// publication.merged event carries the fixed actor and closed payload.
	events, _, err := fixture.store.ReadEvents(fixture.runID)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Type != "publication.merged" || last.StateFrom != domain.StateCIPending || last.StateTo != domain.StateAccepted {
		t.Fatalf("last event = %+v", last)
	}
	if last.Actor == nil || last.Actor.Type != "publisher" || last.Actor.ID != "marshal-scm-merger" {
		t.Fatalf("merged event actor = %+v", last.Actor)
	}
	for _, key := range []string{"intentId", "intentDigest", "receiptId", "receiptDigest", "headOid", "mergeCommitSha", "mergeMethod", "publicationDigest", "remoteCheckRecordDigest"} {
		if value, _ := last.Payload[key].(string); value == "" {
			t.Fatalf("merged event payload lacks %s: %+v", key, last.Payload)
		}
	}

	// Outcome binds intent and receipt digests.
	outcomeData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "outcome.json"))
	if err != nil {
		t.Fatalf("outcome missing: %v", err)
	}
	if err := fixture.validator.Validate(domain.KindOutcome, outcomeData); err != nil {
		t.Fatalf("outcome invalid: %v", err)
	}
	var outcome domain.OutcomeBundle
	if err := json.Unmarshal(outcomeData, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome.TerminalState != domain.StateAccepted || outcome.Verdict != "accept" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.IntentDigest != intent.IntentDigest || outcome.ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("outcome digest binding = %+v", outcome)
	}
}

func readPersistedReceiptFromRun(t *testing.T, runDir string, validator *contract.Validator) domain.SCMMergeReceipt {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(runDir, "scm-merge-receipt.json"))
	if err != nil {
		t.Fatalf("scm-merge-receipt.json missing: %v", err)
	}
	if err := validator.Validate(domain.KindSCMMergeReceipt, data); err != nil {
		t.Fatalf("persisted receipt failed schema validation: %v", err)
	}
	var receipt domain.SCMMergeReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}
