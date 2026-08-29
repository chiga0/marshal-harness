//go:build darwin

package allocationcontrol

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	existingWorktreeGitReadLimit     = 1 << 20
	existingWorktreeProjectionLimit  = 1 << 20
	existingWorktreeRuntimeDirectory = "runtime-v1"
	existingWorktreeProjectionLock   = ".projection.lock"
	existingWorktreeProjectionStage  = ".projection.stage"
	existingWorktreeStageMarker      = ".projection-transaction-"
	// Darwin does not expose directory descriptors as traversable /dev/fd
	// paths. This fixed system launcher performs only fchdir(3) through a Perl
	// filehandle and then execs the fixed Git binary in the same process. The
	// worktree pathname is never reopened or exposed in process arguments.
	existingWorktreeHeldDirectoryLauncher = `open(my $directory, "<&=3") or exit 111; chdir($directory) or exit 112; exec {"/usr/bin/git"} "/usr/bin/git", @ARGV or exit 113;`
)

// NewDescriptorBoundRunV1 captures the exact held directory identity supplied
// by RunStore AcquireExisting. The caller retains ownership of directory and
// must keep its lease borrow alive for the complete controller call.
func NewDescriptorBoundRunV1(runID string, directory *os.File, authorityHeadDigest string) (DescriptorBoundRunV1, error) {
	if directory == nil || !validText(runID) || !validDigest(authorityHeadDigest) {
		return DescriptorBoundRunV1{}, ErrInvalid
	}
	var stat unix.Stat_t
	if unix.Fstat(int(directory.Fd()), &stat) != nil {
		return DescriptorBoundRunV1{}, ErrFilesystemConflict
	}
	run := DescriptorBoundRunV1{RunID: runID, Directory: directory, DirectoryIdentity: objectIdentity(stat), AuthorityHeadDigest: authorityHeadDigest}
	if run.DirectoryIdentity.Validate(ObjectTypeDirectory) != nil {
		return DescriptorBoundRunV1{}, ErrFilesystemConflict
	}
	return run, nil
}

// NewExistingWorktreeDescriptorGraph binds already-held filesystem and
// repository descriptors to the exact repository parent/name edge. It never
// opens a path and does not take ownership of the supplied files.
func NewExistingWorktreeDescriptorGraph(filesystemRoot, repositoryParent, repositoryRoot, repositoryCommonGitDirectory *os.File, repositoryName string) (ExistingWorktreeDescriptorGraphV1, error) {
	if filesystemRoot == nil || repositoryParent == nil || repositoryRoot == nil || repositoryCommonGitDirectory == nil || !validExistingRelativeName(repositoryName) {
		return ExistingWorktreeDescriptorGraphV1{}, ErrInvalid
	}
	var rootStat unix.Stat_t
	if unix.Fstat(int(filesystemRoot.Fd()), &rootStat) != nil {
		return ExistingWorktreeDescriptorGraphV1{}, ErrFilesystemConflict
	}
	current, err := observeDirectoryEdge(int(repositoryParent.Fd()), int(repositoryRoot.Fd()), repositoryName)
	if err != nil {
		return ExistingWorktreeDescriptorGraphV1{}, err
	}
	commonCurrent, err := observeDirectoryEdge(int(repositoryRoot.Fd()), int(repositoryCommonGitDirectory.Fd()), ".git")
	if err != nil {
		return ExistingWorktreeDescriptorGraphV1{}, err
	}
	graph := ExistingWorktreeDescriptorGraphV1{FilesystemRoot: filesystemRoot, FilesystemRootIdentity: objectIdentity(rootStat), RepositoryParent: repositoryParent, RepositoryRoot: repositoryRoot, RepositoryCurrentName: current, RepositoryCommonGitDirectory: repositoryCommonGitDirectory, RepositoryCommonGitCurrentName: commonCurrent}
	if validateExistingWorktreeDescriptorGraph(graph) != nil {
		return ExistingWorktreeDescriptorGraphV1{}, ErrFilesystemConflict
	}
	return graph, nil
}

func validateDescriptorBoundRun(run DescriptorBoundRunV1) error {
	var stat unix.Stat_t
	if run.Directory == nil || unix.Fstat(int(run.Directory.Fd()), &stat) != nil || !sameDirectoryObject(objectIdentity(stat), run.DirectoryIdentity) || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Nlink < 1 {
		return ErrFilesystemConflict
	}
	return nil
}

func validateExistingWorktreeDescriptorGraph(graph ExistingWorktreeDescriptorGraphV1) error {
	if graph.FilesystemRoot == nil || graph.RepositoryParent == nil || graph.RepositoryRoot == nil || graph.RepositoryCommonGitDirectory == nil || graph.FilesystemRootIdentity.Validate(ObjectTypeDirectory) != nil || graph.RepositoryCurrentName.Validate(ObjectTypeDirectory) != nil || graph.RepositoryCommonGitCurrentName.Validate(ObjectTypeDirectory) != nil {
		return ErrInvalid
	}
	var filesystemRoot unix.Stat_t
	if unix.Fstat(int(graph.FilesystemRoot.Fd()), &filesystemRoot) != nil || !sameDirectoryObject(objectIdentity(filesystemRoot), graph.FilesystemRootIdentity) || filesystemRoot.Mode&unix.S_IFMT != unix.S_IFDIR || filesystemRoot.Nlink < 1 {
		return ErrFilesystemConflict
	}
	currentName, err := observeDirectoryEdge(int(graph.RepositoryParent.Fd()), int(graph.RepositoryRoot.Fd()), graph.RepositoryCurrentName.RelativeName)
	if err != nil || !equalCanonical(currentName, graph.RepositoryCurrentName) {
		return ErrFilesystemConflict
	}
	commonCurrent, err := observeDirectoryEdge(int(graph.RepositoryRoot.Fd()), int(graph.RepositoryCommonGitDirectory.Fd()), ".git")
	if err != nil || !equalCanonical(commonCurrent, graph.RepositoryCommonGitCurrentName) {
		return ErrFilesystemConflict
	}
	return nil
}

// existingWorktreeHeldPath keeps every descriptor and current-name edge from
// the already-held filesystem root to the final directory.  No pathname is
// reopened after construction; revalidation is entirely descriptor-relative.
type existingWorktreeHeldPath struct {
	fds         []int
	edges       []CurrentNameIdentityV1
	strictFinal bool
}

func openExistingWorktreeHeldPath(root *os.File, absolutePath string, strictFinal bool) (*existingWorktreeHeldPath, error) {
	if root == nil || !validCanonicalAbsolutePath(absolutePath) {
		return nil, ErrInvalid
	}
	rootFD, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(rootFD)
	components := strings.Split(strings.TrimPrefix(absolutePath, string(filepath.Separator)), string(filepath.Separator))
	held := &existingWorktreeHeldPath{fds: []int{rootFD}, strictFinal: strictFinal}
	for componentIndex, component := range components {
		if component == "" {
			continue
		}
		if !validExistingRelativeName(component) {
			held.Close()
			return nil, ErrInvalid
		}
		parent := held.fds[len(held.fds)-1]
		child, openErr := unix.Openat(parent, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			held.Close()
			return nil, ErrFilesystemConflict
		}
		observe := observeLocatorDirectoryEdge
		if strictFinal && componentIndex == len(components)-1 {
			observe = observePrivateDirectoryEdge
		}
		edge, edgeErr := observe(parent, child, component)
		if edgeErr != nil {
			unix.Close(child)
			held.Close()
			return nil, ErrFilesystemConflict
		}
		held.fds = append(held.fds, child)
		held.edges = append(held.edges, edge)
	}
	if len(held.edges) == 0 {
		held.Close()
		return nil, ErrInvalid
	}
	return held, nil
}

func (held *existingWorktreeHeldPath) FD() int {
	if held == nil || len(held.fds) == 0 {
		return -1
	}
	return held.fds[len(held.fds)-1]
}

func (held *existingWorktreeHeldPath) locatorDigest() (string, error) {
	if held == nil || len(held.edges) == 0 {
		return "", ErrInvalid
	}
	return digestValue(held.edges)
}

func (held *existingWorktreeHeldPath) Revalidate() error {
	if held == nil || len(held.fds) != len(held.edges)+1 {
		return ErrFilesystemConflict
	}
	for index, expected := range held.edges {
		observe := observeLocatorDirectoryEdge
		if held.strictFinal && index == len(held.edges)-1 {
			observe = observePrivateDirectoryEdge
		}
		current, err := observe(held.fds[index], held.fds[index+1], expected.RelativeName)
		if err != nil || !equalCanonical(current, expected) {
			return ErrFilesystemConflict
		}
	}
	return nil
}

func (held *existingWorktreeHeldPath) Close() error {
	if held == nil {
		return nil
	}
	var result error
	for index := len(held.fds) - 1; index >= 0; index-- {
		result = errors.Join(result, unix.Close(held.fds[index]))
	}
	held.fds = nil
	held.edges = nil
	return result
}

type existingWorktreeTargetGuard struct {
	graph       ExistingWorktreeDescriptorGraphV1
	request     ExistingWorktreeBindRequestV1
	target      *existingWorktreeHeldPath
	admin       *existingWorktreeHeldPath
	common      *existingWorktreeHeldPath
	observation ExistingWorktreeObservationV1
	expected    *ExistingWorktreeObservationV1
}

var _ ExistingWorktreeTargetSession = (*existingWorktreeTargetGuard)(nil)

// WithExistingWorktreeTargetFromGraph opens the complete descriptor graph
// once and keeps it live through callback.  expected=nil performs clean bind
// admission; non-nil performs release anchoring while allowing task output,
// HEAD and index to differ from bind time.
func WithExistingWorktreeTargetFromGraph(ctx context.Context, graph ExistingWorktreeDescriptorGraphV1, request ExistingWorktreeBindRequestV1, expected *ExistingWorktreeObservationV1, callback func(ExistingWorktreeTargetSession) error) error {
	if callback == nil || request.Validate() != nil || validateExistingWorktreeDescriptorGraph(graph) != nil {
		return ErrAuthorityConflict
	}
	guard, err := openExistingWorktreeTargetGuard(ctx, graph, request, expected)
	if err != nil {
		return err
	}
	defer guard.Close()
	if err := callback(guard); err != nil {
		return err
	}
	return guard.Revalidate()
}

