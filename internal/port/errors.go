package port

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

type permanentError struct{ err error }

func (e permanentError) Error() string   { return e.err.Error() }
func (e permanentError) Unwrap() error   { return e.err }
func (e permanentError) Permanent() bool { return true }

func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

func Permanentf(format string, args ...any) error { return Permanent(fmt.Errorf(format, args...)) }

func IsPermanent(err error) bool {
	var target interface{ Permanent() bool }
	return errors.As(err, &target) && target.Permanent()
}

// FailureKind 是 Provider 终止性失败的封闭分类集合。Qwen CLI/底层 API 会把
// quota、rate limit、DNS 或 connection terminal error 包装成 exitCode=0，
// 因此分类只能依赖结构化信号，exitCode、文件存在或自由文本 summary 都不是
// 成功或失败的证据。
type FailureKind string

const (
	FailureKindQuotaExhausted    FailureKind = "quota-exhausted"
	FailureKindRateLimited       FailureKind = "rate-limited"
	FailureKindDNSFailure        FailureKind = "dns-failure"
	FailureKindConnectionFailure FailureKind = "connection-failure"
	FailureKindProtocolInvalid   FailureKind = "protocol-invalid"
	FailureKindResultMissing     FailureKind = "result-missing"
	FailureKindProviderTerminal  FailureKind = "provider-terminal"
)

// FailureKinds 冻结封闭分类全集，顺序固定，供契约测试逐项遍历。
var FailureKinds = []FailureKind{
	FailureKindQuotaExhausted,
	FailureKindRateLimited,
	FailureKindDNSFailure,
	FailureKindConnectionFailure,
	FailureKindProtocolInvalid,
	FailureKindResultMissing,
	FailureKindProviderTerminal,
}

// RetryDisposition 是失败处置意向的封闭集合。
type RetryDisposition string

const (
	RetryDispositionRetryable  RetryDisposition = "retryable"
	RetryDispositionBlocked    RetryDisposition = "blocked"
	RetryDispositionDoNotRetry RetryDisposition = "do-not-retry"
)

// RetryDispositions 冻结封闭处置全集，顺序固定。
var RetryDispositions = []RetryDisposition{
	RetryDispositionRetryable,
	RetryDispositionBlocked,
	RetryDispositionDoNotRetry,
}

// AdapterID 是 Adapter 的封闭身份集合。
type AdapterID string

const (
	AdapterIDQwen     AdapterID = "qwen"
	AdapterIDPi       AdapterID = "pi"
	AdapterIDOpenCode AdapterID = "opencode"
	AdapterIDFake     AdapterID = "fake"
	AdapterIDCodex    AdapterID = "codex"
	AdapterIDQoder    AdapterID = "qoder"
)

// AdapterIDs 冻结封闭身份全集，顺序固定。
var AdapterIDs = []AdapterID{
	AdapterIDQwen,
	AdapterIDPi,
	AdapterIDOpenCode,
	AdapterIDFake,
	AdapterIDCodex,
	AdapterIDQoder,
}

// dispositionForKey 冻结唯一合法配对：quota=blocked；
// rate/DNS/connection=retryable；protocol/result-missing/provider-terminal=
// do-not-retry。WorkerResult 缺失是协议/transport 的结构性失败，不能用
// 另一个 Worker Attempt 猜测性重跑。
var dispositionForKey = map[FailureKind]RetryDisposition{
	FailureKindQuotaExhausted:    RetryDispositionBlocked,
	FailureKindRateLimited:       RetryDispositionRetryable,
	FailureKindDNSFailure:        RetryDispositionRetryable,
	FailureKindConnectionFailure: RetryDispositionRetryable,
	FailureKindResultMissing:     RetryDispositionDoNotRetry,
	FailureKindProtocolInvalid:   RetryDispositionDoNotRetry,
	FailureKindProviderTerminal:  RetryDispositionDoNotRetry,
}

