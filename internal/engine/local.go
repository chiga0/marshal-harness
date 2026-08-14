package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// localStateFileName is the single append-only state file the Local Engine
// backend keeps inside the construction-time stateRoot; every line carries
// exactly one canonical JSON fact.
const localStateFileName = "local-engine.jsonl"

// defaultLocalTickInterval is the scheduler resolution for timer wakeups.
const defaultLocalTickInterval = 20 * time.Millisecond

// localWakeupBuffer bounds the in-memory wakeup notification queue.
const localWakeupBuffer = 256

// Closed fact types of the Local Engine append-only state file. Matching is
// case sensitive.
const (
	localFactCommandConsumed  = "command-consumed"
	localFactDeliveryRecorded = "delivery-recorded"
	localFactTimerArmed       = "timer-armed"
	localFactTimerFired       = "timer-fired"
	localFactSignalDelivered  = "signal-delivered"
)

// ErrLocalBackendClosed rejects operations on a closed or killed Local
// Engine backend.
var ErrLocalBackendClosed = errors.New("engine: local backend is closed")

// ErrLocalStateConflict rejects divergent or duplicate facts during state
// replay or delivery.
var ErrLocalStateConflict = errors.New("engine: local backend state conflict")

// localConsumedFact records one consumed command: the complete command and
// the consumption time.
type localConsumedFact struct {
	FactType   string  `json:"factType"`
	Sequence   int64   `json:"sequence"`
	Command    Command `json:"command"`
	ConsumedAt string  `json:"consumedAt"`
	Digest     string  `json:"digest"`
}

// localDeliveryFact records one delivery receipt: the authoritative
// deliveredAt is carried by every attempt so duplicate deliveries never
// diverge, and attemptSeq counts the durable delivery attempts.
type localDeliveryFact struct {
	FactType    string `json:"factType"`
	Sequence    int64  `json:"sequence"`
	CommandId   string `json:"commandId"`
	DeliveredAt string `json:"deliveredAt"`
	AttemptSeq  int64  `json:"attemptSeq"`
	Digest      string `json:"digest"`
}

// localTimerArmedFact records one armed timer wakeup derived from the timer
// command payload.
type localTimerArmedFact struct {
	FactType  string `json:"factType"`
	Sequence  int64  `json:"sequence"`
	CommandId string `json:"commandId"`
	Target    string `json:"target"`
	FireAt    string `json:"fireAt"`
	Digest    string `json:"digest"`
}

// localTimerFiredFact records one fired timer wakeup.
type localTimerFiredFact struct {
	FactType  string `json:"factType"`
	Sequence  int64  `json:"sequence"`
	CommandId string `json:"commandId"`
	FiredAt   string `json:"firedAt"`
	Digest    string `json:"digest"`
}

// localSignalFact records one signal delivered into a target mailbox; the
// body is carried so the durable mailbox rebuilds without the payload store.
type localSignalFact struct {
	FactType    string `json:"factType"`
	Sequence    int64  `json:"sequence"`
	CommandId   string `json:"commandId"`
	Target      string `json:"target"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	DeliveredAt string `json:"deliveredAt"`
	Digest      string `json:"digest"`
}

// localFact is the sealed-envelope contract shared by every Local Engine
// state fact type: the canonical content digest is always computed with the
// digest binding detached and stored back on the fact before the canonical
// line is appended.
type localFact interface {
	factDigest() string
	setFactDigest(digest string)
}

func (fact localConsumedFact) factDigest() string {
	return fact.Digest
}

func (fact *localConsumedFact) setFactDigest(digest string) {
	fact.Digest = digest
}

func (fact localDeliveryFact) factDigest() string {
	return fact.Digest
}

func (fact *localDeliveryFact) setFactDigest(digest string) {
	fact.Digest = digest
}

func (fact localTimerArmedFact) factDigest() string {
	return fact.Digest
}

func (fact *localTimerArmedFact) setFactDigest(digest string) {
	fact.Digest = digest
}

func (fact localTimerFiredFact) factDigest() string {
	return fact.Digest
}

func (fact *localTimerFiredFact) setFactDigest(digest string) {
	fact.Digest = digest
}

func (fact localSignalFact) factDigest() string {
	return fact.Digest
}

func (fact *localSignalFact) setFactDigest(digest string) {
	fact.Digest = digest
}

// localCommandState is the in-memory projection of one consumed command.
type localCommandState struct {
	command     Command
	deliveredAt string // empty until the first delivery-recorded fact
	attemptSeq  int64  // count of delivery-recorded facts
}

// localTimerState is the in-memory projection of one armed timer.
type localTimerState struct {
	target string
	fireAt time.Time
	fired  bool
}

// SignalDelivery is one signal durably delivered into a target mailbox.
type SignalDelivery struct {
	CommandId   string `json:"commandId"`
	Target      string `json:"target"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	DeliveredAt string `json:"deliveredAt"`
}

