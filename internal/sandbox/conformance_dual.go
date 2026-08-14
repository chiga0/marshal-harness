package sandbox

// conformance_dual.go freezes the M9-d dual-topology conformance layer
// (docs/m9-vertical-to-server-design.md §3.4, ADR 0018 §2/§16, ADR 0017
// §7): embedded/in-process, Push HTTP and Pull outbound runner are
// transport/topology adapters of the identical dispatch-bound Port inside
// one versioned protocol family, and the Port semantics never change with
// the transport. The adjudication criterion is outcome/invariant
// equivalence: the suite compares normalized business traces and business
// invariants, never per-step wire traces, and allows topology-specific
// offer/poll/claim/ack transitions and timing.
//
// The normalized business trace format is frozen before any adapter
// implementation: every event is the five-tuple {kind, attemptId, leaseId,
// digest, reasonClass}; kind and reasonClass are closed enumerations; two
// topology traces are equivalent when, grouped by (attemptId, leaseId),
// their kind+reasonClass multisets are equal — timestamps, interleaving
// order and wire-level detail are ignored; the digest participates only on
// result-admitted and exec-finished events.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// DualTopology is the closed enumeration of the transport/topology adapters
// of the dispatch-bound protocol family (ADR 0018 §2).
type DualTopology string

// Closed members of DualTopology.
const (
	TopologyEmbedded DualTopology = "embedded"
	TopologyPush     DualTopology = "push"
	TopologyPull     DualTopology = "pull"
)

// Validate rejects every value outside the closed enumeration.
func (topology DualTopology) Validate() error {
	switch topology {
	case TopologyEmbedded, TopologyPush, TopologyPull:
		return nil
	default:
		return fmt.Errorf("sandbox: dual topology: unknown topology %q", string(topology))
	}
}

// DualTraceKind is the closed enumeration of normalized business trace
// event kinds. Matching is case sensitive.
type DualTraceKind string

// Closed event kinds of the normalized dual-topology business trace.
const (
	DualEventClaimAccepted           DualTraceKind = "claim-accepted"
	DualEventClaimRejected           DualTraceKind = "claim-rejected"
	DualEventExecStarted             DualTraceKind = "exec-started"
	DualEventExecFinished            DualTraceKind = "exec-finished"
	DualEventResultAdmitted          DualTraceKind = "result-admitted"
	DualEventResultQuarantined       DualTraceKind = "result-quarantined"
	DualEventLeaseExpired            DualTraceKind = "lease-expired"
	DualEventLeaseRevoked            DualTraceKind = "lease-revoked"
	DualEventFencingViolationBlocked DualTraceKind = "fencing-violation-blocked"
)

// Validate rejects every value outside the closed enumeration.
func (kind DualTraceKind) Validate() error {
	switch kind {
	case DualEventClaimAccepted, DualEventClaimRejected, DualEventExecStarted,
		DualEventExecFinished, DualEventResultAdmitted, DualEventResultQuarantined,
		DualEventLeaseExpired, DualEventLeaseRevoked, DualEventFencingViolationBlocked:
		return nil
	default:
		return fmt.Errorf("sandbox: dual trace: unknown event kind %q", string(kind))
	}
}

// DualReasonClass is the closed enumeration of business reason classes of
// the normalized dual-topology trace. Matching is case sensitive.
type DualReasonClass string

// Closed reason classes of the normalized dual-topology business trace.
// DualReasonNone (the empty string) is the absence marker carried by
// accepted outcomes; it is never a rejection class.
const (
	DualReasonNone            DualReasonClass = ""
	DualReasonIneligible      DualReasonClass = "ineligible"
	DualReasonRevoked         DualReasonClass = "revoked"
	DualReasonExpired         DualReasonClass = "expired"
	DualReasonIncompatible    DualReasonClass = "incompatible"
	DualReasonSuperseded      DualReasonClass = "superseded"
	DualReasonEvidenceRevoked DualReasonClass = "evidence-revoked"
	DualReasonDuplicateClaim  DualReasonClass = "duplicate-claim"
	DualReasonLateResult      DualReasonClass = "late-result"
	DualReasonFencing         DualReasonClass = "fencing"
	DualReasonDeadline        DualReasonClass = "deadline"
)

// Validate rejects every value outside the closed enumeration (the empty
// absence marker is a member).
func (class DualReasonClass) Validate() error {
	switch class {
	case DualReasonNone, DualReasonIneligible, DualReasonRevoked, DualReasonExpired,
		DualReasonIncompatible, DualReasonSuperseded, DualReasonEvidenceRevoked,
		DualReasonDuplicateClaim, DualReasonLateResult, DualReasonFencing,
		DualReasonDeadline:
		return nil
	default:
		return fmt.Errorf("sandbox: dual trace: unknown reason class %q", string(class))
	}
}

// DualTraceEvent is one frozen normalized business trace event: the
// {kind, attemptId, leaseId, digest, reasonClass} five-tuple. Timestamps,
// interleaving order and wire-level detail never participate: the trace is
// a business event sequence, never a wire trace.
type DualTraceEvent struct {
	Kind        DualTraceKind   `json:"kind"`
	AttemptId   string          `json:"attemptId"`
	LeaseId     string          `json:"leaseId"`
	Digest      string          `json:"digest"`
	ReasonClass DualReasonClass `json:"reasonClass"`
}

// Validate fails closed on any malformed field: the kind and the reason
// class must belong to the closed enumerations, the attemptId must be
// non-empty, and the digest — when present — must be a sha256 digest. The
// leaseId may be empty on events recorded before any lease exists (a
// rejected claim without an issued lease).
func (event DualTraceEvent) Validate() error {
	if err := event.Kind.Validate(); err != nil {
		return err
	}
	if err := event.ReasonClass.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(event.AttemptId) == "" {
		return fmt.Errorf("sandbox: dual trace: attemptId must be a non-empty string")
	}
	if event.Digest != "" && !strings.HasPrefix(event.Digest, DigestPrefix) {
		return fmt.Errorf("sandbox: dual trace: digest must carry the %s prefix", DigestPrefix)
	}
	return nil
}

// dualTraceDigestKinds are the only event kinds whose digest participates
// in trace comparison: result-admitted and exec-finished.
func dualTraceDigestKinds(kind DualTraceKind) bool {
	return kind == DualEventResultAdmitted || kind == DualEventExecFinished
}

// dualTraceGroupKey is the comparator grouping identity of one event.
func dualTraceGroupKey(attemptId, leaseId string) string {
	return attemptId + "\x00" + leaseId
}

// dualTraceEventKey is the multiset identity of one event inside its
// (attemptId, leaseId) group: kind + reasonClass always, the digest only on
// result-admitted and exec-finished.
func dualTraceEventKey(event DualTraceEvent) string {
	key := string(event.Kind) + "\x00" + string(event.ReasonClass)
	if dualTraceDigestKinds(event.Kind) {
		key += "\x00" + event.Digest
	}
	return key
}

// dualTraceMultisets reduces one trace to the per-group event multisets the
// comparator adjudicates.
func dualTraceMultisets(events []DualTraceEvent) map[string]map[string]int {
	groups := make(map[string]map[string]int)
	for _, event := range events {
		group := dualTraceGroupKey(event.AttemptId, event.LeaseId)
		multiset, ok := groups[group]
		if !ok {
			multiset = map[string]int{}
			groups[group] = multiset
		}
		multiset[dualTraceEventKey(event)]++
	}
	return groups
}

// EquivalentDualTraces adjudicates the frozen outcome/invariant equivalence
// of two topology traces: grouped by (attemptId, leaseId), the
// kind+reasonClass multisets must be equal, with the digest participating
// only on result-admitted and exec-finished events. Timestamps, event
// interleaving order and wire-level detail are ignored.
func EquivalentDualTraces(first, second []DualTraceEvent) bool {
	firstGroups := dualTraceMultisets(first)
	secondGroups := dualTraceMultisets(second)
	if len(firstGroups) != len(secondGroups) {
		return false
	}
	for group, firstMultiset := range firstGroups {
		secondMultiset, ok := secondGroups[group]
		if !ok {
			return false
		}
		if len(firstMultiset) != len(secondMultiset) {
			return false
		}
		for key, count := range firstMultiset {
			if secondMultiset[key] != count {
				return false
			}
		}
	}
	return true
}

