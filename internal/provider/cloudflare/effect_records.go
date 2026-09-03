package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// This file implements the mandatory normalized side-effect authority seam of
// the Bridge provider (ADR 0019 §§123–125, as applied to the Cloudflare
// sandbox provider). Provision and Terminate are side-effecting remote
// mutations, so the provider never mutates the Bridge before a durable,
// put-if-absent SideEffectIntent is acknowledged, and it never fabricates a
// resolved receipt or observation for an outcome it did not observe. The
// provider's local allocation map is a cache only: the durable effect
// authority sink is the source of truth for what was intended and what was
// observed, and its Lookup/LookupByTarget surface is fail closed.

// Effect port and operation names of the Cloudflare effect seam.
const (
	EffectPortCloudflare     = "cloudflare"
	EffectOperationProvision = "provision"
	EffectOperationTerminate = "terminate"
)

var (
	// ErrEffectContextRequired rejects a NewProvider configuration that does
	// not carry the Core-injected effect context resolver.
	ErrEffectContextRequired = errors.New("cloudflare effect seam: the provider requires a Core-injected effect context resolver")
	// ErrEffectSinkRequired rejects a NewProvider configuration that does not
	// carry the Core-owned effect authority sink.
	ErrEffectSinkRequired = errors.New("cloudflare effect seam: the provider requires a Core-owned effect authority sink")
	// ErrEffectNotFound reports that no intent carries the requested effect
	// identity (effectId or targetRef+operation).
	ErrEffectNotFound = errors.New("cloudflare effect seam: effect not found")
	// ErrEffectIntentConflict reports a put-if-absent intent that collides
	// with a different intent for the same effect or target.
	ErrEffectIntentConflict = errors.New("cloudflare effect seam: conflicting effect intent")
	// ErrEffectReceiptMismatch reports a receipt or observation that does not
	// bind to the recomputed digest of its effect intent.
	ErrEffectReceiptMismatch = errors.New("cloudflare effect seam: receipt does not bind to the effect intent")
	// ErrEffectCrossEffect reports a receipt or observation whose authority
	// binding crosses effect boundaries.
	ErrEffectCrossEffect = errors.New("cloudflare effect seam: effect binding crosses effects")
	// ErrEffectRecordInvalid reports a record that fails closed validation.
	ErrEffectRecordInvalid = errors.New("cloudflare effect seam: invalid effect authority record")
	// ErrEffectSinkInvalid rejects a malformed sink record.
	ErrEffectSinkInvalid = errors.New("cloudflare effect seam: invalid effect sink record")
	// ErrEffectSinkInconsistent rejects an internally inconsistent sink.
	ErrEffectSinkInconsistent = errors.New("cloudflare effect seam: inconsistent effect sink")
	// ErrEffectAmbiguous reports a pending intent whose remote outcome is
	// unknown; the provider must never re-issue the mutation or self-sign a
	// verdict in this state.
	ErrEffectAmbiguous = errors.New("cloudflare effect seam: effect outcome is ambiguous")
)

// EffectContext is the frozen, Core-owned binding of one side effect. The
// resolver injects it; the provider never derives any authority field itself
// and never writes the transport credential, bridge locator or token into any
// digest, log, error or test output.
type EffectContext struct {
	RunId                string                         `json:"runId"`
	AttemptId            string                         `json:"attemptId"`
	EffectId             string                         `json:"effectId"`
	AuthorityNamespaceId authority.AuthorityNamespaceId `json:"authorityNamespaceId"`
	SecurityDomainId     authority.SecurityDomainId     `json:"securityDomainId"`
	PolicyDigest         string                         `json:"policyDigest"`
	AuthorizationDigest  string                         `json:"authorizationDigest"`
	Deadline             string                         `json:"deadline"`
}

