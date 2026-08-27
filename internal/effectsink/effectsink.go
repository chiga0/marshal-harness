package effectsink

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

// Sentinel errors；所有外部可见错误均带 effectsink: 前缀。
var (
	// ErrUnknownSinkKind 拒绝封闭枚举之外的 SinkKind（fail closed）。
	ErrUnknownSinkKind = errors.New("unknown sink kind")
	// ErrMalformedIntent 拒绝形态非法的 EffectIntent 身份字段。
	ErrMalformedIntent = errors.New("malformed effect intent")
	// ErrIntentTampered 拒绝 IntentDigest 与身份字段重算不一致的 intent
	// （含 IntentDigest 为空或形态非法——单一路径比对，fail closed）。
	ErrIntentTampered = errors.New("effect intent tampered")
	// ErrMalformedView 拒绝形态非法的 CurrentView。
	ErrMalformedView = errors.New("malformed current view")
	// ErrEffectConflict 拒绝同 IdempotencyKey 携带不同 IntentDigest 的再
	// 记录或再执行（账本永不覆盖，永不双执行，fail closed）。
	ErrEffectConflict = errors.New("effect conflict")
	// ErrNilLedger 拒绝 nil 账本（fail closed）。
	ErrNilLedger = errors.New("nil effect ledger")
)

// SinkKind 是 effect sink 种类的封闭枚举；未知值一律 fail closed。
type SinkKind string

const (
	// SinkKindSCMMutation 是 SCM 侧 mutation（分支/tag/PR 等写入）。
	SinkKindSCMMutation SinkKind = "scm-mutation"
	// SinkKindArtifactWrite 是 artifact 写入（产物发布、对象存储）。
	SinkKindArtifactWrite SinkKind = "artifact-write"
	// SinkKindSecretUse 是 secret 使用（凭证读取并作用于外部调用）。
	SinkKindSecretUse SinkKind = "secret-use"
	// SinkKindOtherEffect 是上述之外的其它外部效果。
	SinkKindOtherEffect SinkKind = "other-effect"
)

// RejectionReason 是 pre-mutation 独立 recheck 拒绝标签的封闭枚举。
// 取值只由本包常量构造，未知值永不出现。结构性问题（意图篡改或畸形、
// CurrentView 形态非法）按形状纪律走 error 通道，不出现在
// Verdict.Reason 中。
type RejectionReason string

const (
	// RejectionReasonGenerationStale 表示当前 generation 与意图授权时点
	// 钉住的不一致（意图已过期）。
	RejectionReasonGenerationStale RejectionReason = "generation-stale"
	// RejectionReasonFencingMismatch 表示当前 fencing token 与意图钉住
	// 的不一致。
	RejectionReasonFencingMismatch RejectionReason = "fencing-mismatch"
	// RejectionReasonAuthorizationRevoked 表示授权当前已撤销——
	// revoke→effect 竞态的核心防线：撤销后到达的意图永不执行。
	RejectionReasonAuthorizationRevoked RejectionReason = "authorization-revoked"
	// RejectionReasonAuthorizationSuperseded 表示当前授权 digest 与意图
	// 钉住的不一致（授权已被新记录取代）。
	RejectionReasonAuthorizationSuperseded RejectionReason = "authorization-superseded"
	// RejectionReasonTargetDrifted 表示当前目标状态 digest 与意图钉住
	// 的不一致（目标自授权时点以来已漂移）。
	RejectionReasonTargetDrifted RejectionReason = "target-drifted"
)

// EffectIntent 是执行一次外部效果的请求。签发方在授权时点把决策身份
// 钉住，mutation/secret use 时点必须经 VerifyBeforeEffect 重验。所有
// 字段必填；IntentDigest 由全部身份字段 canonical 派生，改任一字节
// 即被 Validate 判为篡改。
type EffectIntent struct {
	IntentID            string   // 非空
	Sink                SinkKind // 封闭枚举成员
	TargetID            string   // 非空（repo/branch/object 句柄等）
	IdempotencyKey      string   // 非空，EffectLedger 去重键
	Generation          int64    // 严格为正：意图获授权时的 generation
	FencingToken        string   // 非空：意图获授权时的 fencing token
	AuthorizationDigest string   // sha256:<64-hex>，授权记录 digest
	TargetDigest        string   // sha256:<64-hex>，预期目标状态 digest
	IntentDigest        string   // sha256:<64-hex>，canonical 派生
}

