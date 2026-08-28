package resultingress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

// ── Sentinel errors (typed, fail-closed) ─────────────────────────────────────

var (
	ErrDigestMismatch    = errors.New("resultingress: digest mismatch")
	ErrDRCRevoked        = errors.New("resultingress: DRC revoked")
	ErrStaleGeneration   = errors.New("resultingress: stale generation")
	ErrStaleLease        = errors.New("resultingress: stale lease")
	ErrMalformedEnvelope = errors.New("resultingress: malformed envelope")
	ErrMalformedDRC      = errors.New("resultingress: malformed DRC")
)

// ── Closed enumerations ───────────────────────────────────────────────────────

// EnvelopeKind is the closed set of envelope kinds accepted by ResultIngress.
type EnvelopeKind string

const (
	KindWorkerResult EnvelopeKind = "worker-result"
	KindCandidate    EnvelopeKind = "candidate"
	KindEvidenceRef  EnvelopeKind = "evidence-ref"
	KindCheckpoint   EnvelopeKind = "checkpoint"
	KindHeartbeat    EnvelopeKind = "heartbeat"
	KindReceipt      EnvelopeKind = "receipt"
	KindLog          EnvelopeKind = "log"
	KindAssessment   EnvelopeKind = "assessment"
)

// RejectionReason is the closed set of quarantine rejection labels.
type RejectionReason string

const (
	ReasonDigestMismatch         RejectionReason = "digest-mismatch"
	ReasonRevoked                RejectionReason = "revoked"
	ReasonStaleGeneration        RejectionReason = "stale-generation"
	ReasonStaleLease             RejectionReason = "stale-lease"
	ReasonMalformed              RejectionReason = "malformed"
	ReasonExpired                RejectionReason = "expired"
	ReasonUnknownKind            RejectionReason = "unknown-kind"
	ReasonOperationMismatch      RejectionReason = "operation-mismatch"
	ReasonIneligibleRegistration RejectionReason = "ineligible-registration"
	ReasonIneligibleSnapshot     RejectionReason = "ineligible-snapshot"
	ReasonIneligibleEvidence     RejectionReason = "ineligible-evidence"
)

// ── DRC ───────────────────────────────────────────────────────────────────────

// DRC is a DispatchResultCapability (ADR 0018/0044).
// Issuer is always Core; every field is required and validated fail-closed.
// Digest() is based on canonical JSON for stable identity.
type DRC struct {
	AuthorityNamespaceID string
	TaskID               string
	RunID                string
	AttemptID            string
	AllocationID         string
	LeaseID              string
	Generation           uint64
	FencingToken         string
	CommandID            string
	IdempotencyKey       string
	RequestDigest        string // sha256:<64-hex>
	Nonce                string
	Expiry               time.Time
	Operation            Operation // ADR 0018 frozen closed enum
	RegistrationID       string
	SnapshotDigest       string // sha256:<64-hex>
	EvidenceDigest       string // sha256:<64-hex>
}

// Validate checks all fields fail-closed.
func (d DRC) Validate() error {
	if strings.TrimSpace(d.AuthorityNamespaceID) == "" {
		return fmt.Errorf("%w: AuthorityNamespaceID empty", ErrMalformedDRC)
	}
	if strings.TrimSpace(d.TaskID) == "" {
		return fmt.Errorf("%w: TaskID empty", ErrMalformedDRC)
	}
	if strings.TrimSpace(d.RunID) == "" {
		return fmt.Errorf("%w: RunID empty", ErrMalformedDRC)
	}
	if strings.TrimSpace(d.AttemptID) == "" {
		return fmt.Errorf("%w: AttemptID empty", ErrMalformedDRC)
	}
	if strings.TrimSpace(d.AllocationID) == "" {
		return fmt.Errorf("%w: AllocationID empty", ErrMalformedDRC)
	}
	if strings.TrimSpace(d.LeaseID) == "" {
		return fmt.Errorf("%w: LeaseID empty", ErrMalformedDRC)
	}
	if strings.TrimSpace(d.FencingToken) == "" {
		return fmt.Errorf("%w: FencingToken empty", ErrMalformedDRC)
	}
	if strings.TrimSpace(d.CommandID) == "" {
		return fmt.Errorf("%w: CommandID empty", ErrMalformedDRC)
	}
	if strings.TrimSpace(d.IdempotencyKey) == "" {
		return fmt.Errorf("%w: IdempotencyKey empty", ErrMalformedDRC)
	}
	if strings.TrimSpace(d.Nonce) == "" {
		return fmt.Errorf("%w: Nonce empty", ErrMalformedDRC)
	}
	if err := requireDigest("RequestDigest", d.RequestDigest); err != nil {
		return fmt.Errorf("%w: RequestDigest: %v", ErrMalformedDRC, err)
	}
	if d.Expiry.IsZero() {
		return fmt.Errorf("%w: Expiry is zero", ErrMalformedDRC)
	}
	if string(d.Operation) == "" {
		return fmt.Errorf("%w: Operation empty", ErrMalformedDRC)
	}
	if !isValidOperation(d.Operation) {
		return fmt.Errorf("%w: Operation %q not in closed set", ErrMalformedDRC, d.Operation)
	}
	if strings.TrimSpace(d.RegistrationID) == "" {
		return fmt.Errorf("%w: RegistrationID empty", ErrMalformedDRC)
	}
	if err := requireDigest("SnapshotDigest", d.SnapshotDigest); err != nil {
		return fmt.Errorf("%w: SnapshotDigest: %v", ErrMalformedDRC, err)
	}
	if err := requireDigest("EvidenceDigest", d.EvidenceDigest); err != nil {
		return fmt.Errorf("%w: EvidenceDigest: %v", ErrMalformedDRC, err)
	}
	return nil
}

