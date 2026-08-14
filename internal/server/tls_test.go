package server

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/provider"
)

// testPKI is a hermetic certificate authority with one server certificate
// and one client certificate for the transport baseline fixtures.
type testPKI struct {
	caCert        *x509.Certificate
	serverCertPEM []byte
	serverKeyPEM  []byte
	clientCertPEM []byte
	clientKeyPEM  []byte
	clientKey     *ecdsa.PrivateKey
	caPEM         []byte
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	notBefore := time.Now().Add(-time.Hour)
	notAfter := time.Now().Add(24 * time.Hour)

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fixture-ca"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "fixture-server"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:     []string{"localhost"},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}
	serverKeyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		t.Fatalf("marshal server key: %v", err)
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "fixture-provider"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create client certificate: %v", err)
	}
	clientKeyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}

	return &testPKI{
		caCert:        caCert,
		serverCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
		serverKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: serverKeyDER}),
		clientCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}),
		clientKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: clientKeyDER}),
		clientKey:     clientKey,
		caPEM:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
	}
}

// writeBaseline writes the baseline PEM files and returns the configuration.
func (p *testPKI) writeBaseline(t *testing.T) TLSBaseline {
	t.Helper()
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	caFile := filepath.Join(dir, "client-ca.pem")
	for path, data := range map[string][]byte{certFile: p.serverCertPEM, keyFile: p.serverKeyPEM, caFile: p.caPEM} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return TLSBaseline{CertFile: certFile, KeyFile: keyFile, ClientCAFile: caFile}
}

// clientTLSConfig trusts the fixture CA and presents the client certificate.
func (p *testPKI) clientTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(p.caPEM) {
		t.Fatalf("load the fixture CA pool")
	}
	pair, err := tls.X509KeyPair(p.clientCertPEM, p.clientKeyPEM)
	if err != nil {
		t.Fatalf("load the client key pair: %v", err)
	}
	return &tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{pair},
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS12,
	}
}

func (p *testPKI) trustPool(t *testing.T) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(p.caPEM) {
		t.Fatalf("load the fixture CA pool")
	}
	return pool
}

func TestClassifyListenDecisions(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:7718", "[::1]:7718"} {
		loopback, err := ClassifyListen(listen)
		if err != nil || !loopback {
			t.Fatalf("ClassifyListen(%q) = %v, %v; want true, nil", listen, loopback, err)
		}
	}
	for _, listen := range []string{"192.168.1.10:7718", "0.0.0.0:7718", "[::]:7718"} {
		loopback, err := ClassifyListen(listen)
		if err != nil || loopback {
			t.Fatalf("ClassifyListen(%q) = %v, %v; want false, nil", listen, loopback, err)
		}
	}
	for _, listen := range []string{"localhost:7718", ":7718", "no-port"} {
		_, err := ClassifyListen(listen)
		if err == nil {
			t.Fatalf("ClassifyListen(%q) accepted an unusable listen address", listen)
		}
		if !strings.Contains(err.Error(), "loopback") {
			t.Fatalf("ClassifyListen(%q) error lacks the loopback anchor: %v", listen, err)
		}
	}
}