// ExplainDualTraceDifference describes the first detected difference
// between two traces under the frozen comparator semantics, or returns the
// empty string when the traces are equivalent. The explanation is
// deterministic: groups and keys are visited in sorted order.
func ExplainDualTraceDifference(first, second []DualTraceEvent) string {
	if EquivalentDualTraces(first, second) {
		return ""
	}
	firstGroups := dualTraceMultisets(first)
	secondGroups := dualTraceMultisets(second)
	groups := make([]string, 0, len(firstGroups)+len(secondGroups))
	seen := map[string]struct{}{}
	for group := range firstGroups {
		if _, ok := seen[group]; !ok {
			seen[group] = struct{}{}
			groups = append(groups, group)
		}
	}
	for group := range secondGroups {
		if _, ok := seen[group]; !ok {
			seen[group] = struct{}{}
			groups = append(groups, group)
		}
	}
	sort.Strings(groups)
	for _, group := range groups {
		parts := strings.SplitN(group, "\x00", 2)
		attemptId, leaseId := parts[0], ""
		if len(parts) == 2 {
			leaseId = parts[1]
		}
		firstMultiset := firstGroups[group]
		secondMultiset := secondGroups[group]
		keys := map[string]struct{}{}
		for key := range firstMultiset {
			keys[key] = struct{}{}
		}
		for key := range secondMultiset {
			keys[key] = struct{}{}
		}
		sortedKeys := make([]string, 0, len(keys))
		for key := range keys {
			sortedKeys = append(sortedKeys, key)
		}
		sort.Strings(sortedKeys)
		for _, key := range sortedKeys {
			if firstMultiset[key] != secondMultiset[key] {
				return fmt.Sprintf("group (attemptId=%s, leaseId=%s): event %q count %d != %d",
					attemptId, leaseId, strings.ReplaceAll(key, "\x00", "|"), firstMultiset[key], secondMultiset[key])
			}
		}
	}
	return "sandbox: dual trace: traces differ in group composition"
}

// DualReasonForCancelReason maps the machine-readable dispatch cancel
// reasons onto the frozen closed reason-class set. The closed reason-class
// set is coarser than the cancel-reason set: both snapshot expiry classes
// converge on the superseded class (the lease no longer binds the current
// snapshot lineage) and both evidence failure classes converge on the
// evidence-revoked class (the closed set carries no evidence-expired
// member). The mapping is deterministic and shared by every topology
// adapter, so the identical invalidation fact always records the identical
// reason class under every topology.
func DualReasonForCancelReason(reason dispatch.CancelReason) DualReasonClass {
	switch reason {
	case dispatch.CancelReasonSecurityCriticalRevoke:
		return DualReasonRevoked
	case dispatch.CancelReasonRegistrationExpired:
		return DualReasonExpired
	case dispatch.CancelReasonRegistrationIncompatible:
		return DualReasonIncompatible
	case dispatch.CancelReasonSnapshotSuperseded, dispatch.CancelReasonSnapshotExpired:
		return DualReasonSuperseded
	case dispatch.CancelReasonEvidenceRevoked, dispatch.CancelReasonEvidenceExpired:
		return DualReasonEvidenceRevoked
	case dispatch.CancelReasonDeadlineExceeded:
		return DualReasonDeadline
	default:
		return DualReasonIneligible
	}
}

// DualTraceRecorder appends normalized business trace events in emission
// order and hands out deterministic copies; the recorded sequence is the
// sole adjudication input of the invariant assertions.
type DualTraceRecorder struct {
	mu     sync.Mutex
	events []DualTraceEvent
}

// Record appends one validated business event; malformed events fail closed
// and are never recorded.
func (recorder *DualTraceRecorder) Record(event DualTraceEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
	return nil
}

// Events returns a copy of the recorded sequence in emission order.
func (recorder *DualTraceRecorder) Events() []DualTraceEvent {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]DualTraceEvent(nil), recorder.events...)
}

// DualClaimRequest carries one capability requirement claim through one
// topology binding of the dispatch-bound Port.
type DualClaimRequest struct {
	TaskId       string
	RunId        string
	AttemptId    string
	AllocationId string
	WorkloadRole WorkloadRole
	Principal    string
	Requirements domain.SandboxRequirements
}

// Validate fails closed on any missing identity field.
func (request DualClaimRequest) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"taskId", request.TaskId},
		{"runId", request.RunId},
		{"attemptId", request.AttemptId},
		{"allocationId", request.AllocationId},
		{"principal", request.Principal},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("sandbox: dual claim: %s must be a non-empty string", field.name)
		}
	}
	if err := request.WorkloadRole.Validate(); err != nil {
		return fmt.Errorf("sandbox: dual claim: %w", err)
	}
	if _, err := domain.ParseAccessMode(string(request.Requirements.AccessMode)); err != nil {
		return fmt.Errorf("sandbox: dual claim: requirements: %w", err)
	}
	if _, err := domain.ParseAssuranceLevel(string(request.Requirements.MinimumAssuranceLevel)); err != nil {
		return fmt.Errorf("sandbox: dual claim: requirements: %w", err)
	}
	return nil
}

// DualLeaseRef is the lease identity binding that flows through the dual
// scenarios: the lease locator plus the generation/fencingToken the
// fencing guard adjudicates and the deadline window the deadline semantics
// adjudicate.
type DualLeaseRef struct {
	LeaseId       string
	TaskId        string
	RunId         string
	AttemptId     string
	AllocationId  string
	Generation    int64
	FencingToken  string
	AckDeadlineAt string
	ExpiresAt     string
}

// DualOperationOutcome is the adjudicated outcome of one business
// operation: accepted or rejected with a closed reason class. The reason
// class of an accepted operation is always the absence marker.
type DualOperationOutcome struct {
	Accepted    bool
	ReasonClass DualReasonClass
	Detail      string
}

// DualClaimReceipt observes one claim attempt: the adjudicated lease
// reference (zero when the adjudication itself rejected before any lease
// existed) and the operation outcome.
type DualClaimReceipt struct {
	Lease   DualLeaseRef
	Outcome DualOperationOutcome
}

// DualInvalidationKind is the closed enumeration of post-claim
// invalidation facts the suite drives through the authority (ADR 0018 §7:
// revoke/expire/incompatible/supersede/evidence revoke).
type DualInvalidationKind string

// Closed members of DualInvalidationKind.
const (
	DualInvalidateRegistrationRevoke       DualInvalidationKind = "registration-revoke"
	DualInvalidateRegistrationExpire       DualInvalidationKind = "registration-expire"
	DualInvalidateRegistrationIncompatible DualInvalidationKind = "registration-incompatible"
	DualInvalidateSnapshotSupersede        DualInvalidationKind = "snapshot-supersede"
	DualInvalidateEvidenceRevoke           DualInvalidationKind = "evidence-revoke"
)

// Validate rejects every value outside the closed enumeration.
func (kind DualInvalidationKind) Validate() error {
	switch kind {
	case DualInvalidateRegistrationRevoke, DualInvalidateRegistrationExpire,
		DualInvalidateRegistrationIncompatible, DualInvalidateSnapshotSupersede,
		DualInvalidateEvidenceRevoke:
		return nil
	default:
		return fmt.Errorf("sandbox: dual invalidation: unknown kind %q", string(kind))
	}
}

