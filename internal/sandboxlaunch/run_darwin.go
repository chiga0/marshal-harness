//go:build darwin

package sandboxlaunch

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// RunChild executes the private inherited-FD protocol. It accepts no path,
// identity, or authority through argv or environment. Every failure happens
// before the release byte can reach a workload image.
func RunChild() error {
	if len(os.Environ()) != 0 {
		return protocolError("environment")
	}
	if os.Getppid() <= 1 {
		return protocolError("parent")
	}

	specFile := inheritedFile(SpecFD, "marshal-spec")
	readyFile := inheritedFile(ReadyFD, "marshal-ready")
	releaseFile := inheritedFile(ReleaseFD, "marshal-release")
	workingDirectory := inheritedFile(WorkingDirectoryFD, "marshal-cwd")
	executable := inheritedFile(ExecutableFD, "marshal-executable")
	marshalImage := inheritedFile(MarshalFD, "marshal-image")
	if specFile == nil || readyFile == nil || releaseFile == nil || workingDirectory == nil || executable == nil || marshalImage == nil {
		return protocolError("fd")
	}
	defer specFile.Close()
	defer readyFile.Close()
	defer releaseFile.Close()
	defer workingDirectory.Close()
	defer executable.Close()
	defer marshalImage.Close()
	raw, err := io.ReadAll(io.LimitReader(specFile, MaxSpecBytes+1))
	if err != nil || len(raw) > MaxSpecBytes {
		return protocolError("spec read")
	}
	spec, err := Decode(raw)
	if err != nil || spec.ParentPID != os.Getppid() {
		return protocolError("spec")
	}
	var closureFiles []*os.File
	for _, root := range spec.Roots {
		file := inheritedFile(uintptr(root.FD), "marshal-root")
		if file == nil {
			return protocolError("root fd")
		}
		closureFiles = append(closureFiles, file)
		defer file.Close()
	}
	for _, material := range spec.Materials {
		file := inheritedFile(uintptr(material.FD), "marshal-material")
		if file == nil {
			return protocolError("material fd")
		}
		closureFiles = append(closureFiles, file)
		defer file.Close()
	}

	checks := []struct {
		file       *os.File
		binding    ObjectBinding
		kind       uint32
		requireSHA bool
	}{
		{specFile, spec.SpecPipe, unix.S_IFIFO, false},
		{readyFile, spec.ReadyPipe, unix.S_IFIFO, false},
		{releaseFile, spec.ReleasePipe, unix.S_IFIFO, false},
		{workingDirectory, spec.WorkingDirectory, unix.S_IFDIR, false},
		{executable, spec.Executable, unix.S_IFREG, true},
		{marshalImage, spec.Marshal, unix.S_IFREG, true},
	}
	for _, check := range checks {
		if err := verifyFile(check.file, check.binding, check.kind, check.requireSHA); err != nil {
			return err
		}
	}
	for index, root := range spec.Roots {
		if err := verifyFile(closureFiles[index], root.Object, unix.S_IFDIR, false); err != nil {
			return err
		}
	}
	for index, material := range spec.Materials {
		if err := verifyFile(closureFiles[len(spec.Roots)+index], material.Object, unix.S_IFREG, true); err != nil {
			return err
		}
		if err := verifyPath(material.Path, material.Object); err != nil {
			return err
		}
	}
	if err := verifyPath(spec.ExecutablePath, spec.Executable); err != nil {
		return err
	}
	if err := verifyRunningMarshal(spec.Marshal); err != nil {
		return err
	}
	if err := unix.Fchdir(int(workingDirectory.Fd())); err != nil {
		return protocolError("fchdir")
	}
	if count, _ := readyFile.Write([]byte{ReadyByte}); count != 1 {
		return protocolError("ready")
	}
	if err := readyFile.Close(); err != nil {
		return protocolError("ready close")
	}

	var release [2]byte
	count, err := io.ReadFull(releaseFile, release[:1])
	if err != nil || count != 1 || release[0] != ReleaseByte {
		return protocolError("release")
	}
	count, err = releaseFile.Read(release[1:])
	if err != io.EOF || count != 0 {
		return protocolError("release framing")
	}

	// Recheck after authorization and immediately before exec. A parent-side
	// EVFILT_VNODE guard independently rejects any rename/write/swap event.
	for _, check := range checks[3:] {
		if err := verifyFile(check.file, check.binding, check.kind, check.requireSHA); err != nil {
			return err
		}
	}
	for index, root := range spec.Roots {
		if err := verifyFile(closureFiles[index], root.Object, unix.S_IFDIR, false); err != nil {
			return err
		}
	}
	for index, material := range spec.Materials {
		if err := verifyFile(closureFiles[len(spec.Roots)+index], material.Object, unix.S_IFREG, true); err != nil {
			return err
		}
		if err := verifyPath(material.Path, material.Object); err != nil {
			return err
		}
	}
	if err := verifyPath(spec.ExecutablePath, spec.Executable); err != nil {
		return err
	}
	if err := verifyRunningMarshal(spec.Marshal); err != nil {
		return err
	}

	_ = specFile.Close()
	_ = releaseFile.Close()
	_ = workingDirectory.Close()
	_ = marshalImage.Close()
	unix.CloseOnExec(ExecutableFD)
	for _, file := range closureFiles {
		_ = file.Close()
	}

	// Darwin exec-stop is the post-exec barrier. PT_TRACE_ME causes the kernel
	// to stop this exact child on the following exec, after the Node image is
	// installed but before its first userspace instruction (and therefore
	// before the provider entrypoint) can run.
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PTRACE, uintptr(syscall.PT_TRACE_ME), 0, 0, 0, 0, 0); errno != 0 {
		return protocolError("exec barrier")
	}

	if err := syscall.Exec(spec.ExecutablePath, spec.Arguments, spec.Environment); err != nil {
		return protocolError("exec")
	}
	return nil
}

