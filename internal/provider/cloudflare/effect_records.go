package cloudflare

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// This file freezes the normalized effect reconcile of the Cloudflare Bridge
// provider: the Provision and Terminate side effects are normalized onto the
// Core-internal authority records (authority.SideEffectIntent,
// authority.SideEffectReceipt, authority.ReconcileRecord) and cross-bound in
// one EffectAuthorityRecord whose Validate fails closed on any inconsistency.
// The record is durably persisted through the EffectAuthoritySink, and the
// durable phase keys are layered under the HTTP-safe external Idempotency-Key
// so the internal bookkeeping identity never travels on the wire.

// Sentinel errors of the normalized effect reconcile. Every failure wraps
// exactly one of them so fixtures can assert a fixed sentinel string.
var (
	// ErrEffectRecordInvalid rejects a cross-bound effect authority record
	// whose namespace, effect identity, scope, intent/receipt binding or
	// reconcile identity is inconsistent.
	ErrEffectRecordInvalid = errors.New("cloudflare effect authority: invalid record")
	// ErrEffectAuthoritySinkInvalid rejects a malformed effect authority sink.
	ErrEffectAuthoritySinkInvalid = errors.New("cloudflare effect authority: invalid sink")
	// ErrAuthorityContextUnresolved fails closed when the Core authority
	// context (namespace and provider actor provenance) cannot be resolved.
	ErrAuthorityContextUnresolved = errors.New("cloudflare effect authority: the Core authority context could not be resolved")
)

// effectRecordDeadline is the frozen deadline of every normalized effect
// intent. The intent records an already-applied observation, never an
// in-flight one, so the deadline is a fixed far-future constant that keeps
// the record structurally valid without depending on a construction clock.
const effectRecordDeadline = "2099-12-31T00:00:00Z"

