package resultingress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

const existingWorktreeAuthorityProtocolRevision = "existing-worktree-attempt-authority/v1"

const (
	existingWorktreeBindIntentFactType     = "existing-worktree-bind-intent"
	existingWorktreeBindReceiptFactType    = "existing-worktree-bind-receipt"
	existingWorktreeReleaseIntentFactType  = "existing-worktree-release-intent"
	existingWorktreeReleaseReceiptFactType = "existing-worktree-release-receipt"
)

var ErrExistingWorktreeAuthorityConflict = errors.New("resultingress: existing-worktree authority conflict")

type existingWorktreeAuthorityFact struct {
	ProtocolRevision    string                                     `json:"protocolRevision"`
	FactType            string                                     `json:"factType"`
	Sequence            int64                                      `json:"sequence"`
	AttemptKey          string                                     `json:"attemptKey"`
	AttemptRevision     uint64                                     `json:"attemptRevision"`
	PreviousAttemptHead string                                     `json:"previousAttemptHead"`
	Kind                allocationcontrol.ExistingWorktreeFactKind `json:"existingWorktreeFactKind"`
	Payload             json.RawMessage                            `json:"existingWorktreePayload"`
	PayloadDigest       string                                     `json:"existingWorktreePayloadDigest"`
	Digest              string                                     `json:"digest"`
}

// ExistingWorktreeBindAuthorityCheck is non-bearer input to the production
// authority provider. The provider must hold, in this order, the canonical
// repository owner, RunStore AcquireExisting lease/descriptor, active
// reservation+attempt-opened/v2 and current DispatchLease for the callback.
type ExistingWorktreeBindAuthorityCheck struct {
	Identity AttemptIdentity
	Run      allocationcontrol.DescriptorBoundRunV1
	Request  allocationcontrol.ExistingWorktreeBindRequestV1
}

// ExistingWorktreeReleaseAuthorityCheck has the same held owner/Run ordering
// and additionally requires current terminalization, cleanup binding and
// process-terminal facts. Format-valid caller digests grant no authority.
type ExistingWorktreeReleaseAuthorityCheck struct {
	Identity AttemptIdentity
	Run      allocationcontrol.DescriptorBoundRunV1
	Request  allocationcontrol.ExistingWorktreeReleaseRequestV1
}

type CurrentExistingWorktreeAuthorityVerifier interface {
	WithCurrentExistingWorktreeBind(context.Context, ExistingWorktreeBindAuthorityCheck, func(allocationcontrol.ExistingWorktreeCurrentAuthorityV1, allocationcontrol.ExistingWorktreeDescriptorGraphV1) error) error
	WithCurrentExistingWorktreeRelease(context.Context, ExistingWorktreeReleaseAuthorityCheck, func(allocationcontrol.ExistingWorktreeCurrentAuthorityV1, allocationcontrol.ExistingWorktreeDescriptorGraphV1) error) error
}

// ExistingWorktreeAuthority is the production-callable RB1 port. It appends
// into DurableStore's one ResultIngress/Attempt ledger and treats the
// `.marshal/runtime-v1/existing-worktree-bindings` files only as rebuildable
// projections.
type ExistingWorktreeAuthority struct {
	store    *DurableStore
	verifier CurrentExistingWorktreeAuthorityVerifier
}

var _ allocationcontrol.ExistingWorktreeAuthority = (*ExistingWorktreeAuthority)(nil)

func NewExistingWorktreeAuthority(store *DurableStore, verifier CurrentExistingWorktreeAuthorityVerifier) (*ExistingWorktreeAuthority, error) {
	if store == nil || verifier == nil || store.requireBound() != nil {
		return nil, ErrExistingWorktreeAuthorityConflict
	}
	return &ExistingWorktreeAuthority{store: store, verifier: verifier}, nil
}

