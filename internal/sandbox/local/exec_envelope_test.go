package local

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/sandbox"
)

// provisionEnvelopeAllocation provisions one allocation carrying the given
// ADR 0055 declarations.
func provisionEnvelopeAllocation(t *testing.T, runner *LocalRunner, name, allocationId string, workDirs, envKeys []string) sandbox.SandboxAllocation {
	t.Helper()
	receipt, err := runner.Provision(context.Background(), sandbox.ProvisionRequest{
		Identity:             scenarioIdentity(name, allocationId, "cmd-provision", 1),
		Requirements:         workspaceRequirements(t),
		WorkDirAllowlist:     workDirs,
		EnvironmentAllowlist: envKeys,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	return receipt.Allocation
}

// TestProvisionEnvelopeAllowlistRegistration freezes that the Local
// provider records the ADR 0055 declarations in the allocation record and
// rejects malformed declarations fail closed before any host side effect.
func TestProvisionEnvelopeAllowlistRegistration(t *testing.T) {
	ctx := context.Background()
	declared := t.TempDir()
	runner := newTestRunner(t, WithExecutor(newFakeExecutor().run))
	allocation := provisionEnvelopeAllocation(t, runner, "env-reg", "alloc-env-reg", []string{declared}, []string{"MARSHAL_TRACE"})
	if len(allocation.WorkDirAllowlist) != 1 || allocation.WorkDirAllowlist[0] != declared {
		t.Fatalf("the allocation record must snapshot the working-root declaration, got %v", allocation.WorkDirAllowlist)
	}
	if len(allocation.EnvironmentAllowlist) != 1 || allocation.EnvironmentAllowlist[0] != "MARSHAL_TRACE" {
		t.Fatalf("the allocation record must snapshot the environment declaration, got %v", allocation.EnvironmentAllowlist)
	}
	for _, tc := range []struct {
		name     string
		workDirs []string
		envKeys  []string
		sentinel error
	}{
		{"relative-workdir", []string{"relative/dir"}, nil, sandbox.ErrInvalidWorkDir},
		{"empty-workdir", []string{""}, nil, sandbox.ErrInvalidWorkDir},
		{"credential-env-key", nil, []string{"API_TOKEN"}, sandbox.ErrCredentialKeyRejected},
		{"malformed-env-key", nil, []string{"A=B"}, sandbox.ErrInvalidRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rejectedRunner := newTestRunner(t, WithExecutor(newFakeExecutor().run))
			allocationId := "alloc-reject-" + tc.name
			_, err := rejectedRunner.Provision(ctx, sandbox.ProvisionRequest{
				Identity:             scenarioIdentity("reject-"+tc.name, allocationId, "cmd-provision", 1),
				Requirements:         workspaceRequirements(t),
				WorkDirAllowlist:     tc.workDirs,
				EnvironmentAllowlist: tc.envKeys,
			})
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("the declaration must fail closed with %v, got %v", tc.sentinel, err)
			}
			if _, err := rejectedRunner.Exec(ctx, sandbox.ExecRequest{
				Identity:     scenarioIdentity("reject-"+tc.name, allocationId, "cmd-exec", 1),
				AllocationId: allocationId,
				Command:      []string{"plain-cmd"},
			}); !errors.Is(err, sandbox.ErrAllocationNotFound) {
				t.Fatalf("a rejected provision must never leave an allocation behind, got %v", err)
			}
		})
	}
}

// TestExecWorkingDirApproved freezes the honored path of ADR 0055 §1: a
// declared WorkingDir binds and becomes the execution cwd, while the
// allocation-directory cwd stays the untouched default.
func TestExecWorkingDirApproved(t *testing.T) {
	executor := newFakeExecutor()
	runner := newTestRunner(t, WithExecutor(executor.run))
	ctx := context.Background()
	declared := t.TempDir()
	allocation := provisionEnvelopeAllocation(t, runner, "approved", "alloc-approved", []string{declared}, nil)
	receipt, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:     scenarioIdentity("approved", allocation.AllocationId, "cmd-exec", 1),
		AllocationId: allocation.AllocationId,
		Command:      []string{"agent-cli"},
		WorkingDir:   declared,
	})
	if err != nil {
		t.Fatalf("a declared WorkingDir must be honored: %v", err)
	}
	if receipt.Status != sandbox.ExecutionCompleted || receipt.TranscriptDigest != "" {
		t.Fatalf("a zero-transcript execution keeps the ADR 0017 observation, got %+v", receipt)
	}
	resolved, err := filepath.EvalSymlinks(declared)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.specs) != 1 || executor.specs[0].Dir != resolved {
		t.Fatalf("the executor must run with the symlink-resolved declared root as cwd, got %+v", executor.specs)
	}
}

