package goal

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// Fail-closed sentinels of the budget reservation state machine.
var (
	// ErrReservationNotFound is returned when no reservation carries the id.
	ErrReservationNotFound = errors.New("goal: reservation not found")
	// ErrReservationConflict is returned when a transition violates the
	// current-state or CAS expectation, including duplicate settle/release
	// with diverging content and lost-response replays that no longer match.
	ErrReservationConflict = errors.New("goal: reservation state conflict")
	// ErrReservationStale is returned when a release binds a plan revision
	// other than the reservation's own revision.
	ErrReservationStale = errors.New("goal: stale plan revision")
	// ErrBudgetExhausted is returned when a reservation would oversell the
	// cumulative availability.
	ErrBudgetExhausted = errors.New("goal: budget availability exhausted")
	// ErrDispatchHalted is returned when actual usage exceeded a reserved
	// estimate and new dispatch is halted by decision.
	ErrDispatchHalted = errors.New("goal: new dispatch halted")
	// ErrPlanRevisionConflict is returned when an accepted revision fails
	// the CAS, binding or replay check of the plan transaction.
	ErrPlanRevisionConflict = errors.New("goal: plan revision CAS conflict")
	// ErrIdempotencyConflict is returned when an idempotent replay carries
	// content diverging from the original reservation.
	ErrIdempotencyConflict = errors.New("goal: idempotency key conflict")
)

// ReservationState is the closed enumeration of budget reservation states
// (ADR 0019 §4): reserved → committed → settled, or reserved →
// released|expired. No other transition exists.
type ReservationState string

// Closed reservation states.
const (
	ReservationStateReserved  ReservationState = "reserved"
	ReservationStateCommitted ReservationState = "committed"
	ReservationStateSettled   ReservationState = "settled"
	ReservationStateReleased  ReservationState = "released"
	ReservationStateExpired   ReservationState = "expired"
)

// Validate rejects every value outside the closed enumeration.
func (state ReservationState) Validate() error {
	switch state {
	case ReservationStateReserved, ReservationStateCommitted, ReservationStateSettled,
		ReservationStateReleased, ReservationStateExpired:
		return nil
	default:
		return fmt.Errorf("goal: unknown reservationState %q", string(state))
	}
}

// ReservationRequest is the deterministic reservation description produced
// by admission step 6 for one effective node. It binds the Goal, node, plan
// revision, command identity and estimate.
type ReservationRequest struct {
	ReservationId  string       `json:"reservationId"`
	IdempotencyKey string       `json:"idempotencyKey"`
	GoalId         string       `json:"goalId"`
	NodeId         string       `json:"nodeId"`
	PlanRevision   int64        `json:"planRevision"`
	CommandId      string       `json:"commandId"`
	Estimate       NodeEstimate `json:"estimate"`
}

// Validate fails closed on malformed ids, a non-positive plan revision or
// an invalid estimate.
func (request ReservationRequest) Validate() error {
	if err := domain.ValidateID(request.ReservationId); err != nil {
		return fmt.Errorf("goal: reservationRequest.reservationId: %w", err)
	}
	if err := domain.ValidateID(request.IdempotencyKey); err != nil {
		return fmt.Errorf("goal: reservationRequest.idempotencyKey: %w", err)
	}
	if err := domain.ValidateID(request.GoalId); err != nil {
		return fmt.Errorf("goal: reservationRequest.goalId: %w", err)
	}
	if err := domain.ValidateID(request.NodeId); err != nil {
		return fmt.Errorf("goal: reservationRequest.nodeId: %w", err)
	}
	if request.PlanRevision < 1 {
		return fmt.Errorf("goal: reservationRequest.planRevision must be at least 1")
	}
	if err := domain.ValidateID(request.CommandId); err != nil {
		return fmt.Errorf("goal: reservationRequest.commandId: %w", err)
	}
	return request.Estimate.Validate()
}

// matches reports whether the request describes exactly the reservation.
func (request ReservationRequest) matches(reservation BudgetReservation) bool {
	return request.ReservationId == reservation.ReservationId &&
		request.IdempotencyKey == reservation.IdempotencyKey &&
		request.GoalId == reservation.GoalId &&
		request.NodeId == reservation.NodeId &&
		request.PlanRevision == reservation.PlanRevision &&
		request.CommandId == reservation.CommandId &&
		request.Estimate == reservation.Estimate
}

