package qwen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chiga0/marshal-harness/internal/port"
)

// classifyNow 是分类单元测试的固定参考时间，保证 hint 验证可确定性重放。
var classifyNow = time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)

// conflictRepetitions 是取消/截止冲突的重复压力次数：确定性必须逐次成立。
const conflictRepetitions = 20

func terminalEventMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var event map[string]any
	if err := decoder.Decode(&event); err != nil {
		t.Fatal(err)
	}
	return event
}

// terminalLine 把一行完整的终止事件 JSON 作为 JSONL 行输出。
func terminalLine(body string) string {
	return `printf '%s\n' ` + shellQuote(body)
}

// controlledDeadlineContext 只在测试显式 expire 后报告
// context.DeadlineExceeded。它让 terminal 已形成与 deadline 到达之间存在
// 确定性 happens-before，而不依赖宿主调度速度或真实 wall-clock timer。
type controlledDeadlineContext struct {
	done chan struct{}
	once sync.Once
}

func newControlledDeadlineContext() *controlledDeadlineContext {
	return &controlledDeadlineContext{done: make(chan struct{})}
}

func (*controlledDeadlineContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *controlledDeadlineContext) Done() <-chan struct{}     { return c.done }
func (c *controlledDeadlineContext) Value(any) any             { return nil }
func (c *controlledDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}
func (c *controlledDeadlineContext) expire() { c.once.Do(func() { close(c.done) }) }

