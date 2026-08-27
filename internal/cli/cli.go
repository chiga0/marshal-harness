// Package cli implements Marshal's thin command-line boundary.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chiga0/marshal-harness/internal/app"
	"github.com/chiga0/marshal-harness/internal/authority"
	"github.com/chiga0/marshal-harness/internal/buildinfo"
	"github.com/chiga0/marshal-harness/internal/canonical"
	cleanupservice "github.com/chiga0/marshal-harness/internal/cleanup"
	"github.com/chiga0/marshal-harness/internal/contract"
	controlplane "github.com/chiga0/marshal-harness/internal/control"
	"github.com/chiga0/marshal-harness/internal/dashboard"
	"github.com/chiga0/marshal-harness/internal/domain"
	"github.com/chiga0/marshal-harness/internal/execution"
	"github.com/chiga0/marshal-harness/internal/gitworktree"
	"github.com/chiga0/marshal-harness/internal/launcher"
	"github.com/chiga0/marshal-harness/internal/lifecycle"
	"github.com/chiga0/marshal-harness/internal/planning"
	"github.com/chiga0/marshal-harness/internal/port"
	"github.com/chiga0/marshal-harness/internal/publication"
	githubpublisher "github.com/chiga0/marshal-harness/internal/publisher/github"
	"github.com/chiga0/marshal-harness/internal/reconciliation"
	"github.com/chiga0/marshal-harness/internal/repository"
	"github.com/chiga0/marshal-harness/internal/review"
	"github.com/chiga0/marshal-harness/internal/runstore"
	"github.com/chiga0/marshal-harness/internal/sandbox/local"
	"github.com/chiga0/marshal-harness/internal/sandboxbridge"
	"github.com/chiga0/marshal-harness/internal/selfidentity"
	"github.com/chiga0/marshal-harness/internal/supervisor"
	"github.com/chiga0/marshal-harness/internal/taskgen"
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
	"scaffold",
	"plan",
	"approve",
	"run",
	"status",
	"verify",
	"review",
	"rework",
	"publish",
	"accept",
	"reconcile",
	"abort",
	"cleanup",
	"migrate-outcomes",
}

var newWorkerRuntime = app.NewWorkerRuntime

var (
	localBuildInfo             = buildinfo.Current
	localNow                   = func() time.Time { return time.Now().UTC() }
	localDogfoodGateTestBypass = func(buildinfo.Info) bool { return false }
)

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
	var doctor *doctorOptions
	if args[0] == "doctor" {
		parsed, ok := parseDoctorOptions(args[1:], stderr)
		if !ok {
			return ExitUsage
		}
		doctor = &parsed
	}
	observation, exitCode, gated := applyLocalDogfoodGate(args, doctor, stderr)
	if gated {
		return exitCode
	}
	if observation != nil {
		ctx = context.WithValue(ctx, localDogfoodObservationContextKey{}, *observation)
	}

	switch args[0] {
	case "help", "-h", "--help":
		writeUsage(stdout)
		return ExitOK
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(ctx, *doctor, stdout, stderr)
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "contract":
		return runContract(args[1:], stdin, stdout, stderr)
	case "serve", "web":
		return runServe(args[1:], stdout, stderr)
	case "task":
		return runTask(ctx, args[1:], stdin, stdout, stderr)
	case "explain":
		return runExplain(args[1:], stdout, stderr)
	case "supervise":
		return runSupervise(ctx, args[1:], stdout, stderr)
	case "internal":
		return runInternal(args[1:], stdin, stdout, stderr)
	case "__launch":
		return runInternalLaunch(args[1:], stderr)
	case "__detach":
		return runInternalDetach(stderr)
	default:
		fmt.Fprintf(stderr, "未知命令 %q。\n", args[0])
		writeUsage(stderr)
		return ExitUsage
	}
}

type localDogfoodObservationContextKey struct{}

func localDogfoodObservation(ctx context.Context) *selfidentity.LocalSelfIdentityObservationV1 {
	observation, ok := ctx.Value(localDogfoodObservationContextKey{}).(selfidentity.LocalSelfIdentityObservationV1)
	if !ok {
		return nil
	}
	return &observation
}

func applyLocalDogfoodGate(args []string, doctor *doctorOptions, stderr io.Writer) (*selfidentity.LocalSelfIdentityObservationV1, int, bool) {
	build := localBuildInfo()
	if localDogfoodBootstrapCommand(args, doctor) || runtime.GOOS != "darwin" {
		return nil, ExitOK, false
	}
	// The default production seam is always false. Package tests replace it
	// only for their legacy unknown/unprofiled in-process fixture; a built
	// Darwin marshal, including Makefile's default unprofiled binary, reaches
	// the fail-closed profile check below.
	if localDogfoodGateTestBypass(build) {
		return nil, ExitOK, false
	}
	if build.SelfProfile != selfidentity.LocalProfile {
		fmt.Fprintf(stderr, "Marshal local dogfood gate 拒绝：%s。\n", selfidentity.ReasonProfileMismatch)
		return nil, ExitUnavailable, true
	}
	commandClass, denial := localDogfoodCommandClass(args, doctor)
	if denial != "" {
		fmt.Fprintf(stderr, "Marshal local dogfood gate 拒绝：%s。\n", denial)
		return nil, ExitUnavailable, true
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "Marshal local dogfood gate 拒绝：%s。\n", selfidentity.ReasonObjectMismatch)
		return nil, ExitUnavailable, true
	}
	observation, err := selfidentity.Admit(os.Getenv(selfidentity.ActivationEnv), commandClass, workingDirectory,
		selfidentity.BuildIdentity{SourceHead: build.Commit, SelfProfile: build.SelfProfile}, localNow())
	if err != nil {
		fmt.Fprintf(stderr, "Marshal local dogfood gate 拒绝：%s。\n", selfidentity.ReasonCode(err))
		return nil, ExitUnavailable, true
	}
	return &observation, ExitOK, false
}

func localDogfoodBootstrapCommand(args []string, doctor *doctorOptions) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" || args[0] == "version" {
		return true
	}
	if localDogfoodBoundedInternalCommand(args) {
		return true
	}
	return args[0] == "doctor" && doctor != nil && doctor.self && doctor.runID == "" &&
		!doctor.repair && !doctor.printEnv && doctor.validFor > 0
}

// localDogfoodBoundedInternalCommand keeps operator-local preflights on the
// fixed Marshal executable without granting lifecycle, credential, remote, or
// publication authority. Every admitted command has a bounded contract, no
// Core/repository persistence effect, and enforces the attestation-ready
// handshake. The plan checker may launch a bounded Adapter capability probe.
func localDogfoodBoundedInternalCommand(args []string) bool {
	if len(args) < 3 || args[0] != "internal" || !slices.Contains(args[2:], "--attestation-ready") {
		return false
	}
	return slices.Contains([]string{
		"artifact-attestation-check",
		"qoder-transcript-check",
		"plan-premortem-check",
		"review-freshness-check",
		"codex-provider-schema-check",
		"closure-matrix-check",
	}, args[1])
}

func localDogfoodCommandClass(args []string, doctor *doctorOptions) (string, string) {
	switch args[0] {
	case "doctor":
		if doctor == nil || doctor.repair {
			return "", selfidentity.ReasonCommandDenied
		}
		return selfidentity.CommandDoctor, ""
	case "init":
		return selfidentity.CommandInit, ""
	case "task":
		if len(args) <= 1 {
			return "", selfidentity.ReasonCommandDenied
		}
		switch args[1] {
		case "scaffold":
			return selfidentity.CommandTaskScaffold, ""
		case "plan":
			return selfidentity.CommandTaskPlan, ""
		case "status":
			return selfidentity.CommandTaskStatus, ""
		case "run":
			return selfidentity.CommandTaskRun, ""
		case "verify":
			return selfidentity.CommandTaskVerify, ""
		case "review":
			return selfidentity.CommandTaskReview, ""
		case "approve":
			gate, ok := localDogfoodApprovalGate(args[2:])
			if gate == domain.ApprovalGatePublish {
				return "", selfidentity.ReasonPublicationDenied
			}
			if ok && gate == domain.ApprovalGatePlan {
				return selfidentity.CommandTaskApprovePlan, ""
			}
			return "", selfidentity.ReasonCommandDenied
		case "publish", "accept", "reconcile":
			return "", selfidentity.ReasonPublicationDenied
		}
		return "", selfidentity.ReasonCommandDenied
	case "serve", "web":
		return "", selfidentity.ReasonRemoteSurfaceDenied
	case "internal", "__launch", "__detach":
		return "", selfidentity.ReasonCredentialedEffectDenied
	default:
		return "", selfidentity.ReasonCommandDenied
	}
}

func localDogfoodApprovalGate(args []string) (string, bool) {
	gate := ""
	count := 0
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--gate":
			if index+1 >= len(args) {
				return "", false
			}
			index++
			gate = args[index]
			count++
		case strings.HasPrefix(argument, "--gate="):
			gate = strings.TrimPrefix(argument, "--gate=")
			count++
		}
	}
	return gate, count == 1
}

func runInternal(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "内部调用无效。")
		return ExitUsage
	}
	switch args[0] {
	case "artifact-attestation-check":
		return runInternalArtifactAttestationCheck(args[1:], stdin, stdout, stderr)
	case "qoder-transcript-check":
		return runInternalQoderTranscriptCheck(args[1:], stdin, stdout, stderr)
	case "plan-premortem-check":
		return runInternalPlanPremortemCheck(args[1:], stdin, stdout, stderr)
	case "review-freshness-check":
		return runInternalReviewFreshnessCheck(args[1:], stdin, stdout, stderr)
	case "codex-provider-schema-check":
		return runInternalCodexSchemaCheck(args[1:], stdin, stdout, stderr)
	case "closure-matrix-check":
		return runInternalClosureMatrixCheck(args[1:], stdin, stdout, stderr)
	default:
		fmt.Fprintln(stderr, "内部调用无效。")
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
	AdapterID                      string          `json:"adapterId"`
	EnvironmentVariable            string          `json:"environmentVariable"`
	Configured                     bool            `json:"configured"`
	Registered                     bool            `json:"registered"`
	Outcome                        string          `json:"outcome"`
	AuthorityEndpointStatus        string          `json:"authorityEndpointStatus,omitempty"`
	AuthorityDeploymentStatus      string          `json:"authorityDeploymentStatus,omitempty"`
	AuthorityMode                  string          `json:"authorityMode,omitempty"`
	Compatibility                  string          `json:"compatibility"`
	AdapterVersion                 string          `json:"adapterVersion,omitempty"`
	BinaryVersion                  string          `json:"binaryVersion,omitempty"`
	ExecutableDigest               string          `json:"executableDigest,omitempty"`
	ConformanceEvidenceDigest      string          `json:"conformanceEvidenceDigest,omitempty"`
	ConformanceTrustRootKeyID      string          `json:"conformanceTrustRootKeyId,omitempty"`
	ConformanceProbeProfileDigest  string          `json:"conformanceProbeProfileDigest,omitempty"`
	ConformanceValidUntil          string          `json:"conformanceValidUntil,omitempty"`
	ConformanceHostFingerprint     string          `json:"conformanceHostFingerprint,omitempty"`
	ConformanceAuthorityGeneration uint64          `json:"conformanceAuthorityGeneration,omitempty"`
	CodexAuthority                 json.RawMessage `json:"codexAuthority,omitempty"`
	AdapterFailure                 json.RawMessage `json:"adapterFailure,omitempty"`
}

