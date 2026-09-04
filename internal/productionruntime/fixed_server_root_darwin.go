//go:build darwin && arm64

package productionruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/chiga0/marshal-harness/internal/allocationcontrol"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const fixedServerPathBufferSize = 4096

var fixedServerRootComponents = [...]string{".marshal", "runtime-v1", "control", "delivery-v1"}

type fixedServerDirectoryIdentity struct {
	Device    uint64                `json:"device"`
	Inode     uint64                `json:"inode"`
	FileType  uint32                `json:"fileType"`
	UID       uint32                `json:"uid"`
	GID       uint32                `json:"gid"`
	Mode      uint32                `json:"mode"`
	LinkCount uint64                `json:"linkCount"`
	Mutation  ownerMutationIdentity `json:"mutation"`
}

type fixedServerDirectoryNode struct {
	file     *os.File
	identity fixedServerDirectoryIdentity
	name     string
}

// fixedServerRoot is the complete held parent/child capability. A delivery
// leaf is never authorized by a standalone path or child descriptor.
type fixedServerRoot struct {
	repositoryPath string
	nodes          [5]fixedServerDirectoryNode
}

// CanonicalRepositoryRoot is the fixed CLI's held repository capability. It
// freezes the accepted current pathname and object at open time so a rename
// before RepositorySession construction cannot be washed through F_GETPATH.
type CanonicalRepositoryRoot struct {
	file     *os.File
	path     string
	identity fixedServerDirectoryIdentity
}

func OpenCanonicalRepositoryRoot(path string) (*CanonicalRepositoryRoot, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexByte(path, 0) >= 0 {
		return nil, ErrFixedDeliveryConflict
	}
	file, identity, err := openCanonicalRepositoryDirectory(path)
	if err != nil {
		return nil, err
	}
	return &CanonicalRepositoryRoot{file: file, path: path, identity: identity}, nil
}

func openCanonicalRepositoryDirectory(path string) (*os.File, fixedServerDirectoryIdentity, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, fixedServerDirectoryIdentity{}, ErrFixedDeliveryConflict
	}
	file := os.NewFile(uintptr(fd), "marshal-canonical-repository-root")
	if file == nil {
		_ = unix.Close(fd)
		return nil, fixedServerDirectoryIdentity{}, ErrFixedDeliveryConflict
	}
	observed, err := descriptorPath(fd)
	if err != nil || observed != path {
		_ = file.Close()
		return nil, fixedServerDirectoryIdentity{}, ErrFixedDeliveryConflict
	}
	identity, err := observeFixedServerDirectory(fd, false)
	if err != nil {
		_ = file.Close()
		return nil, fixedServerDirectoryIdentity{}, err
	}
	return file, identity, nil
}

func (root *CanonicalRepositoryRoot) validateAndReopen() (*os.File, error) {
	if root == nil || root.file == nil || root.path == "" {
		return nil, ErrFixedDeliveryConflict
	}
	heldPath, pathErr := descriptorPath(int(root.file.Fd()))
	held, heldErr := observeFixedServerDirectory(int(root.file.Fd()), false)
	if pathErr != nil || heldErr != nil || heldPath != root.path || !sameFixedServerDirectory(held, root.identity, true) {
		return nil, ErrFixedDeliveryConflict
	}
	reopened, reopenedIdentity, err := openCanonicalRepositoryDirectory(root.path)
	if err != nil || !sameFixedServerDirectory(reopenedIdentity, root.identity, true) {
		if reopened != nil {
			_ = reopened.Close()
		}
		return nil, ErrFixedDeliveryConflict
	}
	return reopened, nil
}

func (root *CanonicalRepositoryRoot) Close() error {
	if root == nil || root.file == nil {
		return nil
	}
	err := root.file.Close()
	root.file = nil
	return err
}

func descriptorPath(fd int) (string, error) {
	buffer := make([]byte, fixedServerPathBufferSize)
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETPATH, int(uintptr(unsafe.Pointer(&buffer[0])))); err != nil {
		return "", err
	}
	end := 0
	for end < len(buffer) && buffer[end] != 0 {
		end++
	}
	path := string(buffer[:end])
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexByte(path, 0) >= 0 {
		return "", ErrFixedDeliveryConflict
	}
	return path, nil
}