// TestTerminalClassificationFollowsClosedSignalTable 冻结封闭信号表：分类只
// 接受结构化 code/type/status，不使用 message substring；每个 signal 先独立
// 记录 presence/known/类别再合并。
func TestTerminalClassificationFollowsClosedSignalTable(t *testing.T) {
	for _, test := range []struct {
		name        string
		raw         string
		kind        port.FailureKind
		disposition port.RetryDisposition
	}{
		{name: "quota-code", raw: `{"type":"error","error":{"code":"ResourceExhausted"}}`, kind: port.FailureKindQuotaExhausted, disposition: port.RetryDispositionBlocked},
		{name: "quota-code-alias", raw: `{"type":"error","error":{"code":"QuotaExceeded"}}`, kind: port.FailureKindQuotaExhausted, disposition: port.RetryDispositionBlocked},
		{name: "quota-type", raw: `{"type":"error","error":{"type":"quota_exhausted"}}`, kind: port.FailureKindQuotaExhausted, disposition: port.RetryDispositionBlocked},
		{name: "rate-code", raw: `{"type":"error","error":{"code":"RateLimited"}}`, kind: port.FailureKindRateLimited, disposition: port.RetryDispositionRetryable},
		{name: "rate-throttling-code", raw: `{"type":"error","error":{"code":"Throttling.RateQuota"}}`, kind: port.FailureKindRateLimited, disposition: port.RetryDispositionRetryable},
		{name: "rate-type", raw: `{"type":"error","error":{"type":"rate_limited"}}`, kind: port.FailureKindRateLimited, disposition: port.RetryDispositionRetryable},
		{name: "rate-status-429", raw: `{"type":"error","error":{"status":429}}`, kind: port.FailureKindRateLimited, disposition: port.RetryDispositionRetryable},
		{name: "dns-code-enotfound", raw: `{"type":"error","error":{"code":"ENOTFOUND"}}`, kind: port.FailureKindDNSFailure, disposition: port.RetryDispositionRetryable},
		{name: "dns-code-eai-again", raw: `{"type":"error","error":{"code":"EAI_AGAIN"}}`, kind: port.FailureKindDNSFailure, disposition: port.RetryDispositionRetryable},
		{name: "dns-type", raw: `{"type":"error","error":{"type":"dns_failure"}}`, kind: port.FailureKindDNSFailure, disposition: port.RetryDispositionRetryable},
		{name: "connection-code-refused", raw: `{"type":"error","error":{"code":"ECONNREFUSED"}}`, kind: port.FailureKindConnectionFailure, disposition: port.RetryDispositionRetryable},
		{name: "connection-code-reset", raw: `{"type":"error","error":{"code":"ECONNRESET"}}`, kind: port.FailureKindConnectionFailure, disposition: port.RetryDispositionRetryable},
		{name: "connection-code-timeout", raw: `{"type":"error","error":{"code":"ETIMEDOUT"}}`, kind: port.FailureKindConnectionFailure, disposition: port.RetryDispositionRetryable},
		{name: "connection-type", raw: `{"type":"error","error":{"type":"connection_failure"}}`, kind: port.FailureKindConnectionFailure, disposition: port.RetryDispositionRetryable},
		{name: "dns-code-and-type-agree", raw: `{"type":"error","error":{"code":"ENOTFOUND","type":"dns_failure"}}`, kind: port.FailureKindDNSFailure, disposition: port.RetryDispositionRetryable},
		{name: "quota-code-and-type-agree", raw: `{"type":"error","error":{"code":"ResourceExhausted","type":"quota_exhausted"}}`, kind: port.FailureKindQuotaExhausted, disposition: port.RetryDispositionBlocked},
		{name: "top-level-code", raw: `{"type":"error","code":"ECONNREFUSED"}`, kind: port.FailureKindConnectionFailure, disposition: port.RetryDispositionRetryable},
		{name: "top-level-status", raw: `{"type":"result","subtype":"error_quota","status":429}`, kind: port.FailureKindRateLimited, disposition: port.RetryDispositionRetryable},
		{name: "unknown-single-code", raw: `{"type":"error","error":{"code":"MysteryFailure"}}`, kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
		{name: "unknown-single-status", raw: `{"type":"error","error":{"status":500}}`, kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
		{name: "string-carrier", raw: `{"type":"error","error":"provider crashed with secret details"}`, kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
		{name: "null-carrier", raw: `{"type":"error","error":null}`, kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
		{name: "no-carrier", raw: `{"type":"error"}`, kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
		{name: "result-subtype-without-signals", raw: `{"type":"result","subtype":"error_during_execution"}`, kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
		{name: "unknown-not-rescued-by-429", raw: `{"type":"error","error":{"code":"MysteryFailure","status":429}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "unknown-not-rescued-by-known-code", raw: `{"type":"error","error":{"code":"ENOTFOUND","type":"mystery_type"}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "two-unknowns", raw: `{"type":"error","error":{"code":"MysteryA","type":"mystery_b"}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "quota-code-with-429", raw: `{"type":"error","error":{"code":"ResourceExhausted","status":429}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "dns-code-with-429", raw: `{"type":"error","error":{"code":"ENOTFOUND","status":429}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "connection-code-with-429", raw: `{"type":"error","error":{"code":"ECONNRESET","status":429}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "quota-code-with-rate-type", raw: `{"type":"error","error":{"code":"ResourceExhausted","type":"rate_limited"}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "dns-code-with-connection-type", raw: `{"type":"error","error":{"code":"ENOTFOUND","type":"connection_failure"}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "top-level-code-with-429", raw: `{"type":"error","code":"ENOTFOUND","status":429}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := classifyTerminalFailure(terminalEventMap(t, test.raw), classifyNow)
			typed, ok := port.AsAdapterFailure(failure)
			if !ok {
				t.Fatalf("classification did not produce a typed failure: %v", failure)
			}
			if typed.Adapter != port.AdapterIDQwen || typed.Kind != test.kind || typed.Disposition != test.disposition {
				t.Fatalf("classification = %s/%s/%s, want qwen %s/%s", typed.Adapter, typed.Kind, typed.Disposition, test.kind, test.disposition)
			}
			wantDisposition, _ := port.DispositionFor(test.kind)
			if typed.Disposition != wantDisposition {
				t.Fatalf("disposition %s violates the unique pairing for %s", typed.Disposition, test.kind)
			}
			if test.kind == port.FailureKindProtocolInvalid && !errors.Is(failure, ErrProtocol) {
				t.Fatalf("protocol-invalid classification must keep ErrProtocol identity: %v", failure)
			}
			// Error() 绝不回显 provider 输入：原始 JSON 里的任何自由文本都
			// 不能进入错误文本。
			for _, fragment := range []string{"MysteryFailure", "mystery", "provider crashed", "secret"} {
				if strings.Contains(failure.Error(), fragment) {
					t.Fatalf("Error() leaked provider input %q: %s", fragment, failure.Error())
				}
			}
		})
	}
}

// TestTerminalClassificationRejectsInvalidShapes 冻结载体与字段的 fail-closed
// 规则：载体只允许 object/string/null/missing，字段类型错误与显式 null 一律
// protocol-invalid，显式 null 必须区别于 missing。
func TestTerminalClassificationRejectsInvalidShapes(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "array-carrier", raw: `{"type":"error","error":[1,2]}`},
		{name: "bool-carrier", raw: `{"type":"error","error":true}`},
		{name: "number-carrier", raw: `{"type":"error","error":42}`},
		{name: "nested-code-wrong-type", raw: `{"type":"error","error":{"code":429}}`},
		{name: "nested-type-wrong-type", raw: `{"type":"error","error":{"type":42}}`},
		{name: "nested-status-string", raw: `{"type":"error","error":{"status":"429"}}`},
		{name: "nested-status-decimal", raw: `{"type":"error","error":{"status":429.5}}`},
		{name: "nested-status-exponent", raw: `{"type":"error","error":{"status":4.29e2}}`},
		{name: "nested-status-bool", raw: `{"type":"error","error":{"status":true}}`},
		{name: "nested-status-array", raw: `{"type":"error","error":{"status":[429]}}`},
		{name: "nested-status-object", raw: `{"type":"error","error":{"status":{"code":429}}}`},
		{name: "nested-status-overflow", raw: `{"type":"error","error":{"status":99999999999999999999999999}}`},
		{name: "nested-code-explicit-null", raw: `{"type":"error","error":{"code":null}}`},
		{name: "nested-type-explicit-null", raw: `{"type":"error","error":{"type":null}}`},
		{name: "nested-status-explicit-null", raw: `{"type":"error","error":{"status":null}}`},
		{name: "top-level-code-explicit-null", raw: `{"type":"error","code":null}`},
		{name: "top-level-status-explicit-null", raw: `{"type":"error","status":null}`},
		{name: "retry-after-explicit-null", raw: `{"type":"error","retry_after":null}`},
		{name: "not-before-explicit-null", raw: `{"type":"error","not_before":null}`},
		{name: "nested-object-with-top-level-status", raw: `{"type":"error","error":{"code":"ENOTFOUND"},"status":429}`},
		{name: "nested-object-with-top-level-code", raw: `{"type":"error","error":{},"code":"ENOTFOUND"}`},
		{name: "nested-object-with-top-level-retry-after", raw: `{"type":"error","error":{"code":"ENOTFOUND"},"retry_after":7}`},
		{name: "nested-object-with-top-level-not-before", raw: `{"type":"error","error":{},"not_before":null}`},
		{name: "nested-object-with-top-level-explicit-null-code", raw: `{"type":"error","error":{"code":"ENOTFOUND"},"code":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := classifyTerminalFailure(terminalEventMap(t, test.raw), classifyNow)
			typed, ok := port.AsAdapterFailure(failure)
			if !ok || typed.Kind != port.FailureKindProtocolInvalid || typed.Disposition != port.RetryDispositionDoNotRetry {
				t.Fatalf("classification = %v, want protocol-invalid/do-not-retry", failure)
			}
			if !errors.Is(failure, ErrProtocol) {
				t.Fatalf("protocol-invalid must keep ErrProtocol identity: %v", failure)
			}
		})
	}
}

// TestTerminalHintsAreStrictIntegerLiterals 冻结 retry_after/not_before 的
// 精确整数字面量纪律：非法字面量丢弃或 fail closed，合法 hint 进入 typed
// 失败并保持有界。
func TestTerminalHintsAreStrictIntegerLiterals(t *testing.T) {
	classify := func(t *testing.T, raw string) port.AdapterFailure {
		t.Helper()
		failure := classifyTerminalFailure(terminalEventMap(t, raw), classifyNow)
		typed, ok := port.AsAdapterFailure(failure)
		if !ok {
			t.Fatalf("classification did not produce a typed failure: %v", failure)
		}
		return typed
	}

	t.Run("valid-retry-after", func(t *testing.T) {
		typed := classify(t, `{"type":"error","code":"ENOTFOUND","retry_after":77}`)
		if typed.Kind != port.FailureKindDNSFailure || typed.RetryAfter != 77*time.Second {
			t.Fatalf("typed = %+v, want dns with retry-after 77s", typed)
		}
		if !strings.Contains(classifyTerminalFailure(terminalEventMap(t, `{"type":"error","code":"ENOTFOUND","retry_after":77}`), classifyNow).Error(), "retry-after=77s") {
			t.Fatal("safe hint must render as bounded integer seconds")
		}
	})
	t.Run("boundary-retry-after-24h", func(t *testing.T) {
		typed := classify(t, `{"type":"error","code":"ENOTFOUND","retry_after":86400}`)
		if typed.RetryAfter != port.MaxRetryHintWindow {
			t.Fatalf("retry-after = %s, want 24h boundary accepted", typed.RetryAfter)
		}
	})
	t.Run("invalid-retry-after-discarded", func(t *testing.T) {
		for name, raw := range map[string]string{
			"string":   `{"type":"error","code":"ENOTFOUND","retry_after":"77"}`,
			"decimal":  `{"type":"error","code":"ENOTFOUND","retry_after":7.7}`,
			"exponent": `{"type":"error","code":"ENOTFOUND","retry_after":7.7e1}`,
			"bool":     `{"type":"error","code":"ENOTFOUND","retry_after":true}`,
			"array":    `{"type":"error","code":"ENOTFOUND","retry_after":[77]}`,
			"object":   `{"type":"error","code":"ENOTFOUND","retry_after":{"seconds":77}}`,
			"zero":     `{"type":"error","code":"ENOTFOUND","retry_after":0}`,
			"negative": `{"type":"error","code":"ENOTFOUND","retry_after":-5}`,
			"over-24h": `{"type":"error","code":"ENOTFOUND","retry_after":86401}`,
			"overflow": `{"type":"error","code":"ENOTFOUND","retry_after":99999999999999999999999999}`,
		} {
			typed := classify(t, raw)
			if typed.Kind != port.FailureKindDNSFailure {
				t.Fatalf("%s: kind = %s, want classification preserved", name, typed.Kind)
			}
			if typed.RetryAfter != 0 {
				t.Fatalf("%s: retry-after must be discarded: %+v", name, typed)
			}
		}
	})
	t.Run("valid-not-before", func(t *testing.T) {
		deadline := classifyNow.Add(time.Hour)
		raw := fmt.Sprintf(`{"type":"error","code":"ENOTFOUND","not_before":%d}`, deadline.Unix())
		typed := classify(t, raw)
		if typed.Kind != port.FailureKindDNSFailure || !typed.NotBefore.Equal(time.Unix(deadline.Unix(), 0).UTC()) {
			t.Fatalf("typed = %+v, want dns with safe not-before", typed)
		}
	})
	t.Run("invalid-not-before-discarded", func(t *testing.T) {
		past := classifyNow.Add(-time.Hour).Unix()
		overWindow := classifyNow.Add(port.MaxRetryHintWindow + time.Hour).Unix()
		for name, raw := range map[string]string{
			"past":      fmt.Sprintf(`{"type":"error","code":"ENOTFOUND","not_before":%d}`, past),
			"over-24h":  fmt.Sprintf(`{"type":"error","code":"ENOTFOUND","not_before":%d}`, overWindow),
			"string":    `{"type":"error","code":"ENOTFOUND","not_before":"soon"}`,
			"decimal":   `{"type":"error","code":"ENOTFOUND","not_before":123.5}`,
			"bool":      `{"type":"error","code":"ENOTFOUND","not_before":false}`,
			"zero-ish":  `{"type":"error","code":"ENOTFOUND","not_before":0}`,
			"negative":  `{"type":"error","code":"ENOTFOUND","not_before":-1}`,
			"overflow":  `{"type":"error","code":"ENOTFOUND","not_before":99999999999999999999999999}`,
			"both-used": `{"type":"error","code":"ENOTFOUND","retry_after":60,"not_before":1900000000}`,
		} {
			typed := classify(t, raw)
			if typed.Kind != port.FailureKindDNSFailure {
				t.Fatalf("%s: kind = %s, want classification preserved", name, typed.Kind)
			}
			if typed.RetryAfter != 0 || !typed.NotBefore.IsZero() {
				t.Fatalf("%s: conflicting or invalid hints must be discarded: %+v", name, typed)
			}
		}
	})
}

// TestRunClassifiesStructuredProviderTerminalFailures 用真实进程覆盖完整
// 分类：exitCode=0 不构成成功证据，typed 身份绑定 qwen 且 metadata 与最终
// returned error 一致。
func TestRunClassifiesStructuredProviderTerminalFailures(t *testing.T) {
	for _, test := range []struct {
		name        string
		terminal    string
		kind        port.FailureKind
		disposition port.RetryDisposition
	}{
		{name: "dns-nested-code", terminal: `{"type":"error","error":{"code":"ENOTFOUND"}}`, kind: port.FailureKindDNSFailure, disposition: port.RetryDispositionRetryable},
		{name: "quota-nested-code", terminal: `{"type":"error","error":{"code":"ResourceExhausted"}}`, kind: port.FailureKindQuotaExhausted, disposition: port.RetryDispositionBlocked},
		{name: "rate-nested-status", terminal: `{"type":"error","error":{"status":429}}`, kind: port.FailureKindRateLimited, disposition: port.RetryDispositionRetryable},
		{name: "connection-top-level-code", terminal: `{"type":"error","code":"ECONNREFUSED"}`, kind: port.FailureKindConnectionFailure, disposition: port.RetryDispositionRetryable},
		{name: "result-terminal-top-level-status", terminal: `{"type":"result","subtype":"error_quota","status":429}`, kind: port.FailureKindRateLimited, disposition: port.RetryDispositionRetryable},
		{name: "unknown-signal", terminal: `{"type":"error","error":{"code":"MysteryFailure"}}`, kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
		{name: "conflicting-signals", terminal: `{"type":"error","error":{"code":"ResourceExhausted","status":429}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(test.terminal)}, "\n")
			fixture := newRunFixture(t, supportedBinary, body)
			_, err := fixture.adapter.Run(context.Background(), fixture.request)
			failure, ok := port.AsAdapterFailure(err)
			if !ok || failure.Adapter != port.AdapterIDQwen || failure.Kind != test.kind || failure.Disposition != test.disposition {
				t.Fatalf("err = %v, want typed qwen %s/%s", err, test.kind, test.disposition)
			}
			if test.kind == port.FailureKindProtocolInvalid && !errors.Is(err, ErrProtocol) {
				t.Fatalf("protocol-invalid must keep ErrProtocol identity: %v", err)
			}
			metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
			if metaErr != nil {
				t.Fatal(metaErr)
			}
			for _, want := range []string{`"failureKind": "` + string(test.kind) + `"`, `"retryDisposition": "` + string(test.disposition) + `"`, `"exitCode": 0`} {
				if !strings.Contains(string(metadata), want) {
					t.Fatalf("metadata must stay consistent with the returned error (missing %s): %s", want, metadata)
				}
			}
		})
	}
	t.Run("retry-after-hint-survives-run", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(`{"type":"error","code":"ENOTFOUND","retry_after":77}`)}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindDNSFailure || failure.RetryAfter != 77*time.Second {
			t.Fatalf("err = %v, want dns failure with retry-after 77s", err)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil || !strings.Contains(string(metadata), `"retryAfterSeconds": 77`) {
			t.Fatalf("metadata = %s err=%v, want safe retry hint projection", metadata, metaErr)
		}
	})
	t.Run("not-before-hint-survives-run", func(t *testing.T) {
		deadline := time.Now().Add(time.Hour).Unix()
		body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(fmt.Sprintf(`{"type":"error","code":"ENOTFOUND","not_before":%d}`, deadline))}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindDNSFailure || failure.NotBefore.Unix() != deadline {
			t.Fatalf("err = %v, want dns failure with not-before hint", err)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil || !strings.Contains(string(metadata), `"notBefore": "`) {
			t.Fatalf("metadata = %s err=%v, want safe not-before projection", metadata, metaErr)
		}
	})
}

// TestRunTerminalFailureReturnsBeforeReadingDeclaredResult 用预置诱饵与不可读
// 声明证明：typed 终止失败先于 WorkerResult 读取，声明不被读取、规范化、
// 改写或伪造。
func TestRunTerminalFailureReturnsBeforeReadingDeclaredResult(t *testing.T) {
	resultPath := func(fixture runFixture) string {
		return filepath.Join(fixture.controlRoot, "output", "worker-result.json")
	}
	t.Run("bait-declaration-stays-untouched", func(t *testing.T) {
		bait := []byte(`{"bait":"worker-result-must-not-be-touched"}`)
		body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(`{"type":"error","error":{"code":"ResourceExhausted"}}`)}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		if err := os.WriteFile(resultPath(fixture), bait, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindQuotaExhausted || failure.Disposition != port.RetryDispositionBlocked {
			t.Fatalf("err = %v, want typed quota failure before any WorkerResult read", err)
		}
		content, readErr := os.ReadFile(resultPath(fixture))
		if readErr != nil || !bytes.Equal(content, bait) {
			t.Fatalf("WorkerResult bait was read, rewritten or forged: %q err=%v", content, readErr)
		}
	})
	t.Run("unreadable-declaration-does-not-mask-terminal", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(`{"type":"error","error":{"code":"ENOTFOUND"}}`)}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		// 目录不可作为文件读取：比 chmod 更不受运行用户权限影响。
		if err := os.Remove(resultPath(fixture)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(resultPath(fixture), 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindDNSFailure {
			t.Fatalf("err = %v, want typed terminal failure despite unreadable declaration", err)
		}
	})
}

// TestRunResultMissingMetadataMatchesFailure 冻结 success 之后 WorkerResult
// 缺失或不可读的 result-missing/do-not-retry 语义，metadata 投影必须与最终
// returned error 完全一致。
func TestRunResultMissingMetadataMatchesFailure(t *testing.T) {
	successBody := strings.Join([]string{initEvent("session-1", supportedBinary), resultEvent("success", 1, 1)}, "\n")
	assertResultMissing := func(t *testing.T, fixture runFixture) {
		t.Helper()
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindResultMissing || failure.Disposition != port.RetryDispositionDoNotRetry || failure.Adapter != port.AdapterIDQwen {
			t.Fatalf("err = %v, want typed result-missing/do-not-retry", err)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil {
			t.Fatal(metaErr)
		}
		if !strings.Contains(string(metadata), `"failureKind": "result-missing"`) || !strings.Contains(string(metadata), `"retryDisposition": "do-not-retry"`) {
			t.Fatalf("metadata must stay consistent with the returned error: %s", metadata)
		}
	}
	t.Run("missing-declaration", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, successBody)
		if err := os.Remove(filepath.Join(fixture.controlRoot, "output", "worker-result.json")); err != nil {
			t.Fatal(err)
		}
		assertResultMissing(t, fixture)
	})
	t.Run("unreadable-declaration", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, successBody)
		path := filepath.Join(fixture.controlRoot, "output", "worker-result.json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		assertResultMissing(t, fixture)
	})
	t.Run("schema-invalid-declaration", func(t *testing.T) {
		fixture := newRunFixture(t, supportedBinary, successBody)
		fixture.writeDeclared(t, map[string]any{"status": "weird"})
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindResultMissing || failure.Disposition != port.RetryDispositionDoNotRetry || !strings.Contains(err.Error(), "validate WorkerResult declaration") {
			t.Fatalf("err = %v, want typed result-missing for schema-invalid declaration", err)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil {
			t.Fatal(metaErr)
		}
		if !strings.Contains(string(metadata), `"failureKind": "result-missing"`) || !strings.Contains(string(metadata), `"retryDisposition": "do-not-retry"`) {
			t.Fatalf("metadata must stay consistent with the returned error: %s", metadata)
		}
	})
}

// TestRunSanitizesSessionAndProtocolMetadata 冻结脱敏纪律：返回错误与
// metadata 绝不携带 provider session ID、message、stderr、request ID、
// credential、URL、绝对路径或未知 tool name；原始证据仅保留在 bounded
// 0600 文件中。
func TestRunSanitizesSessionAndProtocolMetadata(t *testing.T) {
	sessionSentinel := "session-sk-live-SENTINEL"
	sentinels := []string{
		sessionSentinel,
		"provider-message-SENTINEL",
		"https://secret.example/leak",
		"/Users/leak/absolute/path",
		"req-sentinel-123",
		"stderr-secret-SENTINEL",
	}
	terminal := `{"type":"error","error":{"code":"ENOTFOUND","message":"provider-message-SENTINEL","request_id":"req-sentinel-123","url":"https://secret.example/leak","path":"/Users/leak/absolute/path"}}`
	body := strings.Join([]string{
		initEvent(sessionSentinel, supportedBinary),
		terminalLine(terminal),
		`printf '%s\n' 'stderr-secret-SENTINEL' >&2`,
	}, "\n")
	fixture := newRunFixture(t, supportedBinary, body)
	_, err := fixture.adapter.Run(context.Background(), fixture.request)
	failure, ok := port.AsAdapterFailure(err)
	if !ok || failure.Kind != port.FailureKindDNSFailure {
		t.Fatalf("err = %v, want typed dns failure", err)
	}
	metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
	if metaErr != nil {
		t.Fatal(metaErr)
	}
	for _, sentinel := range sentinels {
		if strings.Contains(err.Error(), sentinel) {
			t.Fatalf("returned error leaked %q: %v", sentinel, err)
		}
		if strings.Contains(string(metadata), sentinel) {
			t.Fatalf("metadata leaked %q: %s", sentinel, metadata)
		}
	}
	// session 必须以固定形状摘要出现，而不是原始 provider session ID。
	if !strings.Contains(string(metadata), `"sessionDigest": "sha256:`) {
		t.Fatalf("metadata must carry a fixed-shape session digest: %s", metadata)
	}
	// bounded 原始证据仍然保留：脱敏不等于销毁证据。
	stderrEvidence, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-stderr.log"))
	if readErr != nil || !strings.Contains(string(stderrEvidence), "stderr-secret-SENTINEL") {
		t.Fatalf("bounded stderr evidence lost: %q err=%v", stderrEvidence, readErr)
	}
	transcript, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript.jsonl"))
	if readErr != nil || !strings.Contains(string(transcript), "provider-message-SENTINEL") {
		t.Fatalf("bounded transcript evidence lost: %q err=%v", transcript, readErr)
	}
	info, statErr := os.Stat(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
	if statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("metadata permissions = %v, want 0600", info.Mode().Perm())
	}
}

// unsafeToolMetadataCases 冻结工具身份验证必须先于计数/收集/denial 处理的
// 违规输入集：缺字段、未知、被排除、未声明与非 tool 身份。
var unsafeToolMetadataCases = []struct {
	name   string
	event  string
	leaked string
}{
	{name: "missing-tool-call-id", event: `{"type":"tool","tool_name":"read_file"}`},
	{name: "unknown-tool-name", event: `{"type":"tool","tool_call_id":"t1","tool_name":"mystery_injector"}`, leaked: "mystery_injector"},
	{name: "excluded-tool", event: `{"type":"tool","tool_call_id":"t1","tool_name":"run_shell_command"}`},
	{name: "non-tool-carries-tool-identity", event: `{"type":"assistant","tool_call_id":"t1","tool_name":"read_file"}`},
}

func unsafeToolFixture(t *testing.T, event string) runFixture {
	t.Helper()
	body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(event), resultEvent("success", 1, 1)}, "\n")
	return newRunFixture(t, supportedBinary, body)
}

// TestRunFailsClosedOnUnsafeToolMetadata 冻结工具身份协议的 fail-closed
// 判定：缺 tool_call_id、未知 tool_name、被排除或未声明工具、非 tool 身份
// 一律 typed protocol-invalid/do-not-retry，且绝不回显未知 tool name；合法
// declared tool 的既有行为不得回归。
func TestRunFailsClosedOnUnsafeToolMetadata(t *testing.T) {
	for _, test := range unsafeToolMetadataCases {
		t.Run(test.name, func(t *testing.T) {
			fixture := unsafeToolFixture(t, test.event)
			_, err := fixture.adapter.Run(context.Background(), fixture.request)
			failure, ok := port.AsAdapterFailure(err)
			if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry || !errors.Is(err, ErrProtocol) {
				t.Fatalf("err = %v, want typed protocol-invalid identity violation", err)
			}
			if test.leaked != "" && strings.Contains(err.Error(), test.leaked) {
				t.Fatalf("error echoed unknown tool name: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(fixture.controlRoot, "output", "qwen-transcript.jsonl")); statErr != nil {
				t.Fatal("transcript evidence was not preserved")
			}
		})
	}
	t.Run("undeclared-tool-stays-excluded", func(t *testing.T) {
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			terminalLine(`{"type":"tool","tool_call_id":"t1","tool_name":"grep"}`),
			resultEvent("success", 1, 1),
		}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"tools": []string{"read", "edit"}}})
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry || !errors.Is(err, ErrProtocol) {
			t.Fatalf("err = %v, want typed protocol-invalid for undeclared tool", err)
		}
	})
	t.Run("legitimate-declared-tools-keep-behavior", func(t *testing.T) {
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			`printf '%s\n' '{"type":"tool","tool_call_id":"t1","tool_name":"read_file"}'`,
			`printf '%s\n' '{"type":"tool","tool_call_id":"t2","tool_name":"edit"}'`,
			resultEvent("success", 1, 1),
		}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		writeJSON(t, filepath.Join(fixture.controlRoot, "input", "task-spec.json"), map[string]any{"worker": map[string]any{"tools": []string{"read", "edit"}}})
		if _, err := fixture.adapter.Run(context.Background(), fixture.request); err != nil {
			t.Fatalf("declared tools must keep working: %v", err)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil {
			t.Fatal(metaErr)
		}
		if !strings.Contains(string(metadata), `"toolCalls": 2`) || !strings.Contains(string(metadata), `"edit"`) || !strings.Contains(string(metadata), `"read"`) {
			t.Fatalf("declared tool accounting drifted: %s", metadata)
		}
	})
}

// TestRunInvalidToolCannotPolluteMetadataOrDenials 冻结：非法工具事件不得
// 污染 metadata/denial 证据，也不得改变最终权限 outcome；违规前的合法
// denial 证据保持原样，违规本身不落 denial、不计 toolCalls/toolNames。
func TestRunInvalidToolCannotPolluteMetadataOrDenials(t *testing.T) {
	for _, test := range unsafeToolMetadataCases {
		t.Run(test.name, func(t *testing.T) {
			fixture := unsafeToolFixture(t, test.event)
			_, err := fixture.adapter.Run(context.Background(), fixture.request)
			failure, ok := port.AsAdapterFailure(err)
			if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry {
				t.Fatalf("err = %v, want typed protocol-invalid identity violation", err)
			}
			metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
			if metaErr != nil {
				t.Fatal(metaErr)
			}
			if !strings.Contains(string(metadata), `"toolCalls": 0`) || !strings.Contains(string(metadata), `"failureKind": "protocol-invalid"`) || !strings.Contains(string(metadata), `"permissionDenied": false`) || !strings.Contains(string(metadata), `"denialsFatal": 0`) {
				t.Fatalf("identity violation polluted metadata: %s", metadata)
			}
			if strings.Contains(string(metadata), `"toolNames": [`) && !strings.Contains(string(metadata), `"toolNames": []`) {
				t.Fatalf("identity violation polluted toolNames: %s", metadata)
			}
			if test.leaked != "" && strings.Contains(string(metadata), test.leaked) {
				t.Fatalf("metadata copied unknown tool name: %s", metadata)
			}
			if _, statErr := os.Stat(filepath.Join(fixture.controlRoot, "output", "denials.jsonl")); !os.IsNotExist(statErr) {
				t.Fatalf("identity violation must not write denial evidence: %v", statErr)
			}
		})
	}
	t.Run("benign-denial-before-invalid-event-keeps-outcome", func(t *testing.T) {
		// 合法 benign denial 先落证据，随后的未知工具事件只能升级为
		// protocol-invalid，不得新增/改写 denial，也不得把 benign 变成 fatal。
		benignDenial := `printf '%s\n' '{"type":"tool","tool_call_id":"t1","tool_name":"read_file","args":{"absolute_path":"'"$PWD"'/source.go"},"is_error":true,"error":"permission denied by safe-mode rule"}'`
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			benignDenial,
			terminalLine(`{"type":"tool","tool_call_id":"t2","tool_name":"mystery_injector"}`),
			resultEvent("success", 1, 1),
		}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		if err := os.WriteFile(filepath.Join(fixture.worktree, "source.go"), []byte("package x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry {
			t.Fatalf("err = %v, want typed protocol-invalid after benign denial", err)
		}
		assertDenialLog(t, fixture.controlRoot, map[string]any{"seq": float64(1), "tool": "read_file", "kind": "read", "grade": "BENIGN", "path-or-cmd": filepath.Join(fixture.worktree, "source.go")})
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil {
			t.Fatal(metaErr)
		}
		if !strings.Contains(string(metadata), `"denialsBenign": 1`) || !strings.Contains(string(metadata), `"denialsFatal": 0`) || !strings.Contains(string(metadata), `"permissionDenied": false`) || !strings.Contains(string(metadata), `"failureKind": "protocol-invalid"`) {
			t.Fatalf("invalid tool event changed the permission outcome: %s", metadata)
		}
		if strings.Contains(string(metadata), "mystery_injector") {
			t.Fatalf("metadata copied unknown tool name: %s", metadata)
		}
	})
}

// TestRunTerminalSeenClosesEventStreamAfterAnyTerminal 冻结 terminalSeen：
// 任意 result/error 终止后事件流被关闭，trailing、重复 terminal、
// success/error 共存一律 typed protocol-invalid，且不再统计 trailing
// tool/token。
func TestRunTerminalSeenClosesEventStreamAfterAnyTerminal(t *testing.T) {
	errorTerminal := terminalLine(`{"type":"error","error":{"code":"ENOTFOUND"}}`)
	assertFrozenViolation := func(t *testing.T, fixture runFixture, detail string) {
		t.Helper()
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry || !errors.Is(err, ErrProtocol) {
			t.Fatalf("err = %v, want typed protocol-invalid freeze violation", err)
		}
		if !strings.Contains(err.Error(), detail) {
			t.Fatalf("err = %v, want detail %q", err, detail)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil || !strings.Contains(string(metadata), `"failureKind": "protocol-invalid"`) {
			t.Fatalf("metadata = %s err=%v, want protocol-invalid projection", metadata, metaErr)
		}
	}
	t.Run("assistant-after-error-terminal", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-1", supportedBinary), errorTerminal, `printf '%s\n' '{"type":"assistant"}'`}, "\n")
		assertFrozenViolation(t, newRunFixture(t, supportedBinary, body), "trailing event after error terminal")
	})
	t.Run("tool-after-error-terminal", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-1", supportedBinary), errorTerminal, `printf '%s\n' '{"type":"tool","tool_call_id":"t9","tool_name":"read_file"}'`}, "\n")
		assertFrozenViolation(t, newRunFixture(t, supportedBinary, body), "trailing event after error terminal")
	})
	t.Run("success-after-error-terminal-is-duplicate", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-1", supportedBinary), errorTerminal, resultEvent("success", 1, 1)}, "\n")
		assertFrozenViolation(t, newRunFixture(t, supportedBinary, body), "duplicate terminal event")
	})
	t.Run("error-after-success-is-duplicate", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-1", supportedBinary), resultEvent("success", 1, 1), errorTerminal}, "\n")
		assertFrozenViolation(t, newRunFixture(t, supportedBinary, body), "duplicate terminal event")
	})
	t.Run("trailing-tokens-are-never-counted", func(t *testing.T) {
		trailing := terminalLine(`{"type":"result","subtype":"success","usage":{"input_tokens":999,"output_tokens":999}}`)
		body := strings.Join([]string{initEvent("session-1", supportedBinary), errorTerminal, trailing}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		if failure, ok := port.AsAdapterFailure(err); !ok || failure.Kind != port.FailureKindProtocolInvalid {
			t.Fatalf("err = %v, want typed protocol-invalid", err)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil {
			t.Fatal(metaErr)
		}
		if !strings.Contains(string(metadata), `"inputTokens": 0`) || !strings.Contains(string(metadata), `"outputTokens": 0`) {
			t.Fatalf("trailing tokens were counted: %s", metadata)
		}
	})
	t.Run("success-terminal-with-error-field-is-coexistence", func(t *testing.T) {
		coexisting := terminalLine(`{"type":"result","subtype":"success","error":{"code":"ENOTFOUND"},"usage":{"input_tokens":1,"output_tokens":1}}`)
		body := strings.Join([]string{initEvent("session-1", supportedBinary), coexisting}, "\n")
		assertFrozenViolation(t, newRunFixture(t, supportedBinary, body), "success and error terminals cannot coexist")
	})
}

// TestRunFailsClosedOnProcessTerminalConflict 冻结：structured terminal
// failure 与非零 exitCode 或终止 signal 共存都是证据冲突，统一归
// protocol-invalid/do-not-retry，不得退化为普通进程失败；exitCode/signal
// 单独既不构成成功证据，也不构成分类证据。脚本顺序输出后直接终止，无时序
// 依赖。
func TestRunFailsClosedOnProcessTerminalConflict(t *testing.T) {
	assertConflict := func(t *testing.T, fixture runFixture, wants []string) {
		t.Helper()
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry || !errors.Is(err, ErrProtocol) {
			t.Fatalf("err = %v, want conflict protocol-invalid", err)
		}
		if errors.Is(err, ErrProcessFailed) {
			t.Fatalf("conflict must not degrade to a plain process failure: %v", err)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil {
			t.Fatal(metaErr)
		}
		for _, want := range wants {
			if !strings.Contains(string(metadata), want) {
				t.Fatalf("metadata missing %s: %s", want, metadata)
			}
		}
	}
	t.Run("nonzero-exit-code", func(t *testing.T) {
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			terminalLine(`{"type":"error","error":{"code":"ENOTFOUND"}}`),
			"exit 3",
		}, "\n")
		assertConflict(t, newRunFixture(t, supportedBinary, body), []string{
			`"exitCode": 3`, `"signal": ""`, `"failureKind": "protocol-invalid"`, `"retryDisposition": "do-not-retry"`,
		})
	})
	t.Run("termination-signal", func(t *testing.T) {
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			terminalLine(`{"type":"error","error":{"code":"ENOTFOUND"}}`),
			"kill -9 $$",
		}, "\n")
		assertConflict(t, newRunFixture(t, supportedBinary, body), []string{
			`"signal": "killed"`, `"failureKind": "protocol-invalid"`, `"retryDisposition": "do-not-retry"`,
		})
	})
}

