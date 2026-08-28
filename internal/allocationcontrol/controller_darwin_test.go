//go:build darwin

package allocationcontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

type fakeAllocationAuthority struct {
	session *fakeAllocationSession
}

func TestControllerRejectsPreexistingEmptyStagingWithoutMarker(t *testing.T) {
	root := canonicalTempDir(t)
	session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
	controller, store := openController(t, root, session)
	defer controller.Close()
	intent := *session.snapshot.ProvisionIntent
	if err := unix.Mkdirat(int(store.objects.Fd()), intent.StagingRelativeName, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.objects.Sync(); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.RecoverProvision(context.Background(), "effect-provision"); !errors.Is(err, ErrFilesystemConflict) {
		t.Fatal("pre-existing empty staging without the O_EXCL marker was adopted")
	}
	if len(store.JournalRecords()) != 1 || session.snapshot.ProvisionPrepared != nil || session.snapshot.ProvisionReceipt != nil {
		t.Fatal("pre-marker conflict produced prepared or receipt authority")
	}
	markerPath := filepath.Join(testObjectsPath(t, root, intent.Binding), intent.StagingRelativeName, intent.MarkerRelativeName)
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pre-marker conflict still created an identity marker")
	}
}

func TestControllerRejectsNonemptyPreMarkerStaging(t *testing.T) {
	root := canonicalTempDir(t)
	session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
	controller, store := openController(t, root, session)
	defer controller.Close()
	intent := *session.snapshot.ProvisionIntent
	if err := unix.Mkdirat(int(store.objects.Fd()), intent.StagingRelativeName, 0o700); err != nil {
		t.Fatal(err)
	}
	directoryFD, err := unix.Openat(int(store.objects.Fd()), intent.StagingRelativeName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	unknownFD, err := unix.Openat(directoryFD, "unknown", unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		unix.Close(directoryFD)
		t.Fatal(err)
	}
	_ = unix.Close(unknownFD)
	_ = unix.Close(directoryFD)
	if _, err := controller.RecoverProvision(context.Background(), "effect-provision"); !errors.Is(err, ErrFilesystemConflict) {
		t.Fatal("unknown pre-marker staging contents were not rejected")
	}
}

func (authority *fakeAllocationAuthority) WithCurrentAllocation(ctx context.Context, _ string, operation func(AuthoritySession) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	authority.session.mu.Lock()
	defer authority.session.mu.Unlock()
	return operation(authority.session)
}

type fakeAllocationSession struct {
	mu                     sync.Mutex
	snapshot               AuthoritySnapshot
	preparedError          error
	provisionReceiptError  error
	terminateReceiptError  error
	commitBeforeError      bool
	afterPreparedCommit    func()
	mutateBeforeSecondRead func(*AuthoritySnapshot)
	snapshotReads          int
}

func (session *fakeAllocationSession) Snapshot() (AuthoritySnapshot, error) {
	session.snapshotReads++
	if session.snapshotReads > 1 && session.mutateBeforeSecondRead != nil {
		session.mutateBeforeSecondRead(&session.snapshot)
		session.mutateBeforeSecondRead = nil
	}
	return cloneAuthoritySnapshot(session.snapshot), nil
}

func (session *fakeAllocationSession) AppendProvisionPrepared(_ context.Context, prepared AllocationStagingPreparedV1) (AuthoritySnapshot, error) {
	appendFact := func() {
		fact := committedFactForValue(RecordProvisionPrepared, "provision-prepared", prepared.Binding, prepared.RequestDigest, prepared)
		session.snapshot.ProvisionPrepared = &prepared
		session.snapshot.ProvisionPreparedFactDigest = fact.AttemptAuthorityFactDigest
		session.snapshot.Facts = append(session.snapshot.Facts, fact)
		if session.afterPreparedCommit != nil {
			session.afterPreparedCommit()
		}
	}
	if session.preparedError != nil {
		if session.commitBeforeError {
			appendFact()
		}
		return cloneAuthoritySnapshot(session.snapshot), session.preparedError
	}
	appendFact()
	return cloneAuthoritySnapshot(session.snapshot), nil
}

func (session *fakeAllocationSession) AppendProvisionReceipt(_ context.Context, receipt AllocationProvisionReceiptV1) (AuthoritySnapshot, error) {
	appendFact := func() {
		fact := committedFactForValue(RecordProvisionReceipt, "provision-receipt", receipt.Binding, receipt.RequestDigest, receipt)
		session.snapshot.ProvisionReceipt = &receipt
		session.snapshot.ProvisionReceiptFactDigest = fact.AttemptAuthorityFactDigest
		session.snapshot.Facts = append(session.snapshot.Facts, fact)
	}
	if session.provisionReceiptError != nil {
		if session.commitBeforeError {
			appendFact()
		}
		return cloneAuthoritySnapshot(session.snapshot), session.provisionReceiptError
	}
	appendFact()
	return cloneAuthoritySnapshot(session.snapshot), nil
}

func (session *fakeAllocationSession) AppendTerminateReceipt(_ context.Context, receipt AllocationTerminateReceiptV1) (AuthoritySnapshot, error) {
	appendFact := func() {
		fact := committedFactForValue(RecordTerminateReceipt, "terminate-receipt", receipt.Binding, receipt.RequestDigest, receipt)
		session.snapshot.TerminateReceipt = &receipt
		session.snapshot.TerminateReceiptFactDigest = fact.AttemptAuthorityFactDigest
		session.snapshot.Facts = append(session.snapshot.Facts, fact)
	}
	if session.terminateReceiptError != nil {
		if session.commitBeforeError {
			appendFact()
		}
		return cloneAuthoritySnapshot(session.snapshot), session.terminateReceiptError
	}
	appendFact()
	return cloneAuthoritySnapshot(session.snapshot), nil
}

func cloneAuthoritySnapshot(snapshot AuthoritySnapshot) AuthoritySnapshot {
	result := snapshot
	result.Facts = append([]CommittedAuthorityFact(nil), snapshot.Facts...)
	return result
}

func committedFactForValue(kind RecordKind, recordID string, binding AllocationBindingV1, requestDigest string, value any) CommittedAuthorityFact {
	payload, _ := EncodeFactPayload(value)
	sequence := map[RecordKind]uint64{
		RecordProvisionIntent: 7, RecordProvisionPrepared: 8, RecordProvisionReceipt: 9,
		RecordTerminateIntent: 10, RecordTerminateReceipt: 11,
	}[kind]
	fact := CommittedAuthorityFact{
		RecordKind: kind, RecordID: recordID, RecordedAt: "2026-08-28T12:00:00Z", Binding: binding,
		ExpectedAttemptSequence: sequence, AttemptAuthorityFactDigest: testDigest("authority-" + recordID),
		RequestDigest: requestDigest, AuthorityFact: payload,
	}
	switch typed := value.(type) {
	case AllocationTerminateIntentV1:
		fact.TerminalizationID = typed.TerminalizationID
		fact.CleanupBindingDigest = typed.CleanupBindingDigest
		fact.ProcessTerminalFactDigest = typed.ProcessTerminalFactDigest
	case AllocationTerminateReceiptV1:
		fact.TerminalizationID = typed.TerminalizationID
		fact.CleanupBindingDigest = typed.CleanupBindingDigest
		fact.ProcessTerminalFactDigest = typed.ProcessTerminalFactDigest
	}
	return fact
}

func initialAuthoritySnapshot(t *testing.T) AuthoritySnapshot {
	t.Helper()
	intent := testProvisionIntent(t)
	fact := committedFactForValue(RecordProvisionIntent, "provision-intent", intent.Binding, intent.RequestDigest, intent)
	return AuthoritySnapshot{Facts: []CommittedAuthorityFact{fact}, ProvisionIntent: &intent, ProvisionIntentFactDigest: fact.AttemptAuthorityFactDigest}
}

func openController(t *testing.T, root string, session *fakeAllocationSession) (*Controller, *Store) {
	t.Helper()
	scope, err := StoreScopeForBinding(session.snapshot.ProvisionIntent.Binding)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(root, scope)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewController(store, &fakeAllocationAuthority{session: session})
	if err != nil {
		t.Fatal(err)
	}
	return controller, store
}

func testObjectsPath(t *testing.T, root string, binding AllocationBindingV1) string {
	t.Helper()
	scope, err := StoreScopeForBinding(binding)
	if err != nil {
		t.Fatal(err)
	}
	scopeName, err := scope.directoryName()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, storeDirectoryName, scopeName, objectsDirectoryName)
}