func TestTLSBaselineFailsClosed(t *testing.T) {
	pki := newTestPKI(t)
	complete := pki.writeBaseline(t)

	for name, baseline := range map[string]TLSBaseline{
		"empty":           {},
		"missing-cert":    {KeyFile: complete.KeyFile, ClientCAFile: complete.ClientCAFile},
		"missing-key":     {CertFile: complete.CertFile, ClientCAFile: complete.ClientCAFile},
		"missing-client":  {CertFile: complete.CertFile, KeyFile: complete.KeyFile},
		"absent-certfile": {CertFile: filepath.Join(t.TempDir(), "absent.crt"), KeyFile: complete.KeyFile, ClientCAFile: complete.ClientCAFile},
	} {
		if _, err := baseline.Config(); err == nil {
			t.Fatalf("baseline %s: an incomplete TLS baseline produced a configuration", name)
		}
		if _, err := NewRemoteListener("127.0.0.1:0", nil); err == nil {
			t.Fatalf("NewRemoteListener accepted a nil TLS configuration")
		}
	}

	// A client CA without a usable certificate fails closed.
	garbageCA := filepath.Join(t.TempDir(), "garbage-ca.pem")
	if err := os.WriteFile(garbageCA, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	garbage := TLSBaseline{CertFile: complete.CertFile, KeyFile: complete.KeyFile, ClientCAFile: garbageCA}
	if _, err := garbage.Config(); err == nil {
		t.Fatalf("a garbage client CA produced a configuration")
	}

	// A one-way TLS configuration never starts a remote listener.
	oneWay := &tls.Config{}
	if _, err := NewRemoteListener("127.0.0.1:0", oneWay); err == nil {
		t.Fatalf("NewRemoteListener accepted a configuration without mutual identity validation")
	}

	// The complete baseline constructs a mutual-TLS listener.
	config, err := complete.Config()
	if err != nil {
		t.Fatalf("the complete baseline failed: %v", err)
	}
	if config.ClientAuth != tls.RequireAndVerifyClientCert || config.ClientCAs == nil {
		t.Fatalf("the baseline configuration does not enforce mutual TLS: %+v", config)
	}
	if config.MinVersion < tls.VersionTLS12 {
		t.Fatalf("the baseline configuration allows TLS below 1.2")
	}
	listener, err := NewRemoteListener("127.0.0.1:0", config)
	if err != nil {
		t.Fatalf("NewRemoteListener rejected the complete baseline: %v", err)
	}
	_ = listener.Close()
}

// startRemoteFixtureServer starts one TLS listener protected by the remote
// baseline over the fixture PKI and returns its base URL, the signing client
// and the replay guard clock controls.
func startRemoteFixtureServer(t *testing.T, pki *testPKI, fixedNow time.Time) (baseURL string, guard *ReplayGuard, seenIdentity *string) {
	t.Helper()
	config, err := pki.writeBaseline(t).Config()
	if err != nil {
		t.Fatalf("load the TLS baseline: %v", err)
	}
	listener, err := NewRemoteListener("127.0.0.1:0", config)
	if err != nil {
		t.Fatalf("bind the remote listener: %v", err)
	}
	guard = NewReplayGuard(DefaultReplayWindow, func() time.Time { return fixedNow })
	identity := ""
	inner := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if client, ok := ClientIdentityFromContext(request.Context()); ok {
			identity = client
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	})
	httpServer := &http.Server{Handler: ProtectRemote(inner, guard)}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })
	return "https://" + listener.Addr().String(), guard, &identity
}

// TestRemoteTransportBaselineEnforced drives the complete remote baseline: a
// signed request with a fresh nonce inside the window succeeds and binds the
// client identity; a replayed nonce, a tampered body, out-of-window
// timestamps and incomplete replay headers all fail closed.
func TestRemoteTransportBaselineEnforced(t *testing.T) {
	pki := newTestPKI(t)
	fixedNow := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	baseURL, _, seenIdentity := startRemoteFixtureServer(t, pki, fixedNow)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: pki.clientTLSConfig(t)},
		Timeout:   30 * time.Second,
	}
	path := APIPrefix + "/registrations"
	body := []byte(`{"registration":{"providerName":"fixture-sandbox"}}`)

	doSigned := func(nonce string, ts time.Time, signBody, sendBody []byte) recordedResponse {
		t.Helper()
		signature, err := SignRequest(pki.clientKey, http.MethodPost, path, ts.Format(time.RFC3339), nonce, signBody)
		if err != nil {
			t.Fatalf("sign the request: %v", err)
		}
		request, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(sendBody))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(HeaderNonce, nonce)
		request.Header.Set(HeaderTimestamp, ts.Format(time.RFC3339))
		request.Header.Set(HeaderSignature, signature)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read the response body: %v", err)
		}
		return recordedResponse{status: response.StatusCode, body: data}
	}

	// Happy path: signed request, fresh nonce, timestamp inside the window.
	response := doSigned("nonce-happy", fixedNow, body, body)
	if response.status != http.StatusOK {
		t.Fatalf("happy path status = %d, body: %s", response.status, response.body)
	}
	if *seenIdentity != "fixture-provider" {
		t.Fatalf("the protected handler saw client identity %q, want fixture-provider", *seenIdentity)
	}

	// The identical nonce replays fail closed inside the window.
	response = doSigned("nonce-happy", fixedNow, body, body)
	if response.status != http.StatusForbidden {
		t.Fatalf("replayed nonce status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeForbiddenIdentity || errBody.Reason != "replayed-nonce" {
		t.Fatalf("replayed nonce error = %+v", errBody)
	}

	// A body substituted after signing breaks the signature.
	response = doSigned("nonce-tampered", fixedNow, body, []byte(`{"registration":{"providerName":"evil"}}`))
	if response.status != http.StatusForbidden {
		t.Fatalf("tampered body status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeForbiddenIdentity || errBody.Reason != "signature-invalid" {
		t.Fatalf("tampered body error = %+v", errBody)
	}

	// Out-of-window timestamps fail closed in both directions.
	response = doSigned("nonce-past", fixedNow.Add(-2*DefaultReplayWindow), body, body)
	if response.status != http.StatusForbidden {
		t.Fatalf("past timestamp status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeForbiddenIdentity || errBody.Reason != "timestamp-outside-window" {
		t.Fatalf("past timestamp error = %+v", errBody)
	}
	response = doSigned("nonce-future", fixedNow.Add(2*DefaultReplayWindow), body, body)
	if response.status != http.StatusForbidden {
		t.Fatalf("future timestamp status = %d, body: %s", response.status, response.body)
	}
	if errBody := response.decodeError(t); errBody.Code != CodeForbiddenIdentity || errBody.Reason != "timestamp-outside-window" {
		t.Fatalf("future timestamp error = %+v", errBody)
	}

	// Incomplete replay protection headers fail closed before any signature
	// is inspected.
	request, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(HeaderNonce, "nonce-incomplete")
	response2, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST incomplete headers: %v", err)
	}
	data, err := io.ReadAll(response2.Body)
	_ = response2.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	incomplete := recordedResponse{status: response2.StatusCode, body: data}
	if incomplete.status != http.StatusForbidden {
		t.Fatalf("incomplete headers status = %d, body: %s", incomplete.status, incomplete.body)
	}
	if errBody := incomplete.decodeError(t); errBody.Code != CodeForbiddenIdentity || errBody.Reason != "replay-protection-incomplete" {
		t.Fatalf("incomplete headers error = %+v", errBody)
	}
}

// TestRemoteTransportRejectsUnauthenticatedClients proves the mutual
// identity validation failure rejection: a client without a certificate
// never completes the handshake, and a plaintext client receives no HTTP
// answer from the TLS-only port.
func TestRemoteTransportRejectsUnauthenticatedClients(t *testing.T) {
	pki := newTestPKI(t)
	fixedNow := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	baseURL, _, _ := startRemoteFixtureServer(t, pki, fixedNow)

	noCertClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    pki.trustPool(t),
			ServerName: "127.0.0.1",
			MinVersion: tls.VersionTLS12,
		}},
		Timeout: 30 * time.Second,
	}
	if _, err := noCertClient.Get(baseURL + "/"); err == nil {
		t.Fatalf("a client without a certificate completed the mutual TLS handshake")
	}

	plainClient := &http.Client{Timeout: 30 * time.Second}
	if _, err := plainClient.Get("http://" + strings.TrimPrefix(baseURL, "https://") + "/"); err == nil {
		t.Fatalf("a plaintext client received a response from the TLS-only port")
	}
}