// TestRunFailsClosedOnMalformedAndDualTerminalEvidence 冻结 malformed JSONL
// 与双重/冲突终止证据的 Run 级 fail-closed：非法行、success 与 error 证据
// 共存、nested 与顶层字段冲突、非法载体与重复 terminal 一律 typed
// protocol-invalid/do-not-retry，保留 ErrProtocol 身份并落安全投影。
func TestRunFailsClosedOnMalformedAndDualTerminalEvidence(t *testing.T) {
	assertEvidenceViolation := func(t *testing.T, fixture runFixture, detail string) {
		t.Helper()
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry || !errors.Is(err, ErrProtocol) {
			t.Fatalf("err = %v, want typed protocol-invalid evidence violation", err)
		}
		if !strings.Contains(err.Error(), detail) {
			t.Fatalf("err = %v, want detail %q", err, detail)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil || !strings.Contains(string(metadata), `"failureKind": "protocol-invalid"`) {
			t.Fatalf("metadata = %s err=%v, want protocol-invalid projection", metadata, metaErr)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.controlRoot, "output", "qwen-transcript.jsonl")); statErr != nil {
			t.Fatal("raw evidence was not preserved")
		}
	}
	for _, test := range []struct {
		name     string
		terminal string
		detail   string
	}{
		{name: "malformed-jsonl-line", terminal: "not-json-at-all", detail: "malformed JSONL"},
		{name: "success-coexists-with-error-carrier", terminal: `{"type":"result","subtype":"success","error":{"code":"ENOTFOUND"},"usage":{"input_tokens":1,"output_tokens":1}}`, detail: "success and error terminals cannot coexist"},
		{name: "success-coexists-with-top-level-code", terminal: `{"type":"result","subtype":"success","code":"ENOTFOUND","usage":{"input_tokens":1,"output_tokens":1}}`, detail: "success and error terminals cannot coexist"},
		{name: "nested-error-conflicts-with-top-level-status", terminal: `{"type":"error","error":{"code":"ENOTFOUND"},"status":429}`, detail: "nested error object conflicts with top-level fields"},
		{name: "nested-error-conflicts-with-top-level-retry-after", terminal: `{"type":"error","error":{"code":"ENOTFOUND"},"retry_after":7}`, detail: "nested error object conflicts with top-level fields"},
		{name: "error-carrier-array", terminal: `{"type":"error","error":[1,2]}`, detail: "terminal error carrier must be an object, string or null"},
	} {
		t.Run(test.name, func(t *testing.T) {
			line := terminalLine(test.terminal)
			if test.name == "malformed-jsonl-line" {
				line = `printf '%s\n' ` + shellQuote(test.terminal)
			}
			body := strings.Join([]string{initEvent("session-1", supportedBinary), line}, "\n")
			assertEvidenceViolation(t, newRunFixture(t, supportedBinary, body), test.detail)
		})
	}
	t.Run("duplicate-success-terminal", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-1", supportedBinary), resultEvent("success", 1, 1), resultEvent("success", 1, 1)}, "\n")
		assertEvidenceViolation(t, newRunFixture(t, supportedBinary, body), "duplicate terminal event")
	})
	t.Run("error-terminal-after-success", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-1", supportedBinary), resultEvent("success", 1, 1), terminalLine(`{"type":"error","error":{"code":"ENOTFOUND"}}`)}, "\n")
		assertEvidenceViolation(t, newRunFixture(t, supportedBinary, body), "duplicate terminal event")
	})
}

