package protocolrev

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

var (
	// ErrMalformedRevision 拒绝形态非法的 revision 原始值或构造值
	// （空值、空 family、空 version、多于一个 '/'、首尾空白、
	// family/version 内含 '/'）。
	ErrMalformedRevision = errors.New("malformed revision")
	// ErrMalformedPin 拒绝非法 pin：family 非法或 version 为空
	// （unversioned pin 一律 fail closed）。
	ErrMalformedPin = errors.New("malformed pinned revision")
	// ErrPinnedMismatch 拒绝不满足 pinned revision 的出示值；比较是
	// 全函数——unversioned 出示值即使 family 相同也拒绝，垃圾值同径。
	ErrPinnedMismatch = errors.New("pinned revision mismatch")
	// ErrMalformedDigest 拒绝形态非法的 digest
	// （非 sha256:<64-hex-lowercase>）。
	ErrMalformedDigest = errors.New("malformed digest")
	// ErrSameDigest 拒绝 FromDigest == ToDigest：迁移绝不原地重写历史。
	ErrSameDigest = errors.New("migration must not reuse the same digest")
	// ErrNotAMigration 拒绝不构成协议 revision 迁移的 supersede：
	// ToRevision 未 versioned，或 ToRevision == FromRevision（普通
	// supersede 不走迁移路径）。
	ErrNotAMigration = errors.New("not a protocol revision migration")
	// ErrFamilyChanged 拒绝跨协议族的迁移：协议族变更不是 revision
	// migration。
	ErrFamilyChanged = errors.New("migration must not change the protocol family")
	// ErrMigrationDigestMismatch 拒绝 MigrationDigest 与字段重算结果
	// 不一致（篡改或陈旧 digest）。
	ErrMigrationDigestMismatch = errors.New("migration digest mismatch")
	// ErrUnknownHistory 拒绝迁移未冻结的历史：FromDigest 必须先经
	// HistoryGuard.Record 冻结。
	ErrUnknownHistory = errors.New("migration source digest is not frozen history")
	// ErrDigestCollision 拒绝落向已冻结的 digest：ToDigest 必须是真正的
	// 新事实。
	ErrDigestCollision = errors.New("migration target digest already frozen")
)

// ── Revision ────────────────────────────────────────────────────────────────

// Revision 是解析后的协议 revision。Family 非空；Version 非空表示
// versioned（如 "v1"），为空表示 unversioned/legacy（如裸 "acp"）。
type Revision struct {
	Family  string
	Version string // EMPTY = unversioned/legacy
}

// ParseRevision 解析 canonical 形态 `family/version`（在最后一个 '/' 处
// 切分）："acp" → Family="acp"、Version=""；"acp/v1" → Family="acp"、
// Version="v1"。Fail closed：空值、空 family（"/v1"）、空 version
// （"acp/"）、多于一个 '/'、首尾空白一律 ErrMalformedRevision。
func ParseRevision(raw string) (Revision, error) {
	if strings.TrimSpace(raw) == "" {
		return Revision{}, fmt.Errorf("protocolrev: %w: must not be empty", ErrMalformedRevision)
	}
	var r Revision
	switch strings.Count(raw, "/") {
	case 0:
		r = Revision{Family: raw}
	case 1:
		idx := strings.LastIndex(raw, "/")
		if idx == 0 || idx == len(raw)-1 {
			return Revision{}, fmt.Errorf("protocolrev: %w: empty family or empty version in %q", ErrMalformedRevision, raw)
		}
		r = Revision{Family: raw[:idx], Version: raw[idx+1:]}
	default:
		return Revision{}, fmt.Errorf("protocolrev: %w: more than one '/' in %q", ErrMalformedRevision, raw)
	}
	if reason := revisionShapeViolation(r); reason != nil {
		return Revision{}, fmt.Errorf("protocolrev: %w: %v", ErrMalformedRevision, reason)
	}
	return r, nil
}

// String 返回 canonical 形态：Version 非空时 `family/version`，否则
// `family`。
func (r Revision) String() string {
	if r.Version == "" {
		return r.Family
	}
	return r.Family + "/" + r.Version
}

// Versioned 报告该 revision 是否携带版本（unversioned/legacy 值为
// false）。
func (r Revision) Versioned() bool {
	return r.Version != ""
}

// revisionShapeViolation 返回 revision 构造值的形态违规原因（无违规
// 返回 nil）。返回的是纯原因错误，不带 sentinel 与前缀，由调用方包装。
func revisionShapeViolation(r Revision) error {
	if strings.TrimSpace(r.Family) == "" {
		return errors.New("family must not be empty")
	}
	if strings.Contains(r.Family, "/") {
		return errors.New("family must not contain '/'")
	}
	if strings.Contains(r.Version, "/") {
		return errors.New("version must not contain '/'")
	}
	if strings.TrimSpace(r.Family) != r.Family || strings.TrimSpace(r.Version) != r.Version {
		return errors.New("family/version must not carry surrounding whitespace")
	}
	return nil
}

// ── PinnedRevision ──────────────────────────────────────────────────────────

