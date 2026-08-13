package publication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/port"
)

// fakeMergeObserver implements port.MergeReceiptObserver for path A and
// reconcile tests. It derives receipts from the observed PublicationRecord
// unless a fixed error is configured.
type fakeMergeObserver struct {
	mu        sync.Mutex
	failWith  error
	build     func(publication domain.PublicationRecord, publicationDigest string) domain.Record
	calls     int
	lastRunID string
}

var _ port.MergeReceiptObserver = (*fakeMergeObserver)(nil)

func (o *fakeMergeObserver) ObserveMergeReceipt(_ context.Context, record domain.Record) (domain.Record, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls++
	if o.failWith != nil {
		return domain.Record{}, o.failWith
	}
	var publication domain.PublicationRecord
	if err := json.Unmarshal(record.Data, &publication); err != nil {
		return domain.Record{}, err
	}
	o.lastRunID = publication.RunID
	digest, err := canonical.DigestJSON(record.Data)
	if err != nil {
		return domain.Record{}, err
	}
	return o.build(publication, digest), nil
}

func receiptFromPublication(publication domain.PublicationRecord, publicationDigest string) domain.Record {
	receipt := buildReceipt(publication, publicationDigest, strings.Repeat("7", 40))
	data, _ := json.Marshal(receipt)
	return domain.Record{Kind: domain.KindSCMMergeReceipt, Data: data}
}

// buildReceipt assembles a schema-valid receipt bound to the publication with
// a correctly detached digest.
func buildReceipt(publication domain.PublicationRecord, publicationDigest, mergeCommitSHA string) domain.SCMMergeReceipt {
	receipt := domain.SCMMergeReceipt{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindSCMMergeReceipt,
		ReceiptID:            "receipt-" + strings.Repeat("7", 64),
		AuthorityNamespaceID: "sha256:" + strings.Repeat("a", 64),
		RunID:                publication.RunID,
		PublicationRecordID:  publicationDigest,
		RepositoryRef:        publication.Repository.NameWithOwner,
		PRNumber:             publication.Request.Number,
		HeadOid:              publication.HeadSHA,
		BaseOid:              publication.BaseSHA,
		MergeCommitSha:       mergeCommitSHA,
		MergedAt:             time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC),
		MergedBy:             "maintainer",
		MergeMethod:          domain.MergeMethodMerge,
		CapturedAt:           time.Date(2026, 8, 12, 10, 5, 0, 0, time.UTC),
	}
	digest, err := receipt.Digest()
	if err == nil {
		receipt.ReceiptDigest = digest
	}
	return receipt
}

func (f *publicationFixture) observeWithMerge(t *testing.T, observer *fakeObserver, mergeObserver port.MergeReceiptObserver) (CheckResult, error) {
	t.Helper()
	return ObserveChecks(context.Background(), CheckInput{
		StateRoot: f.stateRoot, RunID: f.runID, Observer: observer, MergeObserver: mergeObserver, Validator: f.validator,
	})
}