// BudgetReservation is the append-only reservation record bound to its
// Goal, node, plan revision, command and estimate (ADR 0019 §4). Revision
// is the CAS generation: every accepted transition bumps it by exactly one.
type BudgetReservation struct {
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	ReservationId        string                         `json:"reservationId"`
	IdempotencyKey       string                         `json:"idempotencyKey"`
	GoalId               string                         `json:"goalId"`
	NodeId               string                         `json:"nodeId"`
	PlanRevision         int64                          `json:"planRevision"`
	CommandId            string                         `json:"commandId"`
	Estimate             NodeEstimate                   `json:"estimate"`
	State                ReservationState               `json:"state"`
	Revision             int64                          `json:"revision"`
	Actual               *NodeEstimate                  `json:"actual"`
}

// Validate fails closed on a missing ownership namespace, malformed ids, a
// non-positive plan revision or CAS revision, an unknown state, an invalid
// estimate, or an actual usage bound outside the settled state.
func (reservation BudgetReservation) Validate() error {
	if err := reservation.AuthorityNamespaceId.Validate(); err != nil {
		return err
	}
	if err := domain.ValidateID(reservation.ReservationId); err != nil {
		return fmt.Errorf("goal: budgetReservation.reservationId: %w", err)
	}
	if err := domain.ValidateID(reservation.IdempotencyKey); err != nil {
		return fmt.Errorf("goal: budgetReservation.idempotencyKey: %w", err)
	}
	if err := domain.ValidateID(reservation.GoalId); err != nil {
		return fmt.Errorf("goal: budgetReservation.goalId: %w", err)
	}
	if err := domain.ValidateID(reservation.NodeId); err != nil {
		return fmt.Errorf("goal: budgetReservation.nodeId: %w", err)
	}
	if reservation.PlanRevision < 1 {
		return fmt.Errorf("goal: budgetReservation.planRevision must be at least 1")
	}
	if err := domain.ValidateID(reservation.CommandId); err != nil {
		return fmt.Errorf("goal: budgetReservation.commandId: %w", err)
	}
	if err := reservation.Estimate.Validate(); err != nil {
		return err
	}
	if err := reservation.State.Validate(); err != nil {
		return err
	}
	if reservation.Revision < 1 {
		return fmt.Errorf("goal: budgetReservation.revision must be at least 1")
	}
	if reservation.Actual == nil {
		if reservation.State == ReservationStateSettled {
			return fmt.Errorf("goal: a settled budgetReservation must bind actual usage")
		}
		return nil
	}
	if reservation.State != ReservationStateSettled {
		return fmt.Errorf("goal: actual usage must stay nil outside the settled state")
	}
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"budgetReservation.actual.runs", reservation.Actual.Runs},
		{"budgetReservation.actual.attempts", reservation.Actual.Attempts},
		{"budgetReservation.actual.wallTimeSeconds", reservation.Actual.WallTimeSeconds},
		{"budgetReservation.actual.computeUnits", reservation.Actual.ComputeUnits},
		{"budgetReservation.actual.tokens", reservation.Actual.Tokens},
		{"budgetReservation.actual.artifactBytes", reservation.Actual.ArtifactBytes},
	} {
		if field.value < 0 {
			return fmt.Errorf("goal: %s must not be negative", field.name)
		}
	}
	return nil
}

// Canonical returns the deterministic serialization of the validated record.
func (reservation BudgetReservation) Canonical() ([]byte, error) {
	if err := reservation.Validate(); err != nil {
		return nil, err
	}
	return canonicalBytes(reservation)
}