// Wakeup is one fired timer wakeup notification.
type Wakeup struct {
	CommandId string `json:"commandId"`
	Target    string `json:"target"`
	FireAt    string `json:"fireAt"`
	FiredAt   string `json:"firedAt"`
}

// LocalBackend is the embedded/single-machine in-process first-class
// durable execution backend (ADR 0016 §5/§7): durable append-only state
// under the construction-time stateRoot, receipt idempotency under the
// commandId, timer wakeup scheduling, signal mailboxes and crash recovery
// by deterministic state replay. It is a complete backend in its own right,
// not a Temporal stub, and it never touches the reserved .marshal
// directory: state isolation is guaranteed by the caller-supplied
// stateRoot. Delivery here is transport-level consumption and reporting;
// workload execution toward the execution plane and Core wiring belong to
// later milestones.
type LocalBackend struct {
	mu           sync.Mutex
	stateRoot    string
	payloads     PayloadStore
	tickInterval time.Duration
	recovered    bool
	closed       bool
	nextSequence int64

	commands     map[string]*localCommandState
	commandOrder []string
	timers       map[string]*localTimerState
	signals      map[string][]SignalDelivery
	signalIds    map[string]struct{}
	wakeups      chan Wakeup
	stop         chan struct{}
	scheduler    sync.WaitGroup
}

// NewLocalBackend opens (creating it if absent) the durable stateRoot
// directory and binds the payload store. The backend is not usable until
// Recover replays the durable state; every operation before recovery fails
// closed.
func NewLocalBackend(stateRoot string, payloads PayloadStore) (*LocalBackend, error) {
	if strings.TrimSpace(stateRoot) == "" {
		return nil, fmt.Errorf("engine: local backend stateRoot must be a non-empty directory path")
	}
	if payloads == nil {
		return nil, fmt.Errorf("engine: local backend requires a payload store")
	}
	info, err := os.Stat(stateRoot)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(stateRoot, 0o700); err != nil {
			return nil, fmt.Errorf("engine: create local backend stateRoot: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("engine: local backend stateRoot: %w", err)
	case !info.IsDir():
		return nil, fmt.Errorf("engine: local backend stateRoot is not a directory")
	}
	return &LocalBackend{
		stateRoot:    stateRoot,
		payloads:     payloads,
		tickInterval: defaultLocalTickInterval,
		nextSequence: 1,
		commands:     map[string]*localCommandState{},
		timers:       map[string]*localTimerState{},
		signals:      map[string][]SignalDelivery{},
		signalIds:    map[string]struct{}{},
		wakeups:      make(chan Wakeup, localWakeupBuffer),
		stop:         make(chan struct{}),
	}, nil
}

// Recover replays the durable state file and rebuilds the transport state
// deterministically: the identical state bytes always rebuild the identical
// state. Due timers fire immediately during recovery, pending timers are
// re-armed under the scheduler, mailboxes and receipts survive. A corrupt,
// non canonical or conflicting state file fails closed; nothing is silently
// skipped. Recover runs exactly once per backend instance; crash recovery
// constructs a fresh backend over the same stateRoot.
func (backend *LocalBackend) Recover(ctx context.Context) error {
	_ = ctx
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return ErrLocalBackendClosed
	}
	if backend.recovered {
		backend.mu.Unlock()
		return fmt.Errorf("engine: local backend is already recovered; crash recovery constructs a fresh backend over the same stateRoot")
	}
	if err := backend.replayStateLocked(); err != nil {
		backend.mu.Unlock()
		return err
	}
	backend.recovered = true
	due, err := backend.fireDueTimersLocked(time.Now().UTC())
	backend.mu.Unlock()
	if err != nil {
		backend.mu.Lock()
		backend.closed = true
		backend.mu.Unlock()
		return err
	}
	for _, wakeup := range due {
		backend.publishWakeup(wakeup)
	}
	backend.scheduler.Add(1)
	go backend.runScheduler()
	return nil
}

