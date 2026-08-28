package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

const (
	allocationAuthorityProtocolRevision = "allocation-authority/v1"
	allocationFactTypePrepared          = "allocation-staging-prepared"
	allocationLocalIsolationDomain      = "host-process"
	allocationReconcileIdentity         = "marshal-core/allocation-control/v1"
	allocationEffectIdentitySchema      = "marshal/allocation-effect-identity/v1"
	allocationCommandDomain             = "marshal/allocation-command/v1\x00"
	allocationIdempotencyDomain         = "marshal/allocation-idempotency/v1\x00"
	allocationMarkerNonceDomain         = "marshal/allocation-marker-nonce/v1\x00"
)

var (
	ErrAllocationAuthorityConflict = errors.New("resultingress: allocation authority conflict")
	ErrAllocationAuthorityUnknown  = errors.New("resultingress: allocation authority unknown")
	ErrAllocationIntervention      = errors.New("resultingress: allocation effect requires intervention")
)

// AllocationAuthorityCheck is the full use-time authority tuple. Provision
// additionally requires Now and an active exact DispatchLease. Cleanup is
// deliberately lease-state independent and instead requires all four terminal
// cleanup fields to match the current Attempt.
type AllocationAuthorityCheck struct {
	Identity                  AttemptIdentity     `json:"identity"`
	CurrentRunAuthority       RunAuthorityBinding `json:"currentRunAuthority"`
	Phase                     EffectPhase         `json:"phase"`
	Now                       string              `json:"now,omitempty"`
	TerminalizationID         string              `json:"terminalizationId,omitempty"`
	TerminalGeneration        int64               `json:"terminalGeneration,omitempty"`
	CleanupBindingDigest      string              `json:"cleanupBindingDigest,omitempty"`
	ProcessTerminalFactDigest string              `json:"processTerminalFactDigest,omitempty"`
}

type ProvisionAuthorityVerifier interface {
	// The held callback receives the exact provider SecurityDomainId proven by
	// the same current Run/DispatchLease lookup; Stage2 persists that identity
	// verbatim in the generic receipt and never manufactures an actor.
	WithCurrentAllocationProvision(context.Context, AllocationAuthorityCheck, func(authority.SecurityDomainId) error) error
}

type CleanupAuthorityVerifier interface {
	// Cleanup remains valid after lease inactivity, but the durable terminal
	// binding must still resolve the exact provider SecurityDomainId.
	WithCurrentAllocationCleanup(context.Context, AllocationAuthorityCheck, func(authority.SecurityDomainId) error) error
}

// AllocationAuthority binds only long-lived current-authority ports. It never
// caches a successful check, bearer or lease result; every operation replays
// the durable effect and obtains a fresh held verifier callback.
type AllocationAuthority struct {
	store     *DurableStore
	provision ProvisionAuthorityVerifier
	cleanup   CleanupAuthorityVerifier
}

var _ allocationcontrol.Authority = (*AllocationAuthority)(nil)

func NewAllocationAuthority(store *DurableStore, provision ProvisionAuthorityVerifier, cleanup CleanupAuthorityVerifier) (*AllocationAuthority, error) {
	if store == nil || provision == nil || cleanup == nil {
		return nil, ErrAllocationAuthorityConflict
	}
	if err := store.requireBound(); err != nil {
		return nil, err
	}
	return &AllocationAuthority{store: store, provision: provision, cleanup: cleanup}, nil
}

type AllocationIntentAppendResult struct {
	Effect           EffectAuthorityState
	Snapshot         allocationcontrol.AuthoritySnapshot
	Appended         bool
	EffectKey        string
	EffectFactDigest string
	AllocationDigest string
}

// AllocationEffectIdentity is the only v1 derivation of allocation command,
// idempotency and marker-nonce identities. Callers may carry these values in
// typed and generic intents, but may not choose them. MarkerNonceDigest is used
// only by provision; terminate retains the provision marker identity.
type AllocationEffectIdentity struct {
	CommandID         string
	IdempotencyKey    string
	MarkerNonceDigest string
}

// DeriveAllocationEffectIdentity freezes the exact JCS tuple used by the
// production composition root. The domain-separated digests prevent a value
// from one role or phase from being replayed in another.
func DeriveAllocationEffectIdentity(identity AttemptIdentity, phase EffectPhase, effectID, attemptAuthorityHeadDigest string) (AllocationEffectIdentity, error) {
	if err := identity.Validate(); err != nil || phase.Validate() != nil || strings.TrimSpace(effectID) == "" || effectID != strings.TrimSpace(effectID) || requireDigest("attemptAuthorityHeadDigest", attemptAuthorityHeadDigest) != nil {
		return AllocationEffectIdentity{}, ErrAllocationAuthorityConflict
	}
	tuple := struct {
		SchemaVersion              string                         `json:"schemaVersion"`
		AuthorityNamespaceID       authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
		TaskID                     string                         `json:"taskId"`
		RunID                      string                         `json:"runId"`
		AttemptID                  string                         `json:"attemptId"`
		AllocationID               string                         `json:"allocationId"`
		LeaseID                    string                         `json:"leaseId"`
		Generation                 int64                          `json:"generation"`
		Phase                      EffectPhase                    `json:"phase"`
		EffectID                   string                         `json:"effectId"`
		AttemptAuthorityHeadDigest string                         `json:"attemptAuthorityHeadDigest"`
	}{
		SchemaVersion: allocationEffectIdentitySchema, AuthorityNamespaceID: identity.AuthorityNamespaceID,
		TaskID: identity.TaskID, RunID: identity.RunID, AttemptID: identity.AttemptID,
		AllocationID: identity.AllocationID, LeaseID: identity.LeaseID, Generation: identity.DispatchGeneration,
		Phase: phase, EffectID: effectID, AttemptAuthorityHeadDigest: attemptAuthorityHeadDigest,
	}
	raw, err := json.Marshal(tuple)
	if err != nil {
		return AllocationEffectIdentity{}, ErrAllocationAuthorityConflict
	}
	jcs, err := canonical.JSON(raw)
	if err != nil {
		return AllocationEffectIdentity{}, ErrAllocationAuthorityConflict
	}
	commandDigest := canonical.DigestBytes(append([]byte(allocationCommandDomain), jcs...))
	idempotencyDigest := canonical.DigestBytes(append([]byte(allocationIdempotencyDomain), jcs...))
	markerNonceDigest := canonical.DigestBytes(append([]byte(allocationMarkerNonceDomain), jcs...))
	return AllocationEffectIdentity{
		CommandID:         "allocation-command-" + strings.TrimPrefix(commandDigest, "sha256:"),
		IdempotencyKey:    "allocation-idempotency-" + strings.TrimPrefix(idempotencyDigest, "sha256:"),
		MarkerNonceDigest: markerNonceDigest,
	}, nil
}

type allocationAuthorityState struct {
	Snapshot          allocationcontrol.AuthoritySnapshot
	ProvisionEffectID string
	TerminateEffectID string
}

type allocationAuthorityFact struct {
	ProtocolRevision        string                       `json:"protocolRevision"`
	FactType                string                       `json:"factType"`
	Sequence                int64                        `json:"sequence"`
	AttemptKey              string                       `json:"attemptKey"`
	AttemptRevision         uint64                       `json:"attemptRevision"`
	PreviousAttemptHead     string                       `json:"previousAttemptHead"`
	EffectID                string                       `json:"effectId"`
	AllocationRecordKind    allocationcontrol.RecordKind `json:"allocationRecordKind"`
	AllocationAuthorityFact json.RawMessage              `json:"allocationAuthorityFact"`
	AllocationRecordedAt    string                       `json:"allocationRecordedAt"`
	Digest                  string                       `json:"digest"`
}

func (authorityPort *AllocationAuthority) CompareAndAppendAllocationProvisionIntent(ctx context.Context, identity AttemptIdentity, genericIntent authority.SideEffectIntent, typedIntent allocationcontrol.AllocationProvisionIntentV1) (AllocationIntentAppendResult, error) {
	if authorityPort == nil || authorityPort.store == nil {
		return AllocationIntentAppendResult{}, ErrAllocationAuthorityConflict
	}
	binding, err := allocationProvisionBinding(identity, typedIntent)
	if err != nil {
		return AllocationIntentAppendResult{}, err
	}
	request := EffectIntentRequest{Binding: binding, Intent: genericIntent}
	if err := validateAllocationProvisionIntentMapping(identity, request, typedIntent); err != nil {
		return AllocationIntentAppendResult{}, err
	}
	// Intent expiry is an admission property, not a replay property.  The held
	// verifier still receives the current authority time, while the ledger
	// transaction below decides whether this is an exact replay before it
	// considers the intent deadline.
	check := allocationCheck(binding, authorityPort.store.authorityNow())
	var result AllocationIntentAppendResult
	err = authorityPort.withProvisionAuthority(ctx, check, func(_ authority.SecurityDomainId) error {
		var appendErr error
		result, appendErr = authorityPort.store.appendAllocationIntent(request, allocationcontrol.RecordProvisionIntent, typedIntent)
		return appendErr
	})
	return result, err
}

