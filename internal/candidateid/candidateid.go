package candidateid

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

const candidateIDPrefix = "candidate:"

var (
	// ErrMalformedDigest 拒绝形态非法的 digest 值（sha256:<64-hex>）。
	ErrMalformedDigest = errors.New("malformed digest")
	// ErrMalformedCandidateID 拒绝形态非法的 CandidateID
	// （candidate:<64-hex>）。
	ErrMalformedCandidateID = errors.New("malformed candidate id")
	// ErrEmptyOriginAttempt 拒绝空的 OriginAttemptID：legacy lineage 必须保留
	// provenance 锚点（但 OriginAttemptID 不进入 identity）。
	ErrEmptyOriginAttempt = errors.New("empty origin attempt id")
	// ErrEmptyLegacyProvenance 拒绝空的 legacy 三元组分量（taskID/runID 必须
	// 非空，仅作 provenance 记录）。
	ErrEmptyLegacyProvenance = errors.New("empty legacy provenance")
	// ErrIdentityTampered 拒绝与内容重算不一致的 CandidateIdentity
	// （IdentityDigest 或 CandidateID 被改写）。
	ErrIdentityTampered = errors.New("candidate identity tampered")
	// ErrIdentityConflict 拒绝同一 CandidateID 绑定不同内容字段。由于
	// CandidateID 由内容派生，理论上不可达；实践中仍作为 guard fail
	// closed（绝不静默覆盖）。
	ErrIdentityConflict = errors.New("candidate identity conflict")
	// ErrUnknownCandidate 拒绝未注册的 CandidateID。
	ErrUnknownCandidate = errors.New("unknown candidate")
	// ErrEvidenceRebound 拒绝把同一证据换绑到另一 CandidateID：证据换绑
	// 永不允许。
	ErrEvidenceRebound = errors.New("evidence rebound")
	// ErrUnknownEvidence 拒绝未绑定的 evidence digest。
	ErrUnknownEvidence = errors.New("unknown evidence")
	// ErrNilLedger 拒绝 nil IdentityLedger 依赖（fail closed）。
	ErrNilLedger = errors.New("nil identity ledger")
)

// ── CandidateIdentity ───────────────────────────────────────────────────────

// CandidateIdentity 是 Candidate 的一等身份记录。CandidateID 与
// IdentityDigest 仅由 ContentDigest + RecordDigest canonical 派生；
// OriginAttemptID 只作 provenance，不进入 identity——两条 content+record
// digest 相同而 OriginAttemptID 不同的身份记录是同一个 Candidate。
type CandidateIdentity struct {
	CandidateID     string // candidate:<64-hex>，hex 部分等于 IdentityDigest 的 hex
	ContentDigest   string // sha256:<64-hex>
	RecordDigest    string // sha256:<64-hex>
	OriginAttemptID string // provenance ONLY；非空，不进入 identity
	IdentityDigest  string // sha256:<64-hex>，canonical 派生自 (ContentDigest, RecordDigest)
}

// identityJSON 是 identity 派生的 canonical 底稿；刻意不包含
// OriginAttemptID（provenance 不进入 identity）。
type identityJSON struct {
	ContentDigest string `json:"contentDigest"`
	RecordDigest  string `json:"recordDigest"`
}

// validateIdentity 校验除派生字段外的输入字段形态。
func (id CandidateIdentity) validateIdentity() error {
	if err := requireDigest("ContentDigest", id.ContentDigest); err != nil {
		return fmt.Errorf("candidateid: %w: %v", ErrMalformedDigest, err)
	}
	if err := requireDigest("RecordDigest", id.RecordDigest); err != nil {
		return fmt.Errorf("candidateid: %w: %v", ErrMalformedDigest, err)
	}
	if strings.TrimSpace(id.OriginAttemptID) == "" {
		return fmt.Errorf("candidateid: %w: must not be empty", ErrEmptyOriginAttempt)
	}
	return nil
}

