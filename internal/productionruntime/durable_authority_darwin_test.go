//go:build darwin && arm64

package productionruntime

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/resultingress"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

const (
	posixFileRegular   uint32 = 0o100000
	posixFileDirectory uint32 = 0o040000
)

type compositionFixture struct {
	inputs      CompositionInputs
	acquisition resultingress.ControlOwnerAcquisition
	ledger      *CompositionLedger
	runID       string
}

func compositionTestClosure(t *testing.T) launchidentity.ClosureV1 {
	t.Helper()
	object := func(path string, inode uint64, size int64, executable bool) launchidentity.ObjectV1 {
		mode := uint32(posixFileRegular | 0o644)
		if executable {
			mode = uint32(posixFileRegular | 0o755)
		}
		return launchidentity.ObjectV1{CanonicalPath: path, Device: 1, Inode: inode, FileType: posixFileRegular, Mode: mode, UID: 501, GID: 20, Size: size, LinkCount: 1, RawSHA256: canonical.DigestBytes([]byte(path))}
	}
	roots := []launchidentity.MaterialRootV1{
		{Name: "photon-node", CanonicalPath: "/fixed/pi/photon", PackageRelative: "node_modules/@silvia-odwyer/photon-node", Object: launchidentity.ObjectV1{CanonicalPath: "/fixed/pi/photon", Device: 1, Inode: 10, FileType: posixFileDirectory, Mode: uint32(posixFileDirectory) | 0o755, UID: 501, GID: 20, LinkCount: 2}},
		{Name: "pi-bundle", CanonicalPath: "/fixed/pi/bundle", PackageRelative: "dist/bundle", Object: launchidentity.ObjectV1{CanonicalPath: "/fixed/pi/bundle", Device: 1, Inode: 11, FileType: posixFileDirectory, Mode: uint32(posixFileDirectory) | 0o755, UID: 501, GID: 20, LinkCount: 2}},
	}
	materials := make([]launchidentity.LaunchMaterialV1, 0, 55)
	for index := 0; index < 7; index++ {
		size := int64(1)
		if index == 6 {
			size = 2_265_681
		}
		role := fmt.Sprintf("photon-node/file-%02d", index)
		materials = append(materials, launchidentity.LaunchMaterialV1{Role: role, Object: object("/fixed/pi/photon/file-"+fmt.Sprintf("%02d", index), uint64(100+index), size, false)})
	}
	entrypoint := object("/fixed/pi/bundle/cli.js", 200, 629, false)
	entrypoint.RawSHA256 = "sha256:1c3a5094b54aae9ae98c66516ce8c6578140363d081471ca7e91f9cb8c23dc8a"
	materials = append(materials, launchidentity.LaunchMaterialV1{Role: "pi-bundle/cli.js", Object: entrypoint})
	for index := 0; index < 47; index++ {
		size := int64(1)
		if index == 46 {
			size = 7_421_757
		}
		role := fmt.Sprintf("pi-bundle/file-%02d", index)
		materials = append(materials, launchidentity.LaunchMaterialV1{Role: role, Object: object("/fixed/pi/bundle/file-"+fmt.Sprintf("%02d", index), uint64(201+index), size, false)})
	}
	closure, err := launchidentity.Seal(launchidentity.SpecInput{
		RuntimeExecutable: object("/fixed/node", 2, 99, true), ClosureProfileID: launchidentity.Pi0843DarwinARM64Profile,
		MaterialRoots: roots, LaunchMaterials: materials, Arguments: []string{"/fixed/node", entrypoint.CanonicalPath}, Environment: []string{}, WorkingDirectory: "/tmp/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	return closure
}

func newCompositionFixture(t *testing.T) compositionFixture {
	t.Helper()
	inputs, runID := newCompositionInputs(t)
	ledger, err := NewCompositionLedger(inputs)
	if err != nil {
		t.Fatal(err)
	}
	// Direct ledger calls take the runtime claim themselves; the composition
	// entry defers it to newProductionRuntime.
	if err := ledger.owner.claimRuntime(); err != nil {
		t.Fatal(err)
	}
	return compositionFixture{inputs: inputs, acquisition: inputs.Acquisition, ledger: ledger, runID: runID}
}

func newCompositionInputs(t *testing.T) (CompositionInputs, string) {
	t.Helper()
	ownerFixture := newOwnerLockFixture(t)
	store := openOwnerStore(t, ownerFixture)
	acquisition := acquisitionAtEpoch(1)

	runStore := runstore.New(filepath.Join(ownerFixture.base, "run-store"))
	runID := "run:composition"
	runLease, err := runStore.Acquire(runID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runLease.Release() })
	timestamp := time.Unix(1_800_000_000, 0).UTC()
	planned := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:composition-planned", RunID: runID, Sequence: 1, Type: "run.transition", StateFrom: domain.StateCreated, StateTo: domain.StatePlanned, Timestamp: timestamp, Payload: map[string]any{}}
	readyEvent := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: "event:composition-ready", RunID: runID, Sequence: 2, Type: "run.transition", StateFrom: domain.StatePlanned, StateTo: domain.StateReady, Timestamp: timestamp.Add(time.Second), Payload: map[string]any{}}
	specDigest := canonical.DigestBytes([]byte("composition-spec"))
	policyDigest := canonical.DigestBytes([]byte("composition-policy"))
	capabilityDigest := canonical.DigestBytes([]byte("composition-capability"))
	baseSHA := strings.Repeat("a", 40)
	worktreePath := filepath.Join(ownerFixture.base, "worktree")
	readyEvent.Payload = map[string]any{"specDigest": specDigest, "policyDigest": policyDigest, "capabilityDigest": capabilityDigest, "baseSha": baseSHA, "worktreePath": worktreePath, "maxAttempts": 3}
	if err := runStore.Append(runLease, planned, 0); err != nil {
		t.Fatal(err)
	}
	if err := runStore.Append(runLease, readyEvent, 1); err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState("task:composition", runID, timestamp)
	state, err = lifecycle.Replay(state, planned)
	if err != nil {
		t.Fatal(err)
	}
	state, err = lifecycle.Replay(state, readyEvent)
	if err != nil {
		t.Fatal(err)
	}
	state.SpecDigest, state.PolicyDigest, state.CapabilityDigest = specDigest, policyDigest, capabilityDigest
	state.BaseSHA, state.WorktreePath = baseSHA, worktreePath
	if err := runStore.WriteSnapshot(runLease, state); err != nil {
		t.Fatal(err)
	}

	leaseLedger, err := dispatch.NewLeaseLedger(filepath.Join(ownerFixture.base, "dispatch-ledger"))
	if err != nil {
		t.Fatal(err)
	}
	inputs := CompositionInputs{
		Ingress: store, Runs: runStore, RunLease: runLease, LeaseLedger: leaseLedger,
		OwnerDirectory: ownerFixture.directory, Acquisition: acquisition, RunID: runID,
		Namespace: acquisition.Scope.AuthorityNamespaceID, OrchestratorID: "orchestrator:composition",
		ProvisionDomain: authority.SecurityDomainId{TenantNamespace: "tenant", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "host-process"},
		CleanupDomain:   authority.SecurityDomainId{TenantNamespace: "tenant", TrustDomainKind: authority.TrustDomainKindExecution, IsolationDomainId: "host-process-cleanup"},
		RegistrationID:  "registration-composition", CapabilitySnapshot: canonical.DigestBytes([]byte("composition-snapshot")),
		ConformanceEvidence: []string{}, Attestation: provider.Attestation{ProviderInstanceId: "provider-instance-composition", ConfigDigest: canonical.DigestBytes([]byte("config")), TrustRootKeyId: "trust-root-1", TrustRootAlgorithm: "ed25519"},
		AllocationRoot: filepath.Join(ownerFixture.base, "allocations"), LaunchClosure: compositionTestClosure(t),
		Requirements:     allocationcontrol.SandboxRequirementsV1{AccessMode: "workspace-write", MinimumAssuranceLevel: "workspace-write"},
		WorkDirAllowlist: []string{"/tmp/work"}, EnvironmentAllowlist: []string{"PATH"},
	}
	return inputs, runID
}

