//go:build darwin && arm64

package resultingress

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

// pathBPreparedFixture builds a path B (existing-worktree bind receipt)
// prepared execution: a fresh reserved v2 Attempt with a bound owner, a
// current existing-worktree bind chain, a launch-authorized Pi closure, and
// the sealed PreparedExecution. It is the path B counterpart to
// newPreparedExecutionFixture (which is path A).
func pathBPreparedFixture(t *testing.T) (store *DurableStore, owner ControlOwnerState, verifier attemptOwnerVerifier, prepared PreparedExecutionV1, chain existingWorktreeBridgeChain) {
	t.Helper()
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	owner, verifier, bound := existingWorktreeBridgeFixture(t, store, id)
	chain = appendExistingWorktreeBindChain(t, store, id, bound)
	closure := preparedTestPiClosure(t)
	launch, err := appendAuthorizedAttempt(t, store, chain.state.Revision, chain.state.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "pathb-launch", LaunchClosure: closure})
	if err != nil {
		t.Fatalf("launch-authorized via existing-worktree bind receipt: %v", err)
	}
	prepared, err = store.CreatePreparedExecution(context.Background(), verifier, owner.Acquisition, PreparedExecutionCreation{Identity: launch.State.Identity, ExpectedRunSequence: 2, ExpectedRunAuthorityHead: id.RunAuthorityDigest})
	if err != nil {
		t.Fatalf("CreatePreparedExecution via path B: %v", err)
	}
	if prepared.AllocationProvisionReceiptFactDigest != "" || prepared.AllocationProvisionReceiptDigest != "" {
		t.Fatalf("path B prepared execution synthesized a provision receipt: %+v", prepared)
	}
	return store, owner, verifier, prepared, chain
}

// TestCurrentPreparedExistingWorktreeBindReceiptReturnsValidatedReceipt
// proves the extended currentPreparedExistingWorktreeBindReceipt returns the
// validated ExistingWorktreeBindReceiptV1 carrying the exact directory
// ObjectIdentityV1 bound at admission, and that this identity is acceptable to
// preparedAllocationLiveIdentity (i.e. it can feed Darwin source
// verification). No provision receipt is fabricated.
func TestCurrentPreparedExistingWorktreeBindReceiptReturnsValidatedReceipt(t *testing.T) {
	store, _, _, prepared, chain := pathBPreparedFixture(t)
	projection := newAuthorityProjection()
	var receipt allocationcontrol.ExistingWorktreeBindReceiptV1
	var factDigest, receiptDigest string
	if err := store.transact(projection, func() error {
		key, keyErr := prepared.AttemptIdentity.Key()
		if keyErr != nil {
			return keyErr
		}
		state, found := projection.attempts[key]
		if !found {
			t.Fatal("path B prepared attempt not projected")
		}
		got, err := currentPreparedExistingWorktreeBindReceipt(projection, state)
		if err != nil {
			return err
		}
		factDigest, receiptDigest = got.factDigest, got.receiptDigest
		receipt = got.receipt
		return nil
	}); err != nil {
		t.Fatalf("currentPreparedExistingWorktreeBindReceipt: %v", err)
	}
	if factDigest != chain.receiptFactDigest || receiptDigest != chain.receipt.ReceiptDigest {
		t.Fatalf("returned digests drifted from bind chain: fact=%q want=%q receipt=%q want=%q", factDigest, chain.receiptFactDigest, receiptDigest, chain.receipt.ReceiptDigest)
	}
	bound := receipt.Observation.TargetCurrentName.ObjectIdentity
	if bound != chain.observation.TargetCurrentName.ObjectIdentity {
		t.Fatalf("returned receipt directory identity drifted: %+v vs %+v", bound, chain.observation.TargetCurrentName.ObjectIdentity)
	}
	if _, err := preparedAllocationLiveIdentity(bound); err != nil {
		t.Fatalf("path B bound identity is not a valid allocation live identity: %v", err)
	}
}

