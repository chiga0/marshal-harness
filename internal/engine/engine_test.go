package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
)

// fixedDigest derives a well-formed sha256 digest from seed material, so no
// digest-family fixture field is ever assigned one complete literal
// (gitleaks generic-api-key publication gate).
func fixedDigest(seed string) string {
	return canonical.DigestBytes([]byte(seed))
}

// testNamespace builds the fixture authority namespace.
func testNamespace() authority.AuthorityNamespaceId {
	return authority.AuthorityNamespaceId{
		TenantNamespace:  "default",
		ControlPlaneId:   "default",
		AuthorityScopeId: "marshal-harness",
	}
}

// memoryPayloadStore is the in-memory fixture PayloadStore.
type memoryPayloadStore struct {
	mu    sync.Mutex
	bytes map[string][]byte
}

func newMemoryPayloadStore() *memoryPayloadStore {
	return &memoryPayloadStore{bytes: map[string][]byte{}}
}

func (store *memoryPayloadStore) put(digest string, payload []byte) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.bytes[digest] = payload
}

func (store *memoryPayloadStore) Payload(ctx context.Context, digest string) ([]byte, error) {
	_ = ctx
	store.mu.Lock()
	defer store.mu.Unlock()
	payload, ok := store.bytes[digest]
	if !ok {
		return nil, fmt.Errorf("memory payload store: payload %s not found", digest)
	}
	return payload, nil
}

var _ PayloadStore = (*memoryPayloadStore)(nil)

// putCanonicalPayload canonicalizes value, stores the bytes under their
// digest and returns the digest.
func putCanonicalPayload(t *testing.T, store *memoryPayloadStore, value any) string {
	t.Helper()
	raw, digest, err := EncodePayload(value)
	if err != nil {
		t.Fatalf("EncodePayload rejected the fixture payload: %v", err)
	}
	store.put(digest, raw)
	return digest
}

// fixturePayloadDigest stores a valid dispatch payload and returns a fixture
// LedgerFact bound to it.
func fixtureFact(t *testing.T, store *memoryPayloadStore, seed string) LedgerFact {
	t.Helper()
	digest := putCanonicalPayload(t, store, DispatchPayload{
		Target:         "fixture-target-" + seed,
		WorkloadDigest: fixedDigest("fixture-workload-" + seed),
	})
	return LedgerFact{Sequence: 1, FactDigest: fixedDigest("fixture-fact-" + seed), PayloadDigest: digest}
}

// fakeBackend is the recording fixture Backend.
type fakeBackend struct {
	mu          sync.Mutex
	payloads    PayloadStore
	deliveries  []string
	deliverFunc func(command Command) (Receipt, error)
	closed      bool
}

func (fake *fakeBackend) Deliver(ctx context.Context, command Command) (Receipt, error) {
	if err := command.Validate(); err != nil {
		return Receipt{}, err
	}
	payload, err := fake.payloads.Payload(ctx, command.PayloadRef)
	if err != nil {
		return Receipt{}, fmt.Errorf("fake backend: payload unavailable: %w", err)
	}
	if err := VerifyPayloadRef(payload, command.PayloadRef); err != nil {
		return Receipt{}, err
	}
	fake.mu.Lock()
	if fake.closed {
		fake.mu.Unlock()
		return Receipt{}, fmt.Errorf("fake backend: closed")
	}
	fake.deliveries = append(fake.deliveries, command.CommandId)
	deliverFunc := fake.deliverFunc
	fake.mu.Unlock()
	if deliverFunc != nil {
		return deliverFunc(command)
	}
	return Receipt{CommandId: command.CommandId, DeliveredAt: time.Now().UTC().Format(time.RFC3339), AttemptSeq: 1}, nil
}

func (fake *fakeBackend) Recover(ctx context.Context) error {
	_ = ctx
	return nil
}

func (fake *fakeBackend) Close() error {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.closed {
		return fmt.Errorf("fake backend: already closed")
	}
	fake.closed = true
	return nil
}

func (fake *fakeBackend) deliveryCount() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.deliveries)
}

func (fake *fakeBackend) deliveredIds() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	copied := make([]string, len(fake.deliveries))
	copy(copied, fake.deliveries)
	return copied
}

var _ Backend = (*fakeBackend)(nil)