// Deliver consumes one command and reports the transport receipt (Backend
// contract). Duplicate delivery of the identical commandId merges
// idempotently: command effects (timer arming, signal mailbox entries)
// execute exactly once per commandId, the authoritative deliveredAt never
// changes, and attemptSeq counts the durable delivery attempts. Payloads
// are resolved through the payload store and verified against the
// payloadRef digest fail closed.
func (backend *LocalBackend) Deliver(ctx context.Context, command Command) (Receipt, error) {
	if err := command.Validate(); err != nil {
		return Receipt{}, err
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.closed {
		return Receipt{}, ErrLocalBackendClosed
	}
	if !backend.recovered {
		return Receipt{}, fmt.Errorf("%w: the local backend must recover its stateRoot before consuming commands", ErrBackendNotRecovered)
	}
	now := time.Now().UTC()
	if state, known := backend.commands[command.CommandId]; known {
		return backend.redeliverLocked(ctx, state, now)
	}
	return backend.firstDeliveryLocked(ctx, command, now)
}

// Close gracefully stops the scheduler and closes the backend. Durable
// state is unaffected: every fact is synced at append time.
func (backend *LocalBackend) Close() error {
	return backend.shutdown()
}

// Kill is the crash simulation: it stops the scheduler without any further
// state mutation, mirroring a process death. All durable state was already
// synced at append time, so kill and crash leave identical state bytes.
func (backend *LocalBackend) Kill() {
	_ = backend.shutdown()
}

// StateRoot returns the durable state directory.
func (backend *LocalBackend) StateRoot() string {
	return backend.stateRoot
}

// ReceiptFor returns the authoritative receipt recorded for commandId.
func (backend *LocalBackend) ReceiptFor(commandId string) (Receipt, bool) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	state, known := backend.commands[commandId]
	if !known || state.deliveredAt == "" {
		return Receipt{}, false
	}
	return Receipt{CommandId: commandId, DeliveredAt: state.deliveredAt, AttemptSeq: state.attemptSeq}, true
}

// ConsumedCommands returns every consumed command in consumption order.
func (backend *LocalBackend) ConsumedCommands() []Command {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	commands := make([]Command, 0, len(backend.commandOrder))
	for _, commandId := range backend.commandOrder {
		commands = append(commands, backend.commands[commandId].command)
	}
	return commands
}

// SignalsFor returns the durable mailbox of signals delivered to target, in
// delivery order.
func (backend *LocalBackend) SignalsFor(target string) []SignalDelivery {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	mailbox := backend.signals[target]
	copied := make([]SignalDelivery, len(mailbox))
	copy(copied, mailbox)
	return copied
}

// Wakeups returns the fired timer wakeup notification channel.
func (backend *LocalBackend) Wakeups() <-chan Wakeup {
	return backend.wakeups
}

// TimerState reports the armed timer recorded under commandId: the target,
// the RFC 3339 fire time, whether the timer already fired and whether the
// timer exists at all.
func (backend *LocalBackend) TimerState(commandId string) (target string, fireAt string, fired bool, ok bool) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	timer, armed := backend.timers[commandId]
	if !armed {
		return "", "", false, false
	}
	return timer.target, timer.fireAt.Format(time.RFC3339), timer.fired, true
}

// shutdown stops the scheduler exactly once and marks the backend closed.
func (backend *LocalBackend) shutdown() error {
	backend.mu.Lock()
	if backend.closed {
		backend.mu.Unlock()
		return ErrLocalBackendClosed
	}
	backend.closed = true
	wasRecovered := backend.recovered
	close(backend.stop)
	backend.mu.Unlock()
	if wasRecovered {
		backend.scheduler.Wait()
	}
	return nil
}

// statePath returns the path of the append-only state file.
func (backend *LocalBackend) statePath() string {
	return filepath.Join(backend.stateRoot, localStateFileName)
}

