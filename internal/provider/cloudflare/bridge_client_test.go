package cloudflare

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"
)

// mustCredential fails the test when the fixture credential cannot be
// constructed.
func mustCredential(t *testing.T, token string) Credential {
	t.Helper()
	credential, err := NewCredential(token)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	return credential
}

// newTestClient builds one Bridge client against the fixture Bridge with
// deterministic retry settings and no retry delay.
func newTestClient(t *testing.T, fb *fakeBridge, tokenOverride string) *Client {
	t.Helper()
	token := fb.token
	if tokenOverride != "" {
		token = tokenOverride
	}
	client, err := NewClient(ClientConfig{
		BaseURL:        fb.server.URL,
		Credential:     mustCredential(t, token),
		MaxRetries:     2,
		RetryDelay:     -1,
		RequestTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestClientMissingCredentialFailsClosed(t *testing.T) {
	if _, err := NewCredential(""); !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("an empty credential must fail closed with ErrCredentialMissing, got %v", err)
	}
	if _, err := NewClient(ClientConfig{BaseURL: "https://bridge" + ".example"}); !errors.Is(err, ErrCredentialMissing) {
		t.Fatalf("a client without credential must fail closed, got %v", err)
	}
}

func TestClientRejectsCredentialInsideBaseURL(t *testing.T) {
	token := testBridgeToken("base-url")
	_, err := NewClient(ClientConfig{
		BaseURL:    "https://bridge.example/" + token,
		Credential: mustCredential(t, token),
	})
	if !errors.Is(err, ErrInvalidClientConfig) {
		t.Fatalf("a base URL carrying the credential must be rejected, got %v", err)
	}
	if err != nil && strings.Contains(err.Error(), token) {
		t.Fatal("the refusal must never echo the credential")
	}
}

// TestCredentialRedaction freezes the credential discipline across every
// common formatting surface: value and pointer verbs, a carrier struct, an
// error wrap and a log call all redact the literal.
func TestCredentialRedaction(t *testing.T) {
	token := testBridgeToken("redaction")
	credential, err := NewCredential(token)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	observables := []string{
		fmt.Sprintf("%v", credential),
		fmt.Sprintf("%+v", credential),
		fmt.Sprintf("%#v", credential),
		fmt.Sprintf("%s", credential),
		fmt.Sprintf("%q", credential),
		fmt.Sprintf("%v", &credential),
		fmt.Sprintf("%+v", &credential),
		fmt.Sprintf("%#v", &credential),
	}
	type carrier struct {
		Cred Credential
	}
	observables = append(observables, fmt.Sprintf("%+v", carrier{Cred: credential}))
	observables = append(observables, fmt.Sprintf("%#v", carrier{Cred: credential}))
	observables = append(observables, fmt.Sprintf("%v", carrier{Cred: credential}))

	wrapped := fmt.Errorf("wrapped credential: %v", credential)
	observables = append(observables, wrapped.Error())

	var logBuf bytes.Buffer
	logger := log.New(&logBuf, "", 0)
	logger.Printf("credential=%v", credential)
	logger.Printf("credential=%+v", credential)
	observables = append(observables, logBuf.String())

	for _, observable := range observables {
		assertNoCredential(t, token, observable)
		if !strings.Contains(observable, redactedCredential) {
			t.Fatalf("the credential must be redacted, got %q", observable)
		}
	}
}

func TestClientHealthHappyPathNoCredential(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("health"))
	client := newTestClient(t, fb, "")
	report, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !report.OK {
		t.Fatalf("unexpected health report: %+v", report)
	}
	headers := fb.AuthHeaders()
	if len(headers) != 1 || headers[0] != "" {
		t.Fatalf("the health read must carry no Authorization header, got %v", headers)
	}
}

func TestClientHealthMalformedRejected(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("health-malformed"))
	fb.SetHealthRawBody([]byte(`{"ok":true,"ok":false}`))
	client := newTestClient(t, fb, "")
	if _, err := client.Health(context.Background()); !errors.Is(err, ErrInvalidBridgeResponse) {
		t.Fatalf("duplicate JSON members must be rejected by the canonical admission gate, got %v", err)
	}
}

