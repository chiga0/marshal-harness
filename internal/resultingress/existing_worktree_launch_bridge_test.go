package resultingress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
)

// existingWorktreeBridgeChain captures the exact bind chain appended for a
// governed v2 Attempt so launch/PreparedExecution and rejection tests can bind
// and revalidate the same receipts.
type existingWorktreeBridgeChain struct {
	binding           allocationcontrol.ExistingWorktreeBindingV1
	bindingDigest     string
	observation       allocationcontrol.ExistingWorktreeObservationV1
	intent            allocationcontrol.ExistingWorktreeBindIntentV1
	receipt           allocationcontrol.ExistingWorktreeBindReceiptV1
	intentFactDigest  string
	receiptFactDigest string
	state             AttemptAuthorityState
}

// existingWorktreeBridgeFixture opens a fresh reserved v2 Attempt and binds the
// repository owner, leaving the Attempt in LaunchNotAuthorized with a current
// control-owner binding ready for an existing-worktree bind chain.
func existingWorktreeBridgeFixture(t *testing.T, store *DurableStore, id AttemptIdentity) (ControlOwnerState, attemptOwnerVerifier, AttemptAuthorityState) {
	t.Helper()
	opened := appendFreshReservedAttempt(t, store, id)
	owner, verifier := supervisorTestAcquireOwner(t, store, id)
	bound := supervisorTestBindOwner(t, store, opened, owner, verifier)
	return owner, verifier, bound
}

// appendExistingWorktreeBindChain appends the RB1 existing-worktree bind-intent
// + bind-receipt closed-union prefix for a governed v2 Attempt, binding the
// exact reservation and opened fact digests. It returns the chain and the
// updated Attempt state.
func appendExistingWorktreeBindChain(t *testing.T, store *DurableStore, id AttemptIdentity, state AttemptAuthorityState) existingWorktreeBridgeChain {
	t.Helper()
	_, observation := existingWorktreeAuthorityTestBind(t, id, state)
	namespace, err := id.AuthorityNamespaceID.Digest()
	if err != nil {
		t.Fatal(err)
	}
	binding := allocationcontrol.ExistingWorktreeBindingV1{
		AuthorityNamespaceID:    namespace,
		RepositoryOwnerDigest:   attemptTestDigest("repository-owner"),
		TaskID:                  id.TaskID,
		RunID:                   id.RunID,
		AttemptID:               id.AttemptID,
		ReservationFactDigest:   state.ReservationFactDigest,
		AttemptOpenedFactDigest: state.OpenedDigest,
		AllocationID:            id.AllocationID,
		LeaseID:                 id.LeaseID,
		Generation:              id.DispatchGeneration,
		FencingTokenDigest:      id.FencingTokenDigest,
		FrozenInputsDigest:      attemptTestDigest("frozen-inputs"),
		ExpectedAttemptSequence: state.Revision,
	}
	if err := binding.Validate(); err != nil {
		t.Fatalf("binding validate: %v", err)
	}
	request := allocationcontrol.ExistingWorktreeBindRequestV1{
		Binding: binding, WorktreePath: "/private/tmp/marshal-existing-worktree-launch-test",
		ExpectedWorktreeIdentity: observation.TargetCurrentName.ObjectIdentity,
		ExpectedBaseSHA:          observation.Git.HeadSHA,
		RunDirectoryIdentity:     existingWorktreeAuthorityTestObject("30", allocationcontrol.ObjectTypeDirectory, 0o040700, 2, 0),
		RunAuthorityHeadDigest:   id.RunAuthorityDigest,
	}
	if err := request.Seal(); err != nil {
		t.Fatalf("request seal: %v", err)
	}
	bindingDigest, err := request.Binding.Digest()
	if err != nil {
		t.Fatal(err)
	}
	intent := allocationcontrol.ExistingWorktreeBindIntentV1{Request: request, Observation: observation, BindingDigest: bindingDigest, PredecessorRB1HeadDigest: state.HeadDigest}
	if err := intent.Seal(); err != nil {
		t.Fatalf("intent seal: %v", err)
	}
	intentSnapshot, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactBindIntent, intent)
	if err != nil {
		t.Fatalf("append bind-intent: %v", err)
	}
	intentFactDigest := intentSnapshot.Facts[len(intentSnapshot.Facts)-1].AttemptFactDigest
	receipt := allocationcontrol.ExistingWorktreeBindReceiptV1{
		Binding: request.Binding, RequestDigest: request.RequestDigest,
		IntentFactDigest:         intentFactDigest,
		Observation:              observation,
		BindingDigest:            bindingDigest,
		PredecessorRB1HeadDigest: intentSnapshot.CurrentAttemptHeadDigest,
		Disposition:              allocationcontrol.DispositionApplied,
	}
	if err := receipt.Seal(); err != nil {
		t.Fatalf("receipt seal: %v", err)
	}
	if err := receipt.Validate(intent); err != nil {
		t.Fatalf("receipt validate: %v", err)
	}
	receiptSnapshot, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactBindReceipt, receipt)
	if err != nil {
		t.Fatalf("append bind-receipt: %v", err)
	}
	receiptFactDigest := receiptSnapshot.Facts[len(receiptSnapshot.Facts)-1].AttemptFactDigest
	current, _, err := store.AttemptState(id)
	if err != nil {
		t.Fatal(err)
	}
	return existingWorktreeBridgeChain{
		binding: binding, bindingDigest: bindingDigest, observation: observation,
		intent: intent, receipt: receipt, intentFactDigest: intentFactDigest, receiptFactDigest: receiptFactDigest,
		state: current,
	}
}

