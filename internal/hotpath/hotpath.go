package hotpath

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	// ErrInvalidDigest 拒绝形态非法的 digest（必须 sha256: + 64 位小写 hex）。
	ErrInvalidDigest = errors.New("invalid digest")
	// ErrUnknownKind 拒绝封闭枚举之外的 envelope kind（fail closed）。
	ErrUnknownKind = errors.New("unknown envelope kind")
	// ErrUnknownChannel 拒绝封闭枚举之外的 channel（fail closed）。
	ErrUnknownChannel = errors.New("unknown channel")
	// ErrInvalidSequence 拒绝非正数的 ledger sequence。
	ErrInvalidSequence = errors.New("invalid ledger sequence")
	// ErrAdmissionConflict 拒绝同 digest 携带任何不同字段的再记录
	// （账本永不覆盖，含 hot→cold 洗白）。
	ErrAdmissionConflict = errors.New("admission conflict")
	// ErrHotPathForbidden 拒绝热路径承载冷路径专属 authority。
	ErrHotPathForbidden = errors.New("hot path forbidden")
	// ErrUnknownAdmission 拒绝账本中不存在的 digest（fail closed）。
	ErrUnknownAdmission = errors.New("unknown admission")
	// ErrWrongKind 拒绝以错误 kind 消费 admission。
	ErrWrongKind = errors.New("wrong envelope kind")
	// ErrUnknownEffect 拒绝封闭枚举之外的 authority effect（fail closed）。
	ErrUnknownEffect = errors.New("unknown authority effect")
	// ErrNilLedger 拒绝 nil 账本（fail closed）。
	ErrNilLedger = errors.New("nil admission ledger")
)

// Channel 是接纳通道的封闭枚举；未知值一律 fail closed。
type Channel string

const (
	// ChannelHot 是热路径：只允许轻量观察事实，不产生可 Restore 的
	// authority，不延长 lease，不 bump generation，不决定 fencing。
	ChannelHot Channel = "hot"
	// ChannelCold 是冷路径：完整校验后的权威接纳，唯一可承载 authority
	// 效果与 Restore 消费的通道。
	ChannelCold Channel = "cold"
)

// EnvelopeKind 是结果信封 kind 的封闭枚举（与 resultingress 相关 kind
// 对齐）；未知值一律 fail closed。
type EnvelopeKind string

const (
	KindCheckpoint   EnvelopeKind = "checkpoint"
	KindHeartbeat    EnvelopeKind = "heartbeat"
	KindLog          EnvelopeKind = "log"
	KindWorkerResult EnvelopeKind = "worker-result"
	KindCandidate    EnvelopeKind = "candidate"
	KindEvidenceRef  EnvelopeKind = "evidence-ref"
	KindReceipt      EnvelopeKind = "receipt"
	KindAssessment   EnvelopeKind = "assessment"
)

// AuthorityEffect 是 authority 效果的封闭枚举；未知值一律 fail closed。
type AuthorityEffect string

const (
	// EffectExtendLease 延长 lease；只允许落在 ChannelCold 接纳上。
	EffectExtendLease AuthorityEffect = "extend-lease"
	// EffectBumpGeneration bump generation；只允许落在 ChannelCold 接纳上。
	EffectBumpGeneration AuthorityEffect = "bump-generation"
	// EffectDecideFencing 决定 fencing；只允许落在 ChannelCold 接纳上。
	EffectDecideFencing AuthorityEffect = "decide-fencing"
	// EffectRecordObservation 记录观察事实；允许落在任意 channel 上。
	EffectRecordObservation AuthorityEffect = "record-observation"
)

// Admission 是一条已接纳结果对象的权威事实：Digest 是唯一键
// （sha256: + 64 位小写 hex），LedgerSequence 必须为正数。
type Admission struct {
	Digest         string
	Kind           EnvelopeKind
	Channel        Channel
	LedgerSequence uint64
}

// AdmissionLedger 是按 Digest put-if-absent 的接纳账本：并发安全、无
// 时钟依赖。账本只承载接纳事实与 channel 归属判定，不实现 lease、
// generation 或 fencing 本身。
type AdmissionLedger struct {
	mu       sync.Mutex
	byDigest map[string]Admission
}

// NewAdmissionLedger 构造空账本。
func NewAdmissionLedger() *AdmissionLedger {
	return &AdmissionLedger{byDigest: make(map[string]Admission)}
}

