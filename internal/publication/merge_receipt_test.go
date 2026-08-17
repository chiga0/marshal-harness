package publication

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// validMergeIntent builds a schema-valid, sealed SCMMergeIntent whose
// detached digest and canonical idempotency identity are correctly derived,
// so the white-box persistence and binding checks operate on a realistic
// authority record without a full run fixture.
func validMergeIntent() domain.SCMMergeIntent {
	intent := domain.SCMMergeIntent{
		APIVersion:               domain.APIVersionV1Alpha1,
		Kind:                     domain.KindSCMMergeIntent,
		AuthorityNamespaceID:     "sha256:" + strings.Repeat("1", 64),
		TaskID:                   "task:merge",
		RunID:                    "run:merge",
		PublicationRecordID:      "sha256:" + strings.Repeat("2", 64),
		PublicationDigest:        "sha256:" + strings.Repeat("2", 64),
		ReviewDecisionDigest:     "sha256:" + strings.Repeat("3", 64),
		VerificationDigest:       "sha256:" + strings.Repeat("4", 64),
		EvidenceDigest:           "sha256:" + strings.Repeat("5", 64),
		PolicyDigest:             "sha256:" + strings.Repeat("6", 64),
		PublishApprovalRecordID:  "approval:merge",
		PublishApprovalDigest:    "sha256:" + strings.Repeat("7", 64),
		RemoteCheckRecordDigest:  "sha256:" + strings.Repeat("8", 64),
		RepositoryRef:            "org/repo",
		PRNumber:                 7,
		HeadOid:                  strings.Repeat("a", 40),
		BaseOid:                  strings.Repeat("b", 40),
		MergeMethod:              domain.MergeMethodSquash,
		RequestedBy:              "maintainer",
		RequestedAt:              time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
		ExpectedMergedBy:         "github-login:alice",
		MergerSecurityDomainID:   "sha256:" + strings.Repeat("9", 64),
		MergerCredentialIdentity: "sha256:" + strings.Repeat("c", 64),
	}
	id, err := intent.Identity().IntentID()
	if err != nil {
		panic(err)
	}
	intent.IntentID = id
	digest, err := intent.Digest()
	if err != nil {
		panic(err)
	}
	intent.IntentDigest = digest
	return intent
}

var testPublicationDigest = "sha256:" + strings.Repeat("2", 64)

// sealedMergeReceipt builds a schema-valid receipt whose receiptDigest is
// correctly detached-computed and whose fields bind the validMergeIntent
// identity for the controlled-merge production path.
func sealedMergeReceipt() domain.SCMMergeReceipt {
	receipt := domain.SCMMergeReceipt{
		APIVersion:           domain.APIVersionV1Alpha1,
		Kind:                 domain.KindSCMMergeReceipt,
		ReceiptID:            "receipt:" + strings.Repeat("d", 64),
		AuthorityNamespaceID: "sha256:" + strings.Repeat("1", 64),
		RunID:                "run:merge",
		PublicationRecordID:  testPublicationDigest,
		RepositoryRef:        "org/repo",
		PRNumber:             7,
		HeadOid:              strings.Repeat("a", 40),
		BaseOid:              strings.Repeat("b", 40),
		MergeCommitSha:       strings.Repeat("e", 40),
		MergedAt:             time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC),
		MergedBy:             "alice",
		MergeMethod:          domain.MergeMethodSquash,
		CapturedAt:           time.Date(2026, 8, 17, 1, 5, 0, 0, time.UTC),
	}
	digest, err := receipt.Digest()
	if err != nil {
		panic(err)
	}
	receipt.ReceiptDigest = digest
	return receipt
}

func TestMergeReceiptBindingPositive(t *testing.T) {
	if err := validateMergeReceiptBinding(sealedMergeReceipt(), validMergeIntent(), testPublicationDigest); err != nil {
		t.Fatalf("fully bound receipt rejected: %v", err)
	}
}

