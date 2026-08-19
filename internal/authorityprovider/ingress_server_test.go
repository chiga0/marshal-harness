package authorityprovider

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCredentialIngressDeliversOnlyOneCapabilityFDAndReceiptReference(t *testing.T) {
	tempDir, err := os.MkdirTemp("/tmp", "ingress-")
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
	peer := PeerIdentity{PrincipalDigest: stableDigest("secret-provider"), Role: PrincipalSecretProvider}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- ServeCredentialIngress(ctx, listener, func(*net.UnixConn) (PeerIdentity, error) {
			return peer, nil
		}, func(_ context.Context, request CredentialIngressRequest) (CredentialIngressResponseV1, error) {
			if request.Peer != peer || request.Capability == nil || request.Envelope.AuthorityProfile != ProfileQoder {
				return CredentialIngressResponseV1{}, errors.New("invalid credential ingress request")
			}
			return CredentialIngressResponseV1{
				SchemaVersion: IngressResponseSchema, ProtocolFamily: IngressFamily, ProtocolVersion: ProtocolVersion,
				Audience: IngressAudience, RequestID: request.Envelope.RequestID, CommandID: request.Envelope.CommandID,
				ProviderInstanceID: request.Envelope.ProviderInstanceID, AuthorityProfile: request.Envelope.AuthorityProfile,
				SafeCode: CodeOK, SafeMessage: SafeMessageFor(CodeOK),
				Payload: mustJSON(CredentialIngressSuccessPayload{DeliveryReceiptDigest: stableDigest("delivery"), InstallReceiptDigest: stableDigest("install")}),
			}, nil
		})
	}()

	now := time.Now().UTC()
	capPath := filepath.Join(tempDir, "capability")
	if err := os.WriteFile(capPath, []byte("opaque-capability"), 0o600); err != nil {
		t.Fatal(err)
	}
	capability, err := os.Open(capPath)
	if err != nil {
		t.Fatal(err)
	}
	defer capability.Close()
	request := CredentialIngressRequestV1{
		SchemaVersion: IngressRequestSchema, ProtocolFamily: IngressFamily, ProtocolVersion: ProtocolVersion,
		Audience: IngressAudience, RequestID: "request-1", CommandID: "command-1",
		SecretProviderPrincipalDigest: peer.PrincipalDigest, ProviderInstanceID: "provider-1", AuthorityProfile: ProfileQoder,
		ProbeSessionID: "probe-1", TargetIsolationIdentityDigest: stableDigest("target"),
		CredentialIngressEndpointIdentityDigest: stableDigest("endpoint"), CredentialIngressTicketDigest: stableDigest("ticket"),
		IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(time.Minute), Nonce: "nonce-0001",
		Payload: mustJSON(AttachProbeCredentialPayload{
			ProbeSessionID: "probe-1", CapabilityIdentityDigest: stableDigest("capability"),
			CapabilityPolicyDigest: stableDigest("policy"), ServiceIdentityDigest: stableDigest("service"),
			CapabilityExpiresAt: now.Add(time.Minute), DeliveryNonce: "delivery-0001",
			TargetIsolationIdentityDigest: stableDigest("target"),
		}),
	}
	raw, err := SealCredentialIngressRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := (&net.Dialer{}).DialContext(context.Background(), controlNetwork, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		connection.Close()
		t.Fatal("credential ingress connection is not Unix")
	}
	defer unixConnection.Close()
	transport := controlConnection{conn: unixConnection, stream: controlStream}
	if err := transport.write(raw, unix.UnixRights(int(capability.Fd()))); err != nil {
		t.Fatal(err)
	}
	responseRaw, _, _, err := transport.read()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCredentialIngressResponse(responseRaw, request); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("credential ingress server did not stop")
	}
}
