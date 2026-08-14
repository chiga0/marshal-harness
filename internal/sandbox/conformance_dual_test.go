package sandbox

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/chiga0/marshal-harness/internal/dispatch"
)

// scriptedDualLease is the scripted lease state of the in-package dual
// fixtures: the identical business rules the transport adapters adjudicate,
// reduced to a deterministic state machine with no HTTP and no clock.
type scriptedDualLease struct {
	ref          DualLeaseRef
	acked        bool
	terminal     bool
	terminalKind DualTraceKind
	openCommand  string
	finished     map[string]struct{}
}

// scriptedDualAuthority is the scripted DualAuthority of the in-package
// dual fixtures.
type scriptedDualAuthority struct {
	scenario string

	mu                sync.Mutex
	attempts          map[string]string
	leases            map[string]*scriptedDualLease
	registrationState string
	snapshotBroken    string
	evidenceRevoked   bool
	recorder          *DualTraceRecorder
}

func newScriptedDualAuthority(scenario string) *scriptedDualAuthority {
	return &scriptedDualAuthority{
		scenario:          scenario,
		attempts:          map[string]string{},
		leases:            map[string]*scriptedDualLease{},
		registrationState: "active",
		recorder:          &DualTraceRecorder{},
	}
}

// currentLedgerClass mirrors the current-ledger eligibility view: the first
// failing fact determines the closed reason class, exactly like the
// gate-6 claim ordering (registration lifecycle before snapshot before
// evidence).
func (a *scriptedDualAuthority) currentLedgerClass() DualReasonClass {
	switch a.registrationState {
	case "revoked":
		return DualReasonRevoked
	case "expired":
		return DualReasonExpired
	}
	switch a.snapshotBroken {
	case "superseded":
		return DualReasonSuperseded
	case "incompatible":
		return DualReasonIncompatible
	}
	if a.evidenceRevoked {
		return DualReasonEvidenceRevoked
	}
	return DualReasonNone
}

func (a *scriptedDualAuthority) record(event DualTraceEvent) {
	if err := a.recorder.Record(event); err != nil {
		panic(err)
	}
}

func (a *scriptedDualAuthority) AdjudicateClaim(ctx context.Context, request DualClaimRequest) (DualLeaseRef, DualOperationOutcome, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := request.Validate(); err != nil {
		attemptId := request.AttemptId
		if strings.TrimSpace(attemptId) == "" {
			attemptId = "invalid"
		}
		a.record(DualTraceEvent{Kind: DualEventClaimRejected, AttemptId: attemptId, ReasonClass: DualReasonIneligible})
		return DualLeaseRef{}, DualOperationOutcome{Accepted: false, ReasonClass: DualReasonIneligible, Detail: err.Error()}, nil
	}
	if class := a.currentLedgerClass(); class != DualReasonNone {
		a.record(DualTraceEvent{Kind: DualEventClaimRejected, AttemptId: request.AttemptId, ReasonClass: class})
		return DualLeaseRef{}, DualOperationOutcome{Accepted: false, ReasonClass: class, Detail: "the current authority ledger is not eligible"}, nil
	}
	if existing, taken := a.attempts[request.AttemptId]; taken {
		a.record(DualTraceEvent{Kind: DualEventClaimRejected, AttemptId: request.AttemptId, ReasonClass: DualReasonDuplicateClaim})
		return DualLeaseRef{}, DualOperationOutcome{Accepted: false, ReasonClass: DualReasonDuplicateClaim, Detail: "attempt already carries lease " + existing}, nil
	}
	leaseId := dualScenarioDigest(a.scenario + "\x00" + request.AttemptId + "\x00" + request.AllocationId)
	ref := DualLeaseRef{
		LeaseId:       leaseId,
		TaskId:        request.TaskId,
		RunId:         request.RunId,
		AttemptId:     request.AttemptId,
		AllocationId:  request.AllocationId,
		Generation:    1,
		FencingToken:  dualScenarioDigest("fencing" + "\x00" + leaseId),
		AckDeadlineAt: "2026-01-01T00:30:00Z",
		ExpiresAt:     "2026-01-02T00:00:00Z",
	}
	a.attempts[request.AttemptId] = leaseId
	a.leases[leaseId] = &scriptedDualLease{ref: ref, finished: map[string]struct{}{}}
	return ref, DualOperationOutcome{Accepted: true}, nil
}

func (a *scriptedDualAuthority) CompleteClaim(ctx context.Context, lease DualLeaseRef) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.leases[lease.LeaseId]
	if !ok || record.terminal {
		return ErrInvalidRequest
	}
	record.acked = true
	a.record(DualTraceEvent{Kind: DualEventClaimAccepted, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId})
	return nil
}

// fencingCheck adjudicates the presented lease identity against the
// current lease record.
func (a *scriptedDualAuthority) fencingCheck(record *scriptedDualLease, lease DualLeaseRef) bool {
	return record.ref.Generation == lease.Generation && record.ref.FencingToken == lease.FencingToken
}

