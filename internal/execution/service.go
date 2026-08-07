// Package execution coordinates one bounded Worker attempt. It owns lifecycle
// changes and evidence persistence, but delegates all code edits to an adapter.
package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chiga0/marshal-harness/internal/adapter"
	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/verification"
)

type Input struct {
	StateRoot, RepositoryRoot, RunID string
	Adapter                          port.WorkerAdapter
	Validator                        *contract.Validator
}

type Result struct {
	State        domain.RunState `json:"state"`
	AttemptID    string          `json:"attemptId"`
	WorkerResult json.RawMessage `json:"workerResult,omitempty"`
}

func Run(ctx context.Context, input Input) (Result, error) {
	if input.Adapter == nil || input.Validator == nil {
		return Result{}, errors.New("adapter and validator are required")
	}
	store := runstore.New(input.StateRoot)
	lease, err := store.Acquire(input.RunID)
	if err != nil {
		return Result{}, err
	}
	defer lease.Release()
	state, err := store.Inspect(input.RunID)
	if err != nil {
		return Result{}, err
	}
	if state.State != domain.StateReady && state.State != domain.StateRetryPending && state.State != domain.StateReworkRequested {
		return Result{}, fmt.Errorf("run state %s cannot start a worker attempt", state.State)
	}
	runDir := filepath.Join(input.StateRoot, "runs", input.RunID)
	taskData, err := os.ReadFile(filepath.Join(runDir, "task-spec.json"))
	if err != nil {
		return Result{}, fmt.Errorf("read frozen TaskSpec: %w", err)
	}
	if digest, digestErr := canonical.DigestJSON(taskData); digestErr != nil || digest != state.SpecDigest {
		return Result{}, errors.New("TaskSpec digest does not match frozen run")
	}
	var task domain.TaskSpec
	if err := input.Validator.Validate(domain.KindTask, taskData); err != nil {
		return Result{}, err
	}
	if err := json.Unmarshal(taskData, &task); err != nil {
		return Result{}, err
	}
	if task.Metadata.ID != state.TaskID {
		return Result{}, errors.New("task and run identity do not match")
	}
	taskRepository, err := filepath.EvalSymlinks(task.Repository.Path)
	if err != nil || taskRepository != input.RepositoryRoot {
		return Result{}, errors.New("TaskSpec repository does not match the active repository")
	}
	if task.Worker.ExecutionProfile != "workspace-write" {
		return Result{}, errors.New("M4 only supports workspace-write execution profile")
	}
	if task.Worker.SessionPolicy == "resume" {
		return Result{}, errors.New("session resume is not supported before M6")
	}
	if state.PolicyDigest == "" || state.CapabilityDigest == "" || state.BaseSHA == "" || state.WorktreePath == "" {
		return Result{}, errors.New("run is missing frozen policy, capability, base, or worktree identity")
	}
	policyData, err := os.ReadFile(filepath.Join(runDir, "policy-snapshot.json"))
	if err != nil {
		return Result{}, fmt.Errorf("read frozen PolicySnapshot: %w", err)
	}
	if err := input.Validator.Validate(domain.KindPolicySnapshot, policyData); err != nil {
		return Result{}, err
	}
	if digest, digestErr := canonical.DigestJSON(policyData); digestErr != nil || digest != state.PolicyDigest {
		return Result{}, errors.New("PolicySnapshot digest does not match frozen run")
	}
	capabilityData, err := os.ReadFile(filepath.Join(runDir, "capability-snapshot.json"))
	if err != nil {
		return Result{}, fmt.Errorf("read frozen CapabilitySnapshot: %w", err)
	}
	if err := input.Validator.Validate(domain.KindCapabilitySnapshot, capabilityData); err != nil {
		return Result{}, err
	}
	if digest, digestErr := canonical.DigestJSON(capabilityData); digestErr != nil || digest != state.CapabilityDigest {
		return Result{}, errors.New("CapabilitySnapshot digest does not match frozen run")
	}
	selectedAdapterID, err := adapter.ValidateCapability(domain.Record{Kind: domain.KindCapabilitySnapshot, Data: capabilityData}, task)
	if err != nil {
		return Result{}, err
	}
	if selectedAdapterID != input.Adapter.ID() {
		return Result{}, errors.New("frozen capability snapshot does not match the selected adapter")
	}
	currentCapability, err := input.Adapter.Probe(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := sameCapabilityIdentity(capabilityData, currentCapability.Data); err != nil {
		return Result{}, err
	}
	if uint(state.AttemptsUsed) >= uint(task.Budgets.MaxAttempts) {
		return Result{}, errors.New("attempt budget exhausted")
	}
	reviewFindings, err := loadReviewFindings(runDir, state, input.Validator)
	if err != nil {
		return Result{}, err
	}
	repository, err := gitworktree.Open(input.RepositoryRoot)
	if err != nil {
		return Result{}, err
	}
	worktreeLease, err := repository.Acquire(input.StateRoot, state.TaskID, state.WorktreePath, state.BaseSHA)
	if err != nil {
		return Result{}, err
	}
	defer worktreeLease.Release()

	attemptID, err := domain.NewID("attempt")
	if err != nil {
		return Result{}, err
	}
	attemptDir := filepath.Join(runDir, "attempts", attemptID)
	controlRoot := filepath.Join(attemptDir, "control")
	if err := os.MkdirAll(filepath.Join(controlRoot, "input"), 0o700); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Join(controlRoot, "output"), 0o700); err != nil {
		return Result{}, err
	}
	if err := atomicWrite(filepath.Join(controlRoot, "input", "task-spec.json"), taskData, 0o400); err != nil {
		return Result{}, err
	}
	prompt := renderPrompt(task, state, attemptID, controlRoot, selectedAdapterID, reviewFindings)
	if err := atomicWrite(filepath.Join(controlRoot, "input", "prompt.md"), []byte(prompt), 0o400); err != nil {
		return Result{}, err
	}
	requestMap := map[string]any{
		"apiVersion": string(domain.APIVersionV1Alpha1), "kind": string(domain.KindWorkerRequest),
		"taskId": state.TaskID, "runId": state.RunID, "attemptId": attemptID, "attemptNumber": state.AttemptsUsed + 1,
		"specDigest": state.SpecDigest, "policyDigest": state.PolicyDigest, "capabilityDigest": state.CapabilityDigest,
		"baseSha": state.BaseSHA, "worktreePath": state.WorktreePath, "controlRoot": controlRoot,
		"taskSpecPath": "input/task-spec.json", "promptPath": "input/prompt.md", "resultPath": "output/worker-result.json",
		"adapterId": selectedAdapterID, "executionProfile": task.Worker.ExecutionProfile, "sessionPolicy": task.Worker.SessionPolicy,
		"attemptTimeoutSeconds": task.Budgets.AttemptTimeoutSeconds, "maxOutputBytes": task.Budgets.MaxOutputBytes,
		"reviewFindings": reviewFindings,
	}
	requestData, err := json.Marshal(requestMap)
	if err != nil {
		return Result{}, err
	}
	if err := input.Validator.Validate(domain.KindWorkerRequest, requestData); err != nil {
		return Result{}, err
	}
	if err := atomicWrite(filepath.Join(attemptDir, "worker-request.json"), append(requestData, '\n'), 0o600); err != nil {
		return Result{}, err
	}

	started := time.Now().UTC()
	startEvent, next, err := transition(state, attemptID, "worker.started", domain.StateRunning, started, map[string]any{"adapterId": selectedAdapterID}, lifecycle.Guard{LeaseHeld: true, BudgetAvailable: true})
	if err != nil {
		return Result{}, err
	}
	if err := store.Append(lease, startEvent, state.Sequence); err != nil {
		return Result{}, err
	}
	if err := store.WriteSnapshot(lease, next); err != nil {
		return Result{}, err
	}
	workerResult, runErr := input.Adapter.Run(ctx, domain.Record{Kind: domain.KindWorkerRequest, Data: requestData})
	if runErr != nil {
		failedState, persistErr := recordFailure(store, lease, next, attemptID, task, runErr)
		if persistErr != nil {
			return Result{}, errors.Join(runErr, persistErr)
		}
		return Result{State: failedState, AttemptID: attemptID}, runErr
	}
	if workerResult.Kind != domain.KindWorkerResult || input.Validator.Validate(domain.KindWorkerResult, workerResult.Data) != nil {
		protocolErr := errors.New("adapter returned an invalid WorkerResult")
		failedState, persistErr := recordFailure(store, lease, next, attemptID, task, protocolErr)
		if persistErr != nil {
			return Result{}, errors.Join(protocolErr, persistErr)
		}
		return Result{State: failedState, AttemptID: attemptID}, protocolErr
	}
	var resultIdentity struct {
		TaskID    string `json:"taskId"`
		RunID     string `json:"runId"`
		AttemptID string `json:"attemptId"`
		Adapter   struct {
			ID string `json:"id"`
		} `json:"adapter"`
	}
	if err := json.Unmarshal(workerResult.Data, &resultIdentity); err != nil || resultIdentity.TaskID != state.TaskID || resultIdentity.RunID != state.RunID || resultIdentity.AttemptID != attemptID || resultIdentity.Adapter.ID != selectedAdapterID {
		protocolErr := errors.New("WorkerResult identity does not match the active attempt")
		failedState, persistErr := recordFailure(store, lease, next, attemptID, task, protocolErr)
		if persistErr != nil {
			return Result{}, errors.Join(protocolErr, persistErr)
		}
		return Result{State: failedState, AttemptID: attemptID}, protocolErr
	}
	if err := atomicWrite(filepath.Join(attemptDir, "worker-result.json"), append(workerResult.Data, '\n'), 0o600); err != nil {
		return blockAfterWorker(store, lease, next, attemptID, err)
	}
	if err := worktreeLease.Validate(); err != nil {
		return blockAfterWorker(store, lease, next, attemptID, fmt.Errorf("post-worker worktree identity: %w", err))
	}
	captureLimit := task.Scope.MaxDiffBytes + 1
	if captureLimit <= 1 {
		captureLimit = 64 << 20
	}
	observation, err := verification.ObserveContext(ctx, state.WorktreePath, state.BaseSHA, captureLimit)
	if err != nil {
		return blockAfterWorker(store, lease, next, attemptID, fmt.Errorf("record post-worker snapshot: %w", err))
	}
	observationData, err := json.MarshalIndent(observation, "", "  ")
	if err != nil {
		return Result{}, err
	}
	if err := atomicWrite(filepath.Join(attemptDir, "worktree-snapshot.json"), append(observationData, '\n'), 0o600); err != nil {
		return blockAfterWorker(store, lease, next, attemptID, err)
	}
	completed := time.Now().UTC()
	completeEvent, finalState, err := transition(next, attemptID, "worker.completed", domain.StateVerifying, completed, map[string]any{"snapshotDigest": observation.SnapshotDigest, "diffDigest": observation.DiffDigest}, lifecycle.Guard{LeaseHeld: true, WorkerProtocolComplete: true, SnapshotRecorded: true})
	if err != nil {
		return Result{}, err
	}
	if err := store.Append(lease, completeEvent, next.Sequence); err != nil {
		return Result{}, err
	}
	if err := store.WriteSnapshot(lease, finalState); err != nil {
		return Result{}, err
	}
	return Result{State: finalState, AttemptID: attemptID, WorkerResult: workerResult.Data}, nil
}

