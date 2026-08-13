package domain

import (
	"strings"
	"testing"
	"time"
)

// testSHA assembles a deterministic object-id literal without embedding a
// full-length hex secret in one place.
func testSHA(fill string) string { return strings.Repeat(fill, 40) }

// testDigestValue assembles a canonical sha256 digest literal (helper
// construction keeps fixtures gitleaks-safe).
func testDigestValue(fill string) string { return "sha256:" + strings.Repeat(fill, 64) }

func validSCMMergeReceipt() SCMMergeReceipt {
	timestamp := time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)
	return SCMMergeReceipt{
		APIVersion:           APIVersionV1Alpha1,
		Kind:                 KindSCMMergeReceipt,
		ReceiptID:            "receipt-" + strings.Repeat("0", 64),
		AuthorityNamespaceID: "sha256:" + strings.Repeat("a", 64),
		RunID:                "run-01",
		PublicationRecordID:  "sha256:" + strings.Repeat("b", 64),
		RepositoryRef:        "example-org/example-repo",
		PRNumber:             7,
		HeadOid:              testSHA("2"),
		BaseOid:              testSHA("0"),
		MergeCommitSha:       testSHA("4"),
		MergedAt:             timestamp,
		MergedBy:             "maintainer",
		MergeMethod:          MergeMethodMerge,
		CapturedAt:           timestamp.Add(time.Minute),
		ReceiptDigest:        testDigestValue("c"),
	}
}

func validPublicationReconcileRecord() PublicationReconcileRecord {
	return PublicationReconcileRecord{
		APIVersion:            APIVersionV1Alpha1,
		Kind:                  KindPublicationReconcileRecord,
		ReconcileID:           "reconcile-" + strings.Repeat("1", 64),
		AuthorityNamespaceID:  "sha256:" + strings.Repeat("a", 64),
		RunID:                 "run-01",
		SCMMergeReceiptID:     "receipt-" + strings.Repeat("0", 64),
		ReconcileType:         ReconcileTypeAcceptAfterMerge,
		ObservedState:         StateBlocked,
		DecidedState:          StateAccepted,
		EvidenceDigests:       []string{testDigestValue("b"), testDigestValue("d"), testDigestValue("c"), testDigestValue("e")},
		ReconcileReason:       "merged-head.reconciled-after-block",
		ReconciledBy:          "maintainer",
		ReconciledAt:          time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC),
		ReconcileRecordDigest: testDigestValue("f"),
	}
}

func TestSCMMergeReceiptValidateAcceptsValidRecord(t *testing.T) {
	t.Parallel()
	if err := validSCMMergeReceipt().Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want valid receipt", err)
	}
}

func TestSCMMergeReceiptValidateRejectsInvalidFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*SCMMergeReceipt)
	}{
		{name: "wrong apiVersion", mutate: func(r *SCMMergeReceipt) { r.APIVersion = "marshal.dev/v2" }},
		{name: "wrong kind", mutate: func(r *SCMMergeReceipt) { r.Kind = KindPublicationRecord }},
		{name: "empty receiptId", mutate: func(r *SCMMergeReceipt) { r.ReceiptID = "" }},
		{name: "empty authorityNamespaceId", mutate: func(r *SCMMergeReceipt) { r.AuthorityNamespaceID = "" }},
		{name: "empty runId", mutate: func(r *SCMMergeReceipt) { r.RunID = "" }},
		{name: "empty publicationRecordId", mutate: func(r *SCMMergeReceipt) { r.PublicationRecordID = "" }},
		{name: "empty repositoryRef", mutate: func(r *SCMMergeReceipt) { r.RepositoryRef = "" }},
		{name: "oversized repositoryRef", mutate: func(r *SCMMergeReceipt) { r.RepositoryRef = strings.Repeat("r", 2049) }},
		{name: "prNumber below one", mutate: func(r *SCMMergeReceipt) { r.PRNumber = 0 }},
		{name: "missing mergeCommitSha", mutate: func(r *SCMMergeReceipt) { r.MergeCommitSha = "" }},
		{name: "short headOid", mutate: func(r *SCMMergeReceipt) { r.HeadOid = "abc" }},
		{name: "non-hex baseOid", mutate: func(r *SCMMergeReceipt) { r.BaseOid = strings.Repeat("z", 40) }},
		{name: "uppercase mergeCommitSha", mutate: func(r *SCMMergeReceipt) { r.MergeCommitSha = strings.Repeat("A", 40) }},
		{name: "zero mergedAt", mutate: func(r *SCMMergeReceipt) { r.MergedAt = time.Time{} }},
		{name: "empty mergedBy", mutate: func(r *SCMMergeReceipt) { r.MergedBy = "" }},
		{name: "oversized mergedBy", mutate: func(r *SCMMergeReceipt) { r.MergedBy = strings.Repeat("m", 257) }},
		{name: "mergeMethod outside enumeration", mutate: func(r *SCMMergeReceipt) { r.MergeMethod = "unknown" }},
		{name: "zero capturedAt", mutate: func(r *SCMMergeReceipt) { r.CapturedAt = time.Time{} }},
		{name: "receiptDigest without prefix", mutate: func(r *SCMMergeReceipt) { r.ReceiptDigest = strings.Repeat("c", 64) }},
		{name: "receiptDigest wrong length", mutate: func(r *SCMMergeReceipt) { r.ReceiptDigest = "sha256:" + strings.Repeat("c", 63) }},
		{name: "receiptDigest non-hex", mutate: func(r *SCMMergeReceipt) { r.ReceiptDigest = "sha256:" + strings.Repeat("w", 64) }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			receipt := validSCMMergeReceipt()
			test.mutate(&receipt)
			if err := receipt.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly accepted an invalid SCMMergeReceipt")
			}
		})
	}
}