// Digest returns the sha256 digest of the canonical serialization.
func (reservation BudgetReservation) Digest() (string, error) {
	canonicalized, err := reservation.Canonical()
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

// Equal reports whether both reservations carry identical field values.
func (reservation BudgetReservation) Equal(other BudgetReservation) bool {
	if reservation.Actual == nil || other.Actual == nil {
		if reservation.Actual != other.Actual {
			return false
		}
		reservation.Actual, other.Actual = nil, nil
		return reservation == other
	}
	actual := *reservation.Actual
	otherActual := *other.Actual
	reservation.Actual, other.Actual = nil, nil
	return reservation == other && actual == otherActual
}

// ReservationEvent is one append-only history entry of the budget ledger.
// Accepted transitions and rejected transition attempts are both appended:
// a rejected attempt records the reservation's state and revision at the
// moment of rejection, so CAS contention losers and every other fail-closed
// transition remain auditable. Idempotent replays of an already recorded
// transition append nothing.
type ReservationEvent struct {
	Sequence      int64            `json:"sequence"`
	ReservationId string           `json:"reservationId"`
	State         ReservationState `json:"state"`
	Revision      int64            `json:"revision"`
}

// SettlementDecision is the judgment produced by settling a reservation.
// When actual usage exceeds the reserved estimate on any dimension, Core
// halts new dispatch; this phase produces the decision only and never
// executes or books negative balances.
type SettlementDecision struct {
	HaltNewDispatch bool   `json:"haltNewDispatch"`
	Reason          string `json:"reason"`
}

// BudgetLedger is the in-memory append-only budget reservation state
// machine of one Goal. Live reservations arise only in the same transaction
// as their accepted plan revision; every transition validates the current
// state and the caller's CAS expectation, and replays are idempotent or fail
// closed. The ledger never books negative balances and never oversells:
// once actual usage exceeds a reserved estimate, new dispatch is halted.
type BudgetLedger struct {
	mu            sync.Mutex
	namespace     authority.AuthorityNamespaceId
	goalID        string
	limits        Guardrails
	used          BudgetUsage
	revisions     []AcceptedGoalPlanRevision
	reservations  map[string]*BudgetReservation
	byIdempotency map[string]string
	history       []ReservationEvent
	halted        bool
}

// NewBudgetLedger constructs the ledger from the frozen budget snapshot
// record; limits and cumulative usage are carried over unchanged.
func NewBudgetLedger(budget GoalBudgetLedger) (*BudgetLedger, error) {
	if err := budget.Validate(); err != nil {
		return nil, err
	}
	return &BudgetLedger{
		namespace:     budget.AuthorityNamespaceId,
		goalID:        budget.GoalId,
		limits:        budget.Limits,
		used:          budget.Used,
		reservations:  make(map[string]*BudgetReservation),
		byIdempotency: make(map[string]string),
	}, nil
}

// Snapshot returns the current limits and cumulative usage as the frozen
// record type accepted revisions bind.
func (ledger *BudgetLedger) Snapshot() GoalBudgetLedger {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return GoalBudgetLedger{
		AuthorityNamespaceId: ledger.namespace,
		GoalId:               ledger.goalID,
		Limits:               ledger.limits,
		Used:                 ledger.used,
	}
}

// AcceptedRevisions returns copies of every accepted plan revision in
// append order.
func (ledger *BudgetLedger) AcceptedRevisions() []AcceptedGoalPlanRevision {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	revisions := make([]AcceptedGoalPlanRevision, len(ledger.revisions))
	copy(revisions, ledger.revisions)
	return revisions
}

// Events returns a copy of the append-only reservation history.
func (ledger *BudgetLedger) Events() []ReservationEvent {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	events := make([]ReservationEvent, len(ledger.history))
	copy(events, ledger.history)
	return events
}

// HaltNewDispatch reports whether actual usage exceeded a reserved estimate
// and new dispatch is halted.
func (ledger *BudgetLedger) HaltNewDispatch() bool {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.halted
}

// LiveReservations returns copies of every reserved or committed
// reservation ordered by reservationId.
func (ledger *BudgetLedger) LiveReservations() []BudgetReservation {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return ledger.liveReservationsLocked()
}

// LiveReservationCount returns the number of reserved or committed
// reservations.
func (ledger *BudgetLedger) LiveReservationCount() int {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return len(ledger.liveReservationsLocked())
}

func (ledger *BudgetLedger) liveReservationsLocked() []BudgetReservation {
	live := make([]BudgetReservation, 0, len(ledger.reservations))
	for _, reservation := range ledger.reservations {
		if reservation.State == ReservationStateReserved || reservation.State == ReservationStateCommitted {
			live = append(live, *reservation)
		}
	}
	sort.Slice(live, func(i, j int) bool {
		return live[i].ReservationId < live[j].ReservationId
	})
	return live
}

// Availability returns the remaining budget per dimension: limits minus
// cumulative usage minus live reservations. Values may be negative after an
// over-actual settlement; the halt decision, not negative bookkeeping,
// governs further dispatch.
func (ledger *BudgetLedger) Availability() BudgetUsage {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	reserved := ledger.liveReservedTotalsLocked()
	return BudgetUsage{
		PlanRevisions:   ledger.limits.MaxPlanRevisions - ledger.used.PlanRevisions,
		TotalRuns:       ledger.limits.MaxTotalRuns - ledger.used.TotalRuns - reserved.Runs,
		TotalAttempts:   ledger.limits.MaxTotalAttempts - ledger.used.TotalAttempts - reserved.Attempts,
		WallTimeSeconds: ledger.limits.MaxWallTimeSeconds - ledger.used.WallTimeSeconds - reserved.WallTimeSeconds,
		ComputeUnits:    ledger.limits.MaxComputeUnits - ledger.used.ComputeUnits - reserved.ComputeUnits,
		Tokens:          ledger.limits.MaxTokens - ledger.used.Tokens - reserved.Tokens,
		ArtifactBytes:   ledger.limits.MaxArtifactBytes - ledger.used.ArtifactBytes - reserved.ArtifactBytes,
	}
}

func (ledger *BudgetLedger) liveReservedTotalsLocked() NodeEstimate {
	total := NodeEstimate{}
	for _, reservation := range ledger.reservations {
		if reservation.State == ReservationStateReserved || reservation.State == ReservationStateCommitted {
			total = total.Add(reservation.Estimate)
		}
	}
	return total
}

// ApplyPlan appends one accepted plan revision together with its
// reservation records in one all-or-nothing in-memory transaction, the
// phase-1 expression of the ADR 0019 §4 step 8 atomicity. Replaying the
// exact same revision is idempotent and never creates a second reservation;
// any CAS, binding, availability or replay-content failure leaves the live
// reservation count unchanged.
func (ledger *BudgetLedger) ApplyPlan(revision AcceptedGoalPlanRevision, requests []ReservationRequest) ([]BudgetReservation, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()

	if err := revision.Validate(); err != nil {
		return nil, fmt.Errorf("goal: applyPlan: %w", err)
	}
	if !revision.AuthorityNamespaceId.Equal(ledger.namespace) || revision.GoalId != ledger.goalID {
		return nil, fmt.Errorf("%w: the revision belongs to a different goal or namespace", ErrPlanRevisionConflict)
	}
	for index, request := range requests {
		if err := request.Validate(); err != nil {
			return nil, fmt.Errorf("goal: applyPlan: requests[%d]: %w", index, err)
		}
		if request.GoalId != ledger.goalID {
			return nil, fmt.Errorf("%w: requests[%d] belongs to a different goal", ErrPlanRevisionConflict, index)
		}
	}

	revisionDigest, err := revision.Digest()
	if err != nil {
		return nil, fmt.Errorf("goal: applyPlan: %w", err)
	}

	// Idempotent replay of an already applied revision: return the existing
	// reservations without appending a second copy.
	if count := len(ledger.revisions); count > 0 {
		last := ledger.revisions[count-1]
		lastDigest, err := last.Digest()
		if err != nil {
			return nil, fmt.Errorf("goal: applyPlan: %w", err)
		}
		if lastDigest == revisionDigest {
			existing := ledger.reservationsForRevisionLocked(last.PlanRevision)
			if len(existing) != len(requests) {
				return nil, fmt.Errorf("%w: replayed plan revision carries a different reservation count", ErrIdempotencyConflict)
			}
			sortedExisting := make([]BudgetReservation, len(existing))
			copy(sortedExisting, existing)
			sort.Slice(sortedExisting, func(i, j int) bool {
				return sortedExisting[i].ReservationId < sortedExisting[j].ReservationId
			})
			for index, request := range sortReservationRequests(requests) {
				if !request.matches(sortedExisting[index]) {
					return nil, fmt.Errorf("%w: replayed plan revision carries diverging reservation content", ErrIdempotencyConflict)
				}
			}
			return existing, nil
		}
	}

	// CAS: the revision must extend the chain by exactly one.
	if revision.PlanRevision != int64(len(ledger.revisions))+1 {
		return nil, fmt.Errorf("%w: expected planRevision %d, got %d", ErrPlanRevisionConflict, len(ledger.revisions)+1, revision.PlanRevision)
	}
	if len(ledger.revisions) == 0 {
		if revision.PreviousPlanDigest != "" {
			return nil, fmt.Errorf("%w: the first plan revision must carry an empty previousPlanDigest", ErrPlanRevisionConflict)
		}
	} else {
		lastDigest, err := ledger.revisions[len(ledger.revisions)-1].Digest()
		if err != nil {
			return nil, fmt.Errorf("goal: applyPlan: %w", err)
		}
		if revision.PreviousPlanDigest != lastDigest {
			return nil, fmt.Errorf("%w: previousPlanDigest does not bind the current revision", ErrPlanRevisionConflict)
		}
	}

	// Budget snapshot binding: the revision must bind exactly the current
	// ledger snapshot.
	snapshotDigest, err := ledger.snapshotLocked().Digest()
	if err != nil {
		return nil, fmt.Errorf("goal: applyPlan: %w", err)
	}
	if revision.BudgetSnapshotDigest != snapshotDigest {
		return nil, fmt.Errorf("%w: budgetSnapshotDigest does not bind the current ledger snapshot", ErrPlanRevisionConflict)
	}

	if ledger.halted {
		return nil, fmt.Errorf("%w: actual usage exceeded a reserved estimate", ErrDispatchHalted)
	}
	if ledger.used.PlanRevisions+1 > ledger.limits.MaxPlanRevisions {
		return nil, fmt.Errorf("%w: maxPlanRevisions", ErrBudgetExhausted)
	}

	// Availability: never oversell. Aggregate every request on top of the
	// current live reservations before mutating anything.
	projected := ledger.liveReservedTotalsLocked()
	seenReservationIds := make(map[string]struct{}, len(requests))
	seenIdempotencyKeys := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		if _, duplicate := seenReservationIds[request.ReservationId]; duplicate {
			return nil, fmt.Errorf("%w: duplicate reservationId %s", ErrPlanRevisionConflict, request.ReservationId)
		}
		seenReservationIds[request.ReservationId] = struct{}{}
		if _, duplicate := seenIdempotencyKeys[request.IdempotencyKey]; duplicate {
			return nil, fmt.Errorf("%w: duplicate idempotencyKey %s", ErrPlanRevisionConflict, request.IdempotencyKey)
		}
		seenIdempotencyKeys[request.IdempotencyKey] = struct{}{}
		if _, exists := ledger.reservations[request.ReservationId]; exists {
			return nil, fmt.Errorf("%w: reservationId %s already exists", ErrPlanRevisionConflict, request.ReservationId)
		}
		if _, exists := ledger.byIdempotency[request.IdempotencyKey]; exists {
			return nil, fmt.Errorf("%w: idempotencyKey %s already exists", ErrPlanRevisionConflict, request.IdempotencyKey)
		}
		if request.PlanRevision != revision.PlanRevision {
			return nil, fmt.Errorf("%w: request %s binds planRevision %d, the revision carries %d", ErrPlanRevisionConflict, request.ReservationId, request.PlanRevision, revision.PlanRevision)
		}
		projected = projected.Add(request.Estimate)
	}
	checks := []struct {
		name  string
		value int64
		bound int64
	}{
		{"maxTotalRuns", ledger.used.TotalRuns + projected.Runs, ledger.limits.MaxTotalRuns},
		{"maxTotalAttempts", ledger.used.TotalAttempts + projected.Attempts, ledger.limits.MaxTotalAttempts},
		{"maxWallTimeSeconds", ledger.used.WallTimeSeconds + projected.WallTimeSeconds, ledger.limits.MaxWallTimeSeconds},
		{"maxComputeUnits", ledger.used.ComputeUnits + projected.ComputeUnits, ledger.limits.MaxComputeUnits},
		{"maxTokens", ledger.used.Tokens + projected.Tokens, ledger.limits.MaxTokens},
		{"maxArtifactBytes", ledger.used.ArtifactBytes + projected.ArtifactBytes, ledger.limits.MaxArtifactBytes},
	}
	for _, check := range checks {
		if check.value > check.bound {
			return nil, fmt.Errorf("%w: %s", ErrBudgetExhausted, check.name)
		}
	}

	// All checks passed: commit the transaction.
	ledger.revisions = append(ledger.revisions, revision)
	ledger.used.PlanRevisions++
	created := make([]BudgetReservation, 0, len(requests))
	for _, request := range requests {
		reservation := &BudgetReservation{
			AuthorityNamespaceId: ledger.namespace,
			ReservationId:        request.ReservationId,
			IdempotencyKey:       request.IdempotencyKey,
			GoalId:               ledger.goalID,
			NodeId:               request.NodeId,
			PlanRevision:         revision.PlanRevision,
			CommandId:            request.CommandId,
			Estimate:             request.Estimate,
			State:                ReservationStateReserved,
			Revision:             1,
		}
		ledger.reservations[reservation.ReservationId] = reservation
		ledger.byIdempotency[reservation.IdempotencyKey] = reservation.ReservationId
		ledger.appendEventLocked(reservation)
		created = append(created, *reservation)
	}
	return created, nil
}