func (authorityPort *ExistingWorktreeAuthority) WithCurrentExistingWorktreeBind(ctx context.Context, run allocationcontrol.DescriptorBoundRunV1, request allocationcontrol.ExistingWorktreeBindRequestV1, operation func(allocationcontrol.ExistingWorktreeAuthoritySession) error) error {
	if authorityPort == nil || authorityPort.store == nil || authorityPort.verifier == nil || operation == nil || request.Validate() != nil {
		return ErrExistingWorktreeAuthorityConflict
	}
	identity, err := authorityPort.store.findExistingWorktreeAttempt(request.Binding)
	if err != nil {
		return err
	}
	check := ExistingWorktreeBindAuthorityCheck{Identity: identity, Run: run, Request: request}
	return withHeldExistingWorktreeVerifier(func(callback func(allocationcontrol.ExistingWorktreeCurrentAuthorityV1, allocationcontrol.ExistingWorktreeDescriptorGraphV1) error) error {
		return authorityPort.verifier.WithCurrentExistingWorktreeBind(ctx, check, callback)
	}, func(current allocationcontrol.ExistingWorktreeCurrentAuthorityV1, graph allocationcontrol.ExistingWorktreeDescriptorGraphV1) error {
		if allocationcontrol.ValidateExistingWorktreeCurrentBind(current, request, run) != nil {
			return ErrExistingWorktreeAuthorityConflict
		}
		snapshot, err := authorityPort.store.loadExistingWorktreeSnapshot(identity)
		if err != nil || snapshot.CurrentAttemptHeadDigest != current.AttemptAuthorityHeadDigest || snapshot.CurrentAttemptRevision == 0 || current.AttemptOpenedFactDigest != request.Binding.AttemptOpenedFactDigest {
			return ErrExistingWorktreeAuthorityConflict
		}
		session := &existingWorktreeAuthoritySession{authority: authorityPort, identity: identity, current: current, graph: graph, active: true}
		defer session.close()
		return operation(session)
	})
}

func (authorityPort *ExistingWorktreeAuthority) WithCurrentExistingWorktreeRelease(ctx context.Context, run allocationcontrol.DescriptorBoundRunV1, request allocationcontrol.ExistingWorktreeReleaseRequestV1, operation func(allocationcontrol.ExistingWorktreeAuthoritySession) error) error {
	if authorityPort == nil || authorityPort.store == nil || authorityPort.verifier == nil || operation == nil || request.Validate() != nil {
		return ErrExistingWorktreeAuthorityConflict
	}
	identity, err := authorityPort.store.findExistingWorktreeAttempt(request.Binding)
	if err != nil {
		return err
	}
	check := ExistingWorktreeReleaseAuthorityCheck{Identity: identity, Run: run, Request: request}
	return withHeldExistingWorktreeVerifier(func(callback func(allocationcontrol.ExistingWorktreeCurrentAuthorityV1, allocationcontrol.ExistingWorktreeDescriptorGraphV1) error) error {
		return authorityPort.verifier.WithCurrentExistingWorktreeRelease(ctx, check, callback)
	}, func(current allocationcontrol.ExistingWorktreeCurrentAuthorityV1, graph allocationcontrol.ExistingWorktreeDescriptorGraphV1) error {
		if allocationcontrol.ValidateExistingWorktreeCurrentRelease(current, request, run) != nil {
			return ErrExistingWorktreeAuthorityConflict
		}
		snapshot, err := authorityPort.store.loadExistingWorktreeSnapshot(identity)
		if err != nil || snapshot.CurrentAttemptHeadDigest != current.AttemptAuthorityHeadDigest {
			return ErrExistingWorktreeAuthorityConflict
		}
		session := &existingWorktreeAuthoritySession{authority: authorityPort, identity: identity, current: current, graph: graph, active: true}
		defer session.close()
		return operation(session)
	})
}

func withHeldExistingWorktreeVerifier(hold func(func(allocationcontrol.ExistingWorktreeCurrentAuthorityV1, allocationcontrol.ExistingWorktreeDescriptorGraphV1) error) error, operation func(allocationcontrol.ExistingWorktreeCurrentAuthorityV1, allocationcontrol.ExistingWorktreeDescriptorGraphV1) error) error {
	var gate sync.Mutex
	called, closed, duplicate, inFlight := false, false, false, false
	completed := make(chan struct{})
	var callbackErr error
	holdErr := hold(func(current allocationcontrol.ExistingWorktreeCurrentAuthorityV1, graph allocationcontrol.ExistingWorktreeDescriptorGraphV1) error {
		gate.Lock()
		if closed || called {
			duplicate = true
			gate.Unlock()
			return ErrExistingWorktreeAuthorityConflict
		}
		called, inFlight = true, true
		gate.Unlock()
		operationErr := operation(current, graph)
		gate.Lock()
		inFlight, callbackErr = false, operationErr
		close(completed)
		gate.Unlock()
		return operationErr
	})
	gate.Lock()
	if inFlight {
		gate.Unlock()
		// A verifier is required to invoke the callback synchronously. If it
		// returns while an admitted callback is still running, contain that
		// callback before closing the session and report the contract breach.
		<-completed
		gate.Lock()
		duplicate = true
	}
	closed = true
	calledOnce, repeated, innerErr := called, duplicate, callbackErr
	gate.Unlock()
	if repeated {
		return ErrExistingWorktreeAuthorityConflict
	}
	if innerErr != nil {
		return innerErr
	}
	if holdErr != nil || !calledOnce {
		return ErrExistingWorktreeAuthorityConflict
	}
	return nil
}

