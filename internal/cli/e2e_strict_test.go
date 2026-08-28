package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/sandboxbridge"
)

// TestRealPiStrictE2E 是严格的端到端测试：plan→approve→run→WorkerResult→
// ResultIngress→verify→terminal Outcome→server restart→query/restore。
//
// 与 canary 的关键区别：
// - worker.failed 直接 t.Fatal（不允许"失败也算通过"）
// - 必须走完整 verify → review → terminal outcome 路径
// - 验证 server restart 后 run state 可恢复
//
// 启用方式（默认跳过）：
//
//	MARSHAL_RUN_PI_CANARY=1 MARSHAL_PI_PATH=<pi cli 真实路径> \
//	  go test ./internal/cli/ -run TestRealPiStrictE2E -count=1 -v
func TestRealPiStrictE2E(t *testing.T) {
	// 子进程分支：仅做跨进程 restore 查询，绝不再跑 pi worker。由父测试以
	// STRICT_E2E_QUERY_STATE=1 重新 spawn 本测试触发。
	if os.Getenv("STRICT_E2E_QUERY_STATE") == "1" {
		strictE2EQueryStateHelper(
			os.Getenv("STRICT_E2E_STATE_ROOT"),
			os.Getenv("STRICT_E2E_REPO_DIR"),
			os.Getenv("STRICT_E2E_RUN_ID"),
		)
		return
	}
	if os.Getenv("MARSHAL_RUN_PI_CANARY") == "" {
		t.Skip("set MARSHAL_RUN_PI_CANARY=1 to enable the strict E2E test")
	}
	// 生产权威路径 fail-closed：本测试只断言 embedded/durable authority 闭环
	// （AttemptBinding + exact lookup + admission）。不在 embedded 模式就跳过
	// 而不是退回非生产的 seed 路径——非 embedded 缺 AttemptBinding 是门禁
	// 降级，不得作为「成功」证据。
	if os.Getenv("MARSHAL_EMBEDDED_SANDBOX") != "1" {
		t.Skip("strict E2E requires MARSHAL_EMBEDDED_SANDBOX=1 (production durable-authority path)")
	}
	piPath := os.Getenv("MARSHAL_PI_PATH")
	if piPath == "" {
		t.Skip("MARSHAL_PI_PATH not set")
	}
	if _, err := os.Stat(piPath); err != nil {
		t.Skipf("pi executable unavailable: %v", err)
	}

	restore := localDogfoodGateTestBypass
	localDogfoodGateTestBypass = func(buildinfo.Info) bool { return true }
	t.Cleanup(func() { localDogfoodGateTestBypass = restore })

	repositoryRoot := t.TempDir()
	runGit(t, repositoryRoot, "init", "-q", "-b", "main")
	runGit(t, repositoryRoot, "config", "user.email", "marshal@example.invalid")
	runGit(t, repositoryRoot, "config", "user.name", "Marshal Test")
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("strict e2e fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	runGit(t, repositoryRoot, "remote", "add", "origin", "git@example.invalid:strict-e2e/repo.git")

	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })

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
		"worker":       map[string]any{"preferredAdapter": "pi", "fallbackAdapters": []string{}, "sessionPolicy": "ephemeral", "executionProfile": "workspace-write"},
		"publication":  map[string]any{"required": false, "provider": "none", "mode": "none", "remote": "origin", "baseBranch": "main", "mergePolicy": "never", "requiredChecks": []string{}},
	}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, specBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	mustRun := func(args ...string) string {
		t.Helper()
		stdout, stderr := bytes.Buffer{}, bytes.Buffer{}
		exit := Run(args, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitOK {
			t.Fatalf("%v failed: exit=%d stderr=%s", args, exit, stderr.String())
		}
		return stdout.String()
	}

	runID := "run-strict-e2e"
	mustRun("init")
	scaffoldStdout := mustRun("task", "scaffold", "--draft", specPath, "--preferred-adapter", "pi")
	taskPath := filepath.Join(repositoryRoot, ".marshal", "runs", runID, "task-spec.json")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskPath, []byte(scaffoldStdout), 0o600); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(repositoryRoot, ".marshal", "policy-strict-e2e.json")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := map[string]any{
		"apiVersion":  "marshal.dev/v1alpha1",
		"kind":        "PolicySnapshot",
		"taskId":      "STRICT-E2E",
		"runId":       runID,
		"sources":     []map[string]any{{"scope": "builtin", "digest": "sha256:" + strings.Repeat("b", 64), "required": true}},
		"generatedAt": "2026-08-27T10:00:00Z",
		"control": map[string]any{
			"autonomyProfile":       "supervised",
			"requiredApprovals":     []string{"plan", "publish"},
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
	mustRun("task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID)
	mustRun("task", "approve", "--run", runID, "--gate", "plan", "--actor", "strict-e2e")

	// run — 严格：非零退出直接 fatal
	{
		var stdout, stderr bytes.Buffer
		exit := Run([]string{"task", "run", "--run", runID}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitOK {
			// 打印 attempt 目录内容以辅助诊断
			loc, locErr := repository.Discover(".")
			if locErr == nil {
				attemptsDir := filepath.Join(loc.StateRoot, "runs", runID, "attempts")
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
			}
			t.Fatalf("task run failed: exit=%d\nstdout=%s\nstderr=%s", exit, stdout.String(), stderr.String())
		}
	}

	// 验证 worker.completed（严格：worker.failed 直接 fatal）
	location, err := repository.Discover(".")
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := location.StateRoot
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
	verifyStdout, verifyStderr := bytes.Buffer{}, bytes.Buffer{}
	verifyExit := Run([]string{"task", "verify", "--run", runID}, strings.NewReader(""), &verifyStdout, &verifyStderr)
	t.Logf("verify stdout: %s", verifyStdout.String())
	t.Logf("verify stderr: %s", verifyStderr.String())
	if verifyExit != ExitOK {
		// 独立 Verification 必须 fail-closed：acceptance command 失败（即意外
		// 行为）不得记为「成功」。verify 失败即整个闭环失败。
		t.Fatalf("task verify failed: exit=%d stdout=%s stderr=%s", verifyExit, verifyStdout.String(), verifyStderr.String())
	}
	t.Logf("✓ task verify passed (independent acceptance command asserts deliverable bytes)")

	// status 必须解析为结构化 RunState，且身份与状态字段精确匹配——不允许
	// 只打印不解析、把任意输出当「terminal Outcome」。
	statusJSON := mustRun("task", "status", "--run", runID, "--json")
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

	// 真实跨进程恢复：spawn 一个全新子进程（Go test binary）在全新的 Go
	// runtime 里重新打开持久化 state root 并查询同一 Run——这证明 durable
	// journal 在无共享内存的独立进程间可恢复。子进程在 STRICT_E2E_QUERY_STATE
	// 下只读不写。
	selfBin, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary for cross-process query: %v", err)
	}
	var queryOut bytes.Buffer
	var queryErrOut bytes.Buffer
	sub := exec.Command(selfBin, "-test.run=TestRealPiStrictE2E", "-test.count=1")
	sub.Env = append(os.Environ(),
		"STRICT_E2E_QUERY_STATE=1",
		"STRICT_E2E_STATE_ROOT="+stateRoot,
		"STRICT_E2E_REPO_DIR="+repositoryRoot,
		"STRICT_E2E_RUN_ID="+runID,
	)
	sub.Stdout = &queryOut
	sub.Stderr = &queryErrOut
	if err := sub.Run(); err != nil {
		t.Fatalf("cross-process status query failed: %v\nstdout=%s\nstderr=%s", err, queryOut.String(), queryErrOut.String())
	}
	var restored struct {
		RunID            string `json:"runId"`
		TaskID           string `json:"taskId"`
		State            string `json:"state"`
		CurrentAttemptID string `json:"currentAttemptId"`
	}
	// A directly executed Go test binary appends the testing package's exact
	// PASS trailer after the helper's JSON. Strip only that fixed trailer;
	// anything else remains invalid JSON and fails closed below.
	queryJSON := bytes.TrimSpace(queryOut.Bytes())
	testProcessTrailer := []byte("\nPASS")
	if !bytes.HasSuffix(queryJSON, testProcessTrailer) {
		t.Fatalf("cross-process test binary omitted PASS trailer (raw: %s)", queryOut.String())
	}
	queryJSON = bytes.TrimSpace(bytes.TrimSuffix(queryJSON, testProcessTrailer))
	if err := json.Unmarshal(queryJSON, &restored); err != nil {
		t.Fatalf("cross-process status output not a RunState JSON: %v (raw: %s)", err, queryOut.String())
	}
	if restored.RunID != st.RunID || restored.CurrentAttemptID != st.CurrentAttemptID || restored.State != st.State {
		t.Fatalf("cross-process restored state mismatch: got %+v, want same as in-process %+v", restored, st)
	}
	t.Logf("✓ cross-process restore exact: state=%s currentAttemptId=%s", restored.State, restored.CurrentAttemptID)

	_ = location
}

// strictE2EQueryStateHelper 是 TestRealPiStrictE2E 的子进程分支：在独立的全新
// go runtime 里重新打开持久化 state root 查询 Run，证明 durable journal 的
// 跨进程 restore。由 STRICT_E2E_QUERY_STATE=1 触发；只读，绝不写。
func strictE2EQueryStateHelper(stateRoot, repoDir, runID string) {
	localDogfoodGateTestBypass = func(buildinfo.Info) bool { return true }
	if repoDir != "" {
		_ = os.Chdir(repoDir)
	}
	var stdout, stderr bytes.Buffer
	exit := Run([]string{"task", "status", "--run", runID, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if exit != ExitOK {
		fmt.Fprintf(os.Stderr, "query-state failed: exit=%d stderr=%s", exit, stderr.String())
		os.Exit(1)
	}
	// 只把 RunState JSON 写到自身 stdout（父进程读取它做精确断言）。
	fmt.Print(stdout.String())
}
