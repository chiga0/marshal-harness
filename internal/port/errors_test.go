package port

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// contractNow 是契约测试共用的固定参考时间，保证 hint 验证可确定性重放。
var contractNow = time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)

func durationPtr(value time.Duration) *time.Duration { return &value }
func timePtr(value time.Time) *time.Time             { return &value }

// TestAdapterFailureContract 冻结封闭枚举、唯一配对、构造器拒绝规则与
// Error 固定词汇。
func TestAdapterFailureContract(t *testing.T) {
	t.Run("closed-enums-are-frozen", func(t *testing.T) {
		wantKinds := []FailureKind{
			FailureKindQuotaExhausted, FailureKindRateLimited, FailureKindDNSFailure,
			FailureKindConnectionFailure, FailureKindProtocolInvalid, FailureKindResultMissing,
			FailureKindProviderTerminal,
		}
		if len(FailureKinds) != len(wantKinds) {
			t.Fatalf("FailureKinds = %v", FailureKinds)
		}
		for index, kind := range wantKinds {
			if FailureKinds[index] != kind {
				t.Fatalf("FailureKinds drifted: %v", FailureKinds)
			}
		}
		wantDispositions := []RetryDisposition{RetryDispositionRetryable, RetryDispositionBlocked, RetryDispositionDoNotRetry}
		if len(RetryDispositions) != len(wantDispositions) {
			t.Fatalf("RetryDispositions = %v", RetryDispositions)
		}
		for index, disposition := range wantDispositions {
			if RetryDispositions[index] != disposition {
				t.Fatalf("RetryDispositions drifted: %v", RetryDispositions)
			}
		}
		wantAdapters := []AdapterID{AdapterIDQwen, AdapterIDPi, AdapterIDOpenCode, AdapterIDFake, AdapterIDCodex, AdapterIDQoder}
		if len(AdapterIDs) != len(wantAdapters) {
			t.Fatalf("AdapterIDs = %v", AdapterIDs)
		}
		for index, adapter := range wantAdapters {
			if AdapterIDs[index] != adapter {
				t.Fatalf("AdapterIDs drifted: %v", AdapterIDs)
			}
		}
	})

	t.Run("unique-kind-disposition-pairing", func(t *testing.T) {
		want := map[FailureKind]RetryDisposition{
			FailureKindQuotaExhausted:    RetryDispositionBlocked,
			FailureKindRateLimited:       RetryDispositionRetryable,
			FailureKindDNSFailure:        RetryDispositionRetryable,
			FailureKindConnectionFailure: RetryDispositionRetryable,
			FailureKindResultMissing:     RetryDispositionDoNotRetry,
			FailureKindProtocolInvalid:   RetryDispositionDoNotRetry,
			FailureKindProviderTerminal:  RetryDispositionDoNotRetry,
		}
		for _, kind := range FailureKinds {
			disposition, ok := DispositionFor(kind)
			if !ok || disposition != want[kind] {
				t.Fatalf("DispositionFor(%s) = %s/%t, want %s", kind, disposition, ok, want[kind])
			}
			failure, err := NewAdapterFailure(AdapterIDQwen, kind, disposition, nil, nil, contractNow)
			if err != nil {
				t.Fatalf("legal pairing %s/%s rejected: %v", kind, disposition, err)
			}
			if failure.Adapter != AdapterIDQwen || failure.Kind != kind || failure.Disposition != disposition {
				t.Fatalf("failure = %+v", failure)
			}
			if failure.RetryAfter != 0 || !failure.NotBefore.IsZero() {
				t.Fatalf("failure must carry no hints: %+v", failure)
			}
		}
		if _, ok := DispositionFor(FailureKind("bogus")); ok {
			t.Fatal("unknown kind must have no disposition")
		}
	})

	t.Run("constructor-rejects-invalid-pairings", func(t *testing.T) {
		for _, kind := range FailureKinds {
			legal, _ := DispositionFor(kind)
			for _, disposition := range RetryDispositions {
				if disposition == legal {
					continue
				}
				if _, err := NewAdapterFailure(AdapterIDQwen, kind, disposition, nil, nil, contractNow); err == nil || !strings.Contains(err.Error(), "disagree") {
					t.Fatalf("pairing %s/%s err = %v, want pairing rejection", kind, disposition, err)
				}
			}
		}
	})

	t.Run("constructor-rejects-unknown-enums", func(t *testing.T) {
		if _, err := NewAdapterFailure(AdapterID("claude"), FailureKindRateLimited, RetryDispositionRetryable, nil, nil, contractNow); err == nil || !strings.Contains(err.Error(), "unknown adapter id") {
			t.Fatalf("err = %v, want unknown adapter rejection", err)
		}
		if _, err := NewAdapterFailure(AdapterIDQwen, FailureKind("weird"), RetryDispositionRetryable, nil, nil, contractNow); err == nil || !strings.Contains(err.Error(), "unknown failure kind") {
			t.Fatalf("err = %v, want unknown kind rejection", err)
		}
		if _, err := NewAdapterFailure(AdapterIDQwen, FailureKindRateLimited, RetryDisposition("sometimes"), nil, nil, contractNow); err == nil || !strings.Contains(err.Error(), "unknown retry disposition") {
			t.Fatalf("err = %v, want unknown disposition rejection", err)
		}
		if _, err := NewAdapterFailure(AdapterIDQwen, FailureKindRateLimited, RetryDispositionRetryable, nil, nil, time.Time{}); err == nil || !strings.Contains(err.Error(), "reference time") {
			t.Fatalf("err = %v, want zero reference time rejection", err)
		}
	})

	t.Run("error-text-is-fixed-vocabulary", func(t *testing.T) {
		failure, err := NewAdapterFailure(AdapterIDQwen, FailureKindQuotaExhausted, RetryDispositionBlocked, nil, nil, contractNow)
		if err != nil {
			t.Fatal(err)
		}
		want := "adapter qwen provider failure quota-exhausted/blocked"
		if failure.Error() != want {
			t.Fatalf("Error() = %q, want %q", failure.Error(), want)
		}
		// 构造器不存在自由文本通道：任何 provider 输入都无法进入 Error()。
		for _, fragment := range []string{"sk-secret", "Bearer", "http", "/Users", "session", "stderr", "请求"} {
			if strings.Contains(failure.Error(), fragment) {
				t.Fatalf("Error() must stay fixed vocabulary: %q", failure.Error())
			}
		}
	})

	t.Run("errors-as-helper-walks-wrapped-chains", func(t *testing.T) {
		failure, err := NewAdapterFailure(AdapterIDPi, FailureKindDNSFailure, RetryDispositionRetryable, nil, nil, contractNow)
		if err != nil {
			t.Fatal(err)
		}
		wrapped := fmt.Errorf("outer wrap: %w", fmt.Errorf("inner wrap: %w", failure))
		got, ok := AsAdapterFailure(wrapped)
		if !ok || got != failure {
			t.Fatalf("AsAdapterFailure = %+v/%t, want %+v", got, ok, failure)
		}
		if _, ok := AsAdapterFailure(errors.New("plain")); ok {
			t.Fatal("plain error must not surface as AdapterFailure")
		}
		if _, ok := AsAdapterFailure(nil); ok {
			t.Fatal("nil error must not surface as AdapterFailure")
		}
	})
}

