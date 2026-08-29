//go:build darwin

package allocationcontrol

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

type existingWorktreeFixture struct {
	t          *testing.T
	repository string
	worktree   string
	baseSHA    string
	authority  *existingWorktreeTestAuthority
	controller *ExistingWorktreeController
	run        DescriptorBoundRunV1
	request    ExistingWorktreeBindRequestV1
}

type existingWorktreeTestAuthority struct {
	mu               sync.Mutex
	filesystemRoot   *os.File
	repositoryParent *os.File
	repositoryRoot   *os.File
	repositoryName   string
	current          ExistingWorktreeCurrentAuthorityV1
	facts            []ExistingWorktreeAuthorityFactV1
	failAfter        map[ExistingWorktreeFactKind]int
	rejectBind       bool
	rejectRelease    bool
}

func (authority *existingWorktreeTestAuthority) Close() {
	authority.filesystemRoot.Close()
	authority.repositoryParent.Close()
	authority.repositoryRoot.Close()
}

func (authority *existingWorktreeTestAuthority) WithExistingWorktreeSession(_ context.Context, run DescriptorBoundRunV1, callback func(ExistingWorktreeAuthoritySession) error) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if validateDescriptorBoundRun(run) != nil {
		return ErrAuthorityConflict
	}
	return callback(authority)
}

func (authority *existingWorktreeTestAuthority) DescriptorGraph() (ExistingWorktreeDescriptorGraphV1, error) {
	current, err := observeDirectoryEdge(int(authority.repositoryParent.Fd()), int(authority.repositoryRoot.Fd()), authority.repositoryName)
	if err != nil {
		return ExistingWorktreeDescriptorGraphV1{}, err
	}
	var rootStat unix.Stat_t
	if unix.Fstat(int(authority.filesystemRoot.Fd()), &rootStat) != nil {
		return ExistingWorktreeDescriptorGraphV1{}, ErrFilesystemConflict
	}
	return ExistingWorktreeDescriptorGraphV1{FilesystemRoot: authority.filesystemRoot, FilesystemRootIdentity: objectIdentity(rootStat), RepositoryParent: authority.repositoryParent, RepositoryRoot: authority.repositoryRoot, RepositoryCurrentName: current}, nil
}

func (authority *existingWorktreeTestAuthority) ObserveExistingWorktree(ctx context.Context, request ExistingWorktreeBindRequestV1) (ExistingWorktreeObservationV1, error) {
	graph, err := authority.DescriptorGraph()
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	return ObserveExistingWorktreeFromGraph(ctx, graph, request)
}

func (authority *existingWorktreeTestAuthority) VerifyExistingWorktreeTarget(_ context.Context, request ExistingWorktreeBindRequestV1, expected ExistingWorktreeObservationV1) error {
	graph, err := authority.DescriptorGraph()
	if err != nil {
		return err
	}
	return VerifyExistingWorktreeTargetFromGraph(graph, request, expected)
}

func (authority *existingWorktreeTestAuthority) SyncExistingWorktreeProjection(_ context.Context, snapshot ExistingWorktreeAuthoritySnapshotV1) error {
	graph, err := authority.DescriptorGraph()
	if err != nil {
		return err
	}
	return SyncExistingWorktreeProjectionFromGraph(graph, snapshot)
}

func (authority *existingWorktreeTestAuthority) VerifyCurrentBind(_ context.Context, request ExistingWorktreeBindRequestV1, run DescriptorBoundRunV1) (ExistingWorktreeCurrentAuthorityV1, error) {
	if authority.rejectBind || authority.current.validateBind(request, run) != nil {
		return ExistingWorktreeCurrentAuthorityV1{}, ErrAuthorityConflict
	}
	return authority.current, nil
}

func (authority *existingWorktreeTestAuthority) VerifyCurrentRelease(_ context.Context, request ExistingWorktreeReleaseRequestV1, run DescriptorBoundRunV1) (ExistingWorktreeCurrentAuthorityV1, error) {
	if authority.rejectRelease || authority.current.validateRelease(request, run) != nil {
		return ExistingWorktreeCurrentAuthorityV1{}, ErrAuthorityConflict
	}
	return authority.current, nil
}