func (authorityPort *AllocationAuthority) CompareAndAppendAllocationTerminateIntent(ctx context.Context, identity AttemptIdentity, genericIntent authority.SideEffectIntent, typedIntent allocationcontrol.AllocationTerminateIntentV1) (AllocationIntentAppendResult, error) {
	if authorityPort == nil || authorityPort.store == nil {
		return AllocationIntentAppendResult{}, ErrAllocationAuthorityConflict
	}
	binding, err := allocationTerminateBinding(identity, typedIntent)
	if err != nil {
		return AllocationIntentAppendResult{}, err
	}
	request := EffectIntentRequest{Binding: binding, Intent: genericIntent}
	if err := validateAllocationTerminateIntentMapping(identity, request, typedIntent); err != nil {
		return AllocationIntentAppendResult{}, err
	}
	check := allocationCheck(binding, time.Time{})
	var result AllocationIntentAppendResult
	err = authorityPort.withCleanupAuthority(ctx, check, func(_ authority.SecurityDomainId) error {
		var appendErr error
		result, appendErr = authorityPort.store.appendAllocationIntent(request, allocationcontrol.RecordTerminateIntent, typedIntent)
		return appendErr
	})
	return result, err
}

func allocationProvisionBinding(identity AttemptIdentity, intent allocationcontrol.AllocationProvisionIntentV1) (EffectBinding, error) {
	if err := identity.Validate(); err != nil || intent.Validate() != nil || intent.ExpectedAttemptSequence < 2 {
		return EffectBinding{}, ErrAllocationAuthorityConflict
	}
	binding := EffectBinding{
		Identity: identity, CurrentRunAuthority: runAuthorityBindingFor(identity),
		AdmissionAttemptRevision: intent.ExpectedAttemptSequence - 1,
		AdmissionAuthorityDigest: intent.AttemptAuthorityFactDigest,
		Phase:                    EffectPhaseAllocationProvision, MarkerDigest: intent.MarkerNonceDigest,
	}
	if err := binding.Validate(); err != nil {
		return EffectBinding{}, err
	}
	return binding, nil
}

func allocationTerminateBinding(identity AttemptIdentity, intent allocationcontrol.AllocationTerminateIntentV1) (EffectBinding, error) {
	if err := identity.Validate(); err != nil || intent.Validate() != nil || intent.ExpectedAttemptSequence < 2 {
		return EffectBinding{}, ErrAllocationAuthorityConflict
	}
	binding := EffectBinding{
		Identity: identity, CurrentRunAuthority: runAuthorityBindingFor(identity),
		AdmissionAttemptRevision: intent.ExpectedAttemptSequence - 1,
		AdmissionAuthorityDigest: intent.AttemptAuthorityFactDigest,
		Phase:                    EffectPhaseAllocationTerminate, MarkerDigest: intent.Marker.NonceDigest,
		TerminalizationID: intent.TerminalizationID, TerminalGeneration: intent.Binding.Generation + 1,
		CleanupBindingDigest: intent.CleanupBindingDigest, ProcessTerminalFactDigest: intent.ProcessTerminalFactDigest,
	}
	// TerminalGeneration is an Attempt authority value, not allocation
	// generation. The caller cannot supply a different value through B1, so it
	// is checked against the durable Attempt before admission and replaced there.
	binding.TerminalGeneration = identity.DispatchGeneration + 1
	if err := binding.Validate(); err != nil {
		return EffectBinding{}, err
	}
	return binding, nil
}

func allocationCheck(binding EffectBinding, now time.Time) AllocationAuthorityCheck {
	check := AllocationAuthorityCheck{
		Identity: binding.Identity, CurrentRunAuthority: binding.CurrentRunAuthority, Phase: binding.Phase,
		TerminalizationID: binding.TerminalizationID, TerminalGeneration: binding.TerminalGeneration,
		CleanupBindingDigest: binding.CleanupBindingDigest, ProcessTerminalFactDigest: binding.ProcessTerminalFactDigest,
	}
	if !now.IsZero() {
		check.Now = now.UTC().Format(time.RFC3339Nano)
	}
	return check
}

func (authorityPort *AllocationAuthority) withProvisionAuthority(ctx context.Context, check AllocationAuthorityCheck, fn func(authority.SecurityDomainId) error) error {
	if check.Phase != EffectPhaseAllocationProvision || check.Now == "" || check.TerminalizationID != "" || check.TerminalGeneration != 0 || check.CleanupBindingDigest != "" || check.ProcessTerminalFactDigest != "" {
		return ErrAllocationAuthorityConflict
	}
	if authorizedByCleanup(ctx, authorityPort.cleanup, check) {
		return fmt.Errorf("%w: cleanup verifier also authorized provision", ErrAllocationAuthorityConflict)
	}
	return invokeProvisionVerifier(ctx, authorityPort.provision, check, fn)
}

func (authorityPort *AllocationAuthority) withCleanupAuthority(ctx context.Context, check AllocationAuthorityCheck, fn func(authority.SecurityDomainId) error) error {
	if check.Phase != EffectPhaseAllocationTerminate || check.Now != "" || strings.TrimSpace(check.TerminalizationID) == "" || check.TerminalGeneration < 1 || requireDigest("cleanupBindingDigest", check.CleanupBindingDigest) != nil || requireDigest("processTerminalFactDigest", check.ProcessTerminalFactDigest) != nil {
		return ErrAllocationAuthorityConflict
	}
	if authorizedByProvision(ctx, authorityPort.provision, check) {
		return fmt.Errorf("%w: provision verifier also authorized cleanup", ErrAllocationAuthorityConflict)
	}
	return invokeCleanupVerifier(ctx, authorityPort.cleanup, check, fn)
}

func authorizedByCleanup(ctx context.Context, verifier CleanupAuthorityVerifier, check AllocationAuthorityCheck) bool {
	called := false
	err := verifier.WithCurrentAllocationCleanup(ctx, check, func(authority.SecurityDomainId) error { called = true; return nil })
	return called || err == nil
}

func authorizedByProvision(ctx context.Context, verifier ProvisionAuthorityVerifier, check AllocationAuthorityCheck) bool {
	called := false
	err := verifier.WithCurrentAllocationProvision(ctx, check, func(authority.SecurityDomainId) error { called = true; return nil })
	return called || err == nil
}

func invokeProvisionVerifier(ctx context.Context, verifier ProvisionAuthorityVerifier, check AllocationAuthorityCheck, fn func(authority.SecurityDomainId) error) error {
	return invokeHeld(check.Identity, func(callback func(authority.SecurityDomainId) error) error {
		return verifier.WithCurrentAllocationProvision(ctx, check, callback)
	}, fn)
}

func invokeCleanupVerifier(ctx context.Context, verifier CleanupAuthorityVerifier, check AllocationAuthorityCheck, fn func(authority.SecurityDomainId) error) error {
	return invokeHeld(check.Identity, func(callback func(authority.SecurityDomainId) error) error {
		return verifier.WithCurrentAllocationCleanup(ctx, check, callback)
	}, fn)
}

func invokeHeld(identity AttemptIdentity, invoke func(func(authority.SecurityDomainId) error) error, fn func(authority.SecurityDomainId) error) error {
	if fn == nil {
		return ErrAllocationAuthorityConflict
	}
	var gate sync.Mutex
	called, closed, double := false, false, false
	var callbackErr error
	verifierErr := invoke(func(providerDomain authority.SecurityDomainId) error {
		gate.Lock()
		defer gate.Unlock()
		if closed || called {
			double = true
			return ErrAllocationAuthorityConflict
		}
		called = true
		if err := validateAllocationProviderDomain(identity, providerDomain); err != nil {
			callbackErr = err
			return callbackErr
		}
		callbackErr = fn(providerDomain)
		return callbackErr
	})
	gate.Lock()
	closed = true
	calledOnce, invokedTwice, heldErr := called, double, callbackErr
	gate.Unlock()
	if invokedTwice || !calledOnce {
		return ErrAllocationAuthorityConflict
	}
	if heldErr != nil {
		return heldErr
	}
	if verifierErr != nil {
		return fmt.Errorf("%w: verifier rejected authority: %v", ErrAllocationAuthorityConflict, verifierErr)
	}
	return nil
}

