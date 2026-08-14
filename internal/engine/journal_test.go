package engine

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/provider"
)

func TestLedgerFactValidation(t *testing.T) {
	valid := LedgerFact{
		Sequence:      1,
		FactDigest:    fixedDigest("fact-" + "1"),
		PayloadDigest: fixedDigest("payload-" + "1"),
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate rejected a valid ledger fact: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(fact *LedgerFact)
	}{
		{"zero sequence", func(fact *LedgerFact) { fact.Sequence = 0 }},
		{"negative sequence", func(fact *LedgerFact) { fact.Sequence = -3 }},
		{"empty factDigest", func(fact *LedgerFact) { fact.FactDigest = "" }},
		{"malformed factDigest", func(fact *LedgerFact) { fact.FactDigest = "sha256:zz" }},
		{"empty payloadDigest", func(fact *LedgerFact) { fact.PayloadDigest = "" }},
		{"malformed payloadDigest", func(fact *LedgerFact) { fact.PayloadDigest = "digest" }},
	}
	for _, testCase := range cases {
		mutated := valid
		testCase.mutate(&mutated)
		if err := mutated.Validate(); err == nil {
			t.Fatalf("%s: Validate accepted an invalid ledger fact", testCase.name)
		}
	}
}

// TestDeriveCommandIdDeterministic freezes the stable commandId derivation:
// identical ledger fact digest and kind always yield the identical
// commandId; distinct facts or kinds yield distinct commandIds; no random
// source, clock read, backend identity or worker build participates.
func TestDeriveCommandIdDeterministic(t *testing.T) {
	factDigest := fixedDigest("derivation-" + "fact")
	first, err := DeriveCommandId(factDigest, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommandId rejected valid inputs: %v", err)
	}
	second, err := DeriveCommandId(factDigest, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommandId rejected the identical inputs: %v", err)
	}
	if first != second {
		t.Fatalf("identical derivation inputs must yield the identical commandId: %s != %s", first, second)
	}
	otherFact, err := DeriveCommandId(fixedDigest("derivation-"+"other-fact"), CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommandId rejected valid inputs: %v", err)
	}
	if otherFact == first {
		t.Fatal("distinct ledger fact digests must yield distinct commandIds")
	}
	otherKind, err := DeriveCommandId(factDigest, CommandKindTimer)
	if err != nil {
		t.Fatalf("DeriveCommandId rejected valid inputs: %v", err)
	}
	if otherKind == first {
		t.Fatal("distinct command kinds must yield distinct commandIds")
	}
	if _, err := DeriveCommandId("not-a-digest", CommandKindDispatch); err == nil {
		t.Fatal("DeriveCommandId accepted a malformed fact digest")
	}
	if _, err := DeriveCommandId(factDigest, CommandKind("bogus")); err == nil {
		t.Fatal("DeriveCommandId accepted a command kind outside the closed enumeration")
	}
}

func TestJournalDeriveRegistersPending(t *testing.T) {
	store := newMemoryPayloadStore()
	journal, err := NewCommandJournal(testNamespace())
	if err != nil {
		t.Fatalf("NewCommandJournal rejected a valid namespace: %v", err)
	}
	if _, err := NewCommandJournal(authority.AuthorityNamespaceId{}); err == nil {
		t.Fatal("NewCommandJournal accepted an invalid namespace")
	}
	fact := fixtureFact(t, store, "pending-"+"1")
	command, err := journal.Derive(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("Derive rejected a valid ledger fact: %v", err)
	}
	if err := command.Validate(); err != nil {
		t.Fatalf("the derived command does not validate: %v", err)
	}
	if command.Kind != CommandKindDispatch {
		t.Fatalf("the derived command must carry the derivation kind, got %q", string(command.Kind))
	}
	if command.PayloadRef != fact.PayloadDigest {
		t.Fatal("the derived command payloadRef must be the ledger-bound payload digest")
	}
	expectedId, err := DeriveCommandId(fact.FactDigest, CommandKindDispatch)
	if err != nil {
		t.Fatalf("DeriveCommandId failed: %v", err)
	}
	if command.CommandId != expectedId {
		t.Fatalf("the derived commandId must equal the stable derivation, got %s", command.CommandId)
	}
	pending := journal.Pending()
	if len(pending) != 1 || pending[0] != command {
		t.Fatal("a freshly derived command must be pending")
	}
	if _, known := journal.Command(command.CommandId); !known {
		t.Fatal("Command must return the derived command")
	}
	if _, known := journal.Command(fixedDigest("unknown-" + "command")); known {
		t.Fatal("Command must report an unknown commandId as unknown")
	}
}

