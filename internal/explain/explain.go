package explain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/recovery"
	"github.com/chiga0/marshal-harness/internal/runstore"
)

const defaultStalenessThreshold = 15 * time.Minute

const unknownFlag = recovery.ObservationNeverReceived

// Experience 是一次 explain 装配的完整事实+决策面（material 全部来自权威账本）。
type Experience struct {
	RunID           string                 `json:"runId"`
	JournalRunState string                 `json:"journalRunState"`
	Input           recovery.RecoveryInput `json:"input"`
	Decision        recovery.Decision      `json:"decision"`
	Explanation     recovery.Explanation   `json:"explanation"`
	Rendered        string                 `json:"rendered"`
	FactsDigest     string                 `json:"factsDigest"`
}

// Assemble 从真实 Run journal/snapshot/attempt 目录把当前处置事实装配成
// recovery.RecoveryInput 并做 Decide。offline explain 只读不改写任何状态。
func Assemble(stateRoot, runID string, now time.Time) (*Experience, error) {
	if strings.TrimSpace(stateRoot) == "" || strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("explain: state root and run id must not be empty")
	}
	store := runstore.New(stateRoot)
	state, err := store.Inspect(runID)
	if err != nil {
		return nil, fmt.Errorf("explain: %w", err)
	}
	events, _, err := store.ReadEvents(runID)
	if err != nil {
		return nil, fmt.Errorf("explain: %w", err)
	}

	attemptID := latestAttempt(events)
	attemptTerminated := isAttemptTerminal(state.State)
	pendingCommandID := ""
	if state.State == domain.StateRunning {
		inFlight, proofErr := anyAttemptInFlight(events, state, now)
		if proofErr != nil {
			return nil, proofErr
		}
		if inFlight {
			pendingCommandID = "cmd:attempt-result:" + attemptID
		}
	}
	// 命令 digest 与 broker 事实无关时省略（作 digest 需要 pending 存在）。
	commandDigest := ""
	if pendingCommandID != "" {
		commandDigest = canonical.DigestBytes([]byte("pending-followup:" + pendingCommandID))
	}

	leaseState, observation := deriveLeaseAndObservation(state, events, now, defaultStalenessThreshold)
	bindings := deriveBindings(stateRoot, runID, attemptID)
	staleResult := anyFileExists(filepath.Join(stateRoot, "runs", runID, "attempts", attemptID, "quarantinedOutputs"))
	sideEffectDeclared := publicationRequired(filepath.Join(stateRoot, "runs", runID, "task-spec.json"))

	input := recovery.RecoveryInput{
		Ledger: recovery.LedgerView{
			AttemptID:          attemptID,
			PendingCommandID:   pendingCommandID,
			CommandDigest:      commandDigest,
			Lease:              leaseState,
			Generation:         1,
			SideEffectDeclared: sideEffectDeclared,
			AttemptTerminal:    attemptTerminated,
		},
		Observation:          observation,
		Bindings:             bindings,
		StaleResultPresented: staleResult,
	}

	decision, explanation, err := recovery.Decide(input)
	digest := canonical.DigestBytes([]byte(input.Ledger.AttemptID + "|" + string(input.Ledger.Lease) + "|" + string(input.Observation)))
	if err != nil {
		return &Experience{RunID: runID, JournalRunState: string(state.State), Input: input, FactsDigest: digest, Rendered: "<unavailable>"}, fmt.Errorf("explain: assemble decision: %w", err)
	}
	return &Experience{
		RunID:           runID,
		JournalRunState: string(state.State),
		Input:           input,
		Decision:        decision,
		Explanation:     explanation,
		Rendered:        recovery.Render(explanation),
		FactsDigest:     digest,
	}, nil
}

func latestAttempt(events []domain.RunEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].AttemptID != "" && (events[i].Type == "worker.started" || events[i].Type == "worker.failed" || events[i].Type == "worker.completed") {
			return events[i].AttemptID
		}
	}
	return ""
}