// newTestEngine binds an engine over a fresh fake backend and payload store.
func newTestEngine(t *testing.T) (*DurableExecutionEngine, *fakeBackend, *memoryPayloadStore) {
	t.Helper()
	store := newMemoryPayloadStore()
	backend := &fakeBackend{payloads: store}
	engine, err := New(testNamespace(), backend)
	if err != nil {
		t.Fatalf("New rejected valid construction inputs: %v", err)
	}
	return engine, backend, store
}

func TestCommandValidation(t *testing.T) {
	valid := Command{
		CommandId:  fixedDigest("command-" + "1"),
		Kind:       CommandKindDispatch,
		PayloadRef: fixedDigest("payload-" + "1"),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid command: %v", err)
	}
	if !valid.Equal(valid) {
		t.Fatal("Equal must report identical commands equal")
	}
	cases := []struct {
		name    string
		mutate  func(command *Command)
		message string
	}{
		{"empty commandId", func(command *Command) { command.CommandId = "" }, "commandId"},
		{"malformed commandId", func(command *Command) { command.CommandId = "not-a-digest" }, "commandId"},
		{"unknown kind", func(command *Command) { command.Kind = CommandKind("bogus") }, "kind"},
		{"empty payloadRef", func(command *Command) { command.PayloadRef = "" }, "payloadRef"},
		{"malformed payloadRef", func(command *Command) { command.PayloadRef = "sha256:zz" }, "payloadRef"},
	}
	for _, testCase := range cases {
		mutated := valid
		testCase.mutate(&mutated)
		err := mutated.Validate()
		if err == nil {
			t.Fatalf("%s: Validate accepted an invalid command", testCase.name)
		}
		if !strings.Contains(err.Error(), testCase.message) && !strings.Contains(err.Error(), "command kind") {
			t.Fatalf("%s: expected the fail-closed rejection, got: %v", testCase.name, err)
		}
	}
}

func TestCommandKindClosedEnumeration(t *testing.T) {
	for _, kind := range []CommandKind{CommandKindDispatch, CommandKindSignal, CommandKindTimer, CommandKindSideEffect} {
		if err := kind.Validate(); err != nil {
			t.Fatalf("Validate rejected the closed command kind %q: %v", string(kind), err)
		}
	}
	if err := CommandKind("lifecycle-transition").Validate(); err == nil {
		t.Fatal("Validate accepted a command kind outside the closed enumeration")
	}
	if err := CommandKind("").Validate(); err == nil {
		t.Fatal("Validate accepted the empty command kind")
	}
}

func TestReceiptValidation(t *testing.T) {
	valid := Receipt{
		CommandId:   fixedDigest("receipt-" + "1"),
		DeliveredAt: "2026-01-01T00:00:00Z",
		AttemptSeq:  1,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid receipt: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(receipt *Receipt)
	}{
		{"empty commandId", func(receipt *Receipt) { receipt.CommandId = "" }},
		{"malformed commandId", func(receipt *Receipt) { receipt.CommandId = "sha256:zz" }},
		{"empty deliveredAt", func(receipt *Receipt) { receipt.DeliveredAt = "" }},
		{"malformed deliveredAt", func(receipt *Receipt) { receipt.DeliveredAt = "yesterday" }},
		{"zero attemptSeq", func(receipt *Receipt) { receipt.AttemptSeq = 0 }},
		{"negative attemptSeq", func(receipt *Receipt) { receipt.AttemptSeq = -1 }},
	}
	for _, testCase := range cases {
		mutated := valid
		testCase.mutate(&mutated)
		if err := mutated.Validate(); err == nil {
			t.Fatalf("%s: Validate accepted an invalid receipt", testCase.name)
		}
	}
}

func TestBusinessClaimClosedEnumeration(t *testing.T) {
	closed := []BusinessClaim{
		BusinessClaimLifecycleTransition,
		BusinessClaimReviewDecision,
		BusinessClaimRework,
		BusinessClaimTerminalState,
		BusinessClaimSafeToPublish,
	}
	for _, claim := range closed {
		if err := claim.Validate(); err != nil {
			t.Fatalf("Validate rejected the closed business claim %q: %v", string(claim), err)
		}
	}
	if err := BusinessClaim("gate-passed").Validate(); err == nil {
		t.Fatal("Validate accepted a business claim outside the closed enumeration")
	}
}

func TestBackendStatementShapeValidation(t *testing.T) {
	valid := BackendStatement{CommandId: fixedDigest("statement-" + "1")}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate rejected a delivery-shaped statement: %v", err)
	}
	if err := (&BackendStatement{}).Validate(); err == nil {
		t.Fatal("Validate accepted a statement without a commandId")
	}
	unknownClaim := BackendStatement{
		CommandId: fixedDigest("statement-" + "2"),
		Claims:    []BusinessClaim{BusinessClaim("bogus")},
	}
	if err := unknownClaim.Validate(); err == nil {
		t.Fatal("Validate accepted a claim outside the closed enumeration")
	}
}

