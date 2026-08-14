package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

// LedgerFact is the read-only view of one authority ledger fact the journal
// derives from: the monotonic ledger sequence, the canonical content digest
// of the fact and the content digest of the externalized payload bound to
// the fact. The engine package never writes the ledger; Core supplies these
// read-only views after committing the fact, and every command-generating
// fact binds its externalized payload digest.
type LedgerFact struct {
	Sequence      int64  `json:"sequence"`
	FactDigest    string `json:"factDigest"`
	PayloadDigest string `json:"payloadDigest"`
}

// Validate fails closed on a non-positive sequence or any malformed digest.
func (fact LedgerFact) Validate() error {
	if fact.Sequence < 1 {
		return fmt.Errorf("engine: ledgerFact.sequence must be a positive integer")
	}
	if err := requireDigest("ledgerFact.factDigest", fact.FactDigest); err != nil {
		return err
	}
	return requireDigest("ledgerFact.payloadDigest", fact.PayloadDigest)
}

// DeriveCommandId stably derives the commandId from the ledger fact digest
// and the closed command kind (ADR 0018 §15): identical inputs always yield
// the identical commandId, distinct facts or distinct kinds yield distinct
// commandIds, and no random source or clock read participates. Derivation
// never involves backend identity, worker build or workflow state, so
// upgrades and backend replacement never change command identity.
func DeriveCommandId(factDigest string, kind CommandKind) (string, error) {
	if err := requireDigest("ledgerFact.factDigest", factDigest); err != nil {
		return "", err
	}
	if err := kind.Validate(); err != nil {
		return "", err
	}
	return canonicalDigestOf(struct {
		FactDigest string      `json:"factDigest"`
		Kind       CommandKind `json:"kind"`
	}{FactDigest: factDigest, Kind: kind})
}

// DispatchPayload is the externalized payload of a dispatch command: the
// execution target identity and the digest of the workload reference.
type DispatchPayload struct {
	Target         string `json:"target"`
	WorkloadDigest string `json:"workloadDigest"`
}

// Validate fails closed on a missing target or a malformed workload digest.
func (payload DispatchPayload) Validate() error {
	if err := requireText("dispatchPayload.target", payload.Target); err != nil {
		return err
	}
	return requireDigest("dispatchPayload.workloadDigest", payload.WorkloadDigest)
}

// TimerPayload is the externalized payload of a timer command: the wakeup
// target identity and the RFC 3339 fire time the backend must honor.
type TimerPayload struct {
	Target string `json:"target"`
	FireAt string `json:"fireAt"`
}

// Validate fails closed on a missing target or a malformed fire time.
func (payload TimerPayload) Validate() error {
	if err := requireText("timerPayload.target", payload.Target); err != nil {
		return err
	}
	return requireRFC3339("timerPayload.fireAt", payload.FireAt)
}

// SignalPayload is the externalized payload of a signal command: the target
// identity, the closed signal name and the signal body.
type SignalPayload struct {
	Target string `json:"target"`
	Name   string `json:"name"`
	Body   string `json:"body"`
}

// Validate fails closed on a missing target or name. An empty body is a
// legitimate pure notification.
func (payload SignalPayload) Validate() error {
	if err := requireText("signalPayload.target", payload.Target); err != nil {
		return err
	}
	return requireText("signalPayload.name", payload.Name)
}

// SideEffectPayload is the externalized payload of a side-effect command,
// shaped after the authority SideEffectIntent identity fields: the effect
// identity, the owning Port, the operation and the target reference.
type SideEffectPayload struct {
	EffectId  string `json:"effectId"`
	Port      string `json:"port"`
	Operation string `json:"operation"`
	TargetRef string `json:"targetRef"`
}