func (a *scriptedDualAuthority) AdjudicateExecutionStart(ctx context.Context, lease DualLeaseRef, commandId string) (string, DualOperationOutcome, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.leases[lease.LeaseId]
	if !ok {
		return "", DualOperationOutcome{Accepted: false, ReasonClass: DualReasonFencing, Detail: "unknown lease"}, nil
	}
	if record.terminal || !record.acked {
		return "", DualOperationOutcome{Accepted: false, ReasonClass: DualReasonFencing, Detail: "the lease is not in flight"}, nil
	}
	if !a.fencingCheck(record, lease) {
		a.record(DualTraceEvent{Kind: DualEventFencingViolationBlocked, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: DualReasonFencing})
		return "", DualOperationOutcome{Accepted: false, ReasonClass: DualReasonFencing, Detail: "stale fencing identity"}, nil
	}
	if class := a.currentLedgerClass(); class != DualReasonNone {
		record.terminal = true
		record.terminalKind = DualEventLeaseRevoked
		a.record(DualTraceEvent{Kind: DualEventLeaseRevoked, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: class})
		return "", DualOperationOutcome{Accepted: false, ReasonClass: DualReasonFencing, Detail: "the lease lost eligibility"}, nil
	}
	if record.openCommand != "" {
		return "", DualOperationOutcome{Accepted: false, ReasonClass: DualReasonNone, Detail: "the lease already carries an open execution"}, nil
	}
	return "exec:" + lease.LeaseId + ":" + commandId, DualOperationOutcome{Accepted: true}, nil
}

func (a *scriptedDualAuthority) RecordExecutionStarted(ctx context.Context, lease DualLeaseRef, commandId, executionId string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.leases[lease.LeaseId]
	if !ok || record.terminal || record.openCommand != "" {
		return ErrInvalidRequest
	}
	record.openCommand = commandId
	a.record(DualTraceEvent{Kind: DualEventExecStarted, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId})
	return nil
}

func (a *scriptedDualAuthority) RecordExecutionFinished(ctx context.Context, lease DualLeaseRef, commandId, executionId string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.leases[lease.LeaseId]
	if !ok || record.terminal || record.openCommand != commandId {
		return "", ErrInvalidRequest
	}
	record.openCommand = ""
	record.finished[commandId] = struct{}{}
	digest := DualExecutionDigest(lease.LeaseId, commandId, executionId)
	a.record(DualTraceEvent{Kind: DualEventExecFinished, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, Digest: digest})
	return digest, nil
}

func (a *scriptedDualAuthority) AdjudicateResult(ctx context.Context, lease DualLeaseRef, commandId, resultDigest string) (DualOperationOutcome, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.leases[lease.LeaseId]
	if !ok {
		return DualOperationOutcome{Accepted: false, ReasonClass: DualReasonLateResult, Detail: "unknown lease"}, nil
	}
	if record.terminal {
		a.record(DualTraceEvent{Kind: DualEventResultQuarantined, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: DualReasonLateResult})
		return DualOperationOutcome{Accepted: false, ReasonClass: DualReasonLateResult, Detail: "the lease lost eligibility before the result arrived"}, nil
	}
	if !a.fencingCheck(record, lease) {
		a.record(DualTraceEvent{Kind: DualEventResultQuarantined, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: DualReasonFencing})
		return DualOperationOutcome{Accepted: false, ReasonClass: DualReasonFencing, Detail: "stale fencing identity"}, nil
	}
	if class := a.currentLedgerClass(); class != DualReasonNone {
		record.terminal = true
		record.terminalKind = DualEventLeaseRevoked
		a.record(DualTraceEvent{Kind: DualEventLeaseRevoked, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: class})
		a.record(DualTraceEvent{Kind: DualEventResultQuarantined, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: class})
		return DualOperationOutcome{Accepted: false, ReasonClass: class, Detail: "the lease lost eligibility"}, nil
	}
	if _, finished := record.finished[commandId]; !finished {
		a.record(DualTraceEvent{Kind: DualEventResultQuarantined, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: DualReasonIneligible})
		return DualOperationOutcome{Accepted: false, ReasonClass: DualReasonIneligible, Detail: "no finished execution carries this result"}, nil
	}
	a.record(DualTraceEvent{Kind: DualEventResultAdmitted, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, Digest: resultDigest})
	return DualOperationOutcome{Accepted: true}, nil
}