// DualAuthority is the authority-side seam of one dual-topology scenario
// run: the current ledger every claim, heartbeat, execution and result
// admission rechecks against. Every business trace event is recorded by the
// authority adjudication — the topology bindings contribute transport
// transitions only, never business events — so the identical adjudication
// code path records the identical business trace under every topology.
type DualAuthority interface {
	// AdjudicateClaim adjudicates one capability requirement claim fail
	// closed against the current authority ledger. On rejection it records
	// the claim-rejected event with the closed reason class; on acceptance
	// it issues the lease and records nothing — the claim becomes accepted
	// only when the topology completes its offer/ack transitions through
	// CompleteClaim.
	AdjudicateClaim(ctx context.Context, request DualClaimRequest) (DualLeaseRef, DualOperationOutcome, error)

	// CompleteClaim completes the topology's offer/ack transitions of one
	// issued claim and records the claim-accepted event.
	CompleteClaim(ctx context.Context, lease DualLeaseRef) error

	// AdjudicateExecutionStart rechecks the current ledger (lease state,
	// fencing, eligibility) for one execution start and reserves the
	// single-active execution slot. It records no event: the exec-started
	// event is recorded by RecordExecutionStarted only after the transport
	// handoff actually started the execution.
	AdjudicateExecutionStart(ctx context.Context, lease DualLeaseRef, commandId string) (string, DualOperationOutcome, error)

	// RecordExecutionStarted records the exec-started event and opens the
	// execution slot of one successfully handed-off execution.
	RecordExecutionStarted(ctx context.Context, lease DualLeaseRef, commandId, executionId string) error

	// RecordExecutionFinished records the exec-finished event carrying the
	// deterministic execution digest and closes the execution slot.
	RecordExecutionFinished(ctx context.Context, lease DualLeaseRef, commandId, executionId string) (string, error)

	// AdjudicateResult admits or quarantines one submitted result against
	// the current ledger and records result-admitted or
	// result-quarantined with the closed reason class.
	AdjudicateResult(ctx context.Context, lease DualLeaseRef, commandId, resultDigest string) (DualOperationOutcome, error)

	// AdjudicateHeartbeat rechecks one in-flight lease against the current
	// ledger. A passing heartbeat records no business event (heartbeats are
	// deadline transitions); a failing heartbeat cancels or expires the
	// lease and records the terminal event with the closed reason class.
	AdjudicateHeartbeat(ctx context.Context, lease DualLeaseRef) (DualOperationOutcome, error)

	// AdjudicateStaleOperation presents one operation carrying a stale
	// fencingToken and records the fencing-violation-blocked event with the
	// fencing reason class.
	AdjudicateStaleOperation(ctx context.Context, lease DualLeaseRef) (DualOperationOutcome, error)

	// Invalidate applies one post-claim invalidation fact to the authority:
	// every in-flight lease loses eligibility immediately, is cancelled or
	// expired with the machine-readable reason, and the lease-revoked or
	// lease-expired event is recorded with the mapped closed reason class.
	Invalidate(ctx context.Context, kind DualInvalidationKind) error

	// MissAckDeadline advances the scenario past the claim's ack deadline
	// without an ack and expires the lease, recording lease-expired with
	// the deadline reason class (deadline semantics: the ack state).
	MissAckDeadline(ctx context.Context, lease DualLeaseRef) error

	// ExpireLeaseWindow advances the scenario past the lease expiry window
	// and expires the lease, recording lease-expired with the deadline
	// reason class (deadline semantics: the expiry state).
	ExpireLeaseWindow(ctx context.Context, lease DualLeaseRef) error

	// Reregister installs one fresh eligible registration/snapshot/evidence
	// chain after an invalidation fact: the invalidated registration is
	// terminal and never resurrected, continuation is only possible through
	// a new registration re-matched by a new attempt with a new lease (ADR
	// 0018 §6).
	Reregister(ctx context.Context) error

	// Trace returns the authority's normalized business trace in emission
	// order.
	Trace() []DualTraceEvent
}

// DualTopologyBinding is one transport/topology adapter of the
// dispatch-bound Port under the dual conformance suite. The binding
// contributes topology-specific offer/poll/claim/ack transitions and timing
// only; every business trace event is recorded by the authority
// adjudication, so the Port semantics never change with the transport.
type DualTopologyBinding interface {
	// Topology identifies the transport/topology adapter.
	Topology() DualTopology

	// Claim drives one capability requirement claim through this topology's
	// complete transition sequence: adjudication, offer delivery,
	// provision/ack and completion. A lost provision/ack response leaves
	// the claim unaccepted without recording a claim event — the deadline
	// reconciliation of the unaccepted lease is the scenario's next step.
	Claim(ctx context.Context, authority DualAuthority, request DualClaimRequest) (DualClaimReceipt, error)

	// ClaimUnacked drives one claim through the adjudication and offer
	// delivery without completing the ack (the offer is lost or never
	// acknowledged): the topology-specific transition under the deadline
	// scenarios.
	ClaimUnacked(ctx context.Context, authority DualAuthority, request DualClaimRequest) (DualClaimReceipt, error)

	// StartExecution drives one execution start through this topology's
	// handoff: current-ledger adjudication, transport delivery to the
	// provider and the exec-started recording after the execution actually
	// started.
	StartExecution(ctx context.Context, authority DualAuthority, lease DualLeaseRef, commandId string) (string, DualOperationOutcome, error)

	// FinishExecution records the completion of one started execution and
	// returns the deterministic execution digest.
	FinishExecution(ctx context.Context, authority DualAuthority, lease DualLeaseRef, commandId, executionId string) (string, DualOperationOutcome, error)

	// SubmitResult delivers one finished result for admission through this
	// topology's result path.
	SubmitResult(ctx context.Context, authority DualAuthority, lease DualLeaseRef, commandId, resultDigest string) (DualOperationOutcome, error)

	// Heartbeat delivers one lease heartbeat through this topology's
	// heartbeat path.
	Heartbeat(ctx context.Context, authority DualAuthority, lease DualLeaseRef) (DualOperationOutcome, error)

	// PresentStaleOperation presents one operation carrying a stale
	// fencingToken through this topology.
	PresentStaleOperation(ctx context.Context, authority DualAuthority, lease DualLeaseRef) (DualOperationOutcome, error)
}

// DualExecutionDigest derives the deterministic exec-finished digest of one
// execution: identical inputs always yield the identical digest under every
// topology, so the digest comparison of the comparator is adjudicable.
func DualExecutionDigest(leaseId, commandId, executionId string) string {
	return RecomputeSHA256([]byte("dual-exec" + "\x00" + leaseId + "\x00" + commandId + "\x00" + executionId))
}

// DualInvariantId is the closed enumeration of the business invariants the
// suite asserts on every recorded trace (ADR 0018 §16).
type DualInvariantId string

// Closed business invariants of the dual-topology conformance suite.
const (
	// DualInvariantTraceFormat guards the frozen trace format itself:
	// every event belongs to the closed kind and reason-class
	// enumerations.
	DualInvariantTraceFormat DualInvariantId = "closed-trace-format"
	// DualInvariantUniqueClaim: the identical capability requirement
	// (attemptId) never carries two accepted claims — neither inside one
	// topology nor across topologies sharing one ledger.
	DualInvariantUniqueClaim DualInvariantId = "unique-claim"
	// DualInvariantLedgerEligibility: eligibility is always adjudicated
	// against the current authority ledger — once a lease is revoked or
	// expired, no claim/exec/result of that lease is ever accepted again.
	DualInvariantLedgerEligibility DualInvariantId = "ledger-eligibility"
	// DualInvariantFencing: every operation carrying a stale fencingToken
	// is fencing-violation-blocked with the fencing reason class.
	DualInvariantFencing DualInvariantId = "fencing"
	// DualInvariantDeadline: the ack/heartbeat/expiry deadline states are
	// consistent — every lease-expired event carries the deadline reason
	// class, and after any terminal lease event only fencing blocks,
	// quarantined results and claim rejections follow for that lease.
	DualInvariantDeadline DualInvariantId = "deadline-consistency"
	// DualInvariantSingleActiveExecution: no dual-active execution — the
	// identical lease never carries two overlapping exec-started events
	// without a closing exec-finished between them.
	DualInvariantSingleActiveExecution DualInvariantId = "single-active-execution"
	// DualInvariantLateResultQuarantine: a result arriving after the lease
	// lost eligibility is result-quarantined with the late-result class
	// and never result-admitted.
	DualInvariantLateResultQuarantine DualInvariantId = "late-result-quarantine"
)

// DualInvariantViolation is one failed invariant assertion with the
// deterministic detail of the offending observation.
type DualInvariantViolation struct {
	Invariant DualInvariantId `json:"invariant"`
	Detail    string          `json:"detail"`
}

// dualTraceGroups slices the event sequence into per-group ordered indexes
// for the order-sensitive invariant checks.
func dualTraceGroupSequences(events []DualTraceEvent) map[string][]DualTraceEvent {
	groups := map[string][]DualTraceEvent{}
	for _, event := range events {
		group := dualTraceGroupKey(event.AttemptId, event.LeaseId)
		groups[group] = append(groups[group], event)
	}
	return groups
}

