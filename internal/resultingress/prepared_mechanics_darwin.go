//go:build darwin && arm64

package resultingress

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"golang.org/x/sys/unix"
)

// reconcilePreparedExecutionLocked is the concrete S1 fresh-start state
// machine. The caller holds the current repository owner and ResultIngress
// ledger locks. Exact completed resume is replay-only; partial mechanics are
// left intact for the later Attach/intervention slice and are never guessed.
func (s *DurableStore) reconcilePreparedExecutionLocked(ctx context.Context, projection *Ingress, prepared PreparedExecutionV1, state AttemptAuthorityState) (AttemptAuthorityState, error) {
	profile, ok := s.preparedDarwin.(*preparedDarwinExecutionProfile)
	if !ok || profile == nil || profile.controlRoot == nil || ctx == nil || projection == nil {
		return AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
	}
	if err := requireNoActiveLegacySupervisor(projection); err != nil {
		return AttemptAuthorityState{}, err
	}
	if _, err := exactSuccessfulResume(state); err == nil {
		return state, nil
	}
	if state.HeadDigest != prepared.LaunchAuthorizedFactDigest || state.SupervisorBootstrapDigest != "" || state.SupervisorStartedDigest != "" || state.SupervisorPendingIntentDigest != "" || len(state.SupervisorCommandCheckpoints) != 0 || state.ProcessStartedDigest != "" {
		return AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
	}
	closure, source, err := s.verifyPreparedCurrentSourcesLocked(projection, prepared, state)
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	client, state, err := s.startPreparedSupervisorLocked(ctx, projection, state)
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	defer client.Disconnect()

	bindPayload := processsupervisor.BindAuthorityPayload{
		SupervisorStartedFactDigest: state.SupervisorStartedDigest,
		OwnerEpoch:                  state.Owner.OwnerEpoch, PreviousAuthorityHead: client.Anchor().Binding.CurrentAuthorityHead,
		AuthorityHead: state.SupervisorStartedDigest,
	}
	state, bindOutcomeDigest, err := s.executePreparedCommandLocked(ctx, projection, state, client, processsupervisor.CommandBindAuthority, bindPayload)
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	spawnPayload, err := preparedSpawnPayload(state, closure, source)
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	state, spawnOutcomeDigest, err := s.executePreparedCommandLocked(ctx, projection, state, client, processsupervisor.CommandSpawn, spawnPayload)
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	spawnEvidence, found := supervisorCheckpointEvidence(state, spawnOutcomeDigest)
	if !found || spawnEvidence.Command != processsupervisor.CommandSpawn || spawnEvidence.Disposition != "ok" || spawnEvidence.ReasonCode != "process-exec-stopped" || spawnEvidence.Outcome.State != SupervisorProcessExecStopped || spawnEvidence.Outcome.SourceGateRevision != processsupervisor.SourceGateRevisionV1 || requireDigest("exactSetDigest", spawnEvidence.Outcome.ExactSetDigest) != nil {
		return AttemptAuthorityState{}, ErrPreparedExecutionConflict
	}
	observation, err := preparedProcessObservation(closure, source, spawnEvidence.Outcome)
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	transition := AttemptTransition{
		Kind: AttemptTransitionProcessStarted, Identity: state.Identity,
		CommandID: spawnEvidence.CommandID, ObservedAt: spawnEvidence.Outcome.ObservedAt, Process: observation,
		LaunchMaterialsDigest: state.LaunchMaterialsDigest, AgentLaunchSpecDigest: state.AgentLaunchSpecDigest,
		SupervisorBindOutcomeFactDigest: bindOutcomeDigest, SupervisorOutcomeFactDigest: spawnOutcomeDigest,
	}
	state, _, err = s.appendPreparedAttemptTransitionLocked(projection, state, transition)
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	state, _, err = s.executePreparedCommandLocked(ctx, projection, state, client, processsupervisor.CommandResume, processsupervisor.ResumePayload{ProcessStartedFactDigest: state.ProcessStartedDigest})
	if err != nil {
		return AttemptAuthorityState{}, err
	}
	if _, err := exactSuccessfulResume(state); err != nil {
		return AttemptAuthorityState{}, err
	}
	return state, nil
}

