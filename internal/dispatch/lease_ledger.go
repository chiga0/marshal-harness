package dispatch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// leaseLedgerFileName is the single append-only lease ledger kept inside
// the ledger directory; every line carries exactly one canonical JSON fact.
const leaseLedgerFileName = "leases.jsonl"

// Closed fact types of the append-only lease ledger. Matching is case
// sensitive.
const (
	leaseFactTypeClaimed         = "lease-claimed"
	leaseFactTypeReservedClaimed = "lease-reserved-claimed"
	leaseFactTypeCancelled       = "lease-cancelled"
	leaseFactTypeExpired         = "lease-expired"
	leaseFactTypeAttemptTerminal = "lease-attempt-terminal"
	leaseFactTypeBumped          = "generation-bumped"
)

// ErrMemoryOnlyLeaseLedger is returned whenever a ledger that is not bound
// to a durable directory is asked to read or write lease facts: memory-only
// lease ledgers are forbidden fail closed.
var ErrMemoryOnlyLeaseLedger = errors.New("dispatch: memory-only lease ledger not allowed: the lease ledger is not bound to a durable ledger directory")

// ErrLeaseConflict is the fail-closed rejection of any operation that
// collides with the existing ledger state: a duplicate leaseId, a second
// active lease on the identical (runId, attemptId) binding, or any
// operation on a lease that already reached a terminal state.
var ErrLeaseConflict = errors.New("dispatch: lease conflict")

// ErrLeaseGenerationConflict is the compare-and-append rejection: the
// expectedGeneration carried by a cancel, expire or generation bump does
// not match the generation the ledger currently records for the lease.
var ErrLeaseGenerationConflict = errors.New("dispatch: lease generation conflict")

// ErrUnknownLease is returned for operations that reference a leaseId the
// ledger has never accepted.
var ErrUnknownLease = errors.New("dispatch: unknown leaseId")

// LeaseLedger is the durable append-only ledger of DispatchLease lifecycle
// facts (M9-a). Each line of leases.jsonl records exactly one fact —
// lease-claimed, lease-cancelled, lease-expired or generation-bumped —
// carrying a monotonically increasing sequence and a canonical content
// digest. Existing lines are never rewritten or deleted; NewLeaseLedger
// rebuilds the in-memory indexes by replaying every fact, so the current
// lease state, generation and terminal facts survive crashes and restarts
// deterministically: the identical ledger bytes always rebuild the
// identical state.
type LeaseLedger struct {
	mu             sync.Mutex
	dir            string
	leases         map[string]DispatchLease
	activeBindings map[string]string
	// closedAttemptBindings only contains new Attempt-authority projections.
	// Legacy cancelled/expired history keeps its historical replay semantics.
	closedAttemptBindings   map[string]string
	terminalAuthorities     map[string]string
	reservedClaims          map[string]reservedClaimFact
	reservedAttemptBindings map[string]string
	reservationBindings     map[string]string
	nextSequence            int64
}

// leaseClaimFact is the append-only ledger fact recording one accepted
// DispatchLease claim: the complete validated lease snapshot at generation
// start plus the ledger sequence.
type leaseClaimFact struct {
	FactType string        `json:"factType"`
	Sequence int64         `json:"sequence"`
	Lease    DispatchLease `json:"lease"`
	Digest   string        `json:"digest"`
}

// leaseCancelFact is the append-only ledger fact recording one cancel
// transition; the claim line is never rewritten. Generation is the
// resulting generation the lease carries after the transition.
type leaseCancelFact struct {
	FactType     string       `json:"factType"`
	Sequence     int64        `json:"sequence"`
	LeaseId      string       `json:"leaseId"`
	CancelReason CancelReason `json:"cancelReason"`
	Generation   int64        `json:"generation"`
	Digest       string       `json:"digest"`
}

// leaseExpireFact is the append-only ledger fact recording one expire
// transition; the claim line is never rewritten. Generation is the
// resulting generation the lease carries after the transition.
type leaseExpireFact struct {
	FactType   string `json:"factType"`
	Sequence   int64  `json:"sequence"`
	LeaseId    string `json:"leaseId"`
	Generation int64  `json:"generation"`
	ExpiredAt  string `json:"expiredAt"`
	Digest     string `json:"digest"`
}

