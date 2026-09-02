//go:build darwin

package productionruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

// #226 reproduction probes (A2 path, isolated branch): pinpoint which input
// turns a sealed ComposeRuntime into `resultingress: prepared execution
// unavailable`, before any PrepareRunStart is attempted. Stage 1: the full
// path-B composition inputs minus descriptor graph/target (staging mode);
// Stage 2: same + existing-worktree graph/target (path B).
func repro226SealedInputs(t *testing.T, inputs CompositionInputs) (CompositionInputs, *os.File, *os.File) {
	t.Helper()
	heldDir := t.TempDir()
	if err := os.Chmod(heldDir, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(heldDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	controlRootDir := t.TempDir()
	if err := os.Chmod(controlRootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	controlRoot, err := os.Open(controlRootDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controlRoot.Close() })
	fixed, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(fixed); resolveErr != nil {
		t.Fatal(resolveErr)
	} else {
		fixed = resolved
	}
	inputs.Ingress = nil
	inputs.HeldIngressDir = held
	inputs.FixedMarshalPath = fixed
	inputs.OwnerPrivateControlRoot = controlRoot
	// sealed 合同把 acquisition 绑定到当前精确观察的核心（binary + process
	// birth + observedAt）；fixture 的 acquisitionAtEpoch 只近似观察 birth，
	// ObservedAt 不在当前精确回放窗内 → 与 newCompositionInputs 一致补齐。
	core, coreErr := processsupervisor.ObserveCurrentCore(fixed)
	if coreErr != nil {
		t.Fatalf("observe current core: %v", coreErr)
	}
	inputs.Acquisition.OwnerUID = core.UID
	inputs.Acquisition.OwnerGID = core.GID
	inputs.Acquisition.OwnerProcess = core.Process
	inputs.Acquisition.OwnerBinary = core.Binary
	inputs.Acquisition.ObservedAt = time.Unix(core.Process.BirthSeconds, core.Process.BirthMicroseconds*int64(time.Microsecond)).UTC().Add(time.Second).Format(time.RFC3339Nano)
	return inputs, held, controlRoot
}

func TestRepro226SealedComposeBase(t *testing.T) {
	t.Run("step1-open-darwin-store", func(t *testing.T) {
		heldDir := t.TempDir()
		if err := os.Chmod(heldDir, 0o700); err != nil {
			t.Fatal(err)
		}
		held, err := os.Open(heldDir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = held.Close() }()
		if _, err := resultingress.OpenDarwinResultIngressStore(held); err != nil {
			t.Fatalf("OpenDarwinResultIngressStore(fresh held): %v", err)
		}
	})
	t.Run("step2-compose-no-graph", func(t *testing.T) {
		inputs, runID, _, _, _ := pathBCompositionInputsForLaunch(t)
		inputs.ExistingWorktreeDescriptorGraph = allocationcontrol.ExistingWorktreeDescriptorGraphV1{}
		inputs.ExistingWorktreeTargetWorktree = nil
		// Staging composition requires a caller-held Run Lease (path A contract).
		lease, err := inputs.Runs.Acquire(runID)
		if err != nil {
			t.Fatal(err)
		}
		inputs.RunLease = lease
		t.Cleanup(func() { _ = lease.Release() })
		inputs, held, _ := repro226SealedInputs(t, inputs)
		_ = held
		closure := inputs.LaunchClosure
		identity, err := launchidentity.Pi0844IdentityFromClosure(closure)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := NewPi0844Profile(closure.RuntimeExecutable.CanonicalPath, "/fixed/node-runtime", identity.IdentityDigest)
		if err != nil {
			t.Fatal(err)
		}
		composed, err := ComposeRuntime(context.Background(), inputs, profile)
		if err != nil {
			t.Fatalf("compose path-B inputs without graph: %v", err)
		}
		defer func() { _ = composed.Runtime.Close() }()
	})
	t.Run("step3-compose-with-graph", func(t *testing.T) {
		inputs, runID, _, _, _ := pathBCompositionInputsForLaunch(t)
		inputs, held, _ := repro226SealedInputs(t, inputs)
		_ = held
		closure := inputs.LaunchClosure
		identity, err := launchidentity.Pi0844IdentityFromClosure(closure)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := NewPi0844Profile(closure.RuntimeExecutable.CanonicalPath, "/fixed/node-runtime", identity.IdentityDigest)
		if err != nil {
			t.Fatal(err)
		}
		composed, err := ComposeRuntime(context.Background(), inputs, profile)
		if err != nil {
			t.Fatalf("compose path-B inputs with graph: %v", err)
		}
		defer func() { _ = composed.Runtime.Close() }()
		_ = runID
	})
	// step4/step5：compose 后实际驱动 PrepareRunStart，并对被 CLI
	// sealed StartPreparedRun 失败点（application: authority-conflict）所
	// 影响的 Replay identity 进行比较（RehydratePreparedRunStart vs 直接
	// Runtime.PrepareRunStart 结果）：
	t.Run("step4-pathb-prepare-and-rehydrate-identity", func(t *testing.T) {
		inputs, runID, _, _, _ := pathBCompositionInputsForLaunch(t)
		inputs, held, _ := repro226SealedInputs(t, inputs)
		_ = held
		closure := inputs.LaunchClosure
		identity, err := launchidentity.Pi0844IdentityFromClosure(closure)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := NewPi0844Profile(closure.RuntimeExecutable.CanonicalPath, "/fixed/node-runtime", identity.IdentityDigest)
		if err != nil {
			t.Fatal(err)
		}
		composed, err := ComposeRuntime(context.Background(), inputs, profile)
		if err != nil {
			t.Fatalf("compose: %v", err)
		}
		defer func() { _ = composed.Runtime.Close() }()
		projection, err := composed.Runtime.InspectRun(context.Background(), application.InspectRunRequest{RunID: runID})
		if err != nil {
			t.Fatalf("inspect: %v", err)
		}
		prepared, err := composed.Runtime.PrepareRunStart(context.Background(), application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Sequence, ExpectedAuthorityHead: projection.AuthorityHead})
		if err != nil {
			t.Fatalf("sealed path-B prepare: %v", err)
		}
		ctrl := composed.Runtime.controller
		if ctrl == nil {
			t.Fatal("runtime controller missing")
		}
		var durable application.PreparedRunStart
		if err := ctrl.withOwner(context.Background(), true, func(verifier resultingress.CurrentOwnerLockVerifier, _ OwnerProjection) error {
			var herr error
			durable, herr = ctrl.authority.RehydratePreparedRunStart(context.Background(), verifier, ctrl.acquisition, prepared.PreparationDigest)
			return herr
		}); err != nil {
			t.Fatalf("rehydrate leaf: %v", err)
		}
		if durable != prepared {
			t.Fatalf("path-B durable-projection mismatch:\ndurable=%+v\nsupplied=%+v", durable, prepared)
		}
	})

	// step5：同一 durable underling 上串两个 CompositionLedger——CLI
	// `openRun` per-call 的等价形状：runtime#1 完成 PrepareRunStart 后 close，
	// 在新的 CompositionLedger（runtime#2）上 Rehydrate同一 durable 准备投影。
	t.Run("step5-cross-ledger-rehydrate-after-prepare", func(t *testing.T) {
		inputs, runID, _, _, _ := pathBCompositionInputsForLaunch(t)
		inputs, held, controlRoot := repro226SealedInputs(t, inputs)
		closure := inputs.LaunchClosure
		identity, err := launchidentity.Pi0844IdentityFromClosure(closure)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := NewPi0844Profile(closure.RuntimeExecutable.CanonicalPath, "/fixed/node-runtime", identity.IdentityDigest)
		if err != nil {
			t.Fatal(err)
		}
		// runtime #1：PrepareRunStart。
		composed1, err := ComposeRuntime(context.Background(), inputs, profile)
		if err != nil {
			t.Fatalf("compose #1: %v", err)
		}
		projection1, err := composed1.Runtime.InspectRun(context.Background(), application.InspectRunRequest{RunID: runID})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := composed1.Runtime.PrepareRunStart(context.Background(), application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection1.Sequence, ExpectedAuthorityHead: projection1.AuthorityHead})
		if err != nil {
			t.Fatalf("prepare via runtime #1: %v", err)
		}
		if err := composed1.Runtime.Close(); err != nil {
			t.Fatalf("close runtime #1: %v", err)
		}
		// runtime #2：同一 fixture；将 sealed composition inputs 重新闬。
		inputs2 := inputs
		inputs2.Ingress = nil
		inputs2.HeldIngressDir = held
		inputs2.FixedMarshalPath = inputs.FixedMarshalPath
		inputs2.OwnerPrivateControlRoot = controlRoot
		composed2, err := ComposeRuntime(context.Background(), inputs2, profile)
		if err != nil {
			t.Fatalf("compose #2: %v", err)
		}
		defer func() { _ = composed2.Runtime.Close() }()
		ctrl2 := composed2.Runtime.controller
		if ctrl2 == nil {
			t.Fatal("runtime #2 controller missing")
		}
		var durable application.PreparedRunStart
		if err := ctrl2.withOwner(context.Background(), true, func(verifier resultingress.CurrentOwnerLockVerifier, _ OwnerProjection) error {
			var herr error
			durable, herr = ctrl2.authority.RehydratePreparedRunStart(context.Background(), verifier, ctrl2.acquisition, prepared.PreparationDigest)
			return herr
		}); err != nil {
			t.Fatalf("Rehydrate from runtime #2 (CLI openRun #2 equivalent): %v", err)
		}
		if durable != prepared {
			t.Fatalf("CROSS-LEDGER REPRODUCED #226 mismatch:\nruntime#1 supplied=%+v\nruntime#2 rehydrated=%+v", prepared, durable)
		}
	})
	// step6：依样判冷冻重点主漏——直接对 sealed composition drive 收成后 runtime
	// 调用 StartPreparedRun；真实 CLI dogfood 的 StartPreparedRun-阶段被拒
	// （application: authority-conflict）。若在 runtime 层被同样的错误打到解决
	// （vs spawn/launch 预咬生错），则在 controller.startPreparedRun 有其针对 sealed
	// CLI 形状的重现之点。
	t.Run("step6-startpreparedrun-pathb", func(t *testing.T) {
		inputs, runID, _, _, _ := pathBCompositionInputsForLaunch(t)
		inputs, held, _ := repro226SealedInputs(t, inputs)
		_ = held
		closure := inputs.LaunchClosure
		identity, err := launchidentity.Pi0844IdentityFromClosure(closure)
		if err != nil {
			t.Fatal(err)
		}
		profile, err := NewPi0844Profile(closure.RuntimeExecutable.CanonicalPath, "/fixed/node-runtime", identity.IdentityDigest)
		if err != nil {
			t.Fatal(err)
		}
		composed, err := ComposeRuntime(context.Background(), inputs, profile)
		if err != nil {
			t.Fatalf("compose: %v", err)
		}
		defer func() { _ = composed.Runtime.Close() }()
		projection, err := composed.Runtime.InspectRun(context.Background(), application.InspectRunRequest{RunID: runID})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := composed.Runtime.PrepareRunStart(context.Background(), application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Sequence, ExpectedAuthorityHead: projection.AuthorityHead})
		if err != nil {
			t.Fatalf("sealed path-B prepare: %v", err)
		}
		successor, startErr := composed.Runtime.StartPreparedRun(context.Background(), prepared)
		_ = successor
		if startErr == nil {
			t.Fatalf("step6 UNEXPECTED success for test bridge substrate (no #226 reproduction)")
		}
		if application.HasReason(startErr, application.ReasonAuthorityConflict) {
			t.Fatalf("step6 REPRODUCED #226 directly on controller.startPreparedRun: %v", startErr)
		}
		t.Fatalf("step6 classification (not #226): %v", startErr)
	})
}