// Validate fails closed on any missing or malformed frozen binding.
func (c EffectContext) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"effectContext.runId", c.RunId},
		{"effectContext.attemptId", c.AttemptId},
		{"effectContext.effectId", c.EffectId},
	} {
		if err := requireEffectText(field.name, field.value); err != nil {
			return err
		}
	}
	if err := c.AuthorityNamespaceId.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectRecordInvalid, err)
	}
	if err := c.SecurityDomainId.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectRecordInvalid, err)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"effectContext.policyDigest", c.PolicyDigest},
		{"effectContext.authorizationDigest", c.AuthorizationDigest},
	} {
		if err := requireEffectDigest(field.name, field.value); err != nil {
			return err
		}
	}
	if err := validateEffectDeadline("effectContext.deadline", c.Deadline); err != nil {
		return err
	}
	return nil
}

// bindTo enforces the exact run/attempt binding: the resolved context must
// belong to the exact (runId, attemptId) the operation identity carries.
func (c EffectContext) bindTo(identity sandbox.OperationIdentity) error {
	if c.RunId != identity.RunId || c.AttemptId != identity.AttemptId {
		return fmt.Errorf("%w: the effect context binds run %q attempt %q, not the identity's run %q attempt %q", ErrEffectCrossEffect, c.RunId, c.AttemptId, identity.RunId, identity.AttemptId)
	}
	return nil
}

// EffectContextResolver resolves the frozen effect binding Core owns. It is
// injected into NewProvider; production callers must supply it.
type EffectContextResolver interface {
	ResolveEffectContext(ctx context.Context, operation string, identity sandbox.OperationIdentity, allocationId string) (EffectContext, error)
}

// EffectObservation is the provider-authored observation bound to a resolved
// effect. It is an observation, never a verdict: the authority derives the
// reconcile decision out of band and the provider never fabricates one.
type EffectObservation struct {
	EffectId      string                `json:"effectId"`
	IntentDigest  string                `json:"intentDigest"`
	ReceiptDigest string                `json:"receiptDigest"`
	Observation   authority.Observation `json:"observation"`
}

// Validate fails closed on a missing effect id, a malformed digest binding or
// an unknown observation.
func (o EffectObservation) Validate() error {
	if err := requireEffectText("effectObservation.effectId", o.EffectId); err != nil {
		return err
	}
	if err := requireEffectDigest("effectObservation.intentDigest", o.IntentDigest); err != nil {
		return err
	}
	if err := requireEffectDigest("effectObservation.receiptDigest", o.ReceiptDigest); err != nil {
		return err
	}
	return o.Observation.Validate()
}

// EffectAuthorityRecord is the fail-closed view returned by Lookup and
// LookupByTarget: the intent, its resolved receipt (when present) and its
// bound observation (when present). It is validated as a whole so an
// erroneous record never hydrates into a caller-visible result.
type EffectAuthorityRecord struct {
	Intent      authority.SideEffectIntent
	Receipt     *authority.SideEffectReceipt
	Observation *EffectObservation
}