type existingWorktreeAuthoritySession struct {
	authority *ExistingWorktreeAuthority
	identity  AttemptIdentity
	current   allocationcontrol.ExistingWorktreeCurrentAuthorityV1
	graph     allocationcontrol.ExistingWorktreeDescriptorGraphV1
	gate      sync.Mutex
	active    bool
}

var _ allocationcontrol.ExistingWorktreeAuthoritySession = (*existingWorktreeAuthoritySession)(nil)

func (session *existingWorktreeAuthoritySession) CurrentAuthority() allocationcontrol.ExistingWorktreeCurrentAuthorityV1 {
	session.gate.Lock()
	defer session.gate.Unlock()
	if !session.active {
		return allocationcontrol.ExistingWorktreeCurrentAuthorityV1{}
	}
	return session.current
}

func (session *existingWorktreeAuthoritySession) WithExistingWorktreeTarget(ctx context.Context, request allocationcontrol.ExistingWorktreeBindRequestV1, expected *allocationcontrol.ExistingWorktreeObservationV1, callback func(allocationcontrol.ExistingWorktreeTargetSession) error) error {
	session.gate.Lock()
	if !session.active || callback == nil {
		session.gate.Unlock()
		return ErrExistingWorktreeAuthorityConflict
	}
	session.gate.Unlock()
	return allocationcontrol.WithExistingWorktreeTargetFromGraph(ctx, session.graph, request, expected, callback)
}

func (session *existingWorktreeAuthoritySession) SyncExistingWorktreeProjection(_ context.Context, snapshot allocationcontrol.ExistingWorktreeAuthoritySnapshotV1) error {
	session.gate.Lock()
	defer session.gate.Unlock()
	if !session.active || snapshot.Validate() != nil {
		return ErrExistingWorktreeAuthorityConflict
	}
	return allocationcontrol.SyncExistingWorktreeProjectionFromGraph(session.graph, snapshot)
}

func (session *existingWorktreeAuthoritySession) Snapshot() (allocationcontrol.ExistingWorktreeAuthoritySnapshotV1, error) {
	session.gate.Lock()
	defer session.gate.Unlock()
	if !session.active {
		return allocationcontrol.ExistingWorktreeAuthoritySnapshotV1{}, ErrExistingWorktreeAuthorityConflict
	}
	return session.authority.store.loadExistingWorktreeSnapshot(session.identity)
}

func (session *existingWorktreeAuthoritySession) append(ctx context.Context, kind allocationcontrol.ExistingWorktreeFactKind, payload any) (allocationcontrol.ExistingWorktreeAuthoritySnapshotV1, error) {
	if err := ctx.Err(); err != nil {
		return allocationcontrol.ExistingWorktreeAuthoritySnapshotV1{}, err
	}
	session.gate.Lock()
	defer session.gate.Unlock()
	if !session.active {
		return allocationcontrol.ExistingWorktreeAuthoritySnapshotV1{}, ErrExistingWorktreeAuthorityConflict
	}
	return session.authority.store.appendExistingWorktreeFact(session.identity, kind, payload)
}