type doctorSnapshotIdentity struct {
	AdapterID                      string          `json:"adapterId"`
	AdapterVersion                 string          `json:"adapterVersion"`
	BinaryVersion                  string          `json:"binaryVersion"`
	ExecutableDigest               string          `json:"executableDigest"`
	ProbeStatus                    string          `json:"probeStatus"`
	AuthorityMode                  string          `json:"authorityMode"`
	ConformanceEvidenceDigest      string          `json:"conformanceEvidenceDigest"`
	ConformanceTrustRootKeyID      string          `json:"conformanceTrustRootKeyId"`
	ConformanceProbeProfileDigest  string          `json:"conformanceProbeProfileDigest"`
	ConformanceValidUntil          string          `json:"conformanceValidUntil"`
	ConformanceHostFingerprint     string          `json:"conformanceHostFingerprint"`
	ConformanceAuthorityGeneration uint64          `json:"conformanceAuthorityGeneration"`
	CodexAuthority                 json.RawMessage `json:"codexAuthority"`
	AdapterFailure                 json.RawMessage `json:"adapterFailure"`
}

type doctorCodexAuthority struct {
	EvidenceDigest      string `json:"evidenceDigest"`
	TrustRootKeyID      string `json:"trustRootKeyId"`
	ProfileDigest       string `json:"profileDigest"`
	ValidUntil          string `json:"validUntil"`
	HostIdentityDigest  string `json:"hostIdentityDigest"`
	AuthorityGeneration uint64 `json:"authorityGeneration"`
}

type doctorReport struct {
	Status                   string                                       `json:"status"`
	Build                    buildinfo.Info                               `json:"build"`
	ContractSchemas          int                                          `json:"contractSchemas"`
	WorkerAdapters           int                                          `json:"workerAdapters"`
	Milestone                string                                       `json:"milestone"`
	Workers                  []doctorWorker                               `json:"workers"`
	Discovery                []app.Discovery                              `json:"discovery"`
	TimeoutCandidates        []lifecycle.TimeoutCandidate                 `json:"timeoutCandidates"`
	Run                      *reconciliation.Report                       `json:"run,omitempty"`
	Repair                   *reconciliation.RepairResult                 `json:"repair,omitempty"`
	SelfIdentity             *selfidentity.LocalSelfIdentityObservationV1 `json:"selfIdentity,omitempty"`
	PolicyEnvironmentBinding *planning.LocalDogfoodEnvironmentBinding     `json:"policyEnvironmentBinding,omitempty"`
}

type doctorOptions struct {
	jsonOutput     bool
	runID          string
	repair         bool
	printEnv       bool
	self           bool
	repositoryRoot string
	activationID   string
	issuedAtText   string
	validUntilText string
	validFor       time.Duration
}

type singleBoolFlag struct {
	seen  bool
	value bool
}

func (value *singleBoolFlag) Set(raw string) error {
	if value.seen {
		return errors.New("布尔参数不得重复")
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return err
	}
	value.seen = true
	value.value = parsed
	return nil
}

func (*singleBoolFlag) IsBoolFlag() bool { return true }

func (value *singleBoolFlag) String() string {
	return strconv.FormatBool(value.value)
}

func parseDoctorOptions(args []string, stderr io.Writer) (doctorOptions, bool) {
	var options doctorOptions
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.BoolVar(&options.jsonOutput, "json", false, "以 JSON 输出")
	flags.StringVar(&options.runID, "run", "", "核验指定 Run 的本地证据")
	flags.BoolVar(&options.repair, "repair", false, "显式修复可证明的本地 Snapshot")
	flags.BoolVar(&options.printEnv, "print-env", false, "仅打印建议式发现的 export 行，供用户粘贴")
	var self singleBoolFlag
	flags.Var(&self, "self", "只输出 canonical LocalDogfoodActivationV1")
	flags.StringVar(&options.repositoryRoot, "repository-root", ".", "本地 dogfood activation 的 canonical 仓库根")
	flags.StringVar(&options.activationID, "activation-id", "", "可选 activation ID；缺失时随机生成")
	flags.StringVar(&options.issuedAtText, "issued-at", "", "可选 RFC3339 UTC 签发时间")
	flags.StringVar(&options.validUntilText, "valid-until", "", "可选 RFC3339 UTC 失效时间")
	flags.DurationVar(&options.validFor, "valid-for", 8*time.Hour, "未显式给出时间时的 freshness（最大 24h）")
	if err := flags.Parse(args); err != nil {
		return doctorOptions{}, false
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "doctor 不接受位置参数。")
		return doctorOptions{}, false
	}
	options.self = self.value
	return options, true
}

func runDoctor(ctx context.Context, options doctorOptions, stdout, stderr io.Writer) int {
	if options.self {
		if options.runID != "" || options.repair || options.printEnv || options.validFor <= 0 {
			fmt.Fprintln(stderr, "doctor --self 参数无效。")
			return ExitUsage
		}
		now := localNow().UTC().Truncate(time.Second)
		issuedAt := now
		validUntil := now.Add(options.validFor)
		if (options.issuedAtText == "") != (options.validUntilText == "") {
			fmt.Fprintln(stderr, "doctor --self 的 --issued-at 与 --valid-until 必须同时提供。")
			return ExitUsage
		}
		if options.issuedAtText != "" {
			var parseErr error
			issuedAt, parseErr = time.Parse(time.RFC3339, options.issuedAtText)
			if parseErr == nil {
				validUntil, parseErr = time.Parse(time.RFC3339, options.validUntilText)
			}
			if parseErr != nil {
				fmt.Fprintln(stderr, "doctor --self 时间无效。")
				return ExitUsage
			}
		}
		build := localBuildInfo()
		activation, err := selfidentity.RenderActivation(selfidentity.BootstrapOptions{
			RepositoryRoot: options.repositoryRoot, ActivationID: options.activationID,
			IssuedAt: issuedAt, ValidUntil: validUntil,
			Build: selfidentity.BuildIdentity{SourceHead: build.Commit, SelfProfile: build.SelfProfile},
		})
		if err != nil {
			fmt.Fprintf(stderr, "doctor --self 失败：%s。\n", selfidentity.ReasonCode(err))
			return ExitUnavailable
		}
		if _, err := stdout.Write(activation); err != nil {
			fmt.Fprintln(stderr, "doctor --self 输出失败。")
			return ExitFailure
		}
		return ExitOK
	}
	if options.runID != "" {
		if err := domain.ValidateID(options.runID); err != nil {
			fmt.Fprintln(stderr, "doctor 失败：Run ID 无效。")
			return ExitUsage
		}
	}
	if options.repair && options.runID == "" {
		fmt.Fprintln(stderr, "doctor --repair 必须同时指定 --run RUN_ID。")
		return ExitUsage
	}
	if options.printEnv {
		for _, entry := range doctorDiscovery(ctx) {
			if entry.SuggestedEnv != "" {
				fmt.Fprintf(stdout, "export %s=%s\n", entry.EnvironmentVariable, entry.SuggestedEnv)
			}
		}
		return ExitOK
	}
	application, err := app.New()
	if err != nil {
		fmt.Fprintln(stderr, "doctor 失败：应用初始化失败。")
		return ExitFailure
	}
	runtime, err := newWorkerRuntime(os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "doctor 失败：Worker Runtime 初始化失败。")
		return ExitFailure
	}
	workers := doctorWorkers(ctx, runtime)
	report := doctorReport{
		Status:            "ok",
		Build:             buildinfo.Current(),
		ContractSchemas:   application.ContractCount(),
		WorkerAdapters:    len(runtime.Registry().IDs()),
		Milestone:         buildinfo.Milestone,
		Workers:           workers,
		Discovery:         doctorDiscovery(ctx),
		TimeoutCandidates: doctorTimeoutCandidates(ctx, time.Now().UTC()),
		SelfIdentity:      localDogfoodObservation(ctx),
	}
	if report.SelfIdentity != nil {
		binding := planning.LocalDogfoodEnvironmentBindingForObservation(*report.SelfIdentity)
		report.PolicyEnvironmentBinding = &binding
	}
	if options.runID != "" {
		location, err := repository.Discover(".")
		if err != nil || location.ValidateIdentity() != nil {
			fmt.Fprintln(stderr, "doctor 失败：无法验证仓库身份。")
			return ExitFailure
		}
		input := reconciliation.Input{StateRoot: location.StateRoot, RepositoryRoot: location.RepositoryRoot, RunID: options.runID, Validator: runtime.Validator()}
		if options.repair {
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
	if options.jsonOutput {
		if err := writeJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "输出 doctor 报告失败：%v\n", err)
			return ExitFailure
		}
		return exitCode
	}
	fmt.Fprintf(stdout, "状态：%s\nSchema：%d 份已编译\n已注册 Worker Adapter：%d\n", report.Status, report.ContractSchemas, report.WorkerAdapters)
	for _, worker := range report.Workers {
		fmt.Fprintf(stdout, "Worker %s：%s / %s", worker.AdapterID, worker.Outcome, worker.Compatibility)
		if worker.AuthorityEndpointStatus != "" {
			fmt.Fprintf(stdout, " / authority=%s", worker.AuthorityEndpointStatus)
		}
		if worker.AuthorityDeploymentStatus != "" {
			fmt.Fprintf(stdout, " / deployment=%s", worker.AuthorityDeploymentStatus)
		}
		if worker.AuthorityMode != "" {
			fmt.Fprintf(stdout, " / mode=%s", worker.AuthorityMode)
		}
		if worker.BinaryVersion != "" {
			fmt.Fprintf(stdout, " (%s)", worker.BinaryVersion)
		}
		fmt.Fprintln(stdout)
	}
	for _, entry := range report.Discovery {
		if entry.SuggestedEnv == "" {
			continue
		}
		fmt.Fprintf(stdout, "建议注册 %s：export %s=%s（发现仅提供建议，不会自动注册）\n", entry.AdapterID, entry.EnvironmentVariable, entry.SuggestedEnv)
	}
	if len(report.TimeoutCandidates) > 0 {
		fmt.Fprintf(stdout, "超时候选 Run：%d 个（doctor 只报告不处置；终态处置一律由既有合法命令完成）\n", len(report.TimeoutCandidates))
		for _, candidate := range report.TimeoutCandidates {
			fmt.Fprintf(stdout, "- %s：%s / 已挂起 %s / %s / 处置指引：%s", candidate.RunID, candidate.State, candidate.HungFor, candidate.Category, candidate.Guidance)
			if command := lifecycle.GuidanceCommand(candidate.Guidance, candidate.RunID); command != "" {
				fmt.Fprintf(stdout, "（%s）", command)
			}
			fmt.Fprintln(stdout)
		}
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
			AdapterID:                 configuration.AdapterID,
			EnvironmentVariable:       configuration.EnvironmentVariable,
			Configured:                configuration.Configured,
			Registered:                configuration.Registered,
			Outcome:                   configuration.Outcome,
			AuthorityEndpointStatus:   configuration.AuthorityEndpointStatus,
			AuthorityDeploymentStatus: configuration.AuthorityDeploymentStatus,
			AuthorityMode:             configuration.AuthorityMode,
			Compatibility:             "not-probed",
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
		var identity doctorSnapshotIdentity
		if json.Unmarshal(snapshot.Data, &identity) != nil || identity.AdapterID != configuration.AdapterID ||
			(identity.ProbeStatus != "supported" && identity.ProbeStatus != "unsupported") {
			workers = append(workers, result)
			continue
		}
		applyDoctorSnapshotIdentity(&result, identity)
		workers = append(workers, result)
	}
	return workers
}

