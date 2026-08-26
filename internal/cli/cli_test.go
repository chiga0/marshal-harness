package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/planning"
	marshalRepository "github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/runstore"
	marshalSchemas "github.com/chiga0/marshal-harness/schemas"
)

func TestInternalLaunchIsHiddenAndFailClosed(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"__launch"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("missing path exit = %d", exit)
	}
	stderr.Reset()
	secretPath := "/private/secret/launch.json"
	if exit := Run([]string{"__launch", secretPath}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure {
		t.Fatalf("invalid envelope exit = %d", exit)
	}
	if strings.Contains(stderr.String(), secretPath) || strings.Contains(stderr.String(), "private/secret") {
		t.Fatalf("internal launch leaked path: %s", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"help"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("help exit = %d", exit)
	}
	if strings.Contains(stdout.String(), "__launch") {
		t.Fatal("internal launch appeared in public help")
	}
}

func TestDoctorReportsCompiledContracts(t *testing.T) {
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_QODER_PATH", "")
	t.Setenv("MARSHAL_CODEX_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")

	var stdout, stderr bytes.Buffer
	exitCode := Run([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exitCode != ExitOK {
		t.Fatalf("Run() exit = %d, stderr = %s", exitCode, stderr.String())
	}
	var report struct {
		Status          string `json:"status"`
		ContractSchemas int    `json:"contractSchemas"`
		WorkerAdapters  int    `json:"workerAdapters"`
		Milestone       string `json:"milestone"`
		Workers         []struct {
			AdapterID     string `json:"adapterId"`
			Outcome       string `json:"outcome"`
			Compatibility string `json:"compatibility"`
		} `json:"workers"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode doctor output: %v", err)
	}
	if report.Status != "ok" || report.ContractSchemas != 22 || report.WorkerAdapters != 0 || report.Milestone != buildinfo.Milestone || len(report.Workers) != 5 {
		t.Fatalf("doctor report = %+v", report)
	}
	for index, adapterID := range []string{"opencode", "qwen", "qoder", "codex", "pi"} {
		if report.Workers[index].AdapterID != adapterID || report.Workers[index].Outcome != app.WorkerOutcomeNotConfigured || report.Workers[index].Compatibility != "not-probed" {
			t.Fatalf("doctor worker %d = %+v", index, report.Workers[index])
		}
	}
}

func TestDoctorReportsCompatibilityWithoutLocalDetails(t *testing.T) {
	for _, test := range []struct {
		name, version, compatibility string
		exit                         int
	}{
		{name: "supported", version: "1.18.13", compatibility: "supported", exit: 0},
		{name: "unsupported", version: "1.19.0", compatibility: "unsupported", exit: 0},
		{name: "probe failure", version: "top-secret-version", compatibility: "probe-failed", exit: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable := filepath.Join(t.TempDir(), "secret-opencode-location")
			script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s'\nexit %d\n", test.version, test.exit)
			if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MARSHAL_OPENCODE_PATH", executable)
			t.Setenv("MARSHAL_QWEN_PATH", "")
			t.Setenv("MARSHAL_QODER_PATH", "")
			t.Setenv("MARSHAL_CODEX_PATH", "")
			t.Setenv("MARSHAL_PI_PATH", "")

			var stdout, stderr bytes.Buffer
			exitCode := Run([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr)
			if exitCode != ExitOK {
				t.Fatalf("doctor exit = %d, stderr = %s", exitCode, stderr.String())
			}
			var report struct {
				Workers []doctorWorker `json:"workers"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Workers) != 5 || report.Workers[0].AdapterID != "opencode" || report.Workers[0].Compatibility != test.compatibility {
				t.Fatalf("workers = %+v", report.Workers)
			}
			output := stdout.String() + stderr.String()
			if strings.Contains(output, executable) || strings.Contains(output, "secret-opencode-location") || strings.Contains(output, "top-secret-version") {
				t.Fatalf("doctor leaked local detail: %s", output)
			}
			if test.compatibility != "probe-failed" && report.Workers[0].BinaryVersion != test.version {
				t.Fatalf("binary version = %q, want %q", report.Workers[0].BinaryVersion, test.version)
			}
		})
	}
}

func TestDoctorReportsCodex01491OrdinaryUserPlatformCompatibility(t *testing.T) {
	executable := writeVersionExecutableForCLI(t, "codex", "codex-cli 0.149.1")
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_QODER_PATH", "")
	t.Setenv("MARSHAL_CODEX_PATH", executable)
	t.Setenv("MARSHAL_CODEX_MODE", "ordinary-user")
	t.Setenv("MARSHAL_CODEX_AUTHORITY_CONFIG", "")
	t.Setenv("MARSHAL_PI_PATH", "")

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("doctor exit = %d, stderr = %s", exit, stderr.String())
	}
	var report struct {
		Workers []doctorWorker `json:"workers"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	var found *doctorWorker
	for index := range report.Workers {
		if report.Workers[index].AdapterID == "codex" {
			found = &report.Workers[index]
			break
		}
	}
	wantCompatibility := "probe-failed"
	if runtime.GOOS == "darwin" {
		wantCompatibility = "supported"
	}
	if found == nil || !found.Registered || found.Outcome != app.WorkerOutcomeRegistered || found.Compatibility != wantCompatibility || found.BinaryVersion != "0.149.1" || found.AuthorityMode != "ordinary-user" {
		t.Fatalf("doctor codex 0.149.1 = %+v", found)
	}
	if strings.Contains(stdout.String()+stderr.String(), executable) {
		t.Fatalf("doctor leaked configured executable path: %s", stdout.String()+stderr.String())
	}
}

func TestDoctorReportsPi0843Compatibility(t *testing.T) {
	executable := writeVersionExecutableForCLI(t, "pi", "0.84.3")
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_QODER_PATH", "")
	t.Setenv("MARSHAL_CODEX_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", executable)

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("doctor exit = %d, stderr = %s", exit, stderr.String())
	}
	var report struct {
		Workers []doctorWorker `json:"workers"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	var found *doctorWorker
	for index := range report.Workers {
		if report.Workers[index].AdapterID == "pi" {
			found = &report.Workers[index]
			break
		}
	}
	if found == nil || !found.Registered || found.Outcome != app.WorkerOutcomeRegistered || found.Compatibility != "supported" || found.AdapterVersion != "0.4.0" || found.BinaryVersion != "0.84.3" {
		t.Fatalf("doctor pi 0.84.3 = %+v", found)
	}
	if strings.Contains(stdout.String()+stderr.String(), executable) {
		t.Fatalf("doctor leaked configured executable path: %s", stdout.String()+stderr.String())
	}
}

