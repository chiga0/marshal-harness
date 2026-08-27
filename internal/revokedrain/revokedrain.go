package revokedrain

import (
	"errors"
	"fmt"
	"time"
)

// DispositionClass 枚举失效处置分级。
type DispositionClass string

const (
	DispositionClassSecurityCritical DispositionClass = "security-critical"
	DispositionClassPlannedUpgrade   DispositionClass = "planned-upgrade"
	DispositionClassOrdinaryUpgrade  DispositionClass = "ordinary-upgrade"
)

// RevokeReasonCode 枚举机器可读失效原因。
type RevokeReasonCode string

const (
	RevokeReasonCodeCredentialCompromise RevokeReasonCode = "credential-compromise"
	RevokeReasonCodeProtocolViolation    RevokeReasonCode = "protocol-violation"
	RevokeReasonCodeIncompatibleUpgrade  RevokeReasonCode = "incompatible-upgrade"
	RevokeReasonCodePlannedUpgrade       RevokeReasonCode = "planned-upgrade"
)

var validReasonCodes = map[RevokeReasonCode]struct{}{
	RevokeReasonCodeCredentialCompromise: {},
	RevokeReasonCodeProtocolViolation:    {},
	RevokeReasonCodeIncompatibleUpgrade:  {},
	RevokeReasonCodePlannedUpgrade:       {},
}

// DispositionEvent 记录一次分级处置事件。
type DispositionEvent struct {
	Sequence   int
	Class      DispositionClass
	ReasonCode RevokeReasonCode
	Operation  string
	Generation int64
	Narrative  string
}

// DrainPolicy 描述有界 drain 策略。
type DrainPolicy struct {
	DrainWindow   time.Duration
	MaxExtensions int
}

// NewDrainPolicy 构造 DrainPolicy；非法值 fail closed。
func NewDrainPolicy(drainWindow time.Duration, maxExtensions int) (*DrainPolicy, error) {
	if drainWindow <= 0 {
		return nil, fmt.Errorf("revokedrain: %w: drain window must be positive", ErrInvalidDrainPolicy)
	}
	if maxExtensions < 0 {
		return nil, fmt.Errorf("revokedrain: %w: max extensions must be non-negative", ErrInvalidDrainPolicy)
	}
	return &DrainPolicy{
		DrainWindow:   drainWindow,
		MaxExtensions: maxExtensions,
	}, nil
}

// InstanceGuard 承载单个 registration 实例的分级失效处置状态。
type InstanceGuard struct {
	registrationID string
	generation     int64

	drainDeadline  time.Time
	drainWindow    time.Duration
	maxExtensions  int
	extensionsUsed int

	class      DispositionClass
	reasonCode RevokeReasonCode

	events  []DispositionEvent
	nextSeq int

	leaseDigest string
	digestSet   bool

	upgradeStarted          bool
	upgradeTarget           string
	securityCriticalRevoked bool
	fenced                  bool
}

// Sentinel errors；所有外部可见错误均带 revokedrain: 前缀。
var (
	ErrInvalidDrainPolicy  = errors.New("invalid drain policy")
	ErrInvalidRegistration = errors.New("invalid registration")
	ErrInvalidReasonCode   = errors.New("invalid reason code")
	ErrRevoked             = errors.New("security-critical revoke active")
	ErrStopNew             = errors.New("not accepting new leases")
	ErrDrainExpired        = errors.New("drain deadline expired")
	ErrExtensionsExhausted = errors.New("drain extensions exhausted")
	ErrFenced              = errors.New("instance fenced")
	ErrReactivateDenied    = errors.New("reactivation denied")
	ErrDigestImmutable     = errors.New("lease digest already set")
)