func readPersistedReceipt(t *testing.T, fixture *publicationFixture) domain.SCMMergeReceipt {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json"))
	if err != nil {
		t.Fatalf("scm-merge-receipt.json missing: %v", err)
	}
	if err := fixture.validator.Validate(domain.KindSCMMergeReceipt, data); err != nil {
		t.Fatalf("persisted receipt failed schema validation: %v", err)
	}
	var receipt domain.SCMMergeReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestObserveChecksAcceptsMergedHeadWithGreenChecks(t *testing.T) {
	fixture := fabricatedCIPendingFixture(t, fixtureOptions{maxReworkRounds: 1})
	mergeObserver := &fakeMergeObserver{build: receiptFromPublication}
	result, err := fixture.observeWithMerge(t, &fakeObserver{status: "pass"}, mergeObserver)
	if err != nil {
		t.Fatalf("observe merged head failed: %v", err)
	}
	if result.State.State != domain.StateAccepted || result.Checks.Status != "pass" {
		t.Fatalf("result = %+v", result)
	}
	if mergeObserver.calls != 1 {
		t.Fatalf("merge observer calls = %d", mergeObserver.calls)
	}
	receipt := readPersistedReceipt(t, fixture)
	if receipt.RunID != fixture.runID || receipt.HeadOid == "" || receipt.MergeCommitSha == "" || receipt.ReceiptDigest == "" {
		t.Fatalf("persisted receipt = %+v", receipt)
	}
	recomputed, err := receipt.Digest()
	if err != nil || receipt.ReceiptDigest != recomputed {
		t.Fatalf("persisted receipt digest mismatch: %v", err)
	}
	// Path A never writes a PublicationReconcileRecord.
	if _, statErr := os.Lstat(filepath.Join(fixture.runDirectory, "publication-reconcile-records")); !os.IsNotExist(statErr) {
		t.Fatalf("path A must not write reconcile records: %v", statErr)
	}
	state := fixture.inspect(t)
	if state.State != domain.StateAccepted || state.TerminalReason == "" {
		t.Fatalf("state = %+v", state)
	}
	outcomeData, err := os.ReadFile(filepath.Join(fixture.runDirectory, "outcome.json"))
	if err != nil {
		t.Fatalf("accepted outcome missing: %v", err)
	}
	if err := fixture.validator.Validate(domain.KindOutcome, outcomeData); err != nil {
		t.Fatalf("accepted outcome invalid: %v", err)
	}
}

func TestObserveChecksMergedHeadWithFailedChecksKeepsCurrentSemantics(t *testing.T) {
	fixture := fabricatedCIPendingFixture(t, fixtureOptions{maxReworkRounds: 1})
	mergeObserver := &fakeMergeObserver{build: receiptFromPublication}
	result, err := fixture.observeWithMerge(t, &fakeObserver{status: "fail"}, mergeObserver)
	if err != nil {
		t.Fatalf("observe merged head with failing checks failed: %v", err)
	}
	// Current fail semantics: rework while budget remains. The receipt is an
	// immutable fact and stays persisted.
	if result.State.State != domain.StateReworkRequested || result.Checks.Status != "fail" {
		t.Fatalf("result = %+v", result)
	}
	readPersistedReceipt(t, fixture)
}

func TestObserveChecksUnmergedPRKeepsOriginalFlow(t *testing.T) {
	fixture := fabricatedCIPendingFixture(t, fixtureOptions{maxReworkRounds: 1})
	before := fixture.inspect(t)
	mergeObserver := &fakeMergeObserver{failWith: port.ErrPRNotMerged}
	observer := &fakeObserver{status: "pending"}
	result, err := fixture.observeWithMerge(t, observer, mergeObserver)
	if err != nil {
		t.Fatalf("observe failed: %v", err)
	}
	if result.State.State != domain.StateCIPending || result.Checks.Status != "pending" {
		t.Fatalf("result = %+v", result)
	}
	after := fixture.inspect(t)
	if after.State != domain.StateCIPending || after.Sequence != before.Sequence {
		t.Fatalf("unmerged observation advanced the run: %+v", after)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
		t.Fatalf("unmerged PR must not persist a receipt: %v", statErr)
	}
}

func TestObserveChecksConflictingReceiptBlocksRun(t *testing.T) {
	fixture := fabricatedCIPendingFixture(t, fixtureOptions{maxReworkRounds: 1})
	// Pre-seed a different immutable receipt: a second, different merge fact
	// for the same run must conflict and keep the run BLOCKED.
	conflicting := `{"apiVersion":"marshal.dev/v1alpha1","kind":"SCMMergeReceipt","receiptId":"receipt-` + strings.Repeat("8", 64) + `","authorityNamespaceId":"sha256:` + strings.Repeat("9", 64) + `","runId":"` + fixture.runID + `","publicationRecordId":"sha256:` + strings.Repeat("5", 64) + `","repositoryRef":"marshal-test/task-repo","prNumber":7,"headOid":"` + strings.Repeat("1", 40) + `","baseOid":"` + strings.Repeat("2", 40) + `","mergeCommitSha":"` + strings.Repeat("3", 40) + `","mergedAt":"2026-08-12T09:00:00Z","mergedBy":"other","mergeMethod":"squash","capturedAt":"2026-08-12T09:05:00Z","receiptDigest":"sha256:` + strings.Repeat("6", 64) + `"}`
	if err := os.WriteFile(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json"), []byte(conflicting), 0o600); err != nil {
		t.Fatal(err)
	}
	mergeObserver := &fakeMergeObserver{build: receiptFromPublication}
	result, err := fixture.observeWithMerge(t, &fakeObserver{status: "pass"}, mergeObserver)
	if err == nil || result.State.State != domain.StateBlocked {
		t.Fatalf("result=%+v err=%v, want receipt conflict to block", result, err)
	}
	if !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("error = %v, want receipt conflict diagnosis", err)
	}
}

