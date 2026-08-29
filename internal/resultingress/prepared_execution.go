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

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

const (
	PreparedExecutionSchema            = "PreparedExecutionV1"
	PreparedExecutionProtocol          = "prepared-execution/v1"
	preparedExecutionAuthorityProtocol = "prepared-execution-authority/v1"
	preparedExecutionCreatedFactType   = "prepared-execution-created"
)

var (
	ErrPreparedExecutionConflict    = errors.New("resultingress: prepared execution authority conflict")
	ErrPreparedExecutionUnavailable = errors.New("resultingress: prepared execution unavailable")
	ErrCommittedRunStartProof       = errors.New("resultingress: committed Run-start proof violation")
)

// PreparedExecutionV1 is a creation-once, secret-safe index into authority
// facts held by this same ResultIngress ledger. It deliberately contains no
// path, argv, environment, stdin, nonce, handle or transcript bytes.
type PreparedExecutionV1 struct {
	SchemaVersion                        string              `json:"schemaVersion"`
	ProtocolRevision                     string              `json:"protocolRevision"`
	AttemptIdentity                      AttemptIdentity     `json:"attemptIdentity"`
	RunAuthorityBinding                  RunAuthorityBinding `json:"runAuthorityBinding"`
	ExpectedRunSequence                  uint64              `json:"expectedRunSequence"`
	ExpectedRunAuthorityHead             string              `json:"expectedRunAuthorityHead"`
	CurrentOwnerBinding                  CurrentOwnerBinding `json:"currentOwnerBinding"`
	ControlOwnerBoundFactDigest          string              `json:"controlOwnerBoundFactDigest"`
	AttemptAuthorityHeadAtPreparation    string              `json:"attemptAuthorityHeadAtPreparation"`
	AllocationProvisionReceiptFactDigest string              `json:"allocationProvisionReceiptFactDigest"`
	AllocationProvisionReceiptDigest     string              `json:"allocationProvisionReceiptDigest"`
	LaunchAuthorizationID                string              `json:"launchAuthorizationId"`
	LaunchAuthorizedFactDigest           string              `json:"launchAuthorizedFactDigest"`
	StoredClosureDigest                  string              `json:"storedClosureDigest"`
	LaunchMaterialsDigest                string              `json:"launchMaterialsDigest"`
	AgentLaunchSpecDigest                string              `json:"agentLaunchSpecDigest"`
	Pi0843IdentityDigest                 string              `json:"pi0843IdentityDigest"`
	PreparationDigest                    string              `json:"preparationDigest"`
}

func (prepared PreparedExecutionV1) Validate() error {
	if prepared.SchemaVersion != PreparedExecutionSchema || prepared.ProtocolRevision != PreparedExecutionProtocol ||
		prepared.AttemptIdentity.Validate() != nil || prepared.RunAuthorityBinding != runAuthorityBindingFor(prepared.AttemptIdentity) ||
		prepared.ExpectedRunSequence == 0 || prepared.ExpectedRunSequence > maxExactJSONInteger || prepared.CurrentOwnerBinding.Validate() != nil ||
		prepared.CurrentOwnerBinding.Scope.AuthorityNamespaceID != prepared.AttemptIdentity.AuthorityNamespaceID ||
		strings.TrimSpace(prepared.LaunchAuthorizationID) == "" ||
		prepared.AttemptAuthorityHeadAtPreparation != prepared.LaunchAuthorizedFactDigest {
		return ErrPreparedExecutionConflict
	}
	for _, digest := range []string{
		prepared.ExpectedRunAuthorityHead, prepared.ControlOwnerBoundFactDigest,
		prepared.AttemptAuthorityHeadAtPreparation, prepared.AllocationProvisionReceiptFactDigest,
		prepared.AllocationProvisionReceiptDigest, prepared.LaunchAuthorizedFactDigest,
		prepared.StoredClosureDigest, prepared.LaunchMaterialsDigest,
		prepared.AgentLaunchSpecDigest, prepared.Pi0843IdentityDigest, prepared.PreparationDigest,
	} {
		if requireDigest("preparedExecutionDigest", digest) != nil {
			return ErrPreparedExecutionConflict
		}
	}
	want, err := preparedExecutionDigest(prepared)
	if err != nil || want != prepared.PreparationDigest {
		return ErrPreparedExecutionConflict
	}
	return nil
}

