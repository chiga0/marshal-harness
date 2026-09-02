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

	// step5（以两个顺序 CompositionLedger 复用同一 durable 状态）被证明对
	// CLI 形状不具同等价值：sealed CLI 在同一进程内借共享的
	// RepositorySession调用两次，而不会物理两两组合；跨组合的
	// owner phase 会按设计拒绝 not-current，行为夹杂噪音，不构成 #226 证据。
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
			t.Logf("step6 StartPreparedRun succeeded (no #226 reproduction)")
			return
		}
		if application.HasReason(startErr, application.ReasonAuthorityConflict) {
			t.Fatalf("step6 REPRODUCED #226 directly on controller.startPreparedRun: %v", startErr)
		}
		t.Logf("step6 classification (not #226): %v", startErr)
	})
}

// TestRepro226SequentialSessionStart matches the fixed CLI operation shape:
// one repository-wide owner session, a short-lived runtime for Prepare, then
// a fresh short-lived runtime for Start. Keeping both calls in one runtime
// does not exercise the production boundary that regressed in #226.
func TestRepro226SequentialSessionStart(t *testing.T) {
	inputs, runID, _, base, _ := pathBCompositionInputsForLaunch(t)
	// Keep the generic fixture view open while the held Darwin view is sealed,
	// matching TestRepositorySessionReusesOneOwnerAcrossSequentialRunRuntimes.
	// Closing the fixture store first changes the test-only store lifecycle and
	// fails before the production cross-call boundary under examination.
	t.Cleanup(func() { _ = inputs.Ingress.Close() })
	held, err := os.Open(filepath.Join(base, "result-ingress"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	controlPath := filepath.Join(base, "owner-control")
	if err := os.Mkdir(controlPath, 0o700); err != nil {
		t.Fatal(err)
	}
	control, err := os.Open(controlPath)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	fixed, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixed, err = filepath.EvalSymlinks(fixed)
	if err != nil {
		t.Fatal(err)
	}
	core, err := processsupervisor.ObserveCurrentCore(fixed)
	if err != nil {
		t.Fatal(err)
	}
	acquisition := inputs.Acquisition
	acquisition.OwnerUID = core.UID
	acquisition.OwnerGID = core.GID
	acquisition.OwnerProcess = core.Process
	acquisition.OwnerBinary = core.Binary
	acquisition.ObservedAt = time.Unix(core.Process.BirthSeconds, core.Process.BirthMicroseconds*int64(time.Microsecond)).UTC().Add(time.Second).Format(time.RFC3339Nano)
	session, err := OpenRepositorySession(context.Background(), RepositorySessionInputs{
		HeldIngressDir: held, OwnerDirectory: inputs.OwnerDirectory,
		Acquisition: acquisition, FixedMarshalPath: fixed,
		OwnerPrivateControlRoot: control,
	})
	if err != nil {
		t.Fatalf("open repository session: %v", err)
	}
	defer session.Close()

	composeInputs := inputs
	composeInputs.Ingress = nil
	composeInputs.OwnerDirectory = nil
	composeInputs.Acquisition = resultingress.ControlOwnerAcquisition{}
	composeInputs.RepositorySession = session
	identity, err := launchidentity.Pi0844IdentityFromClosure(composeInputs.LaunchClosure)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewPi0844Profile(composeInputs.LaunchClosure.RuntimeExecutable.CanonicalPath, "/fixed/node-runtime", identity.IdentityDigest)
	if err != nil {
		t.Fatal(err)
	}

	first, err := ComposeRuntime(context.Background(), composeInputs, profile)
	if err != nil {
		t.Fatalf("compose prepare runtime: %v", err)
	}
	projection, err := first.Runtime.InspectRun(context.Background(), application.InspectRunRequest{RunID: runID})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := first.Runtime.PrepareRunStart(context.Background(), application.PrepareRunStartRequest{
		RunID: runID, ExpectedSequence: projection.Sequence, ExpectedAuthorityHead: projection.AuthorityHead,
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := first.Runtime.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := ComposeRuntime(context.Background(), composeInputs, profile)
	if err != nil {
		t.Fatalf("compose start runtime: %v", err)
	}
	defer second.Runtime.Close()
	ctrl := second.Runtime.controller
	if ctrl == nil {
		t.Fatal("start runtime controller missing")
	}
	err = ctrl.withOwner(context.Background(), true, func(verifier resultingress.CurrentOwnerLockVerifier, owner OwnerProjection) error {
		durable, err := ctrl.authority.RehydratePreparedRunStart(context.Background(), verifier, ctrl.acquisition, prepared.PreparationDigest)
		if err != nil {
			t.Fatalf("stage rehydrate-prepared: %v", err)
		}
		if durable != prepared {
			t.Fatalf("stage compare-prepared mismatch:\ndurable=%+v\nsupplied=%+v", durable, prepared)
		}
		if replay, found, err := ctrl.authority.RehydrateRunStartOutcome(context.Background(), verifier, ctrl.acquisition, prepared.PreparationDigest); err != nil {
			t.Fatalf("stage rehydrate-outcome: %v", err)
		} else if found {
			t.Fatalf("stage rehydrate-outcome unexpectedly found before start: %+v", replay)
		}
		if err := ctrl.bridge.VerifyAgentProfile(context.Background(), verifier, ctrl.acquisition, owner, ctrl.profile); err != nil {
			t.Fatalf("stage verify-profile: %v", err)
		}
		if err := ctrl.bridge.StartPreparedRun(context.Background(), verifier, ctrl.acquisition, owner, ctrl.profile, prepared); err != nil {
			t.Fatalf("stage bridge-start raw leaf: %T: %v", err, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("stage owner-window: %v", err)
	}
}