// Record 校验并记录一条 admission，fail closed：
//
//  1. 所有字段形态与封闭枚举校验（digest/kind/channel/sequence）；
//  2. channel 冻结规则在记录时执行：checkpoint/heartbeat/log 可记
//     任意 channel；业务 kind 必须 ChannelCold，落在 ChannelHot 即
//     ErrHotPathForbidden；
//  3. put-if-absent：同 digest 且内容完全一致（含 LedgerSequence）
//     幂等成功；任一字段不同即 ErrAdmissionConflict，账本永不覆盖。
func (l *AdmissionLedger) Record(a Admission) error {
	if l == nil {
		return fmt.Errorf("hotpath: %w", ErrNilLedger)
	}
	if err := validateAdmission(a); err != nil {
		return err
	}
	if a.Channel == ChannelHot && !hotPathCapable(a.Kind) {
		return fmt.Errorf("hotpath: %w: kind %q must be recorded on the cold path",
			ErrHotPathForbidden, a.Kind)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.byDigest[a.Digest]; ok {
		if existing == a {
			return nil
		}
		return fmt.Errorf("hotpath: %w: digest %q already recorded with different admission (kind %q→%q channel %q→%q sequence %d→%d)",
			ErrAdmissionConflict, a.Digest,
			existing.Kind, a.Kind, existing.Channel, a.Channel,
			existing.LedgerSequence, a.LedgerSequence)
	}
	l.byDigest[a.Digest] = a
	return nil
}

// Lookup 按 digest 读取既有 admission（只读，不产生任何 authority）。
func (l *AdmissionLedger) Lookup(digest string) (Admission, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.byDigest[digest]
	return a, ok
}

// AllowEffect 判定指定 authority effect 是否允许落在 digest 对应的
// admission 上。冻结规则：
//
//   - effect 必须是封闭枚举成员（fail closed）；
//   - digest 必须已存在于账本（否则 ErrUnknownAdmission）；
//   - record-observation 允许落在任意 channel；
//   - extend-lease / bump-generation / decide-fencing 只允许落在
//     ChannelCold 接纳上，落在 ChannelHot 即 ErrHotPathForbidden。
func AllowEffect(l *AdmissionLedger, digest string, effect AuthorityEffect) error {
	if l == nil {
		return fmt.Errorf("hotpath: %w", ErrNilLedger)
	}
	if err := effect.requireValid(); err != nil {
		return err
	}
	a, ok := l.Lookup(digest)
	if !ok {
		return fmt.Errorf("hotpath: %w: %q", ErrUnknownAdmission, digest)
	}
	if effect == EffectRecordObservation {
		return nil
	}
	if a.Channel != ChannelCold {
		return fmt.Errorf("hotpath: %w: effect %q requires the cold path, got channel %q (digest %q)",
			ErrHotPathForbidden, effect, a.Channel, digest)
	}
	return nil
}

// ConsumeForRestore 是 Restore 消费门禁：digest 必须已存在，kind 必须
// 是 checkpoint，channel 必须是 ChannelCold——热路径接纳的 checkpoint
// 不可 Restore（ErrHotPathForbidden）。门禁判定本身不消耗账本条目，
// 可幂等重查。
func ConsumeForRestore(l *AdmissionLedger, digest string) error {
	if l == nil {
		return fmt.Errorf("hotpath: %w", ErrNilLedger)
	}
	a, ok := l.Lookup(digest)
	if !ok {
		return fmt.Errorf("hotpath: %w: %q", ErrUnknownAdmission, digest)
	}
	if a.Kind != KindCheckpoint {
		return fmt.Errorf("hotpath: %w: restore requires kind %q, got %q",
			ErrWrongKind, KindCheckpoint, a.Kind)
	}
	if a.Channel != ChannelCold {
		return fmt.Errorf("hotpath: %w: hot-admitted checkpoint is not restorable (digest %q)",
			ErrHotPathForbidden, digest)
	}
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// hotPathCapable 报告 kind 是否允许落在热路径：仅 checkpoint/heartbeat/log
// （热路径接纳为轻量、不可 Restore 的观察事实）。
func hotPathCapable(k EnvelopeKind) bool {
	switch k {
	case KindCheckpoint, KindHeartbeat, KindLog:
		return true
	default:
		return false
	}
}

func validateAdmission(a Admission) error {
	if err := requireDigest(a.Digest); err != nil {
		return fmt.Errorf("hotpath: %w: %v", ErrInvalidDigest, err)
	}
	if err := a.Kind.requireValid(); err != nil {
		return err
	}
	if err := a.Channel.requireValid(); err != nil {
		return err
	}
	if a.LedgerSequence == 0 {
		return fmt.Errorf("hotpath: %w: must be positive", ErrInvalidSequence)
	}
	return nil
}

func (k EnvelopeKind) requireValid() error {
	switch k {
	case KindCheckpoint, KindHeartbeat, KindLog,
		KindWorkerResult, KindCandidate, KindEvidenceRef,
		KindReceipt, KindAssessment:
		return nil
	default:
		return fmt.Errorf("hotpath: %w: %q", ErrUnknownKind, string(k))
	}
}

func (c Channel) requireValid() error {
	switch c {
	case ChannelHot, ChannelCold:
		return nil
	default:
		return fmt.Errorf("hotpath: %w: %q", ErrUnknownChannel, string(c))
	}
}

func (e AuthorityEffect) requireValid() error {
	switch e {
	case EffectExtendLease, EffectBumpGeneration,
		EffectDecideFencing, EffectRecordObservation:
		return nil
	default:
		return fmt.Errorf("hotpath: %w: %q", ErrUnknownEffect, string(e))
	}
}

func requireDigest(value string) error {
	const prefix = "sha256:"
	if strings.TrimSpace(value) == "" {
		return errors.New("digest must not be empty")
	}
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("digest must carry the %q prefix", prefix)
	}
	hexPart := strings.TrimPrefix(value, prefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("digest must be a 64-character sha256 hex digest, got %d", len(hexPart))
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return errors.New("digest must be lowercase hex")
		}
	}
	return nil
}