func (ledger *BudgetLedger) snapshotLocked() GoalBudgetLedger {
	return GoalBudgetLedger{
		AuthorityNamespaceId: ledger.namespace,
		GoalId:               ledger.goalID,
		Limits:               ledger.limits,
		Used:                 ledger.used,
	}
}

func (ledger *BudgetLedger) reservationsForRevisionLocked(planRevision int64) []BudgetReservation {
	var matches []BudgetReservation
	for _, reservation := range ledger.reservations {
		if reservation.PlanRevision == planRevision {
			matches = append(matches, *reservation)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ReservationId < matches[j].ReservationId
	})
	return matches
}

// appendEventLocked appends the reservation's current state and revision as
// the next append-only history entry. It records accepted transitions and is
// also invoked before every fail-closed transition return, so a rejected
// attempt — including the loser of a CAS race — is auditable; idempotent
// replays never append.
func (ledger *BudgetLedger) appendEventLocked(reservation *BudgetReservation) {
	ledger.history = append(ledger.history, ReservationEvent{
		Sequence:      int64(len(ledger.history)) + 1,
		ReservationId: reservation.ReservationId,
		State:         reservation.State,
		Revision:      reservation.Revision,
	})
}

// Commit moves a reservation reserved → committed when dispatch is
// accepted. The caller must present the current reservation revision; a
// lost-response replay of an already committed reservation with the matching
// pre-transition revision is idempotent, and any other state fails closed.
func (ledger *BudgetLedger) Commit(reservationID string, expectedRevision int64) (BudgetReservation, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	reservation, err := ledger.locateLocked(reservationID)
	if err != nil {
		return BudgetReservation{}, err
	}
	if reservation.State == ReservationStateCommitted && reservation.Revision == expectedRevision+1 {
		return *reservation, nil
	}
	if reservation.State != ReservationStateReserved {
		ledger.appendEventLocked(reservation)
		return BudgetReservation{}, fmt.Errorf("%w: cannot commit from state %s", ErrReservationConflict, string(reservation.State))
	}
	if reservation.Revision != expectedRevision {
		ledger.appendEventLocked(reservation)
		return BudgetReservation{}, fmt.Errorf("%w: expected revision %d, the reservation carries %d", ErrReservationConflict, expectedRevision, reservation.Revision)
	}
	reservation.State = ReservationStateCommitted
	reservation.Revision++
	ledger.appendEventLocked(reservation)
	return *reservation, nil
}

