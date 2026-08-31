package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	"github.com/chiga0/marshal-harness/internal/verification"
)

func TestMain(m *testing.M) {
	if inheritedTestEntry() {
		os.Exit(0)
	}
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
	configureQwenAuthFixture(t)
	trackedQwen, qwenInvocations := trackedLocalDogfoodWorkerExecutable(t)
	t.Setenv("MARSHAL_QWEN_PATH", trackedQwen)
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

	// Restore the exact frozen activation and cross the real foreground
	// production entry with the existing deterministic ordinary-user qwen
	// producer. This exercises CLI discovery, approval, Adapter protocol and
	// the complete dispatch/result ingress lineage through VERIFYING.
	t.Setenv(selfidentity.ActivationEnv, activationPath)
	const executionRunID = "local-dogfood-execution-run"
	executionPolicy := cliPlanningPolicy(t, taskID, executionRunID)
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
	var runOutput, runError bytes.Buffer
	runContext, cancelRun := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelRun()
	exit = RunContext(runContext, []string{"task", "run", "--run", executionRunID, "--json"}, strings.NewReader(""), &runOutput, &runError)
	if exit != ExitOK {
		t.Fatalf("foreground task run exit=%d stdout=%s stderr=%s", exit, runOutput.String(), runError.String())
	}
	var executionResult struct {
		State domain.RunState `json:"state"`
	}
	if err := json.Unmarshal(runOutput.Bytes(), &executionResult); err != nil || executionResult.State.State != domain.StateVerifying {
		t.Fatalf("foreground task run did not reach VERIFYING: result=%+v err=%v", executionResult, err)
	}
	attemptsRoot := filepath.Join(root, ".marshal", "runs", executionRunID, "attempts")
	attempts, err := os.ReadDir(attemptsRoot)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("foreground task run exit=%d attempts=%d err=%v stdout=%s stderr=%s", exit, len(attempts), err, runOutput.String(), runError.String())
	}
	attemptDir := filepath.Join(attemptsRoot, attempts[0].Name())
	dispatchRaw, err := os.ReadFile(filepath.Join(attemptDir, "local-self-identity-dispatch.json"))
	if err != nil {
		t.Fatal(err)
	}
	dispatchObservation, err := selfidentity.DecodeObservation(dispatchRaw)
	if err != nil {
		t.Fatalf("production dispatch observation: %v", err)
	}
	ingressRaw, err := os.ReadFile(filepath.Join(attemptDir, "local-self-identity-ingress.json"))
	if err != nil {
		t.Fatal(err)
	}
	ingressObservation, err := selfidentity.DecodeObservation(ingressRaw)
	if err != nil {
		t.Fatalf("production ingress observation: %v", err)
	}
	requestRaw, err := os.ReadFile(filepath.Join(attemptDir, "worker-request.json"))
	var request struct {
		Binding *selfidentity.LocalSelfIdentityBindingV1 `json:"localSelfIdentityBinding"`
	}
	if err != nil || json.Unmarshal(requestRaw, &request) != nil || request.Binding == nil ||
		request.Binding.DispatchObservationDigest != dispatchObservation.ObservationDigest {
		t.Fatalf("production WorkerRequest lacks exact local binding: err=%v data=%s", err, requestRaw)
	}
	events, _, err := runstore.New(filepath.Join(root, ".marshal")).ReadEvents(executionRunID)
	if err != nil || len(events) < 2 {
		t.Fatalf("read production task run events: count=%d err=%v", len(events), err)
	}
	started, completed := events[len(events)-2], events[len(events)-1]
	if started.Type != "worker.started" || completed.Type != "worker.completed" ||
		started.Payload["dispatchObservationDigest"] != dispatchObservation.ObservationDigest ||
		completed.Payload["dispatchObservationDigest"] != dispatchObservation.ObservationDigest ||
		completed.Payload["ingressObservationDigest"] != ingressObservation.ObservationDigest {
		t.Fatalf("production task run lineage: started=%+v completed=%+v", started.Payload, completed.Payload)
	}

	// LD-3B must be reachable through the same production entry rather than
	// through verifier/review helpers. A no-change ordinary-user result leaves
	// a deterministic diagnostic artifact for the final NO_CHANGE decision.
	const reviewTaskID, reviewRunID = "local-dogfood-review-task", "local-dogfood-review-run"
	reviewTask := cliPlanningTask(t, root, reviewTaskID, remoteURL)
	reviewTask["scope"] = map[string]any{"allowPaths": []any{"README.md"}, "denyPaths": []any{}, "allowSubmodules": false, "maxChangedFiles": float64(2), "maxDiffBytes": float64(4096)}
	reviewTask["acceptance"] = map[string]any{"allowNoChange": true, "commands": []any{map[string]any{"id": "no-change-check", "argv": []any{"sh", "-c", "true"}, "cwd": ".", "timeoutSeconds": float64(5), "required": true, "baselinePolicy": "none", "maxLogBytes": float64(4096)}}}
	reviewTask["deliverables"] = []any{map[string]any{"id": "diagnostic", "kind": "diagnostic", "required": true, "pathGlob": "README.md", "minimumCount": float64(1)}}
	reviewTaskPath := filepath.Join(root, "review-task.json")
	writeCLIFixture(t, reviewTaskPath, reviewTask)
	reviewPolicy := cliPlanningPolicy(t, reviewTaskID, reviewRunID)
	reviewPolicy["control"].(map[string]any)["requiredApprovals"] = []any{"plan"}
	reviewPolicy["environmentBinding"] = doctor.PolicyEnvironmentBinding
	cliStampPolicyDigest(t, reviewPolicy)
	reviewPolicyPath := filepath.Join(root, "review-policy.json")
	writeCLIFixture(t, reviewPolicyPath, reviewPolicy)
	noChangeQwen := localDogfoodNoChangeWorkerExecutable(t)
	t.Setenv("MARSHAL_QWEN_PATH", noChangeQwen)
	var reviewStdout, reviewStderr bytes.Buffer
	if got := RunContext(context.Background(), []string{"task", "plan", "--task", reviewTaskPath, "--policy", reviewPolicyPath, "--run", reviewRunID, "--json"}, strings.NewReader(""), &reviewStdout, &reviewStderr); got != ExitOK {
		t.Fatalf("review fixture plan exit=%d stderr=%s", got, reviewStderr.String())
	}
	reviewStdout.Reset()
	reviewStderr.Reset()
	if got := RunContext(context.Background(), []string{"task", "approve", "--run", reviewRunID, "--gate", "plan", "--json"}, strings.NewReader(""), &reviewStdout, &reviewStderr); got != ExitOK {
		t.Fatalf("review fixture approval exit=%d stderr=%s", got, reviewStderr.String())
	}
	reviewStdout.Reset()
	reviewStderr.Reset()
	if got := RunContext(context.Background(), []string{"task", "run", "--run", reviewRunID, "--json"}, strings.NewReader(""), &reviewStdout, &reviewStderr); got != ExitOK {
		t.Fatalf("review fixture run exit=%d stderr=%s", got, reviewStderr.String())
	}
	preVerifyState, err := runstore.New(filepath.Join(root, ".marshal")).Inspect(reviewRunID)
	if err != nil {
		t.Fatal(err)
	}
	preVerifyAttemptDir := filepath.Join(root, ".marshal", "runs", reviewRunID, "attempts", preVerifyState.CurrentAttemptID)
	dispatchPath := filepath.Join(preVerifyAttemptDir, "local-self-identity-dispatch.json")
	ingressPath := filepath.Join(preVerifyAttemptDir, "local-self-identity-ingress.json")
	dispatchFixture, err := os.ReadFile(dispatchPath)
	if err != nil {
		t.Fatal(err)
	}
	ingressFixture, err := os.ReadFile(ingressPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, hostile := range []struct {
		name string
		path string
		raw  []byte
	}{
		{name: "missing dispatch", path: dispatchPath, raw: nil},
		{name: "tampered ingress", path: ingressPath, raw: []byte("{}")},
	} {
		t.Run(hostile.name, func(t *testing.T) {
			original := dispatchFixture
			if hostile.path == ingressPath {
				original = ingressFixture
			}
			replaceImmutableFixture(t, hostile.path, hostile.raw)
			defer replaceImmutableFixture(t, hostile.path, original)
			var deniedOut, deniedErr bytes.Buffer
			got := RunContext(context.Background(), []string{"task", "verify", "--run", reviewRunID, "--json"}, strings.NewReader(""), &deniedOut, &deniedErr)
			assertLocalPhaseDenied(t, got, deniedOut.String(), deniedErr.String(), root)
			unchanged, inspectErr := runstore.New(filepath.Join(root, ".marshal")).Inspect(reviewRunID)
			if inspectErr != nil || !reflect.DeepEqual(unchanged, preVerifyState) {
				t.Fatalf("identity rejection consumed verify state: before=%+v after=%+v err=%v", preVerifyState, unchanged, inspectErr)
			}
			if _, statErr := os.Stat(filepath.Join(root, ".marshal", "runs", reviewRunID, "verification-report.json")); !os.IsNotExist(statErr) {
				t.Fatalf("identity rejection reached verifier persistence: %v", statErr)
			}
		})
	}
	reviewStdout.Reset()
	reviewStderr.Reset()
	now = now.Add(time.Second)
	if got := RunContext(context.Background(), []string{"task", "verify", "--run", reviewRunID, "--json"}, strings.NewReader(""), &reviewStdout, &reviewStderr); got != ExitOK {
		t.Fatalf("local task verify exit=%d stdout=%s stderr=%s", got, reviewStdout.String(), reviewStderr.String())
	}
	if state, err := runstore.New(filepath.Join(root, ".marshal")).Inspect(reviewRunID); err != nil || state.State != domain.StateReviewPending {
		t.Fatalf("local task verify state=%+v err=%v", state, err)
	}
	reviewState, err := runstore.New(filepath.Join(root, ".marshal")).Inspect(reviewRunID)
	if err != nil {
		t.Fatal(err)
	}
	reviewAttemptDir := filepath.Join(root, ".marshal", "runs", reviewRunID, "attempts", reviewState.CurrentAttemptID)
	var report verification.Report
	reportRaw, err := os.ReadFile(filepath.Join(root, ".marshal", "runs", reviewRunID, "verification-report.json"))
	if err != nil || json.Unmarshal(reportRaw, &report) != nil || report.LocalSelfIdentityBinding == nil ||
		report.LocalSelfIdentityBinding.VerificationObservationDigest == "" {
		t.Fatalf("verification report lacks local binding: err=%v report=%s", err, reportRaw)
	}
	verificationName, err := selfidentity.VersionedPhaseObservationName("verification", report.LocalSelfIdentityBinding.VerificationObservationDigest)
	if err != nil {
		t.Fatal(err)
	}
	verificationPath := filepath.Join(reviewAttemptDir, verificationName)
	verificationObservation, err := selfidentity.ReadPhaseObservation(verificationPath)
	if err != nil || report.LocalSelfIdentityBinding.VerificationObservationDigest != verificationObservation.ObservationDigest {
		t.Fatalf("read production verification observation: observation=%+v err=%v", verificationObservation, err)
	}
	var manifest verification.ArtifactManifest
	manifestRaw, err := os.ReadFile(filepath.Join(root, ".marshal", "runs", reviewRunID, "artifact-manifest.json"))
	if err != nil || json.Unmarshal(manifestRaw, &manifest) != nil || !reflect.DeepEqual(report.LocalSelfIdentityBinding, manifest.LocalSelfIdentityBinding) {
		t.Fatalf("artifact manifest lacks exact local binding: err=%v manifest=%s", err, manifestRaw)
	}
	verificationFixture, err := os.ReadFile(verificationPath)
	if err != nil {
		t.Fatal(err)
	}
	reviewPendingBefore, err := runstore.New(filepath.Join(root, ".marshal")).Inspect(reviewRunID)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(selfidentity.ActivationEnv, replacementPath)
	var deniedReviewOut, deniedReviewErr bytes.Buffer
	crossGot := RunContext(context.Background(), []string{"task", "review", "--run", reviewRunID, "--json"}, strings.NewReader(""), &deniedReviewOut, &deniedReviewErr)
	assertLocalPhaseDenied(t, crossGot, deniedReviewOut.String(), deniedReviewErr.String(), root)
	t.Setenv(selfidentity.ActivationEnv, activationPath)
	if _, err := os.Stat(filepath.Join(root, ".marshal", "runs", reviewRunID, "review-packet.json")); !os.IsNotExist(err) {
		t.Fatalf("cross-generation review reached packet persistence: %v", err)
	}
	replaceImmutableFixture(t, verificationPath, []byte("{}"))
	deniedReviewOut.Reset()
	deniedReviewErr.Reset()
	crossGot = RunContext(context.Background(), []string{"task", "review", "--run", reviewRunID, "--json"}, strings.NewReader(""), &deniedReviewOut, &deniedReviewErr)
	assertLocalPhaseDenied(t, crossGot, deniedReviewOut.String(), deniedReviewErr.String(), root)
	replaceImmutableFixture(t, verificationPath, verificationFixture)
	if unchanged, inspectErr := runstore.New(filepath.Join(root, ".marshal")).Inspect(reviewRunID); inspectErr != nil || !reflect.DeepEqual(unchanged, reviewPendingBefore) {
		t.Fatalf("review identity rejection consumed state: before=%+v after=%+v err=%v", reviewPendingBefore, unchanged, inspectErr)
	}
	malformedPacketPath := filepath.Join(root, ".marshal", "runs", reviewRunID, "review-packet.json")
	if err := os.WriteFile(malformedPacketPath, []byte("{malformed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeReviewRecords, err := filepath.Glob(filepath.Join(reviewAttemptDir, "local-self-identity-review-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	deniedReviewOut.Reset()
	deniedReviewErr.Reset()
	malformedGot := RunContext(context.Background(), []string{"task", "review", "--run", reviewRunID, "--json"}, strings.NewReader(""), &deniedReviewOut, &deniedReviewErr)
	assertLocalPhaseDenied(t, malformedGot, deniedReviewOut.String(), deniedReviewErr.String(), root)
	afterReviewRecords, err := filepath.Glob(filepath.Join(reviewAttemptDir, "local-self-identity-review-*.json"))
	if err != nil || !reflect.DeepEqual(beforeReviewRecords, afterReviewRecords) {
		t.Fatalf("malformed existing packet created review observation: before=%v after=%v err=%v", beforeReviewRecords, afterReviewRecords, err)
	}
	if err := os.Remove(malformedPacketPath); err != nil {
		t.Fatal(err)
	}
	reviewStdout.Reset()
	reviewStderr.Reset()
	now = now.Add(time.Second)
	if got := RunContext(context.Background(), []string{"task", "review", "--run", reviewRunID, "--json"}, strings.NewReader(""), &reviewStdout, &reviewStderr); got != ExitOK {
		t.Fatalf("local task review packet exit=%d stdout=%s stderr=%s", got, reviewStdout.String(), reviewStderr.String())
	}
	var packetResult struct {
		PacketDigest string               `json:"packetDigest"`
		Packet       *domain.ReviewPacket `json:"packet"`
	}
	if err := json.Unmarshal(reviewStdout.Bytes(), &packetResult); err != nil || packetResult.Packet == nil || packetResult.Packet.LocalSelfIdentityBinding == nil {
		t.Fatalf("local review packet missing binding: err=%v output=%s", err, reviewStdout.String())
	}
	firstPacketDigest := packetResult.PacketDigest
	canonicalPacket, err := os.ReadFile(malformedPacketPath)
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, canonicalPacket); err != nil {
		t.Fatal(err)
	}
	replaceImmutableFixture(t, malformedPacketPath, compact.Bytes())
	beforeReviewRecords, _ = filepath.Glob(filepath.Join(reviewAttemptDir, "local-self-identity-review-*.json"))
	deniedReviewOut.Reset()
	deniedReviewErr.Reset()
	noncanonicalGot := RunContext(context.Background(), []string{"task", "review", "--run", reviewRunID, "--json"}, strings.NewReader(""), &deniedReviewOut, &deniedReviewErr)
	assertLocalPhaseDenied(t, noncanonicalGot, deniedReviewOut.String(), deniedReviewErr.String(), root)
	afterReviewRecords, _ = filepath.Glob(filepath.Join(reviewAttemptDir, "local-self-identity-review-*.json"))
	if !reflect.DeepEqual(beforeReviewRecords, afterReviewRecords) {
		t.Fatalf("noncanonical existing packet created review observation: before=%v after=%v", beforeReviewRecords, afterReviewRecords)
	}
	replaceImmutableFixture(t, malformedPacketPath, canonicalPacket)
	reviewStdout.Reset()
	reviewStderr.Reset()
	now = now.Add(time.Second)
	if got := RunContext(context.Background(), []string{"task", "review", "--run", reviewRunID, "--json"}, strings.NewReader(""), &reviewStdout, &reviewStderr); got != ExitOK {
		t.Fatalf("local review packet replay exit=%d stdout=%s stderr=%s", got, reviewStdout.String(), reviewStderr.String())
	}
	if err := json.Unmarshal(reviewStdout.Bytes(), &packetResult); err != nil || packetResult.PacketDigest != firstPacketDigest {
		t.Fatalf("local review packet replay drift: first=%s next=%s err=%v", firstPacketDigest, packetResult.PacketDigest, err)
	}
	reviewName, err := selfidentity.VersionedPhaseObservationName("review-1", packetResult.Packet.LocalSelfIdentityBinding.ReviewObservationDigest)
	if err != nil {
		t.Fatal(err)
	}
	reviewObservationPath := filepath.Join(reviewAttemptDir, reviewName)
	reviewObservation, err := selfidentity.ReadPhaseObservation(reviewObservationPath)
	if err != nil || packetResult.Packet.LocalSelfIdentityBinding.ReviewObservationDigest != reviewObservation.ObservationDigest {
		t.Fatalf("local review observation binding mismatch: err=%v packet=%+v", err, packetResult.Packet.LocalSelfIdentityBinding)
	}
	bindingDigest, err := selfidentity.DigestReviewBinding(*packetResult.Packet.LocalSelfIdentityBinding)
	if err != nil {
		t.Fatal(err)
	}
	decision := domain.ReviewDecision{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindReviewDecision,
		TaskID: reviewTaskID, RunID: reviewRunID, ReviewRound: 1,
		Reviewer:   domain.Reviewer{Type: "lead-agent", ID: "local-dogfood-reviewer"},
		SpecDigest: packetResult.Packet.SpecDigest, ReviewPacketDigest: packetResult.PacketDigest,
		VerificationDigest: packetResult.Packet.VerificationDigest, ArtifactManifestDigest: packetResult.Packet.ArtifactManifestDigest,
		EvidenceDigest: packetResult.Packet.EvidenceDigest, LocalSelfIdentityBindingDigest: bindingDigest,
		Verdict: "no_change", Summary: "本地 no-change 诊断已独立验证。",
		BlockingFindings: []domain.Finding{}, NonBlockingFindings: []domain.Finding{},
		PublicationRecommendation: "not-applicable", MergeRecommendation: "do-not-merge", DecidedAt: now,
	}
	decisionPath := filepath.Join(root, "review-decision.json")
	reviewObservationFixture, err := os.ReadFile(reviewObservationPath)
	if err != nil {
		t.Fatal(err)
	}
	writeCLIFixture(t, decisionPath, decision)
	replaceImmutableFixture(t, reviewObservationPath, []byte("{}"))
	var deniedDecisionOut, deniedDecisionErr bytes.Buffer
	decisionGot := RunContext(context.Background(), []string{"task", "review", "--run", reviewRunID, "--decision", decisionPath, "--json"}, strings.NewReader(""), &deniedDecisionOut, &deniedDecisionErr)
	assertLocalPhaseDenied(t, decisionGot, deniedDecisionOut.String(), deniedDecisionErr.String(), root)
	replaceImmutableFixture(t, reviewObservationPath, reviewObservationFixture)
	for _, hostileDigest := range []string{"", canonical.DigestBytes([]byte("cross-generation"))} {
		hostileDecision := decision
		hostileDecision.LocalSelfIdentityBindingDigest = hostileDigest
		writeCLIFixture(t, decisionPath, hostileDecision)
		deniedDecisionOut.Reset()
		deniedDecisionErr.Reset()
		decisionGot = RunContext(context.Background(), []string{"task", "review", "--run", reviewRunID, "--decision", decisionPath, "--json"}, strings.NewReader(""), &deniedDecisionOut, &deniedDecisionErr)
		assertLocalPhaseDenied(t, decisionGot, deniedDecisionOut.String(), deniedDecisionErr.String(), root)
	}
	if unchanged, inspectErr := runstore.New(filepath.Join(root, ".marshal")).Inspect(reviewRunID); inspectErr != nil || !reflect.DeepEqual(unchanged, reviewPendingBefore) {
		t.Fatalf("decision identity rejection consumed state: before=%+v after=%+v err=%v", reviewPendingBefore, unchanged, inspectErr)
	}
	if _, err := os.Stat(filepath.Join(root, ".marshal", "runs", reviewRunID, "outcome.json")); !os.IsNotExist(err) {
		t.Fatalf("decision identity rejection reached Outcome persistence: %v", err)
	}
	writeCLIFixture(t, decisionPath, decision)
	reviewStdout.Reset()
	reviewStderr.Reset()
	if got := RunContext(context.Background(), []string{"task", "review", "--run", reviewRunID, "--decision", decisionPath, "--json"}, strings.NewReader(""), &reviewStdout, &reviewStderr); got != ExitOK {
		t.Fatalf("local task review decision exit=%d stdout=%s stderr=%s", got, reviewStdout.String(), reviewStderr.String())
	}
	terminal, err := runstore.New(filepath.Join(root, ".marshal")).Inspect(reviewRunID)
	if err != nil || terminal.State != domain.StateNoChange {
		t.Fatalf("local review terminal state=%+v err=%v", terminal, err)
	}
	var outcome domain.OutcomeBundle
	outcomeRaw, err := os.ReadFile(filepath.Join(root, ".marshal", "runs", reviewRunID, "outcome.json"))
	if err != nil || json.Unmarshal(outcomeRaw, &outcome) != nil || outcome.LocalSelfIdentityBindingDigest != bindingDigest ||
		outcome.Applicability == nil || !reflect.DeepEqual(*outcome.Applicability, packetResult.Packet.LocalSelfIdentityBinding.Applicability) {
		t.Fatalf("local Outcome lacks review binding: err=%v outcome=%s", err, outcomeRaw)
	}

	// A fresh local Run whose Attempt root cannot become a directory must be
	// rejected before Adapter Probe/Run without projecting the absolute state
	// path or the underlying OS cause through the real CLI boundary.
	const rejectedRunID = "local-dogfood-dispatch-persist-rejected"
	rejectedPolicy := cliPlanningPolicy(t, taskID, rejectedRunID)
	rejectedPolicy["control"].(map[string]any)["requiredApprovals"] = []any{"plan"}
	rejectedPolicy["environmentBinding"] = doctor.PolicyEnvironmentBinding
	cliStampPolicyDigest(t, rejectedPolicy)
	rejectedPolicyPath := filepath.Join(root, "rejected-policy.json")
	writeCLIFixture(t, rejectedPolicyPath, rejectedPolicy)
	var rejectedPlanOutput, rejectedPlanError bytes.Buffer
	if got := RunContext(context.Background(), []string{"task", "plan", "--task", taskPath, "--policy", rejectedPolicyPath, "--run", rejectedRunID, "--json"}, strings.NewReader(""), &rejectedPlanOutput, &rejectedPlanError); got != ExitOK {
		t.Fatalf("rejected fixture plan exit=%d stderr=%s", got, rejectedPlanError.String())
	}
	var rejectedApproveOutput, rejectedApproveError bytes.Buffer
	if got := RunContext(context.Background(), []string{"task", "approve", "--run", rejectedRunID, "--gate", "plan", "--json"}, strings.NewReader(""), &rejectedApproveOutput, &rejectedApproveError); got != ExitOK {
		t.Fatalf("rejected fixture approval exit=%d stderr=%s", got, rejectedApproveError.String())
	}
	stateRoot := filepath.Join(root, ".marshal")
	rejectedBefore, err := runstore.New(stateRoot).Inspect(rejectedRunID)
	if err != nil {
		t.Fatal(err)
	}
	invocationsBefore, err := os.ReadFile(qwenInvocations)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	attemptsPath := filepath.Join(stateRoot, "runs", rejectedRunID, "attempts")
	if err := os.Mkdir(attemptsPath, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(attemptsPath, 0o700) })
	var rejectedRunOutput, rejectedRunError bytes.Buffer
	got := RunContext(context.Background(), []string{"task", "run", "--run", rejectedRunID, "--json"}, strings.NewReader(""), &rejectedRunOutput, &rejectedRunError)
	if got != ExitFailure || rejectedRunOutput.Len() != 0 || !strings.Contains(rejectedRunError.String(), selfidentity.ReasonObjectMismatch) {
		t.Fatalf("dispatch persistence rejection exit=%d stdout=%q stderr=%q", got, rejectedRunOutput.String(), rejectedRunError.String())
	}
	for _, forbidden := range []string{root, attemptsPath, "cause-sentinel-do-not-leak", "not a directory"} {
		if strings.Contains(rejectedRunError.String(), forbidden) || strings.Contains(rejectedRunOutput.String(), forbidden) {
			t.Fatalf("dispatch persistence rejection leaked %q: stdout=%q stderr=%q", forbidden, rejectedRunOutput.String(), rejectedRunError.String())
		}
	}
	invocationsAfter, err := os.ReadFile(qwenInvocations)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if !bytes.Equal(invocationsBefore, invocationsAfter) {
		t.Fatalf("dispatch persistence rejection reached Adapter: before=%q after=%q", invocationsBefore, invocationsAfter)
	}
	rejectedAfter, err := runstore.New(stateRoot).Inspect(rejectedRunID)
	if err != nil {
		t.Fatal(err)
	}
	if rejectedAfter.State != rejectedBefore.State || rejectedAfter.Sequence != rejectedBefore.Sequence ||
		rejectedAfter.AttemptsUsed != rejectedBefore.AttemptsUsed || rejectedAfter.OperationalRetriesUsed != rejectedBefore.OperationalRetriesUsed ||
		rejectedAfter.ReviewRound != rejectedBefore.ReviewRound {
		t.Fatalf("dispatch persistence rejection consumed Run authority: before=%+v after=%+v", rejectedBefore, rejectedAfter)
	}

	t.Setenv(selfidentity.ActivationEnv, filepath.Join(root, "missing.json"))
	var stdout, missing bytes.Buffer
	if got := RunContext(context.Background(), []string{"task", "scaffold", "--draft", draftPath}, strings.NewReader(""), &stdout, &missing); got != ExitUnavailable ||
		!strings.Contains(missing.String(), selfidentity.ReasonOptInMissing) {
		t.Fatalf("missing activation exit=%d stderr=%q", got, missing.String())
	}
}

