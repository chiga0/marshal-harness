package sandbox

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

// TestExecRequestValidateEnvelope freezes the provider-independent shape
// adjudication of the ADR 0055 workload envelope: the zero envelope and
// every well-formed dimension validate clean, and each malformed variant
// fails closed with its typed sentinel.
func TestExecRequestValidateEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(request *ExecRequest)
		sentinel error
	}{
		{"zero-envelope", func(request *ExecRequest) {}, nil},
		{"absolute-workdir", func(request *ExecRequest) { request.WorkingDir = "/opt/agent" }, nil},
		{"relative-workdir-rejected", func(request *ExecRequest) { request.WorkingDir = "opt/agent" }, ErrInvalidWorkDir},
		{"empty-env-key-rejected", func(request *ExecRequest) { request.Environment = map[string]string{"": "x"} }, ErrInvalidRequest},
		{"whitespace-env-key-rejected", func(request *ExecRequest) { request.Environment = map[string]string{" PATH": "x"} }, ErrInvalidRequest},
		{"equals-env-key-rejected", func(request *ExecRequest) { request.Environment = map[string]string{"A=B": "x"} }, ErrInvalidRequest},
		{"credential-env-key-rejected", func(request *ExecRequest) { request.Environment = map[string]string{"MY_API_SECRET": "x"} }, ErrCredentialKeyRejected},
		{"plain-env-key", func(request *ExecRequest) { request.Environment = map[string]string{"MARSHAL_TRACE": "1"} }, nil},
		{"transcript-zero-maxbytes-rejected", func(request *ExecRequest) {
			request.TranscriptPolicy = TranscriptPolicy{MaxBytes: 0, ArtifactId: "t.txt"}
		}, ErrInvalidTranscriptPolicy},
		{"transcript-negative-maxbytes-rejected", func(request *ExecRequest) {
			request.TranscriptPolicy = TranscriptPolicy{MaxBytes: -1, ArtifactId: "t.txt"}
		}, ErrInvalidTranscriptPolicy},
		{"transcript-empty-artifact-rejected", func(request *ExecRequest) { request.TranscriptPolicy = TranscriptPolicy{MaxBytes: 64, ArtifactId: ""} }, ErrInvalidTranscriptPolicy},
		{"transcript-absolute-artifact-rejected", func(request *ExecRequest) {
			request.TranscriptPolicy = TranscriptPolicy{MaxBytes: 64, ArtifactId: "/tmp/t.txt"}
		}, ErrInvalidTranscriptPolicy},
		{"transcript-traversal-artifact-rejected", func(request *ExecRequest) {
			request.TranscriptPolicy = TranscriptPolicy{MaxBytes: 64, ArtifactId: "../t.txt"}
		}, ErrInvalidTranscriptPolicy},
		{"transcript-nested-traversal-rejected", func(request *ExecRequest) {
			request.TranscriptPolicy = TranscriptPolicy{MaxBytes: 64, ArtifactId: "nested/../t.txt"}
		}, ErrInvalidTranscriptPolicy},
		{"transcript-well-formed", func(request *ExecRequest) {
			request.TranscriptPolicy = TranscriptPolicy{MaxBytes: 64, ArtifactId: "transcripts/t.txt"}
		}, nil},
		{"timeout-negative-keeps-default", func(request *ExecRequest) { request.TimeoutSeconds = -5 }, nil},
		{"timeout-positive", func(request *ExecRequest) { request.TimeoutSeconds = 30 }, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := ExecRequest{}
			tc.mutate(&request)
			err := request.ValidateEnvelope()
			if tc.sentinel == nil {
				if err != nil {
					t.Fatalf("ValidateEnvelope rejected a well-formed envelope: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("ValidateEnvelope must fail closed with %v, got %v", tc.sentinel, err)
			}
		})
	}
}