func (session *existingWorktreeAuthoritySession) AppendBindIntent(ctx context.Context, value allocationcontrol.ExistingWorktreeBindIntentV1) (allocationcontrol.ExistingWorktreeAuthoritySnapshotV1, error) {
	return session.append(ctx, allocationcontrol.ExistingWorktreeFactBindIntent, value)
}
func (session *existingWorktreeAuthoritySession) AppendBindReceipt(ctx context.Context, value allocationcontrol.ExistingWorktreeBindReceiptV1) (allocationcontrol.ExistingWorktreeAuthoritySnapshotV1, error) {
	return session.append(ctx, allocationcontrol.ExistingWorktreeFactBindReceipt, value)
}
func (session *existingWorktreeAuthoritySession) AppendReleaseIntent(ctx context.Context, value allocationcontrol.ExistingWorktreeReleaseIntentV1) (allocationcontrol.ExistingWorktreeAuthoritySnapshotV1, error) {
	return session.append(ctx, allocationcontrol.ExistingWorktreeFactReleaseIntent, value)
}
func (session *existingWorktreeAuthoritySession) AppendReleaseReceipt(ctx context.Context, value allocationcontrol.ExistingWorktreeReleaseReceiptV1) (allocationcontrol.ExistingWorktreeAuthoritySnapshotV1, error) {
	return session.append(ctx, allocationcontrol.ExistingWorktreeFactReleaseReceipt, value)
}

func (session *existingWorktreeAuthoritySession) close() {
	session.gate.Lock()
	defer session.gate.Unlock()
	session.active = false
}

func (s *DurableStore) findExistingWorktreeAttempt(binding allocationcontrol.ExistingWorktreeBindingV1) (AttemptIdentity, error) {
	projection := newAuthorityProjection()
	var identity AttemptIdentity
	err := s.transact(projection, func() error {
		found := false
		for _, attempt := range projection.attempts {
			if !existingWorktreeBindingMatchesAttempt(binding, attempt) {
				continue
			}
			if found {
				return ErrExistingWorktreeAuthorityConflict
			}
			identity, found = attempt.Identity, true
		}
		if !found {
			return ErrAttemptAuthorityUnknown
		}
		return nil
	})
	return identity, err
}

func existingWorktreeBindingMatchesAttempt(binding allocationcontrol.ExistingWorktreeBindingV1, attempt AttemptAuthorityState) bool {
	namespaceDigest, err := attempt.Identity.AuthorityNamespaceID.Digest()
	if err != nil || binding.Validate() != nil || binding.AuthorityNamespaceID != namespaceDigest || binding.TaskID != attempt.Identity.TaskID || binding.RunID != attempt.Identity.RunID || binding.AttemptID != attempt.Identity.AttemptID || binding.AllocationID != attempt.Identity.AllocationID || binding.LeaseID != attempt.Identity.LeaseID || binding.Generation != attempt.Identity.DispatchGeneration || binding.FencingTokenDigest != attempt.Identity.FencingTokenDigest || binding.AttemptOpenedFactDigest != attempt.OpenedDigest {
		return false
	}
	// Fresh existing-worktree authority is defined only for the v2
	// reservation-bound producer. Legacy v1 Attempt facts remain replay-only;
	// no new bind/release fact may be appended to them.
	if attempt.ReservationFactDigest == "" || binding.ReservationFactDigest != attempt.ReservationFactDigest {
		return false
	}
	return true
}

func (s *DurableStore) loadExistingWorktreeSnapshot(identity AttemptIdentity) (allocationcontrol.ExistingWorktreeAuthoritySnapshotV1, error) {
	projection := newAuthorityProjection()
	var snapshot allocationcontrol.ExistingWorktreeAuthoritySnapshotV1
	err := s.transact(projection, func() error {
		key, err := identity.Key()
		if err != nil {
			return err
		}
		attempt, ok := projection.attempts[key]
		if !ok || attempt.Identity != identity {
			return ErrAttemptAuthorityUnknown
		}
		snapshot = existingWorktreeSnapshot(projection, attempt)
		return snapshot.Validate()
	})
	return snapshot, err
}

func existingWorktreeSnapshot(projection *Ingress, attempt AttemptAuthorityState) allocationcontrol.ExistingWorktreeAuthoritySnapshotV1 {
	return allocationcontrol.ExistingWorktreeAuthoritySnapshotV1{CurrentAttemptRevision: attempt.Revision, CurrentAttemptHeadDigest: attempt.HeadDigest, Facts: append([]allocationcontrol.ExistingWorktreeAttemptFactV1(nil), projection.existingWorktreeFacts...)}
}