func (authority *existingWorktreeTestAuthority) Snapshot() (ExistingWorktreeAuthoritySnapshotV1, error) {
	return ExistingWorktreeAuthoritySnapshotV1{Facts: append([]ExistingWorktreeAuthorityFactV1(nil), authority.facts...)}, nil
}

func (authority *existingWorktreeTestAuthority) append(kind ExistingWorktreeFactKind, value any) (ExistingWorktreeAuthoritySnapshotV1, error) {
	snapshot, _ := authority.Snapshot()
	fact, err := newExistingWorktreeFact(uint64(len(snapshot.Facts)+1), kind, snapshot.HeadDigest(), value)
	if err != nil {
		return ExistingWorktreeAuthoritySnapshotV1{}, err
	}
	candidate := ExistingWorktreeAuthoritySnapshotV1{Facts: append(snapshot.Facts, fact)}
	if candidate.Validate() != nil {
		return ExistingWorktreeAuthoritySnapshotV1{}, ErrAuthorityConflict
	}
	authority.facts = append(authority.facts, fact)
	if authority.failAfter[kind] > 0 {
		authority.failAfter[kind]--
		return ExistingWorktreeAuthoritySnapshotV1{}, errors.New("simulated response loss")
	}
	return candidate, nil
}

func (authority *existingWorktreeTestAuthority) AppendBindIntent(_ context.Context, value ExistingWorktreeBindIntentV1) (ExistingWorktreeAuthoritySnapshotV1, error) {
	return authority.append(ExistingWorktreeFactBindIntent, value)
}
func (authority *existingWorktreeTestAuthority) AppendBindReceipt(_ context.Context, value ExistingWorktreeBindReceiptV1) (ExistingWorktreeAuthoritySnapshotV1, error) {
	return authority.append(ExistingWorktreeFactBindReceipt, value)
}
func (authority *existingWorktreeTestAuthority) AppendReleaseIntent(_ context.Context, value ExistingWorktreeReleaseIntentV1) (ExistingWorktreeAuthoritySnapshotV1, error) {
	return authority.append(ExistingWorktreeFactReleaseIntent, value)
}
func (authority *existingWorktreeTestAuthority) AppendReleaseReceipt(_ context.Context, value ExistingWorktreeReleaseReceiptV1) (ExistingWorktreeAuthoritySnapshotV1, error) {
	return authority.append(ExistingWorktreeFactReleaseReceipt, value)
}

