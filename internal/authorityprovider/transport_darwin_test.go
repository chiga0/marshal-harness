//go:build darwin

package authorityprovider

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDarwinStreamTransportCarriesFramedPayloadAndFD(t *testing.T) {
	tempDir, err := os.MkdirTemp("/tmp", "apap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)
	endpoint := filepath.Join(tempDir, "sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: endpoint, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.AcceptUnix()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		payload, oob, _, readErr := readStreamFrame(connection)
		if readErr != nil {
			serverDone <- readErr
			return
		}
		messages, parseErr := unix.ParseSocketControlMessage(oob)
		if parseErr != nil || len(messages) != 1 {
			serverDone <- unix.EPROTO
			return
		}
		fds, parseErr := unix.ParseUnixRights(&messages[0])
		if parseErr != nil || len(fds) != 1 {
			serverDone <- unix.EPROTO
			return
		}
		_ = unix.Close(fds[0])
		if string(payload) != "request" {
			serverDone <- unix.EPROTO
			return
		}
		serverDone <- writeStreamFrame(connection, []byte("response"), nil)
	}()

	client, err := NewControlClient(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := client.RoundTrip(ctx, []byte("request"), file)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Close()
	if string(response.Payload) != "response" {
		t.Fatalf("response payload = %q", response.Payload)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
