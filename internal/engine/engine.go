package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

// digestPrefix prefixes every canonical content digest used by the seam.
const digestPrefix = "sha256:"

// Sentinel errors of the single authority seam. All seam violations fail
// closed through these sentinels so callers can adjudicate with errors.Is.
var (
	// ErrCommandNotDerived rejects any delivery of a command that was never
	// derived from a committed authority ledger fact: the "command delivered
	// but ledger not committed" state is unreachable by construction.
	ErrCommandNotDerived = errors.New("engine: command was never derived from the authority ledger")

	// ErrReceiptDivergence rejects a backend receipt that does not reference
	// the exact command the seam delivered.
	ErrReceiptDivergence = errors.New("engine: backend receipt diverges from the delivered command")

	// ErrBackendAuthorityViolation rejects any backend statement that
	// announces Core business authority: lifecycle transitions,
	// ReviewDecisions, rework, terminal states or safe-to-publish.
	ErrBackendAuthorityViolation = errors.New("engine: backend announced a Core business authority claim")

	// ErrBackendNotRecovered rejects backend operations before the backend
	// rebuilt its transport state after construction, crash or restart.
	ErrBackendNotRecovered = errors.New("engine: backend transport state has not been recovered")

	// ErrJournalConflict rejects divergent derivations or receipts inside
	// the ledger-derived command journal.
	ErrJournalConflict = errors.New("engine: command journal conflict")

	// ErrUnknownCommand rejects receipts or statements referencing a
	// commandId the journal never derived.
	ErrUnknownCommand = errors.New("engine: unknown commandId")

	// ErrPayloadRejected rejects unavailable, mismatched or malformed
	// externalized payloads.
	ErrPayloadRejected = errors.New("engine: payload rejected")
)

// CommandKind is the closed enumeration of command kinds carried by the
// single authority seam. Matching is case sensitive.
type CommandKind string

// Closed command kinds of the seam.
const (
	CommandKindDispatch   CommandKind = "dispatch"
	CommandKindSignal     CommandKind = "signal"
	CommandKindTimer      CommandKind = "timer"
	CommandKindSideEffect CommandKind = "side-effect"
)

// Validate rejects every value outside the closed enumeration.
func (kind CommandKind) Validate() error {
	switch kind {
	case CommandKindDispatch, CommandKindSignal, CommandKindTimer, CommandKindSideEffect:
		return nil
	default:
		return fmt.Errorf("engine: unknown command kind %q", string(kind))
	}
}

// Command is the unit a backend consumes (ADR 0018 §15): the commandId is
// stably derived from the authority ledger fact digest, the kind is the
// closed command kind and the payloadRef is the sha256 digest of the
// externalized payload bytes. Commands never carry payload bytes inline.
type Command struct {
	CommandId  string      `json:"commandId"`
	Kind       CommandKind `json:"kind"`
	PayloadRef string      `json:"payloadRef"`
}

// Validate fails closed on any missing or malformed field.
func (command Command) Validate() error {
	if err := requireDigest("command.commandId", command.CommandId); err != nil {
		return err
	}
	if err := command.Kind.Validate(); err != nil {
		return err
	}
	return requireDigest("command.payloadRef", command.PayloadRef)
}

// Equal reports whether both commands carry identical field values.
func (command Command) Equal(other Command) bool {
	return command == other
}

// Receipt is the transport-level report a backend returns for a consumed
// command (ADR 0018 §15). deliveredAt is the authoritative first-delivery
// time; attemptSeq is the backend delivery attempt sequence — a transport
// counter only, never a business Attempt, and delivery retry never consumes
// a business retry or rework budget.
type Receipt struct {
	CommandId   string `json:"commandId"`
	DeliveredAt string `json:"deliveredAt"`
	AttemptSeq  int64  `json:"attemptSeq"`
}

// Validate fails closed on any missing or malformed field.
func (receipt Receipt) Validate() error {
	if err := requireDigest("receipt.commandId", receipt.CommandId); err != nil {
		return err
	}
	if err := requireRFC3339("receipt.deliveredAt", receipt.DeliveredAt); err != nil {
		return err
	}
	if receipt.AttemptSeq < 1 {
		return fmt.Errorf("engine: receipt.attemptSeq must be a positive integer")
	}
	return nil
}