// firstDeliveryLocked consumes a previously unseen command: resolve,
// verify and strictly validate the externalized payload fail closed before
// any state is touched, append the command-consumed fact, apply the
// kind-specific effects exactly once and record the authoritative receipt.
func (backend *LocalBackend) firstDeliveryLocked(ctx context.Context, command Command, now time.Time) (Receipt, error) {
	payload, err := backend.resolvePayloadLocked(ctx, command)
	if err != nil {
		return Receipt{}, err
	}
	if err := validateCommandPayload(command.Kind, payload); err != nil {
		return Receipt{}, err
	}
	consumed := localConsumedFact{
		FactType:   localFactCommandConsumed,
		Sequence:   backend.nextSequence,
		Command:    command,
		ConsumedAt: now.Format(time.RFC3339),
	}
	if err := backend.appendFactLineLocked(&consumed); err != nil {
		return Receipt{}, err
	}
	backend.nextSequence++
	state := &localCommandState{command: command}
	backend.commands[command.CommandId] = state
	backend.commandOrder = append(backend.commandOrder, command.CommandId)
	if err := backend.applyCommandEffectsLocked(command, payload, now); err != nil {
		return Receipt{}, err
	}
	return backend.recordDeliveryLocked(state, now)
}

// redeliverLocked merges a duplicate delivery of a known commandId: any
// effects lost in a crash window between consumption and the receipt are
// completed first, then the next durable delivery attempt is recorded. No
// effect ever executes twice for one commandId.
func (backend *LocalBackend) redeliverLocked(ctx context.Context, state *localCommandState, now time.Time) (Receipt, error) {
	effectsComplete := true
	switch state.command.Kind {
	case CommandKindTimer:
		_, effectsComplete = backend.timers[state.command.CommandId]
	case CommandKindSignal:
		_, effectsComplete = backend.signalIds[state.command.CommandId]
	}
	if !effectsComplete {
		payload, err := backend.resolvePayloadLocked(ctx, state.command)
		if err != nil {
			return Receipt{}, err
		}
		if err := backend.applyCommandEffectsLocked(state.command, payload, now); err != nil {
			return Receipt{}, err
		}
	}
	return backend.recordDeliveryLocked(state, now)
}

// resolvePayloadLocked fetches the externalized payload bytes from the
// payload store and verifies them against the payloadRef digest fail
// closed.
func (backend *LocalBackend) resolvePayloadLocked(ctx context.Context, command Command) ([]byte, error) {
	payload, err := backend.payloads.Payload(ctx, command.PayloadRef)
	if err != nil {
		return nil, fmt.Errorf("%w: payload %s is not available from the payload store: %v", ErrPayloadRejected, command.PayloadRef, err)
	}
	if err := VerifyPayloadRef(payload, command.PayloadRef); err != nil {
		return nil, err
	}
	return payload, nil
}

// validateCommandPayload strictly decodes and validates the externalized
// payload for the command kind: payload admission fails closed before any
// durable state is touched.
func validateCommandPayload(kind CommandKind, payload []byte) error {
	switch kind {
	case CommandKindDispatch:
		_, err := DecodeDispatchPayload(payload)
		return err
	case CommandKindSideEffect:
		_, err := DecodeSideEffectPayload(payload)
		return err
	case CommandKindTimer:
		_, err := DecodeTimerPayload(payload)
		return err
	case CommandKindSignal:
		_, err := DecodeSignalPayload(payload)
		return err
	default:
		return kind.Validate()
	}
}