// TestProtectRemoteFailsClosedWithoutTLS proves the wrapper never degrades
// to a plain path: an in-process request without a TLS handshake state is
// rejected fail closed.
func TestProtectRemoteFailsClosedWithoutTLS(t *testing.T) {
	protected := ProtectRemote(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}), nil)
	recorder := httptest.NewRecorder()
	protected.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1alpha1/registrations", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("plain request status = %d, body: %s", recorder.Code, recorder.Body.String())
	}
	response := recordedResponse{status: recorder.Code, body: recorder.Body.Bytes()}
	if errBody := response.decodeError(t); errBody.Code != CodeForbiddenIdentity || errBody.Reason != "missing-client-identity" {
		t.Fatalf("plain request error = %+v", errBody)
	}
}

// TestReplayGuardWindowExpiry proves the nonce ledger semantics: reused
// nonces inside the window fail closed, out-of-window timestamps fail
// closed, expired entries are pruned so a nonce may be reused after the
// window, and distinct client identities keep disjoint nonce spaces.
func TestReplayGuardWindowExpiry(t *testing.T) {
	start := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	clock := start
	guard := NewReplayGuard(time.Minute, func() time.Time { return clock })

	if err := guard.Check("client-a", "nonce-1", start); err != nil {
		t.Fatalf("the first admission failed: %v", err)
	}
	if err := guard.Check("client-b", "nonce-1", start); err != nil {
		t.Fatalf("distinct identities must keep disjoint nonce spaces: %v", err)
	}
	if err := guard.Check("client-a", "nonce-1", start); !errors.Is(err, errReplayNonce) {
		t.Fatalf("the replayed nonce returned %v, want errReplayNonce", err)
	}
	if err := guard.Check("client-a", "nonce-2", start.Add(-2*time.Minute)); !errors.Is(err, errReplayWindow) {
		t.Fatalf("the out-of-window timestamp returned %v, want errReplayWindow", err)
	}

	// After the window elapses the consumed nonce is pruned and reusable,
	// while the timestamp must stay inside the window of the current clock.
	clock = start.Add(2 * time.Minute)
	if err := guard.Check("client-a", "nonce-1", clock); err != nil {
		t.Fatalf("the pruned nonce must be reusable after the window: %v", err)
	}
	if err := guard.Check("client-a", "nonce-3", start); !errors.Is(err, errReplayWindow) {
		t.Fatalf("an old timestamp after the clock advanced returned %v, want errReplayWindow", err)
	}
}