// preparedAllocationSource is the private, non-persistent normalization of
// the durable allocation authority that satisfies Darwin prepared source
// verification and spawn construction. The durable path A receipt has an
// exact live identity. The durable path B receipt has an APFS-stable identity
// whose size/link count are normalized; after the stable current-name check,
// path B is upgraded to the freshly observed exact live identity. It is never
// persisted, carries no bearer token, and exposes no path beyond the closure's
// own working directory. The supervisor therefore still receives exact,
// mutation-adjacent source-gate values on both paths.
type preparedAllocationSource struct {
	liveIdentity launchidentity.LiveIdentity
}

func (s *DurableStore) verifyPreparedCurrentSourcesLocked(projection *Ingress, prepared PreparedExecutionV1, state AttemptAuthorityState) (launchidentity.ClosureV1, preparedAllocationSource, error) {
	var directoryIdentity allocationcontrol.ObjectIdentityV1
	stableDirectory := false
	if prepared.AllocationProvisionReceiptFactDigest != "" {
		// Path A: recheck the durable AllocationProvision applied receipt and
		// copy its live directory identity. No existing-worktree receipt is
		// consulted.
		_, receipt, err := currentPreparedProvisionReceipt(projection, state)
		if err != nil {
			return launchidentity.ClosureV1{}, preparedAllocationSource{}, err
		}
		directoryIdentity = receipt.LiveIdentity
	} else {
		// Path B: recheck the durable existing-worktree bind receipt and copy
		// the exact directory identity observed at admission. No provision
		// receipt is fabricated; the current ledger bind receipt is the only
		// allocation authority and its fact/receipt digests were already
		// rechecked against the prepared execution by resolvePreparedCurrent.
		worktree, err := currentPreparedExistingWorktreeBindReceipt(projection, state)
		if err != nil {
			return launchidentity.ClosureV1{}, preparedAllocationSource{}, err
		}
		directoryIdentity = worktree.receipt.Observation.TargetCurrentName.ObjectIdentity
		stableDirectory = true
	}
	closure, err := state.LaunchClosure.Closure()
	if err != nil {
		return launchidentity.ClosureV1{}, preparedAllocationSource{}, ErrPreparedExecutionConflict
	}
	live, err := preparedAllocationLiveIdentity(directoryIdentity)
	if err != nil {
		return launchidentity.ClosureV1{}, preparedAllocationSource{}, err
	}
	var observed launchidentity.CurrentClosureObservation
	if stableDirectory {
		observed, err = launchidentity.VerifyCurrentClosureWithStableDirectoryIdentity(closure, live)
	} else {
		observed, err = launchidentity.VerifyCurrentClosure(closure, live)
	}
	if err != nil || observed.LaunchMaterialsDigest != prepared.LaunchMaterialsDigest || observed.AgentLaunchSpecDigest != prepared.AgentLaunchSpecDigest || observed.Pi0844IdentityDigest != prepared.Pi0844IdentityDigest || observed.WorkingDirectory.CanonicalPath != closure.WorkingDirectory {
		return launchidentity.ClosureV1{}, preparedAllocationSource{}, ErrPreparedExecutionUnavailable
	}
	identity, err := launchidentity.Pi0844IdentityFromClosure(closure)
	if err != nil || identity.IdentityDigest != prepared.Pi0844IdentityDigest {
		return launchidentity.ClosureV1{}, preparedAllocationSource{}, ErrPreparedExecutionConflict
	}
	return closure, preparedAllocationSource{liveIdentity: launchidentity.LiveIdentityFromObject(observed.WorkingDirectory)}, nil
}

// New-session admission is deliberately separate from historical replay. An
// old Supervisor must be drained by its original bytes, not adopted by v2.
func requireNoActiveLegacySupervisor(projection *Ingress) error {
	if projection == nil {
		return ErrPreparedExecutionConflict
	}
	for _, state := range projection.attempts {
		legacy := state.SupervisorBootstrapDigest != "" && state.SupervisorBootstrap.Request.Generation == (processsupervisor.ProtocolGenerationContract{}) ||
			state.SupervisorStartedDigest != "" && state.SupervisorStarted.V2 == (SupervisorStartedV2{})
		if legacy && (state.CleanupReleasedDigest == "" || state.SupervisorPendingIntentDigest != "") {
			return ErrPreparedExecutionUnavailable
		}
	}
	return nil
}