// Settle moves a reservation committed → settled with the actual usage and
// adds the actuals to the cumulative ledger. A duplicate settle replaying
// identical actuals is idempotent; diverging actuals fail closed. When any
// actual dimension exceeds the reserved estimate, the ledger halts new
// dispatch and returns the halt decision; it never books a negative balance
// to keep overselling.
func (ledger *BudgetLedger) Settle(reservationID string, expectedRevision int64, actual NodeEstimate) (BudgetReservation, SettlementDecision, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	reservation, err := ledger.locateLocked(reservationID)
	if err != nil {
		return BudgetReservation{}, SettlementDecision{}, err
	}
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"actual.runs", actual.Runs},
		{"actual.attempts", actual.Attempts},
		{"actual.wallTimeSeconds", actual.WallTimeSeconds},
		{"actual.computeUnits", actual.ComputeUnits},
		{"actual.tokens", actual.Tokens},
		{"actual.artifactBytes", actual.ArtifactBytes},
	} {
		if field.value < 0 {
			ledger.appendEventLocked(reservation)
			return BudgetReservation{}, SettlementDecision{}, fmt.Errorf("%w: %s must not be negative", ErrReservationConflict, field.name)
		}
	}
	decision := SettlementDecision{}
	if over := actual.Exceeds(reservation.Estimate); len(over) > 0 {
		decision.HaltNewDispatch = true
		decision.Reason = "actual usage exceeded reserved estimate: " + strings.Join(over, ", ")
	}
	if reservation.State == ReservationStateSettled && reservation.Revision == expectedRevision+1 {
		if reservation.Actual != nil && *reservation.Actual == actual {
			return *reservation, decision, nil
		}
		ledger.appendEventLocked(reservation)
		return BudgetReservation{}, SettlementDecision{}, fmt.Errorf("%w: duplicate settle with diverging actual usage", ErrReservationConflict)
	}
	if reservation.State != ReservationStateCommitted {
		ledger.appendEventLocked(reservation)
		return BudgetReservation{}, SettlementDecision{}, fmt.Errorf("%w: cannot settle from state %s", ErrReservationConflict, string(reservation.State))
	}
	if reservation.Revision != expectedRevision {
		ledger.appendEventLocked(reservation)
		return BudgetReservation{}, SettlementDecision{}, fmt.Errorf("%w: expected revision %d, the reservation carries %d", ErrReservationConflict, expectedRevision, reservation.Revision)
	}
	actualCopy := actual
	reservation.State = ReservationStateSettled
	reservation.Revision++
	reservation.Actual = &actualCopy
	ledger.used = ledger.used.AddEstimate(actual)
	if decision.HaltNewDispatch {
		ledger.halted = true
	}
	ledger.appendEventLocked(reservation)
	return *reservation, decision, nil
}