// httpIdempotencyKey derives the external HTTP Idempotency-Key header value
// of one allocation-scoped phase. It is deliberately a different derivation
// from the internal durable phase key (the operation identity ReplayKey):
// the internal key is a full-identity canonical digest that keys the durable
// intent, while the external key is allocation-derived, HTTP-safe, stable
// and deterministic — a pure function of the allocation id and the phase.
// The two namespaces never coincide, so the internal bookkeeping identity
// never surfaces on the wire and the wire contract never constrains the
// internal key. base64url is HTTP-header safe and never carries padding.
func httpIdempotencyKey(allocationId, phase string) string {
	sum := sha256.Sum256([]byte("marshal-cloudflare-idempotency\x00" + allocationId + "\x00" + phase))
	return "marshal-" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// effectIdentity derives the normalized effect identity of one operation:
// deterministic, allocation-derived and generation-bound. The identical
// (operation, allocationId, generation) always derives the identical id.
func effectIdentity(operation, allocationId string, generation int64) string {
	return "marshal-effect:" + operation + ":" + allocationId + ":" + strconv.FormatInt(generation, 10)
}

// reconcileIdentity derives the same-scope reconcile identity: a one-way
// digest over the (runId, attemptId, generation) scope plus the effect
// identity. The identical scope and effect always derive the identical id;
// any drift in scope or effect derives a different id and fails closed.
func reconcileIdentity(runId, attemptId string, generation int64, effectId string) string {
	return canonical.DigestBytes([]byte("marshal-reconcile\x00" + runId + "\x00" + attemptId + "\x00" + strconv.FormatInt(generation, 10) + "\x00" + effectId))
}

// effectDispositionClass derives the frozen disposition class of one
// normalized effect operation: provision and terminate map to their closed
// authority classes.
func effectDispositionClass(operation string) authority.DispositionClass {
	if operation == sandbox.OperationTerminate {
		return authority.DispositionClassSandboxTerminate
	}
	return authority.DispositionClassSandboxProvision
}

// effectIdempotencyPhase derives the allocation-scoped idempotency phase of
// one normalized effect operation: provision uses the create phase and
// terminate uses the destroy phase.
func effectIdempotencyPhase(operation string) string {
	if operation == sandbox.OperationTerminate {
		return "destroy"
	}
	return "create"
}

// effectIdempotencyKey derives the layered external HTTP-safe idempotency
// key of one normalized effect: allocation-derived and phase-bound, so the
// intent idempotency key is a pure function of the effect identity.
func effectIdempotencyKey(allocationId, operation string) string {
	return httpIdempotencyKey(allocationId, effectIdempotencyPhase(operation))
}

// terminateIntentKey is the durable-phase key of a prepared terminate. It is
// namespaced under a distinct prefix so a terminate intent never collides
// with a create intent in the same durable intent map.
func terminateIntentKey(allocationId string, generation int64) string {
	return "terminate\x00" + allocationId + "\x00" + strconv.FormatInt(generation, 10)
}

// isTerminateIntent reports whether a durable intent is a prepared
// terminate rather than a create intent.
func isTerminateIntent(intent CreateIntent) bool {
	return strings.HasPrefix(intent.ReplayKey, "terminate\x00")
}

// AuthorityContext is the resolved Core authority context a normalized
// effect reconcile binds to: the Core authority namespace that owns every
// authority record and the Bridge provider actor security domain (provenance
// only, never an authority owner).
type AuthorityContext struct {
	Namespace              authority.AuthorityNamespaceId
	ProviderSecurityDomain authority.SecurityDomainId
}

// Validate fails closed on an invalid namespace or provider actor domain.
func (ctx AuthorityContext) Validate() error {
	if err := ctx.Namespace.Validate(); err != nil {
		return err
	}
	return ctx.ProviderSecurityDomain.Validate()
}

// CoreAuthorityResolver resolves the Core authority context at reconcile
// time. Production composition roots must bind the real Core context; a
// missing resolver fails closed, never an in-memory fallback.
type CoreAuthorityResolver interface {
	ResolveAuthorityContext() (AuthorityContext, error)
}

// CoreBackedAuthorityResolver is a CoreAuthorityResolver whose namespace is
// issued by the real Core typed-edge runtime, never a static identifier
// wrapper. The production composition root requires it and cross-checks the
// resolved namespace against the runtime issuer at construction, before any
// remote side effect can be issued.
type CoreBackedAuthorityResolver interface {
	CoreAuthorityResolver
	// CoreIssuer returns the Core typed-edge runtime issuer namespace the
	// resolved authority context must equal.
	CoreIssuer() authority.AuthorityNamespaceId
}

// EffectAuthorityRecord is the cross-bound authority record of one
// normalized Cloudflare side effect. It binds the normalized intent, receipt
// and reconcile records to the same (runId, attemptId, generation) scope and
// cross-validates the namespace, the effect identity and the same-scope
// derived reconcile identity. Any inconsistency fails closed.
type EffectAuthorityRecord struct {
	Namespace         authority.AuthorityNamespaceId `json:"namespace"`
	EffectId          string                         `json:"effectId"`
	Operation         string                         `json:"operation"`
	AllocationId      string                         `json:"allocationId"`
	RunId             string                         `json:"runId"`
	AttemptId         string                         `json:"attemptId"`
	Generation        int64                          `json:"generation"`
	Intent            authority.SideEffectIntent     `json:"intent"`
	Receipt           authority.SideEffectReceipt    `json:"receipt"`
	Reconcile         authority.ReconcileRecord      `json:"reconcile"`
	ReconcileIdentity string                         `json:"reconcileIdentity"`
}

// Validate cross-validates the record fail closed: the ownership namespace
// must be well-formed and identical across the intent, receipt, reconcile
// and the record itself; the scope fields must be non-empty with a positive
// generation; the effect identity must equal the derived (operation,
// allocationId, generation) identity; the reconcile identity must equal the
// same-scope-derived digest; and the intent/receipt digests must bind
// exactly to the reconcile record and to each other.
func (record EffectAuthorityRecord) Validate() error {
	if err := record.Namespace.Validate(); err != nil {
		return fmt.Errorf("%w: namespace: %v", ErrEffectRecordInvalid, err)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"effectId", record.EffectId},
		{"operation", record.Operation},
		{"allocationId", record.AllocationId},
		{"runId", record.RunId},
		{"attemptId", record.AttemptId},
		{"reconcileIdentity", record.ReconcileIdentity},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%w: %s must be a non-empty string", ErrEffectRecordInvalid, field.name)
		}
	}
	if record.Generation < 1 {
		return fmt.Errorf("%w: generation must be a positive integer", ErrEffectRecordInvalid)
	}
	if record.Operation != sandbox.OperationProvision && record.Operation != sandbox.OperationTerminate {
		return fmt.Errorf("%w: operation must be provision or terminate", ErrEffectRecordInvalid)
	}
	if err := record.Intent.Validate(); err != nil {
		return fmt.Errorf("%w: intent: %v", ErrEffectRecordInvalid, err)
	}
	if err := record.Receipt.Validate(); err != nil {
		return fmt.Errorf("%w: receipt: %v", ErrEffectRecordInvalid, err)
	}
	if err := record.Reconcile.Validate(); err != nil {
		return fmt.Errorf("%w: reconcile: %v", ErrEffectRecordInvalid, err)
	}
	if !record.Namespace.Equal(record.Intent.AuthorityNamespaceId) {
		return fmt.Errorf("%w: the intent namespace does not match the record namespace", ErrEffectRecordInvalid)
	}
	if !record.Namespace.Equal(record.Receipt.AuthorityNamespaceId) {
		return fmt.Errorf("%w: the receipt namespace does not match the record namespace", ErrEffectRecordInvalid)
	}
	if !record.Namespace.Equal(record.Reconcile.AuthorityNamespaceId) {
		return fmt.Errorf("%w: the reconcile namespace does not match the record namespace", ErrEffectRecordInvalid)
	}
	if record.EffectId != effectIdentity(record.Operation, record.AllocationId, record.Generation) {
		return fmt.Errorf("%w: the effect identity does not match the derived (operation, allocationId, generation) identity", ErrEffectRecordInvalid)
	}
	if record.Intent.EffectId != record.EffectId {
		return fmt.Errorf("%w: the intent effect identity does not match the record effect identity", ErrEffectRecordInvalid)
	}
	if record.Intent.Operation != record.Operation {
		return fmt.Errorf("%w: the intent operation does not match the record operation", ErrEffectRecordInvalid)
	}
	if record.Intent.TargetRef != record.AllocationId {
		return fmt.Errorf("%w: the intent target ref does not match the record allocation", ErrEffectRecordInvalid)
	}
	if record.Intent.TargetDigest != canonical.DigestBytes([]byte("target\x00"+record.AllocationId)) {
		return fmt.Errorf("%w: the intent target digest is not allocation-derived", ErrEffectRecordInvalid)
	}
	if record.Intent.IdempotencyKey != effectIdempotencyKey(record.AllocationId, record.Operation) {
		return fmt.Errorf("%w: the intent idempotency key is not the allocation/phase-derived key", ErrEffectRecordInvalid)
	}
	if record.Intent.DispositionClass != effectDispositionClass(record.Operation) {
		return fmt.Errorf("%w: the intent disposition class does not match the operation", ErrEffectRecordInvalid)
	}
	if record.Intent.PolicyDigest != canonical.DigestBytes([]byte("policy\x00"+record.RunId)) {
		return fmt.Errorf("%w: the intent policy digest is not run-derived", ErrEffectRecordInvalid)
	}
	if record.Intent.AuthorizationDigest != canonical.DigestBytes([]byte("authorization\x00"+record.AttemptId)) {
		return fmt.Errorf("%w: the intent authorization digest is not attempt-derived", ErrEffectRecordInvalid)
	}
	derivedReconcile := reconcileIdentity(record.RunId, record.AttemptId, record.Generation, record.EffectId)
	if record.ReconcileIdentity != derivedReconcile {
		return fmt.Errorf("%w: the reconcile identity does not match the same-scope-derived identity", ErrEffectRecordInvalid)
	}
	if record.Receipt.ReconcileIdentity != record.ReconcileIdentity {
		return fmt.Errorf("%w: the receipt reconcile identity does not match the record reconcile identity", ErrEffectRecordInvalid)
	}
	intentDigest, err := record.Intent.Digest()
	if err != nil {
		return fmt.Errorf("%w: intent digest: %v", ErrEffectRecordInvalid, err)
	}
	receiptDigest, err := record.Receipt.Digest()
	if err != nil {
		return fmt.Errorf("%w: receipt digest: %v", ErrEffectRecordInvalid, err)
	}
	if record.Reconcile.IntentDigest != intentDigest {
		return fmt.Errorf("%w: the reconcile intent digest does not match the intent", ErrEffectRecordInvalid)
	}
	if record.Reconcile.ReceiptDigest != receiptDigest {
		return fmt.Errorf("%w: the reconcile receipt digest does not match the receipt", ErrEffectRecordInvalid)
	}
	if record.Receipt.IntentDigest != intentDigest {
		return fmt.Errorf("%w: the receipt intent digest does not match the intent", ErrEffectRecordInvalid)
	}
	return nil
}

