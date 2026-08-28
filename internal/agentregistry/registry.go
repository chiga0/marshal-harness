package agentregistry

import (
	"fmt"
	"sync"
)

// ── Registry (in-memory, deterministic) ──────────────────────────────────────

// Registry is the in-memory durable identity ledger for AgentProvider
// registrations and capability snapshots. It is safe for concurrent use.
type Registry struct {
	mu sync.Mutex

	// ledger is the durable append-only store of registrations + lifecycle
	// （R2 纵切）. 非空时 register/transition/Lookup 直接委托给账本——账本是
	// 唯一真相，不与 in-memory registrations 双写；崩溃/重启由 ledger 确定性
	// 重放恢复。nil 时为纯内存（测试与轻量场景）。
	ledger *AgentLedger

	// registrations is keyed by RegistrationID.
	registrations map[string]*AgentRegistration

	// byIdempotencyKey maps IdempotencyKey → RegistrationID for idempotent
	// registration. The value is the first registration with that key.
	byIdempotencyKey map[string]string

	// snapshots is keyed by SnapshotDigest.
	snapshots map[string]*AgentCapabilitySnapshot

	// activeSnapshot maps RegistrationID → SnapshotDigest for the active snapshot.
	activeSnapshot map[string]string
}

// NewRegistry returns an empty, ready-to-use Registry.
func NewRegistry() *Registry {
	return &Registry{
		registrations:    make(map[string]*AgentRegistration),
		byIdempotencyKey: make(map[string]string),
		snapshots:        make(map[string]*AgentCapabilitySnapshot),
		activeSnapshot:   make(map[string]string),
	}
}

// OpenDurableRegistry 打开一个由耐久 append-only 账本支撑（R2 纵切）的
// Registry：register/transition/Lookup 直接落到账本并回收恢复（崩溃/重启后
// 由 NewAgentLedger 确定性重放）。账本目录创建或恢复失败一律 fail closed。
// 快照（capability snapshot）由 adapter Probe 稳定派生，仍保留在内存并在
// re-AddSnapshot 时重建，不重复落账。
func OpenDurableRegistry(dir string) (*Registry, error) {
	ledger, err := NewAgentLedger(dir)
	if err != nil {
		return nil, err
	}
	return &Registry{
		ledger:           ledger,
		registrations:    make(map[string]*AgentRegistration),
		byIdempotencyKey: make(map[string]string),
		snapshots:        make(map[string]*AgentCapabilitySnapshot),
		activeSnapshot:   make(map[string]string),
	}, nil
}

// ── Register (idempotent) ─────────────────────────────────────────────────────