func (s *DurableStore) appendExistingWorktreeFact(identity AttemptIdentity, kind allocationcontrol.ExistingWorktreeFactKind, payload any) (allocationcontrol.ExistingWorktreeAuthoritySnapshotV1, error) {
	raw, err := canonicalExistingWorktreePayload(payload)
	if err != nil {
		return allocationcontrol.ExistingWorktreeAuthoritySnapshotV1{}, err
	}
	projection := newAuthorityProjection()
	var snapshot allocationcontrol.ExistingWorktreeAuthoritySnapshotV1
	err = s.transact(projection, func() error {
		key, err := identity.Key()
		if err != nil {
			return err
		}
		attempt, ok := projection.attempts[key]
		if !ok || attempt.Identity != identity {
			return ErrAttemptAuthorityUnknown
		}
		predecessor, binding, err := existingWorktreePayloadAuthority(kind, raw)
		if err != nil {
			return ErrExistingWorktreeAuthorityConflict
		}
		// Exact replay: if the current Attempt head is already an existing-
		// worktree fact with the same kind and canonical payload, the same
		// canonical request was already durable. Return the existing snapshot
		// without appending a duplicate line. This closes the response-loss
		// window for RB1 bind/release facts (same canonical request returns the
		// same receipt; different bytes conflict below).
		payloadDigest := canonical.DigestBytes(raw)
		for _, existing := range projection.existingWorktreeFacts {
			if existing.AttemptKey != key || existing.AttemptFactDigest != attempt.HeadDigest {
				continue
			}
			if existing.Kind == kind && existing.PayloadDigest == payloadDigest {
				snapshot = existingWorktreeSnapshot(projection, attempt)
				return snapshot.Validate()
			}
			break
		}
		if predecessor != attempt.HeadDigest || !existingWorktreeBindingMatchesAttempt(binding, attempt) {
			return ErrExistingWorktreeAuthorityConflict
		}
		factType, err := existingWorktreeFactType(kind)
		if err != nil {
			return err
		}
		fact := &existingWorktreeAuthorityFact{ProtocolRevision: existingWorktreeAuthorityProtocolRevision, FactType: factType, Sequence: s.nextSequence, AttemptKey: key, AttemptRevision: attempt.Revision + 1, PreviousAttemptHead: attempt.HeadDigest, Kind: kind, Payload: raw, PayloadDigest: payloadDigest}
		preflight, err := canonicalDigest(fact)
		if err != nil {
			return err
		}
		fact.Digest = preflight
		if err := validateExistingWorktreeFact(*fact, projection); err != nil {
			return err
		}
		fact.Digest = ""
		if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
			return err
		}
		if fact.Digest != preflight {
			return ErrExistingWorktreeAuthorityConflict
		}
		s.nextSequence++
		if err := applyExistingWorktreeFactValue(*fact, projection); err != nil {
			return err
		}
		snapshot = existingWorktreeSnapshot(projection, projection.attempts[key])
		return snapshot.Validate()
	})
	return snapshot, err
}

func canonicalExistingWorktreePayload(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, ErrExistingWorktreeAuthorityConflict
	}
	jcs, err := canonical.JSON(raw)
	if err != nil {
		return nil, ErrExistingWorktreeAuthorityConflict
	}
	return json.RawMessage(jcs), nil
}

func existingWorktreePayloadAuthority(kind allocationcontrol.ExistingWorktreeFactKind, raw json.RawMessage) (string, allocationcontrol.ExistingWorktreeBindingV1, error) {
	switch kind {
	case allocationcontrol.ExistingWorktreeFactBindIntent:
		var value allocationcontrol.ExistingWorktreeBindIntentV1
		if strictExistingWorktreeDecode(raw, &value) != nil || value.Validate() != nil {
			return "", allocationcontrol.ExistingWorktreeBindingV1{}, ErrExistingWorktreeAuthorityConflict
		}
		return value.PredecessorRB1HeadDigest, value.Request.Binding, nil
	case allocationcontrol.ExistingWorktreeFactBindReceipt:
		var value allocationcontrol.ExistingWorktreeBindReceiptV1
		if strictExistingWorktreeDecode(raw, &value) != nil {
			return "", allocationcontrol.ExistingWorktreeBindingV1{}, ErrExistingWorktreeAuthorityConflict
		}
		return value.PredecessorRB1HeadDigest, value.Binding, nil
	case allocationcontrol.ExistingWorktreeFactReleaseIntent:
		var value allocationcontrol.ExistingWorktreeReleaseIntentV1
		if strictExistingWorktreeDecode(raw, &value) != nil || value.Validate() != nil {
			return "", allocationcontrol.ExistingWorktreeBindingV1{}, ErrExistingWorktreeAuthorityConflict
		}
		return value.PredecessorRB1HeadDigest, value.Request.Binding, nil
	case allocationcontrol.ExistingWorktreeFactReleaseReceipt:
		var value allocationcontrol.ExistingWorktreeReleaseReceiptV1
		if strictExistingWorktreeDecode(raw, &value) != nil {
			return "", allocationcontrol.ExistingWorktreeBindingV1{}, ErrExistingWorktreeAuthorityConflict
		}
		return value.PredecessorRB1HeadDigest, value.Binding, nil
	default:
		return "", allocationcontrol.ExistingWorktreeBindingV1{}, ErrExistingWorktreeAuthorityConflict
	}
}