func (s *DurableStore) startPreparedSupervisorLocked(ctx context.Context, projection *Ingress, state AttemptAuthorityState) (*processsupervisor.ClientV2, AttemptAuthorityState, error) {
	profile, ok := s.preparedDarwin.(*preparedDarwinExecutionProfile)
	if !ok || profile == nil || profile.controlRoot == nil {
		return nil, AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
	}
	currentCore, err := processsupervisor.ObserveCurrentCore(profile.fixedMarshalPath)
	if err != nil || currentCore != profile.core {
		return nil, AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
	}
	rootIdentity, err := processsupervisor.ObserveHeldControlDirectory(profile.controlRoot)
	if err != nil || !sameStableControlDirectoryIdentity(rootIdentity, profile.controlIdentity) {
		return nil, AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
	}
	sessionID, nonce, err := newPreparedSessionIdentity()
	if err != nil {
		return nil, AttemptAuthorityState{}, err
	}
	if err := unix.Mkdirat(int(profile.controlRoot.Fd()), sessionID, 0o700); err != nil {
		return nil, AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
	}
	directoryFD, err := unix.Openat(int(profile.controlRoot.Fd()), sessionID, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
	}
	directory := os.NewFile(uintptr(directoryFD), "marshal-prepared-control-directory")
	if directory == nil {
		_ = unix.Close(directoryFD)
		return nil, AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
	}
	defer directory.Close()
	directoryIdentity, err := processsupervisor.ObserveHeldControlDirectory(directory)
	if err != nil || directoryIdentity.UID != profile.controlIdentity.UID || directoryIdentity.GID != profile.controlIdentity.GID || directoryIdentity.Mode&0o777 != 0o700 {
		return nil, AttemptAuthorityState{}, ErrPreparedExecutionUnavailable
	}
	generation := processsupervisor.DormantV2ProtocolContract()
	request := processsupervisor.BootstrapRequestV2{
		SchemaVersion: generation.BootstrapSchema, ProtocolRevision: generation.ProtocolRevision,
		LaunchChildProtocolRevision: generation.LaunchChildProtocolRevision, MechanicsIdentity: generation.MechanicsIdentity,
		SessionID: sessionID, SessionNonce: nonce, OwnerEpoch: state.Owner.OwnerEpoch,
		Authority: supervisorAuthorityTuple(state.Identity), LaunchAuthorizedFact: state.LaunchAuthorizedDigest,
		CurrentAuthorityHead: state.HeadDigest, ControlDirectoryIdentity: directoryIdentity, Core: currentCore,
	}
	preparedBootstrap, err := NewSupervisorBootstrapPreparedV2(state.Owner, request)
	if err != nil {
		return nil, AttemptAuthorityState{}, err
	}
	state, _, err = s.appendPreparedAttemptTransitionLocked(projection, state, AttemptTransition{Kind: AttemptTransitionSupervisorBootstrap, Identity: state.Identity, SupervisorBootstrap: preparedBootstrap})
	if err != nil {
		return nil, AttemptAuthorityState{}, err
	}
	client, err := processsupervisor.StartV2(ctx, processsupervisor.StartOptionsV2{FixedMarshalPath: profile.fixedMarshalPath, ControlDirectory: directory, Bootstrap: request})
	if err != nil {
		return nil, AttemptAuthorityState{}, err
	}
	anchor, handshake := client.Anchor(), client.Handshake()
	observedPeer := processsupervisor.CoreIdentity{UID: anchor.Binding.UID, GID: anchor.Binding.GID, Process: handshake.SupervisorProcess, Binary: handshake.SupervisorBinary}
	started, err := NewProcessSupervisorStartedV2FromBootstrap(state.SupervisorBootstrapDigest, preparedBootstrap, handshake, anchor, observedPeer)
	if err != nil {
		_ = client.Disconnect()
		return nil, AttemptAuthorityState{}, err
	}
	state, _, err = s.appendPreparedAttemptTransitionLocked(projection, state, AttemptTransition{Kind: AttemptTransitionProcessSupervisorStarted, Identity: state.Identity, SupervisorStarted: started})
	if err != nil {
		_ = client.Disconnect()
		return nil, AttemptAuthorityState{}, err
	}
	return client, state, nil
}

type preparedCommandClientV2 interface {
	Anchor() processsupervisor.SessionAnchorV2
	Prepare(processsupervisor.CommandOptions, any) (processsupervisor.PreparedCommandV2, error)
	DoPrepared(context.Context, processsupervisor.PreparedCommandV2) (processsupervisor.VerifiedCommandOutcomeV2, error)
}