// PinnedRevision 是一条 admission pin：形状与 Revision 相同，但
// Version 必须非空（unversioned pin 在构造时 fail closed）。
type PinnedRevision struct {
	Family  string
	Version string // 必须非空
}

// NewPinnedRevision 构造 pin；family 非法或 version 为空一律
// ErrMalformedPin，fail closed。
func NewPinnedRevision(family, version string) (PinnedRevision, error) {
	p := PinnedRevision{Family: family, Version: version}
	if reason := pinShapeViolation(p); reason != nil {
		return PinnedRevision{}, fmt.Errorf("protocolrev: %w: %v", ErrMalformedPin, reason)
	}
	return p, nil
}

// Revision 返回 pin 的 Revision 视图（必然 Versioned）。
func (p PinnedRevision) Revision() Revision {
	return Revision{Family: p.Family, Version: p.Version}
}

// String 返回 pin 的 canonical 形态 `family/version`。
func (p PinnedRevision) String() string {
	return p.Revision().String()
}

// pinShapeViolation 返回 pin 的形态违规原因（无违规返回 nil）。
func pinShapeViolation(p PinnedRevision) error {
	if reason := revisionShapeViolation(Revision{Family: p.Family, Version: p.Version}); reason != nil {
		return reason
	}
	if p.Version == "" {
		return errors.New("pin must carry a non-empty version")
	}
	return nil
}

// AdmitPinned 是冻结的 pinned revision 接纳规则：出示值必须与 pin 精确
// 相等（family 且 version）。unversioned 出示值永不满足 pinned
// revision——即使 family 相同也 ErrPinnedMismatch；垃圾出示值同径拒绝
// （比较是全函数）。pin 自身非法（未 versioned、形态非法）fail closed，
// 返回 ErrMalformedPin。
func AdmitPinned(pin PinnedRevision, presented Revision) error {
	if reason := pinShapeViolation(pin); reason != nil {
		return fmt.Errorf("protocolrev: %w: %v", ErrMalformedPin, reason)
	}
	if presented != pin.Revision() {
		return fmt.Errorf("protocolrev: %w: pinned %q, presented %q",
			ErrPinnedMismatch, pin.String(), presented.String())
	}
	return nil
}

// ── SupersedeMigration ──────────────────────────────────────────────────────

// SupersedeMigration 是协议 bump 的唯一合法迁移形态：一条新增 supersede
// 事实，把已冻结的 FromDigest（历史 snapshot）指向全新的 ToDigest。
// FromRevision 可为 unversioned 或更旧；ToRevision 必须 Versioned 且 ≠
// FromRevision；两侧 Family 必须相同。ProvenanceDigest 是迁移授权记录的
// digest。MigrationDigest 由 canonical 在全部字段上派生。版本之间没有
// 次序语义——契约只要求不等，"更旧/更新"是治理层判断，本包不解释。
type SupersedeMigration struct {
	FromDigest       string // sha256:<64-hex>
	ToDigest         string // sha256:<64-hex>，必须 ≠ FromDigest
	FromRevision     Revision
	ToRevision       Revision
	ProvenanceDigest string // sha256:<64-hex>，迁移授权记录
	MigrationDigest  string // sha256:<64-hex>，canonical-derived
}

type revisionJSON struct {
	Family  string `json:"family"`
	Version string `json:"version"`
}

type migrationDigestInputJSON struct {
	FromDigest       string       `json:"fromDigest"`
	ToDigest         string       `json:"toDigest"`
	FromRevision     revisionJSON `json:"fromRevision"`
	ToRevision       revisionJSON `json:"toRevision"`
	ProvenanceDigest string       `json:"provenanceDigest"`
}