func openExistingWorktreeTargetGuard(ctx context.Context, graph ExistingWorktreeDescriptorGraphV1, request ExistingWorktreeBindRequestV1, expected *ExistingWorktreeObservationV1) (*existingWorktreeTargetGuard, error) {
	target, err := openExistingWorktreeHeldPath(graph.FilesystemRoot, request.WorktreePath, true)
	if err != nil {
		return nil, err
	}
	closeTarget := true
	defer func() {
		if closeTarget {
			target.Close()
		}
	}()
	if !sameDirectoryObject(target.edges[len(target.edges)-1].ObjectIdentity, request.ExpectedWorktreeIdentity) {
		return nil, ErrFilesystemConflict
	}
	dotGit, _, _, err := readObservedRegularAt(target.FD(), ".git", 16<<10)
	if err != nil {
		return nil, err
	}
	adminPath, err := parseGitdir(dotGit, request.WorktreePath)
	if err != nil {
		return nil, err
	}
	admin, err := openExistingWorktreeHeldPath(graph.FilesystemRoot, adminPath, true)
	if err != nil {
		return nil, err
	}
	closeAdmin := true
	defer func() {
		if closeAdmin {
			admin.Close()
		}
	}()
	commonRaw, _, _, err := readObservedRegularAt(admin.FD(), "commondir", 16<<10)
	if err != nil {
		return nil, err
	}
	commonRelative := strings.TrimSpace(string(commonRaw))
	if commonRelative == "" || filepath.IsAbs(commonRelative) || strings.ContainsRune(commonRelative, 0) {
		return nil, ErrFilesystemConflict
	}
	commonPath := filepath.Clean(filepath.Join(adminPath, commonRelative))
	common, err := openExistingWorktreeHeldPath(graph.FilesystemRoot, commonPath, false)
	if err != nil {
		return nil, err
	}
	closeCommon := true
	defer func() {
		if closeCommon {
			common.Close()
		}
	}()
	if !sameHeldDirectoryFD(common.FD(), int(graph.RepositoryCommonGitDirectory.Fd())) {
		return nil, ErrFilesystemConflict
	}
	guard := &existingWorktreeTargetGuard{graph: graph, request: request, target: target, admin: admin, common: common, expected: expected}
	observation, err := guard.observeMaterial(ctx, expected == nil)
	if err != nil {
		return nil, err
	}
	if expected != nil {
		if expected.Validate() != nil || !sameExistingWorktreeImmutableAnchors(observation, *expected) {
			return nil, ErrFilesystemConflict
		}
	} else {
		guard.observation = observation
	}
	closeTarget, closeAdmin, closeCommon = false, false, false
	return guard, nil
}

func sameHeldDirectoryFD(left, right int) bool {
	var a, b unix.Stat_t
	return unix.Fstat(left, &a) == nil && unix.Fstat(right, &b) == nil && sameDirectoryObject(objectIdentity(a), objectIdentity(b))
}

func (guard *existingWorktreeTargetGuard) Close() error {
	if guard == nil {
		return nil
	}
	return errors.Join(guard.common.Close(), guard.admin.Close(), guard.target.Close())
}