// Release moves a reservation reserved → released. The caller must bind the
// reservation's own plan revision; a stale revision release fails closed.
// Release from any non-reserved state fails closed, and an idempotent replay
// with the matching pre-transition revision returns the released record.
func (ledger *BudgetLedger) Release(reservationID string, expectedRevision, planRevision int64) (BudgetReservation, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	reservation, err := ledger.locateLocked(reservationID)
	if err != nil {
		return BudgetReservation{}, err
	}
	if planRevision != reservation.PlanRevision {
		ledger.appendEventLocked(reservation)
		return BudgetReservation{}, fmt.Errorf("%w: release binds planRevision %d, the reservation carries %d", ErrReservationStale, planRevision, reservation.PlanRevision)
	}
	if reservation.State == ReservationStateReleased && reservation.Revision == expectedRevision+1 {
		return *reservation, nil
	}
	if reservation.State != ReservationStateReserved {
		ledger.appendEventLocked(reservation)
		return BudgetReservation{}, fmt.Errorf("%w: cannot release from state %s", ErrReservationConflict, string(reservation.State))
	}
	if reservation.Revision != expectedRevision {
		ledger.appendEventLocked(reservation)
		return BudgetReservation{}, fmt.Errorf("%w: expected revision %d, the reservation carries %d", ErrReservationConflict, expectedRevision, reservation.Revision)
	}
	reservation.State = ReservationStateReleased
	reservation.Revision++
	ledger.appendEventLocked(reservation)
	return *reservation, nil
}