// TestExecWorkingDirRealProcessTranscript freezes the cwd effect through
// the real host executor: a declared WorkingDir becomes the process cwd,
// and the bounded transcript of `pwd` lands as a staged artifact of the
// allocation whose digest the receipt echoes.
func TestExecWorkingDirRealProcessTranscript(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()
	declared := t.TempDir()
	resolved, err := filepath.EvalSymlinks(declared)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	allocation := provisionEnvelopeAllocation(t, runner, "realcwd", "alloc-realcwd", []string{declared}, nil)
	receipt, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:         scenarioIdentity("realcwd", allocation.AllocationId, "cmd-exec", 1),
		AllocationId:     allocation.AllocationId,
		Command:          []string{"pwd"},
		WorkingDir:       declared,
		TranscriptPolicy: sandbox.TranscriptPolicy{MaxBytes: 4096, ArtifactId: "transcripts/pwd.txt"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if receipt.Status != sandbox.ExecutionCompleted {
		t.Fatalf("pwd must complete cleanly, got %s", string(receipt.Status))
	}
	expected := []byte(resolved + "\n")
	if receipt.TranscriptDigest != sandbox.RecomputeSHA256(expected) {
		t.Fatalf("the transcript must capture the declared cwd, got digest %q for %q", receipt.TranscriptDigest, expected)
	}
	artifactDir, err := runner.AllocationDirectory(allocation.AllocationId)
	if err != nil {
		t.Fatalf("AllocationDirectory: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(artifactDir, "transcripts", "pwd.txt"))
	if err != nil {
		t.Fatalf("the transcript artifact must exist inside the allocation: %v", err)
	}
	if !bytes.Equal(content, expected) {
		t.Fatalf("the staged transcript must hold the declared cwd, got %q", content)
	}
	if receipt.TranscriptDigest != receipt.StdoutSHA256 {
		t.Fatal("the recomputed transcript digest must equal the stdout digest of the capture")
	}
}

// TestExecWorkingDirRejected freezes the fail-closed negative paths of
// ADR 0055 §1, including soft-link traversal into an undeclared target.
func TestExecWorkingDirRejected(t *testing.T) {
	ctx := context.Background()
	declared := t.TempDir()
	outside := t.TempDir()
	linkOut := filepath.Join(declared, "link-out")
	if err := os.Symlink(outside, linkOut); err != nil {
		t.Fatalf("symlink fixture: %v", err)
	}
	regularFile := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(regularFile, []byte("not-a-dir"), 0o600); err != nil {
		t.Fatalf("regular file fixture: %v", err)
	}
	for _, tc := range []struct {
		name       string
		workingDir string
	}{
		{"relative", "relative/dir"},
		{"undeclared-existing", outside},
		{"nonexistent-absolute", filepath.Join(t.TempDir(), "absent")},
		{"symlink-traversal", linkOut},
		{"not-a-directory", regularFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := newFakeExecutor()
			runner := newTestRunner(t, WithExecutor(executor.run))
			allocation := provisionEnvelopeAllocation(t, runner, "wrej-"+tc.name, "alloc-wrej-"+tc.name, []string{declared}, nil)
			_, err := runner.Exec(ctx, sandbox.ExecRequest{
				Identity:     scenarioIdentity("wrej-"+tc.name, allocation.AllocationId, "cmd-exec", 1),
				AllocationId: allocation.AllocationId,
				Command:      []string{"agent-cli"},
				WorkingDir:   tc.workingDir,
			})
			if !errors.Is(err, sandbox.ErrInvalidWorkDir) {
				t.Fatalf("the WorkingDir must fail closed with ErrInvalidWorkDir, got %v", err)
			}
			executor.mu.Lock()
			defer executor.mu.Unlock()
			if len(executor.specs) != 0 {
				t.Fatal("a rejected WorkingDir must never reach the executor")
			}
		})
	}
	// An allocation without any declaration rejects every WorkingDir.
	executor := newFakeExecutor()
	runner := newTestRunner(t, WithExecutor(executor.run))
	plain := provisionAllocation(t, runner, "wrej-plain", "alloc-wrej-plain")
	if _, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:     scenarioIdentity("wrej-plain", plain.AllocationId, "cmd-exec", 1),
		AllocationId: plain.AllocationId,
		Command:      []string{"agent-cli"},
		WorkingDir:   declared,
	}); !errors.Is(err, sandbox.ErrInvalidWorkDir) {
		t.Fatalf("a WorkingDir against an allocation without declaration must fail closed, got %v", err)
	}
}