// Register records a new AgentRegistration in the ledger, enforcing idempotency
// on (IdempotencyKey, RequestDigest).
//
//   - Same IdempotencyKey + same RequestDigest: returns the existing registration
//     without creating a second entry (idempotent replay).
//   - Same IdempotencyKey + different RequestDigest: fails closed (conflict).
//   - Duplicate RegistrationID with a different IdempotencyKey: fails closed.
func (r *Registry) Register(reg AgentRegistration) (*AgentRegistration, error) {
	if err := reg.Validate(); err != nil {
		return nil, err
	}
	if r.ledger != nil {
		// 耐久账本路径：幂等、crash-recovery 由 AgentLedger 承担。
		return r.ledger.Register(reg)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Idempotency: check by IdempotencyKey first.
	if existingID, ok := r.byIdempotencyKey[reg.IdempotencyKey]; ok {
		existing := r.registrations[existingID]
		if existing.RequestDigest == reg.RequestDigest {
			// Identical (key, digest): idempotent replay.
			return existing, nil
		}
		// Same key, different content: conflict.
		return nil, fmt.Errorf("agentregistry: idempotency key %q reused with different RequestDigest (conflict)", reg.IdempotencyKey)
	}

	// Guard against RegistrationID collision from a different key.
	if _, exists := r.registrations[reg.RegistrationID]; exists {
		return nil, fmt.Errorf("agentregistry: RegistrationID %q already exists with a different IdempotencyKey", reg.RegistrationID)
	}

	stored := reg // copy
	r.registrations[reg.RegistrationID] = &stored
	r.byIdempotencyKey[reg.IdempotencyKey] = reg.RegistrationID
	return &stored, nil
}

// ── Lifecycle transitions ─────────────────────────────────────────────────────

// legalTransitions defines the closed set of allowed state transitions.
// Terminal states (revoked, replaced, expired) have no outgoing transitions.
var legalTransitions = map[LifecycleState]map[LifecycleState]struct{}{
	LifecycleStateActive: {
		LifecycleStateSuspended: {},
		LifecycleStateRevoked:   {},
		LifecycleStateReplaced:  {},
		LifecycleStateExpired:   {},
	},
	LifecycleStateSuspended: {
		LifecycleStateActive:  {},
		LifecycleStateRevoked: {},
		LifecycleStateExpired: {},
	},
	// Terminal states: no outgoing transitions (absent from map → fail closed).
}

func (r *Registry) transition(registrationID string, target LifecycleState) (*AgentRegistration, error) {
	if err := target.validate(); err != nil {
		return nil, err
	}
	if r.ledger != nil {
		// 耐久账本路径：生命周期迁移落账，终态/非法迁移一致 fail closed。
		return r.ledger.Transition(registrationID, target)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	reg, ok := r.registrations[registrationID]
	if !ok {
		return nil, fmt.Errorf("agentregistry: registration %q not found", registrationID)
	}
	allowed, ok := legalTransitions[reg.LifecycleState]
	if !ok {
		return nil, fmt.Errorf("agentregistry: state %q is terminal; transition to %q rejected", reg.LifecycleState, target)
	}
	if _, ok := allowed[target]; !ok {
		return nil, fmt.Errorf("agentregistry: illegal lifecycle transition %q → %q", reg.LifecycleState, target)
	}
	reg.LifecycleState = target
	return reg, nil
}

// Suspend transitions the registration to suspended (fail closed if illegal).
func (r *Registry) Suspend(registrationID string) (*AgentRegistration, error) {
	return r.transition(registrationID, LifecycleStateSuspended)
}

// Revoke transitions the registration to revoked (terminal; fail closed if illegal).
func (r *Registry) Revoke(registrationID string) (*AgentRegistration, error) {
	return r.transition(registrationID, LifecycleStateRevoked)
}

// Replace transitions the registration to replaced (terminal; fail closed if illegal).
func (r *Registry) Replace(registrationID string) (*AgentRegistration, error) {
	return r.transition(registrationID, LifecycleStateReplaced)
}

// Expire transitions the registration to expired (terminal; fail closed if illegal).
func (r *Registry) Expire(registrationID string) (*AgentRegistration, error) {
	return r.transition(registrationID, LifecycleStateExpired)
}

// Reactivate transitions a suspended registration back to active.
func (r *Registry) Reactivate(registrationID string) (*AgentRegistration, error) {
	return r.transition(registrationID, LifecycleStateActive)
}

// ── Lookup ────────────────────────────────────────────────────────────────────

// Lookup returns the AgentRegistration for the given RegistrationID, or an
// error if not found.
func (r *Registry) Lookup(registrationID string) (*AgentRegistration, error) {
	if r.ledger != nil {
		// 耐久账本路径：exact lookup，可直接读取跨进程恢复的注册。
		return r.ledger.Lookup(registrationID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	reg, ok := r.registrations[registrationID]
	if !ok {
		return nil, fmt.Errorf("agentregistry: registration %q not found", registrationID)
	}
	return reg, nil
}

// ── Snapshot management ───────────────────────────────────────────────────────

// AddSnapshot stores a capability snapshot and, if the snapshot is active,
// records it as the active snapshot for its RegistrationID.
// The snapshot's RegistrationID must exist in the ledger.
func (r *Registry) AddSnapshot(snap AgentCapabilitySnapshot) (*AgentCapabilitySnapshot, error) {
	if err := snap.Validate(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.registrations[snap.RegistrationID]; !ok {
		return nil, fmt.Errorf("agentregistry: registration %q not found; cannot add snapshot", snap.RegistrationID)
	}
	stored := snap
	r.snapshots[snap.SnapshotDigest] = &stored
	if snap.SnapshotState == SnapshotStateActive {
		r.activeSnapshot[snap.RegistrationID] = snap.SnapshotDigest
	}
	return &stored, nil
}

// ActiveSnapshot returns the active AgentCapabilitySnapshot for the given
// RegistrationID, or an error if none exists or if the snapshot is not active.
func (r *Registry) ActiveSnapshot(registrationID string) (*AgentCapabilitySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	digest, ok := r.activeSnapshot[registrationID]
	if !ok {
		return nil, fmt.Errorf("agentregistry: no active snapshot for registration %q", registrationID)
	}
	snap, ok := r.snapshots[digest]
	if !ok || snap.SnapshotState != SnapshotStateActive {
		return nil, fmt.Errorf("agentregistry: active snapshot for registration %q is no longer active", registrationID)
	}
	return snap, nil
}
