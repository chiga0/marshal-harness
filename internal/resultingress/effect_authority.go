package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

const effectAuthorityProtocolRevision = "effect-authority/v1"

const (
	effectFactTypeIntent    = "effect-intent"
	effectFactTypeReceipt   = "effect-receipt"
	effectFactTypeReconcile = "effect-reconcile"
)

var (
	ErrEffectAuthorityConflict = errors.New("resultingress: effect authority conflict")
	ErrEffectAuthorityOrder    = errors.New("resultingress: effect authority fact out of order")
	ErrEffectAuthorityUnknown  = errors.New("resultingress: effect authority unknown")
	ErrEffectAuthorityExpired  = errors.New("resultingress: effect authority deadline expired")
)

// EffectPhase is the closed v1 allocation effect union. Provision is admitted
// before launch. Terminate is admitted only after an exact process-terminal
// fact under the terminal cleanup binding.
type EffectPhase string

const (
	EffectPhaseAllocationProvision EffectPhase = "allocation-provision"
	EffectPhaseAllocationTerminate EffectPhase = "allocation-terminate"
)

func (phase EffectPhase) Validate() error {
	switch phase {
	case EffectPhaseAllocationProvision, EffectPhaseAllocationTerminate:
		return nil
	default:
		return fmt.Errorf("%w: unknown phase %q", ErrEffectAuthorityConflict, phase)
	}
}

// EffectBinding is immutable across intent, receipt and reconcile facts. The
// admission revision/head is the exact Attempt authority observed before the
// intent CAS. MarkerDigest is the planned allocation identity marker, not a
// pathname and not a bearer capability.
type EffectBinding struct {
	Identity                  AttemptIdentity     `json:"identity"`
	CurrentRunAuthority       RunAuthorityBinding `json:"currentRunAuthority"`
	AdmissionAttemptRevision  uint64              `json:"admissionAttemptRevision"`
	AdmissionAuthorityDigest  string              `json:"admissionAuthorityFactDigest"`
	Phase                     EffectPhase         `json:"phase"`
	MarkerDigest              string              `json:"markerDigest"`
	TerminalizationID         string              `json:"terminalizationId,omitempty"`
	TerminalGeneration        int64               `json:"terminalGeneration,omitempty"`
	CleanupBindingDigest      string              `json:"cleanupBindingDigest,omitempty"`
	ProcessTerminalFactDigest string              `json:"processTerminalFactDigest,omitempty"`
}

func (binding EffectBinding) Validate() error {
	if err := binding.Identity.Validate(); err != nil {
		return err
	}
	if binding.CurrentRunAuthority != runAuthorityBindingFor(binding.Identity) {
		return fmt.Errorf("%w: current Run authority does not match Attempt identity", ErrEffectAuthorityConflict)
	}
	if binding.AdmissionAttemptRevision == 0 {
		return fmt.Errorf("%w: admission Attempt revision must be positive", ErrEffectAuthorityConflict)
	}
	if err := requireDigest("admissionAuthorityFactDigest", binding.AdmissionAuthorityDigest); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
	}
	if err := binding.Phase.Validate(); err != nil {
		return err
	}
	if err := requireDigest("markerDigest", binding.MarkerDigest); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
	}
	switch binding.Phase {
	case EffectPhaseAllocationProvision:
		if binding.TerminalizationID != "" || binding.TerminalGeneration != 0 || binding.CleanupBindingDigest != "" || binding.ProcessTerminalFactDigest != "" {
			return fmt.Errorf("%w: provision binding carries terminal cleanup fields", ErrEffectAuthorityConflict)
		}
	case EffectPhaseAllocationTerminate:
		if strings.TrimSpace(binding.TerminalizationID) == "" || binding.TerminalGeneration < 1 {
			return fmt.Errorf("%w: terminate binding has incomplete terminal identity", ErrEffectAuthorityConflict)
		}
		if err := requireDigest("cleanupBindingDigest", binding.CleanupBindingDigest); err != nil {
			return fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
		}
		if err := requireDigest("processTerminalFactDigest", binding.ProcessTerminalFactDigest); err != nil {
			return fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
		}
	}
	return nil
}

// EffectIntentRequest admits one generic SideEffectIntent into the Attempt
// authority. The generic record is retained verbatim and its canonical digest
// is redundantly sealed into the closed authority fact.
type EffectIntentRequest struct {
	Binding EffectBinding
	Intent  authority.SideEffectIntent
}

// EffectUseRequest identifies an already admitted intent. IntentFactDigest is
// the authority fact digest, distinct from the generic intent record digest.
type EffectUseRequest struct {
	Binding          EffectBinding
	EffectID         string
	IntentFactDigest string
	IntentDeadline   string
}

// CurrentEffectAuthorityCheck is the exact authority tuple an implementation
// must hold while inspect/apply callbacks run. Identity seals the DispatchLease
// id, digest, generation and fencing digest; the verifier additionally proves
// that lease eligibility is still active (not expired, cancelled or revoked).
type CurrentEffectAuthorityCheck struct {
	Identity            AttemptIdentity     `json:"identity"`
	CurrentRunAuthority RunAuthorityBinding `json:"currentRunAuthority"`
	Now                 string              `json:"now"`
}

type CurrentEffectAuthorityVerifier interface {
	// WithCurrentEffectAuthority holds both current Run authority and the exact
	// active DispatchLease eligibility for the complete callback. ResultIngress
	// then replays and verifies the exact current Attempt head under that held
	// callback, preserving Run authority -> ledger flock lock order.
	WithCurrentEffectAuthority(context.Context, CurrentEffectAuthorityCheck, func() error) error
}