// Validate fails closed on any empty identity field.
func (payload SideEffectPayload) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"sideEffectPayload.effectId", payload.EffectId},
		{"sideEffectPayload.port", payload.Port},
		{"sideEffectPayload.operation", payload.Operation},
		{"sideEffectPayload.targetRef", payload.TargetRef},
	} {
		if err := requireText(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

// EncodePayload canonicalizes one payload value under RFC 8785 JCS and
// returns the canonical bytes together with the payload digest the command
// payloadRef must carry. Core binds the returned digest into the authority
// ledger fact before deriving the command.
func EncodePayload(value any) ([]byte, string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, "", fmt.Errorf("engine: marshal payload: %w", err)
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return nil, "", fmt.Errorf("%w: payload is not canonical JSON", ErrPayloadRejected)
	}
	return canonicalized, canonical.DigestBytes(canonicalized), nil
}

// VerifyPayloadRef fails closed unless the payload bytes match the
// payloadRef digest: digest-verified payload resolution, never a blind
// fetch.
func VerifyPayloadRef(payload []byte, payloadRef string) error {
	if err := requireDigest("payloadRef", payloadRef); err != nil {
		return err
	}
	if canonical.DigestBytes(payload) != payloadRef {
		return fmt.Errorf("%w: payload bytes do not match the payloadRef digest", ErrPayloadRejected)
	}
	return nil
}

func decodeStrictPayload(payload []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: decode payload: %v", ErrPayloadRejected, err)
	}
	return nil
}

// DecodeDispatchPayload strictly decodes and validates one dispatch payload.
func DecodeDispatchPayload(payload []byte) (DispatchPayload, error) {
	var decoded DispatchPayload
	if err := decodeStrictPayload(payload, &decoded); err != nil {
		return DispatchPayload{}, err
	}
	if err := decoded.Validate(); err != nil {
		return DispatchPayload{}, err
	}
	return decoded, nil
}

// DecodeTimerPayload strictly decodes and validates one timer payload.
func DecodeTimerPayload(payload []byte) (TimerPayload, error) {
	var decoded TimerPayload
	if err := decodeStrictPayload(payload, &decoded); err != nil {
		return TimerPayload{}, err
	}
	if err := decoded.Validate(); err != nil {
		return TimerPayload{}, err
	}
	return decoded, nil
}

// DecodeSignalPayload strictly decodes and validates one signal payload.
func DecodeSignalPayload(payload []byte) (SignalPayload, error) {
	var decoded SignalPayload
	if err := decodeStrictPayload(payload, &decoded); err != nil {
		return SignalPayload{}, err
	}
	if err := decoded.Validate(); err != nil {
		return SignalPayload{}, err
	}
	return decoded, nil
}

// DecodeSideEffectPayload strictly decodes and validates one side-effect
// payload.
func DecodeSideEffectPayload(payload []byte) (SideEffectPayload, error) {
	var decoded SideEffectPayload
	if err := decodeStrictPayload(payload, &decoded); err != nil {
		return SideEffectPayload{}, err
	}
	if err := decoded.Validate(); err != nil {
		return SideEffectPayload{}, err
	}
	return decoded, nil
}

// journalEntry is one derived command together with its derivation lineage.
type journalEntry struct {
	command              Command
	factDigest           string
	ledgerSequence       int64
	duplicateDerivations int64
}

// CommandJournal is the ledger-derived Core command journal: the single
// authoritative outbound projection of the seam (ADR 0018 §15). Every
// command is deterministically derived from one committed authority ledger
// fact; every receipt merges idempotently under the commandId. Replaying
// the identical ordered ledger fact stream always rebuilds the identical
// journal state, and duplicate derivation or duplicate delivery of the
// identical commandId merges without divergence. The journal carries no
// business lifecycle state: pending/delivered are transport projections
// only, and Core lifecycle, ReviewDecision, rework and terminal authority
// never enter it.
type CommandJournal struct {
	mu                   sync.Mutex
	authorityNamespaceId authority.AuthorityNamespaceId
	entries              map[string]*journalEntry
	order                []string
	receipts             map[string]Receipt
	duplicateDeliveries  map[string]int64
}

// NewCommandJournal constructs an empty journal owned by the authority
// namespace.
func NewCommandJournal(authorityNamespaceId authority.AuthorityNamespaceId) (*CommandJournal, error) {
	if err := authorityNamespaceId.Validate(); err != nil {
		return nil, err
	}
	return &CommandJournal{
		authorityNamespaceId: authorityNamespaceId,
		entries:              map[string]*journalEntry{},
		receipts:             map[string]Receipt{},
		duplicateDeliveries:  map[string]int64{},
	}, nil
}

