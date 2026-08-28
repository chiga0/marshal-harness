package sandboxbridge_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter/pi"
	"github.com/chiga0/marshal-harness/internal/agentregistry"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/dispatch"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/provider"
	"github.com/chiga0/marshal-harness/internal/resultbinding"
	"github.com/chiga0/marshal-harness/internal/sandbox/local"
	"github.com/chiga0/marshal-harness/internal/sandboxbridge"
)

// ── scripted pi CLI fixture（in-package，与 internal/adapter/pi 的夹具同构） ──

func fakePiScript(t *testing.T, version, resultPath, sessionID, taskID, runID, attemptID, adapterExec string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then printf '%s\\n' '" + version + "'; exit 0; fi\n" +
		"printf '%s\\n' '{\"type\":\"session\",\"version\":3,\"id\":\"" + sessionID + "\",\"timestamp\":\"2026-08-27T00:00:00.000Z\",\"cwd\":\"'\"$PWD\"'\"}'\n" +
		"printf '%s\\n' '{\"type\":\"agent_start\"}'\n" +
		"mkdir -p \"$(dirname \"" + resultPath + "\")\"\n" +
		"printf '%s\\n' '{" +
		"\"apiVersion\":\"marshal.dev/v1alpha1\",\"kind\":\"WorkerResult\",\"taskId\":\"" + taskID + "\",\"runId\":\"" + runID + "\",\"attemptId\":\"" + attemptID + "\"," +
		"\"adapter\":{\"id\":\"pi\",\"executable\":\"" + adapterExec + "\",\"version\":\"worker-claim\"}," +
		"\"session\":{\"id\":\"" + sessionID + "\",\"resumable\":false}," +
		"\"status\":\"completed\",\"summary\":\"bridge fixture completed\"," +
		"\"declaredChangedFiles\":[\"file.txt\"],\"declaredArtifacts\":[],\"declaredCommands\":[],\"declaredRisks\":[],\"outputTruncated\":false," +
		"\"startedAt\":\"2026-08-04T00:00:00Z\",\"completedAt\":\"2026-08-04T00:00:01Z\"" +
		"}' > \"" + resultPath + "\"\n" +
		"printf '%s\\n' '{\"type\":\"agent_end\",\"messages\":[],\"willRetry\":false}'\n" +
		"printf '%s\\n' '{\"type\":\"agent_settled\"}'\n"
	path := filepath.Join(t.TempDir(), "pi")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

type bridgePiFixture struct {
	adapter            portable1
	adapterExe         string
	worktree           string
	attemptDir         string
	controlRoot        string
	request            domain.Record
	runner             *local.LocalRunner
	bridge             *sandboxbridge.Bridge
	resultDeclaredPath string
	sessionID          string
}

type portable1 struct{ *pi.Adapter }

func newBridgePiFixture(t *testing.T) bridgePiFixture {
	t.Helper()
	worktree := t.TempDir()
	attemptDir := t.TempDir()
	controlRoot := filepath.Join(attemptDir, "control")
	sessionID := "session-bridge-1"
	resultDeclaredPath := filepath.Join(controlRoot, "output", "worker-result.json")

	validator, err := contract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	adapterExe := fakePiScript(t, "0.84.3", resultDeclaredPath, sessionID, "T1", "R1", "A1", "placeholder")
	// 重写脚本以携带真实 adapterExe（identity 由 pi inspect 取得）——重新生成一次。
	adapterExe = fakePiScript(t, "0.84.3", resultDeclaredPath, sessionID, "T1", "R1", "A1", adapterExe)
	adapter, err := pi.New(adapterExe, validator)
	if err != nil {
		t.Fatal(err)
	}

	taskSpec := map[string]any{"worker": map[string]any{"model": "provider/model"}}
	writeJSONFile(t, filepath.Join(controlRoot, "input", "task-spec.json"), taskSpec)
	if err := os.MkdirAll(filepath.Join(controlRoot, "output"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlRoot, "input", "prompt.md"), []byte("完成 bridge fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(map[string]any{
		"apiVersion": "marshal.dev/v1alpha1", "kind": "WorkerRequest", "taskId": "T1", "runId": "R1", "attemptId": "A1", "attemptNumber": 1,
		"specDigest": canonical.DigestBytes([]byte("spec")), "policyDigest": canonical.DigestBytes([]byte("policy")), "capabilityDigest": canonical.DigestBytes([]byte("cap")),
		"agentRegistrationId": resultbinding.AgentRegistrationID(canonical.DigestBytes([]byte("cap"))), "agentCapabilitySnapshotDigest": canonical.DigestBytes([]byte("cap")),
		"baseSha":      strings.Repeat("1", 40),
		"worktreePath": worktree, "controlRoot": controlRoot, "taskSpecPath": "input/task-spec.json", "promptPath": "input/prompt.md", "resultPath": "output/worker-result.json",
		"adapterId": "pi", "executionProfile": "workspace-write", "sessionPolicy": "ephemeral", "attemptTimeoutSeconds": 30, "maxOutputBytes": 1048576, "reviewFindings": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := domain.Record{Kind: domain.KindWorkerRequest, Data: raw}

	runner, err := local.NewLocalRunner(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := sandboxbridge.NewBridge(runner)
	if err != nil {
		t.Fatal(err)
	}
	bridge.WithTranscriptSource(func(allocationID, artifactID string) ([]byte, error) {
		dir, err := runner.AllocationDirectory(allocationID)
		if err != nil {
			return nil, err
		}
		return os.ReadFile(filepath.Join(dir, filepath.FromSlash(artifactID)))
	})

	return bridgePiFixture{
		adapter: portable1{adapter}, adapterExe: adapterExe,
		worktree: worktree, attemptDir: attemptDir, controlRoot: controlRoot,
		request: request, runner: runner, bridge: bridge,
		resultDeclaredPath: resultDeclaredPath, sessionID: sessionID,
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// ── ADR 0052 §1.2 证明：真实 adapter 进程由 Local allocation 承载运行 ────────

func TestRunWorkerExecChainCarriesAgentInAllocation(t *testing.T) {
	f := newBridgePiFixture(t)

	record, err := f.bridge.RunWorker(context.Background(), f.adapter.Adapter, f.request)
	if err != nil {
		t.Fatalf("RunWorker through exec chain: %v", err)
	}
	if record.Kind != domain.KindWorkerResult {
		t.Fatalf("result kind = %q, want WorkerResult", record.Kind)
	}
	var declared struct {
		TaskID    string `json:"taskId"`
		RunID     string `json:"runId"`
		AttemptID string `json:"attemptId"`
		Adapter   struct {
			ID         string `json:"id"`
			Executable string `json:"executable"`
			Version    string `json:"version"`
		} `json:"adapter"`
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(record.Data, &declared); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if declared.TaskID != "T1" || declared.RunID != "R1" || declared.AttemptID != "A1" {
		t.Errorf("identity mismatch: %+v", declared)
	}
	if declared.Adapter.ID != "pi" || declared.Adapter.Version != "0.84.3" {
		t.Errorf("adapter identity = %+v", declared.Adapter)
	}
	if declared.Session.ID != f.sessionID {
		t.Errorf("session = %q, want %q", declared.Session.ID, f.sessionID)
	}

	// allocation-carried 的直接证据：
	// 1) staged transcript 产生于 allocation 并由 provider digest 核对后才归一化；
	// 2) allocation record 已落盘进 attempt 目录（owner 身份可追溯可终结）；
	// 3) allocation 在 Run 返回时已被终结（sweep 对账成立）。
	transcriptPath := filepath.Join(f.controlRoot, "output", "pi-transcript.jsonl")
	transcript, err := os.ReadFile(transcriptPath)
	if err != nil {
		t.Fatalf("normalized transcript missing: %v", err)
	}
	if !strings.Contains(string(transcript), "\"type\":\"session\"") || !strings.Contains(string(transcript), f.sessionID) {
		t.Errorf("transcript lacks fixture session events: %q", string(transcript[:min(len(transcript), 160)]))
	}
	if _, err := os.Stat(filepath.Join(f.controlRoot, "output", "pi-transcript-meta.json")); err != nil {
		t.Errorf("transcript meta missing: %v", err)
	}

	rec, ok, err := sandboxbridge.LoadAllocationRecord(f.attemptDir)
	if err != nil || !ok {
		t.Fatalf("allocation record must exist at attempt dir: ok=%v err=%v", ok, err)
	}
	if rec.AttemptID != "A1" || rec.AllocationID == "" || rec.FencingToken == "" {
		t.Errorf("allocation record incomplete: %+v", rec)
	}

	// ADR 0052 §1.4 admission anchor：双 binding + ResultIngress 接纳锚点
	// 必须持久化在 attempt 目录且可机械复核。
	anchorRaw, err := os.ReadFile(filepath.Join(f.attemptDir, "sandbox-binding-admission.json"))
	if err != nil {
		t.Fatalf("admission anchor missing: %v", err)
	}
	var anchor struct {
		Accepted         bool   `json:"accepted"`
		AttemptID        string `json:"attemptId"`
		FactDigest       string `json:"admissionFactDigest"`
		DrcDigest        string `json:"drcDigest"`
		AgentOK          bool   `json:"agentSideOk"`
		SandboxOK        bool   `json:"sandboxSideOk"`
		EvidenceRequired bool   `json:"evidenceRequired"`
		EvidenceOK       bool   `json:"evidenceOk"`
		EvidenceReason   string `json:"evidenceReason"`
		ReasonField      string `json:"admissionReason"`
	}
	if err := json.Unmarshal(anchorRaw, &anchor); err != nil {
		t.Fatalf("admission anchor unreadable: %v", err)
	}
	if !anchor.Accepted || !anchor.AgentOK || !anchor.SandboxOK {
		t.Errorf("admission anchor rejected: %s", string(anchorRaw))
	}
	if anchor.EvidenceRequired || anchor.EvidenceOK || anchor.EvidenceReason != "not-required-for-ordinary-user" {
		t.Errorf("ordinary-user evidence must be explicit N/A, got: %s", string(anchorRaw))
	}
	if anchor.FactDigest == "" || anchor.DrcDigest == "" || anchor.AttemptID != "A1" {
		t.Errorf("anchor fields incomplete: %s", string(anchorRaw))
	}

	sweep := f.bridge.SweepRegistered(context.Background(), f.runner, sandboxbridge.NewMapResolver(map[string]bool{"R1": true}), time.Minute, time.Now())
	if sweep.Terminated != 1 || len(sweep.Errors) != 0 {
		t.Errorf("sweep of completed attempt must terminate exactly once, got %+v", sweep)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── R6-TOP4: ResultIngress 接入真链后的 stale/replay/伪造负例矩阵 ──────────
//
// 以下测试在 exec-chain 路径上验证 admission anchor 的拒绝分支：当
// DurableAuthority 注入时，被撤销的 agent/sandbox registration 或过期
// lease 的结果必须被拒绝，anchor 落盘 accepted=false。

// fakeDurableAuthority 为 exec-chain admission 注入可控的 DurableAuthority。
type fakeDurableAuthority struct {
	registration   provider.ProviderRegistration
	snapshot       provider.ProviderCapabilitySnapshot
	store          *provider.RegistrationStore
	lease          dispatch.DispatchLease
	leaseOK        bool
	agentRegActive bool
}

// fakeExecChainLease 是一个通过 ValidateLeaseFencing 的最小 lease fixture
// （generation=1 + 确定性 fencingToken + AllocationId）。
func fakeExecChainLease() dispatch.DispatchLease {
	fencingToken := canonical.DigestBytes([]byte("fake-execchain-fencing"))
	return dispatch.DispatchLease{
		Generation:   1,
		FencingToken: fencingToken,
		AllocationId: "alloc-fake-execchain-" + fencingToken[7:23],
		ExpiresAt:    time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339),
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
}

func (f *fakeDurableAuthority) RegistrationStore() *provider.RegistrationStore { return f.store }
func (f *fakeDurableAuthority) LeaseFor(_, _ string) (dispatch.DispatchLease, bool) {
	return f.lease, f.leaseOK
}
func (f *fakeDurableAuthority) CapabilitySnapshot() provider.ProviderCapabilitySnapshot {
	return f.snapshot
}
func (f *fakeDurableAuthority) Registration() provider.ProviderRegistration { return f.registration }
func (f *fakeDurableAuthority) AgentAuthority(registrationID string) (agentregistry.AgentRegistration, agentregistry.AgentCapabilitySnapshot, error) {
	state := agentregistry.LifecycleStateActive
	if !f.agentRegActive {
		state = agentregistry.LifecycleStateRevoked
	}
	reg := agentregistry.AgentRegistration{
		RegistrationID: registrationID, AuthorityNamespaceID: "authority:marshal-local",
		SecurityDomainID: "default/execution/test", Principal: "principal:agent:test",
		ProviderType: agentregistry.ProviderTypeAgent, ProviderName: "pi", ProviderVersion: "0.84.3",
		ProtocolVersion: "marshal-worker/v1alpha1", Scope: "worker",
		IdempotencyKey: "cap:" + canonical.DigestBytes([]byte("cap")), RequestDigest: canonical.DigestBytes([]byte("cap")),
		LifecycleState: state, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
	}
	snap := agentregistry.AgentCapabilitySnapshot{
		SnapshotDigest: canonical.DigestBytes([]byte("cap")), RegistrationID: registrationID,
		ProtocolVersion: "marshal-worker/v1alpha1", ProviderName: "pi", ProviderVersion: "0.84.3",
		Capabilities:               []agentregistry.Capability{agentregistry.CapabilityExecutionProfileWorkspaceWrite},
		ConformanceEvidenceDigests: []string{canonical.DigestBytes([]byte("cap"))},
		SnapshotState:              agentregistry.SnapshotStateActive,
	}
	return reg, snap, nil
}

// TestExecChainAdmissionRejectsRevokedAgentRegistration 验证当 durable
// authority 报告 agent registration 已撤销时，admission anchor 落盘
// accepted=false 且 AgentOK=false。
func TestExecChainAdmissionRejectsRevokedAgentRegistration(t *testing.T) {
	f := newBridgePiFixture(t)
	// 注入 durable authority，agent registration 已撤销。
	f.bridge.WithDurableAuthority(&fakeDurableAuthority{
		registration:   provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		snapshot:       provider.ProviderCapabilitySnapshot{ProviderCapabilitySnapshotDigest: canonical.DigestBytes([]byte("cap"))},
		lease:          fakeExecChainLease(),
		leaseOK:        true,
		agentRegActive: false, // revoked
	})

	record, err := f.bridge.RunWorker(context.Background(), f.adapter.Adapter, f.request)
	// RunWorker 本身可能在 CompleteLaunch 阶段成功（transcript 解码通过），
	// 但 admitCompletedResult 会拒绝。检查错误或 anchor 拒绝。
	if err == nil {
		// 如果没有返回错误（seed fallback 路径），检查 anchor。
		anchorPath := filepath.Join(f.attemptDir, "sandbox-binding-admission.json")
		anchorRaw, readErr := os.ReadFile(anchorPath)
		if readErr != nil {
			t.Fatalf("admission anchor missing: %v (err=%v)", readErr, err)
		}
		var anchor struct {
			Accepted bool   `json:"accepted"`
			AgentOK  bool   `json:"agentSideOk"`
			Reason   string `json:"admissionReason"`
		}
		if json.Unmarshal(anchorRaw, &anchor) != nil {
			t.Fatalf("admission anchor unreadable: %v", readErr)
		}
		if anchor.Accepted || anchor.AgentOK {
			t.Errorf("revoked agent registration must be rejected, got anchor: %s", string(anchorRaw))
		}
		if !strings.Contains(anchor.Reason, "agent registration") {
			t.Errorf("admission reason must mention agent registration, got %q", anchor.Reason)
		}
	}
	_ = record
}

// TestExecChainAdmissionRejectsRevokedSandboxRegistration 验证当 durable
// authority 报告 sandbox provider registration 已撤销时，admission anchor
// 落盘 accepted=false 且 SandboxOK=false。
func TestExecChainAdmissionRejectsRevokedSandboxRegistration(t *testing.T) {
	f := newBridgePiFixture(t)
	// 使用 nil store + 直接返回 false 的 authority（绕过 RegistrationStore
	// 构造的 authority 字段要求；测试目标是 admission 逻辑而非 store 本身）。
	f.bridge.WithDurableAuthority(&fakeDurableAuthority{
		registration:   provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		snapshot:       provider.ProviderCapabilitySnapshot{ProviderCapabilitySnapshotDigest: canonical.DigestBytes([]byte("cap"))},
		store:          nil, // ProviderRegistrationActive 会返回 false
		lease:          dispatch.DispatchLease{ExpiresAt: time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)},
		leaseOK:        true,
		agentRegActive: true,
	})

	_, err := f.bridge.RunWorker(context.Background(), f.adapter.Adapter, f.request)
	if err == nil {
		anchorPath := filepath.Join(f.attemptDir, "sandbox-binding-admission.json")
		anchorRaw, readErr := os.ReadFile(anchorPath)
		if readErr != nil {
			t.Fatalf("admission anchor missing: %v", readErr)
		}
		var anchor struct {
			Accepted  bool   `json:"accepted"`
			SandboxOK bool   `json:"sandboxSideOk"`
			Reason    string `json:"admissionReason"`
		}
		if json.Unmarshal(anchorRaw, &anchor) != nil {
			t.Fatalf("admission anchor unreadable: %v", readErr)
		}
		if anchor.Accepted || anchor.SandboxOK {
			t.Errorf("revoked sandbox registration must be rejected, got anchor: %s", string(anchorRaw))
		}
	}
}

// TestExecChainAdmissionRejectsExpiredLease 验证当 dispatch 冻结的 lease
// 已过期时，admission anchor 落盘 accepted=false。
func TestExecChainAdmissionRejectsExpiredLease(t *testing.T) {
	f := newBridgePiFixture(t)
	f.bridge.WithDurableAuthority(&fakeDurableAuthority{
		registration:   provider.ProviderRegistration{RegistrationId: "registration:local-runner"},
		snapshot:       provider.ProviderCapabilitySnapshot{ProviderCapabilitySnapshotDigest: canonical.DigestBytes([]byte("cap"))},
		lease:          dispatch.DispatchLease{ExpiresAt: "2020-01-01T00:00:00Z"}, // far past
		leaseOK:        true,
		agentRegActive: true,
	})

	_, err := f.bridge.RunWorker(context.Background(), f.adapter.Adapter, f.request)
	if err == nil {
		anchorPath := filepath.Join(f.attemptDir, "sandbox-binding-admission.json")
		anchorRaw, readErr := os.ReadFile(anchorPath)
		if readErr != nil {
			t.Fatalf("admission anchor missing: %v", readErr)
		}
		var anchor struct {
			Accepted bool `json:"accepted"`
		}
		if json.Unmarshal(anchorRaw, &anchor) != nil {
			t.Fatalf("admission anchor unreadable: %v", readErr)
		}
		if anchor.Accepted {
			t.Errorf("expired lease must be rejected, got anchor: %s", string(anchorRaw))
		}
	}
}