// NewInstanceGuard 构造 InstanceGuard；畸形输入 fail closed。
func NewInstanceGuard(registrationID string, generation int64) (*InstanceGuard, error) {
	if registrationID == "" {
		return nil, fmt.Errorf("revokedrain: %w: registration id must be non-empty", ErrInvalidRegistration)
	}
	if generation <= 0 {
		return nil, fmt.Errorf("revokedrain: %w: generation must be positive", ErrInvalidRegistration)
	}
	return &InstanceGuard{
		registrationID: registrationID,
		generation:     generation,
		nextSeq:        1,
	}, nil
}

// RegistrationID 返回当前 registration id。
func (g *InstanceGuard) RegistrationID() string { return g.registrationID }

// Generation 返回当前 generation。
func (g *InstanceGuard) Generation() int64 { return g.generation }

// DrainDeadline 返回当前 drain deadline（零值表示无 drain 窗口）。
func (g *InstanceGuard) DrainDeadline() time.Time { return g.drainDeadline }

// Events 返回已记录事件的只读快照。
func (g *InstanceGuard) Events() []DispositionEvent {
	out := make([]DispositionEvent, len(g.events))
	copy(out, g.events)
	return out
}

// LeaseDigest 返回已设置的 lease digest（未设置时为空）。
func (g *InstanceGuard) LeaseDigest() string { return g.leaseDigest }

// ApplySecurityCriticalRevoke 立即生效安全关键撤销：此后新 lease 与在途完成均 fail closed。
func (g *InstanceGuard) ApplySecurityCriticalRevoke(reasonCode RevokeReasonCode, now time.Time) error {
	if _, ok := validReasonCodes[reasonCode]; !ok {
		return fmt.Errorf("revokedrain: %w: %q", ErrInvalidReasonCode, reasonCode)
	}
	if g.securityCriticalRevoked {
		return fmt.Errorf("revokedrain: %w", ErrRevoked)
	}

	g.class = DispositionClassSecurityCritical
	g.reasonCode = reasonCode
	g.securityCriticalRevoked = true
	g.upgradeStarted = false
	g.fenced = true
	g.drainDeadline = time.Time{}
	g.extensionsUsed = 0

	g.recordEvent("cancel", fmt.Sprintf("security-critical revoke: %s", reasonCode))
	g.generation++
	g.recordEvent("generation-bump", fmt.Sprintf("generation bumped to %d", g.generation))
	g.recordEvent("kill", "instance killed immediately")
	return nil
}

// StartUpgrade 启动升级 drain：停止接纳新 lease，并在有界窗口内允许在途完成。
func (g *InstanceGuard) StartUpgrade(newRegistrationID string, policy *DrainPolicy, now time.Time) error {
	if newRegistrationID == "" {
		return fmt.Errorf("revokedrain: %w: new registration id must be non-empty", ErrInvalidRegistration)
	}
	if policy == nil {
		return fmt.Errorf("revokedrain: %w: drain policy required", ErrInvalidDrainPolicy)
	}
	if g.securityCriticalRevoked {
		return fmt.Errorf("revokedrain: %w", ErrRevoked)
	}
	if g.fenced {
		return fmt.Errorf("revokedrain: %w", ErrFenced)
	}
	if g.upgradeStarted {
		return fmt.Errorf("revokedrain: upgrade already started")
	}

	g.upgradeStarted = true
	g.upgradeTarget = newRegistrationID
	g.drainDeadline = now.Add(policy.DrainWindow)
	g.drainWindow = policy.DrainWindow
	g.maxExtensions = policy.MaxExtensions
	g.extensionsUsed = 0
	g.class = DispositionClassPlannedUpgrade
	g.reasonCode = RevokeReasonCodePlannedUpgrade

	g.recordEvent("stop-new", fmt.Sprintf("upgrade target: %s", newRegistrationID))
	return nil
}

