package productionruntime

import (
	"context"
	"testing"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

const runtimeTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const runtimeSuccessDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type testOwnerLock struct {
	want       resultingress.ControlOwnerAcquisition
	isClaimed  bool
	closed     bool
	inCritical bool
	sections   int
}

func (lock *testOwnerLock) WithCurrentOwnerLock(_ context.Context, acquisition resultingress.ControlOwnerAcquisition, fn func() error) error {
	if lock.closed || !lock.isClaimed || lock.inCritical || acquisition != lock.want {
		return application.NewError("test-owner", application.ReasonOwnerNotCurrent)
	}
	lock.sections++
	lock.inCritical = true
	defer func() { lock.inCritical = false }()
	return fn()
}

func (lock *testOwnerLock) Close() error { lock.closed = true; return nil }
func (lock *testOwnerLock) acquisition() resultingress.ControlOwnerAcquisition {
	return lock.want
}
func (lock *testOwnerLock) identity() ownerLockIdentity {
	return ownerLockIdentity{Device: 1, Inode: 2}
}
func (lock *testOwnerLock) claimRuntime() error {
	if lock.closed || lock.isClaimed {
		return application.NewError("test-owner", application.ReasonOwnerUnavailable)
	}
	lock.isClaimed = true
	return nil
}
func (lock *testOwnerLock) claimed() bool { return lock.isClaimed && !lock.closed }

type testAuthority struct {
	lock         *testOwnerLock
	owner        OwnerProjection
	prepared     application.PreparedRunStart
	outcome      application.RunProjection
	outcomeFound bool
	projection   application.RunProjection
	err          error
	prepareCalls int
}

func (authority *testAuthority) requireLock() error {
	if authority.lock == nil || !authority.lock.inCritical {
		return application.NewError("test-authority", application.ReasonOwnerNotCurrent)
	}
	return nil
}

func (authority *testAuthority) CurrentOwner(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition) (OwnerProjection, error) {
	if err := authority.requireLock(); err != nil {
		return OwnerProjection{}, err
	}
	return authority.owner, authority.err
}

func (authority *testAuthority) PrepareRunStart(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition, application.PrepareRunStartRequest) (application.PreparedRunStart, error) {
	if err := authority.requireLock(); err != nil {
		return application.PreparedRunStart{}, err
	}
	authority.prepareCalls++
	return authority.prepared, authority.err
}

func (authority *testAuthority) RehydratePreparedRunStart(_ context.Context, _ resultingress.CurrentOwnerLockVerifier, _ resultingress.ControlOwnerAcquisition, digest string) (application.PreparedRunStart, error) {
	if err := authority.requireLock(); err != nil || digest != authority.prepared.PreparationDigest {
		return application.PreparedRunStart{}, application.NewError("test-authority", application.ReasonAuthorityConflict)
	}
	return authority.prepared, authority.err
}

func (authority *testAuthority) RehydrateRunStartOutcome(_ context.Context, _ resultingress.CurrentOwnerLockVerifier, _ resultingress.ControlOwnerAcquisition, digest string) (application.RunProjection, bool, error) {
	if err := authority.requireLock(); err != nil || digest != authority.prepared.PreparationDigest {
		return application.RunProjection{}, false, application.NewError("test-authority", application.ReasonAuthorityConflict)
	}
	return authority.outcome, authority.outcomeFound, authority.err
}

func (authority *testAuthority) InspectRun(context.Context, resultingress.CurrentOwnerLockVerifier, resultingress.ControlOwnerAcquisition, application.InspectRunRequest) (application.RunProjection, error) {
	if err := authority.requireLock(); err != nil {
		return application.RunProjection{}, err
	}
	return authority.projection, authority.err
}

type testBridge struct {
	lock        *testOwnerLock
	configured  PiProfile
	verifyCalls int
	startCalls  int
	startErr    error
	afterStart  func()
}

func (bridge *testBridge) VerifyAgentProfile(_ context.Context, _ resultingress.CurrentOwnerLockVerifier, _ resultingress.ControlOwnerAcquisition, owner OwnerProjection, expected PiProfile) error {
	bridge.verifyCalls++
	if bridge.lock == nil || !bridge.lock.inCritical || owner.PendingRecovery != 0 || expected != bridge.configured {
		return application.NewError("test-bridge", application.ReasonBridgeUnavailable)
	}
	return nil
}

func (bridge *testBridge) StartPreparedRun(_ context.Context, _ resultingress.CurrentOwnerLockVerifier, _ resultingress.ControlOwnerAcquisition, owner OwnerProjection, expected PiProfile, _ application.PreparedRunStart) error {
	bridge.startCalls++
	if bridge.lock == nil || !bridge.lock.inCritical || owner.PendingRecovery != 0 || expected != bridge.configured {
		return application.NewError("test-bridge", application.ReasonBridgeUnavailable)
	}
	if bridge.afterStart != nil {
		bridge.afterStart()
	}
	return bridge.startErr
}

func testAcquisition() resultingress.ControlOwnerAcquisition {
	return resultingress.ControlOwnerAcquisition{
		Scope: resultingress.ControlOwnerScope{
			AuthorityNamespaceID:     authority.AuthorityNamespaceId{TenantNamespace: "tenant", ControlPlaneId: "control", AuthorityScopeId: "repository"},
			RepositoryIdentityDigest: runtimeTestDigest,
		},
		OwnerEpoch:       1,
		OwnerUID:         501,
		OwnerGID:         20,
		OwnerProcess:     processsupervisor.ProcessIdentity{PID: 100, BirthSeconds: 1_700_000_000, BirthMicroseconds: 1, SessionID: 100, ProcessGroupID: 100},
		OwnerBinary:      processsupervisor.BinaryIdentity{CanonicalPath: "/fixed/bin/marshal", Device: 1, Inode: 2, FileType: "regular", UID: 501, GID: 20, Mode: 0o100755, LinkCount: 1, Size: 100, RawSHA256: runtimeTestDigest, CDHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", SourceHead: "cccccccccccccccccccccccccccccccccccccccc", SelfProfile: DarwinLocalDogfoodProfile},
		ObserverIdentity: "darwin-owner-observer/v1",
		ObservedAt:       "2026-08-28T00:00:00Z",
	}
}

func testPrepared() application.PreparedRunStart {
	return application.PreparedRunStart{ProtocolRevision: application.ProtocolRevision, TaskID: "task-1", RunID: "run-1", AttemptID: "attempt-1", State: domain.StateReady, Sequence: 3, AuthorityHead: runtimeTestDigest, PreparationDigest: runtimeTestDigest}
}

func testSuccessor() application.RunProjection {
	prepared := testPrepared()
	return application.RunProjection{TaskID: prepared.TaskID, RunID: prepared.RunID, AttemptID: prepared.AttemptID, State: domain.StateRunning, Sequence: prepared.Sequence + 1, AuthorityHead: runtimeSuccessDigest}
}

func testComponents(t *testing.T) (*controller, *testOwnerLock, *testAuthority, *testBridge, PiProfile) {
	t.Helper()
	acquisition := testAcquisition()
	lock := &testOwnerLock{want: acquisition}
	profile, err := NewPi0843Profile("/fixed/bin/pi", "/fixed/bin/node", runtimeTestDigest)
	if err != nil {
		t.Fatal(err)
	}
	authority := &testAuthority{lock: lock, owner: OwnerProjection{OwnerEpoch: 1, OwnerFactDigest: runtimeTestDigest}, prepared: testPrepared(), projection: application.RunProjection{TaskID: "task-1", RunID: "run-1", AttemptID: "attempt-1", State: domain.StateReady, Sequence: 3, AuthorityHead: runtimeTestDigest}}
	bridge := &testBridge{lock: lock, configured: profile}
	controller, err := newController(authority, bridge, lock, acquisition, profile)
	if err != nil {
		t.Fatal(err)
	}
	return controller, lock, authority, bridge, profile
}

func claimTestRuntime(t *testing.T, controller *controller) *Runtime {
	t.Helper()
	runtime, err := newRuntime(controller)
	if application.HasReason(err, application.ReasonPlatformProfileUnavailable) {
		t.Skip("darwin/arm64-only runtime")
	}
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func TestControllerCannotMutateBeforeRuntimeClaim(t *testing.T) {
	controller, lock, authority, bridge, _ := testComponents(t)
	_, err := controller.prepareRunStart(context.Background(), application.PrepareRunStartRequest{RunID: "run-1", ExpectedSequence: 3, ExpectedAuthorityHead: runtimeTestDigest})
	if !application.HasReason(err, application.ReasonOwnerUnavailable) || lock.sections != 0 || authority.prepareCalls != 0 || bridge.verifyCalls != 0 {
		t.Fatalf("unclaimed err=%v sections=%d prepares=%d verifies=%d", err, lock.sections, authority.prepareCalls, bridge.verifyCalls)
	}
}

func TestStartUsesOneCriticalSectionAndRejectsPreparedForgery(t *testing.T) {
	controller, lock, _, bridge, _ := testComponents(t)
	runtime := claimTestRuntime(t, controller)
	defer runtime.Close()
	forged := testPrepared()
	forged.AuthorityHead = runtimeSuccessDigest
	_, err := runtime.StartPreparedRun(context.Background(), forged)
	if !application.HasReason(err, application.ReasonAuthorityConflict) || lock.sections != 1 || bridge.startCalls != 0 {
		t.Fatalf("forgery err=%v sections=%d starts=%d", err, lock.sections, bridge.startCalls)
	}
}

func TestStartReplayReturnsDurableSuccessWithoutRespawn(t *testing.T) {
	controller, lock, authority, bridge, _ := testComponents(t)
	authority.outcome, authority.outcomeFound = testSuccessor(), true
	bridge.configured, _ = NewPi0843Profile("/fixed/bin/replaced-pi", "/fixed/bin/node", runtimeTestDigest)
	runtime := claimTestRuntime(t, controller)
	defer runtime.Close()
	got, err := runtime.StartPreparedRun(context.Background(), testPrepared())
	if err != nil || got != testSuccessor() || bridge.verifyCalls != 0 || bridge.startCalls != 0 || lock.sections != 1 {
		t.Fatalf("got=%#v err=%v verifies=%d starts=%d sections=%d", got, err, bridge.verifyCalls, bridge.startCalls, lock.sections)
	}
}

func TestLostBridgeResponseReturnsNewDurableOutcome(t *testing.T) {
	controller, lock, authority, bridge, _ := testComponents(t)
	bridge.startErr = application.NewError("test-bridge", application.ReasonBridgeUnavailable)
	bridge.afterStart = func() { authority.outcome, authority.outcomeFound = testSuccessor(), true }
	runtime := claimTestRuntime(t, controller)
	defer runtime.Close()
	got, err := runtime.StartPreparedRun(context.Background(), testPrepared())
	if err != nil || got != testSuccessor() || bridge.startCalls != 1 || lock.sections != 1 {
		t.Fatalf("got=%#v err=%v starts=%d sections=%d", got, err, bridge.startCalls, lock.sections)
	}
}

func TestConfiguredProfileDriftFailsInsideCriticalSection(t *testing.T) {
	controller, lock, _, bridge, _ := testComponents(t)
	bridge.configured, _ = NewPi0843Profile("/fixed/bin/other-pi", "/fixed/bin/node", runtimeTestDigest)
	runtime := claimTestRuntime(t, controller)
	defer runtime.Close()
	_, err := runtime.StartPreparedRun(context.Background(), testPrepared())
	if !application.HasReason(err, application.ReasonBridgeUnavailable) || bridge.startCalls != 0 || lock.sections != 1 {
		t.Fatalf("profile drift err=%v starts=%d sections=%d", err, bridge.startCalls, lock.sections)
	}
}

func TestRecoveryBlocksMutationAndFoundationNeverReportsReady(t *testing.T) {
	controller, _, authority, bridge, _ := testComponents(t)
	authority.owner.PendingRecovery = 2
	runtime := claimTestRuntime(t, controller)
	defer runtime.Close()
	_, err := runtime.PrepareRunStart(context.Background(), application.PrepareRunStartRequest{RunID: "run-1", ExpectedSequence: 3, ExpectedAuthorityHead: runtimeTestDigest})
	if !application.HasReason(err, application.ReasonRecoveryRequired) || bridge.verifyCalls != 0 {
		t.Fatalf("pending recovery err=%v verifies=%d", err, bridge.verifyCalls)
	}
	status, err := runtime.Status(context.Background(), application.StatusRequest{})
	if err != nil || status.Availability != application.AvailabilityRecoveryRequired || status.PendingRecovery != 2 {
		t.Fatalf("recovery status=%#v err=%v", status, err)
	}
	authority.owner.PendingRecovery = 0
	status, err = runtime.Status(context.Background(), application.StatusRequest{})
	if err != nil || status.Availability != application.AvailabilityUnavailable || status.ReasonCode != application.ReasonCompositionIncomplete {
		t.Fatalf("foundation status=%#v err=%v", status, err)
	}
}

func TestRuntimeRejectsSecondCompositionForSameOwnerLock(t *testing.T) {
	controller, _, _, _, _ := testComponents(t)
	first := claimTestRuntime(t, controller)
	defer first.Close()
	if _, err := newRuntime(controller); !application.HasReason(err, application.ReasonOwnerUnavailable) {
		t.Fatalf("second runtime err=%v", err)
	}
}

func TestPiProfileRejectsAnythingExceptExactClosedInputs(t *testing.T) {
	if _, err := NewPi0843Profile("pi", "/fixed/bin/node", runtimeTestDigest); !application.HasReason(err, application.ReasonInvalidRequest) {
		t.Fatalf("relative executable err=%v", err)
	}
	if _, err := NewPi0843Profile("/fixed/bin/pi", "/fixed/bin/node", "sha256:not-a-digest"); !application.HasReason(err, application.ReasonInvalidRequest) {
		t.Fatalf("forged digest err=%v", err)
	}
}