func strictExistingWorktreeDecode(raw []byte, target any) error {
	canonicalRaw, err := canonical.JSON(raw)
	if err != nil || !bytes.Equal(canonicalRaw, raw) {
		return ErrExistingWorktreeAuthorityConflict
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrExistingWorktreeAuthorityConflict
	}
	return nil
}

func existingWorktreeFactType(kind allocationcontrol.ExistingWorktreeFactKind) (string, error) {
	switch kind {
	case allocationcontrol.ExistingWorktreeFactBindIntent:
		return existingWorktreeBindIntentFactType, nil
	case allocationcontrol.ExistingWorktreeFactBindReceipt:
		return existingWorktreeBindReceiptFactType, nil
	case allocationcontrol.ExistingWorktreeFactReleaseIntent:
		return existingWorktreeReleaseIntentFactType, nil
	case allocationcontrol.ExistingWorktreeFactReleaseReceipt:
		return existingWorktreeReleaseReceiptFactType, nil
	default:
		return "", ErrExistingWorktreeAuthorityConflict
	}
}

func applyExistingWorktreeAuthorityLine(line []byte, in *Ingress, wantSequence int64) error {
	canonicalLine, err := canonical.JSON(line)
	if err != nil || !bytes.Equal(canonicalLine, line) {
		return ErrExistingWorktreeAuthorityConflict
	}
	var fact existingWorktreeAuthorityFact
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fact); err != nil {
		return fmt.Errorf("%w: %v", ErrExistingWorktreeAuthorityConflict, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrExistingWorktreeAuthorityConflict
	}
	wantType, err := existingWorktreeFactType(fact.Kind)
	if err != nil || fact.ProtocolRevision != existingWorktreeAuthorityProtocolRevision || fact.FactType != wantType || fact.Sequence != wantSequence {
		return ErrExistingWorktreeAuthorityConflict
	}
	stored := fact.Digest
	fact.Digest = ""
	digest, err := canonicalDigest(fact)
	if err != nil || stored == "" || digest != stored {
		return ErrExistingWorktreeAuthorityConflict
	}
	fact.Digest = stored
	return applyExistingWorktreeFactValue(fact, in)
}

func applyExistingWorktreeFactValue(fact existingWorktreeAuthorityFact, in *Ingress) error {
	if err := validateExistingWorktreeFact(fact, in); err != nil {
		return err
	}
	attempt := in.attempts[fact.AttemptKey]
	in.existingWorktreeFacts = append(in.existingWorktreeFacts, allocationcontrol.ExistingWorktreeAttemptFactV1{AttemptKey: fact.AttemptKey, AttemptRevision: fact.AttemptRevision, Kind: fact.Kind, PreviousAttemptHeadDigest: fact.PreviousAttemptHead, Payload: append(json.RawMessage(nil), fact.Payload...), PayloadDigest: fact.PayloadDigest, AttemptFactDigest: fact.Digest})
	// Project the exact bind/release receipt fact/digest onto the shared
	// Attempt authority so launch-authorized and PreparedExecution can bind and
	// revalidate the exact existing-worktree receipt without requiring an
	// AllocationProvision effect reconcile.
	switch fact.Kind {
	case allocationcontrol.ExistingWorktreeFactBindIntent:
		attempt.ExistingWorktreeBindIntentFactDigest = fact.Digest
	case allocationcontrol.ExistingWorktreeFactBindReceipt:
		var receipt allocationcontrol.ExistingWorktreeBindReceiptV1
		if strictExistingWorktreeDecode(fact.Payload, &receipt) != nil {
			return ErrExistingWorktreeAuthorityConflict
		}
		attempt.ExistingWorktreeBindReceiptFactDigest = fact.Digest
		attempt.ExistingWorktreeBindReceiptDigest = receipt.ReceiptDigest
	case allocationcontrol.ExistingWorktreeFactReleaseIntent:
		attempt.ExistingWorktreeReleaseIntentFactDigest = fact.Digest
	case allocationcontrol.ExistingWorktreeFactReleaseReceipt:
		var receipt allocationcontrol.ExistingWorktreeReleaseReceiptV1
		if strictExistingWorktreeDecode(fact.Payload, &receipt) != nil {
			return ErrExistingWorktreeAuthorityConflict
		}
		attempt.ExistingWorktreeReleaseReceiptFactDigest = fact.Digest
		attempt.ExistingWorktreeReleaseReceiptDigest = receipt.ReceiptDigest
	}
	attempt.Revision, attempt.HeadDigest = fact.AttemptRevision, fact.Digest
	in.attempts[fact.AttemptKey] = attempt
	return nil
}