func preparedExecutionDigest(prepared PreparedExecutionV1) (string, error) {
	prepared.PreparationDigest = ""
	return canonicalDigest(prepared)
}

func sealPreparedExecution(prepared PreparedExecutionV1) (PreparedExecutionV1, error) {
	digest, err := preparedExecutionDigest(prepared)
	if err != nil {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	prepared.PreparationDigest = digest
	if prepared.Validate() != nil {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	return prepared, nil
}

// DecodePreparedExecution accepts only the exact canonical closed wire form.
func DecodePreparedExecution(raw []byte) (PreparedExecutionV1, error) {
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(canonicalRaw, raw) {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	var prepared PreparedExecutionV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&prepared); err != nil {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || prepared.Validate() != nil {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	return prepared, nil
}

// PreparedExecutionCreation carries only Run self-facts. ResultIngress derives
// owner/Attempt/allocation/source and Pi identity exclusively from its current
// ledger; callers cannot supply or override an Agent identity digest.
type PreparedExecutionCreation struct {
	Identity                 AttemptIdentity
	ExpectedRunSequence      uint64
	ExpectedRunAuthorityHead string
}

func (creation PreparedExecutionCreation) validate() error {
	if creation.Identity.Validate() != nil || creation.ExpectedRunSequence == 0 || creation.ExpectedRunSequence > maxExactJSONInteger ||
		requireDigest("expectedRunAuthorityHead", creation.ExpectedRunAuthorityHead) != nil {
		return ErrPreparedExecutionConflict
	}
	return nil
}

type preparedExecutionFact struct {
	ProtocolRevision string              `json:"protocolRevision"`
	FactType         string              `json:"factType"`
	Sequence         int64               `json:"sequence"`
	Prepared         PreparedExecutionV1 `json:"prepared"`
	Digest           string              `json:"digest"`
}

// CommittedRunStartClaim is non-authority provenance. It intentionally omits
// owner, generation, fencing, Run head/successor and raw source material.
type CommittedRunStartClaim struct {
	TaskID                   string
	RunID                    string
	AttemptID                string
	PreparationDigest        string
	ProcessStartedFactDigest string
	ResumeOutcomeFactDigest  string
}

type committedRunStartGuard struct {
	mu       sync.Mutex
	cond     *sync.Cond
	active   bool
	claimed  bool
	inFlight int
	violated bool
	claim    CommittedRunStartClaim
}

type CommittedRunStartProof struct{ guard *committedRunStartGuard }

func newCommittedRunStartProof(claim CommittedRunStartClaim) CommittedRunStartProof {
	guard := &committedRunStartGuard{active: true, claim: claim}
	guard.cond = sync.NewCond(&guard.mu)
	return CommittedRunStartProof{guard: guard}
}

// WithClaim permits exactly one synchronous claim across every proof copy.
func (proof CommittedRunStartProof) WithClaim(fn func(CommittedRunStartClaim) error) (err error) {
	if proof.guard == nil {
		return ErrCommittedRunStartProof
	}
	guard := proof.guard
	guard.mu.Lock()
	if fn == nil || !guard.active || guard.claimed {
		guard.violated = true
		guard.mu.Unlock()
		return ErrCommittedRunStartProof
	}
	guard.claimed = true
	guard.inFlight++
	claim := guard.claim
	guard.mu.Unlock()
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("%w: claim callback panic", ErrCommittedRunStartProof)
		}
		guard.mu.Lock()
		guard.inFlight--
		if err != nil {
			guard.violated = true
		}
		guard.cond.Broadcast()
		guard.mu.Unlock()
	}()
	return fn(claim)
}

func (guard *committedRunStartGuard) deactivateAndWait() error {
	if guard == nil {
		return ErrCommittedRunStartProof
	}
	guard.mu.Lock()
	escaped := guard.inFlight != 0
	guard.active = false
	for guard.inFlight != 0 {
		guard.cond.Wait()
	}
	guard.claim = CommittedRunStartClaim{}
	valid := guard.claimed && !guard.violated && !escaped
	guard.mu.Unlock()
	if !valid {
		return ErrCommittedRunStartProof
	}
	return nil
}

type RunStartProjector interface {
	ProjectCommittedRunStart(context.Context, CommittedRunStartProof) error
}

func (s *DurableStore) CreatePreparedExecution(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, creation PreparedExecutionCreation) (PreparedExecutionV1, error) {
	if s == nil || ctx == nil || acquisition.Validate() != nil || creation.validate() != nil || acquisition.Scope.AuthorityNamespaceID != creation.Identity.AuthorityNamespaceID {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	var result PreparedExecutionV1
	err := withCurrentOwnerLock(ctx, verifier, acquisition, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			key, err := creation.Identity.Key()
			if err != nil {
				return ErrPreparedExecutionConflict
			}
			if digest, ok := projection.preparedExecutionKeys[key]; ok {
				existing, found := projection.preparedExecutions[digest]
				if !found || !preparedCreationMatches(existing, creation) {
					return ErrPreparedExecutionConflict
				}
				result = existing
				return nil
			}
			state, ok := projection.attempts[key]
			if !ok || state.Identity != creation.Identity || state.Owner.OwnerEpoch != acquisition.OwnerEpoch {
				return ErrPreparedExecutionConflict
			}
			prepared, err := derivePreparedExecution(projection, state, creation)
			if err != nil {
				return err
			}
			fact := &preparedExecutionFact{ProtocolRevision: preparedExecutionAuthorityProtocol, FactType: preparedExecutionCreatedFactType, Sequence: s.nextSequence, Prepared: prepared}
			if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
				return err
			}
			s.nextSequence++
			result = prepared
			return nil
		})
	})
	return result, err
}

