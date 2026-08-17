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

const secureFDPlatformReason = ""

var (
	launcherFile, launcherErr = sealedExecutableFD("/proc/self/exe")
)

func secureFDExecutionAvailable() bool { return true }

func secureFDPath(fd int) string { return fmt.Sprintf("/proc/self/fd/%d", fd) }

// secureLauncherFD opens the running image through procfs, whose link is
// bound by the kernel to this process' executable inode. It never resolves
// os.Executable's mutable pathname.
func secureLauncherFD() (*os.File, error) {
	if launcherErr != nil || launcherFile == nil {
		return nil, fmt.Errorf("%w: %v", errSecureFDExecutionUnavailable, launcherErr)
	}
	return launcherFile, nil
}

// sealedExecutableFD copies one opened source inode into an anonymous memfd,
// then applies write/grow/shrink/seal seals. Digest, probe and exec therefore
// consume immutable bytes even if the configured pathname or source inode is
// concurrently replaced or modified by another same-UID process.
func sealedExecutableFD(configured string) (*os.File, error) {
	sourceFD, err := unix.Open(configured, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	source := os.NewFile(uintptr(sourceFD), configured)
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("executable source inode is unavailable")
	}
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

func readBinaryVersionFromFD(ctx context.Context, file *os.File) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	command := exec.CommandContext(probeCtx, "/proc/self/fd/3", "--version")
	command.ExtraFiles = []*os.File{file}
	return readBinaryVersionCommand(ctx, probeCtx, command)
}