func TestControllerProvisionTerminateAndReplay(t *testing.T) {
	root := canonicalTempDir(t)
	session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
	controller, store := openController(t, root, session)
	receipt, err := controller.RecoverProvision(context.Background(), "effect-provision")
	if err != nil {
		t.Fatal(err)
	}
	if len(store.JournalRecords()) != 3 {
		t.Fatal("provision did not project intent, prepared and receipt")
	}
	intent := *session.snapshot.ProvisionIntent
	objects := testObjectsPath(t, root, intent.Binding)
	if _, err := os.Lstat(filepath.Join(objects, intent.StagingRelativeName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staging name remains after successful no-replace promotion")
	}
	if _, err := os.Lstat(filepath.Join(objects, intent.LiveRelativeName)); err != nil {
		t.Fatal("live allocation is absent after provision")
	}
	if replay, err := controller.RecoverProvision(context.Background(), "effect-provision"); err != nil || replay.ReceiptDigest != receipt.ReceiptDigest || len(store.JournalRecords()) != 3 {
		t.Fatal("provision replay was not exact and append-free")
	}

	terminate := testTerminateIntent(t, receipt)
	terminateFact := committedFactForValue(RecordTerminateIntent, "terminate-intent", terminate.Binding, terminate.RequestDigest, terminate)
	session.snapshot.TerminateIntent = &terminate
	session.snapshot.TerminateIntentFactDigest = terminateFact.AttemptAuthorityFactDigest
	session.snapshot.Facts = append(session.snapshot.Facts, terminateFact)
	session.snapshotReads = 0
	terminated, err := controller.RecoverTerminate(context.Background(), "effect-terminate")
	if err != nil {
		t.Fatal(err)
	}
	if !terminated.LiveAbsent || !terminated.TombstonePresent || len(store.JournalRecords()) != 5 {
		t.Fatal("terminate did not persist the exact tombstone observation")
	}
	if _, err := os.Lstat(filepath.Join(objects, terminate.LiveRelativeName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("live allocation remains after terminate")
	}
	if _, err := os.Lstat(filepath.Join(objects, terminate.TombstoneRelativeName)); err != nil {
		t.Fatal("permanent tombstone is absent")
	}
	if replay, err := controller.RecoverTerminate(context.Background(), "effect-terminate"); err != nil || replay.ReceiptDigest != terminated.ReceiptDigest || len(store.JournalRecords()) != 5 {
		t.Fatal("terminate replay changed the permanent tombstone or journal")
	}
}

func TestControllerPrepareTerminateIntentReobservesLiveAndRejectsMarkerSwap(t *testing.T) {
	root := canonicalTempDir(t)
	session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
	controller, _ := openController(t, root, session)
	defer controller.Close()
	provisioned, err := controller.RecoverProvision(context.Background(), "effect-provision")
	if err != nil {
		t.Fatal(err)
	}
	request := testTerminateRequest(t, provisioned)
	intent, err := controller.PrepareTerminateIntent(request)
	if err != nil {
		t.Fatal(err)
	}
	if intent.LiveIdentity != provisioned.LiveIdentity || intent.MarkerIdentity != provisioned.MarkerIdentity || intent.Marker != provisioned.Marker || intent.MarkerDigest != provisioned.MarkerDigest {
		t.Fatal("terminate preparation did not use the current held live observation")
	}
	fact := committedFactForValue(RecordTerminateIntent, "terminate-intent", intent.Binding, intent.RequestDigest, intent)
	session.snapshot.TerminateIntent = &intent
	session.snapshot.TerminateIntentFactDigest = fact.AttemptAuthorityFactDigest
	session.snapshot.Facts = append(session.snapshot.Facts, fact)
	session.snapshotReads = 0

	objects := testObjectsPath(t, root, intent.Binding)
	markerPath := filepath.Join(objects, intent.LiveRelativeName, intent.MarkerRelativeName)
	oldMarkerPath := filepath.Join(root, "marker-before-swap")
	if err := os.Rename(markerPath, oldMarkerPath); err != nil {
		t.Fatal(err)
	}
	markerBytes, err := provisioned.Marker.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, markerBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.RecoverTerminate(context.Background(), "effect-terminate"); !errors.Is(err, ErrFilesystemConflict) {
		t.Fatal("marker pathname swap after authority append was not rejected")
	}
	if session.snapshot.TerminateReceipt != nil {
		t.Fatal("marker swap produced a terminate receipt")
	}
}

func testTerminateIntent(t *testing.T, receipt AllocationProvisionReceiptV1) AllocationTerminateIntentV1 {
	t.Helper()
	request := testTerminateRequest(t, receipt)
	intent, err := bindTerminateIntent(request, receipt.LiveIdentity, receipt.MarkerIdentity, receipt.Marker, receipt.MarkerDigest)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func testTerminateRequest(t *testing.T, receipt AllocationProvisionReceiptV1) TerminateRequestV1 {
	t.Helper()
	_, live, tombstone, _, err := DeriveRelativeNames(receipt.Binding.AllocationID)
	if err != nil {
		t.Fatal(err)
	}
	binding := receipt.Binding
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
	return request
}

func TestControllerRecoversEveryProvisionCommitBoundary(t *testing.T) {
	cases := []struct {
		name           string
		preparedError  bool
		receiptError   bool
		commitResponse bool
	}{
		{name: "marker durable before prepared fact", preparedError: true},
		{name: "prepared fact response lost", preparedError: true, commitResponse: true},
		{name: "rename durable before receipt fact", receiptError: true},
		{name: "receipt fact response lost", receiptError: true, commitResponse: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := canonicalTempDir(t)
			session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t), commitBeforeError: tc.commitResponse}
			if tc.preparedError {
				session.preparedError = errors.New("injected prepared append failure")
			}
			if tc.receiptError {
				session.provisionReceiptError = errors.New("injected receipt append failure")
			}
			controller, _ := openController(t, root, session)
			if _, err := controller.RecoverProvision(context.Background(), "effect-provision"); err == nil {
				t.Fatal("injected crash boundary did not stop the first call")
			}
			if err := controller.Close(); err != nil {
				t.Fatal(err)
			}
			session.preparedError = nil
			session.provisionReceiptError = nil
			session.commitBeforeError = false
			session.snapshotReads = 0
			restarted, store := openController(t, root, session)
			if _, err := restarted.RecoverProvision(context.Background(), "effect-provision"); err != nil {
				t.Fatal("restart did not converge the same provision command", err)
			}
			if len(store.JournalRecords()) != 3 {
				t.Fatal("restart did not rebuild the exact authority projection")
			}
			if err := restarted.Close(); err != nil {
				t.Fatal(err)
			}
			session.snapshotReads = 0
			secondRestart, secondStore := openController(t, root, session)
			defer secondRestart.Close()
			if _, err := secondRestart.RecoverProvision(context.Background(), "effect-provision"); err != nil || len(secondStore.JournalRecords()) != 3 {
				t.Fatal("second restart did not replay the same provision receipt")
			}
		})
	}
}