// appendExistingWorktreeReleaseChain appends the RB1 release-intent +
// release-receipt for an already-bound Attempt. It uses artificial but
// structurally valid terminalization fields; the launch bridge only needs to
// prove that a released binding cannot authorize launch.
func appendExistingWorktreeReleaseChain(t *testing.T, store *DurableStore, id AttemptIdentity, chain existingWorktreeBridgeChain) AttemptAuthorityState {
	t.Helper()
	state := chain.state
	releaseRequest := allocationcontrol.ExistingWorktreeReleaseRequestV1{
		Binding:                    chain.binding,
		BindingReceiptDigest:       chain.receipt.ReceiptDigest,
		TerminalizationID:          "terminal-release-test",
		CleanupBindingDigest:       attemptTestDigest("cleanup-binding"),
		ProcessTerminalFactDigest:  attemptTestDigest("process-terminal"),
		CleanupDisposition:         "preserved",
		RunAuthorityHeadDigest:     id.RunAuthorityDigest,
		AttemptAuthorityHeadDigest: state.HeadDigest,
	}
	if err := releaseRequest.Seal(); err != nil {
		t.Fatalf("release request seal: %v", err)
	}
	releaseIntent := allocationcontrol.ExistingWorktreeReleaseIntentV1{
		Request: releaseRequest, TargetIdentityDigest: chain.observation.TargetIdentityDigest,
		BindingDigest: chain.bindingDigest, PredecessorRB1HeadDigest: state.HeadDigest,
	}
	if err := releaseIntent.Seal(); err != nil {
		t.Fatalf("release intent seal: %v", err)
	}
	intentSnapshot, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactReleaseIntent, releaseIntent)
	if err != nil {
		t.Fatalf("append release-intent: %v", err)
	}
	pendingRequest, pendingReceipt, found, complete, err := store.CurrentExistingWorktreeRelease(id)
	if err != nil || !found || complete || pendingRequest != releaseRequest || pendingReceipt != (allocationcontrol.ExistingWorktreeReleaseReceiptV1{}) {
		t.Fatalf("pending release replay request=%+v receipt=%+v found=%t complete=%t err=%v", pendingRequest, pendingReceipt, found, complete, err)
	}
	releaseIntentFactDigest := intentSnapshot.Facts[len(intentSnapshot.Facts)-1].AttemptFactDigest
	releaseReceipt := allocationcontrol.ExistingWorktreeReleaseReceiptV1{
		Binding: chain.binding, RequestDigest: releaseRequest.RequestDigest,
		ReleaseIntentFactDigest:  releaseIntentFactDigest,
		BindingReceiptDigest:     chain.receipt.ReceiptDigest,
		TargetIdentityDigest:     chain.observation.TargetIdentityDigest,
		PredecessorRB1HeadDigest: intentSnapshot.CurrentAttemptHeadDigest,
		Disposition:              "released",
	}
	if err := releaseReceipt.Seal(); err != nil {
		t.Fatalf("release receipt seal: %v", err)
	}
	if err := releaseReceipt.Validate(releaseIntent); err != nil {
		t.Fatalf("release receipt validate: %v", err)
	}
	if _, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactReleaseReceipt, releaseReceipt); err != nil {
		t.Fatalf("append release-receipt: %v", err)
	}
	current, _, err := store.AttemptState(id)
	if err != nil {
		t.Fatal(err)
	}
	replayedRequest, replayedReceipt, found, complete, err := store.CurrentExistingWorktreeRelease(id)
	if err != nil || !found || !complete || replayedRequest != releaseRequest || replayedReceipt != releaseReceipt {
		t.Fatalf("release replay request=%+v receipt=%+v found=%t complete=%t err=%v", replayedRequest, replayedReceipt, found, complete, err)
	}
	if current.ExistingWorktreeReleaseReceiptDigest != releaseReceipt.ReceiptDigest {
		t.Fatalf("release receipt projection=%q want=%q", current.ExistingWorktreeReleaseReceiptDigest, releaseReceipt.ReceiptDigest)
	}
	return current
}

