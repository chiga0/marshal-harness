//go:build darwin

package productionruntime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
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
		inputs, _, _, _, _ := pathBCompositionInputsForLaunch(t)
		inputs.ExistingWorktreeDescriptorGraph = allocationcontrol.ExistingWorktreeDescriptorGraphV1{}
		inputs.ExistingWorktreeTargetWorktree = nil
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
}
