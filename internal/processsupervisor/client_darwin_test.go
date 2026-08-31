//go:build darwin

package processsupervisor

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSupervisorCommandStartsInIndependentSession(t *testing.T) {
	command := newSupervisorCommand("/fixed/bin/marshal", nil, nil)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid {
		t.Fatal("fixed-image Supervisor would inherit the transient CLI terminal session")
	}
	if len(command.Env) != 0 || len(command.ExtraFiles) != 2 {
		t.Fatalf("supervisor launch boundary drifted: env=%v extraFiles=%d", command.Env, len(command.ExtraFiles))
	}
}

func TestControlSocketAddressUsesShortRepositoryRelativeLocator(t *testing.T) {
	workingDirectory := filepath.Join("/Users/example", strings.Repeat("repository", 9))
	directory := ControlDirectoryIdentity{
		CanonicalPath: filepath.Join(workingDirectory, ".marshal", "owner-control", "prepared-1234567890abcdef"),
		Device:        1, Inode: 2, FileType: "directory", UID: 501, GID: 20, Mode: unix.S_IFDIR | 0o700, LinkCount: 2,
	}
	address, err := controlSocketAddressFromWorkingDirectory(directory, workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(".marshal", "owner-control", "prepared-1234567890abcdef", controlSocket)
	if address != want {
		t.Fatalf("address=%q want=%q", address, want)
	}
	if len(filepath.Join(directory.CanonicalPath, controlSocket)) < len(unix.RawSockaddrUnix{}.Path) {
		t.Fatal("fixture absolute address did not exceed Darwin sockaddr limit")
	}
}

func TestControlSocketAddressRejectsNonDescendantAndLongRelativeLocator(t *testing.T) {
	base := "/Users/example/repository"
	directory := ControlDirectoryIdentity{
		CanonicalPath: "/Users/other/control", Device: 1, Inode: 2, FileType: "directory", UID: 501, GID: 20, Mode: unix.S_IFDIR | 0o700, LinkCount: 2,
	}
	if _, err := controlSocketAddressFromWorkingDirectory(directory, base); err == nil {
		t.Fatal("non-descendant control directory was admitted")
	}
	directory.CanonicalPath = filepath.Join(base, strings.Repeat("x", len(unix.RawSockaddrUnix{}.Path)), "control")
	if _, err := controlSocketAddressFromWorkingDirectory(directory, base); err == nil {
		t.Fatal("oversized relative socket address was admitted")
	}
}