// Expire moves a reservation reserved → expired when its deadline passes.
// Expire from any non-reserved state fails closed, and an idempotent replay
// with the matching pre-transition revision returns the expired record.
func (ledger *BudgetLedger) Expire(reservationID string, expectedRevision int64) (BudgetReservation, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	reservation, err := ledger.locateLocked(reservationID)
	if err != nil {
		return BudgetReservation{}, err
	}
	if reservation.State == ReservationStateExpired && reservation.Revision == expectedRevision+1 {
		return *reservation, nil
	}
	if reservation.State != ReservationStateReserved {
		ledger.appendEventLocked(reservation)
		return BudgetReservation{}, fmt.Errorf("%w: cannot expire from state %s", ErrReservationConflict, string(reservation.State))
	}
	if reservation.Revision != expectedRevision {
		ledger.appendEventLocked(reservation)
		return BudgetReservation{}, fmt.Errorf("%w: expected revision %d, the reservation carries %d", ErrReservationConflict, expectedRevision, reservation.Revision)
	}
	reservation.State = ReservationStateExpired
	reservation.Revision++
	ledger.appendEventLocked(reservation)
	return *reservation, nil
}

func (ledger *BudgetLedger) locateLocked(reservationID string) (*BudgetReservation, error) {
	if err := domain.ValidateID(reservationID); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrReservationNotFound, reservationID)
	}
	reservation, ok := ledger.reservations[reservationID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrReservationNotFound, reservationID)
	}
	return reservation, nil
}
