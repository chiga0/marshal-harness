package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTemporalDelivery is one activity delivery observed by the fake
// Temporal backend.
type fakeTemporalDelivery struct {
	CommandId string
	BuildId   string
	RunId     string
}

// fakeTemporalBackend is the fake Temporal-style backend the profile
// consistency fixtures run against: it simulates worker build identity,
// workflow history growth, Continue-As-New, activity heartbeat-timeout
// delivery retry, cancellation and total memory loss (crash), while the
// authoritative command and receipt state always lives in the seam journal.
type fakeTemporalBackend struct {
	mu               sync.Mutex
	payloads         PayloadStore
	buildId          string
	maxHistoryEvents int64
	recovered        bool
	dropped          map[string]int
	internalAttempts map[string]int
	businessAttempts int
	runs             []string
	history          map[string]int64
	deliveries       []fakeTemporalDelivery
	cancelled        map[string]struct{}
}

func newFakeTemporalBackend(store PayloadStore, buildId string, maxHistoryEvents int64) *fakeTemporalBackend {
	return &fakeTemporalBackend{
		payloads:         store,
		buildId:          buildId,
		maxHistoryEvents: maxHistoryEvents,
		dropped:          map[string]int{},
		internalAttempts: map[string]int{},
		history:          map[string]int64{},
		cancelled:        map[string]struct{}{},
	}
}

func (fake *fakeTemporalBackend) Deliver(ctx context.Context, command Command) (Receipt, error) {
	_ = ctx
	if err := command.Validate(); err != nil {
		return Receipt{}, err
	}
	payload, err := fake.payloads.Payload(ctx, command.PayloadRef)
	if err != nil {
		return Receipt{}, fmt.Errorf("fake temporal backend: payload unavailable: %w", err)
	}
	if err := VerifyPayloadRef(payload, command.PayloadRef); err != nil {
		return Receipt{}, err
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.recovered {
		return Receipt{}, fmt.Errorf("fake temporal backend: transport state has not been recovered")
	}
	if remaining := fake.dropped[command.CommandId]; remaining > 0 {
		fake.dropped[command.CommandId] = remaining - 1
		fake.internalAttempts[command.CommandId]++
		return Receipt{}, fmt.Errorf("fake temporal backend: simulated activity heartbeat timeout for commandId %s", command.CommandId)
	}
	fake.internalAttempts[command.CommandId]++
	runId := fake.currentRunLocked()
	fake.history[runId] += 2
	fake.deliveries = append(fake.deliveries, fakeTemporalDelivery{
		CommandId: command.CommandId,
		BuildId:   fake.buildId,
		RunId:     runId,
	})
	return Receipt{
		CommandId:   command.CommandId,
		DeliveredAt: time.Now().UTC().Format(time.RFC3339),
		AttemptSeq:  int64(fake.internalAttempts[command.CommandId]),
	}, nil
}

func (fake *fakeTemporalBackend) Recover(ctx context.Context) error {
	_ = ctx
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.recovered = true
	return nil
}

func (fake *fakeTemporalBackend) Close() error { return nil }

// currentRunLocked returns the current workflow run identity, starting the
// first run lazily. The caller must hold fake.mu.
func (fake *fakeTemporalBackend) currentRunLocked() string {
	if len(fake.runs) == 0 {
		fake.runs = append(fake.runs, "workflow-run-1")
	}
	return fake.runs[len(fake.runs)-1]
}

// Upgrade rolls the worker build identity, simulating a versioned worker
// rollout.
func (fake *fakeTemporalBackend) Upgrade(buildId string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.buildId = buildId
}

// ContinueAsNew starts the next workflow run. Per the frozen carry-over
// rule the new run carries no workflow memory: all continuation state must
// come from the ledger-derived journal.
func (fake *fakeTemporalBackend) ContinueAsNew() {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.runs = append(fake.runs, fmt.Sprintf("workflow-run-%d", len(fake.runs)+1))
}

// HistoryAtLimit reports whether the current workflow run reached the
// declared Continue-As-New history boundary.
func (fake *fakeTemporalBackend) HistoryAtLimit() bool {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.runs) == 0 {
		return false
	}
	run := fake.runs[len(fake.runs)-1]
	return fake.history[run] >= fake.maxHistoryEvents
}