// Equal reports whether two records carry identical field values.
func (record EffectAuthorityRecord) Equal(other EffectAuthorityRecord) bool {
	return record == other
}

// NewEffectAuthorityRecord builds one cross-bound effect authority record
// from the Core authority context, the allocation identity and the effect
// observation inputs. It normalizes the intent, receipt and reconcile
// records, derives the effect identity, the same-scope reconcile identity
// and the allocation/phase-derived idempotency key, and fails closed unless
// the assembled record validates.
func NewEffectAuthorityRecord(ctx AuthorityContext, meta sandbox.SandboxAllocation, role sandbox.WorkloadRole, bridgeLocator, commandId, operation string) (EffectAuthorityRecord, error) {
	if err := ctx.Validate(); err != nil {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: %v", ErrEffectRecordInvalid, err)
	}
	effectId := effectIdentity(operation, meta.AllocationId, meta.Generation)
	reconcile := reconcileIdentity(meta.RunId, meta.AttemptId, meta.Generation, effectId)

	intent := authority.SideEffectIntent{
		AuthorityNamespaceId: ctx.Namespace,
		EffectId:             effectId,
		OwnerIdentity:        string(role) + ":" + meta.AllocationId,
		Port:                 "sandbox",
		Operation:            operation,
		TargetRef:            meta.AllocationId,
		TargetDigest:         canonical.DigestBytes([]byte("target\x00" + meta.AllocationId)),
		RequestDigest:        canonical.DigestBytes([]byte("request\x00" + operation + "\x00" + meta.AllocationId + "\x00" + commandId)),
		CommandId:            commandId,
		IdempotencyKey:       effectIdempotencyKey(meta.AllocationId, operation),
		PolicyDigest:         canonical.DigestBytes([]byte("policy\x00" + meta.RunId)),
		AuthorizationDigest:  canonical.DigestBytes([]byte("authorization\x00" + meta.AttemptId)),
		Purpose:              "bridge-sandbox-" + operation,
		DispositionClass:     effectDispositionClass(operation),
		Deadline:             effectRecordDeadline,
	}
	if err := intent.Validate(); err != nil {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: normalize intent: %v", ErrEffectRecordInvalid, err)
	}
	intentDigest, err := intent.Digest()
	if err != nil {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: intent digest: %v", ErrEffectRecordInvalid, err)
	}
	receipt := authority.SideEffectReceipt{
		AuthorityNamespaceId:     ctx.Namespace,
		IntentDigest:             intentDigest,
		Disposition:              authority.DispositionApplied,
		ProviderResourceIdentity: bridgeLocator,
		ObservedDigest:           canonical.DigestBytes([]byte("observed\x00" + bridgeLocator)),
		ActorProvenance:          authority.ActorProvenance{SecurityDomainId: ctx.ProviderSecurityDomain},
		ReconcileIdentity:        reconcile,
	}
	if err := receipt.Validate(); err != nil {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: normalize receipt: %v", ErrEffectRecordInvalid, err)
	}
	receiptDigest, err := receipt.Digest()
	if err != nil {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: receipt digest: %v", ErrEffectRecordInvalid, err)
	}
	reconcileRecord := authority.ReconcileRecord{
		AuthorityNamespaceId: ctx.Namespace,
		Observation:          authority.ObservationApplied,
		Decision:             authority.DecisionAccept,
		IntentDigest:         intentDigest,
		ReceiptDigest:        receiptDigest,
	}
	if err := reconcileRecord.Validate(); err != nil {
		return EffectAuthorityRecord{}, fmt.Errorf("%w: normalize reconcile: %v", ErrEffectRecordInvalid, err)
	}

	record := EffectAuthorityRecord{
		Namespace:         ctx.Namespace,
		EffectId:          effectId,
		Operation:         operation,
		AllocationId:      meta.AllocationId,
		RunId:             meta.RunId,
		AttemptId:         meta.AttemptId,
		Generation:        meta.Generation,
		Intent:            intent,
		Receipt:           receipt,
		Reconcile:         reconcileRecord,
		ReconcileIdentity: reconcile,
	}
	if err := record.Validate(); err != nil {
		return EffectAuthorityRecord{}, err
	}
	return record, nil
}