type projectedAdapterFailureError struct{ failure AdapterFailure }

func (e projectedAdapterFailureError) Error() string { return "free-text projection" }

func (e projectedAdapterFailureError) As(target any) bool {
	pointer, ok := target.(*AdapterFailure)
	if ok {
		*pointer = e.failure
	}
	return ok
}

type cyclicAdapterFailureError struct{}

func (*cyclicAdapterFailureError) Error() string   { return "cyclic typed graph" }
func (e *cyclicAdapterFailureError) Unwrap() error { return e }

func TestNormalizeAdapterFailureRejectsForgedAndAmbiguousCarriers(t *testing.T) {
	valid, err := NewAdapterFailure(AdapterIDQwen, FailureKindConnectionFailure, RetryDispositionRetryable, durationPtr(time.Minute), nil, contractNow)
	if err != nil {
		t.Fatal(err)
	}
	normalized, found, err := NormalizeAdapterFailure(fmt.Errorf("fixed wrapper: %w", valid), contractNow)
	if err != nil || !found || normalized != valid || normalized.Error() != valid.Error() {
		t.Fatalf("normalized = %+v found=%t err=%v", normalized, found, err)
	}
	if _, found, err := NormalizeAdapterFailure(errors.New("plain"), contractNow); found || err != nil {
		t.Fatalf("plain error = found=%t err=%v", found, err)
	}

	for _, test := range []struct {
		name    string
		failure AdapterFailure
	}{
		{name: "unknown adapter", failure: AdapterFailure{Adapter: AdapterID("bad\nfree-text"), Kind: FailureKindProtocolInvalid, Disposition: RetryDispositionDoNotRetry}},
		{name: "unknown kind", failure: AdapterFailure{Adapter: AdapterIDQwen, Kind: FailureKind("bad\nfree-text"), Disposition: RetryDispositionDoNotRetry}},
		{name: "invalid pair", failure: AdapterFailure{Adapter: AdapterIDQwen, Kind: FailureKindProtocolInvalid, Disposition: RetryDispositionRetryable}},
		{name: "negative hint", failure: AdapterFailure{Adapter: AdapterIDQwen, Kind: FailureKindRateLimited, Disposition: RetryDispositionRetryable, RetryAfter: -time.Second}},
		{name: "over-bound hint", failure: AdapterFailure{Adapter: AdapterIDQwen, Kind: FailureKindRateLimited, Disposition: RetryDispositionRetryable, RetryAfter: MaxRetryHintWindow + time.Second}},
		{name: "past not-before", failure: AdapterFailure{Adapter: AdapterIDQwen, Kind: FailureKindRateLimited, Disposition: RetryDispositionRetryable, NotBefore: contractNow.Add(-time.Second)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, found, err := NormalizeAdapterFailure(test.failure, contractNow); !found || err == nil {
				t.Fatalf("forged carrier accepted: found=%t err=%v", found, err)
			}
		})
	}

	second, err := NewAdapterFailure(AdapterIDQoder, FailureKindProtocolInvalid, RetryDispositionDoNotRetry, nil, nil, contractNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := NormalizeAdapterFailure(errors.Join(valid, second), contractNow); !found || err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous carriers accepted: found=%t err=%v", found, err)
	}
	if _, found, err := NormalizeAdapterFailure(projectedAdapterFailureError{failure: valid}, contractNow); !found || err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("As-only projection accepted: found=%t err=%v", found, err)
	}
	var nilCarrier *AdapterFailure
	if _, found, err := NormalizeAdapterFailure(nilCarrier, contractNow); !found || err == nil || !strings.Contains(err.Error(), "nil typed carrier") {
		t.Fatalf("nil carrier accepted: found=%t err=%v", found, err)
	}
	if _, found, err := NormalizeAdapterFailure(&cyclicAdapterFailureError{}, contractNow); !found || err == nil || !strings.Contains(err.Error(), "bounded node limit") {
		t.Fatalf("cyclic carrier graph accepted: found=%t err=%v", found, err)
	}
}