func TestExistingWorktreeBindReceiptEnablesLaunchAndPreparedExecution(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	owner, verifier, bound := existingWorktreeBridgeFixture(t, store, id)
	chain := appendExistingWorktreeBindChain(t, store, id, bound)
	if chain.state.ExistingWorktreeBindReceiptFactDigest == "" || chain.state.ExistingWorktreeBindReceiptDigest == "" {
		t.Fatalf("bind receipt not projected onto Attempt state: %+v", chain.state)
	}
	if chain.state.ExistingWorktreeBindReceiptFactDigest != chain.receiptFactDigest || chain.state.ExistingWorktreeBindReceiptDigest != chain.receipt.ReceiptDigest {
		t.Fatalf("projected bind receipt digest mismatch: fact=%q want=%q receipt=%q want=%q", chain.state.ExistingWorktreeBindReceiptFactDigest, chain.receiptFactDigest, chain.state.ExistingWorktreeBindReceiptDigest, chain.receipt.ReceiptDigest)
	}
	// Path B authorizes launch without an AllocationProvision effect reconcile.
	closure := preparedTestPiClosure(t)
	launch, err := appendAuthorizedAttempt(t, store, chain.state.Revision, chain.state.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "worktree-launch", LaunchClosure: closure})
	if err != nil {
		t.Fatalf("launch-authorized via existing-worktree bind receipt: %v", err)
	}
	if !launch.Appended || launch.State.LaunchState != LaunchUncertain {
		t.Fatalf("launch not appended: %+v", launch)
	}
	// PreparedExecution binds and revalidates the exact existing-worktree receipt.
	prepared, err := store.CreatePreparedExecution(context.Background(), verifier, owner.Acquisition, PreparedExecutionCreation{Identity: id, ExpectedRunSequence: 2, ExpectedRunAuthorityHead: id.RunAuthorityDigest})
	if err != nil {
		t.Fatalf("CreatePreparedExecution via path B: %v", err)
	}
	if prepared.ExistingWorktreeBindReceiptFactDigest != chain.receiptFactDigest || prepared.ExistingWorktreeBindReceiptDigest != chain.receipt.ReceiptDigest {
		t.Fatalf("PreparedExecution bound wrong worktree receipt: fact=%q want=%q receipt=%q want=%q", prepared.ExistingWorktreeBindReceiptFactDigest, chain.receiptFactDigest, prepared.ExistingWorktreeBindReceiptDigest, chain.receipt.ReceiptDigest)
	}
	if prepared.AllocationProvisionReceiptFactDigest != "" || prepared.AllocationProvisionReceiptDigest != "" {
		t.Fatalf("PreparedExecution synthesized a provision receipt for path B: %+v", prepared)
	}
	resolved, err := store.ResolvePreparedExecution(context.Background(), verifier, owner.Acquisition, prepared.PreparationDigest)
	if err != nil || resolved != prepared {
		t.Fatalf("ResolvePreparedExecution path B: resolved=%+v err=%v", resolved, err)
	}
	// Path B must not require an AllocationProvision effect reconcile.
	if _, _, _, allocErr := store.loadAllocationEffect(mustEffectKey(id.AuthorityNamespaceID, "provision-"+id.AttemptID)); allocErr == nil {
		t.Fatal("path B PreparedExecution unexpectedly found an AllocationProvision effect")
	}
}