// TestValidateEnvironmentAllowlist freezes the Provision-time closed
// declaration rules of ADR 0055 §2: well-formed unique keys register, and
// every malformed or credential-semantic key fails closed. The credential
// rule is verbatim case-insensitive substring containment ({"key", "token",
// "secret", "password"}), so "monkey" matches through "key".
func TestValidateEnvironmentAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name      string
		allowlist []string
		sentinel  error
	}{
		{"empty", nil, nil},
		{"well-formed", []string{"PATH", "MARSHAL_TRACE"}, nil},
		{"empty-key", []string{""}, ErrInvalidRequest},
		{"whitespace-key", []string{" PATH"}, ErrInvalidRequest},
		{"equals-key", []string{"A=B"}, ErrInvalidRequest},
		{"duplicate-key", []string{"PATH", "PATH"}, ErrInvalidRequest},
		{"token-substring", []string{"API_TOKEN"}, ErrCredentialKeyRejected},
		{"secret-substring-lowercase", []string{"db-password"}, ErrCredentialKeyRejected},
		{"key-substring-uppercase", []string{"AWS_ACCESS_KEY_ID"}, ErrCredentialKeyRejected},
		{"secret-substring", []string{"MYSECRET"}, ErrCredentialKeyRejected},
		{"monkey-matches-key-substring", []string{"MONKEY"}, ErrCredentialKeyRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEnvironmentAllowlist(tc.allowlist)
			if tc.sentinel == nil {
				if err != nil {
					t.Fatalf("ValidateEnvironmentAllowlist rejected %v: %v", tc.allowlist, err)
				}
				return
			}
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("ValidateEnvironmentAllowlist(%v) must fail closed with %v, got %v", tc.allowlist, tc.sentinel, err)
			}
		})
	}
}

// TestValidateWorkDirAllowlist freezes the Provision-time closed
// declaration rules of ADR 0055 §1: only non-empty absolute paths register.
func TestValidateWorkDirAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name      string
		allowlist []string
		sentinel  error
	}{
		{"empty", nil, nil},
		{"absolute", []string{"/opt/agent", "/var/task"}, nil},
		{"relative", []string{"opt/agent"}, ErrInvalidWorkDir},
		{"empty-entry", []string{""}, ErrInvalidWorkDir},
		{"blank-entry", []string{"   "}, ErrInvalidWorkDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWorkDirAllowlist(tc.allowlist)
			if tc.sentinel == nil {
				if err != nil {
					t.Fatalf("ValidateWorkDirAllowlist rejected %v: %v", tc.allowlist, err)
				}
				return
			}
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("ValidateWorkDirAllowlist(%v) must fail closed with %v, got %v", tc.allowlist, tc.sentinel, err)
			}
		})
	}
}

// TestResolveExecEnvironment freezes the closed per-op environment
// adjudication of ADR 0055 §2: allow-listed keys resolve in deterministic
// sorted order, and any unknown or credential-semantic key fails closed
// instead of being unioned into the environment.
func TestResolveExecEnvironment(t *testing.T) {
	overlay, err := ResolveExecEnvironment(nil, []string{"PATH"})
	if err != nil || overlay != nil {
		t.Fatalf("an empty environment must resolve to no overlay, got %v, %v", overlay, err)
	}
	if _, err = ResolveExecEnvironment(map[string]string{"MY_TOKEN": "2"}, []string{}); !errors.Is(err, ErrCredentialKeyRejected) {
		t.Fatalf("a credential-semantic key must fail closed at exec even with an empty allowlist, got %v", err)
	}
	overlay, err = ResolveExecEnvironment(map[string]string{"B_FLAG": "2", "A_FLAG": "1"}, []string{"A_FLAG", "B_FLAG"})
	if err != nil {
		t.Fatalf("allow-listed keys must resolve: %v", err)
	}
	if strings.Join(overlay, ",") != "A_FLAG=1,B_FLAG=2" {
		t.Fatalf("the overlay must be sorted deterministically, got %v", overlay)
	}
	for _, tc := range []struct {
		name     string
		env      map[string]string
		sentinel error
	}{
		{"unknown-key", map[string]string{"NOT_DECLARED": "x"}, ErrEnvKeyNotAllowed},
		{"malformed-key", map[string]string{"A=B": "x"}, ErrInvalidRequest},
		{"credential-key", map[string]string{"MY_TOKEN": "x"}, ErrCredentialKeyRejected},
	} {
		if _, err := ResolveExecEnvironment(tc.env, []string{"A_FLAG"}); !errors.Is(err, tc.sentinel) {
			t.Fatalf("%s: must fail closed with %v, got %v", tc.name, tc.sentinel, err)
		}
	}
}