// CurrentExistingWorktreeBindReceipt returns the exact unreleased RB1 bind
// receipt after replaying the same durable Attempt ledger used by launch. It
// is a read-only composition helper; callers still need the held owner, Run
// descriptor and release authority before they may mutate the RB1 chain.
func (s *DurableStore) CurrentExistingWorktreeBindReceipt(identity AttemptIdentity) (allocationcontrol.ExistingWorktreeBindReceiptV1, error) {
	projection := newAuthorityProjection()
	var receipt allocationcontrol.ExistingWorktreeBindReceiptV1
	err := s.transact(projection, func() error {
		key, err := identity.Key()
		if err != nil {
			return err
		}
		state, found := projection.attempts[key]
		if !found || state.Identity != identity {
			return ErrAttemptAuthorityUnknown
		}
		snapshot := existingWorktreeSnapshot(projection, state)
		if err := snapshot.Validate(); err != nil {
			return ErrExistingWorktreeAuthorityConflict
		}
		var intent allocationcontrol.ExistingWorktreeBindIntentV1
		foundIntent, foundReceipt := false, false
		for _, fact := range snapshot.Facts {
			if fact.AttemptKey != key {
				continue
			}
			switch fact.Kind {
			case allocationcontrol.ExistingWorktreeFactBindIntent:
				if foundIntent || strictExistingWorktreeDecode(fact.Payload, &intent) != nil || intent.Validate() != nil {
					return ErrExistingWorktreeAuthorityConflict
				}
				foundIntent = true
			case allocationcontrol.ExistingWorktreeFactBindReceipt:
				if foundReceipt || strictExistingWorktreeDecode(fact.Payload, &receipt) != nil {
					return ErrExistingWorktreeAuthorityConflict
				}
				foundReceipt = true
			}
		}
		if !foundIntent || !foundReceipt || receipt.Validate(intent) != nil || receipt.ReceiptDigest != state.ExistingWorktreeBindReceiptDigest {
			return ErrExistingWorktreeAuthorityConflict
		}
		return nil
	})
	return receipt, err
}

// CurrentExistingWorktreeSnapshot returns the complete current RB1 snapshot
// for one exact Attempt identity. It is read-only; callers must already hold
// and recheck the repository owner before using it to admit any filesystem
// observation or lifecycle transition.
func (s *DurableStore) CurrentExistingWorktreeSnapshot(identity AttemptIdentity) (allocationcontrol.ExistingWorktreeAuthoritySnapshotV1, error) {
	return s.loadExistingWorktreeSnapshot(identity)
}