func TestCompositionLedgerCurrentOwnerAndPrepareRunStart(t *testing.T) {
	fixture := newCompositionFixture(t)
	owner, err := fixture.ledger.CurrentOwner(context.Background(), fixture.ledger.owner, fixture.acquisition)
	if err != nil {
		t.Fatalf("current owner: %v", err)
	}
	if owner.OwnerEpoch != fixture.acquisition.OwnerEpoch || owner.OwnerFactDigest == "" || owner.PendingRecovery != 0 {
		t.Fatalf("owner projection=%+v", owner)
	}
	start, err := fixture.ledger.PrepareRunStart(context.Background(), fixture.ledger.owner, fixture.acquisition, application.PrepareRunStartRequest{RunID: fixture.runID, ExpectedSequence: 2, ExpectedAuthorityHead: "sha256:" + strings.Repeat("0", 64)})
	if err == nil {
		t.Fatalf("stale authority head accepted: %+v", start)
	}
	projection, err := fixture.inputs.Runs.ReadRunStartAuthorityUnderLease(context.Background(), fixture.inputs.RunLease)
	if err != nil {
		t.Fatal(err)
	}
	start, err = fixture.ledger.PrepareRunStart(context.Background(), fixture.ledger.owner, fixture.acquisition, application.PrepareRunStartRequest{RunID: fixture.runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		t.Fatalf("prepare run start: %v", err)
	}
	if start.Validate() != nil || start.RunID != fixture.runID || start.Sequence != 2 || start.State != domain.StateReady {
		t.Fatalf("prepared run start=%+v", start)
	}
	rehydrated, err := fixture.ledger.RehydratePreparedRunStart(context.Background(), fixture.ledger.owner, fixture.acquisition, start.PreparationDigest)
	if err != nil || rehydrated != start {
		t.Fatalf("rehydrate=%+v err=%v", rehydrated, err)
	}
	if _, found, err := fixture.ledger.RehydrateRunStartOutcome(context.Background(), fixture.ledger.owner, fixture.acquisition, start.PreparationDigest); err != nil || found {
		t.Fatalf("run start outcome before commit found=%t err=%v", found, err)
	}
	inspected, err := fixture.ledger.InspectRun(context.Background(), fixture.ledger.owner, fixture.acquisition, application.InspectRunRequest{RunID: fixture.runID})
	if err != nil || inspected.RunID != fixture.runID || inspected.State != domain.StateReady {
		t.Fatalf("inspect=%+v err=%v", inspected, err)
	}
	// Replay must be byte-identical: the whole chain is creation-once.
	replay, err := fixture.ledger.PrepareRunStart(context.Background(), fixture.ledger.owner, fixture.acquisition, application.PrepareRunStartRequest{RunID: fixture.runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil || replay != start {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func TestComposeRuntimeReadyAndFailsClosedWithoutFixedMarshal(t *testing.T) {
	inputs, runID := newCompositionInputs(t)
	closure := inputs.LaunchClosure
	identity, err := launchidentity.Pi0843IdentityFromClosure(closure)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewPi0843Profile(closure.RuntimeExecutable.CanonicalPath, "/fixed/node-runtime", identity.IdentityDigest)
	if err != nil {
		t.Fatal(err)
	}
	composed, err := ComposeRuntime(context.Background(), inputs, profile)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	defer func() { _ = composed.Runtime.Close() }()
	status, err := composed.Runtime.Status(context.Background(), application.StatusRequest{})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Availability != application.AvailabilityReady || status.OwnerEpoch != inputs.Acquisition.OwnerEpoch {
		t.Fatalf("status=%+v", status)
	}
	projection, err := inputs.Runs.ReadRunStartAuthorityUnderLease(context.Background(), inputs.RunLease)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := composed.Runtime.PrepareRunStart(context.Background(), application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		t.Fatalf("prepare through runtime: %v", err)
	}
	// Without the real fixed marshal supervisor image the sealed mechanics
	// fail closed before any process side effect; the run stays READY.
	if _, err := composed.Runtime.StartPreparedRun(context.Background(), prepared); err == nil {
		t.Fatal("start without the fixed marshal image succeeded")
	}
	after, err := inputs.Runs.ReadRunStartAuthorityUnderLease(context.Background(), inputs.RunLease)
	if err != nil || after.Run.State != domain.StateReady {
		t.Fatalf("run state after failed start=%+v err=%v", after.Run, err)
	}
	// A profile that does not match the held closure must fail closed.
	foreign, err := NewPi0843Profile("/fixed/other", "/fixed/node-runtime", identity.IdentityDigest)
	if err != nil {
		t.Fatal(err)
	}
	foreignInputs, foreignRunID := newCompositionInputs(t)
	composedForeign, err := ComposeRuntime(context.Background(), foreignInputs, foreign)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = composedForeign.Runtime.Close() }()
	foreignProjection, err := foreignInputs.Runs.ReadRunStartAuthorityUnderLease(context.Background(), foreignInputs.RunLease)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := composedForeign.Runtime.PrepareRunStart(context.Background(), application.PrepareRunStartRequest{RunID: foreignRunID, ExpectedSequence: foreignProjection.Run.Sequence, ExpectedAuthorityHead: foreignProjection.Run.AuthorityHead}); err == nil {
		t.Fatal("mismatched agent profile admitted by prepare")
	}
}