func validateAllocationProviderDomain(identity AttemptIdentity, providerDomain authority.SecurityDomainId) error {
	if providerDomain.TenantNamespace != identity.AuthorityNamespaceID.TenantNamespace || providerDomain.TrustDomainKind != authority.TrustDomainKindExecution || providerDomain.IsolationDomainId != allocationLocalIsolationDomain || providerDomain.Validate() != nil {
		return ErrAllocationAuthorityConflict
	}
	return nil
}

func validateAllocationProvisionIntentMapping(identity AttemptIdentity, request EffectIntentRequest, intent allocationcontrol.AllocationProvisionIntentV1) error {
	if err := validateEffectIntentRequest(request); err != nil || intent.Validate() != nil || request.Binding.Identity != identity || request.Binding.Phase != EffectPhaseAllocationProvision {
		return ErrAllocationAuthorityConflict
	}
	if !allocationBindingMatchesIdentity(intent.Binding, identity) || intent.ExpectedAttemptSequence != request.Binding.AdmissionAttemptRevision+1 || intent.AttemptAuthorityFactDigest != request.Binding.AdmissionAuthorityDigest {
		return ErrAllocationAuthorityConflict
	}
	derived, err := DeriveAllocationEffectIdentity(identity, EffectPhaseAllocationProvision, request.Intent.EffectId, request.Binding.AdmissionAuthorityDigest)
	if err != nil || intent.Binding.CommandID != derived.CommandID || intent.Binding.IdempotencyKey != derived.IdempotencyKey || intent.MarkerNonceDigest != derived.MarkerNonceDigest {
		return ErrAllocationAuthorityConflict
	}
	if request.Intent.CommandId != intent.Binding.CommandID || request.Intent.IdempotencyKey != intent.Binding.IdempotencyKey || request.Intent.RequestDigest != intent.RequestDigest || request.Intent.TargetDigest != request.Binding.MarkerDigest {
		return ErrAllocationAuthorityConflict
	}
	return nil
}

func validateAllocationTerminateIntentMapping(identity AttemptIdentity, request EffectIntentRequest, intent allocationcontrol.AllocationTerminateIntentV1) error {
	if err := validateEffectIntentRequest(request); err != nil || intent.Validate() != nil || request.Binding.Identity != identity || request.Binding.Phase != EffectPhaseAllocationTerminate {
		return ErrAllocationAuthorityConflict
	}
	if !allocationBindingMatchesIdentity(intent.Binding, identity) || intent.ExpectedAttemptSequence != request.Binding.AdmissionAttemptRevision+1 || intent.AttemptAuthorityFactDigest != request.Binding.AdmissionAuthorityDigest || intent.OrchestratorID != identity.OrchestratorID {
		return ErrAllocationAuthorityConflict
	}
	derived, err := DeriveAllocationEffectIdentity(identity, EffectPhaseAllocationTerminate, request.Intent.EffectId, request.Binding.AdmissionAuthorityDigest)
	if err != nil || intent.Binding.CommandID != derived.CommandID || intent.Binding.IdempotencyKey != derived.IdempotencyKey {
		return ErrAllocationAuthorityConflict
	}
	if request.Intent.CommandId != intent.Binding.CommandID || request.Intent.IdempotencyKey != intent.Binding.IdempotencyKey || request.Intent.RequestDigest != intent.RequestDigest || request.Intent.TargetDigest != request.Binding.MarkerDigest {
		return ErrAllocationAuthorityConflict
	}
	return nil
}

func allocationBindingMatchesIdentity(binding allocationcontrol.AllocationBindingV1, identity AttemptIdentity) bool {
	namespaceDigest, err := identity.AuthorityNamespaceID.Digest()
	return err == nil && binding.AuthorityNamespaceID == namespaceDigest && binding.TaskID == identity.TaskID && binding.RunID == identity.RunID && binding.AttemptID == identity.AttemptID && binding.AllocationID == identity.AllocationID && binding.LeaseID == identity.LeaseID && binding.Generation == identity.DispatchGeneration && binding.FencingTokenDigest == identity.FencingTokenDigest
}

func (s *ingressDurableStore) appendAllocationIntent(request EffectIntentRequest, kind allocationcontrol.RecordKind, typed any) (AllocationIntentAppendResult, error) {
	payload, err := allocationcontrol.EncodeFactPayload(typed)
	if err != nil {
		return AllocationIntentAppendResult{}, ErrAllocationAuthorityConflict
	}
	projection := newAuthorityProjection()
	var result AllocationIntentAppendResult
	err = s.transact(projection, func() error {
		effectKey, err := effectKey(request.Binding.Identity.AuthorityNamespaceID, request.Intent.EffectId)
		if err != nil {
			return err
		}
		attemptKey := mustAttemptKey(request.Binding.Identity)
		if existing, exists := projection.effects[effectKey]; exists {
			allocationState, ok := projection.allocations[attemptKey]
			if !ok || existing.Binding != request.Binding || existing.Intent != request.Intent {
				return ErrAllocationAuthorityConflict
			}
			factDigest := allocationFactDigest(allocationState.Snapshot, kind)
			if factDigest == "" || !allocationSnapshotHasPayload(allocationState.Snapshot, kind, payload) {
				return ErrAllocationAuthorityConflict
			}
			result = AllocationIntentAppendResult{Effect: existing, Snapshot: cloneAllocationSnapshot(allocationState.Snapshot), EffectKey: effectKey, EffectFactDigest: existing.IntentFactDigest, AllocationDigest: factDigest}
			return nil
		}
		if err := checkEffectIndexes(projection, effectKey, request); err != nil {
			return err
		}
		attempt, exists := projection.attempts[attemptKey]
		if !exists || attempt.Identity != request.Binding.Identity || attempt.Revision != request.Binding.AdmissionAttemptRevision || attempt.HeadDigest != request.Binding.AdmissionAuthorityDigest {
			return ErrAttemptAuthorityConflict
		}
		if err := validateIntentPhase(attempt, request.Binding); err != nil {
			return err
		}
		if kind == allocationcontrol.RecordTerminateIntent {
			if attempt.TerminalizationID != request.Binding.TerminalizationID || attempt.TerminalGeneration != request.Binding.TerminalGeneration || attempt.CleanupBindingDigest != request.Binding.CleanupBindingDigest || attempt.ProcessTerminalDigest != request.Binding.ProcessTerminalFactDigest {
				return ErrAllocationAuthorityConflict
			}
		}
		// This is the first point at which the transaction knows that no exact
		// canonical namespace+effect fact exists.  Check the deadline while both
		// the current-authority callback and the durable ledger lock are held, and
		// only for the fresh append path.  Exact replay above consumes no deadline
		// and emits no new ledger line.
		if _, err := s.currentEffectTime(request.Intent.Deadline); err != nil {
			return err
		}
		intentDigest, err := request.Intent.Digest()
		if err != nil {
			return err
		}
		fact := &effectAuthorityFact{
			ProtocolRevision: effectAuthorityProtocolRevision, FactType: effectFactTypeIntent,
			Sequence: s.nextSequence, AttemptKey: attemptKey, AttemptRevision: attempt.Revision + 1,
			PreviousAttemptHead: attempt.HeadDigest, Binding: request.Binding, EffectID: request.Intent.EffectId,
			Intent: &request.Intent, IntentRecordDigest: intentDigest,
			AllocationRecordKind: kind, AllocationAuthorityFact: payload,
			AllocationRecordedAt: s.authorityNow().Format(time.RFC3339Nano),
		}
		if err := validateAllocationEffectPayload(*fact, projection); err != nil {
			return err
		}
		if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
			return err
		}
		s.nextSequence++
		if err := applyEffectAuthorityFactValue(*fact, projection); err != nil {
			return fmt.Errorf("resultingress: appended allocation intent failed projection: %w", err)
		}
		allocationState := projection.allocations[attemptKey]
		state := projection.effects[effectKey]
		result = AllocationIntentAppendResult{Effect: state, Snapshot: cloneAllocationSnapshot(allocationState.Snapshot), Appended: true, EffectKey: effectKey, EffectFactDigest: fact.Digest, AllocationDigest: fact.Digest}
		return nil
	})
	return result, err
}

