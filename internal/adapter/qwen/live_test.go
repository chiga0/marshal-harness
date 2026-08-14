package qwen

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
)

// TestLiveQwen is opt-in because it consumes the locally configured model
// provider. It never commits, pushes, publishes, or touches the source repo.
func TestLiveQwen(t *testing.T) {
	executable := os.Getenv("MARSHAL_LIVE_QWEN_PATH")
	if executable == "" {
		t.Skip("set MARSHAL_LIVE_QWEN_PATH to run the live adapter E2E")
	}
	validator := newValidator(t)
	adapter, err := New(executable, validator)
	if err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	if output, err := exec.Command("git", "-C", worktree, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	controlRoot := t.TempDir()
	controlRoot, err = filepath.EvalSymlinks(controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(controlRoot, "output", "worker-result.json")
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o700); err != nil {
		t.Fatal(err)
	}
	taskPath := filepath.Join(controlRoot, "input", "task-spec.json")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, []byte(`{"worker":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := "在当前仓库创建 hello.txt，内容必须恰好为 marshal-live-e2e 加换行。不要提交、推送或访问网络。完成后把下面 JSON 原样写到 " + resultPath + `：
{"apiVersion":"marshal.dev/v1alpha1","kind":"WorkerResult","taskId":"TASK-LIVE","runId":"run-live","attemptId":"attempt-live","adapter":{"id":"qwen","executable":"declared","version":"declared"},"status":"completed","summary":"created hello.txt","declaredChangedFiles":["hello.txt"],"declaredArtifacts":[],"declaredCommands":[],"declaredRisks":[],"startedAt":"2026-08-04T00:00:00Z","completedAt":"2026-08-04T00:00:01Z"}`
	promptPath := filepath.Join(controlRoot, "input", "prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}
	request := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerRequest", "taskId": "TASK-LIVE", "runId": "run-live", "attemptId": "attempt-live", "attemptNumber": 1,
		"specDigest": digest("a"), "policyDigest": digest("b"), "capabilityDigest": digest("c"), "baseSha": strings.Repeat("1", 40),
		"worktreePath": worktree, "controlRoot": controlRoot, "taskSpecPath": "input/task-spec.json", "promptPath": "input/prompt.md", "resultPath": "output/worker-result.json",
		"adapterId": "qwen", "executionProfile": "workspace-write", "sessionPolicy": "ephemeral", "attemptTimeoutSeconds": 180, "maxOutputBytes": 5 << 20, "reviewFindings": []any{},
	}
	data, _ := json.Marshal(request)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := adapter.Run(ctx, domain.Record{Kind: domain.KindWorkerRequest, Data: data})
	if err != nil {
		transcript, _ := os.ReadFile(filepath.Join(controlRoot, "output", "qwen-transcript.jsonl"))
		metadata, _ := os.ReadFile(filepath.Join(controlRoot, "output", "qwen-transcript-meta.json"))
		t.Fatalf("%v\nmetadata=%s\ntranscript=%s", err, metadata, transcript)
	}
	if err := validator.Validate(domain.KindWorkerResult, result.Data); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(worktree, "hello.txt"))
	if err != nil || string(content) != "marshal-live-e2e\n" {
		t.Fatalf("hello.txt=%q err=%v", content, err)
	}
}

// TestLiveProbeVersionSupported is the probe-only live gate: it resolves
// the real host binary through MARSHAL_LIVE_QWEN_PATH first and PATH
// second, then proves Probe reports it as a supported version inside the
// supported set without running a full attempt. It stays fully separate
// from the E2E test above and skips when no binary can be resolved or when
// MARSHAL_SKIP_LIVE_PROBE=1 exempts the version judgment; both skip paths
// share the same skipped outcome.
func TestLiveProbeVersionSupported(t *testing.T) {
	liveProbeGate(t)
}

// liveProbeGate implements the probe-only live gate for
// TestLiveProbeVersionSupported and the exemption fixtures. Binary
// resolution, executability and version parsing run exactly as before and
// are never exempted; the MARSHAL_SKIP_LIVE_PROBE exemption applies only to
// the version-membership judgment, after a successful probe, and shares the
// same skipped outcome as the missing-binary path.
func liveProbeGate(t *testing.T) {
	t.Helper()
	executable := os.Getenv("MARSHAL_LIVE_QWEN_PATH")
	if executable == "" {
		resolved, err := exec.LookPath("qwen")
		if err != nil {
			t.Skip("set MARSHAL_LIVE_QWEN_PATH or install qwen on PATH to run the live probe")
		}
		executable = resolved
	}
	snapshot, err := probeCapabilitySnapshot(executable)
	if err != nil {
		t.Fatal(err)
	}
	if liveProbeExempted() {
		t.Skip(liveProbeSkipReason)
	}
	if err := supportedVersionGateFailure(snapshot); err != nil {
		t.Fatal(err)
	}
}

// probeCapabilitySnapshot constructs the adapter for executable and returns
// its validated CapabilitySnapshot. Executability and version-parse failures
// surface here and are never exempted by MARSHAL_SKIP_LIVE_PROBE.
func probeCapabilitySnapshot(executable string) (map[string]any, error) {
	validator, err := contract.NewValidator()
	if err != nil {
		return nil, err
	}
	adapter, err := New(executable, validator)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	record, err := adapter.Probe(ctx)
	if err != nil {
		return nil, err
	}
	var snapshot map[string]any
	if err := json.Unmarshal(record.Data, &snapshot); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// supportedVersionGateFailure returns the probe-only live gate failure for a
// probed snapshot, or nil when the probed version belongs to the closed
// supported set and the probe reported no errors. Its messages are the
// frozen gate messages.
func supportedVersionGateFailure(snapshot map[string]any) error {
	status, _ := snapshot["probeStatus"].(string)
	version, _ := snapshot["binaryVersion"].(string)
	if status != "supported" || !slices.Contains(supportedBinaries, version) {
		return fmt.Errorf("live probe = %s/%s, want a supported version inside %v", status, version, supportedBinaries)
	}
	probeErrors, _ := snapshot["probeErrors"].([]any)
	if len(probeErrors) != 0 {
		return fmt.Errorf("live probeErrors = %v", probeErrors)
	}
	return nil
}

// TestLiveProbeGateExemption pins the MARSHAL_SKIP_LIVE_PROBE exemption
// discipline for the probe-only live gate: the exact value "1" exempts only
// the version-membership judgment with a searchable skip reason; every other
// value leaves the gate's frozen behavior untouched; executability and
// version-parse failures are never masked; and the closed supported set
// never widens.
func TestLiveProbeGateExemption(t *testing.T) {
	// runGate drives liveProbeGate inside a subtest with a pinned binary and
	// exemption value, then reports the gate's final skipped/failed state.
	// It must only be used for scenarios whose gate outcome is pass or skip:
	// a failing gate would fail the surrounding test.
	runGate := func(t *testing.T, envValue, binary string) (skipped, failed bool) {
		t.Helper()
		t.Setenv("MARSHAL_LIVE_QWEN_PATH", binary)
		t.Setenv(liveProbeExemptionEnv, envValue)
		var gate *testing.T
		t.Run("gate", func(sub *testing.T) {
			gate = sub
			liveProbeGate(sub)
		})
		if gate == nil {
			t.Fatal("gate subtest did not run")
		}
		return gate.Skipped(), gate.Failed()
	}
	t.Run("predicate-recognizes-only-exact-one", func(t *testing.T) {
		for _, value := range []string{"", "0", "true", "yes", " 1", "1 ", "01", "2", "11"} {
			t.Setenv(liveProbeExemptionEnv, value)
			if liveProbeExempted() {
				t.Fatalf("value %q must not exempt the live probe gate", value)
			}
		}
		t.Setenv(liveProbeExemptionEnv, "1")
		if !liveProbeExempted() {
			t.Fatal("exact value \"1\" must exempt the live probe gate")
		}
	})
	t.Run("skip-reason-carries-searchable-marker", func(t *testing.T) {
		if !strings.Contains(liveProbeSkipReason, "skipped: "+liveProbeExemptionEnv) {
			t.Fatalf("exemption reason must carry the searchable marker: %q", liveProbeSkipReason)
		}
	})
	t.Run("exempted-unsupported-binary-skips-membership-judgment", func(t *testing.T) {
		skipped, failed := runGate(t, "1", fakeExecutable(t, "9.9.9", "", "", "exit 0"))
		if !skipped || failed {
			t.Fatalf("exempted gate must skip an unsupported binary: skipped=%v failed=%v", skipped, failed)
		}
	})
	t.Run("exempted-supported-binary-still-skips", func(t *testing.T) {
		skipped, failed := runGate(t, "1", fakeExecutable(t, supportedBinary, "", "", "exit 0"))
		if !skipped || failed {
			t.Fatalf("exempted gate must deterministically skip: skipped=%v failed=%v", skipped, failed)
		}
	})
	t.Run("non-exempted-supported-binary-still-passes", func(t *testing.T) {
		// An unset variable reads as "" through os.Getenv, so the empty
		// value also covers the unset case.
		for _, envValue := range []string{"", "0", "true", " 1", "01"} {
			t.Run(fmt.Sprintf("env=%q", envValue), func(t *testing.T) {
				skipped, failed := runGate(t, envValue, fakeExecutable(t, supportedBinary, "", "", "exit 0"))
				if skipped || failed {
					t.Fatalf("non-exempted gate must run and pass: skipped=%v failed=%v", skipped, failed)
				}
			})
		}
	})
	t.Run("non-exempted-unsupported-binary-keeps-frozen-failure", func(t *testing.T) {
		snapshot, err := probeCapabilitySnapshot(fakeExecutable(t, "9.9.9", "", "", "exit 0"))
		if err != nil {
			t.Fatal(err)
		}
		got := supportedVersionGateFailure(snapshot)
		want := fmt.Sprintf("live probe = unsupported/9.9.9, want a supported version inside %v", supportedBinaries)
		if got == nil || got.Error() != want {
			t.Fatalf("gate failure = %v, want frozen message %q", got, want)
		}
	})
	t.Run("exemption-never-masks-non-version-failures", func(t *testing.T) {
		t.Setenv(liveProbeExemptionEnv, "1")
		notExecutable := filepath.Join(t.TempDir(), "qwen")
		if err := os.WriteFile(notExecutable, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := probeCapabilitySnapshot(notExecutable); err == nil || !strings.Contains(err.Error(), "executable regular file") {
			t.Fatalf("non-executable binary must fail despite the exemption: %v", err)
		}
		if _, err := probeCapabilitySnapshot(fakeExecutable(t, "garbage-output", "", "", "exit 0")); err == nil || !strings.Contains(err.Error(), "unrecognized version") {
			t.Fatalf("unparseable version must fail despite the exemption: %v", err)
		}
	})
	t.Run("exemption-never-widens-closed-supported-set", func(t *testing.T) {
		t.Setenv(liveProbeExemptionEnv, "1")
		locked := []string{"0.21.5", "0.21.10", "0.21.11"}
		if !slices.Equal(supportedBinaries, locked) {
			t.Fatalf("supportedBinaries drifted under exemption: %v", supportedBinaries)
		}
		for _, version := range locked {
			if !isSupportedBinary(version) {
				t.Fatalf("supported version %s lost membership under exemption", version)
			}
		}
		for _, version := range []string{"0.21.4", "0.21.12", "9.9.9"} {
			if isSupportedBinary(version) {
				t.Fatalf("version %s must stay outside the closed set under exemption", version)
			}
		}
	})
}
