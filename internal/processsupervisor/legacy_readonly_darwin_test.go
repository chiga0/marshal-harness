//go:build darwin

package processsupervisor

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRetiredLegacyClientsRejectBeforeIOOrOwnerBorrow(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	bootstrap := validBootstrap()
	if _, err := Start(context.Background(), StartOptions{FixedMarshalPath: "/fixed/bin/marshal", ControlDirectory: directory, Bootstrap: bootstrap}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("legacy start: %v", err)
	}
	if _, err := Reconnect(context.Background(), ReconnectOptions{FixedMarshalPath: "/fixed/bin/marshal", ControlDirectory: directory}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("legacy reconnect: %v", err)
	}
	authority := validAttachAuthority()
	verifier := attachVerifierFunc(func(context.Context, AttachAuthority, func() error) error {
		t.Fatal("legacy attach borrowed current owner")
		return ErrConflict
	})
	err = WithAttached(context.Background(), AttachOptions{FixedMarshalPath: "/fixed/bin/marshal", ControlDirectory: directory, ControlDirectoryIdentity: bootstrap.ControlDirectoryIdentity, Authority: authority, OwnerVerifier: verifier}, func(*AttachedSession) error {
		t.Fatal("legacy attach yielded mutation capability")
		return nil
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("legacy attach: %v", err)
	}
	entries, err := directory.ReadDir(-1)
	if err != nil || len(entries) != 0 {
		t.Fatal("retired client modified control directory")
	}
}

func TestProductionBootstrapRejectsLegacyBeforeCreatingControlObjects(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	clientFile, serverFile := os.NewFile(uintptr(fds[0]), "legacy-reject-client"), os.NewFile(uintptr(fds[1]), "legacy-reject-server")
	defer serverFile.Close()
	connection, err := net.FileConn(clientFile)
	_ = clientFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runSupervisorLoop(ctx, serverFile, directory, supervisorLoopOptions{requireV2: true, observeSelf: func() (CoreIdentity, error) {
			t.Error("legacy request reached platform identity observation")
			return CoreIdentity{}, ErrConflict
		}})
	}()
	codec, err := NewProtocolCodec(connection)
	if err != nil || codec.Write(validBootstrap()) != nil {
		t.Fatalf("legacy frame: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("legacy production bootstrap: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("legacy bootstrap did not fail boundedly")
	}
	entries, err := directory.ReadDir(-1)
	if err != nil || len(entries) != 0 {
		t.Fatal("legacy production bootstrap wrote objects")
	}
}