// TestExecAbsoluteArgvWithDeclaredWorkingDir freezes the ADR 0055 exec
// target rule: with a declared WorkingDir an absolute argv[0] that exists
// on disk is a legitimate target, a missing absolute target stays fail
// closed, and without a declared WorkingDir the ADR 0017 absolute-path
// rejection is unchanged.
func TestExecAbsoluteArgvWithDeclaredWorkingDir(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()
	declared := t.TempDir()
	allocation := provisionEnvelopeAllocation(t, runner, "absargv", "alloc-absargv", []string{declared}, nil)
	receipt, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:         scenarioIdentity("absargv", allocation.AllocationId, "cmd-exec-abs", 1),
		AllocationId:     allocation.AllocationId,
		Command:          []string{"/bin/echo", "envelope-ok"},
		WorkingDir:       declared,
		TranscriptPolicy: sandbox.TranscriptPolicy{MaxBytes: 4096, ArtifactId: "transcripts/echo.txt"},
	})
	if err != nil {
		t.Fatalf("an existing absolute argv[0] with a declared WorkingDir must execute: %v", err)
	}
	if receipt.Status != sandbox.ExecutionCompleted || receipt.TranscriptDigest != sandbox.RecomputeSHA256([]byte("envelope-ok\n")) {
		t.Fatalf("the transcript must capture the real process output, got %+v", receipt)
	}
	if _, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:     scenarioIdentity("absargv", allocation.AllocationId, "cmd-exec-missing", 1),
		AllocationId: allocation.AllocationId,
		Command:      []string{"/bin/marshal-definitely-absent-exec-target"},
		WorkingDir:   declared,
	}); !errors.Is(err, sandbox.ErrInvalidRequest) {
		t.Fatalf("a missing absolute argv[0] must fail closed even with a declared WorkingDir, got %v", err)
	}
	if _, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:     scenarioIdentity("absargv", allocation.AllocationId, "cmd-exec-nodecl", 1),
		AllocationId: allocation.AllocationId,
		Command:      []string{"/bin/echo", "envelope-no"},
	}); !errors.Is(err, sandbox.ErrInvalidRequest) {
		t.Fatalf("an absolute argv[0] without a declared WorkingDir must stay rejected, got %v", err)
	}
}

