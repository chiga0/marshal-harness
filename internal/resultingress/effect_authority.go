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
	Digest                string                       `json:"digest"`
}

// CompareAndAppendEffectIntent holds current Run authority across the complete
// replay/read/CAS transaction. The append and fsync establish the pending
// effect barrier before any caller is allowed to invoke a Provider.
func (s *ingressDurableStore) CompareAndAppendEffectIntent(ctx context.Context, verifier CurrentRunAuthorityVerifier, request EffectIntentRequest) (EffectAppendResult, error) {
	if err := validateEffectIntentRequest(request); err != nil {
		return EffectAppendResult{}, err
	}
	var result EffectAppendResult
	err := withCurrentRunAuthority(ctx, verifier, request.Binding.CurrentRunAuthority, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			var err error
			result, err = s.appendEffectIntentLocked(projection, request)
			return err
		})
	})
	if errors.Is(err, ErrRunAuthorityUnauthorized) {
		return EffectAppendResult{}, fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
	}
	return result, err
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

// ExecutePendingEffect rechecks the durable pending intent under held current
// Run authority, releases the ResultIngress flock, invokes effect exactly once,
// then appends/fsyncs the receipt before releasing Run authority. No Provider
// callback executes while the authority store lock is held.
func (s *ingressDurableStore) ExecutePendingEffect(ctx context.Context, verifier CurrentRunAuthorityVerifier, request EffectUseRequest, effect func(EffectAuthorityState) (authority.SideEffectReceipt, error)) (EffectAppendResult, error) {
	if effect == nil {
		return EffectAppendResult{}, ErrEffectAuthorityConflict
	}
	if err := request.validate(); err != nil {
		return EffectAppendResult{}, err
	}
	var result EffectAppendResult
	err := withCurrentRunAuthority(ctx, verifier, request.Binding.CurrentRunAuthority, func() error {
		projection := newAuthorityProjection()
		var state EffectAuthorityState
		var attempt AttemptAuthorityState
		if err := s.transact(projection, func() error {
			var err error
			state, attempt, err = pendingEffectForUse(projection, request)
			if err == nil && state.ReceiptFactDigest != "" {
				result = EffectAppendResult{State: state, FactDigest: state.ReceiptFactDigest}
			}
			return err
		}); err != nil {
			return err
		}
		if state.ReceiptFactDigest != "" {
			return nil
		}

		receipt, err := effect(state)
		if err != nil {
			return err
		}
		if err := validateEffectReceipt(state, receipt); err != nil {
			return err
		}
		receiptDigest, err := receipt.Digest()
		if err != nil {
			return err
		}
		projection = newAuthorityProjection()
		return s.transact(projection, func() error {
			currentState, currentAttempt, err := pendingEffectForUse(projection, request)
			if err != nil {
				return err
			}
			if currentState.ReceiptFactDigest != "" {
				if currentState.Receipt == receipt {
					result = EffectAppendResult{State: currentState, FactDigest: currentState.ReceiptFactDigest}
					return nil
				}
				return ErrEffectAuthorityConflict
			}
			if currentAttempt.Revision != attempt.Revision || currentAttempt.HeadDigest != attempt.HeadDigest {
				return ErrAttemptAuthorityConflict
			}
			fact := &effectAuthorityFact{
				ProtocolRevision: effectAuthorityProtocolRevision,
				FactType:         effectFactTypeReceipt, Sequence: s.nextSequence,
				AttemptKey:      mustAttemptKey(request.Binding.Identity),
				AttemptRevision: currentAttempt.Revision + 1, PreviousAttemptHead: currentAttempt.HeadDigest,
				Binding: request.Binding, EffectID: request.EffectID, IntentRecordDigest: state.IntentRecordDigest,
				IntentFactDigest: state.IntentFactDigest, Receipt: &receipt,
				ReceiptRecordDigest: receiptDigest,
			}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
				return err
			}
			s.nextSequence++
			if err := applyEffectAuthorityFactValue(*fact, projection); err != nil {
				return fmt.Errorf("resultingress: appended effect receipt failed projection: %w", err)
			}
			updated := projection.effects[mustEffectKey(request.Binding.Identity.AuthorityNamespaceID, request.EffectID)]
			result = EffectAppendResult{State: updated, Appended: true, FactDigest: fact.Digest}
			return nil
		})
	})
	if errors.Is(err, ErrRunAuthorityUnauthorized) {
		return EffectAppendResult{}, fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
	}
	return result, err
}