func TestSCMMergeReceiptDigestIsDetached(t *testing.T) {
	t.Parallel()
	receipt := validSCMMergeReceipt()
	receipt.ReceiptDigest = ""
	digest, err := receipt.Digest()
	if err != nil || !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
		t.Fatalf("Digest() = %q, err = %v", digest, err)
	}
	receipt.ReceiptDigest = digest
	recomputed, err := receipt.Digest()
	if err != nil || recomputed != digest {
		t.Fatalf("detached digest is not stable: %q vs %q (err=%v)", recomputed, digest, err)
	}
	receipt.MergeCommitSha = testSHA("9")
	tampered, err := receipt.Digest()
	if err != nil || tampered == digest {
		t.Fatalf("tampered receipt must change the detached digest: %q vs %q", tampered, digest)
	}
}

func TestPublicationReconcileRecordValidateAcceptsValidRecord(t *testing.T) {
	t.Parallel()
	if err := validPublicationReconcileRecord().Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want valid record", err)
	}
}

func TestPublicationReconcileRecordValidateRejectsInvalidFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*PublicationReconcileRecord)
	}{
		{name: "wrong apiVersion", mutate: func(r *PublicationReconcileRecord) { r.APIVersion = "marshal.dev/v2" }},
		{name: "wrong kind", mutate: func(r *PublicationReconcileRecord) { r.Kind = KindSCMMergeReceipt }},
		{name: "empty reconcileId", mutate: func(r *PublicationReconcileRecord) { r.ReconcileID = "" }},
		{name: "empty authorityNamespaceId", mutate: func(r *PublicationReconcileRecord) { r.AuthorityNamespaceID = "" }},
		{name: "empty scmMergeReceiptId", mutate: func(r *PublicationReconcileRecord) { r.SCMMergeReceiptID = "" }},
		{name: "reconcileType outside enumeration", mutate: func(r *PublicationReconcileRecord) { r.ReconcileType = "reject-after-merge" }},
		{name: "observedState outside enumeration", mutate: func(r *PublicationReconcileRecord) { r.ObservedState = StateAccepted }},
		{name: "decidedState outside enumeration", mutate: func(r *PublicationReconcileRecord) { r.DecidedState = StateBlocked }},
		{name: "empty evidenceDigests", mutate: func(r *PublicationReconcileRecord) { r.EvidenceDigests = []string{} }},
		{name: "invalid evidence digest entry", mutate: func(r *PublicationReconcileRecord) { r.EvidenceDigests = []string{"sha256:" + strings.Repeat("w", 64)} }},
		{name: "duplicated evidence digest", mutate: func(r *PublicationReconcileRecord) {
			r.EvidenceDigests = []string{testDigestValue("b"), testDigestValue("b")}
		}},
		{name: "reconcileReason free text", mutate: func(r *PublicationReconcileRecord) { r.ReconcileReason = "not a reason code" }},
		{name: "reconcileReason leading separator", mutate: func(r *PublicationReconcileRecord) { r.ReconcileReason = ".merged-head" }},
		{name: "empty reconciledBy", mutate: func(r *PublicationReconcileRecord) { r.ReconciledBy = "" }},
		{name: "oversized reconciledBy", mutate: func(r *PublicationReconcileRecord) { r.ReconciledBy = strings.Repeat("m", 257) }},
		{name: "zero reconciledAt", mutate: func(r *PublicationReconcileRecord) { r.ReconciledAt = time.Time{} }},
		{name: "reconcileRecordDigest invalid", mutate: func(r *PublicationReconcileRecord) { r.ReconcileRecordDigest = "sha256:" + strings.Repeat("f", 63) }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			record := validPublicationReconcileRecord()
			test.mutate(&record)
			if err := record.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly accepted an invalid PublicationReconcileRecord")
			}
		})
	}
}

func TestPublicationReconcileRecordDigestIsDetached(t *testing.T) {
	t.Parallel()
	record := validPublicationReconcileRecord()
	record.ReconcileRecordDigest = ""
	digest, err := record.Digest()
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("Digest() = %q, err = %v", digest, err)
	}
	record.ReconcileRecordDigest = digest
	recomputed, err := record.Digest()
	if err != nil || recomputed != digest {
		t.Fatalf("detached digest is not stable: %q vs %q (err=%v)", recomputed, digest, err)
	}
	record.ReconciledBy = "someone-else"
	tampered, err := record.Digest()
	if err != nil || tampered == digest {
		t.Fatalf("tampered record must change the detached digest: %q vs %q", tampered, digest)
	}
}