type effectIntentIdentityJSON struct {
	IntentID            string   `json:"intentId"`
	Sink                SinkKind `json:"sink"`
	TargetID            string   `json:"targetId"`
	IdempotencyKey      string   `json:"idempotencyKey"`
	Generation          int64    `json:"generation"`
	FencingToken        string   `json:"fencingToken"`
	AuthorizationDigest string   `json:"authorizationDigest"`
	TargetDigest        string   `json:"targetDigest"`
}

// NewEffectIntent 构造 EffectIntent 并派生 IntentDigest；任一身份字段
// 非法 fail closed（先于 digest 派生）。
func NewEffectIntent(intentID string, sink SinkKind, targetID, idempotencyKey string, generation int64, fencingToken, authorizationDigest, targetDigest string) (EffectIntent, error) {
	intent := EffectIntent{
		IntentID:            intentID,
		Sink:                sink,
		TargetID:            targetID,
		IdempotencyKey:      idempotencyKey,
		Generation:          generation,
		FencingToken:        fencingToken,
		AuthorizationDigest: authorizationDigest,
		TargetDigest:        targetDigest,
	}
	if err := validateIntentIdentity(intent); err != nil {
		return EffectIntent{}, err
	}
	digest, err := intent.deriveIntentDigest()
	if err != nil {
		return EffectIntent{}, err
	}
	intent.IntentDigest = digest
	return intent, nil
}

// Validate 重验全部身份字段，并对身份字段重算 IntentDigest：字段形态
// 非法 → ErrMalformedIntent；重算不一致 → ErrIntentTampered（含
// IntentDigest 为空或形态非法，单一比对路径，fail closed）。
func (i EffectIntent) Validate() error {
	if err := validateIntentIdentity(i); err != nil {
		return err
	}
	want, err := i.deriveIntentDigest()
	if err != nil {
		return err
	}
	if i.IntentDigest != want {
		return fmt.Errorf("effectsink: %w: IntentDigest does not match identity fields", ErrIntentTampered)
	}
	return nil
}

// deriveIntentDigest 对全部身份字段（不含 IntentDigest 自身）做
// canonical 派生。
func (i EffectIntent) deriveIntentDigest() (string, error) {
	raw, err := json.Marshal(effectIntentIdentityJSON{
		IntentID:            i.IntentID,
		Sink:                i.Sink,
		TargetID:            i.TargetID,
		IdempotencyKey:      i.IdempotencyKey,
		Generation:          i.Generation,
		FencingToken:        i.FencingToken,
		AuthorizationDigest: i.AuthorizationDigest,
		TargetDigest:        i.TargetDigest,
	})
	if err != nil {
		return "", fmt.Errorf("effectsink: EffectIntent serialisation failed: %w", err)
	}
	digest, err := canonical.DigestJSON(raw)
	if err != nil {
		return "", fmt.Errorf("effectsink: EffectIntent canonicalisation failed: %w", err)
	}
	return digest, nil
}

// CurrentView 是 effect 时点的当前账本事实，以纯值传入。所有 recheck
// 一律针对 view 判定，绝不只信 intent 自述。
type CurrentView struct {
	CurrentGeneration          int64  // 严格为正
	CurrentFencingToken        string // 非空
	AuthorizationRevoked       bool   // 授权当前已撤销
	CurrentAuthorizationDigest string // sha256:<64-hex>，当前授权真值（来自账本）
	CurrentTargetDigest        string // sha256:<64-hex>，当前目标状态
}

// Validate 校验 CurrentView 形态：CurrentGeneration 必须严格为正，
// CurrentFencingToken 必须非空；无论在不在撤销态，两个 current digest
// 都必须是合法 sha256 digest（view 内部一致，fail closed）。
func (v CurrentView) Validate() error {
	if v.CurrentGeneration <= 0 {
		return fmt.Errorf("effectsink: %w: CurrentGeneration must be strictly positive", ErrMalformedView)
	}
	if strings.TrimSpace(v.CurrentFencingToken) == "" {
		return fmt.Errorf("effectsink: %w: CurrentFencingToken must not be empty", ErrMalformedView)
	}
	if err := requireDigest("CurrentView.CurrentAuthorizationDigest", v.CurrentAuthorizationDigest); err != nil {
		return fmt.Errorf("effectsink: %w: %v", ErrMalformedView, err)
	}
	if err := requireDigest("CurrentView.CurrentTargetDigest", v.CurrentTargetDigest); err != nil {
		return fmt.Errorf("effectsink: %w: %v", ErrMalformedView, err)
	}
	return nil
}