func (s *DurableStore) ResolvePreparedExecution(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, digest string) (PreparedExecutionV1, error) {
	if s == nil || ctx == nil || acquisition.Validate() != nil || requireDigest("preparationDigest", digest) != nil {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	var result PreparedExecutionV1
	err := withCurrentOwnerLock(ctx, verifier, acquisition, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			prepared, _, err := resolvePreparedCurrent(projection, acquisition, digest)
			if err == nil {
				result = prepared
			}
			return err
		})
	})
	return result, err
}

// StartPreparedExecution holds the repository owner and ResultIngress ledger
// through the sealed Darwin fresh-start mechanics, final byte replay, proof
// mint and synchronous Run projection. Generic stores and non-Darwin profiles
// fail closed; callers cannot supply a mechanics driver.
func (s *DurableStore) StartPreparedExecution(ctx context.Context, verifier CurrentOwnerLockVerifier, acquisition ControlOwnerAcquisition, identity AttemptIdentity, preparationDigest string, projector RunStartProjector) error {
	if s == nil || ctx == nil || projector == nil || acquisition.Validate() != nil || identity.Validate() != nil || acquisition.Scope.AuthorityNamespaceID != identity.AuthorityNamespaceID || requireDigest("preparationDigest", preparationDigest) != nil {
		return ErrPreparedExecutionConflict
	}
	return withCurrentOwnerLock(ctx, verifier, acquisition, func() error {
		projection := newAuthorityProjection()
		return s.transact(projection, func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			prepared, state, err := resolvePreparedCurrent(projection, acquisition, preparationDigest)
			if err != nil || prepared.AttemptIdentity != identity {
				return ErrPreparedExecutionConflict
			}
			state, err = s.reconcilePreparedExecutionLocked(ctx, projection, prepared, state)
			if err != nil {
				return err
			}
			resumeDigest, err := exactSuccessfulResume(state)
			if err != nil {
				return err
			}
			// Rebuild once more from bytes immediately before minting. No prior
			// in-memory projection is authority for the proof.
			fresh := newAuthorityProjection()
			s.nextSequence = 1
			if err := s.recoverIntoLocked(fresh); err != nil {
				return err
			}
			freshPrepared, freshState, err := resolvePreparedCurrent(fresh, acquisition, preparationDigest)
			if err != nil || freshPrepared != prepared || freshState.Identity != state.Identity || freshState.Owner != state.Owner || freshState.HeadDigest != state.HeadDigest {
				return ErrPreparedExecutionConflict
			}
			freshResume, err := exactSuccessfulResume(freshState)
			if err != nil || freshResume != resumeDigest || ctx.Err() != nil {
				return ErrPreparedExecutionConflict
			}
			proof := newCommittedRunStartProof(CommittedRunStartClaim{
				TaskID: identity.TaskID, RunID: identity.RunID, AttemptID: identity.AttemptID,
				PreparationDigest: preparationDigest, ProcessStartedFactDigest: freshState.ProcessStartedDigest,
				ResumeOutcomeFactDigest: freshResume,
			})
			projectErr := callRunStartProjector(ctx, projector, proof)
			proofErr := proof.guard.deactivateAndWait()
			if projectErr != nil {
				return projectErr
			}
			return proofErr
		})
	})
}