// TestSignVerifyRequestSignature covers the client/server signature halves
// across the supported key types and the fail-closed negatives.
func TestSignVerifyRequestSignature(t *testing.T) {
	body := []byte(`{"registration":{"providerName":"fixture-sandbox"}}`)
	const (
		method    = http.MethodPost
		path      = "/v1alpha1/registrations"
		timestamp = "2026-08-13T12:00:00Z"
		nonce     = "nonce-roundtrip"
	)

	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := SignRequest(ecdsaKey, method, path, timestamp, nonce, body)
	if err != nil {
		t.Fatalf("ECDSA sign: %v", err)
	}
	ecdsaCert := &x509.Certificate{PublicKey: &ecdsaKey.PublicKey}
	if err := VerifyRequestSignature(ecdsaCert, method, path, timestamp, nonce, body, signature); err != nil {
		t.Fatalf("ECDSA verify: %v", err)
	}
	if err := VerifyRequestSignature(ecdsaCert, method, path+"/other", timestamp, nonce, body, signature); err == nil {
		t.Fatalf("ECDSA verify accepted a substituted path")
	}
	if err := VerifyRequestSignature(ecdsaCert, method, path, timestamp, "other-nonce", body, signature); err == nil {
		t.Fatalf("ECDSA verify accepted a substituted nonce")
	}
	if err := VerifyRequestSignature(ecdsaCert, method, path, timestamp, nonce, []byte(`{"evil":true}`), signature); err == nil {
		t.Fatalf("ECDSA verify accepted a substituted body")
	}
	if err := VerifyRequestSignature(ecdsaCert, method, path, timestamp, nonce, body, "!!!not-base64!!!"); err == nil {
		t.Fatalf("ECDSA verify accepted an invalid signature encoding")
	}

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaSignature, err := SignRequest(rsaKey, method, path, timestamp, nonce, body)
	if err != nil {
		t.Fatalf("RSA sign: %v", err)
	}
	rsaCert := &x509.Certificate{PublicKey: &rsaKey.PublicKey}
	if err := VerifyRequestSignature(rsaCert, method, path, timestamp, nonce, body, rsaSignature); err != nil {
		t.Fatalf("RSA verify: %v", err)
	}

	edPublic, edPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edSignature, err := SignRequest(edPrivate, method, path, timestamp, nonce, body)
	if err != nil {
		t.Fatalf("Ed25519 sign: %v", err)
	}
	edCert := &x509.Certificate{PublicKey: edPublic}
	if err := VerifyRequestSignature(edCert, method, path, timestamp, nonce, body, edSignature); err != nil {
		t.Fatalf("Ed25519 verify: %v", err)
	}

	if _, err := SignRequest("not-a-key", method, path, timestamp, nonce, body); err == nil {
		t.Fatalf("SignRequest accepted an unsupported private key type")
	}
	if err := VerifyRequestSignature(&x509.Certificate{PublicKey: "not-a-key"}, method, path, timestamp, nonce, body, signature); err == nil {
		t.Fatalf("VerifyRequestSignature accepted an unsupported certificate key type")
	}
	if err := VerifyRequestSignature(nil, method, path, timestamp, nonce, body, signature); err == nil {
		t.Fatalf("VerifyRequestSignature accepted a nil certificate")
	}
}