func (s *DurableStore) executePreparedCommandLocked(ctx context.Context, projection *Ingress, state AttemptAuthorityState, client preparedCommandClientV2, command processsupervisor.CommandName, payload any) (AttemptAuthorityState, string, error) {
	if state.SupervisorPendingIntentDigest != "" || client == nil {
		return AttemptAuthorityState{}, "", ErrPreparedExecutionConflict
	}
	currentAuthorityHead := state.HeadDigest
	if command == processsupervisor.CommandBindAuthority || command == processsupervisor.CommandSpawn {
		currentAuthorityHead = client.Anchor().Binding.CurrentAuthorityHead
	}
	prepared, err := client.Prepare(processsupervisor.CommandOptions{
		Command: command, CommandID: fmt.Sprintf("prepared-%s-%d", command, state.SupervisorCommandSequence+1),
		Sequence: state.SupervisorCommandSequence + 1, PreviousCommandDigest: state.SupervisorCommandHead,
		CurrentAuthorityHead: currentAuthorityHead, Deadline: s.authorityNow().Add(preparedCommandTimeout(command)),
	}, payload)
	if err != nil {
		return AttemptAuthorityState{}, "", err
	}
	intent, err := NewSupervisorCommandIntentV2(prepared.Evidence())
	if err != nil {
		return AttemptAuthorityState{}, "", err
	}
	state, _, err = s.appendPreparedSupervisorIntentLocked(projection, state, intent)
	if err != nil {
		return AttemptAuthorityState{}, "", err
	}
	verified, err := client.DoPrepared(ctx, prepared)
	if err != nil {
		return AttemptAuthorityState{}, "", err
	}
	evidence, err := NewSupervisorCommandEvidenceV2(verified)
	if err != nil {
		return AttemptAuthorityState{}, "", err
	}
	return s.appendPreparedSupervisorOutcomeLocked(projection, state, evidence)
}

func (s *DurableStore) appendPreparedAttemptTransitionLocked(projection *Ingress, state AttemptAuthorityState, transition AttemptTransition) (AttemptAuthorityState, string, error) {
	if projection == nil || state.ProtocolRevision != attemptAuthorityProtocolV2 || state.OpenedSchemaRevision != attemptOpenedSchemaV2 || transition.Identity != state.Identity || validateTransitionShape(transition) != nil {
		return AttemptAuthorityState{}, "", ErrPreparedExecutionConflict
	}
	key, err := state.Identity.Key()
	current, found := projection.attempts[key]
	if err != nil || !found || !samePreparedAuthorityState(current, state) {
		return AttemptAuthorityState{}, "", ErrPreparedExecutionConflict
	}
	if err := validateSupervisorTransitionAgainstProjection(projection, state, true, transition, false); err != nil {
		return AttemptAuthorityState{}, "", err
	}
	fact := &attemptAuthorityFact{ProtocolRevision: state.ProtocolRevision, FactType: string(transition.Kind), Sequence: s.nextSequence, AttemptKey: key, Revision: state.Revision + 1, PreviousDigest: state.HeadDigest, Transition: transition}
	if err := prepareAttemptFact(state, true, fact, false); err != nil {
		return AttemptAuthorityState{}, "", err
	}
	// Fresh prepared mechanics must prove that the exact bytes about to be
	// appended are accepted by the same projector used during cold replay.
	// Run this before append/fsync so a schema/protocol/cross-fact mismatch can
	// never leave an unreplayable durable prefix.
	preflightDigest, err := canonicalDigest(fact)
	if err != nil {
		return AttemptAuthorityState{}, "", err
	}
	fact.Digest = preflightDigest
	if err := applyAttemptAuthorityFactValue(*fact, projection, false); err != nil {
		return AttemptAuthorityState{}, "", err
	}
	fact.Digest = ""
	if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
		return AttemptAuthorityState{}, "", err
	}
	if fact.Digest != preflightDigest {
		return AttemptAuthorityState{}, "", fmt.Errorf("%w: prepared Attempt preflight digest drift", ErrPreparedExecutionConflict)
	}
	s.nextSequence++
	return projection.attempts[key], fact.Digest, nil
}

func (s *DurableStore) appendPreparedSupervisorIntentLocked(projection *Ingress, state AttemptAuthorityState, intent SupervisorCommandIntent) (AttemptAuthorityState, string, error) {
	key, err := state.Identity.Key()
	current, found := projection.attempts[key]
	if err != nil || !found || !samePreparedAuthorityState(current, state) || state.SupervisorPendingIntentDigest != "" || validateSupervisorCommandIntentAgainstState(state, intent) != nil {
		return AttemptAuthorityState{}, "", ErrPreparedExecutionConflict
	}
	fact := &supervisorCommandFact{ProtocolRevision: supervisorIntentRecoveryRevision(intent), FactType: supervisorCommandIntentFactType, Sequence: s.nextSequence, AttemptKey: key, AttemptRevision: state.Revision, AttemptAuthorityHead: state.HeadDigest, PreviousRecoveryFactDigest: state.SupervisorCommandRecoveryHead, Intent: intent}
	if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
		return AttemptAuthorityState{}, "", err
	}
	s.nextSequence++
	if err := applySupervisorCommandFactValue(*fact, projection); err != nil {
		return AttemptAuthorityState{}, "", fmt.Errorf("resultingress: appended prepared supervisor intent failed projection: %w", err)
	}
	return projection.attempts[key], fact.Digest, nil
}