func callRunStartProjector(ctx context.Context, projector RunStartProjector, proof CommittedRunStartProof) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("%w: projector panic", ErrCommittedRunStartProof)
		}
	}()
	return projector.ProjectCommittedRunStart(ctx, proof)
}

func preparedCreationMatches(prepared PreparedExecutionV1, creation PreparedExecutionCreation) bool {
	return prepared.AttemptIdentity == creation.Identity && prepared.ExpectedRunSequence == creation.ExpectedRunSequence && prepared.ExpectedRunAuthorityHead == creation.ExpectedRunAuthorityHead
}

func derivePreparedExecution(projection *Ingress, state AttemptAuthorityState, creation PreparedExecutionCreation) (PreparedExecutionV1, error) {
	if state.Identity != creation.Identity || state.HeadDigest != state.LaunchAuthorizedDigest || state.ControlOwnerBindingDigest == "" || state.Owner.Validate() != nil || state.LaunchState != LaunchUncertain || state.PendingEffectIntentFactDigest != "" || state.EffectInterventionDigest != "" || state.SupervisorInterventionDigest != "" || state.SupervisorBootstrapDigest != "" || state.ProcessStartedDigest != "" {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	ownerKey, err := state.Owner.Scope.key()
	if err != nil {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	owner, ok := projection.controlOwners[ownerKey]
	if !ok || !currentOwnerMatches(owner, state.Owner) {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	allocation, receipt, err := currentPreparedProvisionReceipt(projection, state)
	if err != nil {
		return PreparedExecutionV1{}, err
	}
	closure, err := state.LaunchClosure.Closure()
	if err != nil || closure.LaunchMaterialsDigest != state.LaunchMaterialsDigest || closure.AgentLaunchSpecDigest != state.AgentLaunchSpecDigest {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	closureDigest, err := canonicalDigest(state.LaunchClosure)
	if err != nil {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	piIdentity, err := launchidentity.Pi0843IdentityFromClosure(closure)
	if err != nil || piIdentity.Validate() != nil {
		return PreparedExecutionV1{}, ErrPreparedExecutionConflict
	}
	return sealPreparedExecution(PreparedExecutionV1{
		SchemaVersion: PreparedExecutionSchema, ProtocolRevision: PreparedExecutionProtocol,
		AttemptIdentity: state.Identity, RunAuthorityBinding: runAuthorityBindingFor(state.Identity),
		ExpectedRunSequence: creation.ExpectedRunSequence, ExpectedRunAuthorityHead: creation.ExpectedRunAuthorityHead,
		CurrentOwnerBinding: state.Owner, ControlOwnerBoundFactDigest: state.ControlOwnerBindingDigest,
		AttemptAuthorityHeadAtPreparation:    state.HeadDigest,
		AllocationProvisionReceiptFactDigest: allocation.Snapshot.ProvisionReceiptFactDigest,
		AllocationProvisionReceiptDigest:     receipt.ReceiptDigest,
		LaunchAuthorizationID:                state.LaunchAuthorizationID, LaunchAuthorizedFactDigest: state.LaunchAuthorizedDigest,
		StoredClosureDigest: closureDigest, LaunchMaterialsDigest: state.LaunchMaterialsDigest,
		AgentLaunchSpecDigest: state.AgentLaunchSpecDigest, Pi0843IdentityDigest: piIdentity.IdentityDigest,
	})
}

func currentPreparedProvisionReceipt(projection *Ingress, state AttemptAuthorityState) (allocationAuthorityState, allocationcontrol.AllocationProvisionReceiptV1, error) {
	key, err := state.Identity.Key()
	if err != nil {
		return allocationAuthorityState{}, allocationcontrol.AllocationProvisionReceiptV1{}, ErrPreparedExecutionConflict
	}
	allocation, ok := projection.allocations[key]
	if !ok || allocation.Snapshot.Validate() != nil || allocation.Snapshot.ProvisionReceipt == nil || allocation.Snapshot.TerminateIntent != nil || allocation.ProvisionEffectID == "" {
		return allocationAuthorityState{}, allocationcontrol.AllocationProvisionReceiptV1{}, ErrPreparedExecutionConflict
	}
	receipt := *allocation.Snapshot.ProvisionReceipt
	if !allocationBindingMatchesIdentity(receipt.Binding, state.Identity) {
		return allocationAuthorityState{}, allocationcontrol.AllocationProvisionReceiptV1{}, ErrPreparedExecutionConflict
	}
	effectID, err := effectKey(state.Identity.AuthorityNamespaceID, allocation.ProvisionEffectID)
	if err != nil {
		return allocationAuthorityState{}, allocationcontrol.AllocationProvisionReceiptV1{}, ErrPreparedExecutionConflict
	}
	effect, ok := projection.effects[effectID]
	if !ok || effect.Binding.Identity != state.Identity || effect.Binding.Phase != EffectPhaseAllocationProvision || effect.Receipt.Disposition != authority.DispositionApplied || effect.Receipt.ObservedDigest != receipt.ReceiptDigest || effect.ReconcileInspection.Outcome != EffectInspectionApplied || effect.Reconcile.Observation != authority.ObservationApplied || effect.Reconcile.Decision != authority.DecisionAccept || effect.ReconcileFactDigest != state.AllocationProvisionEffectDigest || effect.ReceiptRecordDigest != state.AllocationProvisionReceiptDigest {
		return allocationAuthorityState{}, allocationcontrol.AllocationProvisionReceiptV1{}, ErrPreparedExecutionConflict
	}
	return allocation, receipt, nil
}

func resolvePreparedCurrent(projection *Ingress, acquisition ControlOwnerAcquisition, digest string) (PreparedExecutionV1, AttemptAuthorityState, error) {
	prepared, ok := projection.preparedExecutions[digest]
	if !ok || prepared.Validate() != nil {
		return PreparedExecutionV1{}, AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
	}
	key, err := acquisition.Scope.key()
	if err != nil {
		return PreparedExecutionV1{}, AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	owner, ok := projection.controlOwners[key]
	if !ok || owner.Acquisition != acquisition {
		return PreparedExecutionV1{}, AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	attemptKey, err := prepared.AttemptIdentity.Key()
	if err != nil {
		return PreparedExecutionV1{}, AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	state, ok := projection.attempts[attemptKey]
	if !ok || state.Identity != prepared.AttemptIdentity || state.Owner.OwnerEpoch != acquisition.OwnerEpoch || state.Owner.Scope != acquisition.Scope || !currentOwnerMatches(owner, state.Owner) || state.LaunchAuthorizationID != prepared.LaunchAuthorizationID || state.LaunchAuthorizedDigest != prepared.LaunchAuthorizedFactDigest || state.LaunchMaterialsDigest != prepared.LaunchMaterialsDigest || state.AgentLaunchSpecDigest != prepared.AgentLaunchSpecDigest || state.PendingEffectIntentFactDigest != "" || state.EffectInterventionDigest != "" || state.SupervisorInterventionDigest != "" || state.BarrierDigest != "" {
		return PreparedExecutionV1{}, AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	closureDigest, err := canonicalDigest(state.LaunchClosure)
	if err != nil || closureDigest != prepared.StoredClosureDigest {
		return PreparedExecutionV1{}, AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	allocation, receipt, err := currentPreparedProvisionReceipt(projection, state)
	if err != nil || receipt.ReceiptDigest != prepared.AllocationProvisionReceiptDigest || allocation.Snapshot.ProvisionReceiptFactDigest != prepared.AllocationProvisionReceiptFactDigest {
		return PreparedExecutionV1{}, AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	return prepared, state, nil
}

func exactSuccessfulResume(state AttemptAuthorityState) (string, error) {
	if requireDigest("processStartedFactDigest", state.ProcessStartedDigest) != nil || state.LaunchState != LaunchStarted || state.SupervisorPendingIntentDigest != "" || len(state.SupervisorCommandCheckpoints) == 0 {
		return "", ErrPreparedExecutionUnavailable
	}
	latest := state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1]
	evidence := latest.Evidence
	if requireDigest("resumeOutcomeFactDigest", latest.FactDigest) != nil || latest.FactDigest != state.SupervisorCommandRecoveryHead || evidence.Validate() != nil || evidence.Command != processsupervisor.CommandResume || evidence.Disposition != "ok" || evidence.ReasonCode != "process-resumed" || evidence.CurrentAuthorityHead != state.HeadDigest || evidence.Outcome.State != SupervisorProcessRunning || evidence.Outcome.MechanicsState != "running" || evidence.Outcome.SourceGateRevision != processsupervisor.SourceGateRevisionV1 || requireDigest("exactSetDigest", evidence.Outcome.ExactSetDigest) != nil || !sameSupervisorChildEvidence(state.ProcessStartedEvidence, evidence) {
		return "", ErrPreparedExecutionConflict
	}
	return latest.FactDigest, nil
}

func applyPreparedExecutionLine(line []byte, in *Ingress, wantSequence int64) error {
	canonicalLine, err := canonical.JSON(line)
	if err != nil || !bytes.Equal(canonicalLine, line) {
		return ErrPreparedExecutionConflict
	}
	var fact preparedExecutionFact
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		return ErrPreparedExecutionConflict
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || fact.ProtocolRevision != preparedExecutionAuthorityProtocol || fact.FactType != preparedExecutionCreatedFactType || fact.Sequence != wantSequence || fact.Prepared.Validate() != nil {
		return ErrPreparedExecutionConflict
	}
	stored := fact.Digest
	fact.Digest = ""
	digest, err := canonicalDigest(fact)
	if err != nil || digest != stored {
		return ErrPreparedExecutionConflict
	}
	return applyPreparedExecutionFactValue(fact, in)
}

func applyPreparedExecutionFactValue(fact preparedExecutionFact, in *Ingress) error {
	if in == nil || fact.Prepared.Validate() != nil {
		return ErrPreparedExecutionConflict
	}
	key, err := fact.Prepared.AttemptIdentity.Key()
	if err != nil {
		return ErrPreparedExecutionConflict
	}
	state, ok := in.attempts[key]
	if !ok {
		return ErrPreparedExecutionConflict
	}
	creation := PreparedExecutionCreation{Identity: fact.Prepared.AttemptIdentity, ExpectedRunSequence: fact.Prepared.ExpectedRunSequence, ExpectedRunAuthorityHead: fact.Prepared.ExpectedRunAuthorityHead}
	derived, err := derivePreparedExecution(in, state, creation)
	if err != nil || derived != fact.Prepared {
		return ErrPreparedExecutionConflict
	}
	if _, duplicate := in.preparedExecutionKeys[key]; duplicate {
		return ErrPreparedExecutionConflict
	}
	if _, duplicate := in.preparedExecutions[fact.Prepared.PreparationDigest]; duplicate {
		return ErrPreparedExecutionConflict
	}
	in.preparedExecutionKeys[key] = fact.Prepared.PreparationDigest
	in.preparedExecutions[fact.Prepared.PreparationDigest] = fact.Prepared
	return nil
}