func TestExistingWorktreeIntentOnlyRejectsLaunch(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	_, _, bound := existingWorktreeBridgeFixture(t, store, id)
	// Append only the bind-intent (no receipt): incomplete chain.
	_, observation := existingWorktreeAuthorityTestBind(t, id, bound)
	namespace, _ := id.AuthorityNamespaceID.Digest()
	binding := allocationcontrol.ExistingWorktreeBindingV1{
		AuthorityNamespaceID: namespace, RepositoryOwnerDigest: attemptTestDigest("repository-owner"),
		TaskID: id.TaskID, RunID: id.RunID, AttemptID: id.AttemptID,
		ReservationFactDigest: bound.ReservationFactDigest, AttemptOpenedFactDigest: bound.OpenedDigest,
		AllocationID: id.AllocationID, LeaseID: id.LeaseID, Generation: id.DispatchGeneration,
		FencingTokenDigest: id.FencingTokenDigest, FrozenInputsDigest: attemptTestDigest("frozen-inputs"),
		ExpectedAttemptSequence: bound.Revision,
	}
	request := allocationcontrol.ExistingWorktreeBindRequestV1{Binding: binding, WorktreePath: "/private/tmp/marshal-existing-worktree-intent-only", ExpectedWorktreeIdentity: observation.TargetCurrentName.ObjectIdentity, ExpectedBaseSHA: observation.Git.HeadSHA, RunDirectoryIdentity: existingWorktreeAuthorityTestObject("30", allocationcontrol.ObjectTypeDirectory, 0o040700, 2, 0), RunAuthorityHeadDigest: id.RunAuthorityDigest}
	if err := request.Seal(); err != nil {
		t.Fatal(err)
	}
	bindingDigest, _ := request.Binding.Digest()
	intent := allocationcontrol.ExistingWorktreeBindIntentV1{Request: request, Observation: observation, BindingDigest: bindingDigest, PredecessorRB1HeadDigest: bound.HeadDigest}
	if err := intent.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactBindIntent, intent); err != nil {
		t.Fatal(err)
	}
	current, _, _ := store.AttemptState(id)
	if current.ExistingWorktreeBindReceiptFactDigest != "" {
		t.Fatalf("intent-only projected a bind receipt: %+v", current)
	}
	if current.ExistingWorktreeBindIntentFactDigest == "" {
		t.Fatal("bind intent was not projected onto Attempt authority")
	}
	// Mutual exclusion begins at intent, not only once the bind receipt exists.
	authorityPort := allocationTestAuthority(t, store, current.Identity, true, false)
	generic, typed := allocationTestProvisionIntent(t, current, "provision-after-bind-intent")
	if _, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), current.Identity, generic, typed); !errors.Is(err, ErrEffectAuthorityOrder) {
		t.Fatalf("provision after bind intent err=%v, want ErrEffectAuthorityOrder", err)
	}
	// Launch must reject: no bind receipt and no provision.
	_, err = appendAuthorizedAttempt(t, store, current.Revision, current.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "intent-only-launch", LaunchClosure: attemptTestClosure(t)})
	if !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("intent-only launch err=%v, want ErrAttemptAuthorityOrder", err)
	}
	after, _, _ := store.AttemptState(id)
	if after.LaunchState != LaunchNotAuthorized {
		t.Fatalf("intent-only authorized launch: %+v", after)
	}
}