// applyCommandEffectsLocked applies the kind-specific command effects
// exactly once per commandId: dispatch and side-effect commands are pure
// transport consumption (payload validation only), timer commands arm the
// durable timer and signal commands append the durable mailbox entry.
func (backend *LocalBackend) applyCommandEffectsLocked(command Command, payload []byte, now time.Time) error {
	switch command.Kind {
	case CommandKindDispatch:
		_, err := DecodeDispatchPayload(payload)
		return err
	case CommandKindSideEffect:
		_, err := DecodeSideEffectPayload(payload)
		return err
	case CommandKindTimer:
		if _, armed := backend.timers[command.CommandId]; armed {
			return nil
		}
		decoded, err := DecodeTimerPayload(payload)
		if err != nil {
			return err
		}
		fireAt, err := time.Parse(time.RFC3339, decoded.FireAt)
		if err != nil {
			return fmt.Errorf("%w: timerPayload.fireAt: %v", ErrPayloadRejected, err)
		}
		fact := localTimerArmedFact{
			FactType:  localFactTimerArmed,
			Sequence:  backend.nextSequence,
			CommandId: command.CommandId,
			Target:    decoded.Target,
			FireAt:    decoded.FireAt,
		}
		if err := backend.appendFactLineLocked(&fact); err != nil {
			return err
		}
		backend.nextSequence++
		backend.timers[command.CommandId] = &localTimerState{target: decoded.Target, fireAt: fireAt}
		return nil
	case CommandKindSignal:
		if _, delivered := backend.signalIds[command.CommandId]; delivered {
			return nil
		}
		decoded, err := DecodeSignalPayload(payload)
		if err != nil {
			return err
		}
		deliveredAt := now.Format(time.RFC3339)
		fact := localSignalFact{
			FactType:    localFactSignalDelivered,
			Sequence:    backend.nextSequence,
			CommandId:   command.CommandId,
			Target:      decoded.Target,
			Name:        decoded.Name,
			Body:        decoded.Body,
			DeliveredAt: deliveredAt,
		}
		if err := backend.appendFactLineLocked(&fact); err != nil {
			return err
		}
		backend.nextSequence++
		backend.signalIds[command.CommandId] = struct{}{}
		backend.signals[decoded.Target] = append(backend.signals[decoded.Target], SignalDelivery{
			CommandId:   command.CommandId,
			Target:      decoded.Target,
			Name:        decoded.Name,
			Body:        decoded.Body,
			DeliveredAt: deliveredAt,
		})
		return nil
	default:
		return fmt.Errorf("engine: local backend received command kind %q outside the closed enumeration", string(command.Kind))
	}
}

// recordDeliveryLocked appends the next delivery-recorded fact and returns
// the receipt: the authoritative deliveredAt is set by the first recorded
// attempt and carried unchanged by every later attempt.
func (backend *LocalBackend) recordDeliveryLocked(state *localCommandState, now time.Time) (Receipt, error) {
	deliveredAt := state.deliveredAt
	if deliveredAt == "" {
		deliveredAt = now.Format(time.RFC3339)
	}
	attemptSeq := state.attemptSeq + 1
	fact := localDeliveryFact{
		FactType:    localFactDeliveryRecorded,
		Sequence:    backend.nextSequence,
		CommandId:   state.command.CommandId,
		DeliveredAt: deliveredAt,
		AttemptSeq:  attemptSeq,
	}
	if err := backend.appendFactLineLocked(&fact); err != nil {
		return Receipt{}, err
	}
	backend.nextSequence++
	state.deliveredAt = deliveredAt
	state.attemptSeq = attemptSeq
	return Receipt{CommandId: state.command.CommandId, DeliveredAt: deliveredAt, AttemptSeq: attemptSeq}, nil
}

// runScheduler fires due timers on every tick until the backend closes.
func (backend *LocalBackend) runScheduler() {
	defer backend.scheduler.Done()
	ticker := time.NewTicker(backend.tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-backend.stop:
			return
		case <-ticker.C:
			backend.mu.Lock()
			if backend.closed {
				backend.mu.Unlock()
				return
			}
			due, err := backend.fireDueTimersLocked(time.Now().UTC())
			if err != nil {
				backend.closed = true
				backend.mu.Unlock()
				return
			}
			backend.mu.Unlock()
			for _, wakeup := range due {
				backend.publishWakeup(wakeup)
			}
		}
	}
}