func applyDoctorSnapshotIdentity(result *doctorWorker, identity doctorSnapshotIdentity) {
	result.Compatibility = identity.ProbeStatus
	result.AdapterVersion = identity.AdapterVersion
	result.BinaryVersion = identity.BinaryVersion
	result.ExecutableDigest = identity.ExecutableDigest
	if identity.AuthorityMode == "ordinary-user" {
		if identity.AdapterID != "qoder" && identity.AdapterID != "codex" && identity.AdapterID != "qwen" && identity.AdapterID != "pi" {
			result.Compatibility = "probe-failed"
			return
		}
		if identity.ProbeStatus != "supported" || len(identity.AdapterFailure) != 0 || len(identity.CodexAuthority) != 0 || identity.ConformanceEvidenceDigest != "" {
			result.Compatibility = "probe-failed"
		}
		result.AuthorityMode = identity.AuthorityMode
		return
	}
	if identity.AdapterID == "codex" {
		if identity.ProbeStatus == "supported" {
			if len(identity.CodexAuthority) == 0 || len(identity.AdapterFailure) != 0 {
				result.Compatibility = "probe-failed"
				return
			}
			var authority doctorCodexAuthority
			if json.Unmarshal(identity.CodexAuthority, &authority) != nil ||
				authority.EvidenceDigest == "" || authority.TrustRootKeyID == "" || authority.ProfileDigest == "" || authority.ValidUntil == "" || authority.HostIdentityDigest == "" || authority.AuthorityGeneration == 0 ||
				identity.ConformanceEvidenceDigest != authority.EvidenceDigest || identity.ConformanceTrustRootKeyID != authority.TrustRootKeyID || identity.ConformanceProbeProfileDigest != authority.ProfileDigest || identity.ConformanceValidUntil != authority.ValidUntil || identity.ConformanceHostFingerprint != authority.HostIdentityDigest || identity.ConformanceAuthorityGeneration != authority.AuthorityGeneration {
				result.Compatibility = "probe-failed"
				return
			}
			result.CodexAuthority = append(json.RawMessage(nil), identity.CodexAuthority...)
			result.ConformanceEvidenceDigest = identity.ConformanceEvidenceDigest
			result.ConformanceTrustRootKeyID = identity.ConformanceTrustRootKeyID
			result.ConformanceProbeProfileDigest = identity.ConformanceProbeProfileDigest
			result.ConformanceValidUntil = identity.ConformanceValidUntil
			result.ConformanceHostFingerprint = identity.ConformanceHostFingerprint
			result.ConformanceAuthorityGeneration = identity.ConformanceAuthorityGeneration
		} else {
			if len(identity.AdapterFailure) == 0 || len(identity.CodexAuthority) != 0 {
				result.Compatibility = "probe-failed"
				return
			}
			result.AdapterFailure = append(json.RawMessage(nil), identity.AdapterFailure...)
		}
		return
	}
	if identity.ProbeStatus != "supported" || identity.AdapterID != "qoder" {
		return
	}
	if identity.ConformanceEvidenceDigest == "" || identity.ConformanceTrustRootKeyID == "" || identity.ConformanceProbeProfileDigest == "" || identity.ConformanceValidUntil == "" || identity.ConformanceHostFingerprint == "" || identity.ConformanceAuthorityGeneration == 0 {
		result.Compatibility = "probe-failed"
		return
	}
	result.ConformanceEvidenceDigest = identity.ConformanceEvidenceDigest
	result.ConformanceTrustRootKeyID = identity.ConformanceTrustRootKeyID
	result.ConformanceProbeProfileDigest = identity.ConformanceProbeProfileDigest
	result.ConformanceValidUntil = identity.ConformanceValidUntil
	result.ConformanceHostFingerprint = identity.ConformanceHostFingerprint
	result.ConformanceAuthorityGeneration = identity.ConformanceAuthorityGeneration
}

// doctorDiscovery returns the advisory discovery outcome for adapters whose
// environment variable is unset. Discovery never registers anything; a nil
// result is normalized so doctor JSON always carries a structured discovery
// field.
func doctorDiscovery(ctx context.Context) []app.Discovery {
	discovery := app.DiscoverWorkers(ctx, os.Getenv)
	if discovery == nil {
		discovery = []app.Discovery{}
	}
	return discovery
}

// doctorTimeoutCandidates is the issue #68 wall-clock watchdog wiring: it
// scans every local Run and reports the timeout candidates classified by
// the pure lifecycle watchdog. Doctor only reports: it never writes Run
// state and never executes an abort; terminal disposition is always
// completed through the existing legal command each guidance sentinel
// points to. A repository whose identity cannot be verified, an unreadable
// runs directory, a Run whose evidence does not inspect cleanly, a terminal
// Run and a Run without a readable positive RunTimeoutSeconds budget are
// all skipped fail-closed instead of being reported.
func doctorTimeoutCandidates(ctx context.Context, now time.Time) []lifecycle.TimeoutCandidate {
	candidates := make([]lifecycle.TimeoutCandidate, 0)
	location, err := repository.Discover(".")
	if err != nil || location.ValidateIdentity() != nil {
		return candidates
	}
	entries, err := os.ReadDir(filepath.Join(location.StateRoot, "runs"))
	if err != nil {
		return candidates
	}
	store := runstore.New(location.StateRoot)
	for _, entry := range entries {
		if ctx.Err() != nil {
			return candidates
		}
		if !entry.IsDir() {
			continue
		}
		runID := entry.Name()
		state, inspectErr := store.Inspect(runID)
		if inspectErr != nil || state.State.Terminal() {
			continue
		}
		timeoutSeconds, ok := doctorRunTimeoutSeconds(filepath.Join(location.StateRoot, "runs", runID))
		if !ok {
			continue
		}
		verdict := lifecycle.WatchdogForRun(state, timeoutSeconds, now)
		if !verdict.TimedOut {
			continue
		}
		candidates = append(candidates, lifecycle.CandidateFromVerdict(runID, state.State, verdict))
	}
	return candidates
}

// doctorRunTimeoutSeconds extracts the frozen RunTimeoutSeconds budget from
// the Run's frozen TaskSpec; a missing, unreadable or non-positive budget
// fails closed as absent so the watchdog never judges a Run without a
// defined window.
func doctorRunTimeoutSeconds(runDirectory string) (int64, bool) {
	data, err := os.ReadFile(filepath.Join(runDirectory, "task-spec.json"))
	if err != nil {
		return 0, false
	}
	var spec struct {
		Budgets struct {
			RunTimeoutSeconds int64 `json:"runTimeoutSeconds"`
		} `json:"budgets"`
	}
	if json.Unmarshal(data, &spec) != nil || spec.Budgets.RunTimeoutSeconds <= 0 {
		return 0, false
	}
	return spec.Budgets.RunTimeoutSeconds, true
}

func runContract(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "用法：marshal contract <validate|schema>")
		return ExitUsage
	}
	switch args[0] {
	case "validate":
		return runContractValidate(args[1:], stdin, stdout, stderr)
	case "schema":
		return runContractSchema(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "未知 contract 子命令 %q。\n", args[0])
		fmt.Fprintln(stderr, "用法：marshal contract <validate|schema>")
		return ExitUsage
	}
}