// TestJournalDuplicateDerivationMerges freezes the replay rule: deriving the
// identical fact and kind again merges onto the identical commandId without
// creating a second entry and without disturbing the pending projection.
func TestJournalDuplicateDerivationMerges(t *testing.T) {
	store := newMemoryPayloadStore()
	journal, err := NewCommandJournal(testNamespace())
	if err != nil {
		t.Fatalf("NewCommandJournal rejected a valid namespace: %v", err)
	}
	fact := fixtureFact(t, store, "merge-"+"1")
	first, err := journal.Derive(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("Derive rejected a valid ledger fact: %v", err)
	}
	second, err := journal.Derive(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("replay Derive must merge idempotently, got %v", err)
	}
	if second != first {
		t.Fatal("replay derivation must return the identical command")
	}
	if commands := journal.Commands(); len(commands) != 1 {
		t.Fatalf("replay derivation must never create a second entry, got %d", len(commands))
	}
	if pending := journal.Pending(); len(pending) != 1 {
		t.Fatalf("replay derivation must not disturb the pending projection, got %d", len(pending))
	}
	if journal.DuplicateDerivations(first.CommandId) != 1 {
		t.Fatalf("the replayed derivation must be counted exactly once, got %d", journal.DuplicateDerivations(first.CommandId))
	}
}

// TestJournalConflictingDerivationFailsClosed freezes the conflict rule: a
// divergent derivation colliding on a commandId never merges.
func TestJournalConflictingDerivationFailsClosed(t *testing.T) {
	store := newMemoryPayloadStore()
	journal, err := NewCommandJournal(testNamespace())
	if err != nil {
		t.Fatalf("NewCommandJournal rejected a valid namespace: %v", err)
	}
	fact := fixtureFact(t, store, "conflict-"+"1")
	if _, err := journal.Derive(fact, CommandKindDispatch); err != nil {
		t.Fatalf("Derive rejected a valid ledger fact: %v", err)
	}
	divergentPayload := fact
	divergentPayload.PayloadDigest = fixedDigest("conflict-" + "other-payload")
	if _, err := journal.Derive(divergentPayload, CommandKindDispatch); !errors.Is(err, ErrJournalConflict) {
		t.Fatalf("a divergent payload digest must fail closed with ErrJournalConflict, got %v", err)
	}
	divergentSequence := fact
	divergentSequence.Sequence = 2
	if _, err := journal.Derive(divergentSequence, CommandKindDispatch); !errors.Is(err, ErrJournalConflict) {
		t.Fatalf("a divergent ledger sequence must fail closed with ErrJournalConflict, got %v", err)
	}
	if journal.DuplicateDerivations(fixedDigest("conflict-"+"fact")) != 0 {
		t.Fatal("conflicting derivations must never count as merges")
	}
}

func TestJournalConsolidateReceiptRules(t *testing.T) {
	store := newMemoryPayloadStore()
	journal, err := NewCommandJournal(testNamespace())
	if err != nil {
		t.Fatalf("NewCommandJournal rejected a valid namespace: %v", err)
	}
	fact := fixtureFact(t, store, "receipt-rules-"+"1")
	command, err := journal.Derive(fact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("Derive rejected a valid ledger fact: %v", err)
	}
	stray := Receipt{CommandId: fixedDigest("stray-" + "command"), DeliveredAt: "2026-01-01T00:00:00Z", AttemptSeq: 1}
	if err := journal.ConsolidateReceipt(stray); !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("ConsolidateReceipt must reject an unknown commandId with ErrUnknownCommand, got %v", err)
	}
	invalid := Receipt{CommandId: command.CommandId, DeliveredAt: "not-a-timestamp", AttemptSeq: 1}
	if err := journal.ConsolidateReceipt(invalid); err == nil {
		t.Fatal("ConsolidateReceipt accepted an invalid receipt")
	}
	first := Receipt{CommandId: command.CommandId, DeliveredAt: "2026-01-01T00:00:00Z", AttemptSeq: 1}
	if err := journal.ConsolidateReceipt(first); err != nil {
		t.Fatalf("ConsolidateReceipt rejected the authoritative receipt: %v", err)
	}
	if _, delivered := journal.ReceiptFor(command.CommandId); !delivered {
		t.Fatal("the command must be delivered after the first receipt")
	}
	if pending := journal.Pending(); len(pending) != 0 {
		t.Fatalf("a delivered command must leave the pending projection, got %d", len(pending))
	}
	duplicate := Receipt{CommandId: command.CommandId, DeliveredAt: first.DeliveredAt, AttemptSeq: 2}
	if err := journal.ConsolidateReceipt(duplicate); err != nil {
		t.Fatalf("ConsolidateReceipt rejected a duplicate receipt: %v", err)
	}
	stored, _ := journal.ReceiptFor(command.CommandId)
	if stored != first {
		t.Fatal("the authoritative first receipt must never be overwritten")
	}
	if journal.DuplicateDeliveries(command.CommandId) != 1 {
		t.Fatalf("the duplicate receipt must be counted exactly once, got %d", journal.DuplicateDeliveries(command.CommandId))
	}
}

