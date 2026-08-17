package qwen

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/chiga0/marshal-harness/internal/port"
)

// typedFailure 把封闭 port.AdapterFailure 与固定词汇细节绑定在一起。
// protocol-invalid 同时保留 qwen 包的 ErrProtocol 身份，既有 errors.Is
// 检查继续成立；typed 身份（adapter/kind/disposition）通过 errors.As
// 沿错误链可读。Error() 只包含封闭枚举、已验证 hint 与固定词汇细节，
// 任何 provider 自由文本都无法进入。
type typedFailure struct {
	failure port.AdapterFailure
	detail  string
}

func newQwenFailure(kind port.FailureKind, detail string, retryAfter *time.Duration, notBefore *time.Time, now time.Time) *typedFailure {
	disposition, _ := port.DispositionFor(kind)
	failure, err := port.NewAdapterFailure(port.AdapterIDQwen, kind, disposition, retryAfter, notBefore, now)
	if err != nil {
		// hint 验证失败时降级为无 hint 的同一分类；分类本身不允许丢失。
		failure, _ = port.NewAdapterFailure(port.AdapterIDQwen, kind, disposition, nil, nil, now)
	}
	return &typedFailure{failure: failure, detail: detail}
}

func qwenProtocolInvalid(detail string, now time.Time) *typedFailure {
	return newQwenFailure(port.FailureKindProtocolInvalid, detail, nil, nil, now)
}

func (e *typedFailure) Error() string {
	if e.detail == "" {
		return e.failure.Error()
	}
	return e.failure.Error() + ": " + e.detail
}

func (e *typedFailure) Unwrap() []error {
	if e.failure.Kind == port.FailureKindProtocolInvalid {
		return []error{e.failure, ErrProtocol}
	}
	return []error{e.failure}
}

// signalCategory 是结构化终止信号的封闭类别。
type signalCategory string

const (
	signalCategoryQuota      signalCategory = "quota"
	signalCategoryRateLimit  signalCategory = "rate-limit"
	signalCategoryDNS        signalCategory = "dns"
	signalCategoryConnection signalCategory = "connection"
)

// signalCategoryKinds 冻结类别到失败分类的唯一映射。
var signalCategoryKinds = map[signalCategory]port.FailureKind{
	signalCategoryQuota:      port.FailureKindQuotaExhausted,
	signalCategoryRateLimit:  port.FailureKindRateLimited,
	signalCategoryDNS:        port.FailureKindDNSFailure,
	signalCategoryConnection: port.FailureKindConnectionFailure,
}

// terminalCodeSignals / terminalTypeSignals / terminalStatusSignals 是封闭
// 分类表：分类只接受这些结构化 code/type/status 字面量，绝不使用 message
// substring。表外字面量是未知信号，而不是可猜测的自由文本。
var terminalCodeSignals = map[string]signalCategory{
	"ResourceExhausted":    signalCategoryQuota,
	"QuotaExceeded":        signalCategoryQuota,
	"RateLimited":          signalCategoryRateLimit,
	"Throttling.RateQuota": signalCategoryRateLimit,
	"ENOTFOUND":            signalCategoryDNS,
	"EAI_AGAIN":            signalCategoryDNS,
	"ECONNREFUSED":         signalCategoryConnection,
	"ECONNRESET":           signalCategoryConnection,
	"ETIMEDOUT":            signalCategoryConnection,
}

var terminalTypeSignals = map[string]signalCategory{
	"quota_exhausted":    signalCategoryQuota,
	"rate_limited":       signalCategoryRateLimit,
	"dns_failure":        signalCategoryDNS,
	"connection_failure": signalCategoryConnection,
}

var terminalStatusSignals = map[int64]signalCategory{
	429: signalCategoryRateLimit,
}

// maxRetryHintSeconds 是 retry_after 整数字面量的固定上界（24h 秒数）。
const maxRetryHintSeconds = int64(port.MaxRetryHintWindow / time.Second)

// signalRecord 是一个信号槽的独立记录：presence、known/unknown 与类别必须
// 先分别记录，再进入合并，任何已知 code/type 或 429 都不得事后救援未知信号。
type signalRecord struct {
	present  bool
	known    bool
	category signalCategory
}