// TestRunExitCodeZeroIsNeverSuccessEvidence 冻结：exitCode=0、文件存在与自由
// 文本 summary 都不是成功证据。进程以 exitCode=0 结束时结构化终止失败仍须
// 返回 typed 失败，预置的 WorkerResult 声明诱饵不得被读取或改写。
func TestRunExitCodeZeroIsNeverSuccessEvidence(t *testing.T) {
	t.Run("structured-failure-survives-zero-exit", func(t *testing.T) {
		bait := []byte(`{"bait":"exit-zero-is-not-success-evidence"}`)
		body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(`{"type":"error","error":{"code":"ENOTFOUND"}}`)}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		resultPath := filepath.Join(fixture.controlRoot, "output", "worker-result.json")
		if err := os.WriteFile(resultPath, bait, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Adapter != port.AdapterIDQwen || failure.Kind != port.FailureKindDNSFailure || failure.Disposition != port.RetryDispositionRetryable {
			t.Fatalf("err = %v, want typed dns failure despite exitCode=0", err)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil {
			t.Fatal(metaErr)
		}
		for _, want := range []string{`"exitCode": 0`, `"failureKind": "dns-failure"`, `"retryDisposition": "retryable"`} {
			if !strings.Contains(string(metadata), want) {
				t.Fatalf("metadata missing %s: %s", want, metadata)
			}
		}
		content, readErr := os.ReadFile(resultPath)
		if readErr != nil || !bytes.Equal(content, bait) {
			t.Fatalf("declaration bait was read or rewritten on the failure path: %q err=%v", content, readErr)
		}
	})
	t.Run("quota-terminal-is-not-masked-by-zero-exit", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(`{"type":"error","error":{"code":"ResourceExhausted"}}`)}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindQuotaExhausted || failure.Disposition != port.RetryDispositionBlocked {
			t.Fatalf("err = %v, want typed quota failure despite exitCode=0", err)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil || !strings.Contains(string(metadata), `"exitCode": 0`) {
			t.Fatalf("metadata = %s err=%v, want exitCode 0 preserved", metadata, metaErr)
		}
	})
	t.Run("protocol-violation-survives-zero-exit-and-existing-declaration", func(t *testing.T) {
		body := strings.Join([]string{initEvent("session-1", supportedBinary), resultEvent("success", 1, 1), resultEvent("success", 1, 1)}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry {
			t.Fatalf("err = %v, want typed protocol-invalid despite exitCode=0 and existing declaration", err)
		}
		declared, readErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "worker-result.json"))
		if readErr != nil || !strings.Contains(string(declared), "worker-claim") {
			t.Fatalf("existing declaration was rewritten on the failure path: %q err=%v", declared, readErr)
		}
	})
}

