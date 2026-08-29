package resultingress

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
)

func attemptTestDigest(seed string) string { return canonical.DigestBytes([]byte(seed)) }

func attemptTestIdentity() AttemptIdentity {
	return AttemptIdentity{
		AuthorityNamespaceID:  authority.AuthorityNamespaceId{TenantNamespace: "tenant-1", ControlPlaneId: "core-1", AuthorityScopeId: "scope-1"},
		AuthorityNamespaceRef: "authority:test", TaskID: "task-1", RunID: "run-1",
		AttemptID: "attempt-1", AllocationID: "allocation-1", LeaseID: "lease-1",
		LeaseDigest: attemptTestDigest("lease"), DispatchGeneration: 7,
		FencingTokenDigest: attemptTestDigest("fencing-token"), OrchestratorID: "orchestrator-1",
		RunAuthorityDigest: attemptTestDigest("run-authority"),
	}
}

func attemptTestProcess(t *testing.T) ProcessObservation {
	t.Helper()
	observation, err := SealProcessObservation(ProcessObservation{
		PID: 1234, PGID: 1234, BirthSeconds: 100, BirthMicroseconds: 22,
		WorkingDirectory: "/tmp/work", WorkingDirectoryDevice: 1, WorkingDirectoryInode: 2,
		WorkingDirectoryType: POSIXFileTypeDirectory, WorkingDirectoryOwner: 501, WorkingDirectoryMode: POSIXFileTypeDirectory | 0755,
		ExecutablePath: "/fixed/marshal", ExecutableDevice: 1, ExecutableInode: 3,
		ExecutableSize: 99, ExecutableType: POSIXFileTypeRegular, ExecutableOwner: 501, ExecutableGroup: 20,
		ExecutableMode: POSIXFileTypeRegular | 0755, ExecutableLinkCount: 1, ExecutableSHA256: attemptTestDigest("executable"),
		ObserverIdentity: "core-darwin-observer/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func attemptTestClosure(t *testing.T) launchidentity.ClosureV1 {
	t.Helper()
	closure, err := launchidentity.Seal(launchidentity.SpecInput{RuntimeExecutable: launchidentity.ObjectV1{CanonicalPath: "/fixed/marshal", Device: 1, Inode: 3, FileType: POSIXFileTypeRegular, Mode: POSIXFileTypeRegular | 0755, UID: 501, GID: 20, Size: 99, LinkCount: 1, RawSHA256: attemptTestDigest("executable")}, ClosureProfileID: launchidentity.NativeProfile, MaterialRoots: []launchidentity.MaterialRootV1{}, LaunchMaterials: []launchidentity.LaunchMaterialV1{}, Arguments: []string{"/fixed/marshal"}, Environment: []string{}, WorkingDirectory: "/tmp/work"})
	if err != nil {
		t.Fatal(err)
	}
	return closure
}

func openFreshStartedAttempt(t *testing.T, store *ingressDurableStore) AttemptAuthorityState {
	t.Helper()
	id := attemptTestIdentity()
	openedResult, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	opened := openedResult.State
	opened = appendTestAcceptedProvision(t, store, opened)
	authorizedResult, err := appendAuthorizedAttempt(t, store, opened.Revision, opened.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-auth-1"})
	if err != nil {
		t.Fatal(err)
	}
	authorized := authorizedResult.State
	control := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/fresh-supervisor-" + id.AttemptID, Device: 2, Inode: 101, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	prepared, acquisition := testPreparedSupervisor(t, store, authorized, "supervisor-"+id.AttemptID+"-1", control)
	authorized = testStartPreparedSupervisor(t, store, prepared, acquisition)
	transition := AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: id, CommandID: "command-1", ObservedAt: "2026-08-28T00:00:02Z", Process: attemptTestProcess(t), LaunchMaterialsDigest: authorized.LaunchMaterialsDigest, AgentLaunchSpecDigest: authorized.AgentLaunchSpecDigest}
	authorized = appendTestProcessStartedCheckpoints(t, store, authorized, &transition)
	if transition.SupervisorBindOutcomeFactDigest == "" || transition.SupervisorOutcomeFactDigest == "" {
		t.Fatal("fresh process-started fixture omitted supervisor command outcome references")
	}
	owner, found, err := store.OpenOwner(authorized.Owner.Scope)
	if err != nil || !found {
		t.Fatalf("fresh process-started fixture current owner found=%v err=%v", found, err)
	}
	run := attemptTestRunAuthority(id)
	startedResult, err := store.AppendProcessStarted(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, authorized.Revision, authorized.HeadDigest, AttemptAuthorizationRequest{Identity: id, CurrentRunAuthority: run}, authorized.Owner, transition)
	if err != nil {
		t.Fatalf("append fresh process-started from supervisor outcomes: %v", err)
	}
	return startedResult.State
}

// openStartedAttempt preserves the pre-ADR0060 historical fixture used by
// broad ResultIngress regression tests. Fresh supervisor-bearing mutation is
// covered by openFreshStartedAttempt and must always use bootstrap plus the
// independent command recovery sub-chain.
func openStartedAttempt(t *testing.T, store *ingressDurableStore) AttemptAuthorityState {
	t.Helper()
	id := attemptTestIdentity()
	opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	provisioned := appendTestAcceptedProvision(t, store, opened.State)
	authorized, err := appendAuthorizedAttempt(t, store, provisioned.Revision, provisioned.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "historical-launch-auth-1"})
	if err != nil {
		t.Fatal(err)
	}
	process := attemptTestProcess(t)
	return appendHistoricalAttemptTransition(t, store, authorized.State, AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: id, CommandID: "historical-command-1", ObservedAt: "2026-08-28T00:00:02Z", Process: process, LaunchMaterialsDigest: authorized.State.LaunchMaterialsDigest, AgentLaunchSpecDigest: authorized.State.AgentLaunchSpecDigest})
}

func attemptTestOwnerScope(id AttemptIdentity) ControlOwnerScope {
	return ControlOwnerScope{AuthorityNamespaceID: id.AuthorityNamespaceID, RepositoryIdentityDigest: attemptTestDigest("repository-identity")}
}

func attemptTestBinary() processsupervisor.BinaryIdentity {
	return processsupervisor.BinaryIdentity{CanonicalPath: "/fixed/marshal", Device: 1, Inode: 41, FileType: "regular", UID: 501, GID: 20, Mode: POSIXFileTypeRegular | 0o755, LinkCount: 1, Size: 4096, RawSHA256: attemptTestDigest("fixed-marshal"), CDHash: strings.Repeat("a", 40), SourceHead: strings.Repeat("b", 40), SelfProfile: "darwin-local-dogfood"}
}

func attemptTestControlFiles(device, firstInode uint64) processsupervisor.SessionControlFiles {
	return processsupervisor.SessionControlFiles{
		Nonce:   processsupervisor.ControlFileIdentity{Device: device, Inode: firstInode, FileType: "regular", UID: 501, GID: 20, Mode: 0o100600, LinkCount: 1},
		Journal: processsupervisor.ControlFileIdentity{Device: device, Inode: firstInode + 1, FileType: "regular", UID: 501, GID: 20, Mode: 0o100600, LinkCount: 1},
	}
}