// Backend is the durable execution engine backend contract (ADR 0016 §5,
// ADR 0018 §15, ADR 0019 §1). A backend only consumes commands and reports
// receipts; it carries at-least-once delivery of the identical commandId,
// timer wakeup, signal transport and crash recovery. Workflow and activity
// state inside a backend is never business authority: backends never
// announce lifecycle transitions, ReviewDecisions, rework, terminal states
// or safe-to-publish, and replacing the backend never changes lifecycle
// semantics.
type Backend interface {
	// Deliver consumes one command and reports the transport receipt.
	// Duplicate delivery of the identical commandId must merge idempotently
	// and never re-execute command effects.
	Deliver(ctx context.Context, command Command) (Receipt, error)

	// Recover rebuilds the backend transport state after construction,
	// crash or restart. Crash recovery of the business command set replays
	// through the single seam (journal re-derivation) and never depends on
	// backend internal state.
	Recover(ctx context.Context) error

	// Close releases backend resources.
	Close() error
}

// PayloadStore resolves externalized payload bytes by their content digest.
// Payloads live outside the command; the command only carries the payloadRef
// digest (ADR 0018 §15 payload externalization).
type PayloadStore interface {
	Payload(ctx context.Context, digest string) ([]byte, error)
}

// BusinessClaim is the closed enumeration of the Core business authority
// claims a backend is forbidden to announce. Matching is case sensitive.
type BusinessClaim string

// Closed business authority claims; every member is Core authority and a
// backend announcing any of them fails closed at the seam.
const (
	BusinessClaimLifecycleTransition BusinessClaim = "lifecycle-transition"
	BusinessClaimReviewDecision      BusinessClaim = "review-decision"
	BusinessClaimRework              BusinessClaim = "rework"
	BusinessClaimTerminalState       BusinessClaim = "terminal-state"
	BusinessClaimSafeToPublish       BusinessClaim = "safe-to-publish"
)

// Validate rejects every value outside the closed enumeration.
func (claim BusinessClaim) Validate() error {
	switch claim {
	case BusinessClaimLifecycleTransition, BusinessClaimReviewDecision,
		BusinessClaimRework, BusinessClaimTerminalState, BusinessClaimSafeToPublish:
		return nil
	default:
		return fmt.Errorf("engine: unknown business claim %q", string(claim))
	}
}

// BackendStatement is any statement a backend makes about a command beyond
// the transport receipt. The seam admits only delivery-shaped statements:
// any business authority claim fails closed (ADR 0018 §15, ADR 0019 §1).
type BackendStatement struct {
	CommandId string          `json:"commandId"`
	Claims    []BusinessClaim `json:"claims"`
}

// Validate fails closed on a malformed commandId or any claim outside the
// closed enumeration. Shape validity does not admit business claims:
// DurableExecutionEngine.AcceptBackendStatement rejects every non-empty
// claim set fail closed.
func (statement BackendStatement) Validate() error {
	if err := requireDigest("backendStatement.commandId", statement.CommandId); err != nil {
		return err
	}
	for index, claim := range statement.Claims {
		if err := claim.Validate(); err != nil {
			return fmt.Errorf("engine: backendStatement.claims[%d]: %w", index, err)
		}
	}
	return nil
}

// DurableExecutionEngine is the single authority seam between Core and one
// durable execution backend (ADR 0018 §15). Commands enter the seam only
// through ledger-derived journal derivation; receipts merge idempotently;
// crash/upgrade recovery re-derives every undelivered command from the
// ledger facts and never depends on backend internal state.
type DurableExecutionEngine struct {
	authorityNamespaceId authority.AuthorityNamespaceId
	journal              *CommandJournal
	backend              Backend
}

// New binds the engine to one authority namespace and one backend. The
// namespace must validate and the backend must not be nil; every violation
// fails closed.
func New(authorityNamespaceId authority.AuthorityNamespaceId, backend Backend) (*DurableExecutionEngine, error) {
	if err := authorityNamespaceId.Validate(); err != nil {
		return nil, err
	}
	if backend == nil {
		return nil, fmt.Errorf("engine: backend must not be nil")
	}
	journal, err := NewCommandJournal(authorityNamespaceId)
	if err != nil {
		return nil, err
	}
	return &DurableExecutionEngine{
		authorityNamespaceId: authorityNamespaceId,
		journal:              journal,
		backend:              backend,
	}, nil
}

// AuthorityNamespaceId returns the authority namespace owning the seam.
func (engine *DurableExecutionEngine) AuthorityNamespaceId() authority.AuthorityNamespaceId {
	return engine.authorityNamespaceId
}

// Journal exposes the ledger-derived command journal projection.
func (engine *DurableExecutionEngine) Journal() *CommandJournal {
	return engine.journal
}

// Backend exposes the bound backend.
func (engine *DurableExecutionEngine) Backend() Backend {
	return engine.backend
}

// DeriveCommand is the only way a command enters the seam: the command is
// deterministically derived from one committed authority ledger fact and
// registered in the journal as pending. Deriving the identical fact and
// kind again merges idempotently onto the identical commandId.
func (engine *DurableExecutionEngine) DeriveCommand(fact LedgerFact, kind CommandKind) (Command, error) {
	return engine.journal.Derive(fact, kind)
}