// mergeTerminalSignals 合并独立记录的信号槽。返回分类与固定词汇细节；
// violated 为真时细节描述冲突原因，调用方必须归为 protocol-invalid。
func mergeTerminalSignals(records []signalRecord) (kind port.FailureKind, detail string, violated bool) {
	present, unknown := 0, 0
	var category signalCategory
	for _, record := range records {
		if !record.present {
			continue
		}
		present++
		if !record.known {
			unknown++
			continue
		}
		if category == "" {
			category = record.category
		} else if category != record.category {
			return "", "terminal signals disagree on failure category", true
		}
	}
	if present == 0 {
		return port.FailureKindProviderTerminal, "", false
	}
	if unknown > 0 {
		if present == 1 {
			return port.FailureKindProviderTerminal, "terminal signal is outside the closed table", false
		}
		return "", "unknown terminal signal coexists with other signals", true
	}
	return signalCategoryKinds[category], "", false
}

// parseEventMap 以保留数值字面量的方式解析 JSONL 行，供严格字面量校验使用。
func parseEventMap(line []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	var event map[string]any
	if err := decoder.Decode(&event); err != nil {
		return nil, err
	}
	if event == nil {
		return nil, errEventNotObject
	}
	return event, nil
}

var errEventNotObject = errors.New("event is not a JSON object")

// explicitNullViolation 的细节固定为字段名，不回显任何 provider 值。
func explicitNullViolation(field string, now time.Time) *typedFailure {
	return qwenProtocolInvalid("terminal field "+field+" is explicit null", now)
}

// stringSignalField 读取封闭表内的字符串信号字段。显式 null 区别于
// missing，归 protocol-invalid；错误类型同样 protocol-invalid。
func stringSignalField(object map[string]any, field string, table map[string]signalCategory, now time.Time) (signalRecord, *typedFailure) {
	value, present := object[field]
	if !present {
		return signalRecord{}, nil
	}
	if value == nil {
		return signalRecord{}, explicitNullViolation(field, now)
	}
	text, ok := value.(string)
	if !ok {
		return signalRecord{}, qwenProtocolInvalid("terminal field "+field+" must be a string", now)
	}
	category, known := table[text]
	return signalRecord{present: true, known: known, category: category}, nil
}

// statusSignalField 只接受精确 JSON 整数字面量；字符串、小数、指数、bool、
// array、object、null、溢出一律 fail closed。
func statusSignalField(object map[string]any, field string, now time.Time) (signalRecord, *typedFailure) {
	value, present := object[field]
	if !present {
		return signalRecord{}, nil
	}
	if value == nil {
		return signalRecord{}, explicitNullViolation(field, now)
	}
	number, ok := value.(json.Number)
	if !ok {
		return signalRecord{}, qwenProtocolInvalid("terminal field "+field+" must be an integer literal", now)
	}
	parsed, err := number.Int64()
	if err != nil {
		return signalRecord{}, qwenProtocolInvalid("terminal field "+field+" must be an integer literal", now)
	}
	category, known := terminalStatusSignals[parsed]
	return signalRecord{present: true, known: known, category: category}, nil
}

// integerHintField 只接受精确 JSON 整数字面量。maxValue 是字面量上界：
// retry_after 用 24h 秒数，not_before 用 epoch 时间戳的 int64 上界。显式
// null fail closed；其它错误类型、溢出与越界丢弃 hint。
func integerHintField(event map[string]any, field string, maxValue int64, now time.Time) (int64, bool, *typedFailure) {
	value, present := event[field]
	if !present {
		return 0, false, nil
	}
	if value == nil {
		return 0, false, explicitNullViolation(field, now)
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false, nil
	}
	parsed, err := number.Int64()
	if err != nil || parsed <= 0 || parsed > maxValue {
		return 0, false, nil
	}
	return parsed, true, nil
}