func withCurrentEffectAuthority(ctx context.Context, verifier CurrentEffectAuthorityVerifier, check CurrentEffectAuthorityCheck, fn func() error) error {
	if verifier == nil {
		return fmt.Errorf("%w: verifier is required", ErrEffectAuthorityConflict)
	}
	var gate sync.Mutex
	called, doubleCall, closed := false, false, false
	var callbackErr error
	verifierErr := verifier.WithCurrentEffectAuthority(ctx, check, func() error {
		gate.Lock()
		defer gate.Unlock()
		if closed || called {
			doubleCall = true
			return ErrEffectAuthorityConflict
		}
		called = true
		callbackErr = fn()
		return callbackErr
	})
	gate.Lock()
	closed = true
	calledOnce, invokedTwice, heldErr := called, doubleCall, callbackErr
	gate.Unlock()
	if invokedTwice {
		return fmt.Errorf("%w: verifier invoked held callback more than once", ErrEffectAuthorityConflict)
	}
	if heldErr != nil {
		return heldErr
	}
	if verifierErr != nil || !calledOnce {
		return fmt.Errorf("%w: current Run or DispatchLease authority rejected: %v", ErrEffectAuthorityConflict, verifierErr)
	}
	return nil
}

// EffectInspectionOutcome is the closed inspect result union. Only an exact
// not_applied observation can authorize Apply; ambiguous/conflict/unknown
// never mutate the provider.
type EffectInspectionOutcome string

const (
	EffectInspectionNotApplied EffectInspectionOutcome = "not_applied"
	EffectInspectionApplied    EffectInspectionOutcome = "applied"
	EffectInspectionAmbiguous  EffectInspectionOutcome = "ambiguous"
	EffectInspectionConflict   EffectInspectionOutcome = "conflict"
	EffectInspectionUnknown    EffectInspectionOutcome = "unknown"
)

type EffectInspection struct {
	Outcome EffectInspectionOutcome     `json:"outcome"`
	Receipt authority.SideEffectReceipt `json:"receipt"`
}

// EffectOperator is the only mutation seam: every recovery invokes Inspect
// first and Core invokes Apply only after exact not_applied. Private fields
// prevent callers from substituting an alternate decision/reconcile callback.
type EffectOperator struct {
	inspect func(context.Context, EffectAuthorityState) (EffectInspection, error)
	apply   func(context.Context, EffectAuthorityState) (authority.SideEffectReceipt, error)
}

func NewEffectOperator(
	inspect func(context.Context, EffectAuthorityState) (EffectInspection, error),
	apply func(context.Context, EffectAuthorityState) (authority.SideEffectReceipt, error),
) (EffectOperator, error) {
	if inspect == nil || apply == nil {
		return EffectOperator{}, fmt.Errorf("%w: inspect and apply are required", ErrEffectAuthorityConflict)
	}
	return EffectOperator{inspect: inspect, apply: apply}, nil
}

func (request EffectUseRequest) validate() error {
	if err := request.Binding.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(request.EffectID) == "" {
		return fmt.Errorf("%w: effectId is empty", ErrEffectAuthorityConflict)
	}
	if err := requireDigest("intentFactDigest", request.IntentFactDigest); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
	}
	if strings.TrimSpace(request.IntentDeadline) == "" {
		return fmt.Errorf("%w: intentDeadline is empty", ErrEffectAuthorityConflict)
	}
	return nil
}

// EffectAuthorityState is a deterministic replay projection. A non-empty
// ReconcileFactDigest means the pending barrier is closed.
type EffectAuthorityState struct {
	Binding               EffectBinding               `json:"binding"`
	Intent                authority.SideEffectIntent  `json:"intent"`
	IntentRecordDigest    string                      `json:"intentRecordDigest"`
	IntentFactDigest      string                      `json:"intentFactDigest"`
	Receipt               authority.SideEffectReceipt `json:"receipt,omitempty"`
	ReceiptRecordDigest   string                      `json:"receiptRecordDigest,omitempty"`
	ReceiptFactDigest     string                      `json:"receiptFactDigest,omitempty"`
	Reconcile             authority.ReconcileRecord   `json:"reconcile,omitempty"`
	ReconcileRecordDigest string                      `json:"reconcileRecordDigest,omitempty"`
	ReconcileFactDigest   string                      `json:"reconcileFactDigest,omitempty"`
	ReconcileInspection   EffectInspection            `json:"reconcileInspection,omitempty"`
	InspectionDigest      string                      `json:"inspectionDigest,omitempty"`
}

// EffectAppendResult distinguishes a fresh append from an exact replay.
type EffectAppendResult struct {
	State      EffectAuthorityState
	Appended   bool
	FactDigest string
}

type effectAuthorityFact struct {
	ProtocolRevision      string                       `json:"protocolRevision"`
	FactType              string                       `json:"factType"`
	Sequence              int64                        `json:"sequence"`
	AttemptKey            string                       `json:"attemptKey"`
	AttemptRevision       uint64                       `json:"attemptRevision"`
	PreviousAttemptHead   string                       `json:"previousAttemptHead"`
	Binding               EffectBinding                `json:"binding"`
	EffectID              string                       `json:"effectId"`
	Intent                *authority.SideEffectIntent  `json:"intent,omitempty"`
	IntentRecordDigest    string                       `json:"intentRecordDigest"`
	IntentFactDigest      string                       `json:"intentFactDigest,omitempty"`
	Receipt               *authority.SideEffectReceipt `json:"receipt,omitempty"`
	ReceiptRecordDigest   string                       `json:"receiptRecordDigest,omitempty"`
	ReceiptFactDigest     string                       `json:"receiptFactDigest,omitempty"`
	Reconcile             *authority.ReconcileRecord   `json:"reconcile,omitempty"`
	ReconcileRecordDigest string                       `json:"reconcileRecordDigest,omitempty"`
	Inspection            *EffectInspection            `json:"inspection,omitempty"`
	InspectionDigest      string                       `json:"inspectionDigest,omitempty"`
	Digest                string                       `json:"digest"`
}