// AssertDualBusinessInvariants asserts the closed business invariants on one
// recorded trace in emission order and returns every violation; an empty
// slice means the trace satisfies all invariants.
func AssertDualBusinessInvariants(events []DualTraceEvent) []DualInvariantViolation {
	var violations []DualInvariantViolation
	report := func(invariant DualInvariantId, format string, args ...any) {
		violations = append(violations, DualInvariantViolation{
			Invariant: invariant,
			Detail:    fmt.Sprintf(format, args...),
		})
	}

	// Closed trace format: every event must validate.
	for index, event := range events {
		if err := event.Validate(); err != nil {
			report(DualInvariantTraceFormat, "events[%d] rejected: %v", index, err)
		}
	}

	// Unique claim: per attemptId, at most one claim-accepted — across
	// topologies combined when the traces of a shared-ledger scenario are
	// asserted together.
	acceptedByAttempt := map[string]int{}
	for _, event := range events {
		if event.Kind == DualEventClaimAccepted {
			acceptedByAttempt[event.AttemptId]++
		}
	}
	attempts := make([]string, 0, len(acceptedByAttempt))
	for attemptId := range acceptedByAttempt {
		attempts = append(attempts, attemptId)
	}
	sort.Strings(attempts)
	for _, attemptId := range attempts {
		if acceptedByAttempt[attemptId] > 1 {
			report(DualInvariantUniqueClaim, "attemptId %q carries %d accepted claims; the identical capability requirement is claimed at most once", attemptId, acceptedByAttempt[attemptId])
		}
	}

	groups := dualTraceGroupSequences(events)
	groupKeys := make([]string, 0, len(groups))
	for group := range groups {
		groupKeys = append(groupKeys, group)
	}
	sort.Strings(groupKeys)

	for _, group := range groupKeys {
		sequence := groups[group]
		terminalIndex := -1
		openExecutions := 0
		for index, event := range sequence {
			// Ledger eligibility and deadline consistency: after the first
			// terminal event of one lease, only fencing blocks, quarantined
			// results and claim rejections may follow.
			if terminalIndex >= 0 && index > terminalIndex {
				switch event.Kind {
				case DualEventFencingViolationBlocked, DualEventResultQuarantined, DualEventClaimRejected:
				default:
					report(DualInvariantLedgerEligibility, "lease group %s: %s follows the terminal event at position %d; eligibility is adjudicated against the current ledger", group, string(event.Kind), terminalIndex)
					report(DualInvariantDeadline, "lease group %s: %s follows the terminal event at position %d; deadline states are inconsistent", group, string(event.Kind), terminalIndex)
				}
			}
			switch event.Kind {
			case DualEventLeaseExpired:
				if event.ReasonClass != DualReasonDeadline {
					report(DualInvariantDeadline, "lease group %s: lease-expired carries reason class %q, want %q", group, string(event.ReasonClass), string(DualReasonDeadline))
				}
				if terminalIndex < 0 {
					terminalIndex = index
				}
			case DualEventLeaseRevoked:
				if terminalIndex < 0 {
					terminalIndex = index
				}
			case DualEventFencingViolationBlocked:
				if event.ReasonClass != DualReasonFencing {
					report(DualInvariantFencing, "lease group %s: fencing-violation-blocked carries reason class %q, want %q", group, string(event.ReasonClass), string(DualReasonFencing))
				}
			case DualEventExecStarted:
				if openExecutions > 0 {
					report(DualInvariantSingleActiveExecution, "lease group %s: a second exec-started overlaps an unclosed execution at position %d", group, index)
				}
				openExecutions++
			case DualEventExecFinished:
				if openExecutions == 0 {
					report(DualInvariantSingleActiveExecution, "lease group %s: exec-finished at position %d closes no open execution", group, index)
				} else {
					openExecutions--
				}
			case DualEventResultAdmitted:
				if terminalIndex >= 0 && index > terminalIndex {
					report(DualInvariantLateResultQuarantine, "lease group %s: result-admitted at position %d arrives after the lease lost eligibility; late results are quarantined, never admitted", group, index)
				}
			case DualEventResultQuarantined:
				if terminalIndex >= 0 && index > terminalIndex &&
					event.ReasonClass != DualReasonLateResult && event.ReasonClass != DualReasonFencing {
					report(DualInvariantLateResultQuarantine, "lease group %s: a quarantined result after the terminal event carries reason class %q, want %q or %q", group, string(event.ReasonClass), string(DualReasonLateResult), string(DualReasonFencing))
				}
			case DualEventClaimAccepted:
				if event.ReasonClass != DualReasonNone {
					report(DualInvariantLedgerEligibility, "lease group %s: claim-accepted carries reason class %q, accepted outcomes carry the absence marker", group, string(event.ReasonClass))
				}
			case DualEventClaimRejected:
				if event.ReasonClass == DualReasonNone {
					report(DualInvariantLedgerEligibility, "lease group %s: claim-rejected carries no reason class; rejections always name the current-ledger reason", group)
				}
			}
		}
	}
	return violations
}

// dualScenarioClaimRequest derives the deterministic claim request of one
// scenario: identical scenario names always yield identical capability
// requirements, so two topology runs of the identical scenario claim the
// identical requirement.
func dualScenarioClaimRequest(scenario string) DualClaimRequest {
	requirements, err := domain.NewSandboxRequirements(domain.AccessModeWorkspaceWrite, domain.AssuranceLevelWorkspaceWrite)
	if err != nil {
		panic(fmt.Sprintf("sandbox: dual scenario claim request: %v", err))
	}
	return DualClaimRequest{
		TaskId:       "task-" + scenario,
		RunId:        "run-" + scenario,
		AttemptId:    "attempt-" + scenario,
		AllocationId: "alloc-" + scenario,
		WorkloadRole: WorkloadRoleWorker,
		Principal:    "principal-" + scenario,
		Requirements: requirements,
	}
}

// Frozen scenario names of the dual-topology conformance suite. The suite
// is one matrix parameterized by topology — never one suite per topology.
const (
	DualScenarioHappyPath                  = "dual-happy-path"
	DualScenarioStaleFencing               = "dual-stale-fencing"
	DualScenarioRegistrationRevoke         = "dual-registration-revoke"
	DualScenarioRegistrationExpire         = "dual-registration-expire"
	DualScenarioRegistrationIncompatible   = "dual-registration-incompatible"
	DualScenarioSnapshotSupersede          = "dual-snapshot-supersede"
	DualScenarioEvidenceRevoke             = "dual-evidence-revoke"
	DualScenarioAckDeadlineMissed          = "dual-ack-deadline-missed"
	DualScenarioLeaseExpiry                = "dual-lease-expiry"
	DualScenarioLateResult                 = "dual-late-result"
	DualScenarioSingleActiveExecution      = "dual-single-active-execution"
	DualScenarioCrossTopologyUniqueClaim   = "dual-cross-topology-unique-claim"
	DualScenarioFaultDelayExec             = "dual-fault-delay-exec"
	DualScenarioFaultRejectExec            = "dual-fault-reject-exec"
	DualScenarioFaultDropProvisionResponse = "dual-fault-drop-provision-response"
)

// DualScenarios returns the frozen scenario matrix in execution order.
func DualScenarios() []string {
	return []string{
		DualScenarioHappyPath,
		DualScenarioStaleFencing,
		DualScenarioRegistrationRevoke,
		DualScenarioRegistrationExpire,
		DualScenarioRegistrationIncompatible,
		DualScenarioSnapshotSupersede,
		DualScenarioEvidenceRevoke,
		DualScenarioAckDeadlineMissed,
		DualScenarioLeaseExpiry,
		DualScenarioLateResult,
		DualScenarioSingleActiveExecution,
		DualScenarioCrossTopologyUniqueClaim,
		DualScenarioFaultDelayExec,
		DualScenarioFaultRejectExec,
		DualScenarioFaultDropProvisionResponse,
	}
}

