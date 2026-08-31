package resultingress

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
)

func TestExistingWorktreeFactsAdvanceSameAttemptLedgerAndRecoverSameBytes(t *testing.T) {
	directory := t.TempDir()
	store, err := OpenResultIngressStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	_, _, opened := existingWorktreeBridgeFixture(t, store, id)
	request, observation := existingWorktreeAuthorityTestBind(t, id, opened)
	bindingDigest, err := request.Binding.Digest()
	if err != nil {
		t.Fatal(err)
	}
	intent := allocationcontrol.ExistingWorktreeBindIntentV1{Request: request, Observation: observation, BindingDigest: bindingDigest, PredecessorRB1HeadDigest: opened.HeadDigest}
	if err := intent.Seal(); err != nil {
		t.Fatal(err)
	}
	intentSnapshot, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactBindIntent, intent)
	if err != nil || intentSnapshot.CurrentAttemptRevision != opened.Revision+1 || intentSnapshot.CurrentAttemptHeadDigest == opened.HeadDigest || len(intentSnapshot.Facts) != 1 {
		t.Fatalf("bind intent snapshot=%+v err=%v", intentSnapshot, err)
	}
	receipt := allocationcontrol.ExistingWorktreeBindReceiptV1{
		Binding: request.Binding, RequestDigest: request.RequestDigest,
		IntentFactDigest: intentSnapshot.Facts[0].AttemptFactDigest,
		Observation:      observation, BindingDigest: bindingDigest,
		PredecessorRB1HeadDigest: intentSnapshot.CurrentAttemptHeadDigest,
		Disposition:              allocationcontrol.DispositionApplied,
	}
	if err := receipt.Seal(); err != nil || receipt.Validate(intent) != nil {
		t.Fatalf("seal receipt: %v", err)
	}
	receiptSnapshot, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactBindReceipt, receipt)
	if err != nil || receiptSnapshot.CurrentAttemptRevision != opened.Revision+2 || len(receiptSnapshot.Facts) != 2 {
		t.Fatalf("bind receipt snapshot=%+v err=%v", receiptSnapshot, err)
	}
	current, found, err := store.AttemptState(id)
	if err != nil || !found || current.Revision != receiptSnapshot.CurrentAttemptRevision || current.HeadDigest != receiptSnapshot.CurrentAttemptHeadDigest || current.OpenedDigest != opened.OpenedDigest {
		t.Fatalf("shared attempt current=%+v found=%v err=%v", current, found, err)
	}
	ledgerBefore, err := os.ReadFile(store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenResultIngressStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.loadExistingWorktreeSnapshot(id)
	if err != nil || !reflect.DeepEqual(receiptSnapshot, recovered) {
		t.Fatalf("recovered snapshot=%+v err=%v want=%+v", recovered, err, receiptSnapshot)
	}
	ledgerAfter, err := os.ReadFile(reopened.ledgerPath())
	if err != nil || !bytes.Equal(ledgerBefore, ledgerAfter) {
		t.Fatal("restart changed the shared Attempt ledger bytes")
	}

	forged := intent
	forged.Request.Binding.AllocationID = "foreign-allocation"
	forged.Request.RequestDigest = ""
	if err := forged.Request.Seal(); err != nil {
		t.Fatal(err)
	}
	forged.BindingDigest, _ = forged.Request.Binding.Digest()
	forged.PredecessorRB1HeadDigest = recovered.CurrentAttemptHeadDigest
	forged.IntentDigest = ""
	if err := forged.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactBindIntent, forged); err == nil {
		t.Fatal("foreign allocation binding appended to the current Attempt")
	}
	ledgerFinal, readErr := os.ReadFile(reopened.ledgerPath())
	if readErr != nil || !bytes.Equal(ledgerAfter, ledgerFinal) {
		t.Fatal("rejected foreign binding mutated the shared Attempt ledger")
	}
}