// Validate fails closed on the whole record: the intent must validate, the
// receipt (when present) must validate and bind to the recomputed intent
// digest and authority namespace, and the observation (when present) must
// validate and bind to the recomputed intent and receipt digests.
func (r EffectAuthorityRecord) Validate() error {
	if err := validateCloudflareIntent(r.Intent); err != nil {
		return fmt.Errorf("%w: intent: %v", ErrEffectRecordInvalid, err)
	}
	recomputed, err := r.Intent.Digest()
	if err != nil {
		return fmt.Errorf("%w: intent digest: %v", ErrEffectRecordInvalid, err)
	}
	if r.Receipt != nil {
		if err := r.Receipt.Validate(); err != nil {
			return fmt.Errorf("%w: receipt: %v", ErrEffectRecordInvalid, err)
		}
		if r.Receipt.IntentDigest != recomputed {
			return fmt.Errorf("%w: the receipt intent digest does not equal the recomputed intent digest", ErrEffectReceiptMismatch)
		}
		if !r.Receipt.AuthorityNamespaceId.Equal(r.Intent.AuthorityNamespaceId) {
			return fmt.Errorf("%w: the receipt authority namespace does not match the intent authority namespace", ErrEffectCrossEffect)
		}
		if r.Receipt.ProviderResourceIdentity != r.Intent.TargetRef {
			return fmt.Errorf("%w: the receipt provider resource identity %q does not match the intent target %q", ErrEffectCrossEffect, r.Receipt.ProviderResourceIdentity, r.Intent.TargetRef)
		}
	}
	if r.Observation != nil {
		if r.Receipt == nil {
			return fmt.Errorf("%w: an observation without a resolved receipt is an incomplete record", ErrEffectRecordInvalid)
		}
		if err := r.Observation.Validate(); err != nil {
			return fmt.Errorf("%w: observation: %v", ErrEffectRecordInvalid, err)
		}
		if r.Observation.EffectId != r.Intent.EffectId {
			return fmt.Errorf("%w: the observation effect id %q does not match the intent effect id %q", ErrEffectCrossEffect, r.Observation.EffectId, r.Intent.EffectId)
		}
		if r.Observation.IntentDigest != recomputed {
			return fmt.Errorf("%w: the observation intent digest does not equal the recomputed intent digest", ErrEffectReceiptMismatch)
		}
		receiptDigest, digestErr := r.Receipt.Digest()
		if digestErr != nil {
			return fmt.Errorf("%w: receipt digest: %v", ErrEffectRecordInvalid, digestErr)
		}
		if r.Observation.ReceiptDigest != receiptDigest {
			return fmt.Errorf("%w: the observation receipt digest does not equal the recomputed receipt digest", ErrEffectReceiptMismatch)
		}
	}
	return nil
}

// validateCloudflareIntent enforces the Cloudflare-specific binding of one
// intent beyond the authority-level validation: the port must be the
// Cloudflare port, the operation must be one of the two closed Cloudflare
// operations, the disposition class must map consistently to that operation,
// and the target digest must recompute from the target reference.
func validateCloudflareIntent(intent authority.SideEffectIntent) error {
	if err := intent.Validate(); err != nil {
		return err
	}
	if intent.Port != EffectPortCloudflare {
		return fmt.Errorf("%w: the intent port %q is not the cloudflare port %q", ErrEffectCrossEffect, intent.Port, EffectPortCloudflare)
	}
	dispositionClass, ok := effectOperationDispositionClass(intent.Operation)
	if !ok {
		return fmt.Errorf("%w: unknown cloudflare effect operation %q", ErrEffectRecordInvalid, intent.Operation)
	}
	if intent.DispositionClass != dispositionClass {
		return fmt.Errorf("%w: the intent disposition class %q does not map to the operation %q", ErrEffectCrossEffect, string(intent.DispositionClass), intent.Operation)
	}
	if intent.TargetDigest != sandbox.RecomputeSHA256([]byte(intent.TargetRef)) {
		return fmt.Errorf("%w: the intent target digest does not recompute from the target reference", ErrEffectRecordInvalid)
	}
	return nil
}

// effectOperationDispositionClass returns the disposition class a Cloudflare
// operation must carry, or false for an unknown operation.
func effectOperationDispositionClass(operation string) (authority.DispositionClass, bool) {
	switch operation {
	case EffectOperationProvision:
		return authority.DispositionClassSandboxProvision, true
	case EffectOperationTerminate:
		return authority.DispositionClassSandboxTerminate, true
	default:
		return "", false
	}
}