func (a *scriptedDualAuthority) AdjudicateHeartbeat(ctx context.Context, lease DualLeaseRef) (DualOperationOutcome, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.leases[lease.LeaseId]
	if !ok {
		return DualOperationOutcome{Accepted: false, ReasonClass: DualReasonFencing, Detail: "unknown lease"}, nil
	}
	if record.terminal {
		class := DualReasonRevoked
		if record.terminalKind == DualEventLeaseExpired {
			class = DualReasonDeadline
		}
		return DualOperationOutcome{Accepted: false, ReasonClass: class, Detail: "the lease is terminal"}, nil
	}
	if class := a.currentLedgerClass(); class != DualReasonNone {
		record.terminal = true
		record.terminalKind = DualEventLeaseRevoked
		a.record(DualTraceEvent{Kind: DualEventLeaseRevoked, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: class})
		return DualOperationOutcome{Accepted: false, ReasonClass: class, Detail: "the lease lost eligibility"}, nil
	}
	return DualOperationOutcome{Accepted: true}, nil
}

func (a *scriptedDualAuthority) AdjudicateStaleOperation(ctx context.Context, lease DualLeaseRef) (DualOperationOutcome, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.leases[lease.LeaseId]
	if !ok {
		return DualOperationOutcome{Accepted: false, ReasonClass: DualReasonFencing, Detail: "unknown lease"}, nil
	}
	if a.fencingCheck(record, lease) {
		return DualOperationOutcome{}, ErrInvalidRequest
	}
	a.record(DualTraceEvent{Kind: DualEventFencingViolationBlocked, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: DualReasonFencing})
	return DualOperationOutcome{Accepted: false, ReasonClass: DualReasonFencing, Detail: "stale fencingToken blocked"}, nil
}

func (a *scriptedDualAuthority) Invalidate(ctx context.Context, kind DualInvalidationKind) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := kind.Validate(); err != nil {
		return err
	}
	var class DualReasonClass
	switch kind {
	case DualInvalidateRegistrationRevoke:
		a.registrationState = "revoked"
		class = DualReasonRevoked
	case DualInvalidateRegistrationExpire:
		a.registrationState = "expired"
		class = DualReasonExpired
	case DualInvalidateRegistrationIncompatible:
		a.snapshotBroken = "incompatible"
		class = DualReasonIncompatible
	case DualInvalidateSnapshotSupersede:
		a.snapshotBroken = "superseded"
		class = DualReasonSuperseded
	case DualInvalidateEvidenceRevoke:
		a.evidenceRevoked = true
		class = DualReasonEvidenceRevoked
	}
	for _, record := range a.leases {
		if record.terminal {
			continue
		}
		record.terminal = true
		record.terminalKind = DualEventLeaseRevoked
		a.record(DualTraceEvent{Kind: DualEventLeaseRevoked, AttemptId: record.ref.AttemptId, LeaseId: record.ref.LeaseId, ReasonClass: class})
	}
	return nil
}

func (a *scriptedDualAuthority) MissAckDeadline(ctx context.Context, lease DualLeaseRef) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.leases[lease.LeaseId]
	if !ok || record.terminal || record.acked {
		return ErrInvalidRequest
	}
	record.terminal = true
	record.terminalKind = DualEventLeaseExpired
	a.record(DualTraceEvent{Kind: DualEventLeaseExpired, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: DualReasonDeadline})
	return nil
}

func (a *scriptedDualAuthority) ExpireLeaseWindow(ctx context.Context, lease DualLeaseRef) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.leases[lease.LeaseId]
	if !ok || record.terminal {
		return ErrInvalidRequest
	}
	record.terminal = true
	record.terminalKind = DualEventLeaseExpired
	a.record(DualTraceEvent{Kind: DualEventLeaseExpired, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, ReasonClass: DualReasonDeadline})
	return nil
}

func (a *scriptedDualAuthority) Reregister(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.registrationState = "active"
	a.snapshotBroken = ""
	a.evidenceRevoked = false
	return nil
}

func (a *scriptedDualAuthority) Trace() []DualTraceEvent {
	return a.recorder.Events()
}

// scriptedDualBinding is the scripted DualTopologyBinding of the in-package
// dual fixtures: topology-specific transitions are the no-op abstraction,
// the injected faults apply identically under every topology.
type scriptedDualBinding struct {
	topology DualTopology
	faults   []FaultSpec
}

func (b *scriptedDualBinding) Topology() DualTopology { return b.topology }

func (b *scriptedDualBinding) faultFor(operation string) (FaultKind, bool) {
	for _, spec := range b.faults {
		if spec.matches(operation, "") {
			return spec.Fault, true
		}
	}
	return "", false
}

func (b *scriptedDualBinding) Claim(ctx context.Context, authority DualAuthority, request DualClaimRequest) (DualClaimReceipt, error) {
	lease, outcome, err := authority.AdjudicateClaim(ctx, request)
	if err != nil {
		return DualClaimReceipt{}, err
	}
	if !outcome.Accepted {
		return DualClaimReceipt{Lease: lease, Outcome: outcome}, nil
	}
	if fault, active := b.faultFor(OperationProvision); active && fault == FaultDropResponse {
		return DualClaimReceipt{
			Lease:   lease,
			Outcome: DualOperationOutcome{Accepted: false, ReasonClass: DualReasonNone, Detail: "the provision response was lost"},
		}, nil
	}
	if err := authority.CompleteClaim(ctx, lease); err != nil {
		return DualClaimReceipt{}, err
	}
	return DualClaimReceipt{Lease: lease, Outcome: DualOperationOutcome{Accepted: true}}, nil
}

