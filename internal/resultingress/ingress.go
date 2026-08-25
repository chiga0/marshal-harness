package resultingress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
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
	fact           AdmissionFact
	envelopeDigest string
}

// Ingress is the single admission gate for external results (ADR 0044 decision 1).
// Constructed via NewIngress; zero-value is not valid.
type Ingress struct {
	mu             sync.Mutex
	ledger         LedgerBinding
	ledgerSequence uint64
	// admitted maps idempotencyKey → admittedEntry for replay detection.
	admitted   map[string]admittedEntry
	quarantine []QuarantineRecord
	// clock allows deterministic testing without real time reads.
	clock func() time.Time
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
		ledger:   binding,
		admitted: make(map[string]admittedEntry),
		clock:    time.Now,
	}, nil
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
	i.mu.Lock()
	defer i.mu.Unlock()

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
		if prior.envelopeDigest == envelope.ResultDigest {
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
	i.ledgerSequence++
	factInput, _ := json.Marshal(struct {
		DRCDigest      string `json:"drcDigest"`
		EnvelopeDigest string `json:"envelopeDigest"`
		Sequence       uint64 `json:"sequence"`
	}{drcDigest, envelope.ResultDigest, i.ledgerSequence})
	factDigest := canonical.DigestBytes(factInput)

	fact := AdmissionFact{
		FactDigest:       factDigest,
		LedgerSequence:   i.ledgerSequence,
		IdempotentReplay: false,
	}
	i.admitted[key] = admittedEntry{fact: fact, envelopeDigest: envelope.ResultDigest}
	return fact, nil
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