// DualScenarioFaults returns the fake-provider fault specs one scenario
// injects, parameterized identically for every topology: fault injection is
// additive over the existing internal/sandbox fault kinds and never changes
// an existing fixture's behavior. Non-fault scenarios inject nothing.
func DualScenarioFaults(scenario string) []FaultSpec {
	switch scenario {
	case DualScenarioFaultDelayExec:
		return []FaultSpec{{Operation: OperationExec, Fault: FaultDelay}}
	case DualScenarioFaultRejectExec:
		return []FaultSpec{{Operation: OperationExec, Fault: FaultReject}}
	case DualScenarioFaultDropProvisionResponse:
		return []FaultSpec{{Operation: OperationProvision, Fault: FaultDropResponse}}
	default:
		return nil
	}
}

// dualExpectedEvent is one required or forbidden normalized event of one
// scenario assertion.
type dualExpectedEvent struct {
	kind        DualTraceKind
	reasonClass DualReasonClass
}

// dualScenarioEvents filters one trace down to the events of the scenario's
// lease group (and the pre-lease events of the identical attempt).
func dualScenarioEvents(trace []DualTraceEvent, attemptId, leaseId string) []DualTraceEvent {
	var scoped []DualTraceEvent
	for _, event := range trace {
		if event.AttemptId != attemptId {
			continue
		}
		if event.LeaseId == leaseId || event.LeaseId == "" {
			scoped = append(scoped, event)
		}
	}
	return scoped
}

// dualAssertEvents asserts the required and forbidden normalized events of
// one scenario run and returns every failure description.
func dualAssertEvents(trace []DualTraceEvent, attemptId, leaseId string, required, forbidden []dualExpectedEvent) []string {
	var failures []string
	scoped := dualScenarioEvents(trace, attemptId, leaseId)
	counts := map[dualExpectedEvent]int{}
	for _, event := range scoped {
		counts[dualExpectedEvent{kind: event.Kind, reasonClass: event.ReasonClass}]++
	}
	for _, expected := range required {
		if counts[expected] == 0 {
			failures = append(failures, fmt.Sprintf("required event %s:%s missing", string(expected.kind), string(expected.reasonClass)))
		}
	}
	for _, expected := range forbidden {
		if counts[expected] > 0 {
			failures = append(failures, fmt.Sprintf("forbidden event %s:%s present %d times", string(expected.kind), string(expected.reasonClass), counts[expected]))
		}
	}
	return failures
}

// dualRunOutcome collects the operational expectations one scenario driver
// asserts while it drives the binding.
type dualDriverContext struct {
	failures []string
}

func (driver *dualDriverContext) expect(condition bool, format string, args ...any) {
	if !condition {
		driver.failures = append(driver.failures, fmt.Sprintf(format, args...))
	}
}

// dualInvalidationScenarioClass maps one invalidation scenario onto the
// closed reason class its terminal lease event and post-invalidation
// rejections must carry.
func dualInvalidationScenarioClass(scenario string) (DualInvalidationKind, DualReasonClass, bool) {
	switch scenario {
	case DualScenarioRegistrationRevoke:
		return DualInvalidateRegistrationRevoke, DualReasonRevoked, true
	case DualScenarioRegistrationExpire:
		return DualInvalidateRegistrationExpire, DualReasonExpired, true
	case DualScenarioRegistrationIncompatible:
		return DualInvalidateRegistrationIncompatible, DualReasonIncompatible, true
	case DualScenarioSnapshotSupersede:
		return DualInvalidateSnapshotSupersede, DualReasonSuperseded, true
	case DualScenarioEvidenceRevoke:
		return DualInvalidateEvidenceRevoke, DualReasonEvidenceRevoked, true
	default:
		return "", "", false
	}
}

// dualDriveHappyPath drives the accepted baseline: claim → exec → result.
func dualDriveHappyPath(ctx context.Context, driver *dualDriverContext, authority DualAuthority, binding DualTopologyBinding, request DualClaimRequest) {
	receipt, err := binding.Claim(ctx, authority, request)
	driver.expect(err == nil, "claim failed: %v", err)
	driver.expect(receipt.Outcome.Accepted, "claim rejected: %s (%s)", string(receipt.Outcome.ReasonClass), receipt.Outcome.Detail)
	if !receipt.Outcome.Accepted {
		return
	}
	executionId, outcome, err := binding.StartExecution(ctx, authority, receipt.Lease, "cmd-happy")
	driver.expect(err == nil, "exec start failed: %v", err)
	driver.expect(outcome.Accepted, "exec start rejected: %s (%s)", string(outcome.ReasonClass), outcome.Detail)
	if !outcome.Accepted {
		return
	}
	digest, outcome, err := binding.FinishExecution(ctx, authority, receipt.Lease, "cmd-happy", executionId)
	driver.expect(err == nil, "exec finish failed: %v", err)
	driver.expect(outcome.Accepted, "exec finish rejected: %s (%s)", string(outcome.ReasonClass), outcome.Detail)
	if !outcome.Accepted {
		return
	}
	outcome, err = binding.SubmitResult(ctx, authority, receipt.Lease, "cmd-happy", digest)
	driver.expect(err == nil, "result submission failed: %v", err)
	driver.expect(outcome.Accepted, "result admission rejected: %s (%s)", string(outcome.ReasonClass), outcome.Detail)
}

// dualScenarioDriver drives one scenario against one topology binding and
// returns the scenario-level assertion failures plus the required/forbidden
// event assertions.
type dualScenarioDriver func(ctx context.Context, scenario string, authority DualAuthority, binding DualTopologyBinding) []string