// ReconcilePendingEffect invokes the Core reconcile callback outside the
// ResultIngress lock and appends its closed decision under the same held Run
// authority. Exact response-loss replay never re-invokes the callback.
func (s *ingressDurableStore) ReconcilePendingEffect(ctx context.Context, verifier CurrentRunAuthorityVerifier, request EffectUseRequest, reconcile func(EffectAuthorityState) (authority.ReconcileRecord, error)) (EffectAppendResult, error) {
	if reconcile == nil {
		return EffectAppendResult{}, ErrEffectAuthorityConflict
	}
	if err := request.validate(); err != nil {
		return EffectAppendResult{}, err
	}
	var result EffectAppendResult
	err := withCurrentRunAuthority(ctx, verifier, request.Binding.CurrentRunAuthority, func() error {
		projection := newAuthorityProjection()
		var state EffectAuthorityState
		var attempt AttemptAuthorityState
		if err := s.transact(projection, func() error {
			var err error
			state, attempt, err = pendingEffectForUse(projection, request)
			if err == nil && state.ReconcileFactDigest != "" {
				result = EffectAppendResult{State: state, FactDigest: state.ReconcileFactDigest}
			}
			return err
		}); err != nil {
			return err
		}
		if state.ReconcileFactDigest != "" {
			return nil
		}
		if state.ReceiptFactDigest == "" {
			return ErrEffectAuthorityOrder
		}
		record, err := reconcile(state)
		if err != nil {
			return err
		}
		if err := validateEffectReconcile(state, record); err != nil {
			return err
		}
		recordDigest, err := record.Digest()
		if err != nil {
			return err
		}
		projection = newAuthorityProjection()
		return s.transact(projection, func() error {
			currentState, currentAttempt, err := pendingEffectForUse(projection, request)
			if err != nil {
				return err
			}
			if currentState.ReconcileFactDigest != "" {
				if currentState.Reconcile == record {
					result = EffectAppendResult{State: currentState, FactDigest: currentState.ReconcileFactDigest}
					return nil
				}
				return ErrEffectAuthorityConflict
			}
			if currentState.ReceiptFactDigest == "" || currentAttempt.Revision != attempt.Revision || currentAttempt.HeadDigest != attempt.HeadDigest {
				return ErrAttemptAuthorityConflict
			}
			fact := &effectAuthorityFact{
				ProtocolRevision: effectAuthorityProtocolRevision,
				FactType:         effectFactTypeReconcile, Sequence: s.nextSequence,
				AttemptKey:      mustAttemptKey(request.Binding.Identity),
				AttemptRevision: currentAttempt.Revision + 1, PreviousAttemptHead: currentAttempt.HeadDigest,
				Binding: request.Binding, EffectID: request.EffectID, IntentRecordDigest: state.IntentRecordDigest,
				IntentFactDigest:    state.IntentFactDigest,
				ReceiptRecordDigest: state.ReceiptRecordDigest, ReceiptFactDigest: state.ReceiptFactDigest,
				Reconcile: &record, ReconcileRecordDigest: recordDigest,
			}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
				return err
			}
			s.nextSequence++
			if err := applyEffectAuthorityFactValue(*fact, projection); err != nil {
				return fmt.Errorf("resultingress: appended effect reconcile failed projection: %w", err)
			}
			updated := projection.effects[mustEffectKey(request.Binding.Identity.AuthorityNamespaceID, request.EffectID)]
			result = EffectAppendResult{State: updated, Appended: true, FactDigest: fact.Digest}
			return nil
		})
	})
	if errors.Is(err, ErrRunAuthorityUnauthorized) {
		return EffectAppendResult{}, fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
	}
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
	if state.Binding != request.Binding || state.Intent.EffectId != request.EffectID || state.IntentFactDigest != request.IntentFactDigest {
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

func validateEffectReconcile(state EffectAuthorityState, record authority.ReconcileRecord) error {
	if err := record.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectAuthorityConflict, err)
	}
	if record.AuthorityNamespaceId != state.Binding.Identity.AuthorityNamespaceID || record.IntentDigest != state.IntentRecordDigest || record.ReceiptDigest != state.ReceiptRecordDigest {
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
		if fact.Intent == nil || fact.Intent.EffectId != fact.EffectID || fact.Receipt != nil || fact.Reconcile != nil || fact.IntentFactDigest != "" || fact.ReceiptRecordDigest != "" || fact.ReceiptFactDigest != "" || fact.ReconcileRecordDigest != "" {
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
		if fact.Intent != nil || fact.Receipt == nil || fact.Reconcile != nil || fact.ReceiptFactDigest != "" || fact.ReconcileRecordDigest != "" {
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
		if fact.Intent != nil || fact.Receipt != nil || fact.Reconcile == nil {
			return ErrEffectAuthorityConflict
		}
		state, exists := in.effects[effectKey]
		if !exists || state.Intent.EffectId != fact.EffectID || state.Binding != fact.Binding || state.IntentRecordDigest != fact.IntentRecordDigest || state.IntentFactDigest != fact.IntentFactDigest || state.ReceiptRecordDigest != fact.ReceiptRecordDigest || state.ReceiptFactDigest != fact.ReceiptFactDigest || state.ReceiptFactDigest == "" || state.ReconcileFactDigest != "" {
			return ErrEffectAuthorityOrder
		}
		if prior.PendingEffectID != state.Intent.EffectId || prior.PendingEffectIntentFactDigest != state.IntentFactDigest {
			return ErrEffectAuthorityConflict
		}
		if err := validateEffectReconcile(state, *fact.Reconcile); err != nil {
			return err
		}
		digest, err := fact.Reconcile.Digest()
		if err != nil || digest != fact.ReconcileRecordDigest {
			return ErrEffectAuthorityConflict
		}
		state.Reconcile, state.ReconcileRecordDigest, state.ReconcileFactDigest = *fact.Reconcile, digest, fact.Digest
		in.effects[effectKey] = state
		prior.PendingEffectID = ""
		prior.PendingEffectIntentFactDigest = ""
		prior.PendingEffectRecordDigest = ""
		prior.PendingEffectMarkerDigest = ""
		prior.PendingEffectPhase = ""
		accepted := state.Receipt.Disposition == authority.DispositionApplied && state.Reconcile.Observation == authority.ObservationApplied && state.Reconcile.Decision == authority.DecisionAccept
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
