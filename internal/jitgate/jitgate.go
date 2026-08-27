package jitgate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// Sentinel errors；所有外部可见错误均带 jitgate: 前缀。
var (
	// ErrMalformedToken 拒绝形态非法的 AdmissionToken 身份字段。
	ErrMalformedToken = errors.New("malformed admission token")
	// ErrTokenTampered 拒绝 TokenDigest 与五个身份字段重算不一致的 token
	// （含 TokenDigest 为空或形态非法——单一路径比对，fail closed）。
	ErrTokenTampered = errors.New("admission token tampered")
	// ErrMalformedView 拒绝形态非法或自相矛盾的 LedgerView。
	ErrMalformedView = errors.New("malformed ledger view")
)

// RejectionReason 是 provision 前重验拒绝标签的封闭枚举。取值只由本包
// 常量构造，未知值永不出现。token-tampered 保留在枚举词汇内（审计层使
// 用），但按形状纪律走 error 通道（ErrTokenTampered），不会出现在
// Verdict.Reason 中。
type RejectionReason string

const (
	// RejectionReasonTokenTampered 表示 token 完整性校验失败（error 通道）。
	RejectionReasonTokenTampered RejectionReason = "token-tampered"
	// RejectionReasonTokenExpired 表示 now 已越过 validUntil（半开区间
	// [issue, validUntil)，now == validUntil 即过期）。
	RejectionReasonTokenExpired RejectionReason = "token-expired"
	// RejectionReasonRegistrationInactive 表示 registration 当前不 active。
	RejectionReasonRegistrationInactive RejectionReason = "registration-inactive"
	// RejectionReasonSnapshotSuperseded 表示当前 active snapshot digest 与
	// token 钉住的不一致。
	RejectionReasonSnapshotSuperseded RejectionReason = "snapshot-superseded"
	// RejectionReasonPolicyRotated 表示 current policy digest 与 token 钉住
	// 的不一致。
	RejectionReasonPolicyRotated RejectionReason = "policy-rotated"
	// RejectionReasonPolicyInactive 表示 policy 当前不 active。
	RejectionReasonPolicyInactive RejectionReason = "policy-inactive"
)

// AdmissionToken 是 provision-admission capability：签发方在 admission
// 时点把决策身份钉住，provision 时点必须经 VerifyBeforeProvision 重验。
// 所有字段必填；TokenDigest 由五个身份字段 canonical 派生，改任一字节
// 即被 Validate 判为篡改。
type AdmissionToken struct {
	DecisionID     string // 非空
	RegistrationID string // 非空，必须带 registration: 前缀
	SnapshotDigest string // sha256:<64-hex>
	PolicyDigest   string // sha256:<64-hex>
	ValidUntilUnix int64  // 严格为正；半开区间 [issue, validUntil)
	TokenDigest    string // sha256:<64-hex>，canonical 派生
}

type admissionTokenIdentityJSON struct {
	DecisionID     string `json:"decisionId"`
	RegistrationID string `json:"registrationId"`
	SnapshotDigest string `json:"snapshotDigest"`
	PolicyDigest   string `json:"policyDigest"`
	ValidUntilUnix int64  `json:"validUntilUnix"`
}

// NewAdmissionToken 构造 AdmissionToken 并派生 TokenDigest；任一身份字段
// 非法 fail closed（先于 digest 派生）。
func NewAdmissionToken(decisionID, registrationID, snapshotDigest, policyDigest string, validUntilUnix int64) (AdmissionToken, error) {
	token := AdmissionToken{
		DecisionID:     decisionID,
		RegistrationID: registrationID,
		SnapshotDigest: snapshotDigest,
		PolicyDigest:   policyDigest,
		ValidUntilUnix: validUntilUnix,
	}
	if err := validateTokenIdentity(token); err != nil {
		return AdmissionToken{}, err
	}
	digest, err := token.deriveTokenDigest()
	if err != nil {
		return AdmissionToken{}, err
	}
	token.TokenDigest = digest
	return token, nil
}

// Validate 重验全部身份字段，并对五个身份字段重算 TokenDigest：字段形态
// 非法 → ErrMalformedToken；重算不一致 → ErrTokenTampered（含 TokenDigest
// 为空或形态非法，单一比对路径，fail closed）。
func (t AdmissionToken) Validate() error {
	if err := validateTokenIdentity(t); err != nil {
		return err
	}
	want, err := t.deriveTokenDigest()
	if err != nil {
		return err
	}
	if t.TokenDigest != want {
		return fmt.Errorf("jitgate: %w: TokenDigest does not match identity fields", ErrTokenTampered)
	}
	return nil
}

