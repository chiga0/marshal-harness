package resultingress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/gofrs/flock"
)

// resultIngressStoreFileName 是 ResultIngress replay/quarantine/idempotency 的
// append-only 账本文件名（R2 纵切）。
const (
	resultIngressStoreFileName    = "result-ingress.jsonl"
	resultIngressStoreLockName    = "result-ingress.lock"
	resultIngressProtocolRevision = "result-ingress/v2"
)

// ErrMemoryOnlyResultIngress 在账本未绑定耐久目录时返回：memory-only 的
// ResultIngress replay authority 在生产权威路径中不被接受（与 lease / agent
// registry 耐久约束一致）。
var ErrMemoryOnlyResultIngress = errors.New("resultingress: memory-only result ingress store not allowed: the store is not bound to a durable directory")

// ErrLegacyAdmissionMutationDisabled marks the former ungoverned result
// mutation API as replay-only. Production admission must flow through Ingress
// so it is chained to an exact Attempt authority head.
var ErrLegacyAdmissionMutationDisabled = errors.New("resultingress: legacy ungoverned result admission mutation is disabled")

const (
	resultFactTypeAdmitted    = "result-admitted"
	resultFactTypeQuarantined = "result-quarantined"
)

// resultAdmittedFact 是一条 result-admitted 账本事实：一次成功接纳的幂等
// 权威锚点，包含 replay 检测所需的原始 idempotencyKey 与 envelope digest。内容
// 从不改写，只追加。
type resultAdmittedFact struct {
	ProtocolRevision    string       `json:"protocolRevision"`
	FactType            string       `json:"factType"`
	Sequence            int64        `json:"sequence"`
	IdempotencyKey      string       `json:"idempotencyKey"`
	AttemptKey          string       `json:"attemptKey,omitempty"`
	AttemptRevision     uint64       `json:"attemptRevision,omitempty"`
	PreviousAttemptHead string       `json:"previousAttemptHead,omitempty"`
	DRCDigest           string       `json:"drcDigest"`
	EnvelopeKind        EnvelopeKind `json:"envelopeKind"`
	EnvelopeSequence    uint64       `json:"envelopeSequence"`
	EnvelopeDigest      string       `json:"envelopeDigest"`
	FactDigest          string       `json:"envelopeFactDigest"`
	LedgerSequence      uint64       `json:"ledgerSequence"`
	Digest              string       `json:"digest"`
}

// legacyResultAdmittedFactV1 is the exact unversioned format written before
// result-ingress/v2. It intentionally has no DRC/kind/envelope sequence. Such
// a fact remains part of monotonic history but can never authorize replay: the
// missing authority fields are not synthesized during migration.
type legacyResultAdmittedFactV1 struct {
	FactType       string `json:"factType"`
	Sequence       int64  `json:"sequence"`
	IdempotencyKey string `json:"idempotencyKey"`
	EnvelopeDigest string `json:"envelopeDigest"`
	FactDigest     string `json:"envelopeFactDigest"`
	LedgerSequence uint64 `json:"ledgerSequence"`
	Digest         string `json:"digest"`
}

// resultQuarantinedFact 是一条 result-quarantined 账本事实：一份被拒绝的
// 投递（只读机械审计，不参与业务派生）。内容从不改写，只追加。
type resultQuarantinedFact struct {
	ProtocolRevision string          `json:"protocolRevision"`
	FactType         string          `json:"factType"`
	Sequence         int64           `json:"sequence"`
	Reason           RejectionReason `json:"reason"`
	DRCDigest        string          `json:"drcDigest"`
	EnvelopeDigest   string          `json:"envelopeDigest"`
	ObservedAt       string          `json:"observedAt"`
	Digest           string          `json:"digest"`
}

type legacyResultQuarantinedFactV1 struct {
	FactType       string          `json:"factType"`
	Sequence       int64           `json:"sequence"`
	Reason         RejectionReason `json:"reason"`
	DRCDigest      string          `json:"drcDigest"`
	EnvelopeDigest string          `json:"envelopeDigest"`
	ObservedAt     string          `json:"observedAt"`
	Digest         string          `json:"digest"`
}

// DurableStore 是 ResultIngress 与 Attempt Authority 共用的唯一物理
// append-only 耐久账本（R2/RB1 纵切）。导出名称允许 production
// composition 持有同一 store，而不是为结果和 Attempt 生命周期各造一份
// authority。崩溃/重启由 OpenResultIngressStore 确定性重放全部投影。
type DurableStore struct {
	dir          string
	nextSequence int64
	mu           sync.Mutex
	clock        func() time.Time
}