// TestAdapterFailureHintsStayBounded 冻结 hint 验证：冲突、零、负、过去、
// 溢出与超过 24h 全部拒绝；合法 hint 只以整数秒与 RFC3339 时间进入 Error()。
func TestAdapterFailureHintsStayBounded(t *testing.T) {
	t.Run("conflicting-hints-rejected", func(t *testing.T) {
		retryAfter := 30 * time.Second
		notBefore := contractNow.Add(time.Minute)
		if _, err := NewAdapterFailure(AdapterIDQwen, FailureKindRateLimited, RetryDispositionRetryable, &retryAfter, &notBefore, contractNow); err == nil || !strings.Contains(err.Error(), "conflict") {
			t.Fatalf("err = %v, want hint conflict rejection", err)
		}
	})

	t.Run("retry-after-bounds", func(t *testing.T) {
		for name, value := range map[string]time.Duration{
			"zero":     0,
			"negative": -time.Second,
			"over-24h": MaxRetryHintWindow + time.Second,
			"48h":      2 * MaxRetryHintWindow,
		} {
			if _, err := NewAdapterFailure(AdapterIDQwen, FailureKindRateLimited, RetryDispositionRetryable, durationPtr(value), nil, contractNow); err == nil {
				t.Fatalf("retry-after %s (%s) must be rejected", name, value)
			}
		}
		legal := MaxRetryHintWindow
		failure, err := NewAdapterFailure(AdapterIDQwen, FailureKindRateLimited, RetryDispositionRetryable, &legal, nil, contractNow)
		if err != nil {
			t.Fatalf("24h boundary must stay legal: %v", err)
		}
		if failure.RetryAfter != MaxRetryHintWindow {
			t.Fatalf("RetryAfter = %s", failure.RetryAfter)
		}
	})

	t.Run("not-before-bounds", func(t *testing.T) {
		for name, value := range map[string]time.Time{
			"zero":     {},
			"past":     contractNow.Add(-time.Second),
			"now":      contractNow,
			"over-24h": contractNow.Add(MaxRetryHintWindow + time.Second),
		} {
			if _, err := NewAdapterFailure(AdapterIDQwen, FailureKindRateLimited, RetryDispositionRetryable, nil, timePtr(value), contractNow); err == nil {
				t.Fatalf("not-before %s (%s) must be rejected", name, value)
			}
		}
		legal := contractNow.Add(MaxRetryHintWindow)
		failure, err := NewAdapterFailure(AdapterIDQwen, FailureKindRateLimited, RetryDispositionRetryable, nil, &legal, contractNow)
		if err != nil {
			t.Fatalf("24h boundary must stay legal: %v", err)
		}
		if !failure.NotBefore.Equal(legal) {
			t.Fatalf("NotBefore = %s", failure.NotBefore)
		}
	})

	t.Run("safe-hint-rendering", func(t *testing.T) {
		retryAfter := 90 * time.Second
		failure, err := NewAdapterFailure(AdapterIDQwen, FailureKindQuotaExhausted, RetryDispositionBlocked, &retryAfter, nil, contractNow)
		if err != nil {
			t.Fatal(err)
		}
		want := "adapter qwen provider failure quota-exhausted/blocked retry-after=90s"
		if failure.Error() != want {
			t.Fatalf("Error() = %q, want %q", failure.Error(), want)
		}
		notBefore := time.Date(2026, 8, 17, 14, 30, 0, 0, time.FixedZone("offset", 8*3600))
		failure, err = NewAdapterFailure(AdapterIDQwen, FailureKindRateLimited, RetryDispositionRetryable, nil, &notBefore, contractNow)
		if err != nil {
			t.Fatal(err)
		}
		want = "adapter qwen provider failure rate-limited/retryable not-before=2026-08-17T06:30:00Z"
		if failure.Error() != want {
			t.Fatalf("Error() = %q, want UTC RFC3339 rendering %q", failure.Error(), want)
		}
	})
}

