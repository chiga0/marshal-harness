package allocationcontrol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func testDigest(label string) string {
	return canonical.DigestBytes([]byte("allocationcontrol-test\x00" + label))
}

func testProvisionIntent(t *testing.T) AllocationProvisionIntentV1 {
	t.Helper()
	staging, live, _, marker, err := DeriveRelativeNames("allocation-1")
	if err != nil {
		t.Fatal(err)
	}
	intent := AllocationProvisionIntentV1{
		SchemaVersion: ProvisionSchema, ProtocolRevision: ProtocolRevision,
		Binding: AllocationBindingV1{
			AuthorityNamespaceID: "local/default", TaskID: "task-1", RunID: "run-1", AttemptID: "attempt-1",
			AllocationID: "allocation-1", LeaseID: "lease-1", Generation: 1,
			FencingTokenDigest: testDigest("fence"), CommandID: "command-provision-1", IdempotencyKey: "idempotency-provision-1",
		},
		Requirements:    SandboxRequirementsV1{AccessMode: "workspace-write", MinimumAssuranceLevel: "workspace-write"},
		AllowedStoreIDs: []string{}, WorkDirAllowlist: []string{"/tmp/worktree"}, EnvironmentAllowlist: []string{"PATH"},
		ExpectedOwnerUID: uint32(os.Geteuid()), ExpectedDirectoryMode: 0o700, ExpectedMarkerMode: 0o600,
		StagingRelativeName: staging, LiveRelativeName: live, MarkerRelativeName: marker,
		MarkerNonceDigest: testDigest("nonce"), ExpectedAttemptSequence: 7, AttemptAuthorityFactDigest: testDigest("attempt-head"),
	}
	if err := intent.Seal(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func testStoreScope(t *testing.T) AllocationStoreScopeV1 {
	t.Helper()
	scope, err := StoreScopeForBinding(testProvisionIntent(t).Binding)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func testCommittedFact(t *testing.T, kind RecordKind, recordID string) CommittedAuthorityFact {
	t.Helper()
	intent := testProvisionIntent(t)
	payload, err := EncodeFactPayload(intent)
	if err != nil {
		t.Fatal(err)
	}
	sequence := map[RecordKind]uint64{
		RecordProvisionIntent: 7, RecordProvisionPrepared: 8, RecordProvisionReceipt: 9,
		RecordTerminateIntent: 10, RecordTerminateReceipt: 11,
	}[kind]
	return CommittedAuthorityFact{
		RecordKind: kind, RecordID: recordID, RecordedAt: "2026-08-28T12:00:00Z", Binding: intent.Binding,
		ExpectedAttemptSequence: sequence, AttemptAuthorityFactDigest: testDigest("fact-" + recordID),
		RequestDigest: intent.RequestDigest, AuthorityFact: payload,
	}
}

func testRecord(t *testing.T, sequence uint64, previous string, fact CommittedAuthorityFact) JournalRecord {
	t.Helper()
	record, err := journalRecordForFact(sequence, previous, fact)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestJournalFrameExactGrammarAndRoundTrip(t *testing.T) {
	record := testRecord(t, 1, JournalGenesisDigest, testCommittedFact(t, RecordProvisionIntent, "fact-1"))
	frame, err := frameForRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := record.canonical()
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := fmt.Sprintf("%08x:", len(payload))
	if !bytes.HasPrefix(frame, []byte(wantPrefix)) || frame[len(frame)-1] != '\n' || !bytes.Equal(frame[9:len(frame)-1], payload) {
		t.Fatal("frame does not use the frozen 8hex:JCS\\n grammar")
	}
	records, offset, partial, err := parseJournalFrames(frame)
	if err != nil || partial || offset != len(frame) || len(records) != 1 || !equalCanonical(records[0], record) {
		t.Fatal("exact frame did not round-trip")
	}
}

func TestJournalOnlyFinalLegalPrefixIsPartialTail(t *testing.T) {
	record := testRecord(t, 1, JournalGenesisDigest, testCommittedFact(t, RecordProvisionIntent, "fact-1"))
	frame, err := frameForRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := record.canonical()
	partialSuffixes := [][]byte{
		[]byte("0"), []byte("0000000"), []byte(fmt.Sprintf("%08x", len(payload))),
		[]byte(fmt.Sprintf("%08x:", len(payload))), append([]byte(fmt.Sprintf("%08x:", len(payload))), payload[:len(payload)/2]...),
		append(append([]byte(fmt.Sprintf("%08x:", len(payload))), payload...), []byte{}...),
	}
	for index, suffix := range partialSuffixes {
		data := append(append([]byte(nil), frame...), suffix...)
		records, truncateAt, partial, parseErr := parseJournalFrames(data)
		if parseErr != nil || !partial || truncateAt != len(frame) || len(records) != 1 {
			t.Fatalf("legal partial suffix %d was not recoverable", index)
		}
	}

	invalidSuffixes := [][]byte{[]byte("x"), []byte("0000000g"), []byte("00000000:"), []byte("00000002:{}X")}
	for index, suffix := range invalidSuffixes {
		_, _, partial, parseErr := parseJournalFrames(append(append([]byte(nil), frame...), suffix...))
		if parseErr == nil || partial || !errors.Is(parseErr, ErrJournalCorrupt) {
			t.Fatalf("invalid suffix %d was silently treated as a partial tail", index)
		}
	}
}

func TestJournalTruncatesOnlyRecoverableTailAndFsyncs(t *testing.T) {
	record := testRecord(t, 1, JournalGenesisDigest, testCommittedFact(t, RecordProvisionIntent, "fact-1"))
	frame, _ := frameForRecord(record)
	path := t.TempDir() + "/journal"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(append([]byte(nil), frame...), []byte("0000")...)); err != nil {
		t.Fatal(err)
	}
	journal, err := newRecoveryJournal(file)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	stat, err := file.Stat()
	if err != nil || stat.Size() != int64(len(frame)) {
		t.Fatal("recoverable tail was not truncated to the last complete frame")
	}
}

func TestJournalNeverRepairsCompleteBadTail(t *testing.T) {
	record := testRecord(t, 1, JournalGenesisDigest, testCommittedFact(t, RecordProvisionIntent, "fact-1"))
	frame, _ := frameForRecord(record)
	badPayload := []byte(`{"complete":"but-not-a-record"}`)
	badFrame := []byte(fmt.Sprintf("%08x:%s\n", len(badPayload), badPayload))
	data := append(append([]byte(nil), frame...), badFrame...)
	path := t.TempDir() + "/journal"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, err := newRecoveryJournal(file); !errors.Is(err, ErrJournalCorrupt) {
		t.Fatal("complete invalid final frame was repaired")
	}
	stat, err := os.Stat(path)
	if err != nil || stat.Size() != int64(len(data)) {
		t.Fatal("corrupt complete tail was truncated")
	}
	_ = file.Close()
}

func TestJournalRejectsCanonicalAndChainCorruption(t *testing.T) {
	first := testRecord(t, 1, JournalGenesisDigest, testCommittedFact(t, RecordProvisionIntent, "fact-1"))
	second := testRecord(t, 2, first.RecordDigest, testCommittedFact(t, RecordProvisionPrepared, "fact-2"))
	firstFrame, _ := frameForRecord(first)
	secondFrame, _ := frameForRecord(second)

	broken := second
	broken.PreviousRecordDigest = testDigest("wrong-previous")
	broken.RecordDigest, _ = broken.digest()
	brokenFrame, _ := frameForRecord(broken)

	conflictingID := second
	conflictingID.RecordID = first.RecordID
	conflictingID.RecordDigest, _ = conflictingID.digest()
	conflictingFrame, _ := frameForRecord(conflictingID)

	invalidTerminator := append([]byte(nil), secondFrame...)
	invalidTerminator[len(invalidTerminator)-1] = 'X'
	cases := [][]byte{
		append(append([]byte(nil), firstFrame...), brokenFrame...),
		append(append([]byte(nil), firstFrame...), conflictingFrame...),
		append(append([]byte(nil), firstFrame...), invalidTerminator...),
	}
	for index, data := range cases {
		_, _, _, err := parseJournalFrames(data)
		if !errors.Is(err, ErrJournalCorrupt) {
			t.Fatalf("corruption case %d was accepted", index)
		}
	}
}

func TestJournalRejectsUnknownDuplicateAndNonCanonicalJSON(t *testing.T) {
	record := testRecord(t, 1, JournalGenesisDigest, testCommittedFact(t, RecordProvisionIntent, "fact-1"))
	canonicalRecord, _ := record.canonical()

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(canonicalRecord, &fields); err != nil {
		t.Fatal(err)
	}
	fields["unknownField"] = json.RawMessage(`true`)
	unknownRaw, _ := json.Marshal(fields)
	unknown, _ := canonical.JSON(unknownRaw)

	duplicate := append([]byte(`{"recordDigest":"`+record.RecordDigest+`",`), canonicalRecord[1:]...)
	nonCanonical := append([]byte(" "), canonicalRecord...)
	for index, payload := range [][]byte{unknown, duplicate, nonCanonical} {
		if _, err := DecodeJournalRecord(payload); !errors.Is(err, ErrJournalCorrupt) {
			t.Fatalf("closed JSON case %d was accepted", index)
		}
	}
}

func TestJournalProjectionIsAuthorityPrefixOnly(t *testing.T) {
	path := t.TempDir() + "/journal"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := newRecoveryJournal(file)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	facts := []CommittedAuthorityFact{
		testCommittedFact(t, RecordProvisionIntent, "fact-1"),
		testCommittedFact(t, RecordProvisionPrepared, "fact-2"),
	}
	if err := journal.SyncAuthorityProjection(facts); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := journal.SyncAuthorityProjection(facts); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("exact replay appended duplicate projection frames")
	}
	if err := journal.SyncAuthorityProjection(facts[:1]); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatal("journal ahead of authority was accepted")
	}
	divergent := append([]CommittedAuthorityFact(nil), facts...)
	divergent[0].AttemptAuthorityFactDigest = testDigest("divergent")
	if err := journal.SyncAuthorityProjection(divergent); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatal("divergent authority prefix was accepted")
	}
}

