package cloudflare

// Truthful live-evidence record layer (M10): the closed facts, the
// unforgeable receipt, the verifier-side trust root and the durable atomic
// compare-and-consume replay ledger.
//
// This file is the record layer only. It never constructs a signer, private
// key, credential or consumption authority: a receipt is signed by an
// independent credentialed operator through an injected ReceiptSigner, and
// the replay ledger is owned by an independent verifier, never by the
// provider or the Worker/app. The receipt, evidence and ledger carry only
// non-sensitive identity, digest, closed status and count material; a token,
// URL, environment value, path or stdout/stderr capture never enters any of
// them.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// SignatureAlgorithmEd25519 is the single supported receipt signature
// algorithm. It is a closed constant: any other algorithm fails closed.
const SignatureAlgorithmEd25519 = "ed25519"

// liveEvidenceDigestPrefix prefixes every canonical content digest carried
// by a live-evidence receipt or the replay ledger.
const liveEvidenceDigestPrefix = "sha256:"

// Sentinel errors exposed by the live-evidence record layer. Every
// record-layer failure wraps exactly one of them, so fixtures can assert a
// fixed sentinel.
var (
	// ErrLiveEvidenceInvalid rejects a malformed receipt or ledger record.
	ErrLiveEvidenceInvalid = errors.New("cloudflare live evidence: invalid receipt")

	// ErrLiveEvidenceSignatureInvalid rejects a signature that does not
	// verify against the bound trust root.
	ErrLiveEvidenceSignatureInvalid = errors.New("cloudflare live evidence: signature invalid")

	// ErrLiveEvidenceTrustRootMissing rejects a receipt whose trust root key
	// id is not configured on the verifier.
	ErrLiveEvidenceTrustRootMissing = errors.New("cloudflare live evidence: trust root missing")

	// ErrLiveEvidenceBindingMismatch rejects a receipt whose facts do not
	// exactly match the expected service/run/attempt/allocation/generation
	// binding.
	ErrLiveEvidenceBindingMismatch = errors.New("cloudflare live evidence: binding mismatch")

	// ErrLiveEvidenceSimulated rejects a receipt marked simulated: a
	// fabricated observation is never truthful live evidence.
	ErrLiveEvidenceSimulated = errors.New("cloudflare live evidence: simulated evidence rejected")

	// ErrLiveEvidenceUnavailable rejects a receipt whose live result is
	// unavailable: an unobserved outcome is never truthful live evidence.
	ErrLiveEvidenceUnavailable = errors.New("cloudflare live evidence: live result unavailable")

	// ErrLiveEvidenceReplay rejects a receipt whose digest is already
	// consumed: compare-and-consume makes the identical receipt succeed
	// exactly once.
	ErrLiveEvidenceReplay = errors.New("cloudflare live evidence: receipt already consumed")

	// ErrLiveEvidenceLedgerInvalid rejects a malformed or unreadable replay
	// ledger.
	ErrLiveEvidenceLedgerInvalid = errors.New("cloudflare live evidence: invalid ledger")
)

// LiveEvidenceClass is the closed enumeration of live-evidence provenance.
// Only a truthful live observation is admissible; a simulated record must
// fail closed at consumption.
type LiveEvidenceClass string

// Closed members of LiveEvidenceClass.
const (
	LiveEvidenceClassLive      LiveEvidenceClass = "live"
	LiveEvidenceClassSimulated LiveEvidenceClass = "simulated"
)

// Validate rejects every value outside the closed enumeration.
func (class LiveEvidenceClass) Validate() error {
	switch class {
	case LiveEvidenceClassLive, LiveEvidenceClassSimulated:
		return nil
	default:
		return fmt.Errorf("%w: unknown evidenceClass %q", ErrLiveEvidenceInvalid, string(class))
	}
}

// LiveResultStatus is the closed enumeration of the observed live-result
// fact. Unavailable is a well-formed observation that records the absence of
// a truthful outcome; consumption of an unavailable result fails closed.
type LiveResultStatus string

