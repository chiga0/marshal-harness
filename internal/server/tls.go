package server

// tls.go implements the remote transport security baseline frozen by ADR
// 0018 §12: every non-loopback/in-process transport is TLS with mutual
// identity validation and request-level replay protection from its first
// enable — the obligations are never deferred.
//
// - Listener construction fails closed while the TLS configuration is
//   incomplete (server certificate + key and the client CA are all
//   mandatory); a non-loopback listener without mutual TLS never starts.
// - ProtectRemote wraps the served handler with the per-call baseline: a
//   verified client certificate identity, a request signature bound to the
//   client certificate key (ECDSA/RSA-PSS/Ed25519 over the canonical
//   request binding) and a nonce + time-window replay guard. The wrapper
//   never degrades to a plain path: requests without a completed TLS
//   handshake fail closed.
// - Loopback/in-process transports stay untouched: they are served without
//   the wrapper and keep their existing behavior; a loopback listener may
//   explicitly opt into the identical TLS-only surface with the complete
//   baseline, where plaintext requests are rejected at the handshake stage
//   exactly like on any non-loopback transport.
// - NewTransport assembles one listener + handler surface under this
//   policy, so the wrapped handler is never paired with a plaintext
//   listener and the TLS-only surface is enforced at the very first byte
//   of every connection: the remote listener is a peeking listener that
//   admits only TLS handshake records (first byte 0x16) into the
//   handshake; any other first byte closes the connection directly
//   without writing any response — no TLS alert and no HTTP answer ever
//   crosses the wire to a plaintext probe.
//
// Credentials never enter business JSON, events, logs or digests: only the
// derived client identity string and the request signature verdict cross
// into the handler layer.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// DefaultReplayWindow bounds how far a remote request timestamp may deviate
// from the server clock and how long a consumed nonce stays rejected.
const DefaultReplayWindow = 5 * time.Minute

// The request-level replay protection headers of the remote baseline (ADR
// 0018 §12): every remote call binds a fresh nonce, an RFC 3339 timestamp
// inside the window and a signature over the canonical request binding.
const (
	HeaderNonce     = "Marshal-Nonce"
	HeaderTimestamp = "Marshal-Timestamp"
	HeaderSignature = "Marshal-Signature"
)

// Replay protection rejection sentinels, distinguishable by fixtures.
var (
	errReplayWindow = errors.New("server: replay protection rejected: the timestamp is outside the window")
	errReplayNonce  = errors.New("server: replay protection rejected: the nonce was already consumed inside the window")
)

// ClassifyListen decides whether listen binds an explicit loopback address.
// The loopback path keeps its existing plaintext behavior; every other
// explicit IP enters the remote path that demands the complete TLS baseline
// at listener construction (ADR 0018 §12). Host names, empty hosts and
// malformed addresses fail closed with a loopback-anchored reason.
func ClassifyListen(listen string) (bool, error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false, fmt.Errorf("监听地址无效（要求 HOST:PORT；loopback 保持显式 IP，非 loopback 要求完整 TLS 基线）：%w", err)
	}
	if host == "" {
		return false, errors.New("禁止绑定全部接口：监听主机必须是显式 loopback IP 地址，或提供完整 TLS 基线的显式非 loopback IP 地址")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false, errors.New("监听主机必须是显式 IP 地址：禁止主机名；loopback 路径保持明文，非 loopback 地址要求完整 TLS 基线")
	}
	return ip.IsLoopback(), nil
}

// TLSBaseline is the complete transport security baseline configuration of a
// non-loopback listener (ADR 0018 §12): the server key pair plus the client
// CA that enforces mutual identity validation. Construction fails closed
// while any part is absent.
type TLSBaseline struct {
	CertFile     string
	KeyFile      string
	ClientCAFile string
}

// Complete reports whether every baseline part is present.
func (b TLSBaseline) Complete() bool {
	return strings.TrimSpace(b.CertFile) != "" &&
		strings.TrimSpace(b.KeyFile) != "" &&
		strings.TrimSpace(b.ClientCAFile) != ""
}