// Digest 返回迁移身份字段（不含 MigrationDigest 自身）的 canonical
// sha256 digest。
func (m SupersedeMigration) Digest() (string, error) {
	raw, err := json.Marshal(migrationDigestInputJSON{
		FromDigest:       m.FromDigest,
		ToDigest:         m.ToDigest,
		FromRevision:     revisionJSON{Family: m.FromRevision.Family, Version: m.FromRevision.Version},
		ToRevision:       revisionJSON{Family: m.ToRevision.Family, Version: m.ToRevision.Version},
		ProvenanceDigest: m.ProvenanceDigest,
	})
	if err != nil {
		return "", fmt.Errorf("protocolrev: migration serialisation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// NewSupersedeMigration 构造 SupersedeMigration 并派生 MigrationDigest。
// 任意字段非法 fail closed。
func NewSupersedeMigration(fromDigest, toDigest string, fromRevision, toRevision Revision, provenanceDigest string) (SupersedeMigration, error) {
	m := SupersedeMigration{
		FromDigest:       fromDigest,
		ToDigest:         toDigest,
		FromRevision:     fromRevision,
		ToRevision:       toRevision,
		ProvenanceDigest: provenanceDigest,
	}
	if err := validateMigrationIdentity(m); err != nil {
		return SupersedeMigration{}, err
	}
	digest, err := m.Digest()
	if err != nil {
		return SupersedeMigration{}, err
	}
	m.MigrationDigest = digest
	return m, nil
}

// Validate 复核全部字段并重算 MigrationDigest：任何字段非法、digest
// 形态非法或重算结果不一致均 fail closed。
func (m SupersedeMigration) Validate() error {
	if err := validateMigrationIdentity(m); err != nil {
		return err
	}
	if err := requireDigest("MigrationDigest", m.MigrationDigest); err != nil {
		return fmt.Errorf("protocolrev: %w: %v", ErrMalformedDigest, err)
	}
	derived, err := m.Digest()
	if err != nil {
		return err
	}
	if derived != m.MigrationDigest {
		return fmt.Errorf("protocolrev: %w: stored %q, derived %q",
			ErrMigrationDigestMismatch, m.MigrationDigest, derived)
	}
	return nil
}

// validateMigrationIdentity 校验除 MigrationDigest 之外的全部字段。
func validateMigrationIdentity(m SupersedeMigration) error {
	if err := requireDigest("FromDigest", m.FromDigest); err != nil {
		return fmt.Errorf("protocolrev: %w: %v", ErrMalformedDigest, err)
	}
	if err := requireDigest("ToDigest", m.ToDigest); err != nil {
		return fmt.Errorf("protocolrev: %w: %v", ErrMalformedDigest, err)
	}
	if m.FromDigest == m.ToDigest {
		return fmt.Errorf("protocolrev: %w: migration never rewrites history in place", ErrSameDigest)
	}
	if reason := revisionShapeViolation(m.FromRevision); reason != nil {
		return fmt.Errorf("protocolrev: %w: FromRevision: %v", ErrMalformedRevision, reason)
	}
	if reason := revisionShapeViolation(m.ToRevision); reason != nil {
		return fmt.Errorf("protocolrev: %w: ToRevision: %v", ErrMalformedRevision, reason)
	}
	if !m.ToRevision.Versioned() {
		return fmt.Errorf("protocolrev: %w: ToRevision must be versioned (got %q)",
			ErrNotAMigration, m.ToRevision.String())
	}
	if m.FromRevision.Family != m.ToRevision.Family {
		return fmt.Errorf("protocolrev: %w: %q → %q is a family change, not a revision migration",
			ErrFamilyChanged, m.FromRevision.String(), m.ToRevision.String())
	}
	if m.FromRevision == m.ToRevision {
		return fmt.Errorf("protocolrev: %w: unchanged revision %q is an ordinary supersede",
			ErrNotAMigration, m.ToRevision.String())
	}
	if err := requireDigest("ProvenanceDigest", m.ProvenanceDigest); err != nil {
		return fmt.Errorf("protocolrev: %w: %v", ErrMalformedDigest, err)
	}
	return nil
}

// ── HistoryGuard ────────────────────────────────────────────────────────────

// HistoryGuard 是反重写守护：以 digest 为身份记录已冻结的历史 snapshot。
// digest 即身份，同一 digest 重复 Record 幂等；Record 即冻结历史的动作。
// 并发安全；纯内存、确定性，不携带任何时钟。
type HistoryGuard struct {
	mu       sync.Mutex
	recorded map[string]struct{}
}

// NewHistoryGuard 返回一个空的可用守护。
func NewHistoryGuard() *HistoryGuard {
	return &HistoryGuard{recorded: make(map[string]struct{})}
}

// Record 冻结 snapshotDigest（put-if-absent）：digest 形态非法 fail
// closed（ErrMalformedDigest）；同一 digest 重复记录幂等成功。
func (g *HistoryGuard) Record(snapshotDigest string) error {
	if err := requireDigest("snapshotDigest", snapshotDigest); err != nil {
		return fmt.Errorf("protocolrev: %w: %v", ErrMalformedDigest, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.recorded[snapshotDigest] = struct{}{}
	return nil
}

// AssertMigrationPreserves 判定迁移 m 是否保持"历史不重写"：先
// m.Validate() 复核迁移自身；FromDigest 必须已被 g 冻结（否则
// ErrUnknownHistory——不能迁移从未冻结的历史）；ToDigest 必须尚未被
// 冻结（否则 ErrDigestCollision——目标必须是真正的新事实）。判定成功
// 不修改任何记录：迁移落地由调用方随后对 ToDigest 调用 Record 完成。
func AssertMigrationPreserves(g *HistoryGuard, m SupersedeMigration) error {
	if g == nil {
		return errors.New("protocolrev: HistoryGuard must not be nil")
	}
	if err := m.Validate(); err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.recorded[m.FromDigest]; !ok {
		return fmt.Errorf("protocolrev: %w: FromDigest %q was never frozen", ErrUnknownHistory, m.FromDigest)
	}
	if _, ok := g.recorded[m.ToDigest]; ok {
		return fmt.Errorf("protocolrev: %w: ToDigest %q is already frozen history", ErrDigestCollision, m.ToDigest)
	}
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// requireDigest 校验 sha256:<64-hex-lowercase> digest 形态，返回纯原因
// 错误，由调用方包装 sentinel 与前缀。
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