// dualScenarioDrivers is the frozen scenario matrix of the suite: the
// identical drivers run under every topology parameterization.
var dualScenarioDrivers = map[string]struct {
	driver    dualScenarioDriver
	required  []dualExpectedEvent
	forbidden []dualExpectedEvent
}{
	DualScenarioHappyPath: {
		driver: func(ctx context.Context, scenario string, authority DualAuthority, binding DualTopologyBinding) []string {
			driver := &dualDriverContext{}
			dualDriveHappyPath(ctx, driver, authority, binding, dualScenarioClaimRequest(scenario))
			return driver.failures
		},
		required: []dualExpectedEvent{
			{kind: DualEventClaimAccepted},
			{kind: DualEventExecStarted},
			{kind: DualEventExecFinished},
			{kind: DualEventResultAdmitted},
		},
	},
	DualScenarioStaleFencing: {
		driver: func(ctx context.Context, scenario string, authority DualAuthority, binding DualTopologyBinding) []string {
			driver := &dualDriverContext{}
			request := dualScenarioClaimRequest(scenario)
			receipt, err := binding.Claim(ctx, authority, request)
			driver.expect(err == nil, "claim failed: %v", err)
			driver.expect(receipt.Outcome.Accepted, "claim rejected: %s", string(receipt.Outcome.ReasonClass))
			if !receipt.Outcome.Accepted {
				return driver.failures
			}
			// A stale fencingToken operation is always blocked.
			outcome, err := binding.PresentStaleOperation(ctx, authority, receipt.Lease)
			driver.expect(err == nil, "stale operation failed: %v", err)
			driver.expect(!outcome.Accepted, "a stale fencingToken operation must never be accepted")
			driver.expect(outcome.ReasonClass == DualReasonFencing, "a stale fencingToken operation must carry the fencing reason class, got %q", string(outcome.ReasonClass))
			// The current-generation flow continues unimpaired under the
			// identical accepted claim.
			executionId, outcome, err := binding.StartExecution(ctx, authority, receipt.Lease, "cmd-after-stale")
			driver.expect(err == nil, "exec start failed: %v", err)
			driver.expect(outcome.Accepted, "exec start rejected after the stale block: %s (%s)", string(outcome.ReasonClass), outcome.Detail)
			if !outcome.Accepted {
				return driver.failures
			}
			digest, outcome, err := binding.FinishExecution(ctx, authority, receipt.Lease, "cmd-after-stale", executionId)
			driver.expect(err == nil, "exec finish failed: %v", err)
			driver.expect(outcome.Accepted, "exec finish rejected: %s (%s)", string(outcome.ReasonClass), outcome.Detail)
			outcome, err = binding.SubmitResult(ctx, authority, receipt.Lease, "cmd-after-stale", digest)
			driver.expect(err == nil, "result submission failed: %v", err)
			driver.expect(outcome.Accepted, "result admission rejected: %s (%s)", string(outcome.ReasonClass), outcome.Detail)
			return driver.failures
		},
		required: []dualExpectedEvent{
			{kind: DualEventFencingViolationBlocked, reasonClass: DualReasonFencing},
			{kind: DualEventClaimAccepted},
			{kind: DualEventResultAdmitted},
		},
	},
	DualScenarioAckDeadlineMissed: {
		driver: func(ctx context.Context, scenario string, authority DualAuthority, binding DualTopologyBinding) []string {
			driver := &dualDriverContext{}
			request := dualScenarioClaimRequest(scenario)
			receipt, err := binding.ClaimUnacked(ctx, authority, request)
			driver.expect(err == nil, "unacked claim failed: %v", err)
			driver.expect(!receipt.Outcome.Accepted, "an unacked claim must not complete as accepted")
			driver.expect(receipt.Lease.LeaseId != "", "an unacked claim must still carry the adjudicated lease for the deadline reconciliation")
			if receipt.Lease.LeaseId == "" {
				return driver.failures
			}
			err = authority.MissAckDeadline(ctx, receipt.Lease)
			driver.expect(err == nil, "miss ack deadline failed: %v", err)
			// After the ack deadline miss the lease is dead: no execution,
			// and a late result is quarantined.
			_, outcome, err := binding.StartExecution(ctx, authority, receipt.Lease, "cmd-after-ack-deadline")
			driver.expect(err == nil, "exec start failed: %v", err)
			driver.expect(!outcome.Accepted, "execution start after the ack deadline must be rejected")
			outcome, err = binding.SubmitResult(ctx, authority, receipt.Lease, "cmd-after-ack-deadline", RecomputeSHA256([]byte("late")))
			driver.expect(err == nil, "late result submission failed: %v", err)
			driver.expect(!outcome.Accepted, "a late result after the ack deadline must never be admitted")
			driver.expect(outcome.ReasonClass == DualReasonLateResult, "a late result must carry the late-result reason class, got %q", string(outcome.ReasonClass))
			return driver.failures
		},
		required: []dualExpectedEvent{
			{kind: DualEventLeaseExpired, reasonClass: DualReasonDeadline},
			{kind: DualEventResultQuarantined, reasonClass: DualReasonLateResult},
		},
		forbidden: []dualExpectedEvent{
			{kind: DualEventClaimAccepted},
			{kind: DualEventResultAdmitted},
		},
	},
	DualScenarioLeaseExpiry: {
		driver: func(ctx context.Context, scenario string, authority DualAuthority, binding DualTopologyBinding) []string {
			driver := &dualDriverContext{}
			request := dualScenarioClaimRequest(scenario)
			receipt, err := binding.Claim(ctx, authority, request)
			driver.expect(err == nil, "claim failed: %v", err)
			driver.expect(receipt.Outcome.Accepted, "claim rejected: %s", string(receipt.Outcome.ReasonClass))
			if !receipt.Outcome.Accepted {
				return driver.failures
			}
			// A heartbeat before the expiry passes against the current
			// ledger (deadline semantics: the heartbeat state).
			outcome, err := binding.Heartbeat(ctx, authority, receipt.Lease)
			driver.expect(err == nil, "heartbeat failed: %v", err)
			driver.expect(outcome.Accepted, "an in-flight heartbeat must pass, got %s (%s)", string(outcome.ReasonClass), outcome.Detail)
			err = authority.ExpireLeaseWindow(ctx, receipt.Lease)
			driver.expect(err == nil, "expire lease failed: %v", err)
			outcome, err = binding.Heartbeat(ctx, authority, receipt.Lease)
			driver.expect(err == nil, "post-expiry heartbeat failed: %v", err)
			driver.expect(!outcome.Accepted, "a heartbeat after the expiry must be rejected")
			return driver.failures
		},
		required: []dualExpectedEvent{
			{kind: DualEventClaimAccepted},
			{kind: DualEventLeaseExpired, reasonClass: DualReasonDeadline},
		},
		forbidden: []dualExpectedEvent{
			{kind: DualEventResultAdmitted},
		},
	},
	DualScenarioLateResult: {
		driver: func(ctx context.Context, scenario string, authority DualAuthority, binding DualTopologyBinding) []string {
			driver := &dualDriverContext{}
			request := dualScenarioClaimRequest(scenario)
			receipt, err := binding.Claim(ctx, authority, request)
			driver.expect(err == nil, "claim failed: %v", err)
			driver.expect(receipt.Outcome.Accepted, "claim rejected: %s", string(receipt.Outcome.ReasonClass))
			if !receipt.Outcome.Accepted {
				return driver.failures
			}
			executionId, outcome, err := binding.StartExecution(ctx, authority, receipt.Lease, "cmd-late")
			driver.expect(err == nil, "exec start failed: %v", err)
			driver.expect(outcome.Accepted, "exec start rejected: %s", string(outcome.ReasonClass))
			if !outcome.Accepted {
				return driver.failures
			}
			digest, outcome, err := binding.FinishExecution(ctx, authority, receipt.Lease, "cmd-late", executionId)
			driver.expect(err == nil, "exec finish failed: %v", err)
			driver.expect(outcome.Accepted, "exec finish rejected: %s", string(outcome.ReasonClass))
			err = authority.ExpireLeaseWindow(ctx, receipt.Lease)
			driver.expect(err == nil, "expire lease failed: %v", err)
			outcome, err = binding.SubmitResult(ctx, authority, receipt.Lease, "cmd-late", digest)
			driver.expect(err == nil, "late result submission failed: %v", err)
			driver.expect(!outcome.Accepted, "a result arriving after the lease expiry must never be admitted")
			driver.expect(outcome.ReasonClass == DualReasonLateResult, "a late result must carry the late-result reason class, got %q", string(outcome.ReasonClass))
			return driver.failures
		},
		required: []dualExpectedEvent{
			{kind: DualEventExecFinished},
			{kind: DualEventLeaseExpired, reasonClass: DualReasonDeadline},
			{kind: DualEventResultQuarantined, reasonClass: DualReasonLateResult},
		},
		forbidden: []dualExpectedEvent{
			{kind: DualEventResultAdmitted},
		},
	},
	DualScenarioSingleActiveExecution: {
		driver: func(ctx context.Context, scenario string, authority DualAuthority, binding DualTopologyBinding) []string {
			driver := &dualDriverContext{}
			request := dualScenarioClaimRequest(scenario)
			receipt, err := binding.Claim(ctx, authority, request)
			driver.expect(err == nil, "claim failed: %v", err)
			driver.expect(receipt.Outcome.Accepted, "claim rejected: %s", string(receipt.Outcome.ReasonClass))
			if !receipt.Outcome.Accepted {
				return driver.failures
			}
			executionId, outcome, err := binding.StartExecution(ctx, authority, receipt.Lease, "cmd-single")
			driver.expect(err == nil, "exec start failed: %v", err)
			driver.expect(outcome.Accepted, "exec start rejected: %s", string(outcome.ReasonClass))
			if !outcome.Accepted {
				return driver.failures
			}
			// A second execution start overlapping the open one is the
			// dual-active violation and must be rejected.
			_, outcome, err = binding.StartExecution(ctx, authority, receipt.Lease, "cmd-single-overlap")
			driver.expect(err == nil, "overlapping exec start failed: %v", err)
			driver.expect(!outcome.Accepted, "a second overlapping execution start must be rejected (no dual-active)")
			_, outcome, err = binding.FinishExecution(ctx, authority, receipt.Lease, "cmd-single", executionId)
			driver.expect(err == nil, "exec finish failed: %v", err)
			driver.expect(outcome.Accepted, "exec finish rejected: %s", string(outcome.ReasonClass))
			return driver.failures
		},
		required: []dualExpectedEvent{
			{kind: DualEventExecStarted},
			{kind: DualEventExecFinished},
		},
		forbidden: []dualExpectedEvent{
			{kind: DualEventResultAdmitted},
		},
	},
	DualScenarioFaultDelayExec: {
		driver: func(ctx context.Context, scenario string, authority DualAuthority, binding DualTopologyBinding) []string {
			driver := &dualDriverContext{}
			dualDriveHappyPath(ctx, driver, authority, binding, dualScenarioClaimRequest(scenario))
			return driver.failures
		},
		required: []dualExpectedEvent{
			{kind: DualEventClaimAccepted},
			{kind: DualEventExecStarted},
			{kind: DualEventExecFinished},
			{kind: DualEventResultAdmitted},
		},
	},
	DualScenarioFaultRejectExec: {
		driver: func(ctx context.Context, scenario string, authority DualAuthority, binding DualTopologyBinding) []string {
			driver := &dualDriverContext{}
			request := dualScenarioClaimRequest(scenario)
			receipt, err := binding.Claim(ctx, authority, request)
			driver.expect(err == nil, "claim failed: %v", err)
			driver.expect(receipt.Outcome.Accepted, "claim rejected: %s", string(receipt.Outcome.ReasonClass))
			if !receipt.Outcome.Accepted {
				return driver.failures
			}
			_, outcome, err := binding.StartExecution(ctx, authority, receipt.Lease, "cmd-rejected")
			driver.expect(err == nil, "exec start failed: %v", err)
			driver.expect(!outcome.Accepted, "an injected exec rejection must surface as a rejected execution start")
			return driver.failures
		},
		required: []dualExpectedEvent{
			{kind: DualEventClaimAccepted},
		},
		forbidden: []dualExpectedEvent{
			{kind: DualEventExecStarted},
			{kind: DualEventExecFinished},
			{kind: DualEventResultAdmitted},
		},
	},
	DualScenarioFaultDropProvisionResponse: {
		driver: func(ctx context.Context, scenario string, authority DualAuthority, binding DualTopologyBinding) []string {
			driver := &dualDriverContext{}
			request := dualScenarioClaimRequest(scenario)
			receipt, err := binding.Claim(ctx, authority, request)
			driver.expect(err == nil, "claim failed: %v", err)
			driver.expect(!receipt.Outcome.Accepted, "a claim whose provision response was dropped must not complete as accepted")
			driver.expect(receipt.Lease.LeaseId != "", "the adjudicated lease of the dropped provision must survive for reconciliation")
			if receipt.Lease.LeaseId == "" {
				return driver.failures
			}
			// Lost-response reconciliation: the unaccepted lease is fenced
			// through the ack deadline, never left live (ADR 0017 §7).
			err = authority.MissAckDeadline(ctx, receipt.Lease)
			driver.expect(err == nil, "miss ack deadline failed: %v", err)
			return driver.failures
		},
		required: []dualExpectedEvent{
			{kind: DualEventLeaseExpired, reasonClass: DualReasonDeadline},
		},
		forbidden: []dualExpectedEvent{
			{kind: DualEventClaimAccepted},
			{kind: DualEventResultAdmitted},
		},
	},
}

