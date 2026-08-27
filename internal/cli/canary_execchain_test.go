package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/sandboxbridge"
)

// TestRealPiExecChainCanary 是 R5 的真实 Agent canary：构造最小仓库后，
// standard scaffold→plan→approve→task run 路径使真实 pi CLI（0.84.3）经
// ADR 0052 单节点生产纵切的 worker executor 默认路径执行。
//
// 本 canary 验证 exec-chain 基础设施：真实 pi 进程在 Local allocation 中
// 执行，transcript 被严格解码并落盘，allocation record 锚点存在，事件
// 序列正确。LLM 是否遵从 prompt 写 worker-result.json 不是本 canary 的
// 验证目标——那是模型行为合规问题，不影响 exec-chain 基础设施的正确性。
//
// 启用方式（默认跳过；不影响 CI 与常规回归）：
//
//	MARSHAL_RUN_PI_CANARY=1 MARSHAL_PI_PATH=<pi cli 真实路径> \
//	  go test ./internal/cli/ -run TestRealPiExecChainCanary -count=1 -v
//
// 该测试使用 cli 包内部的 dogfood gate 测试绕行变量（与既有 dogfood 测
// 试同一受让职权），仅本测试文件上下文激活；不对生产 binary 修改任何
// gate 分类。
func TestRealPiExecChainCanary(t *testing.T) {
	if os.Getenv("MARSHAL_RUN_PI_CANARY") == "" {
		t.Skip("set MARSHAL_RUN_PI_CANARY=1 to enable the real pi canary")
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
	if err := os.WriteFile(filepath.Join(repositoryRoot, "README.md"), []byte("canary fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryRoot, "add", "README.md")
	runGit(t, repositoryRoot, "commit", "-q", "-m", "fixture")
	runGit(t, repositoryRoot, "remote", "add", "origin", "git@example.invalid:canary/repo.git")

	if err := os.Chdir(repositoryRoot); err != nil {
		t.Fatal(err)
	}
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })

	const marker = "marshal-r5-execchain-canary-2026-08-27"
	specPath := filepath.Join(t.TempDir(), "canary-task.json")
	markerRel := "docs/r5-canary.md"
	spec := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1",
		"kind":       "Task",
		"metadata":   map[string]any{"id": "R5-EXECCHAIN-CANARY", "title": "R5 真实 Agent canary：pi 经 sandbox 执行链写标记文件"},
		"repository": map[string]any{"path": repositoryRoot, "remote": "origin", "baseRef": strings.TrimSpace(runGitCLI(t, repositoryRoot, "rev-parse", "HEAD")), "expectedRemoteUrl": "git@example.invalid:canary/repo.git"},
		"scope":      map[string]any{"allowPaths": []string{markerRel}, "allowSubmodules": false, "denyPaths": []string{".marshal/**"}, "maxChangedFiles": 1, "maxDiffBytes": 20000},
		"work": map[string]any{
			"objective": "创建一个文件 " + markerRel + "，内容恰好为单行 " + marker + " 加结尾换行。除此之外不做任何事。",
			"constraints": []string{
				"只创建 " + markerRel + " 一个文件；不修改其他文件；不提交、不推送；不使用网络。",
				"若工具调用被拒绝，立即停止并据实提交 WorkerResult。",
			},
			"context":  []string{"输出要求：逐字一行 " + marker + " + 换行。"},
			"nonGoals": []string{"不研究", "不验证自身产物"},
		},
		"acceptance": map[string]any{"allowNoChange": false, "commands": []map[string]any{{
			"id":   "canary-check",
			"argv": []string{"python3", "-B", "-c", "from pathlib import Path; s=Path('" + markerRel + "').read_text(); assert s == " + pyEscape(marker+"\\n") + ", repr(s)"},
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

	run := func(args ...string) (string, int) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		exit := Run(args, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitOK {
			t.Fatalf("%v failed: exit=%d stderr=%s", args, exit, stderr.String())
		}
		return stdout.String(), exit
	}

	runID := "run-r5-execchain-canary"
	// Marshal 仓状态根初始化（.marshal/repo.json 是 plan 读取 repository identity 的前置）。
	run("init")
	scaffoldStdout, _ := run("task", "scaffold", "--draft", specPath, "--preferred-adapter", "pi")
	// scaffold 输出即规范化 TaskSpec（cli 惯例：重定向为任务文件；plan --task
	// 同时接受仓库方向径，统一放置于 .marshal/runs/<run>/task-spec.json 保持与约定）。
	taskPath := filepath.Join(repositoryRoot, ".marshal", "runs", runID, "task-spec.json")
	if err := os.MkdirAll(filepath.Dir(taskPath), 0o700); err != nil {
		t.Fatal(err)
	}
	scaffoldBytes := []byte(scaffoldStdout)
	if err := os.WriteFile(taskPath, scaffoldBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	policyPath := filepath.Join(repositoryRoot, ".marshal", "policy-canary.json")
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := map[string]any{
		"apiVersion": "marshal.dev/v1alpha1",
		"kind":       "PolicySnapshot",
		"taskId":     "R5-EXECCHAIN-CANARY",
		"runId":      runID,
		"sources":    []map[string]any{{"scope": "builtin", "digest": "sha256:" + strings.Repeat("b", 64), "required": true}},
		// policy-snapshot.schema.json 强制 generatedAt；control 块 optional，
		// 但若给出则 required 子字段必须齐全——与 digest 同源单 map 构造。
		"generatedAt": "2026-08-27T10:00:00Z",
		"control": map[string]any{
			// validApprovalGates：非 dogfood 环境下 supervised/balanced 必须
			// 恰好要求 plan+publish 双门（publish 门因 publication.required=false 不会阻塞运行）。
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
	run("task", "plan", "--task", taskPath, "--policy", policyPath, "--run", runID)
	run("task", "approve", "--run", runID, "--gate", "plan", "--actor", "r5-canary")
	{
		var stdout, stderr bytes.Buffer
		exit := Run([]string{"task", "run", "--run", runID}, strings.NewReader(""), &stdout, &stderr)
		if exit != ExitOK {
			t.Logf("task run stdout: %s", stdout.String())
			t.Logf("task run stderr: %s", stderr.String())
		}
	}

	// §1 证明链：exec-chain 基础设施验证。真实 pi 进程经 allocation-carried
	// 路径执行，transcript 被严格解码并落盘，allocation record 锚点存在。
	// LLM 是否遵从 prompt 写 worker-result.json 不是本 canary 的验证目标
	// ——那是模型行为合规问题，不影响 exec-chain 基础设施的正确性。
	location, err := repository.Discover(".")
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := location.StateRoot
	events, err := eventsForRun(t, stateRoot, runID)
	if err != nil {
		t.Fatalf("read run evidence: %v", err)
	}
	for _, e := range events {
		t.Logf("event: type=%s attemptId=%s seq=%d", e.Type, e.AttemptID, e.Sequence)
	}
	attemptID := ""
	var eventType string
	for _, e := range events {
		if e.Type == "worker.completed" || e.Type == "worker.failed" {
			attemptID = e.AttemptID
			eventType = e.Type
			break
		}
	}
	if attemptID == "" {
		t.Fatalf("no worker.completed or worker.failed event found, got events=%v", events)
	}
	attemptDir := filepath.Join(stateRoot, "runs", runID, "attempts", attemptID)
	// 证明 1：pi transcript 被严格解码并落盘（即使 LLM 未写 result，transcript
	// 证明真实 agent 进程在 allocation 中执行了）。
	transcriptPath := filepath.Join(attemptDir, "control", "output", "pi-transcript.jsonl")
	transcript, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("pi transcript missing: %v", err)
	}
	if !strings.Contains(string(transcript), `"type":"session"`) {
		t.Errorf("transcript lacks session header")
	}
	metaPath := filepath.Join(attemptDir, "control", "output", "pi-transcript-meta.json")
	meta, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("pi transcript meta missing: %v", err)
	}
	if !strings.Contains(string(meta), `"eventCount"`) || !strings.Contains(string(meta), `"sessionId"`) {
		t.Errorf("transcript meta incomplete: %s", string(meta))
	}
	t.Logf("transcript meta: %s", string(meta))
	// 证明 2：allocation record 锚点存在（owner 身份可追溯可终结）。
	rec, ok, err := sandboxbridge.LoadAllocationRecord(attemptDir)
	if err != nil || !ok {
		t.Fatalf("allocation record missing: ok=%v err=%v", ok, err)
	}
	if rec.AttemptID != attemptID {
		t.Errorf("allocation record does not match attempt dir: %+v", rec)
	}
	t.Logf("allocation record: allocationID=%s generation=%d", rec.AllocationID, rec.Generation)
	// 证明 3：事件序列正确——worker.started 后必有 worker.completed 或 worker.failed
	if eventType == "worker.completed" {
		t.Log("worker.completed: real pi agent wrote worker-result.json — full闭环成功")
		// admission anchor 仅在成功路径落盘
		if _, err := os.Stat(filepath.Join(attemptDir, "sandbox-binding-admission.json")); err != nil {
			t.Errorf("admission anchor missing on completed path: %v", err)
		}
	} else {
		t.Log("worker.failed: real pi agent did not write worker-result.json — exec-chain 基础设施正确，但 LLM 合规未通过")
		t.Log("注意：本 canary 仅验证 exec-chain 基础设施。完整生产闭环验证请运行 TestRealPiStrictE2E（要求 worker.completed）。")
		// 失败路径下 admission anchor 不落盘是正确的
		if _, err := os.Stat(filepath.Join(attemptDir, "sandbox-binding-admission.json")); err == nil {
			t.Errorf("admission anchor should not exist on failed path")
		}
	}

	_ = location
}

// detachedPolicyDigest 复制 planning/policy.go 的 detached 计算规则
// （测试内联镜像，不由包 export）。
func detachedPolicyDigest(data []byte) (string, error) {
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return "", fmt.Errorf("%w", err)
	}
	document["policyDigest"] = ""
	raw, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	canonicalized, err := canonical.JSON(raw)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(canonicalized), nil
}

func pyEscape(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func eventsForRun(t *testing.T, stateRoot, runID string) ([]runEventRecord, error) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateRoot, "runs", runID, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	var out []runEventRecord
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var e runEventRecord
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

type runEventRecord struct {
	Type      string `json:"type"`
	AttemptID string `json:"attemptId"`
	Sequence  int    `json:"sequence"`
}

var _ = errors.New
