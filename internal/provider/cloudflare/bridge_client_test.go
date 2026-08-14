package cloudflare

import (
	"context"
	"errors"
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

func TestClientHealthHappyPath(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("health"))
	client := newTestClient(t, fb, "")
	report, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if report.Status != "ok" || report.ProtocolVersion != DefaultProtocolVersion {
		t.Fatalf("unexpected health report: %+v", report)
	}
	headers := fb.AuthHeaders()
	if len(headers) != 1 || headers[0] != "Bearer "+fb.token {
		t.Fatalf("the credential must travel only as the Authorization header, got %v", headers)
	}
}

func TestClientHealthProtocolMismatchFailsClosed(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("protocol"))
	fb.SetProtocolVersion("v2" + "-drift")
	client := newTestClient(t, fb, "")
	if _, err := client.Health(context.Background()); !errors.Is(err, ErrProtocolVersionMismatch) {
		t.Fatalf("a protocol version drift must fail closed, got %v", err)
	}
}

func TestClientCredentialRejectedFailsClosedWithoutRetry(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("authz"))
	client := newTestClient(t, fb, fb.token+"-wrong")
	_, err := client.Health(context.Background())
	if !errors.Is(err, ErrCredentialRejected) {
		t.Fatalf("a rejected credential must fail closed with ErrCredentialRejected, got %v", err)
	}
	if got := fb.RequestCount("GET", healthPath); got != 1 {
		t.Fatalf("a credential refusal must never be retried, got %d attempts", got)
	}
}

func TestClientDuplicateJSONMemberRejected(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("duplicate-member"))
	fb.SetHealthRawBody([]byte(`{"status":"ok","status":"ok","protocolVersion":"v1"}`))
	client := newTestClient(t, fb, "")
	if _, err := client.Health(context.Background()); !errors.Is(err, ErrInvalidBridgeResponse) {
		t.Fatalf("duplicate JSON members must be rejected by the canonical admission gate, got %v", err)
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

// TestClientDropBudgetExhaustedFailsClosed freezes that connection-level
// response loss consumes exactly one wire request per attempt: the bounded
// retry budget of the client is the single retry authority, so three lost
// connections exhaust it fail closed for a bodied write and a bodiless
// write alike, with no transparent transport-level recovery beyond the
// budget.
func TestClientDropBudgetExhaustedFailsClosed(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		fb := newFakeBridge(t, testBridgeToken("drop-budget-create"))
		client := newTestClient(t, fb, "")
		fb.DropPathTimes("POST", sandboxesPath, 3)
		_, err := client.CreateSandbox(context.Background(), CreateSandboxRequest{
			SandboxId:  "alloc-drop-budget",
			RunId:      "run-x",
			AttemptId:  "attempt-x",
			Generation: 1,
		}, "key-drop-budget")
		if !errors.Is(err, ErrBridgeUnavailable) {
			t.Fatalf("three lost create responses must exhaust the retry budget fail closed, got %v", err)
		}
		if got := fb.RequestCount("POST", sandboxesPath); got != 3 {
			t.Fatalf("one attempt must be exactly one wire request, got %d", got)
		}
	})
	t.Run("destroy", func(t *testing.T) {
		fb := newFakeBridge(t, testBridgeToken("drop-budget-destroy"))
		client := newTestClient(t, fb, "")
		ctx := context.Background()
		if _, err := client.CreateSandbox(ctx, CreateSandboxRequest{
			SandboxId:  "alloc-drop-budget",
			RunId:      "run-x",
			AttemptId:  "attempt-x",
			Generation: 1,
		}, "key-create"); err != nil {
			t.Fatalf("CreateSandbox: %v", err)
		}
		fb.DropPathTimes("DELETE", sandboxesPath+"/alloc-drop-budget", 3)
		_, err := client.Destroy(ctx, "alloc-drop-budget", "key-destroy")
		if !errors.Is(err, ErrBridgeUnavailable) {
			t.Fatalf("three lost destroy responses must exhaust the retry budget fail closed, got %v", err)
		}
		if got := fb.RequestCount("DELETE", sandboxesPath+"/alloc-drop-budget"); got != 3 {
			t.Fatalf("one attempt must be exactly one wire request, got %d", got)
		}
	})
}

func TestClientRequestTimeoutRetried(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("timeout"))
	fb.SetSlowDuration(200 * time.Millisecond)
	fb.SlowPathTimes("GET", sandboxesPath, 1)
	client, err := NewClient(ClientConfig{
		BaseURL:        fb.server.URL,
		Credential:     mustCredential(t, fb.token),
		MaxRetries:     2,
		RetryDelay:     -1,
		RequestTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.ListSandboxes(context.Background(), "run-timeout", "attempt-timeout"); err != nil {
		t.Fatalf("ListSandboxes must recover from one timeout through the retry budget, got %v", err)
	}
	if got := fb.RequestCount("GET", sandboxesPath); got != 2 {
		t.Fatalf("exactly one retry after the timeout was expected, got %d", got)
	}
}

func TestClientSandboxNotFoundAndContainerLost(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("not-found"))
	client := newTestClient(t, fb, "")
	ctx := context.Background()
	if _, err := client.SandboxStatus(ctx, "alloc-missing"); !errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("an unknown sandbox must map to ErrSandboxNotFound, got %v", err)
	}
	if _, err := client.CreateSandbox(ctx, CreateSandboxRequest{SandboxId: "alloc-lost", RunId: "run-x", AttemptId: "attempt-x", Generation: 1}, "key-lost"); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	fb.LoseContainer(t, "alloc-lost")
	if _, err := client.SandboxStatus(ctx, "alloc-lost"); !errors.Is(err, ErrContainerLost) {
		t.Fatalf("a lost container must map to ErrContainerLost, got %v", err)
	}
}

