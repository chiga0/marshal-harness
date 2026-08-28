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

func TestDetachedRequestAndRecordDigestGoldens(t *testing.T) {
	staging, live, tombstone, markerName, err := DeriveRelativeNames("allocation-1")
	if err != nil {
		t.Fatal(err)
	}
	binding := AllocationBindingV1{
		AuthorityNamespaceID: "local/default", TaskID: "task-1", RunID: "run-1", AttemptID: "attempt-1",
		AllocationID: "allocation-1", LeaseID: "lease-1", Generation: 1,
		FencingTokenDigest: testDigest("fence"), CommandID: "command-provision-1", IdempotencyKey: "idempotency-provision-1",
	}
	provision := AllocationProvisionIntentV1{
		SchemaVersion: ProvisionSchema, ProtocolRevision: ProtocolRevision, Binding: binding,
		Requirements:    SandboxRequirementsV1{AccessMode: "workspace-write", MinimumAssuranceLevel: "workspace-write"},
		AllowedStoreIDs: []string{}, WorkDirAllowlist: []string{"/tmp/worktree"}, EnvironmentAllowlist: []string{"PATH"},
		ExpectedOwnerUID: 501, ExpectedDirectoryMode: 0o700, ExpectedMarkerMode: 0o600,
		StagingRelativeName: staging, LiveRelativeName: live, MarkerRelativeName: markerName,
		MarkerNonceDigest: testDigest("nonce"), ExpectedAttemptSequence: 7, AttemptAuthorityFactDigest: testDigest("attempt-head"),
	}
	if err := provision.Seal(); err != nil {
		t.Fatal(err)
	}
	const provisionGolden = "sha256:7dfb104918f2c35c029ac5739d7fa9d4c6faf81ec91bb22f95cccfbfbc6f5774"
	if provision.RequestDigest != provisionGolden {
		t.Fatal("AllocationProvisionIntentV1 digest is not the JCS object with requestDigest absent")
	}

	terminateBinding := binding
	terminateBinding.CommandID = "command-terminate-1"
	terminateBinding.IdempotencyKey = "idempotency-terminate-1"
	request := TerminateRequestV1{
		SchemaVersion: TerminateRequestSchema, ProtocolRevision: ProtocolRevision, Binding: terminateBinding,
		TerminalizationID: "terminalization-1", CleanupBindingDigest: testDigest("cleanup"),
		ProcessTerminalFactDigest: testDigest("process-terminal"), OrchestratorID: "orchestrator-1",
		ExpectedAttemptSequence: 10, AttemptAuthorityFactDigest: testDigest("terminate-head"),
		LiveRelativeName: live, TombstoneRelativeName: tombstone,
	}
	if err := request.Seal(); err != nil {
		t.Fatal(err)
	}
	const terminateGolden = "sha256:cbfabda87511dbb8071a5d06e1c748f1be1ae11bfcfa66be9a6a48c058370fd2"
	if request.RequestDigest != terminateGolden {
		t.Fatal("TerminateRequestV1 digest is not the JCS object with requestDigest absent")
	}

	payload, err := EncodeFactPayload(provision)
	if err != nil {
		t.Fatal(err)
	}
	fact := CommittedAuthorityFact{
		RecordKind: RecordProvisionIntent, RecordID: "fact-1", RecordedAt: "2026-08-28T12:00:00Z",
		Binding: binding, ExpectedAttemptSequence: 7, AttemptAuthorityFactDigest: testDigest("fact-fact-1"),
		RequestDigest: provision.RequestDigest, AuthorityFact: payload,
	}
	record, err := journalRecordForFact(1, JournalGenesisDigest, fact)
	if err != nil {
		t.Fatal(err)
	}
	const recordGolden = "sha256:76b8e2ef7dfc0fd5529affcdec60fea625f3451dc738bae6fe03426a0be93728"
	if record.RecordDigest != recordGolden {
		t.Fatal("JournalRecord digest is not the JCS object with recordDigest absent")
	}

	marker := provision.Marker()
	markerBytes, err := marker.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	stagingIdentity := ObjectIdentityV1{Device: "1", Inode: "2", Mode: 0o40700, UID: 501, GID: 20, Size: 64, Nlink: 2, Type: ObjectTypeDirectory}
	markerIdentity := ObjectIdentityV1{Device: "1", Inode: "3", Mode: 0o100600, UID: 501, GID: 20, Size: int64(len(markerBytes)), Nlink: 1, Type: ObjectTypeRegular}
	prepared := AllocationStagingPreparedV1{
		SchemaVersion: PreparedSchema, ProtocolRevision: ProtocolRevision, Binding: binding,
		IntentFactDigest: testDigest("provision-intent-fact"), RequestDigest: provision.RequestDigest,
		StagingRelativeName: staging, LiveRelativeName: live, MarkerRelativeName: markerName,
		StagingIdentity: stagingIdentity, MarkerIdentity: markerIdentity, Marker: marker,
		MarkerDigest: canonical.DigestBytes(markerBytes),
	}
	if err := prepared.Seal(); err != nil {
		t.Fatal(err)
	}
	const preparedGolden = "sha256:7f8e3a2fb749b5815dd219039b84761c76047427306af62461007282d6137ffd"
	if prepared.PreparedDigest != preparedGolden {
		t.Fatal("AllocationStagingPreparedV1 digest is not the JCS object with preparedDigest absent")
	}
	preparedWithEmptyField := prepared
	preparedWithEmptyField.PreparedDigest = ""
	preparedEmptyDigest, err := digestValue(preparedWithEmptyField)
	if err != nil {
		t.Fatal(err)
	}
	const preparedEmptyGolden = "sha256:0de6bd7376cac412823151514b1daad402af2acc79276a4e168cee2bb68e2825"
	if preparedEmptyDigest != preparedEmptyGolden || preparedEmptyDigest == prepared.PreparedDigest {
		t.Fatal("preparedDigest empty-field encoding was treated as field absence")
	}

	provisionReceipt := AllocationProvisionReceiptV1{
		SchemaVersion: ProvisionReceiptSchema, ProtocolRevision: ProtocolRevision, Binding: binding,
		IntentFactDigest: prepared.IntentFactDigest, PreparedFactDigest: testDigest("prepared-fact"),
		RequestDigest: provision.RequestDigest, LiveRelativeName: live, LiveIdentity: stagingIdentity,
		MarkerRelativeName: markerName, MarkerIdentity: markerIdentity, Marker: marker,
		MarkerDigest: canonical.DigestBytes(markerBytes), Disposition: DispositionApplied,
	}
	if err := provisionReceipt.Seal(); err != nil {
		t.Fatal(err)
	}
	const provisionReceiptGolden = "sha256:7f60e784ad7f0cbd928bf418d61519f7a0a1b1444d2d2fdc3b8f1a5c9613b2ea"
	if provisionReceipt.ReceiptDigest != provisionReceiptGolden {
		t.Fatal("AllocationProvisionReceiptV1 digest is not the JCS object with receiptDigest absent")
	}
	provisionReceiptWithEmptyField := provisionReceipt
	provisionReceiptWithEmptyField.ReceiptDigest = ""
	provisionReceiptEmptyDigest, err := digestValue(provisionReceiptWithEmptyField)
	if err != nil {
		t.Fatal(err)
	}
	const provisionReceiptEmptyGolden = "sha256:f30e2b5d4c5a72a17774960742101fcbe6ebf5d00c7ae1369bd2cc0d937e67fe"
	if provisionReceiptEmptyDigest != provisionReceiptEmptyGolden || provisionReceiptEmptyDigest == provisionReceipt.ReceiptDigest {
		t.Fatal("provision receipt empty-field encoding was treated as field absence")
	}

	terminateReceipt := AllocationTerminateReceiptV1{
		SchemaVersion: TerminateReceiptSchema, ProtocolRevision: ProtocolRevision, Binding: terminateBinding,
		TerminalizationID: request.TerminalizationID, CleanupBindingDigest: request.CleanupBindingDigest,
		ProcessTerminalFactDigest: request.ProcessTerminalFactDigest, OrchestratorID: request.OrchestratorID,
		ExpectedAttemptSequence: request.ExpectedAttemptSequence, AttemptAuthorityFactDigest: request.AttemptAuthorityFactDigest,
		IntentFactDigest: testDigest("terminate-intent-fact"), RequestDigest: request.RequestDigest,
		LiveRelativeName: live, TombstoneRelativeName: tombstone, TombstoneIdentity: stagingIdentity,
		MarkerRelativeName: markerName, MarkerIdentity: markerIdentity, Marker: marker,
		MarkerDigest: canonical.DigestBytes(markerBytes), LiveAbsent: true, TombstonePresent: true,
		Disposition: DispositionApplied,
	}
	if err := terminateReceipt.Seal(); err != nil {
		t.Fatal(err)
	}
	const terminateReceiptGolden = "sha256:e979ceee97a62696d38a2f21b9388d2c1fa9c9d073be98608730cd1219f2d0bb"
	if terminateReceipt.ReceiptDigest != terminateReceiptGolden {
		t.Fatal("AllocationTerminateReceiptV1 digest is not the JCS object with receiptDigest absent")
	}
	terminateReceiptWithEmptyField := terminateReceipt
	terminateReceiptWithEmptyField.ReceiptDigest = ""
	terminateReceiptEmptyDigest, err := digestValue(terminateReceiptWithEmptyField)
	if err != nil {
		t.Fatal(err)
	}
	const terminateReceiptEmptyGolden = "sha256:f0f4ac572d1323179d0fa3f555efc4b19f7819e7c3442aa4f6c0653a2409d65d"
	if terminateReceiptEmptyDigest != terminateReceiptEmptyGolden || terminateReceiptEmptyDigest == terminateReceipt.ReceiptDigest {
		t.Fatal("terminate receipt empty-field encoding was treated as field absence")
	}
}