func trackedLocalDogfoodWorkerExecutable(t *testing.T) (string, string) {
	t.Helper()
	delegate := autoFlowWorkerExecutable(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "qwen")
	invocations := filepath.Join(directory, "invocations.log")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexec %q \"$@\"\n", invocations, delegate)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, invocations
}

func localDogfoodNoChangeWorkerExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qwen")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then printf '0.21.11\n'; exit 0; fi
for last; do :; done
result_path=$(printf '%s\n' "$last" | sed -n 's/.*写入：\([^[:space:]]*\).*/\1/p')
task_id=$(printf '%s\n' "$last" | sed -n 's/.*taskId=\(.*\)、runId=.*/\1/p')
run_id=$(printf '%s\n' "$last" | sed -n 's/.*runId=\(.*\)、attemptId=.*/\1/p')
attempt_id=$(printf '%s\n' "$last" | sed -n 's/.*attemptId=\(.*\)、adapter\.id=.*/\1/p')
cat > "$result_path" <<EOF
{"apiVersion":"marshal.dev/v1alpha1","kind":"WorkerResult","taskId":"$task_id","runId":"$run_id","attemptId":"$attempt_id","adapter":{"id":"qwen","executable":"/fixture/qwen","version":"fixture"},"status":"completed","summary":"no change","declaredChangedFiles":[],"declaredArtifacts":[],"declaredCommands":[],"declaredRisks":[],"outputTruncated":false,"startedAt":"2026-08-27T09:00:00Z","completedAt":"2026-08-27T09:00:01Z"}
EOF
printf '%s\n' '{"type":"system","subtype":"init","session_id":"session-local-no-change","cwd":"'"$PWD"'","qwen_code_version":"0.21.11"}'
printf '%s\n' '{"type":"result","subtype":"success","usage":{"input_tokens":1,"output_tokens":1}}'
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func replaceImmutableFixture(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if raw == nil {
		return
	}
	if err := os.WriteFile(path, raw, 0o400); err != nil {
		t.Fatal(err)
	}
}

func assertLocalPhaseDenied(t *testing.T, exit int, stdout, stderr, secretPath string) {
	t.Helper()
	if exit != ExitFailure || stdout != "" || !strings.Contains(stderr, selfidentity.ReasonCrossProfileEvidence) {
		t.Fatalf("local phase rejection exit=%d stdout=%q stderr=%q", exit, stdout, stderr)
	}
	for _, forbidden := range []string{secretPath, "permission denied", "no such file", "invalid character", "cause-sentinel"} {
		if strings.Contains(stdout, forbidden) || strings.Contains(stderr, forbidden) {
			t.Fatalf("local phase rejection leaked %q: stdout=%q stderr=%q", forbidden, stdout, stderr)
		}
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