// Closed members of LiveResultStatus.
const (
	LiveResultCompleted   LiveResultStatus = "completed"
	LiveResultFailed      LiveResultStatus = "failed"
	LiveResultKilled      LiveResultStatus = "killed"
	LiveResultUnavailable LiveResultStatus = "unavailable"
)

// Validate rejects every value outside the closed enumeration.
func (status LiveResultStatus) Validate() error {
	switch status {
	case LiveResultCompleted, LiveResultFailed, LiveResultKilled, LiveResultUnavailable:
		return nil
	default:
		return fmt.Errorf("%w: unknown liveResult %q", ErrLiveEvidenceInvalid, string(status))
	}
}

// ProvisionStatus is the closed enumeration of the observed provision fact.
type ProvisionStatus string

// Closed members of ProvisionStatus.
const (
	ProvisionStatusActive  ProvisionStatus = "active"
	ProvisionStatusRefused ProvisionStatus = "refused"
)

// Validate rejects every value outside the closed enumeration.
func (status ProvisionStatus) Validate() error {
	switch status {
	case ProvisionStatusActive, ProvisionStatusRefused:
		return nil
	default:
		return fmt.Errorf("%w: unknown provision status %q", ErrLiveEvidenceInvalid, string(status))
	}
}

// TerminateStatus is the closed enumeration of the observed terminate fact.
type TerminateStatus string

// Closed members of TerminateStatus.
const (
	TerminateStatusTerminated  TerminateStatus = "terminated"
	TerminateStatusReplaced    TerminateStatus = "replaced"
	TerminateStatusNotTerminal TerminateStatus = "not-terminal"
)

// Validate rejects every value outside the closed enumeration.
func (status TerminateStatus) Validate() error {
	switch status {
	case TerminateStatusTerminated, TerminateStatusReplaced, TerminateStatusNotTerminal:
		return nil
	default:
		return fmt.Errorf("%w: unknown terminate status %q", ErrLiveEvidenceInvalid, string(status))
	}
}

// BookkeepingFacts freezes the observed bookkeeping fact as counts and a
// closed drift flag. No allocation id, locator, path or URL is carried here:
// only the closed cardinality and drift observation.
type BookkeepingFacts struct {
	ActiveAllocationCount int64 `json:"activeAllocationCount"`
	OrphanAllocationCount int64 `json:"orphanAllocationCount"`
	DriftDetected         bool  `json:"driftDetected"`
}

// validate fails closed on any negative count.
func (facts BookkeepingFacts) validate() error {
	if facts.ActiveAllocationCount < 0 {
		return fmt.Errorf("%w: bookkeeping.activeAllocationCount must be non-negative", ErrLiveEvidenceInvalid)
	}
	if facts.OrphanAllocationCount < 0 {
		return fmt.Errorf("%w: bookkeeping.orphanAllocationCount must be non-negative", ErrLiveEvidenceInvalid)
	}
	return nil
}

// LiveFacts freezes the exact live-evidence facts one receipt binds:
// service, run, attempt, allocation, generation, live-result, provision,
// terminate and bookkeeping. Every member is non-sensitive identity, closed
// status or count material; no token, URL, environment value, path or
// stdout/stderr capture may enter it.
type LiveFacts struct {
	ServiceId    string           `json:"serviceId"`
	RunId        string           `json:"runId"`
	AttemptId    string           `json:"attemptId"`
	AllocationId string           `json:"allocationId"`
	Generation   int64            `json:"generation"`
	LiveResult   LiveResultStatus `json:"liveResult"`
	ExitCode     int              `json:"exitCode"`
	Provision    ProvisionStatus  `json:"provision"`
	Terminate    TerminateStatus  `json:"terminate"`
	Bookkeeping  BookkeepingFacts `json:"bookkeeping"`
}

