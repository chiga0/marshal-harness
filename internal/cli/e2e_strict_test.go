package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/sandboxbridge"
)

// TestRealPiStrictE2E 是严格的端到端测试：plan→approve→run→WorkerResult→
// ResultIngress→verify→ReviewPacket，并由每条固定 bin/marshal 子进程重开
// durable state，证明真实 Darwin local-dogfood identity gate 与跨进程恢复。
//
// 与基础 canary 的关键区别：
// - 只接受固定 MARSHAL_E2E_BINARY，不调用 package 内 Run，也不 bypass self gate
// - worker.failed 直接 t.Fatal（不允许"失败也算通过"）
// - verify 必须 pass，并生成绑定当前 Evidence 的 ReviewPacket
// - 缺少外部独立 ReviewDecision 时 fail-closed skip，不伪造 reviewer/terminal Outcome
//
// 启用方式（默认跳过）：
//
//	MARSHAL_RUN_PI_CANARY=1 MARSHAL_EMBEDDED_SANDBOX=1 \
//	MARSHAL_E2E_BINARY=<固定 bin/marshal> MARSHAL_E2E_EXPECTED_SOURCE_HEAD=<40hex> \
//	MARSHAL_PI_PATH=<pi cli 真实路径> MARSHAL_E2E_PI_VERSION=<固定版本> \
//	MARSHAL_E2E_PI_MODEL=<固定 provider/model> \
//	  <固定 cli test binary> -test.run TestRealPiStrictE2E -test.count=1 -test.v
func TestRealPiStrictE2E(t *testing.T) {
	if os.Getenv("MARSHAL_RUN_PI_CANARY") == "" {
		t.Skip("set MARSHAL_RUN_PI_CANARY=1 to enable the strict E2E test")
	}
	// 生产权威路径 fail-closed：本测试只断言 embedded/durable authority 闭环
	// （AttemptBinding + exact lookup + admission）。不在 embedded 模式就跳过
	// 而不是退回非生产的 seed 路径——非 embedded 缺 AttemptBinding 是门禁
	// 降级，不得作为「成功」证据。
	if os.Getenv("MARSHAL_EMBEDDED_SANDBOX") != "1" {
		t.Fatal("strict E2E requires MARSHAL_EMBEDDED_SANDBOX=1 (production durable-authority path)")
	}
	piPath := os.Getenv("MARSHAL_PI_PATH")
	if piPath == "" {
		t.Skip("MARSHAL_PI_PATH not set")
	}
	if _, err := os.Stat(piPath); err != nil {
		t.Skipf("pi executable unavailable: %v", err)
	}
	piModel := strings.TrimSpace(os.Getenv("MARSHAL_E2E_PI_MODEL"))
	if piModel == "" {
		t.Fatal("MARSHAL_E2E_PI_MODEL must freeze the exact provider/model")
	}
	piVersion := strings.TrimSpace(os.Getenv("MARSHAL_E2E_PI_VERSION"))
	if piVersion == "" {
		t.Fatal("MARSHAL_E2E_PI_VERSION must freeze the exact Pi identity")
	}
	expectedSourceHead := strings.TrimSpace(os.Getenv("MARSHAL_E2E_EXPECTED_SOURCE_HEAD"))
	if len(expectedSourceHead) != 40 || strings.Trim(expectedSourceHead, "0123456789abcdef") != "" {
		t.Fatal("MARSHAL_E2E_EXPECTED_SOURCE_HEAD must freeze the exact 40-hex source commit")
	}
	marshalPath := strings.TrimSpace(os.Getenv("MARSHAL_E2E_BINARY"))
	if !filepath.IsAbs(marshalPath) || filepath.Clean(marshalPath) != marshalPath {
		t.Fatal("MARSHAL_E2E_BINARY must be an absolute clean path")
	}
	resolvedMarshal, err := filepath.EvalSymlinks(marshalPath)
	if err != nil || resolvedMarshal != marshalPath {
		t.Fatal("MARSHAL_E2E_BINARY must name a fixed non-symlink object")
	}
	marshalInfo, err := os.Stat(marshalPath)
	if err != nil || !marshalInfo.Mode().IsRegular() || marshalInfo.Mode().Perm()&0o111 == 0 {
		t.Fatal("MARSHAL_E2E_BINARY must be an executable regular file")
	}

	repositoryRoot := t.TempDir()
	repositoryRoot, err = filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("strict e2e fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	runGit(t, repositoryRoot, "remote", "add", "origin", "git@example.invalid:strict-e2e/repo.git")

	bootstrap := strictE2ERunner{binary: marshalPath, directory: repositoryRoot,
		environment: strictE2EEnvironment(map[string]string{"MARSHAL_LOCAL_DOGFOOD_ACTIVATION": ""})}
	versionJSON := bootstrap.mustRun(t, "version", "--json")
	var version struct {
		Commit      string `json:"commit"`
		SelfProfile string `json:"selfProfile"`
	}
	if err := json.Unmarshal([]byte(versionJSON), &version); err != nil || version.SelfProfile != "darwin-local-dogfood" ||
		version.Commit != expectedSourceHead {
		t.Fatalf("MARSHAL_E2E_BINARY lacks exact local-dogfood build identity: output=%s err=%v", versionJSON, err)
	}
	activationPath := filepath.Join(t.TempDir(), "local-dogfood-activation.json")
	activation := bootstrap.mustRun(t, "doctor", "--self", "--repository-root", repositoryRoot,
		"--activation-id", "strict-e2e", "--valid-for", "2h")
	if err := os.WriteFile(activationPath, []byte(activation), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := strictE2ERunner{binary: marshalPath, directory: repositoryRoot, environment: strictE2EEnvironment(map[string]string{
		"MARSHAL_LOCAL_DOGFOOD_ACTIVATION": activationPath,
		"MARSHAL_EMBEDDED_SANDBOX":         "1",
		"MARSHAL_PI_PATH":                  piPath,
		"MARSHAL_OPENCODE_PATH":            "",
		"MARSHAL_QWEN_PATH":                "",
		"MARSHAL_QODER_PATH":               "",
		"MARSHAL_CODEX_PATH":               "",
	})}
	doctorJSON := runner.mustRun(t, "doctor", "--json")
	var doctor struct {
		Status                   string         `json:"status"`
		PolicyEnvironmentBinding map[string]any `json:"policyEnvironmentBinding"`
		Workers                  []struct {
			AdapterID     string `json:"adapterId"`
			Outcome       string `json:"outcome"`
			Compatibility string `json:"compatibility"`
			BinaryVersion string `json:"binaryVersion"`
			AuthorityMode string `json:"authorityMode"`
		} `json:"workers"`
	}
	if err := json.Unmarshal([]byte(doctorJSON), &doctor); err != nil || doctor.Status != "ok" || doctor.PolicyEnvironmentBinding == nil {
		t.Fatalf("fixed marshal doctor did not produce local binding: output=%s err=%v", doctorJSON, err)
	}
	piReady := false
	for _, worker := range doctor.Workers {
		if worker.AdapterID == "pi" && worker.Outcome == "registered" && worker.Compatibility == "supported" &&
			worker.BinaryVersion == piVersion && worker.AuthorityMode == "ordinary-user" {
			piReady = true
		}
	}
	if !piReady {
		t.Fatalf("fixed marshal doctor did not admit exact Pi %s ordinary-user profile: %s", piVersion, doctorJSON)
	}

	const marker = "marshal-strict-e2e-2026-08-27"
	specPath := filepath.Join(t.TempDir(), "strict-e2e-task.json")
	markerRel := "docs/strict-e2e.md"
	spec := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1",
		"kind":       "Task",
		"metadata":   map[string]any{"id": "STRICT-E2E", "title": "严格 E2E：pi 全闭环"},
		"repository": map[string]any{"path": repositoryRoot, "remote": "origin", "baseRef": strings.TrimSpace(runGitCLI(t, repositoryRoot, "rev-parse", "HEAD")), "expectedRemoteUrl": "git@example.invalid:strict-e2e/repo.git"},
		"scope":      map[string]any{"allowPaths": []string{markerRel}, "allowSubmodules": false, "denyPaths": []string{".marshal/**"}, "maxChangedFiles": 1, "maxDiffBytes": 20000},
		"work": map[string]any{
			"objective": "创建一个文件 " + markerRel + "，内容恰好为单行 " + marker + " 加结尾换行。完成后必须写入 WorkerResult JSON 文件。",
			"constraints": []string{
				"只创建 " + markerRel + " 一个文件；不修改其他文件；不提交、不推送；不使用网络。",
				"完成后必须写入 WorkerResult JSON 文件——这是 attempt 成功的必要条件。",
			},
			"context":  []string{"输出要求：逐字一行 " + marker + " + 换行。"},
			"nonGoals": []string{"不研究", "不验证自身产物"},
		},
		"acceptance": map[string]any{"allowNoChange": false, "commands": []map[string]any{{
			"id":   "strict-e2e-check",
			"argv": []string{"python3", "-B", "-c", "from pathlib import Path; s=Path('" + markerRel + "').read_text(); assert s == " + pyEscape(marker+"\n") + ", repr(s)"},
			"cwd":  ".", "timeoutSeconds": 30, "maxLogBytes": 10000, "required": true, "baselinePolicy": "none",
		}}},
		"budgets":      map[string]any{"attemptTimeoutSeconds": 900, "runTimeoutSeconds": 1800, "maxAttempts": 2, "maxOperationalRetries": 0, "maxReworkRounds": 1, "maxOutputBytes": 8388608},
		"deliverables": []map[string]any{{"id": "marker", "kind": "documentation", "pathGlob": markerRel, "minimumCount": 1, "required": true}},
		"worker":       map[string]any{"preferredAdapter": "pi", "fallbackAdapters": []string{}, "model": piModel, "sessionPolicy": "ephemeral", "executionProfile": "workspace-write"},
		"publication":  map[string]any{"required": false, "provider": "none", "mode": "none", "remote": "origin", "baseBranch": "main", "mergePolicy": "never", "requiredChecks": []string{}},
	}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, specBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	runID := "run-strict-e2e"
	runner.mustRun(t, "init")
	scaffoldStdout := runner.mustRun(t, "task", "scaffold", "--draft", specPath, "--preferred-adapter", "pi")
	taskPath := filepath.Join(t.TempDir(), "task-spec.json")
	if err := os.WriteFile(taskPath, []byte(scaffoldStdout), 0o600); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(t.TempDir(), "policy-strict-e2e.json")
	policy := map[string]any{
		"apiVersion":         "marshal.dev/v1alpha1",
		"kind":               "PolicySnapshot",
		"taskId":             "STRICT-E2E",
		"runId":              runID,
		"sources":            []map[string]any{{"scope": "builtin", "digest": "sha256:" + strings.Repeat("b", 64), "required": true}},
		"generatedAt":        time.Now().UTC().Format(time.RFC3339),
		"environmentBinding": doctor.PolicyEnvironmentBinding,
		"control": map[string]any{
			"autonomyProfile":       "supervised",
			"requiredApprovals":     []string{"plan"},
			"allowMediatedSteering": false,
			"directPtyPolicy":       "deny",
			"maxSteeringRounds":     0,
		},
		"effective": map[string]any{
			"minimumExecutionProfile":      "workspace-write",
			"requireEnforcedNetworkPolicy": false,
			"networkPolicy":                "unenforced",
			"allowFallbackWorkers":         false,
			"allowWorkerSubagents":         false,
			"allowPublication":             false,
			"allowMerge":                   false,
			"allowGateWaivers":             false,
			"allowedAdapters":              []string{"pi"},
			"environmentAllowlist":         []string{"PATH", "LANG", "TMPDIR", "HOME"},
			"retentionDays":                1,
		},
	}
	policyBytes, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := detachedPolicyDigest(policyBytes)
	if err != nil {
		t.Fatal(err)
	}
	policy["policyDigest"] = digest
	sealedBytes, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, sealedBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	// plan → approve
	runner.mustRun(t, "task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID)
	runner.mustRun(t, "task", "approve", "--run", runID, "--gate", "plan", "--actor", "strict-e2e")

	// run — 严格：非零退出直接 fatal
	{
		result := runner.run("task", "run", "--run", runID)
		if result.err != nil {
			// 打印 attempt 目录内容以辅助诊断
			stateRoot := filepath.Join(repositoryRoot, ".marshal")
			attemptsDir := filepath.Join(stateRoot, "runs", runID, "attempts")
			entries, _ := os.ReadDir(attemptsDir)
			for _, e := range entries {
				attemptDir := filepath.Join(attemptsDir, e.Name())
				controlOutput := filepath.Join(attemptDir, "control", "output")
				files, _ := os.ReadDir(controlOutput)
				for _, f := range files {
					t.Logf("attempt file: %s/%s", e.Name(), f.Name())
				}
				// 打印 transcript 如果存在
				transcriptPath := filepath.Join(controlOutput, "pi-transcript.jsonl")
				if raw, err := os.ReadFile(transcriptPath); err == nil {
					t.Logf("transcript (full): %s", string(raw))
				}
				// 打印 stderr log
				stderrPath := filepath.Join(controlOutput, "pi-stderr.log")
				if raw, err := os.ReadFile(stderrPath); err == nil {
					t.Logf("pi stderr: %s", string(raw))
				}
			}
			t.Fatalf("task run failed: %v\nstdout=%s\nstderr=%s", result.err, result.stdout, result.stderr)
		}
	}

	// 验证 worker.completed（严格：worker.failed 直接 fatal）
	stateRoot := filepath.Join(repositoryRoot, ".marshal")
	events, err := eventsForRun(t, stateRoot, runID)
	if err != nil {
		t.Fatalf("read run evidence: %v", err)
	}
	attemptID := ""
	var eventType string
	for _, e := range events {
		t.Logf("event: type=%s attemptId=%s seq=%d", e.Type, e.AttemptID, e.Sequence)
		if e.Type == "worker.completed" || e.Type == "worker.failed" {
			attemptID = e.AttemptID
			eventType = e.Type
			break
		}
	}
	if attemptID == "" {
		t.Fatalf("no worker.completed or worker.failed event found")
	}
	if eventType != "worker.completed" {
		t.Fatalf("STRICT E2E requires worker.completed, got %s — this is a real failure, not infrastructure proof", eventType)
	}
	t.Logf("✓ worker.completed: real pi agent wrote worker-result.json")

	// 验证 admission anchor 落盘（ResultIngress 接纳成功）
	attemptDir := filepath.Join(stateRoot, "runs", runID, "attempts", attemptID)
	if _, err := os.Stat(filepath.Join(attemptDir, "sandbox-binding-admission.json")); err != nil {
		t.Fatalf("admission anchor missing on completed path: %v", err)
	}
	t.Logf("✓ admission anchor present (ResultIngress 接纳成功)")

	// 验证 allocation record
	rec, ok, err := sandboxbridge.LoadAllocationRecord(attemptDir)
	if err != nil || !ok {
		t.Fatalf("allocation record missing: ok=%v err=%v", ok, err)
	}
	t.Logf("✓ allocation record: allocationID=%s generation=%d", rec.AllocationID, rec.Generation)

	// AttemptBinding 必填（embedded 已强制）：它是 dispatch 时冻结的 durable
	// authority 锚点，缺失即门禁降级，不得作「成功」证据。
	bindingPath := filepath.Join(attemptDir, "attempt-binding.json")
	if _, err := os.Stat(bindingPath); err != nil {
		t.Fatalf("AttemptBinding file missing (production durable-authority path): %v", err)
	}
	t.Logf("✓ AttemptBinding file present")

	// AttemptBinding 内容必须冻结精确 taskId/runId/attemptId 与稳定派生的
	// agent registration id。
	bindingRaw, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatalf("read AttemptBinding: %v", err)
	}
	var binding struct {
		Facts struct {
			TaskID              string `json:"taskId"`
			RunID               string `json:"runId"`
			AttemptID           string `json:"attemptId"`
			AgentRegistrationID string `json:"agentRegistrationId"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(bindingRaw, &binding); err != nil {
		t.Fatalf("decode AttemptBinding: %v", err)
	}
	if binding.Facts.TaskID != "STRICT-E2E" || binding.Facts.RunID != runID || binding.Facts.AttemptID != attemptID {
		t.Fatalf("AttemptBinding identity mismatch: got taskId=%q runId=%q attemptId=%q, want STRICT-E2E/%s/%s",
			binding.Facts.TaskID, binding.Facts.RunID, binding.Facts.AttemptID, runID, attemptID)
	}
	if !strings.HasPrefix(binding.Facts.AgentRegistrationID, "registration:") {
		t.Fatalf("AttemptBinding agentRegistrationId must carry registration: prefix, got %q", binding.Facts.AgentRegistrationID)
	}
	t.Logf("✓ AttemptBinding identity exact: registration=%s", binding.Facts.AgentRegistrationID)

	// verify
	verifyResult := runner.run("task", "verify", "--run", runID)
	t.Logf("verify stdout: %s", verifyResult.stdout)
	t.Logf("verify stderr: %s", verifyResult.stderr)
	if verifyResult.err != nil {
		// 独立 Verification 必须 fail-closed：acceptance command 失败（即意外
		// 行为）不得记为「成功」。verify 失败即整个闭环失败。
		t.Fatalf("task verify failed: %v stdout=%s stderr=%s", verifyResult.err, verifyResult.stdout, verifyResult.stderr)
	}
	t.Logf("✓ task verify passed (independent acceptance command asserts deliverable bytes)")

	// status 必须解析为结构化 RunState，且身份与状态字段精确匹配——不允许
	// 只打印不解析、把任意输出当「terminal Outcome」。
	statusJSON := runner.mustRun(t, "task", "status", "--run", runID, "--json")
	var st struct {
		RunID            string `json:"runId"`
		TaskID           string `json:"taskId"`
		State            string `json:"state"`
		CurrentAttemptID string `json:"currentAttemptId"`
		AttemptsUsed     int    `json:"attemptsUsed"`
	}
	if err := json.Unmarshal([]byte(statusJSON), &st); err != nil {
		t.Fatalf("decode task status JSON: %v (raw: %s)", err, statusJSON)
	}
	if st.RunID != runID || st.TaskID != "STRICT-E2E" {
		t.Fatalf("status identity mismatch: got runId=%q taskId=%q, want %s/STRICT-E2E", st.RunID, st.TaskID, runID)
	}
	if st.CurrentAttemptID != attemptID || st.AttemptsUsed < 1 {
		t.Fatalf("status attempt mismatch: got currentAttemptId=%q attemptsUsed=%d, want %s/>=1", st.CurrentAttemptID, st.AttemptsUsed, attemptID)
	}
	t.Logf("✓ status exact: state=%s currentAttemptId=%s attemptsUsed=%d", st.State, st.CurrentAttemptID, st.AttemptsUsed)

	// runner 的每次调用都是固定 bin/marshal 的全新进程。再次查询同一 Run，
	// 证明 durable journal 在无共享内存的独立进程间可恢复。
	restoredJSON := runner.mustRun(t, "task", "status", "--run", runID, "--json")
	var restored struct {
		RunID            string `json:"runId"`
		TaskID           string `json:"taskId"`
		State            string `json:"state"`
		CurrentAttemptID string `json:"currentAttemptId"`
	}
	if err := json.Unmarshal([]byte(restoredJSON), &restored); err != nil {
		t.Fatalf("cross-process status output not a RunState JSON: %v (raw: %s)", err, restoredJSON)
	}
	if restored.RunID != st.RunID || restored.CurrentAttemptID != st.CurrentAttemptID || restored.State != st.State {
		t.Fatalf("cross-process restored state mismatch: got %+v, want same as in-process %+v", restored, st)
	}
	t.Logf("✓ cross-process restore exact: state=%s currentAttemptId=%s", restored.State, restored.CurrentAttemptID)

	// ReviewPacket 必须由正式 CLI 基于冻结 Verification/ArtifactManifest 和
	// 当前 local self identity 生成。此处只接纳 packet，不在测试内冒充独立
	// reviewer 构造 accept Decision。
	reviewJSON := runner.mustRun(t, "task", "review", "--run", runID, "--json")
	var reviewResult struct {
		Status       string `json:"status"`
		PacketDigest string `json:"packetDigest"`
		Packet       *struct {
			TaskID                   string         `json:"taskId"`
			RunID                    string         `json:"runId"`
			SpecDigest               string         `json:"specDigest"`
			VerificationDigest       string         `json:"verificationDigest"`
			ArtifactManifestDigest   string         `json:"artifactManifestDigest"`
			EvidenceDigest           string         `json:"evidenceDigest"`
			LocalSelfIdentityBinding map[string]any `json:"localSelfIdentityBinding"`
		} `json:"packet"`
	}
	if err := json.Unmarshal([]byte(reviewJSON), &reviewResult); err != nil || reviewResult.Status != "generated" ||
		reviewResult.Packet == nil || reviewResult.Packet.TaskID != "STRICT-E2E" || reviewResult.Packet.RunID != runID ||
		!strings.HasPrefix(reviewResult.PacketDigest, "sha256:") || reviewResult.Packet.SpecDigest == "" ||
		reviewResult.Packet.VerificationDigest == "" || reviewResult.Packet.ArtifactManifestDigest == "" ||
		reviewResult.Packet.EvidenceDigest == "" || reviewResult.Packet.LocalSelfIdentityBinding == nil {
		t.Fatalf("formal ReviewPacket incomplete: output=%s err=%v", reviewJSON, err)
	}
	postPacketJSON := runner.mustRun(t, "task", "status", "--run", runID, "--json")
	var postPacket struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(postPacketJSON), &postPacket); err != nil || postPacket.State != "REVIEW_PENDING" {
		t.Fatalf("ReviewPacket generation did not remain fail-closed at REVIEW_PENDING: output=%s err=%v", postPacketJSON, err)
	}
	t.Logf("✓ formal ReviewPacket generated and current: digest=%s", reviewResult.PacketDigest)
	t.Logf("✓ strict production chain reached the external independent-review boundary; no ReviewProvider/reviewer executor exists, so this canary intentionally passes at REVIEW_PENDING without claiming terminal ACCEPTED: packet=%s", reviewResult.PacketDigest)
}

type strictE2ECommandResult struct {
	stdout string
	stderr string
	err    error
}

type strictE2ERunner struct {
	binary      string
	directory   string
	environment []string
}

func (runner strictE2ERunner) run(args ...string) strictE2ECommandResult {
	command := exec.Command(runner.binary, args...)
	command.Dir = runner.directory
	command.Env = append([]string(nil), runner.environment...)
	command.Stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return strictE2ECommandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func (runner strictE2ERunner) mustRun(t *testing.T, args ...string) string {
	t.Helper()
	result := runner.run(args...)
	if result.err != nil {
		t.Fatalf("fixed marshal %v failed: %v\nstdout=%s\nstderr=%s", args, result.err, result.stdout, result.stderr)
	}
	return result.stdout
}

func strictE2EEnvironment(overrides map[string]string) []string {
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(os.Environ())+len(keys))
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, overridden := overrides[key]; !overridden {
			environment = append(environment, item)
		}
	}
	for _, key := range keys {
		environment = append(environment, key+"="+overrides[key])
	}
	return environment
}