func TestDoctorBindsSupportedQoderConformanceMetadata(t *testing.T) {
	identity := doctorSnapshotIdentity{
		AdapterID: "qoder", AdapterVersion: "0.1.0", BinaryVersion: "1.1.23", ExecutableDigest: "sha256:" + strings.Repeat("d", 64), ProbeStatus: "supported",
		ConformanceEvidenceDigest: "sha256:" + strings.Repeat("a", 64), ConformanceTrustRootKeyID: "root-1",
		ConformanceProbeProfileDigest: "sha256:" + strings.Repeat("b", 64), ConformanceValidUntil: "2026-08-18T01:00:00Z",
		ConformanceHostFingerprint: "sha256:" + strings.Repeat("c", 64), ConformanceAuthorityGeneration: 7,
	}
	result := doctorWorker{AdapterID: "qoder", Compatibility: "probe-failed"}
	applyDoctorSnapshotIdentity(&result, identity)
	if result.Compatibility != "supported" || result.ExecutableDigest != identity.ExecutableDigest || result.ConformanceEvidenceDigest != identity.ConformanceEvidenceDigest || result.ConformanceTrustRootKeyID != identity.ConformanceTrustRootKeyID || result.ConformanceProbeProfileDigest != identity.ConformanceProbeProfileDigest || result.ConformanceValidUntil != identity.ConformanceValidUntil || result.ConformanceHostFingerprint != identity.ConformanceHostFingerprint || result.ConformanceAuthorityGeneration != 7 {
		t.Fatalf("doctor metadata = %+v", result)
	}
	identity.ConformanceEvidenceDigest = ""
	result = doctorWorker{AdapterID: "qoder", Compatibility: "probe-failed"}
	applyDoctorSnapshotIdentity(&result, identity)
	if result.Compatibility != "probe-failed" {
		t.Fatalf("doctor accepted incomplete qoder metadata: %+v", result)
	}
}

func TestDoctorBindsCodexFailClosedMetadata(t *testing.T) {
	failure := json.RawMessage(`{"schemaVersion":"marshal.adapter-failure.v1","adapterId":"codex"}`)
	identity := doctorSnapshotIdentity{AdapterID: "codex", AdapterVersion: "0.1.0", BinaryVersion: "0.145.0", ProbeStatus: "unsupported", AdapterFailure: failure}
	result := doctorWorker{AdapterID: "codex", Compatibility: "probe-failed"}
	applyDoctorSnapshotIdentity(&result, identity)
	if result.Compatibility != "unsupported" || string(result.AdapterFailure) != string(failure) || len(result.CodexAuthority) != 0 {
		t.Fatalf("doctor codex failure metadata = %+v", result)
	}
	identity.AdapterFailure = nil
	result = doctorWorker{AdapterID: "codex", Compatibility: "probe-failed"}
	applyDoctorSnapshotIdentity(&result, identity)
	if result.Compatibility != "probe-failed" {
		t.Fatalf("doctor accepted missing codex failure metadata: %+v", result)
	}
}

func TestDoctorBindsCodexSupportedMetadataWithEqualityGuard(t *testing.T) {
	digest := func(char string) string { return "sha256:" + strings.Repeat(char, 64) }
	authority := json.RawMessage(`{"evidenceDigest":"` + digest("a") + `","trustRootKeyId":"root-1","profileDigest":"` + digest("b") + `","validUntil":"2026-08-19T01:00:00Z","hostIdentityDigest":"` + digest("c") + `","authorityGeneration":7}`)
	identity := doctorSnapshotIdentity{
		AdapterID: "codex", AdapterVersion: "0.1.0", BinaryVersion: "0.145.0", ExecutableDigest: digest("e"), ProbeStatus: "supported", CodexAuthority: authority,
		ConformanceEvidenceDigest: digest("a"), ConformanceTrustRootKeyID: "root-1", ConformanceProbeProfileDigest: digest("b"),
		ConformanceValidUntil: "2026-08-19T01:00:00Z", ConformanceHostFingerprint: digest("c"), ConformanceAuthorityGeneration: 7,
	}
	result := doctorWorker{AdapterID: "codex", Compatibility: "probe-failed"}
	applyDoctorSnapshotIdentity(&result, identity)
	if result.Compatibility != "supported" || result.ExecutableDigest != identity.ExecutableDigest || result.ConformanceEvidenceDigest != identity.ConformanceEvidenceDigest || result.ConformanceTrustRootKeyID != identity.ConformanceTrustRootKeyID || result.ConformanceProbeProfileDigest != identity.ConformanceProbeProfileDigest || result.ConformanceValidUntil != identity.ConformanceValidUntil || result.ConformanceHostFingerprint != identity.ConformanceHostFingerprint || result.ConformanceAuthorityGeneration != 7 {
		t.Fatalf("doctor codex metadata = %+v", result)
	}
	identity.ConformanceEvidenceDigest = digest("d")
	result = doctorWorker{AdapterID: "codex", Compatibility: "probe-failed"}
	applyDoctorSnapshotIdentity(&result, identity)
	if result.Compatibility != "probe-failed" {
		t.Fatalf("doctor accepted divergent codex metadata: %+v", result)
	}
}