// Verdict 是一次 pre-mutation 独立 recheck 的确定性结论。形状纪律：
//
//   - 结构性问题（intent 篡改/畸形、CurrentView 形态非法）只走 error
//     通道，VerifyBeforeEffect 返回 nil Verdict；
//   - 生命周期拒绝只经 Verdict{OK:false, Reason:...} 表达，error 为 nil；
//   - 全部通过 → Verdict{OK:true}；
//   - AlreadyExecuted 仅由 ExecuteIfAdmitted 在重放同 digest 的已执行
//     意图时置位（幂等返回，不产生第二次外部效果声明）。
type Verdict struct {
	OK              bool
	Reason          RejectionReason // 仅当 OK == false 时有值
	AlreadyExecuted bool            // 仅由 ExecuteIfAdmitted 重放路径置位
}

// VerifyBeforeEffect 在外部效果（mutation/secret use）发生之前对
// EffectIntent 做强制的 current-ledger 独立 recheck。
//
// 冻结的评估顺序（首个失败即返回）：
//
//  1. intent.Validate()——篡改或畸形 → error；
//  2. view.Validate()——形态非法 → error ErrMalformedView；
//  3. view.AuthorizationRevoked → authorization-revoked（revoke→effect
//     竞态的核心防线：意图在授权撤销后到达，永不执行）；
//  4. view.CurrentGeneration != intent.Generation → generation-stale；
//  5. view.CurrentFencingToken != intent.FencingToken → fencing-mismatch；
//  6. view.CurrentAuthorizationDigest != intent.AuthorizationDigest →
//     authorization-superseded；
//  7. view.CurrentTargetDigest != intent.TargetDigest → target-drifted。
//
// 步骤 1–2 是结构性校验，只走 error 通道；步骤 3–7 是生命周期拒绝，
// 只经 Verdict 表达且 error 为 nil，两通道互不泄漏。全部通过 →
// Verdict{OK:true}, nil。
func VerifyBeforeEffect(intent EffectIntent, view CurrentView) (*Verdict, error) {
	if err := intent.Validate(); err != nil {
		return nil, err
	}
	if err := view.Validate(); err != nil {
		return nil, err
	}
	if view.AuthorizationRevoked {
		return &Verdict{OK: false, Reason: RejectionReasonAuthorizationRevoked}, nil
	}
	if view.CurrentGeneration != intent.Generation {
		return &Verdict{OK: false, Reason: RejectionReasonGenerationStale}, nil
	}
	if view.CurrentFencingToken != intent.FencingToken {
		return &Verdict{OK: false, Reason: RejectionReasonFencingMismatch}, nil
	}
	if view.CurrentAuthorizationDigest != intent.AuthorizationDigest {
		return &Verdict{OK: false, Reason: RejectionReasonAuthorizationSuperseded}, nil
	}
	if view.CurrentTargetDigest != intent.TargetDigest {
		return &Verdict{OK: false, Reason: RejectionReasonTargetDrifted}, nil
	}
	return &Verdict{OK: true}, nil
}

// EffectLedger 是已执行效果的账本：按 IdempotencyKey put-if-absent，
// 并发安全、无时钟依赖。账本只承载"该幂等键的效果已执行"这一事实与
// 重放一致性判定，不实现 generation、fencing 或授权本身。
type EffectLedger struct {
	mu    sync.Mutex
	byKey map[string]EffectIntent
}

// NewEffectLedger 构造空账本。
func NewEffectLedger() *EffectLedger {
	return &EffectLedger{byKey: make(map[string]EffectIntent)}
}

// MarkExecuted 校验并记录一条已执行效果，fail closed：
//
//  1. intent.Validate()——意图必须自洽（篡改/畸形 → error）；
//  2. put-if-absent：同 IdempotencyKey 且 IntentDigest 一致的重放幂等
//     成功；同 key 不同 IntentDigest 即 ErrEffectConflict，账本永不
//     覆盖、永不双执行。
func (l *EffectLedger) MarkExecuted(intent EffectIntent) error {
	if l == nil {
		return fmt.Errorf("effectsink: %w", ErrNilLedger)
	}
	if err := intent.Validate(); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.byKey[intent.IdempotencyKey]; ok {
		if existing.IntentDigest == intent.IntentDigest {
			return nil
		}
		return fmt.Errorf("effectsink: %w: idempotency key %q already executed with different IntentDigest (%q→%q)",
			ErrEffectConflict, intent.IdempotencyKey, existing.IntentDigest, intent.IntentDigest)
	}
	l.byKey[intent.IdempotencyKey] = intent
	return nil
}