// EffectAuthoritySink is the durable sink of cross-bound effect authority
// records. Production composition roots must bind a durable (file-backed)
// sink; persisting the identical effect id again is idempotent and rejects
// a divergent record under the same effect id fail closed.
type EffectAuthoritySink interface {
	SinkID() string
	PersistEffectAuthority(record EffectAuthorityRecord) error
}

// FileEffectAuthoritySink is the file-backed durable effect authority sink.
// Every persist is an atomic temp-file write plus fsync plus rename, so a
// failed write never leaves a partially-written sink behind, and a reopened
// sink validates and converges on the previously persisted records.
type FileEffectAuthoritySink struct {
	mu      sync.Mutex
	path    string
	records []EffectAuthorityRecord
	write   func([]byte) error
}

// NewFileEffectAuthoritySink opens (or creates) the durable effect authority
// sink at path and loads any previously persisted records. This is the
// production constructor; a malformed, tampered, truncated or forked file
// fails closed.
func NewFileEffectAuthoritySink(path string) (*FileEffectAuthoritySink, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: the effect authority sink path must be a non-empty string", ErrEffectAuthoritySinkInvalid)
	}
	sink := &FileEffectAuthoritySink{path: path}
	sink.write = func(data []byte) error { return atomicWriteFile(path, data) }
	if err := sink.load(); err != nil {
		return nil, err
	}
	return sink, nil
}