// EffectAuthoritySink is the Core-owned durable effect authority seam. It is
// injected into NewProvider; production callers must supply a durable
// (file-backed) sink, tests may supply the in-memory sink.
type EffectAuthoritySink interface {
	// PutIntent records one intent if absent; an existing intent for the same
	// effect id must be identical, otherwise it is a conflict. A non-empty
	// (targetRef, operation) pair is indexed for LookupByTarget.
	PutIntent(intent authority.SideEffectIntent) error
	// AppendReceipt binds one receipt to the intent whose recomputed digest
	// it carries, fail closed on any mismatch or cross-effect binding.
	AppendReceipt(receipt authority.SideEffectReceipt) error
	// BindObservation binds one observation to its resolved effect, fail
	// closed on any mismatch or cross-effect binding.
	BindObservation(observation EffectObservation) error
	// ResolveIntent atomically appends the receipt and binds the observation
	// in one failure-atomic mutation.
	ResolveIntent(receipt authority.SideEffectReceipt, observation EffectObservation) error
	// ClearIntent removes an intent after a definitive (non-ambiguous)
	// resolution, allowing a later attempt to record a fresh intent.
	ClearIntent(effectId string) error
	// Lookup returns the fail-closed authority record for one effect id.
	Lookup(effectId string) (EffectAuthorityRecord, error)
	// LookupByTarget returns the fail-closed authority record discovered by
	// the stable (targetRef, operation) pair.
	LookupByTarget(targetRef, operation string) (EffectAuthorityRecord, error)
	// PendingIntents returns every intent without a resolved receipt.
	PendingIntents() []authority.SideEffectIntent
}

// effectTargetKey derives the reverse-index key of one (targetRef, operation)
// pair.
func effectTargetKey(targetRef, operation string) string {
	return targetRef + "\x00" + operation
}

// effectSinkState is the serializable state of one effect sink.
type effectSinkState struct {
	Intents      map[string]authority.SideEffectIntent  `json:"intents"`
	Receipts     map[string]authority.SideEffectReceipt `json:"receipts"`
	Observations map[string]EffectObservation           `json:"observations"`
	Targets      map[string]string                      `json:"targets"`
}

func newEffectSinkState() *effectSinkState {
	return &effectSinkState{
		Intents:      map[string]authority.SideEffectIntent{},
		Receipts:     map[string]authority.SideEffectReceipt{},
		Observations: map[string]EffectObservation{},
		Targets:      map[string]string{},
	}
}

// clone returns a deep copy of the state; mutation runs on the copy only.
// All stored records are value types with no slice fields, so a shallow copy
// per map entry is a true deep copy.
func (state *effectSinkState) clone() *effectSinkState {
	out := newEffectSinkState()
	for key, intent := range state.Intents {
		out.Intents[key] = intent
	}
	for key, receipt := range state.Receipts {
		out.Receipts[key] = receipt
	}
	for key, observation := range state.Observations {
		out.Observations[key] = observation
	}
	for key, effectId := range state.Targets {
		out.Targets[key] = effectId
	}
	return out
}

// FileEffectSink is a file-durable, failure-atomic effect authority sink. The
// zero value is not usable; construct it with NewFileEffectSink (production)
// or newMemoryEffectSink (tests).
type FileEffectSink struct {
	mu    sync.Mutex
	path  string
	live  *effectSinkState
	write func([]byte) error
}

// NewFileEffectSink opens (or creates) the durable sink file at path and
// loads any previously persisted state. Every mutation is durably persisted
// through an atomic temp-file write plus fsync plus rename; a failed write
// leaves the in-memory state untouched.
func NewFileEffectSink(path string) (*FileEffectSink, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: the effect sink path must be a non-empty string", ErrEffectSinkInvalid)
	}
	sink := &FileEffectSink{path: path, live: newEffectSinkState()}
	sink.write = func(data []byte) error { return atomicWriteFile(path, data) }
	if err := sink.load(); err != nil {
		return nil, err
	}
	return sink, nil
}

// newMemoryEffectSink returns an ephemeral in-memory sink for tests. It
// shares the identical staged-copy mutation discipline but never touches the
// file system, so it is never durable and must not be described as such.
func newMemoryEffectSink() *FileEffectSink {
	sink := &FileEffectSink{live: newEffectSinkState()}
	sink.write = func([]byte) error { return nil }
	return sink
}