// dualInvalidationDriver builds the driver shared by the five post-claim
// invalidation scenarios (ADR 0018 §7): claim accepted → invalidation fact
// → the in-flight lease loses eligibility immediately → heartbeat, exec and
// late result all fail closed → the identical attempt can never be
// re-claimed and a new attempt continues with a new lease.
func dualInvalidationDriver(scenario string) dualScenarioDriver {
	return func(ctx context.Context, scenarioName string, authority DualAuthority, binding DualTopologyBinding) []string {
		driver := &dualDriverContext{}
		invalidation, class, ok := dualInvalidationScenarioClass(scenarioName)
		driver.expect(ok, "scenario %q is not an invalidation scenario", scenarioName)
		if !ok {
			return driver.failures
		}
		request := dualScenarioClaimRequest(scenarioName)
		receipt, err := binding.Claim(ctx, authority, request)
		driver.expect(err == nil, "claim failed: %v", err)
		driver.expect(receipt.Outcome.Accepted, "claim rejected: %s", string(receipt.Outcome.ReasonClass))
		if !receipt.Outcome.Accepted {
			return driver.failures
		}
		err = authority.Invalidate(ctx, invalidation)
		driver.expect(err == nil, "invalidate failed: %v", err)
		// Heartbeat after the invalidation rechecks the current ledger and
		// fails closed.
		outcome, err := binding.Heartbeat(ctx, authority, receipt.Lease)
		driver.expect(err == nil, "heartbeat failed: %v", err)
		driver.expect(!outcome.Accepted, "a heartbeat after the invalidation must fail closed")
		// Execution after the invalidation is fenced.
		_, outcome, err = binding.StartExecution(ctx, authority, receipt.Lease, "cmd-after-invalidation")
		driver.expect(err == nil, "exec start failed: %v", err)
		driver.expect(!outcome.Accepted, "an execution start after the invalidation must fail closed")
		// A late result is quarantined, never admitted.
		outcome, err = binding.SubmitResult(ctx, authority, receipt.Lease, "cmd-after-invalidation", RecomputeSHA256([]byte("late-result")))
		driver.expect(err == nil, "late result submission failed: %v", err)
		driver.expect(!outcome.Accepted, "a late result after the invalidation must never be admitted")
		// Eligibility is adjudicated against the current ledger: the
		// identical attempt can never be re-claimed after the invalidation.
		reclaim, err := binding.Claim(ctx, authority, request)
		driver.expect(err == nil, "re-claim failed: %v", err)
		driver.expect(!reclaim.Outcome.Accepted, "the identical attempt must never be re-claimed after the invalidation")
		driver.expect(reclaim.Outcome.ReasonClass == class, "the re-claim rejection must carry the current-ledger reason class %q, got %q", string(class), string(reclaim.Outcome.ReasonClass))
		// Continuation requires a new registration plus a new attempt with a
		// new claim re-matched against it; the invalidated registration is
		// terminal and never resurrected.
		err = authority.Reregister(ctx)
		driver.expect(err == nil, "reregister failed: %v", err)
		renewed := request
		renewed.AttemptId = request.AttemptId + "-renewed"
		renewed.AllocationId = request.AllocationId + "-renewed"
		renewedReceipt, err := binding.Claim(ctx, authority, renewed)
		driver.expect(err == nil, "renewed claim failed: %v", err)
		driver.expect(renewedReceipt.Outcome.Accepted, "a new attempt re-matched against the fresh registration must claim successfully, got %s (%s)", string(renewedReceipt.Outcome.ReasonClass), renewedReceipt.Outcome.Detail)
		return driver.failures
	}
}

// dualInvalidationTerminalEvent is the terminal event kind an invalidation
// scenario records: eligibility invalidations cancel the lease
// (lease-revoked carries the mapped class), the expiry-class invalidation
// also cancels — the lease-expired kind stays reserved for the deadline
// states.
func dualInvalidationTerminalEvent(class DualReasonClass) dualExpectedEvent {
	return dualExpectedEvent{kind: DualEventLeaseRevoked, reasonClass: class}
}

func init() {
	for _, scenario := range []string{
		DualScenarioRegistrationRevoke,
		DualScenarioRegistrationExpire,
		DualScenarioRegistrationIncompatible,
		DualScenarioSnapshotSupersede,
		DualScenarioEvidenceRevoke,
	} {
		_, class, _ := dualInvalidationScenarioClass(scenario)
		dualScenarioDrivers[scenario] = struct {
			driver    dualScenarioDriver
			required  []dualExpectedEvent
			forbidden []dualExpectedEvent
		}{
			driver: dualInvalidationDriver(scenario),
			required: []dualExpectedEvent{
				{kind: DualEventClaimAccepted},
				dualInvalidationTerminalEvent(class),
				{kind: DualEventResultQuarantined, reasonClass: DualReasonLateResult},
				{kind: DualEventClaimRejected, reasonClass: class},
			},
			forbidden: []dualExpectedEvent{
				{kind: DualEventResultAdmitted},
			},
		}
	}
}

// DualTopologyRun observes one scenario run under one topology.
type DualTopologyRun struct {
	Topology            DualTopology             `json:"topology"`
	Trace               []DualTraceEvent         `json:"trace"`
	InvariantViolations []DualInvariantViolation `json:"invariantViolations"`
	ScenarioFailures    []string                 `json:"scenarioFailures"`
	Error               string                   `json:"error,omitempty"`
}

// passed reports whether the run carries no failures at all.
func (run DualTopologyRun) passed() bool {
	return run.Error == "" && len(run.InvariantViolations) == 0 && len(run.ScenarioFailures) == 0
}