// CompareAndAppendEffectIntent holds current Run authority across the complete
// replay/read/CAS transaction. The append and fsync establish the pending
// effect barrier before any caller is allowed to invoke a Provider.
func (s *ingressDurableStore) CompareAndAppendEffectIntent(ctx context.Context, verifier CurrentEffectAuthorityVerifier, request EffectIntentRequest) (EffectAppendResult, error) {
	if err := validateEffectIntentRequest(request); err != nil {
		return EffectAppendResult{}, err
	}
	now, err := s.currentEffectTime(request.Intent.Deadline)
	if err != nil {
		return EffectAppendResult{}, err
	}
	check := effectAuthorityCheck(request.Binding, now)
	var result EffectAppendResult
	err = withCurrentEffectAuthority(ctx, verifier, check, func() error {
		if _, err := s.currentEffectTime(request.Intent.Deadline); err != nil {
			return err
		}
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			var err error
			result, err = s.appendEffectIntentLocked(projection, request)
			return err
		})
	})
	return result, err
}

func effectAuthorityCheck(binding EffectBinding, now time.Time) CurrentEffectAuthorityCheck {
	return CurrentEffectAuthorityCheck{
		Identity: binding.Identity, CurrentRunAuthority: binding.CurrentRunAuthority,
		Now: now.UTC().Format(time.RFC3339Nano),
	}
}

func (s *ingressDurableStore) currentEffectTime(deadlineText string) (time.Time, error) {
	deadline, err := time.Parse(time.RFC3339Nano, deadlineText)
	if err != nil || deadline.UTC().Format(time.RFC3339Nano) != deadlineText {
		return time.Time{}, fmt.Errorf("%w: deadline is not canonical UTC RFC3339Nano", ErrEffectAuthorityConflict)
	}
	now := s.authorityNow()
	if !now.Before(deadline) {
		return time.Time{}, ErrEffectAuthorityExpired
	}
	return now, nil
}

func validateEffectIntentRequest(request EffectIntentRequest) error {
	if err := request.Binding.Validate(); err != nil {
		return err
	}
	if err := request.Intent.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
	}
	if request.Intent.AuthorityNamespaceId != request.Binding.Identity.AuthorityNamespaceID || request.Intent.OwnerIdentity != request.Binding.Identity.OrchestratorID || request.Intent.TargetRef != request.Binding.Identity.AllocationID || request.Intent.AuthorizationDigest != request.Binding.AdmissionAuthorityDigest {
		return fmt.Errorf("%w: generic intent does not match authority binding", ErrEffectAuthorityConflict)
	}
	switch request.Binding.Phase {
	case EffectPhaseAllocationProvision:
		if request.Intent.DispositionClass != authority.DispositionClassSandboxProvision || request.Intent.Operation != string(EffectPhaseAllocationProvision) {
			return fmt.Errorf("%w: provision phase/intent mismatch", ErrEffectAuthorityConflict)
		}
	case EffectPhaseAllocationTerminate:
		if request.Intent.DispositionClass != authority.DispositionClassSandboxTerminate || request.Intent.Operation != string(EffectPhaseAllocationTerminate) {
			return fmt.Errorf("%w: terminate phase/intent mismatch", ErrEffectAuthorityConflict)
		}
	}
	return nil
}

func (s *ingressDurableStore) appendEffectIntentLocked(projection *Ingress, request EffectIntentRequest) (EffectAppendResult, error) {
	effectKey, err := effectKey(request.Binding.Identity.AuthorityNamespaceID, request.Intent.EffectId)
	if err != nil {
		return EffectAppendResult{}, err
	}
	if existing, exists := projection.effects[effectKey]; exists {
		if existing.Binding == request.Binding && existing.Intent == request.Intent {
			return EffectAppendResult{State: existing, FactDigest: existing.IntentFactDigest}, nil
		}
		return EffectAppendResult{}, ErrEffectAuthorityConflict
	}
	if err := checkEffectIndexes(projection, effectKey, request); err != nil {
		return EffectAppendResult{}, err
	}
	attemptKey, err := request.Binding.Identity.Key()
	if err != nil {
		return EffectAppendResult{}, err
	}
	prior, exists := projection.attempts[attemptKey]
	if !exists {
		return EffectAppendResult{}, ErrAttemptAuthorityUnknown
	}
	if err := validateIntentPhase(prior, request.Binding); err != nil {
		return EffectAppendResult{}, err
	}
	intentDigest, err := request.Intent.Digest()
	if err != nil {
		return EffectAppendResult{}, err
	}
	intent := request.Intent
	fact := &effectAuthorityFact{
		ProtocolRevision: effectAuthorityProtocolRevision,
		FactType:         effectFactTypeIntent, Sequence: s.nextSequence,
		AttemptKey: attemptKey, AttemptRevision: prior.Revision + 1,
		PreviousAttemptHead: prior.HeadDigest, Binding: request.Binding,
		EffectID: request.Intent.EffectId, Intent: &intent, IntentRecordDigest: intentDigest,
	}
	if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
		return EffectAppendResult{}, err
	}
	s.nextSequence++
	if err := applyEffectAuthorityFactValue(*fact, projection); err != nil {
		return EffectAppendResult{}, fmt.Errorf("resultingress: appended effect intent failed projection: %w", err)
	}
	state := projection.effects[effectKey]
	return EffectAppendResult{State: state, Appended: true, FactDigest: fact.Digest}, nil
}