func TestObserveChecksReceiptBindingMismatchBlocksRun(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.SCMMergeReceipt)
	}{
		{"head oid mismatch", func(r *domain.SCMMergeReceipt) { r.HeadOid = strings.Repeat("3", 40) }},
		{"base oid mismatch", func(r *domain.SCMMergeReceipt) { r.BaseOid = strings.Repeat("4", 40) }},
		{"wrong run", func(r *domain.SCMMergeReceipt) { r.RunID = "run:foreign" }},
		{"wrong publication record id", func(r *domain.SCMMergeReceipt) { r.PublicationRecordID = "sha256:" + strings.Repeat("9", 64) }},
		{"wrong repository", func(r *domain.SCMMergeReceipt) { r.RepositoryRef = "attacker/fork" }},
		{"wrong pr number", func(r *domain.SCMMergeReceipt) { r.PRNumber = 99 }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			fixture := fabricatedCIPendingFixture(t, fixtureOptions{maxReworkRounds: 1})
			mergeObserver := &fakeMergeObserver{build: func(publication domain.PublicationRecord, publicationDigest string) domain.Record {
				receipt := buildReceipt(publication, publicationDigest, strings.Repeat("7", 40))
				test.mutate(&receipt)
				// Recompute the detached digest so the failure is the binding
				// check itself, not digest tampering.
				digest, err := receipt.Digest()
				if err != nil {
					t.Fatal(err)
				}
				receipt.ReceiptDigest = digest
				data, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				return domain.Record{Kind: domain.KindSCMMergeReceipt, Data: data}
			}}
			result, err := fixture.observeWithMerge(t, &fakeObserver{status: "pass"}, mergeObserver)
			if err == nil || result.State.State != domain.StateBlocked {
				t.Fatalf("result=%+v err=%v, want binding rejection to block", result, err)
			}
			if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
				t.Fatal("rejected receipt must not be persisted")
			}
		})
	}
}

func TestObserveChecksTamperedReceiptDigestBlocksRun(t *testing.T) {
	fixture := fabricatedCIPendingFixture(t, fixtureOptions{maxReworkRounds: 1})
	mergeObserver := &fakeMergeObserver{build: func(publication domain.PublicationRecord, publicationDigest string) domain.Record {
		receipt := buildReceipt(publication, publicationDigest, strings.Repeat("7", 40))
		receipt.ReceiptDigest = "sha256:" + strings.Repeat("f", 64)
		data, _ := json.Marshal(receipt)
		return domain.Record{Kind: domain.KindSCMMergeReceipt, Data: data}
	}}
	result, err := fixture.observeWithMerge(t, &fakeObserver{status: "pass"}, mergeObserver)
	if err == nil || result.State.State != domain.StateBlocked || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("result=%+v err=%v, want digest recomputation mismatch", result, err)
	}
}