// TestExecEnvironmentAllowlist freezes the Local ADR 0055 §2 adjudication:
// allow-listed keys overlay onto the sanitized baseline deterministically,
// and unknown or credential-semantic keys fail closed before any spawn.
func TestExecEnvironmentAllowlist(t *testing.T) {
	executor := newFakeExecutor()
	runner := newTestRunner(t, WithExecutor(executor.run))
	ctx := context.Background()
	allocation := provisionEnvelopeAllocation(t, runner, "env-exec", "alloc-env-exec", nil, []string{"MARSHAL_TRACE"})
	if _, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:     scenarioIdentity("env-exec", allocation.AllocationId, "cmd-exec-ok", 1),
		AllocationId: allocation.AllocationId,
		Command:      []string{"agent-cli"},
		Environment:  map[string]string{"MARSHAL_TRACE": "1"},
	}); err != nil {
		t.Fatalf("an allow-listed environment key must be honored: %v", err)
	}
	executor.mu.Lock()
	spec := executor.specs[0]
	executor.mu.Unlock()
	if !slices.Contains(spec.Env, "MARSHAL_TRACE=1") || !slices.Contains(spec.Env, "LANG=C") {
		t.Fatalf("the overlay must land on top of the sanitized baseline, got %v", spec.Env)
	}
	for _, tc := range []struct {
		name     string
		env      map[string]string
		sentinel error
	}{
		{"unknown-key", map[string]string{"NOT_DECLARED": "x"}, sandbox.ErrEnvKeyNotAllowed},
		{"credential-key", map[string]string{"MY_SECRET": "x"}, sandbox.ErrCredentialKeyRejected},
	} {
		if _, err := runner.Exec(ctx, sandbox.ExecRequest{
			Identity:     scenarioIdentity("env-exec", allocation.AllocationId, "cmd-exec-"+tc.name, 1),
			AllocationId: allocation.AllocationId,
			Command:      []string{"agent-cli"},
			Environment:  tc.env,
		}); !errors.Is(err, tc.sentinel) {
			t.Fatalf("%s: must fail closed with %v, got %v", tc.name, tc.sentinel, err)
		}
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.specs) != 1 {
		t.Fatal("only the honored environment request may reach the executor")
	}
}

// TestExecTranscriptPolicyRejected freezes that a malformed transcript
// policy fails closed before any spawn.
func TestExecTranscriptPolicyRejected(t *testing.T) {
	executor := newFakeExecutor()
	runner := newTestRunner(t, WithExecutor(executor.run))
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "tp-reject", "alloc-tp-reject")
	for _, tc := range []struct {
		name   string
		policy sandbox.TranscriptPolicy
	}{
		{"zero-maxbytes", sandbox.TranscriptPolicy{MaxBytes: 0, ArtifactId: "t.txt"}},
		{"negative-maxbytes", sandbox.TranscriptPolicy{MaxBytes: -1, ArtifactId: "t.txt"}},
		{"empty-artifact", sandbox.TranscriptPolicy{MaxBytes: 64, ArtifactId: ""}},
		{"absolute-artifact", sandbox.TranscriptPolicy{MaxBytes: 64, ArtifactId: "/tmp/t.txt"}},
		{"traversal-artifact", sandbox.TranscriptPolicy{MaxBytes: 64, ArtifactId: "../t.txt"}},
	} {
		if _, err := runner.Exec(ctx, sandbox.ExecRequest{
			Identity:         scenarioIdentity("tp-reject", allocation.AllocationId, "cmd-exec-"+tc.name, 1),
			AllocationId:     allocation.AllocationId,
			Command:          []string{"agent-cli"},
			TranscriptPolicy: tc.policy,
		}); !errors.Is(err, sandbox.ErrInvalidTranscriptPolicy) {
			t.Fatalf("%s: must fail closed with ErrInvalidTranscriptPolicy, got %v", tc.name, err)
		}
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.specs) != 0 {
		t.Fatal("a rejected transcript policy must never reach the executor")
	}
}