func checkEffectIndexes(projection *Ingress, effectKey string, request EffectIntentRequest) error {
	namespaceDigest, err := request.Binding.Identity.AuthorityNamespaceID.Digest()
	if err != nil {
		return err
	}
	for key, index := range map[string]map[string]string{
		namespaceDigest + "\x00command\x00" + request.Intent.CommandId:          projection.effectCommands,
		namespaceDigest + "\x00idempotency\x00" + request.Intent.IdempotencyKey: projection.effectIdempotency,
	} {
		if prior, exists := index[key]; exists && prior != effectKey {
			return ErrEffectAuthorityConflict
		}
	}
	markerKey := namespaceDigest + "\x00marker\x00" + request.Binding.MarkerDigest
	attemptKey, err := request.Binding.Identity.Key()
	if err != nil {
		return err
	}
	if markerOwner, exists := projection.effectMarkers[markerKey]; exists {
		if markerOwner != attemptKey || request.Binding.Phase == EffectPhaseAllocationProvision {
			return ErrEffectAuthorityConflict
		}
	} else if request.Binding.Phase == EffectPhaseAllocationTerminate {
		// A terminate effect cannot invent a marker that has no provision
		// authority history in this store.
		return ErrEffectAuthorityConflict
	}
	return nil
}

func validateIntentPhase(prior AttemptAuthorityState, binding EffectBinding) error {
	if prior.Identity != binding.Identity || prior.Revision != binding.AdmissionAttemptRevision || prior.HeadDigest != binding.AdmissionAuthorityDigest || prior.PendingEffectIntentFactDigest != "" || prior.EffectInterventionDigest != "" {
		return ErrAttemptAuthorityConflict
	}
	switch binding.Phase {
	case EffectPhaseAllocationProvision:
		if prior.LaunchState != LaunchNotAuthorized || prior.LaunchAuthorizedDigest != "" || prior.ProcessStartedDigest != "" || prior.BarrierDigest != "" || prior.AllocationProvisionEffectDigest != "" {
			return ErrEffectAuthorityOrder
		}
	case EffectPhaseAllocationTerminate:
		if prior.BarrierDigest == "" || prior.TerminalizationID != binding.TerminalizationID || prior.TerminalGeneration != binding.TerminalGeneration || prior.CleanupBindingDigest != binding.CleanupBindingDigest || prior.ProcessTerminalDigest != binding.ProcessTerminalFactDigest || prior.AllocationTerminalDigest != "" || prior.AllocationTerminateEffectDigest != "" || prior.CleanupReleasedDigest != "" || (prior.ProcessTerminalKind != ProcessAbsent && prior.ProcessTerminalKind != ProcessTerminated) {
			return ErrEffectAuthorityOrder
		}
	default:
		return ErrEffectAuthorityConflict
	}
	return nil
}

// RecoverPendingEffect is the only effect execution API. It serializes one
// effect within the process, holds exact Run+Attempt+active-lease authority,
// invokes Inspect first, and invokes Apply at most once only for an exact
// not_applied inspection. Provider callbacks never run under the store lock.
func (s *ingressDurableStore) RecoverPendingEffect(ctx context.Context, verifier CurrentEffectAuthorityVerifier, request EffectUseRequest, operator EffectOperator) (EffectAppendResult, error) {
	if operator.inspect == nil || operator.apply == nil {
		return EffectAppendResult{}, fmt.Errorf("%w: closed effect operator is required", ErrEffectAuthorityConflict)
	}
	if err := request.validate(); err != nil {
		return EffectAppendResult{}, err
	}
	key, err := effectKey(request.Binding.Identity.AuthorityNamespaceID, request.EffectID)
	if err != nil {
		return EffectAppendResult{}, err
	}
	var result EffectAppendResult
	err = s.withEffectFlight(key, func() error {
		now, err := s.currentEffectTime(request.IntentDeadline)
		if err != nil {
			return err
		}
		check := effectAuthorityCheck(request.Binding, now)
		return withCurrentEffectAuthority(ctx, verifier, check, func() error {
			current, _, err := s.loadEffectForUse(request)
			if err != nil {
				return err
			}
			if current.ReconcileFactDigest != "" {
				result = EffectAppendResult{State: current, FactDigest: current.ReconcileFactDigest}
				return nil
			}
			if _, err := s.currentEffectTime(current.Intent.Deadline); err != nil {
				return err
			}

			inspection, err := operator.inspect(ctx, current)
			if err != nil {
				return err
			}
			if err := validateEffectInspection(current, inspection); err != nil {
				return err
			}
			switch inspection.Outcome {
			case EffectInspectionApplied:
				result, err = s.closeAppliedInspection(request, current, inspection)
				return err
			case EffectInspectionConflict:
				result, err = s.closeConflictInspection(request, current, inspection)
				return err
			case EffectInspectionAmbiguous, EffectInspectionUnknown:
				// The pending barrier is the durable intervention state. It may be
				// inspected again but can never authorize mutation from ambiguity.
				if current.ReceiptFactDigest == "" {
					result, err = s.appendEffectReceipt(request, current, inspection.Receipt)
				} else {
					result = EffectAppendResult{State: current, FactDigest: current.ReceiptFactDigest}
				}
				return err
			case EffectInspectionNotApplied:
				if current.ReceiptFactDigest != "" && current.Receipt.Disposition == authority.DispositionApplied {
					return ErrEffectAuthorityConflict
				}
				if _, err := s.currentEffectTime(current.Intent.Deadline); err != nil {
					return err
				}
				receipt, err := operator.apply(ctx, current)
				if err != nil {
					// A lost response remains intent-pending. The next invocation must
					// inspect and cannot blindly repeat this Apply.
					return err
				}
				if err := validateEffectReceipt(current, receipt); err != nil {
					return err
				}
				if current.ReceiptFactDigest != "" {
					// Preserve the immutable first receipt. Even an applied Apply
					// response must be confirmed by the next exact Inspect.
					result = EffectAppendResult{State: current, FactDigest: current.ReceiptFactDigest}
					return nil
				}
				result, err = s.appendEffectReceipt(request, current, receipt)
				if err != nil || receipt.Disposition != authority.DispositionApplied {
					return err
				}
				applied := EffectInspection{Outcome: EffectInspectionApplied, Receipt: receipt}
				result, err = s.appendEffectDecision(request, result.State, applied, true)
				return err
			default:
				return ErrEffectAuthorityConflict
			}
		})
	})
	return result, err
}