func TestClientCredentialRejectedFailsClosedWithoutRetry(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("authz"))
	client := newTestClient(t, fb, fb.token+"-wrong")
	_, err := client.CreateSandbox(context.Background(), "key-authz")
	if !errors.Is(err, ErrCredentialRejected) {
		t.Fatalf("a rejected credential must fail closed with ErrCredentialRejected, got %v", err)
	}
	if got := fb.RequestCount("POST", sandboxPath); got != 1 {
		t.Fatalf("a credential refusal must never be retried, got %d attempts", got)
	}
}

func TestClientRetriesTransientFailuresThenSucceeds(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("retry-success"))
	client := newTestClient(t, fb, "")
	fb.FailPathTimes("GET", healthPath, 2)
	if _, err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health must succeed after two transient failures, got %v", err)
	}
	if got := fb.RequestCount("GET", healthPath); got != 3 {
		t.Fatalf("exactly three attempts were expected, got %d", got)
	}
}

func TestClientRetryExhaustedFailsClosed(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("retry-exhausted"))
	client := newTestClient(t, fb, "")
	fb.FailPathTimes("GET", healthPath, 100)
	_, err := client.Health(context.Background())
	if !errors.Is(err, ErrBridgeUnavailable) {
		t.Fatalf("an exhausted retry budget must fail closed with ErrBridgeUnavailable, got %v", err)
	}
	if got := fb.RequestCount("GET", healthPath); got != 3 {
		t.Fatalf("the retry budget is bounded at maxRetries+1 attempts, got %d", got)
	}
}

func TestClientCreateRunningDestroy(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("lifecycle"))
	client := newTestClient(t, fb, "")
	ctx := context.Background()

	bridgeId, err := client.CreateSandbox(ctx, "key-create")
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if bridgeId == "" {
		t.Fatal("the create must return the Bridge locator")
	}
	running, err := client.SandboxRunning(ctx, bridgeId)
	if err != nil {
		t.Fatalf("SandboxRunning: %v", err)
	}
	if !running {
		t.Fatal("the fresh sandbox must be running")
	}
	if err := client.Destroy(ctx, bridgeId, "key-destroy"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := client.SandboxRunning(ctx, bridgeId); !errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("the destroyed sandbox must map to ErrSandboxNotFound, got %v", err)
	}
}

func TestClientIdempotentReplayAfterLostResponse(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("idempotent"))
	client := newTestClient(t, fb, "")
	fb.DropPathTimes("POST", sandboxPath, 1)
	bridgeId, err := client.CreateSandbox(context.Background(), "key-create")
	if err != nil {
		t.Fatalf("CreateSandbox must recover from one lost response through the idempotent retry, got %v", err)
	}
	if bridgeId == "" {
		t.Fatal("the create must return a locator")
	}
	if got := fb.RequestCount("POST", sandboxPath); got != 2 {
		t.Fatalf("exactly one retry after the lost response was expected, got %d", got)
	}
	// Replaying the identical key must converge on the identical locator.
	replayed, err := client.CreateSandbox(context.Background(), "key-create")
	if err != nil {
		t.Fatalf("CreateSandbox replay: %v", err)
	}
	if replayed != bridgeId {
		t.Fatalf("the idempotent replay must return the identical locator: %q != %q", replayed, bridgeId)
	}
}