// DualScenarioVerdict is the suite's adjudication of one scenario across
// the two topology parameterizations: the per-topology runs, the
// outcome/invariant equivalence of their normalized business traces and the
// combined verdict.
type DualScenarioVerdict struct {
	Scenario   string          `json:"scenario"`
	Passed     bool            `json:"passed"`
	Equivalent bool            `json:"equivalent"`
	Reason     string          `json:"reason"`
	First      DualTopologyRun `json:"first"`
	Second     DualTopologyRun `json:"second"`
}

// DualSuiteHarness parameterizes the one dual-topology conformance suite by
// topology: the factories build one fresh authority per scenario run and
// one topology binding over it, so the identical scenario matrix runs under
// every topology. The shared-authority factory builds the one authority two
// topologies contend on for the cross-topology unique-claim scenario.
type DualSuiteHarness struct {
	First           DualTopology
	Second          DualTopology
	NewAuthority    func(scenario string) DualAuthority
	NewBinding      func(topology DualTopology, scenario string, authority DualAuthority) DualTopologyBinding
	SharedAuthority func(scenario string) DualAuthority
}

// runScenarioOnce drives one scenario under one topology with a fresh
// authority and returns the observed run.
func dualRunScenarioOnce(ctx context.Context, harness DualSuiteHarness, scenario string, topology DualTopology) DualTopologyRun {
	run := DualTopologyRun{Topology: topology, Trace: []DualTraceEvent{}}
	spec, ok := dualScenarioDrivers[scenario]
	if !ok {
		run.Error = fmt.Sprintf("sandbox: dual suite: unknown scenario %q", scenario)
		return run
	}
	authority := harness.NewAuthority(scenario)
	binding := harness.NewBinding(topology, scenario, authority)
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				run.Error = fmt.Sprintf("sandbox: dual suite: scenario %q panicked under topology %s: %v", scenario, string(topology), recovered)
			}
		}()
		run.ScenarioFailures = spec.driver(ctx, scenario, authority, binding)
	}()
	run.Trace = authority.Trace()
	request := dualScenarioClaimRequest(scenario)
	run.ScenarioFailures = append(run.ScenarioFailures,
		dualAssertEvents(run.Trace, request.AttemptId, scenarioLeaseId(run.Trace, request.AttemptId), spec.required, spec.forbidden)...)
	run.InvariantViolations = AssertDualBusinessInvariants(run.Trace)
	return run
}

// scenarioLeaseId discovers the leaseId the scenario run recorded for the
// scenario attempt (the empty string when the scenario never issued a
// lease).
func scenarioLeaseId(trace []DualTraceEvent, attemptId string) string {
	for _, event := range trace {
		if event.AttemptId == attemptId && event.LeaseId != "" {
			return event.LeaseId
		}
	}
	return ""
}

// dualDriveCrossTopologyUniqueClaim drives the shared-ledger contention
// scenario: the identical capability requirement is claimed through both
// topologies against the one authority — exactly one claim is accepted.
func dualDriveCrossTopologyUniqueClaim(ctx context.Context, harness DualSuiteHarness, scenario string) DualScenarioVerdict {
	verdict := DualScenarioVerdict{Scenario: scenario, Equivalent: true}
	authority := harness.SharedAuthority(scenario)
	firstBinding := harness.NewBinding(harness.First, scenario, authority)
	secondBinding := harness.NewBinding(harness.Second, scenario, authority)
	request := dualScenarioClaimRequest(scenario)
	var failures []string
	firstReceipt, err := firstBinding.Claim(ctx, authority, request)
	if err != nil {
		failures = append(failures, fmt.Sprintf("first claim failed: %v", err))
	} else if !firstReceipt.Outcome.Accepted {
		failures = append(failures, fmt.Sprintf("first claim rejected: %s", string(firstReceipt.Outcome.ReasonClass)))
	}
	secondReceipt, err := secondBinding.Claim(ctx, authority, request)
	if err != nil {
		failures = append(failures, fmt.Sprintf("second claim failed: %v", err))
	} else {
		if secondReceipt.Outcome.Accepted {
			failures = append(failures, "the identical capability requirement was accepted by both topologies; the cross-topology unique-claim invariant is violated")
		}
		if secondReceipt.Outcome.ReasonClass != DualReasonDuplicateClaim {
			failures = append(failures, fmt.Sprintf("the contended second claim must carry the duplicate-claim reason class, got %q", string(secondReceipt.Outcome.ReasonClass)))
		}
	}
	trace := authority.Trace()
	failures = append(failures, dualAssertEvents(trace, request.AttemptId, scenarioLeaseId(trace, request.AttemptId),
		[]dualExpectedEvent{
			{kind: DualEventClaimAccepted},
			{kind: DualEventClaimRejected, reasonClass: DualReasonDuplicateClaim},
		},
		[]dualExpectedEvent{
			{kind: DualEventResultAdmitted},
		})...)
	violations := AssertDualBusinessInvariants(trace)
	verdict.First = DualTopologyRun{Topology: harness.First, Trace: trace, InvariantViolations: violations, ScenarioFailures: failures}
	verdict.Second = DualTopologyRun{Topology: harness.Second, Trace: trace}
	verdict.Passed = len(failures) == 0 && len(violations) == 0
	if !verdict.Passed {
		verdict.Reason = strings.Join(failures, "; ")
	}
	return verdict
}

// RunDualTopologySuite runs the frozen dual-topology conformance matrix:
// every scenario runs under both topology parameterizations against fresh
// authorities, the normalized business traces are compared under
// outcome/invariant equivalence, and the business invariants are asserted
// on every recorded trace. The cross-topology unique-claim scenario
// contends both topologies on one shared authority.
func RunDualTopologySuite(ctx context.Context, harness DualSuiteHarness) ([]DualScenarioVerdict, error) {
	if err := harness.First.Validate(); err != nil {
		return nil, err
	}
	if err := harness.Second.Validate(); err != nil {
		return nil, err
	}
	if harness.NewAuthority == nil || harness.NewBinding == nil {
		return nil, fmt.Errorf("sandbox: dual suite: the harness must bind the authority and binding factories")
	}
	verdicts := make([]DualScenarioVerdict, 0, len(DualScenarios()))
	for _, scenario := range DualScenarios() {
		if scenario == DualScenarioCrossTopologyUniqueClaim {
			if harness.SharedAuthority == nil {
				return nil, fmt.Errorf("sandbox: dual suite: the cross-topology unique-claim scenario requires the shared-authority factory")
			}
			verdicts = append(verdicts, dualDriveCrossTopologyUniqueClaim(ctx, harness, scenario))
			continue
		}
		first := dualRunScenarioOnce(ctx, harness, scenario, harness.First)
		second := dualRunScenarioOnce(ctx, harness, scenario, harness.Second)
		equivalent := EquivalentDualTraces(first.Trace, second.Trace)
		passed := first.passed() && second.passed() && equivalent
		reason := ""
		if !equivalent {
			reason = "the topology traces are not outcome/invariant equivalent: " + ExplainDualTraceDifference(first.Trace, second.Trace)
		} else if !first.passed() {
			reason = fmt.Sprintf("topology %s failed the scenario: %s", string(harness.First), dualRunReason(first))
		} else if !second.passed() {
			reason = fmt.Sprintf("topology %s failed the scenario: %s", string(harness.Second), dualRunReason(second))
		}
		verdicts = append(verdicts, DualScenarioVerdict{
			Scenario:   scenario,
			Passed:     passed,
			Equivalent: equivalent,
			Reason:     reason,
			First:      first,
			Second:     second,
		})
	}
	return verdicts, nil
}

// dualRunReason renders the first failure of one run deterministically.
func dualRunReason(run DualTopologyRun) string {
	if run.Error != "" {
		return run.Error
	}
	if len(run.ScenarioFailures) > 0 {
		return strings.Join(run.ScenarioFailures, "; ")
	}
	if len(run.InvariantViolations) > 0 {
		parts := make([]string, 0, len(run.InvariantViolations))
		for _, violation := range run.InvariantViolations {
			parts = append(parts, string(violation.Invariant)+": "+violation.Detail)
		}
		return strings.Join(parts, "; ")
	}
	return ""
}

// dualScenarioDigest stabilizes the deterministic derivation seeds of the
// scripted lease identities used by the in-package dual fixtures; it is a
// package-internal helper and never crosses the wire.
func dualScenarioDigest(seed string) string {
	return canonical.DigestBytes([]byte("dual-scenario" + "\x00" + seed))
}