func (b *scriptedDualBinding) ClaimUnacked(ctx context.Context, authority DualAuthority, request DualClaimRequest) (DualClaimReceipt, error) {
	lease, outcome, err := authority.AdjudicateClaim(ctx, request)
	if err != nil {
		return DualClaimReceipt{}, err
	}
	if !outcome.Accepted {
		return DualClaimReceipt{Lease: lease, Outcome: outcome}, nil
	}
	return DualClaimReceipt{
		Lease:   lease,
		Outcome: DualOperationOutcome{Accepted: false, ReasonClass: DualReasonNone, Detail: "the offer was delivered without an ack"},
	}, nil
}

func (b *scriptedDualBinding) StartExecution(ctx context.Context, authority DualAuthority, lease DualLeaseRef, commandId string) (string, DualOperationOutcome, error) {
	executionId, outcome, err := authority.AdjudicateExecutionStart(ctx, lease, commandId)
	if err != nil || !outcome.Accepted {
		return "", outcome, err
	}
	if fault, active := b.faultFor(OperationExec); active {
		switch fault {
		case FaultReject:
			return "", DualOperationOutcome{Accepted: false, ReasonClass: DualReasonNone, Detail: "the provider rejected the execution through an injected fault"}, nil
		case FaultDropResponse:
			return "", DualOperationOutcome{Accepted: false, ReasonClass: DualReasonNone, Detail: "the exec request was lost"}, nil
		case FaultDelay:
			// The delay fault advances the logical clock only; the
			// business trace stays identical.
		}
	}
	if err := authority.RecordExecutionStarted(ctx, lease, commandId, executionId); err != nil {
		return "", DualOperationOutcome{}, err
	}
	return executionId, DualOperationOutcome{Accepted: true}, nil
}

func (b *scriptedDualBinding) FinishExecution(ctx context.Context, authority DualAuthority, lease DualLeaseRef, commandId, executionId string) (string, DualOperationOutcome, error) {
	digest, err := authority.RecordExecutionFinished(ctx, lease, commandId, executionId)
	if err != nil {
		return "", DualOperationOutcome{}, err
	}
	return digest, DualOperationOutcome{Accepted: true}, nil
}

func (b *scriptedDualBinding) SubmitResult(ctx context.Context, authority DualAuthority, lease DualLeaseRef, commandId, resultDigest string) (DualOperationOutcome, error) {
	return authority.AdjudicateResult(ctx, lease, commandId, resultDigest)
}

func (b *scriptedDualBinding) Heartbeat(ctx context.Context, authority DualAuthority, lease DualLeaseRef) (DualOperationOutcome, error) {
	return authority.AdjudicateHeartbeat(ctx, lease)
}

func (b *scriptedDualBinding) PresentStaleOperation(ctx context.Context, authority DualAuthority, lease DualLeaseRef) (DualOperationOutcome, error) {
	stale := lease
	stale.FencingToken = dualScenarioDigest("stale" + "\x00" + lease.LeaseId)
	stale.Generation = lease.Generation + 1
	return authority.AdjudicateStaleOperation(ctx, stale)
}

// newScriptedDualHarness builds the in-package scripted harness for one
// topology pair.
func newScriptedDualHarness(first, second DualTopology) DualSuiteHarness {
	return DualSuiteHarness{
		First:  first,
		Second: second,
		NewAuthority: func(scenario string) DualAuthority {
			return newScriptedDualAuthority(scenario)
		},
		NewBinding: func(topology DualTopology, scenario string, authority DualAuthority) DualTopologyBinding {
			return &scriptedDualBinding{topology: topology, faults: DualScenarioFaults(scenario)}
		},
		SharedAuthority: func(scenario string) DualAuthority {
			return newScriptedDualAuthority(scenario)
		},
	}
}