// CurrentRunId returns the current workflow run identity.
func (fake *fakeTemporalBackend) CurrentRunId() string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.runs) == 0 {
		return ""
	}
	return fake.runs[len(fake.runs)-1]
}

// DropNextDeliveries scripts count simulated activity heartbeat timeouts
// for commandId: the backend redelivers internally before succeeding.
func (fake *fakeTemporalBackend) DropNextDeliveries(commandId string, count int) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.dropped[commandId] = count
}

// Cancel simulates an activity cancellation for commandId.
func (fake *fakeTemporalBackend) Cancel(commandId string) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.cancelled[commandId] = struct{}{}
}

// Cancelled reports whether the cancellation was simulated.
func (fake *fakeTemporalBackend) Cancelled(commandId string) bool {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	_, cancelled := fake.cancelled[commandId]
	return cancelled
}

// SimulateLostMemory drops every piece of backend internal state,
// simulating a crash; only the authoritative ledger facts survive.
func (fake *fakeTemporalBackend) SimulateLostMemory() {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.deliveries = nil
	fake.internalAttempts = map[string]int{}
	fake.history = map[string]int64{}
	fake.runs = nil
	fake.dropped = map[string]int{}
	fake.recovered = false
}

// BusinessAttempts counts business Attempts created by the backend; the
// frozen delivery retry budget keeps it at zero forever.
func (fake *fakeTemporalBackend) BusinessAttempts() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.businessAttempts
}

// DeliveriesFor returns the delivery observations recorded for commandId.
func (fake *fakeTemporalBackend) DeliveriesFor(commandId string) []fakeTemporalDelivery {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	selected := make([]fakeTemporalDelivery, 0)
	for _, delivery := range fake.deliveries {
		if delivery.CommandId == commandId {
			selected = append(selected, delivery)
		}
	}
	return selected
}

// IssueStatement simulates the backend announcing claims about a command.
func (fake *fakeTemporalBackend) IssueStatement(commandId string, claims ...BusinessClaim) BackendStatement {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return BackendStatement{CommandId: commandId, Claims: append([]BusinessClaim{}, claims...)}
}

var _ Backend = (*fakeTemporalBackend)(nil)

// temporalFactSeq builds a fixture LedgerFact bound to a stored dispatch
// payload.
func temporalFactSeq(t *testing.T, store *memoryPayloadStore, seed string, sequence int64) LedgerFact {
	t.Helper()
	ref := putCanonicalPayload(t, store, DispatchPayload{
		Target:         "temporal-target-" + seed,
		WorkloadDigest: fixedDigest("temporal-workload-" + seed),
	})
	return LedgerFact{Sequence: sequence, FactDigest: fixedDigest("temporal-fact-" + seed), PayloadDigest: ref}
}

// newTemporalFixture wires an engine over a fake Temporal backend with the
// profile's build identity and Continue-As-New history boundary.
func newTemporalFixture(t *testing.T, profile TemporalProfile) (*DurableExecutionEngine, *fakeTemporalBackend, *memoryPayloadStore) {
	t.Helper()
	if err := profile.Validate(); err != nil {
		t.Fatalf("the fixture profile must validate: %v", err)
	}
	store := newMemoryPayloadStore()
	fake := newFakeTemporalBackend(store, profile.Versioning.BuildId, profile.ContinueAsNew.MaxHistoryEvents)
	if err := fake.Recover(context.Background()); err != nil {
		t.Fatalf("Recover rejected the fake backend: %v", err)
	}
	engine, err := New(testNamespace(), fake)
	if err != nil {
		t.Fatalf("New rejected the fake backend: %v", err)
	}
	return engine, fake, store
}