func observeFixedServerDirectory(fd int, ownerOnly bool) (fixedServerDirectoryIdentity, error) {
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil {
		return fixedServerDirectoryIdentity{}, ErrFixedDeliveryConflict
	}
	return fixedServerDirectoryIdentityFromStat(stat, ownerOnly)
}

func observeFixedServerDirectoryAt(parentFD int, name string, ownerOnly bool) (fixedServerDirectoryIdentity, error) {
	var stat unix.Stat_t
	if unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return fixedServerDirectoryIdentity{}, ErrFixedDeliveryConflict
	}
	return fixedServerDirectoryIdentityFromStat(stat, ownerOnly)
}

func fixedServerDirectoryIdentityFromStat(stat unix.Stat_t, ownerOnly bool) (fixedServerDirectoryIdentity, error) {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Gid != uint32(os.Getegid()) || stat.Nlink < 2 {
		return fixedServerDirectoryIdentity{}, ErrFixedDeliveryConflict
	}
	permissions := stat.Mode & 0o777
	if ownerOnly && permissions != 0o700 || !ownerOnly && permissions&0o022 != 0 {
		return fixedServerDirectoryIdentity{}, ErrFixedDeliveryConflict
	}
	return fixedServerDirectoryIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, FileType: uint32(stat.Mode & unix.S_IFMT), UID: stat.Uid, GID: stat.Gid, Mode: uint32(stat.Mode), LinkCount: uint64(stat.Nlink), Mutation: mutationIdentity(stat)}, nil
}

func sameFixedServerDirectory(left, right fixedServerDirectoryIdentity, exactMutation bool) bool {
	// Link count can increase while the fixed hierarchy is created and while
	// immutable records are appended; it remains a validity check, not object
	// identity. All security-relevant stable fields remain exact.
	return left.Device == right.Device && left.Inode == right.Inode && left.FileType == right.FileType && left.UID == right.UID && left.GID == right.GID && left.Mode == right.Mode && (!exactMutation || left.Mutation == right.Mutation)
}