// TestDualTraceFormatClosedSets freezes the normalized business trace
// format: closed kind and reason-class enumerations, the five-tuple shape
// and the recorder's fail-closed admission.
func TestDualTraceFormatClosedSets(t *testing.T) {
	for _, kind := range []DualTraceKind{
		DualEventClaimAccepted, DualEventClaimRejected, DualEventExecStarted,
		DualEventExecFinished, DualEventResultAdmitted, DualEventResultQuarantined,
		DualEventLeaseExpired, DualEventLeaseRevoked, DualEventFencingViolationBlocked,
	} {
		if err := kind.Validate(); err != nil {
			t.Fatalf("closed kind %q must validate: %v", string(kind), err)
		}
	}
	if err := DualTraceKind("wire-step").Validate(); err == nil {
		t.Fatal("a wire-trace kind must never enter the closed business enumeration")
	}
	for _, class := range []DualReasonClass{
		DualReasonNone, DualReasonIneligible, DualReasonRevoked, DualReasonExpired,
		DualReasonIncompatible, DualReasonSuperseded, DualReasonEvidenceRevoked,
		DualReasonDuplicateClaim, DualReasonLateResult, DualReasonFencing, DualReasonDeadline,
	} {
		if err := class.Validate(); err != nil {
			t.Fatalf("closed reason class %q must validate: %v", string(class), err)
		}
	}
	if err := DualReasonClass("transport-error").Validate(); err == nil {
		t.Fatal("a transport-level reason must never enter the closed reason-class enumeration")
	}
	event := DualTraceEvent{Kind: DualEventClaimAccepted, AttemptId: "attempt-1", LeaseId: "lease-1"}
	if err := event.Validate(); err != nil {
		t.Fatalf("a well-formed event must validate: %v", err)
	}
	recorder := &DualTraceRecorder{}
	if err := recorder.Record(DualTraceEvent{Kind: DualTraceKind("bogus"), AttemptId: "attempt-1"}); err == nil {
		t.Fatal("the recorder must reject a malformed event fail closed")
	}
	if err := recorder.Record(DualTraceEvent{Kind: DualEventClaimAccepted, AttemptId: ""}); err == nil {
		t.Fatal("the recorder must reject an event without an attemptId")
	}
}

// TestDualTraceComparatorEquivalence freezes the comparator semantics:
// grouping by (attemptId, leaseId), kind+reasonClass multiset equality,
// order/interleaving ignorance, and digest participation restricted to
// result-admitted and exec-finished.
func TestDualTraceComparatorEquivalence(t *testing.T) {
	base := []DualTraceEvent{
		{Kind: DualEventClaimAccepted, AttemptId: "attempt-1", LeaseId: "lease-1"},
		{Kind: DualEventExecStarted, AttemptId: "attempt-1", LeaseId: "lease-1"},
		{Kind: DualEventExecFinished, AttemptId: "attempt-1", LeaseId: "lease-1", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Kind: DualEventResultAdmitted, AttemptId: "attempt-1", LeaseId: "lease-1", Digest: "sha256:" + strings.Repeat("b", 64)},
	}
	interleaved := []DualTraceEvent{
		{Kind: DualEventResultAdmitted, AttemptId: "attempt-1", LeaseId: "lease-1", Digest: "sha256:" + strings.Repeat("b", 64)},
		{Kind: DualEventClaimAccepted, AttemptId: "attempt-1", LeaseId: "lease-1"},
		{Kind: DualEventExecFinished, AttemptId: "attempt-1", LeaseId: "lease-1", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Kind: DualEventExecStarted, AttemptId: "attempt-1", LeaseId: "lease-1"},
	}
	if !EquivalentDualTraces(base, interleaved) {
		t.Fatal("interleaving order must never break trace equivalence")
	}
	if explanation := ExplainDualTraceDifference(base, interleaved); explanation != "" {
		t.Fatalf("equivalent traces must carry no difference explanation, got %q", explanation)
	}

	// The digest participates only on result-admitted and exec-finished.
	digestElsewhere := append([]DualTraceEvent(nil), base...)
	digestElsewhere[0] = DualTraceEvent{Kind: DualEventClaimAccepted, AttemptId: "attempt-1", LeaseId: "lease-1", Digest: "sha256:" + strings.Repeat("c", 64)}
	if !EquivalentDualTraces(base, digestElsewhere) {
		t.Fatal("a digest on an event outside the digest kinds must not participate in comparison")
	}
	digestChanged := append([]DualTraceEvent(nil), base...)
	digestChanged[2] = DualTraceEvent{Kind: DualEventExecFinished, AttemptId: "attempt-1", LeaseId: "lease-1", Digest: "sha256:" + strings.Repeat("d", 64)}
	if EquivalentDualTraces(base, digestChanged) {
		t.Fatal("a differing exec-finished digest must break trace equivalence")
	}

	// Multiset semantics: a missing event breaks equivalence.
	truncated := base[:3]
	if EquivalentDualTraces(base, truncated) {
		t.Fatal("a missing event must break trace equivalence")
	}
	if explanation := ExplainDualTraceDifference(base, truncated); explanation == "" {
		t.Fatal("a differing trace must carry a deterministic difference explanation")
	}

	// Grouping: identical events under a different (attemptId, leaseId)
	// group are not equivalent.
	regrouped := append([]DualTraceEvent(nil), base...)
	regrouped[0] = DualTraceEvent{Kind: DualEventClaimAccepted, AttemptId: "attempt-2", LeaseId: "lease-1"}
	if EquivalentDualTraces(base, regrouped) {
		t.Fatal("events regrouped under a different attemptId must break trace equivalence")
	}

	// Reason-class differences break equivalence.
	reclassed := append([]DualTraceEvent(nil), base...)
	reclassed[3] = DualTraceEvent{Kind: DualEventResultQuarantined, AttemptId: "attempt-1", LeaseId: "lease-1", ReasonClass: DualReasonLateResult}
	if EquivalentDualTraces(base, reclassed) {
		t.Fatal("a differing kind/reasonClass must break trace equivalence")
	}
}

