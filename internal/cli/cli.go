// Package cli implements Marshal's thin command-line boundary.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
	cleanupservice "github.com/chiga0/marshal-harness/internal/cleanup"
	"github.com/chiga0/marshal-harness/internal/contract"
	controlplane "github.com/chiga0/marshal-harness/internal/control"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/execution"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/launcher"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/publication"
	githubpublisher "github.com/chiga0/marshal-harness/internal/publisher/github"
	"github.com/chiga0/marshal-harness/internal/reconciliation"
	"github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/verification"
)

// Process exit codes are stable CLI contract.
const (
	ExitOK          = 0
	ExitFailure     = 1
	ExitUsage       = 2
	ExitUnavailable = 3

	maxContractInputBytes int64 = 32 << 20
)

var taskCommands = []string{
	"plan",
	"approve",
	"run",
	"status",
	"verify",
	"review",
	"rework",
	"publish",
	"accept",
	"abort",
	"cleanup",
}

// Run executes one CLI invocation without granting Worker or Publisher
// capabilities to the CLI boundary itself.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return RunContext(context.Background(), args, stdin, stdout, stderr)
}

// RunContext executes one CLI invocation and propagates cancellation into
// verifier process groups.
func RunContext(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stderr)
		return ExitUsage
	}

	switch args[0] {
	case "help", "-h", "--help":
		writeUsage(stdout)
		return ExitOK
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr)
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "contract":
		return runContract(args[1:], stdin, stdout, stderr)
	case "task":
		return runTask(ctx, args[1:], stdout, stderr)
	case "__launch":
		return runInternalLaunch(args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "未知命令 %q。\n", args[0])
		writeUsage(stderr)
		return ExitUsage
	}
}

func runInternalLaunch(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "内部启动调用无效。")
		return ExitUsage
	}
	if err := launcher.ExecutePath(args[0], time.Now().UTC()); err != nil {
		// Detailed errors may contain local paths. Terminal output remains terse;
		// the Attempt keeps the authoritative diagnostic.
		fmt.Fprintln(stderr, "内部 Worker 启动失败。")
		return ExitFailure
	}
	return ExitOK
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "version 不接受位置参数。")
		return ExitUsage
	}
	info := buildinfo.Current()
	if *jsonOutput {
		if err := writeJSON(stdout, info); err != nil {
			fmt.Fprintf(stderr, "输出版本信息失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "marshal %s (%s, %s, %s/%s)\n", info.Version, info.Commit, info.GoVersion, info.OS, info.Arch)
	return ExitOK
}

type doctorWorker struct {
	AdapterID           string `json:"adapterId"`
	EnvironmentVariable string `json:"environmentVariable"`
	Configured          bool   `json:"configured"`
	Registered          bool   `json:"registered"`
	Outcome             string `json:"outcome"`
	Compatibility       string `json:"compatibility"`
	AdapterVersion      string `json:"adapterVersion,omitempty"`
	BinaryVersion       string `json:"binaryVersion,omitempty"`
}

type doctorReport struct {
	Status          string                       `json:"status"`
	Build           buildinfo.Info               `json:"build"`
	ContractSchemas int                          `json:"contractSchemas"`
	WorkerAdapters  int                          `json:"workerAdapters"`
	Milestone       string                       `json:"milestone"`
	Workers         []doctorWorker               `json:"workers"`
	Run             *reconciliation.Report       `json:"run,omitempty"`
	Repair          *reconciliation.RepairResult `json:"repair,omitempty"`
}

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	runID := flags.String("run", "", "核验指定 Run 的本地证据")
	repair := flags.Bool("repair", false, "显式修复可证明的本地 Snapshot")
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "doctor 不接受位置参数。")
		return ExitUsage
	}
	if *runID != "" {
		if err := domain.ValidateID(*runID); err != nil {
			fmt.Fprintln(stderr, "doctor 失败：Run ID 无效。")
			return ExitUsage
		}
	}
	if *repair && *runID == "" {
		fmt.Fprintln(stderr, "doctor --repair 必须同时指定 --run RUN_ID。")
		return ExitUsage
	}
	application, err := app.New()
	if err != nil {
		fmt.Fprintln(stderr, "doctor 失败：应用初始化失败。")
		return ExitFailure
	}
	runtime, err := app.NewWorkerRuntime(os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "doctor 失败：Worker Runtime 初始化失败。")
		return ExitFailure
	}
	workers := doctorWorkers(ctx, runtime)
	report := doctorReport{
		Status:          "ok",
		Build:           buildinfo.Current(),
		ContractSchemas: application.ContractCount(),
		WorkerAdapters:  len(runtime.Registry().IDs()),
		Milestone:       "6",
		Workers:         workers,
	}
	if *runID != "" {
		location, err := repository.Discover(".")
		if err != nil || location.ValidateIdentity() != nil {
			fmt.Fprintln(stderr, "doctor 失败：无法验证仓库身份。")
			return ExitFailure
		}
		input := reconciliation.Input{StateRoot: location.StateRoot, RepositoryRoot: location.RepositoryRoot, RunID: *runID, Validator: runtime.Validator()}
		if *repair {
			repairResult, err := reconciliation.Repair(ctx, input, time.Now().UTC())
			if err != nil {
				fmt.Fprintln(stderr, "doctor 失败：本地 Run 修复未完成。")
				return ExitFailure
			}
			report.Repair = &repairResult
			report.Run = &repairResult.Report
		} else {
			runReport, err := reconciliation.Inspect(ctx, input)
			if err != nil {
				fmt.Fprintln(stderr, "doctor 失败：无法核验本地 Run 证据。")
				return ExitFailure
			}
			report.Run = &runReport
		}
		if report.Run.Status != "ok" {
			report.Status = report.Run.Status
		}
	}
	exitCode := ExitOK
	if report.Run != nil && report.Run.Status == "blocked" {
		exitCode = ExitFailure
	}
	if *jsonOutput {
		if err := writeJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "输出 doctor 报告失败：%v\n", err)
			return ExitFailure
		}
		return exitCode
	}
	fmt.Fprintf(stdout, "状态：%s\nSchema：%d 份已编译\n已注册 Worker Adapter：%d\n", report.Status, report.ContractSchemas, report.WorkerAdapters)
	for _, worker := range report.Workers {
		fmt.Fprintf(stdout, "Worker %s：%s / %s", worker.AdapterID, worker.Outcome, worker.Compatibility)
		if worker.BinaryVersion != "" {
			fmt.Fprintf(stdout, " (%s)", worker.BinaryVersion)
		}
		fmt.Fprintln(stdout)
	}
	if report.Run != nil {
		fmt.Fprintf(stdout, "Run %s：%s（Snapshot %d / Journal %d）\n", report.Run.RunID, report.Run.Status, report.Run.SnapshotSequence, report.Run.JournalSequence)
		for _, finding := range report.Run.Findings {
			fmt.Fprintf(stdout, "诊断：%s / %s / repairable=%t\n", finding.Code, finding.Severity, finding.Repairable)
		}
	}
	if report.Repair != nil {
		fmt.Fprintf(stdout, "修复：%s", report.Repair.Outcome)
		if report.Repair.EventID != "" {
			fmt.Fprintf(stdout, "（Event %s）", report.Repair.EventID)
		}
		fmt.Fprintln(stdout)
	}
	return exitCode
}