func isAttemptTerminal(state domain.State) bool {
	switch state {
	case domain.StateRejected, domain.StateAborted, domain.StateNoChange, domain.StatePublished, domain.StateAccepted, domain.StateReviewPending, domain.StateVerifying, domain.StateReworkRequested, domain.StatePublishing, domain.StateCIPending, domain.StateBlocked, domain.StateRetryPending, domain.StateReady, domain.StatePlanned, domain.StateCreated:
		// Ready/Planned/Created（无 attempt 启动过）、RETRY_PENDING（失败已
		// 入账的终态出路）、以及 attempt 已完成的 post-completion 生命周期
		// 阶段（结果已入账），账本已收敛：不能重启。
		return true
	default:
		return false
	}
}

// deriveLeaseAndObservation 把 RunState 形态化为 current lease 与最终
// Inspect/Reconcile 事实：
//   - RUNNING fresh + in-flight pending → active lease + executing；
//   - RUNNING stale（driver 死亡窗口）→ 无法区分进程死活，按不能证明安全：
//     lease revoked + unreachable observation（Decide 据此 fence+new attempt）；
//   - RETRY_PENDING → 失败已入账：active lease + terminal-failure；
//   - RUNNING 早期事件缺失 → unknown。
func deriveLeaseAndObservation(state domain.RunState, events []domain.RunEvent, now time.Time, staleness time.Duration) (recovery.LeaseState, recovery.ObservationKind) {
	if state.State != domain.StateRunning {
		return recovery.LeaseActive, recovery.ObservationTerminalSuccess
	}
	if len(events) == 0 {
		return recovery.LeaseActive, recovery.ObservationNeverReceived
	}
	last := events[len(events)-1]
	age := now.Sub(last.Timestamp)
	inFlight := strings.HasPrefix(last.Type, "worker.") && last.Type == "worker.started"
	switch {
	case age > staleness:
		return recovery.LeaseRevoked, recovery.ObservationUnreachable
	case inFlight:
		return recovery.LeaseActive, recovery.ObservationExecuting
	case last.Type == "worker.failed":
		return recovery.LeaseRevoked, recovery.ObservationTerminalFailure
	default:
		return recovery.LeaseActive, unknownFlag
	}
}

// deriveBindings 从 attempt 目录的双 binding admission anchor 读回双面
// recheck 状态；无 anchor（legacy 路径 run）按未受损处理。
func deriveBindings(stateRoot, runID, attemptID string) recovery.BindingView {
	if attemptID == "" {
		return recovery.BindingView{AgentOK: true, SandboxOK: true}
	}
	var anchor struct {
		Accepted  bool `json:"accepted"`
		AgentOK   bool `json:"agentSideOk"`
		SandboxOK bool `json:"sandboxSideOk"`
	}
	raw, err := os.ReadFile(filepath.Join(stateRoot, "runs", runID, "attempts", attemptID, "sandbox-binding-admission.json"))
	if err != nil {
		return recovery.BindingView{AgentOK: true, SandboxOK: true}
	}
	if json.Unmarshal(raw, &anchor) != nil {
		return recovery.BindingView{AgentOK: false, SandboxOK: false}
	}
	if !anchor.Accepted {
		return recovery.BindingView{AgentOK: anchor.AgentOK, SandboxOK: anchor.SandboxOK}
	}
	return recovery.BindingView{AgentOK: anchor.AgentOK, SandboxOK: anchor.SandboxOK}
}

func publicationRequired(taskSpecPath string) bool {
	raw, err := os.ReadFile(taskSpecPath)
	if err != nil {
		return false
	}
	var spec struct {
		Publication struct {
			Required bool `json:"required"`
		} `json:"publication"`
	}
	if json.Unmarshal(raw, &spec) != nil {
		return false
	}
	return spec.Publication.Required
}

func anyFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func anyAttemptInFlight(events []domain.RunEvent, state domain.RunState, now time.Time) (bool, error) {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if !strings.HasPrefix(e.Type, "worker.") {
			continue
		}
		if e.Type == "worker.started" {
			return true, nil
		}
		if e.Type == "worker.completed" || e.Type == "worker.failed" {
			return false, nil
		}
	}
	return false, nil
}

// Render 渲染 explain 全文（渲染不变性由 recovery.Render 保证）。
func Render(x *Experience) string {
	if x == nil {
		return "<nil experience>"
	}
	if x.Rendered != "" {
		return x.Rendered
	}
	return "<no render>"
}