// leaseAttemptTerminalFact is a read model projection of the single Attempt
// authority barrier. Its closed terminal union prevents abnormal cancellation
// or expiry from being mislabeled as normal completion.
type leaseAttemptTerminalFact struct {
	FactType                   string           `json:"factType"`
	Sequence                   int64            `json:"sequence"`
	LeaseId                    string           `json:"leaseId"`
	RunId                      string           `json:"runId"`
	AttemptId                  string           `json:"attemptId"`
	AllocationId               string           `json:"allocationId"`
	FromGeneration             int64            `json:"fromGeneration"`
	TerminalGeneration         int64            `json:"terminalGeneration"`
	TerminalState              LeaseState       `json:"terminalState"`
	CompletionReason           CompletionReason `json:"completionReason,omitempty"`
	CancelReason               CancelReason     `json:"cancelReason,omitempty"`
	AttemptAuthorityHeadDigest string           `json:"attemptAuthorityHeadDigest"`
	Digest                     string           `json:"digest"`
}

// leaseBumpFact is the append-only ledger fact recording one
// compare-and-append generation bump; the claim line is never rewritten.
// The fencingToken is the deterministic derivation for the new generation.
type leaseBumpFact struct {
	FactType       string `json:"factType"`
	Sequence       int64  `json:"sequence"`
	LeaseId        string `json:"leaseId"`
	FromGeneration int64  `json:"fromGeneration"`
	ToGeneration   int64  `json:"toGeneration"`
	FencingToken   string `json:"fencingToken"`
	Digest         string `json:"digest"`
}

// leaseFact is the sealed-envelope contract shared by every append-only
// lease ledger fact type: the canonical content digest is always computed
// with the digest binding detached and stored back on the fact before the
// canonical line is appended.
type leaseFact interface {
	factDigest() string
	setFactDigest(digest string)
}

func (fact leaseClaimFact) factDigest() string {
	return fact.Digest
}

func (fact *leaseClaimFact) setFactDigest(digest string) {
	fact.Digest = digest
}

func (fact leaseCancelFact) factDigest() string {
	return fact.Digest
}

func (fact *leaseCancelFact) setFactDigest(digest string) {
	fact.Digest = digest
}

func (fact leaseExpireFact) factDigest() string {
	return fact.Digest
}

func (fact *leaseExpireFact) setFactDigest(digest string) {
	fact.Digest = digest
}

func (fact leaseAttemptTerminalFact) factDigest() string           { return fact.Digest }
func (fact *leaseAttemptTerminalFact) setFactDigest(digest string) { fact.Digest = digest }

func (fact leaseBumpFact) factDigest() string {
	return fact.Digest
}

func (fact *leaseBumpFact) setFactDigest(digest string) {
	fact.Digest = digest
}

// NewLeaseLedger opens (creating it if absent) the durable ledger directory
// and rebuilds the in-memory indexes by replaying every ledger fact, so the
// current lease state, generation and terminal facts survive crashes and
// restarts. A corrupt, non canonical or conflicting ledger fails closed at
// construction; nothing is silently skipped. A blank dir leaves the ledger
// unbound: the zero-value and empty-directory ledgers stay constructible,
// but every read and write operation fails closed with
// ErrMemoryOnlyLeaseLedger.
func NewLeaseLedger(dir string) (*LeaseLedger, error) {
	if strings.TrimSpace(dir) == "" {
		return &LeaseLedger{}, nil
	}
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("dispatch: create lease ledger directory: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("dispatch: lease ledger directory: %w", err)
	case !info.IsDir():
		return nil, fmt.Errorf("dispatch: lease ledger path is not a directory")
	}
	ledger := &LeaseLedger{
		dir:                     dir,
		leases:                  map[string]DispatchLease{},
		activeBindings:          map[string]string{},
		closedAttemptBindings:   map[string]string{},
		terminalAuthorities:     map[string]string{},
		reservedClaims:          map[string]reservedClaimFact{},
		reservedAttemptBindings: map[string]string{},
		reservationBindings:     map[string]string{},
		nextSequence:            1,
	}
	if err := ledger.recover(); err != nil {
		return nil, err
	}
	return ledger, nil
}

// requireBound fails closed on any ledger that is not bound to a durable
// directory, including the zero value and nil receivers: memory-only lease
// ledgers are never accepted.
func (l *LeaseLedger) requireBound() error {
	if l == nil || l.dir == "" {
		return ErrMemoryOnlyLeaseLedger
	}
	return nil
}

// ledgerPath returns the path of the append-only ledger file.
func (l *LeaseLedger) ledgerPath() string {
	return filepath.Join(l.dir, leaseLedgerFileName)
}

// claimBindingKey is the single-active identity key of one lease binding:
// the identical (runId, attemptId) tuple never carries two live leases.
func claimBindingKey(runId, attemptId string) string {
	return runId + "\x00" + attemptId
}