// TestExecTranscriptBoundedCapture freezes the ADR 0055 §3 happy path: a
// cleanly completing capture is written as a content-addressed staged
// artifact with its digest recomputed from disk and echoed in the receipt,
// and stderr participates digest-only under the same bound.
func TestExecTranscriptBoundedCapture(t *testing.T) {
	executor := newFakeExecutor()
	stdout := []byte("bounded" + "-stdout" + "\n")
	stderr := []byte("bounded" + "-stderr" + "\n")
	executor.outcomes["agent-cli"] = ExecOutcome{Started: true, ExitCode: 0, Stdout: stdout, Stderr: stderr}
	runner := newTestRunner(t, WithExecutor(executor.run))
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "tp-ok", "alloc-tp-ok")
	receipt, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:         scenarioIdentity("tp-ok", allocation.AllocationId, "cmd-exec", 1),
		AllocationId:     allocation.AllocationId,
		Command:          []string{"agent-cli"},
		TranscriptPolicy: sandbox.TranscriptPolicy{MaxBytes: 4096, ArtifactId: "transcripts/run.txt"},
	})
	if err != nil {
		t.Fatalf("a cleanly completing bounded capture must succeed: %v", err)
	}
	if receipt.Status != sandbox.ExecutionCompleted {
		t.Fatalf("status must be completed, got %s", string(receipt.Status))
	}
	if receipt.TranscriptDigest != sandbox.RecomputeSHA256(stdout) {
		t.Fatalf("the receipt must echo the recomputed transcript digest, got %q", receipt.TranscriptDigest)
	}
	if receipt.TranscriptStderrDigest != sandbox.RecomputeSHA256(stderr) {
		t.Fatalf("the receipt must echo the stderr digest captured under the same bound, got %q", receipt.TranscriptStderrDigest)
	}
	artifactDir, err := runner.AllocationDirectory(allocation.AllocationId)
	if err != nil {
		t.Fatalf("AllocationDirectory: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(artifactDir, "transcripts", "run.txt"))
	if err != nil {
		t.Fatalf("the transcript artifact must exist inside the allocation: %v", err)
	}
	if !bytes.Equal(content, stdout) {
		t.Fatalf("the staged artifact must hold the exact stdout capture, got %q", content)
	}
	// The staged transcript is observable out-of-band through Checkpoint.
	checkpoint, err := runner.Checkpoint(ctx, sandbox.CheckpointRequest{
		Identity:     scenarioIdentity("tp-ok", allocation.AllocationId, "cmd-checkpoint", 1),
		AllocationId: allocation.AllocationId,
	})
	if err != nil {
		t.Fatalf("Checkpoint over the staged transcript must succeed: %v", err)
	}
	if checkpoint.SizeBytes == 0 {
		t.Fatal("the checkpoint must cover the staged transcript artifact")
	}
}

// TestExecTranscriptOversizeKill freezes the ADR 0055 §3.2 bound: an
// overflowing stdout capture kills the workload fail closed, returns the
// typed error with the killed status, and never stages a partial artifact.
func TestExecTranscriptOversizeKill(t *testing.T) {
	executor := newFakeExecutor()
	executor.outcomes["agent-cli"] = ExecOutcome{Started: true, ExitCode: 0, Stdout: bytes.Repeat([]byte("x"), 1024)}
	runner := newTestRunner(t, WithExecutor(executor.run))
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "tp-oversize", "alloc-tp-oversize")
	receipt, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:         scenarioIdentity("tp-oversize", allocation.AllocationId, "cmd-exec", 1),
		AllocationId:     allocation.AllocationId,
		Command:          []string{"agent-cli"},
		TranscriptPolicy: sandbox.TranscriptPolicy{MaxBytes: 64, ArtifactId: "transcripts/oversize.txt"},
	})
	if !errors.Is(err, sandbox.ErrTranscriptLimitExceeded) {
		t.Fatalf("an overflowing capture must fail closed with ErrTranscriptLimitExceeded, got %v", err)
	}
	if receipt == nil || receipt.Status != sandbox.ExecutionKilled {
		t.Fatalf("the overflow path must observe the killed status, got %+v", receipt)
	}
	if receipt.TranscriptDigest != "" || receipt.TranscriptStderrDigest != "" {
		t.Fatal("no partial transcript digests may be reported on the overflow path")
	}
	artifactDir, dirErr := runner.AllocationDirectory(allocation.AllocationId)
	if dirErr != nil {
		t.Fatalf("AllocationDirectory: %v", dirErr)
	}
	if _, statErr := os.Stat(filepath.Join(artifactDir, "transcripts", "oversize.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("no partial transcript artifact may be staged on the overflow path, stat err %v", statErr)
	}
	inspectReport, err := runner.Inspect(ctx, sandbox.InspectRequest{
		Identity:     scenarioIdentity("tp-oversize", allocation.AllocationId, "cmd-inspect", 1),
		AllocationId: allocation.AllocationId,
	})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if inspectReport.SpawnCount != 1 {
		t.Fatalf("the killed op must still be observed as spawned, got %d", inspectReport.SpawnCount)
	}
}