// load reads the persisted sink state from disk, if any. A missing file is an
// empty sink; a malformed file fails closed.
func (sink *FileEffectSink) load() error {
	data, err := os.ReadFile(sink.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: read: %v", ErrEffectSinkInvalid, err)
	}
	if len(data) == 0 {
		return nil
	}
	var state effectSinkState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrEffectSinkInvalid, err)
	}
	sink.live = normalizeEffectSinkState(&state)
	return nil
}

func normalizeEffectSinkState(state *effectSinkState) *effectSinkState {
	if state.Intents == nil {
		state.Intents = map[string]authority.SideEffectIntent{}
	}
	if state.Receipts == nil {
		state.Receipts = map[string]authority.SideEffectReceipt{}
	}
	if state.Observations == nil {
		state.Observations = map[string]EffectObservation{}
	}
	if state.Targets == nil {
		state.Targets = map[string]string{}
	}
	return state
}

// mutate applies one change to a staged deep copy and swaps it in only after
// the staged copy is durably persisted. Any failure — validation, encoding or
// persistence — leaves the live state untouched.
func (sink *FileEffectSink) mutate(change func(*effectSinkState) error) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	staged := sink.live.clone()
	if err := change(staged); err != nil {
		return err
	}
	data, err := json.Marshal(staged)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrEffectSinkInvalid, err)
	}
	if err := sink.write(data); err != nil {
		return err
	}
	sink.live = staged
	return nil
}

// matchIntent returns the effect id whose stored intent recomputes to the
// given digest. A digest that recomputes to no intent is a binding mismatch,
// never a silent miss.
func matchIntent(state *effectSinkState, intentDigest string) (string, authority.SideEffectIntent, error) {
	for effectId, intent := range state.Intents {
		recomputed, err := intent.Digest()
		if err != nil {
			return "", authority.SideEffectIntent{}, fmt.Errorf("%w: %v", ErrEffectRecordInvalid, err)
		}
		if recomputed == intentDigest {
			return effectId, intent, nil
		}
	}
	return "", authority.SideEffectIntent{}, fmt.Errorf("%w: no intent recomputes to digest %q", ErrEffectReceiptMismatch, intentDigest)
}

// appendReceiptLocked binds one receipt to its intent within a staged state.
func appendReceiptLocked(state *effectSinkState, receipt authority.SideEffectReceipt) error {
	effectId, intent, err := matchIntent(state, receipt.IntentDigest)
	if err != nil {
		return err
	}
	if err := validateCloudflareIntent(intent); err != nil {
		return fmt.Errorf("%w: the intent the receipt binds: %v", ErrEffectRecordInvalid, err)
	}
	if !receipt.AuthorityNamespaceId.Equal(intent.AuthorityNamespaceId) {
		return fmt.Errorf("%w: the receipt authority namespace does not match the intent authority namespace", ErrEffectCrossEffect)
	}
	if receipt.ProviderResourceIdentity != intent.TargetRef {
		return fmt.Errorf("%w: the receipt provider resource identity %q does not match the intent target %q", ErrEffectCrossEffect, receipt.ProviderResourceIdentity, intent.TargetRef)
	}
	if existing, ok := state.Receipts[effectId]; ok {
		if !existing.Equal(receipt) {
			return fmt.Errorf("%w: effect %q already carries a different receipt", ErrEffectIntentConflict, effectId)
		}
		return nil
	}
	state.Receipts[effectId] = receipt
	return nil
}