// HasParts reports whether any baseline part is configured: a partial
// baseline fails closed on every listen address.
func (b TLSBaseline) HasParts() bool {
	return strings.TrimSpace(b.CertFile) != "" ||
		strings.TrimSpace(b.KeyFile) != "" ||
		strings.TrimSpace(b.ClientCAFile) != ""
}

// Config loads the baseline into a mutual-TLS server configuration. It
// fails closed on any missing part, unreadable file, unusable key pair or
// client CA without a usable certificate.
func (b TLSBaseline) Config() (*tls.Config, error) {
	if !b.Complete() {
		return nil, errors.New("server: a non-loopback transport requires the complete TLS baseline: server certificate, private key and client CA are all mandatory")
	}
	keyPair, err := tls.LoadX509KeyPair(b.CertFile, b.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("server: load TLS key pair: %w", err)
	}
	caPEM, err := os.ReadFile(b.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("server: read client CA: %w", err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("server: the client CA carries no usable certificate")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{keyPair},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// tlsHandshakeContentType is the TLS record content type of every handshake
// record (RFC 8446 §5.1): every legitimate TLS client speaks this byte
// first, and nothing else may enter the remote baseline.
const tlsHandshakeContentType = 0x16

// peekingListener enforces the TLS-only surface at the first byte of every
// accepted connection (ADR 0018 §12): only a connection starting with a TLS
// handshake record (content type 0x16) proceeds into the TLS handshake; any
// other first byte closes the connection directly without writing any
// response, so a plaintext probe never receives a TLS alert, an HTTP answer
// or any other byte.
type peekingListener struct {
	inner net.Listener
}

func newPeekingListener(inner net.Listener) *peekingListener {
	return &peekingListener{inner: inner}
}

func (l *peekingListener) Accept() (net.Conn, error) {
	conn, err := l.inner.Accept()
	if err != nil {
		return nil, err
	}
	return newPeekingConn(conn), nil
}

func (l *peekingListener) Close() error { return l.inner.Close() }

func (l *peekingListener) Addr() net.Addr { return l.inner.Addr() }

// peekingConn buffers exactly the first byte of one connection and admits
// only TLS handshake records: any other first byte closes the underlying
// connection directly, and the wrapper never writes a response byte to a
// rejected connection.
type peekingConn struct {
	net.Conn

	mu       sync.Mutex
	peeked   bool
	admitted bool
	first    byte
	buffered bool
}

func newPeekingConn(conn net.Conn) *peekingConn {
	return &peekingConn{Conn: conn}
}

// peekOnce reads the first byte exactly once. The connection is admitted
// only when the first byte is the TLS handshake content type; every other
// first byte (and every unreadable connection) closes the underlying
// connection directly.
func (c *peekingConn) peekOnce() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.peeked {
		return
	}
	c.peeked = true
	var first [1]byte
	if _, err := io.ReadFull(c.Conn, first[:]); err != nil {
		_ = c.Conn.Close()
		return
	}
	if first[0] != tlsHandshakeContentType {
		_ = c.Conn.Close()
		return
	}
	c.admitted = true
	c.first = first[0]
	c.buffered = true
}

// Read replays the peeked handshake byte once and then streams the
// underlying connection. A rejected connection reads EOF immediately.
func (c *peekingConn) Read(p []byte) (int, error) {
	c.peekOnce()
	c.mu.Lock()
	if !c.admitted {
		c.mu.Unlock()
		return 0, io.EOF
	}
	if c.buffered {
		if len(p) == 0 {
			c.mu.Unlock()
			return 0, nil
		}
		p[0] = c.first
		c.buffered = false
		c.mu.Unlock()
		return 1, nil
	}
	c.mu.Unlock()
	return c.Conn.Read(p)
}

// Write refuses every byte on a rejected connection: a plaintext probe must
// never receive a response, not even a TLS alert emitted by a layer above.
func (c *peekingConn) Write(p []byte) (int, error) {
	c.peekOnce()
	c.mu.Lock()
	admitted := c.admitted
	c.mu.Unlock()
	if !admitted {
		return 0, net.ErrClosed
	}
	return c.Conn.Write(p)
}

// Close closes the underlying connection exactly once per caller; repeated
// closes report the underlying error without side effects.
func (c *peekingConn) Close() error { return c.Conn.Close() }

// NewRemoteListener binds one TLS listener for a non-loopback transport. It
// fails closed unless the configuration enforces mutual identity validation
// (client CA + RequireAndVerifyClientCert): a remote listener without the
// complete baseline never starts. The bound listener is a peeking listener:
// only connections whose first byte is a TLS handshake record (0x16) enter
// the handshake; every other first byte closes the connection directly
// without writing any response.
func NewRemoteListener(listen string, config *tls.Config) (net.Listener, error) {
	if config == nil {
		return nil, errors.New("server: a non-loopback listener refuses to start without a TLS configuration")
	}
	if config.ClientAuth != tls.RequireAndVerifyClientCert || config.ClientCAs == nil {
		return nil, errors.New("server: a non-loopback listener requires mutual TLS: client CA and RequireAndVerifyClientCert are mandatory")
	}
	tcpListener, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("server: bind the remote listener: %w", err)
	}
	return tls.NewListener(newPeekingListener(tcpListener), config), nil
}

// Transport is one assembled serve surface: the listener and the handler
// bound to it under the ADR 0018 §12 transport security baseline.
type Transport struct {
	Listener   net.Listener
	Handler    http.Handler
	Loopback   bool
	TLSEnabled bool
}

// NewTransport assembles one transport for listen under the ADR 0018 §12
// baseline:
//
//   - a loopback listen without any TLS baseline parts keeps its frozen
//     plaintext behavior and serves handler untouched — the
//     loopback/in-process path never regresses;
//   - a non-loopback listen demands the complete baseline (server
//     certificate + key and client CA) at construction and is served only
//     over mutual TLS wrapped with the per-call replay protection —
//     plaintext connections are closed at the first byte by the peeking
//     listener and never reach the handler;
//   - a loopback listen carrying the complete baseline opts into the
//     identical TLS-only surface, plaintext connections included;
//   - a partial baseline fails closed on every listen address.
func NewTransport(listen string, baseline TLSBaseline, handler http.Handler, guard *ReplayGuard) (*Transport, error) {
	loopback, err := ClassifyListen(listen)
	if err != nil {
		return nil, err
	}
	if !baseline.Complete() {
		if baseline.HasParts() {
			return nil, errors.New("server: the TLS baseline is incomplete: the server certificate, private key and client CA are all mandatory once any TLS part is configured")
		}
		if !loopback {
			return nil, errors.New("server: a non-loopback transport requires the complete TLS baseline (server certificate, private key and client CA) from its first enable; only the loopback path stays plaintext")
		}
		listener, err := net.Listen("tcp", listen)
		if err != nil {
			return nil, fmt.Errorf("server: bind the loopback listener: %w", err)
		}
		if tcpAddress, ok := listener.Addr().(*net.TCPAddr); !ok || tcpAddress.IP == nil || !tcpAddress.IP.IsLoopback() {
			_ = listener.Close()
			return nil, errors.New("server: the plaintext listener did not bind an explicit loopback address; refusing to start")
		}
		return &Transport{Listener: listener, Handler: handler, Loopback: true}, nil
	}
	config, err := baseline.Config()
	if err != nil {
		return nil, err
	}
	listener, err := NewRemoteListener(listen, config)
	if err != nil {
		return nil, err
	}
	if !loopback {
		if tcpAddress, ok := listener.Addr().(*net.TCPAddr); ok && tcpAddress.IP != nil && tcpAddress.IP.IsLoopback() {
			_ = listener.Close()
			return nil, errors.New("server: the remote TLS listener resolved to a loopback address; refusing to start")
		}
	}
	return &Transport{Listener: listener, Handler: ProtectRemote(handler, guard), Loopback: loopback, TLSEnabled: true}, nil
}

type clientIdentityKeyType struct{}

var clientIdentityContextKey = clientIdentityKeyType{}

// ClientIdentityFromContext returns the verified transport client identity
// bound by ProtectRemote when the request arrived over the TLS baseline.
// Loopback/in-process requests carry no client identity.
func ClientIdentityFromContext(ctx context.Context) (string, bool) {
	identity, ok := ctx.Value(clientIdentityContextKey).(string)
	return identity, ok && identity != ""
}

// peerClientIdentity extracts the verified client identity of one remote
// request. Only the certificate validated by the TLS handshake is trusted;
// a missing handshake, a missing client certificate or a certificate without
// an identifiable subject fails closed.
func peerClientIdentity(request *http.Request) (string, *APIError) {
	if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
		return "", apiError(CodeForbiddenIdentity, "missing-client-identity",
			"non-loopback requests must complete a mutual TLS handshake with a client certificate")
	}
	leaf := request.TLS.PeerCertificates[0]
	if name := strings.TrimSpace(leaf.Subject.CommonName); name != "" {
		return name, nil
	}
	if len(leaf.DNSNames) > 0 {
		return leaf.DNSNames[0], nil
	}
	if len(leaf.IPAddresses) > 0 {
		return leaf.IPAddresses[0].String(), nil
	}
	if len(leaf.EmailAddresses) > 0 {
		return leaf.EmailAddresses[0], nil
	}
	return "", apiError(CodeForbiddenIdentity, "unusable-client-identity",
		"the client certificate carries no identifiable subject")
}