func TestExistingWorktreeFreshBindRejectsLegacyAttemptWithoutReservation(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	// Build otherwise-valid v2 binding bytes. They must not be appendable to
	// the legacy v1 Attempt, even if every identity field except reservation
	// matches, because legacy attempts are replay-only for this fact family.
	frozen := opened.State
	frozen.ReservationFactDigest = attemptTestDigest("legacy-forged-reservation")
	request, observation := existingWorktreeAuthorityTestBind(t, id, frozen)
	bindingDigest, err := request.Binding.Digest()
	if err != nil {
		t.Fatal(err)
	}
	intent := allocationcontrol.ExistingWorktreeBindIntentV1{Request: request, Observation: observation, BindingDigest: bindingDigest, PredecessorRB1HeadDigest: opened.State.HeadDigest}
	if err := intent.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactBindIntent, intent); !errors.Is(err, ErrExistingWorktreeAuthorityConflict) {
		t.Fatalf("legacy fresh bind err=%v, want ErrExistingWorktreeAuthorityConflict", err)
	}
}

func TestExistingWorktreeReceiptFromSiblingRejectsLaunch(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	primary := attemptTestIdentity()
	sibling := attemptTestIdentity()
	sibling.RunID = "run-sibling"
	sibling.RunAuthorityDigest = attemptTestDigest("run-authority-sibling")
	sibling.AttemptID, sibling.AllocationID, sibling.LeaseID = "attempt-sibling", "allocation-sibling", "lease-sibling"
	sibling.LeaseDigest = attemptTestDigest("lease-sibling")
	_, _, primaryBound := existingWorktreeBridgeFixture(t, store, primary)
	chain := appendExistingWorktreeBindChain(t, store, primary, primaryBound)
	// The sibling Attempt has its own reservation/opened; it must not inherit
	// the primary's bind receipt.
	_, _, siblingBound := existingWorktreeBridgeFixture(t, store, sibling)
	// Appending the primary's bind-intent to the sibling Attempt must reject:
	// the binding references the primary's AttemptID/opened/reservation.
	siblingNamespace, _ := sibling.AuthorityNamespaceID.Digest()
	_ = siblingNamespace
	forgedIntent := chain.intent
	forgedIntent.PredecessorRB1HeadDigest = siblingBound.HeadDigest
	forgedIntent.IntentDigest = ""
	if err := forgedIntent.Seal(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.appendExistingWorktreeFact(sibling, allocationcontrol.ExistingWorktreeFactBindIntent, forgedIntent); !errors.Is(err, ErrExistingWorktreeAuthorityConflict) {
		t.Fatalf("sibling bind-intent err=%v, want ErrExistingWorktreeAuthorityConflict", err)
	}
	// Launch on the sibling must reject: no allocation authority.
	_, err = appendAuthorizedAttempt(t, store, siblingBound.Revision, siblingBound.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: sibling, LaunchAuthorizationID: "sibling-launch", LaunchClosure: attemptTestClosure(t)})
	if !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("sibling launch err=%v, want ErrAttemptAuthorityOrder", err)
	}
	siblingAfter, _, _ := store.AttemptState(sibling)
	if siblingAfter.LaunchState != LaunchNotAuthorized {
		t.Fatalf("sibling authorized launch via primary receipt: %+v", siblingAfter)
	}
}