// TestRunExplicitNullSignalsFreezeProtocolInvalid 冻结：显式 null 信号必须
// 区别于 missing，一律 fail closed 为 typed protocol-invalid/do-not-retry；
// 无信号的 missing 载体保持 provider-terminal 分类。
func TestRunExplicitNullSignalsFreezeProtocolInvalid(t *testing.T) {
	for _, test := range []struct {
		name     string
		terminal string
	}{
		{name: "nested-code-null", terminal: `{"type":"error","error":{"code":null}}`},
		{name: "nested-type-null", terminal: `{"type":"error","error":{"type":null}}`},
		{name: "nested-status-null", terminal: `{"type":"error","error":{"status":null}}`},
		{name: "top-level-code-null", terminal: `{"type":"error","code":null}`},
		{name: "top-level-status-null", terminal: `{"type":"error","status":null}`},
		{name: "retry-after-null", terminal: `{"type":"error","retry_after":null}`},
		{name: "not-before-null", terminal: `{"type":"error","not_before":null}`},
		{name: "nested-object-with-explicit-null-top-level-code", terminal: `{"type":"error","error":{"code":"ENOTFOUND"},"code":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(test.terminal)}, "\n")
			fixture := newRunFixture(t, supportedBinary, body)
			_, err := fixture.adapter.Run(context.Background(), fixture.request)
			failure, ok := port.AsAdapterFailure(err)
			if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry || !errors.Is(err, ErrProtocol) {
				t.Fatalf("err = %v, want explicit null frozen as protocol-invalid", err)
			}
			metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
			if metaErr != nil || !strings.Contains(string(metadata), `"failureKind": "protocol-invalid"`) || !strings.Contains(string(metadata), `"retryDisposition": "do-not-retry"`) {
				t.Fatalf("metadata = %s err=%v, want protocol-invalid projection", metadata, metaErr)
			}
		})
	}
	t.Run("missing-signals-are-not-null", func(t *testing.T) {
		// 缺失信号是合法但无信号的终止：归 provider-terminal；显式 null
		// 不得与之混淆。
		body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(`{"type":"error"}`)}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		_, err := fixture.adapter.Run(context.Background(), fixture.request)
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindProviderTerminal || failure.Disposition != port.RetryDispositionDoNotRetry {
			t.Fatalf("err = %v, want provider-terminal for missing signals", err)
		}
	})
}

// TestRunNonToolIdentityIsTypedProtocolInvalid 冻结：非 tool 事件携带
// tool_name/tool_call_id 也必须走 typed 路径，一律 protocol-invalid/
// do-not-retry，且不计入任何工具统计。
func TestRunNonToolIdentityIsTypedProtocolInvalid(t *testing.T) {
	for _, test := range []struct {
		name  string
		event string
	}{
		{name: "assistant-with-tool-call-id", event: `{"type":"assistant","tool_call_id":"t1"}`},
		{name: "assistant-with-tool-name", event: `{"type":"assistant","tool_name":"read_file"}`},
		{name: "system-with-tool-identity", event: `{"type":"system","subtype":"turn","tool_call_id":"t1"}`},
		{name: "result-with-tool-identity", event: `{"type":"result","subtype":"success","tool_name":"read_file"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(test.event), resultEvent("success", 1, 1)}, "\n")
			fixture := newRunFixture(t, supportedBinary, body)
			_, err := fixture.adapter.Run(context.Background(), fixture.request)
			failure, ok := port.AsAdapterFailure(err)
			if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry || !errors.Is(err, ErrProtocol) {
				t.Fatalf("err = %v, want typed protocol-invalid non-tool identity", err)
			}
			if !strings.Contains(err.Error(), "non-tool event carries tool identity fields") {
				t.Fatalf("err = %v, want fixed non-tool identity detail", err)
			}
			metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
			if metaErr != nil {
				t.Fatal(metaErr)
			}
			if !strings.Contains(string(metadata), `"failureKind": "protocol-invalid"`) || !strings.Contains(string(metadata), `"toolCalls": 0`) {
				t.Fatalf("non-tool identity polluted metadata: %s", metadata)
			}
		})
	}
}

