//go:build darwin

package allocationcontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/sandbox"
)

type exactEffectAuthority struct {
	effectKey string
	session   *fakeAllocationSession
	err       error
	calls     int
}

func (authority *exactEffectAuthority) WithCurrentAllocation(ctx context.Context, effectKey string, operation func(AuthoritySession) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	authority.calls++
	if authority.err != nil {
		return authority.err
	}
	if effectKey != authority.effectKey || operation == nil {
		return ErrAuthorityConflict
	}
	authority.session.mu.Lock()
	defer authority.session.mu.Unlock()
	return operation(authority.session)
}

func openProvisionedFacade(t *testing.T) (*DurableLocalFacade, *Controller, *Store, *fakeAllocationSession, string) {
	t.Helper()
	root := canonicalTempDir(t)
	session := &fakeAllocationSession{snapshot: initialAuthoritySnapshot(t)}
	controller, store := openController(t, root, session)
	if _, err := controller.RecoverProvision(context.Background(), "effect-provision"); err != nil {
		t.Fatal(err)
	}
	authority := &exactEffectAuthority{effectKey: "effect-provision", session: session}
	facade, err := NewDurableLocalFacade(store, authority, authority.effectKey, session.snapshot.ProvisionIntent.Binding)
	if err != nil {
		t.Fatal(err)
	}
	return facade, controller, store, session, root
}

func TestDurableLocalFacadeStagesAndReplaysWithoutAllocationEffects(t *testing.T) {
	facade, controller, store, session, _ := openProvisionedFacade(t)
	defer controller.Close()
	beforeJournal := store.JournalRecords()
	request := []byte(`{"kind":"WorkerRequest"}`)
	prompt := []byte("inspect only\n")
	inputs := []sandbox.StageInput{
		{InputId: "worker-request", DeclaredSHA256: sandbox.RecomputeSHA256(request), Inline: request},
		{InputId: "input/prompt.md", DeclaredSHA256: sandbox.RecomputeSHA256(prompt), Inline: prompt},
	}
	first, err := facade.Stage(context.Background(), inputs)
	if err != nil || len(first.Receipts) != len(inputs) {
		t.Fatalf("first Stage: report=%+v err=%v", first, err)
	}
	if raw, err := facade.ReadArtifact(context.Background(), "input/prompt.md", int64(len(prompt))); err != nil || !reflect.DeepEqual(raw, prompt) {
		t.Fatalf("ReadArtifact: raw=%q err=%v", raw, err)
	}
	currentBefore, err := facade.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := facade.Stage(context.Background(), inputs)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("exact Stage replay: first=%+v second=%+v err=%v", first, second, err)
	}
	currentAfter, err := facade.Current(context.Background())
	if err != nil || !reflect.DeepEqual(currentAfter, currentBefore) {
		t.Fatalf("Stage changed allocation identity: before=%+v after=%+v err=%v", currentBefore, currentAfter, err)
	}
	if !reflect.DeepEqual(store.JournalRecords(), beforeJournal) || session.snapshot.TerminateIntent != nil {
		t.Fatal("Stage appended allocation authority or initiated Terminate")
	}
}