// DispositionFor 返回失败分类唯一合法的处置意向。
func DispositionFor(kind FailureKind) (RetryDisposition, bool) {
	disposition, ok := dispositionForKey[kind]
	return disposition, ok
}

// MaxRetryHintWindow 是 retry hint 的固定上界；超过 24h 的 hint 一律拒绝。
const MaxRetryHintWindow = 24 * time.Hour

// AdapterFailure 是跨 Adapter 的 typed 终止性失败。Error() 只由固定词汇与
// 已验证的安全数值/时间组成：构造器不接受任何自由文本，因此 provider
// message、stderr、session/request ID、credential、URL 或绝对路径都不可能
// 进入错误文本。
type AdapterFailure struct {
	Adapter     AdapterID
	Kind        FailureKind
	Disposition RetryDisposition
	// RetryAfter 为零值表示没有 hint；非零值必然通过构造器验证。
	RetryAfter time.Duration
	// NotBefore 为零值表示没有 hint；非零值必然通过构造器验证。
	NotBefore time.Time
}

// NewAdapterFailure 构造 typed 失败并强制封闭契约：未知枚举、非法配对、
// 冲突/零/负/过去/溢出或超过 24h 的 hint 全部拒绝。retryAfter 与 notBefore
// 为 nil 表示对应 hint 缺失；两者同时出现视为冲突。now 是验证 notBefore 的
// 参考时间，必须显式提供，保证失败构造可确定性重放。
func NewAdapterFailure(adapter AdapterID, kind FailureKind, disposition RetryDisposition, retryAfter *time.Duration, notBefore *time.Time, now time.Time) (AdapterFailure, error) {
	if now.IsZero() {
		return AdapterFailure{}, errors.New("adapter failure: reference time is required")
	}
	if !knownAdapterID(adapter) {
		return AdapterFailure{}, errors.New("adapter failure: unknown adapter id")
	}
	expected, knownKind := dispositionForKey[kind]
	if !knownKind {
		return AdapterFailure{}, errors.New("adapter failure: unknown failure kind")
	}
	if !knownDisposition(disposition) {
		return AdapterFailure{}, errors.New("adapter failure: unknown retry disposition")
	}
	if disposition != expected {
		return AdapterFailure{}, errors.New("adapter failure: failure kind and retry disposition disagree")
	}
	if retryAfter != nil && notBefore != nil {
		return AdapterFailure{}, errors.New("adapter failure: retry hints conflict")
	}
	failure := AdapterFailure{Adapter: adapter, Kind: kind, Disposition: disposition}
	if retryAfter != nil {
		if *retryAfter <= 0 {
			return AdapterFailure{}, errors.New("adapter failure: retry-after must be positive")
		}
		if *retryAfter > MaxRetryHintWindow {
			return AdapterFailure{}, errors.New("adapter failure: retry-after exceeds the 24h window")
		}
		failure.RetryAfter = *retryAfter
	}
	if notBefore != nil {
		if notBefore.IsZero() {
			return AdapterFailure{}, errors.New("adapter failure: not-before must not be zero")
		}
		if !notBefore.After(now) {
			return AdapterFailure{}, errors.New("adapter failure: not-before is in the past")
		}
		if notBefore.After(now.Add(MaxRetryHintWindow)) {
			return AdapterFailure{}, errors.New("adapter failure: not-before exceeds the 24h window")
		}
		failure.NotBefore = *notBefore
	}
	return failure, nil
}

func knownAdapterID(adapter AdapterID) bool {
	for _, candidate := range AdapterIDs {
		if adapter == candidate {
			return true
		}
	}
	return false
}

func knownDisposition(disposition RetryDisposition) bool {
	for _, candidate := range RetryDispositions {
		if disposition == candidate {
			return true
		}
	}
	return false
}

