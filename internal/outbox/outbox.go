// Package outbox implements the atomic command outbox for I186-R2-C
// (ADR 0044 decision 5): authority fact append and durable command outbox
// entry commit converge into a single atomic transaction, with deterministic
// crash injection at three windows and binary recovery.
package outbox

import (
	"errors"
	"fmt"

	"github.com/chiga0/marshal-harness/internal/engine"
)

// Sentinel errors. All violations fail closed.
var (
	ErrCrashInjected       = errors.New("outbox: crash injected")
	ErrRequestInvalid      = errors.New("outbox: invalid request")
	ErrUnknownCommand      = errors.New("outbox: unknown commandId")
	ErrIdempotencyConflict = errors.New("outbox: idempotency key conflict")
	ErrNotDispatched       = errors.New("outbox: command not yet dispatched")
)

// CrashPoint is the closed enumeration of deterministic crash injection
// windows. No other values are admitted.
type CrashPoint string

const (
	// CrashPointCommit crashes between fact write and outbox persistence:
	// the atomic transaction aborts, neither fact nor entry is visible.
	CrashPointCommit CrashPoint = "commit"

	// CrashPointDispatch crashes after outbox persistence but before the
	// dispatch mark: fact and entry are visible, dispatch state is not.
	CrashPointDispatch CrashPoint = "dispatch"

	// CrashPointResult crashes after result acceptance accounting but
	// before the ack: result is not recorded.
	CrashPointResult CrashPoint = "result"
)

// Validate rejects every value outside the closed enumeration.
func (cp CrashPoint) Validate() error {
	switch cp {
	case CrashPointCommit, CrashPointDispatch, CrashPointResult:
		return nil
	default:
		return fmt.Errorf("outbox: unknown crash point %q", string(cp))
	}
}

// Request is the input for an atomic outbox commit.
type Request struct {
	IdempotencyKey string
	RequestDigest  string
	Kind           engine.CommandKind
	FactDigest     string
}

// Validate fails closed on any missing or malformed field.
func (r Request) Validate() error {
	if r.IdempotencyKey == "" {
		return fmt.Errorf("%w: idempotencyKey must not be empty", ErrRequestInvalid)
	}
	if err := requireDigest("requestDigest", r.RequestDigest); err != nil {
		return fmt.Errorf("%w: %v", ErrRequestInvalid, err)
	}
	if err := r.Kind.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrRequestInvalid, err)
	}
	if err := requireDigest("factDigest", r.FactDigest); err != nil {
		return fmt.Errorf("%w: %v", ErrRequestInvalid, err)
	}
	return nil
}

// Receipt is the output of a successful atomic commit.
type Receipt struct {
	Sequence      int64
	CommandId     string
	FactDigest    string
	RequestDigest string
}

// RecoveryReport is the output of Recover.
type RecoveryReport struct {
	Sequence        int64
	FactCount       int
	EntryCount      int
	DispatchedCount int
	ResultCount     int
	PendingDispatch []string
	PendingResult   []string
}

// Option configures an Outbox.
type Option func(*Outbox)

// WithCrashPoint installs a deterministic crash injection point.
// When set, the corresponding operation returns ErrCrashInjected instead
// of performing the state change.
func WithCrashPoint(cp CrashPoint) Option {
	return func(o *Outbox) { o.crashPoint = cp }
}

// factRecord is a persisted authority fact.
type factRecord struct {
	sequence   int64
	factDigest string
}

// outboxEntry is a persisted command outbox entry.
type outboxEntry struct {
	commandId      string
	idempotencyKey string
	requestDigest  string
	kind           engine.CommandKind
	factDigest     string
	sequence       int64
}

// Outbox is the in-memory atomic outbox. All state is in-process; no real
// disk or network I/O.
type Outbox struct {
	sequence   int64
	facts      map[string]factRecord
	entries    map[string]outboxEntry
	idempotent map[string]string
	dispatched map[string]bool
	results    map[string]string
	crashPoint CrashPoint
}