func TestTemporalProfileDeclaration(t *testing.T) {
	profile := DefaultTemporalProfile()
	if err := profile.Validate(); err != nil {
		t.Fatalf("Validate rejected the frozen default profile: %v", err)
	}
	if err := profile.AssertSeamChoice(); err != nil {
		t.Fatalf("AssertSeamChoice rejected the frozen journal seam: %v", err)
	}
	if profile.BackendName != TemporalBackendName {
		t.Fatalf("the profile must declare the %q backend, got %q", TemporalBackendName, profile.BackendName)
	}
	if profile.Seam != SeamLedgerDerivedJournal {
		t.Fatalf("the frozen M9-e seam selection is the ledger-derived journal, got %q", string(profile.Seam))
	}

	outbox := profile
	outbox.Seam = SeamSameTransactionOutbox
	if err := outbox.Validate(); err != nil {
		t.Fatalf("Validate must accept the outbox seam as a schema-valid declaration: %v", err)
	}
	if err := outbox.AssertSeamChoice(); err == nil {
		t.Fatal("AssertSeamChoice must reject the outbox seam: the frozen selection is the ledger-derived journal and no outbox implementation exists")
	}

	invalidSeam := profile
	invalidSeam.Seam = SeamChoice("bogus")
	if err := invalidSeam.Validate(); err == nil {
		t.Fatal("Validate accepted a seam outside the closed enumeration")
	}

	cases := []struct {
		name   string
		mutate func(temporal *TemporalProfile)
	}{
		{"wrong backend name", func(temporal *TemporalProfile) { temporal.BackendName = "other" }},
		{"empty task queue", func(temporal *TemporalProfile) { temporal.Versioning.TaskQueue = "" }},
		{"empty build id", func(temporal *TemporalProfile) { temporal.Versioning.BuildId = "" }},
		{"unknown versioning strategy", func(temporal *TemporalProfile) { temporal.Versioning.Strategy = WorkflowVersioningStrategy("bogus") }},
		{"zero history events", func(temporal *TemporalProfile) { temporal.ContinueAsNew.MaxHistoryEvents = 0 }},
		{"zero history bytes", func(temporal *TemporalProfile) { temporal.ContinueAsNew.MaxHistoryBytes = 0 }},
		{"unknown carry-over", func(temporal *TemporalProfile) {
			temporal.ContinueAsNew.CarryOver = ContinueAsNewCarryOver("workflow-memory")
		}},
		{"unknown payload placement", func(temporal *TemporalProfile) { temporal.Payload.Placement = PayloadPlacement("inline") }},
		{"zero payload limit", func(temporal *TemporalProfile) { temporal.Payload.MaxPayloadBytes = 0 }},
		{"zero heartbeat timeout", func(temporal *TemporalProfile) { temporal.Activity.HeartbeatTimeoutSeconds = 0 }},
		{"zero initial interval", func(temporal *TemporalProfile) { temporal.Activity.InitialIntervalSeconds = 0 }},
		{"inverted intervals", func(temporal *TemporalProfile) { temporal.Activity.MaxIntervalSeconds = 0 }},
		{"zero max attempts", func(temporal *TemporalProfile) { temporal.Activity.MaxAttempts = 0 }},
		{"unknown cancel semantics", func(temporal *TemporalProfile) {
			temporal.Activity.CancelSemantics = ActivityCancelSemantics("declare-terminal")
		}},
		{"unknown retry budget", func(temporal *TemporalProfile) {
			temporal.Activity.RetryBudget = DeliveryRetryBudget("consumes-attempts")
		}},
	}
	for _, testCase := range cases {
		mutated := profile
		testCase.mutate(&mutated)
		if err := mutated.Validate(); err == nil {
			t.Fatalf("%s: Validate accepted an invalid profile declaration", testCase.name)
		}
	}

	upgraded, err := profile.WithBuildId("temporal-build-2")
	if err != nil {
		t.Fatalf("WithBuildId rejected a valid build rollout: %v", err)
	}
	if upgraded.Versioning.BuildId != "temporal-build-2" {
		t.Fatalf("WithBuildId must roll the build identity, got %q", upgraded.Versioning.BuildId)
	}
	if upgraded.Versioning.TaskQueue != profile.Versioning.TaskQueue || upgraded.Seam != profile.Seam {
		t.Fatal("WithBuildId must keep every other declaration identical")
	}
	if _, err := profile.WithBuildId(""); err == nil {
		t.Fatal("WithBuildId accepted a blank build identity")
	}
}

