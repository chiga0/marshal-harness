//go:build darwin && arm64

package productionruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

const (
	coldOwnerHelperEnv  = "MARSHAL_TEST_COLD_OWNER_HELPER"
	coldOwnerRootEnv    = "MARSHAL_TEST_COLD_OWNER_ROOT"
	coldOwnerEpochEnv   = "MARSHAL_TEST_COLD_OWNER_EPOCH"
	coldOwnerTimeout    = 20 * time.Second
	coldOwnerProcessRun = "^TestRepositorySessionColdOwnerProcessHelper$"
)

func createOwnerOnlyDirectory(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, -1, os.Getgid()); err != nil {
		t.Fatal(err)
	}
}

func exactProcessAcquisition(t *testing.T, fixed string) resultingress.ControlOwnerAcquisition {
	t.Helper()
	core, err := processsupervisor.ObserveCurrentCore(fixed)
	if err != nil {
		t.Fatalf("observe current core: %v", err)
	}
	acquisition := testAcquisition()
	acquisition.OwnerEpoch = 0
	acquisition.OwnerUID = core.UID
	acquisition.OwnerGID = core.GID
	acquisition.OwnerProcess = core.Process
	acquisition.OwnerBinary = core.Binary
	acquisition.ObservedAt = time.Unix(core.Process.BirthSeconds, core.Process.BirthMicroseconds*int64(time.Microsecond)).UTC().Add(time.Second).Format(time.RFC3339Nano)
	return acquisition
}

func TestRepositorySessionReopensThroughFourColdOwnerProcesses(t *testing.T) {
	root, err := os.MkdirTemp("/private/tmp", "marshal-cold-owner-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove cold owner fixture: %v", err)
		}
	})
	for _, name := range []string{"owner", "result-ingress", "control"} {
		createOwnerOnlyDirectory(t, filepath.Join(root, name))
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}

	seenProcesses := make(map[processsupervisor.ProcessIdentity]struct{}, 4)
	previousFactDigest := ""
	for expectedEpoch := 1; expectedEpoch <= 4; expectedEpoch++ {
		ctx, cancel := context.WithTimeout(context.Background(), coldOwnerTimeout)
		command := exec.CommandContext(ctx, executable, "-test.run="+coldOwnerProcessRun)
		command.Env = append(os.Environ(),
			coldOwnerHelperEnv+"=1",
			coldOwnerRootEnv+"="+root,
			coldOwnerEpochEnv+"="+strconv.Itoa(expectedEpoch),
		)
		output, runErr := command.CombinedOutput()
		cancel()
		if runErr != nil {
			t.Fatalf("cold owner epoch %d: %v\n%s", expectedEpoch, runErr, output)
		}

		store, openErr := resultingress.OpenResultIngressStore(filepath.Join(root, "result-ingress"))
		if openErr != nil {
			t.Fatal(openErr)
		}
		state, found, replayErr := store.OpenOwner(testAcquisition().Scope)
		_ = store.Close()
		if replayErr != nil || !found || state.Acquisition.OwnerEpoch != uint64(expectedEpoch) || state.PreviousFactDigest != previousFactDigest {
			t.Fatalf("cold owner replay epoch=%d state=%#v found=%t err=%v", expectedEpoch, state, found, replayErr)
		}
		if _, duplicate := seenProcesses[state.Acquisition.OwnerProcess]; duplicate {
			t.Fatalf("cold owner epoch %d reused process identity %#v", expectedEpoch, state.Acquisition.OwnerProcess)
		}
		seenProcesses[state.Acquisition.OwnerProcess] = struct{}{}
		previousFactDigest = state.FactDigest
	}
}

func TestRepositorySessionColdOwnerProcessHelper(t *testing.T) {
	if os.Getenv(coldOwnerHelperEnv) != "1" {
		t.Skip("cold owner subprocess helper")
	}
	root := os.Getenv(coldOwnerRootEnv)
	expectedEpoch, err := strconv.Atoi(os.Getenv(coldOwnerEpochEnv))
	if err != nil || expectedEpoch < 1 {
		t.Fatalf("invalid expected epoch")
	}
	fixed, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixed, err = filepath.EvalSymlinks(fixed)
	if err != nil {
		t.Fatal(err)
	}
	ownerDirectory, err := os.Open(filepath.Join(root, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer ownerDirectory.Close()
	ingressDirectory, err := os.Open(filepath.Join(root, "result-ingress"))
	if err != nil {
		t.Fatal(err)
	}
	defer ingressDirectory.Close()
	controlDirectory, err := os.Open(filepath.Join(root, "control"))
	if err != nil {
		t.Fatal(err)
	}
	defer controlDirectory.Close()
	repositoryDirectory, err := OpenCanonicalRepositoryRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer repositoryDirectory.Close()

	ctx, cancel := context.WithTimeout(context.Background(), coldOwnerTimeout)
	defer cancel()
	session, err := OpenRepositorySession(ctx, RepositorySessionInputs{
		HeldIngressDir:          ingressDirectory,
		HeldRepositoryRoot:      repositoryDirectory,
		OwnerDirectory:          ownerDirectory,
		Acquisition:             exactProcessAcquisition(t, fixed),
		FixedMarshalPath:        fixed,
		OwnerPrivateControlRoot: controlDirectory,
	})
	if err != nil {
		t.Fatalf("open cold repository session: %v", err)
	}
	if session.acquisition.OwnerEpoch != uint64(expectedEpoch) || session.ownerState.Acquisition != session.acquisition {
		_ = session.Close()
		t.Fatalf("cold session mismatch: expected=%d acquisition=%#v state=%#v", expectedEpoch, session.acquisition, session.ownerState)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close cold repository session: %v", err)
	}
}