func (s *DurableStore) appendPreparedSupervisorOutcomeLocked(projection *Ingress, state AttemptAuthorityState, outcome SupervisorCommandEvidence) (AttemptAuthorityState, string, error) {
	key, err := state.Identity.Key()
	current, found := projection.attempts[key]
	if err != nil || !found || !samePreparedAuthorityState(current, state) || state.SupervisorPendingIntentDigest == "" || validateSupervisorCommandOutcomeAgainstIntent(state, outcome) != nil {
		return AttemptAuthorityState{}, "", ErrPreparedExecutionConflict
	}
	fact := &supervisorCommandFact{ProtocolRevision: supervisorOutcomeRecoveryRevision(outcome), FactType: supervisorCommandOutcomeFactType, Sequence: s.nextSequence, AttemptKey: key, AttemptRevision: state.Revision, AttemptAuthorityHead: state.HeadDigest, PreviousRecoveryFactDigest: state.SupervisorCommandRecoveryHead, Outcome: outcome}
	if err := s.appendLine(fact, func() string { return fact.Digest }, func(value string) { fact.Digest = value }); err != nil {
		return AttemptAuthorityState{}, "", err
	}
	s.nextSequence++
	if err := applySupervisorCommandFactValue(*fact, projection); err != nil {
		return AttemptAuthorityState{}, "", fmt.Errorf("resultingress: appended prepared supervisor outcome failed projection: %w", err)
	}
	return projection.attempts[key], fact.Digest, nil
}