func TestClientNotFoundCodeMapping(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("codes"))
	client := newTestClient(t, fb, "")
	ctx := context.Background()
	if _, err := client.CreateSandbox(ctx, CreateSandboxRequest{SandboxId: "alloc-codes", RunId: "run-x", AttemptId: "attempt-x", Generation: 1}, "key-codes"); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if _, err := client.Hydrate(ctx, "alloc-codes", "ckpt-missing", "key-hydrate"); !errors.Is(err, ErrCheckpointNotFound) {
		t.Fatalf("an unknown checkpoint must map to ErrCheckpointNotFound, got %v", err)
	}
	if _, err := client.WriteFile(ctx, "alloc-codes", WriteFileRequest{
		Path:           "staged/ref",
		DeclaredSHA256: fixtureDigest("locator" + "-content"),
		Locator:        &LocatorRef{StoreId: "store-empty", SHA256: fixtureDigest("locator" + "-content"), SizeBytes: 4},
	}, "key-write"); !errors.Is(err, ErrBridgeLocatorUnresolved) {
		t.Fatalf("an unresolved locator must map to ErrBridgeLocatorUnresolved, got %v", err)
	}
	if _, err := client.ReadFile(ctx, "alloc-codes", "staged/missing"); !errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("a missing file must map to ErrSandboxNotFound, got %v", err)
	}
}

func TestClientIdempotentReplayAfterLostResponse(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("idempotent"))
	client := newTestClient(t, fb, "")
	fb.DropPathTimes("POST", sandboxesPath, 1)
	record, err := client.CreateSandbox(context.Background(), CreateSandboxRequest{
		SandboxId:  "alloc-idempotent",
		RunId:      "run-x",
		AttemptId:  "attempt-x",
		Generation: 1,
	}, "key-create")
	if err != nil {
		t.Fatalf("CreateSandbox must recover from one lost response through the idempotent retry, got %v", err)
	}
	if record.SandboxId != "alloc-idempotent" || record.State != "running" {
		t.Fatalf("unexpected create record: %+v", record)
	}
	if got := fb.RequestCount("POST", sandboxesPath); got != 2 {
		t.Fatalf("exactly one retry after the lost response was expected, got %d", got)
	}
}

func TestClientExecStreamHappyPathAndOutcomes(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("exec"))
	client := newTestClient(t, fb, "")
	ctx := context.Background()
	if _, err := client.CreateSandbox(ctx, CreateSandboxRequest{SandboxId: "alloc-exec", RunId: "run-x", AttemptId: "attempt-x", Generation: 1}, "key-exec"); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	result, err := client.Exec(ctx, "alloc-exec", ExecStreamRequest{Command: []string{"echo", "hello"}})
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
	expectedStderr := "exec stderr\x00" + strings.Join([]string{"echo", "hello"}, "\x00")
	if string(result.Stderr) != expectedStderr {
		t.Fatalf("unexpected stderr capture: %q", string(result.Stderr))
	}

	fb.SetExecOutcome("failing-cmd", 3, false)
	failed, err := client.Exec(ctx, "alloc-exec", ExecStreamRequest{Command: []string{"failing-cmd"}})
	if err != nil {
		t.Fatalf("Exec of the failing command: %v", err)
	}
	if failed.ExitCode != 3 || failed.Signaled {
		t.Fatalf("the scripted failure must surface exit code 3, got %+v", failed)
	}

	fb.SetExecOutcome("doomed-cmd", 137, true)
	killed, err := client.Exec(ctx, "alloc-exec", ExecStreamRequest{Command: []string{"doomed-cmd"}})
	if err != nil {
		t.Fatalf("Exec of the doomed command: %v", err)
	}
	if !killed.Signaled || killed.ExitCode != 137 {
		t.Fatalf("the scripted kill must surface the signaled flag, got %+v", killed)
	}
}

func TestClientExecStreamBrokenFailsClosedWithoutRetry(t *testing.T) {
	fb := newFakeBridge(t, testBridgeToken("exec-broken"))
	client := newTestClient(t, fb, "")
	ctx := context.Background()
	if _, err := client.CreateSandbox(ctx, CreateSandboxRequest{SandboxId: "alloc-broken", RunId: "run-x", AttemptId: "attempt-x", Generation: 1}, "key-broken"); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	fb.DropPathTimes("POST", sandboxesPath+"/alloc-broken/exec", 1)
	if _, err := client.Exec(ctx, "alloc-broken", ExecStreamRequest{Command: []string{"echo"}}); !errors.Is(err, ErrBridgeUnavailable) {
		t.Fatalf("a broken exec stream must fail closed, got %v", err)
	}
	if got := fb.RequestCount("POST", sandboxesPath+"/alloc-broken/exec"); got != 1 {
		t.Fatalf("exec must never be auto-retried, got %d attempts", got)
	}
}

func TestClientCredentialNeverSurfacesInErrors(t *testing.T) {
	token := testBridgeToken("client-leak")
	fb := newFakeBridge(t, token)
	ctx := context.Background()
	var observables []string

	wrongClient := newTestClient(t, fb, token+"-wrong")
	if _, err := wrongClient.Health(ctx); err != nil {
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
	if _, err := client.SandboxStatus(ctx, "alloc-missing"); err != nil {
		observables = append(observables, err.Error())
	} else {
		t.Fatal("the unknown sandbox must fail")
	}

	for _, observable := range observables {
		assertNoCredential(t, token, observable)
		assertNoCredential(t, token+"-wrong", observable)
	}
}