// TestEffectiveTimeoutSeconds freezes the ADR 0055 §4 timeout arithmetic:
// positive requests clamp at the provider cap and non-positive values keep
// the cap, without any overflow.
func TestEffectiveTimeoutSeconds(t *testing.T) {
	for _, tc := range []struct {
		requested int64
		cap       int64
		expected  int64
	}{
		{0, 30, 30},
		{-5, 30, 30},
		{10, 30, 10},
		{30, 30, 30},
		{31, 30, 30},
		{math.MaxInt64, 30, 30},
	} {
		if got := EffectiveTimeoutSeconds(tc.requested, tc.cap); got != tc.expected {
			t.Fatalf("EffectiveTimeoutSeconds(%d, %d) = %d, expected %d", tc.requested, tc.cap, got, tc.expected)
		}
	}
}

// TestSandboxAllocationEnvelopeSnapshotValidation freezes that an
// allocation record carrying malformed ADR 0055 declarations never
// validates, while a well-formed declaration snapshot validates clean.
func TestSandboxAllocationEnvelopeSnapshotValidation(t *testing.T) {
	allocation := validAllocation("allocation-"+"envelope", 1, AllocationActive)
	allocation.WorkDirAllowlist = []string{"/opt/agent"}
	allocation.EnvironmentAllowlist = []string{"MARSHAL_TRACE"}
	if err := allocation.Validate(); err != nil {
		t.Fatalf("a well-formed envelope snapshot must validate: %v", err)
	}
	badWorkDir := allocation
	badWorkDir.WorkDirAllowlist = []string{"relative/dir"}
	if err := badWorkDir.Validate(); !errors.Is(err, ErrInvalidAllocation) {
		t.Fatalf("a relative workDirAllowlist entry must fail closed with ErrInvalidAllocation, got %v", err)
	}
	badEnv := allocation
	badEnv.EnvironmentAllowlist = []string{"API_SECRET"}
	if err := badEnv.Validate(); !errors.Is(err, ErrInvalidAllocation) {
		t.Fatalf("a credential-semantic environmentAllowlist entry must fail closed with ErrInvalidAllocation, got %v", err)
	}
}