func (guard *existingWorktreeTargetGuard) Observation() (ExistingWorktreeObservationV1, error) {
	if guard == nil || guard.expected != nil || guard.observation.Validate() != nil || guard.Revalidate() != nil {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	return guard.observation, nil
}

func (guard *existingWorktreeTargetGuard) Revalidate() error {
	if guard == nil || validateExistingWorktreeDescriptorGraph(guard.graph) != nil || guard.target.Revalidate() != nil || guard.admin.Revalidate() != nil || guard.common.Revalidate() != nil || !sameHeldDirectoryFD(guard.common.FD(), int(guard.graph.RepositoryCommonGitDirectory.Fd())) {
		return ErrFilesystemConflict
	}
	current, err := guard.observeMaterial(context.Background(), false)
	if err != nil {
		return err
	}
	if guard.expected != nil {
		if !sameExistingWorktreeImmutableAnchors(current, *guard.expected) {
			return ErrFilesystemConflict
		}
		return nil
	}
	// Git query-derived fields are stable because no task is allowed to run
	// during bind. Material identity/digest/mutation equality catches ABA of
	// HEAD/index/admin bytes between admission and durable append.
	current.Git.HeadSHA = guard.observation.Git.HeadSHA
	current.Git.CleanStatusDigest = guard.observation.Git.CleanStatusDigest
	if err := current.Seal(); err != nil || !equalCanonical(current, guard.observation) {
		return ErrFilesystemConflict
	}
	return nil
}

func (guard *existingWorktreeTargetGuard) observeMaterial(ctx context.Context, queryGit bool) (ExistingWorktreeObservationV1, error) {
	if guard == nil || guard.target.Revalidate() != nil || guard.admin.Revalidate() != nil || guard.common.Revalidate() != nil {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	targetEdge := guard.target.edges[len(guard.target.edges)-1]
	adminEdge := guard.admin.edges[len(guard.admin.edges)-1]
	targetLocator, err := guard.target.locatorDigest()
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	adminLocator, err := guard.admin.locatorDigest()
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	commonLocator, err := guard.common.locatorDigest()
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	dotGit, dotGitIdentity, dotGitMutation, err := readObservedRegularAt(guard.target.FD(), ".git", 16<<10)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	adminGitdir, adminGitdirIdentity, adminGitdirMutation, err := readObservedRegularAt(guard.admin.FD(), "gitdir", 16<<10)
	if err != nil || filepath.Clean(strings.TrimSpace(string(adminGitdir))) != filepath.Join(guard.request.WorktreePath, ".git") {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	commonRaw, commonFileIdentity, commonFileMutation, err := readObservedRegularAt(guard.admin.FD(), "commondir", 16<<10)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	commonIdentity, commonMutation, err := observedHeldDirectoryIdentity(guard.common.FD())
	if err != nil || !sameHeldDirectoryFD(guard.common.FD(), int(guard.graph.RepositoryCommonGitDirectory.Fd())) {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	headRaw, headIdentity, headMutation, err := readObservedRegularAt(guard.admin.FD(), "HEAD", 64<<10)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	indexRaw, indexIdentity, indexMutation, err := readObservedRegularAt(guard.admin.FD(), "index", existingWorktreeGitReadLimit)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	observation := ExistingWorktreeObservationV1{
		TargetCurrentName: targetEdge, TargetLocatorDigest: targetLocator,
		Git: ExistingGitWorktreeIdentityV1{
			DotGitIdentity: dotGitIdentity, DotGitDigest: digestBytes(dotGit), DotGitMutationDigest: dotGitMutation,
			AdminCurrentName: adminEdge, AdminLocatorDigest: adminLocator,
			AdminGitdirIdentity: adminGitdirIdentity, AdminGitdirDigest: digestBytes(adminGitdir), AdminGitdirMutationDigest: adminGitdirMutation,
			CommonDirFileIdentity: commonFileIdentity, CommonDirFileDigest: digestBytes(commonRaw), CommonDirFileMutationDigest: commonFileMutation,
			CommonDirectoryIdentity: commonIdentity, CommonDirectoryMutationDigest: commonMutation, CommonDirectoryLocatorDigest: commonLocator,
			HeadIdentity: headIdentity, HeadDigest: digestBytes(headRaw), HeadMutationDigest: headMutation,
			IndexIdentity: indexIdentity, IndexDigest: digestBytes(indexRaw), IndexMutationDigest: indexMutation,
			HeadSHA: guard.request.ExpectedBaseSHA, CleanStatusDigest: digestBytes(nil),
		},
	}
	if queryGit {
		head, status, err := runReadOnlyGitOnHeldTarget(ctx, guard.target.FD())
		if err != nil || head != guard.request.ExpectedBaseSHA || len(status) != 0 {
			return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
		}
		observation.Git.HeadSHA = head
		observation.Git.CleanStatusDigest = digestBytes(status)
	}
	if guard.target.Revalidate() != nil || guard.admin.Revalidate() != nil || guard.common.Revalidate() != nil || observation.Seal() != nil {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	return observation, nil
}

func sameExistingWorktreeImmutableAnchors(current, expected ExistingWorktreeObservationV1) bool {
	return current.TargetCurrentName.ParentIdentity == expected.TargetCurrentName.ParentIdentity && current.TargetCurrentName.RelativeName == expected.TargetCurrentName.RelativeName && sameDirectoryObject(current.TargetCurrentName.ObjectIdentity, expected.TargetCurrentName.ObjectIdentity) &&
		current.Git.DotGitIdentity == expected.Git.DotGitIdentity && current.Git.DotGitDigest == expected.Git.DotGitDigest && current.Git.DotGitMutationDigest == expected.Git.DotGitMutationDigest &&
		current.Git.AdminCurrentName.ParentIdentity == expected.Git.AdminCurrentName.ParentIdentity && current.Git.AdminCurrentName.RelativeName == expected.Git.AdminCurrentName.RelativeName && sameDirectoryObject(current.Git.AdminCurrentName.ObjectIdentity, expected.Git.AdminCurrentName.ObjectIdentity) &&
		current.Git.AdminGitdirIdentity == expected.Git.AdminGitdirIdentity && current.Git.AdminGitdirDigest == expected.Git.AdminGitdirDigest && current.Git.AdminGitdirMutationDigest == expected.Git.AdminGitdirMutationDigest &&
		current.Git.CommonDirFileIdentity == expected.Git.CommonDirFileIdentity && current.Git.CommonDirFileDigest == expected.Git.CommonDirFileDigest && current.Git.CommonDirFileMutationDigest == expected.Git.CommonDirFileMutationDigest &&
		sameDirectoryObject(current.Git.CommonDirectoryIdentity, expected.Git.CommonDirectoryIdentity) && current.Git.CommonDirectoryLocatorDigest == expected.Git.CommonDirectoryLocatorDigest
}

func runReadOnlyGitOnHeldTarget(ctx context.Context, targetFD int) (string, []byte, error) {
	dup, err := unix.Dup(targetFD)
	if err != nil {
		return "", nil, err
	}
	target := os.NewFile(uintptr(dup), "held-worktree")
	defer target.Close()
	run := func(arguments ...string) ([]byte, error) {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		prefix := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.hooksPath=/dev/null"}
		launcherArguments := []string{"-e", existingWorktreeHeldDirectoryLauncher, "--"}
		launcherArguments = append(launcherArguments, prefix...)
		launcherArguments = append(launcherArguments, arguments...)
		command := exec.CommandContext(bounded, "/usr/bin/perl", launcherArguments...)
		command.Env = []string{"HOME=/dev/null", "LC_ALL=C", "PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"}
		command.ExtraFiles = []*os.File{target}
		var stdout cappedCommandOutput
		stdout.limit = existingWorktreeGitReadLimit
		command.Stdout, command.Stderr = &stdout, io.Discard
		if err := command.Run(); err != nil {
			return nil, ErrFilesystemConflict
		}
		return stdout.data, nil
	}
	headRaw, err := run("rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", nil, err
	}
	head := strings.TrimSpace(string(headRaw))
	if !validGitObjectID(head) || string(headRaw) != head+"\n" {
		return "", nil, ErrFilesystemConflict
	}
	status, err := run("status", "--porcelain=v1", "-z", "--untracked-files=all")
	return head, status, err
}

// ObserveExistingWorktreeFromGraph is the reference session mechanic. It may
// only be called while the owner keeps graph held and rechecks every locator
// through that graph before and after the bounded read-only Git query.
func ObserveExistingWorktreeFromGraph(ctx context.Context, graph ExistingWorktreeDescriptorGraphV1, request ExistingWorktreeBindRequestV1) (ExistingWorktreeObservationV1, error) {
	if request.Validate() != nil {
		return ExistingWorktreeObservationV1{}, ErrInvalid
	}
	if validateExistingWorktreeDescriptorGraph(graph) != nil {
		return ExistingWorktreeObservationV1{}, ErrAuthorityConflict
	}
	before, err := observeExistingWorktreeMaterial(graph, request)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	head, status, err := runReadOnlyGit(ctx, graph, request)
	if err != nil || head != request.ExpectedBaseSHA || len(status) != 0 {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	middle, err := observeExistingWorktreeMaterial(graph, request)
	if err != nil || !equalCanonical(before, middle) {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	secondHead, secondStatus, err := runReadOnlyGit(ctx, graph, request)
	if err != nil || secondHead != head || len(secondStatus) != 0 || !bytes.Equal(status, secondStatus) {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	after, err := observeExistingWorktreeMaterial(graph, request)
	if err != nil || !equalCanonical(middle, after) {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	after.Git.HeadSHA = head
	after.Git.CleanStatusDigest = digestBytes(status)
	if err := after.Seal(); err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	return after, nil
}

// VerifyExistingWorktreeTargetFromGraph rechecks the immutable binding
// anchors before release. It intentionally ignores HEAD, index, clean status
// and directory mutation timestamps that legitimate task execution changes.
// Release is logical and never rewrites or deletes those user-owned bytes.
func VerifyExistingWorktreeTargetFromGraph(graph ExistingWorktreeDescriptorGraphV1, request ExistingWorktreeBindRequestV1, expected ExistingWorktreeObservationV1) error {
	if request.Validate() != nil || expected.Validate() != nil || validateExistingWorktreeDescriptorGraph(graph) != nil {
		return ErrAuthorityConflict
	}
	current, err := observeExistingWorktreeBindingAnchors(graph, request)
	if err != nil {
		return err
	}
	if !sameExistingWorktreeCurrentNameAnchor(current.TargetCurrentName, expected.TargetCurrentName) ||
		current.DotGitIdentity != expected.Git.DotGitIdentity || current.DotGitDigest != expected.Git.DotGitDigest || current.DotGitMutationDigest != expected.Git.DotGitMutationDigest ||
		!sameExistingWorktreeCurrentNameAnchor(current.AdminCurrentName, expected.Git.AdminCurrentName) ||
		current.AdminGitdirIdentity != expected.Git.AdminGitdirIdentity || current.AdminGitdirDigest != expected.Git.AdminGitdirDigest || current.AdminGitdirMutationDigest != expected.Git.AdminGitdirMutationDigest ||
		current.CommonDirFileIdentity != expected.Git.CommonDirFileIdentity || current.CommonDirFileDigest != expected.Git.CommonDirFileDigest || current.CommonDirFileMutationDigest != expected.Git.CommonDirFileMutationDigest ||
		!sameDirectoryObject(current.CommonDirectoryIdentity, expected.Git.CommonDirectoryIdentity) || current.CommonDirectoryLocatorDigest != expected.Git.CommonDirectoryLocatorDigest {
		return ErrFilesystemConflict
	}
	return nil
}

func sameExistingWorktreeCurrentNameAnchor(current, expected CurrentNameIdentityV1) bool {
	return current.ParentIdentity == expected.ParentIdentity && current.RelativeName == expected.RelativeName && sameDirectoryObject(current.ObjectIdentity, expected.ObjectIdentity)
}

type existingWorktreeBindingAnchors struct {
	TargetCurrentName            CurrentNameIdentityV1
	DotGitIdentity               ObjectIdentityV1
	DotGitDigest                 string
	DotGitMutationDigest         string
	AdminCurrentName             CurrentNameIdentityV1
	AdminGitdirIdentity          ObjectIdentityV1
	AdminGitdirDigest            string
	AdminGitdirMutationDigest    string
	CommonDirFileIdentity        ObjectIdentityV1
	CommonDirFileDigest          string
	CommonDirFileMutationDigest  string
	CommonDirectoryIdentity      ObjectIdentityV1
	CommonDirectoryLocatorDigest string
}

func observeExistingWorktreeBindingAnchors(graph ExistingWorktreeDescriptorGraphV1, request ExistingWorktreeBindRequestV1) (existingWorktreeBindingAnchors, error) {
	parentPath, leaf := filepath.Split(request.WorktreePath)
	parentFD, _, err := openDirectoryFromHeldFilesystemRootWithLineage(graph.FilesystemRoot, filepath.Clean(parentPath))
	if err != nil || !validExistingRelativeName(leaf) {
		return existingWorktreeBindingAnchors{}, ErrFilesystemConflict
	}
	defer unix.Close(parentFD)
	targetFD, err := unix.Openat(parentFD, leaf, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return existingWorktreeBindingAnchors{}, ErrFilesystemConflict
	}
	defer unix.Close(targetFD)
	targetCurrentName, err := observeDirectoryEdge(parentFD, targetFD, leaf)
	if err != nil || !sameDirectoryObject(targetCurrentName.ObjectIdentity, request.ExpectedWorktreeIdentity) {
		return existingWorktreeBindingAnchors{}, ErrFilesystemConflict
	}
	dotGit, dotGitIdentity, dotGitMutation, err := readObservedRegularAt(targetFD, ".git", 16<<10)
	if err != nil {
		return existingWorktreeBindingAnchors{}, err
	}
	adminPath, err := parseGitdir(dotGit, request.WorktreePath)
	if err != nil {
		return existingWorktreeBindingAnchors{}, err
	}
	adminParentPath, adminLeaf := filepath.Split(adminPath)
	adminParentFD, _, err := openDirectoryFromHeldFilesystemRootWithLineage(graph.FilesystemRoot, filepath.Clean(adminParentPath))
	if err != nil || !validExistingRelativeName(adminLeaf) {
		return existingWorktreeBindingAnchors{}, ErrFilesystemConflict
	}
	defer unix.Close(adminParentFD)
	adminFD, err := unix.Openat(adminParentFD, adminLeaf, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return existingWorktreeBindingAnchors{}, ErrFilesystemConflict
	}
	defer unix.Close(adminFD)
	adminCurrentName, err := observeDirectoryEdge(adminParentFD, adminFD, adminLeaf)
	if err != nil {
		return existingWorktreeBindingAnchors{}, err
	}
	adminGitdir, adminGitdirIdentity, adminGitdirMutation, err := readObservedRegularAt(adminFD, "gitdir", 16<<10)
	if err != nil || filepath.Clean(strings.TrimSpace(string(adminGitdir))) != filepath.Join(request.WorktreePath, ".git") {
		return existingWorktreeBindingAnchors{}, ErrFilesystemConflict
	}
	commonRaw, commonFileIdentity, commonFileMutation, err := readObservedRegularAt(adminFD, "commondir", 16<<10)
	if err != nil {
		return existingWorktreeBindingAnchors{}, err
	}
	commonRelative := strings.TrimSpace(string(commonRaw))
	if commonRelative == "" || filepath.IsAbs(commonRelative) || strings.ContainsRune(commonRelative, 0) {
		return existingWorktreeBindingAnchors{}, ErrFilesystemConflict
	}
	commonPath := filepath.Clean(filepath.Join(adminPath, commonRelative))
	commonFD, commonLocatorDigest, err := openDirectoryFromHeldFilesystemRootWithLineage(graph.FilesystemRoot, commonPath)
	if err != nil || !validCanonicalAbsolutePath(commonPath) {
		return existingWorktreeBindingAnchors{}, ErrFilesystemConflict
	}
	defer unix.Close(commonFD)
	commonIdentity, _, err := observedHeldDirectoryIdentity(commonFD)
	if err != nil || !sameHeldDirectoryFD(commonFD, int(graph.RepositoryCommonGitDirectory.Fd())) {
		return existingWorktreeBindingAnchors{}, ErrFilesystemConflict
	}
	if current, edgeErr := observeDirectoryEdge(parentFD, targetFD, leaf); edgeErr != nil || !equalCanonical(current, targetCurrentName) {
		return existingWorktreeBindingAnchors{}, ErrFilesystemConflict
	}
	if current, edgeErr := observeDirectoryEdge(adminParentFD, adminFD, adminLeaf); edgeErr != nil || !equalCanonical(current, adminCurrentName) {
		return existingWorktreeBindingAnchors{}, ErrFilesystemConflict
	}
	return existingWorktreeBindingAnchors{
		TargetCurrentName: targetCurrentName,
		DotGitIdentity:    dotGitIdentity, DotGitDigest: digestBytes(dotGit), DotGitMutationDigest: dotGitMutation,
		AdminCurrentName:    adminCurrentName,
		AdminGitdirIdentity: adminGitdirIdentity, AdminGitdirDigest: digestBytes(adminGitdir), AdminGitdirMutationDigest: adminGitdirMutation,
		CommonDirFileIdentity: commonFileIdentity, CommonDirFileDigest: digestBytes(commonRaw), CommonDirFileMutationDigest: commonFileMutation,
		CommonDirectoryIdentity: commonIdentity, CommonDirectoryLocatorDigest: commonLocatorDigest,
	}, nil
}

func observeExistingWorktreeMaterial(graph ExistingWorktreeDescriptorGraphV1, request ExistingWorktreeBindRequestV1) (ExistingWorktreeObservationV1, error) {
	parentPath, leaf := filepath.Split(request.WorktreePath)
	parentPath = filepath.Clean(parentPath)
	if !validExistingRelativeName(leaf) {
		return ExistingWorktreeObservationV1{}, ErrInvalid
	}
	parentFD, parentLocatorDigest, err := openDirectoryFromHeldFilesystemRootWithLineage(graph.FilesystemRoot, parentPath)
	if err != nil {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	defer unix.Close(parentFD)
	targetFD, err := unix.Openat(parentFD, leaf, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	defer unix.Close(targetFD)
	targetCurrentName, err := observeDirectoryEdge(parentFD, targetFD, leaf)
	if err != nil || !sameDirectoryObject(targetCurrentName.ObjectIdentity, request.ExpectedWorktreeIdentity) {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	targetLocatorDigest, err := extendDirectoryLocatorDigest(parentLocatorDigest, targetCurrentName)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}

	dotGit, dotGitIdentity, dotGitMutation, err := readObservedRegularAt(targetFD, ".git", 16<<10)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	adminPath, err := parseGitdir(dotGit, request.WorktreePath)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	adminParentPath, adminLeaf := filepath.Split(adminPath)
	adminParentPath = filepath.Clean(adminParentPath)
	if !validExistingRelativeName(adminLeaf) {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	adminParentFD, adminParentLocatorDigest, err := openDirectoryFromHeldFilesystemRootWithLineage(graph.FilesystemRoot, adminParentPath)
	if err != nil {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	defer unix.Close(adminParentFD)
	adminFD, err := unix.Openat(adminParentFD, adminLeaf, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	defer unix.Close(adminFD)
	adminCurrentName, err := observeDirectoryEdge(adminParentFD, adminFD, adminLeaf)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	adminLocatorDigest, err := extendDirectoryLocatorDigest(adminParentLocatorDigest, adminCurrentName)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}

	adminGitdir, adminGitdirIdentity, adminGitdirMutation, err := readObservedRegularAt(adminFD, "gitdir", 16<<10)
	if err != nil || filepath.Clean(strings.TrimSpace(string(adminGitdir))) != filepath.Join(request.WorktreePath, ".git") {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	commonRaw, commonFileIdentity, commonFileMutation, err := readObservedRegularAt(adminFD, "commondir", 16<<10)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	commonRelative := strings.TrimSpace(string(commonRaw))
	if commonRelative == "" || filepath.IsAbs(commonRelative) || strings.ContainsRune(commonRelative, 0) {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	commonPath := filepath.Clean(filepath.Join(adminPath, commonRelative))
	if !validCanonicalAbsolutePath(commonPath) {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	commonFD, commonLocatorDigest, err := openDirectoryFromHeldFilesystemRootWithLineage(graph.FilesystemRoot, commonPath)
	if err != nil {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	defer unix.Close(commonFD)
	commonIdentity, commonMutation, err := observedHeldDirectoryIdentity(commonFD)
	if err != nil || !sameHeldDirectoryFD(commonFD, int(graph.RepositoryCommonGitDirectory.Fd())) {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}

	headRaw, headIdentity, headMutation, err := readObservedRegularAt(adminFD, "HEAD", 64<<10)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	indexRaw, indexIdentity, indexMutation, err := readObservedRegularAt(adminFD, "index", existingWorktreeGitReadLimit)
	if err != nil {
		return ExistingWorktreeObservationV1{}, err
	}
	// Recheck the two current-name edges after every material read.
	if currentTarget, edgeErr := observeDirectoryEdge(parentFD, targetFD, leaf); edgeErr != nil || !equalCanonical(currentTarget, targetCurrentName) {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}
	if currentAdmin, edgeErr := observeDirectoryEdge(adminParentFD, adminFD, adminLeaf); edgeErr != nil || !equalCanonical(currentAdmin, adminCurrentName) {
		return ExistingWorktreeObservationV1{}, ErrFilesystemConflict
	}

	return ExistingWorktreeObservationV1{
		TargetCurrentName:   targetCurrentName,
		TargetLocatorDigest: targetLocatorDigest,
		Git: ExistingGitWorktreeIdentityV1{
			DotGitIdentity:                dotGitIdentity,
			DotGitDigest:                  digestBytes(dotGit),
			DotGitMutationDigest:          dotGitMutation,
			AdminCurrentName:              adminCurrentName,
			AdminLocatorDigest:            adminLocatorDigest,
			AdminGitdirIdentity:           adminGitdirIdentity,
			AdminGitdirDigest:             digestBytes(adminGitdir),
			AdminGitdirMutationDigest:     adminGitdirMutation,
			CommonDirFileIdentity:         commonFileIdentity,
			CommonDirFileDigest:           digestBytes(commonRaw),
			CommonDirFileMutationDigest:   commonFileMutation,
			CommonDirectoryIdentity:       commonIdentity,
			CommonDirectoryMutationDigest: commonMutation,
			CommonDirectoryLocatorDigest:  commonLocatorDigest,
			HeadIdentity:                  headIdentity,
			HeadDigest:                    digestBytes(headRaw),
			HeadMutationDigest:            headMutation,
			IndexIdentity:                 indexIdentity,
			IndexDigest:                   digestBytes(indexRaw),
			IndexMutationDigest:           indexMutation,
			// HeadSHA and CleanStatusDigest are populated only after the bounded
			// read-only git query and a second identical material observation.
			HeadSHA: request.ExpectedBaseSHA, CleanStatusDigest: digestBytes(nil),
		},
	}, nil
}

func openDirectoryFromHeldFilesystemRoot(root *os.File, absolutePath string) (int, error) {
	fd, _, err := openDirectoryFromHeldFilesystemRootWithLineage(root, absolutePath)
	return fd, err
}

func openDirectoryFromHeldFilesystemRootWithLineage(root *os.File, absolutePath string) (int, string, error) {
	if root == nil || !validCanonicalAbsolutePath(absolutePath) {
		return -1, "", ErrInvalid
	}
	current, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return -1, "", err
	}
	unix.CloseOnExec(current)
	lineage := make([]CurrentNameIdentityV1, 0, strings.Count(absolutePath, string(filepath.Separator)))
	clean := strings.TrimPrefix(absolutePath, string(filepath.Separator))
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		if component == "" {
			continue
		}
		if !validExistingRelativeName(component) {
			unix.Close(current)
			return -1, "", ErrInvalid
		}
		next, openErr := unix.Openat(current, component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			unix.Close(current)
			return -1, "", ErrFilesystemConflict
		}
		edge, edgeErr := observeLocatorDirectoryEdge(current, next, component)
		unix.Close(current)
		if edgeErr != nil {
			unix.Close(next)
			return -1, "", ErrFilesystemConflict
		}
		lineage = append(lineage, edge)
		current = next
	}
	digest, err := digestValue(lineage)
	if err != nil {
		unix.Close(current)
		return -1, "", err
	}
	return current, digest, nil
}

func observeLocatorDirectoryEdge(parentFD, childFD int, name string) (CurrentNameIdentityV1, error) {
	var parent, held, named unix.Stat_t
	if unix.Fstat(parentFD, &parent) != nil || unix.Fstat(childFD, &held) != nil || unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameNamedDirectoryStat(held, named) || parent.Mode&unix.S_IFMT != unix.S_IFDIR || parent.Nlink < 1 || held.Nlink < 1 {
		return CurrentNameIdentityV1{}, ErrFilesystemConflict
	}
	// Ancestor directories such as /private/tmp may receive unrelated sibling
	// churn. Bind their current-name chain to stable object identity plus inode
	// generation, while the final target/admin edges below additionally bind
	// mutation time to catch rename-away/back ABA of the actual authority object.
	return CurrentNameIdentityV1{ParentIdentity: stableDirectoryIdentity(parent), ParentMutationDigest: statGenerationDigest(parent), RelativeName: name, ObjectIdentity: stableDirectoryIdentity(held), ObjectMutationDigest: statMutationDigest(held)}, nil
}

func extendDirectoryLocatorDigest(parentDigest string, edge CurrentNameIdentityV1) (string, error) {
	if !validDigest(parentDigest) || edge.Validate(ObjectTypeDirectory) != nil {
		return "", ErrInvalid
	}
	return digestValue(struct {
		ParentDigest string                `json:"parentDigest"`
		CurrentName  CurrentNameIdentityV1 `json:"currentName"`
	}{parentDigest, edge})
}

func observeDirectoryEdge(parentFD, childFD int, name string) (CurrentNameIdentityV1, error) {
	var parent, held, named unix.Stat_t
	if unix.Fstat(parentFD, &parent) != nil || unix.Fstat(childFD, &held) != nil || unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameNamedDirectoryStat(held, named) || held.Dev != parent.Dev || held.Uid != uint32(unix.Geteuid()) || held.Mode&0o022 != 0 || parent.Mode&unix.S_IFMT != unix.S_IFDIR || parent.Nlink < 1 {
		return CurrentNameIdentityV1{}, ErrFilesystemConflict
	}
	return CurrentNameIdentityV1{ParentIdentity: stableDirectoryIdentity(parent), ParentMutationDigest: statGenerationDigest(parent), RelativeName: name, ObjectIdentity: objectIdentity(held), ObjectMutationDigest: statMutationDigest(held)}, nil
}

func observePrivateDirectoryEdge(parentFD, childFD int, name string) (CurrentNameIdentityV1, error) {
	edge, err := observeDirectoryEdge(parentFD, childFD, name)
	if err != nil || verifyPrivateDirectory(childFD, uint32(unix.Geteuid())) != nil {
		return CurrentNameIdentityV1{}, ErrFilesystemConflict
	}
	return edge, nil
}

func stableDirectoryIdentity(stat unix.Stat_t) ObjectIdentityV1 {
	identity := objectIdentity(stat)
	// APFS directory size/link count are not stable authority attributes and
	// may change because of an unrelated sibling. Current-name validation only
	// needs the exact directory object, type/mode and owner.
	identity.Size = 0
	identity.Nlink = 1
	return identity
}

func statGenerationDigest(stat unix.Stat_t) string {
	digest, _ := digestValue(struct {
		Generation uint32 `json:"generation"`
	}{stat.Gen})
	return digest
}

func statMutationDigest(stat unix.Stat_t) string {
	value := struct {
		Generation              uint32 `json:"generation"`
		ChangeSeconds           int64  `json:"changeSeconds"`
		ChangeNanoseconds       int64  `json:"changeNanoseconds"`
		ModificationSeconds     int64  `json:"modificationSeconds"`
		ModificationNanoseconds int64  `json:"modificationNanoseconds"`
	}{stat.Gen, stat.Ctim.Sec, stat.Ctim.Nsec, stat.Mtim.Sec, stat.Mtim.Nsec}
	digest, _ := digestValue(value)
	return digest
}

func observedHeldDirectoryIdentity(fd int) (ObjectIdentityV1, string, error) {
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(unix.Geteuid()) || stat.Mode&0o022 != 0 || stat.Nlink < 1 {
		return ObjectIdentityV1{}, "", ErrFilesystemConflict
	}
	return objectIdentity(stat), statMutationDigest(stat), nil
}

func readObservedRegularAt(parentFD int, name string, limit int64) ([]byte, ObjectIdentityV1, string, error) {
	if !validExistingRelativeName(name) || limit < 1 {
		return nil, ObjectIdentityV1{}, "", ErrInvalid
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ObjectIdentityV1{}, "", ErrFilesystemConflict
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before, after, named unix.Stat_t
	if unix.Fstat(fd, &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Uid != uint32(unix.Geteuid()) || before.Mode&0o022 != 0 || before.Nlink != 1 || before.Size < 0 || before.Size > limit {
		return nil, ObjectIdentityV1{}, "", ErrFilesystemConflict
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ObjectIdentityV1{}, "", ErrFilesystemConflict
	}
	if unix.Fstat(fd, &after) != nil || unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameStat(before, after) || !sameStat(after, named) {
		return nil, ObjectIdentityV1{}, "", ErrFilesystemConflict
	}
	return raw, objectIdentity(after), statMutationDigest(after), nil
}

func parseGitdir(raw []byte, worktree string) (string, error) {
	value := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(value, "gitdir: ") {
		return "", ErrFilesystemConflict
	}
	path := strings.TrimSpace(strings.TrimPrefix(value, "gitdir: "))
	if !filepath.IsAbs(path) {
		path = filepath.Join(worktree, path)
	}
	path = filepath.Clean(path)
	if !validCanonicalAbsolutePath(path) {
		return "", ErrFilesystemConflict
	}
	return path, nil
}

type cappedCommandOutput struct {
	limit int
	data  []byte
}

func (output *cappedCommandOutput) Write(p []byte) (int, error) {
	if len(output.data)+len(p) > output.limit {
		return 0, ErrFilesystemConflict
	}
	output.data = append(output.data, p...)
	return len(p), nil
}

func runReadOnlyGit(ctx context.Context, graph ExistingWorktreeDescriptorGraphV1, request ExistingWorktreeBindRequestV1) (string, []byte, error) {
	parentPath, leaf := filepath.Split(request.WorktreePath)
	parentPath = filepath.Clean(parentPath)
	parentFD, err := openDirectoryFromHeldFilesystemRoot(graph.FilesystemRoot, parentPath)
	if err != nil {
		return "", nil, err
	}
	defer unix.Close(parentFD)
	targetFD, err := unix.Openat(parentFD, leaf, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", nil, ErrFilesystemConflict
	}
	target := os.NewFile(uintptr(targetFD), "held-worktree")
	defer target.Close()
	initialCurrentName, edgeErr := observeDirectoryEdge(parentFD, targetFD, leaf)
	if edgeErr != nil || !sameDirectoryObject(initialCurrentName.ObjectIdentity, request.ExpectedWorktreeIdentity) {
		return "", nil, ErrFilesystemConflict
	}
	run := func(arguments ...string) ([]byte, error) {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		prefix := []string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.hooksPath=/dev/null"}
		launcherArguments := []string{"-e", existingWorktreeHeldDirectoryLauncher, "--"}
		launcherArguments = append(launcherArguments, prefix...)
		launcherArguments = append(launcherArguments, arguments...)
		command := exec.CommandContext(bounded, "/usr/bin/perl", launcherArguments...)
		command.Env = []string{"HOME=/dev/null", "LC_ALL=C", "PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"}
		command.ExtraFiles = []*os.File{target}
		var stdout cappedCommandOutput
		stdout.limit = existingWorktreeGitReadLimit
		command.Stdout = &stdout
		command.Stderr = io.Discard
		command.Stdin = nil
		if err := command.Run(); err != nil {
			return nil, ErrFilesystemConflict
		}
		return stdout.data, nil
	}
	headRaw, err := run("rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", nil, err
	}
	head := strings.TrimSpace(string(headRaw))
	if !validGitObjectID(head) || string(headRaw) != head+"\n" {
		return "", nil, ErrFilesystemConflict
	}
	status, err := run("status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", nil, err
	}
	if currentName, edgeErr := observeDirectoryEdge(parentFD, targetFD, leaf); edgeErr != nil || !equalCanonical(currentName, initialCurrentName) {
		return "", nil, ErrFilesystemConflict
	}
	return head, status, nil
}

type existingWorktreeProjection struct {
	parent               *os.File
	directory            *os.File
	directoryCurrentName CurrentNameIdentityV1
	lock                 *os.File
	lockIdentity         existingWorktreeProjectionFileIdentity
	afterPreflight       func()
	beforeCommit         func()
	afterCommit          func()
	cleanupInterrupt     func(string) error
}

type existingWorktreeProjectionFileIdentity struct {
	Object         ObjectIdentityV1
	MutationDigest string
}

type existingWorktreeProjectionPlan struct {
	name      string
	expected  []byte
	observed  []byte
	identity  existingWorktreeProjectionFileIdentity
	preflight *os.File
	missing   bool
}

type existingWorktreeProjectionTransaction struct {
	name                 string
	directory            *os.File
	directoryCurrentName CurrentNameIdentityV1
	lock                 *os.File
	lockIdentity         existingWorktreeProjectionFileIdentity
}

type existingWorktreeProjectionCleanupEntry struct {
	name     string
	file     *os.File
	identity existingWorktreeProjectionFileIdentity
}

const (
	existingWorktreeCleanupAfterDataEntry = "after-data-entry"
	existingWorktreeCleanupAfterDataSync  = "after-data-sync"
	existingWorktreeCleanupAfterLockSync  = "after-lock-sync"
)

func (projection *existingWorktreeProjection) interruptCleanup(phase string) error {
	if projection != nil && projection.cleanupInterrupt != nil {
		return projection.cleanupInterrupt(phase)
	}
	return nil
}

// SyncExistingWorktreeProjectionFromGraph is the reference session projection
// mechanic. It is valid only after the corresponding RB1 receipt fsync.
func SyncExistingWorktreeProjectionFromGraph(graph ExistingWorktreeDescriptorGraphV1, snapshot ExistingWorktreeAuthoritySnapshotV1) error {
	store, err := openExistingWorktreeProjection(graph)
	if err != nil {
		return err
	}
	defer store.Close()
	return store.Sync(snapshot)
}

func openExistingWorktreeProjection(graph ExistingWorktreeDescriptorGraphV1) (*existingWorktreeProjection, error) {
	if validateExistingWorktreeDescriptorGraph(graph) != nil {
		return nil, ErrAuthorityConflict
	}
	marshalDir, err := openOrCreateProjectionDirectory(graph.RepositoryRoot, ".marshal", false)
	if err != nil {
		return nil, err
	}
	defer marshalDir.Close()
	runtimeDir, err := openOrCreateProjectionDirectory(marshalDir, existingWorktreeRuntimeDirectory, true)
	if err != nil {
		return nil, err
	}
	defer runtimeDir.Close()
	bindings, err := openOrCreateProjectionDirectory(runtimeDir, ExistingWorktreeProjectionDirectory, true)
	if err != nil {
		return nil, err
	}
	var lockStat unix.Stat_t
	if statErr := unix.Fstatat(int(bindings.Fd()), existingWorktreeProjectionLock, &lockStat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(statErr, unix.ENOENT) {
		entries, readErr := bindings.ReadDir(-1)
		if readErr != nil || len(entries) != 0 {
			bindings.Close()
			return nil, ErrAuthorityConflict
		}
		if _, seekErr := bindings.Seek(0, 0); seekErr != nil {
			bindings.Close()
			return nil, ErrFilesystemConflict
		}
	} else if statErr != nil {
		bindings.Close()
		return nil, ErrFilesystemConflict
	}
	lock, err := openOrCreateProjectionFile(bindings, existingWorktreeProjectionLock)
	if err != nil {
		bindings.Close()
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		bindings.Close()
		return nil, ErrFilesystemConflict
	}
	parentFD, err := unix.Dup(int(runtimeDir.Fd()))
	if err != nil {
		lock.Close()
		bindings.Close()
		return nil, err
	}
	parent := os.NewFile(uintptr(parentFD), existingWorktreeRuntimeDirectory)
	current, err := observeDirectoryEdge(int(parent.Fd()), int(bindings.Fd()), ExistingWorktreeProjectionDirectory)
	if err != nil {
		parent.Close()
		lock.Close()
		bindings.Close()
		return nil, err
	}
	lockIdentity, err := observeExistingWorktreeProjectionFile(bindings, existingWorktreeProjectionLock, lock)
	if err != nil || lockIdentity.Object.Size != 0 {
		parent.Close()
		lock.Close()
		bindings.Close()
		return nil, ErrFilesystemConflict
	}
	return &existingWorktreeProjection{parent: parent, directory: bindings, directoryCurrentName: current, lock: lock, lockIdentity: lockIdentity}, nil
}

func (projection *existingWorktreeProjection) Close() error {
	if projection == nil {
		return nil
	}
	var result error
	if projection.lock != nil {
		result = errors.Join(result, projection.lock.Close())
		projection.lock = nil
	}
	if projection.directory != nil {
		result = errors.Join(result, projection.directory.Close())
		projection.directory = nil
	}
	if projection.parent != nil {
		result = errors.Join(result, projection.parent.Close())
		projection.parent = nil
	}
	return result
}

func openOrCreateProjectionDirectory(parent *os.File, name string, private bool) (*os.File, error) {
	if parent == nil || !validExistingRelativeName(name) {
		return nil, ErrInvalid
	}
	created := false
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrFilesystemConflict
	}
	var held, named unix.Stat_t
	if unix.Fstat(fd, &held) != nil || unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameNamedDirectoryStat(held, named) || held.Uid != uint32(unix.Geteuid()) || held.Mode&0o022 != 0 || private && held.Mode&0o077 != 0 {
		unix.Close(fd)
		return nil, ErrFilesystemConflict
	}
	file := os.NewFile(uintptr(fd), name)
	if created {
		if err := file.Sync(); err != nil {
			file.Close()
			return nil, err
		}
		if err := parent.Sync(); err != nil {
			file.Close()
			return nil, err
		}
	}
	return file, nil
}

func openOrCreateProjectionFile(parent *os.File, name string) (*os.File, error) {
	created := false
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err == nil {
		created = true
	} else if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, ErrFilesystemConflict
	}
	var held, named unix.Stat_t
	if unix.Fstat(fd, &held) != nil || unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameStat(held, named) || verifyPrivateRegular(held, uint32(unix.Geteuid())) != nil {
		unix.Close(fd)
		return nil, ErrFilesystemConflict
	}
	file := os.NewFile(uintptr(fd), name)
	if created {
		if err := file.Sync(); err != nil {
			file.Close()
			return nil, err
		}
		if err := parent.Sync(); err != nil {
			file.Close()
			return nil, err
		}
	}
	return file, nil
}

func (projection *existingWorktreeProjection) Sync(snapshot ExistingWorktreeAuthoritySnapshotV1) error {
	if projection == nil || projection.directory == nil || projection.lock == nil || snapshot.Validate() != nil {
		return ErrAuthorityConflict
	}
	expected, err := projectionRecords(snapshot)
	if err != nil {
		return err
	}
	plans, err := projection.preflight(expected)
	if err != nil {
		return err
	}
	defer closeExistingWorktreeProjectionPlans(plans)
	if projection.afterPreflight != nil {
		hook := projection.afterPreflight
		projection.afterPreflight = nil
		hook()
	}
	if err := projection.revalidatePlans(plans); err != nil {
		return err
	}
	if err := projection.cleanupStage(plans); err != nil {
		return err
	}
	for _, plan := range plans {
		if plan.missing || !bytes.Equal(plan.observed, plan.expected) {
			return projection.commitPlans(plans)
		}
	}
	return nil
}

// preflight is deliberately all-read-before-any-write. It holds every
// existing entry descriptor until apply completes, so a later corrupt/ahead
// entry cannot be discovered after an earlier behind entry was extended.
func (projection *existingWorktreeProjection) preflight(expected map[string][]ExistingWorktreeProjectionRecordV1) ([]*existingWorktreeProjectionPlan, error) {
	if err := projection.revalidateAuthority(); err != nil {
		return nil, err
	}
	if _, err := projection.directory.Seek(0, 0); err != nil {
		return nil, ErrFilesystemConflict
	}
	entries, err := projection.directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.Name() == existingWorktreeProjectionLock {
			continue
		}
		if entry.IsDir() || !validExistingRelativeName(entry.Name()) {
			return nil, ErrFilesystemConflict
		}
		if _, ok := expected[entry.Name()]; !ok {
			return nil, ErrAuthorityConflict
		}
		seen[entry.Name()] = true
	}
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Strings(names)
	plans := make([]*existingWorktreeProjectionPlan, 0, len(names))
	closeOnError := true
	defer func() {
		if closeOnError {
			closeExistingWorktreeProjectionPlans(plans)
		}
	}()
	for _, name := range names {
		expectedBytes, encodeErr := existingWorktreeProjectionBytes(expected[name])
		if encodeErr != nil {
			return nil, encodeErr
		}
		plan := &existingWorktreeProjectionPlan{name: name, expected: expectedBytes, missing: !seen[name]}
		plans = append(plans, plan)
		if !plan.missing {
			file, openErr := openExistingWorktreeProjectionFile(projection.directory, name, unix.O_RDONLY)
			if openErr != nil {
				return nil, openErr
			}
			plan.preflight = file
			plan.identity, openErr = observeExistingWorktreeProjectionFile(projection.directory, name, file)
			if openErr != nil {
				return nil, openErr
			}
			plan.observed, openErr = readExistingWorktreeProjectionFile(file)
			if openErr != nil || !validExistingWorktreeProjectionPrefix(plan.observed, plan.expected) {
				return nil, ErrAuthorityConflict
			}
		}
	}
	if err := projection.revalidateAuthority(); err != nil {
		return nil, err
	}
	closeOnError = false
	return plans, nil
}

// revalidatePlans is the last read-only gate before a projection transaction
// is staged. Every held entry, every expected absence and both the directory
// and lock current-name identities are checked as one set. A conflict here
// therefore cannot follow an earlier live-entry write.
func (projection *existingWorktreeProjection) revalidatePlans(plans []*existingWorktreeProjectionPlan) error {
	if projection.revalidateAuthority() != nil {
		return ErrFilesystemConflict
	}
	for _, plan := range plans {
		if plan == nil || !validExistingRelativeName(plan.name) || !validExistingWorktreeProjectionPrefix(plan.observed, plan.expected) {
			return ErrFilesystemConflict
		}
		if plan.missing {
			var stat unix.Stat_t
			if err := unix.Fstatat(int(projection.directory.Fd()), plan.name, &stat, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
				return ErrFilesystemConflict
			}
			continue
		}
		identity, err := observeExistingWorktreeProjectionFile(projection.directory, plan.name, plan.preflight)
		if err != nil || !sameExistingWorktreeProjectionFile(identity, plan.identity) {
			return ErrFilesystemConflict
		}
		raw, err := readExistingWorktreeProjectionFile(plan.preflight)
		if err != nil || !bytes.Equal(raw, plan.observed) {
			return ErrAuthorityConflict
		}
	}
	return projection.revalidateAuthority()
}

// commitPlans materializes a complete derived projection in the one bounded
// private stage and exposes every entry with one Darwin RENAME_SWAP. The swap
// is the commit point: every fallible authority/content check is before it.
// After it succeeds the projection is reconstructible from RB1, so cleanup or
// parent-fsync uncertainty is reconciled by the next Sync and cannot turn a
// committed projection into an ordinary conflict response.
func (projection *existingWorktreeProjection) commitPlans(plans []*existingWorktreeProjectionPlan) error {
	stage, err := projection.stagePlans(plans)
	if err != nil {
		return err
	}
	defer stage.Close()
	discard := func(cause error) error {
		_ = stage.Close()
		if cleanupErr := projection.cleanupStage(plans); cleanupErr != nil {
			return cleanupErr
		}
		return cause
	}
	if projection.beforeCommit != nil {
		hook := projection.beforeCommit
		projection.beforeCommit = nil
		hook()
	}
	// Staging may take time. Recheck the complete live set immediately before
	// the single commit operation, still without changing a live entry.
	if err := projection.revalidatePlans(plans); err != nil {
		return discard(err)
	}
	stageCurrent, stageCurrentErr := observeDirectoryEdge(int(projection.parent.Fd()), int(stage.directory.Fd()), stage.name)
	stageLockIdentity, stageLockErr := observeExistingWorktreeProjectionFile(stage.directory, existingWorktreeProjectionLock, stage.lock)
	if stageCurrentErr != nil || !equalCanonical(stageCurrent, stage.directoryCurrentName) || stageLockErr != nil || !sameExistingWorktreeProjectionFile(stageLockIdentity, stage.lockIdentity) || verifyExistingWorktreeProjectionDirectory(stage.directory, plans) != nil {
		return discard(ErrFilesystemConflict)
	}
	parentFD := int(projection.parent.Fd())
	if err := unix.RenameatxNp(parentFD, stage.name, parentFD, ExistingWorktreeProjectionDirectory, unix.RENAME_SWAP); err != nil {
		return discard(ErrFilesystemConflict)
	}
	// COMMIT POINT. No error may escape below this line. The old projection is
	// now at the fixed stage name and is best-effort removed only after exact,
	// descriptor-relative validation. A crash leaves at most that one stage;
	// the next Sync deterministically validates and removes or reuses it.
	if projection.afterCommit != nil {
		hook := projection.afterCommit
		projection.afterCommit = nil
		hook()
	}
	_ = projection.parent.Sync()
	_ = projection.cleanupCommittedOldProjection(plans)
	_ = projection.lock.Close()
	projection.lock = nil
	_ = projection.directory.Close()
	projection.directory = nil
	_ = projection.cleanupStage(plans)
	return nil
}

func (projection *existingWorktreeProjection) cleanupCommittedOldProjection(plans []*existingWorktreeProjectionPlan) error {
	if projection == nil || projection.parent == nil || projection.directory == nil || projection.lock == nil {
		return ErrFilesystemConflict
	}
	name, err := findExistingWorktreeHeldDirectoryName(projection.parent, projection.directory)
	if err != nil {
		return err
	}
	if _, err := observePrivateDirectoryEdge(int(projection.parent.Fd()), int(projection.directory.Fd()), name); err != nil {
		return ErrFilesystemConflict
	}
	expectedNames := map[string]bool{existingWorktreeProjectionLock: true}
	for _, plan := range plans {
		if plan == nil {
			return ErrFilesystemConflict
		}
		if !plan.missing {
			expectedNames[plan.name] = true
		}
	}
	if err := verifyExistingWorktreeProjectionEntryNames(projection.directory, expectedNames); err != nil {
		return err
	}
	lockIdentity, err := observeExistingWorktreeProjectionFile(projection.directory, existingWorktreeProjectionLock, projection.lock)
	if err != nil || !sameExistingWorktreeProjectionFile(lockIdentity, projection.lockIdentity) || lockIdentity.Object.Size != 0 {
		return ErrFilesystemConflict
	}
	for _, plan := range plans {
		if plan.missing {
			continue
		}
		identity, err := observeExistingWorktreeProjectionFile(projection.directory, plan.name, plan.preflight)
		if err != nil || !sameExistingWorktreeProjectionFile(identity, plan.identity) {
			return ErrFilesystemConflict
		}
		raw, err := readExistingWorktreeProjectionFile(plan.preflight)
		if err != nil || !bytes.Equal(raw, plan.observed) {
			return ErrAuthorityConflict
		}
	}
	for _, plan := range plans {
		if plan.missing {
			continue
		}
		identity, err := observeExistingWorktreeProjectionFile(projection.directory, plan.name, plan.preflight)
		if err != nil || !sameExistingWorktreeProjectionFile(identity, plan.identity) || unix.Unlinkat(int(projection.directory.Fd()), plan.name, 0) != nil {
			return ErrFilesystemConflict
		}
		if err := projection.interruptCleanup(existingWorktreeCleanupAfterDataEntry); err != nil {
			return err
		}
	}
	if err := projection.directory.Sync(); err != nil {
		return ErrFilesystemConflict
	}
	if err := projection.interruptCleanup(existingWorktreeCleanupAfterDataSync); err != nil {
		return err
	}
	lockIdentity, err = observeExistingWorktreeProjectionFile(projection.directory, existingWorktreeProjectionLock, projection.lock)
	if err != nil || !sameExistingWorktreeProjectionFile(lockIdentity, projection.lockIdentity) || unix.Unlinkat(int(projection.directory.Fd()), existingWorktreeProjectionLock, 0) != nil {
		return ErrFilesystemConflict
	}
	if err := projection.directory.Sync(); err != nil {
		return ErrFilesystemConflict
	}
	if err := projection.interruptCleanup(existingWorktreeCleanupAfterLockSync); err != nil {
		return err
	}
	if _, err := observePrivateDirectoryEdge(int(projection.parent.Fd()), int(projection.directory.Fd()), name); err != nil {
		return ErrFilesystemConflict
	}
	if err := unix.Unlinkat(int(projection.parent.Fd()), name, unix.AT_REMOVEDIR); err != nil {
		return ErrFilesystemConflict
	}
	return projection.parent.Sync()
}

func findExistingWorktreeHeldDirectoryName(parent, held *os.File) (string, error) {
	if parent == nil || held == nil {
		return "", ErrFilesystemConflict
	}
	var heldStat unix.Stat_t
	if unix.Fstat(int(held.Fd()), &heldStat) != nil || heldStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return "", ErrFilesystemConflict
	}
	duplicateFD, err := unix.Openat(int(parent.Fd()), ".", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", ErrFilesystemConflict
	}
	directory := os.NewFile(uintptr(duplicateFD), "projection-parent-scan")
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return "", ErrFilesystemConflict
	}
	name := ""
	for _, entry := range entries {
		if !validExistingRelativeName(entry.Name()) {
			continue
		}
		var named unix.Stat_t
		if unix.Fstatat(int(parent.Fd()), entry.Name(), &named, unix.AT_SYMLINK_NOFOLLOW) != nil || named.Mode&unix.S_IFMT != unix.S_IFDIR || named.Dev != heldStat.Dev || named.Ino != heldStat.Ino {
			continue
		}
		if name != "" {
			return "", ErrFilesystemConflict
		}
		name = entry.Name()
	}
	if name == "" {
		return "", ErrFilesystemConflict
	}
	return name, nil
}

func (projection *existingWorktreeProjection) stagePlans(plans []*existingWorktreeProjectionPlan) (*existingWorktreeProjectionTransaction, error) {
	if err := projection.cleanupStage(plans); err != nil {
		return nil, err
	}
	directory, err := createExistingWorktreeProjectionStageDirectory(projection.parent)
	if err != nil {
		return nil, err
	}
	stage := &existingWorktreeProjectionTransaction{name: existingWorktreeProjectionStage, directory: directory}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = stage.Close()
			_ = projection.cleanupStage(plans)
		}
	}()
	stage.lock, err = createExistingWorktreeProjectionFile(directory, existingWorktreeProjectionLock)
	if err != nil || unix.Flock(int(stage.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil, ErrFilesystemConflict
	}
	stage.lockIdentity, err = observeExistingWorktreeProjectionFile(directory, existingWorktreeProjectionLock, stage.lock)
	if err != nil || stage.lockIdentity.Object.Size != 0 {
		return nil, ErrFilesystemConflict
	}
	markerName, err := existingWorktreeProjectionStageMarkerName(plans)
	if err != nil {
		return nil, err
	}
	marker, err := createExistingWorktreeProjectionFile(directory, markerName)
	if err != nil {
		return nil, err
	}
	markerOpen := true
	defer func() {
		if markerOpen {
			_ = marker.Close()
		}
	}()
	for _, plan := range plans {
		file, createErr := createExistingWorktreeProjectionFile(directory, plan.name)
		if createErr != nil {
			return nil, createErr
		}
		if writeErr := writeAll(file, plan.expected); writeErr != nil {
			file.Close()
			return nil, writeErr
		}
		if syncErr := file.Sync(); syncErr != nil {
			file.Close()
			return nil, syncErr
		}
		raw, readErr := readExistingWorktreeProjectionFile(file)
		identity, identityErr := observeExistingWorktreeProjectionFile(directory, plan.name, file)
		file.Close()
		if readErr != nil || identityErr != nil || !bytes.Equal(raw, plan.expected) || identity.Object.Size != int64(len(plan.expected)) {
			return nil, ErrFilesystemConflict
		}
	}
	markerIdentity, markerErr := observeExistingWorktreeProjectionFile(directory, markerName, marker)
	if markerErr != nil || markerIdentity.Object.Size != 0 {
		return nil, ErrFilesystemConflict
	}
	if err := unix.Unlinkat(int(directory.Fd()), markerName, 0); err != nil {
		return nil, ErrFilesystemConflict
	}
	if err := marker.Close(); err != nil {
		return nil, err
	}
	markerOpen = false
	if err := directory.Sync(); err != nil {
		return nil, err
	}
	if err := verifyExistingWorktreeProjectionDirectory(directory, plans); err != nil {
		return nil, err
	}
	stage.directoryCurrentName, err = observeDirectoryEdge(int(projection.parent.Fd()), int(directory.Fd()), existingWorktreeProjectionStage)
	if err != nil {
		return nil, ErrFilesystemConflict
	}
	closeOnError = false
	return stage, nil
}

func (stage *existingWorktreeProjectionTransaction) Close() error {
	if stage == nil {
		return nil
	}
	var result error
	if stage.lock != nil {
		result = errors.Join(result, stage.lock.Close())
		stage.lock = nil
	}
	if stage.directory != nil {
		result = errors.Join(result, stage.directory.Close())
		stage.directory = nil
	}
	return result
}

func createExistingWorktreeProjectionStageDirectory(parent *os.File) (*os.File, error) {
	if parent == nil {
		return nil, ErrInvalid
	}
	if err := unix.Mkdirat(int(parent.Fd()), existingWorktreeProjectionStage, 0o700); err != nil {
		return nil, ErrFilesystemConflict
	}
	fd, err := unix.Openat(int(parent.Fd()), existingWorktreeProjectionStage, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrFilesystemConflict
	}
	directory := os.NewFile(uintptr(fd), existingWorktreeProjectionStage)
	if _, err := observePrivateDirectoryEdge(int(parent.Fd()), fd, existingWorktreeProjectionStage); err != nil {
		directory.Close()
		return nil, ErrFilesystemConflict
	}
	if err := directory.Sync(); err != nil {
		directory.Close()
		return nil, err
	}
	if err := parent.Sync(); err != nil {
		directory.Close()
		return nil, err
	}
	return directory, nil
}

func existingWorktreeProjectionStageMarkerName(plans []*existingWorktreeProjectionPlan) (string, error) {
	type entry struct {
		Name          string `json:"name"`
		ContentDigest string `json:"contentDigest"`
	}
	entries := make([]entry, 0, len(plans))
	for _, plan := range plans {
		if plan == nil || !validExistingRelativeName(plan.name) {
			return "", ErrInvalid
		}
		entries = append(entries, entry{Name: plan.name, ContentDigest: digestBytes(plan.expected)})
	}
	digest, err := digestValue(entries)
	if err != nil {
		return "", err
	}
	return existingWorktreeStageMarker + strings.TrimPrefix(digest, "sha256:"), nil
}

// cleanupStage recognizes only the one reserved stage name and only layouts
// derivable from the current RB1 projection: empty creation residue, a marked
// partial build whose bytes are prefixes of current entries, or an unmarked
// prior live prefix left by the atomic swap. Unknown names, types or bytes are
// never deleted.
func (projection *existingWorktreeProjection) cleanupStage(plans []*existingWorktreeProjectionPlan) error {
	if projection == nil || projection.parent == nil {
		return ErrFilesystemConflict
	}
	var stageStat unix.Stat_t
	if err := unix.Fstatat(int(projection.parent.Fd()), existingWorktreeProjectionStage, &stageStat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return ErrFilesystemConflict
	}
	fd, err := unix.Openat(int(projection.parent.Fd()), existingWorktreeProjectionStage, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return ErrFilesystemConflict
	}
	directory := os.NewFile(uintptr(fd), existingWorktreeProjectionStage)
	defer directory.Close()
	initialCurrent, err := observePrivateDirectoryEdge(int(projection.parent.Fd()), fd, existingWorktreeProjectionStage)
	if err != nil {
		return ErrFilesystemConflict
	}
	if _, err := directory.Seek(0, 0); err != nil {
		return ErrFilesystemConflict
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return ErrFilesystemConflict
	}
	markerName, err := existingWorktreeProjectionStageMarkerName(plans)
	if err != nil {
		return err
	}
	plansByName := make(map[string]*existingWorktreeProjectionPlan, len(plans))
	for _, plan := range plans {
		plansByName[plan.name] = plan
	}
	hasMarker := false
	for _, dirEntry := range entries {
		if dirEntry.Name() == markerName {
			hasMarker = true
		}
	}
	cleanup := make([]existingWorktreeProjectionCleanupEntry, 0, len(entries))
	defer func() {
		for index := range cleanup {
			_ = cleanup[index].file.Close()
		}
	}()
	hasLock := false
	lockIndex := -1
	for _, dirEntry := range entries {
		if dirEntry.IsDir() || !validExistingRelativeName(dirEntry.Name()) {
			return ErrFilesystemConflict
		}
		name := dirEntry.Name()
		plan := plansByName[name]
		if name != existingWorktreeProjectionLock && name != markerName && plan == nil {
			return ErrAuthorityConflict
		}
		flags := unix.O_RDONLY
		if name == existingWorktreeProjectionLock {
			flags = unix.O_RDWR
			hasLock = true
		}
		file, openErr := openExistingWorktreeProjectionFile(directory, name, flags)
		if openErr != nil {
			return openErr
		}
		identity, identityErr := observeExistingWorktreeProjectionFile(directory, name, file)
		raw, readErr := readExistingWorktreeProjectionFile(file)
		if identityErr != nil || readErr != nil {
			file.Close()
			return ErrFilesystemConflict
		}
		if name == existingWorktreeProjectionLock {
			if len(raw) != 0 || unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
				file.Close()
				return ErrFilesystemConflict
			}
		} else if name == markerName {
			if len(raw) != 0 {
				file.Close()
				return ErrAuthorityConflict
			}
		} else if hasMarker {
			if len(raw) > len(plan.expected) || !bytes.Equal(raw, plan.expected[:len(raw)]) {
				file.Close()
				return ErrAuthorityConflict
			}
		} else if !validExistingWorktreeProjectionPrefix(raw, plan.expected) {
			file.Close()
			return ErrAuthorityConflict
		}
		cleanup = append(cleanup, existingWorktreeProjectionCleanupEntry{name: name, file: file, identity: identity})
		if name == existingWorktreeProjectionLock {
			lockIndex = len(cleanup) - 1
		}
	}
	if len(entries) > 0 && !hasLock {
		return ErrAuthorityConflict
	}
	current, err := observePrivateDirectoryEdge(int(projection.parent.Fd()), fd, existingWorktreeProjectionStage)
	if err != nil || !equalCanonical(current, initialCurrent) {
		return ErrFilesystemConflict
	}
	dataEntries := make([]*existingWorktreeProjectionCleanupEntry, 0, len(cleanup))
	for index := range cleanup {
		if index != lockIndex {
			dataEntries = append(dataEntries, &cleanup[index])
		}
	}
	sort.Slice(dataEntries, func(left, right int) bool { return dataEntries[left].name < dataEntries[right].name })
	for _, entry := range dataEntries {
		currentIdentity, err := observeExistingWorktreeProjectionFile(directory, entry.name, entry.file)
		if err != nil || !sameExistingWorktreeProjectionFile(currentIdentity, entry.identity) {
			return ErrFilesystemConflict
		}
		if err := unix.Unlinkat(fd, entry.name, 0); err != nil {
			return ErrFilesystemConflict
		}
		if err := projection.interruptCleanup(existingWorktreeCleanupAfterDataEntry); err != nil {
			return err
		}
	}
	if err := directory.Sync(); err != nil {
		return ErrFilesystemConflict
	}
	if err := projection.interruptCleanup(existingWorktreeCleanupAfterDataSync); err != nil {
		return err
	}
	if lockIndex >= 0 {
		lockEntry := &cleanup[lockIndex]
		currentIdentity, err := observeExistingWorktreeProjectionFile(directory, lockEntry.name, lockEntry.file)
		if err != nil || !sameExistingWorktreeProjectionFile(currentIdentity, lockEntry.identity) {
			return ErrFilesystemConflict
		}
		if err := unix.Unlinkat(fd, lockEntry.name, 0); err != nil {
			return ErrFilesystemConflict
		}
		if err := directory.Sync(); err != nil {
			return ErrFilesystemConflict
		}
	}
	if err := projection.interruptCleanup(existingWorktreeCleanupAfterLockSync); err != nil {
		return err
	}
	if _, err := observePrivateDirectoryEdge(int(projection.parent.Fd()), fd, existingWorktreeProjectionStage); err != nil {
		return ErrFilesystemConflict
	}
	if err := unix.Unlinkat(int(projection.parent.Fd()), existingWorktreeProjectionStage, unix.AT_REMOVEDIR); err != nil {
		return ErrFilesystemConflict
	}
	if err := projection.parent.Sync(); err != nil {
		return ErrFilesystemConflict
	}
	return nil
}

func verifyExistingWorktreeProjectionDirectory(directory *os.File, plans []*existingWorktreeProjectionPlan) error {
	if directory == nil {
		return ErrFilesystemConflict
	}
	expectedNames := map[string]bool{existingWorktreeProjectionLock: true}
	for _, plan := range plans {
		if plan == nil {
			return ErrFilesystemConflict
		}
		expectedNames[plan.name] = true
	}
	if err := verifyExistingWorktreeProjectionEntryNames(directory, expectedNames); err != nil {
		return err
	}
	for _, plan := range plans {
		file, err := openExistingWorktreeProjectionFile(directory, plan.name, unix.O_RDONLY)
		if err != nil {
			return err
		}
		raw, readErr := readExistingWorktreeProjectionFile(file)
		identity, identityErr := observeExistingWorktreeProjectionFile(directory, plan.name, file)
		file.Close()
		if readErr != nil || identityErr != nil || !bytes.Equal(raw, plan.expected) || identity.Object.Size != int64(len(plan.expected)) {
			return ErrFilesystemConflict
		}
	}
	return nil
}

func verifyExistingWorktreeProjectionEntryNames(directory *os.File, expected map[string]bool) error {
	if _, err := directory.Seek(0, 0); err != nil {
		return ErrFilesystemConflict
	}
	entries, err := directory.ReadDir(-1)
	if err != nil || len(entries) != len(expected) {
		return ErrFilesystemConflict
	}
	for _, entry := range entries {
		if entry.IsDir() || !expected[entry.Name()] {
			return ErrFilesystemConflict
		}
	}
	return nil
}

func projectionRecords(snapshot ExistingWorktreeAuthoritySnapshotV1) (map[string][]ExistingWorktreeProjectionRecordV1, error) {
	result := make(map[string][]ExistingWorktreeProjectionRecordV1)
	bindingTargets := make(map[string]string)
	for _, fact := range snapshot.Facts {
		var bindingDigest, targetDigest, requestDigest string
		switch fact.Kind {
		case ExistingWorktreeFactBindIntent:
			value, err := decodeExistingWorktreePayload[ExistingWorktreeBindIntentV1](fact)
			if err != nil {
				return nil, err
			}
			bindingDigest, targetDigest, requestDigest = value.BindingDigest, value.Observation.TargetIdentityDigest, value.Request.RequestDigest
			bindingTargets[bindingDigest] = targetDigest
		case ExistingWorktreeFactBindReceipt:
			value, err := decodeExistingWorktreePayload[ExistingWorktreeBindReceiptV1](fact)
			if err != nil {
				return nil, err
			}
			bindingDigest, targetDigest, requestDigest = value.BindingDigest, value.Observation.TargetIdentityDigest, value.RequestDigest
		case ExistingWorktreeFactReleaseIntent:
			value, err := decodeExistingWorktreePayload[ExistingWorktreeReleaseIntentV1](fact)
			if err != nil {
				return nil, err
			}
			bindingDigest, targetDigest, requestDigest = value.BindingDigest, value.TargetIdentityDigest, value.Request.RequestDigest
		case ExistingWorktreeFactReleaseReceipt:
			value, err := decodeExistingWorktreePayload[ExistingWorktreeReleaseReceiptV1](fact)
			if err != nil {
				return nil, err
			}
			bindingDigest, targetDigest, requestDigest = mustExistingWorktreeBindingDigest(value.Binding), value.TargetIdentityDigest, value.RequestDigest
		}
		if bindingTargets[bindingDigest] != targetDigest {
			return nil, ErrAuthorityConflict
		}
		name := strings.TrimPrefix(targetDigest, "sha256:") + ".jsonl"
		previous := fact.PreviousAttemptHeadDigest
		if records := result[name]; len(records) > 0 {
			previous = records[len(records)-1].ProjectionDigest
		}
		record := ExistingWorktreeProjectionRecordV1{AttemptRevision: fact.AttemptRevision, Kind: fact.Kind, AttemptFactDigest: fact.AttemptFactDigest, AuthorityPayloadDigest: fact.PayloadDigest, BindingDigest: bindingDigest, TargetIdentityDigest: targetDigest, RequestDigest: requestDigest, PreviousProjectionDigest: previous}
		if err := record.Seal(); err != nil {
			return nil, err
		}
		result[name] = append(result[name], record)
	}
	return result, nil
}

func existingWorktreeProjectionBytes(records []ExistingWorktreeProjectionRecordV1) ([]byte, error) {
	var result []byte
	for _, record := range records {
		payload, err := canonicalValue(record)
		if err != nil {
			return nil, err
		}
		result = append(result, payload...)
		result = append(result, '\n')
	}
	if len(result) > existingWorktreeProjectionLimit {
		return nil, ErrAuthorityConflict
	}
	return result, nil
}

func validExistingWorktreeProjectionPrefix(observed, expected []byte) bool {
	return len(observed) <= len(expected) && (len(observed) == 0 || observed[len(observed)-1] == '\n') && bytes.Equal(observed, expected[:len(observed)])
}

func openExistingWorktreeProjectionFile(parent *os.File, name string, flags int) (*os.File, error) {
	if parent == nil || !validExistingRelativeName(name) {
		return nil, ErrInvalid
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, ErrFilesystemConflict
	}
	file := os.NewFile(uintptr(fd), name)
	if _, err := observeExistingWorktreeProjectionFile(parent, name, file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func createExistingWorktreeProjectionFile(parent *os.File, name string) (*os.File, error) {
	if parent == nil || !validExistingRelativeName(name) {
		return nil, ErrInvalid
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDWR|unix.O_APPEND|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return nil, ErrFilesystemConflict
	}
	file := os.NewFile(uintptr(fd), name)
	if _, err := observeExistingWorktreeProjectionFile(parent, name, file); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, err
	}
	if err := parent.Sync(); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func observeExistingWorktreeProjectionFile(parent *os.File, name string, file *os.File) (existingWorktreeProjectionFileIdentity, error) {
	if parent == nil || file == nil || !validExistingRelativeName(name) {
		return existingWorktreeProjectionFileIdentity{}, ErrInvalid
	}
	var held, named unix.Stat_t
	if unix.Fstat(int(file.Fd()), &held) != nil || unix.Fstatat(int(parent.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !sameStat(held, named) || verifyPrivateRegular(held, uint32(unix.Geteuid())) != nil {
		return existingWorktreeProjectionFileIdentity{}, ErrFilesystemConflict
	}
	return existingWorktreeProjectionFileIdentity{Object: objectIdentity(held), MutationDigest: statMutationDigest(held)}, nil
}

func sameExistingWorktreeProjectionFile(left, right existingWorktreeProjectionFileIdentity) bool {
	return left.MutationDigest == right.MutationDigest && left.Object == right.Object
}

func readExistingWorktreeProjectionFile(file *os.File) ([]byte, error) {
	if file == nil {
		return nil, ErrFilesystemConflict
	}
	stat, err := file.Stat()
	if err != nil || stat.Size() < 0 || stat.Size() > existingWorktreeProjectionLimit {
		return nil, ErrFilesystemConflict
	}
	return io.ReadAll(io.NewSectionReader(file, 0, stat.Size()))
}

func closeExistingWorktreeProjectionPlans(plans []*existingWorktreeProjectionPlan) {
	for _, plan := range plans {
		if plan != nil && plan.preflight != nil {
			_ = plan.preflight.Close()
			plan.preflight = nil
		}
	}
}

func (projection *existingWorktreeProjection) revalidateAuthority() error {
	if projection == nil || projection.parent == nil || projection.directory == nil || projection.lock == nil {
		return ErrFilesystemConflict
	}
	current, err := observeDirectoryEdge(int(projection.parent.Fd()), int(projection.directory.Fd()), ExistingWorktreeProjectionDirectory)
	if err != nil || !equalCanonical(current, projection.directoryCurrentName) {
		return ErrFilesystemConflict
	}
	lock, err := observeExistingWorktreeProjectionFile(projection.directory, existingWorktreeProjectionLock, projection.lock)
	if err != nil || !sameExistingWorktreeProjectionFile(lock, projection.lockIdentity) || lock.Object.Size != 0 {
		return ErrFilesystemConflict
	}
	return nil
}