func verifyPath(path string, expected ObjectBinding) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return protocolError("path open")
	}
	file := os.NewFile(uintptr(fd), "marshal-workload-path")
	defer file.Close()
	return verifyFile(file, expected, unix.S_IFREG, true)
}

func inheritedFile(fd uintptr, name string) *os.File {
	return os.NewFile(fd, name)
}

func verifyFile(file *os.File, expected ObjectBinding, kind uint32, requireSHA bool) error {
	if file == nil {
		return protocolError("fd")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return protocolError("fstat")
	}
	actual := bindingFromStat(stat)
	if actual.Mode&unix.S_IFMT != kind || !sameMetadata(actual, expected, requireSHA) {
		return protocolError("identity")
	}
	if requireSHA {
		digest, err := digestFile(file)
		if err != nil || digest != expected.SHA256 {
			return protocolError("digest")
		}
	}
	return nil
}

func verifyRunningMarshal(expected ObjectBinding) error {
	path, err := os.Executable()
	if err != nil {
		return protocolError("self path")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return protocolError("self open")
	}
	file := os.NewFile(uintptr(fd), "marshal-running-image")
	defer file.Close()
	return verifyFile(file, expected, unix.S_IFREG, true)
}

func bindingFromStat(stat unix.Stat_t) ObjectBinding {
	return ObjectBinding{
		Device: uint64(stat.Dev),
		Inode:  stat.Ino,
		Mode:   uint32(stat.Mode),
		UID:    stat.Uid,
		GID:    stat.Gid,
		Size:   stat.Size,
		Nlink:  uint64(stat.Nlink),
	}
}

func sameMetadata(actual, expected ObjectBinding, contentSensitive bool) bool {
	if actual.Device != expected.Device || actual.Inode != expected.Inode || actual.Mode != expected.Mode || actual.UID != expected.UID || actual.GID != expected.GID {
		return false
	}
	return !contentSensitive || actual.Size == expected.Size && actual.Nlink == expected.Nlink
}

func digestFile(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}
