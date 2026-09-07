//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

// This holds the actual repository owner lock, without changing authority or
// injecting a fake recheck. The client must still complete the real handshake.
func handshakeContention(t *testing.T) (*productionruntime.FixedEndpointAuthority, func(), <-chan error) {
	t.Helper()
	fixture := newEndpointFixture(t)
	endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	client, err := productionruntime.OpenFixedEndpointClientAuthority(context.Background(), fixture.repository)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- fixture.authority.WithControlMutation(context.Background(), func(*os.File) error {
			close(entered)
			<-release
			return nil
		})
	}()
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unlock()
		if err := <-done; err != nil {
			t.Errorf("owner lock fixture: %v", err)
		}
	})
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("owner lock not acquired")
	}
	served := make(chan error, 1)
	go func() {
		connection, err := endpoint.Accept(context.Background())
		if connection != nil {
			_ = connection.Close()
		}
		served <- err
	}()
	t.Cleanup(func() {
		unlock()
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Error("handshake did not drain")
		}
	})
	return client, unlock, served
}

func TestResponsePhaseHandshakeWaitsForCurrentOwnerWithoutExpiringProof(t *testing.T) {
	client, unlock, _ := handshakeContention(t)
	timer := time.AfterFunc(handshakeTimeout+time.Second, unlock)
	defer timer.Stop()
	connection, err := Dial(context.Background(), client, testBinding())
	if err != nil {
		t.Fatalf("local owner wait consumed proof budget: %v", err)
	}
	_ = connection.Close()
}

func TestResponsePhaseHandshakeOriginalDeadlineAndCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		t.Run(map[bool]string{false: "original-deadline", true: "parent-cancel"}[cancelParent], func(t *testing.T) {
			client, unlock, _ := handshakeContention(t)
			defer unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			binding := testBinding()
			if cancelParent {
				timer := time.AfterFunc(150*time.Millisecond, cancel)
				defer timer.Stop()
			} else {
				binding.Deadline = time.Now().UTC().Add(150 * time.Millisecond).Format(time.RFC3339Nano)
			}
			started := time.Now()
			connection, err := Dial(ctx, client, binding)
			if connection != nil {
				_ = connection.Close()
			}
			if err == nil || time.Since(started) > 2*time.Second {
				t.Fatalf("cancel/deadline did not bound handshake: %v", err)
			}
		})
	}
}

func TestResponsePhaseAuthorityFrameDeadlineBoundsMissingAndPartialFrame(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "partial"}[partial], func(t *testing.T) {
			server, client := unixConnectionPair(t, testBinding())
			if partial {
				if _, err := server.Write([]byte{0}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			started := time.Now()
			_, err := readAuthorityFrame(ctx, client)
			if err == nil || time.Since(started) > 2*time.Second {
				t.Fatalf("frame read extended original deadline: %v", err)
			}
		})
	}
}

func TestResponsePhaseFreshProofCanAwaitCurrentOwnerRecheck(t *testing.T) {
	fixture := newEndpointFixture(t)
	endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	served := make(chan error, 1)
	go func() {
		connection, err := endpoint.Accept(context.Background())
		if connection != nil {
			_ = connection.Close()
		}
		served <- err
	}()
	raw, err := net.Dial("unix", endpoint.locator)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	client := raw.(*net.UnixConn)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	challengeRaw, err := readAuthorityFrame(ctx, client)
	if err != nil {
		t.Fatal(err)
	}
	var challenge challengeFrame
	if decodeClosed(challengeRaw, &challenge) != nil {
		t.Fatal("invalid challenge")
	}
	// Server and test client are this same observed executable/process.
	clientDigest, err := identityDigest(endpoint.server)
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding()
	proof, err := proofDigest(endpoint.token[:], challenge, clientDigest, binding)
	if err != nil {
		t.Fatal(err)
	}
	proofRaw, err := canonicalBytes(proofFrame{SchemaVersion: "fixed-control-proof/v1", ProtocolRevision: ProtocolRevision, ChallengeDigest: canonical.DigestBytes(challengeRaw), ClientIdentityDigest: clientDigest, Binding: binding, Proof: proof})
	if err != nil {
		t.Fatal(err)
	}
	// Acquiring after the challenge forces contention at the SECOND recheck.
	err = fixture.authority.WithControlMutation(ctx, func(*os.File) error {
		if err := writeFrame(client, proofRaw); err != nil {
			return err
		}
		select {
		case <-time.After(handshakeTimeout + time.Second):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	acceptedRaw, err := readAuthorityFrame(ctx, client)
	var accepted acceptedFrame
	if err != nil || decodeClosed(acceptedRaw, &accepted) != nil || accepted.ChallengeDigest != canonical.DigestBytes(challengeRaw) || accepted.ProofDigest != canonical.DigestBytes(proofRaw) {
		t.Fatalf("fresh consumed proof lost during owner recheck: %v", err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("server handshake did not finish")
	}
}