func appendTestSupervisorStarted(t *testing.T, store *DurableStore, state AttemptAuthorityState) AttemptAuthorityState {
	t.Helper()
	scope := attemptTestOwnerScope(state.Identity)
	prior, found, err := store.OpenOwner(scope)
	if err != nil {
		t.Fatal(err)
	}
	epoch, previousDigest := uint64(1), ""
	if found {
		epoch, previousDigest = prior.Acquisition.OwnerEpoch+1, prior.FactDigest
	}
	binary := attemptTestBinary()
	ownerProcess := processsupervisor.ProcessIdentity{PID: 8001 + int(epoch), BirthSeconds: 1_700_000_000, BirthMicroseconds: 11, SessionID: 8001 + int(epoch), ProcessGroupID: 8001 + int(epoch)}
	acquisition := ControlOwnerAcquisition{Scope: scope, OwnerEpoch: epoch, OwnerUID: 501, OwnerGID: 20, OwnerProcess: ownerProcess, OwnerBinary: binary, ObserverIdentity: "darwin-owner-observer/v1", ObservedAt: "2026-08-28T00:00:00Z"}
	ownerResult, err := store.AcquireOwner(context.Background(), attemptOwnerVerifier{want: acquisition}, epoch-1, previousDigest, acquisition)
	if err != nil {
		t.Fatal(err)
	}
	owner := CurrentOwnerBinding{Scope: scope, OwnerEpoch: epoch, ControlOwnerAcquiredFactDigest: ownerResult.State.FactDigest}
	run := attemptTestRunAuthority(state.Identity)
	bound, err := store.BindOwnerToAttempt(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, owner)
	if err != nil {
		t.Fatal(err)
	}
	controlDirectory := processsupervisor.ControlDirectoryIdentity{CanonicalPath: "/tmp/marshal-control-" + state.Identity.AttemptID, Device: 2, Inode: 100 + epoch, FileType: "directory", UID: 501, GID: 20, Mode: POSIXFileTypeDirectory | 0o700, LinkCount: 2}
	socket := processsupervisor.ControlSocketIdentity{Device: 2, Inode: 200 + epoch, FileType: "socket", UID: 501, GID: 20, Mode: 0o140000 | 0o600, LinkCount: 1}
	controlFiles := attemptTestControlFiles(socket.Device, socket.Inode+1)
	supervisorProcess := processsupervisor.ProcessIdentity{PID: 9001 + int(epoch), BirthSeconds: 1_700_000_001, BirthMicroseconds: 12, SessionID: 9001 + int(epoch), ProcessGroupID: 9001 + int(epoch)}
	core := processsupervisor.CoreIdentity{UID: 501, GID: 20, Process: ownerProcess, Binary: binary}
	request := processsupervisor.BootstrapRequest{
		SchemaVersion: processsupervisor.BootstrapSchema, ProtocolRevision: processsupervisor.ProtocolRevision,
		SessionID: "supervisor-" + state.Identity.AttemptID + "-" + fmt.Sprint(epoch), SessionNonce: strings.Repeat("1", 64),
		OwnerEpoch: epoch, Authority: supervisorAuthorityTuple(state.Identity), LaunchAuthorizedFact: bound.State.LaunchAuthorizedDigest,
		CurrentAuthorityHead: bound.State.HeadDigest, ControlDirectoryIdentity: controlDirectory, Core: core,
	}
	prepared, err := NewSupervisorBootstrapPrepared(owner, request)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, err := store.AppendSupervisorBootstrap(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, bound.State.Revision, bound.State.HeadDigest, AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, prepared)
	if err != nil {
		t.Fatal(err)
	}
	handshake := processsupervisor.HandshakeResponse{SchemaVersion: processsupervisor.HandshakeSchema, ProtocolRevision: processsupervisor.ProtocolRevision, Status: "ok", ReasonCode: "process-supervisor-ready", SessionID: request.SessionID, SessionNonceDigest: prepared.SessionNonceDigest, OwnerEpoch: epoch, CurrentAuthorityHead: request.CurrentAuthorityHead, CommandSequence: 0, CommandHead: processsupervisor.CommandGenesisDigest, JournalSequence: 1, JournalHead: attemptTestDigest("journal-head-" + fmt.Sprint(epoch)), ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: "2026-08-28T00:00:01Z", SupervisorProcess: supervisorProcess, SupervisorBinary: binary, ControlSocket: socket, ControlFiles: controlFiles}
	anchor := processsupervisor.HandshakeAnchor{SessionID: handshake.SessionID, SessionNonceDigest: handshake.SessionNonceDigest, Authority: request.Authority, OwnerEpoch: epoch, CurrentAuthorityHead: request.CurrentAuthorityHead, CommandSequence: 0, CommandHead: processsupervisor.CommandGenesisDigest, JournalSequence: 1, JournalHead: handshake.JournalHead, UID: 501, GID: 20, FixedBinary: binary, ControlSocket: socket, ControlFiles: controlFiles}
	started, err := NewProcessSupervisorStartedFromBootstrap(bootstrap.State.SupervisorBootstrapDigest, prepared, handshake, anchor, processsupervisor.CoreIdentity{UID: 501, GID: 20, Process: supervisorProcess, Binary: binary})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.AppendSupervisorStarted(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, bootstrap.State.Revision, bootstrap.State.HeadDigest, AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, started)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.SupervisorMechanicsAnchor.ControlFiles != handshake.ControlFiles {
		t.Fatalf("process-supervisor-started projection dropped control-file identities: got=%+v want=%+v", result.State.SupervisorMechanicsAnchor.ControlFiles, handshake.ControlFiles)
	}
	return result.State
}

func appendTestSupervisorClosed(t *testing.T, store *DurableStore, state AttemptAuthorityState, request CleanupAuthorizationRequest) AttemptAuthorityState {
	t.Helper()
	state = appendTestSupervisorReconnect(t, store, state)
	owner, found, err := store.OpenOwner(state.Owner.Scope)
	if err != nil || !found {
		t.Fatalf("current owner found=%v err=%v", found, err)
	}
	closeIntent := testSupervisorIntent(state, processsupervisor.CommandClose, SupervisorCommandRebuildProjection{
		ProcessTerminalFactDigest: state.ProcessTerminalDigest, AllocationTerminatedFactDigest: state.AllocationTerminalDigest,
		CleanupBindingDigest: state.CleanupBindingDigest,
	})
	closeOutcome := state.ProcessTerminalEvidence.Outcome
	closeOutcome.State = SupervisorSessionClosed
	state, outcomeFactDigest := appendTestSupervisorCheckpoint(t, store, state, closeIntent, closeOutcome, "ok")
	absence := SupervisorAbsenceObservation{State: "absent", SupervisorProcess: state.SupervisorStarted.Handshake.SupervisorProcess, ObserverIdentity: "darwin-supervisor-absence-observer/v1", ObservedAt: "2026-08-29T00:00:02Z"}
	absenceDigest, err := canonicalDigest(absence)
	if err != nil {
		t.Fatal(err)
	}
	closed := ProcessSupervisorClosed{
		ProtocolRevision:                   processsupervisor.ProtocolRevision,
		SessionID:                          state.SupervisorStarted.Handshake.SessionID,
		Owner:                              state.Owner,
		SupervisorStartedFactDigest:        state.SupervisorStartedDigest,
		TerminalizationID:                  state.TerminalizationID,
		CleanupBindingDigest:               state.CleanupBindingDigest,
		ProcessTerminalFactDigest:          state.ProcessTerminalDigest,
		AllocationTerminatedFactDigest:     state.AllocationTerminalDigest,
		CloseIntentDigest:                  closeIntent.RequestDigest,
		CloseReceiptDigest:                 state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1].Evidence.ReceiptDigest,
		CloseObservationDigest:             state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1].Evidence.ObservationDigest,
		FinalCommandHead:                   state.SupervisorCommandHead,
		SupervisorAbsenceObservationDigest: absenceDigest,
		SupervisorProcess:                  state.SupervisorStarted.Handshake.SupervisorProcess,
		ObserverIdentity:                   "darwin-supervisor-absence-observer/v1",
		ObservedAt:                         "2026-08-29T00:00:02Z",
		SupervisorAbsence:                  absence,
	}
	result, err := store.AppendSupervisorClosed(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: request.CurrentRunAuthority}, state.Revision, state.HeadDigest, request, closed, outcomeFactDigest)
	if err != nil {
		t.Fatal(err)
	}
	return result.State
}

func attemptTestBinding() LedgerBinding {
	return LedgerBinding{
		LeaseID: "lease-1", Generation: 7, FencingToken: "fencing-token",
		AttemptID: "attempt-1", AllocationID: "allocation-1", Expiry: time.Now().Add(time.Hour),
		RegistrationID: "registration-1", SnapshotDigest: attemptTestDigest("snapshot"), EvidenceDigest: attemptTestDigest("evidence"),
	}
}

func attemptTestDRC(kind EnvelopeKind, sequence uint64) (DRC, ResultEnvelope) {
	digest := attemptTestDigest(string(kind) + "-payload")
	op, _ := kindToOperation(kind)
	return DRC{
		AuthorityNamespaceID: "authority:test", TaskID: "task-1", RunID: "run-1",
		AttemptID: "attempt-1", AllocationID: "allocation-1", LeaseID: "lease-1",
		Generation: 7, FencingToken: "fencing-token", CommandID: "command-1",
		IdempotencyKey: string(kind) + "-key", RequestDigest: digest, Nonce: "nonce-1",
		Expiry: time.Now().Add(time.Hour), Operation: op, RegistrationID: "registration-1",
		SnapshotDigest: attemptTestDigest("snapshot"), EvidenceDigest: attemptTestDigest("evidence"),
	}, ResultEnvelope{Kind: kind, ResultDigest: digest, Sequence: sequence}
}

func attemptTestDRCForState(state AttemptAuthorityState, kind EnvelopeKind, sequence uint64) (DRC, ResultEnvelope) {
	drc, envelope := attemptTestDRC(kind, sequence)
	drc.AuthorityNamespaceID = state.Identity.AuthorityNamespaceRef
	drc.TaskID = state.Identity.TaskID
	drc.RunID = state.Identity.RunID
	drc.AttemptID = state.Identity.AttemptID
	drc.AllocationID = state.Identity.AllocationID
	drc.LeaseID = state.Identity.LeaseID
	drc.Generation = uint64(state.Identity.DispatchGeneration)
	drc.CommandID = state.CommandID
	return drc, envelope
}

func attemptTestRunAuthority(id AttemptIdentity) RunAuthorityBinding {
	return RunAuthorityBinding{AuthorityNamespaceID: id.AuthorityNamespaceID, RunID: id.RunID, OrchestratorID: id.OrchestratorID, RunAuthorityDigest: id.RunAuthorityDigest}
}

func testSupervisorIntent(state AttemptAuthorityState, command processsupervisor.CommandName, rebuild SupervisorCommandRebuildProjection) SupervisorCommandIntent {
	sequence := state.SupervisorCommandSequence + 1
	commandID := fmt.Sprintf("test-%s-%d", command, sequence)
	pre := state.SupervisorMechanicsAnchor
	return SupervisorCommandIntent{
		ProtocolRevision: processsupervisor.ProtocolRevision,
		SessionID:        state.SupervisorStarted.Handshake.SessionID, Command: command, CommandID: commandID,
		Sequence: sequence, PreviousCommandHead: state.SupervisorCommandHead, CurrentAuthorityHead: pre.CurrentAuthorityHead,
		Deadline: "2026-08-29T00:02:00Z", RequestDigest: attemptTestDigest("request-" + commandID),
		PayloadDigest: attemptTestDigest("payload-" + commandID), Rebuild: rebuild, PreCommand: pre,
	}
}

