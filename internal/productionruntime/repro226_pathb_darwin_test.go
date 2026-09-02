//go:build darwin

package productionruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
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
		composed, err := ComposeRuntime(context.Background(), inputs, profile)
		if err != nil {
			t.Logf("compose step3 err (isolating SealPi next): %v", err)
			// Isolation: run the exact SealPi call with the same acquisition
			// binding NewCompositionLedger uses, and dump its result.
			innerHeld, innerErr := resultingress.OpenDarwinResultIngressStore(held)
			if innerErr != nil {
				t.Fatalf("sealed store reopen failed: %v", innerErr)
			}
			phase, phaseErr := openRepositoryOwnerScopeLock(inputs.OwnerDirectory, inputs.Acquisition.Scope)
			if phaseErr != nil {
				t.Fatalf("owner phase lock: %v", phaseErr)
			}
			ownerState, _, acquireErr := acquireOwner(innerHeld, phase, inputs.Acquisition)
			if acquireErr != nil {
				_ = phase.Close()
				t.Fatalf("acquire owner: %v", acquireErr)
			}
			_ = phase.Close()
			binding := resultingress.CurrentOwnerBinding{
				Scope:                          inputs.Acquisition.Scope,
				OwnerEpoch:                     inputs.Acquisition.OwnerEpoch,
				ControlOwnerAcquiredFactDigest: ownerState.FactDigest,
			}
			sealErr := func() error {
				_, err := resultingress.SealPi0844DarwinPreparedExecutionStore(context.Background(), innerHeld, &borrowedOwnerVerifier{acquisition: inputs.Acquisition, active: true}, binding, inputs.FixedMarshalPath, controlRoot)
				return err
			}()
			t.Fatalf("compose error=%v seal-isolate error=%v", err, sealErr)
		}
		defer func() { _ = composed.Runtime.Close() }()
		_ = runID
	})
}