// Error 只拼接封闭枚举、整数秒与 RFC3339 时间这些已验证的安全值。
func (f AdapterFailure) Error() string {
	message := "adapter " + string(f.Adapter) + " provider failure " + string(f.Kind) + "/" + string(f.Disposition)
	if f.RetryAfter > 0 {
		message += " retry-after=" + strconv.FormatInt(int64(f.RetryAfter.Round(time.Second)/time.Second), 10) + "s"
	}
	if !f.NotBefore.IsZero() {
		message += " not-before=" + f.NotBefore.UTC().Format(time.RFC3339)
	}
	return message
}

// Permanent 与既有 IsPermanent 语义兼容：do-not-retry 的 typed 失败即视为
// 永久失败，retryable/blocked 仍交给运维重试预算处理。
func (f AdapterFailure) Permanent() bool { return f.Disposition == RetryDispositionDoNotRetry }

// AsAdapterFailure 是 errors.As 的 typed helper，沿错误链提取第一个
// AdapterFailure。
func AsAdapterFailure(err error) (AdapterFailure, bool) {
	var failure AdapterFailure
	if errors.As(err, &failure) {
		return failure, true
	}
	return AdapterFailure{}, false
}

// NormalizeAdapterFailure extracts exactly one concrete AdapterFailure from
// an error graph and rebuilds it through the closed constructor. A single
// carrier may be wrapped arbitrarily; joined graphs containing zero or more
// than one concrete carrier are distinguished so Core can reject ambiguous
// authority instead of trusting errors.As traversal order. Custom As-only
// projections are rejected because they have no concrete replayable carrier.
func NormalizeAdapterFailure(err error, now time.Time) (AdapterFailure, bool, error) {
	carriers, hasProjection, walkErr := concreteAdapterFailures(err)
	if walkErr != nil {
		return AdapterFailure{}, true, walkErr
	}
	if len(carriers) == 0 && !hasProjection {
		return AdapterFailure{}, false, nil
	}
	if now.IsZero() {
		return AdapterFailure{}, true, errors.New("adapter failure: normalization reference time is required")
	}
	if hasProjection || len(carriers) != 1 {
		return AdapterFailure{}, true, errors.New("adapter failure: ambiguous typed carrier graph")
	}
	failure := carriers[0]
	var retryAfter *time.Duration
	if failure.RetryAfter != 0 {
		retryAfter = &failure.RetryAfter
	}
	var notBefore *time.Time
	if !failure.NotBefore.IsZero() {
		value := failure.NotBefore.UTC()
		notBefore = &value
	}
	normalized, normalizeErr := NewAdapterFailure(failure.Adapter, failure.Kind, failure.Disposition, retryAfter, notBefore, now.UTC())
	if normalizeErr != nil {
		return AdapterFailure{}, true, normalizeErr
	}
	return normalized, true, nil
}

const maxAdapterFailureGraphNodes = 64

func concreteAdapterFailures(root error) ([]AdapterFailure, bool, error) {
	pending := []error{root}
	carriers := make([]AdapterFailure, 0, 1)
	hasProjection := false
	visited := 0
	for len(pending) > 0 {
		visited++
		if visited > maxAdapterFailureGraphNodes || len(pending) > maxAdapterFailureGraphNodes {
			return nil, hasProjection, errors.New("adapter failure: typed carrier graph exceeds the bounded node limit")
		}
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			continue
		}
		switch typed := current.(type) {
		case AdapterFailure:
			carriers = append(carriers, typed)
			continue
		case *AdapterFailure:
			if typed == nil {
				return nil, hasProjection, errors.New("adapter failure: nil typed carrier")
			}
			carriers = append(carriers, *typed)
			continue
		}
		if _, ok := current.(interface{ As(any) bool }); ok {
			hasProjection = true
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			pending = append(pending, wrapped.Unwrap()...)
		case interface{ Unwrap() error }:
			pending = append(pending, wrapped.Unwrap())
		}
	}
	return carriers, hasProjection, nil
}
