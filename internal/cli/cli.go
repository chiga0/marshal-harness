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
	"github.com/chiga0/marshal-harness/internal/contract"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/repository"
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
	"run",
	"status",
	"verify",
	"review",
	"rework",
	"publish",
	"accept",
	"abort",
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
		return runDoctor(args[1:], stdout, stderr)
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "contract":
		return runContract(args[1:], stdin, stdout, stderr)
	case "task":
		return runTask(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "未知命令 %q。\n", args[0])
		writeUsage(stderr)
		return ExitUsage
	}
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

func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "doctor 不接受位置参数。")
		return ExitUsage
	}
	application, err := app.New()
	if err != nil {
		fmt.Fprintf(stderr, "doctor 失败：%v\n", err)
		return ExitFailure
	}
	report := struct {
		Status          string         `json:"status"`
		Build           buildinfo.Info `json:"build"`
		ContractSchemas int            `json:"contractSchemas"`
		WorkerAdapters  int            `json:"workerAdapters"`
		Milestone       string         `json:"milestone"`
	}{
		Status:          "ok",
		Build:           buildinfo.Current(),
		ContractSchemas: application.ContractCount(),
		WorkerAdapters:  application.AdapterCount(),
		Milestone:       "2",
	}
	if *jsonOutput {
		if err := writeJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "输出 doctor 报告失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "状态：%s\nSchema：%d 份已编译\n已注册 Worker Adapter：%d\n", report.Status, report.ContractSchemas, report.WorkerAdapters)
	return ExitOK
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
	if args[0] == "status" {
		return runTaskStatus(args[1:], stdout, stderr)
	}
	if args[0] == "verify" {
		return runTaskVerify(ctx, args[1:], stdout, stderr)
	}
	if len(args) != 1 {
		return ExitUsage
	}
	fmt.Fprintf(stderr, "marshal task %s 尚未实现；未执行任何 Worker、状态写入或发布副作用。\n", args[0])
	return ExitUnavailable
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
	eventID, err := domain.NewID("event")
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：生成事件 ID：%v\n", err)
		return ExitFailure
	}
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: state.RunID, Sequence: state.Sequence + 1, Type: "verification.completed", StateFrom: state.State, StateTo: domain.StateReviewPending, Timestamp: result.Report.CompletedAt, Actor: &domain.Actor{Type: "system", ID: "marshal-verifier"}, Payload: map[string]any{"reportDigest": reportDigest, "status": result.Report.Status}}
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
  marshal doctor [--json]
  marshal init [--json]
  marshal contract validate [--schema NAME] <PATH|->
  marshal task status --run RUN_ID [--json]
  marshal task verify --run RUN_ID [--json]
  marshal task <COMMAND>

Milestone 2 提供状态目录初始化、只读 Run Inspection 与独立 Verification；其余 task 命令尚不可用。`)
}