var processEffectFlights sync.Map

// ingressDurableStore keeps existing package-internal call sites source
// compatible while DurableStore is the public composition type.
type ingressDurableStore = DurableStore

// OpenResultIngressStore 打开（不存在则创建）耐久账本目录并重放全部 fact。
// 空白目录保持可构造但所有写操作 fail closed（ErrMemoryOnlyResultIngress）。
// 损坏/非规范/冲突行一律 fail closed，绝不静默跳过。
func OpenResultIngressStore(dir string) (*DurableStore, error) {
	return openResultIngressStoreWithClock(dir, time.Now)
}

// openResultIngressStoreWithClock injects the canonical authority clock for
// deterministic deadline tests without creating a caller-controlled deadline
// verdict in the public production API.
func openResultIngressStoreWithClock(dir string, clock func() time.Time) (*DurableStore, error) {
	if clock == nil {
		return nil, errors.New("resultingress: authority clock is required")
	}
	store := &DurableStore{clock: clock}
	if strings.TrimSpace(dir) == "" {
		return store, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("resultingress: create result ingress store directory: %w", err)
	}
	store.dir = dir
	store.nextSequence = 1
	return store, nil
}

func (s *ingressDurableStore) authorityNow() time.Time {
	if s != nil && s.clock != nil {
		return s.clock().UTC()
	}
	return time.Now().UTC()
}

func (s *ingressDurableStore) withEffectFlight(key string, fn func() error) error {
	root, err := filepath.Abs(s.dir)
	if err != nil {
		return fmt.Errorf("resultingress: resolve effect authority root: %w", err)
	}
	flightKey := filepath.Clean(root) + "\x00" + key
	value, _ := processEffectFlights.LoadOrStore(flightKey, &sync.Mutex{})
	flight := value.(*sync.Mutex)
	flight.Lock()
	defer flight.Unlock()
	return fn()
}

func (s *ingressDurableStore) requireBound() error {
	if s == nil || s.dir == "" {
		return ErrMemoryOnlyResultIngress
	}
	return nil
}

func (s *ingressDurableStore) ledgerPath() string {
	return filepath.Join(s.dir, resultIngressStoreFileName)
}

func (s *ingressDurableStore) lockPath() string {
	return filepath.Join(s.dir, resultIngressStoreLockName)
}