// recomputeIdentityDigest 返回 (ContentDigest, RecordDigest) 的 canonical
// identity digest。
func (id CandidateIdentity) recomputeIdentityDigest() (string, error) {
	raw, err := json.Marshal(identityJSON{
		ContentDigest: id.ContentDigest,
		RecordDigest:  id.RecordDigest,
	})
	if err != nil {
		return "", fmt.Errorf("candidateid: identity serialisation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// candidateIDFromIdentityDigest 返回 IdentityDigest 对应的 CandidateID：
// hex 部分与 IdentityDigest 的 hex 完全一致。
func candidateIDFromIdentityDigest(identityDigest string) string {
	return candidateIDPrefix + strings.TrimPrefix(identityDigest, "sha256:")
}

// NewCandidateIdentity 构造身份并派生 IdentityDigest 与 CandidateID；
// 畸形输入 fail closed。
func NewCandidateIdentity(contentDigest, recordDigest, originAttemptID string) (CandidateIdentity, error) {
	id := CandidateIdentity{
		ContentDigest:   contentDigest,
		RecordDigest:    recordDigest,
		OriginAttemptID: originAttemptID,
	}
	if err := id.validateIdentity(); err != nil {
		return CandidateIdentity{}, err
	}
	digest, err := id.recomputeIdentityDigest()
	if err != nil {
		return CandidateIdentity{}, err
	}
	id.IdentityDigest = digest
	id.CandidateID = candidateIDFromIdentityDigest(digest)
	return id, nil
}

// Validate 校验身份全部字段并重算派生值（篡改 fail closed：
// ErrIdentityTampered）。OriginAttemptID 是否变化不影响判定——它不在
// identity 内。
func (id CandidateIdentity) Validate() error {
	if err := id.validateIdentity(); err != nil {
		return err
	}
	if err := requireDigest("IdentityDigest", id.IdentityDigest); err != nil {
		return fmt.Errorf("candidateid: %w: %v", ErrMalformedDigest, err)
	}
	if err := validateCandidateID(id.CandidateID); err != nil {
		return err
	}
	wantDigest, err := id.recomputeIdentityDigest()
	if err != nil {
		return err
	}
	if id.IdentityDigest != wantDigest {
		return fmt.Errorf("candidateid: %w: IdentityDigest does not match recomputed digest of (ContentDigest, RecordDigest)", ErrIdentityTampered)
	}
	if id.CandidateID != candidateIDFromIdentityDigest(wantDigest) {
		return fmt.Errorf("candidateid: %w: CandidateID hex does not match IdentityDigest", ErrIdentityTampered)
	}
	return nil
}

// ── LegacyProvenance ────────────────────────────────────────────────────────

// LegacyProvenance 是 legacy task/run/attempt 三元组引用的 provenance
// 记录，按 CandidateID 存入 IdentityLedger。三元组只作 provenance，不进入
// identity。
type LegacyProvenance struct {
	CandidateID string
	TaskID      string
	RunID       string
	AttemptID   string
}

// ── IdentityLedger ──────────────────────────────────────────────────────────

// IdentityLedger 是 Candidate 身份的权威 put-if-absent 存储：身份只增
// 不改；同一 CandidateID 重复注册同一内容幂等，同一 CandidateID 绑定
// 不同内容 fail closed。并发安全；无时钟依赖。证据绑定与 legacy
// provenance 条目也存于本账本。
type IdentityLedger struct {
	mu         sync.Mutex
	identities map[string]CandidateIdentity
	evidence   map[string]string
	provenance map[string]LegacyProvenance
}

// NewIdentityLedger 返回一个空的可用 IdentityLedger。
func NewIdentityLedger() *IdentityLedger {
	return &IdentityLedger{
		identities: make(map[string]CandidateIdentity),
		evidence:   make(map[string]string),
		provenance: make(map[string]LegacyProvenance),
	}
}

// Register 注册一条身份。Validate 全通过才接纳；同一 CandidateID 重复
// 注册相同 (ContentDigest, RecordDigest, IdentityDigest) 幂等成功——
// provenance-set 语义：多个 Attempt 可收敛到同一 CandidateID，保留首次
// 注册的 OriginAttemptID，其后差异只是 provenance 集合的新成员，不构成
// 冲突。同一 CandidateID 绑定任何不同内容字段 fail closed
// （ErrIdentityConflict，绝不静默覆盖）。
func (l *IdentityLedger) Register(id CandidateIdentity) error {
	if err := id.Validate(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.identities[id.CandidateID]; ok {
		if existing.ContentDigest == id.ContentDigest &&
			existing.RecordDigest == id.RecordDigest &&
			existing.IdentityDigest == id.IdentityDigest {
			return nil
		}
		return fmt.Errorf("candidateid: %w: CandidateID %q already bound to different content",
			ErrIdentityConflict, id.CandidateID)
	}
	l.identities[id.CandidateID] = id
	return nil
}

// Resolve 返回 CandidateID 对应的身份；形态非法 fail closed
// （ErrMalformedCandidateID），未注册 fail closed（ErrUnknownCandidate）。
func (l *IdentityLedger) Resolve(candidateID string) (CandidateIdentity, error) {
	if err := validateCandidateID(candidateID); err != nil {
		return CandidateIdentity{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	id, ok := l.identities[candidateID]
	if !ok {
		return CandidateIdentity{}, fmt.Errorf("candidateid: %w: %q", ErrUnknownCandidate, candidateID)
	}
	return id, nil
}

// Provenance 返回 CandidateID 已记录的 legacy provenance 条目；形态非法
// fail closed（ErrMalformedCandidateID），无记录 fail closed
// （ErrUnknownCandidate）。
func (l *IdentityLedger) Provenance(candidateID string) (LegacyProvenance, error) {
	if err := validateCandidateID(candidateID); err != nil {
		return LegacyProvenance{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.provenance[candidateID]
	if !ok {
		return LegacyProvenance{}, fmt.Errorf("candidateid: %w: %q has no legacy provenance recorded", ErrUnknownCandidate, candidateID)
	}
	return p, nil
}

// CandidateForEvidence 返回证据绑定的 CandidateID；形态非法 fail
// closed（ErrMalformedDigest），未绑定 fail closed（ErrUnknownEvidence）。
func (l *IdentityLedger) CandidateForEvidence(evidenceDigest string) (string, error) {
	if err := requireDigest("EvidenceDigest", evidenceDigest); err != nil {
		return "", fmt.Errorf("candidateid: %w: %v", ErrMalformedDigest, err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	candidateID, ok := l.evidence[evidenceDigest]
	if !ok {
		return "", fmt.Errorf("candidateid: %w: %q", ErrUnknownEvidence, evidenceDigest)
	}
	return candidateID, nil
}

// ── 证据绑定 ────────────────────────────────────────────────────────────────

// EvidenceBinding 把证据绑定到 Candidate 身份（而非 Attempt）。
type EvidenceBinding struct {
	EvidenceDigest string // sha256:<64-hex>
	CandidateID    string // candidate:<64-hex>
}

// BindEvidence 把 evidenceDigest 绑定到 candidateID。candidateID 必须已在
// 账本注册（证据不得先行绑定未冻结身份，否则 ErrUnknownCandidate）；同一
// evidenceDigest 重复绑定同一 candidateID 幂等成功；同一 evidenceDigest
// 换绑另一 candidateID fail closed（ErrEvidenceRebound，换绑永不允许）。
func BindEvidence(l *IdentityLedger, evidenceDigest, candidateID string) error {
	if l == nil {
		return fmt.Errorf("candidateid: %w", ErrNilLedger)
	}
	if err := requireDigest("EvidenceDigest", evidenceDigest); err != nil {
		return fmt.Errorf("candidateid: %w: %v", ErrMalformedDigest, err)
	}
	if err := validateCandidateID(candidateID); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.identities[candidateID]; !ok {
		return fmt.Errorf("candidateid: %w: %q", ErrUnknownCandidate, candidateID)
	}
	if existing, ok := l.evidence[evidenceDigest]; ok {
		if existing == candidateID {
			return nil
		}
		return fmt.Errorf("candidateid: %w: evidence %q already bound to %q, got %q",
			ErrEvidenceRebound, evidenceDigest, existing, candidateID)
	}
	l.evidence[evidenceDigest] = candidateID
	return nil
}

// ── 单向兼容迁移 ────────────────────────────────────────────────────────────

// MigrateLegacyReference 把 legacy task/run/attempt 三元组引用迁入身份
// 账本：originAttemptID 恰为 attemptID；taskID/runID 必须非空，与
// attemptID 一起记录进按 CandidateID 键控的 LegacyProvenance 条目
// （first-wins；三元组只作 provenance，不进入 identity）。注册幂等可重
// 放。两个不同 Attempt 携带相同 content+record digest 时收敛到同一
// CandidateID——「Attempt→Candidate 1:1 不固化」的核心证明。
func MigrateLegacyReference(l *IdentityLedger, taskID, runID, attemptID, contentDigest, recordDigest string) (CandidateIdentity, error) {
	if l == nil {
		return CandidateIdentity{}, fmt.Errorf("candidateid: %w", ErrNilLedger)
	}
	if strings.TrimSpace(taskID) == "" {
		return CandidateIdentity{}, fmt.Errorf("candidateid: %w: taskID must not be empty", ErrEmptyLegacyProvenance)
	}
	if strings.TrimSpace(runID) == "" {
		return CandidateIdentity{}, fmt.Errorf("candidateid: %w: runID must not be empty", ErrEmptyLegacyProvenance)
	}
	id, err := NewCandidateIdentity(contentDigest, recordDigest, attemptID)
	if err != nil {
		return CandidateIdentity{}, err
	}
	if err := l.Register(id); err != nil {
		return CandidateIdentity{}, err
	}
	l.mu.Lock()
	if _, ok := l.provenance[id.CandidateID]; !ok {
		l.provenance[id.CandidateID] = LegacyProvenance{
			CandidateID: id.CandidateID,
			TaskID:      taskID,
			RunID:       runID,
			AttemptID:   attemptID,
		}
	}
	l.mu.Unlock()
	return id, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

func validateCandidateID(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("candidateid: %w: must not be empty", ErrMalformedCandidateID)
	}
	if !strings.HasPrefix(value, candidateIDPrefix) {
		return fmt.Errorf("candidateid: %w: must carry the candidate: prefix", ErrMalformedCandidateID)
	}
	hexPart := strings.TrimPrefix(value, candidateIDPrefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("candidateid: %w: must be a 64-character hex", ErrMalformedCandidateID)
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("candidateid: %w: must be lowercase hex", ErrMalformedCandidateID)
		}
	}
	return nil
}

func requireDigest(field, value string) error {
	const prefix = "sha256:"
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	if !strings.HasPrefix(value, prefix) {
		return fmt.Errorf("%s must carry the sha256: prefix", field)
	}
	hexPart := strings.TrimPrefix(value, prefix)
	if len(hexPart) != 64 {
		return fmt.Errorf("%s must be a 64-character sha256 hex digest", field)
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%s must be lowercase hex", field)
		}
	}
	return nil
}