func TestJournalRejectsCommandAndIdempotencyDigestConflict(t *testing.T) {
	first := testCommittedFact(t, RecordProvisionIntent, "fact-1")
	second := testCommittedFact(t, RecordProvisionPrepared, "fact-2")
	second.RequestDigest = testDigest("different-request")
	if err := validateFactSequence([]CommittedAuthorityFact{first, second}); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatal("same command with a different request digest was accepted")
	}
	second.Binding.CommandID = "other-command"
	if err := validateFactSequence([]CommittedAuthorityFact{first, second}); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatal("same idempotency key with a different request digest was accepted")
	}
}

func TestJournalRejectsOutOfOrderOrNonMonotonicAuthorityFacts(t *testing.T) {
	intent := testCommittedFact(t, RecordProvisionIntent, "fact-1")
	prepared := testCommittedFact(t, RecordProvisionPrepared, "fact-2")
	if err := validateFactSequence([]CommittedAuthorityFact{prepared}); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatal("out-of-order authority projection was accepted")
	}
	prepared.ExpectedAttemptSequence = intent.ExpectedAttemptSequence
	if err := validateFactSequence([]CommittedAuthorityFact{intent, prepared}); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatal("non-monotonic authority sequence was accepted")
	}
}

func TestJournalRecordsDoNotExposeMutableAuthorityBytes(t *testing.T) {
	path := t.TempDir() + "/journal"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := newRecoveryJournal(file)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	facts := []CommittedAuthorityFact{testCommittedFact(t, RecordProvisionIntent, "fact-1")}
	if err := journal.SyncAuthorityProjection(facts); err != nil {
		t.Fatal(err)
	}
	copyRecords := journal.Records()
	copyRecords[0].AuthorityFact[0] ^= 0xff
	if !bytes.Equal(journal.Records()[0].AuthorityFact, facts[0].AuthorityFact) {
		t.Fatal("caller mutated journal-owned authority bytes")
	}
}