// SinkID returns the stable identity of this sink.
func (sink *FileEffectAuthoritySink) SinkID() string {
	return "cloudflare-effect-authority:" + sink.path
}

// PersistEffectAuthority durably appends one validated cross-bound record.
// Re-persisting the identical effect id is idempotent; a divergent record
// under an already-persisted effect id fails closed.
func (sink *FileEffectAuthoritySink) PersistEffectAuthority(record EffectAuthorityRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, existing := range sink.records {
		if existing.EffectId != record.EffectId {
			continue
		}
		if !existing.Equal(record) {
			return fmt.Errorf("%w: effect id %q already carries a different record", ErrEffectRecordInvalid, record.EffectId)
		}
		return nil
	}
	records := append(append([]EffectAuthorityRecord(nil), sink.records...), record)
	if err := sink.persist(records); err != nil {
		return err
	}
	sink.records = records
	return nil
}

// Records returns a copy of every persisted effect authority record.
func (sink *FileEffectAuthoritySink) Records() []EffectAuthorityRecord {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]EffectAuthorityRecord(nil), sink.records...)
}

func (sink *FileEffectAuthoritySink) load() error {
	data, err := os.ReadFile(sink.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: read: %v", ErrEffectAuthoritySinkInvalid, err)
	}
	if len(data) == 0 {
		return nil
	}
	var records []EffectAuthorityRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrEffectAuthoritySinkInvalid, err)
	}
	seen := make(map[string]EffectAuthorityRecord, len(records))
	deduped := make([]EffectAuthorityRecord, 0, len(records))
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return fmt.Errorf("%w: persisted record %q: %v", ErrEffectAuthoritySinkInvalid, record.EffectId, err)
		}
		if existing, ok := seen[record.EffectId]; ok {
			if !existing.Equal(record) {
				return fmt.Errorf("%w: effect id %q appears more than once with divergent records", ErrEffectAuthoritySinkInvalid, record.EffectId)
			}
			continue
		}
		seen[record.EffectId] = record
		deduped = append(deduped, record)
	}
	sink.records = deduped
	return nil
}