// TestJournalReplayDeterminism freezes the deterministic rebuild: replaying
// the identical ordered ledger fact stream into a fresh journal always
// rebuilds the identical state.
func TestJournalReplayDeterminism(t *testing.T) {
	store := newMemoryPayloadStore()
	facts := []LedgerFact{
		{Sequence: 1, FactDigest: fixedDigest("replay-fact-" + "1"), PayloadDigest: putCanonicalPayload(t, store, DispatchPayload{Target: "target-" + "1", WorkloadDigest: fixedDigest("workload-" + "1")})},
		{Sequence: 2, FactDigest: fixedDigest("replay-fact-" + "2"), PayloadDigest: putCanonicalPayload(t, store, TimerPayload{Target: "target-" + "2", FireAt: "2026-01-01T01:00:00Z"})},
		{Sequence: 3, FactDigest: fixedDigest("replay-fact-" + "3"), PayloadDigest: putCanonicalPayload(t, store, SignalPayload{Target: "target-" + "3", Name: "cancel-requested", Body: "body-" + "3"})},
	}
	kinds := []CommandKind{CommandKindDispatch, CommandKindTimer, CommandKindSignal}
	replay := func(t *testing.T) (*CommandJournal, []Command) {
		t.Helper()
		journal, err := NewCommandJournal(testNamespace())
		if err != nil {
			t.Fatalf("NewCommandJournal rejected a valid namespace: %v", err)
		}
		commands := make([]Command, 0, len(facts))
		for index, fact := range facts {
			command, err := journal.Derive(fact, kinds[index])
			if err != nil {
				t.Fatalf("Derive rejected fact %d: %v", fact.Sequence, err)
			}
			commands = append(commands, command)
		}
		receipt := Receipt{CommandId: commands[1].CommandId, DeliveredAt: "2026-01-01T00:30:00Z", AttemptSeq: 1}
		if err := journal.ConsolidateReceipt(receipt); err != nil {
			t.Fatalf("ConsolidateReceipt rejected the replayed receipt: %v", err)
		}
		return journal, commands
	}
	firstJournal, firstCommands := replay(t)
	secondJournal, secondCommands := replay(t)
	if !reflect.DeepEqual(firstCommands, secondCommands) {
		t.Fatal("identical replay streams must derive the identical commands")
	}
	if !reflect.DeepEqual(firstJournal.Commands(), secondJournal.Commands()) {
		t.Fatal("identical replay streams must rebuild the identical command projection")
	}
	if !reflect.DeepEqual(firstJournal.Pending(), secondJournal.Pending()) {
		t.Fatal("identical replay streams must rebuild the identical pending projection")
	}
	firstReceipt, firstDelivered := firstJournal.ReceiptFor(firstCommands[1].CommandId)
	secondReceipt, secondDelivered := secondJournal.ReceiptFor(secondCommands[1].CommandId)
	if !firstDelivered || !secondDelivered || firstReceipt != secondReceipt {
		t.Fatal("identical replay streams must rebuild the identical receipt state")
	}
	pending := firstJournal.Pending()
	if len(pending) != 2 || pending[0] != firstCommands[0] || pending[1] != firstCommands[2] {
		t.Fatalf("the pending projection must keep deterministic derivation order, got %v", pending)
	}
}