// deriveTokenDigest 对五个身份字段（不含 TokenDigest 自身）做 canonical
// 派生。
func (t AdmissionToken) deriveTokenDigest() (string, error) {
	raw, err := json.Marshal(admissionTokenIdentityJSON{
		DecisionID:     t.DecisionID,
		RegistrationID: t.RegistrationID,
		SnapshotDigest: t.SnapshotDigest,
		PolicyDigest:   t.PolicyDigest,
		ValidUntilUnix: t.ValidUntilUnix,
	})
	if err != nil {
		return "", fmt.Errorf("jitgate: AdmissionToken serialisation failed: %w", err)
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil {
		return "", fmt.Errorf("jitgate: AdmissionToken canonicalisation failed: %w", err)
	}
	return digest, nil
}

// LedgerView 是 verifier 所需的当前账本事实，以纯值传入。所有校验一律
// 针对 view 判定，绝不只信 token 自述。
type LedgerView struct {
	RegistrationActive   bool
	ActiveSnapshotDigest string // RegistrationActive==true 时必须是合法 digest
	CurrentPolicyDigest  string // 必须是合法 digest
	PolicyActive         bool
}

// validateShape 校验 LedgerView 形态：CurrentPolicyDigest 必须是合法
// digest；RegistrationActive==true 时 ActiveSnapshotDigest 必须是合法
// digest（否则 view 自相矛盾，fail closed）。
func (v LedgerView) validateShape() error {
	if err := requireDigest("LedgerView.CurrentPolicyDigest", v.CurrentPolicyDigest); err != nil {
		return fmt.Errorf("jitgate: %w: %v", ErrMalformedView, err)
	}
	if v.RegistrationActive {
		if err := requireDigest("LedgerView.ActiveSnapshotDigest", v.ActiveSnapshotDigest); err != nil {
			return fmt.Errorf("jitgate: %w: %v", ErrMalformedView, err)
		}
	}
	return nil
}

// Verdict 是一次 provision 前重验的确定性结论。形状纪律：
//
//   - 结构性问题（token 篡改/畸形、LedgerView 形态非法）只走 error
//     通道，VerifyBeforeProvision 返回 nil Verdict；
//   - 策略/生命周期拒绝只经 Verdict{OK:false, Reason:...} 表达，
//     error 为 nil；
//   - 全部通过 → Verdict{OK:true}。
type Verdict struct {
	OK     bool
	Reason RejectionReason // 仅当 OK == false 时有值
}

// VerifyBeforeProvision 在 provision 前对 AdmissionToken 做强制的
// current-ledger 重验。时钟只经注入参数 now 获得，从不调用 time.Now()。
//
// 冻结的评估顺序（首个失败即返回）：
//
//  1. token.Validate()——篡改或畸形 → error；
//  2. LedgerView 形态校验 → error ErrMalformedView；
//  3. !view.RegistrationActive → registration-inactive；
//  4. view.ActiveSnapshotDigest != token.SnapshotDigest → snapshot-superseded；
//  5. !view.PolicyActive → policy-inactive；
//  6. view.CurrentPolicyDigest != token.PolicyDigest → policy-rotated；
//  7. now.Unix() >= token.ValidUntilUnix → token-expired
//     （半开区间 [issue, validUntil) 为冻结规则，now == validUntil 即过期）。
//
// 步骤 3–7 一律针对 view 判定，绝不只信 token 自述；全部通过 →
// Verdict{OK:true}, nil。
func VerifyBeforeProvision(token AdmissionToken, view LedgerView, now time.Time) (*Verdict, error) {
	if err := token.Validate(); err != nil {
		return nil, err
	}
	if err := view.validateShape(); err != nil {
		return nil, err
	}
	if !view.RegistrationActive {
		return &Verdict{OK: false, Reason: RejectionReasonRegistrationInactive}, nil
	}
	if view.ActiveSnapshotDigest != token.SnapshotDigest {
		return &Verdict{OK: false, Reason: RejectionReasonSnapshotSuperseded}, nil
	}
	if !view.PolicyActive {
		return &Verdict{OK: false, Reason: RejectionReasonPolicyInactive}, nil
	}
	if view.CurrentPolicyDigest != token.PolicyDigest {
		return &Verdict{OK: false, Reason: RejectionReasonPolicyRotated}, nil
	}
	if now.Unix() >= token.ValidUntilUnix {
		return &Verdict{OK: false, Reason: RejectionReasonTokenExpired}, nil
	}
	return &Verdict{OK: true}, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// validateTokenIdentity 校验五个身份字段（不含 TokenDigest）。构造与
// Validate 共用同一份规则，保证派生 digest 前字段形态已被钉死。
func validateTokenIdentity(t AdmissionToken) error {
	if strings.TrimSpace(t.DecisionID) == "" {
		return fmt.Errorf("jitgate: %w: DecisionID must not be empty", ErrMalformedToken)
	}
	if strings.TrimSpace(t.RegistrationID) == "" {
		return fmt.Errorf("jitgate: %w: RegistrationID must not be empty", ErrMalformedToken)
	}
	if !strings.HasPrefix(t.RegistrationID, "registration:") {
		return fmt.Errorf("jitgate: %w: RegistrationID must carry the registration: prefix", ErrMalformedToken)
	}
	if err := requireDigest("AdmissionToken.SnapshotDigest", t.SnapshotDigest); err != nil {
		return fmt.Errorf("jitgate: %w: %v", ErrMalformedToken, err)
	}
	if err := requireDigest("AdmissionToken.PolicyDigest", t.PolicyDigest); err != nil {
		return fmt.Errorf("jitgate: %w: %v", ErrMalformedToken, err)
	}
	if t.ValidUntilUnix <= 0 {
		return fmt.Errorf("jitgate: %w: ValidUntilUnix must be strictly positive", ErrMalformedToken)
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