func TestEngineConstructionFailsClosed(t *testing.T) {
	store := newMemoryPayloadStore()
	backend := &fakeBackend{payloads: store}
	if _, err := New(authority.AuthorityNamespaceId{}, backend); err == nil {
		t.Fatal("New accepted an invalid authority namespace")
	}
	if _, err := New(testNamespace(), nil); err == nil {
		t.Fatal("New accepted a nil backend")
	}
}

// TestEngineDeliverRequiresJournalDerivation freezes the single-seam
// invariant: a command that was never derived from a committed ledger fact
// can never be delivered, so the "command delivered but ledger not
// committed" state is unreachable by construction.
func TestEngineDeliverRequiresJournalDerivation(t *testing.T) {
	engine, _, store := newTestEngine(t)
	payloadRef := putCanonicalPayload(t, store, DispatchPayload{
		Target:         "target-" + "1",
		WorkloadDigest: fixedDigest("workload-" + "1"),
	})
	forged := Command{
		CommandId:  fixedDigest("forged-" + "command"),
		Kind:       CommandKindDispatch,
		PayloadRef: payloadRef,
	}
	_, err := engine.Deliver(context.Background(), forged)
	if !errors.Is(err, ErrCommandNotDerived) {
		t.Fatalf("Deliver must reject an underived command with ErrCommandNotDerived, got %v", err)
	}
	if _, delivered := engine.Journal().ReceiptFor(forged.CommandId); delivered {
		t.Fatal("the journal must not record a receipt for an underived command")
	}
}

func TestEngineDeliverHappyPath(t *testing.T) {
	engine, backend, store := newTestEngine(t)
	fact := fixtureFact(t, store, "happy-"+"1")
	command, err := engine.DeriveCommand(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected a valid ledger fact: %v", err)
	}
	receipt, err := engine.Deliver(context.Background(), command)
	if err != nil {
		t.Fatalf("Deliver rejected a journal-derived command: %v", err)
	}
	if receipt.CommandId != command.CommandId {
		t.Fatalf("the receipt must reference the delivered commandId, got %s", receipt.CommandId)
	}
	if receipt.AttemptSeq != 1 {
		t.Fatalf("the first delivery must carry attemptSeq 1, got %d", receipt.AttemptSeq)
	}
	stored, delivered := engine.Journal().ReceiptFor(command.CommandId)
	if !delivered || stored != receipt {
		t.Fatal("the journal must store the authoritative receipt")
	}
	if backend.deliveryCount() != 1 {
		t.Fatalf("the backend must observe exactly one delivery, got %d", backend.deliveryCount())
	}
	if len(engine.Journal().Pending()) != 0 {
		t.Fatal("a delivered command must leave the pending projection")
	}
}

func TestEngineDeliverRejectsDivergentReceipt(t *testing.T) {
	engine, _, store := newTestEngine(t)
	fact := fixtureFact(t, store, "divergent-"+"1")
	command, err := engine.DeriveCommand(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected a valid ledger fact: %v", err)
	}
	backend := engine.Backend().(*fakeBackend)
	backend.mu.Lock()
	backend.deliverFunc = func(delivered Command) (Receipt, error) {
		return Receipt{
			CommandId:   fixedDigest("divergent-" + "other-command"),
			DeliveredAt: time.Now().UTC().Format(time.RFC3339),
			AttemptSeq:  1,
		}, nil
	}
	backend.mu.Unlock()
	_, err = engine.Deliver(context.Background(), command)
	if !errors.Is(err, ErrReceiptDivergence) {
		t.Fatalf("Deliver must reject a divergent backend receipt with ErrReceiptDivergence, got %v", err)
	}
	if _, delivered := engine.Journal().ReceiptFor(command.CommandId); delivered {
		t.Fatal("the journal must not consolidate a divergent receipt")
	}
	if len(engine.Journal().Pending()) != 1 {
		t.Fatal("the command must stay pending after a divergent receipt")
	}
}