// fireDueTimersLocked fires every due timer exactly once in deterministic
// commandId order, appending one timer-fired fact per fired timer. The
// caller must hold backend.mu.
func (backend *LocalBackend) fireDueTimersLocked(now time.Time) ([]Wakeup, error) {
	dueIds := make([]string, 0, len(backend.timers))
	for commandId, timer := range backend.timers {
		if !timer.fired && !timer.fireAt.After(now) {
			dueIds = append(dueIds, commandId)
		}
	}
	sort.Strings(dueIds)
	fired := make([]Wakeup, 0, len(dueIds))
	for _, commandId := range dueIds {
		timer := backend.timers[commandId]
		timer.fired = true
		firedAt := now.Format(time.RFC3339)
		fact := localTimerFiredFact{
			FactType:  localFactTimerFired,
			Sequence:  backend.nextSequence,
			CommandId: commandId,
			FiredAt:   firedAt,
		}
		if err := backend.appendFactLineLocked(&fact); err != nil {
			return fired, err
		}
		backend.nextSequence++
		fired = append(fired, Wakeup{
			CommandId: commandId,
			Target:    timer.target,
			FireAt:    timer.fireAt.Format(time.RFC3339),
			FiredAt:   firedAt,
		})
	}
	return fired, nil
}

// publishWakeup pushes one fired wakeup notification without blocking past
// a closing backend.
func (backend *LocalBackend) publishWakeup(wakeup Wakeup) {
	select {
	case backend.wakeups <- wakeup:
	case <-backend.stop:
	}
}

