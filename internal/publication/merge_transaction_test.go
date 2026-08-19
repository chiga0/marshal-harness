package publication

import (
	"encoding/json"
	"testing"
)

func TestMergeAuthorityTransactionPreparedAndRevokedSuccessor(t *testing.T) {
	prepared := validMergeAuthorityTransactionFixture(t, "prepared")
	if err := ValidateMergeAuthorityEvent(mergeAuthorityPreparedEvent, mergeCoreActorType, mergeCoreActorID, ciPendingState, ciPendingState, prepared, 9, 8); err != nil {
		t.Fatalf("prepared transaction rejected: %v", err)
	}
	revoked := prepared
	revoked.Status = "revoked"
	revoked.JournalSequence = 10
	revoked.ExpectedPreviousJournalSeq = 9
	revoked.RevocationGeneration = 1
	revoked.PreviousTransactionDigest = prepared.TransactionDigest
	revoked.RevokedAt = "2026-08-19T00:01:00Z"
	var err error
	revoked.TransactionDigest, err = revoked.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMergeAuthorityEvent(mergeAuthorityRevokedEvent, mergeCoreActorType, mergeCoreActorID, ciPendingState, ciPendingState, revoked, 10, 9); err != nil {
		t.Fatalf("revoked successor rejected: %v", err)
	}
	if err := ValidateMergeAuthoritySuccessor(prepared, revoked); err != nil {
		t.Fatalf("successor chain rejected: %v", err)
	}
}

func TestMergeAuthorityTransactionRejectsPublisherAndUnknownField(t *testing.T) {
	transaction := validMergeAuthorityTransactionFixture(t, "prepared")
	if err := ValidateMergeAuthorityEvent(mergeAuthorityPreparedEvent, "publisher", "marshal-scm-merger", ciPendingState, ciPendingState, transaction, 9, 8); err == nil {
		t.Fatal("accepted publisher-authored authority transaction")
	}
	data, err := json.Marshal(transaction)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-1], []byte(`,"unexpected":true}`)...)
	if _, err := DecodeMergeAuthorityTransaction(data); err == nil {
		t.Fatal("accepted authority transaction with unknown field")
	}
}

func TestMergeAuthorityTransactionReplayIdentityAndEventStatusAreBound(t *testing.T) {
	prepared := validMergeAuthorityTransactionFixture(t, "prepared")
	preparedIdentity, err := prepared.ReplayIdentity(mergeAuthorityPreparedEvent)
	if err != nil {
		t.Fatal(err)
	}
	revoked := prepared
	revoked.Status = "revoked"
	revoked.RevocationGeneration = 1
	revoked.JournalSequence = 10
	revoked.ExpectedPreviousJournalSeq = 9
	revoked.PreviousTransactionDigest = prepared.TransactionDigest
	revoked.RevokedAt = "2026-08-19T00:01:00Z"
	revoked.TransactionDigest, err = revoked.Digest()
	if err != nil {
		t.Fatal(err)
	}
	revokedIdentity, err := revoked.ReplayIdentity(mergeAuthorityRevokedEvent)
	if err != nil {
		t.Fatal(err)
	}
	if preparedIdentity == revokedIdentity {
		t.Fatal("prepared and revoked transactions share replay identity")
	}
	if err := ValidateMergeAuthorityEvent(mergeAuthorityRevokedEvent, mergeCoreActorType, mergeCoreActorID, ciPendingState, ciPendingState, prepared, 9, 8); err == nil {
		t.Fatal("accepted prepared transaction under revoked event type")
	}
	if err := ValidateMergeAuthorityEvent(mergeAuthorityPreparedEvent, mergeCoreActorType, mergeCoreActorID, ciPendingState, ciPendingState, revoked, 10, 9); err == nil {
		t.Fatal("accepted revoked transaction under prepared event type")
	}
}

func TestMergeAuthorityTransactionSuccessorRejectsAdmissionDrift(t *testing.T) {
	prepared := validMergeAuthorityTransactionFixture(t, "prepared")
	revoked := prepared
	revoked.Status = "revoked"
	revoked.JournalSequence = 10
	revoked.ExpectedPreviousJournalSeq = 9
	revoked.RevocationGeneration = 1
	revoked.PreviousTransactionDigest = prepared.TransactionDigest
	revoked.RevokedAt = "2026-08-19T00:01:00Z"
	revoked.EvidenceDigest = digestFixture("different-evidence")
	var err error
	revoked.TransactionDigest, err = revoked.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMergeAuthoritySuccessor(prepared, revoked); err == nil {
		t.Fatal("accepted successor with changed evidence binding")
	}
}

func validMergeAuthorityTransactionFixture(t *testing.T, status string) MergeAuthorityTransaction {
	t.Helper()
	transaction := MergeAuthorityTransaction{
		SchemaVersion: 1, RecordKind: "MergeAuthorityTransaction", Status: status,
		AuthorityNamespaceID: "ns-1", TaskID: "task-1", RunID: "run-1",
		JournalSequence: 9, ExpectedPreviousJournalSeq: 8,
		IntentDigest: digestFixture("intent"), AuthorizationDigest: digestFixture("auth"),
		PublicationDigest: digestFixture("publication"), ReviewDecisionDigest: digestFixture("review"),
		VerificationDigest: digestFixture("verification"), EvidenceDigest: digestFixture("evidence"),
		PolicyDigest: digestFixture("policy"), ApprovalDigest: digestFixture("approval"),
		RemoteCheckDigest: digestFixture("remote"), PreparedAt: "2026-08-19T00:00:00Z",
	}
	var err error
	transaction.TransactionDigest, err = transaction.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return transaction
}