// TestNewTransportPolicyDecisions proves the transport assembly policy: a
// non-loopback listen never starts without the complete baseline, a partial
// baseline fails closed on every listen, the loopback path without TLS keeps
// its frozen plaintext behavior with the unwrapped handler, and a loopback
// listen with the complete baseline opts into the identical TLS-only surface
// where plaintext requests are rejected at the handshake stage.
func TestNewTransportPolicyDecisions(t *testing.T) {
	pki := newTestPKI(t)
	complete := pki.writeBaseline(t)
	inner := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"ok":true}`))
	})

	for _, listen := range []string{"0.0.0.0:0", "[::]:0", "192.168.1.10:0"} {
		if _, err := NewTransport(listen, TLSBaseline{}, inner, nil); err == nil {
			t.Fatalf("NewTransport started a non-loopback transport without a TLS baseline: %s", listen)
		}
	}
	partials := []TLSBaseline{
		{CertFile: complete.CertFile},
		{KeyFile: complete.KeyFile},
		{ClientCAFile: complete.ClientCAFile},
		{CertFile: complete.CertFile, KeyFile: complete.KeyFile},
	}
	for _, listen := range []string{"127.0.0.1:0", "0.0.0.0:0"} {
		for index, partial := range partials {
			if _, err := NewTransport(listen, partial, inner, nil); err == nil {
				t.Fatalf("NewTransport accepted the partial TLS baseline %d on %s", index, listen)
			}
		}
	}
	if _, err := NewTransport("localhost:0", TLSBaseline{}, inner, nil); err == nil {
		t.Fatal("NewTransport accepted a hostname listen address")
	}

	// The loopback path without a baseline keeps its frozen plaintext
	// behavior: the handler stays unwrapped and answers plain HTTP.
	plain, err := NewTransport("127.0.0.1:0", TLSBaseline{}, inner, nil)
	if err != nil {
		t.Fatalf("NewTransport rejected the loopback plaintext path: %v", err)
	}
	if plain.TLSEnabled || !plain.Loopback {
		t.Fatalf("loopback plaintext transport = %+v", plain)
	}
	plainServer := &http.Server{Handler: plain.Handler}
	go func() { _ = plainServer.Serve(plain.Listener) }()
	defer func() { _ = plainServer.Close() }()
	plainClient := &http.Client{Timeout: 30 * time.Second}
	response, err := plainClient.Get("http://" + plain.Listener.Addr().String())
	if err != nil {
		t.Fatalf("the loopback plaintext path regressed: %v", err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(data) != `{"ok":true}` {
		t.Fatalf("loopback plaintext response = %d %q", response.StatusCode, data)
	}

	// A loopback listen with the complete baseline opts into the TLS-only
	// surface: plaintext is rejected at the handshake stage, the mutual TLS
	// handshake completes, and the replay protection wrapper engages.
	optIn, err := NewTransport("127.0.0.1:0", complete, inner, nil)
	if err != nil {
		t.Fatalf("NewTransport rejected the loopback TLS opt-in: %v", err)
	}
	if !optIn.TLSEnabled || !optIn.Loopback {
		t.Fatalf("loopback TLS transport = %+v", optIn)
	}
	optInServer := &http.Server{Handler: optIn.Handler}
	go func() { _ = optInServer.Serve(optIn.Listener) }()
	defer func() { _ = optInServer.Close() }()
	if _, err := plainClient.Get("http://" + optIn.Listener.Addr().String()); err == nil {
		t.Fatal("the loopback TLS opt-in answered a plaintext request")
	}
	tlsClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: pki.clientTLSConfig(t)},
		Timeout:   30 * time.Second,
	}
	response, err = tlsClient.Get("https://" + optIn.Listener.Addr().String())
	if err != nil {
		t.Fatalf("the loopback TLS opt-in rejected a mutual TLS request at the transport layer: %v", err)
	}
	data, err = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("loopback TLS handshake-only response = %d %q", response.StatusCode, data)
	}
	recorded := recordedResponse{status: response.StatusCode, body: data}
	if errBody := recorded.decodeError(t); errBody.Code != CodeForbiddenIdentity || errBody.Reason != "replay-protection-incomplete" {
		t.Fatalf("loopback TLS wrapper error = %+v", errBody)
	}
}

// TestRemoteListenerRejectsPlaintextAtHandshake proves the TLS-only
// enforcement is a peeking listener: raw plaintext HTTP bytes sent into the
// remote listener never receive any response — the connection is closed at
// the first byte (not 0x16) directly, without a TLS alert or an HTTP
// answer.
func TestRemoteListenerRejectsPlaintextAtHandshake(t *testing.T) {
	pki := newTestPKI(t)
	config, err := pki.writeBaseline(t).Config()
	if err != nil {
		t.Fatalf("load the TLS baseline: %v", err)
	}
	listener, err := NewRemoteListener("127.0.0.1:0", config)
	if err != nil {
		t.Fatalf("bind the remote listener: %v", err)
	}
	httpServer := &http.Server{Handler: ProtectRemote(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}), nil)}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Close() }()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial the remote listener: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("GET /v1alpha1/registrations HTTP/1.1\r\nHost: fixture.invalid\r\n\r\n")); err != nil {
		t.Fatalf("write the plaintext request: %v", err)
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var received []byte
	buf := make([]byte, 4096)
	for {
		count, readErr := conn.Read(buf)
		if count > 0 {
			received = append(received, buf[:count]...)
		}
		if readErr != nil {
			if errors.Is(readErr, os.ErrDeadlineExceeded) {
				t.Fatal("the TLS-only listener kept the plaintext connection open without closing it")
			}
			break
		}
	}
	if len(received) != 0 {
		t.Fatalf("the peeking listener wrote a response to a plaintext connection, want direct close with zero bytes: %q", received)
	}
}

// TestPeekingListenerAdmitsOnlyTLSHandshakeRecords proves the first-byte
// decision of the peeking listener: a first byte other than 0x16 closes the
// connection directly without any response byte (even a lone probe byte or
// a slow partial write), while a first byte of 0x16 is replayed into the
// TLS handshake untouched.
func TestPeekingListenerAdmitsOnlyTLSHandshakeRecords(t *testing.T) {
	pki := newTestPKI(t)
	config, err := pki.writeBaseline(t).Config()
	if err != nil {
		t.Fatalf("load the TLS baseline: %v", err)
	}
	listener, err := NewRemoteListener("127.0.0.1:0", config)
	if err != nil {
		t.Fatalf("bind the remote listener: %v", err)
	}
	httpServer := &http.Server{Handler: http.NotFoundHandler()}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Close() }()

	expectSilentClose := func(name string, probe []byte) {
		t.Helper()
		conn, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatalf("%s: dial the remote listener: %v", name, err)
		}
		defer func() { _ = conn.Close() }()
		if _, err := conn.Write(probe); err != nil {
			t.Fatalf("%s: write the probe: %v", name, err)
		}
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var received []byte
		buf := make([]byte, 4096)
		for {
			count, readErr := conn.Read(buf)
			if count > 0 {
				received = append(received, buf[:count]...)
			}
			if readErr != nil {
				if errors.Is(readErr, os.ErrDeadlineExceeded) {
					t.Fatalf("%s: the peeking listener kept a non-handshake connection open", name)
				}
				break
			}
		}
		if len(received) != 0 {
			t.Fatalf("%s: the peeking listener wrote a response, want direct close with zero bytes: %x", name, received)
		}
	}
	expectSilentClose("lone non-handshake byte", []byte{0x47})
	expectSilentClose("alert record first byte", []byte{0x15, 0x03, 0x03, 0x00, 0x02, 0x01, 0x00})
	expectSilentClose("application data first byte", []byte{0x17, 0x03, 0x03, 0x00, 0x01, 0x00})
	expectSilentClose("plaintext http request", []byte("GET / HTTP/1.1\r\nHost: fixture.invalid\r\n\r\n"))

	// A first byte of 0x16 must be replayed into the TLS handshake
	// untouched: the TLS stack — not the silent close — answers a malformed
	// handshake record with a TLS alert (content type 0x15).
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial the remote listener: %v", err)
	}
	defer func() { _ = conn.Close() }()
	malformedHandshake := []byte{0x16, 0x03, 0x01, 0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
	if _, err := conn.Write(malformedHandshake); err != nil {
		t.Fatalf("write the malformed handshake record: %v", err)
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var received []byte
	buf := make([]byte, 4096)
	for {
		count, readErr := conn.Read(buf)
		if count > 0 {
			received = append(received, buf[:count]...)
		}
		if readErr != nil {
			if errors.Is(readErr, os.ErrDeadlineExceeded) {
				t.Fatal("the TLS stack did not answer the malformed handshake record")
			}
			break
		}
	}
	if len(received) == 0 {
		t.Fatal("the peeking listener silently closed a connection whose first byte was a TLS handshake record, instead of replaying it into the handshake")
	}
	if received[0] != 0x15 {
		t.Fatalf("the handshake-stage rejection of a malformed record sent unexpected bytes: %x", received)
	}
}

// TestRemoteListenerRejectsMutualAuthFailuresAtHandshake proves the mutual
// identity validation rejects unauthenticated clients at the handshake
// stage: a client without a certificate and a client carrying a certificate
// from a foreign CA never reach application data, while the fixture client
// certificate completes the handshake.
func TestRemoteListenerRejectsMutualAuthFailuresAtHandshake(t *testing.T) {
	pki := newTestPKI(t)
	config, err := pki.writeBaseline(t).Config()
	if err != nil {
		t.Fatalf("load the TLS baseline: %v", err)
	}
	listener, err := NewRemoteListener("127.0.0.1:0", config)
	if err != nil {
		t.Fatalf("bind the remote listener: %v", err)
	}
	httpServer := &http.Server{Handler: http.NotFoundHandler()}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Close() }()
	address := listener.Addr().String()

	mustRejectHandshake := func(name string, clientConfig *tls.Config) {
		t.Helper()
		conn, err := tls.Dial("tcp", address, clientConfig)
		if err != nil {
			return // rejected during the handshake itself
		}
		defer func() { _ = conn.Close() }()
		// A TLS 1.3 client may complete its side optimistically; the server
		// alert must then surface before any application data flows.
		if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatalf("%s: set the deadline: %v", name, err)
		}
		if _, err := conn.Write([]byte("GET / HTTP/1.0\r\n\r\n")); err != nil {
			return // the server alert aborted the connection
		}
		probe := make([]byte, 1)
		if _, err := conn.Read(probe); err == nil {
			t.Fatalf("%s: the server exchanged application data without a verified client identity", name)
		}
	}

	mustRejectHandshake("no client certificate", &tls.Config{
		RootCAs:    pki.trustPool(t),
		ServerName: "127.0.0.1",
		MinVersion: tls.VersionTLS12,
	})

	foreignPKI := newTestPKI(t)
	foreignPair, err := tls.X509KeyPair(foreignPKI.clientCertPEM, foreignPKI.clientKeyPEM)
	if err != nil {
		t.Fatalf("load the foreign client key pair: %v", err)
	}
	mustRejectHandshake("foreign CA client certificate", &tls.Config{
		RootCAs:      pki.trustPool(t),
		Certificates: []tls.Certificate{foreignPair},
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS12,
	})

	// Positive control: the fixture client certificate completes the
	// mutual TLS handshake.
	valid, err := tls.Dial("tcp", address, pki.clientTLSConfig(t))
	if err != nil {
		t.Fatalf("the fixture client certificate failed the mutual TLS handshake: %v", err)
	}
	_ = valid.Close()
}

// TestProtectRemoteNeverAnswersPlaintext proves the wrapper never degrades
// to a plain path even when it is reached without a TLS handshake state:
// over a real connection it tears the plaintext connection down without
// serving any HTTP response.
func TestProtectRemoteNeverAnswersPlaintext(t *testing.T) {
	protected := ProtectRemote(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}), nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind the plaintext listener: %v", err)
	}
	httpServer := &http.Server{Handler: protected}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Close() }()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial the listener: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("GET /v1alpha1/registrations HTTP/1.1\r\nHost: fixture.invalid\r\n\r\n")); err != nil {
		t.Fatalf("write the plaintext request: %v", err)
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var received []byte
	buf := make([]byte, 4096)
	for {
		count, readErr := conn.Read(buf)
		if count > 0 {
			received = append(received, buf[:count]...)
		}
		if readErr != nil {
			if errors.Is(readErr, os.ErrDeadlineExceeded) {
				t.Fatal("the protected handler kept the plaintext connection open without closing it")
			}
			break
		}
	}
	if len(received) > 0 {
		t.Fatalf("the protected handler answered a plaintext request: %q", received)
	}
}

// TestReplayGuardRejectsMalformedInputs proves the replay guard rejects
// malformed inputs fail closed: empty nonces, oversized nonces and absent
// timestamps never consume the nonce space.
func TestReplayGuardRejectsMalformedInputs(t *testing.T) {
	start := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	guard := NewReplayGuard(time.Minute, func() time.Time { return start })

	if err := guard.Check("client-a", "", start); err == nil {
		t.Fatal("the replay guard admitted an empty nonce")
	}
	if err := guard.Check("client-a", strings.Repeat("n", maxHeaderFieldBytes+1), start); err == nil {
		t.Fatal("the replay guard admitted an oversized nonce")
	}
	if err := guard.Check("client-a", "nonce-x", time.Time{}); err == nil {
		t.Fatal("the replay guard admitted an absent timestamp")
	}
	if err := guard.Check("client-a", "nonce-x", start); err != nil {
		t.Fatalf("a fresh nonce after rejected inputs failed: %v", err)
	}
}

// TestRemoteRegistrationEndToEndOverMutualTLS drives one complete remote
// registration through the entire ADR 0018 §12 stack: the mutual TLS
// handshake with the verified client identity, the request signature and
// nonce replay protection, the registration accept and the status query —
// and proves the replayed nonce fails closed while the public-api family
// never enters the registration Port even over TLS.
func TestRemoteRegistrationEndToEndOverMutualTLS(t *testing.T) {
	fixture := newRegistrationFixture(t)
	pki := newTestPKI(t)
	config, err := pki.writeBaseline(t).Config()
	if err != nil {
		t.Fatalf("load the TLS baseline: %v", err)
	}
	listener, err := NewRemoteListener("127.0.0.1:0", config)
	if err != nil {
		t.Fatalf("bind the remote listener: %v", err)
	}
	guard := NewReplayGuard(DefaultReplayWindow, func() time.Time { return fixtureClock })
	httpServer := &http.Server{Handler: ProtectRemote(fixture.port, guard)}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Close() }()
	baseURL := "https://" + listener.Addr().String()
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: pki.clientTLSConfig(t)},
		Timeout:   30 * time.Second,
	}

	registration := fixture.buildRegistration(registrationOptions{registrationID: "reg-mtls"})
	body := registrationBody(t, "mtls-key", registrationPayload(registration))
	submitPath := APIPrefix + "/registrations"

	doSigned := func(method, path, nonce string, requestBody []byte) recordedResponse {
		t.Helper()
		timestamp := fixtureClock.Format(time.RFC3339)
		signature, err := SignRequest(pki.clientKey, method, path, timestamp, nonce, requestBody)
		if err != nil {
			t.Fatalf("sign the request: %v", err)
		}
		headers := fixture.headers("req-mtls")
		if method == http.MethodPost {
			headers["Content-Type"] = "application/json"
		}
		headers[HeaderNonce] = nonce
		headers[HeaderTimestamp] = timestamp
		headers[HeaderSignature] = signature
		var reader io.Reader
		if requestBody != nil {
			reader = bytes.NewReader(requestBody)
		}
		request, err := http.NewRequest(method, baseURL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range headers {
			request.Header.Set(name, value)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatalf("read the response body: %v", err)
		}
		return recordedResponse{status: response.StatusCode, header: response.Header, body: data}
	}

	// Happy path: the signed submit with a fresh nonce over mutual TLS
	// admits the registration; the verified client certificate identity
	// matches the registration principal.
	response := doSigned(http.MethodPost, submitPath, "nonce-mtls-submit", body)
	if response.status != http.StatusCreated {
		t.Fatalf("remote submit status = %d, body: %s", response.status, response.body)
	}
	var accepted RegistrationAccepted
	if err := json.Unmarshal(response.body, &accepted); err != nil {
		t.Fatalf("decode RegistrationAccepted: %v", err)
	}
	if accepted.RegistrationId != "reg-mtls" || accepted.LifecycleState != provider.LifecycleStateCreate {
		t.Fatalf("remote RegistrationAccepted = %+v", accepted)
	}

	// The status query rides the identical baseline with its own nonce.
	statusPath := APIPrefix + "/registrations/reg-mtls"
	status := doSigned(http.MethodGet, statusPath, "nonce-mtls-status", nil)
	if status.status != http.StatusOK {
		t.Fatalf("remote status query status = %d, body: %s", status.status, status.body)
	}
	var view RegistrationStatus
	if err := json.Unmarshal(status.body, &view); err != nil {
		t.Fatalf("decode RegistrationStatus: %v", err)
	}
	if !reflect.DeepEqual(view.Registration, registration) {
		t.Fatalf("remote status projected a divergent record:\n got %+v\nwant %+v", view.Registration, registration)
	}

	// A replayed nonce fails closed even with an intact signature.
	replay := doSigned(http.MethodPost, submitPath, "nonce-mtls-submit", body)
	if replay.status != http.StatusForbidden {
		t.Fatalf("replayed nonce status = %d, body: %s", replay.status, replay.body)
	}
	if errBody := replay.decodeError(t); errBody.Code != CodeForbiddenIdentity || errBody.Reason != "replayed-nonce" {
		t.Fatalf("replayed nonce error = %+v", errBody)
	}

	// The public-api protocol family never enters the registration Port,
	// even over the remote baseline.
	timestamp := fixtureClock.Format(time.RFC3339)
	signature, err := SignRequest(pki.clientKey, http.MethodGet, statusPath, timestamp, "nonce-mtls-foreign", nil)
	if err != nil {
		t.Fatalf("sign the foreign family request: %v", err)
	}
	foreignHeaders := fixture.headers("req-mtls-foreign")
	foreignHeaders[HeaderProtocolVersion] = ProtocolFamily + "/" + ProtocolVersion
	foreignHeaders[HeaderNonce] = "nonce-mtls-foreign"
	foreignHeaders[HeaderTimestamp] = timestamp
	foreignHeaders[HeaderSignature] = signature
	request, err := http.NewRequest(http.MethodGet, baseURL+statusPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range foreignHeaders {
		request.Header.Set(name, value)
	}
	foreignResponse, err := client.Do(request)
	if err != nil {
		t.Fatalf("GET with the foreign protocol family: %v", err)
	}
	defer foreignResponse.Body.Close()
	data, err := io.ReadAll(foreignResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if foreignResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("foreign family status = %d, body: %s", foreignResponse.StatusCode, data)
	}
	foreign := recordedResponse{status: foreignResponse.StatusCode, body: data}
	if errBody := foreign.decodeError(t); errBody.Code != CodeInvalidRequest || errBody.Reason != "protocol-version-mismatch" {
		t.Fatalf("foreign family error = %+v", errBody)
	}
}