// TestRunUnknownSignalsCannotBeRescued 冻结：未知信号不得被 429 或任何已知
// code/type 救援。未知单一信号归 provider-terminal；未知与任何其它信号共存、
// 以及已知信号类别冲突一律 protocol-invalid/do-not-retry，错误文本不回显
// provider 控制的信号字面量。
func TestRunUnknownSignalsCannotBeRescued(t *testing.T) {
	for _, test := range []struct {
		name        string
		terminal    string
		kind        port.FailureKind
		disposition port.RetryDisposition
	}{
		{name: "unknown-code-alone", terminal: `{"type":"error","error":{"code":"MysteryFailure"}}`, kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
		{name: "unknown-status-alone", terminal: `{"type":"error","error":{"status":503}}`, kind: port.FailureKindProviderTerminal, disposition: port.RetryDispositionDoNotRetry},
		{name: "unknown-not-rescued-by-429", terminal: `{"type":"error","error":{"code":"MysteryFailure","status":429}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "unknown-type-not-rescued-by-known-code", terminal: `{"type":"error","error":{"code":"ENOTFOUND","type":"mystery_type"}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "two-unknowns", terminal: `{"type":"error","error":{"code":"MysteryA","type":"mystery_b"}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "quota-code-not-rescued-by-429", terminal: `{"type":"error","error":{"code":"ResourceExhausted","status":429}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
		{name: "dns-code-not-rescued-by-connection-type", terminal: `{"type":"error","error":{"code":"ENOTFOUND","type":"connection_failure"}}`, kind: port.FailureKindProtocolInvalid, disposition: port.RetryDispositionDoNotRetry},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Join([]string{initEvent("session-1", supportedBinary), terminalLine(test.terminal)}, "\n")
			fixture := newRunFixture(t, supportedBinary, body)
			_, err := fixture.adapter.Run(context.Background(), fixture.request)
			failure, ok := port.AsAdapterFailure(err)
			if !ok || failure.Adapter != port.AdapterIDQwen || failure.Kind != test.kind || failure.Disposition != test.disposition {
				t.Fatalf("err = %v, want qwen %s/%s", err, test.kind, test.disposition)
			}
			if test.kind == port.FailureKindProtocolInvalid && !errors.Is(err, ErrProtocol) {
				t.Fatalf("protocol-invalid must keep ErrProtocol identity: %v", err)
			}
			for _, fragment := range []string{"Mystery", "mystery"} {
				if strings.Contains(err.Error(), fragment) {
					t.Fatalf("error echoed provider-controlled signal %q: %v", fragment, err)
				}
			}
			metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
			if metaErr != nil || !strings.Contains(string(metadata), `"failureKind": "`+string(test.kind)+`"`) {
				t.Fatalf("metadata = %s err=%v, want %s projection", metadata, metaErr, test.kind)
			}
		})
	}
}

// TestRunCancellationConflictReturnsDeterministically 是 R5 P1 之一：
// structured terminal + cancel + 进程组 SIGKILL 之后，stdout/stderr capture
// 与 Wait 必须在固定窗口确定性结束，终止/进程冲突优先于 context canceled，
// signal/contextError metadata 保留。重复压力逐次验证确定性。
func TestRunCancellationConflictReturnsDeterministically(t *testing.T) {
	for iteration := 0; iteration < conflictRepetitions; iteration++ {
		ready := filepath.Join(t.TempDir(), "ready")
		terminal := `{"type":"error","error":{"code":"ENOTFOUND"}}`
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			terminalLine(terminal),
			// Keep one background fd holder while replacing the top-level shell
			// with sleep. The process-group kill must therefore converge both
			// processes and Wait must observe SIGKILL rather than a shell-specific
			// zero exit from the wait builtin.
			"sleep 30 &",
			"touch " + shellQuote(ready),
			"exec sleep 30",
		}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := fixture.adapter.Run(ctx, fixture.requestWith(map[string]any{"attemptTimeoutSeconds": 30}))
			done <- err
		}()
		waitForFile(t, ready)
		cancel()
		started := time.Now()
		select {
		case err := <-done:
			if time.Since(started) > 5*time.Second {
				t.Fatalf("iteration %d: Run exceeded the fixed convergence window", iteration)
			}
			failure, ok := port.AsAdapterFailure(err)
			if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry || !errors.Is(err, ErrProtocol) {
				t.Fatalf("iteration %d: err = %v, want deterministic conflict protocol-invalid", iteration, err)
			}
			if errors.Is(err, context.Canceled) {
				t.Fatalf("iteration %d: conflict must not degrade to context.Canceled", iteration)
			}
			metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
			if metaErr != nil {
				t.Fatal(metaErr)
			}
			for _, want := range []string{`"contextError": "context canceled"`, `"signal": "killed"`, `"failureKind": "protocol-invalid"`, `"retryDisposition": "do-not-retry"`} {
				if !strings.Contains(string(metadata), want) {
					t.Fatalf("iteration %d: metadata missing %s: %s", iteration, want, metadata)
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: Run did not converge after cancellation", iteration)
		}
		cancel()
	}
}

// TestResolveAttemptFailureCancellationConflictDoesNotDependOnWaitError
// freezes the macOS race where a canceled shell can be reaped with waitErr=nil.
// The frozen context outcome remains an independent terminal authority, so a
// structured provider failure cannot become retryable merely because Wait's
// platform-specific projection reported exit 0.
func TestResolveAttemptFailureCancellationConflictDoesNotDependOnWaitError(t *testing.T) {
	terminal := newQwenFailure(port.FailureKindDNSFailure, "", nil, nil, time.Now())
	err := resolveAttemptFailure(
		captureResult{terminalFailure: terminal},
		nil,
		nil,
		context.Canceled,
		workerRequest{},
		0,
		time.Now(),
	)
	failure, ok := port.AsAdapterFailure(err)
	if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry || !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v, want cancellation conflict protocol-invalid independent of waitErr", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("conflict must not degrade to context.Canceled: %v", err)
	}
}

// TestRunContextCannotMaskTerminalConflict 是 R5 P1 之一：context 取消永远
// 不得掩盖结构化终止失败。无论取消与进程自然退出的时序如何，返回值必须是
// typed AdapterFailure 而不是 context.Canceled。
func TestRunContextCannotMaskTerminalConflict(t *testing.T) {
	terminal := `{"type":"error","error":{"code":"ENOTFOUND"}}`
	for iteration := 0; iteration < conflictRepetitions; iteration++ {
		ready := filepath.Join(t.TempDir(), "ready")
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			terminalLine(terminal),
			"touch " + shellQuote(ready),
		}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := fixture.adapter.Run(ctx, fixture.requestWith(map[string]any{"attemptTimeoutSeconds": 30}))
			done <- err
		}()
		waitForFile(t, ready)
		cancel()
		select {
		case err := <-done:
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("iteration %d: context masked the structured terminal failure: %v", iteration, err)
			}
			failure, ok := port.AsAdapterFailure(err)
			if !ok || failure.Adapter != port.AdapterIDQwen {
				t.Fatalf("iteration %d: err = %v, want typed qwen failure", iteration, err)
			}
			// 取消与进程退出的时序决定 dns 分类或冲突升级，但两者都必须是
			// typed 失败而不是 context 错误。
			if failure.Kind != port.FailureKindDNSFailure && failure.Kind != port.FailureKindProtocolInvalid {
				t.Fatalf("iteration %d: unexpected kind %s", iteration, failure.Kind)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: Run did not return after cancellation", iteration)
		}
		cancel()
	}
}

