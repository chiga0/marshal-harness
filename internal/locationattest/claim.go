package locationattest

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

var (
	// ErrMalformedClaim 拒绝结构非法的 LocationClaim。
	ErrMalformedClaim = errors.New("malformed location claim")
	// ErrMalformedFact 拒绝结构非法的 LocationFact。
	ErrMalformedFact = errors.New("malformed location fact")
	// ErrDigestTampered 拒绝与内容重算不一致的 digest。
	ErrDigestTampered = errors.New("digest does not match recomputed content")
)

// HandleKind 是 authority-verified location fact 的句柄种类封闭枚举。
type HandleKind string

const (
	HandleKindPID                    HandleKind = "pid"
	HandleKindProcessGroup           HandleKind = "process-group"
	HandleKindCgroup                 HandleKind = "cgroup"
	HandleKindVMHandle               HandleKind = "vm-handle"
	HandleKindIndependentAttestation HandleKind = "independent-attestation"
)

func (k HandleKind) validate() error {
	switch k {
	case HandleKindPID, HandleKindProcessGroup, HandleKindCgroup, HandleKindVMHandle, HandleKindIndependentAttestation:
		return nil
	default:
		return fmt.Errorf("locationattest: unknown HandleKind %q", string(k))
	}
}

// ── LocationClaim（provider-asserted） ──────────────────────────────────────

// LocationClaim 是 Sandbox Provider 自报的执行位置声明。claim 只可用于
// 诊断与收紧权限，永远不能单独支撑 production assurance/publication。
type LocationClaim struct {
	AllocationID           string
	Generation             int64  // strictly positive
	ProviderRegistrationID string // "registration:<hex>"，claim 的出示方
	HandleHint             string // 出示方提供的句柄提示（诊断叙事，不参与裁决）
	ClaimDigest            string // sha256:<64-hex>，canonical 派生
}

type claimJSON struct {
	AllocationID           string `json:"allocationId"`
	Generation             int64  `json:"generation"`
	ProviderRegistrationID string `json:"providerRegistrationId"`
	HandleHint             string `json:"handleHint"`
}

// validateIdentity 校验除 ClaimDigest 外的身份字段。
func (c LocationClaim) validateIdentity() error {
	if strings.TrimSpace(c.AllocationID) == "" {
		return fmt.Errorf("locationattest: %w: AllocationID must not be empty", ErrMalformedClaim)
	}
	if c.Generation < 1 {
		return fmt.Errorf("locationattest: %w: Generation must be positive, got %d", ErrMalformedClaim, c.Generation)
	}
	if strings.TrimSpace(c.ProviderRegistrationID) == "" {
		return fmt.Errorf("locationattest: %w: ProviderRegistrationID must not be empty", ErrMalformedClaim)
	}
	return nil
}

