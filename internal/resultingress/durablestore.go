package resultingress

import (
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

const (
	resultFactTypeAdmitted    = "result-admitted"
	resultFactTypeQuarantined = "result-quarantined"
)

// resultAdmittedFact 是一条 result-admitted 账本事实：一次成功接纳的幂等
// 权威锚点，包含 replay 检测所需的原始 idempotencyKey 与 envelope digest。内容
// 从不改写，只追加。
type resultAdmittedFact struct {
	ProtocolRevision string       `json:"protocolRevision"`
	FactType         string       `json:"factType"`
	Sequence         int64        `json:"sequence"`
	IdempotencyKey   string       `json:"idempotencyKey"`
	DRCDigest        string       `json:"drcDigest"`
	EnvelopeKind     EnvelopeKind `json:"envelopeKind"`
	EnvelopeSequence uint64       `json:"envelopeSequence"`
	EnvelopeDigest   string       `json:"envelopeDigest"`
	FactDigest       string       `json:"envelopeFactDigest"`
	LedgerSequence   uint64       `json:"ledgerSequence"`
	Digest           string       `json:"digest"`
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

// ingressDurableStore 是 ResultIngress 的 replay/quarantine/idempotency
// append-only 耐久账本（R2 纵切）。崩溃/重启由 OpenResultIngressStore 确定性
// 重放：恢复 admitted map（idempotencyKey → {fact,envelopeDigest}）、单调
// ledgerSequence 与全部 quarantine 记录——使「同一 idempotencyKey 重复送达 /
// 跨进程重放」可被机械检测（同 digest 幂等、不同 digest 即伪造 fail closed）。
type ingressDurableStore struct {
	dir          string
	nextSequence int64
	mu           sync.Mutex
}

// OpenResultIngressStore 打开（不存在则创建）耐久账本目录并重放全部 fact。
// 空白目录保持可构造但所有写操作 fail closed（ErrMemoryOnlyResultIngress）。
// 损坏/非规范/冲突行一律 fail closed，绝不静默跳过。
func OpenResultIngressStore(dir string) (*ingressDurableStore, error) {
	if strings.TrimSpace(dir) == "" {
		return &ingressDurableStore{}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("resultingress: create result ingress store directory: %w", err)
	}
	return &ingressDurableStore{dir: dir, nextSequence: 1}, nil
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

// RecordAdmitted 把一次成功接纳的幂等权威锚点持久化（在返回 fact 之前先落账，
// 使后续重复送达或跨进程重放可被机械检测）。
func (s *ingressDurableStore) recordAdmittedLocked(idempotencyKey, drcDigest string, envelope ResultEnvelope, factDigest string, ledgerSequence uint64) error {
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
	if err := s.appendLine(fact,
		func() string { return fact.Digest },
		func(d string) { fact.Digest = d }); err != nil {
		return err
	}
	s.nextSequence++
	return nil
}

func (s *ingressDurableStore) RecordAdmitted(idempotencyKey, drcDigest string, envelope ResultEnvelope, factDigest string, ledgerSequence uint64) error {
	return s.withExclusive(func() error {
		dummy := &Ingress{admitted: make(map[string]admittedEntry)}
		s.nextSequence = 1
		if err := s.recoverIntoLocked(dummy); err != nil {
			return err
		}
		return s.recordAdmittedLocked(idempotencyKey, drcDigest, envelope, factDigest, ledgerSequence)
	})
}

// RecordQuarantined 把一次拒绝投递的只读机械审计记录持久化。
func (s *ingressDurableStore) RecordQuarantined(reason RejectionReason, drcDigest, envelopeDigest string, observedAt time.Time) error {
	return s.withExclusive(func() error {
		dummy := &Ingress{admitted: make(map[string]admittedEntry)}
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
	var head struct {
		ProtocolRevision string `json:"protocolRevision"`
		FactType         string `json:"factType"`
		Digest           string `json:"digest"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return err
	}
	switch head.FactType {
	case resultFactTypeAdmitted:
		if head.ProtocolRevision == "" {
			return s.applyLegacyAdmittedLine(line, in)
		}
		if head.ProtocolRevision != resultIngressProtocolRevision {
			return fmt.Errorf("unsupported result ingress protocol revision %q", head.ProtocolRevision)
		}
		var fact resultAdmittedFact
		if err := json.Unmarshal(line, &fact); err != nil {
			return err
		}
		if fact.Sequence != s.nextSequence {
			return fmt.Errorf("admitted fact sequence %d, want %d", fact.Sequence, s.nextSequence)
		}
		if err := requireDigest("DRCDigest", fact.DRCDigest); err != nil {
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
		in.admitted[fact.IdempotencyKey] = admittedEntry{
			fact:             AdmissionFact{FactDigest: fact.FactDigest, LedgerSequence: fact.LedgerSequence, IdempotentReplay: false},
			drcDigest:        fact.DRCDigest,
			envelopeKind:     fact.EnvelopeKind,
			envelopeSequence: fact.EnvelopeSequence,
			envelopeDigest:   fact.EnvelopeDigest,
		}
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
		at, _ := time.Parse(time.RFC3339, fact.ObservedAt)
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