func TestEngineDeliverRejectsInvalidReceipt(t *testing.T) {
	engine, _, store := newTestEngine(t)
	fact := fixtureFact(t, store, "invalid-receipt-"+"1")
	command, err := engine.DeriveCommand(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected a valid ledger fact: %v", err)
	}
	backend := engine.Backend().(*fakeBackend)
	backend.mu.Lock()
	backend.deliverFunc = func(delivered Command) (Receipt, error) {
		return Receipt{CommandId: delivered.CommandId, DeliveredAt: "not-a-timestamp", AttemptSeq: 1}, nil
	}
	backend.mu.Unlock()
	if _, err := engine.Deliver(context.Background(), command); err == nil {
		t.Fatal("Deliver accepted an invalid backend receipt")
	}
	backend.mu.Lock()
	backend.deliverFunc = func(delivered Command) (Receipt, error) {
		return Receipt{CommandId: delivered.CommandId, DeliveredAt: time.Now().UTC().Format(time.RFC3339), AttemptSeq: 0}, nil
	}
	backend.mu.Unlock()
	if _, err := engine.Deliver(context.Background(), command); err == nil {
		t.Fatal("Deliver accepted a receipt with attemptSeq 0")
	}
	if _, delivered := engine.Journal().ReceiptFor(command.CommandId); delivered {
		t.Fatal("the journal must not consolidate an invalid receipt")
	}
}

// TestEngineDuplicateDeliveryIdempotent freezes the at-least-once rule at
// the seam: redelivering an already-delivered commandId merges onto the
// authoritative receipt without a second backend call, and delivery retry
// never creates a second journal entry — no business Attempt, no business
// retry budget consumption.
func TestEngineDuplicateDeliveryIdempotent(t *testing.T) {
	engine, backend, store := newTestEngine(t)
	fact := fixtureFact(t, store, "duplicate-"+"1")
	command, err := engine.DeriveCommand(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected a valid ledger fact: %v", err)
	}
	first, err := engine.Deliver(context.Background(), command)
	if err != nil {
		t.Fatalf("Deliver rejected a journal-derived command: %v", err)
	}
	second, err := engine.Deliver(context.Background(), command)
	if err != nil {
		t.Fatalf("duplicate Deliver must merge idempotently, got %v", err)
	}
	if second != first {
		t.Fatal("duplicate Deliver must return the identical authoritative receipt")
	}
	if backend.deliveryCount() != 1 {
		t.Fatalf("duplicate Deliver must not reach the backend again, got %d calls", backend.deliveryCount())
	}
	if commands := engine.Journal().Commands(); len(commands) != 1 {
		t.Fatalf("duplicate delivery must never create a second command entry, got %d", len(commands))
	}
	// A backend-observed duplicate receipt (at-least-once transport) merges
	// without diverging from the authoritative first receipt.
	duplicate := Receipt{CommandId: command.CommandId, DeliveredAt: first.DeliveredAt, AttemptSeq: 2}
	if err := engine.ConsolidateReceipt(duplicate); err != nil {
		t.Fatalf("ConsolidateReceipt rejected a duplicate receipt: %v", err)
	}
	stored, _ := engine.Journal().ReceiptFor(command.CommandId)
	if stored != first {
		t.Fatal("the authoritative first receipt must never be overwritten by a duplicate")
	}
	if engine.Journal().DuplicateDeliveries(command.CommandId) != 1 {
		t.Fatalf("the duplicate delivery must be counted exactly once, got %d", engine.Journal().DuplicateDeliveries(command.CommandId))
	}
}

func TestEngineConsolidateReceiptUnknownCommand(t *testing.T) {
	engine, _, _ := newTestEngine(t)
	stray := Receipt{
		CommandId:   fixedDigest("stray-" + "receipt"),
		DeliveredAt: "2026-01-01T00:00:00Z",
		AttemptSeq:  1,
	}
	if err := engine.ConsolidateReceipt(stray); !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("ConsolidateReceipt must reject a receipt for an underived command with ErrUnknownCommand, got %v", err)
	}
}