func doctorWorkers(ctx context.Context, runtime *app.WorkerRuntime) []doctorWorker {
	configurations := runtime.Configurations()
	workers := make([]doctorWorker, 0, len(configurations))
	for _, configuration := range configurations {
		result := doctorWorker{
			AdapterID:           configuration.AdapterID,
			EnvironmentVariable: configuration.EnvironmentVariable,
			Configured:          configuration.Configured,
			Registered:          configuration.Registered,
			Outcome:             configuration.Outcome,
			Compatibility:       "not-probed",
		}
		if !configuration.Registered || ctx.Err() != nil {
			workers = append(workers, result)
			continue
		}
		result.Compatibility = "probe-failed"
		worker, err := runtime.Registry().Resolve(configuration.AdapterID)
		if err != nil {
			workers = append(workers, result)
			continue
		}
		snapshot, err := worker.Probe(ctx)
		if err != nil {
			workers = append(workers, result)
			continue
		}
		if snapshot.Kind != domain.KindCapabilitySnapshot || runtime.Validator().Validate(domain.KindCapabilitySnapshot, snapshot.Data) != nil {
			workers = append(workers, result)
			continue
		}
		var identity struct {
			AdapterID      string `json:"adapterId"`
			AdapterVersion string `json:"adapterVersion"`
			BinaryVersion  string `json:"binaryVersion"`
			ProbeStatus    string `json:"probeStatus"`
		}
		if json.Unmarshal(snapshot.Data, &identity) != nil || identity.AdapterID != configuration.AdapterID ||
			(identity.ProbeStatus != "supported" && identity.ProbeStatus != "unsupported") {
			workers = append(workers, result)
			continue
		}
		result.Compatibility = identity.ProbeStatus
		result.AdapterVersion = identity.AdapterVersion
		result.BinaryVersion = identity.BinaryVersion
		workers = append(workers, result)
	}
	return workers
}

func runContract(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "validate" {
		fmt.Fprintln(stderr, "用法：marshal contract validate [--schema NAME] <PATH|->")
		return ExitUsage
	}
	flags := flag.NewFlagSet("contract validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	schemaName := flags.String("schema", "", "显式 Schema 名称；省略时从 kind 检测")
	if err := flags.Parse(args[1:]); err != nil {
		return ExitUsage
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "contract validate 需要一个文件路径或 -。")
		return ExitUsage
	}
	data, err := readInput(flags.Arg(0), stdin)
	if err != nil {
		fmt.Fprintf(stderr, "读取契约失败：%v\n", err)
		return ExitFailure
	}
	application, err := app.New()
	if err != nil {
		fmt.Fprintf(stderr, "初始化失败：%v\n", err)
		return ExitFailure
	}
	validatedKind := ""
	if *schemaName == "" {
		record, validateErr := application.ValidateDocument(data)
		if validateErr != nil {
			fmt.Fprintf(stderr, "契约无效：%v\n", validateErr)
			return ExitFailure
		}
		validatedKind = string(record.Kind)
	} else {
		descriptor, resolveErr := contract.DescriptorByName(*schemaName)
		if resolveErr != nil {
			fmt.Fprintf(stderr, "%v\n", resolveErr)
			return ExitUsage
		}
		if validateErr := application.ValidateContract(descriptor.Kind, data); validateErr != nil {
			fmt.Fprintf(stderr, "契约无效：%v\n", validateErr)
			return ExitFailure
		}
		validatedKind = string(descriptor.Kind)
	}
	fmt.Fprintf(stdout, "有效：%s\n", validatedKind)
	return ExitOK
}

func runInit(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return ExitUsage
	}
	state, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "初始化失败：%v\n", err)
		return ExitFailure
	}
	if err := state.Init(); err != nil {
		fmt.Fprintf(stderr, "初始化失败：%v\n", err)
		return ExitFailure
	}
	result := struct {
		RepositoryRoot string `json:"repositoryRoot"`
		StateRoot      string `json:"stateRoot"`
	}{state.RepositoryRoot, state.StateRoot}
	if *jsonOutput {
		if err := writeJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "输出初始化结果失败：%v\n", err)
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "已初始化 Marshal 状态目录：%s\n", state.StateRoot)
	}
	return ExitOK
}

