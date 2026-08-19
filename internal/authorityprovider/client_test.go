package authorityprovider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNewControlClientRejectsMutableEndpointShapes(t *testing.T) {
	for _, endpoint := range []string{"", "relative.sock", "/", "/tmp/authority/../socket"} {
		if _, err := NewControlClient(endpoint); err == nil {
			t.Fatalf("endpoint %q was accepted", endpoint)
		}
	}
}

func TestControlClientRoundTripAbsentServiceFailsClosed(t *testing.T) {
	endpoint := filepath.Join(t.TempDir(), "authority.sock")
	client, err := NewControlClient(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.RoundTrip(ctx, []byte("sealed"), file); err == nil {
		t.Fatal("APAP round trip succeeded without an authority service")
	}
}