func (authorityPort *AllocationAuthority) WithCurrentAllocation(ctx context.Context, canonicalEffectKey string, operation func(allocationcontrol.AuthoritySession) error) error {
	if authorityPort == nil || authorityPort.store == nil || strings.TrimSpace(canonicalEffectKey) == "" || operation == nil {
		return ErrAllocationAuthorityConflict
	}
	return authorityPort.store.withEffectFlight(canonicalEffectKey, func() error {
		// The effect is intentionally reloaded only after flight ownership.  No
		// pre-flight state or deadline decision may authorize this operation.
		state, attempt, allocationState, err := authorityPort.store.loadAllocationEffect(canonicalEffectKey)
		if err != nil {
			return err
		}
		if state.ReconcileFactDigest != "" && state.Reconcile.Decision != authority.DecisionAccept {
			return ErrAllocationIntervention
		}
		if err := validateAllocationEffectOwnership(state, attempt, allocationState, state.Intent.EffectId); err != nil {
			return err
		}
		check := allocationCheck(state.Binding, time.Time{})
		if state.Binding.Phase == EffectPhaseAllocationProvision {
			check = allocationCheck(state.Binding, authorityPort.store.authorityNow())
		} else if state.Binding.Phase != EffectPhaseAllocationTerminate {
			return ErrAllocationAuthorityConflict
		}
		held := func(providerDomain authority.SecurityDomainId) error {
			session := &allocationAuthoritySession{authority: authorityPort, effectKey: canonicalEffectKey, effectID: state.Intent.EffectId, binding: state.Binding, providerDomain: providerDomain, active: true}
			defer session.close()
			return operation(session)
		}
		if state.Binding.Phase == EffectPhaseAllocationProvision {
			return authorityPort.withProvisionAuthority(ctx, check, held)
		}
		return authorityPort.withCleanupAuthority(ctx, check, held)
	})
}

type allocationAuthoritySession struct {
	authority      *AllocationAuthority
	effectKey      string
	effectID       string
	binding        EffectBinding
	providerDomain authority.SecurityDomainId
	gate           sync.Mutex
	active         bool
}

var _ allocationcontrol.AuthoritySession = (*allocationAuthoritySession)(nil)