func TestExistingWorktreeAndProvisionMixedRejectsRegardlessOrder(t *testing.T) {
	// Order 1: AllocationProvision (A) first, then existing-worktree bind (B).
	t.Run("provisionThenBind", func(t *testing.T) {
		store, err := OpenResultIngressStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		id := attemptTestIdentity()
		_, _, bound := existingWorktreeBridgeFixture(t, store, id)
		provisioned := appendTestAcceptedProvision(t, store, bound)
		// A bind-intent must reject because path A is already established.
		_, observation := existingWorktreeAuthorityTestBind(t, id, provisioned)
		namespace, _ := id.AuthorityNamespaceID.Digest()
		binding := allocationcontrol.ExistingWorktreeBindingV1{AuthorityNamespaceID: namespace, RepositoryOwnerDigest: attemptTestDigest("repository-owner"), TaskID: id.TaskID, RunID: id.RunID, AttemptID: id.AttemptID, ReservationFactDigest: provisioned.ReservationFactDigest, AttemptOpenedFactDigest: provisioned.OpenedDigest, AllocationID: id.AllocationID, LeaseID: id.LeaseID, Generation: id.DispatchGeneration, FencingTokenDigest: id.FencingTokenDigest, FrozenInputsDigest: attemptTestDigest("frozen-inputs"), ExpectedAttemptSequence: provisioned.Revision}
		request := allocationcontrol.ExistingWorktreeBindRequestV1{Binding: binding, WorktreePath: "/private/tmp/marshal-mixed-1", ExpectedWorktreeIdentity: observation.TargetCurrentName.ObjectIdentity, ExpectedBaseSHA: observation.Git.HeadSHA, RunDirectoryIdentity: existingWorktreeAuthorityTestObject("30", allocationcontrol.ObjectTypeDirectory, 0o040700, 2, 0), RunAuthorityHeadDigest: id.RunAuthorityDigest}
		if err := request.Seal(); err != nil {
			t.Fatal(err)
		}
		bindingDigest, _ := request.Binding.Digest()
		intent := allocationcontrol.ExistingWorktreeBindIntentV1{Request: request, Observation: observation, BindingDigest: bindingDigest, PredecessorRB1HeadDigest: provisioned.HeadDigest}
		if err := intent.Seal(); err != nil {
			t.Fatal(err)
		}
		if _, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactBindIntent, intent); !errors.Is(err, ErrExistingWorktreeAuthorityConflict) {
			t.Fatalf("provision-then-bind intent err=%v, want ErrExistingWorktreeAuthorityConflict", err)
		}
	})

	// Order 2: existing-worktree bind (B) first, then AllocationProvision (A).
	t.Run("bindThenProvision", func(t *testing.T) {
		store, err := OpenResultIngressStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		id := attemptTestIdentity()
		_, _, bound := existingWorktreeBridgeFixture(t, store, id)
		chain := appendExistingWorktreeBindChain(t, store, id, bound)
		// A provision intent must reject because path B is already established.
		authorityPort := allocationTestAuthority(t, store, chain.state.Identity, true, false)
		generic, typed := allocationTestProvisionIntent(t, chain.state, "provision-mixed-"+id.AttemptID)
		if _, err := authorityPort.CompareAndAppendAllocationProvisionIntent(context.Background(), chain.state.Identity, generic, typed); !errors.Is(err, ErrEffectAuthorityOrder) {
			t.Fatalf("bind-then-provision intent err=%v, want ErrEffectAuthorityOrder", err)
		}
		// Launch must still succeed via path B (the rejected provision did not
		// establish a mixed state).
		launch, err := appendAuthorizedAttempt(t, store, chain.state.Revision, chain.state.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "mixed-bind-launch", LaunchClosure: attemptTestClosure(t)})
		if err != nil || !launch.Appended {
			t.Fatalf("path B launch after rejected provision: %+v err=%v", launch, err)
		}
	})
}