func (sink *FileEffectAuthoritySink) persist(records []EffectAuthorityRecord) error {
	data, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrEffectAuthoritySinkInvalid, err)
	}
	return sink.write(data)
}

// isFileBacked reports whether the store is a durable file-backed store
// rather than the ephemeral in-memory test store. The in-memory store is
// constructed without a path, so an empty path is the sole discriminator.
func (store *FileStateStore) isFileBacked() bool {
	return store != nil && store.path != ""
}

// CommitTerminateOutcome atomically installs the terminal allocation record
// and resolves the matching prepared terminate intent in one failure-atomic
// mutation. A failed write therefore never leaves a half-applied terminate.
func (store *FileStateStore) CommitTerminateOutcome(terminateKey string, record AllocationRecord) error {
	if strings.TrimSpace(terminateKey) == "" || strings.TrimSpace(record.Meta.AllocationId) == "" {
		return fmt.Errorf("%w: the terminate outcome key and allocation id must be non-empty strings", ErrStateStoreInvalid)
	}
	if err := record.Meta.Validate(); err != nil {
		return fmt.Errorf("%w: terminate outcome allocation: %v", ErrStateStoreInvalid, err)
	}
	if err := record.Role.Validate(); err != nil {
		return fmt.Errorf("%w: terminate outcome role: %v", ErrStateStoreInvalid, err)
	}
	return store.mutate(func(state *storeState) error {
		if intent, ok := state.Intents[terminateKey]; ok && intent.AllocationId != record.Meta.AllocationId {
			return fmt.Errorf("%w: terminate outcome allocation %q disagrees with the intent allocation %q", ErrStateStoreInconsistent, record.Meta.AllocationId, intent.AllocationId)
		}
		delete(state.Intents, terminateKey)
		state.Allocations[record.Meta.AllocationId] = record.clone()
		return nil
	})
}

// recordEffect durably records the normalized effect reconcile of one
// completed Provision or Terminate through the bound effect authority sink.
// It is a no-op when no sink is bound (the ephemeral test provider); when a
// sink is bound it records the effect under the authority context already
// resolved at construction, so a resolution failure can never surface after
// a remote side effect. The caller must hold p.mu.
func (p *Provider) recordEffect(meta sandbox.SandboxAllocation, role sandbox.WorkloadRole, bridgeLocator, commandId, operation string) error {
	if p.effectSink == nil {
		return nil
	}
	record, err := NewEffectAuthorityRecord(p.authorityContext, meta, role, bridgeLocator, commandId, operation)
	if err != nil {
		return err
	}
	return p.effectSink.PersistEffectAuthority(record)
}