func TestExistingWorktreeBindReleaseReplayAndProjectionRebuild(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	trackedBefore := mustReadFile(t, filepath.Join(fixture.worktree, "tracked.txt"))
	receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
	if err != nil || !equalCanonical(receipt, replay) || len(fixture.authority.facts) != 2 {
		t.Fatalf("bind replay = %+v / %v / facts=%d", replay, err, len(fixture.authority.facts))
	}
	projectionPath := fixture.projectionPath(receipt.Observation.TargetIdentityDigest)
	projection := mustReadFile(t, projectionPath)
	if bytes.Contains(projection, []byte(fixture.worktree)) || bytes.Count(projection, []byte{'\n'}) != 2 {
		t.Fatal("projection leaked locator or did not contain exact bind prefix")
	}

	releaseRun, releaseRequest := fixture.releaseRequest(receipt)
	released, err := fixture.controller.Release(context.Background(), releaseRun, releaseRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(projectionPath); err != nil {
		t.Fatal(err)
	}
	replayedRelease, err := fixture.controller.Release(context.Background(), releaseRun, releaseRequest)
	if err != nil || !equalCanonical(released, replayedRelease) || len(fixture.authority.facts) != 4 {
		t.Fatalf("release replay = %+v / %v / facts=%d", replayedRelease, err, len(fixture.authority.facts))
	}
	projection = mustReadFile(t, projectionPath)
	if bytes.Contains(projection, []byte(fixture.worktree)) || bytes.Count(projection, []byte{'\n'}) != 4 {
		t.Fatal("missing projection was not rebuilt from the four RB1 facts")
	}
	if !bytes.Equal(trackedBefore, mustReadFile(t, filepath.Join(fixture.worktree, "tracked.txt"))) || len(gitOutput(t, fixture.worktree, "status", "--porcelain=v1", "-z", "--untracked-files=all")) != 0 {
		t.Fatal("bind/release modified the existing worktree")
	}
}

func TestExistingWorktreeReleasePreservesCompletedTaskChanges(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	completed := []byte("completed task output\n")
	if err := os.WriteFile(filepath.Join(fixture.worktree, "tracked.txt"), completed, 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, fixture.worktree, "add", "tracked.txt")
	gitOutput(t, fixture.worktree, "commit", "-q", "-m", "completed task")
	completedHead := strings.TrimSpace(string(gitOutput(t, fixture.worktree, "rev-parse", "HEAD")))
	if completedHead == fixture.baseSHA {
		t.Fatal("task fixture did not advance HEAD")
	}
	releaseRun, releaseRequest := fixture.releaseRequest(receipt)
	if _, err := fixture.controller.Release(context.Background(), releaseRun, releaseRequest); err != nil {
		t.Fatal(err)
	}
	if got := mustReadFile(t, filepath.Join(fixture.worktree, "tracked.txt")); !bytes.Equal(got, completed) {
		t.Fatal("release rewrote completed task output")
	}
	if got := strings.TrimSpace(string(gitOutput(t, fixture.worktree, "rev-parse", "HEAD"))); got != completedHead {
		t.Fatal("release rewrote completed task HEAD")
	}
}

func TestExistingWorktreeResponseLossConvergesAtEveryFact(t *testing.T) {
	for _, kind := range []ExistingWorktreeFactKind{ExistingWorktreeFactBindIntent, ExistingWorktreeFactBindReceipt, ExistingWorktreeFactReleaseIntent, ExistingWorktreeFactReleaseReceipt} {
		t.Run(string(kind), func(t *testing.T) {
			fixture := newExistingWorktreeFixture(t)
			defer fixture.Close()
			if kind == ExistingWorktreeFactBindIntent || kind == ExistingWorktreeFactBindReceipt {
				fixture.authority.failAfter[kind] = 1
				if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil {
					t.Fatal("response loss was not surfaced")
				}
			}
			receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			releaseRun, releaseRequest := fixture.releaseRequest(receipt)
			if kind == ExistingWorktreeFactReleaseIntent || kind == ExistingWorktreeFactReleaseReceipt {
				fixture.authority.failAfter[kind] = 1
				if _, err := fixture.controller.Release(context.Background(), releaseRun, releaseRequest); err == nil {
					t.Fatal("release response loss was not surfaced")
				}
			}
			if _, err := fixture.controller.Release(context.Background(), releaseRun, releaseRequest); err != nil {
				t.Fatal(err)
			}
			if len(fixture.authority.facts) != 4 {
				t.Fatalf("facts=%d, want exactly four", len(fixture.authority.facts))
			}
		})
	}
}

func TestExistingWorktreeHostileInputsFailBeforeAuthorityWrite(t *testing.T) {
	t.Run("forged-current-authority", func(t *testing.T) {
		fixture := newExistingWorktreeFixture(t)
		defer fixture.Close()
		fixture.authority.rejectBind = true
		if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil || len(fixture.authority.facts) != 0 {
			t.Fatal("forged binding reached RB1")
		}
	})
	t.Run("forged-release-authority", func(t *testing.T) {
		fixture := newExistingWorktreeFixture(t)
		defer fixture.Close()
		receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
		if err != nil {
			t.Fatal(err)
		}
		releaseRun, releaseRequest := fixture.releaseRequest(receipt)
		fixture.authority.rejectRelease = true
		if _, err := fixture.controller.Release(context.Background(), releaseRun, releaseRequest); err == nil || len(fixture.authority.facts) != 2 {
			t.Fatal("forged release authority reached RB1")
		}
	})
	t.Run("wrong-base", func(t *testing.T) {
		fixture := newExistingWorktreeFixture(t)
		defer fixture.Close()
		fixture.request.ExpectedBaseSHA = strings.Repeat("a", 40)
		if fixture.request.ExpectedBaseSHA == fixture.baseSHA {
			fixture.request.ExpectedBaseSHA = strings.Repeat("b", 40)
		}
		fixture.request.RequestDigest = ""
		if err := fixture.request.Seal(); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil || len(fixture.authority.facts) != 0 {
			t.Fatal("wrong base reached RB1")
		}
	})
	t.Run("dirty", func(t *testing.T) {
		fixture := newExistingWorktreeFixture(t)
		defer fixture.Close()
		if err := os.WriteFile(filepath.Join(fixture.worktree, "untracked"), []byte("dirty"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil || len(fixture.authority.facts) != 0 {
			t.Fatal("dirty target reached RB1")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		fixture := newExistingWorktreeFixture(t)
		defer fixture.Close()
		alias := filepath.Join(filepath.Dir(fixture.worktree), "worktree-alias")
		if err := os.Symlink(fixture.worktree, alias); err != nil {
			t.Fatal(err)
		}
		fixture.request.WorktreePath = alias
		fixture.request.RequestDigest = ""
		if err := fixture.request.Seal(); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil || len(fixture.authority.facts) != 0 {
			t.Fatal("symlink target reached RB1")
		}
	})
}

func TestExistingWorktreeBindRechecksExactCurrentAuthorityBeforeWrite(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExistingWorktreeCurrentAuthorityV1)
	}{
		{"reservation-fact", func(current *ExistingWorktreeCurrentAuthorityV1) {
			current.ReservationFactDigest = testExistingDigest("other-reservation")
		}},
		{"attempt-opened-fact", func(current *ExistingWorktreeCurrentAuthorityV1) {
			current.AttemptOpenedFactDigest = testExistingDigest("other-opened")
		}},
		{"attempt-authority-head", func(current *ExistingWorktreeCurrentAuthorityV1) {
			current.AttemptAuthorityHeadDigest = testExistingDigest("later-attempt-head")
		}},
		{"dispatch-generation", func(current *ExistingWorktreeCurrentAuthorityV1) { current.Generation++ }},
		{"dispatch-fencing", func(current *ExistingWorktreeCurrentAuthorityV1) {
			current.FencingTokenDigest = testExistingDigest("other-fencing")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExistingWorktreeFixture(t)
			defer fixture.Close()
			current := fixture.authority.current
			test.mutate(&current)
			fixture.authority.current = current
			if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil || len(fixture.authority.facts) != 0 {
				t.Fatal("stale or forged current authority reached RB1")
			}
		})
	}
}

func TestExistingWorktreeReleaseRechecksExactTerminalAuthorityBeforeWrite(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ExistingWorktreeCurrentAuthorityV1)
	}{
		{"run-head", func(current *ExistingWorktreeCurrentAuthorityV1) {
			current.RunAuthorityHeadDigest = testExistingDigest("other-run-terminal-head")
		}},
		{"attempt-head", func(current *ExistingWorktreeCurrentAuthorityV1) {
			current.AttemptAuthorityHeadDigest = testExistingDigest("other-attempt-terminal-head")
		}},
		{"terminalization", func(current *ExistingWorktreeCurrentAuthorityV1) { current.TerminalizationID = "other-terminalization" }},
		{"cleanup-binding", func(current *ExistingWorktreeCurrentAuthorityV1) {
			current.CleanupBindingDigest = testExistingDigest("other-cleanup")
		}},
		{"process-terminal", func(current *ExistingWorktreeCurrentAuthorityV1) {
			current.ProcessTerminalFactDigest = testExistingDigest("other-process-terminal")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExistingWorktreeFixture(t)
			defer fixture.Close()
			receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			releaseRun, releaseRequest := fixture.releaseRequest(receipt)
			current := fixture.authority.current
			test.mutate(&current)
			fixture.authority.current = current
			if _, err := fixture.controller.Release(context.Background(), releaseRun, releaseRequest); err == nil || len(fixture.authority.facts) != 2 {
				t.Fatal("stale or forged terminal authority reached RB1 release")
			}
		})
	}
}

