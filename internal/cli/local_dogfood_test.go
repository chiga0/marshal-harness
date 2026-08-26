package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

func TestMain(m *testing.M) {
	localDogfoodGateTestBypass = func(info buildinfo.Info) bool {
		return info.Commit == "unknown" && info.SelfProfile == "unprofiled"
	}
	os.Exit(m.Run())
}

func TestDarwinLocalDogfoodProductionEntry(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin profile gate")
	}
	originalBuild := localBuildInfo
	originalNow := localNow
	originalRuntime := newWorkerRuntime
	t.Cleanup(func() {
		localBuildInfo = originalBuild
		localNow = originalNow
		newWorkerRuntime = originalRuntime
	})

	const sourceHead = "89abcdef0123456789abcdef0123456789abcdef"
	now := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	localBuildInfo = func() buildinfo.Info {
		return buildinfo.Info{Version: "dev", Commit: sourceHead, SelfProfile: selfidentity.LocalProfile,
			OS: runtime.GOOS, Arch: runtime.GOARCH}
	}
	localNow = func() time.Time { return now }

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDirectory) })

	newWorkerRuntime = func(func(string) string) (*app.WorkerRuntime, error) {
		t.Fatal("doctor --self reached Worker Runtime discovery")
		return nil, nil
	}
	var activationOutput, stderr bytes.Buffer
	exit := RunContext(context.Background(), []string{
		"doctor", "--self", "--repository-root", root, "--activation-id", "local-production-entry",
		"--issued-at", now.Format(time.RFC3339), "--valid-until", now.Add(time.Hour).Format(time.RFC3339),
	}, strings.NewReader(""), &activationOutput, &stderr)
	if exit != ExitOK {
		t.Fatalf("doctor --self exit=%d stderr=%s", exit, stderr.String())
	}
	activationRaw := activationOutput.Bytes()
	canonicalRaw, err := canonical.JSON(activationRaw)
	if err != nil || !bytes.Equal(canonicalRaw, activationRaw) || bytes.HasSuffix(activationRaw, []byte("\n")) {
		t.Fatalf("doctor --self did not emit exact canonical activation: err=%v", err)
	}
	var bypassOutput, bypassError bytes.Buffer
	exit = RunContext(context.Background(), []string{"doctor", "--self", "--self=false", "--json"},
		strings.NewReader(""), &bypassOutput, &bypassError)
	if exit != ExitUsage || bypassOutput.Len() != 0 || !strings.Contains(bypassError.String(), "布尔参数不得重复") {
		t.Fatalf("conflicting --self exit=%d stdout=%q stderr=%q", exit, bypassOutput.String(), bypassError.String())
	}
	for _, test := range []struct {
		name   string
		args   []string
		reason string
	}{
		{"self false", []string{"doctor", "--self=false", "--json"}, selfidentity.ReasonOptInMissing},
		{"self with run", []string{"doctor", "--self", "--run", "local-run", "--json"}, selfidentity.ReasonOptInMissing},
		{"self with print env", []string{"doctor", "--self", "--print-env"}, selfidentity.ReasonOptInMissing},
		{"self with repair", []string{"doctor", "--self", "--run", "local-run", "--repair", "--json"}, selfidentity.ReasonCommandDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output, denied bytes.Buffer
			got := RunContext(context.Background(), test.args, strings.NewReader(""), &output, &denied)
			if got != ExitUnavailable || output.Len() != 0 || !strings.Contains(denied.String(), test.reason) {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want reason %q", got, output.String(), denied.String(), test.reason)
			}
		})
	}
	activationPath := filepath.Join(root, "activation.json")
	if err := os.WriteFile(activationPath, activationRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(selfidentity.ActivationEnv, activationPath)

	draftRaw, err := os.ReadFile(filepath.Join(originalDirectory, "..", "..", "schemas", "examples", "happy-path", "task-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	var draft map[string]any
	if err := json.Unmarshal(draftRaw, &draft); err != nil {
		t.Fatal(err)
	}
	worker := draft["worker"].(map[string]any)
	delete(worker, "preferredAdapter")
	delete(worker, "fallbackAdapters")
	draftRaw, err = json.Marshal(draft)
	if err != nil {
		t.Fatal(err)
	}
	draftPath := filepath.Join(root, "draft.json")
	if err := os.WriteFile(draftPath, draftRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	var taskOutput bytes.Buffer
	stderr.Reset()
	exit = RunContext(context.Background(), []string{"task", "scaffold", "--draft", draftPath},
		strings.NewReader(""), &taskOutput, &stderr)
	if exit != ExitOK {
		t.Fatalf("gated task scaffold exit=%d stderr=%s", exit, stderr.String())
	}
	if !json.Valid(taskOutput.Bytes()) {
		t.Fatalf("task scaffold output is not JSON: %s", taskOutput.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".marshal")); !os.IsNotExist(err) {
		t.Fatalf("unexpected state root before denied repair: %v", err)
	}
	var repairOutput, repairError bytes.Buffer
	exit = RunContext(context.Background(), []string{"doctor", "--run", "local-run", "--repair", "--json"},
		strings.NewReader(""), &repairOutput, &repairError)
	if exit != ExitUnavailable || repairOutput.Len() != 0 || !strings.Contains(repairError.String(), selfidentity.ReasonCommandDenied) {
		t.Fatalf("denied repair exit=%d stdout=%q stderr=%q", exit, repairOutput.String(), repairError.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".marshal")); !os.IsNotExist(err) {
		t.Fatalf("denied repair changed state root: %v", err)
	}
	for _, check := range []string{
		"artifact-attestation-check", "qoder-transcript-check", "plan-premortem-check",
		"review-freshness-check", "codex-provider-schema-check", "closure-matrix-check",
	} {
		if !localDogfoodBootstrapCommand([]string{"internal", check, "--attestation-ready"}, nil) {
			t.Fatalf("fixed read-only internal checker %q was not admitted", check)
		}
	}
	if localDogfoodBootstrapCommand([]string{"internal", "plan-premortem-check"}, nil) ||
		localDogfoodBootstrapCommand([]string{"internal", "unknown-check", "--attestation-ready"}, nil) {
		t.Fatal("internal checker bootstrap admitted a missing handshake or unknown command")
	}

	for _, test := range []struct {
		name   string
		args   []string
		reason string
	}{
		{"publication", []string{"task", "publish", "--run", "run-1"}, selfidentity.ReasonPublicationDenied},
		{"remote", []string{"serve"}, selfidentity.ReasonRemoteSurfaceDenied},
		{"internal", []string{"internal", "plan-premortem-check"}, selfidentity.ReasonCredentialedEffectDenied},
		{"worker lifecycle remains LD3", []string{"task", "run", "--run", "local-run"}, selfidentity.ReasonCommandDenied},
		{"publish approval", []string{"task", "approve", "--run", "local-run", "--gate", "publish"}, selfidentity.ReasonPublicationDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, denied bytes.Buffer
			if got := RunContext(context.Background(), test.args, strings.NewReader(""), &stdout, &denied); got != ExitUnavailable {
				t.Fatalf("exit=%d, want %d", got, ExitUnavailable)
			}
			if !strings.Contains(denied.String(), test.reason) {
				t.Fatalf("stderr=%q, want reason %q", denied.String(), test.reason)
			}
		})
	}

	newWorkerRuntime = originalRuntime
	runGitCLI(t, root, "init", "-q")
	runGitCLI(t, root, "config", "user.name", "Marshal Local Test")
	runGitCLI(t, root, "config", "user.email", "marshal-local@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("local dogfood\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCLI(t, root, "add", "README.md")
	runGitCLI(t, root, "commit", "-q", "-m", "local fixture")
	const remoteURL = "https://example.invalid/local-dogfood.git"
	runGitCLI(t, root, "remote", "add", "origin", remoteURL)
	var initOutput, initError bytes.Buffer
	exit = RunContext(context.Background(), []string{"init", "--json"}, strings.NewReader(""), &initOutput, &initError)
	if exit != ExitOK {
		t.Fatalf("local init exit=%d stderr=%s", exit, initError.String())
	}
	configureQwenAuthFixture(t)
	t.Setenv("MARSHAL_QWEN_PATH", writeVersionExecutableForCLI(t, "qwen", "0.21.11"))
	t.Setenv("MARSHAL_QODER_PATH", "")
	t.Setenv("MARSHAL_CODEX_PATH", "")
	t.Setenv("MARSHAL_PI_PATH", "")

	var doctorOutput, doctorError bytes.Buffer
	exit = RunContext(context.Background(), []string{"doctor", "--json"}, strings.NewReader(""), &doctorOutput, &doctorError)
	if exit != ExitOK {
		t.Fatalf("gated doctor exit=%d stderr=%s", exit, doctorError.String())
	}
	var doctor doctorReport
	if err := json.Unmarshal(doctorOutput.Bytes(), &doctor); err != nil {
		t.Fatal(err)
	}
	if doctor.SelfIdentity == nil || doctor.SelfIdentity.IdentitySubjectDigest == "" || doctor.PolicyEnvironmentBinding == nil {
		t.Fatalf("doctor omitted Core self observation: %s", doctorOutput.String())
	}

	const taskID, runID = "local-dogfood-task", "local-dogfood-run"
	taskPath := filepath.Join(root, "task.json")
	policyPath := filepath.Join(root, "policy.json")
	writeCLIFixture(t, taskPath, cliPlanningTask(t, root, taskID, remoteURL))
	unboundRunID := "local-dogfood-unbound-run"
	unboundPolicyPath := filepath.Join(root, "unbound-policy.json")
	unboundPolicy := cliPlanningPolicy(t, taskID, unboundRunID)
	writeCLIFixture(t, unboundPolicyPath, unboundPolicy)
	var unboundOutput, unboundError bytes.Buffer
	exit = RunContext(context.Background(), []string{"task", "plan", "--task", taskPath, "--policy", unboundPolicyPath, "--run", unboundRunID, "--json"}, strings.NewReader(""), &unboundOutput, &unboundError)
	if exit != ExitFailure || !strings.Contains(unboundError.String(), planning.ErrPolicyLocalBindingMissing) {
		t.Fatalf("unbound local plan exit=%d stdout=%s stderr=%s", exit, unboundOutput.String(), unboundError.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".marshal", "runs", unboundRunID)); !os.IsNotExist(err) {
		t.Fatalf("unbound local plan left side effects: %v", err)
	}
	policy := cliPlanningPolicy(t, taskID, runID)
	policy["control"].(map[string]any)["requiredApprovals"] = []any{"plan"}
	policy["environmentBinding"] = doctor.PolicyEnvironmentBinding
	cliStampPolicyDigest(t, policy)
	writeCLIFixture(t, policyPath, policy)

	var planOutput, planError bytes.Buffer
	exit = RunContext(context.Background(), []string{"task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID, "--json"}, strings.NewReader(""), &planOutput, &planError)
	if exit != ExitOK {
		t.Fatalf("local task plan exit=%d stderr=%s", exit, planError.String())
	}
	var statusOutput, statusError bytes.Buffer
	exit = RunContext(context.Background(), []string{"task", "status", "--run", runID, "--json"}, strings.NewReader(""), &statusOutput, &statusError)
	if exit != ExitOK || !strings.Contains(statusOutput.String(), `"currentMatch": true`) || !strings.Contains(statusOutput.String(), `"production": false`) {
		t.Fatalf("local task status exit=%d stdout=%s stderr=%s", exit, statusOutput.String(), statusError.String())
	}
	var statusShape map[string]json.RawMessage
	if err := json.Unmarshal(statusOutput.Bytes(), &statusShape); err != nil {
		t.Fatal(err)
	}
	var statusName string
	if err := json.Unmarshal(statusShape["state"], &statusName); err != nil || statusName != string(domain.StateReady) {
		t.Fatalf("local status .state changed shape: raw=%s value=%q err=%v", statusShape["state"], statusName, err)
	}
	for _, field := range []string{"runId", "taskId", "selfIdentity", "assurance", "execution", "production", "publication", "currentMatch"} {
		if _, ok := statusShape[field]; !ok {
			t.Fatalf("local status omitted top-level %q: %s", field, statusOutput.String())
		}
	}
	var approveOutput, approveError bytes.Buffer
	exit = RunContext(context.Background(), []string{"task", "approve", "--run", runID, "--gate", "plan", "--json"}, strings.NewReader(""), &approveOutput, &approveError)
	if exit != ExitOK {
		t.Fatalf("local task approve exit=%d stderr=%s", exit, approveError.String())
	}
	replacement, err := selfidentity.RenderActivation(selfidentity.BootstrapOptions{
		RepositoryRoot: root, ActivationID: "local-production-entry-replacement", IssuedAt: now,
		ValidUntil: now.Add(time.Hour), Build: selfidentity.BuildIdentity{SourceHead: sourceHead, SelfProfile: selfidentity.LocalProfile},
	})
	if err != nil {
		t.Fatal(err)
	}
	replacementPath := filepath.Join(root, "replacement-activation.json")
	if err := os.WriteFile(replacementPath, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(selfidentity.ActivationEnv, replacementPath)
	statusOutput.Reset()
	statusError.Reset()
	exit = RunContext(context.Background(), []string{"task", "status", "--run", runID, "--json"}, strings.NewReader(""), &statusOutput, &statusError)
	if exit != ExitFailure || !strings.Contains(statusError.String(), "本地 Run 身份绑定无效") {
		t.Fatalf("replacement activation status exit=%d stdout=%s stderr=%s", exit, statusOutput.String(), statusError.String())
	}
	approveOutput.Reset()
	approveError.Reset()
	exit = RunContext(context.Background(), []string{"task", "approve", "--run", runID, "--gate", "plan", "--json"}, strings.NewReader(""), &approveOutput, &approveError)
	if exit != ExitFailure || !strings.Contains(approveError.String(), "Run 证据无效") {
		t.Fatalf("replacement activation approve exit=%d stdout=%s stderr=%s", exit, approveOutput.String(), approveError.String())
	}

	t.Setenv(selfidentity.ActivationEnv, filepath.Join(root, "missing.json"))
	var stdout, missing bytes.Buffer
	if got := RunContext(context.Background(), []string{"task", "scaffold", "--draft", draftPath}, strings.NewReader(""), &stdout, &missing); got != ExitUnavailable ||
		!strings.Contains(missing.String(), selfidentity.ReasonOptInMissing) {
		t.Fatalf("missing activation exit=%d stderr=%q", got, missing.String())
	}
}

func TestDarwinUnprofiledBuildFailsClosedAtProductionEntry(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin profile gate")
	}
	original := localBuildInfo
	t.Cleanup(func() { localBuildInfo = original })
	localBuildInfo = func() buildinfo.Info {
		return buildinfo.Info{Commit: strings.Repeat("a", 40), SelfProfile: "unprofiled"}
	}
	var stdout, stderr bytes.Buffer
	exit := RunContext(context.Background(), []string{"task", "scaffold", "--draft", "ignored"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitUnavailable || !strings.Contains(stderr.String(), selfidentity.ReasonProfileMismatch) {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
}

func TestVersionJSONReportsExplicitDefaultSelfProfile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if exit := runVersion([]string{"--json"}, &stdout, &stderr); exit != ExitOK {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	var info buildinfo.Info
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.SelfProfile != "unprofiled" {
		t.Fatalf("selfProfile=%q, want unprofiled", info.SelfProfile)
	}
}