// TestPermanentCompatibility 冻结 Permanent/Permanentf/IsPermanent 的既有
// 语义，并验证 do-not-retry 的 typed 失败通过同一接口被视为永久失败。
func TestPermanentCompatibility(t *testing.T) {
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) must stay nil")
	}
	base := errors.New("base failure")
	wrapped := Permanent(fmt.Errorf("wrap: %w", base))
	if !IsPermanent(wrapped) {
		t.Fatal("wrapped permanent error must stay permanent")
	}
	if !errors.Is(wrapped, base) {
		t.Fatal("Permanent must preserve the wrapped chain")
	}
	if IsPermanent(base) {
		t.Fatal("plain error must not be permanent")
	}
	permanentf := Permanentf("frozen %s", "message")
	if !IsPermanent(permanentf) || permanentf.Error() != "frozen message" {
		t.Fatalf("Permanentf = %v", permanentf)
	}

	t.Run("typed-failures-follow-disposition", func(t *testing.T) {
		for _, kind := range FailureKinds {
			disposition, _ := DispositionFor(kind)
			failure, err := NewAdapterFailure(AdapterIDQwen, kind, disposition, nil, nil, contractNow)
			if err != nil {
				t.Fatal(err)
			}
			wantPermanent := disposition == RetryDispositionDoNotRetry
			if failure.Permanent() != wantPermanent {
				t.Fatalf("%s Permanent() = %t, want %t", kind, failure.Permanent(), wantPermanent)
			}
			wrappedTyped := fmt.Errorf("attempt failed: %w", failure)
			if IsPermanent(wrappedTyped) != wantPermanent {
				t.Fatalf("IsPermanent(%s) = %t, want %t", kind, IsPermanent(wrappedTyped), wantPermanent)
			}
			got, ok := AsAdapterFailure(wrappedTyped)
			if !ok || got != failure {
				t.Fatalf("AsAdapterFailure through wrap = %+v/%t", got, ok)
			}
		}
	})
}