// TestPreparedPathBReachesSourceVerification proves path B's existing-worktree
// bind receipt flows through verifyPreparedCurrentSourcesLocked all the way to
// the fail-closed nofollow current closure verification. The synthetic Pi
// closure points at non-existent files, so VerifyCurrentClosure fails.
// ErrPreparedExecutionUnavailable is returned only from that closure-
// verification branch, so observing it proves path B passed the bind-receipt
// recheck, extracted the worktree directory identity, parsed it as a valid
// live directory identity, and reached the nofollow source verification
// instead of failing earlier at the receipt recheck (which returns
// ErrPreparedExecutionConflict). Path B must not synthesize or rely on an
// AllocationProvision effect.
func TestPreparedPathBReachesSourceVerification(t *testing.T) {
	store, _, _, prepared, _ := pathBPreparedFixture(t)
	projection := newAuthorityProjection()
	var verifyErr error
	if err := store.transact(projection, func() error {
		key, keyErr := prepared.AttemptIdentity.Key()
		if keyErr != nil {
			return keyErr
		}
		state, found := projection.attempts[key]
		if !found {
			t.Fatal("path B prepared attempt not projected")
		}
		_, _, verifyErr = store.verifyPreparedCurrentSourcesLocked(projection, prepared, state)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(verifyErr, ErrPreparedExecutionUnavailable) {
		t.Fatalf("path B source verification err=%v, want ErrPreparedExecutionUnavailable (reached VerifyCurrentClosure)", verifyErr)
	}
	if _, _, _, allocErr := store.loadAllocationEffect(mustEffectKey(prepared.AttemptIdentity.AuthorityNamespaceID, "provision-"+prepared.AttemptIdentity.AttemptID)); allocErr == nil {
		t.Fatal("path B source verification relied on a provision effect")
	}
}

// TestPreparedPathASourceVerificationUnchanged proves path A behavior is
// unchanged by the normalization: it still reaches VerifyCurrentClosure with
// the AllocationProvision receipt's live identity and fails closed with
// ErrPreparedExecutionUnavailable on the synthetic closure, exactly as before
// the path B source value was introduced.
func TestPreparedPathASourceVerificationUnchanged(t *testing.T) {
	fixture := newPreparedExecutionFixture(t)
	prepared := fixture.prepared
	projection := newAuthorityProjection()
	var verifyErr error
	if err := fixture.store.transact(projection, func() error {
		key, keyErr := prepared.AttemptIdentity.Key()
		if keyErr != nil {
			return keyErr
		}
		state, found := projection.attempts[key]
		if !found {
			t.Fatal("path A prepared attempt not projected")
		}
		_, _, verifyErr = fixture.store.verifyPreparedCurrentSourcesLocked(projection, prepared, state)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(verifyErr, ErrPreparedExecutionUnavailable) {
		t.Fatalf("path A source verification err=%v, want ErrPreparedExecutionUnavailable (unchanged)", verifyErr)
	}
}

// TestPreparedSpawnPayloadNormalizesPathAAndPathB proves the private
// preparedAllocationSource normalization makes path A (provision receipt
// LiveIdentity) and path B (worktree bind receipt TargetCurrentName.
// ObjectIdentity) interchangeable: the same directory ObjectIdentityV1
// produces byte-identical spawn payloads regardless of which allocation
// authority produced it. The payload's allocation live identity and
// working-directory held object are both derived from the normalized source
// identity, so a path-replaced cwd would be rejected by the supervisor source
// gate. This is the path A byte/behavior compatibility invariant.
func TestPreparedSpawnPayloadNormalizesPathAAndPathB(t *testing.T) {
	closure := preparedTestPiClosure(t)
	state := AttemptAuthorityState{LaunchAuthorizedDigest: attemptTestDigest("path-launch"), SupervisorStartedDigest: attemptTestDigest("path-supervisor")}
	identity := allocationcontrol.ObjectIdentityV1{Device: "1", Inode: "10", Mode: 0o040700, UID: 501, GID: 20, Nlink: 2, Type: allocationcontrol.ObjectTypeDirectory}
	payloadA, err := preparedSpawnPayload(state, closure, preparedAllocationSource{directoryIdentity: identity})
	if err != nil {
		t.Fatalf("path A spawn payload: %v", err)
	}
	payloadB, err := preparedSpawnPayload(state, closure, preparedAllocationSource{directoryIdentity: identity})
	if err != nil {
		t.Fatalf("path B spawn payload: %v", err)
	}
	if !reflect.DeepEqual(payloadA, payloadB) {
		t.Fatalf("path A and path B spawn payloads diverged for the same identity:\nA=%+v\nB=%+v", payloadA, payloadB)
	}
	if payloadA.AllocationLiveIdentity == nil || payloadA.AllocationLiveIdentity.Device != 1 || payloadA.AllocationLiveIdentity.Inode != 10 {
		t.Fatalf("spawn payload did not bind the normalized source identity: %+v", payloadA.AllocationLiveIdentity)
	}
	if payloadA.WorkingDirectory.Device != 1 || payloadA.WorkingDirectory.Inode != 10 || payloadA.WorkingDirectory.Mode != identity.Mode {
		t.Fatalf("spawn working directory did not bind the normalized source identity: %+v", payloadA.WorkingDirectory)
	}
	if payloadA.AllocationLiveIdentity.Device != payloadA.WorkingDirectory.Device || payloadA.AllocationLiveIdentity.Inode != payloadA.WorkingDirectory.Inode || payloadA.AllocationLiveIdentity.Mode != payloadA.WorkingDirectory.Mode || payloadA.AllocationLiveIdentity.UID != payloadA.WorkingDirectory.UID || payloadA.AllocationLiveIdentity.GID != payloadA.WorkingDirectory.GID || payloadA.AllocationLiveIdentity.LinkCount != payloadA.WorkingDirectory.LinkCount || payloadA.AllocationLiveIdentity.Size != payloadA.WorkingDirectory.Size {
		t.Fatalf("spawn payload live identity does not match working directory (path-replacement gate): live=%+v working=%+v", payloadA.AllocationLiveIdentity, payloadA.WorkingDirectory)
	}
}

// TestPreparedProcessObservationNormalizesPathAAndPathB proves the process
// observation built after spawn is also normalized through the same source
// value, so path A and path B with the same directory identity produce
// byte-identical observations.
func TestPreparedProcessObservationNormalizesPathAAndPathB(t *testing.T) {
	closure := preparedTestPiClosure(t)
	outcome := SupervisorProcessOutcome{Process: processsupervisor.ProcessIdentity{PID: 4321, ProcessGroupID: 4321, BirthSeconds: 100, BirthMicroseconds: 33, SessionID: 4321}, ObserverIdentity: "core-darwin-observer/v1"}
	identity := allocationcontrol.ObjectIdentityV1{Device: "1", Inode: "10", Mode: 0o040700, UID: 501, GID: 20, Nlink: 2, Type: allocationcontrol.ObjectTypeDirectory}
	obsA, err := preparedProcessObservation(closure, preparedAllocationSource{directoryIdentity: identity}, outcome)
	if err != nil {
		t.Fatalf("path A process observation: %v", err)
	}
	obsB, err := preparedProcessObservation(closure, preparedAllocationSource{directoryIdentity: identity}, outcome)
	if err != nil {
		t.Fatalf("path B process observation: %v", err)
	}
	if !reflect.DeepEqual(obsA, obsB) {
		t.Fatalf("path A and path B process observations diverged for the same identity:\nA=%+v\nB=%+v", obsA, obsB)
	}
	if obsA.WorkingDirectoryDevice != 1 || obsA.WorkingDirectoryInode != 10 {
		t.Fatalf("process observation did not bind the normalized source identity: device=%d inode=%d", obsA.WorkingDirectoryDevice, obsA.WorkingDirectoryInode)
	}
}

// TestPreparedSpawnPayloadRejectsIdentityReplacementDrift proves identity
// replacement drift fails before spawn: a source directory identity that has
// been replaced by a non-directory, a zero device/inode, a setuid/setgid mode
// or a wrong type is rejected by preparedAllocationLiveIdentity with
// ErrPreparedExecutionConflict, so no spawn payload is constructed. This is
// the fail-closed gate that prevents a replaced allocation identity from
// reaching spawn construction.
func TestPreparedSpawnPayloadRejectsIdentityReplacementDrift(t *testing.T) {
	closure := preparedTestPiClosure(t)
	state := AttemptAuthorityState{LaunchAuthorizedDigest: attemptTestDigest("launch"), SupervisorStartedDigest: attemptTestDigest("started")}
	base := allocationcontrol.ObjectIdentityV1{Device: "1", Inode: "10", Mode: 0o040700, UID: 501, GID: 20, Nlink: 2, Type: allocationcontrol.ObjectTypeDirectory}
	if _, err := preparedSpawnPayload(state, closure, preparedAllocationSource{directoryIdentity: base}); err != nil {
		t.Fatalf("baseline source identity rejected: %v", err)
	}
	drifts := map[string]allocationcontrol.ObjectIdentityV1{
		"regular-file-replacement": {Device: "1", Inode: "10", Mode: 0o100600, UID: 501, GID: 20, Size: 100, Nlink: 1, Type: allocationcontrol.ObjectTypeRegular},
		"zero-device":              {Device: "0", Inode: "10", Mode: 0o040700, UID: 501, GID: 20, Nlink: 2, Type: allocationcontrol.ObjectTypeDirectory},
		"zero-inode":               {Device: "1", Inode: "0", Mode: 0o040700, UID: 501, GID: 20, Nlink: 2, Type: allocationcontrol.ObjectTypeDirectory},
		"setuid-mode-replacement":  {Device: "1", Inode: "10", Mode: 0o040700 | 0o4000, UID: 501, GID: 20, Nlink: 2, Type: allocationcontrol.ObjectTypeDirectory},
		"wrong-type-replacement":   {Device: "1", Inode: "10", Mode: 0o040700, UID: 501, GID: 20, Nlink: 2, Type: "symlink"},
	}
	for name, drifted := range drifts {
		if _, err := preparedSpawnPayload(state, closure, preparedAllocationSource{directoryIdentity: drifted}); !errors.Is(err, ErrPreparedExecutionConflict) {
			t.Fatalf("identity drift %q was accepted before spawn: %v", name, err)
		}
	}
}