func (s *ingressDurableStore) loadEffectForUse(request EffectUseRequest) (EffectAuthorityState, AttemptAuthorityState, error) {
	projection := newAuthorityProjection()
	var state EffectAuthorityState
	var attempt AttemptAuthorityState
	err := s.transact(projection, func() error {
		var err error
		state, attempt, err = pendingEffectForUse(projection, request)
		return err
	})
	return state, attempt, err
}

func (s *ingressDurableStore) appendEffectReceipt(request EffectUseRequest, expected EffectAuthorityState, receipt authority.SideEffectReceipt) (EffectAppendResult, error) {
	if err := validateEffectReceipt(expected, receipt); err != nil {
		return EffectAppendResult{}, err
	}
	receiptDigest, err := receipt.Digest()
	if err != nil {
		return EffectAppendResult{}, err
	}
	projection := newAuthorityProjection()
	var result EffectAppendResult
	err = s.transact(projection, func() error {
		state, attempt, err := pendingEffectForUse(projection, request)
		if err != nil {
			return err
		}
		if state.ReceiptFactDigest != "" {
			if state.Receipt != receipt {
				return ErrEffectAuthorityConflict
			}
			result = EffectAppendResult{State: state, FactDigest: state.ReceiptFactDigest}
			return nil
		}
		if state.ReconcileFactDigest != "" || state.IntentFactDigest != expected.IntentFactDigest {
			return ErrAttemptAuthorityConflict
		}
		fact := &effectAuthorityFact{
			ProtocolRevision: effectAuthorityProtocolRevision, FactType: effectFactTypeReceipt,
			Sequence: s.nextSequence, AttemptKey: mustAttemptKey(request.Binding.Identity),
			AttemptRevision: attempt.Revision + 1, PreviousAttemptHead: attempt.HeadDigest,
			Binding: request.Binding, EffectID: request.EffectID,
			IntentRecordDigest: state.IntentRecordDigest, IntentFactDigest: state.IntentFactDigest,
			Receipt: &receipt, ReceiptRecordDigest: receiptDigest,
		}
		if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
			return err
		}
		s.nextSequence++
		if err := applyEffectAuthorityFactValue(*fact, projection); err != nil {
			return fmt.Errorf("resultingress: appended effect receipt failed projection: %w", err)
		}
		result = EffectAppendResult{State: projection.effects[mustEffectKey(request.Binding.Identity.AuthorityNamespaceID, request.EffectID)], Appended: true, FactDigest: fact.Digest}
		return nil
	})
	return result, err
}

func (s *ingressDurableStore) closeAppliedInspection(request EffectUseRequest, state EffectAuthorityState, inspection EffectInspection) (EffectAppendResult, error) {
	var result EffectAppendResult
	var err error
	if state.ReceiptFactDigest == "" {
		result, err = s.appendEffectReceipt(request, state, inspection.Receipt)
		if err != nil {
			return EffectAppendResult{}, err
		}
		state = result.State
	} else if state.Receipt.Disposition == authority.DispositionConflict {
		return EffectAppendResult{}, ErrEffectAuthorityConflict
	}
	return s.appendEffectDecision(request, state, inspection, true)
}

func (s *ingressDurableStore) closeConflictInspection(request EffectUseRequest, state EffectAuthorityState, inspection EffectInspection) (EffectAppendResult, error) {
	if state.ReceiptFactDigest == "" {
		result, err := s.appendEffectReceipt(request, state, inspection.Receipt)
		if err != nil {
			return EffectAppendResult{}, err
		}
		state = result.State
	}
	return s.appendEffectDecision(request, state, inspection, false)
}