func TestExistingWorktreeReleaseBeforeLaunchRejects(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	_, _, bound := existingWorktreeBridgeFixture(t, store, id)
	chain := appendExistingWorktreeBindChain(t, store, id, bound)
	released := appendExistingWorktreeReleaseChain(t, store, id, chain)
	if released.ExistingWorktreeReleaseReceiptFactDigest == "" {
		t.Fatalf("release receipt not projected: %+v", released)
	}
	// Launch must reject: the binding is released.
	_, err = appendAuthorizedAttempt(t, store, released.Revision, released.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "released-launch", LaunchClosure: attemptTestClosure(t)})
	if !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("released launch err=%v, want ErrAttemptAuthorityOrder", err)
	}
	after, _, _ := store.AttemptState(id)
	if after.LaunchState != LaunchNotAuthorized {
		t.Fatalf("released binding authorized launch: %+v", after)
	}
}

func TestExistingWorktreeBridgeReplayPreservesExactDigest(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	owner, verifier, bound := existingWorktreeBridgeFixture(t, store, id)
	chain := appendExistingWorktreeBindChain(t, store, id, bound)
	closure := preparedTestPiClosure(t)
	launch, err := appendAuthorizedAttempt(t, store, chain.state.Revision, chain.state.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "replay-launch", LaunchClosure: closure})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.CreatePreparedExecution(context.Background(), verifier, owner.Acquisition, PreparedExecutionCreation{Identity: id, ExpectedRunSequence: 2, ExpectedRunAuthorityHead: id.RunAuthorityDigest})
	if err != nil {
		t.Fatal(err)
	}
	ledgerBefore, err := os.ReadFile(store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	// Restart: replay must preserve every exact digest byte-for-byte.
	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	recovered, found, err := reopened.AttemptState(id)
	if err != nil || !found {
		t.Fatalf("recover attempt: found=%v err=%v", found, err)
	}
	if recovered.HeadDigest != launch.State.HeadDigest || recovered.LaunchAuthorizedDigest != launch.State.LaunchAuthorizedDigest || recovered.ExistingWorktreeBindReceiptFactDigest != chain.receiptFactDigest || recovered.ExistingWorktreeBindReceiptDigest != chain.receipt.ReceiptDigest || recovered.ExistingWorktreeReleaseReceiptFactDigest != "" {
		t.Fatalf("replay digest drift: recovered=%+v", recovered)
	}
	ledgerAfter, err := os.ReadFile(reopened.ledgerPath())
	if err != nil || !bytes.Equal(ledgerBefore, ledgerAfter) {
		t.Fatal("restart changed the shared Attempt ledger bytes")
	}
	resolved, err := reopened.ResolvePreparedExecution(context.Background(), verifier, owner.Acquisition, prepared.PreparationDigest)
	if err != nil || resolved != prepared {
		t.Fatalf("resolve after restart: resolved=%+v err=%v", resolved, err)
	}
}