func appendTestSupervisorReconnect(t *testing.T, store *DurableStore, state AttemptAuthorityState) AttemptAuthorityState {
	t.Helper()
	if state.SupervisorMechanicsAnchor.OwnerEpoch == state.Owner.OwnerEpoch && state.SupervisorMechanicsAnchor.CurrentAuthorityHead == state.HeadDigest {
		return state
	}
	prior, found, err := store.OpenOwner(state.Owner.Scope)
	if err != nil || !found {
		t.Fatalf("current owner found=%v err=%v", found, err)
	}
	epoch := prior.Acquisition.OwnerEpoch + 1
	process := processsupervisor.ProcessIdentity{PID: 12000 + int(epoch), BirthSeconds: 1_700_000_010, BirthMicroseconds: int64(epoch), SessionID: 12000 + int(epoch), ProcessGroupID: 12000 + int(epoch)}
	acquisition := prior.Acquisition
	acquisition.OwnerEpoch = epoch
	acquisition.OwnerProcess = process
	acquisition.ObservedAt = "2026-08-29T00:00:10Z"
	ownerResult, err := store.AcquireOwner(context.Background(), attemptOwnerVerifier{want: acquisition}, prior.Acquisition.OwnerEpoch, prior.FactDigest, acquisition)
	if err != nil {
		t.Fatal(err)
	}
	owner := CurrentOwnerBinding{Scope: state.Owner.Scope, OwnerEpoch: epoch, ControlOwnerAcquiredFactDigest: ownerResult.State.FactDigest}
	run := attemptTestRunAuthority(state.Identity)
	bound, err := store.BindOwnerToAttempt(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, owner)
	if err != nil {
		t.Fatal(err)
	}
	previous := supervisorHandshakeAnchor(state.SupervisorMechanicsAnchor)
	current := previous
	current.OwnerEpoch = epoch
	current.CurrentAuthorityHead = bound.State.HeadDigest
	recovery := processsupervisor.SessionRecoveryEvidence{Reconciliation: processsupervisor.ReconciliationUnchanged, Previous: previous, Current: current}
	result, err := store.AppendSupervisorReconnect(context.Background(), attemptOwnerVerifier{want: acquisition}, attemptRunVerifier{want: run}, bound.State.Revision, bound.State.HeadDigest, AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, owner, recovery)
	if err != nil {
		t.Fatal(err)
	}
	return result.State
}

func appendTestSupervisorCheckpoint(t *testing.T, store *DurableStore, state AttemptAuthorityState, intent SupervisorCommandIntent, outcome SupervisorProcessOutcome, disposition string) (AttemptAuthorityState, string) {
	t.Helper()
	owner, found, err := store.OpenOwner(state.Owner.Scope)
	if err != nil || !found {
		t.Fatalf("current owner found=%v err=%v", found, err)
	}
	run := attemptTestRunAuthority(state.Identity)
	request := AttemptAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}
	intended, err := store.AppendSupervisorCommandIntent(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, request, state.Owner, intent)
	if err != nil {
		t.Fatalf("append supervisor %s intent: %v", intent.Command, err)
	}
	evidence := testSupervisorEvidence(t, intent, outcome, disposition)
	checkpoint, err := store.AppendSupervisorCommandOutcome(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, intended.State.Revision, intended.State.HeadDigest, request, state.Owner, evidence)
	if err != nil {
		t.Fatalf("append supervisor %s outcome: %v", intent.Command, err)
	}
	return checkpoint.State, checkpoint.TransitionDigest
}

func testSupervisorEvidence(t *testing.T, intent SupervisorCommandIntent, outcome SupervisorProcessOutcome, disposition string) SupervisorCommandEvidence {
	t.Helper()
	evidence := SupervisorCommandEvidence{
		ProtocolRevision: processsupervisor.ProtocolRevision,
		SessionID:        intent.SessionID, Command: intent.Command, CommandID: intent.CommandID,
		Sequence: intent.Sequence, PreviousCommandHead: intent.PreviousCommandHead, CurrentAuthorityHead: intent.CurrentAuthorityHead,
		RequestDigest: intent.RequestDigest, Disposition: disposition, ReasonCode: "process-supervisor-" + string(intent.Command) + "-" + disposition,
		Outcome: outcome, PreCommand: intent.PreCommand,
	}
	if disposition == "rejected" {
		evidence.Outcome = SupervisorProcessOutcome{}
	}
	if intent.Command == processsupervisor.CommandBindAuthority && disposition == "ok" {
		evidence.BoundAuthorityHead = intent.Rebuild.AuthorityHead
	}
	observationDigest, receiptDigest, err := evidence.boundMechanicsDigests()
	if err != nil {
		t.Fatal(err)
	}
	evidence.ObservationDigest, evidence.ReceiptDigest = observationDigest, receiptDigest
	evidence.CommandHead, err = canonicalDigest(struct {
		Previous string `json:"previousCommandDigest"`
		Request  string `json:"requestDigest"`
		Receipt  string `json:"receiptDigest"`
	}{intent.PreviousCommandHead, intent.RequestDigest, evidence.ReceiptDigest})
	if err != nil {
		t.Fatal(err)
	}
	evidence.PostCommand = intent.PreCommand
	evidence.PostCommand.CommandSequence = evidence.Sequence
	evidence.PostCommand.CommandHead = evidence.CommandHead
	evidence.PostCommand.JournalSequence += 2
	evidence.PostCommand.JournalHead = attemptTestDigest("journal-" + intent.CommandID)
	if intent.Command == processsupervisor.CommandBindAuthority && disposition == "ok" {
		evidence.PostCommand.CurrentAuthorityHead = evidence.BoundAuthorityHead
	}
	return evidence
}

func appendTestProcessStartedCheckpoints(t *testing.T, store *DurableStore, state AttemptAuthorityState, transition *AttemptTransition) AttemptAuthorityState {
	t.Helper()
	bind := testSupervisorIntent(state, processsupervisor.CommandBindAuthority, SupervisorCommandRebuildProjection{
		SupervisorStartedFactDigest: state.SupervisorStartedDigest, OwnerEpoch: state.Owner.OwnerEpoch,
		PreviousAuthorityHead: state.SupervisorStarted.Handshake.CurrentAuthorityHead, AuthorityHead: state.SupervisorStartedDigest,
	})
	state, transition.SupervisorBindOutcomeFactDigest = appendTestSupervisorCheckpoint(t, store, state, bind, SupervisorProcessOutcome{}, "ok")
	runtimeDigest := attemptTestDigest("runtime-object-" + state.Identity.AttemptID)
	workingDigest := attemptTestDigest("working-object-" + state.Identity.AttemptID)
	spawn := testSupervisorIntent(state, processsupervisor.CommandSpawn, SupervisorCommandRebuildProjection{
		SupervisorStartedFactDigest: state.SupervisorStartedDigest, LaunchAuthorizedFactDigest: state.LaunchAuthorizedDigest,
		LaunchMaterialsDigest: state.LaunchMaterialsDigest, AgentLaunchSpecDigest: state.AgentLaunchSpecDigest,
		RuntimeObjectDigest: runtimeDigest, WorkingObjectDigest: workingDigest, ClosureProfileID: state.LaunchClosure.ClosureProfileID,
		ArgvDigest: attemptTestDigest("argv-" + state.Identity.AttemptID), EnvironmentDigest: attemptTestDigest("env-" + state.Identity.AttemptID), StdinDigest: attemptTestDigest("stdin-" + state.Identity.AttemptID),
	})
	outcome := SupervisorProcessOutcome{
		State: SupervisorProcessExecStopped, MechanicsState: "exec-stopped",
		Process:          processsupervisor.ProcessIdentity{PID: transition.Process.PID, BirthSeconds: transition.Process.BirthSeconds, BirthMicroseconds: transition.Process.BirthMicroseconds, SessionID: transition.Process.PID, ProcessGroupID: transition.Process.PGID},
		ObserverIdentity: "darwin-fixed-process-supervisor-v1", ObservedAt: transition.ObservedAt,
		RuntimeObjectDigest: runtimeDigest, WorkingObjectDigest: workingDigest,
	}
	state, transition.SupervisorOutcomeFactDigest = appendTestSupervisorCheckpoint(t, store, state, spawn, outcome, "ok")
	return state
}

func appendTestTerminalCheckpoint(t *testing.T, store *DurableStore, state AttemptAuthorityState, transition *AttemptTransition) AttemptAuthorityState {
	t.Helper()
	state = appendTestSupervisorReconnect(t, store, state)
	command := processsupervisor.CommandInspect
	outcomeState := SupervisorProcessAbsent
	if transition.ProcessTerminalKind == ProcessTerminated {
		command, outcomeState = processsupervisor.CommandTerminate, SupervisorProcessExited
	} else if transition.ProcessTerminalKind == ProcessIdentityConflict {
		outcomeState = SupervisorProcessIdentityConflict
	}
	intent := testSupervisorIntent(state, command, SupervisorCommandRebuildProjection{
		TerminalizationBarrierDigest: state.BarrierDigest, TerminalizationID: state.TerminalizationID,
		TerminalGeneration: uint64(state.TerminalGeneration), CleanupBindingDigest: state.CleanupBindingDigest,
		ProcessStartedFactDigest: state.ProcessStartedDigest, LastObservationDigest: supervisorLastObservation(state),
	})
	spawn := state.ProcessStartedEvidence.Outcome
	spawn.State, spawn.MechanicsState = outcomeState, "terminal"
	state, transition.SupervisorOutcomeFactDigest = appendTestSupervisorCheckpoint(t, store, state, intent, spawn, "ok")
	transition.ObservationDigest = state.SupervisorCommandCheckpoints[len(state.SupervisorCommandCheckpoints)-1].Evidence.ObservationDigest
	return state
}