// TestTemporalProfilePayloadPolicy guards the declared payload
// externalization limit fail closed.
func TestTemporalProfilePayloadPolicy(t *testing.T) {
	policy := DefaultTemporalProfile().Payload
	if err := ValidatePayloadPolicy(policy.MaxPayloadBytes, policy); err != nil {
		t.Fatalf("ValidatePayloadPolicy rejected a payload exactly at the limit: %v", err)
	}
	if err := ValidatePayloadPolicy(0, policy); err != nil {
		t.Fatalf("ValidatePayloadPolicy rejected an empty payload: %v", err)
	}
	if err := ValidatePayloadPolicy(policy.MaxPayloadBytes+1, policy); err == nil {
		t.Fatal("ValidatePayloadPolicy accepted a payload above the declared limit")
	}
	if err := ValidatePayloadPolicy(-1, policy); err == nil {
		t.Fatal("ValidatePayloadPolicy accepted a negative payload size")
	}
	invalidPolicy := policy
	invalidPolicy.Placement = PayloadPlacement("inline")
	if err := ValidatePayloadPolicy(1, invalidPolicy); err == nil {
		t.Fatal("ValidatePayloadPolicy accepted a placement outside the closed enumeration")
	}
}

// TestTemporalProfilePayloadExternalization guards that commands carry only
// the payloadRef digest: delivery resolves bytes through the external
// payload store fail closed, never from the command itself.
func TestTemporalProfilePayloadExternalization(t *testing.T) {
	ctx := context.Background()
	profile := DefaultTemporalProfile()
	engine, fake, store := newTemporalFixture(t, profile)
	fact := temporalFactSeq(t, store, "externalized", 1)
	command, err := engine.DeriveCommand(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected the ledger fact: %v", err)
	}
	if command.PayloadRef != fact.PayloadDigest {
		t.Fatal("the command must carry only the external payload reference")
	}
	if _, err := engine.Deliver(ctx, command); err != nil {
		t.Fatalf("Deliver rejected the externalized command: %v", err)
	}
	// A backend without access to the payload store fails closed: the
	// command bytes alone are useless.
	emptyStore := newMemoryPayloadStore()
	starved := newFakeTemporalBackend(emptyStore, profile.Versioning.BuildId, profile.ContinueAsNew.MaxHistoryEvents)
	if err := starved.Recover(ctx); err != nil {
		t.Fatalf("Recover rejected the starved backend: %v", err)
	}
	if _, err := starved.Deliver(ctx, command); err == nil {
		t.Fatal("a backend without the externalized payload must fail closed")
	}
	// Oversized payloads fail closed against the declared limit.
	oversized := make([]byte, profile.Payload.MaxPayloadBytes+1)
	if err := ValidatePayloadPolicy(int64(len(oversized)), profile.Payload); err == nil {
		t.Fatal("ValidatePayloadPolicy accepted an oversized payload")
	}
	if fake.BusinessAttempts() != 0 {
		t.Fatal("payload admission must never create business Attempts")
	}
}

