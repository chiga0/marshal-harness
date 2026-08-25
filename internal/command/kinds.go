package command

import (
	"fmt"
	"time"
)

// 封闭 ApplicationCommand kind 值（与 internal/engine 的四个 CommandKind 对齐）。
const (
	// ApplicationCommandKindAttemptCancel 投影为 engine.CommandKindSignal，
	// 用于取消一次 attempt 执行。
	ApplicationCommandKindAttemptCancel ApplicationCommandKind = "attempt.cancel"

	// ApplicationCommandKindWatchdogTick 投影为 engine.CommandKindTimer，
	// 用于驱动 watchdog 定时器触发。
	ApplicationCommandKindWatchdogTick ApplicationCommandKind = "watchdog.tick"

	// ApplicationCommandKindPublicationIntent 投影为 engine.CommandKindSideEffect，
	// 用于声明一次 publication intent。
	ApplicationCommandKindPublicationIntent ApplicationCommandKind = "publication.intent"
)

// SignalReason 是 signal 类 ApplicationCommand 的封闭原因枚举。
type SignalReason string

// 封闭 SignalReason 值。
const (
	// SignalReasonAttemptCancel 标记一次 attempt 取消信号。
	SignalReasonAttemptCancel SignalReason = "attempt.cancel"
)

// Validate 拒绝封闭枚举之外的任何值。
func (r SignalReason) Validate() error {
	switch r {
	case SignalReasonAttemptCancel:
		return nil
	default:
		return fmt.Errorf("command: unknown signal reason %q", string(r))
	}
}

// validateKindPayload 按 kind 分支校验封闭的 kind 级 payload 字段：
//   - attempt.start：三个 kind 级字段必须全为零值
//   - attempt.cancel：SignalReason 必填且封闭，其余 kind 级字段必须为零值
//   - watchdog.tick：TimerFireAt 必填且 RFC3339 可解析，其余 kind 级字段必须为零值
//   - publication.intent：SideEffectIntentDigest 必填且 sha256 合法，其余 kind 级字段必须为零值
//
// 错误 kind/字段组合一律 fail closed。
func validateKindPayload(cmd ApplicationCommand) error {
	switch cmd.Kind {
	case ApplicationCommandKindAttemptStart:
		if cmd.SignalReason != "" || cmd.TimerFireAt != "" || cmd.SideEffectIntentDigest != "" {
			return fmt.Errorf("command: kind %q must not carry kind-specific payload fields", string(cmd.Kind))
		}
		return nil
	case ApplicationCommandKindAttemptCancel:
		if cmd.TimerFireAt != "" || cmd.SideEffectIntentDigest != "" {
			return fmt.Errorf("command: kind %q must not carry timer or side-effect fields", string(cmd.Kind))
		}
		if cmd.SignalReason == "" {
			return fmt.Errorf("command: signalReason must be non-empty for kind %q", string(cmd.Kind))
		}
		return cmd.SignalReason.Validate()
	case ApplicationCommandKindWatchdogTick:
		if cmd.SignalReason != "" || cmd.SideEffectIntentDigest != "" {
			return fmt.Errorf("command: kind %q must not carry signal or side-effect fields", string(cmd.Kind))
		}
		if cmd.TimerFireAt == "" {
			return fmt.Errorf("command: timerFireAt must be non-empty for kind %q", string(cmd.Kind))
		}
		if _, err := time.Parse(time.RFC3339, cmd.TimerFireAt); err != nil {
			return fmt.Errorf("command: timerFireAt must be RFC3339-parseable for kind %q", string(cmd.Kind))
		}
		return nil
	case ApplicationCommandKindPublicationIntent:
		if cmd.SignalReason != "" || cmd.TimerFireAt != "" {
			return fmt.Errorf("command: kind %q must not carry signal or timer fields", string(cmd.Kind))
		}
		return requireDigest("command.sideEffectIntentDigest", cmd.SideEffectIntentDigest)
	default:
		return nil
	}
}