func TestExistingWorktreeCurrentNameDetectsRenameReplacementAndRenameBack(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	parentFD, err := openExistingDirectoryPath(filepath.Dir(fixture.worktree))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parentFD)
	leaf := filepath.Base(fixture.worktree)
	oldFD, err := unix.Openat(parentFD, leaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(oldFD)
	before, err := observeDirectoryEdge(parentFD, oldFD, leaf)
	if err != nil {
		t.Fatal(err)
	}
	moved := fixture.worktree + "-moved"
	if err := os.Rename(fixture.worktree, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := observeDirectoryEdge(parentFD, oldFD, leaf); err == nil {
		t.Fatal("replacement passed held current-name check")
	}
	if err := os.Remove(fixture.worktree); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, fixture.worktree); err != nil {
		t.Fatal(err)
	}
	after, err := observeDirectoryEdge(parentFD, oldFD, leaf)
	if err != nil {
		t.Fatal(err)
	}
	if equalCanonical(before, after) {
		t.Fatal("rename-away/back ABA retained the same mutation identity")
	}
}

func TestExistingWorktreeLocatorDetectsAncestorReplacement(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	ancestor := filepath.Dir(fixture.worktree)
	beforeFD, before, err := openDirectoryFromHeldFilesystemRootWithLineage(fixture.authority.filesystemRoot, ancestor)
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(beforeFD)
	moved := ancestor + "-moved"
	if err := os.Rename(ancestor, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	afterFD, after, err := openDirectoryFromHeldFilesystemRootWithLineage(fixture.authority.filesystemRoot, ancestor)
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(afterFD)
	if err := os.Remove(ancestor); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, ancestor); err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("ancestor replacement retained the same held locator lineage")
	}
}