// TestTemporalProfileUpgradeBuildIdStability covers the ADR 0018 §8/§15
// workflow versioning/build ID upgrade scenario: command identity derives
// only from the ledger fact digest, so a worker build rollout never
// changes commandIds, duplicate deliveries across builds merge
// idempotently, and delivery observations never become business authority.
func TestTemporalProfileUpgradeBuildIdStability(t *testing.T) {
	ctx := context.Background()
	profile := DefaultTemporalProfile()
	engine, fake, store := newTemporalFixture(t, profile)
	fact1 := temporalFactSeq(t, store, "upgrade-1", 1)
	fact2 := temporalFactSeq(t, store, "upgrade-2", 2)
	first, err := engine.DeriveCommand(fact1, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected fact 1: %v", err)
	}
	second, err := engine.DeriveCommand(fact2, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected fact 2: %v", err)
	}
	if _, err := engine.Deliver(ctx, first); err != nil {
		t.Fatalf("Deliver rejected command 1: %v", err)
	}
	if _, err := engine.Deliver(ctx, second); err != nil {
		t.Fatalf("Deliver rejected command 2: %v", err)
	}

	upgraded, err := profile.WithBuildId("temporal-build-2")
	if err != nil {
		t.Fatalf("WithBuildId rejected the upgrade: %v", err)
	}
	fake.Upgrade(upgraded.Versioning.BuildId)

	// Re-deriving the identical ledger facts under the upgraded build yields
	// the identical commandIds.
	firstReplay, err := engine.DeriveCommand(fact1, CommandKindDispatch)
	if err != nil {
		t.Fatalf("replay DeriveCommand after upgrade failed: %v", err)
	}
	if firstReplay.CommandId != first.CommandId {
		t.Fatalf("the build rollout must never change command identity: %s != %s", firstReplay.CommandId, first.CommandId)
	}

	// A backend-side duplicate delivery under the new build merges
	// idempotently at the seam.
	duplicateReceipt, err := fake.Deliver(ctx, first)
	if err != nil {
		t.Fatalf("duplicate delivery under the upgraded build failed: %v", err)
	}
	if duplicateReceipt.AttemptSeq != 2 {
		t.Fatalf("the duplicate delivery must carry attemptSeq 2, got %d", duplicateReceipt.AttemptSeq)
	}
	if err := engine.ConsolidateReceipt(duplicateReceipt); err != nil {
		t.Fatalf("ConsolidateReceipt rejected the duplicate receipt: %v", err)
	}
	if engine.Journal().DuplicateDeliveries(first.CommandId) != 1 {
		t.Fatalf("the cross-build duplicate must merge exactly once, got %d", engine.Journal().DuplicateDeliveries(first.CommandId))
	}
	observations := fake.DeliveriesFor(first.CommandId)
	if len(observations) != 2 {
		t.Fatalf("the fake backend must observe both deliveries, got %d", len(observations))
	}
	if observations[0].BuildId != "temporal-build-1" || observations[1].BuildId != "temporal-build-2" {
		t.Fatalf("the observations must carry the build identities, got %+v", observations)
	}
	if observations[0].CommandId != observations[1].CommandId {
		t.Fatal("the identical business command must survive the build rollout unchanged")
	}

	// A post-upgrade command delivers under the new build.
	fact3 := temporalFactSeq(t, store, "upgrade-3", 3)
	third, err := engine.DeriveCommand(fact3, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected the post-upgrade fact: %v", err)
	}
	if _, err := engine.Deliver(ctx, third); err != nil {
		t.Fatalf("Deliver rejected the post-upgrade command: %v", err)
	}
	postUpgrade := fake.DeliveriesFor(third.CommandId)
	if len(postUpgrade) != 1 || postUpgrade[0].BuildId != "temporal-build-2" {
		t.Fatalf("the post-upgrade delivery must run under the new build, got %+v", postUpgrade)
	}
	if commands := engine.Journal().Commands(); len(commands) != 3 {
		t.Fatalf("the upgrade must never create divergent command entries, got %d", len(commands))
	}
	if fake.BusinessAttempts() != 0 {
		t.Fatal("the upgrade scenario must never create business Attempts")
	}
}