// bindObservationLocked binds one observation to its resolved effect within a
// staged state.
func bindObservationLocked(state *effectSinkState, observation EffectObservation) error {
	effectId, intent, err := matchIntent(state, observation.IntentDigest)
	if err != nil {
		return err
	}
	if err := validateCloudflareIntent(intent); err != nil {
		return fmt.Errorf("%w: the intent the observation binds: %v", ErrEffectRecordInvalid, err)
	}
	if observation.EffectId != effectId {
		return fmt.Errorf("%w: the observation effect id %q does not match the discovered effect %q", ErrEffectCrossEffect, observation.EffectId, effectId)
	}
	receipt, ok := state.Receipts[effectId]
	if !ok {
		return fmt.Errorf("%w: effect %q carries no resolved receipt, so an observation cannot bind", ErrEffectRecordInvalid, effectId)
	}
	receiptDigest, digestErr := receipt.Digest()
	if digestErr != nil {
		return fmt.Errorf("%w: receipt digest: %v", ErrEffectRecordInvalid, digestErr)
	}
	if observation.ReceiptDigest != receiptDigest {
		return fmt.Errorf("%w: the observation receipt digest does not equal the recomputed receipt digest", ErrEffectReceiptMismatch)
	}
	if existing, ok := state.Observations[effectId]; ok {
		if existing != observation {
			return fmt.Errorf("%w: effect %q already carries a different observation", ErrEffectIntentConflict, effectId)
		}
		return nil
	}
	state.Observations[effectId] = observation
	return nil
}

// PutIntent records one intent if absent.
func (sink *FileEffectSink) PutIntent(intent authority.SideEffectIntent) error {
	if err := validateCloudflareIntent(intent); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectSinkInvalid, err)
	}
	return sink.mutate(func(state *effectSinkState) error {
		key := intent.EffectId
		if existing, ok := state.Intents[key]; ok {
			if !existing.Equal(intent) {
				return fmt.Errorf("%w: effect %q already carries a different intent", ErrEffectIntentConflict, key)
			}
			return nil
		}
		if strings.TrimSpace(intent.TargetRef) != "" {
			targetKey := effectTargetKey(intent.TargetRef, intent.Operation)
			if otherEffect, ok := state.Targets[targetKey]; ok && otherEffect != key {
				return fmt.Errorf("%w: target %q/%q already belongs to effect %q", ErrEffectIntentConflict, intent.TargetRef, intent.Operation, otherEffect)
			}
			state.Targets[targetKey] = key
		}
		state.Intents[key] = intent
		return nil
	})
}

// AppendReceipt binds one receipt to its intent.
func (sink *FileEffectSink) AppendReceipt(receipt authority.SideEffectReceipt) error {
	if err := receipt.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectSinkInvalid, err)
	}
	return sink.mutate(func(state *effectSinkState) error {
		return appendReceiptLocked(state, receipt)
	})
}

// BindObservation binds one observation to its resolved effect.
func (sink *FileEffectSink) BindObservation(observation EffectObservation) error {
	if err := observation.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectSinkInvalid, err)
	}
	return sink.mutate(func(state *effectSinkState) error {
		return bindObservationLocked(state, observation)
	})
}

// ResolveIntent atomically appends the receipt and binds the observation in a
// single failure-atomic mutation.
func (sink *FileEffectSink) ResolveIntent(receipt authority.SideEffectReceipt, observation EffectObservation) error {
	if err := receipt.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectSinkInvalid, err)
	}
	if err := observation.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrEffectSinkInvalid, err)
	}
	return sink.mutate(func(state *effectSinkState) error {
		if err := appendReceiptLocked(state, receipt); err != nil {
			return err
		}
		return bindObservationLocked(state, observation)
	})
}

// ClearIntent removes an intent after a definitive resolution.
func (sink *FileEffectSink) ClearIntent(effectId string) error {
	if strings.TrimSpace(effectId) == "" {
		return fmt.Errorf("%w: the effect id must be a non-empty string", ErrEffectSinkInvalid)
	}
	return sink.mutate(func(state *effectSinkState) error {
		intent, ok := state.Intents[effectId]
		if !ok {
			return nil
		}
		delete(state.Intents, effectId)
		if strings.TrimSpace(intent.TargetRef) != "" {
			targetKey := effectTargetKey(intent.TargetRef, intent.Operation)
			if state.Targets[targetKey] == effectId {
				delete(state.Targets, targetKey)
			}
		}
		return nil
	})
}