func loadReviewFindings(runDir string, state domain.RunState, validator *contract.Validator) ([]map[string]string, error) {
	if state.State != domain.StateReworkRequested {
		return []map[string]string{}, nil
	}
	data, err := os.ReadFile(filepath.Join(runDir, "decisions", fmt.Sprintf("decision-%03d.json", state.ReviewRound)))
	if err != nil {
		return nil, fmt.Errorf("read rework ReviewDecision: %w", err)
	}
	if err := validator.Validate(domain.KindReviewDecision, data); err != nil {
		return nil, err
	}
	var decision domain.ReviewDecision
	if err := json.Unmarshal(data, &decision); err != nil {
		return nil, err
	}
	if decision.TaskID != state.TaskID || decision.RunID != state.RunID || decision.SpecDigest != state.SpecDigest {
		return nil, errors.New("rework ReviewDecision identity does not match the frozen run")
	}
	findings := make([]map[string]string, 0, len(decision.BlockingFindings))
	for _, finding := range decision.BlockingFindings {
		required := finding.RequiredOutcome
		if required == "" {
			required = "解决该阻塞问题并提供新的验证证据。"
		}
		findings = append(findings, map[string]string{"id": finding.ID, "severity": finding.Severity, "description": finding.Description, "requiredOutcome": required})
	}
	return findings, nil
}