// CurrentExistingWorktreeRelease projects an already-created release union.
// A pending intent returns its exact sealed request so recovery cannot create
// a sibling request from the later Attempt head; a committed receipt is
// returned byte-for-byte and needs no new authority mutation.
func (s *DurableStore) CurrentExistingWorktreeRelease(identity AttemptIdentity) (allocationcontrol.ExistingWorktreeReleaseRequestV1, allocationcontrol.ExistingWorktreeReleaseReceiptV1, bool, bool, error) {
	projection := newAuthorityProjection()
	var request allocationcontrol.ExistingWorktreeReleaseRequestV1
	var receipt allocationcontrol.ExistingWorktreeReleaseReceiptV1
	found, complete := false, false
	err := s.transact(projection, func() error {
		key, err := identity.Key()
		if err != nil {
			return err
		}
		state, ok := projection.attempts[key]
		if !ok || state.Identity != identity {
			return ErrAttemptAuthorityUnknown
		}
		snapshot := existingWorktreeSnapshot(projection, state)
		if err := snapshot.Validate(); err != nil {
			return ErrExistingWorktreeAuthorityConflict
		}
		var intent allocationcontrol.ExistingWorktreeReleaseIntentV1
		for _, fact := range snapshot.Facts {
			if fact.AttemptKey != key {
				continue
			}
			switch fact.Kind {
			case allocationcontrol.ExistingWorktreeFactReleaseIntent:
				if found || strictExistingWorktreeDecode(fact.Payload, &intent) != nil || intent.Validate() != nil {
					return ErrExistingWorktreeAuthorityConflict
				}
				request, found = intent.Request, true
			case allocationcontrol.ExistingWorktreeFactReleaseReceipt:
				if complete || strictExistingWorktreeDecode(fact.Payload, &receipt) != nil {
					return ErrExistingWorktreeAuthorityConflict
				}
				complete = true
			}
		}
		if complete && (!found || receipt.Validate(intent) != nil || receipt.ReceiptDigest != state.ExistingWorktreeReleaseReceiptDigest) {
			return ErrExistingWorktreeAuthorityConflict
		}
		return nil
	})
	return request, receipt, found, complete, err
}

func validateExistingWorktreeFact(fact existingWorktreeAuthorityFact, in *Ingress) error {
	wantType, err := existingWorktreeFactType(fact.Kind)
	if err != nil || fact.ProtocolRevision != existingWorktreeAuthorityProtocolRevision || fact.FactType != wantType || fact.Sequence < 1 || requireDigest("attemptKey", fact.AttemptKey) != nil || requireDigest("previousAttemptHead", fact.PreviousAttemptHead) != nil || requireDigest("payloadDigest", fact.PayloadDigest) != nil || requireDigest("digest", fact.Digest) != nil || canonical.DigestBytes(fact.Payload) != fact.PayloadDigest {
		return ErrExistingWorktreeAuthorityConflict
	}
	attempt, ok := in.attempts[fact.AttemptKey]
	if !ok || fact.AttemptRevision != attempt.Revision+1 || fact.PreviousAttemptHead != attempt.HeadDigest {
		return ErrExistingWorktreeAuthorityConflict
	}
	predecessor, binding, err := existingWorktreePayloadAuthority(fact.Kind, fact.Payload)
	if err != nil || predecessor != fact.PreviousAttemptHead || !existingWorktreeBindingMatchesAttempt(binding, attempt) {
		return ErrExistingWorktreeAuthorityConflict
	}
	// Path B (existing-worktree bind) is mutually exclusive with path A
	// (legacy/provider AllocationProvision). A bind-intent or bind-receipt may
	// not be admitted to an Attempt that already carries an AllocationProvision
	// applied receipt; the reverse guard lives in validateIntentPhase.
	if fact.Kind == allocationcontrol.ExistingWorktreeFactBindIntent || fact.Kind == allocationcontrol.ExistingWorktreeFactBindReceipt {
		if attempt.AllocationProvisionEffectDigest != "" || attempt.AllocationProvisionReceiptDigest != "" {
			return ErrExistingWorktreeAuthorityConflict
		}
		if fact.Kind == allocationcontrol.ExistingWorktreeFactBindReceipt && attempt.ExistingWorktreeBindIntentFactDigest == "" {
			return ErrExistingWorktreeAuthorityConflict
		}
	}
	projected := allocationcontrol.ExistingWorktreeAttemptFactV1{AttemptKey: fact.AttemptKey, AttemptRevision: fact.AttemptRevision, Kind: fact.Kind, PreviousAttemptHeadDigest: fact.PreviousAttemptHead, Payload: fact.Payload, PayloadDigest: fact.PayloadDigest, AttemptFactDigest: fact.Digest}
	candidate := allocationcontrol.ExistingWorktreeAuthoritySnapshotV1{CurrentAttemptRevision: fact.AttemptRevision, CurrentAttemptHeadDigest: fact.Digest, Facts: append(append([]allocationcontrol.ExistingWorktreeAttemptFactV1(nil), in.existingWorktreeFacts...), projected)}
	if projected.Validate() != nil || candidate.Validate() != nil {
		return ErrExistingWorktreeAuthorityConflict
	}
	return nil
}