func existingWorktreeAuthorityTestBind(t *testing.T, id AttemptIdentity, opened AttemptAuthorityState) (allocationcontrol.ExistingWorktreeBindRequestV1, allocationcontrol.ExistingWorktreeObservationV1) {
	t.Helper()
	namespace, err := id.AuthorityNamespaceID.Digest()
	if err != nil {
		t.Fatal(err)
	}
	directory := existingWorktreeAuthorityTestObject("10", allocationcontrol.ObjectTypeDirectory, 0o040700, 2, 0)
	parent := existingWorktreeAuthorityTestObject("11", allocationcontrol.ObjectTypeDirectory, 0o040700, 2, 0)
	regular := func(inode string, size int64) allocationcontrol.ObjectIdentityV1 {
		return existingWorktreeAuthorityTestObject(inode, allocationcontrol.ObjectTypeRegular, 0o100600, 1, size)
	}
	current := allocationcontrol.CurrentNameIdentityV1{ParentIdentity: parent, ParentMutationDigest: attemptTestDigest("parent-mutation"), RelativeName: "worktree", ObjectIdentity: directory, ObjectMutationDigest: attemptTestDigest("target-mutation")}
	admin := allocationcontrol.CurrentNameIdentityV1{ParentIdentity: parent, ParentMutationDigest: attemptTestDigest("admin-parent-mutation"), RelativeName: "worktrees-admin", ObjectIdentity: existingWorktreeAuthorityTestObject("12", allocationcontrol.ObjectTypeDirectory, 0o040700, 2, 0), ObjectMutationDigest: attemptTestDigest("admin-mutation")}
	observation := allocationcontrol.ExistingWorktreeObservationV1{
		TargetCurrentName: current, TargetLocatorDigest: attemptTestDigest("target-locator"),
		Git: allocationcontrol.ExistingGitWorktreeIdentityV1{
			DotGitIdentity: regular("20", 64), DotGitDigest: attemptTestDigest("dot-git"), DotGitMutationDigest: attemptTestDigest("dot-git-mutation"),
			AdminCurrentName: admin, AdminLocatorDigest: attemptTestDigest("admin-locator"),
			AdminGitdirIdentity: regular("21", 64), AdminGitdirDigest: attemptTestDigest("admin-gitdir"), AdminGitdirMutationDigest: attemptTestDigest("admin-gitdir-mutation"),
			CommonDirFileIdentity: regular("22", 3), CommonDirFileDigest: attemptTestDigest("common-file"), CommonDirFileMutationDigest: attemptTestDigest("common-file-mutation"),
			CommonDirectoryIdentity: existingWorktreeAuthorityTestObject("23", allocationcontrol.ObjectTypeDirectory, 0o040700, 2, 0), CommonDirectoryMutationDigest: attemptTestDigest("common-mutation"), CommonDirectoryLocatorDigest: attemptTestDigest("common-locator"),
			HeadIdentity: regular("24", 41), HeadDigest: attemptTestDigest("head-bytes"), HeadMutationDigest: attemptTestDigest("head-mutation"),
			IndexIdentity: regular("25", 128), IndexDigest: attemptTestDigest("index-bytes"), IndexMutationDigest: attemptTestDigest("index-mutation"),
			HeadSHA: strings.Repeat("a", 40), CleanStatusDigest: attemptTestDigest("clean"),
		},
	}
	if err := observation.Seal(); err != nil {
		t.Fatal(err)
	}
	binding := allocationcontrol.ExistingWorktreeBindingV1{
		AuthorityNamespaceID: namespace, RepositoryOwnerDigest: attemptTestDigest("repository-owner"), TaskID: id.TaskID, RunID: id.RunID, AttemptID: id.AttemptID,
		ReservationFactDigest: opened.ReservationFactDigest, AttemptOpenedFactDigest: opened.OpenedDigest, AllocationID: id.AllocationID, LeaseID: id.LeaseID,
		Generation: id.DispatchGeneration, FencingTokenDigest: id.FencingTokenDigest, FrozenInputsDigest: attemptTestDigest("frozen-inputs"), ExpectedAttemptSequence: opened.Revision,
	}
	request := allocationcontrol.ExistingWorktreeBindRequestV1{Binding: binding, WorktreePath: "/private/tmp/marshal-existing-worktree-test", ExpectedWorktreeIdentity: directory, ExpectedBaseSHA: observation.Git.HeadSHA, RunDirectoryIdentity: existingWorktreeAuthorityTestObject("30", allocationcontrol.ObjectTypeDirectory, 0o040700, 2, 0), RunAuthorityHeadDigest: id.RunAuthorityDigest}
	if err := request.Seal(); err != nil {
		t.Fatal(err)
	}
	return request, observation
}

func existingWorktreeAuthorityTestObject(inode, objectType string, mode uint32, nlink uint64, size int64) allocationcontrol.ObjectIdentityV1 {
	return allocationcontrol.ObjectIdentityV1{Device: "1", Inode: inode, Mode: mode, UID: 501, GID: 20, Size: size, Nlink: nlink, Type: objectType}
}