func appendTestProcessTerminal(t *testing.T, store *DurableStore, state AttemptAuthorityState, request CleanupAuthorizationRequest, transition AttemptTransition) (AttemptAppendResult, AttemptTransition, error) {
	t.Helper()
	if state.SupervisorBootstrapDigest != "" && transition.SupervisorOutcomeFactDigest == "" {
		state = appendTestTerminalCheckpoint(t, store, state, &transition)
	}
	result, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: request.CurrentRunAuthority}, state.Revision, state.HeadDigest, request, transition)
	return result, transition, err
}

func appendHistoricalAttemptTransition(t *testing.T, store *DurableStore, state AttemptAuthorityState, transition AttemptTransition) AttemptAuthorityState {
	t.Helper()
	key, err := transition.Identity.Key()
	if err != nil {
		t.Fatal(err)
	}
	fact := &attemptAuthorityFact{ProtocolRevision: attemptAuthorityProtocolRevision, FactType: string(transition.Kind), Sequence: store.nextSequence, AttemptKey: key, Revision: state.Revision + 1, PreviousDigest: state.HeadDigest, Transition: transition}
	if err := prepareAttemptFact(state, true, fact, true); err != nil {
		t.Fatal(err)
	}
	if err := store.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
		t.Fatal(err)
	}
	store.nextSequence++
	replayed, found, err := store.AttemptState(transition.Identity)
	if err != nil || !found {
		t.Fatalf("historical transition replay found=%v err=%v", found, err)
	}
	return replayed
}

func appendAuthorizedAttempt(t *testing.T, store *DurableStore, revision uint64, head string, transition AttemptTransition) (AttemptAppendResult, error) {
	t.Helper()
	if transition.Kind == AttemptTransitionLaunchAuthorized && zeroLaunchClosure(transition.LaunchClosure) {
		transition.LaunchClosure = attemptTestClosure(t)
	}
	if transition.Kind == AttemptTransitionProcessStarted && transition.LaunchMaterialsDigest == "" {
		closure := attemptTestClosure(t)
		transition.LaunchMaterialsDigest = closure.LaunchMaterialsDigest
		transition.AgentLaunchSpecDigest = closure.AgentLaunchSpecDigest
	}
	run := attemptTestRunAuthority(transition.Identity)
	if transition.Kind == AttemptTransitionProcessStarted {
		state, found, stateErr := store.AttemptState(transition.Identity)
		if stateErr != nil {
			return AttemptAppendResult{}, stateErr
		}
		if found && state.ControlOwnerBindingDigest != "" {
			if state.SupervisorBootstrapDigest != "" && transition.SupervisorOutcomeFactDigest == "" {
				state = appendTestProcessStartedCheckpoints(t, store, state, &transition)
				revision, head = state.Revision, state.HeadDigest
			}
			owner, ownerFound, ownerErr := store.OpenOwner(state.Owner.Scope)
			if ownerErr != nil {
				return AttemptAppendResult{}, ownerErr
			}
			if !ownerFound {
				return AttemptAppendResult{}, ErrControlOwnerUnknown
			}
			return store.AppendProcessStarted(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, revision, head, AttemptAuthorizationRequest{Identity: transition.Identity, CurrentRunAuthority: run}, state.Owner, transition)
		}
	}
	return store.CompareAndAppendAuthorized(context.Background(), attemptRunVerifier{want: run}, revision, head, AttemptAuthorizationRequest{Identity: transition.Identity, CurrentRunAuthority: run}, transition)
}

func appendTestBarrier(t *testing.T, store *DurableStore, state AttemptAuthorityState, terminalizationID string, reason TerminalReason) AttemptAppendResult {
	t.Helper()
	run := attemptTestRunAuthority(state.Identity)
	result, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, state.Revision, state.HeadDigest, BarrierAuthorizationRequest{Identity: state.Identity, CurrentRunAuthority: run}, AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: state.Identity, TerminalizationID: terminalizationID, EligibilityTerminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: reason}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAttemptAuthorityLaunchCrashProjectionAndReplay(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	openedResult, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil || !openedResult.Appended || openedResult.State.LaunchState != LaunchNotAuthorized {
		t.Fatalf("opened = %#v, err=%v", openedResult, err)
	}
	opened := openedResult.State
	opened = appendTestAcceptedProvision(t, store, opened)
	authorizedResult, err := appendAuthorizedAttempt(t, store, opened.Revision, opened.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-auth-1"})
	if err != nil || !authorizedResult.Appended || authorizedResult.State.LaunchState != LaunchUncertain || authorizedResult.TransitionDigest != authorizedResult.State.LaunchAuthorizedDigest {
		t.Fatalf("authorized = %#v, err=%v", authorizedResult, err)
	}
	authorized := authorizedResult.State
	reopened, err := OpenResultIngressStore(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	recovered, ok, err := reopened.AttemptState(id)
	if err != nil || !ok || recovered.LaunchState != LaunchUncertain || recovered.HeadDigest != authorized.HeadDigest {
		t.Fatalf("recovered = %#v, ok=%v, err=%v", recovered, ok, err)
	}
	// Exact open is a stable replay and never creates a second authority.
	replay, err := appendAuthorizedAttempt(t, reopened, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil || replay.Appended || replay.State.HeadDigest != authorized.HeadDigest || replay.TransitionDigest != authorized.OpenedDigest {
		t.Fatalf("open replay = %#v, err=%v", replay, err)
	}
}

func TestSupervisorOutcomeReferenceProcessStartedExactReplay(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openFreshStartedAttempt(t, store)
	owner, found, err := store.OpenOwner(started.Owner.Scope)
	if err != nil || !found {
		t.Fatalf("owner found=%v err=%v", found, err)
	}
	run := attemptTestRunAuthority(started.Identity)
	transition := AttemptTransition{
		Kind:                            AttemptTransitionProcessStarted,
		Identity:                        started.Identity,
		CommandID:                       started.CommandID,
		ObservedAt:                      started.ObservedAt,
		Process:                         started.Process,
		LaunchMaterialsDigest:           started.LaunchMaterialsDigest,
		AgentLaunchSpecDigest:           started.AgentLaunchSpecDigest,
		SupervisorBindOutcomeFactDigest: started.ProcessStartedBindOutcomeDigest,
		SupervisorOutcomeFactDigest:     started.ProcessStartedOutcomeDigest,
	}
	replay, err := store.AppendProcessStarted(context.Background(), attemptOwnerVerifier{want: owner.Acquisition}, attemptRunVerifier{want: run}, 0, "", AttemptAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, started.Owner, transition)
	if err != nil || replay.Appended || replay.TransitionDigest != started.ProcessStartedDigest || replay.State.HeadDigest != started.HeadDigest {
		t.Fatalf("process-started reference replay=%#v err=%v", replay, err)
	}
}

func TestOpenedLaunchAndProcessFactsRequireHeldCurrentRunAuthority(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	id := attemptTestIdentity()
	run := attemptTestRunAuthority(id)
	openedTransition := AttemptTransition{Kind: AttemptTransitionOpened, Identity: id}
	if _, err := store.CompareAndAppend(0, "", openedTransition); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("generic opened err=%v", err)
	}
	if _, err := store.CompareAndAppendAuthorized(context.Background(), nil, 0, "", AttemptAuthorizationRequest{Identity: id, CurrentRunAuthority: run}, openedTransition); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("nil opened verifier err=%v", err)
	}
	wrong := run
	wrong.OrchestratorID = "stale-orchestrator"
	if _, err := store.CompareAndAppendAuthorized(context.Background(), attemptRunVerifier{want: run}, 0, "", AttemptAuthorizationRequest{Identity: id, CurrentRunAuthority: wrong}, openedTransition); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("stale opened authority err=%v", err)
	}
	if states, err := store.AttemptStates(); err != nil || len(states) != 0 {
		t.Fatalf("unauthorized opened appended state=%#v err=%v", states, err)
	}
	openedResult, err := store.CompareAndAppendAuthorized(context.Background(), attemptRunVerifier{want: run}, 0, "", AttemptAuthorizationRequest{Identity: id, CurrentRunAuthority: run}, openedTransition)
	if err != nil || !openedResult.Appended {
		t.Fatalf("opened=%#v err=%v", openedResult, err)
	}
	launch := AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-held", LaunchClosure: attemptTestClosure(t)}
	if _, err := store.CompareAndAppendAuthorized(context.Background(), attemptRunVerifier{want: run, err: errors.New("authority drift")}, openedResult.State.Revision, openedResult.State.HeadDigest, AttemptAuthorizationRequest{Identity: id, CurrentRunAuthority: run}, launch); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("stale launch authority err=%v", err)
	}
	current, _, _ := store.AttemptState(id)
	if current.LaunchState != LaunchNotAuthorized {
		t.Fatalf("stale authority appended launch: %#v", current)
	}
	current = appendTestAcceptedProvision(t, store, current)
	launchResult, err := store.CompareAndAppendAuthorized(context.Background(), attemptRunVerifier{want: run}, current.Revision, current.HeadDigest, AttemptAuthorizationRequest{Identity: id, CurrentRunAuthority: run}, launch)
	if err != nil || !launchResult.Appended {
		t.Fatalf("launch=%#v err=%v", launchResult, err)
	}
	calls := 0
	replay, err := store.CompareAndAppendAuthorized(context.Background(), attemptRunVerifier{want: run, calls: &calls}, current.Revision, current.HeadDigest, AttemptAuthorizationRequest{Identity: id, CurrentRunAuthority: run}, launch)
	if err != nil || replay.Appended || calls != 1 || replay.TransitionDigest != launchResult.TransitionDigest {
		t.Fatalf("launch replay=%#v calls=%d err=%v", replay, calls, err)
	}
	started := AttemptTransition{
		Kind:                  AttemptTransitionProcessStarted,
		Identity:              id,
		CommandID:             "command-1",
		ObservedAt:            "2026-08-28T00:00:00Z",
		Process:               attemptTestProcess(t),
		LaunchMaterialsDigest: launch.LaunchClosure.LaunchMaterialsDigest,
		AgentLaunchSpecDigest: launch.LaunchClosure.AgentLaunchSpecDigest,
	}
	if _, err := store.CompareAndAppendAuthorized(context.Background(), attemptRunVerifier{want: run, err: errors.New("authority drift")}, launchResult.State.Revision, launchResult.State.HeadDigest, AttemptAuthorizationRequest{Identity: id, CurrentRunAuthority: run}, started); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("stale process authority err=%v", err)
	}
	current, _, _ = store.AttemptState(id)
	if current.ProcessStartedDigest != "" {
		t.Fatalf("stale authority appended process: %#v", current)
	}
}