// recompute 返回 claim 身份字段的 canonical digest。
func (c LocationClaim) recompute() (string, error) {
	raw, err := json.Marshal(claimJSON{
		AllocationID:           c.AllocationID,
		Generation:             c.Generation,
		ProviderRegistrationID: c.ProviderRegistrationID,
		HandleHint:             c.HandleHint,
	})
	if err != nil {
		return "", fmt.Errorf("locationattest: claim serialisation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// NewLocationClaim 构造 claim 并派生 ClaimDigest；畸形输入 fail closed。
func NewLocationClaim(allocationID string, generation int64, providerRegistrationID, handleHint string) (LocationClaim, error) {
	c := LocationClaim{
		AllocationID:           allocationID,
		Generation:             generation,
		ProviderRegistrationID: providerRegistrationID,
		HandleHint:             handleHint,
	}
	if err := c.validateIdentity(); err != nil {
		return LocationClaim{}, err
	}
	digest, err := c.recompute()
	if err != nil {
		return LocationClaim{}, err
	}
	c.ClaimDigest = digest
	return c, nil
}

// Validate 校验 claim 全部字段并重算 digest（篡改 fail closed）。
func (c LocationClaim) Validate() error {
	if err := c.validateIdentity(); err != nil {
		return err
	}
	if err := requireDigest("ClaimDigest", c.ClaimDigest); err != nil {
		return fmt.Errorf("locationattest: %w: %v", ErrMalformedClaim, err)
	}
	want, err := c.recompute()
	if err != nil {
		return err
	}
	if c.ClaimDigest != want {
		return fmt.Errorf("locationattest: %w: ClaimDigest", ErrDigestTampered)
	}
	return nil
}

// ── LocationFact（authority-verified） ──────────────────────────────────────

// LocationFact 是故障域外 authority observer 观测签发的执行位置事实。
// 只有 fact 能支撑 production assurance。FactDigest 由身份字段 canonical
// 派生，注册与裁决时均重算。
type LocationFact struct {
	AllocationID string
	Generation   int64 // strictly positive
	HandleKind   HandleKind
	HandleDigest string // sha256:<64-hex>，kernel-held handle 观测记录 digest
	ObserverID   string // authority 侧 observer 身份；绝不等于被证明方 registration id
	FactDigest   string // sha256:<64-hex>，canonical 派生
}

type factJSON struct {
	AllocationID string `json:"allocationId"`
	Generation   int64  `json:"generation"`
	HandleKind   string `json:"handleKind"`
	HandleDigest string `json:"handleDigest"`
	ObserverID   string `json:"observerId"`
}

func (f LocationFact) validateIdentity() error {
	if strings.TrimSpace(f.AllocationID) == "" {
		return fmt.Errorf("locationattest: %w: AllocationID must not be empty", ErrMalformedFact)
	}
	if f.Generation < 1 {
		return fmt.Errorf("locationattest: %w: Generation must be positive, got %d", ErrMalformedFact, f.Generation)
	}
	if err := f.HandleKind.validate(); err != nil {
		return fmt.Errorf("locationattest: %w: %v", ErrMalformedFact, err)
	}
	if err := requireDigest("HandleDigest", f.HandleDigest); err != nil {
		return fmt.Errorf("locationattest: %w: %v", ErrMalformedFact, err)
	}
	if strings.TrimSpace(f.ObserverID) == "" {
		return fmt.Errorf("locationattest: %w: ObserverID must not be empty", ErrMalformedFact)
	}
	return nil
}

func (f LocationFact) recompute() (string, error) {
	raw, err := json.Marshal(factJSON{
		AllocationID: f.AllocationID,
		Generation:   f.Generation,
		HandleKind:   string(f.HandleKind),
		HandleDigest: f.HandleDigest,
		ObserverID:   f.ObserverID,
	})
	if err != nil {
		return "", fmt.Errorf("locationattest: fact serialisation failed: %w", err)
	}
	return canonical.DigestJSON(raw)
}

// NewLocationFact 构造 fact 并派生 FactDigest；畸形输入 fail closed。
func NewLocationFact(allocationID string, generation int64, handleKind HandleKind, handleDigest, observerID string) (LocationFact, error) {
	f := LocationFact{
		AllocationID: allocationID,
		Generation:   generation,
		HandleKind:   handleKind,
		HandleDigest: handleDigest,
		ObserverID:   observerID,
	}
	if err := f.validateIdentity(); err != nil {
		return LocationFact{}, err
	}
	digest, err := f.recompute()
	if err != nil {
		return LocationFact{}, err
	}
	f.FactDigest = digest
	return f, nil
}

// Validate 校验 fact 全部字段并重算 FactDigest（篡改 fail closed）。
func (f LocationFact) Validate() error {
	if err := f.validateIdentity(); err != nil {
		return err
	}
	if err := requireDigest("FactDigest", f.FactDigest); err != nil {
		return fmt.Errorf("locationattest: %w: %v", ErrMalformedFact, err)
	}
	want, err := f.recompute()
	if err != nil {
		return err
	}
	if f.FactDigest != want {
		return fmt.Errorf("locationattest: %w: FactDigest", ErrDigestTampered)
	}
	return nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

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