func runContractValidate(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("contract validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	schemaName := flags.String("schema", "", "显式 Schema 名称；省略时从 kind 检测")
	if err := flags.Parse(args); err != nil {
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

func runContractSchema(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("contract schema", flag.ContinueOnError)
	flags.SetOutput(stderr)
	schemaName := flags.String("schema", "", "仅导出指定名称的单个 Schema")
	all := flags.Bool("all", false, "导出全部内嵌 Schema 与示例清单")
	out := flags.String("out", "", "与 --all 配合，将文件集写入指定目录")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出 Schema 名称与版本列表")
	if err := flags.Parse(args); err != nil {
		return ExitUsage
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "contract schema 不接受位置参数。")
		return ExitUsage
	}
	if *out != "" && !*all {
		fmt.Fprintln(stderr, "--out 只能与 --all 一起使用。")
		return ExitUsage
	}
	if *schemaName != "" && (*all || *jsonOutput || *out != "") {
		fmt.Fprintln(stderr, "--schema 不能与 --all、--json 或 --out 一起使用。")
		return ExitUsage
	}
	if *jsonOutput && *all {
		fmt.Fprintln(stderr, "--all 已输出 JSON，无需 --json。")
		return ExitUsage
	}

	if *schemaName != "" {
		descriptor, err := contract.DescriptorByName(*schemaName)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return ExitUsage
		}
		data, err := contract.SchemaDocument(descriptor.Name)
		if err != nil {
			fmt.Fprintf(stderr, "读取 Schema 失败：%v\n", err)
			return ExitFailure
		}
		if _, err := stdout.Write(data); err != nil {
			fmt.Fprintf(stderr, "输出 Schema 失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}

	catalog, err := contract.ExportCatalog()
	if err != nil {
		fmt.Fprintf(stderr, "导出契约目录失败：%v\n", err)
		return ExitFailure
	}
	if *all {
		if *out == "" {
			if err := writeJSON(stdout, catalog); err != nil {
				fmt.Fprintf(stderr, "输出契约目录失败：%v\n", err)
				return ExitFailure
			}
			return ExitOK
		}
		if err := exportCatalogFiles(catalog, *out); err != nil {
			fmt.Fprintf(stderr, "导出契约目录失败：%v\n", err)
			return ExitFailure
		}
		fmt.Fprintf(stdout, "已导出 %d 个 Schema 至 %s\n", len(catalog.Schemas), *out)
		return ExitOK
	}

	if *jsonOutput {
		entries := make([]struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}, 0, len(catalog.Schemas))
		for _, schema := range catalog.Schemas {
			entries = append(entries, struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			}{Name: schema.Name, Version: schema.Version})
		}
		if err := writeJSON(stdout, entries); err != nil {
			fmt.Fprintf(stderr, "输出 Schema 列表失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}
	for _, schema := range catalog.Schemas {
		fmt.Fprintf(stdout, "%s %s\n", schema.Name, schema.Version)
	}
	return ExitOK
}

func exportCatalogFiles(catalog contract.Catalog, directory string) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	for _, schema := range catalog.Schemas {
		data, err := contract.SchemaDocument(schema.Name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, schema.SchemaFile), data, 0o644); err != nil {
			return err
		}
		for _, example := range schema.Examples {
			fixture, err := contract.ExampleDocument(example.Path)
			if err != nil {
				return err
			}
			target := filepath.Join(directory, filepath.FromSlash(example.Path))
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(target, fixture, 0o644); err != nil {
				return err
			}
		}
	}
	catalogData, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("encode catalog: %w", err)
	}
	return os.WriteFile(filepath.Join(directory, "catalog.json"), append(catalogData, '\n'), 0o644)
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

const superviseUsage = "用法：marshal supervise [--once] [--interval DURATION] [--marshal-binary PATH] [--revive-retry-pending] [--json]"

// superviseDecision is the stable JSON projection of one supervisor
// DecisionRecord for supervise CLI output. SkipReason is non-empty when the
// candidate was deliberately not dispatched (supervise-exclude list or
// write-domain conflict, issue #100).
type superviseDecision struct {
	RunID      string `json:"runId"`
	State      string `json:"state"`
	Action     string `json:"action"`
	Started    bool   `json:"started"`
	Error      string `json:"error,omitempty"`
	SkipReason string `json:"skipReason,omitempty"`
}

// runSupervise drives the supervisor. Issue #100 semantics, all fail-closed:
// RETRY_PENDING Runs are NOT revived automatically by default — the
// --revive-retry-pending flag is the explicit opt-in restoring the legacy
// behaviour; Runs listed in the stateRoot-relative supervise-exclude list
// are never re-dispatched (a readable list is mandatory: an unreadable list
// aborts the round with zero dispatches); and before any re-dispatch the
// candidate's frozen TaskSpec write domain is checked against every
// in-flight Run's write domain.
func runSupervise(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("supervise", flag.ContinueOnError)
	flags.SetOutput(stderr)
	once := flags.Bool("once", false, "只执行一轮监督决策后退出")
	interval := flags.Duration("interval", 30*time.Second, "常驻监督轮询间隔（必须为正时长）")
	marshalBinary := flags.String("marshal-binary", "", "用于派发驱动的 marshal 可执行文件路径（默认当前可执行文件）")
	reviveRetryPending := flags.Bool("revive-retry-pending", false, "显式复活 RETRY_PENDING 的 Run（默认不再自动重派，issue #100）")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出决策记录")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, superviseUsage)
		return ExitUsage
	}
	if *interval <= 0 {
		fmt.Fprintln(stderr, "supervise 失败：--interval 必须为正时长。")
		fmt.Fprintln(stderr, superviseUsage)
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil || location.ValidateIdentity() != nil {
		fmt.Fprintln(stderr, "supervise 失败：无法验证仓库身份。")
		return ExitFailure
	}
	binary := *marshalBinary
	if binary == "" {
		executable, err := os.Executable()
		if err != nil {
			fmt.Fprintln(stderr, "supervise 失败：无法定位当前可执行文件。")
			return ExitFailure
		}
		binary = executable
	}
	driver, err := supervisor.New(location.StateRoot, binary, supervisor.WithReviveRetryPending(*reviveRetryPending))
	if err != nil {
		fmt.Fprintf(stderr, "supervise 失败：%v\n", err)
		return ExitFailure
	}
	if *once {
		return superviseOnce(ctx, driver, *jsonOutput, stdout, stderr)
	}
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stdout, "Marshal supervise 常驻运行：interval=%s；SIGINT/SIGTERM 优雅退出。\n", *interval)
	if err := driver.Loop(signalCtx, *interval); err != nil {
		fmt.Fprintf(stderr, "supervise 失败：%v\n", err)
		return ExitFailure
	}
	fmt.Fprintln(stdout, "Marshal supervise 已退出。")
	return ExitOK
}

// superviseOnce runs a single Supervise round and prints one line per
// decision (runID/state/action/started). The exit code is ExitOK when every
// action started cleanly or there was nothing to supervise, and ExitFailure
// when any decision carries a spawn error.
func superviseOnce(ctx context.Context, driver *supervisor.Supervisor, jsonOutput bool, stdout, stderr io.Writer) int {
	records, err := driver.Supervise(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "supervise 失败：%v\n", err)
		return ExitFailure
	}
	exitCode := ExitOK
	decisions := make([]superviseDecision, 0, len(records))
	for _, record := range records {
		if record.Error != "" {
			exitCode = ExitFailure
		}
		decisions = append(decisions, superviseDecision{RunID: record.RunID, State: string(record.State), Action: record.Action.String(), Started: record.Started, Error: record.Error, SkipReason: record.SkipReason})
	}
	if jsonOutput {
		if err := writeJSON(stdout, decisions); err != nil {
			fmt.Fprintf(stderr, "输出监督结果失败：%v\n", err)
			return ExitFailure
		}
		return exitCode
	}
	if len(decisions) == 0 {
		fmt.Fprintln(stdout, "无可监督的 Run。")
		return exitCode
	}
	for _, decision := range decisions {
		if decision.SkipReason != "" {
			fmt.Fprintf(stdout, "Run：%s 状态：%s 动作：%s 已跳过：%s\n", decision.RunID, decision.State, decision.Action, decision.SkipReason)
			continue
		}
		fmt.Fprintf(stdout, "Run：%s 状态：%s 动作：%s 已启动：%t\n", decision.RunID, decision.State, decision.Action, decision.Started)
		if decision.Error != "" {
			fmt.Fprintf(stdout, "启动错误：%s\n", decision.Error)
		}
	}
	return exitCode
}

func runTask(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || !slices.Contains(taskCommands, args[0]) {
		fmt.Fprintf(stderr, "用法：marshal task <%s>\n", strings.Join(taskCommands, "|"))
		return ExitUsage
	}
	if args[0] == "plan" {
		return runTaskPlan(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "scaffold" {
		return runTaskScaffold(args[1:], stdin, stdout, stderr)
	}
	if args[0] == "status" {
		return runTaskStatus(ctx, args[1:], stdout, stderr)
	}
	if args[0] == "approve" {
		return runTaskApprove(ctx, args[1:], stdout, stderr)
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
	if args[0] == "reconcile" {
		return runTaskReconcile(ctx, args[1:], stdout, stderr)
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
	if args[0] == "migrate-outcomes" {
		return runTaskMigrateOutcomes(ctx, args[1:], stdout, stderr)
	}
	if len(args) != 1 {
		return ExitUsage
	}
	fmt.Fprintf(stderr, "marshal task %s 尚未实现；未执行任何 Worker、状态写入或发布副作用。\n", args[0])
	return ExitUnavailable
}

const cleanupUsage = "用法：marshal task cleanup --run RUN_ID [--export-patch --actor ID] [--apply] [--json]\n" +
	"       marshal task cleanup --run RUN_ID --record-outcome --actor ID [--json]\n" +
	"       marshal task cleanup --expired [--apply --actor ID] [--json]"

func runTaskCleanup(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task cleanup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	apply := flags.Bool("apply", false, "执行预览中的本地清理")
	exportPatch := flags.Bool("export-patch", false, "将 dirty 托管 Worktree 的未归档变更导出到 .marshal/archive")
	expired := flags.Bool("expired", false, "按 retentionDays 列出并清理已过期的终态 Run")
	recordOutcome := flags.Bool("record-outcome", false, "为缺 Outcome 的终态 Run 重建终态 Outcome（遗留迁移）")
	actor := flags.String("actor", "", "操作者 ID")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, cleanupUsage)
		return ExitUsage
	}
	trimmedActor := strings.TrimSpace(*actor)
	if *expired {
		if *runID != "" || *exportPatch || (*apply && trimmedActor == "") {
			fmt.Fprintln(stderr, cleanupUsage)
			return ExitUsage
		}
		location, err := repository.Discover(".")
		if err != nil || location.ValidateIdentity() != nil {
			fmt.Fprintln(stderr, "清理失败：无法验证仓库身份。")
			return ExitFailure
		}
		result, err := cleanupservice.ExecuteExpired(ctx, cleanupservice.ExpiredInput{
			StateRoot: location.StateRoot, RepositoryRoot: location.RepositoryRoot,
			Apply: *apply, Actor: trimmedActor, Now: time.Now().UTC(),
		})
		if err != nil {
			fmt.Fprintf(stderr, "清理失败：%v\n", err)
			return ExitFailure
		}
		return writeExpiredResult(result, *apply, *jsonOutput, stdout, stderr)
	}
	if *runID == "" || (*exportPatch && *apply) || (*exportPatch && trimmedActor == "") || (*recordOutcome && (*apply || *exportPatch || *expired)) {
		fmt.Fprintln(stderr, cleanupUsage)
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
	if *recordOutcome {
		result, err := cleanupservice.RecordLegacyOutcome(ctx, cleanupservice.Input{
			StateRoot: location.StateRoot, RepositoryRoot: location.RepositoryRoot, RunID: *runID,
			Actor: trimmedActor, Now: time.Now().UTC(), Validator: validator,
		})
		if err != nil {
			fmt.Fprintf(stderr, "记录遗留 Outcome 失败：%v\n", err)
			return ExitFailure
		}
		if err := writeJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "输出结果失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}
	result, err := cleanupservice.Execute(ctx, cleanupservice.Input{
		StateRoot: location.StateRoot, RepositoryRoot: location.RepositoryRoot, RunID: *runID,
		Apply: *apply, ExportPatch: *exportPatch, Actor: trimmedActor, Now: time.Now().UTC(), Validator: validator,
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
	if result.Exported {
		fmt.Fprintf(stdout, "Run %s 的未归档变更已导出：%s\n摘要：%s\n后续可执行 marshal task cleanup --run %s --apply。\n",
			result.RunID, result.ArchivePath, result.ArchiveDigest, result.RunID)
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

func writeExpiredResult(result cleanupservice.ExpiredResult, apply, jsonOutput bool, stdout, stderr io.Writer) int {
	if jsonOutput {
		if err := writeJSON(stdout, result); err != nil {
			fmt.Fprintf(stderr, "输出清理结果失败：%v\n", err)
			return ExitFailure
		}
	} else if len(result.Runs) == 0 {
		fmt.Fprintln(stdout, "没有已过期的终态 Run。")
	} else {
		if apply {
			fmt.Fprintln(stdout, "过期 Run 清理完成：")
		} else {
			fmt.Fprintln(stdout, "过期 Run 预览（未执行；使用 --apply 执行）：")
		}
		for _, run := range result.Runs {
			fmt.Fprintf(stdout, "- %s：%s / retentionDays=%d / updatedAt=%s / outcome=%s\n",
				run.RunID, run.State, run.RetentionDays, run.UpdatedAt.Format(time.RFC3339), run.Outcome)
			for _, target := range run.Targets {
				fmt.Fprintf(stdout, "  - %s：%s（%s）\n", target.Kind, target.Path, target.Action)
			}
		}
	}
	if !apply {
		return ExitOK
	}
	for _, run := range result.Runs {
		if run.Outcome != cleanupservice.OutcomeRemoved && run.Outcome != cleanupservice.OutcomeRemovedWorktreeKept {
			return ExitFailure
		}
	}
	return ExitOK
}

// ADR 0029 pre-attempt abort rejection sentinels (closed set, machine
// readable). The sentinel is the fixed skeleton of every rejection message
// and of the --json rejection output; operator free text never participates
// in it. Extending the set requires an ADR revision, never code alone.
const (
	abortDeniedAttemptExists         = "abort-denied-attempt-exists"
	abortDeniedPublicationIntent     = "abort-denied-publication-intent-present"
	abortDeniedSideEffect            = "abort-denied-side-effect-present"
	abortDeniedPublicationPresent    = "abort-denied-publication-present"
	abortDeniedPublicationInProgress = "abort-denied-publication-in-progress"
	abortDeniedStateNotEligible      = "abort-denied-state-not-eligible"
)

var abortDeniedGuidance = map[string]string{
	abortDeniedAttemptExists:         "存在 Attempt 记录；等待状态推进后经 RETRY_PENDING abort 出口处置，或经 intervention 路径处置活跃 Run",
	abortDeniedPublicationIntent:     "存在发布意图记录；先行处置发布意图（撤销/对账），或人工护栏",
	abortDeniedSideEffect:            "存在 SideEffect 记录或无法判定的效果事实；经 append-only 副作用对账/补偿处置，或人工护栏",
	abortDeniedPublicationPresent:    "存在发布记录或已发布分支；经 typed reconciliation 处置，或人工护栏",
	abortDeniedPublicationInProgress: "发布事务进行中；待发布事务落定后经 reconcile 处置，或人工护栏",
	abortDeniedStateNotEligible:      "当前状态不允许显式终止；活跃 Run 经 intervention 路径处置",
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
	if state.State.Terminal() {
		return finishTerminalAbortRequest(location, store, lease, state, abortActor, abortReason, *jsonOutput, stdout, stderr)
	}
	switch state.State {
	case domain.StateRetryPending:
		return abortRetryPendingRun(location, store, lease, state, abortActor, abortReason, *jsonOutput, stdout, stderr)
	case domain.StatePlanned, domain.StateReady:
		return abortPreAttemptRun(location, store, lease, state, abortActor, abortReason, *jsonOutput, stdout, stderr)
	case domain.StatePublishing:
		return writeAbortDenied(abortDeniedPublicationInProgress, state.State, *jsonOutput, stdout, stderr)
	case domain.StatePublished, domain.StateCIPending:
		return writeAbortDenied(abortDeniedPublicationPresent, state.State, *jsonOutput, stdout, stderr)
	default:
		return writeAbortDenied(abortDeniedStateNotEligible, state.State, *jsonOutput, stdout, stderr)
	}
}

// abortRetryPendingRun is the unchanged ADR 0012 exit: RETRY_PENDING ->
// BLOCKED with terminalReason aborted-by-operator, preserving the current
// Attempt identity on the event and every frozen message.
func abortRetryPendingRun(location repository.State, store *runstore.Store, lease *runstore.Lease, state domain.RunState, abortActor, abortReason string, jsonOutput bool, stdout, stderr io.Writer) int {
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
	runDirectory := filepath.Join(location.StateRoot, "runs", state.RunID)
	prepared, err := review.PrepareOutcome(runDirectory, review.OutcomeData{
		TaskID: state.TaskID, RunID: state.RunID, TerminalState: domain.StateBlocked, Verdict: "abort",
		FinalReviewRound: max(1, state.ReviewRound), FinalReviewDigest: abortDigest, FinalEvidenceDigest: abortDigest,
		Summary: abortReason, FindingCount: 0, GeneratedAt: timestamp,
	})
	if err != nil {
		fmt.Fprintf(stderr, "终止失败：准备终态 Outcome：%v\n", err)
		return ExitFailure
	}
	if err := stageAbortResult(runDirectory, state, abortActor, abortReason, timestamp, domain.StateBlocked, lifecycle.AbortTerminalReason); err != nil {
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
	return writeAbortSuccess(event, lifecycle.AbortTerminalReason, abortActor, jsonOutput, stdout, stderr)
}

// abortPreAttemptRun is the ADR 0029 exit: PLANNED/READY Runs that never
// produced an Attempt terminate to ABORTED with terminalReason
// aborted-before-attempt. Every negative fact is proven affirmatively
// against the authoritative storage before any write; any conflict fails
// closed with byte-for-byte unchanged journal, state, outcome and worktree.
func abortPreAttemptRun(location repository.State, store *runstore.Store, lease *runstore.Lease, state domain.RunState, abortActor, abortReason string, jsonOutput bool, stdout, stderr io.Writer) int {
	runDirectory := filepath.Join(location.StateRoot, "runs", state.RunID)
	events, _, err := store.ReadEvents(state.RunID)
	if err != nil {
		fmt.Fprintf(stderr, "终止失败：%v\n", err)
		return ExitFailure
	}
	if uint64(len(events)) != state.Sequence {
		fmt.Fprintln(stderr, "终止失败：Run 快照与日志不一致，需先执行对账。")
		return ExitFailure
	}
	if sentinel, ok := provePreAttemptAbsence(store, state, runDirectory, events); !ok {
		return writeAbortDenied(sentinel, state.State, jsonOutput, stdout, stderr)
	}
	eventID, err := domain.NewID("event")
	if err != nil {
		fmt.Fprintf(stderr, "终止失败：生成事件 ID：%v\n", err)
		return ExitFailure
	}
	timestamp := time.Now().UTC()
	payload := map[string]any{"terminalReason": lifecycle.PreAttemptAbortTerminalReason, "reason": abortReason}
	event := domain.RunEvent{
		APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: state.RunID,
		AttemptID: "", Sequence: state.Sequence + 1, Type: lifecycle.AbortEventType,
		StateFrom: state.State, StateTo: domain.StateAborted, Timestamp: timestamp,
		Actor: &domain.Actor{Type: domain.ControlSourceTypeHuman, ID: abortActor}, Payload: payload,
	}
	nextState, err := lifecycle.Reduce(state, event, lifecycle.Guard{LeaseHeld: true, AbortAuthorized: true, ChildrenStopped: true, EvidenceFlushed: true, PreAttemptAbsenceProven: true})
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
	prepared, err := review.PrepareOutcome(runDirectory, review.OutcomeData{
		TaskID: state.TaskID, RunID: state.RunID, TerminalState: domain.StateAborted, Verdict: "abort",
		FinalReviewRound: max(1, state.ReviewRound), FinalReviewDigest: abortDigest, FinalEvidenceDigest: abortDigest,
		Summary: abortReason, FindingCount: 0, GeneratedAt: timestamp,
	})
	if err != nil {
		fmt.Fprintf(stderr, "终止失败：准备终态 Outcome：%v\n", err)
		return ExitFailure
	}
	if err := stageAbortResult(runDirectory, state, abortActor, abortReason, timestamp, domain.StateAborted, lifecycle.PreAttemptAbortTerminalReason); err != nil {
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
	return writeAbortSuccess(event, lifecycle.PreAttemptAbortTerminalReason, abortActor, jsonOutput, stdout, stderr)
}

// finishTerminalAbortRequest decides a repeat abort request against a
// terminal Run: an identical request (same Run, same abort authority event,
// same actor/reason/request digest) idempotently returns the existing
// terminal result and completes any crash-interrupted outcome/result/
// snapshot writes from the same authority event; a divergent request against
// an abort-closed Run is a deterministic conflict with zero writes; any
// other terminal Run keeps the ADR 0012 re-entry rejection.
func finishTerminalAbortRequest(location repository.State, store *runstore.Store, lease *runstore.Lease, state domain.RunState, abortActor, abortReason string, jsonOutput bool, stdout, stderr io.Writer) int {
	events, _, err := store.ReadEvents(state.RunID)
	if err == nil {
		if authority, ok := terminalAbortAuthority(events, state); ok {
			if abortRequestMatches(authority, abortActor, abortReason) {
				return completeIdempotentAbort(location, store, lease, state, authority, jsonOutput, stdout, stderr)
			}
			fmt.Fprintf(stderr, "终止失败：Run 已处于终态 %s 且已由终止记录关闭；本次请求与该记录身份不一致，未写入任何事件或 Outcome。\n", state.State)
			return ExitFailure
		}
	}
	fmt.Fprintf(stderr, "终止失败：Run 已处于终态 %s，不能再次终止。\n", state.State)
	return ExitFailure
}

// terminalAbortAuthority locates the run.aborted event that closed the Run,
// tolerating only same-state repair-audit events after it; any other
// authority fact after the abort means the terminal state was produced by
// another exit and the request must not masquerade as an idempotent abort.
func terminalAbortAuthority(events []domain.RunEvent, state domain.RunState) (domain.RunEvent, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type == lifecycle.AbortEventType {
			return event, event.StateTo == state.State
		}
		if event.Type != lifecycle.RepairAuditEventType || event.StateFrom != state.State || event.StateTo != state.State {
			return domain.RunEvent{}, false
		}
	}
	return domain.RunEvent{}, false
}

// abortRequestMatches checks the frozen idempotency identity of a repeat
// request: the same human actor, the same operator reason, and a recorded
// payload that is exactly {terminalReason, reason} — digest equality proves
// no extra payload field was recorded, so a forged or extended abort record
// can never satisfy a repeat request.
func abortRequestMatches(event domain.RunEvent, actor, reason string) bool {
	if event.Actor == nil || event.Actor.Type != domain.ControlSourceTypeHuman || event.Actor.ID != actor {
		return false
	}
	terminalReason, _ := event.Payload["terminalReason"].(string)
	if terminalReason != lifecycle.AbortTerminalReason && terminalReason != lifecycle.PreAttemptAbortTerminalReason {
		return false
	}
	recordedReason, _ := event.Payload["reason"].(string)
	if recordedReason != reason {
		return false
	}
	recordedData, err := json.Marshal(event.Payload)
	if err != nil {
		return false
	}
	recordedDigest, err := canonical.DigestJSON(recordedData)
	if err != nil {
		return false
	}
	requestedData, err := json.Marshal(map[string]any{"terminalReason": terminalReason, "reason": reason})
	if err != nil {
		return false
	}
	requestedDigest, err := canonical.DigestJSON(requestedData)
	if err != nil {
		return false
	}
	return recordedDigest == requestedDigest
}

// completeIdempotentAbort returns the existing terminal result of an
// identical repeat request and completes outcome/result/snapshot writes that
// a crash interrupted after the journal append — always from the same
// authority event, never appending a second abort event, rewriting the first
// event or forging a new evidence digest. Committed evidence is never
// rewritten: each artifact is only created when still missing, with the
// recorded event's timestamp and payload digest.
func completeIdempotentAbort(location repository.State, store *runstore.Store, lease *runstore.Lease, state domain.RunState, authority domain.RunEvent, jsonOutput bool, stdout, stderr io.Writer) int {
	runDirectory := filepath.Join(location.StateRoot, "runs", state.RunID)
	reason, _ := authority.Payload["reason"].(string)
	terminalReason, _ := authority.Payload["terminalReason"].(string)
	actorID := authority.Actor.ID
	if !evidencePresent(filepath.Join(runDirectory, "outcome.json")) {
		payloadData, err := json.Marshal(authority.Payload)
		if err != nil {
			fmt.Fprintf(stderr, "终止失败：编码终止证据：%v\n", err)
			return ExitFailure
		}
		abortDigest, err := canonical.DigestJSON(payloadData)
		if err != nil {
			fmt.Fprintf(stderr, "终止失败：摘要终止证据：%v\n", err)
			return ExitFailure
		}
		prepared, err := review.PrepareOutcome(runDirectory, review.OutcomeData{
			TaskID: state.TaskID, RunID: state.RunID, TerminalState: state.State, Verdict: "abort",
			FinalReviewRound: max(1, state.ReviewRound), FinalReviewDigest: abortDigest, FinalEvidenceDigest: abortDigest,
			Summary: reason, FindingCount: 0, GeneratedAt: authority.Timestamp,
		})
		if err != nil {
			fmt.Fprintf(stderr, "终止失败：准备终态 Outcome：%v\n", err)
			return ExitFailure
		}
		if err := prepared.Commit(); err != nil {
			fmt.Fprintf(stderr, "终止失败：提交终态 Outcome：%v\n", err)
			return ExitFailure
		}
	}
	if !evidencePresent(filepath.Join(runDirectory, "result.md")) {
		if err := stageAbortResult(runDirectory, state, actorID, reason, authority.Timestamp, state.State, terminalReason); err != nil {
			fmt.Fprintf(stderr, "终止失败：准备终止记录：%v\n", err)
			return ExitFailure
		}
		if err := commitAbortResult(runDirectory); err != nil {
			fmt.Fprintf(stderr, "终止失败：提交终止记录：%v\n", err)
			return ExitFailure
		}
	}
	if snapshot, err := store.ReadSnapshot(state.RunID); err != nil || snapshot.Sequence != state.Sequence || snapshot.State != state.State {
		if err := store.WriteSnapshot(lease, state); err != nil {
			fmt.Fprintf(stderr, "终止失败：写入状态快照：%v\n", err)
			return ExitFailure
		}
	}
	return writeAbortSuccess(authority, terminalReason, actorID, jsonOutput, stdout, stderr)
}

// writeAbortSuccess renders one successful abort result; the text and JSON
// shapes are shared by both exits and by the idempotent repeat path.
func writeAbortSuccess(event domain.RunEvent, terminalReason, abortActor string, jsonOutput bool, stdout, stderr io.Writer) int {
	if jsonOutput {
		if err := writeJSON(stdout, struct {
			Status         string       `json:"status"`
			RunID          string       `json:"runId"`
			State          domain.State `json:"state"`
			TerminalReason string       `json:"terminalReason"`
			Actor          string       `json:"actor"`
			Sequence       uint64       `json:"sequence"`
		}{"aborted", event.RunID, event.StateTo, terminalReason, abortActor, event.Sequence}); err != nil {
			fmt.Fprintf(stderr, "输出终止结果失败：%v\n", err)
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "Run：%s\n状态：%s → %s\n终态原因：%s\n操作者：%s\n", event.RunID, event.StateFrom, event.StateTo, terminalReason, abortActor)
	return ExitOK
}

// writeAbortDenied renders one fixed sentinel rejection: the sentinel is the
// machine-readable skeleton on stderr and, when requested, on --json stdout;
// operator free text never participates in either.
func writeAbortDenied(sentinel string, state domain.State, jsonOutput bool, stdout, stderr io.Writer) int {
	fmt.Fprintf(stderr, "终止失败：%s：%s。\n", sentinel, abortDeniedGuidance[sentinel])
	if jsonOutput {
		if err := writeJSON(stdout, struct {
			Status   string       `json:"status"`
			Sentinel string       `json:"sentinel"`
			State    domain.State `json:"state"`
		}{"rejected", sentinel, state}); err != nil {
			fmt.Fprintf(stderr, "输出终止结果失败：%v\n", err)
		}
	}
	return ExitFailure
}

// provePreAttemptAbsence affirmatively proves every ADR 0029 negative fact
// against the Run's authoritative storage while the caller holds the Run
// lease. It follows the frozen decision order — Attempt records, publication
// intent, SideEffects, publication facts — and returns the fixed denial
// sentinel of the first condition that fails. Any unreadable or ambiguous
// record fails closed; absence is never presumed.
func provePreAttemptAbsence(store *runstore.Store, state domain.RunState, runDirectory string, events []domain.RunEvent) (string, bool) {
	// Negative condition 2: zero Attempt records — "never produced an
	// Attempt", not merely "no Attempt currently running".
	if state.CurrentAttemptID != "" || state.AttemptsUsed != 0 {
		return abortDeniedAttemptExists, false
	}
	for _, event := range events {
		if event.AttemptID != "" || event.Type == "worker.started" {
			return abortDeniedAttemptExists, false
		}
	}
	if !attemptTreeProvenAbsent(runDirectory) {
		return abortDeniedAttemptExists, false
	}
	// Negative condition 3: no publication intent record.
	if evidencePresent(filepath.Join(runDirectory, "publication-intent.json")) ||
		evidencePresent(filepath.Join(runDirectory, "publication-intent.json.pending")) {
		return abortDeniedPublicationIntent, false
	}
	// Negative condition 4: no SideEffect record and no other effect-bearing
	// or ambiguous fact.
	if sideEffectFactPresent(store, state, runDirectory, events) {
		return abortDeniedSideEffect, false
	}
	// Negative condition 5: no PublicationRecord and no published branch.
	if publicationFactPresent(state, runDirectory) {
		return abortDeniedPublicationPresent, false
	}
	return "", true
}

// attemptTreeProvenAbsent proves the Run's attempt tree either does not
// exist or holds zero entries; any unreadable, linked or populated tree
// fails closed as present.
func attemptTreeProvenAbsent(runDirectory string) bool {
	attemptsRoot := filepath.Join(runDirectory, "attempts")
	info, err := os.Lstat(attemptsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(attemptsRoot)
	if err != nil {
		return false
	}
	return len(entries) == 0
}

// sideEffectFactPresent fails closed on any SideEffect-bearing or ambiguous
// effect fact: execution/verification/review/publication/control authority
// events that cannot predate the first Attempt, control records that
// authorize or record effects (publish approvals, interventions), and their
// transaction artifacts. The Local MVP keeps sandbox allocations in-memory
// and bound to (runId, attemptId); once zero Attempt records are proven
// above, no allocation, lease or session can belong to this Run.
func sideEffectFactPresent(store *runstore.Store, state domain.RunState, runDirectory string, events []domain.RunEvent) bool {
	for _, event := range events {
		if event.AttemptID != "" ||
			strings.HasPrefix(event.Type, "review.") || strings.HasPrefix(event.Type, "publication.") ||
			strings.HasPrefix(event.Type, "control.") || strings.HasPrefix(event.Type, "worker.") ||
			event.Type == "verification.completed" {
			return true
		}
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return true
	}
	records, err := store.ReadControlRecords(state.RunID, validator)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return true
	}
	for _, record := range records {
		if record.Intervention != nil {
			return true
		}
		if record.Approval != nil && record.Approval.Gate == domain.ApprovalGatePublish {
			return true
		}
	}
	for _, name := range []string{"verification-report.json", "artifact-manifest.json", "review-packet.json", "remote-check-record.json"} {
		if evidencePresent(filepath.Join(runDirectory, name)) {
			return true
		}
	}
	for _, directory := range []string{"decisions", "review-packets"} {
		if evidencePresent(filepath.Join(runDirectory, directory)) {
			return true
		}
	}
	return false
}

// publicationFactPresent fails closed on any publication record, publication
// error receipt, archived publication generation or in-snapshot publication
// identity; all of them evidence a remote fact that must be reconciled, not
// aborted away.
func publicationFactPresent(state domain.RunState, runDirectory string) bool {
	if state.Publication != nil {
		return true
	}
	for _, name := range []string{"publication-record.json", "publication-error.json"} {
		if evidencePresent(filepath.Join(runDirectory, name)) {
			return true
		}
	}
	return evidencePresent(filepath.Join(runDirectory, "publications"))
}

// evidencePresent reports whether any filesystem entry exists at path; an
// unreadable or ambiguous entry fails closed as present, so absence is only
// proven by os.ErrNotExist.
func evidencePresent(path string) bool {
	_, err := os.Lstat(path)
	return !errors.Is(err, os.ErrNotExist)
}

func stageAbortResult(runDirectory string, state domain.RunState, actor, reason string, now time.Time, terminalState domain.State, terminalReason string) error {
	if _, err := os.Lstat(filepath.Join(runDirectory, "result.md")); err == nil {
		return errors.New("terminal result.md already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	pending := filepath.Join(runDirectory, "result.md.pending")
	if err := os.Remove(pending); err != nil && !os.IsNotExist(err) {
		return err
	}
	content := fmt.Sprintf("# Run 终止记录\n\n- 任务 ID：%s\n- Run ID：%s\n- 终态：%s\n- 终态原因：%s\n- 操作者：%s\n- 终止原因：%s\n- 生成时间：%s\n", state.TaskID, state.RunID, terminalState, terminalReason, actor, reason, now.UTC().Format(time.RFC3339))
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

func runTaskScaffold(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task scaffold", flag.ContinueOnError)
	flags.SetOutput(stderr)
	draftPath := flags.String("draft", "", "TaskSpec draft 路径")
	preferred := flags.String("preferred-adapter", "", "显式首选 Worker Adapter")
	var fallbacks stringSlice
	flags.Var(&fallbacks, "fallback-adapter", "显式 fallback Worker Adapter（可重复且保持顺序）")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *draftPath == "" || (*preferred == "" && len(fallbacks) > 0) {
		fmt.Fprintln(stderr, "用法：marshal task scaffold --draft PATH|- [--preferred-adapter ID --fallback-adapter ID ...]")
		return ExitUsage
	}
	draft, err := readInput(*draftPath, stdin)
	if err != nil {
		fmt.Fprintln(stderr, "生成失败：无法读取 TaskSpec draft。")
		return ExitFailure
	}
	validator, err := contract.NewValidator()
	if err != nil {
		fmt.Fprintln(stderr, "生成失败：Schema 初始化失败。")
		return ExitFailure
	}
	var override *taskgen.Selection
	if *preferred != "" {
		override = &taskgen.Selection{Preferred: *preferred, Fallback: append([]string(nil), fallbacks...)}
	}
	generated, err := taskgen.Generate(draft, override, validator)
	if err != nil {
		if errors.Is(err, taskgen.ErrOpenCodeIneligible) {
			fmt.Fprintln(stderr, "生成失败：OpenCode 不可用于新 Task。")
			return ExitUnavailable
		}
		fmt.Fprintln(stderr, "生成失败：TaskSpec draft 无法生成合法 TaskSpec。")
		return ExitFailure
	}
	if _, err := stdout.Write(append(generated, '\n')); err != nil {
		fmt.Fprintln(stderr, "生成失败：无法输出 TaskSpec。")
		return ExitFailure
	}
	return ExitOK
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
	runtime, err := newWorkerRuntime(os.Getenv)
	if err != nil {
		fmt.Fprintln(stderr, "规划失败：Worker Runtime 初始化失败。")
		return ExitFailure
	}
	result, err := planning.Plan(ctx, planning.Input{
		StateRoot:         location.StateRoot,
		RepositoryRoot:    location.RepositoryRoot,
		RunID:             *runID,
		TaskSpec:          taskData,
		PolicySnapshot:    policyData,
		Selector:          runtime.Selector(),
		Validator:         runtime.Validator(),
		LocalSelfIdentity: localDogfoodObservation(ctx),
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

func runTaskApprove(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
		LocalSelfIdentity: localDogfoodObservation(ctx),
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
	detach := flags.Bool("detach", false, "以双 fork + setsid 进入独立会话/进程组运行；父进程打印 detached pid 后立即返回")
	logPath := flags.String("log", "", "--detach 的 stdout 日志文件（缺省 .marshal/detached/RUN_ID.log）")
	logErrPath := flags.String("log-err", "", "--detach 的 stderr 日志文件（缺省与 --log 相同）")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal task publish --run RUN_ID [--json] [--detach [--log PATH] [--log-err PATH]]")
		return ExitUsage
	}
	if (*logPath != "" || *logErrPath != "") && !*detach {
		fmt.Fprintln(stderr, "发布失败：--log/--log-err 只能与 --detach 一起使用。")
		return ExitUsage
	}
	if *detach {
		return detachTaskCommand(stdout, stderr, detachRequest{
			RunID: *runID, JSON: *jsonOutput, LogPath: *logPath, LogErrPath: *logErrPath,
			FinalArgs: taskPublishDetachedArgs(*runID, *jsonOutput),
		})
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
	result, err := publication.ObserveChecks(ctx, publication.CheckInput{StateRoot: location.StateRoot, RunID: *runID, Observer: publisher, MergeObserver: publisher, Validator: validator})
	if err != nil {
		fmt.Fprintf(stderr, "CI 验收失败（状态 %s）：%v\n", result.State.State, err)
		if inspected, inspectErr := runstore.New(location.StateRoot).Inspect(*runID); inspectErr == nil && inspected.State == domain.StateBlocked {
			fmt.Fprintf(stderr, "Run 已进入终态 BLOCKED；若 PR 已被合并且 required checks 全绿，可运行补偿命令：marshal task reconcile --run %s --actor ID\n", *runID)
		}
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

func runTaskReconcile(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task reconcile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "Run ID")
	actor := flags.String("actor", "", "reconcile 执行者身份（缺省观察 GitHub 维护者 login）")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal task reconcile --run RUN_ID [--actor ID] [--json]")
		return ExitUsage
	}
	if domain.ValidateID(*runID) != nil {
		fmt.Fprintln(stderr, "reconcile 失败：Run ID 无效。")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil {
		fmt.Fprintf(stderr, "reconcile 失败：%v\n", err)
		return ExitFailure
	}
	if err := location.ValidateIdentity(); err != nil {
		fmt.Fprintf(stderr, "reconcile 失败：%v\n", err)
		return ExitFailure
	}
	publisher, validator, err := publisherFromEnvironment(location)
	if err != nil {
		fmt.Fprintf(stderr, "reconcile 不可用：%v\n", err)
		return ExitUnavailable
	}
	reconciledBy := strings.TrimSpace(*actor)
	if reconciledBy == "" {
		login, actorErr := publisher.ActorLogin(ctx)
		if actorErr != nil {
			fmt.Fprintf(stderr, "reconcile 失败：无法观察维护者身份：%v\n", actorErr)
			return ExitFailure
		}
		reconciledBy = login
	}
	result, err := publication.Reconcile(ctx, publication.ReconcileInput{
		StateRoot: location.StateRoot, RunID: *runID,
		MergeObserver: publisher, CheckObserver: publisher, Validator: validator,
		ReconciledBy: reconciledBy, Now: time.Now().UTC(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "reconcile 失败（状态 %s）：%s\n", result.State.State, redactLocalPaths(err.Error(), location.RepositoryRoot, location.StateRoot))
		return ExitFailure
	}
	if *jsonOutput {
		if err := writeRedactedJSON(stdout, result, location.RepositoryRoot, location.StateRoot); err != nil {
			fmt.Fprintf(stderr, "输出 reconcile 结果失败：%v\n", err)
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "Run：%s\n状态：%s\nReconcile 记录：%s\n", *runID, result.State.State, result.Record.ReconcileID)
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
	throughVerify := flags.Bool("through-verify", false, "Worker 成功转入 VERIFYING 后，在同一调用内继续执行独立 verify")
	recoverDeadDriver := flags.Bool("recover-dead-driver", false, "仅在监督器已证明当前 Worker PID 退出时立即恢复孤儿 Attempt")
	jsonOutput := flags.Bool("json", false, "以 JSON 输出")
	detach := flags.Bool("detach", false, "以双 fork + setsid 进入独立会话/进程组运行；父进程打印 detached pid 后立即返回")
	logPath := flags.String("log", "", "--detach 的 stdout 日志文件（缺省 .marshal/detached/RUN_ID.log）")
	logErrPath := flags.String("log-err", "", "--detach 的 stderr 日志文件（缺省与 --log 相同）")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *runID == "" {
		fmt.Fprintln(stderr, "用法：marshal task run --run RUN_ID [--through-verify] [--json] [--detach [--log PATH] [--log-err PATH]]")
		return ExitUsage
	}
	entryObservation := localDogfoodObservation(ctx)
	if entryObservation != nil && (*detach || *throughVerify || *recoverDeadDriver) {
		fmt.Fprintf(stderr, "运行失败：local dogfood task run 仅允许 foreground Worker；%s。\n", selfidentity.ReasonCommandDenied)
		return ExitUnavailable
	}
	if (*logPath != "" || *logErrPath != "") && !*detach {
		fmt.Fprintln(stderr, "运行失败：--log/--log-err 只能与 --detach 一起使用。")
		return ExitUsage
	}
	if err := domain.ValidateID(*runID); err != nil {
		fmt.Fprintln(stderr, "运行失败：Run ID 无效。")
		return ExitUsage
	}
	if *detach {
		return detachTaskCommand(stdout, stderr, detachRequest{
			RunID: *runID, JSON: *jsonOutput, LogPath: *logPath, LogErrPath: *logErrPath,
			FinalArgs: taskRunDetachedArgs(*runID, *throughVerify, *recoverDeadDriver, *jsonOutput),
		})
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
	var observeLocalSelfIdentity execution.LocalSelfIdentityObserver
	if entryObservation != nil {
		activationPath := os.Getenv(selfidentity.ActivationEnv)
		build := localBuildInfo()
		workingDirectory, workingDirectoryErr := os.Getwd()
		if workingDirectoryErr != nil {
			fmt.Fprintf(stderr, "运行失败：local dogfood identity 无法重新观察；%s。\n", selfidentity.ReasonObjectMismatch)
			return ExitUnavailable
		}
		observeLocalSelfIdentity = func() (selfidentity.LocalSelfIdentityObservationV1, error) {
			return selfidentity.Admit(activationPath, selfidentity.CommandTaskRun, workingDirectory,
				selfidentity.BuildIdentity{SourceHead: build.Commit, SelfProfile: build.SelfProfile}, localNow())
		}
	}
	runtime, err := newWorkerRuntime(os.Getenv)
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
		if err := controlplane.Require(controlplane.ApprovalInput{
			StateRoot: location.StateRoot, RunID: *runID, Gate: domain.ApprovalGatePlan,
			Validator: runtime.Validator(), LocalSelfIdentity: entryObservation,
		}); err != nil {
			fmt.Fprintln(stderr, "运行失败：缺少当前有效的 plan 审批。")
			return ExitFailure
		}
	}
	// This escape hatch is only valid when the durable lease owner record
	// proves that its recorded process has exited. The supervisor supplies it
	// after an independent PID probe; re-checking here prevents callers from
	// bypassing the normal orphan staleness grace window while a live driver
	// still owns the Run.
	orphanStalenessThreshold := time.Duration(0)
	if *recoverDeadDriver {
		ownerAlive, ownerErr := runstore.New(location.StateRoot).LeaseOwnerProcessAlive(*runID)
		if ownerErr != nil || ownerAlive {
			fmt.Fprintln(stderr, "运行失败：无法证明当前 Worker 进程已退出。")
			return ExitFailure
		}
		repositoryHandle, repositoryErr := gitworktree.Open(location.RepositoryRoot)
		if repositoryErr != nil {
			fmt.Fprintln(stderr, "运行失败：无法核验孤儿 Worktree。")
			return ExitFailure
		}
		if repositoryErr = repositoryHandle.UnlockManaged(location.StateRoot, state.WorktreePath); repositoryErr != nil {
			fmt.Fprintf(stderr, "运行失败：无法解除孤儿 Worktree 锁：%v\n", repositoryErr)
			return ExitFailure
		}
		orphanStalenessThreshold = time.Nanosecond
	}
	// Embedded sandbox runtime (M8 vertical slice): strictly opt-in via
	// MARSHAL_EMBEDDED_SANDBOX=1. The default (unset or any other value)
	// keeps the Local MVP behavior of `task run` completely unchanged and no
	// other subcommand is affected. Push/Pull transport, heartbeat, the
	// dispatcher and the durable lease ledger are M9 scope and intentionally
	// not wired here.
	var dispatchBinder execution.DispatchBinder
	if app.EmbeddedSandboxEnabled(os.Getenv) {
		embeddedRuntime, embeddedErr := app.NewEmbeddedSandboxRuntime(location.StateRoot, time.Now)
		if embeddedErr != nil {
			fmt.Fprintln(stderr, "运行失败：embedded sandbox runtime 初始化失败。")
			return ExitFailure
		}
		dispatchBinder = embeddedRuntime
	}
	// I186-R5 strangler cutover：新路径默认启用。Worker 经 sandboxbridge 在
	// 绑定 allocation/lease 身份的执行链中运行（Provision→Stage→Adapter.Run
	// →Inspect→Terminate）；`MARSHAL_WORKER_EXECUTOR=legacy` 显式回到
	// legacy `Adapter.Run(host)` compatibility profile（ADR 0043 决策 7 的
	// explicit local-nonproduction）。rollback 即设置该环境变量为 legacy，
	// 只涉 gate 方向，无状态迁移；两条路径的 journal/verification/review/
	// publication 语义经端到端等价测试证明相同。
	var workerRunner func(ctx context.Context, adapter port.WorkerAdapter, request domain.Record) (domain.Record, error)
	if os.Getenv("MARSHAL_WORKER_EXECUTOR") != "legacy" {
		// worker executor 实例的 per-op 上限：真实 Agent attempt 预算为
		// attemptTimeoutSeconds（分钟级），runner 默认 30s cap 会立刻 kill；
		// worker 承载路径提升到 4h（Envelope §4 按 min(requested, cap) 生效，
		// 不放宽任何 attempt 预算）。
		embeddedRuntime, embeddedErr := app.NewEmbeddedSandboxRuntime(location.StateRoot, time.Now, app.WithLocalRunnerOptions(local.WithExecTimeout(4*time.Hour)))
		if embeddedErr != nil {
			fmt.Fprintln(stderr, "运行失败：sandbox executor runtime 初始化失败。")
			return ExitFailure
		}
		bridge, bridgeErr := sandboxbridge.NewBridge(embeddedRuntime.Provider())
		if bridgeErr != nil {
			fmt.Fprintln(stderr, "运行失败：sandbox executor bridge 初始化失败。")
			return ExitFailure
		}
		// ADR 0052 §1.2 + ADR 0055：当 provider 是 Local runner 时注入
		// staged transcript artifact 回读面，使实现 LaunchCapable 的 Adapter
		// （当前为 pi）在 allocation 中被承载执行。
		if runner, ok := embeddedRuntime.Provider().(*local.LocalRunner); ok {
			bridge.WithTranscriptSource(func(allocationID, artifactID string) ([]byte, error) {
				dir, err := runner.AllocationDirectory(allocationID)
				if err != nil {
					return nil, err
				}
				return os.ReadFile(filepath.Join(dir, filepath.FromSlash(artifactID)))
			})
		}
		workerRunner = bridge.RunWorker
	}
	result, err := execution.Run(ctx, execution.Input{
		StateRoot: location.StateRoot, RepositoryRoot: location.RepositoryRoot, RunID: *runID,
		Adapter: worker, Validator: runtime.Validator(), WorkerRunner: workerRunner, DispatchBinder: dispatchBinder,
		OrphanStalenessThreshold: orphanStalenessThreshold,
		EntryLocalSelfIdentity:   entryObservation, ObserveLocalSelfIdentity: observeLocalSelfIdentity,
	})
	if err != nil {
		fmt.Fprintf(stderr, "运行失败（Attempt %s，状态 %s）：%v\n", result.AttemptID, result.State.State, err)
		return ExitFailure
	}
	if *throughVerify && result.State.State == domain.StateVerifying {
		return runThroughVerify(ctx, location.StateRoot, *runID, *jsonOutput, result, stdout, stderr)
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

func runThroughVerify(ctx context.Context, stateRoot, runID string, jsonOutput bool, result execution.Result, stdout, stderr io.Writer) int {
	var verifyOutput bytes.Buffer
	verifyExit := executeVerify(ctx, runID, jsonOutput, &verifyOutput, stderr)
	if refreshed, err := runstore.New(stateRoot).Inspect(runID); err == nil {
		result.State = refreshed
	}
	if jsonOutput {
		combined := struct {
			execution.Result
			Verification json.RawMessage `json:"verification,omitempty"`
		}{result, bytes.TrimSpace(verifyOutput.Bytes())}
		if err := writeJSON(stdout, combined); err != nil {
			fmt.Fprintf(stderr, "输出运行结果失败：%v\n", err)
			return ExitFailure
		}
		return verifyExit
	}
	fmt.Fprintf(stdout, "Run：%s\nAttempt：%s\n状态：%s\n", runID, result.AttemptID, result.State.State)
	if _, err := io.Copy(stdout, &verifyOutput); err != nil {
		fmt.Fprintf(stderr, "输出验证结果失败：%v\n", err)
		return ExitFailure
	}
	return verifyExit
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
	return executeVerify(ctx, *runID, *jsonOutput, stdout, stderr)
}

func executeVerify(ctx context.Context, runID string, jsonOutput bool, stdout, stderr io.Writer) int {
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
	lease, err := store.Acquire(runID)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	defer lease.Release()
	state, err := runstore.InspectUnderLease(lease)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	if state.State != domain.StateVerifying {
		fmt.Fprintf(stderr, "验证失败：Run 状态为 %s，要求 VERIFYING。\n", state.State)
		return ExitFailure
	}
	runDirectory := filepath.Join(location.StateRoot, "runs", runID)
	// ADR 0027 candidate mode is switched on by the Attempt identity bound to
	// the Run state; the lifecycle reducer always binds it before VERIFYING.
	// Legacy snapshots that predate the binding keep the pre-ADR-0027 path
	// (§5 legacy fallback): verification stays available and simply admits no
	// Candidate records instead of inventing an identity.
	attemptID := state.CurrentAttemptID
	authorityNamespaceID := ""
	if attemptID != "" {
		if err := domain.ValidateID(attemptID); err != nil {
			fmt.Fprintf(stderr, "验证失败：Run 当前 Attempt 身份非法：%v\n", err)
			return ExitFailure
		}
		derived, deriveErr := verifyAuthorityNamespaceID(location.RepositoryRoot)
		if deriveErr != nil {
			fmt.Fprintf(stderr, "验证失败：推导权威键空间：%v\n", deriveErr)
			return ExitFailure
		}
		authorityNamespaceID = derived
	}
	taskData, err := runstore.ReadFileUnderLease(lease, 2<<20, "task-spec.json")
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
	validator, err := contract.NewValidator()
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
		return ExitFailure
	}
	localVerificationInput, err := prepareLocalVerificationBinding(ctx, lease, state, localDogfoodObservation(ctx), validator)
	if err != nil {
		fmt.Fprintf(stderr, "验证失败：%v\n", err)
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
	result, err := verification.New().Verify(verificationContext, verification.Input{TaskID: state.TaskID, RunID: state.RunID, AttemptID: attemptID, AuthorityNamespaceID: authorityNamespaceID, SpecDigest: state.SpecDigest, BaseSHA: state.BaseSHA, Worktree: state.WorktreePath, ExpectedCommonDir: repositoryIdentity.CommonDir, RunDirectory: runDirectory, Scope: scope, Deliverables: deliverables, Commands: commands, BaselinePath: baselinePath, PatchCaptureBytes: patchCaptureLimit(scope.MaxDiffBytes), LocalSelfIdentity: localVerificationInput})
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
	eventPayload := map[string]any{"reportDigest": reportDigest, "artifactManifestDigest": manifestDigest, "status": result.Report.Status}
	if result.Report.LocalSelfIdentityBinding != nil {
		bindingDigest, digestErr := selfidentity.DigestVerificationBinding(*result.Report.LocalSelfIdentityBinding)
		if digestErr != nil {
			fmt.Fprintf(stderr, "验证失败：%v\n", localPhaseRejected())
			return ExitFailure
		}
		eventPayload["localSelfIdentityBindingDigest"] = bindingDigest
	}
	event := domain.RunEvent{APIVersion: domain.APIVersionV1Alpha1, Kind: domain.KindRunEvent, EventID: eventID, RunID: state.RunID, Sequence: state.Sequence + 1, Type: "verification.completed", StateFrom: state.State, StateTo: domain.StateReviewPending, Timestamp: result.Report.CompletedAt, Actor: &domain.Actor{Type: "system", ID: "marshal-verifier"}, Payload: eventPayload}
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
	if jsonOutput {
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

// verifyAuthorityNamespaceID derives the frozen local authority key space
// that owns the ADR 0027 Candidate records, identically to ADR 0026:
// tenantNamespace=local, controlPlaneId=default, authorityScopeId=repository
// identity. location.RepositoryRoot equals the RepositoryRoot bound into
// repo.json (ValidateIdentity enforces the binding), so every capture site
// derives the identical digest.
func verifyAuthorityNamespaceID(repositoryRoot string) (string, error) {
	namespace := authority.AuthorityNamespaceId{TenantNamespace: "local", ControlPlaneId: "default", AuthorityScopeId: repositoryRoot}
	return namespace.Digest()
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
	state, err := runstore.InspectUnderLease(lease)
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	if state.State != domain.StateReviewPending {
		fmt.Fprintf(stderr, "审查失败：Run 状态为 %s，要求 REVIEW_PENDING。\n", state.State)
		return ExitFailure
	}
	runDirectory := filepath.Join(location.StateRoot, "runs", *runID)
	taskData, err := runstore.ReadFileUnderLease(lease, 2<<20, "task-spec.json")
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
	verificationData, err := runstore.ReadFileUnderLease(lease, 8<<20, "verification-report.json")
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
	manifestData, err := runstore.ReadFileUnderLease(lease, 8<<20, "artifact-manifest.json")
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
	frozenReportDigest, frozenManifestDigest, err := frozenVerificationDigests(lease)
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
	validator, err := contract.NewValidator()
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
		return ExitFailure
	}
	localReviewBinding, err := prepareLocalReviewBinding(ctx, lease, state, localDogfoodObservation(ctx), validator, report, manifest, *decisionPath == "")
	if err != nil {
		fmt.Fprintf(stderr, "审查失败：%v\n", err)
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
	if *decisionPath == "" {
		builder := review.PacketBuilder{RunDirectory: runDirectory, Validator: validator}
		packet, packetDigest, err := builder.Build(review.PacketBuildInput{Task: task, TaskData: taskData, Report: report, ReportData: verificationData, Manifest: manifest, ManifestData: manifestData, TaskID: state.TaskID, RunID: state.RunID, SpecDigest: state.SpecDigest, BaseSHA: state.BaseSHA, ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed, LocalSelfIdentityBinding: localReviewBinding})
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
	result, err := importer.Import(review.DecisionInput{Path: *decisionPath, Task: task, TaskID: state.TaskID, RunID: state.RunID, SpecDigest: state.SpecDigest, ReviewRound: state.ReviewRound, AttemptsUsed: state.AttemptsUsed, ReworkRoundsUsed: state.ReworkRoundsUsed, Report: report, Manifest: manifest, LocalSelfIdentityBinding: localReviewBinding})
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
	if result.Decision.LocalSelfIdentityBindingDigest != "" {
		payload["localSelfIdentityBindingDigest"] = result.Decision.LocalSelfIdentityBindingDigest
	}
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

func frozenVerificationDigests(lease *runstore.Lease) (string, string, error) {
	events, _, err := runstore.ReadEventsUnderLease(lease)
	if err != nil {
		return "", "", fmt.Errorf("读取验证事件：%w", err)
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != "verification.completed" {
			continue
		}
		// Authority binding: only the exact verifier producer may freeze the
		// verification evidence digests; an omitted or forged actor fails
		// closed instead of authorizing review.
		if event.Actor == nil || event.Actor.Type != "system" || event.Actor.ID != "marshal-verifier" {
			return "", "", errors.New("verification.completed 事件必须由 system/marshal-verifier 记录")
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

func runTaskStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
	observation := localDogfoodObservation(ctx)
	if observation != nil {
		validator, validatorErr := contract.NewValidator()
		if validatorErr != nil || validateFrozenLocalDogfoodBinding(location.StateRoot, *runID, validator, observation) != nil {
			fmt.Fprintln(stderr, "读取状态失败：本地 Run 身份绑定无效。")
			return ExitFailure
		}
	}
	if *jsonOutput {
		var output any = state
		if observation != nil {
			output = struct {
				domain.RunState
				SelfIdentity *selfidentity.LocalSelfIdentityObservationV1 `json:"selfIdentity"`
				Assurance    string                                       `json:"assurance"`
				Execution    string                                       `json:"execution"`
				Production   bool                                         `json:"production"`
				Publication  string                                       `json:"publication"`
				CurrentMatch bool                                         `json:"currentMatch"`
			}{state, observation, "ordinary-user", "workspace-write", false, "none", true}
		}
		if err := writeJSON(stdout, output); err != nil {
			fmt.Fprintf(stderr, "输出状态失败：%v\n", err)
			return ExitFailure
		}
	} else {
		fmt.Fprintf(stdout, "Run：%s\n状态：%s\nSequence：%d\n更新时间：%s\n", state.RunID, state.State, state.Sequence, state.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
		if observation != nil {
			fmt.Fprintln(stdout, "适用性：ordinary-user / workspace-write / non-production / publication:none / current-match")
		}
	}
	return ExitOK
}

func validateFrozenLocalDogfoodBinding(stateRoot, runID string, validator *contract.Validator, observation *selfidentity.LocalSelfIdentityObservationV1) error {
	if err := domain.ValidateID(runID); err != nil {
		return err
	}
	state, err := runstore.New(stateRoot).Inspect(runID)
	if err != nil {
		return err
	}
	policyPath := filepath.Join(stateRoot, "runs", runID, "policy-snapshot.json")
	policyData, err := os.ReadFile(policyPath)
	if err != nil || int64(len(policyData)) > maxContractInputBytes {
		return errors.New("frozen local policy unavailable")
	}
	canonicalPolicy, err := canonical.JSON(policyData)
	if err != nil || canonical.DigestBytes(canonicalPolicy) != state.PolicyDigest {
		return errors.New("frozen local policy digest mismatch")
	}
	return planning.ValidateLocalDogfoodEnvironmentBinding(policyData, validator, observation)
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

// redactLocalPaths replaces local repository/state roots with a fixed token
// so reconcile diagnostics and output never carry absolute local paths.
func redactLocalPaths(document string, roots ...string) string {
	for _, root := range roots {
		if root == "" {
			continue
		}
		document = strings.ReplaceAll(document, root, "<local-path>")
	}
	return document
}

// writeRedactedJSON encodes value and redacts the local repository/state
// roots before writing: RunState carries local-only provenance such as the
// worktree path, which must never surface in command output.
func writeRedactedJSON(output io.Writer, value any, roots ...string) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return errors.New("encode JSON: " + err.Error())
	}
	if _, err := fmt.Fprintln(output, redactLocalPaths(string(data), roots...)); err != nil {
		return err
	}
	return nil
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, `Marshal：证据门禁式 Coding Agent 编排器

用法：
  marshal version [--json]
  marshal doctor [--run RUN_ID] [--repair] [--print-env] [--json]
  marshal doctor --self [--repository-root PATH] [--activation-id ID] [--valid-for DURATION]
  marshal init [--json]
  marshal supervise [--once] [--interval DURATION] [--marshal-binary PATH] [--revive-retry-pending] [--json]
  marshal contract validate [--schema NAME] <PATH|->
  marshal contract schema [--all [--out DIR]] [--schema NAME] [--json]
  marshal task scaffold --draft PATH|- [--preferred-adapter ID --fallback-adapter ID ...]
  marshal task plan --task PATH --policy PATH --run RUN_ID [--json]
  marshal task approve --run RUN_ID --gate plan|publish [--actor ID] [--json]
  marshal task status --run RUN_ID [--json]
  marshal task run --run RUN_ID [--through-verify] [--json] [--detach [--log PATH] [--log-err PATH]]
  marshal task verify --run RUN_ID [--json]
  marshal task review --run RUN_ID [--decision PATH] [--json]
  marshal task publish --run RUN_ID [--json] [--detach [--log PATH] [--log-err PATH]]
  marshal task accept --run RUN_ID [--json]
  marshal task reconcile --run RUN_ID [--actor ID] [--json]
  marshal task <COMMAND>

OpenCode、Qwen Code 与 Pi Worker 只产生 Attempt 与真实快照；verify、review、publish 与 accept 是彼此独立的证据门禁。发布命令还要求 absolute MARSHAL_GH_PATH 与 MARSHAL_GH_CONFIG_DIR。

task reconcile 是 ADR 0026 的 accept-after-merge 补偿命令：仅当 Run 处于发布后的终态 BLOCKED、PR 已被合并且 required checks 全绿时，才将其安全迁移到 ACCEPTED。`)
}

// runServe starts the read-only dashboard (experimental). It exposes no control
// endpoints; approve/publish remain in CLI/Skill. Binds loopback by default.
// runTaskMigrateOutcomes reconstructs terminal Outcomes for legacy terminal Runs
// that predate outcome-writing, so cleanup can then proceed. It never overwrites
// an existing valid outcome (RecordLegacyOutcome refuses).
func runTaskMigrateOutcomes(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("task migrate-outcomes", flag.ContinueOnError)
	flags.SetOutput(stderr)
	actor := flags.String("actor", "", "操作者 ID")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || strings.TrimSpace(*actor) == "" {
		fmt.Fprintln(stderr, "用法：marshal task migrate-outcomes --actor ID")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil || location.ValidateIdentity() != nil {
		fmt.Fprintln(stderr, "迁移失败：无法验证仓库身份。")
		return ExitFailure
	}
	validator, err := contract.NewValidator()
	if err != nil {
		return ExitFailure
	}
	runsDir := filepath.Join(location.StateRoot, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		fmt.Fprintf(stderr, "迁移失败：%v\n", err)
		return ExitFailure
	}
	migrated := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := cleanupservice.RecordLegacyOutcome(ctx, cleanupservice.Input{
			StateRoot: location.StateRoot, RepositoryRoot: location.RepositoryRoot,
			RunID: entry.Name(), Actor: strings.TrimSpace(*actor), Now: time.Now().UTC(), Validator: validator,
		}); err == nil {
			migrated++
		}
	}
	fmt.Fprintf(stdout, "已为 %d 个遗留终态 Run 补记 Outcome。\n", migrated)
	return ExitOK
}

// stringSlice is a repeatable --root flag value.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func runServe(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	addr := flags.String("addr", "127.0.0.1:7717", "监听地址（默认仅 loopback）")
	var roots stringSlice
	flags.Var(&roots, "root", "额外聚合的仓库状态根（可重复，用于 Workspace 分组）")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "用法：marshal serve [--addr HOST:PORT] [--root REPO ...]")
		return ExitUsage
	}
	location, err := repository.Discover(".")
	if err != nil || location.ValidateIdentity() != nil {
		fmt.Fprintln(stderr, "serve 失败：无法验证仓库身份。")
		return ExitFailure
	}
	if host, _, err := net.SplitHostPort(*addr); err == nil && host != "" && host != "127.0.0.1" && host != "localhost" && host != "::1" {
		fmt.Fprintf(stderr, "警告：监听非 loopback 地址 %s；dashboard 为只读但请自行加认证/反向代理。\n", *addr)
	}
	fmt.Fprintf(stdout, "Marshal dashboard (read-only) listening on %s\n", *addr)
	if err := dashboard.Serve(dashboard.Options{StateRoot: location.StateRoot, Addr: *addr, Roots: roots}); err != nil {
		fmt.Fprintf(stderr, "serve 失败：%v\n", err)
		return ExitFailure
	}
	return ExitOK
}
