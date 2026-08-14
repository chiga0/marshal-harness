package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Local fixture timing constants: the scheduler tick, timer steps and wait
// windows used by the crash recovery fixtures.
const (
	localTestTimeout    = 3 * time.Second
	localTimerShortStep = 80 * time.Millisecond
	localTimerMidStep   = 60 * time.Millisecond
	localTimerLongStep  = time.Hour
	localSettleWait     = 200 * time.Millisecond
	localIdleWait       = 150 * time.Millisecond
)

// localCommand derives a well-formed command for the local backend
// fixtures: the payload is canonicalized into the store and the commandId
// follows the stable ledger derivation.
func localCommand(t *testing.T, store *memoryPayloadStore, kind CommandKind, seed string, payload any) Command {
	t.Helper()
	ref := putCanonicalPayload(t, store, payload)
	commandId, err := DeriveCommandId(fixedDigest("local-fact-"+seed), kind)
	if err != nil {
		t.Fatalf("DeriveCommandId rejected the fixture inputs: %v", err)
	}
	return Command{CommandId: commandId, Kind: kind, PayloadRef: ref}
}

// newLocalBackend constructs and recovers a Local Engine backend with the
// default scheduler tick and closes it at test end.
func newLocalBackend(t *testing.T, stateRoot string, store PayloadStore) *LocalBackend {
	t.Helper()
	backend, err := NewLocalBackend(stateRoot, store)
	if err != nil {
		t.Fatalf("NewLocalBackend rejected a valid stateRoot: %v", err)
	}
	if err := backend.Recover(context.Background()); err != nil {
		t.Fatalf("Recover rejected a fresh stateRoot: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

// newLocalBackendWithTick constructs and recovers a Local Engine backend
// with a fixed scheduler tick: an hour-long tick parks the scheduler so
// crash fixtures control exactly when timers may fire.
func newLocalBackendWithTick(t *testing.T, stateRoot string, store PayloadStore, tick time.Duration) *LocalBackend {
	t.Helper()
	backend, err := NewLocalBackend(stateRoot, store)
	if err != nil {
		t.Fatalf("NewLocalBackend rejected a valid stateRoot: %v", err)
	}
	backend.tickInterval = tick
	if err := backend.Recover(context.Background()); err != nil {
		t.Fatalf("Recover rejected a fresh stateRoot: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

// waitForWakeup waits for one fired timer wakeup within the timeout.
func waitForWakeup(t *testing.T, backend *LocalBackend, timeout time.Duration) Wakeup {
	t.Helper()
	select {
	case wakeup := <-backend.Wakeups():
		return wakeup
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a timer wakeup")
		return Wakeup{}
	}
}

func TestLocalBackendConstruction(t *testing.T) {
	store := newMemoryPayloadStore()
	if _, err := NewLocalBackend("", store); err == nil {
		t.Fatal("NewLocalBackend accepted a blank stateRoot")
	}
	if _, err := NewLocalBackend(t.TempDir(), nil); err == nil {
		t.Fatal("NewLocalBackend accepted a nil payload store")
	}
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("fixture write failed: %v", err)
	}
	if _, err := NewLocalBackend(notADir, store); err == nil {
		t.Fatal("NewLocalBackend accepted a stateRoot that is not a directory")
	}
	fresh := filepath.Join(t.TempDir(), "engine-state")
	backend, err := NewLocalBackend(fresh, store)
	if err != nil {
		t.Fatalf("NewLocalBackend rejected a fresh stateRoot path: %v", err)
	}
	if info, err := os.Stat(fresh); err != nil || !info.IsDir() {
		t.Fatalf("NewLocalBackend must create the stateRoot directory: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close rejected a fresh backend: %v", err)
	}
}

func TestLocalBackendDeliverBeforeRecoverFailsClosed(t *testing.T) {
	store := newMemoryPayloadStore()
	backend, err := NewLocalBackend(t.TempDir(), store)
	if err != nil {
		t.Fatalf("NewLocalBackend rejected a valid stateRoot: %v", err)
	}
	defer func() { _ = backend.Close() }()
	command := localCommand(t, store, CommandKindDispatch, "pre-recover", DispatchPayload{
		Target:         "target-" + "pre-recover",
		WorkloadDigest: fixedDigest("workload-" + "pre-recover"),
	})
	if _, err := backend.Deliver(context.Background(), command); !errors.Is(err, ErrBackendNotRecovered) {
		t.Fatalf("Deliver before Recover must fail closed with ErrBackendNotRecovered, got %v", err)
	}
}

func TestLocalBackendDispatchDelivery(t *testing.T) {
	root := t.TempDir()
	store := newMemoryPayloadStore()
	backend := newLocalBackend(t, root, store)
	command := localCommand(t, store, CommandKindDispatch, "dispatch-"+"1", DispatchPayload{
		Target:         "target-" + "1",
		WorkloadDigest: fixedDigest("workload-" + "1"),
	})
	receipt, err := backend.Deliver(context.Background(), command)
	if err != nil {
		t.Fatalf("Deliver rejected a valid dispatch command: %v", err)
	}
	if receipt.CommandId != command.CommandId {
		t.Fatalf("the receipt must reference the delivered commandId, got %s", receipt.CommandId)
	}
	if receipt.AttemptSeq != 1 {
		t.Fatalf("the first delivery must carry attemptSeq 1, got %d", receipt.AttemptSeq)
	}
	stored, ok := backend.ReceiptFor(command.CommandId)
	if !ok || stored != receipt {
		t.Fatal("ReceiptFor must return the authoritative receipt")
	}
	consumed := backend.ConsumedCommands()
	if len(consumed) != 1 || consumed[0] != command {
		t.Fatal("the backend must record exactly the consumed command")
	}
	if _, err := os.Stat(filepath.Join(root, localStateFileName)); err != nil {
		t.Fatalf("the durable state file must exist after delivery: %v", err)
	}
}

func TestLocalBackendPayloadFailClosed(t *testing.T) {
	root := t.TempDir()
	store := newMemoryPayloadStore()
	backend := newLocalBackend(t, root, store)

	missing := localCommand(t, store, CommandKindDispatch, "missing-payload", DispatchPayload{
		Target:         "target-" + "missing",
		WorkloadDigest: fixedDigest("workload-" + "missing"),
	})
	store.mu.Lock()
	delete(store.bytes, missing.PayloadRef)
	store.mu.Unlock()
	if _, err := backend.Deliver(context.Background(), missing); !errors.Is(err, ErrPayloadRejected) {
		t.Fatalf("Deliver must fail closed on a missing externalized payload, got %v", err)
	}

	raw, _, err := EncodePayload(DispatchPayload{Target: "target-" + "mismatch", WorkloadDigest: fixedDigest("workload-" + "mismatch")})
	if err != nil {
		t.Fatalf("EncodePayload rejected the fixture payload: %v", err)
	}
	wrongRef := fixedDigest("wrong-" + "ref")
	store.put(wrongRef, raw)
	mismatchId, err := DeriveCommandId(fixedDigest("local-fact-mismatch"), CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommandId failed: %v", err)
	}
	mismatch := Command{CommandId: mismatchId, Kind: CommandKindDispatch, PayloadRef: wrongRef}
	if _, err := backend.Deliver(context.Background(), mismatch); !errors.Is(err, ErrPayloadRejected) {
		t.Fatalf("Deliver must fail closed on a payloadRef digest mismatch, got %v", err)
	}

	surpriseRaw, surpriseRef, err := EncodePayload(struct {
		Target         string `json:"target"`
		WorkloadDigest string `json:"workloadDigest"`
		Surprise       int    `json:"surprise"`
	}{Target: "target-" + "surprise", WorkloadDigest: fixedDigest("workload-" + "surprise"), Surprise: 1})
	if err != nil {
		t.Fatalf("EncodePayload rejected the fixture payload: %v", err)
	}
	store.put(surpriseRef, surpriseRaw)
	surpriseId, err := DeriveCommandId(fixedDigest("local-fact-surprise"), CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommandId failed: %v", err)
	}
	surprise := Command{CommandId: surpriseId, Kind: CommandKindDispatch, PayloadRef: surpriseRef}
	if _, err := backend.Deliver(context.Background(), surprise); !errors.Is(err, ErrPayloadRejected) {
		t.Fatalf("Deliver must fail closed on a payload outside the frozen schema, got %v", err)
	}
	if commands := backend.ConsumedCommands(); len(commands) != 0 {
		t.Fatalf("failed deliveries must never be consumed, got %d", len(commands))
	}
}

// TestLocalBackendDuplicateDeliveryIdempotent freezes the backend half of
// at-least-once delivery: the identical commandId consumes effects exactly
// once, keeps the identical authoritative deliveredAt and counts delivery
// attempts through attemptSeq.
func TestLocalBackendDuplicateDeliveryIdempotent(t *testing.T) {
	root := t.TempDir()
	store := newMemoryPayloadStore()
	backend := newLocalBackend(t, root, store)
	command := localCommand(t, store, CommandKindSignal, "dup-signal", SignalPayload{
		Target: "target-" + "dup",
		Name:   "cancel-requested",
		Body:   "body-" + "dup",
	})
	first, err := backend.Deliver(context.Background(), command)
	if err != nil {
		t.Fatalf("Deliver rejected a valid signal command: %v", err)
	}
	second, err := backend.Deliver(context.Background(), command)
	if err != nil {
		t.Fatalf("duplicate Deliver must merge idempotently, got %v", err)
	}
	if second.AttemptSeq != 2 {
		t.Fatalf("the duplicate delivery must carry attemptSeq 2, got %d", second.AttemptSeq)
	}
	if second.DeliveredAt != first.DeliveredAt {
		t.Fatalf("duplicate delivery must keep the authoritative deliveredAt: %s != %s", second.DeliveredAt, first.DeliveredAt)
	}
	if mailbox := backend.SignalsFor("target-" + "dup"); len(mailbox) != 1 {
		t.Fatalf("the mailbox must hold exactly one entry for duplicate deliveries, got %d", len(mailbox))
	}
	if commands := backend.ConsumedCommands(); len(commands) != 1 {
		t.Fatalf("duplicate delivery must never consume the command twice, got %d", len(commands))
	}
}

func TestLocalBackendTimerWakeup(t *testing.T) {
	root := t.TempDir()
	store := newMemoryPayloadStore()
	backend := newLocalBackend(t, root, store)
	fireAt := time.Now().UTC().Add(localTimerShortStep).Format(time.RFC3339)
	command := localCommand(t, store, CommandKindTimer, "wakeup-"+"1", TimerPayload{
		Target: "target-" + "wakeup",
		FireAt: fireAt,
	})
	receipt, err := backend.Deliver(context.Background(), command)
	if err != nil {
		t.Fatalf("Deliver rejected a valid timer command: %v", err)
	}
	if receipt.AttemptSeq != 1 {
		t.Fatalf("the timer consumption must carry attemptSeq 1, got %d", receipt.AttemptSeq)
	}
	target, armedFireAt, fired, ok := backend.TimerState(command.CommandId)
	if !ok || fired || target != "target-"+"wakeup" || armedFireAt != fireAt {
		t.Fatalf("the timer must be armed and pending with the payload fire time, got target %q fireAt %s fired %v ok %v", target, armedFireAt, fired, ok)
	}
	wakeup := waitForWakeup(t, backend, localTestTimeout)
	if wakeup.CommandId != command.CommandId || wakeup.Target != "target-"+"wakeup" || wakeup.FireAt != fireAt {
		t.Fatalf("the wakeup must identify the fired timer, got %+v", wakeup)
	}
	if _, _, firedAfter, _ := backend.TimerState(command.CommandId); !firedAfter {
		t.Fatal("the timer state must record the fired timer")
	}
}

func TestLocalBackendSignalTransport(t *testing.T) {
	root := t.TempDir()
	store := newMemoryPayloadStore()
	backend := newLocalBackend(t, root, store)
	first := localCommand(t, store, CommandKindSignal, "signal-"+"1", SignalPayload{
		Target: "target-" + "signals",
		Name:   "pause",
		Body:   "body-" + "1",
	})
	second := localCommand(t, store, CommandKindSignal, "signal-"+"2", SignalPayload{
		Target: "target-" + "signals",
		Name:   "resume",
		Body:   "",
	})
	if _, err := backend.Deliver(context.Background(), first); err != nil {
		t.Fatalf("Deliver rejected the first signal: %v", err)
	}
	if _, err := backend.Deliver(context.Background(), second); err != nil {
		t.Fatalf("Deliver rejected the second signal: %v", err)
	}
	// Duplicate transport of the identical commandId never duplicates the
	// mailbox entry.
	if _, err := backend.Deliver(context.Background(), first); err != nil {
		t.Fatalf("duplicate Deliver must merge idempotently, got %v", err)
	}
	mailbox := backend.SignalsFor("target-" + "signals")
	if len(mailbox) != 2 {
		t.Fatalf("the mailbox must hold exactly two entries, got %d", len(mailbox))
	}
	if mailbox[0].Name != "pause" || mailbox[0].Body != "body-"+"1" || mailbox[0].CommandId != first.CommandId {
		t.Fatalf("the first mailbox entry must carry the first signal, got %+v", mailbox[0])
	}
	if mailbox[1].Name != "resume" || mailbox[1].Body != "" || mailbox[1].CommandId != second.CommandId {
		t.Fatalf("the second mailbox entry must carry the second signal, got %+v", mailbox[1])
	}
	if other := backend.SignalsFor("target-" + "unknown"); len(other) != 0 {
		t.Fatalf("an unknown target mailbox must be empty, got %d", len(other))
	}
}

// TestLocalBackendCrashRecoveryStateReplay freezes backend-level crash
// recovery: after a kill the identical state bytes rebuild receipts,
// mailboxes and pending timers deterministically, and redelivery merges.
func TestLocalBackendCrashRecoveryStateReplay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := newMemoryPayloadStore()
	crashed := newLocalBackendWithTick(t, root, store, time.Hour)
	dispatchCommand := localCommand(t, store, CommandKindDispatch, "crash-dispatch", DispatchPayload{
		Target:         "target-" + "crash",
		WorkloadDigest: fixedDigest("workload-" + "crash"),
	})
	signalCommand := localCommand(t, store, CommandKindSignal, "crash-signal", SignalPayload{
		Target: "target-" + "crash",
		Name:   "pause",
		Body:   "body-" + "crash",
	})
	fireAt := time.Now().UTC().Add(localTimerLongStep).Format(time.RFC3339)
	timerCommand := localCommand(t, store, CommandKindTimer, "crash-timer", TimerPayload{
		Target: "target-" + "crash",
		FireAt: fireAt,
	})
	receipts := map[string]Receipt{}
	for _, command := range []Command{dispatchCommand, signalCommand, timerCommand} {
		receipt, err := crashed.Deliver(ctx, command)
		if err != nil {
			t.Fatalf("Deliver rejected command %s: %v", command.CommandId, err)
		}
		receipts[command.CommandId] = receipt
	}
	crashed.Kill()

	recovered := newLocalBackendWithTick(t, root, store, time.Hour)
	for commandId, expected := range receipts {
		stored, ok := recovered.ReceiptFor(commandId)
		if !ok || stored != expected {
			t.Fatalf("recovery must rebuild the identical receipt for %s: got %+v want %+v", commandId, stored, expected)
		}
	}
	if commands := recovered.ConsumedCommands(); len(commands) != 3 {
		t.Fatalf("recovery must rebuild the three consumed commands, got %d", len(commands))
	}
	mailbox := recovered.SignalsFor("target-" + "crash")
	if len(mailbox) != 1 || mailbox[0].Body != "body-"+"crash" {
		t.Fatalf("recovery must rebuild the durable signal mailbox, got %+v", mailbox)
	}
	target, recoveredFireAt, fired, ok := recovered.TimerState(timerCommand.CommandId)
	if !ok || fired || target != "target-"+"crash" || recoveredFireAt != fireAt {
		t.Fatalf("recovery must re-arm the pending timer, got target %q fireAt %s fired %v ok %v", target, recoveredFireAt, fired, ok)
	}
	// Redelivery after recovery merges idempotently.
	redelivered, err := recovered.Deliver(ctx, dispatchCommand)
	if err != nil {
		t.Fatalf("redelivery after recovery failed: %v", err)
	}
	if redelivered.AttemptSeq != 2 || redelivered.DeliveredAt != receipts[dispatchCommand.CommandId].DeliveredAt {
		t.Fatalf("redelivery must merge onto the recovered receipt, got %+v", redelivered)
	}
	if commands := recovered.ConsumedCommands(); len(commands) != 3 {
		t.Fatalf("redelivery must never consume the command again, got %d", len(commands))
	}
}

// TestLocalBackendCrashRecoveryFiresDueTimer freezes the due-timer recovery
// rule: a timer whose fire time passed while the backend was down fires
// immediately during recovery — exactly once.
func TestLocalBackendCrashRecoveryFiresDueTimer(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := newMemoryPayloadStore()
	crashed := newLocalBackendWithTick(t, root, store, time.Hour)
	fireAt := time.Now().UTC().Add(-time.Second).Format(time.RFC3339)
	command := localCommand(t, store, CommandKindTimer, "due-timer", TimerPayload{
		Target: "target-" + "due",
		FireAt: fireAt,
	})
	if _, err := crashed.Deliver(ctx, command); err != nil {
		t.Fatalf("Deliver rejected the due timer command: %v", err)
	}
	if _, _, fired, _ := crashed.TimerState(command.CommandId); fired {
		t.Fatal("the parked scheduler must not fire the timer before the kill")
	}
	crashed.Kill()

	recovered := newLocalBackendWithTick(t, root, store, time.Hour)
	wakeup := waitForWakeup(t, recovered, time.Second)
	if wakeup.CommandId != command.CommandId || wakeup.FireAt != fireAt {
		t.Fatalf("recovery must fire the due timer, got %+v", wakeup)
	}
	if _, _, fired, _ := recovered.TimerState(command.CommandId); !fired {
		t.Fatal("the due timer must be recorded as fired after recovery")
	}
	select {
	case extra := <-recovered.Wakeups():
		t.Fatalf("the due timer must fire exactly once across recovery, got a second wakeup %+v", extra)
	case <-time.After(localIdleWait):
	}
}

// TestLocalBackendNoDoubleFireAcrossRestart guards the fired-timer state:
// a timer that fired before the crash never fires again after recovery.
func TestLocalBackendNoDoubleFireAcrossRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := newMemoryPayloadStore()
	first := newLocalBackend(t, root, store)
	fireAt := time.Now().UTC().Add(localTimerMidStep).Format(time.RFC3339)
	command := localCommand(t, store, CommandKindTimer, "fired-before-crash", TimerPayload{
		Target: "target-" + "fired",
		FireAt: fireAt,
	})
	if _, err := first.Deliver(ctx, command); err != nil {
		t.Fatalf("Deliver rejected the timer command: %v", err)
	}
	waitForWakeup(t, first, localTestTimeout)
	first.Kill()

	second := newLocalBackend(t, root, store)
	select {
	case extra := <-second.Wakeups():
		t.Fatalf("a fired timer must never fire again after recovery, got %+v", extra)
	case <-time.After(localSettleWait):
	}
	if _, _, fired, ok := second.TimerState(command.CommandId); !ok || !fired {
		t.Fatal("recovery must keep the fired timer state")
	}
}

func TestLocalBackendCorruptStateFailsClosed(t *testing.T) {
	root := t.TempDir()
	store := newMemoryPayloadStore()
	if err := os.WriteFile(filepath.Join(root, localStateFileName), []byte("this is not canonical json\n"), 0o600); err != nil {
		t.Fatalf("fixture write failed: %v", err)
	}
	backend, err := NewLocalBackend(root, store)
	if err != nil {
		t.Fatalf("NewLocalBackend rejected the stateRoot: %v", err)
	}
	defer func() { _ = backend.Close() }()
	err = backend.Recover(context.Background())
	if err == nil {
		t.Fatal("Recover accepted a corrupt state file")
	}
	if !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("the recovery failure must identify the corrupt line, got: %v", err)
	}
}

// TestLocalBackendSeamRecoveryIndependentOfBackendState freezes the single
// seam recovery invariant: crash recovery replays ledger facts and receipt
// facts through the journal and never depends on backend internal state —
// a fresh empty backend, a lost receipt window and a retained backend state
// all converge to the identical authoritative outcome.
func TestLocalBackendSeamRecoveryIndependentOfBackendState(t *testing.T) {
	ctx := context.Background()
	namespace := testNamespace()

	storeA := newMemoryPayloadStore()
	rootA := t.TempDir()
	backendA := newLocalBackend(t, rootA, storeA)
	engineA, err := New(namespace, backendA)
	if err != nil {
		t.Fatalf("New rejected the phase-1 construction: %v", err)
	}
	facts := make([]LedgerFact, 0, 3)
	commands := make([]Command, 0, 3)
	receipts := make([]Receipt, 0, 3)
	for index := 0; index < 3; index++ {
		seed := []string{"seam-recovery-1", "seam-recovery-2", "seam-recovery-3"}[index]
		payloadRef := putCanonicalPayload(t, storeA, DispatchPayload{
			Target:         "target-" + seed,
			WorkloadDigest: fixedDigest("workload-" + seed),
		})
		fact := LedgerFact{Sequence: int64(index + 1), FactDigest: fixedDigest("seam-fact-" + seed), PayloadDigest: payloadRef}
		command, err := engineA.DeriveCommand(fact, CommandKindDispatch)
		if err != nil {
			t.Fatalf("DeriveCommand rejected fact %d: %v", index+1, err)
		}
		receipt, err := engineA.Deliver(ctx, command)
		if err != nil {
			t.Fatalf("Deliver rejected command %d: %v", index+1, err)
		}
		facts = append(facts, fact)
		commands = append(commands, command)
		receipts = append(receipts, receipt)
	}

	// Recovery with committed receipt facts and a fresh empty backend: no
	// redelivery is needed and backend internal state plays no role. The
	// durable payload store survives the crash.
	backendB := newLocalBackend(t, t.TempDir(), storeA)
	engineB, err := New(namespace, backendB)
	if err != nil {
		t.Fatalf("New rejected the phase-2 construction: %v", err)
	}
	for index, fact := range facts {
		if _, err := engineB.DeriveCommand(fact, CommandKindDispatch); err != nil {
			t.Fatalf("phase-2 DeriveCommand rejected fact %d: %v", index+1, err)
		}
		if err := engineB.ConsolidateReceipt(receipts[index]); err != nil {
			t.Fatalf("phase-2 ConsolidateReceipt rejected receipt %d: %v", index+1, err)
		}
	}
	redelivered, err := engineB.RedeliverPending(ctx)
	if err != nil {
		t.Fatalf("phase-2 RedeliverPending failed: %v", err)
	}
	if len(redelivered) != 0 {
		t.Fatalf("committed receipt facts must leave nothing pending, got %d redeliveries", len(redelivered))
	}
	if commands := backendB.ConsumedCommands(); len(commands) != 0 {
		t.Fatalf("phase-2 must not deliver anything to the fresh backend, got %d", len(commands))
	}

	// Recovery with receipts lost in the crash window (not yet committed to
	// the ledger): the journal re-derives every command and redelivers
	// through a fresh empty backend.
	backendC := newLocalBackend(t, t.TempDir(), storeA)
	engineC, err := New(namespace, backendC)
	if err != nil {
		t.Fatalf("New rejected the phase-3 construction: %v", err)
	}
	for index, fact := range facts {
		command, err := engineC.DeriveCommand(fact, CommandKindDispatch)
		if err != nil {
			t.Fatalf("phase-3 DeriveCommand rejected fact %d: %v", index+1, err)
		}
		if command.CommandId != commands[index].CommandId {
			t.Fatalf("phase-3 derivation must be stable: %s != %s", command.CommandId, commands[index].CommandId)
		}
	}
	if pending := engineC.Journal().Pending(); len(pending) != 3 {
		t.Fatalf("lost receipts must leave all three commands pending, got %d", len(pending))
	}
	redeliveredC, err := engineC.RedeliverPending(ctx)
	if err != nil {
		t.Fatalf("phase-3 RedeliverPending failed: %v", err)
	}
	if len(redeliveredC) != 3 {
		t.Fatalf("phase-3 must redeliver exactly the three undelivered commands, got %d", len(redeliveredC))
	}
	if commands := backendC.ConsumedCommands(); len(commands) != 3 {
		t.Fatalf("phase-3 backend must consume exactly the three redelivered commands, got %d", len(commands))
	}

	// Recovery over the retained pre-crash backend state: redelivery merges
	// idempotently and keeps the authoritative deliveredAt.
	backendD := newLocalBackend(t, rootA, storeA)
	engineD, err := New(namespace, backendD)
	if err != nil {
		t.Fatalf("New rejected the phase-4 construction: %v", err)
	}
	for _, fact := range facts {
		if _, err := engineD.DeriveCommand(fact, CommandKindDispatch); err != nil {
			t.Fatalf("phase-4 DeriveCommand rejected a fact: %v", err)
		}
	}
	redeliveredD, err := engineD.RedeliverPending(ctx)
	if err != nil {
		t.Fatalf("phase-4 RedeliverPending failed: %v", err)
	}
	if len(redeliveredD) != 3 {
		t.Fatalf("phase-4 must redeliver the three journal-pending commands, got %d", len(redeliveredD))
	}
	for index, receipt := range redeliveredD {
		if receipt.DeliveredAt != receipts[index].DeliveredAt {
			t.Fatalf("phase-4 merged delivery must keep the authoritative deliveredAt: %s != %s", receipt.DeliveredAt, receipts[index].DeliveredAt)
		}
		if receipt.AttemptSeq != 2 {
			t.Fatalf("phase-4 merged delivery must count the redelivery attempt, got %d", receipt.AttemptSeq)
		}
	}
	if commands := backendD.ConsumedCommands(); len(commands) != 3 {
		t.Fatalf("phase-4 must never consume a command twice, got %d", len(commands))
	}
}

func TestLocalBackendCloseSemantics(t *testing.T) {
	store := newMemoryPayloadStore()
	backend := newLocalBackend(t, t.TempDir(), store)
	command := localCommand(t, store, CommandKindDispatch, "close-semantics", DispatchPayload{
		Target:         "target-" + "closed",
		WorkloadDigest: fixedDigest("workload-" + "closed"),
	})
	if err := backend.Close(); err != nil {
		t.Fatalf("Close rejected an open backend: %v", err)
	}
	if _, err := backend.Deliver(context.Background(), command); !errors.Is(err, ErrLocalBackendClosed) {
		t.Fatalf("Deliver after Close must fail closed with ErrLocalBackendClosed, got %v", err)
	}
	if err := backend.Close(); !errors.Is(err, ErrLocalBackendClosed) {
		t.Fatalf("a second Close must fail closed with ErrLocalBackendClosed, got %v", err)
	}
}