// AppendClaim accepts one DispatchLease into the durable ledger. The lease
// must validate fail closed and must not start in a terminal state; the
// single-active invariant rejects any second live lease on the identical
// (runId, attemptId) binding and any duplicate leaseId. The lease-claimed
// fact is durably appended before it becomes visible in the indexes.
func (l *LeaseLedger) AppendClaim(lease DispatchLease) error {
	if err := l.requireBound(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := lease.Validate(); err != nil {
		return fmt.Errorf("dispatch: lease ledger claim rejected: %w", err)
	}
	if lease.LeaseState.IsTerminal() {
		return fmt.Errorf("%w: lease %s starts in terminal leaseState %q and can never be claimed", ErrLeaseConflict, lease.LeaseId, string(lease.LeaseState))
	}
	if err := l.requireClaimable(lease); err != nil {
		return err
	}
	fact := leaseClaimFact{
		FactType: leaseFactTypeClaimed,
		Sequence: l.nextSequence,
		Lease:    lease,
	}
	if err := l.appendFactLine(&fact); err != nil {
		return err
	}
	l.leases[lease.LeaseId] = lease
	l.activeBindings[claimBindingKey(lease.RunId, lease.AttemptId)] = lease.LeaseId
	l.nextSequence++
	return nil
}

// requireClaimable verifies the claim collision invariants against the
// current indexes: the leaseId must be new and the (runId, attemptId)
// binding must not already carry an active lease.
func (l *LeaseLedger) requireClaimable(lease DispatchLease) error {
	if _, exists := l.leases[lease.LeaseId]; exists {
		return fmt.Errorf("%w: leaseId %s already exists in the lease ledger; the append-only ledger never rewrites or replaces an accepted claim", ErrLeaseConflict, lease.LeaseId)
	}
	binding := claimBindingKey(lease.RunId, lease.AttemptId)
	if source, reserved := l.reservedAttemptBindings[binding]; reserved {
		return fmt.Errorf("%w: (runId, attemptId) is owned by reserved claim %s and the legacy claim API cannot mint or resurrect a sibling", ErrLeaseConflict, source)
	}
	if source, closed := l.closedAttemptBindings[binding]; closed {
		return fmt.Errorf("%w: (runId, attemptId) is terminal in Attempt authority projection %s and can never be reclaimed", ErrLeaseConflict, source)
	}
	if existing, taken := l.activeBindings[binding]; taken {
		return fmt.Errorf("%w: (runId, attemptId) already carries active lease %s; the single-active invariant never allows a second live claim of the identical attempt", ErrLeaseConflict, existing)
	}
	return nil
}

// appendFactLine seals fact with its canonical content digest, canonicalizes
// it under RFC 8785 JCS and appends it as one line to the ledger, syncing
// before returning so the fact is durable. Existing lines are never
// rewritten. The caller must hold l.mu and must have validated the fact
// semantics before the append.
func (l *LeaseLedger) appendFactLine(fact leaseFact) error {
	digest, err := leaseFactContentDigest(fact)
	if err != nil {
		return err
	}
	fact.setFactDigest(digest)
	raw, err := json.Marshal(fact)
	if err != nil {
		return fmt.Errorf("dispatch: marshal lease ledger fact: %w", err)
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return fmt.Errorf("dispatch: canonicalize lease ledger fact: %w", err)
	}
	line := append(canonicalized, '\n')
	file, err := os.OpenFile(l.ledgerPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("dispatch: open lease ledger: %w", err)
	}
	if _, err := file.Write(line); err != nil {
		file.Close()
		return fmt.Errorf("dispatch: append lease ledger fact: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("dispatch: sync lease ledger: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("dispatch: close lease ledger: %w", err)
	}
	return nil
}

// leaseFactContentDigest returns the canonical content digest of one ledger
// fact: RFC 8785 JCS over the record with the digest binding detached, so
// the digest never participates in the content it seals. The fact must
// carry the detached (empty) digest binding.
func leaseFactContentDigest(fact leaseFact) (string, error) {
	if fact.factDigest() != "" {
		return "", fmt.Errorf("dispatch: the lease ledger fact digest must be detached before sealing")
	}
	return canonicalDigestOf(fact)
}

// AppendCancel transitions one in-flight lease to the terminal cancelled
// state by appending a lease-cancelled fact; the claim line is never
// rewritten. Compare-and-append: the current generation recorded in the
// ledger must equal expectedGeneration exactly, otherwise stale fencing is
// rejected with ErrLeaseGenerationConflict. Cancelled and expired leases
// are terminal and can never be cancelled again.
func (l *LeaseLedger) AppendCancel(leaseId string, reason CancelReason, expectedGeneration int64) error {
	if err := l.requireBound(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := requireText("leaseId", leaseId); err != nil {
		return err
	}
	if err := reason.Validate(); err != nil {
		return fmt.Errorf("dispatch: lease ledger cancel rejected: %w", err)
	}
	current, err := l.currentForTransition(leaseId, expectedGeneration, "cancelled")
	if err != nil {
		return err
	}
	next, err := current.Cancel(reason)
	if err != nil {
		return fmt.Errorf("dispatch: lease ledger cancel rejected: %w", err)
	}
	fact := leaseCancelFact{
		FactType:     leaseFactTypeCancelled,
		Sequence:     l.nextSequence,
		LeaseId:      leaseId,
		CancelReason: reason,
		Generation:   next.Generation,
	}
	if err := l.appendFactLine(&fact); err != nil {
		return err
	}
	l.recordTerminal(next)
	return nil
}

// AppendExpire transitions one in-flight lease to the terminal expired
// state by appending a lease-expire fact; the claim line is never
// rewritten. Compare-and-append semantics are identical to AppendCancel;
// the expire fact records the transition time.
func (l *LeaseLedger) AppendExpire(leaseId string, expectedGeneration int64) error {
	if err := l.requireBound(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := requireText("leaseId", leaseId); err != nil {
		return err
	}
	current, err := l.currentForTransition(leaseId, expectedGeneration, "expired")
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	next, err := current.Expire(now)
	if err != nil {
		return fmt.Errorf("dispatch: lease ledger expire rejected: %w", err)
	}
	fact := leaseExpireFact{
		FactType:   leaseFactTypeExpired,
		Sequence:   l.nextSequence,
		LeaseId:    leaseId,
		Generation: next.Generation,
		ExpiredAt:  now.Format(time.RFC3339),
	}
	if err := l.appendFactLine(&fact); err != nil {
		return err
	}
	l.recordTerminal(next)
	return nil
}

// AttemptEligibilityProjection is the exact closed terminal projection read
// from the Attempt authority barrier. It carries no fencing token and grants
// no cleanup permission. TerminalState is exactly completed, cancelled, or
// expired and the two reason fields form a sealed union.
type AttemptEligibilityProjection struct {
	LeaseId                    string
	RunId                      string
	AttemptId                  string
	AllocationId               string
	FromGeneration             int64
	TerminalGeneration         int64
	TerminalState              LeaseState
	CompletionReason           CompletionReason
	CancelReason               CancelReason
	AttemptAuthorityHeadDigest string
}

func (projection AttemptEligibilityProjection) validateTerminalUnion() error {
	switch projection.TerminalState {
	case LeaseStateCompleted:
		if projection.CancelReason != "" {
			return fmt.Errorf("dispatch: completed Attempt projection carries cancelReason")
		}
		return projection.CompletionReason.Validate()
	case LeaseStateCancelled:
		if projection.CompletionReason != "" {
			return fmt.Errorf("dispatch: cancelled Attempt projection carries completionReason")
		}
		return projection.CancelReason.Validate()
	case LeaseStateExpired:
		if projection.CompletionReason != "" || projection.CancelReason != "" {
			return fmt.Errorf("dispatch: expired Attempt projection carries a reason")
		}
		return nil
	default:
		return fmt.Errorf("dispatch: Attempt projection terminalState %q is not terminal", projection.TerminalState)
	}
}

// AttemptEligibilityAuthority verifies that a projection was read from the
// current single Attempt authority. LeaseLedger deliberately cannot implement
// this interface itself: it is only a read-model sink and never decides normal
// attempt eligibility independently.
type AttemptEligibilityAuthority interface {
	VerifyAttemptEligibilityProjection(AttemptEligibilityProjection) error
}

// ProjectAttemptEligibility appends a closed terminal read-model projection.
// The source Attempt authority head is mandatory; eligibility authority
// remains in the Attempt log and this ledger cannot independently choose the
// terminal state or reason.
func (l *LeaseLedger) ProjectAttemptEligibility(authority AttemptEligibilityAuthority, projection AttemptEligibilityProjection) error {
	if err := l.requireBound(); err != nil {
		return err
	}
	if authority == nil {
		return fmt.Errorf("%w: Attempt authority verifier is required", ErrLeaseConflict)
	}
	if err := projection.validateTerminalUnion(); err != nil {
		return err
	}
	if err := requireSHA256Digest("attemptAuthorityHeadDigest", projection.AttemptAuthorityHeadDigest); err != nil {
		return err
	}
	if projection.TerminalGeneration != projection.FromGeneration+1 {
		return fmt.Errorf("dispatch: terminalGeneration must advance fromGeneration exactly once")
	}
	if err := authority.VerifyAttemptEligibilityProjection(projection); err != nil {
		return fmt.Errorf("%w: Attempt authority projection rejected: %v", ErrLeaseConflict, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if current, ok := l.leases[projection.LeaseId]; ok && current.LeaseState.IsTerminal() {
		if current.RunId == projection.RunId && current.AttemptId == projection.AttemptId && current.AllocationId == projection.AllocationId && current.Generation == projection.TerminalGeneration && current.LeaseState == projection.TerminalState && current.CompletionReason == projection.CompletionReason && current.CancelReason == projection.CancelReason && l.terminalAuthorities[projection.LeaseId] == projection.AttemptAuthorityHeadDigest {
			return nil
		}
		return fmt.Errorf("%w: conflicting terminal Attempt authority projection", ErrLeaseConflict)
	}
	current, err := l.currentForTransition(projection.LeaseId, projection.FromGeneration, "projected terminal")
	if err != nil {
		return err
	}
	if current.RunId != projection.RunId || current.AttemptId != projection.AttemptId || current.AllocationId != projection.AllocationId {
		return fmt.Errorf("%w: Attempt authority projection tuple does not match lease", ErrLeaseConflict)
	}
	next, err := current.terminalFromAttemptProjection(projection.TerminalState, projection.CompletionReason, projection.CancelReason)
	if err != nil {
		return err
	}
	if next.Generation != projection.TerminalGeneration {
		return ErrLeaseGenerationConflict
	}
	fact := leaseAttemptTerminalFact{
		FactType: leaseFactTypeAttemptTerminal, Sequence: l.nextSequence,
		LeaseId: projection.LeaseId, RunId: projection.RunId,
		AttemptId: projection.AttemptId, AllocationId: projection.AllocationId,
		FromGeneration: projection.FromGeneration, TerminalGeneration: projection.TerminalGeneration,
		TerminalState: projection.TerminalState, CompletionReason: projection.CompletionReason, CancelReason: projection.CancelReason,
		AttemptAuthorityHeadDigest: projection.AttemptAuthorityHeadDigest,
	}
	if err := l.appendFactLine(&fact); err != nil {
		return err
	}
	l.recordTerminal(next)
	l.terminalAuthorities[next.LeaseId] = projection.AttemptAuthorityHeadDigest
	l.closedAttemptBindings[claimBindingKey(next.RunId, next.AttemptId)] = projection.AttemptAuthorityHeadDigest
	return nil
}

// currentForTransition looks the lease up and enforces the shared
// compare-and-append preconditions of cancel, expire and generation bump:
// the lease must exist, must not be terminal and must carry exactly
// expectedGeneration.
func (l *LeaseLedger) currentForTransition(leaseId string, expectedGeneration int64, operation string) (DispatchLease, error) {
	current, ok := l.leases[leaseId]
	if !ok {
		return DispatchLease{}, fmt.Errorf("%w: %s", ErrUnknownLease, leaseId)
	}
	if current.LeaseState.IsTerminal() {
		return DispatchLease{}, fmt.Errorf("%w: lease %s is already in terminal leaseState %q and can never be %s", ErrLeaseConflict, leaseId, string(current.LeaseState), operation)
	}
	if current.Generation != expectedGeneration {
		return DispatchLease{}, fmt.Errorf("%w: lease %s carries generation %d, not the expected generation %d; stale fencing is rejected fail closed", ErrLeaseGenerationConflict, leaseId, current.Generation, expectedGeneration)
	}
	return current, nil
}

// recordTerminal stores the terminal next-generation snapshot and releases
// the single-active binding so a future attempt can claim anew.
func (l *LeaseLedger) recordTerminal(next DispatchLease) {
	l.leases[next.LeaseId] = next
	delete(l.activeBindings, claimBindingKey(next.RunId, next.AttemptId))
	l.nextSequence++
}

// BumpGeneration performs one atomic compare-and-append generation bump on
// an in-flight lease. The current generation recorded in the ledger must
// equal expectedGeneration exactly; otherwise stale fencing is rejected
// with ErrLeaseGenerationConflict and nothing is appended. On success the
// generation-bumped fact is durably appended, the lease snapshot moves to
// expectedGeneration+1 and the fresh fencingToken is derived
// deterministically from the leaseId and the new generation: no random
// source participates and identical inputs always yield the identical
// token.
func (l *LeaseLedger) BumpGeneration(leaseId string, expectedGeneration int64) (int64, string, error) {
	if err := l.requireBound(); err != nil {
		return 0, "", err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := requireText("leaseId", leaseId); err != nil {
		return 0, "", err
	}
	current, err := l.currentForTransition(leaseId, expectedGeneration, "bumped")
	if err != nil {
		return 0, "", err
	}
	newGeneration := expectedGeneration + 1
	newFencingToken, err := bumpedLeaseFencingToken(leaseId, newGeneration)
	if err != nil {
		return 0, "", err
	}
	next := current
	next.Generation = newGeneration
	next.FencingToken = newFencingToken
	next.LeaseDigest = ""
	leaseDigest, err := next.Digest()
	if err != nil {
		return 0, "", err
	}
	next.LeaseDigest = leaseDigest
	fact := leaseBumpFact{
		FactType:       leaseFactTypeBumped,
		Sequence:       l.nextSequence,
		LeaseId:        leaseId,
		FromGeneration: expectedGeneration,
		ToGeneration:   newGeneration,
		FencingToken:   newFencingToken,
	}
	if err := l.appendFactLine(&fact); err != nil {
		return 0, "", err
	}
	l.leases[leaseId] = next
	l.nextSequence++
	return newGeneration, newFencingToken, nil
}

// bumpedLeaseFencingToken derives the fencing token of a bumped generation
// deterministically as the canonical digest of the leaseId and the new
// generation: identical inputs always yield the identical token and no
// random source participates.
func bumpedLeaseFencingToken(leaseId string, generation int64) (string, error) {
	return canonicalDigestOf(struct {
		LeaseId    string `json:"leaseId"`
		Generation int64  `json:"generation"`
	}{LeaseId: leaseId, Generation: generation})
}

// Current returns the current snapshot, leaseState and generation of the
// lease recorded under leaseId. Unknown leaseIds and memory-only ledgers
// fail closed.
func (l *LeaseLedger) Current(leaseId string) (DispatchLease, LeaseState, int64, error) {
	if err := l.requireBound(); err != nil {
		return DispatchLease{}, "", 0, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	lease, ok := l.leases[leaseId]
	if !ok {
		return DispatchLease{}, "", 0, fmt.Errorf("%w: %s", ErrUnknownLease, leaseId)
	}
	return lease, lease.LeaseState, lease.Generation, nil
}

// ActiveLeases returns a snapshot of every currently non-terminal lease,
// keyed by its (runId, attemptId) binding. The embedded runtime recomposes
// its scope → lease index from this on construction (crash recovery): an
// identical ledger always rebuilds the identical active set, terminally
// cancelled/expired leases are excluded, and a memory-only ledger fails
// closed.
func (l *LeaseLedger) ActiveLeases() (map[string]DispatchLease, error) {
	if err := l.requireBound(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make(map[string]DispatchLease, len(l.activeBindings))
	for binding, leaseId := range l.activeBindings {
		lease, ok := l.leases[leaseId]
		if !ok || lease.LeaseState.IsTerminal() {
			continue
		}
		out[binding] = lease
	}
	return out, nil
}

// recover replays the ledger file (when present) and rebuilds the
// in-memory indexes deterministically: the identical ledger bytes always
// rebuild the identical state. Any malformed, non canonical, conflicting
// or orphan line fails closed; nothing is silently skipped.
func (l *LeaseLedger) recover() error {
	file, err := os.Open(l.ledgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("dispatch: open lease ledger: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := l.applyLedgerLine(line); err != nil {
			return fmt.Errorf("dispatch: lease ledger recovery failed at line %d: %w", lineNumber, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("dispatch: read lease ledger: %w", err)
	}
	return nil
}

// applyLedgerLine validates one ledger line as canonical JSON with a
// well-formed sequence and applies the fact it carries to the in-memory
// indexes.
func (l *LeaseLedger) applyLedgerLine(line []byte) error {
	canonicalized, err := canonical.JSON(line)
	if err != nil {
		return fmt.Errorf("ledger line rejected: %w", err)
	}
	if !bytes.Equal(canonicalized, line) {
		return fmt.Errorf("ledger line is not in canonical form")
	}
	var envelope struct {
		FactType string `json:"factType"`
		Sequence int64  `json:"sequence"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return fmt.Errorf("decode lease ledger fact envelope: %w", err)
	}
	if envelope.Sequence != l.nextSequence {
		return fmt.Errorf("ledger fact sequence %d does not follow the ledger sequence %d: the append-only sequence never skips or repeats", envelope.Sequence, l.nextSequence-1)
	}
	switch envelope.FactType {
	case leaseFactTypeClaimed:
		return l.applyClaimFact(line)
	case leaseFactTypeReservedClaimed:
		return l.applyReservedClaimFact(line)
	case leaseFactTypeCancelled:
		return l.applyCancelFact(line)
	case leaseFactTypeExpired:
		return l.applyExpireFact(line)
	case leaseFactTypeAttemptTerminal:
		return l.applyAttemptTerminalFact(line)
	case leaseFactTypeBumped:
		return l.applyBumpFact(line)
	default:
		return fmt.Errorf("unknown lease ledger factType %q", envelope.FactType)
	}
}

func (l *LeaseLedger) applyAttemptTerminalFact(line []byte) error {
	var fact leaseAttemptTerminalFact
	if err := decodeLeaseFact(line, &fact); err != nil {
		return err
	}
	if err := verifyFactDigest(&fact); err != nil {
		return err
	}
	projection := AttemptEligibilityProjection{TerminalState: fact.TerminalState, CompletionReason: fact.CompletionReason, CancelReason: fact.CancelReason}
	if err := projection.validateTerminalUnion(); err != nil {
		return err
	}
	if err := requireSHA256Digest("attemptAuthorityHeadDigest", fact.AttemptAuthorityHeadDigest); err != nil {
		return err
	}
	if fact.TerminalGeneration != fact.FromGeneration+1 {
		return fmt.Errorf("dispatch: invalid lease-attempt-terminal generation")
	}
	current, ok := l.leases[fact.LeaseId]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownLease, fact.LeaseId)
	}
	if current.RunId != fact.RunId || current.AttemptId != fact.AttemptId || current.AllocationId != fact.AllocationId || current.Generation != fact.FromGeneration {
		return fmt.Errorf("%w: lease-attempt-terminal projection tuple/generation mismatch", ErrLeaseConflict)
	}
	next, err := current.terminalFromAttemptProjection(fact.TerminalState, fact.CompletionReason, fact.CancelReason)
	if err != nil || next.Generation != fact.TerminalGeneration {
		return fmt.Errorf("dispatch: invalid lease-attempt-terminal projection")
	}
	l.recordTerminal(next)
	l.terminalAuthorities[next.LeaseId] = fact.AttemptAuthorityHeadDigest
	l.closedAttemptBindings[claimBindingKey(next.RunId, next.AttemptId)] = fact.AttemptAuthorityHeadDigest
	return nil
}

// decodeLeaseFact strictly decodes one canonical ledger line into fact,
// rejecting unknown fields fail closed.
func decodeLeaseFact(line []byte, fact any) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(fact); err != nil {
		return fmt.Errorf("decode lease ledger fact: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode lease ledger fact: trailing JSON value")
	}
	return nil
}

// verifyFactDigest fails closed unless the digest stored on fact equals the
// canonical content digest of the fact with the digest binding detached.
func verifyFactDigest(fact leaseFact) error {
	stored := fact.factDigest()
	if stored == "" {
		return fmt.Errorf("ledger fact carries no digest binding")
	}
	fact.setFactDigest("")
	computed, err := leaseFactContentDigest(fact)
	fact.setFactDigest(stored)
	if err != nil {
		return err
	}
	if stored != computed {
		return fmt.Errorf("ledger fact digest does not match the canonical content digest")
	}
	return nil
}

// applyClaimFact validates and indexes one lease-claimed fact.
func (l *LeaseLedger) applyClaimFact(line []byte) error {
	var fact leaseClaimFact
	if err := decodeLeaseFact(line, &fact); err != nil {
		return err
	}
	if err := verifyFactDigest(&fact); err != nil {
		return err
	}
	if err := fact.Lease.Validate(); err != nil {
		return fmt.Errorf("lease-claimed fact failed validation: %w", err)
	}
	if fact.Lease.LeaseState.IsTerminal() {
		return fmt.Errorf("lease-claimed fact starts in terminal leaseState %q", string(fact.Lease.LeaseState))
	}
	if err := l.requireClaimable(fact.Lease); err != nil {
		return err
	}
	l.leases[fact.Lease.LeaseId] = fact.Lease
	l.activeBindings[claimBindingKey(fact.Lease.RunId, fact.Lease.AttemptId)] = fact.Lease.LeaseId
	l.nextSequence++
	return nil
}

// applyCancelFact validates and applies one lease-cancelled fact by
// re-deriving the terminal snapshot through the production cancel path.
func (l *LeaseLedger) applyCancelFact(line []byte) error {
	var fact leaseCancelFact
	if err := decodeLeaseFact(line, &fact); err != nil {
		return err
	}
	if err := verifyFactDigest(&fact); err != nil {
		return err
	}
	if err := requireText("lease-cancelled fact leaseId", fact.LeaseId); err != nil {
		return err
	}
	if err := fact.CancelReason.Validate(); err != nil {
		return fmt.Errorf("lease-cancelled fact cancelReason: %w", err)
	}
	current, ok := l.leases[fact.LeaseId]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownLease, fact.LeaseId)
	}
	if current.LeaseState.IsTerminal() {
		return fmt.Errorf("lease %s is already in terminal leaseState %q and can never be cancelled", fact.LeaseId, string(current.LeaseState))
	}
	next, err := current.Cancel(fact.CancelReason)
	if err != nil {
		return fmt.Errorf("lease-cancelled fact rejected: %w", err)
	}
	if fact.Generation != next.Generation {
		return fmt.Errorf("lease-cancelled fact records generation %d but the cancel transition yields generation %d", fact.Generation, next.Generation)
	}
	l.recordTerminal(next)
	return nil
}

// applyExpireFact validates and applies one lease-expired fact by
// re-deriving the terminal snapshot through the production expire path at
// the recorded transition time.
func (l *LeaseLedger) applyExpireFact(line []byte) error {
	var fact leaseExpireFact
	if err := decodeLeaseFact(line, &fact); err != nil {
		return err
	}
	if err := verifyFactDigest(&fact); err != nil {
		return err
	}
	if err := requireText("lease-expired fact leaseId", fact.LeaseId); err != nil {
		return err
	}
	if err := requireRFC3339("lease-expired fact expiredAt", fact.ExpiredAt); err != nil {
		return err
	}
	expiredAt, err := time.Parse(time.RFC3339, fact.ExpiredAt)
	if err != nil {
		return fmt.Errorf("lease-expired fact expiredAt: %w", err)
	}
	current, ok := l.leases[fact.LeaseId]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownLease, fact.LeaseId)
	}
	if current.LeaseState.IsTerminal() {
		return fmt.Errorf("lease %s is already in terminal leaseState %q and can never be expired", fact.LeaseId, string(current.LeaseState))
	}
	next, err := current.Expire(expiredAt)
	if err != nil {
		return fmt.Errorf("lease-expired fact rejected: %w", err)
	}
	if fact.Generation != next.Generation {
		return fmt.Errorf("lease-expired fact records generation %d but the expire transition yields generation %d", fact.Generation, next.Generation)
	}
	l.recordTerminal(next)
	return nil
}

// applyBumpFact validates and applies one generation-bumped fact: the bump
// must advance the recorded generation by exactly one and the fencingToken
// must equal the deterministic derivation for the new generation.
func (l *LeaseLedger) applyBumpFact(line []byte) error {
	var fact leaseBumpFact
	if err := decodeLeaseFact(line, &fact); err != nil {
		return err
	}
	if err := verifyFactDigest(&fact); err != nil {
		return err
	}
	if err := requireText("generation-bumped fact leaseId", fact.LeaseId); err != nil {
		return err
	}
	if fact.FromGeneration < 1 {
		return fmt.Errorf("generation-bumped fact fromGeneration must be a positive integer")
	}
	if fact.ToGeneration != fact.FromGeneration+1 {
		return fmt.Errorf("generation-bumped fact toGeneration %d does not bump fromGeneration %d by exactly one", fact.ToGeneration, fact.FromGeneration)
	}
	current, ok := l.leases[fact.LeaseId]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownLease, fact.LeaseId)
	}
	if current.LeaseState.IsTerminal() {
		return fmt.Errorf("lease %s is already in terminal leaseState %q and can never be bumped", fact.LeaseId, string(current.LeaseState))
	}
	if current.Generation != fact.FromGeneration {
		return fmt.Errorf("%w: lease %s carries generation %d, not the recorded fromGeneration %d", ErrLeaseGenerationConflict, fact.LeaseId, current.Generation, fact.FromGeneration)
	}
	expectedToken, err := bumpedLeaseFencingToken(fact.LeaseId, fact.ToGeneration)
	if err != nil {
		return err
	}
	if fact.FencingToken != expectedToken {
		return fmt.Errorf("generation-bumped fact fencingToken does not match the deterministic derivation for generation %d", fact.ToGeneration)
	}
	next := current
	next.Generation = fact.ToGeneration
	next.FencingToken = fact.FencingToken
	next.LeaseDigest = ""
	leaseDigest, err := next.Digest()
	if err != nil {
		return err
	}
	next.LeaseDigest = leaseDigest
	l.leases[fact.LeaseId] = next
	l.nextSequence++
	return nil
}
