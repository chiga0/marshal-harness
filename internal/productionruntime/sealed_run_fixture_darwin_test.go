//go:build darwin && arm64

package productionruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/launchidentity"
)

// The real installed Pi 0.84.4 image (maintainer-designated contract image).
const (
	fixturePiRuntime     = "/opt/homebrew/bin/node"
	fixturePiPackage     = "/opt/homebrew/lib/node_modules/@earendil-works/pi-coding-agent"
	fixturePiEntrypoint  = fixturePiPackage + "/dist/bundle/cli.js"
	fixturePiSessionFlag = "--session"
)

// TestSealedChainReachesRunningWithRealPi drives one READY run through the
// complete sealed chain with the real installed Pi 0.84.4 image: preparation,
// owner acquisition, seal, supervisor spawn of the real image and the sealed
// run-start commit. The run journal must end RUNNING with the exact attempt.
func TestSealedChainReachesRunningWithRealPi(t *testing.T) {
	if _, err := os.Stat(fixturePiEntrypoint); err != nil {
		t.Skipf("real Pi image not present: %v", err)
	}
	// Homebrew installs binaries as symlinks into Cellar; the held-object
	// opens use O_NOFOLLOW_ANY, so every fixture path must be canonical.
	runtimePath, err := filepath.EvalSymlinks(fixturePiRuntime)
	if err != nil {
		t.Fatal(err)
	}
	entrypointPath, err := filepath.EvalSymlinks(fixturePiEntrypoint)
	if err != nil {
		t.Fatal(err)
	}
	inputs, runID := newCompositionInputs(t)
	// The agent working directory must exist and be canonical before the
	// closure is sealed and observed (O_NOFOLLOW_ANY rejects symlinked
	// ancestors such as /tmp).
	workDir := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(workDir); resolveErr != nil {
		t.Fatal(resolveErr)
	} else {
		workDir = resolved
	}
	heldClosure, err := launchidentity.OpenPi0844(runtimePath, entrypointPath, []string{runtimePath, entrypointPath}, []string{}, workDir)
	if err != nil {
		t.Fatalf("open real Pi closure: %v", err)
	}
	defer heldClosure.Close()
	inputs.LaunchClosure = heldClosure.Closure
	inputs.WorkDirAllowlist = []string{workDir}

	identity, err := launchidentity.Pi0844IdentityFromClosure(heldClosure.Closure)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewPi0844Profile(heldClosure.Closure.RuntimeExecutable.CanonicalPath, fixturePiRuntime, identity.IdentityDigest)
	if err != nil {
		t.Fatal(err)
	}
	composed, err := ComposeRuntime(context.Background(), inputs, profile)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	defer func() { _ = composed.Runtime.Close() }()
	// Cleanup hygiene for the fixture: the real Pi child outlives the sealed
	// commit by design (production terminalization owns it); the fixture kills
	// only processes whose command line names this exact image entrypoint.
	t.Cleanup(func() {
		cmd := exec.Command("pkill", "-f", fixturePiEntrypoint)
		_ = cmd.Run()
	})

	projection, err := inputs.Runs.ReadRunStartAuthorityUnderLease(context.Background(), inputs.RunLease)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := composed.Runtime.PrepareRunStart(context.Background(), application.PrepareRunStartRequest{RunID: runID, ExpectedSequence: projection.Run.Sequence, ExpectedAuthorityHead: projection.Run.AuthorityHead})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// The real supervisor handshake involves re-executed test binaries; give
	// the full spawn chain a bounded but generous window.
	startCtx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	if _, err := composed.Runtime.StartPreparedRun(startCtx, prepared); err != nil {
		t.Fatalf("start prepared run with the real Pi image: %v", err)
	}
	after, err := composed.Runtime.InspectRun(context.Background(), application.InspectRunRequest{RunID: runID})
	if err != nil {
		t.Fatalf("inspect after start: %v", err)
	}
	if after.State != domain.StateRunning || after.AttemptID == "" || after.AttemptID != prepared.AttemptID {
		t.Fatalf("run after sealed start=%+v prepared=%+v", after, prepared)
	}

}