func (session *allocationAuthoritySession) Snapshot() (allocationcontrol.AuthoritySnapshot, error) {
	if session == nil {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	session.gate.Lock()
	defer session.gate.Unlock()
	if !session.active || session.authority == nil {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	state, _, allocationState, err := session.authority.store.loadAllocationEffect(session.effectKey)
	if err != nil || state.Binding != session.binding {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	return cloneAllocationSnapshot(allocationState.Snapshot), nil
}

func (session *allocationAuthoritySession) AuthorizeFirstMutation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if session == nil {
		return ErrAllocationAuthorityConflict
	}
	session.gate.Lock()
	defer session.gate.Unlock()
	if !session.active || session.authority == nil {
		return ErrAllocationAuthorityConflict
	}
	state, _, allocationState, err := session.authority.store.loadAllocationEffect(session.effectKey)
	if err != nil || state.Binding != session.binding || state.ReceiptFactDigest != "" || state.ReconcileFactDigest != "" {
		return ErrAllocationAuthorityConflict
	}
	// A durable prepared fact proves the first provision Apply already started.
	// All later work is exact inspection/recovery and must not be blocked merely
	// because the original intent deadline elapsed meanwhile.
	if state.Binding.Phase == EffectPhaseAllocationProvision && allocationState.Snapshot.ProvisionPrepared != nil {
		return nil
	}
	_, err = session.authority.store.currentEffectTime(state.Intent.Deadline)
	return err
}

func (session *allocationAuthoritySession) AppendProvisionPrepared(ctx context.Context, prepared allocationcontrol.AllocationStagingPreparedV1) (allocationcontrol.AuthoritySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return allocationcontrol.AuthoritySnapshot{}, err
	}
	if session == nil {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	session.gate.Lock()
	defer session.gate.Unlock()
	if !session.active || session.binding.Phase != EffectPhaseAllocationProvision {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	return session.authority.store.appendAllocationPrepared(session.effectID, session.binding, prepared)
}

func (session *allocationAuthoritySession) AppendProvisionReceipt(ctx context.Context, receipt allocationcontrol.AllocationProvisionReceiptV1) (allocationcontrol.AuthoritySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return allocationcontrol.AuthoritySnapshot{}, err
	}
	if session == nil {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	session.gate.Lock()
	defer session.gate.Unlock()
	if !session.active || session.binding.Phase != EffectPhaseAllocationProvision {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	return session.authority.store.appendAllocationReceipt(session.effectID, session.binding, session.providerDomain, allocationcontrol.RecordProvisionReceipt, receipt)
}

func (session *allocationAuthoritySession) AppendTerminateReceipt(ctx context.Context, receipt allocationcontrol.AllocationTerminateReceiptV1) (allocationcontrol.AuthoritySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return allocationcontrol.AuthoritySnapshot{}, err
	}
	if session == nil {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	session.gate.Lock()
	defer session.gate.Unlock()
	if !session.active || session.binding.Phase != EffectPhaseAllocationTerminate {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	return session.authority.store.appendAllocationReceipt(session.effectID, session.binding, session.providerDomain, allocationcontrol.RecordTerminateReceipt, receipt)
}

func (session *allocationAuthoritySession) ProjectAndReconcile(ctx context.Context, project func(allocationcontrol.AuthoritySnapshot) error) (allocationcontrol.AuthoritySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return allocationcontrol.AuthoritySnapshot{}, err
	}
	if session == nil || project == nil {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	session.gate.Lock()
	defer session.gate.Unlock()
	if !session.active || session.authority == nil {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	state, _, allocationState, err := session.authority.store.loadAllocationEffect(session.effectKey)
	if err != nil || state.Binding != session.binding || state.ReceiptFactDigest == "" {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	snapshot := cloneAllocationSnapshot(allocationState.Snapshot)
	if snapshot.Validate() != nil {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	if projectErr := project(snapshot); projectErr != nil {
		kind := allocationFailureForProviderError(projectErr)
		if kind != "" {
			if err := session.authority.store.appendAllocationProjectionIntervention(session.effectKey, kind); err != nil {
				return allocationcontrol.AuthoritySnapshot{}, errors.Join(projectErr, err)
			}
		}
		return allocationcontrol.AuthoritySnapshot{}, projectErr
	}
	if err := session.authority.store.repairAllocationReconcile(session.effectKey); err != nil {
		return allocationcontrol.AuthoritySnapshot{}, err
	}
	_, _, reconciled, err := session.authority.store.loadAllocationEffect(session.effectKey)
	return cloneAllocationSnapshot(reconciled.Snapshot), err
}

func (session *allocationAuthoritySession) RecordIntervention(ctx context.Context, kind allocationcontrol.AuthorityFailureKind) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if session == nil || kind.Validate() != nil {
		return ErrAllocationAuthorityConflict
	}
	session.gate.Lock()
	defer session.gate.Unlock()
	if !session.active || session.authority == nil {
		return ErrAllocationAuthorityConflict
	}
	return session.authority.store.appendAllocationIntervention(session.effectKey, session.providerDomain, kind)
}

func (session *allocationAuthoritySession) close() {
	session.gate.Lock()
	defer session.gate.Unlock()
	session.active = false
}

func (s *ingressDurableStore) loadAllocationEffect(canonicalEffectKey string) (EffectAuthorityState, AttemptAuthorityState, allocationAuthorityState, error) {
	projection := newAuthorityProjection()
	var effect EffectAuthorityState
	var attempt AttemptAuthorityState
	var allocationState allocationAuthorityState
	err := s.transact(projection, func() error {
		candidate, ok := projection.effects[canonicalEffectKey]
		if !ok || mustEffectKey(candidate.Binding.Identity.AuthorityNamespaceID, candidate.Intent.EffectId) != canonicalEffectKey {
			return ErrAllocationAuthorityUnknown
		}
		effect = candidate
		attemptKey := mustAttemptKey(effect.Binding.Identity)
		attempt, ok = projection.attempts[attemptKey]
		if !ok {
			return ErrAttemptAuthorityUnknown
		}
		allocationState, ok = projection.allocations[attemptKey]
		if !ok {
			return ErrAllocationAuthorityUnknown
		}
		return nil
	})
	return effect, attempt, allocationState, err
}

func validateAllocationEffectOwnership(effect EffectAuthorityState, attempt AttemptAuthorityState, state allocationAuthorityState, effectID string) error {
	if effect.Binding.Identity != attempt.Identity || state.Snapshot.Validate() != nil {
		return ErrAllocationAuthorityConflict
	}
	switch effect.Binding.Phase {
	case EffectPhaseAllocationProvision:
		if state.ProvisionEffectID != effectID {
			return ErrAllocationAuthorityConflict
		}
	case EffectPhaseAllocationTerminate:
		if state.TerminateEffectID != effectID {
			return ErrAllocationAuthorityConflict
		}
	default:
		return ErrAllocationAuthorityConflict
	}
	return nil
}

func (s *ingressDurableStore) appendAllocationPrepared(effectID string, binding EffectBinding, prepared allocationcontrol.AllocationStagingPreparedV1) (allocationcontrol.AuthoritySnapshot, error) {
	payload, err := allocationcontrol.EncodeFactPayload(prepared)
	if err != nil {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	projection := newAuthorityProjection()
	var snapshot allocationcontrol.AuthoritySnapshot
	err = s.transact(projection, func() error {
		effect, attempt, state, err := allocationStateForMutation(projection, effectID, binding)
		if err != nil || effect.Binding.Phase != EffectPhaseAllocationProvision || state.Snapshot.ProvisionIntent == nil || prepared.Validate(*state.Snapshot.ProvisionIntent) != nil || prepared.IntentFactDigest != state.Snapshot.ProvisionIntentFactDigest {
			return ErrAllocationAuthorityConflict
		}
		if state.Snapshot.ProvisionPrepared != nil {
			if !allocationSnapshotHasPayload(state.Snapshot, allocationcontrol.RecordProvisionPrepared, payload) {
				return ErrAllocationAuthorityConflict
			}
			snapshot = cloneAllocationSnapshot(state.Snapshot)
			return nil
		}
		if effect.ReconcileFactDigest != "" || attempt.PendingEffectID != effectID {
			return ErrAllocationAuthorityConflict
		}
		fact := &allocationAuthorityFact{
			ProtocolRevision: allocationAuthorityProtocolRevision, FactType: allocationFactTypePrepared,
			Sequence: s.nextSequence, AttemptKey: mustAttemptKey(binding.Identity), AttemptRevision: attempt.Revision + 1,
			PreviousAttemptHead: attempt.HeadDigest, EffectID: effectID,
			AllocationRecordKind: allocationcontrol.RecordProvisionPrepared, AllocationAuthorityFact: payload,
			AllocationRecordedAt: s.authorityNow().Format(time.RFC3339Nano),
		}
		if err := validateStandaloneAllocationFact(*fact, projection); err != nil {
			return err
		}
		if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
			return err
		}
		s.nextSequence++
		if err := applyAllocationAuthorityFactValue(*fact, projection); err != nil {
			return err
		}
		snapshot = cloneAllocationSnapshot(projection.allocations[fact.AttemptKey].Snapshot)
		return nil
	})
	return snapshot, err
}

func (s *ingressDurableStore) appendAllocationReceipt(effectID string, binding EffectBinding, providerDomain authority.SecurityDomainId, kind allocationcontrol.RecordKind, typed any) (allocationcontrol.AuthoritySnapshot, error) {
	if err := validateAllocationProviderDomain(binding.Identity, providerDomain); err != nil {
		return allocationcontrol.AuthoritySnapshot{}, err
	}
	payload, err := allocationcontrol.EncodeFactPayload(typed)
	if err != nil {
		return allocationcontrol.AuthoritySnapshot{}, ErrAllocationAuthorityConflict
	}
	projection := newAuthorityProjection()
	var snapshot allocationcontrol.AuthoritySnapshot
	err = s.transact(projection, func() error {
		effect, attempt, state, err := allocationStateForMutation(projection, effectID, binding)
		if err != nil {
			return err
		}
		if existing := allocationFactDigest(state.Snapshot, kind); existing != "" {
			if !allocationSnapshotHasPayload(state.Snapshot, kind, payload) {
				return ErrAllocationAuthorityConflict
			}
			snapshot = cloneAllocationSnapshot(projection.allocations[mustAttemptKey(binding.Identity)].Snapshot)
			return nil
		}
		if effect.ReceiptFactDigest != "" || effect.ReconcileFactDigest != "" || attempt.PendingEffectID != effectID {
			return ErrAllocationAuthorityConflict
		}
		if err := validateAllocationReceiptAgainstSnapshot(kind, typed, state.Snapshot); err != nil {
			return err
		}
		genericReceipt, err := genericAllocationReceipt(effect, typed, providerDomain)
		if err != nil {
			return err
		}
		recordDigest, err := genericReceipt.Digest()
		if err != nil {
			return err
		}
		fact := &effectAuthorityFact{
			ProtocolRevision: effectAuthorityProtocolRevision, FactType: effectFactTypeReceipt,
			Sequence: s.nextSequence, AttemptKey: mustAttemptKey(binding.Identity), AttemptRevision: attempt.Revision + 1,
			PreviousAttemptHead: attempt.HeadDigest, Binding: binding, EffectID: effectID,
			IntentRecordDigest: effect.IntentRecordDigest, IntentFactDigest: effect.IntentFactDigest,
			Receipt: &genericReceipt, ReceiptRecordDigest: recordDigest,
			AllocationRecordKind: kind, AllocationAuthorityFact: payload,
			AllocationRecordedAt: s.authorityNow().Format(time.RFC3339Nano),
		}
		if err := validateAllocationEffectPayload(*fact, projection); err != nil {
			return err
		}
		if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
			return err
		}
		s.nextSequence++
		if err := applyEffectAuthorityFactValue(*fact, projection); err != nil {
			return err
		}
		snapshot = cloneAllocationSnapshot(projection.allocations[fact.AttemptKey].Snapshot)
		return nil
	})
	return snapshot, err
}

func allocationStateForMutation(projection *Ingress, effectID string, binding EffectBinding) (EffectAuthorityState, AttemptAuthorityState, allocationAuthorityState, error) {
	effectKey := mustEffectKey(binding.Identity.AuthorityNamespaceID, effectID)
	effect, ok := projection.effects[effectKey]
	if !ok || effect.Binding != binding || effect.Intent.EffectId != effectID {
		return EffectAuthorityState{}, AttemptAuthorityState{}, allocationAuthorityState{}, ErrAllocationAuthorityConflict
	}
	attemptKey := mustAttemptKey(binding.Identity)
	attempt, ok := projection.attempts[attemptKey]
	if !ok || attempt.Identity != binding.Identity {
		return EffectAuthorityState{}, AttemptAuthorityState{}, allocationAuthorityState{}, ErrAttemptAuthorityUnknown
	}
	state, ok := projection.allocations[attemptKey]
	if !ok || state.Snapshot.Validate() != nil {
		return EffectAuthorityState{}, AttemptAuthorityState{}, allocationAuthorityState{}, ErrAllocationAuthorityUnknown
	}
	return effect, attempt, state, nil
}

func validateAllocationReceiptAgainstSnapshot(kind allocationcontrol.RecordKind, typed any, snapshot allocationcontrol.AuthoritySnapshot) error {
	switch receipt := typed.(type) {
	case allocationcontrol.AllocationProvisionReceiptV1:
		if kind != allocationcontrol.RecordProvisionReceipt || snapshot.ProvisionIntent == nil || snapshot.ProvisionPrepared == nil || receipt.Validate(*snapshot.ProvisionIntent, *snapshot.ProvisionPrepared) != nil || receipt.IntentFactDigest != snapshot.ProvisionIntentFactDigest || receipt.PreparedFactDigest != snapshot.ProvisionPreparedFactDigest {
			return ErrAllocationAuthorityConflict
		}
	case allocationcontrol.AllocationTerminateReceiptV1:
		if kind != allocationcontrol.RecordTerminateReceipt || snapshot.TerminateIntent == nil || receipt.Validate(*snapshot.TerminateIntent) != nil || receipt.IntentFactDigest != snapshot.TerminateIntentFactDigest {
			return ErrAllocationAuthorityConflict
		}
	default:
		return ErrAllocationAuthorityConflict
	}
	return nil
}

func genericAllocationReceipt(effect EffectAuthorityState, typed any, providerDomain authority.SecurityDomainId) (authority.SideEffectReceipt, error) {
	if err := validateAllocationProviderDomain(effect.Binding.Identity, providerDomain); err != nil {
		return authority.SideEffectReceipt{}, err
	}
	var observed, resource string
	switch receipt := typed.(type) {
	case allocationcontrol.AllocationProvisionReceiptV1:
		observed = receipt.ReceiptDigest
		resource = allocationObjectIdentity(effect.Binding.Identity.AllocationID, receipt.LiveIdentity, receipt.MarkerDigest)
	case allocationcontrol.AllocationTerminateReceiptV1:
		observed = receipt.ReceiptDigest
		resource = allocationObjectIdentity(effect.Binding.Identity.AllocationID, receipt.TombstoneIdentity, receipt.MarkerDigest)
	default:
		return authority.SideEffectReceipt{}, ErrAllocationAuthorityConflict
	}
	receipt := authority.SideEffectReceipt{
		AuthorityNamespaceId: effect.Binding.Identity.AuthorityNamespaceID,
		IntentDigest:         effect.IntentRecordDigest, Disposition: authority.DispositionApplied,
		ProviderResourceIdentity: resource, ObservedDigest: observed,
		ActorProvenance:   authority.ActorProvenance{SecurityDomainId: providerDomain},
		ReconcileIdentity: allocationReconcileIdentity,
	}
	if err := receipt.Validate(); err != nil {
		return authority.SideEffectReceipt{}, ErrAllocationAuthorityConflict
	}
	return receipt, nil
}

func allocationObjectIdentity(allocationID string, object allocationcontrol.ObjectIdentityV1, markerDigest string) string {
	digest, _ := canonicalDigest(struct {
		AllocationID string                             `json:"allocationId"`
		Object       allocationcontrol.ObjectIdentityV1 `json:"object"`
		MarkerDigest string                             `json:"markerDigest"`
	}{allocationID, object, markerDigest})
	return "allocation-object:" + digest
}

func (s *ingressDurableStore) appendAllocationIntervention(canonicalEffectKey string, providerDomain authority.SecurityDomainId, kind allocationcontrol.AuthorityFailureKind) error {
	if kind.Validate() != nil {
		return ErrAllocationAuthorityConflict
	}
	projection := newAuthorityProjection()
	return s.transact(projection, func() error {
		state, ok := projection.effects[canonicalEffectKey]
		if !ok || mustEffectKey(state.Binding.Identity.AuthorityNamespaceID, state.Intent.EffectId) != canonicalEffectKey || validateAllocationProviderDomain(state.Binding.Identity, providerDomain) != nil {
			return ErrAllocationAuthorityConflict
		}
		if state.ReconcileFactDigest != "" {
			if state.AllocationFailureKind == kind && state.Reconcile.Decision == authority.DecisionBlock {
				return nil
			}
			return ErrAllocationAuthorityConflict
		}
		if state.ReceiptFactDigest == "" {
			disposition := authority.DispositionAmbiguous
			if kind == allocationcontrol.AuthorityFailureConflict {
				disposition = authority.DispositionConflict
			}
			observedDigest, err := canonicalDigest(struct {
				SchemaVersion string                                 `json:"schemaVersion"`
				EffectKey     string                                 `json:"effectKey"`
				FailureKind   allocationcontrol.AuthorityFailureKind `json:"failureKind"`
				IntentDigest  string                                 `json:"intentDigest"`
			}{"marshal/allocation-intervention/v1", canonicalEffectKey, kind, state.IntentRecordDigest})
			if err != nil {
				return err
			}
			receipt := authority.SideEffectReceipt{
				AuthorityNamespaceId: state.Binding.Identity.AuthorityNamespaceID,
				IntentDigest:         state.IntentRecordDigest, Disposition: disposition,
				ProviderResourceIdentity: "allocation-intervention:" + state.Binding.MarkerDigest,
				ObservedDigest:           observedDigest,
				ActorProvenance:          authority.ActorProvenance{SecurityDomainId: providerDomain},
				ReconcileIdentity:        allocationReconcileIdentity,
			}
			if err := receipt.Validate(); err != nil {
				return ErrAllocationAuthorityConflict
			}
			recordDigest, err := receipt.Digest()
			if err != nil {
				return err
			}
			attemptKey := mustAttemptKey(state.Binding.Identity)
			attempt := projection.attempts[attemptKey]
			fact := &effectAuthorityFact{
				ProtocolRevision: effectAuthorityProtocolRevision, FactType: effectFactTypeReceipt,
				Sequence: s.nextSequence, AttemptKey: attemptKey, AttemptRevision: attempt.Revision + 1,
				PreviousAttemptHead: attempt.HeadDigest, Binding: state.Binding, EffectID: state.Intent.EffectId,
				IntentRecordDigest: state.IntentRecordDigest, IntentFactDigest: state.IntentFactDigest,
				Receipt: &receipt, ReceiptRecordDigest: recordDigest, AllocationFailureKind: kind,
			}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
				return err
			}
			s.nextSequence++
			if err := applyEffectAuthorityFactValue(*fact, projection); err != nil {
				return err
			}
		} else if state.AllocationFailureKind != kind {
			attemptKey := mustAttemptKey(state.Binding.Identity)
			allocationState, hasAllocation := projection.allocations[attemptKey]
			typedEffect := hasAllocation && (state.Binding.Phase == EffectPhaseAllocationProvision && allocationState.ProvisionEffectID == state.Intent.EffectId || state.Binding.Phase == EffectPhaseAllocationTerminate && allocationState.TerminateEffectID == state.Intent.EffectId)
			if state.AllocationFailureKind == "" && !typedEffect && (state.Binding.Phase == EffectPhaseAllocationProvision || state.Binding.Phase == EffectPhaseAllocationTerminate) {
				// Pre-Stage2 generic allocation history may already contain an
				// immutable provider receipt. Preserve that receipt and close only
				// the authority bypass as an explicit intervention.
				return s.appendAllocationReconcileLocked(projection, canonicalEffectKey, kind)
			}
			return ErrAllocationAuthorityConflict
		}
		return s.appendAllocationReconcileLocked(projection, canonicalEffectKey)
	})
}

func allocationFailureForProviderError(err error) allocationcontrol.AuthorityFailureKind {
	switch {
	case errors.Is(err, allocationcontrol.ErrFilesystemConflict), errors.Is(err, allocationcontrol.ErrAuthorityConflict):
		return allocationcontrol.AuthorityFailureConflict
	case errors.Is(err, allocationcontrol.ErrFilesystemUnknown):
		return allocationcontrol.AuthorityFailureUnknown
	default:
		return ""
	}
}

func (s *ingressDurableStore) appendAllocationProjectionIntervention(canonicalEffectKey string, kind allocationcontrol.AuthorityFailureKind) error {
	if kind.Validate() != nil {
		return ErrAllocationAuthorityConflict
	}
	projection := newAuthorityProjection()
	return s.transact(projection, func() error {
		state, ok := projection.effects[canonicalEffectKey]
		if !ok || state.ReceiptFactDigest == "" || state.Receipt.Disposition != authority.DispositionApplied {
			return ErrAllocationAuthorityConflict
		}
		if state.ReconcileFactDigest != "" {
			if state.AllocationFailureKind == kind && state.Reconcile.Decision == authority.DecisionBlock {
				return nil
			}
			return ErrAllocationAuthorityConflict
		}
		return s.appendAllocationReconcileLocked(projection, canonicalEffectKey, kind)
	})
}

func (s *ingressDurableStore) repairAllocationReconcile(canonicalEffectKey string) error {
	projection := newAuthorityProjection()
	return s.transact(projection, func() error {
		state, ok := projection.effects[canonicalEffectKey]
		if !ok {
			return ErrAllocationAuthorityUnknown
		}
		if state.ReconcileFactDigest != "" {
			return nil
		}
		if state.ReceiptFactDigest == "" {
			return nil
		}
		return s.appendAllocationReconcileLocked(projection, canonicalEffectKey)
	})
}

func (s *ingressDurableStore) appendAllocationReconcileLocked(projection *Ingress, canonicalEffectKey string, failureOverride ...allocationcontrol.AuthorityFailureKind) error {
	state, ok := projection.effects[canonicalEffectKey]
	if !ok || state.ReceiptFactDigest == "" {
		return ErrAllocationAuthorityConflict
	}
	effectID := state.Intent.EffectId
	if state.ReconcileFactDigest != "" {
		return nil
	}
	attemptKey := mustAttemptKey(state.Binding.Identity)
	attempt, ok := projection.attempts[attemptKey]
	if !ok || attempt.PendingEffectID != effectID || attempt.PendingEffectIntentFactDigest != state.IntentFactDigest {
		return ErrAllocationAuthorityConflict
	}
	failureKind := state.AllocationFailureKind
	if len(failureOverride) > 1 || len(failureOverride) == 1 && failureOverride[0].Validate() != nil {
		return ErrAllocationAuthorityConflict
	}
	if len(failureOverride) == 1 {
		if failureKind != "" && failureKind != failureOverride[0] {
			return ErrAllocationAuthorityConflict
		}
		failureKind = failureOverride[0]
	}
	allocationState, hasAllocation := projection.allocations[attemptKey]
	typedEffect := hasAllocation && (state.Binding.Phase == EffectPhaseAllocationProvision && allocationState.ProvisionEffectID == effectID || state.Binding.Phase == EffectPhaseAllocationTerminate && allocationState.TerminateEffectID == effectID)
	legacyReceiptIntervention := len(failureOverride) == 1 && state.AllocationFailureKind == "" && !typedEffect && (state.Binding.Phase == EffectPhaseAllocationProvision || state.Binding.Phase == EffectPhaseAllocationTerminate)
	inspection := EffectInspection{Outcome: EffectInspectionApplied, Receipt: state.Receipt}
	observation, decision := authority.ObservationApplied, authority.DecisionAccept
	if failureKind != "" {
		observation = allocationFailureObservation(failureKind)
		if observation == "" {
			return ErrAllocationAuthorityConflict
		}
		if legacyReceiptIntervention {
			var ok bool
			inspection.Outcome, ok = effectInspectionOutcomeForReceipt(state.Receipt.Disposition)
			if !ok {
				return ErrAllocationAuthorityConflict
			}
		} else {
			switch failureKind {
			case allocationcontrol.AuthorityFailureConflict:
				if state.Receipt.Disposition != authority.DispositionApplied {
					inspection.Outcome = EffectInspectionConflict
				}
			case allocationcontrol.AuthorityFailureAmbiguous:
				if state.Receipt.Disposition != authority.DispositionApplied {
					inspection.Outcome = EffectInspectionAmbiguous
				}
			case allocationcontrol.AuthorityFailureUnknown:
				if state.Receipt.Disposition != authority.DispositionApplied {
					inspection.Outcome = EffectInspectionUnknown
				}
			default:
				return ErrAllocationAuthorityConflict
			}
		}
		decision = authority.DecisionBlock
	}
	inspectionDigest, err := canonicalDigest(inspection)
	if err != nil {
		return err
	}
	record := authority.ReconcileRecord{
		AuthorityNamespaceId: state.Binding.Identity.AuthorityNamespaceID,
		Observation:          observation, Decision: decision,
		IntentDigest: state.IntentRecordDigest, ReceiptDigest: state.ReceiptRecordDigest,
	}
	recordDigest, err := record.Digest()
	if err != nil {
		return err
	}
	fact := &effectAuthorityFact{
		ProtocolRevision: effectAuthorityProtocolRevision, FactType: effectFactTypeReconcile,
		Sequence: s.nextSequence, AttemptKey: attemptKey, AttemptRevision: attempt.Revision + 1,
		PreviousAttemptHead: attempt.HeadDigest, Binding: state.Binding, EffectID: effectID,
		IntentRecordDigest: state.IntentRecordDigest, IntentFactDigest: state.IntentFactDigest,
		ReceiptRecordDigest: state.ReceiptRecordDigest, ReceiptFactDigest: state.ReceiptFactDigest,
		Reconcile: &record, ReconcileRecordDigest: recordDigest,
		Inspection: &inspection, InspectionDigest: inspectionDigest,
		AllocationFailureKind: failureKind,
	}
	if err := s.appendLine(fact, func() string { return fact.Digest }, func(d string) { fact.Digest = d }); err != nil {
		return err
	}
	s.nextSequence++
	if err := applyEffectAuthorityFactValue(*fact, projection); err != nil {
		return err
	}
	return nil
}

func allocationFailureObservation(kind allocationcontrol.AuthorityFailureKind) authority.Observation {
	switch kind {
	case allocationcontrol.AuthorityFailureConflict:
		return authority.ObservationConflict
	case allocationcontrol.AuthorityFailureAmbiguous, allocationcontrol.AuthorityFailureUnknown:
		return authority.ObservationUnknown
	default:
		return ""
	}
}

func effectInspectionOutcomeForReceipt(disposition authority.Disposition) (EffectInspectionOutcome, bool) {
	switch disposition {
	case authority.DispositionApplied:
		return EffectInspectionApplied, true
	case authority.DispositionNotApplied:
		return EffectInspectionNotApplied, true
	case authority.DispositionAmbiguous:
		return EffectInspectionAmbiguous, true
	case authority.DispositionConflict:
		return EffectInspectionConflict, true
	default:
		return "", false
	}
}

func applyAllocationAuthorityLine(line []byte, in *Ingress, wantSequence int64) error {
	canonicalLine, err := canonical.JSON(line)
	if err != nil || !bytes.Equal(canonicalLine, line) {
		return ErrAllocationAuthorityConflict
	}
	var fact allocationAuthorityFact
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		return fmt.Errorf("%w: %v", ErrAllocationAuthorityConflict, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrAllocationAuthorityConflict
	}
	if fact.ProtocolRevision != allocationAuthorityProtocolRevision || fact.FactType != allocationFactTypePrepared || fact.Sequence != wantSequence {
		return ErrAllocationAuthorityConflict
	}
	stored := fact.Digest
	fact.Digest = ""
	digest, err := canonicalDigest(fact)
	if err != nil || stored == "" || digest != stored {
		return ErrAllocationAuthorityConflict
	}
	fact.Digest = stored
	return applyAllocationAuthorityFactValue(fact, in)
}

func applyAllocationAuthorityFactValue(fact allocationAuthorityFact, in *Ingress) error {
	if err := validateStandaloneAllocationFact(fact, in); err != nil {
		return err
	}
	attempt := in.attempts[fact.AttemptKey]
	if err := appendAllocationProjection(in, fact.AttemptKey, fact.EffectID, fact.AllocationRecordKind, fact.AllocationAuthorityFact, fact.AllocationRecordedAt, fact.AttemptRevision, fact.Digest); err != nil {
		return err
	}
	attempt.Revision, attempt.HeadDigest = fact.AttemptRevision, fact.Digest
	in.attempts[fact.AttemptKey] = attempt
	return nil
}

func validateStandaloneAllocationFact(fact allocationAuthorityFact, in *Ingress) error {
	if fact.ProtocolRevision != allocationAuthorityProtocolRevision || fact.FactType != allocationFactTypePrepared || fact.AllocationRecordKind != allocationcontrol.RecordProvisionPrepared || strings.TrimSpace(fact.EffectID) == "" || len(fact.AllocationAuthorityFact) == 0 || canonicalRecordedAt(fact.AllocationRecordedAt) != nil {
		return ErrAllocationAuthorityConflict
	}
	attempt, ok := in.attempts[fact.AttemptKey]
	if !ok || fact.AttemptRevision != attempt.Revision+1 || fact.PreviousAttemptHead != attempt.HeadDigest || attempt.PendingEffectID != fact.EffectID || attempt.PendingEffectPhase != EffectPhaseAllocationProvision {
		return ErrAllocationAuthorityConflict
	}
	state, ok := in.allocations[fact.AttemptKey]
	if !ok || state.ProvisionEffectID != fact.EffectID || state.Snapshot.ProvisionPrepared != nil {
		return ErrAllocationAuthorityConflict
	}
	var prepared allocationcontrol.AllocationStagingPreparedV1
	if err := decodeAllocationPayload(fact.AllocationAuthorityFact, &prepared); err != nil || state.Snapshot.ProvisionIntent == nil || prepared.Validate(*state.Snapshot.ProvisionIntent) != nil || prepared.IntentFactDigest != state.Snapshot.ProvisionIntentFactDigest {
		return ErrAllocationAuthorityConflict
	}
	return nil
}

func applyAllocationProjectionFromEffectFact(fact effectAuthorityFact, in *Ingress) error {
	allEmpty := fact.AllocationRecordKind == "" && len(fact.AllocationAuthorityFact) == 0 && fact.AllocationRecordedAt == ""
	if allEmpty {
		return nil
	}
	if fact.AllocationRecordKind == "" || len(fact.AllocationAuthorityFact) == 0 || canonicalRecordedAt(fact.AllocationRecordedAt) != nil {
		return ErrAllocationAuthorityConflict
	}
	if err := validateAllocationEffectPayload(fact, in); err != nil {
		return err
	}
	return appendAllocationProjection(in, fact.AttemptKey, fact.EffectID, fact.AllocationRecordKind, fact.AllocationAuthorityFact, fact.AllocationRecordedAt, fact.AttemptRevision, fact.Digest)
}

func validateAllocationEffectPayload(fact effectAuthorityFact, in *Ingress) error {
	if fact.FactType != effectFactTypeIntent && fact.FactType != effectFactTypeReceipt {
		return ErrAllocationAuthorityConflict
	}
	if canonicalRecordedAt(fact.AllocationRecordedAt) != nil {
		return ErrAllocationAuthorityConflict
	}
	attemptKey := fact.AttemptKey
	state, exists := in.allocations[attemptKey]
	switch fact.AllocationRecordKind {
	case allocationcontrol.RecordProvisionIntent:
		var intent allocationcontrol.AllocationProvisionIntentV1
		if fact.FactType != effectFactTypeIntent || exists || decodeAllocationPayload(fact.AllocationAuthorityFact, &intent) != nil || validateAllocationProvisionIntentMapping(fact.Binding.Identity, EffectIntentRequest{Binding: fact.Binding, Intent: dereferenceIntent(fact.Intent)}, intent) != nil || intent.ExpectedAttemptSequence != fact.AttemptRevision || intent.AttemptAuthorityFactDigest != fact.PreviousAttemptHead {
			return ErrAllocationAuthorityConflict
		}
	case allocationcontrol.RecordProvisionReceipt:
		var receipt allocationcontrol.AllocationProvisionReceiptV1
		if decodeAllocationPayload(fact.AllocationAuthorityFact, &receipt) != nil {
			return ErrAllocationAuthorityConflict
		}
		effect := in.effects[mustEffectKey(fact.Binding.Identity.AuthorityNamespaceID, fact.EffectID)]
		providerDomain := authority.SecurityDomainId{}
		if fact.Receipt != nil {
			providerDomain = fact.Receipt.ActorProvenance.SecurityDomainId
		}
		wantReceipt, receiptErr := genericAllocationReceipt(effect, receipt, providerDomain)
		if fact.FactType != effectFactTypeReceipt || !exists || state.ProvisionEffectID != fact.EffectID || validateAllocationReceiptAgainstSnapshot(fact.AllocationRecordKind, receipt, state.Snapshot) != nil || fact.Receipt == nil || receiptErr != nil || *fact.Receipt != wantReceipt {
			return ErrAllocationAuthorityConflict
		}
	case allocationcontrol.RecordTerminateIntent:
		var intent allocationcontrol.AllocationTerminateIntentV1
		if fact.FactType != effectFactTypeIntent || !exists || state.TerminateEffectID != "" || decodeAllocationPayload(fact.AllocationAuthorityFact, &intent) != nil || validateAllocationTerminateIntentMapping(fact.Binding.Identity, EffectIntentRequest{Binding: fact.Binding, Intent: dereferenceIntent(fact.Intent)}, intent) != nil || intent.ExpectedAttemptSequence != fact.AttemptRevision || intent.AttemptAuthorityFactDigest != fact.PreviousAttemptHead {
			return ErrAllocationAuthorityConflict
		}
	case allocationcontrol.RecordTerminateReceipt:
		var receipt allocationcontrol.AllocationTerminateReceiptV1
		if decodeAllocationPayload(fact.AllocationAuthorityFact, &receipt) != nil {
			return ErrAllocationAuthorityConflict
		}
		effect := in.effects[mustEffectKey(fact.Binding.Identity.AuthorityNamespaceID, fact.EffectID)]
		providerDomain := authority.SecurityDomainId{}
		if fact.Receipt != nil {
			providerDomain = fact.Receipt.ActorProvenance.SecurityDomainId
		}
		wantReceipt, receiptErr := genericAllocationReceipt(effect, receipt, providerDomain)
		if fact.FactType != effectFactTypeReceipt || !exists || state.TerminateEffectID != fact.EffectID || validateAllocationReceiptAgainstSnapshot(fact.AllocationRecordKind, receipt, state.Snapshot) != nil || fact.Receipt == nil || receiptErr != nil || *fact.Receipt != wantReceipt {
			return ErrAllocationAuthorityConflict
		}
	default:
		return ErrAllocationAuthorityConflict
	}
	return nil
}

func dereferenceIntent(intent *authority.SideEffectIntent) authority.SideEffectIntent {
	if intent == nil {
		return authority.SideEffectIntent{}
	}
	return *intent
}

func appendAllocationProjection(in *Ingress, attemptKey, effectID string, kind allocationcontrol.RecordKind, payload json.RawMessage, recordedAt string, revision uint64, factDigest string) error {
	state := in.allocations[attemptKey]
	committed := allocationcontrol.CommittedAuthorityFact{
		RecordKind: kind, RecordID: string(kind) + ":" + canonical.DigestBytes([]byte(effectID)), RecordedAt: recordedAt,
		ExpectedAttemptSequence: revision, AttemptAuthorityFactDigest: factDigest,
		AuthorityFact: append(json.RawMessage(nil), payload...),
	}
	switch kind {
	case allocationcontrol.RecordProvisionIntent:
		var value allocationcontrol.AllocationProvisionIntentV1
		if decodeAllocationPayload(payload, &value) != nil {
			return ErrAllocationAuthorityConflict
		}
		committed.Binding, committed.RequestDigest = value.Binding, value.RequestDigest
		state.ProvisionEffectID = effectID
		state.Snapshot.ProvisionIntent, state.Snapshot.ProvisionIntentFactDigest = &value, factDigest
	case allocationcontrol.RecordProvisionPrepared:
		var value allocationcontrol.AllocationStagingPreparedV1
		if decodeAllocationPayload(payload, &value) != nil {
			return ErrAllocationAuthorityConflict
		}
		committed.Binding, committed.RequestDigest = value.Binding, value.RequestDigest
		state.Snapshot.ProvisionPrepared, state.Snapshot.ProvisionPreparedFactDigest = &value, factDigest
	case allocationcontrol.RecordProvisionReceipt:
		var value allocationcontrol.AllocationProvisionReceiptV1
		if decodeAllocationPayload(payload, &value) != nil {
			return ErrAllocationAuthorityConflict
		}
		committed.Binding, committed.RequestDigest = value.Binding, value.RequestDigest
		state.Snapshot.ProvisionReceipt, state.Snapshot.ProvisionReceiptFactDigest = &value, factDigest
	case allocationcontrol.RecordTerminateIntent:
		var value allocationcontrol.AllocationTerminateIntentV1
		if decodeAllocationPayload(payload, &value) != nil {
			return ErrAllocationAuthorityConflict
		}
		committed.Binding, committed.RequestDigest = value.Binding, value.RequestDigest
		committed.TerminalizationID, committed.CleanupBindingDigest, committed.ProcessTerminalFactDigest = value.TerminalizationID, value.CleanupBindingDigest, value.ProcessTerminalFactDigest
		state.TerminateEffectID = effectID
		state.Snapshot.TerminateIntent, state.Snapshot.TerminateIntentFactDigest = &value, factDigest
	case allocationcontrol.RecordTerminateReceipt:
		var value allocationcontrol.AllocationTerminateReceiptV1
		if decodeAllocationPayload(payload, &value) != nil {
			return ErrAllocationAuthorityConflict
		}
		committed.Binding, committed.RequestDigest = value.Binding, value.RequestDigest
		committed.TerminalizationID, committed.CleanupBindingDigest, committed.ProcessTerminalFactDigest = value.TerminalizationID, value.CleanupBindingDigest, value.ProcessTerminalFactDigest
		state.Snapshot.TerminateReceipt, state.Snapshot.TerminateReceiptFactDigest = &value, factDigest
	default:
		return ErrAllocationAuthorityConflict
	}
	if committed.Validate() != nil {
		return ErrAllocationAuthorityConflict
	}
	state.Snapshot.Facts = append(state.Snapshot.Facts, committed)
	if state.Snapshot.Validate() != nil {
		return ErrAllocationAuthorityConflict
	}
	in.allocations[attemptKey] = state
	return nil
}

func decodeAllocationPayload(payload json.RawMessage, target any) error {
	canonicalPayload, err := canonical.JSON(payload)
	if err != nil || !bytes.Equal(canonicalPayload, payload) {
		return ErrAllocationAuthorityConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrAllocationAuthorityConflict
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrAllocationAuthorityConflict
	}
	return nil
}

func canonicalRecordedAt(value string) error {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.UTC().Format(time.RFC3339Nano) != value {
		return ErrAllocationAuthorityConflict
	}
	return nil
}

func allocationFactDigest(snapshot allocationcontrol.AuthoritySnapshot, kind allocationcontrol.RecordKind) string {
	switch kind {
	case allocationcontrol.RecordProvisionIntent:
		return snapshot.ProvisionIntentFactDigest
	case allocationcontrol.RecordProvisionPrepared:
		return snapshot.ProvisionPreparedFactDigest
	case allocationcontrol.RecordProvisionReceipt:
		return snapshot.ProvisionReceiptFactDigest
	case allocationcontrol.RecordTerminateIntent:
		return snapshot.TerminateIntentFactDigest
	case allocationcontrol.RecordTerminateReceipt:
		return snapshot.TerminateReceiptFactDigest
	default:
		return ""
	}
}

func allocationSnapshotHasPayload(snapshot allocationcontrol.AuthoritySnapshot, kind allocationcontrol.RecordKind, payload json.RawMessage) bool {
	digest := allocationFactDigest(snapshot, kind)
	for _, fact := range snapshot.Facts {
		if fact.RecordKind == kind && fact.AttemptAuthorityFactDigest == digest && bytes.Equal(fact.AuthorityFact, payload) {
			return true
		}
	}
	return false
}

func cloneAllocationSnapshot(snapshot allocationcontrol.AuthoritySnapshot) allocationcontrol.AuthoritySnapshot {
	cloned := snapshot
	cloned.Facts = append([]allocationcontrol.CommittedAuthorityFact(nil), snapshot.Facts...)
	for index := range cloned.Facts {
		cloned.Facts[index].AuthorityFact = append(json.RawMessage(nil), cloned.Facts[index].AuthorityFact...)
	}
	return cloned
}