// validate fails closed on any missing or malformed fact.
func (facts LiveFacts) validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"serviceId", facts.ServiceId},
		{"runId", facts.RunId},
		{"attemptId", facts.AttemptId},
		{"allocationId", facts.AllocationId},
	} {
		if err := requireLiveText(field.name, field.value); err != nil {
			return err
		}
	}
	if facts.Generation < 1 {
		return fmt.Errorf("%w: generation must be a positive integer", ErrLiveEvidenceInvalid)
	}
	if err := facts.LiveResult.Validate(); err != nil {
		return err
	}
	if err := facts.Provision.Validate(); err != nil {
		return err
	}
	if err := facts.Terminate.Validate(); err != nil {
		return err
	}
	return facts.Bookkeeping.validate()
}

// LiveEvidenceReceipt is the unforgeable live-evidence receipt issued by an
// independent credentialed operator over one exact fact set. ReceiptDigest
// is the canonical content digest of the record with the digest itself and
// the signature detached; the signature covers that digest and is verified
// against the trust root identified by TrustRootKeyId. Any substitution of a
// fact, class, trust root key id, algorithm or timestamp breaks the digest
// and therefore the signature.
type LiveEvidenceReceipt struct {
	ReceiptDigest      string            `json:"receiptDigest"`
	EvidenceClass      LiveEvidenceClass `json:"evidenceClass"`
	Facts              LiveFacts         `json:"facts"`
	TrustRootKeyId     string            `json:"trustRootKeyId"`
	SignatureAlgorithm string            `json:"signatureAlgorithm"`
	Signature          string            `json:"signature"`
	SignedAt           string            `json:"signedAt"`
}

// validateContent checks every content field except the receiptDigest
// binding and the signature itself, so a not-yet-signed receipt can still
// compute its digest.
func (receipt LiveEvidenceReceipt) validateContent() error {
	if err := receipt.EvidenceClass.Validate(); err != nil {
		return err
	}
	if err := receipt.Facts.validate(); err != nil {
		return err
	}
	if err := requireLiveText("trustRootKeyId", receipt.TrustRootKeyId); err != nil {
		return err
	}
	if receipt.SignatureAlgorithm != SignatureAlgorithmEd25519 {
		return fmt.Errorf("%w: unsupported signatureAlgorithm %q", ErrLiveEvidenceInvalid, receipt.SignatureAlgorithm)
	}
	return requireLiveRFC3339("signedAt", receipt.SignedAt)
}

// Digest returns the canonical content digest of the receipt: RFC 8785 JCS
// over all content fields with receiptDigest and signature detached.
func (receipt LiveEvidenceReceipt) Digest() (string, error) {
	if err := receipt.validateContent(); err != nil {
		return "", err
	}
	detached := receipt
	detached.ReceiptDigest = ""
	detached.Signature = ""
	return liveCanonicalDigest(detached)
}

// Validate fails closed on any missing or malformed field, on a missing
// signature, and on a receiptDigest that does not match the recomputed
// canonical content digest.
func (receipt LiveEvidenceReceipt) Validate() error {
	if err := receipt.validateContent(); err != nil {
		return err
	}
	if strings.TrimSpace(receipt.ReceiptDigest) == "" {
		return fmt.Errorf("%w: receiptDigest must be a non-empty digest", ErrLiveEvidenceInvalid)
	}
	if strings.TrimSpace(receipt.Signature) == "" {
		return fmt.Errorf("%w: signature must be a non-empty string", ErrLiveEvidenceInvalid)
	}
	computed, err := receipt.Digest()
	if err != nil {
		return err
	}
	if receipt.ReceiptDigest != computed {
		return fmt.Errorf("%w: receiptDigest does not match the canonical content digest", ErrLiveEvidenceInvalid)
	}
	return nil
}

// ParseLiveEvidenceReceipt decodes a wire document into a validated
// LiveEvidenceReceipt. The document is first canonicalized under RFC 8785
// JCS, which rejects duplicate members fail closed, and unknown members are
// rejected at every depth.
func ParseLiveEvidenceReceipt(raw []byte) (*LiveEvidenceReceipt, error) {
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: document rejected: %v", ErrLiveEvidenceInvalid, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(canonicalized))
	decoder.DisallowUnknownFields()
	var receipt LiveEvidenceReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return nil, fmt.Errorf("%w: document decode: %v", ErrLiveEvidenceInvalid, err)
	}
	if err := receipt.Validate(); err != nil {
		return nil, err
	}
	return &receipt, nil
}