func TestDoctorCanceledContextDoesNotProbeWorkers(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "probed")
	executable := filepath.Join(t.TempDir(), "opencode")
	script := "#!/bin/sh\n: > \"" + marker + "\"\nprintf '1.18.13\\n'\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_OPENCODE_PATH", executable)
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_QODER_PATH", "")
	t.Setenv("MARSHAL_CODEX_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if exit := RunContext(ctx, []string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("doctor exit = %d, stderr = %s", exit, stderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("doctor probed after cancellation: %v", err)
	}
	var report struct {
		Workers []doctorWorker `json:"workers"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Workers) != 5 || report.Workers[0].Compatibility != "not-probed" {
		t.Fatalf("workers = %+v", report.Workers)
	}
}

func TestDoctorRejectsInvalidRunBeforeWorkerProbe(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "probed")
	executable := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n: > \""+marker+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_OPENCODE_PATH", executable)
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"doctor", "--run", "../escape", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
		t.Fatalf("doctor exit = %d, stderr = %s", exit, stderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("invalid request probed worker: %v", err)
	}
}

func TestDoctorRunReconcilesEvidenceAndBlocksCorruption(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	repositoryRoot, err = filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	const remoteURL = "https://example.invalid/marshal-doctor-run.git"
	runGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}
	executable := filepath.Join(t.TempDir(), "qwen")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '0.21.11\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	configureQwenAuthFixture(t)
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", executable)
	t.Setenv("MARSHAL_PI_PATH", "")
	const (
		taskID = "doctor-run-task"
		runID  = "doctor-run-01"
	)
	taskPath := filepath.Join(t.TempDir(), "task.json")
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	writeCLIFixture(t, taskPath, cliPlanningTask(t, repositoryRoot, taskID, remoteURL))
	writeCLIFixture(t, policyPath, cliPlanningPolicy(t, taskID, runID))
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("plan exit = %d, stderr = %s", exit, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"doctor", "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("doctor exit = %d, stdout = %s, stderr = %s", exit, stdout.String(), stderr.String())
	}
	var healthy doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &healthy); err != nil {
		t.Fatal(err)
	}
	if healthy.Status != "ok" || healthy.Run == nil || healthy.Run.Status != "ok" || len(healthy.Run.Findings) != 0 {
		t.Fatalf("healthy report = %+v", healthy)
	}

	capabilityPath := filepath.Join(repositoryRoot, ".marshal", "runs", runID, "capability-snapshot.json")
	capabilityData, err := os.ReadFile(capabilityPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capabilityPath, []byte(`{"secret":"must-not-leak"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"doctor", "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure {
		t.Fatalf("corrupt doctor exit = %d, stderr = %s", exit, stderr.String())
	}
	var corrupt doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &corrupt); err != nil {
		t.Fatal(err)
	}
	if corrupt.Status != "blocked" || corrupt.Run == nil {
		t.Fatalf("corrupt report = %+v", corrupt)
	}
	found := false
	for _, finding := range corrupt.Run.Findings {
		found = found || finding.Code == "capability-snapshot-invalid"
	}
	if !found || strings.Contains(stdout.String()+stderr.String(), "must-not-leak") || strings.Contains(stdout.String()+stderr.String(), capabilityPath) {
		t.Fatalf("unsafe corrupt report: %s%s", stdout.String(), stderr.String())
	}

	if err := os.WriteFile(capabilityPath, capabilityData, 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(repositoryRoot, ".marshal", "runs", runID, "state.json")
	if err := os.WriteFile(statePath, []byte(`{"secret":"damaged-state"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"doctor", "--run", runID, "--repair", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("repair exit = %d, stdout = %s, stderr = %s", exit, stdout.String(), stderr.String())
	}
	var repaired doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &repaired); err != nil {
		t.Fatal(err)
	}
	if repaired.Status != "ok" || repaired.Run == nil || repaired.Run.Status != "ok" || repaired.Repair == nil || repaired.Repair.Outcome != "applied" || repaired.Repair.EventID == "" {
		t.Fatalf("repair report = %+v", repaired)
	}
	if strings.Contains(stdout.String()+stderr.String(), "damaged-state") || strings.Contains(stdout.String()+stderr.String(), statePath) {
		t.Fatalf("repair leaked damaged state: %s%s", stdout.String(), stderr.String())
	}
}

func TestContractValidateFromStandardInput(t *testing.T) {
	t.Parallel()

	data, err := marshalSchemas.FS.ReadFile("examples/happy-path/task-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := Run([]string{"contract", "validate", "-"}, bytes.NewReader(data), &stdout, &stderr)
	if exitCode != ExitOK {
		t.Fatalf("Run() exit = %d, stderr = %s", exitCode, stderr.String())
	}
	if got := stdout.String(); got != "有效：Task\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestContractValidateWithExplicitSchema(t *testing.T) {
	t.Parallel()

	task, err := marshalSchemas.FS.ReadFile("examples/happy-path/task-spec.json")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := Run([]string{"contract", "validate", "--schema", "task-spec", "-"}, bytes.NewReader(task), &stdout, &stderr)
	if exitCode != ExitOK {
		t.Fatalf("valid Task exit = %d, stderr = %s", exitCode, stderr.String())
	}

	runState, err := marshalSchemas.FS.ReadFile("examples/happy-path/run-state.json")
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	exitCode = Run([]string{"contract", "validate", "--schema", "task-spec", "-"}, bytes.NewReader(runState), &stdout, &stderr)
	if exitCode != ExitFailure {
		t.Fatalf("mismatched RunState exit = %d, want %d", exitCode, ExitFailure)
	}
}

func TestReadBoundedRejectsOversizedInput(t *testing.T) {
	t.Parallel()

	if _, err := readBounded(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("readBounded accepted oversized input")
	}
}

func TestPatchCaptureLimitUsesSafeDefault(t *testing.T) {
	if got := patchCaptureLimit(0); got != 64<<20 {
		t.Fatalf("default patch capture limit = %d", got)
	}
	if got := patchCaptureLimit(99); got != 100 {
		t.Fatalf("bounded patch capture limit = %d", got)
	}
}

func TestTaskSkeletonHasNoFilesystemSideEffects(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	temporaryDirectory := t.TempDir()
	if err := os.Chdir(temporaryDirectory); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	for _, command := range taskCommands {
		if command == "plan" || command == "approve" || command == "run" || command == "status" || command == "verify" || command == "review" || command == "publish" || command == "accept" || command == "reconcile" || command == "cleanup" || command == "abort" || command == "migrate-outcomes" {
			continue
		}
		var stdout, stderr bytes.Buffer
		exitCode := Run([]string{"task", command}, strings.NewReader(""), &stdout, &stderr)
		want := ExitUnavailable
		if command == "scaffold" {
			want = ExitUsage
		}
		if exitCode != want {
			t.Fatalf("task %s exit = %d, want %d", command, exitCode, want)
		}
	}
	if _, err := os.Stat(filepath.Join(temporaryDirectory, ".marshal")); !os.IsNotExist(err) {
		t.Fatalf("task skeleton created .marshal: %v", err)
	}
}

func TestTaskCleanupRequiresRunAndHasNoImplicitApply(t *testing.T) {
	for _, args := range [][]string{
		{"task", "cleanup"},
		{"task", "cleanup", "--apply"},
		{"task", "cleanup", "--run", "run-1", "extra"},
		{"task", "cleanup", "--expired", "--run", "run-1"},
		{"task", "cleanup", "--expired", "--export-patch"},
		{"task", "cleanup", "--expired", "--apply"},
		{"task", "cleanup", "--expired", "--apply", "--actor", "  "},
		{"task", "cleanup", "--run", "run-1", "--export-patch"},
		{"task", "cleanup", "--run", "run-1", "--export-patch", "--apply", "--actor", "op:1"},
	} {
		var stdout, stderr bytes.Buffer
		if exit := Run(args, strings.NewReader(""), &stdout, &stderr); exit != ExitUsage {
			t.Fatalf("Run(%v) exit=%d stderr=%s", args, exit, stderr.String())
		}
	}
}

func TestTaskPlanRequiresAllNamedArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"none":           {"task", "plan"},
		"missing task":   {"task", "plan", "--policy", "policy.json", "--run", "run-1"},
		"missing policy": {"task", "plan", "--task", "task.json", "--run", "run-1"},
		"missing run":    {"task", "plan", "--task", "task.json", "--policy", "policy.json"},
		"positional":     {"task", "plan", "--task", "task.json", "--policy", "policy.json", "--run", "run-1", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			exitCode := Run(args, strings.NewReader(""), &stdout, &stderr)
			if exitCode != ExitUsage {
				t.Fatalf("Run(%v) exit = %d, want %d; stderr=%s", args, exitCode, ExitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), "marshal task plan --task PATH --policy PATH --run RUN_ID") {
				t.Fatalf("usage missing from stderr: %q", stderr.String())
			}
		})
	}
}

func TestTaskPlanEndToEndFreezesSelectedAdapter(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	const remoteURL = "https://example.invalid/marshal-cli-plan.git"
	runGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)

	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}

	executable := filepath.Join(t.TempDir(), "qwen")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '0.21.11\\n'; exit 0; fi\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	hostSystemRead := configureQwenAuthFixture(t)
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", executable)
	t.Setenv("MARSHAL_PI_PATH", "")

	const (
		taskID = "cli-plan-task"
		runID  = "cli-plan-run"
	)
	taskPath := filepath.Join(t.TempDir(), "task.json")
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	writeCLIFixture(t, taskPath, cliPlanningTask(t, repositoryRoot, taskID, remoteURL))
	writeCLIFixture(t, policyPath, cliPlanningPolicy(t, taskID, runID))

	stdout.Reset()
	stderr.Reset()
	exit := Run([]string{"task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("task plan exit = %d, stderr = %s", exit, stderr.String())
	}
	var result struct {
		State domain.RunState `json:"state"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode planning result: %v", err)
	}
	if result.State.State != domain.StateReady || result.State.RunID != runID || result.State.TaskID != taskID {
		t.Fatalf("planning state = %+v", result.State)
	}
	capability, err := os.ReadFile(filepath.Join(repositoryRoot, ".marshal", "runs", runID, "capability-snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var identity struct {
		AdapterID string `json:"adapterId"`
	}
	if err := json.Unmarshal(capability, &identity); err != nil || identity.AdapterID != "qwen" {
		t.Fatalf("frozen capability adapter = %q, err = %v", identity.AdapterID, err)
	}
	if hostSystemRead.Load() {
		t.Fatal("CLI Qwen fixture read a hostile host system auth setting")
	}
}

func TestTaskRunUsesFrozenFallbackAdapter(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	const remoteURL = "https://example.invalid/marshal-cli-run.git"
	runGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}
	marker := filepath.Join(t.TempDir(), "pi-started")
	executable := filepath.Join(t.TempDir(), "pi")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then printf '0.84.1\\n'; exit 0; fi\n" +
		": > \"" + marker + "\"\n" +
		"exit 1\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", executable)

	const (
		taskID = "cli-run-fallback-task"
		runID  = "cli-run-fallback-run"
	)
	taskPath := filepath.Join(t.TempDir(), "task.json")
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	t.Setenv("MARSHAL_QODER_PATH", writeVersionExecutableForCLI(t, "qodercli", "1.1.23"))
	t.Setenv("MARSHAL_QODER_CONFORMANCE_CONFIG", "")
	writeCLIFixture(t, taskPath, cliPlanningTaskWithWorkers(t, repositoryRoot, taskID, remoteURL, "qoder", []any{"pi"}))
	writeCLIFixture(t, policyPath, cliPlanningPolicyWithWorkers(t, taskID, runID, true, []any{"qoder", "pi"}))
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("task plan exit = %d, stderr = %s", exit, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "run", "--run", runID}, strings.NewReader(""), &stdout, &stderr); exit != ExitFailure || !strings.Contains(stderr.String(), "plan 审批") {
		t.Fatalf("unapproved task run exit = %d, stderr = %s", exit, stderr.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unapproved task run started Worker: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "approve", "--run", runID, "--gate", "plan", "--actor", "cli-test"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("task approve exit = %d, stderr = %s", exit, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	exit := Run([]string{"task", "run", "--run", runID}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitFailure {
		t.Fatalf("task run exit = %d, want worker failure %d; stderr = %s", exit, ExitFailure, stderr.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("frozen Pi adapter was not started: %v; stderr = %s", err, stderr.String())
	}
	capability, err := os.ReadFile(filepath.Join(repositoryRoot, ".marshal", "runs", runID, "capability-snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var identity struct {
		AdapterID string `json:"adapterId"`
	}
	if err := json.Unmarshal(capability, &identity); err != nil || identity.AdapterID != "pi" {
		t.Fatalf("frozen capability adapter = %q, err = %v", identity.AdapterID, err)
	}
}

// TestTaskPlanProductionDefaultOrder pins the production task-generation
// contract at the real CLI boundary. Planning itself remains deliberately
// explicit: the generated TaskSpec carries qoder -> codex -> qwen -> pi, and
// the Selector attempts that exact order without adding configured OpenCode.
func TestTaskPlanProductionDefaultOrder(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	templateData, err := os.ReadFile(filepath.Join(originalDirectory, "../../.agents/skills/marshal/templates/research-task.json"))
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	const remoteURL = "https://example.invalid/marshal-cli-default-order.git"
	runGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}
	writeVersionExecutable := func(name, version string) string {
		path := filepath.Join(t.TempDir(), name)
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", version)
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	// Qoder and Codex are configured but lack production authority, so their
	// truthful probes are unsupported and selection continues to Qwen.
	// OpenCode is configured to prove it is never injected into the explicit
	// production candidate chain.
	configureQwenAuthFixture(t)
	t.Setenv("MARSHAL_QODER_PATH", writeVersionExecutable("qodercli", "1.1.23"))
	t.Setenv("MARSHAL_QODER_CONFORMANCE_CONFIG", "")
	t.Setenv("MARSHAL_CODEX_PATH", writeVersionExecutable("codex", "0.145.0"))
	t.Setenv("MARSHAL_CODEX_AUTHORITY_CONFIG", "")
	t.Setenv("MARSHAL_QWEN_PATH", writeVersionExecutable("qwen", "0.21.11"))
	t.Setenv("MARSHAL_PI_PATH", writeVersionExecutable("pi", "0.84.1"))
	openCodeProbeMarker := filepath.Join(t.TempDir(), "opencode-probed")
	openCodeExecutable := filepath.Join(t.TempDir(), "opencode")
	openCodeScript := fmt.Sprintf("#!/bin/sh\n: > %q\nprintf '1.18.13\\n'\n", openCodeProbeMarker)
	if err := os.WriteFile(openCodeExecutable, []byte(openCodeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MARSHAL_OPENCODE_PATH", openCodeExecutable)

	scaffold := func(taskID string, extraArgs ...string) (string, []byte) {
		t.Helper()
		draft := cliProductionTaskDraftFromTemplate(t, templateData, repositoryRoot, taskID, remoteURL)
		worker := draft["worker"].(map[string]any)
		delete(worker, "preferredAdapter")
		delete(worker, "fallbackAdapters")
		draftPath := filepath.Join(t.TempDir(), "draft.json")
		taskPath := filepath.Join(t.TempDir(), "task.json")
		writeCLIFixture(t, draftPath, draft)
		stdout.Reset()
		stderr.Reset()
		args := append([]string{"task", "scaffold", "--draft", draftPath}, extraArgs...)
		if exit := Run(args, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
			t.Fatalf("task scaffold exit = %d, stderr = %s", exit, stderr.String())
		}
		generated := append([]byte(nil), stdout.Bytes()...)
		if err := os.WriteFile(taskPath, generated, 0o600); err != nil {
			t.Fatal(err)
		}
		return taskPath, generated
	}

	const (
		taskID = "cli-default-order-task"
		runID  = "cli-default-order-run"
	)
	taskPath, generated := scaffold(taskID)
	assertCLITaskWorkerOrder(t, generated, "qoder", []string{"codex", "qwen", "pi"})
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	writeCLIFixture(t, policyPath, cliPlanningPolicyWithWorkers(t, taskID, runID, true, []any{"qoder", "codex", "qwen", "pi", "opencode"}))

	stdout.Reset()
	stderr.Reset()
	exit := Run([]string{"task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("task plan exit = %d, stderr = %s", exit, stderr.String())
	}
	var result struct {
		State             domain.RunState `json:"state"`
		SelectionAttempts []struct {
			AdapterID string `json:"adapterId"`
			Outcome   string `json:"outcome"`
		} `json:"selectionAttempts"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode planning result: %v", err)
	}
	if result.State.State != domain.StateReady {
		t.Fatalf("planning state = %+v", result.State)
	}
	want := []struct{ adapterID, outcome string }{
		{"qoder", "unsupported"},
		{"codex", "unsupported"},
		{"qwen", "selected"},
	}
	if len(result.SelectionAttempts) != len(want) {
		t.Fatalf("selection attempts = %+v, want %d attempts", result.SelectionAttempts, len(want))
	}
	for index, expected := range want {
		actual := result.SelectionAttempts[index]
		if actual.AdapterID != expected.adapterID || actual.Outcome != expected.outcome {
			t.Fatalf("selectionAttempts[%d] = %+v, want adapter=%q outcome=%q", index, actual, expected.adapterID, expected.outcome)
		}
	}
	for _, attempt := range result.SelectionAttempts {
		if attempt.AdapterID == "opencode" {
			t.Fatalf("OpenCode entered the production candidate chain: %+v", result.SelectionAttempts)
		}
	}

	const (
		customTaskID = "cli-custom-order-task"
		customRunID  = "cli-custom-order-run"
	)
	customTaskPath, customGenerated := scaffold(customTaskID,
		"--preferred-adapter", "pi",
		"--fallback-adapter", "qwen",
		"--fallback-adapter", "codex",
		"--fallback-adapter", "qoder",
	)
	assertCLITaskWorkerOrder(t, customGenerated, "pi", []string{"qwen", "codex", "qoder"})
	_, singleGenerated := scaffold("cli-single-worker-task", "--preferred-adapter", "qwen")
	assertCLITaskWorkerOrder(t, singleGenerated, "qwen", []string{})
	customPolicyPath := filepath.Join(t.TempDir(), "policy.json")
	writeCLIFixture(t, customPolicyPath, cliPlanningPolicyWithWorkers(t, customTaskID, customRunID, true, []any{"qoder", "codex", "qwen", "pi", "opencode"}))
	stdout.Reset()
	stderr.Reset()
	exit = Run([]string{"task", "plan", "--task", customTaskPath, "--policy", customPolicyPath, "--run", customRunID, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("custom task plan exit = %d, stderr = %s", exit, stderr.String())
	}
	result = struct {
		State             domain.RunState `json:"state"`
		SelectionAttempts []struct {
			AdapterID string `json:"adapterId"`
			Outcome   string `json:"outcome"`
		} `json:"selectionAttempts"`
	}{}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode custom planning result: %v", err)
	}
	if len(result.SelectionAttempts) != 1 || result.SelectionAttempts[0].AdapterID != "pi" || result.SelectionAttempts[0].Outcome != "selected" {
		t.Fatalf("custom selection attempts = %+v, want pi selected", result.SelectionAttempts)
	}

	openCodeDraft := cliProductionTaskDraftFromTemplate(t, templateData, repositoryRoot, "cli-opencode-rejected-task", remoteURL)
	openCodeDraft["worker"].(map[string]any)["preferredAdapter"] = "opencode"
	openCodeDraft["worker"].(map[string]any)["fallbackAdapters"] = []any{"qoder"}
	openCodeDraftPath := filepath.Join(t.TempDir(), "draft.json")
	writeCLIFixture(t, openCodeDraftPath, openCodeDraft)
	for name, args := range map[string][]string{
		"draft":              {"task", "scaffold", "--draft", openCodeDraftPath},
		"preferred override": {"task", "scaffold", "--draft", taskPath, "--preferred-adapter", "opencode"},
		"fallback override":  {"task", "scaffold", "--draft", taskPath, "--preferred-adapter", "qoder", "--fallback-adapter", "opencode"},
	} {
		t.Run("reject OpenCode "+name, func(t *testing.T) {
			stdout.Reset()
			stderr.Reset()
			exit := Run(args, strings.NewReader(""), &stdout, &stderr)
			if exit != ExitUnavailable || stderr.String() != "生成失败：OpenCode 不可用于新 Task。\n" || stdout.Len() != 0 {
				t.Fatalf("exit = %d, stdout = %q, stderr = %q", exit, stdout.String(), stderr.String())
			}
		})
	}

	for _, test := range []struct {
		name      string
		preferred string
		fallbacks []any
	}{
		{name: "preferred", preferred: "opencode", fallbacks: []any{"pi"}},
		{name: "fallback", preferred: "pi", fallbacks: []any{"opencode"}},
	} {
		t.Run("direct task plan rejects OpenCode "+test.name, func(t *testing.T) {
			directTaskID := "cli-direct-opencode-" + test.name + "-task"
			directRunID := "cli-direct-opencode-" + test.name + "-run"
			directTaskPath := filepath.Join(t.TempDir(), "task.json")
			directPolicyPath := filepath.Join(t.TempDir(), "policy.json")
			writeCLIFixture(t, directTaskPath, cliPlanningTaskWithWorkers(t, repositoryRoot, directTaskID, remoteURL, test.preferred, test.fallbacks))
			writeCLIFixture(t, directPolicyPath, cliPlanningPolicyWithWorkers(t, directTaskID, directRunID, true, []any{"opencode", "pi"}))
			stdout.Reset()
			stderr.Reset()
			exit := Run([]string{"task", "plan", "--task", directTaskPath, "--policy", directPolicyPath, "--run", directRunID, "--json"}, strings.NewReader(""), &stdout, &stderr)
			if exit != ExitFailure || !strings.Contains(stderr.String(), planning.ErrPolicyOpenCode) || stdout.Len() != 0 {
				t.Fatalf("exit = %d, stdout = %q, stderr = %q", exit, stdout.String(), stderr.String())
			}
			if _, err := os.Stat(filepath.Join(repositoryRoot, ".marshal", "runs", directRunID)); !os.IsNotExist(err) {
				t.Fatalf("rejected direct TaskSpec created run state: %v", err)
			}
			if _, err := os.Stat(openCodeProbeMarker); !os.IsNotExist(err) {
				t.Fatalf("rejected direct TaskSpec probed OpenCode: %v", err)
			}
		})
	}
}

func TestTaskRunRejectsUnsafeOrUnavailableFrozenIdentityBeforeWorkerStart(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	runGit(t, repositoryRoot, "init", "-q")
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")

	t.Run("invalid run id", func(t *testing.T) {
		stdout.Reset()
		stderr.Reset()
		exit := Run([]string{"task", "run", "--run", "../../escape"}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitUsage || !strings.Contains(stderr.String(), "Run ID 无效") {
			t.Fatalf("exit = %d, stderr = %q", exit, stderr.String())
		}
		if _, err := os.Stat(filepath.Join(repositoryRoot, "escape")); !os.IsNotExist(err) {
			t.Fatalf("invalid Run ID created an escaped path: %v", err)
		}
	})

	t.Run("missing snapshot", func(t *testing.T) {
		stdout.Reset()
		stderr.Reset()
		exit := Run([]string{"task", "run", "--run", "missing-run"}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitFailure || stderr.String() != "运行失败：读取冻结 CapabilitySnapshot 失败。\n" {
			t.Fatalf("exit = %d, stderr = %q", exit, stderr.String())
		}
	})

	t.Run("malformed snapshot", func(t *testing.T) {
		runDir := filepath.Join(repositoryRoot, ".marshal", "runs", "malformed-run")
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runDir, "capability-snapshot.json"), []byte(`{"secret":"must-not-leak"`), 0o600); err != nil {
			t.Fatal(err)
		}
		stdout.Reset()
		stderr.Reset()
		exit := Run([]string{"task", "run", "--run", "malformed-run"}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitFailure || stderr.String() != "运行失败：冻结 CapabilitySnapshot 无效。\n" || strings.Contains(stderr.String(), "must-not-leak") {
			t.Fatalf("exit = %d, stderr = %q", exit, stderr.String())
		}
	})

	t.Run("unregistered frozen adapter", func(t *testing.T) {
		runDir := filepath.Join(repositoryRoot, ".marshal", "runs", "unregistered-run")
		if err := os.MkdirAll(runDir, 0o700); err != nil {
			t.Fatal(err)
		}
		capability := readCLIFixture(t, "examples/happy-path/capability-snapshot.json")
		capability["adapterId"] = "pi"
		writeCLIFixture(t, filepath.Join(runDir, "capability-snapshot.json"), capability)
		stdout.Reset()
		stderr.Reset()
		exit := Run([]string{"task", "run", "--run", "unregistered-run"}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitUnavailable || stderr.String() != "运行失败：冻结 Worker Adapter 当前未配置或不可用。\n" {
			t.Fatalf("exit = %d, stderr = %q", exit, stderr.String())
		}
	})
}

func cliPlanningTask(t *testing.T, repositoryRoot, taskID, remoteURL string) map[string]any {
	return cliPlanningTaskWithWorkers(t, repositoryRoot, taskID, remoteURL, "qwen", []any{})
}

func cliPlanningTaskWithWorkers(t *testing.T, repositoryRoot, taskID, remoteURL, preferred string, fallbacks []any) map[string]any {
	t.Helper()
	fixture := readCLIFixture(t, "examples/happy-path/task-spec.json")
	fixture["metadata"].(map[string]any)["id"] = taskID
	repository := fixture["repository"].(map[string]any)
	repository["path"] = repositoryRoot
	repository["baseRef"] = strings.TrimSpace(runGitCLI(t, repositoryRoot, "rev-parse", "HEAD"))
	repository["remote"] = "origin"
	repository["expectedRemoteUrl"] = remoteURL
	worker := fixture["worker"].(map[string]any)
	worker["preferredAdapter"] = preferred
	worker["fallbackAdapters"] = fallbacks
	worker["executionProfile"] = "workspace-write"
	worker["sessionPolicy"] = "ephemeral"
	fixture["deliverables"] = []any{map[string]any{"id": "implementation", "kind": "code", "required": true, "pathGlob": "README.md"}}
	publication := fixture["publication"].(map[string]any)
	publication["required"] = false
	publication["provider"] = "none"
	publication["mode"] = "none"
	publication["remote"] = "origin"
	publication["baseBranch"] = "main"
	publication["mergePolicy"] = "never"
	publication["requiredChecks"] = []any{}
	return fixture
}

func cliProductionTaskDraftFromTemplate(t *testing.T, templateData []byte, repositoryRoot, taskID, remoteURL string) map[string]any {
	t.Helper()
	var draft map[string]any
	if err := json.Unmarshal(templateData, &draft); err != nil {
		t.Fatal(err)
	}
	draft["metadata"].(map[string]any)["id"] = taskID
	repository := draft["repository"].(map[string]any)
	repository["path"] = repositoryRoot
	repository["baseRef"] = strings.TrimSpace(runGitCLI(t, repositoryRoot, "rev-parse", "HEAD"))
	repository["remote"] = "origin"
	repository["expectedRemoteUrl"] = remoteURL
	scope := draft["scope"].(map[string]any)
	scope["allowPaths"] = []any{"README.md"}
	draft["acceptance"] = readCLIFixture(t, "examples/happy-path/task-spec.json")["acceptance"]
	draft["deliverables"] = []any{map[string]any{
		"id": "research-report", "kind": "documentation", "pathGlob": "README.md", "minimumCount": float64(1), "required": true,
	}}
	return draft
}

func assertCLITaskWorkerOrder(t *testing.T, generated []byte, preferred string, fallbacks []string) {
	t.Helper()
	var task struct {
		Worker struct {
			Preferred string   `json:"preferredAdapter"`
			Fallback  []string `json:"fallbackAdapters"`
		} `json:"worker"`
	}
	if err := json.Unmarshal(generated, &task); err != nil {
		t.Fatal(err)
	}
	if task.Worker.Preferred != preferred || !reflect.DeepEqual(task.Worker.Fallback, fallbacks) {
		t.Fatalf("worker order = %q -> %v, want %q -> %v", task.Worker.Preferred, task.Worker.Fallback, preferred, fallbacks)
	}
}

func cliPlanningPolicy(t *testing.T, taskID, runID string) map[string]any {
	return cliPlanningPolicyWithWorkers(t, taskID, runID, false, []any{"qwen"})
}

func cliPlanningPolicyWithWorkers(t *testing.T, taskID, runID string, allowFallback bool, allowed []any) map[string]any {
	t.Helper()
	fixture := readCLIFixture(t, "examples/happy-path/policy-snapshot.json")
	fixture["taskId"] = taskID
	fixture["runId"] = runID
	effective := fixture["effective"].(map[string]any)
	effective["allowFallbackWorkers"] = allowFallback
	effective["allowPublication"] = false
	effective["allowMerge"] = false
	effective["allowedAdapters"] = allowed
	cliStampPolicyDigest(t, fixture)
	return fixture
}

// cliStampPolicyDigest recomputes the detached policyDigest of a mutated
// policy document exactly the way the production planning gate verifies it:
// blank the policyDigest field, marshal the document, canonicalize it with
// canonical.JSON and digest it with canonical.DigestBytes. Fixtures must
// seal the document at test runtime instead of carrying a stale digest.
func cliStampPolicyDigest(t *testing.T, policy map[string]any) {
	t.Helper()
	policy["policyDigest"] = ""
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := canonical.DigestJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	policy["policyDigest"] = digest
}

func readCLIFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := marshalSchemas.FS.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func writeCLIFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func configureQwenAuthFixture(t *testing.T) *atomic.Bool {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsPath := filepath.Join(home, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCLIFixture(t, settingsPath, map[string]any{
		"security": map[string]any{"auth": map[string]any{"selectedType": "qwen-oauth"}},
	})
	hostSystemRead := &atomic.Bool{}
	previous := newWorkerRuntime
	newWorkerRuntime = func(getenv func(string) string) (*app.WorkerRuntime, error) {
		return app.NewWorkerRuntimeWithQwenAuthSettingsForTesting(getenv, []string{settingsPath}, func(path string, maxBytes int64) ([]byte, error) {
			if strings.HasPrefix(path, "/Library/Application Support/QwenCode/") {
				hostSystemRead.Store(true)
				return []byte(`{"security":{"auth":{"selectedType":`), nil
			}
			if path != settingsPath {
				return nil, os.ErrNotExist
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			if int64(len(data)) > maxBytes {
				return nil, fmt.Errorf("qwen auth fixture exceeds bounded input")
			}
			return data, nil
		})
	}
	t.Cleanup(func() { newWorkerRuntime = previous })
	return hostSystemRead
}

func writeVersionExecutableForCLI(t *testing.T, name, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q\n", version)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func TestInitAndTaskStatusEndToEnd(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	command := exec.Command("git", "-C", repository, "init", "-q")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if err := os.Chdir(repository); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}
	store := runstore.New(filepath.Join(repository, ".marshal"))
	lease, err := store.Acquire("run:fixture")
	if err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState("task:fixture", "run:fixture", time.Unix(1, 0))
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "status", "--run", "run:fixture", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("status exit = %d, stderr = %s", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"state": "CREATED"`) {
		t.Fatalf("status output = %s", stdout.String())
	}
}

func TestTaskVerifyEndToEnd(t *testing.T) {
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	runGitCLI(t, repository, "init", "-q")
	runGitCLI(t, repository, "config", "user.name", "Marshal Test")
	runGitCLI(t, repository, "config", "user.email", "marshal@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCLI(t, repository, "add", "README.md")
	runGitCLI(t, repository, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(runGitCLI(t, repository, "rev-parse", "HEAD"))
	location, err := marshalRepository.Discover(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := location.Init(); err != nil {
		t.Fatal(err)
	}
	manager, err := gitworktree.Open(repository)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := manager.Create(location.StateRoot, "TASK-1", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Release(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repository, "worktree", "remove", "--force", worktree.Path).Run()
		_ = exec.Command("git", "-C", repository, "branch", "-D", worktree.Branch).Run()
	})
	if err := os.MkdirAll(filepath.Join(worktree.Path, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree.Path, "src", "code.go"), []byte("package src\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	taskData := []byte(fmt.Sprintf(`{
  "apiVersion":"marshal.dev/v1alpha1","kind":"Task",
  "metadata":{"id":"TASK-1","title":"CLI verification fixture"},
  "repository":{"path":%q,"baseRef":"%s","remote":"origin"},
  "work":{"objective":"Verify an isolated fixture change."},
  "scope":{"allowPaths":["src/**"],"denyPaths":[],"allowSubmodules":false,"maxChangedFiles":5,"maxDiffBytes":100000},
  "acceptance":{"commands":[{"id":"source-exists","argv":["sh","-c","test -f src/code.go"],"cwd":".","timeoutSeconds":5,"required":true,"baselinePolicy":"none","maxLogBytes":4096}],"allowNoChange":false},
  "deliverables":[{"id":"source","kind":"code","required":true,"pathGlob":"src/*.go","minimumCount":1},{"id":"pull-request","kind":"publication","required":true}],
  "worker":{"preferredAdapter":"fake","fallbackAdapters":[],"executionProfile":"workspace-write","sessionPolicy":"ephemeral"},
  "budgets":{"runTimeoutSeconds":60,"attemptTimeoutSeconds":30,"maxAttempts":1,"maxOperationalRetries":0,"maxReworkRounds":0,"maxOutputBytes":100000},
  "publication":{"required":true,"provider":"github","mode":"draft","remote":"origin","baseBranch":"main","mergePolicy":"never","requiredChecks":[]}
}`, repository, base))
	digest, err := canonical.DigestJSON(taskData)
	if err != nil {
		t.Fatal(err)
	}
	store := runstore.New(location.StateRoot)
	lease, err := store.Acquire("run:verify")
	if err != nil {
		t.Fatal(err)
	}
	state := domain.NewRunState("TASK-1", "run:verify", time.Now())
	transitions := [][2]domain.State{{domain.StateCreated, domain.StatePlanned}, {domain.StatePlanned, domain.StateReady}, {domain.StateReady, domain.StateRunning}, {domain.StateRunning, domain.StateVerifying}}
	for index, transition := range transitions {
		event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: fmt.Sprintf("event:%d", index+1), RunID: "run:verify", Sequence: uint64(index + 1), Type: "run.transition", StateFrom: transition[0], StateTo: transition[1], Timestamp: time.Now().UTC(), Payload: map[string]any{}}
		if transition[1] == domain.StateRunning {
			event.AttemptID = "attempt:1"
		}
		if err := store.Append(lease, event, uint64(index)); err != nil {
			t.Fatal(err)
		}
	}
	state.State, state.Sequence, state.SpecDigest, state.BaseSHA, state.WorktreePath = domain.StateVerifying, uint64(len(transitions)), digest, base, worktree.Path
	if err := store.WriteSnapshot(lease, state); err != nil {
		t.Fatal(err)
	}
	runDirectory := filepath.Join(location.StateRoot, "runs", "run:verify")
	if err := os.WriteFile(filepath.Join(runDirectory, "task-spec.json"), taskData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repository); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"task", "verify", "--run", "run:verify", "--json"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("verify exit = %d, stderr = %s", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status": "pass"`) {
		t.Fatalf("verify output = %s", stdout.String())
	}
	verifiedState, err := store.Inspect("run:verify")
	if err != nil {
		t.Fatal(err)
	}
	if verifiedState.State != domain.StateReviewPending || verifiedState.Sequence != 5 {
		t.Fatalf("verified state = %+v", verifiedState)
	}
}

type autoFlowSetup struct {
	repositoryRoot, remoteURL string
}

func newAutoFlowSetup(t *testing.T) autoFlowSetup {
	t.Helper()
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	repositoryRoot, err = filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	const remoteURL = "https://example.invalid/marshal-autoflow.git"
	runGit(t, repositoryRoot, "remote", "add", "origin", remoteURL)
	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"init"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("init exit = %d, stderr = %s", exit, stderr.String())
	}
	configureQwenAuthFixture(t)
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	t.Setenv("MARSHAL_QWEN_PATH", autoFlowWorkerExecutable(t))
	t.Setenv("MARSHAL_PI_PATH", "")
	return autoFlowSetup{repositoryRoot: repositoryRoot, remoteURL: remoteURL}
}

func (s autoFlowSetup) planAndApprove(t *testing.T, taskID, runID string, acceptancePasses bool) {
	t.Helper()
	task := cliPlanningTask(t, s.repositoryRoot, taskID, s.remoteURL)
	task["scope"] = map[string]any{"allowPaths": []string{"src/auth/**"}, "denyPaths": []string{}, "allowSubmodules": false, "maxChangedFiles": 5, "maxDiffBytes": 100000}
	checkTarget := "src/auth/worker-change.txt"
	if !acceptancePasses {
		checkTarget = "src/auth/missing-file.txt"
	}
	task["acceptance"] = map[string]any{
		"allowNoChange": false,
		"commands": []any{map[string]any{
			"id": "change-check", "argv": []string{"sh", "-c", "test -f " + checkTarget}, "cwd": ".",
			"timeoutSeconds": 5, "required": true, "baselinePolicy": "none", "maxLogBytes": 4096,
		}},
	}
	task["deliverables"] = []any{map[string]any{"id": "implementation", "kind": "code", "required": true, "pathGlob": "src/auth/**", "minimumCount": 1}}
	taskPath := filepath.Join(t.TempDir(), "task.json")
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	writeCLIFixture(t, taskPath, task)
	writeCLIFixture(t, policyPath, cliPlanningPolicy(t, taskID, runID))
	var stdout, stderr bytes.Buffer
	if exit := Run([]string{"task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("task plan exit = %d, stderr = %s", exit, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if exit := Run([]string{"task", "approve", "--run", runID, "--gate", "plan", "--actor", "autoflow-test"}, strings.NewReader(""), &stdout, &stderr); exit != ExitOK {
		t.Fatalf("task approve exit = %d, stderr = %s", exit, stderr.String())
	}
}

func autoFlowWorkerExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qwen")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then printf '0.21.11\n'; exit 0; fi
for last; do :; done
result_path=$(printf '%s\n' "$last" | sed -n 's/.*写入：\([^[:space:]]*\).*/\1/p')
task_id=$(printf '%s\n' "$last" | sed -n 's/.*taskId=\(.*\)、runId=.*/\1/p')
run_id=$(printf '%s\n' "$last" | sed -n 's/.*runId=\(.*\)、attemptId=.*/\1/p')
attempt_id=$(printf '%s\n' "$last" | sed -n 's/.*attemptId=\(.*\)、adapter\.id=.*/\1/p')
mkdir -p src/auth
printf 'fixture change\n' > src/auth/worker-change.txt
cat > "$result_path" <<EOF
{"apiVersion":"marshal.dev/v1alpha1","kind":"WorkerResult","taskId":"$task_id","runId":"$run_id","attemptId":"$attempt_id","adapter":{"id":"qwen","executable":"/fixture/qwen","version":"fixture"},"status":"completed","summary":"fixture change","declaredChangedFiles":["src/auth/worker-change.txt"],"declaredArtifacts":[],"declaredCommands":[],"declaredRisks":[],"outputTruncated":false,"startedAt":"2026-08-07T00:00:00Z","completedAt":"2026-08-07T00:00:01Z"}
EOF
printf '%s\n' '{"type":"system","subtype":"init","session_id":"session-autoflow","cwd":"'"$PWD"'","qwen_code_version":"0.21.11"}'
printf '%s\n' '{"type":"result","subtype":"success","usage":{"input_tokens":1,"output_tokens":1}}'
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTaskRunThroughVerifyReachesReviewPending(t *testing.T) {
	setup := newAutoFlowSetup(t)
	const taskID, runID = "autoflow-task-pass", "autoflow-run-pass"
	setup.planAndApprove(t, taskID, runID, true)
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"task", "run", "--run", runID, "--through-verify", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("task run --through-verify exit = %d, stderr = %s", exit, stderr.String())
	}
	var combined struct {
		State        domain.RunState `json:"state"`
		AttemptID    string          `json:"attemptId"`
		Verification struct {
			Status string `json:"status"`
		} `json:"verification"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &combined); err != nil {
		t.Fatalf("decode run output: %v\n%s", err, stdout.String())
	}
	if combined.State.State != domain.StateReviewPending || combined.AttemptID == "" || combined.Verification.Status != "pass" {
		t.Fatalf("combined result = %+v\n%s", combined, stdout.String())
	}
	store := runstore.New(filepath.Join(setup.repositoryRoot, ".marshal"))
	state, err := store.Inspect(runID)
	if err != nil || state.State != domain.StateReviewPending {
		t.Fatalf("stored state = %+v, err = %v", state, err)
	}
	events, _, err := store.ReadEvents(runID)
	if err != nil || len(events) == 0 || events[len(events)-1].Type != "verification.completed" {
		t.Fatalf("journal tail = %+v, err = %v", events, err)
	}
}

func TestTaskRunWithoutThroughVerifyStopsAtVerifying(t *testing.T) {
	setup := newAutoFlowSetup(t)
	const taskID, runID = "autoflow-task-plain", "autoflow-run-plain"
	setup.planAndApprove(t, taskID, runID, true)
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"task", "run", "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		t.Fatalf("task run exit = %d, stderr = %s", exit, stderr.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("decode run output: %v\n%s", err, stdout.String())
	}
	if _, exists := raw["verification"]; exists {
		t.Fatalf("run without --through-verify must not embed verification: %s", stdout.String())
	}
	var result struct {
		State domain.RunState `json:"state"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State.State != domain.StateVerifying {
		t.Fatalf("state = %+v", result.State)
	}
	reportPath := filepath.Join(setup.repositoryRoot, ".marshal", "runs", runID, "verification-report.json")
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatalf("verify ran without the flag: %v", err)
	}
}

func TestTaskRunThroughVerifyDoesNotMaskFailedVerification(t *testing.T) {
	setup := newAutoFlowSetup(t)
	const taskID, runID = "autoflow-task-fail", "autoflow-run-fail"
	setup.planAndApprove(t, taskID, runID, false)
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"task", "run", "--run", runID, "--through-verify", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitFailure {
		t.Fatalf("task run --through-verify exit = %d, want %d; stderr = %s", exit, ExitFailure, stderr.String())
	}
	var combined struct {
		State        domain.RunState `json:"state"`
		Verification struct {
			Status string `json:"status"`
		} `json:"verification"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &combined); err != nil {
		t.Fatalf("decode run output: %v\n%s", err, stdout.String())
	}
	if combined.State.State != domain.StateReviewPending || combined.Verification.Status != "fail" {
		t.Fatalf("combined result = %+v\n%s", combined, stdout.String())
	}
	state, err := runstore.New(filepath.Join(setup.repositoryRoot, ".marshal")).Inspect(runID)
	if err != nil || state.State != domain.StateReviewPending {
		t.Fatalf("stored state = %+v, err = %v", state, err)
	}
}

func runGitCLI(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}