// withExclusive serializes recovery, replay/CAS and append across every store
// instance and process that shares this state root. The in-process mutex avoids
// flock's process-scoped reentrancy from admitting two goroutines at once.
func (s *ingressDurableStore) withExclusive(fn func() error) error {
	if err := s.requireBound(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	coordination := flock.New(s.lockPath())
	if err := coordination.Lock(); err != nil {
		return fmt.Errorf("resultingress: acquire durable ledger lock: %w", err)
	}
	defer func() { _ = coordination.Unlock() }()
	return fn()
}

func (s *ingressDurableStore) transact(in *Ingress, fn func() error) error {
	return s.withExclusive(func() error {
		in.resetDurableReplayState()
		s.nextSequence = 1
		if err := s.recoverIntoLocked(in); err != nil {
			return err
		}
		return fn()
	})
}

// appendLine 给 fact 计算 detached digest、canonical 化并追加一行 + sync。
func (s *ingressDurableStore) appendLine(fact any, getDigest func() string, setDigest func(string)) error {
	if err := s.requireBound(); err != nil {
		return err
	}
	if getDigest() != "" {
		return fmt.Errorf("resultingress: fact digest must be detached before sealing")
	}
	rawJSON, err := json.Marshal(fact)
	if err != nil {
		return fmt.Errorf("resultingress: marshal fact: %w", err)
	}
	digest, err := drcLessDigest(rawJSON)
	if err != nil {
		return err
	}
	setDigest(digest)
	rawJSON, err = json.Marshal(fact)
	if err != nil {
		return err
	}
	raw, err := canonical.JSON(rawJSON)
	if err != nil {
		return fmt.Errorf("resultingress: canonicalize fact: %w", err)
	}
	line := append(raw, '\n')
	f, err := os.OpenFile(s.ledgerPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("resultingress: open ledger: %w", err)
	}
	defer f.Close()
	if n, err := f.Write(line); err != nil {
		return fmt.Errorf("resultingress: append fact: %w", err)
	} else if n != len(line) {
		return fmt.Errorf("resultingress: append fact: short write %d/%d", n, len(line))
	}
	if err := f.Sync(); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// recordAdmittedLocked persists admission only for the governed Ingress path;
// it is never a public authority bypass.
func (s *ingressDurableStore) recordAdmittedLocked(idempotencyKey string, governed *AttemptAuthorityState, drcDigest string, envelope ResultEnvelope, factDigest string, ledgerSequence uint64) (string, error) {
	fact := &resultAdmittedFact{
		ProtocolRevision: resultIngressProtocolRevision,
		FactType:         resultFactTypeAdmitted,
		Sequence:         s.nextSequence,
		IdempotencyKey:   idempotencyKey,
		DRCDigest:        drcDigest,
		EnvelopeKind:     envelope.Kind,
		EnvelopeSequence: envelope.Sequence,
		EnvelopeDigest:   envelope.ResultDigest,
		FactDigest:       factDigest,
		LedgerSequence:   ledgerSequence,
	}
	if governed != nil {
		key, err := governed.Identity.Key()
		if err != nil {
			return "", err
		}
		if governed.ProcessStartedDigest == "" || governed.BarrierDigest != "" || governed.PendingEffectIntentFactDigest != "" {
			return "", ErrAttemptAuthorityOrder
		}
		fact.AttemptKey = key
		fact.AttemptRevision = governed.Revision + 1
		fact.PreviousAttemptHead = governed.HeadDigest
	}
	if err := s.appendLine(fact,
		func() string { return fact.Digest },
		func(d string) { fact.Digest = d }); err != nil {
		return "", err
	}
	s.nextSequence++
	return fact.Digest, nil
}

// RecordAdmitted is retained only for source compatibility with legacy replay
// callers. It never mutates a bound or memory-only store.
func (s *ingressDurableStore) RecordAdmitted(idempotencyKey, drcDigest string, envelope ResultEnvelope, factDigest string, ledgerSequence uint64) error {
	if err := s.requireBound(); err != nil {
		return err
	}
	return ErrLegacyAdmissionMutationDisabled
}

// RecordQuarantined 把一次拒绝投递的只读机械审计记录持久化。
func (s *ingressDurableStore) RecordQuarantined(reason RejectionReason, drcDigest, envelopeDigest string, observedAt time.Time) error {
	return s.withExclusive(func() error {
		dummy := newAuthorityProjection()
		s.nextSequence = 1
		if err := s.recoverIntoLocked(dummy); err != nil {
			return err
		}
		return s.recordQuarantinedLocked(reason, drcDigest, envelopeDigest, observedAt)
	})
}

func (s *ingressDurableStore) recordQuarantinedLocked(reason RejectionReason, drcDigest, envelopeDigest string, observedAt time.Time) error {
	fact := &resultQuarantinedFact{
		ProtocolRevision: resultIngressProtocolRevision,
		FactType:         resultFactTypeQuarantined,
		Sequence:         s.nextSequence,
		Reason:           reason,
		DRCDigest:        drcDigest,
		EnvelopeDigest:   envelopeDigest,
		ObservedAt:       observedAt.UTC().Format(time.RFC3339),
	}
	if err := s.appendLine(fact,
		func() string { return fact.Digest },
		func(d string) { fact.Digest = d }); err != nil {
		return err
	}
	s.nextSequence++
	return nil
}

// recover 重放账本并把 admitted map / ledgerSequence / quarantine 落到提供的
// Ingress 状态上。仅本包内使用（OpenDurableIngress 路径）。
func (s *ingressDurableStore) recoverInto(in *Ingress) error {
	return s.transact(in, func() error { return nil })
}

func (s *ingressDurableStore) recoverIntoLocked(in *Ingress) error {
	data, err := os.ReadFile(s.ledgerPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resultingress: read store: %w", err)
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		return errors.New("resultingress: truncated ledger tail without newline")
	}
	for i, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := s.applyLine([]byte(line), in); err != nil {
			return fmt.Errorf("resultingress: apply ledger line %d: %w", i+1, err)
		}
	}
	return nil
}

// applyLine 解析一行并落到 Ingress 状态。
func (s *ingressDurableStore) applyLine(line []byte, in *Ingress) error {
	canonicalLine, err := canonical.JSON(line)
	if err != nil || !bytes.Equal(canonicalLine, line) {
		return errors.New("resultingress: ledger line is not canonical JSON")
	}
	var head struct {
		ProtocolRevision string `json:"protocolRevision"`
		FactType         string `json:"factType"`
		Digest           string `json:"digest"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return err
	}
	switch head.FactType {
	case string(AttemptTransitionOpened), string(AttemptTransitionLaunchAuthorized), string(AttemptTransitionProcessStarted), string(AttemptTransitionTerminalizationBarrier), string(AttemptTransitionProcessTerminal), string(AttemptTransitionAllocationTerminated), string(AttemptTransitionCleanupCompleted), string(AttemptTransitionCleanupReleased):
		if err := applyAttemptAuthorityLine(line, in, s.nextSequence); err != nil {
			return err
		}
	case effectFactTypeIntent, effectFactTypeReceipt, effectFactTypeReconcile:
		if err := applyEffectAuthorityLine(line, in, s.nextSequence); err != nil {
			return err
		}
	case resultFactTypeAdmitted:
		if head.ProtocolRevision == "" {
			return s.applyLegacyAdmittedLine(line, in)
		}
		if head.ProtocolRevision != resultIngressProtocolRevision {
			return fmt.Errorf("unsupported result ingress protocol revision %q", head.ProtocolRevision)
		}
		var fact resultAdmittedFact
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&fact); err != nil {
			return err
		}
		if fact.Sequence != s.nextSequence {
			return fmt.Errorf("admitted fact sequence %d, want %d", fact.Sequence, s.nextSequence)
		}
		if err := requireDigest("DRCDigest", fact.DRCDigest); err != nil {
			return err
		}
		if strings.TrimSpace(fact.IdempotencyKey) == "" {
			return errors.New("admitted fact idempotency key is empty")
		}
		if err := requireDigest("FactDigest", fact.FactDigest); err != nil {
			return err
		}
		if err := (ResultEnvelope{Kind: fact.EnvelopeKind, ResultDigest: fact.EnvelopeDigest, Sequence: fact.EnvelopeSequence}).Validate(); err != nil {
			return err
		}
		storeddigest := fact.Digest
		fact.Digest = ""
		rawJSON, _ := json.Marshal(&fact)
		if digest, err := drcLessDigest(rawJSON); err != nil || digest != storeddigest {
			return errors.New("admitted fact digest mismatch")
		}
		fact.Digest = storeddigest
		if _, exists := in.admitted[fact.IdempotencyKey]; exists {
			return fmt.Errorf("duplicate admitted idempotency key %q", fact.IdempotencyKey)
		}
		if fact.LedgerSequence != in.ledgerSequence+1 {
			return fmt.Errorf("admitted ledger sequence %d, want %d", fact.LedgerSequence, in.ledgerSequence+1)
		}
		entry := admittedEntry{
			fact:             AdmissionFact{FactDigest: fact.FactDigest, LedgerSequence: fact.LedgerSequence, IdempotentReplay: false},
			attemptKey:       fact.AttemptKey,
			drcDigest:        fact.DRCDigest,
			envelopeKind:     fact.EnvelopeKind,
			envelopeSequence: fact.EnvelopeSequence,
			envelopeDigest:   fact.EnvelopeDigest,
		}
		if fact.AttemptKey != "" {
			if err := requireDigest("attemptKey", fact.AttemptKey); err != nil {
				return err
			}
			state, exists := in.attempts[fact.AttemptKey]
			if !exists || state.ProcessStartedDigest == "" || state.BarrierDigest != "" || state.PendingEffectIntentFactDigest != "" || fact.AttemptRevision != state.Revision+1 || fact.PreviousAttemptHead != state.HeadDigest {
				return ErrAttemptAuthorityOrder
			}
			state.Revision = fact.AttemptRevision
			state.HeadDigest = storeddigest
			if fact.EnvelopeKind == KindWorkerResult {
				if state.CommittedResultFactDigest != "" {
					return ErrAttemptAuthorityConflict
				}
				state.CommittedResultFactDigest = fact.FactDigest
				state.CommittedResultSequence = fact.LedgerSequence
			}
			in.attempts[fact.AttemptKey] = state
		} else if fact.AttemptRevision != 0 || fact.PreviousAttemptHead != "" {
			return ErrAttemptAuthorityConflict
		}
		in.admitted[fact.IdempotencyKey] = entry
		if fact.LedgerSequence > in.ledgerSequence {
			in.ledgerSequence = fact.LedgerSequence
		}
	case resultFactTypeQuarantined:
		if head.ProtocolRevision == "" {
			return s.applyLegacyQuarantinedLine(line, in)
		}
		if head.ProtocolRevision != resultIngressProtocolRevision {
			return fmt.Errorf("unsupported result ingress protocol revision %q", head.ProtocolRevision)
		}
		var fact resultQuarantinedFact
		if err := json.Unmarshal(line, &fact); err != nil {
			return err
		}
		if fact.Sequence != s.nextSequence {
			return fmt.Errorf("quarantined fact sequence %d, want %d", fact.Sequence, s.nextSequence)
		}
		storeddigest := fact.Digest
		fact.Digest = ""
		rawJSON, _ := json.Marshal(&fact)
		if digest, err := drcLessDigest(rawJSON); err != nil || digest != storeddigest {
			return errors.New("quarantined fact digest mismatch")
		}
		fact.Digest = storeddigest
		at, err := time.Parse(time.RFC3339, fact.ObservedAt)
		if err != nil {
			return fmt.Errorf("quarantined observedAt: %w", err)
		}
		in.quarantine = append(in.quarantine, QuarantineRecord{
			Reason:         fact.Reason,
			DRCDigest:      fact.DRCDigest,
			EnvelopeDigest: fact.EnvelopeDigest,
			ObservedAt:     at.UTC(),
		})
	default:
		return fmt.Errorf("unknown result ingress fact type %q", head.FactType)
	}
	s.nextSequence++
	return nil
}

func (s *ingressDurableStore) applyLegacyAdmittedLine(line []byte, in *Ingress) error {
	var fact legacyResultAdmittedFactV1
	if err := json.Unmarshal(line, &fact); err != nil {
		return err
	}
	if fact.Sequence != s.nextSequence {
		return fmt.Errorf("legacy admitted fact sequence %d, want %d", fact.Sequence, s.nextSequence)
	}
	storedDigest := fact.Digest
	fact.Digest = ""
	rawJSON, _ := json.Marshal(&fact)
	if digest, err := drcLessDigest(rawJSON); err != nil || digest != storedDigest {
		return errors.New("legacy admitted fact digest mismatch")
	}
	if _, exists := in.admitted[fact.IdempotencyKey]; exists {
		return fmt.Errorf("duplicate admitted idempotency key %q", fact.IdempotencyKey)
	}
	if strings.TrimSpace(fact.IdempotencyKey) == "" {
		return errors.New("legacy admitted fact idempotency key is empty")
	}
	if err := requireDigest("legacy EnvelopeDigest", fact.EnvelopeDigest); err != nil {
		return err
	}
	if err := requireDigest("legacy FactDigest", fact.FactDigest); err != nil {
		return err
	}
	if fact.LedgerSequence != in.ledgerSequence+1 {
		return fmt.Errorf("legacy admitted ledger sequence %d, want %d", fact.LedgerSequence, in.ledgerSequence+1)
	}
	in.admitted[fact.IdempotencyKey] = admittedEntry{
		fact:                AdmissionFact{FactDigest: fact.FactDigest, LedgerSequence: fact.LedgerSequence},
		envelopeDigest:      fact.EnvelopeDigest,
		legacyReplayBlocked: true,
	}
	in.ledgerSequence = fact.LedgerSequence
	s.nextSequence++
	return nil
}

func (s *ingressDurableStore) applyLegacyQuarantinedLine(line []byte, in *Ingress) error {
	var fact legacyResultQuarantinedFactV1
	if err := json.Unmarshal(line, &fact); err != nil {
		return err
	}
	if fact.Sequence != s.nextSequence {
		return fmt.Errorf("legacy quarantined fact sequence %d, want %d", fact.Sequence, s.nextSequence)
	}
	storedDigest := fact.Digest
	fact.Digest = ""
	rawJSON, _ := json.Marshal(&fact)
	if digest, err := drcLessDigest(rawJSON); err != nil || digest != storedDigest {
		return errors.New("legacy quarantined fact digest mismatch")
	}
	at, err := time.Parse(time.RFC3339, fact.ObservedAt)
	if err != nil {
		return fmt.Errorf("legacy quarantined observedAt: %w", err)
	}
	in.quarantine = append(in.quarantine, QuarantineRecord{Reason: fact.Reason, DRCDigest: fact.DRCDigest, EnvelopeDigest: fact.EnvelopeDigest, ObservedAt: at.UTC()})
	s.nextSequence++
	return nil
}

// drcLessDigest 返回 RFC8785 canonical JSON 的 sha256: digest（与已有实现一致：
// detached digest 不参与自身摘要）。
func drcLessDigest(rawJSON []byte) (string, error) {
	raw, err := canonical.JSON(rawJSON)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(raw), nil
}