func (s *ingressDurableStore) appendEffectDecision(request EffectUseRequest, expected EffectAuthorityState, inspection EffectInspection, accept bool) (EffectAppendResult, error) {
	if expected.ReceiptFactDigest == "" {
		return EffectAppendResult{}, ErrEffectAuthorityOrder
	}
	if err := validateEffectInspection(expected, inspection); err != nil {
		return EffectAppendResult{}, err
	}
	inspectionDigest, err := canonicalDigest(inspection)
	if err != nil {
		return EffectAppendResult{}, err
	}
	record := authority.ReconcileRecord{
		AuthorityNamespaceId: expected.Binding.Identity.AuthorityNamespaceID,
		IntentDigest:         expected.IntentRecordDigest, ReceiptDigest: expected.ReceiptRecordDigest,
		Observation: authority.ObservationConflict, Decision: authority.DecisionBlock,
	}
	if accept {
		record.Observation, record.Decision = authority.ObservationApplied, authority.DecisionAccept
	}
	recordDigest, err := record.Digest()
	if err != nil {
		return EffectAppendResult{}, err
	}
	projection := newAuthorityProjection()
	var result EffectAppendResult
	err = s.transact(projection, func() error {
		state, attempt, err := pendingEffectForUse(projection, request)
		if err != nil {
			return err
		}
		if state.ReconcileFactDigest != "" {
			result = EffectAppendResult{State: state, FactDigest: state.ReconcileFactDigest}
			return nil
		}
		if state.ReceiptFactDigest == "" || state.IntentFactDigest != expected.IntentFactDigest || state.ReceiptFactDigest != expected.ReceiptFactDigest {
			return ErrAttemptAuthorityConflict
		}
		fact := &effectAuthorityFact{
			ProtocolRevision: effectAuthorityProtocolRevision, FactType: effectFactTypeReconcile,
			Sequence: s.nextSequence, AttemptKey: mustAttemptKey(request.Binding.Identity),
			AttemptRevision: attempt.Revision + 1, PreviousAttemptHead: attempt.HeadDigest,
			Binding: request.Binding, EffectID: request.EffectID,
			IntentRecordDigest: state.IntentRecordDigest, IntentFactDigest: state.IntentFactDigest,
			ReceiptRecordDigest: state.ReceiptRecordDigest, ReceiptFactDigest: state.ReceiptFactDigest,
			Reconcile: &record, ReconcileRecordDigest: recordDigest,
			Inspection: &inspection, InspectionDigest: inspectionDigest,
		}
		if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
			return err
		}
		s.nextSequence++
		if err := applyEffectAuthorityFactValue(*fact, projection); err != nil {
			return fmt.Errorf("resultingress: appended effect reconcile failed projection: %w", err)
		}
		result = EffectAppendResult{State: projection.effects[mustEffectKey(request.Binding.Identity.AuthorityNamespaceID, request.EffectID)], Appended: true, FactDigest: fact.Digest}
		return nil
	})
	return result, err
}

func pendingEffectForUse(projection *Ingress, request EffectUseRequest) (EffectAuthorityState, AttemptAuthorityState, error) {
	effectKey, err := effectKey(request.Binding.Identity.AuthorityNamespaceID, request.EffectID)
	if err != nil {
		return EffectAuthorityState{}, AttemptAuthorityState{}, err
	}
	state, exists := projection.effects[effectKey]
	if !exists {
		return EffectAuthorityState{}, AttemptAuthorityState{}, ErrEffectAuthorityUnknown
	}
	if state.Binding != request.Binding || state.Intent.EffectId != request.EffectID || state.IntentFactDigest != request.IntentFactDigest || state.Intent.Deadline != request.IntentDeadline {
		return EffectAuthorityState{}, AttemptAuthorityState{}, ErrEffectAuthorityConflict
	}
	attemptKey, err := request.Binding.Identity.Key()
	if err != nil {
		return EffectAuthorityState{}, AttemptAuthorityState{}, err
	}
	attempt, exists := projection.attempts[attemptKey]
	if !exists || attempt.Identity != request.Binding.Identity {
		return EffectAuthorityState{}, AttemptAuthorityState{}, ErrAttemptAuthorityUnknown
	}
	if state.ReconcileFactDigest == "" {
		if attempt.PendingEffectID != request.EffectID || attempt.PendingEffectIntentFactDigest != request.IntentFactDigest || attempt.PendingEffectRecordDigest != state.IntentRecordDigest || attempt.PendingEffectMarkerDigest != request.Binding.MarkerDigest || attempt.PendingEffectPhase != request.Binding.Phase {
			return EffectAuthorityState{}, AttemptAuthorityState{}, ErrEffectAuthorityConflict
		}
	} else if attempt.PendingEffectIntentFactDigest != "" {
		return EffectAuthorityState{}, AttemptAuthorityState{}, ErrEffectAuthorityConflict
	}
	return state, attempt, nil
}

func validateEffectReceipt(state EffectAuthorityState, receipt authority.SideEffectReceipt) error {
	if err := receipt.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
	}
	if receipt.AuthorityNamespaceId != state.Binding.Identity.AuthorityNamespaceID || receipt.IntentDigest != state.IntentRecordDigest {
		return ErrEffectAuthorityConflict
	}
	return nil
}

func validateEffectInspection(state EffectAuthorityState, inspection EffectInspection) error {
	if err := validateEffectReceipt(state, inspection.Receipt); err != nil {
		return err
	}
	want := authority.Disposition("")
	switch inspection.Outcome {
	case EffectInspectionNotApplied:
		want = authority.DispositionNotApplied
	case EffectInspectionApplied:
		want = authority.DispositionApplied
	case EffectInspectionAmbiguous, EffectInspectionUnknown:
		want = authority.DispositionAmbiguous
	case EffectInspectionConflict:
		want = authority.DispositionConflict
	default:
		return ErrEffectAuthorityConflict
	}
	if inspection.Receipt.Disposition != want {
		return ErrEffectAuthorityConflict
	}
	return nil
}