func TestControllerRecoversTerminateRenameAndReceiptBoundaries(t *testing.T) {
	for _, commitBeforeError := range []bool{false, true} {
		name := "rename-before-receipt"
		if commitBeforeError {
			name = "receipt-commit-response-lost"
		}
		t.Run(name, func(t *testing.T) {
			root := canonicalTempDir(t)
			session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
			controller, _ := openController(t, root, session)
			provisioned, err := controller.RecoverProvision(context.Background(), "effect-provision")
			if err != nil {
				t.Fatal(err)
			}
			terminate := testTerminateIntent(t, provisioned)
			fact := committedFactForValue(RecordTerminateIntent, "terminate-intent", terminate.Binding, terminate.RequestDigest, terminate)
			session.snapshot.TerminateIntent = &terminate
			session.snapshot.TerminateIntentFactDigest = fact.AttemptAuthorityFactDigest
			session.snapshot.Facts = append(session.snapshot.Facts, fact)
			session.snapshotReads = 0
			session.terminateReceiptError = errors.New("injected terminate receipt append failure")
			session.commitBeforeError = commitBeforeError
			if _, err := controller.RecoverTerminate(context.Background(), "effect-terminate"); err == nil {
				t.Fatal("injected terminate crash boundary did not stop the first call")
			}
			if err := controller.Close(); err != nil {
				t.Fatal(err)
			}
			session.terminateReceiptError = nil
			session.commitBeforeError = false
			session.snapshotReads = 0
			restarted, store := openController(t, root, session)
			defer restarted.Close()
			receipt, err := restarted.RecoverTerminate(context.Background(), "effect-terminate")
			if err != nil || !receipt.LiveAbsent || !receipt.TombstonePresent || len(store.JournalRecords()) != 5 {
				t.Fatal("restart did not converge the exact permanent tombstone", err)
			}
		})
	}
}