func blockAfterWorker(store *runstore.Store, lease *runstore.Lease, state domain.RunState, attemptID string, cause error) (Result, error) {
	event, next, err := transition(state, attemptID, "worker.evidence-failed", domain.StateBlocked, time.Now().UTC(), map[string]any{"error": cause.Error(), "terminalReason": "post-worker evidence could not be persisted"}, lifecycle.Guard{LeaseHeld: true})
	if err == nil {
		err = store.Append(lease, event, state.Sequence)
	}
	if err == nil {
		err = store.WriteSnapshot(lease, next)
	}
	return Result{State: next, AttemptID: attemptID}, errors.Join(cause, err)
}

func sameCapabilityIdentity(frozen, current []byte) error {
	var a, b struct {
		AdapterID        string `json:"adapterId"`
		AdapterVersion   string `json:"adapterVersion"`
		Executable       string `json:"executable"`
		ExecutableDigest string `json:"executableDigest"`
		BinaryVersion    string `json:"binaryVersion"`
		ProbeStatus      string `json:"probeStatus"`
	}
	if json.Unmarshal(frozen, &a) != nil || json.Unmarshal(current, &b) != nil {
		return errors.New("decode capability identity")
	}
	if a != b || b.ProbeStatus != "supported" {
		return errors.New("current adapter capability differs from frozen snapshot")
	}
	return nil
}