// TestPayloadEncodeDecodeRoundTrip guards the externalized payload schemas:
// canonical encoding, digest-verified reference binding and strict decoding.
func TestPayloadEncodeDecodeRoundTrip(t *testing.T) {
	dispatchPayload := DispatchPayload{Target: "target-" + "1", WorkloadDigest: fixedDigest("workload-" + "1")}
	raw, digest, err := EncodePayload(dispatchPayload)
	if err != nil {
		t.Fatalf("EncodePayload rejected the dispatch payload: %v", err)
	}
	if err := VerifyPayloadRef(raw, digest); err != nil {
		t.Fatalf("VerifyPayloadRef rejected the matching bytes: %v", err)
	}
	if err := VerifyPayloadRef(raw, fixedDigest("other-"+"ref")); err == nil {
		t.Fatal("VerifyPayloadRef accepted a mismatched payloadRef")
	}
	decodedDispatch, err := DecodeDispatchPayload(raw)
	if err != nil {
		t.Fatalf("DecodeDispatchPayload rejected canonical bytes: %v", err)
	}
	if decodedDispatch != dispatchPayload {
		t.Fatal("the dispatch payload round trip must be lossless")
	}

	timerPayload := TimerPayload{Target: "target-" + "2", FireAt: "2026-01-01T01:00:00Z"}
	timerRaw, _, err := EncodePayload(timerPayload)
	if err != nil {
		t.Fatalf("EncodePayload rejected the timer payload: %v", err)
	}
	decodedTimer, err := DecodeTimerPayload(timerRaw)
	if err != nil {
		t.Fatalf("DecodeTimerPayload rejected canonical bytes: %v", err)
	}
	if decodedTimer != timerPayload {
		t.Fatal("the timer payload round trip must be lossless")
	}

	signalPayload := SignalPayload{Target: "target-" + "3", Name: "cancel-requested", Body: ""}
	signalRaw, _, err := EncodePayload(signalPayload)
	if err != nil {
		t.Fatalf("EncodePayload rejected the signal payload: %v", err)
	}
	decodedSignal, err := DecodeSignalPayload(signalRaw)
	if err != nil {
		t.Fatalf("DecodeSignalPayload rejected canonical bytes: %v", err)
	}
	if decodedSignal != signalPayload {
		t.Fatal("the signal payload round trip must be lossless")
	}

	sideEffectPayload := SideEffectPayload{EffectId: "effect-" + "1", Port: "publication", Operation: "close-draft-pr", TargetRef: "ref-" + "1"}
	sideEffectRaw, _, err := EncodePayload(sideEffectPayload)
	if err != nil {
		t.Fatalf("EncodePayload rejected the side-effect payload: %v", err)
	}
	decodedSideEffect, err := DecodeSideEffectPayload(sideEffectRaw)
	if err != nil {
		t.Fatalf("DecodeSideEffectPayload rejected canonical bytes: %v", err)
	}
	if decodedSideEffect != sideEffectPayload {
		t.Fatal("the side-effect payload round trip must be lossless")
	}

	unknownField := []byte(`{"target":"x","workloadDigest":"` + fixedDigest("workload-"+"2") + `","surprise":1}`)
	if _, err := DecodeDispatchPayload(unknownField); !errors.Is(err, ErrPayloadRejected) {
		t.Fatalf("DecodeDispatchPayload must reject unknown fields with ErrPayloadRejected, got %v", err)
	}
	missingField := []byte(`{"target":"x"}`)
	if _, err := DecodeDispatchPayload(missingField); err == nil {
		t.Fatal("DecodeDispatchPayload accepted a payload missing the workload digest")
	}
	badTimer := []byte(`{"target":"x","fireAt":"yesterday"}`)
	if _, err := DecodeTimerPayload(badTimer); err == nil {
		t.Fatal("DecodeTimerPayload accepted a malformed fire time")
	}
}