func runTask(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || !slices.Contains(taskCommands, args[0]) {
		fmt.Fprintf(stderr, "用法：marshal task <%s>\n", strings.Join(taskCommands, "|"))
		return ExitUsage
	}
	if args[0] == "plan" {
		return runTaskPlan(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "status" {
		return runTaskStatus(args[1:], stdout, stderr)
	}
	if args[0] == "approve" {
		return runTaskApprove(args[1:], stdout, stderr)
	}
	if args[0] == "verify" {
		return runTaskVerify(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "run" {
		return runTaskWorker(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "publish" {
		return runTaskPublish(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "accept" {
		return runTaskAccept(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "review" {
		return runTaskReview(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "cleanup" {
		return runTaskCleanup(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "abort" {
		return runTaskAbort(args[1:], stdout, stderr)
	}
	if len(args) != 1 {
		return ExitUsage
	}
	fmt.Fprintf(stderr, "marshal task %s 尚未实现；未执行任何 Worker、状态写入或发布副作用。\n", args[0])
	return ExitUnavailable
}

func runTaskCleanup(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task cleanup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	apply := flags.Bool("apply", false, "执行预览中的本地清理")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal task cleanup --run RUN_ID [--apply] [--json]")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "清理失败：%v\n", err)
		return ExitFailure
	}
	if err := location.ValidateIdentity(); err != nil {
		fmt.Fprintf(stderr, "清理失败：%v\n", err)
		return ExitFailure
	}
	validator, err := contract.NewValidator()
	if err != nil {
		fmt.Fprintln(stderr, "清理失败：Contract Validator 初始化失败。")
		return ExitFailure
	}
	result, err := cleanupservice.Execute(ctx, cleanupservice.Input{
		StateRoot: location.StateRoot, RepositoryRoot: location.RepositoryRoot, RunID: *runID,
		Apply: *apply, Now: time.Now().UTC(), Validator: validator,
	})
	if err != nil {
		fmt.Fprintf(stderr, "清理失败：%v\n", err)
		return ExitFailure
	}
	if *jsonOutput {
		if err := writeJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "输出清理结果失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}
	if len(result.Targets) == 0 {
		fmt.Fprintf(stdout, "Run %s 没有待清理的本地目标。\n", result.RunID)
		return ExitOK
	}
	if result.Applied {
		fmt.Fprintf(stdout, "Run %s 清理完成：\n", result.RunID)
	} else {
		fmt.Fprintf(stdout, "Run %s 清理预览（未执行；使用 --apply 执行）：\n", result.RunID)
	}
	for _, target := range result.Targets {
		fmt.Fprintf(stdout, "- %s：%s（%s）\n", target.Kind, target.Path, target.Action)
	}
	return ExitOK
}

func runTaskAbort(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task abort", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	actor := flags.String("actor", "", "终止操作者 ID")
	reason := flags.String("reason", "", "终止原因")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" || strings.TrimSpace(*actor) == "" || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stderr, "用法：marshal task abort --run RUN_ID --actor ID --reason TEXT [--json]")
		return ExitUsage
	}
	abortActor := strings.TrimSpace(*actor)
	abortReason := strings.TrimSpace(*reason)
	if domain.ValidateID(*runID) != nil || len(abortActor) > 512 || len(abortReason) > 12000 {
		fmt.Fprintln(stderr, "终止失败：Run ID、操作者或原因无效。")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil || location.ValidateIdentity() != nil {
		fmt.Fprintln(stderr, "终止失败：无法验证仓库身份。")
		return ExitFailure
	}
	store := runstore.New(location.StateRoot)
	lease, err := store.Acquire(*runID)
	if err != nil {
		fmt.Fprintf(stderr, "终止失败：%v\n", err)
		return ExitFailure
	}
	defer lease.Release()
	state, err := store.Inspect(*runID)
	if err != nil {
		fmt.Fprintf(stderr, "终止失败：%v\n", err)
		return ExitFailure
	}
	if state.State != domain.StateRetryPending {
		if state.State.Terminal() {
			fmt.Fprintf(stderr, "终止失败：Run 已处于终态 %s，不能再次终止。\n", state.State)
		} else {
			fmt.Fprintf(stderr, "终止失败：仅允许从 RETRY_PENDING 显式终止。\n")
		}
		return ExitFailure
	}
	eventID, err := domain.NewID("event")
	if err != nil {
		fmt.Fprintf(stderr, "终止失败：生成事件 ID：%v\n", err)
		return ExitFailure
	}
	timestamp := time.Now().UTC()
	payload := map[string]any{"terminalReason": lifecycle.AbortTerminalReason, "reason": abortReason}
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: state.RunID,
		AttemptID: state.CurrentAttemptID, Sequence: state.Sequence + 1, Type: lifecycle.AbortEventType,
		StateFrom: state.State, StateTo: domain.StateBlocked, Timestamp: timestamp,
		Actor: &domain.Actor{Type: domain.ControlSourceTypeHuman, ID: abortActor}, Payload: payload,
	}
	nextState, err := lifecycle.Reduce(state, event, lifecycle.Guard{LeaseHeld: true})
	if err != nil {
		fmt.Fprintf(stderr, "终止失败：生命周期转换：%v\n", err)
		return ExitFailure
	}
	payloadData, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(stderr, "终止失败：编码终止证据：%v\n", err)
		return ExitFailure
	}
	abortDigest, err := canonical.DigestJSON(payloadData)
	if err != nil {
		fmt.Fprintf(stderr, "终止失败：摘要终止证据：%v\n", err)
		return ExitFailure
	}
	runDirectory := filepath.Join(location.StateRoot, "runs", *runID)
	prepared, err := review.PrepareOutcome(runDirectory, review.OutcomeData{
		TaskID: state.TaskID, RunID: state.RunID, TerminalState: domain.StateBlocked, Verdict: "abort",
		FinalReviewRound: max(1, state.ReviewRound), FinalReviewDigest: abortDigest, FinalEvidenceDigest: abortDigest,
		Summary: abortReason, FindingCount: 0, GeneratedAt: timestamp,
	})
	if err != nil {
		fmt.Fprintf(stderr, "终止失败：准备终态 Outcome：%v\n", err)
		return ExitFailure
	}
	if err := stageAbortResult(runDirectory, state, abortActor, abortReason, timestamp); err != nil {
		prepared.Abort()
		fmt.Fprintf(stderr, "终止失败：准备终止记录：%v\n", err)
		return ExitFailure
	}
	if err := store.Append(lease, event, state.Sequence); err != nil {
		prepared.Abort()
		removeAbortResult(runDirectory)
		fmt.Fprintf(stderr, "终止失败：记录事件：%v\n", err)
		return ExitFailure
	}
	if err := commitAbortResult(runDirectory); err != nil {
		fmt.Fprintf(stderr, "终止失败：提交终止记录：%v\nJournal 事件已保留，需执行恢复检查。\n", err)
		return ExitFailure
	}
	if err := prepared.Commit(); err != nil {
		fmt.Fprintf(stderr, "终止失败：提交终态 Outcome：%v\nJournal 与终止记录已保留，需执行恢复检查。\n", err)
		return ExitFailure
	}
	if err := store.WriteSnapshot(lease, nextState); err != nil {
		fmt.Fprintf(stderr, "终止失败：写入状态快照：%v\nJournal 与终态 Outcome 已保留，需执行恢复检查。\n", err)
		return ExitFailure
	}
	if *jsonOutput {
		if err := writeJSON(stdout, struct {
			Status         string       `json:"status"`
			RunID          string       `json:"runId"`
			State          domain.State `json:"state"`
			TerminalReason string       `json:"terminalReason"`
			Actor          string       `json:"actor"`
			Sequence       uint64       `json:"sequence"`
		}{"aborted", state.RunID, nextState.State, lifecycle.AbortTerminalReason, abortActor, nextState.Sequence}); err != nil {
			fmt.Fprintf(stderr, "输出终止结果失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "Run：%s\n状态：RETRY_PENDING → BLOCKED\n终态原因：%s\n操作者：%s\n", state.RunID, lifecycle.AbortTerminalReason, abortActor)
	return ExitOK
}

func stageAbortResult(runDirectory string, state domain.RunState, actor, reason string, now time.Time) error {
	if _, err := os.Lstat(filepath.Join(runDirectory, "result.md")); err == nil {
		return errors.New("terminal result.md already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	pending := filepath.Join(runDirectory, "result.md.pending")
	if err := os.Remove(pending); err != nil && !os.IsNotExist(err) {
		return err
	}
	content := fmt.Sprintf("# Run 终止记录\n\n- 任务 ID：%s\n- Run ID：%s\n- 终态：BLOCKED\n- 终态原因：%s\n- 操作者：%s\n- 终止原因：%s\n- 生成时间：%s\n", state.TaskID, state.RunID, lifecycle.AbortTerminalReason, actor, reason, now.UTC().Format(time.RFC3339))
	file, err := os.OpenFile(pending, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.WriteString(content)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr == nil {
		writeErr = syncErr
	}
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		os.Remove(pending)
	}
	return writeErr
}

func commitAbortResult(runDirectory string) error {
	if err := os.Rename(filepath.Join(runDirectory, "result.md.pending"), filepath.Join(runDirectory, "result.md")); err != nil {
		return err
	}
	directory, err := os.Open(runDirectory)
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	return err
}

func removeAbortResult(runDirectory string) {
	_ = os.Remove(filepath.Join(runDirectory, "result.md.pending"))
}

func runTaskPlan(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	taskPath := flags.String("task", "", "TaskSpec 路径")
	policyPath := flags.String("policy", "", "PolicySnapshot 路径")
	runID := flags.String("run", "", "Run ID")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *taskPath == "" || *policyPath == "" || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal task plan --task PATH --policy PATH --run RUN_ID [--json]")
		return ExitUsage
	}

	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "规划失败：%v\n", err)
		return ExitFailure
	}
	if err := location.ValidateIdentity(); err != nil {
		fmt.Fprintf(stderr, "规划失败：%v\n", err)
		return ExitFailure
	}
	taskData, err := readInput(*taskPath, strings.NewReader(""))
	if err != nil {
		fmt.Fprintf(stderr, "规划失败：读取 TaskSpec：%v\n", err)
		return ExitFailure
	}
	policyData, err := readInput(*policyPath, strings.NewReader(""))
	if err != nil {
		fmt.Fprintf(stderr, "规划失败：读取 PolicySnapshot：%v\n", err)
		return ExitFailure
	}
	runtime, err := app.NewWorkerRuntime(os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "规划失败：Worker Runtime 初始化失败。")
		return ExitFailure
	}
	result, err := planning.Plan(ctx, planning.Input{
		StateRoot:      location.StateRoot,
		RepositoryRoot: location.RepositoryRoot,
		RunID:          *runID,
		TaskSpec:       taskData,
		PolicySnapshot: policyData,
		Selector:       runtime.Selector(),
		Validator:      runtime.Validator(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "规划失败：%v\n", err)
		return ExitFailure
	}
	if result.Adapter == nil || result.State.State != domain.StateReady {
		fmt.Fprintln(stderr, "规划失败：未生成 READY Run 或未冻结 Worker Adapter。")
		return ExitFailure
	}
	if *jsonOutput {
		if err := writeJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "输出规划结果失败：%v\n", err)
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "Run：%s\nWorker Adapter：%s\n状态：%s\n", *runID, result.Adapter.ID(), result.State.State)
	}
	return ExitOK
}

func runTaskApprove(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task approve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	gate := flags.String("gate", "", "审批 Gate：plan 或 publish")
	actor := flags.String("actor", "local-operator", "审批人 ID")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" || *gate == "" {
		fmt.Fprintln(stderr, "用法：marshal task approve --run RUN_ID --gate plan|publish [--actor ID] [--json]")
		return ExitUsage
	}
	if err := domain.ValidateID(*runID); err != nil || (*gate != domain.ApprovalGatePlan && *gate != domain.ApprovalGatePublish) {
		fmt.Fprintln(stderr, "审批失败：Run ID 或 Gate 无效。")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil || location.ValidateIdentity() != nil {
		fmt.Fprintln(stderr, "审批失败：无法验证仓库身份。")
		return ExitFailure
	}
	validator, err := contract.NewValidator()
	if err != nil {
		fmt.Fprintln(stderr, "审批失败：契约初始化失败。")
		return ExitFailure
	}
	record, err := controlplane.Approve(controlplane.ApprovalInput{
		StateRoot: location.StateRoot, RunID: *runID, Gate: *gate, SourceID: *actor, Now: time.Now().UTC(), Validator: validator,
	})
	if err != nil {
		fmt.Fprintln(stderr, "审批失败：当前 Gate 不可批准或 Run 证据无效。")
		return ExitFailure
	}
	if *jsonOutput {
		if err := writeJSON(stdout, record); err != nil {
			fmt.Fprintln(stderr, "输出审批结果失败。")
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "Run：%s\nGate：%s\nApproval：%s\n", *runID, *gate, record.RecordID)
	}
	return ExitOK
}

func publisherFromEnvironment(location repository.State) (*githubpublisher.Publisher, *contract.Validator, error) {
	ghPath, configDir := os.Getenv("MARSHAL_GH_PATH"), os.Getenv("MARSHAL_GH_CONFIG_DIR")
	if ghPath == "" || configDir == "" {
		return nil, nil, errors.New("必须配置 absolute MARSHAL_GH_PATH 与 MARSHAL_GH_CONFIG_DIR")
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return nil, nil, err
	}
	publisher, err := githubpublisher.New(ghPath, configDir, location.RepositoryRoot, validator)
	return publisher, validator, err
}

func runTaskPublish(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task publish", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal task publish --run RUN_ID [--json]")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "发布失败：%v\n", err)
		return ExitFailure
	}
	if err := location.ValidateIdentity(); err != nil {
		fmt.Fprintf(stderr, "发布失败：%v\n", err)
		return ExitFailure
	}
	approvalValidator, err := contract.NewValidator()
	if err != nil || controlplane.Require(controlplane.ApprovalInput{StateRoot: location.StateRoot, RunID: *runID, Gate: domain.ApprovalGatePublish, Validator: approvalValidator}) != nil {
		fmt.Fprintln(stderr, "发布失败：缺少当前有效的 publish 审批。")
		return ExitFailure
	}
	publisher, validator, err := publisherFromEnvironment(location)
	if err != nil {
		fmt.Fprintf(stderr, "发布不可用：%v\n", err)
		return ExitUnavailable
	}
	result, err := publication.Publish(ctx, publication.Input{StateRoot: location.StateRoot, RepositoryRoot: location.RepositoryRoot, RunID: *runID, Publisher: publisher, Validator: validator})
	if err != nil {
		fmt.Fprintf(stderr, "发布失败（状态 %s）：%v\n", result.State.State, err)
		return ExitFailure
	}
	if *jsonOutput {
		if err := writeJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "输出发布结果失败：%v\n", err)
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "Run：%s\n状态：%s\nDraft PR：%s\n", *runID, result.State.State, result.Publication.Request.URL)
	}
	return ExitOK
}

func runTaskAccept(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task accept", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal task accept --run RUN_ID [--json]")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "CI 验收失败：%v\n", err)
		return ExitFailure
	}
	if err := location.ValidateIdentity(); err != nil {
		fmt.Fprintf(stderr, "CI 验收失败：%v\n", err)
		return ExitFailure
	}
	publisher, validator, err := publisherFromEnvironment(location)
	if err != nil {
		fmt.Fprintf(stderr, "CI 验收不可用：%v\n", err)
		return ExitUnavailable
	}
	result, err := publication.ObserveChecks(ctx, publication.CheckInput{StateRoot: location.StateRoot, RunID: *runID, Observer: publisher, Validator: validator})
	if err != nil {
		fmt.Fprintf(stderr, "CI 验收失败（状态 %s）：%v\n", result.State.State, err)
		return ExitFailure
	}
	if *jsonOutput {
		if err := writeJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "输出 CI 结果失败：%v\n", err)
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "Run：%s\n状态：%s\nRemote Checks：%s\n", *runID, result.State.State, result.Checks.Status)
	}
	if result.State.State == domain.StateCIPending {
		return ExitUnavailable
	}
	if result.State.State != domain.StateAccepted {
		return ExitFailure
	}
	return ExitOK
}

func runTaskWorker(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal task run --run RUN_ID [--json]")
		return ExitUsage
	}
	if err := domain.ValidateID(*runID); err != nil {
		fmt.Fprintln(stderr, "运行失败：Run ID 无效。")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "运行失败：%v\n", err)
		return ExitFailure
	}
	if err := location.ValidateIdentity(); err != nil {
		fmt.Fprintf(stderr, "运行失败：%v\n", err)
		return ExitFailure
	}
	runtime, err := app.NewWorkerRuntime(os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "运行失败：Worker Runtime 初始化失败。")
		return ExitFailure
	}
	snapshotData, err := os.ReadFile(filepath.Join(location.StateRoot, "runs", *runID, "capability-snapshot.json"))
	if err != nil {
		fmt.Fprintln(stderr, "运行失败：读取冻结 CapabilitySnapshot 失败。")
		return ExitFailure
	}
	if err := runtime.Validator().Validate(domain.KindCapabilitySnapshot, snapshotData); err != nil {
		fmt.Fprintln(stderr, "运行失败：冻结 CapabilitySnapshot 无效。")
		return ExitFailure
	}
	var frozenAdapter struct {
		AdapterID string `json:"adapterId"`
	}
	if err := json.Unmarshal(snapshotData, &frozenAdapter); err != nil || frozenAdapter.AdapterID == "" {
		fmt.Fprintln(stderr, "运行失败：冻结 CapabilitySnapshot 缺少 Adapter 身份。")
		return ExitFailure
	}
	worker, err := runtime.Registry().Resolve(frozenAdapter.AdapterID)
	if err != nil {
		fmt.Fprintln(stderr, "运行失败：冻结 Worker Adapter 当前未配置或不可用。")
		return ExitUnavailable
	}
	state, err := runstore.New(location.StateRoot).Inspect(*runID)
	if err != nil {
		fmt.Fprintln(stderr, "运行失败：无法核验当前 Run 状态。")
		return ExitFailure
	}
	if state.State == domain.StateReady {
		if err := controlplane.Require(controlplane.ApprovalInput{StateRoot: location.StateRoot, RunID: *runID, Gate: domain.ApprovalGatePlan, Validator: runtime.Validator()}); err != nil {
			fmt.Fprintln(stderr, "运行失败：缺少当前有效的 plan 审批。")
			return ExitFailure
		}
	}
	result, err := execution.Run(ctx, execution.Input{StateRoot: location.StateRoot, RepositoryRoot: location.RepositoryRoot, RunID: *runID, Adapter: worker, Validator: runtime.Validator()})
	if err != nil {
		fmt.Fprintf(stderr, "运行失败（Attempt %s，状态 %s）：%v\n", result.AttemptID, result.State.State, err)
		return ExitFailure
	}
	if *jsonOutput {
		if err := writeJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "输出运行结果失败：%v\n", err)
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "Run：%s\nAttempt：%s\n状态：%s\n", *runID, result.AttemptID, result.State.State)
	}
	return ExitOK
}

