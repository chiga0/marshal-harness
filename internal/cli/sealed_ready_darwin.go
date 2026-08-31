//go:build darwin && arm64

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/app"
	application "github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"github.com/chiga0/marshal-harness/internal/resultbinding"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

// runSealedReadyBranch drives one READY run through the sealed production
// composition. The Pi runtime and entrypoint come from MARSHAL_PI_RUNTIME and
// MARSHAL_PI_ENTRYPOINT; both are mandatory and the launchidentity seal fails
// closed unless the bytes match the frozen Pi 0.84.4 identity.
func runSealedReadyBranch(ctx context.Context, stateRoot, repositoryRoot, taskID, runID string, stdout, stderr io.Writer) int {
	piRuntime := os.Getenv("MARSHAL_PI_RUNTIME")
	piEntrypoint := os.Getenv("MARSHAL_PI_ENTRYPOINT")
	if piRuntime == "" || piEntrypoint == "" {
		fmt.Fprintln(stderr, "运行失败：sealed 组合需要 MARSHAL_PI_RUNTIME 与 MARSHAL_PI_ENTRYPOINT 指向冻结的 Pi 0.84.4 镜像。")
		return ExitUnavailable
	}
	piRuntime, err := filepath.EvalSymlinks(piRuntime)
	if err != nil {
		fmt.Fprintln(stderr, "运行失败：Pi Node runtime 路径无法解析。")
		return ExitUnavailable
	}
	piEntrypoint, err = filepath.EvalSymlinks(piEntrypoint)
	if err != nil {
		fmt.Fprintln(stderr, "运行失败：Pi entrypoint 路径无法解析。")
		return ExitUnavailable
	}
	ingressDir, ledgerDir, allocationRoot, ownerDir := productionruntime.CompositionPaths(stateRoot)
	for _, dir := range []string{ingressDir, ledgerDir, allocationRoot, ownerDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(stderr, "运行失败：无法准备 sealed 组合目录：%v\n", err)
			return ExitFailure
		}
	}
	ownerDirHandle, err := os.Open(ownerDir)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：无法打开持有 owner 目录：%v\n", err)
		return ExitFailure
	}
	defer ownerDirHandle.Close()
	heldIngress, err := os.Open(ingressDir)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：无法打开持有 result-ingress 目录：%v\n", err)
		return ExitFailure
	}
	defer heldIngress.Close()
	controlRootPath := filepath.Join(stateRoot, "owner-control")
	if err := os.MkdirAll(controlRootPath, 0o700); err != nil {
		fmt.Fprintf(stderr, "运行失败：无法准备 owner-private control root：%v\n", err)
		return ExitFailure
	}
	controlRoot, err := os.Open(controlRootPath)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：无法打开 owner-private control root：%v\n", err)
		return ExitFailure
	}
	defer controlRoot.Close()
	fixedMarshal, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：无法定位 fixed marshal 镜像：%v\n", err)
		return ExitFailure
	}
	if resolved, resolveErr := filepath.EvalSymlinks(fixedMarshal); resolveErr != nil {
		fmt.Fprintf(stderr, "运行失败：fixed marshal 镜像路径无法解析：%v\n", resolveErr)
		return ExitFailure
	} else {
		fixedMarshal = resolved
	}

	runStore := runstore.New(stateRoot)
	// Transient existing-only lease for input pre-read (READY projection +
	// task-spec). It is released BEFORE any repository owner acquisition so it
	// never enters the RB1 lock order (ADR 0069 §3.3/§4); ComposeRuntime
	// acquires the real Run Lease inside NewCompositionLedger after the owner.
	preReadLease, err := runStore.AcquireExisting(runID)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：无法获取 Run 租约：%v\n", err)
		return ExitFailure
	}
	preReadReleased := false
	defer func() {
		if !preReadReleased {
			_ = preReadLease.Release()
		}
	}()
	projection, err := runStore.ReadRunStartAuthorityUnderLease(ctx, preReadLease)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：READY 权威投影不可读：%v\n", err)
		return ExitFailure
	}
	// Path B (ADR 0069/0070): build and hold the repository descriptor graph
	// plus the exact projection.WorktreePath target descriptor. The handles
	// cover ComposeRuntime/Prepare/Start and close on exit. The fixed
	// /usr/bin/git is used only as a locator; the descriptor-graph
	// constructors validate the held descriptors.
	worktreeHandles, err := openExistingWorktreeComposition(repositoryRoot, projection.WorktreePath)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：无法构建 existing-worktree 描述符图：%v\n", err)
		return ExitFailure
	}
	defer func() { _ = worktreeHandles.Close() }()
	taskData, err := runstore.ReadFileUnderLease(preReadLease, 2<<20, "task-spec.json")
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：无法读取 task spec：%v\n", err)
		return ExitFailure
	}
	// Release the transient pre-read lease now; the real Run Lease is acquired
	// inside NewCompositionLedger after repository owner acquisition. The
	// formal Runtime.Close releases that internal lease exactly once.
	if err := preReadLease.Release(); err != nil {
		fmt.Fprintf(stderr, "运行失败：预读租约释放失败：%v\n", err)
		return ExitFailure
	}
	preReadReleased = true
	appInstance, appErr := app.New()
	if appErr != nil {
		fmt.Fprintf(stderr, "运行失败：application 初始化失败：%v\n", appErr)
		return ExitFailure
	}
	task, taskErr := appInstance.ParseTaskSpec(taskData)
	if taskErr != nil {
		fmt.Fprintf(stderr, "运行失败：task spec 无法解析：%v\n", taskErr)
		return ExitFailure
	}
	requirements, err := productionruntime.Pi0844Requirements(task.Worker.ExecutionProfile)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：执行 profile 不被 sealed 组合接受：%v\n", err)
		return ExitFailure
	}
	heldClosure, err := launchidentity.OpenPi0844(piRuntime, piEntrypoint, []string{piRuntime, piEntrypoint}, []string{}, projection.WorktreePath)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：Pi 镜像身份与冻结的 0.84.4 合同不一致，sealed 组合拒绝启动：%v\n", err)
		return ExitUnavailable
	}
	defer heldClosure.Close()
	closure := heldClosure.Closure
	identity, err := launchidentity.Pi0844IdentityFromClosure(closure)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：Pi 身份派生失败：%v\n", err)
		return ExitFailure
	}
	profile, err := productionruntime.NewPi0844Profile(closure.RuntimeExecutable.CanonicalPath, piEntrypoint, identity.IdentityDigest)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：Pi profile 无效：%v\n", err)
		return ExitFailure
	}
	leaseLedger, err := dispatch.NewLeaseLedger(ledgerDir)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：dispatch ledger 初始化失败：%v\n", err)
		return ExitFailure
	}
	namespace := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: repositoryRoot}
	acquisition, err := observeCompositionAcquisition(fixedMarshal, namespace)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：fixed marshal core 身份观察失败：%v\n", err)
		return ExitFailure
	}
	attestation, err := productionruntime.LocalAttestation(fixedMarshal)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：attestation 观察失败：%v\n", err)
		return ExitFailure
	}
	inputs := productionruntime.CompositionInputs{
		Ingress: nil, Runs: runStore, RunLease: nil, LeaseLedger: leaseLedger,
		OwnerDirectory: ownerDirHandle, Acquisition: acquisition, RunID: runID,
		HeldIngressDir: heldIngress, FixedMarshalPath: fixedMarshal, OwnerPrivateControlRoot: controlRoot,
		Namespace: namespace, OrchestratorID: "orchestrator:" + taskID,
		ProvisionDomain: productionruntime.LocalProvisionDomain(namespace), CleanupDomain: productionruntime.LocalCleanupDomain(namespace),
		RegistrationID: resultbinding.AgentRegistrationID(projection.CapabilityDigest), CapabilitySnapshot: projection.CapabilityDigest, ConformanceEvidence: []string{},
		Attestation: attestation, AllocationRoot: allocationRoot,
		LaunchClosure: closure, Requirements: requirements,
		WorkDirAllowlist: []string{projection.WorktreePath}, EnvironmentAllowlist: []string{"PATH"},
		ExistingWorktreeDescriptorGraph: worktreeHandles.graph,
		ExistingWorktreeTargetWorktree:  worktreeHandles.target,
	}
	composed, err := productionruntime.ComposeRuntime(ctx, inputs, profile)
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：sealed 组合失败：%v\n", err)
		return ExitFailure
	}
	defer func() { _ = composed.Runtime.Close() }()
	prepared, err := composed.Runtime.PrepareRunStart(ctx, application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：sealed PrepareRunStart 失败：%v\n", err)
		return ExitFailure
	}
	if _, err := composed.Runtime.StartPreparedRun(ctx, prepared); err != nil {
		fmt.Fprintf(stderr, "运行失败：sealed StartPreparedRun 失败：%v\n", err)
		return ExitFailure
	}
	after, err := composed.Runtime.InspectRun(ctx, application.InspectRunRequest{RunID: runID})
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：sealed 启动后状态不可读：%v\n", err)
		return ExitFailure
	}
	fmt.Fprintf(stdout, "Run：%s\nAttempt：%s\n状态：%s\n", runID, after.AttemptID, after.State)
	return ExitOK
}

// observeCompositionAcquisition binds acquisition evidence to the exact fixed
// marshal core already resolved by the CLI composition root.
func observeCompositionAcquisition(fixedMarshal string, namespace authority.AuthorityNamespaceId) (productionruntime.ControlOwnerAcquisition, error) {
	core, err := processsupervisor.ObserveCurrentCore(fixedMarshal)
	if err != nil {
		return productionruntime.ControlOwnerAcquisition{}, err
	}
	repositoryIdentityDigest, err := namespace.Digest()
	if err != nil {
		return productionruntime.ControlOwnerAcquisition{}, err
	}
	return productionruntime.ControlOwnerAcquisition{
		Scope:      productionruntime.ControlOwnerScope{AuthorityNamespaceID: namespace, RepositoryIdentityDigest: repositoryIdentityDigest},
		OwnerEpoch: 1,
		OwnerUID:   core.UID, OwnerGID: core.GID, OwnerProcess: core.Process, OwnerBinary: core.Binary,
		ObserverIdentity: "darwin-owner-observer/v1",
		ObservedAt:       time.Unix(core.Process.BirthSeconds, core.Process.BirthMicroseconds*int64(time.Microsecond)).UTC().Add(time.Second).Format(time.RFC3339Nano),
	}, nil
}