func validateEffectReconcile(state EffectAuthorityState, record authority.ReconcileRecord, inspection EffectInspection, inspectionDigest string) error {
	if err := record.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
	}
	if record.AuthorityNamespaceId != state.Binding.Identity.AuthorityNamespaceID || record.IntentDigest != state.IntentRecordDigest || record.ReceiptDigest != state.ReceiptRecordDigest {
		return ErrEffectAuthorityConflict
	}
	if err := validateEffectInspection(state, inspection); err != nil {
		return err
	}
	digest, err := canonicalDigest(inspection)
	if err != nil || digest != inspectionDigest {
		return ErrEffectAuthorityConflict
	}
	switch inspection.Outcome {
	case EffectInspectionApplied:
		if record.Observation != authority.ObservationApplied || record.Decision != authority.DecisionAccept {
			return ErrEffectAuthorityConflict
		}
	case EffectInspectionConflict:
		if record.Observation != authority.ObservationConflict || record.Decision != authority.DecisionBlock {
			return ErrEffectAuthorityConflict
		}
	default:
		return ErrEffectAuthorityConflict
	}
	return nil
}

// EffectState returns a value copy of one effect authority projection.
func (s *ingressDurableStore) EffectState(namespace authority.AuthorityNamespaceId, effectID string) (EffectAuthorityState, bool, error) {
	key, err := effectKey(namespace, effectID)
	if err != nil {
		return EffectAuthorityState{}, false, err
	}
	projection := newAuthorityProjection()
	var state EffectAuthorityState
	var found bool
	err = s.transact(projection, func() error {
		state, found = projection.effects[key]
		return nil
	})
	return state, found, err
}

// PendingEffects returns all intent facts without a reconcile decision,
// ordered by Attempt key then effect ID. It is the restart recovery source.
func (s *ingressDurableStore) PendingEffects() ([]EffectAuthorityState, error) {
	projection := newAuthorityProjection()
	var states []EffectAuthorityState
	err := s.transact(projection, func() error {
		keys := make([]string, 0, len(projection.effects))
		for key, state := range projection.effects {
			if state.ReconcileFactDigest == "" {
				keys = append(keys, key)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			left, right := projection.effects[keys[i]], projection.effects[keys[j]]
			leftAttempt, _ := left.Binding.Identity.Key()
			rightAttempt, _ := right.Binding.Identity.Key()
			if leftAttempt != rightAttempt {
				return leftAttempt < rightAttempt
			}
			return left.Intent.EffectId < right.Intent.EffectId
		})
		states = make([]EffectAuthorityState, 0, len(keys))
		for _, key := range keys {
			states = append(states, projection.effects[key])
		}
		return nil
	})
	return states, err
}

func applyEffectAuthorityLine(line []byte, in *Ingress, wantSequence int64) error {
	canonicalLine, err := canonical.JSON(line)
	if err != nil || !bytes.Equal(canonicalLine, line) {
		return fmt.Errorf("%w: fact is not canonical", ErrEffectAuthorityConflict)
	}
	var fact effectAuthorityFact
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON value", ErrEffectAuthorityConflict)
	}
	if fact.ProtocolRevision != effectAuthorityProtocolRevision || fact.Sequence != wantSequence {
		return ErrEffectAuthorityConflict
	}
	stored := fact.Digest
	fact.Digest = ""
	digest, err := canonicalDigest(fact)
	if err != nil || stored == "" || digest != stored {
		return ErrEffectAuthorityConflict
	}
	fact.Digest = stored
	return applyEffectAuthorityFactValue(fact, in)
}

