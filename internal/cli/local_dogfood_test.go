package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
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
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
)

func TestMain(m *testing.M) {
	if inheritedTestEntry() {
		os.Exit(0)
	}
	// CI `make test` 注入真实 git head 后，dogfood gate 只应区分「in-process
	// unprofiled 测试 fixture」与「构建产物」。commit 全由 ldflags 决定，不能
	// 参与该区分；产生断言失败的 TestDarwinUnprofiled* 在测试体内临时关闭
	// bypass 以保持 fail-closed 覆盖。
	localDogfoodGateTestBypass = func(info buildinfo.Info) bool {
		return info.SelfProfile == "unprofiled"
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
		"review-freshness-check", "codex-provider-schema-check", "closure-matrix-check", "process-supervisor-v2-canary",
	} {
		if !localDogfoodBootstrapCommand([]string{"internal", check, "--attestation-ready"}, nil) {
			t.Fatalf("fixed read-only internal checker %q was not admitted", check)
		}
	}
	if localDogfoodBootstrapCommand([]string{"internal", "plan-premortem-check"}, nil) ||
		localDogfoodBootstrapCommand([]string{"internal", "unknown-check", "--attestation-ready"}, nil) {
		t.Fatal("internal checker bootstrap admitted a missing handshake or unknown command")
	}
	for _, check := range []string{
		"artifact-attestation-check", "qoder-transcript-check", "plan-premortem-check",
		"review-freshness-check", "codex-provider-schema-check", "closure-matrix-check",
	} {
		t.Run("bootstrap reaches "+check, func(t *testing.T) {
			var output, checkError bytes.Buffer
			exit := RunContext(context.Background(), []string{"internal", check, "--attestation-ready"},
				strings.NewReader("\x00{}"), &output, &checkError)
			if exit == ExitUnavailable || strings.Contains(checkError.String(), "Marshal local dogfood gate 拒绝") {
				t.Fatalf("checker did not reach its handler: exit=%d stdout=%q stderr=%q", exit, output.String(), checkError.String())
			}
		})
	}

	for _, test := range []struct {
		name   string
		args   []string
		reason string
	}{
		{"publication", []string{"task", "publish", "--run", "run-1"}, selfidentity.ReasonPublicationDenied},
		{"remote", []string{"serve"}, selfidentity.ReasonRemoteSurfaceDenied},
		{"internal", []string{"internal", "plan-premortem-check"}, selfidentity.ReasonCredentialedEffectDenied},
		{"detached worker denied", []string{"task", "run", "--run", "local-run", "--detach"}, selfidentity.ReasonCommandDenied},
		{"through verify denied", []string{"task", "run", "--run", "local-run", "--through-verify"}, selfidentity.ReasonCommandDenied},
		{"dead driver recovery denied", []string{"task", "run", "--run", "local-run", "--recover-dead-driver"}, selfidentity.ReasonCommandDenied},
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
	t.Setenv("MARSHAL_QWEN_PATH", "")
	t.Setenv("MARSHAL_QODER_PATH", "")
	t.Setenv("MARSHAL_CODEX_PATH", "")
	t.Setenv("MARSHAL_OPENCODE_PATH", "")
	// zero-selector 后 `task plan` 只有 LaunchCapable 的真实 Pi 0.84.4 才能产生
	// READY Run；没有真实 runtime 的宿主打卡跳过计划阶段（门禁阶段不受影响）。
	requireProductionPiForPlan(t)

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
	writeCLIFixture(t, taskPath, cliPlanningTaskWithWorkers(t, root, taskID, remoteURL, "pi", []any{}))
	unboundRunID := "local-dogfood-unbound-run"
	unboundPolicyPath := filepath.Join(root, "unbound-policy.json")
	unboundPolicy := cliPlanningPolicyWithWorkers(t, taskID, unboundRunID, false, []any{"pi"})
	writeCLIFixture(t, unboundPolicyPath, unboundPolicy)
	var unboundOutput, unboundError bytes.Buffer
	exit = RunContext(context.Background(), []string{"task", "plan", "--task", taskPath, "--policy", unboundPolicyPath, "--run", unboundRunID, "--json"}, strings.NewReader(""), &unboundOutput, &unboundError)
	if exit != ExitFailure || !strings.Contains(unboundError.String(), planning.ErrPolicyLocalBindingMissing) {
		t.Fatalf("unbound local plan exit=%d stdout=%s stderr=%s", exit, unboundOutput.String(), unboundError.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".marshal", "runs", unboundRunID)); !os.IsNotExist(err) {
		t.Fatalf("unbound local plan left side effects: %v", err)
	}
	policy := cliPlanningPolicyWithWorkers(t, taskID, runID, false, []any{"pi"})
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

	// zero-selector cutover 后，普通测试进程内的 `task run` 不再退回 compat
	// executor：frozen pi 进入 sealed 生产分支，而 sealed 组合缺少
	// MARSHAL_PI_RUNTIME/MARSHAL_PI_ENTRYPOINT 即 fail closed。该 Run 的
	// authority 不因此被消耗，attempts 不得创建；真实执行链证据由 strict E2E
	// 与 RC1 canary（固定 bin/marshal + 真实 Pi 0.84.4）承担。
	t.Setenv(selfidentity.ActivationEnv, activationPath)
	const executionRunID = "local-dogfood-execution-run"
	executionPolicy := cliPlanningPolicyWithWorkers(t, taskID, executionRunID, false, []any{"pi"})
	executionPolicy["control"].(map[string]any)["requiredApprovals"] = []any{"plan"}
	executionPolicy["environmentBinding"] = doctor.PolicyEnvironmentBinding
	cliStampPolicyDigest(t, executionPolicy)
	executionPolicyPath := filepath.Join(root, "execution-policy.json")
	writeCLIFixture(t, executionPolicyPath, executionPolicy)
	var executionPlanOutput, executionPlanError bytes.Buffer
	if got := RunContext(context.Background(), []string{"task", "plan", "--task", taskPath, "--policy", executionPolicyPath, "--run", executionRunID, "--json"}, strings.NewReader(""), &executionPlanOutput, &executionPlanError); got != ExitOK {
		t.Fatalf("execution plan exit=%d stderr=%s", got, executionPlanError.String())
	}
	var executionApproveOutput, executionApproveError bytes.Buffer
	if got := RunContext(context.Background(), []string{"task", "approve", "--run", executionRunID, "--gate", "plan", "--json"}, strings.NewReader(""), &executionApproveOutput, &executionApproveError); got != ExitOK {
		t.Fatalf("execution approval exit=%d stderr=%s", got, executionApproveError.String())
	}
	beforeRun, err := runstore.New(filepath.Join(root, ".marshal")).Inspect(executionRunID)
	if err != nil {
		t.Fatal(err)
	}
	var runOutput, runError bytes.Buffer
	runContext, cancelRun := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelRun()
	exit = RunContext(runContext, []string{"task", "run", "--run", executionRunID, "--json"}, strings.NewReader(""), &runOutput, &runError)
	if exit != ExitUnavailable || !strings.Contains(runError.String(), "sealed Pi Runtime 当前配置不可用") {
		t.Fatalf("post-cutover foreground task run exit=%d stdout=%s stderr=%s", exit, runOutput.String(), runError.String())
	}
	if _, statErr := os.Stat(filepath.Join(root, ".marshal", "runs", executionRunID, "attempts")); !os.IsNotExist(statErr) {
		t.Fatalf("fail-closed run created attempts: %v", statErr)
	}
	afterRun, err := runstore.New(filepath.Join(root, ".marshal")).Inspect(executionRunID)
	if err != nil || afterRun.State != beforeRun.State || afterRun.Sequence != beforeRun.Sequence {
		t.Fatalf("fail-closed run consumed Run authority: before=%+v after=%+v err=%v", beforeRun, afterRun, err)
	}

	t.Setenv(selfidentity.ActivationEnv, filepath.Join(root, "missing.json"))
	var stdout, missing bytes.Buffer
	if got := RunContext(context.Background(), []string{"task", "scaffold", "--draft", draftPath}, strings.NewReader(""), &stdout, &missing); got != ExitUnavailable ||
		!strings.Contains(missing.String(), selfidentity.ReasonOptInMissing) {
		t.Fatalf("missing activation exit=%d stderr=%q", got, missing.String())
	}
}

func TestLocalDogfoodClassifiesCompleteFixedServerLifecycle(t *testing.T) {
	for _, test := range []struct {
		command string
		want    string
	}{
		{"collect", selfidentity.CommandControlPlaneCollect},
		{"verify", selfidentity.CommandControlPlaneVerify},
		{"review-packet", selfidentity.CommandControlPlaneReview},
		{"decision", selfidentity.CommandControlPlaneDecision},
	} {
		t.Run(test.command, func(t *testing.T) {
			got, reason := localDogfoodCommandClass([]string{"control-plane", test.command}, nil)
			if got != test.want || reason != "" {
				t.Fatalf("class=%q reason=%q, want class=%q", got, reason, test.want)
			}
		})
	}
}

func TestDarwinUnprofiledBuildFailsClosedAtProductionEntry(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin profile gate")
	}
	original := localBuildInfo
	// 本测试必须证明未命中 production profile 的 built binary 被 fail closed：
	// TestMain 的 unprofiled bypass 不能放行这里，测试体内关闭它。
	originalBypass := localDogfoodGateTestBypass
	localDogfoodGateTestBypass = func(buildinfo.Info) bool { return false }
	t.Cleanup(func() {
		localBuildInfo = original
		localDogfoodGateTestBypass = originalBypass
	})
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

// requireProductionPiForPlan wires the test to the host's real Pi 0.84.4
// executable and exact Node runtime, which is the only LaunchCapable worker
// after the zero-selector cutover. Hosts without the real runtime skip the
// plan-dependent phases instead of falling back to script workers.
func requireProductionPiForPlan(t *testing.T) {
	t.Helper()
	piCandidate, err := exec.LookPath("pi")
	if err != nil {
		t.Skipf("real Pi executable unavailable, skipping plan-dependent phases: %v", err)
	}
	resolvedPi, err := filepath.EvalSymlinks(piCandidate)
	if err != nil {
		t.Skipf("real Pi path cannot be resolved: %v", err)
	}
	nodeCandidate, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("exact Node runtime unavailable, skipping plan-dependent phases: %v", err)
	}
	resolvedNode, err := filepath.EvalSymlinks(nodeCandidate)
	if err != nil {
		t.Skipf("exact Node runtime cannot be resolved: %v", err)
	}
	t.Setenv("MARSHAL_PI_PATH", resolvedPi)
	t.Setenv("MARSHAL_PI_NODE_PATH", resolvedNode)
}