// drcJSON is the canonical serialisation shape for Digest().
type drcJSON struct {
	AuthorityNamespaceID string `json:"authorityNamespaceId"`
	TaskID               string `json:"taskId"`
	RunID                string `json:"runId"`
	AttemptID            string `json:"attemptId"`
	AllocationID         string `json:"allocationId"`
	LeaseID              string `json:"leaseId"`
	Generation           uint64 `json:"generation"`
	FencingToken         string `json:"fencingToken"`
	CommandID            string `json:"commandId"`
	IdempotencyKey       string `json:"idempotencyKey"`
	RequestDigest        string `json:"requestDigest"`
	Nonce                string `json:"nonce"`
	ExpiryUnixSec        int64  `json:"expiryUnixSec"`
	Operation            string `json:"operation"`
	RegistrationID       string `json:"registrationId"`
	SnapshotDigest       string `json:"snapshotDigest"`
	EvidenceDigest       string `json:"evidenceDigest"`
}

// Digest returns the sha256 digest of the canonical JSON form of the DRC.
// It requires Validate() to have passed; returns error on serialisation failure.
func (d DRC) Digest() (string, error) {
	raw, err := json.Marshal(drcJSON{
		AuthorityNamespaceID: d.AuthorityNamespaceID,
		TaskID:               d.TaskID,
		RunID:                d.RunID,
		AttemptID:            d.AttemptID,
		AllocationID:         d.AllocationID,
		LeaseID:              d.LeaseID,
		Generation:           d.Generation,
		FencingToken:         d.FencingToken,
		CommandID:            d.CommandID,
		IdempotencyKey:       d.IdempotencyKey,
		RequestDigest:        d.RequestDigest,
		Nonce:                d.Nonce,
		ExpiryUnixSec:        d.Expiry.Unix(),
		Operation:            string(d.Operation),
		RegistrationID:       d.RegistrationID,
		SnapshotDigest:       d.SnapshotDigest,
		EvidenceDigest:       d.EvidenceDigest,
	})
	if err != nil {
		return "", fmt.Errorf("resultingress: DRC serialisation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// ── LedgerBinding ─────────────────────────────────────────────────────────────

// LedgerBinding is a fake current-ledger view for this walking skeleton.
// It represents the authority ledger's current knowledge for a given attempt.
type LedgerBinding struct {
	LeaseID        string
	Generation     uint64
	FencingToken   string
	AttemptID      string
	AllocationID   string
	Expiry         time.Time
	Revoked        bool
	RegistrationID string
	SnapshotDigest string // sha256:<64-hex>
	EvidenceDigest string // sha256:<64-hex>
}

// ── ResultEnvelope ─────────────────────────────────────────────────────────────

// ResultEnvelope is the delivery container for an external result.
type ResultEnvelope struct {
	Kind         EnvelopeKind
	ResultDigest string // sha256:<64-hex> over the payload
	Sequence     uint64
}

// Validate checks all fields fail-closed.
func (e ResultEnvelope) Validate() error {
	if _, ok := kindToOperation(e.Kind); !ok {
		return fmt.Errorf("%w: unknown kind %q", ErrUnknownKind, e.Kind)
	}
	if err := requireDigest("ResultDigest", e.ResultDigest); err != nil {
		return fmt.Errorf("%w: ResultDigest: %v", ErrMalformedEnvelope, err)
	}
	if e.Sequence == 0 {
		return fmt.Errorf("%w: Sequence must be > 0", ErrMalformedEnvelope)
	}
	return nil
}

// ── AdmissionFact ─────────────────────────────────────────────────────────────

// AdmissionFact is the ledger fact produced on successful admission.
// It carries no trusted/verified semantic fields; it only proves source
// and authorisation were checked at admission time.
type AdmissionFact struct {
	FactDigest       string
	LedgerSequence   uint64
	IdempotentReplay bool
}

// ── QuarantineRecord ──────────────────────────────────────────────────────────

// QuarantineRecord captures a rejected delivery for read-only mechanical audit.
// Quarantine records do not participate in business derivation.
type QuarantineRecord struct {
	Reason         RejectionReason
	DRCDigest      string
	EnvelopeDigest string
	ObservedAt     time.Time
}

// ── Ingress ───────────────────────────────────────────────────────────────────

// admittedEntry bundles the recorded AdmissionFact with the original envelope
// digest so that replay detection can compare the incoming digest correctly.
type admittedEntry struct {
	fact                AdmissionFact
	attemptKey          string
	drcDigest           string
	envelopeKind        EnvelopeKind
	envelopeSequence    uint64
	envelopeDigest      string
	legacyReplayBlocked bool
}

// Ingress is the single admission gate for external results (ADR 0044 decision 1).
// Constructed via NewIngress; zero-value is not valid.
type Ingress struct {
	mu             sync.Mutex
	ledger         LedgerBinding
	ledgerSequence uint64
	// admitted maps idempotencyKey → admittedEntry for replay detection.
	admitted map[string]admittedEntry
	attempts map[string]AttemptAuthorityState
	// controlOwners is the repository/authority-scope owner projection rebuilt
	// from control-owner-acquired facts in this same physical ledger. It is not
	// a second lifecycle ledger and never authorizes an Attempt without an exact
	// per-Attempt owner binding fact.
	controlOwners map[string]ControlOwnerState
	effects       map[string]EffectAuthorityState
	// allocations is rebuilt exclusively from the same durable Attempt log.
	// It is the five-fact authority source projected into allocationcontrol;
	// the Provider journal is never allowed to populate this map.
	allocations map[string]allocationAuthorityState
	// The three indexes are authority-namespace scoped. Command/idempotency map
	// to an effect key; marker maps to its immutable logical Attempt key. They
	// are rebuilt exclusively from the authority log on every transaction.
	effectCommands    map[string]string
	effectIdempotency map[string]string
	effectMarkers     map[string]string
	quarantine        []QuarantineRecord
	// clock allows deterministic testing without real time reads.
	clock func() time.Time
	// store is the optional durable replay/quarantine/idempotency append-only
	// ledger（R2 纵切）. 非空时 Admit 成功与每次 quarantine 都先落账，使跨进程
	// 重放/重复送达可被机械检测；nil 时为纯内存（与以往行为、既有测试一致）。
	store *ingressDurableStore
}

// NewIngress creates an Ingress backed by the provided fake ledger binding.
func NewIngress(binding LedgerBinding) (*Ingress, error) {
	if strings.TrimSpace(binding.LeaseID) == "" {
		return nil, errors.New("resultingress: LedgerBinding.LeaseID must not be empty")
	}
	if strings.TrimSpace(binding.AttemptID) == "" {
		return nil, errors.New("resultingress: LedgerBinding.AttemptID must not be empty")
	}
	if strings.TrimSpace(binding.AllocationID) == "" {
		return nil, errors.New("resultingress: LedgerBinding.AllocationID must not be empty")
	}
	if strings.TrimSpace(binding.FencingToken) == "" {
		return nil, errors.New("resultingress: LedgerBinding.FencingToken must not be empty")
	}
	if strings.TrimSpace(binding.RegistrationID) == "" {
		return nil, errors.New("resultingress: LedgerBinding.RegistrationID must not be empty")
	}
	if err := requireDigest("SnapshotDigest", binding.SnapshotDigest); err != nil {
		return nil, fmt.Errorf("resultingress: LedgerBinding.SnapshotDigest: %v", err)
	}
	if err := requireDigest("EvidenceDigest", binding.EvidenceDigest); err != nil {
		return nil, fmt.Errorf("resultingress: LedgerBinding.EvidenceDigest: %v", err)
	}
	return &Ingress{
		ledger:            binding,
		admitted:          make(map[string]admittedEntry),
		attempts:          make(map[string]AttemptAuthorityState),
		controlOwners:     make(map[string]ControlOwnerState),
		effects:           make(map[string]EffectAuthorityState),
		allocations:       make(map[string]allocationAuthorityState),
		effectCommands:    make(map[string]string),
		effectIdempotency: make(map[string]string),
		effectMarkers:     make(map[string]string),
		clock:             time.Now,
	}, nil
}

// NewDurableIngress 创建一个由耐久 replay/quarantine/idempotency 账本（R2
// 纵切）支撑的 Ingress：先用 NewIngress 的 exact binding 校验门禁，再从
// store 确定性重放已采用的 admitted map / ledgerSequence / quarantine，
// 使跨进程重放或重复送达被机械检测。账本创建/恢复失败一律 fail closed。
// binding 校验与 NewIngress 完全一致。
func NewDurableIngress(binding LedgerBinding, store *ingressDurableStore) (*Ingress, error) {
	in := &Ingress{
		ledger:            binding,
		admitted:          make(map[string]admittedEntry),
		attempts:          make(map[string]AttemptAuthorityState),
		controlOwners:     make(map[string]ControlOwnerState),
		effects:           make(map[string]EffectAuthorityState),
		effectCommands:    make(map[string]string),
		effectIdempotency: make(map[string]string),
		effectMarkers:     make(map[string]string),
		clock:             time.Now,
		store:             store,
	}
	if store == nil {
		return nil, errors.New("resultingress: durable ingress requires a non-nil store")
	}
	if store.requireBound() != nil {
		return nil, errors.New("resultingress: durable ingress store must be bound to a durable directory")
	}
	if binding.LeaseID == "" || binding.AttemptID == "" || binding.AllocationID == "" || binding.FencingToken == "" || binding.RegistrationID == "" {
		return nil, errors.New("resultingress: LedgerBinding identity fields must not be empty")
	}
	if err := requireDigest("SnapshotDigest", binding.SnapshotDigest); err != nil {
		return nil, fmt.Errorf("resultingress: LedgerBinding.SnapshotDigest: %v", err)
	}
	if err := requireDigest("EvidenceDigest", binding.EvidenceDigest); err != nil {
		return nil, fmt.Errorf("resultingress: LedgerBinding.EvidenceDigest: %v", err)
	}
	if err := store.recoverInto(in); err != nil {
		return nil, err
	}
	return in, nil
}

// Admit checks the DRC against the current ledger binding and, if valid,
// records the envelope as a ledger fact. Admission only proves source and
// authorisation are legal; it does not verify content correctness.
//
// Idempotent replay: if a delivery with the same idempotencyKey and matching
// resultDigest has already been admitted, the existing AdmissionFact is
// returned with IdempotentReplay=true and the ledger sequence does not advance.
//
// All rejection paths fail closed and write a QuarantineRecord.
func (i *Ingress) Admit(ctx context.Context, drc DRC, envelope ResultEnvelope) (AdmissionFact, error) {
	return i.admitWithSupervisorCollect(ctx, drc, envelope, SupervisorCommandEvidence{}, "")
}

// AdmitWithSupervisorCollect preserves source compatibility for historical
// embedded-evidence replay. A fresh bootstrap-prepared Attempt rejects this
// path and must cite an independently durable outcome fact instead.
func (i *Ingress) AdmitWithSupervisorCollect(ctx context.Context, drc DRC, envelope ResultEnvelope, collect SupervisorCommandEvidence) (AdmissionFact, error) {
	return i.admitWithSupervisorCollect(ctx, drc, envelope, collect, "")
}

// AdmitWithSupervisorCollectOutcome co-commits one WorkerResult with an exact
// already-durable collect outcome checkpoint. It does not extract transcript
// payloads; production composition must obtain the VerifiedCommandOutcome
// from processsupervisor.Client and checkpoint it first.
func (i *Ingress) AdmitWithSupervisorCollectOutcome(ctx context.Context, drc DRC, envelope ResultEnvelope, outcomeFactDigest string) (AdmissionFact, error) {
	return i.admitWithSupervisorCollect(ctx, drc, envelope, SupervisorCommandEvidence{}, outcomeFactDigest)
}

func (i *Ingress) admitWithSupervisorCollect(ctx context.Context, drc DRC, envelope ResultEnvelope, collect SupervisorCommandEvidence, outcomeFactDigest string) (AdmissionFact, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.store != nil {
		var fact AdmissionFact
		var admitErr error
		if err := i.store.transact(i, func() error {
			fact, admitErr = i.admitLocked(ctx, drc, envelope, collect, outcomeFactDigest)
			return nil
		}); err != nil {
			return AdmissionFact{}, err
		}
		return fact, admitErr
	}
	return i.admitLocked(ctx, drc, envelope, collect, outcomeFactDigest)
}

func (i *Ingress) admitLocked(_ context.Context, drc DRC, envelope ResultEnvelope, collect SupervisorCommandEvidence, outcomeFactDigest string) (AdmissionFact, error) {
	now := i.clock()

	// ── 1. Structural validation (fail closed for malformed input) ────────────
	if err := drc.Validate(); err != nil {
		i.recordQuarantine(ReasonMalformed, "", envelope.ResultDigest, now)
		return AdmissionFact{}, err
	}
	if err := envelope.Validate(); err != nil {
		drcDigest, _ := drc.Digest()
		if errors.Is(err, ErrUnknownKind) {
			i.recordQuarantine(ReasonUnknownKind, drcDigest, "", now)
		} else {
			i.recordQuarantine(ReasonMalformed, drcDigest, "", now)
		}
		return AdmissionFact{}, err
	}

	drcDigest, err := drc.Digest()
	if err != nil {
		i.recordQuarantine(ReasonMalformed, "", envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("resultingress: DRC digest failed: %w", err)
	}
	authorityState, authorityKey, governed, authorityConflict := i.currentAttemptForDRC(drc)
	if authorityConflict {
		i.recordQuarantine(ReasonStaleLease, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: DRC tuple conflicts with durable Attempt authority", ErrStaleLease)
	}

	// A committed delivery is an immutable effect. Exact replay is checked
	// before current lease/registration freshness because restart recovery may
	// happen after the dispatch lease expires. The stored DRC digest prevents a
	// differently bound credential from claiming an old effect.
	replayKey := drc.IdempotencyKey
	if prior, ok := i.admitted[replayKey]; ok {
		if prior.legacyReplayBlocked {
			i.recordQuarantine(ReasonDigestMismatch, drcDigest, envelope.ResultDigest, now)
			return AdmissionFact{}, fmt.Errorf("%w: idempotency key %q belongs to an unversioned legacy admission whose missing DRC/envelope authority cannot be synthesized",
				ErrDigestMismatch, replayKey)
		}
		if prior.drcDigest == drcDigest && prior.envelopeKind == envelope.Kind && prior.envelopeSequence == envelope.Sequence && prior.envelopeDigest == envelope.ResultDigest {
			if governed && authorityState.BarrierDigest != "" && (prior.attemptKey != authorityKey || authorityState.BarrierAdmissionFactDigest != prior.fact.FactDigest) {
				i.recordQuarantine(ReasonStaleLease, drcDigest, envelope.ResultDigest, now)
				return AdmissionFact{}, fmt.Errorf("%w: terminalization barrier did not bind this admission", ErrStaleLease)
			}
			fact := prior.fact
			fact.IdempotentReplay = true
			return fact, nil
		}
		i.recordQuarantine(ReasonDigestMismatch, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: idempotency key %q reused with different DRC or result digest",
			ErrDigestMismatch, replayKey)
	}
	// Every result kind participates in the same Attempt barrier. Hot-path
	// checkpoint/heartbeat/log traffic is not allowed to leak through after
	// terminalization merely because it skips capability freshness checks.
	if governed {
		if authorityState.PendingEffectIntentFactDigest != "" || authorityState.SupervisorPendingIntentDigest != "" || authorityState.SupervisorInterventionDigest != "" {
			i.recordQuarantine(ReasonStaleLease, drcDigest, envelope.ResultDigest, now)
			return AdmissionFact{}, fmt.Errorf("%w: attempt admission is closed by pending effect authority", ErrStaleLease)
		}
		if authorityState.BarrierDigest != "" {
			i.recordQuarantine(ReasonStaleLease, drcDigest, envelope.ResultDigest, now)
			return AdmissionFact{}, fmt.Errorf("%w: attempt admission is closed by terminalization barrier", ErrStaleLease)
		}
		if authorityState.ProcessStartedDigest == "" {
			i.recordQuarantine(ReasonStaleLease, drcDigest, envelope.ResultDigest, now)
			return AdmissionFact{}, fmt.Errorf("%w: attempt has no process-started authority", ErrStaleLease)
		}
		closure, closureErr := authorityState.LaunchClosure.Closure()
		if closureErr != nil || authorityState.LaunchMaterialsDigest != closure.LaunchMaterialsDigest || authorityState.AgentLaunchSpecDigest != closure.AgentLaunchSpecDigest || !processMatchesRuntime(authorityState.Process, closure.RuntimeExecutable) {
			i.recordQuarantine(ReasonStaleLease, drcDigest, envelope.ResultDigest, now)
			return AdmissionFact{}, fmt.Errorf("%w: launch closure/process authority mismatch", ErrStaleLease)
		}
		if closure.ClosureProfileID == launchidentity.Pi0843DarwinARM64Profile {
			held, reopenErr := launchidentity.Reopen(closure)
			if reopenErr != nil {
				i.recordQuarantine(ReasonStaleLease, drcDigest, envelope.ResultDigest, now)
				return AdmissionFact{}, fmt.Errorf("%w: current launch closure cannot be re-opened", ErrStaleLease)
			}
			held.Close()
		}
		if envelope.Kind == KindWorkerResult && authorityState.CommittedResultFactDigest != "" {
			i.recordQuarantine(ReasonDigestMismatch, drcDigest, envelope.ResultDigest, now)
			return AdmissionFact{}, fmt.Errorf("%w: Attempt already has a committed WorkerResult", ErrDigestMismatch)
		}
		if envelope.Kind == KindWorkerResult && authorityState.SupervisorBootstrapDigest != "" {
			if validateBusinessOutcomeReference(authorityState, outcomeFactDigest, processsupervisor.CommandCollect, SupervisorTranscriptCollected) != nil || !zeroSupervisorCommandEvidence(collect) {
				i.recordQuarantine(ReasonStaleLease, drcDigest, envelope.ResultDigest, now)
				return AdmissionFact{}, fmt.Errorf("%w: WorkerResult is not bound to a durable current supervisor collect outcome", ErrStaleLease)
			}
		} else if !zeroSupervisorCommandEvidence(collect) || outcomeFactDigest != "" {
			i.recordQuarantine(ReasonMalformed, drcDigest, envelope.ResultDigest, now)
			return AdmissionFact{}, fmt.Errorf("%w: supervisor collect evidence on unrelated admission", ErrMalformedEnvelope)
		}
	} else if !zeroSupervisorCommandEvidence(collect) || outcomeFactDigest != "" {
		i.recordQuarantine(ReasonMalformed, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: supervisor collect evidence on ungoverned admission", ErrMalformedEnvelope)
	}

	// ── 2. Kind→operation mapping check (ADR 0044 R2) ─────────────────────────
	expectedOp, _ := kindToOperation(envelope.Kind)
	if drc.Operation != expectedOp {
		i.recordQuarantine(ReasonOperationMismatch, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: kind %q maps to operation %q but DRC has %q",
			ErrOperationMismatch, envelope.Kind, expectedOp, drc.Operation)
	}

	// ── 3. Revocation check ───────────────────────────────────────────────────
	if i.ledger.Revoked {
		i.recordQuarantine(ReasonRevoked, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: DRC has been revoked", ErrDRCRevoked)
	}

	// ── 4. Actor/target binding checks ───────────────────────────────────────
	if drc.AttemptID != i.ledger.AttemptID {
		i.recordQuarantine(ReasonMalformed, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: attemptId mismatch: got %q want %q",
			ErrMalformedDRC, drc.AttemptID, i.ledger.AttemptID)
	}
	if drc.AllocationID != i.ledger.AllocationID {
		i.recordQuarantine(ReasonMalformed, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: allocationId mismatch: got %q want %q",
			ErrMalformedDRC, drc.AllocationID, i.ledger.AllocationID)
	}
	if drc.LeaseID != i.ledger.LeaseID {
		i.recordQuarantine(ReasonStaleLease, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: leaseId mismatch", ErrStaleLease)
	}
	if drc.FencingToken != i.ledger.FencingToken {
		i.recordQuarantine(ReasonStaleGeneration, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: fencingToken mismatch", ErrStaleGeneration)
	}

	// ── 5. Generation check ───────────────────────────────────────────────────
	if drc.Generation < i.ledger.Generation {
		i.recordQuarantine(ReasonStaleGeneration, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: generation %d < current %d",
			ErrStaleGeneration, drc.Generation, i.ledger.Generation)
	}

	// ── 6. Lease expiry check ─────────────────────────────────────────────────
	if !i.ledger.Expiry.IsZero() && now.After(i.ledger.Expiry) {
		i.recordQuarantine(ReasonStaleLease, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: lease expired at %v", ErrStaleLease, i.ledger.Expiry)
	}
	if now.After(drc.Expiry) {
		i.recordQuarantine(ReasonExpired, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: DRC expired at %v", ErrExpired, drc.Expiry)
	}

	// ── 7. Digest check ───────────────────────────────────────────────────────
	// RequestDigest on the DRC must match the envelope resultDigest.
	if drc.RequestDigest != envelope.ResultDigest {
		i.recordQuarantine(ReasonDigestMismatch, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: DRC.RequestDigest %q != envelope.ResultDigest %q",
			ErrDigestMismatch, drc.RequestDigest, envelope.ResultDigest)
	}

	// ── 8. Cold path eligibility recheck (ADR 0018 current-ledger recheck) ─────
	// Hot path kinds (checkpoint, heartbeat, log) skip eligibility recheck;
	// cold path kinds (worker-result, candidate, evidence-ref, assessment,
	// receipt) must carry RegistrationID/SnapshotDigest/EvidenceDigest matching
	// the current ledger binding.
	if !isHotPathKind(envelope.Kind) {
		if err := i.recheckCold(drc, drcDigest, envelope.ResultDigest, now); err != nil {
			return AdmissionFact{}, err
		}
	}

	// ── 9. Idempotent replay detection ────────────────────────────────────────
	key := drc.IdempotencyKey
	if prior, ok := i.admitted[key]; ok {
		if !prior.legacyReplayBlocked && prior.drcDigest == drcDigest && prior.envelopeKind == envelope.Kind && prior.envelopeSequence == envelope.Sequence && prior.envelopeDigest == envelope.ResultDigest {
			// Same digest: idempotent replay — return existing fact unchanged.
			fact := prior.fact
			fact.IdempotentReplay = true
			return fact, nil
		}
		// Same idempotency key but different digest: this is a forgery.
		i.recordQuarantine(ReasonDigestMismatch, drcDigest, envelope.ResultDigest, now)
		return AdmissionFact{}, fmt.Errorf("%w: idempotency key %q reused with different digest",
			ErrDigestMismatch, key)
	}

	// ── 10. Admit ─────────────────────────────────────────────────────────────
	nextLedgerSequence := i.ledgerSequence + 1
	factInput, _ := json.Marshal(struct {
		DRCDigest        string       `json:"drcDigest"`
		EnvelopeKind     EnvelopeKind `json:"envelopeKind"`
		EnvelopeSequence uint64       `json:"envelopeSequence"`
		EnvelopeDigest   string       `json:"envelopeDigest"`
		LedgerSequence   uint64       `json:"ledgerSequence"`
	}{drcDigest, envelope.Kind, envelope.Sequence, envelope.ResultDigest, nextLedgerSequence})
	factDigest := canonical.DigestBytes(factInput)

	fact := AdmissionFact{
		FactDigest:       factDigest,
		LedgerSequence:   nextLedgerSequence,
		IdempotentReplay: false,
	}
	authorityHead := ""
	if i.store != nil {
		// 成功接纳先将幂等权威锚点落账，再写入进程内 admitted——崩溃/重启或
		// 重复送达时，跨进程的 replay 检测由该账本支持。落账失败即 reject。
		var governedState *AttemptAuthorityState
		if governed {
			governedState = &authorityState
		}
		var err error
		authorityHead, err = i.store.recordAdmittedLocked(key, governedState, drcDigest, envelope, factDigest, nextLedgerSequence, collect, outcomeFactDigest)
		if err != nil {
			return AdmissionFact{}, fmt.Errorf("resultingress: record admitted to durable store: %w", err)
		}
	}
	i.ledgerSequence = nextLedgerSequence
	entry := admittedEntry{fact: fact, drcDigest: drcDigest, envelopeKind: envelope.Kind, envelopeSequence: envelope.Sequence, envelopeDigest: envelope.ResultDigest}
	if governed {
		entry.attemptKey = authorityKey
		authorityState.Revision++
		authorityState.HeadDigest = authorityHead
		if envelope.Kind == KindWorkerResult {
			authorityState.CommittedResultFactDigest = fact.FactDigest
			authorityState.CommittedResultSequence = fact.LedgerSequence
			authorityState.CommittedResultOutcomeDigest = outcomeFactDigest
			authorityState.CommittedResultCollect = collect
			if outcomeFactDigest != "" {
				authorityState.CommittedResultCollect, _ = supervisorCheckpointEvidence(authorityState, outcomeFactDigest)
				authorityState.SupervisorMechanicsAuthorityHead = authorityHead
			}
		}
		i.attempts[authorityKey] = authorityState
	}
	i.admitted[key] = entry
	return fact, nil
}

// ReplayCommitted performs a lookup-only replay of an already committed
// delivery. It never creates a new admission. This is the crash-recovery path
// for an outbox whose authority may be stale by the time the driver restarts.
// Exact DRC and result digests are required; a conflicting reuse fails closed.
func (i *Ingress) ReplayCommitted(drc DRC, envelope ResultEnvelope) (AdmissionFact, bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.store != nil {
		var fact AdmissionFact
		var found bool
		var replayErr error
		if err := i.store.transact(i, func() error {
			fact, found, replayErr = i.replayCommittedLocked(drc, envelope)
			return nil
		}); err != nil {
			return AdmissionFact{}, false, err
		}
		return fact, found, replayErr
	}
	return i.replayCommittedLocked(drc, envelope)
}

func (i *Ingress) replayCommittedLocked(drc DRC, envelope ResultEnvelope) (AdmissionFact, bool, error) {
	if err := drc.Validate(); err != nil {
		return AdmissionFact{}, false, err
	}
	if err := envelope.Validate(); err != nil {
		return AdmissionFact{}, false, err
	}
	drcDigest, err := drc.Digest()
	if err != nil {
		return AdmissionFact{}, false, err
	}
	authorityState, authorityKey, governed, authorityConflict := i.currentAttemptForDRC(drc)
	if authorityConflict {
		return AdmissionFact{}, false, fmt.Errorf("%w: DRC tuple conflicts with durable Attempt authority", ErrStaleLease)
	}
	prior, ok := i.admitted[drc.IdempotencyKey]
	if !ok {
		return AdmissionFact{}, false, nil
	}
	if prior.legacyReplayBlocked {
		return AdmissionFact{}, false, fmt.Errorf("%w: idempotency key %q belongs to an unversioned legacy admission whose missing authority cannot be replayed",
			ErrDigestMismatch, drc.IdempotencyKey)
	}
	if prior.drcDigest != drcDigest || prior.envelopeKind != envelope.Kind || prior.envelopeSequence != envelope.Sequence || prior.envelopeDigest != envelope.ResultDigest {
		return AdmissionFact{}, false, fmt.Errorf("%w: idempotency key %q reused with different DRC or result digest",
			ErrDigestMismatch, drc.IdempotencyKey)
	}
	if governed && authorityState.BarrierDigest != "" && (prior.attemptKey != authorityKey || authorityState.BarrierAdmissionFactDigest != prior.fact.FactDigest) {
		return AdmissionFact{}, false, fmt.Errorf("%w: terminalization barrier did not bind this admission", ErrStaleLease)
	}
	fact := prior.fact
	fact.IdempotentReplay = true
	return fact, true, nil
}

func (i *Ingress) resetDurableReplayState() {
	i.ledgerSequence = 0
	i.admitted = make(map[string]admittedEntry)
	i.attempts = make(map[string]AttemptAuthorityState)
	i.controlOwners = make(map[string]ControlOwnerState)
	i.effects = make(map[string]EffectAuthorityState)
	i.allocations = make(map[string]allocationAuthorityState)
	i.effectCommands = make(map[string]string)
	i.effectIdempotency = make(map[string]string)
	i.effectMarkers = make(map[string]string)
	i.quarantine = nil
}

// currentAttemptForDRC resolves a governed logical Attempt by namespace,
// task, run and attempt, then requires the entire frozen dispatch/process
// tuple. Allocation, lease or command drift is a conflict, never a fallback
// into the legacy admission path.
func (i *Ingress) currentAttemptForDRC(drc DRC) (AttemptAuthorityState, string, bool, bool) {
	fencingDigest := canonical.DigestBytes([]byte(drc.FencingToken))
	relatedCandidates := 0
	wireNamespaceCandidates := 0
	exactMatches := 0
	var matchedState AttemptAuthorityState
	var matchedKey string
	for key, state := range i.attempts {
		id := state.Identity
		if id.TaskID != drc.TaskID || id.RunID != drc.RunID || id.AttemptID != drc.AttemptID {
			continue
		}
		relatedCandidates++
		if id.AuthorityNamespaceRef != drc.AuthorityNamespaceID {
			continue
		}
		wireNamespaceCandidates++
		matches := drc.Generation <= math.MaxInt64 && id.AllocationID == drc.AllocationID && id.LeaseID == drc.LeaseID && id.DispatchGeneration == int64(drc.Generation) && id.FencingTokenDigest == fencingDigest && state.ProcessStartedDigest != "" && state.CommandID == drc.CommandID
		if matches {
			exactMatches++
			matchedState, matchedKey = state, key
		}
	}
	if relatedCandidates == 0 {
		return AttemptAuthorityState{}, "", false, false
	}
	if wireNamespaceCandidates == 1 && exactMatches == 1 {
		return matchedState, matchedKey, true, false
	}
	return AttemptAuthorityState{}, "", false, true
}

// Quarantine returns a read-only copy of all quarantine records.
// Quarantine records must not be used for business derivation.
func (i *Ingress) Quarantine() []QuarantineRecord {
	i.mu.Lock()
	defer i.mu.Unlock()
	out := make([]QuarantineRecord, len(i.quarantine))
	copy(out, i.quarantine)
	return out
}

func (i *Ingress) recordQuarantine(reason RejectionReason, drcDigest, envelopeDigest string, at time.Time) {
	i.quarantine = append(i.quarantine, QuarantineRecord{
		Reason:         reason,
		DRCDigest:      drcDigest,
		EnvelopeDigest: envelopeDigest,
		ObservedAt:     at,
	})
	if i.store != nil {
		// 写出器由调用方（Admit）持锁；落账失败会让准确整体 admission reject，
		// 见调用路径的处理。best-effort 记录，绝不静默丢弃失败。
		_ = i.store.recordQuarantinedLocked(reason, drcDigest, envelopeDigest, at)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

const digestPrefix = "sha256:"

func requireDigest(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if !strings.HasPrefix(v, digestPrefix) {
		return fmt.Errorf("%s must carry the sha256: prefix", field)
	}
	hex := strings.TrimPrefix(v, digestPrefix)
	if len(hex) != 64 {
		return fmt.Errorf("%s must be a 64-character sha256 hex digest", field)
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%s must be lowercase hex", field)
		}
	}
	return nil
}