func transition(state domain.RunState, attemptID, eventType string, target domain.State, at time.Time, payload map[string]any, guard lifecycle.Guard) (domain.RunEvent, domain.RunState, error) {
	eventID, err := domain.NewID("event")
	if err != nil {
		return domain.RunEvent{}, state, err
	}
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: state.RunID, AttemptID: attemptID, Sequence: state.Sequence + 1, Type: eventType, StateFrom: state.State, StateTo: target, Timestamp: at, Actor: &domain.Actor{Type: "system", ID: "marshal-worker-runner"}, Payload: payload}
	next, err := lifecycle.Reduce(state, event, guard)
	return event, next, err
}

func recordFailure(store *runstore.Store, lease *runstore.Lease, state domain.RunState, attemptID string, task domain.TaskSpec, cause error) (domain.RunState, error) {
	target := domain.StateBlocked
	guard := lifecycle.Guard{LeaseHeld: true}
	if state.OperationalRetriesUsed < uint(task.Budgets.MaxOperationalRetries) && state.AttemptsUsed < uint(task.Budgets.MaxAttempts) {
		target, guard.BudgetAvailable = domain.StateRetryPending, true
	}
	event, next, err := transition(state, attemptID, "worker.failed", target, time.Now().UTC(), map[string]any{"error": cause.Error()}, guard)
	if err != nil {
		return state, err
	}
	if err := store.Append(lease, event, state.Sequence); err != nil {
		return state, err
	}
	if err := store.WriteSnapshot(lease, next); err != nil {
		return state, err
	}
	return next, nil
}

func renderPrompt(task domain.TaskSpec, state domain.RunState, attemptID, controlRoot, adapterID string, findings []map[string]string) string {
	findingsData, _ := json.MarshalIndent(findings, "", "  ")
	prompt := fmt.Sprintf(`# Marshal Worker 任务

你是受 Marshal 控制的 Coding Worker。只完成 TaskSpec 指定的实现、测试和文档，不提交、不推送、不发布，也不要修改 .marshal。

目标：%s

必须关闭的上一轮阻塞问题：%s

TaskSpec：%s
业务 Worktree：%s

完成后必须将符合 WorkerResult JSON Schema 的 JSON 写入：%s
其中 taskId=%s、runId=%s、attemptId=%s、adapter.id=%s。时间字段可先填写合法 RFC3339 时间；Marshal 会以实际观测值覆盖不可信的运行元数据。
`, task.Work.Objective, findingsData, filepath.Join(controlRoot, "input", "task-spec.json"), state.WorktreePath, filepath.Join(controlRoot, "output", "worker-result.json"), state.TaskID, state.RunID, attemptID, adapterID)
	return prompt + workerResultTemplateSection
}

const workerResultTemplateSection = `
## WorkerResult 输出模板

完成后写入的 worker-result.json 必须逐字采用以下 JSON 模板的结构：字段名、apiVersion 与 kind 不得改动，尖括号占位符替换为实际值。

` + "```json\n" + `{
  "apiVersion": "marshal.dev/v1alpha1",
  "kind": "WorkerResult",
  "taskId": "<taskId：使用上文给定的 taskId>",
  "runId": "<runId：使用上文给定的 runId>",
  "attemptId": "<attemptId：使用上文给定的 attemptId>",
  "adapter": {
    "id": "<adapter.id：使用上文给定的 adapter.id>",
    "executable": "<adapter 可执行文件路径>",
    "version": "<adapter 版本号>"
  },
  "status": "<status：completed、blocked、failed、cancelled 之一>",
  "summary": "<本次尝试的简要总结>",
  "declaredChangedFiles": ["<本次实际修改的仓库相对路径，无修改时为空数组>"],
  "declaredArtifacts": [],
  "declaredCommands": [],
  "declaredRisks": [],
  "startedAt": "<startedAt：RFC3339 时间>",
  "completedAt": "<completedAt：RFC3339 时间>"
}
` + "```\n\n" + `模板填写规则：

1. session 为可选字段：ephemeral 会话一律省略整个 session 字段，不得虚构，也不得填写空字符串。
2. declaredChangedFiles、declaredArtifacts、declaredCommands、declaredRisks 可为空数组，但必须存在，不得省略整个字段。
3. startedAt、completedAt 填合法 RFC3339 时间；Marshal 会以实际观测值覆盖不可信的运行元数据。
`

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".marshal-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
