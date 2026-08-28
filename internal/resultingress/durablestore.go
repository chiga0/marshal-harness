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
)

// resultIngressStoreFileName 是 ResultIngress replay/quarantine/idempotency 的
// append-only 账本文件名（R2 纵切）。
const resultIngressStoreFileName = "result-ingress.jsonl"

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
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("resultingress: append fact: %w", err)
	}
	return f.Sync()
}

// RecordAdmitted 把一次成功接纳的幂等权威锚点持久化（在返回 fact 之前先落账，
// 使后续重复送达或跨进程重放可被机械检测）。
func (s *ingressDurableStore) RecordAdmitted(idempotencyKey, envelopeDigest, factDigest string, ledgerSequence uint64) error {
	fact := &resultAdmittedFact{
		FactType:       resultFactTypeAdmitted,
		Sequence:       s.nextSequence,
		IdempotencyKey: idempotencyKey,
		EnvelopeDigest: envelopeDigest,
		FactDigest:     factDigest,
		LedgerSequence: ledgerSequence,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.appendLine(fact,
		func() string { return fact.Digest },
		func(d string) { fact.Digest = d }); err != nil {
		return err
	}
	s.nextSequence++
	return nil
}

// RecordQuarantined 把一次拒绝投递的只读机械审计记录持久化。
func (s *ingressDurableStore) RecordQuarantined(reason RejectionReason, drcDigest, envelopeDigest string, observedAt time.Time) error {
	fact := &resultQuarantinedFact{
		FactType:       resultFactTypeQuarantined,
		Sequence:       s.nextSequence,
		Reason:         reason,
		DRCDigest:      drcDigest,
		EnvelopeDigest: envelopeDigest,
		ObservedAt:     observedAt.UTC().Format(time.RFC3339),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if err := s.requireBound(); err != nil {
		return err
	}
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
		FactType string `json:"factType"`
		Digest   string `json:"digest"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return err
	}
	switch head.FactType {
	case resultFactTypeAdmitted:
		var fact resultAdmittedFact
		if err := json.Unmarshal(line, &fact); err != nil {
			return err
		}
		storeddigest := fact.Digest
		fact.Digest = ""
		rawJSON, _ := json.Marshal(&fact)
		if digest, err := drcLessDigest(rawJSON); err != nil || digest != storeddigest {
			return errors.New("admitted fact digest mismatch")
		}
		fact.Digest = storeddigest
		in.admitted[fact.IdempotencyKey] = admittedEntry{
			fact:           AdmissionFact{FactDigest: fact.FactDigest, LedgerSequence: fact.LedgerSequence, IdempotentReplay: false},
			envelopeDigest: fact.EnvelopeDigest,
		}
		if fact.LedgerSequence > in.ledgerSequence {
			in.ledgerSequence = fact.LedgerSequence
		}
	case resultFactTypeQuarantined:
		var fact resultQuarantinedFact
		if err := json.Unmarshal(line, &fact); err != nil {
			return err
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

// drcLessDigest 返回 RFC8785 canonical JSON 的 sha256: digest（与已有实现一致：
// detached digest 不参与自身摘要）。
func drcLessDigest(rawJSON []byte) (string, error) {
	raw, err := canonical.JSON(rawJSON)
	if err != nil {
		return "", err
	}
	return canonical.DigestBytes(raw), nil
}