// TestDualBusinessInvariantsCatchViolations freezes that each business
// invariant assertion catches its crafted violation.
func TestDualBusinessInvariantsCatchViolations(t *testing.T) {
	clean := []DualTraceEvent{
		{Kind: DualEventClaimAccepted, AttemptId: "attempt-1", LeaseId: "lease-1"},
		{Kind: DualEventExecStarted, AttemptId: "attempt-1", LeaseId: "lease-1"},
		{Kind: DualEventExecFinished, AttemptId: "attempt-1", LeaseId: "lease-1", Digest: "sha256:" + strings.Repeat("a", 64)},
		{Kind: DualEventResultAdmitted, AttemptId: "attempt-1", LeaseId: "lease-1", Digest: "sha256:" + strings.Repeat("b", 64)},
	}
	if violations := AssertDualBusinessInvariants(clean); len(violations) != 0 {
		t.Fatalf("a clean trace must satisfy every invariant, got %+v", violations)
	}

	cases := []struct {
		name      string
		trace     []DualTraceEvent
		invariant DualInvariantId
	}{
		{
			name: "duplicate accepted claims of one attempt",
			trace: []DualTraceEvent{
				{Kind: DualEventClaimAccepted, AttemptId: "attempt-1", LeaseId: "lease-1"},
				{Kind: DualEventClaimAccepted, AttemptId: "attempt-1", LeaseId: "lease-2"},
			},
			invariant: DualInvariantUniqueClaim,
		},
		{
			name: "result admitted after the lease expired",
			trace: []DualTraceEvent{
				{Kind: DualEventClaimAccepted, AttemptId: "attempt-1", LeaseId: "lease-1"},
				{Kind: DualEventLeaseExpired, AttemptId: "attempt-1", LeaseId: "lease-1", ReasonClass: DualReasonDeadline},
				{Kind: DualEventResultAdmitted, AttemptId: "attempt-1", LeaseId: "lease-1"},
			},
			invariant: DualInvariantLateResultQuarantine,
		},
		{
			name: "two overlapping executions on one lease",
			trace: []DualTraceEvent{
				{Kind: DualEventClaimAccepted, AttemptId: "attempt-1", LeaseId: "lease-1"},
				{Kind: DualEventExecStarted, AttemptId: "attempt-1", LeaseId: "lease-1"},
				{Kind: DualEventExecStarted, AttemptId: "attempt-1", LeaseId: "lease-1"},
				{Kind: DualEventExecFinished, AttemptId: "attempt-1", LeaseId: "lease-1"},
				{Kind: DualEventExecFinished, AttemptId: "attempt-1", LeaseId: "lease-1"},
			},
			invariant: DualInvariantSingleActiveExecution,
		},
		{
			name: "fencing block without the fencing class",
			trace: []DualTraceEvent{
				{Kind: DualEventFencingViolationBlocked, AttemptId: "attempt-1", LeaseId: "lease-1", ReasonClass: DualReasonIneligible},
			},
			invariant: DualInvariantFencing,
		},
		{
			name: "lease expired without the deadline class",
			trace: []DualTraceEvent{
				{Kind: DualEventLeaseExpired, AttemptId: "attempt-1", LeaseId: "lease-1", ReasonClass: DualReasonRevoked},
			},
			invariant: DualInvariantDeadline,
		},
		{
			name: "execution accepted after the lease revoked",
			trace: []DualTraceEvent{
				{Kind: DualEventClaimAccepted, AttemptId: "attempt-1", LeaseId: "lease-1"},
				{Kind: DualEventLeaseRevoked, AttemptId: "attempt-1", LeaseId: "lease-1", ReasonClass: DualReasonRevoked},
				{Kind: DualEventExecStarted, AttemptId: "attempt-1", LeaseId: "lease-1"},
			},
			invariant: DualInvariantLedgerEligibility,
		},
		{
			name: "claim accepted carrying a reason class",
			trace: []DualTraceEvent{
				{Kind: DualEventClaimAccepted, AttemptId: "attempt-1", LeaseId: "lease-1", ReasonClass: DualReasonIneligible},
			},
			invariant: DualInvariantLedgerEligibility,
		},
		{
			name: "malformed event kind",
			trace: []DualTraceEvent{
				{Kind: DualTraceKind("wire-step"), AttemptId: "attempt-1", LeaseId: "lease-1"},
			},
			invariant: DualInvariantTraceFormat,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			violations := AssertDualBusinessInvariants(testCase.trace)
			for _, violation := range violations {
				if violation.Invariant == testCase.invariant {
					return
				}
			}
			t.Fatalf("the %s invariant was not reported, got %+v", string(testCase.invariant), violations)
		})
	}
}