// TestExecTranscriptHostOverflowKill freezes the host-side enforcement of
// the transcript bound: the real process group is killed the moment the
// stdout capture exceeds the declared MaxBytes.
func TestExecTranscriptHostOverflowKill(t *testing.T) {
	runner := newTestRunner(t)
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "tp-host", "alloc-tp-host")
	receipt, err := runner.Exec(ctx, sandbox.ExecRequest{
		Identity:         scenarioIdentity("tp-host", allocation.AllocationId, "cmd-exec", 1),
		AllocationId:     allocation.AllocationId,
		Command:          []string{"yes", "transcript-overflow"},
		TranscriptPolicy: sandbox.TranscriptPolicy{MaxBytes: 1024, ArtifactId: "transcripts/flood.txt"},
	})
	if !errors.Is(err, sandbox.ErrTranscriptLimitExceeded) {
		t.Fatalf("the host executor must kill the workload at the transcript bound, got %v", err)
	}
	if receipt == nil || receipt.Status != sandbox.ExecutionKilled {
		t.Fatalf("the overflow kill must observe the killed status, got %+v", receipt)
	}
	if len(receipt.StdoutSHA256) == 0 {
		t.Fatal("the truncated capture digest is still a valid observation")
	}
}

// TestExecPerOpTimeout freezes ADR 0055 §4: a positive TimeoutSeconds takes
// effect as min(requested, the runner cap), a non-positive value keeps the
// runner default, and a timeout kills the workload onto the killed branch.
func TestExecPerOpTimeout(t *testing.T) {
	executor := newFakeExecutor()
	runner := newTestRunner(t, WithExecutor(executor.run))
	ctx := context.Background()
	allocation := provisionAllocation(t, runner, "timeout", "alloc-timeout")
	for _, tc := range []struct {
		name     string
		seconds  int64
		expected time.Duration
	}{
		{"default-kept", 0, defaultExecTimeout},
		{"negative-kept", -5, defaultExecTimeout},
		{"below-cap", 5, 5 * time.Second},
		{"above-cap-clamped", 999999, defaultExecTimeout},
	} {
		if _, err := runner.Exec(ctx, sandbox.ExecRequest{
			Identity:       scenarioIdentity("timeout", allocation.AllocationId, "cmd-exec-"+tc.name, 1),
			AllocationId:   allocation.AllocationId,
			Command:        []string{"agent-cli"},
			TimeoutSeconds: tc.seconds,
		}); err != nil {
			t.Fatalf("%s: a bounded timeout request must be honored: %v", tc.name, err)
		}
		executor.mu.Lock()
		spec := executor.specs[len(executor.specs)-1]
		executor.mu.Unlock()
		if spec.Timeout != tc.expected {
			t.Fatalf("%s: effective timeout must be %v, got %v", tc.name, tc.expected, spec.Timeout)
		}
	}
	// The real host kill path: the runner cap overrides the per-op request
	// and the killed status is the observation, never a normal completion.
	hostRunner := newTestRunner(t, WithExecTimeout(150*time.Millisecond))
	hostAllocation := provisionAllocation(t, hostRunner, "timeout-host", "alloc-timeout-host")
	receipt, err := hostRunner.Exec(ctx, sandbox.ExecRequest{
		Identity:       scenarioIdentity("timeout-host", hostAllocation.AllocationId, "cmd-exec", 1),
		AllocationId:   hostAllocation.AllocationId,
		Command:        []string{"sleep", "5"},
		TimeoutSeconds: 120,
	})
	if err != nil {
		t.Fatalf("a timeout shows as an observation, not an SPI error: %v", err)
	}
	if receipt.Status != sandbox.ExecutionKilled {
		t.Fatalf("a timed-out workload must observe the killed status, got %s", string(receipt.Status))
	}
	if receipt.TranscriptDigest != "" {
		t.Fatal("a timed-out op never stages a transcript artifact")
	}
}