func TestAttemptLogicalKeyRejectsSiblingAuthorityAndTupleDrift(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	id := attemptTestIdentity()
	if _, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*AttemptIdentity){
		func(other *AttemptIdentity) { other.OrchestratorID = "other-orchestrator" },
		func(other *AttemptIdentity) { other.RunAuthorityDigest = attemptTestDigest("other-run-authority") },
		func(other *AttemptIdentity) { other.AllocationID = "other-allocation" },
		func(other *AttemptIdentity) {
			other.LeaseID, other.LeaseDigest = "other-lease", attemptTestDigest("other-lease")
		},
		func(other *AttemptIdentity) { other.AuthorityNamespaceRef = "authority:other-wire-ref" },
	} {
		other := id
		mutate(&other)
		if _, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: other}); !errors.Is(err, ErrAttemptAuthorityConflict) {
			t.Fatalf("sibling identity accepted: %#v err=%v", other, err)
		}
	}
	states, err := store.AttemptStates()
	if err != nil || len(states) != 1 || states[0].Identity != id {
		t.Fatalf("states=%#v err=%v", states, err)
	}
}

func TestProcessObservationRejectsNonCanonicalPathsAndFileTypes(t *testing.T) {
	base := attemptTestProcess(t)
	for _, mutate := range []func(*ProcessObservation){
		func(observation *ProcessObservation) { observation.WorkingDirectory = "relative/work" },
		func(observation *ProcessObservation) { observation.WorkingDirectory = "/tmp/../work" },
		func(observation *ProcessObservation) { observation.ExecutablePath = "relative/marshal" },
		func(observation *ProcessObservation) { observation.ExecutablePath = "/fixed/../marshal" },
		func(observation *ProcessObservation) { observation.WorkingDirectoryType = POSIXFileTypeRegular },
		func(observation *ProcessObservation) { observation.ExecutableType = POSIXFileTypeDirectory },
	} {
		forged := base
		mutate(&forged)
		if _, err := SealProcessObservation(forged); err == nil {
			t.Fatalf("forged process observation accepted: %#v", forged)
		}
	}
}

func TestProcessStartedRejectsNonCanonicalOrPreBirthObservedAt(t *testing.T) {
	for _, observedAt := range []string{"not-a-time", "2026-08-28T00:00:00+00:00", "1970-01-01T00:00:01Z"} {
		t.Run(observedAt, func(t *testing.T) {
			store, _ := OpenResultIngressStore(t.TempDir())
			id := attemptTestIdentity()
			opened, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
			if err != nil {
				t.Fatal(err)
			}
			provisioned := appendTestAcceptedProvision(t, store, opened.State)
			authorized, err := appendAuthorizedAttempt(t, store, provisioned.Revision, provisioned.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-time"})
			if err != nil {
				t.Fatal(err)
			}
			transition := AttemptTransition{Kind: AttemptTransitionProcessStarted, Identity: id, CommandID: "command-1", ObservedAt: observedAt, Process: attemptTestProcess(t)}
			if _, err := appendAuthorizedAttempt(t, store, authorized.State.Revision, authorized.State.HeadDigest, transition); !errors.Is(err, ErrAttemptAuthorityConflict) {
				t.Fatalf("observedAt %q err=%v", observedAt, err)
			}
			current, _, _ := store.AttemptState(id)
			if current.ProcessStartedDigest != "" {
				t.Fatalf("invalid observedAt appended process: %#v", current)
			}
		})
	}
}

func TestAttemptAuthorityRejectsStaleRevisionAndHead(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	openedResult, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	opened := openedResult.State
	for name, stale := range map[string]struct {
		revision uint64
		head     string
	}{
		"stale-revision": {revision: 0, head: opened.HeadDigest},
		"stale-head":     {revision: opened.Revision, head: attemptTestDigest("stale-head")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := appendAuthorizedAttempt(t, store, stale.revision, stale.head, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-auth-stale"})
			if !errors.Is(err, ErrAttemptAuthorityConflict) {
				t.Fatalf("err=%v, want ErrAttemptAuthorityConflict", err)
			}
		})
	}
	current, found, err := store.AttemptState(id)
	if err != nil || !found || current.Revision != opened.Revision || current.HeadDigest != opened.HeadDigest {
		t.Fatalf("stale CAS mutated authority: current=%#v found=%v err=%v", current, found, err)
	}
}

func TestAttemptAuthorityTwoStoreCASCompetition(t *testing.T) {
	dir := t.TempDir()
	first, _ := OpenResultIngressStore(dir)
	second, _ := OpenResultIngressStore(dir)
	openedResult, err := appendAuthorizedAttempt(t, first, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: attemptTestIdentity()})
	if err != nil {
		t.Fatal(err)
	}
	opened := appendTestAcceptedProvision(t, first, openedResult.State)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, candidate := range []struct {
		store *ingressDurableStore
		id    string
	}{{first, "launch-a"}, {second, "launch-b"}} {
		wg.Add(1)
		go func(candidate struct {
			store *ingressDurableStore
			id    string
		}) {
			defer wg.Done()
			_, err := appendAuthorizedAttempt(t, candidate.store, opened.Revision, opened.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: attemptTestIdentity(), LaunchAuthorizationID: candidate.id})
			errs <- err
		}(candidate)
	}
	wg.Wait()
	close(errs)
	succeeded, conflicted := 0, 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrAttemptAuthorityConflict) {
			conflicted++
		} else {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestAdmissionAndBarrierShareCASAndAllKindsClose(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	started := openStartedAttempt(t, store)
	ingress, err := NewDurableIngress(attemptTestBinding(), store)
	if err != nil {
		t.Fatal(err)
	}
	drc, envelope := attemptTestDRCForState(started, KindWorkerResult, 1)
	admission, err := ingress.Admit(context.Background(), drc, envelope)
	if err != nil {
		t.Fatal(err)
	}
	current, ok, err := store.AttemptState(started.Identity)
	if err != nil || !ok || current.CommittedResultFactDigest != admission.FactDigest {
		t.Fatalf("current = %#v ok=%v err=%v", current, ok, err)
	}
	barrierResult := appendTestBarrier(t, store, current, "terminal-1", TerminalAttemptCompleted)
	barrier := barrierResult.State
	if !barrier.AdmissionClosed || barrier.BarrierAdmissionFactDigest != admission.FactDigest || barrier.TerminalGeneration != 8 {
		t.Fatalf("barrier = %#v", barrier)
	}

	for sequence, kind := range []EnvelopeKind{KindWorkerResult, KindCandidate, KindEvidenceRef, KindCheckpoint, KindHeartbeat, KindReceipt, KindLog, KindAssessment} {
		drc, envelope := attemptTestDRCForState(current, kind, uint64(sequence+2))
		drc.IdempotencyKey += "-late"
		if _, err := ingress.Admit(context.Background(), drc, envelope); !errors.Is(err, ErrStaleLease) {
			t.Errorf("late %s err=%v, want ErrStaleLease", kind, err)
		}
	}
	quarantine := ingress.Quarantine()
	if len(quarantine) < 8 {
		t.Fatalf("quarantine len=%d, want >=8", len(quarantine))
	}
}

func TestGovernedDRCRequiresPersistedProcessCommandAndNamespace(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	ingress, _ := NewDurableIngress(attemptTestBinding(), store)
	drc, envelope := attemptTestDRC(KindWorkerResult, 1)
	drc.CommandID = "different-command"
	if _, err := ingress.Admit(context.Background(), drc, envelope); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("mismatched process command admitted: %v", err)
	}
	drc, envelope = attemptTestDRC(KindWorkerResult, 1)
	drc.AuthorityNamespaceID = "authority:drifted"
	if _, err := ingress.Admit(context.Background(), drc, envelope); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("mismatched authority namespace fell back to legacy admission: %v", err)
	}
	current, ok, err := store.AttemptState(started.Identity)
	if err != nil || !ok || current.CommittedResultFactDigest != "" || current.Revision != started.Revision {
		t.Fatalf("mismatched command mutated authority: %#v ok=%v err=%v", current, ok, err)
	}
}

