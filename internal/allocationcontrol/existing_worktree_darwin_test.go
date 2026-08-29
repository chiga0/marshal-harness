//go:build darwin

package allocationcontrol

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	repositoryCommon *os.File
	repositoryName   string
	current          ExistingWorktreeCurrentAuthorityV1
	attemptRevision  uint64
	attemptHead      string
	facts            []ExistingWorktreeAttemptFactV1
	failAfter        map[ExistingWorktreeFactKind]int
	rejectBind       bool
	rejectRelease    bool
	beforeTarget     func()
}

func TestLinkedExistingWorktreeDescriptorGraphBindsDotGitAndCommonDirectoryEdges(t *testing.T) {
	rootPath := t.TempDir()
	repositoryPath := filepath.Join(rootPath, "repository")
	commonParentPath := filepath.Join(rootPath, "common-parent")
	commonPath := filepath.Join(commonParentPath, "admin")
	for _, directory := range []string{repositoryPath, commonParentPath, commonPath} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	dotGitPath := filepath.Join(repositoryPath, ".git")
	if err := os.WriteFile(dotGitPath, []byte("gitdir: ../common-parent/admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	openDirectory := func(path string) *os.File {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	root := openDirectory(rootPath)
	repository := openDirectory(repositoryPath)
	commonParent := openDirectory(commonParentPath)
	common := openDirectory(commonPath)
	dotGit, err := os.Open(dotGitPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dotGit.Close() })

	graph, err := NewLinkedExistingWorktreeDescriptorGraph(root, root, repository, dotGit, commonParent, common, "repository", "admin")
	if err != nil {
		t.Fatalf("construct linked graph: %v", err)
	}
	if graph.RepositoryDotGitCurrentName.RelativeName != ".git" || graph.RepositoryCommonGitCurrentName.RelativeName != "admin" {
		t.Fatal("linked graph did not freeze both current-name edges")
	}
	if graph.RepositoryDotGitDigest != digestBytes([]byte("gitdir: ../common-parent/admin\n")) {
		t.Fatal("linked graph did not freeze held .git bytes")
	}
	detachedPath := dotGitPath + ".detached"
	graph.beforeDotGitRead = func() {
		if err := os.Rename(dotGitPath, detachedPath); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dotGitPath, []byte("gitdir: ../attacker/admin\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	graph.afterDotGitRead = func() {
		if err := os.Remove(dotGitPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(detachedPath, dotGitPath); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := readGraphDotGitAt(graph, int(repository.Fd())); !errors.Is(err, ErrFilesystemConflict) {
		t.Fatalf("validate-read-restore .git ABA must fail closed, got %v", err)
	}
	graph.beforeDotGitRead = nil
	graph.afterDotGitRead = nil
	if err := os.Rename(dotGitPath, dotGitPath+".detached"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dotGitPath, []byte("gitdir: ../common-parent/admin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateExistingWorktreeDescriptorGraph(graph); !errors.Is(err, ErrFilesystemConflict) {
		t.Fatalf("replacement .git file must fail closed, got %v", err)
	}
}

func (authority *existingWorktreeTestAuthority) Close() {
	authority.filesystemRoot.Close()
	authority.repositoryParent.Close()
	authority.repositoryRoot.Close()
	authority.repositoryCommon.Close()
}

func (authority *existingWorktreeTestAuthority) WithCurrentExistingWorktreeBind(_ context.Context, run DescriptorBoundRunV1, request ExistingWorktreeBindRequestV1, callback func(ExistingWorktreeAuthoritySession) error) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.current.AttemptAuthorityHeadDigest = authority.attemptHead
	if validateDescriptorBoundRun(run) != nil || authority.rejectBind || authority.current.validateBind(request, run) != nil {
		return ErrAuthorityConflict
	}
	return callback(authority)
}

func (authority *existingWorktreeTestAuthority) WithCurrentExistingWorktreeRelease(_ context.Context, run DescriptorBoundRunV1, request ExistingWorktreeReleaseRequestV1, callback func(ExistingWorktreeAuthoritySession) error) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.current.AttemptAuthorityHeadDigest = authority.attemptHead
	if validateDescriptorBoundRun(run) != nil || authority.rejectRelease || authority.current.validateRelease(request, run) != nil {
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
	common, err := observeDirectoryEdge(int(authority.repositoryRoot.Fd()), int(authority.repositoryCommon.Fd()), ".git")
	if err != nil {
		return ExistingWorktreeDescriptorGraphV1{}, err
	}
	return ExistingWorktreeDescriptorGraphV1{FilesystemRoot: authority.filesystemRoot, FilesystemRootIdentity: objectIdentity(rootStat), RepositoryParent: authority.repositoryParent, RepositoryRoot: authority.repositoryRoot, RepositoryCurrentName: current, RepositoryCommonGitDirectory: authority.repositoryCommon, RepositoryCommonGitCurrentName: common}, nil
}

func (authority *existingWorktreeTestAuthority) CurrentAuthority() ExistingWorktreeCurrentAuthorityV1 {
	return authority.current
}

func (authority *existingWorktreeTestAuthority) WithExistingWorktreeTarget(ctx context.Context, request ExistingWorktreeBindRequestV1, expected *ExistingWorktreeObservationV1, callback func(ExistingWorktreeTargetSession) error) error {
	graph, err := authority.DescriptorGraph()
	if err != nil {
		return err
	}
	return WithExistingWorktreeTargetFromGraph(ctx, graph, request, expected, func(target ExistingWorktreeTargetSession) error {
		if authority.beforeTarget != nil {
			hook := authority.beforeTarget
			authority.beforeTarget = nil
			hook()
		}
		return callback(target)
	})
}

func (authority *existingWorktreeTestAuthority) SyncExistingWorktreeProjection(_ context.Context, snapshot ExistingWorktreeAuthoritySnapshotV1) error {
	graph, err := authority.DescriptorGraph()
	if err != nil {
		return err
	}
	return SyncExistingWorktreeProjectionFromGraph(graph, snapshot)
}

func (authority *existingWorktreeTestAuthority) Snapshot() (ExistingWorktreeAuthoritySnapshotV1, error) {
	return ExistingWorktreeAuthoritySnapshotV1{CurrentAttemptRevision: authority.attemptRevision, CurrentAttemptHeadDigest: authority.attemptHead, Facts: append([]ExistingWorktreeAttemptFactV1(nil), authority.facts...)}, nil
}

func (authority *existingWorktreeTestAuthority) append(kind ExistingWorktreeFactKind, value any) (ExistingWorktreeAuthoritySnapshotV1, error) {
	snapshot, _ := authority.Snapshot()
	raw, err := canonicalValue(value)
	if err != nil {
		return ExistingWorktreeAuthoritySnapshotV1{}, err
	}
	attemptKey := testExistingDigest(authority.current.RunID + "\x00" + authority.current.AttemptID)
	authority.attemptRevision++
	material := struct {
		AttemptKey    string                   `json:"attemptKey"`
		Revision      uint64                   `json:"revision"`
		Kind          ExistingWorktreeFactKind `json:"kind"`
		Previous      string                   `json:"previous"`
		PayloadDigest string                   `json:"payloadDigest"`
	}{attemptKey, authority.attemptRevision, kind, authority.attemptHead, digestBytes(raw)}
	factDigest, err := digestValue(material)
	if err != nil {
		return ExistingWorktreeAuthoritySnapshotV1{}, err
	}
	fact := ExistingWorktreeAttemptFactV1{AttemptKey: attemptKey, AttemptRevision: authority.attemptRevision, Kind: kind, PreviousAttemptHeadDigest: authority.attemptHead, Payload: raw, PayloadDigest: digestBytes(raw), AttemptFactDigest: factDigest}
	candidate := ExistingWorktreeAuthoritySnapshotV1{CurrentAttemptRevision: authority.attemptRevision, CurrentAttemptHeadDigest: factDigest, Facts: append(snapshot.Facts, fact)}
	if candidate.Validate() != nil {
		return ExistingWorktreeAuthoritySnapshotV1{}, ErrAuthorityConflict
	}
	authority.facts = append(authority.facts, fact)
	authority.attemptHead = factDigest
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
	targetBefore := snapshotFilesystemTree(t, fixture.worktree)
	adminBefore := snapshotFilesystemTree(t, mustExistingWorktreeAdminPath(t, fixture.worktree))
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
	assertFilesystemTreeEqual(t, "bind target", targetBefore, snapshotFilesystemTree(t, fixture.worktree))
	assertFilesystemTreeEqual(t, "bind git admin", adminBefore, snapshotFilesystemTree(t, mustExistingWorktreeAdminPath(t, fixture.worktree)))

	releaseRun, releaseRequest := fixture.releaseRequest(receipt)
	releaseTargetBefore := snapshotFilesystemTree(t, fixture.worktree)
	releaseAdminBefore := snapshotFilesystemTree(t, mustExistingWorktreeAdminPath(t, fixture.worktree))
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
	assertFilesystemTreeEqual(t, "release target", releaseTargetBefore, snapshotFilesystemTree(t, fixture.worktree))
	assertFilesystemTreeEqual(t, "release git admin", releaseAdminBefore, snapshotFilesystemTree(t, mustExistingWorktreeAdminPath(t, fixture.worktree)))
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
	if err := os.WriteFile(filepath.Join(fixture.worktree, "untracked-output"), []byte("preserve me\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked.txt", filepath.Join(fixture.worktree, "task-link")); err != nil {
		t.Fatal(err)
	}
	completedHead := strings.TrimSpace(string(gitOutput(t, fixture.worktree, "rev-parse", "HEAD")))
	if completedHead == fixture.baseSHA {
		t.Fatal("task fixture did not advance HEAD")
	}
	releaseRun, releaseRequest := fixture.releaseRequest(receipt)
	targetBefore := snapshotFilesystemTree(t, fixture.worktree)
	adminBefore := snapshotFilesystemTree(t, mustExistingWorktreeAdminPath(t, fixture.worktree))
	if _, err := fixture.controller.Release(context.Background(), releaseRun, releaseRequest); err != nil {
		t.Fatal(err)
	}
	assertFilesystemTreeEqual(t, "release task target", targetBefore, snapshotFilesystemTree(t, fixture.worktree))
	assertFilesystemTreeEqual(t, "release task git admin", adminBefore, snapshotFilesystemTree(t, mustExistingWorktreeAdminPath(t, fixture.worktree)))
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
	t.Run("foreign-repository-common-dir", func(t *testing.T) {
		fixture := newExistingWorktreeFixture(t)
		defer fixture.Close()
		foreignRepository := filepath.Join(filepath.Dir(fixture.repository), "foreign-repository")
		foreignWorktree := filepath.Join(filepath.Dir(fixture.repository), "foreign-worktree")
		if err := os.Mkdir(foreignRepository, 0o700); err != nil {
			t.Fatal(err)
		}
		gitOutput(t, foreignRepository, "init", "-q")
		gitOutput(t, foreignRepository, "config", "user.email", "marshal@example.invalid")
		gitOutput(t, foreignRepository, "config", "user.name", "Marshal Test")
		if err := os.WriteFile(filepath.Join(foreignRepository, "tracked.txt"), []byte("foreign\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitOutput(t, foreignRepository, "add", "tracked.txt")
		gitOutput(t, foreignRepository, "commit", "-q", "-m", "foreign base")
		base := strings.TrimSpace(string(gitOutput(t, foreignRepository, "rev-parse", "HEAD")))
		gitOutput(t, foreignRepository, "worktree", "add", "-q", "--detach", foreignWorktree, base)
		makeExistingWorktreePrivate(t, foreignWorktree)
		fixture.request.WorktreePath = foreignWorktree
		fixture.request.ExpectedWorktreeIdentity = identityForPath(t, foreignWorktree)
		fixture.request.ExpectedBaseSHA = base
		fixture.request.RequestDigest = ""
		if err := fixture.request.Seal(); err != nil {
			t.Fatal(err)
		}
		fixture.authority.current = currentAuthorityForRequest(fixture.request, fixture.run)
		if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil || len(fixture.authority.facts) != 0 {
			t.Fatal("worktree backed by another repository common-dir reached RB1")
		}
	})
}

func TestExistingWorktreeFinalTargetAndAdminRequireStablePrivateOwnership(t *testing.T) {
	tests := []struct {
		name string
		path func(*existingWorktreeFixture) string
	}{
		{name: "target-initial-unsafe-mode", path: func(fixture *existingWorktreeFixture) string { return fixture.worktree }},
		{name: "admin-initial-unsafe-mode", path: func(fixture *existingWorktreeFixture) string {
			return mustExistingWorktreeAdminPath(t, fixture.worktree)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExistingWorktreeFixture(t)
			defer fixture.Close()
			if err := os.Chmod(test.path(fixture), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil || len(fixture.authority.facts) != 0 {
				t.Fatal("unsafe final target/admin mode reached RB1")
			}
		})
	}

	for _, test := range tests {
		t.Run(strings.Replace(test.name, "initial-unsafe-mode", "mode-drift-restore", 1), func(t *testing.T) {
			fixture := newExistingWorktreeFixture(t)
			defer fixture.Close()
			fixture.authority.beforeTarget = func() {
				path := test.path(fixture)
				if err := os.Chmod(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil || len(fixture.authority.facts) != 0 {
				t.Fatal("final target/admin mode drift-and-restore reached RB1")
			}
		})
	}
}

func TestExistingWorktreeHeldTargetRejectsRenameAwayAndSameObjectBackBeforeAppend(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	fixture.authority.beforeTarget = func() {
		moved := fixture.worktree + "-moved"
		if err := os.Rename(fixture.worktree, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(moved, fixture.worktree); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil || len(fixture.authority.facts) != 0 {
		t.Fatal("rename-away/back ABA reached RB1 append")
	}
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
		{"terminal-attempt-head", func(current *ExistingWorktreeCurrentAuthorityV1) {
			current.TerminalAttemptHeadDigest = testExistingDigest("other-attempt-terminal-head")
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

func TestExistingWorktreePartialProjectionTailFailsClosedWithoutRepair(t *testing.T) {
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
	partial := mustReadFile(t, path)
	if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err == nil || len(fixture.authority.facts) != 2 {
		t.Fatal("partial projection tail was repaired or changed authority")
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, partial) {
		t.Fatal("partial projection tail was truncated or overwritten")
	}
}

func TestExistingWorktreeProjectionPreflightIsGlobalBeforeAnyWrite(t *testing.T) {
	first := newExistingWorktreeFixture(t)
	defer first.Close()
	second := newDistinctExistingWorktreeFixture(t)
	defer second.Close()
	firstReceipt, err := first.controller.Bind(context.Background(), first.run, first.request)
	if err != nil {
		t.Fatal(err)
	}
	secondReceipt, err := second.controller.Bind(context.Background(), second.run, second.request)
	if err != nil {
		t.Fatal(err)
	}
	combined := ExistingWorktreeAuthoritySnapshotV1{
		CurrentAttemptRevision:   first.authority.attemptRevision,
		CurrentAttemptHeadDigest: first.authority.attemptHead,
		Facts:                    append(append([]ExistingWorktreeAttemptFactV1(nil), first.authority.facts...), second.authority.facts...),
	}
	if err := combined.Validate(); err != nil {
		t.Fatal(err)
	}
	graph, err := first.authority.DescriptorGraph()
	if err != nil || SyncExistingWorktreeProjectionFromGraph(graph, combined) != nil {
		t.Fatalf("seed combined projection: %v", err)
	}
	paths := []string{first.projectionPath(firstReceipt.Observation.TargetIdentityDigest), first.projectionPath(secondReceipt.Observation.TargetIdentityDigest)}
	sort.Strings(paths)
	behindSource := mustReadFile(t, paths[0])
	withoutFinalNewline := behindSource[:len(behindSource)-1]
	lastRecord := bytes.LastIndexByte(withoutFinalNewline, '\n')
	if lastRecord < 0 {
		t.Fatal("projection did not contain two records")
	}
	behind := append([]byte(nil), behindSource[:lastRecord+1]...)
	if err := os.WriteFile(paths[0], behind, 0o600); err != nil {
		t.Fatal(err)
	}
	corruptFile, err := os.OpenFile(paths[1], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := corruptFile.WriteString("forged\n"); err != nil {
		corruptFile.Close()
		t.Fatal(err)
	}
	corruptFile.Close()
	if err := SyncExistingWorktreeProjectionFromGraph(graph, combined); err == nil {
		t.Fatal("later corrupt entry did not fail global projection preflight")
	}
	if after := mustReadFile(t, paths[0]); !bytes.Equal(after, behind) {
		t.Fatal("earlier behind entry was extended before later corruption was discovered")
	}
}

func TestExistingWorktreeProjectionHeldDirectoryAndLockRejectRenameBackABA(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.authority.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	graph, err := fixture.authority.DescriptorGraph()
	if err != nil {
		t.Fatal(err)
	}
	projectionDirectory := filepath.Join(fixture.repository, ".marshal", existingWorktreeRuntimeDirectory, ExistingWorktreeProjectionDirectory)

	t.Run("directory", func(t *testing.T) {
		projection, err := openExistingWorktreeProjection(graph)
		if err != nil {
			t.Fatal(err)
		}
		defer projection.Close()
		moved := projectionDirectory + "-moved"
		if err := os.Rename(projectionDirectory, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(moved, projectionDirectory); err != nil {
			t.Fatal(err)
		}
		if err := projection.Sync(snapshot); err == nil {
			t.Fatal("projection directory rename-away/back passed held current-name check")
		}
	})

	t.Run("lock", func(t *testing.T) {
		projection, err := openExistingWorktreeProjection(graph)
		if err != nil {
			t.Fatal(err)
		}
		defer projection.Close()
		lockPath := filepath.Join(projectionDirectory, existingWorktreeProjectionLock)
		moved := lockPath + "-moved"
		if err := os.Rename(lockPath, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(moved, lockPath); err != nil {
			t.Fatal(err)
		}
		if err := projection.Sync(snapshot); err == nil {
			t.Fatal("projection lock rename-away/back passed held current-name check")
		}
	})
}

func TestExistingWorktreeProjectionDetectsLaterSameFileMutationAfterPreflightBeforeAnyLiveWrite(t *testing.T) {
	first := newExistingWorktreeFixture(t)
	defer first.Close()
	second := newDistinctExistingWorktreeFixture(t)
	defer second.Close()
	firstReceipt, err := first.controller.Bind(context.Background(), first.run, first.request)
	if err != nil {
		t.Fatal(err)
	}
	secondReceipt, err := second.controller.Bind(context.Background(), second.run, second.request)
	if err != nil {
		t.Fatal(err)
	}
	combined := ExistingWorktreeAuthoritySnapshotV1{
		CurrentAttemptRevision:   first.authority.attemptRevision,
		CurrentAttemptHeadDigest: first.authority.attemptHead,
		Facts:                    append(append([]ExistingWorktreeAttemptFactV1(nil), first.authority.facts...), second.authority.facts...),
	}
	if err := combined.Validate(); err != nil {
		t.Fatal(err)
	}
	graph, err := first.authority.DescriptorGraph()
	if err != nil || SyncExistingWorktreeProjectionFromGraph(graph, combined) != nil {
		t.Fatalf("seed combined projection: %v", err)
	}
	paths := []string{first.projectionPath(firstReceipt.Observation.TargetIdentityDigest), first.projectionPath(secondReceipt.Observation.TargetIdentityDigest)}
	sort.Strings(paths)
	complete := mustReadFile(t, paths[0])
	withoutFinalNewline := complete[:len(complete)-1]
	lastRecord := bytes.LastIndexByte(withoutFinalNewline, '\n')
	if lastRecord < 0 {
		t.Fatal("projection did not contain two records")
	}
	behind := append([]byte(nil), complete[:lastRecord+1]...)
	if err := os.WriteFile(paths[0], behind, 0o600); err != nil {
		t.Fatal(err)
	}
	projection, err := openExistingWorktreeProjection(graph)
	if err != nil {
		t.Fatal(err)
	}
	defer projection.Close()
	var mutated []byte
	projection.afterPreflight = func() {
		mutated = append(mustReadFile(t, paths[1]), []byte("concurrent-forgery\n")...)
		if err := os.WriteFile(paths[1], mutated, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := projection.Sync(combined); err == nil {
		t.Fatal("later same-file mutation after preflight was accepted")
	}
	if after := mustReadFile(t, paths[0]); !bytes.Equal(after, behind) {
		t.Fatal("earlier behind entry changed before later plan conflict")
	}
	if after := mustReadFile(t, paths[1]); !bytes.Equal(after, mutated) {
		t.Fatal("RB1 changed the concurrently-mutated later entry")
	}
	stagePath := filepath.Join(first.repository, ".marshal", existingWorktreeRuntimeDirectory, existingWorktreeProjectionStage)
	if _, err := os.Lstat(stagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-commit conflict left projection stage: %v", err)
	}
}

func TestExistingWorktreeProjectionPreCommitFailureCleansStageWithoutLiveWrite(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.authority.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	graph, err := fixture.authority.DescriptorGraph()
	if err != nil {
		t.Fatal(err)
	}
	path := fixture.projectionPath(receipt.Observation.TargetIdentityDigest)
	complete := mustReadFile(t, path)
	withoutFinalNewline := complete[:len(complete)-1]
	lastRecord := bytes.LastIndexByte(withoutFinalNewline, '\n')
	if lastRecord < 0 {
		t.Fatal("projection did not contain two records")
	}
	behind := append([]byte(nil), complete[:lastRecord+1]...)
	if err := os.WriteFile(path, behind, 0o600); err != nil {
		t.Fatal(err)
	}
	projection, err := openExistingWorktreeProjection(graph)
	if err != nil {
		t.Fatal(err)
	}
	defer projection.Close()
	projectionDirectory := filepath.Join(fixture.repository, ".marshal", existingWorktreeRuntimeDirectory, ExistingWorktreeProjectionDirectory)
	lockPath := filepath.Join(projectionDirectory, existingWorktreeProjectionLock)
	projection.beforeCommit = func() {
		moved := lockPath + "-aba"
		if err := os.Rename(lockPath, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(moved, lockPath); err != nil {
			t.Fatal(err)
		}
	}
	if err := projection.Sync(snapshot); err == nil {
		t.Fatal("pre-commit lock ABA was accepted")
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, behind) {
		t.Fatal("pre-commit failure changed live projection bytes")
	}
	stagePath := filepath.Join(fixture.repository, ".marshal", existingWorktreeRuntimeDirectory, existingWorktreeProjectionStage)
	if _, err := os.Lstat(stagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-commit failure left projection stage: %v", err)
	}
}

func TestExistingWorktreeProjectionPostCommitLostStageIsSuccess(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.authority.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	graph, err := fixture.authority.DescriptorGraph()
	if err != nil {
		t.Fatal(err)
	}
	path := fixture.projectionPath(receipt.Observation.TargetIdentityDigest)
	complete := mustReadFile(t, path)
	withoutFinalNewline := complete[:len(complete)-1]
	lastRecord := bytes.LastIndexByte(withoutFinalNewline, '\n')
	if lastRecord < 0 {
		t.Fatal("projection did not contain two records")
	}
	behind := append([]byte(nil), complete[:lastRecord+1]...)
	if err := os.WriteFile(path, behind, 0o600); err != nil {
		t.Fatal(err)
	}
	projection, err := openExistingWorktreeProjection(graph)
	if err != nil {
		t.Fatal(err)
	}
	defer projection.Close()
	runtimePath := filepath.Join(fixture.repository, ".marshal", existingWorktreeRuntimeDirectory)
	stagePath := filepath.Join(runtimePath, existingWorktreeProjectionStage)
	lostPath := filepath.Join(runtimePath, ".projection-stage-lost")
	projection.afterCommit = func() {
		if err := os.Rename(stagePath, lostPath); err != nil {
			t.Fatal(err)
		}
	}
	if err := projection.Sync(snapshot); err != nil {
		t.Fatalf("post-commit stage loss returned ordinary conflict: %v", err)
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, complete) {
		t.Fatal("committed live projection is not the exact RB1 snapshot")
	}
	if _, err := os.Lstat(lostPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renamed committed-old stage was not cleaned: %v", err)
	}
	if _, err := os.Lstat(stagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-commit cleanup left fixed stage: %v", err)
	}
}

func TestExistingWorktreeProjectionCrashStageReconcilesAndDoesNotAccumulate(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	receipt, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.authority.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	graph, err := fixture.authority.DescriptorGraph()
	if err != nil {
		t.Fatal(err)
	}
	path := fixture.projectionPath(receipt.Observation.TargetIdentityDigest)
	expected := mustReadFile(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	projection, err := openExistingWorktreeProjection(graph)
	if err != nil {
		t.Fatal(err)
	}
	records, err := projectionRecords(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := projection.preflight(records)
	if err != nil {
		t.Fatal(err)
	}
	stage, err := projection.stagePlans(plans)
	closeExistingWorktreeProjectionPlans(plans)
	if err != nil {
		projection.Close()
		t.Fatal(err)
	}
	stage.Close() // simulated crash after a complete stage, before the swap
	projection.Close()
	stagePath := filepath.Join(fixture.repository, ".marshal", existingWorktreeRuntimeDirectory, existingWorktreeProjectionStage)
	if _, err := os.Lstat(stagePath); err != nil {
		t.Fatalf("simulated crash stage missing: %v", err)
	}
	if err := SyncExistingWorktreeProjectionFromGraph(graph, snapshot); err != nil {
		t.Fatalf("next Sync did not reconcile crash stage: %v", err)
	}
	if after := mustReadFile(t, path); !bytes.Equal(after, expected) {
		t.Fatal("crash reconcile did not rebuild exact live projection")
	}
	if _, err := os.Lstat(stagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful reconcile left a stale stage: %v", err)
	}
}

func TestExistingWorktreeProjectionInterruptedCleanupLeavesRecoverableStage(t *testing.T) {
	tests := []struct {
		name        string
		phase       string
		wantEntries int
	}{
		{name: "data-subset", phase: existingWorktreeCleanupAfterDataEntry, wantEntries: 2},
		{name: "lock-only", phase: existingWorktreeCleanupAfterDataSync, wantEntries: 1},
		{name: "empty", phase: existingWorktreeCleanupAfterLockSync, wantEntries: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := newExistingWorktreeFixture(t)
			defer first.Close()
			second := newDistinctExistingWorktreeFixture(t)
			defer second.Close()
			if _, err := first.controller.Bind(context.Background(), first.run, first.request); err != nil {
				t.Fatal(err)
			}
			if _, err := second.controller.Bind(context.Background(), second.run, second.request); err != nil {
				t.Fatal(err)
			}
			snapshot := ExistingWorktreeAuthoritySnapshotV1{
				CurrentAttemptRevision:   first.authority.attemptRevision,
				CurrentAttemptHeadDigest: first.authority.attemptHead,
				Facts:                    append(append([]ExistingWorktreeAttemptFactV1(nil), first.authority.facts...), second.authority.facts...),
			}
			if err := snapshot.Validate(); err != nil {
				t.Fatal(err)
			}
			graph, err := first.authority.DescriptorGraph()
			if err != nil {
				t.Fatal(err)
			}
			projection, err := openExistingWorktreeProjection(graph)
			if err != nil {
				t.Fatal(err)
			}
			records, err := projectionRecords(snapshot)
			if err != nil {
				projection.Close()
				t.Fatal(err)
			}
			plans, err := projection.preflight(records)
			if err != nil {
				projection.Close()
				t.Fatal(err)
			}
			stage, err := projection.stagePlans(plans)
			if err != nil {
				closeExistingWorktreeProjectionPlans(plans)
				projection.Close()
				t.Fatal(err)
			}
			if err := stage.Close(); err != nil {
				closeExistingWorktreeProjectionPlans(plans)
				projection.Close()
				t.Fatal(err)
			}
			sentinel := errors.New("simulated cleanup interruption")
			interrupted := false
			projection.cleanupInterrupt = func(phase string) error {
				if !interrupted && phase == test.phase {
					interrupted = true
					return sentinel
				}
				return nil
			}
			cleanupErr := projection.cleanupStage(plans)
			closeExistingWorktreeProjectionPlans(plans)
			projection.Close()
			if !errors.Is(cleanupErr, sentinel) || !interrupted {
				t.Fatalf("cleanup phase %q was not interrupted: %v", test.phase, cleanupErr)
			}
			stagePath := filepath.Join(first.repository, ".marshal", existingWorktreeRuntimeDirectory, existingWorktreeProjectionStage)
			entries, err := os.ReadDir(stagePath)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != test.wantEntries {
				t.Fatalf("interrupted stage entries = %d, want %d", len(entries), test.wantEntries)
			}
			if err := SyncExistingWorktreeProjectionFromGraph(graph, snapshot); err != nil {
				t.Fatalf("next Sync did not reconcile %s cleanup residue: %v", test.name, err)
			}
			if _, err := os.Lstat(stagePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("next Sync left %s cleanup residue: %v", test.name, err)
			}
		})
	}
}

func TestExistingWorktreeProjectionUnknownStageFailsClosedWithoutDeletion(t *testing.T) {
	fixture := newExistingWorktreeFixture(t)
	defer fixture.Close()
	if _, err := fixture.controller.Bind(context.Background(), fixture.run, fixture.request); err != nil {
		t.Fatal(err)
	}
	snapshot, err := fixture.authority.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	graph, err := fixture.authority.DescriptorGraph()
	if err != nil {
		t.Fatal(err)
	}
	stagePath := filepath.Join(fixture.repository, ".marshal", existingWorktreeRuntimeDirectory, existingWorktreeProjectionStage)
	if err := os.Mkdir(stagePath, 0o700); err != nil {
		t.Fatal(err)
	}
	unknownPath := filepath.Join(stagePath, "user-data")
	unknown := []byte("must-not-delete\n")
	if err := os.WriteFile(unknownPath, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SyncExistingWorktreeProjectionFromGraph(graph, snapshot); err == nil {
		t.Fatal("unknown projection stage was accepted")
	}
	if after := mustReadFile(t, unknownPath); !bytes.Equal(after, unknown) {
		t.Fatal("unknown projection stage data was deleted or changed")
	}
}

type filesystemSnapshotEntry struct {
	Mode       fs.FileMode
	Size       int64
	Device     uint64
	Inode      uint64
	LinkCount  uint64
	LinkTarget string
	Digest     string
}

func snapshotFilesystemTree(t *testing.T, root string) map[string]filesystemSnapshotEntry {
	t.Helper()
	result := make(map[string]filesystemSnapshotEntry)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := filesystemSnapshotEntry{Mode: info.Mode(), Size: info.Size()}
		if stat, ok := info.Sys().(*unix.Stat_t); ok {
			value.Device, value.Inode, value.LinkCount = uint64(stat.Dev), stat.Ino, uint64(stat.Nlink)
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			value.LinkTarget, err = os.Readlink(path)
		case info.Mode().IsRegular():
			var raw []byte
			raw, err = os.ReadFile(path)
			value.Digest = digestBytes(raw)
		}
		if err != nil {
			return err
		}
		result[relative] = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertFilesystemTreeEqual(t *testing.T, label string, before, after map[string]filesystemSnapshotEntry) {
	t.Helper()
	if !equalCanonical(before, after) {
		t.Fatalf("%s changed: before=%v after=%v", label, before, after)
	}
}

func mustExistingWorktreeAdminPath(t *testing.T, worktree string) string {
	t.Helper()
	raw := strings.TrimSpace(string(mustReadFile(t, filepath.Join(worktree, ".git"))))
	if !strings.HasPrefix(raw, "gitdir: ") {
		t.Fatal("worktree .git did not contain gitdir")
	}
	path := strings.TrimSpace(strings.TrimPrefix(raw, "gitdir: "))
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatal("worktree gitdir was not canonical absolute")
	}
	return path
}

func makeExistingWorktreePrivate(t *testing.T, worktree string) {
	t.Helper()
	if err := os.Chmod(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(mustExistingWorktreeAdminPath(t, worktree), 0o700); err != nil {
		t.Fatal(err)
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
	makeExistingWorktreePrivate(t, worktree)

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
	repositoryCommon, err := os.Open(filepath.Join(repository, ".git"))
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
	authority := &existingWorktreeTestAuthority{filesystemRoot: filesystemRoot, repositoryParent: repositoryParent, repositoryRoot: repositoryRoot, repositoryCommon: repositoryCommon, repositoryName: filepath.Base(repository), current: currentAuthorityForRequest(request, run), attemptRevision: binding.ExpectedAttemptSequence, attemptHead: binding.AttemptOpenedFactDigest, failAfter: make(map[ExistingWorktreeFactKind]int)}
	controller, err := NewExistingWorktreeController(authority)
	if err != nil {
		t.Fatal(err)
	}
	return &existingWorktreeFixture{t: t, repository: repository, worktree: worktree, baseSHA: baseSHA, authority: authority, controller: controller, run: run, request: request}
}

func newDistinctExistingWorktreeFixture(t *testing.T) *existingWorktreeFixture {
	t.Helper()
	fixture := newExistingWorktreeFixture(t)
	fixture.run.RunID = "run-2"
	fixture.run.AuthorityHeadDigest = testExistingDigest("run-2-ready-head")
	fixture.request.Binding.RunID = fixture.run.RunID
	fixture.request.Binding.AttemptID = "attempt-2"
	fixture.request.Binding.ReservationFactDigest = testExistingDigest("reservation-fact-2")
	fixture.request.Binding.AttemptOpenedFactDigest = testExistingDigest("attempt-opened-2")
	fixture.request.Binding.AllocationID = "allocation-2"
	fixture.request.Binding.LeaseID = "lease-2"
	fixture.request.Binding.Generation = 2
	fixture.request.Binding.FencingTokenDigest = testExistingDigest("fencing-2")
	fixture.request.Binding.FrozenInputsDigest = testExistingDigest("frozen-inputs-2")
	fixture.request.Binding.ExpectedAttemptSequence = 11
	fixture.request.RunAuthorityHeadDigest = fixture.run.AuthorityHeadDigest
	fixture.request.RequestDigest = ""
	if err := fixture.request.Seal(); err != nil {
		t.Fatal(err)
	}
	fixture.authority.current = currentAuthorityForRequest(fixture.request, fixture.run)
	fixture.authority.attemptRevision = fixture.request.Binding.ExpectedAttemptSequence
	fixture.authority.attemptHead = fixture.request.Binding.AttemptOpenedFactDigest
	fixture.authority.facts = nil
	return fixture
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
	current.TerminalAttemptHeadDigest = request.AttemptAuthorityHeadDigest
	current.CleanupBindingDigest = request.CleanupBindingDigest
	current.ProcessTerminalFactDigest = request.ProcessTerminalFactDigest
	current.CleanupDisposition = request.CleanupDisposition
	fixture.authority.current = current
	// Terminalization facts between bind and release advance the same Attempt
	// head before RB1 release begins.
	fixture.authority.attemptRevision++
	fixture.authority.attemptHead = request.AttemptAuthorityHeadDigest
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