func runTaskVerify(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal task verify --run RUN_ID [--json]")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	if err := location.ValidateIdentity(); err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	store := runstore.New(location.StateRoot)
	lease, err := store.Acquire(*runID)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	defer lease.Release()
	state, err := store.Inspect(*runID)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	if state.State != domain.StateVerifying {
		fmt.Fprintf(stderr, "验证失败：Run 状态为 %s，要求 VERIFYING。\n", state.State)
		return ExitFailure
	}
	runDirectory := filepath.Join(location.StateRoot, "runs", *runID)
	taskData, err := readInput(filepath.Join(runDirectory, "task-spec.json"), strings.NewReader(""))
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：读取冻结 TaskSpec：%v\n", err)
		return ExitFailure
	}
	digest, err := canonical.DigestJSON(taskData)
	if err != nil || digest != state.SpecDigest {
		fmt.Fprintf(stderr, "验证失败：TaskSpec 摘要与冻结 Run 不一致。\n")
		return ExitFailure
	}
	application, err := app.New()
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	task, err := application.ParseTaskSpec(taskData)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	if task.Metadata.ID != state.TaskID || state.WorktreePath == "" || state.BaseSHA == "" {
		fmt.Fprintln(stderr, "验证失败：Run 缺少匹配的 Task、Worktree 或 Base 身份。")
		return ExitFailure
	}
	taskRepositoryPath, err := filepath.Abs(task.Repository.Path)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：TaskSpec Repository 路径无效：%v\n", err)
		return ExitFailure
	}
	taskRepository, err := filepath.EvalSymlinks(taskRepositoryPath)
	if err != nil || taskRepository != location.RepositoryRoot {
		fmt.Fprintln(stderr, "验证失败：TaskSpec Repository 与当前仓库身份不一致。")
		return ExitFailure
	}
	repositoryIdentity, err := gitworktree.Open(location.RepositoryRoot)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	scope, deliverables, commands := verification.PolicyFromTask(task)
	worktreeLease, err := repositoryIdentity.Acquire(location.StateRoot, state.TaskID, state.WorktreePath, state.BaseSHA)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	defer worktreeLease.Release()
	baselinePath := ""
	if commandsNeedBaseline(commands) {
		baseline, baselineErr := repositoryIdentity.CreateDetached(location.StateRoot, filepath.Join(runDirectory, "baseline-worktree"), state.BaseSHA)
		if baselineErr != nil {
			fmt.Fprintf(stderr, "验证失败：创建 Baseline Worktree：%v\n", baselineErr)
			return ExitFailure
		}
		defer baseline.Remove()
		baselinePath = baseline.Path
	}
	verificationContext, cancelVerification := context.WithTimeout(ctx, time.Duration(task.Budgets.RunTimeoutSeconds)*time.Second)
	defer cancelVerification()
	result, err := verification.New().Verify(verificationContext, verification.Input{TaskID: state.TaskID, RunID: state.RunID, SpecDigest: state.SpecDigest, BaseSHA: state.BaseSHA, Worktree: state.WorktreePath, ExpectedCommonDir: repositoryIdentity.CommonDir, RunDirectory: runDirectory, Scope: scope, Deliverables: deliverables, Commands: commands, BaselinePath: baselinePath, PatchCaptureBytes: patchCaptureLimit(scope.MaxDiffBytes)})
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	reportData, err := json.Marshal(result.Report)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：编码报告：%v\n", err)
		return ExitFailure
	}
	reportDigest, err := canonical.DigestJSON(reportData)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：摘要报告：%v\n", err)
		return ExitFailure
	}
	manifestData, err := json.Marshal(result.Manifest)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：编码 ArtifactManifest：%v\n", err)
		return ExitFailure
	}
	manifestDigest, err := canonical.DigestJSON(manifestData)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：摘要 ArtifactManifest：%v\n", err)
		return ExitFailure
	}
	eventID, err := domain.NewID("event")
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：生成事件 ID：%v\n", err)
		return ExitFailure
	}
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: state.RunID, Sequence: state.Sequence + 1, Type: "verification.completed", StateFrom: state.State, StateTo: domain.StateReviewPending, Timestamp: result.Report.CompletedAt, Actor: &domain.Actor{Type: "system", ID: "marshal-verifier"}, Payload: map[string]any{"reportDigest": reportDigest, "artifactManifestDigest": manifestDigest, "status": result.Report.Status}}
	nextState, err := lifecycle.Reduce(state, event, lifecycle.Guard{LeaseHeld: true, EvidenceCurrent: true, ReportComplete: true})
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：生命周期转换：%v\n", err)
		return ExitFailure
	}
	if err := store.Append(lease, event, state.Sequence); err != nil {
		fmt.Fprintf(stderr, "验证失败：记录事件：%v\n", err)
		return ExitFailure
	}
	if err := store.WriteSnapshot(lease, nextState); err != nil {
		fmt.Fprintf(stderr, "验证失败：写入状态快照：%v\n", err)
		return ExitFailure
	}
	if *jsonOutput {
		if err := writeJSON(stdout, result.Report); err != nil {
			fmt.Fprintf(stderr, "输出验证报告失败：%v\n", err)
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "Run：%s\nVerification：%s\nGate：%d\n", state.RunID, result.Report.Status, len(result.Report.Gates))
	}
	if result.Report.Status != "pass" {
		return ExitFailure
	}
	return ExitOK
}