// TestDualTopologySuiteScriptedEquivalence runs the frozen scenario matrix
// under the scripted push and pull parameterizations and freezes that every
// scenario passes with outcome/invariant equivalent traces.
func TestDualTopologySuiteScriptedEquivalence(t *testing.T) {
	pairs := []struct {
		first, second DualTopology
	}{
		{TopologyPush, TopologyPull},
		{TopologyEmbedded, TopologyPush},
		{TopologyEmbedded, TopologyPull},
	}
	for _, pair := range pairs {
		t.Run(string(pair.first)+"-vs-"+string(pair.second), func(t *testing.T) {
			verdicts, err := RunDualTopologySuite(context.Background(), newScriptedDualHarness(pair.first, pair.second))
			if err != nil {
				t.Fatalf("RunDualTopologySuite: %v", err)
			}
			if len(verdicts) != len(DualScenarios()) {
				t.Fatalf("the suite must run the frozen scenario matrix once, got %d verdicts for %d scenarios", len(verdicts), len(DualScenarios()))
			}
			for _, verdict := range verdicts {
				if !verdict.Passed {
					t.Fatalf("scenario %s failed under the scripted %s/%s parameterization: %s (first: %s; second: %s)",
						verdict.Scenario, string(pair.first), string(pair.second), verdict.Reason,
						dualRunReason(verdict.First), dualRunReason(verdict.Second))
				}
				if verdict.Scenario != DualScenarioCrossTopologyUniqueClaim && !verdict.Equivalent {
					t.Fatalf("scenario %s produced divergent topology traces: %s", verdict.Scenario, verdict.Reason)
				}
			}
		})
	}
}

// dishonestLateAdmitAuthority admits late results on purpose: the suite
// must catch the invariant violation and fail the scenario.
type dishonestLateAdmitAuthority struct {
	*scriptedDualAuthority
}

func (a *dishonestLateAdmitAuthority) AdjudicateResult(ctx context.Context, lease DualLeaseRef, commandId, resultDigest string) (DualOperationOutcome, error) {
	a.mu.Lock()
	record, ok := a.leases[lease.LeaseId]
	if ok && record.terminal {
		a.record(DualTraceEvent{Kind: DualEventResultAdmitted, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId, Digest: resultDigest})
		a.mu.Unlock()
		return DualOperationOutcome{Accepted: true}, nil
	}
	a.mu.Unlock()
	return a.scriptedDualAuthority.AdjudicateResult(ctx, lease, commandId, resultDigest)
}

// TestDualTopologySuiteCatchesDishonestAuthority freezes that the suite
// fails a topology whose authority admits late results: the
// late-result-quarantine and ledger-eligibility invariants report the
// violation.
func TestDualTopologySuiteCatchesDishonestAuthority(t *testing.T) {
	harness := newScriptedDualHarness(TopologyPush, TopologyPull)
	harness.NewAuthority = func(scenario string) DualAuthority {
		if scenario == DualScenarioLateResult {
			return &dishonestLateAdmitAuthority{scriptedDualAuthority: newScriptedDualAuthority(scenario)}
		}
		return newScriptedDualAuthority(scenario)
	}
	verdicts, err := RunDualTopologySuite(context.Background(), harness)
	if err != nil {
		t.Fatalf("RunDualTopologySuite: %v", err)
	}
	for _, verdict := range verdicts {
		if verdict.Scenario != DualScenarioLateResult {
			continue
		}
		if verdict.Passed {
			t.Fatal("the suite must fail a topology that admits late results")
		}
		return
	}
	t.Fatal("the late-result scenario was not run")
}

// dishonestDualStartAuthority records a second overlapping exec-started
// event on purpose: the suite must catch the single-active violation.
type dishonestDualStartAuthority struct {
	*scriptedDualAuthority
}

func (a *dishonestDualStartAuthority) RecordExecutionStarted(ctx context.Context, lease DualLeaseRef, commandId, executionId string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, ok := a.leases[lease.LeaseId]
	if !ok || record.terminal {
		return ErrInvalidRequest
	}
	record.openCommand = commandId
	a.record(DualTraceEvent{Kind: DualEventExecStarted, AttemptId: lease.AttemptId, LeaseId: lease.LeaseId})
	return nil
}

// doubleStartBinding starts two overlapping executions on purpose: the
// suite must catch the single-active violation.
type doubleStartBinding struct {
	*scriptedDualBinding
}