// ReceiptSigner signs a canonical receipt digest with the credentialed
// operator's bound key. The private key lives behind this interface and is
// injected by the harness; this package and the app never construct it.
type ReceiptSigner interface {
	// KeyId returns the stable id of the signing key, which is the trust
	// root key id the verifier resolves.
	KeyId() string
	// Algorithm returns the signature algorithm, which must be
	// SignatureAlgorithmEd25519.
	Algorithm() string
	// Sign signs the canonical receipt digest and returns the raw signature
	// bytes.
	Sign(receiptDigest string) ([]byte, error)
}

// TrustRoot is the verifier-side public trust anchor for one credentialed
// operator key. It carries only public material: a key id, an algorithm and
// the ed25519 public key.
type TrustRoot struct {
	KeyId     string
	Algorithm string
	PublicKey ed25519.PublicKey
}

// Validate fails closed on an empty key id, an unsupported algorithm or a
// malformed public key.
func (root TrustRoot) Validate() error {
	if err := requireLiveText("trustRoot.keyId", root.KeyId); err != nil {
		return err
	}
	if root.Algorithm != SignatureAlgorithmEd25519 {
		return fmt.Errorf("%w: unsupported trust root algorithm %q", ErrLiveEvidenceInvalid, root.Algorithm)
	}
	if len(root.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: trust root public key must be %d bytes", ErrLiveEvidenceInvalid, ed25519.PublicKeySize)
	}
	return nil
}

// Verify checks one receipt signature against the trust root. The signature
// is base64-encoded and covers the canonical receipt digest.
func (root TrustRoot) Verify(receiptDigest, signature string) error {
	if err := root.Validate(); err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("%w: signature is not valid base64", ErrLiveEvidenceSignatureInvalid)
	}
	if !ed25519.Verify(root.PublicKey, []byte(receiptDigest), raw) {
		return ErrLiveEvidenceSignatureInvalid
	}
	return nil
}

// LiveEvidenceLedger is the durable, failure-atomic, compare-and-consume
// replay ledger of consumed live-evidence receipt digests. It is owned by
// the independent verifier: the Worker/app never forges consumption
// authority by writing to it out of band. The zero value is not usable;
// construct it with NewLiveEvidenceLedger.
type LiveEvidenceLedger struct {
	mu       sync.Mutex
	path     string
	consumed map[string]struct{}
	write    func([]byte) error
}

// liveEvidenceLedgerState is the serializable form of one ledger.
type liveEvidenceLedgerState struct {
	Consumed []string `json:"consumed"`
}

// NewLiveEvidenceLedger opens (or creates) the durable ledger file at path
// and loads any previously consumed digests. This is the production
// constructor: every consume is durably persisted through an atomic
// temp-file write plus fsync plus rename, and a failed write leaves the
// in-memory set untouched, so a restart or a crash converges on the exact
// set of digests already consumed.
func NewLiveEvidenceLedger(path string) (*LiveEvidenceLedger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: the ledger path must be a non-empty string", ErrLiveEvidenceLedgerInvalid)
	}
	ledger := &LiveEvidenceLedger{path: path, consumed: map[string]struct{}{}}
	ledger.write = func(data []byte) error { return atomicWriteFile(path, data) }
	if err := ledger.load(); err != nil {
		return nil, err
	}
	return ledger, nil
}

// load reads the persisted ledger from disk, if any. A missing file is an
// empty ledger; a malformed file fails closed.
func (ledger *LiveEvidenceLedger) load() error {
	data, err := os.ReadFile(ledger.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: read: %v", ErrLiveEvidenceLedgerInvalid, err)
	}
	if len(data) == 0 {
		return nil
	}
	var state liveEvidenceLedgerState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrLiveEvidenceLedgerInvalid, err)
	}
	consumed := make(map[string]struct{}, len(state.Consumed))
	for _, digest := range state.Consumed {
		if err := requireLiveDigest("consumed", digest); err != nil {
			return fmt.Errorf("%w: %v", ErrLiveEvidenceLedgerInvalid, err)
		}
		if _, duplicate := consumed[digest]; duplicate {
			return fmt.Errorf("%w: duplicate consumed digest", ErrLiveEvidenceLedgerInvalid)
		}
		consumed[digest] = struct{}{}
	}
	ledger.consumed = consumed
	return nil
}