func patchCaptureLimit(maxDiffBytes int64) int64 {
	if maxDiffBytes <= 0 {
		return 64 << 20
	}
	return maxDiffBytes + 1
}

func commandsNeedBaseline(commands []verification.CommandSpec) bool {
	for _, command := range commands {
		if command.BaselinePolicy == "always" || command.BaselinePolicy == "on-failure" {
			return true
		}
	}
	return false
}

func runTaskReview(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task review", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	decisionPath := flags.String("decision", "", "ReviewDecision JSON 路径")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal task review --run RUN_ID [--decision PATH] [--json]")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	if err := location.ValidateIdentity(); err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	store := runstore.New(location.StateRoot)
	lease, err := store.Acquire(*runID)
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	defer lease.Release()
	state, err := store.Inspect(*runID)
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	if state.State != domain.StateReviewPending {
		fmt.Fprintf(stderr, "审查失败：Run 状态为 %s，要求 REVIEW_PENDING。\n", state.State)
		return ExitFailure
	}
	runDirectory := filepath.Join(location.StateRoot, "runs", *runID)
	taskData, err := readInput(filepath.Join(runDirectory, "task-spec.json"), strings.NewReader(""))
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：读取冻结 TaskSpec：%v\n", err)
		return ExitFailure
	}
	taskDigest, err := canonical.DigestJSON(taskData)
	if err != nil || taskDigest != state.SpecDigest {
		fmt.Fprintln(stderr, "审查失败：TaskSpec 摘要与冻结 Run 不一致。")
		return ExitFailure
	}
	application, err := app.New()
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	task, err := application.ParseTaskSpec(taskData)
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	if task.Metadata.ID != state.TaskID {
		fmt.Fprintln(stderr, "审查失败：TaskSpec 与 Run 身份不一致。")
		return ExitFailure
	}
	verificationData, err := readInput(filepath.Join(runDirectory, "verification-report.json"), strings.NewReader(""))
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：读取 VerificationReport：%v\n", err)
		return ExitFailure
	}
	if err := application.ValidateContract(domain.KindVerificationReport, verificationData); err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	var report verification.Report
	if err := json.Unmarshal(verificationData, &report); err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	manifestData, err := readInput(filepath.Join(runDirectory, "artifact-manifest.json"), strings.NewReader(""))
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：读取 ArtifactManifest：%v\n", err)
		return ExitFailure
	}
	if err := application.ValidateContract(domain.KindArtifactManifest, manifestData); err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	var manifest verification.ArtifactManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	frozenReportDigest, frozenManifestDigest, err := frozenVerificationDigests(store, state.RunID)
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	currentReportDigest, err := canonical.DigestJSON(verificationData)
	if err != nil || currentReportDigest != frozenReportDigest {
		fmt.Fprintln(stderr, "审查失败：VerificationReport 与 verification.completed 冻结摘要不一致。")
		return ExitFailure
	}
	currentManifestDigest, err := canonical.DigestJSON(manifestData)
	if err != nil || currentManifestDigest != frozenManifestDigest {
		fmt.Fprintln(stderr, "审查失败：ArtifactManifest 与 verification.completed 冻结摘要不一致。")
		return ExitFailure
	}
	repositoryIdentity, err := gitworktree.Open(location.RepositoryRoot)
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	worktreeLease, err := repositoryIdentity.Acquire(location.StateRoot, state.TaskID, state.WorktreePath, state.BaseSHA)
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	defer worktreeLease.Release()
	observation, err := verification.ObserveContext(ctx, state.WorktreePath, state.BaseSHA, patchCaptureLimit(task.Scope.MaxDiffBytes))
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：重新观察 Worktree：%v\n", err)
		return ExitFailure
	}
	if err := review.ValidateCurrentObservation(report, observation); err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	validator, err := contract.NewValidator()
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	if *decisionPath == "" {
		builder := review.PacketBuilder{RunDirectory: runDirectory, Validator: validator}
		packet, packetDigest, err := builder.Build(review.PacketBuildInput{Task: task, TaskData: taskData, Report: report, ReportData: verificationData, Manifest: manifest, ManifestData: manifestData, TaskID: state.TaskID, RunID: state.RunID, SpecDigest: state.SpecDigest, BaseSHA: state.BaseSHA, ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed})
		if err != nil {
			fmt.Fprintf(stderr, "审查失败：生成 ReviewPacket：%v\n", err)
			return ExitFailure
		}
		if *jsonOutput {
			if err := writeJSON(stdout, struct {
				Status        string               `json:"status"`
				PacketDigest  string               `json:"packetDigest"`
				Packet        *domain.ReviewPacket `json:"packet"`
				PromptVersion string               `json:"promptVersion"`
			}{"generated", packetDigest, packet, review.PromptVersion}); err != nil {
				fmt.Fprintf(stderr, "输出 ReviewPacket 失败：%v\n", err)
				return ExitFailure
			}
		} else {
			fmt.Fprintf(stdout, "已生成 ReviewPacket：%s\n摘要：%s\n审查轮次：%d\n", filepath.Join(runDirectory, "review-packet.json"), packetDigest, state.ReviewRound)
		}
		return ExitOK
	}
	importer := review.DecisionImporter{RunDirectory: runDirectory, Validator: validator}
	result, err := importer.Import(review.DecisionInput{Path: *decisionPath, Task: task, TaskID: state.TaskID, RunID: state.RunID, SpecDigest: state.SpecDigest, ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed, ReworkRoundsUsed: state.ReworkRoundsUsed, Report: report, Manifest: manifest})
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：导入 Decision：%v\n", err)
		return ExitFailure
	}
	eventID, err := domain.NewID("event")
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	timestamp := time.Now().UTC()
	eventType := "review." + strings.ReplaceAll(result.Decision.Verdict, "_", "-")
	if result.Decision.Verdict == "rework" && result.TargetState == domain.StateRejected {
		eventType = "review.rework-budget-exhausted"
	}
	payload := map[string]any{"verdict": result.Decision.Verdict, "decisionDigest": result.DecisionDigest, "evidenceDigest": result.Decision.EvidenceDigest}
	if result.TargetState.Terminal() {
		reason := result.Decision.Summary
		if result.BudgetExhausted && result.Decision.Verdict == "rework" {
			reason = "Rework/Attempt 预算耗尽：" + reason
		}
		payload["terminalReason"] = reason
	}
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: state.RunID, Sequence: state.Sequence + 1, Type: eventType, StateFrom: state.State, StateTo: result.TargetState, Timestamp: timestamp, Actor: &domain.Actor{Type: "system", ID: "marshal-review"}, Payload: payload}
	guard := lifecycle.Guard{LeaseHeld: true, EvidenceCurrent: true, RequiredGatesPass: report.Status == "pass", DecisionCurrent: true, NoChangeAllowed: task.Acceptance.AllowNoChange, BudgetAvailable: !result.BudgetExhausted}
	nextState, err := lifecycle.Reduce(state, event, guard)
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：生命周期转换：%v\n", err)
		return ExitFailure
	}
	outcome := review.TerminalOutcome(state.TaskID, state.RunID, result.TargetState, result, timestamp)
	prepared, err := review.PrepareRecords(runDirectory, result, outcome)
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：准备持久化记录：%v\n", err)
		return ExitFailure
	}
	if err := store.Append(lease, event, state.Sequence); err != nil {
		prepared.Abort()
		fmt.Fprintf(stderr, "审查失败：记录事件：%v\n", err)
		return ExitFailure
	}
	if err := prepared.Commit(); err != nil {
		fmt.Fprintf(stderr, "审查失败：提交审查记录：%v\n", err)
		return ExitFailure
	}
	if err := store.WriteSnapshot(lease, nextState); err != nil {
		fmt.Fprintf(stderr, "审查失败：写入状态快照：%v；Journal 与审查记录已保留，需执行恢复检查。\n", err)
		return ExitFailure
	}
	if *jsonOutput {
		if err := writeJSON(stdout, struct {
			Status         string       `json:"status"`
			Verdict        string       `json:"verdict"`
			TargetState    domain.State `json:"targetState"`
			DecisionDigest string       `json:"decisionDigest"`
		}{"applied", result.Decision.Verdict, result.TargetState, result.DecisionDigest}); err != nil {
			fmt.Fprintf(stderr, "输出决策结果失败：%v\n", err)
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "Decision 已应用。\n结论：%s\n状态：REVIEW_PENDING → %s\n", result.Decision.Verdict, result.TargetState)
	}
	return ExitOK
}