func TestCurrentAttemptForDRCRejectsAmbiguousCandidatesDeterministically(t *testing.T) {
	ingress, err := NewIngress(attemptTestBinding())
	if err != nil {
		t.Fatal(err)
	}
	state := AttemptAuthorityState{Identity: attemptTestIdentity(), ProcessStartedDigest: attemptTestDigest("started"), CommandID: "command-1"}
	other := state
	other.Identity.AuthorityNamespaceID.TenantNamespace = "tenant-2"
	ingress.attempts = map[string]AttemptAuthorityState{"z": state, "a": other}
	drc, _ := attemptTestDRC(KindWorkerResult, 1)
	for range 20 {
		_, _, governed, conflict := ingress.currentAttemptForDRC(drc)
		if governed || !conflict {
			t.Fatalf("ambiguous candidate selected: governed=%v conflict=%v", governed, conflict)
		}
	}
}

func TestAdmissionBarrierRaceHasOneOrderAndRetryBindsWinner(t *testing.T) {
	dir := t.TempDir()
	admitStore, _ := OpenResultIngressStore(dir)
	barrierStore, _ := OpenResultIngressStore(dir)
	started := openStartedAttempt(t, admitStore)
	ingress, _ := NewDurableIngress(attemptTestBinding(), admitStore)
	drc, envelope := attemptTestDRC(KindWorkerResult, 1)
	start := make(chan struct{})
	var admission AdmissionFact
	var admitErr, barrierErr error
	var racedBarrierResult AttemptAppendResult
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		admission, admitErr = ingress.Admit(context.Background(), drc, envelope)
	}()
	go func() {
		defer wg.Done()
		<-start
		run := attemptTestRunAuthority(started.Identity)
		racedBarrierResult, barrierErr = barrierStore.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity, TerminalizationID: "terminal-race", EligibilityTerminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptCompleted}})
	}()
	close(start)
	wg.Wait()
	if admitErr == nil && barrierErr == nil {
		t.Fatal("admission and stale-head barrier both won the same CAS slot")
	}
	if admitErr != nil && barrierErr != nil {
		t.Fatalf("both race sides failed: admit=%v barrier=%v", admitErr, barrierErr)
	}
	current, ok, err := barrierStore.AttemptState(started.Identity)
	if err != nil || !ok {
		t.Fatalf("state ok=%v err=%v", ok, err)
	}
	if barrierErr != nil {
		if !errors.Is(barrierErr, ErrAttemptAuthorityConflict) || admitErr != nil {
			t.Fatalf("admission-first order: admit=%v barrier=%v", admitErr, barrierErr)
		}
		run := attemptTestRunAuthority(started.Identity)
		racedBarrierResult, err = barrierStore.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, current.Revision, current.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity, TerminalizationID: "terminal-race", EligibilityTerminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptCompleted}})
		if err != nil {
			t.Fatal(err)
		}
		racedBarrier := racedBarrierResult.State
		if racedBarrier.BarrierAdmissionFactDigest != admission.FactDigest || !racedBarrier.AdmissionClosed {
			t.Fatalf("retry did not bind admitted winner: %#v", racedBarrier)
		}
	} else {
		racedBarrier := racedBarrierResult.State
		if !errors.Is(admitErr, ErrStaleLease) || !racedBarrier.AdmissionClosed {
			t.Fatalf("barrier-first order: admit=%v barrier=%#v", admitErr, racedBarrier)
		}
	}
}

func TestBarrierFirstClosesAdmissionAndStaleBarrierCASCanRetry(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	ingress, _ := NewDurableIngress(attemptTestBinding(), store)
	barrierResult := appendTestBarrier(t, store, started, "terminal-first", TerminalAttemptAborted)
	barrier := barrierResult.State
	if !barrier.AdmissionClosed {
		t.Fatalf("barrier = %#v", barrier)
	}
	drc, envelope := attemptTestDRC(KindCheckpoint, 1)
	if _, err := ingress.Admit(context.Background(), drc, envelope); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("late admit err=%v", err)
	}
	run := attemptTestRunAuthority(started.Identity)
	if _, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity, TerminalizationID: "different", EligibilityTerminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptFailed}}); !errors.Is(err, ErrAttemptAuthorityConflict) {
		t.Fatalf("stale barrier err=%v", err)
	}
}

func TestBarrierAuthorizationIsHeldForAppendAndExactReplay(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	run := attemptTestRunAuthority(started.Identity)
	transition := AttemptTransition{
		Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity,
		TerminalizationID:   "terminal-authorized",
		EligibilityTerminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptCompleted},
	}
	if _, err := store.CompareAndAppend(started.Revision, started.HeadDigest, transition); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("generic barrier err=%v", err)
	}
	if _, err := store.CompareAndAppendBarrier(context.Background(), nil, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("nil verifier err=%v", err)
	}
	wrong := run
	wrong.OrchestratorID = "second-orchestrator"
	if _, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: wrong}, transition); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("wrong orchestrator err=%v", err)
	}
	if _, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run, skip: true}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition); !errors.Is(err, ErrRunAuthorityUnauthorized) {
		t.Fatalf("verifier without held callback err=%v", err)
	}
	calls := 0
	if _, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run, calls: &calls}, started.Revision+1, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition); !errors.Is(err, ErrAttemptAuthorityConflict) || calls != 1 {
		t.Fatalf("stale revision err=%v calls=%d", err, calls)
	}
	result, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition)
	if err != nil || !result.Appended {
		t.Fatalf("fresh barrier=%#v err=%v", result, err)
	}
	calls = 0
	replay, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run, calls: &calls}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition)
	if err != nil || replay.Appended || replay.TransitionDigest != result.TransitionDigest || calls != 1 {
		t.Fatalf("replay=%#v calls=%d err=%v", replay, calls, err)
	}
}

func TestBarrierCommitsClosedTerminalEligibilityUnion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		terminal EligibilityTerminal
	}{
		{name: "completed", terminal: EligibilityTerminal{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptFailed}},
		{name: "cancelled", terminal: EligibilityTerminal{Kind: EligibilityTerminalCancelled, CancelReason: EligibilityCancelDeadlineExceeded}},
		{name: "security-revoke", terminal: EligibilityTerminal{Kind: EligibilityTerminalCancelled, CancelReason: EligibilityCancelSecurityCriticalRevoke}},
		{name: "expired", terminal: EligibilityTerminal{Kind: EligibilityTerminalExpired}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := OpenResultIngressStore(t.TempDir())
			started := openStartedAttempt(t, store)
			run := attemptTestRunAuthority(started.Identity)
			transition := AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity, TerminalizationID: "terminal-" + tc.name, EligibilityTerminal: tc.terminal}
			result, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition)
			if err != nil || result.State.EligibilityTerminal != tc.terminal || !result.State.AdmissionClosed {
				t.Fatalf("barrier=%#v err=%v", result, err)
			}
		})
	}
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	run := attemptTestRunAuthority(started.Identity)
	for _, invalid := range []EligibilityTerminal{
		{},
		{Kind: EligibilityTerminalCompleted},
		{Kind: EligibilityTerminalCompleted, CompletionReason: TerminalAttemptCompleted, CancelReason: EligibilityCancelDeadlineExceeded},
		{Kind: EligibilityTerminalCancelled, CompletionReason: TerminalAttemptFailed, CancelReason: EligibilityCancelDeadlineExceeded},
		{Kind: EligibilityTerminalExpired, CancelReason: EligibilityCancelDeadlineExceeded},
	} {
		transition := AttemptTransition{Kind: AttemptTransitionTerminalizationBarrier, Identity: started.Identity, TerminalizationID: "invalid", EligibilityTerminal: invalid}
		if _, err := store.CompareAndAppendBarrier(context.Background(), attemptRunVerifier{want: run}, started.Revision, started.HeadDigest, BarrierAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run}, transition); err == nil {
			t.Fatalf("invalid terminal union accepted: %#v", invalid)
		}
	}
}