func TestDurableLocalFacadeRecoversExactCrashTempAndRejectsDivergence(t *testing.T) {
	facade, controller, _, session, root := openProvisionedFacade(t)
	defer controller.Close()
	inputID := "input/task-spec.json"
	content := []byte("exact-stage-content")
	temp := ".stage-" + strings.TrimPrefix(canonical.DigestBytes([]byte(inputID+"\x00"+sandbox.RecomputeSHA256(content))), "sha256:")
	live := filepath.Join(testObjectsPath(t, root, session.snapshot.ProvisionIntent.Binding), session.snapshot.ProvisionReceipt.LiveRelativeName)
	parent := filepath.Join(live, "input")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, temp), content, 0o600); err != nil {
		t.Fatal(err)
	}
	input := sandbox.StageInput{InputId: inputID, DeclaredSHA256: sandbox.RecomputeSHA256(content), Inline: content}
	if _, err := facade.Stage(context.Background(), []sandbox.StageInput{input}); err != nil {
		t.Fatalf("exact crash temp was not recovered: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(parent, temp)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("crash temp remains after exact promotion")
	}
	if err := os.WriteFile(filepath.Join(parent, inputID[strings.LastIndexByte(inputID, '/')+1:]), []byte("divergent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := facade.Stage(context.Background(), []sandbox.StageInput{input}); !errors.Is(err, ErrAllocationIntervention) {
		t.Fatalf("divergent replay did not require intervention: %v", err)
	}
}

func TestDurableLocalFacadeRejectsForgedStaleAndTerminalAuthorityBeforeMutation(t *testing.T) {
	facade, controller, _, session, root := openProvisionedFacade(t)
	defer controller.Close()
	live := filepath.Join(testObjectsPath(t, root, session.snapshot.ProvisionIntent.Binding), session.snapshot.ProvisionReceipt.LiveRelativeName)
	before, err := os.ReadDir(live)
	if err != nil {
		t.Fatal(err)
	}
	input := sandbox.StageInput{InputId: "worker-request", DeclaredSHA256: sandbox.RecomputeSHA256([]byte("x")), Inline: []byte("x")}

	forged := *session.snapshot.ProvisionReceipt
	forged.ReceiptDigest = testDigest("forged-receipt")
	session.snapshot.ProvisionReceipt = &forged
	session.snapshotReads = 0
	if _, err := facade.Stage(context.Background(), []sandbox.StageInput{input}); !errors.Is(err, ErrAllocationIntervention) {
		t.Fatalf("forged receipt was accepted: %v", err)
	}

	provisioned := forged
	provisioned.ReceiptDigest, _ = provisioned.digest()
	session.snapshot.ProvisionReceipt = &provisioned
	terminate := testTerminateIntent(t, provisioned)
	terminateFact := committedFactForValue(RecordTerminateIntent, "terminate-intent", terminate.Binding, terminate.RequestDigest, terminate)
	session.snapshot.TerminateIntent = &terminate
	session.snapshot.TerminateIntentFactDigest = terminateFact.AttemptAuthorityFactDigest
	session.snapshot.Facts = append(session.snapshot.Facts, terminateFact)
	session.snapshotReads = 0
	if _, err := facade.Stage(context.Background(), []sandbox.StageInput{input}); !errors.Is(err, ErrAllocationIntervention) {
		t.Fatalf("terminalizing allocation was staged: %v", err)
	}
	after, err := os.ReadDir(live)
	if err != nil || !reflect.DeepEqual(directoryNames(before), directoryNames(after)) {
		t.Fatalf("failed authority mutated live directory: before=%v after=%v err=%v", directoryNames(before), directoryNames(after), err)
	}
}

func TestDurableLocalFacadeRejectsCrossAttemptAndHeldRootDrift(t *testing.T) {
	facade, controller, store, session, root := openProvisionedFacade(t)
	defer controller.Close()
	wrong := session.snapshot.ProvisionIntent.Binding
	wrong.AttemptID = "attempt-other"
	wrong.CommandID = "command-other"
	wrong.IdempotencyKey = "idempotency-other"
	authority := &exactEffectAuthority{effectKey: "effect-provision", session: session}
	cross, err := NewDurableLocalFacade(store, authority, authority.effectKey, wrong)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cross.Current(context.Background()); !errors.Is(err, ErrAllocationIntervention) {
		t.Fatalf("cross-attempt facade was accepted: %v", err)
	}
	wrongNamespace := session.snapshot.ProvisionIntent.Binding
	wrongNamespace.AuthorityNamespaceID = testDigest("other-repository-namespace")
	crossRepository, err := NewDurableLocalFacade(store, authority, authority.effectKey, wrongNamespace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := crossRepository.Current(context.Background()); !errors.Is(err, ErrAllocationIntervention) {
		t.Fatalf("cross-repository facade was accepted: %v", err)
	}

	scope, _ := StoreScopeForBinding(session.snapshot.ProvisionIntent.Binding)
	scopeName, _ := scope.directoryName()
	base := filepath.Join(root, storeDirectoryName, scopeName)
	objects := filepath.Join(base, objectsDirectoryName)
	old := filepath.Join(base, "objects-swapped")
	if err := os.Rename(objects, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(objects, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := facade.Current(context.Background()); !errors.Is(err, ErrAllocationIntervention) {
		t.Fatalf("held root ABA/path drift was accepted: %v", err)
	}
}

func TestDurableLocalFacadeCompletionRewalkRejectsDetachedLineage(t *testing.T) {
	t.Run("nested parent replaced after mutation", func(t *testing.T) {
		facade, controller, store, session, root := openProvisionedFacade(t)
		defer controller.Close()
		content := []byte("held-lineage")
		input := sandbox.StageInput{InputId: "input/task.txt", DeclaredSHA256: sandbox.RecomputeSHA256(content), Inline: content}
		if _, err := facade.Stage(context.Background(), []sandbox.StageInput{input}); err != nil {
			t.Fatal(err)
		}
		live := filepath.Join(testObjectsPath(t, root, session.snapshot.ProvisionIntent.Binding), session.snapshot.ProvisionReceipt.LiveRelativeName)
		if err := os.Rename(filepath.Join(live, "input"), filepath.Join(live, "input-detached")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(live, "input"), 0o700); err != nil {
			t.Fatal(err)
		}
		store.mu.Lock()
		err := store.verifyCurrentStageFileLocked(session.snapshot, store.objectsIdentity, []string{"input", "task.txt"}, content)
		store.mu.Unlock()
		if !errors.Is(err, ErrFilesystemUnknown) && !errors.Is(err, ErrFilesystemConflict) {
			t.Fatalf("detached nested inode passed completion rewalk: %v", err)
		}
	})

	t.Run("live directory replaced after mutation", func(t *testing.T) {
		facade, controller, store, session, root := openProvisionedFacade(t)
		defer controller.Close()
		content := []byte("held-live")
		input := sandbox.StageInput{InputId: "worker-request", DeclaredSHA256: sandbox.RecomputeSHA256(content), Inline: content}
		if _, err := facade.Stage(context.Background(), []sandbox.StageInput{input}); err != nil {
			t.Fatal(err)
		}
		objects := testObjectsPath(t, root, session.snapshot.ProvisionIntent.Binding)
		liveName := session.snapshot.ProvisionReceipt.LiveRelativeName
		if err := os.Rename(filepath.Join(objects, liveName), filepath.Join(objects, liveName+"-detached")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(objects, liveName), 0o700); err != nil {
			t.Fatal(err)
		}
		store.mu.Lock()
		err := store.verifyCurrentStageFileLocked(session.snapshot, store.objectsIdentity, []string{"worker-request"}, content)
		store.mu.Unlock()
		if !errors.Is(err, ErrFilesystemConflict) {
			t.Fatalf("detached live inode passed completion rewalk: %v", err)
		}
	})
}

func directoryNames(entries []os.DirEntry) []string {
	result := make([]string, len(entries))
	for index, entry := range entries {
		result[index] = entry.Name()
	}
	return result
}
