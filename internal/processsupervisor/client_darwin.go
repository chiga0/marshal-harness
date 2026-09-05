//go:build darwin

package processsupervisor

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"golang.org/x/sys/unix"
)

const handshakeTimeout = 30 * time.Second

// ObserveCurrentCore returns the kernel-adjacent identity that must be bound by
// the caller's current durable control-owner-acquired fact. fixedMarshalPath is
// exact: aliases, PATH lookup and a different Marshal image are rejected.
func ObserveCurrentCore(fixedMarshalPath string) (CoreIdentity, error) {
	if !absoluteClean(fixedMarshalPath) {
		return CoreIdentity{}, ErrInvalid
	}
	identity, err := observeSelfIdentity()
	if err != nil || identity.Binary.CanonicalPath != fixedMarshalPath {
		return CoreIdentity{}, ErrConflict
	}
	return identity, nil
}

// ObserveHeldControlDirectory returns the exact descriptor and pathname
// identity used in authority. Callers retain ownership of file.
func ObserveHeldControlDirectory(file *os.File) (ControlDirectoryIdentity, error) {
	_, identity, err := observeControlDirectory(file)
	return identity, err
}

// ObserveHeldControlSocket observes the rendezvous descriptor-relatively. It
// does not search the filesystem or adopt an unknown socket.
func ObserveHeldControlSocket(directory *os.File) (ControlSocketIdentity, error) {
	return observeControlSocket(directory)
}

// Start is the retired v1 entry. Historical decoding remains available, but
// this fixed image cannot create or mutate a legacy session.
func Start(ctx context.Context, options StartOptions) (*Client, error) {
	if ctx == nil || options.ControlDirectory == nil || !absoluteClean(options.FixedMarshalPath) || options.Bootstrap.validate() != nil {
		return nil, ErrInvalid
	}
	// ADR 0079: the new fixed image never starts or adopts a v1 session.
	return nil, ErrUnavailable
}

// newSupervisorCommand detaches the long-lived fixed-image Supervisor from
// the invoking CLI's terminal session. Without a new session, closing a PTY
// after `task run` can deliver SIGHUP to both Supervisor and Worker, making
// the next fixed CLI unable to attach even though RUNNING was committed.
// Empty env and inherited descriptor positions remain unchanged.
func newSupervisorCommand(fixedMarshalPath string, bootstrap, controlDirectory *os.File) *exec.Cmd {
	command := exec.Command(fixedMarshalPath, "internal", "process-supervisor")
	command.Env = []string{}
	command.ExtraFiles = []*os.File{bootstrap, controlDirectory}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return command
}

// Reconnect is retained as a fail-closed v1 API, not an adoption path.
func Reconnect(ctx context.Context, options ReconnectOptions) (*Client, error) {
	if ctx == nil || options.ControlDirectory == nil || !absoluteClean(options.FixedMarshalPath) {
		return nil, ErrInvalid
	}
	return nil, ErrUnavailable
}

type attachControlSnapshot struct {
	Directory     ControlDirectoryIdentity
	Socket        ControlSocketIdentity
	Files         SessionControlFiles
	NonceSize     int64
	NonceDigest   string
	JournalSize   int64
	JournalDigest string
}

// WithAttached rejects legacy adoption before opening a connection. Only
// WithAttachedV2 can borrow a live session in the new fixed image.
func WithAttached(ctx context.Context, options AttachOptions, fn func(*AttachedSession) error) error {
	if ctx == nil || options.ControlDirectory == nil || !absoluteClean(options.FixedMarshalPath) ||
		options.ControlDirectoryIdentity.validate() != nil || options.Authority.validate() != nil || options.OwnerVerifier == nil || fn == nil {
		return ErrInvalid
	}
	// Reject before any owner callback, socket, nonce read or continuation.
	return ErrUnavailable
}

// controlSocketAddress keeps Darwin AF_UNIX rendezvous addresses below the
// kernel sockaddr limit without changing the process working directory. The
// fixed CLI is composed from the repository root and owner-control lives below
// that root, so a clean descendant-relative locator is both shorter and more
// precise than the potentially very long canonical pathname. Authority still
// comes from the held directory/socket identities and the fixed-binary peer
// check before and after connect; this relative pathname grants none.
func controlSocketAddress(directory ControlDirectoryIdentity) (string, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", ErrConflict
	}
	return controlSocketAddressFromWorkingDirectory(directory, workingDirectory)
}