func TestClientExecStreamHappyPathAndOutcomes(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("exec"))
	client := newTestClient(t, fb, "")
	ctx := context.Background()
	bridgeId, err := client.CreateSandbox(ctx, "key-exec")
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	result, err := client.Exec(ctx, bridgeId, "sess-1", ExecStreamRequest{Argv: []string{"echo", "hello"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if result.ExitCode != 0 || result.Signaled {
		t.Fatalf("the happy-path exec must complete cleanly, got %+v", result)
	}
	expectedStdout := "exec stdout\x00" + strings.Join([]string{"echo", "hello"}, "\x00")
	if string(result.Stdout) != expectedStdout {
		t.Fatalf("unexpected stdout capture: %q", string(result.Stdout))
	}

	fb.SetExecOutcome("failing-cmd", 3, false)
	failed, err := client.Exec(ctx, bridgeId, "sess-1", ExecStreamRequest{Argv: []string{"failing-cmd"}})
	if err != nil {
		t.Fatalf("Exec of the failing command: %v", err)
	}
	if failed.ExitCode != 3 || failed.Signaled {
		t.Fatalf("the scripted failure must surface exit code 3, got %+v", failed)
	}

	fb.SetExecOutcome("doomed-cmd", 137, true)
	killed, err := client.Exec(ctx, bridgeId, "sess-1", ExecStreamRequest{Argv: []string{"doomed-cmd"}})
	if err != nil {
		t.Fatalf("Exec of the doomed command: %v", err)
	}
	if !killed.Signaled {
		t.Fatalf("the scripted kill must surface the signaled flag, got %+v", killed)
	}
}

func TestClientExecStreamBrokenFailsClosedWithoutRetry(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("exec-broken"))
	client := newTestClient(t, fb, "")
	ctx := context.Background()
	bridgeId, err := client.CreateSandbox(ctx, "key-broken")
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	fb.DropPathTimes("POST", "/v1/sandbox/"+bridgeId+"/exec", 1)
	if _, err := client.Exec(ctx, bridgeId, "", ExecStreamRequest{Argv: []string{"echo"}}); !errors.Is(err, ErrBridgeUnavailable) {
		t.Fatalf("a broken exec stream must fail closed, got %v", err)
	}
	if got := fb.RequestCount("POST", "/v1/sandbox/"+bridgeId+"/exec"); got != 1 {
		t.Fatalf("exec must never be auto-retried, got %d attempts", got)
	}
}

func TestClientFileWriteReadRawBytes(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("file"))
	client := newTestClient(t, fb, "")
	ctx := context.Background()
	bridgeId, err := client.CreateSandbox(ctx, "key-file")
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	content := []byte("raw-file-content")
	if err := client.WriteFile(ctx, bridgeId, "staged/payload", content, "key-write"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	readBack, err := client.ReadFile(ctx, bridgeId, "staged/payload")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(readBack, content) {
		t.Fatalf("the raw bytes must round-trip, got %q", string(readBack))
	}
}

func TestClientPersistHydrateRawTar(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("persist"))
	client := newTestClient(t, fb, "")
	ctx := context.Background()
	bridgeId, err := client.CreateSandbox(ctx, "key-persist")
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	content := []byte("persist-content")
	if err := client.WriteFile(ctx, bridgeId, "staged/payload", content, "key-write"); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	tarBytes, err := client.Persist(ctx, bridgeId, "key-persist-2")
	if err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if len(tarBytes) == 0 {
		t.Fatal("the persist must return the raw tar snapshot")
	}
	files, err := parseTar(tarBytes)
	if err != nil {
		t.Fatalf("the persist payload must be a tar: %v", err)
	}
	if !bytes.Equal(files["staged/payload"], content) {
		t.Fatal("the tar snapshot must hold the staged content")
	}

	replacement, err := client.CreateSandbox(ctx, "key-create-2")
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if err := client.Hydrate(ctx, replacement, tarBytes, "key-hydrate"); err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	restored, err := client.ReadFile(ctx, replacement, "staged/payload")
	if err != nil {
		t.Fatalf("ReadFile after hydrate: %v", err)
	}
	if !bytes.Equal(restored, content) {
		t.Fatal("the hydrate must restore the staged content byte for byte")
	}
}