func TestTerminateRequestDigestExcludesObservationButIntentBindsIt(t *testing.T) {
	provision := testProvisionIntent(t)
	marker := provision.Marker()
	markerBytes, err := marker.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	_, live, tombstone, _, err := DeriveRelativeNames(provision.Binding.AllocationID)
	if err != nil {
		t.Fatal(err)
	}
	binding := provision.Binding
	binding.CommandID = "command-terminate-1"
	binding.IdempotencyKey = "idempotency-terminate-1"
	request := TerminateRequestV1{
		SchemaVersion: TerminateRequestSchema, ProtocolRevision: ProtocolRevision, Binding: binding,
		TerminalizationID: "terminalization-1", CleanupBindingDigest: testDigest("cleanup"),
		ProcessTerminalFactDigest: testDigest("process-terminal"), OrchestratorID: "orchestrator-1",
		ExpectedAttemptSequence: 10, AttemptAuthorityFactDigest: testDigest("terminate-head"),
		LiveRelativeName: live, TombstoneRelativeName: tombstone,
	}
	if err := request.Seal(); err != nil {
		t.Fatal(err)
	}
	markerIdentity := ObjectIdentityV1{Device: "1", Inode: "3", Mode: 0o100600, UID: 501, GID: 20, Size: int64(len(markerBytes)), Nlink: 1, Type: ObjectTypeRegular}
	firstLive := ObjectIdentityV1{Device: "1", Inode: "2", Mode: 0o40700, UID: 501, GID: 20, Size: 64, Nlink: 2, Type: ObjectTypeDirectory}
	secondLive := firstLive
	secondLive.Inode = "4"
	first, err := bindTerminateIntent(request, firstLive, markerIdentity, marker, canonical.DigestBytes(markerBytes))
	if err != nil {
		t.Fatal(err)
	}
	second, err := bindTerminateIntent(request, secondLive, markerIdentity, marker, canonical.DigestBytes(markerBytes))
	if err != nil {
		t.Fatal(err)
	}
	if first.RequestDigest != request.RequestDigest || second.RequestDigest != request.RequestDigest {
		t.Fatal("held observation changed the sealed caller request digest")
	}
	if equalCanonical(first, second) {
		t.Fatal("different held observations produced the same terminate intent")
	}
	firstPayload, _ := EncodeFactPayload(first)
	fact := committedFactForTerminateTest(first, firstPayload)
	snapshot := AuthoritySnapshot{Facts: []CommittedAuthorityFact{fact}}
	if snapshot.hasExactFact(RecordTerminateIntent, fact.AttemptAuthorityFactDigest, request.RequestDigest, second) {
		t.Fatal("authority fact for one observation accepted a conflicting terminate intent")
	}
	changed := request
	changed.TerminalizationID = "terminalization-2"
	changed.RequestDigest = ""
	if err := changed.Seal(); err != nil || changed.RequestDigest == request.RequestDigest {
		t.Fatal("caller-controlled terminate tuple change did not change requestDigest")
	}
}

func committedFactForTerminateTest(intent AllocationTerminateIntentV1, payload json.RawMessage) CommittedAuthorityFact {
	return CommittedAuthorityFact{
		RecordKind: RecordTerminateIntent, RecordID: "terminate-intent", RecordedAt: "2026-08-28T12:00:00Z",
		Binding: intent.Binding, ExpectedAttemptSequence: intent.ExpectedAttemptSequence,
		AttemptAuthorityFactDigest: testDigest("terminate-intent-fact"), RequestDigest: intent.RequestDigest,
		TerminalizationID: intent.TerminalizationID, CleanupBindingDigest: intent.CleanupBindingDigest,
		ProcessTerminalFactDigest: intent.ProcessTerminalFactDigest, AuthorityFact: payload,
	}
}
