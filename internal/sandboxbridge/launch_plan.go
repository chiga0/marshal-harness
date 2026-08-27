package sandboxbridge

import (
	"path/filepath"
	"strings"
)

// LaunchPlan 是 provider-neutral 启动计划接缝（ADR 0052 §1.2 + ADR 0055）：
// 任何实现 LaunchCapable 的 Adapter 通过 PrepareLaunch 返回本接口，
// sandboxbridge 只消费这些字段，不直接依赖任何特定 Adapter 的类型。
//
// 这使得 pi、opencode 或未来其它 Adapter 的 launch plan 类型只需实现
// 本接口即可接入 allocation-carried 执行链，无需 sandboxbridge import 它们。
type LaunchPlan interface {
	// Argv 返回可执行文件路径 + 参数（argv[0] 绝对路径）。
	Argv() []string
	// EnvBlock 返回完整环境块（"K=V" 列表）。
	EnvBlock() []string
	// WorkDir 返回解析后的 worktree 绝对路径。
	WorkDir() string
	// TimeoutSeconds 返回 attempt 级超时秒数。
	TimeoutSeconds() int64
	// ResultFilePath 返回 ControlRoot 下的 worker-result 绝对路径。
	ResultFilePath() string
	// ControlRootPath 返回绝对 control root。
	ControlRootPath() string
	// SessionPolicyName 返回 session policy 名称。
	SessionPolicyName() string
	// MaxOutput 返回最大输出字节数。
	MaxOutput() int64
	// ProviderVersion 返回 adapter provider 的二进制版本（审计字段）。
	ProviderVersion() string
}

// ValidateLaunchPlan 复核 PrepareLaunch 的输出与冻结请求一致（不放行
// 跨 attempt 复用的 plan）。provider-neutral，不依赖任何 Adapter 类型。
func ValidateLaunchPlan(plan LaunchPlan) error {
	if plan == nil {
		return errLaunchPlanNil
	}
	argv := plan.Argv()
	if len(argv) == 0 {
		return errLaunchPlanEmptyArgv
	}
	if plan.WorkDir() == "" || !filepath.IsAbs(plan.WorkDir()) {
		return errLaunchPlanBadWorkDir
	}
	if plan.TimeoutSeconds() <= 0 {
		return errLaunchPlanBadTimeout
	}
	if plan.MaxOutput() <= 0 {
		return errLaunchPlanBadMaxOutput
	}
	if strings.TrimSpace(plan.ControlRootPath()) == "" || !filepath.IsAbs(plan.ControlRootPath()) {
		return errLaunchPlanBadControlRoot
	}
	return nil
}

var (
	errLaunchPlanNil            = strErr("sandboxbridge: nil launch plan")
	errLaunchPlanEmptyArgv      = strErr("sandboxbridge: launch plan has empty argv")
	errLaunchPlanBadWorkDir     = strErr("sandboxbridge: launch plan working directory is not absolute")
	errLaunchPlanBadTimeout     = strErr("sandboxbridge: launch plan timeout must be positive")
	errLaunchPlanBadMaxOutput   = strErr("sandboxbridge: launch plan max output bytes must be positive")
	errLaunchPlanBadControlRoot = strErr("sandboxbridge: launch plan control root is not absolute")
)

type strErr string

func (e strErr) Error() string { return string(e) }