func TestObserveChecksInvalidReceiptRecordBlocksRun(t *testing.T) {
	fixture := fabricatedCIPendingFixture(t, fixtureOptions{maxReworkRounds: 1})
	mergeObserver := &fakeMergeObserver{build: func(publication domain.PublicationRecord, publicationDigest string) domain.Record {
		receipt := buildReceipt(publication, publicationDigest, strings.Repeat("7", 40))
		data, _ := json.Marshal(receipt)
		// Drop a required field to fail the schema.
		var document map[string]any
		_ = json.Unmarshal(data, &document)
		delete(document, "mergeCommitSha")
		mutated, _ := json.Marshal(document)
		return domain.Record{Kind: domain.KindSCMMergeReceipt, Data: mutated}
	}}
	result, err := fixture.observeWithMerge(t, &fakeObserver{status: "pass"}, mergeObserver)
	if err == nil || result.State.State != domain.StateBlocked || !strings.Contains(err.Error(), "invalid SCMMergeReceipt") {
		t.Fatalf("result=%+v err=%v, want schema-invalid receipt rejection", result, err)
	}
}

func TestObserveChecksMergeObserverTransientFailureKeepsCIPending(t *testing.T) {
	fixture := fabricatedCIPendingFixture(t, fixtureOptions{maxReworkRounds: 1})
	before := fixture.inspect(t)
	mergeObserver := &fakeMergeObserver{failWith: errors.New("simulated merge observation timeout")}
	if _, err := fixture.observeWithMerge(t, &fakeObserver{status: "pass"}, mergeObserver); err == nil {
		t.Fatal("expected transient merge observation failure")
	}
	state := fixture.inspect(t)
	if state.State != domain.StateCIPending || state.Sequence != before.Sequence {
		t.Fatalf("transient merge failure mutated the run: %+v", state)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.runDirectory, "scm-merge-receipt.json")); !os.IsNotExist(statErr) {
		t.Fatal("failed merge observation must not persist a receipt")
	}
}

func TestObserveChecksMergeObserverPermanentFailureBlocksRun(t *testing.T) {
	fixture := fabricatedCIPendingFixture(t, fixtureOptions{maxReworkRounds: 1})
	mergeObserver := &fakeMergeObserver{failWith: port.Permanent(errors.New("remote PR head or identity changed"))}
	result, err := fixture.observeWithMerge(t, &fakeObserver{status: "pass"}, mergeObserver)
	if err == nil || result.State.State != domain.StateBlocked {
		t.Fatalf("result=%+v err=%v, want permanent merge failure to block", result, err)
	}
}

func TestObserveChecksRunDeadlinePrecedesMergeIdentification(t *testing.T) {
	fixture := fabricatedCIPendingFixture(t, fixtureOptions{maxReworkRounds: 1})
	deadline := fixtureRunDeadline(t, fixture)
	mergeObserver := &fakeMergeObserver{build: receiptFromPublication}
	observer := &fakeObserver{status: "pass"}
	result, err := fixture.observeAtMerge(t, observer, mergeObserver, deadline.Add(time.Minute))
	if err == nil || err.Error() != "ci-deadline-exceeded" {
		t.Fatalf("deadline must precede merge identification: err = %v", err)
	}
	if result.State.State != domain.StateBlocked {
		t.Fatalf("result = %+v", result)
	}
	if mergeObserver.calls != 0 {
		t.Fatalf("merge observer must not run after the local deadline: %d", mergeObserver.calls)
	}
}

func (f *publicationFixture) observeAtMerge(t *testing.T, observer *fakeObserver, mergeObserver port.MergeReceiptObserver, now time.Time) (CheckResult, error) {
	t.Helper()
	return ObserveChecks(context.Background(), CheckInput{
		StateRoot: f.stateRoot, RunID: f.runID, Observer: observer, MergeObserver: mergeObserver, Validator: f.validator, Now: now,
	})
}