func TestExistingWorktreeReleasedReservationAndAttemptCannotRebind(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	releaseRun, releaseRequest := fixture.releaseRequest(receipt)
	if _, err := fixture.controller.Release(context.Background(), releaseRun, releaseRequest); err != nil {
		t.Fatal(err)
	}
	second := fixture.request
	second.Binding.AllocationID = "allocation-2"
	second.Binding.LeaseID = "lease-2"
	second.RequestDigest = ""
	second.RunAuthorityHeadDigest = releaseRun.AuthorityHeadDigest
	second.RunDirectoryIdentity = releaseRun.DirectoryIdentity
	if err := second.Seal(); err != nil {
		t.Fatal(err)
	}
	fixture.authority.current = currentAuthorityForRequest(second, releaseRun)
	if _, err := fixture.controller.Bind(context.Background(), releaseRun, second); err == nil || len(fixture.authority.facts) != 4 {
		t.Fatal("released reservation/Attempt was rebound")
	}
}

func TestExistingWorktreeReservationAndOpenedFactAreRepositoryGlobalForever(t *testing.T) {
	tests := []struct {
		name               string
		reuseReservation   bool
		reuseAttemptOpened bool
	}{
		{name: "reservation-fact-across-run", reuseReservation: true},
		{name: "attempt-opened-fact-across-run", reuseAttemptOpened: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExistingWorktreeFixture(t)
			defer fixture.Close()
			receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			releaseRun, releaseRequest := fixture.releaseRequest(receipt)
			if _, err := fixture.controller.Release(context.Background(), releaseRun, releaseRequest); err != nil {
				t.Fatal(err)
			}

			secondRun := releaseRun
			secondRun.RunID = "run-2"
			secondRun.AuthorityHeadDigest = testExistingDigest("run-2-ready-head")
			second := fixture.request
			second.Binding.RunID = secondRun.RunID
			second.Binding.AttemptID = "attempt-2"
			second.Binding.AllocationID = "allocation-2"
			second.Binding.LeaseID = "lease-2"
			second.Binding.ReservationFactDigest = testExistingDigest("reservation-fact-2")
			second.Binding.AttemptOpenedFactDigest = testExistingDigest("attempt-opened-2")
			if test.reuseReservation {
				second.Binding.ReservationFactDigest = fixture.request.Binding.ReservationFactDigest
			}
			if test.reuseAttemptOpened {
				second.Binding.AttemptOpenedFactDigest = fixture.request.Binding.AttemptOpenedFactDigest
			}
			second.RunDirectoryIdentity = secondRun.DirectoryIdentity
			second.RunAuthorityHeadDigest = secondRun.AuthorityHeadDigest
			second.RequestDigest = ""
			if err := second.Seal(); err != nil {
				t.Fatal(err)
			}
			fixture.authority.current = currentAuthorityForRequest(second, secondRun)
			if _, err := fixture.controller.Bind(context.Background(), secondRun, second); err == nil || len(fixture.authority.facts) != 4 {
				t.Fatal("repository-global historical authority identity was rebound")
			}
		})
	}
}