// provisionEnvelopeAllocation provisions one fake allocation carrying the
// given ADR 0055 declarations.
func provisionEnvelopeAllocation(t *testing.T, fake *FakeProvider, allocationId string, workDirs, envKeys []string) SandboxAllocation {
	t.Helper()
	receipt, err := fake.Provision(context.Background(), ProvisionRequest{
		Identity:             testIdentity(allocationId, "command-provision"),
		Requirements:         workspaceWriteRequirements(),
		WorkDirAllowlist:     workDirs,
		EnvironmentAllowlist: envKeys,
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	return receipt.Allocation
}

// fakeInspectLogLines returns the observation log lines of one fake
// allocation for envelope-effect assertions.
func fakeInspectLogLines(t *testing.T, fake *FakeProvider, allocationId string) []string {
	t.Helper()
	report, err := fake.Inspect(context.Background(), InspectRequest{
		Identity:     testIdentity(allocationId, "command-inspect"),
		AllocationId: allocationId,
	})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return report.LogLines
}

func fakeLogContains(lines []string, substring string) bool {
	for _, line := range lines {
		if strings.Contains(line, substring) {
			return true
		}
	}
	return false
}

// TestFakeProvisionEnvelopeRegistration freezes that the fake provider
// records the ADR 0055 declarations in the allocation record and rejects
// malformed declarations fail closed before creating any allocation.
func TestFakeProvisionEnvelopeRegistration(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	allocation := provisionEnvelopeAllocation(t, fake, "allocation-"+"envelope-prov", []string{"/opt/agent"}, []string{"MARSHAL_TRACE"})
	if len(allocation.WorkDirAllowlist) != 1 || allocation.WorkDirAllowlist[0] != "/opt/agent" {
		t.Fatalf("the allocation record must snapshot the working-root declaration, got %v", allocation.WorkDirAllowlist)
	}
	if len(allocation.EnvironmentAllowlist) != 1 || allocation.EnvironmentAllowlist[0] != "MARSHAL_TRACE" {
		t.Fatalf("the allocation record must snapshot the environment declaration, got %v", allocation.EnvironmentAllowlist)
	}
	if _, err := fake.Provision(ctx, ProvisionRequest{
		Identity:         testIdentity("allocation-"+"bad-workdir", "command-provision-bad"),
		Requirements:     workspaceWriteRequirements(),
		WorkDirAllowlist: []string{"relative/dir"},
	}); !errors.Is(err, ErrInvalidWorkDir) {
		t.Fatalf("a relative workDirAllowlist entry must fail closed with ErrInvalidWorkDir, got %v", err)
	}
	if _, err := fake.Provision(ctx, ProvisionRequest{
		Identity:             testIdentity("allocation-"+"bad-env", "command-provision-bad-env"),
		Requirements:         workspaceWriteRequirements(),
		EnvironmentAllowlist: []string{"API_TOKEN"},
	}); !errors.Is(err, ErrCredentialKeyRejected) {
		t.Fatalf("a credential-semantic environmentAllowlist entry must fail closed with ErrCredentialKeyRejected, got %v", err)
	}
	for _, allocationId := range []string{"allocation-" + "bad-workdir", "allocation-" + "bad-env"} {
		if _, err := fake.Exec(ctx, ExecRequest{
			Identity:     testIdentity(allocationId, "command-exec-after-reject"),
			AllocationId: allocationId,
			Command:      []string{"echo-" + "rejected"},
		}); !errors.Is(err, ErrAllocationNotFound) {
			t.Fatalf("a rejected provision must never leave an allocation behind, got %v", err)
		}
	}
}

// TestFakeExecWorkingDirBinding freezes the fake provider's deterministic
// ADR 0055 §1 adjudication: declared working roots are honored, and
// undeclared or non-absolute requests fail closed.
func TestFakeExecWorkingDirBinding(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	allocation := provisionEnvelopeAllocation(t, fake, "allocation-"+"envelope-cwd", []string{"/opt/agent"}, nil)
	execOk := func(workingDir string) error {
		_, err := fake.Exec(ctx, ExecRequest{
			Identity:     testIdentity(allocation.AllocationId, "command-exec-"+workingDir),
			AllocationId: allocation.AllocationId,
			Command:      []string{"agent-cli"},
			WorkingDir:   workingDir,
		})
		return err
	}
	if err := execOk("/opt/agent"); err != nil {
		t.Fatalf("a declared working root must be honored, got %v", err)
	}
	if err := execOk("/opt/other"); !errors.Is(err, ErrInvalidWorkDir) {
		t.Fatalf("an undeclared working root must fail closed, got %v", err)
	}
	if err := execOk("opt/agent"); !errors.Is(err, ErrInvalidWorkDir) {
		t.Fatalf("a non-absolute working root must fail closed, got %v", err)
	}
	if !fakeLogContains(fakeInspectLogLines(t, fake, allocation.AllocationId), "exec cwd: /opt/agent") {
		t.Fatal("the honored working root must be recorded in the observation log")
	}
	// An allocation without any declaration rejects every WorkingDir.
	plainFake := NewFakeProvider(FakeConfig{})
	plain := provisionTestAllocation(t, plainFake, "allocation-"+"envelope-plain")
	if _, err := plainFake.Exec(ctx, ExecRequest{
		Identity:     testIdentity(plain.AllocationId, "command-exec-undeclared"),
		AllocationId: plain.AllocationId,
		Command:      []string{"agent-cli"},
		WorkingDir:   "/opt/agent",
	}); !errors.Is(err, ErrInvalidWorkDir) {
		t.Fatalf("a WorkingDir against an allocation without declaration must fail closed, got %v", err)
	}
}

// TestFakeExecEnvironmentAllowlist freezes the fake provider's ADR 0055 §2
// adjudication: allow-listed keys pass through deterministically, and
// unknown or credential-semantic keys fail closed.
func TestFakeExecEnvironmentAllowlist(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	allocation := provisionEnvelopeAllocation(t, fake, "allocation-"+"envelope-env", nil, []string{"MARSHAL_TRACE"})
	if _, err := fake.Exec(ctx, ExecRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-exec-env-ok"),
		AllocationId: allocation.AllocationId,
		Command:      []string{"agent-cli"},
		Environment:  map[string]string{"MARSHAL_TRACE": "1"},
	}); err != nil {
		t.Fatalf("an allow-listed environment key must be honored, got %v", err)
	}
	if !fakeLogContains(fakeInspectLogLines(t, fake, allocation.AllocationId), "exec env overlay: MARSHAL_TRACE=1") {
		t.Fatal("the allow-listed environment overlay must be recorded in the observation log")
	}
	if _, err := fake.Exec(ctx, ExecRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-exec-env-unknown"),
		AllocationId: allocation.AllocationId,
		Command:      []string{"agent-cli"},
		Environment:  map[string]string{"NOT_DECLARED": "x"},
	}); !errors.Is(err, ErrEnvKeyNotAllowed) {
		t.Fatalf("an environment key outside the allowlist must fail closed, got %v", err)
	}
	if _, err := fake.Exec(ctx, ExecRequest{
		Identity:     testIdentity(allocation.AllocationId, "command-exec-env-cred"),
		AllocationId: allocation.AllocationId,
		Command:      []string{"agent-cli"},
		Environment:  map[string]string{"MY_API_KEY": "x"},
	}); !errors.Is(err, ErrCredentialKeyRejected) {
		t.Fatalf("a credential-semantic environment key must fail closed at exec, got %v", err)
	}
	// An allocation without any declaration rejects every environment key.
	plainFake := NewFakeProvider(FakeConfig{})
	plain := provisionTestAllocation(t, plainFake, "allocation-"+"envelope-env-plain")
	if _, err := plainFake.Exec(ctx, ExecRequest{
		Identity:     testIdentity(plain.AllocationId, "command-exec-env-plain"),
		AllocationId: plain.AllocationId,
		Command:      []string{"agent-cli"},
		Environment:  map[string]string{"MARSHAL_TRACE": "1"},
	}); !errors.Is(err, ErrEnvKeyNotAllowed) {
		t.Fatalf("an environment key against an allocation without declaration must fail closed, got %v", err)
	}
}