// ExtendDrain 在允许范围内追加一个完整 drain 窗口。
func (g *InstanceGuard) ExtendDrain(now time.Time) error {
	if g.securityCriticalRevoked {
		return fmt.Errorf("revokedrain: %w", ErrRevoked)
	}
	if g.fenced {
		return fmt.Errorf("revokedrain: %w", ErrFenced)
	}
	if !g.upgradeStarted {
		return fmt.Errorf("revokedrain: no active drain")
	}
	if now.After(g.drainDeadline) {
		return fmt.Errorf("revokedrain: %w", ErrDrainExpired)
	}
	if g.extensionsUsed >= g.maxExtensions {
		return fmt.Errorf("revokedrain: %w", ErrExtensionsExhausted)
	}

	g.drainDeadline = g.drainDeadline.Add(g.drainWindow)
	g.extensionsUsed++
	g.recordEvent("extension", fmt.Sprintf("extended to %s (%d used)", g.drainDeadline, g.extensionsUsed))
	return nil
}

// AcceptNewLease 判断是否允许接纳新 lease。
func (g *InstanceGuard) AcceptNewLease(now time.Time) error {
	if g.securityCriticalRevoked {
		return fmt.Errorf("revokedrain: %w", ErrRevoked)
	}
	if g.fenced {
		return fmt.Errorf("revokedrain: %w", ErrFenced)
	}
	if g.upgradeStarted {
		return fmt.Errorf("revokedrain: %w", ErrStopNew)
	}
	return nil
}

// AcceptInFlightCompletion 判断在途完成是否被接受；首次超期触发 fence。
func (g *InstanceGuard) AcceptInFlightCompletion(now time.Time) error {
	if g.securityCriticalRevoked {
		return fmt.Errorf("revokedrain: %w", ErrRevoked)
	}
	if g.fenced {
		return fmt.Errorf("revokedrain: %w", ErrFenced)
	}
	if !g.upgradeStarted {
		return nil
	}
	if now.After(g.drainDeadline) {
		g.fence()
		return fmt.Errorf("revokedrain: %w", ErrDrainExpired)
	}
	return nil
}

// Reactivate 尝试复活旧 registration；在 fence 或 security-critical revoke 后 fail closed。
func (g *InstanceGuard) Reactivate(registrationID string, now time.Time) error {
	if registrationID == "" {
		return fmt.Errorf("revokedrain: %w: registration id must be non-empty", ErrInvalidRegistration)
	}
	if g.securityCriticalRevoked {
		return fmt.Errorf("revokedrain: %w", ErrReactivateDenied)
	}
	if g.fenced {
		return fmt.Errorf("revokedrain: %w", ErrReactivateDenied)
	}
	if g.upgradeStarted {
		return fmt.Errorf("revokedrain: %w", ErrReactivateDenied)
	}
	if registrationID != g.registrationID {
		return fmt.Errorf("revokedrain: %w", ErrReactivateDenied)
	}
	g.recordEvent("reactivate", fmt.Sprintf("reactivated %s", registrationID))
	return nil
}

// SetLeaseDigest 设置 lease digest，仅允许一次。
func (g *InstanceGuard) SetLeaseDigest(digest string) error {
	if g.digestSet {
		return fmt.Errorf("revokedrain: %w", ErrDigestImmutable)
	}
	if digest == "" {
		return fmt.Errorf("revokedrain: lease digest must be non-empty")
	}
	g.leaseDigest = digest
	g.digestSet = true
	return nil
}

func (g *InstanceGuard) fence() {
	if g.fenced {
		return
	}
	g.recordEvent("cancel", fmt.Sprintf("drain deadline exceeded at %s", g.drainDeadline))
	g.generation++
	g.recordEvent("generation-bump", fmt.Sprintf("generation bumped to %d", g.generation))
	g.fenced = true
	g.upgradeStarted = false
}

func (g *InstanceGuard) recordEvent(operation, narrative string) {
	g.events = append(g.events, DispositionEvent{
		Sequence:   g.nextSeq,
		Class:      g.class,
		ReasonCode: g.reasonCode,
		Operation:  operation,
		Generation: g.generation,
		Narrative:  narrative,
	})
	g.nextSeq++
}
