//go:build linux

package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

var (
	launcherFile, launcherErr = sealedRunningExecutableFD()
)

func secureFDExecutionAvailable() bool { return launcherErr == nil && launcherFile != nil }

func secureFDExecutionReason() string {
	if launcherErr != nil {
		return "authenticated launcher initialization failed: " + launcherErr.Error()
	}
	if launcherFile == nil {
		return "authenticated launcher initialization failed: launcher image is unavailable"
	}
	return "authenticated fd execution is available"
}

func secureFDPath(fd int) string { return fmt.Sprintf("/proc/self/fd/%d", fd) }

// secureLauncherFD opens the running image through procfs, whose link is
// bound by the kernel to this process' executable inode. It never resolves
// os.Executable's mutable pathname.
func secureLauncherFD() (*os.File, error) {
	if launcherErr != nil || launcherFile == nil {
		return nil, fmt.Errorf("%w: %s", errSecureFDExecutionUnavailable, secureFDExecutionReason())
	}
	return launcherFile, nil
}

// openRunningExecutableFD deliberately follows only procfs' kernel-provided
// `exe` magic link. The proc mount and numeric PID directory are first pinned
// by descriptors and verified as procfs/non-symlink objects, so an attacker
// cannot substitute an ordinary pathname symlink for the running image.
func openRunningExecutableFD() (*os.File, error) {
	return openRunningExecutableFDAt("/proc")
}

// openRunningExecutableFDAt exists so tests can prove that a lookalike tree
// is rejected before either magic link is followed. Production always passes
// /proc.
func openRunningExecutableFDAt(procPath string) (*os.File, error) {
	procFD, err := unix.Open(procPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	proc := os.NewFile(uintptr(procFD), procPath)
	defer proc.Close()
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(procFD, &filesystem); err != nil {
		return nil, err
	}
	if uint64(filesystem.Type) != uint64(unix.PROC_SUPER_MAGIC) {
		return nil, errors.New("/proc is not a procfs mount")
	}
	// Follow only procfs' kernel-provided self magic link from the already
	// verified mount. A numeric os.Getpid() lookup is not equivalent when the
	// process and the pinned procfs mount observe different PID namespaces.
	pidFD, err := unix.Openat(procFD, "self", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	pidDirectory := os.NewFile(uintptr(pidFD), "proc-self")
	defer pidDirectory.Close()
	// `exe` is a procfs magic link by contract. O_NOFOLLOW would correctly
	// return ELOOP and is intentionally not used at this one verified edge.
	executableFD, err := unix.Openat(pidFD, "exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	executable := os.NewFile(uintptr(executableFD), "proc-self-exe")
	info, err := executable.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		_ = executable.Close()
		return nil, errors.New("procfs running image is not an executable regular file")
	}
	return executable, nil
}

func sealedRunningExecutableFD() (*os.File, error) {
	source, err := openRunningExecutableFD()
	if err != nil {
		return nil, err
	}
	defer source.Close()
	return sealOpenExecutable(source)
}

// sealedExecutableFD copies one opened source inode into an anonymous memfd,
// then applies write/grow/shrink/seal seals. Digest, probe and exec therefore
// consume immutable bytes even if the configured pathname or source inode is
// concurrently replaced or modified by another same-UID process.
func sealedExecutableFD(configured string) (*os.File, error) {
	source, err := openExecutableSourceFD(configured)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	return sealOpenExecutable(source)
}

func openExecutableSourceFD(configured string) (*os.File, error) {
	sourceFD, err := unix.Open(configured, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	source := os.NewFile(uintptr(sourceFD), configured)
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		_ = source.Close()
		return nil, errors.New("executable source inode is unavailable")
	}
	return source, nil
}

func sealOpenExecutable(source *os.File) (*os.File, error) {
	fd, err := unix.MemfdCreate("marshal-codex-exec", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	sealed := os.NewFile(uintptr(fd), "memfd:marshal-codex-exec")
	failed := true
	defer func() {
		if failed {
			_ = sealed.Close()
		}
	}()
	if _, err := io.Copy(sealed, source); err != nil {
		return nil, err
	}
	if err := unix.Fchmod(fd, 0o500); err != nil {
		return nil, err
	}
	seals := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, seals); err != nil {
		return nil, err
	}
	if _, err := sealed.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	failed = false
	return sealed, nil
}

func sealExecutableSourceFD(source *os.File) (*os.File, error) { return sealOpenExecutable(source) }

func readBinaryVersionFromFD(ctx context.Context, file *os.File) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	command := exec.CommandContext(probeCtx, "/proc/self/fd/3", "--version")
	command.ExtraFiles = []*os.File{file}
	return readBinaryVersionCommand(ctx, probeCtx, command)
}