// New creates an empty Outbox with the given options.
func New(opts ...Option) *Outbox {
	o := &Outbox{
		facts:      make(map[string]factRecord),
		entries:    make(map[string]outboxEntry),
		idempotent: make(map[string]string),
		dispatched: make(map[string]bool),
		results:    make(map[string]string),
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Commit atomically appends the authority fact and the outbox entry.
// Both are visible together or neither is visible.
// Idempotent replay: same idempotencyKey + same requestDigest returns
// the existing receipt. Different requestDigest fails closed.
func (o *Outbox) Commit(req Request) (Receipt, error) {
	if err := req.Validate(); err != nil {
		return Receipt{}, err
	}

	commandId, err := engine.DeriveCommandId(req.FactDigest, req.Kind)
	if err != nil {
		return Receipt{}, fmt.Errorf("outbox: derive commandId: %w", err)
	}

	if existingCmdId, exists := o.idempotent[req.IdempotencyKey]; exists {
		existing := o.entries[existingCmdId]
		if existing.requestDigest == req.RequestDigest {
			return Receipt{
				Sequence:      existing.sequence,
				CommandId:     existing.commandId,
				FactDigest:    existing.factDigest,
				RequestDigest: existing.requestDigest,
			}, nil
		}
		return Receipt{}, fmt.Errorf("%w: idempotencyKey %q reused with different digest",
			ErrIdempotencyConflict, req.IdempotencyKey)
	}

	if o.crashPoint == CrashPointCommit {
		return Receipt{}, fmt.Errorf("%w: at commit", ErrCrashInjected)
	}

	o.sequence++
	o.facts[req.FactDigest] = factRecord{sequence: o.sequence, factDigest: req.FactDigest}
	o.entries[commandId] = outboxEntry{
		commandId:      commandId,
		idempotencyKey: req.IdempotencyKey,
		requestDigest:  req.RequestDigest,
		kind:           req.Kind,
		factDigest:     req.FactDigest,
		sequence:       o.sequence,
	}
	o.idempotent[req.IdempotencyKey] = commandId

	return Receipt{
		Sequence:      o.sequence,
		CommandId:     commandId,
		FactDigest:    req.FactDigest,
		RequestDigest: req.RequestDigest,
	}, nil
}

// Dispatch marks an outbox entry as dispatched.
// Idempotent: already-dispatched entries return (false, nil).
func (o *Outbox) Dispatch(commandId string) (bool, error) {
	if commandId == "" {
		return false, fmt.Errorf("%w: commandId must not be empty", ErrRequestInvalid)
	}
	if _, exists := o.entries[commandId]; !exists {
		return false, fmt.Errorf("%w: %s", ErrUnknownCommand, commandId)
	}
	if o.dispatched[commandId] {
		return false, nil
	}
	if o.crashPoint == CrashPointDispatch {
		return false, fmt.Errorf("%w: at dispatch", ErrCrashInjected)
	}
	o.dispatched[commandId] = true
	return true, nil
}

// RecordResult records a result acceptance for a dispatched command.
// Idempotent: same commandId + same resultDigest returns (true, nil).
// Different resultDigest fails closed. Undispatched commands fail closed.
func (o *Outbox) RecordResult(commandId, resultDigest string) (bool, error) {
	if commandId == "" {
		return false, fmt.Errorf("%w: commandId must not be empty", ErrRequestInvalid)
	}
	if err := requireDigest("resultDigest", resultDigest); err != nil {
		return false, fmt.Errorf("%w: %v", ErrRequestInvalid, err)
	}
	if _, exists := o.entries[commandId]; !exists {
		return false, fmt.Errorf("%w: %s", ErrUnknownCommand, commandId)
	}
	if !o.dispatched[commandId] {
		return false, fmt.Errorf("%w: %s", ErrNotDispatched, commandId)
	}
	if existing, recorded := o.results[commandId]; recorded {
		if existing == resultDigest {
			return true, nil
		}
		return false, fmt.Errorf("%w: commandId %s already has result with different digest",
			ErrIdempotencyConflict, commandId)
	}
	if o.crashPoint == CrashPointResult {
		return false, fmt.Errorf("%w: at result", ErrCrashInjected)
	}
	o.results[commandId] = resultDigest
	return false, nil
}

// Recover rebuilds the recovery projection from persisted state.
// Conclusion is binary: every entry is either committed (possibly with
// idempotent replay) or uncommitted (safe to redeliver).
func (o *Outbox) Recover() (RecoveryReport, error) {
	var pendingDispatch, pendingResult []string
	for id := range o.entries {
		if !o.dispatched[id] {
			pendingDispatch = append(pendingDispatch, id)
		} else if _, has := o.results[id]; !has {
			pendingResult = append(pendingResult, id)
		}
	}
	return RecoveryReport{
		Sequence:        o.sequence,
		FactCount:       len(o.facts),
		EntryCount:      len(o.entries),
		DispatchedCount: len(o.dispatched),
		ResultCount:     len(o.results),
		PendingDispatch: pendingDispatch,
		PendingResult:   pendingResult,
	}, nil
}

// LedgerSequence returns the current monotonic sequence counter.
func (o *Outbox) LedgerSequence() int64 { return o.sequence }

// FactCount returns the number of persisted facts.
func (o *Outbox) FactCount() int { return len(o.facts) }

// EntryCount returns the number of persisted outbox entries.
func (o *Outbox) EntryCount() int { return len(o.entries) }

// DispatchCount returns the number of dispatched entries.
func (o *Outbox) DispatchCount() int { return len(o.dispatched) }

// ResultCount returns the number of recorded results.
func (o *Outbox) ResultCount() int { return len(o.results) }

// Entry returns the outbox entry for commandId.
func (o *Outbox) Entry(commandId string) (outboxEntry, bool) {
	e, ok := o.entries[commandId]
	return e, ok
}

// IsDispatched reports whether commandId has been dispatched.
func (o *Outbox) IsDispatched(commandId string) bool { return o.dispatched[commandId] }

// Result returns the recorded result digest for commandId.
func (o *Outbox) Result(commandId string) (string, bool) {
	r, ok := o.results[commandId]
	return r, ok
}

// CrashPointConfig returns the configured crash point.
func (o *Outbox) CrashPointConfig() CrashPoint { return o.crashPoint }

// setCrashPoint replaces the crash injection point on an existing outbox
// without losing persisted state. Internal: used by tests only.
func (o *Outbox) setCrashPoint(cp CrashPoint) { o.crashPoint = cp }

// requireDigest fails closed unless the value is a full lowercase hex
// sha256 digest with the sha256: prefix.
func requireDigest(field, value string) error {
	const prefix = "sha256:"
	if value == "" {
		return fmt.Errorf("outbox: %s must not be empty", field)
	}
	if len(value) < len(prefix) || value[:len(prefix)] != prefix {
		return fmt.Errorf("outbox: %s must carry the sha256: prefix", field)
	}
	hex := value[len(prefix):]
	if len(hex) != 64 {
		return fmt.Errorf("outbox: %s must be a 64-character sha256 hex digest", field)
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("outbox: %s must be lowercase hex", field)
		}
	}
	return nil
}