// interopLease builds a sealed active DispatchLease through the exported
// dispatch API only (read-only consumption of the M9-a lease ledger
// contract).
func interopLease(seed string) dispatch.DispatchLease {
	lease := dispatch.DispatchLease{
		LeaseId:                          fixedDigest("engine-interop-lease-" + seed),
		AuthorityNamespaceId:             testNamespace(),
		SecurityDomainId:                 authority.SecurityDomainId{TenantNamespace: "default", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "isolation-local"},
		RegistrationId:                   "engine-interop-registration-" + seed,
		ProviderCapabilitySnapshotDigest: fixedDigest("engine-interop-snapshot-" + seed),
		ConformanceEvidenceDigests:       []string{fixedDigest("engine-interop-evidence-" + seed)},
		Attestation: provider.Attestation{
			ProviderInstanceId: "engine-interop-instance-" + seed,
			ConfigDigest:       fixedDigest("engine-interop-config-" + seed),
			TrustRootKeyId:     "engine-interop-key-" + seed,
			TrustRootAlgorithm: "ed25519",
		},
		TaskId:        "engine-interop-task-" + seed,
		RunId:         "engine-interop-run-" + seed,
		AttemptId:     "engine-interop-attempt-" + seed,
		AllocationId:  "engine-interop-allocation-" + seed,
		Generation:    1,
		FencingToken:  fixedDigest("engine-interop-fencing-" + seed),
		AckDeadlineAt: "2026-01-01T00:30:00Z",
		ExpiresAt:     "2026-01-01T02:00:00Z",
		LeaseState:    dispatch.LeaseStateActive,
		CreatedAt:     "2026-01-01T00:00:00Z",
	}
	digest, err := lease.Digest()
	if err != nil {
		panic(err)
	}
	lease.LeaseDigest = digest
	return lease
}

// TestJournalDerivesFromDispatchLeaseLedger demonstrates the journal
// derivation path over the real M9-a durable lease ledger, consuming the
// internal/dispatch exported API read-only: the journal derives dispatch
// commands from the authoritative lease content digests recorded in the
// ledger, never writing the ledger itself.
func TestJournalDerivesFromDispatchLeaseLedger(t *testing.T) {
	ledger, err := dispatch.NewLeaseLedger(t.TempDir())
	if err != nil {
		t.Fatalf("NewLeaseLedger rejected a fresh directory: %v", err)
	}
	first := interopLease("1")
	second := interopLease("2")
	if err := ledger.AppendClaim(first); err != nil {
		t.Fatalf("AppendClaim rejected the first lease: %v", err)
	}
	if err := ledger.AppendClaim(second); err != nil {
		t.Fatalf("AppendClaim rejected the second lease: %v", err)
	}

	journal, err := NewCommandJournal(testNamespace())
	if err != nil {
		t.Fatalf("NewCommandJournal rejected a valid namespace: %v", err)
	}

	currentFirst, state, generation, err := ledger.Current(first.LeaseId)
	if err != nil {
		t.Fatalf("Current rejected the claimed lease: %v", err)
	}
	if state != dispatch.LeaseStateActive || generation != 1 {
		t.Fatalf("the lease ledger must report an active generation-1 lease, got %q generation %d", string(state), generation)
	}
	firstFact := LedgerFact{Sequence: 1, FactDigest: currentFirst.LeaseDigest, PayloadDigest: fixedDigest("engine-interop-payload-" + "1")}
	firstCommand, err := journal.Derive(firstFact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("Derive rejected the ledger-derived fact: %v", err)
	}

	currentSecond, _, _, err := ledger.Current(second.LeaseId)
	if err != nil {
		t.Fatalf("Current rejected the second claimed lease: %v", err)
	}
	secondFact := LedgerFact{Sequence: 2, FactDigest: currentSecond.LeaseDigest, PayloadDigest: fixedDigest("engine-interop-payload-" + "2")}
	secondCommand, err := journal.Derive(secondFact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("Derive rejected the second ledger-derived fact: %v", err)
	}
	if firstCommand.CommandId == secondCommand.CommandId {
		t.Fatal("distinct ledger facts must derive distinct commandIds")
	}

	replayed, err := journal.Derive(firstFact, CommandKindDispatch)
	if err != nil {
		t.Fatalf("replay Derive must merge the identical lease fact, got %v", err)
	}
	if replayed != firstCommand {
		t.Fatal("replay derivation must return the identical command")
	}
	if journal.DuplicateDerivations(firstCommand.CommandId) != 1 {
		t.Fatalf("the replayed lease fact must be counted exactly once, got %d", journal.DuplicateDerivations(firstCommand.CommandId))
	}
	if commands := journal.Commands(); len(commands) != 2 {
		t.Fatalf("the journal must hold exactly the two lease-derived commands, got %d", len(commands))
	}
	if !strings.HasPrefix(firstCommand.CommandId, "sha256:") {
		t.Fatalf("the derived commandId must carry the sha256 prefix, got %s", firstCommand.CommandId)
	}
	if bytes.Equal([]byte(firstCommand.CommandId), []byte(firstFact.FactDigest)) {
		t.Fatal("the commandId must be a derivation of the fact digest, never the fact digest itself")
	}
}