func openFixedServerRoot(repository *CanonicalRepositoryRoot) (fixedServerRoot, error) {
	if repository == nil {
		return fixedServerRoot{}, ErrFixedDeliveryConflict
	}
	reopened, err := repository.validateAndReopen()
	if err != nil {
		return fixedServerRoot{}, err
	}
	root := fixedServerRoot{repositoryPath: repository.path}
	root.nodes[0] = fixedServerDirectoryNode{file: reopened, identity: repository.identity}
	cleanup := func(cause error) (fixedServerRoot, error) {
		_ = root.close()
		return fixedServerRoot{}, cause
	}
	for index, component := range fixedServerRootComponents {
		parent := root.nodes[index].file
		if err := validateFixedServerRoot(root, index+1); err != nil {
			return cleanup(err)
		}
		created := false
		if err := unix.Mkdirat(int(parent.Fd()), component, 0o700); err != nil {
			if !errors.Is(err, unix.EEXIST) {
				return cleanup(ErrFixedDeliveryConflict)
			}
		} else {
			created = true
		}
		fd, err := unix.Openat(int(parent.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
		if err != nil {
			return cleanup(ErrFixedDeliveryConflict)
		}
		child := os.NewFile(uintptr(fd), "marshal-fixed-server-"+component)
		if child == nil {
			_ = unix.Close(fd)
			return cleanup(ErrFixedDeliveryConflict)
		}
		identity, err := observeFixedServerDirectory(fd, true)
		named, namedErr := observeFixedServerDirectoryAt(int(parent.Fd()), component, true)
		if err != nil || namedErr != nil || !sameFixedServerDirectory(identity, named, true) {
			_ = child.Close()
			return cleanup(ErrFixedDeliveryConflict)
		}
		root.nodes[index+1] = fixedServerDirectoryNode{file: child, identity: identity, name: component}
		if created {
			if child.Sync() != nil || parent.Sync() != nil {
				return cleanup(ErrFixedDeliveryUnknown)
			}
			parentIdentity, parentErr := observeFixedServerDirectory(int(parent.Fd()), index != 0)
			childIdentity, childErr := observeFixedServerDirectory(fd, true)
			if parentErr != nil || childErr != nil {
				return cleanup(ErrFixedDeliveryConflict)
			}
			root.nodes[index].identity = parentIdentity
			root.nodes[index+1].identity = childIdentity
		}
		if err := validateFixedServerRoot(root, index+2); err != nil {
			return cleanup(err)
		}
	}
	// Existing-worktree projections are produced lazily by the first real
	// StartRun. Materialize their stable parent before freezing runtime-v1;
	// otherwise that legitimate first projection adds a child directory and
	// makes the fixed server reject its own delivery receipt as root drift.
	// Files inside this directory remain rebuildable and are still validated by
	// allocationcontrol; only the parent name/object is admitted here.
	if err := ensureFixedExistingWorktreeProjectionRoot(root.nodes[2].file); err != nil {
		return cleanup(err)
	}
	// Directory creation legitimately changes parent link counts. Refresh the
	// complete post-create observation once, so the authority-root digest is
	// stable across a cold reopen of the same hierarchy.
	for index := range root.nodes {
		identity, err := observeFixedServerDirectory(int(root.nodes[index].file.Fd()), index != 0)
		if err != nil {
			return cleanup(err)
		}
		root.nodes[index].identity = identity
	}
	if err := validateFixedServerRoot(root, len(root.nodes)); err != nil {
		return cleanup(err)
	}
	// Creating the fixed hierarchy is the one authorized mutation of the
	// canonical repository object during construction. Keep the caller-held
	// capability usable for a later owner generation by advancing its frozen
	// mutation observation only after the complete held/name chain has been
	// revalidated. Device, inode, permissions and current pathname remain
	// exact; an external replacement or incomplete hierarchy never reaches
	// this adoption point.
	repository.identity = root.nodes[0].identity
	return root, nil
}

// openExistingFixedServerRoot opens the already-published fixed-server
// hierarchy without creating any directory. It is the read-only locator root
// used by a separate fixed Marshal client while the resident server owns the
// repository session.
func openExistingFixedServerRoot(repository *CanonicalRepositoryRoot) (fixedServerRoot, error) {
	if repository == nil {
		return fixedServerRoot{}, ErrFixedDeliveryConflict
	}
	reopened, err := repository.validateAndReopen()
	if err != nil {
		return fixedServerRoot{}, err
	}
	root := fixedServerRoot{repositoryPath: repository.path}
	root.nodes[0] = fixedServerDirectoryNode{file: reopened, identity: repository.identity}
	cleanup := func(cause error) (fixedServerRoot, error) {
		_ = root.close()
		return fixedServerRoot{}, cause
	}
	for index, component := range fixedServerRootComponents {
		parent := root.nodes[index].file
		fd, err := unix.Openat(int(parent.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
		if err != nil {
			return cleanup(ErrFixedDeliveryConflict)
		}
		child := os.NewFile(uintptr(fd), "marshal-fixed-server-client-"+component)
		if child == nil {
			_ = unix.Close(fd)
			return cleanup(ErrFixedDeliveryConflict)
		}
		identity, identityErr := observeFixedServerDirectory(fd, true)
		named, namedErr := observeFixedServerDirectoryAt(int(parent.Fd()), component, true)
		if identityErr != nil || namedErr != nil || !sameFixedServerDirectory(identity, named, true) {
			_ = child.Close()
			return cleanup(ErrFixedDeliveryConflict)
		}
		root.nodes[index+1] = fixedServerDirectoryNode{file: child, identity: identity, name: component}
	}
	if err := validateFixedServerRoot(root, len(root.nodes)); err != nil {
		return cleanup(err)
	}
	return root, nil
}

func validateFixedServerRoot(root fixedServerRoot, count int) error {
	if count < 1 || count > len(root.nodes) || root.nodes[0].file == nil {
		return ErrFixedDeliveryConflict
	}
	reopened, reopenedIdentity, err := openCanonicalRepositoryDirectory(root.repositoryPath)
	if err != nil {
		return ErrFixedDeliveryConflict
	}
	_ = reopened.Close()
	heldRoot, heldErr := observeFixedServerDirectory(int(root.nodes[0].file.Fd()), false)
	if heldErr != nil || !sameFixedServerDirectory(heldRoot, root.nodes[0].identity, true) || !sameFixedServerDirectory(reopenedIdentity, root.nodes[0].identity, true) {
		return ErrFixedDeliveryConflict
	}
	for index := 1; index < count; index++ {
		parent, child := root.nodes[index-1], root.nodes[index]
		if parent.file == nil || child.file == nil || child.name == "" {
			return ErrFixedDeliveryConflict
		}
		held, err := observeFixedServerDirectory(int(child.file.Fd()), true)
		named, namedErr := observeFixedServerDirectoryAt(int(parent.file.Fd()), child.name, true)
		// delivery-v1 is the append-only record directory, so publishing a
		// record legitimately changes its own ctime. Its current name is still
		// protected against ABA by the exact mutation identity of control.
		exactMutation := index < len(root.nodes)-1
		if err != nil || namedErr != nil || !sameFixedServerDirectory(held, child.identity, exactMutation) || !sameFixedServerDirectory(named, child.identity, exactMutation) {
			return ErrFixedDeliveryConflict
		}
	}
	return nil
}

func ensureFixedExistingWorktreeProjectionRoot(runtimeRoot *os.File) error {
	if runtimeRoot == nil {
		return ErrFixedDeliveryConflict
	}
	created := false
	if err := unix.Mkdirat(int(runtimeRoot.Fd()), allocationcontrol.ExistingWorktreeProjectionDirectory, 0o700); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return ErrFixedDeliveryConflict
		}
	} else {
		created = true
	}
	fd, err := unix.Openat(int(runtimeRoot.Fd()), allocationcontrol.ExistingWorktreeProjectionDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return ErrFixedDeliveryConflict
	}
	projectionRoot := os.NewFile(uintptr(fd), "marshal-fixed-server-existing-worktree-bindings")
	if projectionRoot == nil {
		_ = unix.Close(fd)
		return ErrFixedDeliveryConflict
	}
	defer projectionRoot.Close()
	held, heldErr := observeFixedServerDirectory(fd, true)
	named, namedErr := observeFixedServerDirectoryAt(int(runtimeRoot.Fd()), allocationcontrol.ExistingWorktreeProjectionDirectory, true)
	if heldErr != nil || namedErr != nil || !sameFixedServerDirectory(held, named, true) {
		return ErrFixedDeliveryConflict
	}
	if created && (projectionRoot.Sync() != nil || runtimeRoot.Sync() != nil) {
		return ErrFixedDeliveryUnknown
	}
	return nil
}

// adoptFixedServerRuntimeMutation advances runtime-v1 only after the caller
// has independently joined the current RB1 ledger to the exact derived
// projection bytes. The runtime directory must still contain exactly the two
// admitted children, and the authoritative control/delivery chain must retain
// its pre-mutation identity. This admits the projection's RENAME_SWAP without
// washing through an unrelated sibling insertion or control-directory ABA.
func adoptFixedServerRuntimeMutation(root *fixedServerRoot) error {
	if root == nil || validateFixedServerRoot(*root, 2) != nil {
		return ErrFixedDeliveryConflict
	}
	marshalRoot := root.nodes[1]
	runtimeRoot := root.nodes[2]
	held, heldErr := observeFixedServerDirectory(int(runtimeRoot.file.Fd()), true)
	named, namedErr := observeFixedServerDirectoryAt(int(marshalRoot.file.Fd()), runtimeRoot.name, true)
	if heldErr != nil || namedErr != nil || !sameFixedServerDirectory(held, runtimeRoot.identity, false) || !sameFixedServerDirectory(named, runtimeRoot.identity, false) || !sameFixedServerDirectory(held, named, true) {
		return ErrFixedDeliveryConflict
	}
	if _, err := runtimeRoot.file.Seek(0, 0); err != nil {
		return ErrFixedDeliveryConflict
	}
	entries, err := runtimeRoot.file.ReadDir(-1)
	if err != nil || len(entries) != 2 {
		return ErrFixedDeliveryConflict
	}
	expected := map[string]bool{
		fixedServerRootComponents[2]:                          false,
		allocationcontrol.ExistingWorktreeProjectionDirectory: false,
	}
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok || !entry.IsDir() || expected[entry.Name()] {
			return ErrFixedDeliveryConflict
		}
		expected[entry.Name()] = true
	}
	for _, found := range expected {
		if !found {
			return ErrFixedDeliveryConflict
		}
	}
	control := root.nodes[3]
	controlHeld, controlHeldErr := observeFixedServerDirectory(int(control.file.Fd()), true)
	controlNamed, controlNamedErr := observeFixedServerDirectoryAt(int(runtimeRoot.file.Fd()), control.name, true)
	if controlHeldErr != nil || controlNamedErr != nil || !sameFixedServerDirectory(controlHeld, control.identity, true) || !sameFixedServerDirectory(controlNamed, control.identity, true) || !sameFixedServerDirectory(controlHeld, controlNamed, true) {
		return ErrFixedDeliveryConflict
	}
	delivery := root.nodes[4]
	deliveryHeld, deliveryHeldErr := observeFixedServerDirectory(int(delivery.file.Fd()), true)
	deliveryNamed, deliveryNamedErr := observeFixedServerDirectoryAt(int(control.file.Fd()), delivery.name, true)
	if deliveryHeldErr != nil || deliveryNamedErr != nil || !sameFixedServerDirectory(deliveryHeld, delivery.identity, false) || !sameFixedServerDirectory(deliveryNamed, delivery.identity, false) || !sameFixedServerDirectory(deliveryHeld, deliveryNamed, true) {
		return ErrFixedDeliveryConflict
	}
	projection, projectionErr := observeFixedServerDirectoryAt(int(runtimeRoot.file.Fd()), allocationcontrol.ExistingWorktreeProjectionDirectory, true)
	if projectionErr != nil || projection.Device == 0 || projection.Inode == 0 {
		return ErrFixedDeliveryConflict
	}
	root.nodes[2].identity = held
	return validateFixedServerRoot(*root, len(root.nodes))
}

// adoptFixedServerControlMutation advances only the frozen mutation
// observation of the already-held control directory after the current owner
// has created or removed exact endpoint objects. Stable object/name fields and
// the complete child chain remain mandatory; ordinary rechecks never call it.
func adoptFixedServerControlMutation(root *fixedServerRoot) error {
	if root == nil || validateFixedServerRoot(*root, 3) != nil {
		return ErrFixedDeliveryConflict
	}
	parent := root.nodes[2]
	control := root.nodes[3]
	held, heldErr := observeFixedServerDirectory(int(control.file.Fd()), true)
	named, namedErr := observeFixedServerDirectoryAt(int(parent.file.Fd()), control.name, true)
	if heldErr != nil || namedErr != nil || !sameFixedServerDirectory(held, control.identity, false) || !sameFixedServerDirectory(named, control.identity, false) || !sameFixedServerDirectory(held, named, true) {
		return ErrFixedDeliveryConflict
	}
	root.nodes[3].identity = held
	return validateFixedServerRoot(*root, len(root.nodes))
}

// stateRoot returns the held canonical RepositoryRoot/.marshal directory.
//
// runtime-v1 is the private namespace for owner, ingress, control and other
// runtime projections. Run journals intentionally remain at StateRoot/runs;
// opening the RunStore on runtime-v1 would create a second, empty authority
// root and make every CLI-created Run appear unknown to the fixed session.
func (root fixedServerRoot) stateRoot() *os.File    { return root.nodes[1].file }
func (root fixedServerRoot) deliveryRoot() *os.File { return root.nodes[4].file }

func (root fixedServerRoot) digest() (string, error) {
	if err := validateFixedServerRoot(root, len(root.nodes)); err != nil {
		return "", err
	}
	// The authority identity binds stable held objects and their closed names,
	// but deliberately excludes ctime/birthtime observations. Immutable
	// delivery appends change those observations without changing the root;
	// strict successors must be able to re-derive the same identity.
	type stableDirectoryIdentity struct {
		Device   uint64 `json:"device"`
		Inode    uint64 `json:"inode"`
		FileType uint32 `json:"fileType"`
		UID      uint32 `json:"uid"`
		GID      uint32 `json:"gid"`
		Mode     uint32 `json:"mode"`
	}
	value := struct {
		RepositoryPath string                     `json:"repositoryPath"`
		Objects        [5]stableDirectoryIdentity `json:"objects"`
		Names          [4]string                  `json:"names"`
	}{RepositoryPath: root.repositoryPath, Names: fixedServerRootComponents}
	for index := range root.nodes {
		identity := root.nodes[index].identity
		value.Objects[index] = stableDirectoryIdentity{Device: identity.Device, Inode: identity.Inode, FileType: identity.FileType, UID: identity.UID, GID: identity.GID, Mode: identity.Mode}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return canonical.DigestJSON(raw)
}

func (root *fixedServerRoot) close() error {
	if root == nil {
		return nil
	}
	var result error
	for index := len(root.nodes) - 1; index >= 0; index-- {
		if root.nodes[index].file != nil {
			result = errors.Join(result, root.nodes[index].file.Close())
			root.nodes[index].file = nil
		}
	}
	return result
}