// Executed 按 IdempotencyKey 读取既有已执行意图（只读，不产生任何
// 效果声明）。
func (l *EffectLedger) Executed(key string) (EffectIntent, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i, ok := l.byKey[key]
	return i, ok
}

// ExecuteIfAdmitted 是收敛证明的组合门禁：先执行 VerifyBeforeEffect；
// 仅在 OK 时查账本——
//
//  1. VerifyBeforeEffect：error 原样上抛；生命周期拒绝原样返回
//     Verdict（不记录任何执行）；
//  2. 同 IdempotencyKey 同 IntentDigest 的已执行记录 →
//     Verdict{OK:true, AlreadyExecuted:true} 幂等返回（不重复
//     记录，不产生第二次外部效果声明）；
//  3. 同 key 不同 IntentDigest → error ErrEffectConflict，fail closed，
//     账本保持首次内容不变；
//  4. 无既有记录 → MarkExecuted 后 Verdict{OK:true}。
//
// 冻结语义：VerifyBeforeEffect 先于账本重放判定，授权一旦撤销，即使
// 重放已执行过的意图同样被拒绝——revocation 竞态永不产生双重效果。
func ExecuteIfAdmitted(l *EffectLedger, intent EffectIntent, view CurrentView) (*Verdict, error) {
	if l == nil {
		return nil, fmt.Errorf("effectsink: %w", ErrNilLedger)
	}
	verdict, err := VerifyBeforeEffect(intent, view)
	if err != nil {
		return nil, err
	}
	if !verdict.OK {
		return verdict, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.byKey[intent.IdempotencyKey]; ok {
		if existing.IntentDigest == intent.IntentDigest {
			return &Verdict{OK: true, AlreadyExecuted: true}, nil
		}
		return nil, fmt.Errorf("effectsink: %w: idempotency key %q already executed with different IntentDigest (%q→%q)",
			ErrEffectConflict, intent.IdempotencyKey, existing.IntentDigest, intent.IntentDigest)
	}
	l.byKey[intent.IdempotencyKey] = intent
	return &Verdict{OK: true}, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

// validateIntentIdentity 校验全部身份字段（不含 IntentDigest）。构造与
// Validate 共用同一份规则，保证派生 digest 前字段形态已被钉死。
func validateIntentIdentity(i EffectIntent) error {
	if strings.TrimSpace(i.IntentID) == "" {
		return fmt.Errorf("effectsink: %w: IntentID must not be empty", ErrMalformedIntent)
	}
	switch i.Sink {
	case SinkKindSCMMutation, SinkKindArtifactWrite, SinkKindSecretUse, SinkKindOtherEffect:
	default:
		return fmt.Errorf("effectsink: %w: %q", ErrUnknownSinkKind, string(i.Sink))
	}
	if strings.TrimSpace(i.TargetID) == "" {
		return fmt.Errorf("effectsink: %w: TargetID must not be empty", ErrMalformedIntent)
	}
	if strings.TrimSpace(i.IdempotencyKey) == "" {
		return fmt.Errorf("effectsink: %w: IdempotencyKey must not be empty", ErrMalformedIntent)
	}
	if i.Generation <= 0 {
		return fmt.Errorf("effectsink: %w: Generation must be strictly positive", ErrMalformedIntent)
	}
	if strings.TrimSpace(i.FencingToken) == "" {
		return fmt.Errorf("effectsink: %w: FencingToken must not be empty", ErrMalformedIntent)
	}
	if err := requireDigest("EffectIntent.AuthorizationDigest", i.AuthorizationDigest); err != nil {
		return fmt.Errorf("effectsink: %w: %v", ErrMalformedIntent, err)
	}
	if err := requireDigest("EffectIntent.TargetDigest", i.TargetDigest); err != nil {
		return fmt.Errorf("effectsink: %w: %v", ErrMalformedIntent, err)
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