// TestFakeExecTranscriptPolicy freezes the fake provider's deterministic
// transcript sink of ADR 0055 §3: a malformed policy fails closed, a
// cleanly completing capture is staged in memory with its digest recomputed
// and echoed in the receipt, and an overflowing capture kills the workload
// without a staged artifact or partial success.
func TestFakeExecTranscriptPolicy(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	allocation := provisionTestAllocation(t, fake, "allocation-"+"envelope-transcript")
	for _, tc := range []struct {
		name   string
		policy TranscriptPolicy
	}{
		{"zero-maxbytes", TranscriptPolicy{MaxBytes: 0, ArtifactId: "t.txt"}},
		{"empty-artifact", TranscriptPolicy{MaxBytes: 128, ArtifactId: ""}},
		{"traversal-artifact", TranscriptPolicy{MaxBytes: 128, ArtifactId: "../t.txt"}},
	} {
		if _, err := fake.Exec(ctx, ExecRequest{
			Identity:         testIdentity(allocation.AllocationId, "command-exec-tp-"+tc.name),
			AllocationId:     allocation.AllocationId,
			Command:          []string{"agent-cli"},
			TranscriptPolicy: tc.policy,
		}); !errors.Is(err, ErrInvalidTranscriptPolicy) {
			t.Fatalf("%s: a malformed transcript policy must fail closed with ErrInvalidTranscriptPolicy, got %v", tc.name, err)
		}
	}
	receipt, err := fake.Exec(ctx, ExecRequest{
		Identity:         testIdentity(allocation.AllocationId, "command-exec-tp-ok"),
		AllocationId:     allocation.AllocationId,
		Command:          []string{"agent-cli"},
		TranscriptPolicy: TranscriptPolicy{MaxBytes: 4096, ArtifactId: "transcripts/run-1.txt"},
	})
	if err != nil {
		t.Fatalf("a cleanly completing bounded capture must succeed: %v", err)
	}
	expectedStdout := []byte("stdout:" + "agent-cli")
	if receipt.TranscriptDigest != RecomputeSHA256(expectedStdout) {
		t.Fatalf("the receipt must echo the provider-recomputed transcript digest, got %q", receipt.TranscriptDigest)
	}
	if receipt.TranscriptStderrDigest != RecomputeSHA256([]byte("stderr:"+"agent-cli")) {
		t.Fatalf("the receipt must echo the stderr digest captured under the same policy, got %q", receipt.TranscriptStderrDigest)
	}
	if receipt.TranscriptDigest != receipt.StdoutSHA256 {
		t.Fatal("the transcript digest must equal the recomputed stdout digest of the deterministic capture")
	}
	if !fakeLogContains(fakeInspectLogLines(t, fake, allocation.AllocationId), "transcript staged: transcripts/run-1.txt") {
		t.Fatal("the staged transcript artifact must be recorded in the observation log")
	}
	// The staged transcript participates in the allocation's staged content
	// and changes the checkpoint digest deterministically.
	if _, err := fake.Checkpoint(ctx, CheckpointRequest{Identity: testIdentity(allocation.AllocationId, "command-checkpoint"), AllocationId: allocation.AllocationId}); err != nil {
		t.Fatalf("checkpoint over the staged transcript must succeed: %v", err)
	}
	oversize, err := fake.Exec(ctx, ExecRequest{
		Identity:         testIdentity(allocation.AllocationId, "command-exec-tp-oversize"),
		AllocationId:     allocation.AllocationId,
		Command:          []string{"agent-cli", "verbose"},
		TranscriptPolicy: TranscriptPolicy{MaxBytes: 4, ArtifactId: "transcripts/oversize.txt"},
	})
	if !errors.Is(err, ErrTranscriptLimitExceeded) {
		t.Fatalf("an overflowing capture must fail closed with ErrTranscriptLimitExceeded, got %v", err)
	}
	if oversize == nil || oversize.Status != ExecutionKilled {
		t.Fatalf("an overflowing capture must observe the killed status, got %+v", oversize)
	}
	if oversize.TranscriptDigest != "" {
		t.Fatal("no partial transcript digest may be reported on the overflow path")
	}
	inspectReport, err := fake.Inspect(ctx, InspectRequest{Identity: testIdentity(allocation.AllocationId, "command-inspect-oversize"), AllocationId: allocation.AllocationId})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if inspectReport.ExitCode != -1 {
		t.Fatalf("the overflow kill must be observable out-of-band, got exit code %d", inspectReport.ExitCode)
	}
}