// TestTemporalProfileContinueAsNewBoundary covers the ADR 0018 §8/§15
// Continue-As-New scenario: at the declared history boundary the workflow
// continues as a new run carrying no workflow memory; the new run is
// supplied entirely by the ledger-derived journal and duplicate deliveries
// across the boundary merge idempotently.
func TestTemporalProfileContinueAsNewBoundary(t *testing.T) {
	ctx := context.Background()
	profile := DefaultTemporalProfile()
	profile.ContinueAsNew.MaxHistoryEvents = 4
	engine, fake, store := newTemporalFixture(t, profile)
	if profile.ContinueAsNew.CarryOver != ContinueAsNewCarryOverJournal {
		t.Fatalf("the frozen carry-over rule must re-derive from the journal, got %q", string(profile.ContinueAsNew.CarryOver))
	}
	fact1 := temporalFactSeq(t, store, "can-1", 1)
	fact2 := temporalFactSeq(t, store, "can-2", 2)
	first, err := engine.DeriveCommand(fact1, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected fact 1: %v", err)
	}
	second, err := engine.DeriveCommand(fact2, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected fact 2: %v", err)
	}
	if _, err := engine.Deliver(ctx, first); err != nil {
		t.Fatalf("Deliver rejected command 1: %v", err)
	}
	if _, err := engine.Deliver(ctx, second); err != nil {
		t.Fatalf("Deliver rejected command 2: %v", err)
	}
	if !fake.HistoryAtLimit() {
		t.Fatal("the fixture history must reach the declared Continue-As-New boundary")
	}
	firstRun := fake.CurrentRunId()

	// Continue-As-New: the new run carries no workflow memory.
	fake.ContinueAsNew()
	if fake.CurrentRunId() == firstRun {
		t.Fatal("Continue-As-New must start a fresh workflow run")
	}
	// The journal stays authoritative across the boundary.
	if commands := engine.Journal().Commands(); len(commands) != 2 {
		t.Fatalf("Continue-As-New must never disturb the journal, got %d commands", len(commands))
	}
	if _, delivered := engine.Journal().ReceiptFor(first.CommandId); !delivered {
		t.Fatal("the pre-boundary receipt must survive Continue-As-New")
	}

	// A post-boundary command consumes on the new run.
	fact3 := temporalFactSeq(t, store, "can-3", 3)
	third, err := engine.DeriveCommand(fact3, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected the post-boundary fact: %v", err)
	}
	if _, err := engine.Deliver(ctx, third); err != nil {
		t.Fatalf("Deliver rejected the post-boundary command: %v", err)
	}
	postBoundary := fake.DeliveriesFor(third.CommandId)
	if len(postBoundary) != 1 || postBoundary[0].RunId != fake.CurrentRunId() {
		t.Fatalf("the post-boundary delivery must run on the new workflow run, got %+v", postBoundary)
	}

	// An at-least-once duplicate of a pre-boundary command merges without
	// creating a second entry or a second business effect.
	duplicateReceipt, err := fake.Deliver(ctx, first)
	if err != nil {
		t.Fatalf("cross-boundary duplicate delivery failed: %v", err)
	}
	if err := engine.ConsolidateReceipt(duplicateReceipt); err != nil {
		t.Fatalf("ConsolidateReceipt rejected the cross-boundary duplicate: %v", err)
	}
	if engine.Journal().DuplicateDeliveries(first.CommandId) != 1 {
		t.Fatalf("the cross-boundary duplicate must merge exactly once, got %d", engine.Journal().DuplicateDeliveries(first.CommandId))
	}
	if commands := engine.Journal().Commands(); len(commands) != 3 {
		t.Fatalf("Continue-As-New must never duplicate journal entries, got %d", len(commands))
	}
	if fake.BusinessAttempts() != 0 {
		t.Fatal("Continue-As-New must never create business Attempts")
	}
}

// TestTemporalProfileActivityHeartbeatRetry covers the ADR 0018 §8/§15
// activity heartbeat/retry scenario: heartbeat timeouts retry the delivery
// under the identical commandId, attemptSeq counts transport attempts only,
// and no business Attempt is created or consumed.
func TestTemporalProfileActivityHeartbeatRetry(t *testing.T) {
	ctx := context.Background()
	profile := DefaultTemporalProfile()
	if profile.Activity.RetryBudget != DeliveryRetryBudgetBackendOnly {
		t.Fatalf("the frozen retry budget must be backend-only, got %q", string(profile.Activity.RetryBudget))
	}
	engine, fake, store := newTemporalFixture(t, profile)
	fact := temporalFactSeq(t, store, "heartbeat", 1)
	command, err := engine.DeriveCommand(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected the ledger fact: %v", err)
	}
	fake.DropNextDeliveries(command.CommandId, 2)
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := engine.Deliver(ctx, command); err == nil {
			t.Fatalf("attempt %d: the scripted heartbeat timeout must fail the delivery", attempt+1)
		} else if !strings.Contains(err.Error(), "heartbeat timeout") {
			t.Fatalf("attempt %d: expected the simulated heartbeat timeout, got %v", attempt+1, err)
		}
	}
	if _, delivered := engine.Journal().ReceiptFor(command.CommandId); delivered {
		t.Fatal("failed delivery attempts must never produce a receipt")
	}
	if pending := engine.Journal().Pending(); len(pending) != 1 {
		t.Fatalf("the command must stay pending across heartbeat retries, got %d", len(pending))
	}
	receipt, err := engine.Deliver(ctx, command)
	if err != nil {
		t.Fatalf("the retried delivery must eventually succeed: %v", err)
	}
	if receipt.AttemptSeq != 3 {
		t.Fatalf("attemptSeq must count the backend delivery attempts, got %d", receipt.AttemptSeq)
	}
	if commands := engine.Journal().Commands(); len(commands) != 1 {
		t.Fatalf("delivery retry must never create a second command entry, got %d", len(commands))
	}
	if fake.BusinessAttempts() != 0 {
		t.Fatalf("delivery retry must never create a business Attempt, got %d", fake.BusinessAttempts())
	}
	// A further duplicate delivery keeps the business budget untouched.
	extra, err := fake.Deliver(ctx, command)
	if err != nil {
		t.Fatalf("the duplicate delivery failed: %v", err)
	}
	if err := engine.ConsolidateReceipt(extra); err != nil {
		t.Fatalf("ConsolidateReceipt rejected the duplicate receipt: %v", err)
	}
	if fake.BusinessAttempts() != 0 {
		t.Fatal("duplicate delivery must never consume a business retry budget")
	}
}