// Lookup returns the fail-closed authority record for one effect id.
func (sink *FileEffectSink) Lookup(effectId string) (EffectAuthorityRecord, error) {
	if strings.TrimSpace(effectId) == "" {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: the effect id must be a non-empty string", ErrEffectSinkInvalid)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	record, err := sink.lookupLocked(sink.live, effectId)
	if err != nil {
		return EffectAuthorityRecord{}, err
	}
	if record.Intent.EffectId != effectId {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: the stored intent effect id %q does not match the requested %q", ErrEffectCrossEffect, record.Intent.EffectId, effectId)
	}
	return record, nil
}

// lookupLocked builds the record for one effect id from a given state.
func (sink *FileEffectSink) lookupLocked(state *effectSinkState, effectId string) (EffectAuthorityRecord, error) {
	intent, ok := state.Intents[effectId]
	if !ok {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: %q", ErrEffectNotFound, effectId)
	}
	record := EffectAuthorityRecord{Intent: intent}
	if receipt, ok := state.Receipts[effectId]; ok {
		record.Receipt = &receipt
	}
	if observation, ok := state.Observations[effectId]; ok {
		record.Observation = &observation
	}
	if err := record.Validate(); err != nil {
		return EffectAuthorityRecord{}, err
	}
	return record, nil
}

// LookupByTarget returns the fail-closed authority record discovered by the
// stable (targetRef, operation) pair.
func (sink *FileEffectSink) LookupByTarget(targetRef, operation string) (EffectAuthorityRecord, error) {
	if strings.TrimSpace(targetRef) == "" || strings.TrimSpace(operation) == "" {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: the target reference and operation must be non-empty strings", ErrEffectSinkInvalid)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	effectId, ok := sink.live.Targets[effectTargetKey(targetRef, operation)]
	if !ok {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: no effect targets %q/%q", ErrEffectNotFound, targetRef, operation)
	}
	record, err := sink.lookupLocked(sink.live, effectId)
	if err != nil {
		return EffectAuthorityRecord{}, err
	}
	if record.Intent.TargetRef != targetRef || record.Intent.Operation != operation {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: the stored intent targets %q/%q, not the requested %q/%q", ErrEffectCrossEffect, record.Intent.TargetRef, record.Intent.Operation, targetRef, operation)
	}
	return record, nil
}

// PendingIntents returns every intent without a resolved receipt.
func (sink *FileEffectSink) PendingIntents() []authority.SideEffectIntent {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	out := make([]authority.SideEffectIntent, 0, len(sink.live.Intents))
	for effectId, intent := range sink.live.Intents {
		if _, resolved := sink.live.Receipts[effectId]; resolved {
			continue
		}
		out = append(out, intent)
	}
	return out
}

// buildEffectIntent constructs one normalized SideEffectIntent from a frozen
// effect context, the dispatch identity, the target reference and the
// idempotency key. The exact binding (runId/attemptId/effectId/
// authorityNamespaceId/securityDomainId/policyDigest/authorizationDigest) is
// frozen by the context; the provider only adds its own port, operation,
// target and idempotency key.
func buildEffectIntent(effectCtx EffectContext, identity sandbox.OperationIdentity, operation, targetRef, idempotencyKey string) (authority.SideEffectIntent, error) {
	if err := effectCtx.Validate(); err != nil {
		return authority.SideEffectIntent{}, err
	}
	if err := effectCtx.bindTo(identity); err != nil {
		return authority.SideEffectIntent{}, err
	}
	if strings.TrimSpace(targetRef) == "" {
		return authority.SideEffectIntent{}, fmt.Errorf("%w: the target reference must be a non-empty string", ErrEffectRecordInvalid)
	}
	dispositionClass := authority.DispositionClassSandboxProvision
	switch operation {
	case EffectOperationProvision:
		dispositionClass = authority.DispositionClassSandboxProvision
	case EffectOperationTerminate:
		dispositionClass = authority.DispositionClassSandboxTerminate
	default:
		return authority.SideEffectIntent{}, fmt.Errorf("%w: unknown effect operation %q", ErrEffectRecordInvalid, operation)
	}
	intent := authority.SideEffectIntent{
		AuthorityNamespaceId: effectCtx.AuthorityNamespaceId,
		EffectId:             effectCtx.EffectId,
		OwnerIdentity:        identity.TaskId,
		Port:                 EffectPortCloudflare,
		Operation:            operation,
		TargetRef:            targetRef,
		TargetDigest:         sandbox.RecomputeSHA256([]byte(targetRef)),
		RequestDigest:        idempotencyKey,
		CommandId:            identity.CommandId,
		IdempotencyKey:       idempotencyKey,
		PolicyDigest:         effectCtx.PolicyDigest,
		AuthorizationDigest:  effectCtx.AuthorizationDigest,
		Purpose:              "cloudflare " + operation + " of allocation " + identity.AllocationId,
		DispositionClass:     dispositionClass,
		Deadline:             effectCtx.Deadline,
	}
	if err := intent.Validate(); err != nil {
		return authority.SideEffectIntent{}, fmt.Errorf("%w: %v", ErrEffectRecordInvalid, err)
	}
	return intent, nil
}

// buildEffectReceipt constructs one normalized SideEffectReceipt bound to the
// recomputed digest of its intent.
func buildEffectReceipt(intent authority.SideEffectIntent, effectCtx EffectContext, disposition authority.Disposition, providerResourceIdentity, observedDigest, reconcileIdentity string) (authority.SideEffectReceipt, error) {
	intentDigest, err := intent.Digest()
	if err != nil {
		return authority.SideEffectReceipt{}, err
	}
	receipt := authority.SideEffectReceipt{
		AuthorityNamespaceId:     intent.AuthorityNamespaceId,
		IntentDigest:             intentDigest,
		Disposition:              disposition,
		ProviderResourceIdentity: providerResourceIdentity,
		ObservedDigest:           observedDigest,
		ActorProvenance:          authority.ActorProvenance{SecurityDomainId: effectCtx.SecurityDomainId},
		ReconcileIdentity:        reconcileIdentity,
	}
	if err := receipt.Validate(); err != nil {
		return authority.SideEffectReceipt{}, fmt.Errorf("%w: %v", ErrEffectRecordInvalid, err)
	}
	return receipt, nil
}

// buildEffectObservation constructs one provider-authored observation bound to
// the recomputed digests of its intent and receipt.
func buildEffectObservation(intent authority.SideEffectIntent, receipt authority.SideEffectReceipt, observation authority.Observation) (EffectObservation, error) {
	intentDigest, err := intent.Digest()
	if err != nil {
		return EffectObservation{}, err
	}
	receiptDigest, err := receipt.Digest()
	if err != nil {
		return EffectObservation{}, err
	}
	result := EffectObservation{
		EffectId:      intent.EffectId,
		IntentDigest:  intentDigest,
		ReceiptDigest: receiptDigest,
		Observation:   observation,
	}
	if err := result.Validate(); err != nil {
		return EffectObservation{}, fmt.Errorf("%w: %v", ErrEffectRecordInvalid, err)
	}
	return result, nil
}

func requireEffectText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s must be a non-empty string", ErrEffectRecordInvalid, field)
	}
	return nil
}

func requireEffectDigest(field, value string) error {
	if !strings.HasPrefix(value, sandbox.DigestPrefix) || len(value) == len(sandbox.DigestPrefix) {
		return fmt.Errorf("%w: %s must be a non-empty %s digest", ErrEffectRecordInvalid, field, sandbox.DigestPrefix)
	}
	return nil
}

func validateEffectDeadline(field, value string) error {
	if err := requireEffectText(field, value); err != nil {
		return err
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return fmt.Errorf("%w: %s must be an RFC 3339 timestamp", ErrEffectRecordInvalid, field)
	}
	if parsed.IsZero() {
		return fmt.Errorf("%w: %s must not be the zero time", ErrEffectRecordInvalid, field)
	}
	return nil
}