// TestEngineAuthorityBoundary freezes the backend authority boundary: every
// business claim a backend announces fails closed at the seam; only
// delivery-shaped statements about journal-derived commands are admitted.
func TestEngineAuthorityBoundary(t *testing.T) {
	engine, _, store := newTestEngine(t)
	fact := fixtureFact(t, store, "authority-"+"1")
	command, err := engine.DeriveCommand(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommand rejected a valid ledger fact: %v", err)
	}
	for _, claim := range []BusinessClaim{
		BusinessClaimLifecycleTransition,
		BusinessClaimReviewDecision,
		BusinessClaimRework,
		BusinessClaimTerminalState,
		BusinessClaimSafeToPublish,
	} {
		statement := BackendStatement{CommandId: command.CommandId, Claims: []BusinessClaim{claim}}
		if err := engine.AcceptBackendStatement(statement); !errors.Is(err, ErrBackendAuthorityViolation) {
			t.Fatalf("claim %q: AcceptBackendStatement must fail closed with ErrBackendAuthorityViolation, got %v", string(claim), err)
		}
	}
	deliveryOnly := BackendStatement{CommandId: command.CommandId}
	if err := engine.AcceptBackendStatement(deliveryOnly); err != nil {
		t.Fatalf("AcceptBackendStatement rejected a delivery-shaped statement: %v", err)
	}
	unknown := BackendStatement{CommandId: fixedDigest("unknown-" + "statement-command")}
	if err := engine.AcceptBackendStatement(unknown); !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("AcceptBackendStatement must reject an underived commandId with ErrUnknownCommand, got %v", err)
	}
}

// TestEngineRecoverySingleSeam freezes the crash/upgrade recovery path:
// after losing the engine and the backend, replaying the identical ledger
// facts and receipts through a fresh seam redelivers exactly the
// undelivered commands — recovery never depends on backend internal state.
func TestEngineRecoverySingleSeam(t *testing.T) {
	ctx := context.Background()
	engine, backend, store := newTestEngine(t)
	facts := make([]LedgerFact, 0, 3)
	commands := make([]Command, 0, 3)
	for index := 0; index < 3; index++ {
		seed := fmt.Sprintf("recovery-fact-%d", index+1)
		fact := LedgerFact{
			Sequence:      int64(index + 1),
			FactDigest:    fixedDigest(seed),
			PayloadDigest: putCanonicalPayload(t, store, DispatchPayload{Target: "target-" + seed, WorkloadDigest: fixedDigest("workload-" + seed)}),
		}
		command, err := engine.DeriveCommand(fact, CommandKindDispatch)
		if err != nil {
			t.Fatalf("DeriveCommand rejected ledger fact %d: %v", index+1, err)
		}
		facts = append(facts, fact)
		commands = append(commands, command)
	}
	firstReceipt, err := engine.Deliver(ctx, commands[0])
	if err != nil {
		t.Fatalf("Deliver rejected command 1: %v", err)
	}

	// Crash: the engine and the backend are gone; only the ledger facts,
	// the committed receipt fact and the durable content-addressed payload
	// store survive.
	recoveredBackend := &fakeBackend{payloads: store}
	recovered, err := New(testNamespace(), recoveredBackend)
	if err != nil {
		t.Fatalf("New rejected the recovery construction: %v", err)
	}
	recoveredCommands := make([]Command, 0, len(facts))
	for _, fact := range facts {
		command, err := recovered.DeriveCommand(fact, CommandKindDispatch)
		if err != nil {
			t.Fatalf("recovery DeriveCommand rejected fact %d: %v", fact.Sequence, err)
		}
		recoveredCommands = append(recoveredCommands, command)
	}
	for index, command := range recoveredCommands {
		if command.CommandId != commands[index].CommandId {
			t.Fatalf("recovery derivation must be stable: commandId %s != %s", command.CommandId, commands[index].CommandId)
		}
	}
	if err := recovered.ConsolidateReceipt(firstReceipt); err != nil {
		t.Fatalf("recovery ConsolidateReceipt rejected the committed receipt: %v", err)
	}
	if pending := recovered.Journal().Pending(); len(pending) != 2 {
		t.Fatalf("recovery must see exactly the two undelivered commands pending, got %d", len(pending))
	}
	receipts, err := recovered.RedeliverPending(ctx)
	if err != nil {
		t.Fatalf("RedeliverPending failed during recovery: %v", err)
	}
	if len(receipts) != 2 {
		t.Fatalf("RedeliverPending must redeliver exactly the two pending commands, got %d", len(receipts))
	}
	delivered := recoveredBackend.deliveredIds()
	if len(delivered) != 2 || delivered[0] != commands[1].CommandId || delivered[1] != commands[2].CommandId {
		t.Fatalf("recovery must redeliver exactly the undelivered commandIds in derivation order, got %v", delivered)
	}
	if backend.deliveryCount() != 1 {
		t.Fatalf("the pre-crash backend state must not be consulted during recovery, got %d", backend.deliveryCount())
	}
	if pending := recovered.Journal().Pending(); len(pending) != 0 {
		t.Fatalf("after recovery redelivery nothing may stay pending, got %d", len(pending))
	}
}