func (b *doubleStartBinding) StartExecution(ctx context.Context, authority DualAuthority, lease DualLeaseRef, commandId string) (string, DualOperationOutcome, error) {
	executionId, outcome, err := b.scriptedDualBinding.StartExecution(ctx, authority, lease, commandId)
	if err != nil || !outcome.Accepted {
		return executionId, outcome, err
	}
	// Dishonest second start overlapping the open execution.
	if err := authority.RecordExecutionStarted(ctx, lease, commandId+"-overlap", executionId+"-overlap"); err != nil {
		return "", DualOperationOutcome{}, err
	}
	return executionId, outcome, nil
}

// TestDualTopologySuiteCatchesDualActiveViolation freezes that the suite
// fails a topology that opens two overlapping executions on one lease.
func TestDualTopologySuiteCatchesDualActiveViolation(t *testing.T) {
	harness := newScriptedDualHarness(TopologyPush, TopologyPull)
	harness.NewAuthority = func(scenario string) DualAuthority {
		if scenario == DualScenarioSingleActiveExecution {
			return &dishonestDualStartAuthority{scriptedDualAuthority: newScriptedDualAuthority(scenario)}
		}
		return newScriptedDualAuthority(scenario)
	}
	harness.NewBinding = func(topology DualTopology, scenario string, authority DualAuthority) DualTopologyBinding {
		binding := &scriptedDualBinding{topology: topology, faults: DualScenarioFaults(scenario)}
		if scenario == DualScenarioSingleActiveExecution && topology == TopologyPull {
			return &doubleStartBinding{scriptedDualBinding: binding}
		}
		return binding
	}
	verdicts, err := RunDualTopologySuite(context.Background(), harness)
	if err != nil {
		t.Fatalf("RunDualTopologySuite: %v", err)
	}
	for _, verdict := range verdicts {
		if verdict.Scenario != DualScenarioSingleActiveExecution {
			continue
		}
		if verdict.Passed {
			t.Fatal("the suite must fail a topology that opens overlapping executions")
		}
		if len(verdict.Second.InvariantViolations) == 0 {
			t.Fatal("the single-active-execution invariant must be reported on the dishonest topology run")
		}
		return
	}
	t.Fatal("the single-active scenario was not run")
}

// TestDualReasonForCancelReasonMapping freezes the deterministic mapping of
// every machine-readable cancel reason onto the closed reason-class set.
func TestDualReasonForCancelReasonMapping(t *testing.T) {
	cases := map[dispatch.CancelReason]DualReasonClass{
		dispatch.CancelReasonSecurityCriticalRevoke:   DualReasonRevoked,
		dispatch.CancelReasonRegistrationExpired:      DualReasonExpired,
		dispatch.CancelReasonRegistrationIncompatible: DualReasonIncompatible,
		dispatch.CancelReasonSnapshotSuperseded:       DualReasonSuperseded,
		dispatch.CancelReasonSnapshotExpired:          DualReasonSuperseded,
		dispatch.CancelReasonEvidenceRevoked:          DualReasonEvidenceRevoked,
		dispatch.CancelReasonEvidenceExpired:          DualReasonEvidenceRevoked,
		dispatch.CancelReasonDeadlineExceeded:         DualReasonDeadline,
	}
	for reason, want := range cases {
		got := DualReasonForCancelReason(reason)
		if got != want {
			t.Fatalf("cancel reason %s maps to %q, want %q", string(reason), string(got), string(want))
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("the mapped reason class %q must belong to the closed set: %v", string(got), err)
		}
	}
}

// TestDualScenarioFaultsAdditive freezes that the fault parameterization is
// additive over the existing fault kinds and never injects outside the
// fault scenarios.
func TestDualScenarioFaultsAdditive(t *testing.T) {
	for _, scenario := range DualScenarios() {
		specs := DualScenarioFaults(scenario)
		switch scenario {
		case DualScenarioFaultDelayExec, DualScenarioFaultRejectExec, DualScenarioFaultDropProvisionResponse:
			if len(specs) == 0 {
				t.Fatalf("fault scenario %s must inject faults", scenario)
			}
			for _, spec := range specs {
				switch spec.Fault {
				case FaultDelay, FaultReject, FaultDropResponse:
				default:
					t.Fatalf("fault scenario %s injects an unexpected fault kind %q", scenario, string(spec.Fault))
				}
			}
		default:
			if len(specs) != 0 {
				t.Fatalf("non-fault scenario %s must not inject faults, got %+v", scenario, specs)
			}
		}
	}
}

// TestDualScenarioClaimRequestDeterministic freezes the deterministic
// scenario claim derivation.
func TestDualScenarioClaimRequestDeterministic(t *testing.T) {
	first := dualScenarioClaimRequest(DualScenarioHappyPath)
	second := dualScenarioClaimRequest(DualScenarioHappyPath)
	if first != second {
		t.Fatal("identical scenarios must derive identical claim requests")
	}
	other := dualScenarioClaimRequest(DualScenarioLateResult)
	if first.AttemptId == other.AttemptId {
		t.Fatal("distinct scenarios must derive distinct attempts")
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("the scenario claim request must validate: %v", err)
	}
}