func preparedSpawnPayload(state AttemptAuthorityState, closure launchidentity.ClosureV1, source preparedAllocationSource) (processsupervisor.SpawnPayload, error) {
	live, err := preparedSupervisorAllocationLiveIdentity(source.liveIdentity)
	if err != nil {
		return processsupervisor.SpawnPayload{}, err
	}
	keys := make([]string, 0, len(closure.Environment))
	for _, item := range closure.Environment {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			return processsupervisor.SpawnPayload{}, ErrPreparedExecutionConflict
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	argvDigest, err := canonicalDigest(closure.Arguments)
	if err != nil {
		return processsupervisor.SpawnPayload{}, err
	}
	environmentDigest, err := canonicalDigest(closure.Environment)
	if err != nil {
		return processsupervisor.SpawnPayload{}, err
	}
	working := launchidentity.ObjectV1{CanonicalPath: closure.WorkingDirectory, Device: live.Device, Inode: live.Inode, FileType: POSIXFileTypeDirectory, Mode: live.Mode, UID: live.UID, GID: live.GID, Size: live.Size, LinkCount: live.LinkCount}
	return processsupervisor.SpawnPayload{
		LaunchAuthorizedFactDigest: state.LaunchAuthorizedDigest, SupervisorStartedFactDigest: state.SupervisorStartedDigest,
		Runtime: preparedHeldObjectSpec("runtime", closure.RuntimeExecutable, "regular"), WorkingDirectory: preparedHeldObjectSpec("working-directory", working, "directory"),
		SourceGateRevision: processsupervisor.SourceGateRevisionV1, AllocationLiveIdentity: &live,
		ClosureProfileID: closure.ClosureProfileID, MaterialRoots: append([]launchidentity.MaterialRootV1(nil), closure.MaterialRoots...), LaunchMaterials: append([]launchidentity.LaunchMaterialV1(nil), closure.LaunchMaterials...),
		LaunchMaterialsDigest: closure.LaunchMaterialsDigest, AgentLaunchSpecDigest: closure.AgentLaunchSpecDigest,
		ArgvDigest: argvDigest, EnvironmentDigest: environmentDigest, StdinDigest: canonical.DigestBytes(nil),
		EnvironmentKeys: keys, Argv: append([]string{}, closure.Arguments...), Environment: append([]string{}, closure.Environment...), Stdin: []byte{},
	}, nil
}

func preparedHeldObjectSpec(role string, object launchidentity.ObjectV1, kind string) processsupervisor.HeldObjectSpec {
	return processsupervisor.HeldObjectSpec{Role: role, CanonicalPath: object.CanonicalPath, Device: object.Device, Inode: object.Inode, FileType: kind, UID: object.UID, GID: object.GID, Mode: object.Mode, LinkCount: object.LinkCount, Size: object.Size, RawSHA256: object.RawSHA256}
}

func preparedProcessObservation(closure launchidentity.ClosureV1, source preparedAllocationSource, outcome SupervisorProcessOutcome) (ProcessObservation, error) {
	working := source.liveIdentity
	if working.Validate() != nil {
		return ProcessObservation{}, ErrPreparedExecutionConflict
	}
	// The authenticated report binds cwd through WorkingObjectDigest and the
	// exact-set digest. The persisted object fields come from the current live
	// directory observation verified immediately before spawn; path B first
	// binds that observation to the stable RB1 receipt identity.
	return SealProcessObservation(ProcessObservation{
		PID: outcome.Process.PID, PGID: outcome.Process.ProcessGroupID, BirthSeconds: outcome.Process.BirthSeconds, BirthMicroseconds: outcome.Process.BirthMicroseconds,
		WorkingDirectory: closure.WorkingDirectory, WorkingDirectoryDevice: working.Device, WorkingDirectoryInode: working.Inode,
		WorkingDirectoryType: POSIXFileTypeDirectory, WorkingDirectoryOwner: working.UID, WorkingDirectoryMode: working.Mode,
		ExecutablePath: closure.RuntimeExecutable.CanonicalPath, ExecutableDevice: closure.RuntimeExecutable.Device, ExecutableInode: closure.RuntimeExecutable.Inode,
		ExecutableSize: closure.RuntimeExecutable.Size, ExecutableType: POSIXFileTypeRegular, ExecutableOwner: closure.RuntimeExecutable.UID, ExecutableGroup: closure.RuntimeExecutable.GID,
		ExecutableMode: closure.RuntimeExecutable.Mode, ExecutableLinkCount: closure.RuntimeExecutable.LinkCount, ExecutableSHA256: closure.RuntimeExecutable.RawSHA256,
		ObserverIdentity: outcome.ObserverIdentity,
	})
}

func preparedAllocationLiveIdentity(identity allocationcontrol.ObjectIdentityV1) (launchidentity.LiveIdentity, error) {
	device, err := strconv.ParseUint(identity.Device, 10, 64)
	if err != nil {
		return launchidentity.LiveIdentity{}, ErrPreparedExecutionConflict
	}
	inode, err := strconv.ParseUint(identity.Inode, 10, 64)
	if err != nil || identity.Validate(allocationcontrol.ObjectTypeDirectory) != nil {
		return launchidentity.LiveIdentity{}, ErrPreparedExecutionConflict
	}
	live := launchidentity.LiveIdentity{Device: device, Inode: inode, FileType: POSIXFileTypeDirectory, Mode: identity.Mode, UID: identity.UID, GID: identity.GID, Size: identity.Size, LinkCount: identity.Nlink}
	if live.Validate() != nil {
		return launchidentity.LiveIdentity{}, ErrPreparedExecutionConflict
	}
	return live, nil
}

func preparedSupervisorAllocationLiveIdentity(live launchidentity.LiveIdentity) (processsupervisor.AllocationLiveIdentity, error) {
	if live.Validate() != nil {
		return processsupervisor.AllocationLiveIdentity{}, ErrPreparedExecutionConflict
	}
	return processsupervisor.AllocationLiveIdentity{Device: live.Device, Inode: live.Inode, FileType: "directory", UID: live.UID, GID: live.GID, Mode: live.Mode, LinkCount: live.LinkCount, Size: live.Size}, nil
}

func preparedCommandTimeout(command processsupervisor.CommandName) time.Duration {
	if command == processsupervisor.CommandSpawn {
		return 2 * time.Minute
	}
	return 30 * time.Second
}

func samePreparedAuthorityState(left, right AttemptAuthorityState) bool {
	leftDigest, leftErr := canonicalDigest(left)
	rightDigest, rightErr := canonicalDigest(right)
	return leftErr == nil && rightErr == nil && leftDigest == rightDigest
}

func newPreparedSessionIdentity() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", ErrPreparedExecutionUnavailable
	}
	encoded := hex.EncodeToString(raw)
	return "prepared-" + encoded[:24], encoded, nil
}