// Consume atomically records receiptDigest as consumed. It returns true the
// first time the digest is consumed; a digest that is already present is a
// replay and returns false with ErrLiveEvidenceReplay. Any encoding or
// persistence failure leaves the in-memory set untouched and fails closed.
// The compare-and-consume is performed under one mutex followed by one
// durable atomic write, so two concurrent consumers cannot both succeed.
func (ledger *LiveEvidenceLedger) Consume(receiptDigest string) (bool, error) {
	if err := requireLiveDigest("receiptDigest", receiptDigest); err != nil {
		return false, err
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if _, ok := ledger.consumed[receiptDigest]; ok {
		return false, ErrLiveEvidenceReplay
	}
	staged := make(map[string]struct{}, len(ledger.consumed)+1)
	for digest := range ledger.consumed {
		staged[digest] = struct{}{}
	}
	staged[receiptDigest] = struct{}{}
	data, err := json.Marshal(&liveEvidenceLedgerState{Consumed: sortedLiveDigests(staged)})
	if err != nil {
		return false, fmt.Errorf("%w: encode: %v", ErrLiveEvidenceLedgerInvalid, err)
	}
	if err := ledger.write(data); err != nil {
		return false, err
	}
	ledger.consumed = staged
	return true, nil
}

// Consumed reports whether receiptDigest is already recorded as consumed.
func (ledger *LiveEvidenceLedger) Consumed(receiptDigest string) bool {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	_, ok := ledger.consumed[receiptDigest]
	return ok
}

// Count returns the number of distinct consumed digests.
func (ledger *LiveEvidenceLedger) Count() int {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return len(ledger.consumed)
}

// ConsumedDigests returns the consumed digests in stable order.
func (ledger *LiveEvidenceLedger) ConsumedDigests() []string {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	return sortedLiveDigests(ledger.consumed)
}

// sortedLiveDigests returns the digest keys in ascending order, so the
// serialized ledger is deterministic.
func sortedLiveDigests(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for digest := range set {
		out = append(out, digest)
	}
	sort.Strings(out)
	return out
}

// requireLiveText fails closed on empty or blank values.
func requireLiveText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s must be a non-empty string", ErrLiveEvidenceInvalid, field)
	}
	return nil
}

// requireLiveDigest fails closed unless value is a well-formed
// sha256-prefixed lowercase hex digest.
func requireLiveDigest(field, value string) error {
	if err := requireLiveText(field, value); err != nil {
		return err
	}
	if !strings.HasPrefix(value, liveEvidenceDigestPrefix) {
		return fmt.Errorf("%w: %s must carry the %s digest prefix", ErrLiveEvidenceInvalid, field, liveEvidenceDigestPrefix)
	}
	hexPart := strings.TrimPrefix(value, liveEvidenceDigestPrefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("%w: %s must be a 64 character sha256 hex digest", ErrLiveEvidenceInvalid, field)
	}
	for _, r := range hexPart {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("%w: %s must be lowercase hex", ErrLiveEvidenceInvalid, field)
		}
	}
	return nil
}

// requireLiveRFC3339 fails closed unless value parses as RFC 3339.
func requireLiveRFC3339(field, value string) error {
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("%w: %s must be an RFC 3339 timestamp", ErrLiveEvidenceInvalid, field)
	}
	return nil
}

// liveCanonicalDigest marshals value, canonicalizes it under RFC 8785 JCS and
// returns the sha256 digest of the canonical bytes.
func liveCanonicalDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: canonical marshal: %v", ErrLiveEvidenceInvalid, err)
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return "", fmt.Errorf("%w: canonical digest: %v", ErrLiveEvidenceInvalid, err)
	}
	return canonical.DigestBytes(canonicalized), nil
}