func TestExistingWorktreeBridgeResponseLossConvergesSameBytes(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	_, _, bound := existingWorktreeBridgeFixture(t, store, id)
	// Build the canonical bind-receipt payload once, then simulate response
	// loss by replaying the exact same append against the recovered ledger.
	_, observation := existingWorktreeAuthorityTestBind(t, id, bound)
	namespace, _ := id.AuthorityNamespaceID.Digest()
	binding := allocationcontrol.ExistingWorktreeBindingV1{AuthorityNamespaceID: namespace, RepositoryOwnerDigest: attemptTestDigest("repository-owner"), TaskID: id.TaskID, RunID: id.RunID, AttemptID: id.AttemptID, ReservationFactDigest: bound.ReservationFactDigest, AttemptOpenedFactDigest: bound.OpenedDigest, AllocationID: id.AllocationID, LeaseID: id.LeaseID, Generation: id.DispatchGeneration, FencingTokenDigest: id.FencingTokenDigest, FrozenInputsDigest: attemptTestDigest("frozen-inputs"), ExpectedAttemptSequence: bound.Revision}
	request := allocationcontrol.ExistingWorktreeBindRequestV1{Binding: binding, WorktreePath: "/private/tmp/marshal-response-loss", ExpectedWorktreeIdentity: observation.TargetCurrentName.ObjectIdentity, ExpectedBaseSHA: observation.Git.HeadSHA, RunDirectoryIdentity: existingWorktreeAuthorityTestObject("30", allocationcontrol.ObjectTypeDirectory, 0o040700, 2, 0), RunAuthorityHeadDigest: id.RunAuthorityDigest}
	if err := request.Seal(); err != nil {
		t.Fatal(err)
	}
	bindingDigest, _ := request.Binding.Digest()
	intent := allocationcontrol.ExistingWorktreeBindIntentV1{Request: request, Observation: observation, BindingDigest: bindingDigest, PredecessorRB1HeadDigest: bound.HeadDigest}
	if err := intent.Seal(); err != nil {
		t.Fatal(err)
	}
	intentSnapshot, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactBindIntent, intent)
	if err != nil {
		t.Fatal(err)
	}
	receipt := allocationcontrol.ExistingWorktreeBindReceiptV1{Binding: request.Binding, RequestDigest: request.RequestDigest, IntentFactDigest: intentSnapshot.Facts[len(intentSnapshot.Facts)-1].AttemptFactDigest, Observation: observation, BindingDigest: bindingDigest, PredecessorRB1HeadDigest: intentSnapshot.CurrentAttemptHeadDigest, Disposition: allocationcontrol.DispositionApplied}
	if err := receipt.Seal(); err != nil {
		t.Fatal(err)
	}
	firstSnapshot, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactBindReceipt, receipt)
	if err != nil {
		t.Fatalf("first bind-receipt append: %v", err)
	}
	ledgerAfterFirst, err := os.ReadFile(store.ledgerPath())
	if err != nil {
		t.Fatal(err)
	}
	// Simulate response loss: the caller retries the exact same canonical
	// bind-receipt. The store must converge to the same snapshot and not
	// append a duplicate line.
	replaySnapshot, err := store.appendExistingWorktreeFact(id, allocationcontrol.ExistingWorktreeFactBindReceipt, receipt)
	if err != nil {
		t.Fatalf("response-loss replay err=%v", err)
	}
	if !reflect.DeepEqual(firstSnapshot, replaySnapshot) {
		t.Fatalf("response-loss replay did not converge: first=%+v replay=%+v", firstSnapshot, replaySnapshot)
	}
	ledgerAfterReplay, err := os.ReadFile(store.ledgerPath())
	if err != nil || !bytes.Equal(ledgerAfterFirst, ledgerAfterReplay) {
		t.Fatal("response-loss replay appended a duplicate line")
	}
}

func TestLegacyProvisionStillEnablesLaunch(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	// Path A: legacy/provider AllocationProvision applied receipt still
	// satisfies allocation authority without any existing-worktree binding.
	provisioned := appendTestAcceptedProvision(t, store, opened.State)
	if provisioned.AllocationProvisionEffectDigest == "" || provisioned.AllocationProvisionReceiptDigest == "" {
		t.Fatalf("provision not established: %+v", provisioned)
	}
	launch, err := appendAuthorizedAttempt(t, store, provisioned.Revision, provisioned.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "legacy-launch", LaunchClosure: attemptTestClosure(t)})
	if err != nil || !launch.Appended || launch.State.LaunchState != LaunchUncertain {
		t.Fatalf("legacy provision launch: %+v err=%v", launch, err)
	}
	if launch.State.ExistingWorktreeBindReceiptFactDigest != "" {
		t.Fatalf("legacy launch synthesized a worktree receipt: %+v", launch.State)
	}
}

// suppress unused import warnings for canonical/launchidentity when the test
// build constraints trim helper references.
var (
	_ = canonical.DigestBytes
	_ = launchidentity.Pi0844DarwinARM64Profile
)