func TestControllerNeverClobbersExistingTombstoneTarget(t *testing.T) {
	root := canonicalTempDir(t)
	session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
	controller, _ := openController(t, root, session)
	defer controller.Close()
	provisioned, err := controller.RecoverProvision(context.Background(), "effect-provision")
	if err != nil {
		t.Fatal(err)
	}
	terminate := testTerminateIntent(t, provisioned)
	fact := committedFactForValue(RecordTerminateIntent, "terminate-intent", terminate.Binding, terminate.RequestDigest, terminate)
	session.snapshot.TerminateIntent = &terminate
	session.snapshot.TerminateIntentFactDigest = fact.AttemptAuthorityFactDigest
	session.snapshot.Facts = append(session.snapshot.Facts, fact)
	session.snapshotReads = 0
	objects := testObjectsPath(t, root, terminate.Binding)
	if err := os.Mkdir(filepath.Join(objects, terminate.TombstoneRelativeName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.RecoverTerminate(context.Background(), "effect-terminate"); !errors.Is(err, ErrFilesystemConflict) {
		t.Fatal("existing tombstone target was not a no-clobber conflict")
	}
	if session.snapshot.TerminateReceipt != nil {
		t.Fatal("tombstone target conflict produced a receipt")
	}
}

func TestControllerNeverClobbersExistingLiveTarget(t *testing.T) {
	root := canonicalTempDir(t)
	session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
	objects := testObjectsPath(t, root, session.snapshot.ProvisionIntent.Binding)
	session.afterPreparedCommit = func() {
		intent := *session.snapshot.ProvisionIntent
		_ = os.Mkdir(filepath.Join(objects, intent.LiveRelativeName), 0o700)
	}
	controller, _ := openController(t, root, session)
	defer controller.Close()
	if _, err := controller.RecoverProvision(context.Background(), "effect-provision"); !errors.Is(err, ErrFilesystemConflict) {
		t.Fatal("existing live target was not a no-clobber conflict")
	}
}

func TestControllerRejectsHardlinkedMarkerBeforeRename(t *testing.T) {
	root := canonicalTempDir(t)
	session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
	objects := testObjectsPath(t, root, session.snapshot.ProvisionIntent.Binding)
	session.afterPreparedCommit = func() {
		intent := *session.snapshot.ProvisionIntent
		marker := filepath.Join(objects, intent.StagingRelativeName, intent.MarkerRelativeName)
		_ = os.Link(marker, filepath.Join(root, "marker-hardlink"))
	}
	controller, _ := openController(t, root, session)
	defer controller.Close()
	if _, err := controller.RecoverProvision(context.Background(), "effect-provision"); !errors.Is(err, ErrFilesystemConflict) {
		t.Fatal("multiply-linked marker was not rejected before rename")
	}
}

func TestControllerRejectsPathSwapBeforeRename(t *testing.T) {
	root := canonicalTempDir(t)
	session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
	objects := testObjectsPath(t, root, session.snapshot.ProvisionIntent.Binding)
	session.afterPreparedCommit = func() {
		intent := *session.snapshot.ProvisionIntent
		aside := filepath.Join(objects, "attacker-aside")
		_ = os.Rename(filepath.Join(objects, intent.StagingRelativeName), aside)
		_ = os.Symlink("attacker-aside", filepath.Join(objects, intent.StagingRelativeName))
	}
	controller, _ := openController(t, root, session)
	if _, err := controller.RecoverProvision(context.Background(), "effect-provision"); !errors.Is(err, ErrFilesystemConflict) {
		t.Fatal("staging pathname swap was not rejected before rename")
	}
	intent := *session.snapshot.ProvisionIntent
	if _, err := os.Lstat(filepath.Join(objects, intent.LiveRelativeName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("path-swap rejection still created a live allocation")
	}
}

func TestControllerRejectsAuthorityDriftAdjacentToRename(t *testing.T) {
	root := canonicalTempDir(t)
	session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
	session.mutateBeforeSecondRead = func(snapshot *AuthoritySnapshot) {
		snapshot.Facts[0].AttemptAuthorityFactDigest = testDigest("drifted-current-head")
	}
	controller, _ := openController(t, root, session)
	if _, err := controller.RecoverProvision(context.Background(), "effect-provision"); !errors.Is(err, ErrAuthorityConflict) {
		t.Fatal("mutation-adjacent authority drift was not rejected")
	}
}

func TestTerminateNotFoundNeverBecomesSuccess(t *testing.T) {
	root := canonicalTempDir(t)
	session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
	controller, _ := openController(t, root, session)
	receipt, err := controller.RecoverProvision(context.Background(), "effect-provision")
	if err != nil {
		t.Fatal(err)
	}
	terminate := testTerminateIntent(t, receipt)
	fact := committedFactForValue(RecordTerminateIntent, "terminate-intent", terminate.Binding, terminate.RequestDigest, terminate)
	session.snapshot.TerminateIntent = &terminate
	session.snapshot.TerminateIntentFactDigest = fact.AttemptAuthorityFactDigest
	session.snapshot.Facts = append(session.snapshot.Facts, fact)
	session.snapshotReads = 0
	objects := testObjectsPath(t, root, session.snapshot.ProvisionIntent.Binding)
	if err := os.Rename(filepath.Join(objects, terminate.LiveRelativeName), filepath.Join(root, "unknown-object")); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.RecoverTerminate(context.Background(), "effect-terminate"); !errors.Is(err, ErrFilesystemUnknown) {
		t.Fatal("live+tombstone absence was treated as successful termination")
	}
	if session.snapshot.TerminateReceipt != nil {
		t.Fatal("unknown filesystem state produced a terminate receipt")
	}
}
