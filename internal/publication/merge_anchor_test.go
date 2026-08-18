package publication

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestMergeDeliveryAnchorMutationFenceValidatesDetachedDigestAndReplayIdentity(t *testing.T) {
	a := MergeDeliveryAnchor{
		SchemaVersion: 1, RecordKind: "MergeDeliveryAnchor", Status: "mutation-fence-consumed",
		AuthorityNamespaceID: "ns-1", TaskID: "task-1", RunID: "run-1",
		JournalSequence: 8, ExpectedPreviousJournalSeq: 7, LedgerSequence: 2,
		PreviousAnchorDigest: digestFixture("pending"), PendingAnchorDigest: digestFixture("pending"),
		Operation: "merge", DeliveryAttempt: 1,
		IntentDigest: digestFixture("intent"), AuthorizationDigest: digestFixture("auth"),
		PublicationDigest: digestFixture("publication"), ReviewDecisionDigest: digestFixture("review"),
		VerificationDigest: digestFixture("verification"), EvidenceDigest: digestFixture("evidence"),
		PolicyDigest: digestFixture("policy"), ApprovalDigest: digestFixture("approval"),
		RemoteCheckDigest: digestFixture("remote"), ProviderRequestDigest: digestFixture("request"),
		ExpiresAt: "2026-08-19T01:00:00Z", ConsumedAt: "2026-08-19T00:00:00Z",
	}
	var err error
	a.CanonicalReplayIdentity, err = a.ReplayIdentity()
	if err != nil {
		t.Fatal(err)
	}
	a.AnchorDigest, err = a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ValidateMutationFence(8, 7); err != nil {
		t.Fatalf("valid anchor rejected: %v", err)
	}
	if err := ValidateMutationFenceEvent(mergeDeliveryAnchorEvent, mergeCoreActorType, mergeCoreActorID, ciPendingState, ciPendingState, a, 8, 7); err != nil {
		t.Fatalf("valid Core event rejected: %v", err)
	}
}

func TestMergeDeliveryAnchorMutationFenceRejectsLineageAndDigestDrift(t *testing.T) {
	a := validMergeDeliveryAnchorFixture(t)
	a.ExpectedPreviousJournalSeq = 6
	if err := a.ValidateMutationFence(8, 7); err == nil {
		t.Fatal("accepted non-contiguous journal sequence")
	}
	a = validMergeDeliveryAnchorFixture(t)
	a.ProviderRequestDigest = digestFixture("different-request")
	if err := a.ValidateMutationFence(8, 7); err == nil {
		t.Fatal("accepted provider request digest drift")
	}
	a = validMergeDeliveryAnchorFixture(t)
	if err := ValidateMutationFenceEvent(mergeDeliveryAnchorEvent, "publisher", "marshal-scm-merger", ciPendingState, ciPendingState, a, 8, 7); err == nil {
		t.Fatal("accepted publisher-authored mutation fence event")
	}
	data, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-1], []byte(`,"unexpected":true}`)...)
	if _, err := DecodeMergeDeliveryAnchor(data); err == nil {
		t.Fatal("accepted mutation fence payload with unknown field")
	}
}

func validMergeDeliveryAnchorFixture(t *testing.T) MergeDeliveryAnchor {
	t.Helper()
	a := MergeDeliveryAnchor{
		SchemaVersion: 1, RecordKind: "MergeDeliveryAnchor", Status: "mutation-fence-consumed",
		AuthorityNamespaceID: "ns-1", TaskID: "task-1", RunID: "run-1",
		JournalSequence: 8, ExpectedPreviousJournalSeq: 7, LedgerSequence: 2,
		PreviousAnchorDigest: digestFixture("pending"), PendingAnchorDigest: digestFixture("pending"),
		Operation: "merge", DeliveryAttempt: 1,
		IntentDigest: digestFixture("intent"), AuthorizationDigest: digestFixture("auth"),
		PublicationDigest: digestFixture("publication"), ReviewDecisionDigest: digestFixture("review"),
		VerificationDigest: digestFixture("verification"), EvidenceDigest: digestFixture("evidence"),
		PolicyDigest: digestFixture("policy"), ApprovalDigest: digestFixture("approval"),
		RemoteCheckDigest: digestFixture("remote"), ProviderRequestDigest: digestFixture("request"),
		ExpiresAt: "2026-08-19T01:00:00Z", ConsumedAt: "2026-08-19T00:00:00Z",
	}
	var err error
	a.CanonicalReplayIdentity, err = a.ReplayIdentity()
	if err != nil {
		t.Fatal(err)
	}
	a.AnchorDigest, err = a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func digestFixture(label string) string { return "sha256:" + fmt.Sprintf("%064x", len(label)) }
