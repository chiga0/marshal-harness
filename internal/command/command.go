package command

import (
	"fmt"
	"strings"

	"github.com/chiga0/marshal-harness/internal/engine"
)

// digestPrefix 与 internal/engine 一致的 sha256 摘要前缀。
const digestPrefix = "sha256:"

// ApplicationCommandKind 是 ApplicationCommand 的封闭枚举。匹配大小写敏感，
// 未知值一律 fail closed。
type ApplicationCommandKind string

// 封闭 ApplicationCommand kind 值。
const (
	// ApplicationCommandKindAttemptStart 投影为 engine.CommandKindDispatch，
	// 用于启动一次 attempt 执行。
	ApplicationCommandKindAttemptStart ApplicationCommandKind = "attempt.start"
)

// Validate 拒绝封闭枚举之外的任何值。
func (kind ApplicationCommandKind) Validate() error {
	switch kind {
	case ApplicationCommandKindAttemptStart:
		return nil
	default:
		return fmt.Errorf("command: unknown application command kind %q", string(kind))
	}
}

// kindMapping 是 ApplicationCommandKind → engine.CommandKind 的封闭映射表。
// 新增映射必须在此表中显式登记，未登记 kind 一律 fail closed。
var kindMapping = map[ApplicationCommandKind]engine.CommandKind{
	ApplicationCommandKindAttemptStart: engine.CommandKindDispatch,
}

// ApplicationCommand 是不可变的应用命令请求对象：携带封闭 kind 枚举、
// sha256 摘要形式的 requestDigest 与正整数 expectedSequence。
type ApplicationCommand struct {
	Kind             ApplicationCommandKind `json:"kind"`
	RequestDigest    string                 `json:"requestDigest"`
	ExpectedSequence int64                  `json:"expectedSequence"`
}

// Validate 对全部字段 fail closed 校验：
//   - kind 必须属于封闭枚举
//   - requestDigest 必须携带 sha256: 前缀且为 64 位小写十六进制
//   - expectedSequence 必须 >= 1
func (cmd ApplicationCommand) Validate() error {
	if err := cmd.Kind.Validate(); err != nil {
		return err
	}
	if err := requireDigest("command.requestDigest", cmd.RequestDigest); err != nil {
		return err
	}
	if cmd.ExpectedSequence < 1 {
		return fmt.Errorf("command: expectedSequence must be a positive integer")
	}
	return nil
}

// DeriveDurableCommand 把一条已提交 authority ledger fact 与一条
// ApplicationCommand 投影为 engine.DurableExecutionEngine 的派生命令。
//
// 同一 fact + 同一 kind 重复调用幂等得到同一 commandId（委托
// engine.DeriveCommand）；畸形输入、未知 kind、空 request digest、
// expectedSequence 非正一律 fail closed。
func DeriveDurableCommand(eng *engine.DurableExecutionEngine, fact engine.LedgerFact, appCommand ApplicationCommand) (engine.Command, error) {
	if eng == nil {
		return engine.Command{}, fmt.Errorf("command: engine must not be nil")
	}
	if err := appCommand.Validate(); err != nil {
		return engine.Command{}, err
	}
	engineKind, ok := kindMapping[appCommand.Kind]
	if !ok {
		return engine.Command{}, fmt.Errorf("command: no engine kind mapping for application command kind %q", string(appCommand.Kind))
	}
	if fact.Sequence != appCommand.ExpectedSequence {
		return engine.Command{}, fmt.Errorf("command: expectedSequence %d does not match ledger fact sequence %d", appCommand.ExpectedSequence, fact.Sequence)
	}
	return eng.DeriveCommand(fact, engineKind)
}

// requireDigest 与 internal/engine 一致的校验语义：必须携带 sha256: 前缀，
// 后接 64 位小写十六进制字符。
func requireDigest(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("command: %s must be a non-empty string", field)
	}
	if !strings.HasPrefix(value, digestPrefix) {
		return fmt.Errorf("command: %s must carry the %s digest prefix", field, digestPrefix)
	}
	hexPart := strings.TrimPrefix(value, digestPrefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("command: %s must be a 64 character sha256 hex digest", field)
	}
	for _, r := range hexPart {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("command: %s must be lowercase hex", field)
		}
	}
	return nil
}