// TestTemporalProfileActivityCancel covers the cancel semantics: a
// cancelled activity reports a delivery-shaped statement only; any business
// terminal claim fails closed at the seam.
func TestTemporalProfileActivityCancel(t *testing.T) {
	ctx := context.Background()
	profile := DefaultTemporalProfile()
	if profile.Activity.CancelSemantics != ActivityCancelReportOnly {
		t.Fatalf("the frozen cancel semantics must report no business claim, got %q", string(profile.Activity.CancelSemantics))
	}
	engine, fake, store := newTemporalFixture(t, profile)
	fact := temporalFactSeq(t, store, "cancel", 1)
	command, err := engine.DeriveCommand(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected the ledger fact: %v", err)
	}
	if _, err := engine.Deliver(ctx, command); err != nil {
		t.Fatalf("Deliver rejected the command: %v", err)
	}
	fake.Cancel(command.CommandId)
	if !fake.Cancelled(command.CommandId) {
		t.Fatal("the cancellation must be recorded by the fake backend")
	}
	report := fake.IssueStatement(command.CommandId)
	if err := engine.AcceptBackendStatement(report); err != nil {
		t.Fatalf("AcceptBackendStatement rejected the cancellation transport report: %v", err)
	}
	rogue := fake.IssueStatement(command.CommandId, BusinessClaimTerminalState)
	if err := engine.AcceptBackendStatement(rogue); !errors.Is(err, ErrBackendAuthorityViolation) {
		t.Fatalf("a cancelled activity announcing a terminal state must fail closed, got %v", err)
	}
}

// TestTemporalProfileNegativeBusinessClaims covers the backend authority
// boundary through the fake backend: every business claim a backend
// announces fails closed at the seam.
func TestTemporalProfileNegativeBusinessClaims(t *testing.T) {
	ctx := context.Background()
	profile := DefaultTemporalProfile()
	engine, fake, store := newTemporalFixture(t, profile)
	fact := temporalFactSeq(t, store, "claims", 1)
	command, err := engine.DeriveCommand(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected the ledger fact: %v", err)
	}
	if _, err := engine.Deliver(ctx, command); err != nil {
		t.Fatalf("Deliver rejected the command: %v", err)
	}
	for _, claim := range []BusinessClaim{
		BusinessClaimLifecycleTransition,
		BusinessClaimReviewDecision,
		BusinessClaimRework,
		BusinessClaimTerminalState,
		BusinessClaimSafeToPublish,
	} {
		statement := fake.IssueStatement(command.CommandId, claim)
		if err := engine.AcceptBackendStatement(statement); !errors.Is(err, ErrBackendAuthorityViolation) {
			t.Fatalf("claim %q: the seam must fail closed with ErrBackendAuthorityViolation, got %v", string(claim), err)
		}
	}
	combined := fake.IssueStatement(command.CommandId, BusinessClaimRework, BusinessClaimSafeToPublish)
	if err := engine.AcceptBackendStatement(combined); !errors.Is(err, ErrBackendAuthorityViolation) {
		t.Fatalf("combined business claims must fail closed, got %v", err)
	}
}