func TestBarrierBindsBusinessResultNotLaterAuxiliaryAdmissions(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	ingress, _ := NewDurableIngress(attemptTestBinding(), store)
	resultDRC, resultEnvelope := attemptTestDRCForState(started, KindWorkerResult, 1)
	resultFact, err := ingress.Admit(context.Background(), resultDRC, resultEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	var auxiliary []struct {
		drc      DRC
		envelope ResultEnvelope
		fact     AdmissionFact
	}
	for sequence, kind := range []EnvelopeKind{KindLog, KindHeartbeat} {
		drc, envelope := attemptTestDRCForState(started, kind, uint64(sequence+2))
		fact, err := ingress.Admit(context.Background(), drc, envelope)
		if err != nil {
			t.Fatal(err)
		}
		auxiliary = append(auxiliary, struct {
			drc      DRC
			envelope ResultEnvelope
			fact     AdmissionFact
		}{drc: drc, envelope: envelope, fact: fact})
	}
	current, ok, err := store.AttemptState(started.Identity)
	if err != nil || !ok || current.CommittedResultFactDigest != resultFact.FactDigest {
		t.Fatalf("current=%#v ok=%v err=%v", current, ok, err)
	}
	barrier := appendTestBarrier(t, store, current, "terminal-business", TerminalAttemptCompleted).State
	if barrier.BarrierAdmissionFactDigest != resultFact.FactDigest {
		t.Fatalf("barrier bound auxiliary admission: %#v", barrier)
	}
	replay, err := ingress.Admit(context.Background(), resultDRC, resultEnvelope)
	if err != nil || !replay.IdempotentReplay {
		t.Fatalf("business result replay=%#v err=%v", replay, err)
	}
	for _, admission := range auxiliary {
		if _, err := ingress.Admit(context.Background(), admission.drc, admission.envelope); !errors.Is(err, ErrStaleLease) {
			t.Fatalf("auxiliary replay after barrier err=%v", err)
		}
	}
}

func TestBarrierWithoutBusinessResultClosesEmptySlot(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	ingress, _ := NewDurableIngress(attemptTestBinding(), store)
	drc, envelope := attemptTestDRCForState(started, KindLog, 1)
	if _, err := ingress.Admit(context.Background(), drc, envelope); err != nil {
		t.Fatal(err)
	}
	current, _, _ := store.AttemptState(started.Identity)
	barrier := appendTestBarrier(t, store, current, "terminal-empty", TerminalAttemptFailed).State
	if !barrier.AdmissionClosed || barrier.BarrierAdmissionFactDigest != "" || barrier.BarrierAdmissionSequence != 0 {
		t.Fatalf("barrier=%#v", barrier)
	}
	if _, err := ingress.Admit(context.Background(), drc, envelope); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("auxiliary replay after empty barrier err=%v", err)
	}
}

type attemptRunVerifier struct {
	want  RunAuthorityBinding
	err   error
	calls *int
	skip  bool
}

type attemptOwnerVerifier struct {
	want ControlOwnerAcquisition
	err  error
}

func (v attemptOwnerVerifier) WithCurrentOwnerLock(_ context.Context, got ControlOwnerAcquisition, fn func() error) error {
	if v.err != nil {
		return v.err
	}
	if got != v.want {
		return errors.New("wrong current control owner")
	}
	return fn()
}

type attemptDoubleRunVerifier struct {
	want RunAuthorityBinding
}

type attemptDeferredRunVerifier struct {
	want     RunAuthorityBinding
	deferred *func() error
}

func (v attemptDeferredRunVerifier) WithCurrentRunAuthority(_ context.Context, got RunAuthorityBinding, fn func() error) error {
	if got != v.want {
		return errors.New("wrong current Run authority")
	}
	*v.deferred = fn
	return nil
}

func (v attemptDoubleRunVerifier) WithCurrentRunAuthority(_ context.Context, got RunAuthorityBinding, fn func() error) error {
	if got != v.want {
		return errors.New("wrong current Run authority")
	}
	if err := fn(); err != nil {
		return err
	}
	return fn()
}

func (v attemptRunVerifier) WithCurrentRunAuthority(_ context.Context, got RunAuthorityBinding, fn func() error) error {
	if v.calls != nil {
		*v.calls = *v.calls + 1
	}
	if v.err != nil {
		return v.err
	}
	if got != v.want {
		return errors.New("wrong current Run authority")
	}
	if v.skip {
		return nil
	}
	return fn()
}

func TestCurrentRunVerifierCannotInvokeEffectCallbackTwice(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	barrier := appendTestBarrier(t, store, started, "terminal-double-verifier", TerminalAttemptFailed).State
	run := attemptTestRunAuthority(started.Identity)
	request := CleanupAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest, Operation: CleanupSignal}
	effectCalls := 0
	err := store.WithAuthorizedCleanup(context.Background(), attemptDoubleRunVerifier{want: run}, request, func(AttemptAuthorityState) error {
		effectCalls++
		return nil
	})
	if !errors.Is(err, ErrCleanupUnauthorized) || effectCalls != 1 {
		t.Fatalf("double verifier err=%v effectCalls=%d", err, effectCalls)
	}
}

func TestCurrentRunVerifierCannotDeferEffectCallback(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openStartedAttempt(t, store)
	barrier := appendTestBarrier(t, store, started, "terminal-deferred-verifier", TerminalAttemptFailed).State
	run := attemptTestRunAuthority(started.Identity)
	request := CleanupAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest, Operation: CleanupSignal}
	var deferred func() error
	effectCalls := 0
	err := store.WithAuthorizedCleanup(context.Background(), attemptDeferredRunVerifier{want: run, deferred: &deferred}, request, func(AttemptAuthorityState) error {
		effectCalls++
		return nil
	})
	if !errors.Is(err, ErrCleanupUnauthorized) || deferred == nil || effectCalls != 0 {
		t.Fatalf("deferred verifier err=%v callbackNil=%v effectCalls=%d", err, deferred == nil, effectCalls)
	}
	if err := deferred(); !errors.Is(err, ErrRunAuthorityUnauthorized) || effectCalls != 0 {
		t.Fatalf("late callback err=%v effectCalls=%d", err, effectCalls)
	}
}

func TestCleanupAuthorizationRejectsWrongTupleBindingOrchestratorAndRelease(t *testing.T) {
	store, _ := OpenResultIngressStore(t.TempDir())
	started := openFreshStartedAttempt(t, store)
	barrierResult := appendTestBarrier(t, store, started, "terminal-1", TerminalAttemptFailed)
	barrier := barrierResult.State
	run := RunAuthorityBinding{AuthorityNamespaceID: started.Identity.AuthorityNamespaceID, RunID: started.Identity.RunID, OrchestratorID: started.Identity.OrchestratorID, RunAuthorityDigest: started.Identity.RunAuthorityDigest}
	request := CleanupAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest, Operation: CleanupSignal}
	if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); err != nil {
		t.Fatal(err)
	}
	request.Operation = CleanupTerminate
	if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("Provider terminate before process terminal err=%v", err)
	}
	request.Operation = CleanupReconcile
	mutations := []func(*CleanupAuthorizationRequest){
		func(r *CleanupAuthorizationRequest) { r.Identity.LeaseID = "wrong" },
		func(r *CleanupAuthorizationRequest) { r.CleanupBindingDigest = attemptTestDigest("wrong") },
		func(r *CleanupAuthorizationRequest) { r.CurrentRunAuthority.OrchestratorID = "other" },
		func(r *CleanupAuthorizationRequest) { r.TerminalGeneration++ },
	}
	for _, mutate := range mutations {
		forged := request
		mutate(&forged)
		if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, forged); !errors.Is(err, ErrCleanupUnauthorized) && !errors.Is(err, ErrAttemptAuthorityUnknown) {
			t.Errorf("forged request err=%v", err)
		}
	}
	terminalTransition := AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ProcessTerminalKind: ProcessAbsent, ObservationDigest: attemptTestDigest("absent")}
	terminalResult, terminalTransition, err := appendTestProcessTerminal(t, store, barrier, request, terminalTransition)
	if err != nil {
		t.Fatal(err)
	}
	terminal := terminalResult.State
	request.Operation = CleanupSignal
	if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("process terminal still authorized Signal: %v", err)
	}
	request.Operation = CleanupTerminate
	if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); err != nil {
		t.Fatalf("Provider terminate after process terminal rejected: %v", err)
	}
	request.Operation = CleanupReconcile
	verifierCalls := 0
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run, err: errors.New("run authority drifted"), calls: &verifierCalls}, barrier.Revision, barrier.HeadDigest, request, terminalTransition); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("exact cleanup replay with authority drift err=%v", err)
	}
	if verifierCalls != 1 {
		t.Fatalf("exact cleanup replay verifier calls=%d, want 1", verifierCalls)
	}
	verifierCalls = 0
	replayedTerminal, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run, calls: &verifierCalls}, barrier.Revision, barrier.HeadDigest, request, terminalTransition)
	if err != nil || replayedTerminal.Appended || replayedTerminal.State.HeadDigest != terminal.HeadDigest || replayedTerminal.TransitionDigest != terminal.ProcessTerminalDigest || verifierCalls != 1 {
		t.Fatalf("exact cleanup replay=%#v calls=%d err=%v", replayedTerminal, verifierCalls, err)
	}
	forgedTerminal := terminalTransition
	forgedTerminal.SupervisorOutcomeFactDigest = attemptTestDigest("wrong-terminal-outcome")
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, barrier.Revision, barrier.HeadDigest, request, forgedTerminal); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("cleanup replay with wrong supervisor outcome err=%v", err)
	}
	request.Operation = CleanupTerminate
	terminal, terminateReceiptDigest := appendTestAcceptedTerminate(t, store, terminal)
	allocationResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, terminal.Revision, terminal.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ReceiptDigest: terminateReceiptDigest})
	if err != nil {
		t.Fatal(err)
	}
	allocation := allocationResult.State
	if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("allocation terminal retained Provider effect: %v", err)
	}
	request.Operation = CleanupReconcile
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, barrier.Revision, barrier.HeadDigest, request, terminalTransition); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("allocation-terminal phase replayed old process terminal: %v", err)
	}
	closed := appendTestSupervisorClosed(t, store, allocation, request)
	cleanedResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, closed.Revision, closed.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionCleanupCompleted, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, SupervisorClosedFactDigest: closed.SupervisorClosedDigest})
	if err != nil {
		t.Fatal(err)
	}
	cleaned := cleanedResult.State
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, terminal.Revision, terminal.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ReceiptDigest: attemptTestDigest("allocation")}); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("cleanup-completed phase replayed old allocation terminal: %v", err)
	}
	releasedResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, cleaned.Revision, cleaned.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionCleanupReleased, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID})
	if err != nil || releasedResult.State.CleanupReleasedDigest == "" {
		t.Fatalf("released=%#v err=%v", releasedResult, err)
	}
	if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("released binding err=%v", err)
	}
	releaseTransition := AttemptTransition{Kind: AttemptTransitionCleanupReleased, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID}
	verifierCalls = 0
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run, err: errors.New("run authority drifted"), calls: &verifierCalls}, cleaned.Revision, cleaned.HeadDigest, request, releaseTransition); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("released replay with authority drift err=%v", err)
	}
	if verifierCalls != 1 {
		t.Fatalf("released replay verifier calls=%d, want 1", verifierCalls)
	}
	verifierCalls = 0
	releasedReplay, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run, calls: &verifierCalls}, cleaned.Revision, cleaned.HeadDigest, request, releaseTransition)
	if err != nil || releasedReplay.Appended || releasedReplay.TransitionDigest != releasedResult.TransitionDigest {
		t.Fatalf("released exact replay=%#v err=%v", releasedReplay, err)
	}
	if verifierCalls != 1 {
		t.Fatalf("released replay verifier calls=%d, want 1", verifierCalls)
	}
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, releasedResult.State.Revision, releasedResult.State.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionCleanupCompleted, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID}); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("released binding authorized cleanup re-effect: %v", err)
	}
	forgedRelease := releaseTransition
	forgedRelease.TerminalizationID = "different"
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, cleaned.Revision, cleaned.HeadDigest, request, forgedRelease); !errors.Is(err, ErrCleanupUnauthorized) {
		t.Fatalf("different release replay err=%v", err)
	}
	pending, err := store.PendingAttemptStates()
	if err != nil || len(pending) != 0 {
		t.Fatalf("released attempt remained pending: %#v err=%v", pending, err)
	}
}