// TestMergeReceiptBindingNegatives covers T8, T12, T18, T19, T20 and T21:
// every field mismatch, cross-authority/cross-run replay, the mergedBy
// canonical identity and the publicationRecordId/publicationDigest triple
// must fail closed with the fixed sentinel.
func TestMergeReceiptBindingNegatives(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.SCMMergeReceipt)
		reseal bool
		want   string
	}{
		{"cross authority namespace", func(r *domain.SCMMergeReceipt) { r.AuthorityNamespaceID = "sha256:" + strings.Repeat("9", 64) }, true, "authorityNamespaceId or runId"},
		{"cross run receipt", func(r *domain.SCMMergeReceipt) { r.RunID = "run:foreign" }, true, "authorityNamespaceId or runId"},
		{"empty authority namespace", func(r *domain.SCMMergeReceipt) { r.AuthorityNamespaceID = "" }, true, "authorityNamespaceId or runId"},
		{"head oid mismatch", func(r *domain.SCMMergeReceipt) { r.HeadOid = strings.Repeat("f", 40) }, true, "head, base or method"},
		{"base oid mismatch", func(r *domain.SCMMergeReceipt) { r.BaseOid = strings.Repeat("0", 40) }, true, "head, base or method"},
		{"merge method mismatch", func(r *domain.SCMMergeReceipt) { r.MergeMethod = domain.MergeMethodRebase }, true, "head, base or method"},
		{"publication record id mismatch", func(r *domain.SCMMergeReceipt) { r.PublicationRecordID = "sha256:" + strings.Repeat("8", 64) }, true, "triple publication digest"},
		{"repository mismatch", func(r *domain.SCMMergeReceipt) { r.RepositoryRef = "other/repo" }, true, "repository or PR number"},
		{"pr number mismatch", func(r *domain.SCMMergeReceipt) { r.PRNumber = 99 }, true, "repository or PR number"},
		{"merged by identity mismatch", func(r *domain.SCMMergeReceipt) { r.MergedBy = "bob" }, true, "mergedBy"},
		{"empty merged by", func(r *domain.SCMMergeReceipt) { r.MergedBy = "" }, true, "mergedBy"},
		{"tampered receipt digest", func(r *domain.SCMMergeReceipt) { r.ReceiptDigest = "sha256:" + strings.Repeat("f", 64) }, false, "digest recomputation"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			receipt := sealedMergeReceipt()
			test.mutate(&receipt)
			if test.reseal {
				digest, err := receipt.Digest()
				if err != nil {
					t.Fatal(err)
				}
				receipt.ReceiptDigest = digest
			}
			err := validateMergeReceiptBinding(receipt, validMergeIntent(), testPublicationDigest)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

// TestMergeReceiptBindingIntentDualIdentity covers T20: an intent carrying two
// divergent publication identities must fail the triple equality even when the
// receipt itself is internally consistent.
func TestMergeReceiptBindingIntentDualIdentity(t *testing.T) {
	intent := validMergeIntent()
	intent.PublicationDigest = "sha256:" + strings.Repeat("8", 64)
	err := validateMergeReceiptBinding(sealedMergeReceipt(), intent, testPublicationDigest)
	if err == nil || !strings.Contains(err.Error(), "triple publication digest") {
		t.Fatalf("error = %v, want dual-identity triple failure", err)
	}
}

// TestMergeReceiptBindingPublicationDigestMismatch covers the publication
// triple's third leg: the current-generation PublicationRecord digest differs
// from the intent-bound digest.
func TestMergeReceiptBindingPublicationDigestMismatch(t *testing.T) {
	err := validateMergeReceiptBinding(sealedMergeReceipt(), validMergeIntent(), "sha256:"+strings.Repeat("8", 64))
	if err == nil || !strings.Contains(err.Error(), "triple publication digest") {
		t.Fatalf("error = %v, want publication digest mismatch", err)
	}
}

// TestPersistMergeIntentPutIfAbsentIdempotentAndConflict covers T11 and T18:
// the digest-verified put-if-absent transaction merges an identical replay and
// fails closed on same-identity/different-content conflict.
func TestPersistMergeIntentPutIfAbsentIdempotentAndConflict(t *testing.T) {
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	intent := validMergeIntent()

	if _, created, err := persistMergeIntent(runDir, validator, intent); err != nil || !created {
		t.Fatalf("first persist: created=%v err=%v", created, err)
	}
	if _, created, err := persistMergeIntent(runDir, validator, intent); err != nil || created {
		t.Fatalf("idempotent replay: created=%v err=%v", created, err)
	}

	conflicting := intent
	conflicting.RequestedAt = intent.RequestedAt.Add(time.Hour)
	digest, err := conflicting.Digest()
	if err != nil {
		t.Fatal(err)
	}
	conflicting.IntentDigest = digest
	if _, _, err := persistMergeIntent(runDir, validator, conflicting); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("same-identity different-content intent error = %v, want conflict", err)
	}
}

// TestPersistMergeIntentRejectsMismatchedIdentity covers T18: an intent whose
// intentId does not match its canonical idempotency tuple must fail closed
// before any write.
func TestPersistMergeIntentRejectsMismatchedIdentity(t *testing.T) {
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	intent := validMergeIntent()
	intent.IntentID = "intent:" + strings.Repeat("9", 64)
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	intent.IntentDigest = digest
	if _, _, err := persistMergeIntent(t.TempDir(), validator, intent); err == nil || !strings.Contains(err.Error(), "idempotency tuple") {
		t.Fatalf("mismatched intentId error = %v", err)
	}
}

// TestPersistMergedReceiptIdempotentAndConflict covers T13: the immutable
// receipt merges on identical bytes and conflicts (fail closed) on any
// divergence.
func TestPersistMergedReceiptIdempotentAndConflict(t *testing.T) {
	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	intent := validMergeIntent()
	receipt := sealedMergeReceipt()
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	record := domain.Record{Kind: domain.KindSCMMergeReceipt, Data: data}

	if _, err := persistMergedReceipt(runDir, validator, record, intent, testPublicationDigest); err != nil {
		t.Fatalf("first receipt persist failed: %v", err)
	}
	if _, err := persistMergedReceipt(runDir, validator, record, intent, testPublicationDigest); err != nil {
		t.Fatalf("idempotent receipt replay failed: %v", err)
	}

	conflicting := sealedMergeReceipt()
	conflicting.MergeCommitSha = strings.Repeat("f", 40)
	digest, err := conflicting.Digest()
	if err != nil {
		t.Fatal(err)
	}
	conflicting.ReceiptDigest = digest
	conflictingData, err := json.Marshal(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	conflictingRecord := domain.Record{Kind: domain.KindSCMMergeReceipt, Data: conflictingData}
	if _, err := persistMergedReceipt(runDir, validator, conflictingRecord, intent, testPublicationDigest); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting receipt error = %v, want conflict", err)
	}
}
