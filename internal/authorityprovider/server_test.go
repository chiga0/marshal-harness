package authorityprovider

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServeControlRequiresAuthenticatedHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ServeControl(ctx, nil, nil, nil); err == nil {
		t.Fatal("nil APAP server inputs were accepted")
	}
}

func TestListenAndServeControlRoundTripWithPeerBinding(t *testing.T) {
	tempDir, err := os.MkdirTemp("/tmp", "apap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)
	endpoint := filepath.Join(tempDir, "sock")
	listener, err := ListenControl(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeControl(ctx, listener, func(*net.UnixConn) (PeerIdentity, error) {
			return PeerIdentity{PrincipalDigest: testDigest("peer"), Role: PrincipalVerifierController}, nil
		}, func(_ context.Context, request ControlRequest) (ControlResponse, error) {
			if request.Peer.Role != PrincipalVerifierController || string(request.Payload) != "request" || len(request.FDs) != 1 {
				return ControlResponse{}, errors.New("unexpected request")
			}
			return ControlResponse{Payload: []byte("response")}, nil
		})
	}()
	client, err := NewControlClient(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tempDir, "input")
	if err := os.WriteFile(path, []byte("input"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	requestCtx, requestCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer requestCancel()
	response, err := client.RoundTrip(requestCtx, []byte("request"), file)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Close()
	if string(response.Payload) != "response" || len(response.FDs) != 0 {
		t.Fatalf("response = %+v", response)
	}
	cancel()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("APAP server did not stop")
	}
}