// TestFakeExecTimeoutClamp freezes the fake provider's deterministic
// ADR 0055 §4 clamp: positive values are honored as min(TimeoutSeconds,
// the provider cap) and recorded in the observation log; no wall clock ever
// participates.
func TestFakeExecTimeoutClamp(t *testing.T) {
	ctx := context.Background()
	fake := NewFakeProvider(FakeConfig{})
	allocation := provisionTestAllocation(t, fake, "allocation-"+"envelope-timeout")
	for _, tc := range []struct {
		name      string
		seconds   int64
		effective int64
	}{
		{"below-cap", 5, 5},
		{"above-cap", 7200, fakeTimeoutCapSeconds},
	} {
		if _, err := fake.Exec(ctx, ExecRequest{
			Identity:       testIdentity(allocation.AllocationId, "command-exec-timeout-"+tc.name),
			AllocationId:   allocation.AllocationId,
			Command:        []string{"agent-cli"},
			TimeoutSeconds: tc.seconds,
		}); err != nil {
			t.Fatalf("%s: a bounded timeout request must be honored, got %v", tc.name, err)
		}
	}
	lines := fakeInspectLogLines(t, fake, allocation.AllocationId)
	if !fakeLogContains(lines, "exec timeout effective: 5s") {
		t.Fatal("the below-cap timeout must be honored verbatim")
	}
	if !fakeLogContains(lines, "exec timeout effective: 3600s") {
		t.Fatal("the above-cap timeout must clamp at the provider cap")
	}
}