// TestTemporalProfileCrashRecoverySingleSeam covers the ADR 0018 §15
// crash/upgrade recovery scenario: after the backend loses all internal
// state, replaying the ledger facts and committed receipts through a fresh
// seam redelivers exactly the undelivered commands with stable commandIds —
// recovery never depends on backend internal state.
func TestTemporalProfileCrashRecoverySingleSeam(t *testing.T) {
	ctx := context.Background()
	profile := DefaultTemporalProfile()
	engine, fake, store := newTemporalFixture(t, profile)
	facts := []LedgerFact{
		temporalFactSeq(t, store, "crash-1", 1),
		temporalFactSeq(t, store, "crash-2", 2),
		temporalFactSeq(t, store, "crash-3", 3),
	}
	commands := make([]Command, 0, len(facts))
	for _, fact := range facts {
		command, err := engine.DeriveCommand(fact, CommandKindDispatch)
		if err != nil {
			t.Fatalf("DeriveCommand rejected fact %d: %v", fact.Sequence, err)
		}
		commands = append(commands, command)
	}
	receipts := make([]Receipt, 0, 2)
	for _, command := range commands[:2] {
		receipt, err := engine.Deliver(ctx, command)
		if err != nil {
			t.Fatalf("Deliver rejected command %s: %v", command.CommandId, err)
		}
		receipts = append(receipts, receipt)
	}

	// Crash: the backend loses all internal state; only the ledger facts
	// and the committed receipt facts survive.
	fake.SimulateLostMemory()
	if err := fake.Recover(ctx); err != nil {
		t.Fatalf("Recover rejected the crashed backend: %v", err)
	}
	recovered, err := New(testNamespace(), fake)
	if err != nil {
		t.Fatalf("New rejected the recovery construction: %v", err)
	}
	recoveredCommands := make([]Command, 0, len(facts))
	for _, fact := range facts {
		command, err := recovered.DeriveCommand(fact, CommandKindDispatch)
		if err != nil {
			t.Fatalf("recovery DeriveCommand rejected fact %d: %v", fact.Sequence, err)
		}
		recoveredCommands = append(recoveredCommands, command)
	}
	for index, command := range recoveredCommands {
		if command.CommandId != commands[index].CommandId {
			t.Fatalf("recovery derivation must be stable across backend memory loss: %s != %s", command.CommandId, commands[index].CommandId)
		}
	}
	for _, receipt := range receipts {
		if err := recovered.ConsolidateReceipt(receipt); err != nil {
			t.Fatalf("recovery ConsolidateReceipt rejected a committed receipt: %v", err)
		}
	}
	if pending := recovered.Journal().Pending(); len(pending) != 1 || pending[0].CommandId != commands[2].CommandId {
		t.Fatalf("recovery must see exactly the undelivered command pending, got %v", pending)
	}
	redelivered, err := recovered.RedeliverPending(ctx)
	if err != nil {
		t.Fatalf("RedeliverPending failed during recovery: %v", err)
	}
	if len(redelivered) != 1 || redelivered[0].CommandId != commands[2].CommandId {
		t.Fatalf("recovery must redeliver exactly the undelivered command, got %v", redelivered)
	}
	if observations := fake.DeliveriesFor(commands[2].CommandId); len(observations) != 1 {
		t.Fatalf("the recovered backend must observe exactly the redelivered command, got %d", len(observations))
	}
	if observations := fake.DeliveriesFor(commands[0].CommandId); len(observations) != 0 {
		t.Fatalf("already-delivered commands must not be redelivered after memory loss, got %d", len(observations))
	}
	if pending := recovered.Journal().Pending(); len(pending) != 0 {
		t.Fatalf("after recovery nothing may stay pending, got %d", len(pending))
	}
	if fake.BusinessAttempts() != 0 {
		t.Fatal("crash recovery must never create business Attempts")
	}
}