// AuthorityNamespaceId returns the authority namespace owning the journal.
func (journal *CommandJournal) AuthorityNamespaceId() authority.AuthorityNamespaceId {
	return journal.authorityNamespaceId
}

// Derive derives one command from a committed authority ledger fact and
// registers it as pending. Replaying the identical fact and kind merges
// idempotently onto the identical commandId; any divergent derivation
// colliding on a commandId fails closed.
func (journal *CommandJournal) Derive(fact LedgerFact, kind CommandKind) (Command, error) {
	if err := fact.Validate(); err != nil {
		return Command{}, err
	}
	if err := kind.Validate(); err != nil {
		return Command{}, err
	}
	commandId, err := DeriveCommandId(fact.FactDigest, kind)
	if err != nil {
		return Command{}, err
	}
	command := Command{CommandId: commandId, Kind: kind, PayloadRef: fact.PayloadDigest}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if existing, known := journal.entries[commandId]; known {
		if existing.factDigest != fact.FactDigest || existing.ledgerSequence != fact.Sequence || !existing.command.Equal(command) {
			return Command{}, fmt.Errorf("%w: commandId %s is already derived from a different ledger fact; the journal never merges divergent derivations", ErrJournalConflict, commandId)
		}
		existing.duplicateDerivations++
		return existing.command, nil
	}
	journal.entries[commandId] = &journalEntry{
		command:        command,
		factDigest:     fact.FactDigest,
		ledgerSequence: fact.Sequence,
	}
	journal.order = append(journal.order, commandId)
	return command, nil
}

// ConsolidateReceipt merges one receipt under its commandId idempotently:
// the first receipt is the authoritative delivery record, every later
// receipt for the identical commandId counts as a duplicate delivery and
// never diverges. A receipt for a commandId the journal never derived fails
// closed.
func (journal *CommandJournal) ConsolidateReceipt(receipt Receipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if _, known := journal.entries[receipt.CommandId]; !known {
		return fmt.Errorf("%w: receipt references commandId %s that was never derived from the authority ledger", ErrUnknownCommand, receipt.CommandId)
	}
	if _, delivered := journal.receipts[receipt.CommandId]; delivered {
		journal.duplicateDeliveries[receipt.CommandId]++
		return nil
	}
	journal.receipts[receipt.CommandId] = receipt
	return nil
}

// Command returns the derived command recorded under commandId.
func (journal *CommandJournal) Command(commandId string) (Command, bool) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	entry, known := journal.entries[commandId]
	if !known {
		return Command{}, false
	}
	return entry.command, true
}

// Commands returns every derived command in deterministic derivation order.
func (journal *CommandJournal) Commands() []Command {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	commands := make([]Command, 0, len(journal.order))
	for _, commandId := range journal.order {
		commands = append(commands, journal.entries[commandId].command)
	}
	return commands
}

// Pending returns every derived command without a consolidated receipt, in
// deterministic derivation order: these are the commands crash/upgrade
// recovery must redeliver through the single seam.
func (journal *CommandJournal) Pending() []Command {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	pending := make([]Command, 0, len(journal.order))
	for _, commandId := range journal.order {
		if _, delivered := journal.receipts[commandId]; delivered {
			continue
		}
		pending = append(pending, journal.entries[commandId].command)
	}
	return pending
}

// ReceiptFor returns the authoritative first receipt consolidated under
// commandId.
func (journal *CommandJournal) ReceiptFor(commandId string) (Receipt, bool) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	receipt, delivered := journal.receipts[commandId]
	return receipt, delivered
}

// DuplicateDerivations counts how many times the identical ledger fact and
// kind were replayed through Derive for commandId beyond the first
// derivation.
func (journal *CommandJournal) DuplicateDerivations(commandId string) int64 {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	entry, known := journal.entries[commandId]
	if !known {
		return 0
	}
	return entry.duplicateDerivations
}

// DuplicateDeliveries counts how many receipts for commandId merged beyond
// the authoritative first receipt.
func (journal *CommandJournal) DuplicateDeliveries(commandId string) int64 {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.duplicateDeliveries[commandId]
}