// TestRunTerminalConflictBeatsContextDeadline 是 R5 P1 之一：Run context 的
// deadline exceeded 同样不得掩盖 terminal/process 冲突；测试先以 ready
// barrier 证明 terminal 已写入，再触发可控 deadline，在固定窗口内确定性
// 收敛并保留 contextError/signal metadata。
func TestRunTerminalConflictBeatsContextDeadline(t *testing.T) {
	for iteration := 0; iteration < conflictRepetitions/2; iteration++ {
		ready := filepath.Join(t.TempDir(), "ready")
		terminal := `{"type":"error","error":{"code":"ResourceExhausted"}}`
		body := strings.Join([]string{
			initEvent("session-1", supportedBinary),
			terminalLine(terminal),
			"touch " + shellQuote(ready),
			// exec 保证 Adapter 等待的顶层进程直接承受 SIGKILL；否则 shell
			// 可能把子进程信号转译为普通 exitCode 137 并丢失 signal metadata。
			"exec sleep 30",
		}, "\n")
		fixture := newRunFixture(t, supportedBinary, body)
		ctx := newControlledDeadlineContext()
		t.Cleanup(ctx.expire)
		done := make(chan error, 1)
		go func() {
			_, err := fixture.adapter.Run(ctx, fixture.requestWith(map[string]any{"attemptTimeoutSeconds": 30}))
			done <- err
		}()
		// ready 只会在完整 terminal JSONL 写入成功后建立；随后触发的
		// DeadlineExceeded 因而只能与已经形成的 terminal failure 竞争。
		waitForFile(t, ready)
		started := time.Now()
		ctx.expire()
		var err error
		select {
		case err = <-done:
			if time.Since(started) > 5*time.Second {
				t.Fatalf("iteration %d: Run exceeded the fixed convergence window", iteration)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: Run did not converge after controlled deadline", iteration)
		}
		failure, ok := port.AsAdapterFailure(err)
		if !ok || failure.Kind != port.FailureKindProtocolInvalid || failure.Disposition != port.RetryDispositionDoNotRetry {
			t.Fatalf("iteration %d: err = %v, want conflict protocol-invalid", iteration, err)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("iteration %d: deadline masked the terminal conflict", iteration)
		}
		metadata, metaErr := os.ReadFile(filepath.Join(fixture.controlRoot, "output", "qwen-transcript-meta.json"))
		if metaErr != nil {
			t.Fatal(metaErr)
		}
		for _, want := range []string{`"contextError": "context deadline exceeded"`, `"signal": "killed"`, `"failureKind": "protocol-invalid"`} {
			if !strings.Contains(string(metadata), want) {
				t.Fatalf("iteration %d: metadata missing %s: %s", iteration, want, metadata)
			}
		}
	}
}