func TestClientSessionCreateDelete(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("session"))
	client := newTestClient(t, fb, "")
	ctx := context.Background()
	bridgeId, err := client.CreateSandbox(ctx, "key-session")
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	sessionId, err := client.CreateSession(ctx, bridgeId, "key-session-2")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sessionId == "" {
		t.Fatal("the session endpoint must return a session id")
	}
	if err := client.DeleteSession(ctx, bridgeId, sessionId); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if err := client.DeleteSession(ctx, bridgeId, sessionId); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("deleting a deleted session must map to ErrSessionNotFound, got %v", err)
	}
}

// TestParseExecStreamTerminalDiscipline freezes the "exactly one terminal"
// rule: a stream without a terminal, with two terminals, or with a trailing
// event after the terminal, all fail closed.
func TestParseExecStreamTerminalDiscipline(t *testing.T) {
	stream := func(events ...string) string {
		var b strings.Builder
		for _, event := range events {
			b.WriteString(event)
			b.WriteString("\n\n")
		}
		return b.String()
	}
	outputEvent := func(name, raw string) string {
		return "event: " + name + "\ndata: " + base64.StdEncoding.EncodeToString([]byte(raw))
	}
	exitEvent := "event: exit\ndata: {\"exit_code\":0}"
	errorEvent := "event: error\ndata: {\"message\":\"killed\"}"

	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{"happy", stream(outputEvent("stdout", "out"), exitEvent), false},
		{"error-terminal", stream(errorEvent), false},
		{"no-terminal", stream(outputEvent("stdout", "out")), true},
		{"two-terminals", stream(exitEvent, exitEvent), true},
		{"trailing-after-terminal", stream(exitEvent, outputEvent("stdout", "out")), true},
		{"malformed-output", stream("event: stdout\ndata: !!!not-base64!!!", exitEvent), true},
		{"unknown-event", stream("event: mystery\ndata: x", exitEvent), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := parseExecStream(strings.NewReader(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("the stream must fail closed, got %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatalf("the stream must parse, got %v", err)
			}
		})
	}
}

func TestClientNotFoundAndContainerLostMapping(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("not-found"))
	client := newTestClient(t, fb, "")
	ctx := context.Background()
	if _, err := client.SandboxRunning(ctx, "br-missing"); !errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("an unknown sandbox must map to ErrSandboxNotFound, got %v", err)
	}
	bridgeId, err := client.CreateSandbox(ctx, "key-lost")
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	fb.LoseContainer(t, bridgeId)
	if _, err := client.SandboxRunning(ctx, bridgeId); err != nil {
		t.Fatalf("running of a lost container must be an observation, got %v", err)
	}
	if _, err := client.Persist(ctx, bridgeId, "key-persist"); !errors.Is(err, ErrContainerLost) {
		t.Fatalf("persist of a lost container must map to ErrContainerLost, got %v", err)
	}
}

func TestClientCredentialNeverSurfacesInErrors(t *testing.T) {
	token := testBridgeToken("client-leak")
	fb := newFakeBridge(t, token)
	ctx := context.Background()
	var observables []string

	wrongClient := newTestClient(t, fb, token+"-wrong")
	if _, err := wrongClient.CreateSandbox(ctx, "key-leak"); err != nil {
		observables = append(observables, err.Error())
	} else {
		t.Fatal("the wrong credential must be rejected")
	}

	client := newTestClient(t, fb, "")
	fb.FailPathTimes("GET", healthPath, 100)
	if _, err := client.Health(ctx); err != nil {
		observables = append(observables, err.Error())
	} else {
		t.Fatal("the exhausted retry budget must fail")
	}
	if _, err := client.SandboxRunning(ctx, "br-missing"); err != nil {
		observables = append(observables, err.Error())
	} else {
		t.Fatal("the unknown sandbox must fail")
	}

	for _, observable := range observables {
		assertNoCredential(t, token, observable)
		assertNoCredential(t, token+"-wrong", observable)
	}
}
