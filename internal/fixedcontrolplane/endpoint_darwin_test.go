//go:build darwin && arm64

package fixedcontrolplane

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/processsupervisor"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
	"github.com/chiga0/marshal-harness/internal/resultingress"
)

type endpointFixture struct {
	session   *productionruntime.RepositorySession
	authority *productionruntime.FixedEndpointAuthority
}

func newEndpointFixture(t *testing.T) endpointFixture {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "marshal-fixed-endpoint-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove fixture: %v", err)
		}
	})
	openDirectory := func(name string) *os.File {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return file
	}
	repositoryPath := filepath.Join(root, "repository")
	if err := os.Mkdir(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := productionruntime.OpenCanonicalRepositoryRoot(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	fixed, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixed, err = filepath.EvalSymlinks(fixed)
	if err != nil {
		t.Fatal(err)
	}
	core, err := processsupervisor.ObserveCurrentCore(fixed)
	if err != nil {
		t.Fatalf("observe current fixed binary: %v", err)
	}
	acquisition := resultingress.ControlOwnerAcquisition{
		Scope: resultingress.ControlOwnerScope{
			AuthorityNamespaceID:     authority.AuthorityNamespaceId{TenantNamespace: "test", ControlPlaneId: "fixed-server", AuthorityScopeId: "repository"},
			RepositoryIdentityDigest: canonical.DigestBytes([]byte(repositoryPath)),
		},
		OwnerUID: core.UID, OwnerGID: core.GID, OwnerProcess: core.Process, OwnerBinary: core.Binary,
		ObserverIdentity: "fixed-endpoint-test/v1", ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	session, err := productionruntime.OpenRepositorySession(context.Background(), productionruntime.RepositorySessionInputs{
		HeldIngressDir: openDirectory("ingress"), HeldRepositoryRoot: repository, OwnerDirectory: openDirectory("owner"),
		Acquisition: acquisition, FixedMarshalPath: fixed, OwnerPrivateControlRoot: openDirectory("private-control"),
	})
	if err != nil {
		t.Fatalf("open repository session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	endpointAuthority, err := session.OpenFixedEndpointAuthority(context.Background())
	if err != nil {
		t.Fatalf("open endpoint authority: %v", err)
	}
	t.Cleanup(func() { _ = endpointAuthority.Close() })
	return endpointFixture{session: session, authority: endpointAuthority}
}

func testBinding() RequestBinding {
	digest := func(value string) string { return canonical.DigestBytes([]byte(value)) }
	return RequestBinding{
		RequestKeyDigest: digest("request-key"), RequestDigest: digest("request"), IntentDigest: digest("intent"),
		Deadline: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
	}
}

func TestEndpointAuthenticatesAndCarriesBoundApplicationBytes(t *testing.T) {
	fixture := newEndpointFixture(t)
	endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
	if err != nil {
		t.Fatalf("open endpoint: %v", err)
	}
	binding := testBinding()
	accepted := make(chan *AuthenticatedConnection, 1)
	errorsSeen := make(chan error, 1)
	go func() {
		connection, acceptErr := endpoint.Accept(context.Background())
		if acceptErr != nil {
			errorsSeen <- acceptErr
			return
		}
		accepted <- connection
	}()
	client, err := Dial(context.Background(), fixture.authority, binding)
	if err != nil {
		t.Fatalf("dial endpoint: %v", err)
	}
	var server *AuthenticatedConnection
	select {
	case server = <-accepted:
	case err := <-errorsSeen:
		t.Fatalf("accept endpoint: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("accept timeout")
	}
	if server.Binding != binding {
		t.Fatalf("binding=%+v want=%+v", server.Binding, binding)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	var payload [4]byte
	if _, err := io.ReadFull(server, payload[:]); err != nil || string(payload[:]) != "ping" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	_ = client.Close()
	_ = server.Close()
	snapshot := fixture.authority.Snapshot()
	if err := endpoint.Close(); err != nil {
		t.Fatalf("close endpoint: %v", err)
	}
	epoch := snapshot.Acquisition.OwnerEpoch
	for _, name := range []string{"s-" + base36(epoch), "t-" + base36(epoch)} {
		if _, err := os.Lstat(filepath.Join(snapshot.ControlPath, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("endpoint leaf %q still exists: %v", name, err)
		}
	}
	if err := fixture.authority.Recheck(context.Background()); err != nil {
		t.Fatalf("authority after exact close: %v", err)
	}
}

func TestEndpointRejectsOversizedHandshakeAndRemainsAvailable(t *testing.T) {
	fixture := newEndpointFixture(t)
	endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })
	accepted := make(chan error, 1)
	go func() {
		_, acceptErr := endpoint.Accept(context.Background())
		accepted <- acceptErr
	}()
	raw, err := net.Dial("unix", endpoint.locator)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := readFrame(raw); err != nil {
		t.Fatal(err)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], maxHandshakeFrame+1)
	if _, err := raw.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-accepted:
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("accept err=%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reject timeout")
	}
}

func TestEndpointRejectsForgedAndReplayedProofs(t *testing.T) {
	fixture := newEndpointFixture(t)
	endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = endpoint.Close() })

	acceptOnce := func() <-chan error {
		result := make(chan error, 1)
		go func() {
			connection, acceptErr := endpoint.Accept(context.Background())
			if connection != nil {
				_ = connection.Close()
			}
			result <- acceptErr
		}()
		return result
	}
	await := func(result <-chan error) error {
		select {
		case err := <-result:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("accept timeout")
			return nil
		}
	}
	dialRaw := func() *net.UnixConn {
		raw, dialErr := net.Dial("unix", endpoint.locator)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		connection, ok := raw.(*net.UnixConn)
		if !ok {
			t.Fatal("not a Unix connection")
		}
		return connection
	}
	buildProof := func(connection *net.UnixConn) ([]byte, challengeFrame) {
		challengeRaw, readErr := readFrame(connection)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var challenge challengeFrame
		if decodeClosed(challengeRaw, &challenge) != nil {
			t.Fatal("invalid server challenge")
		}
		self, observeErr := processsupervisor.ObserveCurrentCore(fixture.authority.Snapshot().FixedMarshalPath)
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		clientDigest, digestErr := identityDigest(self)
		if digestErr != nil {
			t.Fatal(digestErr)
		}
		binding := testBinding()
		proof, proofErr := proofDigest(endpoint.token[:], challenge, clientDigest, binding)
		if proofErr != nil {
			t.Fatal(proofErr)
		}
		raw, marshalErr := canonicalBytes(proofFrame{SchemaVersion: "fixed-control-proof/v1", ProtocolRevision: ProtocolRevision, ChallengeDigest: canonical.DigestBytes(challengeRaw), ClientIdentityDigest: clientDigest, Binding: binding, Proof: proof})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return raw, challenge
	}

	forgedResult := acceptOnce()
	forged := dialRaw()
	proofRaw, _ := buildProof(forged)
	var proof proofFrame
	if decodeClosed(proofRaw, &proof) != nil {
		t.Fatal("invalid generated proof")
	}
	proof.Proof = "0000000000000000000000000000000000000000000000000000000000000000"
	proofRaw, err = canonicalBytes(proof)
	if err != nil || writeFrame(forged, proofRaw) != nil {
		t.Fatalf("write forged proof: %v", err)
	}
	_ = forged.Close()
	if err := await(forgedResult); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged proof err=%v", err)
	}

	firstResult := acceptOnce()
	first := dialRaw()
	proofRaw, _ = buildProof(first)
	if err := writeFrame(first, proofRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := readFrame(first); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	if err := await(firstResult); err != nil {
		t.Fatalf("valid proof err=%v", err)
	}

	replayResult := acceptOnce()
	replay := dialRaw()
	if _, err := readFrame(replay); err != nil {
		t.Fatal(err)
	}
	if err := writeFrame(replay, proofRaw); err != nil {
		t.Fatal(err)
	}
	_ = replay.Close()
	if err := await(replayResult); !errors.Is(err, ErrConflict) {
		t.Fatalf("replayed proof err=%v", err)
	}
}

func TestEndpointRejectsTokenABA(t *testing.T) {
	fixture := newEndpointFixture(t)
	endpoint, err := OpenEndpoint(context.Background(), fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := fixture.authority.Snapshot()
	tokenPath := filepath.Join(snapshot.ControlPath, endpoint.tokenName)
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := endpoint.recheck(context.Background()); err == nil {
		t.Fatal("token ABA unexpectedly admitted")
	}
	if err := endpoint.Close(); err == nil {
		t.Fatal("close unexpectedly removed a replaced token")
	}
}

func TestWriteFrameCompletesShortWrites(t *testing.T) {
	writer := &oneByteWriter{}
	if err := writeFrame(writer, []byte("abc")); err != nil {
		t.Fatal(err)
	}
	if len(writer.raw) != 7 || binary.BigEndian.Uint32(writer.raw[:4]) != 3 || string(writer.raw[4:]) != "abc" {
		t.Fatalf("raw=%v", writer.raw)
	}
}

type oneByteWriter struct{ raw []byte }

func (writer *oneByteWriter) Write(raw []byte) (int, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	writer.raw = append(writer.raw, raw[0])
	return 1, nil
}

func base36(value uint64) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if value == 0 {
		return "0"
	}
	var raw [64]byte
	index := len(raw)
	for value > 0 {
		index--
		raw[index] = digits[value%36]
		value /= 36
	}
	return string(raw[index:])
}