func frozenVerificationDigests(store *runstore.Store, runID string) (string, string, error) {
	events, _, err := store.ReadEvents(runID)
	if err != nil {
		return "", "", fmt.Errorf("读取验证事件：%w", err)
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != "verification.completed" {
			continue
		}
		reportDigest, reportOK := event.Payload["reportDigest"].(string)
		manifestDigest, manifestOK := event.Payload["artifactManifestDigest"].(string)
		if !reportOK || reportDigest == "" || !manifestOK || manifestDigest == "" {
			return "", "", errors.New("verification.completed 缺少冻结的验证产物摘要")
		}
		return reportDigest, manifestDigest, nil
	}
	return "", "", errors.New("未找到 verification.completed 事件")
}

func runTaskStatus(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal task status --run RUN_ID [--json]")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "读取状态失败：%v\n", err)
		return ExitFailure
	}
	if err := location.ValidateIdentity(); err != nil {
		fmt.Fprintf(stderr, "读取状态失败：%v\n", err)
		return ExitFailure
	}
	state, err := runstore.New(location.StateRoot).Inspect(*runID)
	if err != nil {
		fmt.Fprintf(stderr, "读取状态失败：%v\n", err)
		return ExitFailure
	}
	if *jsonOutput {
		if err := writeJSON(stdout, state); err != nil {
			fmt.Fprintf(stderr, "输出状态失败：%v\n", err)
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "Run：%s\n状态：%s\nSequence：%d\n更新时间：%s\n", state.RunID, state.State, state.Sequence, state.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	return ExitOK
}

func readInput(name string, stdin io.Reader) ([]byte, error) {
	if name == "-" {
		return readBounded(stdin, maxContractInputBytes)
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readBounded(file, maxContractInputBytes)
}

func readBounded(input io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("contract input exceeds %d bytes", limit)
	}
	return data, nil
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return errors.New("encode JSON: " + err.Error())
	}
	return nil
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, `Marshal：证据门禁式 Coding Agent 编排器

用法：
  marshal version [--json]
  marshal doctor [--run RUN_ID] [--repair] [--json]
  marshal init [--json]
  marshal contract validate [--schema NAME] <PATH|->
  marshal task plan --task PATH --policy PATH --run RUN_ID [--json]
  marshal task approve --run RUN_ID --gate plan|publish [--actor ID] [--json]
  marshal task status --run RUN_ID [--json]
  marshal task run --run RUN_ID [--json]
  marshal task verify --run RUN_ID [--json]
  marshal task review --run RUN_ID [--decision PATH] [--json]
  marshal task publish --run RUN_ID [--json]
  marshal task accept --run RUN_ID [--json]
  marshal task abort --run RUN_ID --actor ID --reason TEXT [--json]
  marshal task <COMMAND>

OpenCode、Qwen Code 与 Pi Worker 只产生 Attempt 与真实快照；verify、review、publish 与 accept 是彼此独立的证据门禁。发布命令还要求 absolute MARSHAL_GH_PATH 与 MARSHAL_GH_CONFIG_DIR。`)
}