// appendFactLineLocked seals fact with its canonical content digest,
// canonicalizes it under RFC 8785 JCS and appends it as one line to the
// state file, syncing before returning so the fact is durable. Existing
// lines are never rewritten. The caller must hold backend.mu.
func (backend *LocalBackend) appendFactLineLocked(fact localFact) error {
	digest, err := localFactContentDigest(fact)
	if err != nil {
		return err
	}
	fact.setFactDigest(digest)
	raw, err := json.Marshal(fact)
	if err != nil {
		return fmt.Errorf("engine: marshal local backend fact: %w", err)
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return fmt.Errorf("engine: canonicalize local backend fact: %w", err)
	}
	line := append(canonicalized, '\n')
	file, err := os.OpenFile(backend.statePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("engine: open local backend state: %w", err)
	}
	if _, err := file.Write(line); err != nil {
		file.Close()
		return fmt.Errorf("engine: append local backend fact: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("engine: sync local backend state: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("engine: close local backend state: %w", err)
	}
	return nil
}

// localFactContentDigest returns the canonical content digest of one state
// fact: RFC 8785 JCS over the record with the digest binding detached.
func localFactContentDigest(fact localFact) (string, error) {
	if fact.factDigest() != "" {
		return "", fmt.Errorf("engine: the local backend fact digest must be detached before sealing")
	}
	return canonicalDigestOf(fact)
}

// replayStateLocked replays the state file (when present) and rebuilds the
// in-memory transport state deterministically. Any malformed, non
// canonical, conflicting or orphan line fails closed; nothing is silently
// skipped. The caller must hold backend.mu.
func (backend *LocalBackend) replayStateLocked() error {
	file, err := os.Open(backend.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("engine: open local backend state: %w", err)
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
		if err := backend.applyStateLineLocked(line); err != nil {
			return fmt.Errorf("engine: local backend recovery failed at line %d: %w", lineNumber, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("engine: read local backend state: %w", err)
	}
	return nil
}

// applyStateLineLocked validates one state line as canonical JSON with a
// well-formed sequence and applies the fact it carries. The caller must
// hold backend.mu.
func (backend *LocalBackend) applyStateLineLocked(line []byte) error {
	canonicalized, err := canonical.JSON(line)
	if err != nil {
		return fmt.Errorf("state line rejected: %w", err)
	}
	if !bytes.Equal(canonicalized, line) {
		return fmt.Errorf("state line is not in canonical form")
	}
	var envelope struct {
		FactType string `json:"factType"`
		Sequence int64  `json:"sequence"`
	}
	if err := json.Unmarshal(line, &envelope); err != nil {
		return fmt.Errorf("decode local backend fact envelope: %w", err)
	}
	if envelope.Sequence != backend.nextSequence {
		return fmt.Errorf("local backend fact sequence %d does not follow the state sequence %d: the append-only sequence never skips or repeats", envelope.Sequence, backend.nextSequence-1)
	}
	switch envelope.FactType {
	case localFactCommandConsumed:
		return backend.applyConsumedFactLocked(line)
	case localFactDeliveryRecorded:
		return backend.applyDeliveryFactLocked(line)
	case localFactTimerArmed:
		return backend.applyTimerArmedFactLocked(line)
	case localFactTimerFired:
		return backend.applyTimerFiredFactLocked(line)
	case localFactSignalDelivered:
		return backend.applySignalFactLocked(line)
	default:
		return fmt.Errorf("unknown local backend factType %q", envelope.FactType)
	}
}

// decodeLocalFact strictly decodes one canonical state line into fact,
// rejecting unknown fields fail closed.
func decodeLocalFact(line []byte, fact any) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(fact); err != nil {
		return fmt.Errorf("decode local backend fact: %w", err)
	}
	return nil
}

// verifyLocalFactDigest fails closed unless the digest stored on fact equals
// the canonical content digest of the fact with the digest binding
// detached.
func verifyLocalFactDigest(fact localFact) error {
	stored := fact.factDigest()
	if stored == "" {
		return fmt.Errorf("local backend fact carries no digest binding")
	}
	fact.setFactDigest("")
	computed, err := localFactContentDigest(fact)
	fact.setFactDigest(stored)
	if err != nil {
		return err
	}
	if stored != computed {
		return fmt.Errorf("local backend fact digest does not match the canonical content digest")
	}
	return nil
}

// applyConsumedFactLocked validates and indexes one command-consumed fact.
func (backend *LocalBackend) applyConsumedFactLocked(line []byte) error {
	var fact localConsumedFact
	if err := decodeLocalFact(line, &fact); err != nil {
		return err
	}
	if err := verifyLocalFactDigest(&fact); err != nil {
		return err
	}
	if err := fact.Command.Validate(); err != nil {
		return fmt.Errorf("command-consumed fact failed validation: %w", err)
	}
	if err := requireRFC3339("command-consumed fact consumedAt", fact.ConsumedAt); err != nil {
		return err
	}
	if _, exists := backend.commands[fact.Command.CommandId]; exists {
		return fmt.Errorf("%w: duplicate command-consumed fact for commandId %s", ErrLocalStateConflict, fact.Command.CommandId)
	}
	backend.commands[fact.Command.CommandId] = &localCommandState{command: fact.Command}
	backend.commandOrder = append(backend.commandOrder, fact.Command.CommandId)
	backend.nextSequence++
	return nil
}

// applyDeliveryFactLocked validates and applies one delivery-recorded fact:
// attemptSeq must follow the recorded attempt count exactly and every
// attempt must carry the identical authoritative deliveredAt.
func (backend *LocalBackend) applyDeliveryFactLocked(line []byte) error {
	var fact localDeliveryFact
	if err := decodeLocalFact(line, &fact); err != nil {
		return err
	}
	if err := verifyLocalFactDigest(&fact); err != nil {
		return err
	}
	if err := requireDigest("delivery-recorded fact commandId", fact.CommandId); err != nil {
		return err
	}
	if err := requireRFC3339("delivery-recorded fact deliveredAt", fact.DeliveredAt); err != nil {
		return err
	}
	if fact.AttemptSeq < 1 {
		return fmt.Errorf("delivery-recorded fact attemptSeq must be a positive integer")
	}
	state, known := backend.commands[fact.CommandId]
	if !known {
		return fmt.Errorf("%w: delivery-recorded fact references unknown commandId %s", ErrUnknownCommand, fact.CommandId)
	}
	expected := state.attemptSeq + 1
	if fact.AttemptSeq != expected {
		return fmt.Errorf("%w: delivery-recorded fact attemptSeq %d does not follow the recorded attempt count %d", ErrLocalStateConflict, fact.AttemptSeq, state.attemptSeq)
	}
	if state.deliveredAt == "" {
		state.deliveredAt = fact.DeliveredAt
	} else if state.deliveredAt != fact.DeliveredAt {
		return fmt.Errorf("%w: duplicate delivery must carry the identical authoritative deliveredAt", ErrLocalStateConflict)
	}
	state.attemptSeq = fact.AttemptSeq
	backend.nextSequence++
	return nil
}

// applyTimerArmedFactLocked validates and applies one timer-armed fact.
func (backend *LocalBackend) applyTimerArmedFactLocked(line []byte) error {
	var fact localTimerArmedFact
	if err := decodeLocalFact(line, &fact); err != nil {
		return err
	}
	if err := verifyLocalFactDigest(&fact); err != nil {
		return err
	}
	if err := requireDigest("timer-armed fact commandId", fact.CommandId); err != nil {
		return err
	}
	if err := requireText("timer-armed fact target", fact.Target); err != nil {
		return err
	}
	if err := requireRFC3339("timer-armed fact fireAt", fact.FireAt); err != nil {
		return err
	}
	state, known := backend.commands[fact.CommandId]
	if !known {
		return fmt.Errorf("%w: timer-armed fact references unknown commandId %s", ErrUnknownCommand, fact.CommandId)
	}
	if state.command.Kind != CommandKindTimer {
		return fmt.Errorf("%w: timer-armed fact references commandId %s with command kind %q", ErrLocalStateConflict, fact.CommandId, string(state.command.Kind))
	}
	if _, armed := backend.timers[fact.CommandId]; armed {
		return fmt.Errorf("%w: duplicate timer-armed fact for commandId %s", ErrLocalStateConflict, fact.CommandId)
	}
	fireAt, err := time.Parse(time.RFC3339, fact.FireAt)
	if err != nil {
		return fmt.Errorf("timer-armed fact fireAt: %w", err)
	}
	backend.timers[fact.CommandId] = &localTimerState{target: fact.Target, fireAt: fireAt}
	backend.nextSequence++
	return nil
}

// applyTimerFiredFactLocked validates and applies one timer-fired fact: the
// timer must be armed and must not already have fired, so a timer never
// fires twice across restarts.
func (backend *LocalBackend) applyTimerFiredFactLocked(line []byte) error {
	var fact localTimerFiredFact
	if err := decodeLocalFact(line, &fact); err != nil {
		return err
	}
	if err := verifyLocalFactDigest(&fact); err != nil {
		return err
	}
	if err := requireDigest("timer-fired fact commandId", fact.CommandId); err != nil {
		return err
	}
	if err := requireRFC3339("timer-fired fact firedAt", fact.FiredAt); err != nil {
		return err
	}
	timer, armed := backend.timers[fact.CommandId]
	if !armed {
		return fmt.Errorf("%w: timer-fired fact references unknown commandId %s", ErrUnknownCommand, fact.CommandId)
	}
	if timer.fired {
		return fmt.Errorf("%w: timer %s already fired and can never fire twice", ErrLocalStateConflict, fact.CommandId)
	}
	timer.fired = true
	backend.nextSequence++
	return nil
}

// applySignalFactLocked validates and applies one signal-delivered fact
// into the durable target mailbox.
func (backend *LocalBackend) applySignalFactLocked(line []byte) error {
	var fact localSignalFact
	if err := decodeLocalFact(line, &fact); err != nil {
		return err
	}
	if err := verifyLocalFactDigest(&fact); err != nil {
		return err
	}
	if err := requireDigest("signal-delivered fact commandId", fact.CommandId); err != nil {
		return err
	}
	if err := requireText("signal-delivered fact target", fact.Target); err != nil {
		return err
	}
	if err := requireText("signal-delivered fact name", fact.Name); err != nil {
		return err
	}
	if err := requireRFC3339("signal-delivered fact deliveredAt", fact.DeliveredAt); err != nil {
		return err
	}
	state, known := backend.commands[fact.CommandId]
	if !known {
		return fmt.Errorf("%w: signal-delivered fact references unknown commandId %s", ErrUnknownCommand, fact.CommandId)
	}
	if state.command.Kind != CommandKindSignal {
		return fmt.Errorf("%w: signal-delivered fact references commandId %s with command kind %q", ErrLocalStateConflict, fact.CommandId, string(state.command.Kind))
	}
	if _, delivered := backend.signalIds[fact.CommandId]; delivered {
		return fmt.Errorf("%w: duplicate signal-delivered fact for commandId %s", ErrLocalStateConflict, fact.CommandId)
	}
	backend.signalIds[fact.CommandId] = struct{}{}
	backend.signals[fact.Target] = append(backend.signals[fact.Target], SignalDelivery{
		CommandId:   fact.CommandId,
		Target:      fact.Target,
		Name:        fact.Name,
		Body:        fact.Body,
		DeliveredAt: fact.DeliveredAt,
	})
	backend.nextSequence++
	return nil
}