// ConsolidateReceipt merges one receipt into the journal: receipts from
// live delivery and receipts replayed from authority ledger receipt facts
// follow the identical idempotent path.
func (engine *DurableExecutionEngine) ConsolidateReceipt(receipt Receipt) error {
	return engine.journal.ConsolidateReceipt(receipt)
}

// Deliver transports one journal-derived command through the backend. The
// command must be registered in the journal (the seam never delivers a
// command whose ledger fact is not committed); an already-delivered command
// returns the authoritative stored receipt without contacting the backend;
// a divergent or invalid backend receipt fails closed.
func (engine *DurableExecutionEngine) Deliver(ctx context.Context, command Command) (Receipt, error) {
	if err := command.Validate(); err != nil {
		return Receipt{}, err
	}
	if _, known := engine.journal.Command(command.CommandId); !known {
		return Receipt{}, fmt.Errorf("%w: commandId %s must be derived from the authority ledger before delivery", ErrCommandNotDerived, command.CommandId)
	}
	if stored, delivered := engine.journal.ReceiptFor(command.CommandId); delivered {
		return stored, nil
	}
	receipt, err := engine.backend.Deliver(ctx, command)
	if err != nil {
		return Receipt{}, fmt.Errorf("engine: backend delivery failed: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return Receipt{}, fmt.Errorf("engine: backend returned an invalid receipt: %w", err)
	}
	if receipt.CommandId != command.CommandId {
		return Receipt{}, fmt.Errorf("%w: receipt carries commandId %s but the seam delivered commandId %s", ErrReceiptDivergence, receipt.CommandId, command.CommandId)
	}
	if err := engine.journal.ConsolidateReceipt(receipt); err != nil {
		return Receipt{}, err
	}
	stored, _ := engine.journal.ReceiptFor(command.CommandId)
	return stored, nil
}

// RedeliverPending delivers every pending (derived but undelivered) command
// through the backend in deterministic derivation order: this is the
// crash/upgrade recovery redelivery half of the single seam. The derivation
// half replays ledger facts through DeriveCommand/ConsolidateReceipt before
// this call; recovery never consults backend internal state.
func (engine *DurableExecutionEngine) RedeliverPending(ctx context.Context) ([]Receipt, error) {
	pending := engine.journal.Pending()
	receipts := make([]Receipt, 0, len(pending))
	for _, command := range pending {
		receipt, err := engine.Deliver(ctx, command)
		if err != nil {
			return receipts, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

// AcceptBackendStatement is the authority boundary admission point: the
// statement must reference a journal-derived command and must carry no
// business authority claim. Lifecycle transitions, ReviewDecisions, rework,
// terminal states and safe-to-publish are Core authority; a backend
// announcing any of them fails closed.
func (engine *DurableExecutionEngine) AcceptBackendStatement(statement BackendStatement) error {
	if err := statement.Validate(); err != nil {
		return err
	}
	if _, known := engine.journal.Command(statement.CommandId); !known {
		return fmt.Errorf("%w: backend statement references commandId %s that was never derived from the authority ledger", ErrUnknownCommand, statement.CommandId)
	}
	if len(statement.Claims) > 0 {
		return fmt.Errorf("%w: backends only consume commands and report receipts; lifecycle transitions, ReviewDecisions, rework, terminal states and safe-to-publish belong to Core", ErrBackendAuthorityViolation)
	}
	return nil
}

// requireText fails closed on empty or blank values.
func requireText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("engine: %s must be a non-empty string", field)
	}
	return nil
}

// requireDigest fails closed unless the value is a full lowercase hex
// sha256 digest with the sha256: prefix.
func requireDigest(field, value string) error {
	if err := requireText(field, value); err != nil {
		return err
	}
	if !strings.HasPrefix(value, digestPrefix) {
		return fmt.Errorf("engine: %s must carry the %s digest prefix", field, digestPrefix)
	}
	hexPart := strings.TrimPrefix(value, digestPrefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("engine: %s must be a 64 character sha256 hex digest", field)
	}
	for _, r := range hexPart {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("engine: %s must be lowercase hex", field)
		}
	}
	return nil
}

// requireRFC3339 fails closed unless the value parses as RFC 3339.
func requireRFC3339(field, value string) error {
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("engine: %s must be an RFC 3339 timestamp", field)
	}
	return nil
}

// canonicalDigestOf marshals value, canonicalizes it under RFC 8785 JCS and
// returns the sha256 digest of the canonical bytes. Member order in the
// input never changes the digest and no random source participates.
func canonicalDigestOf(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("engine: canonical marshal: %w", err)
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return "", fmt.Errorf("engine: canonical digest: %w", err)
	}
	return canonical.DigestBytes(canonicalized), nil
}