func controlSocketAddressFromWorkingDirectory(directory ControlDirectoryIdentity, workingDirectory string) (string, error) {
	if directory.validate() != nil || !absoluteClean(workingDirectory) {
		return "", ErrInvalid
	}
	relative, err := filepath.Rel(workingDirectory, directory.CanonicalPath)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", ErrConflict
	}
	address := filepath.Join(relative, controlSocket)
	// RawSockaddrUnix.Path reserves one byte for the trailing NUL used by
	// pathname sockets. Reject deterministically instead of surfacing an opaque
	// net.DialUnix "path too long" after durable owner succession.
	if len(address) >= len(unix.RawSockaddrUnix{}.Path) {
		return "", ErrConflict
	}
	return address, nil
}

func captureAttachControlSnapshot(directory *os.File, identity ControlDirectoryIdentity, held *heldSessionControlFiles) (attachControlSnapshot, JournalSnapshot, error) {
	if directory == nil || held == nil || identity.validate() != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, ErrInvalid
	}
	observed, err := ObserveHeldControlDirectory(directory)
	if err != nil || !sameControlDirectoryObject(observed, identity) || revalidateHeldSessionControlFiles(directory, held, held.identity) != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, ErrConflict
	}
	socket, err := ObserveHeldControlSocket(directory)
	if err != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, ErrConflict
	}
	nonceIdentity, nonceSize, nonceDigest, err := digestHeldControlFile(held.nonce, nonceBytes)
	if err != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, err
	}
	journalIdentity, journalSize, journalDigest, err := digestHeldControlFile(held.journal, MaxJournalFileBytes)
	if err != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, err
	}
	if nonceIdentity != held.identity.Nonce || journalIdentity != held.identity.Journal {
		return attachControlSnapshot{}, JournalSnapshot{}, ErrConflict
	}
	journal, err := readHeldJournalSnapshot(held.journal)
	if err != nil || revalidateControlDirectoryForSnapshot(directory, identity, journal) != nil {
		return attachControlSnapshot{}, JournalSnapshot{}, ErrConflict
	}
	return attachControlSnapshot{Directory: observed, Socket: socket, Files: held.identity, NonceSize: nonceSize, NonceDigest: nonceDigest, JournalSize: journalSize, JournalDigest: journalDigest}, journal, nil
}

func digestHeldControlFile(file *os.File, limit int) (ControlFileIdentity, int64, string, error) {
	identity, size, err := observeControlFile(file)
	if err != nil || size <= 0 || size > int64(limit) {
		return ControlFileIdentity{}, 0, "", ErrConflict
	}
	data, err := io.ReadAll(io.NewSectionReader(file, 0, size))
	if err != nil || int64(len(data)) != size {
		return ControlFileIdentity{}, 0, "", ErrIntervention
	}
	after, afterSize, err := observeControlFile(file)
	if err != nil || after != identity || afterSize != size {
		return ControlFileIdentity{}, 0, "", ErrConflict
	}
	return identity, size, canonical.DigestBytes(data), nil
}

func revalidateHeldRuntimeControlBoundary(directory *os.File, identity ControlDirectoryIdentity, held *heldSessionControlFiles, anchor HandshakeAnchor) error {
	if held == nil || revalidateHeldSessionControlFiles(directory, held, anchor.ControlFiles) != nil || observeControlSocketExact(directory, anchor.ControlSocket) != nil {
		return ErrConflict
	}
	snapshot, err := readHeldJournalSnapshot(held.journal)
	if err != nil || revalidateControlDirectoryForSnapshot(directory, identity, snapshot) != nil || revalidateHeldSessionControlFiles(directory, held, anchor.ControlFiles) != nil {
		return ErrConflict
	}
	return nil
}

func abortStartedSupervisor(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
	done := make(chan struct{})
	go func() {
		_ = command.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}