func applyEffectAuthorityFactValue(fact effectAuthorityFact, in *Ingress) error {
	if err := fact.Binding.Validate(); err != nil {
		return err
	}
	attemptKey, err := fact.Binding.Identity.Key()
	if err != nil || attemptKey != fact.AttemptKey {
		return ErrEffectAuthorityConflict
	}
	prior, exists := in.attempts[attemptKey]
	if !exists || prior.Identity != fact.Binding.Identity || fact.AttemptRevision != prior.Revision+1 || fact.PreviousAttemptHead != prior.HeadDigest {
		return ErrEffectAuthorityOrder
	}
	if strings.TrimSpace(fact.EffectID) == "" {
		return ErrEffectAuthorityConflict
	}
	effectKey := mustEffectKey(fact.Binding.Identity.AuthorityNamespaceID, fact.EffectID)
	switch fact.FactType {
	case effectFactTypeIntent:
		if fact.Intent == nil || fact.Intent.EffectId != fact.EffectID || fact.Receipt != nil || fact.Reconcile != nil || fact.Inspection != nil || fact.InspectionDigest != "" || fact.IntentFactDigest != "" || fact.ReceiptRecordDigest != "" || fact.ReceiptFactDigest != "" || fact.ReconcileRecordDigest != "" {
			return ErrEffectAuthorityConflict
		}
		request := EffectIntentRequest{Binding: fact.Binding, Intent: *fact.Intent}
		if err := validateEffectIntentRequest(request); err != nil {
			return err
		}
		if _, exists := in.effects[effectKey]; exists {
			return ErrEffectAuthorityConflict
		}
		if err := checkEffectIndexes(in, effectKey, request); err != nil {
			return err
		}
		if err := validateIntentPhase(prior, fact.Binding); err != nil {
			return err
		}
		digest, err := fact.Intent.Digest()
		if err != nil || digest != fact.IntentRecordDigest {
			return ErrEffectAuthorityConflict
		}
		state := EffectAuthorityState{Binding: fact.Binding, Intent: *fact.Intent, IntentRecordDigest: digest, IntentFactDigest: fact.Digest}
		in.effects[effectKey] = state
		indexEffect(in, effectKey, state)
		prior.PendingEffectID = fact.Intent.EffectId
		prior.PendingEffectIntentFactDigest = fact.Digest
		prior.PendingEffectRecordDigest = digest
		prior.PendingEffectMarkerDigest = fact.Binding.MarkerDigest
		prior.PendingEffectPhase = fact.Binding.Phase
	case effectFactTypeReceipt:
		if fact.Intent != nil || fact.Receipt == nil || fact.Reconcile != nil || fact.Inspection != nil || fact.InspectionDigest != "" || fact.ReceiptFactDigest != "" || fact.ReconcileRecordDigest != "" {
			return ErrEffectAuthorityConflict
		}
		state, exists := in.effects[effectKey]
		if !exists || state.Intent.EffectId != fact.EffectID || state.Binding != fact.Binding || state.IntentRecordDigest != fact.IntentRecordDigest || state.IntentFactDigest != fact.IntentFactDigest || state.ReceiptFactDigest != "" || state.ReconcileFactDigest != "" {
			return ErrEffectAuthorityOrder
		}
		if prior.PendingEffectID != state.Intent.EffectId || prior.PendingEffectIntentFactDigest != state.IntentFactDigest {
			return ErrEffectAuthorityConflict
		}
		if err := validateEffectReceipt(state, *fact.Receipt); err != nil {
			return err
		}
		digest, err := fact.Receipt.Digest()
		if err != nil || digest != fact.ReceiptRecordDigest {
			return ErrEffectAuthorityConflict
		}
		state.Receipt, state.ReceiptRecordDigest, state.ReceiptFactDigest = *fact.Receipt, digest, fact.Digest
		in.effects[effectKey] = state
	case effectFactTypeReconcile:
		if fact.Intent != nil || fact.Receipt != nil || fact.Reconcile == nil || fact.Inspection == nil || fact.InspectionDigest == "" {
			return ErrEffectAuthorityConflict
		}
		state, exists := in.effects[effectKey]
		if !exists || state.Intent.EffectId != fact.EffectID || state.Binding != fact.Binding || state.IntentRecordDigest != fact.IntentRecordDigest || state.IntentFactDigest != fact.IntentFactDigest || state.ReceiptRecordDigest != fact.ReceiptRecordDigest || state.ReceiptFactDigest != fact.ReceiptFactDigest || state.ReceiptFactDigest == "" || state.ReconcileFactDigest != "" {
			return ErrEffectAuthorityOrder
		}
		if prior.PendingEffectID != state.Intent.EffectId || prior.PendingEffectIntentFactDigest != state.IntentFactDigest {
			return ErrEffectAuthorityConflict
		}
		if err := validateEffectReconcile(state, *fact.Reconcile, *fact.Inspection, fact.InspectionDigest); err != nil {
			return err
		}
		digest, err := fact.Reconcile.Digest()
		if err != nil || digest != fact.ReconcileRecordDigest {
			return ErrEffectAuthorityConflict
		}
		state.Reconcile, state.ReconcileRecordDigest, state.ReconcileFactDigest = *fact.Reconcile, digest, fact.Digest
		state.ReconcileInspection, state.InspectionDigest = *fact.Inspection, fact.InspectionDigest
		in.effects[effectKey] = state
		prior.PendingEffectID = ""
		prior.PendingEffectIntentFactDigest = ""
		prior.PendingEffectRecordDigest = ""
		prior.PendingEffectMarkerDigest = ""
		prior.PendingEffectPhase = ""
		accepted := state.ReconcileInspection.Outcome == EffectInspectionApplied && state.Reconcile.Observation == authority.ObservationApplied && state.Reconcile.Decision == authority.DecisionAccept
		if accepted {
			switch state.Binding.Phase {
			case EffectPhaseAllocationProvision:
				prior.AllocationProvisionEffectDigest = fact.Digest
				prior.AllocationProvisionReceiptDigest = state.ReceiptRecordDigest
			case EffectPhaseAllocationTerminate:
				prior.AllocationTerminateEffectDigest = fact.Digest
				prior.AllocationTerminateReceiptDigest = state.ReceiptRecordDigest
			}
		} else {
			// A non-accepted reconcile closes concurrent mutation but cannot make
			// the next lifecycle transition legal. Resolution requires a future,
			// explicit intervention contract rather than a silent retry.
			prior.EffectInterventionDigest = fact.Digest
		}
	default:
		return ErrEffectAuthorityConflict
	}
	prior.Revision = fact.AttemptRevision
	prior.HeadDigest = fact.Digest
	in.attempts[attemptKey] = prior
	return nil
}

func indexEffect(in *Ingress, effectKey string, state EffectAuthorityState) {
	namespaceDigest, _ := state.Binding.Identity.AuthorityNamespaceID.Digest()
	attemptKey, _ := state.Binding.Identity.Key()
	in.effectCommands[namespaceDigest+"\x00command\x00"+state.Intent.CommandId] = effectKey
	in.effectIdempotency[namespaceDigest+"\x00idempotency\x00"+state.Intent.IdempotencyKey] = effectKey
	in.effectMarkers[namespaceDigest+"\x00marker\x00"+state.Binding.MarkerDigest] = attemptKey
}

func effectKey(namespace authority.AuthorityNamespaceId, effectID string) (string, error) {
	if err := namespace.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(effectID) == "" {
		return "", ErrEffectAuthorityConflict
	}
	namespaceDigest, err := namespace.Digest()
	if err != nil {
		return "", err
	}
	return namespaceDigest + "\x00effect\x00" + effectID, nil
}

func mustEffectKey(namespace authority.AuthorityNamespaceId, effectID string) string {
	key, _ := effectKey(namespace, effectID)
	return key
}

func mustAttemptKey(identity AttemptIdentity) string {
	key, _ := identity.Key()
	return key
}
