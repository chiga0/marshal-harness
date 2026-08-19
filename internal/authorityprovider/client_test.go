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

func TestDecodeSignedResponseEnvelopeRejectsNonCanonicalOrUnknownShape(t *testing.T) {
	valid := []byte(`{"document":{"response":"ok"},"signature":{"objectDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","signature":"sig"}}`)
	if _, err := DecodeSignedResponseEnvelope(valid); err != nil {
		t.Fatalf("valid signed response rejected: %v", err)
	}
	for name, raw := range map[string][]byte{
		"unknown member":        []byte(`{"document":{"response":"ok"},"signature":{"objectDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","signature":"sig"},"extra":true}`),
		"noncanonical document": []byte(`{"document":{"response":"ok","a":1},"signature":{"objectDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","signature":"sig"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeSignedResponseEnvelope(raw); err == nil {
				t.Fatal("malformed signed response was accepted")
			}
		})
	}
}