// extractTerminalSignals 提取终止事件的信号与 hint。载体只允许 object、
// string、null 或 missing；nested error object 与顶层 code/status/
// retry_after/not_before 共存统一拒绝。
func extractTerminalSignals(event map[string]any, now time.Time) ([]signalRecord, *time.Duration, *time.Time, *typedFailure) {
	var records []signalRecord
	carrier, hasCarrier := event["error"]
	nested := false
	if hasCarrier {
		switch carrier.(type) {
		case map[string]any:
			nested = true
		case string, nil:
			// 字符串与显式 null 载体合法：终止性错误但没有 nested 信号。
		default:
			return nil, nil, nil, qwenProtocolInvalid("terminal error carrier must be an object, string or null", now)
		}
	}
	if nested {
		// 顶层 type 是事件判别字段，不参与信号；显式 null 必须区别于 missing。
		for _, field := range []string{"code", "status", "retry_after", "not_before"} {
			if _, present := event[field]; present {
				return nil, nil, nil, qwenProtocolInvalid("nested error object conflicts with top-level fields", now)
			}
		}
		object := carrier.(map[string]any)
		code, violation := stringSignalField(object, "code", terminalCodeSignals, now)
		if violation != nil {
			return nil, nil, nil, violation
		}
		typ, violation := stringSignalField(object, "type", terminalTypeSignals, now)
		if violation != nil {
			return nil, nil, nil, violation
		}
		status, violation := statusSignalField(object, "status", now)
		if violation != nil {
			return nil, nil, nil, violation
		}
		return []signalRecord{code, typ, status}, nil, nil, nil
	}
	code, violation := stringSignalField(event, "code", terminalCodeSignals, now)
	if violation != nil {
		return nil, nil, nil, violation
	}
	status, violation := statusSignalField(event, "status", now)
	if violation != nil {
		return nil, nil, nil, violation
	}
	records = []signalRecord{code, status}

	retryAfterSeconds, hasRetryAfter, violation := integerHintField(event, "retry_after", maxRetryHintSeconds, now)
	if violation != nil {
		return nil, nil, nil, violation
	}
	notBeforeSeconds, hasNotBefore, violation := integerHintField(event, "not_before", math.MaxInt64, now)
	if violation != nil {
		return nil, nil, nil, violation
	}
	var retryAfter *time.Duration
	var notBefore *time.Time
	// 两个 hint 同时有效视为冲突：成对丢弃，分类本身不受影响。
	if hasRetryAfter && !hasNotBefore {
		duration := time.Duration(retryAfterSeconds) * time.Second
		retryAfter = &duration
	} else if hasNotBefore && !hasRetryAfter {
		moment := time.Unix(notBeforeSeconds, 0).UTC()
		if moment.After(now) && !moment.After(now.Add(port.MaxRetryHintWindow)) {
			notBefore = &moment
		}
	}
	return records, retryAfter, notBefore, nil
}

// classifyTerminalFailure 分类一个 error 终止事件（type=error，或
// result 且 subtype 非 success）。返回值永远非 nil。
func classifyTerminalFailure(event map[string]any, now time.Time) *typedFailure {
	records, retryAfter, notBefore, violation := extractTerminalSignals(event, now)
	if violation != nil {
		return violation
	}
	hasSignal := false
	for _, record := range records {
		if record.present {
			hasSignal = true
			break
		}
	}
	if !hasSignal {
		detail := "provider reported a terminal error"
		if eventType, _ := event["type"].(string); eventType == "result" {
			detail = "result subtype is not success (expected success)"
		}
		return newQwenFailure(port.FailureKindProviderTerminal, detail, nil, nil, now)
	}
	kind, detail, violated := mergeTerminalSignals(records)
	if violated {
		return qwenProtocolInvalid(detail, now)
	}
	return newQwenFailure(kind, detail, retryAfter, notBefore, now)
}

// successTerminalViolation 检查 success 终止事件是否与 error 载体或信号
// 字段共存；共存统一归 protocol-invalid/do-not-retry。
func successTerminalViolation(event map[string]any, now time.Time) *typedFailure {
	for _, field := range []string{"error", "code", "status"} {
		if _, present := event[field]; present {
			return qwenProtocolInvalid("success and error terminals cannot coexist", now)
		}
	}
	return nil
}

// failureProjection 从最终返回的错误提取安全失败投影：只有封闭枚举、
// 整数秒与 RFC3339 时间能进入 metadata。
func failureProjection(failure error) (kind string, disposition string, retryAfterSeconds int64, notBefore string) {
	typed, ok := port.AsAdapterFailure(failure)
	if !ok {
		return "", "", 0, ""
	}
	if typed.RetryAfter > 0 {
		retryAfterSeconds = int64(typed.RetryAfter.Round(time.Second) / time.Second)
	}
	if !typed.NotBefore.IsZero() {
		notBefore = typed.NotBefore.UTC().Format(time.RFC3339)
	}
	return string(typed.Kind), string(typed.Disposition), retryAfterSeconds, notBefore
}

// sessionDigestOf 把 provider session ID 转成固定形状摘要；metadata 永远不
// 复制原始 session ID。
func sessionDigestOf(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sessionID))
	return "sha256:" + hex.EncodeToString(sum[:])
}