func TestLaunchUncertainCleanupAllowsHeldProcessSignalButNotProviderTerminate(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := attemptTestIdentity()
	openedResult, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: id})
	if err != nil {
		t.Fatal(err)
	}
	opened := openedResult.State
	opened = appendTestAcceptedProvision(t, store, opened)
	authorizedResult, err := appendAuthorizedAttempt(t, store, opened.Revision, opened.HeadDigest, AttemptTransition{Kind: AttemptTransitionLaunchAuthorized, Identity: id, LaunchAuthorizationID: "launch-uncertain"})
	if err != nil {
		t.Fatal(err)
	}
	authorized := authorizedResult.State
	barrierResult := appendTestBarrier(t, store, authorized, "terminal-uncertain", TerminalOrphanReconciled)
	barrier := barrierResult.State
	run := RunAuthorityBinding{AuthorityNamespaceID: id.AuthorityNamespaceID, RunID: id.RunID, OrchestratorID: id.OrchestratorID, RunAuthorityDigest: id.RunAuthorityDigest}
	request := CleanupAuthorizationRequest{Identity: id, CurrentRunAuthority: run, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest}
	for _, operation := range []CleanupOperation{CleanupInspect, CleanupReconcile, CleanupSignal} {
		request.Operation = operation
		if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); err != nil {
			t.Fatalf("operation %q rejected: %v", operation, err)
		}
	}
	for _, operation := range []CleanupOperation{CleanupTerminate} {
		request.Operation = operation
		if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); !errors.Is(err, ErrCleanupUnauthorized) {
			t.Fatalf("operation %q err=%v, want ErrCleanupUnauthorized", operation, err)
		}
	}
	request.Operation = CleanupReconcile
	absentResult, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, barrier.Revision, barrier.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: id, TerminalizationID: barrier.TerminalizationID, ProcessTerminalKind: ProcessAbsent, ObservationDigest: attemptTestDigest("launch-uncertain-absent")})
	if err != nil || absentResult.State.ProcessTerminalKind != ProcessAbsent {
		t.Fatalf("reconciled absence=%#v err=%v", absentResult, err)
	}
	terminated, terminateReceiptDigest := appendTestAcceptedTerminate(t, store, absentResult.State)
	request.Operation = CleanupTerminate
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, terminated.Revision, terminated.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: id, TerminalizationID: barrier.TerminalizationID, ReceiptDigest: terminateReceiptDigest}); err != nil {
		t.Fatalf("launch-uncertain absence did not unblock allocation cleanup: %v", err)
	}
}

func TestProcessIdentityConflictPermanentlyBlocksKillAndCompletion(t *testing.T) {
	store, err := OpenResultIngressStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	started := openStartedAttempt(t, store)
	barrierResult := appendTestBarrier(t, store, started, "terminal-conflict", TerminalOrphanReconciled)
	barrier := barrierResult.State
	run := RunAuthorityBinding{AuthorityNamespaceID: started.Identity.AuthorityNamespaceID, RunID: started.Identity.RunID, OrchestratorID: started.Identity.OrchestratorID, RunAuthorityDigest: started.Identity.RunAuthorityDigest}
	request := CleanupAuthorizationRequest{Identity: started.Identity, CurrentRunAuthority: run, TerminalizationID: barrier.TerminalizationID, TerminalGeneration: barrier.TerminalGeneration, CleanupBindingDigest: barrier.CleanupBindingDigest, Operation: CleanupInspect}
	conflictResult, _, err := appendTestProcessTerminal(t, store, barrier, request, AttemptTransition{Kind: AttemptTransitionProcessTerminal, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ProcessTerminalKind: ProcessIdentityConflict, ObservationDigest: attemptTestDigest("identity-conflict")})
	if err != nil {
		t.Fatal(err)
	}
	conflict := conflictResult.State
	for _, operation := range []CleanupOperation{CleanupSignal, CleanupTerminate} {
		request.Operation = operation
		if err := store.AuthorizeCleanup(context.Background(), attemptRunVerifier{want: run}, request); !errors.Is(err, ErrCleanupUnauthorized) {
			t.Fatalf("identity conflict operation %q err=%v", operation, err)
		}
	}
	request.Operation = CleanupReconcile
	if _, err := store.CompareAndAppendCleanup(context.Background(), attemptRunVerifier{want: run}, conflict.Revision, conflict.HeadDigest, request, AttemptTransition{Kind: AttemptTransitionAllocationTerminated, Identity: started.Identity, TerminalizationID: barrier.TerminalizationID, ReceiptDigest: attemptTestDigest("forged-allocation-terminal")}); !errors.Is(err, ErrAttemptAuthorityOrder) {
		t.Fatalf("identity conflict advanced to allocation terminal: %v", err)
	}
}

func TestAttemptStateEnumerationIsDeterministicAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	store, _ := OpenResultIngressStore(dir)
	first := attemptTestIdentity()
	second := attemptTestIdentity()
	second.AttemptID, second.AllocationID, second.LeaseID = "attempt-2", "allocation-2", "lease-2"
	second.LeaseDigest = attemptTestDigest("lease-2")
	for _, identity := range []AttemptIdentity{second, first} {
		if _, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: identity}); err != nil {
			t.Fatal(err)
		}
	}
	states, err := store.AttemptStates()
	if err != nil || len(states) != 2 {
		t.Fatalf("states=%#v err=%v", states, err)
	}
	firstKey, _ := first.Key()
	secondKey, _ := second.Key()
	wantFirst := first
	if secondKey < firstKey {
		wantFirst = second
	}
	if states[0].Identity != wantFirst {
		t.Fatalf("enumeration is not key ordered: %#v", states)
	}
	reopened, err := OpenResultIngressStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.PendingAttemptStates()
	if err != nil || len(recovered) != len(states) || !reflect.DeepEqual(recovered[0], states[0]) || !reflect.DeepEqual(recovered[1], states[1]) {
		t.Fatalf("recovered=%#v states=%#v err=%v", recovered, states, err)
	}
}

func TestAttemptAuthorityCorruptTruncatedDuplicateAndReorderFailClosed(t *testing.T) {
	build := func(t *testing.T) (string, []byte) {
		t.Helper()
		dir := t.TempDir()
		store, _ := OpenResultIngressStore(dir)
		_ = func() error {
			_, err := appendAuthorizedAttempt(t, store, 0, "", AttemptTransition{Kind: AttemptTransitionOpened, Identity: attemptTestIdentity()})
			return err
		}()
		raw, err := os.ReadFile(filepath.Join(dir, resultIngressStoreFileName))
		if err != nil {
			t.Fatal(err)
		}
		return dir, raw
	}
	for _, test := range []struct {
		name string
		edit func([]byte) []byte
	}{
		{"truncated", func(raw []byte) []byte { return raw[:len(raw)-1] }},
		{"corrupt", func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), "attempt-opened", "attempt-broken", 1))
		}},
		{"duplicate", func(raw []byte) []byte { return append(append([]byte{}, raw...), raw...) }},
		{"trailing-json-value", func(raw []byte) []byte { return append(bytes.TrimSpace(raw), []byte("{}\n")...) }},
		{"reorder", func(raw []byte) []byte {
			return []byte(strings.Replace(string(raw), `"revision":1`, `"revision":2`, 1))
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, raw := build(t)
			if err := os.WriteFile(filepath.Join(dir, resultIngressStoreFileName), test.edit(raw), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := OpenResultIngressStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.AttemptState(attemptTestIdentity()); err == nil {
				t.Fatal("corrupt authority unexpectedly replayed")
			}
		})
	}
}