func TestExistingWorktreeProjectionCorruptionFailsClosed(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	path := fixture.projectionPath(receipt.Observation.TargetIdentityDigest)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("forged\n"); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil || len(fixture.authority.facts) != 2 {
		t.Fatal("corrupt/ahead projection was repaired or changed authority")
	}
}

func TestExistingWorktreePartialProjectionTailIsRecoveredOnlyAsExactPrefix(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	path := fixture.projectionPath(receipt.Observation.TargetIdentityDigest)
	raw := mustReadFile(t, path)
	if len(raw) < 16 {
		t.Fatal("projection fixture too small")
	}
	if err := os.Truncate(path, int64(len(raw)-8)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err != nil {
		t.Fatal(err)
	}
	if rebuilt := mustReadFile(t, path); !bytes.Equal(rebuilt, raw) {
		t.Fatal("matching partial tail did not converge to exact authority projection")
	}
}

func newExistingWorktreeFixture(t *testing.T) *existingWorktreeFixture {
	t.Helper()
	root := canonicalTempDir(t)
	repository := filepath.Join(root, "repository")
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repository, "init", "-q")
	gitOutput(t, repository, "config", "user.email", "marshal@example.invalid")
	gitOutput(t, repository, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitOutput(t, repository, "add", "tracked.txt")
	gitOutput(t, repository, "commit", "-q", "-m", "base")
	baseSHA := strings.TrimSpace(string(gitOutput(t, repository, "rev-parse", "HEAD")))
	gitOutput(t, repository, "worktree", "add", "-q", "--detach", worktree, baseSHA)

	filesystemRoot, err := os.Open("/")
	if err != nil {
		t.Fatal(err)
	}
	repositoryParent, err := os.Open(filepath.Dir(repository))
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot, err := os.Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Join(root, "run")
	if err := os.Mkdir(runPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runDirectory, err := os.Open(runPath)
	if err != nil {
		t.Fatal(err)
	}
	runIdentity := identityForFile(t, runDirectory)
	run := DescriptorBoundRunV1{RunID: "run-1", Directory: runDirectory, DirectoryIdentity: runIdentity, AuthorityHeadDigest: testExistingDigest("run-ready-head")}
	binding := ExistingWorktreeBindingV1{
		AuthorityNamespaceID: "authority-1", RepositoryOwnerDigest: testExistingDigest("repository-owner"), TaskID: "task-1", RunID: run.RunID, AttemptID: "attempt-1",
		ReservationFactDigest: testExistingDigest("reservation-fact"), AttemptOpenedFactDigest: testExistingDigest("attempt-opened"), AllocationID: "allocation-1", LeaseID: "lease-1", Generation: 1,
		FencingTokenDigest: testExistingDigest("fencing"), FrozenInputsDigest: testExistingDigest("frozen-inputs"), ExpectedAttemptSequence: 7,
	}
	request := ExistingWorktreeBindRequestV1{Binding: binding, WorktreePath: worktree, ExpectedWorktreeIdentity: identityForPath(t, worktree), ExpectedBaseSHA: baseSHA, RunDirectoryIdentity: runIdentity, RunAuthorityHeadDigest: run.AuthorityHeadDigest}
	if err := request.Seal(); err != nil {
		t.Fatal(err)
	}
	authority := &existingWorktreeTestAuthority{filesystemRoot: filesystemRoot, repositoryParent: repositoryParent, repositoryRoot: repositoryRoot, repositoryName: filepath.Base(repository), current: currentAuthorityForRequest(request, run), failAfter: make(map[ExistingWorktreeFactKind]int)}
	controller, err := NewExistingWorktreeController(authority)
	if err != nil {
		t.Fatal(err)
	}
	return &existingWorktreeFixture{t: t, repository: repository, worktree: worktree, baseSHA: baseSHA, authority: authority, controller: controller, run: run, request: request}
}

func (fixture *existingWorktreeFixture) Close() {
	fixture.run.Directory.Close()
	fixture.authority.Close()
}
func (fixture *existingWorktreeFixture) projectionPath(targetDigest string) string {
	return filepath.Join(fixture.repository, ".marshal", existingWorktreeRuntimeDirectory, ExistingWorktreeProjectionDirectory, strings.TrimPrefix(targetDigest, "sha256:")+".jsonl")
}

func (fixture *existingWorktreeFixture) releaseRequest(receipt ExistingWorktreeBindReceiptV1) (DescriptorBoundRunV1, ExistingWorktreeReleaseRequestV1) {
	releaseRun := fixture.run
	releaseRun.AuthorityHeadDigest = testExistingDigest("run-terminal-head")
	request := ExistingWorktreeReleaseRequestV1{Binding: fixture.request.Binding, BindingReceiptDigest: receipt.ReceiptDigest, TerminalizationID: "terminalization-1", CleanupBindingDigest: testExistingDigest("cleanup-binding"), ProcessTerminalFactDigest: testExistingDigest("process-terminal"), CleanupDisposition: "preserved", RunAuthorityHeadDigest: releaseRun.AuthorityHeadDigest, AttemptAuthorityHeadDigest: testExistingDigest("attempt-terminal-head")}
	if err := request.Seal(); err != nil {
		fixture.t.Fatal(err)
	}
	current := currentAuthorityForRequest(fixture.request, releaseRun)
	current.AttemptAuthorityHeadDigest = request.AttemptAuthorityHeadDigest
	current.TerminalizationID = request.TerminalizationID
	current.CleanupBindingDigest = request.CleanupBindingDigest
	current.ProcessTerminalFactDigest = request.ProcessTerminalFactDigest
	current.CleanupDisposition = request.CleanupDisposition
	fixture.authority.current = current
	return releaseRun, request
}

func currentAuthorityForRequest(request ExistingWorktreeBindRequestV1, run DescriptorBoundRunV1) ExistingWorktreeCurrentAuthorityV1 {
	binding := request.Binding
	return ExistingWorktreeCurrentAuthorityV1{AuthorityNamespaceID: binding.AuthorityNamespaceID, RepositoryOwnerDigest: binding.RepositoryOwnerDigest, TaskID: binding.TaskID, RunID: binding.RunID, RunAuthorityHeadDigest: run.AuthorityHeadDigest, AttemptID: binding.AttemptID, AttemptAuthorityHeadDigest: binding.AttemptOpenedFactDigest, ReservationFactDigest: binding.ReservationFactDigest, AttemptOpenedFactDigest: binding.AttemptOpenedFactDigest, AllocationID: binding.AllocationID, LeaseID: binding.LeaseID, Generation: binding.Generation, FencingTokenDigest: binding.FencingTokenDigest, FrozenInputsDigest: binding.FrozenInputsDigest, ExpectedAttemptSequence: binding.ExpectedAttemptSequence, WorktreePath: request.WorktreePath, ExpectedWorktreeIdentity: request.ExpectedWorktreeIdentity, ExpectedBaseSHA: request.ExpectedBaseSHA}
}

func identityForFile(t *testing.T, file *os.File) ObjectIdentityV1 {
	t.Helper()
	var stat unix.Stat_t
	if unix.Fstat(int(file.Fd()), &stat) != nil {
		t.Fatal("fstat")
	}
	return objectIdentity(stat)
}
func identityForPath(t *testing.T, path string) ObjectIdentityV1 {
	t.Helper()
	var stat unix.Stat_t
	if unix.Stat(path, &stat) != nil {
		t.Fatal("stat")
	}
	return objectIdentity(stat)
}
func testExistingDigest(label string) string { return canonical.DigestBytes([]byte(label)) }
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func gitOutput(t *testing.T, root string, args ...string) []byte {
	t.Helper()
	command := exec.Command("/usr/bin/git", append([]string{"-C", root}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return output
}