// ReplayGuard is the request-level replay protection of the remote baseline
// (ADR 0018 §12): every admitted call consumes a unique nonce inside a
// bounded time window; out-of-window timestamps and reused nonces fail
// closed. The consumed-nonce index is an in-memory guard keyed by transport
// client identity and pruned after the window; the business-level replay key
// remains the authority ledger's idempotency records.
type ReplayGuard struct {
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	seen map[string]map[string]time.Time
}

// NewReplayGuard binds one replay guard. A non-positive window selects
// DefaultReplayWindow; a nil clock selects time.Now.
func NewReplayGuard(window time.Duration, now func() time.Time) *ReplayGuard {
	if window <= 0 {
		window = DefaultReplayWindow
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &ReplayGuard{window: window, now: now, seen: map[string]map[string]time.Time{}}
}

// Check admits one (identity, nonce, timestamp) tuple fail closed and
// consumes the nonce for the rest of the window.
func (g *ReplayGuard) Check(identity, nonce string, timestamp time.Time) error {
	if strings.TrimSpace(nonce) == "" {
		return errors.New("server: replay protection rejected: the nonce is empty")
	}
	if len(nonce) > maxHeaderFieldBytes {
		return errors.New("server: replay protection rejected: the nonce exceeds its size limit")
	}
	if timestamp.IsZero() {
		return errors.New("server: replay protection rejected: the timestamp is absent")
	}
	now := g.now().UTC()
	delta := now.Sub(timestamp.UTC())
	if delta < 0 {
		delta = -delta
	}
	if delta > g.window {
		return errReplayWindow
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked(now)
	nonces, ok := g.seen[identity]
	if !ok {
		nonces = map[string]time.Time{}
		g.seen[identity] = nonces
	}
	if _, replayed := nonces[nonce]; replayed {
		return errReplayNonce
	}
	nonces[nonce] = now.Add(g.window)
	return nil
}

// pruneLocked drops consumed nonces whose rejection window has elapsed.
func (g *ReplayGuard) pruneLocked(now time.Time) {
	for identity, nonces := range g.seen {
		for nonce, expiry := range nonces {
			if !expiry.After(now) {
				delete(nonces, nonce)
			}
		}
		if len(nonces) == 0 {
			delete(g.seen, identity)
		}
	}
}

// ProtectRemote wraps the served handler with the remote transport security
// baseline: verified mutual client identity, request signature bound to the
// client certificate key, and nonce + time-window replay protection on every
// call. Requests without a completed TLS handshake fail closed; the wrapper
// never degrades to a plain path.
func ProtectRemote(handler http.Handler, guard *ReplayGuard) http.Handler {
	if guard == nil {
		guard = NewReplayGuard(DefaultReplayWindow, nil)
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		identity, apiErr := peerClientIdentity(request)
		if apiErr != nil {
			if request.TLS == nil {
				// A plaintext request can never receive an HTTP answer from
				// the remote baseline: when the wrapper is reached without
				// a completed TLS handshake, tear the connection down
				// exactly like the TLS handshake would, instead of serving
				// any response over plaintext.
				if hijacker, ok := writer.(http.Hijacker); ok {
					if conn, _, err := hijacker.Hijack(); err == nil {
						_ = conn.Close()
						return
					}
				}
			}
			writeError(writer, request.Header.Get(HeaderRequestID), apiErr)
			return
		}
		if apiErr := verifyReplayProtection(guard, request, identity); apiErr != nil {
			writeError(writer, request.Header.Get(HeaderRequestID), apiErr)
			return
		}
		ctx := context.WithValue(request.Context(), clientIdentityContextKey, identity)
		handler.ServeHTTP(writer, request.WithContext(ctx))
	})
}

// verifyReplayProtection enforces the per-call replay protection: complete
// nonce/timestamp/signature headers, a timestamp inside the window and a
// request signature that verifies against the client certificate key over
// the canonical request binding including the body content digest. Only a
// signature-valid call may consume a nonce, so forged requests never burn
// legitimate nonces.
func verifyReplayProtection(guard *ReplayGuard, request *http.Request, identity string) *APIError {
	nonce := request.Header.Get(HeaderNonce)
	timestampHeader := request.Header.Get(HeaderTimestamp)
	signature := request.Header.Get(HeaderSignature)
	if strings.TrimSpace(nonce) == "" || strings.TrimSpace(timestampHeader) == "" || strings.TrimSpace(signature) == "" {
		return apiError(CodeForbiddenIdentity, "replay-protection-incomplete",
			"remote requests must bind the nonce, timestamp and request signature headers")
	}
	timestamp, err := time.Parse(time.RFC3339, timestampHeader)
	if err != nil {
		return apiError(CodeForbiddenIdentity, "timestamp-invalid",
			"the request timestamp is not a valid RFC 3339 value")
	}
	var body []byte
	if request.Body != nil {
		body, err = io.ReadAll(io.LimitReader(request.Body, maxRequestBodyBytes+1))
		if err != nil {
			return apiError(CodeInvalidRequest, "body-unreadable", "the request body could not be read")
		}
		if int64(len(body)) > maxRequestBodyBytes {
			return apiError(CodeInvalidRequest, "body-too-large", "the request body exceeds the limit")
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
	}
	if err := VerifyRequestSignature(request.TLS.PeerCertificates[0], request.Method, request.URL.Path, timestampHeader, nonce, body, signature); err != nil {
		return apiError(CodeForbiddenIdentity, "signature-invalid",
			"the request signature does not verify against the client certificate")
	}
	if err := guard.Check(identity, nonce, timestamp); err != nil {
		switch {
		case errors.Is(err, errReplayWindow):
			return apiError(CodeForbiddenIdentity, "timestamp-outside-window",
				"the request timestamp is outside the replay protection window")
		case errors.Is(err, errReplayNonce):
			return apiError(CodeForbiddenIdentity, "replayed-nonce",
				"the request nonce was already consumed inside the replay protection window")
		default:
			return apiError(CodeForbiddenIdentity, "replay-protection-rejected",
				"the replay protection rejected the request")
		}
	}
	return nil
}

// requestBinding is the canonical document every remote request signature
// covers: method, path, the timestamp header verbatim, the nonce and the
// content digest of the body. The timestamp travels verbatim so both sides
// bind the identical bytes; RFC 8785 JCS canonicalization makes the binding
// independent of member order in any transport encoding.
type requestBinding struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Timestamp     string `json:"timestamp"`
	Nonce         string `json:"nonce"`
	ContentDigest string `json:"contentDigest"`
}

func canonicalRequestBinding(method, path, timestamp, nonce string, body []byte) ([]byte, error) {
	binding := requestBinding{
		Method:        method,
		Path:          path,
		Timestamp:     timestamp,
		Nonce:         nonce,
		ContentDigest: canonical.DigestBytes(body),
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return nil, fmt.Errorf("server: marshal request binding: %w", err)
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return nil, errors.New("server: canonicalize request binding failed")
	}
	return canonicalized, nil
}

// SignRequest produces the base64 request signature of the client-side half
// of the replay protection protocol. ECDSA and RSA-PSS sign the sha256
// digest of the canonical request binding; Ed25519 signs the canonical bytes
// directly. timestamp is the exact Marshal-Timestamp header value.
func SignRequest(privateKey crypto.PrivateKey, method, path, timestamp, nonce string, body []byte) (string, error) {
	canonicalized, err := canonicalRequestBinding(method, path, timestamp, nonce, body)
	if err != nil {
		return "", err
	}
	switch key := privateKey.(type) {
	case *ecdsa.PrivateKey:
		sum := sha256.Sum256(canonicalized)
		signature, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
		if err != nil {
			return "", fmt.Errorf("server: sign request binding: %w", err)
		}
		return base64.StdEncoding.EncodeToString(signature), nil
	case *rsa.PrivateKey:
		sum := sha256.Sum256(canonicalized)
		signature, err := rsa.SignPSS(rand.Reader, key, crypto.SHA256, sum[:], nil)
		if err != nil {
			return "", fmt.Errorf("server: sign request binding: %w", err)
		}
		return base64.StdEncoding.EncodeToString(signature), nil
	case ed25519.PrivateKey:
		return base64.StdEncoding.EncodeToString(ed25519.Sign(key, canonicalized)), nil
	default:
		return "", errors.New("server: sign request binding: unsupported private key type")
	}
}

// VerifyRequestSignature verifies one base64 request signature against the
// client certificate public key over the identical canonical request
// binding. Unsupported key types fail closed.
func VerifyRequestSignature(certificate *x509.Certificate, method, path, timestamp, nonce string, body []byte, signatureB64 string) error {
	if certificate == nil {
		return errors.New("server: request signature verification requires a client certificate")
	}
	canonicalized, err := canonicalRequestBinding(method, path, timestamp, nonce, body)
	if err != nil {
		return err
	}
	signature, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return errors.New("server: request signature is not base64")
	}
	switch publicKey := certificate.PublicKey.(type) {
	case *ecdsa.PublicKey:
		sum := sha256.Sum256(canonicalized)
		if !ecdsa.VerifyASN1(publicKey, sum[:], signature) {
			return errors.New("server: ECDSA request signature mismatch")
		}
		return nil
	case *rsa.PublicKey:
		sum := sha256.Sum256(canonicalized)
		if err := rsa.VerifyPSS(publicKey, crypto.SHA256, sum[:], signature, nil); err != nil {
			return errors.New("server: RSA-PSS request signature mismatch")
		}
		return nil
	case ed25519.PublicKey:
		if !ed25519.Verify(publicKey, canonicalized, signature) {
			return errors.New("server: Ed25519 request signature mismatch")
		}
		return nil
	default:
		return errors.New("server: unsupported client certificate key type")
	}
}
